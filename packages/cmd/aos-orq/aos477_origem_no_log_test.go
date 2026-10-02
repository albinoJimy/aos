package main

// aos477_origem_no_log_test.go — O REGISTO DO PLANO CHEGA AO OBJECTIVO E AO PEDIDO (AOS-477),
// pelo processo real do `aos-orq`.
//
//  1. O compromisso tem o vector do contrato — o MESMO dos testes do nó, que o calcula com a sua
//     cópia da função na ingestão.
//  2. `serve --goal` manual: o `plan.proposed` leva o compromisso, o texto do objectivo não entra
//     no WAL, e com o sal impresso e o objectivo recalcula-se o valor do log.
//  3. `consume`: o `plan.proposed` cita o pedido (stream + seq) e o compromisso é o do pedido; o
//     sal do nó NÃO aparece no stdout; cada run filho leva o plano e o nó num campo.
//  4. `consume` com um objectivo que não é o do pedido: recusa ANTES da posse e do modelo.
//
// O stream da fila é OPACO para este binário: copia-o da reclamação para o `plan.proposed` sem o
// conhecer (o `TestAOS417BannerDoConsumidorNaoApodrece` do nó recusa que esta árvore o nomeie).
// Daí o nome inventado nos testes.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// vectorDoContrato: sal 00..1f, objectivo «recolher e analisar dados». O mesmo valor está em
// `packages/cmd/aos/aos477_origem_test.go` e em `orchestrator/plannerevents/aos477_proposta_test.go`.
const (
	vectorDoContratoObjectivo   = "recolher e analisar dados"
	vectorDoContratoCompromisso = "hmac-sha256:a95479ee76699c6bd97e848784f11a25ae02d8a5f7175f376e02b071ea8e1b2f"
)

func salDoVector() []byte {
	sal := make([]byte, 32)
	for i := range sal {
		sal[i] = byte(i)
	}
	return sal
}

func TestAOS477CompromissoTemOVectorDoContrato(t *testing.T) {
	if c := compromissoDoObjectivo(salDoVector(), vectorDoContratoObjectivo); c != vectorDoContratoCompromisso {
		t.Fatalf("o compromisso divergiu do contrato partilhado com o nó:\n quer %s\n veio %s", vectorDoContratoCompromisso, c)
	}
	// E não é o SHA-256 sem chave — esse inverte-se por dicionário a partir do log.
	nu := sha256.Sum256([]byte(vectorDoContratoObjectivo))
	if strings.HasSuffix(vectorDoContratoCompromisso, hex.EncodeToString(nu[:])) {
		t.Fatal("o compromisso nao pode ser o SHA-256 do objectivo")
	}
}

