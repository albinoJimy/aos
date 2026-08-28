package agentruntime

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestTailFromResultMarksProvenance: o segmento de tool traz sempre "taint=…" e,
// quando a tool falhou, o marcador "tool_error=" — mantendo o Value untrusted.
func TestTailFromResultMarksProvenance(t *testing.T) {
	ok := tailFromResult(Untrusted([]byte("saida")), nil)
	// AssemblyVersion 1.3.0: o rotulo vive na LINHA DE DELIMITACAO; o corpo e SO o valor.
	if got := rotuloDe(ok, "taint"); got != TaintUntrusted {
		t.Fatalf("taint=%q, quero %q", got, TaintUntrusted)
	}
	if bytes.Contains(ok.Content, []byte("taint=")) {
		t.Fatalf("o rotulo voltou ao CORPO — o segundo vector da forja reabre: %q", ok.Content)
	}
	if bytes.Contains(ok.Content, []byte("tool_error=")) {
		t.Fatalf("não devia haver tool_error sem erro: %q", ok.Content)
	}
	failed := tailFromResult(Untrusted(nil), errors.New("kaput"))
	if !bytes.Contains(failed.Content, []byte("tool_error=kaput\n")) {
		t.Fatalf("faltava marcador tool_error: %q", failed.Content)
	}
}

// TestTailFromHistoryMarksProvenance: o texto do modelo (untrusted-por-construção)
// leva o mesmo esquema de marcação de proveniência dos resultados de tool.
func TestTailFromHistoryMarksProvenance(t *testing.T) {
	h := tailFromHistory("resposta do modelo")
	if h.Kind != TailHistory {
		t.Fatalf("kind errado: %q", h.Kind)
	}
	// AssemblyVersion 1.3.0: o rotulo vive na LINHA DE DELIMITACAO, nao no corpo.
	if got := rotuloDe(h, "taint"); got != TaintUntrusted {
		t.Fatalf("taint=%q, quero %q", got, TaintUntrusted)
	}
	// E a metade que importa: o corpo e SO conteudo. Se o rotulo voltasse para ca,
	// voltaria a partilhar espaco de linhas com o que a tool escreve.
	if bytes.Contains(h.Content, []byte("taint=")) {
		t.Fatalf("o rotulo voltou ao CORPO — o segundo vector da forja reabre: %q", h.Content)
	}
	if !bytes.Equal(h.Content, []byte("resposta do modelo")) {
		t.Fatalf("corpo alterado: %q", h.Content)
	}
}

// rotuloDe devolve o valor do rotulo k na linha de delimitacao do segmento ("" se ausente).
func rotuloDe(seg TailSegment, k string) string {
	for _, m := range seg.Meta {
		if m.Key == k {
			return m.Value
		}
	}
	return ""
}

func toolSet() []ToolSpec {
	return []ToolSpec{
		{Name: "web_search", Version: "1.7.0", Digest: "sha256:aa01"},
		{Name: "report_writer", Version: "2.3.1", Digest: "sha256:bb02"},
		{Name: "fs.read", Version: "1.0.4", Digest: "sha256:cc03", MCPServer: "fs@1.0.4"},
	}
}

// TestPrefixByteIdenticalAcrossTurns é a regressão de cache canónica (ADR-009):
// o prefixo tem de ser BYTE-IDÊNTICO entre turnos com o mesmo tool set, mesmo
// quando o tail cresce.
func TestPrefixByteIdenticalAcrossTurns(t *testing.T) {
	asm := NewPromptAssembler("És um agente do AOS.", toolSet())

	tail1 := []TailSegment{{Kind: TailObjective, Content: []byte("objectivo")}}
	tail2 := append(append([]TailSegment{}, tail1...),
		TailSegment{Kind: TailHistory, Content: []byte("turno 1")},
		TailSegment{Kind: TailToolResult, Content: []byte("resultado")},
	)

	v1 := asm.Assemble(1, tail1)
	v2 := asm.Assemble(2, tail2)

	if !bytes.Equal(v1.Prefix, v2.Prefix) {
		t.Fatalf("prefixo divergiu entre turnos:\nt1=%q\nt2=%q", v1.Prefix, v2.Prefix)
	}
	// E também idêntico ao prefixo exposto directamente.
	if !bytes.Equal(v1.Prefix, asm.Prefix()) {
		t.Fatalf("Prefix() diverge do prefixo materializado")
	}
	// O prefixo tem de ser um prefixo real do materializado.
	if !bytes.HasPrefix(v1.Materialized, v1.Prefix) {
		t.Fatalf("materializado não começa pelo prefixo congelado")
	}
	if !bytes.HasPrefix(v2.Materialized, v2.Prefix) {
		t.Fatalf("materializado (t2) não começa pelo prefixo congelado")
	}
}

// TestPromptHashChangesWithTail garante que o hash muda quando o tail cresce
// (o prompt_hash é por turno) mas o prefixo/hash-de-prefixo se mantém.
func TestPromptHashChangesWithTail(t *testing.T) {
	asm := NewPromptAssembler("sys", toolSet())
	v1 := asm.Assemble(1, []TailSegment{{Kind: TailObjective, Content: []byte("a")}})
	v2 := asm.Assemble(2, []TailSegment{
		{Kind: TailObjective, Content: []byte("a")},
		{Kind: TailHistory, Content: []byte("b")},
	})
	if v1.PromptHash == v2.PromptHash {
		t.Fatalf("prompt_hash devia mudar com o tail; ambos = %s", v1.PromptHash)
	}
	if !strings.HasPrefix(v1.PromptHash, "sha256:") {
		t.Fatalf("prompt_hash sem prefixo sha256: %s", v1.PromptHash)
	}
	if asm.PrefixHash() == "" || asm.SystemHash() == "" {
		t.Fatalf("hashes de prefixo/system vazios")
	}
}

// TestToolSetOrderIsFrozen prova que a ORDEM do tool set é significativa: dois
// assemblers com as mesmas tools em ordem diferente produzem prefixos distintos
// (o prefixo nunca é reordenado — reordenar seria uma regressão de cache).
func TestToolSetOrderIsFrozen(t *testing.T) {
	a := NewPromptAssembler("sys", []ToolSpec{{Name: "x"}, {Name: "y"}})
	b := NewPromptAssembler("sys", []ToolSpec{{Name: "y"}, {Name: "x"}})
	if bytes.Equal(a.Prefix(), b.Prefix()) {
		t.Fatalf("ordem do tool set devia afectar o prefixo (nunca reordenar)")
	}
}

// TestSameInputsSamePrefix confirma determinismo: os mesmos inputs produzem o
// mesmo prefixo byte-a-byte em instâncias independentes.
func TestSameInputsSamePrefix(t *testing.T) {
	a := NewPromptAssembler("sys", toolSet())
	b := NewPromptAssembler("sys", toolSet())
	if !bytes.Equal(a.Prefix(), b.Prefix()) {
		t.Fatalf("prefixo não-determinístico entre instâncias")
	}
	if a.PrefixHash() != b.PrefixHash() {
		t.Fatalf("prefix hash não-determinístico")
	}
}
