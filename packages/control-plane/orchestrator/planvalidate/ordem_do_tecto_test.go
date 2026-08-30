package planvalidate

// A ORDEM DO TECTO DE CARDINALIDADE — porque ela é observável, e o que a fixa.
//
// # O DEFEITO, medido
//
// O `MaxNodes` era conferido em [checkCeilings], a ÚLTIMA linha de [Validate] — depois de
// [buildAdmissionDAG] já ter construído o grafo inteiro. Como cada `AddEdge` impõe a
// aciclicidade com uma travessia de alcançabilidade, esse trabalho é O(E·(V+E)); o tecto
// que existe para BARRAR planos grandes só olhava para eles depois de os pagar por
// inteiro. Medido a 2026-08-30, com `MaxNodes=50`:
//
//	nós      antes        depois
//	 500     996 ms       0,0 ms
//	1000   2 524 ms       0,5 ms
//	2000  10 085 ms       1,1 ms
//
// # PORQUE ESTE TESTE É SOBRE ATRIBUIÇÃO E NÃO SOBRE TEMPO
//
// Um teste de duração seria instável na CI e mediria a máquina, não a ordem. O que a
// ordem MUDA de forma determinística é a RAZÃO por que um plano morre — e [Validate] é um
// ficheiro que trata isso como propriedade de primeira classe: cada passo traz um
// comentário a justificar porque corre onde corre, para que um plano morra «pela sua razão
// real». Fixar a atribuição fixa a ordem.
//
// # A DECISÃO DE ATRIBUIÇÃO, assumida
//
// Um plano que exceda a cardinalidade E tenha um ciclo passa a morrer por
// `max_nodes_exceeded` em vez de `cycle`. É a razão certa: um organigrama que nunca devia
// ter sido construído não merece um diagnóstico sobre a sua topologia interna — e a
// alternativa é pagar V⁴ para o descobrir.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// docSimples constrói um plano de n nós SEM arestas — só cardinalidade, para a fronteira
// do tecto ser testada sem que topologia nenhuma interfira.
func docSimples(t *testing.T, n int) plan.PlanDocument {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"plan_version":"1.0.0","objective":"o",`)
	b.WriteString(`"budget_total":{"tokens":0,"cost_micro_usd":0},`)
	b.WriteString(`"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"` + capHash + `"},"nodes":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"node_id":"n%d","role":"r","objective":"o","tools":null,"depends_on":[],`+
			`"budget_estimate":{"tokens":0,"cost_micro_usd":0},"risk_class":""}`, i)
	}
	b.WriteString(`]}`)
	doc, err := plan.Decode([]byte(b.String()))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return doc
}

// docComCicloE constrói um plano com n nós em que os dois primeiros formam um CICLO
// (n0↔n1). Os alvos existem todos: a rejeição procurada é de cardinalidade ou de ciclo,
// nunca de integridade referencial.
func docComCicloE(t *testing.T, n int) plan.PlanDocument {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"plan_version":"1.0.0","objective":"o",`)
	b.WriteString(`"budget_total":{"tokens":0,"cost_micro_usd":0},`)
	b.WriteString(`"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"` + capHash + `"},"nodes":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		deps := ""
		switch i {
		case 0:
			deps = `"n1"` // n0 depende de n1
		case 1:
			deps = `"n0"` // e n1 de n0 — ciclo
		}
		fmt.Fprintf(&b, `{"node_id":"n%d","role":"r","objective":"o","tools":null,"depends_on":[%s],`+
			`"budget_estimate":{"tokens":0,"cost_micro_usd":0},"risk_class":""}`, i, deps)
	}
	b.WriteString(`]}`)
	doc, err := plan.Decode([]byte(b.String()))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return doc
}

// TestOrdemDoTecto_CardinalidadeVenceOCiclo fixa a ordem. Com o MaxNodes de volta ao fim
// de [Validate], o plano morreria por `cycle` em [buildAdmissionDAG] — que é exactamente
// o custo que a mudança de ordem existe para não pagar.
func TestOrdemDoTecto_CardinalidadeVenceOCiclo(t *testing.T) {
	doc := docComCicloE(t, 5)
	v := Validate(doc, baseSnapshot(), Ceilings{MaxNodes: 2})
	if !v.Rejected() {
		t.Fatal("um plano de 5 nós com MaxNodes=2 tinha de ser rejeitado")
	}
	if v.Reason != ReasonMaxNodesExceeded {
		t.Fatalf("razão = %q, quero %q — o tecto de cardinalidade tem de correr ANTES do trabalho de grafo",
			v.Reason, ReasonMaxNodesExceeded)
	}
}

