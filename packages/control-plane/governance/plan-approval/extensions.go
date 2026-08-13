package planapproval

import (
	"sort"
	"strconv"
	"strings"
)

// extensions.go — AS EXTENSÕES DE ADR-022 TORNADAS VISÍVEIS AO GATE (DEF-274).
//
// O ADR-022 (ratificado 2026-08-13) fecha cinco invariantes; o QUINTO é uma
// obrigação DESTE módulo: «o humano no gate vê o organigrama COM as condições e os
// verificadores declarados» (§2.4(5)), reafirmada em §5 («o approval-card apresenta
// condições e verificadores»). As três extensões foram entregues a montante — arestas
// condicionais (AOS-270), papel verificador (AOS-271), payload tipado (AOS-272) — mas
// o cartão que o humano aprova mostrava só nós e arestas de precedência: aprovava-se
// um organigrama que NÃO era o que ia correr. Este ficheiro fecha essa distância.
//
// # PORQUE A PORTA CRESCE EM VEZ DE IMPORTAR O ORQUESTRADOR
//
// [PlanNode] é, por desenho declarado (ver ports.go), a PORTA que desacopla o gate do
// `orchestrator.NodeSpec`/`plan.Node`: o gate não importa o orquestrador — é o wiring
// que mapeia. Importar `orchestrator/plan` para ler `ConditionalOn`/`Outputs` partiria
// esse desacoplamento e o módulo (zero-dep, go.mod próprio). A projecção é por isso
// LOCAL e SIMBÓLICA: os tipos abaixo são a MESMA FORMA que o orquestrador publica,
// declarada aqui em vez de herdada — o mapeamento é literal e está escrito, como o dos
// rótulos de taint em `plan/payload.go` (que faz exactamente o mesmo ao kernel).
//
// # A REGRA DE OURO DO CARTÃO É O CRITÉRIO DE DESENHO, NÃO UM CUIDADO
//
// «SEM segredos: o Preview é o efeito resolvido, nunca o input da tool» (ports.go).
// Uma condição e um contrato de dados são, em geral, o sítio ÓBVIO por onde conteúdo
// do run entraria num cartão — «se o output X contiver <isto>» carregaria o <isto>.
// O que atravessa esta porta é por isso, sem excepção, uma de três coisas:
//
//	(1) um SÍMBOLO de um enum fechado a montante (subject, operador, tipo, taint,
//	    papel) — validado aqui contra um charset ASCII fechado;
//	(2) um INTEIRO de máquina em forma decimal canónica;
//	(3) uma REFERÊNCIA estrutural (um task_id que TEM de existir no próprio plano).
//
// Não há um campo por onde um valor de payload, um excerto de output, um locator ou
// um segredo caibam — e isso não é uma convenção documentada, é imposto por
// [PlanNode.validateExtensions] com [ErrNonCanonicalExtension] fail-closed. Um wiring
// que tente empurrar texto livre pela porta não degrada o cartão: RECUSA o plano.
//
// # O GATE NÃO É UMA SEGUNDA AUTORIDADE DE GRAMÁTICA
//
// O que o validador puro (AOS-231) decide — que operando pertence a que observável,
// tectos de aridade, aciclicidade, compatibilidade de tipo, autoridade de taint do
// consumidor — NÃO é re-decidido aqui, e é deliberado: duas autoridades sobre a mesma
// gramática envelhecem em direcções diferentes e a divergência descobre-se em
// produção. O gate impõe só a FORMA que garante (1)–(3) — a propriedade de que ele
// próprio precisa para não virar canal de conteúdo — e APRESENTA o resto tal-qual.

// RoleVerifier é o papel RESERVADO de ADR-022 §2.2 na representação local do gate. É
// a forma textual EXACTA do `plan.RoleVerifier` do orquestrador, escrita aqui porque
// este módulo não o importa: o mapeamento é literal e declarado, não convencionado.
//
// Para o humano no cartão, é a distinção que o invariante §2.4(5) exige: um nó
// verificador é read-only por construção, independente do que julga, e é o ÚNICO
// ponto de desclassificação de taint que o sistema sanciona — ver um verificador no
// organigrama é ver ONDE a confiança é produzida.
const RoleVerifier = "verifier"

