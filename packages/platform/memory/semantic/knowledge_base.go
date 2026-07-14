// Package semantic implementa a classe de MEMÓRIA SEMÂNTICA do AOS (AOS-039): a
// BASE DE CONHECIMENTO factual (factos/entidades/relações) consultável, com
// PROVENIÊNCIA OBRIGATÓRIA, quarentena de conhecimento untrusted, curadoria/promoção
// auditável, consulta que preserva o taint até ao consumidor, e conformidade
// (TTL + redação de PII + crypto-shredding).
//
// # Composição (não reimplementação)
//
// A base de conhecimento COMPÕE as fundações já entregues:
//
//   - modelo (AOS-035, domain): [domain.SemanticBody] é o esquema do facto e
//     [domain.Metadata] carrega a proveniência OBRIGATÓRIA (fonte, classificação
//     trusted|untrusted, run_id de origem);
//   - proveniência/quarentena (AOS-042, provenance): a marcação automática de
//     untrusted ([provenance.Ingestor]/[provenance.Classify]), a barreira estrutural
//     control-plane/data-plane ([provenance.Partition]/[provenance.TrustedView]/
//     [provenance.DataItem]) e a promoção auditável ([provenance.Promoter]). NÃO se
//     redefine a barreira — usa-se;
//   - crypto-shredding (AOS-038, episodic): a porta [episodic.KeyStore] (KEK por
//     titular) é o mecanismo de crypto-shredding — apagar a chave torna o conteúdo do
//     facto irrecuperável SEM partir a hash-chain (que sela o HASH do ciphertext);
//   - Event Store (AOS-002, ADR-007): o log append-only, fonte de verdade, de onde a
//     consulta RECONSTRÓI a base por replay — sem estado autoritativo em RAM;
//   - audit hash-chain (AOS-011): sela o HASH do ciphertext de cada facto e regista
//     as promoções (tamper-evident).
//
// # Duas superfícies de consulta DISJUNTAS (a barreira do Princípio 5 / ADR-005)
//
//   - [KnowledgeBase.ControlPlaneView] — SÓ conhecimento trusted, servido como
//     [provenance.TrustedView]. É o que o PLANEADOR lê; só isto pode autorizar uma
//     acção privilegiada. Conhecimento untrusted NUNCA aparece aqui.
//   - [KnowledgeBase.Recall] — conhecimento como DADOS taint-marcados. O untrusted é
//     servido como [provenance.DataItem] — estruturalmente INCAPAZ de autorizar uma
//     tool call (não satisfaz [provenance.PrivilegedAuthorizer]). Cada resultado
//     devolve SEMPRE a etiqueta de proveniência e preserva o taint até ao consumidor.
//
// Determinismo: relógio/entropia/ids injectáveis; ranking estável; serialização
// estável. Observabilidade via a porta Tracer zero-dep do Agent Runtime; sem segredos.
package semantic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/episodic"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/substrate/eventstore"
)

// KnowledgeStreamID é o stream do Event Store onde os factos e as promoções são
// escritos (append-only). Um stream dedicado dá um log ordenado e auditável da base
// de conhecimento.
const KnowledgeStreamID = "memory.semantic.knowledge"

// Tipos canónicos dos eventos no Event Store.
const (
	// EventTypeFactRecorded — um facto escrito na base de conhecimento.
	EventTypeFactRecorded = "memory.semantic.fact.recorded"
	// EventTypeFactPromoted — uma promoção curada untrusted→trusted (referencia o
	// audit_seq da hash-chain; o conteúdo do facto NÃO é reescrito).
	EventTypeFactPromoted = "memory.semantic.fact.promoted"
)

// DefaultChainPartition é a partição default da hash-chain que sela o HASH do
// ciphertext de cada facto (tamper-evident). As promoções usam uma partição própria
// (run_id/AuditPartition), independente desta.
const DefaultChainPartition = "aos.memory.semantic"

// semanticRecordSchemaVersion é a versão SemVer do envelope de facto persistido e do
// schema do registo semântico (prepara AOS-041).
const semanticRecordSchemaVersion = "1.0.0"

// RedactedPlaceholder substitui um campo marcado como PII antes de servir (redação
// de PII, ADR-011). O valor original vive CIFRADO; a redação impede a exposição do
// PII nos resultados de consulta.
const RedactedPlaceholder = "[REDACTED]"

// Dimensões da cifra de envelope (AES-256-GCM), idênticas a AOS-038.
const (
	kekSize   = 32
	dekSize   = 32
	nonceSize = 12
)

// Nomes/atributos de span (namespace próprio aos.memory.semantic.*; as operações de
// memória não são inferência GenAI).
const (
	spanWrite   = "memory.semantic.write"
	spanRecall  = "memory.semantic.recall"
	spanCurate  = "memory.semantic.curate"
	spanSweep   = "memory.semantic.sweep"
	attrFactID  = "aos.memory.semantic.fact_id"
	attrKey     = "aos.memory.semantic.key"
	attrSubject = "aos.memory.semantic.subject_id"
	attrProv    = "aos.memory.semantic.provenance"
	attrTaint   = "aos.memory.semantic.quarantined"
	attrAudit   = "aos.memory.semantic.audit_seq"
	attrResult  = "aos.memory.semantic.result"
)

// EventLog é o subconjunto do Event Store de que a base de conhecimento depende:
// Append (escrita append-only idempotente) e Read (reconstrução por replay).
// *eventstore.Store satisfaz esta interface.
type EventLog interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// FactField identifica um dos campos de um facto (asserção sujeito–predicado–objecto),
// para marcar quais são PII (redação antes de servir).
type FactField string

const (
	FieldSubject   FactField = "subject"
	FieldPredicate FactField = "predicate"
	FieldObject    FactField = "object"
)

