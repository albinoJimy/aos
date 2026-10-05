package main

// AOS-494 — O QUE A REVISÃO ADVERSARIAL DEIXOU SEM REDE, E PASSA A TER.
//
//   - o selo WORM da leitura durável que DECIFRA: a capability, o principal do leitor, e o selo
//     ANTES de abrir conteúdo (I4);
//   - a custódia fechada ou sem resposta não é «saída indisponível»: 503, e a saída volta
//     quando a custódia volta (I3);
//   - a trava do AOS-426 no ramo que decifra (M2);
//   - as três direcções do fio entre o `aos-orq` e o nó (I5): o corpo do `POST /runs` que o
//     `aos-orq` envia, o anúncio do `GET /tools`, e a resposta do ramo durável.
//
// Os ficheiros do fio vivem de cada lado em quem os GERA: `testdata/aos494_fio/` é do nó,
// `../aos-orq/testdata/aos495_fio/` é do `aos-orq`. Regeneram-se com `AOS494_ACTUALIZAR_FIO=1`,
// por esta ordem: este pacote (gera o anúncio), o do `aos-orq` (gera o corpo do `POST`, com o
// anúncio), e este pacote outra vez (gera as respostas ao corpo do `aos-orq`).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos494Actualizar diz se os ficheiros do fio estão a ser regenerados.
func aos494Actualizar() bool { return os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" }

// aos494OpenerEspiao é o cifrador por-titular do nó com um aviso antes de cada abertura.
type aos494OpenerEspiao struct {
	dentro  agentruntime.ContentOpener
	aoAbrir func()
}

func (o *aos494OpenerEspiao) OpenContent(ctx context.Context, subject string, sealed []byte) ([]byte, error) {
	o.aoAbrir()
	return o.dentro.OpenContent(ctx, subject, sealed)
}

// aos494CofreComPortao é um cofre de KEK cuja custódia se pode FECHAR sem apagar nada: o que o
// Vault selado, ou sem resposta, é para o nó de produção (AOS-436).
type aos494CofreComPortao struct {
	*audit.InMemoryKeyVault
	fechado atomic.Bool
}

func (c *aos494CofreComPortao) portaoDoTitular(string) error {
	if c.fechado.Load() {
		return errors.New("custodia fechada (teste)")
	}
	return nil
}

// aos494Servir entrega um pedido ja construido ao handler do no.
func aos494Servir(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// aos494Concluir corre o run bom até ao fim numa primeira incarnação do nó e fecha-a. Devolve o
// que o ramo em memória respondeu.
func aos494Concluir(t *testing.T, dir string, vault audit.KeyVault) aos494Resposta {
	t.Helper()
	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos494Modelo(""), true)
	aos494Submeter(t, node, svc, h, govHeaders(), []string{aos494Tool})
	_, mem := aos494Ler(t, h, govHeaders())
	if mem.Status != "completed" || !mem.Terminated || mem.FinalText != aos494Boa {
		t.Fatalf("pre-condicao: em memoria o run conclui com o texto; veio %+v", mem)
	}
	aos494Fechar(t, node, svc)
	return mem
}

// TestAOS494_RamoDuravel_SelaAntesDeDecifrar: a leitura durável de um run concluído DECIFRA a
// captura do turno terminal. Deixa UM selo WORM, com a capability da leitura de desfecho e o
// principal do leitor, e o selo já está na cadeia quando o conteúdo é aberto. Um leitor que o
// gate não admite não recebe conteúdo, não deixa selo e não chega ao cifrador.
func TestAOS494_RamoDuravel_SelaAntesDeDecifrar(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	mem := aos494Concluir(t, dir, vault)

	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos494Modelo(""), true)
	defer aos494Fechar(t, node, svc)
	if _, emMemoria := svc.Outcome(aos494RunID); emMemoria {
		t.Fatal("depois do reinicio o desfecho nao pode estar em memoria: o teste nao exercitava o ramo duravel")
	}
	particao := readAuditPartition(aos494RunID)
	cabeca := func() uint64 {
		n, err := node.WORM.Head(ctx, particao)
		if err != nil {
			t.Fatalf("WORM.Head: %v", err)
		}
		return n
	}
	var aberturas int
	var cabecaNaPrimeiraAbertura uint64
	node.contentOpener = &aos494OpenerEspiao{dentro: node.contentOpener, aoAbrir: func() {
		if aberturas == 0 {
			cabecaNaPrimeiraAbertura = cabeca()
		}
		aberturas++
	}}

	// (1) Quem o gate não admite: 404 uniforme, sem conteúdo, sem selo, sem abrir nada.
	antes := cabeca()
	for nome, cabecalhos := range map[string]map[string]string{
		"board desconhecido": {HeaderReaderPrincipal: "nhi:intruso", HeaderReaderBoard: "board:desconhecido"},
		"sem credencial":     nil,
	} {
		rec := getReq(h, "/runs/"+aos494RunID, cabecalhos)
		if rec.Code != http.StatusNotFound || bytes.Contains(rec.Body.Bytes(), []byte(aos494Documento)) {
			t.Fatalf("leitor nao admitido (%s) no ramo duravel: %d %s", nome, rec.Code, rec.Body.String())
		}
	}
	if cabeca() != antes || aberturas != 0 {
		t.Fatalf("um leitor nao admitido deixou selo (%d→%d) ou chegou ao cifrador (%d abertura(s))", antes, cabeca(), aberturas)
	}

	// (2) O leitor admitido: a saída, UM selo, e o selo antes de decifrar.
	_, dur := aos494Ler(t, h, govHeaders())
	if dur.FinalText != mem.FinalText || dur.OutputUnavailable {
		t.Fatalf("o ramo duravel tinha de devolver a saida do log; veio %+v", dur)
	}
	depois := cabeca()
	if depois != antes+1 {
		t.Fatalf("a leitura duravel que decifra deixa UM selo WORM; a cadeia foi de %d para %d", antes, depois)
	}
	selo, ok, err := node.WORM.At(ctx, particao, depois)
	if err != nil || !ok {
		t.Fatalf("WORM.At(%d): ok=%v err=%v", depois, ok, err)
	}
	if selo.Capability != capReadOutcome || selo.Principal.NHIID != govReader || selo.RunID != aos494RunID || selo.Decision != audit.DecisionAllow {
		t.Fatalf("selo da leitura duravel: capability=%q principal=%q run=%q decision=%q; quero %q, %q, %q, allow",
			selo.Capability, selo.Principal.NHIID, selo.RunID, selo.Decision, capReadOutcome, govReader, aos494RunID)
	}
	if aberturas == 0 {
		t.Fatal("o ramo duravel devolveu texto sem abrir conteudo: o teste nao mediu a decifracao")
	}
	if cabecaNaPrimeiraAbertura != depois {
		t.Fatalf("o conteudo foi aberto com a cadeia em %d, e o selo desta leitura e o %d: decifrou-se ANTES de selar", cabecaNaPrimeiraAbertura, depois)
	}
}

