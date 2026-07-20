package controlsurface

import (
	"strconv"
	"strings"
)

// SchemaDomain é o DOMÍNIO de versionamento do contrato da superfície de controlo. O
// sufixo ".v1" fixa a linha MAJOR corrente (molde: o domínio de assinatura
// "aos.policy.bundle.v1" do PDP e o versionamento de schema de memória, AOS-041). Um
// adaptador de plataforma (AOS-122) consome o contrato por este domínio + a
// [ControlSchemaVersion] que cada mensagem carrega, sem se acoplar à implementação.
const SchemaDomain = "aos.control.surface.v1"

// ControlSchemaVersion é a versão SemVer do CONTRATO da superfície de controlo
// (AC5). É um value type imutável e comparável, sem pré-release/build — mantendo a
// ordenação total e determinística (molde: platform/memory/schema.Version). O
// contrato é versionado para que adaptadores de plataforma o consumam com
// COMPATIBILIDADE explícita: MAJOR diferente é quebra de contrato e é rejeitada
// (fail-closed em [ControlMessage.Validate]).
type ControlSchemaVersion struct {
	Major int
	Minor int
	Patch int
}

// CurrentVersion é a versão corrente do contrato publicada por este módulo. Cada
// [ControlMessage] carrega a sua própria versão; a superfície valida-a contra esta.
var CurrentVersion = ControlSchemaVersion{Major: 1, Minor: 0, Patch: 0}

// ParseControlSchemaVersion aceita ESTRITAMENTE "X.Y.Z" com X,Y,Z inteiros
// não-negativos. Fail-closed: qualquer outra forma devolve [ErrInvalidSchemaVersion]
// (nunca um zero-value silencioso) — um adaptador que não carimbe a versão
// correctamente é recusado.
func ParseControlSchemaVersion(s string) (ControlSchemaVersion, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return ControlSchemaVersion{}, ErrInvalidSchemaVersion
	}
	var v ControlSchemaVersion
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" {
			return ControlSchemaVersion{}, ErrInvalidSchemaVersion
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return ControlSchemaVersion{}, ErrInvalidSchemaVersion
		}
		*dst[i] = n
	}
	return v, nil
}

// String devolve a forma canónica "X.Y.Z". Estável e determinística — é o valor que
// vai no campo schema_version de cada mensagem no wire.
func (v ControlSchemaVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// Compare devolve -1/0/+1 conforme v é menor/igual/maior que o. Ordenação total
// (precedência MAJOR > MINOR > PATCH).
func (v ControlSchemaVersion) Compare(o ControlSchemaVersion) int {
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
func (v ControlSchemaVersion) Less(o ControlSchemaVersion) bool { return v.Compare(o) < 0 }

// Equal indica se v e o são a mesma versão.
func (v ControlSchemaVersion) Equal(o ControlSchemaVersion) bool { return v.Compare(o) == 0 }

// Compatible indica se uma mensagem na versão o pode ser interpretada por uma
// superfície na versão v: MESMO MAJOR. Uma diferença de MAJOR é quebra de contrato
// (fail-closed) — a mensagem é rejeitada. Diferenças de MINOR/PATCH são
// retrocompatíveis (campos aditivos), pelo que um adaptador mais antigo/novo dentro
// do mesmo MAJOR continua a interoperar.
func (v ControlSchemaVersion) Compatible(o ControlSchemaVersion) bool {
	return v.Major == o.Major
}

// ChangeKind classifica a natureza de uma mudança de versão do contrato (molde:
// schema.ChangeKind). Só [ChangeMajor] é uma quebra que exige uma nova linha de
// adaptadores.
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
// [ChangeMajor] (mesmo um downgrade é quebra de contrato). Precedência MAJOR > MINOR
// > PATCH.
func Classify(from, to ControlSchemaVersion) ChangeKind {
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
