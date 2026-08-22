package audit

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"time"
)

// RawRecord é a entrada da ingestão ANTES da redação: leva os metadados de
// responsabilização (Record) MAIS o payload pessoal em claro (PII) e o seu titular
// (SubjectID). A ingestão cifra a PII sob a chave do titular, sela apenas a
// referência ([PayloadRef]) na cadeia e DESCARTA o claro — que nunca entra no
// AuditRecord nem, logo, no conteúdo canónico selado.
//
// Os campos de Record são só metadados (quem/o quê/quando/resultado); o produtor
// NÃO deve colocar PII neles — a PII viaja exclusivamente em PII, para ser cifrada.
type RawRecord struct {
	// Record são os metadados de responsabilização (sem PII in-line).
	Record AuditRecord
	// SubjectID é o titular do payload pessoal (obrigatório se PII != nil).
	SubjectID string
	// PII é o payload pessoal em claro. Se vazio, não há payload a redigir e o
	// registo é selado sem PayloadRef.
	PII []byte
}

// IngestPipeline é o ponto de entrada COESO do audit tamper-evident (AOS-083):
//
//	Ingest(raw) → redigir PII (cifrar sob chave por-titular + PayloadRef + tokenizar)
//	           → Append (hash-chain, WORM) → [Seal periódico assinado]
//
// A PII em claro NUNCA entra na cadeia: só o ciphertext (fora da cadeia, no
// [PayloadStore]) e a referência selada [PayloadRef]{ContentHash, KeyRef, SubjectID}.
// A retenção/legal hold governam a eliminação a jusante (ver [Shredder]).
type IngestPipeline struct {
	store    Store
	vault    KeyVault
	payloads PayloadStore
	rand     RandSource
	signer   *Signer
	index    SubjectPartitionIndex
}

// IngestOption configura o [IngestPipeline].
type IngestOption func(*IngestPipeline)

// WithIngestRand injecta a fonte de entropia (determinística em teste). Por
// omissão usa crypto/rand.
func WithIngestRand(r RandSource) IngestOption {
	return func(p *IngestPipeline) {
		if r != nil {
			p.rand = r
		}
	}
}

// WithIngestSigner liga um [Signer] ao pipeline, habilitando [IngestPipeline.Seal]
// (selagem periódica assinada) como parte do mesmo ponto de entrada. A chave
// privada vive FORA do repo (KMS/HSM); os testes usam pares efémeros.
func WithIngestSigner(s *Signer) IngestOption {
	return func(p *IngestPipeline) { p.signer = s }
}

// WithIngestSubjectIndex liga um [SubjectPartitionIndex] que a ingestão alimenta a
// cada registo com PII (titular → partição). É o que torna EXECUTÁVEL o legal hold
// por-partição no [Shredder]: sem ele ligado aqui E no shredder, só o hold
// por-titular é feito valer. Ligar o MESMO índice a ambos.
func WithIngestSubjectIndex(idx SubjectPartitionIndex) IngestOption {
	return func(p *IngestPipeline) { p.index = idx }
}

// NewIngestPipeline constrói o pipeline sobre o [Store] (hash-chain WORM), o
// [KeyVault] (chaves por titular) e o [PayloadStore] (ciphertext fora da cadeia).
func NewIngestPipeline(store Store, vault KeyVault, payloads PayloadStore, opts ...IngestOption) *IngestPipeline {
	p := &IngestPipeline{
		store:    store,
		vault:    vault,
		payloads: payloads,
		rand:     cryptoRand,
	}
	for _, o := range opts {
		o(p)
	}
	if p.rand == nil {
		p.rand = cryptoRand
	}
	return p
}

