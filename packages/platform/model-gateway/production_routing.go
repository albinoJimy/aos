package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/weights"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
	"github.com/aos-ref/platform/model-gateway/routing/keypool"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// production_routing.go COMPÕE O ESTÁGIO DE ROTEAMENTO do gateway de produção
// (AOS-280, fecha DEF-271). O slot "roteamento" passa a ser uma CADEIA, na ordem em
// que o dono a decidiu (2026-08-13), com o selo de governação a fechá-la:
//
//	failover (AOS-058)  →  routingstage + router (AOS-059 + ADR-021)  →  selo WORM
//	   impõe a fronteira        refina DENTRO dela                        da troca
//
// # Porque ENCADEAR e não substituir nem fundir
//
// Cada elo faz o que o outro não faz, e nenhum é dispensável:
//
//   - o FAILOVER é o único que impõe a SOBERANIA por chamada (a fronteira derivada
//     da allowlist do board para o modelo pedido), faz failover por SAÚDE dentro
//     dessa fronteira e — decisivo — SELA o deny cross-border no audit WORM
//     ([audit.DecisionDeny] via o allowlist.Recorder de AOS-058), atribuível a
//     principal + board;
//   - o ROUTER refina dentro da fronteira já fixada: carga real (headroom TPM/RPM do
//     keypool), tier mais barato CAPAZ, degradação graciosa por orçamento, reserva
//     de admissão global e o ranking ponderado assinado de ADR-021.
//
// A alternativa — o router SUBSTITUIR o failover — foi recusada com razão nomeada: a
// saúde passaria de DECISÃO a PESO do score (um peso alto noutro factor poderia
// eleger um endpoint doente) e perder-se-ia o TRILHO DE AUDITORIA do deny de
// soberania, porque o router só tem [router.DecisionSink] — análise post-hoc, não
// audit WORM.
//
// # O que NÃO se compõe, e porquê (a armadilha do keypool)
//
// O router tem uma porta [router.KeyPool] e o gateway já escolhe a chave pooled em
// `Gateway.credential` DEPOIS do roteamento. Compor as duas faria DOIS `Select` por
// chamada — e `keypool.Pool.Select` INCREMENTA o RPM observado da conta escolhida:
// a carga contabilizada duplicaria e o pool saturaria ao dobro do ritmo real. Por
// isso [router.WithKeyPool] NÃO é composto: o router deriva o KeyID do candidato da
// região escolhida (leitura pura) e a selecção que CONSOME continua a ser a do
// gateway, uma só vez. O sinal de carga que o router lê é o mesmo pool, pela porta
// [router.LoadProvider] — [keypool.Registry.Headroom] é leitura pura.
//
// # Opt-in por ESCADA DECLARADA (e porque não há default)
//
// A cadeia só se compõe quando o deployment declara a sua ESCADA DE TIERS
// ([RoutingConfig.Tiers]) — o custo e a capacidade de cada modelo que este nó pode
// servir. Não há escada por omissão e é deliberado: adivinhá-la (por nome de modelo,
// por preço) seria inventar política de qualidade no caminho quente, e escolher um
// tier «equivalente» sem base faria o GW despachar um modelo que o operador não
// caracterizou — possivelmente sem preço na tabela (o que RECUSA a chamada) ou sem
// credencial na região. Sem escada, o slot mantém-se exactamente como antes de
// AOS-280 (só failover): nenhuma regressão para quem não declarou nada.
//
// # O que a ESCADA obriga a validar no ARRANQUE (remediação adversarial)
//
// Declarar a escada é declarar que o GW pode despachar QUALQUER um daqueles modelos —
// e não só o pedido. Duas invariantes que antes de AOS-280 eram trivialmente
// verdadeiras (o modelo despachado era sempre o PEDIDO, já validado pela allowlist e
// com preço em mão de quem compôs) deixaram de o ser, e por isso passam a ser recusa
// de BOOT ou selagem por chamada, e não uma surpresa a jusante:
//
//   - COBERTURA DE PREÇO ([ErrRoutingPriceCoverage]) — um modelo da escada sem preço
//     numa região alcançável faria o gateway INVOCAR o provider (tokens consumidos e
//     facturados a montante) e só DEPOIS falhar em `recordCost` com `pricing.ErrNoPrice`,
//     porque o custo só é calculável com o usage em mão: dinheiro gasto, chamador sem
//     resposta, e o retry a repetir o gasto. A verificação cruza a escada com a porta
//     de cobertura ([RoutingConfig.PriceCoverage], ou a de [ProductionConfig.Cost]);
//   - SELAGEM DA TROCA DE MODELO — o estágio de allowlist sela no WORM a decisão sobre
//     o par PEDIDO. Se o refino despachar OUTRO modelo, o trilho de governação diria
//     que o board consumiu o modelo pedido. O terceiro elo da cadeia
//     ([modelSwapRecorder]) sela a decisão do par EFECTIVO no MESMO eixo WORM em que o
//     deny cross-border já é selado — o [router.DecisionSink] é análise post-hoc, não
//     audit, e é precisamente essa a razão pela qual o router não substituiu o failover.

