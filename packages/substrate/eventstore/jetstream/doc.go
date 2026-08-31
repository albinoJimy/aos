// Package jetstream é a implementação de [eventstore.EventStore] sobre um cluster NATS
// JetStream replicado (AOS-100, ADR-007).
//
// É a peça que converte «o substrato tem a propriedade» em «o Event Store do AOS tem a
// propriedade». Sem ela, o cliente de protocolo e o instrumento de conformidade existem
// mas não se tocam: o nó continua a abrir o WAL local, e os sensores do DEF-282
// continuam — correctamente — a acusar ausência.
//
// # A ideia toda, numa frase
//
// A ordem por stream é do AOS; a ARBITRAGEM é do servidor. Cada stream do AOS é um
// subject do JetStream, e cada escrita afirma, no cabeçalho
// Nats-Expected-Last-Subject-Sequence, qual era a última mensagem que vira nesse
// subject. Se outro escritor entretanto escreveu, o servidor RECUSA — e é aí, e só aí,
// que reside a propriedade que o Event Store de referência não tem entre processos.
//
// # Duas numerações, e porque não se podem confundir
//
// O contrato C2 exige `seq` GAPLESS POR STREAM a começar em 1. O JetStream numera
// GLOBALMENTE por stream físico, e o seu Nats-Expected-Last-Subject-Sequence refere-se a
// essa numeração global, não a um contador por subject. São coisas diferentes e ambas
// necessárias:
//
//   - o seq do AOS vive DENTRO do envelope (Event.Seq), é atribuído por nós e é o que o
//     chamador vê;
//   - o seq do JetStream é o token de CAS, vive fora do envelope e nunca é exposto.
//
// Confundi-los daria um log com buracos (a numeração global salta entre subjects) e
// quebraria a re-hidratação, o replay e a hash-chain que assentam no seq do AOS.
//
// # A deduplicação é DERIVADA DO LOG, nunca da janela do servidor
//
// MEDIDO a 2026-08-31: a deduplicação do JetStream é uma JANELA TEMPORAL. A idempotência
// do AOS por (run_id, step_id) não tem prazo — um resume-from-step horas depois repete o
// mesmo passo — pelo que o índice é reconstruído a partir do log, como o modelo de
// referência sempre fez. A Nats-Msg-Id é enviada na mesma, como rede de segurança barata
// para retries imediatos, mas NUNCA é a garantia.
//
// # Limites, declarados e não descobertos
//
//   - LEITURA POR ROUND-TRIP. Reconstruir um stream caminha o subject com
//     `next_by_subj`, um pedido por evento. É a operação mais simples que é correcta;
//     não há batching nem cache de eventos. Um stream longo custa proporcionalmente.
//   - SUBSCRIÇÃO SÓ DO NOVO. [Store.Subscribe] cria um consumidor efémero com
//     deliver_policy "new" — a mesma semântica do modelo de referência, que só faz
//     fanout do que é escrito depois da subscrição. Não é um consumidor durável: sem
//     acks, sem flow control, sem heartbeats. É a configuração em que o push foi
//     MEDIDO, e não se finge cobrir mais.
//   - SOBERANIA (AC5, ADR-011): IMPLEMENTADA por `placement` no stream e VERIFICADA
//     contra a configuração armazenada — ver soberania.go. Sem [ComRegiao] a fronteira
//     fica DORMENTE (retro-compatível), tal como no store de referência; com ela, um
//     stream sem colocação — ou com a de outra região — ABORTA fail-closed.
//   - SEM RECONEXÃO. Herdado do cliente: uma ligação partida devolve erro ao chamador.
package jetstream
