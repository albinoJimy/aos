package main

// AOS-552 — O TAINT E A LINHAGEM DO ARTEFACTO: proveniência registada, não desclassificação.
//
// Dois eixos, e o segundo é dívida de uma fase já declarada fechada em produção:
//
//   - LINHAGEM: o evento de proveniência passa a levar os digests das entradas em quarentena que
//     produziram o artefacto (ADR-043 §2.3, que o exigia e que o `artefactoCriado` não cumpria). A
//     linhagem viaja DENTRO do efeito, no [bilheteDaCall] da cadeia de reescritas, pelo que faz
//     parte do que o Reference Monitor medeia e sela.
//   - O GUARD-TEST QUE O ADR-043 §8 EXIGE PARA A R1b e que NÃO EXISTIA: «o artefacto é sempre
//     untrusted». O que havia era a invariante genérica do RM e um teste do lado da ENTRADA
//     (`aos549_escrita_local_no_test.go`, que prova a mediação com `Context.Taint == untrusted`).
//     Nenhum teste afirmava o taint do RESULTADO.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/substrate/eventstore"
)

// aos552Espiao devolve o armazém com os handles ligados e um espião dos eventos selados, seguro
// para concorrência.
func aos552Espiao(t *testing.T) (*armazemDeArtefactos, *planoDeHandles, func() []artefactoCriado) {
	t.Helper()
	a, plano := aos550ArmazemComHandles(t)
	var mu sync.Mutex
	var vistos []artefactoCriado
	registar := a.registar
	a.registar = func(c context.Context, runID string, p artefactoCriado) error {
		mu.Lock()
		vistos = append(vistos, p)
		mu.Unlock()
		return registar(c, runID, p)
	}
	return a, plano, func() []artefactoCriado {
		mu.Lock()
		defer mu.Unlock()
		return append([]artefactoCriado(nil), vistos...)
	}
}

// aos552Grava corre a cadeia COMPOSTA de reescritas do nó — a mesma que vai no `EffectRewriter`, e
// a única que transporta o bilhete — e depois o executor da tool. Devolve a referência do handle
// que a gravação cunhou.
func aos552Grava(t *testing.T, a *armazemDeArtefactos, plano *planoDeHandles, runID, stepID, args string) string {
	t.Helper()
	rw := reescritaDoEfeito(nil, nil, nil, true, plano)
	call := referencemonitor.Call{ToolID: artifactWriteToolName, RunID: runID, StepID: stepID,
		Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "titular-550"},
		Input:     []byte(args)}
	reescrita, err := rw(call)
	if err != nil {
		t.Fatalf("reescrita de %s: %v", stepID, err)
	}
	out, err := a.executar(context.Background(), reescrita.Input)
	if err != nil {
		t.Fatalf("executar %s: %v", stepID, err)
	}
	return aos550Ref(t, out)
}

// TestAOS552_LinhagemSeladaNoEvento: o evento leva os digests das entradas, e leva-os porque a
// reescrita que resolveu as referências os anotou no bilhete desta Call — dentro do efeito, não
// por fora dele. Uma gravação inline não tem entradas.
func TestAOS552_LinhagemSeladaNoEvento(t *testing.T) {
	a, plano, eventos := aos552Espiao(t)
	const original = `{"comodos":["SALA","WC"]}`
	ref := aos552Grava(t, a, plano, "run-1", "step-000001-tool-1",
		`{"name":"comodos.json","media_type":"application/json","content":`+strconv.Quote(original)+`}`)
	if vistos := eventos(); len(vistos) != 1 || len(vistos[0].Inputs) != 0 {
		t.Fatalf("uma gravacao inline nao tem entradas: %+v", vistos)
	}
	// DERIVADA: a call seguinte leva o handle da primeira.
	aos552Grava(t, a, plano, "run-1", "step-000002-tool-1",
		`{"name":"copia.json","media_type":"application/json","content":"`+ref+`"}`)
	vistos := eventos()
	if len(vistos) != 2 {
		t.Fatalf("esperava 2 eventos, %d", len(vistos))
	}
	derivado := vistos[1]
	if len(derivado.Inputs) != 1 || derivado.Inputs[0] != vistos[0].Digest {
		t.Fatalf("a linhagem do derivado = %v, queria [%q]", derivado.Inputs, vistos[0].Digest)
	}
	// NENHUM BYTE DO CONTEÚDO no evento, nem no campo novo.
	if bruto, _ := json.Marshal(derivado); strings.Contains(string(bruto), "SALA") {
		t.Fatalf("o evento leva conteudo: %s", bruto)
	}
}

