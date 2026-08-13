package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// verifier_test.go — A SEMÂNTICA DE SISTEMA DO VERIFICADOR na admissão (ADR-022
// §2.2, AOS-271). Cada teste nomeia a FALHA-ANTES concreta: o que um plano hostil
// (ou apenas descuidado) conseguia fazer antes da regra existir.

// verdictPass é o predicado de um ramo de QUALIDADE — o observável que AOS-271
// amarra ao papel verificador.
func verdictPass() []plan.Predicate {
	return []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumPass}}
}

// readOnlyTool é uma tool SEM efeito pelos eixos pinados: sem egress, reversível.
// É o que um verificador pode pinar.
func readOnlyTool() plan.ToolRef {
	return plan.ToolRef{Name: "inspect", Version: "1.0.0", Digest: "sha256:inspect"}
}

// effectTool é uma tool DE EFEITO: irreversível (uma escrita que não se desfaz).
func effectTool() plan.ToolRef {
	return plan.ToolRef{Name: "write", Version: "1.0.0", Digest: "sha256:write"}
}

// egressTool tem efeito pelo OUTRO eixo: reversível, mas fala para fora.
func egressTool() plan.ToolRef {
	return plan.ToolRef{Name: "post", Version: "1.0.0", Digest: "sha256:post"}
}

// verifierSnapshot estende o snapshot base com os três eixos explícitos de que a
// regra (V3) depende. `search` fica com os eixos do base (valores-zero) de propósito:
// é a prova de que uma capability POR CLASSIFICAR conta como de efeito (fail-closed
// pelo tipo).
func verifierSnapshot() Snapshot {
	s := baseSnapshot()
	s.Tools = append(s.Tools,
		Capability{Name: "inspect", Version: "1.0.0", Digest: "sha256:inspect", Admissible: true,
			Sensitivity: risk.SensitivitySensitive, Egress: risk.EgressNone, Reversibility: risk.Reversible},
		Capability{Name: "write", Version: "1.0.0", Digest: "sha256:write", Admissible: true,
			Sensitivity: risk.SensitivityInternal, Egress: risk.EgressNone, Reversibility: risk.Irreversible},
		Capability{Name: "post", Version: "1.0.0", Digest: "sha256:post", Admissible: true,
			Sensitivity: risk.SensitivityPublic, Egress: risk.EgressExternal, Reversibility: risk.Reversible},
	)
	return s
}

// qualityBranchDoc é o organigrama LEGÍTIMO de ADR-022 §2.2: `work` produz, `review`
// (papel reservado, read-only) verifica-o, e dois ramos declarados à priori consomem
// o VEREDICTO. É a linha de base de não-vacuidade — se as regras novas rejeitassem
// isto, rejeitariam o desenho que o ADR congelou.
func qualityBranchDoc() plan.PlanDocument {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "work", Role: "producer", Objective: "produz", Tools: []plan.ToolRef{searchTool()}},
		{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica",
			DependsOn: []string{"work"}, Tools: []plan.ToolRef{readOnlyTool()}},
		{NodeID: "publish", Role: "r", Objective: "publica", DependsOn: []string{"work"},
			ConditionalOn: []plan.ConditionalEdge{{From: "review", When: verdictPass()}}},
	}
	return doc
}

// TestQualityBranchAccepted — NÃO-VACUIDADE. O ramo de qualidade canónico passa: o
// verificador lê o trabalho (`depends_on work`), o consumidor consome o mesmo
// trabalho E o veredicto, e o grafo continua acíclico.
//
// Este teste é o que impede a leitura errada de «sub-árvore de delegação»: se a regra
// (V2) fosse «o verificador não pode descender do produtor», ESTE plano — o do ADR —
// seria rejeitado, e o pacote inteiro passaria a recusar tudo.
func TestQualityBranchAccepted(t *testing.T) {
	doc := qualityBranchDoc()
	mustBeShapeValid(t, doc)
	if v := Validate(doc, verifierSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("ramo de qualidade legítimo (o desenho de ADR-022 §2.2) rejeitado: %+v", v)
	}
}

