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
// # Adapter OTLP real — FECHADO em AOS-173 (correcção documental de AOS-274)
//
// Este parágrafo dizia «DIFERIDO». Já não é verdade, e a correcção é o AC3 de
// AOS-274: um TODO que sobrevive à sua própria resolução leva o leitor seguinte a
// escrever de novo o que já existe — ou a concluir que o nó não exporta nada.
//
// O adapter REAL existe e está LIGADO: `packages/cmd/aos/otlpexporter.go`
// ([OTLPHTTPExporter]) implementa a porta [Exporter] fazendo POST de OTLP/JSON a um
// colector OTLP/HTTP com **apenas net/http + o [MarshalOTLP] deste pacote** — sem o
// SDK go.opentelemetry.io e sem uma única dependência externa, pelo que a regra de
// build offline/zero-dep do monorepo (ADR-017) e o baseline SCA/govulncheck ficam
// intactos. É composto no composition root do nó e injectado no RT/RM pela via já
// prevista (agentruntime.WithTracer / referencemonitor.WithTracer), sem tocar no
// núcleo. A exportação é fail-open (a telemetria nunca derruba o nó); um endpoint
// malformado aborta o ARRANQUE (fail-closed de config).
//
// O que continua diferido — e é outra coisa — é um adapter sobre o SDK oficial
// (pdata/ptrace, OTLP-gRPC). Só faria sentido num deployment que exija gRPC ou os
// processadores do SDK, e continuaria a ser um módulo SEPARADO pela mesma razão de
// sempre: as dependências externas não entram no binário do nó.
//
// O [SpanTracer] + [RecordingExporter] + [MarshalOTLP] mantêm-se como a prova
// in-process da árvore de spans e do wire format ponta-a-ponta.
//
// # Consumidor em RUNTIME dos SLIs/alertas (AOS-274)
//
// [BuildDashboard]/[EvaluateAlerts] (AOS-085/086) e [DashboardCatalog.Render]/
// [EvaluateOperationalAlerts] (AOS-104/105) já não são só superfície de teste: o nó
// corre-os periodicamente no seu loop de serviço (`packages/cmd/aos/slo_evaluator.go`)
// sobre os MESMOS spans que saem para o colector, e liga cada alerta ao registo de
// runbooks de AOS-106. A regra ANTI-VACUIDADE deste pacote (Samples == 0 ⇒ nem breach
// nem cumprimento afirmado) é o que mantém honesto um nó cujos produtores não estão
// todos ligados — nenhum valor é injectado para preencher um painel.
//
// # Segredos
//
// Nenhum valor de payload entra num span: o execute_tool regista o nome da tool e
// o HASH sha256(tool+args), nunca os args (content-capture por referência; o
// payload/replay é AOS-079). O rótulo de taint é a etiqueta de confiança, não o
// conteúdo (ADR-005/ADR-006).
package otelgenai
