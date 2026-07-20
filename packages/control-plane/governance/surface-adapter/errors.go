package surfaceadapter

import "errors"

// Sentinelas fail-closed do adaptador. Toda a renderização/autorização recusa por
// omissão: um card de contrato MAJOR incompatível, um card incoerente, uma plataforma
// desconhecida ou uma dependência ausente NUNCA produzem uma render/decisão
// silenciosamente permissiva.
var (
	// ErrIncompatibleContract — o card carimba um MAJOR incompatível com a versão
	// corrente do contrato de AOS-120 ([approvalcard.CurrentVersion]). Quebra de
	// contrato, rejeitada (fail-closed) em [Renderer.Render]. O adaptador consome o
	// contrato VERSIONADO (AC5) sem se acoplar à implementação interna do card.
	ErrIncompatibleContract = errors.New("surfaceadapter: contrato do card MAJOR incompativel (rejeitado, fail-closed)")

	// ErrUnknownPlatform — pedido de renderer para uma plataforma não suportada. Uma
	// superfície desconhecida não é representável: fail-closed em [RendererFor].
	ErrUnknownPlatform = errors.New("surfaceadapter: plataforma desconhecida (fail-closed)")

	// ErrNilDependency — o autorizador de superfície foi construído sem renderer ou sem
	// [approvalcard.DualControlCollector]. Sem forma de renderizar/devolver a decisão ao
	// gate, não há autorização possível — fail-closed na construção.
	ErrNilDependency = errors.New("surfaceadapter: dependencia ausente (renderer/collector, fail-closed)")
)
