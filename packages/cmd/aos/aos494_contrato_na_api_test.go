package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"

	pdp "github.com/aos-ref/control-plane/pdp"
)

// AOS-494 — O CONTRATO DE CONCLUSÃO NA API DO NÓ.
//
// O que este ficheiro prova, sempre pela API HTTP do nó composto (cadeia de mediação real,
// execução durável, cifra por-titular) e com um modelo falso:
//
//   - o `POST /runs` aceita `completion_requires`, e recusa com 400 e mensagem própria um
//     contrato que não cabe na lista-branca do mesmo pedido;
//   - o `GET /tools` anuncia o suporte, com o modo do nó;
//   - o `GET /runs/{id}` devolve a razão e o vector, e um run não cumprido responde `failed`,
//     `terminated=false`, sem texto (o M7 da revisão do AOS-493);
//   - depois de um REINÍCIO do nó, o ramo durável devolve o mesmo desfecho, a mesma razão e a
//     mesma saída que o ramo em memória.
//
// As respostas do `GET` em memória ficam em `testdata/aos494_fio/`. O `aos-orq` não pode
// importar este pacote: os testes dele servem ESTES ficheiros a partir de um nó falso, e é por
// eles que os dois lados do fio ficam presos um ao outro.

const (
	aos494RunID     = "aos494-fio"
	aos494Tool      = "counter"
	aos494Documento = "conteudo do documento notes"
	aos494Boa       = "O documento notes diz: " + aos494Documento
)

// aos494Resposta é o `GET /runs/{id}` como um cliente o lê.
type aos494Resposta struct {
	RunID             string                `json:"run_id"`
	Status            string                `json:"status"`
	Terminated        bool                  `json:"terminated"`
	Error             string                `json:"error"`
	FinalText         string                `json:"final_text"`
	OutcomeReason     string                `json:"outcome_reason"`
	Verdict           *agentruntime.Verdict `json:"verdict"`
	OutputUnavailable bool                  `json:"output_unavailable"`
}

// aos494Modelo devolve o modelo falso de um caso. `texto` vazio ⇒ a resposta BOA: o turno 1
// pede a tool, o turno 2 conclui. Senão, um só turno com esse texto e nenhuma tool call — as
// respostas de produção de 2026-10-04.
func aos494Modelo(texto string) agentruntime.ModelClient {
	var turnos int32
	return agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		uso := agentruntime.Usage{InputTokens: 9, OutputTokens: 4}
		if texto != "" {
			return agentruntime.ModelResponse{Text: texto, Final: true, StopReason: agentruntime.StopStop, Usage: uso}, nil
		}
		if atomic.AddInt32(&turnos, 1) == 1 {
			return agentruntime.ModelResponse{
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: aos494Tool, Capability: durCap, ResourceRegion: govRegion, Input: []byte("notes")}},
				Usage:     uso,
			}, nil
		}
		return agentruntime.ModelResponse{Text: aos494Boa, Final: true, StopReason: agentruntime.StopStop, Usage: uso}, nil
	})
}

