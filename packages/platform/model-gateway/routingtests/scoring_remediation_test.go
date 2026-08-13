package routingtests

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// ===========================================================================
// AOS-269 / ADR-021 — CENÁRIOS DE REMEDIAÇÃO (auditoria adversarial da wave).
//
// Três invariantes que a primeira implementação do scoring não tinha e que a
// auditoria apanhou. Todos comparam o modo PONTUADO com o modo LEXICOGRÁFICO no
// MESMO harness: armar o scoring não pode mudar a NATUREZA da disposição, só a
// ordem dos sobreviventes.
//
//	 9. SATURAÇÃO É PRESSÃO, NÃO GUARDA — uma região saturada (ou um erro
//	    transitório de leitura de carga) NÃO pode virar rejeição permanente. O
//	    ADR-021 regra 1 enumera TRÊS guardas (soberania, allowlist, capacidade);
//	    o headroom é FACTOR (regra 2), e a cadeia do ADR-008 já tem o degrau
//	    `defer` para a pressão.
//	10. COERÊNCIA MODELO↔FACTORES — depois de uma degradação por orçamento, o
//	    score e os factores registados descrevem o modelo DESPACHADO, não o que o
//	    scorer tinha eleito (senão a calibração offline da regra 4 aprende do
//	    modelo errado).
//	11. INTENÇÃO POR PEDIDO — o perfil de pesos é seleccionável POR DECISÃO
//	    (ADR-021 §1 gap 2) e um perfil desconhecido é recusa fail-closed; a classe
//	    da tarefa (AOS-059) não se perde ao armar o scoring.
// ===========================================================================

// errLoadProvider é um [router.LoadProvider] que falha SEMPRE — o erro transitório
// de leitura de carga (janela de rate-limit, sonda em falha). Não é saturação: é
// AUSÊNCIA DE SINAL, que a regra 2 do ADR-021 manda resolver «pelo lado seguro»
// (factor 0), não por exclusão do candidato.
type errLoadProvider struct{}

func (errLoadProvider) Load(context.Context, string, string) (router.Headroom, error) {
	return router.Headroom{}, errors.New("routingtests: sonda de carga indisponivel (transitorio)")
}

