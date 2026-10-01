package main

// AOS-467 — O TECTO DE GERAÇÕES DE PLANEAMENTO POR PEDIDO: o nó decide, o consumidor fecha.
//
// Um desfecho transitório devolve o pedido à fila na geração seguinte (ADR-030 §2.6), e até aqui sem
// limite: um pedido cuja decomposição falha sempre de forma transitória re-planeava sem fim. Estes
// testes fixam o que conta para o tecto (as re-verificações de um plano à espera de humano não
// contam), que a geração que o passa se entrega MARCADA — sem quota e mesmo de objectivo ilegível —,
// e que o drenador a fecha como terminal.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS467OAmbienteDoTecto — default 5; um inteiro > 0 afina; 0, negativo ou ilegível aborta.
func TestAOS467OAmbienteDoTecto(t *testing.T) {
	casos := []struct {
		raw   string
		quer  int
		falha bool
	}{
		{"", DefaultPlanMaxGenerations, false},
		{"3", 3, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"cinco", 0, true},
	}
	for _, c := range casos {
		t.Run("valor="+c.raw, func(t *testing.T) {
			clearIngressEnv(t)
			t.Setenv("AOS_PLAN_MAX_GENERATIONS", c.raw)
			lim, _, err := ingressLimitsFromEnv()
			if c.falha {
				if !errors.Is(err, ErrBadIngressLimits) {
					t.Fatalf("AOS_PLAN_MAX_GENERATIONS=%q devia abortar, veio %v", c.raw, err)
				}
				return
			}
			if err != nil || lim.planMaxGenerations != c.quer {
				t.Fatalf("AOS_PLAN_MAX_GENERATIONS=%q: tecto %d (%v), esperava %d", c.raw, lim.planMaxGenerations, err, c.quer)
			}
		})
	}
	if DefaultPlanMaxGenerations != 5 {
		t.Fatalf("o default decidido pelo dono e 5, e o codigo diz %d", DefaultPlanMaxGenerations)
	}
}

// TestAOS467OQueContaParaOTecto — a contagem vive na projecção, função pura. As re-verificações de um
// plano à espera de humano não contam; as re-ofertas depois de um transitório e de uma reclamação
// expirada contam.
func TestAOS467OQueContaParaOTecto(t *testing.T) {
	agora := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	antes := agora.Add(-2 * time.Hour) // reclamações expiradas, re-verificações já vencidas
	casos := []struct {
		nome     string
		eventos  []eventstore.Event
		geracao  int
		contadas int
	}{
		{"pedido novo", []eventstore.Event{evSubmetido(1, "p", "")}, 1, 1},
		{"dois transitorios", []eventstore.Event{
			evSubmetido(1, "p", ""),
			evReclamado(2, "p", 1, antes), evDesfechoEm(3, "p", 1, DesfechoTransitorio, antes),
			evReclamado(4, "p", 2, antes), evDesfechoEm(5, "p", 2, DesfechoTransitorio, antes),
		}, 3, 3},
		{"reclamacao expirada conta", []eventstore.Event{
			evSubmetido(1, "p", ""), evReclamado(2, "p", 1, antes),
		}, 2, 2},
		{"re-verificacao nao conta", []eventstore.Event{
			evSubmetido(1, "p", ""),
			evReclamado(2, "p", 1, antes), evDesfechoEm(3, "p", 1, DesfechoAguardaHumano, antes),
		}, 2, 1},
		{"muitas re-verificacoes nao contam", []eventstore.Event{
			evSubmetido(1, "p", ""),
			evDesfechoEm(2, "p", 1, DesfechoAguardaHumano, antes),
			evDesfechoEm(3, "p", 2, DesfechoAguardaHumano, antes),
			evDesfechoEm(4, "p", 3, DesfechoAguardaHumano, antes),
			evDesfechoEm(5, "p", 4, DesfechoAguardaHumano, antes),
		}, 5, 1},
		{"transitorio depois da aprovacao conta a seguinte", []eventstore.Event{
			evSubmetido(1, "p", ""),
			evDesfechoEm(2, "p", 1, DesfechoAguardaHumano, antes),
			evDesfechoEm(3, "p", 2, DesfechoAguardaHumano, antes),
			evDesfechoEm(4, "p", 3, DesfechoTransitorio, antes),
		}, 4, 2},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			fila := projectarFila(c.eventos, agora)
			if len(fila) != 1 {
				t.Fatalf("esperava o pedido na fila, veio %+v", fila)
			}
			if fila[0].Geracao != c.geracao || fila[0].GeracoesContadas != c.contadas {
				t.Fatalf("geracao %d contadas %d, esperava %d e %d", fila[0].Geracao, fila[0].GeracoesContadas, c.geracao, c.contadas)
			}
		})
	}
}

