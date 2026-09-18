package replay

import (
	"bytes"
	"encoding/json"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestAOS406_CustoNaoDerivadoSobreviveACaptura — um turno retomado a partir da captura não pode
// voltar a parecer gratuito; e uma resposta com preço serializa os bytes de sempre.
func TestAOS406_CustoNaoDerivadoSobreviveACaptura(t *testing.T) {
	c := &EventStoreCapturer{}

	semPreco := agentruntime.ModelResponse{Text: "x", Usage: agentruntime.Usage{InputTokens: 5, OutputTokens: 2}, CustoNaoDerivado: true}
	bs, err := json.Marshal(c.encodeResponse(semPreco))
	if err != nil {
		t.Fatal(err)
	}
	var rc responseCapture
	if err := json.Unmarshal(bs, &rc); err != nil {
		t.Fatal(err)
	}
	if !rc.decode().CustoNaoDerivado {
		t.Fatalf("a marca perdeu-se na captura: %s", bs)
	}

	comPreco := agentruntime.ModelResponse{Text: "x", Usage: agentruntime.Usage{InputTokens: 5, OutputTokens: 2}, CostMicroUSD: 42}
	bs, err = json.Marshal(c.encodeResponse(comPreco))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bs, []byte("custo_nao_derivado")) {
		t.Fatalf("uma resposta com preço mudou de bytes (digest da captura): %s", bs)
	}
}
