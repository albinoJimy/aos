package main

// AOS-466 — O GASTO DE PLANEAMENTO DO `POST /plans` CONTA NA QUOTA POR PRINCIPAL.
//
// O `aos-orq` decompõe cada pedido com o modelo antes de submeter os runs-filho, e antes deste
// ticket esse gasto não contava em quota nenhuma: um principal esgotado continuava a planear. Estes
// testes fixam a reserva no `POST /plans`, a liquidação por geração no `POST /plans/outcome` contra
// quem SUBMETEU (e não contra o drenador), e a regra de quando a reserva se liberta.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/substrate/eventstore"
)

// quotaComPlaneamento compõe a quota com `limite` tokens, `porRun` por admissão de run e `plano`
// por pedido de plano.
func quotaComPlaneamento(es eventstore.EventStore, limite, porRun, plano int64, rel *relogioDeQuota) *quotaPorPrincipal {
	q := quotaDeTeste(es, limite, porRun, rel, consumoFixo(0))
	q.reservaDoPlano = budget.Amount{Tokens: plano}
	return q
}

func gastoDe(t *testing.T, q *quotaPorPrincipal, principal string) budget.Amount {
	t.Helper()
	st, err := q.ler(context.Background(), quotaStreamDe(principal, mesUTC(q.agora())))
	if err != nil {
		t.Fatalf("ler a quota: %v", err)
	}
	return st.gasto()
}

func medido(tokens int64) consumoDoPlaneamento {
	return consumoDoPlaneamento{Tokens: tokens, TokensMedidos: true, CustoMedido: true}
}

