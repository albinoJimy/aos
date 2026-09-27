package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// segmentPayload é o PLAINTEXT de um segmento antes da cifra: os eventos NOVOS
// (envelope intacto) por stream neste ciclo incremental. json.Marshal ordena as
// chaves de mapa, pelo que a serialização é determinística (reprodutível).
type segmentPayload struct {
	Streams map[string][]eventstore.Event `json:"streams"`
}

// Exporter exporta o Event Store para backup imutável de forma CONTÍNUA/
// INCREMENTAL. A cada ciclo ([Export]) lê os eventos novos (por stream, seq >
// último exportado), cifra-os em repouso, escreve-os como um segmento imutável no
// [ImmutableStore] e sela o head do manifesto hash-chain num [Checkpoint] assinado.
// Seguro para concorrência.
type Exporter struct {
	src      eventstore.BackupSource
	dst      ImmutableStore
	vault    audit.KeyVault
	signer   Signer
	rand     audit.RandSource
	subject  string
	policy   audit.RetentionPolicy
	retClass audit.DataClass

	periodicity time.Duration
	now         func() time.Time

	mu             sync.Mutex
	manifest       Manifest
	lastExported   map[string]uint64
	lastCheckpoint Checkpoint
	lastExportAt   time.Time
	started        bool

	// Estado de RETOMA (AOS-101, ver resume.go). Um exportador construído sobre um destino que já
	// tem cadeia continua-a a partir daqui, em vez de recomeçar do génesis. Num destino virgem os
	// três são o zero-value e o comportamento é exactamente o de antes.
	//
	// Estes campos existem porque o manifesto EM MEMÓRIA deixou de ser a cadeia toda: guarda só os
	// segmentos selados POR ESTE PROCESSO. Tudo o que antes se derivava de `len(manifest.Segments)`
	// e de `manifest.head()` passa por [Exporter.nextIndex]/[Exporter.chainHead], que somam esta
	// base.
	baseCycle uint64            // ciclos selados antes deste processo (0 ⇒ destino virgem)
	baseHead  []byte            // EntryHash do último elo selado antes deste processo
	baseHeads map[string]uint64 // StreamHeads cumulativos desse elo

	// pending é o registo de ciclo cuja escrita devolveu um erro que NÃO foi [ErrImmutable] — o
	// Put pode ter feito commit (timeout depois do commit) ou não. É a ÚNICA coisa que este
	// exportador adopta numa colisão futura: um registo autêntico que não seja exactamente este é
	// de outro escritor ([ErrChainOwned]), por mais que continue o nosso head.
	// São TODOS os que se tentaram para o próximo ciclo (uma re-tentativa ambígua pode ela própria
	// ser ambígua, e qualquer uma delas pode ter sido a que fez commit).
	pending []cycleRecord
}

// ExporterOption configura o [Exporter].
type ExporterOption func(*Exporter)

// WithKeyVault injecta o audit.KeyVault (KMS/Vault) que detém a KEK do backup. Por
// omissão usa um InMemoryKeyVault de referência com a RandSource configurada.
//
// AOS-453: se o vault implementar também [audit.KeyWrapper] (custódia key-never-leaves, p.ex.
// Vault Transit), os segmentos selam-se por ENVELOPE — a DEK é embrulhada dentro da custódia e a
// KEK nunca entra no processo. Uma custódia que nem entrega a KEK nem implementa o envelope é
// recusada na construção ([ErrKEKCustodyUnsupported]).
func WithKeyVault(v audit.KeyVault) ExporterOption { return func(e *Exporter) { e.vault = v } }

// WithRandSource injecta a fonte de entropia (determinística em testes).
func WithRandSource(r audit.RandSource) ExporterOption { return func(e *Exporter) { e.rand = r } }

// WithPeriodicity define a periodicidade-alvo do ciclo de exportação (base do RPO).
func WithPeriodicity(d time.Duration) ExporterOption {
	return func(e *Exporter) { e.periodicity = d }
}

// WithRetention define a política de object-lock/retenção dos segmentos (reutiliza
// audit.RetentionPolicy). class é a classe de retenção aplicada aos segmentos.
func WithRetention(policy audit.RetentionPolicy, class audit.DataClass) ExporterOption {
	return func(e *Exporter) { e.policy = policy; e.retClass = class }
}

