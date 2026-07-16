// Package routingtests é a SUITE ADVERSARIAL de roteamento/failover do Model
// Gateway (AOS-063, o chore de TESTES que FECHA o EPIC-06). É um pacote SÓ de
// testes — NÃO reimplementa nenhum controlo: ORQUESTRA os controlos REAIS de
// AOS-058/059 (router cost/load-aware, tiering, degradação, guarda de soberania,
// allowlist regional, keypool) e prova, de forma DETERMINISTA e repetível, que
// cada cenário de risco de tecnica/06 §5/§6/§9 é reproduzido e tratado
// correctamente:
//
//  1. SATURAÇÃO — a selecção least-loaded/token-aware escolhe o endpoint de mais
//     folga e, sob pressão global, NÃO colapsa o agregado (ADR-008): a coordenação
//     com o admission GLOBAL ADIA em vez de estourar o tecto partilhado.
//  2. TIERING — a tarefa recebe o tier MAIS BARATO que satisfaz a capacidade (nunca
//     um incapaz); interactivo favorece latência, batch tolera lento/barato.
//  3. DEGRADAÇÃO — a sequência shed→defer→degradar→rejeitar (mapeada aos Outcomes do
//     router, coordenada com a cadeia do Escalonador): exaustão graciosa a ~80%
//     OFERECE degradar para tier mais barato CAPAZ, nunca um hard-stop cego.
//  4. FAILOVER INTRA — primário indisponível + alternativo intra-fronteira → failover
//     intra; sem alternativo intra-fronteira → REJEIÇÃO (nunca cross-border).
//  5. CROSS-BORDER — qualquer tentativa de failover cross-border é BLOQUEADA
//     fail-closed, com DENY registado e ATRIBUÍVEL a principal + board (audit WORM).
//
// # Não-vacuidade (o coração do padrão AOS-054)
//
// Para cada cenário há um META-TESTE (metatests_test.go) que, com o controlo real
// CONTORNADO/desligado (guarda de soberania a colapsar fronteiras, allowlist a
// permitir tudo, admission removido, piso de capacidade removido), prova que o
// ataque PASSA — logo o teste principal DETECTA mesmo (o verde não é vazio).
//
// # Determinismo
//
// Sem relógio nem aleatoriedade reais: carga, orçamento, admissão e saúde são
// injectados por porta (as impls de referência determinísticas de AOS-057/059). Os
// fakes de provider por região (o keypool.Pool + router.StaticLoadProvider por
// região) têm carga injectável (SetLoad/Set). Repetível: mesma entrada → mesmo
// resultado, sem flakiness. Sem segredos (ADR-006): as contas são KeyIDs
// NÃO-secretos.
package routingtests

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/keypool"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// Constantes do cenário: um provedor lógico, um board de soberania, duas regiões
// INTRA-fronteira (mesma jurisdição "eu") e uma região CROSS-BORDER ("us").
const (
	provider = "openai"

	boardEU = "board-eu"

	regEUWest    = "eu-west"
	regEUCentral = "eu-central"
	regUSEast    = "us-east"

	boundEU = "eu"
	boundUS = "us"
)

// Modelos da escada de tiers de teste (NÃO-secretos, só identificadores lógicos).
const (
	modelEconomy    = "econo-mini" // Basic, mais barato
	modelBasicFast  = "fast-mini"  // Basic, rápido
	modelStandard   = "std-1"      // Standard, barato/lento
	modelStdFast    = "std-fast"   // Standard, rápido/caro
	modelFrontBatch = "front-slow" // Frontier, lento/barato
	modelFrontier   = "front-fast" // Frontier, rápido/caro
)

// testLadder constrói a escada de tiers REAL (routing/tiering, AOS-059) com pares de
// tiers na MESMA capacidade a custos diferentes — condição necessária para exercitar
// (a) a escolha do mais barato capaz, (b) o ramo interactivo-vs-batch e (c) a
// degradação para um mais barato CAPAZ (frontier→frontier-batch) distinta de uma
// descida para um INCAPAZ (o que a degradação NUNCA faz).
func testLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "economy", Model: modelEconomy, CostRank: 1, Capability: tiering.CapabilityBasic},
		tiering.Tier{Name: "basic-fast", Model: modelBasicFast, CostRank: 2, Capability: tiering.CapabilityBasic, Fast: true},
		tiering.Tier{Name: "standard", Model: modelStandard, CostRank: 3, Capability: tiering.CapabilityStandard},
		tiering.Tier{Name: "standard-fast", Model: modelStdFast, CostRank: 4, Capability: tiering.CapabilityStandard, Fast: true},
		tiering.Tier{Name: "frontier-batch", Model: modelFrontBatch, CostRank: 5, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "frontier", Model: modelFrontier, CostRank: 6, Capability: tiering.CapabilityFrontier, Fast: true},
	)
}