// TestAOS466OAmbienteDaReservaDePlaneamento — a variável é obrigatória com a quota (decisão do
// dono), e cada configuração que a deixaria inerte, impossível ou por definir aborta o arranque.
func TestAOS466OAmbienteDaReservaDePlaneamento(t *testing.T) {
	casos := []struct {
		nome                                 string
		quota, quotaCusto, plano, planoCusto string
		erro                                 error
		reserva                              budget.Amount
	}{
		{"sem quota e sem reserva", "", "", "", "", nil, budget.Amount{}},
		{"reserva sem quota e inerte", "", "", "10", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva em dolares sem quota", "", "", "", "10", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"quota sem reserva nao arranca", "1000", "", "", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva zero", "1000", "", "0", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva negativa", "1000", "", "-5", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva ilegivel", "1000", "", "dez", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva maior do que a quota", "1000", "", "1001", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva igual a quota", "1000", "", "1000", "", nil, budget.Amount{Tokens: 1000}},
		{"composta", "1000", "", "40", "", nil, budget.Amount{Tokens: 40}},
		{"dolares na reserva sem dolares na quota", "1000", "", "40", "7", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"quota em dolares sem reserva em dolares", "1000", "5000", "40", "", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva em dolares maior do que a quota", "1000", "5000", "40", "5001", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"reserva em dolares zero", "1000", "5000", "40", "0", ErrBadPrincipalPlanQuota, budget.Amount{}},
		{"composta com dolares", "1000", "5000", "40", "70", nil, budget.Amount{Tokens: 40, CostMicroUSD: 70}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			limparAmbienteDaQuota(t)
			t.Setenv("AOS_BUDGET_MAX_TOKENS", "100")
			t.Setenv("AOS_BUDGET_MAX_COST_MICRO_USD", "500")
			t.Setenv("AOS_BUDGET_PRINCIPAL_MAX_TOKENS", c.quota)
			t.Setenv("AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD", c.quotaCusto)
			t.Setenv("AOS_BUDGET_PRINCIPAL_PLAN_TOKENS", c.plano)
			t.Setenv("AOS_BUDGET_PRINCIPAL_PLAN_COST_MICRO_USD", c.planoCusto)
			rb, err := budgetFromEnv()
			if err != nil {
				t.Fatalf("budgetFromEnv: %v", err)
			}
			q, err := principalQuotaFromEnv(rb, novoStore(t), nil, nil, nil)
			if c.erro != nil {
				if !errors.Is(err, c.erro) {
					t.Fatalf("devia abortar com %v, veio %v", c.erro, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("devia compor: %v", err)
			}
			if c.quota == "" {
				if q != nil {
					t.Fatal("sem quota nao se compoe nada")
				}
				return
			}
			if q.reservaDoPlano != c.reserva {
				t.Fatalf("reserva de planeamento %+v, esperava %+v", q.reservaDoPlano, c.reserva)
			}
		})
	}
}

// TestAOS466OPlaneamentoEOsRunsPartilhamAQuota — a reserva de planeamento é gasto do principal como
// outro qualquer: conta contra a admissão de runs, e o inverso.
func TestAOS466OPlaneamentoEOsRunsPartilhamAQuota(t *testing.T) {
	ctx := context.Background()
	rel := &relogioDeQuota{t: setembro}
	q := quotaComPlaneamento(novoStore(t), 250, 100, 100, rel)

	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatalf("a primeira reserva de planeamento cabe: %v", err)
	}
	// Idempotente: um re-POST do mesmo pedido não reserva outra vez.
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatalf("re-reservar o mesmo pedido: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "run-a"); err != nil {
		t.Fatalf("100 + 100 cabem em 250: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "run-b"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("a reserva de planeamento tinha de contar contra os runs (300 > 250), veio %v", err)
	}

	// O inverso, noutro principal: os runs esgotam o que o planeamento precisava.
	if err := q.reservar(ctx, "human:bob", "run-c"); err != nil {
		t.Fatalf("run-c: %v", err)
	}
	if err := q.reservar(ctx, "human:bob", "run-d"); err != nil {
		t.Fatalf("run-d: %v", err)
	}
	if err := q.reservarPlaneamento(ctx, "human:bob", "plano-2"); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("os runs tinham de contar contra o planeamento (300 > 250), veio %v", err)
	}
	if g := gastoDe(t, q, "human:bob"); g.Tokens != 200 {
		t.Fatalf("a recusa nao pode ter reservado: gasto %d, esperava 200", g.Tokens)
	}
}

// TestAOS466UmPlanoEUmRunComOMesmoNomeNaoSeConfundem — o planeamento reserva sob uma chave do espaço
// reservado: um pedido de plano `x` e um run `x` são duas reservas, e nenhuma é idempotente contra a
// outra (senão um run admitido «já estaria reservado» pelo planeamento, e sairia de graça).
func TestAOS466UmPlanoEUmRunComOMesmoNomeNaoSeConfundem(t *testing.T) {
	ctx := context.Background()
	rel := &relogioDeQuota{t: setembro}
	q := quotaComPlaneamento(novoStore(t), 1000, 100, 40, rel)
	if err := q.reservarPlaneamento(ctx, "human:alice", "x"); err != nil {
		t.Fatal(err)
	}
	if err := q.reservar(ctx, "human:alice", "x"); err != nil {
		t.Fatal(err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 140 {
		t.Fatalf("o plano e o run com o mesmo nome confundiram-se: gasto %d, esperava 140", g.Tokens)
	}
	// E a liquidação de um não liquida o outro.
	fechar(t, q, "x", 1, medido(0))
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("a parcela do plano liquidou o run: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466OReservaDoMesAnteriorNaoSeRepete — um re-POST no mês seguinte do mesmo pedido não
// reserva de novo: o `Append` do pedido deduplica, ninguém voltaria a planear, e a reserva nova
// ficaria presa até ao fim do mês.
func TestAOS466OReservaDoMesAnteriorNaoSeRepete(t *testing.T) {
	ctx := context.Background()
	rel := &relogioDeQuota{t: setembro}
	q := quotaComPlaneamento(novoStore(t), 1000, 100, 100, rel)
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	rel.ir(setembro.AddDate(0, 1, 0))
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 0 {
		t.Fatalf("o re-POST no mes seguinte reservou de novo: gasto de outubro %d", g.Tokens)
	}
	// E a liquidação encontra a reserva no mês anterior.
	fechar(t, q, "plano-1", 1, medido(7))
	rel.ir(setembro)
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 7 {
		t.Fatalf("a liquidacao de outubro devia valer contra a reserva de setembro: gasto %d, esperava 7", g.Tokens)
	}
}

// TestAOS466ALiquidacaoPorGeracao — quando a reserva se liberta, e quando não.
func TestAOS466ALiquidacaoPorGeracao(t *testing.T) {
	type parcela struct {
		geracao int
		consumo consumoDoPlaneamento
	}
	naoMedido := func(tokens int64) consumoDoPlaneamento {
		return consumoDoPlaneamento{Tokens: tokens, CustoMedido: true}
	}
	// entregues: até que geração o nó entregou; fecho: o pedido terminou (desfecho terminal no log).
	casos := []struct {
		nome      string
		entregues int
		parcelas  []parcela
		fecho     bool
		gasto     int64
	}{
		{"antes de qualquer entrega conta a reserva", 0, nil, false, 100},
		{"uma geracao a correr conta a reserva", 1, nil, false, 100},
		{"duas geracoes entregues sem desfecho contam duas reservas", 2, nil, false, 200},
		{"transitoria abaixo da reserva nao liberta", 1, []parcela{{1, medido(30)}}, false, 100},
		{"transitoria acima da reserva conta o excesso", 1, []parcela{{1, medido(150)}}, false, 150},
		{"terminal medida liberta o resto", 1, []parcela{{1, medido(30)}}, true, 30},
		{"terminal medida acima da reserva", 1, []parcela{{1, medido(170)}}, true, 170},
		{"as geracoes somam", 2, []parcela{{1, medido(30)}, {2, medido(40)}}, true, 70},
		{"terminal nao medida custa o medido mais a reserva", 1, []parcela{{1, naoMedido(30)}}, true, 130},
		{"uma geracao nao medida custa a reserva", 2, []parcela{{1, naoMedido(0)}, {2, medido(40)}}, true, 140},
		{"geracao entregue sem parcela custa a reserva", 2, []parcela{{2, medido(40)}}, true, 140},
		{"entregue depois da ultima parcela custa a reserva", 3, []parcela{{1, medido(40)}}, true, 240},
		{"a mesma geracao duas vezes conta uma", 1, []parcela{{1, medido(150)}, {1, medido(150)}}, false, 150},
		{"consumo negativo nao desconta", 1, []parcela{{1, medido(-500)}}, true, 0},
		// Achado MÉDIO-1 da revisão: com Σ acima da reserva, uma geração não medida somava zero.
		{"nao medida acima da reserva ainda soma", 2, []parcela{{1, medido(500)}, {2, naoMedido(0)}}, true, 600},
		{"aos-orq anterior: cinco geracoes sem consumo", 5, []parcela{{1, naoMedido(0)}, {2, naoMedido(0)}, {3, naoMedido(0)}, {4, naoMedido(0)}, {5, naoMedido(0)}}, true, 500},
		// Achado MÉDIO-2: sem fecho não se liberta, e a lacuna do meio conta mesmo com o pedido vivo.
		{"lacuna com o pedido vivo", 3, []parcela{{1, medido(10)}, {3, medido(5)}}, false, 115},
		// Achado MÉDIO da re-revisão: a geração em curso conta mesmo com Σ acima da reserva.
		{"a geracao em curso conta acima da reserva", 2, []parcela{{1, medido(500)}}, false, 600},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ctx := context.Background()
			rel := &relogioDeQuota{t: setembro}
			q := quotaComPlaneamento(novoStore(t), 10_000, 100, 100, rel)
			if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
				t.Fatal(err)
			}
			for g := 1; g <= c.entregues; g++ {
				if err := q.registarEntrega(ctx, "human:alice", "plano-1", g, true); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range c.parcelas {
				if err := q.registarPlaneamento(ctx, "human:alice", "plano-1", p.geracao, p.consumo); err != nil {
					t.Fatalf("registar geracao %d: %v", p.geracao, err)
				}
			}
			if c.fecho {
				if err := q.fecharPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
					t.Fatal(err)
				}
			}
			if g := gastoDe(t, q, "human:alice"); g.Tokens != c.gasto {
				t.Fatalf("gasto %d, esperava %d", g.Tokens, c.gasto)
			}
		})
	}
}

// fechar regista a entrega, a parcela de uma geração terminal e o fecho do pedido.
func fechar(t *testing.T, q *quotaPorPrincipal, runID string, g int, c consumoDoPlaneamento) {
	t.Helper()
	ctx := context.Background()
	if err := q.registarEntrega(ctx, "human:alice", runID, g, true); err != nil {
		t.Fatal(err)
	}
	if err := q.registarPlaneamento(ctx, "human:alice", runID, g, c); err != nil {
		t.Fatal(err)
	}
	if err := q.fecharPlaneamento(ctx, "human:alice", runID); err != nil {
		t.Fatal(err)
	}
}

// TestAOS466OsDolaresDoPlaneamentoFicamPelaReserva — o `aos-orq` não mede dólares: com chamadas ao
// modelo, a dimensão em dólares fica pela reserva mesmo no terminal; sem chamadas, é zero medido.
func TestAOS466OsDolaresDoPlaneamentoFicamPelaReserva(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		nome        string
		custoMedido bool
		custo       int64
	}{
		{"com chamadas ao modelo", false, 50},
		{"sem chamadas ao modelo", true, 0},
	} {
		t.Run(c.nome, func(t *testing.T) {
			rel := &relogioDeQuota{t: setembro}
			q := quotaComPlaneamento(novoStore(t), 10_000, 100, 100, rel)
			q.limite.CostMicroUSD = 10_000
			q.reservaDoPlano.CostMicroUSD = 50
			if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
				t.Fatal(err)
			}
			consumo := consumoDoPlaneamento{Tokens: 30, TokensMedidos: true, CustoMedido: c.custoMedido}
			fechar(t, q, "plano-1", 1, consumo)
			g := gastoDe(t, q, "human:alice")
			if g.Tokens != 30 || g.CostMicroUSD != c.custo {
				t.Fatalf("gasto %+v, esperava 30 tokens e %d micro-USD", g, c.custo)
			}
		})
	}
}

