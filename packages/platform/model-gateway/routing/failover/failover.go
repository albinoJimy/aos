// Package failover é o ROUTER DE FAILOVER do Model Gateway (o wiring de imposição
// diferido de AOS-058, base do AOS-059) — o estágio de roteamento que resolve a
// região de destino de uma model call EXCLUSIVAMENTE através da guarda de soberania
// ([sovereignty.Guard]). É a costura que torna o bloqueio cross-border efectivo no
// DATA-PLANE VIVO, não apenas um primitivo testado à parte.
//
// # No-bypass (o coração do ticket)
//
// O único caminho para resolver a região é [Guard.Route]/[Guard.Failover]: o estágio
// NÃO tem um ramo alternativo que escolha uma região sem passar pela guarda. Um
// candidato fora da fronteira de soberania é DESCARTADO pela guarda ANTES de qualquer
// escolha (prova estrutural em routing/sovereignty); uma tentativa de failover que só
// encontre capacidade cross-border produz um DENY fail-closed, atribuível a principal
// + board e selado no audit WORM (reutiliza o [allowlist.Recorder] de AOS-058). A
// disponibilidade NUNCA é comprada à custa da soberania.
//
// # Fronteira derivada da allowlist (coerência com AOS-058)
//
// A fronteira de soberania de cada chamada é DERIVADA da allowlist regional já em
// vigor: [allowlist.Policy.AllowedRegions](board, modelo) são as regiões legalmente
// permitidas a esse board — e são exactamente a fronteira que a guarda usa para
// separar intra de cross-border. Assim o failover só pode aterrar numa região que o
// board também tem allowlisted (soberania E allowlist coerentes, como
// TestIntegration_SovereigntyCoherentWithAllowlist prova). Uma região de infra fora
// dessa fronteira (ex.: "us-east" para um "board-eu") é cross-border e descartada.
//
// # Papel vs AOS-059
//
// Este estágio impõe a SOBERANIA (a região). A escolha da CHAVE dentro da região
// (throughput) é do keypool (AOS-057), ligado à parte no passo de credencial do
// gateway. AOS-059 (cost/load-aware) refina a escolha SEMPRE dentro dos
// sobreviventes intra-fronteira que esta guarda autoriza — nunca a expande.
//
// # Determinismo
//
// A saúde do primário é INJECTÁVEL ([sovereignty.HealthFunc]); sem rede nem rand na
// decisão. A guarda constrói-se por chamada a partir da allowlist (mapa pequeno).
package failover

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
)

// stageName é o nome canónico do estágio na pipeline (preserva o slot "roteamento"
// de AOS-055, para que o rasto de decisões e a variância continuem a indexá-lo igual).
const stageName = "roteamento"

// Erros fail-closed do router de failover.
var (
	// ErrRouterUnconfigured — o estágio foi construído sem policy ou sem inventário de
	// endpoints. Fail-closed: recusa toda a chamada em vez de rotear às cegas.
	ErrRouterUnconfigured = errors.New("failover: router de soberania nao configurado (fail-closed)")
	// ErrCrossBorderBlocked — a única capacidade disponível para o failover está FORA
	// da fronteira de soberania do board. Fail-closed: a chamada é recusada (nunca
	// encaminhada cross-border) e o deny é selado, atribuível a principal + board.
	ErrCrossBorderBlocked = errors.New("failover: failover cross-border bloqueado (soberania, fail-closed)")
	// ErrNoIntraCapacity — não há capacidade intra-fronteira (nem primário saudável
	// nem candidato de failover na fronteira). Fail-closed: backpressure graciosa a
	// montante, nunca sair da jurisdição.
	ErrNoIntraCapacity = errors.New("failover: sem capacidade intra-fronteira (fail-closed, backpressure)")
)

// Inventory é o INVENTÁRIO de endpoints de infra (KeyID + região) disponíveis por
// provider — a topologia real que o router consulta para o failover. NUNCA
// transporta segredos (ADR-006): o KeyID é o identificador não-secreto da conta (o
// mesmo eixo do keypool de AOS-057). O composition root constrói-o a partir da mesma
// fonte de verdade que o keypool, mantendo os dois coerentes.
type Inventory interface {
	// Endpoints devolve os endpoints de infra do provider (em qualquer região).
	Endpoints(provider string) []sovereignty.Endpoint
}

