package progresssurface

// DefaultThreshold é o limiar por omissão do prompt de exaustão graciosa: ~80% do
// orçamento consumido (AC2/AC5). Configurável via WithThreshold.
const DefaultThreshold = 0.80

// ExhaustionOption é a escolha do utilizador no prompt de exaustão graciosa (AC2). São as
// TRÊS opções do plano: estender, resumir-e-parar, abortar.
type ExhaustionOption int

const (
	// OptionUnset é a ausência de escolha (valor-zero). Não é uma decisão válida a
	// resolver — o caminho sem resposta vai por OnPromptTimeout → Degrader.
	OptionUnset ExhaustionOption = iota
	// OptionExtend PEDE ao controlo mais headroom (delegado ao BudgetExtender). A
	// superfície NÃO estende o orçamento por si (AC3).
	OptionExtend
	// OptionSummarizeStop pede para resumir o trabalho e parar graciosamente — a decisão
	// devolve-se ao orquestrador.
	OptionSummarizeStop
	// OptionAbort aborta o run — a decisão devolve-se ao orquestrador.
	OptionAbort
)

// String devolve o rótulo estável da opção (para spans/telemetria, sem segredos).
func (o ExhaustionOption) String() string {
	switch o {
	case OptionExtend:
		return "extend"
	case OptionSummarizeStop:
		return "summarize_stop"
	case OptionAbort:
		return "abort"
	default:
		return "unset"
	}
}

// PromptOptions são as TRÊS opções apresentadas, sempre nesta ordem (estável para a UI e
// para os testes). Devolve uma cópia nova a cada chamada — o chamador não partilha o slice.
func PromptOptions() []ExhaustionOption {
	return []ExhaustionOption{OptionExtend, OptionSummarizeStop, OptionAbort}
}

// PromptState é o ciclo de vida do prompt de exaustão.
type PromptState int

const (
	// PromptIdle — abaixo do limiar, sem prompt.
	PromptIdle PromptState = iota
	// PromptWarned — o aviso ~80% foi sinalizado (reservado para a fase de aviso).
	PromptWarned
	// PromptPrompting — o prompt está apresentado, à espera da escolha das 3 opções.
	PromptPrompting
	// PromptResolved — a escolha foi resolvida (ou a degradação aplicada por timeout).
	PromptResolved
)

// String devolve o rótulo estável do estado do prompt.
func (s PromptState) String() string {
	switch s {
	case PromptWarned:
		return "warned"
	case PromptPrompting:
		return "prompting"
	case PromptResolved:
		return "resolved"
	default:
		return "idle"
	}
}

// ExhaustionPrompt é o prompt apresentado quando a fracção consumida atinge o limiar. Traz
// a fracção corrente, o limiar que a disparou e as TRÊS opções.
type ExhaustionPrompt struct {
	// Fraction é a fracção consumida que disparou o prompt.
	Fraction float64
	// Threshold é o limiar configurado que foi atingido.
	Threshold float64
	// Options são as 3 opções (extend/summarize_stop/abort), nesta ordem.
	Options []ExhaustionOption
}

// validThreshold indica se t é um limiar de fracção válido (0 < t < 1). Um limiar fora de
// gama (<=0 ou >=1) é rejeitado — WithThreshold cai no DefaultThreshold (fail-closed).
func validThreshold(t float64) bool { return t > 0 && t < 1 }
