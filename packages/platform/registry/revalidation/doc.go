// Package revalidation implementa a REVALIDAÇÃO CRIPTOGRÁFICA POR CHAMADA (AOS-051,
// tecnica/05 §5/§6) — a ÚLTIMA LINHA anti rug-pull que apanha o schema drift no
// exacto momento antes da execução de cada tool call.
//
// O congelamento (AOS-050) é a EXPECTATIVA; esta revalidação é a VERIFICAÇÃO. Ainda
// que o tool set esteja congelado no prefixo do prompt do run, um servidor MCP pode
// mutar o seu schema em backing store A MEIO do run. A revalidação por chamada
// recalcula/consulta o digest da definição prestes a executar e compara-o com o
// digest congelado; qualquer divergência BLOQUEIA a execução, ALERTA e coloca o
// artefacto em QUARENTENA — fechando definitivamente a janela do rug-pull.
//
// # Sequência FAIL-CLOSED (tecnica/05 §5: LOOKUP → digest → assinatura → scope → EXEC → AUDIT)
//
//	(1) LOOKUP        — a tool está no [toolset.FrozenToolSet] do run? Fora do
//	                    congelado = não foi resolvida no arranque → BLOQUEIA
//	                    (default-deny, sem quarentena: não há artefacto conhecido).
//	(2) DIGEST        — recalcula o digest da definição em backing store (AOS-047)
//	                    e COMPARA com o [toolset.Expectation.Digest] congelado
//	                    (AOS-050). Diverge (schema drift / rug-pull) → BLOQUEIA +
//	                    quarentena + alerta. A identidade pinada (id, version) tem
//	                    de coincidir com a congelada — um swap de versão é drift.
//	(3) ASSINATURA    — revalida a assinatura sobre (id, version, digest) com a
//	                    chave PÚBLICA do publicador confiável (AOS-048). Ausente,
//	                    de chave não-confiável, ou inválida → BLOQUEIA + quarentena
//	                    + alerta. Um atacante que recalcule um digest coerente sobre
//	                    conteúdo adulterado não consegue assiná-lo sob a chave
//	                    legítima.
//	(4) SCOPE/EGRESS  — os scopes de credencial declarados e a classe de egress do
//	                    contract estão DENTRO do permitido pela política do run
//	                    (ADR-006 + egress allowlist de EPIC-07)? Fora → BLOQUEIA +
//	                    quarentena + alerta.
//	(5) EXEC          — só se TODAS passarem, emite um [Permit] NÃO-FORJÁVEL (selado
//	                    por campo não exportado, à imagem do RM) que autoriza o
//	                    despacho.
//	(6) AUDIT         — cada decisão (despacho OU bloqueio) sela-se no audit
//	                    hash-chain WORM (AOS-011) com id, version, digest e resultado.
//	                    Uma autorização não-auditável degrada para bloqueio.
//
// # Composição, não reimplementação
//
// Este pacote é FINO: compõe as primitivas já implementadas e NÃO reimplementa
// nenhuma. Reutiliza o digest de AOS-047 ([digest.SHA256Digester]/[digest.Compare]),
// a assinatura de AOS-048 ([signing.Verify]), o conjunto congelado de AOS-050
// ([toolset.FrozenToolSet]) e o audit WORM de AOS-011 ([audit.Store]). A quarentena
// (AOS-042), o alerta e a allowlist de egress (EPIC-07) são PORTAS que produção liga
// às suas máquinas reais — o mesmo padrão de costura usado no resto do REG
// ([toolset.Catalog], a porta [mcp.EgressAllowlist], [agentruntime.Tracer]). ZERO
// dependências externas.
//
// # Mediação total (ADR-002) — depende da integração no RM
//
// Este pacote é a PEÇA de decisão da mediação anti rug-pull, DESENHADA para o
// Reference Monitor (AOS-003) a invocar antes de mintar o seu próprio permit e
// despachar. A garantia "nenhuma execução directa" só é REALIZADA quando o RM estiver
// ligado a [Revalidator.Revalidate]: enquanto esse wiring estiver pendente (ticket de
// integração dedicado), a totalidade de mediação não é verificável end-to-end e a
// proteção anti rug-pull não está activa num caminho de despacho real. O [Permit]
// não-forjável existe precisamente para o RM o consumir como pré-condição de despacho.
//
// # Orçamento de latência (ADR-002, p95 < 15 ms)
//
// O digest da definição actual é SEMPRE recalculado por chamada (SHA-256 colision-
// resistant sobre os bytes reais) — NÃO há cache. Um discriminador não-criptográfico
// (ex.: um fingerprint FNV) seria vulnerável a segunda-preimagem adversarial, dado que
// o atacante modelado controla o backing store, e poderia MASCARAR um drift num falso
// cache-hit; por isso o único discriminador do passo (2) é o próprio SHA-256, recalcu-
// lado a cada chamada. O custo é de poucos µs sobre um contrato de poucos KB e o
// Ed25519 (passo 3) domina o orçamento de mediação. Ver [Revalidator] e o benchmark
// associado.
//
// # Determinismo e observabilidade
//
// A DECISÃO é pura (sem time.Now/rand): o mesmo (expectativa, definição, política)
// produz sempre o mesmo veredicto. O relógio é injectável e serve APENAS os
// timestamps observacionais do audit. Os spans OTel levam id/version/digest/decisão
// (públicos) — NUNCA scopes, segredos de credencial nem a assinatura.
package revalidation
