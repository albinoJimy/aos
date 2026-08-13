package modelgateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/policy/weights"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// routing_chain_aos280_test.go é a PROVA PELA CADEIA REAL de AOS-280: o estágio de
// roteamento composto (`failover` → `routingstage`+`router` com o scoring assinado
// armado) exercitado ao nível do GATEWAY DE PRODUÇÃO — [modelgateway.NewProduction]
// + Chat sobre um servidor OpenAI-compatible —, nunca do router isolado.
//
// Cada propriedade é provada com o POSITIVO e o NEGATIVO, porque um teste que só
// mostra o caso feliz não distingue «o controlo funcionou» de «o controlo não fez
// nada»:
//
//	(a) soberania    — deny cross-border continua SELADO no WORM COM a cadeia composta;
//	                   e o caminho intra-fronteira equivalente passa (não é o refino
//	                   que está a bloquear tudo);
//	(b) saturação    — sem headroom de admissão a chamada é ADIADA e o provider NÃO é
//	                   invocado; com headroom, é despachada;
//	(c) orçamento    — a ~90% degrada para o tier mais barato AINDA capaz; a 10% não;
//	(d) scoring      — com MAIS DO QUE UM candidato (2 regiões × 2 tiers), trocar o
//	                   PERFIL de pesos (e só isso) troca o modelo despachado;
//	(e) failover     — uma decisão de failover POR SAÚDE sobrevive ao refino; e o
//	                   gémeo independente mostra que o encadeamento ingénuo (ancorar na
//	                   região PEDIDA, com o endpoint doente ainda como candidato) a
//	                   desfazia.
//
// O modelo EFECTIVAMENTE despachado é lido do corpo do pedido que chega ao servidor
// (não da resposta, que o servidor escolhe), e a REGIÃO efectiva é lida do bearer —
// cada região tem uma credencial de infra distinta em [testCreds].

// dispatch é o que o servidor de provider viu: o modelo do corpo do pedido e o
// bearer (que identifica a região da conta de infra usada).
type dispatch struct {
	Model string
	Auth  string
}

// dispatchLog acumula os despachos vistos pelo servidor. O acesso é protegido dos
// dois lados (escrita no handler, leitura na asserção) para que a suite continue
// limpa sob -race — o servidor responde noutro goroutine.
type dispatchLog struct {
	mu   sync.Mutex
	seen []dispatch
}

func (l *dispatchLog) add(d dispatch) {
	l.mu.Lock()
	l.seen = append(l.seen, d)
	l.mu.Unlock()
}

func (l *dispatchLog) all() []dispatch {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]dispatch, len(l.seen))
	copy(out, l.seen)
	return out
}

// last devolve o último despacho (falha o teste se o provider não foi invocado).
func (l *dispatchLog) last(t *testing.T) dispatch {
	t.Helper()
	all := l.all()
	if len(all) == 0 {
		t.Fatal("o provider nao foi invocado — nao ha despacho para inspeccionar")
	}
	return all[len(all)-1]
}

