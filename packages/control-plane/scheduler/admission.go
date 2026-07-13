// admission.go — Admission control GLOBAL (AOS-027, concretização do ADR-008).
//
// O modo de falha central do plano-base é "individualmente ok, agregadamente
// colapsa": 15 boards, cada um dentro do seu max_spawn local, saturam
// colectivamente o rate limit partilhado do provider. A resolução é um
// TOKEN-BUCKET DISTRIBUÍDO sobre o TPM/RPM REAL (lido da porta [QuotaProvider]),
// de modo que a admissão de trabalho seja uma decisão GLOBAL, não a soma de
// decisões locais cegas umas às outras.
//
// RESERVA ATÓMICA SEM SPOF (ADR-007): o estado do bucket vive num stream do
// Event Store replicado (chave = provider:model:region). A reserva de débito é
// um Append CAS (WithExpectedSeq) a esse stream. Workers stateless: cada admit
// relê o estado, calcula o headroom, e tenta reservar via CAS; o perdedor de
// corrida faz retry (reler + reavaliar) ou, sem headroom, ADIA (defer). NÃO há
// single-writer, NÃO há contador em memória partilhado.
//
// REFILL TEMPORIZADO (relógio injectável — sem time.Now no caminho de decisão):
// uma reserva é contabilizada enquanto a sua idade for < Window; findo o prazo,
// é libertada implicitamente (janela deslizante por reserva). Invariante central:
// em qualquer instante, a soma das reservas activas nunca excede o TPM/RPM
// efectivo.
//
// EVENTOS append-only (cada decisão é auditável e o replay reconstrói a
// sequência): admit_requested / admit_deferred vão para o stream de AUDITORIA
// (append simples, tolerante a ordem); admit_granted / quota_released vão para o
// stream de RESERVA (fonte de verdade do débito, com CAS). Separar os streams
// evita que os eventos de auditoria colidam com o CAS da reserva.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento de admissão (append-only). São o contrato auditável do
// AOS-027.
const (
	// EventAdmitRequested marca a chegada de um pedido de admissão (auditoria).
	EventAdmitRequested = "admission.admit_requested"
	// EventAdmitGranted marca a reserva de débito bem-sucedida (fonte de verdade).
	EventAdmitGranted = "admission.admit_granted"
	// EventAdmitDeferred marca um adiamento por falta de headroom (auditoria).
	EventAdmitDeferred = "admission.admit_deferred"
	// EventQuotaReleased marca a libertação/reconciliação de uma reserva.
	EventQuotaReleased = "admission.quota_released"
)

// DefaultAdmissionNHI é a NHI por omissão do admission control nos eventos que
// emite.
const DefaultAdmissionNHI = "nhi:control-plane/scheduler/admission"

// Prefixos de stream. O bucket de reserva e o log de auditoria são streams
// distintos por chave.
const (
	bucketStreamPrefix = "admission/bucket/"
	auditStreamPrefix  = "admission/audit/"
)

// Atributos de span (OTel GenAI-style; reutilizam a convenção zero-dep).
const (
	attrAdmitKey       = "aos.admission.key"
	attrAdmitTenant    = "aos.admission.tenant"
	attrAdmitCost      = "aos.admission.cost_tokens"
	attrAdmitGranted   = "aos.admission.granted"
	attrAdmitHeadroomT = "aos.admission.headroom_tokens"
	attrAdmitHeadroomR = "aos.admission.headroom_requests"
	attrAdmitRetryMs   = "aos.admission.retry_after_ms"
	// attrAdmitUnsatisfiable sinaliza uma rejeição permanente (custo > tecto): o
	// pedido nunca será admitido, distinto de um defer transitório.
	attrAdmitUnsatisfiable = "aos.admission.unsatisfiable"
	// attrAdmitBackpressure marca um defer causado por BACKPRESSURE (fila saturada
	// a montante, AOS-030), distinto de um defer por falta de headroom.
	attrAdmitBackpressure = "aos.admission.backpressure"
)

// ErrIdempotencyConflict é devolvido quando um RequestID já activo é reutilizado
// com um custo estimado DIFERENTE do que foi originalmente reservado. A
// idempotência é sobre o MESMO pedido (mesmo id + mesmo custo); reutilizar a
// chave com outro custo não é um retry — seria oversubscription silenciosa
// (grant cego sem re-debitar o novo custo). Fail-closed: rejeita em vez de
// conceder.
var ErrIdempotencyConflict = errors.New("admission: RequestID reutilizado com custo divergente da reserva activa")

// opAdmit é o nome de operação do span de admissão.
const opAdmit = "admission_control"

