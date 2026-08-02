// Package plandispatch implementa a INTEGRAÇÃO DO ESCALONADOR (AOS-238, EPIC-18 /
// tecnica/18 §4): o despacho de nós de um plano APROVADO, a jusante do gate.
//
// FRONTEIRA ADR-018 (crítica). O SCH DESPACHA o que o ORQ MATERIALIZOU; NUNCA
// planeia, nunca aprova, e NUNCA é uma autoridade concorrente do ciclo de vida.
// Este pacote é, por construção, um AVALIADOR SEM ESTADO de escalonamento: uma
// passagem ([Dispatcher.Dispatch]) que, dado (plano materializado, vista do ciclo
// de vida, headroom, gate, cartões), decide que nós podem despachar AGORA e
// entrega-os ao sink — deferindo os restantes. NÃO detém o "running", não escreve
// transições de estado (lê-as por [LifecycleView], cuja autoridade é a máquina de
// estados durável do run, AOS-017), e não corre um loop próprio de retentativa (o
// escalonador externo re-invoca quando o headroom liberta). É este desenho que
// impede a "segunda fonte de verdade" do ciclo de vida que o ADR-018 proíbe.
//
// Os quatro invariantes que este pacote impõe fail-closed:
//
//  1. A JUSANTE DO GATE. Nenhum nó despacha antes de o plano ter passado o gate e
//     sido materializado ([Gate.Materialized] → `plan.materialized`, AOS-237). O
//     gate é observado, nunca produzido aqui.
//
//  2. DEPENDÊNCIAS SATISFEITAS. Um nó só despacha quando TODAS as suas `depends_on`
//     estão CONCLUÍDAS na vista do ciclo de vida. As arestas vêm do documento
//     APROVADO; a conclusão é lida (nunca decidida) por [LifecycleView].
//
//  3. TECTO DE CONCORRÊNCIA max_spawn = f(headroom) (AOS-028), run-time e DISTINTO
//     dos tectos de TAMANHO do plano (AOS-231, aplicados a montante na validação).
//     O headroom é uma PORTA ([Headroom]). A re-verificação é TOCTOU: sob pressão
//     ADIA (spawn diferido) via [Headroom.Acquire] atómico — NUNCA oversubscreve
//     nem faz spawn parcial silencioso. Um snapshot ([Headroom.Available]) é apenas
//     ADVISORY; a autoridade é o Acquire no instante do spawn.
//
//  4. ESPERA NÃO CONSOME HEADROOM. Um nó bloqueado — plano não materializado,
//     `depends_on` por satisfazer, ou `waiting_on_capability`/`danger` sem cartão
//     resolvido — NUNCA chega a [Headroom.Acquire]: a elegibilidade é avaliada
//     ANTES de qualquer reserva de slot. Esperar no gate/deps/cartão é gratuito em
//     concorrência.
//
// Nada aqui declara tipos de evento novos: o domínio `aos.planner.v1` é reutilizado
// de plannerevents (ex.: [plannerevents.MaterializedPayload] em [PlanFrom]).
package plandispatch