// recordingProviderServer devolve um httptest.Server OpenAI-compatible que REGISTA
// cada pedido (modelo + bearer). É a única fonte de verdade sobre o que o GW
// despachou — o resto do teste é inferência.
func recordingProviderServer(t *testing.T) (*httptest.Server, *dispatchLog) {
	t.Helper()
	log := &dispatchLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		log.add(dispatch{Model: body.Model, Auth: r.Header.Get("Authorization")})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"` + body.Model + `",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

// chainTiers é a ESCADA declarada nestes testes. Os dois modelos estão na allowlist
// regional EMBEBIDA do board-eu (regiões eu e eu-west), pelo que o refino pode
// despachar qualquer um deles — a condição que [modelgateway.RoutingConfig.Tiers]
// exige de quem declara a escada.
func chainTiers() []tiering.Tier {
	return []tiering.Tier{
		{Name: "standard-fast", Model: "gpt-4o", CostRank: 2, Capability: tiering.CapabilityStandard, Fast: true},
		{Name: "economy", Model: "gpt-4o-mini", CostRank: 1, Capability: tiering.CapabilityStandard},
	}
}

// twoRegionAccounts é o inventário de duas contas INTRA-fronteira do board-eu (eu e
// eu-west): é o que dá ao refino mais do que um candidato para ordenar.
func twoRegionAccounts() []modelgateway.InfraAccount {
	return []modelgateway.InfraAccount{
		{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000},
		{KeyID: "acct-euw-1", Provider: "openai", Region: "eu-west", LimitRPM: 100, LimitTPM: 100000},
	}
}

// chatEU faz a chamada de teste (board-eu, região eu, modelo gpt-4o).
func chatEU(gw *modelgateway.Gateway) error {
	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	return err
}

// taskFitFavouring dá crédito de eval (task-fit) ao modelo indicado e quase nada ao
// outro — o sinal OFFLINE que o perfil "quality" pondera acima de tudo.
func taskFitFavouring(model string) *scoring.StaticTaskFit {
	return scoring.NewStaticTaskFit(100).Set(model, tiering.CapabilityStandard, 900)
}

// (a) SOBERANIA — o deny cross-border continua SELADO no WORM com a cadeia composta.
//
// É o invariante que a decisão de desenho protege: o refino foi ENCADEADO a jusante
// (não substituiu o failover), pelo que o único caminho para a região continua a ser
// a guarda de soberania — e o deny continua a ser um registo de auditoria atribuível,
// não um DecisionSink post-hoc.
func TestAOS280_Chain_CrossBorderDenyStillSealedInWORM(t *testing.T) {
	// POSITIVO (deny): só há capacidade us-east para um board-eu.
	store := audit.NewMemStore()
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-us-1", Provider: "openai", Region: "us-east", LimitRPM: 100, LimitTPM: 100000}})
	cfg.Credentials = testCreds{"openai|us-east": "sk-infra-us"}
	cfg.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "cheap"}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction com a cadeia composta: %v", err)
	}
	if err := chatEU(gw); !errors.Is(err, failover.ErrCrossBorderBlocked) {
		t.Fatalf("com o refino encadeado, o cross-border tem de continuar bloqueado pelo failover; got %v", err)
	}
	if len(log.all()) != 0 {
		t.Fatalf("o provider NAO devia ser invocado num deny de soberania; despachos=%+v", log.all())
	}
	if !hasSealedDeny(t, store, "modelgw-gov:board-eu") {
		t.Fatal("o deny cross-border tem de estar SELADO no WORM (audit.DecisionDeny), atribuivel a principal + board")
	}

	// NEGATIVO (allow): a mesma cadeia, com capacidade INTRA-fronteira, despacha — o
	// refino não é um bloqueio universal disfarçado de controlo de soberania.
	store2 := audit.NewMemStore()
	srv2, log2 := recordingProviderServer(t)
	cfg2 := prodConfig(store2, srv2.URL, srv2.Client(), twoRegionAccounts())
	cfg2.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "cheap"}
	gw2, err := modelgateway.NewProduction(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("NewProduction (controlo intra-fronteira): %v", err)
	}
	if err := chatEU(gw2); err != nil {
		t.Fatalf("a chamada intra-fronteira devia passar pela cadeia composta; got %v", err)
	}
	if len(log2.all()) != 1 {
		t.Fatalf("o provider devia ter sido invocado 1x no caminho intra-fronteira; got %d", len(log2.all()))
	}
	if hasSealedDeny(t, store2, "modelgw-gov:board-eu") {
		t.Fatal("o caminho intra-fronteira NAO pode selar um deny (o controlo estaria a disparar ao lado)")
	}
}

// hasSealedDeny procura um registo de governação com veredicto DENY na partição.
func hasSealedDeny(t *testing.T, store *audit.MemStore, partition string) bool {
	t.Helper()
	ctx := context.Background()
	head, _ := store.Head(ctx, partition)
	for i := uint64(1); i <= head; i++ {
		rec, ok, err := store.At(ctx, partition, i)
		if err != nil || !ok {
			continue
		}
		if rec.Decision == audit.DecisionDeny {
			return true
		}
	}
	return false
}

