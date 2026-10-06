package main

// AOS-501 — O QUE A REVISÃO ADVERSARIAL DE 2026-10-06 MOSTROU QUE NÃO ESTAVA PRESO.
//
// Dois grupos de testes:
//
//   - O NÓ MISTO NÃO EXISTE (achado I4). O validador do plano recusa-o; aqui prende-se a defesa do
//     executor, que falha fechado: um nó com uma saída com origem e outra de texto não se submete,
//     não se entrega, e NADA dele se publica do texto final;
//   - O CORPUS ADVERSARIAL PELO CAMINHO POR REFERÊNCIA (ADR-038 §5, condição para ligar `on`):
//     cada documento do corpus de injecção, dentro de um envelope real da sandbox, passa pelo
//     binário real em `on` e chega aos `inputs` do nó consumidor byte a byte, publicado como
//     `untrusted`. É o terceiro elo de uma cadeia de ficheiros de fio — ver
//     `packages/security-tests/aos501_corpus_por_referencia_test.go`.
//
// Os outros achados estão presos ao lado do que corrigem, em aos501_entrega_por_referencia_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// aos501RecorderComStore é o [planRecorderDeTeste] que devolve também o store: estes testes lêem
// o que o executor escreveu no log do plano.
func aos501RecorderComStore(t *testing.T, runID, planID string) (*eventstore.Store, *runlifecycle.PlanRecorder) {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	leases, err := durable.NewLeaseManager(store, leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	ten, err := runlifecycle.Claim(context.Background(), store, leases, runID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := runlifecycle.NewPlanRecorder(ten, planID, eventstore.Producer{NHIID: "nhi:teste"})
	if err != nil {
		t.Fatal(err)
	}
	return store, rec
}

// aos501DoLog devolve os eventos do tipo dado que estão no stream do plano.
func aos501DoLog(t *testing.T, store *eventstore.Store, planID, tipo string) []eventstore.Event {
	t.Helper()
	eventos, err := store.Read(context.Background(), planID, 0)
	if err != nil && !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("ler o stream do plano: %v", err)
	}
	var out []eventstore.Event
	for _, e := range eventos {
		if e.Type == tipo {
			out = append(out, e)
		}
	}
	return out
}

// aos501Misto é o NÓ MISTO: o leitor com a saída `conteudo` por referência e, ao lado, uma saída
// de texto. `tipo` é o da saída de texto.
func aos501Misto(tipo plan.PayloadType) plan.Node {
	n := aos501NoLeitor("doc_read")
	n.Outputs = append(n.Outputs, plan.Output{Name: "resumo", Type: tipo})
	return n
}

// TestAOS501_NoMisto_NaoSeSubmete: a defesa do executor na SUBMISSÃO. Um nó misto que chegasse ao
// executor — não chega, o validador recusa-o — não é submetido: nenhum `POST /runs`, nenhum facto
// no log, e o erro é o determinista (saída 10), não um que se repita em todas as gerações.
func TestAOS501_NoMisto_NaoSeSubmete(t *testing.T) {
	for _, tipo := range []plan.PayloadType{plan.PayloadSummary, plan.PayloadRecord, plan.PayloadArtifact} {
		store, rec := aos501RecorderComStore(t, "run-418", "plan-418")
		cli := &runnerQueGuarda{}
		misto := aos501Misto(tipo)
		e := &executorDeNos{cli: cli, rec: rec, runID: "run-418", nos: map[string]plan.Node{"ler": misto},
			tools: map[string][]string{"ler": {"cap:tool:doc_read"}}, entregaActiva: true,
			emVoo: map[string]struct{}{}, declaradas: map[string]declaracaoDeOrigem{}, candidatos: map[string]bool{}}
		err := e.submeter(context.Background(), "ler")
		if !errors.Is(err, errNoSemEntregaPorReferencia) {
			t.Fatalf("saida de texto %s: um no misto nao se submete, com o erro determinista; veio %v", tipo, err)
		}
		if tipoDoErro(err) != "no_sem_saida_por_referencia" || codigoDe(err) != exitDocumentoRecusado {
			t.Fatalf("saida de texto %s: a recusa e terminal (10) e tem causa propria; veio %s/%d", tipo, tipoDoErro(err), codigoDe(err))
		}
		if cli.ultimo.RunID != "" || len(e.emVoo) != 0 || len(e.declaradas) != 0 {
			t.Fatalf("saida de texto %s: o no misto foi pedido ao no aos (run=%q, em voo=%d, factos=%d)", tipo, cli.ultimo.RunID, len(e.emVoo), len(e.declaradas))
		}
		if n := len(aos501DoLog(t, store, "plan-418", plannerevents.EventOutputSourceDeclared)); n != 0 {
			t.Fatalf("saida de texto %s: ficou um facto de declaracao no log para um no que nao foi submetido (%d)", tipo, n)
		}
	}
	// NÃO-VACUIDADE: o mesmo nó SEM a saída de texto submete-se, com o vínculo vinculativo e o
	// facto no log; e com uma saída de forma fechada ao lado também.
	for nome, n := range map[string]plan.Node{"so a origem": aos501NoLeitor("doc_read"), "origem e metrics": aos501Misto(plan.PayloadMetrics)} {
		store, rec := aos501RecorderComStore(t, "run-418", "plan-418")
		cli := &runnerQueGuarda{}
		e := &executorDeNos{cli: cli, rec: rec, runID: "run-418", nos: map[string]plan.Node{"ler": n},
			tools: map[string][]string{"ler": {"cap:tool:doc_read"}}, entregaActiva: true,
			emVoo: map[string]struct{}{}, declaradas: map[string]declaracaoDeOrigem{}, candidatos: map[string]bool{}}
		if err := e.submeter(context.Background(), "ler"); err != nil {
			t.Fatalf("%s: o no tinha de se submeter: %v", nome, err)
		}
		if cli.ultimo.RunID != aos501RunDoLeitor || cli.ultimo.OutputFromTool != "doc_read" || cli.ultimo.OutputBinding != agentruntime.OutputSourceBinds {
			t.Fatalf("%s: o pedido leva a origem e o vinculo binding: %+v", nome, cli.ultimo)
		}
		if d := e.declaradas["ler"]; !d.vinculativa("doc_read", plan.OutputDigest(n, n.Outputs[0])) {
			t.Fatalf("%s: o facto em memoria e o vinculativo, com o digest do contrato: %+v", nome, d)
		}
		if n := len(aos501DoLog(t, store, "plan-418", plannerevents.EventOutputSourceDeclared)); n != 1 {
			t.Fatalf("%s: o facto da declaracao fica no log (veio %d)", nome, n)
		}
	}
}

// TestAOS501_NoMisto_NadaSePublicaDoTextoFinal: a defesa do executor na PUBLICAÇÃO. De um nó que
// declara a origem, só a saída com origem se publica — e só da entrega. A saída de texto de um nó
// misto NUNCA se publica: nem com a entrega resolvida, nem sem ela. O consumidor que a consumisse
// ficava com o contrato por cumprir, em vez de receber o resumo do modelo vindo de um nó que
// declarou a origem.
func TestAOS501_NoMisto_NadaSePublicaDoTextoFinal(t *testing.T) {
	envelope, documento := aos499Envelope(t, "notas")
	st := estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true, FinalText: aos501TextoFinal}
	entrega := &entregaPorReferencia{conteudo: documento, extraccao: plannerevents.PayloadExtractionSandboxStdoutText,
		passo: "step-000001-tool-1", digestDaAncora: digestDoConteudo(envelope), bytesDaAncora: len(envelope)}
	doTexto := chaveDePayload{no: "ler", output: "resumo"}
	porRef := chaveDePayload{no: "ler", output: "conteudo"}
	for _, tipo := range []plan.PayloadType{plan.PayloadSummary, plan.PayloadRecord, plan.PayloadArtifact} {
		for nome, ent := range map[string]*entregaPorReferencia{"com a entrega resolvida": entrega, "sem entrega": nil} {
			store, rec := aos501RecorderComStore(t, "run-418", "plan-418")
			e := &executorDeNos{rec: rec, runID: "run-418", payloads: map[chaveDePayload]string{}}
			if err := e.publicarSaidas(context.Background(), aos501Misto(tipo), st, nil, ent); err != nil {
				t.Fatalf("%s/%s: publicarSaidas: %v", tipo, nome, err)
			}
			if texto, publicou := e.payloads[doTexto]; publicou {
				t.Fatalf("%s/%s: a saida de TEXTO de um no que declara a origem publicou-se (%q)", tipo, nome, texto)
			}
			for _, conteudo := range e.payloads {
				if strings.Contains(conteudo, aos501TextoFinal) {
					t.Fatalf("%s/%s: o texto final do produtor ficou num payload", tipo, nome)
				}
			}
			publicados := aos501DoLog(t, store, "plan-418", plannerevents.EventPayloadPublished)
			quantos := 0
			if ent != nil {
				quantos = 1
			}
			if len(publicados) != quantos || len(e.payloads) != quantos {
				t.Fatalf("%s/%s: publicaram-se %d eventos e ficaram %d payloads; quero %d", tipo, nome, len(publicados), len(e.payloads), quantos)
			}
			if ent != nil {
				var p plannerevents.PayloadPublishedPayload
				if err := json.Unmarshal(publicados[0].Payload, &p); err != nil || p.Output != "conteudo" || p.Source == nil || p.Taint != plan.TaintUntrusted {
					t.Fatalf("%s/%s: o unico evento e o da saida por referencia, untrusted: %+v (%v)", tipo, nome, p, err)
				}
				if e.payloads[porRef] != documento {
					t.Fatalf("%s/%s: o payload por referencia e o documento", tipo, nome)
				}
			}
		}
	}
	// A ORIGEM NUMA SAÍDA DE FORMA FECHADA, ao lado de UMA saída de texto. O validador recusa-o
	// (a origem só cabe em `record` ou `artifact`), e a contagem das saídas abertas não o vê: é
	// uma só. Sem o ramo próprio do executor, a saída de texto publicava-se do texto final de um nó
	// que declara a origem (a mutação que sobrevivia sem este caso). Nada se publica, com ou sem
	// entrega.
	for _, fechada := range []plan.PayloadType{plan.PayloadMetrics, plan.PayloadVerdict} {
		for nome, ent := range map[string]*entregaPorReferencia{"com a entrega resolvida": entrega, "sem entrega": nil} {
			store, rec := aos501RecorderComStore(t, "run-418", "plan-418")
			e := &executorDeNos{rec: rec, runID: "run-418", payloads: map[chaveDePayload]string{}}
			no := plan.Node{NodeID: "ler", Role: "reader", Objective: "ler o documento",
				Tools: []plan.ToolRef{{Name: "doc_read", Version: "1", Digest: "sha256:a"}},
				Outputs: []plan.Output{
					{Name: "conteudo", Type: fechada, FromTool: "doc_read"},
					{Name: "resumo", Type: plan.PayloadSummary},
				}}
			if !no.DeclaresOutputSource() {
				t.Fatalf("pre-condicao: o no declara a origem numa saida %s", fechada)
			}
			if err := e.publicarSaidas(context.Background(), no, st, nil, ent); err != nil {
				t.Fatalf("origem em %s/%s: publicarSaidas: %v", fechada, nome, err)
			}
			if len(e.payloads) != 0 || len(aos501DoLog(t, store, "plan-418", plannerevents.EventPayloadPublished)) != 0 {
				t.Fatalf("origem em %s/%s: de um no que declara a origem publicou-se do texto final: %v", fechada, nome, e.payloads)
			}
			if _, ok := saidaComOrigem(no); ok {
				t.Fatalf("origem em %s: um no com uma saida de texto ao lado da origem nao e entregavel", fechada)
			}
		}
	}

	// NÃO-VACUIDADE: um nó SEM origem, com a mesma saída de texto, publica o texto final — a
	// defesa é do nó que declara a origem, e não um «nunca se publica texto».
	store, rec := aos501RecorderComStore(t, "run-418", "plan-418")
	e := &executorDeNos{rec: rec, runID: "run-418", payloads: map[chaveDePayload]string{}}
	semOrigem := plan.Node{NodeID: "ler", Role: "reader", Objective: "ler", Outputs: []plan.Output{{Name: "resumo", Type: plan.PayloadSummary}}}
	if err := e.publicarSaidas(context.Background(), semOrigem, st, nil, nil); err != nil {
		t.Fatalf("publicarSaidas sem origem: %v", err)
	}
	if e.payloads[doTexto] != aos501TextoFinal || len(aos501DoLog(t, store, "plan-418", plannerevents.EventPayloadPublished)) != 1 {
		t.Fatalf("um no sem origem publica a sua saida de texto, como sempre: %v", e.payloads)
	}
	// E o nó misto não é ENTREGÁVEL: é a mesma pergunta que a submissão e o fecho fazem.
	for _, tipo := range []plan.PayloadType{plan.PayloadSummary, plan.PayloadRecord, plan.PayloadArtifact} {
		if _, ok := saidaComOrigem(aos501Misto(tipo)); ok {
			t.Errorf("um no com a origem e uma saida %s ao lado nao e entregavel por referencia", tipo)
		}
	}
	if _, ok := saidaComOrigem(aos501Misto(plan.PayloadMetrics)); !ok {
		t.Error("a origem com uma saida de forma fechada ao lado e entregavel: metrics nunca se publica do texto")
	}
}

