package planapproval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// def274_extensions_test.go — O GATE VÊ AS EXTENSÕES DE ADR-022 (invariante §2.4(5)).
//
// O que estes testes falsificam é uma afirmação concreta e que já foi falsa: «o humano
// no gate vê o organigrama COM as condições e os verificadores declarados». Antes de
// DEF-274 o cartão mostrava nós e arestas de precedência, e um plano com verificador e
// ramo de qualidade chegava ao aprovador como uma lista — aprovava-se um organigrama
// que não era o que ia correr.
//
// O caso positivo (o organigrama canónico do ADR: produtor → verificador → remediação
// condicional) e o caso NEGATIVO (um plano sem extensões produz o cartão de sempre)
// estão ambos aqui, e a regra de ouro do cartão é falsificada por um sentinela de
// segredo em CADA campo da porta.

// segredoSentinela é o material que NUNCA pode entrar no cartão. Serve dois papéis:
// (a) o payload hostil de cada caso de recusa, (b) a agulha procurada no wire do cartão
// legítimo.
const segredoSentinela = "sk-live-DEADBEEF/o cliente disse: demite o Joao"

// planoComVerificador devolve o organigrama canónico de ADR-022: um produtor
// (`pesquisa`), um VERIFICADOR read-only que consome o resumo e publica um veredicto
// tipado (`revisao`), e uma remediação que só entra se o veredicto REPROVAR ou as
// fontes forem poucas (`suplemento`). Nenhuma aresta de precedência liga
// revisao→suplemento: a ligação é a aresta *condicional*, e é isso que se quer provar.
func planoComVerificador() Plan {
	return Plan{
		RunID:  "run-def274",
		Agent:  "agent:planner",
		Domain: "http",
		Nodes: []PlanNode{
			{
				TaskID: "pesquisa", Class: risk.ClassSafe, Capability: "http.get",
				Resource: "https://x/fontes", Preview: "cap:http.get -> https://x/fontes",
				Role:    "researcher",
				Outputs: []PlanOutput{{Name: "resumo", Type: "summary"}},
			},
			{
				TaskID: "revisao", Class: risk.ClassSafe, Capability: "fs.read",
				Resource: "mem://run/pesquisa", Preview: "cap:fs.read -> mem://run/pesquisa",
				Role:     RoleVerifier,
				Outputs:  []PlanOutput{{Name: "veredicto", Type: "verdict", Taint: TaintTrusted}},
				Consumes: []PlanConsume{{From: "pesquisa", Output: "resumo", Type: "summary"}},
			},
			{
				TaskID: "suplemento", Class: risk.ClassGray, Capability: "http.get",
				Resource: "https://x/suplemento", Preview: "cap:http.get -> https://x/suplemento",
				Role: "researcher",
				ConditionalOn: []PlanCondition{{
					From: "revisao",
					When: []PlanPredicate{
						{Subject: "verdict", Op: "eq", Operand: "fail"},
						{Subject: "metric", Metric: "fontes", Op: "lt", Operand: "3"},
					},
				}},
				Consumes: []PlanConsume{{From: "revisao", Output: "veredicto", Type: "verdict"}},
			},
		},
		Edges: [][2]string{{"pesquisa", "revisao"}},
	}
}

