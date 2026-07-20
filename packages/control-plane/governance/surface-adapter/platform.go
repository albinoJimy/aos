// Package surfaceadapter é o ADAPTADOR DE PLATAFORMA de AOS-122 (EPIC-12, UX/HITL):
// renderiza o MODELO CANÓNICO do approval-card (AOS-120) em Slack, Telegram e desktop
// com PARIDADE FUNCIONAL — preview do efeito concreto, dual-control para
// irreversíveis, e devolução da decisão ao gate HITL — mantendo UM ÚNICO modelo
// canónico como fonte de verdade.
//
// É PURA TRADUÇÃO DE APRESENTAÇÃO. NENHUMA decisão de risco vive no adaptador:
//   - a classe/irreversibilidade são LIDAS do card (AOS-074/AOS-120), nunca
//     recalculadas;
//   - a decisão é DEVOLVIDA ao gate via [approvalcard.DualControlCollector] (que a
//     entrega ao [risk.ConfirmationChannel] de AOS-095 para assinar/impor) — o
//     adaptador NÃO decide nem assina;
//   - o contrato é consumido VERSIONADO (AOS-119/AOS-120): um MAJOR incompatível é
//     rejeitado (fail-closed).
//
// MODELO OFFLINE (reference model): os blocos Slack, os teclados inline Telegram e os
// componentes desktop são ESTRUTURAS DE DADOS deterministas (a renderização), NÃO
// chamadas a APIs reais de Slack/Telegram — a variante de PRODUÇÃO liga as APIs reais
// por trás da mesma interface [Renderer].
package surfaceadapter

import (
	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
)

// Platform identifica a SUPERFÍCIE onde o utilizador está. É o rótulo de apresentação
// que o adaptador mapeia ao contrato versionado [controlsurface.ChannelID] de AOS-119
// (sem se acoplar à implementação interna do canal).
type Platform string

const (
	// PlatformSlack — superfície Slack (blocos Block-Kit-like, interactivos).
	PlatformSlack Platform = "slack"
	// PlatformTelegram — superfície Telegram (teclado inline, DM de bot).
	PlatformTelegram Platform = "telegram"
	// PlatformDesktop — superfície desktop (componentes ricos nativos).
	PlatformDesktop Platform = "desktop"
)

// Known indica se p é uma das plataformas suportadas. Fail-closed: uma plataforma
// desconhecida não é representável e não deve renderizar (ver [RendererFor]).
func (p Platform) Known() bool {
	switch p {
	case PlatformSlack, PlatformTelegram, PlatformDesktop:
		return true
	default:
		return false
	}
}

// ChannelID mapeia a plataforma ao contrato versionado de AOS-119
// ([controlsurface.ChannelID]): slack/telegram → chatbot, desktop → desktop. É o
// ponto onde o adaptador CONSOME o contrato de canal sem acoplar a implementação (AC5).
// Uma plataforma desconhecida resolve fail-closed para chatbot (o canal mais restrito
// quanto a capacidades neste modelo), nunca para desktop.
func (p Platform) ChannelID() controlsurface.ChannelID {
	switch p {
	case PlatformDesktop:
		return controlsurface.ChannelDesktop
	case PlatformSlack, PlatformTelegram:
		return controlsurface.ChannelChatbot
	default:
		return controlsurface.ChannelChatbot
	}
}

// Capabilities descreve o que uma superfície CONSEGUE representar. É o eixo que
// governa a degradação fail-closed (AC4): uma capacidade em falta (ex.: dual-control
// num canal sem UI de dois-aprovadores) faz o card degradar — recusa/encaminha —,
// nunca aprovar por omissão.
type Capabilities struct {
	// SupportsDualControl indica se a superfície consegue recolher DOIS aprovadores
	// DISTINTOS numa apresentação (o requisito das acções irreversíveis). Uma superfície
	// sem esta capacidade DEGRADA os cards irreversíveis (fail-closed).
	SupportsDualControl bool
	// SupportsButtons indica controlos interactivos (botões/teclados). Sem eles a
	// superfície ainda apresenta o preview, mas a recolha de decisão é textual.
	SupportsButtons bool
	// SupportsRichPreview indica formatação rica do preview (blocos/markdown). Nunca
	// afecta a PRESENÇA do preview — o efeito concreto é SEMPRE preservado (AC2); só a
	// apresentação (rica vs. texto simples) varia.
	SupportsRichPreview bool
}

// PlatformCapabilities devolve as capacidades DETERMINISTAS de cada plataforma
// (reference model). Desktop e Slack representam o dual-control (painéis/Block-Kit com
// dois aprovadores distintos); Telegram, modelado como DM de bot com teclado inline
// ligado a UMA identidade de chat, NÃO consegue recolher dois aprovadores distintos
// numa única apresentação — pelo que DEGRADA os cards irreversíveis (fail-closed). É
// uma decisão do MODELO (a variante de produção pode reconfigurá-la por plataforma),
// não uma afirmação sobre o Telegram real.
func PlatformCapabilities(p Platform) Capabilities {
	switch p {
	case PlatformDesktop:
		return Capabilities{SupportsDualControl: true, SupportsButtons: true, SupportsRichPreview: true}
	case PlatformSlack:
		return Capabilities{SupportsDualControl: true, SupportsButtons: true, SupportsRichPreview: true}
	case PlatformTelegram:
		// Teclado inline (botões) e preview simples, MAS sem dual-control: um card
		// irreversível degrada fail-closed neste canal.
		return Capabilities{SupportsDualControl: false, SupportsButtons: true, SupportsRichPreview: false}
	default:
		// Plataforma desconhecida: nenhuma capacidade (fail-closed).
		return Capabilities{}
	}
}