// --- O CORPUS ADVERSARIAL PELO CAMINHO POR REFERÊNCIA ------------------------------------------

// aos501PedidoDoCorpus é o corpo do `POST /runs` do nó consumidor para UM documento do corpus,
// tal como este binário o enviou. `Post` é o corpo CRU, numa string: o nó consome-o byte a byte.
type aos501PedidoDoCorpus struct {
	ID   string `json:"id"`
	Post string `json:"post"`
}

// aos501FioDosPedidos é o ficheiro de fio que o teste do nó consome.
type aos501FioDosPedidos struct {
	CorpusVersion string                 `json:"corpus_version"`
	Pedidos       []aos501PedidoDoCorpus `json:"pedidos"`
}

// aos501LerFioDeFora lê um ficheiro de fio gerado noutro módulo, ou falha com a instrução.
func aos501LerFioDeFora(t *testing.T, caminho, gerador string, destino any) {
	t.Helper()
	cru, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ficheiro de fio em falta (%v) — gera-o em %s com AOS494_ACTUALIZAR_FIO=1", err, gerador)
	}
	if err := json.Unmarshal(cru, destino); err != nil {
		t.Fatalf("ficheiro de fio %s ilegivel: %v", caminho, err)
	}
}

// TestAOS501_CorpusAdversarialPeloCaminhoPorReferencia é a condição do ADR-038 §5 para ligar a
// entrega: o corpus de injecção da suite de segurança corre pelo caminho por referência.
//
// Por cada documento do corpus (`security-tests/testdata/aos501_corpus_por_referencia/`), o
// ENVELOPE que o codificador real da sandbox escreveu para ele
// (`substrate/sandbox/testdata/aos501_corpus_envelope/`) é o resultado designado de um run do nó
// produtor, e o `aos-orq consume` REAL, em `on`, recolhe-o:
//
//   - a extracção entrega o documento BYTE A BYTE — o adversário não ganha nem perde um byte entre
//     a tool e o `plan_input`, e não é o envelope nem o texto do modelo que chega;
//   - o evento de publicação diz `taint: untrusted` e a forma da extracção, e não leva conteúdo;
//   - o documento não aparece no log, nas métricas, no `detail` nem em nenhum evento.
//
// O corpo do `POST /runs` do consumidor fica em `testdata/aos501_corpus/`, e o teste do nó
// (`cmd/aos`, `TestAOS501_Fio_CorpusAdversarialNoConsumidor`) entrega-o ao nó REAL: é lá que se
// verifica o `plan_input` `taint=untrusted`, a neutralização e a autoridade do run.
func TestAOS501_CorpusAdversarialPeloCaminhoPorReferencia(t *testing.T) {
	var documentos struct {
		CorpusVersion string `json:"corpus_version"`
		Documentos    []struct {
			ID, Entrada, Variante, Texto string
		} `json:"documentos"`
	}
	aos501LerFioDeFora(t, filepath.Join("..", "..", "security-tests", "testdata", "aos501_corpus_por_referencia", "documentos.json"), "packages/security-tests", &documentos)
	var envelopes struct {
		CorpusVersion string `json:"corpus_version"`
		Envelopes     []struct{ ID, Envelope string }
	}
	aos501LerFioDeFora(t, filepath.Join("..", "..", "substrate", "sandbox", "testdata", "aos501_corpus_envelope", "envelopes.json"), "packages/substrate/sandbox", &envelopes)
	if len(documentos.Documentos) == 0 || len(documentos.Documentos) != len(envelopes.Envelopes) || documentos.CorpusVersion != envelopes.CorpusVersion {
		t.Fatalf("os dois fios tem de ser do mesmo corpus e do mesmo tamanho: %d documentos (%s), %d envelopes (%s) — regenera a cadeia", len(documentos.Documentos), documentos.CorpusVersion, len(envelopes.Envelopes), envelopes.CorpusVersion)
	}

	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	comOrigem := aos501Plano(t, p)
	fio := aos501FioDosPedidos{CorpusVersion: documentos.CorpusVersion}
	entradas := map[string]bool{}
	for i, doc := range documentos.Documentos {
		env := envelopes.Envelopes[i]
		if env.ID != doc.ID {
			t.Fatalf("o envelope %d e de %q e o documento e %q: os fios nao estao pela mesma ordem", i, env.ID, doc.ID)
		}
		entradas[doc.Entrada] = true

		// (1) A EXTRACÇÃO, em unidade: do envelope real sai o documento, byte a byte.
		ancora := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
			StepID: "step-000001-tool-1", Digest: digestDoConteudo(env.Envelope), Bytes: len(env.Envelope)}
		ent, causa := entregaDoRun("doc_read", aos501Run+"~read_notes", estadoDoRun{RunID: aos501Run + "~read_notes", Status: "completed", Terminated: true,
			FinalText: aos501TextoFinal, OutputSource: ancora, Output: &env.Envelope})
		if causa != "" || ent.conteudo != doc.Texto || ent.extraccao != plannerevents.PayloadExtractionSandboxStdoutText {
			t.Fatalf("%s: da extraccao tinha de sair o documento do corpus byte a byte (causa=%q, extraccao=%q, %d bytes; quero %d)", doc.ID, causa, ent.extraccao, len(ent.conteudo), len(doc.Texto))
		}

		// (2) O BINÁRIO REAL, em `on`: o run do produtor responde com a âncora e o envelope.
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{
			"read_notes": aos501Concluido(aos501Ancora("binding", "designated", env.Envelope), aos501Servido(t, env.Envelope))}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("%s: o plano tinha de sair terminal/0; saiu %s/%d %q\n%s", doc.ID, d.classe, d.codigo, d.detalhe, d.stdout)
		}
		entrada := aos501Entrada(t, f)
		if entrada["content"] != doc.Texto || entrada["digest"] != digestDoConteudo(doc.Texto) || entrada["from"] != "read_notes" || entrada["output"] != "conteudo" {
			t.Fatalf("%s: o consumidor tinha de receber o documento do corpus byte a byte, com o seu digest e a proveniencia do contrato:\n  quero: %q\n  veio:  %v", doc.ID, doc.Texto, entrada)
		}
		if entrada["content"] == env.Envelope || strings.Contains(entrada["content"].(string), aos501TextoFinal) {
			t.Fatalf("%s: chegou ao consumidor o envelope, ou o texto do modelo", doc.ID)
		}
		// Nenhum campo do pedido diz ao nó que o conteúdo é de confiança: o nó não aceita taint do
		// chamador, e o corpo do consumidor tem a forma de sempre.
		cru := f.crus[aos501Run+"~summarize"]
		for _, proibido := range []string{`"taint"`, `"trusted"`, `"output_from_tool"`, `"output_source_binding"`} {
			if bytes.Contains(cru, []byte(proibido)) && !strings.Contains(doc.Texto, strings.Trim(proibido, `"`)) {
				t.Fatalf("%s: o POST /runs do consumidor leva %s:\n%s", doc.ID, proibido, cru)
			}
		}

		// (3) O EVENTO: untrusted, por referência, sem conteúdo.
		eventos := aos499EventosDoPlano(t, d.wal, aos501Run)
		publicados := aos501Eventos(eventos, plannerevents.EventPayloadPublished)
		var pub plannerevents.PayloadPublishedPayload
		if len(publicados) != 1 || json.Unmarshal([]byte(publicados[0].Payload), &pub) != nil {
			t.Fatalf("%s: queria um plan.payload_published legivel: %+v", doc.ID, publicados)
		}
		if pub.Taint != plan.TaintUntrusted {
			t.Fatalf("%s: o payload por referencia foi publicado SEM a marca untrusted (taint=%q)", doc.ID, pub.Taint)
		}
		if pub.Source == nil || pub.Source.Tool != "doc_read" || pub.Source.Extraction != plannerevents.PayloadExtractionSandboxStdoutText ||
			pub.Record.Digest != digestDoConteudo(doc.Texto) || pub.Source.AnchorDigest != digestDoConteudo(env.Envelope) {
			t.Fatalf("%s: o evento regista a origem, a extraccao, o digest do entregue e o da ancora: %+v", doc.ID, pub)
		}

		// (4) SEM CONTEÚDO fora dos `inputs`: o documento adversarial não vai ao log, às métricas,
		// ao `detail` nem a evento nenhum; e o texto do modelo não vai a lado nenhum.
		aos501NuncaAparece(t, "o texto final do produtor", aos501TextoFinal, d, f, eventos)
		for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": d.metricas, "detalhe": d.detalhe} {
			if strings.Contains(texto, doc.Texto) {
				t.Fatalf("%s: o documento adversarial aparece em %s", doc.ID, onde)
			}
		}
		for _, e := range eventos {
			if strings.Contains(e.Payload, doc.Texto) {
				t.Fatalf("%s: o documento adversarial aparece no evento %s do plano", doc.ID, e.Tipo)
			}
		}
		fio.Pedidos = append(fio.Pedidos, aos501PedidoDoCorpus{ID: doc.ID, Post: string(bytes.TrimSpace(cru))})
	}

	agora, err := json.MarshalIndent(fio, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	agora = append(agora, '\n')
	caminho := filepath.Join("testdata", "aos501_corpus", "post-runs-summarize.json")
	if os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatalf("criar a pasta do fio: %v", err)
		}
		if err := os.WriteFile(caminho, agora, 0o644); err != nil {
			t.Fatalf("escrever %s: %v", caminho, err)
		}
	}
	quer, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o com AOS494_ACTUALIZAR_FIO=1", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(quer, []byte("\r\n"), []byte("\n")), agora) {
		t.Fatalf("o POST /runs do consumidor deixou de levar o que o no recebe nos testes dele (%s): regenera a cadeia com AOS494_ACTUALIZAR_FIO=1", caminho)
	}
	t.Logf("corpus %s: %d entradas de injeccao, %d documentos, pelo binario real em on", fio.CorpusVersion, len(entradas), len(fio.Pedidos))
}
