// Package provenance implementa a PROVENIÊNCIA e a QUARENTENA de memória derivada
// de conteúdo untrusted (AOS-042), a fronteira de segurança que mitiga o memory
// poisoning persistente (ASI06) e o vector nº1 de prompt injection (OWASP LLM01),
// aplicando o Princípio 5 do _FONTE e o ADR-005: conteúdo untrusted é DADOS, nunca
// instruções.
//
// # O que este pacote impõe (assente no metadado obrigatório de AOS-035)
//
//   - PROVENIÊNCIA OBRIGATÓRIA E IMUTÁVEL: toda a escrita que atravessa esta
//     camada carrega proveniência (fonte, classificação trusted|untrusted, run_id).
//     A proveniência é SELADA no acto de ingestão ([Ingested]) — o campo é não
//     exportado e não há mutador —, pelo que não pode ser alterada após a escrita.
//     Uma escrita sem proveniência canónica é REJEITADA (fail-closed, [Seal]).
//
//   - MARCAÇÃO AUTOMÁTICA DE UNTRUSTED NA INGESTÃO: a classificação é feita pela
//     FONTE, estruturalmente ([Classify]) — nunca por uma tag in-band que o próprio
//     conteúdo carregue (tags não são separação de privilégio). Tool results, web e
//     schemas MCP são sempre untrusted; só system/utilizador-autenticado é trusted;
//     uma fonte desconhecida cai em untrusted (lado seguro).
//
//   - TAINT TRANSITIVO (sem lavagem): memória derivada de memória untrusted HERDA
//     untrusted; uma derivação que mistura trusted+untrusted resulta untrusted — o
//     taint é contagioso ([TaintController.Derive]). Não há caminho pelo qual
//     conteúdo untrusted "lave" o seu estatuto ao passar pela memória.
//
//   - BARREIRA ESTRUTURAL control-plane / data-plane: a separação é a NÍVEL DE
//     TIPO/CAMINHO, à imagem da barreira read-only de AOS-036. O planeador consome
//     EXCLUSIVAMENTE uma [TrustedView] — um tipo que só expõe memória trusted. A
//     memória em quarentena ([Quarantine]) é servida ao modelo como [DataItem]:
//     dados taint-marcados, sem QUALQUER método que autorize uma acção privilegiada.
//     Só a memória trusted ([TrustedEntry]) satisfaz [PrivilegedAuthorizer]; um
//     [DataItem] NÃO o satisfaz (asserção falha; a chamada nem sequer compila).
//
//   - PROMOÇÃO AUDITÁVEL untrusted → trusted: exige validação EXPLÍCITA (política
//     ou humano) e é registada na hash-chain tamper-evident de AOS-011 ([Promoter]).
//     Não há promoção silenciosa nem automática; a promoção cria um registo trusted
//     NOVO (o original untrusted permanece imutável, coerente com o event-sourcing).
//
// # Integração com EPIC-07 (por PORTA, não reimplementada)
//
// O mecanismo real de taint control/data-plane (SBX / dual-LLM / CaMeL) é EPIC-07 e
// NÃO é reimplementado aqui. Este pacote depende apenas de duas interfaces —
// [TaintController] e [DataPlane] — e fornece implementações de REFERÊNCIA
// ([DefaultTaintController], [ReferenceDataPlane]); EPIC-07 fornecerá as de
// produção sem alterar esta camada.
//
// Determinismo: sem time.Now/rand no caminho de decisão (relógio injectável no
// [Promoter]); serialização estável herdada do audit. Observabilidade via a porta
// Tracer zero-dep do Agent Runtime; sem segredos nos spans.
package provenance
