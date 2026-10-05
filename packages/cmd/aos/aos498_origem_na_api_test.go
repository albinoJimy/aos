package main

// AOS-498 — A ORIGEM DA SAÍDA NA API DO NÓ.
//
// O que este ficheiro prova, pela API HTTP do nó composto (cadeia de mediação real, execução
// durável, cifra por-titular, gate soberano de leitura) e com um modelo falso:
//
//   - o `POST /runs` aceita `output_from_tool` + `output_source_binding`, e recusa com 400 e
//     mensagem própria uma declaração mal formada ou fora da lista-branca do mesmo pedido;
//   - o `GET /tools` anuncia o suporte (e não o anuncia com o veredicto desligado);
//   - o `GET /runs/{id}` devolve a âncora e os bytes que a TOOL devolveu — diferentes do texto
//     final do modelo —, e depois de um REINÍCIO o ramo durável devolve os mesmos;
//   - os bytes vêm do step-ledger, só saem depois do selo WORM e só se conferirem com o digest
//     selado; a captura do turno não é fonte;
//   - a falta dos bytes manifesta-se conforme o vínculo, e nunca muda o desfecho de um run
//     declarado «só medição»;
//   - um run sem declaração responde como antes (os ficheiros do AOS-494 não mudaram um byte:
//     são os testes dele que o fixam).
//
// OS FICHEIROS DE FIO. `testdata/aos498_fio/` é GERADO aqui e consumido pelos testes do
// `aos-orq`; `../aos-orq/testdata/aos499_fio/` é gerado lá e consumido aqui. Regeneram-se com
// `AOS494_ACTUALIZAR_FIO=1`, pela ordem do AOS-494: este pacote (o anúncio), o do `aos-orq` (o
// corpo do `POST`), e este outra vez (as respostas a esse corpo).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

const (
	aos498RunID = "aos498-fio"
	// As duas tools cujo resultado não se transporta: uma acima do tecto, uma que não é texto.
	aos498ToolGrande  = "blob"
	aos498ToolBinaria = "bin"
)

// aos498Resposta é o `GET /runs/{id}` como um cliente NOVO o lê.
type aos498Resposta struct {
	aos494Resposta
	OutputSource  *agentruntime.OutputSource `json:"output_source"`
	Output        *string                    `json:"output"`
	OutputOmitted string                     `json:"output_omitted"`
}

// aos498Fio compara uma resposta do nó com o ficheiro do fio que o `aos-orq` consome nos testes
// dele. `AOS494_ACTUALIZAR_FIO=1` reescreve-o.
func aos498Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos498_fio", nome+".json")
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

// aos498Guiao devolve um modelo falso que responde, por ordem, o que lhe derem.
func aos498Guiao(respostas ...agentruntime.ModelResponse) agentruntime.ModelClient {
	var n int32
	return agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(respostas) {
			i = len(respostas) - 1
		}
		r := respostas[i]
		r.Usage = agentruntime.Usage{InputTokens: 9, OutputTokens: 4}
		return r, nil
	})
}

// aos498Chama é o turno em que o modelo pede a tool `n` vezes.
func aos498Chama(tool string, n int) agentruntime.ModelResponse {
	var calls []agentruntime.ToolInvocation
	for i := 0; i < n; i++ {
		calls = append(calls, agentruntime.ToolInvocation{ToolID: tool, Capability: durCap, ResourceRegion: govRegion, Input: []byte("notes")})
	}
	return agentruntime.ModelResponse{ToolCalls: calls, StopReason: agentruntime.StopToolCalls}
}

// aos498Conclui é o turno final, com o texto do modelo — que NÃO é o que a tool devolveu.
func aos498Conclui() agentruntime.ModelResponse {
	return agentruntime.ModelResponse{Text: aos494Boa, Final: true, StopReason: agentruntime.StopStop}
}

// aos498ComToolsQueNaoSeTransportam acrescenta ao catálogo do nó as duas tools de resultado não
// transportável. Quem compõe regista-as depois, com [aos498RegistarNaoTransportaveis].
func aos498ComToolsQueNaoSeTransportam(t *testing.T) func(*Config) {
	t.Helper()
	signer := durSigner(t)
	entradas := []domain.Entry{counterEntry(t, signer), aos486Entry(signer, aos498ToolGrande), aos486Entry(signer, aos498ToolBinaria)}
	return func(cfg *Config) {
		cfg.Catalog = catalogStub{entries: entradas}
		cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entradas...)
	}
}

// aos498Grande e aos498Binario são os dois resultados que não se transportam.
var (
	aos498Grande  = bytes.Repeat([]byte("a"), maxPlanInputBytes+1)
	aos498Binario = []byte{'d', 'o', 'c', 0xff, 0xfe, 0x00, 'x'}
)

func aos498RegistarNaoTransportaveis(t *testing.T, node *Node) {
	t.Helper()
	for tool, valor := range map[string][]byte{aos498ToolGrande: aos498Grande, aos498ToolBinaria: aos498Binario} {
		if err := node.Runtime.Register(tool, func(context.Context, []byte) ([]byte, error) { return valor, nil }); err != nil {
			t.Fatalf("Register(%s): %v", tool, err)
		}
	}
}

// aos498Pedido é o que varia no corpo do `POST /runs` destes testes.
type aos498Pedido struct {
	tool     string // a tool da lista-branca; vazio ⇒ `counter`
	vinculo  string // vazio ⇒ o corpo NÃO leva a declaração de origem
	contrato bool   // o corpo leva `completion_requires` com a mesma tool
}

// aos498Submeter submete o run de um nó de plano e espera que ele acabe.
func aos498Submeter(t *testing.T, node *Node, svc *NodeService, h http.Handler, cabecalhos map[string]string, p aos498Pedido) {
	t.Helper()
	ctx := context.Background()
	if p.tool == "" {
		p.tool = aos494Tool
	}
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	corpo := map[string]any{
		"run_id":        aos498RunID,
		"objective":     "Le o documento notes e devolve o conteudo",
		"principal_nhi": durAgent,
		"credential":    tok.Compact,
		"tools":         []string{p.tool},
		"inputs":        []map[string]string{},
	}
	if p.contrato {
		corpo["completion_requires"] = []string{p.tool}
	}
	if p.vinculo != "" {
		corpo["output_from_tool"] = p.tool
		corpo["output_source_binding"] = p.vinculo
	}
	if rec := postReq(h, "/runs", corpo, cabecalhos); rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	wc, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(wc, aos498RunID); werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado e acabado: ok=%v err=%v", ok, werr)
	}
}

