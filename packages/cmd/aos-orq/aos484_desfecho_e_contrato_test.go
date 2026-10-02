package main

// AOS-484 — «TODOS OS NÓS CONCLUÍRAM» NÃO É «O OBJECTIVO FOI CUMPRIDO».
//
// Medido em produção a 2026-10-02 (`plan-e2e-pegadas-1790956072`): o planeador declarou
// `depends_on` sem `outputs`/`consumes`, o nó de resumo correu sem o documento que o anterior leu,
// e o plano saiu `terminal` com código 0. O dono decidiu duas coisas, e este ficheiro prova as duas
// pelo processo real do `aos-orq` contra um nó `aos` falso:
//
//   - (B) com o contrato DECLARADO — o que a regra 12 do prompt 1.4.0 passa a pedir ao modelo —, o
//     conteúdo do produtor chega ao consumidor pelo canal `inputs`, e o log tem o
//     `plan.payload_published`. O controlo ao lado mostra o defeito medido: sem `consumes`, o mesmo
//     plano é admitido e o consumidor é submetido sem nada.
//   - (C) um plano que chega ao fim com um nó `failed` deixa de sair com 0: sai com 13, larga a
//     posse, e repetir o `serve` não re-executa nada.
//
// O que (C) NÃO apanha, e fica declarado no ticket: um nó que termina `complete` a dizer que não
// conseguiu. O controlo de (B) sai com 0 por isso mesmo.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// aos484PlanoLerEResumir é o plano do objectivo medido em produção, com o contrato de dados
// declarado: `n1` lê com uma tool e publica um `record`; `n2`, sem tools, consome-o. Sem risco:
// auto-aprova.
const aos484PlanoLerEResumir = `{
  "plan_version": "1.2.0",
  "objective": "ler o documento notes e resumi-lo em tres pontos",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.4.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"n1","role":"reader","objective":"ler o documento notes","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "outputs":[{"name":"conteudo","type":"record","taint":"untrusted"}]},
    {"node_id":"n2","role":"summarizer","objective":"resumir em tres pontos o que foi lido","depends_on":["n1"],
     "tools":[],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "consumes":[{"from":"n1","output":"conteudo","type":"record"}]}
  ]
}`

// aos484ConsumesDoN2 é a linha que o controlo retira: o plano fica com a dependência só de ordem.
const aos484ConsumesDoN2 = `,
     "consumes":[{"from":"n1","output":"conteudo","type":"record"}]`

// aos484PlanoDoisAbertos: o `n1` declara DOIS outputs de forma aberta. O validador admite-o; o
// executor não transporta nenhum (um run devolve UMA saída final) — é a frase da regra 12.
const aos484PlanoDoisAbertos = `{
  "plan_version": "1.2.0",
  "objective": "ler e resumir, com dois outputs abertos no produtor",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.4.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"n1","role":"reader","objective":"ler o documento notes","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "outputs":[{"name":"conteudo","type":"record","taint":"untrusted"},
                {"name":"resumo","type":"summary","taint":"untrusted"}]},
    {"node_id":"n2","role":"summarizer","objective":"resumir o que foi lido","depends_on":["n1"],
     "tools":[],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "consumes":[{"from":"n1","output":"conteudo","type":"record"}]}
  ]
}`

// aos484Ambiente é o `serve --goal` de um plano sem risco contra o nó falso, com o documento
// validado guardado em `doc` (é por ele que a retoma corre, como no `consume`).
type aos484Ambiente struct {
	f                       *aos413No
	env                     []string
	bin, wal, snapPath, doc string
	run                     string
}

