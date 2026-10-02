package main

// AOS-484 — o código de saída 13 do `aos-orq` (o plano chegou ao fim com nós falhados) do lado de
// quem o RECEBE.
//
// O nó não conhece os códigos do `serve` (ADR-018; o único que conhece é o 12, e só como protocolo
// da geração marcada — AOS-467). Por isso não há tabela nenhuma a actualizar aqui. Mas «não há
// tabela» é uma afirmação sobre código que alguém pode mudar: este teste segue o valor até ao fim —
// o `POST /plans/outcome` aceita o desfecho, o pedido FECHA (não volta à fila), e o
// `GET /plans/<id>` devolve a quem submeteu `terminal` com `exit_code` 13 e o detalhe de nome
// estável. Sem isto, um plano com nós falhados continuaria a ser indistinguível, para quem o
// submeteu, de um que correu.

import (
	"net/http"
	"testing"
)

// O detalhe que o `aos-orq` reporta num plano com nós falhados: o resumo, com o TIPO do erro.
const aos484ResumoComNosFalhados = "resumo: origem=decomposicao geracao=1 nos=2 duracao_s=43.900 erro=nos_falhados"

func TestAOS484TerminalComNosFalhadosChegaAQuemSubmeteu(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)
	const runID = "run-484-nos-falhados"

	if rec := postReq(h, "/plans", planRequest{RunID: runID, Objective: "objectivo"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postReq(h, "/plans/claim", struct{}{}, euReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("POST /plans/claim: %d (%s)", rec.Code, rec.Body.String())
	}
	desf := pedidoDeDesfecho{RunID: runID, Geracao: 1, Classe: DesfechoTerminal, CodigoSaida: 13, Detalhe: aos484ResumoComNosFalhados}
	if rec := postReq(h, "/plans/outcome", desf, euReaderHeaders()); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /plans/outcome com terminal/13 devia dar 204, veio %d (%s) — o desfecho nunca seria "+
			"registado e o pedido ficava preso até ao TTL da reclamação", rec.Code, rec.Body.String())
	}

	est := estadoDoPlanoDeTeste(t, h, runID, euReaderHeaders())
	if est.Estado != EstadoPlanoTerminado {
		t.Fatalf("estado = %q, quer %q", est.Estado, EstadoPlanoTerminado)
	}
	if est.CodigoSaida != 13 {
		t.Fatalf("exit_code = %d, quer 13 — com 0 quem submeteu lia um plano bem-sucedido", est.CodigoSaida)
	}
	if est.Detalhe != aos484ResumoComNosFalhados {
		t.Fatalf("detail = %q, quer o resumo com o tipo do erro", est.Detalhe)
	}

	// TERMINAL fecha o pedido: não é re-oferecido (um transitório voltaria à fila e re-planeava).
	if rec := postReq(h, "/plans/claim", struct{}{}, euReaderHeaders()); rec.Code == http.StatusOK {
		t.Fatalf("um pedido fechado com terminal/13 foi re-oferecido: %s", rec.Body.String())
	}
}
