package main

// aos425_fronteira_de_entrada_test.go — O `--run` DO `aos-orq` É UMA PORTA DE ENTRADA, E ESTAVA
// SEM GUARDA.
//
// # O DEFEITO
//
// O valor de `--run` torna-se QUATRO nomes de stream, nenhum deles literal:
//
//	<run_id>                 o stream do próprio run     (runlifecycle.Claim)
//	lease:<run_id>           a posse exclusiva           (agent-runtime/durable)
//	<run_id>-plan            os eventos do planeador     (plannerevents.Recorder)
//	<run_id>                 o id da árvore de orçamento (budget.New, no aos-orq)
//
// O nó de referência já validava o MESMO valor nas duas portas HTTP (`POST /runs` e
// `POST /plans`, AOS-424). Este binário validava só o vazio e o `~`. O mesmo valor, duas
// portas, uma guardada — e o `aos-orq` corre em produção desde a v0.1.20.
//
// Sem guarda, a recusa dá-se no `Append`, DEPOIS de o lease estar reclamado, e a mensagem fala
// de um `stream_id` que o operador nunca escreveu.

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// O `--run` IRREPRESENTÁVEL É RECUSADO, e antes de se ler o ambiente.
//
// O teste não monta env var nenhuma DE PROPÓSITO: é isso que prova que a recusa acontece antes
// da resolução do executor de nós. Se a verificação voltar a descer no `cmdServe`, este teste
// passa a falhar com um erro de ambiente e acusa a regressão.
func TestAOS425ServeRecusaRunIDIrrepresentavel(t *testing.T) {
	for _, caso := range []struct {
		run    string
		porque string
	}{
		{"cliente.pedido-1", "o ponto separa tokens num subject NATS"},
		{"run com espaco", "o espaco nao e transportavel"},
		{"run*", "curinga"},
		{"run>", "curinga"},
		{"run\tcom-tab", "tab"},
		{"run\x01controlo", "caractere de controlo — a classe que a lista nomeada nao continha"},
	} {
		err := cmdServe([]string{"--run", caso.run})
		if err == nil {
			t.Errorf("cmdServe(--run %q) devia recusar (%s), passou", caso.run, caso.porque)
			continue
		}
		if !errors.Is(err, eventstore.ErrConfig) {
			t.Errorf("cmdServe(--run %q) devia recusar com ErrConfig (%s), veio: %v",
				caso.run, caso.porque, err)
			continue
		}
		// A mensagem tem de nomear o FLAG. É a diferença entre isto e a recusa tardia no
		// `Append`, que nomeia um `stream_id` que o operador nunca escreveu.
		if !strings.Contains(err.Error(), "--run") {
			t.Errorf("a recusa de %q nao nomeia o flag `--run`: %v", caso.run, err)
		}
	}
}

// O `--plan` EXPLÍCITO tem a sua própria guarda: o derivado herda a validade do `--run`, um
// valor dado à mão não herda nada.
func TestAOS425ServeRecusaPlanIDIrrepresentavel(t *testing.T) {
	err := cmdServe([]string{"--run", "run-425", "--plan", "plano.de.teste"})
	if !errors.Is(err, eventstore.ErrConfig) {
		t.Fatalf("um --plan com ponto devia ser recusado, veio: %v", err)
	}
	if !strings.Contains(err.Error(), "--plan") {
		t.Errorf("a recusa nao nomeia o flag `--plan`: %v", err)
	}
}

// O `~` CONTINUA A TER A SUA PRÓPRIA MENSAGEM.
//
// O `~` É representável num subject — a guarda do AOS-413 existe por outra razão (a unicidade
// do id do run filho). Se a validação nova a engolisse, a mensagem deixaria de explicar a razão
// verdadeira, e um operador que use `~` no id ficaria sem saber porquê.
func TestAOS425OSeparadorMantemAMensagemDoAOS413(t *testing.T) {
	err := cmdServe([]string{"--run", "run~425"})
	if err == nil {
		t.Fatal("um --run com `~` devia continuar a ser recusado")
	}
	if !strings.Contains(err.Error(), "runs filhos") {
		t.Errorf("a recusa do `~` perdeu a razao do AOS-413 (unicidade do run filho): %v", err)
	}
	if errors.Is(err, eventstore.ErrConfig) {
		t.Errorf("o `~` E representavel num subject; recusa-lo como nome invalido troca a razao: %v", err)
	}
}

