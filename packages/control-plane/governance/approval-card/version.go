// Package approvalcard é o MODELO CANÓNICO do approval-card (AOS-120, EPIC-12): a
// estrutura de APRESENTAÇÃO que resolve o EFEITO CONCRETO de uma acção escalada,
// exige dual-control para irreversíveis, e DEVOLVE a decisão ao gate HITL de EPIC-09
// para assinatura e enforcement fail-closed.
//
// É uma camada de APRESENTAÇÃO que COMPÕE — não reimplementa:
//   - a decisão de risco pertence a AOS-074 (o card LÊ Class/Irreversible, não
//     classifica);
//   - a assinatura, autoridade, anti-replay, 4-eyes e audit pertencem a AOS-095 (o
//     card DEVOLVE a decisão à porta [risk.ConfirmationChannel], não assina);
//   - a ausência de segredos/PII no preview pertence a AOS-091 (o card aplica o
//     [redaction.Engine] e prova [Engine.Scan] == []).
//
// O ÚNICO enforcement NOVO é a regra dual-control approver_1 != approver_2 para
// acções irreversíveis (inexistente em AOS-095, cujo 4-eyes só garante approver !=
// requester).
package approvalcard

import (
	"strconv"
	"strings"
)

// SchemaDomain é o DOMÍNIO de versionamento do contrato do approval-card. O sufixo
// ".v1" fixa a linha MAJOR corrente (molde: controlsurface.SchemaDomain de AOS-119,
// "aos.control.surface.v1"). Um adaptador de plataforma (AOS-122) consome o contrato
// por este domínio + a [CardSchemaVersion] que cada card carrega, sem se acoplar à
// implementação.
const SchemaDomain = "aos.approval.card.v1"

// CardSchemaVersion é a versão SemVer do CONTRATO do approval-card (AC5). É um value
// type imutável e comparável, sem pré-release/build — mantendo a ordenação total e
// determinística (molde: controlsurface.ControlSchemaVersion). O contrato é
// versionado para que os adaptadores de AOS-122 o consumam com COMPATIBILIDADE
// explícita: MAJOR diferente é quebra de contrato e é rejeitada (fail-closed em
// [ApprovalCard.Validate]).
type CardSchemaVersion struct {
	Major int
	Minor int
	Patch int
}

// CurrentVersion é a versão corrente do contrato publicada por este módulo. Cada
// [ApprovalCard] carimba a sua própria versão; a validação rejeita um MAJOR
// incompatível.
var CurrentVersion = CardSchemaVersion{Major: 1, Minor: 0, Patch: 0}

// ParseCardSchemaVersion aceita ESTRITAMENTE "X.Y.Z" com X,Y,Z inteiros
// não-negativos. Fail-closed: qualquer outra forma devolve [ErrInvalidSchemaVersion]
// (nunca um zero-value silencioso) — um card que não carimbe a versão correctamente é
// recusado.
func ParseCardSchemaVersion(s string) (CardSchemaVersion, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return CardSchemaVersion{}, ErrInvalidSchemaVersion
	}
	var v CardSchemaVersion
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" {
			return CardSchemaVersion{}, ErrInvalidSchemaVersion
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return CardSchemaVersion{}, ErrInvalidSchemaVersion
		}
		*dst[i] = n
	}
	return v, nil
}

// String devolve a forma canónica "X.Y.Z". Estável e determinística — é o valor que
// vai no campo schema_version de cada card no wire.
func (v CardSchemaVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// Compare devolve -1/0/+1 conforme v é menor/igual/maior que o. Ordenação total
// (precedência MAJOR > MINOR > PATCH).
func (v CardSchemaVersion) Compare(o CardSchemaVersion) int {
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
func (v CardSchemaVersion) Equal(o CardSchemaVersion) bool { return v.Compare(o) == 0 }

// Compatible indica se um card na versão o pode ser interpretado por um leitor na
// versão v: MESMO MAJOR. Uma diferença de MAJOR é quebra de contrato (fail-closed) —
// o card é rejeitado. Diferenças de MINOR/PATCH são retrocompatíveis (campos
// aditivos), pelo que um adaptador mais antigo/novo dentro do mesmo MAJOR continua a
// interoperar.
func (v CardSchemaVersion) Compatible(o CardSchemaVersion) bool {
	return v.Major == o.Major
}

// ChangeKind classifica a natureza de uma mudança de versão do contrato (molde:
// controlsurface.ChangeKind). Só [ChangeMajor] é uma quebra que exige uma nova linha
// de adaptadores.
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
func Classify(from, to CardSchemaVersion) ChangeKind {
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
