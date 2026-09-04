package hitl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// Presentation é o pedido de confirmação APRESENTADO ao aprovador pelo [Channel]
// (out-of-band). Carrega o preview do efeito CONCRETO resolvido, a classe, o
// solicitante (para o 4-eyes) e um RequestID único que a decisão assinada tem de
// referenciar (liga a assinatura a ESTE pedido — anti-replay). Sem segredos: nunca o
// input da tool nem chaves.
type Presentation struct {
	// RequestID identifica unicamente este pedido; a [SignedApproval] tem de o repetir.
	RequestID string
	// Requester é o principal cuja acção está a ser gatada (o solicitante). Base do
	// dual-control 4-eyes: para danger, o aprovador tem de ser DISTINTO deste.
	Requester string
	// Class é a classe de risco que motivou a confirmação.
	Class risk.Class
	// Batch indica um pedido de LOTE gray (uma confirmação para o grupo).
	Batch bool
	// Irreversible marca a acção como não-desfazível (timeout fail-closed não-desactivável).
	Irreversible bool
	// Preview é o efeito CONCRETO resolvido apresentado ao humano.
	Preview string
	// Capability e Resource identificam a acção (atribuição no audit).
	Capability string
	Resource   string
}

// ApprovalSource é a PORTA out-of-band por onde o [Channel] apresenta um pedido e
// AGUARDA a decisão ASSINADA do aprovador. É a fronteira que torna o gate testável e
// desacoplado do transporte real (UI, fila, webhook): Await DEVE respeitar o ctx — um
// ctx expirado/cancelado (o gate impõe a deadline fail-closed) tem de devolver
// prontamente, e o [Channel] traduz isso em DENY. A implementação de referência
// (testes) usa um canal/relógio injectáveis; produção liga o transporte real.
type ApprovalSource interface {
	Await(ctx context.Context, p Presentation) (SignedApproval, error)
}

// Channel é o gate HITL concreto: implementa [risk.ConfirmationChannel]. Compõe, por
// porta, o [ApproverRegistry] (aprovadores + chaves pinadas + autoridade), a
// [ApprovalSource] (decisão assinada out-of-band), o [audit.Store] (selar a decisão
// assinada), a [TieringPolicy] (policy-as-code SA-ROC), o [Tracer] e o [MetricSink]
// (override-rate). Seguro para concorrência na medida em que os colaboradores o forem
// (o [audit.MemStore] e os contadores atómicos são concurrency-safe). Construir com
// [NewChannel].
type Channel struct {
	registry  ApproverRegistry
	source    ApprovalSource
	sealer    audit.Store
	tiering   TieringPolicy
	tracer    Tracer
	sink      MetricSink
	metrics   Metrics
	now       func() time.Time
	newID     func() string
	partition func(requester string) string
	threshold float64
}

// ChannelOption configura o [Channel].
type ChannelOption func(*Channel)

// WithTiering substitui a política de tiering SA-ROC (policy-as-code). Por omissão
// [DefaultTieringPolicy].
func WithTiering(p TieringPolicy) ChannelOption { return func(c *Channel) { c.tiering = p } }

// WithTracer injecta a porta de observabilidade (default [NoopTracer]).
func WithTracer(t Tracer) ChannelOption {
	return func(c *Channel) {
		if t != nil {
			c.tracer = t
		}
	}
}

// WithMetricSink injecta a porta de métricas do override-rate (default [NoopSink]).
func WithMetricSink(s MetricSink) ChannelOption {
	return func(c *Channel) {
		if s != nil {
			c.sink = s
		}
	}
}

// WithClock injecta o relógio (default [time.Now]) usado no timestamp observacional
// do selo de audit. Uso interno/testes deterministas. A DECISÃO de timeout não
// depende deste relógio — depende do ctx que o gate de risco impõe.
func WithClock(f func() time.Time) ChannelOption {
	return func(c *Channel) {
		if f != nil {
			c.now = f
		}
	}
}

// WithIDSource injecta o gerador de RequestID (default: 16 bytes crypto/rand em hex).
// Permite testes deterministas.
func WithIDSource(f func() string) ChannelOption {
	return func(c *Channel) {
		if f != nil {
			c.newID = f
		}
	}
}

// WithPartitioner define como derivar a partição de audit a partir do solicitante.
// Default: "hitl:<requester>" (uma cadeia de aprovações por run/solicitante).
func WithPartitioner(f func(requester string) string) ChannelOption {
	return func(c *Channel) {
		if f != nil {
			c.partition = f
		}
	}
}

