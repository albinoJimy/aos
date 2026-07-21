package progresssurface

import "errors"

var (
	// ErrNilBudgetReader — Evaluate foi chamado sem uma porta BudgetReader. Sem o
	// orçamento não há denominador para o burn-down — fail-closed (não se assume um
	// orçamento infinito nem se dispara/suprime o prompt às cegas).
	ErrNilBudgetReader = errors.New("progresssurface: BudgetReader em falta (o burn-down lê o orçamento de EPIC-03)")

	// ErrNilBudgetExtender — OptionExtend foi resolvido sem uma porta BudgetExtender. A
	// extensão é DELEGADA ao controlo; sem o extender não há a quem pedir — fail-closed
	// (a superfície não muta o orçamento por si).
	ErrNilBudgetExtender = errors.New("progresssurface: BudgetExtender em falta (a extensão é delegada ao admission control de EPIC-03)")

	// ErrNilDegrader — OnPromptTimeout foi chamado sem uma porta Degrader. A ausência de
	// resposta TEM de degradar (EPIC-03); sem o Degrader a superfície morreria em
	// silêncio — recusa-se fail-closed em vez disso.
	ErrNilDegrader = errors.New("progresssurface: Degrader em falta (a ausência de resposta degrada via EPIC-03, nunca morre em silêncio)")

	// ErrUnknownOption — ResolvePrompt recebeu uma opção fora das três do contrato
	// (extend/summarize_stop/abort). Fail-closed: uma escolha desconhecida não é
	// interpretada à força (o caminho legítimo sem escolha é OnPromptTimeout → Degrader).
	ErrUnknownOption = errors.New("progresssurface: opção de exaustão desconhecida (esperado extend/summarize_stop/abort)")
)
