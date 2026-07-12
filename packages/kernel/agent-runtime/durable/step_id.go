package durable

import (
	"strconv"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Defaults do formato de step_id. "step-000001" espelha o default sequencial do
// hook StepIdentity de AOS-013 (agent-runtime/hooks.go) — o StepSequencer é um
// drop-in ADITIVO: substitui o default no-op sem mudar a forma dos step_ids já
// observada pelos testes de AOS-013.
const (
	// DefaultStepPrefix é o prefixo dos step_ids ("step-").
	DefaultStepPrefix = "step-"
	// DefaultStepWidth é a largura zero-padded do número do passo (6 → "000001").
	DefaultStepWidth = 6
)

// StepSequencer atribui step_ids MONOTÓNICOS e ESTÁVEIS por passo lógico dentro
// de um run. É a materialização de AOS-014 do hook [agentruntime.StepIdentity]
// (AOS-013): liga o loop ao contrato de idempotência.
//
// # Estabilidade sob retry e replay (ADR-001, tecnica/02 §4)
//
// [StepSequencer.StepID] é uma FUNÇÃO PURA da posição do passo no run (o número
// do turno = a sua posição no log), não de um relógio nem de um UUID aleatório.
// Consequência directa: o mesmo passo lógico recebe SEMPRE o mesmo step_id, quer
// numa primeira execução, quer numa re-tentativa após crash, quer num replay
// determinístico (AOS-016). Os step_ids nunca são reatribuídos — é isto que
// permite que a idempotency key = f(run_id, step_id) identifique o MESMO efeito
// externo entre tentativas, e que o checkpoint de AOS-015 case com o ledger.
//
// Monotonicidade: para turnos crescentes 1,2,3,… os step_ids ordenam-se
// lexicograficamente na mesma ordem (graças ao zero-padding de largura fixa).
type StepSequencer struct {
	prefix string
	width  int
}

// SequencerOption configura o [StepSequencer].
type SequencerOption func(*StepSequencer)

// WithPrefix sobrepõe o prefixo dos step_ids (default [DefaultStepPrefix]).
func WithPrefix(p string) SequencerOption { return func(s *StepSequencer) { s.prefix = p } }

// WithWidth sobrepõe a largura zero-padded do número do passo (default
// [DefaultStepWidth]). Valores <= 0 caem no default.
func WithWidth(w int) SequencerOption { return func(s *StepSequencer) { s.width = w } }

// NewStepSequencer constrói um sequenciador com o formato dado (defaults:
// "step-" + 6 dígitos). O sequenciador é imutável e seguro para uso concorrente
// (não tem estado mutável — os step_ids derivam puramente dos argumentos).
func NewStepSequencer(opts ...SequencerOption) *StepSequencer {
	s := &StepSequencer{prefix: DefaultStepPrefix, width: DefaultStepWidth}
	for _, o := range opts {
		o(s)
	}
	if s.width <= 0 {
		s.width = DefaultStepWidth
	}
	return s
}

// StepID devolve o step_id do turno dado (1-based). Implementa
// [agentruntime.StepIdentity]. PURO: depende apenas de turn e da configuração de
// formato — logo estável entre execução, retry e replay. O runID é aceite por
// conformidade com a interface do hook mas não entra no formato (o step_id é
// namespaced pelo run_id ao formar a chave em [IdempotencyKey]).
func (s *StepSequencer) StepID(_ string, turn int) string {
	return s.prefix + pad(turn, s.width)
}

// SubStepID devolve o step_id de um SUB-PASSO com efeito externo dentro de um
// turno (p.ex. a n-ésima tool call / activity do turno), 1-based no index. É
// DISTINTO do step_id do turno (para não colidir com a dedup do turn.recorded no
// Event Store) e igualmente PURO/estável entre execução e replay. Espelha a
// convenção do loop base ("step-000001-tool-1").
func (s *StepSequencer) SubStepID(runID string, turn, index int) string {
	return s.StepID(runID, turn) + "-tool-" + strconv.Itoa(index)
}

// Key é uma conveniência: deriva directamente a idempotency key canónica do turno
// dado, equivalente a IdempotencyKey(runID, s.StepID(runID, turn)). Propaga a
// validação de [IdempotencyKey].
func (s *StepSequencer) Key(runID string, turn int) (string, error) {
	return IdempotencyKey(runID, s.StepID(runID, turn))
}

// SubKey é a conveniência análoga para um sub-passo (activity) do turno.
func (s *StepSequencer) SubKey(runID string, turn, index int) (string, error) {
	return IdempotencyKey(runID, s.SubStepID(runID, turn, index))
}

// pad formata n com zeros à esquerda até `width` dígitos. Determinístico, sem fmt.
func pad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// Verificação em tempo de compilação: o StepSequencer implementa o hook de
// AOS-013. É o ponto de ligação (WithStepIdentity) — aditivo, sem alterar o loop.
var _ agentruntime.StepIdentity = (*StepSequencer)(nil)