// WithOverrideRateThreshold ajusta o limiar anti-rubber-stamping (default
// [DefaultOverrideRateThreshold] = 0.40). Um valor <= 0 ou >= 1 mantém o default.
func WithOverrideRateThreshold(t float64) ChannelOption {
	return func(c *Channel) {
		if t > 0 && t < 1 {
			c.threshold = t
		}
	}
}

// NewChannel constrói o gate HITL. registry (chaves pinadas + autoridade), source
// (decisão assinada out-of-band) e sealer (audit WORM) são OBRIGATÓRIOS — a sua
// ausência é fail-closed ([ErrNilDeps]). Por omissão usa [DefaultTieringPolicy],
// [NoopTracer], [NoopSink], [time.Now], id crypto/rand e o limiar 0.40.
func NewChannel(registry ApproverRegistry, source ApprovalSource, sealer audit.Store, opts ...ChannelOption) (*Channel, error) {
	if registry == nil || source == nil || sealer == nil {
		return nil, ErrNilDeps
	}
	c := &Channel{
		registry:  registry,
		source:    source,
		sealer:    sealer,
		tiering:   DefaultTieringPolicy(),
		tracer:    NoopTracer{},
		sink:      NoopSink{},
		now:       time.Now,
		newID:     randomID,
		partition: func(requester string) string { return "hitl:" + requester },
		threshold: DefaultOverrideRateThreshold,
	}
	for _, o := range opts {
		o(c)
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.newID == nil {
		c.newID = randomID
	}
	return c, nil
}

// Metrics devolve o ponteiro para os contadores do gate (inclui o override-rate).
func (c *Channel) Metrics() *Metrics { return &c.metrics }

// TieringPolicyVersion devolve o identificador tamper-evident da política de tiering
// em vigor (selado como PolicyVersion nas decisões).
func (c *Channel) TieringPolicyVersion() string { return c.tiering.Version() }

// Confirm implementa [risk.ConfirmationChannel]: materializa o gate HITL concreto de
// AOS-095. O FLUXO (ADR-013):
//
//	tiering (safe não é gatável → deny) → apresenta o pedido com o preview →
//	aguarda a decisão ASSINADA OU o timeout (ctx do gate) → resolve o aprovador e a
//	sua autoridade (desconhecido/sem autoridade → deny) → VERIFICA a assinatura
//	(forjada/inválida → deny) → 4-eyes para danger (aprovador == solicitante → deny)
//	→ SELA a decisão assinada no audit → actualiza métricas (Prompted/Overrides/
//	Timeouts) e expõe o override-rate → devolve a [risk.ConfirmationResponse].
//
// Para acções IRREVERSÍVEIS o timeout/silêncio → DENY (Timeouts++): a ausência de
// aprovação NEGA, nunca permite. Toda a decisão terminal (aprovação, recusa assinada,
// timeout, rejeição) é SELADA no audit tamper-evident.
func (c *Channel) Confirm(ctx context.Context, req risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	ctx, span := c.tracer.StartSpan(ctx, OpApprovalConfirm)
	defer span.End()
	mode := c.tiering.ModeFor(req.Class)
	span.SetAttribute(AttrClass, req.Class.String())
	span.SetAttribute(AttrMode, mode.String())
	span.SetAttribute(AttrApprover, req.Principal)
	span.SetAttribute(AttrIrreversible, req.Irreversible)

	pres := Presentation{
		RequestID:    c.newID(),
		Requester:    req.Principal,
		Class:        req.Class,
		Batch:        req.Batch,
		Irreversible: req.Irreversible,
		Preview:      req.Preview,
		Capability:   req.Capability,
		Resource:     req.Resource,
	}

	// Tiering fail-closed: uma acção SAFE não é gatável (corre sem gate no
	// [risk.Gate]). Se chega aqui é um erro de wiring — nega, nunca corre por engano.
	if mode == ModeRun {
		return c.finish(ctx, span, pres, SignedApproval{}, false, ReasonNotGated, false, false)
	}

	// Se o ctx já expirou antes de apresentar, não apresenta — nega fail-closed.
	if ctx.Err() != nil {
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		return c.finish(ctx, span, pres, SignedApproval{}, false, ReasonTimeout, false, timedOut)
	}

	// Apresenta o pedido e AGUARDA a decisão assinada ou o timeout do gate.
	appr, err := c.source.Await(ctx, pres)
	if err != nil || ctx.Err() != nil {
		// Timeout / cancelamento / falha da fonte ⇒ DENY fail-closed. A ausência de
		// aprovação NEGA. Distingue-se o timeout (deadline) para contabilização.
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)
		return c.finish(ctx, span, pres, SignedApproval{}, false, ReasonTimeout, false, timedOut)
	}

	// Resolve o aprovador clamado: chave pública pinada + autoridade autoritativa.
	pub, authority, ok, lerr := c.registry.Lookup(ctx, appr.Approver)
	if lerr != nil || !ok || len(pub) != ed25519.PublicKeySize {
		return c.finish(ctx, span, pres, appr, false, ReasonUnknownApprover, false, false)
	}

	// AUTORIDADE (AC2): a autoridade autoritativa do aprovador tem de cobrir a classe.
	// Um principal autêntico mas SEM autoridade para a classe é recusado (fail-closed).
	if !contains(authority, RequiredAuthority(req.Class)) {
		return c.finish(ctx, span, pres, appr, false, ReasonAuthorityNotCovered, false, false)
	}

	// Forma + ligação ao pedido: a decisão tem de referenciar ESTE request-id e trazer
	// nonce/timestamp/assinatura válidos (anti-replay de uma aprovação de outro pedido).
	if appr.RequestID != pres.RequestID || len(appr.Nonce) < approvalNonceMinLen ||
		appr.IssuedAt.IsZero() || len(appr.Signature) != ed25519.SignatureSize {
		return c.finish(ctx, span, pres, appr, false, ReasonInvalidApproval, false, false)
	}

	// ASSINATURA (AC4): tem de validar contra a chave PINADA do aprovador. Forjada/
	// adulterada ⇒ recusada (não-repúdio).
	if !verifyApproval(pub, appr) {
		return c.finish(ctx, span, pres, appr, false, ReasonForgedSignature, false, false)
	}

	// DUAL-CONTROL 4-eyes (AC6): para uma acção INTRINSECAMENTE danger ou irreversível,
	// o aprovador tem de ser DISTINTO do solicitante. Um auto-approve é recusado. Gray
	// (lote) não exige 4-eyes. AMARRA-SE AO RISCO INTRÍNSECO (Class/Irreversible), NÃO
	// ao Mode do tiering: uma política mal-configurada que mapeasse danger para um modo
	// mais leve não pode contornar o dual-control de uma acção não-desfazível. A
	// assinatura JÁ é válida — a tentativa é atribuível ao aprovador (verified=true):
	// sela-se na sua cadeia com a assinatura, para forense de quem tentou o
	// self-approval, mas a decisão é DENY.
	if (pres.Class == risk.ClassDanger || pres.Irreversible) && appr.Approver == pres.Requester {
		return c.finish(ctx, span, pres, appr, false, ReasonDualControl, true, false)
	}

	// Decisão assinada, verificada, autorizada e (para danger) 4-eyes: honra-a.
	reason := ReasonApproved
	if !appr.Approved {
		reason = ReasonRefused
	}
	return c.finish(ctx, span, pres, appr, appr.Approved, reason, true, false)
}

