package dsar

import (
	"context"
	"errors"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Tipos de evento auditável do fluxo DSAR, selados na hash-chain (AC6). Ficam no
// campo Capability do AuditRecord — vocabulário estável para relatórios de
// conformidade (AOS-097) lerem os DSARs a partir do audit.
const (
	// EventReceived — o pedido DSAR foi recebido (início do fluxo).
	EventReceived = "dsar.received"
	// EventKeyDestroyed — a(s) chave(s) do titular foram destruídas (apagamento
	// satisfeito); carrega o timestamp e os rótulos dos stores afectados.
	EventKeyDestroyed = "dsar.key_destroyed"
	// EventBlocked — o apagamento foi BLOQUEADO (legal hold ou store que recusou);
	// nenhuma chave foi destruída para além das já listadas no evento.
	EventBlocked = "dsar.blocked"
)

const (
	// component identifica o produtor do evento (ToolID), sem PII.
	component = "gov.dsar"
	// subjectResourceType rotula o Resource cujo Value é o subjectID pseudónimo.
	subjectResourceType = "dsar.subject"
	// storesObligation rotula a obrigação que enumera os stores shredded (rótulos).
	storesObligation = "dsar.stores"
	// defaultPartition é a partição de audit onde os eventos DSAR são encadeados
	// quando o pedido não a sobrepõe.
	defaultPartition = "governance.dsar"

	opErase       = "gov.dsar.erase"
	attrSubject   = "aos.dsar.subject_id"
	attrRequest   = "aos.dsar.request_id"
	attrPartition = "aos.dsar.partition"
	attrBlocked   = "aos.dsar.blocked"
	attrStores    = "aos.dsar.stores_destroyed"
	attrOutcome   = "aos.dsar.outcome"
	attrStage     = "aos.dsar.error_stage"
	attrPartial   = "aos.dsar.partial_erasure"
)

// Request é um pedido DSAR de apagamento. NÃO carrega qualquer valor pessoal: o
// SubjectID é o identificador PSEUDÓNIMO do titular (o mesmo que ancora a chave
// por-titular no vault), nunca o dado pessoal em si.
type Request struct {
	// RequestID é o id opaco do pedido DSAR (correlação/idempotência externa), sem PII.
	RequestID string
	// SubjectID é o titular cujas chaves de PII serão destruídas (pseudónimo).
	SubjectID string
	// Partition sobrepõe (opcional) a partição de audit onde os eventos são selados.
	Partition string
}

// Result é o desfecho de um [Flow.Receive]. Os *Seq referenciam os audit_seq dos
// eventos selados (prova de auditabilidade); StoresShredded lista os rótulos dos
// stores efectivamente destruídos. Sem PII nem chave.
type Result struct {
	RequestID      string
	SubjectID      string
	Blocked        bool
	ReceivedSeq    uint64
	OutcomeSeq     uint64
	StoresShredded []string
	// Partial sinaliza uma erasure PARCIAL e IRREVERSÍVEL: o apagamento foi
	// BLOQUEADO (Blocked=true) DEPOIS de já ter destruído a chave em pelo menos um
	// store (StoresShredded não-vazio). Só pode ocorrer na janela TOCTOU em que um
	// legal hold é colocado a meio da erasure unificada — o que já foi destruído não
	// se recupera. Blocked=true com Partial=false ⇒ nenhuma chave foi destruída (o
	// caso normal do bloqueio); Blocked=true com Partial=true exige atenção
	// operacional (a PII de parte dos stores ficou irrecuperável apesar do hold).
	Partial bool
}

// Flow é o orquestrador do fluxo DSAR (AOS-093). Compõe os stores shreddable por
// porta e sela os eventos de conformidade na hash-chain. Reutilizável e seguro para
// uso sequencial; a concorrência é a dos stores/sealer subjacentes.
type Flow struct {
	sealer    EventSealer
	holds     HoldOracle
	stores    []ShreddableKeyStore
	partition string
	tracer    otelgenai.Tracer
	now       func() time.Time
}

// Option configura o [Flow].
type Option func(*Flow)

// WithClock injecta o relógio (default time.Now) — os eventos selados timestampam
// por aqui, para determinismo em teste.
func WithClock(now func() time.Time) Option {
	return func(f *Flow) {
		if now != nil {
			f.now = now
		}
	}
}

// WithTracer liga um [otelgenai.Tracer] (default NoopTracer). O span do fluxo
// carrega só subjectID/rótulos/desfecho — NUNCA PII nem chave.
func WithTracer(tr otelgenai.Tracer) Option {
	return func(f *Flow) {
		if tr != nil {
			f.tracer = tr
		}
	}
}

// WithPartition define a partição de audit onde os eventos DSAR são encadeados
// (default "governance.dsar"). Um Request pode sobrepô-la por pedido.
func WithPartition(partition string) Option {
	return func(f *Flow) {
		if strings.TrimSpace(partition) != "" {
			f.partition = partition
		}
	}
}

// NoHold é o HoldOracle EXPLÍCITO para sistemas sem legal holds: reporta sempre
// "não retido". É o opt-in AUDITÁVEL para renunciar à preservação — um fluxo DSAR
// construído com nil (em vez de NoHold{}) recusa o apagamento fail-closed
// ([ErrNoHoldOracle]), para o fail-open nunca ser um default silencioso (garantia P0).
type NoHold struct{}

// Held implementa [HoldOracle]: nunca retido (opt-in explícito de sem-preservação).
func (NoHold) Held(string) bool { return false }

// NewFlow constrói o fluxo sobre o [EventSealer] (audit), o [HoldOracle] (legal
// hold) e os [ShreddableKeyStore] a apagar. holds nil é RECUSADO fail-closed em
// [Flow.Receive] ([ErrNoHoldOracle]) — sem-preservação exige um [NoHold]{} explícito;
// stores vazio é permitido (o fluxo apenas regista received/destroyed
// sem nada a destruir). O sealer é obrigatório — validado em [Flow.Receive].
func NewFlow(sealer EventSealer, holds HoldOracle, stores []ShreddableKeyStore, opts ...Option) *Flow {
	f := &Flow{
		sealer:    sealer,
		holds:     holds,
		stores:    append([]ShreddableKeyStore(nil), stores...),
		partition: defaultPartition,
		tracer:    otelgenai.NoopTracer{},
		now:       time.Now,
	}
	for _, o := range opts {
		o(f)
	}
	// holds NÃO cai num default silencioso: um nil é recusado fail-closed em
	// [Flow.Receive] ([ErrNoHoldOracle]). Para renunciar à preservação, o caller passa
	// um [NoHold]{} EXPLÍCITO. Isto evita o fail-open silencioso da garantia P0.
	if f.tracer == nil {
		f.tracer = otelgenai.NoopTracer{}
	}
	if f.now == nil {
		f.now = time.Now
	}
	return f
}

// Receive satisfaz um pedido DSAR de apagamento (Art. 17). Passos, todos auditáveis
// e fail-closed:
//
//  1. valida o subjectID (vazio ⇒ [ErrNoSubject], antes de qualquer selagem);
//  2. sela dsar.received (metadados, sem PII);
//  3. consulta o legal hold — retido ⇒ sela dsar.blocked, preserva as chaves e
//     devolve [ErrLegalHold] (Blocked=true no Result);
//  4. destrói a chave do titular em CADA store (erasure unificada), RE-CONSULTANDO o
//     legal hold imediatamente antes de cada shred — a enforcement do hold é
//     AUTORITATIVA por-store, não dependente da ordem de wiring dos stores (fecha a
//     janela TOCTOU mesmo para stores sem re-check próprio, ex.: redaction); se um
//     store recusar, sela dsar.blocked e aborta (não sela key_destroyed sobre
//     erasure incompleta);
//  5. sela dsar.key_destroyed (timestamp + rótulos dos stores).
//
// Idempotente: re-submeter um titular já apagado volta a selar received/destroyed
// (factos de conformidade) e o shred das chaves ausentes é no-op. A chave NUNCA é
// devolvida nem exposta.
func (f *Flow) Receive(ctx context.Context, req Request) (Result, error) {
	subject := strings.TrimSpace(req.SubjectID)
	if subject == "" {
		return Result{}, ErrNoSubject
	}
	if f.sealer == nil {
		return Result{}, ErrNoSealer
	}
	// Legal hold é uma garantia de preservação P0: sem um oráculo de hold, o fluxo
	// não pode provar que o titular não está retido e RECUSA fail-closed (antes de
	// qualquer selagem/destruição). Sem-preservação exige um [NoHold]{} explícito.
	if f.holds == nil {
		return Result{}, ErrNoHoldOracle
	}
	partition := f.partition
	if p := strings.TrimSpace(req.Partition); p != "" {
		partition = p
	}

	ctx, span := f.tracer.StartSpan(ctx, opErase)
	defer span.End()
	span.SetAttribute(attrSubject, subject)
	span.SetAttribute(attrRequest, req.RequestID)
	span.SetAttribute(attrPartition, partition)

	res := Result{RequestID: req.RequestID, SubjectID: subject}

	// 1. dsar.received — o pedido é um facto de conformidade selado.
	recv, err := f.seal(ctx, partition, subject, req.RequestID, EventReceived, audit.DecisionAllow, nil)
	if err != nil {
		span.SetAttribute(attrStage, "received")
		return res, err
	}
	res.ReceivedSeq = recv.AuditSeq

	// 2. legal hold (subject OU partição) — fail-closed: nada é destruído.
	if f.holds.Held(subject) {
		return f.sealBlocked(ctx, span, res, partition, subject, req.RequestID, nil, "blocked_legal_hold", ErrLegalHold)
	}

	// 3. erasure unificada: destruir a chave por-titular em CADA store. O legal hold é
	// RE-CONSULTADO imediatamente antes de cada shred — a enforcement é AUTORITATIVA
	// por-store, não dependente da ordem de wiring: fecha a janela TOCTOU em que um
	// hold é colocado após o gate inicial mas antes deste store ser alcançado,
	// incluindo stores que não re-checam o hold internamente (ex.: redaction).
	destroyed := make([]string, 0, len(f.stores))
	for _, st := range f.stores {
		if f.holds.Held(subject) {
			// Hold apareceu durante a erasure: bloqueia ANTES de tocar neste store.
			// destroyed pode já conter stores destruídos (erasure parcial irreversível
			// — sinalizada em Result.Partial e no evento/span).
			return f.sealBlocked(ctx, span, res, partition, subject, req.RequestID, destroyed, "blocked_legal_hold", ErrLegalHold)
		}
		if serr := st.Shred(subject); serr != nil {
			// Fail-closed: um store recusou (ex.: legal hold re-checado no store). Sela
			// dsar.blocked com o que já foi destruído e aborta — NÃO se sela
			// key_destroyed sobre uma erasure incompleta.
			outcome, cause := "blocked_store_refused", serr
			if errors.Is(serr, audit.ErrLegalHold) {
				outcome, cause = "blocked_legal_hold", ErrLegalHold
			}
			return f.sealBlocked(ctx, span, res, partition, subject, req.RequestID, destroyed, outcome, cause)
		}
		destroyed = append(destroyed, st.Name())
	}
	res.StoresShredded = destroyed

	// 4. dsar.key_destroyed — apagamento satisfeito, timestamp + rótulos dos stores.
	done, err := f.seal(ctx, partition, subject, req.RequestID, EventKeyDestroyed, audit.DecisionAllow, destroyed)
	if err != nil {
		span.SetAttribute(attrStage, "key_destroyed")
		return res, err
	}
	res.OutcomeSeq = done.AuditSeq
	span.SetAttribute(attrStores, len(destroyed))
	span.SetAttribute(attrOutcome, "erased")
	return res, nil
}

// sealBlocked sela dsar.blocked e devolve o Result fail-closed com a causa dada. É o
// único ponto de bloqueio do fluxo — centraliza a marcação do Result/span para que a
// enforcement do hold seja consistente onde quer que dispare (gate inicial,
// re-check por-store, ou recusa do próprio store). Se alguma chave já foi destruída
// antes do bloqueio (janela TOCTOU: hold a meio da erasure unificada), marca
// Result.Partial e o span — o Blocked=true NÃO esconde a erasure parcial irreversível.
// Nunca expõe PII nem chave: só subjectID/rótulos/desfecho.
func (f *Flow) sealBlocked(ctx context.Context, span otelgenai.Span, res Result, partition, subject, requestID string, destroyed []string, outcome string, cause error) (Result, error) {
	res.Blocked = true
	res.StoresShredded = destroyed
	res.Partial = len(destroyed) > 0
	span.SetAttribute(attrBlocked, true)
	span.SetAttribute(attrOutcome, outcome)
	if res.Partial {
		span.SetAttribute(attrPartial, true)
		span.SetAttribute(attrStores, len(destroyed))
	}
	blk, berr := f.seal(ctx, partition, subject, requestID, EventBlocked, audit.DecisionDeny, destroyed)
	if berr != nil {
		span.SetAttribute(attrStage, "blocked")
		return res, berr
	}
	res.OutcomeSeq = blk.AuditSeq
	return res, cause
}

// seal constrói e sela um AuditRecord de evento DSAR — SEM PII: o RawRecord não leva
// PII, pelo que a ingestão não cria PayloadRef nem cifra nada. O subjectID
// pseudónimo vai no Resource; os rótulos dos stores (se houver) numa Obligation.
func (f *Flow) seal(ctx context.Context, partition, subject, requestID, verb string, decision audit.Decision, stores []string) (audit.AuditRecord, error) {
	rec := audit.AuditRecord{
		Partition:  partition,
		Timestamp:  f.now().UTC(),
		Decision:   decision,
		Capability: verb,
		RequestID:  requestID,
		ToolID:     component,
		Resource:   audit.Resource{Type: subjectResourceType, Value: subject},
	}
	if len(stores) > 0 {
		rec.Obligations = []audit.Obligation{{
			Type:   storesObligation,
			Fields: append([]string(nil), stores...),
		}}
	}
	// RawRecord sem PII ⇒ nenhum PayloadRef, nenhuma cifra: o evento é puramente
	// metadados de responsabilização selados na hash-chain (AC4/AC6).
	return f.sealer.Ingest(ctx, audit.RawRecord{Record: rec})
}