// TestDEF274_CartaoMostraVerificadorERamoCondicional é o caso que interessa: um plano
// com verificador E ramo condicional produz um cartão onde AMBOS são legíveis — quem
// verifica quem, e sob que condição cada ramo entra.
func TestDEF274_CartaoMostraVerificadorERamoCondicional(t *testing.T) {
	card, err := BuildPlanCard(planoComVerificador())
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}

	// A projecção existe e é POSICIONAL (uma entrada por nó da ordem) — uma lista
	// parcial faria o humano ler ausência como «não tem condições».
	if len(card.NodeExtensions) != len(card.Order) {
		t.Fatalf("node_extensions %d != order %d", len(card.NodeExtensions), len(card.Order))
	}

	byID := map[string]NodeExtension{}
	for _, e := range card.NodeExtensions {
		byID[e.TaskID] = e
	}

	// (1) O PAPEL: o humano distingue quem JULGA de quem PRODUZ.
	if !byID["revisao"].Verifier || byID["revisao"].Role != RoleVerifier {
		t.Fatalf("o no de revisao nao esta marcado como verificador: %+v", byID["revisao"])
	}
	if byID["pesquisa"].Verifier {
		t.Fatal("um produtor foi marcado como verificador")
	}
	if got := card.VerifierTaskIDs(); len(got) != 1 || got[0] != "revisao" {
		t.Fatalf("VerifierTaskIDs = %v (esperava [revisao])", got)
	}

	// (2) A CONDIÇÃO: o ramo de remediação diz SOB QUE CONDIÇÃO entra, em forma
	// canónica legível — e nomeia a origem observada.
	conds := byID["suplemento"].Conditions
	if len(conds) != 1 || conds[0].From != "revisao" {
		t.Fatalf("condicoes do suplemento: %+v (esperava 1, de revisao)", conds)
	}
	// Este literal é a ISOMORFIA com a `plan.CanonicalConditional` do orquestrador,
	// PINADA aqui: o módulo é desacoplado (não importa `orchestrator/plan`), pelo que
	// não há como cruzar as duas formas em código — o pino é o preço declarado do
	// desacoplamento, e é ele que apanha uma deriva de gramática.
	const querido = "revisao{verdict=eq=fail,metric(fontes)=lt=3}"
	if conds[0].Canonical != querido {
		t.Fatalf("forma canonica = %q, esperava %q", conds[0].Canonical, querido)
	}

	// (3) OS CONTRATOS DE DADOS: que trabalho de quem entra em cada nó, com que tipo e
	// que rótulo de taint — nunca o conteúdo.
	if got := byID["revisao"].Consumes; len(got) != 1 || got[0] != "pesquisa:resumo:summary" {
		t.Fatalf("consumes da revisao = %v", got)
	}
	if got := byID["revisao"].Outputs; len(got) != 1 || got[0] != "veredicto:verdict:trusted" {
		t.Fatalf("outputs da revisao = %v", got)
	}
	// O output do produtor NÃO tem taint declarado — o cartão mostra o pior caso.
	if got := byID["pesquisa"].Outputs; len(got) != 1 || got[0] != "resumo:summary:untrusted" {
		t.Fatalf("outputs da pesquisa = %v (esperava taint fail-closed untrusted)", got)
	}

	// (4) QUEM VERIFICA QUEM, numa leitura só.
	view := card.VerificationView()
	if len(view) != 1 || view[0].Verifier != "revisao" {
		t.Fatalf("VerificationView = %+v", view)
	}
	if len(view[0].Verifies) != 1 || view[0].Verifies[0] != "pesquisa" {
		t.Fatalf("revisao verifica %v (esperava [pesquisa])", view[0].Verifies)
	}

	// (5) A ORDEM APRESENTADA É A QUE VAI CORRER: nada liga revisao→suplemento por
	// precedência declarada; é a aresta condicional que o faz, e a ordenação conta-a.
	pos := map[string]int{}
	for i, id := range card.Order {
		pos[id] = i
	}
	if !(pos["pesquisa"] < pos["revisao"] && pos["revisao"] < pos["suplemento"]) {
		t.Fatalf("ordem do cartao %v nao respeita a aresta condicional", card.Order)
	}
}

// TestDEF274_ExtensoesViajamNoWire prova que a projecção sobrevive à serialização (é o
// contrato que os adaptadores de plataforma consomem, não só uma vista em memória) e
// volta a validar na desserialização.
func TestDEF274_ExtensoesViajamNoWire(t *testing.T) {
	card, err := BuildPlanCard(planoComVerificador())
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, agulha := range []string{
		`"node_extensions"`, `"verifier":true`,
		"revisao{verdict=eq=fail,metric(fontes)=lt=3}",
		"veredicto:verdict:trusted", "pesquisa:resumo:summary",
	} {
		if !strings.Contains(string(raw), agulha) {
			t.Fatalf("o wire do cartao nao mostra %q:\n%s", agulha, raw)
		}
	}

	var back PlanCard
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.NodeExtensions) != len(card.NodeExtensions) {
		t.Fatalf("extensoes perdidas na ida-e-volta: %d != %d", len(back.NodeExtensions), len(card.NodeExtensions))
	}
	if got := back.VerifierTaskIDs(); len(got) != 1 || got[0] != "revisao" {
		t.Fatalf("verificador perdido na ida-e-volta: %v", got)
	}
}

