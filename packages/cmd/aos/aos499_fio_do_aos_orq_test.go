package main

// AOS-499 — O FIO ENTRE O `aos-orq` EM OBSERVAÇÃO E O NÓ REAL, NA FORMA DE PRODUÇÃO.
//
// O corpo do `POST /runs` que o `aos-orq consume` real envia a um nó candidato — com a
// declaração de origem e o vínculo «só medição» — é GERADO pelo teste do `aos-orq`
// (`../aos-orq/testdata/aos499_fio/`) e CONSUMIDO aqui, byte a byte, pelo nó real. As respostas
// do nó a esse corpo ficam em `testdata/aos498_fio/producao-*.json`, e são as que o teste do
// `aos-orq` lê. Regenera-se com `AOS494_ACTUALIZAR_FIO=1`, pela ordem do AOS-494: o nó (o
// anúncio), o `aos-orq` (o corpo), e o nó outra vez (as respostas).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ---------------------------------------------------------------------------------------------
// A forma de produção: nó `read_notes`, tool `doc_read`, saída `record`
// ---------------------------------------------------------------------------------------------

// aos499FioDoAosOrq é a pasta dos ficheiros que o teste do `aos-orq` GERA para este ticket.
var aos499FioDoAosOrq = filepath.Join("..", "aos-orq", "testdata", "aos499_fio")

// aos499NoDeProducaoSoberano é o [aos494NoDeProducao] com o gate soberano de leitura composto —
// sem ele o nó não abre os bytes designados para ninguém.
func aos499NoDeProducaoSoberano(t *testing.T, modo agentruntime.CompletionMode) *aos486No {
	t.Helper()
	ordem, extra := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec
	aos486OrdemDoFicheiro = []string{"doc_read"}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	t.Cleanup(func() { aos486OrdemDoFicheiro, aos486CamposExtraDaSpec = ordem, extra })
	return aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.CompletionVerdict = modo
		cfg.BoardRegions = map[string]string{govBoard: govRegion}
	})
}

