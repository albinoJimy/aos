// Package signing implementa o pilar ASSINATURA da tríade de supply-chain do
// Skill/Tool Registry (REG) — AOS-048, EPIC-05, ADR-012/ADR-006. Pin e hash
// (AOS-047) provam INTEGRIDADE (o conteúdo não mudou); a assinatura autentica a
// ORIGEM (quem publicou). É a peça que fecha a porta ao RUG-PULL: mesmo que um
// atacante recalcule um digest coerente sobre conteúdo adulterado, não consegue
// assiná-lo com a chave de confiança do publicador legítimo.
//
// # O que este pacote entrega (escopo estrito AOS-048)
//
//   - [SigningInput]/[Sign]/[Verify]: assinatura Ed25519 (crypto/ed25519 stdlib,
//     ZERO deps externas) sobre o tuplo canónico (id, version, digest), com
//     serialização determinista e domain separation por comprimento.
//   - [TrustStore]: um store de chaves PÚBLICAS de publicadores confiáveis,
//     GERÍVEL (Add/Revoke) e AUDITÁVEL (cada mudança sela-se no audit hash-chain
//     WORM de AOS-011). NUNCA guarda chaves privadas. Uma chave revogada deixa
//     imediatamente de validar.
//   - [AdmissionVerifier]: o verificador de admissão FAIL-CLOSED que concretiza a
//     porta registry.AdmissionVerifier (placeholder de AOS-045). Antes de qualquer
//     artefacto passar de staging a active verifica: (a) signature PRESENTE, (b)
//     VÁLIDA sobre (id, version, digest), (c) de uma chave CONFIÁVEL e não-revogada.
//     Falha em qualquer condição RECUSA a promoção (o artefacto fica em staging).
//     Cada decisão (aceite/recusada) sela-se no audit WORM com id, version, digest
//     e resultado.
//
// # Invariante dos scopes de credencial (ADR-006)
//
// Os CredentialScopes declarados no contract são os ÚNICOS que o broker (BRK,
// EPIC-06 — aqui apenas a porta/invariante) aceitará conceder à tool; o agente
// NUNCA vê o segredo. Como a assinatura cobre o digest, e o digest (AOS-047) cobre
// o contrato — logo os credential scopes —, os scopes autorizados ficam
// CRIPTOGRAFICAMENTE ligados à assinatura do publicador. [Result.AuthorizedScopes]
// expõe essa declaração (nunca um segredo) para o ponto de extensão do broker.
//
// # Reutilização a jusante (AOS-051)
//
// [Verifier.VerifyEntry] é a API reutilizável de verificação de assinatura: a
// revalidação criptográfica por chamada (AOS-051) reutiliza-a para recusar em
// runtime um artefacto cuja assinatura deixe de validar (chave revogada, digest
// divergente). NÃO se implementa aqui AOS-049/050/051/053.
//
// # Determinismo e segredos
//
// A serialização é canónica e pura (sem time.Now/rand numa decisão); Ed25519 é
// determinístico. As chaves são injectáveis nos testes; o relógio é injectável
// (só para timestamps de audit). NENHUM segredo entra em código/logs/spans: o
// TrustStore guarda apenas chaves públicas; as chaves privadas vivem fora do REG.
package signing
