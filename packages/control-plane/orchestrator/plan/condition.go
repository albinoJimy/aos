package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// condition.go — A GRAMÁTICA DAS ARESTAS CONDICIONAIS (AOS-270, ADR-022 §2.1).
//
// O ADR congela o QUE: «expressão declarativa em SUBCONJUNTO FECHADO do schema
// (sem código arbitrário), avaliada deterministicamente sobre o RESULTADO
// REGISTADO do nó de origem (veredicto, métricas declaradas, estado terminal)».
// Este ficheiro é o COMO — a gramática concreta que o ADR deixou explicitamente
// fora (§4) e que este ticket fixa.
//
// PORQUE É UM SUBCONJUNTO FECHADO (o argumento, não a afirmação):
//
//  1. FINITUDE. Só há três OBSERVÁVEIS ([ConditionSubject]) e seis OPERADORES
//     ([ConditionOp]), ambos enums fechados. Os operandos são ou um símbolo de um
//     enum fechado ([ConditionEnum]) ou um INTEIRO de máquina — nunca texto livre,
//     nunca vírgula flutuante (uma comparação sobre float não é reproduzível
//     byte-a-byte, o que partiria ADR-010).
//  2. NÃO-RECURSIVIDADE. A condição é uma CONJUNÇÃO PLANA ([ConditionalEdge.When]:
//     uma lista de [Predicate] ligados por E). Não há aninhamento, parênteses,
//     negação estrutural nem disjunção — logo não há profundidade a explorar, não
//     há avaliação parcial ambígua e a aridade é limitada
//     ([maxPredicatesPerEdge]). A DISJUNÇÃO foi deliberadamente deixada de fora:
//     ver a nota de MONOTONIA abaixo.
//  3. AUSÊNCIA DE CÓDIGO. Não há chamadas, aritmética, indexação, referências a
//     outros nós que não a ORIGEM declarada da aresta, nem qualquer forma de
//     interpolação. Um predicado não pode *computar* — só *comparar*.
//  4. TOTALIDADE. Cada predicado bem-formado devolve exactamente `true` ou `false`
//     sobre um resultado registado; um observável AUSENTE (métrica não declarada,
//     veredicto inexistente) devolve `false` — fail-closed na direcção de NÃO
//     despachar trabalho (a única direcção segura: não-despachar não tem efeito).
//
// MONOTONIA (a invariante de segurança que a ausência de disjunção compra). Todas
// as conjunções são E: entre os predicados de uma aresta E entre as arestas
// condicionais de um nó. Consequência: acrescentar uma aresta condicional só pode
// tornar um nó MENOS despachável, nunca mais. Uma aresta condicional NUNCA relaxa
// um `depends_on` nem substitui o gate — é um travão adicional, jamais um atalho.
// Um plano hostil não consegue usar condições para *abrir* caminho, só para fechar.
//
// FRONTEIRA. Este ficheiro define e valida a FORMA da gramática (como o resto de
// [plan]). NÃO avalia (isso é o despachante sem estado, `plandispatch`, ADR-022 §5)
// e NÃO decide aciclicidade (isso é o validador puro `planvalidate`, que REUTILIZA
// o DAG de AOS-025). Zero dependências fora da stdlib.

// ConditionSubject é O QUE se observa do RESULTADO REGISTADO do nó de origem.
// Enum FECHADO — os três observáveis que ADR-022 §2.1 nomeia, e mais nenhum.
type ConditionSubject string

const (
	// SubjectTerminalState observa o ESTADO TERMINAL da origem (concluída vs falhada).
	// É o observável que suporta o ramo de recuperação («se X falhar, corre Y»).
	SubjectTerminalState ConditionSubject = "terminal_state"
	// SubjectVerdict observa o VEREDICTO estruturado registado pela origem
	// (pass/fail). É o observável dos ramos de QUALIDADE. O veredicto tipado do papel
	// verificador é AOS-271; aqui fixa-se apenas o símbolo que a condição consome.
	SubjectVerdict ConditionSubject = "verdict"
	// SubjectMetric observa UMA métrica DECLARADA do resultado, por nome, com valor
	// INTEIRO. Sem float: determinismo de replay (ADR-010).
	SubjectMetric ConditionSubject = "metric"
)

// Valid indica se s é um dos observáveis do enum fechado. Fail-closed: o vazio NÃO
// é admissível (ao contrário de [RiskClass], onde a ausência é advisory).
func (s ConditionSubject) Valid() bool {
	switch s {
	case SubjectTerminalState, SubjectVerdict, SubjectMetric:
		return true
	default:
		return false
	}
}