// PlanPredicate é UMA comparação atómica da gramática fechada das arestas condicionais
// (ADR-022 §2.1), na forma SIMBÓLICA que o gate apresenta.
//
// Os quatro campos são todos símbolos ou um inteiro decimal — nunca um valor
// observado. `Operand` é uma string e não um par (símbolo, número) porque o cartão
// não AVALIA nada: a distinção de tipo do operando é do despachante, e replicá-la
// aqui seria a segunda autoridade que a nota de topo recusa. O que o gate impõe é que
// seja um símbolo do charset fechado OU um inteiro canónico — e nada mais cabe lá.
type PlanPredicate struct {
	// Subject é o observável do resultado registado da origem (símbolo do enum
	// fechado a montante: `terminal_state`, `verdict`, `metric`).
	Subject string `json:"subject"`
	// Metric é o NOME da métrica observada (só com o observável `metric`).
	// Identificador de charset fechado — nunca o VALOR da métrica.
	Metric string `json:"metric,omitempty"`
	// Op é o operador de comparação (símbolo do enum fechado: `eq`,`ne`,`lt`,…).
	Op string `json:"op"`
	// Operand é o operando: um SÍMBOLO (`pass`,`fail`,`complete`,`failed`) ou um
	// INTEIRO em forma decimal canónica. Nunca conteúdo.
	Operand string `json:"operand"`
}

// PlanCondition é a aresta *condicional* que GOVERNA A ENTRADA de um nó (ADR-022 §2.1):
// a origem observada e a CONJUNÇÃO PLANA de predicados que todos têm de valer.
//
// A direcção espelha a do orquestrador — declarada no nó de DESTINO, a apontar para
// trás — porque é assim que a aresta condicional é também uma aresta de PRECEDÊNCIA,
// e é por isso que [Plan.TopoOrder] a conta (ver [Plan.effectiveEdges]): um cartão que
// mostrasse o nó guardado ANTES da sua origem estaria a mostrar uma ordem que não é a
// que vai correr, que é o defeito exacto que este ficheiro fecha.
type PlanCondition struct {
	// From é o task_id da ORIGEM observada. TEM de existir no plano ([Plan.Validate]).
	From string `json:"from"`
	// When é a conjunção de predicados (todos têm de valer). Vazia é recusada: uma
	// aresta «condicional» sem condição não é apresentável como condição nenhuma.
	When []PlanPredicate `json:"when"`
}

// PlanOutput é o contrato de SAÍDA declarado por um nó (ADR-022 §2.3) na forma que o
// gate apresenta: nome, tipo e rótulo de taint — os três CANÓNICOS, nunca o conteúdo
// que o output virá a carregar.
type PlanOutput struct {
	// Name é o nome do contrato (identificador de charset fechado).
	Name string `json:"name"`
	// Type é o schema do payload (símbolo do enum fechado: `summary`, `record`,
	// `artifact`, `metrics`, `verdict`).
	Type string `json:"type"`
	// Taint é o rótulo EFECTIVO do payload (`trusted`/`untrusted`) — o que VALE, já
	// derivado a montante por `plan.Node.EffectiveOutputTaint` (forma fechada E
	// produtor verificador). O gate NÃO deriva taint: exibe o que lhe é dado, e
	// exibe-o fail-closed — ver [PlanOutput.EffectiveTaint].
	Taint string `json:"taint,omitempty"`
}

// TaintTrusted/TaintUntrusted são as duas formas textuais canónicas do reticulado de
// ADR-005 tal como o gate as apresenta. Mesmo mapeamento literal de [RoleVerifier]:
// escritas aqui porque o módulo não importa nem o kernel nem o orquestrador.
const (
	TaintTrusted   = "trusted"
	TaintUntrusted = "untrusted"
)

