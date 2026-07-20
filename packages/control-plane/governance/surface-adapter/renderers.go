package surfaceadapter

import (
	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
)

// Os três renderers DERIVAM da MESMA [renderBase] (a fonte única) e só ACRESCENTAM os
// blocos específicos da plataforma. Todos preservam o Preview e o indicador de
// dual-control (AC2); nenhum reintroduz lógica de risco. Numa render DEGRADADA os
// controlos oferecem recusar/encaminhar — NUNCA um botão de auto-aprovar (AC4).

// dualControlNote é a nota de apresentação, comum às três plataformas, que torna o
// indicador de dual-control VISÍVEL ao aprovador (paridade da semântica, AC2).
func dualControlNote(rc RenderedCard) string {
	if rc.Degraded {
		return "Requer dois aprovadores distintos — nao representavel aqui: encaminhar para um canal capaz."
	}
	if rc.DualControlRequired {
		return "Requer dois aprovadores distintos (irreversivel)."
	}
	return "Requer uma aprovacao."
}

// --- Slack -------------------------------------------------------------------------

type slackRenderer struct{}

func (slackRenderer) Platform() Platform { return PlatformSlack }

func (r slackRenderer) Render(card approvalcard.ApprovalCard) (RenderedCard, error) {
	rc, err := renderBase(card, PlatformSlack)
	if err != nil {
		return RenderedCard{}, err
	}
	// Bloco section com o PREVIEW (o efeito concreto, já redigido em AOS-120) e a nota
	// de dual-control. Deriva dos essenciais canónicos.
	blocks := []SlackBlock{
		{Type: "section", Text: rc.Preview},
		{Type: "section", Text: dualControlNote(rc)},
	}
	// Bloco actions: um botão por acção lógica (nunca "aprovar" numa render degradada).
	elements := make([]SlackElement, 0, len(rc.Actions))
	for _, a := range rc.Actions {
		elements = append(elements, SlackElement{
			Type:     "button",
			Text:     a.Label,
			ActionID: actionCallback(a.Kind, rc.RequestID),
			Style:    slackStyle(a.Kind),
		})
	}
	blocks = append(blocks, SlackBlock{Type: "actions", Elements: elements})
	rc.SlackBlocks = blocks
	return rc, nil
}

func slackStyle(kind ActionKind) string {
	switch kind {
	case ActionApprove:
		return "primary"
	case ActionReject, ActionForward:
		return "danger"
	default:
		return ""
	}
}

// --- Telegram ----------------------------------------------------------------------

type telegramRenderer struct{}

func (telegramRenderer) Platform() Platform { return PlatformTelegram }

func (r telegramRenderer) Render(card approvalcard.ApprovalCard) (RenderedCard, error) {
	rc, err := renderBase(card, PlatformTelegram)
	if err != nil {
		return RenderedCard{}, err
	}
	// Corpo da mensagem: o PREVIEW + a nota de dual-control (preservados mesmo sem
	// formatação rica — AC2).
	kb := &TelegramKeyboard{Text: rc.Preview + "\n" + dualControlNote(rc)}
	// Uma linha de botões inline por acção lógica (nunca "aprovar" numa render degradada).
	row := make([]TelegramButton, 0, len(rc.Actions))
	for _, a := range rc.Actions {
		row = append(row, TelegramButton{Text: a.Label, CallbackData: actionCallback(a.Kind, rc.RequestID)})
	}
	kb.InlineButtons = [][]TelegramButton{row}
	rc.TelegramKeyboard = kb
	return rc, nil
}

// --- Desktop -----------------------------------------------------------------------

type desktopRenderer struct{}

func (desktopRenderer) Platform() Platform { return PlatformDesktop }

func (r desktopRenderer) Render(card approvalcard.ApprovalCard) (RenderedCard, error) {
	rc, err := renderBase(card, PlatformDesktop)
	if err != nil {
		return RenderedCard{}, err
	}
	comp := &DesktopComponent{
		Title: "Aprovacao pendente — " + rc.Class.String(),
		// Corpo: o PREVIEW + a nota de dual-control (derivados dos essenciais canónicos).
		Body: rc.Preview + "\n" + dualControlNote(rc),
	}
	for _, a := range rc.Actions {
		comp.Buttons = append(comp.Buttons, DesktopButton{
			Label:  a.Label,
			Action: actionCallback(a.Kind, rc.RequestID),
			Danger: a.Kind != ActionApprove,
		})
	}
	rc.DesktopComponent = comp
	return rc, nil
}