// aos494Compor levanta UMA incarnação do nó sobre a pasta `dir` e o cofre `vault`. Duas chamadas
// com a mesma pasta e o mesmo cofre são o nó antes e depois de um reinício: o Event Store e o
// WORM estão em ficheiro, e tudo o que vive em memória (o registo de desfechos) nasce vazio.
func aos494Compor(t *testing.T, dir string, vault audit.KeyVault, modo agentruntime.CompletionMode, modelo agentruntime.ModelClient, soberano bool) (*Node, *NodeService, http.Handler) {
	t.Helper()
	ctx := context.Background()
	signer := durSigner(t)
	entry := counterEntry(t, signer)

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.DSARVault = vault
	cfg.Model = modelo
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entry)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.CompletionVerdict = modo
	if soberano {
		cfg.BoardRegions = map[string]string{govBoard: govRegion}
	}
	var err error
	if cfg.PDP, err = pdp.Open(pdpPoliciesDir); err != nil {
		t.Fatalf("abrir o bundle de politica de referencia: %v", err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := node.Runtime.Register(aos494Tool, func(context.Context, []byte) ([]byte, error) {
		return []byte(aos494Documento), nil
	}); err != nil {
		_ = node.Close()
		t.Fatalf("Register(%s): %v", aos494Tool, err)
	}
	svc, h := newAPI(t, node)
	return node, svc, h
}

// aos494Fechar pára o serviço e fecha o nó: o «desligar» de um reinício.
func aos494Fechar(t *testing.T, node *Node, svc *NodeService) {
	t.Helper()
	sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.Shutdown(sc); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := node.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// aos494Submeter submete o run de um nó de plano, com o corpo que o `aos-orq` envia, e espera
// que ele acabe. `contrato` nil ⇒ o corpo de um `aos-orq` anterior ao AOS-495, sem o campo.
func aos494Submeter(t *testing.T, node *Node, svc *NodeService, h http.Handler, cabecalhos map[string]string, contrato []string) {
	t.Helper()
	ctx := context.Background()
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	corpo := map[string]any{
		"run_id":        aos494RunID,
		"objective":     "Le o documento notes e devolve o conteudo",
		"principal_nhi": durAgent,
		"credential":    tok.Compact,
		"tools":         []string{aos494Tool},
		"inputs":        []map[string]string{},
	}
	if contrato != nil {
		corpo["completion_requires"] = contrato
	}
	rec := postReq(h, "/runs", corpo, cabecalhos)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	wc, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(wc, aos494RunID); werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado e acabado: ok=%v err=%v", ok, werr)
	}
}

// aos494Ler faz o `GET /runs/{id}` e devolve os bytes e a resposta descodificada.
func aos494Ler(t *testing.T, h http.Handler, cabecalhos map[string]string) ([]byte, aos494Resposta) {
	t.Helper()
	rec := getReq(h, "/runs/"+aos494RunID, cabecalhos)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runs/%s devia dar 200, veio %d (%s)", aos494RunID, rec.Code, rec.Body.String())
	}
	var r aos494Resposta
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("resposta ilegivel: %v (%s)", err, rec.Body.String())
	}
	return rec.Body.Bytes(), r
}

// aos494Fio compara a resposta em memória com o ficheiro do fio que o `aos-orq` consome nos
// testes dele. `AOS494_ACTUALIZAR_FIO=1` reescreve-o.
func aos494Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos494_fio", nome+".json")
	cru = bytes.TrimSpace(cru)
	if os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
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
		t.Fatalf("o GET /runs/{id} deixou de responder o que o aos-orq le nos testes dele (%s):\n  fio:   %s\n  agora: %s", caminho, bytes.TrimSpace(quer), cru)
	}
}

