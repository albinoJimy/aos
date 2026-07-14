package registry

import (
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

// PinnedDep projecta uma entrada RESOLVIDA do catálogo no par pinado
// (version, digest) do manifesto de dependências da trajectória (AOS-047,
// tecnica/05 §7; ADR-012). REUTILIZA o tipo agentruntime.PinnedDep do manifesto
// por turno do RT (AOS-013/016) — NÃO reimplementa o manifesto: apenas produz a
// dependência pinada a partir da entrada resolvida, que o RT grava no seu
// manifesto imutável (base do replay fiel).
//
// O digest transportado é o digest JÁ VERIFICADO na resolução (Resolve
// recalcula e compara antes de devolver a entrada), pelo que uma PinnedDep só é
// construível a partir de conteúdo cuja integridade coincide com o pin.
//
// Para um servidor MCP, o campo MCPServer é preenchido com a origem (ex.:
// "mcp://host") para desambiguar a proveniência do transporte; para tool/skill
// fica vazio.
func PinnedDep(e domain.Entry) agentruntime.PinnedDep {
	dep := agentruntime.PinnedDep{
		Name:    e.ID,
		Version: e.Version.String(),
		Digest:  e.Digest,
	}
	if e.Kind == domain.KindMCPServer {
		dep.MCPServer = e.Provenance.Origin
	}
	return dep
}

// ManifestDeps projecta um conjunto de entradas resolvidas (o tool set do run)
// nas dependências pinadas do manifesto, PRESERVANDO a ordem dada (determinismo
// do manifesto). É o adaptador que o congelamento por run (AOS-050) usará para
// gravar (version, digest) de todo o tool set congelado no manifesto de
// dependências da trajectória.
func ManifestDeps(entries []domain.Entry) []agentruntime.PinnedDep {
	if len(entries) == 0 {
		return nil
	}
	out := make([]agentruntime.PinnedDep, len(entries))
	for i, e := range entries {
		out[i] = PinnedDep(e)
	}
	return out
}
