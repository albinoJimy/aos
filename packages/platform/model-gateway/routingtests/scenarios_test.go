package routingtests

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// ===========================================================================
// CENÁRIO 1 — SATURAÇÃO: selecção least-loaded/token-aware SEM colapso agregado.
// Orquestra router (AOS-059) + StaticLoadProvider + StaticAdmissionCoordinator
// (ADR-008). Prova (A) que o router escolhe a região de mais folga entre os
// sobreviventes intra-fronteira e (B) que, sob pressão do tecto GLOBAL partilhado,
// vários boards são ADIADOS em vez de estourarem colectivamente o rate limit.
// ===========================================================================

func TestScenario1_Saturation_LeastLoaded_NoAggregateCollapse(t *testing.T) {
	t.Parallel()

	// (A) LEAST-LOADED — a escolha segue a CARGA, não a ordem alfabética. Saturamos
	// eu-central (alfabeticamente o primeiro dos KeyIDs) e deixamos eu-west folgado:
	// o router tem de escolher eu-west (mais folga). Depois invertemos para provar
	// que é a carga a conduzir, não um desempate estável.
	t.Run("least_loaded_segue_a_carga", func(t *testing.T) {
		h := newHarness(t)
		cands := []sovereignty.Endpoint{ep("k-euc", regEUCentral), ep("k-euw", regEUWest)}

		h.load.Set(provider, regEUCentral, router.Headroom{WorstUsed: 99, WorstLimit: 100})
		h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 1, WorstLimit: 100})
		dec := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, cands...))
		if dec.Outcome != router.OutcomeRouted || dec.Region != regEUWest {
			t.Fatalf("esperava rota least-loaded para %s, obtive outcome=%s region=%s", regEUWest, dec.Outcome, dec.Region)
		}

		// Inverte a carga: agora eu-central tem mais folga → tem de mudar de escolha.
		h.load.Set(provider, regEUCentral, router.Headroom{WorstUsed: 1, WorstLimit: 100})
		h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 99, WorstLimit: 100})
		dec2 := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, cands...))
		if dec2.Region != regEUCentral {
			t.Fatalf("com a carga invertida esperava %s, obtive %s (a escolha não segue a carga)", regEUCentral, dec2.Region)
		}
		if dec2.KeyID == "" {
			t.Fatal("rota admitida sem KeyID: a conta pooled devia ter sido escolhida (keypool AOS-057)")
		}
	})

	// (A') SATURAÇÃO TOTAL de um endpoint (Saturated) exclui-o dos candidatos.
	t.Run("endpoint_saturado_e_excluido", func(t *testing.T) {
		h := newHarness(t)
		cands := []sovereignty.Endpoint{ep("k-euc", regEUCentral), ep("k-euw", regEUWest)}
		h.load.Set(provider, regEUCentral, router.Headroom{Saturated: true, WorstUsed: 100, WorstLimit: 100})
		h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 5, WorstLimit: 100})
		dec := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, cands...))
		if dec.Region != regEUWest {
			t.Fatalf("o endpoint saturado devia ser excluído; esperava %s, obtive %s", regEUWest, dec.Region)
		}
	})

	// (B) NÃO-COLAPSO AGREGADO — o tecto GLOBAL é partilhado por (provider:model:
	// region). Quatro boards, cada um "ok" localmente (custo 100), partilham um tecto
	// de 300 tokens: os três primeiros são ADMITIDOS, o quarto é ADIADO (defer) — a
	// coordenação com o admission global (ADR-008) impede o estouro colectivo do
	// rate limit partilhado. SEM esta coordenação (ver o meta-teste) o quarto seria
	// despachado às cegas — o colapso agregado.
	t.Run("sem_colapso_agregado_defere_o_excedente", func(t *testing.T) {
		h := newHarness(t)
		// Batch/Standard → modelo std-1; tecto partilhado da chave std-1@eu-central.
		h.adm.SetLimit(provider, modelStandard, regEUCentral, 300, 1_000_000)

		boards := []string{"board-eu", "board-fr", "board-de", "board-pt"}
		var deferred int
		var granted int
		for i, b := range boards {
			rq := router.Request{
				Board: b, Tenant: b, Provider: provider, Region: regEUCentral,
				Capability: tiering.CapabilityStandard, Class: tiering.ClassBatch, EstimatedTokens: 100,
			}
			dec, err := h.router().Route(context.Background(), rq)
			if err != nil {
				t.Fatalf("board %s: erro inesperado: %v", b, err)
			}
			switch dec.Outcome {
			case router.OutcomeRouted:
				granted++
			case router.OutcomeDeferred:
				deferred++
				if dec.RetryAfter <= 0 {
					t.Errorf("board %s adiado sem retry_after (backpressure não aconselhada)", b)
				}
			default:
				t.Fatalf("board %s (i=%d): outcome inesperado %s", b, i, dec.Outcome)
			}
		}
		if granted != 3 || deferred != 1 {
			t.Fatalf("colapso agregado: esperava 3 admitidos + 1 adiado (tecto 300/custo 100), obtive granted=%d deferred=%d", granted, deferred)
		}
	})
}