// (b) SATURAÇÃO — sem headroom de admissão GLOBAL a chamada é ADIADA (ADR-008) e o
// provider NÃO é invocado; com headroom, é despachada. É a coordenação que o router
// traz e que o failover sozinho nunca teve.
func TestAOS280_Chain_AdmissionSaturationDefersBeforeProvider(t *testing.T) {
	// POSITIVO (defer): o tecto global de PEDIDOS está esgotado para todas as rotas
	// possíveis (2 modelos × 2 regiões), pelo que qualquer escolha do refino adia.
	adm := router.NewStaticAdmissionCoordinator(1_000_000, 1, 750*time.Millisecond)
	for _, model := range []string{"gpt-4o", "gpt-4o-mini"} {
		for _, region := range []string{"eu", "eu-west"} {
			if _, err := adm.Reserve(context.Background(), router.AdmissionRequest{
				Provider: "openai", Model: model, Region: region, EstimatedTokens: 1,
			}); err != nil {
				t.Fatalf("pre-consumo do headroom global: %v", err)
			}
		}
	}
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "cheap", Admission: adm}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	err = chatEU(gw)
	if !errors.Is(err, routingstage.ErrRouteDeferred) {
		t.Fatalf("sem headroom global a chamada tem de ser ADIADA fail-closed; got %v", err)
	}
	if len(log.all()) != 0 {
		t.Fatalf("um defer NAO pode chegar ao provider; despachos=%+v", log.all())
	}

	// NEGATIVO: a MESMA composição com headroom disponível despacha.
	srv2, log2 := recordingProviderServer(t)
	cfg2 := prodConfig(audit.NewMemStore(), srv2.URL, srv2.Client(), twoRegionAccounts())
	cfg2.Routing = modelgateway.RoutingConfig{
		Tiers: chainTiers(), Profile: "cheap",
		Admission: router.NewStaticAdmissionCoordinator(1_000_000, 10, 750*time.Millisecond),
	}
	gw2, err := modelgateway.NewProduction(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("NewProduction (controlo com headroom): %v", err)
	}
	if err := chatEU(gw2); err != nil {
		t.Fatalf("com headroom global a chamada devia passar; got %v", err)
	}
	if len(log2.all()) != 1 {
		t.Fatalf("com headroom o provider devia ser invocado 1x; got %d", len(log2.all()))
	}
}

// (c) ORÇAMENTO — a exaustão graciosa desce para o tier mais barato AINDA CAPAZ; sem
// pressão de orçamento, mantém-se o eleito pelo score. A capacidade exigida nunca é
// violada (ambos os tiers são Standard, que é o piso desta chamada).
func TestAOS280_Chain_BudgetDegradesToCheaperCapableTier(t *testing.T) {
	// Perfil "quality" com task-fit a favor do gpt-4o: sem pressão, o score elege o
	// modelo caro — o que torna a degradação observável (senão o barato já era o eleito).
	routing := func(budget degradation.BudgetProvider) modelgateway.RoutingConfig {
		return modelgateway.RoutingConfig{
			Tiers:   chainTiers(),
			Profile: "quality",
			TaskFit: taskFitFavouring("gpt-4o"),
			Budget:  budget,
		}
	}
	key := degradation.BudgetKey{Board: "board-eu", Tenant: "board-eu"}

	// POSITIVO (90% do orçamento): degrada para o tier capaz mais barato.
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = routing(degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(key, degradation.BudgetState{Used: 90, Limit: 100}))
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if err := chatEU(gw); err != nil {
		t.Fatalf("a exaustao graciosa nao pode falhar a chamada: %v", err)
	}
	if got := log.last(t).Model; got != "gpt-4o-mini" {
		t.Fatalf("a ~90%% do orcamento o GW devia degradar para o tier capaz mais barato (gpt-4o-mini), despachou %q", got)
	}

	// NEGATIVO (10% do orçamento): sem pressão, despacha o eleito pelo score.
	srv2, log2 := recordingProviderServer(t)
	cfg2 := prodConfig(audit.NewMemStore(), srv2.URL, srv2.Client(), twoRegionAccounts())
	cfg2.Routing = routing(degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(key, degradation.BudgetState{Used: 10, Limit: 100}))
	gw2, err := modelgateway.NewProduction(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("NewProduction (controlo sem pressao): %v", err)
	}
	if err := chatEU(gw2); err != nil {
		t.Fatalf("chamada sem pressao de orcamento: %v", err)
	}
	if got := log2.last(t).Model; got != "gpt-4o" {
		t.Fatalf("sem pressao de orcamento o eleito pelo score (gpt-4o) devia ser despachado, despachou %q", got)
	}
}