// TestAOS466UmaParcelaDeOutraReservaNaoVale — a corrida entre réplicas que o vínculo à reserva
// fecha: uma parcela escrita depois de um apagamento, contra a reserva ANTERIOR a ele, não pode
// liquidar a reserva nova do mesmo pedido. Reproduz-se escrevendo os eventos pela ordem da corrida.
func TestAOS466UmaParcelaDeOutraReservaNaoVale(t *testing.T) {
	ctx := context.Background()
	rel := &relogioDeQuota{t: setembro}
	es := novoStore(t)
	q := quotaComPlaneamento(es, 10_000, 100, 100, rel)
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	st, err := q.ler(ctx, quotaStreamDe("human:alice", mesUTC(setembro)))
	if err != nil {
		t.Fatal(err)
	}
	antiga := st.reservas[chaveDoPlano("plano-1")].Reserva
	if err := q.Shred("human:alice"); err != nil {
		t.Fatal(err)
	}
	// A outra réplica leu antes do apagamento e escreve agora, contra a reserva antiga, uma parcela
	// medida, o fecho e uma entrega.
	for i, p := range []quotaPayload{
		{RunID: chaveDoPlano("plano-1"), Tokens: 500, Geracao: 2, ReservaDoPlano: antiga},
		{RunID: chaveDoPlano("plano-1"), ReservaDoPlano: antiga, Final: true},
		{RunID: chaveDoPlano("plano-1"), Geracao: 3, ReservaDoPlano: antiga, Entregue: true},
	} {
		bruto, _ := json.Marshal(p)
		if _, err := es.Append(ctx, quotaStreamDe("human:alice", mesUTC(setembro)), eventstore.EventInput{
			Type: EventTypeQuotaSettled, Payload: bruto, RunID: quotaRunID, StepID: "corrida-" + strconv.Itoa(i),
			Producer: eventstore.Producer{NHIID: quotaNHI},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	// A geração 1 da reserva NOVA, ainda sem fecho: o pedido custa pelo menos a reserva. Se o fecho
	// antigo valesse, custaria só estes 5; se a parcela antiga valesse, 505; se a entrega antiga
	// valesse, 205.
	if err := q.registarPlaneamento(ctx, "human:alice", "plano-1", 1, medido(5)); err != nil {
		t.Fatal(err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("a parcela ou o fecho da reserva antiga valeram para a nova: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466AReservaDeMuitasGeracoesEmFaltaNaoDaAVolta — a reserva vezes as gerações em falta
// satura em vez de dar a volta para negativo (e caber na quota).
func TestAOS466AReservaDeMuitasGeracoesEmFaltaNaoDaAVolta(t *testing.T) {
	r := quotaPayload{Tokens: math.MaxInt64 / 2}
	c := cobrancaDoPlano(r, map[int]quotaPayload{}, 3, nil, true)
	if c.Tokens != math.MaxInt64 {
		t.Fatalf("tres geracoes em falta de MaxInt64/2 deviam saturar, deram %d", c.Tokens)
	}
	if produtoSaturado(7, 3) != 21 || produtoSaturado(7, 0) != 0 || produtoSaturado(0, 5) != 0 {
		t.Fatal("produtoSaturado errado nos casos pequenos")
	}
}

// TestAOS466UmaReplicaAnteriorContaAReservaInteira — num deploy rolante, uma réplica anterior a
// este ticket lê as parcelas como liquidações. Com `Reserva` a zero, não as reconhece e conta a
// reserva inteira (o lado seguro); se a parcela usasse `Reserva`, contaria só a ÚLTIMA geração.
func TestAOS466UmaReplicaAnteriorContaAReservaInteira(t *testing.T) {
	ctx := context.Background()
	rel := &relogioDeQuota{t: setembro}
	q := quotaComPlaneamento(novoStore(t), 10_000, 100, 100, rel)
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	fechar(t, q, "plano-1", 1, medido(5))
	evs, err := q.es.Read(ctx, quotaStreamDe("human:alice", mesUTC(setembro)), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type != EventTypeQuotaSettled {
			continue
		}
		// A leitura de uma réplica anterior: só conhece os campos do AOS-457.
		var antigo struct {
			RunID   string `json:"run_id"`
			Reserva uint64 `json:"reserva"`
		}
		if err := json.Unmarshal(ev.Payload, &antigo); err != nil || antigo.RunID == "" {
			t.Fatalf("uma replica anterior nao le a parcela (negaria tudo): %v", err)
		}
		if antigo.Reserva != 0 {
			t.Fatalf("a parcela leva `reserva`=%d: uma replica anterior le-a como a liquidacao final", antigo.Reserva)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// As rotas.
// ---------------------------------------------------------------------------------------------

const drenador466 = "nhi:drenador-466"

// aos466No compõe o nó com a fila, o gate com credencial forte, o drenador listado e a quota.
func aos466No(t *testing.T, limite, plano int64) (*Node, http.Handler, *quotaPorPrincipal) {
	t.Helper()
	node, h := aos464No(t)
	node.PlanDrainers = map[string]bool{drenador466: true}
	q := quotaComPlaneamento(node.EventStore, limite, 100, plano, &relogioDeQuota{t: setembro})
	node.QuotaPorPrincipal = q
	return node, h, q
}

func pedidosNaFila(t *testing.T, node *Node) int {
	t.Helper()
	evs, err := node.EventStore.Read(context.Background(), planRequestStream, 0)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == EventTypePlanRequestSubmitted {
			n++
		}
	}
	return n
}

// TestAOS466PostPlansRecusaSemQuotaParaOPlaneamento — critério 2: 429 com Retry-After, e nada entra
// na fila. O CONTROLO é o primeiro pedido, que cabe.
func TestAOS466PostPlansRecusaSemQuotaParaOPlaneamento(t *testing.T) {
	node, h, q := aos466No(t, 150, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466-a"); rec.Code != http.StatusCreated {
		t.Fatalf("o primeiro pedido cabe (100 <= 150): %d %s", rec.Code, rec.Body.String())
	}
	rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466-b")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sem quota para a reserva de planeamento devia dar 429, veio %d %s", rec.Code, rec.Body.String())
	}
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra <= 0 {
		t.Fatalf("a recusa tem de dizer quando repoe (Retry-After), veio %q", rec.Header().Get("Retry-After"))
	}
	if strings.Contains(rec.Body.String(), "100") {
		t.Fatalf("o corpo nao leva os numeros do gasto: %s", rec.Body.String())
	}
	if n := pedidosNaFila(t, node); n != 1 {
		t.Fatalf("o pedido recusado entrou na fila: %d pedidos, esperava 1", n)
	}
	// O re-POST do pedido aceite continua a passar: a reserva é idempotente.
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466-a"); rec.Code != http.StatusCreated {
		t.Fatalf("o re-POST do pedido aceite devia continuar 201: %d", rec.Code)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("gasto %d, esperava 100", g.Tokens)
	}
	// Outro principal não é afectado.
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "plano-466-c"); rec.Code != http.StatusCreated {
		t.Fatalf("a quota de alice nao fecha a rota a bob: %d", rec.Code)
	}
}

// reclamarEReportar reclama como drenador e reporta o desfecho com o corpo dado.
func reclamarEReportar(t *testing.T, h http.Handler, corpo map[string]any) *httpResposta {
	t.Helper()
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	if rec := postReq(h, "/plans/claim", nil, dren); rec.Code != http.StatusOK {
		t.Fatalf("o drenador devia reclamar: %d %s", rec.Code, rec.Body.String())
	}
	rec := postReq(h, "/plans/outcome", corpo, dren)
	return &httpResposta{codigo: rec.Code, corpo: rec.Body.String()}
}

type httpResposta struct {
	codigo int
	corpo  string
}

// TestAOS466ODesfechoLiquidaContraQuemSubmeteu — critério 4 pela rota: a parcela vai para a quota
// de quem SUBMETEU o pedido, nunca para a do drenador que reporta.
func TestAOS466ODesfechoLiquidaContraQuemSubmeteu(t *testing.T) {
	_, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 30, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", r.codigo, r.corpo)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 30 {
		t.Fatalf("o terminal medido devia liquidar a reserva de alice para 30, veio %d", g.Tokens)
	}
	if g := gastoDe(t, q, drenador466); g.Tokens != 0 {
		t.Fatalf("o drenador nao paga o planeamento de ninguem: gasto %d", g.Tokens)
	}
}

// TestAOS466UmDesfechoTransitorioNaoLiberta — pela rota: um desfecho transitório (o pedido volta
// à fila e vai ser planeado de novo) com consumo medido abaixo da reserva NÃO a liberta — a geração
// seguinte ainda pode gastar. Só o terminal liberta.
func TestAOS466UmDesfechoTransitorioNaoLiberta(t *testing.T) {
	_, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTransitorio,
		"consumo": map[string]any{"tokens": 30, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", r.codigo, r.corpo)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("um transitorio libertou a reserva: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466UmaGeracaoEntregueSemDesfechoNaoLiberta — pela rota: a geração 1 foi ENTREGUE ao drenador
// (reclamada) e o desfecho dela nunca chegou; o terminal da geração 2 chega medido. A geração 1 pode
// ter gastado, e o nó sabe que a entregou — a reserva fica. O desfecho da geração 2 é reportado sem a
// reclamar, que é o que basta para a projecção (a reclamação dela, aqui, só mudaria o relógio).
func TestAOS466UmaGeracaoEntregueSemDesfechoNaoLiberta(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	if rec := postReq(h, "/plans/claim", nil, dren); rec.Code != http.StatusOK {
		t.Fatalf("reclamar: %d", rec.Code)
	}
	// O desfecho da geração 1 perdeu-se; a reclamação expirou e a 2 foi entregue.
	entregarAMao(t, node, q, "plano-466", 2)
	rec := postReq(h, "/plans/outcome", map[string]any{
		"run_id": "plano-466", "generation": 2, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 40, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	}, dren)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", rec.Code, rec.Body.String())
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 140 {
		t.Fatalf("a geracao 1 entregue e sem parcela ficou por contar: gasto %d, esperava 40 + a reserva (140)", g.Tokens)
	}
}

// entregarAMao faz o que a reclamação da geração g faz — a entrega na quota e a reclamação no log —
// sem esperar o TTL real (meia hora) da reclamação anterior.
func entregarAMao(t *testing.T, node *Node, q *quotaPorPrincipal, runID string, g int) {
	t.Helper()
	if err := q.registarEntrega(context.Background(), "human:alice", runID, g, true); err != nil {
		t.Fatal(err)
	}
	if _, err := node.EventStore.Append(context.Background(), planRequestStream, eventstore.EventInput{
		Type:     EventTypePlanRequestClaimed,
		Payload:  json.RawMessage(`{"v":"` + planRequestVersao + `","by":"` + drenador466 + `"}`),
		RunID:    planRequestRunID,
		StepID:   prefixoReclamo + strconv.Itoa(g) + "-" + runID,
		Producer: eventstore.Producer{NHIID: planIngressNHI},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAOS466UmDesfechoAtrasadoContaAsGeracoesEntreguesDepois — o desfecho terminal da geração 1
// chega DEPOIS de o nó ter entregue a geração 2 (o reporte demorou, a reclamação expirou). A geração
// 2 está a planear e ainda não tem parcela: o fecho regista que o nó entregou até à 2, e ela custa a
// reserva. A reclamação da geração 2 escreve-se directamente no log, porque o TTL real da reclamação
// é de meia hora.
func TestAOS466UmDesfechoAtrasadoContaAsGeracoesEntreguesDepois(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	if rec := postReq(h, "/plans/claim", nil, dren); rec.Code != http.StatusOK {
		t.Fatalf("reclamar: %d", rec.Code)
	}
	entregarAMao(t, node, q, "plano-466", 2)
	rec := postReq(h, "/plans/outcome", map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 10, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	}, dren)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", rec.Code, rec.Body.String())
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 110 {
		t.Fatalf("a geracao 2 entregue ficou por contar: gasto %d, esperava 10 + a reserva (110)", g.Tokens)
	}
}

// TestAOS466AGeracaoEmCursoContaAcimaDaReserva — achado MÉDIO da re-revisão, pela rota: a geração 1
// mede 500 e volta à fila; a 2 é reclamada e está a planear. Sem a entrega na quota, o pedido
// custava 500 e a geração 2 corria sem reserva nenhuma.
func TestAOS466AGeracaoEmCursoContaAcimaDaReserva(t *testing.T) {
	_, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTransitorio,
		"consumo": map[string]any{"tokens": 500, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", r.codigo, r.corpo)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	rec := postReq(h, "/plans/claim", nil, dren)
	var p respostaDeReclamo
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Geracao != 2 {
		t.Fatalf("o transitorio devia voltar a fila e ser entregue na geracao 2: %d %s", rec.Code, rec.Body.String())
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 600 {
		t.Fatalf("a geracao 2 em curso nao conta: gasto %d, esperava 500 + a reserva (600)", g.Tokens)
	}
}

// TestAOS466UmDesfechoRepetidoNaoFecha — achado da re-revisão: a mesma geração reportada transitória
// e depois terminal. O log guarda o primeiro (o pedido volta à fila); o segundo é duplicado e NÃO
// pode fechar a quota.
func TestAOS466UmDesfechoRepetidoNaoFecha(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	consumo := map[string]any{"tokens": 30, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true}
	if r := reclamarEReportar(t, h, map[string]any{"run_id": "plano-466", "generation": 1, "classe": DesfechoTransitorio, "consumo": consumo}); r.codigo != http.StatusNoContent {
		t.Fatalf("transitorio: %d", r.codigo)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	if rec := postReq(h, "/plans/outcome", map[string]any{"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal, "consumo": consumo}, dren); rec.Code != http.StatusNoContent {
		t.Fatalf("repetido: %d", rec.Code)
	}
	est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-466", setembro)
	if err != nil || est.resposta.Estado == EstadoPlanoTerminado {
		t.Fatalf("o log devia guardar o transitorio: %+v %v", est.resposta, err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("o desfecho repetido fechou a quota de um pedido vivo: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466UmaGeracaoNuncaEntregueE400 — achado da re-revisão: com a quota composta, um desfecho só
// liquida uma geração que o nó entregou. Uma geração arbitrária (10⁹) negaria o mês do titular.
func TestAOS466UmaGeracaoNuncaEntregueE400(t *testing.T) {
	_, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{"run_id": "plano-466", "generation": 1_000_000_000, "classe": DesfechoTransitorio})
	if r.codigo != http.StatusBadRequest {
		t.Fatalf("uma geracao nunca entregue devia dar 400, veio %d %s", r.codigo, r.corpo)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("a geracao nunca entregue mexeu na quota: gasto %d", g.Tokens)
	}
}

// TestAOS466SemEntregaNaQuotaNaoHaReclamacao — a entrega fica na quota ANTES da reclamação: se não
// se grava, o drenador não recebe o pedido (503) — uma geração que planeasse sem constar da quota
// seria planeamento de graça.
func TestAOS466SemEntregaNaQuotaNaoHaReclamacao(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	q.es = &storeQueFalha466{EventStorePort: node.EventStore, falha: func(_ string, in eventstore.EventInput) bool {
		return strings.Contains(in.StepID, ":delivered:")
	}}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	if rec := postReq(h, "/plans/claim", nil, dren); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sem a entrega na quota a reclamacao devia dar 503, veio %d %s", rec.Code, rec.Body.String())
	}
	est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-466", setembro)
	if err != nil || len(est.reclamadas) != 0 {
		t.Fatalf("a reclamacao ficou no log sem a entrega na quota: %v %v", est.reclamadas, err)
	}
}

// TestAOS466AQuotaIlegivelDeUmTitularNaoFechaAFilaAosOutros — o stream de quota de alice fica
// ilegível depois de ela submeter; o pedido de bob, atrás do dela na fila, continua a ser entregue.
func TestAOS466AQuotaIlegivelDeUmTitularNaoFechaAFilaAosOutros(t *testing.T) {
	node, h, _ := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-alice"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao de alice: %d", rec.Code)
	}
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "plano-bob"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao de bob: %d", rec.Code)
	}
	if _, err := node.EventStore.Append(context.Background(), quotaStreamDe("human:alice", mesUTC(setembro)), eventstore.EventInput{
		Type: "budget.quota.desconhecido", Payload: json.RawMessage(`{}`), RunID: quotaRunID, StepID: "estraga",
		Producer: eventstore.Producer{NHIID: quotaNHI},
	}); err != nil {
		t.Fatal(err)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	rec := postReq(h, "/plans/claim", nil, dren)
	var p respostaDeReclamo
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.RunID != "plano-bob" {
		t.Fatalf("o pedido de bob devia ser entregue apesar da quota ilegivel de alice: %d %s", rec.Code, rec.Body.String())
	}
	est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-alice", setembro)
	if err != nil || len(est.reclamadas) != 0 {
		t.Fatalf("o pedido de alice foi entregue sem entrega na quota: %v %v", est.reclamadas, err)
	}
}

// TestAOS466UmaReofertaSoSeEntregaComQuota — decisão do dono depois da terceira revisão: a geração
// 1 está coberta pela reserva do `POST /plans` e entrega-se sempre; da 2 em diante a entrega exige
// quota do mês corrente para mais uma reserva. A repetição de uma entrega já feita passa.
func TestAOS466UmaReofertaSoSeEntregaComQuota(t *testing.T) {
	ctx := context.Background()
	rel := &relogioDeQuota{t: setembro}
	q := quotaComPlaneamento(novoStore(t), 300, 100, 100, rel)
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	if err := q.registarEntrega(ctx, "human:alice", "plano-1", 2, true); err != nil {
		t.Fatalf("200 + 100 <= 300: a geracao 2 entrega-se: %v", err)
	}
	if err := q.reservar(ctx, "human:alice", "run-a"); err != nil {
		t.Fatal(err)
	}
	// Gasto 300 (pedido 200 + run 100): nada cabe.
	if err := q.registarEntrega(ctx, "human:alice", "plano-1", 3, true); !errors.Is(err, ErrPrincipalQuotaExhausted) {
		t.Fatalf("a geracao 3 sem quota nao se entrega, veio %v", err)
	}
	if err := q.registarEntrega(ctx, "human:alice", "plano-1", 2, true); err != nil {
		t.Fatalf("repetir a entrega ja feita da geracao 2 passa: %v", err)
	}
	if err := q.registarEntrega(ctx, "human:alice", "plano-1", 1, true); err != nil {
		t.Fatalf("a geracao 1 esta coberta pela reserva do pedido e entrega-se sempre: %v", err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 300 {
		t.Fatalf("a recusa escreveu uma entrega: gasto %d, esperava 300", g.Tokens)
	}
}

// TestAOS466UmaReofertaSemQuotaFicaPendente — pela rota: a geração 2 esgota a quota; a re-oferta da
// 3 não se entrega (o drenador recebe o pedido de outro titular), e volta a entregar-se quando a quota
// repõe no mês seguinte.
func TestAOS466UmaReofertaSemQuotaFicaPendente(t *testing.T) {
	node, h, q := aos466No(t, 700, 100)
	rel := &relogioDeQuota{t: setembro}
	q.agora = rel.agora
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-alice"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao de alice: %d", rec.Code)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	transitorio := func(g int, tokens int64) {
		t.Helper()
		rec := postReq(h, "/plans/claim", nil, dren)
		var p respostaDeReclamo
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.RunID != "plano-alice" || p.Geracao != g {
			t.Fatalf("esperava entregar plano-alice na geracao %d: %d %s", g, rec.Code, rec.Body.String())
		}
		if rec := postReq(h, "/plans/outcome", map[string]any{"run_id": "plano-alice", "generation": g, "classe": DesfechoTransitorio,
			"consumo": map[string]any{"tokens": tokens, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true}}, dren); rec.Code != http.StatusNoContent {
			t.Fatalf("desfecho %d: %d", g, rec.Code)
		}
	}
	transitorio(1, 500) // gasto 500
	transitorio(2, 300) // 500 + 100 <= 700 entregou; gasto 800 > 700

	if rec := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "plano-bob"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao de bob: %d", rec.Code)
	}
	rec := postReq(h, "/plans/claim", nil, dren)
	var p respostaDeReclamo
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.RunID != "plano-bob" {
		t.Fatalf("a re-oferta de alice sem quota nao se entrega, e bob e servido: %d %s", rec.Code, rec.Body.String())
	}
	est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-alice", setembro)
	if err != nil || est.resposta.Estado == EstadoPlanoTerminado || slices.Contains(est.reclamadas, 3) {
		t.Fatalf("o pedido de alice fica pendente, sem a geracao 3: %+v %v %v", est.resposta, est.reclamadas, err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 800 {
		t.Fatalf("a recusa mexeu na quota: gasto %d, esperava 800", g.Tokens)
	}

	// A quota repõe no mês seguinte: a re-oferta volta a entregar-se, e conta no mês do pedido.
	rel.ir(setembro.AddDate(0, 1, 0))
	rec = postReq(h, "/plans/claim", nil, dren)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.RunID != "plano-alice" || p.Geracao != 3 {
		t.Fatalf("com a quota reposta a geracao 3 entrega-se: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAOS466UmDesfechoSemConsumoNaoLiberta — um `aos-orq` anterior não envia `consumo`: vale como
// não medido, e a reserva fica inteira.
func TestAOS466UmDesfechoSemConsumoNaoLiberta(t *testing.T) {
	_, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", r.codigo, r.corpo)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("sem consumo declarado a reserva fica inteira: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466ConsumoNegativoE400 — um consumo negativo desconta quota a outro; é recusado antes de
// qualquer escrita.
func TestAOS466ConsumoNegativoE400(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	if rec := postReq(h, "/plans/claim", nil, dren); rec.Code != http.StatusOK {
		t.Fatalf("reclamar: %d", rec.Code)
	}
	for _, consumo := range []map[string]any{
		{"tokens": -1, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
		{"tokens": 1, "tokens_medidos": true, "cost_micro_usd": -1, "custo_medido": true},
	} {
		rec := postReq(h, "/plans/outcome", map[string]any{"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal, "consumo": consumo}, dren)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("consumo %v devia dar 400, veio %d %s", consumo, rec.Code, rec.Body.String())
		}
		est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-466", setembro)
		if err != nil || est.resposta.Estado == EstadoPlanoTerminado {
			t.Fatalf("o desfecho recusado ficou gravado: %+v %v", est.resposta, err)
		}
		if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
			t.Fatalf("o consumo recusado mexeu na quota: gasto %d", g.Tokens)
		}
	}
}

// storeQueFalhaNaQuota falha as escritas de PARCELAS nos streams da quota quando ligado.
type storeQueFalhaNaQuota struct {
	eventstore.EventStore
	falhar atomic.Bool
}

func (s *storeQueFalhaNaQuota) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.falhar.Load() && strings.HasPrefix(stream, quotaStreamPrefix) && strings.Contains(in.StepID, ":settled:") {
		return eventstore.AppendResult{}, errors.New("falha injectada no stream da quota")
	}
	return s.EventStore.Append(ctx, stream, in, opts...)
}

// TestAOS466SemParcelaNaoHaDesfecho — se a parcela não se grava, o desfecho também não: o drenador
// vê 503. Na ordem inversa uma geração que gastou ficava contada no pedido e ausente da quota.
func TestAOS466SemParcelaNaoHaDesfecho(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	falhador := &storeQueFalhaNaQuota{EventStore: node.EventStore}
	q.es = falhador
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	falhador.falhar.Store(true)
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 1, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusServiceUnavailable {
		t.Fatalf("sem parcela o desfecho devia dar 503, veio %d %s", r.codigo, r.corpo)
	}
	est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-466", setembro)
	if err != nil || est.resposta.Estado == EstadoPlanoTerminado {
		t.Fatalf("o desfecho ficou gravado sem a parcela: %+v %v", est.resposta, err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("gasto %d, esperava a reserva inteira", g.Tokens)
	}
}

// storeQueFalha466 falha os `Append` que o predicado escolhe, e delega o resto.
type storeQueFalha466 struct {
	EventStorePort
	falha func(stream string, in eventstore.EventInput) bool
}

func (s *storeQueFalha466) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.falha(stream, in) {
		return eventstore.AppendResult{}, errors.New("falha injectada")
	}
	return s.EventStorePort.Append(ctx, stream, in, opts...)
}

// TestAOS466UmDesfechoQueFalhaNaoLibertaAReserva — achado MÉDIO-2 da revisão adversarial, pela rota:
// a parcela grava-se, o desfecho terminal NÃO (o drenador vê 503, o pedido continua vivo). A reserva
// não se pode libertar — a primeira versão marcava a parcela como final antes do desfecho, e o
// gasto caía para o consumo desta geração com o pedido ainda a ser planeado.
func TestAOS466UmDesfechoQueFalhaNaoLibertaAReserva(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	node.EventStore = &storeQueFalha466{EventStorePort: node.EventStore, falha: func(stream string, in eventstore.EventInput) bool {
		return stream == planRequestStream && in.Type == EventTypePlanRequestOutcome
	}}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 10, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusServiceUnavailable {
		t.Fatalf("o desfecho falhado devia dar 503, veio %d %s", r.codigo, r.corpo)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("a reserva libertou-se com o pedido vivo: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466UmFechoQueFalhaDeixaAReservaInteira — o desfecho terminal está no log e o fecho da quota
// falha: 204 (o drenador tem de avisar o fim do plano, que é verdade), e a reserva fica inteira — a
// mais, nunca a menos.
func TestAOS466UmFechoQueFalhaDeixaAReservaInteira(t *testing.T) {
	node, h, q := aos466No(t, 10_000, 100)
	q.es = &storeQueFalha466{EventStorePort: node.EventStore, falha: func(_ string, in eventstore.EventInput) bool {
		return strings.Contains(in.StepID, ":closed:")
	}}
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-466", "generation": 1, "classe": DesfechoTerminal,
		"consumo": map[string]any{"tokens": 10, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("o desfecho esta no log: 204, veio %d %s", r.codigo, r.corpo)
	}
	est, _, err := estadoDoPedido(context.Background(), node.EventStore, "plano-466", setembro)
	if err != nil || est.resposta.Estado != EstadoPlanoTerminado {
		t.Fatalf("o desfecho terminal devia estar no log: %+v %v", est.resposta, err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("sem fecho a reserva fica inteira: gasto %d, esperava 100", g.Tokens)
	}
}

// TestAOS466AQuotaIlegivelNaoAdmitePlaneamento — uma quota que não se lê não admite planeamento às
// cegas: 503, e o pedido não entra na fila.
func TestAOS466AQuotaIlegivelNaoAdmitePlaneamento(t *testing.T) {
	node, h, _ := aos466No(t, 10_000, 100)
	bruto := json.RawMessage(`{}`)
	if _, err := node.EventStore.Append(context.Background(), quotaStreamDe("human:alice", mesUTC(setembro)), eventstore.EventInput{
		Type: "budget.quota.desconhecido", Payload: bruto, RunID: quotaRunID, StepID: "x",
		Producer: eventstore.Producer{NHIID: quotaNHI},
	}); err != nil {
		t.Fatal(err)
	}
	rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-466")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("quota ilegivel devia dar 503, veio %d", rec.Code)
	}
	if n := pedidosNaFila(t, node); n != 0 {
		t.Fatalf("o pedido entrou na fila sem reserva: %d", n)
	}
}

// TestAOS466SemCredencialForteNaoReservaContraONomeEscrito — a asserção do invariante do AOS-457: a
// quota composta num gate sem credencial forte (o `Bootstrap` não o deixa, mas o handler não o pode
// assumir) recusa, em vez de reservar contra um principal que o chamador escreveu no header.
func TestAOS466SemCredencialForteNaoReservaContraONomeEscrito(t *testing.T) {
	node, h := noComFila(t)
	q := quotaComPlaneamento(node.EventStore, 10_000, 100, 100, &relogioDeQuota{t: setembro})
	node.QuotaPorPrincipal = q
	rec := postReq(h, "/plans", map[string]any{"run_id": "plano-466", "objective": "o"}, euReaderHeaders())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sem credencial forte devia recusar 503, veio %d %s", rec.Code, rec.Body.String())
	}
	if g := gastoDe(t, q, govReader); g.Tokens != 0 {
		t.Fatalf("reservou contra o nome escrito no header: %d", g.Tokens)
	}
	if n := pedidosNaFila(t, node); n != 0 {
		t.Fatalf("o pedido entrou na fila: %d", n)
	}
}

// TestAOS466OBannerDizOQueContaEOQueNao — critério 6.
func TestAOS466OBannerDizOQueContaEOQueNao(t *testing.T) {
	q := quotaComPlaneamento(novoStore(t), 1000, 100, 40, &relogioDeQuota{t: setembro})
	q.limite.CostMicroUSD = integration.UnlimitedCostMicroUSD
	b := strings.Join(principalQuotaPostureBanner(q), "\n")
	if strings.Contains(b, "NAO cobre o gasto de PLANEAMENTO") {
		t.Fatal("o banner ainda diz que o planeamento nao conta")
	}
	for _, deve := range []string{"PLANEAMENTO", "RESERVA 40 tokens", "AOS-466", "nunca sao medidos", "AOS-467"} {
		if !strings.Contains(b, deve) {
			t.Fatalf("o banner nao diz %q:\n%s", deve, b)
		}
	}
}

// TestAOS467AsEntregasDeFechoNaoCustam — o achado MÉDIO da terceira revisão do AOS-467: cada re-entrega
// de uma geração marcada (a reclamação expira, a geração volta) era uma geração sem parcela e custava a
// reserva para sempre, mesmo depois do fecho. A entrega de fecho não planeia e não custa.
func TestAOS467AsEntregasDeFechoNaoCustam(t *testing.T) {
	ctx := context.Background()
	q := quotaComPlaneamento(novoStore(t), 10_000, 100, 100, &relogioDeQuota{t: setembro})
	if err := q.reservarPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	if err := q.registarEntrega(ctx, "human:alice", "plano-1", 1, true); err != nil {
		t.Fatal(err)
	}
	if err := q.registarPlaneamento(ctx, "human:alice", "plano-1", 1, medido(30)); err != nil {
		t.Fatal(err)
	}
	// A 2, a 3 e a 4 são entregas de FECHO (marcadas) que expiraram sem desfecho; a 4 fecha com 12.
	for g := 2; g <= 4; g++ {
		if err := q.registarEntrega(ctx, "human:alice", "plano-1", g, false); err != nil {
			t.Fatal(err)
		}
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 100 {
		t.Fatalf("antes do fecho o pedido custa max(reserva, 30): %d, esperava 100", g.Tokens)
	}
	if err := q.registarPlaneamento(ctx, "human:alice", "plano-1", 4, medido(0)); err != nil {
		t.Fatal(err)
	}
	if err := q.fecharPlaneamento(ctx, "human:alice", "plano-1"); err != nil {
		t.Fatal(err)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens != 30 {
		t.Fatalf("as entregas de fecho nao custam: gasto %d, esperava 30", g.Tokens)
	}
}
