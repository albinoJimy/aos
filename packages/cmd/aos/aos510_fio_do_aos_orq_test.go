package main

// AOS-510 — OS FICHEIROS DE FIO DA TENTATIVA POR RESPOSTA VAZIA, DO LADO DO NÓ.
//
// O que atravessa o fio entre os dois binários, nos dois sentidos:
//
//   - `testdata/aos510_fio/tools-enforce-retry-2-empty.json` é o `GET /tools` do nó real com o
//     tecto a 2 e a classe da resposta vazia ligada — GERADO aqui e lido pelo cliente do `aos-orq`;
//   - `../aos-orq/testdata/aos511_fio/post-runs-summarize*.json` são os corpos do `POST /runs` que
//     o `aos-orq` REAL enviou para a primeira, a segunda e a terceira tentativas do nó de resumo
//     — gerados lá e CONSUMIDOS aqui pelo nó real, com o vínculo ao pedido;
//   - `testdata/aos510_fio/tentativa-vazia-*.json` são as respostas do `GET /runs/{id}` do nó real
//     a esses corpos, geradas aqui e consumidas pelo `aos-orq`.
//
// Regeneram-se com `AOS494_ACTUALIZAR_FIO=1`: este pacote (o anúncio), o `aos-orq` (os corpos),
// este pacote (as respostas), e o `aos-orq` outra vez.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos510LerDoAosOrq lê um ficheiro gerado pelo `aos-orq` (AOS-511).
func aos510LerDoAosOrq(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "aos-orq", "testdata", "aos511_fio", nome+".json"))
	if err != nil {
		if aos494Actualizar() {
			t.Skipf("o ficheiro %s ainda nao foi gerado (%v): corre primeiro o teste do aos-orq com AOS494_ACTUALIZAR_FIO=1, e este outra vez", nome, err)
		}
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o no aos-orq com AOS494_ACTUALIZAR_FIO=1", err)
	}
	return bytes.TrimSpace(cru)
}

// aos510Fio compara (ou, a regenerar, escreve) um ficheiro que o `aos-orq` consome.
func aos510Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos510_fio", nome+".json")
	cru = bytes.TrimSpace(cru)
	if aos494Actualizar() {
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatalf("criar a pasta do fio: %v", err)
		}
		if err := os.WriteFile(caminho, append(cru, '\n'), 0o644); err != nil {
			t.Fatalf("escrever %s: %v", caminho, err)
		}
		return
	}
	quer, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o com AOS494_ACTUALIZAR_FIO=1", err)
	}
	if !bytes.Equal(bytes.TrimSpace(quer), cru) {
		t.Fatalf("o no deixou de responder o que o aos-orq le nos testes dele (%s):\n  fio:   %s\n  agora: %s", caminho, bytes.TrimSpace(quer), cru)
	}
}

