// Package bus implementa o barramento de eventos push com subscrições duráveis
// do AOS (ticket AOS-009), a camada de distribuição fiável sobre o Event Store
// (AOS-002, ADR-007).
//
// # Posição na arquitectura
//
// O plano de controlo escala horizontalmente com push event-driven: o
// Escalonador empurra trabalho a workers stateless (tecnica/03). O barramento é
// a camada que desacopla produtores de consumidores e evita polling. NÃO
// reimplementa o Event Store — ENVOLVE-O (github.com/aos-ref/substrate/eventstore)
// e acrescenta por cima: subscrições nomeadas por filtro, cursores duráveis com
// ACK explícito (entrega at-least-once), replay a partir de um seq arbitrário,
// backpressure com política de degradação declarada e dead-letter.
//
// # Entrega push (sem polling)
//
// Subscribe liga uma subscrição AO VIVO ao Event Store (es.Subscribe) e nunca
// faz polling. Os eventos committed são empurrados ao Handler do subscritor. A
// costura catch-up→live garante que a história anterior à subscrição também é
// entregue, sem saltos nem buracos (ver "Costura catch-up→live").
//
// # Cursor durável + ACK (at-least-once)
//
// Cada subscrição NOMEADA (SubConfig.Name) tem um cursor durável por stream: o
// último seq CONFIRMADO (ACK) pelo consumidor. O Handler confirma explicitamente
// via Delivery.Ack; só então o cursor avança de forma durável (CursorStore). No
// reinício, a subscrição RETOMA de cursor+1 — 0 eventos saltados.
//
// Contrato AT-LEAST-ONCE: um evento entregue mas NÃO confirmado (o consumidor
// caiu, ou o Handler não chamou Ack) é RE-ENTREGUE após reinício. O consumidor
// DEVE ser idempotente (deduplicar por (stream_id, seq) ou por idempotency_key
// do envelope). O cursor avança de forma CONTÍGUA: só ultrapassa o seq N quando
// todos os seq <= N estão confirmados, pelo que um buraco de ACK nunca é
// silenciosamente saltado.
//
// # Costura catch-up→live (sem saltos nem buracos)
//
// Ao subscrever, o barramento (1) liga PRIMEIRO a subscrição live (que passa a
// bufferizar os eventos que cheguem), (2) lê do Event Store a história a partir
// de start (cursor+1, ou FromSeq no replay) até à cabeça, (3) entrega a
// história e (4) drena o buffer live DEDUPLICANDO por (stream_id, seq) na
// fronteira. Como a subscrição live é registada ANTES da leitura histórica,
// qualquer evento committed durante a transição está no buffer; a sobreposição
// é deduplicada por marca de água (watermark) por stream. Resultado: 0 skips e
// ordem de seq monotónica por stream.
//
// # Replay
//
// SubConfig.FromSeq (arbitrário) reprocessa a partir desse seq: história do
// Event Store desse ponto + live, com a mesma costura. Ignora o cursor guardado
// para efeitos de posição de arranque.
//
// # Âmbito por stream (catch-up, cursor durável e replay)
//
// A leitura histórica do Event Store é POR STREAM (Read(stream, fromSeq)) e o seq
// é atribuído por stream; não há índice global por type/producer nem enumeração
// de streams. Por isso as garantias de catch-up histórico, cursor durável (retoma
// 0-skips) e replay aplicam-se APENAS a subscrições stream-scoped (Filter.Streams
// não vazio) — é uma limitação intencional do modelo de referência.
//
// Uma subscrição só por type e/ou producer (sem Streams) é um padrão de fan-out
// VÁLIDO, mas recebe apenas LIVE a partir do instante da subscrição: não faz
// catch-up da história anterior nem retoma por cursor após reinício. Para não
// prometer silenciosamente o que não pode cumprir, Subscribe FALHA-RÁPIDO com
// ErrConfig quando é pedido replay (FromSeq != nil) sem Streams. Consumidores que
// precisem de retoma durável ou replay devem enumerar os streams no filtro.
//
// # Backpressure (política DECLARADA)
//
// O buffer live é LIMITADO por subscritor (SubConfig.Buffer). Quando enche, a
// OverflowPolicy declarada decide:
//
//   - Block: a intake DESTE subscritor espera por espaço. Como a intake corre na
//     goroutine da subscrição live do Event Store (uma por subscritor), bloquear
//     não afecta o produtor (o Append devolve após enfileirar, O(1)) nem os
//     outros subscritores. Preserva ordem e at-least-once.
//   - DropOldest: descarta o evento mais antigo do buffer para dar entrada ao
//     novo (sheds load). O descartado é uma PERDA deliberada: fica registado como
//     buraco conhecido e o cursor AVANÇA para além dele (não é re-entregue no
//     reinício). É um tradeoff de DURABILIDADE (o evento perde-se), não uma
//     degradação de liveness — sem esta marca o cursor ficaria preso no primeiro
//     descarte e o tracker de acks cresceria sem limite.
//   - DeadLetter: encaminha o evento em excesso para a dead-letter queue
//     (inspecionável), sem bloquear. Tal como no dead-letter por falha de Handler,
//     o cursor avança para além do evento (que fica capturado na fila).
//
// Em nenhuma política um consumidor lento bloqueia o produtor ou outros
// subscritores.
//
// Nota sobre o tracker de acks: um evento entregue mas NÃO confirmado (nem Ack
// nem Nack) NÃO é um buraco resolvido — mantém o piso do cursor preso e as
// confirmações fora de ordem acima dele acumulam-se até o reinício o re-entregar
// (contrato at-least-once). Só as resoluções (Ack, dead-letter, DropOldest)
// fecham buracos e compactam o tracker.
//
// # Dead-letter
//
// Um Handler que falha repetidamente (Delivery.Nack mais do que SubConfig.Retry
// vezes) manda o evento para a dead-letter queue (Bus.DeadLetter, inspecionável)
// e o cursor avança para além dele — a subscrição não fica presa.
//
// # Métricas de latência
//
// Bus.Metrics expõe, além de Avg/Max, percentis de latência de entrega
// (P50/P95/P99) sobre uma janela recente de amostras (reservatório circular
// limitado). Assim o alvo p95 (< 250 ms) fica OBSERVÁVEL em produção via
// Bus.Metrics(), e não apenas verificável dentro de um teste. Sem SDK OTel
// (EPIC-08).
//
// # Modelo de referência
//
// Determinístico e in-process, ZERO dependências externas. O CursorStore é
// plugável: MemoryCursorStore (referência) e SnapshotCursorStore (variante que
// espelha cada escrita para um sink durável). Em produção o cursor vive num
// store persistente (ex.: consumer durável de NATS JetStream / KV) e o barramento
// assenta sobre o Event Store de produção.
package bus