// TestDEF274_PlanoSemExtensoesFicaComoAntes é o caso NEGATIVO — retrocompatibilidade.
// Um plano pré-ADR-022 não paga nada pela extensão: nenhuma projecção, nenhum campo
// novo no wire, nenhuma vista a inventar verificadores que não existem.
func TestDEF274_PlanoSemExtensoesFicaComoAntes(t *testing.T) {
	card, err := BuildPlanCard(samplePlan("run-legado", risk.ClassSafe))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	if card.NodeExtensions != nil {
		t.Fatalf("plano sem extensoes projectou %+v", card.NodeExtensions)
	}
	if card.VerifierTaskIDs() != nil || card.VerificationView() != nil {
		t.Fatal("plano sem extensoes inventou verificadores")
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "node_extensions") {
		t.Fatalf("o wire de um plano legado ganhou o campo novo:\n%s", raw)
	}
	// A ordem e as arestas continuam a ser as de sempre (sem condições, as arestas
	// efectivas SÃO as declaradas).
	if strings.Join(card.Order, ",") != "a,b,c,d" {
		t.Fatalf("ordem do plano legado mudou: %v", card.Order)
	}
}

// TestDEF274_FormaNaoCanonicaERecusada é a regra de ouro do cartão com força de
// estrutura: por CADA campo da porta, tentar empurrar conteúdo do run recusa o plano —
// não o sanea, não o trunca, não o apresenta com o conteúdo lá dentro.
func TestDEF274_FormaNaoCanonicaERecusada(t *testing.T) {
	casos := map[string]func(n *PlanNode){
		"papel com texto livre": func(n *PlanNode) { n.Role = segredoSentinela },
		"observavel com conteudo": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "pesquisa", When: []PlanPredicate{{Subject: segredoSentinela, Op: "eq", Operand: "fail"}}}}
		},
		"operador com conteudo": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "pesquisa", When: []PlanPredicate{{Subject: "verdict", Op: segredoSentinela, Operand: "fail"}}}}
		},
		"operando com conteudo (o caso obvio)": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "pesquisa", When: []PlanPredicate{{Subject: "verdict", Op: "eq", Operand: segredoSentinela}}}}
		},
		"nome de metrica com conteudo": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "pesquisa", When: []PlanPredicate{{Subject: "metric", Metric: segredoSentinela, Op: "lt", Operand: "3"}}}}
		},
		"inteiro nao canonico": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "pesquisa", When: []PlanPredicate{{Subject: "metric", Metric: "fontes", Op: "lt", Operand: "007"}}}}
		},
		"conjuncao vazia": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "pesquisa"}}
		},
		"origem inexistente": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "fantasma", When: []PlanPredicate{{Subject: "verdict", Op: "eq", Operand: "fail"}}}}
		},
		"origem a apontar para si proprio": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "suplemento", When: []PlanPredicate{{Subject: "verdict", Op: "eq", Operand: "fail"}}}}
		},
		"separador na referencia": func(n *PlanNode) {
			n.ConditionalOn = []PlanCondition{{From: "revisao{x}", When: []PlanPredicate{{Subject: "verdict", Op: "eq", Operand: "fail"}}}}
		},
		"nome de output com conteudo": func(n *PlanNode) {
			n.Outputs = []PlanOutput{{Name: segredoSentinela, Type: "summary"}}
		},
		"tipo de output com conteudo": func(n *PlanNode) {
			n.Outputs = []PlanOutput{{Name: "resumo", Type: segredoSentinela}}
		},
		"taint com conteudo": func(n *PlanNode) {
			n.Outputs = []PlanOutput{{Name: "resumo", Type: "summary", Taint: segredoSentinela}}
		},
		"consumo com origem inexistente": func(n *PlanNode) {
			n.Consumes = []PlanConsume{{From: "fantasma", Output: "resumo", Type: "summary"}}
		},
		"nome de output consumido com conteudo": func(n *PlanNode) {
			n.Consumes = []PlanConsume{{From: "pesquisa", Output: segredoSentinela, Type: "summary"}}
		},
		// O campo que faltava à afirmação «sentinela em CADA campo»: sem este caso, apagar
		// a validação do TIPO do consumo deixava todos os testes verdes e um wiring podia
		// empurrar texto livre pelo terceiro segmento da forma `origem:output:tipo`.
		"tipo de consumo com conteudo": func(n *PlanNode) {
			n.Consumes = []PlanConsume{{From: "pesquisa", Output: "resumo", Type: segredoSentinela}}
		},
		// A invariante de MONTANTE espelhada: consumir o trabalho de um nó de que não se
		// espera é ler numa corrida com o produtor — e o cartão mostraria o fluxo de dados
		// ao lado de uma ordem em que a origem ainda não correu.
		"consumo de origem que nao e aresta de entrada": func(n *PlanNode) {
			n.Consumes = []PlanConsume{{From: "pesquisa", Output: "resumo", Type: "summary"}}
		},
	}

	for nome, hostil := range casos {
		t.Run(nome, func(t *testing.T) {
			p := planoComVerificador()
			for i := range p.Nodes {
				if p.Nodes[i].TaskID == "suplemento" {
					// Limpa as extensões legítimas do nó antes de aplicar a hostil, para
					// que a recusa seja atribuível ao caso e não a um resíduo.
					p.Nodes[i].Role, p.Nodes[i].ConditionalOn = "", nil
					p.Nodes[i].Outputs, p.Nodes[i].Consumes = nil, nil
					hostil(&p.Nodes[i])
				}
			}
			card, err := BuildPlanCard(p)
			if !errors.Is(err, ErrNonCanonicalExtension) {
				t.Fatalf("erro = %v (esperava ErrNonCanonicalExtension); cartao = %+v", err, card)
			}
		})
	}

	// E o complemento: o cartão do plano LEGÍTIMO não contém o sentinela em lado nenhum
	// — não há caminho por onde ele entre.
	card, err := BuildPlanCard(planoComVerificador())
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sk-live") || strings.Contains(string(raw), "demite") {
		t.Fatalf("o cartao carrega material do run:\n%s", raw)
	}
}

