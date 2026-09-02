// Package activity define o CONTRATO DE ACTIVITY do Agent Runtime (AOS-021): a
// unidade que ISOLA um efeito externo (tool call / I/O / rede) do batimento
// determinístico do loop.
//
// "O loop é o batimento cardíaco, mas cada efeito externo é uma activity durável,
// isolada, idempotente e mediada" (tecnica/02 §4, _FONTE_ Fluxo de execução). Este
// pacote não reimplementa nenhuma das peças — COMPÕE-as num único ponto de despacho:
//
//   - IDEMPOTÊNCIA (AOS-014): cada activity tem step_id + idempotency key
//     ([durable.IdempotencyKey]); o resultado é memorizado no [durable.StepLedger]
//     e a verificação "already-applied" PRECEDE qualquer efeito — reexecutar a
//     activity devolve o resultado REGISTADO sem re-correr o efeito.
//   - MEDIAÇÃO (AOS-003 / ADR-002): o efeito é despachado pelo Reference Monitor
//     ([referencemonitor.Monitor.Mediate]) ANTES de executar (identidade, política,
//     orçamento, egress, audit). NÃO há caminho directo — o dispatcher nunca detém
//     uma função de efeito directamente invocável; o efeito vive como tool
//     registada no RM e só corre sob permit (no-bypass estrutural, ver abaixo).
//   - REPLAY (AOS-016): em [ModeReplay] a activity DEVOLVE o resultado REGISTADO do
//     log (via [ReplaySource]) com ZERO efeito, SEM mediação nem execução.
//   - TAINT (ADR-005): o resultado devolvido ao loop está SEMPRE marcado
//     [agentruntime.TaintUntrusted].
//   - COMPENSAÇÃO (AOS-020): se a activity tiver uma [Compensation], é registada no
//     [saga.CompensationRegistry] associada ao step_id, no momento do permit.
//
// # No-bypass ESTRUTURAL (não é convenção, é impossibilidade)
//
// O contrato reusa a garantia de AOS-003: o efeito externo é uma tool registada no
// [referencemonitor.Monitor]; a ÚNICA via de o executar é [referencemonitor.Monitor.Mediate],
// cujo dispatcher interno exige um permit não-forjável (campo não-exportado, uso
// único). O [Dispatcher] constrói um [referencemonitor.Call] e chama Mediate DENTRO
// do efeito do ledger — nunca expõe nem invoca a acção de efeito por outra via. Em
// [ModeReplay] o dispatcher sequer detém um Mediator (rm == nil): devolver o registo
// não pode, por construção, disparar um efeito. O subpacote activity/separation
// fornece um lint (AST, stdlib) que DETECTA um efeito externo (net/http, os, exec…)
// fora de uma activity, fechando a segunda camada (defesa-em-profundidade).
//
// # Agnóstico ao engine (AOS-022)
//
// A abstracção é AGNÓSTICA ao engine de durable execution. As peças que o dispatcher
// consome são interfaces ([Mediator], [Ledger], [ReplaySource], [CompensationRegistrar]);
// o adaptador de AOS-022 (Temporal / Restate / DBOS OU o contrato próprio) satisfaz
// [Ledger]/[ReplaySource] sobre o seu backend sem alterar esta API. O mapeamento:
//
//   - Activity       ↔ activity/step do engine (durável, com input + step_id estável).
//   - Dispatcher     ↔ o worker que executa a activity (aqui: ledger + RM).
//   - Ledger.Apply   ↔ a semântica exactly-once-observável do engine (memoização por key).
//   - ReplaySource   ↔ o event history / journal que o engine relê no replay.
//
// Este pacote NÃO implementa o engine externo (fica para AOS-022): fornece só o
// contrato e a composição sobre o contrato próprio (AOS-014/016).
//
// # Âncora de identidade: step_id (pré-condição de segurança)
//
// A idempotency key liga-se a (RunID, StepID) — NÃO aos parâmetros do call. No dedup
// a mediação é saltada e o resultado registado devolvido SEM confirmar que o call
// actual iguala o mediado da primeira vez. É, por isso, PRÉ-CONDIÇÃO do contrato que
// o mesmo (RunID, StepID) identifique sempre o MESMO call lógico (step_id
// determinístico e estável entre tentativas/replay; ver [Activity.StepID]). "Inputs
// normalizados", na linguagem do detalhe técnico de AOS-021, materializa-se AQUI como
// esse step_id determinístico — a âncora reprodutível por que a activity é
// identificada, deduplicada e reproduzida — e não como um hash separado do payload de
// input persistido no evento (o evento de ledger guarda status + payload do resultado
// + hash do RESULTADO; ver [durable.StepLedger]). O custo (Activity.CostMicroUSD) é
// observabilidade SÓ-POR-SPAN (emitido apenas no efeito real; ver [Dispatcher.Dispatch]
// e AOS021-Q5), não parte do evento durável. A ligação forte call→resultado por
// fingerprint durável (recusar em dedup um call divergente para um step_id já aplicado)
// fica para a evolução do evento de ledger / adaptador de engine.
//
// # Compensação: alcance e limite (durabilidade da intenção)
//
// Se a activity tiver uma [Compensation], o dispatcher regista-a no
// [CompensationRegistrar] DEPOIS de Apply, tanto no caminho applied (efeito agora)
// como no caminho dedup (already-applied) — a intenção de compensar é reconstruível
// independentemente de o efeito ter corrido nesta invocação. É isto que faz o
// crash-resume funcionar: um worker novo que RE-DESPACHA os passos aplicados (obtendo
// dedup) restaura as compensações no registry antes de a saga (AOS-020) as executar em
// LIFO. O registo é idempotente por step_id, pelo que re-despachar não duplica.
//
// LIMITE HONESTO: o registry é in-memory e a Action é uma closure não-serializável, logo
// a reconstrução ASSENTA no re-despacho dos passos aplicados pelo loop na retoma. Um
// worker que retome a MEIO de um run SEM re-despachar os passos já aplicados não terá as
// suas compensações — a durabilidade PLENA da intenção (marcador por step_id no Event
// Store + factory de compensação por ToolID no rebuild do registry) fica para o
// adaptador de engine (AOS-022). O contrato aqui não finge sobreviver a uma troca de
// worker sem re-despacho.
//
// # Adopção pelo loop (AOS-013): DIFERIDA
//
// O ESCOPO estrito de AOS-021 é o CONTRATO de activity (este pacote + testes). O que o loop
// faz com esse contrato DEPENDE DO MODO, e a redacção anterior deste parágrafo não o dizia —
// afirmava de forma INCONDICIONAL que o loop «ainda NÃO despacha via [Dispatcher.Dispatch]»,
// o que é verdade num modo e falso no outro. Uma auditoria concluiu a partir daí que DEF-801 e
// DEF-805 contavam dívida inexistente; a conclusão estava errada, mas foi esta frase que a
// tornou plausível. Os dois modos (AOS-295):
//
//   - MODO POR OMISSÃO — o do binário sem execução durável. Ninguém injecta dispatcher, e o
//     runtime atribui `directDispatcher{rm}` (agent-runtime/loop.go, na resolução de defaults):
//     cada tool call é mediada DIRECTAMENTE via [referencemonitor.Monitor.Mediate] (no-bypass e
//     taint garantidos) e NÃO passa por [Dispatcher.Dispatch]. Neste modo a idempotência/replay
//     pelo step-ledger não cobre o efeito externo REAL do loop em execução.
//
//   - MODO DURÁVEL — nó com AOS_DURABLE_EXECUTION=1. O composition-root
//     (packages/integration, secured.go) constrói o [Dispatcher] ledger-backed a partir do
//     StepLedger + RM e injecta-o por WithActivityDispatcher. Neste modo o despacho É
//     ledger-backed e a afirmação acima não se aplica.
//
// O que continua DEFERIDO, e é o que DEF-801/DEF-805 registam, é a adopção no caminho POR
// OMISSÃO: tornar o despacho ledger-backed o comportamento do loop base em vez de uma opção que
// o composition-root tem de ligar. Eixo: AOS-022 (adopção do dispatcher ledger-backed no loop).
// Ver agent-runtime/loop.go (mediateToolCall).
package activity
