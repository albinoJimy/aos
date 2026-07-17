package risk

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Maturity é o nível de MATURIDADE do principal/utilizador, o eixo que a
// auto-aprovação usa para calibrar a fricção (anti-fatigue). É ordenado: um
// utilizador mais maduro auto-aprova classes mais altas — MAS nunca danger (ver
// [AutoApprovePolicy.Allows]). O valor-zero é o mais conservador (novice).
type Maturity uint8

const (
	// MaturityNovice é o valor-zero: nenhuma auto-aprovação (toda a classe > safe
	// gera fricção).
	MaturityNovice Maturity = iota
	// MaturityExperienced — auto-aprova safe.
	MaturityExperienced
	// MaturityTrusted — auto-aprova safe e gray (nunca danger).
	MaturityTrusted
)

// AutoApprovePolicy configura a AUTO-APROVAÇÃO por CLASSE e MATURIDADE (anti
// approval-fatigue). É a peça configurável; a INVARIANTE não-negociável — danger/
// irreversível NUNCA é auto-aprovável — é imposta ESTRUTURALMENTE em [Allows] (o
// caso [ClassDanger] devolve sempre false, ignorando quaisquer campos/maturidade).
type AutoApprovePolicy struct {
	// GrayFrom é a maturidade MÍNIMA a partir da qual a classe GRAY é auto-aprovada.
	// Abaixo dela, gray gera a confirmação de lote. Não existe campo equivalente para
	// danger — por construção, danger não é auto-aprovável.
	GrayFrom Maturity
}

// DefaultAutoApprove é a configuração de referência: gray só se auto-aprova a
// partir de [MaturityTrusted]; safe corre sempre sem gate (não depende de config).
func DefaultAutoApprove() AutoApprovePolicy {
	return AutoApprovePolicy{GrayFrom: MaturityTrusted}
}

// Allows indica se a classe é AUTO-APROVÁVEL para a maturidade dada, SEM
// confirmação humana. Prova ESTRUTURAL do anti-bypass: o caso [ClassDanger]
// devolve false incondicionalmente (a maturidade é ignorada), pelo que NENHUMA
// configuração ou maturidade pode auto-aprovar danger/irreversível — o caminho de
// auto-approve NÃO alcança a classe danger (fail-closed). SAFE é sempre
// auto-aprovável (corre sem gate por definição). GRAY depende da maturidade.
func (p AutoApprovePolicy) Allows(class Class, m Maturity) bool {
	switch class {
	case ClassSafe:
		return true
	case ClassGray:
		return m >= p.GrayFrom
	case ClassDanger:
		// NUNCA auto-aprovável sem confirmação (ADR-013). Estrutural: não há campo de
		// política nem valor de maturidade que altere este retorno.
		return false
	default:
		// Classe desconhecida ⇒ fail-closed (não auto-aprova).
		return false
	}
}

// ConfirmationRequest é o pedido de confirmação HITL apresentado ao utilizador. O
// PREVIEW é o efeito CONCRETO RESOLVIDO da acção (não um genérico) — o
// diferenciador do ADR-013: o utilizador vê exactamente o que vai acontecer
// (capability + recurso resolvido) antes de aprovar. Sem segredos (nunca o Input
// da tool).
type ConfirmationRequest struct {
	// Class é a classe de risco que motivou a confirmação (gray em lote; danger
	// individual).
	Class Class
	// Batch indica que o pedido cobre um LOTE de acções gray (uma confirmação para o
	// grupo, anti-fatigue) em vez de uma acção individual.
	Batch bool
	// Irreversible marca uma acção que não pode ser desfeita (o gate impõe-lhe o
	// timeout fail-closed).
	Irreversible bool
	// Preview é o efeito CONCRETO resolvido (ex.: "cap:http.post -> https://x/y").
	Preview string
	// BatchSummary descreve a CLASSE DE EQUIVALÊNCIA que uma confirmação de lote gray
	// cobre (todas as acções gray do run com a mesma capability e destino). Torna
	// explícito, para o humano, o âmbito exacto que uma única aprovação autoriza —
	// não um "run inteiro" opaco (SAROC-03).
	BatchSummary string
	// Principal, Capability e Resource identificam a acção para atribuição no audit.
	Principal  string
	Capability string
	Resource   string
}

// ConfirmationResponse é a resposta do canal HITL.
type ConfirmationResponse struct {
	// Approved é true se o humano aprovou a acção (um OVERRIDE do gate).
	Approved bool
	// Approver identifica quem decidiu (atribuição no audit).
	Approver string
}