// (d) SCORING — com QUATRO candidatos (2 regiões × 2 tiers), a ORDENAÇÃO é a soma
// ponderada da tabela ASSINADA: trocar SÓ o perfil de pesos troca o modelo despachado.
// É o que prova que o ranking ordena de facto — e não que um caminho fixo coincide
// com o resultado esperado.
func TestAOS280_Chain_ScoringOrdersSurvivorsByProfile(t *testing.T) {
	tab, err := weights.LoadTable()
	if err != nil {
		t.Fatalf("weights.LoadTable (artefacto assinado): %v", err)
	}
	run := func(t *testing.T, rc modelgateway.RoutingConfig) (dispatch, []router.Decision) {
		t.Helper()
		var decisions []router.Decision
		srv, log := recordingProviderServer(t)
		cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
		rc.Tiers = chainTiers()
		rc.DecisionSink = router.DecisionSinkFunc(func(_ context.Context, d router.Decision) {
			decisions = append(decisions, d)
		})
		cfg.Routing = rc
		gw, err := modelgateway.NewProduction(context.Background(), cfg)
		if err != nil {
			t.Fatalf("NewProduction: %v", err)
		}
		if err := chatEU(gw); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		return log.last(t), decisions
	}

	// Perfil "cheap" (o custo domina) ⇒ o tier capaz mais barato.
	cheap, cheapDecisions := run(t, modelgateway.RoutingConfig{Profile: "cheap"})
	if cheap.Model != "gpt-4o-mini" {
		t.Fatalf("perfil 'cheap' devia eleger o tier capaz mais barato (gpt-4o-mini), despachou %q", cheap.Model)
	}
	// Perfil "quality" (o task-fit domina), com o sinal de eval a favor do gpt-4o.
	quality, qualityDecisions := run(t, modelgateway.RoutingConfig{
		Profile: "quality", TaskFit: taskFitFavouring("gpt-4o"),
	})
	if quality.Model != "gpt-4o" {
		t.Fatalf("perfil 'quality' com task-fit a favor devia eleger gpt-4o, despachou %q", quality.Model)
	}

	// A decisão registada tem de ser PONTUADA e ligada à tabela ASSINADA em vigor —
	// sem isso, o modelo certo teria saído por acaso e não pelo ranking.
	for _, tc := range []struct {
		name    string
		decs    []router.Decision
		profile string
	}{
		{"cheap", cheapDecisions, "cheap"},
		{"quality", qualityDecisions, "quality"},
	} {
		if len(tc.decs) == 0 {
			t.Fatalf("%s: o DecisionSink do router nao recebeu decisao alguma (o refino nao correu)", tc.name)
		}
		d := tc.decs[len(tc.decs)-1]
		if !d.Scored {
			t.Fatalf("%s: a decisao tem de ser PONTUADA (scoring armado no caminho de producao): %+v", tc.name, d)
		}
		if d.ScoreProfile != tc.profile {
			t.Fatalf("%s: perfil aplicado = %q", tc.name, d.ScoreProfile)
		}
		if d.WeightsVersion != tab.Version() {
			t.Fatalf("%s: a decisao tem de citar a versao da tabela assinada %q, cita %q", tc.name, tab.Version(), d.WeightsVersion)
		}
	}
}

// (e) FAILOVER POR SAÚDE — a decisão do failover SOBREVIVE ao refino.
//
// O primário da região pedida (eu) está doente; o failover escolhe eu-west, dentro da
// fronteira. O refino corre a seguir e NÃO pode trazer a chamada de volta para eu.
// Duas coisas o garantem, e o teste prova as duas: a região de entrada do refino é a
// RESOLVIDA (não a pedida) e o endpoint doente não é sequer oferecido como candidato.
func TestAOS280_Chain_HealthFailoverSurvivesRefinement(t *testing.T) {
	accounts := []modelgateway.InfraAccount{
		// KeyIDs escolhidos para que o failover (que desempata por KeyID ascendente)
		// eleja a conta SAUDÁVEL de eu-west.
		{KeyID: "k2-eu", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000},
		{KeyID: "k1-euw", Provider: "openai", Region: "eu-west", LimitRPM: 100, LimitTPM: 100000},
	}
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), accounts)
	cfg.Health = func(keyID, _ string) bool { return keyID == "k1-euw" } // o primário de eu está doente
	var decisions []router.Decision
	cfg.Routing = modelgateway.RoutingConfig{
		Tiers: chainTiers(), Profile: "cheap",
		DecisionSink: router.DecisionSinkFunc(func(_ context.Context, d router.Decision) {
			decisions = append(decisions, d)
		}),
	}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if err := chatEU(gw); err != nil {
		t.Fatalf("o failover por saude devia servir a chamada: %v", err)
	}
	// A REGIÃO efectiva lê-se do bearer: cada região tem a sua credencial de infra.
	if got := log.last(t).Auth; !strings.Contains(got, "sk-infra-euw") {
		t.Fatalf("a chamada devia sair pela conta de eu-west escolhida pelo failover; bearer=%q", got)
	}
	if len(decisions) == 0 {
		t.Fatal("o refino nao correu (sem decisao no sink) — o teste nao provaria nada")
	}
	if got := decisions[len(decisions)-1].Region; got != "eu-west" {
		t.Fatalf("o refino tem de partir da regiao RESOLVIDA pelo failover (eu-west), decidiu %q", got)
	}

	// GÉMEO INDEPENDENTE (não-vacuidade): o encadeamento INGÉNUO — ancorar o refino na
	// região PEDIDA e deixar o endpoint doente na lista de candidatos — traria a chamada
	// de volta a "eu", desfazendo o failover em silêncio. Construído aqui à parte, com o
	// router REAL, para que a asserção acima não seja uma tautologia.
	naive := router.New(tiering.NewLadder(chainTiers()...),
		router.WithAllowlist(routeAllow{}),
		router.WithGuard(sovereignty.NewGuard(
			sovereignty.WithBoundary("eu", "eu-legal"),
			sovereignty.WithBoundary("eu-west", "eu-legal"),
		)),
		router.WithLoadProvider(router.NewStaticLoadProvider(router.Headroom{WorstUsed: 50, WorstLimit: 100}).
			Set("openai", "eu", router.Headroom{WorstUsed: 1, WorstLimit: 100})),
	)
	dec, err := naive.Route(context.Background(), router.Request{
		Board: "board-eu", Tenant: "board-eu", Provider: "openai",
		Region:     "eu", // a região PEDIDA — o erro que o encadeamento ingénuo cometeria
		Capability: tiering.CapabilityStandard, Class: tiering.ClassInteractive,
		Candidates: []sovereignty.Endpoint{{KeyID: "k2-eu", Region: "eu"}, {KeyID: "k1-euw", Region: "eu-west"}},
	})
	if err != nil {
		t.Fatalf("gemeo: Route: %v", err)
	}
	if dec.Region != "eu" {
		t.Fatalf("gemeo nao-vacuo: o encadeamento ingenuo tinha de voltar a %q, deu %q — a assercao acima nao prova nada", "eu", dec.Region)
	}
}

