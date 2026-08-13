package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// payload.go — OS CONTRATOS TIPADOS DE PAYLOAD POR ARESTA (AOS-272, ADR-022 §2.3).
//
// O ADR congela o QUE: «cada nó passa a poder declarar `outputs` e cada aresta
// `consumes` — contratos tipados (nome, schema, classificação de *taint*) validados
// estaticamente pelo validador puro» e, sobretudo, a rejeição (c): «o transporte do
// payload NÃO é um blackboard: é referência a registo no Event Store/MEM com
// proveniência, respeitando “contexto ≠ registo”». Este ficheiro é o COMO da parte
// declarativa — os TIPOS DE PAYLOAD que a §4 do ADR deixou expressamente fora.
//
// # ONDE VIVE O `consumes`, E PORQUE NÃO DENTRO DE `depends_on`
//
// Há dois canais de aresta no schema: `depends_on` (uma lista de node_ids) e
// `conditional_on` (a lista de [ConditionalEdge] de AOS-270). O `consumes` teria de
// caber nos DOIS — um payload flui tanto por uma dependência simples como por um ramo
// — e `depends_on` é `[]string`: alargá-lo a uma lista de objectos é uma QUEBRA de
// forma, logo um MAJOR de `plan_version`, que é trabalho de AOS-273 e não deste
// ticket.
//
// A forma escolhida declara a aresta de dados PELO SEU EXTREMO: [Node.Consumes] é uma
// lista de [PayloadEdge], cada uma nomeando a ORIGEM (`from`), o OUTPUT dessa origem e
// o TIPO que o consumidor espera. É exactamente «uma aresta a declarar `consumes`» —
// o par (from → este nó) identifica a aresta —, é OPCIONAL e ADITIVO (um documento
// sem o campo decodifica e comporta-se como antes, logo sem MAJOR), e serve os dois
// canais com uma só regra.
//
// A contrapartida é uma invariante que o validador impõe e que vale a pena dizer aqui
// porque é ELA que mantém o grafo de dados subordinado ao grafo de execução: `from`
// TEM de ser uma aresta de entrada JÁ DECLARADA do nó (por `depends_on` ou por
// `conditional_on`). Consequências, ambas desejadas: (i) não se consome o trabalho de
// um nó por quem não espera por ele — uma leitura sem precedência era uma corrida com
// o produtor; (ii) o grafo de payload é um SUB-GRAFO do DAG de admissão, pelo que é
// ACÍCLICO POR CONSTRUÇÃO — sem uma linha nova de travessia e sem um detector novo que
// se possa esquecer de correr (a mesma disciplina que AOS-270 usou para as arestas
// guardadas por condição).
//
// # O TAINT NÃO É DECLARADO — E O TIPO SOZINHO NÃO CHEGA
//
// A classificação de *taint* que o ADR pede NÃO é uma taxonomia nova: são os dois
// rótulos do reticulado canónico de ADR-005 (`kernel/reference-monitor/taint`:
// trusted ⊑ untrusted), nas suas formas textuais canónicas. O que este ficheiro fixa é
// de ONDE o rótulo vem — e não vem da palavra do planeador, que é *untrusted* como
// todo o documento.
//
// A PRIMEIRA VERSÃO DESTE FICHEIRO derivava o rótulo SÓ do tipo ([PayloadType.ClosedForm]):
// forma fechada ⇒ `trusted`. A auditoria adversarial da wave falsificou-o, e vale a
// pena registar o defeito porque ele é instrutivo: `type` É UM CAMPO DO DOCUMENTO. Um
// plano hostil declarava `outputs: [{name: achados, type: metrics}]` num nó cujo
// trabalho é prosa, e um consumidor privilegiado recebia material untrusted com
// rótulo `trusted` — a barreira P0 de ADR-005 contornada por uma palavra.
//
// O rótulo `trusted` exige por isso DUAS condições, e o piso é `untrusted` sempre que
// uma delas falhe:
//
//	(1) AUTORIDADE — o tipo é de forma fechada E O PRODUTOR É UM NÓ `role: verifier`.
//	    O verificador é o ponto de desclassificação que ADR-022 §2.2 SANCIONA, e o que
//	    o ganha são as três propriedades que o sistema lhe IMPÕE na admissão:
//	    independência do trabalho que julga, read-only por construção, e não produzir
//	    trabalho ele próprio (só declara outputs de forma fechada — `planvalidate`).
//	    Qualquer outro nó publica `untrusted`, e é isto que SUBSUME a propagação pelo
//	    reticulado: um nó intermédio não pode lavar untrusted→trusted num salto porque
//	    não pode publicar trusted de todo.
//	(2) FORMA — a forma fechada é IMPOSTA NA EMISSÃO, não prometida no documento: o
//	    facto `plan.payload_published` de um tipo fechado carrega o conteúdo INLINE
//	    (símbolos, códigos e inteiros, validados pelo construtor) e NÃO tem locator; o
//	    de um tipo aberto carrega locator e NENHUM conteúdo. A hipótese intermédia —
//	    uma referência opaca com rótulo trusted — deixou de existir (`plannerevents`).
//
// O campo `taint` do documento é ADVISORY e SÓ ELEVA — a mesma regra já ratificada
// para o `risk_class` (ver [RiskClass] e a regra 6 de AOS-232). Declarar `untrusted`
// num output fechado é honrado (o emissor sabe algo que o tipo não diz); declarar
// `trusted` num resumo é IGNORADO. Não há caminho por onde o documento baixe um
// rótulo de taint — que é precisamente a propriedade que o reticulado de ADR-005
// impõe («não existe desclassificação»).
//
// FRONTEIRA. Este ficheiro define e valida a FORMA (como o resto de [plan]) e a
// derivação PURA do taint efectivo. NÃO resolve o grafo (existência da origem, aresta
// declarada, compatibilidade de tipo, autoridade do consumidor: `planvalidate`), NÃO
// publica nem transporta payload (`plannerevents` emite a referência; `plandispatch`
// resolve-a por contrato). Zero dependências fora da stdlib.

