package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	state "github.com/aos-ref/kernel/agent-runtime/state"
)

// leRun faz GET /runs/{id} e devolve o codigo e o corpo desserializado.
func leRun(t *testing.T, h http.Handler, runID string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+runID, nil))
	var corpo map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &corpo)
	return rec.Code, corpo
}

// UM RUN PAUSADO SAI COMO `paused` PELOS DOIS CAMINHOS DE LEITURA.
//
// Achado C da verificacao de funcionamento de 2026-08-23, e a regressao e minha: no PR #139
// alarguei o `suspendedDurably` para contar `paused` (era o que fazia o `/resume` funcionar
// sobre uma pausa). Esse alargamento fez o ramo de "suspenso" do handleGet APANHAR os runs
// pausados e rotula-los `waiting_on_human` — com a lista de pendentes VAZIA, porque nunca
// houve pedido nenhum. O operador ficava a espera de uma decisao que ninguem lhe pediu.
//
// O handleGet TEM DOIS CAMINHOS, e os dois sao vivos:
//
//	(A) a replica LEMBRA o run (esta no balde `completed`) ⇒ `Suspended()` devolve false e a
//	    leitura cai no switch sobre o estado duravel. E o caso de um run parado pelo
//	    DISJUNTOR, que o `finish` arquiva em `completed`.
//	(B) a replica ESQUECEU o run (restart, poda FIFO) ⇒ `Suspended()` devolve true pelo log
//	    e a leitura sai pelo ramo de suspensao. Era aqui que a mentira saia.
//
// A PROPRIEDADE E A CONCORDANCIA: o MESMO run tem de se ler igual, saiba a replica dele ou
// nao. Enumerar um caminho so deixaria o outro livre para divergir — que e exactamente como
// este defeito nasceu.
func TestPausado_LeSeIgualPelosDoisCaminhos(t *testing.T) {
	const run = "run-pausado-dois-caminhos"
	_, svc, h := runPausado(t, run)

	// (A) A REPLICA LEMBRA: o `runPausado` deixa o run no balde `completed`.
	svc.mu.Lock()
	_, lembra := svc.completed[run]
	svc.mu.Unlock()
	if !lembra {
		t.Fatal("PRECONDICAO: o caminho (A) exige o run no balde `completed`")
	}
	if _, susp := svc.Suspended(context.Background(), run); susp {
		t.Fatal("PRECONDICAO: com o run em `completed` o Suspended devia dar false — o caminho (A) e o switch")
	}
	codigoA, corpoA := leRun(t, h, run)

	// (B) A REPLICA ESQUECE: e o que acontece a seguir a um restart ou a uma poda FIFO.
	svc.mu.Lock()
	delete(svc.completed, run)
	svc.mu.Unlock()
	if _, susp := svc.Suspended(context.Background(), run); !susp {
		t.Fatal("PRECONDICAO: sem o balde, o Suspended devia dar true pelo log — o caminho (B) e o ramo de suspensao")
	}
	codigoB, corpoB := leRun(t, h, run)

	for _, c := range []struct {
		nome   string
		codigo int
		corpo  map[string]any
	}{{"(A) a replica lembra", codigoA, corpoA}, {"(B) a replica esqueceu", codigoB, corpoB}} {
		if c.codigo != http.StatusOK {
			t.Fatalf("%s: HTTP %d, queria 200", c.nome, c.codigo)
		}
		if got := c.corpo["status"]; got != string(state.Paused) {
			t.Fatalf("%s: status=%v, queria %q — um run pausado nao esta a espera de decisao humana", c.nome, got, state.Paused)
		}
		if c.corpo["paused"] != true {
			t.Fatalf("%s: paused=%v, queria true", c.nome, c.corpo["paused"])
		}
		// NAO HA PENDENTES numa pausa: ninguem pediu decisao a ninguem. Uma lista vazia
		// debaixo de `waiting_on_human` parece um erro de leitura; debaixo de `paused` nao
		// tem sequer razao para aparecer.
		if _, tem := c.corpo["pending_approvals"]; tem {
			t.Fatalf("%s: a resposta traz pending_approvals numa PAUSA: %v", c.nome, c.corpo["pending_approvals"])
		}
	}
}

// CONTROLO ANTI-VACUIDADE: UM RUN A ESPERA DE HUMANO CONTINUA A SAIR COMO TAL.
//
// Sem este caso, uma correccao que rotulasse TUDO `paused` passaria o teste de cima. E o
// ramo de `waiting_on_human` e o que serve as duas decisoes humanas reais do no.
func TestPausado_ControloEsperaHumanaNaoFoiRotulada(t *testing.T) {
	const run = "run-espera-humana-controlo"
	node, svc, h, _ := aos263Node(t)
	aos263TornaRetomavel(t, node, svc, run)

	if _, susp := svc.Suspended(context.Background(), run); !susp {
		t.Fatal("PRECONDICAO: o run devia estar suspenso a espera de humano")
	}
	codigo, corpo := leRun(t, h, run)
	if codigo != http.StatusOK {
		t.Fatalf("HTTP %d, queria 200", codigo)
	}
	if got := corpo["status"]; got != "waiting_on_human" {
		t.Fatalf("CONTROLO: status=%v, queria waiting_on_human — a correccao rotulou de pausa uma espera REAL", got)
	}
	if corpo["paused"] == true {
		t.Fatal("CONTROLO: uma espera humana veio marcada como paused")
	}
}