func TestAOS477ResolverOrigemNoLog(t *testing.T) {
	salHex := hex.EncodeToString(salDoVector())

	// Manual: sal novo, impresso, e o compromisso é o HMAC desse sal.
	o, impresso, err := resolverOrigemNoLog("objectivo", "", "", "", 0, "run", false)
	if err != nil || impresso == "" || o.pedido != nil {
		t.Fatalf("manual: %+v %q %v", o, impresso, err)
	}
	sal, _ := hex.DecodeString(impresso)
	if o.compromisso != compromissoDoObjectivo(sal, "objectivo") {
		t.Fatal("manual: o compromisso tem de ser o do sal impresso")
	}
	if o2, imp2, _ := resolverOrigemNoLog("objectivo", "", "", "", 0, "run", false); imp2 == impresso || o2.compromisso == o.compromisso {
		t.Fatal("dois serves manuais com o mesmo objectivo nao podem dar o mesmo compromisso (sal por pedido)")
	}

	// Fila: o sal vem do nó e o compromisso tem de bater com o do pedido.
	o, impresso, err = resolverOrigemNoLog(vectorDoContratoObjectivo, salHex, vectorDoContratoCompromisso, "fila-opaca/pedidos", 44, "plano", true)
	if err != nil || impresso != "" {
		t.Fatalf("fila: %v (sal impresso %q — o da fila nunca se imprime)", err, impresso)
	}
	if o.compromisso != vectorDoContratoCompromisso || o.pedido == nil || *o.pedido != (plannerevents.PlanRequestRef{Stream: "fila-opaca/pedidos", Seq: 44, RunID: "plano"}) {
		t.Fatalf("fila: %+v %+v", o, o.pedido)
	}
	if _, _, err := resolverOrigemNoLog("outro objectivo", salHex, vectorDoContratoCompromisso, "s", 1, "plano", true); !errors.Is(err, errObjectivoNaoEOdoPedido) {
		t.Fatalf("um objectivo que nao e o do pedido tem de ser recusado, veio %v", err)
	}

	// Formas inválidas.
	for nome, c := range map[string][5]string{
		"compromisso sem sal": {"g", "", vectorDoContratoCompromisso, "", ""},
		"sal curto":           {"g", "abcd", "", "", ""},
		"sal nao hex":         {"g", strings.Repeat("zz", 32), "", "", ""},
		"sal sem goal":        {"", salHex, "", "", ""},
		"seq sem stream":      {"g", "", "", "", "seq"},
	} {
		var seq uint64
		if c[4] == "seq" {
			seq = 3
		}
		if _, _, err := resolverOrigemNoLog(c[0], c[1], c[2], c[3], seq, "run", false); err == nil {
			t.Errorf("%s: tinha de recusar", nome)
		}
	}
	// --plan-doc com pedido: cita o pedido, sem compromisso.
	if o, _, err := resolverOrigemNoLog("", "", "", "s", 9, "run", true); err != nil || o.compromisso != "" || o.pedido == nil {
		t.Fatalf("--plan-doc com pedido: %+v %v", o, err)
	}
	// Fila sem sal (pedido ou nó anterior ao AOS-477): sem compromisso, e NENHUM sal tirado e
	// impresso — seria a chave de um compromisso sobre um objectivo selado pelo nó, no journal.
	if o, imp, err := resolverOrigemNoLog("objectivo", "", "", "s", 9, "run", true); err != nil || o.compromisso != "" || imp != "" || o.pedido == nil {
		t.Fatalf("fila sem sal: %+v %q %v", o, imp, err)
	}
}

// propostaDoWAL lê o `plan.proposed` do stream do plano no WAL, só para leitura.
func propostaDoWAL(t *testing.T, wal, planID string) (plannerevents.ProposedPayload, bool) {
	t.Helper()
	st, err := eventstore.OpenReadOnly(wal)
	if err != nil {
		t.Fatalf("abrir o WAL %s: %v", wal, err)
	}
	defer func() { _ = st.Close() }()
	eventos, err := st.Read(context.Background(), planID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return plannerevents.ProposedPayload{}, false
		}
		t.Fatal(err)
	}
	for _, e := range eventos {
		if e.Type == plannerevents.EventProposed {
			var p plannerevents.ProposedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			return p, true
		}
	}
	return plannerevents.ProposedPayload{}, false
}

var reCompromissoImpresso = regexp.MustCompile(`compromisso do objectivo: (hmac-sha256:[0-9a-f]{64}) sal=([0-9a-f]{64})`)

