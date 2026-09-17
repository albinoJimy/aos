package main

// AOS-405 — a política partida por hook chega ao /metrics do nó, pela torneira de spans e pela mesma
// passagem do avaliador de SLOs, sem SLO nem alerta.
//
// FALHA-ANTES: a latência por hook não existia; a política só se lia inteira.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

func aos405EmitMediacao(node *Node, decisao, negadoPor string, hooks map[string]time.Duration) {
	_, span := node.Tracer.StartSpan(context.Background(), otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrToolName, "aos405.tool")
	span.SetAttribute(otelgenai.AttrDecision, decisao)
	if negadoPor != "" {
		span.SetAttribute(otelgenai.AttrDeniedBy, negadoPor)
	}
	var politica time.Duration
	for h, d := range hooks {
		span.SetAttribute(otelgenai.MediationHookLatencyAttr(h), d.Nanoseconds())
		politica += d
	}
	span.SetAttribute(otelgenai.AttrMediationPolicyLatencyNanos, politica.Nanoseconds())
	span.End()
}

func aos405Metrics(t *testing.T, emitir func(*Node)) string {
	t.Helper()
	node := aos274Node(t, time.Millisecond)
	svc := aos274Service(t, node)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	emitir(node)
	svc.EvaluateSLOsNow(context.Background())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devia dar 200, veio %d", rec.Code)
	}
	return rec.Body.String()
}

func TestAOS405_MetricsExpoeALatenciaPorHookSemSLO(t *testing.T) {
	ms := time.Millisecond
	body := aos405Metrics(t, func(node *Node) {
		aos405EmitMediacao(node, otelgenai.DecisionPermit, "", map[string]time.Duration{"identity": 1 * ms, "revalidation": 4 * ms, "egress": 2 * ms})
		aos405EmitMediacao(node, otelgenai.DecisionPermit, "", map[string]time.Duration{"identity": 1 * ms, "revalidation": 40 * ms, "egress": 3 * ms})
		aos405EmitMediacao(node, otelgenai.DecisionDeny, "policy", map[string]time.Duration{"identity": 3 * ms})
		// Contexto cancelado: nenhum hook correu de facto.
		aos405EmitMediacao(node, otelgenai.DecisionDeny, "context", map[string]time.Duration{"identity": 90 * ms})
	})

	quer := map[string]int64{
		`aos_mediation_hook_samples{hook="identity"}`:                   3,
		`aos_mediation_hook_samples{hook="revalidation"}`:               2,
		`aos_mediation_hook_samples{hook="egress"}`:                     2,
		`aos_mediation_hook_latency_ns{hook="identity",stat="max"}`:     int64(3 * ms),
		`aos_mediation_hook_latency_ns{hook="identity",stat="p50"}`:     int64(1 * ms),
		`aos_mediation_hook_latency_ns{hook="revalidation",stat="max"}`: int64(40 * ms),
		`aos_mediation_hook_latency_ns{hook="revalidation",stat="p50"}`: int64(22 * ms),
		`aos_mediation_hook_latency_ns{hook="revalidation",stat="p95"}`: int64(38200 * time.Microsecond),
		`aos_mediation_hook_latency_ns{hook="egress",stat="p95"}`:       int64(2950 * time.Microsecond),
	}
	for serie, valor := range quer {
		got, ok := valorDaSerie(body, serie)
		if !ok {
			t.Errorf("/metrics devia conter %s", serie)
			continue
		}
		if got != valor {
			t.Errorf("%s = %d, quero %d", serie, got, valor)
		}
	}
	// Sem SLO: nenhuma série de alvo, breach ou alerta fala desta medida.
	if strings.Contains(body, `sli="mediation_hook`) {
		t.Error("a latencia por hook nao tem SLO nem alerta")
	}
	for _, linha := range strings.Split(body, "\n") {
		if !strings.HasPrefix(linha, "aos_mediation_hook_") {
			continue
		}
		nome, valor, ok := strings.Cut(linha, " ")
		if !ok {
			t.Errorf("amostra sem valor: %q", linha)
			continue
		}
		if i := strings.IndexByte(nome, '{'); i >= 0 {
			nome = nome[:i]
		}
		if !nomeValido.MatchString(nome) {
			t.Errorf("nome invalido %q", nome)
		}
		if _, err := strconv.ParseFloat(valor, 64); err != nil {
			t.Errorf("valor ilegivel %q em %q", valor, linha)
		}
	}
	for _, nome := range []string{"aos_mediation_hook_samples", "aos_mediation_hook_latency_ns"} {
		if n := strings.Count(body, "# HELP "+nome+" "); n != 1 {
			t.Errorf("%s tem %d linhas HELP; quero 1", nome, n)
		}
		if n := strings.Count(body, "# TYPE "+nome+" gauge"); n != 1 {
			t.Errorf("%s tem %d linhas TYPE gauge; quero 1", nome, n)
		}
	}
}

// TestAOS405_JanelaSemMediacaoNaoPublicaHooks — sem amostras não se conhece a lista de hooks, e
// inventá-la seria afirmar uma cadeia que o nó pode não compor.
func TestAOS405_JanelaSemMediacaoNaoPublicaHooks(t *testing.T) {
	body := aos405Metrics(t, func(*Node) {})
	if strings.Contains(body, "aos_mediation_hook_") {
		t.Error("sem mediacao na janela nao ha series de hook a publicar")
	}
}
