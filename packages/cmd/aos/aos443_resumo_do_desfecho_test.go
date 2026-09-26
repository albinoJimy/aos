package main

// aos443_resumo_do_desfecho_test.go — AOS-443, do lado do nó.
//
// O `aos-orq` passou a reportar um resumo no `detalhe` também em SUCESSO. O nó não muda: o
// `GET /plans/{id}` já servia o `detail` do desfecho terminal sempre que ele não era vazio
// (`omitempty`), e o contrato do ADR-031 §2.3 — `{run_id, status, generation, exit_code, detail}` —
// fica igual. O que estes testes fixam é que isso é VERDADE para o terminal/0, que até aqui nunca
// tinha levado detalhe, e que o truncamento do nó não come o resumo.

import (
	"net/http"
	"strings"
	"testing"
)

// O resumo, escrito à mão na forma que o `aos-orq` produz (`detalheDoDesfecho` em
// packages/cmd/aos-orq/metricas_do_consumo.go). São binários distintos: derivá-lo de uma constante
// partilhada não provaria que as duas pontas concordam.
const aos443ResumoEmSucesso = "resumo: origem=documento geracao=2 nos=3 duracao_s=41.207"

func TestAOS443TerminadoComSucessoMostraOResumo(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)
	const runID = "run-443-sucesso"

	if rec := postReq(h, "/plans", planRequest{RunID: runID, Objective: "objectivo"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postReq(h, "/plans/claim", struct{}{}, euReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("POST /plans/claim: %d (%s)", rec.Code, rec.Body.String())
	}
	desf := pedidoDeDesfecho{RunID: runID, Geracao: 1, Classe: DesfechoTerminal, CodigoSaida: 0, Detalhe: aos443ResumoEmSucesso}
	if rec := postReq(h, "/plans/outcome", desf, euReaderHeaders()); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /plans/outcome: %d (%s)", rec.Code, rec.Body.String())
	}

	est := estadoDoPlanoDeTeste(t, h, runID, euReaderHeaders())
	if est.Estado != EstadoPlanoTerminado || est.CodigoSaida != 0 {
		t.Fatalf("estado %q exit_code %d, quer terminal/0", est.Estado, est.CodigoSaida)
	}
	if est.Detalhe != aos443ResumoEmSucesso {
		t.Fatalf("detail de um terminal/0 = %q, quer o resumo reportado — sem ele «terminado com "+
			"sucesso» e «terminado» dizem o mesmo", est.Detalhe)
	}
}

// O `truncar` do nó corta aos 512 bytes. O resumo não leva texto livre (o erro vai só como TIPO,
// de vocabulário fechado), e mesmo com os campos no máximo cabe inteiro.
func TestAOS443TruncamentoDoNoGuardaOResumo(t *testing.T) {
	detalhe := "resumo: origem=reverificacao geracao=2147483647 nos=2147483647 duracao_s=9999999.999 erro=snapshot_diferente_do_selado"
	if gravado := truncar(detalhe, 512); gravado != detalhe || strings.Contains(gravado, "|") {
		t.Fatalf("o truncamento mexeu no resumo: %q", gravado)
	}
}
