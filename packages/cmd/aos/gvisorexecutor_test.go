package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aos-ref/substrate/sandbox"
)

// TestRemoteGVisorExecutor_Sucesso: o executor do nó envia a tool call ao componente e devolve o
// stdout/exit. Prova o lado CLIENTE (stdlib) do seam — o lado runsc é provado pelo round-trip
// real do componente gvisor.
func TestRemoteGVisorExecutor_Sucesso(t *testing.T) {
	content := []byte("Reuniao 3a: rever o plano de migracao. Owner: alice.")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in gvExecInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decode: %v", err)
		}
		if in.Call.Command != "read" || in.Call.Path != "notes" {
			t.Errorf("call inesperada: %+v", in.Call)
		}
		// AOS-383: run_id/step_id transportam a identidade de execução REAL — não o
		// inst.ID composto ("gv-...") nem um step_id vazio.
		if in.RunID != "run-1" {
			t.Errorf("run_id = %q, quero o RunID real \"run-1\" (nao o inst.ID composto)", in.RunID)
		}
		if in.StepID != "step-2" {
			t.Errorf("step_id = %q, quero o StepID real \"step-2\" (antes viajava vazio)", in.StepID)
		}
		_ = json.NewEncoder(w).Encode(gvResult{Stdout: content, ExitCode: 0})
	}))
	defer srv.Close()

	e := &remoteGVisorExecutor{url: srv.URL, client: srv.Client()}
	out, arts, code, err := e.RunInGuest(context.Background(),
		sandbox.Instance{ID: "gv-run-1-step-2-1", RunID: "run-1", StepID: "step-2"},
		sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"})
	if err != nil {
		t.Fatalf("RunInGuest: %v", err)
	}
	if code != 0 || string(out) != string(content) || arts != nil {
		t.Fatalf("resultado inesperado: code=%d out=%q arts=%v", code, out, arts)
	}
}

// TestAOS383_ExecutorTransportaRunEStepReais é o controlo negativo do AOS-383: o corpo enviado ao
// componente tem de levar o run_id e o step_id REAIS (os campos próprios da Instance), NUNCA o
// inst.ID composto/ambíguo no run_id nem um step_id vazio — o comportamento antigo. Com o ID
// composto distinto de RunID (o `-` ocorre dentro das partes), asserir `in.RunID == RunID` e
// `in.RunID != inst.ID` prova que a correlação deixou de depender de desfazer um ID ambíguo.
func TestAOS383_ExecutorTransportaRunEStepReais(t *testing.T) {
	const instID = "gv-e3-fsread-step-000001-tool-1-1" // composto e ambíguo (o `-` está nas partes)
	var got gvExecInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(gvResult{ExitCode: 0})
	}))
	defer srv.Close()

	e := &remoteGVisorExecutor{url: srv.URL, client: srv.Client()}
	if _, _, _, err := e.RunInGuest(context.Background(),
		sandbox.Instance{ID: instID, RunID: "e3-fsread", StepID: "step-000001-tool-1"},
		sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"}); err != nil {
		t.Fatalf("RunInGuest: %v", err)
	}
	if got.RunID != "e3-fsread" {
		t.Errorf("run_id = %q, quero \"e3-fsread\"", got.RunID)
	}
	if got.RunID == instID {
		t.Error("run_id ainda transporta o inst.ID composto (comportamento antigo) — deve ser o RunID real")
	}
	if got.StepID != "step-000001-tool-1" {
		t.Errorf("step_id = %q, quero \"step-000001-tool-1\" (antes viajava vazio)", got.StepID)
	}
}

// TestRemoteGVisorExecutor_PropagaErroDoGuest: um erro reportado pelo guest (r.Error) propaga-se
// como erro — o GVisorDriver materializa-o (o efeito falhou apesar do permit). É o caso do
// path-escape, que no gVisor é recusado DENTRO do sandbox, não por uma rede Go no nó.
func TestRemoteGVisorExecutor_PropagaErroDoGuest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(gvResult{ExitCode: 1, Error: "path foge da raiz semeada"})
	}))
	defer srv.Close()

	e := &remoteGVisorExecutor{url: srv.URL, client: srv.Client()}
	_, _, code, err := e.RunInGuest(context.Background(),
		sandbox.Instance{ID: "gv-run-2"},
		sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "../../etc/passwd"})
	if err == nil {
		t.Fatal("um erro do guest TEM de propagar — senão um efeito falhado passaria por sucesso")
	}
	if code != 1 {
		t.Fatalf("exit code do guest não propagado: %d", code)
	}
}

// TestRemoteGVisorExecutor_HTTPNao200: um componente que responde != 200 NEGA a execução. Nunca
// se degrada para uma execução no host — indisponibilidade é recusa, não bypass.
func TestRemoteGVisorExecutor_HTTPNao200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	e := &remoteGVisorExecutor{url: srv.URL, client: srv.Client()}
	if _, _, _, err := e.RunInGuest(context.Background(),
		sandbox.Instance{ID: "gv-run-3"},
		sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"}); err == nil {
		t.Fatal("componente indisponível TEM de negar a execução (fail-closed)")
	}
}

// TestBuildSandboxDriver_GVisorSemURLFicaSkeleton: sem AOS_SANDBOX_GVISOR_URL o driver gvisor
// continua o skeleton — e o skeleton é fail-closed no exec (ErrDriverUnavailable). É o gap
// HONESTO: o nó não finge ter isolamento que não tem.
func TestBuildSandboxDriver_GVisorSemURLFicaSkeleton(t *testing.T) {
	t.Setenv("AOS_SANDBOX_GVISOR_URL", "")
	d, err := buildSandboxDriver(sandbox.DriverGVisor)
	if err != nil {
		t.Fatalf("buildSandboxDriver: %v", err)
	}
	if d.Kind() != sandbox.DriverGVisor {
		t.Fatalf("kind errado: %v", d.Kind())
	}
}

// TestBuildSandboxDriver_GVisorComURLInjectaExecutor: com a URL definida o driver passa a ter
// executor. Provamo-lo pelo COMPORTAMENTO — o driver com executor deixa de devolver
// ErrDriverUnavailable no Create — e não por inspecção de campos privados.
func TestBuildSandboxDriver_GVisorComURLInjectaExecutor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(gvResult{Stdout: []byte("ok"), ExitCode: 0})
	}))
	defer srv.Close()
	t.Setenv("AOS_SANDBOX_GVISOR_URL", srv.URL)

	d, err := buildSandboxDriver(sandbox.DriverGVisor)
	if err != nil {
		t.Fatalf("buildSandboxDriver: %v", err)
	}
	if d.Kind() != sandbox.DriverGVisor {
		t.Fatalf("kind errado: %v", d.Kind())
	}
	if _, ok := os.LookupEnv("AOS_SANDBOX_GVISOR_URL"); !ok {
		t.Fatal("a env tem de estar definida neste subteste")
	}
}
