package domain

// ArtifactKind é o TIPO de artefacto de catálogo. O REG distingue três tipos, cada
// um com um enquadramento de supply-chain próprio (tecnica/05 §3):
//
//   - skill: capacidade composta, potencialmente auto-escrita (governada por
//     eval-gate na promoção — AOS-053);
//   - tool: um schema de função individual com contrato de I/O;
//   - servidor MCP: um endpoint que expõe um conjunto de tools/recursos por um
//     transporte (STDIO/SSE/Streamable HTTP — integração em AOS-046).
//
// O tipo é parte do conteúdo canonicalizado que alimenta o digest: dois artefactos
// com o mesmo contrato mas tipos diferentes têm digests diferentes.
type ArtifactKind string

const (
	// KindSkill — capacidade composta (potencialmente auto-escrita).
	KindSkill ArtifactKind = "skill"
	// KindTool — schema de função individual com contrato de I/O.
	KindTool ArtifactKind = "tool"
	// KindMCPServer — servidor MCP que expõe tools/recursos por um transporte.
	KindMCPServer ArtifactKind = "mcp_server"
)

// Valid indica se k é um dos três tipos canónicos. Fail-closed: um tipo
// desconhecido é sempre inválido (a publicação de um artefacto de tipo não
// reconhecido é recusada).
func (k ArtifactKind) Valid() bool {
	switch k {
	case KindSkill, KindTool, KindMCPServer:
		return true
	default:
		return false
	}
}

// AllKinds devolve os três tipos canónicos, em ordem estável (determinismo de
// iteração em testes e diagnóstico).
func AllKinds() []ArtifactKind {
	return []ArtifactKind{KindSkill, KindTool, KindMCPServer}
}
