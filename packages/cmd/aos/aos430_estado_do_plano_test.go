package main

// aos430_estado_do_plano_test.go — AOS-430.
//
// Quatro propriedades, por ordem de importância:
//
//  1. a premissa do ticket — que `GET /runs/<topo>` dá 404 — é VERIFICADA EM EXECUÇÃO, não por
//     leitura de código, que é o que o ticket marcava como por fazer;
//  2. quem submeteu vê o desfecho do seu plano;
//  3. quem NÃO submeteu não distingue «não é teu» de «não existe» — o ADR-030 §2.1;
//  4. a rota encontra um plano TERMINADO, que é o que a marca de água do AOS-429 esconderia.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// ── (1) A PREMISSA DO TICKET, MEDIDA ───────────────────────────────────────────────────────

// TestAOS430ORunDeTopoNaoEServivelPeloReadPath fecha o critério que o AOS-430 declarava
// **NÃO VERIFICADO**: «a conclusão vem de leitura de código e dos volumes do compose».
//
// Mede-o. Submete um plano e pergunta pelas TRÊS rotas de leitura de run pelo `run_id` de topo.
// Se alguma delas servisse, a premissa do ticket estava errada e o desenho todo com ela.
func TestAOS430ORunDeTopoNaoEServivelPeloReadPath(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	const topo = "run-430-topo"
	if rec := postReq(h, "/plans", planRequest{RunID: topo, Objective: "objectivo"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	for _, rota := range []string{"/runs/" + topo, "/runs/" + topo + "/trajectory", "/runs/" + topo + "/reconstruct"} {
		rec := getReq(h, rota, euReaderHeaders())
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s devia dar 404 (o run de topo nunca e hospedado pelo no — so os filhos "+
				"`<topo>~<no>` sao submetidos), veio %d: %s", rota, rec.Code, rec.Body.String())
		}
	}
}

// ── (2) QUEM SUBMETEU VÊ ───────────────────────────────────────────────────────────────────

// TestAOS430QuemSubmeteuVeOEstado percorre o ciclo completo pela rota nova.
func TestAOS430QuemSubmeteuVeOEstado(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)
	const runID = "run-430-ciclo"

	if rec := postReq(h, "/plans", planRequest{RunID: runID, Objective: "objectivo"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d (%s)", rec.Code, rec.Body.String())
	}

	// PENDENTE — submetido, ninguém reclamou.
	est := estadoDoPlanoDeTeste(t, h, runID, euReaderHeaders())
	if est.Estado != EstadoPlanoPendente {
		t.Errorf("logo apos submeter o estado devia ser %q, veio %q", EstadoPlanoPendente, est.Estado)
	}
	if est.RunID != runID {
		t.Errorf("run_id = %q, quer %q", est.RunID, runID)
	}

	// EM CURSO — depois de reclamado.
	if rec := postReq(h, "/plans/claim", struct{}{}, euReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("POST /plans/claim devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	est = estadoDoPlanoDeTeste(t, h, runID, euReaderHeaders())
	if est.Estado != EstadoPlanoEmCurso {
		t.Errorf("depois de reclamado o estado devia ser %q, veio %q", EstadoPlanoEmCurso, est.Estado)
	}

	// TERMINADO — com o código de saída e o detalhe do consumidor.
	desf := pedidoDeDesfecho{RunID: runID, Geracao: est.Geracao, Classe: DesfechoTerminal, CodigoSaida: 7, Detalhe: "plano recusado pelo gate"}
	if rec := postReq(h, "/plans/outcome", desf, euReaderHeaders()); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /plans/outcome devia dar 204, veio %d (%s)", rec.Code, rec.Body.String())
	}
	est = estadoDoPlanoDeTeste(t, h, runID, euReaderHeaders())
	if est.Estado != EstadoPlanoTerminado {
		t.Errorf("estado = %q, quer %q", est.Estado, EstadoPlanoTerminado)
	}
	if est.CodigoSaida != 7 {
		t.Errorf("exit_code = %d, quer 7 — sem ele quem submeteu sabe que acabou e nao sabe como", est.CodigoSaida)
	}
	if est.Detalhe != "plano recusado pelo gate" {
		t.Errorf("detail = %q, quer o detalhe reportado pelo consumidor", est.Detalhe)
	}
}

// ── (3) A NÃO-ORACULARIDADE ────────────────────────────────────────────────────────────────

// TestAOS430NaoEeTeuEeNaoExisteSaoIndISTINGUIVEIS é o teste que mantém o ADR-030 §2.1 de pé.
//
// Compara as respostas BYTE-A-BYTE. Um teste que só comparasse os códigos deixaria passar um
// corpo diferente — e o corpo é um canal tão bom como o código para um oráculo.
func TestAOS430NaoEeTeuEeNaoExisteSaoIndISTINGUIVEIS(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	const meu = "run-430-do-outro"
	if rec := postReq(h, "/plans", planRequest{RunID: meu, Objective: "segredo"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d", rec.Code)
	}

	// O MESMO pedido, visto por OUTRO principal.
	doOutro := getReq(h, "/plans/"+meu, usReaderHeaders())
	// Um pedido que nunca existiu, visto pelo mesmo outro principal.
	inexistente := getReq(h, "/plans/run-430-nunca-existiu", usReaderHeaders())

	if doOutro.Code != http.StatusNotFound {
		t.Fatalf("um pedido de OUTRO principal tinha de dar 404, veio %d (%s)", doOutro.Code, doOutro.Body.String())
	}
	if doOutro.Code != inexistente.Code || doOutro.Body.String() != inexistente.Body.String() {
		t.Errorf("«nao e teu» e «nao existe» sao DISTINGUIVEIS, e isso e o oraculo que o ADR-030 "+
			"§2.1 fecha:\n  do outro:    %d %q\n  inexistente: %d %q",
			doOutro.Code, doOutro.Body.String(), inexistente.Code, inexistente.Body.String())
	}
}

// TestAOS430SemGateSoberanoRecusaCom501 — a mesma postura das rotas irmãs.
func TestAOS430SemGateSoberanoRecusaCom501(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	if rec := getReq(h, "/plans/run-430-qualquer", nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("sem gate soberano a rota devia dar 501 (nao 403): a diferenca entre «nao estas "+
			"autorizado» e «este no nao sabe autorizar ninguem» e diagnostica; veio %d", rec.Code)
	}
}

// ── (4) A ARMADILHA DA MARCA DE ÁGUA ───────────────────────────────────────────────────────

// TestAOS430EncontraUmPlanoAbaixoDaMarcaDeAgua é o teste que existe por causa de um defeito que
// eu próprio criei no AOS-429.
//
// A projecção da fila passou a ler A PARTIR da marca de água. Um pedido TERMINADO está, por
// definição, abaixo dela — e é precisamente o desfecho dele que quem submeteu vem procurar.
// Reutilizar o caminho quente devolveria «não existe» para todos os planos que acabaram.
//
// Aqui força-se a marca a avançar (submetendo e terminando um pedido, e depois chamando o
// caminho que a move) e exige-se que a rota continue a encontrá-lo.
func TestAOS430EncontraUmPlanoAbaixoDaMarcaDeAgua(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)
	const antigo = "run-430-antigo"

	if rec := postReq(h, "/plans", planRequest{RunID: antigo, Objective: "o primeiro"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d", rec.Code)
	}
	rec := postReq(h, "/plans/claim", struct{}{}, euReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: %d", rec.Code)
	}
	var reclamado respostaDeReclamo
	if err := json.Unmarshal(rec.Body.Bytes(), &reclamado); err != nil {
		t.Fatalf("resposta do claim: %v", err)
	}
	desf := pedidoDeDesfecho{RunID: antigo, Geracao: reclamado.Geracao, Classe: DesfechoTerminal, CodigoSaida: 0}
	if rec := postReq(h, "/plans/outcome", desf, euReaderHeaders()); rec.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d", rec.Code)
	}

	// A ARMADILHA, DEMONSTRADA: a projecção da fila avança a marca sobre este pedido, e a
	// partir daí o caminho quente deixa de o ver. É um controlo de NÃO-VACUIDADE — sem ele,
	// este teste passaria mesmo que a marca nunca avançasse e não estaria a medir nada.
	eventos, err := node.EventStore.Read(context.Background(), planRequestStream, 0)
	if err != nil {
		t.Fatalf("ler a fila: %v", err)
	}
	_, marca := projectarFilaComMarca(eventos, time.Now().UTC())
	if marca == 0 {
		t.Fatal("a marca de agua nao avancou sobre um pedido TERMINADO — este teste deixou de " +
			"medir a armadilha que diz medir")
	}
	acimaDaMarca := make([]eventstore.Event, 0, len(eventos))
	for _, ev := range eventos {
		if ev.Seq > marca {
			acimaDaMarca = append(acimaDaMarca, ev)
		}
	}
	for _, p := range projectarFila(acimaDaMarca, time.Now().UTC()) {
		if p.RunID == antigo {
			t.Fatal("o caminho quente ainda ve o pedido terminado — a marca nao o cobriu, e a " +
				"armadilha que este teste existe para provar nao esta presente")
		}
	}

	// E A ROTA TEM DE O ENCONTRAR NA MESMA — é este o ponto.
	est := estadoDoPlanoDeTeste(t, h, antigo, euReaderHeaders())
	if est.Estado != EstadoPlanoTerminado {
		t.Errorf("um plano TERMINADO, abaixo da marca de agua, tinha de continuar legivel; veio %q\n"+
			"se a rota reutilizasse o caminho quente, todos os planos que acabaram diriam «nao existe»",
			est.Estado)
	}
}

// TestAOS430RunIDReservadoERecusadoNoDesfecho fecha a assimetria que a discovery do AOS-430
// encontrou: o `POST /plans` recusava um `run_id` reservado e o `POST /plans/outcome` não.
//
// A barra É representável (decisão do AOS-424), logo o `runIDInvalido` não a apanhava.
func TestAOS430RunIDReservadoERecusadoNoDesfecho(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	reservado := streamsReservados + "plan-requests"
	if runIDInvalido(reservado) {
		t.Fatalf("premissa do teste caiu: %q passou a ser invalido por representabilidade, "+
			"logo este teste deixou de medir a guarda do RESERVADO", reservado)
	}
	desf := pedidoDeDesfecho{RunID: reservado, Geracao: 1, Classe: DesfechoTerminal}
	if rec := postReq(h, "/plans/outcome", desf, euReaderHeaders()); rec.Code != http.StatusBadRequest {
		t.Errorf("um desfecho para um run_id RESERVADO devia dar 400, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// estadoDoPlanoDeTeste chama a rota e descodifica, falhando o teste se não vier 200.
func estadoDoPlanoDeTeste(t *testing.T, h http.Handler, runID string, hdr map[string]string) respostaDeEstadoDoPlano {
	t.Helper()
	rec := getReq(h, "/plans/"+runID, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plans/%s devia dar 200, veio %d (%s)", runID, rec.Code, rec.Body.String())
	}
	var est respostaDeEstadoDoPlano
	if err := json.Unmarshal(rec.Body.Bytes(), &est); err != nil {
		t.Fatalf("resposta nao descodifica: %v", err)
	}
	return est
}