// FactInput é o que o produtor fornece para escrever um facto. A proveniência é
// declarada pela FONTE estruturada (parâmetro de [KnowledgeBase.Write]) — NÃO por uma
// tag in-band — e o run_id de origem é obrigatório.
type FactInput struct {
	// FactID é a identidade do facto (idempotência f(run_id, fact_id)).
	FactID string
	// Key é a chave de consulta (recuperação por chave). Obrigatória.
	Key string
	// Tags são as etiquetas de indexação (recuperação por tags).
	Tags []string
	// Subject/Predicate/Object formam a asserção factual.
	Subject   string
	Predicate string
	Object    string
	// Confidence é o grau de confiança [0,1] atribuído à asserção.
	Confidence float64
	// PII marca quais campos são dados pessoais — redigidos antes de servir (ADR-011).
	PII []FactField
	// SubjectID é o titular dos dados — a chave por titular que o cifra e que o
	// crypto-shredding apaga. Obrigatório.
	SubjectID string
	// AgentID é a NHI que produziu o facto (proveniência/responsabilização).
	AgentID string
	// RunID é o run de ORIGEM — componente obrigatório da proveniência.
	RunID string
	// TTLClass é a classe de retenção (TTL por classe; ADR-011).
	TTLClass domain.TTLClass
	// CreatedAt é o instante de criação; se zero, a fachada preenche pelo relógio.
	CreatedAt time.Time
}

// validate impõe os campos mínimos (fail-closed). A validação da FONTE de
// proveniência é feita em [KnowledgeBase.Write] (é um parâmetro estrutural).
func (in FactInput) validate() error {
	if in.FactID == "" {
		return ErrMissingFactID
	}
	if in.Key == "" {
		return ErrMissingKey
	}
	if in.SubjectID == "" {
		return ErrMissingSubjectID
	}
	if in.AgentID == "" {
		return ErrMissingAgentID
	}
	if in.RunID == "" {
		return ErrMissingRunID
	}
	if !in.TTLClass.Valid() {
		return ErrInvalidTTLClass
	}
	return nil
}

// factEnvelope é a forma PERSISTIDA de um facto no Event Store. Os campos de índice
// (Key/Tags/Provenance/Source/RunID/…) ficam em CLARO para a consulta e para a
// etiqueta de proveniência SEMPRE devolvida; o CONTEÚDO (a asserção) vai CIFRADO em
// Sealed sob a KEK do titular. ContentHash é o hash do Sealed que a hash-chain sela.
type factEnvelope struct {
	SchemaVersion string   `json:"schema_version"`
	FactID        string   `json:"fact_id"`
	Key           string   `json:"key"`
	Tags          []string `json:"tags"`
	Provenance    string   `json:"provenance"` // classificação de origem (trusted|untrusted)
	Source        string   `json:"source"`     // fonte forense (tool_result|web|system|…)
	RunID         string   `json:"run_id"`
	AgentID       string   `json:"agent_id"`
	SubjectID     string   `json:"subject_id"`
	TTLClass      string   `json:"ttl_class"`
	CreatedAt     string   `json:"created_at"`
	PII           []string `json:"pii"`
	Sealed        envelope `json:"sealed"`
	ContentHash   string   `json:"content_hash"`
	AuditSeq      uint64   `json:"audit_seq"`
}

// promotionEnvelope é a forma PERSISTIDA de uma promoção curada. NÃO reescreve o
// facto (o original untrusted permanece imutável, coerente com o event-sourcing):
// referencia o FactID e o audit_seq da hash-chain, e regista a fonte-curadora
// (trusted) que a reconstrução usa para servir o facto promovido como trusted.
type promotionEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	FactID        string `json:"fact_id"`
	// ContentHash LIGA a promoção ao CONTEÚDO concreto que foi validado (o
	// ContentHash do envelope curado), não apenas ao FactID. A reconstrução só serve
	// o facto como trusted se o envelope EFECTIVO ainda tiver este mesmo hash — um
	// envelope-sombra (mesmo FactID, conteúdo diferente, escrito por uma fonte
	// untrusted) NÃO herda a promoção (fail-closed). É a defesa central contra o
	// bypass da barreira de quarentena por reutilização de um FactID já promovido.
	ContentHash       string `json:"content_hash"`
	CuratorSource     string `json:"curator_source"` // fonte trusted da reconstrução
	Method            string `json:"method"`
	Validator         string `json:"validator"`
	RunID             string `json:"run_id"`
	AgentID           string `json:"agent_id"`
	PromotionAuditSeq uint64 `json:"promotion_audit_seq"`
}

// KnowledgeBase é a base de conhecimento semântica. Escreve factos (cifrados,
// append-only) e serve-os por duas superfícies disjuntas (trusted control-plane vs.
// dados taint-marcados), com curadoria auditável, TTL, redação de PII e crypto-shredding.
type KnowledgeBase struct {
	es        EventLog
	keys      episodic.KeyStore
	chain     audit.Store
	ingestor  *provenance.Ingestor
	promoter  *provenance.Promoter
	dp        provenance.DataPlane
	tracer    agentruntime.Tracer
	clock     func() time.Time
	rand      episodic.RandSource
	partition string
	ttlPolicy episodic.TTLPolicy
	// writeMu SERIALIZA o caminho de escrita (Write/Curate): torna atómico o
	// check-then-act de idempotência/monotonicidade de proveniência + a selagem da
	// hash-chain + o append no Event Store. Sem ele, duas escritas concorrentes da
	// mesma f(run_id, fact_id) podem passar ambas o pre-check e SELAR a cadeia duas
	// vezes (entrada de audit órfã) enquanto o Event Store só grava um evento
	// (dedup por run_id:step_id). É correcção de integridade de audit sob concorrência.
	writeMu sync.Mutex
}

// Option configura a KnowledgeBase.
type Option func(*KnowledgeBase)

// WithTracer injecta a porta Tracer (default NoopTracer).
func WithTracer(t agentruntime.Tracer) Option {
	return func(kb *KnowledgeBase) {
		if t != nil {
			kb.tracer = t
		}
	}
}

