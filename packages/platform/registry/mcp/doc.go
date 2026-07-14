// Package mcp implementa o AOS como HOST do Model Context Protocol (MCP, AOS-046,
// EPIC-05 §4, ADR-005/ADR-004): a camada que integra servidores de tools de
// terceiros por TRÊS transportes — STDIO (subprocesso local em sandbox), SSE
// (streaming HTTP legado) e Streamable HTTP (transporte remoto recomendado) —
// tratando TUDO o que um servidor devolve (schemas, descrições de tools e
// resources) como conteúdo UNTRUSTED que nunca comanda o planeador.
//
// # Fronteiras de segurança (não-negociáveis)
//
//   - TAINT (ADR-005): os schemas/descrições MCP são marcados UNTRUSTED reutilizando
//     a maquinaria de AOS-042 ([provenance.Ingestor]/[provenance.Partition]) — a
//     barreira estrutural control/data-plane. Uma descrição do tipo "ignora as
//     instruções anteriores…" é servida como [provenance.DataItem] (dados
//     taint-marcados), NÃO implementa [provenance.PrivilegedAuthorizer] e é, por
//     TIPO, incapaz de autorizar uma tool call (tool poisoning inerte). Esta camada
//     NÃO reimplementa a barreira — depende dela.
//   - ISOLAMENTO (ADR-004): o STDIO corre SEMPRE via a porta [SandboxLauncher] (o
//     substrato microVM de EPIC-07 implementá-la-á), nunca com o socket do host. A
//     impl de referência [OSSandboxLauncher] documenta o isolamento (sem ambiente do
//     host herdado, sem descritores extra, só pipes stdin/stdout).
//   - EGRESS (ADR-004): os transportes remotos (SSE, Streamable HTTP) só ligam a
//     endpoints permitidos pela porta [EgressAllowlist] (a allowlist de EPIC-07);
//     um endpoint FORA da allowlist é BLOQUEADO fail-closed ([ErrEgressBlocked]).
//   - TLS OBRIGATÓRIO nos remotos: um endpoint http:// puro (sem TLS) é recusado
//     fail-closed ([ErrTLSRequired]); nunca há downgrade silencioso.
//
// # Handshake e descoberta → staging
//
// O handshake MCP é initialize → tools/list → resources/list (JSON-RPC 2.0 sobre o
// transporte). O manifesto de capabilities devolvido ([CapabilityManifest]) alimenta
// o contract; o seu campo Digest é RESERVADO (o hashing SHA-256 do manifesto é
// AOS-047). A descoberta de tools produz entradas CANDIDATAS no REG (AOS-045) via
// [registry.Registry.Publish] — SEMPRE em staging, NUNCA directamente active.
//
// # Ports (EPIC-07 substitui as impls de referência)
//
// [SandboxLauncher] e [EgressAllowlist] são as portas de isolamento/egress; este
// pacote fornece impls de REFERÊNCIA ([OSSandboxLauncher], [StaticEgressAllowlist],
// [DenyAllEgress]) que documentam a fronteira. EPIC-07 fornecerá as de produção
// (Firecracker/gVisor, filtro de egress) sem alterar esta camada.
//
// # Determinismo e observabilidade
//
// Relógio/IDs injectáveis; sem time.Now/rand no caminho de decisão. Os testes usam
// httptest (SSE/Streamable HTTP) e um subprocesso helper / launcher fake (STDIO).
// Spans OTel GenAI para ligação/descoberta via a porta [agentruntime.Tracer]
// zero-dep. SEM SEGREDOS: tokens de auth do host e Mcp-Session-Id NUNCA entram em
// logs ou spans.
package mcp
