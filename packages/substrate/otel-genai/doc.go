// Package otelgenai é a camada de instrumentação OpenTelemetry GenAI semconv
// PARTILHADA do AOS (AOS-076). É um módulo FOLHA do substrato (zero dependências
// internas e externas — só stdlib), pelo que tanto o Reference Monitor como o
// Agent Runtime a podem importar sem criar ciclos (ambos já importam
// substrate/eventstore; o RM não pode importar o RT).
//
// # Porquê aqui
//
// A trajectória completa de cada agente e sub-agente é persistida como uma árvore
// de spans em OTel GenAI semconv (ADR-010, tecnica/08 §3): cada nível de
// delegação abre um span invoke_agent; cada turno de modelo, um span chat; cada
// tool call mediada pelo Reference Monitor, um span execute_tool. Adoptar a
// semconv como wire format evita lock-in: qualquer backend compatível com OTel
// consome os mesmos dados. Este pacote é a fonte única do vocabulário
// (ver semconv.go) e da mecânica de spans/propagação/exportação sobre a qual RT e
// RM instrumentam.
//
// # Peças
//
//   - Vocabulário (semconv.go): as constantes gen_ai.*/error.type/aos.* e os nomes
//     de operação. O adaptador OTel real mapeia estas strings para attribute.Key/
//     instrument.Name SEM renomear.
//   - Propagação (spancontext.go): [SpanContext] (trace_id/span_id, hex W3C/OTLP) e
//     [ContextWithSpanContext]/[SpanContextFromContext] para propagar via
//     context.Context. É assim que chat/execute_tool se ligam ao invoke_agent
//     partilhando o trace_id e apontando o parent_span_id.
//   - Identidade (idgen.go): [IDGenerator] injectável — [CryptoIDGenerator] sobre
//     crypto/rand em produção, [SequentialIDGenerator] determinista em teste.
//   - Portas (tracer.go): [Tracer]/[Span] e o [NoopTracer] default (sem overhead).
//   - Exportação (exporter.go, spantracer.go, otlp.go): [SpanData]/[Exporter], o
//     [SpanTracer] concreto que exporta ao End() de cada span, o [RecordingExporter]
//     in-memory, e [MarshalOTLP] (ResourceSpans→ScopeSpans→Span com ids em HEX).
//   - Captura de teste (recording.go): [RecordingTracer]/[RecordedSpan], o duplo
//     leve que RT/RM usam nos testes (atributos + topologia, sem exportador).
//   - Contrato (contract.go): a tabela requiredAttrs por operação e
//     [ValidateSpanData], o validador de conformidade semconv.
//
// # Adapter OTLP real — DIFERIDO (TODO de wiring de deployment)
//
// O exportador OTLP-gRPC/HTTP REAL (sobre o SDK go.opentelemetry.io/otel e
// go.opentelemetry.io/otel/exporters/otlp/...) é um ADAPTER DE DEPLOYMENT
// deliberadamente DIFERIDO: implementá-lo aqui puxaria dependências externas e
// partiria a regra de build offline/zero-dep do monorepo (e o baseline
// SCA/govulncheck). O caminho de adopção, quando um deployment o exigir, é um
// módulo/adapter SEPARADO que:
//
//  1. implementa a porta [Exporter] traduzindo [SpanData] → pdata/ptrace (o
//     [MarshalOTLP] deste pacote já dá a forma OTLP/JSON de referência), e/ou
//     implementa a porta [Tracer] sobre trace.Tracer do SDK;
//  2. mapeia as constantes de semconv.go para attribute.Key sem renomear;
//  3. é injectado no RT (agentruntime.WithTracer) e no RM
//     (referencemonitor.WithTracer) no composition root — sem tocar no núcleo.
//
// Até lá, o [SpanTracer] + [RecordingExporter] + [MarshalOTLP] provam a árvore de
// spans e o wire format ponta-a-ponta, mantendo o build offline.
//
// # Segredos
//
// Nenhum valor de payload entra num span: o execute_tool regista o nome da tool e
// o HASH sha256(tool+args), nunca os args (content-capture por referência; o
// payload/replay é AOS-079). O rótulo de taint é a etiqueta de confiança, não o
// conteúdo (ADR-005/ADR-006).
package otelgenai
