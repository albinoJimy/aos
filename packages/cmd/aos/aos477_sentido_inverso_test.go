package main

// aos477_sentido_inverso_test.go — DE UM `tool.call.mediated` DE UM RUN FILHO AO PEDIDO DE PLANO,
// SÓ POR CAMPOS (AOS-477, critério 6).
//
// Os dois binários reais, cada um com o seu ficheiro:
//
//	alice ── POST /plans ──► nó `aos` (este processo, Event Store em disco)
//	`aos-orq consume` (PROCESSO REAL, compilado da árvore) ── reclama, planeia, submete o run filho
//	run filho no nó ── tool call mediada (`tool.call.mediated`)
//
// e depois o percurso que o E2E de 2026-10-01 fez à mão, agora sem partir um único id:
//
//	tool.call.mediated.stream_id
//	  └► run.plan_origin (mesmo stream) ── plan_request.{stream,seq}, plan_id, node_id
//	       ├► planrequest.submitted em {stream,seq} ── run_id, objective_commitment
//	       └► WAL do aos-orq, stream plan_id
//	            ├► plan.proposed ── request.{stream,seq} == o mesmo pedido; objective_commitment == o do pedido
//	            └► plan.materialized ── nodes[].node_id contém o node_id
//	  e, para fechar no OBJECTIVO, quem tem a custódia abre o sal e o objectivo do pedido e
//	  recalcula o compromisso.
//
// As leituras do lado do plano usam structs LOCAIS com os nomes dos campos do `aos.planner.v1`:
// o nó não importa o orquestrador (ADR-018), e é exactamente isso que prova que o contrato é de
// CAMPOS e não de tipos partilhados. Nenhuma linha deste teste corta um id por `~` ou por `-plan`.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// Formas LOCAIS dos factos do plano (`aos.planner.v1`), lidas por nome de campo.
type aos477Proposta struct {
	PlanID              string `json:"plan_id"`
	ObjectiveCommitment string `json:"objective_commitment"`
	Request             *struct {
		Stream string `json:"stream"`
		Seq    uint64 `json:"seq"`
		RunID  string `json:"run_id"`
	} `json:"request"`
}

type aos477Materializado struct {
	PlanID string `json:"plan_id"`
	Nodes  []struct {
		NodeID string `json:"node_id"`
	} `json:"nodes"`
}

// construirAosOrq compila o `aos-orq` da árvore para um ficheiro temporário. É um PROCESSO, não
// um import: o nó continua sem depender do orquestrador (boundary_orq_sch_test.go).
func construirAosOrq(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("toolchain `go` indisponivel: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "aos-orq-477")
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "aos-orq")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build do aos-orq: %v\n%s", err, out)
	}
	return bin
}