// defaultMaxCASRetries limita as re-tentativas de CAS por admit. O progresso é
// garantido (cada CAS perdido corresponde a um evento committed por outro
// worker), mas o cap protege contra livelock patológico.
const defaultMaxCASRetries = 10000

// EventLog é a fatia mínima do Event Store (AOS-002) de que a admissão precisa:
// Append (com CAS via WithExpectedSeq) e Read. *eventstore.Store satisfá-la. A
// admissão NÃO tem outra via de mutar estado — a reserva é sempre um Append CAS.
type EventLog interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// admissionPayload é o corpo serializado (estável) de cada evento de admissão.
// Serialização determinística (campos fixos, sem mapas) para replay fiel.
type admissionPayload struct {
	Type          string `json:"type"`
	Key           string `json:"key"`
	Tenant        string `json:"tenant,omitempty"`
	Board         string `json:"board,omitempty"`
	ReservationID string `json:"reservation_id"`
	CostTokens    int64  `json:"cost_tokens"`
	CostRequests  int64  `json:"cost_requests"`
	// HeadroomTokens/Requests são o headroom GLOBAL no momento da decisão.
	HeadroomTokens   int64 `json:"headroom_tokens"`
	HeadroomRequests int64 `json:"headroom_requests"`
	// TSUnixNano é o instante da decisão pelo relógio INJECTÁVEL (determinismo).
	TSUnixNano int64 `json:"ts_unix_nano"`
	// RetryAfterMs preenchido apenas em admit_deferred.
	RetryAfterMs int64 `json:"retry_after_ms,omitempty"`
	// Unsatisfiable, quando true num admit_deferred, marca uma rejeição PERMANENTE
	// (custo > tecto TPM/RPM): o pedido nunca será admissível, não é um adiamento
	// transitório. Preservado no log para o replay/auditoria distinguir os dois.
	Unsatisfiable bool `json:"unsatisfiable,omitempty"`
	// Backpressure, quando true num admit_deferred, marca um defer causado por
	// BACKPRESSURE (fila saturada a montante, AOS-030) — havia headroom, mas o
	// sinal de saturação forçou o adiamento. Distingue-o do defer por falta de
	// headroom no replay/auditoria.
	Backpressure bool `json:"backpressure,omitempty"`
}

// AdmitRequest é o pedido de admissão de um trabalho que consome quota do
// provider.
type AdmitRequest struct {
	// Key identifica o bucket partilhado (provider:model:region).
	Key ProviderKey
	// Tenant e Board são as dimensões de partição (quota multidimensional). A
	// partição por tenant preserva sempre o tecto global.
	Tenant string
	Board  string
	// EstimatedTokens, se > 0, fixa o custo previsto e tem precedência sobre o
	// CostEstimator. Caso contrário usa-se o estimador injectado.
	EstimatedTokens int64
	// RequestID torna a reserva idempotente: dois Admit com o mesmo RequestID
	// produzem a MESMA reserva (dedup do Event Store por step_id). Se vazio, é
	// gerado pelo idGen injectável.
	RequestID string
}

// AdmitResult é o veredicto de [Admission.Admit].
type AdmitResult struct {
	// Granted indica se o débito foi reservado atomicamente.
	Granted bool
	// Rejected marca uma rejeição PERMANENTE (não um adiamento transitório): o
	// pedido nunca poderá ser admitido tal como está — o seu custo excede o tecto
	// TPM/RPM da chave (ou do tenant). Distingue-se do defer para o chamador não
	// fazer poll eterno de um pedido envenenado. Quando Rejected=true, Granted é
	// false e RetryAfter é 0 (não há instante futuro que o torne admissível).
	Rejected bool
	// ReservationID identifica a reserva (para posterior [Admission.Release]).
	ReservationID string
	// RetryAfter, quando Granted=false && !Rejected, é o tempo aconselhado até
	// haver headroom (derivado do refill temporizado). NUNCA é um descarte
	// silencioso. Em rejeição permanente é 0.
	RetryAfter time.Duration
	// HeadroomTokens/Requests são o headroom global observado na decisão.
	HeadroomTokens   int64
	HeadroomRequests int64
}

// Admission é o controlador de admissão global (token-bucket distribuído). É
// stateless: todo o estado vive no Event Store. Construir com [NewAdmission].
type Admission struct {
	log      EventLog
	qp       QuotaProvider
	est      CostEstimator
	now      func() time.Time
	idGen    func() string
	tracer   agentruntime.Tracer
	producer eventstore.Producer
	maxRetry int
	// bp é o seam OPCIONAL de backpressure (AOS-030). Nil por omissão: sem ele o
	// admit comporta-se EXACTAMENTE como no AOS-027 (acoplamento aditivo).
	bp BackpressureSource
}

