// Package controlsurface é o que RESTA da superfície de controlo HITL out-of-band do AOS
// (AOS-119, EPIC-12, ADR-013) — três peças com consumidor real, e nada mais.
//
// # O QUE ESTE PACOTE É HOJE
//
//   - [StateProjector] — o read-model por run que subscreve [eventstore.Store.Subscribe] e faz
//     FAN-OUT do estado corrente a todos os canais subscritos: dois canais vêem o MESMO estado
//     (fonte única). Consumido por `cmd/aos-demo` e rastreado pelos EPIC-13/14/15 —
//     [reflection.go].
//   - [ControlSchemaVersion] — o contrato de versão em SemVer, com o domínio
//     [SchemaDomain] = "aos.control.surface.v1". Consumido por `governance/approval-card` —
//     [version.go].
//   - [ChannelID] — o rótulo da superfície de origem (desktop/chatbot/API), presentation pura.
//     Consumido por `governance/surface-adapter` — [messages.go].
//
// # O QUE SAIU, E EM QUE ORDEM
//
// Este pacote foi desenhado como um CONTRATO UNIFICADO: um protocolo versionado
// (`ControlMessage`) que qualquer canal emitiria, e uma `ControlSurface` que o traduzia para as
// APIs reais de AOS-023. Nenhuma das duas metades chegou a produção, e saíram em dois passos
// deliberadamente separados:
//
//   - **AOS-292 (AC4)** removeu a `ControlSurface`. Nunca foi composta: construía-se só em
//     testes, enquanto o nó chamava o [control.SteerChannel] directamente. Eram duas vias de
//     retoma, e a ligada era a outra.
//   - **AOS-303** removeu o PAYLOAD que ela era a única a consumir — `ControlMessage`, o seu
//     codec, a `Validate`, o discriminador `Kind` e os cinco construtores.
//
// Foram dois tickets e não um porque não era a mesma decisão. Remover a superfície fechava uma AC
// de AOS-292; remover o protocolo decide o destino de um deliverable nomeado de AOS-119/EPIC-12,
// com teste de schema próprio e promessa de estabilidade em SemVer. Arrastá-lo teria sido decidir
// por outro epic em silêncio.
//
// # A GARANTIA QUE NÃO ESTAVA AQUI
//
// O mecanismo durável de HITL — sinal sempre aceite, pausa graciosa no fim do turno, tudo
// gravável e reconstruível por replay (ADR-013) — sempre viveu em
// kernel/agent-runtime/control (AOS-023), e continua lá, composto e em uso. Este pacote nunca o
// implementou: compunha-o. O que se removeu foi a camada de apresentação que ninguém adoptou,
// não uma garantia.
package controlsurface