// Erros fail-closed da composição do refino de roteamento.
var (
	// ErrNoRoutingTiers — pediu-se o refino sem uma escada utilizável. Cobre os DOIS
	// modos: (i) a escada declarada não tem um único tier com nome; (ii) a
	// [RoutingConfig] foi preenchida no resto (orçamento, admissão, perfil, sinks…)
	// SEM Tiers — que não armaria nada e desligaria EM SILÊNCIO os controlos que o
	// operador configurou, com destaque para a coordenação de admissão global do
	// ADR-008. Fail-closed: quem pediu o refino obtém-no válido ou o GW não arranca —
	// nunca um router sem escada (que rejeitaria toda a chamada em runtime) nem uma
	// config que não compõe o que declara. O VALOR-ZERO da [RoutingConfig] continua a
	// ser «sem refino» e nunca produz este erro (retro-compatibilidade intacta).
	ErrNoRoutingTiers = errors.New("modelgateway: escada de tiers do roteamento vazia/invalida (fail-closed)")
	// ErrRoutingPriceCoverage — a escada declara um modelo que a contabilidade de
	// custo não sabe precificar numa região que o refino pode alcançar. Fail-closed no
	// ARRANQUE: sem esta guarda a lacuna só aparece DEPOIS de o provider ter sido
	// invocado e facturado, por chamada e só quando aquele tier ganha (pressão de
	// orçamento, perfil barato) — um modo de falha intermitente que não aparece em
	// smoke tests. Uma recusa única e diagnosticável em vez de dinheiro gasto sem
	// resposta.
	ErrRoutingPriceCoverage = errors.New("modelgateway: modelo da escada sem preco na tabela de custo para uma regiao alcancavel (fail-closed)")
	// ErrModelSwapNotSealed — o refino trocou o modelo despachado e essa decisão NÃO
	// foi selável no audit WORM. Fail-closed (ADR-010, audit-before-effect): uma
	// decisão de governação não-auditável aborta a chamada ANTES de o provider ser
	// invocado — nunca se despacha um modelo cuja autorização não ficou no trilho.
	ErrModelSwapNotSealed = errors.New("modelgateway: troca de modelo pelo roteamento nao selada no audit WORM (fail-closed)")
	// ErrRoutingWeights — a tabela de pesos EMBEBIDA e ASSINADA não carrega/verifica
	// (assinatura, trust anchor pinado, perfis). É a regra 3 do ADR-021 com efeito
	// real: com o scoring composto, sem tabela válida NÃO se rota com pesos
	// implícitos — aqui recusa-se ANTES de servir tráfego.
	ErrRoutingWeights = errors.New("modelgateway: tabela de pesos do scoring invalida/nao-verificavel (fail-closed, ADR-021 regra 3)")
	// ErrRoutingProfileUnknown — um perfil de pesos que o classificador pode pedir
	// não existe na tabela em vigor. O router rejeitaria fail-closed CADA chamada
	// dessa classe; esta guarda transforma isso numa recusa de ARRANQUE, única e
	// diagnosticável, em vez de uma interrupção por chamada em produção.
	ErrRoutingProfileUnknown = errors.New("modelgateway: perfil de pesos pedido pelo classificador nao existe na tabela assinada (fail-closed)")
)

