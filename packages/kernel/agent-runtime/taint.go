package agentruntime

// Níveis de taint (ADR-005). O taint marca a proveniência e o nível de confiança
// de um dado: conteúdo untrusted (ex.: a saída de uma tool, ou de uma NHI
// externa) não pode, por si só, autorizar acções privilegiadas — a política do
// Reference Monitor (AOS-004/007) é que decide, tendo o taint como input
// ([referencemonitor.CallContext].Taint).
const (
	// TaintTrusted — dado de origem confiável (ex.: system prompt, objectivo).
	TaintTrusted = "trusted"
	// TaintUntrusted — dado de origem não-confiável (resultado de tool, saída do
	// modelo). É o taint por omissão de tudo o que entra no loop vindo de fora.
	TaintUntrusted = "untrusted"
)

// Tainted embrulha um valor com o seu taint. O RT devolve SEMPRE os resultados de
// tools embrulhados assim, marcados [TaintUntrusted], para que o loop (e o turno
// seguinte) tratem esse conteúdo como não-confiável (ADR-005). Nunca há um
// caminho que devolva um resultado de tool "cru" sem taint.
type Tainted struct {
	// Value é o conteúdo opaco (ex.: o output da tool).
	Value []byte
	// Taint é o nível de confiança ([TaintTrusted] / [TaintUntrusted]).
	Taint string
}

// Untrusted embrulha um valor como untrusted (helper).
func Untrusted(v []byte) Tainted { return Tainted{Value: v, Taint: TaintUntrusted} }

// IsUntrusted indica se o valor está marcado como não-confiável.
func (t Tainted) IsUntrusted() bool { return t.Taint == TaintUntrusted }