// WithClock injecta o relógio (uso interno/testes determinísticos).
func WithClock(f func() time.Time) ExporterOption { return func(e *Exporter) { e.now = f } }

// NewExporter constrói um exportador para (src → dst), validando a soberania
// fail-closed (ADR-011): se o destino cruza a fronteira regional do board, ou não
// declara região, devolve [ErrSovereigntyViolation] — o backup NUNCA é criado
// cross-border. signer sela os checkpoints (chave privada fora do repo).
//
// # RETOMA (AOS-101)
//
// Se o destino já contiver uma cadeia, o exportador RETOMA-A em vez de recomeçar do génesis: sonda
// o último ciclo selado em O(log N), verifica-o fail-closed ([ErrResumeUnverifiable]) e continua no
// ciclo seguinte com o cursor incremental restaurado. Ver resume.go para o desenho e para o que a
// verificação cobre.
//
// A retoma é AUTOMÁTICA e não uma opção, porque a alternativa seria um interruptor que, esquecido,
// produziria exactamente o defeito que ela existe para fechar. Num destino virgem a sondagem não
// encontra nada e o comportamento é, byte a byte, o de sempre.
//
// Consequência para quem compõe: este construtor passou a fazer I/O ao destino. Um destino que não
// saiba responder ABORTA a construção — não se assume "virgem" um destino que só não respondeu,
// porque isso recomeçaria uma cadeia que já existe.
func NewExporter(src eventstore.BackupSource, dst ImmutableStore, signer Signer, opts ...ExporterOption) (*Exporter, error) {
	if src == nil || dst == nil || signer == nil {
		return nil, ErrConfig
	}
	if err := checkSovereignty(src.Region(), dst.Region()); err != nil {
		return nil, err
	}
	e := &Exporter{
		src:          src,
		dst:          dst,
		signer:       signer,
		retClass:     audit.ClassAudit,
		periodicity:  30 * time.Second,
		now:          time.Now,
		manifest:     Manifest{Region: normalizeRegion(dst.Region())},
		lastExported: make(map[string]uint64),
	}
	for _, o := range opts {
		o(e)
	}
	if e.rand == nil {
		e.rand = cryptoRand
	}
	if e.vault == nil {
		e.vault = audit.NewInMemoryKeyVault(e.rand)
	}
	// O titular da KEK do backup é derivado da região de soberania: uma chave por
	// fronteira, nunca partilhada entre regiões.
	e.subject = backupSubjectFor(dst.Region())
	// A retoma e a colisão entre dois donos assentam em write-once condicional. Um destino que não
	// o cumpra bifurcava em silêncio; prova-se aqui, antes de qualquer ciclo (ver resume.go).
	if err := probeConditionalPut(dst, e.manifest.Region, retainUntilFor(e.policy, e.retClass, e.now().UTC())); err != nil {
		return nil, err
	}
	// AOS-453: a custódia tem de saber selar segmentos — provado AQUI, e não ciclo a ciclo. E ANTES
	// da retoma: uma custódia em baixo não pode chegar à retoma e ler-se lá como «KEK errada».
	if err := checkKEKCustody(e.vault, e.subject, e.rand); err != nil {
		return nil, err
	}
	if err := e.resume(); err != nil {
		return nil, err
	}
	return e, nil
}