// SEM ESCADA DECLARADA — o slot de roteamento fica exactamente como antes de AOS-280
// (só failover): o modelo pedido é o despachado, sem refino nenhum. É a guarda
// anti-regressão de quem não configurou nada.
func TestAOS280_NoTiersDeclared_KeepsFailoverOnly(t *testing.T) {
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if err := chatEU(gw); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := log.last(t).Model; got != "gpt-4o" {
		t.Fatalf("sem escada declarada o modelo pedido tem de ser o despachado, despachou %q", got)
	}
}

// MODELO FORA DA ESCADA — a chamada NÃO é recusada nem roteada às cegas: mantém-se a
// rota que a guarda de soberania resolveu, com a razão registada. Uma escada mal
// declarada é uma optimização perdida, nunca uma interrupção do caminho quente.
func TestAOS280_ModelOutsideLadder_KeepsSovereignRouteWithoutRefinement(t *testing.T) {
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	// A escada só declara o gpt-4o-mini; a chamada pede gpt-4o (permitido pela
	// allowlist do board, mas não caracterizado pelo deployment).
	cfg.Routing = modelgateway.RoutingConfig{
		Tiers:   []tiering.Tier{{Name: "economy", Model: "gpt-4o-mini", CostRank: 1, Capability: tiering.CapabilityStandard}},
		Profile: "cheap",
	}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if err := chatEU(gw); err != nil {
		t.Fatalf("um modelo fora da escada nao pode falhar a chamada: %v", err)
	}
	if got := log.last(t).Model; got != "gpt-4o" {
		t.Fatalf("sem caracterizacao o modelo pedido mantem-se (%q despachado)", got)
	}
}

// ARRANQUE FAIL-CLOSED — um perfil de pesos que a tabela ASSINADA não conhece é
// recusado no ARRANQUE (uma vez, diagnosticável), não em cada chamada de modelo.
func TestAOS280_UnknownWeightProfile_FailsClosedAtBoot(t *testing.T) {
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{
		Tiers:          chainTiers(),
		ProfileByClass: map[tiering.Class]string{tiering.ClassBatch: "perfil-que-nao-existe"},
	}
	if _, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrRoutingProfileUnknown) {
		t.Fatalf("um perfil desconhecido tem de recusar o ARRANQUE fail-closed; got %v", err)
	}
	if len(log.all()) != 0 {
		t.Fatalf("nenhum trafego devia ter sido servido; despachos=%+v", log.all())
	}
}

