package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// payload_test.go — Os contratos tipados de aresta na admissão (ADR-022 §2.3,
// AOS-272). Cada teste nomeia a FALHA-ANTES concreta: o que um plano hostil (ou
// apenas descuidado) conseguia fazer antes da regra existir.

// summaryOut/metricsOut são os dois lados da fronteira de taint: um payload que
// admite CONTEÚDO do trabalho (untrusted por derivação) e um FECHADO POR CONSTRUÇÃO
// (trusted).
func summaryOut() plan.Output {
	return plan.Output{Name: "resumo", Type: plan.PayloadSummary}
}

func metricsOut() plan.Output {
	return plan.Output{Name: "cobertura", Type: plan.PayloadMetrics}
}

// payloadDoc é o organigrama de base de §2.3: `src` produz dois contratos, `sink`
// depende dele e consome um. O consumidor NÃO tem tools (autoridade vazia), pelo que
// a regra (P4) não dispara — os testes que a atacam pinam a tool explicitamente.
func payloadDoc(consumes []plan.PayloadEdge, sinkTools []plan.ToolRef) plan.PlanDocument {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "src", Role: "r", Objective: "produz", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{summaryOut(), metricsOut()}},
		{NodeID: "sink", Role: "r", Objective: "consome", DependsOn: []string{"src"},
			Tools: sinkTools, Consumes: consumes},
	}
	return doc
}

// assertRejects corre o validador e exige a REGRA e o SUB-CÓDIGO exactos — «rejeitado»
// não é um resultado que o re-planeamento consiga corrigir.
func assertRejects(t *testing.T, doc plan.PlanDocument, rule plannerevents.Rule, reason Reason, locator string) {
	t.Helper()
	mustBeShapeValid(t, doc) // a defesa NÃO é o parser: o doc é forma válida
	v := Validate(doc, verifierSnapshot(), Ceilings{})
	if !v.Rejected() {
		t.Fatalf("plano devia ser rejeitado, veio: %+v", v)
	}
	if v.Rule != rule || v.Reason != reason {
		t.Fatalf("rejeição = (%q,%q), quer (%q,%q)", v.Rule, v.Reason, rule, reason)
	}
	if v.Locator.NodeID != locator {
		t.Fatalf("Locator.NodeID = %q, quer %q (o consumidor que declarou o contrato)", v.Locator.NodeID, locator)
	}
}

// TestPayloadContractAccepted — NÃO-VACUIDADE. O contrato legítimo passa: `sink`
// espera por `src` (depends_on) e consome um output que `src` declara, do mesmo tipo.
// Se as regras novas rejeitassem isto, rejeitariam o desenho que o ADR congelou.
func TestPayloadContractAccepted(t *testing.T) {
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}}, nil)
	mustBeShapeValid(t, doc)
	if v := Validate(doc, verifierSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("contrato legítimo devia ser aceite, veio: %+v", v)
	}
}

// TestConsumesUnknownOutput — a rejeição literal do ADR: «consome um output
// inexistente». FALHA-ANTES: sem a regra, o contrato era uma esperança sobre o que o
// produtor viesse a fazer, e o buraco só aparecia em execução (ou nunca).
func TestConsumesUnknownOutput(t *testing.T) {
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "inexistente", Type: plan.PayloadSummary}}, nil)
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesUnknownOutput, "sink")
}

// TestConsumesTypeMismatch — «de tipo incompatível». A compatibilidade é IDENTIDADE:
// pedir `metrics` de um contrato declarado `summary` é rejeitado, sem coerção nem
// subtipagem que o validador tivesse de raciocinar.
func TestConsumesTypeMismatch(t *testing.T) {
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadMetrics}}, nil)
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesTypeMismatch, "sink")
}

