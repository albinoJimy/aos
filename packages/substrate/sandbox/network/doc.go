// Package network é a REDE DEFAULT-DENY + EGRESS ALLOWLIST da sandbox (AOS-067,
// EPIC-07, ADR-004/011/010). Materializa a fronteira de rede do substrato: por
// omissão NENHUM tráfego de saída é permitido e o egress só passa para destinos
// numa allowlist DECLARATIVA e VERSIONADA, ESCOPADA ao principal/classe de agente.
// Qualquer egress fora da allowlist é BLOQUEADO e SELADO como evento de segurança
// no audit WORM tamper-evident (liga a AOS-072). A negação é FAIL-CLOSED:
// allowlist ausente/ambígua/malformada resulta em bloqueio, nunca em permissão.
//
// # Peças
//
//   - [Policy] — a allowlist POLICY-AS-CODE (egress_policy.json, embebida por
//     embed), VERSIONADA por um DIGEST sha256 canónico (tamper-evident, molde
//     de AOS-058/AOS-066). Carregada FAIL-CLOSED: um default != "deny", um selector
//     de principal ausente, um destino sem localizador (host/CIDR) ou um CIDR/porta
//     inválidos são REJEITADOS no carregamento ([ErrPolicyMalformed]) — nunca um
//     allow por omissão nem uma regra ambígua. [Policy.Evaluate] é default-deny: só
//     devolve allow se existir uma regra do PRINCIPAL/CLASSE que case explicitamente
//     o destino (host exacto ou IP em CIDR) E a porta.
//   - [EgressPolicyResolver] — a PORTA que resolve a allowlist POR PRINCIPAL. O PDP
//     real (cedar, control-plane/pdp) liga-se por trás; [EmbeddedResolver] é a impl
//     de referência (a mesma opção policy-as-code embutida de AOS-057/058/066,
//     data-plane zero-dep). A allowlist de um principal A NÃO permite o principal B.
//   - [EgressFilter] — a DECISÃO allow/deny ([EgressFilter.Decide]): consulta o
//     resolver, avalia default-deny, e num bloqueio SELA um evento de segurança no
//     audit WORM (atribuível a principal + destino tentado). FAIL-CLOSED em toda a
//     borda: destino inválido, allowlist ausente/malformada ou audit indisponível =
//     DENY, nunca bypass.
//   - [EgressHook] — o ponto de COMPOSIÇÃO com o Reference Monitor: implementa
//     [referencemonitor.Hook] (o slot "egress" da cadeia canónica identity → policy
//     → budget → egress → audit), pelo que É O RM QUE APLICA a decisão de egress na
//     mediação. Nenhum caminho de execução salta o RM (ADR-002); o filtro apenas
//     DECIDE, o RM APLICA.
//
// # Fronteira de integração (composition root)
//
// O enforcement REAL de egress só fica ligado quando o Monitor do sandbox é montado
// com o [EgressHook] no slot "egress" (via [referencemonitor.WithHooks]), SUBSTITUINDO
// o [referencemonitor.EgressStub] neutro. Esse wiring vive no composition root
// (packages/integration, AOS-021/037/043) — não neste módulo data-plane. O composition
// root DEVE: (1) construir o [EgressFilter] com um [SecurityAuditSink] WORM real
// (obrigatório — [NewEgressFilter] recusa sem sink); (2) passar o [EgressHook] a
// [referencemonitor.New] no lugar do stub. Enquanto o stub não for substituído, o slot
// de egress em produção é neutro (o RM não aplica egress) — a demonstrabilidade de
// AOS-067 é satisfeita pelo hook_test (RM real), mas a ligação em produção é a fronteira
// a garantir na composição.
//
// # Ambiente sem rede real
//
// Este ambiente não tem rede real nem iptables/nftables. O egress_filter é aqui um
// MODELO verificável: DECIDE (allow/deny) por (principal/classe, IP/porta/host)
// contra a allowlist e IMPÕE a decisão fail-closed; os drivers reais
// (firecracker/gvisor) traduziriam a mesma allowlist para o filtro de rede do kernel
// (iptables/nftables/eBPF) na montagem da microVM. DNS é tratado à parte (AOS-068):
// aqui a filtragem é ao nível de IP/porta/host.
package network
