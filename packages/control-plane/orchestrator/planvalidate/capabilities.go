package planvalidate

import "github.com/aos-ref/kernel/reference-monitor/risk"

// Capability é UMA entrada do snapshot de capabilities pinado: uma ferramenta
// conhecida do REG, com a versão e o digest a que está fixada e o seu estado de
// admissibilidade. É DADO PINADO (trusted): faz parte do input imutável do
// validador, não é obtida por lookup vivo.
type Capability struct {
	// Name+Version+Digest são a referência PINADA (tecnica/18 §3.3). Uma [plan.ToolRef]
	// só resolve se casar EXACTAMENTE nos três.
	Name    string
	Version string
	Digest  string
	// Deprecated marca uma capability retirada: presente no snapshot para
	// diagnóstico, mas INADMISSÍVEL para planeamento novo — resolve para rejeição,
	// nunca para trimming silencioso.
	Deprecated bool
	// Admissible é a decisão de política do snapshot (allowlist): se falso, a
	// ferramenta existe mas não é admitida para este planeamento. Fail-closed.
	Admissible bool

	// Sensitivity/Egress/Reversibility são os EIXOS DE RISCO PINADOS da capability
	// (AOS-232, regra 6): a propriedade INERENTE da ferramenta que o classificador
	// SA-ROC ([risk.Classify]) consome para DERIVAR o piso de risco de cada nó. São
	// dados TRUSTED do snapshot — a derivação NÃO lê o rótulo do LLM.
	//
	// FAIL-CLOSED PELO TIPO: os valores-zero de cada eixo são os mais perigosos
	// ([risk.SensitivityUnknown]→sensível, [risk.EgressUnknown]→externo,
	// [risk.ReversibilityUnknown]→irreversível). Uma capability pinada SEM eixos de
	// risco explícitos deriva `danger` — uma ferramenta por classificar trata-se como
	// perigosa, nunca como segura.
	Sensitivity   risk.Sensitivity
	Egress        risk.Egress
	Reversibility risk.Reversibility

	// Mutation é o QUARTO eixo pinado (AOS-409, DEF-275): a ferramenta altera estado?
	// Não é um eixo do classificador SA-ROC ([risk.Classify] tem três, ADR-013) — é lido
	// por este pacote, em dois sítios e com UMA definição: [IsEffectTool] (fronteira
	// read-only de ADR-022 §2.2) e [deriveNodeAction] (regra 6, onde um mutador entra
	// no classificador como IRREVERSÍVEL — decisão R1 do AOS-409).
	//
	// FAIL-CLOSED PELO TIPO, como os outros três: o valor-zero é [MutationUnknown], que
	// conta como mutador. Uma capability construída sem este campo — um literal Go, ou a
	// capability de eixos-zero que [resolveCaps] fabrica para uma tool não resolvida —
	// é tratada como uma ferramenta que escreve.
	Mutation Mutation
}

// Mutation é o eixo de MUTAÇÃO de uma capability pinada (AOS-409): se a ferramenta
// altera estado — local ou remoto, desfazível ou não.
//
// É ortogonal ao egress e à reversibilidade, e é por isso que existe: uma escrita local
// com undo é `EgressNone` + `Reversible`, e sem este eixo contava como leitura — um
// verificador podia pinar a tool que mexe no que revê, e um consumidor com autoridade de
// escrita não contava como privilegiado para a regra de taint (DEF-275).
type Mutation uint8

const (
	// MutationUnknown é o valor-zero: mutação por declarar. FAIL-CLOSED — conta como
	// mutador ([Mutation.Mutates] devolve true). Não é «inócua por omissão».
	MutationUnknown Mutation = iota
	// MutationNone declara que a ferramenta NÃO altera estado nenhum (só lê/computa). É
	// o ÚNICO valor que [Mutation.Mutates] trata como inócuo, e tem de ser declarado.
	MutationNone
	// MutationMutates declara que a ferramenta altera estado.
	MutationMutates
)

// Mutates diz se o eixo conta como mutador. FAIL-CLOSED: tudo o que não seja
// explicitamente [MutationNone] — incluindo o valor-zero e qualquer valor fora do enum —
// muta. É a mesma forma de [risk.Reversibility.IsIrreversible].
func (m Mutation) Mutates() bool { return m != MutationNone }

// String devolve o nome do eixo, o mesmo vocabulário do ficheiro do snapshot pinado
// (`none`/`mutates`/`unknown`). Um valor fora do enum diz-se `unknown`.
func (m Mutation) String() string {
	switch m {
	case MutationNone:
		return "none"
	case MutationMutates:
		return "mutates"
	default:
		return "unknown"
	}
}

// Snapshot é o conjunto PINADO de capabilities contra o qual a regra 3 resolve as
// `tools[]` do plano. É passado como ARGUMENTO ao validador — nunca um lookup vivo
// (determinismo: o mesmo snapshot dá sempre o mesmo veredicto).
//
// Hash liga-se ao `capabilities_hash` de [plan.PlannerMeta] (AOS-243): este pacote
// NÃO o computa; apenas confere, na regra 1, que o snapshot recebido é aquele
// contra o qual o plano foi carimbado (binding fail-closed).
type Snapshot struct {
	Hash  string
	Tools []Capability
}

// toolKey é a chave de resolução (nome+versão). O digest é conferido à parte para
// distinguir "versão desconhecida" de "digest não bate" no diagnóstico allowlisted.
type toolKey struct {
	name    string
	version string
}

// index constrói o mapa de resolução do snapshot. Em duplicados (nome+versão
// repetidos no snapshot pinado) o PRIMEIRO vence — determinístico e independente da
// ordem de iteração de mapas. É uma função pura sobre o snapshot recebido.
func (s Snapshot) index() map[toolKey]Capability {
	idx := make(map[toolKey]Capability, len(s.Tools))
	for _, c := range s.Tools {
		k := toolKey{name: c.Name, version: c.Version}
		if _, seen := idx[k]; seen {
			continue // primeiro vence: resolução estável
		}
		idx[k] = c
	}
	return idx
}