// ConditionOp é o operador de comparação. Enum FECHADO de seis: igualdade para
// símbolos, ordem total para inteiros.
type ConditionOp string

const (
	OpEq  ConditionOp = "eq"
	OpNe  ConditionOp = "ne"
	OpLt  ConditionOp = "lt"
	OpLte ConditionOp = "lte"
	OpGt  ConditionOp = "gt"
	OpGte ConditionOp = "gte"
)

// Valid indica se op pertence ao enum fechado.
func (op ConditionOp) Valid() bool {
	switch op {
	case OpEq, OpNe, OpLt, OpLte, OpGt, OpGte:
		return true
	default:
		return false
	}
}

// ordering indica se op é um operador de ORDEM (só admissível sobre o operando
// inteiro): símbolos comparam-se por igualdade, nunca por «menor que».
func (op ConditionOp) ordering() bool {
	switch op {
	case OpLt, OpLte, OpGt, OpGte:
		return true
	default:
		return false
	}
}

// ConditionEnum é o operando SIMBÓLICO. Enum FECHADO, particionado por observável:
// {complete,failed} para [SubjectTerminalState], {pass,fail} para [SubjectVerdict].
// A partição é imposta na forma — um `verdict eq complete` é lixo de schema.
type ConditionEnum string

const (
	// EnumComplete/EnumFailed — operandos de [SubjectTerminalState].
	EnumComplete ConditionEnum = "complete"
	EnumFailed   ConditionEnum = "failed"
	// EnumPass/EnumFail — operandos de [SubjectVerdict].
	EnumPass ConditionEnum = "pass"
	EnumFail ConditionEnum = "fail"
)

// Predicate é UMA comparação atómica do subconjunto fechado: um observável, um
// operador e EXACTAMENTE UM operando do tipo que o observável exige.
//
// A escolha de DOIS campos de operando (símbolo e inteiro) em vez de um campo
// polimórfico é deliberada: mantém o schema estático e legível por
// [json.Decoder.DisallowUnknownFields], e a regra «exactamente um, e o certo para
// o subject» é imposta fail-closed por [Predicate.validateShape] — nunca inferida.
// Number é um PONTEIRO para distinguir «ausente» de «zero» (um `metric gte 0` é
// uma condição legítima e não pode ser confundida com um operando omitido).
type Predicate struct {
	// Subject é o observável (enum fechado).
	Subject ConditionSubject `json:"subject"`
	// Metric é o NOME da métrica observada — obrigatório e SÓ com
	// [SubjectMetric]. Identificador de charset fechado (ver [validMetricName]),
	// nunca texto livre.
	Metric string `json:"metric,omitempty"`
	// Op é o operador (enum fechado). Operadores de ORDEM só com [SubjectMetric].
	Op ConditionOp `json:"op"`
	// Enum é o operando SIMBÓLICO — obrigatório e SÓ com [SubjectTerminalState] ou
	// [SubjectVerdict], e apenas com o símbolo da partição respectiva.
	Enum ConditionEnum `json:"enum,omitempty"`
	// Number é o operando INTEIRO — obrigatório e SÓ com [SubjectMetric].
	Number *int64 `json:"number,omitempty"`
}

// ConditionalEdge é uma aresta ORIGEM→(este nó) GUARDADA por uma condição: o nó
// que a declara só é despachável se o resultado registado de [ConditionalEdge.From]
// satisfizer TODOS os predicados de [ConditionalEdge.When].
//
// DIRECÇÃO (a decisão de desenho que mantém a extensão barata). Tal como
// `depends_on`, a aresta condicional é declarada no nó de DESTINO e aponta para
// TRÁS, para a origem. Um «ramo» de ADR-022 §2.1 escreve-se como duas arestas
// simétricas — o nó do caminho feliz declara `verdict eq pass` sobre X, o nó do
// caminho de reprovação declara `verdict eq fail` sobre o MESMO X. É por isto que
// a aciclicidade REUTILIZA, sem uma linha nova de travessia, o mesmo primitivo de
// AOS-025 que já consome `depends_on` (direcção dep→nó).
type ConditionalEdge struct {
	// From é o node_id da ORIGEM cujo resultado registado é observado.
	From string `json:"from"`
	// When é a CONJUNÇÃO PLANA de predicados (todos têm de valer). Uma lista vazia
	// é rejeitada: uma aresta «condicional» sem condição seria uma dependência
	// disfarçada com semântica de terminalidade diferente.
	When []Predicate `json:"when"`
}

