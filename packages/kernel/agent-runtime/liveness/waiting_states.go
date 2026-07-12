package liveness

import (
	"context"
	"sync"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
)

// Clock é o relógio INJECTÁVEL do pacote — a fonte do wall-clock que decide a
// expiração do gate de espera ([WaitingGate]) e a acumulação do relógio de trabalho
// activo ([WorkClock]). Injectá-lo torna os testes de timeout/acumulação
// DETERMINÍSTICOS, sem sleeps frágeis. Default: [systemClock]. Alinha-se com o
// [state.Clock] de AOS-017 e o [durable.Clock] de AOS-018 (mesma assinatura).
type Clock interface {
	Now() time.Time
}

// ClockFunc adapta uma função a [Clock].
type ClockFunc func() time.Time

// Now implementa [Clock].
func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// ---------------------------------------------------------------------------
// Classificação de liveness
// ---------------------------------------------------------------------------

// Classification é o veredicto do [ZombieClassifier] sobre um run. É a distinção de
// PRIMEIRA CLASSE entre suspensão legítima e worker preso que impede um gate humano
// de parecer um zombie e vice-versa (tecnica/02 §5-6).
type Classification string

const (
	// Alive — o run está saudável: em [state.Running] com o lease de trabalho vivo, ou
	// num estado activo/de-recuperação não-terminal (ready/failed/compensating) que não
	// é nem espera legítima nem zombie. Nunca deve ser morto por liveness.
	Alive Classification = "alive"

	// Zombie — o run está em [state.Running] MAS o lease de trabalho activo (o
	// heartbeat de AOS-018) EXPIROU: o worker está realmente preso/morto. É o
	// verdadeiro-positivo que a reatribuição por lease/fencing trata (o worker obsoleto
	// é fenced-out; um novo claim minta um token maior). NÃO é morte do run — é
	// reatribuição.
	Zombie Classification = "zombie"

	// WaitingLegitimate — o run está numa SUSPENSÃO LEGÍTIMA e retomável
	// (waiting_on_human/waiting_on_tool/paused). O relógio de trabalho activo está
	// PAUSADO, pelo que NUNCA é zombie — MESMO que o lease de trabalho estivesse
	// expirado (a expiração do lease de trabalho não conta em estados de espera). É a
	// invariante não-negociável de AOS-019.
	WaitingLegitimate Classification = "waiting_legitimate"

	// GateExpired — o run está em [state.WaitingOnHuman] e o GATE TTL próprio do gate
	// humano foi EXCEDIDO. NÃO é um zombie — é o timeout fail-closed do gate (ADR-013):
	// sem aprovação no prazo, o run transita waiting_on_human → killed (via
	// [state.Machine.CheckDeadlines]). Distingue-se de [Zombie] porque a acção é MATAR
	// o run (política), não reatribuí-lo.
	GateExpired Classification = "gate_expired"

	// Terminal — o run está num estado terminal absorvente (complete/killed/timed_out).
	// Não há liveness a avaliar; a detecção de zombi ignora-o.
	Terminal Classification = "terminal"
)

// IsZombie reporta se a classificação é o verdadeiro-positivo de worker preso.
func (c Classification) IsZombie() bool { return c == Zombie }

// IsLegitimateWait reporta se a classificação é suspensão legítima (nunca zombie).
func (c Classification) IsLegitimateWait() bool { return c == WaitingLegitimate }

// RequiresKill reporta se a classificação implica MATAR o run fail-closed — só
// [GateExpired] (timeout do gate humano, ADR-013). [Zombie] NÃO implica matar o run:
// implica reatribuição por lease/fencing (o run continua vivo com um novo worker).
func (c Classification) RequiresKill() bool { return c == GateExpired }

