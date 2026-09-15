package main

// AOS-394 — os selos de governação do Model Gateway ligados ao run e ao passo, pela cadeia REAL
// do nó: Agent Runtime real + newGatewayModelClient (NewProduction com allowlist assinada
// embebida e estágio authn real) + upstream OpenAI-compatível em httptest. O store de governação
// é passado ao wiring para que os selos se possam ler.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos394Upstream responde o wire OpenAI com uma conclusão final e usage.
func aos394Upstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// aos394Selos lê a partição de governação do board do nó.
func aos394Selos(t *testing.T, store *audit.MemStore) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()
	part := "modelgw-gov:" + defaultModelGatewayBoard
	head, err := store.Head(ctx, part)
	if err != nil {
		t.Fatalf("Head(%s): %v", part, err)
	}
	var out []audit.AuditRecord
	for i := uint64(1); i <= head; i++ {
		rec, ok, err := store.At(ctx, part, i)
		if err != nil || !ok {
			t.Fatalf("At(%s, %d): ok=%v err=%v", part, i, ok, err)
		}
		out = append(out, rec)
	}
	return out
}

// aos394Runtime compõe o runtime real sobre o cliente de modelo do nó e um Event Store em memória.
func aos394Runtime(t *testing.T, mc agentruntime.ModelClient) (*agentruntime.Runtime, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return agentruntime.New(mc, referencemonitor.New(), agentruntime.NewTurnRecorder(es)), es
}

// TestAOS394_NoGateway_SeloLigaORunEOPasso: dois runs do MESMO agente pelo mesmo cliente de modelo
// do nó deixam selos distinguíveis, cada um com o seu run e com o passo que o turn.recorded grava.
// Antes do AOS-394 os dois selos saíam com RunID e StepID vazios e só a hora os separava.
func TestAOS394_NoGateway_SeloLigaORunEOPasso(t *testing.T) {
	srv := aos394Upstream(t)
	verifier, credCtx := aos278ModelIdentity(t)
	store := audit.NewMemStore()
	mc, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, store, nil, false, nil, 0)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	rt, es := aos394Runtime(t, mc)

	runs := []string{"run-aos394-a", "run-aos394-b"}
	for _, runID := range runs {
		res, err := rt.Run(credCtx, agentruntime.Goal{
			RunID:     runID,
			Principal: referencemonitor.Principal{NHIID: "agent-1"},
			System:    "sistema",
			Objective: "um turno",
		})
		if err != nil {
			t.Fatalf("Run(%s): %v", runID, err)
		}
		if res.Turns != 1 {
			t.Fatalf("Run(%s): esperava 1 turno, veio %d", runID, res.Turns)
		}
	}

	selos := aos394Selos(t, store)
	if len(selos) != len(runs) {
		t.Fatalf("esperava %d selos (um por chamada), veio %d", len(runs), len(selos))
	}
	for i, s := range selos {
		if s.Decision != audit.DecisionAllow || s.Resource.Value != "gpt-4o" {
			t.Fatalf("selo %d = %s/%s; quero allow/gpt-4o", i+1, s.Decision, s.Resource.Value)
		}
		if s.RunID != runs[i] {
			t.Fatalf("selo %d tem RunID %q; quero %q", i+1, s.RunID, runs[i])
		}
		// O passo selado é o MESMO que o evento durável do turno grava.
		eventos, err := es.Read(context.Background(), runs[i], 0)
		if err != nil {
			t.Fatalf("es.Read(%s): %v", runs[i], err)
		}
		passo := ""
		for _, ev := range eventos {
			if ev.Type == "turn.recorded" {
				passo = ev.StepID
			}
		}
		if passo == "" || s.StepID != passo {
			t.Fatalf("selo %d tem StepID %q; o turn.recorded de %s tem %q", i+1, s.StepID, runs[i], passo)
		}
	}
}

// TestAOS394_NoGateway_DenyDaAllowlistLigaORunEOPasso: um modelo fora da allowlist é negado
// fail-closed e o deny selado identifica o run e o passo que o pediram.
func TestAOS394_NoGateway_DenyDaAllowlistLigaORunEOPasso(t *testing.T) {
	srv := aos394Upstream(t)
	verifier, credCtx := aos278ModelIdentity(t)
	store := audit.NewMemStore()
	mc, err := newGatewayModelClient(verifier, srv.URL, "modelo-fora-da-allowlist", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, store, nil, false, nil, 0)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	rt, _ := aos394Runtime(t, mc)

	const runID = "run-aos394-deny"
	if _, err := rt.Run(credCtx, agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "agent-1"},
		System:    "sistema",
		Objective: "um turno",
	}); err == nil {
		t.Fatal("um modelo fora da allowlist tinha de falhar a chamada")
	}

	selos := aos394Selos(t, store)
	if len(selos) == 0 {
		t.Fatal("o deny da allowlist tinha de ficar selado")
	}
	for _, s := range selos {
		if s.Decision != audit.DecisionDeny {
			t.Fatalf("esperava só denies, veio %s", s.Decision)
		}
		if s.RunID != runID || s.StepID != "step-000001" {
			t.Fatalf("deny selado com RunID=%q StepID=%q; quero %s/step-000001", s.RunID, s.StepID, runID)
		}
	}
}

// TestAOS394_NoGateway_ComESemRunNoCtx_Discrimina: asserção DIFERENCIAL, e é o ponto do teste.
// A mesma chamada, pelo mesmo cliente de modelo, sela a correlação quando o ctx a traz e mostra a
// ausência quando não a traz. Uma versão que só afirmasse «vazio» passaria com a correcção
// revertida — foi o defeito que a revisão adversarial apanhou nesta bateria.
func TestAOS394_NoGateway_ComESemRunNoCtx_Discrimina(t *testing.T) {
	srv := aos394Upstream(t)
	verifier, credCtx := aos278ModelIdentity(t)
	store := audit.NewMemStore()
	mc, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, store, nil, false, nil, 0)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	view := agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}

	// (1) COM correlação no ctx — como o runtime a escreve em cada turno.
	comCtx := agentruntime.ContextWithModelCall(credCtx, "run-aos394-ctx", "step-000042")
	if _, err := mc.Call(comCtx, view); err != nil {
		t.Fatalf("Call com correlacao: %v", err)
	}
	// (2) SEM correlação — uma chamada fora do loop do agente.
	if _, err := mc.Call(credCtx, view); err != nil {
		t.Fatalf("Call sem correlacao: %v", err)
	}

	selos := aos394Selos(t, store)
	if len(selos) != 2 {
		t.Fatalf("esperava 2 selos (uma chamada com correlacao e uma sem), veio %d", len(selos))
	}
	if selos[0].RunID != "run-aos394-ctx" || selos[0].StepID != "step-000042" {
		t.Fatalf("a chamada COM correlacao tinha de selar run e passo; veio RunID=%q StepID=%q", selos[0].RunID, selos[0].StepID)
	}
	if selos[1].RunID != "" || selos[1].StepID != "" {
		t.Fatalf("a chamada SEM correlacao tinha de mostrar a ausência; veio RunID=%q StepID=%q", selos[1].RunID, selos[1].StepID)
	}
	for i, s := range selos {
		if s.Decision != audit.DecisionAllow {
			t.Fatalf("selo %d = %s; quero allow (a governação não depende da correlação)", i+1, s.Decision)
		}
	}
}