// ConfirmationChannel é a PORTA HITL (human-in-the-loop) por onde o gate escala
// uma acção gray (lote) ou danger (individual) para confirmação humana. É a
// fronteira estável entre o gate de risco e o HITL do EPIC-09 (NÃO construído
// aqui): a impl de referência é síncrona e determinista; o HITL real (possivelmente
// assíncrono) liga-se por trás desta porta, e o TIMEOUT do gate garante o
// fail-closed independentemente da latência do canal.
//
// Contrato: Confirm DEVE respeitar o ctx — um ctx expirado/cancelado tem de
// devolver prontamente (o gate cancela o ctx no timeout de uma acção irreversível).
type ConfirmationChannel interface {
	Confirm(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error)
}

// DenyChannel é uma [ConfirmationChannel] que nega tudo (nunca aprova). É o default
// fail-closed: sem um canal HITL ligado, nenhuma acção gray/danger passa. Produção
// liga o HITL real (EPIC-09).
type DenyChannel struct{}

// Confirm implementa [ConfirmationChannel]: nega sempre.
func (DenyChannel) Confirm(context.Context, ConfirmationRequest) (ConfirmationResponse, error) {
	return ConfirmationResponse{Approved: false}, nil
}

// Metrics são os contadores de observabilidade do gate de risco (molde de SLI por
// porta, AOS-061 — sem SDK OTel, que é EPIC-08). O OVERRIDE-RATE (anti
// rubber-stamping) é a fracção de acções gray/danger PROMPTED que o utilizador
// APROVA (override do gate). Todos os acessos são atómicos.
type Metrics struct {
	// Prompted é o número de acções gray/danger que chegaram a uma confirmação HITL
	// (não auto-aprovadas). O denominador do override-rate.
	Prompted atomic.Uint64
	// Overrides é o número dessas que o utilizador APROVOU (override do gate). O
	// numerador do override-rate.
	Overrides atomic.Uint64
	// AutoApproved é o número de acções gray auto-aprovadas por maturidade (não
	// contam para o override-rate: não houve prompt, é configuração, não
	// rubber-stamping).
	AutoApproved atomic.Uint64
	// BatchCovered é o número de acções gray que apanharam BOLEIA numa confirmação de
	// lote JÁ dada (reuso da decisão, sem novo prompt). Expõe a AMPLIFICAÇÃO
	// aprovação→efeitos que o override-rate não vê: 1 aprovação a cobrir N acções
	// conta Prompted=1 mas BatchCovered=N-1. Um valor alto sinaliza lotes demasiado
	// largos (SAROC-06). Acesso atómico.
	BatchCovered atomic.Uint64
	// Denials é o número de acções negadas pelo gate (rejeição ou timeout fail-closed).
	Denials atomic.Uint64
	// Timeouts é o número de acções irreversíveis negadas por TIMEOUT (fail-closed).
	Timeouts atomic.Uint64
}

// OverrideRate devolve a fracção de acções prompted que o utilizador aprovou
// (Overrides/Prompted), em [0,1]. Um valor alto sinaliza rubber-stamping. Devolve 0
// se nada foi prompted (sem divisão por zero).
func (m *Metrics) OverrideRate() float64 {
	prompted := m.Prompted.Load()
	if prompted == 0 {
		return 0
	}
	return float64(m.Overrides.Load()) / float64(prompted)
}

// Snapshot devolve uma leitura consistente-o-suficiente dos contadores mais o
// override-rate derivado.
func (m *Metrics) Snapshot() (prompted, overrides, autoApproved, denials, timeouts uint64, overrideRate float64) {
	prompted = m.Prompted.Load()
	overrides = m.Overrides.Load()
	autoApproved = m.AutoApproved.Load()
	denials = m.Denials.Load()
	timeouts = m.Timeouts.Load()
	if prompted > 0 {
		overrideRate = float64(overrides) / float64(prompted)
	}
	return
}

// Outcome é o veredicto do gate SA-ROC para uma acção.
type Outcome uint8

const (
	// OutcomeAllow — a acção corre (safe sem gate, ou gray/danger confirmada/
	// auto-aprovada).
	OutcomeAllow Outcome = iota
	// OutcomeDeny — a acção é negada (rejeição HITL, timeout fail-closed, ou sem canal).
	OutcomeDeny
)