// TestScenario9_Scoring_SaturationIsPressureNotGuard prova que, com UMA única
// região intra-fronteira, a saturação e o erro de carga produzem EXACTAMENTE a
// mesma disposição com e sem scoring — e que essa disposição NUNCA é uma rejeição
// atribuída à allowlist/capacidade (que estão intactas).
func TestScenario9_Scoring_SaturationIsPressureNotGuard(t *testing.T) {
	t.Parallel()

	// Perfil que pesa SÓ o headroom: o caso mais hostil possível para o invariante
	// (o único factor que o candidato saturado tem vale 0, logo o score total é 0).
	// Mesmo assim o candidato TEM de existir — score 0 não é ausência.
	const headroomOnly = `{"version":"aos269-remediacao/v1","semver":"1.0.0","default_profile":"so_headroom","profiles":[` +
		`{"name":"so_headroom","weights":{"health":0,"headroom":1000,"cost":0,"latency":0,"task_fit":0,"stability":0}}]}`

	cases := []struct {
		name string
		// degrade devolve a MESMA porta de carga que o router e o factor headroom
		// vão consumir — é o ponto: os dois modos vêem o mesmo sinal estragado.
		degrade func(h *harness) router.LoadProvider
	}{
		{
			name: "regiao_saturada",
			degrade: func(h *harness) router.LoadProvider {
				h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 100, WorstLimit: 100, Saturated: true})
				return h.load
			},
		},
		{
			name: "erro_transitorio_de_leitura",
			degrade: func(*harness) router.LoadProvider {
				return errLoadProvider{}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// (A) LEXICOGRÁFICO — a referência de AOS-059.
			hLex := newHarness(t)
			lex := mustRoute(t, hLex.router(router.WithLoadProvider(tc.degrade(hLex))),
				req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))

			// (B) PONTUADO — mesmo harness, mesmo pedido, scoring armado sobre a MESMA
			// porta de carga estragada.
			hSc := newHarness(t)
			lp := tc.degrade(hSc)
			sc := hSc.scorerOver(t, signTable(t, "aos269-remediacao-weights-seed!!", headroomOnly), "so_headroom",
				scoring.WithHeadroom(scoring.HeadroomFromReader(router.HeadroomReaderFrom(lp))))
			scored := mustRoute(t, hSc.router(router.WithLoadProvider(lp), router.WithScoring(sc)),
				req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))

			if scored.Outcome != lex.Outcome {
				t.Fatalf("armar o scoring mudou a DISPOSIÇÃO sob pressão de carga: lex=%s vs scoring=%s (%s)",
					lex.Outcome, scored.Outcome, scored.Reason)
			}
			if scored.Outcome == router.OutcomeRejected {
				t.Fatalf("saturação/erro de carga virou REJEIÇÃO PERMANENTE (o degrau é defer, ADR-008): %q", scored.Reason)
			}
			// A razão NUNCA pode culpar a allowlist/capacidade por um problema de carga.
			if strings.Contains(scored.Reason, "allowlist") {
				t.Fatalf("deny não-atribuível: a razão culpa a allowlist por pressão de carga: %q", scored.Reason)
			}
			if !scored.Scored {
				t.Fatalf("a decisão devia ter sido pontuada (o candidato saturado é FACTOR 0, não ausência): %+v", scored)
			}
			// O invariante material: o headroom vale ZERO (o sinal é honesto) e o
			// candidato foi eleito na mesma.
			if scored.ScoreFactors.Headroom != 0 {
				t.Fatalf("com carga saturada/ilegível o factor headroom tem de ser 0, obtive %d", scored.ScoreFactors.Headroom)
			}
			if scored.Model == "" {
				t.Fatal("nenhum modelo eleito apesar de existir candidato intra-fronteira, permitido e capaz")
			}
		})
	}
}