// TestAOS494_RamoDuravel_CustodiaFechadaNaoESaidaIndisponivel: com a custódia das KEK fechada —
// o Vault selado ou sem resposta, não um apagamento — o ramo durável responde 503, como o
// `/reconstruct`, e NÃO `output_unavailable`. Quando a custódia volta, a mesma leitura devolve
// a saída. Com o titular apagado de vez, aí sim, `output_unavailable`.
func TestAOS494_RamoDuravel_CustodiaFechadaNaoESaidaIndisponivel(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	dir := t.TempDir()
	cofre := &aos494CofreComPortao{InMemoryKeyVault: audit.NewInMemoryKeyVault(nil)}
	mem := aos494Concluir(t, dir, cofre)

	node, svc, h := aos494Compor(t, dir, cofre, agentruntime.CompletionEnforce, aos494Modelo(""), true)

	// (1) Custódia FECHADA: 503 nas duas rotas que decifram, e nada que se pareça com um desfecho.
	cofre.fechado.Store(true)
	for _, rota := range []string{"/runs/" + aos494RunID, "/runs/" + aos494RunID + "/reconstruct"} {
		rec := getReq(h, rota, govHeaders())
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s com a custodia fechada: queria 503, veio %d (%s)", rota, rec.Code, rec.Body.String())
		}
		for _, proibido := range []string{aos494Documento, "output_unavailable", `"status"`} {
			if strings.Contains(rec.Body.String(), proibido) {
				t.Fatalf("GET %s com a custodia fechada nao pode levar %q: %s", rota, proibido, rec.Body.String())
			}
		}
	}

	// (2) A custódia VOLTA: a mesma leitura devolve a saída. Nada foi dado por perdido.
	cofre.fechado.Store(false)
	_, dur := aos494Ler(t, h, govHeaders())
	if dur.Status != "completed" || !dur.Terminated || dur.FinalText != mem.FinalText || dur.OutputUnavailable {
		t.Fatalf("com a custodia de volta o ramo duravel devolve a saida; veio %+v", dur)
	}
	aos494Fechar(t, node, svc)

	// (3) O titular APAGADO de vez (a KEK já não existe): `output_unavailable`, sem texto, e o
	// `/reconstruct` responde 410. É o que o `aos-orq` lê como `saida_indisponivel`.
	node3, svc3, h3 := aos494Compor(t, dir, audit.NewInMemoryKeyVault(nil), agentruntime.CompletionEnforce, aos494Modelo(""), true)
	defer aos494Fechar(t, node3, svc3)
	cru, sem := aos494Ler(t, h3, govHeaders())
	if sem.Status != "completed" || !sem.Terminated || !sem.OutputUnavailable || sem.FinalText != "" || bytes.Contains(cru, []byte(aos494Documento)) {
		t.Fatalf("sem a KEK do titular: queria completed + output_unavailable, sem texto; veio %s", cru)
	}
	aos494Fio(t, "duravel-saida-indisponivel", cru)
	if rec := getReq(h3, "/runs/"+aos494RunID+"/reconstruct", govHeaders()); rec.Code != http.StatusGone {
		t.Fatalf("o /reconstruct de um titular apagado responde 410; veio %d", rec.Code)
	}
}