// ===========================================================================
// CENÁRIO 2 — MODEL TIERING: o tier MAIS BARATO que satisfaz a capacidade (nunca um
// incapaz); interactivo favorece latência, batch tolera lento/barato. Orquestra o
// router sobre a escada de tiers REAL (routing/tiering, AOS-059).
// ===========================================================================

func TestScenario2_Tiering_CheapestCapable_InteractiveVsBatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// (a) Uma tarefa de RACIOCÍNIO (Frontier) recebe um tier Frontier — NUNCA um
	// económico incapaz (o fix do high de AOS-059). Batch → o Frontier mais barato.
	front := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10))
	if front.Outcome != router.OutcomeRouted {
		t.Fatalf("frontier batch: esperava rota, obtive %s", front.Outcome)
	}
	if front.Model != modelFrontBatch {
		t.Fatalf("frontier batch: esperava o Frontier mais barato (%s), obtive %s (tier=%s)", modelFrontBatch, front.Model, front.Tier)
	}
	if got := tierCapability(t, h, front.Tier); got < tiering.CapabilityFrontier {
		t.Fatalf("tarefa de raciocínio servida por tier INCAPAZ (capability=%d < Frontier)", got)
	}

	// (b) INTERACTIVO favorece LATÊNCIA: uma tarefa Standard interactiva recebe o
	// tier RÁPIDO (std-fast) mesmo sendo mais caro que o standard lento.
	inter := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassInteractive, 10))
	if inter.Model != modelStdFast {
		t.Fatalf("standard interactivo: esperava o tier rápido (%s), obtive %s", modelStdFast, inter.Model)
	}

	// (c) BATCH tolera lento/barato: a MESMA capacidade Standard em batch recebe o
	// mais BARATO (std-1), não o rápido caro. É a distinção interactivo vs batch.
	batch := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10))
	if batch.Model != modelStandard {
		t.Fatalf("standard batch: esperava o mais barato (%s), obtive %s", modelStandard, batch.Model)
	}
	if inter.Model == batch.Model {
		t.Fatal("interactivo e batch escolheram o MESMO tier — a distinção latência-vs-custo não é exercida")
	}
}

// ===========================================================================
// CENÁRIO 3 — DEGRADAÇÃO GRACIOSA: a sequência shed→defer→degradar→rejeitar mapeada
// aos Outcomes do router sob pressão de orçamento/rate-limit. Orquestra o router +
// degradation.Policy (AOS-059). Exaustão graciosa a ~80% OFERECE degradar para tier
// mais barato CAPAZ; nunca um hard-stop cego.
// ===========================================================================

