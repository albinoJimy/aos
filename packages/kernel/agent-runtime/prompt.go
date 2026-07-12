package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// AssemblyVersion é a versão do CÓDIGO de montagem do prompt. Vai no manifesto
// por trajectória (ADR-010): um replay tem de saber com que assembler o prompt
// foi materializado. Incrementar sempre que o layout do prefixo ou do tail mude
// de forma que altere os bytes materializados.
const AssemblyVersion = "1.0.0"

// hashPrefix é o prefixo dos hashes emitidos (formato tecnica/13 §3: "sha256:…").
const hashPrefix = "sha256:"

// ToolSpec é a identidade PINADA de uma tool ou skill congelada no run: nome +
// versão + digest (+ servidor MCP opcional). É exactamente a lista que o tool set
// congelado do prefixo cache-estável fixa (ADR-009) e que o manifesto pina
// (tecnica/13 §6). A ORDEM em que é fornecida é significativa e NUNCA é
// reordenada — reordenar quebraria a estabilidade byte-a-byte do prefixo.
type ToolSpec struct {
	Name      string
	Version   string
	Digest    string
	MCPServer string
}

// TailKind classifica um segmento do tail append-only.
type TailKind string

const (
	// TailMemory — memory_context injectado (ver EPIC-04).
	TailMemory TailKind = "memory"
	// TailTimestamp — um timestamp observacional.
	TailTimestamp TailKind = "timestamp"
	// TailObjective — o objectivo/instrução inicial do turno.
	TailObjective TailKind = "objective"
	// TailToolResult — o resultado (untrusted) de uma tool despachada.
	TailToolResult TailKind = "tool_result"
	// TailHistory — histórico de turnos anteriores (texto do modelo).
	TailHistory TailKind = "history"
)

// TailSegment é uma unidade append-only do tail. O tail cresce a cada turno; o
// prefixo NUNCA. Content é opaco (bytes já serializados pelo chamador).
type TailSegment struct {
	Kind    TailKind
	Content []byte
}

// PromptView é o resultado de materializar o prompt de um turno. Expõe o prefixo
// (para asserção de estabilidade de cache) e o materializado completo, mais o
// hash do materializado (âncora de replay fiel, ADR-010).
type PromptView struct {
	// Turn é o índice (1-based) do turno.
	Turn int
	// Prefix é o prefixo IMUTÁVEL (system + tool set congelado). É byte-idêntico
	// entre todos os turnos deste assembler.
	Prefix []byte
	// Materialized é o prompt completo do turno: Prefix ++ tail serializado.
	Materialized []byte
	// PromptHash é sha256(Materialized) no formato "sha256:<hex>".
	PromptHash string
	// PrefixHash é sha256(Prefix) no formato "sha256:<hex>". É byte-idêntico entre
	// turnos do mesmo run — o SLI de estabilidade do prefixo, exposto no PromptView
	// para que o span chat o emita (cache-hit-rate observável, AOS-013 CA3).
	PrefixHash string
}

// PromptAssembler monta prompts cache-estáveis (ADR-009). É construído uma vez
// por run com o system prompt e o tool set congelado; o prefixo é calculado UMA
// vez e reutilizado (bytes idênticos) em cada [PromptAssembler.Assemble]. Não é
// mutável após a construção — não há qualquer método que altere o prefixo, o que
// torna a regressão de cache estruturalmente impossível dentro de um run.
type PromptAssembler struct {
	prefix     []byte // imutável após New
	systemHash string // sha256(system) — vai ao manifesto (system_hash)
	prefixHash string // sha256(prefix)
}

// NewPromptAssembler congela o prefixo a partir do system prompt e do tool set.
// A ordem de tools é preservada tal-e-qual (nunca reordenada).
func NewPromptAssembler(system string, tools []ToolSpec) *PromptAssembler {
	prefix := buildPrefix(system, tools)
	return &PromptAssembler{
		prefix:     prefix,
		systemHash: sha256Tagged([]byte(system)),
		prefixHash: sha256Tagged(prefix),
	}
}

// buildPrefix serializa o prefixo de forma determinística e estável. O layout é
// deliberadamente simples e append-only-friendly: uma secção SYSTEM seguida de
// uma secção TOOLSET onde cada linha descreve uma tool na ORDEM congelada. Não há
// mapas nem qualquer fonte de não-determinismo — os mesmos inputs produzem
// sempre os mesmos bytes.
func buildPrefix(system string, tools []ToolSpec) []byte {
	var b []byte
	b = append(b, "=== SYSTEM ===\n"...)
	b = append(b, system...)
	b = append(b, '\n')
	b = append(b, "=== TOOLSET (frozen) ===\n"...)
	for _, t := range tools {
		// Campos separados por TAB e terminados por \n; ordem NUNCA alterada.
		b = append(b, "tool\t"...)
		b = append(b, t.Name...)
		b = append(b, '\t')
		b = append(b, t.Version...)
		b = append(b, '\t')
		b = append(b, t.Digest...)
		b = append(b, '\t')
		b = append(b, t.MCPServer...)
		b = append(b, '\n')
	}
	b = append(b, "=== CONTEXT (append-only) ===\n"...)
	return b
}

