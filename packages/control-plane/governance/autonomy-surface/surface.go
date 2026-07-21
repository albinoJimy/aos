package autonomysurface

import (
	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Surface é a SUPERFÍCIE de autonomia progressiva (AOS-125): torna LEGÍVEL e
// ACCIONÁVEL o nível L0–L5 corrente por (agente, domínio), os critérios/progresso
// rumo à próxima promoção e as transições (promoção/demoção com o seu motivo). É uma
// camada FINA de APRESENTAÇÃO — COMPÕE as portas de AOS-089/090 e o Tracer, LÊ o
// registo de níveis e DELEGA a decisão de revisão ao Controller. NUNCA decide nem
// fixa o nível: não há aqui nenhuma chamada a SetLevel (isso é exclusivo do
// Controller de AOS-090).
//
// COMPOSIÇÃO: o [LevelReader] é o registo de AOS-089 (LÊ nível/histórico); o
// [ReliabilityReader] é a MESMA fonte de sinal que o Controller consulta (o progresso
// apresentado reflecte fielmente a métrica da decisão); o [LevelReviewer] (opcional) é
// o adaptador sobre o Controller.Evaluate a quem a superfície DELEGA o pedido de mais
// autonomia. A [autonomy.AutonomyControlConfig] é a MESMA policy-as-code do Controller
// (AC2/AC3): os critérios e o limiar apresentados são exactamente os que a decisão usa.
type Surface struct {
	reader   LevelReader
	rel      ReliabilityReader
	reviewer LevelReviewer
	tracer   agentruntime.Tracer
	cfg      autonomy.AutonomyControlConfig
	runID    string
}

// Option configura a [Surface] na construção.
type Option func(*Surface)

// WithReliabilityReader liga a fonte do sinal de progresso (AC2). Sem ela, o progresso
// é apresentado como INDISPONÍVEL (janela sem cobertura) — nunca inventa um valor.
func WithReliabilityReader(r ReliabilityReader) Option {
	return func(s *Surface) { s.rel = r }
}

// WithReviewer liga a porta de DECISÃO (AC4): o adaptador sobre o Controller de AOS-090
// a quem [Surface.RequestMoreAutonomy] DELEGA o pedido de revisão. Sem ela, o pedido é
// recusado fail-closed ([ErrNoReviewer]) — a superfície nunca se auto-promove.
func WithReviewer(r LevelReviewer) Option {
	return func(s *Surface) { s.reviewer = r }
}

// WithTracer injecta o [agentruntime.Tracer] que emite os spans de interacção da
// superfície (DoD). Por omissão [agentruntime.NoopTracer] (sem custo). Um tracer nil é
// ignorado (mantém-se o Noop).
func WithTracer(t agentruntime.Tracer) Option {
	return func(s *Surface) {
		if t != nil {
			s.tracer = t
		}
	}
}

// WithRunID fixa o run_id que correlaciona os spans de interacção ao trace do run
// (AttrRunID). Opcional: a ligação de trace faz-se sempre pelo ctx propagado; o runID
// é o rótulo de correlação quando conhecido.
func WithRunID(runID string) Option {
	return func(s *Surface) { s.runID = runID }
}

// New constrói a superfície sobre o [LevelReader] de AOS-089 e a policy-as-code de
// AOS-090 (a MESMA que o Controller usa). Fail-closed: um reader nil devolve
// [ErrNilLevelReader]; uma config inválida devolve o erro de
// [autonomy.AutonomyControlConfig.Validate] (nunca apresenta critérios sobre uma
// política malformada). As portas de sinal/decisão ligam-se por [Option].
func New(reader LevelReader, cfg autonomy.AutonomyControlConfig, opts ...Option) (*Surface, error) {
	if reader == nil {
		return nil, ErrNilLevelReader
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Surface{
		reader: reader,
		tracer: agentruntime.NoopTracer{},
		cfg:    cfg,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Config devolve a policy-as-code que a superfície LÊ para derivar os critérios e o
// limiar de progresso (a MESMA do Controller). Só leitura.
func (s *Surface) Config() autonomy.AutonomyControlConfig { return s.cfg }