func TestScenario3_Degradation_ShedDeferDegradeReject(t *testing.T) {
	t.Parallel()

	// A cadeia declarativa do GW é IDÊNTICA à do Escalonador (não inventa uma
	// paralela): shed → defer → degradar → rejeitar. NOTA (transparência): o GW NÃO
	// emite um Outcome "shed" — o shed (e a EXECUÇÃO da cadeia) pertence ao Escalonador
	// (AOS-031, técnica/06 §6); o GW é dono só da ESCOLHA de tier e da OFERTA de
	// degradação. Por isso o "shed" é validado ESTRUTURALMENTE (a constante DefaultOrder
	// abaixo), enquanto defer/degradar/rejeitar são asseverados como Outcomes reais do
	// router sob pressão. O loop end-to-end do shed seria um teste de integração
	// GW↔Escalonador (routing/tieradapter), fora do âmbito desta suite adversarial.
	if got := degradation.DefaultOrder; len(got) != 4 ||
		got[0] != degradation.ActionShed || got[1] != degradation.ActionDefer ||
		got[2] != degradation.ActionDegrade || got[3] != degradation.ActionReject {
		t.Fatalf("ordem de degradação divergente da cadeia do Escalonador: %v", got)
	}

	// (defer) sob rate-limit: tecto de 100 tokens; a 1.ª chamada é admitida, a 2.ª é
	// ADIADA (defer) — nunca despacha sem headroom global reservado.
	t.Run("defer_sob_rate_limit", func(t *testing.T) {
		h := newHarness(t)
		h.adm.SetLimit(provider, modelStandard, regEUWest, 100, 1_000_000)
		r := h.router()
		first := mustRoute(t, r, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
		if first.Outcome != router.OutcomeRouted {
			t.Fatalf("1.ª chamada: esperava rota, obtive %s", first.Outcome)
		}
		second := mustRoute(t, r, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
		if second.Outcome != router.OutcomeDeferred || second.RetryAfter <= 0 {
			t.Fatalf("2.ª chamada: esperava DEFER com retry_after, obtive %s (retry=%s)", second.Outcome, second.RetryAfter)
		}
	})

	// (degradar) exaustão graciosa a ~80%: uma tarefa Frontier interactiva no tier
	// caro (front-fast) é degradada para o Frontier mais barato CAPAZ (front-slow) —
	// nunca abaixo da capacidade exigida.
	t.Run("degradar_para_mais_barato_capaz_a_80pct", func(t *testing.T) {
		h := newHarness(t)
		h.setBudget(80, 100) // 80% do orçamento
		dec := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10))
		if dec.Outcome != router.OutcomeDegraded || !dec.Degraded {
			t.Fatalf("a 80%% esperava DEGRADED, obtive %s (degraded=%v)", dec.Outcome, dec.Degraded)
		}
		if dec.FromTier != "frontier" || dec.ToTier != "frontier-batch" || dec.Model != modelFrontBatch {
			t.Fatalf("degradação esperada frontier→frontier-batch, obtive %s→%s (model=%s)", dec.FromTier, dec.ToTier, dec.Model)
		}
		if got := tierCapability(t, h, dec.Tier); got < tiering.CapabilityFrontier {
			t.Fatalf("degradou ABAIXO da capacidade exigida (capability=%d < Frontier) — proibido", got)
		}
	})

	// (exaustão SEM hard-stop) orçamento esgotado (>=100%) e SEM degrau mais barato
	// CAPAZ: o router NÃO faz hard-stop cego — sinaliza BudgetExhausted e mantém a
	// rota capaz, deixando a cadeia do Escalonador rejeitar de forma informada.
	t.Run("esgotado_sem_hard_stop_cego", func(t *testing.T) {
		h := newHarness(t)
		h.setBudget(100, 100) // esgotado
		// Frontier batch → já é o Frontier mais barato (front-slow); não há capaz abaixo.
		dec := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10))
		if dec.Outcome == router.OutcomeRejected {
			t.Fatal("hard-stop cego: esgotado sem cheaper NÃO deve rejeitar no router (a cadeia do Escalonador decide)")
		}
		if !dec.BudgetExhausted {
			t.Fatal("orçamento esgotado sem sinal BudgetExhausted — observabilidade infiel (gasto silencioso)")
		}
		if got := tierCapability(t, h, dec.Tier); got < tiering.CapabilityFrontier {
			t.Fatalf("sob esgotamento a rota caiu para tier INCAPAZ (capability=%d)", got)
		}
	})

	// (rejeitar) rejeição PERMANENTE do admission: um custo que excede o próprio tecto
	// é rejeição fail-closed (nunca admissível por refill).
	t.Run("rejeitar_quando_custo_excede_o_tecto", func(t *testing.T) {
		h := newHarness(t)
		h.adm.SetLimit(provider, modelStandard, regEUWest, 50, 1_000_000) // tecto 50
		dec := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
		if dec.Outcome != router.OutcomeRejected {
			t.Fatalf("custo 100 > tecto 50: esperava REJECT permanente, obtive %s", dec.Outcome)
		}
	})
}

