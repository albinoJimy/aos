package capabilitygap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	identity "github.com/aos-ref/platform/identity"
)

// State é a fase da máquina de admissão de um nó `capability_gap`. A ordem de
// declaração espelha a progressão canónica do pipeline; NÃO é alfabética. Os dois
// estados terminais não-despacháveis ([StateBlocked], [StateRejected]) e o único
// estado despachável ([StateResolved]) são o coração do gate: [GapNode.CanDispatch]
// só admite despacho em [StateResolved], e [StateResolved] só é alcançável
// atravessando TODAS as etapas por ordem + ratificação assinada.
type State string

const (
	// StateWaiting — estado INICIAL. O nó abriu o gap e BLOQUEIA: não despacha
	// (waiting_on_capability). Nenhuma skill foi ainda sintetizada.
	StateWaiting State = "waiting_on_capability"
	// StateAuthoring — o agente-autor governado produziu um artefacto taintado.
	StateAuthoring State = "authoring"
	// StateDryRun — o artefacto passou o dry-run (AOS-126).
	StateDryRun State = "dry_run"
	// StateEvalGate — o artefacto (taintado) passou o eval-gate (AOS-114/115/189).
	StateEvalGate State = "eval_gate"
	// StateCanary — o artefacto passou o canary.
	StateCanary State = "canary"
	// StateResolved — ratificação ASSINADA e verificada: o gap está resolvido. É o
	// ÚNICO estado em que o nó despacha.
	StateResolved State = "resolved"
	// StateBlocked — uma etapa do pipeline FALHOU fail-closed (dry-run/eval/canary).
	// Terminal: nunca despacha.
	StateBlocked State = "blocked"
	// StateRejected — o humano REJEITOU ou SUBSTITUIU o nó na ratificação. Terminal:
	// nunca despacha.
	StateRejected State = "rejected"
)

// Origin é a proveniência (imutável) de um artefacto de skill. É o TAINT: marca o
// artefacto como escrito pela máquina e acompanha-o por todo o pipeline. NÃO é
// "limpo" por nenhuma etapa automática — só a ratificação assinada autoriza
// produção.
type Origin string

// OriginSelfAuthored — o artefacto foi sintetizado pelo agente-autor a partir de
// uma spec de gap UNTRUSTED. É o único valor produzido por este pacote.
const OriginSelfAuthored Origin = "self_authored"

// Sentinelas de erro — todos fail-closed, comparáveis por errors.Is.
var (
	// ErrGapCeilingExceeded — abrir este gap excederia o teto de gaps do plano.
	ErrGapCeilingExceeded = errors.New("capabilitygap: teto de gaps por plano excedido (fail-closed)")
	// ErrStageOutOfOrder — um método de avanço foi chamado fora do seu estado
	// predecessor: uma tentativa de BYPASS do pipeline. Fail-closed.
	ErrStageOutOfOrder = errors.New("capabilitygap: etapa fora de ordem (bypass do pipeline recusado)")
	// ErrNodeBlocked — despacho pedido a um nó que não está `resolved` (ainda a
	// aguardar capacidade, bloqueado ou rejeitado). Fail-closed.
	ErrNodeBlocked = errors.New("capabilitygap: nó não despacha — capacidade não resolvida (fail-closed)")
	// ErrAllowlistViolation — o artefacto auto-escrito pediu uma tool FORA da
	// allowlist restrita do agente-autor: uma tentativa (via input untrusted) de
	// alargar autoridade. Fail-closed.
	ErrAllowlistViolation = errors.New("capabilitygap: tool pedida fora da allowlist restrita (fail-closed)")
	// ErrDryRunFailed — o dry-run (AOS-126) reprovou o artefacto.
	ErrDryRunFailed = errors.New("capabilitygap: dry-run reprovou (fail-closed)")
	// ErrEvalRejected — o eval-gate (AOS-114/115/189) não admitiu o artefacto.
	ErrEvalRejected = errors.New("capabilitygap: eval-gate não admitiu (fail-closed)")
	// ErrCanaryFailed — o canary reprovou o artefacto.
	ErrCanaryFailed = errors.New("capabilitygap: canary reprovou (fail-closed)")
	// ErrRatificationRefused — a ratificação assinada NÃO aprovou (rejeição humana,
	// não-verificada, sem token, ou não-apresentável). O nó fica bloqueado.
	ErrRatificationRefused = errors.New("capabilitygap: ratificação não aprovou (fail-closed)")
	// ErrRatificationReplaced — o humano SUBSTITUIU o nó por outro. O nó original
	// fica rejeitado; o substituto está em [GapNode.ReplacementNodeID].
	ErrRatificationReplaced = errors.New("capabilitygap: nó substituído por decisão humana")
	// ErrRatificationTransplant — a ratificação assinada está amarrada a OUTRO
	// artefacto (content-hash divergente): anti-transplante. Fail-closed.
	ErrRatificationTransplant = errors.New("capabilitygap: ratificação amarrada a outro artefacto (transplante recusado)")
	// ErrCoordinatorDeps — dependências obrigatórias do Coordinator em falta.
	ErrCoordinatorDeps = errors.New("capabilitygap: dependências obrigatórias em falta")
	// ErrInvalidConfig — configuração inválida (teto <=0, allowlist vazia, reserva).
	ErrInvalidConfig = errors.New("capabilitygap: configuração inválida")
	// ErrInvalidSpec — spec de gap malformada (plan_id/node_id/skill em falta).
	ErrInvalidSpec = errors.New("capabilitygap: spec de gap inválida")
)

