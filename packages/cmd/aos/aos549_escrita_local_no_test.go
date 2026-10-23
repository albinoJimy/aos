package main

// AOS-549 — PONTA A PONTA PELO NÓ COMPOSTO: com o gate de taint ARMADO (cap:fs.read privilegiada) e
// a política 1.1.0, o modelo lê um documento (o contexto fica untrusted) e depois pede a
// `artifact_write`. A call passa pelo RM inteiro — reescrita do efeito, identidade, política
// (`allow_artifact_write`), taint (`local_run_write`) — e o artefacto fica gravado, cifrado, com o
// evento de proveniência no stream do run.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

const aos549SpecArtefacto = `{"name":"artifact_write","description":"grava um ficheiro","capability":"cap:artifact.write",` +
	`"resource_type":"file","resource_value":"artifact://{name}","resource_region":"eu",` +
	`"egress":"none","reversibility":"reversible","mutation":"run_artifact"}`

func TestAOS549_No_EscreveDepoisDeLerPeloRMInteiro(t *testing.T) {
	ordem, extra, specs, caps := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486SpecsExtra, aos486CapsExtra
	t.Cleanup(func() {
		aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486SpecsExtra, aos486CapsExtra = ordem, extra, specs, caps
	})
	aos486OrdemDoFicheiro = []string{"doc_read"}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	aos486SpecsExtra = map[string]string{artifactWriteToolName: aos549SpecArtefacto}
	aos486CapsExtra = []string{artifactWriteCapability}
	pasta := t.TempDir()
	n := aos486Compor(t, func(cfg *Config) {
		cfg.ArtifactDir, cfg.ArtifactMaxBytes = pasta, 1<<30
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)
	})
	if n.node.artefactos == nil {
		t.Fatal("o armazem nao foi composto")
	}
	var pedidos atomic.Int64
	resposta := func(tool, args string) []byte {
		return []byte(`{"id":"c","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-` + tool + `","type":"function","function":{"name":"` + tool + `","arguments":` + args + `}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	n.upstream.mu.Lock()
	n.upstream.pede = "x"
	n.upstream.responde = func(bool, string) []byte {
		switch pedidos.Add(1) {
		case 1:
			return resposta("doc_read", `"{\"doc_id\":\"notes\"}"`)
		case 2:
			return resposta(artifactWriteToolName, `"{\"name\":\"comodos.json\",\"media_type\":\"application/json\",\"content\":\"{\\\"comodos\\\":[\\\"SALA\\\",\\\"WC\\\"]}\"}"`)
		default:
			return []byte(`{"id":"f","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
		}
	}
	n.upstream.mu.Unlock()
	runID := "run-aos549-no"
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Le o documento notes e grava a lista dos comodos",
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"doc_read", artifactWriteToolName}, "max_turns": 4,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	oc, ok, werr := n.svc.Wait(wctx, runID)
	if werr != nil || !ok || oc.Err != nil {
		t.Fatalf("run: ok=%v err=%v oc=%v\n%s", ok, werr, oc.Err, n.log.String())
	}
	eventos, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// A mediação da artifact_write: permitida, com o contexto untrusted e a razão local_run_write.
	var mediada bool
	for _, e := range eventos {
		if e.Type != "tool.call.mediated" {
			continue
		}
		var p struct {
			Decision string `json:"decision"`
			ToolID   string `json:"tool_id"`
			Context  struct {
				Taint          string `json:"taint"`
				TaintPrivilege string `json:"taint_privilege"`
			} `json:"context"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.ToolID == artifactWriteToolName {
			mediada = true
			if p.Decision != "permit" || p.Context.Taint != "untrusted" || p.Context.TaintPrivilege != referencemonitor.TaintPrivilegeLocalRunWrite {
				t.Fatalf("mediacao da artifact_write: %+v", p)
			}
		}
	}
	if !mediada {
		t.Fatalf("a artifact_write nao foi mediada\n%s", n.log.String())
	}
	arts := artefactosDoRun(eventos)
	if len(arts) != 1 || arts[0].Name != "comodos.json" || arts[0].StepID == "" || arts[0].SealedSubject == "" {
		t.Fatalf("proveniencia: %+v", arts)
	}
	claro, err := n.node.artefactos.abrir(context.Background(), arts[0].SealedSubject, runID, arts[0].Digest)
	if err != nil || !strings.Contains(string(claro), "SALA") {
		t.Fatalf("artefacto: %q %v", claro, err)
	}
	if !strings.Contains(n.log.String(), "livres por escrita local do run: artifact_write") {
		t.Fatalf("o banner do gate nao nomeia a escrita local do run:\n%s", n.log.String())
	}
}
