// Package domain define o MODELO de domínio do Skill/Tool Registry (REG, AOS-045):
// a entrada versionada de catálogo, os três tipos de artefacto distinguíveis
// (skill / tool / servidor MCP), o contrato público de capability, a proveniência
// (com estado de confiança TOFU) e a máquina de estados do ciclo de vida
// (staging → active → deprecated/revoked). É um pacote PURO (só stdlib): não
// conhece o Event Store nem o tracing — a persistência append-only e a orquestração
// vivem no pacote registry.
//
// Todas as invariantes deste pacote são FAIL-CLOSED: uma versão mal-formada, uma
// transição de estado inválida ou uma tentativa de saltar directamente para active
// sem verificação são sempre REJEITADAS, nunca aceites silenciosamente.
package domain

import (
	"strconv"
	"strings"
)

// Version é uma versão de artefacto em SemVer numérico (MAJOR.MINOR.PATCH). É um
// value type imutável e comparável; sem pré-release/build (não usados nos
// artefactos do AOS), o que mantém a ordenação total e determinística. Espelha o
// padrão SemVer já estabelecido no repo (platform/memory/schema, ADR-012).
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion aceita ESTRITAMENTE "X.Y.Z" com X,Y,Z inteiros não-negativos.
// Fail-closed: qualquer outra forma (incluindo referências flutuantes como
// "latest"/"main", intervalos ou pré-releases) devolve ErrInvalidVersion — nunca
// um zero-value silencioso.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return Version{}, ErrInvalidVersion
	}
	var v Version
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" {
			return Version{}, ErrInvalidVersion
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, ErrInvalidVersion
		}
		*dst[i] = n
	}
	return v, nil
}

// String devolve a forma canónica "X.Y.Z". Estável e determinística.
func (v Version) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// IsZero indica se v é o value-zero (0.0.0). O REG trata 0.0.0 como a versão
// NÃO-ESPECIFICADA (o sentinela de "sem versão") e recusa resolvê-la: a resolução
// é sempre por versão pinada exacta, nunca por uma referência ausente/flutuante.
func (v Version) IsZero() bool { return v == Version{} }

// Compare devolve -1/0/+1 conforme v é menor/igual/maior que o. Ordenação total.
func (v Version) Compare(o Version) int {
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

// Less indica se v é estritamente anterior a o.
func (v Version) Less(o Version) bool { return v.Compare(o) < 0 }

// Equal indica se v e o são a mesma versão.
func (v Version) Equal(o Version) bool { return v.Compare(o) == 0 }

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

// ChangeKind classifica a NATUREZA de uma mudança entre duas versões em termos de
// contrato (ADR-012): só MAJOR (quebra de contrato) justifica um novo estado de
// confiança TOFU e re-aprovação (a lógica avançada de SemVer é AOS-052; aqui o
// classificador básico serve de fundação).
type ChangeKind int

const (
	// ChangeNone — sem mudança de versão (from == to).
	ChangeNone ChangeKind = iota
	// ChangePatch — correcção sem alteração de contrato (retrocompatível).
	ChangePatch
	// ChangeMinor — adição retrocompatível.
	ChangeMinor
	// ChangeMajor — quebra de contrato. Exige re-aprovação.
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

// Classify devolve a natureza da mudança de from para to. Uma alteração do MAJOR
// (em qualquer direcção) é sempre ChangeMajor — mesmo um downgrade é uma quebra de
// contrato que tem de passar pelo gate (fail-closed). Precedência MAJOR>MINOR>PATCH.
func Classify(from, to Version) ChangeKind {
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