// AdmissionOption configura a [Admission].
type AdmissionOption func(*Admission)

// WithClock injecta o relógio de decisão. Determinismo/replay: sem time.Now no
// caminho de decisão.
func WithClock(now func() time.Time) AdmissionOption {
	return func(a *Admission) {
		if now != nil {
			a.now = now
		}
	}
}

// WithIDGen injecta o gerador de reservation IDs (determinismo/replay).
func WithIDGen(gen func() string) AdmissionOption {
	return func(a *Admission) {
		if gen != nil {
			a.idGen = gen
		}
	}
}

// WithCostEstimator injecta o estimador de custo em tokens.
func WithCostEstimator(est CostEstimator) AdmissionOption {
	return func(a *Admission) {
		if est != nil {
			a.est = est
		}
	}
}

// WithTracer injecta a porta OTel (spans com headroom e custo por span). Zero-dep
// (reutiliza agentruntime.Tracer).
func WithTracer(t agentruntime.Tracer) AdmissionOption {
	return func(a *Admission) {
		if t != nil {
			a.tracer = t
		}
	}
}

// WithAdmissionProducer injecta a identidade emissora (NHI) dos eventos.
func WithAdmissionProducer(p eventstore.Producer) AdmissionOption {
	return func(a *Admission) { a.producer = p }
}

// WithMaxCASRetries afina o cap de re-tentativas de CAS por admit.
func WithMaxCASRetries(n int) AdmissionOption {
	return func(a *Admission) {
		if n > 0 {
			a.maxRetry = n
		}
	}
}

// WithBackpressure ACOPLA o admission control a uma [BackpressureSource] (AOS-030).
// É ADITIVO: sob saturação da fila do tenant, o admit passa a ADIAR (defer) mesmo
// havendo headroom — o sinal de backpressure propaga-se a montante em vez de
// deixar a fila crescer. Sem esta opção, o admit é bit-a-bit o do AOS-027 (nenhum
// teste do AOS-027 muda de comportamento). O check de idempotência de uma reserva
// JÁ activa NÃO é afectado (um retry do mesmo pedido não é revogado por pressão).
func WithBackpressure(src BackpressureSource) AdmissionOption {
	return func(a *Admission) {
		if src != nil {
			a.bp = src
		}
	}
}

// NewAdmission constrói o controlador. Falha se log ou qp forem nil (sem Event
// Store não há reserva atómica; sem QuotaProvider não há fonte de limites). O
// estimador por omissão devolve 1 token; substitua-o por [WithCostEstimator].
func NewAdmission(log EventLog, qp QuotaProvider, opts ...AdmissionOption) (*Admission, error) {
	if log == nil {
		return nil, fmt.Errorf("admission: event log nil (reserva atómica exige Event Store)")
	}
	if qp == nil {
		return nil, fmt.Errorf("admission: quota provider nil (limites TPM/RPM sem fonte)")
	}
	a := &Admission{
		log:      log,
		qp:       qp,
		est:      FixedCostEstimator{Tokens: 1},
		now:      time.Now,
		tracer:   agentruntime.NoopTracer{},
		producer: eventstore.Producer{NHIID: DefaultAdmissionNHI},
		maxRetry: defaultMaxCASRetries,
	}
	for _, opt := range opts {
		opt(a)
	}
	if a.idGen == nil {
		a.idGen = defaultIDGen()
	}
	return a, nil
}

