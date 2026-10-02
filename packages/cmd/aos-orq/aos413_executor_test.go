package main

// AOS-413 (ADR-027) — o organigrama executa até ao fim, pelo processo real do `aos-orq` contra um
// nó `aos` falso (httptest): cada nó despachado é um run submetido por POST /runs, a conclusão
// volta ao log, o veredicto do verificador decide o ramo condicional, e o nó de risco só corre
// com a aprovação humana e o `pass`.
//
// O plano é o que o modelo vivo produziu em produção no run `run-aos412-vivo-1`: `n1` lê, `n2`
// verifica o que o `n1` leu, `n3` publica (tool `danger`) só se o `n2` disser `pass`. Foi esse
// run que parou no `n1` — `running` para sempre — e que este ticket existe para levar ao fim.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

const aos413PlanoDoModeloVivo = `{
  "plan_version": "1.2.0",
  "objective": "ler-o-relatorio-interno-e-publica-lo-num-servico-externo",
  "budget_total": {"tokens": 1500, "cost_micro_usd": 1500},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"n1","role":"reader","objective":"Ler o relatorio interno",
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],"depends_on":[],
     "budget_estimate":{"tokens":500,"cost_micro_usd":500},
     "outputs":[{"name":"report_content","type":"record","taint":"untrusted"}]},
    {"node_id":"n2","role":"verifier","objective":"Verificar se o relatorio e publicavel",
     "tools":null,"depends_on":["n1"],"budget_estimate":{"tokens":400,"cost_micro_usd":400},
     "outputs":[{"name":"checks","type":"metrics"},{"name":"decision","type":"verdict"}],
     "consumes":[{"from":"n1","output":"report_content","type":"record"}]},
    {"node_id":"n3","role":"publisher","objective":"Publicar o relatorio no servico externo",
     "tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"}],"depends_on":[],
     "budget_estimate":{"tokens":600,"cost_micro_usd":600},
     "conditional_on":[{"from":"n2","when":[{"subject":"verdict","op":"eq","enum":"pass"}]}],
     "consumes":[{"from":"n2","output":"decision","type":"verdict"}]}
  ]
}`

// aos413No é o nó `aos` falso: guarda as submissões e responde ao GET com o guião do teste.
type aos413No struct {
	mu         sync.Mutex
	submissoes map[string]map[string]any // run_id → corpo
	emCurso    int                       // quantos GET respondem in_progress antes do desfecho
	lidos      map[string]int
	saidaVerif string // final_text do run verificador
	nuncaAcaba bool
	// aMeio são os nós cujo run acaba `completed` SEM `terminated` — parou por orçamento ou
	// turnos, com o trabalho a meio.
	aMeio map[string]bool
}

func (f *aos413No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	f.submissoes = map[string]map[string]any{}
	f.lidos = map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		var corpo map[string]any
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		id, _ := corpo["run_id"].(string)
		f.mu.Lock()
		_, repetida := f.submissoes[id]
		f.submissoes[id] = corpo
		f.mu.Unlock()
		if repetida {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		_, existe := f.submissoes[id]
		f.lidos[id]++
		n := f.lidos[id]
		f.mu.Unlock()
		if !existe {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.nuncaAcaba || n <= f.emCurso {
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "in_progress"})
			return
		}
		saida := "feito: " + id
		if strings.HasSuffix(id, "~n2") {
			saida = f.saidaVerif
		}
		no := id[strings.LastIndex(id, "~")+1:]
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "completed", "terminated": !f.aMeio[no], "final_text": saida})
	})
	// AOS-441: o `serve` com executor confere o snapshot com o catálogo do nó antes da posse. O
	// nó falso tem as tools do snapshot destes testes (aos408SnapshotComPerigo), tal e qual.
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(aos441CatalogoDoSnapshotComPerigo))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *aos413No) submetidos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.submissoes))
	for id := range f.submissoes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// inputs devolve os payloads que um run filho recebeu (AOS-414).
func (f *aos413No) inputs(id string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	xs, _ := f.submissoes[id]["inputs"].([]any)
	return xs
}

func (f *aos413No) tools(id string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts, _ := f.submissoes[id]["tools"].([]any)
	return ts
}