// TestAOS467UmaGeracaoEnormeNaoEUmLaco — um desfecho reportado para uma geração arbitrária não faz
// da projecção um laço até ela: a contagem desconta pelo mapa dos desfechos.
func TestAOS467UmaGeracaoEnormeNaoEUmLaco(t *testing.T) {
	agora := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	g := math.MaxInt32
	ev := []eventstore.Event{evSubmetido(1, "p", ""), evDesfechoEm(2, "p", g, DesfechoTransitorio, agora.Add(-time.Hour))}
	inicio := time.Now()
	fila := projectarFila(ev, agora)
	if d := time.Since(inicio); d > time.Second {
		t.Fatalf("a projeccao demorou %v com uma geracao de %d", d, g)
	}
	if len(fila) != 1 || fila[0].GeracoesContadas != g+1 {
		t.Fatalf("contagem errada: %+v", fila)
	}
}

// noComFilaETecto é o [noComFila] com o tecto de gerações afinado.
func noComFilaETecto(t *testing.T, tecto int) (*Node, http.Handler) {
	t.Helper()
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM), WithPlanMaxGenerations(tecto))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return node, h
}

func reclamar467(t *testing.T, h http.Handler) respostaDeReclamo {
	t.Helper()
	rec := postReq(h, "/plans/claim", nil, euReaderHeaders())
	var p respostaDeReclamo
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil {
		t.Fatalf("reclamacao: %d %s", rec.Code, rec.Body.String())
	}
	return p
}

func reportar467(t *testing.T, h http.Handler, runID string, g int, classe string, codigo int) {
	t.Helper()
	rec := postReq(h, "/plans/outcome", map[string]any{"run_id": runID, "generation": g, "classe": classe, "codigo_saida": codigo}, euReaderHeaders())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("desfecho %d: %d %s", g, rec.Code, rec.Body.String())
	}
}