// Tectos de ARIDADE da gramática. Não são política do operador (esses são os
// [Ceilings] do validador): são o limite ESTRUTURAL que mantém a expressão
// pequena, legível no approval-card do gate (ADR-013) e barata de avaliar.
const (
	// maxConditionalEdgesPerNode limita as arestas condicionais declaradas por nó.
	maxConditionalEdgesPerNode = 8
	// maxPredicatesPerEdge limita os predicados de uma conjunção.
	maxPredicatesPerEdge = 8
	// maxMetricNameLen limita o nome de uma métrica observada.
	maxMetricNameLen = 64
)

// Erros de forma das arestas condicionais. Fail-closed: recusam o documento
// inteiro, nunca corrigem nem descartam a aresta ofensora.
var (
	// ErrTooManyConditionals — arestas condicionais acima do tecto de aridade.
	ErrTooManyConditionals = errors.New("plan: arestas condicionais acima do tecto de aridade")
	// ErrInvalidConditionalEdge — `from` vazio/duplicado, ou `when` vazio/acima do tecto.
	ErrInvalidConditionalEdge = errors.New("plan: aresta condicional invalida (from vazio/duplicado ou when vazio)")
	// ErrInvalidPredicate — predicado fora do subconjunto fechado (subject/op/operando).
	ErrInvalidPredicate = errors.New("plan: predicado de condicao fora do subconjunto fechado")
)

// validateConditional confere a FORMA das arestas condicionais de um nó. Puro e
// determinístico (itera pela ordem do slice). Recusa: excesso de aridade, `from`
// vazio ou repetido no mesmo nó, conjunção vazia, e qualquer predicado fora do
// subconjunto fechado.
//
// NÃO confere: existência da origem, aciclicidade, nem sobreposição com
// `depends_on` — tudo isso é SEMÂNTICA de grafo e pertence a AOS-231
// (`planvalidate`), que já detém o snapshot e o DAG.
func validateConditional(nodeID string, edges []ConditionalEdge) error {
	if len(edges) > maxConditionalEdgesPerNode {
		return fmt.Errorf("%w: %d > %d (no %q)", ErrTooManyConditionals, len(edges), maxConditionalEdgesPerNode, nodeID)
	}
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if e.From == "" {
			return fmt.Errorf("%w: from vazio (no %q)", ErrInvalidConditionalEdge, nodeID)
		}
		if _, dup := seen[e.From]; dup {
			// Duas arestas condicionais para a MESMA origem seriam uma conjunção
			// escrita em dois sítios — ambígua de ler no gate e redundante. Uma só
			// aresta por origem, com a conjunção explícita em `when`.
			return fmt.Errorf("%w: from %q repetido em %q", ErrInvalidConditionalEdge, e.From, nodeID)
		}
		seen[e.From] = struct{}{}
		if len(e.When) == 0 {
			return fmt.Errorf("%w: when vazio para from %q (no %q)", ErrInvalidConditionalEdge, e.From, nodeID)
		}
		if len(e.When) > maxPredicatesPerEdge {
			return fmt.Errorf("%w: %d predicados > %d (no %q)", ErrTooManyConditionals, len(e.When), maxPredicatesPerEdge, nodeID)
		}
		for _, p := range e.When {
			if err := p.validateShape(); err != nil {
				return fmt.Errorf("%w (no %q, from %q)", err, nodeID, e.From)
			}
		}
	}
	return nil
}

