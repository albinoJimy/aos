package planmaterialize

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// materialize_verifier_test.go — READ-ONLY POR CONSTRUÇÃO na MATERIALIZAÇÃO
// (ADR-022 §2.2, AOS-271, AC1). O que se prova aqui não é que existe um filtro: é que
// a NHI EMITIDA de um nó verificador nunca carrega autoridade de efeito, e que a tool
// call do nó-folha sai do MESMO filtro que a autoridade.

// noEffect é um oráculo que considera de efeito TUDO menos `read`. Modela o que o
// composition root liga: `planvalidate.Snapshot.EffectOracle()`, ancorado nos eixos
// de risco PINADOS. Ligado por TIPO ESTRUTURAL — sem import entre os pacotes.
func noEffect(t plan.ToolRef) bool { return t.Name != "read" }

// TestVerifierAuthorityHasNoEffectTools — a NHI de um papel verificador materializa-se
// SEM as capabilities de efeito, e a de um nó normal com o MESMO conjunto de tools
// materializa-se INTACTA (a prova de que o clamp é do PAPEL e não do materializador
// inteiro).
//
// FALHA-ANTES (verificável removendo o filtro de [Materializer.authorityForNode]): o
// spawn do verificador levava `cap:tool:write` no Authority[] da filha, e «read-only
// por construção» passava a ser uma promessa do organigrama — mediada, na melhor das
// hipóteses, pelo RM no caminho quente.
func TestVerifierAuthorityHasNoEffectTools(t *testing.T) {
	sp := &fakeSpawner{}
	lf := &fakeLeaf{}
	m, err := NewMaterializer(&fakeAdmission{}, lf, sp, &fakeRecorder{}, WithEffectOracle(noEffect))
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}

	tools := []plan.ToolRef{tool("write"), tool("read"), tool("post")}
	// AMBOS têm um dependente. O nó normal materializa-se por isso como PAPEL (Spawn);
	// o verificador NÃO — um verificador é sempre FOLHA (ver
	// [TestVerifierIsAlwaysLeaf]). A comparação isola na mesma a única variável que
	// interessa aqui: a AUTORIDADE emitida com o mesmo conjunto de tools.
	verifier := plan.Node{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica", Tools: tools}
	plain := plan.Node{NodeID: "author", Role: "writer", Objective: "escreve", Tools: tools}
	sink := node("sink", nil, "review", "author")

	if _, err := m.Materialize(context.Background(), baseReq(verifier, plain, sink)); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	var reviewCaps []string
	for _, c := range lf.calls {
		if c.nodeID == "review" {
			reviewCaps = c.caps
		}
	}
	if len(reviewCaps) != 1 || reviewCaps[0] != "cap:tool:read" {
		t.Fatalf("autoridade do verificador = %v; queria exactamente [cap:tool:read] — as tools de efeito não podem ser EMITIDAS", reviewCaps)
	}
	got := map[string][]string{}
	for _, c := range sp.calls {
		got[c.nodeID] = c.authority
	}
	if len(got["author"]) != 3 {
		t.Fatalf("autoridade do nó normal = %v; queria as 3 capabilities intactas (o clamp é do PAPEL, não global)", got["author"])
	}
}

// TestVerifierIsAlwaysLeaf — O «SPAWN» QUE §2.2 EXCLUI DO VERIFICADOR NÃO É UMA TOOL.
// No organigrama, spawn é materializar-se como PAPEL-QUE-EXPANDE: uma
// `identity.ChildRequest` própria e uma sub-árvore de delegação a jusante. O clamp de
// [Materializer.authorityForNode] só filtrava capabilities derivadas de TOOLS, pelo que
// um verificador com dependentes era classificado papel pelo [DefaultClassifier] e
// ganhava, por via da TOPOLOGIA, a autoridade de delegação que o ADR lhe nega.
//
// FALHA-ANTES (verificada pela auditoria da wave): `review` (role verifier, tool
// read-only) com `impl` a declará-lo em `depends_on` era materializado via
// `Delegator.Spawn` e passava a encabeçar a organização efémera a jusante — a relação
// «quem comissiona o trabalho» que §2.2 quer separar de «quem o julga».
//
// O forço NÃO depende do classificador injectado: um verificador não delega, e isso não
// é política de wiring.
func TestVerifierIsAlwaysLeaf(t *testing.T) {
	sp := &fakeSpawner{}
	lf := &fakeLeaf{}
	// Classificador ADVERSARIAL: declara TUDO papel-que-expande.
	always := func(plan.Node, plan.PlanDocument) plannerevents.SpawnKind { return plannerevents.SpawnRole }
	m, err := NewMaterializer(&fakeAdmission{}, lf, sp, &fakeRecorder{},
		WithEffectOracle(noEffect), WithClassifier(always))
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	verifier := plan.Node{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica",
		Tools: []plan.ToolRef{tool("read")}}
	payload, err := m.Materialize(context.Background(), baseReq(verifier, node("impl", nil, "review")))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	for _, c := range sp.calls {
		if c.nodeID == "review" {
			t.Fatal("o VERIFICADOR foi materializado por Spawn: encabeça uma sub-árvore de delegação, que é o «spawn» que §2.2 exclui")
		}
	}
	var found bool
	for _, c := range lf.calls {
		if c.nodeID == "review" {
			found = true
		}
	}
	if !found {
		t.Fatal("o verificador não foi materializado como nó-folha")
	}
	// E o facto `plan.materialized` diz a verdade sobre o que correu.
	for _, n := range payload.Nodes {
		if n.NodeID == "review" && n.Kind != plannerevents.SpawnLeaf {
			t.Fatalf("plan.materialized[review].Kind = %q; queria leaf", n.Kind)
		}
	}
}

