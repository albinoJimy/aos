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
// A LINHA 1.x, MINOR A MINOR (a janela de suporte declarada vive em `tecnica/18`
// §3.6.1 e a sua forma executável em `planmigrate.SupportWindow`):
//
//   - 1.0.0 — a linha base do PlanDocument (AOS-230).
//   - 1.1.0 (AOS-270, ADR-022 §2.1) — MINOR: o `Node` ganhou `conditional_on`, campo
//     OPCIONAL e ADITIVO.
//   - 1.2.0 (AOS-271+AOS-272, ADR-022 §2.2/§2.3) — MINOR: o `Node` ganhou `outputs` e
//     `consumes` (campos OPCIONAIS e ADITIVOS) e o literal `verifier` do campo `role`
//     passou a ser RESERVADO, com a semântica de sistema de §2.2 imposta na admissão.
//
// PORQUE O MINOR PERTENCE A QUEM ALARGA O SCHEMA. Sem o bump, dois binários
// carimbavam AMBOS a mesma versão e discordavam sobre o schema aceite: um planeador
// novo emitia `outputs`/`consumes` com `plan_version: 1.1.0` e um nó anterior — que
// passa a verificação de MAJOR — falhava com «unknown field outputs»
// (DisallowUnknownFields). O operador via um erro de schema num documento cuja
// versão declarada era idêntica à suportada, ou seja: o carimbo deixava de
// identificar o schema, que é exactamente o que ADR-012/§3.6 exige dele. O caso
// «aditivo retrocompatível» é literalmente a definição de MINOR neste ficheiro.
//
// E O BUMP É IMPOSTO, não confiado: o `plan_version` é um campo do documento untrusted,
// pelo que um produtor podia carimbar a linha antiga e usar os campos novos à mesma. A
// tabela [schemaFeatures]/[FeatureFloor] deriva o PISO de versão das features que o
// documento USA, e a regra 1 de `planvalidate` recusa um carimbo abaixo desse piso
// (`plan_version_below_features`) — é isso que faz esta justificação realizar-se em vez
// de ficar em prosa.
//
// PORQUE O `role: verifier` ENTRA NO MESMO MINOR, E NÃO NUM MAJOR. A reserva do
// literal não muda campo nenhum: `role` era e continua a ser texto livre, e um
// documento de 1.0.0/1.1.0 que já usasse a palavra `verifier` decodifica exactamente
// como antes. O que muda é o que o sistema IMPÕE a esse nó na admissão — logo é
// aditivo em SIGNIFICADO, e a direcção do risco é a segura (um leitor 1.2.0 vê
// documentos antigos com MAIS regras, nunca com menos). O carimbo continua a ser
// preciso ao MINOR porque é ele que distingue «este documento foi admitido por um
// leitor que impõe §2.2» de «foi admitido por um que só via um rótulo».
//
// PORQUE NÃO É MAJOR (a decisão de AOS-273, escrita onde é lida). MAJOR é «campo
// removido ou semântica alterada». Nenhuma das três extensões de ADR-022 remove um
// campo, muda o tipo de um campo existente ou altera o significado de um documento
// que não use os campos novos. Os três campos novos são `omitempty`, pelo que um
// documento anterior — decodificado por este binário e re-serializado por [Encode] —
// produz OS MESMOS BYTES que produzia antes das extensões existirem: é essa a
// propriedade que sustenta o replay byte-a-byte de `planmigrate` e que o pacote prova
// contra fixtures congeladas das versões anteriores. Inventar um MAJOR para ter uma
// transformação de dados a exercitar seria quebrar a compatibilidade de graça e dar
// trabalho a fingir ao `planmigrate` — a migração REAL desta linha é a ausência de
// migração, e prova-se, não se declara.
var CurrentPlanVersion = PlanVersion{Major: 1, Minor: 2, Patch: 0}