// ESCADA VAZIA — pedir o refino com uma escada inutilizável é recusa de arranque
// (nunca um router sem escada, que rejeitaria toda a chamada em runtime).
func TestAOS280_EmptyLadder_FailsClosedAtBoot(t *testing.T) {
	srv := okOpenAIServer(t, new(int))
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{Tiers: []tiering.Tier{{Model: "sem-nome"}}}
	if _, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrNoRoutingTiers) {
		t.Fatalf("uma escada sem tiers utilizaveis tem de recusar o arranque; got %v", err)
	}
}

// CONFIG PARCIAL — uma [RoutingConfig] preenchida SEM Tiers não pode arrancar verde:
// os controlos que ela declara (aqui a coordenação de admissão GLOBAL do ADR-008) não
// seriam compostos, e o operador ficaria convencido de que o tecto partilhado está a
// ser reservado quando nada o reserva.
//
// A assimetria que este teste fecha: `Tiers: [{Model:"sem-nome"}]` já era recusa de
// boot, mas `Tiers: nil` com o resto todo preenchido era um no-op SILENCIOSO.
func TestAOS280_PartialRoutingConfigWithoutTiers_FailsClosedAtBoot(t *testing.T) {
	// POSITIVO: admissão + orçamento + perfil + sink, sem escada ⇒ recusa de arranque.
	adm := router.NewStaticAdmissionCoordinator(1_000_000, 1, 750*time.Millisecond)
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{Profile: "cheap", Admission: adm}
	if _, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrNoRoutingTiers) {
		t.Fatalf("uma RoutingConfig preenchida sem Tiers tem de recusar o arranque; got %v", err)
	}
	if len(log.all()) != 0 {
		t.Fatalf("nenhum trafego devia ter sido servido; despachos=%+v", log.all())
	}

	// NEGATIVO (não-regressão): o VALOR-ZERO continua a ser «sem refino» e arranca.
	srv2, log2 := recordingProviderServer(t)
	cfg2 := prodConfig(audit.NewMemStore(), srv2.URL, srv2.Client(), twoRegionAccounts())
	cfg2.Routing = modelgateway.RoutingConfig{}
	gw2, err := modelgateway.NewProduction(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("o valor-zero da RoutingConfig tem de continuar a arrancar (retro-compat): %v", err)
	}
	if err := chatEU(gw2); err != nil {
		t.Fatalf("Chat sem refino declarado: %v", err)
	}
	if got := log2.last(t).Model; got != "gpt-4o" {
		t.Fatalf("sem refino o modelo pedido tem de ser o despachado, despachou %q", got)
	}

	// GUARD DO PRÓPRIO GUARD: o predicado que distingue zero-valor de config parcial
	// enumera os campos à mão. Se alguém acrescentar um campo a RoutingConfig e se
	// esquecer dele, um deployment que só preencha ESSE campo volta a ser um no-op
	// silencioso. A contagem por reflexão obriga a revisitar [declaresRefinement].
	if n := reflect.TypeOf(modelgateway.RoutingConfig{}).NumField(); n != routingConfigFields {
		t.Fatalf("RoutingConfig passou de %d para %d campos: rever declaresRefinement "+
			"(um campo novo fora do predicado reabre o no-op silencioso)", routingConfigFields, n)
	}
}

// routingConfigFields é o número de campos de [modelgateway.RoutingConfig] no momento
// em que [declaresRefinement] foi escrita. Não é cosmética: é o gatilho que obriga
// quem acrescenta um campo a decidir se ele PEDE refino.
const routingConfigFields = 13

