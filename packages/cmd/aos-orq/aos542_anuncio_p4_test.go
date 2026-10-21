package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// AOS-542: o `aos-orq` lê `plan_input_deferral.first_turn_trusted` do `GET /tools` do nó e só
// com ele, e com o snapshot conferido, relaxa a regra P4 do validador.

func aos542Anuncio(t *testing.T, corpo map[string]any) anuncioDoNo {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(corpo)
	}))
	defer srv.Close()
	a, err := (&nodeClient{base: srv.URL, http: srv.Client()}).ContratoDeConclusao(context.Background())
	if err != nil {
		t.Fatalf("anuncio: %v", err)
	}
	return a
}

func TestAOS542_AnuncioLidoDoNo(t *testing.T) {
	com := aos542Anuncio(t, map[string]any{"tools": []any{}, "plan_input_deferral": map[string]bool{"first_turn_trusted": true}})
	if !com.primeiroTurnoConfiavel {
		t.Fatal("o anuncio first_turn_trusted nao foi lido")
	}
	for nome, corpo := range map[string]map[string]any{
		"sem o campo":   {"tools": []any{}},
		"campo a falso": {"tools": []any{}, "plan_input_deferral": map[string]bool{"first_turn_trusted": false}},
		"objecto vazio": {"tools": []any{}, "plan_input_deferral": map[string]any{}},
		"outro anuncio": {"tools": []any{}, "run_retry": map[string]any{"max": 2}},
	} {
		if aos542Anuncio(t, corpo).primeiroTurnoConfiavel {
			t.Errorf("%s: relaxou a P4 sem o anuncio", nome)
		}
	}
}

func TestAOS542_SoComSnapshotConferido(t *testing.T) {
	com := anuncioDoNo{primeiroTurnoConfiavel: true}
	conferido := aplicarAnuncioDaP4(snapshotConferido{snap: planvalidate.Snapshot{Hash: "h"}, ok: true}, com)
	if !conferido.snap.ConsumerFirstTurnTrusted || !strings.Contains(bannerDaP4(com, true), "RELAXADA") {
		t.Fatal("com o anuncio e o snapshot conferido a P4 tinha de ficar relaxada")
	}
	if c := aplicarAnuncioDaP4(snapshotConferido{snap: planvalidate.Snapshot{Hash: "h"}}, com); c.snap.ConsumerFirstTurnTrusted || !strings.Contains(bannerDaP4(com, false), "COMO ANTES") {
		t.Fatal("sem snapshot conferido a P4 nao se relaxa")
	}
	if c := aplicarAnuncioDaP4(snapshotConferido{snap: planvalidate.Snapshot{Hash: "h"}, ok: true}, anuncioDoNo{}); c.snap.ConsumerFirstTurnTrusted || !strings.Contains(bannerDaP4(anuncioDoNo{}, true), "COMO ANTES") {
		t.Fatal("sem o anuncio a P4 nao se relaxa")
	}
}

// Revisão adversarial do AOS-542 (achado MÉDIO 1): o `POST /runs` de um consumidor leva a exigência
// do primeiro turno confiável quando o plano foi validado com a P4 relaxada; sem ela, o campo não
// vai no corpo (um nó anterior recusava-o com 400).
func TestAOS542_SubmitLevaAExigencia(t *testing.T) {
	var ultimo map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ultimo = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&ultimo)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	cred := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(cred, []byte("token-de-teste"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := &nodeClient{base: srv.URL, http: srv.Client(), credFile: cred, principal: "nhi:teste"}
	for _, exige := range []bool{true, false} {
		if err := cli.Submit(context.Background(), pedidoDeRun{RunID: "r", Objective: "o", Tools: []string{"ocr_read"}, RequiresFirstTurnTrusted: exige}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		v, tem := ultimo["requires_first_turn_trusted"]
		if exige != tem || (tem && v != true) {
			t.Fatalf("exige=%v: campo presente=%v valor=%v", exige, tem, v)
		}
	}
}

// Re-revisão do AOS-542 (Mu4): o EXECUTOR põe a exigência só nos nós com `inputs`, e só com o
// plano validado com a P4 relaxada.
func TestAOS542_ExecutorExigeSoNosConsumidores(t *testing.T) {
	nos := map[string]plan.Node{
		"n0": {NodeID: "n0", Objective: "le"},
		"n1": {NodeID: "n1", Objective: "usa", DependsOn: []string{"n0"},
			Consumes: []plan.PayloadEdge{{From: "n0", Output: "texto", Type: plan.PayloadSummary}}},
	}
	for _, exigido := range []bool{true, false} {
		cli := &runnerQueGuarda{}
		e := &executorDeNos{cli: cli, runID: "plano", nos: nos, emVoo: map[string]struct{}{},
			payloads:             map[chaveDePayload]string{{no: "n0", output: "texto"}: "dados"},
			primeiroTurnoExigido: exigido}
		if err := e.submeter(context.Background(), "n0"); err != nil {
			t.Fatal(err)
		}
		if cli.ultimo.RequiresFirstTurnTrusted {
			t.Fatalf("exigido=%v: um no sem inputs nao leva a exigencia", exigido)
		}
		if err := e.submeter(context.Background(), "n1"); err != nil {
			t.Fatal(err)
		}
		if cli.ultimo.RequiresFirstTurnTrusted != exigido {
			t.Fatalf("exigido=%v: o consumidor levou %v", exigido, cli.ultimo.RequiresFirstTurnTrusted)
		}
	}
}