// TestAOS494_SaidaDuravel_ClassificacaoDosErros: a lista dos erros DEFINITIVOS é fechada. Tudo o
// que não está nela — a custódia indisponível, o Event Store que não leu, um erro que ninguém
// classificou — é transitório, e o `GET` responde 503.
func TestAOS494_SaidaDuravel_ClassificacaoDosErros(t *testing.T) {
	for _, c := range []struct {
		nome   string
		err    error
		deVez  bool
		status int // o que o `/reconstruct` responde ao mesmo erro; 0 ⇒ não se compara
	}{
		{"titular apagado", audit.ErrDecrypt, true, http.StatusGone},
		{"titular apagado, embrulhado", fmt.Errorf("replay: turno 2: %w", audit.ErrDecrypt), true, http.StatusGone},
		{"sem capturas", replay.ErrNoTrajectory, true, http.StatusNotFound},
		{"captura incompleta", replay.ErrIncompleteCapture, true, http.StatusUnprocessableEntity},
		{"captura corrompida", replay.ErrCorruptCapture, true, 0},
		{"gate do opener nega", replay.ErrPayloadAccessDenied, true, http.StatusForbidden},
		{"log cifrado sem cifra", durable.ErrSealedResultNoCipher, true, 0},
		{"no sem gate de leitura", errSaidaDuravelSemGate, true, 0},
		{"reconstrucao sem turnos", errSaidaDuravelSemTurnos, true, 0},

		{"custodia fechada", durable.ErrConteudoIndisponivel, false, http.StatusServiceUnavailable},
		{"custodia sem resposta com a KEK por verificar", fmt.Errorf("%w: %w", durable.ErrConteudoIndisponivel, audit.ErrDecrypt), false, http.StatusServiceUnavailable},
		{"event store fechado", eventstore.ErrClosed, false, 0},
		{"event store sem quorum", eventstore.ErrNoQuorum, false, 0},
		{"prazo do pedido", context.DeadlineExceeded, false, 0},
		{"erro que ninguem classificou", errors.New("falha desconhecida"), false, 0},
	} {
		if got := saidaDuravelIndisponivelDeVez(c.err); got != c.deVez {
			t.Errorf("%s: indisponivel de vez = %v, quero %v", c.nome, got, c.deVez)
		}
		if c.status != 0 {
			if got := reconstructErrorStatus(c.err); got != c.status {
				t.Errorf("%s: o /reconstruct responde %d, e este teste supunha %d", c.nome, got, c.status)
			}
			// As duas rotas concordam em qual é o erro transitório: 503 de um lado, não-definitivo do outro.
			if (c.status == http.StatusServiceUnavailable) == c.deVez {
				t.Errorf("%s: o /reconstruct responde %d e o ramo duravel diz deVez=%v — as duas rotas discordam", c.nome, c.status, c.deVez)
			}
		}
	}
}