// TestDEF274_ArestaCondicionalNaoFechaCiclo — o invariante 1 do ADR (aciclicidade na
// admissão) vale TAMBÉM no cartão, e sem uma travessia nova: como a aresta condicional
// conta como precedência, o detector de ciclo de sempre apanha-a.
func TestDEF274_ArestaCondicionalNaoFechaCiclo(t *testing.T) {
	p := planoComVerificador()
	for i := range p.Nodes {
		if p.Nodes[i].TaskID == "pesquisa" {
			p.Nodes[i].ConditionalOn = []PlanCondition{{
				From: "suplemento",
				When: []PlanPredicate{{Subject: "terminal_state", Op: "eq", Operand: "complete"}},
			}}
		}
	}
	if _, err := BuildPlanCard(p); !errors.Is(err, ErrPlanCycle) {
		t.Fatalf("erro = %v (esperava ErrPlanCycle)", err)
	}
}

// TestDEF274_EdicaoDeExtensaoAparECeNoDiff — uma edição que troca o papel de um nó ou
// reescreve a condição de um ramo muda a EXECUÇÃO sem mudar nós nem arestas. Sem o eixo
// de extensões, o registo da decisão dizia «sem mudança estrutural».
func TestDEF274_EdicaoDeExtensaoAparECeNoDiff(t *testing.T) {
	antes := planoComVerificador()

	// (a) o verificador deixa de o ser — mesmo grafo, outra execução.
	depois := planoComVerificador()
	for i := range depois.Nodes {
		if depois.Nodes[i].TaskID == "revisao" {
			depois.Nodes[i].Role = "researcher"
		}
	}
	d := DiffPlans(antes, depois)
	if d.Empty() {
		t.Fatal("troca de papel do verificador registada como plano inalterado")
	}
	if len(d.ChangedExtensions) != 1 || d.ChangedExtensions[0] != "revisao" {
		t.Fatalf("changed_extensions = %v (esperava [revisao])", d.ChangedExtensions)
	}
	if len(d.AddedNodes)+len(d.RemovedNodes)+len(d.AddedEdges)+len(d.RemovedEdges) != 0 {
		t.Fatalf("diff estrutural inventado: %+v", d)
	}

	// (b) a condição do ramo é reescrita (fail → pass): o ramo de remediação passa a
	// entrar no caso oposto.
	invertido := planoComVerificador()
	for i := range invertido.Nodes {
		if invertido.Nodes[i].TaskID == "suplemento" {
			invertido.Nodes[i].ConditionalOn[0].When[0].Operand = "pass"
		}
	}
	d2 := DiffPlans(antes, invertido)
	if len(d2.ChangedExtensions) != 1 || d2.ChangedExtensions[0] != "suplemento" {
		t.Fatalf("changed_extensions = %v (esperava [suplemento])", d2.ChangedExtensions)
	}

	// (c) o plano idêntico a si mesmo não inventa mudança nenhuma.
	if !DiffPlans(antes, planoComVerificador()).Empty() {
		t.Fatal("diff inventou mudanca entre dois planos iguais")
	}
}

