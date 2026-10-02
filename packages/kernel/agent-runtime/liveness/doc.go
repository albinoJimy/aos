// Package liveness resolve, no Agent Runtime do AOS (AOS-019), a colisão entre a
// SUSPENSÃO LEGÍTIMA de um run e a DETECÇÃO DE ZOMBI. No plano-base, um gate humano
// (waiting_on_human) parecia um worker `running` pendurado e a detecção de zombi
// marcava-o como morto; este pacote garante que os estados de espera NÃO são
// classificados como zumbi, preservando o gate TTL fail-closed (ADR-013).
//
// # Os DOIS relógios (o cerne)
//
// AOS-019 separa dois relógios que o plano-base confundia:
//
//   - Relógio de TRABALHO ACTIVO — o heartbeat/lease de AOS-018 ([durable]). Governa a
//     liveness de [state.Running]: um lease expirado (sem heartbeat no TTL) é worker
//     preso. Nos estados de espera este relógio PAUSA — não é renovado, mas TAMBÉM não
//     conta como expirado-para-zumbi. O contrato de EXCLUSÃO para o breaker é o
//     [WorkClock], que acumula SÓ o tempo em running.
//   - Relógio de ESPERA — o [WaitingGate], um TTL PRÓPRIO do gate humano, separado do
//     heartbeat. Excedido o TTL sem aprovação → o run transita waiting_on_human →
//     killed (fail-closed, ADR-013), alinhado com [state.Machine.CheckDeadlines].
//
// # Classificação
//
// [ZombieClassifier.Classify] mapeia uma [RunLiveness] (estado + lease de trabalho
// expirado + gate de espera excedido) para uma [Classification]:
//
//   - estados de espera (waiting_on_human/waiting_on_tool/paused) → [WaitingLegitimate]
//     — NUNCA [Zombie], mesmo com o lease de trabalho expirado (invariante
//     não-negociável);
//   - waiting_on_human com o gate TTL excedido → [GateExpired] (leva a killed, NÃO é
//     zombi);
//   - running com o lease de trabalho expirado → [Zombie] (worker realmente preso —
//     não-regressão);
//   - terminais → [Terminal]; restantes activos/recuperação → [Alive].
//
// # Contrato de sinais (circuit breaker multi-sinal, EPIC-08)
//
// O breaker multi-sinal (tecnica/08 §6) é EPIC-08. AOS-019 fornece SÓ a EXCLUSÃO do
// tempo de espera do sinal "sem progresso de trabalho activo": [WorkClock.ActiveWork]
// (acumula só running), [CountsAsActiveWork] e [IsWorkPaused]. O breaker completo
// (avaliação e trip) NÃO é implementado aqui.
//
// # Integração aditiva
//
// [RunLivenessFrom] compõe a entrada do classificador a partir da Machine (AOS-017), do
// lease (AOS-018) e do gate deste ticket, sem os acoplar nem os quebrar. Todos os
// relógios são injectáveis ([Clock]) para testes determinísticos sem sleeps. Para
// eliminar POR CONSTRUÇÃO o drift entre o TTL do gate e o da Machine, prefira
// [NewWaitingGateFrom] — deriva o [WaitingGate] do mesmo TTL e do mesmo relógio da
// Machine, em vez de os replicar por convenção ([NewWaitingGate]).
//
// # Fronteira fail-closed: quem MATA vs quem SINALIZA (importante)
//
// A classificação [GateExpired] é ADVISORY — um SINAL, não o executor. O ÚNICO caminho
// que mata o run fail-closed (waiting_on_human → killed, ADR-013) é
// [state.Machine.CheckDeadlines], que o consumidor TEM de correr periodicamente. A
// garantia "gate excedido ⇒ killed" depende, portanto, de o chamador (a) construir o
// gate com o TTL da Machine — use [NewWaitingGateFrom] — E (b) chamar CheckDeadlines; o
// pacote liveness NÃO a garante por si. Corolário fail-OPEN: um waiting_on_human
// avaliado SEM gate (nil) é [WaitingLegitimate] indefinidamente — isso é wiring
// incompleto, não espera legítima.
//
// # Backstop de waiting_on_tool e paused: FORA deste pacote (contrato explícito)
//
// Só waiting_on_human tem relógio de espera (o gate). waiting_on_tool e paused são
// SEMPRE [WaitingLegitimate] e NÃO têm timeout de backstop NESTE pacote. "Espera
// legítima" NÃO implica "reapeada aqui": a fronteira da espera não-humana é DELEGADA —
// o timeout da activity externa (AOS-018) é a primeira linha para waiting_on_tool, e a
// rede de segurança é o tecto de wall-clock ABSOLUTO (nunca o [WorkClock.ActiveWork],
// que CONGELA nestes estados).
//
// ACTUALIZAÇÃO (AOS-419, eixo do DEF-906): essa rede de segurança EXISTE desde que
// [state.Machine.CheckDeadlines] passou a transitar waiting_on_tool/paused → timed_out
// no mesmo tecto de [state.WithRunWallClock]. Até lá, o switch da Machine só limitava
// waiting_on_human e running, e o disjuntor de EPIC-08 — a segunda via — é no-op fora de
// running ([CountsAsActiveWork]), pelo que os dois estados não tinham prazo NENHUM. A
// ressalva que sobra é de ALCANCE, não de mecanismo: o backstop só morde onde alguém
// CHAMA CheckDeadlines sobre a máquina do run, com o tecto configurado (0 desliga-o).
//
// # Relógio: wall-clock, sem saltos
//
// Os deadlines (gate e CheckDeadlines) são comparações de WALL-CLOCK: o enteredAt de
// [state.Machine.EnteredAt] tem a componente monotónica removida. Assumem um relógio
// sem saltos — um ajuste NTP desloca o instante do kill (para trás adia, para a frente
// antecipa). Gate e Machine degradam da MESMA forma, pelo que concordam sempre entre si.
//
// # Fora de âmbito (delegado)
//
// A transição durável waiting_on_human → killed é de [state.Machine.CheckDeadlines]
// (AOS-017), tal como o backstop de waiting_on_tool/paused → timed_out (AOS-419); a
// origem monotónica do lease/fencing é de AOS-018; o circuit breaker multi-sinal
// completo é EPIC-08.
package liveness
