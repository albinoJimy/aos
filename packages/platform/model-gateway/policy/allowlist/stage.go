package allowlist

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// ErrModelNotAllowed — o par (board, modelo, região) NÃO está na allowlist
// regional do board. Default-deny fail-closed: os estágios seguintes não correm e
// o provider não é invocado. Comparável por errors.Is; o pipeline envolve-o em
// StageError, tornando a recusa atribuível ao estágio "allowlist-regional".
var ErrModelNotAllowed = errors.New("allowlist: modelo/regiao fora da allowlist regional do board (default-deny, fail-closed)")

// ErrStageUnconfigured — o estágio foi construído sem policy. Fail-closed: recusa
// toda a chamada em vez de deixar passar (nunca fail-open).
var ErrStageUnconfigured = errors.New("allowlist: estagio allowlist-regional nao configurado (fail-closed)")

// stageName é o nome canónico do estágio na pipeline (preserva o slot de AOS-055).
const stageName = "allowlist-regional"

// Stage é o estágio allowlist-regional REAL (AOS-058). Implementa [pipeline.Stage]
// e substitui o pass-through de AOS-055: avalia a allowlist default-deny por
// (board, modelo, região) ANTES do roteamento (é o 1.º ramo do diagrama de decisão
// de tecnica/06 §5), regista a decisão por chamada (span + WORM) e FALHA-FECHA um
// modelo fora de soberania. Construir com [NewStage].
type Stage struct {
	policy   *Policy
	recorder *Recorder
}

// StageOption configura o [Stage].
type StageOption func(*Stage)

// WithRecorder liga o [Recorder] de governação: a decisão de allowlist (allow E
// deny) é registada por chamada, atribuível a principal + board, selada no WORM. Se
// a selagem falhar, a chamada FALHA-FECHA (uma decisão de soberania não-auditável é
// inaceitável, ADR-010). Sem recorder, o estágio só regista no rasto de decisões.
func WithRecorder(r *Recorder) StageOption { return func(s *Stage) { s.recorder = r } }

// NewStage constrói o estágio sobre a allowlist carregada/verificada. Policy nil
// torna o estágio fail-closed (recusa toda a chamada) — nunca fail-open.
func NewStage(p *Policy, opts ...StageOption) *Stage {
	s := &Stage{policy: p}
	for _, o := range opts {
		o(s)
	}
	return s
}

// LoadAndActivate é o PONTO DE ACTIVAÇÃO da allowlist regional embebida para um
// composition root (AOS-058, ADR-011). É o entry-point SEGURO por omissão — em
// contraste com o pass-through allow-by-default de AOS-055 ([pipeline.PassthroughAllowlist],
// que NÃO impõe soberania e serve apenas de esqueleto): um gateway de produção liga
// o estágio devolvido aqui via [modelgateway.WithAllowlistStage]. LoadAndActivate:
//
//  1. carrega+verifica a policy assinada e PINA o trust anchor ([LoadPolicy]);
//  2. SELA a activação da versão da policy no changelog WORM ([Recorder.SealChangelog])
//     ANTES de servir qualquer chamada — audit-before-effect (ADR-011): a versão em
//     vigor fica no rasto tamper-evident na activação, não só nas decisões por chamada;
//  3. devolve o estágio REAL fail-closed (default-deny), já com o recorder ligado.
//
// Fail-closed: se o carregamento OU a selagem do changelog falharem, NÃO devolve
// estágio — o gateway recusa arrancar sobre uma policy não-carregável ou uma activação
// não-auditada (nunca degrada para allow). Com recorder nil, o passo (2) é no-op
// (só a decisão por chamada seria não-selada); produção liga sempre um recorder.
func LoadAndActivate(ctx context.Context, rec *Recorder, at time.Time) (*Stage, *Policy, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return nil, nil, err
	}
	st, err := ActivateWith(ctx, rec, at, pol)
	if err != nil {
		return nil, nil, err
	}
	return st, pol, nil
}