func correrComEnv(t *testing.T, env []string, bin string, args ...string) resultado {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !asExitError(err, &ee) {
			t.Fatalf("execução de %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return resultado{code: code, stdout: out.String(), stderr: errb.String()}
}

// aos413Aprovado corre a cerimónia do plano do modelo vivo e devolve o ambiente do executor e os
// caminhos. O nó falso NÃO pode receber nada até à aprovação — o pendente não executa.
func aos413Aprovado(t *testing.T, f *aos413No, run string) (env []string, bin, wal, snapPath, doc string) {
	t.Helper()
	srv := f.servidor(t)
	bin = construir(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	env = []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}

	snapPath = filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, aos413PlanoDoModeloVivo)
	doc = filepath.Join(dir, "aprovado.json")
	wal = filepath.Join(dir, "es.wal")

	r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--goal", "ler-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", fix, "--plan-out", doc, "--worker", "p1")
	if r.code != exitPendenteDeAprovacao {
		t.Fatalf("o plano com n3 danger tinha de ficar pendente, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if ids := f.submetidos(); len(ids) != 0 {
		t.Fatalf("um plano PENDENTE não pode executar nada no nó, e submeteu %v", ids)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", run)
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("sem request_id:\n%s", pl.stdout)
	}
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", m[1], "human:alice", priv, true)
	if d := correr(t, bin, "decide", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snapPath,
		"--decision", "approve", "--approval", aprovacao, "--approvers", aprovadores); d.code != exitOK {
		t.Fatalf("aprovação: %d\n%s\n%s", d.code, d.stdout, d.stderr)
	}
	return env, bin, wal, snapPath, doc
}

func aos413Serve(t *testing.T, env []string, bin, wal, run, snapPath, doc string, extra ...string) resultado {
	t.Helper()
	args := append([]string{"serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snapPath,
		"--worker", "p2", "--poll-interval", "20ms"}, extra...)
	return correrComEnv(t, env, bin, args...)
}

// TestAOS413_OrganigramaAprovadoExecutaAteAoFim é o caso para que o ticket existe.
// FALHA-ANTES: o `serve --plan-doc` parava com o `n1` a correr e nada submetido ao nó.
func TestAOS413_OrganigramaAprovadoExecutaAteAoFim(t *testing.T) {
	f := &aos413No{emCurso: 2, saidaVerif: `{"outcome":"pass","reasons":["relatorio_valido"]}`}
	const run = "run-aos413-fim"
	env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)

	r := aos413Serve(t, env, bin, wal, run, snapPath, doc)
	if r.code != exitOK {
		t.Fatalf("o organigrama aprovado tinha de executar até ao fim, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if got := strings.Join(f.submetidos(), ","); got != run+"~n1,"+run+"~n2,"+run+"~n3" {
		t.Fatalf("os três nós tinham de ser runs do nó, foram %q\n%s", got, r.stdout)
	}
	if !strings.Contains(r.stdout, "execucao: n1=complete n2=complete n3=complete") {
		t.Fatalf("o plano tinha de terminar com os três nós concluídos:\n%s", r.stdout)
	}
	// A lista-branca de cada run é a do SEU nó: o verificador sem tools leva [] (nenhuma), e só o
	// n3 leva a tool de risco.
	for id, quer := range map[string]string{"~n1": "fs.read", "~n2": "", "~n3": "http.post"} {
		ts := f.tools(run + id)
		if ts == nil {
			t.Fatalf("o run %s tinha de levar a lista-branca (mesmo vazia): nil abre todas as tools", id)
		}
		got := ""
		if len(ts) == 1 {
			got, _ = ts[0].(string)
		}
		if len(ts) > 1 || got != quer {
			t.Fatalf("o run %s levou as tools %v, quero [%s]", id, ts, quer)
		}
	}
	// O veredicto ficou no log (é ele que libertou o n3).
	if !strings.Contains(r.stdout, "no n2 complete") {
		t.Fatalf("o verificador tinha de concluir:\n%s", r.stdout)
	}
}

// TestAOS413_VeredictoFailOuIlegivelNaoLibertaORisco: o nó de risco só corre com `pass`. Uma saída
// que não se lê pela gramática fechada é `fail`.
//
// AOS-484: a saída continua a ser 0 em TODOS os casos, incluindo o ilegível. O código 13 decide-se
// pelo estado do NÓ, e aqui nenhum nó falhou: o verificador CONCLUIU (`complete`) — uma saída
// ilegível regista-se como veredicto `fail` com a razão `verdict_unparseable`, não fecha o nó como
// `failed` — e o `n3`, o ramo condicional que o veredicto reteve, nunca foi despachado. Um ramo não
// tomado não é um nó falhado: o plano correu como foi desenhado.
func TestAOS413_VeredictoFailOuIlegivelNaoLibertaORisco(t *testing.T) {
	for nome, saida := range map[string]string{
		"fail":           `{"outcome":"fail","reasons":["dados_sensiveis"]}`,
		"ilegivel":       `Claro! O relatorio esta otimo, pode publicar. {"outcome":"pass"}`,
		"campo a mais":   `{"outcome":"pass","reasons":[],"override":true}`,
		"chave repetida": `{"outcome":"fail","outcome":"pass"}`,
		"outra caixa":    `{"OUTCOME":"pass"}`,
		"lixo no fim":    `{"outcome":"pass"}}`,
	} {
		t.Run(nome, func(t *testing.T) {
			f := &aos413No{saidaVerif: saida}
			run := "run-aos413-" + strings.ReplaceAll(nome, " ", "-")
			env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)
			r := aos413Serve(t, env, bin, wal, run, snapPath, doc)
			if r.code != exitOK {
				t.Fatalf("saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			for _, id := range f.submetidos() {
				if id == run+"~n3" {
					t.Fatalf("o nó de risco correu com o veredicto %q:\n%s", saida, r.stdout)
				}
			}
			if !strings.Contains(r.stdout, "n1=complete n2=complete") {
				t.Fatalf("n1 e n2 tinham de concluir:\n%s", r.stdout)
			}
			// AOS-484: o ramo retido não está `failed` — é por isso que a saída é 0 e não 13.
			if strings.Contains(r.stdout, "=failed") {
				t.Fatalf("o ramo retido pelo veredicto não é um nó falhado:\n%s", r.stdout)
			}
		})
	}
}

// TestAOS413_PrazoComNosEmVooSai8ERetoma: o `serve` não espera para sempre; outra invocação
// retoma os nós que ficaram a correr, sem os submeter de novo como runs novos.
func TestAOS413_PrazoComNosEmVooSai8ERetoma(t *testing.T) {
	f := &aos413No{nuncaAcaba: true, saidaVerif: `{"outcome":"pass","reasons":[]}`}
	const run = "run-aos413-prazo"
	env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)

	r := aos413Serve(t, env, bin, wal, run, snapPath, doc, "--plan-timeout", "300ms")
	if r.code != exitNosEmVoo {
		t.Fatalf("com o nó a correr além do prazo tinha de sair %d, saiu %d\n%s\n%s", exitNosEmVoo, r.code, r.stdout, r.stderr)
	}
	// O nó acaba entretanto; a retoma recolhe-o e leva o plano ao fim.
	f.mu.Lock()
	f.nuncaAcaba = false
	f.mu.Unlock()
	r2 := aos413Serve(t, env, bin, wal, run, snapPath, doc)
	if r2.code != exitOK || !strings.Contains(r2.stdout, "execucao: n1=complete n2=complete n3=complete") {
		t.Fatalf("a retoma tinha de levar o plano ao fim, saiu %d\n%s\n%s", r2.code, r2.stdout, r2.stderr)
	}
}

// Um run que parou por esgotar o orçamento responde `completed` sem `terminated`: é trabalho a
// meio, e o nó fica `failed` — os dependentes não arrancam sobre ele.
//
// AOS-484: e o `serve` sai com 13, não com 0. Exigia-se 0 aqui, e era o defeito: o plano chegava ao
// fim com o primeiro nó a meio, nenhum dependente corria, e o desfecho era o de um plano bem-sucedido.
func TestAOS413_RunAMeioNaoConcluiONo(t *testing.T) {
	f := &aos413No{saidaVerif: `{"outcome":"pass","reasons":[]}`, aMeio: map[string]bool{"n1": true}}
	const run = "run-aos413-a-meio"
	env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)
	r := aos413Serve(t, env, bin, wal, run, snapPath, doc)
	if r.code != exitNosFalhados {
		t.Fatalf("com o n1 a meio tinha de sair %d, saiu %d\n%s\n%s", exitNosFalhados, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "n1=failed") {
		t.Fatalf("o n1 que parou a meio tinha de ficar failed:\n%s", r.stdout)
	}
	for _, id := range f.submetidos() {
		if id == run+"~n2" || id == run+"~n3" {
			t.Fatalf("um dependente arrancou sobre trabalho a meio (%s):\n%s", id, r.stdout)
		}
	}
}

// Um run com o id do run filho que o plano NÃO criou (409 na primeira submissão) não é aceite:
// o seu desfecho seria um veredicto alheio.
func TestAOS413_RunFilhoPreExistenteERecusado(t *testing.T) {
	f := &aos413No{saidaVerif: `{"outcome":"pass","reasons":[]}`}
	const run = "run-aos413-squat"
	env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)
	// Alguém criou de antemão o run do verificador.
	f.mu.Lock()
	f.submissoes[run+"~n2"] = map[string]any{"objective": "alheio"}
	f.mu.Unlock()
	r := aos413Serve(t, env, bin, wal, run, snapPath, doc)
	if r.code == exitOK || !strings.Contains(r.stderr, "nao foi este plano que o criou") {
		t.Fatalf("um run filho pré-existente tinha de ser recusado, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	for _, id := range f.submetidos() {
		if id == run+"~n3" {
			t.Fatalf("o nó de risco correu com o veredicto de um run alheio:\n%s", r.stdout)
		}
	}
}

func TestAOS413_RunComOSeparadorERecusado(t *testing.T) {
	bin := construir(t)
	r := correr(t, bin, "serve", "--wal", filepath.Join(t.TempDir(), "es.wal"), "--run", "a~b", "--nodes", "x")
	if r.code == exitOK || !strings.Contains(r.stderr, "~") {
		t.Fatalf("um run_id com ~ tinha de ser recusado, saiu %d\n%s", r.code, r.stderr)
	}
}

// TestAOS414_OVerificadorRecebeOQueONoAnteriorLeu é o caso que a produção mediu em falta: o
// verificador reprovava com `documento_nao_fornecido` porque não via o documento.
// FALHA-ANTES: o run do verificador era submetido sem `inputs`.
func TestAOS414_OVerificadorRecebeOQueONoAnteriorLeu(t *testing.T) {
	f := &aos413No{saidaVerif: `{"outcome":"pass","reasons":["documento_valido"]}`}
	const run = "run-aos414-cadeia"
	env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)

	r := aos413Serve(t, env, bin, wal, run, snapPath, doc)
	if r.code != exitOK {
		t.Fatalf("saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	// (1) O verificador recebeu o payload do n1, pelo contrato que o SEU `consumes` declara.
	xs := f.inputs(run + "~n2")
	if len(xs) != 1 {
		t.Fatalf("o verificador tinha de receber 1 payload, recebeu %d: %v", len(xs), xs)
	}
	in, _ := xs[0].(map[string]any)
	if in["from"] != "n1" || in["output"] != "report_content" {
		t.Fatalf("o payload veio com outro contrato: %v", in)
	}
	if conteudo, _ := in["content"].(string); !strings.Contains(conteudo, run+"~n1") {
		t.Fatalf("o conteúdo não é a saída do n1: %q", conteudo)
	}
	if dig, _ := in["digest"].(string); !strings.HasPrefix(dig, "sha256:") {
		t.Fatalf("o payload tinha de levar digest: %v", in)
	}
	// (2) O contrato ficou publicado no log, com a referência.
	if !strings.Contains(r.stdout, "payload n1/report_content publicado") {
		t.Fatalf("o contrato do n1 tinha de ser publicado:\n%s", r.stdout)
	}
	// (3) O veredicto do verificador é uma forma FECHADA publicada, e o n3 (danger, aprovado)
	// recebeu-a e correu.
	if !strings.Contains(r.stdout, "payload n2/decision publicado (verdict)") {
		t.Fatalf("o veredicto tinha de ser publicado como forma fechada:\n%s", r.stdout)
	}
	if xs3 := f.inputs(run + "~n3"); len(xs3) != 1 {
		t.Fatalf("o nó de risco tinha de receber o veredicto, recebeu %v", xs3)
	}
	if !strings.Contains(r.stdout, "execucao: n1=complete n2=complete n3=complete") {
		t.Fatalf("a cadeia tinha de chegar ao fim:\n%s", r.stdout)
	}
}

// Um nó só recebe o que o SEU `consumes` declara — não o que houver publicado.
func TestAOS414_SoOQueOConsumesDeclara(t *testing.T) {
	f := &aos413No{saidaVerif: `{"outcome":"pass","reasons":[]}`}
	const run = "run-aos414-so-o-seu"
	env, bin, wal, snapPath, doc := aos413Aprovado(t, f, run)
	if r := aos413Serve(t, env, bin, wal, run, snapPath, doc); r.code != exitOK {
		t.Fatalf("saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	// O n1 não consome nada; o n3 consome só o veredicto do n2, não o documento do n1.
	if xs := f.inputs(run + "~n1"); len(xs) != 0 {
		t.Fatalf("o n1 não consome nada e recebeu %v", xs)
	}
	xs3 := f.inputs(run + "~n3")
	if len(xs3) != 1 {
		t.Fatalf("o n3 tinha de receber só o veredicto, recebeu %v", xs3)
	}
	if in, _ := xs3[0].(map[string]any); in["from"] != "n2" || in["output"] != "decision" {
		t.Fatalf("o n3 recebeu um contrato que não declara: %v", xs3[0])
	}
}

// aos414PlanoComMetrics: o n2 consome um contrato `metrics` do n1 — que o validador ADMITE e que
// o executor NUNCA consegue publicar (ninguem mede os numeros). Sem risco: auto-aprova.
const aos414PlanoComMetrics = `{
  "plan_version": "1.2.0",
  "objective": "medir e reagir",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"n1","role":"worker","objective":"medir","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10},
     "outputs":[{"name":"medidas","type":"metrics"}]},
    {"node_id":"n2","role":"worker","objective":"reagir as medidas","depends_on":["n1"],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10},
     "consumes":[{"from":"n1","output":"medidas","type":"metrics"}]}
  ]
}`

// TestAOS414_ContratoPorCumprirFalhaONoENaoOServe: um contrato que nunca pode ser cumprido
// fecha o CONSUMIDOR e deixa o plano terminar. FALHA-ANTES: o sink recusava, a passagem abortava,
// o serve saia com erro e TODAS as retomas repetiam o mesmo — o plano nunca acabava.
//
// AOS-484: o código de saída passa de 0 para 13 (um nó `failed` não é um plano bem-sucedido). O
// que este teste protege não muda, e continua provado: o `serve` não ABORTA a meio — chega ao fim
// do plano, imprime o resumo e larga a posse — e a retoma não repete o erro nem re-executa nada.
// Um `1` genérico (a passagem abortada de antes) continuaria a avermelhar aqui.
func TestAOS414_ContratoPorCumprirFalhaONoENaoOServe(t *testing.T) {
	f := &aos413No{}
	srv := f.servidor(t)
	bin := construir(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, aos414PlanoComMetrics)
	wal := filepath.Join(dir, "es.wal")
	const run = "run-aos414-metrics"

	doc := filepath.Join(dir, "validado.json")
	r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--goal", "medir-e-reagir",
		"--snapshot", snapPath, "--decompose-fixture", fix, "--plan-out", doc, "--worker", "p1", "--poll-interval", "20ms")
	if r.code != exitNosFalhados {
		t.Fatalf("o serve tinha de TERMINAR o plano e sair %d (nó falhado), saiu %d\n%s\n%s", exitNosFalhados, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "despachado: plano="+run+"-plan") {
		t.Fatalf("o serve tinha de chegar ao FIM do despacho, e não abortar a meio:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "execucao: n1=complete n2=failed") {
		t.Fatalf("o n1 tinha de concluir e o n2 fechar sem payload:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "o contrato n1/medidas ficou por cumprir") {
		t.Fatalf("a razão tinha de estar visível:\n%s", r.stdout)
	}
	if ids := f.submetidos(); len(ids) != 1 || ids[0] != run+"~n1" {
		t.Fatalf("só o n1 podia correr, correram %v", ids)
	}
	// A RETOMA não repete: a posse foi largada (não sai 3), o nó já fechado não se fecha outra vez,
	// nada é despachado, e o desfecho é o mesmo.
	r2 := aos413Serve(t, env, bin, wal, run, snapPath, doc)
	if r2.code != exitNosFalhados {
		t.Fatalf("a retoma tinha de sair %d outra vez, saiu %d\n%s\n%s", exitNosFalhados, r2.code, r2.stdout, r2.stderr)
	}
	if strings.Contains(r2.stdout, "ficou por cumprir") || strings.Contains(r2.stdout, "despacho: ") {
		t.Fatalf("a retoma não podia repetir o fecho do nó nem despachar:\n%s", r2.stdout)
	}
	if ids := f.submetidos(); len(ids) != 1 || ids[0] != run+"~n1" {
		t.Fatalf("a retoma não podia submeter nada, e há %v", ids)
	}
}
