package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/sandbox"
)

// writeToolsEnv escreve um registry AOS_MODEL_TOOLS temporário e aponta a env para ele.
func writeToolsEnv(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("escrever tools.json: %v", err)
	}
	t.Setenv("AOS_MODEL_TOOLS", p)
}

// TestSandboxBindingsFromEnv_ParseESelecao: só tools com bloco `sandbox` viram binding; o
// Command/slots são lidos do registry (trusted).
func TestSandboxBindingsFromEnv_ParseESelecao(t *testing.T) {
	writeToolsEnv(t, `[
	  {"name":"doc_read","capability":"cap:fs.read","sandbox":{"command":"read","path_arg":"doc_id"}},
	  {"name":"web_get","capability":"cap:http.get"}
	]`)
	b, err := sandboxBindingsFromEnv()
	if err != nil {
		t.Fatalf("sandboxBindingsFromEnv: %v", err)
	}
	if len(b) != 1 {
		t.Fatalf("esperava 1 binding (só doc_read tem bloco sandbox), veio %d", len(b))
	}
	got, ok := b["doc_read"]
	if !ok {
		t.Fatal("binding de doc_read em falta")
	}
	if got.Command != "read" || got.PathArg != "doc_id" {
		t.Fatalf("binding inesperado: %+v", got)
	}
	if _, ok := b["web_get"]; ok {
		t.Fatal("web_get não declara `sandbox` — não devia ter binding")
	}
}

// TestSandboxBindingsFromEnv_FailClosedSemCommand: um bloco `sandbox` sem `command` aborta
// (o Command é FIXO e trusted; não pode faltar).
func TestSandboxBindingsFromEnv_FailClosedSemCommand(t *testing.T) {
	writeToolsEnv(t, `[{"name":"doc_read","capability":"cap:fs.read","sandbox":{"path_arg":"doc_id"}}]`)
	if _, err := sandboxBindingsFromEnv(); err == nil {
		t.Fatal("bloco sandbox sem command devia falhar (fail-closed)")
	}
}

// TestNewSandboxEffectRewriter_TransformaArgsParaExecRequest: o coração do adaptador no nó —
// os args opacos do modelo viram um ExecRequest com o RunID/StepID REAIS da Call, e o Command
// vem da binding (não dos args). Uma tool sem binding passa inalterada.
func TestNewSandboxEffectRewriter_TransformaArgsParaExecRequest(t *testing.T) {
	rw := newSandboxEffectRewriter(map[string]sandbox.SandboxBinding{
		"doc_read": {Command: "read", PathArg: "doc_id"},
	})
	if rw == nil {
		t.Fatal("rewriter nil apesar de haver bindings")
	}

	out, err := rw(referencemonitor.Call{
		RunID: "run-x", StepID: "run-x-tool-1", ToolID: "doc_read",
		Input: []byte(`{"doc_id":"notes"}`),
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	var req sandbox.ExecRequest
	if err := json.Unmarshal(out.Input, &req); err != nil {
		t.Fatalf("Call.Input não é um ExecRequest: %v", err)
	}
	if req.RunID != "run-x" || req.StepID != "run-x-tool-1" {
		t.Fatalf("RunID/StepID reais não propagados: %+v", req)
	}
	if req.Call.ToolID != "doc_read" || req.Call.Command != "read" || req.Call.Path != "notes" {
		t.Fatalf("ToolCall inesperado (Command devia vir da binding): %+v", req.Call)
	}

	// Tool SEM binding: passthrough (Input opaco inalterado).
	pass, err := rw(referencemonitor.Call{ToolID: "web_get", Input: []byte(`{"url":"x"}`)})
	if err != nil {
		t.Fatalf("passthrough: %v", err)
	}
	if string(pass.Input) != `{"url":"x"}` {
		t.Fatalf("passthrough alterou o Input: %s", pass.Input)
	}
}

// TestNewSandboxEffectRewriter_FailClosedArgsMaus: um arg nomeado em falta → erro (que o
// rewritingDispatcher materializa como Deny, sem efeito).
func TestNewSandboxEffectRewriter_FailClosedArgsMaus(t *testing.T) {
	rw := newSandboxEffectRewriter(map[string]sandbox.SandboxBinding{
		"doc_read": {Command: "read", PathArg: "doc_id"},
	})
	if _, err := rw(referencemonitor.Call{ToolID: "doc_read", Input: []byte(`{"outro":"x"}`)}); err == nil {
		t.Fatal("args sem doc_id deviam falhar (fail-closed → Deny)")
	}
}

// TestNewSandboxEffectRewriter_NilSemBindings: sem bindings o rewriter é nil ⇒ SecuredConfig
// mantém o comportamento byte-idêntico (sem reescrita).
func TestNewSandboxEffectRewriter_NilSemBindings(t *testing.T) {
	if newSandboxEffectRewriter(nil) != nil {
		t.Fatal("sem bindings o rewriter deve ser nil")
	}
}