// finish é o ponto ÚNICO de saída: actualiza as métricas, sela a decisão no audit
// tamper-evident, emite o sinal de override-rate e devolve a resposta. verified
// indica que a decisão foi assinada+verificada por um aprovador autorizado (o único
// caso em que o selo é atribuível ao aprovador REAL e carrega a assinatura de
// não-repúdio). timedOut marca o subconjunto negado por deadline.
func (c *Channel) finish(ctx context.Context, span Span, pres Presentation, appr SignedApproval, approved bool, reason string, verified, timedOut bool) (risk.ConfirmationResponse, error) {
	// FAIL-CLOSED DO EFEITO — verificado AQUI, no caminho da DECISÃO, e não no sink de
	// auditoria. Uma aprovação assinada só autoriza o efeito com o prazo do gate ainda
	// vivo: se o ctx morreu entre a verificação da assinatura (Confirm) e este ponto, a
	// acção deixa de estar autorizada a correr — a ausência de aprovação DENTRO do prazo
	// nega, que é a propriedade AC3.
	//
	// Isto ESTAVA a ser garantido por acidente: com o AOS-311, o Append recusava sob ctx
	// morto e o `!sealed` abaixo forçava o deny. Depender do sink para isto misturava duas
	// coisas distintas — o prazo do EFEITO e a durabilidade da PROVA — e custava a segunda
	// (ver [Channel.seal]). Aqui a condição é explícita e independente do store: o gate
	// continua fail-closed por prazo mesmo com um sink que ignore o ctx.
	//
	// `verified` NÃO é rebaixado: a decisão FOI assinada e verificada, e o selo tem de
	// continuar a carregar a obrigação `hitl_signature` — sem ela perde-se o não-repúdio
	// de quem aprovou uma acção que o prazo depois negou.
	if approved && ctx.Err() != nil {
		approved = false
		reason = ReasonTimeout
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	}

	// Métricas: toda a passagem por Confirm (gray/danger) contou como um PROMPT — o
	// denominador do override-rate, incluindo timeouts (molde [risk.Gate]).
	c.metrics.Prompted.Add(1)
	if approved {
		c.metrics.Overrides.Add(1)
	} else {
		c.metrics.Denials.Add(1)
		if timedOut {
			c.metrics.Timeouts.Add(1)
		}
	}

	// Sela a decisão (audit-before-return). Uma decisão não-selável NUNCA vira
	// aceitação: se a selagem falhar POR NÃO SER DURÁVEL (disco cheio, erro de E/S,
	// partição alheia), força-se o deny — fail-closed que continua a valer. O que já não
	// conta como "não-selável" é o cancelamento do chamador: ver [Channel.seal].
	sealed := c.seal(ctx, pres, appr, approved, reason, verified, timedOut)
	if !sealed {
		approved = false
	}

	// Override-rate exposto como sinal OTel (AC5) + limiar anti-rubber-stamping.
	prompted, overrides, _, _, rate := c.metrics.Snapshot()
	span.SetAttribute(AttrReason, reason)
	span.SetAttribute(AttrDecision, decisionString(approved))
	span.SetAttribute(AttrDualControl, verified && pres.Class == risk.ClassDanger)
	span.SetAttribute(AttrOverrideRate, rate)
	c.sink.RecordOverrideRate(ctx, rate, prompted, overrides)
	if c.metrics.Exceeds(c.threshold) {
		span.SetAttribute("aos.hitl.override_rate_alarm", true)
		c.sink.SignalHighOverrideRate(ctx, rate, c.threshold)
	}

	approver := ""
	if verified {
		approver = appr.Approver
	}
	return risk.ConfirmationResponse{Approved: approved, Approver: approver}, nil
}

