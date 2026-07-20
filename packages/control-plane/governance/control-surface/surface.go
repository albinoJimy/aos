package controlsurface

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// StateReader é a fonte de LEITURA do estado durável corrente de um run, para a
// reflexão (AC4). *[state.Machine] satisfá-lo directamente (Current() State) — o
// estado é o do read-model in-process da máquina, idêntico ao que
// [state.Machine.Rebuild] e o [StateProjector] reconstroem do log (fonte única).
type StateReader interface {
	Current() state.State
}

// RunBinding liga a superfície aos recursos DURÁVEIS de UM run: a máquina de estados
// (para reflectir o estado corrente) e o [control.StateGate] (para o resume delegar a
// transição paused→running no runtime). Uma superfície serve muitos runs (o
// [control.SteerChannel] é multi-run, indexado por run_id); o binding fornece o
// contexto per-run no despacho.
//
// [NewRunBinding] deriva o gate da máquina via [control.NewMachineGate] — COMPÕE
// AOS-023, não reimplementa: a transição continua a ser materializada pela máquina
// real através das arestas que AOS-017 já expõe.
type RunBinding struct {
	// State reflecte o estado durável corrente (obrigatório para o Ack reflectir).
	State StateReader
	// Gate materializa paused→running no resume (obrigatório só no resume). Deve ser
	// um [control.MachineGate] sobre a MESMA máquina de State.
	Gate control.StateGate
}

// NewRunBinding constrói o binding de um run a partir da sua [state.Machine],
// derivando o [control.StateGate] com [control.NewMachineGate]. É a forma canónica —
// garante que a reflexão (State) e a materialização do resume (Gate) falam da MESMA
// máquina durável.
func NewRunBinding(m *state.Machine) RunBinding {
	return RunBinding{State: m, Gate: control.NewMachineGate(m)}
}

// ControlSurface é o CONTRATO UNIFICADO em execução (AC2/AC3): traduz uma
// [ControlMessage] validada para as APIs REAIS de AOS-023 e reflecte de volta o
// estado durável. É uma CAMADA FINA — não detém estado de run próprio; compõe o
// [control.SteerChannel] (durável, multi-run, autenticado) e delega toda a
// durabilidade/não-repúdio/aceitação-garantida nele. NUNCA chama
// state.Machine.Pause/Resume directamente.
//
// Seguro para uso concorrente: não tem estado mutável próprio (o [control.SteerChannel]
// serializa a sua própria projecção).
type ControlSurface struct {
	channel *control.SteerChannel
	tracer  agentruntime.Tracer
	version ControlSchemaVersion
}

// SurfaceOption configura a [ControlSurface] na construção.
type SurfaceOption func(*ControlSurface)

// WithTracer injecta a porta de observabilidade para os spans de interacção (AC6).
// Default: [agentruntime.NoopTracer]. É INDEPENDENTE do tracer do [control.SteerChannel]
// — a superfície emite o seu próprio span (dimensão de canal), o canal emite o dele
// (sinal durável).
func WithTracer(t agentruntime.Tracer) SurfaceOption {
	return func(s *ControlSurface) { s.tracer = t }
}

// WithVersion fixa a versão corrente do contrato que a superfície aceita (as
// mensagens são validadas contra ela). Default: [CurrentVersion].
func WithVersion(v ControlSchemaVersion) SurfaceOption {
	return func(s *ControlSurface) { s.version = v }
}

// NewControlSurface constrói a superfície sobre um [control.SteerChannel] de AOS-023.
// O canal é OBRIGATÓRIO: sem ele não há para onde traduzir os sinais (fail-closed na
// construção — [ErrNilChannel]).
func NewControlSurface(channel *control.SteerChannel, opts ...SurfaceOption) (*ControlSurface, error) {
	if channel == nil {
		return nil, ErrNilChannel
	}
	s := &ControlSurface{
		channel: channel,
		tracer:  agentruntime.NoopTracer{},
		version: CurrentVersion,
	}
	for _, o := range opts {
		o(s)
	}
	if s.tracer == nil {
		s.tracer = agentruntime.NoopTracer{}
	}
	return s, nil
}

// Version devolve a versão corrente do contrato que esta superfície aceita.
func (s *ControlSurface) Version() ControlSchemaVersion { return s.version }

// Correction é a correcção de controlo aplicada numa retoma, projectada para o Ack. É
// a prova OUT-OF-BAND (AC3): entrou pelo canal de controlo AUTENTICADA, pelo que o seu
// Taint é SEMPRE [agentruntime.TaintTrusted] (control-plane) — nunca um dado untrusted
// do data-plane. É uma projecção read-only da [control.Correction] de AOS-023.
type Correction struct {
	// Value é o conteúdo da correcção (a instrução confiável).
	Value []byte
	// EmitterID é o emissor autenticado que a injectou (não-repúdio).
	EmitterID string
	// Taint é o rótulo de proveniência — sempre "trusted" (control-plane).
	Taint string
}

// Trusted indica se a correcção é control-plane confiável (invariante: sempre true
// quando presente — só existe porque a assinatura do emissor validou).
func (c Correction) Trusted() bool { return c.Taint == agentruntime.TaintTrusted }

