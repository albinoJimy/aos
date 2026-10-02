package main

// AOS-402 — a escrita do selo de mediação chega ao /metrics do nó, pela torneira de spans e pela
// mesma passagem do avaliador de SLOs, sem SLO nem alerta.
//
// FALHA-ANTES: até aqui a escrita (`aos.mediation.audit_write_latency_ns`) só existia no span, e o
// colector OTel de produção descarta os traces — nenhuma série do /metrics a expunha.

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

// aos402EmitMediacao emite um span `execute_tool` do Reference Monitor com a política e a escrita
// do selo, pelo tracer do nó.
func aos402EmitMediacao(node *Node, decisao, negadoPor string, politica, escrita time.Duration) {
	_, span := node.Tracer.StartSpan(context.Background(), otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrToolName, "aos402.tool")
	span.SetAttribute(otelgenai.AttrDecision, decisao)
	if negadoPor != "" {
		span.SetAttribute(otelgenai.AttrDeniedBy, negadoPor)
	}
	span.SetAttribute(otelgenai.AttrMediationPolicyLatencyNanos, politica.Nanoseconds())
	span.SetAttribute(otelgenai.AttrMediationAuditWriteLatencyNanos, escrita.Nanoseconds())
	span.SetAttribute(otelgenai.AttrMediationDecisionLatencyNanos, (politica + escrita).Nanoseconds())
	span.End()
}

func TestAOS402_MetricsExpoeAEscritaDoSeloPorDecisaoSemSLO(t *testing.T) {
	node := aos274Node(t, time.Millisecond)
	svc := aos274Service(t, node)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	aos402EmitMediacao(node, otelgenai.DecisionPermit, "", 5*time.Millisecond, 20*time.Millisecond)
	aos402EmitMediacao(node, otelgenai.DecisionPermit, "", 6*time.Millisecond, 30*time.Millisecond)
	aos402EmitMediacao(node, otelgenai.DecisionDeny, "policy", 3*time.Millisecond, 7*time.Millisecond)
	// Recusa por contexto cancelado: o Reference Monitor não escreve e publica zero — não conta.
	aos402EmitMediacao(node, otelgenai.DecisionDeny, "context", time.Millisecond, 0)
	svc.EvaluateSLOsNow(context.Background())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devia dar 200, veio %d", rec.Code)
	}
	body := rec.Body.String()

	quer := map[string]int64{
		`aos_mediation_audit_write_samples{decision="permit"}`:               2,
		`aos_mediation_audit_write_samples{decision="deny"}`:                 1,
		`aos_mediation_audit_write_samples{decision="escalate"}`:             0,
		`aos_mediation_audit_write_latency_ns{decision="permit",stat="p50"}`: int64(25 * time.Millisecond),
		`aos_mediation_audit_write_latency_ns{decision="permit",stat="p95"}`: int64(29500 * time.Microsecond),
		`aos_mediation_audit_write_latency_ns{decision="permit",stat="max"}`: int64(30 * time.Millisecond),
		`aos_mediation_audit_write_latency_ns{decision="deny",stat="max"}`:   int64(7 * time.Millisecond),
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
	// Uma decisão sem amostras não publica percentis a zero (seriam escritas que não aconteceram).
	if strings.Contains(body, `aos_mediation_audit_write_latency_ns{decision="escalate"`) {
		t.Error("escalate sem amostras nao pode publicar percentis")
	}
	// Sem SLO: nenhuma série de alvo, breach ou alerta fala desta medida.
	for _, proibido := range []string{`sli="mediation_audit_write`, `sli="audit_write`} {
		if strings.Contains(body, proibido) {
			t.Errorf("a escrita do selo nao tem SLO nem alerta; o /metrics contem %q", proibido)
		}
	}
	// Formato de exposição das famílias novas: o guarda geral corre sem avaliador e não as vê.
	for _, linha := range strings.Split(body, "\n") {
		if !strings.HasPrefix(linha, "aos_mediation_audit_write_") {
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
	for _, nome := range []string{"aos_mediation_audit_write_samples", "aos_mediation_audit_write_latency_ns"} {
		if n := strings.Count(body, "# HELP "+nome+" "); n != 1 {
			t.Errorf("%s tem %d linhas HELP; quero 1", nome, n)
		}
		if n := strings.Count(body, "# TYPE "+nome+" gauge"); n != 1 {
			t.Errorf("%s tem %d linhas TYPE gauge; quero 1", nome, n)
		}
	}
}

// TestAOS402_JanelaSemMediacaoSoPublicaAmostrasAZero — um nó sem tool calls na janela diz que não
// há medida (samples a 0) e não inventa percentis.
func TestAOS402_JanelaSemMediacaoSoPublicaAmostrasAZero(t *testing.T) {
	node := aos274Node(t, time.Millisecond)
	svc := aos274Service(t, node)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	svc.EvaluateSLOsNow(context.Background())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if got, ok := valorDaSerie(body, `aos_mediation_audit_write_samples{decision="permit"}`); !ok || got != 0 {
		t.Fatalf("sem mediacao: samples do permit a 0; veio %d (ok=%v)", got, ok)
	}
	if strings.Contains(body, "aos_mediation_audit_write_latency_ns") {
		t.Error("sem amostras nao ha percentis a publicar")
	}
}

// TestAOS402_SemTorneiraNaoPublicaNada — com a observabilidade OTLP desligada o avaliador não vê
// spans; publicar `samples` a zero leria-se como «nenhuma mediação» enquanto os permits sobem.
func TestAOS402_SemTorneiraNaoPublicaNada(t *testing.T) {
	var b strings.Builder
	obs := []otelgenai.AuditWriteLatency{{Decision: otelgenai.DecisionPermit, Samples: 3, P95: 10}}
	writeAuditWriteMetrics(&b, obs, false)
	if b.Len() != 0 {
		t.Fatalf("sem torneira nada sai; saiu:\n%s", b.String())
	}
	writeAuditWriteMetrics(&b, obs, true)
	if !strings.Contains(b.String(), `aos_mediation_audit_write_latency_ns{decision="permit",stat="p95"} 10`) {
		t.Fatalf("com torneira a serie sai; saiu:\n%s", b.String())
	}
}

// valorDaSerie lê o valor inteiro de uma série exacta (nome + rótulos) do corpo do /metrics.
func valorDaSerie(body, serie string) (int64, bool) {
	for _, linha := range strings.Split(body, "\n") {
		if !strings.HasPrefix(linha, serie+" ") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(linha, serie)), 64)
		if err != nil {
			return 0, false
		}
		return int64(v), true
	}
	return 0, false
}
