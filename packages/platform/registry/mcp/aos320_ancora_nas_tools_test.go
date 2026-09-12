package mcp

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
)

// TestAncoraChegaAsEntradasTool fecha o buraco que a revisão adversarial de AOS-320 encontrou:
// o digest do `mcp_server` estava a ser endurecido, mas o gate do CAMINHO QUENTE nunca o consulta.
//
// A revalidação por chamada (AOS-051) chaveia por `call.ToolID`, e o ToolID de uma tool MCP é
// `serverID+"/"+nome` — a entrada `kind=tool`, não a do servidor. Com o contrato dessa entrada
// limitado a (schema sanitizado, egress), um servidor que mudasse de endpoint deixava todas as
// suas tools BYTE-A-BYTE idênticas: o digest do `mcp_server` movia-se e a revalidação permitia
// a chamada na mesma. O AOS-320 protegia uma entrada que ninguém verifica a cada tool call.
//
// O que este teste fixa: mover o servidor move o digest de TUDO o que ele serve.
func TestAncoraChegaAsEntradasTool(t *testing.T) {
	t.Parallel()

	const serverID = "mcp.fs"

	staged := func(t *testing.T, endpoint string) map[string]domain.Entry {
		t.Helper()
		h, _ := newTestHost(t, nil)
		tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "server"}, seqFrom(1))
		if err != nil {
			t.Fatalf("NewSTDIOTransport: %v", err)
		}
		defer func() { _ = tr.Close() }()

		conn := testConn(serverID)
		conn.Endpoint = endpoint
		res, err := h.Discover(context.Background(), tr, conn)
		if err != nil {
			t.Fatalf("Discover(%q): %v", endpoint, err)
		}
		out := map[string]domain.Entry{}
		for _, e := range res.Staged {
			out[e.ID] = e
		}
		return out
	}

	a := staged(t, "https://mcp-a.example")
	b := staged(t, "https://mcp-b.example")

	if len(a) < 2 {
		t.Fatalf("a descoberta devia produzir o servidor MAIS pelo menos uma tool, veio %d", len(a))
	}

	var tools int
	for id, ea := range a {
		eb, ok := b[id]
		if !ok {
			t.Fatalf("entrada %q ausente na segunda descoberta", id)
		}
		if ea.Digest == eb.Digest {
			t.Errorf("entrada %q (kind=%s): o digest NAO mudou com o endpoint — %s", id, ea.Kind, ea.Digest)
		}
		if ea.Kind == domain.KindTool {
			tools++
			if ea.Contract.ManifestDigest == "" {
				t.Errorf("tool %q sem ManifestDigest: a revalidacao por chamada nao tem como ver a mudanca de servidor", id)
			}
			if ea.Contract.ManifestDigest == a[serverID].Contract.ManifestDigest {
				continue // é a MESMA âncora do servidor, que é o que se quer
			}
			t.Errorf("tool %q devia levar a MESMA ancora do servidor que a anunciou", id)
		}
	}
	if tools == 0 {
		t.Fatal("nenhuma entrada kind=tool no resultado — o teste nao exercitou o caminho que interessa")
	}
}