// TestAOS499_Fio_PostRunsDoAosOrq CONSOME o corpo do `POST /runs` que o `aos-orq` REAL enviou em
// modo `observe` para o nó `read_notes` de um plano na forma de produção — com
// `output_from_tool:"doc_read"` e `output_source_binding:"measure"`.
//
//  1. BYTE A BYTE: o corpo atravessa o decoder estrito do nó e todas as recusas de pedido.
//  2. O RUN: os mesmos bytes, trocando só o que não se fixa num ficheiro (credencial, principal)
//     e tirando o vínculo `plan_request`. O `GET /runs/{id}` devolve a âncora `designated` e os
//     bytes que `doc_read` devolveu, diferentes do texto final do modelo. As respostas ficam em
//     `testdata/aos498_fio/producao-*.json`, que o `aos-orq` consome.
func TestAOS499_Fio_PostRunsDoAosOrq(t *testing.T) {
	cru, err := os.ReadFile(filepath.Join(aos499FioDoAosOrq, "post-runs-read_notes.json"))
	if err != nil {
		if aos494Actualizar() {
			t.Skipf("o corpo do POST /runs ainda nao foi gerado (%v): corre primeiro o teste do aos-orq com AOS494_ACTUALIZAR_FIO=1, e este outra vez", err)
		}
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o no aos-orq com AOS494_ACTUALIZAR_FIO=1", err)
	}
	cru = bytes.TrimSpace(cru)
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cru, &campos); err != nil {
		t.Fatalf("o corpo do aos-orq nao e JSON: %v", err)
	}
	for campo, quer := range map[string]string{
		"tools": `["doc_read"]`, "completion_requires": `["doc_read"]`,
		"output_from_tool": `"doc_read"`, "output_source_binding": `"measure"`,
	} {
		if string(campos[campo]) != quer {
			t.Fatalf("o corpo do aos-orq tem %s = %s; a forma de producao em observe e %s", campo, campos[campo], quer)
		}
	}
	var runID string
	if err := json.Unmarshal(campos["run_id"], &runID); err != nil || !strings.HasSuffix(runID, "~read_notes") {
		t.Fatalf("o run_id do corpo tem de ser o do no read_notes; veio %s (%v)", campos["run_id"], err)
	}

	t.Run("ByteAByte_PassaODecoderEAsRecusasDePedido", func(t *testing.T) {
		n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
		rec := aos494Servir(n.h, httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(cru)))
		// 403 é a recusa do VÍNCULO `plan_request` (este nó não autentica quem chama): vem depois
		// do decoder, da lista-branca, do contrato e da origem.
		if rec.Code != http.StatusForbidden {
			t.Fatalf("o corpo do aos-orq, byte a byte, tinha de passar todas as recusas de PEDIDO e parar na autenticacao (403); veio %d (%s)", rec.Code, rec.Body.String())
		}
	})

	const (
		documento = "conteudo do documento" // o que a `doc_read` do nó de teste devolve
		textoBom  = "feito"                 // o que o modelo falso escreve depois da leitura
	)
	producao := aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"]
	for _, c := range []struct {
		nome  string
		modo  agentruntime.CompletionMode
		texto string // vazio ⇒ o modelo pede `doc_read` e depois conclui
	}{
		{"producao-designada-enforce", agentruntime.CompletionEnforce, ""},
		{"producao-designada-observe", agentruntime.CompletionObserve, ""},
		{"producao-em-falta-observe", agentruntime.CompletionObserve, producao},
	} {
		t.Run(c.nome, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			n := aos499NoDeProducaoSoberano(t, c.modo)
			if c.texto == "" {
				n.upstream.pede = "doc_read"
			} else {
				conteudo, _ := json.Marshal(c.texto)
				n.upstream.responde = func(bool, string) []byte {
					return []byte(`{"id":"cmpl-2","object":"chat.completion","model":"gpt-4o",` +
						`"choices":[{"index":0,"message":{"role":"assistant","content":` + string(conteudo) + `},"finish_reason":"stop"}],` +
						`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
				}
			}
			corpo := map[string]json.RawMessage{}
			for k, v := range campos {
				corpo[k] = v
			}
			corpo["credential"], _ = json.Marshal(n.tok)
			corpo["principal_nhi"], _ = json.Marshal(durAgent)
			delete(corpo, "plan_request")
			if rec := postReq(n.h, "/runs", corpo, govHeaders()); rec.Code != http.StatusCreated {
				t.Fatalf("POST /runs com o corpo do aos-orq: %d (%s)", rec.Code, rec.Body.String())
			}
			wc, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, ok, werr := n.svc.Wait(wc, runID); werr != nil || !ok {
				t.Fatalf("o run devia ter sido hospedado e acabado: ok=%v err=%v", ok, werr)
			}
			get := getReq(n.h, "/runs/"+runID, govHeaders())
			if get.Code != http.StatusOK {
				t.Fatalf("GET /runs/%s: %d (%s)", runID, get.Code, get.Body.String())
			}
			var r aos498Resposta
			if err := json.Unmarshal(get.Body.Bytes(), &r); err != nil {
				t.Fatalf("resposta ilegivel: %v", err)
			}
			execs := atomic.LoadInt64(n.execs["doc_read"])
			if c.texto == "" {
				if r.Status != "completed" || !r.Terminated || r.FinalText != textoBom || execs != 1 {
					t.Fatalf("o run de leitura conclui com o texto do modelo; veio %s (execs=%d)", get.Body.String(), execs)
				}
				aos498ExigirDesignada(t, r.OutputSource, "doc_read", "measure", []byte(documento))
				if r.Output == nil || *r.Output != documento || *r.Output == r.FinalText {
					t.Fatalf("output tem de ser o que doc_read devolveu (%q), e nao o texto final (%q); veio %v", documento, r.FinalText, r.Output)
				}
			} else {
				// A resposta de produção de 2026-10-04: a tool call escrita como texto. Em
				// observação o run conclui, e a âncora diz que não houve origem.
				quer := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceMeasure, State: agentruntime.OutputSourceMissing}
				if r.Status != "completed" || !r.Terminated || r.FinalText != c.texto || execs != 0 || !reflect.DeepEqual(r.OutputSource, quer) || r.Output != nil {
					t.Fatalf("a resposta de producao em observe: completed, com o texto, ancora missing e sem bytes; veio %s (execs=%d)", get.Body.String(), execs)
				}
			}
			aos498Fio(t, c.nome, get.Body.Bytes())
		})
	}
}
