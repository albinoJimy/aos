package main

// AOS-502 — OS FICHEIROS DE FIO DA NOVA TENTATIVA, DO LADO DO NÓ.
//
// O que atravessa o fio entre os dois binários, nos dois sentidos, fica preso aqui:
//
//   - `testdata/aos502_fio/tools-enforce-retry-2.json` é o `GET /tools` do nó real com o tecto de
//     tentativas a 2 — GERADO aqui e lido pelo cliente do `aos-orq`;
//   - `../aos-orq/testdata/aos503_fio/vectores-do-id.json` são os ids que o `aos-orq` compõe para
//     cada (pedido, nó, tentativa) — gerados lá e conferidos aqui contra o que o nó compõe;
//   - `../aos-orq/testdata/aos503_fio/post-runs-read_notes*.json` são os corpos do `POST /runs`
//     que o `aos-orq` REAL enviou para a primeira, a segunda e a terceira tentativas — gerados lá
//     e CONSUMIDOS aqui, byte a byte, pelo nó real, COM o vínculo ao pedido (é dele que a prova
//     depende);
//   - `testdata/aos502_fio/tentativa-*.json` são as respostas do `GET /runs/{id}` do nó real a
//     esses corpos — a resposta de produção de 2026-10-04 nas tentativas falhadas —, geradas aqui
//     e consumidas pelo `aos-orq`.
//
// Regeneram-se com `AOS494_ACTUALIZAR_FIO=1`: este pacote (o anúncio), o `aos-orq` (os corpos e
// os vectores), este pacote (as respostas), e o `aos-orq` outra vez.

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

// aos503FioDoAosOrq é a pasta dos ficheiros que o teste do `aos-orq` GERA para a nova tentativa.
var aos503FioDoAosOrq = filepath.Join("..", "aos-orq", "testdata", "aos503_fio")

// aos502Plano é o pedido de plano dos corpos do `aos-orq`: o teste dele usa este id.
const aos502Plano = "plan-aos503-fio"

// aos502LerDoAosOrq lê um ficheiro gerado pelo `aos-orq`.
func aos502LerDoAosOrq(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join(aos503FioDoAosOrq, nome+".json"))
	if err != nil {
		if aos494Actualizar() {
			t.Skipf("o ficheiro %s ainda nao foi gerado (%v): corre primeiro o teste do aos-orq com AOS494_ACTUALIZAR_FIO=1, e este outra vez", nome, err)
		}
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o no aos-orq com AOS494_ACTUALIZAR_FIO=1", err)
	}
	return bytes.TrimSpace(cru)
}

// aos502Fio compara (ou, a regenerar, escreve) um ficheiro que o `aos-orq` consome.
func aos502Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos502_fio", nome+".json")
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

// TestAOS502_Fio_AnuncioDoGetTools GERA o anúncio: o `GET /tools` do nó real de produção com o
// tecto a 2. É, byte a byte, o anúncio do AOS-494 com o `run_retry` acrescentado no fim — e com
// o tecto a zero é o do AOS-494 sem mais nada.
func TestAOS502_Fio_AnuncioDoGetTools(t *testing.T) {
	n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
	semTecto := postJSON(n.h, http.MethodGet, "/tools", nil)
	antes, err := os.ReadFile(filepath.Join("testdata", "aos494_fio", "tools-enforce.json"))
	if err != nil {
		t.Fatal(err)
	}
	antes = bytes.TrimSpace(antes)
	if got := bytes.TrimSpace(semTecto.Body.Bytes()); !bytes.Equal(got, antes) {
		t.Fatalf("com o tecto a zero o GET /tools e, byte a byte, o do fio do AOS-494:\n  fio:   %s\n  agora: %s", antes, got)
	}
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewAPIHandler(n.svc, n.node, WithToolCatalog(cat), WithRunRetryMax(2))
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(h, http.MethodGet, "/tools", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tools: %d (%s)", rec.Code, rec.Body.String())
	}
	quer := string(bytes.TrimSuffix(antes, []byte("}"))) + `,"run_retry":{"max":2}}`
	if got := strings.TrimSpace(rec.Body.String()); got != quer {
		t.Fatalf("o anuncio e o unico acrescento:\n  quero: %s\n  veio:  %s", quer, got)
	}
	aos502Fio(t, "tools-enforce-retry-2", rec.Body.Bytes())
}

