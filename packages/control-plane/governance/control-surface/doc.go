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
//   - [StateProjector] é o read-model por run que subscreve
//     [eventstore.Store.Subscribe] e faz FAN-OUT do estado corrente a todos os
//     canais subscritos — dois canais vêem o MESMO estado (fonte única) —
//     [reflection.go].
//
// # O TRADUTOR FOI REMOVIDO (AOS-292 AC4)
//
// Existia aqui um ControlSurface que traduzia cada [ControlMessage] validada para as
// APIs de AOS-023 (interrupt → Pause, steer → Steer, resume → Resume via MachineGate),
// mais o span de interacção que cada acção emitia. NUNCA foi composto em produção:
// construía-se apenas em testes, enquanto o nó chamava o [control.SteerChannel]
// directamente. Eram DUAS vias de retoma, e a que estava ligada era a outra.
//
// Removê-lo não perdeu cobertura: as propriedades que os seus testes provavam — recusa
// de sinal não autenticado, graceful pause, materialização paused→running pela máquina —
// são provadas em kernel/agent-runtime/control ao nível que corre em produção, e com mais
// casos. O que morreu com ele foi o guarda de ordenação do resume-com-correcção, que só
// fazia sentido para a operação composta que ele oferecia.
//
// FICA UM RESIDUAL DECLARADO: sem o tradutor, a metade de PAYLOAD deste contrato
// ([ControlMessage], [DecodeMessage], [NewInterrupt], [NewSteer], [NewResume],
// [NewResumeWithCorrection], [NewStateQuery], [Kind]) deixou de ter consumidor — de
// produção ou de outro pacote. O que TEM consumidor real e por isso fica é [ChannelID]
// (governance/surface-adapter) e [ControlSchemaVersion] (governance/approval-card).
// Alargar esta remoção ao payload seria decidir, em silêncio, o destino de um
// deliverable de AOS-119/EPIC-12; está aberto em AOS-303.
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