// testAllowlistJSON é uma allowlist regional (policy-as-code, AOS-058) que autoriza
// os modelos da escada APENAS nas regiões intra-fronteira "eu-west"/"eu-central" —
// jamais "us-east". O board é "*" (qualquer board é intra desde que na região
// permitida) para o cenário de saturação com vários boards; a soberania é imposta
// pela REGIÃO (correspondência exacta, sem wildcard) — us-east nunca é permitida.
const testAllowlistJSON = `{"version":"gw-routingtests/v1","default":"deny","rules":[` +
	`{"id":"eu-all","board":"*",` +
	`"models":["` + modelEconomy + `","` + modelBasicFast + `","` + modelStandard + `","` +
	modelStdFast + `","` + modelFrontBatch + `","` + modelFrontier + `"],` +
	`"regions":["` + regEUWest + `","` + regEUCentral + `"]}]}`

// testPolicyVersion é a versão declarada da policy de teste (para o registo de
// governação).
const testPolicyVersion = "gw-routingtests/v1"

// buildPolicy carrega a allowlist de teste pelo CAMINHO DE VERIFICAÇÃO REAL
// (allowlist.LoadSignedPolicy): gera um par ed25519 de teste, assina o DIGEST
// canónico da policy e carrega — exercitando a verificação de assinatura real de
// AOS-058. A chave de teste NÃO é a de produção (o trust anchor pinado só é exigido
// por LoadPolicy, o carregador embebido); LoadSignedPolicy verifica a assinatura
// contra a pública fornecida, que é o que os testes de AOS-058 também fazem.
func buildPolicy() (*allowlist.Policy, error) {
	var seed [32]byte
	copy(seed[:], []byte("aos-063-routingtests-signing-key"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	dig, err := allowlist.Digest([]byte(testAllowlistJSON))
	if err != nil {
		return nil, err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(dig)))
	return allowlist.LoadSignedPolicy([]byte(testAllowlistJSON), sig, pub)
}

// testGuard constrói a guarda de soberania REAL (routing/sovereignty, AOS-058) com
// as duas regiões "eu" na MESMA fronteira e "us-east" numa fronteira distinta — o
// mapeamento que torna o failover eu-west↔eu-central intra e eu-*↔us-east
// cross-border.
func testGuard() *sovereignty.Guard {
	return sovereignty.NewGuard(
		sovereignty.WithBoundary(regEUWest, boundEU),
		sovereignty.WithBoundary(regEUCentral, boundEU),
		sovereignty.WithBoundary(regUSEast, boundUS),
	)
}

// allowAll é uma allowlist fail-OPEN usada SÓ nos meta-testes (controlo contornado):
// permite qualquer (board, modelo, região). Prova, por contraste, que a allowlist
// real é o que impede a fuga — nunca é usada nos cenários principais.
type allowAll struct{}

func (allowAll) Allows(_, _, _ string) bool { return true }

// recordingSink capta cada router.Decision (a prova de que CADA decisão de
// roteamento fica registada para análise post-hoc — modelo/tier/razão).
type recordingSink struct {
	decisions []router.Decision
}

func (s *recordingSink) Record(_ context.Context, d router.Decision) {
	s.decisions = append(s.decisions, d)
}

func (s *recordingSink) last() (router.Decision, bool) {
	if len(s.decisions) == 0 {
		return router.Decision{}, false
	}
	return s.decisions[len(s.decisions)-1], true
}

// harness reúne os controlos REAIS (impls de referência determinísticas) que a
// suite orquestra. Todos são injectáveis (carga/orçamento/admissão/keypool), sem
// relógio nem rede — repetível.
type harness struct {
	ladder *tiering.Ladder
	guard  *sovereignty.Guard
	allow  router.Allowlist
	load   *router.StaticLoadProvider
	adm    *router.StaticAdmissionCoordinator
	budget *degradation.StaticBudgetProvider
	keys   *keypool.Registry
	sink   *recordingSink
}

// newHarnessErr constrói o harness por omissão: carga folgada, orçamento sem tecto,
// admissão global generosa, pools de chaves por região. Cada cenário aperta a porta
// que quer exercitar. Devolve erro (usado pelas probes do relatório sem *testing.T).
func newHarnessErr() (*harness, error) {
	pol, err := buildPolicy()
	if err != nil {
		return nil, err
	}
	h := &harness{
		ladder: testLadder(),
		guard:  testGuard(),
		allow:  routingstage.AllowlistFrom(pol),
		load:   router.NewStaticLoadProvider(router.Headroom{WorstUsed: 0, WorstLimit: 100}),
		adm:    router.NewStaticAdmissionCoordinator(1_000_000, 1_000_000, 250*time.Millisecond),
		budget: degradation.NewStaticBudgetProvider(degradation.BudgetState{}),
		keys:   keypool.NewRegistry(),
		sink:   &recordingSink{},
	}
	for _, reg := range []string{regEUWest, regEUCentral, regUSEast} {
		p := keypool.NewPool(
			keypool.Account{KeyID: "acct-" + reg + "-1", LimitRPM: 100000, LimitTPM: 100000000},
			keypool.Account{KeyID: "acct-" + reg + "-2", LimitRPM: 100000, LimitTPM: 100000000},
		)
		h.keys.Register(provider, reg, p)
	}
	return h, nil
}

// newHarness é newHarnessErr com fatal em teste.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h, err := newHarnessErr()
	if err != nil {
		t.Fatalf("newHarness: %v", err)
	}
	return h
}

