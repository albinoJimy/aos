// Package securitytests é a SUITE ADVERSARIAL DE SEGURANÇA do AOS (AOS-075) — o
// ÚLTIMO ticket do EPIC-07 e o safety net de QA da fronteira de segurança inteira.
//
// É DELIBERADAMENTE um módulo SÓ-DE-TESTES: exceto este ficheiro de documentação
// (que fixa apenas a versão da suite), tudo são ficheiros _test.go. NÃO reimplementa
// nenhum controlo — ORQUESTRA os controlos REAIS já implementados (AOS-066..070) e
// prova, por adversário, que os vectores prioritários da fronteira de segurança —
// prompt injection (OWASP LLM01 / ASI01) e exfiltração via tools "benignas" (padrão
// CamoLeak, CVSS 9.6) — são REPRODUZIDOS e BLOQUEADOS. "Uma defesa só existe se for
// provada por adversário."
//
// # Os quatro cenários (cada um provado BLOQUEADO + um META-TESTE de detecção não-vácua)
//
//  1. PROMPT INJECTION (AOS-069) — conteúdo untrusted injectado em tool result / web /
//     memória NÃO origina acção privilegiada: o TaintGate do Reference Monitor NEGA a
//     tool call privilegiada cuja autorização é untrusted (ADR-005). Bateria do corpus
//     versionado → 100% bloqueado.
//  2. EXFILTRAÇÃO (AOS-067/068) — egress fora da allowlist (EgressFilter DENY), DNS
//     tunneling / domínio fora da allowlist (DNSFilter DENY) e tool "benigna" com tipo
//     de recurso mislabelado (EgressHook DENY fail-closed) — todos negados E selados no
//     audit WORM tamper-evident (AOS-072).
//  3. SEGREDOS (AOS-070) — o valor do segredo downstream NUNCA aparece no output da
//     troca, nos portadores server-side, nos spans nem no Event Store (scan de sentinela).
//  4. ISOLAMENTO (AOS-066) — o overlay efémero não persiste (execução N+1 não observa a
//     escrita de N, no caminho de execução REAL mediado); uma syscall fora do perfil
//     seccomp é bloqueada default-deny; e a fronteira sem-socket-do-host é imposta
//     fail-closed.
//
// # Meta-testes (o coração da não-vacuidade)
//
// Cada cenário tem pelo menos um TestMetaDetects_* que reproduz o MESMO ataque com o
// controlo CONTORNADO/desligado (TaintGate removido; allowlist a permitir o destino de
// exfil; destino do mislabel tornado derivável; overlay reciclado; perfil seccomp
// permissivo; fronteira do host enfraquecida; scan a incidir sobre vazamentos sintéticos
// verbatim/codificados/fragmentados) e prova que, aí, o ataque PASSA (ou o vazamento é
// detectado, ou a sandbox recusa fail-closed). Se a asserção de bloqueio do cenário fosse
// vácua (sempre verdadeira), o meta-teste falharia — juntos provam que a suite discrimina
// genuinamente, não é green-vazio. A dimensão de PROVENIÊNCIA da prompt injection (laundering
// untrusted→trusted, que a variedade de codificação do corpus não exercita por a defesa ser
// content-blind) é discriminada por TestPromptInjection_ProvenanceLaunderingResisted.
//
// # Corpus versionado e extensível
//
// O corpus de payloads adversariais (testdata/corpus.json) é VERSIONADO (campo version
// selado por [SuiteVersion]) e a harness é TABLE-DRIVEN: acrescentar um vector é
// acrescentar uma entrada ao JSON, sem reescrever a harness ([TestCorpusVersionedAndExtensible]
// prova a extensibilidade e a coerência do corpus).
//
// # Gate CI fail-closed
//
// scripts/ci/security.sh corre a suite como GATE BLOQUEANTE: require_tests exige que
// CADA cenário + cada meta-teste + o relatório tenham EFECTIVAMENTE corrido (não-vazio),
// -race, e ancora ao veredicto agregado do relatório (AOS_SECURITY_REPORT). O self-test
// (scripts/ci/selftest.sh, secção H) corre um teste-veneno que, com um controlo desligado,
// AVERMELHA o gate — prova o fail-closed. É o análogo, para a fronteira de segurança, dos
// gates supplychain (AOS-054) e routing (AOS-063).
package securitytests

// SuiteVersion é a versão da suite adversarial de segurança (AOS-075). Espelha o campo
// "version" do corpus versionado (testdata/corpus.json); [TestCorpusVersionedAndExtensible]
// assere a coerência entre os dois, para que uma alteração ao corpus force um bump
// consciente e o relatório da suite carregue a versão exacta em vigor.
const SuiteVersion = "aos-security/v1"
