package main

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// O APAGAMENTO DO TITULAR CHEGA AO DESFECHO EM MEMÓRIA.
//
// O `GET /runs/{id}` tem dois ramos para um run concluído: o registo de desfechos em memória,
// que guarda o `final_text` em claro, e o ramo durável, que o decifra da captura do turno
// terminal. O `/dsar/erase` destrói a KEK do titular: o ramo durável e o `/reconstruct` deixam
// de abrir o conteúdo. O registo em memória não dependia da KEK e continuava a servir o texto
// até o nó reiniciar ou a poda FIFO o levar.

// TestApagamentoDoTitular_ODesfechoEmMemoriaDeixaDeServirOTexto: no MESMO processo, sem
// reinício, o texto final de um run cujo titular foi apagado não sai pelo `GET /runs/{id}`.
// A resposta é a que o ramo durável dá ao mesmo run depois de um reinício.
func TestApagamentoDoTitular_ODesfechoEmMemoriaDeixaDeServirOTexto(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce, aos494Modelo(""), true)
	defer aos494Fechar(t, node, svc)
	aos494Submeter(t, node, svc, h, govHeaders(), []string{aos494Tool})

	// ANTES (não-vácuo): o texto sai, e sai do registo em memória.
	if _, emMemoria := svc.Outcome(aos494RunID); !emMemoria {
		t.Fatal("o desfecho tinha de estar em memoria antes do apagamento: o teste nao exercitava esse ramo")
	}
	if _, antes := aos494Ler(t, h, govHeaders()); antes.FinalText != aos494Boa {
		t.Fatalf("antes do apagamento o run conclui com o texto; veio %+v", antes)
	}

	// O titular do run, em modo soberano, é o principal AUTENTICADO de quem o submeteu (AOS-217),
	// e não o `principal_nhi` do corpo.
	er := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "apaga-desfecho", SubjectID: govReader}, govHeaders())
	if er.Code != http.StatusOK {
		t.Fatalf("POST /dsar/erase devia dar 200, veio %d (%s)", er.Code, er.Body.String())
	}
	// CONTROLO, antes da leitura que interessa: o apagamento foi real e foi do titular DESTE run.
	// Sem ele, apagar outro titular deixava o texto sair e o teste acusava um defeito que não há.
	if rec := getReq(h, "/runs/"+aos494RunID+"/reconstruct", govHeaders()); rec.Code != http.StatusGone {
		t.Fatalf("a reconstrucao de um titular apagado devia dar 410, veio %d (%s)", rec.Code, rec.Body.String())
	}

	cru, depois := aos494Ler(t, h, govHeaders())
	if bytes.Contains(cru, []byte(aos494Documento)) || depois.FinalText != "" {
		t.Fatalf("o GET /runs/{id} continua a servir o texto de um titular apagado: %s", cru)
	}
	if depois.Status != "completed" || !depois.Terminated || !depois.OutputUnavailable {
		t.Fatalf("depois do apagamento o run responde completed, sem texto e com output_unavailable; veio %+v", depois)
	}
	// O veredicto não é conteúdo de titular: continua a sair, agora do log.
	if depois.Verdict == nil || !depois.Verdict.Fulfilled {
		t.Fatalf("o vector do veredicto tem de sair depois do apagamento; veio %+v", depois.Verdict)
	}
}

// TestApagamentoDoTitular_NaoTocaNoDesfechoDeOutroTitular: apagar um titular que não é o do run
// deixa o desfecho em memória como estava.
func TestApagamentoDoTitular_NaoTocaNoDesfechoDeOutroTitular(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce, aos494Modelo(""), true)
	defer aos494Fechar(t, node, svc)
	aos494Submeter(t, node, svc, h, govHeaders(), []string{aos494Tool})

	er := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "apaga-outro", SubjectID: "nhi:outro-titular"}, govHeaders())
	if er.Code != http.StatusOK {
		t.Fatalf("POST /dsar/erase devia dar 200, veio %d (%s)", er.Code, er.Body.String())
	}
	if _, emMemoria := svc.Outcome(aos494RunID); !emMemoria {
		t.Fatal("o desfecho de um run de OUTRO titular saiu do registo em memoria")
	}
	if _, r := aos494Ler(t, h, govHeaders()); r.FinalText != aos494Boa || r.OutputUnavailable {
		t.Fatalf("o run de outro titular continua a concluir com o texto; veio %+v", r)
	}
}

// TestExpiracaoDoTitular_ODesfechoEmMemoriaDeixaDeServirOTexto: a expiração por TTL destrói a
// MESMA KEK por outra porta ([cryptoShredSink]), e o desfecho em memória sai do registo da mesma
// maneira. Corre pela rota, sobre o nó composto: o que se prova é a cablagem do [Bootstrap].
func TestExpiracaoDoTitular_ODesfechoEmMemoriaDeixaDeServirOTexto(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	comRetencao := func(cfg *Config) {
		rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{audit.ClassPIIOperational: time.Minute})
		if err != nil {
			t.Fatalf("NewRetentionConfig: %v", err)
		}
		cfg.Retention = rc
		cfg.RetentionClock = retentionFarFuture
	}
	node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce, aos494Modelo(""), true, comRetencao)
	defer aos494Fechar(t, node, svc)
	aos494Submeter(t, node, svc, h, govHeaders(), []string{aos494Tool})
	if _, antes := aos494Ler(t, h, govHeaders()); antes.FinalText != aos494Boa {
		t.Fatalf("antes da expiracao o run conclui com o texto; veio %+v", antes)
	}

	ex := postReq(h, "/dsar/expire", map[string]any{}, govHeaders())
	if ex.Code != http.StatusOK {
		t.Fatalf("POST /dsar/expire devia dar 200, veio %d (%s)", ex.Code, ex.Body.String())
	}
	// CONTROLO: a expiração destruiu a KEK do titular deste run.
	if rec := getReq(h, "/runs/"+aos494RunID+"/reconstruct", govHeaders()); rec.Code != http.StatusGone {
		t.Fatalf("a reconstrucao de um titular expirado devia dar 410, veio %d (%s)", rec.Code, rec.Body.String())
	}
	cru, depois := aos494Ler(t, h, govHeaders())
	if bytes.Contains(cru, []byte(aos494Documento)) || depois.FinalText != "" || !depois.OutputUnavailable {
		t.Fatalf("o GET /runs/{id} continua a servir o texto de um titular expirado: %s", cru)
	}
}