// TestDEF274_FormaCanonicaEstavelEntreGemeosIndependentes prova ESTABILIDADE sem cair
// na tautologia de comparar uma função consigo própria: os dois lados são structs
// CONSTRUÍDOS POR CAMINHOS DIFERENTES (um literal completo, outro montado campo-a-campo
// e por append) com os MESMOS valores. O cartão tem de ser byte-a-byte o mesmo.
func TestDEF274_FormaCanonicaEstavelEntreGemeosIndependentes(t *testing.T) {
	literal := PlanNode{
		TaskID: "n", Role: RoleVerifier,
		ConditionalOn: []PlanCondition{{From: "src", When: []PlanPredicate{
			{Subject: "verdict", Op: "eq", Operand: "fail"},
			{Subject: "metric", Metric: "fontes", Op: "gte", Operand: "-2"},
		}}},
		Outputs:  []PlanOutput{{Name: "veredicto", Type: "verdict", Taint: TaintTrusted}},
		Consumes: []PlanConsume{{From: "src", Output: "resumo", Type: "summary"}},
	}

	var gemeo PlanNode
	gemeo.TaskID = "n"
	gemeo.Role = RoleVerifier
	cond := PlanCondition{From: "src"}
	cond.When = append(cond.When, PlanPredicate{Subject: "verdict", Op: "eq", Operand: "fail"})
	p := PlanPredicate{Subject: "metric", Op: "gte"}
	p.Metric = "fontes"
	p.Operand = "-2"
	cond.When = append(cond.When, p)
	gemeo.ConditionalOn = append(gemeo.ConditionalOn, cond)
	out := PlanOutput{Name: "veredicto"}
	out.Type, out.Taint = "verdict", TaintTrusted
	gemeo.Outputs = append(gemeo.Outputs, out)
	gemeo.Consumes = append(gemeo.Consumes, PlanConsume{From: "src", Output: "resumo", Type: "summary"})

	if literal.extensionSignature() != gemeo.extensionSignature() {
		t.Fatalf("assinaturas divergentes:\n%s\n%s", literal.extensionSignature(), gemeo.extensionSignature())
	}
	const querida = "src{verdict=eq=fail,metric(fontes)=gte=-2}"
	if got := CanonicalConditions(literal.ConditionalOn); got != querida {
		t.Fatalf("forma canonica = %q, esperava %q", got, querida)
	}

	// E o cartão inteiro: dois planos gémeos, um wire idêntico.
	base := func(n PlanNode) Plan {
		return Plan{RunID: "r", Agent: "a", Nodes: []PlanNode{
			{TaskID: "src", Class: risk.ClassSafe, Capability: "http.get", Resource: "https://x", Preview: "cap:http.get -> https://x"},
			n,
		}}
	}
	nA := literal
	nA.Class, nA.Capability, nA.Resource, nA.Preview = risk.ClassSafe, "fs.read", "mem://r", "cap:fs.read -> mem://r"
	nB := gemeo
	nB.Class, nB.Capability, nB.Resource, nB.Preview = risk.ClassSafe, "fs.read", "mem://r", "cap:fs.read -> mem://r"

	cardA, errA := BuildPlanCard(base(nA))
	cardB, errB := BuildPlanCard(base(nB))
	if errA != nil || errB != nil {
		t.Fatalf("BuildPlanCard: %v / %v", errA, errB)
	}
	rawA, _ := json.Marshal(cardA)
	rawB, _ := json.Marshal(cardB)
	if string(rawA) != string(rawB) {
		t.Fatalf("cartoes de gemeos divergem:\n%s\n%s", rawA, rawB)
	}
}