// EffectiveTaint é o rótulo que o cartão MOSTRA. Fail-closed na apresentação: só o
// símbolo EXACTO `trusted` mostra trusted; ausente, desconhecido ou qualquer outra
// coisa mostra `untrusted` — o pior caso, o mesmo princípio de [parseClass] (rótulo
// desconhecido ⇒ danger).
//
// Não é uma derivação de taint (essa é do orquestrador, e o gate não a duplica): é a
// garantia de que o cartão nunca apresenta um rótulo MAIS PERMISSIVO do que aquele
// que lhe foi entregue. Um wiring que se esqueça de povoar o campo faz o humano ver
// «untrusted», não um branco tranquilizador.
func (o PlanOutput) EffectiveTaint() string {
	if o.Taint == TaintTrusted {
		return TaintTrusted
	}
	return TaintUntrusted
}

// PlanConsume é a ARESTA DE DADOS declarada no extremo consumidor (ADR-022 §2.3):
// «desta origem leio o output X, que espero ser do tipo T». É o que torna legível, no
// cartão, QUE trabalho de QUEM alimenta este nó — a informação que faltava para o
// humano ver quem verifica quem.
type PlanConsume struct {
	// From é o task_id da ORIGEM. TEM de existir no plano ([Plan.Validate]).
	From string `json:"from"`
	// Output é o nome do contrato na origem (identificador de charset fechado).
	Output string `json:"output"`
	// Type é o schema ESPERADO (símbolo do enum fechado).
	Type string `json:"type"`
}

// Canonical devolve a forma canónica do predicado: `subject=op=operand`, com o nome da
// métrica entre parênteses quando o observável é `metric`. Determinística e
// não-ambígua POR CONSTRUÇÃO: os separadores (`=`,`(`,`)`) não podem ocorrer dentro de
// nenhum campo interpolado, porque todos passaram por [validCanonicalSymbol] ou por
// [validCanonicalOperand] — não-ambígua por charset, não por escape.
//
// É deliberadamente ISOMORFA à `plan.CanonicalConditional` do orquestrador: o humano
// lê no cartão a MESMA string sobre a qual o digest da decisão de ramo fecha a
// montante. Sendo os dois módulos independentes (o gate não importa o orquestrador), a
// coincidência é DECLARADA e testada aqui — não herdada.
func (p PlanPredicate) Canonical() string {
	var b strings.Builder
	b.WriteString(p.Subject)
	if p.Metric != "" {
		b.WriteByte('(')
		b.WriteString(p.Metric)
		b.WriteByte(')')
	}
	b.WriteByte('=')
	b.WriteString(p.Op)
	b.WriteByte('=')
	b.WriteString(p.Operand)
	return b.String()
}

// Canonical devolve a forma canónica da aresta condicional: `from{p1,p2,…}`, na ORDEM
// DECLARADA (não ordena — duas declarações permutadas são semanticamente equivalentes
// mas são DOCUMENTOS diferentes, e o cartão apresenta o documento que vai correr).
func (c PlanCondition) Canonical() string {
	var b strings.Builder
	b.WriteString(c.From)
	b.WriteByte('{')
	for i, p := range c.When {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.Canonical())
	}
	b.WriteByte('}')
	return b.String()
}

// CanonicalConditions junta as arestas condicionais de um nó por `|`, na ordem
// declarada. É a forma que entra na assinatura de extensões do diff de edição
// ([DiffPlans]).
func CanonicalConditions(cs []PlanCondition) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, c.Canonical())
	}
	return strings.Join(parts, "|")
}

// Canonical devolve a forma canónica do contrato de saída: `nome:tipo:taint`, com o
// taint EFECTIVO ([PlanOutput.EffectiveTaint]) — o rótulo que vale, não um campo em
// branco. Isomorfa à `plan.CanonicalOutput`.
func (o PlanOutput) Canonical() string {
	return o.Name + ":" + o.Type + ":" + o.EffectiveTaint()
}

// Canonical devolve a forma canónica da aresta de dados: `from:output:tipo`.
func (c PlanConsume) Canonical() string {
	return c.From + ":" + c.Output + ":" + c.Type
}

