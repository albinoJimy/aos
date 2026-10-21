package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