// TestAOS552_LinhagemSoDoArgumentoQueTransportaConteudo: o `media_type` pode levar uma referência
// resolvida — e isso NÃO é uma derivação do conteúdo. A revisão adversarial mediu o contrário na
// primeira versão deste ticket: um evento a declarar `inputs` de um handle que só forneceu o tipo
// de media, com o conteúdo escrito inline, e o modelo a controlar inteiramente os dois passos.
func TestAOS552_LinhagemSoDoArgumentoQueTransportaConteudo(t *testing.T) {
	a, plano, eventos := aos552Espiao(t)
	// (1) Um artefacto cujo CONTEÚDO é, ele mesmo, um media type válido.
	ref := aos552Grava(t, a, plano, "run-1", "step-000001-tool-1",
		`{"name":"t.txt","media_type":"text/plain","content":"application/json"}`)
	// (2) A referência vai no `media_type`; o conteúdo é inline.
	aos552Grava(t, a, plano, "run-1", "step-000002-tool-1",
		`{"name":"b.json","media_type":"`+ref+`","content":"{\"x\":1}"}`)
	vistos := eventos()
	if len(vistos) != 2 {
		t.Fatalf("esperava 2 eventos, %d", len(vistos))
	}
	if vistos[1].MediaType != "application/json" {
		t.Fatalf("a referencia no media_type tinha de resolver: %q", vistos[1].MediaType)
	}
	if len(vistos[1].Inputs) != 0 {
		t.Fatalf("o handle so forneceu o TIPO: a linhagem tinha de ficar vazia, veio %v", vistos[1].Inputs)
	}
}

// TestAOS552_LinhagemSobCarga é o teste de regressão do defeito que a revisão adversarial MEDIU na
// primeira versão deste ticket: a linhagem passava por um mapa partilhado com tecto de 64 entradas,
// consumido só depois da mediação inteira do RM, e com 100 calls em voo perdiam-se 36 linhagens —
// os eventos saíam sem `inputs`, indistinguíveis de uma gravação inline e sem contador que o
// dissesse.
//
// São 80 runs concorrentes, acima do tecto que existia. Com a linhagem a viajar no bilhete da
// própria Call não há mapa, tecto nem janela: cada evento tem de levar a SUA entrada.
func TestAOS552_LinhagemSobCarga(t *testing.T) {
	const runs = 80
	a, plano, eventos := aos552Espiao(t)
	esperado := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run := "run-" + strconv.Itoa(i)
			// Conteúdo distinto por run: o digest da entrada é único, logo uma troca entre runs
			// seria visível.
			base := `{"n":` + strconv.Itoa(i) + `}`
			ref := aos552Grava(t, a, plano, run, "step-000001-tool-1",
				`{"name":"a.json","media_type":"application/json","content":`+strconv.Quote(base)+`}`)
			aos552Grava(t, a, plano, run, "step-000002-tool-1",
				`{"name":"b.json","media_type":"application/json","content":"`+ref+`"}`)
			mu.Lock()
			esperado[run] = "sha256:" + digestHex([]byte(base))
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	vistos := eventos()
	if len(vistos) != 2*runs {
		t.Fatalf("esperava %d eventos, %d", 2*runs, len(vistos))
	}
	derivados := 0
	porEntrada := map[string]int{}
	for _, e := range vistos {
		if e.StepID != "step-000002-tool-1" {
			continue
		}
		derivados++
		if len(e.Inputs) != 1 {
			t.Fatalf("um derivado saiu SEM linhagem: %+v — era o defeito do mapa partilhado", e)
		}
		// Os bytes do derivado são os da entrada, logo o digest é o mesmo: se a linhagem viesse
		// de outro run, não bateria.
		if e.Inputs[0] != e.Digest {
			t.Fatalf("a linhagem nao e a deste run: entrada %q, artefacto %q", e.Inputs[0], e.Digest)
		}
		porEntrada[e.Inputs[0]]++
	}
	if derivados != runs {
		t.Fatalf("derivados = %d, queria %d", derivados, runs)
	}
	for run, digest := range esperado {
		if porEntrada[digest] != 1 {
			t.Fatalf("o run %s: a sua entrada %q aparece %d vezes", run, digest, porEntrada[digest])
		}
	}
}

