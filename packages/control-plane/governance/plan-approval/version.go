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
var CurrentVersion = PlanCardSchemaVersion{Major: 1, Minor: 0, Patch: 0}

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
