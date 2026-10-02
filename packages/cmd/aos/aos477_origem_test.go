package main

// aos477_origem_test.go — DO REGISTO SE CHEGA AO PEDIDO POR CAMPOS (AOS-477), do lado do nó.
//
//  1. O compromisso tem o vector do contrato — o mesmo do `aos-orq` e do `plannerevents`.
//  2. O `planrequest.submitted` guarda o compromisso e sela o sal com o objectivo; sem titular,
//     os dois ficam em claro juntos. O apagamento do titular torna o sal ilegível.
//  3. A reclamação entrega a referência ao pedido, o compromisso e o sal — e o sal recalcula o
//     compromisso sobre o objectivo entregue.
//  4. O run filho com o vínculo verificado declara a sua origem num `run.plan_origin`; um
//     `plan_id`/`node_id` malformado é recusado com a mesma 403; sem vínculo não há origem.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// O vector do contrato: sal 00..1f, objectivo «recolher e analisar dados». O MESMO valor está em
// `packages/cmd/aos-orq/aos477_origem_no_log_test.go` e em
// `packages/control-plane/orchestrator/plannerevents/aos477_proposta_test.go`.
const (
	aos477VectorObjectivo   = "recolher e analisar dados"
	aos477VectorCompromisso = "hmac-sha256:a95479ee76699c6bd97e848784f11a25ae02d8a5f7175f376e02b071ea8e1b2f"
)

func aos477SalDoVector() []byte {
	sal := make([]byte, 32)
	for i := range sal {
		sal[i] = byte(i)
	}
	return sal
}

func TestAOS477CompromissoTemOVectorDoContrato(t *testing.T) {
	if c := compromissoDoObjetivo(aos477SalDoVector(), aos477VectorObjectivo); c != aos477VectorCompromisso {
		t.Fatalf("o compromisso do no divergiu do contrato partilhado com o aos-orq:\n quer %s\n veio %s", aos477VectorCompromisso, c)
	}
}

// pedidosGravados devolve os payloads e os `seq` dos `planrequest.submitted`.
func pedidosGravados(t *testing.T, node *Node) ([]planRequestPayload, []uint64) {
	t.Helper()
	evs, err := node.EventStore.Read(context.Background(), planRequestStream, 1)
	if err != nil {
		t.Fatalf("ler a fila: %v", err)
	}
	var ps []planRequestPayload
	var seqs []uint64
	for _, ev := range evs {
		if ev.Type != EventTypePlanRequestSubmitted {
			continue
		}
		ps = append(ps, planRequestPayloadDeTeste(t, ev.Payload))
		seqs = append(seqs, ev.Seq)
	}
	return ps, seqs
}

// Com titular: o compromisso em claro, o sal SELADO, nenhum dos dois textos no facto — e o
// apagamento do titular torna o sal (e com ele o compromisso) inverificável.
func TestAOS477IngressoGuardaOCompromissoESelaOSal(t *testing.T) {
	f := noAOS439(t)
	const objectivo = "auditar o pipeline de faturas"
	for _, run := range []string{"plano-477-a", "plano-477-b"} {
		if r := postReq(f.h, "/plans", map[string]any{"run_id": run, "objective": objectivo}, aos439Headers(aos439Alice)); r.Code != http.StatusCreated {
			t.Fatalf("POST /plans: %d %s", r.Code, r.Body.String())
		}
	}
	ps, _ := pedidosGravados(t, f.node)
	if len(ps) != 2 {
		t.Fatalf("esperava 2 pedidos, veio %d", len(ps))
	}
	for _, p := range ps {
		bruto, _ := json.Marshal(p)
		if p.CompromissoDoObjetivo == "" || len(p.SalSelado) == 0 || p.Sal != "" {
			t.Fatalf("com titular: compromisso em claro, sal SELADO e nenhum sal em claro — veio %s", bruto)
		}
		if strings.Contains(string(bruto), objectivo) {
			t.Fatal("o objectivo nao pode aparecer em claro no facto")
		}
		sal, err := abrirSal(f.node, p)
		if err != nil {
			t.Fatalf("o no abre o sal que selou: %v", err)
		}
		if strings.Contains(string(bruto), sal) {
			t.Fatal("o sal em claro nao pode aparecer no facto")
		}
		cru, _ := hex.DecodeString(sal)
		if compromissoDoObjetivo(cru, objectivo) != p.CompromissoDoObjetivo {
			t.Fatal("HMAC(sal, objectivo) tem de dar o compromisso gravado")
		}
	}
	if ps[0].CompromissoDoObjetivo == ps[1].CompromissoDoObjetivo {
		t.Fatal("o mesmo objectivo em dois pedidos nao pode dar o mesmo compromisso (sal por pedido)")
	}
	if ps[0].Versao != "1.2" {
		t.Fatalf("o payload passou a 1.2, veio %q", ps[0].Versao)
	}

	// O APAGAMENTO alcança o sal: o compromisso fica no log, mas deixa de ser verificável.
	if _, err := f.node.DSAR.Receive(context.Background(), dsar.Request{RequestID: "req-477", SubjectID: aos439Alice}); err != nil {
		t.Fatalf("DSAR erase: %v", err)
	}
	if _, err := abrirSal(f.node, ps[0]); !errors.Is(err, errObjetivoIlegivel) {
		t.Fatalf("depois do apagamento o sal tinha de ficar ilegivel, veio %v", err)
	}
}

