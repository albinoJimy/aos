package provenance

import (
	"context"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
)

// ProvenanceError é o tipo de erro sentinela do pacote de proveniência. Carrega um
// código estável comparável com errors.Is. Toda a decisão resolve-se pelo lado
// seguro (fail-closed).
type ProvenanceError struct {
	Code string
	msg  string
}

func (e *ProvenanceError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrNilAuditStore — construção de um [Promoter] sem Store de audit. A promoção
	// TEM de ser auditável; sem hash-chain não há promoção (fail-closed).
	ErrNilAuditStore = &ProvenanceError{Code: "E_PROV_NIL_AUDIT_STORE", msg: "audit store obrigatorio para promocao auditavel"}

	// ErrNotQuarantined — pedido de promoção de um registo que NÃO está em
	// quarentena (já é trusted). Não há o que promover; promover trusted seria um
	// no-op perigoso que se rejeita explicitamente.
	ErrNotQuarantined = &ProvenanceError{Code: "E_PROV_NOT_QUARANTINED", msg: "so memoria untrusted (em quarentena) pode ser promovida"}

	// ErrPromotionNotValidated — promoção sem validação EXPLÍCITA (método inválido
	// ou validador vazio). Não há promoção silenciosa/automática (fail-closed).
	ErrPromotionNotValidated = &ProvenanceError{Code: "E_PROV_NOT_VALIDATED", msg: "promocao untrusted->trusted exige validacao explicita (politica ou humano)"}

	// ErrSealTrustedForbidden — tentativa de [Seal] de um registo marcado trusted.
	// Seal não pode fabricar control-plane a partir de uma tag in-band (não é
	// separação de privilégio); a memória trusted só nasce de [Ingestor.Ingest]
	// (classificação pela fonte) ou de [Promoter.Promote] (audit-chain).
	ErrSealTrustedForbidden = &ProvenanceError{Code: "E_PROV_SEAL_TRUSTED_FORBIDDEN", msg: "Seal nao pode produzir memoria trusted a partir de uma tag in-band; usar Ingest(Classify) ou Promote(audit)"}
)

// ValidationMethod é a forma de validação que autoriza uma promoção. Não há
// promoção automática: uma promoção válida tem de ser por política assinada OU por
// decisão humana.
type ValidationMethod string

const (
	// ValidationPolicy — validação por política-as-code explícita (ex.: eval-gate,
	// regra de curadoria). O Validator identifica a política.
	ValidationPolicy ValidationMethod = "policy"
	// ValidationHuman — validação por revisor humano no circuito. O Validator
	// identifica o humano (ex.: o principal que ratificou).
	ValidationHuman ValidationMethod = "human"
)

// Valid indica se m é um método de validação canónico.
func (m ValidationMethod) Valid() bool {
	return m == ValidationPolicy || m == ValidationHuman
}

// PromotionRequest é o pedido de promoção de uma entrada em quarentena para
// trusted. A validação é OBRIGATÓRIA e explícita; sem ela, a promoção falha-fecha.
type PromotionRequest struct {
	// Entry é a entrada em quarentena a promover (tem de ser untrusted).
	Entry Ingested
	// Method é a forma de validação (política ou humano).
	Method ValidationMethod
	// Validator identifica QUEM/QUE validou (id de política ou principal humano).
	// Vazio = promoção não validada (rejeitada).
	Validator string
	// Justification é a justificação de auditoria (opcional, selada na cadeia).
	Justification string
	// AgentID e RunID atribuem a promoção à NHI e ao run (accountability).
	AgentID string
	RunID   string
	// AuditPartition é a fronteira de encadeamento do audit (default: RunID, ou
	// "global" se RunID vazio).
	AuditPartition string
}

// Promoter promove memória em quarentena (untrusted) para trusted, exigindo
// validação EXPLÍCITA e registando a promoção na hash-chain tamper-evident de
// AOS-011. Não há promoção silenciosa: cada promoção deixa um registo selado.
type Promoter struct {
	store  audit.Store
	now    func() time.Time
	tracer agentruntime.Tracer
}

// PromoterOption configura o [Promoter].
type PromoterOption func(*Promoter)

// WithClock injecta o relógio do timestamp observacional do audit (default:
// time.Now). Injectar um relógio fixo torna a promoção determinística em testes.
func WithClock(now func() time.Time) PromoterOption {
	return func(p *Promoter) {
		if now != nil {
			p.now = now
		}
	}
}

// WithPromoteTracer injecta o Tracer dos spans de promoção (default: NoopTracer).
func WithPromoteTracer(tr agentruntime.Tracer) PromoterOption {
	return func(p *Promoter) {
		if tr != nil {
			p.tracer = tr
		}
	}
}