func TestAOS477ServeManualComprometeOObjectivoSemOGravar(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "orq.wal")
	const goal = "auditar o pipeline de faturas; contacto ana.silva@example.com"

	r := correrComEnv(t, []string{"AOS_ORQ_NODE_URL=", "AOS_MODE="}, bin, "serve", "--wal", wal, "--run", "run-477",
		"--goal", goal, "--snapshot", snap, "--decompose-fixture", fix, "--worker", "p1", "--release")
	if r.code != exitOK {
		t.Fatalf("serve --goal: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	m := reCompromissoImpresso.FindStringSubmatch(r.stdout)
	if m == nil {
		t.Fatalf("o serve manual tem de imprimir o compromisso e o sal:\n%s", r.stdout)
	}
	p, ok := propostaDoWAL(t, wal, "run-477-plan")
	if !ok {
		t.Fatal("sem plan.proposed no WAL")
	}
	if p.ObjectiveCommitment != m[1] {
		t.Fatalf("o plan.proposed tem de levar o compromisso impresso: log %q, stdout %q", p.ObjectiveCommitment, m[1])
	}
	if p.Request != nil {
		t.Fatalf("um serve manual nao tem pedido a citar: %+v", p.Request)
	}
	// VERIFICÁVEL por quem tem o texto e o sal — fora do binário, como um auditor o faria.
	sal, _ := hex.DecodeString(m[2])
	mac := hmac.New(sha256.New, sal)
	mac.Write([]byte(goal))
	if plannerevents.CommitmentScheme+hex.EncodeToString(mac.Sum(nil)) != p.ObjectiveCommitment {
		t.Fatal("HMAC-SHA256(sal, objectivo) tem de dar o compromisso do log")
	}
	// E nem o texto nem o sal estão no WAL.
	bruto, err := os.ReadFile(wal)
	if err != nil {
		t.Fatal(err)
	}
	for _, segredo := range []string{goal, "ana.silva", m[2]} {
		if bytes.Contains(bruto, []byte(segredo)) {
			t.Fatalf("o WAL nao pode conter %q", segredo)
		}
	}
}

// aos477No é o nó falso do caminho da fila: oferece um pedido com a referência e o compromisso, e
// guarda o corpo de cada POST /runs.
type aos477No struct {
	mu        sync.Mutex
	oferta    *pedidoReclamado
	corpos    map[string]map[string]any
	desfechos []pedidoDeDesfechoVisto
}

func (f *aos477No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	f.corpos = map[string]map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plans/claim", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.oferta == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(f.oferta)
		f.oferta = nil
	})
	mux.HandleFunc("POST /plans/outcome", func(w http.ResponseWriter, r *http.Request) {
		var d pedidoDeDesfechoVisto
		_ = json.NewDecoder(r.Body).Decode(&d)
		f.mu.Lock()
		f.desfechos = append(f.desfechos, d)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		var corpo map[string]any
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		id, _ := corpo["run_id"].(string)
		f.mu.Lock()
		f.corpos[id] = corpo
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		_, existe := f.corpos[id]
		f.mu.Unlock()
		if !existe {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "completed", "terminated": true, "final_text": "feito"})
	})
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(aos441CatalogoDoSnapshotComPerigo))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *aos477No) ambiente(t *testing.T) (env []string, dir, wal, snap string) {
	t.Helper()
	srv := f.servidor(t)
	dir = t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	snap = filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	return []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}, dir, filepath.Join(dir, "consume.wal"), snap
}

// aos477PlanoComPonto é o plano das duas folhas com um `node_id` com `.` (que a gramática admite):
// o run filho leva-o ESCAPADO e o vínculo declara-o CRU — é o que o nó confere (M-2 da revisão).
var aos477PlanoComPonto = strings.Replace(planoFixtureDuasFolhasComSnapshotAOS408, `"node_id":"recolha"`, `"node_id":"recolha.v1"`, 1)

func (f *aos477No) consumir(t *testing.T, env []string, bin, dir, wal, snap string) resultado {
	t.Helper()
	fix := filepath.Join(dir, "fixture.json")
	escrever(t, fix, aos477PlanoComPonto)
	return correrComEnv(t, env, bin, "consume", "--wal", wal, "--snapshot", snap, "--decompose-fixture", fix,
		"--poll-interval", "20ms", "--max", "1")
}