// TestAOS467AGeracaoQuePassaOTectoEntregaSeMarcada — pela rota, com o tecto a 2: as gerações 1 e 2
// entregam-se para planear; a 3 entrega-se MARCADA; o desfecho terminal (12) que o drenador escreve
// fecha o pedido, e ele não volta a ser oferecido.
func TestAOS467AGeracaoQuePassaOTectoEntregaSeMarcada(t *testing.T) {
	node, h := noComFilaETecto(t, 2)
	if rec := postReq(h, "/plans", map[string]any{"run_id": "plano-467", "objective": "o"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	for g := 1; g <= 2; g++ {
		p := reclamar467(t, h)
		if p.Geracao != g || p.GeracoesEsgotadas || p.Objective == "" {
			t.Fatalf("a geracao %d planeia: %+v", g, p)
		}
		reportar467(t, h, "plano-467", g, DesfechoTransitorio, 1)
	}
	// O nome do campo escrito à MÃO: o `aos-orq` lê `generations_exhausted`, e descodificar para o
	// mesmo struct deixaria passar uma tag mudada (o gémeo do lado do aos-orq é o
	// TestAOS467AMarcaVemComONomeQueONoEscreve).
	rec := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"generations_exhausted":true`) || !strings.Contains(rec.Body.String(), `"generation":3`) {
		t.Fatalf("a geracao 3 passa o tecto (2) e entrega-se marcada: %d %s", rec.Code, rec.Body.String())
	}
	reportar467(t, h, "plano-467", 3, DesfechoTerminal, 12)
	if rec := postReq(h, "/plans/claim", nil, euReaderHeaders()); rec.Code != http.StatusNoContent {
		t.Fatalf("o pedido fechado nao volta a ser oferecido: %d %s", rec.Code, rec.Body.String())
	}
	est, achado, err := estadoDoPedido(context.Background(), node.EventStore, "plano-467", time.Now())
	if err != nil || !achado || est.resposta.Estado != EstadoPlanoTerminado || est.resposta.CodigoSaida != 12 {
		t.Fatalf("o pedido devia estar terminado com o codigo 12: %+v %v", est.resposta, err)
	}
}

// TestAOS467ODefaultEO5 — sem afinar, o tecto é o decidido pelo dono: a 6.ª geração contada é a
// marcada. Fixa que o handler compõe o default, e não «sem tecto».
func TestAOS467ODefaultEO5(t *testing.T) {
	_, h := noComFila(t)
	if rec := postReq(h, "/plans", map[string]any{"run_id": "plano-467", "objective": "o"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	for g := 1; g <= 5; g++ {
		if p := reclamar467(t, h); p.GeracoesEsgotadas {
			t.Fatalf("a geracao %d esta dentro do tecto por omissao (5): %+v", g, p)
		}
		reportar467(t, h, "plano-467", g, DesfechoTransitorio, 1)
	}
	if p := reclamar467(t, h); p.Geracao != 6 || !p.GeracoesEsgotadas {
		t.Fatalf("a geracao 6 passa o tecto por omissao: %+v", p)
	}
}

// TestAOS467UmPedidoIlegivelFechaPeloTecto — o resíduo do AOS-442: um pedido de objectivo ilegível
// era re-reclamado a cada expiração para sempre, porque a reclamação o saltava sem entregar. Ao passar
// o tecto entrega-se MARCADO e SEM objectivo, para o drenador o fechar.
//
// A geração 1 fecha-se aqui com um desfecho transitório escrito à mão, e não por expiração: o TTL
// real da reclamação é de uma hora, e o que conta para o tecto já está provado sozinho no
// [TestAOS467OQueContaParaOTecto]. O que se prova aqui é o ramo do HANDLER.
func TestAOS467UmPedidoIlegivelFechaPeloTecto(t *testing.T) {
	node, h := noComFilaETecto(t, 1)
	ctx := context.Background()
	morto, _ := json.Marshal(planRequestPayload{
		Versao: planRequestVersao, RunID: "run-467-morto", Principal: "human:apagado",
		ObjetivoSelado: []byte(`{"isto":"ja nao abre"}`),
	})
	for _, in := range []eventstore.EventInput{
		{Type: EventTypePlanRequestSubmitted, Payload: morto, StepID: prefixoPedido + "run-467-morto"},
		{Type: EventTypePlanRequestOutcome, Payload: json.RawMessage(`{"v":"` + planRequestVersao + `","run_id":"run-467-morto","classe":"transitorio"}`),
			StepID: prefixoDesfecho + "1-run-467-morto"},
	} {
		in.RunID, in.Producer = planRequestRunID, eventstore.Producer{NHIID: planIngressNHI}
		if _, err := node.EventStore.Append(ctx, planRequestStream, in); err != nil {
			t.Fatal(err)
		}
	}
	p := reclamar467(t, h)
	if p.RunID != "run-467-morto" || p.Geracao != 2 || !p.GeracoesEsgotadas || p.Objective != "" {
		t.Fatalf("o ilegivel que passa o tecto entrega-se marcado e sem objectivo: %+v", p)
	}
}

// TestAOS467AbaixoDoTectoOIlegivelContinuaASerSaltado — o CONTROLO do teste acima: abaixo do tecto,
// um ilegível continua a não ser entregue (sem objectivo não há o que planear).
func TestAOS467AbaixoDoTectoOIlegivelContinuaASerSaltado(t *testing.T) {
	node, h := noComFilaETecto(t, 5)
	ctx := context.Background()
	morto, _ := json.Marshal(planRequestPayload{
		Versao: planRequestVersao, RunID: "run-467-morto", Principal: "human:apagado",
		ObjetivoSelado: []byte(`{"isto":"ja nao abre"}`),
	})
	if _, err := node.EventStore.Append(ctx, planRequestStream, eventstore.EventInput{
		Type: EventTypePlanRequestSubmitted, Payload: morto, RunID: planRequestRunID,
		StepID: prefixoPedido + "run-467-morto", Producer: eventstore.Producer{NHIID: planIngressNHI},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := postReq(h, "/plans/claim", nil, euReaderHeaders()); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("abaixo do tecto o ilegivel nao se entrega (503 como antes), veio %d %s", rec.Code, rec.Body.String())
	}
}

// TestAOS467AEntregaDeFechoNaoExigeQuota — com a quota do AOS-466 esgotada, uma re-oferta comum fica
// pendente (TestAOS466UmaReofertaSemQuotaFicaPendente); a de FECHO entrega-se na mesma — não planeia,
// e exigir-lhe quota deixaria pendente até à reposição um pedido que só falta fechar.
func TestAOS467AEntregaDeFechoNaoExigeQuota(t *testing.T) {
	node, h := aos464No(t, WithPlanMaxGenerations(1))
	node.PlanDrainers = map[string]bool{drenador466: true}
	q := quotaComPlaneamento(node.EventStore, 150, 100, 100, &relogioDeQuota{t: setembro})
	node.QuotaPorPrincipal = q
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-467"); rec.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", rec.Code)
	}
	r := reclamarEReportar(t, h, map[string]any{
		"run_id": "plano-467", "generation": 1, "classe": DesfechoTransitorio,
		"consumo": map[string]any{"tokens": 500, "tokens_medidos": true, "cost_micro_usd": 0, "custo_medido": true},
	})
	if r.codigo != http.StatusNoContent {
		t.Fatalf("desfecho: %d %s", r.codigo, r.corpo)
	}
	if g := gastoDe(t, q, "human:alice"); g.Tokens <= 150 {
		t.Fatalf("o cenario exige a quota esgotada: gasto %d", g.Tokens)
	}
	dren := map[string]string{HeaderReaderPrincipal: drenador466, HeaderReaderBoard: govBoard}
	rec := postReq(h, "/plans/claim", nil, dren)
	var p respostaDeReclamo
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Geracao != 2 || !p.GeracoesEsgotadas {
		t.Fatalf("a geracao de fecho entrega-se com a quota esgotada, marcada: %d %s", rec.Code, rec.Body.String())
	}
}