// schemaFeature é UMA extensão do schema com o MINOR em que passou a EXISTIR. A tabela
// abaixo é o outro lado da lista MINOR-a-MINOR de [CurrentPlanVersion]: aquela diz o que
// cada linha ACRESCENTOU; esta diz, para cada acréscimo, qual é o piso de `plan_version`
// que um documento TEM de carimbar para o poder usar.
//
// PORQUE A TABELA EXISTE, E PORQUE VIVE AQUI. Sem ela o bump de MINOR era um carimbo que
// ninguém impunha: o `plan_version` é um campo do PlanDocument — dados UNTRUSTED escritos
// pelo modelo —, [Decode] não olha para ele e a verificação de compatibilidade a jusante
// é por MAJOR. Um produtor podia carimbar `1.0.0` e emitir `outputs`/`consumes`, o leitor
// corrente aceitava (conhece os campos), o plano era aprovado e congelado com esse
// carimbo, e MESES DEPOIS um reader `1.0.0` — retido legitimamente, porque a janela de
// suporte é por MAJOR — falhava o replay com «unknown field outputs»: um erro NÃO
// atribuível por nenhuma das políticas de `planmigrate` ([ErrOutsideSupportWindow],
// ErrRetired, ErrReaderMismatch), no run que essa maquinaria existe para reproduzir. Pior:
// o registo de auditoria dizia que o plano fora admitido sob a linha 1.0.0 quando fora
// admitido por um validador que impõe as regras de §2.2/§2.3 de 1.2.0 — o carimbo mentia
// sobre que conjunto de regras aprovou o plano.
//
// A IMPOSIÇÃO É DO VALIDADOR PURO, não deste pacote: [Decode] valida FORMA e a forma de
// um documento 1.2.0 é a mesma independentemente do que ele carimba. O piso é SEMÂNTICA
// (regra 1 de `planvalidate`, sub-código `plan_version_below_features`) — ver
// [FeatureFloor], que é a função que o validador chama.
//
// O PRÓXIMO MINOR ENTRA AQUI. Uma linha nova nesta tabela (e a sua irmã na lista de
// [CurrentPlanVersion]) é tudo o que um acréscimo de schema precisa para ficar imposto.
type schemaFeature struct {
	// Name é o nome ESTRUTURAL da feature (o campo, ou o literal reservado). É um
	// símbolo fechado, seguro para telemetria — nunca conteúdo do documento.
	Name string
	// Since é o MINOR em que a feature passou a existir: o piso que um documento que a
	// USE tem de carimbar.
	Since PlanVersion
	// Used indica se o nó usa a feature.
	Used func(Node) bool
}

// schemaFeatures é a tabela feature→versão mínima, pela ordem de introdução. A ordem é
// determinística e importa para o desempate de [FeatureFloor] (o primeiro uso encontrado
// que fixa o piso é o que se reporta).
var schemaFeatures = []schemaFeature{
	// 1.1.0 (AOS-270, ADR-022 §2.1).
	{Name: "conditional_on", Since: PlanVersion{Major: 1, Minor: 1, Patch: 0},
		Used: func(n Node) bool { return len(n.ConditionalOn) > 0 }},
	// 1.2.0 (AOS-272, ADR-022 §2.3).
	{Name: "outputs", Since: PlanVersion{Major: 1, Minor: 2, Patch: 0},
		Used: func(n Node) bool { return len(n.Outputs) > 0 }},
	{Name: "consumes", Since: PlanVersion{Major: 1, Minor: 2, Patch: 0},
		Used: func(n Node) bool { return len(n.Consumes) > 0 }},
	// 1.2.0 (AOS-271, ADR-022 §2.2) — o literal RESERVADO do campo `role`. Não é um
	// campo novo: é o significado que o sistema passou a IMPOR a esse nó.
	{Name: "role_verifier", Since: PlanVersion{Major: 1, Minor: 2, Patch: 0},
		Used: Node.IsVerifier},
}

// FeatureUse é o uso CONCRETO que fixa o piso de versão de um documento: que feature, em
// que nó, desde que linha. Content-free — `Feature` é um símbolo da tabela fechada e
// `NodeID` é o identificador estrutural que o veredicto já propaga.
type FeatureUse struct {
	Feature string
	Since   PlanVersion
	NodeID  string
}

// FeatureFloor devolve o PISO de `plan_version` exigido pelas features que doc USA — a
// MAIOR das versões de introdução das features presentes — e o uso que o fixa. Um
// documento que não use nenhuma extensão devolve o zero-value {0,0,0} (piso vazio: todas
// as linhas o admitem) e um [FeatureUse] vazio.
//
// Pura e determinística: itera os nós pela ordem do slice e as features pela ordem da
// tabela; em empate de versão vence o PRIMEIRO uso encontrado. É esta função que dá à
// regra 1 de `planvalidate` a fronteira entre «o que o documento DECLARA ser» e «o que o
// documento USA» — sem ela o carimbo não identifica o schema.
func FeatureFloor(doc PlanDocument) (PlanVersion, FeatureUse) {
	var floor PlanVersion
	var use FeatureUse
	for _, n := range doc.Nodes {
		for _, f := range schemaFeatures {
			if !f.Used(n) {
				continue
			}
			if f.Since.Compare(floor) > 0 {
				floor = f.Since
				use = FeatureUse{Feature: f.Name, Since: f.Since, NodeID: n.NodeID}
			}
		}
	}
	return floor, use
}

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