// router constrói o router de PRODUÇÃO (routing/router, AOS-059) ligado a todos os
// controlos reais do harness. As opções extra sobrepõem-se (últimas vencem) para os
// meta-testes que trocam um controlo.
func (h *harness) router(extra ...router.Option) *router.Router {
	opts := []router.Option{
		router.WithGuard(h.guard),
		router.WithAllowlist(h.allow),
		router.WithLoadProvider(h.load),
		router.WithAdmission(h.adm),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
		router.WithDecisionSink(h.sink),
	}
	return router.New(h.ladder, append(opts, extra...)...)
}

// routerNoLoad e routerNoAdmission constroem variantes com um controlo REMOVIDO
// (não apenas trocado) — usadas pelos meta-testes, já que WithLoadProvider(nil)/
// WithAdmission(nil) são no-ops (não conseguem "desligar" via opção).
func (h *harness) routerNoLoad() *router.Router {
	return router.New(h.ladder,
		router.WithGuard(h.guard),
		router.WithAllowlist(h.allow),
		router.WithAdmission(h.adm),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
		router.WithDecisionSink(h.sink),
	)
}

func (h *harness) routerNoAdmission() *router.Router {
	return router.New(h.ladder,
		router.WithGuard(h.guard),
		router.WithAllowlist(h.allow),
		router.WithLoadProvider(h.load),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
		router.WithDecisionSink(h.sink),
	)
}

// setBudget fixa o estado de orçamento do board (exaustão graciosa).
func (h *harness) setBudget(used, limit int64) {
	h.budget.Set(degradation.BudgetKey{Board: boardEU, Tenant: boardEU}, degradation.BudgetState{Used: used, Limit: limit})
}

// req monta um router.Request para o board de soberania, provedor e região dados.
func req(region string, cap tiering.Capability, class tiering.Class, tokens int64, candidates ...sovereignty.Endpoint) router.Request {
	return router.Request{
		Board:           boardEU,
		Tenant:          boardEU,
		Provider:        provider,
		Region:          region,
		Capability:      cap,
		Class:           class,
		EstimatedTokens: tokens,
		Candidates:      candidates,
	}
}

// ep é um atalho para um endpoint candidato (KeyID, região).
func ep(keyID, region string) sovereignty.Endpoint {
	return sovereignty.Endpoint{KeyID: keyID, Region: region}
}

// mustRoute corre o router e falha em erro inesperado (reject/defer NÃO são erro —
// são Outcomes; só o admission propaga erro real).
func mustRoute(t *testing.T, r *router.Router, rq router.Request) router.Decision {
	t.Helper()
	dec, err := r.Route(context.Background(), rq)
	if err != nil {
		t.Fatalf("Route erro inesperado: %v", err)
	}
	return dec
}
