// Package autonomysurface é a UX DA AUTONOMIA PROGRESSIVA (AOS-125, EPIC-12): a
// SUPERFÍCIE que torna LEGÍVEL e ACCIONÁVEL a taxonomia de autonomia L0–L5 por
// (agente, domínio) — o nível corrente, os critérios/progresso rumo à próxima promoção,
// as transições (promoção/demoção) com o seu motivo, e o fluxo pelo qual o utilizador
// SOLICITA revisão de nível dentro da política.
//
// # LÊ e DELEGA — NUNCA decide o nível
//
// Esta camada é APRESENTAÇÃO pura (padrão porta+adaptador). CONSOME o que outros
// subsistemas já impõem e NÃO reimplementa nada:
//   - AOS-089 (registo de níveis): o NÍVEL CORRENTE e o HISTÓRICO de transições são
//     LIDOS via [LevelReader] (satisfeita pelo *autonomy.LevelRegistry). Não há aqui
//     nenhuma porta de escrita — a superfície nunca chama SetLevel.
//   - AOS-090 (controlador): o PROGRESSO deriva do MESMO sinal de fiabilidade que a
//     decisão usa ([ReliabilityReader]); a DECISÃO de promoção fica atrás da porta
//     [LevelReviewer] que o wiring adapta ao Controller.Evaluate. A superfície PEDE
//     revisão; a política DECIDE. Uma DEMOÇÃO automática é comunicada IMEDIATA e sem
//     ocultação ([Surface.NotifyLevelChange] → [DemotionNotice]).
//
// Ver ADR-014 (taxonomia L0–L5 e promoção/demoção por fiabilidade medida), specs/EPIC-12
// (## AOS-125) e specs/EPIC-09 (AOS-089/090). A superfície reutiliza o vocabulário de
// spans aos.autonomy.* de AOS-089 ([autonomy.ExposeLevel]) para emitir a observabilidade
// de interacção, sem segredos.
//
// # Mapa dos critérios de aceitação
//
//   - AC1: [Surface.BuildLevelView] → [LevelView.Current] LIDO de [LevelReader.LevelFor].
//   - AC2: [ProgressToPromotion] derivado de [ReliabilityReader] vs o limiar da
//     policy-as-code de AOS-090.
//   - AC3: [TransitionView] (From/To/Reason) LIDO de [LevelReader.HistoryFor] — a
//     superfície EXPLICA a decisão, sem a tomar.
//   - AC4: [Surface.RequestMoreAutonomy] DELEGA a [LevelReviewer]; [Eligible] expõe a
//     opção de forma progressiva — a decisão é sempre da política.
//   - AC5: [Surface.NotifyLevelChange] → [DemotionNotice] imediata com o motivo.
package autonomysurface