// Admit é a decisão de admissão global. Reserva débito atomicamente se há
// headroom; sem headroom, ADIA (defer com retry_after), NUNCA descarta
// silenciosamente.
//
// Fluxo:
//  1. estima o custo (tokens) e emite admit_requested (auditoria);
//  2. lê os limites globais (e o tecto do tenant, se houver) pela porta;
//  3. loop CAS: relê o stream de reserva, dobra o estado activo (respeitando o
//     refill temporizado), calcula o headroom GLOBAL e do TENANT; se cabe,
//     Append(admit_granted, WithExpectedSeq(last)); perdedor de corrida relê e
//     re-tenta; sem headroom, emite admit_deferred e devolve retry_after.
func (a *Admission) Admit(ctx context.Context, req AdmitRequest) (AdmitResult, error) {
	ctx, span := a.tracer.StartSpan(ctx, opAdmit)
	defer span.End()

	cost := req.EstimatedTokens
	if cost <= 0 {
		cost = a.est.EstimateTokens(req)
	}
	if cost < 1 {
		cost = 1
	}
	const reqCost = int64(1) // cada admissão consome 1 request (RPM)

	resID := req.RequestID
	if resID == "" {
		resID = a.idGen()
	}

	keyStr := req.Key.String()
	span.SetAttribute(attrAdmitKey, keyStr)
	span.SetAttribute(attrAdmitTenant, req.Tenant)
	span.SetAttribute(attrAdmitCost, cost)

	// Limites globais (fonte: porta QuotaProvider — nunca constantes locais).
	glob, err := a.qp.Limits(ctx, req.Key)
	if err != nil {
		return AdmitResult{}, fmt.Errorf("admission: limites globais: %w", err)
	}
	if glob.Window <= 0 {
		return AdmitResult{}, fmt.Errorf("admission: janela de refill inválida (<=0) para %s", keyStr)
	}

	// Tecto do tenant (opcional). O global domina sempre.
	var tenantLim ProviderLimits
	var hasTenantCap bool
	if tqp, ok := a.qp.(TenantQuotaProvider); ok && req.Tenant != "" {
		tl, present, terr := tqp.TenantLimits(ctx, req.Key, req.Tenant)
		if terr != nil {
			return AdmitResult{}, fmt.Errorf("admission: limites do tenant: %w", terr)
		}
		tenantLim, hasTenantCap = tl, present
	}

	nowNano := a.now().UnixNano()

	// admit_requested (auditoria append-only).
	if err := a.appendAudit(ctx, req.Key, EventAdmitRequested, admissionPayload{
		Type:          EventAdmitRequested,
		Key:           keyStr,
		Tenant:        req.Tenant,
		Board:         req.Board,
		ReservationID: resID,
		CostTokens:    cost,
		CostRequests:  reqCost,
		TSUnixNano:    nowNano,
	}); err != nil {
		return AdmitResult{}, err
	}

	bucketID := bucketStreamPrefix + keyStr
	windowNano := glob.Window.Nanoseconds()

	// BACKPRESSURE (AOS-030, acoplamento ADITIVO): se o seam estiver injectado e a
	// fila do tenant estiver saturada, o admit passa a ADIAR mesmo havendo headroom
	// — o sinal propaga-se a montante. Consultado UMA vez (determinístico, barato)
	// antes do loop de CAS. Sem seam (bp==nil), bpSaturated é false e o caminho é o
	// do AOS-027, inalterado.
	var bpSaturated bool
	var bpRetry time.Duration
	if a.bp != nil {
		sig := a.bp.Backpressure(ctx, req.Key, req.Tenant)
		bpSaturated = sig.Saturated
		bpRetry = sig.RetryAfter
	}
	span.SetAttribute(attrAdmitBackpressure, bpSaturated)

	// Rejeição PERMANENTE (não adiamento): se o custo excede o próprio tecto
	// TPM/RPM da chave — ou, havendo cap de tenant, o tecto do tenant — nenhum
	// refill futuro o tornará admissível. Adiar seria aconselhar um poll eterno
	// (livelock) contra um pedido envenenado. Sinaliza-o distintamente para o
	// chamador não voltar a tentar o mesmo pedido.
	if unsatisfiable(cost, reqCost, glob, tenantLim, hasTenantCap) {
		span.SetAttribute(attrAdmitGranted, false)
		span.SetAttribute(attrAdmitUnsatisfiable, true)
		if err := a.appendAudit(ctx, req.Key, EventAdmitDeferred, admissionPayload{
			Type:          EventAdmitDeferred,
			Key:           keyStr,
			Tenant:        req.Tenant,
			Board:         req.Board,
			ReservationID: resID,
			CostTokens:    cost,
			CostRequests:  reqCost,
			TSUnixNano:    nowNano,
			Unsatisfiable: true,
		}); err != nil {
			return AdmitResult{}, err
		}
		return AdmitResult{
			Granted:          false,
			Rejected:         true,
			ReservationID:    resID,
			RetryAfter:       0,
			HeadroomTokens:   glob.TPM,
			HeadroomRequests: glob.RPM,
		}, nil
	}

	for attempt := 0; attempt < a.maxRetry; attempt++ {
		st, lastSeq, err := a.foldBucket(ctx, bucketID, nowNano, windowNano)
		if err != nil {
			return AdmitResult{}, err
		}

		// Idempotência: se esta reserva JÁ está activa (retry do MESMO RequestID),
		// devolve o veredicto original sem re-debitar — evita adiar contra a própria
		// reserva quando ela, sozinha, encheu o headroom. Mas a idempotência é sobre
		// o MESMO pedido: o custo estimado tem de coincidir com o reservado. Reusar
		// o RequestID com custo diferente NÃO é um retry — conceder cegamente
		// deixaria o chamador consumir o novo custo (maior) contra apenas o antigo
		// débito ⇒ oversubscription silenciosa. Fail-closed com ErrIdempotencyConflict.
		for _, r := range st.active {
			if r.id == resID {
				if cost != r.grantTokens || reqCost != r.grantRequests {
					return AdmitResult{}, fmt.Errorf("%w: %q reservou %d tokens/%d requests mas o novo pedido estima %d/%d",
						ErrIdempotencyConflict, resID, r.grantTokens, r.grantRequests, cost, reqCost)
				}
				span.SetAttribute(attrAdmitGranted, true)
				return AdmitResult{
					Granted:          true,
					ReservationID:    resID,
					HeadroomTokens:   glob.TPM - st.tokens,
					HeadroomRequests: glob.RPM - st.requests,
				}, nil
			}
		}

		headTokens := glob.TPM - st.tokens
		headReq := glob.RPM - st.requests

		// Headroom do tenant (se houver cap): min entre o que resta do tenant e o
		// que resta do global. O GLOBAL DOMINA — mesmo com folga na partição do
		// tenant, o tecto global limita.
		fitsTenant := true
		if hasTenantCap {
			tHeadTokens := tenantLim.TPM - st.tenantTokens[req.Tenant]
			tHeadReq := tenantLim.RPM - st.tenantRequests[req.Tenant]
			if cost > tHeadTokens || reqCost > tHeadReq {
				fitsTenant = false
			}
		}

		if cost <= headTokens && reqCost <= headReq && fitsTenant && !bpSaturated {
			// Há headroom E não há backpressure: tenta reservar atomicamente via CAS.
			pl := admissionPayload{
				Type:             EventAdmitGranted,
				Key:              keyStr,
				Tenant:           req.Tenant,
				Board:            req.Board,
				ReservationID:    resID,
				CostTokens:       cost,
				CostRequests:     reqCost,
				HeadroomTokens:   headTokens,
				HeadroomRequests: headReq,
				TSUnixNano:       nowNano,
			}
			raw, merr := json.Marshal(pl)
			if merr != nil {
				return AdmitResult{}, merr
			}
			_, aerr := a.log.Append(ctx, bucketID, eventstore.EventInput{
				Type:     EventAdmitGranted,
				Payload:  raw,
				RunID:    bucketID,
				StepID:   "grant:" + resID,
				Producer: a.producer,
			}, eventstore.WithExpectedSeq(lastSeq))

			switch {
			case aerr == nil:
				// Reserva committed (ou duplicada idempotente): admitido.
				span.SetAttribute(attrAdmitGranted, true)
				span.SetAttribute(attrAdmitHeadroomT, headTokens)
				span.SetAttribute(attrAdmitHeadroomR, headReq)
				return AdmitResult{
					Granted:          true,
					ReservationID:    resID,
					HeadroomTokens:   headTokens,
					HeadroomRequests: headReq,
				}, nil
			case errors.Is(aerr, eventstore.ErrSeqConflict), errors.Is(aerr, eventstore.ErrAppendOnlyViolation):
				// Perdeu a corrida de CAS: outro worker reservou. Relê e reavalia.
				continue
			default:
				return AdmitResult{}, aerr
			}
		}

		// ADIA (defer): ou não há headroom (global/tenant), ou há BACKPRESSURE a
		// montante (fila saturada). Calcula retry_after do refill; sob backpressure
		// com headroom, o refill daria 0 (nada a expirar), por isso usa-se o
		// bpRetry aconselhado pela fila — evita re-tentar de imediato contra uma
		// fila cheia.
		retry := retryAfter(st.active, nowNano, windowNano, cost, glob.TPM-st.tokens)
		if hasTenantCap && !fitsTenant {
			// Se foi o tenant a bloquear, o retry_after do tenant pode diferir; usa
			// o mais tardio dos dois (o que garante headroom em ambas as dimensões).
			tRetry := retryAfterTenant(st.active, req.Tenant, nowNano, windowNano, cost, tenantLim.TPM-st.tenantTokens[req.Tenant])
			if tRetry > retry {
				retry = tRetry
			}
		}
		if bpSaturated && bpRetry > retry {
			retry = bpRetry
		}
		span.SetAttribute(attrAdmitGranted, false)
		span.SetAttribute(attrAdmitHeadroomT, headTokens)
		span.SetAttribute(attrAdmitHeadroomR, headReq)
		span.SetAttribute(attrAdmitRetryMs, retry.Milliseconds())

		if err := a.appendAudit(ctx, req.Key, EventAdmitDeferred, admissionPayload{
			Type:             EventAdmitDeferred,
			Key:              keyStr,
			Tenant:           req.Tenant,
			Board:            req.Board,
			ReservationID:    resID,
			CostTokens:       cost,
			CostRequests:     reqCost,
			HeadroomTokens:   headTokens,
			HeadroomRequests: headReq,
			TSUnixNano:       nowNano,
			RetryAfterMs:     retry.Milliseconds(),
			Backpressure:     bpSaturated,
		}); err != nil {
			return AdmitResult{}, err
		}
		return AdmitResult{
			Granted:          false,
			ReservationID:    resID,
			RetryAfter:       retry,
			HeadroomTokens:   headTokens,
			HeadroomRequests: headReq,
		}, nil
	}

	// Cap de CAS esgotado (contenção patológica): adia com retry imediato, nunca
	// descarta.
	return AdmitResult{Granted: false, ReservationID: resID, RetryAfter: 0}, nil
}