// RunLiveness é a fotografia dos sinais de liveness de um run, a entrada do
// [ZombieClassifier.Classify]. Os campos são DELIBERADAMENTE separados por eixo:
//
//   - State: o estado durável corrente (AOS-017) — a família (activo/suspenso/terminal)
//     é o primeiro discriminante da classificação.
//   - WorkLeaseExpired: se o LEASE DE TRABALHO ACTIVO (o heartbeat de AOS-018) expirou.
//     Só é sinal de zombi em [state.Running]; nos estados de espera o relógio de
//     trabalho está pausado e este flag é IGNORADO (não há falso-positivo).
//   - GateDeadlineExceeded: se o GATE TTL próprio do [state.WaitingOnHuman] foi
//     excedido (relógio de espera, distinto do de trabalho — ver [WaitingGate]). Só
//     tem efeito em waiting_on_human; ignorado nos demais estados.
//
// A separação dos DOIS RELÓGIOS (trabalho activo vs espera) é o cerne de AOS-019: um
// lease de trabalho expirado NUNCA classifica uma espera como zombi; um gate de espera
// excedido MATA o run fail-closed sem passar por "zombi".
type RunLiveness struct {
	State                state.State
	WorkLeaseExpired     bool
	GateDeadlineExceeded bool
}

// ClassifierObserver é o gancho de observabilidade da classificação (contadores para
// o circuit breaker multi-sinal / SLIs de tecnica/08). Recebe rótulos, nunca segredos.
// Default: [NopClassifierObserver].
type ClassifierObserver interface {
	// Classified é chamado após cada [ZombieClassifier.Classify], com o estado avaliado
	// e o veredicto.
	Classified(s state.State, c Classification)
}

// NopClassifierObserver descarta os eventos de observabilidade. É o default.
type NopClassifierObserver struct{}

// Classified implementa [ClassifierObserver].
func (NopClassifierObserver) Classified(state.State, Classification) {}

// ZombieClassifier decide, por run, entre suspensão legítima, worker preso, timeout
// de gate e terminal — SEM confundir os eixos. É PURO e determinístico (sem I/O, sem
// estado mutável partilhado além do observer): a mesma [RunLiveness] produz sempre a
// mesma [Classification]. Seguro para uso concorrente.
//
// # Regras (fail-closed onde importa)
//
//	State ∈ {waiting_on_human, waiting_on_tool, paused}          → WaitingLegitimate
//	    ... EXCEPTO waiting_on_human com GateDeadlineExceeded     → GateExpired
//	State == running  &&  WorkLeaseExpired                        → Zombie
//	State == running  &&  !WorkLeaseExpired                       → Alive
//	State terminal (complete/killed/timed_out)                   → Terminal
//	restantes activos/recuperação (ready/failed/compensating)    → Alive
//
// A invariante não-negociável: um estado de espera legítima NUNCA é [Zombie] (mesmo
// com o lease de trabalho expirado); um worker realmente preso em running com o lease
// expirado É [Zombie] (não-regressão).
type ZombieClassifier struct {
	obs ClassifierObserver
}

// ClassifierOption configura o [ZombieClassifier].
type ClassifierOption func(*ZombieClassifier)

// WithClassifierObserver injecta o gancho de observabilidade (default
// [NopClassifierObserver]).
func WithClassifierObserver(o ClassifierObserver) ClassifierOption {
	return func(c *ZombieClassifier) {
		if o != nil {
			c.obs = o
		}
	}
}

// NewZombieClassifier constrói o classificador. Não tem configuração obrigatória (é
// puro); os opts ajustam apenas a observabilidade.
func NewZombieClassifier(opts ...ClassifierOption) *ZombieClassifier {
	c := &ZombieClassifier{obs: NopClassifierObserver{}}
	for _, o := range opts {
		o(c)
	}
	if c.obs == nil {
		c.obs = NopClassifierObserver{}
	}
	return c
}

// Classify aplica as regras de AOS-019 sobre run e devolve a classificação. O ctx é
// aceite para simetria de contrato e futura propagação de tracing; a decisão é pura e
// não faz I/O. Total: qualquer [state.State] (incluindo um forjado/desconhecido) é
// classificado conservadoramente — um estado não-canónico cai em [Alive] (NÃO é morto
// como zombi: a detecção de zombi nunca dispara sobre o que não sabe interpretar; a
// defesa contra estados corrompidos é do Rebuild fail-closed de AOS-017).
func (c *ZombieClassifier) Classify(_ context.Context, run RunLiveness) Classification {
	res := classify(run)
	c.obs.Classified(run.State, res)
	return res
}