// ---------------------------------------------------------------------------
// Portas (o wiring liga; nenhuma executa a skill).
// ---------------------------------------------------------------------------

// GapRecorder é a porta de emissão dos factos do gap. *plannerevents.Recorder
// satisfá-la — reutiliza as constantes `plan.capability_gap_opened/resolved` sem
// as redeclarar.
type GapRecorder interface {
	RecordCapabilityGap(ctx context.Context, p plannerevents.CapabilityGapPayload) (uint64, error)
}

// ChildIssuer é a porta de emissão da NHI PRÓPRIA do agente-autor (AOS-005/006).
// *identity.Issuer satisfá-la via IssueChild. A autoridade pedida é derivada da
// allowlist restrita — a NHI do autor nunca pode exceder o que a allowlist permite.
type ChildIssuer interface {
	IssueChild(ctx context.Context, parentCompact string, req identity.ChildRequest) (identity.Token, error)
}

// Reserver é a porta do orçamento do agente-autor (AOS-008). *budget.Budget
// satisfá-la. A síntese consome orçamento real: reserva antes, consolida em
// sucesso (Commit) e liberta em falha (Release) — sem leak.
type Reserver interface {
	Reserve(ctx context.Context, nodeID string, amt budget.Amount) (budget.Reservation, error)
	Commit(ctx context.Context, r budget.Reservation) error
	Release(ctx context.Context, r budget.Reservation) error
}

// SkillAuthor é a porta de GERAÇÃO da skill candidata — o passo apoiado no modelo.
// É UNTRUSTED: recebe a spec do gap (input untrusted) e a NHI do autor, e devolve
// um [CandidateSkill] cujo conteúdo NUNCA é executado por este pacote (só hashed e
// taintado). A allowlist restrita é passada para o autor a conhecer, mas a sua
// imposição é feita POR ESTE PACOTE, não por confiança na porta.
type SkillAuthor interface {
	Author(ctx context.Context, spec GapSpec, authorNHI identity.Token, allowlist []string) (CandidateSkill, error)
}

// DryRunner é a porta do dry-run (AOS-126). Recebe o artefacto TAINTADO.
type DryRunner interface {
	DryRun(ctx context.Context, art TaintedArtifact) (DryRunResult, error)
}

// EvalGate é a porta do eval-gate (AOS-114/115/189). Recebe o artefacto TAINTADO —
// o taint acompanha o artefacto ATÉ AQUI e mantém-se depois: o eval-gate não
// destainta, apenas produz um veredicto de admissão.
type EvalGate interface {
	Evaluate(ctx context.Context, art TaintedArtifact) (EvalVerdict, error)
}

// CanaryRunner é a porta do canary. Recebe o artefacto TAINTADO.
type CanaryRunner interface {
	Canary(ctx context.Context, art TaintedArtifact) (CanaryResult, error)
}