// validateShape confere que o predicado pertence ao subconjunto fechado: enums
// válidos e — a parte que fecha a gramática — EXACTAMENTE UM operando, do tipo que
// o observável exige. Puro.
func (p Predicate) validateShape() error {
	if !p.Subject.Valid() {
		return fmt.Errorf("%w: subject %q", ErrInvalidPredicate, p.Subject)
	}
	if !p.Op.Valid() {
		return fmt.Errorf("%w: op %q", ErrInvalidPredicate, p.Op)
	}
	switch p.Subject {
	case SubjectTerminalState, SubjectVerdict:
		// Observável SIMBÓLICO: operando `enum` da partição certa, sem `number`, sem
		// `metric`, e só igualdade/desigualdade (ordem não se define sobre símbolos).
		if p.Number != nil {
			return fmt.Errorf("%w: subject %q nao admite operando numerico", ErrInvalidPredicate, p.Subject)
		}
		if p.Metric != "" {
			return fmt.Errorf("%w: subject %q nao admite nome de metrica", ErrInvalidPredicate, p.Subject)
		}
		if p.Op.ordering() {
			return fmt.Errorf("%w: op %q nao se aplica a operando simbolico", ErrInvalidPredicate, p.Op)
		}
		if !p.Enum.validFor(p.Subject) {
			return fmt.Errorf("%w: operando %q fora da particao de %q", ErrInvalidPredicate, p.Enum, p.Subject)
		}
	case SubjectMetric:
		// Observável NUMÉRICO: nome de métrica com grammar fechada, operando
		// `number` presente, sem `enum`.
		if p.Enum != "" {
			return fmt.Errorf("%w: subject %q nao admite operando simbolico", ErrInvalidPredicate, p.Subject)
		}
		if p.Number == nil {
			return fmt.Errorf("%w: subject %q exige operando numerico", ErrInvalidPredicate, p.Subject)
		}
		if !validMetricName(p.Metric) {
			return fmt.Errorf("%w: nome de metrica invalido", ErrInvalidPredicate)
		}
	}
	return nil
}

// validFor indica se o símbolo pertence à PARTIÇÃO do observável. É esta partição
// que impede um `verdict eq complete` (sintacticamente plausível, semanticamente
// lixo) de entrar no documento.
func (e ConditionEnum) validFor(s ConditionSubject) bool {
	switch s {
	case SubjectTerminalState:
		return e == EnumComplete || e == EnumFailed
	case SubjectVerdict:
		return e == EnumPass || e == EnumFail
	default:
		return false
	}
}

// validMetricName confere que o nome da métrica é um IDENTIFICADOR ESTRUTURAL
// limitado: 1..[maxMetricNameLen] bytes, a começar por letra minúscula, de um
// charset ASCII FECHADO (minúsculas, dígitos, `_` e `.`). É o mesmo raciocínio do
// `node_id` em AOS-231: o nome viaja para a forma canónica (e daí para o digest e
// para o approval-card), pelo que não pode veicular texto livre do modelo. Puro.
func validMetricName(s string) bool {
	if len(s) == 0 || len(s) > maxMetricNameLen {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

// CanonicalConditional devolve a forma CANÓNICA e determinística das arestas
// condicionais de um nó — a string sobre a qual [ConditionDigest] fecha.
//
// Preserva a ORDEM DECLARADA (não ordena): duas declarações permutadas são
// semanticamente equivalentes (a conjunção é comutativa) mas são DOCUMENTOS
// diferentes, e o digest existe para amarrar a decisão de ramo registada ao
// documento EXACTO que a produziu — ser mais estrito é o lado seguro.
//
// Os separadores (`|`, `{`, `}`, `,`, `=`, `(`, `)`) não podem ocorrer dentro de
// nenhum campo interpolado: `from` é um node_id (charset fechado, AOS-231), o nome
// de métrica passa por [validMetricName], e subject/op/enum são enums fechados. A
// forma canónica é, por isso, não-ambígua por construção — não por escape.
func CanonicalConditional(edges []ConditionalEdge) string {
	var b strings.Builder
	for i, e := range edges {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(e.From)
		b.WriteByte('{')
		for j, p := range e.When {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteString(string(p.Subject))
			if p.Subject == SubjectMetric {
				b.WriteByte('(')
				b.WriteString(p.Metric)
				b.WriteByte(')')
			}
			b.WriteByte('=')
			b.WriteString(string(p.Op))
			b.WriteByte('=')
			if p.Number != nil {
				b.WriteString(strconv.FormatInt(*p.Number, 10))
			} else {
				b.WriteString(string(p.Enum))
			}
		}
		b.WriteByte('}')
	}
	return b.String()
}

// ConditionDigest é o carimbo determinístico das arestas condicionais de um nó,
// na forma `sha256:<hex>`. AMARRA a decisão de ramo REGISTADA (evento
// `plan.branch_decided`, AOS-270) à expressão EXACTA que a produziu: no replay, um
// digest divergente significa que o documento mudou — e um plano editado não é um
// replay. Fail-closed a jusante.
//
// Um conjunto de arestas VAZIO tem digest da string vazia — determinístico e
// nunca consultado (um nó sem condições não produz decisão de ramo).
func ConditionDigest(edges []ConditionalEdge) string {
	sum := sha256.Sum256([]byte(CanonicalConditional(edges)))
	return "sha256:" + hex.EncodeToString(sum[:])
}