// classify é o núcleo puro (sem observer) — reusado pelos benchmarks e testável em
// isolamento.
func classify(run RunLiveness) Classification {
	switch run.State {
	case state.WaitingOnHuman:
		// Gate humano: o TTL PRÓPRIO do gate (relógio de espera) é o único caminho para
		// terminar; o lease de TRABALHO expirado é irrelevante aqui (o relógio de
		// trabalho está pausado). Excedido o gate → killed fail-closed (ADR-013), NÃO
		// zombi.
		if run.GateDeadlineExceeded {
			return GateExpired
		}
		return WaitingLegitimate

	case state.WaitingOnTool, state.Paused:
		// Suspensão legítima sem gate humano: NUNCA zombi, mesmo com o lease de trabalho
		// expirado. O GateDeadlineExceeded é específico do gate humano e ignorado aqui.
		return WaitingLegitimate

	case state.Running:
		// Único caminho para zombi: worker realmente preso — em running com o lease de
		// trabalho activo (heartbeat de AOS-018) expirado.
		if run.WorkLeaseExpired {
			return Zombie
		}
		return Alive

	default:
		// Terminais absorventes não têm liveness a avaliar.
		if state.IsTerminal(run.State) {
			return Terminal
		}
		// ready/failed/compensating (e, defensivamente, estados não-canónicos): activos
		// ou de recuperação, nem espera nem zombi.
		return Alive
	}
}

// ---------------------------------------------------------------------------
// Gate TTL fail-closed do waiting_on_human (relógio de ESPERA)
// ---------------------------------------------------------------------------

// WaitingGate é o RELÓGIO DE ESPERA do gate humano — distinto e independente do
// heartbeat de trabalho activo (AOS-018). Governa o TTL de aprovação: passado o TTL
// desde a entrada em [state.WaitingOnHuman] sem aprovação, o gate está EXCEDIDO e o
// run deve transitar para [state.Killed] fail-closed (ADR-013). Alinha-se com o
// [state.Machine.CheckDeadlines] de AOS-017 — a MESMA fronteira inclusiva e o MESMO
// relógio injectável — para que a decisão do gate e a transição durável concordem.
//
// O gate é PURO (sem estado mutável): recebe o instante de entrada no estado
// (Machine.EnteredAt) e reporta se o TTL foi excedido AGORA (clock.Now()). Seguro para
// uso concorrente.
type WaitingGate struct {
	ttl   time.Duration
	clock Clock
}

// GateOption configura o [WaitingGate].
type GateOption func(*WaitingGate)

// WithGateClock injecta o relógio de espera (default [systemClock]). Usar nos testes
// de timeout do gate para determinismo sem sleeps. Ignora nil.
func WithGateClock(clk Clock) GateOption {
	return func(g *WaitingGate) {
		if clk != nil {
			g.clock = clk
		}
	}
}

// NewWaitingGate constrói o gate com o TTL de aprovação dado. ttl tem de ser > 0
// ([ErrInvalidGateTTL]) — um gate sem TTL não é fail-closed, o que contraria o
// propósito (ADR-013).
//
// AVISO de DRIFT: este construtor recebe o SEU PRÓPRIO ttl e o SEU PRÓPRIO relógio,
// SEPARADOS dos da Machine. Nada aqui garante que ttl == [state.WithHumanApprovalTTL]
// nem que o relógio é o mesmo — o alinhamento fica por CONVENÇÃO do chamador. Se
// divergirem, o veredicto GateExpired do classificador dessincroniza do kill real de
// [state.Machine.CheckDeadlines] (o kill continua fail-closed no humanTTL da Machine;
// só o SINAL diverge no tempo). Para eliminar o drift POR CONSTRUÇÃO, prefira
// [NewWaitingGateFrom], que deriva ambos da própria Machine.
func NewWaitingGate(ttl time.Duration, opts ...GateOption) (*WaitingGate, error) {
	if ttl <= 0 {
		return nil, ErrInvalidGateTTL
	}
	g := &WaitingGate{ttl: ttl, clock: systemClock{}}
	for _, o := range opts {
		o(g)
	}
	if g.clock == nil {
		g.clock = systemClock{}
	}
	return g, nil
}