// TestAOS552_LinhagemNaoEDesclassificacao é o coração do ticket: a herança é PROVENIÊNCIA
// REGISTADA. Nenhum caminho torna um artefacto trusted, com ou sem entradas, e o rótulo vai SELADO
// no evento em vez de ser recalculado por quem o lê.
func TestAOS552_LinhagemNaoEDesclassificacao(t *testing.T) {
	a, plano, eventos := aos552Espiao(t)
	// O valor selado é o do vocabulário do kernel. A comparação cruza os dois vocabulários: o
	// rótulo é uma CONSTANTE de compilação, pelo que isto não é a definição comparada consigo
	// mesma.
	if taintDoArtefacto != taint.Untrusted.String() {
		t.Fatalf("o rotulo selado = %q, queria o do kernel %q", taintDoArtefacto, taint.Untrusted.String())
	}
	if !taint.ParseLabel(taintDoArtefacto).IsUntrusted() || taint.ParseLabel(taintDoArtefacto).IsTrusted() {
		t.Fatal("o rotulo selado tem de classificar untrusted")
	}
	// Uma cadeia de três: inline → derivado do handle → derivado do handle do derivado. Em NENHUM
	// degrau o rótulo muda.
	ref := ""
	for i := 1; i <= 3; i++ {
		passo := "step-00000" + strconv.Itoa(i) + "-tool-1"
		args := `{"name":"a` + strconv.Itoa(i) + `.json","media_type":"application/json","content":"{\"n\":` + strconv.Itoa(i) + `}"}`
		if i > 1 {
			args = `{"name":"a` + strconv.Itoa(i) + `.json","media_type":"application/json","content":"` + ref + `"}`
		}
		ref = aos552Grava(t, a, plano, "run-1", passo, args)
	}
	vistos := eventos()
	if len(vistos) != 3 {
		t.Fatalf("esperava 3 eventos, %d", len(vistos))
	}
	for i, e := range vistos {
		if e.Taint != taint.StringUntrusted {
			t.Fatalf("degrau %d: o rotulo selado = %q", i+1, e.Taint)
		}
		// `ParseLabel` é o parser do RÓTULO. O `LabelFor` é de ORIGENS e devolve untrusted para
		// tudo o que não reconheça — incluindo a string "trusted" —, pelo que usá-lo aqui dava uma
		// asserção inerte exactamente para o valor que se quer excluir.
		if taint.ParseLabel(e.Taint).IsTrusted() {
			t.Fatalf("degrau %d: o rotulo selado classifica trusted", i+1)
		}
	}
	if len(vistos[0].Inputs) != 0 || len(vistos[1].Inputs) != 1 || len(vistos[2].Inputs) != 1 {
		t.Fatalf("linhagem da cadeia: %v / %v / %v", vistos[0].Inputs, vistos[1].Inputs, vistos[2].Inputs)
	}
	// E A RETENÇÃO continua a alcançar o artefacto pelo titular, com os campos novos no payload.
	bruto, _ := json.Marshal(vistos[2])
	if got := subjectOf(eventstore.Event{Type: EventTypeArtifactCreated, Payload: bruto}); got != "titular-550" {
		t.Fatalf("subjectOf com linhagem = %q", got)
	}
}

