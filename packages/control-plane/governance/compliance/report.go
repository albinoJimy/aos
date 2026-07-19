package compliance

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// IntegrityProof é a prova, embutida no relatório, de que este DERIVA de um
// intervalo do audit VERIFICADO (AC4). Range + o EntryHash do head verificado
// ancoram o relatório à hash-chain: recomputar [audit.Verify] sobre o mesmo range e
// comparar o HeadEntryHashHex re-liga o relatório à cadeia tamper-evident. Nunca se
// gera um relatório com Verified=false — a sua presença é a garantia de integridade.
// IntegrityProof é a prova de que o relatório deriva de um intervalo [From,To] do
// audit que passou a verificação de hash-chain ([audit.Verify]).
//
// LIMITAÇÃO (inerente às hash-chains, ADR-010): [audit.Verify] prova a INTEGRIDADE
// ENCADEADA dos registos PRESENTES no intervalo, mas NÃO detecta uma TRUNCATURA DE
// TAIL (registos removidos do fim, ou um [To] passado aquém do head real) nem uma
// cadeia inteiramente reescrita da génese — a cadeia truncada/reescrita é internamente
// consistente. Para integridade de GRAU REGULATÓRIO, ancorar o head a um [audit.Checkpoint]
// ASSINADO e verificar com [audit.VerifyFromCheckpointAtHead] (o expectedHead conhecido
// independentemente do store); a assinatura do checkpoint é o único backstop contra a
// truncatura/rollback. Follow-up: aceitar um checkpoint opcional em [GenerateReport].
type IntegrityProof struct {
	Partition        string
	From             uint64
	To               uint64
	HeadEntryHashHex string
	Verified         bool
}

// Attribution é a atribuição de acções a um HUMANO responsável (AC3): quantas
// acções e por que agentes (NHIIDs) sob a autoridade delegada desse humano. Só
// identificadores de responsabilização, sem PII.
type Attribution struct {
	// Human é a raiz humana da cadeia (ex.: "human:alice").
	Human string
	// Actions é o nº de acções atribuídas a este humano no intervalo.
	Actions int
	// Principals são os NHIIDs distintos que agiram sob este humano (ordenados).
	Principals []string
}

// PDPDecisions é a agregação das decisões PDP mediadas (AC3): permit/deny/escalate.
type PDPDecisions struct {
	Permits   int
	Denies    int
	Escalates int
}

// Total é o nº total de decisões PDP projectadas.
func (d PDPDecisions) Total() int { return d.Permits + d.Denies + d.Escalates }

// HITLApprovals é a agregação das aprovações HITL + o override-rate (AC3, AOS-095).
// O override-rate é a fracção de decisões PROMPTED que foram APROVADAS (mesma
// definição de hitl.Metrics.OverrideRate) — o sinal anti rubber-stamping.
type HITLApprovals struct {
	// Prompted é o nº de decisões HITL projectadas (o denominador do override-rate).
	Prompted int
	// Approved e Denied particionam Prompted pela decisão selada.
	Approved int
	Denied   int
	// Unauthenticated é o nº de decisões em quarentena (sem aprovador autenticado).
	Unauthenticated int
	// OverrideRate = Approved/Prompted em [0,1] (0 se nada foi prompted).
	OverrideRate float64
}

// DSAREvent projecta um evento do fluxo DSAR (AC3, AOS-093). O Subject é o
// pseudónimo do titular (o mesmo que ancora a chave por-titular), NUNCA PII em claro:
// um titular shredded aparece aqui como REFERÊNCIA (AC5).
type DSAREvent struct {
	// Event é o verbo selado (dsar.received/key_destroyed/blocked).
	Event string
	// Subject é o pseudónimo do titular (referência, não PII).
	Subject string
	// RequestID correlaciona o pedido DSAR (opaco, sem PII).
	RequestID string
	// Stores são os rótulos dos stores shredded (se o evento os enumera).
	Stores []string
	// Partition e AuditSeq localizam o evento na cadeia.
	Partition string
	AuditSeq  uint64
}

// SovereigntyEvent projecta um evento de SOBERANIA por região (AC3, AOS-094):
// uma acção governada pela fronteira regional do board (obrigação `region`) ou cujo
// recurso-alvo carrega uma região. Só metadados de governação, sem PII.
type SovereigntyEvent struct {
	// Region é a região autorizada/alvo (ex.: "eu").
	Region string
	// Decision é o veredicto da acção governada pela soberania.
	Decision audit.Decision
	// Capability e Resource identificam a acção (sem PII).
	Capability string
	Resource   string
	// Partition e AuditSeq localizam o evento na cadeia.
	Partition string
	AuditSeq  uint64
}

