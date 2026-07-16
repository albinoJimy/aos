package taint

// Label é o rótulo de confiança de um dado no reticulado control/data-plane
// (ADR-005). É um tipo distinto (não uma string livre) para que o rótulo viaje no
// sistema de tipos e não seja confundido com texto arbitrário.
//
// FAIL-CLOSED PELO TIPO: o valor-zero é [Untrusted]. Um [Label] que nunca foi
// explicitamente marcado [Trusted] é untrusted — não há promoção implícita. Não
// existe operação de desclassificação (untrusted→trusted) nesta API: a promoção é
// estruturalmente impossível, só a origem ([LabelFor]) ou o join ([Join]) atribuem
// rótulos, e o join nunca produz trusted a partir de untrusted.
type Label uint8

const (
	// Untrusted é o rótulo por omissão (valor-zero, fail-closed): conteúdo que
	// não pode, por si só, autorizar acções privilegiadas. É dados, nunca
	// instruções.
	Untrusted Label = iota
	// Trusted é conteúdo de origem confiável (system + utilizador autenticado). Só
	// dados trusted podem originar uma tool call privilegiada (enforcement no RM).
	Trusted
)

// Formas textuais canónicas dos rótulos. Espelham as strings já usadas pelo Agent
// Runtime (agentruntime.TaintTrusted/TaintUntrusted) e pelo campo
// referencemonitor.CallContext.Taint, permitindo a ponte por [ParseLabel] /
// [Label.String] sem acoplar os tipos.
const (
	// StringTrusted é a forma textual de [Trusted].
	StringTrusted = "trusted"
	// StringUntrusted é a forma textual de [Untrusted].
	StringUntrusted = "untrusted"
)

// IsTrusted indica se o rótulo é EXACTAMENTE [Trusted]. Qualquer outro valor
// (incluindo rótulos inválidos) é untrusted.
func (l Label) IsTrusted() bool { return l == Trusted }

// IsUntrusted indica se o rótulo NÃO é trusted (fail-closed: tudo o que não é
// explicitamente [Trusted] conta como untrusted).
func (l Label) IsUntrusted() bool { return l != Trusted }

// Valid indica se o rótulo é um valor canónico do reticulado.
func (l Label) Valid() bool { return l == Trusted || l == Untrusted }

// String devolve a forma textual canónica. Fail-closed: qualquer valor que não
// seja [Trusted] serializa como [StringUntrusted] (nunca há representação que
// sugira confiança para um valor não-trusted).
func (l Label) String() string {
	if l == Trusted {
		return StringTrusted
	}
	return StringUntrusted
}

// ParseLabel converte a forma textual no [Label]. É FAIL-CLOSED: só a string
// canónica [StringTrusted] resolve [Trusted]; vazio, "untrusted" ou qualquer
// valor desconhecido/forjado resolve [Untrusted]. Assim, um campo de taint
// ausente ou adulterado nunca é tratado como confiável.
func ParseLabel(s string) Label {
	if s == StringTrusted {
		return Trusted
	}
	return Untrusted
}

// Origin é a FONTE de onde um dado entrou no sistema — o "de onde veio". Os
// valores canónicos espelham platform/memory/domain.ProvenanceSource, para que a
// proveniência de memória componha com este reticulado sem importar a memória.
type Origin string

const (
	// OriginSystem — conteúdo produzido pelo próprio sistema (system prompt,
	// objectivo selado). Classifica [Trusted].
	OriginSystem Origin = "system"
	// OriginAuthenticatedUser — input do utilizador autenticado. Classifica [Trusted].
	OriginAuthenticatedUser Origin = "authenticated_user"
	// OriginToolResult — output de uma tool call (conteúdo externo). Untrusted.
	OriginToolResult Origin = "tool_result"
	// OriginWeb — conteúdo obtido da web. Untrusted.
	OriginWeb Origin = "web"
	// OriginMCPSchema — descrições/schemas de servidores MCP. Untrusted.
	OriginMCPSchema Origin = "mcp_schema"
	// OriginDerivedMemory — memória derivada de outra memória (o taint deriva dos
	// pais, não da fonte directa). Untrusted.
	OriginDerivedMemory Origin = "derived_memory"
	// OriginModelOutput — a saída do próprio modelo. Untrusted-por-construção: o
	// modelo pode ter sido influenciado por dados untrusted no seu contexto.
	OriginModelOutput Origin = "model_output"
)

// LabelFor classifica uma [Origin] no reticulado de confiança. FAIL-CLOSED: SÓ
// [OriginSystem] e [OriginAuthenticatedUser] são [Trusted]; TODAS as outras
// origens — incluindo uma origem vazia ou desconhecida/forjada — classificam
// [Untrusted]. É o único ponto onde um dado ADQUIRE o rótulo trusted (na origem);
// daí em diante só o join o pode degradar, nunca elevar.
func LabelFor(o Origin) Label {
	switch o {
	case OriginSystem, OriginAuthenticatedUser:
		return Trusted
	default:
		return Untrusted
	}
}