// RoutingConfig configura o REFINO de roteamento (AOS-059 + ADR-021) encadeado a
// jusante da guarda de soberania. Zero-valor ⇒ sem refino (só failover).
type RoutingConfig struct {
	// Tiers é a ESCADA de modelos deste deployment: nome lógico, modelo concreto,
	// custo relativo (CostRank), capacidade OFERECIDA e o bit de baixa latência. É a
	// declaração que ARMA toda a cadeia — ver a nota «opt-in por escada declarada».
	// Cada modelo aqui declarado tem de estar coberto pela allowlist regional do
	// board E pela tabela de preços (quando há contabilidade de custo ligada): o
	// refino pode despachar QUALQUER um deles.
	Tiers []tiering.Tier
	// Profile é o perfil de pesos por omissão (ADR-021). Vazio ⇒ o default DECLARADO
	// na tabela assinada.
	Profile string
	// ProfileByClass mapeia a classe da chamada (interactiva/batch) a um perfil —
	// a INTENÇÃO por classe de tráfego. Nomes validados contra a tabela no arranque.
	ProfileByClass map[tiering.Class]string
	// ClassOf deriva a classe da chamada dos hints do [pipeline.Exchange]. Nil ⇒
	// tudo interactivo (ver [routingstage.ClassifierConfig.ClassOf]).
	ClassOf func(ex *pipeline.Exchange) tiering.Class
	// Load é o sinal de CARGA por (provider, região). Nil ⇒ derivado do KEYPOOL
	// construído das mesmas contas de infra ([keypool.Registry.Headroom]) — a fonte
	// real, sem contabilidade paralela.
	Load router.LoadProvider
	// DEFERIDO (DEF-280-PORTAS) — Budget é a porta de ORÇAMENTO da degradação graciosa
	// (~80% ⇒ oferece descer para um tier mais barato AINDA capaz). Nil ⇒ sem oferta de
	// degradação. O burn-down real vive no control-plane (EPIC-03): é o composition root que
	// liga esta porta, porque o módulo do GW não importa control-plane no caminho quente — e
	// nenhum o liga hoje, pelo que a seam existe sem fonte.
	Budget degradation.BudgetProvider
	// Admission coordena com o admission control GLOBAL (ADR-008): sem headroom, a
	// chamada é ADIADA em vez de saturar o tecto partilhado. Nil ⇒ sem coordenação
	// (comportamento anterior a AOS-280).
	Admission router.AdmissionCoordinator
	// DecisionSink recebe cada decisão de roteamento (modelo/tier/razão/score) para
	// análise de custo post-hoc e calibração OFFLINE dos pesos (ADR-021 regra 4).
	DecisionSink router.DecisionSink
	// TaskFit é o factor de QUALIDADE por (modelo, capacidade) — o sinal que o eval
	// harness (EPIC-08) produz OFFLINE. Nil ⇒ sem crédito de qualidade (0 para
	// todos): o lado seguro, um modelo sem evidência de eval não ganha pontos.
	TaskFit scoring.FactorProvider
	// Stability é a taxa de sucesso histórica por modelo, calibrada OFFLINE. Nil ⇒
	// 0 para todos (não discrimina; nunca crédito não-ganho).
	Stability scoring.FactorProvider
	// Latency é o p95 medido OFFLINE por modelo. Nil ⇒ [scoring.NewStaticLatency]
	// com recurso ao bit Fast da escada, que preserva a semântica de classe de
	// AOS-059 (batch não paga bónus de rapidez).
	Latency scoring.FactorProvider
	// Health é o factor de SAÚDE por (provider, região) do scoring. Nil ⇒ derivado
	// da MESMA [ProductionConfig.Health] do failover sobre o inventário: uma região
	// com pelo menos um endpoint saudável vale [scoring.Scale], sem nenhum vale 0.
	// (A saúde continua a ser DECISÃO no failover — este factor só desempata.)
	Health scoring.FactorProvider
	// PriceCoverage é a porta de COBERTURA DE PREÇO usada na validação de arranque
	// ([ErrRoutingPriceCoverage]): responde se o par (modelo, região) é
	// contabilizável. Nil ⇒ a cobertura de [ProductionConfig.Cost] (quando há
	// contabilidade ligada). Existe para o deployment cuja contabilidade não é o
	// *cost.Recorder do módulo poder impor a MESMA invariante em vez de a deixar em
	// prosa. Sem contabilidade nenhuma, não há verificação — nem faria sentido:
	// nenhuma chamada seria recusada por falta de preço.
	PriceCoverage func(model, region string) bool
}

