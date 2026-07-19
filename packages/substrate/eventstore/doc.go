// Package eventstore implementa o Event Store append-only replicado do AOS
// (ticket AOS-002), a fonte de verdade única e ordenada por stream da
// plataforma (ADR-007).
//
// # Modelo
//
// Todo o facto de um run — um turno de modelo, uma tool call, um resultado,
// uma transição de estado — é gravado como um evento append-only com o mesmo
// envelope canónico (ver tecnica/13 §3). O log nunca é sobrescrito: correcções
// são novos eventos. A API pública não expõe qualquer operação de update ou
// delete.
//
// # Garantias (contrato C2 — RT ↔ ES)
//
//   - Append-only estrito: nenhuma API muta um evento persistido. Uma tentativa
//     de escrever numa posição já ocupada (ver WithExpectedSeq) devolve
//     ErrAppendOnlyViolation. Read devolve cópias — o chamador nunca mantém uma
//     referência ao estado guardado.
//   - Ordem total por (stream_id, seq): seq monotónico, gapless, por stream,
//     começa em 1, serializado pelo líder.
//   - Concorrência optimista: WithExpectedSeq(n) afirma que o último seq
//     committed do stream é n; se não bater devolve ErrSeqConflict.
//   - Idempotência por idempotency_key = f(run_id, step_id): um segundo Append
//     com a mesma chave devolve Status=Duplicate e o seq committed original, sem
//     duplicar. Sobrevive a failover porque o índice de dedup é mantido em cada
//     réplica (reconstrutível a partir do log committed).
//   - Read de stream inexistente devolve ErrStreamNotFound.
//
// A ordem de verificação num Append é: idempotência primeiro (o duplicado
// ganha), depois expected_seq, depois quórum, e só então a escrita.
//
// # Replicação (modelo de referência)
//
// As réplicas são in-process. A replicação é síncrona, em lockstep, a todo o
// conjunto de réplicas vivas (o "in-sync replica set"): depois de cada commit
// todas as réplicas vivas têm um log idêntico. Uma entrada só é committed
// quando pelo menos o quórum (maioria) de réplicas vivas a armazena — antes do
// ACK ao produtor e antes do push. Se o número de réplicas vivas for inferior
// ao quórum a escrita é rejeitada com ErrNoQuorum e não deixa rasto
// (fail-closed). No failover a eleição escolhe a réplica viva mais actualizada
// (maior commit index); eventos confirmados nunca se perdem enquanto sobreviver
// um quórum.
//
// Este modelo torna as invariantes determinísticas e testáveis; não é um Raft
// completo. Em produção o backend é NATS JetStream (R3/R5, replicação Raft).
//
// # Concorrência: sem single-writer (AOS-100, ADR-007)
//
// A ordem total é POR STREAM ((stream_id, seq)), nunca global. Por isso NÃO há um
// escritor único: a serialização é POR-STREAM (locks listrados — ver sharding.go).
// Appends ao MESMO stream serializam-se (seq gapless, CAS de WithExpectedSeq, dedup
// e ordem de push preservados); appends a streams DIFERENTES progridem EM PARALELO,
// sem contenção global — múltiplos workers escrevem e leem para replay em paralelo.
// O antigo mutex global (que serializava TODAS as escritas através de um único
// líder) foi eliminado: o mu do Store protege apenas a membership do cluster
// (líder, alive set); os appends detêm-no em RLock. A dedup e o log são por stream,
// removendo o último contentor partilhado entre streams. Não existe ponto único de
// escrita (SPOF): a falha de um nó (kill de réplica) não interrompe as escritas nem
// perde dados confirmados dentro do quórum.
//
// # Soberania regional (ADR-011)
//
// Um board tem uma fronteira regional de soberania: os seus dados só podem residir
// nessa região. Quando configurada (WithRegion ou WithSovereigntyBoard), TODAS as
// réplicas têm de estar na região do board; uma réplica fora da fronteira — ou com
// região ausente/desconhecida — é REJEITADA na construção com ErrSovereigntyViolation
// (fail-closed: região desconhecida ⇒ deny). O quórum é computado dentro da região e
// a eleição de líder nunca promove liderança cross-border. Réplicas e backups NUNCA
// cruzam a fronteira. Sem fronteira configurada a soberania fica dormente
// (retro-compatível).
//
// # Transporte push
//
// Subscribe entrega eventos committed a subscritores por push, em ordem de seq,
// com filtro por streams e/ou types. Cada subscritor tem a sua própria
// goroutine e fila; um subscritor lento não bloqueia os outros. Unsubscribe e
// Close libertam a goroutine e a fila sem fugas.
package eventstore
