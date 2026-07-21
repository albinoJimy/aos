package trajectorysurface

import (
	"context"
	"errors"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
)

// ErrNilEngine — fail-closed: a superfície EXIGE um motor de redação (sem ele não há
// como garantir a ausência de PII em claro na apresentação — AC4).
var ErrNilEngine = errors.New("trajectorysurface: motor de redacao nulo (fail-closed)")

// defaultRedactionSubject é o titular default sob o qual se redige (rótulo; a política
// default RemoveAll minimiza sem tokenizar, pelo que o subject é só documental).
const defaultRedactionSubject = "trajectory-surface"

// defaultPolicyVersion identifica a versão da política de minimização default.
const defaultPolicyVersion = "trajectory-surface-remove-all-v1"

// TrajectorySurface é a SUPERFÍCIE de visualização/drill-down da trajectória (AOS-127):
// COMPÕE os spans OTel de AOS-077 numa árvore navegável e inspeccionável, apresentando
// cada valor REDIGIDO (sem PII em claro) e o conteúdo untrusted marcado como DADO, com
// ligação opcional a eval/replay. É LEITURA PURA — CONSOME os spans, não os captura,
// muta nem re-emite; emite apenas os SEUS spans de interacção (tree_view/drill_down).
//
// Fail-closed: [New] exige um motor de redação. As portas de eval/replay são opcionais
// (sem elas as ligações ficam Available=false — nunca inventadas). O tracer default é
// [otelgenai.NoopTracer] (sem custo de observabilidade).
type TrajectorySurface struct {
	red    *Redaction
	tracer otelgenai.Tracer
	eval   EvalLinkSource
	replay ReplayLinkSource
	runID  string
}

// Option configura a [TrajectorySurface] na construção.
type Option func(*TrajectorySurface)

// WithTracer injecta o [otelgenai.Tracer] que emite os spans de interacção da
// superfície (DoD). Por omissão [otelgenai.NoopTracer]. Um tracer nil é ignorado.
func WithTracer(t otelgenai.Tracer) Option {
	return func(s *TrajectorySurface) {
		if t != nil {
			s.tracer = t
		}
	}
}

// WithPolicy fixa a [redaction.Policy] de apresentação. Por omissão a política de
// MINIMIZAÇÃO máxima ([redaction.RemoveAllPolicy]) — o default mais seguro, que não
// exige KeySource.
func WithPolicy(p redaction.Policy) Option {
	return func(s *TrajectorySurface) { s.red.Policy = p }
}

// WithRedactionSubject fixa o titular sob o qual se redige (rótulo de tokenização).
func WithRedactionSubject(subject string) Option {
	return func(s *TrajectorySurface) {
		if subject != "" {
			s.red.Subject = subject
		}
	}
}

// WithEvalSource liga a porta (opcional) de localização de evals (AC5). Sem ela,
// [TrajectorySurface.LinkEval] devolve Available=false.
func WithEvalSource(src EvalLinkSource) Option {
	return func(s *TrajectorySurface) { s.eval = src }
}

// WithReplaySource liga a porta (opcional) de localização de replays (AC5). Sem ela,
// [TrajectorySurface.LinkReplay] devolve Available=false.
func WithReplaySource(src ReplayLinkSource) Option {
	return func(s *TrajectorySurface) { s.replay = src }
}

// WithRunID fixa o run_id que correlaciona os spans de interacção ao trace do run
// (aos.run_id). Opcional: a ligação de trace faz-se sempre pelo ctx propagado.
func WithRunID(runID string) Option {
	return func(s *TrajectorySurface) { s.runID = runID }
}

// New constrói a superfície sobre um motor de redação (obrigatório — AC4). Fail-closed:
// engine nil => [ErrNilEngine]. Defaults: política [redaction.RemoveAllPolicy], subject
// [defaultRedactionSubject], tracer [otelgenai.NoopTracer]. As portas de eval/replay e
// o run_id ligam-se por [Option].
func New(engine *redaction.Engine, opts ...Option) (*TrajectorySurface, error) {
	if engine == nil {
		return nil, ErrNilEngine
	}
	s := &TrajectorySurface{
		red: &Redaction{
			Engine:  engine,
			Subject: defaultRedactionSubject,
			Policy:  redaction.RemoveAllPolicy(defaultPolicyVersion),
		},
		tracer: otelgenai.NoopTracer{},
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// TreeView constrói a árvore de spans de um run e dos seus sub-agentes (AC1) e emite o
// span de interacção tree_view ligado à trajectória. LEITURA PURA: os SpanData de
// entrada ficam intactos (AC3).
func (s *TrajectorySurface) TreeView(ctx context.Context, spans []otelgenai.SpanData) []*SpanNode {
	roots := BuildTree(spans)
	s.emitTreeView(ctx, len(roots), len(spans))
	return roots
}

// DrillDown inspecciona um nó (AC2) — tokens/custo/resultado/taint e os atributos
// REDIGIDOS com o taint marcado (AC4) — e emite o span de interacção drill_down. Para um
// invoke_agent, o [SpanDetail] traz o custo da sub-árvore (COMPOSTO por RollupByTrace).
func (s *TrajectorySurface) DrillDown(ctx context.Context, node *SpanNode) SpanDetail {
	detail := Inspect(node, s.red)
	kind := ""
	subtree := 0
	if node != nil {
		kind = node.Kind
		subtree = len(Flatten(node))
	}
	s.emitDrillDown(ctx, kind, subtree)
	return detail
}

// LinkEval devolve a ligação de NAVEGAÇÃO de node à eval da sua trajectória (AC5), via
// a porta injectada. Sem porta, Available=false (não inventa). NÃO recalcula o eval.
func (s *TrajectorySurface) LinkEval(node *SpanNode) EvalLink {
	return LinkEval(node, s.eval)
}

// LinkReplay devolve a ligação de NAVEGAÇÃO de node ao replay da sua trajectória (AC5),
// via a porta injectada. Sem porta, Available=false. NÃO re-executa o replay.
func (s *TrajectorySurface) LinkReplay(node *SpanNode) ReplayLink {
	return LinkReplay(node, s.replay)
}
