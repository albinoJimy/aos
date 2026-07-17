// Package breaker implementa o CIRCUIT BREAKER MULTI-SINAL do AGENTE VIVO EM LOOP
// (AOS-080) — o disjuntor que apanha o agente que o PID/lease nunca via: o processo
// parece saudável (heartbeat vivo), mas o agente está preso num ciclo semântico com
// explosão de custo silenciosa. É o COMPLEMENTO da detecção de zumbis por
// lease/heartbeat (AOS-018/019, que apanha o worker MORTO): aqui o worker está VIVO.
//
// # Sinais independentes (colectores → snapshot → avaliador puro → decisão)
//
// O breaker combina sinais INDEPENDENTES e faz trip quando QUALQUER um — ou uma
// COMPOSIÇÃO configurável ([Composition]) — cruza o limiar:
//
//   - COST/TOKEN VELOCITY — o [otelgenai.CostVelocity] de AOS-078 (partilhado com o
//     orçamento por árvore, ADR-008): custo/tokens por segundo de wall-clock.
//   - WALL-CLOCK — tempo ABSOLUTO desde a entrada em [state.Running] (não o relógio do
//     lease, nem o [liveness.WorkClock.ActiveWork] — este é o backstop das esperas que
//     tecnica/08 §6 exige, wall-clock absoluto).
//   - AUSÊNCIA DE PROGRESSO — nenhum novo estado útil entre iterações. O sinal concreto
//     de action-dedup por hash é AOS-081; AOS-080 expõe só uma PORTA de progresso
//     plugável ([ProgressSource]: "fez progresso nesta iteração?") e conta as iterações
//     estéreis consecutivas. Sem a porta ligada, o sinal nunca dispara.
//
// A ARQUITECTURA é deliberadamente em camadas: os COLECTORES (as portas
// [VelocitySource]/[WallClockSource]/[ProgressSource]) produzem um [SignalSnapshot]; o
// AVALIADOR [Evaluate] é PURO/determinista (sem I/O, sem relógio) e devolve uma
// [Decision]. Só o [Breaker] tem estado (o contador de iterações estéreis) e efeitos.
//
// # Trip → estado durável (NUNCA kill cego), span, alerta, escalada
//
// Ao abrir, o breaker transita o run para um estado DURÁVEL da máquina de AOS-017 — sem
// o matar cegamente:
//
//   - velocity / no-progress → [state.Paused];
//   - WALL-CLOCK             → [state.TimedOut] (a spec fixa: o wall-clock leva ao
//     estado durável timed_out).
//
// O trip emite um SPAN dedicado ([OpBreakerTrip] = "aos.breaker.trip", com atributos
// aos.breaker.* — reason/sinal/limiar/valores, SEM segredos) e dispara um ALERTA
// operacional ([AlertSink], default no-op). Permite ainda ESCALAR A HUMANO
// ([Breaker.EscalateToHuman] → [state.WaitingOnHuman]) ou ABORTAR de forma graciosa
// ([Breaker.Abort] → [state.Failed], a saga de compensação — não um kill cego).
//
// É IDEMPOTENTE (re-trip sem resume intermédio é no-op — não duplica transições nem
// alertas) e FAIL-CLOSED (uma falha da transição durável reporta erro, não a engole). A
// TRAJECTÓRIA é PRESERVADA para RCA: a transição apenas APENDE ao event log
// append-only; nada é destruído, os spans/eventos anteriores ficam intactos.
//
// # Limiares por classe de agente
//
// Os [Thresholds] são CONFIGURÁVEIS POR CLASSE DE AGENTE via [ThresholdProvider]
// ([StaticThresholdProvider] é a impl de referência — molde do StaticThresholdProvider
// do breaker de orçamento, sem o acoplar). O breaker resolve os limiares da sua classe
// na construção.
//
// # Fronteira com o breaker de ORÇAMENTO (AOS-029)
//
// Este NÃO é o breaker de orçamento do Escalonador (packages/control-plane/scheduler):
// aquele governa a CONTINUAÇÃO do gasto de uma ÁRVORE (velocity+esgotamento, máquina
// closed/open/half-open própria). Este governa a LIVENESS de UM run vivo em loop
// (velocity+wall-clock+no-progress) e transita a máquina das TAREFAS (AOS-017). Ambos
// partilham o MESMO sinal de cost/token velocity (AOS-078, ADR-008) — mas são
// disjuntores distintos, com alvos distintos.
package breaker