// COBERTURA DE PREÇO — a escada cruzada com a contabilidade de custo NO ARRANQUE.
// Sem esta guarda, o par sem preço só falha depois de o provider ter sido invocado e
// FACTURADO (o custo calcula-se com o usage em mão), e só quando aquele tier ganha.
func TestAOS280_UnpricedLadderTier_FailsClosedAtBoot(t *testing.T) {
	// POSITIVO: a tabela cobre (gpt-4o, eu|eu-west) mas não o gpt-4o-mini — que a
	// escada declara e a degradação por orçamento elegeria.
	partial := mustPricingRecorder(t, []pricing.Entry{
		{Model: "gpt-4o", Region: "eu", Rate: testRate},
		{Model: "gpt-4o", Region: "eu-west", Rate: testRate},
	})
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Cost = partial
	cfg.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "cheap"}
	_, err := modelgateway.NewProduction(context.Background(), cfg)
	if !errors.Is(err, modelgateway.ErrRoutingPriceCoverage) {
		t.Fatalf("um modelo da escada sem preco numa regiao alcancavel tem de recusar o ARRANQUE; got %v", err)
	}
	if !strings.Contains(err.Error(), "gpt-4o-mini") {
		t.Fatalf("a recusa tem de NOMEAR o par em falta (diagnosticavel): %v", err)
	}
	if len(log.all()) != 0 {
		t.Fatalf("nada devia ter sido despachado (nem facturado); despachos=%+v", log.all())
	}

	// NEGATIVO: a MESMA composição com a tabela completa arranca e despacha — a guarda
	// não é um bloqueio universal disfarçado.
	full := mustPricingRecorder(t, []pricing.Entry{
		{Model: "gpt-4o", Region: "eu", Rate: testRate},
		{Model: "gpt-4o", Region: "eu-west", Rate: testRate},
		{Model: "gpt-4o-mini", Region: "eu", Rate: testRate},
		{Model: "gpt-4o-mini", Region: "eu-west", Rate: testRate},
	})
	srv2, log2 := recordingProviderServer(t)
	cfg2 := prodConfig(audit.NewMemStore(), srv2.URL, srv2.Client(), twoRegionAccounts())
	cfg2.Cost = full
	cfg2.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "cheap"}
	gw2, err := modelgateway.NewProduction(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("com a cobertura completa o GW tem de arrancar: %v", err)
	}
	if err := chatEU(gw2); err != nil {
		t.Fatalf("Chat com preco coberto: %v", err)
	}
	if got := log2.last(t).Model; got != "gpt-4o-mini" {
		t.Fatalf("o perfil 'cheap' devia despachar o tier capaz mais barato, despachou %q", got)
	}

	// SEM CONTABILIDADE (não-regressão): sem recorder de custo não há invariante a
	// impor — nenhuma chamada é recusada por falta de preço — e o arranque mantém-se.
	srv3, _ := recordingProviderServer(t)
	cfg3 := prodConfig(audit.NewMemStore(), srv3.URL, srv3.Client(), twoRegionAccounts())
	cfg3.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "cheap"}
	if _, err := modelgateway.NewProduction(context.Background(), cfg3); err != nil {
		t.Fatalf("sem contabilidade de custo o arranque nao pode passar a falhar: %v", err)
	}
}

// testRate é um preço qualquer (não-negativo) — o que se testa é a COBERTURA, não o
// valor. Inteiro, como todo o eixo de dinheiro (ADR-008).
var testRate = pricing.Rate{
	InputPerMTokMicroUSD:      1_000_000,
	OutputPerMTokMicroUSD:     2_000_000,
	CacheReadPerMTokMicroUSD:  100_000,
	CacheWritePerMTokMicroUSD: 0,
}

// mustPricingRecorder constrói o recorder de custo REAL (tabela → calculador →
// recorder) sobre as entradas dadas.
func mustPricingRecorder(t *testing.T, entries []pricing.Entry) *cost.Recorder {
	t.Helper()
	tab, err := pricing.NewTable("teste-cobertura/v1", entries)
	if err != nil {
		t.Fatalf("pricing.NewTable: %v", err)
	}
	return cost.NewRecorder(cost.NewCalculator(tab))
}