// declaresRefinement reporta se a [RoutingConfig] PEDE refino — isto é, se algum
// campo saiu do valor-zero. É o que distingue «não configurei roteamento» (postura
// retro-compatível: o slot fica só com o failover) de «configurei e falta-me a
// escada» (fail-closed, [ErrNoRoutingTiers]): sem esta distinção, uma config com
// Admission/Budget/Profile mas sem Tiers desligava em silêncio os controlos que o
// operador julga estar a compor.
//
// A lista é EXAUSTIVA por construção e há um guard-test que a mantém assim: conta os
// campos de [RoutingConfig] por reflexão e fica vermelho quando alguém acrescenta um
// campo sem o considerar aqui (um campo esquecido reintroduziria exactamente o
// silêncio que este predicado existe para não ter).
func (rc RoutingConfig) declaresRefinement() bool {
	return len(rc.Tiers) > 0 ||
		rc.Profile != "" ||
		len(rc.ProfileByClass) > 0 ||
		rc.ClassOf != nil ||
		rc.Load != nil ||
		rc.Budget != nil ||
		rc.Admission != nil ||
		rc.DecisionSink != nil ||
		rc.TaskFit != nil ||
		rc.Stability != nil ||
		rc.Latency != nil ||
		rc.Health != nil ||
		rc.PriceCoverage != nil
}

// composeRoutingStage constrói o estágio do SLOT "roteamento": o failover sozinho
// (comportamento anterior) ou a CADEIA failover → refino, quando a escada de tiers
// está declarada. Fail-closed em cada passo do refino.
func composeRoutingStage(
	fail pipeline.Stage,
	cfg ProductionConfig,
	pol *allowlist.Policy,
	inv failover.Inventory,
	kp *keypool.Registry,
	gov *allowlist.Recorder,
) (pipeline.Stage, error) {
	refine, err := newRefineStage(cfg, pol, inv, kp)
	if err != nil {
		return nil, err
	}
	if refine == nil {
		return fail, nil
	}
	// O TERCEIRO elo sela no WORM a troca de modelo que o refino acabou de decidir (ver
	// o cabeçalho): corre DEPOIS do refino — só aí se sabe o par efectivo — e ANTES de
	// qualquer efeito, porque o slot inteiro precede a invocação do provider.
	stages := []pipeline.Stage{fail, refine, &modelSwapRecorder{slot: fail.Name(), rec: gov, pol: pol}}
	// O nome do slot vem do estágio que já o ocupava — não de uma constante repetida
	// aqui, que poderia divergir e desalinhar o rasto de decisões/StageError.
	return pipeline.Chain{StageName: fail.Name(), Stages: stages}, nil
}

