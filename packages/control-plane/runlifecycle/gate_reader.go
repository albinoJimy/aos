package runlifecycle

import (
	"context"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// gate_reader.go — A PORTA [plandispatch.Gate] DERIVADA do log (AOS-390, ADR-024).
//
// O despacho é a jusante do gate: nenhum nó despacha até o plano estar materializado
// (`plan.materialized` apenso). Esta é a única autoridade que o despachante consulta
// sobre isso, e — como todas as portas de `readers.go` — é de LEITURA: deriva o facto
// do stream do plano, nunca o escreve. Preserva a fronteira ADR-018 (o SCH observa o
// gate, não o aprova).
//
// Antes de AOS-390 não existia implementação de produção desta porta — só duplos de
// teste (`gateAberto`, `fakeGate`). Um despachante composto em produção precisa de a
// derivar do mesmo log que tudo o resto deriva.

// GateReader é a fonte da porta [plandispatch.Gate]: reporta se `plan.materialized`
// foi apenso ao stream do plano. Amarrado a UM plan_id na construção; não escreve nada.
type GateReader struct {
	store  EventStore
	planID string
}

// NewGateReader amarra um leitor de gate ao stream de um plano. store e plan_id são
// obrigatórios (fail-closed).
func NewGateReader(store EventStore, planID string) (*GateReader, error) {
	if store == nil {
		return nil, ErrDeps
	}
	if planID == "" {
		return nil, fmt.Errorf("%w: plan_id", ErrEmptyRunID)
	}
	return &GateReader{store: store, planID: planID}, nil
}

// Snapshot relê o stream do plano UMA vez e fixa se o plano já foi materializado — o
// retrato imutável sobre o qual uma passagem de despacho inteira corre (mesma
// disciplina de coerência dos outros readers: relê-se uma vez, não um mosaico).
func (r *GateReader) Snapshot(ctx context.Context) (*GateSnapshot, error) {
	events, err := readStream(ctx, r.store, r.planID)
	if err != nil {
		return nil, fmt.Errorf("runlifecycle: retrato do gate do plano %q: %w", r.planID, err)
	}
	materialized := false
	for i := range events {
		if events[i].Type == plannerevents.EventMaterialized {
			materialized = true
			break
		}
	}
	return &GateSnapshot{planID: r.planID, materialized: materialized}, nil
}

// GateSnapshot é o retrato IMUTÁVEL do estado de materialização de um plano.
// Satisfaz [plandispatch.Gate].
type GateSnapshot struct {
	planID       string
	materialized bool
}

var _ plandispatch.Gate = (*GateSnapshot)(nil)

// Materialized reporta se `plan.materialized` foi apenso ao plano amarrado. Fail-closed:
// um plano diferente do amarrado é um erro ([ErrForeignPlan]), nunca um false silencioso
// (que faria um plano materializado noutro stream parecer por-materializar).
func (s *GateSnapshot) Materialized(_ context.Context, planID string) (bool, error) {
	if planID != s.planID {
		return false, fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, s.planID)
	}
	return s.materialized, nil
}