// TestDEF274_TaintNaApresentacaoEFailClosed — o gate não deriva taint (isso é do
// orquestrador), mas nunca mostra um rótulo MAIS PERMISSIVO do que o que recebeu: só o
// símbolo exacto `trusted` mostra trusted.
func TestDEF274_TaintNaApresentacaoEFailClosed(t *testing.T) {
	casos := map[string]string{
		"":                  TaintUntrusted,
		TaintUntrusted:      TaintUntrusted,
		TaintTrusted:        TaintTrusted,
		"trusted_mesmo":     TaintUntrusted,
		"confidential.high": TaintUntrusted,
	}
	for dado, querido := range casos {
		if got := (PlanOutput{Name: "o", Type: "verdict", Taint: dado}).EffectiveTaint(); got != querido {
			t.Fatalf("taint %q apresentado como %q (esperava %q)", dado, got, querido)
		}
	}
}

// TestDEF274_BumpDoSchemaEMinor — o contrato do cartão MUDOU e o carimbo tem de o
// dizer; mas a mudança é ADITIVA, logo MINOR e não MAJOR: um cartão carimbado na linha
// anterior continua a desserializar aqui.
func TestDEF274_BumpDoSchemaEMinor(t *testing.T) {
	anterior := PlanCardSchemaVersion{Major: 1, Minor: 0, Patch: 0}
	if CurrentVersion.Compare(anterior) <= 0 {
		t.Fatalf("a versao corrente (%s) nao subiu face a %s", CurrentVersion, anterior)
	}
	if k := Classify(anterior, CurrentVersion); k != ChangeMinor {
		t.Fatalf("Classify = %s (esperava minor — a adicao e retrocompativel)", k)
	}
	if !CurrentVersion.Compatible(anterior) || !anterior.Compatible(CurrentVersion) {
		t.Fatal("o bump quebrou a compatibilidade de MAJOR")
	}

	// Um cartão de wire da linha anterior (sem `node_extensions`) continua a entrar.
	card, err := BuildPlanCard(samplePlan("run-antigo", risk.ClassSafe))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	antigo := strings.Replace(string(raw), `"schema_version":"1.1.0"`, `"schema_version":"1.0.0"`, 1)
	if antigo == string(raw) {
		t.Fatalf("o wire nao carimba a versao corrente:\n%s", raw)
	}
	var back PlanCard
	if err := json.Unmarshal([]byte(antigo), &back); err != nil {
		t.Fatalf("cartao da linha anterior rejeitado: %v", err)
	}
	if back.SchemaVersion.String() != "1.0.0" {
		t.Fatalf("versao reparseada = %s", back.SchemaVersion)
	}
}

