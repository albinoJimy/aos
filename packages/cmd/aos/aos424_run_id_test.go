package main

// aos424_run_id_test.go — O `run_id` É O NOME DE UM STREAM, E NEM TODO O TEXTO O PODE SER.
//
// Até ao AOS-424 as duas rotas de submissão validavam o `run_id` contra duas coisas: não ser
// vazio e não invadir o prefixo reservado. Tudo o resto passava — e o `run_id` de um run É o seu
// stream no Event Store, pelo que um cliente escolhia livremente o nome de um stream.
//
// O efeito medido: um `run_id` com ponto é aceite sobre WAL e recusado com `E_CONFIG` sobre
// JetStream. Funciona em desenvolvimento e parte na única topologia que arbitra entre processos
// (DEF-282) — que é a que o caminho do plano precisa para ter consumidor.

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// UM `run_id` QUE NÃO PODE SER UM STREAM É RECUSADO NO `POST /plans`.
//
// A recusa é `400` e não `503`: é o pedido que está mal, não o nó que está em baixo.
//
// SÓ NESTA ROTA, e o teste abaixo fixa a assimetria em vez de a deixar por explicar — ver
// [TestAOS424PostRunsAindaNaoValidaEPorque].
func TestAOS424RunIDInvalidoERecusadoNoIngressoDoPlano(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	maus := []struct{ nome, id string }{
		{"ponto", "cliente.pedido-1"},
		{"espaco", "run com espaco"},
		{"curinga asterisco", "run-*"},
		{"curinga maior", "run->tudo"},
		{"tab", "run\tcom-tab"},
		{"nova linha", "run\ncom-linha"},
	}
	for _, m := range maus {
		t.Run(m.nome, func(t *testing.T) {
			plans := postJSON(h, "POST", "/plans", map[string]any{
				"run_id": m.id, "objective": "objectivo",
			})
			if plans.Code != http.StatusBadRequest {
				t.Errorf("POST /plans com run_id %q devia dar 400, veio %d (%s)",
					m.id, plans.Code, plans.Body.String())
			}
		})
	}
}

// CONTROLO DE NÃO-VACUIDADE. Sem isto, uma guarda que recusasse tudo passaria no teste acima.
//
// Inclui de propósito os caracteres que um `run_id` legítimo USA e que o subject NATS aceita —
// o hífen, o sublinhado e a barra — para que apertar a regra por engano fique vermelho.
func TestAOS424RunIDLegitimoContinuaAceite(t *testing.T) {
	bons := []string{"run-424-simples", "run_com_sublinhado", "run-424/sub", "RUN424", "r1"}
	for _, id := range bons {
		if runIDInvalido(id) {
			t.Errorf("o run_id legitimo %q foi recusado — a regra apertou demais", id)
		}
	}

	// E a via HTTP, ponta a ponta, para o caso mais simples.
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": "run-424-simples", "principal_nhi": "nhi:run-424-simples",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("um run_id legitimo devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// A REGRA DUPLICADA NÃO PODE DERIVAR DA FONTE.
//
// O nó não pode importar o backend JetStream para lhe perguntar a regra — um `import` só para
// isto arrastaria o cliente NATS para o caminho de ingresso —, pelo que
// [caracteresProibidosNoStream] é uma CÓPIA. Uma cópia sem detector apodrece; este teste é o
// detector, e lê o original em vez de o repetir.
func TestAOS424RegraDuplicadaCoincideComAFonte(t *testing.T) {
	const fonte = "../../substrate/eventstore/jetstream/store.go"
	bruto, err := os.ReadFile(fonte)
	if err != nil {
		t.Fatalf("ler %s: %v — sem a fonte este teste nao tem o que comparar", fonte, err)
	}
	m := regexp.MustCompile(`ContainsAny\(streamID, "([^"]*)"\)`).FindSubmatch(bruto)
	if m == nil {
		t.Fatalf("nao encontrei a regra em %s: a guarda do subject NATS mudou de forma.\n"+
			"Actualize este teste para ler a regra nova — NAO o relaxe.", fonte)
	}
	daFonte := strings.NewReplacer(`\t`, "\t", `\r`, "\r", `\n`, "\n").Replace(string(m[1]))

	// CONTROLO: a regra lida tem de apanhar um valor conhecidamente mau. Se a extracção
	// partir, o teste falha em vez de comparar duas coisas vazias.
	if !strings.ContainsAny("a.b", daFonte) {
		t.Fatalf("a regra lida (%q) nao apanha um valor com ponto — a extraccao esta errada", daFonte)
	}

	if ordenar(daFonte) != ordenar(caracteresProibidosNoStream) {
		t.Errorf("a regra do no (%q) DIVERGIU da fonte (%q):\n"+
			"um run_id que o no aceite e o subject NATS recuse volta a ser aceite sobre WAL e a "+
			"partir sobre JetStream — que e exactamente o defeito que o AOS-424 fechou.",
			caracteresProibidosNoStream, daFonte)
	}
}

// ordenar normaliza um conjunto de caracteres para comparação independente da ordem.
func ordenar(s string) string {
	r := []rune(s)
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j] < r[j-1]; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
	return string(r)
}

// A ASSIMETRIA FICA FIXADA POR TESTE, para que seja uma DECISÃO e não um esquecimento.
//
// O `POST /runs` ainda aceita um `run_id` com ponto, e tem de continuar a aceitar enquanto o
// `plan.ValidNodeID` admitir `.` e `:` num `node_id`: o `childRunID` compõe `<run>~<node_id>` e
// submete-o por esta rota, pelo que apertar aqui partiria o caminho do plano em produção — que
// corre sobre WAL, onde estes ids funcionam.
//
// Quando o `ValidNodeID` for apertado (eixo registado no AOS-424), este teste fica VERMELHO e
// obriga a ligar a guarda à outra rota. É esse o momento em que a assimetria deixa de ser
// necessária, e é exactamente aí que alguém tem de ser avisado.
func TestAOS424PostRunsAindaNaoValidaEPorque(t *testing.T) {
	no, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = no.Close() }()
	_, h := newAPI(t, no)

	// O id que um nó de plano chamado `analise.dados` produziria.
	const filho = "run-424~analise.dados"
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": filho, "principal_nhi": "nhi:x",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("o `POST /runs` tem de continuar a aceitar %q (veio %d): é o id que o "+
			"childRunID compõe para um nó de plano com ponto, e o ValidNodeID permite pontos. "+
			"Se o ValidNodeID foi apertado, ligue [runIDInvalido] também ao POST /runs e "+
			"actualize este teste.", filho, rec.Code)
	}
}