// TestVerifierLeafToolMatchesClampedAuthority — a tool call CONCRETA de um nó-folha
// verificador é a primeira que SOBREVIVE ao clamp, não a primeira declarada.
//
// FALHA-ANTES (verificável revertendo [Materializer.primaryTool] para `Tools[0]`): o
// nó-folha entrava no DAG com `ToolID=write` e `Capability=cap:tool:write` enquanto a
// sua própria autoridade só tinha `cap:tool:read` — uma tool call que nasce fora da
// autoridade de quem a faz. Ou o RM a nega (trabalho morto), ou alguém a jusante
// «arranja» a autoridade para ela passar. Nenhuma das duas é aceitável.
func TestVerifierLeafToolMatchesClampedAuthority(t *testing.T) {
	lf := &fakeLeaf{}
	m, err := NewMaterializer(&fakeAdmission{}, lf, &fakeSpawner{}, &fakeRecorder{}, WithEffectOracle(noEffect))
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	// Sem dependentes ⇒ FOLHA. `write` vem PRIMEIRO no documento, de propósito.
	verifier := plan.Node{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica",
		Tools: []plan.ToolRef{tool("write"), tool("read")}}

	if _, err := m.Materialize(context.Background(), baseReq(verifier)); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(lf.calls) != 1 {
		t.Fatalf("esperava 1 nó-folha, veio %d", len(lf.calls))
	}
	c := lf.calls[0]
	if c.toolID != "read" {
		t.Fatalf("tool call do verificador = %q; queria \"read\" (a primeira que sobrevive ao clamp)", c.toolID)
	}
	if len(c.caps) != 1 || c.caps[0] != "cap:tool:read" {
		t.Fatalf("capabilities = %v; queria [cap:tool:read]", c.caps)
	}
}

// TestDefaultEffectOracleIsFailClosed — sem oráculo ligado, o default trata TODA a
// tool como de efeito e o verificador materializa-se com autoridade VAZIA.
//
// É a escolha declarada em [DefaultEffectOracle]: um wiring incompleto produz
// verificadores inúteis (nota-se) em vez de verificadores com autoridade total num
// sistema onde ninguém olhou (não se notava).
func TestDefaultEffectOracleIsFailClosed(t *testing.T) {
	sp := &fakeSpawner{}
	lf := &fakeLeaf{}
	m, err := NewMaterializer(&fakeAdmission{}, lf, sp, &fakeRecorder{})
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	verifier := plan.Node{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica",
		Tools: []plan.ToolRef{tool("read")}}
	if _, err := m.Materialize(context.Background(), baseReq(verifier, node("sink", nil, "review"))); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	// Um verificador é sempre FOLHA, pelo que a autoridade emitida lê-se no
	// [LeafAdmitter] — e tem de estar VAZIA sem oráculo ligado. Ler `sp.calls` (como
	// este teste fazia) passaria por vacuidade: o verificador nem lá aparece.
	var seen bool
	for _, c := range lf.calls {
		if c.nodeID != "review" {
			continue
		}
		seen = true
		if len(c.caps) != 0 {
			t.Fatalf("autoridade = %v; sem oráculo ligado o default é fail-closed (autoridade vazia)", c.caps)
		}
	}
	if !seen {
		t.Fatal("o verificador não foi materializado — o teste passaria por vacuidade")
	}
	for _, c := range sp.calls {
		if c.nodeID == "review" {
			t.Fatal("o verificador não pode materializar-se como papel-que-expande")
		}
	}
}

// TestMaterializedEventShowsClampedAuthority — o que o clamp retira fica VISÍVEL no
// facto `plan.materialized`: `Nodes[].Tools` é a autoridade CLAMPADA, não a
// declarada. Sem isto, a remoção seria silenciosa e o log diria uma coisa enquanto a
// NHI dizia outra.
func TestMaterializedEventShowsClampedAuthority(t *testing.T) {
	rec := &fakeRecorder{}
	m, err := NewMaterializer(&fakeAdmission{}, &fakeLeaf{}, &fakeSpawner{}, rec, WithEffectOracle(noEffect))
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	verifier := plan.Node{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica",
		Tools: []plan.ToolRef{tool("write"), tool("read")}}
	payload, err := m.Materialize(context.Background(), baseReq(verifier))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	for _, n := range payload.Nodes {
		if n.NodeID != "review" {
			continue
		}
		if len(n.Tools) != 1 || n.Tools[0] != "cap:tool:read" {
			t.Fatalf("plan.materialized[review].Tools = %v; queria a autoridade CLAMPADA [cap:tool:read]", n.Tools)
		}
	}
	if len(rec.payloads) != 1 {
		t.Fatalf("esperava 1 `plan.materialized` apenso, veio %d", len(rec.payloads))
	}
}