// resume adopta a cadeia que já exista no destino. Destino virgem ⇒ no-op.
func (e *Exporter) resume() error {
	last, err := lastSealedCycle(e.dst, e.manifest.Region)
	if err != nil {
		return fmt.Errorf("backup: sondagem de retoma no destino da regiao %q: %w", e.manifest.Region, err)
	}
	if last == 0 {
		return nil
	}
	if err := checkNoCyclesBeyond(e.dst, e.manifest.Region, last); err != nil {
		return err
	}
	rec, err := loadCycleRecord(e.dst, e.manifest.Region, last)
	if err != nil {
		return err
	}
	if err := verifyCycleRecord(e.signer.Public(), e.manifest.Region, last, rec); err != nil {
		return err
	}
	// A assinatura prova quem selou; não prova que ESTA custódia de chaves ainda decifra o que foi
	// selado. Sem isto a cadeia retomava, verificava, e não restaurava (ver resume.go).
	if err := checkLastSegmentOpens(e.dst, e.vault, e.subject, rec.Entry); err != nil {
		return err
	}
	// O que a retoma NÃO confere é se a cadeia é deste LOG: é o Export que recusa, sem escrever,
	// um log atrás do cursor ([ErrSourceBehindBackup]) — no arranque recusá-lo impediria o nó de
	// subir depois de um PITR ou de um DR.
	e.baseCycle = rec.Entry.Index
	e.baseHead = cloneBytes(rec.Entry.EntryHash)
	e.baseHeads = cloneHeads(rec.Entry.StreamHeads)
	e.lastCheckpoint = rec.Checkpoint
	// O cursor incremental É o StreamHeads do elo — autenticado pela assinatura, via o EntryHash
	// que canonicalSegment faz cobrir esse mapa.
	for st, h := range rec.Entry.StreamHeads {
		e.lastExported[st] = h
	}
	e.started = true
	return nil
}

// nextIndex é a posição do próximo elo na cadeia: os ciclos selados antes deste processo mais os
// selados por ele. NUNCA `len(manifest.Segments)+1` — num exportador retomado isso daria 1 e
// bifurcaria a cadeia.
func (e *Exporter) nextIndex() uint64 {
	return e.currentCycle() + 1
}

// currentCycle é o último ciclo SELADO nesta cadeia — os de antes deste processo mais os deste.
// Num exportador retomado, `len(manifest.Segments)` daria 0 e um ciclo sem novidade anunciaria
// «ciclo 0» sobre um backup com cadeia: o número que o operador lê ficaria a contar a vida do
// processo em vez da do backup.
func (e *Exporter) currentCycle() uint64 {
	return e.baseCycle + uint64(len(e.manifest.Segments))
}

// chainHead é o PrevHash do próximo elo: o último elo em memória, ou o elo retomado, ou o génesis
// da região — por esta ordem.
func (e *Exporter) chainHead() []byte {
	if n := len(e.manifest.Segments); n > 0 {
		return cloneBytes(e.manifest.Segments[n-1].EntryHash)
	}
	if e.baseHead != nil {
		return cloneBytes(e.baseHead)
	}
	return e.manifest.head()
}

// ResumedFrom devolve o ciclo a partir do qual este exportador retomou a cadeia do destino, ou 0
// se arrancou do génesis. Observacional — existe para o nó poder ANUNCIAR o que ligou, no molde do
// resto dos banners de postura: um exportador que retomou e um que recomeçou são coisas
// diferentes, e a única forma de as distinguir não pode ser esperar pelo primeiro ciclo.
func (e *Exporter) ResumedFrom() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.baseCycle
}

// ExportResult descreve o resultado de um ciclo de exportação.
type ExportResult struct {
	// Created indica se um segmento novo foi escrito (false ⇒ backup já em dia).
	Created bool
	// Cycle é o índice do último segmento selado (número de segmentos no manifesto).
	Cycle uint64
	// Ref é a referência do segmento escrito neste ciclo ("" se Created==false).
	Ref string
	// Events é o nº de eventos exportados neste ciclo.
	Events uint64
	// StreamHeads é o head cumulativo por stream após este ciclo.
	StreamHeads map[string]uint64
	// At é o instante do ciclo.
	At time.Time
	// Checkpoint é o checkpoint assinado do head após este ciclo.
	Checkpoint Checkpoint
}