func aos484Arrancar(t *testing.T, f *aos413No, run, plano string) (aos484Ambiente, resultado) {
	t.Helper()
	srv := f.servidor(t)
	bin := construir(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	a := aos484Ambiente{
		f:        f,
		env:      []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="},
		bin:      bin,
		wal:      filepath.Join(dir, "es.wal"),
		snapPath: filepath.Join(dir, "snap.json"),
		doc:      filepath.Join(dir, "validado.json"),
		run:      run,
	}
	escrever(t, a.snapPath, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, plano)
	r := correrComEnv(t, a.env, bin, "serve", "--wal", a.wal, "--run", run, "--goal", "ler-e-resumir",
		"--snapshot", a.snapPath, "--decompose-fixture", fix, "--plan-out", a.doc, "--worker", "p1", "--poll-interval", "20ms", "--release")
	return a, r
}

// retomar corre o `serve --plan-doc` sobre o mesmo run — a via por onde o `consume` retoma (AOS-442).
func (a aos484Ambiente) retomar(t *testing.T) resultado {
	t.Helper()
	return aos413Serve(t, a.env, a.bin, a.wal, a.run, a.snapPath, a.doc, "--release")
}

// publicados lê do WAL os `plan.payload_published` do plano do run.
func (a aos484Ambiente) publicados(t *testing.T) []plannerevents.PayloadPublishedPayload {
	t.Helper()
	store, fechar, err := (substrato{wal: a.wal}).abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrir o WAL para leitura: %v", err)
	}
	defer func() { _ = fechar() }()
	eventos, err := store.Read(context.Background(), a.run+"-plan", 0)
	if err != nil {
		t.Fatalf("ler o stream do plano: %v", err)
	}
	var out []plannerevents.PayloadPublishedPayload
	for _, ev := range eventos {
		if ev.Type != plannerevents.EventPayloadPublished {
			continue
		}
		var p plannerevents.PayloadPublishedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("%s ilegivel no log: %v", plannerevents.EventPayloadPublished, err)
		}
		out = append(out, p)
	}
	return out
}

