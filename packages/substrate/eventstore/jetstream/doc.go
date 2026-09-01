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
//   - LEITURA EM LOTES, e o custo está MEDIDO. Um lote é um consumidor efémero que
//     empurra a janela inteira (ver janelaDeLeitura); a completude é VERIFICÁVEL porque
//     a contagem do subject é pedida ao servidor antes, e um lote incompleto é ERRO — um
//     log truncado em silêncio faria o replay reconstruir estado errado. Um stream de 200
//     eventos lê-se em ~25–51 ms (3 881–7 967 eventos/s), contra ~1,7 s da versão
//     pedido-por-evento que isto substituiu. O WAL local continua ~4–5 MILHÕES/s: é
//     memória contra rede, e a diferença é o preço da replicação. Sem cache de eventos —
//     cada Read vai ao servidor, que é a diferença entre um log partilhado e N cópias.
//   - SUBSCRIÇÃO SÓ DO NOVO. [Store.Subscribe] cria um consumidor efémero com
//     deliver_policy "new" — a mesma semântica do modelo de referência, que só faz
//     fanout do que é escrito depois da subscrição. Não é um consumidor durável: sem
//     acks, sem flow control, sem heartbeats. É a configuração em que o push foi
//     MEDIDO, e não se finge cobrir mais.
//   - SOBERANIA (AC5, ADR-011): IMPLEMENTADA por `placement` no stream e VERIFICADA
//     contra a configuração armazenada — ver soberania.go. Sem [ComRegiao] a fronteira
//     fica DORMENTE (retro-compatível), tal como no store de referência; com ela, um
//     stream sem colocação — ou com a de outra região — ABORTA fail-closed.
//   - RECONEXÃO AUTOMÁTICA, com um limite nomeado. O cliente reconecta-se sozinho (recuo
//     exponencial até 5 s) e as escritas RETOMAM — MEDIDO: 1 s depois de morrer o nó a
//     que estava ligado, sem reiniciar o processo. Antes disto, NUNCA retomavam. Dê-lhe
//     VÁRIOS endereços separados por vírgula: com um só, só há cura quando esse nó voltar.
//     As subscrições RECUPERAM, e não apenas retomam: o consumidor é DURÁVEL com acks
//     explícitos, pelo que o servidor sabe até onde a entrega foi confirmada e recomeça
//     aí — os eventos escritos ENTRE a quebra e o retomar SÃO entregues (medido). O ACK
//     vai depois do handler, para confirmar o que foi processado e não o que chegou.
//     Há FLOW CONTROL (um subscritor lento é travado, não atropelado) e BATIMENTO: sem
//     ele, um consumidor morto do lado do servidor é indistinguível de um stream
//     sossegado, e a subscrição ficava morta sem ninguém saber. Ao fim de 15 s sem nada —
//     nem evento nem batimento — o consumidor é dado por morto e a entrega
//     re-estabelecida (MEDIDO, apagando o consumidor pelas costas do subscritor).
//     LIMITE ACEITE: o consumidor recriado parte do seq fixado na subscrição, logo os
//     eventos desde então são REENTREGUES. É at-least-once — nada se perde, algumas
//     coisas repetem-se —, e para um log cuja idempotência é por (run_id, step_id) essa é
//     a troca certa. O consumidor é R1: se o nó que o aloja morrer, é isto que o cobre.
package jetstream