// Assemble materializa o prompt do turno: reutiliza o prefixo congelado e
// serializa o tail append-only por cima. Devolve cópias defensivas (o chamador
// não pode mutar o estado interno do assembler).
func (a *PromptAssembler) Assemble(turn int, tail []TailSegment) PromptView {
	// Prefixo: cópia dos bytes congelados (byte-idêntica entre turnos).
	mat := make([]byte, len(a.prefix))
	copy(mat, a.prefix)

	// Tail append-only, na ordem fornecida (cronológica).
	for _, seg := range tail {
		mat = append(mat, '<')
		mat = append(mat, string(seg.Kind)...)
		mat = append(mat, ">\n"...)
		mat = append(mat, seg.Content...)
		mat = append(mat, '\n')
	}

	prefixCopy := make([]byte, len(a.prefix))
	copy(prefixCopy, a.prefix)

	return PromptView{
		Turn:         turn,
		Prefix:       prefixCopy,
		Materialized: mat,
		PromptHash:   sha256Tagged(mat),
		PrefixHash:   a.prefixHash,
	}
}

// Prefix devolve uma cópia do prefixo congelado (imutável). Existe para asserção
// de estabilidade de cache e para observabilidade.
func (a *PromptAssembler) Prefix() []byte {
	out := make([]byte, len(a.prefix))
	copy(out, a.prefix)
	return out
}

// SystemHash devolve sha256("<system>") no formato "sha256:<hex>". Vai ao
// manifesto (system_hash) e o prompt_hash de cada turno deve resolver contra o
// tool set congelado deste assembler (tecnica/13 §6).
func (a *PromptAssembler) SystemHash() string { return a.systemHash }

// PrefixHash devolve sha256(prefix) no formato "sha256:<hex>". É o SLI de
// estabilidade do prefixo: se mudar entre turnos do mesmo run, houve regressão
// de cache.
func (a *PromptAssembler) PrefixHash() string { return a.prefixHash }

// sha256Tagged calcula sha256 e devolve "sha256:<hex>".
func sha256Tagged(b []byte) string {
	sum := sha256.Sum256(b)
	return hashPrefix + hex.EncodeToString(sum[:])
}

// tailFromResult constrói um segmento de tail a partir de um resultado de tool
// (marcado untrusted). O prefixo textual torna a proveniência explícita no
// prompt materializado — coerente com ADR-005 (o modelo vê que é untrusted).
// Quando a tool PERMITIDA falhou em runtime (toolErr != nil), a condição de erro
// é materializada como marcador "tool_error=<msg>" para que o modelo distinga um
// output vazio legítimo de uma falha de execução downstream (o conteúdo mantém-se
// untrusted; a mensagem de erro NÃO autoriza nada).
func tailFromResult(r Tainted, toolErr error) TailSegment {
	content := append([]byte("taint="), r.Taint...)
	content = append(content, '\n')
	if toolErr != nil {
		content = append(content, "tool_error="...)
		content = append(content, toolErr.Error()...)
		content = append(content, '\n')
	}
	content = append(content, r.Value...)
	return TailSegment{Kind: TailToolResult, Content: content}
}

// tailFromHistory constrói um segmento de tail a partir do texto do modelo. A
// saída do modelo é untrusted-por-construção (ADR-005, taint.go): aplicamos-lhe o
// MESMO esquema de marcação de proveniência dos resultados de tool ("taint=…"),
// para consistência e auditabilidade do prompt materializado (defesa-em-profundidade).
func tailFromHistory(text string) TailSegment {
	content := append([]byte("taint="), TaintUntrusted...)
	content = append(content, '\n')
	content = append(content, text...)
	return TailSegment{Kind: TailHistory, Content: content}
}

// TailFromModelText constrói o segmento de tail do texto do modelo de um turno.
// É EXACTAMENTE a construção que o loop usa ([tailFromHistory]) — exportada para o
// motor de replay (AOS-016) reconstruir o tail BYTE-IDÊNTICO ao original, condição
// necessária para o prompt_hash re-materializado coincidir com o gravado. Reutilizar
// esta função (em vez de replicar o layout) garante que o tail do replay não
// diverge do tail do loop se o layout evoluir (a par de [AssemblyVersion]).
func TailFromModelText(text string) TailSegment { return tailFromHistory(text) }

// TailFromToolResult constrói o segmento de tail de um resultado de tool. É a MESMA
// construção do loop ([tailFromResult]), exportada para o replay reconstruir o tail
// byte-idêntico (ver [TailFromModelText]).
func TailFromToolResult(r Tainted, toolErr error) TailSegment { return tailFromResult(r, toolErr) }

// itoa é um atalho local (evita fmt em hot path de montagem).
func itoa(n int) string { return strconv.Itoa(n) }
