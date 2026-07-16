// Package sandbox é o Sandbox Substrate (SBX, AOS-064): o substrato que corre
// CADA tool call com efeitos externos numa microVM DEDICADA por execução, com o
// ciclo de vida (criar → executar → destruir) mediado e observável. É a fronteira
// de isolamento ao nível do kernel por execução do AOS (ADR-004): directory jails
// e filtros de comando descem a defesa-em-profundidade secundária; a fronteira
// primária é a microVM (Firecracker) ou gVisor.
//
// # Fronteira de invocação — só o Reference Monitor (ADR-002)
//
// A sandbox NUNCA é invocada directamente pelo runtime. A ÚNICA via de execução é
// o Reference Monitor (AOS-003): quando o RM PERMITE uma tool call, o seu
// dispatcher interno despacha a ToolFunc registada — e é essa ToolFunc que corre a
// sandbox. O no-bypass é ESTRUTURAL, em três camadas que se reforçam:
//
//  1. O ciclo de vida ([Launcher.run]) é NÃO-EXPORTADO: nenhum pacote externo o
//     alcança.
//  2. As operações do driver ([SandboxDriver] Create/Exec/Destroy) exigem uma
//     [capability] — um tipo NÃO-EXPORTADO. Um pacote externo não consegue nomear
//     o tipo, logo não consegue nem chamar um driver nem IMPLEMENTAR a interface:
//     os únicos drivers são os first-party deste pacote e o único chamador é o
//     [Launcher].
//  3. O único adaptador exportado que corre a sandbox — [MediatedLauncher] —
//     REGISTA o seu despacho como ToolFunc no RM e a sua superfície pública
//     ([MediatedLauncher.Execute]) apenas chama [referencemonitor.Monitor.Mediate].
//     Não há caminho que salte o RM.
//
// Assim, à imagem do permit não-forjável do RM e do internal/adapters do Model
// Gateway, "a invocação da sandbox só pode partir do Reference Monitor" é uma
// propriedade do TIPO, não uma convenção (ver nobypass_test.go).
//
// # Contrato de execução
//
//	exec(run_id, step_id, tool_call, credentials_handle) -> result{stdout, artifacts, taint=untrusted}
//
// O resultado é SEMPRE marcado untrusted ([ExecResult.Taint] devolve sempre
// [TaintUntrusted], imposto pelo tipo — prepara AOS-069). O credentials_handle é um
// identificador OPACO e NÃO-SECRETO (ADR-006): a sandbox recebe o handle, nunca o
// segredo em claro; a resolução server-side (broker/vault) é AOS-070.
//
// # Ciclo de vida e eventos (ADR-010)
//
// O [Launcher] orquestra create → exec → destroy. Cada transição sela um evento no
// Event Store (AOS-002) com run_id/step_id; o create regista ANTES do efeito de
// exec (audit-before-effect) e o destroy é GARANTIDO (defer/cleanup) mesmo em erro
// ou panic — não há microVMs órfãs. Um span execute_tool cobre o ciclo com a
// dimensão de custo por span (placeholder até à metering real de EPIC-08) e SEM
// segredos.
//
// # Drivers seleccionáveis por config (contrato idêntico)
//
// O runtime da sandbox é seleccionável por configuração ([DriverKind]:
// firecracker | gvisor | fake) com contrato de execução IDÊNTICO — a mesma
// sequência create → exec → destroy e o mesmo [ExecResult]. [FakeDriver] é o driver
// de referência determinista (in-process) que modela o jail (isolamento de FS,
// bloqueio de escape por symlink/metacaracteres) e é o usado nos testes.
// [FirecrackerDriver] e [GVisorDriver] são skeletons que DOCUMENTAM a integração
// real (sem socket do host, sem namespace de rede/PID partilhado, rootfs/jail
// dedicado) e satisfazem o contrato via um [GuestExecutor] injectável; sem KVM/host
// support (este ambiente) devolvem [ErrDriverUnavailable].
//
// # Escopo (AOS-064)
//
// Este ticket entrega o isolamento de processo/FS/kernel por execução e a mediação.
// NÃO implementa rede (default-deny é AOS-067), nem o pool com snapshot/restore
// (AOS-065), nem o overlay/seccomp (AOS-066), nem o broker real (AOS-070).
package sandbox
