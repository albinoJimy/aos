package controlsurface

// IDENTIFICAÇÃO DE CANAL — o que sobrou do contrato de mensagens (AOS-303).
//
// Este ficheiro tinha o CONTRATO ÚNICO de mensagens de controlo: o tipo `ControlMessage` (união
// versionada para steer / interrupt / resume-com-correcção / state), o seu codec JSON, uma
// `Validate` fail-closed, o discriminador `Kind` e cinco construtores.
//
// Tudo isso saiu em AOS-303, e a razão é a mesma que removeu a `ControlSurface` em AOS-292 (AC4),
// um ticket antes: o TRADUTOR que convertia cada mensagem nas chamadas do
// `control.SteerChannel` era o ÚNICO consumidor do payload. Removido ele, `ControlMessage`,
// `DecodeMessage`, `NewInterrupt`, `NewSteer`, `NewResume`, `NewResumeWithCorrection`,
// `NewStateQuery` e `Kind` ficaram sem consumidor nenhum — nem de produção, nem de outro pacote,
// nem interno fora dos testes que os validavam a si próprios.
//
// # PORQUE ISTO NÃO FOI FEITO EM AOS-292, E FOI EM DOIS PASSOS
//
// Porque não era a mesma decisão. Remover a superfície fechava uma AC de AOS-292; remover o
// PROTOCOLO decide o destino de um deliverable nomeado de AOS-119/EPIC-12, com teste de schema
// próprio e uma promessa de estabilidade em SemVer. Fazê-lo de arrasto teria sido decidir por
// outro epic em silêncio. Ficou como AOS-303, com a medição ao lado, e é essa decisão que este
// ficheiro executa.
//
// # O QUE FICA, E PORQUE FICA
//
// [ChannelID] — usado por `control-plane/governance/surface-adapter`, que mapeia plataformas
// (slack/telegram → chatbot, desktop → desktop) a este rótulo. É presentation: entra no span de
// interacção como `aos.control.channel` e nunca afectou a tradução.
//
// Fora deste ficheiro sobrevivem, pela mesma regra: [ControlSchemaVersion] (`version.go`, usada
// por `governance/approval-card`) e [StateProjector] (`reflection.go`, usado por `cmd/aos-demo` e
// rastreado pelos EPIC-13/14/15).

// ChannelID identifica a SUPERFÍCIE de onde uma acção de controlo vem (desktop/chatbot/API). É
// presentation: um rótulo de observabilidade (entra no span de interacção como
// aos.control.channel), NÃO afecta comportamento — o mecanismo é idêntico em todos os canais;
// muda só o adaptador. Opcional.
type ChannelID string

const (
	// ChannelDesktop — superfície de desktop.
	ChannelDesktop ChannelID = "desktop"
	// ChannelChatbot — superfície de chatbot.
	ChannelChatbot ChannelID = "chatbot"
	// ChannelAPI — superfície de API/programática.
	ChannelAPI ChannelID = "api"
)