// TROCA DE MODELO SELADA NO WORM — quando o refino despacha um modelo DIFERENTE do
// pedido, a decisão de governação do par EFECTIVO fica no mesmo trilho tamper-evident
// em que o deny cross-border já ficava. Sem isto, um auditor que lesse a partição do
// board concluiria que ele consumiu o modelo PEDIDO.
func TestAOS280_Chain_ModelSwapSealedInWORM(t *testing.T) {
	key := degradation.BudgetKey{Board: "board-eu", Tenant: "board-eu"}
	run := func(t *testing.T, used int64) (*audit.MemStore, dispatch) {
		t.Helper()
		store := audit.NewMemStore()
		srv, log := recordingProviderServer(t)
		cfg := prodConfig(store, srv.URL, srv.Client(), twoRegionAccounts())
		cfg.Routing = modelgateway.RoutingConfig{
			Tiers:   chainTiers(),
			Profile: "quality",
			TaskFit: taskFitFavouring("gpt-4o"),
			Budget: degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
				Set(key, degradation.BudgetState{Used: used, Limit: 100}),
		}
		gw, err := modelgateway.NewProduction(context.Background(), cfg)
		if err != nil {
			t.Fatalf("NewProduction: %v", err)
		}
		if err := chatEU(gw); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		return store, log.last(t)
	}

	// POSITIVO: a 90% do orçamento o refino degrada para gpt-4o-mini — e o WORM tem de
	// nomear o modelo EFECTIVAMENTE despachado.
	store, got := run(t, 90)
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("pre-condicao do teste: esperava-se a degradacao para gpt-4o-mini, despachou %q", got.Model)
	}
	if !hasSealedModel(t, store, "modelgw-gov:board-eu", audit.DecisionAllow, "gpt-4o-mini") {
		t.Fatal("a troca de modelo tem de estar SELADA no WORM para o par EFECTIVO (allow, gpt-4o-mini)")
	}
	if !hasSealedModel(t, store, "modelgw-gov:board-eu", audit.DecisionAllow, "gpt-4o") {
		t.Fatal("o registo do par PEDIDO (allowlist regional) nao pode desaparecer — os dois factos coexistem")
	}

	// NEGATIVO: sem troca (10% do orçamento) NÃO se sela um segundo registo — o eixo
	// não passa a carimbar o que não aconteceu.
	store2, got2 := run(t, 10)
	if got2.Model != "gpt-4o" {
		t.Fatalf("pre-condicao do teste: sem pressao esperava-se gpt-4o, despachou %q", got2.Model)
	}
	if hasSealedModel(t, store2, "modelgw-gov:board-eu", audit.DecisionAllow, "gpt-4o-mini") {
		t.Fatal("sem troca de modelo nao pode existir registo de um modelo que nunca foi despachado")
	}
	if n := countSealed(t, store2, "modelgw-gov:board-eu"); n != 1 {
		t.Fatalf("sem troca, a governacao do board tem exactamente 1 registo por chamada; got %d", n)
	}
}

// hasSealedModel procura na partição um registo com o veredicto e o modelo dados.
func hasSealedModel(t *testing.T, store *audit.MemStore, partition string, want audit.Decision, model string) bool {
	t.Helper()
	ctx := context.Background()
	head, _ := store.Head(ctx, partition)
	for i := uint64(1); i <= head; i++ {
		rec, ok, err := store.At(ctx, partition, i)
		if err != nil || !ok {
			continue
		}
		if rec.Decision == want && rec.Resource.Value == model {
			return true
		}
	}
	return false
}

// countSealed conta os registos de governação da partição.
func countSealed(t *testing.T, store *audit.MemStore, partition string) int {
	t.Helper()
	head, _ := store.Head(context.Background(), partition)
	return int(head)
}

// PERFIL POR OMISSÃO DESCONHECIDO — a recusa de boot tem de acusar a peça CERTA. Um
// typo no nome do perfil não pode mandar o operador depurar a assinatura/trust anchor
// da tabela de pesos embebida, que está intacta (ADR-010/AOS-011: um deny que culpa a
// peça errada manda depurar a coisa errada).
func TestAOS280_UnknownDefaultProfile_BlamesProfileNotWeightsArtefact(t *testing.T) {
	cfg := prodConfig(audit.NewMemStore(), "http://provider.invalid", http.DefaultClient, twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{Tiers: chainTiers(), Profile: "chepa"} // typo de "cheap"
	_, err := modelgateway.NewProduction(context.Background(), cfg)
	if !errors.Is(err, modelgateway.ErrRoutingProfileUnknown) {
		t.Fatalf("um perfil por omissao desconhecido tem de sair como ErrRoutingProfileUnknown; got %v", err)
	}
	if errors.Is(err, modelgateway.ErrRoutingWeights) {
		t.Fatalf("a recusa NAO pode acusar o artefacto de pesos (assinatura/trust anchor intactos): %v", err)
	}
	if !errors.Is(err, scoring.ErrProfileUnknown) {
		t.Fatalf("a cadeia do sentinela de scoring tem de ser preservada (%%w): %v", err)
	}
	// O caminho IRMÃO (perfil por classe) continua a dar o mesmo sentinela: os dois
	// modos de declarar um perfil falham da mesma maneira.
	cfg2 := prodConfig(audit.NewMemStore(), "http://provider.invalid", http.DefaultClient, twoRegionAccounts())
	cfg2.Routing = modelgateway.RoutingConfig{
		Tiers:          chainTiers(),
		ProfileByClass: map[tiering.Class]string{tiering.ClassBatch: "chepa"},
	}
	if _, err := modelgateway.NewProduction(context.Background(), cfg2); !errors.Is(err, modelgateway.ErrRoutingProfileUnknown) {
		t.Fatalf("perfil por classe desconhecido: esperado ErrRoutingProfileUnknown; got %v", err)
	}
}