// Release liberta (reconcilia) uma reserva antes do fim natural da janela —
// p.ex. quando o custo real ficou abaixo do estimado, ou o trabalho terminou.
//
// Semântica de RECONCILIAÇÃO PARCIAL: costTokens/costRequests são o montante a
// devolver, SUBTRAÍDO do débito activo da reserva (com clamp em 0). Libertar o
// custo total da reserva remove-a por inteiro; libertar menos reduz o débito e
// mantém o remanescente activo (ainda in-flight contra o provider). É este o
// contrato que impede o headroom fantasma: devolver 100 de uma reserva de 1000
// abre 100 de headroom, não 1000. Para o comportamento anterior de libertação
// total, passe o custo total reservado.
//
// É idempotente por step_id APENAS para a MESMA tripla (id, tokens, requests):
// o step_id codifica o montante, pelo que duas libertações parciais distintas da
// mesma reserva são eventos distintos (ambas subtraem). Repetir a MESMA
// libertação é deduplicada pelo Event Store (não subtrai duas vezes). Não precisa
// de CAS: uma libertação só ALIVIA a pressão, nunca causa oversubscription (o
// clamp em 0 garante que sobre-libertar não abre headroom além do reservado).
func (a *Admission) Release(ctx context.Context, key ProviderKey, reservationID string, costTokens, costRequests int64) error {
	bucketID := bucketStreamPrefix + key.String()
	pl := admissionPayload{
		Type:          EventQuotaReleased,
		Key:           key.String(),
		ReservationID: reservationID,
		CostTokens:    costTokens,
		CostRequests:  costRequests,
		TSUnixNano:    a.now().UnixNano(),
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	_, err = a.log.Append(ctx, bucketID, eventstore.EventInput{
		Type:    EventQuotaReleased,
		Payload: raw,
		RunID:   bucketID,
		// O step_id codifica o MONTANTE: repetir a MESMA libertação é deduplicado
		// (não subtrai duas vezes), mas duas libertações parciais distintas da mesma
		// reserva são eventos distintos que ambos subtraem.
		StepID:   fmt.Sprintf("release:%s:%d:%d", reservationID, costTokens, costRequests),
		Producer: a.producer,
	})
	return err
}

// reservation é uma reserva activa dobrada do stream (para retry_after e
// idempotência).
type reservation struct {
	id     string
	tenant string
	// tokens/requests são o débito EFECTIVO (grant menos libertações parciais,
	// com clamp em 0) — é o que conta para o headroom e para retry_after.
	tokens   int64
	requests int64
	// grantTokens/grantRequests é o custo ORIGINAL reservado no grant. Serve o
	// check de idempotência (reusar o RequestID exige o MESMO custo), independente
	// de libertações parciais entretanto aplicadas.
	grantTokens   int64
	grantRequests int64
	tsNano        int64
}

// bucketFold é o estado dobrado do stream de reserva num instante.
type bucketFold struct {
	tokens         int64
	requests       int64
	tenantTokens   map[string]int64
	tenantRequests map[string]int64
	active         []reservation
}

// foldBucket relê o stream de reserva e reconstrói o estado ACTIVO no instante
// nowNano, aplicando o refill temporizado (uma reserva com idade >= Window já
// não conta) e as libertações explícitas (quota_released). Devolve também o
// último seq committed (âncora do CAS). É a projecção determinística usada tanto
// na decisão como no replay.
func (a *Admission) foldBucket(ctx context.Context, bucketID string, nowNano, windowNano int64) (bucketFold, uint64, error) {
	evs, err := a.log.Read(ctx, bucketID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return bucketFold{
				tenantTokens:   map[string]int64{},
				tenantRequests: map[string]int64{},
			}, 0, nil
		}
		return bucketFold{}, 0, err
	}

	// Reconstrói por reservation_id: grant materializa o débito; cada release
	// SUBTRAI o montante libertado (reconciliação parcial), com clamp em 0. Uma
	// reserva só sai da contabilidade quando o seu débito efectivo chega a 0 (ou
	// quando expira pelo refill temporizado). Marcar "released" all-or-nothing
	// abria headroom fantasma e permitia oversubscrever o limite real (AOS-027 C1).
	type entry struct {
		tenant string
		// grantTokens/grantRequests: custo original reservado (para idempotência).
		grantTokens   int64
		grantRequests int64
		// relTokens/relRequests: soma das libertações parciais.
		relTokens   int64
		relRequests int64
		tsNano      int64
	}
	byID := make(map[string]*entry)
	order := make([]string, 0, len(evs))
	var lastSeq uint64
	for _, ev := range evs {
		if ev.Seq > lastSeq {
			lastSeq = ev.Seq
		}
		var pl admissionPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return bucketFold{}, 0, fmt.Errorf("admission: payload corrompido no bucket %s seq %d: %w", bucketID, ev.Seq, err)
		}
		switch pl.Type {
		case EventAdmitGranted:
			if _, ok := byID[pl.ReservationID]; !ok {
				byID[pl.ReservationID] = &entry{
					tenant:        pl.Tenant,
					grantTokens:   pl.CostTokens,
					grantRequests: pl.CostRequests,
					tsNano:        pl.TSUnixNano,
				}
				order = append(order, pl.ReservationID)
			}
		case EventQuotaReleased:
			if e, ok := byID[pl.ReservationID]; ok {
				e.relTokens += pl.CostTokens
				e.relRequests += pl.CostRequests
			}
		}
	}

	fold := bucketFold{
		tenantTokens:   map[string]int64{},
		tenantRequests: map[string]int64{},
	}
	for _, id := range order {
		e := byID[id]
		// Débito efectivo = reservado − libertado, com clamp em 0 (sobre-libertar
		// nunca abre headroom além do reservado, logo nunca oversubscreve).
		effTokens := e.grantTokens - e.relTokens
		if effTokens < 0 {
			effTokens = 0
		}
		effRequests := e.grantRequests - e.relRequests
		if effRequests < 0 {
			effRequests = 0
		}
		// Reserva totalmente reconciliada (débito efectivo esgotado): não conta.
		if effTokens == 0 && effRequests == 0 {
			continue
		}
		// Refill temporizado: reserva expira ao fim de Window (janela deslizante).
		if nowNano-e.tsNano >= windowNano {
			continue
		}
		fold.tokens += effTokens
		fold.requests += effRequests
		fold.tenantTokens[e.tenant] += effTokens
		fold.tenantRequests[e.tenant] += effRequests
		fold.active = append(fold.active, reservation{
			id:            id,
			tenant:        e.tenant,
			tokens:        effTokens,
			requests:      effRequests,
			grantTokens:   e.grantTokens,
			grantRequests: e.grantRequests,
			tsNano:        e.tsNano,
		})
	}
	return fold, lastSeq, nil
}