// TestScenario10_Scoring_DegradationRescoresChosenTier prova a COERÊNCIA entre o
// modelo despachado e os factores registados depois de uma degradação por
// orçamento: o `DecisionSink` — o consumidor canónico de calibração que o ADR-021
// §4 cria — nunca recebe factores de um modelo que não correu.
func TestScenario10_Scoring_DegradationRescoresChosenTier(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// 85% do orçamento ⇒ a política OFERECE degradar (limiar ~80%).
	h.setBudget(85, 100)

	// Perfil "quality": o task-fit domina, pelo que o scorer elege o FRONTIER caro.
	// A degradação por orçamento troca-o depois pelo frontier-batch (mais barato e
	// AINDA capaz) — a troca que descoordenava os campos de scoring.
	const qualityOnly = `{"version":"aos269-remediacao/v1","semver":"1.0.0","default_profile":"so_qualidade","profiles":[` +
		`{"name":"so_qualidade","weights":{"health":0,"headroom":0,"cost":0,"latency":0,"task_fit":1000,"stability":0}}]}`
	fit := scoring.NewStaticTaskFit(0).
		Set(modelFrontier, tiering.CapabilityFrontier, scoring.Scale).
		Set(modelFrontBatch, tiering.CapabilityFrontier, 100)
	sc := h.scorerOver(t, signTable(t, "aos269-remediacao-weights-seed!!", qualityOnly), "so_qualidade",
		scoring.WithTaskFit(fit))

	dec := mustRoute(t, h.router(router.WithScoring(sc)),
		req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-euw", regEUWest)))

	if !dec.Degraded || dec.Model != modelFrontBatch {
		t.Fatalf("esperava degradação para %s, obtive degraded=%v model=%s (%s)", modelFrontBatch, dec.Degraded, dec.Model, dec.Reason)
	}
	// O candidato ELEITO pelo score fica preservado — e é DISTINTO do despachado.
	if dec.ScoredModel != modelFrontier {
		t.Fatalf("o candidato pontuado antes da troca devia ficar preservado (%s), obtive %q", modelFrontier, dec.ScoredModel)
	}
	// A COERÊNCIA: os factores registados são os do modelo DESPACHADO. Com o perfil
	// só-task-fit, o score normalizado é o próprio task-fit do modelo.
	if dec.ScoreFactors.TaskFit != 100 || dec.Score != 100 {
		t.Fatalf("score/factores descrevem o modelo errado: score=%d factores=%s (esperava o task-fit de %s = 100)",
			dec.Score, dec.ScoreFactors, modelFrontBatch)
	}
	if dec.ScoredScore != scoring.Scale {
		t.Fatalf("o score do candidato ORIGINAL devia ficar preservado (%d), obtive %d", scoring.Scale, dec.ScoredScore)
	}
	// E o registo post-hoc (o que a calibração offline consome) vê exactamente isso.
	last, ok := h.sink.last()
	if !ok || last.Model != modelFrontBatch || last.ScoreFactors != dec.ScoreFactors {
		t.Fatalf("o DecisionSink não recebeu a decisão coerente: %+v", last)
	}
	// A razão continua a carregar o scoring (regra 5) — agora o do modelo trocado.
	if !strings.Contains(dec.Reason, "scoring ponderado determinista") {
		t.Fatalf("a razão da degradação perdeu o sufixo de scoring: %q", dec.Reason)
	}
}

// TestScenario11_Scoring_ProfilePerRequestAndClass prova (a) que a INTENÇÃO é
// seleccionável por pedido — o mesmo router, dois perfis, duas escolhas —, (b) que
// um perfil desconhecido é recusa fail-closed com razão PRÓPRIA, e (c) que a
// CLASSE de AOS-059 não se perde: com o mesmo perfil, um batch não paga o bónus
// estrutural do bit `Fast`.
func TestScenario11_Scoring_ProfilePerRequestAndClass(t *testing.T) {
	t.Parallel()

	// (a) DOIS perfis na MESMA tabela assinada, seleccionados POR PEDIDO.
	const twoProfiles = `{"version":"aos269-remediacao/v1","semver":"1.0.0","default_profile":"barato","profiles":[` +
		`{"name":"barato","weights":{"health":0,"headroom":0,"cost":1000,"latency":0,"task_fit":0,"stability":0}},` +
		`{"name":"qualidade","weights":{"health":0,"headroom":0,"cost":0,"latency":0,"task_fit":1000,"stability":0}}]}`
	h := newHarness(t)
	fit := scoring.NewStaticTaskFit(0).
		Set(modelStdFast, tiering.CapabilityStandard, scoring.Scale).
		Set(modelStandard, tiering.CapabilityStandard, 0)
	sc := h.scorerOver(t, signTable(t, "aos269-remediacao-weights-seed!!", twoProfiles), "barato",
		scoring.WithTaskFit(fit))
	r := h.router(router.WithScoring(sc))

	cheap := req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, ep("k-euw", regEUWest))
	quality := cheap
	quality.Profile = "qualidade"

	cheapDec, qualityDec := mustRoute(t, r, cheap), mustRoute(t, r, quality)
	if cheapDec.Model != modelStandard {
		t.Fatalf("perfil composto (barato) devia eleger o standard barato, obtive %s", cheapDec.Model)
	}
	if qualityDec.Model != modelStdFast {
		t.Fatalf("o perfil PEDIDO no request (qualidade) não foi aplicado: obtive %s", qualityDec.Model)
	}
	if qualityDec.ScoreProfile != "qualidade" || cheapDec.ScoreProfile != "barato" {
		t.Fatalf("o perfil registado não é o aplicado: %q vs %q", cheapDec.ScoreProfile, qualityDec.ScoreProfile)
	}

	// (b) Perfil DESCONHECIDO: recusa fail-closed, com razão própria — nunca queda
	// silenciosa no composto nem na razão da allowlist.
	bogus := cheap
	bogus.Profile = "nao-existe"
	bad := mustRoute(t, r, bogus)
	if bad.Outcome != router.OutcomeRejected {
		t.Fatalf("perfil desconhecido tinha de REJEITAR, obtive %s/%s", bad.Outcome, bad.Model)
	}
	if !strings.Contains(bad.Reason, "perfil de pesos desconhecido") {
		t.Fatalf("a rejeição por perfil desconhecido não é atribuível: %q", bad.Reason)
	}

	// (c) CLASSE (AOS-059 preservado sob scoring): perfil que pesa SÓ a latência,
	// sem p95 declarado. Interactivo prefere o tier `Fast`; batch NÃO paga esse
	// bónus estrutural e o desempate cai no mais barato capaz.
	const latencyOnly = `{"version":"aos269-remediacao/v1","semver":"1.0.0","default_profile":"so_latencia","profiles":[` +
		`{"name":"so_latencia","weights":{"health":0,"headroom":0,"cost":0,"latency":1000,"task_fit":0,"stability":0}}]}`
	h2 := newHarness(t)
	sc2 := h2.scorerOver(t, signTable(t, "aos269-remediacao-weights-seed!!", latencyOnly), "so_latencia",
		scoring.WithLatency(scoring.NewStaticLatency(true)))
	r2 := h2.router(router.WithScoring(sc2))

	inter := mustRoute(t, r2, req(regEUWest, tiering.CapabilityStandard, tiering.ClassInteractive, 10, ep("k-euw", regEUWest)))
	batch := mustRoute(t, r2, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))
	if inter.Model != modelStdFast {
		t.Fatalf("interactivo devia favorecer o tier Fast (%s), obtive %s", modelStdFast, inter.Model)
	}
	if batch.Model != modelStandard {
		t.Fatalf("batch não pode pagar o bónus estrutural do bit Fast: esperava %s, obtive %s", modelStandard, batch.Model)
	}
}

