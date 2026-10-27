package main

// aos483_node_id_chave_test.go — O ID DO RUN FILHO SERVE DUAS REGRAS, E TEM DE PASSAR NAS DUAS.
//
// O `childRunID` é ao mesmo tempo um `stream_id` do Event Store e um `run_id` da chave de
// idempotência do runtime durável. Os dois recusam coisas DIFERENTES, e o `:` é o caso que o
// mostra: um subject NATS representa-o, pelo que o predicado do Event Store deixava-o passar; o
// durável recusa-o, porque é o delimitador de `run_id:step_id`.
//
// Medido no ticket AOS-483: um `node_id` `a:b` produzia `run-x~a:b`, o `POST /runs` respondia
// 201 e o run falhava logo. Um plano válido não corria.
//
// Estes testes prendem as três gramáticas pelo PREDICADO de cada regra, não por uma lista de
// caracteres — uma lista escrita à mão partilha o modo de falha do defeito que persegue.

import (
	"strings"
	"testing"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS483IDDoRunFilhoEAceitoPelaChaveDuravel é a asserção que fecha o ticket: para TODO o
// `node_id` que a gramática do plano admite, o id do run filho composto é um input válido da
// chave de idempotência.
//
// Varre o charset admitido um carácter de cada vez em vez de usar exemplos: é o que torna a
// prova sobre a gramática, e não sobre os casos de que alguém se lembrou.
func TestAOS483IDDoRunFilhoEAceitoPelaChaveDuravel(t *testing.T) {
	for b := 0; b < 256; b++ {
		c := byte(b)
		nodeID := "n" + string(rune(c)) + "o"
		if !plan.ValidNodeID(nodeID) {
			continue // fora da gramática do plano: não é este o eixo
		}
		filho := childRunID("run-x", nodeID)
		if _, err := durable.IdempotencyKey(filho, "passo-1"); err != nil {
			t.Errorf("node_id %q (byte 0x%02x) compõe o run filho %q, que a chave de "+
				"idempotência RECUSA: %v.\nO escape do childRunID tem de cobrir tudo o que o "+
				"durable.CaractereProibidoNaChave recusa — ver deveEscapar e AOS-483.",
				nodeID, b, filho, err)
		}
	}
}

// TestAOS483IDDoRunFilhoEStreamIDValido é a outra metade da mesma obrigação, e está aqui para
// que apertar uma das regras não possa quebrar a outra em silêncio.
func TestAOS483IDDoRunFilhoEStreamIDValido(t *testing.T) {
	for b := 0; b < 256; b++ {
		c := byte(b)
		nodeID := "n" + string(rune(c)) + "o"
		if !plan.ValidNodeID(nodeID) {
			continue
		}
		filho := childRunID("run-x", nodeID)
		if err := eventstore.ValidarStreamID(filho); err != nil {
			t.Errorf("node_id %q (byte 0x%02x) compõe o run filho %q, que não é um stream_id "+
				"válido: %v", nodeID, b, filho, err)
		}
	}
}

// TestAOS483EscapeCobreOsDoisPredicados prende as regras à função que decide, em vez de à lista
// de caracteres. Se qualquer um dos dois predicados apertar e o `deveEscapar` não acompanhar,
// isto fica vermelho no mesmo commit.
func TestAOS483EscapeCobreOsDoisPredicados(t *testing.T) {
	for b := 0; b < 256; b++ {
		c := byte(b)
		esperado := eventstore.CaractereNaoRepresentavel(rune(c)) ||
			durable.CaractereProibidoNaChave(rune(c)) ||
			c == separadorDoRunFilho[0]
		if got := deveEscapar(c); got != esperado {
			t.Errorf("byte 0x%02x: deveEscapar=%v mas a união dos predicados diz %v.\n"+
				"O deveEscapar TEM de ser a união das regras que o id do run filho serve; "+
				"qualquer carácter que uma delas recuse e ele deixe passar é um plano válido "+
				"que não corre.", b, got, esperado)
		}
	}
}

// TestAOS483EscapeContinuaInjectivoComOsDoisPontos é o controlo que a primeira versão do escape
// não teve e que lhe custou uma colisão: dois `node_id` distintos nunca podem produzir o mesmo
// run filho.
//
// O par que importa é `a:b` contra a sua própria forma escapada em texto. Com um escape não
// injectivo, um `node_id` que JÁ contenha a sequência de escape colide com o escape de outro —
// foi assim que `a.b` e `a_2eb` acabaram no mesmo stream quando a marca era o `_`.
func TestAOS483EscapeContinuaInjectivoComOsDoisPontos(t *testing.T) {
	vistos := map[string]string{}
	candidatos := []string{
		"a:b", "a+3ab", "a++3ab", "ab", "a.b", "a+2eb", "a:b:c", "a+", "a++",
		":", "+", "a-b", "a_b", "n1", "pesquisa",
	}
	for _, nodeID := range candidatos {
		filho := childRunID("run-x", nodeID)
		if antes, ok := vistos[filho]; ok {
			t.Errorf("os node_id %q e %q produzem o MESMO run filho %q: dois nós do plano no "+
				"mesmo stream, que é pior do que o problema que o escape resolve", antes, nodeID, filho)
			continue
		}
		vistos[filho] = nodeID
	}
}

// TestAOS483OsDoisPontosEscapamDeFacto é o controlo de não-vacuidade dos testes acima: eles
// passariam se o escape escapasse TUDO, ou se a gramática do plano deixasse de admitir o `:`.
//
// Se a gramática for apertada algum dia (a outra saída que o ADR-029 §3 deixou), este teste é o
// sítio onde se vê que o escape passou a ser desnecessário para este carácter — e o
// TestAOS424CharsetDoNodeIDEstaAcopladoAoStream, no pacote `plan`, fica vermelho primeiro.
func TestAOS483OsDoisPontosEscapamDeFacto(t *testing.T) {
	if !plan.ValidNodeID("etapa:build") {
		t.Skip("a gramática do plano deixou de admitir `:`: ver ADR-029 §3 e o teste do AOS-424")
	}
	filho := childRunID("run-x", "etapa:build")
	if strings.Contains(filho, ":") {
		t.Fatalf("o run filho %q ainda tem `:` cru: o escape não cobre o delimitador da chave "+
			"durável, que é o defeito do AOS-483", filho)
	}
	if !strings.HasPrefix(filho, "run-x"+separadorDoRunFilho) {
		t.Fatalf("o run filho %q perdeu a forma `<plano>~<nó>`, que é contrato do nó desde o "+
			"AOS-439", filho)
	}
	// O caso comum não muda: um node_id sem caracteres a escapar atravessa intacto, e os ids
	// que já existiam continuam a ser o que eram.
	if got := childRunID("run-x", "pesquisa"); got != "run-x~pesquisa" {
		t.Fatalf("um node_id sem caracteres a escapar mudou de forma: %q", got)
	}
}
