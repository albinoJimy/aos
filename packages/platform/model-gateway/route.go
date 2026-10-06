package modelgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A ROTA SOB GOVERNAÇÃO (AOS-505, emenda ao ADR-036 §2.8).
//
// A allowlist assinada governa QUE NOME se pode pedir. O que esse nome significa — que modelo e
// que endpoint o servem — decide-se na configuração do proxy, fora de qualquer assinatura. Aqui o
// gateway passa a comparar, em cada turno, a rota que o proxy DECLARA ter servido com a que o
// perfil da rota espera.
//
// O QUE ISTO DETECTA: uma troca de CONFIGURAÇÃO no proxy — outro modelo por baixo do mesmo nome
// pedido, ou outro endpoint. Medido com a imagem de produção do proxy contra providers falsos.
//
// O QUE NÃO DETECTA: uma troca feita pelo PROVIDER por trás do mesmo nome e do mesmo endpoint. Os
// cabeçalhos dizem o que o proxy está configurado para pedir, não o que o provider serviu. E não
// são atestação: quem os emite é o proxy, e valem enquanto o canal entre o nó e o proxy for de
// confiança.

// Modos do interruptor da governação da rota. Vocabulário FECHADO.
const (
	// RouteGovernanceOff — a rota não é comparada. O gateway comporta-se como antes do AOS-505:
	// nenhum campo novo sai dele. É a omissão.
	RouteGovernanceOff = "off"
	// RouteGovernanceObserve — cada turno é comparado; uma variância é registada (evento, selo
	// e contador) e o turno segue.
	RouteGovernanceObserve = "observe"
	// RouteGovernanceEnforce — cada turno é comparado; um turno cuja rota não se prove igual à
	// do perfil FALHA, com causa em vocabulário fechado ([RouteVarianceError]).
	RouteGovernanceEnforce = "enforce"
)

// ErrBadRouteGovernance — o modo pedido não é do vocabulário fechado.
var ErrBadRouteGovernance = errors.New("model-gateway: modo de governacao da rota desconhecido (aceites: off, observe, enforce)")

// ParseRouteGovernance valida um modo. Sem normalização de caixa nem de espaços.
func ParseRouteGovernance(mode string) (string, error) {
	switch mode {
	case RouteGovernanceOff, RouteGovernanceObserve, RouteGovernanceEnforce:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrBadRouteGovernance, mode)
	}
}

// Causas de uma comparação que não deu `igual`. Vocabulário FECHADO: é a causa do erro em
// imposição, a razão do selo de governação e a do evento de variância.
const (
	// RouteCauseModelDifferent — o proxy declarou um modelo que não é o do perfil.
	RouteCauseModelDifferent = "modelo_diferente"
	// RouteCauseModelUnreported — o proxy não declarou o modelo servido.
	RouteCauseModelUnreported = "modelo_nao_reportado"
	// RouteCauseEndpointDifferent — o proxy declarou um endpoint cujo host não é o esperado.
	RouteCauseEndpointDifferent = "endpoint_diferente"
	// RouteCauseEndpointUnreported — há um host esperado e o proxy não declarou o endpoint.
	RouteCauseEndpointUnreported = "endpoint_nao_reportado"
	// RouteCauseNoProfile — o nome pedido ao proxy não tem perfil: não há esperado com que
	// comparar. Conta como `diferente`: uma rota sem perfil está fora do que é governado.
	RouteCauseNoProfile = "rota_sem_perfil"
)

// Rótulos do modelo servido para métricas, quando ele não é um dos nomes do conjunto fechado dos
// perfis ([ServedModelLabel]).
const (
	// ServedLabelOther — o proxy declarou um nome que nenhum perfil espera.
	ServedLabelOther = "outro"
	// ServedLabelUnreported — o proxy não declarou nome nenhum.
	ServedLabelUnreported = port.RouteCheckUnreported
)

// Classes de wire de um perfil.
const (
	// WireOpenAIChat — chat/completions na forma OpenAI.
	WireOpenAIChat = "openai-chat-completions"
)

// Capacidades que um perfil declara.
const (
	// CapabilityTools — a rota aceita o campo `tools` do pedido.
	CapabilityTools = "tools"
)

