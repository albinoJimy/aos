// Package schema define o VERSIONAMENTO de schema de memória do AOS (AOS-041): a
// versão SemVer de schema POR CLASSE (episódica/semântica/procedural/de trabalho)
// e a disciplina de evolução monótona sobre a qual assenta o motor de migrações
// expand/contract (ver o subpacote migrations).
//
// Cada registo de memória já carrega o seu schema_version obrigatório (AOS-035,
// domain.Metadata); este pacote dá a esse campo uma SEMÂNTICA: uma classe tem uma
// versão de schema corrente em SemVer, e a sua evolução é estritamente monótona —
// uma versão que não seja mais recente que a corrente é REJEITADA (fail-closed,
// espelhando o hot-reload versionado da política do scheduler, AOS-030).
//
// O SemVer é interpretado em termos de COMPATIBILIDADE DE CONTRATO (ADR-012,
// tecnica/11): MAJOR é quebra de contrato (exige migração + eval-gate), MINOR é
// adição retrocompatível, PATCH é correcção sem impacto de forma. É esta
// classificação (ver Classify/ChangeKind) que o motor de migração consulta para
// decidir se uma mudança precisa de passar pela porta de aprovação.
package schema

import (
	"strconv"
	"strings"
)

// Version é uma versão de schema em SemVer numérico (MAJOR.MINOR.PATCH). É um
// value type imutável e comparável; sem pré-release/build (não usados nos
// artefactos do AOS), o que mantém a ordenação total e determinística.
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion aceita ESTRITAMENTE "X.Y.Z" com X,Y,Z inteiros não-negativos.
// Fail-closed: qualquer outra forma devolve ErrInvalidVersion (nunca um zero-value
// silencioso). Reutiliza o padrão de parsing SemVer do scheduler (AOS-030).
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

// ChangeKind classifica a NATUREZA de uma mudança de schema em termos de contrato
// (ADR-012). É o discriminador que o motor de migração usa para decidir o gate:
// só MAJOR (quebra de contrato) exige eval-gate/aprovação.
type ChangeKind int

const (
	// ChangeNone — sem mudança de versão (from == to).
	ChangeNone ChangeKind = iota
	// ChangePatch — correcção sem impacto de forma de registo (retrocompatível).
	ChangePatch
	// ChangeMinor — adição retrocompatível (novo campo opcional, novo índice).
	ChangeMinor
	// ChangeMajor — quebra de contrato (remove/altera a forma de um registo). Exige
	// migração e re-aprovação por eval-gate.
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

// Classify devolve a natureza da mudança de from para to. É simétrica quanto à
// componente que difere: uma alteração do MAJOR (em qualquer direcção) é sempre
// ChangeMajor — mesmo um downgrade de MAJOR é uma quebra de contrato que tem de
// passar pelo gate (fail-closed). A precedência é MAJOR > MINOR > PATCH.
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