// PayloadType é o SCHEMA de um payload — o «tipo» dos contratos de ADR-022 §2.3.
// Enum FECHADO e deliberadamente CURTO: cinco formas que cobrem o que atravessa uma
// aresta de um organigrama, sem abrir uma linguagem de tipos dentro do documento
// (que seria código arbitrário por outro nome, exactamente o que §2.1 recusou para as
// condições).
//
// A COMPATIBILIDADE É IDENTIDADE, não subtipagem: o consumidor declara o tipo que
// espera e ele tem de ser IGUAL ao que o produtor declara. Não há coerção, promoção
// nem tipo universal. É a mesma disciplina da gramática das condições — comparar, não
// computar: uma relação de subtipagem obrigaria o validador a RACIOCINAR sobre tipos,
// e um raciocínio é uma superfície onde uma incompatibilidade real se pode perder.
type PayloadType string

const (
	// PayloadSummary — o RESUMO em linguagem natural (o resumo filho→pai de
	// «contexto ≠ registo», 1–2k tokens). Admite conteúdo ⇒ untrusted.
	PayloadSummary PayloadType = "summary"
	// PayloadRecord — um registo ESTRUTURADO produzido pelo nó (campos nomeados).
	// Estruturado não quer dizer fechado: os valores continuam a vir do trabalho ⇒
	// untrusted.
	PayloadRecord PayloadType = "record"
	// PayloadArtifact — a referência a um artefacto armazenado (ficheiro, blob). O
	// conteúdo é opaco e vem do trabalho ⇒ untrusted.
	PayloadArtifact PayloadType = "artifact"
	// PayloadMetrics — um conjunto de MÉTRICAS: nomes de charset fechado e valores
	// INTEIROS, a mesma forma que os predicados `metric` de condition.go e as
	// `metrics[]` do veredicto tipado consomem. Fechado por construção ⇒ trusted.
	PayloadMetrics PayloadType = "metrics"
	// PayloadVerdict — o VEREDICTO TIPADO de ADR-022 §2.2 (pass/fail + códigos de
	// razão + métricas inteiras), validado na emissão. Fechado por construção ⇒
	// trusted.
	PayloadVerdict PayloadType = "verdict"
)

// Valid indica se t pertence ao enum fechado. Fail-closed: o vazio NÃO é admissível —
// um contrato sem tipo não é um contrato.
func (t PayloadType) Valid() bool {
	switch t {
	case PayloadSummary, PayloadRecord, PayloadArtifact, PayloadMetrics, PayloadVerdict:
		return true
	default:
		return false
	}
}

// ClosedForm indica se o tipo é FECHADO POR CONSTRUÇÃO — se a sua forma admite
// apenas símbolos de enum, códigos de charset fechado e inteiros, todos validados na
// fronteira de emissão. É o predicado de que [PayloadType.TaintFloor] deriva o
// rótulo, e é a única razão pela qual um payload pode ser trusted: não é que se
// confie no nó, é que a FORMA não deixa passar conteúdo nenhum do trabalho.
//
// FAIL-CLOSED PELO TIPO, sem uma linha para isso: só dois símbolos respondem
// verdadeiro; um tipo inválido, vazio ou acrescentado sem tocar aqui responde falso e
// o seu piso de taint é `untrusted`. Um tipo por classificar nunca é trusted por
// omissão.
func (t PayloadType) ClosedForm() bool {
	return t == PayloadMetrics || t == PayloadVerdict
}

