// Package tofu implementa o modelo TOFU (Trust On First Use) com DETECÇÃO DE
// MUDANÇA DE SCHEMA do Skill/Tool Registry (REG) — AOS-049, EPIC-05, ADR-005/
// ADR-012, tecnica/05 §5. É o controlo que apanha o rug-pull do "Dia 7": o schema de
// um servidor MCP que muta silenciosamente depois de ter ganho confiança.
//
// # Máquina de confiança (first_seen → pinned → changed)
//
// À imagem da máquina de estados durável, cada identidade de servidor MCP tem um
// estado de confiança com semântica clara:
//
//   - first_seen — na PRIMEIRA ligação, regista-se a IDENTIDADE e o DIGEST do
//     manifesto de capabilities (via AOS-047). Estado inicial; NADA é confiado ainda.
//   - pinned — o operador RATIFICA explicitamente ([Monitor.Ratify]) o first_seen. Só
//     depois de pinned o artefacto é de confiança TOFU.
//   - changed — em ligações subsequentes, se o digest do manifesto DIVERGE do pinado
//     (schema mutou / tools diferentes), a identidade é classificada changed. É um
//     INCIDENTE de segurança, não uma actualização de rotina.
//
// # Detecção de drift e bloqueio (fail-closed)
//
// A cada re-descoberta, [Monitor.Observe] recalcula/recebe o digest do manifesto e
// COMPARA-o com o pinado (reutilizando o digest de AOS-047 — [DigestManifest]).
// Idêntico → mantém pinned (passa). Divergente → changed + [ErrSchemaDrift]. Um
// estado changed BLOQUEIA a utilização ([Monitor.Admits] recusa) até re-aprovação.
//
// # Re-aprovação exige nova versão SemVer (nunca in-band)
//
// A recuperação de um incidente é [Monitor.Reapprove], que EXIGE uma nova versão
// SemVer (ADR-012): a mesma versão com digest diferente é RECUSADA
// ([ErrInBandReapproval]) — re-pinar a mesma versão seria aceitar o rug-pull in-band.
// Uma versão inferior é recusada ([ErrVersionRegression]).
//
// # Schemas untrusted durante todo o processo (ADR-005)
//
// O Monitor manipula APENAS identidade + digest (opacos). NUNCA interpreta o
// conteúdo do manifesto: os schemas/descrições MCP permanecem untrusted (a barreira
// de taint de AOS-042, aplicada na descoberta MCP de AOS-046). O TOFU dá confiança à
// IDENTIDADE e à ESTABILIDADE do schema — não transforma o conteúdo em instruções.
// Um schema alterado com texto injectado produz um digest diferente, é classificado
// changed e é BLOQUEADO antes de qualquer efeito.
//
// # Audit WORM e determinismo
//
// Cada transição de confiança (first_seen/pinned/changed/re-aprovação) é SELADA na
// hash-chain WORM (AOS-011, partição [DefaultPartition]) com identidade/versão/
// digest/estado ANTES de tomar efeito; uma transição não-auditável é recusada
// (fail-closed). A decisão de transição é PURA (sem time.Now/rand); o relógio e os
// spans OTel são injectáveis ([WithClock]/[WithTracer]) e só carregam metadados
// públicos — nunca segredos.
//
// # API mínima
//
//   - [Monitor.Observe]   — first_seen na 1.ª ligação; detecção de drift nas seguintes.
//   - [Monitor.Ratify]    — first_seen → pinned (ratificação do operador).
//   - [Monitor.Reapprove] — changed → pinned (exige nova versão SemVer).
//   - [Monitor.Admits]    — BLOQUEIO default-deny: admissível só em pinned.
//
// NÃO reimplementa o digest (AOS-047), a descoberta MCP (AOS-046) nem a hash-chain
// (AOS-011): compõe-se sobre eles.
package tofu