// ---------------------------------------------------------------------------
// PROBES do relatório da suite (AOS_ROUTING_REPORT) — sem *testing.T.
// ---------------------------------------------------------------------------

// probeScoringSaturationNotGuard: com a única região saturada, o modo pontuado NÃO
// rejeita (a saturação é pressão, tratada a jusante pela cadeia do ADR-008).
func probeScoringSaturationNotGuard() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 100, WorstLimit: 100, Saturated: true})
	tab, err := loadAdversarialTable()
	if err != nil {
		return false
	}
	sc, err := scoring.NewScorer(scoring.TableFrom(tab), "barato",
		scoring.WithCost(scoring.CostFromLadder(h.ladder)),
		scoring.WithHeadroom(scoring.HeadroomFromReader(router.HeadroomReaderFrom(h.load))))
	if err != nil {
		return false
	}
	dec, err := h.router(router.WithScoring(sc)).Route(context.Background(),
		req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))
	return err == nil && dec.Outcome != router.OutcomeRejected && dec.Scored && dec.ScoreFactors.Headroom == 0
}

// probeScoringUnknownProfileRejects: um perfil pedido que não existe na tabela é
// recusa fail-closed — nunca queda silenciosa no perfil composto.
func probeScoringUnknownProfileRejects() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	tab, err := loadAdversarialTable()
	if err != nil {
		return false
	}
	sc, err := scoring.NewScorer(scoring.TableFrom(tab), "barato", scoring.WithCost(scoring.CostFromLadder(h.ladder)))
	if err != nil {
		return false
	}
	rq := req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10, ep("k-euw", regEUWest))
	rq.Profile = "inexistente"
	dec, err := h.router(router.WithScoring(sc)).Route(context.Background(), rq)
	return err == nil && dec.Outcome == router.OutcomeRejected && !dec.Scored
}