func TestAOS477ConsumeCitaOPedidoEOsFilhosDeclaramAOrigem(t *testing.T) {
	bin := construir(t)
	salHex := hex.EncodeToString(salDoVector())
	f := &aos477No{oferta: &pedidoReclamado{RunID: "plan-477", Objective: vectorDoContratoObjectivo, Geracao: 1,
		RequestStream: "fila-opaca/pedidos", RequestSeq: 44,
		ObjectiveCommitment: vectorDoContratoCompromisso, ObjectiveSalt: salHex}}
	env, dir, wal, snap := f.ambiente(t)
	r := f.consumir(t, env, bin, dir, wal, snap)
	if r.code != exitOK {
		t.Fatalf("consume: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout+r.stderr, salHex) {
		t.Fatal("o sal da fila NAO pode sair no stdout do drenador: esta selado sob o titular no no")
	}

	// (AC2) O registo do plano cita o pedido e leva o compromisso do pedido.
	p, ok := propostaDoWAL(t, wal, "plan-477-plan")
	if !ok {
		t.Fatalf("sem plan.proposed\n%s", r.stdout)
	}
	if p.Request == nil || *p.Request != (plannerevents.PlanRequestRef{Stream: "fila-opaca/pedidos", Seq: 44, RunID: "plan-477"}) {
		t.Fatalf("o plan.proposed tem de citar o pedido (stream, seq, run_id), veio %+v", p.Request)
	}
	if p.ObjectiveCommitment != vectorDoContratoCompromisso {
		t.Fatalf("o compromisso do plano tem de ser o do pedido: %q", p.ObjectiveCommitment)
	}

	// (AC3) Cada run filho leva o plano e o nó num campo.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.corpos) != 2 {
		t.Fatalf("esperava os dois nós submetidos, veio %d", len(f.corpos))
	}
	nos := []string{}
	for id, corpo := range f.corpos {
		v, _ := corpo["plan_request"].(map[string]any)
		if v == nil || v["plan_id"] != "plan-477-plan" || v["run_id"] != "plan-477" {
			t.Fatalf("o run filho %s tem de declarar o plano no vinculo: %+v", id, v)
		}
		no, _ := v["node_id"].(string)
		if childRunID("plan-477", no) != id {
			t.Fatalf("o node_id declarado (%q) tem de ser o do run %s", no, id)
		}
		nos = append(nos, no)
	}
	slices.Sort(nos)
	if _, ok := f.corpos["plan-477~recolha+2ev1"]; !ok {
		t.Fatalf("o run filho do no `recolha.v1` tem de levar o node_id ESCAPADO no id: %v", f.corpos)
	}
	if !slices.Equal(nos, []string{"analise", "recolha.v1"}) {
		t.Fatalf("nós declarados: %v", nos)
	}
}

func TestAOS477ConsumeRecusaUmObjectivoQueNaoEODoPedido(t *testing.T) {
	bin := construir(t)
	f := &aos477No{oferta: &pedidoReclamado{RunID: "plan-477-trocado", Objective: "OUTRO objectivo", Geracao: 1,
		RequestStream: "fila-opaca/pedidos", RequestSeq: 45,
		ObjectiveCommitment: vectorDoContratoCompromisso, ObjectiveSalt: hex.EncodeToString(salDoVector())}}
	env, dir, wal, snap := f.ambiente(t)
	r := f.consumir(t, env, bin, dir, wal, snap)
	if strings.Contains(r.stdout, "posse:") || strings.Contains(r.stdout, "decomposto:") {
		t.Fatalf("um objectivo que nao e o do pedido nao pode tomar posse nem decompor:\n%s", r.stdout)
	}
	if _, ok := propostaDoWAL(t, wal, "plan-477-trocado-plan"); ok {
		t.Fatal("nenhum plan.proposed pode ficar no log")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.corpos) != 0 || len(f.desfechos) != 1 || f.desfechos[0].Classe != "transitorio" {
		t.Fatalf("esperava nenhum run e um desfecho transitorio, veio corpos=%d desfechos=%+v", len(f.corpos), f.desfechos)
	}
}