// NewPromoter constrói um Promoter sobre uma [audit.Store]. Fail-closed: um store
// nil é [ErrNilAuditStore] — a promoção tem de ser auditável.
func NewPromoter(store audit.Store, opts ...PromoterOption) (*Promoter, error) {
	if store == nil {
		return nil, ErrNilAuditStore
	}
	p := &Promoter{store: store, now: time.Now, tracer: agentruntime.NoopTracer{}}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Promote promove a entrada em quarentena para trusted. Passos (todos fail-closed):
//
//  1. a entrada TEM de estar em quarentena (untrusted) — senão [ErrNotQuarantined];
//  2. a validação TEM de ser explícita (método canónico + validador não vazio) —
//     senão [ErrPromotionNotValidated];
//  3. regista a promoção na hash-chain de audit (AOS-011) — DecisionAllow, o taint
//     de ORIGEM (untrusted) e a validação seladas no registo;
//  4. devolve uma NOVA [Ingested] trusted (o original untrusted permanece imutável)
//     e o registo de audit SELADO.
//
// A promoção NÃO muta a entrada de origem: cria um registo trusted novo (coerente
// com o event-sourcing — não há update-in-place). Assim a proveniência selada
// nunca é alterada; o histórico untrusted continua auditável.
func (p *Promoter) Promote(ctx context.Context, req PromotionRequest) (Ingested, audit.AuditRecord, error) {
	_, span := p.tracer.StartSpan(ctx, opPromote)
	defer span.End()
	span.SetAttribute(attrOperation, opPromote)
	span.SetAttribute(attrFrom, string(Untrusted))
	span.SetAttribute(attrTo, string(Trusted))

	// Só memória canonicamente UNTRUSTED (em quarentena) é promovível. Reafirma-se
	// pelo lado positivo (== Untrusted), não pela negação de Trusted: assim um
	// Ingested zero-value (prov=="") ou com proveniência não-canónica é rejeitado em
	// vez de escorregar a guarda e ser promovido a trusted (fail-closed).
	if req.Entry.prov != Untrusted {
		span.SetAttribute(attrResult, "rejected")
		return Ingested{}, audit.AuditRecord{}, ErrNotQuarantined
	}
	if !req.Method.Valid() || req.Validator == "" {
		span.SetAttribute(attrResult, "rejected")
		return Ingested{}, audit.AuditRecord{}, ErrPromotionNotValidated
	}
	// Revalida a memória a promover ANTES de tocar na cadeia de audit: só memória
	// untrusted VÁLIDA é promovível, e uma promoção rejeitada não deixa rasto de
	// audit (fail-closed antes do Append).
	promoted := req.Entry.rec.Clone()
	promoted.Metadata.Provenance = Trusted
	if err := promoted.Validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return Ingested{}, audit.AuditRecord{}, err
	}
	span.SetAttribute(attrMethod, string(req.Method))
	span.SetAttribute(attrValidator, req.Validator)

	partition := req.AuditPartition
	if partition == "" {
		if req.RunID != "" {
			partition = req.RunID
		} else {
			partition = "global"
		}
	}

	rec := audit.AuditRecord{
		Partition:  partition,
		Timestamp:  p.now().UTC(),
		Decision:   audit.DecisionAllow,
		Principal:  audit.Principal{NHIID: req.AgentID},
		Capability: "memory:promote:untrusted->trusted",
		RunID:      req.RunID,
		ToolID:     "memory.provenance.promote",
		Resource: audit.Resource{
			Type:  "memory",
			Value: req.Entry.rec.Class.String() + "/" + req.Entry.rec.ID,
		},
		// O taint de ORIGEM é selado: a cadeia prova que a promoção partiu de
		// untrusted (nunca uma promoção silenciosa de conteúdo já trusted).
		Context: audit.CallContext{Taint: string(Untrusted)},
		Obligations: []audit.Obligation{{
			Type: "memory_promotion",
			Params: map[string]string{
				"method":        string(req.Method),
				"validator":     req.Validator,
				"justification": req.Justification,
				"from":          string(Untrusted),
				"to":            string(Trusted),
				"mem_id":        req.Entry.rec.ID,
				"mem_class":     req.Entry.rec.Class.String(),
			},
		}},
	}

	sealed, err := p.store.Append(ctx, rec)
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return Ingested{}, audit.AuditRecord{}, err
	}
	span.SetAttribute(attrAuditSeq, sealed.AuditSeq)
	span.SetAttribute(attrResult, "promoted")

	// Novo registo trusted (já validado; o original untrusted permanece imutável).
	return Ingested{rec: promoted, prov: Trusted}, sealed, nil
}
