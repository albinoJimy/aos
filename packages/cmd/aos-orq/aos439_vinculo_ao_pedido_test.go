package main

// aos439_vinculo_ao_pedido_test.go — O LADO DO `aos-orq` DO VÍNCULO AO PEDIDO (AOS-439).
//
// O `consume` passa ao `serve` a geração que reclamou; o executor leva `plan_request` em cada run
// filho; e a recusa do nó por submissor fora do mandato fecha o pedido como TERMINAL — atravessando
// o despachador do plano, que até aqui achatava a causa com `%v`.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	identity "github.com/aos-ref/platform/identity"
)

// noQueRecusaOMandato é um nó falso cujo POST /runs responde como o nó real responde a um
// submissor fora dos requesters (403 + code), e regista o corpo recebido.
func noQueRecusaOMandato(t *testing.T, corpoDaRecusa string) (*httptest.Server, *map[string]any) {
	t.Helper()
	recebido := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recebido)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(corpoDaRecusa))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &recebido
}

func TestAOS439SubmitLevaOVinculoEReconheceARecusaDoMandato(t *testing.T) {
	srv, recebido := noQueRecusaOMandato(t, `{"error":"submissor fora do mandato","code":"E_MANDATE_REQUESTER"}`)
	c := aos413ClienteDoAmbiente(t, srv.URL)
	err := c.Submit(context.Background(), pedidoDeRun{RunID: "plano~n1", Objective: "o",
		PlanRequest: &vinculoAoPedido{RunID: "plano", Geracao: 3}})
	if !errors.Is(err, errRequerenteForaDoMandato) {
		t.Fatalf("a recusa do mandato tinha de dar errRequerenteForaDoMandato, veio %v", err)
	}
	pr, _ := (*recebido)["plan_request"].(map[string]any)
	if pr["run_id"] != "plano" || pr["generation"] != float64(3) {
		t.Fatalf("o corpo tinha de levar plan_request {plano, 3}, levou %v", (*recebido)["plan_request"])
	}
	if _, tem := (*recebido)["requested_by"]; tem {
		t.Fatal("o aos-orq NUNCA afirma o submissor: quem o deriva e o no")
	}
}

// Sem o vínculo, ou com a 403 uniforme, a recusa NÃO é a do mandato — seria fechar como terminal
// uma credencial expirada, que é transitória.
func TestAOS439OutrasRecusasNaoSaoADoMandato(t *testing.T) {
	uniforme, _ := noQueRecusaOMandato(t, `{"error":"nao autorizado"}`)
	c := aos413ClienteDoAmbiente(t, uniforme.URL)
	err := c.Submit(context.Background(), pedidoDeRun{RunID: "p~n1", PlanRequest: &vinculoAoPedido{RunID: "p", Geracao: 1}})
	if err == nil || errors.Is(err, errRequerenteForaDoMandato) {
		t.Fatalf("a 403 uniforme nao e a recusa do mandato: %v", err)
	}
	comCodigo, _ := noQueRecusaOMandato(t, `{"error":"x","code":"E_MANDATE_REQUESTER"}`)
	c = aos413ClienteDoAmbiente(t, comCodigo.URL)
	if err := c.Submit(context.Background(), pedidoDeRun{RunID: "p~n1"}); err == nil || errors.Is(err, errRequerenteForaDoMandato) {
		t.Fatalf("sem vinculo enviado, o nó nao pode ter emitido esta recusa: %v", err)
	}
}

// A CLASSIFICAÇÃO atravessa o despachador do plano: é assim que o erro chega ao `serve`.
func TestAOS439RecusaDoMandatoETerminal(t *testing.T) {
	doSink := fmt.Errorf("execução do nó %q: %w", "n1", fmt.Errorf("%w: %s", errRequerenteForaDoMandato, "p~n1"))
	doDespacho := fmt.Errorf("despacho governado: %w", fmt.Errorf("despacho (passagem 1): %w",
		fmt.Errorf("%w: nó %q: %w", plandispatch.ErrDispatchSink, "n1", doSink)))
	codigo, classe, tipo := desfechoDoServe(doDespacho)
	if codigo != exitRequerenteForaDoMandato || classe != "terminal" || tipo != "requerente_fora_do_mandato" {
		t.Fatalf("a recusa do mandato tem de fechar o pedido: (%d, %q, %q)", codigo, classe, tipo)
	}
}

