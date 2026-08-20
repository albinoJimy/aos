package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// A EXPORTAÇÃO OTLP PODE MORRER SEM PARAR MAIS NADA — E ISSO TEM DE SER VISÍVEL.
//
// O exporter é FAIL-OPEN por decisão declarada: a recusa do colector não quebra o run. É a escolha
// certa, e tem um preço — uma má configuração (certificado de cliente errado, colector que
// rejeita) pára os spans sem parar nada mais.
//
// O inventário dizia que isso acontecia «em silêncio». NÃO acontece: cada lote falhado escreve no
// log. O que faltava era outra coisa, e são duas:
//
//  1. nenhum dos contadores saía em `/metrics` — a falha não era ALERTÁVEL, só legível à mão
//     depois de alguém desconfiar;
//  2. a linha saía por CADA lote, o que inunda o log e afoga a que interessa entre as suas
//     próprias repetições.
// ---------------------------------------------------------------------------

// TestQueixaDeExportNaoInundaOLog — a primeira falha sai já; as seguintes ficam represadas.
func TestQueixaDeExportNaoInundaOLog(t *testing.T) {
	e := &OTLPHTTPExporter{maxRetries: 2}
	t0 := time.Now()

	primeira := e.queixaDeExport(t0, 5)
	if primeira == "" {
		t.Fatal("a PRIMEIRA falha ficou calada — e a que muda o mundo de «funciona» para «nao funciona»")
	}
	// As seguintes, dentro do intervalo, ficam represadas.
	for i := 0; i < 20; i++ {
		if msg := e.queixaDeExport(t0.Add(time.Duration(i)*time.Second), 5); msg != "" {
			t.Fatalf("a falha %d escreveu no log dentro do intervalo — o log inunda: %s", i+2, msg)
		}
	}

	// Passado o intervalo, sai UMA linha que diz QUANTAS foram. Contar é o que distingue
	// «continua mau» de «voltou a acontecer».
	resumo := e.queixaDeExport(t0.Add(intervaloDeQueixa+time.Second), 5)
	if resumo == "" {
		t.Fatal("passado o intervalo NADA saiu — a falha persistente ficaria muda")
	}
	if !strings.Contains(resumo, "21") {
		t.Errorf("o resumo nao diz quantas falhas houve (esperava 21): %s", resumo)
	}
	// E aponta para onde se alerta, senão quem o lê não sabe o que fazer a seguir.
	if !strings.Contains(resumo, "aos_otlp_spans_failed_total") {
		t.Errorf("o resumo nao aponta para a metrica alertavel: %s", resumo)
	}
}

// TestPrimeiraQueixaNomeiaOFailOpen — quem lê a linha tem de perceber que o run NÃO parou.
//
// Sem isso, um operador lê «FALHOU» e vai procurar runs partidos que não existem. O fail-open é
// uma decisão declarada; a linha que anuncia a falha tem de a declarar também.
func TestPrimeiraQueixaNomeiaOFailOpen(t *testing.T) {
	e := &OTLPHTTPExporter{maxRetries: 2}
	msg := e.queixaDeExport(time.Now(), 3)
	if !strings.Contains(msg, "fail-open") {
		t.Errorf("a linha de falha nao declara o fail-open — quem a ler vai procurar runs partidos: %s", msg)
	}
	if !strings.Contains(msg, "3 span") {
		t.Errorf("a linha nao diz QUANTOS spans se perderam: %s", msg)
	}
}

// TestQueixaVoltaASairDepoisDeUmPeriodoLimpo é o CONTROLO do represamento.
//
// Sem ele, um travão que calasse TUDO depois da primeira linha passaria no teste acima — e a
// segunda avaria, semanas depois, seria muda.
func TestQueixaVoltaASairDepoisDeUmPeriodoLimpo(t *testing.T) {
	e := &OTLPHTTPExporter{maxRetries: 2}
	t0 := time.Now()
	if e.queixaDeExport(t0, 1) == "" {
		t.Fatal("a primeira devia sair")
	}
	// Uma avaria MUITO mais tarde: tem de voltar a falar.
	if msg := e.queixaDeExport(t0.Add(24*time.Hour), 1); msg == "" {
		t.Error("uma avaria 24h depois ficou muda — o travao calou tudo em vez de represar")
	}
}