// seal grava a decisão HITL na cadeia de audit WORM tamper-evident. Devolve false se
// a selagem falhar (o chamador força o deny — audit-before-effect).
//
// O CTX DO CHAMADOR NÃO CANCELA ESTE SELO (achado de revisão adversarial sobre AOS-311).
// Todo o caminho que chega aqui passa por [Channel.finish], e [Channel.finish] só é
// chamado com uma decisão TERMINAL já tomada — aprovada, recusada, negada por timeout,
// por autoridade ou por 4-eyes. Não existe neste ficheiro um Append que PARTICIPE na
// decisão: quem decide são as guardas de `ctx.Err()` em [Channel.Confirm] (:216, :223) e
// a guarda de prazo no topo de [Channel.finish]. Este Append regista um facto CONSUMADO.
//
// Porque isto importa. O caminho de TIMEOUT chega aqui com o ctx morto POR CONSTRUÇÃO —
// é o prazo esgotado que produz a decisão. Desde que o AOS-311 pôs o `audit.Store.Append`
// a respeitar o ctx, esse Append passou a falhar sempre nesse caminho e o erro morre no
// `err == nil` abaixo: a negação fail-closed continuava a acontecer, mas deixava de ser
// ESCRITA. Um auditor que pergunte «porque é que esta acção foi negada» não encontrava
// registo nenhum, e uma aprovação assinada apanhada pelo prazo perdia a obrigação
// `hitl_signature` — a base do não-repúdio. Perda de rasto silenciosa.
//
// `WithoutCancel` preserva os valores do ctx (correlação/tracing) e larga só o
// cancelamento; o prazo próprio cobre o caso de o store estar pendurado. É o idioma já
// usado em `packages/integration/budget.go` e em `packages/substrate/sandbox/lifecycle.go`
// para exactamente esta situação.
//
// ATRIBUIÇÃO (molde messaging): uma decisão VERIFICADA (assinada por aprovador
// autorizado) é atribuível ao aprovador REAL — Principal=aprovador, na partição do
// solicitante — e carrega a ASSINATURA (não-repúdio, re-verificável a partir do
// audit). Uma decisão NÃO verificada (timeout, aprovador desconhecido/sem autoridade,
// assinatura forjada) NÃO se atribui a nenhum aprovador autenticado: sela-se sem
// principal, com o aprovador CLAMADO registado como claim para forense. NUNCA sela o
// preview/payload — só metadados de responsabilização e a assinatura (que é pública).
func (c *Channel) seal(ctx context.Context, pres Presentation, appr SignedApproval, approved bool, reason string, verified, timedOut bool) bool {
	decision := audit.DecisionDeny
	if approved {
		decision = audit.DecisionAllow
	}
	obs := []audit.Obligation{{
		Type: "hitl_decision",
		Params: map[string]string{
			"reason":       reason,
			"request_id":   pres.RequestID,
			"class":        pres.Class.String(),
			"mode":         c.tiering.ModeFor(pres.Class).String(),
			"irreversible": boolStr(pres.Irreversible),
			"timed_out":    boolStr(timedOut),
		},
	}}

	rec := audit.AuditRecord{
		Timestamp:     c.now(),
		Decision:      decision,
		Capability:    pres.Capability,
		PolicyVersion: c.tiering.Version(),
		ToolID:        "governance.hitl",
		RequestID:     pres.RequestID,
		Resource:      audit.Resource{Type: "action", Value: pres.Resource},
	}

	if verified {
		// Atribuível ao aprovador REAL; carrega a assinatura de não-repúdio, o nonce e
		// o timestamp para re-verificação a partir da cadeia.
		rec.Partition = c.partition(pres.Requester)
		rec.Principal = audit.Principal{NHIID: appr.Approver}
		obs = append(obs, audit.Obligation{
			Type: "hitl_signature",
			Params: map[string]string{
				"approver":  appr.Approver,
				"requester": pres.Requester,
				// A decisão TAL COMO ASSINADA pelo aprovador (base do não-repúdio e da
				// re-verificação a partir da cadeia). Pode divergir do Decision do registo
				// quando a política nega uma decisão assinada (ex.: self-approval 4-eyes).
				"approved":   boolStr(appr.Approved),
				"nonce":      base64.StdEncoding.EncodeToString(appr.Nonce),
				"issued_at":  appr.IssuedAt.UTC().Format(time.RFC3339Nano),
				"signature":  base64.StdEncoding.EncodeToString(appr.Signature),
				"sig_domain": approvalDomain,
				"dual_ctrl":  boolStr(pres.Class == risk.ClassDanger),
			},
		})
	} else {
		// Quarentena: sem principal autenticado; o aprovador clamado é apenas um claim.
		rec.Partition = partitionUnauth
		claimed := appr.Approver
		obs = append(obs, audit.Obligation{
			Type: "hitl_unauthenticated",
			Params: map[string]string{
				"claimed_approver": claimed,
				"requester":        pres.Requester,
				"authenticated":    "false",
			},
		})
	}
	rec.Obligations = obs

	selCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalSealTimeout)
	defer cancel()
	_, err := c.sealer.Append(selCtx, rec)
	return err == nil
}

// terminalSealTimeout é o prazo PRÓPRIO dos selos de decisão terminal deste pacote
// ([Channel.seal] e [RatificationGate.seal]). Existe porque esses selos deixaram de
// herdar o cancelamento do chamador: sem prazo nenhum, um store pendurado prenderia o
// gate para sempre. Generoso face a um fsync local e curto face a uma sessão humana.
const terminalSealTimeout = 5 * time.Second

// partitionUnauth é a partição de audit de QUARENTENA das decisões cuja origem NÃO
// está autenticada (timeout, aprovador desconhecido/sem autoridade, assinatura
// forjada). Concentrá-las numa cadeia própria — com o aprovador clamado como claim,
// não como principal — impede que um atacante polua a cadeia de aprovações de um run
// com floods "em nome" de um aprovador (molde messaging).
const partitionUnauth = "hitl-unauth"

// randomID devolve um RequestID único (16 bytes crypto/rand em hex). Fail-safe: em
// caso improvável de falha do RNG, cai para um id baseado no relógio monotónico via
// time — mas mantém unicidade suficiente para a ligação assinatura↔pedido.
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "hitl-" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func decisionString(approved bool) string {
	if approved {
		return "allow"
	}
	return "deny"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
