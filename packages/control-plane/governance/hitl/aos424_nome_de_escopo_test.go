package hitl

// aos424_nome_de_escopo_test.go — OS NOMES COMPOSTOS DESTE PACOTE SÃO SEMPRE REPRESENTÁVEIS.
//
// Estes dois streams foram o que travou o aperto do `Append` (AOS-424), e nenhum deles aparecia
// no inventário: esse inventário procurou LITERAIS, e estes nomes compõem-se em runtime a partir
// de constantes de domínio noutro pacote, de um `request_id` do corpo de um pedido, e de um
// tuplo separado por `\x00`.
//
// A asserção usa a FONTE da regra ([eventstore.ValidarStreamID]) e não uma lista repetida aqui:
// se a regra apertar, estes testes apertam com ela.

import (
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// escoposAdversariais cobre as três proveniências reais mais o que elas sugerem.
var escoposAdversariais = []string{
	// (1) constantes de domínio do autenticador — as três têm ponto
	"foureyes.challenge", "governance.dsar", "nhi.revoke", "autonomy",
	// (2) o tuplo do nonceScope, com o separador de CONTROLO lá dentro
	"steer:foureyes.challenge\x00human:approver-1",
	"steer:run-x\x00nhi:agent-7",
	// (3) o request_id do cliente, via integration.ChallengeScope
	"4eyes:req-1.10", "4eyes:req sem aspas", "4eyes:req*", "4eyes:req>", "4eyes:",
	// (4) um RatificationID opaco de fonte externa
	"", "  ", "\t\r\n", "já.com.acentos", strings.Repeat("a.", 200),
}

// O NOME COMPOSTO É SEMPRE UM `stream_id` VÁLIDO, venha o escopo de onde vier.
func TestAOS424NomesCompostosSaoSempreValidos(t *testing.T) {
	const nonce32 = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	for _, escopo := range escoposAdversariais {
		nonce := nonceStreamPrefix + nomeDeEscopo(escopo) + ":" + nonce32
		if err := eventstore.ValidarStreamID(nonce); err != nil {
			t.Errorf("o stream do nonce para o escopo %q nao e valido (%q): %v\n"+
				"sobre JetStream o ConsumeNonce devolveria erro de backend, o gate trataria isso\n"+
				"como bloqueio, e TODA a ratificacao seria negada com um 403 que nao diz porque",
				escopo, nonce, err)
		}
		challenge := challengeStream(escopo, []byte{0x0f, 0x1e, 0x2d, 0x3c})
		if err := eventstore.ValidarStreamID(challenge); err != nil {
			t.Errorf("o stream do challenge para o escopo %q nao e valido (%q): %v",
				escopo, challenge, err)
		}
	}
}

// INJECTIVIDADE — escopos distintos não podem partilhar stream.
//
// Uma colisão aqui NÃO seria um bypass: dois escopos no mesmo stream fazem o segundo consumo ver
// um replay e NEGAR. Seria uma negação silenciosa de uma cerimónia legítima — e essas são as que
// ninguém diagnostica, porque o sintoma é um 403 correcto à superfície.
func TestAOS424NomeDeEscopoEeInjectivo(t *testing.T) {
	vistos := map[string]string{}
	for _, escopo := range escoposAdversariais {
		n := nomeDeEscopo(escopo)
		if anterior, colide := vistos[n]; colide {
			t.Errorf("COLISAO: os escopos %q e %q produzem o mesmo segmento %q", anterior, escopo, n)
			continue
		}
		vistos[n] = escopo
	}
}

// ESTÁVEL: emitir e verificar têm de concordar no nome, senão o challenge emitido nunca é
// encontrado e a perna é negada sempre.
func TestAOS424NomeDeEscopoEeEstavel(t *testing.T) {
	for _, escopo := range escoposAdversariais {
		if a, b := nomeDeEscopo(escopo), nomeDeEscopo(escopo); a != b {
			t.Fatalf("nomeDeEscopo(%q) nao e determinista: %q != %q", escopo, a, b)
		}
	}
	// E o helper partilhado tem de dar o mesmo nome às duas chamadas de challengeStream — é o
	// que faz o IssueChallenge e o verificador olharem para o mesmo stream. Guarda contra
	// alguém lhe acrescentar um carimbo ou um sal: nesse dia o challenge emitido deixaria de
	// ser encontrado e TODA a perna seria negada, em silêncio e com o 403 correcto à superfície.
	//
	// Pelas VARIÁVEIS e não em linha: o `staticcheck` lê `f(x) != f(x)` como tautologia
	// (SA4000) e tem razão a olhar só para a forma — o que se está a afirmar é sobre a FUNÇÃO,
	// não sobre as expressões.
	c := []byte{1, 2, 3}
	primeira := challengeStream("4eyes:req-1", c)
	segunda := challengeStream("4eyes:req-1", c)
	if primeira != segunda {
		t.Errorf("challengeStream nao e determinista (%q != %q): emitir e verificar deixariam "+
			"de concordar no nome do stream", primeira, segunda)
	}
	if outroPedido := challengeStream("4eyes:req-2", c); primeira == outroPedido {
		t.Errorf("challengeStream colapsou dois request_id distintos no mesmo stream (%q)", primeira)
	}
}

// O PREFIXO SOBREVIVE. Resumir o escopo paga-se em legibilidade, e o que compra o troco é o
// prefixo continuar a dizer de que classe é o stream — um operador que vê `ratify-nonce:…` sabe
// o que está a olhar sem ter de reverter um resumo que não é reversível.
func TestAOS424PrefixoContinuaLegivel(t *testing.T) {
	n := nonceStreamPrefix + nomeDeEscopo("governance.dsar") + ":abcd"
	if !strings.HasPrefix(n, "ratify-nonce:") {
		t.Errorf("o stream do nonce perdeu o prefixo de classe: %q", n)
	}
	if !strings.HasPrefix(challengeStream("x", []byte{1}), "4eyes-challenge:") {
		t.Error("o stream do challenge perdeu o prefixo de classe")
	}
}
