package surfaceadapter

import (
	"context"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// SurfaceAuthorizer é o ponto onde a decisão recolhida numa superfície é DEVOLVIDA ao
// gate (AC3). COMPÕE — não reimplementa: renderiza o card canónico para a superfície
// (via [Renderer]) e, quando a superfície CONSEGUE representar a semântica de
// aprovação, delega a autorização no [approvalcard.DualControlCollector], que a entrega
// ao [risk.ConfirmationChannel] de AOS-095 (que assina/verifica autoridade/anti-replay/
// 4-eyes/audit). O adaptador NÃO decide nem assina — só traduz a apresentação e devolve
// a decisão pela porta canónica.
//
// Quando a superfície DEGRADA fail-closed (não representa o dual-control de um card
// irreversível), o autorizador RECUSA (Authorized=false) e NÃO chama o gate para
// aprovar — a decisão nega por construção, nunca por omissão (AC4).
type SurfaceAuthorizer struct {
	renderer  Renderer
	collector *approvalcard.DualControlCollector
	tracer    agentruntime.Tracer
}

// AuthorizerOption configura o [SurfaceAuthorizer].
type AuthorizerOption func(*SurfaceAuthorizer)

// WithTracer injecta a porta de observabilidade do span de interacção por canal. Por
// omissão não emite (tracer nil).
func WithTracer(t agentruntime.Tracer) AuthorizerOption {
	return func(a *SurfaceAuthorizer) {
		if t != nil {
			a.tracer = t
		}
	}
}

// NewSurfaceAuthorizer constrói o autorizador com o renderer da superfície e o
// coletor canónico a que DEVOLVE a decisão. Fail-closed: um renderer ou um coletor nil
// devolve [ErrNilDependency] (sem forma de renderizar/devolver ao gate, não há
// autorização possível).
func NewSurfaceAuthorizer(r Renderer, collector *approvalcard.DualControlCollector, opts ...AuthorizerOption) (*SurfaceAuthorizer, error) {
	if r == nil || collector == nil {
		return nil, ErrNilDependency
	}
	a := &SurfaceAuthorizer{renderer: r, collector: collector}
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

// Platform devolve a superfície deste autorizador.
func (a *SurfaceAuthorizer) Platform() Platform { return a.renderer.Platform() }

// Authorize renderiza o card canónico para esta superfície e RECOLHE a decisão,
// devolvendo-a ao gate (AC3). O fluxo:
//
//   - RENDER falha (contrato MAJOR incompatível / card incoerente, AC5) ⇒ erro
//     fail-closed, sem tocar no gate.
//   - Render DEGRADADA (AC4: superfície sem dual-control para um card irreversível) ⇒
//     RECUSA (Authorized=false), encaminha, e NÃO chama o gate para aprovar — nunca
//     aprova por omissão.
//   - Render CAPAZ ⇒ delega no [approvalcard.DualControlCollector.Authorize], que
//     devolve a decisão ao [risk.ConfirmationChannel] (uma aprovação para reversível;
//     DOIS aprovadores DISTINTOS para irreversível). A identidade do(s) aprovador(es)
//     — recolhida pelo canal ligado a ESTA superfície — propaga-se no
//     [approvalcard.Decision].
//
// Emite o span de interacção por canal (aos.surface.*), ligado ao trace pelo ctx, sem
// segredos. Devolve também a [RenderedCard] para inspecção da apresentação.
func (a *SurfaceAuthorizer) Authorize(ctx context.Context, card approvalcard.ApprovalCard) (approvalcard.Decision, RenderedCard, error) {
	rc, err := a.renderer.Render(card)
	if err != nil {
		return approvalcard.Decision{Authorized: false, Reason: "render recusada (fail-closed)"}, RenderedCard{}, err
	}

	// Span de interacção por canal — emitido para toda a apresentação (capaz ou
	// degradada), ligado ao trace do run. Sem segredos (o Preview nunca entra).
	emitSurfaceSpan(ctx, a.tracer, rc)

	if rc.Degraded {
		// Degradação fail-closed (AC4): a superfície não representa o dual-control. NÃO
		// devolve ao gate para aprovar — recusa/encaminha. Nunca aprova por omissão.
		return approvalcard.Decision{
			Authorized: false,
			Reason:     "superficie sem dual-control para card irreversivel: encaminhada, nao aprovada (fail-closed)",
		}, rc, nil
	}

	// Superfície capaz: DEVOLVE a decisão ao gate pela porta canónica. O coletor não é
	// reimplementado aqui — é COMPOSTO. A identidade do aprovador vem do canal.
	dec, err := a.collector.Authorize(ctx, card)
	return dec, rc, err
}