// TestOrdemDoTecto_DentroDoTectoOCicloContinuaAMatar é a metade que impede a mudança de
// ordem de MASCARAR diagnósticos. O tecto passa à frente do grafo, mas não o substitui:
// um plano que CABE no tecto tem de continuar a morrer pela sua topologia.
func TestOrdemDoTecto_DentroDoTectoOCicloContinuaAMatar(t *testing.T) {
	doc := docComCicloE(t, 5)
	v := Validate(doc, baseSnapshot(), Ceilings{MaxNodes: 50})
	if !v.Rejected() {
		t.Fatal("um plano com ciclo tinha de ser rejeitado")
	}
	if v.Reason != ReasonCycle {
		t.Fatalf("razão = %q, quero %q — dentro do tecto, o ciclo é a razão real", v.Reason, ReasonCycle)
	}
}

// TestOrdemDoTecto_SemPoliticaNadaMuda guarda a semântica declarada do valor-zero: «um
// tecto <= 0 significa sem limite, não rejeita tudo». Mover a verificação não podia
// transformar a ausência de política numa violação.
func TestOrdemDoTecto_SemPoliticaNadaMuda(t *testing.T) {
	doc := docComCicloE(t, 5)
	v := Validate(doc, baseSnapshot(), Ceilings{}) // MaxNodes = 0, desligado
	if v.Reason != ReasonCycle {
		t.Fatalf("razão = %q, quero %q — sem política de cardinalidade o plano morre pelo ciclo",
			v.Reason, ReasonCycle)
	}
}

// TestOrdemDoTecto_CardinalidadeVenceTambemAsTools cobre o outro lado: o tecto corre antes
// de [checkTools], logo um plano grande com uma tool irresolúvel morre pelo tamanho.
// Sem isto, mover a verificação para uma posição intermédia — depois do grafo, antes das
// tools — passaria neste ficheiro sem fechar o custo que motivou a mudança.
func TestOrdemDoTecto_CardinalidadeVenceTambemAsTools(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"plan_version":"1.0.0","objective":"o",`)
	b.WriteString(`"budget_total":{"tokens":0,"cost_micro_usd":0},`)
	b.WriteString(`"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"` + capHash + `"},"nodes":[`)
	for i := 0; i < 5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"node_id":"n%d","role":"r","objective":"o",`+
			`"tools":[{"name":"inexistente","version":"9.9.9","digest":"sha256:nada"}],`+
			`"depends_on":[],"budget_estimate":{"tokens":0,"cost_micro_usd":0},"risk_class":""}`, i)
	}
	b.WriteString(`]}`)
	doc, err := plan.Decode([]byte(b.String()))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if v := Validate(doc, baseSnapshot(), Ceilings{MaxNodes: 2}); v.Reason != ReasonMaxNodesExceeded {
		t.Fatalf("razão = %q, quero %q", v.Reason, ReasonMaxNodesExceeded)
	}
}

// TestDefaultMaxNodes_EstaLigadoAoTecto amarra a constante derivada à verificação. Sem
// isto, [DefaultMaxNodes] seria um número num comentário: alguém podia mudá-lo e nada
// notaria, ou o tecto podia deixar de o respeitar.
//
// A fronteira é testada nos DOIS lados de propósito — exactamente no tecto passa, um
// acima cai. Só o lado de cima deixaria uma constante a zero passar por boa.
func TestDefaultMaxNodes_EstaLigadoAoTecto(t *testing.T) {
	noTecto := docSimples(t, DefaultMaxNodes)
	if v := Validate(noTecto, baseSnapshot(), Ceilings{MaxNodes: DefaultMaxNodes}); v.Rejected() {
		t.Fatalf("um plano com exactamente %d nós está DENTRO do tecto e foi rejeitado: %s",
			DefaultMaxNodes, v.Reason)
	}
	acima := docSimples(t, DefaultMaxNodes+1)
	v := Validate(acima, baseSnapshot(), Ceilings{MaxNodes: DefaultMaxNodes})
	if v.Reason != ReasonMaxNodesExceeded {
		t.Fatalf("um plano com %d nós devia exceder o tecto de %d; razão = %q",
			DefaultMaxNodes+1, DefaultMaxNodes, v.Reason)
	}
}

// TestDefaultMaxNodes_ERevisivel guarda a DERIVAÇÃO, não o número. O tecto vem da
// revisibilidade humana — um cartão de aprovação por nó, todos lidos antes de autorizar
// despesa. Um valor que saia desta banda deixa de ser derivável dessa base: em baixo
// recusa organigramas legítimos, em cima deixa de caber numa revisão.
//
// Se a derivação mudar, este teste tem de mudar COM ela, e é esse o ponto: obriga a
// escrever a razão nova em vez de mexer no número em silêncio.
func TestDefaultMaxNodes_ERevisivel(t *testing.T) {
	if DefaultMaxNodes < 16 || DefaultMaxNodes > 256 {
		t.Fatalf("DefaultMaxNodes = %d está fora da banda que a derivação por revisibilidade "+
			"humana sustenta (16..256). Se o valor mudou de base, actualize a derivação no "+
			"comentário da constante — não só o número", DefaultMaxNodes)
	}
}