// TestConsumesWithoutDeclaredEdge — a aresta de dados TEM de montar numa aresta de
// execução. FALHA-ANTES: `consumes` seria um segundo canal de aresta INVISÍVEL ao DAG
// de admissão — um nó lia o trabalho de outro sem esperar por ele (corrida com o
// produtor, resultado dependente do escalonador) e o grafo de dados podia fechar
// ciclos que a aciclicidade de AOS-025 nunca veria.
func TestConsumesWithoutDeclaredEdge(t *testing.T) {
	doc := payloadDoc(nil, nil)
	// `other` existe no plano mas NÃO é aresta de entrada de `sink`.
	doc.Nodes = append(doc.Nodes, plan.Node{NodeID: "other", Role: "r", Objective: "o",
		Outputs: []plan.Output{summaryOut()}})
	doc.Nodes[1].Consumes = []plan.PayloadEdge{{From: "other", Output: "resumo", Type: plan.PayloadSummary}}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesUnknownEdge, "sink")
}

// TestConsumesOverConditionalEdgeAccepted prova que a regra (P1) aceita os DOIS canais
// de aresta: uma aresta de dados pode montar numa aresta guardada por condição
// (AOS-270), não só numa dependência simples.
func TestConsumesOverConditionalEdgeAccepted(t *testing.T) {
	doc := payloadDoc(nil, nil)
	doc.Nodes[1].DependsOn = nil
	doc.Nodes[1].ConditionalOn = []plan.ConditionalEdge{{
		From: "src",
		When: []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumComplete}},
	}}
	doc.Nodes[1].Consumes = []plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}}
	mustBeShapeValid(t, doc)
	if v := Validate(doc, verifierSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("contrato sobre aresta guardada por condição devia ser aceite, veio: %+v", v)
	}
}

// TestConsumesTaintIncompatibleWithAuthority — a terceira rejeição do ADR, e a que
// tem eixo de segurança: um payload UNTRUSTED (um resumo — carrega o trabalho, que é
// saída de modelo) alimentar um consumidor com AUTORIDADE PRIVILEGIADA.
//
// FALHA-ANTES: o plano era admitido, o humano aprovava no gate um organigrama onde um
// nó com egress recebe material untrusted, e a barreira P0 de ADR-005 só disparava no
// RM — depois do spawn, depois dos tokens, e com o operador a ver uma negação sem
// perceber que o organigrama a garantia desde o início.
func TestConsumesTaintIncompatibleWithAuthority(t *testing.T) {
	// Os DOIS eixos de «privilegiado», provados em separado: fala para fora, e não se
	// desfaz. É o mesmo critério de [IsEffectTool] — uma definição, duas perguntas.
	for _, tc := range []struct {
		name string
		tool plan.ToolRef
	}{
		{"egress externo", egressTool()},
		{"irreversível", effectTool()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}},
				[]plan.ToolRef{tc.tool})
			assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesTaintAuthority, "sink")
		})
	}
}

// declassifierDoc é o ÚNICO caminho legítimo para material chegar a um consumidor
// PRIVILEGIADO: `src` produz trabalho, `judge` (papel reservado, read-only) examina-o e
// publica um contrato de FORMA FECHADA, e `sink` — que pina uma tool de egress — lê
// esse contrato guardado pelo veredicto de `judge`.
//
// Repare-se no canal: `sink` observa `judge` por `conditional_on`, NUNCA por
// `depends_on` — um verificador é sumidouro do canal de execução (regra V4), senão
// encabeçaria a sub-árvore de delegação de quem o consome.
func declassifierDoc(consumes []plan.PayloadEdge, judgeOutputs []plan.Output) plan.PlanDocument {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "src", Role: "r", Objective: "produz", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{summaryOut()}},
		{NodeID: "judge", Role: plan.RoleVerifier, Objective: "examina", DependsOn: []string{"src"},
			Tools: []plan.ToolRef{readOnlyTool()}, Outputs: judgeOutputs,
			Consumes: []plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}}},
		{NodeID: "sink", Role: "r", Objective: "age", DependsOn: []string{"src"},
			ConditionalOn: []plan.ConditionalEdge{{From: "judge", When: verdictPass()}},
			Tools:         []plan.ToolRef{egressTool()}, Consumes: consumes},
	}
	return doc
}