// NewWaitingGateFrom deriva o [WaitingGate] directamente da [state.Machine] de
// AOS-017: reusa o MESMO TTL ([state.Machine.HumanApprovalTTL]) e o MESMO relógio
// ([state.Machine.Clock]) que governam o kill fail-closed de
// [state.Machine.CheckDeadlines]. É o construtor PREFERIDO para o wiring de produção —
// elimina, POR CONSTRUÇÃO, o drift de configuração que [NewWaitingGate] permite: o
// veredicto GateExpired do classificador e o kill real passam a partilhar a única fonte
// de verdade (TTL + relógio da Machine).
//
// Fail-closed: se a Machine não tiver TTL humano configurado (HumanApprovalTTL() == 0),
// devolve [ErrInvalidGateTTL] — o mesmo critério de [NewWaitingGate] —, tornando um
// wiring em falta um ERRO EXPLÍCITO em vez de um gate silenciosamente permissivo. m nil
// devolve [ErrNilMachine].
func NewWaitingGateFrom(m *state.Machine) (*WaitingGate, error) {
	if m == nil {
		return nil, ErrNilMachine
	}
	// state.Clock e liveness.Clock têm o mesmo método (Now() time.Time): o relógio da
	// Machine satisfaz estruturalmente [Clock], pelo que gate e Machine partilham o
	// MESMO "agora" — sem duas fontes a divergir.
	return NewWaitingGate(m.HumanApprovalTTL(), WithGateClock(m.Clock()))
}

// TTL devolve o TTL de aprovação configurado.
func (g *WaitingGate) TTL() time.Duration { return g.ttl }

// Deadline devolve o instante-limite do gate (enteredAt + TTL): a partir dele
// (inclusive) o gate está excedido.
func (g *WaitingGate) Deadline(enteredAt time.Time) time.Time {
	return enteredAt.Add(g.ttl)
}

// Remaining devolve o tempo até ao limite do gate a partir de agora. Zero ou negativo
// significa que o gate JÁ foi excedido (fail-closed).
func (g *WaitingGate) Remaining(enteredAt time.Time) time.Duration {
	return g.Deadline(enteredAt).Sub(g.clock.Now())
}

// Exceeded reporta se o TTL do gate já esgotou desde enteredAt, no relógio injectado.
// A fronteira é INCLUSIVA (now == deadline ⇒ excedido), IGUAL ao critério de
// [state.Machine.CheckDeadlines] (!now.Before(enteredAt.Add(ttl))) — fail-closed. É
// este bool que alimenta [RunLiveness.GateDeadlineExceeded] para o classificador
// devolver [GateExpired].
//
// Relógio: WALL-CLOCK. enteredAt vem de [state.Machine.EnteredAt], que teve a
// componente monotónica removida; a comparação assume um relógio sem saltos (um ajuste
// NTP desloca o instante). É a MESMA degradação de CheckDeadlines, pelo que gate e
// Machine concordam sempre entre si.
func (g *WaitingGate) Exceeded(enteredAt time.Time) bool {
	return !g.clock.Now().Before(g.Deadline(enteredAt))
}

// RunLivenessFrom compõe uma [RunLiveness] a partir do estado corrente, do flag de
// lease de trabalho expirado (de AOS-018) e — SÓ quando o estado é
// [state.WaitingOnHuman] — da avaliação do gate de espera sobre enteredAt. É o ponto
// de integração aditiva: liga a Machine (AOS-017), o lease (AOS-018) e o gate deste
// ticket numa entrada única para o [ZombieClassifier], sem que nenhum deles conheça o
// outro. GateDeadlineExceeded é derivado SÓ para waiting_on_human (o gate humano é
// específico desse estado); nos demais estados fica false.
//
// ADVISORY, NÃO fail-closed: a classificação [GateExpired] daqui derivada é apenas um
// SINAL para observabilidade/reatribuição — NÃO é o executor do kill. A fronteira
// fail-closed (waiting_on_human → killed) é EXCLUSIVAMENTE de
// [state.Machine.CheckDeadlines] (AOS-017), que o chamador TEM de correr
// periodicamente. Corolário fail-OPEN: se gate for nil, um waiting_on_human é
// classificado [WaitingLegitimate] INDEFINIDAMENTE (o classificador sozinho nunca
// fecha o gate). Passar gate nil em waiting_on_human é, portanto, um wiring INCOMPLETO,
// não uma espera legítima — construa o gate com [NewWaitingGateFrom] para o derivar da
// mesma Machine cujo CheckDeadlines executa o kill.
func RunLivenessFrom(s state.State, workLeaseExpired bool, gate *WaitingGate, enteredAt time.Time) RunLiveness {
	rl := RunLiveness{State: s, WorkLeaseExpired: workLeaseExpired}
	if s == state.WaitingOnHuman && gate != nil {
		rl.GateDeadlineExceeded = gate.Exceeded(enteredAt)
	}
	return rl
}