// ActivateWith sela a activação de uma policy JÁ CARREGADA no changelog WORM e devolve o stage. É o
// núcleo partilhado por [LoadAndActivate] (policy EMBEBIDA, trust anchor pinado) e pelo carregamento
// EXTERNO ([LoadSignedPolicyFromDir], bundle montado + trust anchor out-of-band): a governança
// (selagem WORM + default-deny + tamper-evidence) é IDÊNTICA; só a proveniência da policy muda.
// Fail-closed: policy nil ou selagem falhada ⇒ erro (nenhum gateway sem activação selada).
func ActivateWith(ctx context.Context, rec *Recorder, at time.Time, pol *Policy) (*Stage, error) {
	if pol == nil {
		return nil, errors.New("allowlist: policy nil na activacao (fail-closed)")
	}
	if rec != nil {
		if _, err := rec.SealChangelog(ctx, pol.Version(), at); err != nil {
			return nil, fmt.Errorf("allowlist: activacao da policy nao selada no changelog (fail-closed): %w", err)
		}
	}
	return NewStage(pol, WithRecorder(rec)), nil
}

// Name implementa [pipeline.Stage]: mantém o nome canónico do slot ("allowlist-regional").
func (s *Stage) Name() string { return stageName }

// Process implementa [pipeline.Stage]. Avalia a allowlist default-deny sobre
// (board, modelo, região) PEDIDOS (o estágio corre ANTES do roteamento; a região
// pedida é a fronteira que o board declarou). Um modelo fora da allowlist é DENY
// fail-closed com um registo atribuível a principal + board selado no WORM. Um
// allow regista a rota (modelo, região, resultado) por chamada.
func (s *Stage) Process(ctx context.Context, ex *pipeline.Exchange) error {
	if s.policy == nil {
		return ErrStageUnconfigured
	}
	// A região da decisão é a PEDIDA (ainda não houve roteamento). Se o roteamento a
	// resolver, fá-lo-á dentro da fronteira já validada aqui.
	region := ex.RequestedRegion
	if region == "" {
		region = ex.ResolvedRegion
	}
	in := Input{Board: ex.Board, Model: ex.RequestedModel, Region: region}
	effect := s.policy.Evaluate(in)

	version := s.policy.Version()
	if effect != EffectAllow {
		reason := fmt.Sprintf("modelo %q fora da allowlist regional do board %q na regiao %q", in.Model, in.Board, in.Region)
		ex.Record(stageName, "deny", reason+"; "+version)
		if err := s.record(ctx, ex, in, audit.DecisionDeny, reason, version); err != nil {
			// A selagem do deny falhou: mesmo assim NEGAMOS (fail-closed duplo). A causa
			// da selagem acompanha o erro para diagnóstico, mas o veredicto é sempre deny.
			return fmt.Errorf("%w: %w", ErrModelNotAllowed, err)
		}
		return fmt.Errorf("%w: %s", ErrModelNotAllowed, reason)
	}

	reason := "modelo na allowlist regional do board"
	ex.Record(stageName, "allow", reason+"; "+version+"; regiao="+in.Region)
	// Regista a decisão de allow (rota: modelo, região, resultado) por chamada. Se a
	// selagem falhar, FALHA-FECHA: uma decisão de soberania não-auditável aborta a
	// chamada ANTES de qualquer efeito (audit-before-effect, ADR-010).
	if err := s.record(ctx, ex, in, audit.DecisionAllow, reason, version); err != nil {
		return &pipeline.StageError{Stage: stageName, Err: err}
	}
	return nil
}

// record sela a decisão de governação (se houver recorder). O span é anotado pelo
// gateway a partir do rasto (os estágios não recebem o span); aqui o eixo durável é
// o WORM. Fail-closed é responsabilidade do chamador (Process).
func (s *Stage) record(ctx context.Context, ex *pipeline.Exchange, in Input, decision audit.Decision, reason, version string) error {
	if s.recorder == nil {
		return nil
	}
	rec := GovRecord{
		Board:           ex.Board,
		PrincipalUser:   ex.PrincipalUser,
		PrincipalAgent:  ex.PrincipalAgent,
		AgentClass:      ex.AgentClass,
		HumanRoot:       ex.HumanRoot,
		DelegationChain: toStageHops(ex.DelegationChain),
		Model:           in.Model,
		Region:          in.Region,
		Decision:        decision,
		Reason:          reason,
		PolicyVersion:   version,
		Operation:       string(ex.Op),
		Timestamp:       ex.Now(),
	}
	// O span é anotado pelo gateway (ver Gateway.annotateAllowlist); aqui só selamos
	// no WORM via Seal (não Record), evitando exigir um span que o estágio não tem.
	_, err := s.recorder.Seal(ctx, rec)
	return err
}

// toStageHops projecta a cadeia de delegação do Exchange para os hops do registo.
func toStageHops(hops []pipeline.DelegationHop) []Hop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]Hop, len(hops))
	for i, h := range hops {
		out[i] = Hop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}