// TestAOS494_Fio_DesfechoRazaoESaidaNosDoisRamos percorre os casos do fio entre o `aos-orq` e o
// nó: as duas respostas de produção de 2026-10-04 e a resposta boa, com o nó em `enforce` e em
// `observe`. Em cada um lê o desfecho em memória, REINICIA o nó, e exige ao ramo durável o mesmo
// estado, a mesma razão, o mesmo vector e a mesma saída.
func TestAOS494_Fio_DesfechoRazaoESaidaNosDoisRamos(t *testing.T) {
	producao := aos493RespostasDeProducao(t)
	type caso struct {
		nome     string
		modo     agentruntime.CompletionMode
		texto    string // vazio ⇒ a resposta boa
		contrato []string
	}
	casos := []caso{
		{nome: "boa-enforce", modo: agentruntime.CompletionEnforce, contrato: []string{aos494Tool}},
		{nome: "boa-observe", modo: agentruntime.CompletionObserve, contrato: []string{aos494Tool}},
	}
	for plano, texto := range producao {
		casos = append(casos,
			caso{nome: plano + "-enforce", modo: agentruntime.CompletionEnforce, texto: texto, contrato: []string{aos494Tool}},
			caso{nome: plano + "-observe", modo: agentruntime.CompletionObserve, texto: texto, contrato: []string{aos494Tool}},
		)
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			dir := t.TempDir()
			vault := audit.NewInMemoryKeyVault(nil)

			node, svc, h := aos494Compor(t, dir, vault, c.modo, aos494Modelo(c.texto), true)
			aos494Submeter(t, node, svc, h, govHeaders(), c.contrato)
			cru, mem := aos494Ler(t, h, govHeaders())
			aos494Fio(t, c.nome, cru)

			negativo := c.texto != ""
			imposto := c.modo == agentruntime.CompletionEnforce
			quer := &agentruntime.Verdict{Mode: c.modo, Fulfilled: !negativo, Tools: []agentruntime.ToolEvidence{{Tool: aos494Tool}}}
			if negativo {
				quer.Reason = agentruntime.OutcomeContractNoCall
			} else {
				quer.ToolCallsRequested = 1
				quer.Tools[0] = agentruntime.ToolEvidence{Tool: aos494Tool, Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective}
			}
			if !reflect.DeepEqual(mem.Verdict, quer) {
				t.Fatalf("vector do veredicto = %+v, quero %+v", mem.Verdict, quer)
			}
			switch {
			case negativo && imposto:
				// O M7 da revisão do AOS-493: respondia `completed`.
				if mem.Status != "failed" || mem.Terminated || mem.FinalText != "" || mem.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("um run failed por contrato responde failed, terminated=false, com a razao e sem texto; veio %+v", mem)
				}
			case negativo:
				// Em observação o desfecho é o de sempre, e a razão vai ao lado.
				if mem.Status != "completed" || !mem.Terminated || mem.FinalText != c.texto || mem.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("em observacao o run conclui com o texto e leva a razao observada; veio %+v", mem)
				}
			default:
				if mem.Status != "completed" || !mem.Terminated || mem.FinalText != aos494Boa || mem.OutcomeReason != "" {
					t.Fatalf("a resposta boa conclui com o texto e sem razao; veio %+v", mem)
				}
			}

			// REINÍCIO: fecha-se o nó e levanta-se outro sobre os mesmos ficheiros. O registo de
			// desfechos em memória nasce vazio; o que o GET responder vem do log.
			aos494Fechar(t, node, svc)
			node2, svc2, h2 := aos494Compor(t, dir, vault, c.modo, aos494Modelo(c.texto), true)
			defer aos494Fechar(t, node2, svc2)
			if _, emMemoria := svc2.Outcome(aos494RunID); emMemoria {
				t.Fatal("depois do reinicio o desfecho nao pode estar em memoria: o teste nao exercitava o ramo duravel")
			}
			_, dur := aos494Ler(t, h2, govHeaders())
			if dur.OutputUnavailable {
				t.Fatalf("o ramo duravel tinha de ler a saida do log; veio output_unavailable (%+v)", dur)
			}
			if !reflect.DeepEqual(dur, mem) {
				t.Fatalf("o ramo duravel nao responde o mesmo que o ramo em memoria:\n  memoria: %+v\n  duravel: %+v", mem, dur)
			}
		})
	}
}

// TestAOS494_RamoDuravelSemGateNaoServeASaida: num nó sem o gate soberano de leitura o ramo
// durável não decifra a saída para um chamador que ninguém autenticou. Responde `completed`
// com `output_unavailable`, e não um `completed` sem texto que se confunde com um run mudo.
func TestAOS494_RamoDuravelSemGateNaoServeASaida(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)

	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos494Modelo(""), false)
	aos494Submeter(t, node, svc, h, nil, []string{aos494Tool})
	_, mem := aos494Ler(t, h, nil)
	if mem.Status != "completed" || !mem.Terminated || mem.FinalText != aos494Boa || mem.OutputUnavailable {
		t.Fatalf("em memoria o run conclui com o texto; veio %+v", mem)
	}
	aos494Fechar(t, node, svc)

	node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos494Modelo(""), false)
	defer aos494Fechar(t, node2, svc2)
	_, dur := aos494Ler(t, h2, nil)
	if dur.Status != "completed" || !dur.Terminated || dur.FinalText != "" || !dur.OutputUnavailable {
		t.Fatalf("sem gate o ramo duravel responde completed, sem texto e com output_unavailable; veio %+v", dur)
	}
	// O veredicto não é conteúdo de titular: sai na mesma.
	if !reflect.DeepEqual(dur.Verdict, mem.Verdict) || dur.Verdict == nil {
		t.Fatalf("o vector tem de sair nos dois ramos: memoria=%+v duravel=%+v", mem.Verdict, dur.Verdict)
	}
}

