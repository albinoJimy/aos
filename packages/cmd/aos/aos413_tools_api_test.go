package main

// AOS-413 (ADR-027) — o campo `tools` do `POST /runs` é a lista-branca do run: pelo nó real, uma
// tool call fora dela é negada e a tool não executa. Desde o AOS-485 quem nega é o Reference
// Monitor, e a recusa fica no stream do run, no contador e no WORM como qualquer outra.
//
// LIMITE deste ficheiro: a `echo` não está no catálogo assinado do nó, pelo que sem a lista
// seria negada na mesma, pela revalidação. O que aqui se prova é QUEM nega (`denied_by` da
// lista) e a pegada; que é a lista a IMPEDIR a execução prova-se em
// aos485_recusa_lista_no_composto_test.go, com a tool no catálogo e o controlo positivo.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

func TestAOS413_ToolsDoPostRunsCortaAToolForaDaLista(t *testing.T) {
	// ["outra"]: a tool pedida não está na lista. []: o nó do plano sem tools pinadas — lista
	// PRESENTE e vazia, que nega tudo (ausente seria «sem restrição»).
	for nome, tools := range map[string][]string{"fora da lista": {"outra"}, "lista vazia": {}} {
		t.Run(nome, func(t *testing.T) { aos413NegaPeloRM(t, tools) })
	}
}

func aos413NegaPeloRM(t *testing.T, tools []string) {
	t.Helper()
	cfg := tnBaseConfig()
	model := &toolEmittingModel{inv: agentruntime.ToolInvocation{
		ToolID:     "echo",
		Capability: tnCap,
		Input:      []byte("ping"),
	}}
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	executou := 0
	if err := node.Runtime.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		executou++
		return in, nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	svc, h := newAPI(t, node)
	antesP, antesD, _ := node.Runtime.Monitor().Metrics().Snapshot()

	rec := postJSON(h, "POST", "/runs", map[string]any{
		// AOS-424: o `run_id` É o nome de um stream, e o ponto não é representável num
		// subject NATS. Este id imitava um run filho de plano (`<run>~<nó>`) com um
		// ponto — e o `childRunID` passou a ESCAPAR o `node_id`, pelo que o id real
		// para um nó `n1` é este. O ponto aqui era uma invenção do teste, não do produto.
		"run_id":        "run-413~n1",
		"objective":     "o trabalho de um no do plano",
		"principal_nhi": medAgentID,
		"credential":    tok.Compact,
		"tools":         tools,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(waitCtx, "run-413~n1"); werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado: ok=%v err=%v", ok, werr)
	}
	if model.turns < 2 {
		t.Fatalf("o modelo devia ter emitido a tool call; turnos=%d", model.turns)
	}
	if executou != 0 {
		t.Fatalf("a tool fora da lista-branca do run EXECUTOU %d vez(es)", executou)
	}
	// Negada PELO RM (AOS-485): uma recusa a mais, nenhum permit a mais. Até ao AOS-485 a call
	// era cortada antes do RM e este contador não se movia — era a recusa sem pegada.
	depoisP, depoisD, _ := node.Runtime.Monitor().Metrics().Snapshot()
	if depoisD != antesD+1 || depoisP != antesP {
		t.Fatalf("a recusa pela lista-branca tinha de contar no RM: denials %d→%d (quero +1), permits %d→%d (quero igual)",
			antesD, depoisD, antesP, depoisP)
	}

	// E fica no stream do run: um `tool.call.denied` com o código e o `denied_by` da lista,
	// ligado ao turno que pediu a call — e nada que diga que algo foi autorizado, despachado
	// ou lançado em sandbox.
	events, err := node.EventStore.Read(context.Background(), "run-413~n1", 1)
	if err != nil {
		t.Fatalf("ler o stream do run: %v", err)
	}
	turno1, negados := "", 0
	for _, e := range events {
		switch {
		case e.Type == agentruntime.EventTypeTurnRecorded && turno1 == "":
			turno1 = e.StepID
		case e.Type == referencemonitor.EventTypeMediated, e.Type == referencemonitor.EventTypeOutcome, strings.HasPrefix(e.Type, "sandbox."):
			t.Fatalf("o stream do run tem um %s: nada podia ter sido autorizado nem lançado", e.Type)
		case e.Type == referencemonitor.EventTypeDenied:
			negados++
			var p struct {
				Code      string `json:"code"`
				DeniedBy  string `json:"denied_by"`
				Principal struct {
					DelegationChain []struct {
						Sub string `json:"sub"`
					} `json:"delegation_chain"`
				} `json:"principal"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("payload do tool.call.denied ilegivel: %v", err)
			}
			if p.Code != agentruntime.CodeToolOutsideRunAllowlist || p.DeniedBy != referencemonitor.RunAllowlistHookName {
				t.Fatalf("a recusa saiu por outra razão: %s", e.Payload)
			}
			if len(p.Principal.DelegationChain) == 0 || p.Principal.DelegationChain[0].Sub != "human:"+tnHuman {
				t.Fatalf("a recusa não leva a cadeia de delegação do token verificado: %s", e.Payload)
			}
			if e.ParentStepID == "" || e.ParentStepID != turno1 || e.StepID != turno1+"-tool-1" {
				t.Fatalf("step_id=%q parent_step_id=%q, quero %q e %q", e.StepID, e.ParentStepID, turno1+"-tool-1", turno1)
			}
		}
	}
	if negados != 1 {
		t.Fatalf("queria exactamente 1 tool.call.denied no stream do run, vieram %d", negados)
	}
}

func TestAOS413_ToolsComEntradaVaziaERecusado(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-413-vazio",
		"objective":     "x",
		"principal_nhi": "nhi:run-413-vazio",
		"tools":         []string{"fs.read", ""},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("uma entrada vazia em tools devia dar 400, veio %d (%s)", rec.Code, rec.Body.String())
	}
}
