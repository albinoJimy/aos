package main

// AOS-501 — OS FICHEIROS DE FIO DA ENTREGA POR REFERÊNCIA, DO LADO DO NÓ.
//
// O `aos-orq` passa a declarar a origem de uma saída com o vínculo VINCULATIVO e a entregar ao
// nó seguinte o que a tool devolveu. O que atravessa o fio entre os dois binários, nos dois
// sentidos, fica preso aqui:
//
//   - `../aos-orq/testdata/aos501_fio/post-runs-read_notes.json` é GERADO pelo `aos-orq` real (o
//     corpo do `POST /runs` do nó produtor, com `output_source_binding: binding`) e CONSUMIDO
//     aqui, byte a byte, pelo nó real;
//   - `testdata/aos501_fio/sandbox-binding-*.json` são as respostas do `GET /runs/{id}` de um nó
//     real, CUJA TOOL CORRE NA SANDBOX, a esse corpo — geradas aqui e consumidas pelo `aos-orq`;
//   - `../aos-orq/testdata/aos501_fio/post-runs-summarize.json` é o corpo do `POST /runs` do nó
//     CONSUMIDOR, gerado pelo `aos-orq` com o documento nos `inputs`, e entregue aqui byte a byte.
//
// Regeneram-se com `AOS494_ACTUALIZAR_FIO=1`, por esta ordem: o `aos-orq` (o corpo do produtor),
// este pacote (as respostas), o `aos-orq` outra vez (o corpo do consumidor), e este pacote.
//
// ESTE TICKET NÃO MUDA O NÓ: os testes prendem o que o nó já faz (AOS-498) ao que o `aos-orq`
// passa a pedir-lhe e a ler dele.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
)

// aos501FioDoAosOrq é a pasta dos ficheiros que o teste do `aos-orq` GERA para este ticket.
var aos501FioDoAosOrq = filepath.Join("..", "aos-orq", "testdata", "aos501_fio")

// aos501LerDoAosOrq lê um corpo de `POST /runs` gerado pelo `aos-orq`.
func aos501LerDoAosOrq(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join(aos501FioDoAosOrq, nome+".json"))
	if err != nil {
		if aos494Actualizar() {
			t.Skipf("o corpo %s ainda nao foi gerado (%v): corre primeiro o teste do aos-orq com AOS494_ACTUALIZAR_FIO=1, e este outra vez", nome, err)
		}
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o no aos-orq com AOS494_ACTUALIZAR_FIO=1", err)
	}
	return bytes.TrimSpace(cru)
}

// aos501Fio compara (ou, a regenerar, escreve) uma resposta do nó que o `aos-orq` consome.
func aos501Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos501_fio", nome+".json")
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

// aos501Resumo é o que o modelo falso escreve depois de ler o documento: um RESUMO que perde o
// número do total aprovado (1250) e uma tarefa. É o defeito medido em produção a 2026-10-05.
const aos501Resumo = "Resumo das notas: a reuniao de agosto aprovou um total em tres parcelas e deixou tarefas por fechar, entre elas rever o orcamento e ligar ao fornecedor."

// aos501NoComSandbox compõe um nó cuja `doc_read` é a TOOL REAL DA SANDBOX (o `MediatedLauncher`
// com o driver de referência), com os documentos dados semeados, em imposição e com o gate
// soberano de leitura. O modelo falso pede a leitura de `docID` e conclui com [aos501Resumo].
func aos501NoComSandbox(t *testing.T, documentos map[string]string, docID string) (*Node, *NodeService, http.Handler) {
	t.Helper()
	pinBreakerEnv(t, "0", "0", "0", "0")
	const tool = "doc_read"
	semente := t.TempDir()
	for nome, conteudo := range documentos {
		if err := os.WriteFile(filepath.Join(semente, nome), []byte(conteudo), 0o600); err != nil {
			t.Fatalf("semear o documento: %v", err)
		}
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
	pedido, err := json.Marshal(map[string]string{"doc_id": docID})
	if err != nil {
		t.Fatal(err)
	}
	modelo := aos498Guiao(
		agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{ToolID: tool, Capability: durCap, ResourceRegion: govRegion, Input: pedido}}, StopReason: agentruntime.StopToolCalls},
		agentruntime.ModelResponse{Text: aos501Resumo, Final: true, StopReason: agentruntime.StopStop},
	)
	return aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce, modelo, true, comSandbox)
}

