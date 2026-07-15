// Package tieradapter é o ADAPTADOR FINO que liga o router cost/load-aware de
// PRODUÇÃO do Model Gateway (AOS-059) às portas do Escalonador (EPIC-03), sem que
// o núcleo do GW dependa do control-plane e sem o Escalonador reimplementar a
// degradação (AOS-031). É o análogo do ModelClientAdapter de AOS-055: reconcilia
// dois contratos por um adaptador de fronteira, isolando aqui — e SÓ aqui — o
// import de control-plane/scheduler.
//
// # Decisão de layering (documentada, tecnica/06 §6)
//
// O núcleo do roteamento (routing/tiering, routing/router, routing/degradation) é
// ZERO-dep de control-plane: define os seus tipos nativos e é testável sem o
// Escalonador. Para SATISFAZER a porta scheduler.ModelTierRouter (exigência de
// AOS-059: o router de produção substitui o StaticModelTierRouter de referência
// por trás da MESMA porta), ALGUM ponto tem de nomear os tipos da porta do
// Escalonador — Go satisfaz interfaces por identidade de tipo, não estruturalmente.
// Esse ponto é ESTE pacote-folha: o import platform→control-plane fica confinado a
// um adaptador, preservando o núcleo layering-limpo (platform não depende de
// control-plane no caminho crítico). Foi a opção (b) do handoff — "o router do GW
// define o seu tiering e um ADAPTER fino satisfaz a porta".
//
// # O que o adaptador faz
//
//   - [TierRouter] satisfaz scheduler.ModelTierRouter: Cheaper desce UM degrau na
//     escada de custo do GW, SEMPRE dentro da allowlist regional (AOS-058) — a
//     escolha de tier cost/soberania-aware que substitui a referência estática. O
//     Escalonador continua dono da CADEIA (shed/defer/downgrade/reject) e sela a
//     variância model_downgraded; o GW só dá a ESCOLHA.
//   - [AdmissionAdapter] satisfaz router.AdmissionCoordinator envolvendo
//     *scheduler.Admission (ADR-008): o router COORDENA com o admission global real
//     (consome o headroom por porta, não reimplementa o token-bucket).
package tieradapter