// newRefineStage constrói o estágio de refino (routingstage + router armado). Devolve
// (nil, nil) quando o deployment não declarou escada — o caso «sem refino».
func newRefineStage(
	cfg ProductionConfig,
	pol *allowlist.Policy,
	inv failover.Inventory,
	kp *keypool.Registry,
) (pipeline.Stage, error) {
	rc := cfg.Routing
	if len(rc.Tiers) == 0 {
		if !rc.declaresRefinement() {
			return nil, nil // valor-zero: sem refino (a postura retro-compatível)
		}
		// Config PARCIAL: pediram-se controlos (admissão, orçamento, perfil, sinks) que
		// só o refino compõe, mas não a escada que o arma. Recusar é o único desfecho
		// honesto — devolver (nil, nil) aqui deixaria o operador convencido de que o
		// tecto global do ADR-008 estava a ser reservado quando nada o reserva.
		return nil, fmt.Errorf("%w: RoutingConfig preenchida SEM Tiers — os controlos declarados "+
			"(admissao/orcamento/perfil/sinks/factores) nao seriam compostos", ErrNoRoutingTiers)
	}
	ladder := tiering.NewLadder(rc.Tiers...)
	if len(ladder.Tiers()) == 0 {
		return nil, ErrNoRoutingTiers
	}

	// CARGA — a porta é a mesma que o ranking ponderado consome pelo factor headroom
	// (router.HeadroomReaderFrom): um só sinal de carga, não dois.
	load := rc.Load
	if load == nil {
		load = keypoolLoad{reg: kp}
	}

	// SCORING (ADR-021) — tabela EMBEBIDA e ASSINADA (ed25519 + trust anchor pinado),
	// carregamento fail-closed. É AQUI que a regra 3 deixa de ser inerte: o scoring
	// passa a estar COMPOSTO no caminho de produção e, sem tabela válida, o GW não
	// arranca (e um scorer não-armado faria o router recusar cada rota).
	tab, err := weights.LoadTable()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRoutingWeights, err)
	}
	sc, err := scoring.NewScorer(scoring.TableFrom(tab), rc.Profile,
		scoring.WithCost(scoring.CostFromLadder(ladder)),
		scoring.WithHeadroom(scoring.HeadroomFromReader(router.HeadroomReaderFrom(load))),
		scoring.WithLatency(orFactor(rc.Latency, scoring.NewStaticLatency(true))),
		scoring.WithHealth(orFactor(rc.Health, healthFactor(cfg.Health, inv))),
		scoring.WithTaskFit(orFactor(rc.TaskFit, scoring.NewStaticTaskFit(0))),
		scoring.WithStability(orFactor(rc.Stability, scoring.NewStaticStability(0))),
	)
	if err != nil {
		// ATRIBUIBILIDADE DO ERRO (ADR-010/AOS-011 — «um deny que culpa a peça errada
		// manda o operador depurar a coisa errada»). Um NOME de perfil que a tabela não
		// conhece é um typo do operador, não um artefacto de pesos corrompido: sai com
		// [ErrRoutingProfileUnknown], igual ao caminho irmão (ProfileByClass), e não a
		// acusar a assinatura/trust anchor da tabela EMBEBIDA — que está intacta. A
		// cadeia é preservada com %w nos dois ramos (o sentinela de scoring continua
		// comparável por errors.Is).
		if errors.Is(err, scoring.ErrProfileUnknown) {
			return nil, fmt.Errorf("%w: perfil por omissao %q (perfis da tabela %s: %s): %w",
				ErrRoutingProfileUnknown, rc.Profile, tab.Version(), strings.Join(tab.Names(), ", "), err)
		}
		return nil, fmt.Errorf("%w: %w", ErrRoutingWeights, err)
	}

	// CLASSIFICADOR DE PRODUÇÃO — povoa candidatos (inventário ∩ regiões legais ∩
	// saudáveis) e resolve o perfil de pesos da classe. Sem ele o refino seria
	// degenerado (ver o cabeçalho de routingstage/classifier.go).
	cls, err := routingstage.NewProductionClassifier(routingstage.ClassifierConfig{
		Policy:         pol,
		Inventory:      inv,
		Ladder:         ladder,
		Health:         adaptHealth(cfg.Health),
		ClassOf:        rc.ClassOf,
		ProfileByClass: rc.ProfileByClass,
		DefaultProfile: rc.Profile,
	})
	if err != nil {
		return nil, err
	}
	// Validação de ARRANQUE dos perfis: um nome que a tabela não conhece é recusa de
	// boot, não uma rejeição por chamada em produção.
	for _, p := range cls.Profiles() {
		if !sc.HasProfile(p) {
			return nil, fmt.Errorf("%w: %q (perfis da tabela %s: %s)",
				ErrRoutingProfileUnknown, p, tab.Version(), strings.Join(tab.Names(), ", "))
		}
	}
	// COBERTURA DE PREÇO — a escada cruzada com a contabilidade, no arranque. Ver o
	// cabeçalho: o custo é fail-closed DEPOIS de o provider ser invocado, pelo que a
	// lacuna tem de ser apanhada AQUI.
	if cover := priceCoverageOf(cfg); cover != nil {
		if gaps := unpricedLadderPairs(ladder, pol, cfg.Accounts, cover); len(gaps) > 0 {
			return nil, fmt.Errorf("%w: pares (modelo,regiao) sem preco: %s", ErrRoutingPriceCoverage, strings.Join(gaps, ", "))
		}
	}

	opts := []router.Option{
		// SOBERANIA — guarda derivada da MESMA policy que é fronteira do failover.
		router.WithGuard(guardFromPolicy(pol)),
		// ALLOWLIST — a fronteira EXACTA por (board, modelo, região): corre como
		// GUARDA antes do ranking, pelo que nenhum peso elege um par não permitido.
		router.WithAllowlist(routingstage.AllowlistFrom(pol)),
		router.WithLoadProvider(load),
		router.WithScoring(sc),
	}
	if rc.Budget != nil {
		opts = append(opts, router.WithBudget(rc.Budget))
	}
	if rc.Admission != nil {
		opts = append(opts, router.WithAdmission(rc.Admission))
	}
	if rc.DecisionSink != nil {
		opts = append(opts, router.WithDecisionSink(rc.DecisionSink))
	}
	if cfg.Tracer != nil {
		opts = append(opts, router.WithTracer(cfg.Tracer))
	}
	// NOTA: router.WithKeyPool NÃO entra — ver a armadilha do duplo Select no
	// cabeçalho deste ficheiro.
	return routingstage.NewStage(router.New(ladder, opts...),
		routingstage.WithClassifier(cls.Classifier())), nil
}

