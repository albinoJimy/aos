// Package registry implementa o Skill/Tool Registry (REG) do AOS (AOS-045,
// EPIC-05, ADR-012/ADR-002/ADR-007): o catálogo APPEND-ONLY e VERSIONADO de três
// tipos de artefacto — skill, tool e servidor MCP — que transforma "coerência por
// contrato" numa fronteira concreta. É a fundação sobre a qual assentam os controlos
// de supply-chain seguintes (pin+hash, assinatura, TOFU, congelamento por run).
//
// # Fonte de verdade
//
// O catálogo NÃO tem estado autoritativo em RAM nem usa um single-writer SQLite: é
// uma projecção DETERMINÍSTICA do Event Store replicado (AOS-002, ADR-007). Cada
// publicação/transição é um evento append-only; o estado corrente reconstrói-se por
// replay (ver Registry.snapshot / foldProjection). A imutabilidade é estrutural —
// publicar uma (id, version) já existente é recusado (ErrVersionExists); uma
// alteração produz sempre uma NOVA versão.
//
// # Modelo de domínio (subpacote domain)
//
// Cada [domain.Entry] expõe os campos essenciais (tecnica/05 §3): id + version
// (SemVer), digest, signature, contract (schema de I/O, scopes de credencial, classe
// de egress), provenance (origem, publicador, timestamp, estado de confiança TOFU) e
// status. Os três tipos são [domain.ArtifactKind] (skill/tool/mcp_server),
// distinguíveis e parte do conteúdo canonicalizado do digest.
//
// # Ciclo de vida (fail-closed)
//
// staging → active → deprecated/revoked. A publicação entra SEMPRE em staging;
// NENHUM artefacto salta directamente para active. A única aresta para active a
// partir de staging atravessa o [AdmissionVerifier] — o ponto de extensão onde
// AOS-047 (hash), AOS-048 (assinatura) e AOS-053 (eval-gate) imporão a verificação.
// A máquina de estados ([domain.CanTransition]) recusa qualquer transição não
// enumerada.
//
// # API mínima
//
//   - [Registry.Publish]     — admite em staging (nunca active).
//   - [Registry.Resolve]     — devolve a entrada por versão PINADA exacta (nunca latest).
//   - [Registry.ResolveString] — rejeita referências flutuantes (latest/main/…).
//   - [Registry.GetDigest]   — devolve o digest esperado para o Reference Monitor.
//   - [Registry.SetStatus]   — transição de estado válida (com gate na promoção).
//   - [Registry.IsAdmissible]— DEFAULT-DENY (ADR-002): despachável só se no catálogo E active.
//
// # Pontos de extensão reservados
//
// O campo digest é derivado por um [domain.Digester] injectável; o default
// ([domain.PlaceholderDigester]) é determinista mas NÃO criptográfico (prefixo
// "placeholder-fnv1a:") — o SHA-256 sobre o conteúdo canónico é AOS-047. O campo
// signature é reservado (verificação em AOS-048). O estado de confiança TOFU
// (first_seen→pinned→changed) é reservado na proveniência (detecção em AOS-049).
//
// # Determinismo e observabilidade
//
// Relógio injectável ([WithClock]); serialização e ordenação estáveis; sem time.Now
// nem rand numa decisão. As operações de consulta emitem spans OTel GenAI via a porta
// [agentruntime.Tracer] zero-dep — sem segredos (id/version/digest são públicos; os
// valores de credencial nunca são expostos).
package registry