// TestVerdictFromNonVerifierRejected — (V1) O BURACO QUE AOS-270 DEIXOU ABERTO. O
// plano ramifica sobre o `verdict` do PRÓPRIO nó produtor.
//
// FALHA-ANTES: a gramática de AOS-270 admite o símbolo e nada ligava o veredicto a um
// emissor privilegiado; a jusante, como nenhum nó produzia veredicto, o ramo avaliava
// «ausente ⇒ falso» e PODAVA-SE em silêncio. Uma auto-certificação que o sistema não
// via — e cuja consequência (metade do organigrama morta) o revisor do gate também
// não via.
func TestVerdictFromNonVerifierRejected(t *testing.T) {
	doc := qualityBranchDoc()
	// O consumidor passa a observar o veredicto do PRODUTOR, não o do verificador.
	doc.Nodes[2].DependsOn = nil
	doc.Nodes[2].ConditionalOn = []plan.ConditionalEdge{{From: "work", When: verdictPass()}}
	mustBeShapeValid(t, doc) // a FORMA é válida: a defesa não é o parser
	v := Validate(doc, verifierSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonVerdictNotFromVerifier {
		t.Fatalf("veredicto = (%s/%s); queria (schema/verdict_not_from_verifier)", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "publish" {
		t.Fatalf("locator = %q; queria o consumidor ofensor \"publish\"", v.Locator.NodeID)
	}
}

// TestVerifierSelfSubtreeRejected — (V2) «PRODUTOR ≠ VERIFICADOR». O verificador está
// A MONTANTE do trabalho que certifica: `impl` descende de `review`, logo pertence à
// sub-árvore de delegação que `review` encabeça, e `review` assina o pass sobre ela.
//
// FALHA-ANTES: sem esta regra bastava um passo de indirecção para lavar a
// auto-certificação — o verificador não emite veredicto sobre si próprio, emite-o
// sobre trabalho que ele mesmo comissionou, e o validador não notava a diferença.
func TestVerifierSelfSubtreeRejected(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "review", Role: plan.RoleVerifier, Objective: "verifica", Tools: []plan.ToolRef{readOnlyTool()},
			DependsOn: []string{"seed"}},
		{NodeID: "seed", Role: "r", Objective: "semeia"},
		// `impl` está NA SUB-ÁRVORE de `review` (descende dele).
		{NodeID: "impl", Role: "r", Objective: "implementa", DependsOn: []string{"review"},
			Tools: []plan.ToolRef{searchTool()}},
		// … e `publish` consome o trabalho de `impl` guardado pelo veredicto de `review`.
		{NodeID: "publish", Role: "r", Objective: "publica", DependsOn: []string{"impl"},
			ConditionalOn: []plan.ConditionalEdge{{From: "review", When: verdictPass()}}},
	}
	mustBeShapeValid(t, doc)
	v := Validate(doc, verifierSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonVerifierSelfSubtree {
		t.Fatalf("veredicto = (%s/%s); queria (schema/verifier_self_subtree)", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "publish" {
		t.Fatalf("locator = %q; queria \"publish\"", v.Locator.NodeID)
	}
}

// TestVerifierWithoutSubjectRejected — (V1-bis) um «verificador» solto, sem arestas
// de entrada, não observa nó nenhum: o seu pass é uma constante com nome de
// veredicto. É também a forma mais barata de lavar uma auto-certificação — declara-se
// o verificador ao lado do trabalho e faz-se o ramo depender dele.
func TestVerifierWithoutSubjectRejected(t *testing.T) {
	doc := qualityBranchDoc()
	doc.Nodes[1].DependsOn = nil // o verificador deixa de observar o trabalho
	mustBeShapeValid(t, doc)
	v := Validate(doc, verifierSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonVerifierWithoutSubject {
		t.Fatalf("veredicto = (%s/%s); queria (schema/verifier_without_subject)", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "review" {
		t.Fatalf("locator = %q; queria o verificador \"review\"", v.Locator.NodeID)
	}
}

// TestVerifierEffectToolRejected — (V3) READ-ONLY POR CONSTRUÇÃO, nos DOIS eixos que
// definem «efeito», mais o caso fail-closed da capability por classificar.
//
// FALHA-ANTES: um verificador com uma tool de escrita passava a admissão, o humano
// aprovava no gate um organigrama em que o revisor podia mexer no que revia, e a
// única defesa era o clamp a jusante — que o approval-card não descrevia.
func TestVerifierEffectToolRejected(t *testing.T) {
	cases := []struct {
		name string
		tool plan.ToolRef
	}{
		{"irreversivel", effectTool()},
		{"egress", egressTool()},
		// `search` está no snapshot base SEM eixos declarados (valores-zero): conta
		// como de efeito pelo tipo, sem uma linha de código para isso.
		{"por-classificar", searchTool()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := qualityBranchDoc()
			doc.Nodes[1].Tools = []plan.ToolRef{readOnlyTool(), tc.tool}
			mustBeShapeValid(t, doc)
			v := Validate(doc, verifierSnapshot(), Ceilings{})
			if v.Rule != plannerevents.RuleToolResolution || v.Reason != ReasonVerifierEffectTool {
				t.Fatalf("veredicto = (%s/%s); queria (tool_resolution/verifier_effect_tool)", v.Rule, v.Reason)
			}
			if v.Locator.NodeID != "review" || v.Locator.Tool.Name != tc.tool.Name {
				t.Fatalf("locator = %+v; queria o nó \"review\" e a tool %q", v.Locator, tc.tool.Name)
			}
		})
	}
}

// TestVerifierRoleIsCaseSensitive — o papel reservado é UM literal exacto. Um plano
// que declare `Verifier` PARECE um verificador ao revisor humano do approval-card mas
// não é o papel do sistema; a admissão tem de o tratar como o que é — um nó qualquer
// — e recusar o ramo por (V1), em vez de lhe conceder a semântica pelo aspecto.
func TestVerifierRoleIsCaseSensitive(t *testing.T) {
	doc := qualityBranchDoc()
	doc.Nodes[1].Role = "Verifier"
	mustBeShapeValid(t, doc)
	v := Validate(doc, verifierSnapshot(), Ceilings{})
	if v.Reason != ReasonVerdictNotFromVerifier {
		t.Fatalf("reason = %q; queria verdict_not_from_verifier (o papel reservado é case-sensitive)", v.Reason)
	}
}

// TestIsEffectToolCriterion — o CRITÉRIO declarado, em tabela: efeito ⇔ egress ≠ none
// OU irreversível. Inclui os valores-zero de cada eixo, que são o lado fail-closed.
func TestIsEffectToolCriterion(t *testing.T) {
	cases := []struct {
		name string
		cap  Capability
		want bool
	}{
		{"leitura local reversivel", Capability{Egress: risk.EgressNone, Reversibility: risk.Reversible}, false},
		{"leitura sensivel continua read-only", Capability{Sensitivity: risk.SensitivitySensitive, Egress: risk.EgressNone, Reversibility: risk.Reversible}, false},
		{"irreversivel", Capability{Egress: risk.EgressNone, Reversibility: risk.Irreversible}, true},
		{"egress interno tambem conta", Capability{Egress: risk.EgressInternal, Reversibility: risk.Reversible}, true},
		{"egress externo", Capability{Egress: risk.EgressExternal, Reversibility: risk.Reversible}, true},
		{"eixos por classificar (valores-zero)", Capability{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEffectTool(tc.cap); got != tc.want {
				t.Fatalf("IsEffectTool = %v; queria %v", got, tc.want)
			}
		})
	}
}

// TestEffectOracleFailsClosedOffSnapshot — o oráculo que o materializador consome é
// fail-closed em toda a direita da resolução: uma tool que não resolve, com digest
// divergente, deprecada ou inadmissível conta como DE EFEITO. Sem isto, um
// verificador ganhava autoridade a partir de uma referência que ninguém reconhece.
func TestEffectOracleFailsClosedOffSnapshot(t *testing.T) {
	oracle := verifierSnapshot().EffectOracle()
	if oracle(readOnlyTool()) {
		t.Fatal("a tool read-only PINADA devia contar como sem efeito")
	}
	cases := map[string]plan.ToolRef{
		"desconhecida":      {Name: "fantasma", Version: "1.0.0", Digest: "sha256:x"},
		"digest divergente": {Name: "inspect", Version: "1.0.0", Digest: "sha256:outro"},
		"deprecada":         {Name: "legacy", Version: "2.0.0", Digest: "sha256:legacy"},
		"inadmissivel":      {Name: "blocked", Version: "1.0.0", Digest: "sha256:blocked"},
	}
	for name, ref := range cases {
		if !oracle(ref) {
			t.Fatalf("%s: devia contar como DE EFEITO (fail-closed)", name)
		}
	}
}

// TestVerdictRulesAreDeterministic — o veredicto das regras novas é estável entre
// invocações (as regras iteram slices e um DAG, nunca mapas).
func TestVerdictRulesAreDeterministic(t *testing.T) {
	doc := qualityBranchDoc()
	doc.Nodes[1].Tools = []plan.ToolRef{effectTool(), egressTool()}
	first := Validate(doc, verifierSnapshot(), Ceilings{})
	if !first.Rejected() {
		t.Fatalf("pré-condição: o doc devia ser rejeitado, veio %+v", first)
	}
	for i := 0; i < 50; i++ {
		if got := Validate(doc, verifierSnapshot(), Ceilings{}); got != first {
			t.Fatalf("veredicto instável na iteração %d: %+v != %+v", i, got, first)
		}
	}
}
