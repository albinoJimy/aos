package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/substrate/sandbox"
)

// TestRemoteFirecrackerExecutor_Sucesso: o executor do nó envia a tool call ao orchestrator e
// devolve o stdout/exit. Prova o lado CLIENTE (stdlib) do seam — o lado microVM é provado pelo
// round-trip real do componente firecracker.
func TestRemoteFirecrackerExecutor_Sucesso(t *testing.T) {
	content := []byte("Reuniao 3a: rever o plano de migracao. Owner: alice.")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in fcExecInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decode: %v", err)
		}
		if in.Call.Command != "read" || in.Call.Path != "notes" {
			t.Errorf("call inesperada: %+v", in.Call)
		}
		if in.RunID != "fc-run-1" {
			t.Errorf("run id não propagado: %q", in.RunID)
		}
		_ = json.NewEncoder(w).Encode(fcResult{Stdout: content, ExitCode: 0})
	}))
	defer srv.Close()

	e := &remoteFirecrackerExecutor{url: srv.URL, client: srv.Client()}
	out, arts, code, err := e.RunInGuest(context.Background(),
		sandbox.Instance{ID: "fc-run-1"},
		sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"})
	if err != nil {
		t.Fatalf("RunInGuest: %v", err)
	}
	if code != 0 || string(out) != string(content) || arts != nil {
		t.Fatalf("resultado inesperado: code=%d out=%q arts=%v", code, out, arts)
	}
}

// TestRemoteFirecrackerExecutor_PropagaErroDoGuest: um erro reportado pelo guest (r.Error)
// propaga-se como erro — o FirecrackerDriver materializa-o (o efeito falhou apesar do permit).
func TestRemoteFirecrackerExecutor_PropagaErroDoGuest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(fcResult{ExitCode: 1, Error: "path foge da raiz semeada"})
	}))
	defer srv.Close()

	e := &remoteFirecrackerExecutor{url: srv.URL, client: srv.Client()}
	_, _, code, err := e.RunInGuest(context.Background(), sandbox.Instance{ID: "x"}, sandbox.ToolCall{Command: "read"})
	if err == nil {
		t.Fatal("erro do guest devia propagar")
	}
	if code != 1 {
		t.Fatalf("exit code esperado 1, veio %d", code)
	}
}

// TestRemoteFirecrackerExecutor_HTTPNao200: uma falha de transporte/estado não-200 é erro.
func TestRemoteFirecrackerExecutor_HTTPNao200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "indisponível", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	e := &remoteFirecrackerExecutor{url: srv.URL, client: srv.Client()}
	if _, _, _, err := e.RunInGuest(context.Background(), sandbox.Instance{ID: "x"}, sandbox.ToolCall{Command: "read"}); err == nil {
		t.Fatal("estado HTTP não-200 devia ser erro")
	}
}

// TestBuildSandboxDriver_Firecracker: com AOS_SANDBOX_FIRECRACKER_URL constrói o driver
// firecracker (executor injectado); sem a URL, o fake continua a ser o default.
func TestBuildSandboxDriver_Firecracker(t *testing.T) {
	t.Setenv("AOS_SANDBOX_FIRECRACKER_URL", "http://firecracker:9100/exec")
	d, err := buildSandboxDriver(sandbox.DriverFirecracker)
	if err != nil {
		t.Fatalf("buildSandboxDriver(firecracker): %v", err)
	}
	if d.Kind() != sandbox.DriverFirecracker {
		t.Fatalf("kind=%v, esperava firecracker", d.Kind())
	}

	// Default continua fake (sem env de driver).
	fd, err := buildSandboxDriver(sandbox.DriverFake)
	if err != nil {
		t.Fatalf("buildSandboxDriver(fake): %v", err)
	}
	if fd.Kind() != sandbox.DriverFake {
		t.Fatalf("kind=%v, esperava fake", fd.Kind())
	}
}