// Export corre um ciclo incremental: captura os eventos committed ainda não
// exportados (por stream), cifra-os, escreve um segmento imutável e sela o head.
// Fail-closed: revalida a soberania a cada ciclo. Se nada mudou desde o último
// ciclo, não escreve segmento mas actualiza o relógio de RPO (o backup confirmou
// estar em dia com o head do Store).
func (e *Exporter) Export(ctx context.Context) (ExportResult, error) {
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	// Revalidação defensiva da fronteira (o destino não deve mudar de região, mas
	// nunca se assume — fail-closed).
	if err := checkSovereignty(e.src.Region(), e.dst.Region()); err != nil {
		return ExportResult{}, err
	}

	now := e.now().UTC()

	// 1) Recolhe os eventos novos por stream (incremental, gapless).
	//
	// AOS-352 — FAIL-CLOSED. Um export construido sobre uma enumeracao FALHADA seria um
	// backup que parece completo e nao e: os streams que nao foram vistos ficariam de
	// fora, o `lastExported` NAO avancaria para eles, mas o resultado seria devolvido como
	// sucesso. Um backup incompleto que se declara completo e pior do que nenhum — e a
	// resposta certa a uma falha de enumeracao e nao produzir export nenhum.
	streams, err := e.src.Streams()
	if err != nil {
		return ExportResult{}, fmt.Errorf("backup: enumerar streams da fonte: %w", err)
	}
	// Um stream que o cursor conhece e a fonte deixou de enumerar tem, para a fonte, head 0 —
	// abaixo do cursor. Ver o `head < from` abaixo.
	enumerados := make(map[string]struct{}, len(streams))
	for _, st := range streams {
		enumerados[st] = struct{}{}
	}
	for st, from := range e.lastExported {
		if _, ok := enumerados[st]; !ok && from > 0 {
			return ExportResult{}, fmt.Errorf("%w: o stream %q esta no cursor (head %d) e a fonte nao o enumera", ErrSourceBehindBackup, st, from)
		}
	}
	newByStream := make(map[string][]eventstore.Event)
	cumHeads := make(map[string]uint64)
	var total uint64
	for _, st := range streams {
		head, err := e.src.StreamHead(ctx, st)
		if err != nil {
			return ExportResult{}, err
		}
		cumHeads[st] = head
		from := e.lastExported[st]
		// `head < from` era impossível enquanto `from` só vinha dos ciclos deste processo. Desde a
		// retoma (AOS-101) `from` vem do DESTINO, e um log rebobinado — ou de outro nó — fica com o
		// head abaixo do cursor. Um `continue` aqui saltaria o stream em SILÊNCIO e, quando o head
		// voltasse a passar o cursor, exportaria uma história diferente por cima da que o backup já
		// tem. Fail-closed: nenhum segmento, e o erro nomeia o stream. O que fazer a seguir (um
		// destino novo para a cadeia nova) é decisão de operação, não deste ciclo.
		if head < from {
			return ExportResult{}, fmt.Errorf("%w: stream %q com head %d e cursor %d", ErrSourceBehindBackup, st, head, from)
		}
		if head == from {
			continue
		}
		evs, err := e.src.SnapshotStream(ctx, st, head)
		if err != nil {
			return ExportResult{}, err
		}
		// evs[i] tem seq i+1; os novos são os de índice >= from.
		newEvents := evs[from:]
		if len(newEvents) == 0 {
			continue
		}
		newByStream[st] = newEvents
		total += uint64(len(newEvents))
	}

	// 2) Nada novo: backup em dia. Actualiza o relógio de RPO e devolve.
	if total == 0 {
		e.lastExportAt = now
		return ExportResult{
			Created:     false,
			Cycle:       e.currentCycle(),
			StreamHeads: e.copyHeads(),
			At:          now,
			Checkpoint:  e.lastCheckpoint,
		}, nil
	}

	// 3) Serializa (determinístico) e cifra em repouso (envelope AES-256-GCM).
	plaintext, err := json.Marshal(segmentPayload{Streams: newByStream})
	if err != nil {
		return ExportResult{}, err
	}
	seg, err := sealSegment(e.vault, e.subject, plaintext, e.rand)
	if err != nil {
		return ExportResult{}, err
	}
	blob, err := marshalSegment(seg)
	if err != nil {
		return ExportResult{}, err
	}
	contentHash := sha256.Sum256(blob)

	// 4) Encadeia o elo. A ref do segmento é ENDEREÇADA POR CONTEÚDO (ver resume.go): é o que
	//    torna a re-tentativa de um ciclo interrompido idempotente em vez de permanentemente
	//    bloqueada. Os streams sem novidade herdam o head do elo anterior.
	index := e.nextIndex()
	ref := segmentRef(e.manifest.Region, index, contentHash[:])
	entryHeads := e.mergeHeads(cumHeads)
	entry := SegmentEntry{
		Index:       index,
		Ref:         ref,
		ContentHash: contentHash[:],
		Events:      total,
		StreamHeads: entryHeads,
		PrevHash:    e.chainHead(),
		CreatedAt:   now,
	}
	entry.EntryHash = computeEntryHash(entry.PrevHash, entry)
	cp := sealCheckpoint(e.signer, e.manifest.Region, index, entry.EntryHash, now)
	retainUntil := retainUntilFor(e.policy, e.retClass, now)

	// 5) Escreve o segmento IMUTÁVEL e, só depois, o registo de ciclo que o sela.
	//
	//    A ORDEM é deliberada e não é reversível: um registo de ciclo escrito primeiro apontaria,
	//    durante uma janela, para um segmento inexistente — e essa janela sobrevive a um crash,
	//    deixando um backup cuja verificação falha INTEIRA. Nesta ordem, um crash no meio deixa um
	//    segmento órfão: retido pelo object-lock, referenciado por nada, e sem efeito na cadeia,
	//    que simplesmente não avançou.
	if err := e.putSegment(ref, blob, contentHash[:], retainUntil); err != nil {
		return ExportResult{}, err
	}
	recBlob, err := json.Marshal(cycleRecord{Entry: entry, Checkpoint: cp})
	if err != nil {
		return ExportResult{}, err
	}
	if err := e.dst.Put(cycleRef(e.manifest.Region, index), recBlob, retainUntil); err != nil {
		if errors.Is(err, ErrImmutable) {
			// A ref do registo de ciclo é indexada de propósito: é AQUI que dois exportadores
			// sobre o mesmo destino se encontram. Mas a colisão tem TRÊS causas, e só uma é a
			// nossa — lê-se o registo antes de a nomear ([Exporter.resolveCycleCollision]).
			adoptado, aerr := e.resolveCycleCollision(index)
			if aerr != nil {
				return ExportResult{}, aerr
			}
			return e.adopt(adoptado), nil
		}
		// Escrita AMBÍGUA: pode ter feito commit. Guarda-se o que se tentou escrever, para que a
		// colisão da re-tentativa o possa reconhecer como nosso — e só a ele.
		if len(e.pending) > 0 && e.pending[0].Entry.Index != index {
			e.pending = nil
		}
		e.pending = append(e.pending, cycleRecord{Entry: entry, Checkpoint: cp})
		return ExportResult{}, err
	}
	e.pending = nil

	// 6) SÓ DEPOIS de o ciclo estar durável se muta o estado em memória. Ao contrário, um ciclo
	//    que falhasse a escrever deixaria o exportador a acreditar num elo que o destino não tem,
	//    e o ciclo seguinte encadearia a partir de um head que ninguém pode verificar.
	e.manifest.Segments = append(e.manifest.Segments, entry)
	for st, h := range cumHeads {
		if h > e.lastExported[st] {
			e.lastExported[st] = h
		}
	}
	e.lastCheckpoint = cp
	e.lastExportAt = now
	e.started = true

	return ExportResult{
		Created:     true,
		Cycle:       index,
		Ref:         ref,
		Events:      total,
		StreamHeads: cloneHeads(entryHeads),
		At:          now,
		Checkpoint:  cp,
	}, nil
}