// O CASO BOM CONTINUA A PASSAR PELA VALIDAÇÃO.
//
// Sem esta metade, uma guarda que recusasse TUDO passaria os testes acima.
//
// A paragem seguinte é DETERMINISTA e é `substrato.validar()`: sem `--wal` nem `--nats` não há
// canal de coordenação. Asserir essa mensagem concreta — em vez de «algum erro» — é o que
// impede este teste de se auto-desligar.
//
// (Uma versão anterior deste teste dizia que a paragem era a «falta de ambiente» para o
// executor de nós. Era FALSO: `nodeClientDoAmbiente` devolve `(nil, nil)` sem
// `AOS_ORQ_NODE_URL`, não erra. A asserção era condicional e não provava nada.)
func TestAOS425RunIDValidoAtravessaAValidacao(t *testing.T) {
	err := cmdServe([]string{"--run", "run-425-valido"})
	if err == nil {
		t.Fatal("o serve sem --wal nem --nats tinha de parar; se passou, este teste deixou de " +
			"exercitar o caminho e pode ter aberto um WAL")
	}
	if errors.Is(err, eventstore.ErrConfig) {
		t.Errorf("um --run valido foi recusado pela validacao de nome: %v", err)
	}
	if !strings.Contains(err.Error(), "--wal") {
		t.Errorf("esperava parar em `substrato.validar()` (sem --wal nem --nats), veio: %v\n"+
			"se a paragem mudou, confirme que o --run valido continua a atravessar a fronteira", err)
	}
}

// A ORDEM: O FLAG É JULGADO ANTES DO AMBIENTE.
//
// Com o `--run` mau E o `AOS_ORQ_NODE_URL` mau, o operador tem de ouvir falar do flag que
// escreveu — não de uma variável que não mencionou. É esta a garantia que a reordenação
// introduziu, e sem este teste ela não era exercitada por nada.
func TestAOS425OFlagEeJulgadoAntesDoAmbiente(t *testing.T) {
	t.Setenv("AOS_ORQ_NODE_URL", "isto-nao-e-um-url")
	err := cmdServe([]string{"--run", "cliente.pedido-1"})
	if err == nil {
		t.Fatal("com os dois maus, o serve tinha de recusar")
	}
	if !strings.Contains(err.Error(), "--run") {
		t.Errorf("com `--run` mau e ambiente mau, a mensagem devia nomear o FLAG, veio: %v", err)
	}
}

// E O ESCAPE DO `node_id` DECIDE PELA MESMA REGRA QUE O `Append` IMPÕE.
//
// O `deveEscapar` derivava a decisão de [eventstore.CaracteresNaoRepresentaveis], que é um
// SUBCONJUNTO PRÓPRIO do que a regra recusa desde que ela passou a apanhar os caracteres de
// controlo. Estava tapado por a gramática do `node_id` ter charset fechado — isto é, por acaso.
func TestAOS425EscapeCobreTudoOQueORegraRecusa(t *testing.T) {
	// O ALFABETO É 0x00–0x7F, e a razão é uma armadilha medida: `string(byte(0xc3))` NÃO produz
	// o byte cru — produz a codificação UTF-8 de U+00C3, DOIS bytes. Metade de um ciclo até 256
	// compararia duas respostas «não» sobre uma string que não contém o byte em causa, e passaria
	// por cobertura sem cobrir nada.
	//
	// Os bytes >= 0x80 ficam declarados como resíduo do AOS-425: um `node_id` com um byte cru
	// não-ASCII não é escapado nem recusado (a regra descodifica-o como U+FFFD). A gramática do
	// `node_id` não os admite, mas o `Decode` não a impõe — é a mesma porta pela qual o `~` entra,
	// e essa foi fechada por escape explícito.
	for c := 0; c < 0x80; c++ {
		b := byte(c)
		nome := "a" + string(rune(c)) + "b"
		if len(nome) != 3 {
			t.Fatalf("fixture partida: %q tem %d bytes, esperava 3", nome, len(nome))
		}
		if eventstore.ValidarStreamID(nome) != nil && !deveEscapar(b) {
			t.Errorf("o byte %#x e recusado pela regra mas o escape deixa-o passar cru:\n"+
				"um node_id com este byte produziria um childRunID que o Append recusa, e o run\n"+
				"do no nunca arrancaria", b)
		}
	}
}