// Ratifier é a porta de ratificação ASSINADA (AOS-096/206) — o gate humano. Espelha
// hitl.RatificationGate: devolve um [RatificationOutcome] que só autoriza produção
// se for uma APROVAÇÃO verificada, amarrada por content-hash a ESTE artefacto. O
// humano pode REJEITAR ou SUBSTITUIR o nó.
type Ratifier interface {
	Ratify(ctx context.Context, req RatificationRequest) (RatificationOutcome, error)
}

// ---------------------------------------------------------------------------
// Tipos de dados.
// ---------------------------------------------------------------------------

// GapSpec descreve o gap de capacidade aberto por um nó do plano. A `CandidateSkill`
// é o id da skill em falta (não o conteúdo). É INPUT UNTRUSTED — deriva do plano
// proposto pelo LLM (ADR-005) — e a sua influência é confinada: nunca alarga a
// allowlist nem salta etapas.
type GapSpec struct {
	PlanID         string
	NodeID         string
	CandidateSkill string
}

func (s GapSpec) valid() bool {
	return s.PlanID != "" && s.NodeID != "" && s.CandidateSkill != ""
}

// CandidateSkill é o artefacto UNTRUSTED devolvido pelo [SkillAuthor]. `Content` é
// opaco — nunca executado aqui. `RequestedTools` tem de ser ⊆ allowlist restrita
// (imposto por este pacote, fail-closed).
type CandidateSkill struct {
	Name           string
	Version        string
	Content        []byte
	RequestedTools []string
	Meta           plannerevents.PlannerMeta
}

// TaintedArtifact é o [CandidateSkill] envolvido no seu TAINT: a origem imutável
// ([OriginSelfAuthored]) e o content-hash que amarra a ratificação a estes bytes
// exactos (anti-transplante). Acompanha o artefacto por todo o pipeline.
type TaintedArtifact struct {
	PlanID      string
	NodeID      string
	Skill       CandidateSkill
	Origin      Origin
	ContentHash string
}

