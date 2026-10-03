package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// aos487Documento é a forma do documento que o `doc_read` devolve em produção: texto com
// acentos, quebras de linha e os três caracteres que o escape de HTML do JSON mexeria.
const aos487Documento = "Reunião de 15/08/2026 — nota de trabalho.\n\nDecisão: <gVisor> & runsc.\n"

// TestAOS487_StdoutDeTextoViajaComoTexto prova que o resultado serializado — o que o modelo
// lê como resultado da tool call — leva o stdout de texto legível, e não em base64.
func TestAOS487_StdoutDeTextoViajaComoTexto(t *testing.T) {
	enc, err := encodeResult(newResult([]byte(aos487Documento), nil, 0))
	if err != nil {
		t.Fatalf("encodeResult: %v", err)
	}
	if bytes.Contains(enc, []byte(base64.StdEncoding.EncodeToString([]byte(aos487Documento)))) {
		t.Fatalf("o stdout de texto saiu em base64: %s", enc)
	}
	for _, legivel := range []string{`"stdout_text":"Reunião de 15/08/2026`, "Decisão: <gVisor> & runsc."} {
		if !bytes.Contains(enc, []byte(legivel)) {
			t.Fatalf("o resultado não tem %q em texto: %s", legivel, enc)
		}
	}
	if bytes.Contains(enc, []byte(`"stdout":`)) {
		t.Fatalf("o stdout de texto não pode ir também no campo binário: %s", enc)
	}
	if bytes.HasSuffix(enc, []byte("\n")) {
		t.Fatalf("o resultado não pode acabar em \\n (o Encode acrescenta-o): %q", enc)
	}
	dec, err := decodeResult(enc)
	if err != nil {
		t.Fatalf("decodeResult: %v", err)
	}
	if string(dec.Stdout) != aos487Documento {
		t.Fatalf("ida e volta: stdout = %q, esperado %q", dec.Stdout, aos487Documento)
	}
}

// TestAOS487_StdoutBinarioContinuaEmBase64 prova que um stdout que não é UTF-8 válido não
// é mutilado: continua no campo binário (base64) e volta byte a byte.
func TestAOS487_StdoutBinarioContinuaEmBase64(t *testing.T) {
	binario := []byte{0xff, 0xfe, 0x00, 'a', 0x80}
	enc, err := encodeResult(newResult(binario, nil, 3))
	if err != nil {
		t.Fatalf("encodeResult: %v", err)
	}
	if bytes.Contains(enc, []byte(`"stdout_text"`)) {
		t.Fatalf("um stdout binário não pode ir como texto: %s", enc)
	}
	if !bytes.Contains(enc, []byte(`"stdout":"`+base64.StdEncoding.EncodeToString(binario)+`"`)) {
		t.Fatalf("o stdout binário tinha de ir em base64 no campo stdout: %s", enc)
	}
	dec, err := decodeResult(enc)
	if err != nil {
		t.Fatalf("decodeResult: %v", err)
	}
	if !bytes.Equal(dec.Stdout, binario) || dec.ExitCode != 3 {
		t.Fatalf("ida e volta: stdout=%v exit=%d", dec.Stdout, dec.ExitCode)
	}
}

// TestAOS487_ResultadoGravadoAntesLeSeIgual prova a compatibilidade: um resultado na forma
// anterior (stdout de texto em base64 no campo `stdout`) descodifica para os mesmos bytes.
func TestAOS487_ResultadoGravadoAntesLeSeIgual(t *testing.T) {
	antigo, err := json.Marshal(struct {
		Stdout   []byte `json:"stdout,omitempty"`
		ExitCode int    `json:"exit_code"`
	}{Stdout: []byte(aos487Documento)})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	dec, err := decodeResult(antigo)
	if err != nil {
		t.Fatalf("decodeResult: %v", err)
	}
	if string(dec.Stdout) != aos487Documento {
		t.Fatalf("o formato anterior descodificou para %q", dec.Stdout)
	}
	if dec.Taint() != TaintUntrusted {
		t.Fatal("o resultado descodificado tem de ser untrusted")
	}
}

// TestAOS487_StdoutVazioMantemOsBytes prova que o resultado sem stdout fica byte a byte igual
// ao de antes (`{"exit_code":0}`), e que um resultado com os dois campos é recusado.
func TestAOS487_StdoutVazioMantemOsBytes(t *testing.T) {
	enc, err := encodeResult(newResult(nil, nil, 0))
	if err != nil {
		t.Fatalf("encodeResult: %v", err)
	}
	if string(enc) != `{"exit_code":0}` {
		t.Fatalf("resultado vazio = %s, esperado {\"exit_code\":0}", enc)
	}
	_, err = decodeResult([]byte(`{"stdout_text":"a","stdout":"Yg==","exit_code":0}`))
	if !errors.Is(err, ErrAmbiguousResult) {
		t.Fatalf("os dois campos ao mesmo tempo: err = %v, esperado ErrAmbiguousResult", err)
	}
}

// TestAOS487_OModeloRecebeOTextoPeloRM prova o caminho de produção: o output que o Reference
// Monitor devolve ao despachar a tool da sandbox — o que o ciclo do runtime põe no prompt —
// traz o stdout em texto.
func TestAOS487_OModeloRecebeOTextoPeloRM(t *testing.T) {
	store := newStore(t)
	launcher, err := NewLauncher(NewFakeDriver(), WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "doc_read")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	input, err := json.Marshal(ExecRequest{
		RunID: "run-487", StepID: "step-000001-tool-1",
		Call: ToolCall{Command: "read", Args: []string{"Reunião", "notes"}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	authz := defaultAuthz()
	dec, err := rm.Mediate(context.Background(), referencemonitor.Call{
		RunID: "run-487", StepID: "step-000001-tool-1", ToolID: ml.ToolID(),
		Capability: authz.Capability, Resource: authz.Resource, Principal: authz.Principal,
		Credential: authz.Credential, Input: input,
	})
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit || dec.ToolErr != nil {
		t.Fatalf("decisão = %s, erro da tool = %v", dec.Effect, dec.ToolErr)
	}
	if !strings.Contains(string(dec.Output), `"stdout_text":"read Reunião notes"`) {
		t.Fatalf("o output que chega ao modelo não traz o stdout em texto: %s", dec.Output)
	}
}
