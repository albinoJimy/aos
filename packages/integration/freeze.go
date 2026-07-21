package integration

import (
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/toolset"
)

// RunToolSets é o registo, seguro para concorrência, dos tool sets CONGELADOS por
// run (AOS-050). O composition root regista o snapshot no arranque de cada run
// ([RunToolSets.Put]) e a revalidação por chamada consulta-o ([RunToolSets.Frozen])
// para obter a EXPECTATIVA imutável contra a qual verifica a definição actual. É a
// ponte entre o congelamento (arranque) e a mediação (caminho quente): sem uma
// entrada para o run, a revalidação nega fail-closed (default-deny — um run sem
// tool set congelado não executa tool nenhuma).
//
// Satisfaz [FrozenProvider].
type RunToolSets struct {
	mu    sync.RWMutex
	byRun map[string]*toolset.FrozenToolSet
}

// NewRunToolSets constrói um registo vazio.
func NewRunToolSets() *RunToolSets {
	return &RunToolSets{byRun: make(map[string]*toolset.FrozenToolSet)}
}

// Put regista (ou substitui) o tool set congelado de um run. O snapshot é imutável
// por construção (ver [toolset.FrozenToolSet]); guardá-lo por ponteiro não expõe
// mutação. Um frozen nil é ignorado (não se regista uma expectativa vazia).
func (r *RunToolSets) Put(frozen *toolset.FrozenToolSet) {
	if frozen == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRun[frozen.RunID()] = frozen
}

// Frozen devolve o tool set congelado do run e um booleano de presença. Um run sem
// entrada devolve (nil, false) — a revalidação trata isso como default-deny.
func (r *RunToolSets) Frozen(runID string) (*toolset.FrozenToolSet, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.byRun[runID]
	return f, ok
}

// Release remove a entrada de um run terminado. É idempotente. Liberta a referência
// ao snapshot quando o run acaba (o composition root chama-o no defer de Run).
func (r *RunToolSets) Release(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRun, runID)
}

// ApplyFrozenToGoal materializa o tool set congelado no [agentruntime.Goal] do run:
// fixa Goal.Tools na projecção pinada do snapshot ([toolset.FrozenToolSet.Specs],
// ordem estável congelada) — a MESMA lista que o prefixo imutável do prompt e o
// manifesto de dependências fixam a jusante (o loop constrói ambos a partir de
// Goal.Tools). O RunID do goal é alinhado com o do snapshot (consistência: a
// revalidação indexa o frozen por RunID). Devolve uma CÓPIA do goal — não muta o
// argumento.
//
// NOTA (tools vs skills): o snapshot funde tools+skills+servidores MCP numa única
// projecção na ordem congelada (id, version), pelo que Goal.Skills fica vazio e
// TODAS as dependências pinadas (versões+digests) vão a Goal.Tools. O que importa
// para a integridade de supply-chain — o pin exacto de cada dependência no prefixo
// e no manifesto — é preservado; a categorização tool/skill do manifesto (que
// [toolset.FrozenToolSet.ApplyToManifest] faria) é uma fidelidade menor que o loop
// base não distingue ao materializar Goal.Tools. Preferimos a estabilidade
// byte-a-byte do prefixo (ADR-009), que exige a ordem congelada ÚNICA.
func ApplyFrozenToGoal(goal agentruntime.Goal, frozen *toolset.FrozenToolSet) agentruntime.Goal {
	if frozen == nil {
		return goal
	}
	goal.RunID = frozen.RunID()
	goal.Tools = frozen.Specs()
	goal.Skills = nil
	return goal
}