// TestDEF274_ProjeccaoParcialERecusada — a coerência posicional é fail-closed. E não é só
// de COMPRIMENTO: uma lista do mesmo tamanho com entradas trocadas ou duplicadas é o mesmo
// defeito visto de outro lado — um nó fica sem projecção e o humano lê a ausência de
// condições como «este nó não tem condições», enquanto outro nó recebe a projecção alheia.
func TestDEF274_ProjeccaoParcialERecusada(t *testing.T) {
	casos := map[string]func(c *PlanCard){
		"truncada": func(c *PlanCard) {
			c.NodeExtensions = c.NodeExtensions[:1]
		},
		"mesmo comprimento, entrada duplicada": func(c *PlanCard) {
			// Duplica `pesquisa` e perde `suplemento` — o nó guardado por condição fica
			// sem projecção, que é a leitura mais perigosa das três.
			c.NodeExtensions[2] = c.NodeExtensions[0]
		},
		"mesmo comprimento, entradas trocadas": func(c *PlanCard) {
			c.NodeExtensions[0], c.NodeExtensions[1] = c.NodeExtensions[1], c.NodeExtensions[0]
		},
		"triagem desalinhada": func(c *PlanCard) {
			c.NodeReviews[0], c.NodeReviews[2] = c.NodeReviews[2], c.NodeReviews[0]
		},
	}
	for nome, mutar := range casos {
		t.Run(nome, func(t *testing.T) {
			card, err := BuildPlanCard(planoComVerificador())
			if err != nil {
				t.Fatalf("BuildPlanCard: %v", err)
			}
			mutar(&card)
			if err := card.Validate(); !errors.Is(err, ErrInvalidPlanCard) {
				t.Fatalf("erro = %v (esperava ErrInvalidPlanCard)", err)
			}
		})
	}
}

// TestDEF274_WireNaoCanonicoERecusado — a regra de ouro do cartão vale TAMBÉM na porta de
// ENTRADA. A falha-antes é literal: `validateExtensions` só corria em
// [Plan.Validate]/[BuildPlanCard], e o wire — «o contrato que os adaptadores consomem» —
// entrava por [PlanCard.UnmarshalJSON], que validava contagens e mais nada. Um adaptador
// de superfície (ou um cartão persistido e reeditado no ciclo de [VerdictEdit]) podia
// entregar um cartão com material do run dentro de `node_extensions` e esse material era
// apresentado ao aprovador e selado com a decisão.
//
// Este teste injecta o sentinela NO WIRE (não no [Plan]) — o simétrico exacto de
// [TestDEF274_FormaNaoCanonicaERecusada].
func TestDEF274_WireNaoCanonicoERecusado(t *testing.T) {
	card, err := BuildPlanCard(planoComVerificador())
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Controlo: o wire legítimo volta a entrar. Sem isto, os casos abaixo podiam estar a
	// falhar por o cartão nunca desserializar.
	var ok PlanCard
	if err := json.Unmarshal(raw, &ok); err != nil {
		t.Fatalf("o cartao legitimo devia desserializar: %v", err)
	}

	casos := map[string][2]string{
		"conteudo no contrato de saida": {
			`"resumo:summary:untrusted"`,
			`"resumo:summary:untrusted — o cliente disse: demite o Joao"`,
		},
		"conteudo dentro da forma canonica da condicao": {
			`revisao{verdict=eq=fail,metric(fontes)=lt=3}`,
			`revisao{verdict=eq=sk-live-DEADBEEF}`,
		},
		"conteudo no papel": {
			`"role":"researcher"`,
			`"role":"sk-live-DEADBEEF/demite o Joao"`,
		},
		"conteudo na aresta de dados": {
			`"pesquisa:resumo:summary"`,
			`"pesquisa:resumo:summary sk-live-DEADBEEF"`,
		},
		// O rótulo `verifier` é a leitura ao primeiro olhar: um wire que o afirme sobre um
		// nó cujo papel diz outra coisa apresenta quem produz como quem julga.
		"rotulo de verificador incoerente com o papel": {
			`"role":"verifier","verifier":true`,
			`"role":"researcher","verifier":true`,
		},
		// Um taint fora do reticulado nunca foi produzido por este módulo — e um rótulo
		// que o humano não sabe ler é pior do que o pior caso.
		"taint fora do reticulado": {
			`"veredicto:verdict:trusted"`,
			`"veredicto:verdict:confidencial"`,
		},
	}
	for nome, par := range casos {
		t.Run(nome, func(t *testing.T) {
			hostil := strings.Replace(string(raw), par[0], par[1], 1)
			if hostil == string(raw) {
				t.Fatalf("a agulha %q nao existe no wire — o caso nao exercita nada:\n%s", par[0], raw)
			}
			var back PlanCard
			err := json.Unmarshal([]byte(hostil), &back)
			if !errors.Is(err, ErrNonCanonicalExtension) {
				t.Fatalf("erro = %v (esperava ErrNonCanonicalExtension); cartao = %+v", err, back)
			}
		})
	}
}

