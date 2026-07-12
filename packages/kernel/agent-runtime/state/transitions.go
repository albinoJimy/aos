package state

// State é um dos dez estados canónicos do run durável (AOS-017). O tipo é uma
// string legível para que o valor persistido no evento de transição seja
// auto-descritivo no log append-only do Event Store (ADR-001).
type State string

// Os DEZ estados canónicos da máquina durável (exactamente dez — a fonte substitui
// a máquina grosseira `ready → running → complete + blocked`, que confundia
// suspensão legítima com falha, ver tecnica/02 §5). Repartem-se em três famílias:
//
//   - ACTIVOS:   Ready (elegível para claim), Running (a executar sob fencing token).
//   - SUSPENSOS: WaitingOnTool, WaitingOnHuman, Paused — suspensão LEGÍTIMA e
//     retomável, distinta de falha e de worker morto (é a separação que impede um
//     gate humano de parecer um zombie e vice-versa).
//   - TERMINAIS/RECUPERAÇÃO: Complete, Failed (falha recuperável que entra em
//     Compensating), Compensating (saga de rollback), Killed (terminado por política
//     ou timeout fail-closed), TimedOut (excedeu o wall-clock).
const (
	// Ready — run elegível para ser reclamado (claim) por um worker.
	Ready State = "ready"
	// Running — a executar sob um fencing token válido (o claim atribuiu-o).
	Running State = "running"
	// WaitingOnTool — bloqueado numa activity externa (suspensão legítima).
	WaitingOnTool State = "waiting_on_tool"
	// WaitingOnHuman — parado num gate HITL; com timeout fail-closed para Killed.
	WaitingOnHuman State = "waiting_on_human"
	// Paused — steer/interrupt aceite; retomável com correcção (AOS-023 acciona).
	Paused State = "paused"
	// Complete — terminou com sucesso (terminal absorvente).
	Complete State = "complete"
	// Failed — erro RECUPERÁVEL; entra em Compensating para a saga de rollback.
	Failed State = "failed"
	// Compensating — a reproduzir o log em sentido inverso (saga); regressa a Ready.
	Compensating State = "compensating"
	// Killed — terminado por política ou timeout fail-closed (terminal absorvente).
	Killed State = "killed"
	// TimedOut — excedeu o wall-clock configurado a partir de Running (terminal).
	TimedOut State = "timed_out"
)

// AllStates é a lista canónica dos dez estados, por ordem de família. Serve a
// varredura da matriz 10×10 nos testes e qualquer enumeração exaustiva.
var AllStates = []State{
	Ready, Running, WaitingOnTool, WaitingOnHuman, Paused,
	Complete, Failed, Compensating, Killed, TimedOut,
}

// transition é um par ordenado (origem → destino). É a UNIDADE da tabela
// declarativa: a máquina de estados é DADOS (um conjunto de pares válidos), não
// lógica espalhada por if/switch — o que a torna testável por matriz e permite ao
// replay reconstruir estado sem re-derivar regras.
type transition struct {
	From State
	To   State
}

// validTransitions é a TABELA DECLARATIVA CANÓNICA de AOS-017 — o conjunto EXACTO
// dos pares (from → to) permitidos, seguindo o stateDiagram da fonte (tecnica/02
// §5). Qualquer par NÃO presente nesta tabela é INVÁLIDO e rejeitado com
// [ErrInvalidTransition] sem tocar no estado persistido.
//
// Tabela completa (13 pares):
//
//	ready            → running          (EXIGE fencing token válido — o claim)
//	running          → waiting_on_tool  (bloqueio numa activity externa)
//	waiting_on_tool  → running          (activity resolvida; retoma sob o mesmo lease)
//	running          → waiting_on_human (gate de risco HITL)
//	waiting_on_human → running          (aprovação assinada; retoma sob o mesmo lease)
//	waiting_on_human → killed           (timeout fail-closed — ADR-013)
//	running          → paused           (steer/interrupt aceite)
//	paused           → running          (resume; retoma sob o mesmo lease)
//	running          → complete         (sucesso)
//	running          → failed           (erro recuperável)
//	running          → timed_out        (excede o wall-clock)
//	failed           → compensating     (saga de rollback)
//	compensating     → ready            (retry idempotente após compensação)
//
// Estados TERMINAIS absorventes (zero transições de saída): complete, killed,
// timed_out. Failed NÃO é absorvente — a única saída é para compensating (é a
// falha recuperável de que fala a saga em tecnica/02 §7).
//
// NOTA de contrato (AOS-018): o fencing token é exigido APENAS em ready → running
// (o CLAIM). As retomas a partir de suspensão (waiting_on_tool/waiting_on_human/
// paused → running) reentram sob o lease JÁ detido — não voltam a reclamar, logo
// não re-exigem token. Ver [RequiresFencingToken].
var validTransitions = map[transition]struct{}{
	{Ready, Running}:          {},
	{Running, WaitingOnTool}:  {},
	{WaitingOnTool, Running}:  {},
	{Running, WaitingOnHuman}: {},
	{WaitingOnHuman, Running}: {},
	{WaitingOnHuman, Killed}:  {},
	{Running, Paused}:         {},
	{Paused, Running}:         {},
	{Running, Complete}:       {},
	{Running, Failed}:         {},
	{Running, TimedOut}:       {},
	{Failed, Compensating}:    {},
	{Compensating, Ready}:     {},
}

// IsValidTransition consulta a TABELA declarativa: devolve true sse (from → to)
// for um par permitido. É a única fonte de verdade da validação — a [Machine]
// delega aqui. Determinística e pura (sem I/O, sem estado).
func IsValidTransition(from, to State) bool {
	_, ok := validTransitions[transition{from, to}]
	return ok
}

// RequiresFencingToken indica se a transição (from → to) exige um fencing token
// válido como pré-condição. É verdadeiro SÓ para o claim ready → running (contrato
// partilhado com AOS-018 e o Escalonador do EPIC-03). As retomas de suspensão para
// running reentram sob o lease já detido e NÃO re-exigem token.
func RequiresFencingToken(from, to State) bool {
	return from == Ready && to == Running
}

// IsKnown indica se s é um dos dez estados canónicos (defesa contra estados
// forjados vindos de um log corrompido/legado durante o Rebuild).
func IsKnown(s State) bool {
	switch s {
	case Ready, Running, WaitingOnTool, WaitingOnHuman, Paused,
		Complete, Failed, Compensating, Killed, TimedOut:
		return true
	default:
		return false
	}
}

// IsTerminal indica se s é um estado TERMINAL ABSORVENTE — sem qualquer transição
// de saída na tabela (complete, killed, timed_out). Failed NÃO é absorvente (sai
// para compensating); use [IsFailure] para a semântica de falha.
func IsTerminal(s State) bool {
	switch s {
	case Complete, Killed, TimedOut:
		return true
	default:
		return false
	}
}

// IsSuspended indica se s é um estado de SUSPENSÃO LEGÍTIMA e retomável
// (waiting_on_tool, waiting_on_human, paused) — deliberadamente distinto de falha e
// de worker morto (tecnica/02 §5). É a distinção que dá valor à máquina rica.
func IsSuspended(s State) bool {
	switch s {
	case WaitingOnTool, WaitingOnHuman, Paused:
		return true
	default:
		return false
	}
}

// IsFailure indica se s é o estado de falha RECUPERÁVEL (failed) — o ponto de
// entrada da saga de compensação (failed → compensating → ready).
func IsFailure(s State) bool { return s == Failed }