// aos498Ler faz o `GET /runs/{id}` e devolve os bytes e a resposta descodificada.
func aos498Ler(t *testing.T, h http.Handler, cabecalhos map[string]string) ([]byte, aos498Resposta) {
	t.Helper()
	rec := getReq(h, "/runs/"+aos498RunID, cabecalhos)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runs/%s devia dar 200, veio %d (%s)", aos498RunID, rec.Code, rec.Body.String())
	}
	var r aos498Resposta
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("resposta ilegivel: %v (%s)", err, rec.Body.String())
	}
	return rec.Body.Bytes(), r
}

// aos498ExigirDesignada: a âncora designa a chamada da tool, com o digest e o tamanho de `bytes`.
func aos498ExigirDesignada(t *testing.T, a *agentruntime.OutputSource, tool, vinculo string, valor []byte) {
	t.Helper()
	quer := &agentruntime.OutputSource{
		Tool: tool, Binding: agentruntime.OutputSourceBinding(vinculo), State: agentruntime.OutputSourceDesignated,
		StepID: "step-000001-tool-1", Digest: aos497Digest(string(valor)), Bytes: len(valor),
	}
	if !reflect.DeepEqual(a, quer) {
		t.Fatalf("output_source = %+v; quero %+v", a, quer)
	}
}

// aos498Cabeca devolve a cabeça da partição WORM das leituras do run.
func aos498Cabeca(t *testing.T, node *Node) uint64 {
	t.Helper()
	n, err := node.WORM.Head(context.Background(), readAuditPartition(aos498RunID))
	if err != nil {
		t.Fatalf("WORM.Head: %v", err)
	}
	return n
}

// aos498OpenerQueAltera é o cifrador por-titular do nó com uma função que ALTERA o conteúdo
// depois de aberto: o que um log adulterado, ou uma fonte errada, entregava a quem lê.
type aos498OpenerQueAltera struct {
	dentro agentruntime.ContentOpener
	altera func([]byte) []byte
}

func (o *aos498OpenerQueAltera) OpenContent(ctx context.Context, subject string, sealed []byte) ([]byte, error) {
	claro, err := o.dentro.OpenContent(ctx, subject, sealed)
	if err != nil {
		return nil, err
	}
	return o.altera(claro), nil
}

// ---------------------------------------------------------------------------------------------
// A porta e o anúncio
// ---------------------------------------------------------------------------------------------

// TestAOS498_PostRuns_OrigemContraAListaBranca: as recusas da declaração de origem na porta, cada
// uma com 400 e a sua mensagem — e nenhuma repete o valor recusado.
func TestAOS498_PostRuns_OrigemContraAListaBranca(t *testing.T) {
	node, _ := newAPINode(t, nil, false)
	_, h := newAPI(t, node)
	base := func() map[string]any {
		return map[string]any{"run_id": "aos498-porta", "objective": "ler", "principal_nhi": "nhi:agent-1"}
	}
	for _, c := range []struct {
		nome string
		muda func(map[string]any)
		quer string
	}{
		{"fora da lista-branca", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{"doc_read"}, "doc_write", "measure"
		}, erroOrigemForaDaLista},
		{"lista-branca vazia", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{}, "doc_read", "measure"
		}, erroOrigemForaDaLista},
		{"outra caixa nao e a tool", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{"doc_read"}, "Doc_Read", "binding"
		}, erroOrigemForaDaLista},
		{"sem lista-branca", func(m map[string]any) {
			m["output_from_tool"], m["output_source_binding"] = "doc_read", "measure"
		}, erroOrigemSemListaBranca},
		{"origem sem vinculo", func(m map[string]any) {
			m["tools"], m["output_from_tool"] = []string{"doc_read"}, "doc_read"
		}, erroOrigemSemVinculo},
		{"vinculo sem origem", func(m map[string]any) {
			m["tools"], m["output_source_binding"] = []string{"doc_read"}, "measure"
		}, erroVinculoSemOrigem},
		{"vinculo desconhecido", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{"doc_read"}, "doc_read", "enforce-por-favor"
		}, erroVinculoDesconhecido},
		{"vinculo com outra caixa", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{"doc_read"}, "doc_read", "Measure"
		}, erroVinculoDesconhecido},
		{"nome com espaco, mesmo estando na lista", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{"ler documento"}, "ler documento", "measure"
		}, erroOrigemComNomeInvalido},
		{"nome comprido, mesmo estando na lista", func(m map[string]any) {
			nome := strings.Repeat("a", 129)
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{nome}, nome, "measure"
		}, erroOrigemComNomeInvalido},
		{"o estado da designacao nao vem do corpo", func(m map[string]any) {
			m["tools"], m["output_from_tool"], m["output_source_binding"] = []string{"doc_read"}, "doc_read", "measure"
			m["output_source"] = map[string]any{"state": "designated"}
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
			for _, valor := range []string{"doc_write", "Doc_Read", "enforce-por-favor", "ler documento"} {
				if strings.Contains(rec.Body.String(), valor) {
					t.Fatalf("a recusa repete um valor que veio do corpo (%q): %s", valor, rec.Body.String())
				}
			}
		})
	}
	// A declaração bem formada passa a porta, nos dois vínculos. (A tool não existe neste nó: é o
	// kernel que o diz, no arranque do run — TestAOS497_OrigemImpossivelOuMalFormada_RespondeFailed.)
	for i, vinculo := range agentruntime.OutputSourceBindings() {
		corpo := base()
		corpo["run_id"] = "aos498-porta-" + string(vinculo)
		corpo["tools"], corpo["output_from_tool"], corpo["output_source_binding"] = []string{"doc_read"}, "doc_read", string(vinculo)
		if rec := postJSON(h, http.MethodPost, "/runs", corpo); rec.Code != http.StatusCreated {
			t.Fatalf("declaracao bem formada %d (%s): queria 201, veio %d (%s)", i, vinculo, rec.Code, rec.Body.String())
		}
	}
}