// TestDEF274_GrafoEfectivoEReconstruivelDoWire — o cartão ordenava pelas arestas
// EFECTIVAS mas serializava só as DECLARADAS: dois campos do mesmo cartão descreviam
// grafos diferentes e nada o assinalava. Uma superfície que desenhasse o organigrama a
// partir de `edges` — o único campo com forma de grafo, e o mesmo que a edição humana
// devolve em `RevisedEdges` — mostrava `suplemento` como raiz SEM ENTRADA: um ramo de
// remediação a parecer que corre incondicionalmente desde o início.
//
// O contrato agora é explícito: `edges` continua a ser o canal declarado (não se
// corrompe o que o humano edita) e `conditional_edges` carrega as induzidas, com a
// validação a exigir que toda a condição projectada tenha a sua aresta num dos dois.
func TestDEF274_GrafoEfectivoEReconstruivelDoWire(t *testing.T) {
	card, err := BuildPlanCard(planoComVerificador())
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	// `edges` fica com o que foi DECLARADO — nem mais, nem menos.
	if len(card.Edges) != 1 || card.Edges[0] != [2]string{"pesquisa", "revisao"} {
		t.Fatalf("edges = %v (esperava so a declarada pesquisa->revisao)", card.Edges)
	}
	// E a ligação guardada por condição está no wire, como ARESTA.
	if len(card.ConditionalEdges) != 1 || card.ConditionalEdges[0] != [2]string{"revisao", "suplemento"} {
		t.Fatalf("conditional_edges = %v (esperava revisao->suplemento)", card.ConditionalEdges)
	}

	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"conditional_edges":[["revisao","suplemento"]]`) {
		t.Fatalf("o wire nao expoe a aresta condicional:\n%s", raw)
	}

	// A propriedade que interessa: o grafo EFECTIVO reconstruído SÓ a partir do wire dá a
	// mesma ordem que o humano aprovou. Sem `conditional_edges`, uma reconstrução a partir
	// de `edges` admitia `suplemento` antes de `revisao`.
	var back PlanCard
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	indeg := map[string]int{}
	for _, id := range back.Order {
		indeg[id] = 0
	}
	for _, e := range append(append([][2]string(nil), back.Edges...), back.ConditionalEdges...) {
		indeg[e[1]]++
	}
	if indeg["suplemento"] != 1 {
		t.Fatalf("suplemento tem %d arestas de entrada no wire (esperava 1) — apareceria como raiz", indeg["suplemento"])
	}

	// E fail-closed no sentido inverso: um wire cuja condição projectada NÃO tenha aresta
	// nenhuma volta a ter dois grafos, e é recusado.
	orfao := card
	orfao.ConditionalEdges = nil
	if err := orfao.Validate(); !errors.Is(err, ErrInvalidPlanCard) {
		t.Fatalf("erro = %v (esperava ErrInvalidPlanCard: condicao sem aresta no cartao)", err)
	}

	// Retrocompatibilidade: um plano sem condições não ganha o campo no wire.
	simples, err := BuildPlanCard(samplePlan("run-legado", risk.ClassSafe))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	rawSimples, err := json.Marshal(simples)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(rawSimples), "conditional_edges") {
		t.Fatalf("o wire de um plano sem condicoes ganhou o campo novo:\n%s", rawSimples)
	}
}
