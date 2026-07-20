package testkit

import (
	"strconv"
	"sync/atomic"

	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// FixtureRunID é o run_id canónico das fixtures. É estável e livre do delimitador
// ':' (que [durable.IdempotencyKey] proíbe nos inputs), pelo que compõe sempre uma
// idempotency key válida.
const FixtureRunID = "run-testkit-0001"

// StepSequencer reexporta o sequenciador de step_ids MONOTÓNICO/PURO de AOS-014
// ([durable.StepSequencer]): o mesmo passo lógico recebe SEMPRE o mesmo step_id,
// quer numa primeira execução, quer num retry após crash, quer num replay
// determinista — é isto que liga a idempotency key ao MESMO efeito externo entre
// tentativas. Composição, não reinvenção: o testkit não reimplementa a derivação.
type StepSequencer = durable.StepSequencer

// NewStepSequencer constrói o sequenciador canónico (formato "step-000001").
// Reexporta [durable.NewStepSequencer] para que os testes de domínio não precisem
// de importar o agent-runtime directamente.
func NewStepSequencer(opts ...durable.SequencerOption) *StepSequencer {
	return durable.NewStepSequencer(opts...)
}

// IdempotencyKey deriva a idempotency key canónica de (run_id, step_id), byte-
// idêntica à que o Event Store usa para deduplicar (run_id + ":" + step_id).
// Reexporta [durable.IdempotencyKey] — a MESMA função pura que a produção usa, para
// que um teste de idempotência exercite a chave real, não uma imitação. Propaga a
// validação (run/step vazios ou com ':' ⇒ erro).
func IdempotencyKey(runID, stepID string) (string, error) {
	return durable.IdempotencyKey(runID, stepID)
}

// FixtureStepID devolve o step_id determinista do turno dado (1-based) sob o
// sequenciador canónico — ex.: FixtureStepID(1) == "step-000001". Estável entre
// execução, retry e replay.
func FixtureStepID(turn int) string {
	return NewStepSequencer().StepID(FixtureRunID, turn)
}

// FixtureKey é a conveniência que compõe [FixtureRunID] com o step_id do turno
// numa idempotency key canónica — o par (run_id, step_id) que um teste de
// idempotência/replay reutiliza para provar que o mesmo passo lógico dedup-a.
func FixtureKey(turn int) (string, error) {
	return IdempotencyKey(FixtureRunID, FixtureStepID(turn))
}

// SeqIDGen é um gerador de identificadores SEQUENCIAL e DETERMINISTA (nunca
// UUID/rand): substitui uma fonte aleatória nos testes que precisam de ids opacos
// estáveis (handles, request_ids, nonces observacionais). O n-ésimo Next é
// `prefix + "-" + n` (1-based). Seguro para uso concorrente (-race); a ordem de
// atribuição entre goroutines não é garantida, mas os valores são únicos e o
// conjunto é determinista.
type SeqIDGen struct {
	prefix string
	n      atomic.Uint64
}

// NewSeqIDGen constrói um gerador com o prefixo dado (vazio ⇒ "id").
func NewSeqIDGen(prefix string) *SeqIDGen {
	if prefix == "" {
		prefix = "id"
	}
	return &SeqIDGen{prefix: prefix}
}

// Next devolve o próximo id sequencial (`prefix-1`, `prefix-2`, ...). Atómico.
func (g *SeqIDGen) Next() string {
	return g.prefix + "-" + strconv.FormatUint(g.n.Add(1), 10)
}

// Reset repõe o contador a zero — útil entre subtestes que exigem a mesma
// sequência de ids do início. Serializa com um Next concorrente via o próprio
// atómico (mas destina-se a uso entre fases, não durante emissão concorrente).
func (g *SeqIDGen) Reset() { g.n.Store(0) }