// GateResult é o resultado da avaliação SA-ROC de uma acção.
type GateResult struct {
	Outcome Outcome
	Class   Class
	// Batched indica que a decisão veio de uma confirmação de LOTE gray (agregada),
	// não de uma confirmação individual.
	Batched bool
	// AutoApproved indica que a acção foi auto-aprovada por maturidade (sem HITL).
	AutoApproved bool
	// TimedOut indica que a negação resultou do timeout fail-closed (irreversível).
	TimedOut bool
	// Approver identifica QUEM confirmou a acção (do [ConfirmationResponse.Approver]),
	// para atribuição no audit tamper-evident: um override danger/gray fica ligado à
	// identidade que o autorizou. Vazio quando não houve confirmação humana
	// (auto-aprovada, timeout, recusada, ou sem canal). Sem segredos (SAROC-05).
	Approver string
	// Reason descreve a decisão (sem segredos).
	Reason string
}

// DecisionMode devolve a NATUREZA da decisão HITL, para o audit distinguir COMO uma
// acção foi resolvida (não só permit/deny): "auto" (auto-aprovada por maturidade,
// sem humano), "batch" (coberta por uma confirmação de lote gray), "human" (danger
// confirmada individualmente por um humano), "timeout" (negada pelo timeout
// fail-closed), "denied" (recusada pelo humano / sem canal). Torna um permit gray
// auto-aprovado distinguível de um confirmado por humano no log (SAROC-05).
func (r GateResult) DecisionMode() string {
	switch {
	case r.AutoApproved:
		return "auto"
	case r.TimedOut:
		return "timeout"
	case r.Batched:
		return "batch"
	case r.Outcome == OutcomeAllow:
		return "human"
	default:
		return "denied"
	}
}

// Request é a acção submetida ao gate, já classificada pelo RiskGate, mais os
// metadados de atribuição/preview.
type Request struct {
	// Classification é o veredicto do classificador (classe + eixos + versão).
	Classification Classification
	// BatchKey agrupa acções gray na MESMA confirmação de lote (tipicamente o RunID).
	// Vazio ⇒ cada acção gray é o seu próprio lote (uma confirmação por acção).
	BatchKey string
	// Preview é o efeito CONCRETO resolvido, apresentado na confirmação danger.
	Preview string
	// BatchSummary descreve a classe de equivalência de um lote gray (ver
	// [ConfirmationRequest.BatchSummary]).
	BatchSummary string
	// Principal, Capability e Resource identificam a acção (atribuição/audit).
	Principal  string
	Capability string
	Resource   string
}

// ErrNoChannel é devolvido na construção quando falta o canal de confirmação.
var ErrNoChannel = errors.New("risk: canal de confirmacao ausente")

// Gate é o motor SA-ROC (ADR-013): aplica a fricção proporcional à classe. É
// seguro para concorrência. Construir com [NewGate].
type Gate struct {
	channel   ConfirmationChannel
	autoApp   AutoApprovePolicy
	maturity  Maturity
	timeout   time.Duration
	now       func() time.Time
	metrics   Metrics
	mu        sync.Mutex
	batchSeen map[string]batchDecision // batchKey → decisão de lote já tomada
}

// batchDecision é a decisão memoizada de um lote gray: se foi aprovado e QUEM o
// aprovou (para atribuir no audit as acções que reutilizam a decisão do lote).
type batchDecision struct {
	approved bool
	approver string
}

// GateOption configura o [Gate].
type GateOption func(*Gate)

// WithMaturity fixa a maturidade do principal (calibra a auto-aprovação).
func WithMaturity(m Maturity) GateOption { return func(g *Gate) { g.maturity = m } }

// WithAutoApprove substitui a política de auto-aprovação.
func WithAutoApprove(p AutoApprovePolicy) GateOption { return func(g *Gate) { g.autoApp = p } }

// WithTimeout fixa o timeout de guarda fail-closed das acções DANGER. Um valor <= 0
// desactiva o timeout para acções danger REVERSÍVEIS (o gate depende então do ctx do
// chamador) — NÃO recomendado em produção. Para acções IRREVERSÍVEIS o timeout NÃO é
// desactivável: com d <= 0 aplica-se o piso [DefaultTimeout], garantindo sempre um
// bound temporal fail-closed independente do ctx do chamador. O default é
// [DefaultTimeout] para toda a classe danger.
func WithTimeout(d time.Duration) GateOption { return func(g *Gate) { g.timeout = d } }

// WithClock injecta o relógio usado para computar a deadline do timeout
// fail-closed. Permite testes DETERMINISTAS (sem sleeps reais): um relógio que
// devolva um instante já passado faz a deadline de uma acção irreversível expirar
// de imediato. Por omissão usa [time.Now].
func WithClock(f func() time.Time) GateOption { return func(g *Gate) { g.now = f } }

// DefaultTimeout é o timeout fail-closed por omissão de uma acção irreversível: a
// ausência de aprovação dentro deste intervalo NEGA.
const DefaultTimeout = 30 * time.Second

