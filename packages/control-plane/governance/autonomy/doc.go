// Package autonomy implementa a TAXONOMIA DE AUTONOMIA L0–L5 com oversight
// PROPORCIONAL ao impacto (AOS-089; ADR-014, ADR-013). Substitui o desenho
// quase-binário ("HITL por default, autonomia opt-in") por uma escada de seis
// níveis atribuída por par (AGENTE, DOMÍNIO): um agente pode operar a L4 num
// domínio de baixo risco e a L1 noutro sensível.
//
// # O que este módulo entrega
//
//   - [Level] (L0–L5) com a semântica de oversight de cada nível. O valor-zero é
//     [L0] — fail-closed: o mais supervisionado.
//   - [Oversight] — a função PURA nível × classe-de-risco → [OversightMode], que
//     COMPÕE (não reimplementa) o tiering SA-ROC de AOS-074/095: a linha L3 É o
//     tiering base (safe corre, gray em lote, danger confirma).
//   - [LevelRegistry] — o registo (agente, domínio) → [Level] com consulta O(1)
//     FAIL-CLOSED (par sem nível → [L0]), histórico, e [LevelRegistry.SetLevel]
//     AUDITÁVEL (sela um evento autonomy.level_changed com old→new/motivo/actor).
//   - [Oracle] — a PORTA que o caminho de decisão (PDP) consulta para obter o
//     nível corrente. O registo concreto satisfá-la; o PDP aceita-a por inversão.
//   - [ExposeLevel] — o span aos.autonomy.level que expõe o nível corrente por
//     (agente, domínio) na observabilidade (AC4/DoD).
//
// # Composição, não reimplementação
//
// O módulo NÃO reimplementa o gate SA-ROC (AOS-074), o tiering/HITL (AOS-095) nem
// o PDP (AOS-087). COMPÕE: [Oversight] integra o tiering (a linha L3), o PDP
// consulta o [Oracle] no caminho de decisão via [WithAutonomyOracle], e as
// alterações de nível selam-se na hash-chain WORM de platform/audit (o molde
// policy.changed de AOS-088 / retention.config.changed de AOS-092).
//
// # Fail-closed
//
// Um par sem nível registado, um nível inválido ou um domínio indeterminado
// resolvem sempre para o MAIS RESTRITIVO ([L0] / [OversightSuggest]) — nunca para
// autonomia elevada por omissão.
package autonomy