// aos501Submeter entrega ao nó o corpo do `aos-orq` trocando só o que não se fixa num ficheiro (a
// credencial e o principal) e tirando o vínculo `plan_request` (este nó não autentica quem
// chama), espera o run e devolve o `GET /runs/{id}`.
func aos501Submeter(t *testing.T, node *Node, svc *NodeService, h http.Handler, campos map[string]json.RawMessage, runID string) []byte {
	t.Helper()
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	corpo := map[string]json.RawMessage{}
	for k, v := range campos {
		corpo[k] = v
	}
	corpo["credential"], _ = json.Marshal(tok.Compact)
	corpo["principal_nhi"], _ = json.Marshal(durAgent)
	delete(corpo, "plan_request")
	if rec := postReq(h, "/runs", corpo, govHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs com o corpo do aos-orq: %d (%s)", rec.Code, rec.Body.String())
	}
	wc, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(wc, runID); werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado e acabado: ok=%v err=%v", ok, werr)
	}
	get := getReq(h, "/runs/"+runID, govHeaders())
	if get.Code != http.StatusOK {
		t.Fatalf("GET /runs/%s: %d (%s)", runID, get.Code, get.Body.String())
	}
	return get.Body.Bytes()
}

// TestAOS501_Fio_ProdutorDoAosOrq CONSOME o corpo do `POST /runs` que o `aos-orq` REAL enviou em
// modo `on` para o nó `read_notes` de um plano que declara a origem da saída — com
// `output_from_tool:"doc_read"` e `output_source_binding:"binding"`.
//
//  1. BYTE A BYTE: o corpo atravessa o decoder estrito do nó e todas as recusas de pedido.
//  2. O RUN, NUM NÓ CUJA TOOL CORRE NA SANDBOX: o modelo falso lê o documento e escreve um resumo
//     que perde um número. O `GET /runs/{id}` devolve a âncora `binding`/`designated` e o
//     ENVELOPE da sandbox, com o documento inteiro lá dentro. As respostas ficam em
//     `testdata/aos501_fio/`, que o `aos-orq` consome.
func TestAOS501_Fio_ProdutorDoAosOrq(t *testing.T) {
	cru := aos501LerDoAosOrq(t, "post-runs-read_notes")
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cru, &campos); err != nil {
		t.Fatalf("o corpo do aos-orq nao e JSON: %v", err)
	}
	for campo, quer := range map[string]string{
		"tools": `["doc_read"]`, "completion_requires": `["doc_read"]`,
		"output_from_tool": `"doc_read"`, "output_source_binding": `"binding"`,
	} {
		if string(campos[campo]) != quer {
			t.Fatalf("o corpo do aos-orq tem %s = %s; em on, com a origem declarada, e %s", campo, campos[campo], quer)
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

	// O documento é o do ficheiro de fio do envelope (o que o codificador real da sandbox escreve
	// para a leitura de `notes`): tem o número 1250, que o resumo do modelo perde.
	envelope, documento := aos498EnvelopeDaSandbox(t, "leitura-notas")
	if !strings.Contains(documento, "1250") || strings.Contains(aos501Resumo, "1250") {
		t.Fatal("pre-condicao: o documento tem o numero 1250 e o resumo do modelo nao")
	}

	t.Run("Sandbox_OModeloResumeEOsBytesDesignadosSaoODocumento", func(t *testing.T) {
		node, svc, h := aos501NoComSandbox(t, map[string]string{"notes": documento}, "notes")
		defer aos494Fechar(t, node, svc)
		resposta := aos501Submeter(t, node, svc, h, campos, runID)
		var r aos498Resposta
		if err := json.Unmarshal(resposta, &r); err != nil {
			t.Fatalf("resposta ilegivel: %v (%s)", err, resposta)
		}
		if r.Status != "completed" || !r.Terminated || r.FinalText != aos501Resumo {
			t.Fatalf("o run conclui com o RESUMO do modelo como texto final; veio %s", resposta)
		}
		aos498ExigirDesignada(t, r.OutputSource, "doc_read", "binding", envelope)
		if r.Output == nil || *r.Output != string(envelope) || r.OutputOmitted != "" || r.OutputUnavailable {
			t.Fatalf("output tem de ser o ENVELOPE que a sandbox escreveu, byte a byte:\n  quero: %s\n  veio:  %v", envelope, r.Output)
		}
		if !strings.Contains(*r.Output, "1250") || strings.Contains(r.FinalText, "1250") {
			t.Fatal("o numero 1250 esta nos bytes designados e NAO esta no texto final: e o caso que a entrega por referencia fecha")
		}
		aos501Fio(t, "sandbox-binding-resumo", resposta)
	})

	// A TOOL CORRE E FALHA: o documento pedido não existe. Para o runtime é uma chamada efectiva
	// (despachada, sem erro de tool), e o kernel designa-a; os bytes são o envelope de uma falha.
	// O `aos-orq` não o entrega (`origem_tool_falhou`).
	t.Run("Sandbox_DocumentoEmFalta_EnvelopeComSaidaDiferenteDeZero", func(t *testing.T) {
		node, svc, h := aos501NoComSandbox(t, map[string]string{"notes": documento}, "nao-existe")
		defer aos494Fechar(t, node, svc)
		resposta := aos501Submeter(t, node, svc, h, campos, runID)
		var r aos498Resposta
		if err := json.Unmarshal(resposta, &r); err != nil {
			t.Fatalf("resposta ilegivel: %v (%s)", err, resposta)
		}
		if r.OutputSource == nil || r.OutputSource.State != agentruntime.OutputSourceDesignated || r.Output == nil {
			t.Fatalf("a leitura de um documento em falta e uma chamada efectiva, e o kernel designa-a; veio %s", resposta)
		}
		var env struct {
			Saida int `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(*r.Output), &env); err != nil || env.Saida == 0 {
			t.Fatalf("os bytes designados tem de ser o envelope de uma execucao que falhou (exit_code != 0); veio %q (%v)", *r.Output, err)
		}
		aos501Fio(t, "sandbox-binding-tool-falhou", resposta)
	})

	// O DOCUMENTO VAZIO: a leitura tem êxito e o envelope não traz texto nenhum. Os bytes
	// designados não são zero bytes (são o envelope), e o `aos-orq` não os entrega (`origem_vazia`).
	t.Run("Sandbox_DocumentoVazio_EnvelopeSemTexto", func(t *testing.T) {
		node, svc, h := aos501NoComSandbox(t, map[string]string{"notes": ""}, "notes")
		defer aos494Fechar(t, node, svc)
		resposta := aos501Submeter(t, node, svc, h, campos, runID)
		var r aos498Resposta
		if err := json.Unmarshal(resposta, &r); err != nil {
			t.Fatalf("resposta ilegivel: %v (%s)", err, resposta)
		}
		if r.OutputSource == nil || r.OutputSource.State != agentruntime.OutputSourceDesignated || r.Output == nil || len(*r.Output) == 0 {
			t.Fatalf("a leitura de um documento vazio designa o envelope, que nao tem zero bytes; veio %s", resposta)
		}
		var env struct {
			Texto string `json:"stdout_text"`
			Saida int    `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(*r.Output), &env); err != nil || env.Saida != 0 || env.Texto != "" {
			t.Fatalf("os bytes designados tem de ser um envelope com exit_code 0 e sem texto; veio %q (%v)", *r.Output, err)
		}
		aos501Fio(t, "sandbox-binding-vazio", resposta)
	})
}

// aos501ModeloQueCaptura guarda o prompt materializado do primeiro turno e conclui.
type aos501ModeloQueCaptura struct{ visto string }

func (m *aos501ModeloQueCaptura) Call(_ context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if m.visto == "" {
		m.visto = string(v.Materialized)
	}
	return agentruntime.ModelResponse{Text: "tres pontos", Final: true, StopReason: agentruntime.StopStop, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

// TestAOS501_Fio_ConsumidorDoAosOrq CONSOME o corpo do `POST /runs` do nó CONSUMIDOR, tal como o
// `aos-orq` real o enviou depois de entregar por referência: os `inputs` levam o DOCUMENTO que a
// tool do produtor devolveu (extraído do envelope da sandbox), e não o resumo do modelo.
//
// O NÓ CONSUMIDOR NÃO MUDA: o corpo tem a forma de sempre e passa as validações de sempre (o
// digest de cada entrada, os tectos), e o conteúdo chega ao prompt do run como `plan_input`,
// marcado untrusted, com a proveniência do contrato.
func TestAOS501_Fio_ConsumidorDoAosOrq(t *testing.T) {
	cru := aos501LerDoAosOrq(t, "post-runs-summarize")
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cru, &campos); err != nil {
		t.Fatalf("o corpo do aos-orq nao e JSON: %v", err)
	}
	// A forma é a de hoje: nenhum campo novo, e nenhuma declaração de origem no consumidor.
	for campo := range campos {
		conhecido := false
		for _, c := range []string{"run_id", "objective", "principal_nhi", "credential", "tools", "inputs", "plan_request", "completion_requires"} {
			conhecido = conhecido || c == campo
		}
		if !conhecido {
			t.Fatalf("o corpo do consumidor leva o campo %q, que o consumidor de hoje nao recebia", campo)
		}
	}
	var entradas []struct {
		From, Output, Digest, Content string
	}
	if err := json.Unmarshal(campos["inputs"], &entradas); err != nil || len(entradas) != 1 {
		t.Fatalf("o consumidor recebe uma entrada: %s (%v)", campos["inputs"], err)
	}
	_, documento := aos498EnvelopeDaSandbox(t, "leitura-notas")
	e := entradas[0]
	if e.Content != documento || e.Digest != aos497Digest(documento) || e.From != "read_notes" || e.Output != "conteudo" {
		t.Fatalf("a entrada tem de ser o DOCUMENTO que a tool devolveu, com o seu digest e a proveniencia do contrato:\n  quero: %q\n  veio:  %+v", documento, e)
	}
	if strings.Contains(e.Content, aos501Resumo) || !strings.Contains(e.Content, "1250") || strings.Contains(e.Content, `"exit_code"`) {
		t.Fatalf("a entrada nao e o resumo do modelo nem o envelope: e o texto do documento, com o numero 1250:\n%s", e.Content)
	}
	var runID string
	if err := json.Unmarshal(campos["run_id"], &runID); err != nil || !strings.HasSuffix(runID, "~summarize") {
		t.Fatalf("o run_id do corpo tem de ser o do no summarize; veio %s (%v)", campos["run_id"], err)
	}

	t.Run("ByteAByte_PassaODecoderEAsValidacoesDasEntradas", func(t *testing.T) {
		n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
		if rec := aos494Servir(n.h, httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(cru))); rec.Code != http.StatusForbidden {
			t.Fatalf("o corpo do consumidor, byte a byte, tinha de passar o decoder e as validacoes das entradas e parar na autenticacao (403); veio %d (%s)", rec.Code, rec.Body.String())
		}
		// NÃO-VACUIDADE: o mesmo corpo com UM byte do conteúdo trocado é recusado pelo digest (400),
		// antes da autenticação. O 403 de cima não é «o nó não olhou para as entradas».
		adulterado := bytes.Replace(cru, []byte("1250"), []byte("1251"), 1)
		if bytes.Equal(adulterado, cru) {
			t.Fatal("pre-condicao: o corpo leva o numero 1250 no conteudo")
		}
		if rec := aos494Servir(n.h, httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(adulterado))); rec.Code != http.StatusBadRequest {
			t.Fatalf("um conteudo que nao bate com o digest tinha de ser recusado com 400; veio %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("ODocumentoChegaAoPromptComoPlanInputUntrusted", func(t *testing.T) {
		pinBreakerEnv(t, "0", "0", "0", "0")
		modelo := &aos501ModeloQueCaptura{}
		node, svc, h := aos494Compor(t, t.TempDir(), audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce, modelo, true)
		defer aos494Fechar(t, node, svc)
		aos501Submeter(t, node, svc, h, campos, runID)
		for _, quer := range []string{"<plan_input", "taint=untrusted", "plan_input_from=read_notes", "Total aprovado: 1250 EUR em 3 parcelas."} {
			if !strings.Contains(modelo.visto, quer) {
				t.Fatalf("o prompt do run consumidor tinha de trazer %q:\n%s", quer, modelo.visto)
			}
		}
		if strings.Contains(modelo.visto, aos501Resumo) {
			t.Fatalf("o resumo do modelo do produtor chegou ao prompt do consumidor:\n%s", modelo.visto)
		}
	})
}