func TestAOS477DoToolCallAoPedidoSoPorCampos(t *testing.T) {
	ctx := context.Background()
	dig := digest.SHA256Digester{}.Digest(domain.KindTool, domain.Contract{Egress: domain.EgressNone})
	catalogo := []entradaDoCatalogo{{Name: "counter", Version: "1.0.0", Digest: dig, Egress: "none", Reversibility: "reversible", Mutation: "none"}}
	f := noAOS439Em(t, t.TempDir(), audit.SchemaV4, nil, nil, nil, WithToolCatalog(catalogo))
	bin := construirAosOrq(t)

	// (1) A alice pede o plano.
	const plano = "plano-477-e2e"
	const objectivo = "contar os documentos do arquivo"
	if r := postReq(f.h, "/plans", map[string]any{"run_id": plano, "objective": objectivo}, aos439Headers(aos439Alice)); r.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d %s", r.Code, r.Body.String())
	}

	// (2) O nó, visto pelo drenador. O proxy faz o papel do IdP: autentica o drenador na região do
	// board (o gate demo-grade das fixtures lê-o destes cabeçalhos).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range aos439Headers(aos439Drenador) {
			r.Header.Set(k, v)
		}
		f.h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escreverFicheiro(t, cred, f.tokenDoMandato(t, aos439Alice))
	snap := filepath.Join(dir, "snap.json")
	escreverFicheiro(t, snap, `{"hash":"sha256:snap-477","tools":[{"name":"counter","version":"1.0.0","digest":"`+dig+
		`","admissible":true,"sensitivity":"public","egress":"none","reversibility":"reversible","mutation":"none"}]}`)
	fix := filepath.Join(dir, "plano.json")
	escreverFicheiro(t, fix, `{"plan_version":"1.0.0","objective":"contar","budget_total":{"tokens":100,"cost_micro_usd":100},
 "planner_meta":{"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-477"},
 "nodes":[{"node_id":"n1","role":"worker","objective":"contar os documentos","depends_on":[],
  "tools":[{"name":"counter","version":"1.0.0","digest":"`+dig+`"}],"budget_estimate":{"tokens":10,"cost_micro_usd":10}}]}`)
	wal := filepath.Join(dir, "consume.wal")

	// (3) O `aos-orq consume`, processo real.
	cmd := exec.Command(bin, "consume", "--wal", wal, "--snapshot", snap, "--decompose-fixture", fix,
		"--poll-interval", "20ms", "--max", "1")
	cmd.Env = append(os.Environ(), "AOS_ORQ_NODE_URL="+srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE="+cred, "AOS_MODE=")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "nos_despachados") {
		t.Fatalf("aos-orq consume: %v\n%s", err, out)
	}

	// (4) O PONTO DE PARTIDA: um `tool.call.mediated` no Event Store do nó. Procura-se pelo TIPO,
	// em todos os streams — como um auditor que só tem o ficheiro.
	streams, err := f.node.EventStore.Streams()
	if err != nil {
		t.Fatal(err)
	}
	var mediado *eventstore.Event
	for _, s := range streams {
		evs, err := f.node.EventStore.Read(ctx, s, 1)
		if err != nil {
			t.Fatal(err)
		}
		for i := range evs {
			if evs[i].Type == "tool.call.mediated" {
				mediado = &evs[i]
				break
			}
		}
		if mediado != nil {
			break
		}
	}
	if mediado == nil {
		t.Fatalf("nenhum tool.call.mediated no no — o run filho nao chegou a chamar a tool\n%s", out)
	}

	// (5) stream_id do evento → `run.plan_origin` no MESMO stream.
	var origem origemDoRunFilho
	achou := false
	evs, err := f.node.EventStore.Read(ctx, mediado.StreamID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type == "run.plan_origin" {
			if err := json.Unmarshal(e.Payload, &origem); err != nil {
				t.Fatal(err)
			}
			achou = true
		}
	}
	if !achou {
		t.Fatalf("o run %q nao declara a sua origem", mediado.StreamID)
	}

	// (6) plan_request.{stream,seq} → o `planrequest.submitted`, pelo seq.
	fila, err := f.node.EventStore.Read(ctx, origem.Pedido.Stream, origem.Pedido.Seq)
	if err != nil || len(fila) == 0 || fila[0].Seq != origem.Pedido.Seq {
		t.Fatalf("o pedido %s#%d nao se le: %v", origem.Pedido.Stream, origem.Pedido.Seq, err)
	}
	if fila[0].Type != "planrequest.submitted" {
		t.Fatalf("em %s#%d esta %q, nao o pedido", origem.Pedido.Stream, origem.Pedido.Seq, fila[0].Type)
	}
	var pedido planRequestPayload
	if err := json.Unmarshal(fila[0].Payload, &pedido); err != nil {
		t.Fatal(err)
	}
	if pedido.RunID != origem.Pedido.RunID || pedido.Principal != aos439Alice || pedido.CompromissoDoObjetivo == "" {
		t.Fatalf("o pedido citado nao e o do run: %+v / %+v", pedido, origem.Pedido)
	}

	// (7) O LADO DO PLANO, noutro ficheiro: o stream `plan_id` (campo) do WAL do `aos-orq`.
	orq, err := eventstore.OpenReadOnly(wal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orq.Close() }()
	doPlano, err := orq.Read(ctx, origem.PlanID, 1)
	if err != nil {
		t.Fatalf("o plano %q declarado pelo run nao existe no WAL do aos-orq: %v", origem.PlanID, err)
	}
	var proposta *aos477Proposta
	noNoPlano := false
	for _, e := range doPlano {
		switch e.Type {
		case "plan.proposed":
			var p aos477Proposta
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			proposta = &p
		case "plan.materialized":
			var m aos477Materializado
			if err := json.Unmarshal(e.Payload, &m); err != nil {
				t.Fatal(err)
			}
			for _, n := range m.Nodes {
				if n.NodeID == origem.NodeID {
					noNoPlano = true
				}
			}
		}
	}
	if proposta == nil || proposta.Request == nil {
		t.Fatalf("o plan.proposed tem de citar o pedido: %+v", proposta)
	}
	if proposta.Request.Stream != origem.Pedido.Stream || proposta.Request.Seq != origem.Pedido.Seq || proposta.Request.RunID != pedido.RunID {
		t.Fatalf("o plano cita %+v e o run filho cita %+v — nao e o mesmo pedido", *proposta.Request, origem.Pedido)
	}
	if proposta.ObjectiveCommitment != pedido.CompromissoDoObjetivo {
		t.Fatalf("o compromisso do plano (%s) nao e o do pedido (%s)", proposta.ObjectiveCommitment, pedido.CompromissoDoObjetivo)
	}
	if !noNoPlano {
		t.Fatalf("o node_id %q declarado pelo run nao e um no do plano materializado", origem.NodeID)
	}

	// (8) E FECHA NO OBJECTIVO — para quem tem a custódia do titular (o nó): abre o objectivo e o
	// sal do pedido e recalcula o compromisso que o plano gravou.
	claro, err := abrirObjetivo(f.node, pedido)
	if err != nil {
		t.Fatal(err)
	}
	salHex, err := abrirSal(f.node, pedido)
	if err != nil {
		t.Fatal(err)
	}
	sal, _ := hex.DecodeString(salHex)
	if claro != objectivo || compromissoDoObjetivo(sal, claro) != proposta.ObjectiveCommitment {
		t.Fatalf("o compromisso do plano nao se verifica com o objectivo do pedido")
	}
	t.Logf("percurso: tool.call.mediated %s#%d -> run.plan_origin -> %s#%d (planrequest.submitted, run_id=%s) <- plan.proposed em %s (no %s), compromisso %s",
		mediado.StreamID, mediado.Seq, origem.Pedido.Stream, origem.Pedido.Seq, pedido.RunID, origem.PlanID, origem.NodeID, proposta.ObjectiveCommitment)
	// O texto e o sal ficaram FORA do WAL do plano.
	bruto, err := os.ReadFile(wal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bruto), objectivo) || strings.Contains(string(bruto), salHex) {
		t.Fatal("o objectivo ou o sal entraram no WAL do aos-orq")
	}
}

func escreverFicheiro(t *testing.T, caminho, conteudo string) {
	t.Helper()
	if err := os.WriteFile(caminho, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
}
