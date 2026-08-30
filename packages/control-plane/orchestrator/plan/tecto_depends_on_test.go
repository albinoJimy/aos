package plan

// TECTO DE ARIDADE DO `depends_on` — o que faltava para o nº de ARESTAS ser função do
// nº de NÓS.
//
// # O DEFEITO, medido
//
// O `Decode` já limitava a aridade das arestas condicionais (8), dos predicados (8), dos
// `outputs` (8) e dos `consumes` (16). O `depends_on` não tinha tecto nenhum, pelo que um
// documento podia declarar E = O(V²). E o custo de `planvalidate.buildAdmissionDAG` é
// O(E·(V+E)) — cada `AddEdge` corre uma travessia de alcançabilidade para impor a
// aciclicidade —, logo o trabalho crescia com V⁴.
//
// Medido a 2026-08-30, com os nós declarados por ORDEM INVERSA (a que maximiza o
// fecho-para-a-frente, e que é escolhida por quem escreve o documento):
//
//	nós   arestas    buildAdmissionDAG SEM tecto    COM tecto
//	200    19 900              3 512 ms             recusado no Decode (9 ms)
//	400    79 800             43 958 ms             recusado no Decode (23 ms)
//	600   179 700            215 604 ms             recusado no Decode (48 ms)
//
// 600 nós é um plano de 1,28 MB que era ACEITE e custava 3 minutos e 36 segundos numa só
// chamada. Com o tecto, é recusado na FORMA, antes de qualquer trabalho de grafo.
//
// # O QUE ISTO NÃO FECHA, dito por extenso
//
// O tecto torna E linear em V (E ≤ 8V), o que baixa o custo de O(V⁴) para O(V²) — mas
// NÃO limita V. Medido: 2000 nós dentro do tecto continuam a custar ~10 s. Quem quiser
// fechar isso tem de usar o `Ceilings.MaxNodes` do validador, que existe, é conferido no
// FIM de `Validate` (depois de o DAG estar construído) e não é configurado em produção.
// Este ficheiro não trata desse eixo; declara-o.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// docComDeps constrói um documento de dois nós em que o segundo declara n dependências.
// Os alvos existem todos: a recusa que se procura é de ARIDADE, não de integridade
// referencial — senão o teste passaria pela razão errada.
func docComDeps(n int) string {
	var nos strings.Builder
	var deps strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			deps.WriteString(",")
		}
		fmt.Fprintf(&deps, `"d%d"`, i)
		fmt.Fprintf(&nos, `{"node_id":"d%d","role":"r","objective":"o","tools":null,"depends_on":null,`+
			`"budget_estimate":{"tokens":0,"cost_micro_usd":0},"risk_class":""},`, i)
	}
	return `{"plan_version":"1.0.0","objective":"o",` +
		`"budget_total":{"tokens":0,"cost_micro_usd":0},` +
		`"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},"nodes":[` +
		nos.String() +
		`{"node_id":"alvo","role":"r","objective":"o","tools":null,"depends_on":[` + deps.String() + `],` +
		`"budget_estimate":{"tokens":0,"cost_micro_usd":0},"risk_class":""}]}`
}

// TestDependsOnAcimaDoTectoERecusado é a fronteira: no tecto passa, um acima cai.
func TestDependsOnAcimaDoTectoERecusado(t *testing.T) {
	// EXACTAMENTE no tecto: tem de passar. Sem esta metade, um tecto a zero passaria
	// no caso de cima e o teste não distinguiria nada.
	if _, err := Decode([]byte(docComDeps(maxDependsOnPerNode))); err != nil {
		t.Fatalf("%d dependências estão DENTRO do tecto e foram recusadas: %v", maxDependsOnPerNode, err)
	}
	// Um acima: recusado, e pela razão certa.
	_, err := Decode([]byte(docComDeps(maxDependsOnPerNode + 1)))
	if !errors.Is(err, ErrTooManyDependsOn) {
		t.Fatalf("erro = %v; queria ErrTooManyDependsOn", err)
	}
	// A mensagem tem de nomear os números — quem escreve o plano precisa de saber o
	// tecto sem ir ao código.
	if msg := err.Error(); !strings.Contains(msg, fmt.Sprintf("%d > %d", maxDependsOnPerNode+1, maxDependsOnPerNode)) {
		t.Fatalf("a mensagem não nomeia o observado e o tecto: %q", msg)
	}
}

// TestDependsOnDensoERecusadoAntesDoGrafo prova o que o tecto existe para impedir: um
// documento denso é recusado na FORMA, sem que o custo O(E·(V+E)) do grafo chegue a ser
// pago. É a diferença entre 48 ms e 3 minutos e 36 segundos.
func TestDependsOnDensoERecusadoAntesDoGrafo(t *testing.T) {
	const n = 200
	var b strings.Builder
	b.WriteString(`{"plan_version":"1.0.0","objective":"o",`)
	b.WriteString(`"budget_total":{"tokens":0,"cost_micro_usd":0},`)
	b.WriteString(`"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},"nodes":[`)
	for k := 0; k < n; k++ {
		i := n - 1 - k
		if k > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"node_id":"n%d","role":"r","objective":"o","tools":null,"depends_on":[`, i)
		for j := i - 1; j >= 0; j-- {
			if j < i-1 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"n%d"`, j)
		}
		b.WriteString(`],"budget_estimate":{"tokens":0,"cost_micro_usd":0},"risk_class":""}`)
	}
	b.WriteString(`]}`)

	if _, err := Decode([]byte(b.String())); !errors.Is(err, ErrTooManyDependsOn) {
		t.Fatalf("um plano com E=O(V²) foi ACEITE na forma: err=%v", err)
	}
}

// TestDependsOnTectoAcompanhaOsIrmaos amarra o valor aos tectos de aridade vizinhos. Não
// é cosmética: um tecto muito mais alto que os irmãos reabre o eixo de custo em silêncio,
// e um muito mais baixo recusa planos legítimos que os irmãos aceitariam.
func TestDependsOnTectoAcompanhaOsIrmaos(t *testing.T) {
	if maxDependsOnPerNode != maxConditionalEdgesPerNode {
		t.Fatalf("o tecto do depends_on (%d) divergiu do das arestas condicionais (%d) — "+
			"se a divergência é deliberada, escreva a razão aqui em vez de a deixar acontecer",
			maxDependsOnPerNode, maxConditionalEdgesPerNode)
	}
}