// canonicalSeparators são os bytes que a forma canónica usa como estrutura. Uma
// REFERÊNCIA (task_id) que os contivesse tornaria a forma ambígua — e a
// não-ambiguidade desta gramática é por charset, não por escape.
const canonicalSeparators = "|{},=():"

// validCanonicalSymbol confere que s é um SÍMBOLO da forma canónica: 1..64 bytes, a
// começar por letra minúscula, de um charset ASCII FECHADO (minúsculas, dígitos, `_`,
// `.`).
//
// É a MESMA grammar do `plan.ValidIdentifier` do orquestrador, escrita aqui pela razão
// de sempre (o módulo não o importa). E é ELA que faz a regra de ouro do cartão ser
// estrutural: um valor de payload, um excerto de output ou um segredo não são
// identificadores minúsculos de 64 bytes — não passam, e o plano é recusado em vez de
// apresentado com conteúdo dentro.
func validCanonicalSymbol(s string) bool {
	if len(s) == 0 || len(s) > maxCanonicalSymbolLen {
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

// maxCanonicalSymbolLen é o tecto de um símbolo da forma canónica. Não é política do
// operador: é o limite que mantém a expressão legível numa linha do cartão — e que
// nega, por tamanho, o uso do campo como transporte.
const maxCanonicalSymbolLen = 64

// validCanonicalOperand confere que s é um operando admissível: um SÍMBOLO
// ([validCanonicalSymbol]) ou um INTEIRO em forma decimal CANÓNICA.
//
// A canonicidade do inteiro é imposta por ida-e-volta (`ParseInt` seguido de
// `FormatInt` que tem de devolver a MESMA string): `007`, `+1`, ` 1` e `1_000` são
// recusados. Não é pedantismo — é o que impede o campo de veicular bytes arbitrários
// que um parser tolerante deixaria passar.
func validCanonicalOperand(s string) bool {
	if validCanonicalSymbol(s) {
		return true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && strconv.FormatInt(n, 10) == s
}

// validCanonicalRef confere que s é uma REFERÊNCIA estrutural apresentável: não-vazia,
// ASCII imprimível sem espaços e sem nenhum dos [canonicalSeparators].
//
// É mais frouxa que [validCanonicalSymbol] de propósito: um task_id é do documento de
// plano e a sua grammar é do validador a montante — o gate não a re-decide. Impõe só o
// que a APRESENTAÇÃO exige: uma referência que não parta a forma canónica nem carregue
// controlo/whitespace para dentro de uma linha do cartão.
func validCanonicalRef(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c > '~' {
			return false
		}
		if strings.IndexByte(canonicalSeparators, c) >= 0 {
			return false
		}
	}
	return true
}

// IsVerifier indica se o nó declara o papel RESERVADO [RoleVerifier] — a distinção que
// o humano no gate precisa de fazer entre quem PRODUZ e quem JULGA (ADR-022 §2.2).
func (n PlanNode) IsVerifier() bool { return n.Role == RoleVerifier }

// hasExtensions indica se o nó declara ALGUMA das extensões de ADR-022. É o predicado
// que decide se o cartão emite de todo a projecção: um plano sem extensões produz um
// cartão IDÊNTICO ao de antes (retrocompatibilidade — ver [buildNodeExtensions]).
func (n PlanNode) hasExtensions() bool {
	return n.Role != "" || len(n.ConditionalOn) != 0 || len(n.Outputs) != 0 || len(n.Consumes) != 0
}

// validateExtensions impõe a FORMA canónica das extensões de um nó — a imposição
// estrutural da regra de ouro do cartão. `known` é o conjunto de task_ids do plano: as
// referências (`from`) têm de existir, senão o cartão mostraria uma condição sobre um
// nó que ninguém vai correr. `declared` é o conjunto de arestas de precedência
// DECLARADAS do plano ([Plan.Edges]), usado pela invariante de montante do `consumes`.
//
// Fail-closed com [ErrNonCanonicalExtension]: NADA é saneado, truncado nem descartado.
// Um plano cuja extensão não caiba na forma canónica não é aprovável — porque
// apresentá-lo exigiria ou mostrar conteúdo, ou mentir por omissão.
func (n PlanNode) validateExtensions(known map[string]bool, declared map[[2]string]bool) error {
	if n.Role != "" && !validCanonicalSymbol(n.Role) {
		return ErrNonCanonicalExtension
	}
	for _, c := range n.ConditionalOn {
		if !validCanonicalRef(c.From) || !known[c.From] || c.From == n.TaskID {
			return ErrNonCanonicalExtension
		}
		if len(c.When) == 0 {
			return ErrNonCanonicalExtension
		}
		for _, p := range c.When {
			if !validCanonicalSymbol(p.Subject) || !validCanonicalSymbol(p.Op) {
				return ErrNonCanonicalExtension
			}
			if p.Metric != "" && !validCanonicalSymbol(p.Metric) {
				return ErrNonCanonicalExtension
			}
			if !validCanonicalOperand(p.Operand) {
				return ErrNonCanonicalExtension
			}
		}
	}
	for _, o := range n.Outputs {
		if !validCanonicalSymbol(o.Name) || !validCanonicalSymbol(o.Type) {
			return ErrNonCanonicalExtension
		}
		if o.Taint != "" && !validCanonicalSymbol(o.Taint) {
			return ErrNonCanonicalExtension
		}
	}
	for _, c := range n.Consumes {
		// A FORMA primeiro (nome e tipo), depois a REFERÊNCIA, depois a topologia. A
		// ordem é de ATRIBUIÇÃO, não de dependência: uma tentativa de empurrar conteúdo
		// pelo nome ou pelo tipo do consumo tem de morrer na regra que nomeia a forma,
		// mesmo quando a aresta também está mal declarada.
		if !validCanonicalSymbol(c.Output) || !validCanonicalSymbol(c.Type) {
			return ErrNonCanonicalExtension
		}
		if !validCanonicalRef(c.From) || !known[c.From] || c.From == n.TaskID {
			return ErrNonCanonicalExtension
		}
		// INVARIANTE DE MONTANTE, ESPELHADA (ADR-022 §2.3, `plan/plandocument.go`): a
		// origem de um `consumes` TEM de ser uma aresta de entrada JÁ DECLARADA do nó —
		// por precedência ou por condição. O argumento que fez a aresta condicional contar
		// na ordenação ([Plan.effectiveEdges]) aplica-se textualmente ao consumo: um cartão
		// que mostrasse «leio o output de X» num nó que, na ordem apresentada, corre ANTES
		// de X, estaria a mostrar um fluxo de dados que não é o que vai acontecer — e o
		// humano leria uma corrida com o produtor como um contrato cumprido.
		//
		// ESPELHA em vez de duplicar: o gate não re-decide a gramática do validador puro
		// (nome do output, tipo, taint, autoridade do consumidor) — só recusa a aresta que
		// não existe, que é a que ele PRÓPRIO iria desenhar mal. Para planos vindos de um
		// PlanDocument validado o caso não ocorre; para `Plan` construídos à mão (o único
		// consumidor real hoje) ocorre, e é aí que a defesa vale.
		if !declared[[2]string{c.From, n.TaskID}] && !n.conditionalFrom(c.From) {
			return ErrNonCanonicalExtension
		}
	}
	return nil
}

// conditionalFrom indica se o nó tem uma aresta *condicional* a observar `from` — o segundo
// canal por onde uma origem pode ser aresta de entrada declarada deste nó.
func (n PlanNode) conditionalFrom(from string) bool {
	for _, c := range n.ConditionalOn {
		if c.From == from {
			return true
		}
	}
	return false
}

// extensionSignature é a assinatura canónica e determinística das extensões de um nó —
// papel, condições, outputs e consumos, na ordem declarada. Serve o DIFF de edição
// ([DiffPlans]): sem ela, uma edição que transformasse um verificador num produtor, ou
// que reescrevesse a condição de um ramo, aparecia no registo da decisão como «sem
// mudança estrutural» — nós e arestas iguais, execução diferente.
func (n PlanNode) extensionSignature() string {
	outs := make([]string, 0, len(n.Outputs))
	for _, o := range n.Outputs {
		outs = append(outs, o.Canonical())
	}
	ins := make([]string, 0, len(n.Consumes))
	for _, c := range n.Consumes {
		ins = append(ins, c.Canonical())
	}
	return n.Role + "|" + CanonicalConditions(n.ConditionalOn) +
		"|" + strings.Join(outs, ",") + "|" + strings.Join(ins, ",")
}

// CardCondition é UMA aresta condicional TAL COMO O CARTÃO A APRESENTA: a origem
// (para a ligar ao organigrama) e a condição em forma canónica (para o humano a ler).
// Content-free por construção — ver a nota de topo.
type CardCondition struct {
	// From é o task_id da origem observada.
	From string `json:"from"`
	// Canonical é a conjunção em forma canónica, ex.: `verdict=eq=fail` ou
	// `metric(fontes)=lt=3`.
	Canonical string `json:"canonical"`
}

// NodeExtension é a PROJECÇÃO das extensões de ADR-022 de UM nó no [PlanCard] — a peça
// que faltava para o invariante §2.4(5) ser verdade. Emitida na MESMA ordem de
// [PlanCard.Order], como [NodeReview].
type NodeExtension struct {
	// TaskID é o nó a que esta projecção se refere.
	TaskID string `json:"task_id"`
	// Role é o papel declarado do nó (vazio ⇒ não declarado).
	Role string `json:"role,omitempty"`
	// Verifier é o rótulo que o humano precisa de ver ao primeiro olhar: este nó JULGA
	// o trabalho de outros (ADR-022 §2.2) em vez de o produzir.
	Verifier bool `json:"verifier"`
	// Conditions são as arestas condicionais que GOVERNAM A ENTRADA deste nó — sob que
	// condição este ramo corre de todo.
	Conditions []CardCondition `json:"conditions,omitempty"`
	// Outputs são os contratos de saída em forma canónica `nome:tipo:taint`.
	Outputs []string `json:"outputs,omitempty"`
	// Consumes são as arestas de dados em forma canónica `origem:output:tipo` — QUE
	// trabalho de QUEM entra neste nó.
	Consumes []string `json:"consumes,omitempty"`
}

// HasAny indica se esta entrada projecta alguma extensão (uma entrada toda vazia
// existe só para manter o alinhamento posicional com [PlanCard.Order]).
func (e NodeExtension) HasAny() bool {
	return e.Role != "" || len(e.Conditions) != 0 || len(e.Outputs) != 0 || len(e.Consumes) != 0
}

// validate impõe a forma canónica sobre a PROJECÇÃO — o wire do cartão —, e não só sobre
// o caminho de construção.
//
// PORQUE EXISTE (a falha-anterior, concreta). A regra de ouro do cartão foi declarada
// «content-free por ESTRUTURA, não por convenção», mas [PlanNode.validateExtensions] só
// corre em [Plan.Validate]/[BuildPlanCard]. O wire é «o contrato que os adaptadores
// consomem» e entra por [PlanCard.UnmarshalJSON] — que validava contagens e nada mais:
// `role`, `conditions[].canonical`, `outputs[]` e `consumes[]` eram strings livres na
// desserialização. Havia por isso uma porta por onde um cartão entrava sem passar pela
// porta que impõe a forma — e um cartão de wire com
// `outputs: ["resumo:summary:untrusted — o cliente disse: …"]` era apresentado ao
// aprovador e SELADO com a decisão.
//
// O que se impõe é a MESMA gramática, re-parseada a partir das strings do wire: a forma
// canónica é não-ambígua por CHARSET (não por escape), pelo que re-parseá-la é decidível
// e barato. Fail-closed com [ErrNonCanonicalExtension].
func (e NodeExtension) validate() error {
	if !validCanonicalRef(e.TaskID) {
		return ErrNonCanonicalExtension
	}
	if e.Role != "" && !validCanonicalSymbol(e.Role) {
		return ErrNonCanonicalExtension
	}
	// COERÊNCIA DO RÓTULO: `verifier` é a leitura ao primeiro olhar do humano e tem de
	// dizer o mesmo que o papel. Um cartão de wire com `role: "researcher"` e
	// `verifier: true` (ou o inverso) apresentaria quem produz como quem julga.
	if e.Verifier != (e.Role == RoleVerifier) {
		return ErrNonCanonicalExtension
	}
	for _, c := range e.Conditions {
		from, preds, ok := parseCanonicalCondition(c.Canonical)
		if !ok || from != c.From {
			return ErrNonCanonicalExtension
		}
		if len(preds) == 0 {
			return ErrNonCanonicalExtension
		}
	}
	for _, o := range e.Outputs {
		if !validCanonicalOutput(o) {
			return ErrNonCanonicalExtension
		}
	}
	for _, c := range e.Consumes {
		if !validCanonicalConsume(c) {
			return ErrNonCanonicalExtension
		}
	}
	return nil
}

// parseCanonicalCondition re-parseia `from{p1,p2,…}` contra a MESMA gramática que
// [PlanCondition.Canonical] produz, devolvendo a origem e os predicados. Não interpreta
// nada: só confirma que cada peça é um símbolo, um operando ou uma referência — o
// contrário seria o gate a tornar-se a segunda autoridade de gramática que a nota de topo
// recusa.
func parseCanonicalCondition(s string) (string, []string, bool) {
	open := strings.IndexByte(s, '{')
	if open <= 0 || len(s) == 0 || s[len(s)-1] != '}' {
		return "", nil, false
	}
	from := s[:open]
	if !validCanonicalRef(from) {
		return "", nil, false
	}
	body := s[open+1 : len(s)-1]
	if body == "" {
		return "", nil, false
	}
	preds := strings.Split(body, ",")
	for _, p := range preds {
		if !validCanonicalPredicate(p) {
			return "", nil, false
		}
	}
	return from, preds, true
}

// validCanonicalPredicate confere `subject=op=operand` ou `subject(metric)=op=operand`.
func validCanonicalPredicate(s string) bool {
	parts := strings.Split(s, "=")
	if len(parts) != 3 {
		return false
	}
	subject := parts[0]
	if open := strings.IndexByte(subject, '('); open >= 0 {
		if subject[len(subject)-1] != ')' {
			return false
		}
		metric := subject[open+1 : len(subject)-1]
		subject = subject[:open]
		if !validCanonicalSymbol(metric) {
			return false
		}
	}
	return validCanonicalSymbol(subject) && validCanonicalSymbol(parts[1]) && validCanonicalOperand(parts[2])
}

// validCanonicalOutput confere `nome:tipo:taint`. O taint tem de ser um dos DOIS rótulos
// do reticulado: [PlanOutput.Canonical] emite sempre o taint EFECTIVO, pelo que qualquer
// outra coisa no wire é um rótulo que este módulo nunca produziu.
func validCanonicalOutput(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return false
	}
	if parts[2] != TaintTrusted && parts[2] != TaintUntrusted {
		return false
	}
	return validCanonicalSymbol(parts[0]) && validCanonicalSymbol(parts[1])
}

// validCanonicalConsume confere `origem:output:tipo`. A origem é uma REFERÊNCIA (a
// grammar do task_id é do validador a montante); as outras duas são símbolos.
func validCanonicalConsume(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return false
	}
	return validCanonicalRef(parts[0]) && validCanonicalSymbol(parts[1]) && validCanonicalSymbol(parts[2])
}

