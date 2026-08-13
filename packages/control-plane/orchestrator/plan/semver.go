package plan

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// ErrInvalidPlanVersion — o `plan_version` não é um SemVer "X.Y.Z" estrito de
// inteiros não-negativos. Fail-closed: um plano que não carimbe correctamente a
// sua versão de schema é recusado (nunca um zero-value silencioso).
var ErrInvalidPlanVersion = errors.New("plan: plan_version invalido (exige SemVer estrito X.Y.Z)")

// PlanVersion é a versão SemVer do SCHEMA do PlanDocument (§3.6), distinta da
// `prompt_version` e do `capabilities_hash` em [PlannerMeta]. É um value type
// imutável e comparável, sem pré-release/build — mantendo ordenação total e
// determinística (molde: approvalcard.CardSchemaVersion de AOS-120).
//
// Semântica SemVer (§3.6): MAJOR = mudança que quebra (campo removido ou
// semântica alterada); MINOR = aditivo retrocompatível; PATCH = clarificação. No
// wire serializa-se como a string canónica "X.Y.Z" (ver [PlanVersion.MarshalJSON]).
//
// O zero-value {0,0,0} é o SENTINELA de "não carimbado" e NÃO é uma versão de
// plano admissível — [Decode] rejeita-o fail-closed.
type PlanVersion struct {
	Major int
	Minor int
	Patch int
}

// CurrentPlanVersion é a linha de schema corrente que este módulo publica. As
// propostas novas saem sempre na versão corrente (§3.6) e validam-se contra o
// schema corrente. Bump governado (ADR-012) actualiza esta constante.
//
// 1.1.0 (AOS-270, ADR-022 §2.1) — MINOR: o `Node` ganhou `conditional_on`, campo
// OPCIONAL e ADITIVO. Documentos 1.0.0 continuam a decodificar sem alteração
// (o campo é `omitempty` e a sua ausência significa «sem condições»), pelo que
// não há migração de dados a fazer — `planmigrate`, a janela de suporte e os
// golden-sets são AOS-273.
//
// PORQUE O MINOR PERTENCE A QUEM ALARGA O SCHEMA. Sem o bump, dois binários
// carimbavam AMBOS 1.0.0 e discordavam sobre o schema aceite: um planeador novo
// emitia `conditional_on` com `plan_version: 1.0.0` e um nó anterior — que passa a
// verificação de MAJOR — falhava com «unknown field conditional_on»
// (DisallowUnknownFields). O operador via um erro de schema num documento cuja
// versão declarada era idêntica à suportada, ou seja: o carimbo deixava de
// identificar o schema, que é exactamente o que ADR-012/§3.6 exige dele. O caso
// «aditivo retrocompatível» é literalmente a definição de MINOR neste ficheiro.
var CurrentPlanVersion = PlanVersion{Major: 1, Minor: 1, Patch: 0}

// ParsePlanVersion aceita ESTRITAMENTE "X.Y.Z" com X,Y,Z inteiros não-negativos,
// na forma CANÓNICA exacta — sem whitespace envolvente nem interno. Fail-closed:
// qualquer outra forma (componentes a mais/menos, prefixo "v", não-numérico,
// negativo, vazio, com espaços) devolve [ErrInvalidPlanVersion]. Não há
// normalização silenciosa: o wire de um contrato estrito é a string canónica que
// [PlanVersion.String] produz e nada mais.
func ParsePlanVersion(s string) (PlanVersion, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return PlanVersion{}, ErrInvalidPlanVersion
	}
	var v PlanVersion
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		// Exige um componente de DÍGITOS puros: strconv.Atoi por si só aceitaria
		// sinais espúrios ("+1"), pelo que a estritez fail-closed precisa deste
		// guarda antes da conversão. Rejeita "", "+1", "-1", "1a", " 1", etc.
		if !isASCIIDigits(p) {
			return PlanVersion{}, ErrInvalidPlanVersion
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return PlanVersion{}, ErrInvalidPlanVersion
		}
		*dst[i] = n
	}
	return v, nil
}

// IsZero indica se v é o sentinela {0,0,0} de "não carimbado".
func (v PlanVersion) IsZero() bool { return v == PlanVersion{} }

// String devolve a forma canónica "X.Y.Z" — o valor que vai no campo
// `plan_version` do documento no wire.
func (v PlanVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// MarshalJSON serializa a versão como a string canónica "X.Y.Z". Mantém o
// contrato de wire estável e legível — o `plan_version` é uma string, não um
// objecto {major,minor,patch}.
func (v PlanVersion) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.String())
}

// UnmarshalJSON desserializa a partir da string canónica, delegando a validação
// estrita em [ParsePlanVersion] — fail-closed. Recusa também um `plan_version`
// que não seja uma string JSON (ex.: número ou objecto).
func (v *PlanVersion) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return ErrInvalidPlanVersion
	}
	parsed, err := ParsePlanVersion(s)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

// Compare devolve -1/0/+1 conforme v é menor/igual/maior que o. Ordenação total
// com precedência MAJOR > MINOR > PATCH.
func (v PlanVersion) Compare(o PlanVersion) int {
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
func (v PlanVersion) Equal(o PlanVersion) bool { return v.Compare(o) == 0 }

// Compatible indica se um plano na versão o pode ser lido por um materializador na
// versão v: MESMO MAJOR. Uma diferença de MAJOR é quebra de contrato (fail-closed)
// — o plano é invalidado e volta a planeamento (§3.6). MINOR/PATCH são
// retrocompatíveis (aditivo/clarificação).
func (v PlanVersion) Compatible(o PlanVersion) bool {
	return v.Major == o.Major
}

// isASCIIDigits indica se s é não-vazio e composto SÓ por dígitos ASCII 0-9.
func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