// A saída 11 LARGA a posse, como as outras recusas terminais: o pedido fechou, e reter o lease
// até ao TTL bloqueava quem viesse a seguir sobre o mesmo run.
func TestAOS439RecusaDoMandatoLargaAPosse(t *testing.T) {
	recusa := fmt.Errorf("despacho: %w", fmt.Errorf("%w: p~n1", errRequerenteForaDoMandato))
	if !largaAPosse(recusa) {
		t.Fatal("a recusa do mandato (saida 11) tem de largar a posse")
	}
	// Controlo: o genérico (transitório, retoma-se) NÃO larga, e as recusas de sempre continuam a largar.
	if largaAPosse(errors.New("rede em baixo")) || largaAPosse(nil) {
		t.Fatal("um erro generico nao larga a posse")
	}
	if !largaAPosse(fmt.Errorf("x: %w", errPlanoPendente)) || !largaAPosse(fmt.Errorf("x: %w", errNosEmVoo)) {
		t.Fatal("as recusas que ja largavam tem de continuar a largar")
	}
}

// O despachador do plano propaga a causa do sink (era `%v`). Medido com o despachador real.
func TestAOS439DespachadorPropagaACausaDoSink(t *testing.T) {
	err := fmt.Errorf("%w: nó %q: %w", plandispatch.ErrDispatchSink, "n1", errRequerenteForaDoMandato)
	if !errors.Is(err, errRequerenteForaDoMandato) || !errors.Is(err, plandispatch.ErrDispatchSink) {
		t.Fatal("a forma do erro do despachador tem de preservar as duas causas")
	}
}

// O `consume` passa a geração ao `serve`; sem geração (serve manual) não há flag.
func TestAOS439ConsumePassaAGeracaoAoServe(t *testing.T) {
	args := argsDoServe("/s.json", pedidoReclamado{RunID: "plano", Objective: "o", Geracao: 4},
		substrato{wal: "/w"}, time.Minute, time.Second, "", origemDoPlano{})
	i := slices.Index(args, "--plan-request-generation")
	if i < 0 || i+1 >= len(args) || args[i+1] != "4" {
		t.Fatalf("o consume tem de passar --plan-request-generation 4; args=%v", args)
	}
	semGeracao := argsDoServe("/s.json", pedidoReclamado{RunID: "plano", Objective: "o"},
		substrato{wal: "/w"}, time.Minute, time.Second, "", origemDoPlano{})
	if slices.Contains(semGeracao, "--plan-request-generation") {
		t.Fatalf("sem geracao nao ha vinculo: %v", semGeracao)
	}
}