// RouteProfile é o PERFIL MÍNIMO de uma rota: o que o nó espera de um nome que pede ao proxy.
// Vive em código ([routeProfiles]); o perfil como artefacto assinado do registo é da fase A3.
// Não contém segredos nem endereços — o host esperado do endpoint é configuração do nó
// ([RouteGovernance.ExpectedAPIHost]) e não entra no perfil nem no seu digest.
type RouteProfile struct {
	// Requested é o nome que o nó pede ao proxy (o `model` do pedido).
	Requested string `json:"requested"`
	// ExpectedModel é o modelo que o proxy deve declarar ter servido ([port.HeaderServedModel]).
	ExpectedModel string `json:"expected_model"`
	// WireClass é a classe de wire da rota.
	WireClass string `json:"wire_class"`
	// Capabilities são as capacidades declaradas da rota, por ordem alfabética.
	Capabilities []string `json:"capabilities"`
}

// Digest é o digest do perfil: `sha256:` sobre o JSON canónico dos quatro campos, com as
// capacidades ordenadas. É o que fica no manifesto de cada turno comparado.
func (p RouteProfile) Digest() string {
	caps := append([]string(nil), p.Capabilities...)
	sort.Strings(caps)
	canon, err := json.Marshal(RouteProfile{Requested: p.Requested, ExpectedModel: p.ExpectedModel, WireClass: p.WireClass, Capabilities: caps})
	if err != nil {
		// Quatro campos de texto: o Marshal não falha. Um digest vazio seria lido como «sem
		// perfil», pelo que se devolve um valor que não casa com nenhum.
		return "sha256:invalido"
	}
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// routeProfiles é a tabela dos perfis, em código. Dois pares de entradas para os mesmos dois
// modelos: o nome pedido de HOJE (os aliases da allowlist assinada em vigor) e o nome REAL, para
// que a troca do nome pedido — um passo de produção, com política re-assinada — não precise de
// outro binário.
var routeProfiles = []RouteProfile{
	{Requested: "gpt-4o-mini", ExpectedModel: "openai/kimi-for-coding", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}},
	{Requested: "gpt-4o", ExpectedModel: "openai/k3", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}},
	{Requested: "kimi-for-coding", ExpectedModel: "openai/kimi-for-coding", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}},
	{Requested: "k3", ExpectedModel: "openai/k3", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}},
}

// RouteProfileFor devolve o perfil do nome pedido ao proxy. Comparação exacta.
func RouteProfileFor(requested string) (RouteProfile, bool) {
	for _, p := range routeProfiles {
		if p.Requested == requested {
			return p, true
		}
	}
	return RouteProfile{}, false
}

// RouteProfiles devolve uma cópia da tabela dos perfis, pela ordem em que está escrita.
func RouteProfiles() []RouteProfile {
	out := make([]RouteProfile, len(routeProfiles))
	copy(out, routeProfiles)
	return out
}

// ServedModelLabels devolve o conjunto FECHADO de valores que o rótulo do modelo servido pode
// ter numa métrica: os modelos esperados dos perfis (sem repetições, pela ordem da tabela),
// seguidos de [ServedLabelOther] e [ServedLabelUnreported].
func ServedModelLabels() []string {
	var out []string
	visto := map[string]bool{}
	for _, p := range routeProfiles {
		if !visto[p.ExpectedModel] {
			visto[p.ExpectedModel] = true
			out = append(out, p.ExpectedModel)
		}
	}
	return append(out, ServedLabelOther, ServedLabelUnreported)
}

// ServedModelLabel fecha o nome do modelo servido no conjunto de [ServedModelLabels]: um nome
// que nenhum perfil espera é `outro`, e a ausência é `nao_reportado`. O texto que o proxy
// declarou nunca chega a um rótulo.
func ServedModelLabel(served string) string {
	if served == "" {
		return ServedLabelUnreported
	}
	for _, p := range routeProfiles {
		if p.ExpectedModel == served {
			return served
		}
	}
	return ServedLabelOther
}

// CompareRoute compara a rota declarada pelo proxy com o perfil do nome pedido. `expectedHost`
// vazio ⇒ o endpoint não é comparado. Devolve o resultado ([port.RouteCheckEqual],
// [port.RouteCheckDifferent] ou [port.RouteCheckUnreported]) e a causa (vazia em `igual`).
//
// A ordem das regras é a da gravidade: uma rota sem perfil e um modelo diferente ganham a um
// modelo por reportar, e este ao endpoint.
func CompareRoute(profile RouteProfile, hasProfile bool, servedModel, servedHost, expectedHost string) (check, cause string) {
	switch {
	case !hasProfile:
		return port.RouteCheckDifferent, RouteCauseNoProfile
	case servedModel == "":
		return port.RouteCheckUnreported, RouteCauseModelUnreported
	case servedModel != profile.ExpectedModel:
		return port.RouteCheckDifferent, RouteCauseModelDifferent
	case expectedHost == "":
		return port.RouteCheckEqual, ""
	case servedHost == "":
		return port.RouteCheckUnreported, RouteCauseEndpointUnreported
	case servedHost != expectedHost:
		return port.RouteCheckDifferent, RouteCauseEndpointDifferent
	default:
		return port.RouteCheckEqual, ""
	}
}

