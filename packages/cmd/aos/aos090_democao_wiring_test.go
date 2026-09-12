package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/agent-runtime/breaker"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos090_democao_wiring_test.go — AOS-090/DEF-908, Fase A: o TRIP do disjuntor demove a
// CLASSE dos pares que o run tocou, com a classe derivada do Event Store (tool.call.mediated),
// não do WORM. Prova que a metade de segurança está LIGADA e é class-aware.

// esFake é um [eventstore.EventStore] mínimo: Read devolve os eventos programados por stream.
type esFake struct {
	porStream map[string][]eventstore.Event
	readErr   error
}

func (f *esFake) Read(_ context.Context, streamID string, _ uint64) ([]eventstore.Event, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.porStream[streamID], nil
}
func (f *esFake) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, nil
}
func (f *esFake) Subscribe(context.Context, eventstore.Filter, eventstore.Handler) (eventstore.Subscription, error) {
	return nil, nil
}
func (f *esFake) Close() error { return nil }

// eventoMediado constrói um evento tool.call.mediated com a classe e a capability dadas, no
// mesmo formato JSON que o RM sela (reference-monitor/eventsink.go).
func eventoMediado(t *testing.T, classe, capability, resource string) eventstore.Event {
	t.Helper()
	payload := map[string]any{
		"capability": capability,
		"resource":   map[string]any{"value": resource},
		"principal":  map[string]any{"agent_class": classe},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return eventstore.Event{Type: referencemonitor.EventTypeMediated, Payload: raw}
}

// controladorDeTeste compõe um Controller sobre um registo selado num MemStore, com uma classe
// já promovida a `nivel` no domínio `dominio`.
func controladorDeTeste(t *testing.T, classe, dominio string, nivel autonomy.Level) (*autonomy.Controller, *autonomy.LevelRegistry) {
	t.Helper()
	store := audit.NewMemStore()
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(autonomy.NewAuditSink(store, "")))
	if _, err := reg.SetLevel(context.Background(), autonomy.ClassPrefix+classe, dominio, nivel, "promocao de teste", "gov-admin"); err != nil {
		t.Fatalf("promover a classe: %v", err)
	}
	ctrl, err := autonomy.NewController(reg, nil, autonomy.DefaultAutonomyControlConfig())
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return ctrl, reg
}

// TestAOS090_Wiring_TripDemoveAClasse — o caminho feliz: um TRIP de um run cuja mediação tem
// classe worker a L4 demove a CLASSE worker:fs para L2 (salto 2, piso L1). O efectivo que o PDP
// leria para uma instância baixa com ela.
func TestAOS090_Wiring_TripDemoveAClasse(t *testing.T) {
	ctrl, reg := controladorDeTeste(t, "worker", "fs", autonomy.L4)
	es := &esFake{porStream: map[string][]eventstore.Event{
		"run-1": {eventoMediado(t, "worker", "cap:fs.write", "/data/x")},
	}}
	r := novasAnomaliasDeAutonomia(ctrl, es, func(string, ...any) {})

	r.despromover(breaker.Alert{RunID: "run-1", Kind: breaker.AlertTrip, Signal: breaker.SignalNoProgress})

	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L2 {
		t.Fatalf("classe worker:fs = %s apos trip; quer L2 (L4 - salto 2)", got)
	}
	// O efectivo class-aware de uma instância da classe também desceu.
	if got := reg.LevelForAgentOrClass("worker-run-1", "worker", "fs"); got != autonomy.L2 {
		t.Fatalf("efectivo via classe = %s; quer L2", got)
	}
}

// TestAOS090_Wiring_SemClasseNaoDemove — uma mediação sem agent_class não dá classe a demover;
// o nível mantém-se e nada rebenta (é preferível não demover a demover a classe errada).
func TestAOS090_Wiring_SemClasseNaoDemove(t *testing.T) {
	ctrl, reg := controladorDeTeste(t, "worker", "fs", autonomy.L4)
	es := &esFake{porStream: map[string][]eventstore.Event{
		"run-2": {eventoMediado(t, "", "cap:fs.write", "/data/x")}, // sem classe
	}}
	r := novasAnomaliasDeAutonomia(ctrl, es, func(string, ...any) {})

	r.despromover(breaker.Alert{RunID: "run-2", Kind: breaker.AlertTrip})

	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L4 {
		t.Fatalf("sem classe na mediação a demoção não devia mexer; classe=%s (quer L4)", got)
	}
}

// TestAOS090_Wiring_SoTripAutomatico — AlertEscalate/AlertAbort são acções MANUAIS de um
// operador; não demovem (escalar não pode custar autonomia). Só o AlertTrip enfileira.
func TestAOS090_Wiring_SoTripAutomatico(t *testing.T) {
	ctrl, reg := controladorDeTeste(t, "worker", "fs", autonomy.L4)
	es := &esFake{porStream: map[string][]eventstore.Event{
		"run-3": {eventoMediado(t, "worker", "cap:fs.write", "/data/x")},
	}}
	r := novasAnomaliasDeAutonomia(ctrl, es, func(string, ...any) {})

	r.Alert(context.Background(), breaker.Alert{RunID: "run-3", Kind: breaker.AlertEscalate})
	r.Alert(context.Background(), breaker.Alert{RunID: "run-3", Kind: breaker.AlertAbort})
	// Drena o que houver (não devia haver nada) e verifica que a classe não mexeu.
	drenar(r)
	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L4 {
		t.Fatalf("escalada/abort não deviam demover; classe=%s (quer L4)", got)
	}

	// Um TRIP, esse, demove.
	r.Alert(context.Background(), breaker.Alert{RunID: "run-3", Kind: breaker.AlertTrip})
	drenar(r)
	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L2 {
		t.Fatalf("o TRIP devia demover para L2; classe=%s", got)
	}
}

// drenar corre o encaminhador até esvaziar a fila e sair (fecha o stop e deixa o drain do
// [autonomiaAnomalias.correr] processar o que ficou).
func drenar(r *autonomiaAnomalias) {
	stop := make(chan struct{})
	close(stop)
	r.correr(stop)
}
