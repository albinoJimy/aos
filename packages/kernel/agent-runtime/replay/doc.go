// Package replay implementa o replay determinístico resume-from-step do Agent
// Runtime (AOS-016) — o motor que reconstrói uma trajectória a partir do Event
// Store reproduzindo exactamente as mesmas transições, SEM re-executar o modelo
// nem qualquer efeito externo, e validando por hash de prompt por turno.
//
// # As duas metades: captura + replay
//
// O replay fiel exige que TODOS os inputs não-determinísticos de uma trajectória
// tenham sido capturados na execução original. O loop base (AOS-013) grava o
// evento "turn.recorded" com o MANIFESTO (prompt_hash, model-id/params/seed,
// versões pinadas) mas NÃO os inputs crus. Este pacote fecha esse gap em duas
// peças complementares:
//
//   - [EventStoreCapturer] (nondeterminism_capture.go) — implementa o ponto de
//     ligação [agentruntime.Capturer] e persiste, por turno, um evento
//     "replay.captured" com a resposta do modelo COMPLETA (texto + tool calls +
//     uso + custo + final), o output de cada tool call e o relógio de captura. É
//     ADITIVO: sem ele o loop de AOS-013 é byte-idêntico.
//   - [ReplayEngine] (engine.go) — lê o stream do run do Event Store, re-materializa
//     o prompt de cada turno com o MESMO [agentruntime.PromptAssembler], compara o
//     prompt_hash re-materializado com o gravado no manifesto e detecta divergência
//     com a LOCALIZAÇÃO do passo exacto. Suporta arranque de qualquer step_id
//     (resume-from-step).
//
// # Fonte dos inputs não-determinísticos (nunca ao vivo)
//
//   - Resposta do modelo (texto + tool calls): lida do evento "replay.captured".
//   - Resultado de cada tool call: lido do evento "replay.captured".
//   - Seed: lido do manifesto por trajectória (turn.recorded → model.seed).
//   - Relógio: lido do carimbo de captura (replay.captured → observed_at).
//
// Os inputs DETERMINÍSTICOS (system prompt, tool set congelado, objectivo,
// memory_context) são re-fornecidos pelo chamador via [TrajectorySpec] — são
// código/configuração, re-materializados pelo assembler. Se um deles tiver
// EVOLUÍDO desde a execução original (o risco "replay infiel após evolução de
// código"), o prompt_hash re-materializado diverge do gravado e o motor localiza
// o passo exacto onde isso acontece.
//
// # Zero efeitos externos (garantia estrutural)
//
// O [ReplayEngine] detém APENAS um leitor do Event Store ([EventReader], só Read).
// NÃO tem [agentruntime.ModelClient], NÃO tem Reference Monitor, NÃO tem registo de
// tools nem qualquer Append. Não existe, por construção, caminho para um efeito
// externo em modo replay: o cliente de modelo de replay devolve a resposta
// REGISTADA e o dispatcher de replay devolve o resultado REGISTADO da tool.
//
// # Observabilidade (ADR-010)
//
// O motor emite um marcador de replay/eval via [agentruntime.Tracer] (span
// "replay" com atributos aos.replay.* e gen_ai.evaluation.result) ligado ao trace
// original por aos.run_id — base do eval-driven development e do RCA (tecnica/08).
package replay
