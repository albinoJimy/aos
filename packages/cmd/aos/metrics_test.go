package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Testa a via de métricas /metrics (revisão de prontidão #5): o nó só exportava traces; sem
// métricas os SLOs (disponibilidade, saúde de dependências, USE) eram indetectáveis. Prova que o
// endpoint existe, responde em texto Prometheus e expõe os SLIs-chave.
func TestMetricsEndpoint(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devia dar 200, veio %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("/metrics devia ser text/plain (Prometheus), veio %q", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	s := string(body)
	// SLIs mínimos que a via tem de expor.
	for _, want := range []string{
		"aos_up 1",
		"# TYPE aos_up gauge",
		"aos_eventstore_healthy",
		"aos_ready",
		"aos_goroutines",
		"aos_memory_alloc_bytes",
		"aos_build_info",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("/metrics devia conter %q; corpo:\n%s", want, s)
		}
	}
}

// TestMetricsNoVaultGaugeWithoutCustody: sem custódia de KEK externa (vault in-memory de
// referência), o gauge aos_dsar_vault_ready NÃO é emitido (não há o que sondar).
func TestMetricsNoVaultGaugeWithoutCustody(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(string(body), "aos_dsar_vault_ready") {
		t.Error("sem custodia externa (in-memory) o gauge aos_dsar_vault_ready nao devia sair")
	}
}