// StaticInventory é uma [Inventory] em memória (provider → endpoints). Determinista.
type StaticInventory map[string][]sovereignty.Endpoint

// Endpoints implementa [Inventory].
func (m StaticInventory) Endpoints(provider string) []sovereignty.Endpoint { return m[provider] }

// Stage é o estágio de roteamento REAL: resolve a região só via a guarda de
// soberania. Implementa [pipeline.Stage] e substitui o IdentityRouting pass-through.
// Construir com [NewStage].
type Stage struct {
	policy    *allowlist.Policy
	inventory Inventory
	health    sovereignty.HealthFunc
	recorder  *allowlist.Recorder
}

// StageOption configura o [Stage].
type StageOption func(*Stage)

// WithHealth injecta a função de saúde do endpoint primário ([sovereignty.HealthFunc]).
// Sem ela, o primário é tratado como indisponível (força a avaliação do failover, que
// permanece intra-fronteira). A saúde é uma optimização de liveness — a fronteira de
// soberania é imposta INDEPENDENTEMENTE da saúde (o controlo de segurança é o descarte
// cross-border, não a saúde).
func WithHealth(h sovereignty.HealthFunc) StageOption { return func(s *Stage) { s.health = h } }

// WithRecorder liga o [allowlist.Recorder] de governação: um deny de failover
// cross-border é selado no audit WORM, atribuível a principal + board (nunca anónimo,
// ADR-010). Sem recorder, o deny cross-border ainda FALHA-FECHA, mas só regista no
// rasto de decisões (produção liga sempre um recorder).
func WithRecorder(r *allowlist.Recorder) StageOption { return func(s *Stage) { s.recorder = r } }

