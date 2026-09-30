package main

// AOS-457 — QUOTA DE DESPESA POR PRINCIPAL, MENSAL UTC.
//
// Três camadas: a quota sobre um Event Store em memória (a aritmética, a janela, a durabilidade, a
// concorrência e o apagamento), a admissão pelo NodeService real (reserva, retoma isenta,
// liquidação no fim do run, 429 com Retry-After), e o Bootstrap (o ambiente, a recusa sem principal
// verificado, o store DSAR composto).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	dsar "github.com/aos-ref/control-plane/governance/dsar"
	"github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// relogioDeQuota é um relógio manual para a janela mensal.
type relogioDeQuota struct {
	mu sync.Mutex
	t  time.Time
}

func (r *relogioDeQuota) agora() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *relogioDeQuota) ir(t time.Time) {
	r.mu.Lock()
	r.t = t
	r.mu.Unlock()
}

func novoStore(t *testing.T) eventstore.EventStore {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

// quotaDeTeste compõe a quota: `limite` tokens por mês, `porRun` tokens reservados por admissão.
func quotaDeTeste(es eventstore.EventStore, limite, porRun int64, rel *relogioDeQuota, consumo integration.ConsumoDuravel) *quotaPorPrincipal {
	return &quotaPorPrincipal{
		es:      es,
		limite:  budget.Amount{Tokens: limite, CostMicroUSD: integration.UnlimitedCostMicroUSD},
		reserva: budget.Amount{Tokens: porRun},
		consumo: consumo,
		agora:   rel.agora,
	}
}

func consumoFixo(tokens int64) integration.ConsumoDuravel {
	return func(context.Context, string) (budget.Amount, error) { return budget.Amount{Tokens: tokens}, nil }
}

var setembro = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// TestAOS457AReservaEDura — o critério central. A quota é de 250 e cada admissão reserva 100: a
// terceira admissão é RECUSADA sem nenhum run ter gasto nada, porque as duas primeiras podem vir a
// gastar o tecto inteiro. É isso que impede N runs admitidos juntos de ultrapassar a quota.
func TestAOS457AReservaEDura(t *testing.T) {
	ctx := context.Background()
	q := quotaDeTeste(novoStore(t), 250, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	for _, run := range []string{"r1", "r2"} {
		if err := q.reservar(ctx, "human:alice", run); err != nil {
			t.Fatalf("reservar %s: %v", run, err)
		}
	}
	err := q.reservar(ctx, "human:alice", "r3")
	if !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("a 3a reserva devia ser recusada (200 reservados + 100 > 250), veio %v", err)
	}
	var qe *quotaEsgotadaError
	if !errors.As(err, &qe) || qe.gasto.Tokens != 200 {
		t.Fatalf("a recusa devia dizer o gasto reservado (200): %v", err)
	}
}

// TestAOS457ALiquidacaoLibertaONaoUsado — o run terminou a gastar 30: a quota passa a contar 30 e
// não 100, e cabem mais duas reservas.
func TestAOS457ALiquidacaoLibertaONaoUsado(t *testing.T) {
	ctx := context.Background()
	q := quotaDeTeste(novoStore(t), 250, 100, &relogioDeQuota{t: setembro}, consumoFixo(30))
	for _, run := range []string{"r1", "r2"} {
		if err := q.reservar(ctx, "human:alice", run); err != nil {
			t.Fatalf("reservar %s: %v", run, err)
		}
	}
	q.liquidar(ctx, "human:alice", "r1")
	q.liquidar(ctx, "human:alice", "r1") // idempotente
	if err := q.reservar(ctx, "human:alice", "r3"); err != nil {
		t.Fatalf("depois de liquidar r1 por 30, 30+100+100 cabe em 250: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "r4"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("30+100+100+100 > 250 devia recusar, veio %v", err)
	}
}

// TestAOS457ConsumoIlegivelMantemAReservaInteira — sem saber quanto o run gastou (turnos sem usage),
// a liquidação NÃO acontece: contar a menos seria a quota a abrir sobre uma cegueira.
func TestAOS457ConsumoIlegivelMantemAReservaInteira(t *testing.T) {
	ctx := context.Background()
	cego := func(context.Context, string) (budget.Amount, error) { return budget.Amount{}, ErrBurndownNoUsage }
	q := quotaDeTeste(novoStore(t), 150, 100, &relogioDeQuota{t: setembro}, cego)
	if err := q.reservar(ctx, "human:alice", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	q.liquidar(ctx, "human:alice", "r1")
	if err := q.reservar(ctx, "human:alice", "r2"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("com o consumo de r1 ilegivel a reserva fica inteira (100), e 100+100 > 150; veio %v", err)
	}
}

// TestAOS457AReservaEIdempotentePorRun — reservar o mesmo run duas vezes (re-submissão, ou retoma
// de um plano) reserva uma vez.
func TestAOS457AReservaEIdempotentePorRun(t *testing.T) {
	ctx := context.Background()
	q := quotaDeTeste(novoStore(t), 150, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	for i := 0; i < 3; i++ {
		if err := q.reservar(ctx, "human:alice", "r1"); err != nil {
			t.Fatalf("reservar r1 pela %da vez: %v", i+1, err)
		}
	}
}

// TestAOS457OsPrincipaisSaoIndependentes — a quota de um principal não toca na de outro.
func TestAOS457OsPrincipaisSaoIndependentes(t *testing.T) {
	ctx := context.Background()
	q := quotaDeTeste(novoStore(t), 100, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	if err := q.reservar(ctx, "human:alice", "a1"); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "a2"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("alice esgotada devia recusar, veio %v", err)
	}
	if err := q.reservar(ctx, "human:bob", "b1"); err != nil {
		t.Fatalf("bob nao gastou nada e foi recusado: %v", err)
	}
}

// TestAOS457AJanelaEOMesUTC — esgotada a 30 de Setembro às 23:59 UTC, repõe às 00:00 UTC de 1 de
// Outubro. O relógio chega num fuso que já é Outubro em hora local e continua a ser Setembro em
// UTC: a janela é a UTC, não a local.
func TestAOS457AJanelaEOMesUTC(t *testing.T) {
	ctx := context.Background()
	lisboaVerao := time.FixedZone("UTC+1", 3600)
	rel := &relogioDeQuota{t: time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)}
	q := quotaDeTeste(novoStore(t), 100, 100, rel, consumoFixo(0))
	if err := q.reservar(ctx, "human:alice", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	// 00:30 de 1 de Outubro em UTC+1 = 23:30 de 30 de Setembro em UTC.
	rel.ir(time.Date(2026, 10, 1, 0, 30, 0, 0, lisboaVerao))
	err := q.reservar(ctx, "human:alice", "r2")
	var qe *quotaEsgotadaError
	if !errors.As(err, &qe) {
		t.Fatalf("ainda e Setembro em UTC: devia recusar, veio %v", err)
	}
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !qe.repoe.Equal(want) || qe.faltam != 30*time.Minute {
		t.Fatalf("repoe=%v faltam=%v, esperava %v e 30m", qe.repoe, qe.faltam, want)
	}
	rel.ir(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err := q.reservar(ctx, "human:alice", "r2"); err != nil {
		t.Fatalf("em Outubro UTC a quota repos-se: %v", err)
	}
}

// TestAOS457ORunLiquidaNoMesEmQueReservou — admitido em Setembro, termina em Outubro: a liquidação
// vai para Setembro e liberta a quota de Setembro, não mexe na de Outubro.
func TestAOS457ORunLiquidaNoMesEmQueReservou(t *testing.T) {
	ctx := context.Background()
	es := novoStore(t)
	rel := &relogioDeQuota{t: time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)}
	q := quotaDeTeste(es, 100, 100, rel, consumoFixo(7))
	if err := q.reservar(ctx, "human:alice", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	rel.ir(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	q.liquidar(ctx, "human:alice", "r1")
	evs, err := es.Read(ctx, quotaStreamDe("human:alice", "202609"), 1)
	if err != nil {
		t.Fatalf("ler Setembro: %v", err)
	}
	if n := len(evs); n != 2 || evs[1].Type != EventTypeQuotaSettled {
		t.Fatalf("Setembro devia ter a reserva e a liquidacao, tem %d eventos", n)
	}
	if _, err := es.Read(ctx, quotaStreamDe("human:alice", "202610"), 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("Outubro nao devia ter sido tocado: %v", err)
	}
}

// TestAOS457UmRestartNaoRepoeAQuota — o critério de durabilidade, ao nível da quota: uma quota NOVA
// sobre o MESMO store (é o que um restart do nó compõe) lê o gasto e continua a recusar.
func TestAOS457UmRestartNaoRepoeAQuota(t *testing.T) {
	ctx := context.Background()
	es := novoStore(t)
	rel := &relogioDeQuota{t: setembro}
	if err := quotaDeTeste(es, 100, 100, rel, consumoFixo(0)).reservar(ctx, "human:alice", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	depoisDoRestart := quotaDeTeste(es, 100, 100, rel, consumoFixo(0))
	if err := depoisDoRestart.reservar(ctx, "human:alice", "r2"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("a quota foi reposta por uma instancia nova sobre o mesmo store: %v", err)
	}
}

// TestAOS457OApagamentoRepoeAQuota — decisão do dono: o `/dsar/erase` apaga o agregado, e a quota
// repõe-se. Uma leitura ignora tudo o que vem antes da marca.
func TestAOS457OApagamentoRepoeAQuota(t *testing.T) {
	ctx := context.Background()
	q := quotaDeTeste(novoStore(t), 100, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	if err := q.reservar(ctx, "human:alice", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	if err := q.Shred("human:alice"); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "r2"); err != nil {
		t.Fatalf("depois do apagamento a quota devia estar reposta: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "r3"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("a quota reposta volta a contar: %v", err)
	}
}

// TestAOS457UmRegistoIlegivelNega — um evento que não se sabe ler no stream da quota recusa a
// admissão, em vez de a somar como zero.
func TestAOS457UmRegistoIlegivelNega(t *testing.T) {
	ctx := context.Background()
	es := novoStore(t)
	q := quotaDeTeste(es, 1000, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	if _, err := es.Append(ctx, quotaStreamDe("human:alice", "202609"), eventstore.EventInput{
		Type: EventTypeQuotaReserved, Payload: json.RawMessage(`{"tokens":"muitos"}`), RunID: quotaRunID, StepID: "lixo",
		Producer: eventstore.Producer{NHIID: quotaNHI},
	}); err != nil {
		t.Fatalf("semear: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "r1"); !errors.Is(err, ErrPrincipalQuotaUnreadable) {
		t.Fatalf("um registo ilegivel devia recusar, veio %v", err)
	}
	// E um TIPO que a quota não conhece: ignorá-lo seria somar como zero o que não se sabe ler.
	es2 := novoStore(t)
	q2 := quotaDeTeste(es2, 1000, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	if _, err := es2.Append(ctx, quotaStreamDe("human:alice", "202609"), eventstore.EventInput{
		Type: EventTypeToolCallBudget, Payload: json.RawMessage(`{"run_id":"x","tokens":1}`), RunID: quotaRunID, StepID: "estranho",
		Producer: eventstore.Producer{NHIID: quotaNHI},
	}); err != nil {
		t.Fatalf("semear: %v", err)
	}
	if err := q2.reservar(ctx, "human:alice", "r1"); !errors.Is(err, ErrPrincipalQuotaUnreadable) {
		t.Fatalf("um tipo desconhecido no stream da quota devia recusar, veio %v", err)
	}
}

// TestAOS457ADimensaoDeDolaresTambemNega — com quota em dólares, nega a que esgotar primeiro. Aqui
// os tokens sobram e os dólares não.
func TestAOS457ADimensaoDeDolaresTambemNega(t *testing.T) {
	ctx := context.Background()
	q := quotaDeTeste(novoStore(t), 1_000_000, 10, &relogioDeQuota{t: setembro}, consumoFixo(0))
	q.limite.CostMicroUSD = 150
	q.reserva.CostMicroUSD = 100
	if err := q.reservar(ctx, "human:alice", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "r2"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("100+100 micro-USD > 150 devia recusar com os tokens a sobrar, veio %v", err)
	}
}

// TestAOS457SemPrincipalNaoHaAdmissao — defesa em profundidade: com a quota composta, um run sem
// principal a quem imputar não passa sem quota.
func TestAOS457SemPrincipalNaoHaAdmissao(t *testing.T) {
	q := quotaDeTeste(novoStore(t), 1000, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	if err := q.reservar(context.Background(), "", "r1"); !errors.Is(err, ErrPrincipalQuotaNoPrincipal) {
		t.Fatalf("veio %v", err)
	}
}

// TestAOS457DuasReplicasNaoUltrapassamAQuota — duas quotas sobre o mesmo store (duas réplicas, cada
// uma com o seu mutex) e 40 admissões em paralelo: o `WithExpectedSeq` serializa-as no stream do
// principal, e nunca ficam reservadas mais do que cabem.
func TestAOS457DuasReplicasNaoUltrapassamAQuota(t *testing.T) {
	ctx := context.Background()
	es := novoStore(t)
	rel := &relogioDeQuota{t: setembro}
	replicas := []*quotaPorPrincipal{quotaDeTeste(es, 1000, 100, rel, consumoFixo(0)), quotaDeTeste(es, 1000, 100, rel, consumoFixo(0))}
	var wg sync.WaitGroup
	var mu sync.Mutex
	aceites := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := replicas[i%2].reservar(ctx, "human:alice", fmt.Sprintf("r%02d", i)); err == nil {
				mu.Lock()
				aceites++
				mu.Unlock()
			} else if !errors.Is(err, ErrPrincipalQuotaExhausted) && !errors.Is(err, ErrPrincipalQuotaContention) {
				t.Errorf("erro inesperado: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if aceites > 10 {
		t.Fatalf("%d admissoes aceites com quota para 10 — duas replicas ultrapassaram a quota", aceites)
	}
	st, err := replicas[0].ler(ctx, quotaStreamDe("human:alice", "202609"))
	if err != nil {
		t.Fatalf("ler: %v", err)
	}
	if g := st.gasto(); g.Tokens > 1000 {
		t.Fatalf("gasto reservado %d > quota 1000", g.Tokens)
	}
}

// ---------------------------------------------------------------------------------------------
// O NodeService real.
// ---------------------------------------------------------------------------------------------

func servicoComQuota(t *testing.T, node *Node, limite, porRun int64, rel *relogioDeQuota) *NodeService {
	t.Helper()
	node.QuotaPorPrincipal = quotaDeTeste(node.EventStore, limite, porRun, rel,
		consumoDuravelParaOrcamento(newTurnLedgerBurndown(node.EventStore)))
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})
	return svc
}

func esperarRun(t *testing.T, svc *NodeService, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := svc.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait %s: %v", runID, err)
	}
}

// TestAOS457OServicoReservaELiquidaPeloLedger — um run real termina e a quota liquida pelo que o
// ledger de turnos diz que ele gastou (o countingModel reporta 1+1 tokens por turno).
func TestAOS457OServicoReservaELiquidaPeloLedger(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	t.Cleanup(func() { _ = node.Close() })
	rel := &relogioDeQuota{t: setembro}
	svc := servicoComQuota(t, node, 250, 100, rel)
	ctx := context.Background()

	g := svcGoal("run-q1", "trabalho")
	if err := svc.Submit(ctx, g); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	esperarRun(t, svc, "run-q1")

	st, err := node.QuotaPorPrincipal.ler(ctx, quotaStreamDe(imputadoA(g), "202609"))
	if err != nil {
		t.Fatalf("ler: %v", err)
	}
	l, ok := st.liquidadas["run-q1"]
	if !ok {
		t.Fatal("o run terminou com desfecho no log e a quota nao foi liquidada")
	}
	if l.Tokens != 2 {
		t.Fatalf("liquidado %d tokens, o ledger diz 2", l.Tokens)
	}
}

// TestAOS457OServicoRecusaERetomaIsenta — quota 100 e 100 por run: run-a é admitido e liquida por
// 2; run-b precisaria de 2+100 > 100 e é RECUSADO sem ficar em curso. A RETOMA não reserva — o run
// já reservou quando foi admitido —, pelo que passa com a quota esgotada.
func TestAOS457OServicoRecusaERetomaIsenta(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	t.Cleanup(func() { _ = node.Close() })
	svc := servicoComQuota(t, node, 100, 100, &relogioDeQuota{t: setembro})
	ctx := context.Background()
	comPrincipal := func(runID string) agentruntime.Goal {
		g := svcGoal(runID, "trabalho")
		g.Principal.NHIID = "nhi:partilhado"
		return g
	}
	if err := svc.Submit(ctx, comPrincipal("run-a")); err != nil {
		t.Fatalf("Submit run-a: %v", err)
	}
	esperarRun(t, svc, "run-a")
	if err := svc.Submit(ctx, comPrincipal("run-b")); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("run-b devia ser recusado pela quota, veio %v", err)
	}
	// A recusa desfaz a reserva do `run_id` em `s.runs`: re-submeter recebe a MESMA recusa, e não
	// «já em curso» — que seria um run fantasma a bloquear o id e a contar no tecto por-chamador.
	if err := svc.Submit(ctx, comPrincipal("run-b")); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("re-submeter run-b devia ter a mesma recusa, veio %v — a reserva em s.runs nao foi desfeita", err)
	}
	if err := svc.submit(ctx, comPrincipal("run-c"), true); err != nil {
		t.Fatalf("a retoma nao reserva e nao pode ser recusada pela quota: %v", err)
	}
	esperarRun(t, svc, "run-c")
}

// TestAOS457O429TrazRetryAfter — pelo `POST /runs`: a recusa é 429, o corpo é o uniforme, e o
// Retry-After diz quantos segundos faltam para a reposição.
func TestAOS457O429TrazRetryAfter(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	t.Cleanup(func() { _ = node.Close() })
	// 12:00 de 30 de Setembro UTC: faltam 12 h para a reposição.
	rel := &relogioDeQuota{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	node.QuotaPorPrincipal = quotaDeTeste(node.EventStore, 100, 100, rel, consumoFixo(0))
	if err := node.QuotaPorPrincipal.reservar(context.Background(), "nhi:esgotado", "anterior"); err != nil {
		t.Fatalf("esgotar: %v", err)
	}
	_, h := newAPI(t, node)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/runs",
		strings.NewReader(`{"run_id":"run-http","principal_nhi":"nhi:esgotado","objective":"x"}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, esperava 429: %s", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != "43200" {
		t.Fatalf("Retry-After=%q, esperava 43200 (12 h)", ra)
	}
	if strings.Contains(rec.Body.String(), "tokens") || strings.Contains(rec.Body.String(), "100") {
		t.Fatalf("o corpo nao pode levar os numeros do gasto: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------------------------
// O Bootstrap: o ambiente, o principal verificado, o store DSAR.
// ---------------------------------------------------------------------------------------------

func limparAmbienteDaQuota(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AOS_BUDGET_PRINCIPAL_MAX_TOKENS", "AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD",
		"AOS_BUDGET_MAX_TOKENS", "AOS_BUDGET_MAX_COST_MICRO_USD"} {
		t.Setenv(k, "")
	}
}

// TestAOS457OAmbienteValidaOParFinal — cada configuração que seria uma quota inexistente e
// anunciada, ou uma quota que nega tudo, aborta.
func TestAOS457OAmbienteValidaOParFinal(t *testing.T) {
	casos := []struct {
		nome                             string
		quota, quotaCusto, run, runCusto string
		erro                             error
	}{
		{"por configurar", "", "", "", "", nil},
		{"por configurar com tecto por-run", "", "", "100", "", nil},
		{"composta", "1000", "", "100", "", nil},
		{"composta com dolares", "1000", "5000", "100", "500", nil},
		{"zero nao desliga", "0", "", "100", "", ErrBadPrincipalQuota},
		{"negativa", "-1", "", "100", "", ErrBadPrincipalQuota},
		{"ilegivel", "mil", "", "100", "", ErrBadPrincipalQuota},
		{"dolares sem tokens", "", "5000", "100", "500", ErrBadPrincipalQuota},
		{"dolares ilegiveis", "1000", "x", "100", "500", ErrBadPrincipalQuota},
		{"sem tecto por-run", "1000", "", "", "", ErrPrincipalQuotaWithoutRunBudget},
		{"dolares sem tecto por-run em dolares", "1000", "5000", "100", "", ErrPrincipalQuotaWithoutRunBudget},
		{"quota menor do que o tecto por-run", "99", "", "100", "", ErrPrincipalQuotaBelowRunBudget},
		{"quota em dolares menor", "1000", "499", "100", "500", ErrPrincipalQuotaBelowRunBudget},
		{"quota igual ao tecto cabe um run", "100", "", "100", "", nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			limparAmbienteDaQuota(t)
			t.Setenv("AOS_BUDGET_PRINCIPAL_MAX_TOKENS", c.quota)
			t.Setenv("AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD", c.quotaCusto)
			t.Setenv("AOS_BUDGET_MAX_TOKENS", c.run)
			t.Setenv("AOS_BUDGET_MAX_COST_MICRO_USD", c.runCusto)
			rb, err := budgetFromEnv()
			if err != nil {
				t.Fatalf("budgetFromEnv: %v", err)
			}
			q, err := principalQuotaFromEnv(rb, novoStore(t), nil, nil, nil)
			if c.erro == nil {
				if err != nil {
					t.Fatalf("devia compor: %v", err)
				}
				if (c.quota == "") != (q == nil) {
					t.Fatalf("quota=%v com AOS_BUDGET_PRINCIPAL_MAX_TOKENS=%q", q, c.quota)
				}
				return
			}
			if !errors.Is(err, c.erro) {
				t.Fatalf("devia abortar com %v, veio %v", c.erro, err)
			}
		})
	}
}

// TestAOS457SemPrincipalVerificadoNaoArranca — a quota configurada num nó cujo principal vem de um
// header (sem credencial forte) ABORTA o arranque: um atacante gastaria a quota de outro.
func TestAOS457SemPrincipalVerificadoNaoArranca(t *testing.T) {
	limparAmbienteDaQuota(t)
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "100")
	t.Setenv("AOS_BUDGET_PRINCIPAL_MAX_TOKENS", "1000")
	for nome, cfg := range map[string]Config{
		"sem gate soberano": tnBaseConfig(),
		"gate sem credencial forte": func() Config {
			c := tnBaseConfig()
			c.BoardRegions = map[string]string{govBoard: govRegion}
			return c
		}(),
	} {
		t.Run(nome, func(t *testing.T) {
			node, err := Bootstrap(context.Background(), cfg, io.Discard)
			if err == nil {
				_ = node.Close()
			}
			if !errors.Is(err, ErrPrincipalQuotaUnverified) {
				t.Fatalf("devia recusar arrancar, veio %v", err)
			}
		})
	}
}

// TestAOS457OBootstrapCompoeEOApagamentoChegaAQuota — com principal verificado o Bootstrap compõe a
// quota, e o fluxo DSAR REAL (`node.DSAR.Receive`) repõe-a: prova que o store entrou na lista de
// apagamento, e não só que existe.
func TestAOS457OBootstrapCompoeEOApagamentoChegaAQuota(t *testing.T) {
	limparAmbienteDaQuota(t)
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "100")
	t.Setenv("AOS_BUDGET_PRINCIPAL_MAX_TOKENS", "100")
	idp := newSovTestIDP(t)
	node := newSovOIDCNode(t, &countingModel{}, idp)
	q := node.QuotaPorPrincipal
	if q == nil {
		t.Fatal("o Bootstrap nao compos a quota configurada sobre principal verificado")
	}
	ctx := context.Background()
	if err := q.reservar(ctx, "human:titular", "r1"); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	if err := q.reservar(ctx, "human:titular", "r2"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("esgotada devia recusar, veio %v", err)
	}
	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-457", SubjectID: "human:titular"}); err != nil {
		t.Fatalf("DSAR.Receive: %v", err)
	}
	if err := q.reservar(ctx, "human:titular", "r2"); err != nil {
		t.Fatalf("o /dsar/erase devia repor a quota (store no fluxo DSAR): %v", err)
	}
}

// TestAOS457ONilTipadoNaoEntraNoFluxo — sem quota, a lista de apagamento não ganha um store que
// seria uma interface não-nil sobre um ponteiro nil.
func TestAOS457ONilTipadoNaoEntraNoFluxo(t *testing.T) {
	if n := len(storesDeApagamento(nil, nil, nil)); n != 2 {
		t.Fatalf("sem quota: %d stores, esperava 2", n)
	}
	if n := len(storesDeApagamento(nil, nil, &quotaPorPrincipal{})); n != 3 {
		t.Fatalf("com quota: %d stores, esperava 3", n)
	}
}

// TestAOS457OBannerDeclaraAPostura — os dois estados possíveis, com os números em vigor.
func TestAOS457OBannerDeclaraAPostura(t *testing.T) {
	if l := strings.Join(principalQuotaPostureBanner(nil), " "); !strings.Contains(l, "NAO CONFIGURADA") {
		t.Fatalf("sem quota: %s", l)
	}
	q := quotaDeTeste(nil, 5000, 100, &relogioDeQuota{t: setembro}, nil)
	l := strings.Join(principalQuotaPostureBanner(q), " ")
	for _, want := range []string{"VERIFICADO", "5000 tokens", "mes UTC", "RESERVA o tecto por-run inteiro (100 tokens", "429", "/dsar/erase"} {
		if !strings.Contains(l, want) {
			t.Fatalf("o banner nao diz %q: %s", want, l)
		}
	}
}

// noSobreStore compõe um nó sobre um Event Store dado — dois nós sobre o mesmo store são o nó antes
// e depois de um restart.
func noSobreStore(t *testing.T, es *eventstore.Store, model agentruntime.ModelClient) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.EventStore = es
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return node
}

// TestAOS457ACrashARetomaNaoRepoemAQuota — o critério de durabilidade ao nível do NÓ, pelo caminho
// do crash: um run está EM CURSO (reservou, não liquidou) quando o nó cai. O nó que arranca sobre o
// mesmo Event Store tem a quota a zero em memória — e continua a recusar, porque a reserva está no
// log. Se a quota vivesse em memória, o restart repunha-a e o run em curso passava a não contar.
func TestAOS457ACrashARetomaNaoRepoemAQuota(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rel := &relogioDeQuota{t: setembro}
	ctx := context.Background()
	comPrincipal := func(runID string) agentruntime.Goal {
		g := svcGoal(runID, "trabalho")
		g.Principal.NHIID = "nhi:mesmo"
		return g
	}

	// Antes do crash: um run a gastar, sem fim à vista.
	bloqueia := &blockingModel{started: make(chan struct{})}
	antes := noSobreStore(t, es, bloqueia)
	svcAntes := servicoComQuota(t, antes, 100, 100, rel)
	if err := svcAntes.Submit(ctx, comPrincipal("run-em-curso")); err != nil {
		t.Fatalf("Submit antes do crash: %v", err)
	}
	<-bloqueia.started

	// O «restart»: um nó e uma quota NOVOS sobre o mesmo store. Nada em memória passa de um para o
	// outro.
	depois := noSobreStore(t, es, &countingModel{})
	t.Cleanup(func() { _ = depois.Close() })
	svcDepois := servicoComQuota(t, depois, 100, 100, rel)
	if err := svcDepois.Submit(ctx, comPrincipal("run-novo")); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("o no reiniciado esqueceu a reserva do run em curso: veio %v", err)
	}
}

// reservaLiquidada diz se o run tem liquidação no stream do mês de Setembro do principal.
func reservaLiquidada(t *testing.T, q *quotaPorPrincipal, principal, runID string) bool {
	t.Helper()
	st, err := q.ler(context.Background(), quotaStreamDe(principal, "202609"))
	if err != nil {
		t.Fatalf("ler: %v", err)
	}
	if _, ok := st.reservas[runID]; !ok {
		t.Fatalf("o run %q nao tem reserva", runID)
	}
	_, ok := st.liquidadas[runID]
	return ok
}

// TestAOS457UmRunSuspensoNaoLiquida — um run que sai do hostRun SUSPENSO (`waiting_on_human`) não
// terminou: vai continuar a gastar quando for retomado. Liquidá-lo libertaria a reserva de trabalho
// por fazer. O selo terminal é um no-op sobre `waiting_on_human`, e a quota tem de o respeitar.
func TestAOS457UmRunSuspensoNaoLiquida(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	node.QuotaPorPrincipal = quotaDeTeste(node.EventStore, 1000, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	const run = "run-457-suspenso"
	aos263Suspende(t, node, run, 6, 880, 1000)
	if err := node.QuotaPorPrincipal.reservar(context.Background(), "human:alice", run); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	// O hostRun a sair: o gate reabre sobre o log (waiting_on_human) e o selo corre.
	if err := node.stateGates.Open(context.Background(), run, state.Uint64Token(2)); err != nil {
		t.Fatalf("gates.Open: %v", err)
	}
	t.Cleanup(func() { node.stateGates.Close(run) })
	rs := &runState{runID: run, principal: "human:alice", done: make(chan struct{})}
	svc.sealTerminalState(rs, "", agentruntime.Result{}, errors.New("suspenso a espera de um humano"), false)
	if reservaLiquidada(t, node.QuotaPorPrincipal, "human:alice", run) {
		t.Fatal("um run suspenso foi liquidado — a reserva de trabalho por fazer foi libertada")
	}
}

// TestAOS457OAbortPorExaustaoLiquida — o abort por exaustão (AOS-263) termina o run (`killed`) SEM o
// voltar a hospedar, pelo que o selo do hostRun nunca corre. Sem a liquidação no abort, a reserva
// ficava presa até ao fim do mês.
func TestAOS457OAbortPorExaustaoLiquida(t *testing.T) {
	node, svc, h, opPriv := aos263Node(t)
	node.QuotaPorPrincipal = quotaDeTeste(node.EventStore, 1000, 100, &relogioDeQuota{t: setembro}, consumoFixo(0))
	const run = "run-457-abort"
	prompt := aos263Suspende(t, node, run, 6, 880, 1000)
	if err := node.QuotaPorPrincipal.reservar(context.Background(), "human:alice", run); err != nil {
		t.Fatalf("reservar: %v", err)
	}
	svc.mu.Lock()
	svc.suspended[run] = &runState{runID: run, suspended: true, principal: "human:alice", done: make(chan struct{})}
	svc.mu.Unlock()

	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, prompt.StepID), nil); rec.Code != http.StatusOK {
		t.Fatalf("abort devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if !reservaLiquidada(t, node.QuotaPorPrincipal, "human:alice", run) {
		t.Fatal("o abort por exaustao terminou o run e a reserva ficou por liquidar")
	}
}

// storeIntercalado deixa um teste meter uma escrita de OUTRA réplica exactamente entre a leitura e a
// escrita desta: o gancho corre uma vez, no primeiro Append, antes de o delegar.
type storeIntercalado struct {
	eventstore.EventStore
	gancho func()
	uma    sync.Once
}

func (s *storeIntercalado) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.uma.Do(func() {
		if s.gancho != nil {
			s.gancho()
		}
	})
	return s.EventStore.Append(ctx, stream, in, opts...)
}

// TestAOS457AOutraReplicaEntreALeituraEAEscrita — a corrida entre réplicas, forçada em vez de
// esperada. A réplica A lê a quota vazia e, antes de escrever, a réplica B reserva o único lugar. Sem
// a escrita condicional (`WithExpectedSeq`), A escreveria sobre uma leitura obsoleta e as duas
// ficariam reservadas com quota para uma. O teste de concorrência com goroutines não o via com
// `-race` (a corrida deixava de acontecer), e um sensor que depende do escalonamento não é sensor.
func TestAOS457AOutraReplicaEntreALeituraEAEscrita(t *testing.T) {
	ctx := context.Background()
	base := novoStore(t)
	rel := &relogioDeQuota{t: setembro}
	replicaB := quotaDeTeste(base, 100, 100, rel, consumoFixo(0))
	intercalado := &storeIntercalado{EventStore: base}
	intercalado.gancho = func() {
		if err := replicaB.reservar(ctx, "human:alice", "rB"); err != nil {
			t.Errorf("a replica B devia reservar o lugar livre: %v", err)
		}
	}
	replicaA := quotaDeTeste(intercalado, 100, 100, rel, consumoFixo(0))
	if err := replicaA.reservar(ctx, "human:alice", "rA"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("A escreveu sobre uma leitura obsoleta (a quota so tinha um lugar e B tomou-o): veio %v", err)
	}
	st, err := replicaA.ler(ctx, quotaStreamDe("human:alice", "202609"))
	if err != nil {
		t.Fatalf("ler: %v", err)
	}
	if n := len(st.reservas); n != 1 {
		t.Fatalf("%d reservas com quota para uma", n)
	}
}
