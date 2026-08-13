// Package plan define o CONTRATO do PlanDocument (AOS-230, EPIC-19, tecnica/18
// §3.3): o artefacto declarativo de schema FECHADO que o LLM propõe e o sistema
// valida — o organigrama de um meta-run antes de qualquer spawn.
//
// FRONTEIRA DE CONFIANÇA (ADR-005). O PlanDocument é DADOS *untrusted*: nunca é
// interpretado como instrução de execução. Este pacote apenas o DEFINE e o
// DESSERIALIZA fail-closed — não valida grafo (aciclicidade, resolução de tools,
// tectos: AOS-231), não deriva risco nem re-preça orçamento (AOS-232), não faz
// I/O, rede ou execução. A desserialização espelha a disciplina de config-loading
// do nó: [json.Decoder.DisallowUnknownFields] — um campo desconhecido é REJEITADO,
// nunca ignorado em silêncio, apanhando drift de schema em vez de o mascarar.
//
// ZERO dependências fora da stdlib. O pacote é o núcleo estável sobre o qual
// AOS-231/232/235 constroem o validador puro, o risco derivado e o domínio de
// eventos `aos.planner.v1`.
//
// VERSIONAMENTO. O contrato carrega o seu próprio [PlanVersion] SemVer (§3.6):
// MAJOR = quebra, MINOR = aditivo retrocompatível, PATCH = clarificação. Um plano
// materializa-se na versão em que foi aprovado — nunca auto-migrado (fail-closed). A
// linha corrente e o que cada MINOR alargou estão em [CurrentPlanVersion]; a JANELA
// DE SUPORTE dos MAJORs com reader retido é `planmigrate.SupportWindow`, documentada
// em `tecnica/18` §3.6.1.
//
// ARESTAS CONDICIONAIS (ADR-022 §2.1, AOS-270). O `Node` admite `conditional_on`:
// arestas guardadas por uma expressão de um SUBCONJUNTO FECHADO (a gramática vive
// em condition.go, com o argumento de fecho explícito). O campo é OPCIONAL e
// ADITIVO — um documento sem ele decodifica e comporta-se exactamente como antes —,
// pelo que a extensão é retrocompatível e NÃO consome um MAJOR de [PlanVersion]: o
// carimbo é o MINOR 1.1.0 ([CurrentPlanVersion]). Este pacote mantém-se compatível
// com a linha corrente em ambas as direcções: um leitor novo lê documentos antigos,
// e um documento novo SEM condições é indistinguível de um antigo. Nota honesta
// sobre a direcção inversa: por `DisallowUnknownFields`, um leitor ANTIGO recusa um
// documento que USE `conditional_on` — é o comportamento fail-closed desejado (nunca
// ignorar um guarda que não se sabe avaliar), e é exactamente a razão por que
// alargar o schema OBRIGA a bump de MINOR.
//
// PAYLOAD TIPADO POR ARESTA (ADR-022 §2.3, AOS-272). O `Node` admite `outputs` (o
// que produz: nome, schema, taint) e `consumes` (as arestas de dados que lê: origem,
// output, tipo esperado) — a gramática vive em payload.go, com a derivação do taint
// declarada. Ambos OPCIONAIS e ADITIVOS, pela mesma razão e com a mesma consequência
// de `conditional_on`: um documento sem eles é indistinguível de um pré-ADR-022, logo
// a extensão NÃO consome um MAJOR de [PlanVersion] — carimba-se, com a reserva do
// literal `verifier` de §2.2, no MINOR 1.2.0 (AOS-273). Duas decisões desta extensão
// vivem aqui e não no validador porque são do CONTRATO: o `consumes` é declarado no
// extremo CONSUMIDOR e nomeia a origem — o que evita alargar `depends_on` de
// `[]string` para uma lista de objectos, que seria uma quebra de forma; e o `taint` é
// ADVISORY que SÓ ELEVA o piso DERIVADO DO TIPO, exactamente como `risk_class` só
// eleva o piso derivado das tools.
//
// risk_class É ADVISORY. O campo `risk_class` por nó é uma PROPOSTA do LLM. O
// contrato documenta-o (ver [RiskClass]) como advisory que só pode ELEVAR o piso
// de risco DERIVADO das ferramentas pinadas em AOS-232 — NUNCA baixá-lo. Este
// pacote transporta e valida a forma do rótulo; a derivação e o "só eleva" são
// enforcement de AOS-232.
package plan