// TestAOS510_Fio_AnuncioDoGetTools GERA o anúncio: o `GET /tools` do nó real de produção com o
// tecto a 2 e a classe ligada. É, byte a byte, o do AOS-502 com `empty_output` acrescentado — e
// com o interruptor desligado é o do AOS-502 sem mais nada (compatibilidade com um `aos-orq`
// anterior, que lê o mesmo anúncio de sempre).
func TestAOS510_Fio_AnuncioDoGetTools(t *testing.T) {
	n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
	antes, err := os.ReadFile(filepath.Join("testdata", "aos502_fio", "tools-enforce-retry-2.json"))
	if err != nil {
		t.Fatal(err)
	}
	antes = bytes.TrimSpace(antes)
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatal(err)
	}
	desligado, err := NewAPIHandler(n.svc, n.node, WithToolCatalog(cat), WithRunRetryMax(2))
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.TrimSpace(postJSON(desligado, http.MethodGet, "/tools", nil).Body.Bytes()); !bytes.Equal(got, antes) {
		t.Fatalf("com o interruptor desligado o GET /tools e, byte a byte, o do fio do AOS-502:\n  fio:   %s\n  agora: %s", antes, got)
	}
	ligado, err := NewAPIHandler(n.svc, n.node, WithToolCatalog(cat), WithRunRetryMax(2), WithRunRetryEmpty())
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(ligado, http.MethodGet, "/tools", nil)
	quer := strings.TrimSuffix(string(antes), `{"max":2}}`) + `{"max":2,"empty_output":true}}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != quer {
		t.Fatalf("o anuncio da classe e o unico acrescento:\n  quero: %s\n  veio:  %d %s", quer, rec.Code, got)
	}
	aos510Fio(t, "tools-enforce-retry-2-empty", rec.Body.Bytes())
}

// aos510Entregar entrega ao nó real o corpo do `aos-orq` como o drenador, espera o run e devolve
// o `GET /runs/{id}`.
func aos510Entregar(t *testing.T, n *aos502No, nome string) ([]byte, aos494Resposta) {
	t.Helper()
	campos, runID := aos502CorpoDoAosOrq(t, n, aos510LerDoAosOrq(t, nome))
	if r := postReq(n.h, "/runs", campos, govHeaders()); r.Code != http.StatusCreated {
		t.Fatalf("POST /runs com o corpo %s do aos-orq: %d (%s)", nome, r.Code, r.Body.String())
	}
	n.esperar(t, runID)
	return n.ler(t, runID)
}

// TestAOS510_Fio_TentativasDoAosOrq CONSOME os corpos do `POST /runs` que o `aos-orq` real enviou
// para as três tentativas do nó `summarize`, e GERA as respostas que ele lê.
func TestAOS510_Fio_TentativasDoAosOrq(t *testing.T) {
	const corpo1, corpo2, corpo3 = "post-runs-summarize", "post-runs-summarize-tentativa-2", "post-runs-summarize-tentativa-3"

	// A FORMA, lida dos bytes do `aos-orq`: sem tools, sem contrato, sem origem, com `inputs`.
	for nome, tentativa := range map[string]int{corpo1: 1, corpo2: 2, corpo3: 3} {
		cru := aos510LerDoAosOrq(t, nome)
		var corpo struct {
			RunID       string            `json:"run_id"`
			Tools       []string          `json:"tools"`
			Contrato    []string          `json:"completion_requires"`
			Origem      string            `json:"output_from_tool"`
			Inputs      []json.RawMessage `json:"inputs"`
			PlanRequest *vinculoAoPedido  `json:"plan_request"`
		}
		if err := json.Unmarshal(cru, &corpo); err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		v := corpo.PlanRequest
		if v == nil || v.RunID != aos502Plano || v.NodeID != aos510No_ || corpo.RunID != idDaTentativa(aos502Plano, aos510No_, tentativa) {
			t.Fatalf("%s: o corpo leva o vinculo com o plano e o no, e o id da tentativa %d; veio %+v run_id=%s", nome, tentativa, v, corpo.RunID)
		}
		if (tentativa == 1 && v.Attempt != 0) || (tentativa > 1 && v.Attempt != tentativa) {
			t.Fatalf("%s: attempt so a partir da segunda tentativa; veio %d", nome, v.Attempt)
		}
		if len(corpo.Tools) != 0 || len(corpo.Contrato) != 0 || corpo.Origem != "" || len(corpo.Inputs) == 0 {
			t.Fatalf("%s: a forma de producao do no de resumo e sem tools, sem contrato, sem origem e com inputs; veio %s", nome, cru)
		}
		// O PEDIDO NÃO ESCOLHE A CLASSE: nada no corpo diz porque se pede a tentativa.
		if strings.Contains(string(cru), "empty_output") || strings.Contains(string(cru), "retry_reason") || strings.Contains(string(cru), `"reason"`) {
			t.Fatalf("%s: o corpo do aos-orq nao pode nomear a classe da tentativa: %s", nome, cru)
		}
	}

	// BYTE A BYTE: o corpo da tentativa atravessa o decoder estrito do nó e pára na 403 do vínculo.
	t.Run("ByteAByte_PassaODecoderEAsRecusasDePedido", func(t *testing.T) {
		n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
		for _, nome := range []string{corpo2, corpo3} {
			req := httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(aos510LerDoAosOrq(t, nome)))
			if rec := aos494Servir(n.h, req); rec.Code != http.StatusForbidden {
				t.Fatalf("%s, byte a byte, tinha de passar o decoder e parar no vinculo (403); veio %d (%s)", nome, rec.Code, rec.Body.String())
			}
		}
	})

	// VAZIO, VAZIO, SUCESSO — as três tentativas do `aos-orq` no nó real, com a prova a correr.
	t.Run("VazioVazioSucesso", func(t *testing.T) {
		n := aos510Compor(t, agentruntime.CompletionEnforce, 2, true, false)
		if ger := n.pedir(t, aos502Plano); ger != 1 {
			t.Fatalf("o corpo do aos-orq nomeia a geracao 1; a reclamacao deu %d", ger)
		}
		n.modeloNaoChama("", "stop")
		cru1, r1 := aos510Entregar(t, n, corpo1)
		if r1.Status != "failed" || r1.OutcomeReason != string(agentruntime.OutcomeEmptyOutput) || r1.Verdict == nil || r1.Verdict.ToolCallsRequested != 0 || r1.FinalText != "" {
			t.Fatalf("a primeira tentativa: failed por empty_output, sem tool calls e sem texto; veio %+v", r1)
		}
		aos510Fio(t, "tentativa-vazia-1-falhada", cru1)
		cru2, r2 := aos510Entregar(t, n, corpo2)
		if r2.Status != "failed" || r2.OutcomeReason != string(agentruntime.OutcomeEmptyOutput) || !bytes.Contains(cru2, []byte(`"plan_attempt":{"plan_request":"`+aos502Plano+`"`)) {
			t.Fatalf("a segunda tentativa volta a responder vazio, e declara de que pedido e; veio %+v", r2)
		}
		aos510Fio(t, "tentativa-vazia-2-falhada", cru2)
		n.modeloNaoChama("Resumo: o documento tem tres notas.", "stop")
		cru3, r3 := aos510Entregar(t, n, corpo3)
		if r3.Status != "completed" || !r3.Terminated || r3.FinalText == "" {
			t.Fatalf("a terceira tentativa responde texto e conclui; veio %+v", r3)
		}
		aos510Fio(t, "tentativa-vazia-3-recuperada", cru3)
		if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_empty_admitted_total", 2) || !aos502TemSerie(m, "aos_runs_retry_admitted_total", 0) {
			t.Fatalf("o no admitiu as duas tentativas a mais do aos-orq, na serie desta classe:\n%s", aos502SoRetry(m))
		}
	})

	// VAZIO TRÊS VEZES — a terceira resposta falhada, para o `aos-orq` esgotar as tentativas.
	t.Run("VazioTresVezes", func(t *testing.T) {
		n := aos510Compor(t, agentruntime.CompletionEnforce, 2, true, false)
		n.pedir(t, aos502Plano)
		n.modeloNaoChama("", "stop")
		aos510Entregar(t, n, corpo1)
		aos510Entregar(t, n, corpo2)
		cru3, r3 := aos510Entregar(t, n, corpo3)
		if r3.Status != "failed" || r3.OutcomeReason != string(agentruntime.OutcomeEmptyOutput) {
			t.Fatalf("a terceira tentativa volta a responder vazio; veio %+v", r3)
		}
		aos510Fio(t, "tentativa-vazia-3-falhada", cru3)
	})

	// O MESMO CORPO DA TENTATIVA CONTRA UM NÓ COM O INTERRUPTOR DESLIGADO — o nó de antes, que um
	// `aos-orq` novo encontra enquanto o nó não for ligado: recusado, e nada se hospeda.
	t.Run("NoComOInterruptorDesligado_Recusa", func(t *testing.T) {
		n := aos510Compor(t, agentruntime.CompletionEnforce, 2, false, false)
		n.pedir(t, aos502Plano)
		n.modeloNaoChama("", "stop")
		aos510Entregar(t, n, corpo1)
		campos, runID := aos502CorpoDoAosOrq(t, n, aos510LerDoAosOrq(t, corpo2))
		if r := postReq(n.h, "/runs", campos, govHeaders()); r.Code != http.StatusForbidden {
			t.Fatalf("com o interruptor desligado o corpo da tentativa e recusado (403); veio %d %s", r.Code, r.Body.String())
		}
		if n.existe(t, runID) {
			t.Fatal("a tentativa recusada nao hospeda nada")
		}
	})
}