// ---------------------------------------------------------------------------
// Contrato de sinais para o circuit breaker multi-sinal (EPIC-08) — só a EXCLUSÃO
// ---------------------------------------------------------------------------

// CountsAsActiveWork reporta se o estado s CONTA como trabalho activo para o sinal de
// "ausência de progresso" do circuit breaker multi-sinal (tecnica/08 §6). Só
// [state.Running] conta: é o único estado em que o worker está de facto a trabalhar.
// Todos os demais — em particular os de espera legítima — NÃO contam.
func CountsAsActiveWork(s state.State) bool { return s == state.Running }

// IsWorkPaused reporta se, no estado s, o RELÓGIO DE TRABALHO ACTIVO está PAUSADO — os
// estados de suspensão legítima (waiting_on_human/waiting_on_tool/paused). Nestes o
// heartbeat de trabalho não é renovado (pausa) mas TAMBÉM não conta como
// expirado-para-zumbi, e o breaker EXCLUI o seu tempo do sinal "sem progresso". É o
// espelho de [state.IsSuspended].
func IsWorkPaused(s state.State) bool { return state.IsSuspended(s) }

// WorkClock é o RELÓGIO DE TRABALHO ACTIVO acumulado — o contrato de EXCLUSÃO que
// AOS-019 oferece ao circuit breaker multi-sinal de EPIC-08. Acumula APENAS o tempo
// passado em [state.Running]; o tempo em qualquer estado de espera (ou outro) NÃO
// conta. O breaker consome [WorkClock.ActiveWork] como o seu sinal de wall-clock/
// ausência-de-progresso de TRABALHO, garantindo que uma longa espera humana nunca é
// lida como "sem progresso" (critério 2 de AOS-019). Aqui implementa-se SÓ a exclusão;
// o breaker completo (avaliação multi-sinal, trip) é EPIC-08.
//
// O relógio é INJECTÁVEL (determinismo em teste, sem sleeps). Seguro para uso
// concorrente (um mutex serializa Observe/ActiveWork).
type WorkClock struct {
	clock Clock

	mu      sync.Mutex
	total   time.Duration // tempo activo já acumulado (fecho de spans de running)
	since   time.Time     // início do span de running aberto (válido sse running)
	running bool
}

// WorkClockOption configura o [WorkClock].
type WorkClockOption func(*WorkClock)

// WithWorkClockClock injecta o relógio (default [systemClock]). Ignora nil.
func WithWorkClockClock(clk Clock) WorkClockOption {
	return func(w *WorkClock) {
		if clk != nil {
			w.clock = clk
		}
	}
}

// NewWorkClock constrói o relógio de trabalho activo, começando PARADO (zero tempo
// acumulado, sem span aberto). O tempo só começa a acumular quando [WorkClock.Observe]
// vê [state.Running].
func NewWorkClock(opts ...WorkClockOption) *WorkClock {
	w := &WorkClock{clock: systemClock{}}
	for _, o := range opts {
		o(w)
	}
	if w.clock == nil {
		w.clock = systemClock{}
	}
	return w
}

// Observe regista a ENTRADA no estado s no instante corrente do relógio. Fecha
// qualquer span de running aberto (acumulando o seu tempo em total) e, se s for
// [state.Running], abre um novo span. Assim o tempo em estados de espera fica de fora
// do total — a EXCLUSÃO que o breaker precisa. Chamável a cada transição da Machine
// (AOS-017) ou periodicamente.
func (w *WorkClock) Observe(s state.State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	if w.running {
		w.total += now.Sub(w.since)
		w.running = false
	}
	if s == state.Running {
		w.since = now
		w.running = true
	}
}

// ActiveWork devolve o tempo TOTAL de trabalho activo acumulado até agora — a soma dos
// spans em [state.Running], EXCLUINDO todo o tempo em espera/pausa/outros. Se um span
// de running estiver aberto, inclui o seu decorrido até ao instante corrente. É o
// sinal que o breaker de EPIC-08 usa como wall-clock/ausência-de-progresso de trabalho.
func (w *WorkClock) ActiveWork() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.total
	if w.running {
		t += w.clock.Now().Sub(w.since)
	}
	return t
}

// Running reporta se há um span de trabalho activo aberto (o último Observe viu
// [state.Running]).
func (w *WorkClock) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}
