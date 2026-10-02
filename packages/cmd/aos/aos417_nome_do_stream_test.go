package main

// aos417_nome_do_stream_test.go — O NOME DO STREAM DA FILA TEM DE SER REPRESENTÁVEL NO SUBSTRATO
// QUE ARBITRA.
//
// A primeira versão do ingresso chamou à fila `aos.internal/plan-requests`. O ponto foi escolhido
// por hábito de nomear famílias de eventos, e tornou a rota INUTILIZÁVEL sobre JetStream: o
// `stream_id` do AOS é livre, mas um subject NATS não é — o ponto separa tokens, e o
// `jetstream.Store.subjectDe` RECUSA qualquer `stream_id` que o contenha, em vez de escapar em
// silêncio para um subject vizinho onde outro stream leria os nossos eventos. O `Append`
// chama-o antes de tudo, pelo que o `POST /plans` respondia `503` a todo o pedido num nó
// replicado.
//
// O QUE TORNA ISTO MAIS GRAVE DO QUE UM ERRO DE DIGITAÇÃO, e é a razão de este teste existir:
// o substrato de ficheiro NÃO arbitra entre processos (DEF-282) e o JetStream é o único que
// arbitra. Ou seja, o único substrato onde um consumidor da fila pode sequer existir era
// exactamente aquele onde o ingresso não gravava — e o defeito só apareceria a quem tentasse
// escrever o consumidor, muito depois de o ingresso ter sido dado por feito.
//
// O defeito passou por dez gates verdes, uma revisão adversarial e um smoke de dez passos,
// porque TUDO isso corre sobre o substrato de ficheiro.

import (
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// O teste CHAMA A REGRA em vez de a repetir — e em vez de a extrair da fonte por regex.
//
// # PORQUE É QUE ISTO MUDOU DUAS VEZES
//
// A primeira versão repetia a lista de caracteres. A segunda extraía-a do
// `ContainsAny(streamID, …)` do `jetstream/store.go` por expressão regular — melhor, porque
// uma regra lida não deriva, mas ainda um parser a espreitar para dentro de outro pacote.
//
// O aperto do contrato (AOS-424, decisão 1) concentrou a regra em
// [eventstore.ValidarStreamID], que é exportada e que este pacote já importa. Agora
// chama-se. Não há cópia para derivar nem extracção para partir.
//
// Quando a mudança foi feita, este teste ficou VERMELHO com «nao encontrei a regra» — falhou
// fechado, que é exactamente o que se lhe pedia.
func TestAOS417NomeDoStreamERepresentavelNoNATS(t *testing.T) {
	// CONTROLO DE NÃO-VACUIDADE: a regra tem de apanhar o nome ANTIGO, o que o AOS-417 usou e
	// que tornava a rota inutilizável sobre JetStream. Sem isto, uma regra que aceitasse tudo
	// deixaria o bloco seguinte a medir nada.
	const nomeAntigo = "aos.internal/plan-requests"
	if eventstore.ValidarStreamID(nomeAntigo) == nil {
		t.Fatalf("a regra aceita %q, que o subject NATS nao representa: ela deixou de restringir "+
			"e este teste nao esta a medir nada", nomeAntigo)
	}

	// E os nomes EM USO.
	for _, nome := range []string{streamsReservados, planRequestStream, planRequestRunID} {
		if err := eventstore.ValidarStreamID(nome); err != nil {
			t.Errorf("%q nao e representavel (%v):\n"+
				"sobre JetStream o Append recusa com E_CONFIG e o POST /plans responde 503 a TODO "+
				"o pedido. E o JetStream e o unico substrato que arbitra entre processos "+
				"(DEF-282), logo o unico onde um consumidor da fila pode existir.", nome, err)
		}
	}
}

// A BARRA TEM DE SOBREVIVER. É ela que mantém a fila fora do alcance de `GET /runs/{id}/...`,
// porque o padrão da stdlib casa `{id}` com UM só segmento de caminho. Trocar a barra por outra
// coisa ao corrigir o ponto reabriria a leitura da fila pela rota de trajectória — que foi um
// dos dois defeitos críticos que a revisão adversarial do AOS-417 encontrou.
func TestAOS417PrefixoReservadoMantemABarra(t *testing.T) {
	if !strings.Contains(streamsReservados, "/") {
		t.Fatalf("o prefixo reservado (%q) perdeu a barra: sem ela o nome volta a ser um "+
			"`run_id` de um so segmento e GET /runs/<stream>/trajectory alcanca a fila",
			streamsReservados)
	}
	// E continua a ser recusado como `run_id` nas duas portas — a propriedade que a barra serve.
	if !runIDReservado(planRequestStream) {
		t.Fatalf("o stream da fila (%q) deixou de ser um run_id reservado", planRequestStream)
	}
}