// Ingest redige a PII (se presente) e sela o registo na hash-chain. Passos:
//
//  1. se há PII, exige SubjectID, provisiona a KEK do titular ([KeyVault.EnsureKey]),
//     cifra a PII por envelope ([sealPayload]), guarda o ciphertext no [PayloadStore]
//     e substitui o claro por uma [PayloadRef]{ContentHash, KeyRef, SubjectID} — o
//     TOKEN que referencia o payload cifrado sem o expor;
//  2. faz [Store.Append] do registo redigido (hash-chain, WORM).
//
// O claro (raw.PII) é descartado ao retornar; nunca é atribuído a nenhum campo do
// AuditRecord, pelo que não entra no conteúdo canónico selado. Redação determinista:
// com a mesma RandSource, a mesma entrada produz o mesmo ciphertext, hash e token.
func (p *IngestPipeline) Ingest(ctx context.Context, raw RawRecord) (AuditRecord, error) {
	rec := raw.Record
	// Blindagem: um produtor não pode fornecer um PayloadRef pré-fabricado — a
	// referência é SEMPRE derivada da cifra da PII nesta etapa (ou ausente).
	rec.PayloadRef = nil

	if len(raw.PII) > 0 {
		if raw.SubjectID == "" {
			return AuditRecord{}, ErrNoSubject
		}
		key, keyRef, err := p.vault.EnsureKey(raw.SubjectID)
		if err != nil {
			return AuditRecord{}, err
		}
		enc, err := sealPayload(key, raw.PII, p.rand)
		if err != nil {
			return AuditRecord{}, err
		}
		blob, contentHash, err := marshalPayload(enc)
		if err != nil {
			return AuditRecord{}, err
		}
		// O ciphertext vive FORA da cadeia, indexado pelo hex do ContentHash.
		p.payloads.Put(hex.EncodeToString(contentHash), blob)
		// TOKENIZAÇÃO: o claro é substituído pela referência selável.
		rec.PayloadRef = &PayloadRef{
			ContentHash: contentHash,
			KeyRef:      keyRef,
			SubjectID:   raw.SubjectID,
		}
		// Regista o titular na partição do registo, para o legal hold por-partição
		// ser executável no shred (ver [SubjectPartitionIndex]).
		if p.index != nil {
			p.index.Link(raw.SubjectID, rec.Partition)
		}
	}
	return p.store.Append(ctx, rec)
}

// Recover decifra o payload pessoal referenciado por ref, provando a
// recuperabilidade ANTES do shred. Fail-closed a jusante do crypto-shredding:
//
//   - se a chave foi destruída ([Shredder.Shred]) → [ErrShredded] (PII irrecuperável);
//   - se o ciphertext não existe no [PayloadStore] → [ErrPayloadMissing];
//   - se a KEK/blob não autenticam (adulteração) → [ErrDecrypt].
//
// A cadeia continua a verificar em qualquer destes casos — Recover opera FORA dela.
func (p *IngestPipeline) Recover(ref PayloadRef) ([]byte, error) {
	if ref.KeyRef == "" || len(ref.ContentHash) == 0 {
		return nil, ErrPayloadMissing
	}
	blob, ok := p.payloads.Get(hex.EncodeToString(ref.ContentHash))
	if !ok {
		return nil, ErrPayloadMissing
	}
	key, ok := p.vault.Key(ref.KeyRef)
	if !ok {
		return nil, ErrShredded
	}
	var enc encryptedPayload
	if err := json.Unmarshal(blob, &enc); err != nil {
		return nil, ErrDecrypt
	}
	return openPayload(key, enc)
}

// Seal sela periodicamente a cadeia da partição no audit_seq dado, produzindo um
// [Checkpoint] assinado — a etapa final do pipeline. Exige um [Signer] ligado por
// [WithIngestSigner]; caso contrário devolve [ErrNoSigner].
func (p *IngestPipeline) Seal(ctx context.Context, partition string, seq uint64) (Checkpoint, error) {
	if p.signer == nil {
		return Checkpoint{}, ErrNoSigner
	}
	return p.signer.Seal(ctx, p.store, partition, seq)
}

// Shredder executa o crypto-shredding por titular (GDPR Art. 17) governado por
// retenção e legal hold. Destruir a chave do titular torna a PII irrecuperável SEM
// mutar a cadeia (que selou o hash do ciphertext) — "imutável = íntegro, não eterno".
type Shredder struct {
	vault     KeyVault
	holds     *LegalHold
	retention RetentionPolicy
	index     SubjectPartitionIndex
}

// ShredderOption configura o [Shredder].
type ShredderOption func(*Shredder)

// WithShredderSubjectIndex liga o [SubjectPartitionIndex] que o shred consulta para
// fazer valer o legal hold POR PARTIÇÃO: se qualquer partição que contém dados do
// titular estiver retida, o shred é recusado (fail-closed). Ligar o MESMO índice
// que alimenta o pipeline ([WithIngestSubjectIndex]); sem ele, só o hold
// por-titular é executável.
func WithShredderSubjectIndex(idx SubjectPartitionIndex) ShredderOption {
	return func(s *Shredder) { s.index = idx }
}

