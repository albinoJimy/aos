// Package freeze CONGELA o prefixo cache-estável de um run a partir do tool set
// congelado (AOS-060, tecnica/06 §7). É o ponto de COMPOSIÇÃO com o
// toolset.FrozenToolSet de AOS-050 (platform/registry): o snapshot IMUTÁVEL do
// tool set do run — resolvido UMA vez no arranque, imune a mudanças posteriores no
// catálogo — entra no PREFIXO IMUTÁVEL do prompt via o PromptAssembler cache-estável
// de AOS-013.
//
// # Novas tools só em runs novos
//
// O [RunPrefix] é criado UMA vez por run e guarda o assembler cache-estável +
// o tool-set-hash congelado. Uma tool MCP adicionada/alterada a MEIO do run NÃO
// altera este prefixo (o FrozenToolSet é estruturalmente imune a mudanças mid-run
// — prova já em AOS-050); só um congelamento NOVO (o próximo run) vê o novo
// conjunto. A guarda de layout (cache/layout) rejeita um turno cujo tool-set-hash
// diverge do congelado neste RunPrefix.
//
// # Composição, não reimplementação
//
// freeze NÃO reimplementa o registry, o congelamento nem o PromptAssembler. Consome
// o snapshot congelado através de uma PORTA mínima [FrozenToolSet] — que o
// *toolset.FrozenToolSet real satisfaz — mantendo o núcleo do layout sem um import
// direto ao módulo registry (a composição concreta acontece no wiring/teste, à
// imagem do adaptador de fronteira de AOS-059).
package freeze

import (
	"errors"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"

	"github.com/aos-ref/platform/model-gateway/cache/layout"
)

// Erros do congelamento do prefixo.
var (
	// ErrNoToolSet — congelamento sem tool set congelado (fail-closed: o prefixo
	// imutável exige um snapshot).
	ErrNoToolSet = errors.New("freeze: tool set congelado ausente")
	// ErrRunMismatch — o tool set congelado pertence a OUTRO run (o snapshot é
	// por-run; congelar o prefixo de run-A com o tool set de run-B misturaria
	// fronteiras de cache/soberania).
	ErrRunMismatch = errors.New("freeze: tool set congelado de outro run")
)

// FrozenToolSet é a PORTA mínima que o congelamento do prefixo consome do snapshot
// de AOS-050. O *toolset.FrozenToolSet real satisfá-la estruturalmente:
//
//   - RunID — o run a que o snapshot pertence (para casar com o run do prefixo);
//   - Hash — o tool-set-hash congelado (testemunha de imutabilidade, entra no
//     estado por-run da guarda de layout);
//   - Assembler — projecta o snapshot no PREFIXO IMUTÁVEL via o PromptAssembler
//     cache-estável (system + tool set congelado, ordem estável, byte-idêntico).
//
// Manter uma porta (em vez de importar o tipo concreto no núcleo) desacopla o
// layout do módulo registry — a composição concreta é feita no wiring/teste.
type FrozenToolSet interface {
	RunID() string
	Hash() string
	Assembler(system string) *agentruntime.PromptAssembler
}

// RunPrefix é o PREFIXO CONGELADO de um run: o assembler cache-estável (que produz
// o prefixo imutável byte-idêntico em cada turno) + o tool-set-hash congelado + o
// runID. Criado UMA vez por run com [Freeze]; imutável depois — não há método que
// altere o prefixo ou o tool set (novas tools só num RunPrefix NOVO).
type RunPrefix struct {
	runID       string
	toolSetHash string
	assembler   *agentruntime.PromptAssembler
}

// Freeze congela o prefixo cache-estável do run a partir do system prompt e do tool
// set congelado (AOS-050). Fail-closed: tool set ausente, runID vazio, ou um tool
// set de OUTRO run são recusados. O prefixo resultante é byte-idêntico durante toda
// a vida do run.
func Freeze(runID, system string, fts FrozenToolSet) (*RunPrefix, error) {
	if runID == "" {
		return nil, layout.ErrNoRunID
	}
	if fts == nil {
		return nil, ErrNoToolSet
	}
	if fr := fts.RunID(); fr != "" && fr != runID {
		return nil, ErrRunMismatch
	}
	return &RunPrefix{
		runID:       runID,
		toolSetHash: fts.Hash(),
		assembler:   fts.Assembler(system),
	}, nil
}

// RunID devolve o id do run a que este prefixo congelado pertence.
func (r *RunPrefix) RunID() string { return r.runID }

// ToolSetHash devolve o hash do tool set congelado que entrou no prefixo. É a
// testemunha que a guarda de layout pina no turno 1 e contra a qual rejeita um
// tool-set drift a meio do run.
func (r *RunPrefix) ToolSetHash() string { return r.toolSetHash }

// PrefixHash devolve o hash do prefixo imutável (byte-idêntico entre turnos).
func (r *RunPrefix) PrefixHash() string { return r.assembler.PrefixHash() }

// Assemble materializa o prompt do turno REUTILIZANDO o prefixo congelado e
// serializando o tail append-only por cima. Delega ao PromptAssembler cache-estável
// (nunca reordena o prefixo). É a HOT PATH da montagem — sem qualquer referência à
// compressão (prova estrutural off-hot-path).
func (r *RunPrefix) Assemble(turn int, tail []agentruntime.TailSegment) agentruntime.PromptView {
	return r.assembler.Assemble(turn, tail)
}

// Turn materializa o turno e projecta-o num [layout.Turn] pronto para
// [layout.Guard.Admit] — casa o runID, o índice e o tool-set-hash congelado com o
// prompt materializado. É a conveniência que liga a montagem à guarda numa só
// chamada, garantindo que o tool-set-hash entregue à guarda é SEMPRE o congelado
// neste run (nunca um valor solto).
func (r *RunPrefix) Turn(index int, tail []agentruntime.TailSegment) layout.Turn {
	return layout.Turn{
		RunID:       r.runID,
		Index:       index,
		ToolSetHash: r.toolSetHash,
		View:        r.Assemble(index, tail),
	}
}
