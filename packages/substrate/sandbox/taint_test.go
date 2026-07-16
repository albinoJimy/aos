package sandbox

import (
	"context"
	"testing"
)

// TestTaint_EveryResultUntrusted prova o critério: todo o resultado devolvido é
// untrusted — imposto pelo TIPO. Mesmo um ExecResult{} zero é untrusted; não há
// construtor que produza trusted (prepara AOS-069).
func TestTaint_EveryResultUntrusted(t *testing.T) {
	// 1) Zero-value.
	if (ExecResult{}).Taint() != TaintUntrusted || !(ExecResult{}).IsUntrusted() {
		t.Fatal("ExecResult zero nao e untrusted")
	}
	// 2) Construtor do núcleo.
	if newResult([]byte("x"), nil, 0).Taint() != TaintUntrusted {
		t.Fatal("newResult nao e untrusted")
	}
	// 3) Resultado real via todos os drivers.
	drivers := []SandboxDriver{
		NewFakeDriver(),
		NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})),
		NewGVisorDriver(WithGVisorExecutor(echoExecutor{})),
	}
	for _, d := range drivers {
		launcher, err := NewLauncher(d)
		if err != nil {
			t.Fatalf("NewLauncher(%q): %v", d.Kind(), err)
		}
		res, err := launcher.run(context.Background(), ExecRequest{RunID: "r", StepID: "s", Call: ToolCall{Command: "ok"}})
		if err != nil {
			t.Fatalf("run(%q): %v", d.Kind(), err)
		}
		if res.Taint() != TaintUntrusted || !res.IsUntrusted() {
			t.Fatalf("driver %q: resultado nao untrusted", d.Kind())
		}
	}
}

// TestTaint_DecodedResultUntrusted prova que o resultado que atravessa a fronteira
// RM (serializado/descodificado) permanece untrusted — o taint é reimposto na
// descodificação, não vem do fio.
func TestTaint_DecodedResultUntrusted(t *testing.T) {
	enc, err := encodeResult(newResult([]byte("out"), []Artifact{{Name: "a", Data: []byte("d")}}, 0))
	if err != nil {
		t.Fatalf("encodeResult: %v", err)
	}
	dec, err := decodeResult(enc)
	if err != nil {
		t.Fatalf("decodeResult: %v", err)
	}
	if dec.Taint() != TaintUntrusted || !dec.IsUntrusted() {
		t.Fatal("resultado descodificado nao e untrusted")
	}
	// Também o caminho de output vazio (deny/erro) é untrusted.
	empty, err := decodeResult(nil)
	if err != nil {
		t.Fatalf("decodeResult(nil): %v", err)
	}
	if empty.Taint() != TaintUntrusted {
		t.Fatal("resultado vazio nao e untrusted")
	}
}

// TestTaint_SpanCarriesUntrusted prova que o span execute_tool anota o taint
// untrusted e o custo, sem segredos.
func TestTaint_SpanCarriesUntrusted(t *testing.T) {
	tracer := &recordingTracer{}
	launcher, err := NewLauncher(NewFakeDriver(), WithTracer(tracer))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	if _, err := launcher.run(context.Background(), ExecRequest{RunID: "r", StepID: "s", Call: ToolCall{Command: "ok"}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	found := false
	for _, v := range tracer.attrValues() {
		if s, ok := v.(string); ok && s == string(TaintUntrusted) {
			found = true
		}
	}
	if !found {
		t.Fatal("span nao anotou taint untrusted")
	}
	// Custo por span (ADR-010): a dimensão de custo está presente (placeholder até
	// à metering de EPIC-08) — sem isto o item de DoD 'custo por span' ficaria por
	// cobrir.
	cost, ok := tracer.attr(AttrCostUSD)
	if !ok {
		t.Fatalf("span nao anotou o custo (%s ausente)", AttrCostUSD)
	}
	if _, isFloat := cost.(float64); !isFloat {
		t.Fatalf("custo do span = %T, esperado float64 (USD)", cost)
	}
}
