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
	"os"
	"regexp"
	"strings"
	"testing"
)

// O teste LÊ A REGRA DA FONTE em vez de a repetir.
//
// Duplicar o conjunto de caracteres aqui daria um teste que fica verde no dia em que a regra do
// NATS mudar e este nome deixar de ser válido — que é precisamente o modo de falha que ele
// existe para apanhar. Ler o literal do `subjectDe` amarra os dois lados: se a regra apertar, o
// teste aperta com ela.
//
// Precedente da técnica no mesmo ticket: `aos417_banner_test.go` lê o `bootstrap.go` para
// detectar um literal que apodrece.
func TestAOS417NomeDoStreamERepresentavelNoNATS(t *testing.T) {
	const fonte = "../../substrate/eventstore/jetstream/store.go"
	bruto, err := os.ReadFile(fonte)
	if err != nil {
		t.Fatalf("ler %s: %v — sem a fonte da regra este teste nao tem o que impor", fonte, err)
	}

	// Extrai o conjunto de caracteres que `subjectDe` recusa, do próprio código.
	re := regexp.MustCompile(`ContainsAny\(streamID, "([^"]*)"\)`)
	m := re.FindSubmatch(bruto)
	if m == nil {
		t.Fatalf("nao encontrei a regra `ContainsAny(streamID, ...)` em %s:\n"+
			"a guarda do subject NATS mudou de forma. Actualize este teste para ler a regra nova "+
			"— NAO o relaxe, porque o que ele impede e uma rota que responde 503 a tudo no unico "+
			"substrato que arbitra entre processos.", fonte)
	}
	// O literal Go traz escapes (`\t`, `\r`, `\n`) que têm de ser desfeitos para comparar.
	proibidos := strings.NewReplacer(`\t`, "\t", `\r`, "\r", `\n`, "\n").Replace(string(m[1]))
	if proibidos == "" {
		t.Fatal("o conjunto de caracteres proibidos veio vazio — a leitura da regra falhou")
	}

	// CONTROLO DE NÃO-VACUIDADE: o nome ANTIGO tem de ser apanhado por esta mesma verificação.
	// Sem isto, um bug na extracção da regra deixaria o teste verde a medir nada.
	const nomeAntigo = "aos.internal/plan-requests"
	if !strings.ContainsAny(nomeAntigo, proibidos) {
		t.Fatalf("a regra lida de %s (%q) NAO apanha o nome antigo %q — a extracao esta errada "+
			"e este teste nao esta a medir nada", fonte, proibidos, nomeAntigo)
	}

	// E agora o que importa: os nomes EM USO.
	for _, nome := range []string{streamsReservados, planRequestStream, planRequestRunID} {
		if i := strings.IndexAny(nome, proibidos); i >= 0 {
			t.Errorf("%q contem o caracter %q, que o subject NATS nao representa (%s):\n"+
				"sobre JetStream o Append recusa com E_CONFIG e o POST /plans responde 503 a "+
				"TODO o pedido. E o JetStream e o unico substrato que arbitra entre processos "+
				"(DEF-282), logo o unico onde um consumidor da fila pode existir.",
				nome, nome[i], fonte)
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