// TestAOS502_Fio_VectoresDoId confere, contra o que o NÓ compõe, os ids que o `aos-orq` compõe
// para cada (pedido, nó, tentativa). Os dois binários não se importam: é este ficheiro que os
// prende à mesma forma.
func TestAOS502_Fio_VectoresDoId(t *testing.T) {
	var vectores []struct {
		Plano     string `json:"plano"`
		NodeID    string `json:"node_id"`
		Tentativa int    `json:"tentativa"`
		ID        string `json:"id"`
	}
	if err := json.Unmarshal(aos502LerDoAosOrq(t, "vectores-do-id"), &vectores); err != nil {
		t.Fatalf("vectores ilegiveis: %v", err)
	}
	if len(vectores) < 20 {
		t.Fatalf("o ficheiro tem %d vectores: o teste nao comparava nada", len(vectores))
	}
	tentativas := map[int]bool{}
	for _, v := range vectores {
		tentativas[v.Tentativa] = true
		if got := idDaTentativa(v.Plano, v.NodeID, v.Tentativa); got != v.ID {
			t.Errorf("(%q, %q, %d): o aos-orq compoe %q e o no %q", v.Plano, v.NodeID, v.Tentativa, v.ID, got)
		}
		// E o vínculo com esse id passa a FORMA que o nó exige — a mesma função que o `POST /runs` usa.
		vinculo := vinculoAoPedido{RunID: v.Plano, Geracao: 1, PlanID: "plano", NodeID: v.NodeID}
		if v.Tentativa >= 2 {
			vinculo.Attempt = v.Tentativa
			if err := formaDaTentativa(vinculo, v.ID); err != nil {
				t.Errorf("(%q, %q, %d): o id do aos-orq nao passa a forma da tentativa: %v", v.Plano, v.NodeID, v.Tentativa, err)
			}
		}
	}
	if !tentativas[1] || !tentativas[2] || !tentativas[3] {
		t.Fatalf("os vectores cobrem as tres tentativas; cobrem %v", tentativas)
	}
}

// aos502CorpoDoAosOrq prepara um corpo do `aos-orq` para o nó real: os mesmos bytes, trocando SÓ
// o que não se fixa num ficheiro — a credencial e o principal. O vínculo `plan_request` FICA: é
// por ele que o nó deriva o submissor e prova a tentativa.
func aos502CorpoDoAosOrq(t *testing.T, n *aos502No, cru []byte) (map[string]json.RawMessage, string) {
	t.Helper()
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cru, &campos); err != nil {
		t.Fatalf("o corpo do aos-orq nao e JSON: %v", err)
	}
	campos["credential"], _ = json.Marshal(n.tok)
	campos["principal_nhi"], _ = json.Marshal(durAgent)
	var runID string
	if err := json.Unmarshal(campos["run_id"], &runID); err != nil {
		t.Fatal(err)
	}
	return campos, runID
}

// aos502Entregar entrega ao nó real o corpo do `aos-orq` como o drenador, espera o run e devolve
// o `GET /runs/{id}`.
func aos502Entregar(t *testing.T, n *aos502No, nome string) ([]byte, aos494Resposta) {
	t.Helper()
	campos, runID := aos502CorpoDoAosOrq(t, n, aos502LerDoAosOrq(t, nome))
	if r := postReq(n.h, "/runs", campos, govHeaders()); r.Code != http.StatusCreated {
		t.Fatalf("POST /runs com o corpo %s do aos-orq: %d (%s)", nome, r.Code, r.Body.String())
	}
	n.esperar(t, runID)
	return n.ler(t, runID)
}

