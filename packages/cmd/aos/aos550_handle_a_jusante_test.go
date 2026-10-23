package main

// AOS-550 — O HANDLE RESOLVE-SE A JUSANTE DO RM (fase R2 do ADR-043).
//
// O que estes testes impõem, pela ordem dos Critérios de Aceitação:
//
//   - a quarentena lê do ARMAZÉM por `(titular, run, digest)`, e os ids são uma função pura do
//     stream — a mesma derivação ao vivo e em reconstrução;
//   - um argumento de tool pode ser um handle, e a troca pelos bytes acontece DEPOIS da
//     decisão do RM: o que o RM vê, o que fica no tail e o que fica nos selos é a referência;
//   - um handle forjado é recusado por duas condições independentes — (a) só uma tool de
//     código do nó cunha um handle, (b) o par `(run, digest)` tem de constar de um
//     `artifact.created` do stream do run;
//   - o tecto do corpo de um `tool_result` existe, é imposto e a recusa é nomeada;
//   - os contadores do armazém são publicados em `/metrics`;
//   - desligado por omissão: o resultado da tool é byte-idêntico ao de antes.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos550Armazem abre um armazém com o plano de handles ligado e um registo de proveniência em
// memória, de que a leitura dos artefactos declarados (a condição (b)) é a projecção fiel.
func aos550Armazem(t *testing.T) (*armazemDeArtefactos, *[]artefactoCriado) {
	t.Helper()
	a, _, _ := aos548Armazem(t, 1<<30)
	declarados := map[string][]artefactoCriado{}
	var todos []artefactoCriado
	a.registar = func(_ context.Context, runID string, p artefactoCriado) error {
		declarados[runID] = append(declarados[runID], p)
		todos = append(todos, p)
		return nil
	}
	a.handles = &planoDeHandles{armazem: a, declarados: func(_ context.Context, runID string) ([]artefactoCriado, error) {
		return declarados[runID], nil
	}}
	return a, &todos
}

// aos550Entrada é a entrada da tool DEPOIS da reescrita do efeito (o run, o passo e o titular
// vêm da Call real, nunca do modelo).
func aos550Entrada(runID, stepID, nome, tipo, conteudo string) []byte {
	b, _ := json.Marshal(entradaDoArtefacto{Schema: artifactInputSchema, RunID: runID, StepID: stepID,
		Subject: "titular-550", Name: nome, MediaType: tipo, Content: conteudo})
	return b
}