import (
	"context"

	scheduler "github.com/aos-ref/control-plane/scheduler"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// Allowlist é a porta de allowlist regional que o adaptador compõe para NUNCA
// degradar para um modelo fora da fronteira de soberania (AOS-058). É a mesma
// forma que router.Allowlist.
type Allowlist interface {
	Allows(board, model, region string) bool
}

// allowNone é o fallback fail-CLOSED (default-deny) quando nenhuma allowlist é
// injectada: nada é elegível, logo Cheaper NUNCA degrada sem uma allowlist real
// ligada. Alinha com o default-deny de router e de routingstage.AllowlistFrom(nil)
// — a soberania da degradação não depende de LEMBRAR de injectar a allowlist.
type allowNone struct{}

func (allowNone) Allows(_, _, _ string) bool { return false }

// BoardFunc mapeia o Tenant de um scheduler.TierRouteRequest para o BOARD de
// soberania usado na allowlist do GW. A porta do Escalonador não carrega o board
// (a sua unidade é o tenant); o GW resolve-o por esta função. Default: identidade
// (o tenant É o board).
type BoardFunc func(tenant string) string

// TierRouter é a impl de PRODUÇÃO de scheduler.ModelTierRouter (AOS-059): a escada
// de tiers do GW (routing/tiering) por trás da MESMA porta que o StaticModelTierRouter
// de referência (AOS-031). Cheaper desce um degrau de custo dentro da allowlist
// regional — determinística, sem I/O.
type TierRouter struct {
	ladder    *tiering.Ladder
	allowlist Allowlist
	boardOf   BoardFunc
}

// Verificação estática: o adaptador satisfaz a porta do Escalonador.
var _ scheduler.ModelTierRouter = (*TierRouter)(nil)

// TierOption configura o [TierRouter].
type TierOption func(*TierRouter)

// WithTierAllowlist injecta a allowlist regional (AOS-058). Sem ela, o adaptador é
// fail-CLOSED (default-deny): o Cheaper NUNCA degrada (nada passa o filtro) — a
// soberania não depende de lembrar de a ligar. Produção liga sempre a allowlist
// real, e o Cheaper nunca degrada para fora da fronteira.
func WithTierAllowlist(a Allowlist) TierOption {
	return func(t *TierRouter) {
		if a != nil {
			t.allowlist = a
		}
	}
}

// WithBoardFunc injecta o mapeamento tenant→board para a allowlist.
func WithBoardFunc(fn BoardFunc) TierOption {
	return func(t *TierRouter) {
		if fn != nil {
			t.boardOf = fn
		}
	}
}

// NewTierRouter constrói o adaptador sobre a escada de tiers do GW.
func NewTierRouter(ladder *tiering.Ladder, opts ...TierOption) *TierRouter {
	t := &TierRouter{
		ladder:    ladder,
		allowlist: allowNone{},
		boardOf:   func(tenant string) string { return tenant },
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Cheaper implementa scheduler.ModelTierRouter: desce UM degrau na escada de custo
// a partir do tier corrente (ou do modelo corrente, se o tier não for dado),
// SEMPRE dentro da allowlist regional do board (a região vem de Key.Region). Se o
// corrente já é o mais barato elegível — ou não há degrau dentro da fronteira —
// devolve Downgraded=false (nunca um upgrade acidental nem um swap cross-border).
func (t *TierRouter) Cheaper(_ context.Context, req scheduler.TierRouteRequest) (scheduler.TierRouteDecision, error) {
	board := t.boardOf(req.Tenant)
	region := req.Key.Region
	filter := func(tier tiering.Tier) bool {
		return t.allowlist.Allows(board, tier.Model, region)
	}

	var cheaper tiering.Tier
	var ok bool
	if req.CurrentTier != "" {
		cheaper, ok = t.ladder.Cheaper(req.CurrentTier, filter)
	} else {
		cheaper, ok = t.ladder.CheaperByModel(req.CurrentModel, filter)
	}
	if !ok {
		return scheduler.TierRouteDecision{
			Downgraded: false,
			FromTier:   req.CurrentTier,
			FromModel:  req.CurrentModel,
		}, nil
	}
	return scheduler.TierRouteDecision{
		Downgraded: true,
		FromTier:   req.CurrentTier,
		ToTier:     cheaper.Name,
		FromModel:  req.CurrentModel,
		ToModel:    cheaper.Model,
	}, nil
}

// AdmissionAdapter satisfaz router.AdmissionCoordinator envolvendo o admission
// control GLOBAL real do Escalonador (*scheduler.Admission, AOS-027/ADR-008). O
// router de produção COORDENA com o token-bucket distribuído por esta porta — NÃO
// o reimplementa: cada Route reserva débito a montante (Admit) e nunca despacha
// sem Granted.
type AdmissionAdapter struct {
	adm *scheduler.Admission
}

// Verificação estática: o adaptador satisfaz a porta de coordenação do router.
var _ router.AdmissionCoordinator = (*AdmissionAdapter)(nil)

// NewAdmissionAdapter constrói o adaptador sobre um *scheduler.Admission já wired
// (Event Store + QuotaProvider). nil ⇒ nil (o router trata a ausência de porta).
func NewAdmissionAdapter(adm *scheduler.Admission) *AdmissionAdapter {
	if adm == nil {
		return nil
	}
	return &AdmissionAdapter{adm: adm}
}

// Reserve implementa router.AdmissionCoordinator traduzindo para scheduler.Admit:
// reserva atómica de débito no bucket global (provider:model:region). Mapeia o
// veredicto do Escalonador para o do router (Granted/Rejected/defer com retry).
func (a *AdmissionAdapter) Reserve(ctx context.Context, req router.AdmissionRequest) (router.AdmissionOutcome, error) {
	res, err := a.adm.Admit(ctx, scheduler.AdmitRequest{
		Key: scheduler.ProviderKey{
			Provider: req.Provider,
			Model:    req.Model,
			Region:   req.Region,
		},
		Tenant:          req.Tenant,
		Board:           req.Board,
		EstimatedTokens: req.EstimatedTokens,
	})
	if err != nil {
		return router.AdmissionOutcome{}, err
	}
	return router.AdmissionOutcome{
		Granted:          res.Granted,
		Rejected:         res.Rejected,
		ReservationID:    res.ReservationID,
		RetryAfter:       res.RetryAfter,
		HeadroomTokens:   res.HeadroomTokens,
		HeadroomRequests: res.HeadroomRequests,
	}, nil
}