// resolveCycleCollision decide o que é o registo que já ocupa a referência do ciclo index:
//
//   - não verifica com a nossa chave (assinatura, índice, região, elo) ⇒ [ErrCycleRecordInvalid]:
//     adulteração ou lixo no destino, e escala como tal;
//   - verifica e é EXACTAMENTE o registo que este exportador tentou escrever numa escrita ambígua
//     anterior (em [Exporter.pending]: o Put fez commit e devolveu erro) — mesmo EntryHash, logo o
//     mesmo segmento, o mesmo content-hash, o mesmo cursor e o mesmo head ⇒ é nosso, adopta-se; o
//     segmento deste ciclo fica órfão, como no ciclo interrompido;
//   - verifica e é QUALQUER outro ⇒ [ErrChainOwned]: outro escritor com a mesma chave. Continuar o
//     nosso head NÃO basta para o adoptar — num destino condicional isso é verdade para todos os
//     escritores reais, e adoptar o elo de outro log saltaria o cursor para o dele e perderia em
//     silêncio os eventos deste (medido pela revisão adversarial: A1,A2,B3,B4,B5 restaurados como
//     verificados, B1..B2 perdidos).
//
// Um registo que o Put diz existir e o Get não encontra devolve o erro do Get (o laço re-tenta).
func (e *Exporter) resolveCycleCollision(index uint64) (cycleRecord, error) {
	rec, err := loadCycleRecord(e.dst, e.manifest.Region, index)
	if err != nil {
		if errors.Is(err, ErrResumeUnverifiable) {
			return cycleRecord{}, fmt.Errorf("%w: ciclo %d na regiao %q: %v", ErrCycleRecordInvalid, index, e.manifest.Region, err)
		}
		return cycleRecord{}, err
	}
	if verr := verifyCycleRecord(e.signer.Public(), e.manifest.Region, index, rec); verr != nil {
		return cycleRecord{}, fmt.Errorf("%w: ciclo %d na regiao %q: %v", ErrCycleRecordInvalid, index, e.manifest.Region, verr)
	}
	for _, tentado := range e.pending {
		if tentado.Entry.Index == index && bytes.Equal(rec.Entry.EntryHash, tentado.Entry.EntryHash) {
			e.pending = nil
			return rec, nil
		}
	}
	return cycleRecord{}, fmt.Errorf("%w: ciclo %d na regiao %q", ErrChainOwned, index, e.manifest.Region)
}