// TestPrivilegedConsumerMayReadDeclassifiedClosedForm prova que a regra (P4) NÃO é uma
// proibição em bloco: um consumidor privilegiado pode consumir um payload de forma
// FECHADA produzido pelo VERIFICADOR — o ponto de desclassificação que §2.2 sanciona.
// Sem este teste, a regra podia estar a rejeitar tudo e passaria por correcta.
func TestPrivilegedConsumerMayReadDeclassifiedClosedForm(t *testing.T) {
	doc := declassifierDoc(
		[]plan.PayloadEdge{{From: "judge", Output: "cobertura", Type: plan.PayloadMetrics}},
		[]plan.Output{metricsOut()})
	mustBeShapeValid(t, doc)
	if v := Validate(doc, verifierSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("forma fechada de um VERIFICADOR devia poder alimentar consumidor privilegiado, veio: %+v", v)
	}
}

// TestClosedFormFromNonVerifierIsUntrusted — A CORRECÇÃO DO BLOCKER DA AUDITORIA. A
// forma fechada é condição NECESSÁRIA, nunca SUFICIENTE: `type` é um campo do
// documento untrusted, e um nó qualquer a declarar `type: metrics` num output que
// carrega o seu trabalho entregava material untrusted a um consumidor privilegiado com
// rótulo trusted.
//
// FALHA-ANTES (verificada): este plano era ADMITIDO — `TaintFloor()` dizia
// metrics⇒trusted e (P4) nunca disparava; a jusante, nada validava a forma REAL do que
// era publicado (o conteúdo vivia atrás de um locator opaco). A barreira P0 de ADR-005
// — a única razão de existir de (P4) — era contornada por uma palavra do documento.
func TestClosedFormFromNonVerifierIsUntrusted(t *testing.T) {
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "cobertura", Type: plan.PayloadMetrics}},
		[]plan.ToolRef{egressTool()})
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesTaintAuthority, "sink")
}

// TestTaintLaunderedByIntermediateNodeRejected — LAVAGEM POR NÓ INTERMÉDIO, o segundo
// disfarce que a auditoria demonstrou ADMITIDO: `scrape` produz um resumo (untrusted);
// `distil`, SEM tools (logo não-privilegiado, logo autorizado a consumir untrusted),
// re-publica-o como `metrics`; `exfil`, com egress, consome as métricas como trusted.
//
// FALHA-ANTES (verificada): admitido. 32 pares (nome, inteiro) escolhidos por um nó
// cujo único input é material untrusted chegavam ao nó com egress rotulados trusted —
// canal suficiente para dirigir a acção privilegiada. A correcção não precisa de
// travessia nova: como só um verificador publica trusted, não há salto que lave nada.
func TestTaintLaunderedByIntermediateNodeRejected(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "scrape", Role: "r", Objective: "recolhe", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "notas", Type: plan.PayloadSummary}}},
		{NodeID: "distil", Role: "r", Objective: "destila", DependsOn: []string{"scrape"},
			Consumes: []plan.PayloadEdge{{From: "scrape", Output: "notas", Type: plan.PayloadSummary}},
			Outputs:  []plan.Output{{Name: "score", Type: plan.PayloadMetrics}}},
		{NodeID: "exfil", Role: "r", Objective: "age", DependsOn: []string{"distil"},
			Tools:    []plan.ToolRef{effectTool()},
			Consumes: []plan.PayloadEdge{{From: "distil", Output: "score", Type: plan.PayloadMetrics}}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesTaintAuthority, "exfil")
}

// TestTaintLaunderingByDeclarationRejected — a lavagem óbvia: declarar `taint:
// trusted` num resumo para o fazer passar a um consumidor privilegiado. O rótulo do
// documento SÓ ELEVA; o piso derivado do tipo vence.
func TestTaintLaunderingByDeclarationRejected(t *testing.T) {
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}},
		[]plan.ToolRef{egressTool()})
	doc.Nodes[0].Outputs = []plan.Output{{Name: "resumo", Type: plan.PayloadSummary, Taint: plan.TaintTrusted}}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesTaintAuthority, "sink")
}