// O executor só declara a origem quando a reclamação veio de um nó que entregou o pedido.
func TestAOS477ExecutorDeclaraOrigemSoComONoQueEntregouOPedido(t *testing.T) {
	cli := &runnerQueGuarda{}
	rec := planRecorderDeTeste(t, "plano", "plano-plan")
	e := &executorDeNos{cli: cli, rec: rec, runID: "plano", nos: nosDeTeste("n1"), geracaoDoPedido: 1, emVoo: map[string]struct{}{}}
	if err := e.submeter(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
	if v := cli.ultimo.PlanRequest; v == nil || v.PlanID != "" || v.NodeID != "" {
		t.Fatalf("sem declararOrigem o vinculo e o do AOS-439 (um no anterior recusava os campos): %+v", v)
	}
	e.declararOrigem = true
	if err := e.submeter(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
	if v := cli.ultimo.PlanRequest; v == nil || v.PlanID != "plano-plan" || v.NodeID != "n1" {
		t.Fatalf("com declararOrigem o vinculo leva o plano e o no: %+v", v)
	}
}

// planRecorderDeTeste é um emissor do plano sobre um store em memória, com a posse do run.
func planRecorderDeTeste(t *testing.T, runID, planID string) *runlifecycle.PlanRecorder {
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
	return rec
}

// Os MESMOS vectores do nó (`packages/cmd/aos/aos477_origem_test.go`,
// `aos477VectoresDoRunFilho`): o nó confere o `node_id` declarado contra o id do run filho com a sua
// cópia do escape; se as duas cópias divergirem, um dos dois testes fica vermelho.
func TestAOS477ChildRunIDTemOsVectoresDoNo(t *testing.T) {
	for no, quer := range map[string]string{
		"n1":           "p~n1",
		"recolha.v1:a": "p~recolha+2ev1:a",
		"a.b":          "p~a+2eb",
		"a_2eb":        "p~a_2eb",
		"a+b":          "p~a++b",
		"a~b":          "p~a+7eb",
	} {
		if got := childRunID("p", no); got != quer {
			t.Errorf("childRunID(p, %q) = %q, quer %q", no, got, quer)
		}
	}
}

// B-1 da revisão: um `serve --goal` repetido no mesmo run não imprime um sal que não verifica o
// log. O `plan.proposed` tem passo fixo; fica o da primeira, e é esse que a linha nomeia.
func TestAOS477ServeRepetidoNaoImprimeSalQueNaoVerificaOLog(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "orq.wal")
	serve := func() resultado {
		return correrComEnv(t, []string{"AOS_ORQ_NODE_URL=", "AOS_MODE="}, bin, "serve", "--wal", wal, "--run", "run-477-r",
			"--goal", "recolher e analisar dados", "--snapshot", snap, "--decompose-fixture", fix, "--worker", "p1", "--release")
	}
	primeiro := serve()
	m := reCompromissoImpresso.FindStringSubmatch(primeiro.stdout)
	if primeiro.code != exitOK || m == nil {
		t.Fatalf("primeiro serve: %d\n%s\n%s", primeiro.code, primeiro.stdout, primeiro.stderr)
	}
	segundo := serve()
	if segundo.code != exitOK {
		t.Fatalf("segundo serve: %d\n%s\n%s", segundo.code, segundo.stdout, segundo.stderr)
	}
	if strings.Contains(segundo.stdout, "sal=") {
		t.Fatalf("o segundo serve imprimiu um sal que nao verifica o log:\n%s", segundo.stdout)
	}
	if !strings.Contains(segundo.stdout, "ja tem proposta registada") || !strings.Contains(segundo.stdout, m[1]) {
		t.Fatalf("o segundo serve tem de nomear o compromisso da PRIMEIRA proposta (%s):\n%s", m[1], segundo.stdout)
	}
	if p, ok := propostaDoWAL(t, wal, "run-477-r-plan"); !ok || p.ObjectiveCommitment != m[1] {
		t.Fatalf("o log guarda o compromisso da primeira proposta: %+v", p)
	}
}