// WithClock injecta o relógio (default time.Now). Determinismo em teste.
func WithClock(now func() time.Time) Option {
	return func(kb *KnowledgeBase) {
		if now != nil {
			kb.clock = now
		}
	}
}

// WithRandSource injecta a fonte de entropia da cripto (default crypto/rand).
func WithRandSource(r episodic.RandSource) Option {
	return func(kb *KnowledgeBase) {
		if r != nil {
			kb.rand = r
		}
	}
}

// WithChainPartition sobrepõe a partição da hash-chain de conteúdo (default
// [DefaultChainPartition]).
func WithChainPartition(p string) Option {
	return func(kb *KnowledgeBase) {
		if p != "" {
			kb.partition = p
		}
	}
}

// WithTTLPolicy sobrepõe a política de TTL por classe (default [episodic.DefaultTTLPolicy]).
func WithTTLPolicy(p episodic.TTLPolicy) Option {
	return func(kb *KnowledgeBase) {
		if p != nil {
			kb.ttlPolicy = p
		}
	}
}

// NewKnowledgeBase constrói a base de conhecimento sobre o Event Store, o KeyStore
// (crypto-shredding) e a hash-chain de audit. Os três são obrigatórios (fail-closed
// na construção). Constrói internamente o Ingestor (marcação automática de untrusted)
// e o Promoter (curadoria auditável) de AOS-042.
func NewKnowledgeBase(es EventLog, keys episodic.KeyStore, chain audit.Store, opts ...Option) (*KnowledgeBase, error) {
	if es == nil {
		return nil, ErrNilStore
	}
	if keys == nil {
		return nil, ErrNilKeyStore
	}
	if chain == nil {
		return nil, ErrNilAuditStore
	}
	kb := &KnowledgeBase{
		es:        es,
		keys:      keys,
		chain:     chain,
		dp:        provenance.ReferenceDataPlane{},
		tracer:    agentruntime.NoopTracer{},
		clock:     time.Now,
		rand:      cryptoRand,
		partition: DefaultChainPartition,
		ttlPolicy: episodic.DefaultTTLPolicy(),
	}
	for _, o := range opts {
		o(kb)
	}
	kb.ingestor = provenance.NewIngestor(nil, provenance.WithIngestTracer(kb.tracer))
	promoter, err := provenance.NewPromoter(chain,
		provenance.WithClock(kb.clock),
		provenance.WithPromoteTracer(kb.tracer),
	)
	if err != nil {
		return nil, err
	}
	kb.promoter = promoter
	return kb, nil
}

// Partition devolve a partição da hash-chain de conteúdo (para verificação externa).
func (kb *KnowledgeBase) Partition() string { return kb.partition }

// WrittenFact é o resultado de escrever um facto: a etiqueta de proveniência
// (SEMPRE presente) e se o facto entrou em quarentena (untrusted).
type WrittenFact struct {
	FactID      string
	Provenance  domain.Provenance
	Source      domain.ProvenanceSource
	Quarantined bool
	AuditSeq    uint64
}

