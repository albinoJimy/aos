// Package memory implementa o modelo de domínio e a fachada do Memory Service
// (MEM) do AOS (AOS-035, EPIC-04), a fundação sobre a qual assentam os restantes
// tickets de memória e persistência.
//
// # As quatro classes
//
// A memória do agente NÃO é um único armazém de contexto: são quatro classes
// distintas, cada uma com identidade, esquema TIPADO (domain.Body) e ciclo de
// vida próprio (ver domain.MemoryClass):
//
//   - episódica (domain.ClassEpisodic) — trajectórias de execução passadas;
//   - semântica (domain.ClassSemantic) — base de conhecimento factual;
//   - procedural (domain.ClassProcedural) — skills/heurísticas aprendidas;
//   - de trabalho (domain.ClassWorking) — contexto activo do turno.
//
// A classe faz parte da identidade de um registo e escopa toda a operação de
// porta — as quatro classes não se cruzam (uma leitura semântica nunca devolve um
// registo episódico).
//
// # Porta versionada
//
// O serviço expõe-se pela porta ports.MemoryPort (SemVer, ports.PortVersion),
// com CRUD/query por classe, sem vazar o backend ao chamador. Existem dois
// adaptadores que passam a MESMA suite de contrato — a prova do backend-swap por
// configuração:
//
//   - adapters.EventStoreAdapter — FONTE DE VERDADE (ADR-007): escreve eventos
//     append-only e reconstrói a leitura por replay do log; sem single-writer;
//   - adapters.InMemoryAdapter — backend de teste com semântica observável
//     idêntica.
//
// # Metadados obrigatórios (fail-closed)
//
// Todo o registo carrega metadados obrigatórios (domain.Metadata): agent_id,
// run_id, provenance (trusted|untrusted), created_at, ttl_class e schema_version.
// A ausência de qualquer um FALHA-FECHA na validação; em particular, escrever sem
// provenance OU sem schema_version é sempre rejeitado, nunca um default
// silencioso. A proveniência prepara a quarentena (AOS-042) e o ttl_class prepara
// o TTL/GDPR (ADR-011); nenhum desses mecanismos é implementado aqui — só o
// modelo e os metadados.
//
// # Observabilidade e determinismo
//
// Cada operação de porta emite um span OTel (gen_ai.*) via a porta Tracer
// zero-dep do Agent Runtime. O relógio (created_at) e o gerador de IDs são
// injectáveis na fachada; não há time.Now nem rand no caminho de decisão, e a
// serialização é estável.
//
// # Projecção contexto ≠ registo (AOS-036)
//
// O Princípio 4 (contexto ≠ registo) materializa-se em DUAS vias fisicamente
// separadas, em subpacotes próprios, que nunca partilham o caminho de descarte:
//
//   - record — a via de REGISTO: record.TrajectoryRecord (append-only) e
//     record.Persist emitem a trajectória completa como ÁRVORE DE SPANS para o Tracer
//     injectado — manifesto por turno e o COMPRIMENTO do cru como atributo, nunca o
//     cru em si. A durabilidade do conteúdo cru é do sink a montante (EPIC-08), e com
//     o Tracer por omissão (NoopTracer) o registo NÃO sai deste processo;
//   - projection — a via de CONTEXTO: projection.ProjectContext produz o que o
//     modelo vê (resumo higienizado, limitado em tokens, política SemVer,
//     determinística).
//
// A barreira é a NÍVEL DE TIPO: a projecção recebe apenas record.RecordView (vista
// read-only, sem mutadores), pelo que é impossível — por compilação — apagar/mutar
// o registo a partir da projecção. O manifesto por turno (hash do prompt
// materializado, model-id/params, versões) é gravado independentemente da higiene.
//
// Fora de âmbito (tickets seguintes): janela de trabalho (AOS-037), classes
// concretas (AOS-038/039/040), migrações de schema (AOS-041), quarentena untrusted
// (AOS-042), compressão assíncrona (AOS-043).
package memory
