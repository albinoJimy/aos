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
//
// # ENTRADA «RESOLVIDA-PRIMEIRO» (AOS-280) — a forma escolhida, declarada
//
// Este estágio pode correr SOZINHO no slot de roteamento (é como AOS-059 o compôs)
// ou ENCADEADO a jusante da guarda de soberania/failover (AOS-058), que é a
// composição de produção decidida em AOS-280: `failover` → `routingstage`. Nesse
// encadeamento o estágio anterior JÁ resolveu a região (impondo a fronteira legal e,
// se o primário estava doente, fazendo failover intra-fronteira deliberado).
//
// A entrada do refino é, por isso, a saída do estágio anterior QUANDO ELA EXISTE:
//
//	região   = ex.ResolvedRegion   se != ""   senão ex.RequestedRegion
//	provider = ex.ResolvedProvider se != ""   senão ex.RequestedProvider
//
// A forma escolhida foi o FALLBACK (ler o resolvido, cair no pedido) e NÃO uma
// Option explícita, por duas razões: (i) não há composição correcta em que o refino
// deva ignorar uma resolução já tomada — uma Option seria um interruptor para uma
// configuração errada; (ii) o fallback preserva byte-a-byte o uso standalone (sem
// estágio anterior, ResolvedRegion está vazio à entrada da pipeline — o Gateway só
// o preenche DEPOIS do roteamento), pelo que nada em AOS-059/AOS-063 muda.
//
// Ler sempre a região PEDIDA seria o encadeamento ingénuo e está PARTIDO: descartaria
// em silêncio a decisão do failover — incluindo um failover por SAÚDE — e o refino
// partiria outra vez da região que a guarda acabou de recusar. A regra acima é o que
// torna «a decisão de soberania sobrevive ao refino» uma propriedade do código e não
// uma convenção de quem compõe.
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
	// Profile é o PERFIL DE PESOS que esta chamada pede ao scoring ponderado
	// (ADR-021 §1 gap 2: a intenção declarada — «o mais barato possível» vs
	// «qualidade acima de tudo»). É AQUI que a intenção entra, porque é o
	// [Classifier] que conhece a tarefa; o router limita-se a resolvê-lo contra a
	// tabela ASSINADA e a recusar fail-closed um perfil desconhecido. Vazio ⇒ o
	// perfil composto no scorer. Sem efeito no modo lexicográfico.
	Profile string
	// NoRefine, quando NÃO-VAZIO, declara que o classificador não conseguiu
	// CARACTERIZAR esta chamada (ex.: o modelo pedido está fora da escada de tiers
	// declarada pelo deployment) e traz a RAZÃO. O estágio então PRESERVA a resolução
	// do estágio anterior — a fronteira de soberania que o failover já impôs — e
	// regista a razão no rasto, em vez de correr o router com uma capacidade
	// INVENTADA. É deliberadamente uma string e não um bool: saltar o refino sem
	// dizer porquê seria exactamente a degradação silenciosa que este estágio existe
	// para não ter.
	//
	// PORQUE NÃO É FAIL-CLOSED. O que falta é a caracterização (custo/capacidade) de
	// um modelo que a allowlist regional JÁ permitiu e que o failover JÁ resolveu
	// dentro da fronteira: nenhuma guarda foi contornada, só não há por onde refinar.
	// Recusar converteria uma escada mal declarada numa interrupção total do caminho
	// quente; o refino é uma optimização de custo/carga, não um controlo de segurança.
	NoRefine string
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
	if task.NoRefine != "" {
		// Sem caracterização não se refina — mas também não se perde a resolução já
		// tomada a montante. Se este estágio correr SOZINHO (sem failover à frente), a
		// resolução ainda não existe: espelha-se o pedido, que é exactamente o que o
		// pass-through de AOS-055 fazia. Nunca se deixa o Exchange sem rota.
		if ex.ResolvedModel == "" {
			ex.ResolvedModel = ex.RequestedModel
		}
		if ex.ResolvedRegion == "" {
			ex.ResolvedRegion = ex.RequestedRegion
		}
		if ex.ResolvedProvider == "" {
			ex.ResolvedProvider = ex.RequestedProvider
		}
		ex.Record(stageName, "no-refine", task.NoRefine)
		return nil
	}
	dec, err := s.r.Route(ctx, router.Request{
		Board:           ex.Board,
		Tenant:          ex.Board, // o board é a unidade de soberania/quota do GW
		Provider:        inputProvider(ex),
		Region:          inputRegion(ex),
		Capability:      task.Capability,
		Class:           task.Class,
		Profile:         task.Profile,
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

// inputRegion é a regra «RESOLVIDA-PRIMEIRO» do encadeamento (AOS-280, ver o doc do
// pacote): a região de entrada do refino é a que o estágio anterior RESOLVEU e, só
// na sua ausência (estágio a correr sozinho), a região PEDIDA. É a única linha que
// impede o refino de descartar em silêncio um failover por saúde já decidido.
func inputRegion(ex *pipeline.Exchange) string {
	if ex.ResolvedRegion != "" {
		return ex.ResolvedRegion
	}
	return ex.RequestedRegion
}

// inputProvider aplica a MESMA regra ao provedor (o failover fixa-o antes de
// resolver a região): resolvido primeiro, pedido em fallback.
func inputProvider(ex *pipeline.Exchange) string {
	if ex.ResolvedProvider != "" {
		return ex.ResolvedProvider
	}
	return ex.RequestedProvider
}

// InputRegion expõe a regra «resolvida-primeiro» a quem CLASSIFICA: um classificador
// que derive candidatos (regiões de inventário) tem de partir da MESMA região de que
// o estágio parte, senão as duas metades da decisão divergem — o refino ancorado numa
// região e os candidatos noutra. Uma só definição, um só sítio.
func InputRegion(ex *pipeline.Exchange) string { return inputRegion(ex) }

// InputProvider expõe a regra «resolvida-primeiro» do provedor (ver [InputRegion]).
func InputProvider(ex *pipeline.Exchange) string { return inputProvider(ex) }

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