// TestAOS484_ContratoDeclaradoEntregaAoConsumidorOQueOProdutorLeu — critério (B), pelo processo
// real: o plano de dois nós com o contrato declarado entrega ao segundo o conteúdo do primeiro.
//
// SENSIBILIDADE, medida por duas mutações diferentes:
//   - no EXECUTOR (`entradasDe` a não entregar nada): falha em «o n2 tinha de receber 1 payload,
//     recebeu 0»;
//   - no FIXTURE (retirar a linha [aos484ConsumesDoN2] da constante): falha logo na pré-condição
//     abaixo, «a linha do consumes mudou» — o teste não chega a correr o plano. O que acontece a
//     esse plano sem `consumes` está fixado no sub-teste de controlo, ao lado.
func TestAOS484_ContratoDeclaradoEntregaAoConsumidorOQueOProdutorLeu(t *testing.T) {
	if strings.Count(aos484PlanoLerEResumir, aos484ConsumesDoN2) != 1 {
		t.Fatal("pre-condicao: a linha do consumes mudou; actualizar o mutante do controlo")
	}

	t.Run("com o contrato, o n2 recebe o que o n1 leu", func(t *testing.T) {
		const run = "run-aos484-contrato"
		f := &aos413No{saidaVerif: "resumo em tres pontos"}
		a, r := aos484Arrancar(t, f, run, aos484PlanoLerEResumir)
		if r.code != exitOK {
			t.Fatalf("o plano todo `complete` tinha de sair 0, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
		}
		if !strings.Contains(r.stdout, "execucao: n1=complete n2=complete") {
			t.Fatalf("os dois nós tinham de concluir:\n%s", r.stdout)
		}
		conteudoDoN1 := "feito: " + run + "~n1" // o `final_text` que o nó falso devolve pelo run do n1

		// (1) O log tem o `plan.payload_published` do n1, com a referência ao run filho e o digest
		// do conteúdo.
		pubs := a.publicados(t)
		if len(pubs) != 1 {
			t.Fatalf("o log tinha de ter 1 %s, tem %d: %+v", plannerevents.EventPayloadPublished, len(pubs), pubs)
		}
		if p := pubs[0]; p.NodeID != "n1" || p.Output != "conteudo" || p.Record.Stream != run+"~n1" ||
			p.Record.Digest != digestDoConteudo(conteudoDoN1) {
			t.Fatalf("o payload publicado não é o do n1: %+v", p)
		}

		// (2) O corpo do POST /runs do n2 leva `inputs` com o conteúdo do n1 e o digest.
		xs := f.inputs(run + "~n2")
		if len(xs) != 1 {
			t.Fatalf("o n2 tinha de receber 1 payload, recebeu %d: %v\n%s", len(xs), xs, r.stdout)
		}
		in, _ := xs[0].(map[string]any)
		if in["from"] != "n1" || in["output"] != "conteudo" {
			t.Fatalf("o payload veio com outro contrato: %v", in)
		}
		if in["content"] != conteudoDoN1 {
			t.Fatalf("o conteúdo que o n2 recebeu não é a saída do n1: %v", in["content"])
		}
		if in["digest"] != digestDoConteudo(conteudoDoN1) {
			t.Fatalf("o digest não é o do conteúdo entregue: %v", in["digest"])
		}
		// (3) O n2 não tem tools: leva a lista-branca vazia, não nil.
		if ts := f.tools(run + "~n2"); ts == nil || len(ts) != 0 {
			t.Fatalf("o n2 sem tools tinha de levar [] (nenhuma), levou %v", ts)
		}
		// (4) O n1 não consome nada.
		if xs := f.inputs(run + "~n1"); len(xs) != 0 {
			t.Fatalf("o n1 não consome nada e recebeu %v", xs)
		}
	})

	// CONTROLO — o defeito medido, e o resíduo que o ticket declara. O mesmo plano sem `consumes`
	// é ADMITIDO (a opção (A), recusá-lo na validação, foi recusada pelo dono), o n2 corre SEM o
	// que o n1 leu, e o plano sai com 0: nenhum nó falhou. É (B) — a regra 12 — que o trata, e é
	// uma instrução ao modelo, não uma garantia.
	t.Run("controlo: sem consumes o n2 corre sem nada, e o plano sai 0", func(t *testing.T) {
		const run = "run-aos484-so-ordem"
		f := &aos413No{saidaVerif: "nao foi possivel ler o documento"}
		a, r := aos484Arrancar(t, f, run, strings.Replace(aos484PlanoLerEResumir, aos484ConsumesDoN2, "", 1))
		if r.code != exitOK {
			t.Fatalf("a dependência só de ordem continua a ser um plano válido; saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
		}
		if xs := f.inputs(run + "~n2"); len(xs) != 0 {
			t.Fatalf("sem consumes o n2 não podia receber nada, e recebeu %v", xs)
		}
		if got := strings.Join(f.submetidos(), ","); got != run+"~n1,"+run+"~n2" {
			t.Fatalf("os dois nós tinham de correr, correram %q", got)
		}
		// O n1 declara o output e publica-o; sem consumidor que o declare, não vai a lado nenhum.
		if pubs := a.publicados(t); len(pubs) != 1 || pubs[0].NodeID != "n1" {
			t.Fatalf("o n1 continua a publicar o seu output: %+v", pubs)
		}
	})
}

// TestAOS484_PlanoComNoFalhadoSai13LargaAPosseENaoReexecuta — critério (C), pelo processo real.
// FALHA-ANTES: o `serve` saía com 0 com `n1=complete n2=failed` no resumo.
func TestAOS484_PlanoComNoFalhadoSai13LargaAPosseENaoReexecuta(t *testing.T) {
	const run = "run-aos484-falhado"
	f := &aos413No{}
	a, r := aos484Arrancar(t, f, run, aos414PlanoComMetrics)
	if r.code != exitNosFalhados {
		t.Fatalf("um plano com um nó failed tinha de sair %d, saiu %d\n%s\n%s", exitNosFalhados, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "execucao: n1=complete n2=failed") {
		t.Fatalf("o resumo tinha de mostrar o nó falhado:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "o plano terminou com nos falhados") || !strings.Contains(r.stderr, "(n2)") {
		t.Fatalf("o stderr tinha de nomear o sentinela e o nó falhado:\n%s", r.stderr)
	}
	// O desfecho que o `consume` reportaria: terminal, com o detalhe de nome estável.
	if classe := classeDoDesfecho(r.code); classe != "terminal" {
		t.Fatalf("o código %d tem de ser terminal (repetir não resolve), é %q", r.code, classe)
	}

	// A RETOMA. (1) A posse foi largada: a invocação seguinte, imediata, toma-a — não sai com 3
	// («lease detido») à espera do TTL. (2) Não re-executa: nada é despachado nem submetido de
	// novo. (3) O desfecho é o mesmo — o estado dos nós é durável.
	antes := strings.Join(f.submetidos(), ",")
	for i := 0; i < 2; i++ {
		r2 := a.retomar(t)
		if r2.code == exitPosseNegada {
			t.Fatalf("retoma %d: a posse do run tinha de ter sido largada, e está detida\n%s\n%s", i, r2.stdout, r2.stderr)
		}
		if r2.code != exitNosFalhados {
			t.Fatalf("retoma %d: tinha de voltar a sair %d, saiu %d\n%s\n%s", i, exitNosFalhados, r2.code, r2.stdout, r2.stderr)
		}
		if strings.Contains(r2.stdout, "despacho: ") || !strings.Contains(r2.stdout, "nos_despachados=0") {
			t.Fatalf("retoma %d: nada podia ser despachado de novo:\n%s", i, r2.stdout)
		}
		if strings.Contains(r2.stdout, "NAO corre") {
			t.Fatalf("retoma %d: o nó já fechado não se fecha outra vez:\n%s", i, r2.stdout)
		}
		if !strings.Contains(r2.stdout, "execucao: n1=complete n2=failed") {
			t.Fatalf("retoma %d: o estado dos nós tinha de vir do log:\n%s", i, r2.stdout)
		}
	}
	// SEM O EXECUTOR composto (sem `AOS_ORQ_NODE_URL`), o `serve --plan-doc` sobre o mesmo run vê
	// os mesmos nós `failed` no grafo re-hidratado e sai com o MESMO código. FALHA-ANTES: a
	// verificação vivia dentro do `if ex != nil`, e esta invocação saía 0 com `n2=failed` no log.
	r3 := correr(t, a.bin, "serve", "--wal", a.wal, "--run", run, "--plan-doc", a.doc, "--snapshot", a.snapPath, "--worker", "p3")
	if r3.code != exitNosFalhados {
		t.Fatalf("sem executor, o serve com o documento tinha de sair %d, saiu %d\n%s\n%s", exitNosFalhados, r3.code, r3.stdout, r3.stderr)
	}
	if !strings.Contains(r3.stdout, "executor de nos (AOS-413): NAO composto") || !strings.Contains(r3.stdout, "execucao: n1=complete n2=failed") {
		t.Fatalf("pre-condicao: esta invocação tinha de correr SEM executor e mostrar o estado dos nós:\n%s", r3.stdout)
	}
	// LIMITE DECLARADO (ver [exitNosFalhados]): um `serve` SEM documento não passa pelo despacho
	// do plano e não olha para o estado dos nós — sai 0. Fixado aqui para que mudar isto seja uma
	// decisão, e para que o comentário não volte a dizer «qualquer repetição dá o mesmo código».
	r4 := correr(t, a.bin, "serve", "--wal", a.wal, "--run", run, "--release", "--worker", "p4")
	if r4.code != exitOK {
		t.Fatalf("o serve sem documento não corre o plano e sai 0 (limite declarado); saiu %d\n%s\n%s", r4.code, r4.stdout, r4.stderr)
	}
	if depois := strings.Join(f.submetidos(), ","); depois != antes || antes != run+"~n1" {
		t.Fatalf("só o n1 podia ter corrido, uma vez: antes=%q depois=%q", antes, depois)
	}
	f.mu.Lock()
	leiturasDoN2 := f.lidos[run+"~n2"]
	f.mu.Unlock()
	if leiturasDoN2 != 0 {
		t.Fatalf("o n2 nunca foi um run: ninguém lhe podia ler o estado (%d leituras)", leiturasDoN2)
	}
}

// TestAOS484_DoisOutputsAbertosNaoSeTransportam prende a frase da regra 12 do prompt 1.4.0 — «com
// mais do que um, o executor nao transporta nenhum» — ao comportamento do executor. Se o
// `publicarSaidas` passar a transportar um deles, a regra fica a mentir ao modelo e isto avermelha.
func TestAOS484_DoisOutputsAbertosNaoSeTransportam(t *testing.T) {
	const run = "run-aos484-dois-abertos"
	f := &aos413No{}
	a, r := aos484Arrancar(t, f, run, aos484PlanoDoisAbertos)
	if r.code != exitNosFalhados {
		t.Fatalf("o consumidor sem payload fecha failed e o plano sai %d; saiu %d\n%s\n%s", exitNosFalhados, r.code, r.stdout, r.stderr)
	}
	if pubs := a.publicados(t); len(pubs) != 0 {
		t.Fatalf("com dois outputs abertos o executor não transporta nenhum, e publicou %+v", pubs)
	}
	if !strings.Contains(r.stdout, "o contrato n1/conteudo ficou por cumprir") ||
		!strings.Contains(r.stdout, "execucao: n1=complete n2=failed") {
		t.Fatalf("o n2 tinha de fechar sem payload, com a razão visível:\n%s", r.stdout)
	}
	if ids := f.submetidos(); len(ids) != 1 || ids[0] != run+"~n1" {
		t.Fatalf("só o n1 podia correr, correram %v", ids)
	}
}

// aos484PlanoRamoComMetrics: o `n3` está atrás do veredicto do `n2` (`conditional_on … verdict eq
// pass`) e declara um `consumes` de `metrics` do verificador — a forma dos outputs do `n2` é a do
// plano que o modelo vivo produziu (`aos413PlanoDoModeloVivo`). O validador admite-o; o executor
// nunca publica `metrics`. O `n3` não tem tools: sem risco, auto-aprova.
const aos484PlanoRamoComMetrics = `{
  "plan_version": "1.2.0",
  "objective": "ler, verificar e agir sobre as medidas do verificador",
  "budget_total": {"tokens": 150, "cost_micro_usd": 150},
  "planner_meta": {"model":"fixture","prompt_version":"1.4.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"n1","role":"reader","objective":"ler o relatorio","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "outputs":[{"name":"report_content","type":"record","taint":"untrusted"}]},
    {"node_id":"n2","role":"verifier","objective":"verificar o relatorio","depends_on":["n1"],
     "tools":[],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "outputs":[{"name":"checks","type":"metrics"},{"name":"decision","type":"verdict"}],
     "consumes":[{"from":"n1","output":"report_content","type":"record"}]},
    {"node_id":"n3","role":"worker","objective":"agir sobre as medidas","depends_on":[],
     "tools":[],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "conditional_on":[{"from":"n2","when":[{"subject":"verdict","op":"eq","enum":"pass"}]}],
     "consumes":[{"from":"n2","output":"checks","type":"metrics"}]}
  ]
}`

// TestAOS484_VeredictoFailQueRetemRamoComConsumesPorCumprirSai0 — critério «o ramo não tomado
// não é um nó falhado», no caso em que o ramo retido tem um `consumes` que não se cumpriria.
//
// FALHA-ANTES (encontrado pela revisão adversarial, com sonda): a poda dos consumidores sem
// payload corria antes da decisão de ramo e não perguntava se o nó ia correr. O `n3` — retido
// pelo veredicto `fail` — era fechado como `failed` por lhe faltar o `metrics`, e o plano saía 13.
// Antes do AOS-484 o mesmo plano saía 0: era o ticket a transformar um plano que correu como foi
// desenhado num plano falhado.
func TestAOS484_VeredictoFailQueRetemRamoComConsumesPorCumprirSai0(t *testing.T) {
	const run = "run-aos484-ramo-retido"
	f := &aos413No{saidaVerif: `{"outcome":"fail","reasons":["dados_sensiveis"]}`}
	a, r := aos484Arrancar(t, f, run, aos484PlanoRamoComMetrics)
	if r.code != exitOK {
		t.Fatalf("o veredicto fail que retém o ramo é um plano que correu bem: tinha de sair 0, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	exigir := func(r resultado, quando string) {
		t.Helper()
		if !strings.Contains(r.stdout, "execucao: n1=complete n2=complete n3=") || strings.Contains(r.stdout, "=failed") {
			t.Fatalf("%s: n1 e n2 concluem e o n3, retido, NÃO fica failed:\n%s", quando, r.stdout)
		}
		if strings.Contains(r.stdout, "NAO corre") || strings.Contains(r.stdout, "ficou por cumprir") {
			t.Fatalf("%s: um nó atrás de um ramo não tomado não se fecha por falta de payload:\n%s", quando, r.stdout)
		}
	}
	exigir(r, "1.ª invocação")
	if got := strings.Join(f.submetidos(), ","); got != run+"~n1,"+run+"~n2" {
		t.Fatalf("o n3 retido não podia correr; correram %q", got)
	}
	// A retoma lê a decisão «não tomado» do log e chega ao mesmo sítio: 0, e o n3 por fechar.
	r2 := a.retomar(t)
	if r2.code != exitOK {
		t.Fatalf("a retoma tinha de sair 0 outra vez, saiu %d\n%s\n%s", r2.code, r2.stdout, r2.stderr)
	}
	exigir(r2, "retoma")
	if got := strings.Join(f.submetidos(), ","); got != run+"~n1,"+run+"~n2" {
		t.Fatalf("a retoma não podia submeter o n3; há %q", got)
	}
}

// TestAOS484_MetricsNaoSeTransportaNemDeUmVerificador prende ao executor a frase da regra 12 do
// prompt 1.4.0 — «Um consumes de "metrics" NAO e entregue, venha de que no vier: o no que o declara
// nao corre e fica failed». É o MESMO plano do teste anterior, com veredicto `pass`: o ramo é
// tomado, o `n3` ia correr, e o que lhe falta é o `metrics` que ninguém mede (resíduo do AOS-414).
//
// Fixa também o caminho que a correcção do ramo retido abriu: a decisão «tomado» nasce na MESMA
// passagem em que o nó fica elegível, depois da poda. O sink recusa o nó sem payload, e o `serve`
// NÃO aborta com 1 (o defeito que o AOS-414 fechou) — a passagem seguinte fecha o nó.
func TestAOS484_MetricsNaoSeTransportaNemDeUmVerificador(t *testing.T) {
	const run = "run-aos484-metrics-do-verificador"
	f := &aos413No{saidaVerif: `{"outcome":"pass","reasons":["relatorio_valido"]}`}
	a, r := aos484Arrancar(t, f, run, aos484PlanoRamoComMetrics)
	if r.code != exitNosFalhados {
		t.Fatalf("com o ramo tomado e o metrics por entregar, o n3 falha e o plano sai %d; saiu %d\n%s\n%s", exitNosFalhados, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "execucao: n1=complete n2=complete n3=failed") {
		t.Fatalf("o n3 tinha de fechar failed:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "o contrato n2/checks ficou por cumprir") {
		t.Fatalf("a razão tinha de estar visível:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "despachado: plano="+run+"-plan") {
		t.Fatalf("o serve tinha de chegar ao fim do despacho, e não abortar na recusa do sink:\n%s", r.stdout)
	}
	// Do verificador transporta-se o `verdict`, e só ele: o `metrics` não chega ao log.
	var outputs []string
	for _, p := range a.publicados(t) {
		outputs = append(outputs, p.NodeID+"/"+p.Output)
	}
	if got := strings.Join(outputs, ","); got != "n1/report_content,n2/decision" {
		t.Fatalf("publicados: quero o record do n1 e o verdict do n2, e nunca o metrics; vieram %q", got)
	}
	if got := strings.Join(f.submetidos(), ","); got != run+"~n1,"+run+"~n2" {
		t.Fatalf("o n3 não podia correr sem o que consome; correram %q", got)
	}
	// A retoma não repete: mesmo código, nada despachado, o nó não se fecha outra vez.
	r2 := a.retomar(t)
	if r2.code != exitNosFalhados || strings.Contains(r2.stdout, "ficou por cumprir") || strings.Contains(r2.stdout, "despacho: ") {
		t.Fatalf("a retoma tinha de sair %d sem repetir nada; saiu %d\n%s\n%s", exitNosFalhados, r2.code, r2.stdout, r2.stderr)
	}
}

// aos484PlanoComRecuperacao: o `n3` é o ramo de RECUPERAÇÃO — só corre se o `n1` falhar.
const aos484PlanoComRecuperacao = `{
  "plan_version": "1.1.0",
  "objective": "ler, e recuperar se a leitura falhar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.4.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"n1","role":"reader","objective":"ler o relatorio","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50}},
    {"node_id":"n3","role":"worker","objective":"recuperar da leitura falhada","depends_on":[],
     "tools":[],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "conditional_on":[{"from":"n1","when":[{"subject":"terminal_state","op":"eq","enum":"failed"}]}]}
  ]
}`

// TestAOS484_RamoDeRecuperacaoCorreEASaidaE13 FIXA um limite declarado, a decidir pelo dono: um nó
// `failed` cuja falha o plano previa (`conditional_on … terminal_state eq failed`) dá a saída 13
// na mesma. O ramo de recuperação corre e conclui; o critério do ticket é «um nó `failed` ⇒ não
// sai 0», e não pergunta se a falha foi tratada. Se o dono decidir o contrário, é este teste que
// muda.
func TestAOS484_RamoDeRecuperacaoCorreEASaidaE13(t *testing.T) {
	const run = "run-aos484-recuperacao"
	f := &aos413No{aMeio: map[string]bool{"n1": true}}
	_, r := aos484Arrancar(t, f, run, aos484PlanoComRecuperacao)
	if !strings.Contains(r.stdout, "execucao: n1=failed n3=complete") {
		t.Fatalf("pre-condicao: o n1 falha e o ramo de recuperação corre e conclui:\n%s\n%s", r.stdout, r.stderr)
	}
	if got := strings.Join(f.submetidos(), ","); got != run+"~n1,"+run+"~n3" {
		t.Fatalf("o ramo de recuperação tinha de correr; correram %q", got)
	}
	if r.code != exitNosFalhados {
		t.Fatalf("limite declarado: com o n1 failed a saída é %d mesmo com a recuperação concluída; saiu %d\n%s\n%s", exitNosFalhados, r.code, r.stdout, r.stderr)
	}
}

// TestAOS484_OConsumeReportaOPlanoFalhadoComoTerminal13 segue o código até quem o consome: o
// desfecho que chega ao nó (`planrequest.outcome`: classe, código, detalhe), a linha `aviso:` que
// o drenar-planos.sh recolhe, e as métricas que o alerta lê.
func TestAOS484_OConsumeReportaOPlanoFalhadoComoTerminal13(t *testing.T) {
	bin := construir(t)
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	espiao := &aos443Espiao{}
	frente := espiao.servidor(t, strings.TrimPrefix(a.env[0], "AOS_ORQ_NODE_URL="))
	a.env[0] = "AOS_ORQ_NODE_URL=" + frente.URL
	const run = "plan-aos484-falhado"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "medir e reagir", Geracao: 1})
	r := a.consumir(t, aos414PlanoComMetrics, "--plan-timeout", "30s")

	// (1) O nó recebe terminal/13 — nem sucesso (0), nem transitório (voltaria à fila).
	exigirDesfechos(t, no.vistos(), pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "terminal", Codigo: exitNosFalhados})
	// (2) O detalhe é o resumo com o NOME ESTÁVEL do erro, nunca o texto dele (que cita node_ids).
	ds := espiao.detalhes()
	if len(ds) != 1 || !strings.HasPrefix(ds[0], "resumo: origem=decomposicao geracao=1 nos=2 duracao_s=") ||
		!strings.HasSuffix(ds[0], " erro=nos_falhados") {
		t.Fatalf("detail do desfecho: %q", ds)
	}
	if strings.Contains(ds[0], "n2") || strings.Contains(ds[0], "terminou com") {
		t.Fatalf("o detail leva texto do erro: %q", ds[0])
	}
	// (3) A linha que a drenagem recolhe para o aviso ao operador.
	if quer := linhaDoAviso(run, 1, "terminal", exitNosFalhados); !strings.Contains(r.stdout, quer+"\n") {
		t.Fatalf("faltou a linha %q:\n%s", quer, r.stdout)
	}
	if !strings.Contains(r.stdout, "desfecho: run="+run+" codigo=13 classe=terminal ") {
		t.Fatalf("a linha do desfecho tinha de dizer codigo=13 classe=terminal:\n%s", r.stdout)
	}
	// (4) As métricas: conta como terminal com o código 13, e SOMA nas falhas seguidas — um plano
	// que acabou com nós falhados é um plano que acabou mal.
	m, err := lerMetricas(filepath.Join(a.dir, nomeDoFicheiroDeMetricas))
	if err != nil {
		t.Fatal(err)
	}
	exigirSerie(t, m, serie(metricaDesfechos, "classe", "terminal", "codigo", "13"), 1)
	exigirSerie(t, m, metricaFalhasConsecutivas, 1)
	// (5) O pedido fechou: o documento do plano sai do disco.
	exigirDocumentoApagado(t, a.documentoDe(t, run))
	// (6) Só o n1 correu.
	if ids := no.submetidos(); len(ids) != 1 || ids[0] != run+"~n1" {
		t.Fatalf("só o n1 podia correr, correram %v", ids)
	}
}

// TestAOS484_OCodigo13NasTabelas: o sentinela, o código, a classe, o tipo e a largada da posse
// dizem o mesmo — as quatro tabelas ([codigoDe], [classeDoDesfecho], [tipoDoErro], [largaAPosse])
// não podem divergir.
func TestAOS484_OCodigo13NasTabelas(t *testing.T) {
	if exitNosFalhados != 13 {
		t.Fatalf("o código dos nós falhados é contrato com os scripts de deploy (avisar-planos.sh): %d", exitNosFalhados)
	}
	err := despachoEmbrulhado(errNosFalhados)
	codigo, classe, tipo := desfechoDoServe(err)
	if codigo != exitNosFalhados || classe != "terminal" || tipo != "nos_falhados" {
		t.Fatalf("desfecho de um plano com nós falhados: codigo=%d classe=%q tipo=%q", codigo, classe, tipo)
	}
	if !largaAPosse(err) {
		t.Fatal("o plano acabou: a posse tem de ser largada, senão a invocação seguinte espera pelo TTL")
	}
	if efeitoNasFalhas(classe, codigo) != somaFalha {
		t.Fatal("um plano com nós falhados tem de somar nas falhas seguidas que o alerta lê")
	}
	// Não se confunde com os vizinhos: nós EM VOO é transitório (retoma-se), e sem erro é 0.
	if c, cl, _ := desfechoDoServe(despachoEmbrulhado(errNosEmVoo)); c != exitNosEmVoo || cl != "transitorio" {
		t.Fatalf("nós em voo continua transitório/8: %d %q", c, cl)
	}
	if c, cl, tp := desfechoDoServe(nil); c != exitOK || cl != "terminal" || tp != "" {
		t.Fatalf("sem erro continua terminal/0: %d %q %q", c, cl, tp)
	}
}

// despachoEmbrulhado embrulha o sentinela como o `serve` o faz (despachar → composeEDespachar).
func despachoEmbrulhado(sentinela error) error {
	return errEmbrulhado{"despacho governado", errEmbrulhado{"2 de 3 no(s) do plano run-x-plan (n2,n3)", sentinela}}
}

type errEmbrulhado struct {
	contexto string
	dentro   error
}

func (e errEmbrulhado) Error() string { return e.contexto + ": " + e.dentro.Error() }
func (e errEmbrulhado) Unwrap() error { return e.dentro }
