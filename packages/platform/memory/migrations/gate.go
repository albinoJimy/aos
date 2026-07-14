package migrations

import (
	"context"
	"sync"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/schema"
)

// GateRequest descreve uma migração submetida ao eval-gate. É puro (sem estado):
// o gate decide só a partir da natureza da mudança e da identidade da migração.
type GateRequest struct {
	MigrationID string
	Class       domain.MemoryClass
	From        schema.Version
	To          schema.Version
	Kind        schema.ChangeKind
}

// Decision é o veredicto do gate. Allowed=false é sempre acompanhado de Reason.
type Decision struct {
	Allowed bool
	Reason  string
}

// Gate é a PORTA de admission control da evolução de schema (ADR-012, tecnica/11).
// É o mesmo princípio do eval-gate da auto-modificação: uma mudança MAJOR (quebra
// de contrato) só entra em produção depois de avaliada e aprovada. A implementação
// completa (golden-set, trace-diffing, ratificação assinada) é do EPIC-05; aqui
// vive apenas o CONTRATO da porta e uma implementação de referência determinística.
type Gate interface {
	// Evaluate decide se a migração pode prosseguir. Deve ser determinística e sem
	// efeitos colaterais.
	Evaluate(ctx context.Context, req GateRequest) Decision
}

// EvalGate é a implementação de REFERÊNCIA da porta. A sua política é fail-closed
// quanto a MAJOR: mudanças MINOR/PATCH/None passam sempre (retrocompatíveis); uma
// mudança MAJOR só passa se a migração constar do conjunto de aprovações
// pré-registadas (o análogo à ratificação humana assinada). Sem aprovação, MAJOR
// é RECUSADA.
//
// É seguro para concorrência.
type EvalGate struct {
	mu       sync.RWMutex
	approved map[string]bool
}

// NewEvalGate constrói o gate de referência, opcionalmente com um conjunto inicial
// de IDs de migração MAJOR já aprovados.
func NewEvalGate(approvedMigrationIDs ...string) *EvalGate {
	g := &EvalGate{approved: make(map[string]bool, len(approvedMigrationIDs))}
	for _, id := range approvedMigrationIDs {
		g.approved[id] = true
	}
	return g
}

// Approve regista a aprovação de uma migração MAJOR (o gancho onde o EPIC-05
// ligará a ratificação assinada). Idempotente.
func (g *EvalGate) Approve(migrationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.approved[migrationID] = true
}

// Evaluate implementa Gate.
func (g *EvalGate) Evaluate(_ context.Context, req GateRequest) Decision {
	if req.Kind != schema.ChangeMajor {
		return Decision{Allowed: true, Reason: "mudanca retrocompativel (" + req.Kind.String() + "): nao exige gate"}
	}
	g.mu.RLock()
	ok := g.approved[req.MigrationID]
	g.mu.RUnlock()
	if ok {
		return Decision{Allowed: true, Reason: "migracao MAJOR aprovada"}
	}
	return Decision{Allowed: false, Reason: "migracao MAJOR sem aprovacao (fail-closed)"}
}

// denyMajorGate é o gate por omissão quando nenhum é injectado: fail-closed total
// para MAJOR (nunca aprovado), retrocompatíveis passam. Garante que a ausência de
// configuração NUNCA deixa passar uma quebra de contrato.
type denyMajorGate struct{}

func (denyMajorGate) Evaluate(_ context.Context, req GateRequest) Decision {
	if req.Kind != schema.ChangeMajor {
		return Decision{Allowed: true, Reason: "mudanca retrocompativel: nao exige gate"}
	}
	return Decision{Allowed: false, Reason: "sem gate configurado: MAJOR recusada (fail-closed)"}
}