// unsatisfiable decide se um pedido é PERMANENTEMENTE inadmissível: o seu custo
// excede o tecto TPM/RPM da própria chave (bucket vazio não chega) ou, havendo
// cap de tenant, o tecto do tenant. Nesse caso nenhum refill futuro o torna
// admissível — adiar seria um livelock (poll eterno). Determinística (sem
// relógio): só compara custo com os tectos.
func unsatisfiable(cost, reqCost int64, glob, tenantLim ProviderLimits, hasTenantCap bool) bool {
	if cost > glob.TPM || reqCost > glob.RPM {
		return true
	}
	if hasTenantCap && (cost > tenantLim.TPM || reqCost > tenantLim.RPM) {
		return true
	}
	return false
}

// retryAfter calcula, de forma determinística, quanto tempo falta até expirarem
// reservas activas suficientes para caber `need` tokens, dado o headroom
// corrente `head` (= TPM - reservado). As reservas expiram a tsNano+Window.
func retryAfter(active []reservation, nowNano, windowNano, need, head int64) time.Duration {
	if head >= need {
		return 0
	}
	// Ordena por expiração (tsNano ascendente ⇒ expira primeiro).
	sorted := make([]reservation, len(active))
	copy(sorted, active)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].tsNano < sorted[j].tsNano })
	freed := int64(0)
	for _, r := range sorted {
		freed += r.tokens
		if head+freed >= need {
			expireAt := r.tsNano + windowNano
			d := expireAt - nowNano
			if d < 0 {
				d = 0
			}
			return time.Duration(d)
		}
	}
	// Mesmo libertando tudo não cabe (custo > TPM): aconselha uma janela inteira.
	return time.Duration(windowNano)
}

