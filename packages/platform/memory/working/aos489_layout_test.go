package working

// AOS-489 — a janela gerida monta o prompt NO LAYOUT DO RUN.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestAOS489_LayoutDoPrefixo: o [Config.AssemblyVersion] decide o prefixo que o WindowManager
// congela — e portanto a ocupação do prefixo em tokens —, é herdado pelos runs derivados, e uma
// versão desconhecida é recusada na construção.
func TestAOS489_LayoutDoPrefixo(t *testing.T) {
	porCaracter := func(s string) int { return len(s) }
	novo := func(versao string) (*WindowManager, error) {
		return NewWindowManager(Config{
			RunID: "run-489", System: "sys", Tools: refTools(), ModelTokenLimit: 100_000,
			Estimator: porCaracter, AssemblyVersion: versao,
		})
	}

	wm130, err := novo(agentruntime.AssemblyVersion130)
	if err != nil {
		t.Fatalf("1.3.0: %v", err)
	}
	wm140, err := novo(agentruntime.AssemblyVersion140)
	if err != nil {
		t.Fatalf("1.4.0: %v", err)
	}
	porOmissao, err := novo("")
	if err != nil {
		t.Fatalf("sem versao: %v", err)
	}

	const protocolo = "=== PROTOCOL ===\n"
	if bytes.Contains(wm130.Prefix(), []byte(protocolo)) || !bytes.HasPrefix(wm130.Prefix(), []byte("=== SYSTEM ===\n")) {
		t.Fatalf("o prefixo 1.3.0 nao pode ter o preambulo:\n%s", wm130.Prefix())
	}
	if !bytes.HasPrefix(wm140.Prefix(), []byte(protocolo)) {
		t.Fatalf("o prefixo 1.4.0 tem de abrir com o preambulo:\n%s", wm140.Prefix())
	}
	// Sem versão, o layout dos runs novos.
	if agentruntime.AssemblyVersion != agentruntime.AssemblyVersion140 || !bytes.Equal(porOmissao.Prefix(), wm140.Prefix()) {
		t.Fatal("um Config sem AssemblyVersion tem de montar o layout dos runs novos")
	}
	// O preâmbulo CONTA na ocupação: é prompt que o modelo vê em todos os turnos. A diferença
	// entre os dois prefixos é exactamente o que o 1.4.0 tem a mais.
	o130, o140 := wm130.Occupancy().PrefixTokens, wm140.Occupancy().PrefixTokens
	if o140-o130 != len(wm140.Prefix())-len(wm130.Prefix()) || o140 <= o130 {
		t.Fatalf("ocupacao do prefixo: 1.3.0=%d 1.4.0=%d (bytes: %d e %d)", o130, o140, len(wm130.Prefix()), len(wm140.Prefix()))
	}
	// A vista diz o layout, e o tail é montado com os rótulos que a porta transporta.
	wm140.Append(TailInput{
		Kind:    TailToolCall,
		Meta:    []TailMeta{{Key: "taint", Value: "untrusted"}, {Key: "id", Value: "step-000001-tool-1"}, {Key: "name", Value: "doc_read"}},
		Content: `{"doc_id":"notes"}`,
	})
	v := wm140.Turn(context.Background()).View
	if v.AssemblyVersion != agentruntime.AssemblyVersion140 || v.System != "sys" {
		t.Fatalf("vista: layout=%q system=%q", v.AssemblyVersion, v.System)
	}
	if quero := "<tool_call taint=untrusted id=step-000001-tool-1 name=doc_read>\n{\"doc_id\":\"notes\"}\n"; !bytes.HasSuffix(v.Materialized, []byte(quero)) {
		t.Fatalf("o tool_call nao foi materializado com os seus rotulos:\n%s", v.Materialized)
	}
	if len(v.Tail) != 1 || v.Tail[0].Kind != TailToolCall || len(v.Tail[0].Meta) != 3 {
		t.Fatalf("o tail estruturado da vista: %+v", v.Tail)
	}

	// O run DERIVADO herda o layout — um run 1.3.0 não deriva um 1.4.0.
	derivado, err := wm130.NewRunWith("run-489-b", ToolSpec{Name: "extra", Version: "1.0.0", Digest: "sha256:ee"})
	if err != nil {
		t.Fatalf("NewRunWith: %v", err)
	}
	if bytes.Contains(derivado.Prefix(), []byte(protocolo)) {
		t.Fatalf("o run derivado de um 1.3.0 ganhou o preambulo:\n%s", derivado.Prefix())
	}

	// Versão desconhecida ⇒ a janela não se constrói.
	for _, v := range []string{"1.2.0", "9.9.9", "latest"} {
		if wm, err := novo(v); !errors.Is(err, agentruntime.ErrUnknownAssemblyVersion) || wm != nil {
			t.Fatalf("AssemblyVersion=%q: (%v, %v), quero (nil, ErrUnknownAssemblyVersion)", v, wm, err)
		}
	}
}
