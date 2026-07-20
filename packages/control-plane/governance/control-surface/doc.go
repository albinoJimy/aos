// Package controlsurface é o CONTRATO UNIFICADO da superfície de controlo HITL
// out-of-band do AOS (AOS-119, EPIC-12, ADR-013).
//
// # O que é (e o que NÃO é)
//
// É uma camada FINA de APRESENTAÇÃO/PROTOCOLO. Define um protocolo estável e
// VERSIONADO (SemVer) — steer, interrupt, resume-com-correcção, query-estado — que
// QUALQUER canal (desktop, chatbot, API) usa para sinalizar o loop do agente, e que
// reflecte de volta o estado durável (running/paused/waiting_on_human) de forma
// consistente a todos os canais.
//
// NÃO reimplementa a máquina de estados, o estado `paused` nem o graceful pause —
// esse MECANISMO DURÁVEL já existe e é completo em kernel/agent-runtime/control
// (AOS-023, EPIC-02, Done). Este módulo COMPÕE-o:
//
//   - [ControlMessage] é o contrato único (um schema versionado, independente do
//     canal) — [messages.go].
//   - [ControlSchemaVersion] versiona-o em SemVer com o domínio
//     [SchemaDomain] = "aos.control.surface.v1" — [version.go].
//   - [ControlSurface] TRADUZ cada mensagem validada para as APIs REAIS de AOS-023:
//     interrupt → [control.SteerChannel.Pause] (graceful pause pendente), steer →
//     [control.SteerChannel.Steer] (correcção autenticada), resume →
//     [control.SteerChannel.Resume] (paused→running via [control.MachineGate]),
//     state → reflexão do estado corrente. NUNCA chama state.Machine.Pause/Resume
//     directamente — vai SEMPRE via SteerChannel para preservar durabilidade,
//     não-repúdio e aceitação-garantida — [surface.go].
//   - [StateProjector] é o read-model por run que subscreve
//     [eventstore.Store.Subscribe] e faz FAN-OUT do estado corrente a todos os
//     canais subscritos — dois canais vêem o MESMO estado (fonte única) —
//     [reflection.go].
//   - Cada acção de controlo emite um span de interacção ligado ao trace do run,
//     reusando o vocabulário aos.control.* — [span.go].
//
// # Três garantias inegociáveis (ADR-013), herdadas de AOS-023
//
//  1. Sinal SEMPRE ACEITE — [control.SteerChannel.Pause] marca a pausa PENDENTE
//     mesmo a meio de um turno; o sinal nunca é descartado.
//  2. Pausa GRACIOSA — a transição running→paused só se materializa no FIM do turno
//     via [control.SteerChannel.GracefulPause]; este módulo NÃO a implementa,
//     delega no runtime.
//  3. TUDO GRAVÁVEL — o sinal e a correcção viram eventos append-only no Event
//     Store (não-repúdio); o estado durável é reconstruível por replay.
//
// # Separação control/data-plane (out-of-band)
//
// O canal de controlo é DISTINTO do canal de dados/conteúdo. Uma correcção de
// utilizador entra AUTENTICADA por [control.SteerChannel.Steer]; a
// [control.Correction] resultante é SEMPRE [agentruntime.TaintTrusted] (origem
// [taint.OriginAuthenticatedUser]). Conteúdo untrusted (resultado de tool / web)
// não carrega uma assinatura de emissor válida — o autenticador rejeita-o
// ([control.ErrUnauthenticated]) e ele NUNCA se torna um sinal nem uma instrução no
// data-plane.
package controlsurface