// TestAOS502_Fio_TentativasDoAosOrq CONSOME os corpos do `POST /runs` que o `aos-orq` real enviou
// para as três tentativas do nó `read_notes`, e GERA as respostas que ele lê.
func TestAOS502_Fio_TentativasDoAosOrq(t *testing.T) {
	producao := aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"]
	const corpo1, corpo2, corpo3 = "post-runs-read_notes", "post-runs-read_notes-tentativa-2", "post-runs-read_notes-tentativa-3"

	// A FORMA, lida dos bytes do `aos-orq`: é isto que o teste fixa do lado de quem consome.
	for nome, tentativa := range map[string]int{corpo1: 1, corpo2: 2, corpo3: 3} {
		var corpo struct {
			RunID       string           `json:"run_id"`
			Tools       []string         `json:"tools"`
			Contrato    []string         `json:"completion_requires"`
			PlanRequest *vinculoAoPedido `json:"plan_request"`
		}
		if err := json.Unmarshal(aos502LerDoAosOrq(t, nome), &corpo); err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		v := corpo.PlanRequest
		if v == nil || v.RunID != aos502Plano || v.NodeID != aos502No_ || v.PlanID == "" || corpo.RunID != idDaTentativa(aos502Plano, aos502No_, tentativa) {
			t.Fatalf("%s: o corpo leva o vinculo com o plano e o no, e o id da tentativa %d; veio %+v run_id=%s", nome, tentativa, v, corpo.RunID)
		}
		if (tentativa == 1 && v.Attempt != 0) || (tentativa > 1 && v.Attempt != tentativa) {
			t.Fatalf("%s: attempt so a partir da segunda tentativa; veio %d", nome, v.Attempt)
		}
		if len(corpo.Tools) != 1 || corpo.Tools[0] != aos502Leitura || len(corpo.Contrato) != 1 || corpo.Contrato[0] != aos502Leitura {
			t.Fatalf("%s: a forma de producao e tools=[doc_read] e completion_requires=[doc_read]; veio %v %v", nome, corpo.Tools, corpo.Contrato)
		}
	}

	// BYTE A BYTE, e sem lhe tocar: o corpo da tentativa atravessa o decoder estrito do nó e as
	// recusas de pedido, e pára na 403 do vínculo (este nó não autentica quem chama). Um nó
	// ANTERIOR ao AOS-502 parava antes, com 400: o decoder dele não conhece `attempt`.
	t.Run("ByteAByte_PassaODecoderEAsRecusasDePedido", func(t *testing.T) {
		n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
		for _, nome := range []string{corpo2, corpo3} {
			req := httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(aos502LerDoAosOrq(t, nome)))
			if rec := aos494Servir(n.h, req); rec.Code != http.StatusForbidden {
				t.Fatalf("%s, byte a byte, tinha de passar o decoder e parar no vinculo (403); veio %d (%s)", nome, rec.Code, rec.Body.String())
			}
		}
	})

	// FALHA, FALHA, SUCESSO — as três tentativas do `aos-orq` no nó real, com a prova a correr.
	t.Run("FalhaFalhaSucesso", func(t *testing.T) {
		n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
		if ger := n.pedir(t, aos502Plano); ger != 1 {
			t.Fatalf("o corpo do aos-orq nomeia a geracao 1; a reclamacao deu %d", ger)
		}
		n.modeloNaoChama(producao, "stop")
		cru1, r1 := aos502Entregar(t, n, corpo1)
		if r1.Status != "failed" || r1.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || r1.Verdict == nil || r1.Verdict.ToolCallsRequested != 0 || r1.FinalText != "" {
			t.Fatalf("a primeira tentativa, com a resposta de producao: failed por contract_unmet_no_call, sem tool calls e sem texto; veio %+v", r1)
		}
		aos502Fio(t, "tentativa-1-falhada", cru1)
		cru2, r2 := aos502Entregar(t, n, corpo2)
		if r2.Status != "failed" || r2.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || r2.Verdict.ToolCallsRequested != 0 {
			t.Fatalf("a segunda tentativa volta a falhar do mesmo modo; veio %+v", r2)
		}
		aos502Fio(t, "tentativa-2-falhada", cru2)
		n.modeloChama(aos502Leitura)
		cru3, r3 := aos502Entregar(t, n, corpo3)
		if r3.Status != "completed" || !r3.Terminated || r3.FinalText == "" || !r3.Verdict.Fulfilled || *n.execs[aos502Leitura] != 1 {
			t.Fatalf("a terceira tentativa chama a tool e conclui; veio %+v", r3)
		}
		aos502Fio(t, "tentativa-3-recuperada", cru3)
		if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_admitted_total", 2) {
			t.Fatalf("o no admitiu as duas tentativas a mais do aos-orq:\n%s", aos502SoRetry(m))
		}
		o3, _ := n.origemDe(t, idDaTentativa(aos502Plano, aos502No_, 3))
		if o3.Attempt != 3 || o3.RetryOf != idDaTentativa(aos502Plano, aos502No_, 2) {
			t.Fatalf("a origem da terceira tentativa: %+v", o3)
		}
	})

	// FALHA TRÊS VEZES — a terceira resposta falhada, para o `aos-orq` esgotar as tentativas.
	t.Run("FalhaTresVezes", func(t *testing.T) {
		n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
		n.pedir(t, aos502Plano)
		n.modeloNaoChama(producao, "stop")
		cru1, _ := aos502Entregar(t, n, corpo1)
		aos502Fio(t, "tentativa-1-falhada", cru1)
		cru2, _ := aos502Entregar(t, n, corpo2)
		aos502Fio(t, "tentativa-2-falhada", cru2)
		cru3, r3 := aos502Entregar(t, n, corpo3)
		if r3.Status != "failed" || r3.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || r3.Verdict.ToolCallsRequested != 0 {
			t.Fatalf("a terceira tentativa volta a falhar; veio %+v", r3)
		}
		aos502Fio(t, "tentativa-3-falhada", cru3)
		if *n.execs[aos502Leitura] != 0 {
			t.Fatal("em tres tentativas falhadas a tool nunca correu")
		}
	})

	// O MESMO CORPO DA TENTATIVA CONTRA UM NÓ COM O TECTO A ZERO: recusado, e a tool não corre.
	t.Run("TectoAZero_Recusa", func(t *testing.T) {
		n := aos502Compor(t, agentruntime.CompletionEnforce, 0)
		n.pedir(t, aos502Plano)
		n.modeloNaoChama(producao, "stop")
		aos502Entregar(t, n, corpo1)
		n.modeloChama(aos502Leitura)
		campos, runID := aos502CorpoDoAosOrq(t, n, aos502LerDoAosOrq(t, corpo2))
		if r := postReq(n.h, "/runs", campos, govHeaders()); r.Code != http.StatusForbidden {
			t.Fatalf("com o tecto a zero o corpo da tentativa e recusado (403); veio %d %s", r.Code, r.Body.String())
		}
		if n.existe(t, runID) || *n.execs[aos502Leitura] != 0 {
			t.Fatal("a tentativa recusada nao hospeda nem corre nada")
		}
	})
}