// ErrRouteVariance — em imposição, a rota de um turno não se provou igual à do perfil.
var ErrRouteVariance = errors.New("model-gateway: a rota servida nao se provou igual a do perfil — turno recusado (governacao da rota em enforce)")

// RouteVarianceError é o erro de um turno recusado pela governação da rota em imposição. Casa
// com [ErrRouteVariance] por [errors.Is]. Só leva vocabulário fechado: o nome que o proxy
// declarou e o endpoint não entram na mensagem.
type RouteVarianceError struct {
	// Check é o resultado da comparação (`diferente` ou `nao_reportado`).
	Check string
	// Cause é a causa, uma das constantes RouteCause*.
	Cause string
}

// Error implementa error.
func (e *RouteVarianceError) Error() string {
	return fmt.Sprintf("%v: resultado=%s causa=%s", ErrRouteVariance, e.Check, e.Cause)
}

// Unwrap liga o erro a [ErrRouteVariance].
func (e *RouteVarianceError) Unwrap() error { return ErrRouteVariance }

// RouteObservation é o que o gateway reporta a quem conta as comparações (as métricas do nó).
// Todos os campos são de vocabulário fechado.
type RouteObservation struct {
	// Check é o resultado da comparação.
	Check string
	// Served é o rótulo do modelo servido ([ServedModelLabel]).
	Served string
	// Cause é a causa (vazia em `igual`).
	Cause string
}

// RouteGovernance configura a governação da rota de um [Gateway].
type RouteGovernance struct {
	// Mode é o interruptor: [RouteGovernanceOff] (ou vazio), [RouteGovernanceObserve] ou
	// [RouteGovernanceEnforce]. Um valor fora do vocabulário recusa a composição
	// ([NewProduction]) e, numa opção ([WithRouteGovernance]), conta como imposição: um modo
	// ilegível nunca desliga a comparação em silêncio.
	Mode string
	// ExpectedAPIHost é o host (com a porta, se a tiver) do endpoint que o proxy deve declarar.
	// Vazio ⇒ o endpoint não é comparado. É configuração do nó e não do perfil; só se compara,
	// e não é gravado em lado nenhum.
	ExpectedAPIHost string
	// Observer recebe o resultado de cada comparação. Opcional.
	Observer func(RouteObservation)
}

// routeSealer é o que a governação da rota precisa do recorder de governação.
type routeSealer interface {
	SealRouteVariance(ctx context.Context, rec allowlist.GovRecord, expectedModel, servedModel string) (audit.AuditRecord, error)
}

// routeGovernor é a governação da rota composta num gateway. nil ⇒ desligada.
type routeGovernor struct {
	enforce      bool
	expectedHost string
	observer     func(RouteObservation)
	sealer       routeSealer
}

// WithRouteGovernance liga a governação da rota (AOS-505). Com o modo desligado (ou vazio) a
// opção é inerte e o gateway fica como estava. O selo de governação de cada variância só existe
// num gateway composto por [NewProduction], que lhe liga o recorder do audit.
func WithRouteGovernance(cfg RouteGovernance) Option {
	return func(g *Gateway) { g.route = newRouteGovernor(cfg, nil) }
}

func newRouteGovernor(cfg RouteGovernance, sealer routeSealer) *routeGovernor {
	if cfg.Mode == "" || cfg.Mode == RouteGovernanceOff {
		return nil
	}
	return &routeGovernor{
		// Fail-closed: tudo o que não seja `observe` impõe.
		enforce:      cfg.Mode != RouteGovernanceObserve,
		expectedHost: port.NormalizeAPIHost(cfg.ExpectedAPIHost),
		observer:     cfg.Observer,
		sealer:       sealer,
	}
}

// Atributos de span da comparação da rota. Só vocabulário fechado.
const (
	attrRouteCheck  = "aos.route.check"
	attrRouteServed = "aos.route.served"
	attrRouteCause  = "aos.route.cause"
	// attrRouteSealFailed marca que o selo de uma variância não foi escrito (em observação o
	// turno segue; a falha fica visível aqui).
	attrRouteSealFailed = "aos.route.seal_failed"
)

