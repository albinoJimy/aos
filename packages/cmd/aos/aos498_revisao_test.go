package main

// AOS-498 — O QUE A REVISÃO ADVERSARIAL DE 2026-10-05 DEIXOU PRESO POR TESTE.
//
// A revisão não encontrou bloqueantes, mas mostrou que várias propriedades do HEAD estavam certas
// sem que teste nenhum as fixasse — mutações que as partiam sobreviviam à suite inteira. Cada
// teste deste ficheiro nasce de um achado, e diz qual:
//
//   - I2 (mutações R6a, R6b, R6c): `output` não sai de um run que não concluiu, pela API, nos
//     dois ramos e nos dois vínculos;
//   - M1: um registo do step-ledger ilegível e uma âncora cujo passo não forma chave são
//     DEFINITIVOS — `unavailable`, nunca `unavailable_now` nem 503 para sempre;
//   - M2: um registo em claro não sai, com ou sem a KEK do titular;
//   - R2 e R5b: a chave compara-se inteira e o digest também;
//   - R8: o tecto de transporte é inclusivo — 128 KiB exactos saem;
//   - I5: o envelope REAL da sandbox, de ponta a ponta no nó.

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
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------------------------
// I2 — um run que não concluiu não serve os bytes designados
// ---------------------------------------------------------------------------------------------

// TestAOS498_RunQueNaoConcluiu_NaoServeOsBytes é o cenário REAL em que um run falhado tem uma
// âncora `designated`: nó em imposição, contrato sobre DUAS tools, e o modelo só chama a
// designada. O kernel designa a origem (uma chamada efectiva da tool declarada) e fecha o run
// `failed` (o contrato não se cumpriu pela outra). Os bytes estão no step-ledger, conferem com a
// âncora, e NÃO podem sair: um run que não concluiu não tem saída — a regra do `final_text`.
//
// É a guarda que impede a entrega por referência (AOS-501) de entregar bytes de um run falhado.
// Nos dois ramos (em memória e, depois de um reinício, durável) e nos dois vínculos. As mutações
// R6a e R6b da revisão (`concluido` sempre verdadeiro num ramo e no outro) sobreviviam à suite.
func TestAOS498_RunQueNaoConcluiu_NaoServeOsBytes(t *testing.T) {
	for _, vinculo := range []string{"measure", "binding"} {
		t.Run(vinculo, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			dir := t.TempDir()
			vault := audit.NewInMemoryKeyVault(nil)
			modelo := func() agentruntime.ModelClient { return aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()) }
			node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true, aos498ComToolsQueNaoSeTransportam(t))
			aos498RegistarNaoTransportaveis(t, node)
			tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, durAgent, durClass, []string{durCap})
			if err != nil {
				t.Fatalf("MintForHuman: %v", err)
			}
			corpo := map[string]any{
				"run_id": aos498RunID, "objective": "Le o documento notes e devolve o conteudo",
				"principal_nhi": durAgent, "credential": tok.Compact,
				"tools": []string{aos494Tool, aos498ToolGrande}, "inputs": []map[string]string{},
				"completion_requires": []string{aos494Tool, aos498ToolGrande},
				"output_from_tool":    aos494Tool, "output_source_binding": vinculo,
			}
			if rec := postReq(h, "/runs", corpo, govHeaders()); rec.Code != http.StatusCreated {
				t.Fatalf("POST /runs: %d (%s)", rec.Code, rec.Body.String())
			}
			wc, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, ok, werr := svc.Wait(wc, aos498RunID); werr != nil || !ok {
				t.Fatalf("o run devia ter sido hospedado e acabado: ok=%v err=%v", ok, werr)
			}
			exigir := func(ramo string, node *Node, h http.Handler) {
				t.Helper()
				var aberturas int
				node.contentOpener = &aos494OpenerEspiao{dentro: node.contentOpener, aoAbrir: func() { aberturas++ }}
				cru, r := aos498Ler(t, h, govHeaders())
				if r.Status != "failed" || r.Terminated {
					t.Fatalf("%s: pre-condicao — o run tinha de fechar failed (o contrato nao se cumpriu pela outra tool); veio %s", ramo, cru)
				}
				aos498ExigirDesignada(t, r.OutputSource, aos494Tool, vinculo, []byte(aos494Documento))
				if r.Output != nil || r.OutputOmitted != "" || bytes.Contains(cru, []byte(aos494Documento)) || bytes.Contains(cru, []byte(`"output":`)) {
					t.Fatalf("%s: um run FAILED serviu os bytes designados (ou uma marca deles): %s", ramo, cru)
				}
				if r.FinalText != "" || r.OutputUnavailable {
					t.Fatalf("%s: um run failed nao tem texto final nem saida indisponivel; veio %s", ramo, cru)
				}
				if aberturas != 0 {
					t.Fatalf("%s: um run failed nao abre conteudo do titular; abriu %d vez(es)", ramo, aberturas)
				}
			}
			exigir("memoria", node, h)
			aos494Fechar(t, node, svc)
			node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true, aos498ComToolsQueNaoSeTransportam(t))
			defer aos494Fechar(t, node2, svc2)
			if _, emMemoria := svc2.Outcome(aos498RunID); emMemoria {
				t.Fatal("depois do reinicio o desfecho nao pode estar em memoria: o teste nao exercitava o ramo duravel")
			}
			exigir("duravel", node2, h2)
		})
	}
}

