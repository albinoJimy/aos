// Package compliance é o capstone do EPIC-09 (AOS-097): o MODELO DE
// RESPONSABILIZAÇÃO e os RELATÓRIOS DE CONFORMIDADE do AOS.
//
// # Modelo de responsabilização (AC1)
//
// Toda a acção do sistema tem um PRINCIPAL COMPLETO — a cadeia de delegação
// on-behalf-of INTEIRA — rastreável até um HUMANO responsável. NÃO existem
// execuções anónimas. O [AccountabilityVerifier] é o verificador de COMPLETUDE
// sobre um conjunto de [audit.AuditRecord]: cada registo classificado como ACÇÃO
// (mediação de tool call) tem de ter um Principal com [audit.Principal.DelegationChain]
// não-vazia, contígua e terminada numa RAIZ HUMANA (prefixo "human:"). Um registo
// sem principal completo — cadeia vazia, raiz não-humana, elo órfão — é DETECTADO
// como acção anónima e sinalizado FAIL-CLOSED (ADR-003).
//
// # Relatórios de conformidade (AC3)
//
// [GenerateReport] produz um [ComplianceReport] como PROJECÇÃO query-time sobre os
// AuditRecords do Store — SEM duplicar dados (o mesmo padrão wide events de
// AOS-082, aqui sobre o audit WORM). Cada secção é uma agregação sobre os registos
// selados:
//   - Attribution  — atribuição de acções ao humano responsável (principal→humano);
//   - PDPDecisions — decisões PDP mediadas (permit/deny/escalate);
//   - HITL         — aprovações HITL + override-rate (AOS-095);
//   - DSARs        — pedidos DSAR/crypto-shredding (AOS-093);
//   - Sovereignty  — eventos de soberania por região (AOS-094);
//   - Anomalies    — acções ANÓNIMAS detectadas (AC1; vazio ⇒ sem anonimato).
//
// # Integridade (AC4)
//
// O relatório é DERIVADO do audit tamper-evident e a sua integridade é verificável:
// [GenerateReport] corre [audit.Verify] sobre o intervalo de que deriva ANTES de
// projectar. Se a cadeia estiver adulterada, NÃO se gera relatório (erro). O
// relatório carrega a prova de integridade ([IntegrityProof]: range + EntryHash do
// head verificado).
//
// # Ausência de PII (AC5)
//
// O relatório usa SÓ os campos não-pessoais dos AuditRecords, já redigidos/
// tokenizados na ingestão (ADR-011). NUNCA decifra o payload pessoal ([audit.PayloadRef]
// não é lido). Um titular shredded aparece como REFERÊNCIA (o subjectID pseudónimo),
// nunca como PII em claro.
package compliance