// NewGate constrói o gate SA-ROC com o canal HITL dado. Um canal nil devolve
// [ErrNoChannel] (fail-closed: sem forma de confirmar, o gate não pode operar).
// Por omissão usa [DefaultAutoApprove], [MaturityNovice] e [DefaultTimeout].
func NewGate(channel ConfirmationChannel, opts ...GateOption) (*Gate, error) {
	if channel == nil {
		return nil, ErrNoChannel
	}
	g := &Gate{
		channel:   channel,
		autoApp:   DefaultAutoApprove(),
		maturity:  MaturityNovice,
		timeout:   DefaultTimeout,
		now:       time.Now,
		batchSeen: make(map[string]batchDecision),
	}
	for _, o := range opts {
		o(g)
	}
	if g.now == nil {
		g.now = time.Now
	}
	return g, nil
}

// Metrics devolve o ponteiro para os contadores (inclui o override-rate).
func (g *Gate) Metrics() *Metrics { return &g.metrics }

// Evaluate aplica o gate SA-ROC (ADR-013) a uma acção classificada:
//
//   - SAFE → corre SEM gate (allow imediato, sem fricção, sem HITL).
//   - GRAY → auto-aprovável por maturidade? allow (sem HITL). Senão AGRUPA numa
//     confirmação de LOTE (uma confirmação por [Request.BatchKey], não uma por
//     acção): a primeira acção gray do lote consulta o canal com um resumo; as
//     seguintes reutilizam a decisão do grupo.
//   - DANGER → NUNCA auto-aprovável. Escala para CONFIRMAÇÃO INDIVIDUAL com o
//     PREVIEW concreto e um TIMEOUT DE GUARDA fail-closed: a ausência de resposta
//     dentro do intervalo NEGA (nunca permite). Para acções IRREVERSÍVEIS o timeout
//     é não-desactivável (piso [DefaultTimeout] mesmo com WithTimeout(<=0)).
//
// Uma aprovação numa acção gray/danger PROMPTED conta como OVERRIDE (métrica
// anti-rubber-stamping). A decisão é determinista dado o canal e o relógio.
func (g *Gate) Evaluate(ctx context.Context, req Request) GateResult {
	class := req.Classification.Class
	switch class {
	case ClassSafe:
		// Corre sem gate. Sem fricção, sem HITL, sem contagem de override.
		return GateResult{Outcome: OutcomeAllow, Class: ClassSafe, Reason: "safe: corre sem gate"}

	case ClassGray:
		// Anti-fatigue: auto-aprovável por maturidade?
		if g.autoApp.Allows(ClassGray, g.maturity) {
			g.metrics.AutoApproved.Add(1)
			return GateResult{Outcome: OutcomeAllow, Class: ClassGray, AutoApproved: true, Reason: "gray: auto-aprovada por maturidade"}
		}
		// Agrupa em lote: uma confirmação para o grupo (BatchKey).
		return g.evaluateBatch(ctx, req)

	default: // ClassDanger (inclui o valor-zero fail-closed)
		return g.evaluateDanger(ctx, req)
	}
}

// evaluateBatch trata a classe GRAY: uma ÚNICA confirmação por [Request.BatchKey]
// cobre todas as acções gray do lote (anti-fatigue). A decisão do grupo é
// memoizada: a primeira acção gray do lote consulta o canal; as seguintes reutilizam
// o veredicto sem novo prompt.
func (g *Gate) evaluateBatch(ctx context.Context, req Request) GateResult {
	// Lote sem chave ⇒ trata-se como um lote individual (uma confirmação para esta
	// acção). Nunca partilha decisão com outras.
	if req.BatchKey != "" {
		g.mu.Lock()
		if prior, ok := g.batchSeen[req.BatchKey]; ok {
			g.mu.Unlock()
			if prior.approved {
				// Acção coberta por uma aprovação de lote JÁ dada: reutiliza a decisão
				// (e o aprovador) sem novo prompt, mas contabiliza a AMPLIFICAÇÃO.
				g.metrics.BatchCovered.Add(1)
				return GateResult{Outcome: OutcomeAllow, Class: ClassGray, Batched: true, Approver: prior.approver, Reason: "gray: coberta pela confirmacao de lote"}
			}
			return GateResult{Outcome: OutcomeDeny, Class: ClassGray, Batched: true, Reason: "gray: lote rejeitado"}
		}
		g.mu.Unlock()
	}

	// Primeira acção do lote (ou lote sem chave): consulta o canal com o RESUMO do
	// âmbito que esta aprovação cobre (a classe de equivalência, não o run opaco).
	resp, err := g.confirm(ctx, ConfirmationRequest{
		Class:        ClassGray,
		Batch:        true,
		Preview:      req.Preview,
		BatchSummary: req.BatchSummary,
		Principal:    req.Principal,
		Capability:   req.Capability,
		Resource:     req.Resource,
	})
	approved := err == nil && resp.Approved
	approver := ""
	if approved {
		approver = resp.Approver
	}
	g.recordPrompt(approved)

	if req.BatchKey != "" {
		g.mu.Lock()
		// Só memoiza se ainda ninguém decidiu (uma corrida concorrente pode ter
		// decidido primeiro; nesse caso respeita-se a decisão existente).
		if prior, ok := g.batchSeen[req.BatchKey]; ok {
			approved = prior.approved
			approver = prior.approver
		} else {
			g.batchSeen[req.BatchKey] = batchDecision{approved: approved, approver: approver}
		}
		g.mu.Unlock()
	}

	if approved {
		return GateResult{Outcome: OutcomeAllow, Class: ClassGray, Batched: true, Approver: approver, Reason: "gray: lote confirmado"}
	}
	g.metrics.Denials.Add(1)
	return GateResult{Outcome: OutcomeDeny, Class: ClassGray, Batched: true, Reason: "gray: lote nao confirmado (fail-closed)"}
}

