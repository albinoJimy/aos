package main

// aos417_soberania_test.go — O INGRESSO DO PLANO NÃO É UMA PORTA LATERAL PARA A SOBERANIA.
//
// Estes testes nasceram de uma revisão adversarial que encontrou DOIS defeitos CRÍTICOS na
// primeira versão da rota, ambos com a mesma raiz: **a fila foi posta no espaço de nomes dos
// runs, e a rota copiou do `POST /runs` um passo cuja razão de ser ela não tem.**
//
// O que a primeira versão fazia de errado, escrito para não se repetir:
//
//  1. o stream da fila chamava-se `plan.requests`, e o read-path de trajectória endereça
//     streams POR run_id — pelo que `GET /runs/plan.requests/trajectory` servia a fila inteira,
//     ao vivo, a um leitor de QUALQUER região (a fila não tem residência selada, logo caía no
//     ramo «run legado, sem check cross-region»). Os objectivos de todos os tenants, legíveis;
//  2. a rota chamava `sealResidency`, que é PRÉ-CONDIÇÃO DA HOSPEDAGEM de um run. Como esta rota
//     não hospeda nada, o run_id ficava LIVRE e a residência ficava FIXA — e quem selasse
//     primeiro fixava a fronteira de soberania de um run que outra pessoa viria a criar. A
//     vítima corria o run e não o conseguia ler; o atacante lia-o.
//
// A segunda é pior do que o squat que já era possível pelo `POST /runs`: ali o atacante tem de
// hospedar o run e o id fica ocupado (é negação de serviço); aqui o run da vítima CORRE e o
// conteúdo dele fica legível pela região do atacante (é exfiltração).

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A FILA NÃO É LEGÍVEL PELO READ-PATH DOS RUNS.
//
// A asserção é sobre o ESPAÇO DE NOMES, e não sobre o valor da constante: o que tem de ser
// verdade é que nenhum `run_id` aceitável pelas rotas de run consegue nomear o stream da fila.
// Testar o nome literal amarraria o teste à escolha de hoje; testar a propriedade sobrevive a
// mudá-la.
func TestAOS417FilaForaDoEspacoDeNomesDosRuns(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/plans", map[string]any{
		"run_id":    "plan-req-namespace",
		"objective": "SEGREDO-que-nao-pode-sair-pela-trajectoria",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// (a) O nome do stream da fila tem de ser RECUSADO como run_id — senão as rotas de run
	// alcançam-no. É a guarda que fecha a leitura E a injecção de eventos de run na fila.
	if !runIDReservado(planRequestStream) {
		t.Fatalf("o stream da fila (%q) tem de ser um run_id RESERVADO: enquanto for aceitavel "+
			"como run_id, GET /runs/<stream>/trajectory serve a fila inteira", planRequestStream)
	}

	// (b) E a recusa é EFECTIVA na fronteira, não só uma função que ninguém chama.
	for _, rota := range []string{"/runs", "/plans"} {
		sub := postJSON(h, "POST", rota, map[string]any{
			"run_id":    planRequestStream,
			"objective": "injeccao no stream da fila",
		})
		if sub.Code != http.StatusBadRequest {
			t.Fatalf("POST %s com run_id no prefixo reservado devia dar 400, veio %d (%s)",
				rota, sub.Code, sub.Body.String())
		}
	}

	// (c) E a trajectória da fila NÃO serve. Sem posse nem residência, um 200 aqui seria a
	// fila inteira num SSE.
	traj := postJSON(h, "GET", "/runs/"+planRequestStream+"/trajectory", nil)
	if traj.Code == http.StatusOK {
		t.Fatalf("GET da trajectoria do stream da fila devia ser recusado, veio 200 (%s)",
			traj.Body.String())
	}
	if strings.Contains(traj.Body.String(), "SEGREDO") {
		t.Fatalf("o corpo da recusa nao pode conter o objectivo de um pedido: %s", traj.Body.String())
	}
}