// Write escreve um facto na base de conhecimento classificando-o pela FONTE
// estruturada src (a marcação automática de untrusted de AOS-042). Passos:
//
//  1. valida os campos mínimos e a FONTE de proveniência (fail-closed: fonte
//     ausente/não-canónica → [ErrMissingProvenanceSource] — escrita SEM proveniência
//     REJEITADA);
//  2. ingere via [provenance.Ingestor]: impõe a proveniência derivada da fonte sobre
//     os metadados e valida-os (schema_version/agent_id/run_id/ttl_class — fail-closed);
//  3. cifra a asserção por envelope sob a KEK do titular (crypto-shredding);
//  4. sela o HASH do ciphertext na hash-chain (tamper-evident), com a obrigação de
//     redação de PII selada;
//  5. escreve o envelope append-only no Event Store (idempotente f(run_id, fact_id)).
//
// A proveniência é DERIVADA de src e sobrepõe-se a qualquer afirmação do chamador:
// conteúdo de tool_result/web/mcp_schema entra em quarentena (untrusted), NUNCA como
// trusted. Devolve SEMPRE a etiqueta de proveniência.
func (kb *KnowledgeBase) Write(ctx context.Context, in FactInput, src provenance.Source) (WrittenFact, error) {
	ctx, span := kb.tracer.StartSpan(ctx, spanWrite)
	defer span.End()

	if err := in.validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return WrittenFact{}, err
	}
	// Proveniência OBRIGATÓRIA: a fonte tem de ser canónica. Uma fonte vazia/
	// desconhecida é REJEITADA (fail-closed) — não admitida silenciosamente.
	if !canonicalSource(src) {
		span.SetAttribute(attrResult, "rejected")
		return WrittenFact{}, ErrMissingProvenanceSource
	}
	span.SetAttribute(attrFactID, in.FactID)
	span.SetAttribute(attrKey, in.Key)
	span.SetAttribute(attrSubject, in.SubjectID)

	createdAt := in.CreatedAt
	if createdAt.IsZero() {
		createdAt = kb.clock()
	}

	rec := domain.Record{
		ID:    in.FactID,
		Class: domain.ClassSemantic,
		Metadata: domain.Metadata{
			AgentID:       in.AgentID,
			RunID:         in.RunID,
			CreatedAt:     createdAt,
			TTLClass:      in.TTLClass,
			SchemaVersion: semanticRecordSchemaVersion,
		},
		Body: domain.SemanticBody{
			Subject:    in.Subject,
			Predicate:  in.Predicate,
			Object:     in.Object,
			Confidence: in.Confidence,
		},
	}

	// (2) Ingestão AOS-042: impõe a proveniência da fonte e valida (fail-closed).
	ingested, err := kb.ingestor.Ingest(ctx, rec, src)
	if err != nil {
		span.SetAttribute(attrResult, "rejected")
		return WrittenFact{}, err
	}
	prov := ingested.Provenance()
	span.SetAttribute(attrProv, string(prov))
	span.SetAttribute(attrTaint, prov != provenance.Trusted)

	// SERIALIZAÇÃO do caminho de escrita: o pre-check (idempotência/monotonicidade),
	// a selagem da cadeia e o append no Event Store têm de ser atómicos entre si. Sob
	// concorrência, dois writes da mesma f(run_id, fact_id) que passassem o pre-check
	// antes de qualquer Append selariam a cadeia DUAS vezes (audit órfão) enquanto o
	// ES só grava um evento. O lock elimina essa janela TOCTOU.
	kb.writeMu.Lock()
	defer kb.writeMu.Unlock()

	// Relê o estado UMA vez sob o lock (Event Store = fonte de verdade) para decidir
	// idempotência e monotonicidade de proveniência.
	st, serr := kb.buildState(ctx)
	if serr != nil {
		return WrittenFact{}, serr
	}
	if prior, ok := st.facts[in.FactID]; ok {
		// Idempotência: um facto já persistido (mesma f(run_id, fact_id)) é no-op — não
		// re-sela a cadeia nem re-emite.
		if prior.RunID == in.RunID {
			span.SetAttribute(attrResult, "duplicate")
			eff := st.effectiveProvenance(in.FactID)
			return WrittenFact{
				FactID:      prior.FactID,
				Provenance:  eff,
				Source:      domain.ProvenanceSource(prior.Source),
				Quarantined: eff != provenance.Trusted,
				AuditSeq:    prior.AuditSeq,
			}, nil
		}
		// MONOTONICIDADE de proveniência: um envelope de MENOR confiança NÃO pode
		// sobrepor (shadow) um FactID já de MAIOR confiança (trusted por origem ou
		// promovido). Sem isto, um segundo Write untrusted (web/tool_result) com o
		// MESMO FactID e RunID diferente vencia por last-write-wins e (a) envenenava o
		// control-plane herdando a flag `promoted` do envelope original OU (b) evictava
		// o facto trusted do caminho do planeador. Rejeita fail-closed.
		if st.effectiveProvenance(in.FactID) == provenance.Trusted && prov != provenance.Trusted {
			span.SetAttribute(attrResult, "rejected")
			return WrittenFact{}, ErrProvenanceDowngrade
		}
	}

	// (3) CIFRAGEM por envelope sob a KEK do titular (provisionada on-first-write).
	plaintext, err := json.Marshal(ingested.Record().Body)
	if err != nil {
		return WrittenFact{}, err
	}
	kek, err := kb.keys.EnsureKey(in.SubjectID)
	if err != nil {
		return WrittenFact{}, err
	}
	blob, err := sealBody(kek, plaintext, kb.rand)
	if err != nil {
		return WrittenFact{}, err
	}
	ch, err := bodyHash(blob)
	if err != nil {
		return WrittenFact{}, err
	}

	// (4) SELAGEM na hash-chain: sela o HASH do ciphertext + KeyRef/SubjectID e a
	// obrigação de redação de PII. Habilita o crypto-shredding sem partir a cadeia.
	sealedRec, err := kb.chain.Append(ctx, kb.auditRecordFor(in, prov, ch))
	if err != nil {
		return WrittenFact{}, err
	}
	span.SetAttribute(attrAudit, sealedRec.AuditSeq)

	// (5) ESCRITA append-only no Event Store (idempotente f(run_id, fact_id)).
	env := factEnvelope{
		SchemaVersion: semanticRecordSchemaVersion,
		FactID:        in.FactID,
		Key:           in.Key,
		Tags:          append([]string(nil), in.Tags...),
		Provenance:    string(prov),
		Source:        string(src),
		RunID:         in.RunID,
		AgentID:       in.AgentID,
		SubjectID:     in.SubjectID,
		TTLClass:      string(in.TTLClass),
		CreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
		PII:           piiNames(in.PII),
		Sealed:        blob,
		ContentHash:   hexHash(ch),
		AuditSeq:      sealedRec.AuditSeq,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return WrittenFact{}, err
	}
	if _, err := kb.es.Append(ctx, KnowledgeStreamID, eventstore.EventInput{
		Type:     EventTypeFactRecorded,
		Payload:  payload,
		RunID:    in.RunID,
		StepID:   "semantic:fact:" + in.FactID,
		Producer: eventstore.Producer{NHIID: in.AgentID},
	}); err != nil {
		return WrittenFact{}, err
	}
	span.SetAttribute(attrResult, "committed")

	return WrittenFact{
		FactID:      in.FactID,
		Provenance:  prov,
		Source:      domain.ProvenanceSource(src),
		Quarantined: prov != provenance.Trusted,
		AuditSeq:    sealedRec.AuditSeq,
	}, nil
}

// auditRecordFor constrói o AuditRecord que sela um facto na hash-chain. Só metadados
// de responsabilização e o PayloadRef (ContentHash + KeyRef + SubjectID) entram —
// NUNCA o plaintext. A obrigação redact_pii sela a política de redação (ADR-011).
func (kb *KnowledgeBase) auditRecordFor(in FactInput, prov domain.Provenance, ch []byte) audit.AuditRecord {
	obligations := []audit.Obligation{{
		Type:   "ttl",
		Params: map[string]string{"ttl_class": string(in.TTLClass)},
	}}
	if len(in.PII) > 0 {
		obligations = append(obligations, audit.Obligation{
			Type:   "redact_pii",
			Fields: piiNames(in.PII),
		})
	}
	return audit.AuditRecord{
		Partition:     kb.partition,
		Timestamp:     kb.clock(),
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: in.AgentID},
		Capability:    "memory:semantic:write",
		PolicyVersion: semanticRecordSchemaVersion,
		RunID:         in.RunID,
		StepID:        "semantic:fact:" + in.FactID,
		ToolID:        "memory.semantic",
		Resource: audit.Resource{
			Type:  "memory",
			Value: domain.ClassSemantic.String() + "/" + in.FactID,
		},
		Context:     audit.CallContext{Taint: string(prov)},
		Obligations: obligations,
		PayloadRef: &audit.PayloadRef{
			ContentHash: ch,
			KeyRef:      in.SubjectID,
			SubjectID:   in.SubjectID,
		},
	}
}