// TestAOS498_DesfechoEmMemoriaSemTerminated_NaoServeOsBytes fecha a outra metade da condição do
// ramo em memória (mutação R6c da revisão: `concluido` a olhar só para o rótulo `completed`).
//
// O ramo em memória rotula `completed` qualquer desfecho que não seja pausa, disjuntor ou
// incumprimento — incluindo o de um run que esgotou os turnos, que NÃO terminou. O kernel de hoje
// só sela a âncora num run que concluiu ou que não cumpriu, pelo que esse desfecho não traz
// âncora designada; mas a regra «só um run que TERMINOU tem saída» é do `GET`, e não pode
// depender de o kernel nunca vir a mudar. O teste põe na memória do serviço o desfecho que essa
// mudança produziria — o de um run real, com os bytes reais no step-ledger, sem `Terminated` — e
// exige que os bytes não saiam.
func TestAOS498_DesfechoEmMemoriaSemTerminated_NaoServeOsBytes(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce,
		aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()), true)
	defer aos494Fechar(t, node, svc)
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: "binding", contrato: true})
	if _, r := aos498Ler(t, h, govHeaders()); r.Output == nil || *r.Output != aos494Documento {
		t.Fatalf("pre-condicao: o run concluido serve os bytes designados; veio %+v", r)
	}
	svc.mu.Lock()
	svc.completed[aos498RunID].result.Terminated = false
	svc.mu.Unlock()
	cru, r := aos498Ler(t, h, govHeaders())
	if r.Status != "completed" || r.Terminated {
		t.Fatalf("pre-condicao: o desfecho em memoria tem o rotulo completed sem terminated; veio %s", cru)
	}
	if r.OutputSource == nil || r.OutputSource.State != agentruntime.OutputSourceDesignated {
		t.Fatalf("a ancora sai sempre; veio %s", cru)
	}
	if r.Output != nil || r.OutputOmitted != "" || bytes.Contains(cru, []byte(`"output":`)) {
		t.Fatalf("um run que nao TERMINOU serviu os bytes designados: %s", cru)
	}
}

// ---------------------------------------------------------------------------------------------
// M1, M2, R2, R5b — registos que não são os que a âncora designou, ou que não se lêem
// ---------------------------------------------------------------------------------------------