// Sem titular: o objectivo já fica em claro; o sal fica em claro AO LADO dele (e só nesse caso).
func TestAOS477SemTitularOSalFicaEmClaroComOObjectivo(t *testing.T) {
	node := &Node{DSARVault: audit.NewInMemoryKeyVault(nil)}
	p, err := selarObjetivo(node, planRequestPayload{RunID: "r", Objective: "o", Sal: hex.EncodeToString(aos477SalDoVector())})
	if err != nil {
		t.Fatal(err)
	}
	if p.Objective != "o" || p.Sal == "" || len(p.SalSelado) != 0 {
		t.Fatalf("sem titular o sal acompanha o objectivo em claro: %+v", p)
	}
	com, err := selarObjetivo(node, planRequestPayload{RunID: "r", Objective: "o", Principal: "human:x", Sal: hex.EncodeToString(aos477SalDoVector())})
	if err != nil {
		t.Fatal(err)
	}
	if com.Sal != "" || len(com.SalSelado) == 0 || com.Objective != "" {
		t.Fatalf("com titular o sal sela-se com o objectivo e o claro sai: %+v", com)
	}
	if s, err := abrirSal(node, com); err != nil || s != hex.EncodeToString(aos477SalDoVector()) {
		t.Fatalf("o sal selado abre-se igual: %q %v", s, err)
	}
}

// A reclamação entrega a referência ao pedido, o compromisso e o sal com que o drenador o
// recalcula sobre o objectivo que recebe.
func TestAOS477ReclamacaoEntregaOPedidoEOCompromisso(t *testing.T) {
	f := noAOS439(t)
	const objectivo = "objectivo da alice"
	if r := postReq(f.h, "/plans", map[string]any{"run_id": "plano-477", "objective": objectivo}, aos439Headers(aos439Alice)); r.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d", r.Code)
	}
	r := postReq(f.h, "/plans/claim", nil, aos439Headers(aos439Drenador))
	if r.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", r.Code, r.Body.String())
	}
	var c respostaDeReclamo
	if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	ps, seqs := pedidosGravados(t, f.node)
	if c.RequestStream != planRequestStream || c.RequestSeq != seqs[0] || c.RequestSeq == 0 {
		t.Fatalf("a reclamacao tem de citar o pedido (%s#%d), veio %s#%d", planRequestStream, seqs[0], c.RequestStream, c.RequestSeq)
	}
	if c.ObjectiveCommitment != ps[0].CompromissoDoObjetivo {
		t.Fatalf("o compromisso entregue tem de ser o gravado")
	}
	sal, err := hex.DecodeString(c.ObjectiveSalt)
	if err != nil || compromissoDoObjetivo(sal, c.Objective) != c.ObjectiveCommitment {
		t.Fatalf("com o sal entregue, o drenador recalcula o compromisso sobre o objectivo entregue: %v", err)
	}
}

// eventosDoRun lê o stream de um run.
func eventosDoRun(t *testing.T, node *Node, runID string) []eventstore.Event {
	t.Helper()
	evs, err := node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("ler o run %s: %v", runID, err)
	}
	return evs
}

func origemDe(t *testing.T, evs []eventstore.Event) (origemDoRunFilho, bool) {
	t.Helper()
	for _, e := range evs {
		if e.Type == EventTypeRunPlanOrigin {
			var o origemDoRunFilho
			if err := json.Unmarshal(e.Payload, &o); err != nil {
				t.Fatal(err)
			}
			return o, true
		}
	}
	return origemDoRunFilho{}, false
}

func TestAOS477RunFilhoDeclaraAOrigemNumCampo(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-477-o"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	_, seqs := pedidosGravados(t, f.node)
	tok := f.tokenDoMandato(t, aos439Alice)

	// Malformados: a MESMA 403, e o run não existe.
	for nome, v := range map[string]vinculoAoPedido{
		"plan_id sem node_id": {RunID: plano, Geracao: ger, PlanID: plano + "-plan"},
		"node_id com espaco":  {RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: "n 1"},
		"plan_id reservado":   {RunID: plano, Geracao: ger, PlanID: streamsReservados + "x", NodeID: "n1"},
	} {
		if r := f.submeterFilho(t, plano+separadorDoRunFilho+"m", tok, &v, aos439Drenador); r.code != http.StatusForbidden {
			t.Fatalf("%s: tinha de ser a 403 uniforme, veio %d %s", nome, r.code, r.body)
		}
	}

	filho := plano + separadorDoRunFilho + "n1"
	v := vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: "n1"}
	if r := f.submeterFilho(t, filho, tok, &v, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("run filho: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)
	o, ok := origemDe(t, eventosDoRun(t, f.node, filho))
	if !ok {
		t.Fatal("o run filho com o vinculo verificado tem de declarar a origem (run.plan_origin)")
	}
	quer := origemDoRunFilho{Versao: versaoDaOrigem,
		Pedido: refDoPedidoDeOrigem{Stream: planRequestStream, Seq: seqs[0], RunID: plano, Geracao: ger},
		PlanID: plano + "-plan", NodeID: "n1"}
	if o != quer {
		t.Fatalf("origem declarada:\n quer %+v\n veio %+v", quer, o)
	}

	// Sem vínculo (um run directo do drenador) não há origem a declarar.
	manualTok, err := f.node.Authority.MintForHuman(context.Background(), tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatal(err)
	}
	if r := f.submeterFilho(t, "run-477-directo", manualTok.Compact, nil, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("run directo: %d %s", r.code, r.body)
	}
	f.esperar(t, "run-477-directo")
	if _, ok := origemDe(t, eventosDoRun(t, f.node, "run-477-directo")); ok {
		t.Fatal("um run sem vinculo nao pode declarar origem de plano nenhum")
	}
}
