package activity_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-485 — a lista-branca do run atravessa a Activity até ao Call que o RM medeia, e uma call
// fora da lista não entra no step-ledger.

// listaRecorder guarda a lista-branca de cada Call que o RM recebeu, e permite.
type listaRecorder struct {
	mu     sync.Mutex
	listas [][]string
}

func (h *listaRecorder) Name() string { return "lista-rec" }
func (h *listaRecorder) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	h.mu.Lock()
	h.listas = append(h.listas, call.AllowedTools)
	h.mu.Unlock()
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// TestAOS485_ListaChegaAoCallTalComoFoiDespachada mede a tradução Activity → Call neste pacote:
// nil chega nil, vazia chega vazia (e não nil), e uma lista chega igual. Sem isto só o módulo
// `integration` apanhava uma `toCall` que perdesse o campo.
func TestAOS485_ListaChegaAoCallTalComoFoiDespachada(t *testing.T) {
	casos := map[string][]string{"nil": nil, "vazia": {}, "com a tool": {testTool}}
	for nome, lista := range casos {
		t.Run(nome, func(t *testing.T) {
			store, err := eventstore.New()
			if err != nil {
				t.Fatalf("eventstore.New: %v", err)
			}
			defer store.Close()
			rec := &listaRecorder{}
			rm := referencemonitor.New(referencemonitor.WithHooks(rec), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
			if err := rm.Register(testTool, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
				t.Fatalf("Register: %v", err)
			}
			ledger, err := durable.NewStepLedger(store)
			if err != nil {
				t.Fatalf("NewStepLedger: %v", err)
			}
			d, err := activity.NewDispatcher(rm, ledger)
			if err != nil {
				t.Fatalf("NewDispatcher: %v", err)
			}
			act := baseActivity()
			act.AllowedTools = lista
			// O erro de negação (lista vazia) é esperado; o que se mede é o que o RM recebeu.
			if _, err := d.Dispatch(context.Background(), act); err != nil && !errors.Is(err, activity.ErrMediationDenied) {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(rec.listas) != 1 {
				t.Fatalf("esperava 1 mediação, vieram %d", len(rec.listas))
			}
			// DeepEqual distingue nil de vazia — é a distinção que interessa.
			if !reflect.DeepEqual(rec.listas[0], lista) {
				t.Fatalf("a lista %#v chegou ao RM como %#v", lista, rec.listas[0])
			}
		})
	}
}

// TestAOS485_ForaDaListaNaoRecebeOResultadoMemorizado reproduz a sonda da revisão: o passo foi
// aplicado SEM lista (permit, output memorizado no ledger) e é despachado de novo, a mesma chave
// e a mesma acção, com a lista VAZIA. O already-applied do ledger corre antes da mediação e a
// impressão da acção não tem a lista: sem a guarda, o segundo despacho devolvia o output
// memorizado como permit, sem passar pelo RM.
func TestAOS485_ForaDaListaNaoRecebeOResultadoMemorizado(t *testing.T) {
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()

	primeira, err := d.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch #1 (sem lista): %v", err)
	}
	if string(primeira.Output.Value) != "out:payload" {
		t.Fatalf("preparação: o primeiro despacho tinha de memorizar o output, veio %q", primeira.Output.Value)
	}

	segunda := baseActivity()
	segunda.AllowedTools = []string{}
	res, err := d.Dispatch(ctx, segunda)
	if !errors.Is(err, activity.ErrMediationDenied) {
		t.Fatalf("o segundo despacho, com a lista vazia, tinha de ser negado; veio res=%+v err=%v", res, err)
	}
	var md *activity.MediationDenial
	if !errors.As(err, &md) || md.Code != referencemonitor.CodeToolOutsideRunAllowlist || md.DeniedBy != referencemonitor.RunAllowlistHookName {
		t.Fatalf("a recusa tinha de vir do RM, pela lista: %v", err)
	}
	if len(res.Output.Value) != 0 || res.Deduplicated {
		t.Fatalf("o output memorizado chegou ao chamador: %+v", res)
	}
	if got := h.spy.calls.Load(); got != 1 {
		t.Fatalf("o efeito tinha de ter corrido só no primeiro despacho, correu %d", got)
	}
	if permits, denials, _ := h.rm.Metrics().Snapshot(); permits != 1 || denials != 1 {
		t.Fatalf("contadores do RM: permits=%d denials=%d, quero 1 e 1 — a recusa tem de ser mediada", permits, denials)
	}

	// CONTROLO: com a tool NA lista, a mesma chave deduplica como sempre.
	terceira := baseActivity()
	terceira.AllowedTools = []string{testTool}
	res3, err := d.Dispatch(ctx, terceira)
	if err != nil || !res3.Deduplicated || string(res3.Output.Value) != "out:payload" {
		t.Fatalf("controlo: com a tool na lista o passo aplicado tinha de deduplicar: res=%+v err=%v", res3, err)
	}
}

// mediadorQuePermiteTudo é um Mediator que não impõe a lista: permite sempre.
type mediadorQuePermiteTudo struct{}

func (mediadorQuePermiteTudo) Mediate(context.Context, referencemonitor.Call) (referencemonitor.Decision, error) {
	return referencemonitor.Decision{Effect: referencemonitor.EffectPermit, Output: []byte("nao devia sair")}, nil
}

// Um Mediator que PERMITE uma call fora da lista não é obedecido: o despacho falha fechado e o
// output não sai. O RM de referência nunca chega aqui — nega por si.
func TestAOS485_MediadorQueNaoImpoeAListaFalhaFechado(t *testing.T) {
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(mediadorQuePermiteTudo{}, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	act.AllowedTools = []string{}
	res, err := d.Dispatch(context.Background(), act)
	if !errors.Is(err, activity.ErrAllowlistNotEnforced) || len(res.Output.Value) != 0 {
		t.Fatalf("queria ErrAllowlistNotEnforced e nenhum output; veio res=%+v err=%v", res, err)
	}
}
