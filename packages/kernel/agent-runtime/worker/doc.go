// Package worker é o LOOP SUPERVISOR DE POSSE-DE-PARTIÇÃO-POR-RUN de AOS-099: o
// processo do plano de dados que amarra os primitivos duráveis de
// [github.com/aos-ref/kernel/agent-runtime/durable] num worker STATELESS, de modo
// que a morte ou substituição de um worker a meio de um run seja recuperável
// resume-from-step, SEM efeitos duplicados e SEM dupla execução cross-host.
//
// # O delta (COMPOR, nunca reimplementar)
//
// A fundação técnica (lease/fencing/idempotency/ledger/resume/checkpoint/máquina de
// estados/event store/tracing) JÁ EXISTE e está testada. Este pacote NÃO
// reimplementa nenhum desses mecanismos — apenas os COMPÕE num loop de worker:
//
//   - [durable.LeaseManager] — posse de partição via lease/heartbeat com TTL e
//     fencing token ESTRITAMENTE monotónico (NUNCA PID — a liveness é decidida por
//     lease/TTL sobre o relógio injectado). É a base de AC4.
//   - [durable.FencedAppender] — TODA a escrita de progresso do worker é fenced: um
//     token inferior ao corrente (worker superado por um novo claim) é rejeitado com
//     [durable.ErrStaleFencingToken] e a escrita NÃO chega ao log. É o enforcement de
//     "no máximo um escritor efectivo" que fecha AC4/AC5.
//   - [durable.StepLedger] — cada efeito é aplicado sob a idempotency key
//     f(run_id, step_id) (ADR-001): a re-execução após crash, ou uma corrida entre
//     workers, deduplica (StatusDuplicate) e produz ZERO efeitos observáveis
//     duplicados. É a rede de segurança de AC1/AC5.
//   - [durable.Resumer] + [durable.EventStoreCheckpointer] — resume-from-step: um
//     worker novo relê os checkpoints do log e retoma no próximo passo NÃO
//     confirmado, sem repetir os já confirmados. É a recuperação de AC1.
//   - [durable.StepSequencer] — step_ids monotónicos e ESTÁVEIS (função pura da
//     posição), invariantes entre execução, retry e replay: é o que torna a
//     idempotency key idêntica entre tentativas.
//   - Reference Monitor (ADR-002) — TODA a tool call é mediada por [Mediator.Mediate]
//     ANTES de qualquer efeito; o worker nunca contorna o PEP.
//   - [otelgenai.Tracer] — o worker emite um span execute_tool por passo com o custo
//     por span (AttrRunID/AttrStepID/AttrCostMicroUSD).
//
// # Statelessness (AC2)
//
// O [Worker] NÃO guarda estado durável de run em campos do processo — só handles
// para as PORTAS (LeaseManager, FencedAppender, StepLedger, Resumer, Checkpointer,
// Mediator, Tracer) e configuração imutável. Todo o estado de execução de um run (o
// lease detido, o token de fencing, o cursor de progresso, o passo corrente) vive na
// PILHA de [Worker.Run] (na [runSession]) e, de forma durável, no Event Store. Um
// [Worker] é, por isso, seguro para servir vários runs em paralelo, e um processo
// worker NOVO reconstrói o ponto de retoma inteiramente a partir do log — provado no
// teste de reconstrução (o worker de substituição não herda nada do processo morto).
//
// # Partição por run (AC3) — sharding natural, sem rebalancing disruptivo
//
// O particionamento é por run_id: cada run é a SUA PRÓPRIA partição. Não há um número
// fixo de partições nem um mapa de hash a rebalancear — uma réplica nova assume uma
// partição simplesmente RECLAMANDO um run cujo lease está livre ou expirado
// ([Assigner]). Um run cujo lease ainda é válido é detido por outra réplica
// ([durable.ErrLeaseHeld]) e é deixado em paz — sem roubo, sem coordenação
// intra-processo. Scale-in de N→N-1 (AC5): o lease do worker terminado expira por
// ausência de heartbeat e outra réplica reclama-o e retoma resume-from-step; o
// trabalho em curso não se perde e os efeitos não se duplicam (ledger + fencing).
//
// # Ordem fail-closed de um passo
//
// Para cada passo, detendo um lease com token T, o worker:
//
//  1. GATE FENCED (primeiro): appenda um evento de progresso via [durable.FencedAppender]
//     com T. Se T for inferior ao corrente (um novo claim superou este lease) ou o
//     lease tiver expirado, a escrita é rejeitada com [durable.ErrStaleFencingToken] e
//     o worker PARA IMEDIATAMENTE de escrever (fail-closed) — não medeia, não aplica,
//     não duplica. É o ponto que garante "no máximo um escritor efectivo".
//  2. EFEITO via ledger: [durable.StepLedger.Apply] sob a key f(run_id, step_id). O
//     effect medeia a tool call pelo Reference Monitor e memoriza o resultado.
//     Already-applied ⇒ o effect NÃO corre (sem re-mediação, sem re-despacho).
//  3. CHECKPOINT: avança o cursor de progresso (fase verified do turno) para que um
//     worker de substituição retome DEPOIS deste passo.
//  4. SPAN: emite o span execute_tool. O CUSTO POR PASSO só é anotado quando o efeito
//     CORREU (applied==true); um passo re-executado na retoma e DEDUPLICADO pelo
//     ledger não re-contabiliza custo (a agregação AOS-078 conta cada passo uma vez).
//
// # Fronteira honesta (TOCTOU token-IGUAL — delegado a AOS-100)
//
// O [durable.FencedAppender] fecha, de forma PROVADA, o caso token ESTRITAMENTE
// INFERIOR (o worker obsoleto): a sua escrita nunca entra no log. O boundary
// token-IGUAL sob concorrência REAL de processos — dobrar o token no CAS DURÁVEL do
// Event Store (expected_seq condicionado ao token, sobrevivendo à morte do processo)
// — fica DELEGADO à implementação de produção do Event Store (AOS-100) e não é do
// âmbito deste ticket. Os testes usam o Event Store de referência in-memory e provam
// (a) reconstrução-a-partir-do-log e (b) invalidação do token OBSOLETO (menor), que é
// o que o reference impl garante. A replicação interna do ES (AOS-100), o pool de
// microVMs (AOS-103) e a IaC (AOS-098) estão FORA de escopo.
package worker