// ComplianceReport é o relatório de conformidade — uma PROJECÇÃO query-time sobre os
// AuditRecords do Store (AC3), derivada de um intervalo VERIFICADO (AC4), sem PII em
// claro (AC5). NÃO duplica dados: cada secção agrega os registos selados.
type ComplianceReport struct {
	GeneratedAt time.Time
	Integrity   IntegrityProof
	Attribution []Attribution
	PDP         PDPDecisions
	HITL        HITLApprovals
	DSARs       []DSAREvent
	Sovereignty []SovereigntyEvent
	// Anomalies são as ACÇÕES ANÓNIMAS detectadas (AC1). Vazio ⇒ sem execuções
	// anónimas (a condição conforme). Não-vazio ⇒ [GenerateReport] sinaliza
	// [ErrAnonymousAction] (fail-closed).
	Anomalies []AnonymousAction
}

// HasAnonymousActions indica se o relatório detectou execuções anónimas (AC1).
func (r *ComplianceReport) HasAnonymousActions() bool { return len(r.Anomalies) > 0 }

// Clean indica se o relatório é CONFORME: integridade verificada E sem acções
// anónimas.
func (r *ComplianceReport) Clean() bool {
	return r.Integrity.Verified && !r.HasAnonymousActions()
}

// generator detém as dependências injectáveis do [GenerateReport].
type generator struct {
	tracer   otelgenai.Tracer
	verifier *AccountabilityVerifier
	now      func() time.Time
}

// Option configura o [GenerateReport].
type Option func(*generator)

// WithTracer liga um [otelgenai.Tracer] (default NoopTracer) para o span da geração.
func WithTracer(tr otelgenai.Tracer) Option {
	return func(g *generator) {
		if tr != nil {
			g.tracer = tr
		}
	}
}

// WithVerifier sobrepõe o [AccountabilityVerifier] (default [NewAccountabilityVerifier]).
func WithVerifier(v *AccountabilityVerifier) Option {
	return func(g *generator) {
		if v != nil {
			g.verifier = v
		}
	}
}

// WithClock injecta o relógio de GeneratedAt (default time.Now). Nunca time.Now()
// directo em código testável — padrão do repo.
func WithClock(now func() time.Time) Option {
	return func(g *generator) {
		if now != nil {
			g.now = now
		}
	}
}

// Nomes de operação e atributos do span OTel da geração (DoD AOS-097). São aos.* e
// NUNCA transportam PII/segredos — só o range, as contagens e a integridade.
const (
	opGenerateReport = "aos.compliance.report"

	attrPartition   = "aos.compliance.partition"
	attrFrom        = "aos.compliance.from"
	attrTo          = "aos.compliance.to"
	attrVerified    = "aos.compliance.integrity_verified"
	attrPermits     = "aos.compliance.pdp_permits"
	attrDenies      = "aos.compliance.pdp_denies"
	attrEscalates   = "aos.compliance.pdp_escalates"
	attrHITL        = "aos.compliance.hitl_prompted"
	attrOverride    = "aos.compliance.hitl_override_rate"
	attrDSARs       = "aos.compliance.dsar_count"
	attrSovereignty = "aos.compliance.sovereignty_count"
	attrAnomalies   = "aos.compliance.anonymous_actions"
	attrError       = otelgenai.AttrErrorType
)