// evaluateDanger trata a classe DANGER: confirmação INDIVIDUAL com preview
// concreto. NUNCA auto-aprovável. Acções irreversíveis têm timeout fail-closed.
func (g *Gate) evaluateDanger(ctx context.Context, req Request) GateResult {
	irreversible := req.Classification.Irreversible()

	// TIMEOUT DE GUARDA FAIL-CLOSED para TODA a classe danger (não só a irreversível):
	// nenhuma acção danger fica pendente indefinidamente à espera do HITL, mesmo que o
	// ctx do chamador não tenha deadline (SAROC-07). Para acções IRREVERSÍVEIS o bound
	// é NÃO-DESACTIVÁVEL: mesmo com timeout<=0 aplica-se o piso [DefaultTimeout],
	// garantindo um limite temporal independente do ctx do chamador (o pior caso — o
	// efeito não pode ser desfeito — nunca fica sem guarda por misconfiguração).
	timeout := g.timeout
	if irreversible && timeout <= 0 {
		timeout = DefaultTimeout
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		deadline := g.now().Add(timeout)
		callCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	resp, err := g.confirm(callCtx, ConfirmationRequest{
		Class:        ClassDanger,
		Irreversible: irreversible,
		Preview:      req.Preview,
		Principal:    req.Principal,
		Capability:   req.Capability,
		Resource:     req.Resource,
	})

	// Timeout / cancelamento numa acção (tipicamente irreversível) ⇒ DENY fail-closed.
	// A ausência de aprovação NEGA, nunca permite.
	if err != nil || callCtx.Err() != nil {
		g.recordPrompt(false)
		g.metrics.Denials.Add(1)
		timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)
		if timedOut {
			g.metrics.Timeouts.Add(1)
		}
		return GateResult{
			Outcome:  OutcomeDeny,
			Class:    ClassDanger,
			TimedOut: timedOut,
			Reason:   "danger: sem aprovacao (timeout fail-closed)",
		}
	}

	g.recordPrompt(resp.Approved)
	if resp.Approved {
		return GateResult{Outcome: OutcomeAllow, Class: ClassDanger, Approver: resp.Approver, Reason: "danger: confirmada individualmente"}
	}
	g.metrics.Denials.Add(1)
	return GateResult{Outcome: OutcomeDeny, Class: ClassDanger, Reason: "danger: confirmacao recusada (fail-closed)"}
}

// confirm invoca o canal HITL respeitando o ctx. Um panic do canal converte-se em
// erro (fail-closed): o gate nega em vez de propagar a falha.
func (g *Gate) confirm(ctx context.Context, req ConfirmationRequest) (resp ConfirmationResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			resp = ConfirmationResponse{Approved: false}
			err = errors.New("risk: canal de confirmacao entrou em panic (fail-closed)")
		}
	}()
	// Se o ctx já expirou antes de chamar o canal, não chama — nega fail-closed.
	if ctx.Err() != nil {
		return ConfirmationResponse{Approved: false}, ctx.Err()
	}
	return g.channel.Confirm(ctx, req)
}

// recordPrompt contabiliza uma acção que chegou a uma confirmação HITL (o
// denominador do override-rate) e, se aprovada, o override (o numerador).
func (g *Gate) recordPrompt(approved bool) {
	g.metrics.Prompted.Add(1)
	if approved {
		g.metrics.Overrides.Add(1)
	}
}