// retryAfterTenant é o análogo de retryAfter restrito às reservas do tenant.
func retryAfterTenant(active []reservation, tenant string, nowNano, windowNano, need, head int64) time.Duration {
	if head >= need {
		return 0
	}
	sorted := make([]reservation, 0, len(active))
	for _, r := range active {
		if r.tenant == tenant {
			sorted = append(sorted, r)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].tsNano < sorted[j].tsNano })
	freed := int64(0)
	for _, r := range sorted {
		freed += r.tokens
		if head+freed >= need {
			expireAt := r.tsNano + windowNano
			d := expireAt - nowNano
			if d < 0 {
				d = 0
			}
			return time.Duration(d)
		}
	}
	return time.Duration(windowNano)
}

// appendAudit escreve um evento de auditoria (admit_requested/deferred) no stream
// de auditoria da chave. Append simples (sem CAS): a auditoria é tolerante a
// ordem e não participa na contabilidade do débito. Idempotente por step_id
// (tipo+reservation_id).
func (a *Admission) appendAudit(ctx context.Context, key ProviderKey, evType string, pl admissionPayload) error {
	auditID := auditStreamPrefix + key.String()
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	_, err = a.log.Append(ctx, auditID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    auditID,
		StepID:   auditStepID(evType, pl.ReservationID),
		Producer: a.producer,
	})
	return err
}

