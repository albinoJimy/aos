package trajectorysurface

import (
	"encoding/hex"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// SpanNode é um nó da árvore de trajectória: o span OTel (LIDO, nunca mutado), o
// seu KIND de operação (invoke_agent|execute_tool|chat, derivado de
// gen_ai.operation.name) e os filhos ligados por ParentSpanID->SpanID. É a forma
// NAVEGÁVEL da árvore de spans de AOS-077 — expandir/colapsar um sub-agente é
// percorrer [SpanNode.Children].
//
// LEITURA PURA: [SpanNode.Span] é uma CÓPIA por valor da [otelgenai.SpanData] de
// entrada. Esta camada nunca escreve nos atributos nem re-emite o span — os
// SpanData de entrada ficam byte-a-byte intactos (AC3).
type SpanNode struct {
	// Span é o span OTel lido de AOS-077 (cópia por valor; nunca mutado).
	Span otelgenai.SpanData
	// Kind é a operação GenAI do span (ver [KindInvokeAgent]/[KindExecuteTool]/
	// [KindChat]); "" se o span não declara operação nem tem Name reconhecível.
	Kind string
	// Children são os spans-filho DIRECTOS (parent_span_id == este span_id), na
	// ordem em que aparecem na lista de entrada.
	Children []*SpanNode
}

// Kinds de operação apresentados (espelham [otelgenai.OpInvokeAgent] etc.). São só
// rótulos de leitura; a fonte é o atributo gen_ai.operation.name do span.
const (
	// KindInvokeAgent — o span que envolve um run/nível de delegação (sub-agente).
	KindInvokeAgent = otelgenai.OpInvokeAgent
	// KindExecuteTool — o span de uma tool call mediada pelo Reference Monitor.
	KindExecuteTool = otelgenai.OpExecuteTool
	// KindChat — o span de um turno de modelo (a unidade-verdade de tokens/custo).
	KindChat = otelgenai.OpChat
)

// BuildTree constrói a floresta de [SpanNode] a partir de um conjunto de spans de um
// run e dos seus sub-agentes, ligando cada nó ao pai por ParentSpanID->SpanID
// (AC1). As RAÍZES são os spans sem pai no conjunto (parent nulo, ou um
// parent_span_id que não pertence ao conjunto — floresta parcial). A hierarquia
// invoke_agent -> execute_tool -> chat e a ORDEM de aparição são preservadas.
//
// LEITURA PURA (AC3): copia/lê os SpanData, NUNCA os muta e NUNCA re-emite um span.
// Um span_id repetido no conjunto é resolvido pela PRIMEIRA ocorrência (determinista;
// a árvore de spans real é acíclica e de ids únicos por construção).
func BuildTree(spans []otelgenai.SpanData) []*SpanNode {
	nodes := make(map[string]*SpanNode, len(spans))
	order := make([]string, 0, len(spans))
	for i := range spans {
		sd := spans[i] // cópia por valor — a entrada fica intacta
		id := sd.SpanContext.SpanIDHex()
		if _, exists := nodes[id]; exists {
			continue // primeira ocorrência ganha (determinismo)
		}
		nodes[id] = &SpanNode{Span: sd, Kind: kindOf(sd)}
		order = append(order, id)
	}

	var roots []*SpanNode
	for _, id := range order {
		n := nodes[id]
		parentHex := parentHexOf(n.Span)
		if parentHex == "" {
			roots = append(roots, n)
			continue
		}
		if parent, ok := nodes[parentHex]; ok && parent != n {
			parent.Children = append(parent.Children, n)
			continue
		}
		// Pai fora do conjunto (ou auto-referência degenerada) => raiz da floresta.
		roots = append(roots, n)
	}
	return roots
}

// Flatten devolve os spans da sub-árvore enraizada em node por percurso em
// pré-ordem (node primeiro, depois cada filho recursivamente). É a projecção que
// alimenta as agregações de custo COMPOSTAS de otel-genai (RollupByTrace/
// AggregateByTrace) sem reimplementar a contabilidade. Leitura pura: devolve cópias
// dos SpanData, sem mutar a árvore.
func Flatten(node *SpanNode) []otelgenai.SpanData {
	if node == nil {
		return nil
	}
	out := make([]otelgenai.SpanData, 0, 1)
	var walk func(n *SpanNode)
	walk = func(n *SpanNode) {
		out = append(out, n.Span)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(node)
	return out
}

// kindOf deriva o KIND de um span: o valor de gen_ai.operation.name se presente e
// string não-vazia, senão o Name do span (espelha a semântica de operationOf de
// otel-genai sem depender do seu símbolo não-exportado).
func kindOf(sd otelgenai.SpanData) string {
	if v, ok := sd.Attribute(otelgenai.AttrOperationName); ok {
		if s, isStr := v.(string); isStr && s != "" {
			return s
		}
	}
	return sd.Name
}

// parentHexOf devolve o parent_span_id em hex, ou "" se o span é raiz (parent nulo).
func parentHexOf(sd otelgenai.SpanData) string {
	if sd.ParentSpanID == ([8]byte{}) {
		return ""
	}
	return hex.EncodeToString(sd.ParentSpanID[:])
}
