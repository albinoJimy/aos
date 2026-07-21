// Package uxdx é a BATERIA DE TESTES DE UX/DX dos gates de governação de AOS-128
// (EPIC-12, o que FECHA o epic).
//
// A UX de governação não é acessória: um gate confuso produz APPROVAL FATIGUE e
// click-through — utilizadores que auto-aprovam a maioria dos pedidos, anulando a
// supervisão. A eficácia mede-se, e a métrica-chave JÁ existe: o OVERRIDE-RATE de
// AOS-095. Esta bateria valida a USABILIDADE DOS GATES e usa o override-rate como
// sinal ANTI-FADIGA — CONSOME a métrica, NÃO cria enforcement.
//
// # Composição, não reinvenção
//
// TODAS as superfícies já existem e são COMPOSTAS aqui — nada é reimplementado:
//
//   - approval-card (AOS-120) — [approvalcard.BuildCard]/[approvalcard.ApprovalCard]
//     (preview do efeito concreto) + [approvalcard.DualControlCollector] (dois
//     aprovadores distintos, fail-closed). Usabilidade: preview completo, dual-control
//     inequívoco, self/único rejeitado.
//   - plan-approval (AOS-121) — [planapproval.BuildPlanCard]/[planapproval.PlanGate]
//     (grafo antes do spawn) + [planapproval.SpawnGuard] (ErrPlanNotApproved).
//     Usabilidade: plano completo, verdicts inequívocos, nada spawna sem aprovação.
//   - surface-adapter (AOS-122) — [surfaceadapter.RendererFor] deriva cards
//     equivalentes nas 3 plataformas da MESMA [approvalcard.ApprovalCard]; degrada
//     fail-closed nos canais sem dual-control (Telegram).
//   - progress-surface (AOS-123) — [progresssurface.PromptOptions] (exactamente as 3
//     opções extend/summarize-stop/abort) + [progresssurface.ProgressSurface.OnPromptTimeout]
//     (a ausência de resposta degrada, nunca morre em silêncio).
//   - autonomy-surface (AOS-125) — [autonomysurface.Surface.BuildLevelView] (nível
//     legível, transições com motivo) + [autonomysurface.Surface.NotifyLevelChange]
//     (demoção imediata).
//
// # Anti-fadiga (override-rate de AOS-095)
//
// A bateria CONSOME o [hitl.Metrics] do gate HITL REAL (AOS-095): compõe um
// [hitl.Channel] assinado, exercita-o e assere que o override-rate é EXPOSTO
// ([hitl.MetricOverrideRate]) ao [hitl.MetricSink] em cada decisão e que um
// override-rate cronicamente alto (> [hitl.DefaultOverrideRateThreshold] = 0.40)
// dispara [hitl.MetricSink.SignalHighOverrideRate] — o sinal anti-rubber-stamping.
// NÃO recria a métrica nem o enforcement: só valida que a superfície o expõe.
//
// # Determinismo
//
// Tudo é in-process e determinista: relógios injectados ([hitl.WithClock]/
// [autonomy.WithClock]), seeds de assinatura efémeras (nenhuma chave hard-coded),
// dados sintéticos (sem PII/segredos reais). A suite corre sob -race -count=2 com o
// mesmo veredicto.
//
// O código de teste vive nos ficheiros *_test.go; este ficheiro existe só para
// documentar o pacote (o módulo é uma folha de teste, importado por ninguém).
package uxdx