// PayloadTaint é o rótulo de confiança de um payload no reticulado control/data-plane
// de ADR-005. NÃO é uma taxonomia nova: são as duas formas textuais canónicas do
// `taint.Label` do reference-monitor (`taint.StringTrusted`/`taint.StringUntrusted`),
// escritas aqui porque [plan] é zero-dep e não importa o kernel — o mapeamento é
// literal e está declarado, não convencionado.
type PayloadTaint string

const (
	// TaintUnset — campo ausente. Advisory não fornecido; o piso derivado do tipo
	// vale sozinho. É um valor de enum ADMISSÍVEL (o campo é opcional), como
	// [RiskUnset].
	TaintUnset PayloadTaint = ""
	// TaintTrusted — conteúdo que pode originar acção privilegiada (ADR-005).
	TaintTrusted PayloadTaint = "trusted"
	// TaintUntrusted — dados que NÃO autorizam elevação.
	TaintUntrusted PayloadTaint = "untrusted"
)

// Valid indica se t é um dos valores do enum fechado (incluindo o vazio, que é «não
// fornecido»). Fail-closed: qualquer outro literal é rejeitado por [Decode].
func (t PayloadTaint) Valid() bool {
	switch t {
	case TaintUnset, TaintTrusted, TaintUntrusted:
		return true
	default:
		return false
	}
}

// taintRank é a ordem do reticulado (ausente ⊏ trusted ⊏ untrusted), para o «só
// eleva» de [Node.EffectiveOutputTaint]. O ausente fica no FUNDO — um advisory omitido
// nunca eleva nada, exactamente como [riskRank] trata [RiskUnset].
func (t PayloadTaint) rank() int {
	switch t {
	case TaintTrusted:
		return 1
	case TaintUntrusted:
		return 2
	default:
		return 0 // TaintUnset / ausente
	}
}

// TaintFloor é o piso de taint DA FORMA: `trusted` para as formas fechadas
// ([PayloadType.ClosedForm]), `untrusted` para tudo o resto. Puro.
//
// NÃO É O RÓTULO QUE VALE, e a distinção é a substância da correcção da wave: a forma
// fechada é condição NECESSÁRIA de `trusted`, nunca SUFICIENTE — falta a autoridade do
// produtor. O rótulo efectivo é [Node.EffectiveOutputTaint], e é ele que a admissão
// (`planvalidate`) e a emissão (`plannerevents`) usam. Este piso existe só como a
// metade «forma» dessa derivação.
func (t PayloadType) TaintFloor() PayloadTaint {
	if t.ClosedForm() {
		return TaintTrusted
	}
	return TaintUntrusted
}

// Output é o contrato de SAÍDA declarado por um nó: um nome, um schema e um rótulo de
// taint ADVISORY.
type Output struct {
	// Name é o nome do contrato, ÚNICO no nó. Identificador de charset fechado
	// ([ValidIdentifier]) — nunca texto livre: o nome viaja para a forma canónica,
	// para o digest e para o payload do facto que publica a referência.
	Name string `json:"name"`
	// Type é o schema do payload (enum fechado).
	Type PayloadType `json:"type"`
	// Taint é o rótulo ADVISORY. Só ELEVA o piso derivado — ver
	// [Node.EffectiveOutputTaint]. Opcional.
	Taint PayloadTaint `json:"taint,omitempty"`
}

// EffectiveOutputTaint é o rótulo que VALE para um contrato de saída DECLARADO POR
// ESTE NÓ. É a derivação completa da nota de topo — forma E autoridade —, e vive no
// [Node] (não no [Output]) precisamente porque a autoridade é uma propriedade do
// PRODUTOR: um `Output` isolado não sabe quem o declara, e foi essa ignorância que a
// primeira versão transformou num buraco.
//
//	piso := untrusted
//	se o.Type.ClosedForm() E n é verificador ⇒ piso := trusted
//	efectivo := max(piso, advisory)        // o documento SÓ ELEVA
//
// Puro, determinístico e sem grafo: a propagação transitiva pelo reticulado (o taint
// do que o nó CONSOME) é SUBSUMIDA — um nó não-verificador tem piso `untrusted` em
// TODOS os seus outputs, logo o supremo com o que quer que consuma continua
// `untrusted`; e o verificador é, por §2.2, o ponto de desclassificação sancionado,
// que existe exactamente para NÃO propagar o taint do trabalho que examina (senão o
// organigrama canónico do ADR — produtor → verificador → consumidor privilegiado —
// seria impossível de escrever). Não há travessia nova, e não há regra que se possa
// esquecer de correr.
func (n Node) EffectiveOutputTaint(o Output) PayloadTaint {
	floor := TaintUntrusted
	if o.Type.ClosedForm() && n.IsVerifier() {
		floor = TaintTrusted
	}
	if o.Taint.rank() > floor.rank() {
		return o.Taint
	}
	return floor
}