// TestTaintElevationHonoured prova a direcção que o documento PODE mover: mesmo no
// único caminho que produz `trusted` — a forma fechada de um verificador —, declarar
// `taint: untrusted` corta-o. O advisory eleva, e a elevação é honrada.
func TestTaintElevationHonoured(t *testing.T) {
	doc := declassifierDoc(
		[]plan.PayloadEdge{{From: "judge", Output: "cobertura", Type: plan.PayloadMetrics}},
		[]plan.Output{{Name: "cobertura", Type: plan.PayloadMetrics, Taint: plan.TaintUntrusted}})
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonConsumesTaintAuthority, "sink")
}

// TestVerifierMayNotProduceWork — (V5). Um «verificador» que declara um output de forma
// ABERTA está a produzir TRABALHO, e quem produz trabalho é um produtor por muito que o
// `role` diga o contrário.
//
// FALHA-ANTES (verificada): `review` (role verifier, read-only) declarava
// `outputs: [{report, summary}]`, o consumidor lia esse relatório E ramificava sobre o
// veredicto do MESMO nó — o organigrama aprovado mostrava um «revisor», e o que corria
// era um nó a produzir trabalho e a assinar o próprio pass. É também esta regra que
// mantém coerente a desclassificação: se o verificador é o ponto que desclassifica,
// tudo o que ele publica tem de ser de forma fechada.
func TestVerifierMayNotProduceWork(t *testing.T) {
	doc := declassifierDoc(
		[]plan.PayloadEdge{{From: "judge", Output: "resumo", Type: plan.PayloadSummary}},
		[]plan.Output{{Name: "resumo", Type: plan.PayloadSummary}})
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonVerifierProducesWork, "judge")
}

// TestNoNodeMayDependOnVerifier — (V4). O verificador é SUMIDOURO do canal de execução:
// um nó que o declare em `depends_on` fá-lo encabeçar uma sub-árvore de delegação — o
// «spawn» que §2.2 nomeia como efeito e que nenhum clamp de TOOLS apanha.
//
// FALHA-ANTES (verificada): a regra (V2) interrogava as arestas do CONSUMIDOR, pelo que
// bastava o consumidor do veredicto NÃO declarar o trabalho como aresta de entrada para
// o verificador certificar trabalho que ele próprio comissionou. Uma regra ancorada em
// quem OBSERVA a violação contorna-se deixando de observar.
func TestNoNodeMayDependOnVerifier(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "brief", Role: "r", Objective: "enquadra"},
		{NodeID: "review", Role: plan.RoleVerifier, Objective: "revê", DependsOn: []string{"brief"},
			Tools: []plan.ToolRef{readOnlyTool()}},
		// `impl` descende do verificador — e `publish` NÃO o declara como aresta de
		// entrada, que era o que fazia a regra antiga calar-se.
		{NodeID: "impl", Role: "r", Objective: "implementa", DependsOn: []string{"review"}},
		{NodeID: "publish", Role: "r", Objective: "publica",
			ConditionalOn: []plan.ConditionalEdge{{From: "review", When: verdictPass()}}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonVerifierCommissionsWork, "impl")
}

// TestVerifierMustObserveTheWorkItReleases — (V6). Um verificador ligado a OUTRO ramo
// do grafo libertava trabalho que nunca viu: `review` depende só de `brief`, e é ele a
// autoridade que liberta a publicação de `draft`.
//
// FALHA-ANTES (verificada): admitido. (V1-bis) exigia apenas que o verificador tivesse
// ALGUMA aresta de entrada — «tem sujeito» era uma propriedade sobre a existência de
// arestas, não sobre O QUE o ramo guarda.
func TestVerifierMustObserveTheWorkItReleases(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "brief", Role: "r", Objective: "enquadra"},
		{NodeID: "draft", Role: "r", Objective: "escreve", DependsOn: []string{"brief"}},
		{NodeID: "review", Role: plan.RoleVerifier, Objective: "revê", DependsOn: []string{"brief"},
			Tools: []plan.ToolRef{readOnlyTool()}},
		{NodeID: "publish", Role: "r", Objective: "publica", DependsOn: []string{"draft"},
			ConditionalOn: []plan.ConditionalEdge{{From: "review", When: verdictPass()}}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonVerifierNotObservingWork, "publish")
}

