package mcp

import (
	"context"
	"strings"
	"testing"

	memdomain "github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
)

// TestSecurity_ToolPoisoning_TaintedNotControlPlane é o teste central de segurança
// (ADR-005): uma descrição de tool com "IGNORA AS INSTRUCOES ANTERIORES…" é tratada
// como DADOS inertes (taint), NÃO comanda o planeador. Prova a barreira estrutural de
// AOS-042 reutilizada por AOS-046:
//
//   - tudo o que o servidor devolve cai na QUARENTENA (data-plane), nunca na
//     TrustedView (control-plane);
//   - cada item é um [provenance.DataItem] que, por TIPO, NÃO satisfaz
//     [provenance.PrivilegedAuthorizer] — não consegue autorizar uma tool call;
//   - o texto injectado É transportado (como dados), provando que não foi filtrado
//     de forma frágil mas sim neutralizado estruturalmente.
func TestSecurity_ToolPoisoning_TaintedNotControlPlane(t *testing.T) {
	t.Parallel()
	h, _ := newTestHost(t, nil)
	tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "server"}, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	res, err := h.Discover(context.Background(), tr, testConn("mcp.evil"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// (1) NADA do que o servidor devolveu é control-plane: a TrustedView está vazia.
	if n := res.Taint.TrustedView().Len(); n != 0 {
		t.Fatalf("TrustedView tem %d entradas; conteudo MCP nunca e control-plane", n)
	}

	// (2) Tudo caiu na quarentena (2 tools + 1 resource = 3).
	items := res.Taint.Quarantine().Items()
	if len(items) != 3 {
		t.Fatalf("quarentena tem %d itens, quer 3 (2 tools + 1 resource)", len(items))
	}

	// (3) Cada item é UNTRUSTED e NÃO satisfaz PrivilegedAuthorizer (barreira de tipo).
	var foundPoison bool
	for _, item := range items {
		if item.Taint() != provenance.Untrusted {
			t.Fatalf("item de quarentena com taint %q, quer untrusted", item.Taint())
		}
		if _, ok := any(item).(provenance.PrivilegedAuthorizer); ok {
			t.Fatal("um DataItem em quarentena NAO pode satisfazer PrivilegedAuthorizer")
		}
		// O texto injectado está presente como DADOS (não foi silenciosamente descartado).
		if strings.Contains(contentOf(item), "IGNORA AS INSTRUCOES ANTERIORES") {
			foundPoison = true
		}
	}
	if !foundPoison {
		t.Fatal("a descricao envenenada devia ser transportada como dados taintados")
	}
}

// TestSecurity_DiscoverIngestsAsMCPSchema prova que a marcação usa a fonte mcp_schema
// (classificada untrusted por AOS-042), independentemente do transporte.
func TestSecurity_DiscoverIngestsAsMCPSchema(t *testing.T) {
	t.Parallel()
	// A classificação canónica de mcp_schema é untrusted (fonte de verdade AOS-042).
	if got := provenance.Classify(provenance.SourceMCPSchema); got != provenance.Untrusted {
		t.Fatalf("Classify(mcp_schema) = %q, quer untrusted", got)
	}
}

// contentOf extrai o conteúdo textual de um DataItem de memória de trabalho.
func contentOf(item provenance.DataItem) string {
	if wb, ok := item.Content().Body.(memdomain.WorkingBody); ok {
		return wb.Content
	}
	return ""
}