// TestAOS494_RamoDuravel_StreamQueNaoEDeRunNaoSeDecifra: a trava do AOS-426 no ramo que decifra.
// Um stream com o estado `complete` e com eventos de OUTRO dono não é o stream de um run: 404
// uniforme, sem selo e sem abrir conteúdo. Sem o evento alheio, o mesmo stream serve a saída.
func TestAOS494_RamoDuravel_StreamQueNaoEDeRunNaoSeDecifra(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	aos494Concluir(t, dir, vault)

	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos494Modelo(""), true)
	defer aos494Fechar(t, node, svc)
	if deRun, err := (&apiHandler{node: node, svc: svc}).streamDuravelEDeRun(ctx, aos494RunID); err != nil || !deRun {
		t.Fatalf("pre-condicao: o stream de um run e de run (deRun=%v err=%v)", deRun, err)
	}
	if _, dur := aos494Ler(t, h, govHeaders()); dur.FinalText != aos494Boa {
		t.Fatalf("pre-condicao: o ramo duravel serve a saida; veio %+v", dur)
	}

	// Um evento de OUTRO dono no mesmo stream — o que um stream interno do nó tem em todos.
	if _, err := node.EventStore.Append(ctx, aos494RunID, eventstore.EventInput{
		Type: EventTypeRunPlanOrigin, Payload: json.RawMessage(`{}`), RunID: "outro-dono", StepID: "intruso-1",
	}); err != nil {
		t.Fatalf("Append do evento alheio: %v", err)
	}
	particao := readAuditPartition(aos494RunID)
	antes, _ := node.WORM.Head(ctx, particao)
	var aberturas int
	node.contentOpener = &aos494OpenerEspiao{dentro: node.contentOpener, aoAbrir: func() { aberturas++ }}

	rec := getReq(h, "/runs/"+aos494RunID, govHeaders())
	if rec.Code != http.StatusNotFound || bytes.Contains(rec.Body.Bytes(), []byte(aos494Documento)) {
		t.Fatalf("um stream com eventos de outro dono nao se decifra: queria 404, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if depois, _ := node.WORM.Head(ctx, particao); depois != antes || aberturas != 0 {
		t.Fatalf("a recusa da trava deixou selo (%d→%d) ou abriu conteudo (%d)", antes, depois, aberturas)
	}
}

// ---------------------------------------------------------------------------------------------
// AS TRÊS DIRECÇÕES DO FIO (I5), na forma de produção: nó `read_notes`, tool `doc_read`.
// ---------------------------------------------------------------------------------------------

// aos495FioDoAosOrq é a pasta dos ficheiros que o teste do `aos-orq` GERA.
var aos495FioDoAosOrq = filepath.Join("..", "aos-orq", "testdata", "aos495_fio")

// aos494NoDeProducao compõe o nó real com o gateway real e UMA tool chamada `doc_read`, lida do
// `AOS_MODEL_TOOLS` com os três eixos de risco declarados — a forma do nó de produção.
func aos494NoDeProducao(t *testing.T, modo agentruntime.CompletionMode) *aos486No {
	t.Helper()
	ordem, extra := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec
	aos486OrdemDoFicheiro = []string{"doc_read"}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	t.Cleanup(func() { aos486OrdemDoFicheiro, aos486CamposExtraDaSpec = ordem, extra })
	n := aos486ComporCom(t, "native", func(cfg *Config) { cfg.CompletionVerdict = modo })
	// O catalogo do `GET /tools` compoe-se como o `main` o compoe: do mesmo manifesto que o no
	// oferece ao modelo.
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	if n.h, err = NewAPIHandler(n.svc, n.node, WithToolCatalog(cat)); err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return n
}

