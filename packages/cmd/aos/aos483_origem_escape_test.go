package main

// aos483_origem_escape_test.go — O ESCAPE DO NÓ DECIDE PELA REGRA, NÃO POR UMA LISTA.
//
// # PORQUE É QUE ESTE FICHEIRO EXISTE, E O QUE O AOS-477 NÃO APANHOU
//
// O `idDoRunFilho` deste binário reconstrói o id que o `aos-orq` compõe, para conferir que o
// `node_id` declarado é mesmo o do `run_id`. Os dois binários não se importam (ADR-018), pelo
// que a forma é um CONTRATO mantido em duas cópias — e uma cópia só vale enquanto algo a
// confrontar com a outra.
//
// O [TestAOS477IdDoRunFilhoTemOsVectoresDoOrquestrador] existe para isso, e **não chegou**:
// percorre `aos477VectoresDoRunFilho`, um mapa de exemplos escritos à mão. Quando o AOS-483
// acrescentou o `:` à regra do lado do orquestrador, nenhum vector o continha — o teste ficou
// verde com os dois lados a produzir ids DIFERENTES para `etapa:build`, e a conferência da
// origem teria recusado um run legítimo em produção.
//
// É o padrão da allowlist escrita à mão: um detector que partilha o modo de falha do defeito
// que persegue, porque esquecer uma entrada produz o mesmo silêncio que esquecer a regra.
//
// Os testes abaixo prendem o PREDICADO. Se qualquer uma das duas regras apertar, ficam
// vermelhos sem ninguém se lembrar de acrescentar um vector.

import (
	"os"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// escapouONo diz se o [idDoRunFilho] escapou o byte `c` quando ele é o `node_id` inteiro.
func escapouONo(c byte) bool {
	return idDoRunFilho("p", string(rune(c))) != "p"+separadorDoRunFilho+string(rune(c))
}

// TestAOS483EscapeDoNoCobreOsDoisPredicados varre o espaço de um byte e exige que o nó escape
// exactamente o que as duas regras recusam — nem menos (o orquestrador escapa e o nó não
// reconhece), nem mais (o nó escapa o que o orquestrador deixa passar).
func TestAOS483EscapeDoNoCobreOsDoisPredicados(t *testing.T) {
	for b := 0; b < 256; b++ {
		c := byte(b)
		if c == marcaDeEscapeDoRunFilho {
			continue // a marca duplica-se em vez de virar `+<hex>`; é o caso próprio abaixo
		}
		esperado := eventstore.CaractereNaoRepresentavel(rune(c)) ||
			durable.CaractereProibidoNaChave(rune(c)) ||
			c == separadorDoRunFilho[0]
		if got := escapouONo(c); got != esperado {
			t.Errorf("byte 0x%02x: o nó escapa=%v, mas a união das regras diz %v.\n"+
				"O id do run filho é ao mesmo tempo um stream_id e um run_id da chave de "+
				"idempotência. Se o nó e o `aos-orq` não decidirem pelos MESMOS predicados, um "+
				"compõe uma forma e o outro espera outra, e a conferência da origem recusa um "+
				"run legítimo (AOS-483).", b, got, esperado)
		}
	}
}

// TestAOS483AMarcaContinuaADuplicarSe é o que torna o escape reversível e, portanto, injectivo.
// Sem ele, o varrimento acima passaria com uma marca que atravessasse intacta — e um `node_id`
// que já contivesse a marca colidiria com o escape de outro.
func TestAOS483AMarcaContinuaADuplicarSe(t *testing.T) {
	quer := "p" + separadorDoRunFilho + string([]byte{marcaDeEscapeDoRunFilho, marcaDeEscapeDoRunFilho})
	if got := idDoRunFilho("p", string(rune(marcaDeEscapeDoRunFilho))); got != quer {
		t.Fatalf("idDoRunFilho(p, %q) = %q, quer %q: a marca deixou de se duplicar e o escape "+
			"deixou de ser injectivo", string(rune(marcaDeEscapeDoRunFilho)), got, quer)
	}
}

// TestAOS483OOrquestradorDecidePelosMesmosDoisPredicados lê a FONTE do outro binário, no molde
// do [TestAOS439SeparadorDoRunFilhoCasaComOOrquestrador].
//
// Não substitui o varrimento acima — prova outra coisa: que a decisão do lado de lá também vem
// dos predicados, e não de uma lista. Um `deveEscapar` que voltasse a escrever o carácter à mão
// passaria no varrimento deste lado e divergiria na primeira vez que uma das regras apertasse.
func TestAOS483OOrquestradorDecidePelosMesmosDoisPredicados(t *testing.T) {
	b, err := os.ReadFile("../aos-orq/node_executor.go")
	if err != nil {
		t.Fatalf("não consegui ler a fonte do orquestrador: %v", err)
	}
	fonte := string(b)
	i := strings.Index(fonte, "func deveEscapar(")
	if i < 0 {
		t.Fatal("o `aos-orq` já não declara `deveEscapar`: o contrato da forma do id do run " +
			"filho mudou de sítio e este teste deixou de o vigiar")
	}
	corpo := fonte[i:]
	if j := strings.Index(corpo, "\n}"); j > 0 {
		corpo = corpo[:j]
	}
	for _, exigido := range []string{
		"eventstore.CaractereNaoRepresentavel",
		"durable.CaractereProibidoNaChave",
		"separadorDoRunFilho[0]",
	} {
		if !strings.Contains(corpo, exigido) {
			t.Errorf("o `deveEscapar` do `aos-orq` já não decide por %s.\n"+
				"As duas cópias do escape (aqui e lá) têm de derivar dos MESMOS predicados; "+
				"assim que uma passar a listar caracteres à mão, voltam a divergir em silêncio "+
				"— ver o cabeçalho deste ficheiro e AOS-483.", exigido)
		}
	}
}

// TestAOS483OsDoisPontosNaoChegamCruos é o caso concreto que o ticket mediu, deste lado.
func TestAOS483OsDoisPontosNaoChegamCruos(t *testing.T) {
	got := idDoRunFilho("run-x", "etapa:build")
	if strings.Contains(got, ":") {
		t.Fatalf("idDoRunFilho(run-x, etapa:build) = %q: o `:` chega cru, e a chave de "+
			"idempotência `run_id:step_id` deixa de ter decomposição única", got)
	}
	// O caso comum não muda: os ids que já existiam continuam a ser o que eram.
	if got := idDoRunFilho("run-x", "n1"); got != "run-x~n1" {
		t.Fatalf("um node_id sem caracteres a escapar mudou de forma: %q", got)
	}
}