// buildNodeExtensions computa a projecção por-nó na ORDEM dada (a ordem topológica do
// cartão).
//
// RETROCOMPATIBILIDADE POR AUSÊNCIA: se NENHUM nó do plano declara extensões, devolve
// nil — e o campo, sendo `omitempty`, desaparece do wire. Um plano pré-ADR-022 produz
// por isso um cartão indistinguível do que produzia antes deste ticket (salvo o
// carimbo de versão, que é o sinal honesto de que o contrato cresceu). O custo de
// apresentação só é pago por quem usa as extensões.
func buildNodeExtensions(order []string, byID map[string]PlanNode) []NodeExtension {
	any := false
	for _, id := range order {
		if byID[id].hasExtensions() {
			any = true
			break
		}
	}
	if !any {
		return nil
	}
	out := make([]NodeExtension, 0, len(order))
	for _, id := range order {
		n := byID[id]
		e := NodeExtension{TaskID: id, Role: n.Role, Verifier: n.IsVerifier()}
		for _, c := range n.ConditionalOn {
			e.Conditions = append(e.Conditions, CardCondition{From: c.From, Canonical: c.Canonical()})
		}
		for _, o := range n.Outputs {
			e.Outputs = append(e.Outputs, o.Canonical())
		}
		for _, c := range n.Consumes {
			e.Consumes = append(e.Consumes, c.Canonical())
		}
		out = append(out, e)
	}
	return out
}

