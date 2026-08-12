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

	// ErrNilBurndownSource — EvaluateRun foi chamado sem uma [BurndownSource] ligada
	// ([WithBurndownSource]). É o CRITÉRIO DURO de AOS-261: sem fonte a leitura devolve
	// ERRO, nunca um burn-down de 0% — que seria indistinguível de «o run não gastou
	// nada» e faria o aviso de exaustão calar-se para sempre.
	ErrNilBurndownSource = errors.New("progresssurface: BurndownSource em falta (sem fonte NAO ha burn-down — 0% seria uma leitura falsa, nao uma leitura vazia)")

	// ErrNoBurndownSpans — Evaluate (a via por SPANS, anterior a AOS-261) foi chamado com
	// uma fatia NIL. Nenhum nó produz/retém spans (NoopTracer por omissão; o SpanTracer
	// dispara-e-esquece), pelo que `nil` é o sintoma de «não há fonte», não de «este run
	// ainda não gastou». Fail-closed pela mesma razão de [ErrNilBurndownSource]. Uma
	// fatia VAZIA mas não-nil continua a ser uma leitura legítima (o chamador afirma ter
	// olhado e não haver spans); a via com contrato de ausência explícito é
	// [ProgressSurface.EvaluateRun].
	ErrNoBurndownSpans = errors.New("progresssurface: Evaluate sem spans (fatia nil) — nenhum tracer os produz/retem; use EvaluateRun com uma BurndownSource (AOS-261)")

	// ErrUnknownOption — ResolvePrompt recebeu uma opção fora das três do contrato
	// (extend/summarize_stop/abort). Fail-closed: uma escolha desconhecida não é
	// interpretada à força (o caminho legítimo sem escolha é OnPromptTimeout → Degrader).
	ErrUnknownOption = errors.New("progresssurface: opção de exaustão desconhecida (esperado extend/summarize_stop/abort)")
)
