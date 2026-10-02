package modelgateway

// AOS-396 — o adaptador do runtime passa ao turno o modelo que serviu a resposta. Antes, o
// `port.ChatResponse.Model` só aparecia na mensagem de erro de uma resposta sem choices, e o
// `turn.recorded` ficava sem modelo nenhum.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aos-ref/platform/model-gateway/port"
)

func TestAOS396_TranslateResponsePassaOModeloServido(t *testing.T) {
	t.Parallel()
	resp := respostaComUsage(port.Usage{PromptTokens: 3, CompletionTokens: 1})
	resp.Model = "gpt-4o-mini-2024-07-18"
	out, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if out.Model != "gpt-4o-mini-2024-07-18" {
		t.Fatalf("ModelResponse.Model = %q, quero o modelo que o provider devolveu", out.Model)
	}
}

// TestAOS396_ModeloServidoESaneado: o `model` do provider fica em claro em cada evento, pelo
// que não passa com caracteres de controlo nem acima do tecto de bytes, e o corte não parte
// um carácter UTF-8.
func TestAOS396_ModeloServidoESaneado(t *testing.T) {
	t.Parallel()
	if got := modeloServido(" gpt-4o\x00-mini\n\x1b[31m "); got != "gpt-4o-mini[31m" {
		t.Fatalf("controlo nao retirado: %q", got)
	}
	longo := strings.Repeat("é", maxModeloServido) // 2 bytes cada
	got := modeloServido(longo)
	if len(got) > maxModeloServido || !utf8.ValidString(got) {
		t.Fatalf("corte invalido: %d bytes, utf8=%v", len(got), utf8.ValidString(got))
	}
}