// adopt incorpora a NOSSA escrita ambígua, que afinal está durável no destino. O relógio de RPO
// fica no instante em que esse elo foi selado (por este exportador), não em agora: os eventos que
// este ciclo tinha capturado para além dele seguem no próximo.
func (e *Exporter) adopt(rec cycleRecord) ExportResult {
	e.manifest.Segments = append(e.manifest.Segments, rec.Entry)
	for st, h := range rec.Entry.StreamHeads {
		if h > e.lastExported[st] {
			e.lastExported[st] = h
		}
	}
	e.lastCheckpoint = rec.Checkpoint
	e.lastExportAt = rec.Entry.CreatedAt
	e.started = true
	return ExportResult{
		Created:     true,
		Cycle:       rec.Entry.Index,
		Ref:         rec.Entry.Ref,
		Events:      rec.Entry.Events,
		StreamHeads: cloneHeads(rec.Entry.StreamHeads),
		At:          rec.Entry.CreatedAt,
		Checkpoint:  rec.Checkpoint,
	}
}

// putSegment escreve o segmento, tratando a colisão de uma ref endereçada por conteúdo como o que
// ela quase sempre é: a re-tentativa idempotente de um ciclo que morreu entre o segmento e o
// registo que o sela. O objecto que se queria escrever já lá está — escrever de novo seria pedir
// ao WORM que se contradisse.
//
// «Quase sempre» não chega, e por isso confirma-se: a ref só carrega um PREFIXO do content-hash, e
// aceitar por prefixo deixaria o manifesto a selar um hash e o destino a guardar outro blob — uma
// corrupção silenciosa que só apareceria no restauro, como [ErrSegmentTampered], sem ninguém saber
// porquê. Compara-se o hash INTEIRO, e o caminho custa um Get só na re-tentativa.
func (e *Exporter) putSegment(ref string, blob, contentHash []byte, retainUntil time.Time) error {
	err := e.dst.Put(ref, blob, retainUntil)
	if err == nil || !errors.Is(err, ErrImmutable) {
		return err
	}
	existing, gerr := e.dst.Get(ref)
	if gerr != nil {
		return err
	}
	sum := sha256.Sum256(existing)
	if !bytes.Equal(sum[:], contentHash) {
		return fmt.Errorf("%w: ref %q", ErrSegmentRefCollision, ref)
	}
	return nil
}