// TestAOS494_AosOrqAnterior_ContinuaAFuncionar: o corpo de um `aos-orq` anterior ao AOS-495 (sem
// `completion_requires`) é aceite, e o desfecho lê-se pelos campos de sempre com a regra de
// sempre — `completed`, `terminated`, sem erro. Vale com o nó em `enforce`: sem contrato, a
// resposta de produção tem texto e não foi cortada, pelo que conclui como hoje.
func TestAOS494_AosOrqAnterior_ContinuaAFuncionar(t *testing.T) {
	// estadoAnterior é o `estadoDoRun` do `aos-orq` antes deste ticket, e `concluiu` a regra dele.
	type estadoAnterior struct {
		RunID      string `json:"run_id"`
		Status     string `json:"status"`
		Terminated bool   `json:"terminated,omitempty"`
		Error      string `json:"error,omitempty"`
		FinalText  string `json:"final_text,omitempty"`
	}
	concluiu := func(e estadoAnterior) bool { return e.Status == "completed" && e.Terminated && e.Error == "" }

	for plano, texto := range aos493RespostasDeProducao(t) {
		for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionEnforce, agentruntime.CompletionObserve} {
			t.Run(plano+"-"+string(modo), func(t *testing.T) {
				pinBreakerEnv(t, "0", "0", "0", "0")
				node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), modo, aos494Modelo(texto), true)
				defer aos494Fechar(t, node, svc)
				aos494Submeter(t, node, svc, h, govHeaders(), nil)
				cru, _ := aos494Ler(t, h, govHeaders())
				var e estadoAnterior
				if err := json.Unmarshal(cru, &e); err != nil {
					t.Fatalf("um cliente anterior tem de conseguir ler a resposta: %v", err)
				}
				if !concluiu(e) || e.FinalText != texto {
					t.Fatalf("sem contrato o run conclui como hoje, com o texto; veio %+v", e)
				}
			})
		}
	}
}