// contentHash calcula o hash canónico do conteúdo do artefacto (name\0version\0content).
// Amarra a ratificação aos bytes promovidos — nunca o conteúdo/segredo entra no hash-input
// de forma reversível.
func contentHash(s CandidateSkill) string {
	h := sha256.New()
	_, _ = h.Write([]byte(s.Name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(s.Version))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(s.Content)
	return hex.EncodeToString(h.Sum(nil))
}

// DryRunResult é o veredicto do dry-run (AOS-126).
type DryRunResult struct {
	Passed bool
	Reason string
}

// EvalVerdict é o veredicto do eval-gate (AOS-114/115/189).
type EvalVerdict struct {
	Admitted bool
	Score    float64
	Reason   string
}

// CanaryResult é o veredicto do canary.
type CanaryResult struct {
	Passed bool
	Reason string
}

// RatificationDisposition é a disposição da decisão humana de ratificação.
type RatificationDisposition string

const (
	// DispositionApproved — o humano aprovou a promoção deste artefacto.
	DispositionApproved RatificationDisposition = "approved"
	// DispositionRejected — o humano rejeitou (o nó fica bloqueado).
	DispositionRejected RatificationDisposition = "rejected"
	// DispositionReplaced — o humano substituiu o nó por outro (ReplacementNodeID).
	DispositionReplaced RatificationDisposition = "replaced"
)

// RatificationRequest é o que se apresenta ao gate de ratificação. Carrega o
// content-hash do artefacto (o que o humano assina), o veredicto do eval e o
// canary — a pré-condição de apresentabilidade (espelha hitl.SelfModArtifact).
type RatificationRequest struct {
	PlanID       string
	NodeID       string
	ContentHash  string
	EvalVerdict  EvalVerdict
	CanaryPassed bool
}

// RatificationOutcome é o resultado do gate. Só `Approved` + `Verified` +
// `RatificationID != ""` + `BoundContentHash == artefacto` autoriza produção. Tudo
// o resto é fail-closed.
type RatificationOutcome struct {
	Disposition RatificationDisposition
	// Verified indica que a decisão foi assinada e verificada contra a chave PINADA
	// do ratificador (não-repúdio). Uma aprovação não-verificada NÃO promove.
	Verified bool
	// RatificationID é o token estável (anti-transplante) que a assinatura referencia.
	RatificationID string
	// BoundContentHash é o content-hash sobre o qual o humano ASSINOU. Tem de bater
	// com o do artefacto apresentado, senão é transplante (fail-closed).
	BoundContentHash string
	// ReplacementNodeID é o nó substituto quando Disposition == Replaced.
	ReplacementNodeID string
}

// ---------------------------------------------------------------------------
// Config e Coordinator.
// ---------------------------------------------------------------------------

// Config parametriza o Coordinator. MaxGapsPerPlan > 0 e Allowlist não-vazia são
// obrigatórios (fail-closed em [NewCoordinator]).
type Config struct {
	// MaxGapsPerPlan é o TETO de gaps que um plano pode abrir. >0 obrigatório.
	MaxGapsPerPlan int
	// ParentToken é o token compacto on-behalf-of o qual a NHI do agente-autor é
	// emitida. A NHI do autor herda a cadeia até um humano responsável.
	ParentToken string
	// AuthorClass é a classe de identidade da NHI do agente-autor.
	AuthorClass string
	// Allowlist RESTRITA de tools que o agente-autor pode pedir. Não-vazia. O input
	// untrusted NUNCA a alarga.
	Allowlist []string
	// AuthorBudgetNode é o nó de orçamento onde a síntese reserva. Obrigatório.
	AuthorBudgetNode string
	// AuthorReserve é a quantia reservada por síntese. Tem de ser uma reserva válida.
	AuthorReserve budget.Amount
}

// Coordinator governa os nós `capability_gap` de todos os planos: impõe o teto por
// plano, corre o agente-autor governado e detém as portas do pipeline. Seguro para
// uso concorrente (o contador de gaps é protegido por mutex; as portas são
// responsabilidade do wiring).
type Coordinator struct {
	rec      GapRecorder
	issuer   ChildIssuer
	reserver Reserver
	author   SkillAuthor
	dry      DryRunner
	eval     EvalGate
	canary   CanaryRunner
	ratifier Ratifier
	cfg      Config

	allow map[string]struct{} // allowlist como conjunto (lookup O(1))

	mu     sync.Mutex
	opened map[string]int // plan_id -> nº de gaps abertos (monotónico; teto)
}

// NewCoordinator constrói um Coordinator. Todas as portas são OBRIGATÓRIAS —
// ausência é fail-closed ([ErrCoordinatorDeps]). Config é validada
// ([ErrInvalidConfig]).
func NewCoordinator(rec GapRecorder, issuer ChildIssuer, reserver Reserver, author SkillAuthor,
	dry DryRunner, eval EvalGate, canary CanaryRunner, ratifier Ratifier, cfg Config) (*Coordinator, error) {
	if rec == nil || issuer == nil || reserver == nil || author == nil ||
		dry == nil || eval == nil || canary == nil || ratifier == nil {
		return nil, ErrCoordinatorDeps
	}
	if cfg.MaxGapsPerPlan <= 0 || len(cfg.Allowlist) == 0 || cfg.AuthorBudgetNode == "" {
		return nil, ErrInvalidConfig
	}
	// AuthorReserve tem de ser uma reserva legítima: nenhuma dimensão negativa e
	// pelo menos uma positiva (reservar zero/negativo não faz sentido). A validação
	// autoritativa é do budget no Reserve; aqui rejeita-se cedo, fail-closed.
	if cfg.AuthorReserve.Tokens < 0 || cfg.AuthorReserve.CostMicroUSD < 0 ||
		(cfg.AuthorReserve.Tokens == 0 && cfg.AuthorReserve.CostMicroUSD == 0) {
		return nil, ErrInvalidConfig
	}
	allow := make(map[string]struct{}, len(cfg.Allowlist))
	for _, t := range cfg.Allowlist {
		if t != "" {
			allow[t] = struct{}{}
		}
	}
	if len(allow) == 0 {
		return nil, ErrInvalidConfig
	}
	return &Coordinator{
		rec:      rec,
		issuer:   issuer,
		reserver: reserver,
		author:   author,
		dry:      dry,
		eval:     eval,
		canary:   canary,
		ratifier: ratifier,
		cfg:      cfg,
		allow:    allow,
		opened:   make(map[string]int),
	}, nil
}

// OpenGap abre um gap de capacidade: impõe o TETO por plano (fail-closed), emite
// `plan.capability_gap_opened`, e devolve um [GapNode] em [StateWaiting] (bloqueado
// — não despacha). Se a emissão falhar, o slot é devolvido e nenhum nó é criado.
func (c *Coordinator) OpenGap(ctx context.Context, spec GapSpec) (*GapNode, error) {
	if !spec.valid() {
		return nil, ErrInvalidSpec
	}
	// Teto por plano (monotónico): reserva o slot ANTES dos efeitos.
	c.mu.Lock()
	if c.opened[spec.PlanID] >= c.cfg.MaxGapsPerPlan {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: plano %q (max %d)", ErrGapCeilingExceeded, spec.PlanID, c.cfg.MaxGapsPerPlan)
	}
	c.opened[spec.PlanID]++
	c.mu.Unlock()

	_, err := c.rec.RecordCapabilityGap(ctx, plannerevents.CapabilityGapPayload{
		PlanID:         spec.PlanID,
		NodeID:         spec.NodeID,
		State:          plannerevents.GapOpened,
		CandidateSkill: spec.CandidateSkill,
	})
	if err != nil {
		// Devolve o slot: a abertura não persistiu, o teto não deve contá-la.
		c.mu.Lock()
		if c.opened[spec.PlanID] > 0 {
			c.opened[spec.PlanID]--
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("capabilitygap: emitir gap_opened: %w", err)
	}
	return &GapNode{co: c, spec: spec, state: StateWaiting}, nil
}

// authorSkill corre o AGENTE-AUTOR GOVERNADO: emite a NHI própria (autoridade =
// allowlist), reserva orçamento, chama o [SkillAuthor] (untrusted) e IMPÕE a
// allowlist restrita. Consolida o orçamento em sucesso; liberta-o em qualquer
// falha (sem leak). Devolve o artefacto TAINTADO. NÃO executa o conteúdo.
func (c *Coordinator) authorSkill(ctx context.Context, spec GapSpec) (TaintedArtifact, error) {
	// 1) NHI PRÓPRIA do agente-autor, on-behalf-of o pai, autoridade = allowlist.
	authorNHI, err := c.issuer.IssueChild(ctx, c.cfg.ParentToken, identity.ChildRequest{
		AgentID:    "author:" + spec.PlanID + ":" + spec.NodeID,
		AgentClass: c.cfg.AuthorClass,
		PolicyRef:  "capabilitygap.author",
		Authority:  append([]string(nil), c.cfg.Allowlist...),
	})
	if err != nil {
		return TaintedArtifact{}, fmt.Errorf("capabilitygap: emitir NHI do autor: %w", err)
	}

	// 2) Reserva de orçamento ANTES da geração (fail-closed se não couber).
	res, err := c.reserver.Reserve(ctx, c.cfg.AuthorBudgetNode, c.cfg.AuthorReserve)
	if err != nil {
		return TaintedArtifact{}, fmt.Errorf("capabilitygap: reservar orçamento do autor: %w", err)
	}

	// 3) Geração UNTRUSTED. Qualquer falha a jusante liberta a reserva.
	cand, err := c.author.Author(ctx, spec, authorNHI, c.cfg.Allowlist)
	if err != nil {
		_ = c.reserver.Release(ctx, res)
		return TaintedArtifact{}, fmt.Errorf("capabilitygap: geração da skill: %w", err)
	}

	// 4) IMPOSIÇÃO da allowlist restrita: o input untrusted NÃO alarga autoridade.
	for _, t := range cand.RequestedTools {
		if _, ok := c.allow[t]; !ok {
			_ = c.reserver.Release(ctx, res)
			return TaintedArtifact{}, fmt.Errorf("%w: %q", ErrAllowlistViolation, t)
		}
	}

	// 5) Sucesso: consolida o orçamento e devolve o artefacto TAINTADO.
	if err := c.reserver.Commit(ctx, res); err != nil {
		_ = c.reserver.Release(ctx, res)
		return TaintedArtifact{}, fmt.Errorf("capabilitygap: consolidar orçamento do autor: %w", err)
	}
	return TaintedArtifact{
		PlanID:      spec.PlanID,
		NodeID:      spec.NodeID,
		Skill:       cand,
		Origin:      OriginSelfAuthored,
		ContentHash: contentHash(cand),
	}, nil
}

// ---------------------------------------------------------------------------
// GapNode — máquina de estados por-nó, sem bypass.
// ---------------------------------------------------------------------------

// GapNode é a máquina de admissão de UM nó `capability_gap`. Não é seguro para uso
// concorrente por si só (é conduzido por um único fluxo de admissão por nó); o
// teto e a emissão vivem no Coordinator, thread-safe.
type GapNode struct {
	co    *Coordinator
	spec  GapSpec
	state State

	artifact       TaintedArtifact
	evalVerdict    EvalVerdict
	canaryPassed   bool
	ratificationID string
	replacement    string
}

// State devolve o estado corrente.
func (n *GapNode) State() State { return n.state }

// Artifact devolve o artefacto taintado (vazio antes de [GapNode.Author]).
func (n *GapNode) Artifact() TaintedArtifact { return n.artifact }

// RatificationID devolve o token da ratificação assinada (vazio até resolver).
func (n *GapNode) RatificationID() string { return n.ratificationID }

// ReplacementNodeID devolve o nó substituto quando o humano substituiu (vazio senão).
func (n *GapNode) ReplacementNodeID() string { return n.replacement }

// CanDispatch é o GATE DE ESTADO: só permite despacho quando o gap está `resolved`.
// Em qualquer outro estado (a aguardar capacidade, no pipeline, bloqueado ou
// rejeitado) recusa fail-closed. É esta função que impede o nó de despachar antes
// da ratificação — sem ela, o nó despacharia no estado inicial.
func (n *GapNode) CanDispatch() error {
	if n.state != StateResolved {
		return fmt.Errorf("%w: estado %q", ErrNodeBlocked, n.state)
	}
	return nil
}

// Author avança waiting → authoring, correndo o agente-autor governado. Fail-closed:
// só corre a partir de [StateWaiting] (bypass recusado). Uma falha da síntese deixa
// o nó em [StateWaiting] (continua a aguardar capacidade) e propaga o erro.
func (n *GapNode) Author(ctx context.Context) error {
	if n.state != StateWaiting {
		return fmt.Errorf("%w: Author exige %q, está %q", ErrStageOutOfOrder, StateWaiting, n.state)
	}
	art, err := n.co.authorSkill(ctx, n.spec)
	if err != nil {
		return err
	}
	n.artifact = art
	n.state = StateAuthoring
	return nil
}

// DryRun avança authoring → dry_run (AOS-126). Fail-closed: exige [StateAuthoring];
// uma reprovação bloqueia o nó ([StateBlocked]).
func (n *GapNode) DryRun(ctx context.Context) error {
	if n.state != StateAuthoring {
		return fmt.Errorf("%w: DryRun exige %q, está %q", ErrStageOutOfOrder, StateAuthoring, n.state)
	}
	res, err := n.co.dry.DryRun(ctx, n.artifact)
	if err != nil {
		n.state = StateBlocked
		return fmt.Errorf("capabilitygap: dry-run: %w", err)
	}
	if !res.Passed {
		n.state = StateBlocked
		return fmt.Errorf("%w: %s", ErrDryRunFailed, res.Reason)
	}
	n.state = StateDryRun
	return nil
}

// EvalGate avança dry_run → eval_gate (AOS-114/115/189). O artefacto passa TAINTADO.
// Fail-closed: exige [StateDryRun]; não-admissão bloqueia ([StateBlocked]).
func (n *GapNode) EvalGate(ctx context.Context) error {
	if n.state != StateDryRun {
		return fmt.Errorf("%w: EvalGate exige %q, está %q", ErrStageOutOfOrder, StateDryRun, n.state)
	}
	v, err := n.co.eval.Evaluate(ctx, n.artifact)
	if err != nil {
		n.state = StateBlocked
		return fmt.Errorf("capabilitygap: eval-gate: %w", err)
	}
	if !v.Admitted {
		n.state = StateBlocked
		return fmt.Errorf("%w: %s", ErrEvalRejected, v.Reason)
	}
	n.evalVerdict = v
	n.state = StateEvalGate
	return nil
}

// Canary avança eval_gate → canary. Fail-closed: exige [StateEvalGate]; reprovação
// bloqueia ([StateBlocked]).
func (n *GapNode) Canary(ctx context.Context) error {
	if n.state != StateEvalGate {
		return fmt.Errorf("%w: Canary exige %q, está %q", ErrStageOutOfOrder, StateEvalGate, n.state)
	}
	res, err := n.co.canary.Canary(ctx, n.artifact)
	if err != nil {
		n.state = StateBlocked
		return fmt.Errorf("capabilitygap: canary: %w", err)
	}
	if !res.Passed {
		n.state = StateBlocked
		return fmt.Errorf("%w: %s", ErrCanaryFailed, res.Reason)
	}
	n.canaryPassed = true
	n.state = StateCanary
	return nil
}

// Ratify avança canary → resolved SÓ com ratificação ASSINADA, verificada, aprovada
// e amarrada por content-hash a ESTE artefacto (AOS-096/206). Fail-closed:
//
//   - exige [StateCanary] (bypass recusado);
//   - REJEIÇÃO humana ⇒ [StateRejected] + [ErrRatificationRefused];
//   - SUBSTITUIÇÃO humana ⇒ [StateRejected] + [ErrRatificationReplaced] (o nó
//     substituto fica em [GapNode.ReplacementNodeID]);
//   - aprovação NÃO verificada / sem token ⇒ [StateRejected] + [ErrRatificationRefused];
//   - content-hash divergente (transplante) ⇒ [StateRejected] + [ErrRatificationTransplant];
//   - só a aprovação legítima emite `plan.capability_gap_resolved` e resolve.
func (n *GapNode) Ratify(ctx context.Context) error {
	if n.state != StateCanary {
		return fmt.Errorf("%w: Ratify exige %q, está %q", ErrStageOutOfOrder, StateCanary, n.state)
	}
	out, err := n.co.ratifier.Ratify(ctx, RatificationRequest{
		PlanID:       n.spec.PlanID,
		NodeID:       n.spec.NodeID,
		ContentHash:  n.artifact.ContentHash,
		EvalVerdict:  n.evalVerdict,
		CanaryPassed: n.canaryPassed,
	})
	if err != nil {
		// Erro do gate é tratado como bloqueio (fail-closed): nunca promove.
		n.state = StateRejected
		return fmt.Errorf("capabilitygap: ratificação: %w", err)
	}

	switch out.Disposition {
	case DispositionReplaced:
		n.replacement = out.ReplacementNodeID
		n.state = StateRejected
		return fmt.Errorf("%w: substituto %q", ErrRatificationReplaced, out.ReplacementNodeID)
	case DispositionApproved:
		// continua para as verificações fail-closed abaixo
	default: // DispositionRejected ou desconhecido
		n.state = StateRejected
		return ErrRatificationRefused
	}

	// APROVAÇÃO: tem de ser verificada, com token, e amarrada a ESTE artefacto.
	if !out.Verified || out.RatificationID == "" {
		n.state = StateRejected
		return fmt.Errorf("%w: aprovação não-verificada ou sem token", ErrRatificationRefused)
	}
	if out.BoundContentHash != n.artifact.ContentHash {
		n.state = StateRejected
		return fmt.Errorf("%w: esperado %s, assinado %s", ErrRatificationTransplant,
			n.artifact.ContentHash, out.BoundContentHash)
	}

	// Resolvido: emite `plan.capability_gap_resolved` com a RatificationID. Se a
	// emissão falhar, NÃO resolve (fail-closed: audit-before-effect).
	_, err = n.co.rec.RecordCapabilityGap(ctx, plannerevents.CapabilityGapPayload{
		PlanID:         n.spec.PlanID,
		NodeID:         n.spec.NodeID,
		State:          plannerevents.GapResolved,
		CandidateSkill: n.spec.CandidateSkill,
		RatificationID: out.RatificationID,
	})
	if err != nil {
		n.state = StateRejected
		return fmt.Errorf("capabilitygap: emitir gap_resolved: %w", err)
	}
	n.ratificationID = out.RatificationID
	n.state = StateResolved
	return nil
}
