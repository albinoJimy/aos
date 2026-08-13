package planapproval

import (
	"strconv"
	"strings"
)

// SchemaDomain é o DOMÍNIO de versionamento do contrato do PLAN-CARD. O sufixo ".v1"
// fixa a linha MAJOR corrente. É DISTINTO do domínio do approval-card
// ("aos.approval.card.v1", AOS-120): o plan-card opera sobre o GRAFO, o approval-card
// sobre uma tool call — schemas e domínios separados (AC3). Um adaptador de plataforma
// consome o contrato por este domínio + a [PlanCardSchemaVersion] que cada card
// carrega, sem se acoplar à implementação.
const SchemaDomain = "aos.plan.card.v1"

// PlanCardSchemaVersion é a versão SemVer do CONTRATO do plan-card (AC5). É um value
// type imutável e comparável, sem pré-release/build — mantendo a ordenação total e
// determinística (molde: [approvalcard.CardSchemaVersion]). O contrato é versionado
// para consumo com COMPATIBILIDADE explícita: MAJOR diferente é quebra de contrato e é
// rejeitada (fail-closed em [PlanCard.Validate]).
type PlanCardSchemaVersion struct {
	Major int
	Minor int
	Patch int
}

// CurrentVersion é a versão corrente do contrato publicada por este módulo. Cada
// [PlanCard] carimba a sua própria versão; a validação rejeita um MAJOR incompatível.
//
// 1.1.0 (DEF-274, ADR-022 §2.4(5)/§5) — MINOR: o cartão ganhou `node_extensions`, a
// projecção das três extensões declarativas (papel, arestas condicionais, contratos de
// payload). É ADITIVO em toda a linha: campo novo, `omitempty`, NIL quando nenhum nó
// declara extensões — nenhum campo foi removido e nenhum mudou de significado. Um
// leitor 1.0.0 continua a interpretar o cartão inteiro (ignora o campo que não
// conhece), e um cartão de wire 1.0.0 continua a desserializar aqui sem migração:
// `Compatible` decide por MAJOR e o MAJOR não mexeu.
//
// PORQUE O MINOR SOBE MESMO ASSIM (o argumento, não a formalidade). Sem o bump, dois
// binários carimbavam AMBOS 1.0.0 e discordavam sobre o que o cartão MOSTRA — e a
// diferença não é cosmética: é a diferença entre um humano que vê quem verifica quem e
// sob que condição cada ramo entra, e um que aprova um organigrama incompleto. O
// carimbo existe para identificar o contrato apresentado ao aprovador; um contrato que
// passa a descrever a execução com fidelidade diferente TEM de se distinguir no
// carimbo, senão a versão deixa de identificar o que foi aprovado — que é precisamente
// o que ADR-012 e a auditabilidade da decisão exigem dela. O caso «adição
// retrocompatível» é literalmente a definição de MINOR neste ficheiro
// (ver [ChangeMinor]); molde: `plan.CurrentPlanVersion` 1.0.0→1.1.0 em AOS-270.
var CurrentVersion = PlanCardSchemaVersion{Major: 1, Minor: 1, Patch: 0}

// ParsePlanCardSchemaVersion aceita ESTRITAMENTE "X.Y.Z" com X,Y,Z inteiros
// não-negativos. Fail-closed: qualquer outra forma devolve [ErrInvalidSchemaVersion]
// (nunca um zero-value silencioso) — um card que não carimbe a versão correctamente é
// recusado.
func ParsePlanCardSchemaVersion(s string) (PlanCardSchemaVersion, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return PlanCardSchemaVersion{}, ErrInvalidSchemaVersion
	}
	var v PlanCardSchemaVersion
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" {
			return PlanCardSchemaVersion{}, ErrInvalidSchemaVersion
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return PlanCardSchemaVersion{}, ErrInvalidSchemaVersion
		}
		*dst[i] = n
	}
	return v, nil
}

// String devolve a forma canónica "X.Y.Z". Estável e determinística — é o valor que
// vai no campo schema_version de cada plan-card no wire.
func (v PlanCardSchemaVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// Compare devolve -1/0/+1 conforme v é menor/igual/maior que o. Ordenação total
// (precedência MAJOR > MINOR > PATCH).
func (v PlanCardSchemaVersion) Compare(o PlanCardSchemaVersion) int {
	switch {
	case v.Major != o.Major:
		return sign(v.Major - o.Major)
	case v.Minor != o.Minor:
		return sign(v.Minor - o.Minor)
	case v.Patch != o.Patch:
		return sign(v.Patch - o.Patch)
	default:
		return 0
	}
}

// Equal indica se v e o são a mesma versão.
func (v PlanCardSchemaVersion) Equal(o PlanCardSchemaVersion) bool { return v.Compare(o) == 0 }

// Compatible indica se um card na versão o pode ser interpretado por um leitor na
// versão v: MESMO MAJOR. Uma diferença de MAJOR é quebra de contrato (fail-closed) — o
// card é rejeitado. Diferenças de MINOR/PATCH são retrocompatíveis (campos aditivos).
func (v PlanCardSchemaVersion) Compatible(o PlanCardSchemaVersion) bool {
	return v.Major == o.Major
}

// ChangeKind classifica a natureza de uma mudança de versão do contrato. Só
// [ChangeMajor] é uma quebra que exige uma nova linha de adaptadores.
type ChangeKind int

const (
	// ChangeNone — sem mudança (from == to).
	ChangeNone ChangeKind = iota
	// ChangePatch — correcção sem impacto de forma (retrocompatível).
	ChangePatch
	// ChangeMinor — adição retrocompatível (novo campo opcional).
	ChangeMinor
	// ChangeMajor — quebra de contrato (adaptadores têm de migrar).
	ChangeMajor
)

// String implementa fmt.Stringer para diagnóstico legível.
func (k ChangeKind) String() string {
	switch k {
	case ChangeNone:
		return "none"
	case ChangePatch:
		return "patch"
	case ChangeMinor:
		return "minor"
	case ChangeMajor:
		return "major"
	default:
		return "unknown"
	}
}

// Classify devolve a natureza da mudança de from para to. Simétrica quanto à
// componente que difere: uma alteração do MAJOR em qualquer direcção é sempre
// [ChangeMajor] (mesmo um downgrade é quebra de contrato).
func Classify(from, to PlanCardSchemaVersion) ChangeKind {
	switch {
	case from.Major != to.Major:
		return ChangeMajor
	case from.Minor != to.Minor:
		return ChangeMinor
	case from.Patch != to.Patch:
		return ChangePatch
	default:
		return ChangeNone
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
