package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// schemastamp_test.go — O CARIMBO TEM DE IDENTIFICAR O SCHEMA (AOS-273, ADR-022 §4).
//
// A FALHA-ANTES é concreta e passou uma auditoria adversarial: o bump para 1.2.0 não
// era imposto por nada. `plan.Decode` não olha para o `plan_version` (valida FORMA, e a
// forma é a mesma), a regra 1 só comparava MAJORs, e o `plan_version` é um campo do
// PlanDocument — dados untrusted escritos pelo modelo. Um produtor carimbava 1.1.0,
// emitia `outputs`/`consumes`, o plano era ADMITIDO, aprovado e CONGELADO com esse
// carimbo; meses depois um reader 1.1.0 — retido legitimamente, porque a janela de
// suporte é por MAJOR — falhava o replay com «unknown field outputs», um erro que
// nenhuma política de `planmigrate` sabe atribuir. O carimbo, que é a coordenada de toda
// essa maquinaria, não identificava o schema.
//
// O par que se segue é o golden-set mínimo desta regra: o MESMO documento é recusado
// carimbado abaixo do piso das features que usa e aceite carimbado no piso — a rejeição
// é do CARIMBO, não do conteúdo, e prova-se pela sua ausência no segundo caso.

// stamped devolve doc com outro `plan_version` — o único eixo que estes testes movem.
func stamped(doc plan.PlanDocument, major, minor, patch int) plan.PlanDocument {
	doc.PlanVersion = plan.PlanVersion{Major: major, Minor: minor, Patch: patch}
	return doc
}

// TestOutputsAbaixoDoPisoSaoRecusados — o par de ouro de §2.3: um documento que declara
// `outputs` (linha 1.2.0) carimbado 1.1.0 é INADMISSÍVEL; carimbado 1.2.0 é admitido.
func TestOutputsAbaixoDoPisoSaoRecusados(t *testing.T) {
	doc := payloadDoc(nil, nil) // `src` declara outputs ⇒ piso 1.2.0

	baixo := stamped(doc, 1, 1, 0)
	mustBeShapeValid(t, baixo) // a defesa é SEMÂNTICA: a forma continua válida
	v := Validate(baixo, verifierSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonVersionBelowFeatures {
		t.Fatalf("veredicto = (%s/%s); queria (schema/plan_version_below_features)", v.Rule, v.Reason)
	}
	// O locator nomeia o nó cujo uso fixa o piso — o feedback tem de dizer ONDE.
	if v.Locator.NodeID != "src" {
		t.Fatalf("locator = %q; queria o nó que usa a feature (src)", v.Locator.NodeID)
	}

	// NÃO-VACUIDADE: o MESMO documento, carimbado no piso, é aceite. Sem esta metade a
	// regra podia estar a recusar por outra razão qualquer.
	if v := Validate(stamped(doc, 1, 2, 0), verifierSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("o mesmo documento carimbado 1.2.0 foi recusado: (%s/%s)", v.Rule, v.Reason)
	}
}

// TestConditionalOnAbaixoDoPisoERecusada — o mesmo par para §2.1: `conditional_on` só
// existe desde 1.1.0, logo um documento que a use carimbado 1.0.0 mente sobre o schema.
func TestConditionalOnAbaixoDoPisoERecusada(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = append(doc.Nodes, plan.Node{
		NodeID: "d", Role: "r", Objective: "o-d",
		ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}},
	})

	baixo := stamped(doc, 1, 0, 0)
	mustBeShapeValid(t, baixo)
	if v := Validate(baixo, baseSnapshot(), Ceilings{}); v.Reason != ReasonVersionBelowFeatures {
		t.Fatalf("reason = %q; queria plan_version_below_features", v.Reason)
	}
	if v := Validate(stamped(doc, 1, 1, 0), baseSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("o mesmo documento carimbado 1.1.0 foi recusado: (%s/%s)", v.Rule, v.Reason)
	}
}

// TestPlanoSemExtensoesAceitaLinhaAntiga é o contra-facto que impede a regra de virar
// «carimba sempre a corrente»: um documento que NÃO usa nenhuma extensão continua
// admissível carimbado 1.0.0. O piso é derivado do que o documento USA — não do que o
// leitor publica —, que é o que mantém a retrocompatibilidade real.
func TestPlanoSemExtensoesAceitaLinhaAntiga(t *testing.T) {
	if v := Validate(stamped(baseDoc(), 1, 0, 0), baseSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("documento pré-ADR-022 recusado na sua própria linha: (%s/%s)", v.Rule, v.Reason)
	}
}

// TestMinorAcimaDoLeitorERecusado — o simétrico: um documento que reivindica um MINOR do
// MAJOR corrente que este leitor não publica é recusado com sub-código PRÓPRIO. Sem ele,
// um documento 1.9.0 sem campos novos atravessava a regra 1 e ficava congelado a
// reivindicar um contrato que ninguém emitiu.
func TestMinorAcimaDoLeitorERecusado(t *testing.T) {
	acima := stamped(baseDoc(), plan.CurrentPlanVersion.Major, plan.CurrentPlanVersion.Minor+1, 0)
	v := Validate(acima, baseSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonVersionAheadOfReader {
		t.Fatalf("veredicto = (%s/%s); queria (schema/plan_version_ahead_of_reader)", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "" {
		t.Fatalf("locator = %q; a violação é do documento, não de um nó", v.Locator.NodeID)
	}
}

// TestPisoDerivaDeCadaFeature cobre a TABELA inteira (`plan.schemaFeatures`) de uma vez,
// incluindo o literal reservado `role: verifier` — que não é um campo novo e por isso
// não teria sítio óbvio num teste de admissão sem arrastar as regras de §2.2.
func TestPisoDerivaDeCadaFeature(t *testing.T) {
	v110 := plan.PlanVersion{Major: 1, Minor: 1, Patch: 0}
	v120 := plan.PlanVersion{Major: 1, Minor: 2, Patch: 0}

	casos := []struct {
		nome    string
		node    plan.Node
		piso    plan.PlanVersion
		feature string
	}{
		{"sem extensões", plan.Node{NodeID: "n", Role: "r", Objective: "o"}, plan.PlanVersion{}, ""},
		{"conditional_on", plan.Node{NodeID: "n", Role: "r", Objective: "o",
			ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}}}, v110, "conditional_on"},
		{"outputs", plan.Node{NodeID: "n", Role: "r", Objective: "o",
			Outputs: []plan.Output{summaryOut()}}, v120, "outputs"},
		{"consumes", plan.Node{NodeID: "n", Role: "r", Objective: "o",
			Consumes: []plan.PayloadEdge{{From: "a", Output: "resumo", Type: plan.PayloadSummary}}}, v120, "consumes"},
		{"role: verifier", plan.Node{NodeID: "n", Role: plan.RoleVerifier, Objective: "o"}, v120, "role_verifier"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			doc := baseDoc()
			doc.Nodes = []plan.Node{c.node}
			piso, uso := plan.FeatureFloor(doc)
			if !piso.Equal(c.piso) {
				t.Fatalf("piso = %s; queria %s", piso, c.piso)
			}
			if uso.Feature != c.feature {
				t.Fatalf("feature = %q; queria %q", uso.Feature, c.feature)
			}
		})
	}
}
