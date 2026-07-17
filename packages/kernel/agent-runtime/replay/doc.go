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
// # Content-capture mode 3: payloads em storage externo com IAM próprio (AOS-079)
//
// Por omissão o payload não-determinístico de um turno é gravado INLINE no evento
// "replay.captured". A opção [WithPayloadStore] activa o content-capture "mode 3"
// (OTel): o payload COMPLETO migra para um [PayloadStore] EXTERNO sob o seu IAM
// próprio e o evento do Event Store passa a carregar APENAS uma [PayloadRef] opaca e
// verificável por hash — pequeno e fora do caminho quente. O escritor do Event Store
// (escopo de escrita) é SEPARADO do leitor de replay (escopo de leitura): um accessor
// não autorizado é negado fail-closed pelo store. O motor resolve as referências via
// [WithPayloadResolver], impondo o accessor autorizado. A minimização/redação
// (ADR-011) é aplicada ANTES da fronteira do store — a PII nunca sai em claro, nem
// para o Event Store nem para o payload store. A impl [InMemoryPayloadStore] modela o
// IAM zero-dep/offline; o adapter real (S3 + política IAM) é diferido para deployment.
//
// # Gate de admissão: fidelidade é condição, não opção (AOS-079)
//
// Antes de reproduzir, o motor corre um GATE DE ADMISSÃO ([ErrIncompleteCapture]) que
// verifica que TODOS os turnos têm captura de não-determinismo completa e um manifesto
// utilizável. Um run completamente capturado é admissível e atinge 100% de passos
// reproduzíveis; um com captura em falta (evento ausente, PayloadRef não resolúvel,
// payload perdido por TTL/crypto-shredding, ou manifesto sem prompt_hash) é
// INADMISSÍVEL — o replay recusa fail-closed em vez de produzir silenciosamente uma
// reprodução de baixa fidelidade.
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