// O INGRESSO NÃO REIVINDICA A RESIDÊNCIA DE UM RUN QUE NÃO CRIA.
//
// Prova pelo COMPORTAMENTO observável, e não pela ausência da chamada: o que tem de ser verdade
// é que quem cria o run continua a poder lê-lo. Amarrar o teste a «não chama sealResidency»
// deixaria passar qualquer outra forma de fixar a fronteira.
func TestAOS417IngressoNaoReivindicaResidencia(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	const runID = "run-417-squat"

	// (1) A região US pede um PLANO para um run_id que ainda não existe.
	rec := postReq(h, "/plans", planRequest{RunID: runID, Objective: "squat"}, usReaderHeaders())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans (US) devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// (2) A residência do run NÃO pode ter sido fixada: nenhum run foi criado.
	if head, _ := node.WORM.Head(context.Background(), readResidencyPartition(runID)); head != 0 {
		t.Fatalf("um pedido de plano NAO pode selar a residencia de um run que nao cria "+
			"(head=%d). Quem sela primeiro fixa a fronteira de soberania de um run alheio, e a "+
			"residencia nao e re-negociavel", head)
	}

	// (3) A região EU cria REALMENTE o run com o mesmo id — e sela a residência, como sempre.
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())

	// (4) O DONO lê o seu próprio run. É esta a asserção que importa: com o selo do ingresso,
	// este GET dava 404 e o run corria para ser lido pela outra região.
	dono := getReq(h, "/runs/"+runID, euReaderHeaders())
	if dono.Code != http.StatusOK {
		t.Fatalf("o submissor EU tem de conseguir ler o run que criou, veio %d (%s)",
			dono.Code, dono.Body.String())
	}

	// (5) E a região que só pediu o plano NÃO o lê. A fronteira continua a existir — o que se
	// corrigiu foi quem a fixa, não que ela exista.
	intruso := getReq(h, "/runs/"+runID, usReaderHeaders())
	if intruso.Code == http.StatusOK {
		t.Fatalf("a regiao que apenas pediu o plano nao pode ler o run, veio 200 (%s)",
			intruso.Body.String())
	}
}

// SOBERANIA COMPOSTA ⇒ A ROTA AUTENTICA. O sensor que faltava: os outros testes do AOS-417
// correm com `readGov == nil` (o nó de teste não compõe WORM nem regiões), pelo que o bloco
// inteiro de soberania podia ser apagado sem uma única falha. Aqui ele é exercitado.
func TestAOS417IngressoExigeCredencialQuandoSoberaniaComposta(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	// Sem credencial ⇒ 403. Um ingresso que aceitasse anónimos em produção seria uma porta
	// lateral para o caminho do plano.
	anon := postReq(h, "/plans", planRequest{RunID: "run-417-anon", Objective: "sem credencial"}, nil)
	if anon.Code != http.StatusForbidden {
		t.Fatalf("POST /plans sem credencial devia dar 403, veio %d (%s)", anon.Code, anon.Body.String())
	}

	// Com credencial ⇒ 201, e o facto guarda o principal e a região VERIFICADOS, nunca os do
	// corpo. É o que o consumidor vai usar para decidir onde o plano pode correr.
	ok := postReq(h, "/plans", planRequest{RunID: "run-417-auth", Objective: "com credencial"}, euReaderHeaders())
	if ok.Code != http.StatusCreated {
		t.Fatalf("POST /plans com credencial devia dar 201, veio %d (%s)", ok.Code, ok.Body.String())
	}
	fila := lerFilaDePedidos(t, node)
	if len(fila) != 1 {
		t.Fatalf("devia haver 1 pedido na fila, ha %d", len(fila))
	}
	if fila[0].Principal == "" {
		t.Fatalf("o facto tem de guardar o principal resolvido da credencial, veio vazio")
	}
	if fila[0].Region != govRegion {
		t.Fatalf("o facto tem de guardar a regiao resolvida da credencial (%q), veio %q",
			govRegion, fila[0].Region)
	}
}
