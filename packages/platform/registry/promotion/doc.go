// Package promotion concretiza o CICLO DE PUBLICAÇÃO/PROMOÇÃO de skills e tools no
// REG (AOS-053) — o admission control que materializa, ao nível do Registry, o
// fluxo de governação de ADR-012: publicação em staging → verificação de
// integridade → eval-gate (só skills auto-escritas) → active (com SemVer) →
// deprecated/revoked.
//
// COMPOSIÇÃO (não reimplementação). Este pacote COMPÕE as peças já construídas em
// AOS-045..052 em vez de as reimplementar:
//
//   - Registry (AOS-045): o catálogo append-only, a máquina de estados fail-closed
//     e a porta AdmissionVerifier (o único caminho para active). O Pipeline instala
//     a sua própria verificação composta como AdmissionVerifier do Registry, pelo
//     que NENHUMA promoção a active — nem sequer uma chamada directa a SetStatus que
//     ignore o Pipeline — escapa ao gate (fecho estrutural do "salto para active").
//   - Assinatura (AOS-048): a verificação criptográfica de origem (signing.Verifier)
//     é a dimensão de assinatura da integridade, injectada como porta.
//   - Hash (AOS-047): o digest canónico recalculado é a dimensão de hash da
//     integridade.
//   - SemVer (AOS-052): semver.ValidateBump é ligado AQUI (o skip de AOS-052) — uma
//     promoção que mude o contrato tem de trazer o bump correcto; o Lifecycle dá o
//     rollback atómico.
//   - Audit WORM (AOS-011): cada transição do ciclo (published, integrity_verified,
//     eval, ratified, promoted, revoked, rolled_back) sela-se na hash-chain. A
//     INTENÇÃO de cada transição de estado é selada ANTES de o estado ser comprometido
//     na fonte de verdade (um audit indisponível falha antes da mutação, nunca
//     depois), e a confirmação depois — nenhuma transição efectiva fica sem selo.
//
// DISTINÇÃO tools vs skills auto-escritas (estrutural, SÓ por kind): um artefacto de
// terceiros (tool/mcp_server) atravessa apenas verificação de integridade (a confiança
// TOFU é AOS-049). TODA a entrada kind=skill — INDEPENDENTEMENTE da origem declarada —
// é tratada como auto-escrita e atravessa verificação + EVAL-GATE (golden-set +
// trace-diffing vs baseline, porta que reutiliza o harness de EPIC-11) + RATIFICAÇÃO
// HUMANA ASSINADA (ed25519, não-repúdio). O kind está ligado ao material assinado (via
// digest), pelo que a distinção não é forjável; o Provenance.Origin, string livre do
// publicador não coberta pela assinatura, NUNCA isenta uma skill da governação. Uma
// skill que falha o eval-gate ou sem ratificação válida é REJEITADA — nunca chega a
// active.
//
// Determinismo: relógio e chaves são injectáveis; nenhuma decisão usa time.Now/rand.
// Nenhum segredo (chave privada, assinatura em claro) entra em spans ou logs.
package promotion