// mandatoV2DeTeste escreve um mandato.json v2 que nomeia só `requesters`, e devolve o caminho.
func mandatoV2DeTeste(t *testing.T, dir string, requesters ...string) string {
	t.Helper()
	humano := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	agora := time.Now().UTC()
	sm, err := identity.SignMandate(humano, identity.Mandate{
		ID: "m-439", Human: "alice", Board: "board-eu", AgentID: "agent:aos-orq", AgentClass: "planner",
		PolicyRef: "policy://planner", Scope: []string{"run:submit"}, Issuer: "iss:aos-issuer-auto",
		MaxTTLSeconds: 2700, NotBefore: agora.Unix(), NotAfter: agora.Add(24 * time.Hour).Unix(),
		Requesters: requesters,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	caminho := filepath.Join(dir, "mandato.json")
	escrever(t, caminho, string(b))
	return caminho
}

func TestAOS439RequerenteForaDoMandatoDecisao(t *testing.T) {
	m, err := lerMandatoDoDrenador(mandatoV2DeTeste(t, t.TempDir(), "sub-alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !requerenteForaDoMandato(&m, pedidoReclamado{RequestedBy: "sub-mallory"}) {
		t.Fatal("um submissor fora dos requesters fecha antes de planear")
	}
	if requerenteForaDoMandato(&m, pedidoReclamado{RequestedBy: "sub-alice"}) {
		t.Fatal("um submissor nomeado segue para o serve")
	}
	// 2.ª ronda da revisão: sob um v2, um pedido SEM submissor fecha — o nó recusá-lo-ia com a 403
	// uniforme (transitória) e o pedido voltaria à fila para sempre.
	if !requerenteForaDoMandato(&m, pedidoReclamado{}) {
		t.Fatal("sob um mandato v2, um pedido sem requested_by fecha antes de planear")
	}
	if requerenteForaDoMandato(nil, pedidoReclamado{RequestedBy: "x"}) || requerenteForaDoMandato(nil, pedidoReclamado{}) {
		t.Fatal("sem mandato configurado, quem decide e o no")
	}
	v1 := m
	v1.Requesters = nil
	if requerenteForaDoMandato(&v1, pedidoReclamado{RequestedBy: "sub-mallory"}) || requerenteForaDoMandato(&v1, pedidoReclamado{}) {
		t.Fatal("um mandato v1 nao compara (a janela e do no)")
	}
	if _, err := lerMandatoDoDrenador(filepath.Join(t.TempDir(), "nao-existe.json")); err == nil {
		t.Fatal("um mandato ausente tem de ser erro (fail-closed)")
	}
}

// nhiComMandato escreve um NHI com a FORMA de um JWS cujo payload embebe um mandato com o id dado
// — não é assinado: o cruzamento do `consume` lê-o sem verificar, e é isso que se testa.
func nhiComMandato(t *testing.T, caminho, id string) {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	escrever(t, caminho, enc([]byte(`{"alg":"EdDSA","typ":"NHI"}`))+"."+
		enc([]byte(`{"iss":"iss:aos-issuer-auto","mandate":{"mandate":{"id":"`+id+`"},"sig":"x"}}`))+"."+enc([]byte("sig")))
}

// O CRUZAMENTO DO MANDATO COM O NHI EM USO (2.ª ronda da revisão): só com os dois ids legíveis, e só
// aborta quando divergem.
func TestAOS439CruzarMandatoComONHI(t *testing.T) {
	dir := t.TempDir()
	m, err := lerMandatoDoDrenador(mandatoV2DeTeste(t, dir, "sub-alice"))
	if err != nil {
		t.Fatal(err)
	}
	nhi := filepath.Join(dir, "nhi.jwt")
	nhiComMandato(t, nhi, "m-antigo")
	if err := cruzarMandatoComONHI(m, nhi); err == nil || !strings.Contains(err.Error(), "m-antigo") {
		t.Fatalf("um NHI de outro mandato tinha de abortar, veio %v", err)
	}
	nhiComMandato(t, nhi, m.ID)
	if err := cruzarMandatoComONHI(m, nhi); err != nil {
		t.Fatalf("o NHI do mesmo mandato passa: %v", err)
	}
	for _, sem := range []string{"nhi-do-operador", "a.b.c", ""} {
		escrever(t, nhi, sem)
		if err := cruzarMandatoComONHI(m, nhi); err != nil {
			t.Errorf("um NHI sem mandato legivel (%q) nao cruza: %v", sem, err)
		}
	}
	if err := cruzarMandatoComONHI(m, filepath.Join(dir, "nao-existe")); err != nil {
		t.Errorf("um NHI ilegivel nao cruza (o Submit e que falha): %v", err)
	}
}

// O `consume` com o binário real: um pedido de um submissor que o mandato não nomeia fecha com 11
// SEM decompor e SEM POST /runs; o nomeado corre; um mandato ilegível não reclama nada.
// Vermelho antes da guarda: o `serve` decompunha (o modelo corria) e só o nó recusava.
func TestAOS439ConsumeNaoPlaneiaPorQuemOMandatoNaoNomeia(t *testing.T) {
	bin := construir(t)
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	mandato := mandatoV2DeTeste(t, a.dir, "sub-alice")

	no.oferecer(pedidoReclamado{RunID: "plan-439-mallory", Objective: "o", Geracao: 1, RequestedBy: "sub-mallory"})
	r := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--mandate", mandato)
	if strings.Contains(r.stdout, "decomposto:") {
		t.Fatalf("o planeador correu por um submissor que o mandato nao nomeia:\n%s", r.stdout)
	}
	if len(no.submetidos()) != 0 {
		t.Fatalf("houve POST /runs por um submissor fora do mandato: %v", no.submetidos())
	}
	exigirDesfechos(t, no.vistos(), pedidoDeDesfechoVisto{RunID: "plan-439-mallory", Geracao: 1, Classe: "terminal", Codigo: exitRequerenteForaDoMandato})

	// 2.ª ronda: sob um v2, um pedido SEM requested_by fecha com 11, sem planear — sem isto o nó
	// recusava-o com a 403 uniforme (transitória) e ele voltava à fila para sempre.
	no.oferecer(pedidoReclamado{RunID: "plan-439-anonimo", Objective: "o", Geracao: 1})
	r = a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--mandate", mandato)
	if strings.Contains(r.stdout, "decomposto:") || len(no.submetidos()) != 0 {
		t.Fatalf("um pedido sem submissor foi planeado sob um mandato v2:\n%s", r.stdout)
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: "plan-439-mallory", Geracao: 1, Classe: "terminal", Codigo: exitRequerenteForaDoMandato},
		pedidoDeDesfechoVisto{RunID: "plan-439-anonimo", Geracao: 1, Classe: "terminal", Codigo: exitRequerenteForaDoMandato})

	// 2.ª ronda: o NHI em uso foi cunhado sob OUTRO mandato (a janela entre re-assinar e o timer
	// trocar o NHI) — aborta ANTES de reclamar, e o pedido fica na fila.
	cred := filepath.Join(a.dir, "nhi.jwt")
	nhiComMandato(t, cred, "m-antigo")
	no.oferecer(pedidoReclamado{RunID: "plan-439-cruzado", Objective: "o", Geracao: 1, RequestedBy: "sub-mallory"})
	fixC := filepath.Join(a.dir, "fixture-cruzado.json")
	escrever(t, fixC, planoFixtureDuasFolhasComSnapshotAOS408)
	cruzado := correrComEnv(t, a.env, a.bin, "consume", "--wal", a.wal, "--snapshot", a.snap,
		"--decompose-fixture", fixC, "--max", "1", "--mandate", mandato)
	if cruzado.code == exitOK || !strings.Contains(cruzado.stderr+cruzado.stdout, "m-antigo") {
		t.Fatalf("um NHI de outro mandato tinha de abortar a drenagem, nomeando-o:\n%s\n%s", cruzado.stdout, cruzado.stderr)
	}
	no.mu.Lock()
	ficou := len(no.ofertas)
	no.ofertas = nil
	no.mu.Unlock()
	if ficou != 1 || len(no.vistos()) != 2 {
		t.Fatalf("com o NHI cruzado nao se reclama nada (ofertas=%d, desfechos=%d)", ficou, len(no.vistos()))
	}
	escrever(t, cred, "nhi-do-operador")

	// O mandato ilegível aborta ANTES de reclamar: o pedido continua na fila do nó falso.
	escrever(t, mandato, "{lixo")
	no.oferecer(pedidoReclamado{RunID: "plan-439-alice", Objective: "o", Geracao: 1, RequestedBy: "sub-alice"})
	fix := filepath.Join(a.dir, "fixture-ilegivel.json")
	escrever(t, fix, planoFixtureDuasFolhasComSnapshotAOS408)
	ilegivel := correrComEnv(t, a.env, a.bin, "consume", "--wal", a.wal, "--snapshot", a.snap,
		"--decompose-fixture", fix, "--max", "1", "--mandate", mandato)
	if ilegivel.code == exitOK {
		t.Fatalf("um mandato ilegivel tinha de abortar a drenagem:\n%s\n%s", ilegivel.stdout, ilegivel.stderr)
	}
	no.mu.Lock()
	restam := len(no.ofertas)
	no.mu.Unlock()
	if restam != 1 {
		t.Fatalf("com o mandato ilegivel nao se reclama nada (restam %d ofertas, esperava 1)", restam)
	}
}

// runnerQueGuarda é o nó visto pelo executor: guarda o último pedido de run.
type runnerQueGuarda struct{ ultimo pedidoDeRun }

func (r *runnerQueGuarda) Submit(_ context.Context, p pedidoDeRun) error {
	r.ultimo = p
	return nil
}

func (r *runnerQueGuarda) Status(context.Context, string) (estadoDoRun, bool, error) {
	return estadoDoRun{}, false, nil
}

func nosDeTeste(ids ...string) map[string]plan.Node {
	out := make(map[string]plan.Node, len(ids))
	for _, id := range ids {
		out[id] = plan.Node{NodeID: id, Objective: "trabalho de " + id}
	}
	return out
}

// O executor leva o vínculo em cada run filho — e só quando o `serve` trabalha um pedido.
func TestAOS439ExecutorLevaOVinculo(t *testing.T) {
	cli := &runnerQueGuarda{}
	e := &executorDeNos{cli: cli, runID: "plano", nos: nosDeTeste("n1"), geracaoDoPedido: 2, emVoo: map[string]struct{}{}}
	if err := e.submeter(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
	if cli.ultimo.PlanRequest == nil || cli.ultimo.PlanRequest.RunID != "plano" || cli.ultimo.PlanRequest.Geracao != 2 {
		t.Fatalf("o run filho tinha de levar plan_request {plano, 2}: %+v", cli.ultimo.PlanRequest)
	}
	e.geracaoDoPedido = 0
	if err := e.submeter(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
	if cli.ultimo.PlanRequest != nil {
		t.Fatalf("um serve manual nao tem pedido: %+v", cli.ultimo.PlanRequest)
	}
}