// auditStepID compõe o step_id idempotente de um evento de auditoria.
func auditStepID(evType, resID string) string {
	switch evType {
	case EventAdmitRequested:
		return "req:" + resID
	case EventAdmitDeferred:
		return "defer:" + resID
	default:
		return evType + ":" + resID
	}
}

// AdmissionRecord é uma decisão de admissão reconstruída do log (para replay).
type AdmissionRecord struct {
	Type          string
	ReservationID string
	Tenant        string
	Board         string
	CostTokens    int64
	CostRequests  int64
	TSUnixNano    int64
	Seq           uint64
}

// Replay reconstrói fielmente a sequência de reservas (admit_granted /
// quota_released) do stream de reserva da chave, por ordem de seq. É a prova de
// que a sequência de admissões se reconstrói do Event Store (determinismo/ADR-001).
func (a *Admission) Replay(ctx context.Context, key ProviderKey) ([]AdmissionRecord, error) {
	bucketID := bucketStreamPrefix + key.String()
	evs, err := a.log.Read(ctx, bucketID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]AdmissionRecord, 0, len(evs))
	for _, ev := range evs {
		var pl admissionPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		out = append(out, AdmissionRecord{
			Type:          pl.Type,
			ReservationID: pl.ReservationID,
			Tenant:        pl.Tenant,
			Board:         pl.Board,
			CostTokens:    pl.CostTokens,
			CostRequests:  pl.CostRequests,
			TSUnixNano:    pl.TSUnixNano,
			Seq:           ev.Seq,
		})
	}
	return out, nil
}

// ReplayAudit reconstrói a sequência de auditoria (admit_requested /
// admit_deferred) do stream de auditoria da chave.
func (a *Admission) ReplayAudit(ctx context.Context, key ProviderKey) ([]AdmissionRecord, error) {
	auditID := auditStreamPrefix + key.String()
	evs, err := a.log.Read(ctx, auditID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]AdmissionRecord, 0, len(evs))
	for _, ev := range evs {
		var pl admissionPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		out = append(out, AdmissionRecord{
			Type:          pl.Type,
			ReservationID: pl.ReservationID,
			Tenant:        pl.Tenant,
			Board:         pl.Board,
			CostTokens:    pl.CostTokens,
			CostRequests:  pl.CostRequests,
			TSUnixNano:    pl.TSUnixNano,
			Seq:           ev.Seq,
		})
	}
	return out, nil
}

// defaultIDGen devolve um gerador de reservation IDs por omissão: um contador
// atómico monotónico com um prefixo de arranque, único por processo. Em produção
// injecte um ULID/UUID via [WithIDGen]; em testes injecte um gerador
// determinístico.
func defaultIDGen() func() string {
	var ctr uint64
	seed := time.Now().UnixNano()
	return func() string {
		n := atomic.AddUint64(&ctr, 1)
		return fmt.Sprintf("res-%d-%d", seed, n)
	}
}