// VerifyChain verifica a integridade da hash-chain de conteúdo de ponta a ponta.
// Devolve nil se íntegra (mesmo após crypto-shredding: apagar a chave não muta a
// cadeia). Uma partição vazia (nenhum facto ainda) é nil.
func (kb *KnowledgeBase) VerifyChain(ctx context.Context) error {
	head, err := kb.chain.Head(ctx, kb.partition)
	if err != nil {
		return err
	}
	if head == 0 {
		return nil
	}
	return audit.Verify(ctx, kb.chain, kb.partition, 1, head)
}

// ---------------------------------------------------------------------------
// Reconstrução de estado a partir do log (Event Store = fonte de verdade)
// ---------------------------------------------------------------------------

// state é a base de conhecimento RECONSTRUÍDA por replay: o último envelope por
// FactID e o conjunto de promoções curadas.
type state struct {
	facts    map[string]factEnvelope
	order    []string // FactIDs por ordem de primeira escrita (determinismo)
	promoted map[string]promotionEnvelope
}

// buildState relê o log e reconstrói o estado da base (factos + promoções). O Event
// Store é a fonte de verdade; não há estado autoritativo em RAM.
func (kb *KnowledgeBase) buildState(ctx context.Context) (state, error) {
	events, err := kb.es.Read(ctx, KnowledgeStreamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return state{facts: map[string]factEnvelope{}, promoted: map[string]promotionEnvelope{}}, nil
		}
		return state{}, err
	}
	st := state{
		facts:    make(map[string]factEnvelope),
		promoted: make(map[string]promotionEnvelope),
	}
	for _, ev := range events {
		switch ev.Type {
		case EventTypeFactRecorded:
			var env factEnvelope
			if err := json.Unmarshal(ev.Payload, &env); err != nil {
				return state{}, err
			}
			if _, seen := st.facts[env.FactID]; !seen {
				st.order = append(st.order, env.FactID)
			}
			st.facts[env.FactID] = env
		case EventTypeFactPromoted:
			var pe promotionEnvelope
			if err := json.Unmarshal(ev.Payload, &pe); err != nil {
				return state{}, err
			}
			st.promoted[pe.FactID] = pe
		}
	}
	return st, nil
}

// effectiveProvenance devolve a classificação EFECTIVA de um facto. Um facto é
// trusted se (a) a sua classificação de origem selada é trusted, OU (b) foi promovido
// por curadoria E o envelope EFECTIVO é ainda EXACTAMENTE o conteúdo que foi validado.
//
// A promoção está ligada ao CONTEÚDO (ContentHash do envelope curado), não apenas ao
// FactID: se um envelope-sombra (mesmo FactID, conteúdo diferente — tipicamente escrito
// por uma fonte untrusted num RunID distinto) passasse a ser o conteúdo efectivo, NÃO
// herda a promoção — devolve untrusted (fail-closed). É a defesa que impede que a
// reutilização de um FactID já promovido sirva conteúdo do atacante como trusted.
func (st state) effectiveProvenance(factID string) domain.Provenance {
	env, ok := st.facts[factID]
	if !ok {
		return ""
	}
	if pe, promoted := st.promoted[factID]; promoted && pe.ContentHash == env.ContentHash {
		return provenance.Trusted
	}
	return domain.Provenance(env.Provenance)
}

// recordFromEnvelope reconstrói o [domain.Record] de um facto a partir do seu
// envelope, decifrando a asserção se a chave existir. Se a chave foi apagada
// (crypto-shredding), o corpo vem vazio e recoverable=false (o índice permanece).
func (kb *KnowledgeBase) recordFromEnvelope(env factEnvelope, createdAt time.Time) (domain.Record, domain.SemanticBody, bool) {
	body, recoverable := kb.decryptBody(env)
	rec := domain.Record{
		ID:    env.FactID,
		Class: domain.ClassSemantic,
		Metadata: domain.Metadata{
			AgentID:       env.AgentID,
			RunID:         env.RunID,
			Provenance:    domain.Provenance(env.Provenance),
			Source:        domain.ProvenanceSource(env.Source),
			CreatedAt:     createdAt,
			TTLClass:      domain.TTLClass(env.TTLClass),
			SchemaVersion: env.SchemaVersion,
		},
		Body: body,
	}
	return rec, body, recoverable
}

// decryptBody decifra a asserção de um facto. Fail-closed: se a KEK do titular foi
// apagada (crypto-shredding/TTL) ou a decifragem falha, devolve um corpo vazio e
// recoverable=false — o facto é irrecuperável no conteúdo (o índice mantém-se).
func (kb *KnowledgeBase) decryptBody(env factEnvelope) (domain.SemanticBody, bool) {
	kek, ok := kb.keys.Key(env.SubjectID)
	if !ok {
		return domain.SemanticBody{}, false
	}
	plaintext, err := openBody(kek, env.Sealed)
	if err != nil {
		return domain.SemanticBody{}, false
	}
	var body domain.SemanticBody
	if err := json.Unmarshal(plaintext, &body); err != nil {
		return domain.SemanticBody{}, false
	}
	return body, true
}

// ---------------------------------------------------------------------------
// Consulta — Princípio 5: trusted-only (planeador) vs. dados taint-marcados
// ---------------------------------------------------------------------------

