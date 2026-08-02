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
// materializa-se na versão em que foi aprovado — nunca auto-migrado (fail-closed).
//
// risk_class É ADVISORY. O campo `risk_class` por nó é uma PROPOSTA do LLM. O
// contrato documenta-o (ver [RiskClass]) como advisory que só pode ELEVAR o piso
// de risco DERIVADO das ferramentas pinadas em AOS-232 — NUNCA baixá-lo. Este
// pacote transporta e valida a forma do rótulo; a derivação e o "só eleva" são
// enforcement de AOS-232.
package plan