// NewStage constrói o router de failover sobre a allowlist em vigor (a fronteira de
// soberania) e o inventário de endpoints de infra. Policy ou inventário nil tornam o
// estágio fail-closed (recusa toda a chamada) — nunca fail-open.
func NewStage(policy *allowlist.Policy, inventory Inventory, opts ...StageOption) *Stage {
	s := &Stage{policy: policy, inventory: inventory}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implementa [pipeline.Stage]: mantém o nome canónico do slot ("roteamento").
func (s *Stage) Name() string { return stageName }

// Process implementa [pipeline.Stage]. Resolve a região de destino SÓ via a guarda de
// soberania construída a partir da allowlist do board. Fail-closed:
//
//   - primário saudável e intra-fronteira → OutcomePrimary (usa a região pedida);
//   - senão → failover INTRA-fronteira (a guarda descarta os cross-border);
//   - só há capacidade cross-border → DENY atribuível selado no WORM + erro fail-closed;
//   - nenhuma capacidade intra-fronteira → backpressure fail-closed.
//
// O modelo/provider resolvidos são os pedidos (este estágio impõe a SOBERANIA da
// região; AOS-059 pode refinar modelo/tier, sempre dentro da fronteira autorizada).
func (s *Stage) Process(ctx context.Context, ex *pipeline.Exchange) error {
	if s.policy == nil || s.inventory == nil {
		return ErrRouterUnconfigured
	}
	ex.ResolvedModel = ex.RequestedModel
	provider := ex.ResolvedProvider
	if provider == "" {
		provider = ex.RequestedProvider
		ex.ResolvedProvider = provider
	}

	// A fronteira de soberania da chamada é derivada da allowlist já em vigor: as
	// regiões legalmente permitidas ao (board, modelo) partilham a fronteira do board;
	// qualquer outra região (ex.: capacidade de infra noutra jurisdição) fica FORA da
	// fronteira e é cross-border. É a MESMA semântica de fronteira que a allowlist usa
	// (AllowedRegions ignora wildcards/vazios), pelo que soberania e allowlist coincidem.
	home := boardBoundary(ex.Board)
	legal := s.policy.AllowedRegions(ex.Board, ex.RequestedModel)
	opts := make([]sovereignty.Option, 0, len(legal))
	for _, r := range legal {
		opts = append(opts, sovereignty.WithBoundary(r, home))
	}
	guard := sovereignty.NewGuard(opts...)

	reqRegion := ex.RequestedRegion
	if reqRegion == "" {
		reqRegion = ex.ResolvedRegion
	}
	candidates := s.inventory.Endpoints(provider)
	primary := pickPrimary(candidates, reqRegion)

	dec := guard.Route(reqRegion, primary, candidates, s.health)
	switch dec.Outcome {
	case sovereignty.OutcomePrimary, sovereignty.OutcomeFailover:
		ex.ResolvedRegion = dec.Chosen.Region
		ex.Record(stageName, string(dec.Outcome), fmt.Sprintf("soberania intra-fronteira %q; regiao=%q", dec.Home, dec.Chosen.Region))
		return nil
	default: // OutcomeReject
		if dec.CrossBorderBlocked() {
			reason := fmt.Sprintf("failover cross-border bloqueado (soberania): fronteira %q, pedida %q", dec.Home, reqRegion)
			ex.Record(stageName, "deny", reason)
			// Sela o deny cross-border, atribuível a principal + board. Mesmo que a
			// selagem falhe, NEGAMOS (fail-closed duplo) — a causa acompanha o erro.
			if err := s.sealCrossBorderDeny(ctx, ex, reqRegion, reason); err != nil {
				return fmt.Errorf("%w: %w", ErrCrossBorderBlocked, err)
			}
			return fmt.Errorf("%w: %s", ErrCrossBorderBlocked, reason)
		}
		ex.Record(stageName, "reject", "sem capacidade intra-fronteira")
		return ErrNoIntraCapacity
	}
}

// boardBoundary é a fronteira de soberania de um board. Namespaced ("board:<id>") para
// nunca colidir com um nome de região literal do inventário (uma região não-permitida
// que por acaso se chamasse como o board seria erradamente intra-fronteira).
func boardBoundary(board string) sovereignty.Boundary { return sovereignty.Boundary("board:" + board) }

// pickPrimary escolhe o endpoint primário: o primeiro candidato na região PEDIDA. Se
// nenhum candidato serve a região pedida, devolve o endpoint zero — a guarda trata-o
// como primário indisponível e avalia o failover (que permanece intra-fronteira).
func pickPrimary(candidates []sovereignty.Endpoint, reqRegion string) sovereignty.Endpoint {
	if reqRegion == "" {
		return sovereignty.Endpoint{}
	}
	for _, c := range candidates {
		if c.Region == reqRegion && c.KeyID != "" {
			return c
		}
	}
	return sovereignty.Endpoint{}
}

// sealCrossBorderDeny sela o deny de failover cross-border no audit WORM, atribuível a
// principal + board (reutiliza o eixo de governação de AOS-058). Sem recorder é no-op
// (o veredicto continua deny fail-closed). A selagem exige board + principal presentes
// (o estágio de authn deve tê-los resolvido a montante), impostos fail-closed pelo Seal.
func (s *Stage) sealCrossBorderDeny(ctx context.Context, ex *pipeline.Exchange, region, reason string) error {
	if s.recorder == nil {
		return nil
	}
	version := "n/a"
	if s.policy != nil {
		version = s.policy.Version()
	}
	rec := allowlist.GovRecord{
		Board:           ex.Board,
		PrincipalUser:   ex.PrincipalUser,
		PrincipalAgent:  ex.PrincipalAgent,
		AgentClass:      ex.AgentClass,
		HumanRoot:       ex.HumanRoot,
		DelegationChain: toGovHops(ex.DelegationChain),
		Model:           ex.RequestedModel,
		Region:          region,
		Decision:        audit.DecisionDeny,
		Reason:          reason,
		PolicyVersion:   version,
		Operation:       string(ex.Op),
		Timestamp:       ex.Now(),
	}
	_, err := s.recorder.Seal(ctx, rec)
	return err
}

// toGovHops projecta a cadeia de delegação do Exchange para os hops do registo de
// governação da allowlist.
func toGovHops(hops []pipeline.DelegationHop) []allowlist.Hop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]allowlist.Hop, len(hops))
	for i, h := range hops {
		out[i] = allowlist.Hop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}