// Acknowledgement é a resposta do despacho de uma mensagem: o estado durável corrente
// REFLECTIDO (AC4) e, num resume que consumiu uma correcção, a [Correction] aplicada
// (control-plane). É o mesmo para todos os canais — o contrato reflecte o mesmo estado
// em desktop e em chatbot.
type Acknowledgement struct {
	// RunID é o run a que o ack diz respeito.
	RunID string
	// Kind é o kind da mensagem despachada.
	Kind Kind
	// SchemaVersion é a versão do contrato desta resposta (a corrente da superfície).
	SchemaVersion string
	// State é o estado durável corrente reflectido (running/paused/waiting_on_human/...).
	State state.State
	// PendingPause indica se há uma pausa pendente ainda não materializada (o sinal
	// foi aceite; a transição dá-se no fim do turno via GracefulPause).
	PendingPause bool
	// Correction é a correcção aplicada numa retoma (nil se não houve).
	Correction *Correction
}

// Dispatch é o CERNE (AC2/AC3): valida a mensagem contra o schema versionado, emite o
// span de interacção (AC6) e TRADUZ o kind para a API real de AOS-023, delegando toda
// a durabilidade no [control.SteerChannel]. Devolve um [Acknowledgement] com o estado
// durável reflectido (AC4).
//
// Mapeamento (a superfície NÃO implementa a transição — delega):
//
//   - interrupt → [control.SteerChannel.Pause]: marca a pausa PENDENTE (sinal sempre
//     aceite). A transição running→paused NÃO acontece aqui — materializa-se no fim do
//     turno via [control.SteerChannel.GracefulPause], chamada pelo loop do runtime.
//   - steer → [control.SteerChannel.Steer]: injecta a correcção autenticada (pendente).
//   - resume → (opcional [control.SteerChannel.Steer] se a mensagem trouxer correcção)
//     seguido de [control.SteerChannel.Resume], que materializa paused→running via o
//     [control.StateGate] do binding e devolve a [control.Correction] confiável.
//   - state → leitura pura, sem escrita.
//
// A autenticação (fronteira out-of-band, AC3) é feita DENTRO do canal: uma mensagem
// cujo emissor não valide devolve [control.ErrUnauthenticated] e NUNCA vira sinal.
func (s *ControlSurface) Dispatch(ctx context.Context, m ControlMessage, binding RunBinding) (Acknowledgement, error) {
	if err := m.Validate(s.version); err != nil {
		return Acknowledgement{}, err
	}

	// Span de interacção SEMPRE (AC6): a acção é observada mesmo que a tradução falhe
	// a jusante (p.ex. autenticação recusada) — o trace regista a TENTATIVA de controlo.
	s.emitInteractionSpan(ctx, m)

	ack := Acknowledgement{
		RunID:         m.RunID,
		Kind:          m.Kind,
		SchemaVersion: s.version.String(),
	}
	emitter := m.Emitter()

	switch m.Kind {
	case KindInterrupt:
		if err := s.channel.Pause(ctx, m.RunID, emitter); err != nil {
			return Acknowledgement{}, err
		}

	case KindSteer:
		if err := s.channel.Steer(ctx, m.RunID, m.Correction, emitter); err != nil {
			return Acknowledgement{}, err
		}

	case KindResume:
		if binding.Gate == nil {
			return Acknowledgement{}, ErrNilBinding
		}
		// "resume com payload de correcção": se a mensagem trouxer correcção, injecta-a
		// primeiro (steer autenticado com a assinatura DEDICADA da injecção) e só depois
		// retoma — a Resume consome a correcção pendente.
		//
		// GUARDA DE ORDENAÇÃO: só injecta a correcção se o run TEM pausa pendente (é
		// retomável). Sem esta guarda, um resume-com-correcção sobre um run NÃO pausado
		// gravaria o control.steer (correcção pendente) e só depois a Resume recusaria com
		// [state.ErrInvalidTransition] — deixando uma correcção órfã no log que seria
		// aplicada numa retoma FUTURA. Falha cedo com o MESMO erro que um resume LIMPO de
		// um run não-pausado devolve (espelha [control.SteerChannel.Resume]), sem escrever
		// nada. Não é uma fuga de informação: o estado durável é reflectido a todos os
		// canais por contrato (AC4) e a query control.state é leitura pública.
		if len(m.Correction) > 0 {
			if !s.channel.PendingPause(m.RunID) {
				return Acknowledgement{}, state.ErrInvalidTransition
			}
			if err := s.channel.Steer(ctx, m.RunID, m.Correction, m.correctionEmitter()); err != nil {
				return Acknowledgement{}, err
			}
		}
		corr, err := s.channel.Resume(ctx, m.RunID, emitter, binding.Gate)
		if err != nil {
			return Acknowledgement{}, err
		}
		if corr.Present {
			ack.Correction = &Correction{
				Value:     corr.Value,
				EmitterID: corr.EmitterID,
				Taint:     corr.Tainted().Taint, // sempre TaintTrusted (control-plane)
			}
		}

	case KindState:
		// Leitura pura — nenhuma escrita, nenhum sinal. O estado é reflectido abaixo.
	}

	// Reflexão do estado durável corrente (AC4). PendingPause expõe a aceitação-
	// garantida: o sinal já foi gravado, a materialização é diferida.
	if binding.State != nil {
		ack.State = binding.State.Current()
	}
	ack.PendingPause = s.channel.PendingPause(m.RunID)
	return ack, nil
}