// ===========================================================================
// CENÁRIO 4 — FAILOVER INTRA-FRONTEIRA + REJEIÇÃO sem capacidade intra. Orquestra a
// guarda de soberania REAL (routing/sovereignty, AOS-058): o primário indisponível
// falha para um alternativo INTRA-fronteira; sem alternativo intra, REJEITA (nunca
// cross-border).
//
// NOTA DE LAYERING (transparência): o failover POR SAÚDE é responsabilidade EXCLUSIVA
// da guarda (guard.Route, com o predicado de saúde) — o Router.Route só aplica
// FRONTEIRA + CARGA (chooseRegion usa guard.SameBoundary + least-loaded, nunca
// guard.Route). Por isso este cenário exercita a semântica de failover directamente
// contra a guarda REAL; não é um teste de integração do router. O router intra-nível
// (fronteira+carga) é coberto pelos cenários 1 e 5.
// ===========================================================================

func TestScenario4_Failover_IntraBoundary_And_Reject(t *testing.T) {
	t.Parallel()
	g := testGuard()
	primary := ep("k-euw", regEUWest)
	healthyAll := func(sovereignty.Endpoint) bool { return true }
	down := func(e sovereignty.Endpoint) bool { return e != primary } // o primário caiu

	// (a) primário saudável e intra → PRIMARY.
	if d := g.Route(regEUWest, primary, nil, healthyAll); d.Outcome != sovereignty.OutcomePrimary || d.Chosen != primary {
		t.Fatalf("primário saudável: esperava PRIMARY(%v), obtive %s(%v)", primary, d.Outcome, d.Chosen)
	}

	// (b) primário indisponível + alternativo INTRA-fronteira → FAILOVER intra.
	alt := ep("k-euc", regEUCentral)
	d := g.Route(regEUWest, primary, []sovereignty.Endpoint{primary, alt}, down)
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen != alt {
		t.Fatalf("failover intra: esperava FAILOVER→%v, obtive %s(%v)", alt, d.Outcome, d.Chosen)
	}
	if g.BoundaryOf(d.Chosen.Region) != g.BoundaryOf(regEUWest) {
		t.Fatalf("failover saiu da fronteira: %s não é intra de %s", d.Chosen.Region, regEUWest)
	}

	// (c) primário indisponível e SEM alternativo intra (só existe capacidade
	// cross-border) → REJEIÇÃO fail-closed, nunca cross-border.
	rej := g.Route(regEUWest, primary, []sovereignty.Endpoint{ep("k-us", regUSEast)}, down)
	if rej.Outcome != sovereignty.OutcomeReject {
		t.Fatalf("sem intra: esperava REJECT, obtive %s(%v)", rej.Outcome, rej.Chosen)
	}
	if !rej.CrossBorderBlocked() {
		t.Fatal("rejeição por só haver cross-border deve marcar CrossBorderBlocked (deny atribuível)")
	}

	// (d) primário indisponível e SEM candidatos de todo → REJEIÇÃO simples (sem
	// capacidade intra-fronteira), sem candidatos cross-border descartados.
	rej2 := g.Route(regEUWest, primary, nil, down)
	if rej2.Outcome != sovereignty.OutcomeReject || rej2.CrossBorderBlocked() {
		t.Fatalf("sem candidatos: esperava REJECT simples, obtive %s (crossborder=%v)", rej2.Outcome, rej2.CrossBorderBlocked())
	}
}

// ===========================================================================
// CENÁRIO 5 — CROSS-BORDER BLOQUEADO fail-closed, com DENY registado e ATRIBUÍVEL.
// Orquestra o router (AOS-059) — que DESCARTA os candidatos cross-border ANTES de
// escolher — com o Recorder de governação REAL (AOS-058) + audit WORM (AOS-011): o
// deny cross-border é selado atribuível a principal + board na hash-chain
// tamper-evident, respondendo a "quem foi recusado e porquê".
// ===========================================================================