// Query é o critério de consulta por chave e/ou tags. Um campo vazio não filtra
// nessa dimensão. A consulta devolve SEMPRE a etiqueta de proveniência.
type Query struct {
	// Key filtra por chave exacta (vazio = qualquer chave).
	Key string
	// Tags são as etiquetas a casar. Ver MatchAll.
	Tags []string
	// MatchAll: se true exige TODAS as Tags; se false basta uma (any-of).
	MatchAll bool
	// IncludeQuarantined: se true, [KnowledgeBase.Recall] inclui conhecimento untrusted
	// (servido como DADOS taint-marcados); se false, devolve só trusted-como-dados. A
	// distinção materializa "query trusted-only vs. query que inclui quarentena".
	IncludeQuarantined bool
	// Limit limita o nº de resultados (0 = sem limite), aplicado após o ranking.
	Limit int
}

// RecalledFact é um facto recuperado. Carrega SEMPRE a etiqueta de proveniência
// (Provenance) e a fonte forense. O taint é PRESERVADO até ao consumidor por
// construção de tipo: um facto untrusted expõe um [provenance.DataItem] (via
// DataItem()) — que NÃO satisfaz [provenance.PrivilegedAuthorizer] — e um facto
// trusted expõe um [provenance.TrustedEntry] (via Authorizer()). Um facto untrusted
// NUNCA devolve um autorizador: é estruturalmente incapaz de autorizar uma acção.
type RecalledFact struct {
	FactID     string
	Key        string
	Tags       []string
	Provenance domain.Provenance
	Source     domain.ProvenanceSource
	// Recoverable indica se a asserção foi decifrada (a chave existe). Se false, o
	// conteúdo foi crypto-shredded/expirou — o índice e a proveniência permanecem.
	Recoverable bool
	// Body é a asserção com os campos PII REDIGIDOS (só válida se Recoverable).
	Body domain.SemanticBody
	// Redacted lista os campos que foram redigidos (PII).
	Redacted []string
	AuditSeq uint64
	Score    int

	data    *provenance.DataItem
	trusted *provenance.TrustedEntry
}

// Authorizer devolve o autorizador de control-plane do facto SE ele for trusted. Um
// facto untrusted (em quarentena) devolve (nil, false) — não há como obter dele uma
// autoridade de acção. É a barreira estrutural na superfície de consulta.
func (f RecalledFact) Authorizer() (provenance.PrivilegedAuthorizer, bool) {
	if f.trusted == nil {
		return nil, false
	}
	return *f.trusted, true
}

// DataItem devolve o item de dados taint-marcado do facto SE ele for untrusted (em
// quarentena). Um facto trusted devolve (zero, false).
func (f RecalledFact) DataItem() (provenance.DataItem, bool) {
	if f.data == nil {
		return provenance.DataItem{}, false
	}
	return *f.data, true
}

