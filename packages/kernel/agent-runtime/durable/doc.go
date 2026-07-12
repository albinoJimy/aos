// Package durable implementa o CONTRATO DE EXECUÇÃO DURÁVEL do Agent Runtime do
// AOS — a cláusula de idempotência por passo do ADR-001 (AOS-014) e o checkpoint
// intra-iteração resume-from-step sobre o Event Store replicado (AOS-015, ADR-007).
// É o alicerce sobre o qual assentam o replay resume-from-step (AOS-016), as sagas
// de compensação (AOS-020) e as activities de efeito externo (AOS-021/022).
//
// # Checkpoint intra-iteração e resume (AOS-015)
//
// O [EventStoreCheckpointer] é o [github.com/aos-ref/kernel/agent-runtime.Checkpointer]
// REAL: persiste cada fase confirmada de um turno como um evento append-only
// [EventTypeCheckpoint] com o [Cursor] de progresso (turno, fase, sub-passo,
// activities pendentes) — REFERENCIANDO, não copiando, o payload já no log. A sua
// idempotency_key é namespaced (run_id:ckpt-…), um TERCEIRO domínio de dedup
// distinto do turno e do ledger; o confirmed_step_id CASA com o step_id do ledger
// para o mesmo passo lógico. O [Resumer] relê os checkpoints do run e devolve o
// [ResumePoint] — o próximo passo NÃO confirmado — retomando sem repetir os
// confirmados nem perder os pendentes. Como os checkpoints vivem no Event Store
// replicado, o cursor sobrevive ao failover do worker. A escrita de checkpoint só
// CRESCE o registo append-only — nunca muta o prefixo cache-estável (ADR-009).
//
// O cursor é um MARCADOR de progresso, não estado de execução: é auto-suficiente em
// fronteiras de turno mas NÃO mid-dispatch — as invocações de tool pendentes
// (ToolID/Input) não vivem no log de AOS-015, pelo que re-despachá-las sem
// re-chamar o modelo, e re-montar o tail com fidelidade de prompt_hash, são
// responsabilidade de AOS-016 (ver [ResumePoint] e [Resumer]). O acoplamento de
// formato de step_id entre o loop e o [Resumer] é VERIFICADO no acto da retoma
// (fail-closed com [ErrStepIdentityMismatch]).
//
// # As três peças de idempotência (AOS-014)
//
//   - [IdempotencyKey] — função PURA e DETERMINÍSTICA key = run_id + ":" + step_id.
//     Injectiva (rejeita ':' nos inputs para fechar a colisão de deslocamento do
//     delimitador). A forma casa byte-a-byte com a chave por que o Event Store
//     (AOS-002) deduplica o turn.recorded e com o header downstream. "Espaço único"
//     é conceptual (convenção de derivação), NÃO literal: por passo há até três
//     chaves de ES — run_id:step_id (turno+downstream), run_id:step_id-tool-n
//     (sub-passo) e run_id:ledger-step_id (registo do ledger, domínio de dedup
//     separado). Ver [IdempotencyKey].
//   - [StepSequencer] — atribui step_ids monotónicos e ESTÁVEIS por passo lógico,
//     puros na posição (turno) e por isso invariantes entre execução, retry e
//     replay. Implementa o hook [github.com/aos-ref/kernel/agent-runtime.StepIdentity]
//     de AOS-013 (ligação ADITIVA via WithStepIdentity — sem alterar o loop).
//   - [StepLedger] — o LEDGER DE RESULTADO sobre o Event Store. Apply verifica
//     already-applied ANTES de qualquer efeito, memoriza {key→status,resultado,
//     hash} de forma durável, e é reconstruível do log (Rebuild) após reinício.
//
// # Modelo de garantia (ADR-001) — honesto
//
// Exactly-once VERDADEIRO do efeito externo é impossível sem cooperação downstream.
// O contrato publicado é AT-LEAST-ONCE + idempotência downstream honrando a key =
// ZERO efeitos OBSERVÁVEIS duplicados. O ledger NÃO afirma exactly-once do efeito;
// afirma que um downstream que honre a idempotency key regista o efeito UMA vez
// observável, mesmo com crash entre o efeito e o commit do ledger (o effect pode
// correr mais de uma vez; o downstream deduplica pela chave determinística).
//
// # Contrato para consumidores (AOS-020 / AOS-021 / AOS-022)
//
// Uma activity de efeito externo (AOS-021) executa-se SEMPRE dentro de
// [StepLedger.Apply], derivando a sua chave de [IdempotencyKey] (ou [StepSequencer.SubKey])
// e propagando essa MESMA chave ao downstream (header/campo de idempotência do
// alvo). Uma saga (AOS-020) regista, a par do resultado, a sua compensação, e
// reproduz o log em sentido inverso — cada compensação é, ela própria, um Apply
// idempotente. AOS-022 consome o ledger para observabilidade (contadores
// apply/dedup via [Observer]) e reconciliação. O ledger é reconstruível
// (Rebuild). Os inputs NÃO-DETERMINÍSTICOS de nível de turno — a resposta do modelo
// e as tool calls que dela derivam (ToolID/Input) — NÃO estão hoje no log de
// AOS-014/015 (o turn.recorded grava só o manifesto e a contagem de tool calls, e o
// evento de mediação do RM não grava o Input). Persistir/reconstruir esses inputs, e
// re-derivar as tool calls a partir deles em vez de os regenerar, é o trabalho do
// replay determinístico (AOS-016); AOS-015 entrega a POSIÇÃO (o cursor), não o
// conteúdo não-determinístico.
//
// # Liveness por lease/heartbeat + fencing tokens (AOS-018)
//
// A liveness distribuída NÃO é decidida por PID (falha silenciosamente cross-host):
// é decidida por LEASE com TTL renovável por HEARTBEAT sobre um relógio INJECTÁVEL
// ([Clock]), com FENCING TOKENS monotónicos que invalidam escritas de um worker
// obsoleto. As três peças:
//
//   - [LeaseManager] — a autoridade. [LeaseManager.Claim] reclama um run livre (nunca
//     reclamado ou com o lease expirado), mintando um fencing token ESTRITAMENTE
//     MONOTÓNICO e um [Lease] com TTL. [LeaseManager.Heartbeat] renova o TTL enquanto
//     o lease for o corrente e não tiver expirado. A ORIGEM e a durabilidade do
//     contador são o stream de lease ("lease:"+run_id) do Event Store: cada claim faz
//     Append com WithExpectedSeq (concorrência optimista, AOS-002); dois claims
//     concorrentes competem no mesmo slot — um vence, o outro relê e obtém um token
//     ESTRITAMENTE MAIOR. Não há contador só em memória: é reconstruível por replay.
//   - [FencingToken] — o contador uint64. Satisfaz ESTRUTURALMENTE o contrato
//     state.FencingToken de AOS-017 (Valid()/Value()), pelo que o token mintado pelo
//     Claim alimenta directamente Machine.Transition(ready → running) — o claim — sem
//     o pacote durable importar state (ligação por interface implícita).
//   - [FencedAppender] — o ENFORCEMENT OPT-IN. É o GUARD com que o CONSUMIDOR envolve
//     as suas escritas de efeito (padrão Claim → token → FencedAppender.Append): rejeita
//     ([ErrStaleFencingToken]) qualquer escrita cujo token seja inferior ao corrente do
//     run (worker superado por um novo claim) e, com [LeaseExpiryAuthority], também a de
//     um detentor com lease EXPIRADO. A escrita obsoleta NÃO chega ao log — é o que
//     garante NO MÁXIMO UM ESCRITOR EFECTIVO por run sobre os caminhos ROTEADOS por ele.
//
// # Alcance HONESTO do enforcement (o que está e o que NÃO está ligado)
//
// O fencing só protege as escritas EFECTIVAMENTE encaminhadas pelo [FencedAppender].
// NÃO está (por AOS-018) ligado aos caminhos de escrita internos do módulo: o
// [StepLedger], o [EventStoreCheckpointer] e a máquina de estados de AOS-017 persistem
// DIRECTO no Event Store. Nesses caminhos, o que impede duplicados HOJE é a DEDUP por
// idempotency_key do Event Store (StatusDuplicate) MAIS a idempotência DOWNSTREAM — e
// NÃO o fencing. As duas camadas são COMPLEMENTARES: um consumidor que queira a garantia
// de "no máximo um escritor efectivo" tem de encaminhar a sua escrita de efeito pelo
// [FencedAppender]. Análogo na máquina de estados: o claim ready → running valida por
// omissão só a PRESENÇA do token; a rejeição de um token OBSOLETO na própria transição
// exige ligar uma FencingAuthority (state.WithFencingAuthority).
//
// # Limite conhecido (TOCTOU) e durabilidade
//
// O token é consultado EXTERNAMENTE e NÃO é dobrado no envelope/CAS do próprio evento
// de negócio. AOS-018 fecha, de forma provada, o caso token-ESTRITAMENTE-inferior; o
// boundary token-IGUAL sob concorrência real (dobrar o token no CAS durável via
// expected_seq) fica delegado à implementação de produção do Event Store — FORA do
// âmbito deste módulo. Do mesmo modo, a DURABILIDADE e a monotonicidade CROSS-HOST do
// contador herdam do backend do Event Store: o reference impl é in-memory, pelo que os
// testes de "survives restart/failover" provam RECONSTRUÇÃO-a-partir-do-log (o
// LeaseManager é stateless e relê o stream), não persistência através da morte do
// processo — essa é responsabilidade do substrate (o ES expor um CAS durável partilhado).
//
// # Contrato partilhável com o Escalonador (EPIC-03)
//
// [LeaseAuthority] (Claim/Heartbeat/CurrentToken) e [TokenSource] (CurrentToken) são
// as interfaces PARTILHÁVEIS: o Escalonador (AOS-025..034) reutiliza o MESMO token
// monotónico e a MESMA autoridade de expiração, em vez de duplicar o mecanismo. O
// SCH atribui leases, prioridade e backpressure sobre este primitivo (ver tecnica/03).
//
// # Proibição de PID
//
// NENHUM caminho de código decide liveness por PID (os.Getpid/os.Getppid). É
// auditável — um teste varre os ficheiros de produção e falha se os encontrar.
//
// # Zero dependências externas
//
// Só a stdlib e os pacotes internos por path (eventstore, agent-runtime). O -race
// é limpo; o estado durável vive no Event Store (o [LeaseManager] é stateless entre
// chamadas — a verdade é o log), e o restante estado é protegido por mutex.
package durable