// TestAOS498_ValidarOrigemDaSaida_Ordem: a ordem das recusas é a das causas, e a ausência das
// duas metades é a ausência da declaração.
func TestAOS498_ValidarOrigemDaSaida_Ordem(t *testing.T) {
	lista := []string{"doc_read"}
	for _, c := range []struct {
		origem, vinculo string
		lista           []string
		quer            string
	}{
		{"", "", nil, ""},
		{"", "", lista, ""},
		{"doc_read", "measure", lista, ""},
		{"doc_read", "binding", lista, ""},
		{"", "binding", nil, erroVinculoSemOrigem},
		{"doc_read", "", nil, erroOrigemSemVinculo},
		{"doc_read", "outro", nil, erroVinculoDesconhecido},
		{"doc read", "measure", nil, erroOrigemComNomeInvalido},
		{"doc_read", "measure", nil, erroOrigemSemListaBranca},
		{"doc_read", "measure", []string{"outra"}, erroOrigemForaDaLista},
	} {
		if got := validarOrigemDaSaida(c.origem, c.vinculo, c.lista); got != c.quer {
			t.Errorf("validarOrigemDaSaida(%q, %q, %v) = %q; quero %q", c.origem, c.vinculo, c.lista, got, c.quer)
		}
	}
}

// TestAOS498_GetTools_AnunciaAOrigem: o `GET /tools` anuncia a origem da saída numa chave própria
// cuja presença é o anúncio. Com o veredicto desligado o nó não produz âncora e não anuncia; o
// resto da resposta é o de sempre nos três modos.
func TestAOS498_GetTools_AnunciaAOrigem(t *testing.T) {
	for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce, agentruntime.CompletionOff} {
		t.Run(string(modo), func(t *testing.T) {
			cfg := tnBaseConfig()
			cfg.CompletionVerdict = modo
			node, err := Bootstrap(context.Background(), cfg, discard{})
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
				CompletionContract *anuncioDoContrato   `json:"completion_contract"`
				OutputSource       *anuncioDaOrigem     `json:"output_source"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &corpo); err != nil {
				t.Fatalf("resposta ilegivel: %v", err)
			}
			if corpo.Tools == nil || corpo.CompletionContract == nil || corpo.CompletionContract.Mode != modo {
				t.Fatalf("`tools` e `completion_contract` saem como sempre; veio %s", rec.Body.String())
			}
			if modo == agentruntime.CompletionOff {
				if corpo.OutputSource != nil || strings.Contains(rec.Body.String(), "output_source") {
					t.Fatalf("com o veredicto desligado o no nao tem ancora e nao a anuncia; veio %s", rec.Body.String())
				}
				return
			}
			quer := &anuncioDaOrigem{Bindings: agentruntime.OutputSourceBindings(), MaxBytes: maxPlanInputBytes}
			if !reflect.DeepEqual(corpo.OutputSource, quer) {
				t.Fatalf("anuncio da origem = %+v; quero %+v (%s)", corpo.OutputSource, quer, rec.Body.String())
			}
		})
	}
}

// discard é um io.Writer que deita fora (o log do Bootstrap destes testes).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// ---------------------------------------------------------------------------------------------
// Os dois ramos
// ---------------------------------------------------------------------------------------------

// TestAOS498_Fio_OrigemNosDoisRamos: um run de leitura com a origem declarada devolve no `GET` a
// âncora `designated` e os bytes que a TOOL devolveu, diferentes do texto final do modelo. Depois
// de um REINÍCIO (custódia das KEK partilhada entre as duas incarnações) o ramo durável devolve os
// mesmos bytes e os mesmos metadados. Nos dois vínculos, e com o nó em imposição e em observação.
func TestAOS498_Fio_OrigemNosDoisRamos(t *testing.T) {
	for _, c := range []struct {
		nome    string
		modo    agentruntime.CompletionMode
		vinculo string
	}{
		{"measure-enforce", agentruntime.CompletionEnforce, "measure"},
		{"measure-observe", agentruntime.CompletionObserve, "measure"},
		{"binding-enforce", agentruntime.CompletionEnforce, "binding"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			dir := t.TempDir()
			vault := audit.NewInMemoryKeyVault(nil)
			modelo := func() agentruntime.ModelClient { return aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()) }

			node, svc, h := aos494Compor(t, dir, vault, c.modo, modelo(), true)
			aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: c.vinculo, contrato: true})
			cru, mem := aos498Ler(t, h, govHeaders())
			aos498Fio(t, "memoria-"+c.nome, cru)

			if mem.Status != "completed" || !mem.Terminated || mem.FinalText != aos494Boa || mem.OutcomeReason != "" || mem.OutputUnavailable {
				t.Fatalf("o run conclui como sempre, com o texto final do modelo; veio %+v", mem.aos494Resposta)
			}
			aos498ExigirDesignada(t, mem.OutputSource, aos494Tool, c.vinculo, []byte(aos494Documento))
			if mem.Output == nil || *mem.Output != aos494Documento || mem.OutputOmitted != "" {
				t.Fatalf("output tem de ser o que a tool devolveu (%q); veio %v (omitted=%q)", aos494Documento, mem.Output, mem.OutputOmitted)
			}
			if *mem.Output == mem.FinalText {
				t.Fatal("pre-condicao: o texto final do modelo tem de ser diferente do resultado da tool, senao o teste nao distingue as duas fontes")
			}

			// UM CLIENTE ANTERIOR (o `estadoDoRun` do `aos-orq` antes deste ticket, e a regra dele)
			// lê a mesma resposta e decide como sempre: os campos novos são aditivos.
			var anterior struct {
				Status            string `json:"status"`
				Terminated        bool   `json:"terminated,omitempty"`
				Error             string `json:"error,omitempty"`
				FinalText         string `json:"final_text,omitempty"`
				OutputUnavailable bool   `json:"output_unavailable,omitempty"`
			}
			if err := json.Unmarshal(cru, &anterior); err != nil {
				t.Fatalf("um cliente anterior tem de conseguir ler a resposta: %v", err)
			}
			if anterior.Status != "completed" || !anterior.Terminated || anterior.Error != "" || anterior.FinalText != aos494Boa || anterior.OutputUnavailable {
				t.Fatalf("um cliente anterior le o run concluido, com o texto final; leu %+v", anterior)
			}

			// REINÍCIO.
			aos494Fechar(t, node, svc)
			node2, svc2, h2 := aos494Compor(t, dir, vault, c.modo, modelo(), true)
			defer aos494Fechar(t, node2, svc2)
			if _, emMemoria := svc2.Outcome(aos498RunID); emMemoria {
				t.Fatal("depois do reinicio o desfecho nao pode estar em memoria: o teste nao exercitava o ramo duravel")
			}
			cruDur, dur := aos498Ler(t, h2, govHeaders())
			aos498Fio(t, "duravel-"+c.nome, cruDur)
			if !reflect.DeepEqual(dur, mem) {
				t.Fatalf("o ramo duravel nao responde o mesmo que o ramo em memoria:\n  memoria: %s\n  duravel: %s", cru, cruDur)
			}
		})
	}
}

// TestAOS498_SemDeclaracao_ARespostaNaoMuda: um run sem origem declarada não ganha campo nenhum,
// em nenhum dos ramos. (Os bytes exactos estão presos pelos ficheiros do AOS-494.)
func TestAOS498_SemDeclaracao_ARespostaNaoMuda(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()), true)
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{contrato: true})
	semCampos := func(cru []byte) {
		t.Helper()
		for _, campo := range []string{"output_source", `"output":`, "output_omitted"} {
			if bytes.Contains(cru, []byte(campo)) {
				t.Fatalf("um run sem origem declarada nao leva %s: %s", campo, cru)
			}
		}
	}
	cru, _ := aos498Ler(t, h, govHeaders())
	semCampos(cru)
	aos494Fechar(t, node, svc)
	node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos498Guiao(aos498Conclui()), true)
	defer aos494Fechar(t, node2, svc2)
	cru, _ = aos498Ler(t, h2, govHeaders())
	semCampos(cru)
}

// TestAOS498_SelaAntesDeAbrirOsBytes: nos DOIS ramos, a leitura que serve os bytes designados
// deixa UM selo WORM `read:outcome` com o principal do leitor, e o selo já está na cadeia quando
// o cifrador é chamado. Quem o gate não admite não recebe bytes, não deixa selo e não chega ao
// cifrador.
func TestAOS498_SelaAntesDeAbrirOsBytes(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	modelo := func() agentruntime.ModelClient { return aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()) }

	medir := func(t *testing.T, node *Node, h http.Handler) {
		t.Helper()
		var aberturas int
		var cabecaNaPrimeiraAbertura uint64
		node.contentOpener = &aos494OpenerEspiao{dentro: node.contentOpener, aoAbrir: func() {
			if aberturas == 0 {
				cabecaNaPrimeiraAbertura = aos498Cabeca(t, node)
			}
			aberturas++
		}}
		antes := aos498Cabeca(t, node)
		for nome, cabecalhos := range map[string]map[string]string{
			"board desconhecido": {HeaderReaderPrincipal: "nhi:intruso", HeaderReaderBoard: "board:desconhecido"},
			"sem credencial":     nil,
		} {
			rec := getReq(h, "/runs/"+aos498RunID, cabecalhos)
			if rec.Code != http.StatusNotFound || bytes.Contains(rec.Body.Bytes(), []byte(aos494Documento)) {
				t.Fatalf("leitor nao admitido (%s): %d %s", nome, rec.Code, rec.Body.String())
			}
		}
		if aos498Cabeca(t, node) != antes || aberturas != 0 {
			t.Fatalf("um leitor nao admitido deixou selo (%d→%d) ou chegou ao cifrador (%d abertura(s))", antes, aos498Cabeca(t, node), aberturas)
		}

		_, r := aos498Ler(t, h, govHeaders())
		if r.Output == nil || *r.Output != aos494Documento {
			t.Fatalf("o leitor admitido recebe os bytes designados; veio %+v", r)
		}
		depois := aos498Cabeca(t, node)
		if depois != antes+1 {
			t.Fatalf("a leitura deixa UM selo WORM; a cadeia foi de %d para %d", antes, depois)
		}
		selo, ok, err := node.WORM.At(ctx, readAuditPartition(aos498RunID), depois)
		if err != nil || !ok {
			t.Fatalf("WORM.At(%d): ok=%v err=%v", depois, ok, err)
		}
		if selo.Capability != capReadOutcome || selo.Principal.NHIID != govReader || selo.RunID != aos498RunID || selo.Decision != audit.DecisionAllow {
			t.Fatalf("selo da leitura: capability=%q principal=%q run=%q decision=%q", selo.Capability, selo.Principal.NHIID, selo.RunID, selo.Decision)
		}
		if aberturas == 0 {
			t.Fatal("os bytes sairam sem abrir conteudo: o teste nao mediu a decifracao")
		}
		if cabecaNaPrimeiraAbertura != depois {
			t.Fatalf("o conteudo foi aberto com a cadeia em %d, e o selo desta leitura e o %d: abriu-se ANTES de selar", cabecaNaPrimeiraAbertura, depois)
		}
	}

	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true)
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: "measure", contrato: true})
	if _, emMemoria := svc.Outcome(aos498RunID); !emMemoria {
		t.Fatal("pre-condicao: antes do reinicio o desfecho esta em memoria")
	}
	t.Run("ramo em memoria", func(t *testing.T) { medir(t, node, h) })
	aos494Fechar(t, node, svc)

	node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true)
	defer aos494Fechar(t, node2, svc2)
	if _, emMemoria := svc2.Outcome(aos498RunID); emMemoria {
		t.Fatal("pre-condicao: depois do reinicio o desfecho nao esta em memoria")
	}
	t.Run("ramo duravel", func(t *testing.T) { medir(t, node2, h2) })
}

// TestAOS498_BytesQueNaoConferem_NaoSaem: o step-ledger entrega bytes que não são os que o kernel
// selou (um log adulterado). O digest não confere e os bytes NÃO saem — nem os adulterados, nem
// os verdadeiros. Com o vínculo «só medição» o desfecho do run é o de sempre; com o vinculativo,
// a saída do run está indisponível.
func TestAOS498_BytesQueNaoConferem_NaoSaem(t *testing.T) {
	const adulterado = "CONTEUDO ADULTERADO no log!"
	for _, vinculo := range []string{"measure", "binding"} {
		t.Run(vinculo, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			dir := t.TempDir()
			vault := audit.NewInMemoryKeyVault(nil)
			modelo := func() agentruntime.ModelClient { return aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()) }
			adulterar := func(node *Node) *int {
				trocas := new(int)
				node.contentOpener = &aos498OpenerQueAltera{dentro: node.contentOpener, altera: func(claro []byte) []byte {
					// O registo do ledger é o resultado da tool, cru; a captura é JSON e não é igual.
					if string(claro) == aos494Documento {
						*trocas++
						return []byte(adulterado)
					}
					return claro
				}}
				return trocas
			}
			exigir := func(t *testing.T, cru []byte, r aos498Resposta, trocas int) {
				t.Helper()
				if trocas == 0 {
					t.Fatal("o ledger nao foi lido: o teste nao exercitou a conferencia")
				}
				if r.Output != nil || r.OutputOmitted != saidaOmitidaDeVez || bytes.Contains(cru, []byte(adulterado)) || bytes.Contains(cru, []byte(`"output":`)) {
					t.Fatalf("bytes que nao conferem nao saem, e a causa e output_omitted=unavailable; veio %s", cru)
				}
				// A âncora continua a sair: é o que o kernel selou.
				aos498ExigirDesignada(t, r.OutputSource, aos494Tool, vinculo, []byte(aos494Documento))
				if r.Status != "completed" || !r.Terminated || r.FinalText != aos494Boa {
					t.Fatalf("o estado e o texto final do run nao mudam; veio %s", cru)
				}
				if r.OutputUnavailable != (vinculo == "binding") {
					t.Fatalf("output_unavailable so com o vinculo vinculativo (vinculo=%s); veio %s", vinculo, cru)
				}
			}

			node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true)
			aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: vinculo, contrato: true})
			trocas := adulterar(node)
			cru, r := aos498Ler(t, h, govHeaders())
			exigir(t, cru, r, *trocas)
			if vinculo == "measure" {
				aos498Fio(t, "memoria-bytes-indisponiveis-measure", cru)
			}
			aos494Fechar(t, node, svc)

			node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true)
			defer aos494Fechar(t, node2, svc2)
			trocas2 := adulterar(node2)
			cru2, r2 := aos498Ler(t, h2, govHeaders())
			exigir(t, cru2, r2, *trocas2)
		})
	}
}

// TestAOS498_ACapturaNaoEFonte: a captura do turno designado tem OUTROS bytes (aqui, alterados à
// leitura — o que acontece de facto quando o turno mudou de desfecho entre vidas). O `GET`
// continua a servir os do step-ledger, que são os que conferem com o digest selado.
func TestAOS498_ACapturaNaoEFonte(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	modelo := func() agentruntime.ModelClient { return aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()) }
	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true)
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: "binding", contrato: true})
	aos494Fechar(t, node, svc)

	node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, modelo(), true)
	defer aos494Fechar(t, node2, svc2)
	// Na captura o resultado da tool vai em base64, dentro do JSON; no ledger vai cru.
	naCaptura := base64.StdEncoding.EncodeToString([]byte(aos494Documento))
	outro := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("X", len(aos494Documento))))
	var trocas int
	node2.contentOpener = &aos498OpenerQueAltera{dentro: node2.contentOpener, altera: func(claro []byte) []byte {
		if n := bytes.Count(claro, []byte(naCaptura)); n > 0 {
			trocas += n
			return bytes.ReplaceAll(claro, []byte(naCaptura), []byte(outro))
		}
		return claro
	}}
	cru, r := aos498Ler(t, h2, govHeaders())
	if trocas == 0 {
		t.Fatal("pre-condicao: a captura do turno tinha de ter sido lida e alterada (o ramo duravel le dela o texto final)")
	}
	if r.Output == nil || *r.Output != aos494Documento || r.OutputOmitted != "" || r.OutputUnavailable {
		t.Fatalf("com a captura a divergir, os bytes servidos sao os do step-ledger; veio %s", cru)
	}
}

// TestAOS498_BytesDoLedger_FalhaERetoma é o caso REAL em que a captura não tem os bytes
// designados (ADR-038 §2.3): a tool falha no turno 1 da primeira vida, o nó morre, e a segunda
// vida volta a despachar a chamada com êxito. A captura do turno é a da falha
// (TestAOS497_No_DigestSelado_EOResultHashDoLedger_FalhaERetoma fixa-o); a leitura deste ticket
// devolve os bytes da segunda vida, que são os que a âncora sela.
func TestAOS498_BytesDoLedger_FalhaERetoma(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	const runID = "plan-498~falha-e-retoma"
	approvers := crashResumeApprovers(t)
	goal := agentruntime.Goal{
		RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent},
		Model: agentruntime.ModelConfig{ModelID: "modelo-486"}, Objective: "o trabalho de um no do plano", MaxTurns: 4,
		AllowedTools: []string{"beta", "counter"}, CompletionRequires: []string{"counter"}, CompletionMode: agentruntime.CompletionEnforce,
		OutputFromTool: "counter", OutputSourceBinding: agentruntime.OutputSourceBinds,
	}
	const vida2 = "documento da vida 2"
	var execs int64
	counter := func([]byte) ([]byte, error) {
		if atomic.AddInt64(&execs, 1) == 1 {
			return nil, errors.New("timeout a jusante")
		}
		return []byte(vida2), nil
	}
	store := aos493Store(t)
	vault := audit.NewInMemoryKeyVault(nil)
	inc1 := aos493Incarnar(t, store, vault, approvers, counter)
	prod := goal
	prod.Credential = inc1.cred
	prod.MaxTurns = 1
	if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("turno 1 antes do crash: %v", rerr)
	}
	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim do crash simulado: %v", err)
	}
	rec, err := resumeRecordFromGoal(goal)
	if err != nil {
		t.Fatalf("resumeRecordFromGoal: %v", err)
	}
	if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
		t.Fatalf("semear o registo de retoma: %v", err)
	}
	_ = inc1.node.Close()

	inc2 := aos493Incarnar(t, store, vault, approvers, counter)
	t.Cleanup(func() { _ = inc2.node.Close() })
	svc2, _ := aos493ServicoComLog(t, inc2.node)
	svc2.mu.Lock()
	svc2.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
	svc2.mu.Unlock()
	if err := svc2.Resume(ctx, runID, inc2.cred); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	res := aos493Esperar(t, svc2, runID).Result
	a := res.OutputSource
	if execs != 2 || !res.Terminated || a == nil || a.State != agentruntime.OutputSourceDesignated {
		t.Fatalf("a segunda vida tinha de concluir com a origem designada: execs=%d res=%+v", execs, res)
	}
	if aos497DigestsDaCaptura(t, inc2.node, runID)[a.Digest] {
		t.Fatal("pre-condicao: a captura do turno designado nao tem os bytes da segunda vida")
	}

	// A leitura deste ticket, com o gate composto: os bytes da segunda vida, conferidos.
	h := &apiHandler{node: inc2.node, svc: svc2, readGov: &readGovernance{}}
	resp := runStateResponse{RunID: runID}
	if !h.origemNaResposta(httptest.NewRequest(http.MethodGet, "/runs/x", nil), &resp, a, true) {
		t.Fatal("a leitura tinha de responder")
	}
	if resp.Output == nil || *resp.Output != vida2 || resp.OutputOmitted != "" || resp.OutputUnavailable {
		t.Fatalf("os bytes servidos tem de ser os do step-ledger (%q); veio output=%v omitted=%q", vida2, resp.Output, resp.OutputOmitted)
	}
}

// TestAOS498_CustodiaFechada_OVinculoDecide: com a custódia das KEK fechada por instantes, os
// bytes designados não se lêem. Num run «só medição» isso NÃO muda o que o `GET` diz do run — o
// ramo em memória continua a responder 200 com o texto final —; num run vinculativo, a saída do
// run não se lê agora: 503. Quando a custódia volta, os bytes voltam.
func TestAOS498_CustodiaFechada_OVinculoDecide(t *testing.T) {
	for _, vinculo := range []string{"measure", "binding"} {
		t.Run(vinculo, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			cofre := &aos494CofreComPortao{InMemoryKeyVault: audit.NewInMemoryKeyVault(nil)}
			node, svc, h := aos494Compor(t, t.TempDir(), cofre, agentruntime.CompletionEnforce,
				aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()), true)
			defer aos494Fechar(t, node, svc)
			aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: vinculo, contrato: true})

			cofre.fechado.Store(true)
			rec := getReq(h, "/runs/"+aos498RunID, govHeaders())
			if bytes.Contains(rec.Body.Bytes(), []byte(`"output":`)) {
				t.Fatalf("com a custodia fechada nao ha bytes para servir: %s", rec.Body.String())
			}
			if vinculo == "binding" {
				if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "output_unavailable") || strings.Contains(rec.Body.String(), `"status"`) {
					t.Fatalf("vinculativo com a custodia fechada: queria 503 sem desfecho; veio %d %s", rec.Code, rec.Body.String())
				}
			} else {
				var r aos498Resposta
				if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || rec.Code != http.StatusOK {
					t.Fatalf("so medicao com a custodia fechada: queria 200; veio %d %s", rec.Code, rec.Body.String())
				}
				if r.Status != "completed" || !r.Terminated || r.FinalText != aos494Boa || r.OutputUnavailable || r.OutputOmitted != saidaOmitidaAgora {
					t.Fatalf("so medicao: o desfecho do run sai como sempre, com output_omitted=unavailable_now; veio %s", rec.Body.String())
				}
				aos498ExigirDesignada(t, r.OutputSource, aos494Tool, vinculo, []byte(aos494Documento))
			}

			cofre.fechado.Store(false)
			if _, r := aos498Ler(t, h, govHeaders()); r.Output == nil || *r.Output != aos494Documento {
				t.Fatalf("com a custodia de volta os bytes voltam; veio %+v", r)
			}
		})
	}
}

// TestAOS498_ApagamentoDoTitular_RetiraOsBytes (com o AOS-496): antes do apagamento o ramo em
// memória serve os bytes; depois, o desfecho saiu do registo em memória, a KEK já não existe, e
// o `GET` responde pelo log — `output_unavailable`, a âncora, e byte nenhum do titular.
func TestAOS498_ApagamentoDoTitular_RetiraOsBytes(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	vault := audit.NewInMemoryKeyVault(nil)
	node, svc, h := aos494Compor(t, t.TempDir(), vault, agentruntime.CompletionEnforce,
		aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()), true)
	defer aos494Fechar(t, node, svc)
	aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: "measure", contrato: true})
	if _, r := aos498Ler(t, h, govHeaders()); r.Output == nil || *r.Output != aos494Documento {
		t.Fatalf("pre-condicao: antes do apagamento o ramo em memoria serve os bytes; veio %+v", r)
	}

	// O apagamento: a KEK do titular é destruída e quem guarda conteúdo em memória é avisado —
	// o que o `/dsar/erase` e a expiração por TTL fazem. Em modo soberano o titular é o submissor.
	vault.Delete(govReader)
	node.titularApagado.avisar(govReader)
	if _, emMemoria := svc.Outcome(aos498RunID); emMemoria {
		t.Fatal("o apagamento tinha de retirar o desfecho do registo em memoria (AOS-496)")
	}
	cru, r := aos498Ler(t, h, govHeaders())
	if r.Output != nil || bytes.Contains(cru, []byte(aos494Documento)) || bytes.Contains(cru, []byte(`"output":`)) {
		t.Fatalf("depois do apagamento nenhum byte do titular sai: %s", cru)
	}
	if r.Status != "completed" || !r.Terminated || !r.OutputUnavailable || r.FinalText != "" || r.OutputOmitted != saidaOmitidaDeVez {
		t.Fatalf("depois do apagamento: completed + output_unavailable, sem texto, output_omitted=unavailable; veio %s", cru)
	}
	// O digest sobrevive ao apagamento, como o `result_hash` do ledger já sobrevive (ADR-038 §4).
	aos498ExigirDesignada(t, r.OutputSource, aos494Tool, "measure", []byte(aos494Documento))
	aos498Fio(t, "duravel-titular-apagado-measure", cru)
}

// TestAOS498_Fio_NaoTransportavel: um resultado acima do tecto (128 KiB) e um que não é UTF-8
// válido respondem com os metadados e SEM conteúdo, com uma marca própria que não é
// `output_unavailable`. Nada é truncado. Acima do tecto o step-ledger nem é aberto.
func TestAOS498_Fio_NaoTransportavel(t *testing.T) {
	for _, c := range []struct {
		nome, tool, marca string
		valor             []byte
	}{
		{"grande", aos498ToolGrande, saidaOmitidaGrande, aos498Grande},
		{"binario", aos498ToolBinaria, saidaOmitidaNaoTexto, aos498Binario},
	} {
		t.Run(c.nome, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce,
				aos498Guiao(aos498Chama(c.tool, 1), aos498Conclui()), true, aos498ComToolsQueNaoSeTransportam(t))
			defer aos494Fechar(t, node, svc)
			aos498RegistarNaoTransportaveis(t, node)
			aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{tool: c.tool, vinculo: "binding", contrato: true})

			var aberturas int
			node.contentOpener = &aos494OpenerEspiao{dentro: node.contentOpener, aoAbrir: func() { aberturas++ }}
			cru, r := aos498Ler(t, h, govHeaders())
			aos498ExigirDesignada(t, r.OutputSource, c.tool, "binding", c.valor)
			if r.Output != nil || r.OutputOmitted != c.marca || r.OutputUnavailable || bytes.Contains(cru, []byte(`"output":`)) {
				t.Fatalf("nao transportavel: metadados, output_omitted=%s, sem conteudo e sem output_unavailable; veio output_omitted=%q unavailable=%v", c.marca, r.OutputOmitted, r.OutputUnavailable)
			}
			if r.Status != "completed" || !r.Terminated || r.FinalText != aos494Boa {
				t.Fatalf("o run conclui como sempre; veio %+v", r.aos494Resposta)
			}
			if len(cru) > 4096 {
				t.Fatalf("a resposta nao pode levar o resultado, nem parte dele: %d bytes", len(cru))
			}
			if c.marca == saidaOmitidaGrande && aberturas != 0 {
				t.Fatalf("acima do tecto o tamanho vem da ancora: o step-ledger nao se abre (%d abertura(s))", aberturas)
			}
			aos498Fio(t, "nao-transportavel-"+c.nome, cru)
		})
	}
}

// TestAOS498_EmFaltaEAmbigua_SoMetadados: os estados `missing` e `ambiguous` respondem com a
// âncora e sem `output`, sem marca nenhuma — não há bytes designados de que falar.
func TestAOS498_EmFaltaEAmbigua_SoMetadados(t *testing.T) {
	for _, c := range []struct {
		nome   string
		modelo agentruntime.ModelClient
		estado agentruntime.OutputSourceState
	}{
		{"missing", aos498Guiao(aos498Conclui()), agentruntime.OutputSourceMissing},
		{"ambiguous", aos498Guiao(aos498Chama(aos494Tool, 2), aos498Conclui()), agentruntime.OutputSourceAmbiguous},
	} {
		t.Run(c.nome, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			dir := t.TempDir()
			vault := audit.NewInMemoryKeyVault(nil)
			// «Só medição» com o nó em IMPOSIÇÃO e sem contrato: o run conclui como um run sem
			// declaração — é o que o AOS-499 envia.
			node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, c.modelo, true)
			aos498Submeter(t, node, svc, h, govHeaders(), aos498Pedido{vinculo: "measure"})
			quer := &agentruntime.OutputSource{Tool: aos494Tool, Binding: agentruntime.OutputSourceMeasure, State: c.estado}
			exigir := func(cru []byte, r aos498Resposta) {
				t.Helper()
				if !reflect.DeepEqual(r.OutputSource, quer) || r.Output != nil || r.OutputOmitted != "" || r.OutputUnavailable {
					t.Fatalf("%s: so a ancora; veio %s", c.estado, cru)
				}
				if r.Status != "completed" || !r.Terminated || r.FinalText != aos494Boa || r.OutcomeReason != "" {
					t.Fatalf("so medicao: o run conclui como um run sem declaracao, com o no em enforce; veio %s", cru)
				}
			}
			cru, mem := aos498Ler(t, h, govHeaders())
			exigir(cru, mem)
			aos498Fio(t, "memoria-"+c.nome+"-measure", cru)
			aos494Fechar(t, node, svc)

			node2, svc2, h2 := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, c.modelo, true)
			defer aos494Fechar(t, node2, svc2)
			cru2, dur := aos498Ler(t, h2, govHeaders())
			exigir(cru2, dur)
		})
	}
}

// TestAOS498_NoSemGate_NaoAbreOsBytes: num nó sem o gate soberano de leitura o `GET` em memória
// continua a servir o texto final (tem-no em claro) e a âncora, e NÃO decifra o step-ledger para
// um chamador que ninguém autenticou.
func TestAOS498_NoSemGate_NaoAbreOsBytes(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce,
		aos498Guiao(aos498Chama(aos494Tool, 1), aos498Conclui()), false)
	defer aos494Fechar(t, node, svc)
	aos498Submeter(t, node, svc, h, nil, aos498Pedido{vinculo: "measure", contrato: true})
	var aberturas int
	node.contentOpener = &aos494OpenerEspiao{dentro: node.contentOpener, aoAbrir: func() { aberturas++ }}
	cru, r := aos498Ler(t, h, nil)
	if r.Output != nil || r.OutputOmitted != saidaOmitidaDeVez || aberturas != 0 || bytes.Contains(cru, []byte(`"output":`)) {
		t.Fatalf("sem gate nao se abre conteudo de titular: veio %s (%d abertura(s))", cru, aberturas)
	}
	if r.Status != "completed" || !r.Terminated || r.FinalText != aos494Boa || r.OutputUnavailable || r.OutputSource == nil {
		t.Fatalf("o desfecho do run, o texto final e a ancora saem como sempre; veio %s", cru)
	}
}

// ---------------------------------------------------------------------------------------------
// A classificação, sobre um step-ledger montado à mão
// ---------------------------------------------------------------------------------------------

// aos498Cifra é um cifrador por-titular de teste: «sela» com um prefixo e abre devolvendo o erro
// que lhe mandarem.
type aos498Cifra struct{ erro error }

func (c *aos498Cifra) SealContent(_ context.Context, _, _ string, claro []byte) ([]byte, error) {
	return append([]byte("selado:"), claro...), nil
}

func (c *aos498Cifra) OpenContent(_ context.Context, _ string, selado []byte) ([]byte, error) {
	if c.erro != nil {
		return nil, c.erro
	}
	return bytes.TrimPrefix(selado, []byte("selado:")), nil
}

// TestAOS498_OrigemNaResposta_Classificacao percorre o que a resposta diz por causa e por vínculo.
func TestAOS498_OrigemNaResposta_Classificacao(t *testing.T) {
	ctx := context.Background()
	const (
		runID = "run-498-classes"
		passo = "step-000001-tool-1"
		doc   = "documento do titular"
	)
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cifra := &aos498Cifra{}
	ledger, err := durable.NewStepLedger(store, durable.WithContentSealer(cifra))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	if _, _, err := ledger.Apply(durable.ContextWithTitular(ctx, "nhi:titular"), runID+":"+passo, func(context.Context) (durable.Result, error) {
		return durable.Result{Status: "ok", Payload: []byte(doc)}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ancora := func(vinculo agentruntime.OutputSourceBinding) *agentruntime.OutputSource {
		return &agentruntime.OutputSource{Tool: "doc_read", Binding: vinculo, State: agentruntime.OutputSourceDesignated,
			StepID: passo, Digest: aos497Digest(doc), Bytes: len(doc)}
	}
	type quer struct {
		responde     bool
		output       bool
		omitida      string
		indisponivel bool
	}
	for _, c := range []struct {
		nome      string
		erro      error                              // o que o cifrador devolve ao abrir
		ajusta    func(a *agentruntime.OutputSource) // muda a âncora
		semGate   bool                               // nó sem gate de leitura
		concluido bool
		measure   quer
		binding   quer
	}{
		{nome: "confere", concluido: true, measure: quer{true, true, "", false}, binding: quer{true, true, "", false}},
		{nome: "run que nao concluiu", concluido: false, measure: quer{true, false, "", false}, binding: quer{true, false, "", false}},
		{nome: "titular apagado", erro: audit.ErrDecrypt, concluido: true,
			measure: quer{true, false, saidaOmitidaDeVez, false}, binding: quer{true, false, saidaOmitidaDeVez, true}},
		{nome: "custodia fechada", erro: durable.ErrConteudoIndisponivel, concluido: true,
			measure: quer{true, false, saidaOmitidaAgora, false}, binding: quer{false, false, "", false}},
		{nome: "erro que ninguem classificou", erro: errors.New("falha desconhecida"), concluido: true,
			measure: quer{true, false, saidaOmitidaAgora, false}, binding: quer{false, false, "", false}},
		{nome: "passo fora do ledger", ajusta: func(a *agentruntime.OutputSource) { a.StepID = "step-000001-tool-2" }, concluido: true,
			measure: quer{true, false, saidaOmitidaDeVez, false}, binding: quer{true, false, saidaOmitidaDeVez, true}},
		{nome: "digest de outros bytes", ajusta: func(a *agentruntime.OutputSource) { a.Digest = aos497Digest("outro documento!!!!") }, concluido: true,
			measure: quer{true, false, saidaOmitidaDeVez, false}, binding: quer{true, false, saidaOmitidaDeVez, true}},
		{nome: "tamanho que nao bate", ajusta: func(a *agentruntime.OutputSource) { a.Bytes++ }, concluido: true,
			measure: quer{true, false, saidaOmitidaDeVez, false}, binding: quer{true, false, saidaOmitidaDeVez, true}},
		{nome: "acima do tecto", ajusta: func(a *agentruntime.OutputSource) { a.Bytes = maxPlanInputBytes + 1 }, concluido: true,
			measure: quer{true, false, saidaOmitidaGrande, false}, binding: quer{true, false, saidaOmitidaGrande, false}},
		{nome: "no sem gate de leitura", semGate: true, concluido: true,
			measure: quer{true, false, saidaOmitidaDeVez, false}, binding: quer{true, false, saidaOmitidaDeVez, true}},
	} {
		for vinculo, q := range map[agentruntime.OutputSourceBinding]quer{agentruntime.OutputSourceMeasure: c.measure, agentruntime.OutputSourceBinds: c.binding} {
			t.Run(c.nome+"/"+string(vinculo), func(t *testing.T) {
				cifra.erro = c.erro
				t.Cleanup(func() { cifra.erro = nil })
				h := &apiHandler{node: &Node{EventStore: store, contentOpener: cifra}}
				if !c.semGate {
					h.readGov = &readGovernance{}
				}
				a := ancora(vinculo)
				if c.ajusta != nil {
					c.ajusta(a)
				}
				resp := runStateResponse{RunID: runID, Status: "completed", Terminated: true, FinalText: "o texto final"}
				responde := h.origemNaResposta(httptest.NewRequest(http.MethodGet, "/runs/"+runID, nil), &resp, a, c.concluido)
				if responde != q.responde {
					t.Fatalf("responde = %v; quero %v (false ⇒ 503)", responde, q.responde)
				}
				if !responde {
					return
				}
				if (resp.Output != nil) != q.output || resp.OutputOmitted != q.omitida || resp.OutputUnavailable != q.indisponivel {
					t.Fatalf("output=%v omitted=%q unavailable=%v; quero output=%v omitted=%q unavailable=%v",
						resp.Output != nil, resp.OutputOmitted, resp.OutputUnavailable, q.output, q.omitida, q.indisponivel)
				}
				if q.output && *resp.Output != doc {
					t.Fatalf("output = %q; quero %q", *resp.Output, doc)
				}
				if resp.OutputSource == nil || *resp.OutputSource != *a || resp.OutputSource == a {
					t.Fatalf("a ancora sai sempre, e por copia; veio %+v", resp.OutputSource)
				}
				if resp.Status != "completed" || !resp.Terminated || resp.FinalText != "o texto final" {
					t.Fatalf("a origem nunca mexe no estado nem no texto final; veio %+v", resp)
				}
			})
		}
	}
	// Sem âncora, nada: nem campo, nem leitura.
	resp := runStateResponse{RunID: runID}
	if !(&apiHandler{}).origemNaResposta(httptest.NewRequest(http.MethodGet, "/runs/x", nil), &resp, nil, true) || !reflect.DeepEqual(resp, runStateResponse{RunID: runID}) {
		t.Fatalf("um run sem ancora nao muda a resposta; veio %+v", resp)
	}
}

// TestAOS498_OrigemIndisponivelDeVez: a lista dos definitivos é a do AOS-494 mais os dois erros
// que só a leitura do step-ledger produz. A custódia fechada decide-se primeiro.
func TestAOS498_OrigemIndisponivelDeVez(t *testing.T) {
	for _, c := range []struct {
		nome  string
		err   error
		deVez bool
	}{
		{"passo fora do ledger", durable.ErrAppliedResultNotFound, true},
		{"bytes que nao conferem", errOrigemNaoConfere, true},
		{"titular apagado", audit.ErrDecrypt, true},
		{"no sem gate", errSaidaDuravelSemGate, true},
		{"registo selado sem cifra", durable.ErrSealedResultNoCipher, true},
		{"custodia fechada", durable.ErrConteudoIndisponivel, false},
		{"custodia fechada com a KEK por verificar", errors.Join(durable.ErrConteudoIndisponivel, audit.ErrDecrypt), false},
		{"event store fechado", eventstore.ErrClosed, false},
		{"prazo do pedido", context.DeadlineExceeded, false},
		{"erro que ninguem classificou", errors.New("falha desconhecida"), false},
	} {
		if got := origemIndisponivelDeVez(c.err); got != c.deVez {
			t.Errorf("%s: de vez = %v, quero %v", c.nome, got, c.deVez)
		}
	}
}