// Recall recupera factos por chave/tags e devolve-os como DADOS taint-marcados, por
// ordem de relevância DETERMINÍSTICA. Cada resultado carrega SEMPRE a etiqueta de
// proveniência e PRESERVA o taint: conhecimento untrusted é servido como
// [provenance.DataItem] (incapaz de autorizar). Se q.IncludeQuarantined for false,
// só devolve conhecimento trusted (como dados). Factos cujo conteúdo foi
// crypto-shredded são devolvidos com Recoverable=false (visíveis no índice).
//
// Ranking determinístico: por Score DESC; empate por AuditSeq ASC; empate remanescente
// por FactID ASC.
func (kb *KnowledgeBase) Recall(ctx context.Context, q Query) ([]RecalledFact, error) {
	_, span := kb.tracer.StartSpan(ctx, spanRecall)
	defer span.End()

	st, err := kb.buildState(ctx)
	if err != nil {
		return nil, err
	}

	type scored struct {
		env   factEnvelope
		score int
	}
	matched := make([]scored, 0, len(st.order))
	for _, id := range st.order {
		env := st.facts[id]
		if q.Key != "" && env.Key != q.Key {
			continue
		}
		score, ok := scoreTags(env.Tags, q.Tags, q.MatchAll)
		if !ok {
			continue
		}
		if q.Key != "" && env.Key == q.Key {
			score++ // bónus de chave exacta
		}
		prov := st.effectiveProvenance(id)
		if prov != provenance.Trusted && !q.IncludeQuarantined {
			continue // quarentena excluída da consulta trusted-como-dados
		}
		matched = append(matched, scored{env: env, score: score})
	}

	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		if matched[i].env.AuditSeq != matched[j].env.AuditSeq {
			return matched[i].env.AuditSeq < matched[j].env.AuditSeq
		}
		return matched[i].env.FactID < matched[j].env.FactID
	})

	out := make([]RecalledFact, 0, len(matched))
	for _, m := range matched {
		rf, err := kb.recallOne(ctx, st, m.env, m.score)
		if err != nil {
			return nil, err
		}
		out = append(out, rf)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// recallOne materializa um RecalledFact preservando o taint por construção de tipo.
func (kb *KnowledgeBase) recallOne(ctx context.Context, st state, env factEnvelope, score int) (RecalledFact, error) {
	createdAt, _ := time.Parse(time.RFC3339Nano, env.CreatedAt)
	rec, body, recoverable := kb.recordFromEnvelope(env, createdAt)
	redactedBody, redacted := redactPII(body, env.PII)
	prov := st.effectiveProvenance(env.FactID)

	rf := RecalledFact{
		FactID:      env.FactID,
		Key:         env.Key,
		Tags:        append([]string(nil), env.Tags...),
		Provenance:  prov,
		Source:      domain.ProvenanceSource(env.Source),
		Recoverable: recoverable,
		AuditSeq:    env.AuditSeq,
		Score:       score,
	}
	if recoverable {
		rf.Body = redactedBody
		rf.Redacted = redacted
	}

	if prov == provenance.Trusted {
		// Reconstrói a entrada trusted pela fonte que a autoriza (a fonte-curadora,
		// se promovido; senão a fonte de origem trusted). Só assim se obtém um
		// TrustedEntry — a autoridade de control-plane é inforjável (AOS-042).
		te, err := kb.trustedEntry(ctx, st, env, rec)
		if err != nil {
			return RecalledFact{}, err
		}
		rf.trusted = &te
	} else {
		// Untrusted: servido como DADOS taint-marcados via o data-plane (AOS-042).
		// Redige o PII também no item de dados servido ao modelo.
		servedRec := rec
		servedRec.Body = redactedBody
		di := kb.dp.Serve(servedRec)
		rf.data = &di
	}
	return rf, nil
}

// trustedEntry reconstrói o [provenance.TrustedEntry] de um facto trusted, admitindo-o
// numa partição via a FONTE que o autoriza. Para um facto promovido usa a fonte-curadora
// (trusted) registada na promoção; para um facto trusted de origem usa a sua fonte. É a
// única via legítima de obter um TrustedEntry (a autoridade de control-plane não é
// forjável — AOS-042). O corpo é redigido antes de entrar no control-plane.
func (kb *KnowledgeBase) trustedEntry(ctx context.Context, st state, env factEnvelope, rec domain.Record) (provenance.TrustedEntry, error) {
	redacted, _ := redactPII(rec.Body.(domain.SemanticBody), env.PII)
	rec.Body = redacted

	src := provenance.Source(env.Source)
	if pe, ok := st.promoted[env.FactID]; ok {
		src = provenance.Source(pe.CuratorSource)
	}
	ingested, err := kb.ingestor.Ingest(ctx, rec, src)
	if err != nil {
		return provenance.TrustedEntry{}, err
	}
	part := provenance.NewPartition(kb.dp)
	part.Admit(ingested)
	entries := part.TrustedView().Entries()
	if len(entries) != 1 {
		// Defesa em profundidade: a fonte deveria classificar trusted; se não, não há
		// entrada trusted (fail-closed — devolve zero-value inerte).
		return provenance.TrustedEntry{}, nil
	}
	return entries[0], nil
}

// ControlPlaneView reconstrói a superfície de control-plane (SÓ conhecimento trusted)
// que o PLANEADOR consome — a [provenance.TrustedView] de AOS-042. Conhecimento
// untrusted (em quarentena) NUNCA aparece aqui: o planeador não consegue, sequer,
// alcançá-lo por este caminho (segregação de caminho). Filtra por chave/tags como Recall.
func (kb *KnowledgeBase) ControlPlaneView(ctx context.Context, q Query) (*provenance.TrustedView, error) {
	st, err := kb.buildState(ctx)
	if err != nil {
		return nil, err
	}
	part := provenance.NewPartition(kb.dp)
	for _, id := range st.order {
		env := st.facts[id]
		if q.Key != "" && env.Key != q.Key {
			continue
		}
		if _, ok := scoreTags(env.Tags, q.Tags, q.MatchAll); !ok {
			continue
		}
		if st.effectiveProvenance(id) != provenance.Trusted {
			continue // só trusted alimenta o control-plane
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, env.CreatedAt)
		rec, body, _ := kb.recordFromEnvelope(env, createdAt)
		redacted, _ := redactPII(body, env.PII)
		rec.Body = redacted

		src := provenance.Source(env.Source)
		if pe, ok := st.promoted[id]; ok {
			src = provenance.Source(pe.CuratorSource)
		}
		ingested, err := kb.ingestor.Ingest(ctx, rec, src)
		if err != nil {
			return nil, err
		}
		part.Admit(ingested)
	}
	return part.TrustedView(), nil
}

// ---------------------------------------------------------------------------
// Conformidade — TTL por classe via crypto-shredding (padrão de AOS-038)
// ---------------------------------------------------------------------------

// SweptFact identifica um facto expirado por TTL e crypto-shredded.
type SweptFact struct {
	FactID    string
	SubjectID string
	TTLClass  string
}

// Sweep aplica o TTL POR CLASSE via crypto-shredding, respeitando que a chave é POR
// TITULAR (uma KEK por subject cifra TODOS os seus factos). Apaga a KEK de um titular
// SÓ quando TODOS os seus factos já expiraram — um único facto não-expirado (ou de
// classe sem expiração) RETÉM a KEK e nada desse titular é varrido (não-destruição de
// não-expirados). Apagar a KEK torna os factos IRRECUPERÁVEIS sem apagar o registo do
// log nem partir a hash-chain (ADR-011). Determinístico (relógio via now; ordem =
// ordem de escrita). É o mesmo padrão do Sweep episódico (AOS-038).
func (kb *KnowledgeBase) Sweep(ctx context.Context, now time.Time) ([]SweptFact, error) {
	_, span := kb.tracer.StartSpan(ctx, spanSweep)
	defer span.End()

	st, err := kb.buildState(ctx)
	if err != nil {
		return nil, err
	}

	type subjectState struct {
		total   int
		expired int
		facts   []factEnvelope
	}
	states := make(map[string]*subjectState)
	var order []string
	for _, id := range st.order {
		env := st.facts[id]
		ss := states[env.SubjectID]
		if ss == nil {
			ss = &subjectState{}
			states[env.SubjectID] = ss
			order = append(order, env.SubjectID)
		}
		ss.total++
		ss.facts = append(ss.facts, env)

		ttl, ok := kb.ttlPolicy[domain.TTLClass(env.TTLClass)]
		if !ok || ttl <= 0 {
			continue // classe sem expiração (ex.: permanent) ou desconhecida
		}
		created, perr := time.Parse(time.RFC3339Nano, env.CreatedAt)
		if perr != nil {
			return nil, perr
		}
		if now.Before(created.Add(ttl)) {
			continue // ainda dentro do TTL
		}
		ss.expired++
	}

	var swept []SweptFact
	for _, subject := range order {
		ss := states[subject]
		if ss.total == 0 || ss.expired != ss.total {
			continue // algum facto não-expirado: RETÉM a KEK
		}
		kb.keys.DeleteKey(subject)
		for _, env := range ss.facts {
			swept = append(swept, SweptFact{
				FactID:    env.FactID,
				SubjectID: env.SubjectID,
				TTLClass:  env.TTLClass,
			})
		}
	}
	return swept, nil
}

// ---------------------------------------------------------------------------
// Helpers deterministas
// ---------------------------------------------------------------------------

// canonicalSource indica se src é uma fonte de proveniência canónica (não vazia). É a
// guarda de "proveniência obrigatória": uma fonte fora do conjunto é rejeitada.
func canonicalSource(src provenance.Source) bool {
	switch src {
	case provenance.SourceSystem, provenance.SourceAuthenticatedUser,
		provenance.SourceToolResult, provenance.SourceWeb,
		provenance.SourceMCPSchema, provenance.SourceDerivedMemory:
		return true
	default:
		return false
	}
}

// piiNames converte os campos PII em nomes estáveis (ordem preservada).
func piiNames(fields []FactField) []string {
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = string(f)
	}
	return out
}