// GenerateReport gera o [ComplianceReport] do intervalo [from,to] da partição, como
// projecção query-time sobre o audit tamper-evident (AC3). Passos:
//
//  1. INTEGRIDADE (AC4): corre [audit.Verify] sobre [from,to]. Se a cadeia estiver
//     adulterada, devolve (nil, [ErrTamperedAudit]) — NÃO se gera relatório sobre
//     audit adulterado.
//  2. PROJECÇÃO (AC3/AC5): lê os registos e projecta cada secção usando SÓ campos
//     não-pessoais (nunca decifra [audit.PayloadRef]).
//  3. COMPLETUDE (AC1): corre o [AccountabilityVerifier] sobre as acções; as anónimas
//     vão para [ComplianceReport.Anomalies].
//  4. PROVA: embute a [IntegrityProof] (range + EntryHash do head verificado).
//
// Se houver acções anónimas, devolve o relatório (não-nil, para forense) MAIS
// [ErrAnonymousAction] — o chamador DEVE tratar o erro (fail-closed, AC1). Um
// intervalo válido sem registos devolve um relatório vazio verificado.
//
// Emite um span OTel (sem PII) com o range, as contagens e a integridade.
func GenerateReport(ctx context.Context, store audit.Store, partition string, from, to uint64, opts ...Option) (*ComplianceReport, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	g := &generator{
		tracer:   otelgenai.NoopTracer{},
		verifier: NewAccountabilityVerifier(),
		now:      time.Now,
	}
	for _, o := range opts {
		o(g)
	}

	ctx, span := g.tracer.StartSpan(ctx, opGenerateReport)
	defer span.End()
	span.SetAttribute(attrPartition, partition)
	span.SetAttribute(attrFrom, int64(from))
	span.SetAttribute(attrTo, int64(to))

	// 1. INTEGRIDADE (AC4): não se projecta sobre audit adulterado.
	if err := audit.Verify(ctx, store, partition, from, to); err != nil {
		span.SetAttribute(attrVerified, false)
		span.SetAttribute(attrError, "integrity")
		return nil, fmt.Errorf("%w: %w", ErrTamperedAudit, err)
	}
	span.SetAttribute(attrVerified, true)

	// 2. Leitura do intervalo verificado (fonte única — sem duplicação).
	recs, err := store.Read(ctx, partition, from, to)
	if err != nil {
		span.SetAttribute(attrError, "read")
		return nil, fmt.Errorf("compliance: ler registos [%d,%d] da particao %q: %w", from, to, partition, err)
	}

	// 3. PROJECÇÃO query-time de cada secção (AC3/AC5).
	report := &ComplianceReport{
		GeneratedAt: g.now().UTC(),
		Attribution: projectAttribution(recs, g.verifier),
		PDP:         projectPDP(recs),
		HITL:        projectHITL(recs),
		DSARs:       projectDSARs(recs),
		Sovereignty: projectSovereignty(recs),
		Anomalies:   g.verifier.Verify(recs),
	}

	// 4. PROVA de integridade (AC4): EntryHash do head verificado do intervalo.
	report.Integrity = buildIntegrityProof(ctx, store, partition, from, to)

	// Atributos de observabilidade (sem PII).
	span.SetAttribute(attrPermits, int64(report.PDP.Permits))
	span.SetAttribute(attrDenies, int64(report.PDP.Denies))
	span.SetAttribute(attrEscalates, int64(report.PDP.Escalates))
	span.SetAttribute(attrHITL, int64(report.HITL.Prompted))
	span.SetAttribute(attrOverride, report.HITL.OverrideRate)
	span.SetAttribute(attrDSARs, int64(len(report.DSARs)))
	span.SetAttribute(attrSovereignty, int64(len(report.Sovereignty)))
	span.SetAttribute(attrAnomalies, int64(len(report.Anomalies)))

	if report.HasAnonymousActions() {
		// Fail-closed (AC1): o relatório vai não-nil (forense), mas o erro sinaliza a
		// violação do modelo de responsabilização.
		span.SetAttribute(attrError, "anonymous_action")
		return report, ErrAnonymousAction
	}
	return report, nil
}

// projectAttribution agrupa as ACÇÕES com principal COMPLETO pelo humano responsável
// (raiz da cadeia) e conta as acções e os agentes distintos. As acções anónimas NÃO
// entram aqui — são sinalizadas em Anomalies; a atribuição só cobre o que É
// atribuível.
func projectAttribution(recs []audit.AuditRecord, v *AccountabilityVerifier) []Attribution {
	type agg struct {
		actions    int
		principals map[string]struct{}
	}
	byHuman := make(map[string]*agg)
	for _, rec := range recs {
		if classify(rec) != EventAction {
			continue
		}
		if ok, _ := v.principalComplete(rec.Principal); !ok {
			continue // anónima: tratada em Anomalies, não atribuível a um humano
		}
		human := rec.Principal.DelegationChain[0].Sub
		a := byHuman[human]
		if a == nil {
			a = &agg{principals: make(map[string]struct{})}
			byHuman[human] = a
		}
		a.actions++
		if rec.Principal.NHIID != "" {
			a.principals[rec.Principal.NHIID] = struct{}{}
		}
	}
	out := make([]Attribution, 0, len(byHuman))
	for human, a := range byHuman {
		principals := make([]string, 0, len(a.principals))
		for p := range a.principals {
			principals = append(principals, p)
		}
		sort.Strings(principals)
		out = append(out, Attribution{Human: human, Actions: a.actions, Principals: principals})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Human < out[j].Human })
	return out
}

