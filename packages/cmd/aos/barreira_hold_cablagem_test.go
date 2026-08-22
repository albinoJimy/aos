package main

import (
	"net/http"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// A ROTA DO HOLD TOMA MESMO A BARREIRA — E É A DÉCIMA PRIMEIRA VEZ QUE ESTE PADRÃO APARECE.
//
// A barreira de destruição é exercida no pacote que a implementa
// (`platform/audit/barreira_destruicao_test.go`), com um sink bloqueante e canais. Isso prova que
// a PRIMITIVA funciona — e não prova nada sobre esta rota. A rota podia continuar a selar e a
// aplicar exactamente como antes, com a barreira a existir e ninguém a chamá-la.
//
// A construção é a inversa da do outro teste, e é o que a torna barata: em vez de prender uma
// destruição e ver se o hold espera, o TESTE segura a barreira em modo exclusivo e vê se o
// handler espera. Se o handler não a tomar, responde de imediato e o teste cai.
// ---------------------------------------------------------------------------------------------

func TestARotaDoHoldTOMAABarreiraDeDestruicao(t *testing.T) {
	node := newRetentionNode(t)
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	// CONTROLO DO CENÁRIO: sem legal hold composto a rota responde 501 e tudo o que se segue
	// mediria o vazio.
	if node.DSARHolds == nil {
		t.Fatal("o legal hold nao esta composto — a rota responderia 501 e este teste nao provaria nada")
	}

	// O TESTE segura a barreira, como se uma destruição estivesse em voo.
	fim := node.DSARHolds.BeginPlacement()

	respondeu := make(chan int, 1)
	go func() {
		rec := postReq(h, "/dsar/hold", map[string]any{
			"request_id": "req-barreira-1",
			"subject_id": "nhi-titular-barreira",
		}, govHeaders())
		respondeu <- rec.Code
	}()

	select {
	case code := <-respondeu:
		fim()
		t.Fatalf("a rota respondeu %d com a barreira de destruicao SEGURA — nao a toma, e um 200 "+
			"dela nao significa que o hold esteja em vigor para o que se esta a destruir agora", code)
	case <-time.After(250 * time.Millisecond):
		// Espera-se este ramo.
	}

	fim()

	// CONTROLO: largada a barreira, a rota responde. Sem este ramo, um handler que bloqueasse
	// para sempre passaria no teste acima.
	select {
	case code := <-respondeu:
		if code != http.StatusOK {
			t.Fatalf("/dsar/hold devolveu %d depois de a barreira ser largada", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a rota NUNCA respondeu depois de a barreira ser largada — o handler nao a larga")
	}
	if !node.DSARHolds.HeldSubject("nhi-titular-barreira") {
		t.Error("a rota respondeu 200 e o hold NAO ficou em vigor")
	}
}