// TestAOS552_ModeloNaoDeclaraLinhagem: a linhagem vem da reescrita, não do modelo. O pedido do
// modelo tem três campos e `DisallowUnknownFields`, pelo que um `inputs` escrito por ele não passa.
func TestAOS552_ModeloNaoDeclaraLinhagem(t *testing.T) {
	t.Parallel()
	rw := newArtifactRewriter()
	call := referencemonitor.Call{ToolID: artifactWriteToolName, RunID: "run-1", StepID: "s1",
		Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "titular-550"},
		Input:     []byte(`{"name":"a.json","media_type":"application/json","content":"{}","inputs":["sha256:` + strings.Repeat("a", 64) + `"]}`)}
	if _, err := rw(call, &bilheteDaCall{}); err == nil {
		t.Fatal("o modelo conseguiu declarar linhagem nos argumentos")
	}
	// Sem handles resolvidos, a entrada sai sem linhagem.
	call.Input = []byte(`{"name":"a.json","media_type":"application/json","content":"{}"}`)
	got, err := rw(call, &bilheteDaCall{})
	if err != nil {
		t.Fatal(err)
	}
	var in entradaDoArtefacto
	if err := json.Unmarshal(got.Input, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Inputs) != 0 {
		t.Fatalf("sem handles resolvidos nao ha linhagem: %v", in.Inputs)
	}
}