// varianceKindServedRoute é o Kind do [VarianceEvent] de uma variância de rota.
const varianceKindServedRoute = "served_route"

// governRoute compara a rota que o proxy declarou para esta resposta com o perfil do nome que o
// gateway lhe pediu, e escreve o resultado em resp.Route. `host` é o host do endpoint declarado,
// já retirado da resposta por quem chama: não sai daqui.
//
// Desligada (g.route == nil), apaga o que o adaptador leu dos cabeçalhos e não faz mais nada: a
// resposta sai como saía antes de o campo existir.
//
// Em observação devolve sempre nil. Em imposição devolve [*RouteVarianceError] quando o resultado
// não é `igual` — a resposta do provider já foi paga e o seu custo já foi contado; o que se
// recusa é usá-la.
func (g *Gateway) governRoute(ctx context.Context, span agentruntime.Span, ex *pipeline.Exchange, resp *port.ChatResponse, host string) error {
	if g.route == nil {
		resp.Route = port.ServedRoute{}
		return nil
	}
	requested := ex.ResolvedModel
	if requested == "" {
		requested = ex.RequestedModel
	}
	profile, ok := RouteProfileFor(requested)
	served := port.SanitizeServedModel(resp.Route.Model)
	check, cause := CompareRoute(profile, ok, served, host, g.route.expectedHost)
	digest := ""
	if ok {
		digest = profile.Digest()
	}
	resp.Route = port.ServedRoute{Model: served, Check: check, ProfileDigest: digest}

	label := ServedModelLabel(served)
	span.SetAttribute(attrRouteCheck, check)
	span.SetAttribute(attrRouteServed, label)
	if g.route.observer != nil {
		g.route.observer(RouteObservation{Check: check, Served: label, Cause: cause})
	}
	if check == port.RouteCheckEqual {
		return nil
	}
	span.SetAttribute(attrRouteCause, cause)
	g.variance.Emit(ctx, VarianceEvent{
		Kind:           varianceKindServedRoute,
		RequestedModel: ex.RequestedModel,
		ResolvedModel:  ex.ResolvedModel,
		ExpectedModel:  profile.ExpectedModel,
		ServedModel:    served,
		Provider:       ex.ResolvedProvider,
		ResolvedRegion: ex.ResolvedRegion,
		Reason:         cause,
		Principal:      ex.Principal,
		Board:          ex.Board,
	})
	sealErr := g.sealRouteVariance(ctx, ex, requested, profile.ExpectedModel, served, cause, digest)
	if sealErr != nil {
		span.SetAttribute(attrRouteSealFailed, true)
	}
	if !g.route.enforce {
		return nil
	}
	rerr := &RouteVarianceError{Check: check, Cause: cause}
	if sealErr != nil {
		return fmt.Errorf("%w (e o selo da variancia falhou: %w)", rerr, sealErr)
	}
	return rerr
}

// sealRouteVariance sela a variância no audit de governação do gateway, atribuída ao principal,
// ao board, ao run e ao passo. Sem selador (um gateway montado à mão) é no-op.
func (g *Gateway) sealRouteVariance(ctx context.Context, ex *pipeline.Exchange, requested, expected, served, cause, digest string) error {
	if g.route.sealer == nil {
		return nil
	}
	decision := audit.DecisionAllow
	if g.route.enforce {
		decision = audit.DecisionDeny
	}
	version := "route-profile/none"
	if digest != "" {
		version = "route-profile/" + digest
	}
	hops := make([]allowlist.Hop, 0, len(ex.DelegationChain))
	for _, h := range ex.DelegationChain {
		hops = append(hops, allowlist.Hop{Sub: h.Sub, ActAs: h.ActAs})
	}
	_, err := g.route.sealer.SealRouteVariance(ctx, allowlist.GovRecord{
		Board:           ex.Board,
		PrincipalUser:   ex.PrincipalUser,
		PrincipalAgent:  ex.PrincipalAgent,
		AgentClass:      ex.AgentClass,
		HumanRoot:       ex.HumanRoot,
		DelegationChain: hops,
		Model:           requested,
		Region:          ex.ResolvedRegion,
		Decision:        decision,
		Reason:          cause,
		PolicyVersion:   version,
		Operation:       string(ex.Op),
		RunID:           ex.RunID,
		StepID:          ex.StepID,
		Timestamp:       ex.Now(),
	}, expected, served)
	return err
}