// PayloadEdge é uma ARESTA DE DADOS declarada no extremo CONSUMIDOR: «desta origem,
// que já é minha aresta de entrada, leio o output X, que espero ser do tipo T».
//
// NÃO declara taint, e a ausência é a decisão. O taint do payload é uma propriedade
// do PRODUTOR (derivada do tipo do seu output) e a autoridade é uma propriedade do
// CONSUMIDOR (derivada das suas tools pinadas): se o consumidor pudesse declarar o
// taint que aceita, um plano hostil resolvia a incompatibilidade escrevendo-a. A
// compatibilidade é DECIDIDA pelo validador sobre as duas propriedades derivadas —
// nunca negociada no documento.
type PayloadEdge struct {
	// From é o node_id da ORIGEM. Tem de ser uma aresta de entrada JÁ declarada do
	// nó (`depends_on` ou `conditional_on`) — imposto por AOS-231.
	From string `json:"from"`
	// Output é o nome do contrato na origem.
	Output string `json:"output"`
	// Type é o schema ESPERADO. Tem de ser IGUAL ao declarado pela origem.
	Type PayloadType `json:"type"`
}

// Tectos de ARIDADE dos contratos. Não são política do operador (esses são os
// `Ceilings` do validador): são o limite ESTRUTURAL que mantém o contrato pequeno,
// legível no approval-card do gate (ADR-013) e barato de validar.
const (
	// maxOutputsPerNode limita os contratos de saída declarados por nó.
	maxOutputsPerNode = 8
	// maxConsumesPerNode limita as arestas de dados declaradas por nó.
	maxConsumesPerNode = 16
)

// Erros de forma dos contratos de payload. Fail-closed: recusam o documento inteiro,
// nunca corrigem nem descartam o contrato ofensor.
var (
	// ErrTooManyPayloadContracts — outputs/consumes acima do tecto de aridade.
	ErrTooManyPayloadContracts = errors.New("plan: contratos de payload acima do tecto de aridade")
	// ErrInvalidOutput — output com nome fora da grammar, duplicado no nó, tipo fora
	// do enum ou taint fora do enum.
	ErrInvalidOutput = errors.New("plan: contrato de output invalido (nome/tipo/taint)")
	// ErrInvalidConsumes — aresta de dados com `from` vazio, nome de output fora da
	// grammar, tipo fora do enum, ou par (from,output) repetido no mesmo nó.
	ErrInvalidConsumes = errors.New("plan: aresta de consumo invalida (from/output/tipo)")
)