// TestMetricasDeOTLPSaemNoEndpoint é a metade que torna a falha ALERTÁVEL.
//
// Sem estas séries, a única forma de saber que a exportação morreu era ler o log à mão, depois de
// alguém desconfiar. `failed` e `dropped` a subir com `exported` parado é a assinatura exacta de
// uma autenticação mal configurada — e é isso que um alerta precisa de conseguir ver.
func TestMetricasDeOTLPSaemNoEndpoint(t *testing.T) {
	exp := &OTLPHTTPExporter{}
	exp.exported.Add(7)
	exp.failed.Add(3)
	exp.dropped.Add(2)
	exp.batches.Add(5)

	h := &apiHandler{node: &Node{otlp: exp}, svc: &NodeService{}}
	w := httptest.NewRecorder()
	h.handleMetrics(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics devolveu %d", w.Code)
	}
	corpo := w.Body.String()

	for serie, valor := range map[string]string{
		"aos_otlp_spans_exported_total": "7",
		"aos_otlp_spans_failed_total":   "3",
		"aos_otlp_spans_dropped_total":  "2",
		"aos_otlp_batches_total":        "5",
	} {
		if !strings.Contains(corpo, serie+" "+valor) {
			t.Errorf("falta a serie %s com valor %s — a falha de export nao seria alertavel", serie, valor)
		}
	}

	// CONTROLO: sem exporter composto, as séries NÃO saem. Emitir zeros faria um nó sem
	// observabilidade parecer um nó cuja exportação está saudável — que é pior do que não emitir.
	h2 := &apiHandler{node: &Node{}, svc: &NodeService{}}
	w2 := httptest.NewRecorder()
	h2.handleMetrics(w2, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(w2.Body.String(), "aos_otlp_") {
		t.Error("sem exporter composto sairam series de OTLP — um no sem observabilidade pareceria saudavel")
	}
}

// TestSendUsaOTravaoDeQueixa fecha a ligação que os testes acima NÃO cobriam.
//
// Eles exercitam `queixaDeExport` directamente. Uma mutação que substituísse a CHAMADA por um
// `logf` cru — repondo a inundação — passava neles sem tocar em nada: a função continuava correcta
// e ninguém a usava. É o mesmo erro que já apareceu hoje três vezes (testar a unidade e não a
// ligação), e é por isso que este teste vai pelo caminho REAL do `send`.
func TestSendUsaOTravaoDeQueixa(t *testing.T) {
	// Colector que recusa SEMPRE — a assinatura de uma autenticação mal configurada.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var linhas int
	exp, err := NewOTLPHTTPExporter(srv.URL,
		WithOTLPHTTPClient(srv.Client()),
		WithOTLPMaxRetries(0),
		WithOTLPBackoff(time.Millisecond),
		WithOTLPLogger(func(string, ...any) { mu.Lock(); linhas++; mu.Unlock() }),
	)
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	defer exp.Close()

	const lotes = 25
	for i := 0; i < lotes; i++ {
		exp.send([]otelgenai.SpanData{{Name: "span"}})
	}

	mu.Lock()
	got := linhas
	mu.Unlock()

	// CONTROLO ANTES: as falhas ACONTECERAM mesmo — senão este teste mediria um exporter parado.
	if s := exp.Stats(); s.Failed != lotes {
		t.Fatalf("esperava %d spans falhados, veio %d — o cenario nao exercitou o caminho de falha", lotes, s.Failed)
	}
	// E o log NÃO acompanhou o número de lotes.
	if got >= lotes {
		t.Errorf("o log escreveu %d linha(s) para %d lote(s) falhados — a inundacao esta de volta", got, lotes)
	}
	if got == 0 {
		t.Error("o log ficou COMPLETAMENTE mudo — represar nao e calar")
	}
}
