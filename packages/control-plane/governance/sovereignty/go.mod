module github.com/aos-ref/control-plane/governance/sovereignty

go 1.24

// AOS-094 — o mapa de GOVERNAÇÃO board→região autorizada (soberania por board,
// ADR-011). É um módulo GOV puro (irmão de autonomy/dsar/hitl), SEM dependências:
// só codifica a fronteira regional de cada board e resolve-a fail-closed. O PDP
// compõe-no para EMITIR a obrigação `region` (que o PEP de AOS-087 já impõe); o
// Model Gateway (EPIC-06) mantém a sua própria guarda de fronteira no failover. A
// soberania é, assim, uma propriedade do ENFORCEMENT (PDP→PEP), não papel.