// redactPII devolve uma cópia do corpo com os campos marcados PII substituídos pelo
// [RedactedPlaceholder], e a lista dos campos redigidos. É aplicada ANTES de servir
// (redação de PII, ADR-011) — tanto no data-plane como no control-plane.
func redactPII(body domain.SemanticBody, pii []string) (domain.SemanticBody, []string) {
	if len(pii) == 0 {
		return body, nil
	}
	redacted := make([]string, 0, len(pii))
	for _, field := range pii {
		switch FactField(field) {
		case FieldSubject:
			body.Subject = RedactedPlaceholder
			redacted = append(redacted, field)
		case FieldPredicate:
			body.Predicate = RedactedPlaceholder
			redacted = append(redacted, field)
		case FieldObject:
			body.Object = RedactedPlaceholder
			redacted = append(redacted, field)
		}
	}
	return body, redacted
}

// scoreTags devolve a pontuação de casamento de tags e se o facto passa o filtro. Sem
// tags de consulta, passa com score 0. MatchAll exige todas; caso contrário basta uma.
func scoreTags(have, want []string, matchAll bool) (int, bool) {
	if len(want) == 0 {
		return 0, true
	}
	set := make(map[string]struct{}, len(have))
	for _, t := range have {
		set[t] = struct{}{}
	}
	n := 0
	for _, w := range want {
		if _, ok := set[w]; ok {
			n++
		}
	}
	if matchAll {
		return n, n == len(want)
	}
	return n, n > 0
}

// ---------------------------------------------------------------------------
// Cifra de envelope (AES-256-GCM) — MESMO padrão de AOS-038 (episodic/crypto.go),
// reutilizando a porta episodic.KeyStore (o mecanismo de crypto-shredding: apagar a
// KEK do titular torna o ciphertext irrecuperável). O wrapper de AOS-038 é
// não-exportado, pelo que a cifra de envelope é reaplicada aqui sobre a MESMA porta.
// ---------------------------------------------------------------------------

// envelope é o CIPHERTEXT de envelope de um facto (dois níveis: DEK por facto cifra o
// plaintext; a KEK do titular embrulha a DEK). Vai INTEIRO para o log append-only; a
// hash-chain sela o seu HASH ([bodyHash]) — nunca o plaintext.
type envelope struct {
	WrappedDEK []byte `json:"wrapped_dek"`
	DEKNonce   []byte `json:"dek_nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
}

// cryptoRand adapta crypto/rand.Read à assinatura de [episodic.RandSource].
func cryptoRand(p []byte) error {
	_, err := io.ReadFull(rand.Reader, p)
	return err
}

// sealBody cifra plaintext por envelope sob a KEK dada (DEK+nonces via rnd injectável).
func sealBody(kek, plaintext []byte, rnd episodic.RandSource) (envelope, error) {
	dek := make([]byte, dekSize)
	if err := rnd(dek); err != nil {
		return envelope{}, err
	}
	nonce := make([]byte, nonceSize)
	if err := rnd(nonce); err != nil {
		return envelope{}, err
	}
	dekNonce := make([]byte, nonceSize)
	if err := rnd(dekNonce); err != nil {
		return envelope{}, err
	}
	contentGCM, err := newGCM(dek)
	if err != nil {
		return envelope{}, err
	}
	ciphertext := contentGCM.Seal(nil, nonce, plaintext, nil)
	kekGCM, err := newGCM(kek)
	if err != nil {
		return envelope{}, err
	}
	wrapped := kekGCM.Seal(nil, dekNonce, dek, nil)
	return envelope{WrappedDEK: wrapped, DEKNonce: dekNonce, Ciphertext: ciphertext, Nonce: nonce}, nil
}

// openBody decifra um envelope sob a KEK dada (inverso de [sealBody]). Fail-closed:
// uma KEK ausente/errada ou um blob adulterado falha na autenticação do GCM.
func openBody(kek []byte, e envelope) ([]byte, error) {
	kekGCM, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	dek, err := kekGCM.Open(nil, e.DEKNonce, e.WrappedDEK, nil)
	if err != nil {
		return nil, err
	}
	contentGCM, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	plaintext, err := contentGCM.Open(nil, e.Nonce, e.Ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

// newGCM constrói um AEAD AES-GCM a partir de uma chave de 32 bytes (AES-256).
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// bodyHash é o HASH do ciphertext de envelope que a hash-chain sela (serialização
// canónica JSON de campos com ordem fixa). NÃO depende do plaintext nem da KEK: apagar
// a chave não altera este hash, pelo que a cadeia continua a verificar.
func bodyHash(e envelope) ([]byte, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// hexHash formata um hash em hex.
func hexHash(h []byte) string { return hex.EncodeToString(h) }