// TestAOS552_HandleDeUmArtefactoEUntrusted: o handle que a gravação cunha é untrusted, e o valor
// que ele devolve quando é RELIDO também — é a segunda metade do guard-test do ADR-043 §8.
//
// O que isto guarda é o `quarantineOrigin` do kernel: a selagem passa `taint.OriginToolResult`
// fixo, e se alguém algum dia passar por ali uma origem que classifique trusted, a quarentena tem
// de a deixar cair.
func TestAOS552_HandleDeUmArtefactoEUntrusted(t *testing.T) {
	ctx := context.Background()
	a, plano := aos550ArmazemComHandles(t)
	out, err := a.executar(ctx, aos550Entrada("run-1", "step-000001-tool-1", "a.json", "application/json", `{"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	token, ok := agentruntime.ParseHandleRef(aos550Ref(t, out))
	if !ok {
		t.Fatal("a tool nao devolveu um handle")
	}
	q, err := plano.quarentenaDoRun(ctx, "titular-550", "run-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	h, achou := q.Lookup(token)
	if !achou {
		t.Fatal("o handle nao esta na quarentena")
	}
	if taint.LabelFor(h.Origin()).IsTrusted() {
		t.Fatalf("a proveniencia do handle (%q) classifica trusted", h.Origin())
	}
	v, err := q.Resolve(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsUntrusted() || v.IsTrusted() {
		t.Fatal("o valor relido por handle tem de ser untrusted")
	}
}

// TestAOS552_No_ArtefactoESempreUntrusted é o guard-test que o ADR-043 §8 exige para a R1b, no nó
// COMPOSTO: o evento selado, o contexto da call, e o `tool_result` DA `artifact_write` que o modelo
// vê no turno seguinte.
//
// Fecha dívida da R1b: a fase foi declarada fechada em produção (v0.1.69) sem este teste.
func TestAOS552_No_ArtefactoESempreUntrusted(t *testing.T) {
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
		cfg.HandleDir, cfg.HandleMaxBytes = t.TempDir(), 1<<30
	})
	var pedidos atomic.Int64
	n.upstream.mu.Lock()
	n.upstream.pede = "x"
	n.upstream.responde = func(_ bool, _ string) []byte {
		if pedidos.Add(1) == 1 {
			return []byte(`{"id":"c","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + artifactWriteToolName + `","arguments":"{\"name\":\"comodos.json\",\"media_type\":\"application/json\",\"content\":\"{\\\"comodos\\\":[\\\"SALA\\\"]}\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
		}
		return []byte(`{"id":"f","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	n.upstream.mu.Unlock()
	runID := "run-aos552-taint"
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Grava a lista dos comodos",
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{artifactWriteToolName}, "max_turns": 3,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if oc, ok, werr := n.svc.Wait(wctx, runID); werr != nil || !ok || oc.Err != nil {
		t.Fatalf("run: ok=%v err=%v oc=%v\n%s", ok, werr, oc.Err, n.log.String())
	}
	eventos, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// (1) O EVENTO SELADO.
	arts := artefactosDoRun(eventos)
	if len(arts) != 1 {
		t.Fatalf("artefactos: %+v\n%s", arts, n.log.String())
	}
	if arts[0].Taint != taint.StringUntrusted || taint.ParseLabel(arts[0].Taint).IsTrusted() {
		t.Fatalf("o evento selou o rotulo %q", arts[0].Taint)
	}
	// (2) O CONTEXTO DA CALL É TRUSTED, e o artefacto é untrusted MESMO ASSIM. Neste run o modelo
	// grava no PRIMEIRO turno, sem ter lido nada: a autoridade da call é trusted (só o objectivo a
	// precedeu). É o caso que separa duas coisas que se confundem com facilidade — a autoridade de
	// QUEM PEDE a escrita, que depende do que o modelo viu, e o taint DO QUE SE ESCREVEU, que é
	// untrusted porque quem o escreveu foi o modelo. O eixo da entrada com contexto untrusted é o
	// do `aos549_escrita_local_no_test.go`; este é o complemento dele.
	var contextoDaCall string
	for _, e := range eventos {
		if e.Type != referencemonitor.EventTypeMediated {
			continue
		}
		var p struct {
			ToolID  string `json:"tool_id"`
			Context struct {
				Taint string `json:"taint"`
			} `json:"context"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.ToolID == artifactWriteToolName {
			contextoDaCall = p.Context.Taint
		}
	}
	if contextoDaCall == "" {
		t.Fatal("a artifact_write nao foi mediada")
	}
	if contextoDaCall != taint.StringTrusted {
		t.Fatalf("neste run a call e pedida no 1.º turno, logo o contexto e trusted; veio %q — se o cenario mudou, o teste perde o seu ponto", contextoDaCall)
	}
	if arts[0].Taint == taint.StringTrusted {
		t.Fatal("um contexto trusted NAO pode produzir um artefacto trusted")
	}
	// (3) O RESULTADO QUE O MODELO VÊ. Procura-se o segmento `tool_result` DA `artifact_write`
	// pelo nome, e não o marcador em qualquer sítio do corpo: o tail marca TODOS os resultados
	// como untrusted por construção, pelo que uma busca solta passaria por qualquer tool e não
	// provaria nada sobre artefactos.
	n.upstream.mu.Lock()
	var ultimo string
	if k := len(n.upstream.corpos); k > 0 {
		ultimo = string(n.upstream.corpos[k-1])
	}
	n.upstream.mu.Unlock()
	// O cabeçalho INTEIRO do segmento, por concatenação: o rótulo, o id da tool call e o NOME da
	// tool na mesma linha. É isto que o liga à `artifact_write` — procurar só o marcador
	// `taint=untrusted` em qualquer sítio do corpo passaria por qualquer tool, porque o tail marca
	// TODOS os resultados como untrusted por construção, e não provaria nada sobre artefactos.
	cabecalho := "tool_result taint=" + taint.StringUntrusted + " id=step-000001-tool-1 name=" + artifactWriteToolName
	if !strings.Contains(ultimo, cabecalho) {
		t.Fatalf("o prompt seguinte nao tem o tool_result untrusted da %s (%q): %s", artifactWriteToolName, cabecalho, ultimo)
	}
	if strings.Contains(ultimo, `tool_result taint=`+taint.StringTrusted) {
		t.Fatal("um tool_result saiu marcado trusted")
	}
}
