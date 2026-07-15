// Package routingstage liga o router cost/load-aware de produção (routing/router)
// ao ESTÁGIO de roteamento da pipeline do Model Gateway (AOS-059, tecnica/06 §6):
// substitui o pass-through [pipeline.IdentityRouting] de AOS-055 pela escolha REAL
// de modelo/tier/região/conta dentro da fronteira de soberania.
//
// É um adaptador de fronteira (à imagem do estágio allowlist de AOS-058): traduz o
// [pipeline.Exchange] para um router.Request, corre o router, e reflecte a decisão
// no Exchange (ResolvedModel/Region/Provider/KeyID) — ou FALHA-FECHA a chamada num
// defer/reject (o provider não é invocado sem uma rota admitida). Mantém o núcleo
// do router PURO (sem dependência da pipeline).
package routingstage

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// stageName preserva o slot canónico do estágio de roteamento na pipeline.
const stageName = "roteamento"

// ErrRouteDeferred — o admission GLOBAL não concedeu headroom (ou o pool saturou):
// a chamada é ADIADA (defer, coordenação ADR-008), NUNCA despachada sem débito
// reservado. Fail-closed comparável por errors.Is; o chamador reagenda com o
// retry_after do Escalonador/backpressure.
var ErrRouteDeferred = errors.New("routingstage: rota adiada — sem headroom global de admissao (defer, ADR-008)")

// ErrRouteRejected — não há rota admissível dentro da fronteira de soberania: sem
// endpoint intra-fronteira, sem tier dentro da allowlist que satisfaça a
// capacidade, ou rejeição permanente do admission. Fail-closed (o provider não é
// invocado).
var ErrRouteRejected = errors.New("routingstage: rota rejeitada — sem destino admissivel dentro da fronteira")

// Task é a classificação de uma chamada para efeitos de roteamento: a capacidade
// exigida, a classe latência/prioridade, o custo estimado e os endpoints
// candidatos (para a selecção menos-carregado e a filtragem de soberania). É
// derivada do [pipeline.Exchange] por um [Classifier].
type Task struct {
	Capability      tiering.Capability
	Class           tiering.Class
	EstimatedTokens int64
	Candidates      []sovereignty.Endpoint
}

// Classifier deriva a [Task] de um [pipeline.Exchange]. Injectável para que a
// política de classificação (que tarefas exigem raciocínio vs extracção,
// interactivo vs batch) seja configurável sem alterar o estágio. Default:
// [DefaultClassifier].
type Classifier func(ex *pipeline.Exchange) Task

// DefaultClassifier é a classificação por omissão: capacidade STANDARD, classe
// INTERACTIVA, sem candidatos explícitos (usa a região pedida) e custo estimado
// mínimo. Produção injecta um classificador que lê os hints da tarefa.
func DefaultClassifier(_ *pipeline.Exchange) Task {
	return Task{Capability: tiering.CapabilityStandard, Class: tiering.ClassInteractive}
}

// Stage é o estágio de roteamento REAL (AOS-059). Implementa [pipeline.Stage].
type Stage struct {
	r        *router.Router
	classify Classifier
}

// Option configura o [Stage].
type Option func(*Stage)

// WithClassifier injecta a classificação de tarefa (capacidade/classe/candidatos).
func WithClassifier(c Classifier) Option {
	return func(s *Stage) {
		if c != nil {
			s.classify = c
		}
	}
}

// NewStage constrói o estágio de roteamento sobre o router de produção.
func NewStage(r *router.Router, opts ...Option) *Stage {
	s := &Stage{r: r, classify: DefaultClassifier}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implementa [pipeline.Stage]: mantém o nome canónico do slot ("roteamento").
func (s *Stage) Name() string { return stageName }

// Process implementa [pipeline.Stage]. Corre o router e reflecte a decisão no
// Exchange:
//
//   - Routed/Degraded ⇒ preenche ResolvedModel/Region/Provider e KeyID (a conta
//     pooled menos-carregada), regista a razão no rasto (modelo/tier/razão) — um
//     degrade fica observável como variância explícita a jusante (recordVariance);
//   - Deferred ⇒ [ErrRouteDeferred] fail-closed (coordenação com o admission);
//   - Rejected ⇒ [ErrRouteRejected] fail-closed (fora da fronteira/sem tier).
func (s *Stage) Process(ctx context.Context, ex *pipeline.Exchange) error {
	if s.r == nil {
		return fmt.Errorf("routingstage: router nao configurado (fail-closed)")
	}
	task := s.classify(ex)
	dec, err := s.r.Route(ctx, router.Request{
		Board:           ex.Board,
		Tenant:          ex.Board, // o board é a unidade de soberania/quota do GW
		Provider:        ex.RequestedProvider,
		Region:          ex.RequestedRegion,
		Capability:      task.Capability,
		Class:           task.Class,
		EstimatedTokens: task.EstimatedTokens,
		Candidates:      task.Candidates,
	})
	if err != nil {
		return err
	}

	switch dec.Outcome {
	case router.OutcomeDeferred:
		ex.Record(stageName, "defer", dec.Reason)
		return fmt.Errorf("%w: retry_after=%s", ErrRouteDeferred, dec.RetryAfter)
	case router.OutcomeRejected:
		ex.Record(stageName, "reject", dec.Reason)
		return fmt.Errorf("%w: %s", ErrRouteRejected, dec.Reason)
	}

	ex.ResolvedModel = dec.Model
	ex.ResolvedRegion = dec.Region
	if dec.Provider != "" {
		ex.ResolvedProvider = dec.Provider
	}
	if dec.KeyID != "" {
		ex.KeyID = dec.KeyID
	}
	result := "route"
	if dec.Outcome == router.OutcomeDegraded {
		result = "degrade"
	}
	ex.Record(stageName, result, dec.Reason)
	return nil
}

// AllowlistFrom adapta a *allowlist.Policy (AOS-058) à porta router.Allowlist: um
// (board, modelo, região) é permitido se a policy o avaliar como allow
// (default-deny). É a composição estrutural que garante que o router NUNCA escolhe
// nem degrada para fora da fronteira de soberania.
func AllowlistFrom(p *allowlist.Policy) router.Allowlist {
	return policyAllowlist{p: p}
}

type policyAllowlist struct{ p *allowlist.Policy }

// Allows implementa router.Allowlist. Uma policy nil é fail-closed (nada é
// permitido) — nunca fail-open por ausência de política.
func (a policyAllowlist) Allows(board, model, region string) bool {
	if a.p == nil {
		return false
	}
	return a.p.Evaluate(allowlist.Input{Board: board, Model: model, Region: region}) == allowlist.EffectAllow
}