// priceCoverageOf escolhe a fonte da COBERTURA de preço para a validação de arranque:
// a porta explícita do deployment, ou a da contabilidade composta. Sem contabilidade
// ligada devolve nil — e é correcto: sem recorder de custo nenhuma chamada é recusada
// por falta de preço, logo não há invariante a impor (ver [ProductionConfig.Cost]).
func priceCoverageOf(cfg ProductionConfig) func(model, region string) bool {
	if cfg.Routing.PriceCoverage != nil {
		return cfg.Routing.PriceCoverage
	}
	if cfg.Cost != nil {
		return cfg.Cost.HasPrice
	}
	return nil
}

// unpricedLadderPairs devolve, ORDENADOS, os pares (modelo, região) que o refino pode
// despachar e que a contabilidade NÃO sabe precificar. O produto verificado é o
// ALCANÇÁVEL, não o cartesiano:
//
//	modelos  = os da escada declarada (o refino pode eleger ou degradar para qualquer um);
//	regiões  = as das contas de infra deste nó (só lá há endpoint para servir);
//	filtro   = a allowlist tem de permitir o par a ALGUM board (senão nunca é despachado).
//
// Verificar o cartesiano puro recusaria arranques legítimos (um modelo que a policy
// nunca autoriza naquela região não precisa de preço); verificar menos do que isto
// deixaria passar exactamente o par que a degradação por orçamento vai escolher.
//
// Determinístico: a ordem de iteração dos mapas não influencia o RESULTADO (a
// alcançabilidade é um OU lógico) e a saída é ordenada antes de devolver.
func unpricedLadderPairs(
	ladder *tiering.Ladder,
	pol *allowlist.Policy,
	accounts []InfraAccount,
	hasPrice func(model, region string) bool,
) []string {
	if ladder == nil || pol == nil || hasPrice == nil {
		return nil
	}
	boards := make(map[string]struct{}, len(pol.Rules))
	for _, r := range pol.Rules {
		if r.Board != "" {
			boards[r.Board] = struct{}{} // inclui o wildcard: é um board avaliável como outro qualquer
		}
	}
	regions := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		if a.Region != "" {
			regions[a.Region] = struct{}{}
		}
	}
	gaps := make(map[string]struct{})
	for _, t := range ladder.Tiers() {
		if t.Model == "" {
			continue
		}
		for reg := range regions {
			reachable := false
			for b := range boards {
				if pol.Evaluate(allowlist.Input{Board: b, Model: t.Model, Region: reg}) == allowlist.EffectAllow {
					reachable = true
					break
				}
			}
			if !reachable {
				continue // a allowlist nunca autoriza este par: o refino não o pode despachar
			}
			if !hasPrice(t.Model, reg) {
				gaps[t.Model+"@"+reg] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(gaps))
	for g := range gaps {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// DEFERIDO (DEF-280-REGIAO) — modelSwapRecorder é o TERCEIRO elo do slot de roteamento: sela
// no audit WORM a decisão de governação do par EFECTIVO quando — e só quando — o refino trocou o
// MODELO despachado. Uma mudança só de REGIÃO, com o mesmo modelo, não produz este selo.
//
// # Porque é preciso (e porque não basta o DecisionSink)
//
// O estágio de allowlist (AOS-058) sela a decisão sobre o par PEDIDO, que era o
// despachado até AOS-280. Com o refino composto, um auditor que lesse a partição
// `modelgw-gov:<board>` veria `allow (modelo pedido)` e concluiria que o board
// consumiu esse modelo — enquanto o provider recebeu outro. A verificação do par
// efectivo existe no router (a allowlist corre como guarda ANTES do ranking), mas o
// seu resultado ia só para o span e para o [router.DecisionSink] — análise post-hoc,
// não trilho de auditoria. Este elo põe a troca no MESMO eixo WORM em que o deny
// cross-border já é selado, atribuível a principal + board.
//
// # Alcance declarado
//
// Sela a troca de MODELO (o facto novo que AOS-280 introduz), com o par completo
// (modelo E região efectivos). Uma resolução que mude só a REGIÃO dentro da fronteira
// já validada mantém a postura de AOS-058 — é a guarda de soberania que a decide e o
// registo de atribuição (`Gateway.attribute`) que a sela —, inalterada por este ticket.
type modelSwapRecorder struct {
	slot string
	rec  *allowlist.Recorder
	pol  *allowlist.Policy
}

// Name implementa [pipeline.Stage] com o nome do SLOT (o rasto continua a indexar o
// papel, como o resto da cadeia).
func (s *modelSwapRecorder) Name() string { return s.slot }

// Process implementa [pipeline.Stage]. No-op quando não houve troca de modelo — o
// caso esmagadoramente comum, e por isso o custo em caminho quente é uma comparação
// de strings.
func (s *modelSwapRecorder) Process(ctx context.Context, ex *pipeline.Exchange) error {
	if s.rec == nil || ex.ResolvedModel == "" || ex.ResolvedModel == ex.RequestedModel {
		return nil
	}
	region := ex.ResolvedRegion
	if region == "" {
		region = ex.RequestedRegion
	}
	version := ""
	if s.pol != nil {
		version = s.pol.Version()
	}
	reason := fmt.Sprintf("roteamento trocou o modelo despachado: %q -> %q (regiao %q); %s",
		ex.RequestedModel, ex.ResolvedModel, region, lastRoutingReason(ex))

	// DEFESA EM PROFUNDIDADE: sela-se o que se VERIFICA. A allowlist já correu como
	// guarda dentro do router; reavaliá-la aqui custa uma travessia de regras e garante
	// que o registo selado não afirma um allow que a policy em vigor não sustenta. Um
	// deny nesta posição é impossível pelo desenho — se acontecer, é a composição que
	// está partida e a chamada morre fail-closed, com o deny selado.
	decision := audit.DecisionAllow
	denied := s.pol == nil || s.pol.Evaluate(allowlist.Input{Board: ex.Board, Model: ex.ResolvedModel, Region: region}) != allowlist.EffectAllow
	if denied {
		decision = audit.DecisionDeny
		reason = "par EFECTIVO fora da allowlist regional apos o refino; " + reason
	}
	if _, err := s.rec.Seal(ctx, allowlist.GovRecord{
		Board:           ex.Board,
		PrincipalUser:   ex.PrincipalUser,
		PrincipalAgent:  ex.PrincipalAgent,
		AgentClass:      ex.AgentClass,
		HumanRoot:       ex.HumanRoot,
		DelegationChain: govHops(ex.DelegationChain),
		Model:           ex.ResolvedModel,
		Region:          region,
		Decision:        decision,
		Reason:          reason,
		PolicyVersion:   version,
		Operation:       string(ex.Op),
		Timestamp:       ex.Now(),
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrModelSwapNotSealed, err)
	}
	if denied {
		return fmt.Errorf("%w: %s", allowlist.ErrModelNotAllowed, reason)
	}
	ex.Record(s.slot, "allow", reason)
	return nil
}

// lastRoutingReason devolve a razão da ÚLTIMA decisão do rasto — a que o refino
// acabou de registar (perfil/pesos/score, ou a razão da degradação). Liga o registo
// de governação à razão do router sem duplicar a sua formatação.
func lastRoutingReason(ex *pipeline.Exchange) string {
	if n := len(ex.Decisions); n > 0 {
		d := ex.Decisions[n-1]
		return d.Stage + "/" + d.Result + ": " + d.Reason
	}
	return "sem razao registada no rasto"
}

// govHops projecta a cadeia de delegação do Exchange para os hops do registo de
// governação (o mesmo eixo de atribuição do estágio de allowlist).
func govHops(hops []pipeline.DelegationHop) []allowlist.Hop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]allowlist.Hop, len(hops))
	for i, h := range hops {
		out[i] = allowlist.Hop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}

// Compile-time: o selador da troca satisfaz [pipeline.Stage].
var _ pipeline.Stage = (*modelSwapRecorder)(nil)

// orFactor devolve o factor injectado pelo deployment ou a impl de referência.
func orFactor(injected, fallback scoring.FactorProvider) scoring.FactorProvider {
	if injected != nil {
		return injected
	}
	return fallback
}

// keypoolLoad adapta o KEYPOOL (AOS-057) à porta [router.LoadProvider]: o headroom
// do par (provider, região) é o da conta que o pool escolheria — a MESMA aritmética
// inteira do pior eixo (RPM/TPM) que já governa a selecção. É LEITURA PURA: não
// consome throughput (ver a armadilha do duplo Select). Sem registo, tudo saturado
// (lado seguro: sem pool não há capacidade).
type keypoolLoad struct{ reg *keypool.Registry }

// Load implementa [router.LoadProvider].
func (k keypoolLoad) Load(_ context.Context, provider, region string) (router.Headroom, error) {
	if k.reg == nil {
		return router.Headroom{Saturated: true}, nil
	}
	used, limit, saturated := k.reg.Headroom(provider, region)
	return router.Headroom{WorstUsed: used, WorstLimit: limit, Saturated: saturated}, nil
}

// healthFactor deriva o factor de SAÚDE do scoring da MESMA função de saúde que o
// failover usa: uma região vale [scoring.Scale] se tiver pelo menos um endpoint
// saudável no inventário do provedor, e 0 se não tiver nenhum. Sem função de saúde
// (nil), tudo saudável — coerente com [adaptHealth].
//
// É um DESEMPATE, não uma guarda: a exclusão do endpoint doente já aconteceu duas
// vezes a montante (o failover não o escolhe; o classificador não o oferece como
// candidato). Aritmética inteira, sem I/O.
func healthFactor(h func(keyID, region string) bool, inv failover.Inventory) scoring.FactorProvider {
	return scoring.FactorFunc(func(_ context.Context, t scoring.Task, c scoring.Candidate) (int, error) {
		if h == nil || inv == nil {
			return scoring.Scale, nil
		}
		for _, e := range inv.Endpoints(t.Provider) {
			if e.Region == c.Region && h(e.KeyID, e.Region) {
				return scoring.Scale, nil
			}
		}
		return 0, nil
	})
}

// guardFromPolicy deriva a guarda de fronteira ESTÁTICA do router da MESMA allowlist
// regional que é fronteira do failover. Duas regiões partilham fronteira se — e só
// se — servirem EXACTAMENTE o mesmo conjunto de boards nas regras da policy: para a
// policy em vigor, {eu, eu-west} são a fronteira de board-eu e {us-east} a de
// board-us, que é o mesmo agrupamento que o failover deriva por chamada.
//
// PORQUE ESTÁTICA E PORQUE ISTO É SEGURO. O router constrói-se uma vez e a sua guarda
// não pode ser por-chamada; a fronteira EXACTA de cada chamada continua imposta pelas
// duas peças que a conhecem: o FAILOVER a montante (que já resolveu a região dentro
// da fronteira do board e selou o deny cross-border) e a ALLOWLIST como guarda do
// próprio router (`Allows(board, modelo, região)` corre ANTES do ranking, pelo que
// nenhum peso elege um par não permitido). Esta guarda é o pré-filtro grosseiro que
// decide que regiões são sequer COMPARÁVEIS — e o critério «mesmo conjunto de boards»
// é conservador: uma região partilhada por mais boards fica numa fronteira PRÓPRIA
// (cross-border, descartada), nunca fundida com as outras.
//
// Uma região que não apareça em regra alguma mantém a sua própria fronteira (o
// default de [sovereignty.Guard]) — nunca é intra-fronteira com outra.
func guardFromPolicy(pol *allowlist.Policy) *sovereignty.Guard {
	if pol == nil {
		return sovereignty.NewGuard()
	}
	boardsByRegion := map[string]map[string]struct{}{}
	for _, r := range pol.Rules {
		if r.Board == "" {
			continue // regra sem board não autoriza nada (fail-closed, como Evaluate)
		}
		for _, reg := range r.Regions {
			if reg == "" || reg == "*" {
				continue // wildcard não é fronteira de soberania (como AllowedRegions)
			}
			if boardsByRegion[reg] == nil {
				boardsByRegion[reg] = map[string]struct{}{}
			}
			boardsByRegion[reg][r.Board] = struct{}{}
		}
	}
	opts := make([]sovereignty.Option, 0, len(boardsByRegion))
	for reg, boards := range boardsByRegion {
		names := make([]string, 0, len(boards))
		for b := range boards {
			names = append(names, b)
		}
		sort.Strings(names) // rótulo canónico: independente da ordem de iteração
		opts = append(opts, sovereignty.WithBoundary(reg, sovereignty.Boundary("gw:boards="+strings.Join(names, ","))))
	}
	return sovereignty.NewGuard(opts...)
}