// mergeHeads produz o head cumulativo por stream: os streams com novidade tomam o
// novo head; os restantes herdam o head do último segmento do manifesto.
func (e *Exporter) mergeHeads(cur map[string]uint64) map[string]uint64 {
	out := cloneHeads(e.priorHeads())
	for k, v := range cur {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

func (e *Exporter) copyHeads() map[string]uint64 {
	return cloneHeads(e.priorHeads())
}

// priorHeads são os StreamHeads cumulativos do último elo: o de memória, ou — num exportador
// retomado que ainda não selou nada neste processo — o do elo adoptado do destino.
//
// Sem esta segunda hipótese, o primeiro elo depois de uma retoma declararia StreamHeads apenas
// dos streams COM novidade nesse ciclo, deixando cair os heads dos streams sossegados. O elo
// continuaria a encadear e a verificar (o EntryHash cobre o que lá está, não o que devia lá
// estar), mas o restauro passaria a ver a cobertura a ANDAR PARA TRÁS — e o cursor de um
// exportador retomado a partir desse elo herdaria a regressão.
func (e *Exporter) priorHeads() map[string]uint64 {
	if n := len(e.manifest.Segments); n > 0 {
		return e.manifest.Segments[n-1].StreamHeads
	}
	return e.baseHeads
}

func cloneHeads(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Manifest devolve uma cópia profunda dos elos selados POR ESTE PROCESSO.
//
// # Não é necessariamente a cadeia toda, e não se finge que é
//
// Desde a retoma (AOS-101), um exportador construído sobre um destino que já tinha cadeia continua
// no ciclo N+1 com o manifesto em memória VAZIO. O que este método devolve é, nesse caso, um sufixo
// — e um sufixo não é restaurável: [Restorer.VerifyManifest] recusa-o em `len(Segments) != cp.Cycle`
// com [ErrChainBroken], que é o comportamento certo e é RUIDOSO. Não há aqui um caminho silencioso.
//
// Para obter a cadeia COMPLETA — que é o que um restauro precisa — use [Restorer.LoadManifest], que
// a reconstrói a partir dos registos de ciclo do destino. Este método existe para observar o estado
// vivo do exportador, não para alimentar um restauro.
//
// A alternativa seria carregar a cadeia inteira em cada arranque: O(N) gets a cada reinício do nó,
// para servir uma leitura que só acontece num restauro. O custo ficaria no arranque de todos os
// dias para poupar trabalho no dia em que há um desastre.
func (e *Exporter) Manifest() Manifest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneManifest(e.manifest)
}

// Checkpoint devolve o último checkpoint assinado do head do backup.
func (e *Exporter) Checkpoint() Checkpoint {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastCheckpoint
}

// Vault devolve o KeyVault usado (necessário ao restauro para decifrar).
func (e *Exporter) Vault() audit.KeyVault { return e.vault }

// Public devolve a chave pública de verificação dos checkpoints.
func (e *Exporter) Public() ed25519.PublicKey { return e.signer.Public() }

// Immutable devolve o ImmutableStore de destino.
func (e *Exporter) Immutable() ImmutableStore { return e.dst }

// Periodicity devolve a periodicidade-alvo configurada (base do RPO).
func (e *Exporter) Periodicity() time.Duration { return e.periodicity }

// WithinRPO indica se a periodicidade satisfaz o RPO-alvo (AC4): sob um ciclo a
// cada Periodicity(), a janela máxima de perda é limitada pela periodicidade.
func (e *Exporter) WithinRPO(target time.Duration) bool {
	return e.periodicity > 0 && e.periodicity <= target
}

// RPOWindow devolve a janela efectiva de RPO no instante now: o tempo desde que o
// backup confirmou estar em dia com o head do Store (now − último Export). Sob um
// loop a cada Periodicity(), este valor mantém-se <= Periodicity().
func (e *Exporter) RPOWindow(now time.Time) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastExportAt.IsZero() {
		return 0
	}
	return now.UTC().Sub(e.lastExportAt)
}

func cloneManifest(m Manifest) Manifest {
	out := Manifest{Region: m.Region, Segments: make([]SegmentEntry, len(m.Segments))}
	for i, s := range m.Segments {
		cp := s
		cp.ContentHash = cloneBytes(s.ContentHash)
		cp.PrevHash = cloneBytes(s.PrevHash)
		cp.EntryHash = cloneBytes(s.EntryHash)
		cp.StreamHeads = cloneHeads(s.StreamHeads)
		out.Segments[i] = cp
	}
	return out
}

// sortedStreams devolve os streams do manifesto ordenados (uso em restauro).
func sortedStreams(heads map[string]uint64) []string {
	out := make([]string, 0, len(heads))
	for k := range heads {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
