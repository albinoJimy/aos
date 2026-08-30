// Package runlifecycle é a COMPOSIÇÃO ORQ/SCH↔posse sob disciplina de lease
// (AOS-281), governada pelo ADR-023.
//
// # A regra, numa frase
//
// O direito de escrever uma transição de estado de ciclo de vida do run R É,
// exactamente, a posse do fencing token corrente de `lease:<R>`. Não é um atributo
// de um módulo, de um binário nem de um papel — é um facto durável no Event Store
// replicado.
//
// Daí decorre tudo o que este pacote faz, e nada do que ele faz é mecanismo novo:
//
//   - [Tenure] é a POSSE. Reclama (`lease.claimed`), renova (`lease.renewed`) e
//     ANUNCIA que largou (`lease.released`), sobre o [durable.LeaseManager] de
//     AOS-018. Enquanto a detém, e só enquanto a detém, expõe uma via de escrita.
//   - A via de escrita é o [durable.FencedAppender] de AOS-018, que recusa
//     ([durable.ErrStaleFencingToken]) uma escrita cujo token seja inferior ao
//     corrente — e, porque o [durable.LeaseManager] também satisfaz
//     [durable.LeaseExpiryAuthority], recusa TAMBÉM a escrita de um detentor que já
//     largou ou cujo lease expirou, ANTES sequer de existir um novo claim. É isto
//     que torna o handoff um intervalo em que NINGUÉM escreve, em vez de uma corrida.
//   - [Tenure.Graph] devolve um construtor de grafo RE-HIDRATADO
//     ([orchestrator.RebuildDAG]) e amarrado ao token. Não há via de produção neste
//     pacote que construa um grafo cego sobre um run que já existe no log — o
//     `ErrLogAhead` de AOS-025 deixa de ser o detector do erro depois de ele
//     acontecer e passa a ser inalcançável.
//
// # O que este pacote NÃO faz (ADR-023 §2.2)
//
// Não dá ao Escalonador nenhuma via de escrita de ciclo de vida, e não pede ao
// `plandispatch` que mude. As portas que aqui se implementam sobre o Event Store —
// [LifecycleReader], [ResultReader], [PayloadReader] — são todas de LEITURA, que é
// a assimetria que a `plandispatch.LifecycleView` documenta como sendo o que preserva
// a fronteira do ADR-018. O despachante continua sem saber que existe um Event Store.
//
// A única porta de ESCRITA que o despachante consome, [BranchJournal], regista os
// FACTOS DE DECISÃO DELE (`plan.branch_decided`, ADR-022 §2.4(3)) no stream do PLANO
// — não estado de ciclo de vida, não o stream do run. A distinção está declarada no
// ADR-023 §2.2 e é imposta aqui por o journal escrever noutro stream que o da posse.
//
// # Fronteira (a direcção é o que mantém os guardas verdes)
//
// Este pacote importa o orquestrador e o despachante; nenhum deles o importa. O nó
// `aos` não o requer, pelo que ele não entra no `go list -deps` do binário do nó e o
// guarda transitivo do ADR-018 §5 continua a passar SEM ALTERAÇÃO. Ver
// [boundary_test.go] deste pacote, que verifica a direcção a partir DESTE lado.
package runlifecycle