// NewShredder constrói o shredder sobre o [KeyVault] a partir do qual apaga chaves,
// com o [LegalHold] e a [RetentionPolicy] que governam a eliminação. holds nil é
// tratado como "sem retenções activas" (mas passar um legal hold é o esperado em
// produção). Ligue [WithShredderSubjectIndex] para o hold por-partição ser
// executável.
func NewShredder(vault KeyVault, holds *LegalHold, retention RetentionPolicy, opts ...ShredderOption) *Shredder {
	s := &Shredder{vault: vault, holds: holds, retention: retention}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Shred satisfaz um DSAR (direito ao apagamento) destruindo a chave por titular.
// FAIL-CLOSED: se o titular estiver sob legal hold — por-titular OU por qualquer
// partição onde tem dados selados ([SubjectPartitionIndex]) — NADA é destruído e
// devolve [ErrLegalHold]. Um dado sob obrigação de preservação não pode ser
// shredded, mesmo a pedido do titular. Sem hold, apaga a KEK (idempotente); a PII
// passa a irrecuperável e a cadeia continua a verificar.
func (s *Shredder) Shred(subjectID string) error {
	if subjectID == "" {
		return ErrNoSubject
	}
	// BARREIRA DE DESTRUIÇÃO (ver [LegalHold.BeginDestruction]). Aqui a janela `held`→`Delete` é
	// de microssegundos — não há `fsync` pelo meio, ao contrário do varredor. Toma-se na mesma
	// para que a invariante seja UMA e não duas: um 200 do /dsar/hold significa que nenhuma
	// destruição posterior deixa de ver este hold, venha ela do varredor ou do /dsar/erase. Uma
	// invariante com uma excepção é uma invariante que ninguém consegue citar.
	defer s.holds.BeginDestruction()()

	if s.held(subjectID) {
		return ErrLegalHold
	}
	s.vault.Delete(subjectID)
	return nil
}

// Held expõe, como QUERY pública, se o titular está sob legal hold (por-titular OU
// por qualquer partição onde tem dados selados) segundo as MESMAS regras
// fail-closed do [Shredder.Shred]. Existe para um orquestrador de DSAR (AOS-093)
// consultar a preservação ANTES de iniciar o apagamento em vários stores — sem
// duplicar a lógica de hold por-partição nem expor qualquer segredo. Não muta nada.
func (s *Shredder) Held(subjectID string) bool { return s.held(subjectID) }

// held indica se o titular está sob legal hold, seja por-titular ou por-partição
// (via o índice, se ligado). A KEK é destruída globalmente no shred, pelo que basta
// UMA partição retida com dados do titular para o bloquear inteiro (fail-closed).
func (s *Shredder) held(subjectID string) bool {
	if s.holds == nil {
		return false
	}
	if s.holds.HeldSubject(subjectID) {
		return true
	}
	if s.index != nil {
		for _, part := range s.index.Partitions(subjectID) {
			if s.holds.HeldPartition(part) {
				return true
			}
		}
		return false
	}
	// Sem índice ligado, o shredder NÃO consegue mapear o titular às suas partições.
	// Se existem holds por-partição em vigor, recusar o shred fail-closed (não é
	// possível provar que este titular não está sob preservação por-partição) —
	// evita o fail-open silencioso por wiring em falta. Sem holds por-partição, não
	// há nada a preservar por esta via e o shred procede.
	return s.holds.HasPartitionHolds()
}

// PurgeExpired é o purge AUTOMÁTICO por retenção: só shreda se a idade do dado
// ultrapassou a retenção da classe E o titular não estiver sob legal hold. Devolve
// [ErrRetentionActive] se ainda dentro do período, ou [ErrLegalHold] se retido —
// em ambos os casos NADA é destruído (fail-closed). O legal hold impede a
// eliminação MESMO depois de a retenção expirar.
func (s *Shredder) PurgeExpired(subjectID string, class DataClass, age time.Duration) error {
	if !s.retention.Expired(class, age) {
		return ErrRetentionActive
	}
	return s.Shred(subjectID)
}

// BeginDestruction delega a BARREIRA DE DESTRUIÇÃO do legal hold que este shredder faz valer, para
// que OUTROS stores shreddable do mesmo fluxo DSAR — que não têm o `*LegalHold` mas têm este
// oráculo — possam tomá-la com a mesma semântica.
//
// Existe porque a invariante do `POST /dsar/hold` é ABSOLUTA («nenhuma destruição posterior deixa
// de ver este hold») e um store que a não pudesse tomar seria uma excepção. Ver
// [dsar.RedactionStore].
//
// NÃO ANINHAR: quem chama isto NÃO pode estar dentro de outro `BeginDestruction`. Um `RLock`
// recursivo do mesmo `RWMutex` bloqueia para sempre assim que um escritor fique à espera. Os
// stores de um fluxo tomam-na em SEQUÊNCIA (um por iteração do ciclo), nunca encaixados.
func (s *Shredder) BeginDestruction() func() {
	if s == nil || s.holds == nil {
		return func() {}
	}
	return s.holds.BeginDestruction()
}