// VerifierTaskIDs devolve os task_ids dos nós com papel [RoleVerifier], na ordem
// topológica do cartão. É o conjunto que uma superfície de revisão destaca (e que uma
// política de gate pode exigir que seja revisto item-a-item, a par de
// [PlanCard.ForcedTaskIDs]).
func (c PlanCard) VerifierTaskIDs() []string {
	var out []string
	for _, e := range c.NodeExtensions {
		if e.Verifier {
			out = append(out, e.TaskID)
		}
	}
	return out
}

// VerificationLink é uma linha da vista «QUEM verifica QUEM»: um nó verificador e o
// conjunto de nós cujo trabalho lhe entra.
type VerificationLink struct {
	// Verifier é o task_id do nó com papel verificador.
	Verifier string `json:"verifier"`
	// Verifies são os task_ids cujo trabalho entra no verificador — a UNIÃO das três
	// vias de entrada declaradas: precedência ([PlanCard.Edges]), aresta condicional e
	// aresta de dados. Ordenados (determinista) e sem duplicados.
	Verifies []string `json:"verifies,omitempty"`
	// Under são as condições em forma canónica que governam a entrada do verificador —
	// sob que condição a verificação corre de todo.
	Under []string `json:"under,omitempty"`
}

// VerificationView devolve a vista «quem verifica quem» do cartão: por cada nó
// verificador, os nós cujo trabalho lhe entra e as condições que governam a sua
// entrada. É a leitura que o invariante §2.4(5) pede em uma frase — e que uma lista de
// nós com arestas de precedência não dava.
//
// Deriva-se do que o cartão JÁ carrega (as extensões projectadas + as arestas), não de
// uma segunda fonte: a união das três vias de entrada é feita aqui porque o
// organigrama do ADR — produtor → verificador → consumidor privilegiado — pode
// escrever a mesma ligação por qualquer uma delas. Determinista: verificadores na
// ordem topológica, origens ordenadas lexicograficamente.
func (c PlanCard) VerificationView() []VerificationLink {
	var out []VerificationLink
	for _, e := range c.NodeExtensions {
		if !e.Verifier {
			continue
		}
		srcs := make(map[string]bool)
		for _, edge := range c.Edges {
			if edge[1] == e.TaskID {
				srcs[edge[0]] = true
			}
		}
		link := VerificationLink{Verifier: e.TaskID}
		for _, cond := range e.Conditions {
			srcs[cond.From] = true
			link.Under = append(link.Under, cond.Canonical)
		}
		for _, cons := range e.Consumes {
			if from, _, ok := strings.Cut(cons, ":"); ok {
				srcs[from] = true
			}
		}
		for id := range srcs {
			link.Verifies = append(link.Verifies, id)
		}
		sort.Strings(link.Verifies)
		out = append(out, link)
	}
	return out
}
