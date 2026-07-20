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
// content-blind) é discriminada por TestPromptInjection_ProvenanceLaunderingResisted. A
// OFUSCAÇÃO por symlink / path-traversal (AC1, a par de base64/metacaracteres) é coberta
// por TestPromptInjection_SymlinkTraversal_Blocked: um recurso de aparência benigna que
// resolve (deref de symlink + travessia .,..) para um alvo sensível é NEGADO na mesma —
// a ofuscação do recurso é irrelevante para o gate (decide por taint, não por caminho); o
// meta-teste TestMetaDetects_SymlinkObfuscation_WhenPathNotResolved prova que a
// de-ofuscação de caminho é mesmo necessária (o caminho bruto esconde o alvo).
//
// NOTA sobre "mediação" nos cenários 5–7: a mediação faz-se pelo PEP APROPRIADO AO
// DOMÍNIO — ingestão de memória (provenance.Ingestor/Partition), verificação de mensagem
// (messaging.Verifier) e re-validação de schema (tofu.Monitor) — porque essas superfícies
// NÃO são tool calls. A mediação pelo Reference Monitor (Monitor.Mediate) é a superfície
// das TOOL CALLS (cenários 1 e 8). Em qualquer caso, cada tentativa é mediada por um
// controlo de produção REAL e selada tamper-evident.
//
// # Extensão AOS-117 (EPIC-11) — quatro cenários adversariais adicionais
//
// A suite é ESTENDIDA (nunca alterada) por AOS-117 com mais quatro cenários, cada um a
// COMPOR primitivos REAIS e com o seu META-TESTE de detecção não-vácua:
//
//  5. MEMORY POISONING (AOS-042) — memória de origem untrusted (tool result / web /
//     schema MCP / derivada) é admitida na Quarentena (data-plane), NUNCA na TrustedView;
//     a proveniência é selada e imutável; um Seal de trusted forjado é recusado
//     (ErrSealTrustedForbidden); e a barreira de TIPO impede a escalada — um DataItem em
//     quarentena não implementa PrivilegedAuthorizer (item.AuthorizeToolCall nem compila).
//  6. HALLUCINATION GATE (AOS-073) — uma mensagem inter-agente com origem FORJADA
//     (assinatura vs chave pinada), autoridade NÃO coberta, referência INAUTÊNTICA ou
//     replay é rejeitada fail-closed e selada no WORM; uma mensagem legítima passa.
//  7. RE-APROVAÇÃO DE SCHEMA MCP (AOS-049) — schema drift do manifesto (rug-pull) é
//     detectado e bloqueado (ErrSchemaDrift); a re-aprovação in-band na MESMA versão
//     SemVer é recusada (ErrInBandReapproval); só uma nova versão recupera a confiança.
//  8. CENÁRIO 1 REFORÇADO COM O PDP REAL (AOS-113) — o PDP (bundle Cedar assinado +
//     allowlist default-deny) COMO hook "policy", com o TaintGate como defesa-em-
//     profundidade: assere QUAL a camada nega (allowlist → DeniedBy "policy"; taint →
//     DeniedBy "taint").
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
