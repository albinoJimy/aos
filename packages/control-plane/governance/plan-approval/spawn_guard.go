package planapproval

import (
	"context"
	"sync"
)

// SpawnGuard envolve um [Spawner] (a fronteira PRE-SPAWN) e RECUSA um Spawn de um run
// cujo plano NÃO foi aprovado pelo [PlanGate]. É a prova ESTRUTURAL do AC1
// (defesa-em-profundidade): mesmo que uma cadeia de wiring tentasse spawnar sem passar
// pela aprovação-de-plano, o guard nega fail-closed — o custo de tokens do plano fica
// ADIADO até à decisão. O [PlanGate] regista os runs aprovados (via [WithSpawnGuard])
// após uma decisão approve/edit; um Spawn de run não-registado devolve [ErrPlanNotApproved].
//
// Satisfaz [Spawner] — é um decorador transparente que o scheduler pode envolver no
// wiring. Seguro para concorrência. Construir com [NewSpawnGuard].
type SpawnGuard struct {
	inner    Spawner
	mu       sync.Mutex
	approved map[string]bool
}

// NewSpawnGuard envolve o spawner dado. Um spawner nil é fail-closed ([ErrNilSpawner]).
func NewSpawnGuard(inner Spawner) (*SpawnGuard, error) {
	if inner == nil {
		return nil, ErrNilSpawner
	}
	return &SpawnGuard{inner: inner, approved: make(map[string]bool)}, nil
}

// markApproved regista o run como aprovado (chamado pelo [PlanGate] após approve/edit).
// Unexported: só a decisão de aprovação-de-plano pode libertar um run — nada mais no
// pacote consumidor pode marcar um run como aprovado por fora do gate.
func (g *SpawnGuard) markApproved(runID string) {
	g.mu.Lock()
	g.approved[runID] = true
	g.mu.Unlock()
}

// IsApproved indica se o plano do run foi aprovado (leitura para diagnóstico/wiring).
func (g *SpawnGuard) IsApproved(runID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.approved[runID]
}

// Spawn implementa [Spawner]: só delega no spawner envolvido SE o plano do run tiver
// sido aprovado; senão RECUSA com [ErrPlanNotApproved] (fail-closed) — nenhum sub-agente
// é lançado antes da aprovação do plano (AC1). O run é lido sob lock para uma decisão
// consistente com marcações concorrentes.
func (g *SpawnGuard) Spawn(ctx context.Context, runID string) error {
	g.mu.Lock()
	ok := g.approved[runID]
	g.mu.Unlock()
	if !ok {
		return ErrPlanNotApproved
	}
	return g.inner.Spawn(ctx, runID)
}
