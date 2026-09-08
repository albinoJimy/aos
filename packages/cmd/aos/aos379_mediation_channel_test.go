package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestAOS379_MediationChannel_NodeEventStore prova, através do NÓ REAL composto por Bootstrap
// (a MESMA cadeia de produção que integration.NewSecuredRuntime monta, agora com a porta
// MediationEvents preenchida com o Event Store do nó — AOS-379/AC5), que o canal de eventos de
// mediação tool.call.* é ALCANÇÁVEL no Event Store: um run submetido pela API REAL cujo modelo
// EMITE uma tool call deixa um evento tool.call.denied NO Event Store do nó (AC6).
//
// NÃO-VACUOSO: antes de AOS-379 o Event Store não estava composto no sink de mediação (só o WORM
// via a mediação), pelo que a contagem seria ZERO — a call era mediada mas o Event Store nunca a
// registava. Sob o chain default do nó o PDP está NÃO-carregado (deny fail-closed), logo o evento
// é tool.call.denied; é exactamente essa negação, agora LEGÍVEL no Event Store, que testemunha o
// canal composto. (O caminho de PERMIT do canal é provado em packages/integration sobre o MESMO
// tipo de RM com o bundle assinado carregado — TestAOS379_MediationChannel_CountsEvents.)
func TestAOS379_MediationChannel_NodeEventStore(t *testing.T) {
	cfg := tnBaseConfig()
	model := &toolEmittingModel{inv: agentruntime.ToolInvocation{
		ToolID:     "echo",
		Capability: tnCap, // cap:doc.read — registada na classe do nó de teste
		Input:      []byte("ping"),
	}}
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	if err := node.Runtime.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register(echo) no RM do no: %v", err)
	}

	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	svc, h := newAPI(t, node)

	const runID = "run-aos379-canal"
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        runID,
		"objective":     "prova do canal de mediacao no event store",
		"principal_nhi": medAgentID,
		"credential":    tok.Compact,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oc, ok, werr := svc.Wait(waitCtx, runID)
	if werr != nil || !ok {
		t.Fatalf("run devia ter sido hospedado/concluido: ok=%v err=%v", ok, werr)
	}
	if !oc.Result.Terminated {
		t.Fatalf("run devia ter concluido, veio %+v", oc)
	}
	if model.turns < 2 {
		t.Fatalf("o modelo do nó devia ter EMITIDO a tool call (turno 1) e concluido (turno 2); turnos=%d", model.turns)
	}

	// CONTAGEM no Event Store do NÓ: a tool call mediada deixou pelo menos um tool.call.denied.
	// É a materialização que a porta MediationEvents de AOS-379 liga — hoje >=1, antes seria 0.
	events, err := node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read(%q) do Event Store do no: %v", runID, err)
	}
	denied := 0
	for _, e := range events {
		if e.Type == "tool.call.denied" {
			denied++
		}
	}
	if denied < 1 {
		t.Fatalf("tool.call.denied no Event Store do no = %d, quero >=1 — o canal de mediacao nao foi materializado no Event Store (porta MediationEvents nao ligada?)", denied)
	}
}