// aos550Ref lê o campo `handle` do resultado da tool.
func aos550Ref(t *testing.T, out []byte) string {
	t.Helper()
	var ref struct {
		Handle string `json:"handle"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatalf("resultado da tool: %v (%s)", err, out)
	}
	return ref.Handle
}

func TestAOS550_Env(t *testing.T) {
	t.Setenv("AOS_HANDLE_NOS_ARGUMENTOS", "")
	if on, err := parseHandleNosArgumentosFromEnv(); on || err != nil {
		t.Fatalf("omissao: %v %v", on, err)
	}
	t.Setenv("AOS_HANDLE_NOS_ARGUMENTOS", "off")
	if on, err := parseHandleNosArgumentosFromEnv(); on || err != nil {
		t.Fatalf("off: %v %v", on, err)
	}
	t.Setenv("AOS_HANDLE_NOS_ARGUMENTOS", "on")
	if on, err := parseHandleNosArgumentosFromEnv(); !on || err != nil {
		t.Fatalf("on: %v %v", on, err)
	}
	for _, mau := range []string{"ON", "1", "true", "sim", "handle"} {
		t.Setenv("AOS_HANDLE_NOS_ARGUMENTOS", mau)
		if _, err := parseHandleNosArgumentosFromEnv(); !errors.Is(err, ErrBadHandleNosArgumentos) {
			t.Errorf("%q: %v", mau, err)
		}
	}
	if handlesBanner(nil) != nil {
		t.Error("banner sem plano")
	}
}

// TestAOS550_ComposicaoFailClosed: `on` sem armazém ou sem Event Store ABORTA o arranque. Um
// plano ligado sem onde pôr o conteúdo, ou sem como verificar a proveniência, seria um plano
// que diz que protege e não protege.
func TestAOS550_ComposicaoFailClosed(t *testing.T) {
	t.Parallel()
	a, _, _ := aos548Armazem(t, 1<<30)
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	if p, err := comporPlanoDeHandles(false, nil, nil); p != nil || err != nil {
		t.Fatalf("desligado: %v %v", p, err)
	}
	if _, err := comporPlanoDeHandles(true, nil, es); !errors.Is(err, ErrHandleSemArmazem) {
		t.Fatalf("sem armazem: %v", err)
	}
	if _, err := comporPlanoDeHandles(true, a, nil); !errors.Is(err, ErrHandleSemEventStore) {
		t.Fatalf("sem event store: %v", err)
	}
	p, err := comporPlanoDeHandles(true, a, es)
	if err != nil || p == nil {
		t.Fatalf("ligado: %v %v", p, err)
	}
	// O caminho de ENTRADA directa em quarentena (sem evento de proveniência) é fail-closed e
	// nomeado: uma gravação sem evento produziria um handle que a condição (b) recusa.
	if _, err := (quarentenaDoArmazem{plano: p}).SealQuarantined(context.Background(), "t", "r", []byte("x")); !errors.Is(err, ErrSelarEmQuarentenaPorLigar) {
		t.Fatalf("selar sem evento: %v", err)
	}
}

// TestAOS550_IdsSaoFuncaoPuraDoStream: `h1`, `h2`, … pela ordem dos eventos `artifact.created`
// do run. A derivação é a MESMA ao vivo (o handle que a tool devolve) e em reconstrução (a
// quarentena rebuilt a partir dos eventos), pelo que não há dois caminhos que possam divergir —
// é o que o replay por dobra pura exige.
func TestAOS550_IdsSaoFuncaoPuraDoStream(t *testing.T) {
	ctx := context.Background()
	a, _ := aos550Armazem(t)
	primeiro, err := a.executar(ctx, aos550Entrada("run-1", "s1", "a.json", "application/json", `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	segundo, err := a.executar(ctx, aos550Entrada("run-1", "s2", "b.json", "application/json", `{"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := aos550Ref(t, primeiro); h != agentruntime.HandleRefPrefix+"h1" {
		t.Fatalf("1.º handle = %q", h)
	}
	if h := aos550Ref(t, segundo); h != agentruntime.HandleRefPrefix+"h2" {
		t.Fatalf("2.º handle = %q", h)
	}
	// RECONSTRUÇÃO: a quarentena rebuilt dos eventos dá os mesmos ids e os mesmos digests.
	q, err := a.handles.quarentenaDoRun(ctx, "titular-550", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	hs := q.Handles()
	if len(hs) != 2 || hs[0].String() != "h1" || hs[1].String() != "h2" {
		t.Fatalf("reconstrucao: %v", hs)
	}
	var refs []struct {
		Digest    string `json:"digest"`
		MediaType string `json:"media_type"`
		Bytes     int    `json:"bytes"`
		Name      string `json:"name"`
	}
	for _, out := range [][]byte{primeiro, segundo} {
		var r struct {
			Digest    string `json:"digest"`
			MediaType string `json:"media_type"`
			Bytes     int    `json:"bytes"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, r)
	}
	for i, h := range hs {
		if h.Digest() != refs[i].Digest {
			t.Fatalf("handle %s: digest %q, queria %q", h, h.Digest(), refs[i].Digest)
		}
		f := h.Facts()
		// OS FACTOS SÃO DE NÍVEL-ESQUEMA e são o que o NÓ afirma: tipo, bytes, nome, validade.
		if f.MediaType != refs[i].MediaType || f.Bytes != int64(refs[i].Bytes) || f.Name != refs[i].Name || !f.WellFormed {
			t.Fatalf("handle %s: factos %+v", h, f)
		}
		// E nenhum deles leva conteúdo.
		if strings.Contains(f.Name+f.MediaType, `{"a"`) {
			t.Fatalf("handle %s leva conteudo nos factos", h)
		}
	}
	// O MESMO conteúdo gravado outra vez no run: outro evento, outro id, o mesmo digest.
	terceiro, err := a.executar(ctx, aos550Entrada("run-1", "s3", "a-copia.json", "application/json", `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := aos550Ref(t, terceiro); h != agentruntime.HandleRefPrefix+"h3" {
		t.Fatalf("3.º handle = %q", h)
	}
}

// TestAOS550_ResolveOArgumentoDepoisDaDecisao: o `content` de uma `artifact_write` pode ser
// EXACTAMENTE um handle. O executor — que o RM invoca só em `permit` — troca-o pelos bytes do
// armazém, e o resultado da tool continua a ser a referência: nenhum byte do conteúdo volta
// para cima.
func TestAOS550_ResolveOArgumentoDepoisDaDecisao(t *testing.T) {
	ctx := context.Background()
	a, declarados := aos550Armazem(t)
	const segredo = `{"comodos":["SALA-SEGREDO-550","WC"]}`
	primeiro, err := a.executar(ctx, aos550Entrada("run-1", "s1", "comodos.json", "application/json", segredo))
	if err != nil {
		t.Fatal(err)
	}
	ref := aos550Ref(t, primeiro)
	// A SEGUNDA chamada leva a REFERÊNCIA no argumento, não os bytes.
	out, err := a.executar(ctx, aos550Entrada("run-1", "s2", "copia.json", "application/json", ref))
	if err != nil {
		t.Fatalf("resolucao: %v", err)
	}
	// O conteúdo gravado é o do handle — o nó trocou a referência pelos bytes.
	var r struct {
		Digest string `json:"digest"`
		Bytes  int    `json:"bytes"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r.Bytes != len(segredo) {
		t.Fatalf("bytes gravados = %d, queria %d (os do handle)", r.Bytes, len(segredo))
	}
	claro, err := a.abrir(ctx, "titular-550", "run-1", r.Digest)
	if err != nil || string(claro) != segredo {
		t.Fatalf("conteudo resolvido: %q %v", claro, err)
	}
	// O RESULTADO DA TOOL não leva um byte do conteúdo — nem o segundo, nem o primeiro.
	for _, saida := range [][]byte{primeiro, out} {
		if strings.Contains(string(saida), "SALA-SEGREDO-550") {
			t.Fatalf("o resultado da tool leva o conteudo: %s", saida)
		}
	}
	// A proveniência do artefacto resolvido é selada como qualquer outra, sem conteúdo.
	if len(*declarados) != 2 || (*declarados)[1].Name != "copia.json" || (*declarados)[1].Bytes != len(segredo) {
		t.Fatalf("proveniencia: %+v", *declarados)
	}
	if a.handles.resolvidos.Load() != 1 || a.handles.cunhados.Load() != 2 {
		t.Fatalf("contadores: resolvidos=%d cunhados=%d", a.handles.resolvidos.Load(), a.handles.cunhados.Load())
	}
	// O TIPO DECLARADO CONTINUA A VALER sobre os bytes resolvidos: um handle de JSON declarado
	// como SVG é recusado pelo validador do conteúdo, como se os bytes tivessem vindo inline.
	if _, err := a.executar(ctx, aos550Entrada("run-1", "s3", "p.svg", "image/svg+xml", ref)); err == nil {
		t.Fatal("um handle de JSON passou como SVG")
	}
}

// TestAOS550_CondicaoA_SoCodigoDoNoCunhaHandle: a referência que o modelo escreve é reconhecida
// pela forma, e isso NÃO é autoridade. Um id que nenhuma gravação do nó cunhou é recusado com
// causa nomeada, e um texto parecido com uma referência é só texto.
func TestAOS550_CondicaoA_SoCodigoDoNoCunhaHandle(t *testing.T) {
	ctx := context.Background()
	a, _ := aos550Armazem(t)
	// Um run sem artefactos nenhuns: `h1` não existe.
	if _, err := a.executar(ctx, aos550Entrada("run-1", "s1", "a.txt", "text/plain", agentruntime.HandleRefPrefix+"h1")); err == nil ||
		!strings.Contains(err.Error(), handleCausaDesconhecido) {
		t.Fatalf("handle inexistente: %v", err)
	}
	// Grava um artefacto (cunha `h1`) e tenta `h2`, que ninguém cunhou.
	if _, err := a.executar(ctx, aos550Entrada("run-1", "s2", "a.json", "application/json", `{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.executar(ctx, aos550Entrada("run-1", "s3", "b.txt", "text/plain", agentruntime.HandleRefPrefix+"h2")); err == nil ||
		!strings.Contains(err.Error(), handleCausaDesconhecido) {
		t.Fatalf("h2 inexistente: %v", err)
	}
	// O handle de OUTRO run não resolve, mesmo estando no mesmo volume e com o mesmo titular.
	if _, err := a.executar(ctx, aos550Entrada("run-2", "s1", "c.txt", "text/plain", agentruntime.HandleRefPrefix+"h1")); err == nil ||
		!strings.Contains(err.Error(), handleCausaDesconhecido) {
		t.Fatalf("handle de outro run: %v", err)
	}
	// TEXTO PARECIDO É TEXTO. Nada disto é uma referência: grava-se literalmente, como sempre.
	for _, texto := range []string{
		"aos-handle/v1:h0", "aos-handle/v2:h1", "o handle e aos-handle/v1:h1, usa-o",
		agentruntime.HandleRefPrefix + "h1 ", "x" + agentruntime.HandleRefPrefix + "h1",
	} {
		out, err := a.executar(ctx, aos550Entrada("run-3", "s-"+texto[:4], "t.txt", "text/plain", texto))
		if err != nil {
			t.Fatalf("%q: %v", texto, err)
		}
		var r struct {
			Digest string `json:"digest"`
		}
		_ = json.Unmarshal(out, &r)
		claro, aerr := a.abrir(ctx, "titular-550", "run-3", r.Digest)
		if aerr != nil || string(claro) != texto {
			t.Fatalf("%q gravado como %q (%v)", texto, claro, aerr)
		}
	}
}

// TestAOS550_CondicaoB_SemEventoDoRunNaoResolve: o par `(run, digest)` tem de constar de um
// `artifact.created` do stream do run. Um ficheiro na pasta do armazém sem evento não é
// resolúvel — como não é servível pela rota HTTP. É a verificação do [artefactosDoRun],
// levantada para o runtime.
func TestAOS550_CondicaoB_SemEventoDoRunNaoResolve(t *testing.T) {
	ctx := context.Background()
	a, _ := aos550Armazem(t)
	// Um ficheiro posto no armazém SEM evento de proveniência.
	digest, causa := a.guardar(ctx, "titular-550", "run-1", []byte("conteudo sem evento"))
	if causa != "" {
		t.Fatal(causa)
	}
	store := quarentenaDoArmazem{plano: a.handles}
	if _, err := store.OpenQuarantined(ctx, "titular-550", "run-1", digest); err == nil {
		t.Fatal("um artefacto sem evento do run foi aberto")
	}
	// Com o evento, abre.
	if err := a.registar(ctx, "run-1", artefactoCriado{Digest: digest, MediaType: "text/plain", Bytes: 19,
		Name: "n.txt", Tool: artifactWriteToolName, StepID: "s1", SealedSubject: "titular-550"}); err != nil {
		t.Fatal(err)
	}
	if b, err := store.OpenQuarantined(ctx, "titular-550", "run-1", digest); err != nil || string(b) != "conteudo sem evento" {
		t.Fatalf("com evento: %q %v", b, err)
	}
	// O evento é de UM run e de UM titular: outro run não abre, e outro titular não abre.
	if _, err := store.OpenQuarantined(ctx, "titular-550", "run-9", digest); err == nil {
		t.Fatal("o digest resolveu noutro run")
	}
	if _, err := store.OpenQuarantined(ctx, "outro-titular", "run-1", digest); err == nil {
		t.Fatal("o digest resolveu com outro titular")
	}
	// E a quarentena reconstruída de um run sem eventos não tem handle nenhum.
	q, err := a.handles.quarentenaDoRun(ctx, "titular-550", "run-sem-nada")
	if err != nil {
		t.Fatal(err)
	}
	if q.Len() != 0 {
		t.Fatalf("quarentena de um run sem eventos: %d handles", q.Len())
	}
}

// TestAOS550_TectoDoToolResultComRecusaNomeada: o tecto do corpo de um `tool_result` existe
// ([agentruntime.MaxToolResultBytes]), cabe no tecto de 1 MiB por artefacto, e a recusa é
// nomeada e contada. Um artefacto legítimo acima dele continua a descarregar-se pela rota
// soberana — o que o tecto fecha é a passagem dos seus bytes por um argumento de tool.
func TestAOS550_TectoDoToolResultComRecusaNomeada(t *testing.T) {
	ctx := context.Background()
	if agentruntime.MaxToolResultBytes >= artifactMaxBytes {
		t.Fatalf("o tecto do tool_result (%d) tem de caber no tecto por artefacto (%d)", agentruntime.MaxToolResultBytes, artifactMaxBytes)
	}
	a, _ := aos550Armazem(t)
	grande := strings.Repeat("g", agentruntime.MaxToolResultBytes+1)
	out, err := a.executar(ctx, aos550Entrada("run-1", "s1", "grande.txt", "text/plain", grande))
	if err != nil {
		t.Fatalf("o artefacto grande (abaixo de 1 MiB) tinha de ser gravado: %v", err)
	}
	ref := aos550Ref(t, out)
	antes := a.handles.recusados.Load()
	if _, err := a.executar(ctx, aos550Entrada("run-1", "s2", "copia.txt", "text/plain", ref)); err == nil ||
		!strings.Contains(err.Error(), handleCausaTecto) {
		t.Fatalf("acima do tecto: %v", err)
	}
	if a.handles.recusados.Load() != antes+1 {
		t.Fatalf("a recusa nao foi contada")
	}
	// No tecto exacto, resolve.
	noTecto := strings.Repeat("t", agentruntime.MaxToolResultBytes)
	out, err = a.executar(ctx, aos550Entrada("run-2", "s1", "exacto.txt", "text/plain", noTecto))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.executar(ctx, aos550Entrada("run-2", "s2", "copia.txt", "text/plain", aos550Ref(t, out))); err != nil {
		t.Fatalf("no tecto exacto: %v", err)
	}
}

// TestAOS550_DesligadoPorOmissao: sem o plano, o resultado da tool é BYTE-IDÊNTICO ao de antes
// (sem campo `handle`) e uma referência no `content` é texto, gravado literalmente.
func TestAOS550_DesligadoPorOmissao(t *testing.T) {
	ctx := context.Background()
	a, _, _ := aos548Armazem(t, 1<<30)
	var declarados []artefactoCriado
	a.registar = func(_ context.Context, _ string, p artefactoCriado) error {
		declarados = append(declarados, p)
		return nil
	}
	if a.handles != nil {
		t.Fatal("o plano de handles tinha de estar desligado")
	}
	out, err := a.executar(ctx, aos550Entrada("run-1", "s1", "a.json", "application/json", `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "handle") {
		t.Fatalf("desligado, o resultado nao pode ter campo handle: %s", out)
	}
	// Uma referência no content é texto: grava-se tal-qual (e nem existe quarentena para a ler).
	texto := agentruntime.HandleRefPrefix + "h1"
	out, err = a.executar(ctx, aos550Entrada("run-1", "s2", "t.txt", "text/plain", texto))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Digest string `json:"digest"`
		Bytes  int    `json:"bytes"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r.Bytes != len(texto) {
		t.Fatalf("bytes = %d, queria %d", r.Bytes, len(texto))
	}
	claro, err := a.abrir(ctx, "titular-550", "run-1", r.Digest)
	if err != nil || string(claro) != texto {
		t.Fatalf("gravado %q (%v)", claro, err)
	}
	if len(declarados) != 2 {
		t.Fatalf("proveniencia: %+v", declarados)
	}
}

// TestAOS550_No_HandleNoArgumentoPeloRMInteiro é a prova PONTA A PONTA no nó composto: o modelo
// grava um artefacto, recebe o handle, e na chamada seguinte põe o HANDLE no argumento. A call
// passa pelo RM inteiro; o que fica no tail e no selo de mediação é a REFERÊNCIA; e o artefacto
// gravado tem os bytes que o handle nomeia. O `/metrics` publica os contadores do armazém.
func TestAOS550_No_HandleNoArgumentoPeloRMInteiro(t *testing.T) {
	ordem, extra, specs, caps := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486SpecsExtra, aos486CapsExtra
	t.Cleanup(func() {
		aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486SpecsExtra, aos486CapsExtra = ordem, extra, specs, caps
	})
	aos486OrdemDoFicheiro = []string{"doc_read"}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	aos486SpecsExtra = map[string]string{artifactWriteToolName: aos549SpecArtefacto}
	aos486CapsExtra = []string{artifactWriteCapability}
	n := aos486Compor(t, func(cfg *Config) {
		cfg.ArtifactDir, cfg.ArtifactMaxBytes = t.TempDir(), 1<<30
		cfg.HandleNosArgumentos = true
	})
	if n.node.artefactos == nil || n.node.artefactos.handles == nil {
		t.Fatal("o plano de handles nao foi composto")
	}
	if !strings.Contains(n.log.String(), "entradas por handle (AOS-550") {
		t.Fatalf("o banner nao declara as entradas por handle:\n%s", n.log.String())
	}
	const segredo = `{"comodos":["SALA-SEGREDO-E2E","WC"]}`
	var pedidos atomic.Int64
	resposta := func(tool, args string) []byte {
		return []byte(`{"id":"c","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-` + tool + `","type":"function","function":{"name":"` + tool + `","arguments":` + args + `}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	n.upstream.mu.Lock()
	n.upstream.pede = "x"
	n.upstream.responde = func(bool, string) []byte {
		switch pedidos.Add(1) {
		case 1:
			return resposta(artifactWriteToolName, `"{\"name\":\"comodos.json\",\"media_type\":\"application/json\",\"content\":\"{\\\"comodos\\\":[\\\"SALA-SEGREDO-E2E\\\",\\\"WC\\\"]}\"}"`)
		case 2:
			// O MODELO EMITE A REFERÊNCIA, não os bytes.
			return resposta(artifactWriteToolName, `"{\"name\":\"copia.json\",\"media_type\":\"application/json\",\"content\":\"`+agentruntime.HandleRefPrefix+`h1\"}"`)
		default:
			return []byte(`{"id":"f","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
		}
	}
	n.upstream.mu.Unlock()
	runID := "run-aos550-handle"
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Grava a lista dos comodos e depois uma copia dela",
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{artifactWriteToolName}, "max_turns": 4,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	oc, ok, werr := n.svc.Wait(wctx, runID)
	if werr != nil || !ok || oc.Err != nil {
		t.Fatalf("run: ok=%v err=%v oc=%v\n%s", ok, werr, oc.Err, n.log.String())
	}
	eventos, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// AS DUAS GRAVAÇÕES, e a segunda tem os BYTES do handle (mesmo digest que a primeira).
	arts := artefactosDoRun(eventos)
	if len(arts) != 2 || arts[0].Name != "comodos.json" || arts[1].Name != "copia.json" {
		t.Fatalf("artefactos: %+v", arts)
	}
	if arts[1].Digest != arts[0].Digest || arts[1].Bytes != len(segredo) {
		t.Fatalf("a copia nao tem os bytes do handle: %+v", arts[1])
	}
	claro, err := n.node.artefactos.abrir(context.Background(), arts[1].SealedSubject, runID, arts[1].Digest)
	if err != nil || string(claro) != segredo {
		t.Fatalf("conteudo da copia: %q %v", claro, err)
	}
	// A SEGUNDA CALL FOI MEDIADA, e o selo não leva um byte do conteúdo.
	var mediadas int
	for _, e := range eventos {
		if e.Type != referencemonitor.EventTypeMediated {
			continue
		}
		if strings.Contains(string(e.Payload), "SALA-SEGREDO-E2E") {
			t.Fatalf("o selo de mediacao leva o conteudo: %s", e.Payload)
		}
		var p struct {
			ToolID   string `json:"tool_id"`
			Decision string `json:"decision"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.ToolID == artifactWriteToolName && p.Decision == "permit" {
			mediadas++
		}
	}
	if mediadas != 2 {
		t.Fatalf("mediacoes permitidas da %s = %d", artifactWriteToolName, mediadas)
	}
	// O QUE O MODELO VIU: o handle no resultado da 1.ª chamada, e NENHUM byte do conteúdo no
	// segmento que levou a referência de volta. O conteúdo da 1.ª gravação veio do modelo, pelo
	// que aparece nos argumentos DESSA chamada; o da 2.ª não aparece em lado nenhum.
	var turnos int
	for _, e := range eventos {
		if e.Type != "turn.recorded" {
			continue
		}
		turnos++
		if turnos >= 2 && strings.Contains(string(e.Payload), "SALA-SEGREDO-E2E") {
			t.Fatalf("o turno %d leva o conteudo resolvido: %s", turnos, e.Payload)
		}
	}
	if turnos == 0 {
		t.Fatalf("nenhum turno gravado")
	}
	// O /metrics PUBLICA OS CONTADORES DO ARMAZÉM e os do plano de handles.
	m := httptest.NewRecorder()
	n.h.ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if m.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", m.Code)
	}
	for _, serie := range []string{
		`aos_artifact_store_total{resultado="gravados"} 2`,
		`aos_artifact_store_total{resultado="recusados"} 0`,
		`aos_artifact_store_total{resultado="servidos"} 0`,
		`aos_handle_args_total{resultado="cunhados"} 2`,
		`aos_handle_args_total{resultado="resolvidos"} 1`,
		`aos_handle_args_total{resultado="recusados"} 0`,
	} {
		if !strings.Contains(m.Body.String(), serie) {
			t.Errorf("/metrics sem a serie %q", serie)
		}
	}
}
