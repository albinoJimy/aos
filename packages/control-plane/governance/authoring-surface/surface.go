package authoringsurface

import (
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AuthoringLoop é a SUPERFÍCIE do loop de autoria de skills (AOS-126): compõe as
// quatro portas (dry-run, atribuição, eval, submissão) e o Tracer, e orquestra o
// loop — DRY-RUN (sem efeitos cometidos) → ATRIBUIÇÃO (autor/versão/proveniência) →
// EVAL (veredicto/canary antes da decisão) → SUBMISSÃO (ao gate de ratificação de
// AOS-096). É uma camada FINA de APRESENTAÇÃO: COMPÕE o que o sandbox/registry/eval-
// gate/ratificação já impõem e NÃO reimplementa nada.
//
// INVARIANTES ESTRUTURAIS (a forma, não a convenção):
//   - NÃO comete efeitos: a única porta de execução é o [DryRunner], que só captura
//     (Committed=false validado fail-closed; egress default-deny; efeitos untrusted).
//   - NÃO ratifica: a única porta sobre o gate é o [RatificationSubmitter], que só
//     submete e devolve o token — não há caminho de Ratify na superfície.
type AuthoringLoop struct {
	dryRunner   DryRunner
	attribution AttributionReader
	eval        EvalResultReader
	submitter   RatificationSubmitter
	tracer      agentruntime.Tracer
	runID       string
}

// Option configura o [AuthoringLoop] na construção.
type Option func(*AuthoringLoop)

// WithAttributionReader liga a porta de leitura da atribuição (AC2). Sem ela,
// [AuthoringLoop.Attribution] recusa fail-closed ([ErrNoAttributionReader]).
func WithAttributionReader(r AttributionReader) Option {
	return func(l *AuthoringLoop) { l.attribution = r }
}

// WithEvalResultReader liga a porta de leitura do eval-gate/canary (AC4). Sem ela,
// [AuthoringLoop.EvalOutcome] recusa fail-closed ([ErrNoEvalReader]).
func WithEvalResultReader(r EvalResultReader) Option {
	return func(l *AuthoringLoop) { l.eval = r }
}

// WithRatificationSubmitter liga a porta de encaminhamento ao gate de ratificação de
// AOS-096 (AC3). Sem ela, [AuthoringLoop.SubmitForRatification] recusa fail-closed
// ([ErrNoSubmitter]) — a superfície nunca promove localmente.
func WithRatificationSubmitter(s RatificationSubmitter) Option {
	return func(l *AuthoringLoop) { l.submitter = s }
}

// WithTracer injecta o [agentruntime.Tracer] que emite os spans do loop (AC5). Por
// omissão [agentruntime.NoopTracer] (sem custo). Um tracer nil é ignorado.
func WithTracer(t agentruntime.Tracer) Option {
	return func(l *AuthoringLoop) {
		if t != nil {
			l.tracer = t
		}
	}
}

// WithRunID fixa o run_id que correlaciona os spans do loop ao trace da trajectória
// (AttrRunID). Opcional: a ligação de trace faz-se sempre pelo ctx propagado; o runID
// é o rótulo de correlação quando conhecido.
func WithRunID(runID string) Option {
	return func(l *AuthoringLoop) { l.runID = runID }
}

// New constrói o loop de autoria sobre o [DryRunner] (o cerne — AC1) e liga as
// restantes portas por [Option]. Fail-closed: um dryRunner nil devolve
// [ErrNilDryRunner]. As portas de atribuição/eval/submissão são opcionais na
// construção — cada operação que dependa de uma porta ausente recusa fail-closed no
// seu ponto de uso (nunca uma degradação silenciosa).
func New(dryRunner DryRunner, opts ...Option) (*AuthoringLoop, error) {
	if dryRunner == nil {
		return nil, ErrNilDryRunner
	}
	l := &AuthoringLoop{
		dryRunner: dryRunner,
		tracer:    agentruntime.NoopTracer{},
	}
	for _, o := range opts {
		o(l)
	}
	return l, nil
}