// projectPDP conta as decisões PDP mediadas (só acções — não os eventos de
// governação).
func projectPDP(recs []audit.AuditRecord) PDPDecisions {
	var d PDPDecisions
	for _, rec := range recs {
		if classify(rec) != EventAction {
			continue
		}
		switch rec.Decision {
		case audit.DecisionAllow:
			d.Permits++
		case audit.DecisionDeny:
			d.Denies++
		case audit.DecisionEscalate:
			d.Escalates++
		}
	}
	return d
}

// projectHITL agrega as decisões HITL e deriva o override-rate. Conta UMA vez por
// decisão (o registo que carrega a obrigação hitl_decision), particionando por
// veredicto e por autenticação.
func projectHITL(recs []audit.AuditRecord) HITLApprovals {
	var h HITLApprovals
	for _, rec := range recs {
		if classify(rec) != EventHITL || !hasObligation(rec, obHITLDecision) {
			continue
		}
		h.Prompted++
		switch rec.Decision {
		case audit.DecisionAllow:
			h.Approved++
		default:
			h.Denied++
		}
		if hasObligation(rec, obHITLUnauthed) {
			h.Unauthenticated++
		}
	}
	if h.Prompted > 0 {
		h.OverrideRate = float64(h.Approved) / float64(h.Prompted)
	}
	return h
}

// projectDSARs projecta os eventos do fluxo DSAR — o Subject é o pseudónimo
// (referência), nunca PII.
func projectDSARs(recs []audit.AuditRecord) []DSAREvent {
	out := make([]DSAREvent, 0)
	for _, rec := range recs {
		if classify(rec) != EventDSAR {
			continue
		}
		ev := DSAREvent{
			Event:     rec.Capability,
			Subject:   rec.Resource.Value, // pseudónimo do titular (não PII)
			RequestID: rec.RequestID,
			Partition: rec.Partition,
			AuditSeq:  rec.AuditSeq,
		}
		if ob, ok := obligationOf(rec, labelDSARStores); ok && len(ob.Fields) > 0 {
			ev.Stores = append([]string(nil), ob.Fields...)
		}
		out = append(out, ev)
	}
	return out
}

// projectSovereignty projecta os eventos de soberania: acções com a obrigação
// `region` (fronteira do board imposta) ou cujo recurso-alvo carrega uma região. A
// obrigação tem prioridade como fonte da região autorizada.
func projectSovereignty(recs []audit.AuditRecord) []SovereigntyEvent {
	out := make([]SovereigntyEvent, 0)
	for _, rec := range recs {
		region, governed := sovereigntyRegion(rec)
		if !governed {
			continue
		}
		out = append(out, SovereigntyEvent{
			Region:     region,
			Decision:   rec.Decision,
			Capability: rec.Capability,
			Resource:   rec.Resource.Value,
			Partition:  rec.Partition,
			AuditSeq:   rec.AuditSeq,
		})
	}
	return out
}

// sovereigntyRegion devolve a região de soberania de um registo e se ele é governado
// por soberania: a obrigação `region` (Params["region"]) é a fonte preferida; na sua
// ausência, uma Resource.Region não-vazia. Devolve ("", false) se nenhuma se aplica.
func sovereigntyRegion(rec audit.AuditRecord) (string, bool) {
	if ob, ok := obligationOf(rec, obRegion); ok {
		if r := ob.Params[paramRegion]; r != "" {
			return r, true
		}
		// Obrigação region sem param explícito: cai na Resource.Region se houver.
		if rec.Resource.Region != "" {
			return rec.Resource.Region, true
		}
		return "", true
	}
	if rec.Resource.Region != "" {
		return rec.Resource.Region, true
	}
	return "", false
}

// buildIntegrityProof lê o registo do head verificado (`to`) e embute o seu EntryHash
// como âncora da prova. O intervalo já passou [audit.Verify] neste ponto; a leitura
// do head reconfirma a existência e serve de raiz para re-verificação externa.
func buildIntegrityProof(ctx context.Context, store audit.Store, partition string, from, to uint64) IntegrityProof {
	proof := IntegrityProof{Partition: partition, From: from, To: to, Verified: true}
	if rec, ok, err := store.At(ctx, partition, to); err == nil && ok {
		proof.HeadEntryHashHex = hex.EncodeToString(rec.EntryHash)
	}
	return proof
}