// aos498RegistoAMao escreve um `step.ledger.applied` sem passar pelo ledger: o que um log
// adulterado, danificado ou de outra composição tem. «Selado» é a forma do [aos498Cifra].
func aos498RegistoAMao(t *testing.T, store *eventstore.Store, stream, chave, passoDoEvento string, claro []byte, selado bool) {
	t.Helper()
	res := claro
	if selado {
		res = append([]byte("selado:"), claro...)
	}
	rec := map[string]any{"key": chave, "status": "ok", "result": res, "result_hash": strings.TrimPrefix(aos497Digest(string(claro)), "sha256:")}
	if selado {
		rec["sealed"], rec["subject"] = true, "nhi:titular"
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	aos498EventoDoLedger(t, store, stream, passoDoEvento, payload)
}

func aos498EventoDoLedger(t *testing.T, store *eventstore.Store, stream, passoDoEvento string, payload []byte) {
	t.Helper()
	if _, err := store.Append(context.Background(), stream, eventstore.EventInput{
		Type: durable.EventTypeLedgerApplied, Payload: payload, RunID: stream, StepID: passoDoEvento,
		Producer: eventstore.Producer{NHIID: "nhi:titular"},
	}); err != nil {
		t.Fatalf("Append(%s/%s): %v", stream, passoDoEvento, err)
	}
}

// TestAOS498_LedgerForjado monta à mão step-ledgers que NÃO têm, para a âncora, o registo que o
// kernel selou — ou que o têm e não se lêem — e percorre o que o `GET` responde nos dois vínculos.
//
// O que fica fixo:
//
//   - nunca saem bytes que a âncora não designou (outro run, outro stream, outro passo, um
//     duplicado posterior com outros bytes, um digest em prefixo ou vazio);
//   - o que não se lê por causa do LOG é DEFINITIVO (revisão, M1): `output_omitted:"unavailable"`
//     em `measure`, mais `output_unavailable` em `binding`, e a função responde — nunca 503;
//   - um registo em claro não sai, com a KEK do titular destruída ou inteira (revisão, M2).
func TestAOS498_LedgerForjado(t *testing.T) {
	const (
		passo = "step-000001-tool-1"
		doc   = "documento do titular"
		outro = "OUTRO documento 1234"
	)
	if len(doc) != len(outro) {
		t.Fatal("pre-condicao: os dois documentos tem o mesmo tamanho, para so o digest os distinguir")
	}
	for _, c := range []struct {
		nome   string
		monta  func(t *testing.T, s *eventstore.Store)
		ajusta func(a *agentruntime.OutputSource)
		semKEK bool
		output string // "" ⇒ não sai, e a marca é `unavailable`
	}{
		{nome: "o registo certo (controlo)", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), true)
		}, output: doc},
		{nome: "duplicado: certo, depois errado (vale o ultimo)", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), true)
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo+"-bis", []byte(outro), true)
		}},
		{nome: "duplicado: errado, depois certo", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(outro), true)
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo+"-bis", []byte(doc), true)
		}, output: doc},
		// Mutação R2: a chave compara-se inteira.
		{nome: "registo de OUTRO run no stream deste", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-B:"+passo, "ledger-"+passo, []byte(doc), true)
		}},
		{nome: "registo deste run no stream de OUTRO", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-B", "run-A:"+passo, "ledger-"+passo, []byte(doc), true)
		}},
		{nome: "outro passo do mesmo run, com os mesmos bytes", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:step-000002-tool-1", "ledger-step-000002-tool-1", []byte(doc), true)
		}},
		{nome: "o tamanho bate e o digest nao", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(outro), true)
		}},
		// Mutação R5b: o digest compara-se inteiro.
		{nome: "digest da ancora em prefixo", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), true)
		}, ajusta: func(a *agentruntime.OutputSource) { a.Digest = a.Digest[:20] }},
		{nome: "digest da ancora vazio", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), true)
		}, ajusta: func(a *agentruntime.OutputSource) { a.Digest = "" }},
		// M1: definitivos.
		{nome: "passo da ancora com ':' (nao forma chave)", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:x:"+passo, "ledger-x", []byte(doc), true)
		}, ajusta: func(a *agentruntime.OutputSource) { a.StepID = "x:" + passo }},
		{nome: "registo ilegivel NOUTRO passo do stream", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), true)
			aos498EventoDoLedger(t, s, "run-A", "ledger-partido", []byte(`{"key":{"nao":"e texto"}}`))
		}},
		// M2: em claro não sai.
		{nome: "registo em claro, com a KEK destruida", semKEK: true, monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), false)
		}},
		{nome: "registo em claro, com a KEK inteira", monta: func(t *testing.T, s *eventstore.Store) {
			aos498RegistoAMao(t, s, "run-A", "run-A:"+passo, "ledger-"+passo, []byte(doc), false)
		}},
	} {
		t.Run(c.nome, func(t *testing.T) {
			store, err := eventstore.New()
			if err != nil {
				t.Fatalf("eventstore.New: %v", err)
			}
			defer store.Close()
			c.monta(t, store)
			for _, vinculo := range []agentruntime.OutputSourceBinding{agentruntime.OutputSourceMeasure, agentruntime.OutputSourceBinds} {
				cifra := &aos498Cifra{}
				if c.semKEK {
					cifra.erro = audit.ErrDecrypt
				}
				a := &agentruntime.OutputSource{Tool: "doc_read", Binding: vinculo, State: agentruntime.OutputSourceDesignated,
					StepID: passo, Digest: aos497Digest(doc), Bytes: len(doc)}
				if c.ajusta != nil {
					c.ajusta(a)
				}
				h := &apiHandler{node: &Node{EventStore: store, contentOpener: cifra}, readGov: &readGovernance{}}
				resp := runStateResponse{RunID: "run-A", Status: "completed", Terminated: true, FinalText: "texto"}
				responde := h.origemNaResposta(httptest.NewRequest(http.MethodGet, "/runs/run-A", nil), aos498Leitor, &resp, a, true)
				if !responde {
					t.Fatalf("[%s] respondeu 503: nada disto passa com o tempo, e quem sonda ficava a espera para sempre", vinculo)
				}
				if c.output != "" {
					if resp.Output == nil || *resp.Output != c.output || resp.OutputOmitted != "" || resp.OutputUnavailable {
						t.Fatalf("[%s] quero os bytes designados; veio output=%v omitted=%q unavailable=%v", vinculo, resp.Output, resp.OutputOmitted, resp.OutputUnavailable)
					}
					continue
				}
				if resp.Output != nil {
					t.Fatalf("[%s] SERVIU BYTES QUE A ANCORA NAO DESIGNOU (ou que nao podia abrir): %q", vinculo, *resp.Output)
				}
				if resp.OutputOmitted != saidaOmitidaDeVez {
					t.Fatalf("[%s] output_omitted = %q; quero %q (definitivo)", vinculo, resp.OutputOmitted, saidaOmitidaDeVez)
				}
				if quer := vinculo == agentruntime.OutputSourceBinds; resp.OutputUnavailable != quer {
					t.Fatalf("[%s] output_unavailable = %v; quero %v", vinculo, resp.OutputUnavailable, quer)
				}
				if strings.HasPrefix(c.nome, "registo em claro") && cifra.aberturas != 0 {
					t.Fatalf("[%s] um registo em claro nao chega ao cifrador; foi chamado %d vez(es)", vinculo, cifra.aberturas)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// R8 — o tecto de transporte é inclusivo
// ---------------------------------------------------------------------------------------------

// TestAOS498_TectoExacto: um resultado designado com EXACTAMENTE o tecto de transporte (128 KiB)
// sai inteiro; o `TestAOS498_Fio_NaoTransportavel` fixa que com mais um byte já não sai. É a
// fronteira que o anúncio (`max_bytes`) promete a quem submete. A mutação R8 da revisão (`>=` em
// vez de `>`) sobrevivia.
func TestAOS498_TectoExacto(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	exacto := bytes.Repeat([]byte("a"), maxPlanInputBytes)
	node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce,
		aos498Guiao(aos498Chama(aos498ToolGrande, 1), aos498Conclui()), true, aos498ComToolsQueNaoSeTransportam(t))
	defer aos494Fechar(t, node, svc)
	for tool, valor := range map[string][]byte{aos498ToolGrande: exacto, aos498ToolBinaria: []byte("b")} {
		if err := node.Runtime.Register(tool, func(context.Context, []byte) ([]byte, error) { return valor, nil }); err != nil {
			t.Fatalf("Register(%s): %v", tool, err)
		}
	}
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{tool: aos498ToolGrande, vinculo: "binding", contrato: true})
	_, r := aos498Ler(t, h, govHeaders())
	if r.OutputSource == nil || r.OutputSource.Bytes != maxPlanInputBytes {
		t.Fatalf("pre-condicao: a ancora designa %d bytes; veio %+v", maxPlanInputBytes, r.OutputSource)
	}
	if r.Output == nil || len(*r.Output) != maxPlanInputBytes || r.OutputOmitted != "" || r.OutputUnavailable {
		t.Fatalf("128 KiB exactos tinham de sair inteiros; veio output!=nil=%v omitted=%q unavailable=%v", r.Output != nil, r.OutputOmitted, r.OutputUnavailable)
	}
}

// ---------------------------------------------------------------------------------------------
// I5 — o envelope REAL da sandbox, de ponta a ponta no nó
// ---------------------------------------------------------------------------------------------

// aos498EnvelopeDaSandbox lê um ficheiro de fio do envelope da sandbox — gerado pelo CODIFICADOR
// REAL no teste do pacote (`substrate/sandbox`, `TestAOS499_EnvelopeDeFio`) — e devolve os bytes
// do envelope e o documento que ele transporta.
func aos498EnvelopeDaSandbox(t *testing.T, nome string) (envelope []byte, documento string) {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "..", "substrate", "sandbox", "testdata", "aos499_envelope", nome+".json"))
	if err != nil {
		t.Fatalf("ficheiro de fio do envelope em falta (%v) — gera-o no pacote da sandbox com AOS494_ACTUALIZAR_FIO=1", err)
	}
	envelope = bytes.TrimSuffix(cru, []byte("\n"))
	var campos struct {
		StdoutText string `json:"stdout_text"`
	}
	if err := json.Unmarshal(envelope, &campos); err != nil {
		t.Fatalf("envelope ilegivel: %v", err)
	}
	return envelope, campos.StdoutText
}

// TestAOS498_Sandbox_EnvelopeReal corre um run de leitura num nó cuja `doc_read` é a TOOL REAL DA
// SANDBOX — o `MediatedLauncher` registado no Reference Monitor pelo arranque do nó, a partir do
// bloco `sandbox` do `AOS_MODEL_TOOLS`, com o driver de referência e o documento semeado — e não
// uma função de teste que devolve bytes crus.
//
// Era o que faltava (revisão, I5 e I1): em produção o resultado designado é o ENVELOPE que a
// sandbox escreve, e nenhum teste servia um pelo caminho ledger → `GET`. Aqui:
//
//   - a âncora designa o envelope: o digest e o tamanho são os dos bytes do ficheiro de fio que o
//     codificador real gerou;
//   - `output` é esse envelope, byte a byte, nos dois ramos (em memória e depois de um reinício);
//   - o documento está DENTRO do envelope (`stdout_text`), e `output` não é o documento.
//
// A resposta fica em `testdata/aos498_fio/sandbox-designada-measure.json`, que o `aos-orq`
// consome para medir (AOS-499).
func TestAOS498_Sandbox_EnvelopeReal(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	const tool = "doc_read"
	envelope, documento := aos498EnvelopeDaSandbox(t, "leitura-notas")
	if documento == "" || bytes.Equal(envelope, []byte(documento)) {
		t.Fatal("pre-condicao: o envelope transporta o documento e nao e o documento")
	}

	semente := t.TempDir()
	if err := os.WriteFile(filepath.Join(semente, "notes"), []byte(documento), 0o600); err != nil {
		t.Fatalf("semear o documento: %v", err)
	}
	t.Setenv("AOS_SANDBOX_DRIVER", "")
	t.Setenv("AOS_SANDBOX_SEED_DIR", semente)
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"`+tool+`","description":"le um documento","capability":"`+durCap+
		`","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"none","reversibility":"reversible","mutation":"none",`+
		`"sandbox":{"command":"read","path_arg":"doc_id"}}]`))
	signer := durSigner(t)
	entradas := []domain.Entry{counterEntry(t, signer), aos486Entry(signer, tool)}
	comSandbox := func(cfg *Config) {
		cfg.Catalog = catalogStub{entries: entradas}
		cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entradas...)
	}
	// O modelo pede a leitura com os argumentos que o `EffectRewriter` do nó transforma no pedido
	// de execução, e conclui com uma transcrição do documento atrás de uma frase.
	textoFinal := "O documento notes diz:\n\n" + documento
	modelo := func() agentruntime.ModelClient {
		return aos498Guiao(
			agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{ToolID: tool, Capability: durCap, ResourceRegion: govRegion, Input: []byte(`{"doc_id":"notes"}`)}}, StopReason: agentruntime.StopToolCalls},
			agentruntime.ModelResponse{Text: textoFinal, Final: true, StopReason: agentruntime.StopStop},
		)
	}
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true, comSandbox)
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{tool: tool, vinculo: "measure", contrato: true})

	exigir := func(ramo string, cru []byte, r aos498Resposta) {
		t.Helper()
		if r.Status != "completed" || !r.Terminated || r.FinalText != textoFinal {
			t.Fatalf("%s: o run conclui com o texto do modelo; veio %s", ramo, cru)
		}
		aos498ExigirDesignada(t, r.OutputSource, tool, "measure", envelope)
		if r.Output == nil || *r.Output != string(envelope) || r.OutputOmitted != "" {
			t.Fatalf("%s: output tem de ser o ENVELOPE que a sandbox escreveu, byte a byte:\n  quero: %s\n  veio:  %v (omitted=%q)", ramo, envelope, r.Output, r.OutputOmitted)
		}
		if *r.Output == documento {
			t.Fatalf("%s: output e o documento cru — este teste nao estava a exercitar a sandbox", ramo)
		}
	}
	cru, mem := aos498Ler(t, h, govHeaders())
	exigir("memoria", cru, mem)
	// O que a sandbox deixou no log é o seu ciclo de vida: a tool correu mesmo lá.
	evs, err := node.EventStore.Read(context.Background(), aos498RunID, 1)
	if err != nil {
		t.Fatalf("ler o stream do run: %v", err)
	}
	daSandbox := 0
	for _, e := range evs {
		if strings.HasPrefix(e.Type, "sandbox.") {
			daSandbox++
		}
	}
	if daSandbox == 0 {
		t.Fatal("o stream do run nao tem eventos da sandbox: a tool nao correu pelo MediatedLauncher")
	}
	aos498Fio(t, "sandbox-designada-measure", cru)

	aos494Fechar(t, node, svc)
	node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true, comSandbox)
	defer aos494Fechar(t, node2, svc2)
	if _, emMemoria := svc2.Outcome(aos498RunID); emMemoria {
		t.Fatal("depois do reinicio o desfecho nao pode estar em memoria")
	}
	cruDur, dur := aos498Ler(t, h2, govHeaders())
	exigir("duravel", cruDur, dur)
	if !reflect.DeepEqual(dur, mem) {
		t.Fatalf("o ramo duravel nao responde o mesmo que o ramo em memoria:\n  memoria: %s\n  duravel: %s", cru, cruDur)
	}
}
