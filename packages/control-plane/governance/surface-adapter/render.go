package surfaceadapter

import (
	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// ActionKind é a acção LÓGICA que uma superfície oferece sobre um card. É
// INDEPENDENTE DA PLATAFORMA — o botão Slack, o botão de teclado Telegram e o botão
// desktop traduzem-se todos para uma destas. Fail-closed: um card degradado NUNCA
// expõe [ActionApprove] (ver [buildActions]).
type ActionKind string

const (
	// ActionApprove — recolher uma aprovação e DEVOLVÊ-LA ao gate. Nunca presente num
	// card degradado (fail-closed).
	ActionApprove ActionKind = "approve"
	// ActionReject — recolher uma rejeição e devolvê-la ao gate.
	ActionReject ActionKind = "reject"
	// ActionForward — ENCAMINHAR o card para uma superfície capaz (o desfecho da
	// degradação fail-closed de um card irreversível num canal sem dual-control).
	ActionForward ActionKind = "forward"
)

// RenderedAction é uma acção lógica oferecida na render, com o rótulo apresentado.
type RenderedAction struct {
	Kind  ActionKind
	Label string
}

// --- Estruturas de apresentação POR PLATAFORMA (dados, não chamadas a APIs) --------
//
// São a "renderização" do reference model: estruturas deterministas que a variante de
// produção traduziria para os payloads reais de Slack/Telegram/desktop. Todas DERIVAM
// da MESMA [approvalcard.ApprovalCard] — não há um segundo modelo por plataforma.

// SlackBlock é um bloco Block-Kit-like (data only).
type SlackBlock struct {
	Type     string         // "section" | "actions"
	Text     string         // texto do bloco section (ex.: o preview)
	Elements []SlackElement // botões de um bloco actions
}

// SlackElement é um elemento interactivo de um bloco actions (data only).
type SlackElement struct {
	Type     string // "button"
	Text     string
	ActionID string
	Style    string // "primary" | "danger" | ""
}

// TelegramKeyboard é um teclado inline (data only).
type TelegramKeyboard struct {
	Text          string             // corpo da mensagem (ex.: o preview)
	InlineButtons [][]TelegramButton // linhas de botões inline
}

// TelegramButton é um botão de teclado inline (data only).
type TelegramButton struct {
	Text         string
	CallbackData string
}

// DesktopComponent é um componente de UI desktop (data only).
type DesktopComponent struct {
	Title   string
	Body    string // corpo (ex.: o preview)
	Buttons []DesktopButton
}

// DesktopButton é um botão de um componente desktop (data only).
type DesktopButton struct {
	Label  string
	Action string
	Danger bool
}

// RenderedCard é a representação renderizada de um approval-card numa superfície. Os
// ESSENCIAIS canónicos (Preview, Class, Irreversible, DualControlRequired, RequestID,
// Requester) são COPIADOS DIRECTAMENTE da [approvalcard.ApprovalCard] — a fonte única
// (AC1): mudar um campo canónico propaga-se a esta render sem um modelo duplicado. Os
// blocos ESPECÍFICOS da plataforma (SlackBlocks/TelegramKeyboard/DesktopComponent) são
// derivados desses mesmos essenciais.
type RenderedCard struct {
	// Platform e ChannelID identificam a superfície e o canal do contrato de AOS-119.
	Platform  Platform
	ChannelID controlsurface.ChannelID

	// Essenciais canónicos, LIDOS do card (AC1/AC2). Preservados em TODAS as superfícies.
	RequestID           string
	Requester           string
	Class               risk.Class
	Irreversible        bool
	DualControlRequired bool
	Preview             string

	// Degraded indica a DEGRADAÇÃO fail-closed (AC4): a superfície não consegue
	// representar a semântica de aprovação (dual-control) do card. Uma render degradada
	// recusa/encaminha e NUNCA oferece [ActionApprove].
	Degraded      bool
	DegradeReason string

	// Actions são as acções LÓGICAS oferecidas (independentes da plataforma).
	Actions []RenderedAction

	// Blocos ESPECÍFICOS da plataforma (exactamente um é populado, conforme Platform).
	SlackBlocks      []SlackBlock
	TelegramKeyboard *TelegramKeyboard
	DesktopComponent *DesktopComponent
}

// Renderer traduz o modelo canónico numa superfície concreta. A implementação de
// referência produz ESTRUTURAS DE DADOS; a de produção liga as APIs reais por trás da
// MESMA interface. Todos os renderers LEEM da mesma [approvalcard.ApprovalCard] — não
// há modelo por plataforma.
type Renderer interface {
	// Platform devolve a superfície deste renderer.
	Platform() Platform
	// Render deriva a [RenderedCard] do card canónico, preservando o Preview e o
	// indicador de dual-control (AC2) e degradando fail-closed quando a plataforma não
	// suporta a capacidade exigida (AC4). Rejeita um card de contrato MAJOR incompatível
	// (AC5, fail-closed).
	Render(card approvalcard.ApprovalCard) (RenderedCard, error)
}

// validateForRender impõe o contrato VERSIONADO (AC5) e a coerência do card ANTES de
// qualquer render. Consome só a API pública do card (Compatible/Validate) — sem
// acoplamento à implementação interna. Fail-closed: um MAJOR incompatível ou um card
// incoerente não renderiza.
func validateForRender(card approvalcard.ApprovalCard) error {
	// Contrato versionado de AOS-120: MESMO MAJOR (ver [approvalcard.CardSchemaVersion.Compatible]).
	if !approvalcard.CurrentVersion.Compatible(card.SchemaVersion) {
		return ErrIncompatibleContract
	}
	// Coerência canónica (request-id/requester não-vazios, dual-control ⇔ irreversível).
	if err := card.Validate(); err != nil {
		return err
	}
	return nil
}

// degrade decide a DEGRADAÇÃO fail-closed (AC4): um card que EXIGE dual-control
// (irreversível) numa superfície que NÃO o suporta degrada — recusa/encaminha para um
// canal capaz. É a ÚNICA regra de degradação, central e partilhada pelos três
// renderers (evita divergência de semântica entre plataformas).
func degrade(card approvalcard.ApprovalCard, caps Capabilities) (bool, string) {
	if card.DualControlRequired && !caps.SupportsDualControl {
		return true, "dual-control exigido (irreversivel) mas a superficie nao o suporta: encaminha para um canal capaz (fail-closed)"
	}
	return false, ""
}

// buildActions deriva as acções LÓGICAS. Fail-closed: uma render degradada oferece
// APENAS recusar/encaminhar — NUNCA [ActionApprove] (nunca aprovar por omissão). Uma
// render capaz oferece aprovar/rejeitar.
func buildActions(degraded bool) []RenderedAction {
	if degraded {
		return []RenderedAction{
			{Kind: ActionForward, Label: "Encaminhar para um canal capaz"},
			{Kind: ActionReject, Label: "Rejeitar"},
		}
	}
	return []RenderedAction{
		{Kind: ActionApprove, Label: "Aprovar"},
		{Kind: ActionReject, Label: "Rejeitar"},
	}
}

// renderBase constrói a PARTE CANÓNICA da render — os essenciais copiados do card, a
// decisão de degradação e as acções lógicas —, PARTILHADA pelos três renderers (a
// prova estrutural da fonte única: os três derivam desta função, que lê só do card).
func renderBase(card approvalcard.ApprovalCard, p Platform) (RenderedCard, error) {
	if err := validateForRender(card); err != nil {
		return RenderedCard{}, err
	}
	caps := PlatformCapabilities(p)
	degraded, reason := degrade(card, caps)
	return RenderedCard{
		Platform:            p,
		ChannelID:           p.ChannelID(),
		RequestID:           card.RequestID,
		Requester:           card.Requester,
		Class:               card.Class,
		Irreversible:        card.Irreversible,
		DualControlRequired: card.DualControlRequired,
		Preview:             card.Preview,
		Degraded:            degraded,
		DegradeReason:       reason,
		Actions:             buildActions(degraded),
	}, nil
}

// actionCallback é o identificador estável de uma acção nos blocos da plataforma. Sem
// segredos — o RequestID é o id de apresentação (não-segredo, ver AOS-120).
func actionCallback(kind ActionKind, requestID string) string {
	return string(kind) + ":" + requestID
}

// RendererFor devolve o renderer da plataforma dada. Fail-closed: uma plataforma
// desconhecida devolve [ErrUnknownPlatform] (não há render por omissão).
func RendererFor(p Platform) (Renderer, error) {
	switch p {
	case PlatformSlack:
		return slackRenderer{}, nil
	case PlatformTelegram:
		return telegramRenderer{}, nil
	case PlatformDesktop:
		return desktopRenderer{}, nil
	default:
		return nil, ErrUnknownPlatform
	}
}