// TestAOS494_Fio_AnuncioDoGetTools GERA o anúncio: o `GET /tools` do nó real, com o catálogo e o
// `completion_contract`. O teste do `aos-orq` serve estes bytes ao seu cliente.
func TestAOS494_Fio_AnuncioDoGetTools(t *testing.T) {
	for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionEnforce, agentruntime.CompletionObserve} {
		t.Run(string(modo), func(t *testing.T) {
			n := aos494NoDeProducao(t, modo)
			rec := postJSON(n.h, http.MethodGet, "/tools", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /tools: %d (%s)", rec.Code, rec.Body.String())
			}
			var corpo struct {
				Tools              []entradaDoCatalogo `json:"tools"`
				CompletionContract *anuncioDoContrato  `json:"completion_contract"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &corpo); err != nil {
				t.Fatalf("resposta ilegivel: %v", err)
			}
			if len(corpo.Tools) != 1 || corpo.Tools[0].Name != "doc_read" || corpo.CompletionContract == nil || corpo.CompletionContract.Mode != modo {
				t.Fatalf("o anuncio de producao tem a tool doc_read e o modo %q; veio %s", modo, rec.Body.String())
			}
			aos494Fio(t, "tools-"+string(modo), rec.Body.Bytes())
		})
	}
}

// aos494CorpoDoAosOrq lê o corpo do `POST /runs` que o `aos-orq` REAL enviou, gravado pelo teste
// dele (`TestAOS495_Fio_…`).
func aos494CorpoDoAosOrq(t *testing.T) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join(aos495FioDoAosOrq, "post-runs-read_notes.json"))
	if err != nil {
		if aos494Actualizar() {
			t.Skipf("o corpo do POST /runs ainda nao foi gerado (%v): corre primeiro o teste do aos-orq com AOS494_ACTUALIZAR_FIO=1, e este outra vez", err)
		}
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o no aos-orq com AOS494_ACTUALIZAR_FIO=1", err)
	}
	return bytes.TrimSpace(cru)
}

// TestAOS494_Fio_PostRunsDoAosOrq CONSOME o corpo do `POST /runs` que o `aos-orq` real enviou
// para o nó `read_notes` de um plano, com a forma de produção — `tools:["doc_read"]`,
// `completion_requires:["doc_read"]`.
//
// Em duas partes, e a razão de serem duas está no que o corpo leva:
//
//  1. BYTE A BYTE. O corpo, sem lhe tocar, atravessa o decoder estrito do nó e todas as
//     recusas de pedido (run_id, lista-branca, contrato, inputs): a resposta é a 403 da
//     autenticação, e não um 400. Um campo que o nó não conhecesse, ou um contrato fora da
//     lista-branca, parava aqui com 400.
//  2. O RUN. Os mesmos bytes, trocando SÓ o que não pode ser fixado num ficheiro — a
//     credencial (um token assinado, com validade) e o principal — e tirando o vínculo
//     `plan_request`, que exige a reclamação viva do pedido na fila deste nó. Tudo o resto,
//     incluindo o `run_id`, a lista-branca e o contrato, são os bytes do `aos-orq`. As respostas
//     do `GET /runs/{id}` ficam em `testdata/aos494_fio/producao-*.json`, que o `aos-orq`
//     consome nos testes dele.
func TestAOS494_Fio_PostRunsDoAosOrq(t *testing.T) {
	cru := aos494CorpoDoAosOrq(t)
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cru, &campos); err != nil {
		t.Fatalf("o corpo do aos-orq nao e JSON: %v", err)
	}
	// A forma de produção, lida dos bytes: é isto que o teste fixa do lado de quem consome.
	for campo, quer := range map[string]string{"tools": `["doc_read"]`, "completion_requires": `["doc_read"]`} {
		if string(campos[campo]) != quer {
			t.Fatalf("o corpo do aos-orq tem %s = %s; a forma de producao e %s", campo, campos[campo], quer)
		}
	}
	var runID string
	if err := json.Unmarshal(campos["run_id"], &runID); err != nil || !strings.HasSuffix(runID, "~read_notes") {
		t.Fatalf("o run_id do corpo tem de ser o do no read_notes; veio %s (%v)", campos["run_id"], err)
	}
	if _, tem := campos["plan_request"]; !tem {
		t.Fatal("o corpo de producao leva o vinculo plan_request: o ficheiro nao e o de um consume")
	}

	t.Run("ByteAByte_PassaODecoderEAsRecusasDePedido", func(t *testing.T) {
		n := aos494NoDeProducao(t, agentruntime.CompletionEnforce)
		req := httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(cru))
		rec := aos494Servir(n.h, req)
		// 403 é a recusa do VÍNCULO (este nó não autentica quem chama): vem depois do decoder,
		// da validação do run_id, da lista-branca, do contrato e dos inputs.
		if rec.Code != http.StatusForbidden {
			t.Fatalf("o corpo do aos-orq, byte a byte, tinha de passar todas as recusas de PEDIDO e parar na autenticacao (403); veio %d (%s)", rec.Code, rec.Body.String())
		}
	})

	producao := aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"]
	for _, c := range []struct {
		nome  string
		modo  agentruntime.CompletionMode
		texto string // vazio ⇒ o modelo pede `doc_read` e depois conclui
	}{
		{"producao-enforce", agentruntime.CompletionEnforce, producao},
		{"producao-observe", agentruntime.CompletionObserve, producao},
		{"producao-boa-enforce", agentruntime.CompletionEnforce, ""},
	} {
		t.Run(c.nome, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			n := aos494NoDeProducao(t, c.modo)
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
			bruto, err := json.Marshal(corpo)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/runs", bytes.NewReader(bruto))
			if rec := aos494Servir(n.h, req); rec.Code != http.StatusCreated {
				t.Fatalf("POST /runs com o corpo do aos-orq: %d (%s)", rec.Code, rec.Body.String())
			}
			wc, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, ok, werr := n.svc.Wait(wc, runID); werr != nil || !ok {
				t.Fatalf("o run devia ter sido hospedado e acabado: ok=%v err=%v", ok, werr)
			}
			get := postJSON(n.h, http.MethodGet, "/runs/"+runID, nil)
			if get.Code != http.StatusOK {
				t.Fatalf("GET /runs/%s: %d (%s)", runID, get.Code, get.Body.String())
			}
			var r aos494Resposta
			if err := json.Unmarshal(get.Body.Bytes(), &r); err != nil {
				t.Fatalf("resposta ilegivel: %v", err)
			}
			// O vector nomeia a tool do contrato que o `aos-orq` enviou, nos três casos.
			if r.Verdict == nil || len(r.Verdict.Tools) != 1 || r.Verdict.Tools[0].Tool != "doc_read" {
				t.Fatalf("o vector tem de ter a linha da tool do contrato (doc_read); veio %+v", r.Verdict)
			}
			execs := atomic.LoadInt64(n.execs["doc_read"])
			switch c.nome {
			case "producao-enforce":
				if r.Status != "failed" || r.Terminated || r.FinalText != "" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || execs != 0 {
					t.Fatalf("a resposta de producao em enforce: failed, com a razao, sem texto e sem a tool correr; veio %+v (execs=%d)", r, execs)
				}
			case "producao-observe":
				if r.Status != "completed" || !r.Terminated || r.FinalText != c.texto || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || execs != 0 {
					t.Fatalf("a resposta de producao em observe: completed com o texto e a razao observada; veio %+v (execs=%d)", r, execs)
				}
			default:
				if r.Status != "completed" || !r.Terminated || r.OutcomeReason != "" || !r.Verdict.Fulfilled || r.Verdict.Tools[0].Effective != 1 || execs != 1 {
					t.Fatalf("a resposta boa: completed, contrato cumprido, doc_read efectiva uma vez; veio %+v %+v (execs=%d)", r, r.Verdict, execs)
				}
			}
			aos494Fio(t, c.nome, get.Body.Bytes())
		})
	}
}

// TestAOS494_Fio_RamoDuravel GERA a resposta do ramo durável de um run concluído, depois de um
// reinício, para o `aos-orq` a consumir. (A resposta `output_unavailable` é gerada pelo
// TestAOS494_RamoDuravel_CustodiaFechadaNaoESaidaIndisponivel.)
func TestAOS494_Fio_RamoDuravel(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	dir := t.TempDir()
	vault := audit.NewInMemoryKeyVault(nil)
	mem := aos494Concluir(t, dir, vault)

	node, svc, h := aos494Compor(t, dir, vault, agentruntime.CompletionEnforce, aos494Modelo(""), true)
	defer aos494Fechar(t, node, svc)
	cru, dur := aos494Ler(t, h, govHeaders())
	if dur.Status != "completed" || !dur.Terminated || dur.FinalText != mem.FinalText || dur.OutputUnavailable || dur.Verdict == nil {
		t.Fatalf("o ramo duravel devolve o desfecho, o vector e a saida; veio %+v", dur)
	}
	aos494Fio(t, "duravel-boa-enforce", cru)
}