// TestAOS494_PostRuns_ContratoContraAListaBranca: as recusas do contrato na porta, cada uma com
// 400 e a sua mensagem; e o modo, que não tem campo no corpo.
func TestAOS494_PostRuns_ContratoContraAListaBranca(t *testing.T) {
	node, _ := newAPINode(t, nil, false)
	_, h := newAPI(t, node)
	base := func() map[string]any {
		return map[string]any{"run_id": "aos494-porta", "objective": "ler", "principal_nhi": "nhi:agent-1"}
	}
	for _, c := range []struct {
		nome string
		muda func(map[string]any)
		quer string
	}{
		{"tool fora da lista-branca", func(m map[string]any) {
			m["tools"] = []string{"doc_read"}
			m["completion_requires"] = []string{"doc_write"}
		}, erroContratoForaDaLista},
		{"lista-branca vazia", func(m map[string]any) {
			m["tools"] = []string{}
			m["completion_requires"] = []string{"doc_read"}
		}, erroContratoForaDaLista},
		{"nome com outra caixa nao e a tool", func(m map[string]any) {
			m["tools"] = []string{"doc_read"}
			m["completion_requires"] = []string{"Doc_Read"}
		}, erroContratoForaDaLista},
		{"uma dentro e uma fora", func(m map[string]any) {
			m["tools"] = []string{"doc_read"}
			m["completion_requires"] = []string{"doc_read", "doc_write"}
		}, erroContratoForaDaLista},
		{"sem lista-branca", func(m map[string]any) {
			m["completion_requires"] = []string{"doc_read"}
		}, erroContratoSemListaBranca},
		{"entrada vazia", func(m map[string]any) {
			m["tools"] = []string{"doc_read"}
			m["completion_requires"] = []string{"doc_read", ""}
		}, erroContratoComEntradaVazia},
		{"o modo nao vem do corpo", func(m map[string]any) {
			m["tools"] = []string{"doc_read"}
			m["completion_requires"] = []string{"doc_read"}
			m["completion_mode"] = "off"
		}, "corpo invalido"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			corpo := base()
			c.muda(corpo)
			rec := postJSON(h, http.MethodPost, "/runs", corpo)
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			if rec.Code != http.StatusBadRequest || e.Error != c.quer {
				t.Fatalf("queria 400 %q; veio %d %q", c.quer, rec.Code, e.Error)
			}
		})
	}
	// Um contrato vazio é a ausência dele: aceite, com ou sem lista-branca.
	corpo := base()
	corpo["completion_requires"] = []string{}
	if rec := postJSON(h, http.MethodPost, "/runs", corpo); rec.Code != http.StatusCreated {
		t.Fatalf("um contrato vazio nao e contrato: queria 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAOS494_ContratoImpossivel_RespondeFailed: uma tool que está na lista-branca do pedido mas
// que o nó não oferece passa a porta (o tool set do run só se congela no arranque do run) e é
// recusada pelo kernel antes do primeiro turno. O `GET` responde `failed`, e não `completed`.
func TestAOS494_ContratoImpossivel_RespondeFailed(t *testing.T) {
	var chamadas int32
	modelo := agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		atomic.AddInt32(&chamadas, 1)
		return agentruntime.ModelResponse{Text: "nao devia ter sido interrogado", Final: true}, nil
	})
	node, _ := newAPINode(t, modelo, false)
	svc, h := newAPI(t, node)
	rec := postJSON(h, http.MethodPost, "/runs", map[string]any{
		"run_id": "aos494-impossivel", "objective": "ler", "principal_nhi": "nhi:agent-1",
		"tools": []string{"doc_read"}, "completion_requires": []string{"doc_read"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("a porta nao conhece o tool set do no: queria 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	wc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(wc, "aos494-impossivel"); err != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, err)
	}
	var r aos494Resposta
	get := postJSON(h, http.MethodGet, "/runs/aos494-impossivel", nil)
	if err := json.Unmarshal(get.Body.Bytes(), &r); err != nil || get.Code != http.StatusOK {
		t.Fatalf("GET: %d %v (%s)", get.Code, err, get.Body.String())
	}
	if r.Status != "failed" || r.Terminated || r.FinalText != "" || !strings.Contains(r.Error, "contrato de conclusao impossivel") {
		t.Fatalf("o contrato impossivel responde failed, com o erro do kernel e sem texto; veio %+v", r)
	}
	if n := atomic.LoadInt32(&chamadas); n != 0 {
		t.Fatalf("o modelo foi interrogado %d vez(es) num run que o kernel recusou a partida", n)
	}
}

// TestAOS494_GetTools_AnunciaOContrato: o `GET /tools` diz que o nó aceita o contrato, com o
// modo em vigor, e continua a servir `tools` como antes.
func TestAOS494_GetTools_AnunciaOContrato(t *testing.T) {
	for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce, agentruntime.CompletionOff} {
		t.Run(string(modo), func(t *testing.T) {
			cfg := tnBaseConfig()
			cfg.CompletionVerdict = modo
			node, err := Bootstrap(context.Background(), cfg, io.Discard)
			if err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			t.Cleanup(func() { _ = node.Close() })
			_, h := newAPI(t, node)
			rec := postJSON(h, http.MethodGet, "/tools", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /tools: %d (%s)", rec.Code, rec.Body.String())
			}
			var corpo struct {
				Tools              *[]entradaDoCatalogo `json:"tools"`
				CompletionContract *struct {
					Mode string `json:"mode"`
				} `json:"completion_contract"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &corpo); err != nil {
				t.Fatalf("resposta ilegivel: %v", err)
			}
			if corpo.Tools == nil {
				t.Fatal("`tools` tem de continuar presente: um cliente anterior recusa a resposta sem ele")
			}
			if corpo.CompletionContract == nil || corpo.CompletionContract.Mode != string(modo) {
				t.Fatalf("o no tem de anunciar o contrato com o modo %q; veio %s", modo, rec.Body.String())
			}
		})
	}
}
