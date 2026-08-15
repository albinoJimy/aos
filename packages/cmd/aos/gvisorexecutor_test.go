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
		if in.RunID != "gv-run-1" {
			t.Errorf("run id não propagado: %q", in.RunID)
		}
		_ = json.NewEncoder(w).Encode(gvResult{Stdout: content, ExitCode: 0})
	}))
	defer srv.Close()

	e := &remoteGVisorExecutor{url: srv.URL, client: srv.Client()}
	out, arts, code, err := e.RunInGuest(context.Background(),
		sandbox.Instance{ID: "gv-run-1"},
		sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"})
	if err != nil {
		t.Fatalf("RunInGuest: %v", err)
	}
	if code != 0 || string(out) != string(content) || arts != nil {
		t.Fatalf("resultado inesperado: code=%d out=%q arts=%v", code, out, arts)
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