func TestScenario5_CrossBorder_Blocked_DenyAttributable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)

	// O primário (eu-west) caiu e a ÚNICA capacidade candidata é us-east (cross-border).
	// O router descarta us-east ANTES de escolher e REJEITA (nunca cross-border).
	dec := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	if dec.Outcome != router.OutcomeRejected {
		t.Fatalf("cross-border: esperava REJECT, obtive %s (region=%s)", dec.Outcome, dec.Region)
	}
	if len(dec.Dropped) != 1 || dec.Dropped[0].Region != regUSEast {
		t.Fatalf("o candidato cross-border devia ser DESCARTADO (Dropped), obtive %v", dec.Dropped)
	}
	if !strings.Contains(dec.Reason, "soberania") {
		t.Fatalf("razão da rejeição não atribui a soberania: %q", dec.Reason)
	}
	// A decisão ficou REGISTADA no sink do router (análise post-hoc).
	if last, ok := h.sink.last(); !ok || last.Outcome != router.OutcomeRejected {
		t.Fatal("a decisão de rejeição cross-border não foi registada no DecisionSink")
	}

	// DENY ATRIBUÍVEL — sela no audit WORM via o Recorder REAL, atribuível a
	// principal (agente) + board. É a prova de "quem foi recusado e porquê".
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	denied := dec.Dropped[0].Region
	gr := allowlist.GovRecord{
		Board:          boardEU,
		PrincipalUser:  "user-alice",
		PrincipalAgent: "agent-7",
		Model:          modelFrontier,
		Region:         denied,
		Decision:       audit.DecisionDeny,
		Reason:         "failover cross-border bloqueado: " + dec.Reason,
		PolicyVersion:  testPolicyVersion,
	}
	sealed, err := rec.Seal(ctx, gr)
	if err != nil {
		t.Fatalf("selagem do deny cross-border falhou: %v", err)
	}

	// A hash-chain tem de VERIFICAR (tamper-evident) e o registo tem de atribuir o
	// deny ao principal + board (não um deny anónimo).
	part := "modelgw-gov:" + boardEU
	head, _ := store.Head(ctx, part)
	if err := audit.Verify(ctx, store, part, 1, head); err != nil {
		t.Fatalf("cadeia de audit do deny não verifica: %v", err)
	}
	if sealed.Principal.NHIID != "agent-7" {
		t.Fatalf("deny não atribuído ao principal: NHIID=%q", sealed.Principal.NHIID)
	}
	if sealed.Decision != audit.DecisionDeny {
		t.Fatalf("registo selado não é DENY: %s", sealed.Decision)
	}
	if !obligationHas(sealed, "board", boardEU) || !obligationHas(sealed, "principal_user", "user-alice") {
		t.Fatalf("deny não atribuível a board+principal nas obligations: %+v", sealed.Obligations)
	}

	// ATRIBUIBILIDADE IMPOSTA (fail-closed): um deny SEM board ou SEM principal é
	// RECUSADO pelo Recorder — a soberania nunca sela um deny anónimo.
	if _, err := rec.Seal(ctx, allowlist.GovRecord{PrincipalAgent: "x", Decision: audit.DecisionDeny}); !errors.Is(err, allowlist.ErrNoBoard) {
		t.Fatalf("deny sem board devia ser recusado (ErrNoBoard), obtive %v", err)
	}
	if _, err := rec.Seal(ctx, allowlist.GovRecord{Board: boardEU, Decision: audit.DecisionDeny}); !errors.Is(err, allowlist.ErrNoAttribution) {
		t.Fatalf("deny sem principal devia ser recusado (ErrNoAttribution), obtive %v", err)
	}
}

// tierCapability devolve a capacidade OFERECIDA pelo tier de nome dado na escada do
// harness (via a API pública TierOf da escada real).
func tierCapability(t *testing.T, h *harness, tierName string) tiering.Capability {
	t.Helper()
	tr, ok := h.ladder.TierOf(tierName)
	if !ok {
		t.Fatalf("tier desconhecido na escada: %q", tierName)
	}
	return tr.Capability
}

// obligationHas reporta se o registo selado tem uma obligation de governação com o
// par chave/valor dado (a prova de atribuição num único registo).
func obligationHas(rec audit.AuditRecord, key, val string) bool {
	for _, ob := range rec.Obligations {
		if ob.Params[key] == val {
			return true
		}
	}
	return false
}
