package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// AOS-544: o consumo de planeamento que o drenador declara fica no facto de desfecho, mesmo SEM a
// quota por principal configurada (sem ela a parcela não se grava) — é aí que o custo do planeador
// se lê em cada plano.
func TestAOS544_OConsumoFicaNoDesfechoSemQuota(t *testing.T) {
	node, h := aos464No(t)
	node.PlanDrainers = map[string]bool{drenador466: true}
	if node.QuotaPorPrincipal != nil {
		t.Fatal("o cenario e sem quota por principal")
	}
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-544"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-544", "generation": 1, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 1834, "tokens_medidos": true, "cost_micro_usd": 345, "custo_medido": true},
	})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", r.codigo, r.corpo)
	}
	evs, err := node.EventStore.Read(context.Background(), planRequestStream, 1)
	if err != nil {
		t.Fatalf("ler o stream dos pedidos: %v", err)
	}
	var achado bool
	for _, e := range evs {
		if e.Type != EventTypePlanRequestOutcome {
			continue
		}
		var p desfechoPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.RunID != "plano-544" {
			continue
		}
		achado = true
		if p.Consumo == nil || p.Consumo.CostMicroUSD != 345 || !p.Consumo.CustoMedido || p.Consumo.Tokens != 1834 || !p.Consumo.TokensMedidos {
			t.Fatalf("o desfecho tem de levar o consumo declarado: %+v", p.Consumo)
		}
	}
	if !achado {
		t.Fatal("nao houve facto de desfecho do plano")
	}
	// Um desfecho sem consumo declarado não leva o campo.
	if b, _ := json.Marshal(desfechoPayload{RunID: "x"}); json.Valid(b) && contemChave(b, "consumo") {
		t.Fatalf("sem consumo o campo nao vai no facto: %s", b)
	}
}

func contemChave(b []byte, chave string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m[chave]
	return ok
}