// TestTaintLabelsMatchTheKernelLattice — a AMARRA que faltava entre os dois alfabetos.
// `plan` é zero-dep e re-escreve os rótulos de taint como literais, declarando-os «o
// mapeamento literal» de `taint.StringTrusted`/`StringUntrusted` — mas nada ligava as
// duas definições: um rename do lado do kernel compilava e passava os testes dos dois
// lados, e a incompatibilidade só apareceria quando um rótulo atravessasse a fronteira
// em runtime. Este pacote já importa o módulo kernel, pelo que a amarra cabe aqui — a
// mesma disciplina que AOS-271 usou entre `plannerevents.VerdictOutcome` e
// `plan.EnumPass/EnumFail`.
func TestTaintLabelsMatchTheKernelLattice(t *testing.T) {
	if string(plan.TaintTrusted) != taint.StringTrusted {
		t.Fatalf("plan.TaintTrusted = %q, o reticulado do RM diz %q", plan.TaintTrusted, taint.StringTrusted)
	}
	if string(plan.TaintUntrusted) != taint.StringUntrusted {
		t.Fatalf("plan.TaintUntrusted = %q, o reticulado do RM diz %q", plan.TaintUntrusted, taint.StringUntrusted)
	}
}

// TestUnresolvedToolMakesConsumerPrivileged prova o fail-closed da direita da
// resolução: uma tool que não resolve conta como privilegiada. O caminho normal nem
// chega aqui (checkTools rejeita antes) — este teste ataca a função directamente.
func TestUnresolvedToolMakesConsumerPrivileged(t *testing.T) {
	idx := verifierSnapshot().index()
	cases := []struct {
		name string
		tool plan.ToolRef
	}{
		{"desconhecida", plan.ToolRef{Name: "fantasma", Version: "1.0.0", Digest: "sha256:x"}},
		{"digest divergente", plan.ToolRef{Name: "inspect", Version: "1.0.0", Digest: "sha256:outro"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := plan.Node{NodeID: "x", Tools: []plan.ToolRef{tc.tool}}
			if !privilegedAuthority(n, idx) {
				t.Fatalf("tool que não resolve devia contar como privilegiada (fail-closed)")
			}
		})
	}
	// Um nó SEM tools não tem autoridade nenhuma — pode consumir untrusted à vontade.
	if privilegedAuthority(plan.Node{NodeID: "x"}, idx) {
		t.Fatalf("nó sem tools não devia ser privilegiado")
	}
	// E a tool read-only pinada continua a NÃO ser privilegiada (não-vacuidade).
	if privilegedAuthority(plan.Node{NodeID: "x", Tools: []plan.ToolRef{readOnlyTool()}}, idx) {
		t.Fatalf("tool sem egress e reversível não devia ser privilegiada")
	}
}

// TestPayloadValidationIsDeterministic — o mesmo input dá sempre o mesmo veredicto
// (ADR-010): a iteração é por slices, nunca por mapas.
func TestPayloadValidationIsDeterministic(t *testing.T) {
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "inexistente", Type: plan.PayloadSummary}}, nil)
	first := Validate(doc, verifierSnapshot(), Ceilings{})
	for i := 0; i < 32; i++ {
		if got := Validate(doc, verifierSnapshot(), Ceilings{}); got != first {
			t.Fatalf("veredicto instável na passagem %d: %+v != %+v", i, got, first)
		}
	}
}

// TestNoPayloadContractsLeavesPathUnchanged prova o interruptor: um plano sem
// contratos atravessa a regra nova sem tocar em nada.
func TestNoPayloadContractsLeavesPathUnchanged(t *testing.T) {
	doc := baseDoc()
	if v := checkPayloadContracts(doc, baseSnapshot()); v != accepted {
		t.Fatalf("plano sem contratos devia atravessar intacto, veio: %+v", v)
	}
}
