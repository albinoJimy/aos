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
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

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

// segundaGeracao fecha a geração 1 com um desfecho transitório e reclama de novo: geração 2.
func (f *aos439Fixture) segundaGeracao(t *testing.T, plano string) int {
	t.Helper()
	if r := postReq(f.h, "/plans/outcome", map[string]any{"run_id": plano, "generation": 1, "classe": "transitorio", "codigo_saida": 1},
		aos439Headers(aos439Drenador)); r.Code/100 != 2 {
		t.Fatalf("desfecho da geracao 1: %d %s", r.Code, r.Body.String())
	}
	r := postReq(f.h, "/plans/claim", nil, aos439Headers(aos439Drenador))
	var c respostaDeReclamo
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &c) != nil || c.RunID != plano {
		t.Fatalf("segunda reclamacao: %d %s", r.Code, r.Body.String())
	}
	return c.Geracao
}

func TestAOS477RunFilhoDeclaraAOrigemNumCampo(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-477-o"
	if g := f.pedirEReclamar(t, plano, aos439Alice); g != 1 {
		t.Fatalf("primeira geracao: %d", g)
	}
	// A GERAÇÃO QUE O VÍNCULO NOMEIA é a que fica — testa-se com a 2, e não com a 1 de sempre.
	ger := f.segundaGeracao(t, plano)
	if ger != 2 {
		t.Fatalf("esperava a geracao 2, veio %d", ger)
	}
	tok := f.tokenDoMandato(t, aos439Alice)

	// O NÓ DO PLANO com `.` (que a gramática admite): o run filho leva-o ESCAPADO, na forma do
	// `aos-orq`, e o `node_id` declarado é o cru. (Sem `:` — um run_id com `:` é recusado pelo
	// runtime durável, defeito anterior a este ticket, reportado à parte.)
	const no = "recolha.v1"
	filho := plano + separadorDoRunFilho + "recolha+2ev1"
	if idDoRunFilho(plano, no) != filho {
		t.Fatalf("pré-condição: idDoRunFilho(%q, %q) = %q", plano, no, idDoRunFilho(plano, no))
	}

	// Recusados: a MESMA 403, e o run não existe.
	for nome, c := range map[string]struct {
		run string
		v   vinculoAoPedido
	}{
		"plan_id sem node_id":   {filho, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan"}},
		"node_id com espaco":    {filho, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: "n 1"}},
		"node_id acima de 128":  {plano + separadorDoRunFilho + strings.Repeat("n", 129), vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: strings.Repeat("n", 129)}},
		"plan_id reservado":     {filho, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: streamsReservados + "x", NodeID: no}},
		"plan_id com ponto":     {filho, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + ".plan", NodeID: no}},
		"node_id de outro run":  {filho, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: "n1"}},
		"node_id noutro escape": {plano + separadorDoRunFilho + "recolha+2Ev1", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: no}},
	} {
		v := c.v
		if r := f.submeterFilho(t, c.run, tok, &v, aos439Drenador); r.code != http.StatusForbidden {
			t.Fatalf("%s: tinha de ser a 403 uniforme, veio %d %s", nome, r.code, r.body)
		}
		if _, err := f.node.EventStore.Read(context.Background(), c.run, 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
			t.Fatalf("%s: o run recusado nao pode existir (%v)", nome, err)
		}
	}
	// O tecto é 128, e 128 passa a forma (o run com esse id é o dele).
	if err := validarOrigemDeclarada(vinculoAoPedido{RunID: plano, PlanID: plano + "-plan", NodeID: strings.Repeat("n", 128)},
		plano+separadorDoRunFilho+strings.Repeat("n", 128)); err != nil {
		t.Fatalf("um node_id de 128 bytes e da gramatica: %v", err)
	}

	v := vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: no}
	if r := f.submeterFilho(t, filho, tok, &v, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("run filho: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)
	o, ok := origemDe(t, eventosDoRun(t, f.node, filho))
	if !ok {
		t.Fatal("o run filho com o vinculo verificado tem de declarar a origem (run.plan_origin)")
	}
	quer := origemDoRunFilho{Versao: versaoDaOrigem,
		Pedido: refDoPedidoDeOrigem{Stream: planRequestStream, RunID: plano, Geracao: 2},
		PlanID: plano + "-plan", NodeID: no}
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

// O run.plan_origin NÃO LEVA O `seq` da fila (revisão do AOS-477, B-2): o run filho lê-se por
// região, e o `seq` conta a actividade de fila do nó inteiro.
func TestAOS477OrigemNaoExpoeOSeqDaFila(t *testing.T) {
	raw, err := json.Marshal(origemDoRunFilho{Versao: versaoDaOrigem,
		Pedido: refDoPedidoDeOrigem{Stream: planRequestStream, RunID: "p", Geracao: 3}, PlanID: "p-plan", NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"seq"`) {
		t.Fatalf("o run.plan_origin nao pode levar o seq da fila: %s", raw)
	}
}

// M-1 da revisão: a origem grava-se DEPOIS do Submit. Um run `<plano>~<nó>` criado ANTES por outra
// via (um POST /runs sem vínculo) não pode receber a origem que o drenador declara depois.
func TestAOS477OrigemNaoEntraNumRunAlheio(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-477-alheio"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	filho := plano + separadorDoRunFilho + "n1"

	// O run alheio, criado primeiro e SEM vínculo.
	manualTok, err := f.node.Authority.MintForHuman(context.Background(), tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatal(err)
	}
	if r := f.submeterFilho(t, filho, manualTok.Compact, nil, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("run alheio: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)

	// Depois, o drenador com o vínculo válido para o mesmo id.
	v := vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: plano + "-plan", NodeID: "n1"}
	r := f.submeterFilho(t, filho, f.tokenDoMandato(t, aos439Alice), &v, aos439Drenador)
	// PRÉ-CONDIÇÃO: a re-submissão tem de ter sido ACEITE (201 idempotente). Uma recusa (403) faria
	// o teste passar sem chegar ao ponto em que a ordem «origem depois do Submit» decide.
	if r.code != http.StatusCreated {
		t.Fatalf("a re-submissao do drenador sobre o run alheio devia dar 201 idempotente, veio %d %s", r.code, r.body)
	}
	if _, ok := origemDe(t, eventosDoRun(t, f.node, filho)); ok {
		t.Fatal("a origem do drenador entrou no stream de um run que ele NAO hospedou")
	}
}

// B-4 da revisão: a origem grava-se mesmo que o cliente já tenha desligado.
func TestAOS477OrigemGravaComOClienteDesligado(t *testing.T) {
	f := noAOS439(t)
	h := &apiHandler{node: f.node}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.gravarOrigemDoRunFilho(ctx, "plano-477-ctx~n1", vinculoAoPedido{RunID: "plano-477-ctx", Geracao: 1, PlanID: "plano-477-ctx-plan", NodeID: "n1"}, agentruntime.RetryNoticeNone)
	if _, ok := origemDe(t, eventosDoRun(t, f.node, "plano-477-ctx~n1")); !ok {
		t.Fatal("com o contexto do pedido cancelado a origem tem de ser gravada na mesma")
	}
}

// B-3 da revisão: a gramática do node_id do nó é a do plano, lida da FONTE (os dois módulos não
// se importam). Se divergirem, todos os runs filhos com um node_id fora da copia levam 403.
func TestAOS477GramaticaDoNodeIDCasaComOPlano(t *testing.T) {
	plano := lerFonteDeTeste(t, "../../control-plane/orchestrator/plan/plandocument.go")
	if !strings.Contains(plano, "const maxNodeIDLen = "+strconv.Itoa(maxNodeIDDeclarado)+"\n") {
		t.Fatalf("o tecto do node_id do plano deixou de ser %d — a copia de validarOrigemDeclarada tem de mudar com ele", maxNodeIDDeclarado)
	}
	// IGUALDADE dos ramos do switch, e não presença: um ramo ACRESCENTADO de um dos lados (um
	// carácter novo admitido pelo plano) tem de avermelhar, ou o nó recusa com 403 todos os runs
	// filhos que o usem (revisão da ronda 2, B-b).
	doPlano := casosDoSwitch(t, plano, "func ValidNodeID(")
	doNo := casosDoSwitch(t, lerFonteDeTeste(t, "plan_origem.go"), "func validarOrigemDeclarada(")
	if strings.Join(doPlano, "\n") != strings.Join(doNo, "\n") {
		t.Fatalf("a gramatica do node_id divergiu:\n plano %q\n no    %q", doPlano, doNo)
	}
}

// casosDoSwitch devolve as linhas `case …` do primeiro `switch {` depois de `assinatura`, até ao
// `default:` — o charset da gramática, tal como está escrito.
func casosDoSwitch(t *testing.T, fonte, assinatura string) []string {
	t.Helper()
	i := strings.Index(fonte, assinatura)
	if i < 0 {
		t.Fatalf("%q nao encontrada na fonte", assinatura)
	}
	j := strings.Index(fonte[i:], "switch {")
	if j < 0 {
		t.Fatalf("sem switch em %q", assinatura)
	}
	var casos []string
	for _, l := range strings.Split(fonte[i+j:], "\n")[1:] {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "default:") {
			break
		}
		if strings.HasPrefix(l, "case ") {
			casos = append(casos, l)
		}
	}
	if len(casos) == 0 {
		t.Fatalf("switch sem ramos em %q", assinatura)
	}
	return casos
}

// B-3 / M-2: o id do run filho que o nó confere é o que o `aos-orq` compõe. Os MESMOS vectores
// estão em `packages/cmd/aos-orq/aos477_origem_no_log_test.go` (TestAOS477ChildRunIDTemOsVectoresDoNo).
func TestAOS477IdDoRunFilhoTemOsVectoresDoOrquestrador(t *testing.T) {
	for no, quer := range aos477VectoresDoRunFilho {
		if got := idDoRunFilho("p", no); got != quer {
			t.Errorf("idDoRunFilho(p, %q) = %q, quer %q", no, got, quer)
		}
	}
}

// aos477VectoresDoRunFilho: node_id → id do run filho do pedido `p`.
var aos477VectoresDoRunFilho = map[string]string{
	// O ALFABETO INTEIRO da gramática: só o `.` se escapa (revisão da ronda 2, B-a).
	"azAZ09_-.:": "p~azAZ09_-+2e:",
	// 128 bytes (o tecto) com um `.` no fim.
	strings.Repeat("a", 127) + ".": "p~" + strings.Repeat("a", 127) + "+2e",
	"n1":                           "p~n1",
	"recolha.v1:a":                 "p~recolha+2ev1:a",
	"a.b":                          "p~a+2eb",
	"a_2eb":                        "p~a_2eb",
	"a+b":                          "p~a++b",
	"a~b":                          "p~a+7eb",
}

// storeQueBloqueia é um Event Store cujo Append só volta quando o contexto acaba.
type storeQueBloqueia struct{ EventStorePort }

func (storeQueBloqueia) Append(ctx context.Context, _ string, _ eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	<-ctx.Done()
	return eventstore.AppendResult{}, ctx.Err()
}

// O contexto da origem não é cancelável pelo cliente, mas TEM prazo (controlSealTimeout): um store
// pendurado não pode prender o handler do `POST /runs` para sempre (revisão da ronda 2, mutante C).
func TestAOS477OrigemTemPrazoProprio(t *testing.T) {
	h := &apiHandler{node: &Node{EventStore: storeQueBloqueia{}}}
	feito := make(chan struct{})
	go func() {
		h.gravarOrigemDoRunFilho(context.Background(), "p~n1", vinculoAoPedido{RunID: "p", Geracao: 1}, agentruntime.RetryNoticeNone)
		close(feito)
	}()
	select {
	case <-feito:
	case <-time.After(controlSealTimeout + 3*time.Second):
		t.Fatalf("a gravacao da origem nao voltou dentro do prazo proprio (%s)", controlSealTimeout)
	}
}