// validatePayload confere a FORMA dos contratos de payload de um nó. Puro e
// determinístico (itera pela ordem dos slices).
//
// NÃO confere: existência da origem, correspondência com uma aresta declarada,
// existência do output na origem, compatibilidade de tipo nem de taint com a
// autoridade do consumidor — tudo isso é SEMÂNTICA DE GRAFO e pertence a AOS-231
// (`planvalidate`), que detém o snapshot pinado e o DAG. A mesma fronteira que
// `depends_on` e `conditional_on` já respeitam.
func validatePayload(nodeID string, outputs []Output, consumes []PayloadEdge) error {
	if len(outputs) > maxOutputsPerNode {
		return fmt.Errorf("%w: %d outputs > %d (no %q)", ErrTooManyPayloadContracts, len(outputs), maxOutputsPerNode, nodeID)
	}
	seenOut := make(map[string]struct{}, len(outputs))
	for _, o := range outputs {
		if !ValidIdentifier(o.Name) {
			return fmt.Errorf("%w: nome fora da grammar (no %q)", ErrInvalidOutput, nodeID)
		}
		if _, dup := seenOut[o.Name]; dup {
			// Dois outputs com o mesmo nome tornariam o `consumes` ambíguo, e a
			// ambiguidade resolver-se-ia por ordem de iteração — que é exactamente
			// como um contrato deixa de ser um contrato.
			return fmt.Errorf("%w: output %q repetido (no %q)", ErrInvalidOutput, o.Name, nodeID)
		}
		seenOut[o.Name] = struct{}{}
		if !o.Type.Valid() {
			return fmt.Errorf("%w: tipo %q (no %q)", ErrInvalidOutput, o.Type, nodeID)
		}
		if !o.Taint.Valid() {
			return fmt.Errorf("%w: taint %q (no %q)", ErrInvalidOutput, o.Taint, nodeID)
		}
	}
	if len(consumes) > maxConsumesPerNode {
		return fmt.Errorf("%w: %d consumes > %d (no %q)", ErrTooManyPayloadContracts, len(consumes), maxConsumesPerNode, nodeID)
	}
	seenIn := make(map[string]struct{}, len(consumes))
	for _, c := range consumes {
		if c.From == "" {
			return fmt.Errorf("%w: from vazio (no %q)", ErrInvalidConsumes, nodeID)
		}
		if !ValidIdentifier(c.Output) {
			return fmt.Errorf("%w: nome de output fora da grammar (no %q)", ErrInvalidConsumes, nodeID)
		}
		if !c.Type.Valid() {
			return fmt.Errorf("%w: tipo %q (no %q)", ErrInvalidConsumes, c.Type, nodeID)
		}
		// O par (from,output) é a IDENTIDADE da aresta de dados: repeti-lo seria
		// declarar duas vezes o mesmo contrato, com a hipótese de os dois tipos
		// divergirem — e então qual valia?
		k := c.From + "\x00" + c.Output
		if _, dup := seenIn[k]; dup {
			return fmt.Errorf("%w: par (%q,%q) repetido (no %q)", ErrInvalidConsumes, c.From, c.Output, nodeID)
		}
		seenIn[k] = struct{}{}
	}
	return nil
}

// CanonicalOutput devolve a forma CANÓNICA e determinística de UM contrato de saída
// declarado pelo nó n — a string sobre a qual [OutputDigest] fecha. Inclui o taint
// EFECTIVO ([Node.EffectiveOutputTaint]), não o declarado: é o rótulo que vale, e é o
// que a referência publicada carrega.
//
// TOMA O NÓ, e não só o output, pela mesma razão que [Node.EffectiveOutputTaint]: o
// rótulo que vale depende do PRODUTOR. Um digest que fechasse só sobre (nome, tipo,
// advisory) seria igual para o `metrics` de um verificador e para o `metrics` de um nó
// qualquer — dois contratos com rótulos EFECTIVOS opostos a partilhar carimbo, que é
// a mesma confusão que a auditoria apanhou uma camada acima.
//
// Os separadores (`:`) não podem ocorrer dentro de nenhum campo interpolado — o nome
// passa por [ValidIdentifier] e tipo/taint são enums fechados —, pelo que a forma é
// não-ambígua por construção, não por escape. É o mesmo argumento de
// [CanonicalConditional].
func CanonicalOutput(n Node, o Output) string {
	var b strings.Builder
	b.WriteString(o.Name)
	b.WriteByte(':')
	b.WriteString(string(o.Type))
	b.WriteByte(':')
	b.WriteString(string(n.EffectiveOutputTaint(o)))
	return b.String()
}

// OutputDigest é o carimbo determinístico de UM contrato de saída, na forma
// `sha256:<hex>`. AMARRA a REFERÊNCIA publicada (`plan.payload_published`) ao contrato
// EXACTO que a autorizou: no replay, um digest divergente significa que o documento
// mudou — e um plano editado não é um replay. Fail-closed a jusante.
//
// A amarra só é REAL porque as duas pontas o computam a partir do documento aprovado,
// nunca o aceitam de um chamador: a emissão deriva-o (`plannerevents.NewPayloadPublished`)
// e o consumo re-deriva-o e compara (`plandispatch.PayloadResolver.Inbox`).
func OutputDigest(n Node, o Output) string {
	sum := sha256.Sum256([]byte(CanonicalOutput(n, o)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FindOutput devolve o contrato de saída de nome `name` declarado pelo nó. É a ÚNICA
// resolução nome→contrato do módulo: o validador (AOS-231), o emissor da referência e
// o resolvedor do consumidor passam todos por aqui, pelo que não há duas noções de
// «que output é este». Determinística (ordem do slice; o duplicado já é impossível
// pela forma). Puro, sem alocação.
func (n Node) FindOutput(name string) (Output, bool) {
	for _, o := range n.Outputs {
		if o.Name == name {
			return o, true
		}
	}
	return Output{}, false
}
