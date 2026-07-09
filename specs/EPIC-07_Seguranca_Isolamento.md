# EPIC-07 — Segurança e Isolamento

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Segurança e Isolamento |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-01_Fundacoes_Plano_Controlo.md`, `specs/EPIC-09_Governacao_Conformidade.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/13_Modelo_Dados_Eventos.md` |

---

## 1. Visão do Epic

Este epic constrói a **fronteira de segurança** do AOS: o conjunto de mecanismos que tornam as falhas de segurança *arquitecturalmente impossíveis* em vez de *politicamente desencorajadas*. Corresponde à **Fase 1 — Fronteira de segurança (P0)** do roadmap e à Dimensão 2 (Segurança) do blueprint de referência.

A tese é simples e dura: *directory jails* e filtros de comando — triviais de contornar com base64, metacaracteres ou symlinks — não são fronteira; são defesa-em-profundidade secundária. A fronteira **primária** é o isolamento ao nível do kernel por execução (microVM Firecracker/gVisor, ADR-004), rede *default-deny* com egress *allowlist* e filtragem DNS, e a incapacidade física de conteúdo *untrusted* autorizar acções privilegiadas (taint tracking control/data-plane, ADR-005). Sobre isto assentam o **Credential Broker** com tokens JIT (ADR-006) — o agente nunca vê o segredo *downstream* —, a autoridade escopada ao principal (anti *confused deputy*), o audit *tamper-evident* (hash-chain + WORM + assinatura), as mensagens inter-agente assinadas e os gates de risco por sensibilidade + egress + reversibilidade (ADR-013).

Este epic depende das fundações do EPIC-01 (Reference Monitor como PEP físico e identidade não-humana por agente) e entrega ao EPIC-09 a base criptográfica e de audit que a governação e a conformidade GDPR/EU AI Act exigem. O vector nº1 endereçado é o OWASP LLM01 / ASI01 (prompt injection) e o padrão de exfiltração via tools "benignas" (CamoLeak, CVSS 9.6), não o `rm -rf`.

**Componentes-alvo (catálogo canónico):** SBX (Sandbox Substrate), BRK (Credential Broker + Vault), RM (Reference Monitor), OBS (audit hash-chain + WORM), GOV (autoridade escopada, gates de risco).

**ADRs estruturantes:** ADR-004 (isolamento ao nível do kernel), ADR-005 (control/data-plane + taint), ADR-006 (Credential Broker com tokens JIT), ADR-013 (gates de risco SA-ROC). ADRs de suporte: ADR-002 (Reference Monitor mandatório), ADR-003 (identidade não-humana), ADR-010 (audit hash-chain + WORM).

---

## 2. Critérios de Saída do Epic

- [ ] Toda a execução de tool corre numa **microVM isolada** (Firecracker/gVisor) com FS read-only + overlay efémero e seccomp mínimo; nenhum socket do host acessível (ADR-004).
- [ ] O **cold-start de sandbox é < 125 ms** (restore 5–30 ms) via pool pré-aquecido com snapshot/restore, cumprindo o driver não-funcional.
- [ ] A rede da sandbox é **default-deny**; egress só é permitido por *allowlist* explícita e o DNS é filtrado por sandbox (sem resolução para destinos fora da allowlist).
- [ ] Conteúdo *untrusted* (tool results, web, memória, schemas MCP) é **estruturalmente impedido** de autorizar acções privilegiadas: taint tracking control/data-plane operacional e testado contra prompt injection (ADR-005).
- [ ] Nenhum agente vê um segredo *downstream*: credenciais são injectadas server-side pelo **Credential Broker** com tokens JIT, TTL curto e revogáveis (ADR-006).
- [ ] Autoridade é sempre `utilizador ∩ classe de agente`, imposta pelo kernel; **confused deputy** demonstravelmente prevenido.
- [ ] O **audit é tamper-evident**: hash-chain + WORM + assinatura, verificável e separado dos diagnósticos efémeros (ADR-010).
- [ ] Mensagens inter-agente são **assinadas** e verificadas na origem (autenticação de origem + autoridade + referência).
- [ ] O **gate de risco** classifica cada acção por sensibilidade + egress + reversibilidade e escala para HITL quando danger/irreversível, com timeout fail-closed (ADR-013).
- [ ] Existe uma **suite de testes de segurança** (prompt injection, exfiltração) verde no CI como gate bloqueante.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-064 | Sandbox microVM (Firecracker/gVisor) por execução | feature | L | P0 | AOS-001, AOS-005 |
| AOS-065 | Pool de microVMs com snapshot/restore (<125 ms) | feature | L | P0 | AOS-064 |
| AOS-066 | FS read-only + overlay efémero + seccomp | feature | M | P0 | AOS-064 |
| AOS-067 | Rede default-deny + egress allowlist | feature | M | P0 | AOS-064 |
| AOS-068 | Filtragem DNS por sandbox | feature | S | P0 | AOS-067 |
| AOS-069 | Taint tracking control/data-plane (dual-LLM/CaMeL) | feature | L | P0 | AOS-001 |
| AOS-070 | Credential Broker + Vault (tokens JIT server-side) | feature | L | P0 | AOS-001, AOS-005 |
| AOS-071 | Autoridade escopada ao principal (anti confused deputy) | feature | M | P0 | AOS-005, AOS-070 |
| AOS-072 | Audit tamper-evident (hash-chain + WORM + assinatura) | feature | M | P0 | AOS-001 |
| AOS-073 | Mensagens inter-agente assinadas | feature | M | P1 | AOS-005, AOS-072 |
| AOS-074 | Gate de risco por sensibilidade + egress + reversibilidade | feature | L | P1 | AOS-069, AOS-067 |
| AOS-075 | Testes de segurança (prompt injection, exfiltração) | chore | M | P0 | AOS-066, AOS-067, AOS-068, AOS-069, AOS-070 |

---

## AOS-064 — Sandbox microVM (Firecracker/gVisor) por execução

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-003 (Reference Monitor), AOS-002 (Event Store) |
| Bloqueia | AOS-065, AOS-066, AOS-067, AOS-075 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-004, ADR-002 |

### Contexto
O plano-base isolava execução com *directory jails* e filtros de comando — contornáveis com base64, metacaracteres ou symlinks. O blueprint rebaixa esses mecanismos a defesa-em-profundidade secundária e define a fronteira primária como **isolamento ao nível do kernel por execução** (ADR-004). Cada tool call que produz efeitos externos deve correr dentro de uma microVM, invocada exclusivamente através do Reference Monitor (ADR-002), nunca directamente pelo runtime.

### Objectivo
Implementar o substrato de sandbox (SBX) que executa cada tool call numa microVM Firecracker (ou gVisor onde microVM não é viável), sem acesso ao socket do host, com ciclo de vida (criar → executar → destruir) mediado e observável.

### Critérios de Aceitação
- [ ] Toda a execução de tool com efeitos externos corre numa microVM dedicada; nenhuma corre no processo do worker do host.
- [ ] A microVM **não expõe o socket do host** nem partilha o namespace de rede/PID do host.
- [ ] A invocação da sandbox é feita **apenas** pelo Reference Monitor (não há caminho de código que a invoque directamente).
- [ ] O runtime da sandbox é seleccionável por configuração (`firecracker` | `gvisor`) com contrato de execução idêntico.
- [ ] O identificador de execução (`run_id`, `step_id`) é propagado à sandbox e gravado como evento no Event Store.
- [ ] Cada resultado devolvido pela sandbox ao runtime é marcado como *untrusted* (prepara AOS-069).

### Detalhes Técnicos
- Componentes: SBX (novo `sandbox-substrate`), RM (ponto de invocação), ES (eventos de ciclo de vida).
- Ficheiros/módulos: `sandbox/driver_firecracker`, `sandbox/driver_gvisor`, `sandbox/lifecycle`, interface `SandboxDriver{ create, exec, destroy }`.
- Contrato: `exec(run_id, step_id, tool_call, credentials_handle) -> result{stdout, artifacts, taint=untrusted}`.
- Sem rede por omissão neste ticket (default-deny é AOS-067); foco no isolamento de processo/FS/kernel.

### Testes Requeridos
- Integração: uma tool call executa numa microVM e devolve resultado; verificar ausência de acesso ao socket do host.
- Segurança: tentativa de escapar via symlink/metacaracteres não alcança o host (esperado: bloqueado).
- Evento: ciclo de vida create/exec/destroy gravado no Event Store com `run_id`/`step_id`.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Toda a tool call mediada pelo Reference Monitor (sem chamada directa) — verificado por teste (ADR-002).
- [ ] Spans OTel GenAI `execute_tool` emitidos com o ciclo de vida da sandbox e custo por span (ADR-010).
- [ ] Sem segredos em código/logs/spans (ADR-006).
- [ ] Revisão por dois revisores (artefacto P0 de segurança).

### Handoff para Claude Code
```text
Implementa AOS-064 (EPIC-07). Cria o Sandbox Substrate (SBX) que corre cada tool
call numa microVM Firecracker ou gVisor, seleccionável por config, sem socket do
host nem namespace partilhado. A invocação só pode partir do Reference Monitor
(ADR-002); nenhum caminho de código chama a sandbox directamente. Propaga
run_id/step_id, grava o ciclo de vida no Event Store e marca todo o resultado
como untrusted. Não implementes rede (default-deny é AOS-067) nem pool (AOS-065).
Segue _BRIEF.md e 01_Engineering_Standards_e_Handoff.md. Não expandas escopo.
```

---

## AOS-065 — Pool de microVMs com snapshot/restore (<125 ms)

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-064 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Segurança / DevOps-SRE |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-004 |

### Contexto
O isolamento por microVM introduz latência de arranque que, ingénua, tornaria o sistema inutilizável na hot path. O blueprint resolve a tensão *isolamento vs latência* com **snapshot/restore + pool pré-aquecido**: o driver não-funcional exige cold-start < 125 ms com restore de 5–30 ms.

### Objectivo
Implementar um pool de microVMs pré-aquecidas com snapshot/restore, de modo a que a atribuição de uma sandbox a uma execução cumpra o alvo de latência sem sacrificar o isolamento por execução (cada execução recebe uma VM limpa).

### Critérios de Aceitação
- [ ] O tempo de disponibilização de uma sandbox pronta a executar é **< 125 ms** (p95) com restore medido em **5–30 ms**.
- [ ] Cada execução recebe uma microVM **restaurada de snapshot limpo** — sem estado de execuções anteriores.
- [ ] O pool mantém um número configurável de VMs pré-aquecidas e repõe-nas após consumo (warm replenishment).
- [ ] Sob esgotamento do pool, a política de degradação é explícita (esperar/expandir/rejeitar) e observável, nunca reutilizar estado sujo.
- [ ] Métrica de cold-start exposta como SLI com alerta ao ultrapassar o alvo.

### Detalhes Técnicos
- Componentes: SBX.
- Ficheiros/módulos: `sandbox/pool`, `sandbox/snapshot`, `sandbox/metrics`.
- Snapshot base imutável por versão de imagem; restore por cópia-em-escrita para o overlay efémero (liga a AOS-066).
- Reserva atómica de VM ao atribuir (evita corrida no contador do pool).

### Testes Requeridos
- Desempenho: medir cold-start e restore sob carga; provar p95 < 125 ms e restore 5–30 ms.
- Isolamento: execução N+1 não observa artefactos da execução N (VM limpa).
- Resiliência: esgotamento do pool aplica a política declarada, sem reutilização de estado sujo.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos com medições anexadas.
- [ ] Spans OTel + métrica de cold-start como SLI, com custo por span (ADR-010).
- [ ] Toda a atribuição de sandbox continua mediada pelo Reference Monitor.
- [ ] Sem segredos; scan limpo.
- [ ] Revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-065 (EPIC-07) sobre AOS-064. Cria um pool de microVMs
pré-aquecidas com snapshot/restore que cumpra cold-start p95 < 125 ms e restore
5-30 ms. Cada execução recebe uma VM restaurada de snapshot LIMPO (nunca reutilizar
estado sujo). Repõe o pool após consumo, reserva a VM atomicamente e expõe o
cold-start como SLI com alerta. Define política explícita de esgotamento
(esperar/expandir/rejeitar). Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-066 — FS read-only + overlay efémero + seccomp

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-064 |
| Bloqueia | AOS-075 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-004 |

### Contexto
A microVM só é uma fronteira robusta se o seu interior for hostil à persistência e à evasão. O blueprint especifica **filesystem read-only + overlay efémero, seccomp mínimo e sem socket do host** como propriedades da sandbox (ADR-004). O overlay garante que nada escrito por uma execução sobrevive; o seccomp reduz a superfície de syscalls exploráveis.

### Objectivo
Configurar cada microVM com raiz de FS read-only, um overlay efémero descartado no fim da execução, e um perfil seccomp mínimo que permita apenas as syscalls necessárias, negando o resto por omissão (fail-closed).

### Critérios de Aceitação
- [ ] O sistema de ficheiros raiz da sandbox é **read-only**; qualquer escrita vai para um **overlay efémero**.
- [ ] O overlay é **descartado** ao terminar a execução; a execução seguinte não observa nenhum ficheiro escrito pela anterior.
- [ ] Existe um **perfil seccomp** que permite apenas o conjunto mínimo de syscalls; syscalls fora da allowlist são bloqueadas (default-deny).
- [ ] Tentativas de escrita fora do overlay (ex.: na raiz read-only) falham de forma controlada.
- [ ] O perfil seccomp é versionado e o seu hash é registado no manifesto da execução.

### Detalhes Técnicos
- Componentes: SBX.
- Ficheiros/módulos: `sandbox/rootfs` (montagem read-only), `sandbox/overlay` (overlayfs/tmpfs efémero), `sandbox/seccomp/profile.json`.
- Perfil seccomp expresso como allowlist de syscalls; negação por omissão.
- Integração com AOS-065: o overlay é a camada de escrita sobre o snapshot base imutável.

### Testes Requeridos
- Segurança: escrita na raiz é rejeitada; escrita no overlay funciona e desaparece após destroy.
- Isolamento: ficheiro criado na execução N ausente na execução N+1.
- Seccomp: uma syscall fora da allowlist é bloqueada (teste negativo).

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Hash do perfil seccomp gravado no manifesto por trajectória.
- [ ] Spans OTel emitidos; sem segredos.
- [ ] Toda a execução continua mediada pelo Reference Monitor.
- [ ] Revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-066 (EPIC-07) sobre AOS-064/065. Monta o rootfs da microVM
read-only com um overlay efémero (descartado no destroy) e aplica um perfil
seccomp mínimo (allowlist de syscalls, negação por omissão). Prova que escritas
fora do overlay falham, que o overlay não sobrevive entre execuções e que uma
syscall fora da allowlist é bloqueada. Versiona o perfil seccomp e grava o seu
hash no manifesto da execução. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-067 — Rede default-deny + egress allowlist

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-064 |
| Bloqueia | AOS-068, AOS-074, AOS-075 |
| Responsável sugerido | Engenheiro de Segurança / DevOps-SRE |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-004, ADR-013 |

### Contexto
O risco real não é o `rm -rf` mas a **exfiltração via tools "benignas"** (padrão CamoLeak, CVSS 9.6). A defesa arquitectural é rede **default-deny com egress allowlist**: a sandbox não fala com o exterior a menos que o destino esteja explicitamente permitido para aquele principal/classe de agente.

### Objectivo
Implementar a política de rede da sandbox como default-deny, com egress permitido apenas por *allowlist* declarativa por principal/classe, e negar (com registo) qualquer tentativa de egress fora da allowlist.

### Critérios de Aceitação
- [ ] Por omissão, **nenhum tráfego de saída** da sandbox é permitido (default-deny).
- [ ] O egress é permitido **apenas** para destinos numa *allowlist* declarativa, escopada ao principal/classe de agente.
- [ ] Qualquer tentativa de egress fora da allowlist é **bloqueada e registada** como evento de segurança no audit.
- [ ] A allowlist é versionada (policy-as-code) e alterá-la exige o mesmo rigor de qualquer política (liga a EPIC-09).
- [ ] A negação é **fail-closed**: allowlist ausente/ambígua resulta em bloqueio, nunca em permissão.

### Detalhes Técnicos
- Componentes: SBX, RM (aplica a decisão de egress), GOV/PDP (fonte da allowlist).
- Ficheiros/módulos: `sandbox/network/policy`, `sandbox/network/egress_filter`, integração com PDP para resolução da allowlist por principal.
- Filtragem ao nível de IP/porta/host; DNS é tratado separadamente em AOS-068.

### Testes Requeridos
- Segurança: egress para destino fora da allowlist é bloqueado e gera evento de audit.
- Política: egress para destino na allowlist do principal é permitido; para outro principal é negado.
- Fail-closed: sem allowlist configurada, todo o egress é negado.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Política de egress expressa como policy-as-code versionada com teste allow/deny default-deny (ADR-011).
- [ ] Eventos de bloqueio de egress no audit tamper-evident (liga a AOS-072).
- [ ] Spans OTel; sem segredos.
- [ ] Revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-067 (EPIC-07) sobre AOS-064. Configura a rede da sandbox como
default-deny e permite egress apenas por allowlist declarativa escopada ao
principal/classe, resolvida via PDP. Bloqueia e regista no audit qualquer egress
fora da allowlist. Fail-closed: sem allowlist => nega tudo. Não trates DNS
(é AOS-068). Expressa a allowlist como policy-as-code versionada com teste
allow/deny. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-068 — Filtragem DNS por sandbox

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | AOS-067 |
| Bloqueia | AOS-075 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-004 |

### Contexto
Uma allowlist de egress por IP é insuficiente se a resolução de nomes for livre: a exfiltração por DNS (encapsular dados em consultas a um domínio controlado) contorna filtros de camada 3/4. O blueprint exige **filtragem DNS** como parte da fronteira de rede, por sandbox.

### Objectivo
Implementar um resolvedor DNS por sandbox que só resolve nomes cujos destinos são consistentes com a egress allowlist do principal, negando e registando qualquer outra resolução — fechando o canal de exfiltração por DNS.

### Critérios de Aceitação
- [ ] A sandbox usa um **resolvedor DNS controlado** (não o do host nem um público arbitrário).
- [ ] Só são resolvidos nomes cujos destinos estão na **egress allowlist** do principal (AOS-067).
- [ ] Consultas a domínios fora da allowlist são **negadas e registadas** como evento de segurança.
- [ ] Padrões de exfiltração por DNS (consultas de alta entropia/volume a um domínio) são detectáveis e bloqueáveis.
- [ ] Comportamento **fail-closed**: sem política DNS resolúvel, a resolução é negada.

### Detalhes Técnicos
- Componentes: SBX, GOV/PDP (allowlist), OBS (eventos).
- Ficheiros/módulos: `sandbox/network/dns_filter`, integração com `egress_filter` (AOS-067).
- Coerência entre nome resolvido e IP permitido (evitar rebinding).

### Testes Requeridos
- Segurança: resolução de domínio fora da allowlist negada e registada.
- Exfiltração: consulta DNS de alta entropia a domínio controlado é bloqueada (teste sintético).
- Fail-closed: sem política, resolução negada.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Eventos de negação DNS no audit tamper-evident.
- [ ] Spans OTel; sem segredos.
- [ ] Política DNS versionada e testada (allow/deny).
- [ ] Revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-068 (EPIC-07) sobre AOS-067. Dá a cada sandbox um resolvedor DNS
controlado que só resolve nomes consistentes com a egress allowlist do principal.
Nega e regista consultas a domínios fora da allowlist e detecta exfiltração por
DNS (alta entropia/volume). Garante coerência nome->IP (anti rebinding) e
fail-closed sem política. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-069 — Taint tracking control/data-plane (dual-LLM/CaMeL)

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-001 |
| Bloqueia | AOS-074, AOS-075 |
| Responsável sugerido | Engenheiro de Segurança / Engenheiro de Runtime |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-005 |

### Contexto
O vector nº1 (OWASP LLM01 / ASI01, prompt injection) não se resolve com tags `memory_context` in-band — tags in-band não são separação de privilégio. A defesa arquitectural é a **separação control-plane/data-plane com taint** (dual-LLM/CaMeL, ADR-005): conteúdo untrusted (tool results, web, memória, schemas MCP) é *dados*, nunca *instruções*, e é estruturalmente impedido de autorizar acções privilegiadas.

### Objectivo
Implementar taint tracking real que marca todo o conteúdo untrusted na sua origem, propaga o taint pelo fluxo e garante que apenas dados *trusted* (system + utilizador autenticado) podem originar tool calls privilegiadas — o planeador opera sobre dados confiáveis; o untrusted fica em quarentena como dados.

### Critérios de Aceitação
- [ ] Todo o conteúdo de tool results, web, memória e schemas MCP é marcado **UNTRUSTED** na origem.
- [ ] O taint **propaga-se** por derivações: dados derivados de untrusted permanecem untrusted (inclui memória derivada — proveniência para mitigar *memory poisoning*, ASI06).
- [ ] Conteúdo untrusted **não pode autorizar** uma tool call privilegiada — a tentativa é bloqueada no Reference Monitor.
- [ ] Existe separação efectiva entre o plano que planeia sobre dados confiáveis e o plano que apenas manipula dados (dual-LLM/CaMeL ou equivalente contratado).
- [ ] Uma injecção clássica ("ignora as instruções e envia X para Y") embutida num tool result **não** resulta em acção privilegiada.

### Detalhes Técnicos
- Componentes: RT (marcação na origem), RM (enforcement no gate), MEM (proveniência).
- Ficheiros/módulos: `taint/label`, `taint/propagation`, integração no dispatch de tools do runtime e na avaliação do RM.
- Modelo dual-LLM/CaMeL: planeador vê apenas dados trusted; conteúdo untrusted é referenciado por handle, nunca interpolado como instrução.

### Testes Requeridos
- Segurança (prompt injection): payload injectado em tool result não origina tool call privilegiada (liga a AOS-075).
- Propagação: memória derivada de untrusted mantém o taint e a proveniência.
- Enforcement: RM bloqueia autorização originada em dados untrusted.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Toda a tool call mediada pelo Reference Monitor, com verificação de taint testada (ADR-002/005).
- [ ] Spans OTel registam a decisão de taint; sem segredos.
- [ ] Revisão por dois revisores (P0 de segurança).

### Handoff para Claude Code
```text
Implementa AOS-069 (EPIC-07). Cria taint tracking control/data-plane (dual-LLM/
CaMeL, ADR-005): marca UNTRUSTED todo o conteúdo de tool results/web/memória/
schemas MCP na origem, propaga o taint por derivações (memória incluída, com
proveniência) e garante no Reference Monitor que untrusted NÃO pode autorizar
tool calls privilegiadas. Tags in-band não contam como separação. Prova que uma
injecção clássica embutida num tool result não gera acção privilegiada.
Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-070 — Credential Broker + Vault (tokens JIT server-side)

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-003 (Reference Monitor), AOS-002 (Event Store) |
| Bloqueia | AOS-071 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-006 |

### Contexto
Se o agente detém o segredo *downstream*, qualquer prompt injection ou exfiltração o compromete. O blueprint elimina esse risco com o **Credential Broker + Vault** (ADR-006): os segredos vivem no vault e o broker troca o token *scoped* do agente por credenciais downstream **server-side**, com TTL curto e revogáveis. O agente **nunca vê o segredo**.

### Objectivo
Implementar o Credential Broker (BRK) que, a partir do token scoped do agente, obtém do Vault e injecta credenciais downstream directamente na sandbox server-side, com TTL curto, escopo mínimo e revogação, sem que a credencial atravesse o contexto do agente ou o Event Store.

### Critérios de Aceitação
- [ ] O agente **nunca recebe** o segredo downstream; a credencial é injectada server-side na sandbox pelo broker.
- [ ] O broker só troca por credenciais **consistentes com o escopo** do token do agente (utilizador ∩ classe).
- [ ] As credenciais downstream têm **TTL curto** e são **revogáveis**; expiram automaticamente.
- [ ] **Nenhum segredo** aparece em código, logs, spans ou eventos do Event Store (redação/handle, não valor).
- [ ] A troca de token é mediada pelo Reference Monitor e registada (quem, para quê, quando) sem expor o valor.

### Detalhes Técnicos
- Componentes: BRK (novo), Vault, RM (autoriza a troca), SBX (recebe a injecção).
- Ficheiros/módulos: `broker/exchange`, `broker/vault_client`, `broker/injection` (server-side para a microVM).
- Fluxo (blueprint): RM solicita token JIT scoped → Vault/broker injecta credencial server-side na sandbox → execução usa-a sem a expor ao runtime.

### Testes Requeridos
- Segurança: o valor do segredo nunca é observável no contexto do agente, logs, spans ou Event Store.
- Escopo: pedido fora do escopo do token é negado.
- TTL/revogação: credencial expira no TTL e a revogação corta o acesso imediatamente.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Scan de segredos limpo; credenciais só via Broker/Vault com tokens JIT (ADR-006).
- [ ] Troca mediada pelo Reference Monitor e auditada sem expor valores.
- [ ] Spans OTel sem segredos; revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-070 (EPIC-07). Cria o Credential Broker + Vault (BRK, ADR-006):
a partir do token scoped do agente, troca por credenciais downstream no Vault e
injecta-as SERVER-SIDE na sandbox; o agente NUNCA vê o segredo. Escopo = utilizador
∩ classe, TTL curto e revogável. Garante que nenhum segredo surge em código, logs,
spans ou Event Store (usa handles/redação). A troca é mediada pelo Reference
Monitor e auditada sem expor valores. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-071 — Autoridade escopada ao principal (anti confused deputy)

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-005 (identidade NHI), AOS-070 (credential broker) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Segurança / Engenheiro de Governação |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-09_Governacao_Conformidade.md`, ADR-003, ADR-005 |

### Contexto
O *confused deputy* ocorre quando um componente privilegiado age com a sua autoridade a pedido de terceiros sem autoridade. No AOS, a regra é **autoridade = utilizador ∩ classe de agente**, imposta pelo kernel (ADR-003): um agente nunca excede a intersecção da autoridade do utilizador que representa com a da sua classe, mesmo quando um sub-agente ou conteúdo untrusted o solicita.

### Objectivo
Garantir que cada tool call é autorizada estritamente contra a autoridade escopada do principal (utilizador ∩ classe), impedindo escalada por delegação ou por conteúdo untrusted, e cortando o padrão confused deputy no Reference Monitor.

### Critérios de Aceitação
- [ ] Toda a autorização calcula e aplica a **intersecção** `utilizador ∩ classe de agente`; nenhuma tool call excede esse escopo.
- [ ] Um sub-agente **não pode obter** autoridade superior à do principal que o delegou (a cadeia de delegação só restringe, nunca amplia).
- [ ] Um pedido originado em conteúdo untrusted **não** eleva a autoridade efectiva (liga a AOS-069).
- [ ] Uma tentativa de confused deputy (agente A induzido a agir com autoridade que não é do principal) é **negada e registada**.
- [ ] O escopo efectivo de cada tool call é observável no span e no audit.

### Detalhes Técnicos
- Componentes: RM (cálculo e enforcement), GOV (definição de classes), BRK (o token trocado herda o escopo).
- Ficheiros/módulos: `authz/scope`, `authz/delegation_chain`, integração no PDP.
- Regra: escopo efectivo = intersecção monotonicamente decrescente ao longo da cadeia on-behalf-of.

### Testes Requeridos
- Autorização: tool call dentro do escopo permitida; fora do escopo negada.
- Delegação: sub-agente não escala acima do principal (teste negativo).
- Confused deputy: pedido que tenta usar autoridade alheia é negado e auditado.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Decisão de autorização como policy-as-code com teste allow/deny default-deny (ADR-011).
- [ ] Toda a tool call mediada pelo Reference Monitor; escopo efectivo no span (ADR-002/010).
- [ ] Sem segredos; revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-071 (EPIC-07). Impõe autoridade escopada ao principal
(utilizador ∩ classe, ADR-003) no Reference Monitor. A cadeia de delegação só
restringe, nunca amplia: um sub-agente não excede o principal. Conteúdo untrusted
(AOS-069) não eleva autoridade. Nega e regista tentativas de confused deputy e
expõe o escopo efectivo no span/audit. Expressa como policy-as-code com teste
allow/deny. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-072 — Audit tamper-evident (hash-chain + WORM + assinatura)

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-002 (Event Store) |
| Bloqueia | AOS-073 |
| Responsável sugerido | Engenheiro de Segurança / Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-09_Governacao_Conformidade.md`, ADR-010 |

### Contexto
O cenário *The Audit Log Lied* ocorre quando o audit é "append-only por convenção" (ex.: SQLite) e não prova integridade. O blueprint exige audit **tamper-evident**: hash-chain + WORM assinado, **separado dos diagnósticos efémeros**. "Imutável" significa *íntegro*, não *eterno* — o que reconcilia com o GDPR via crypto-shredding (tratado no EPIC-09).

### Objectivo
Implementar o audit trail tamper-evident: cada registo encadeia o hash do anterior (hash-chain), é escrito em armazenamento WORM e assinado, permitindo verificar a integridade de toda a cadeia e detectar qualquer adulteração — separado do canal de diagnósticos efémeros.

### Critérios de Aceitação
- [ ] Cada registo de audit inclui o **hash do registo anterior** (hash-chain verificável ponta-a-ponta).
- [ ] Os registos são persistidos em **WORM** (write-once-read-many); nenhuma reescrita ou remoção é possível pela via aplicacional.
- [ ] Cada registo (ou lote) é **assinado**; a assinatura é verificável e prova origem e integridade.
- [ ] Qualquer adulteração (edição/remoção/reordenação) é **detectável** por verificação da cadeia e das assinaturas.
- [ ] O audit é **fisicamente separado** dos diagnósticos efémeros (destinos e retenção distintos).
- [ ] Existe um verificador que valida a integridade da cadeia de um intervalo dado.

### Detalhes Técnicos
- Componentes: OBS (audit), ES (fonte dos eventos), GOV (retenção/legal hold no EPIC-09).
- Ficheiros/módulos: `audit/hashchain`, `audit/worm_store`, `audit/signer`, `audit/verifier`.
- Compatível com crypto-shredding (EPIC-09): a cadeia mantém-se íntegra mesmo quando o payload cifrado se torna irrecuperável.

### Testes Requeridos
- Integridade: verificador confirma cadeia intacta; adulteração de um registo é detectada.
- WORM: tentativa de reescrita/remoção via aplicação falha.
- Assinatura: registo com assinatura inválida é rejeitado na verificação.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Audit hash-chain + WORM + assinatura operacional e verificável (ADR-010).
- [ ] Separação face aos diagnósticos efémeros demonstrada.
- [ ] Sem segredos; revisão por dois revisores (P0).

### Handoff para Claude Code
```text
Implementa AOS-072 (EPIC-07). Cria o audit tamper-evident: hash-chain (cada
registo encadeia o hash do anterior) + WORM + assinatura por registo/lote,
separado dos diagnósticos efémeros (ADR-010). Fornece um verificador que valida
a integridade de um intervalo e detecta adulteração/remoção/reordenação. Mantém
a cadeia compatível com crypto-shredding (EPIC-09): íntegra mesmo com payload
irrecuperável. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-073 — Mensagens inter-agente assinadas

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-005 (identidade), AOS-072 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, ADR-003 |

### Contexto
No plano-base, o *hallucination gate* apenas verificava a existência de um ID — um pai podia agir sobre um resumo fabricado por um sub-agente. O blueprint eleva-o a **autenticação de origem + autoridade + referência via assinatura**: cada mensagem inter-agente é assinada pela identidade não-humana do emissor e verificada pelo receptor.

### Objectivo
Implementar assinatura e verificação de mensagens inter-agente, de modo a que um agente só aja sobre mensagens cuja origem, autoridade e referência estejam criptograficamente verificadas, eliminando a acção sobre conteúdo fabricado ou forjado.

### Critérios de Aceitação
- [ ] Toda a mensagem inter-agente é **assinada** pela identidade não-humana do emissor (ADR-003).
- [ ] O receptor **verifica origem, autoridade e referência** antes de agir; mensagem com assinatura inválida é rejeitada.
- [ ] Uma mensagem **forjada** (emissor falsificado) ou com referência inexistente é rejeitada e registada.
- [ ] A verificação distingue "ID existe" de "origem autêntica com autoridade" — não basta o ID (melhoria explícita ao hallucination gate).
- [ ] Chaves de assinatura são geridas via Vault/broker; nenhuma chave privada em código.

### Detalhes Técnicos
- Componentes: RT/ORQ (troca de mensagens), BRK/Vault (chaves), OBS (registo de rejeições).
- Ficheiros/módulos: `messaging/sign`, `messaging/verify`, integração no canal de mensagens inter-agente.
- Assinatura cobre payload + metadados de origem/autoridade/referência.

### Testes Requeridos
- Autenticidade: mensagem assinada válida aceite; assinatura inválida rejeitada.
- Anti-forja: emissor falsificado é detectado e rejeitado.
- Autoridade: mensagem cuja autoridade não cobre a acção pedida é recusada.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Rejeições registadas no audit tamper-evident (AOS-072).
- [ ] Chaves via Broker/Vault; scan de segredos limpo (ADR-006).
- [ ] Spans OTel; revisão por (pelo menos) um revisor de segurança.

### Handoff para Claude Code
```text
Implementa AOS-073 (EPIC-07). Assina toda a mensagem inter-agente com a
identidade não-humana do emissor (ADR-003) e verifica no receptor origem +
autoridade + referência antes de agir. Rejeita e regista mensagens forjadas,
com assinatura inválida ou referência inexistente — vai além de "o ID existe".
Gere chaves via Vault/broker, sem chaves privadas em código. Regista rejeições
no audit tamper-evident (AOS-072). Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-074 — Gate de risco por sensibilidade + egress + reversibilidade

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-069, AOS-067 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Segurança / Engenheiro de Governação |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-09_Governacao_Conformidade.md`, ADR-013 |

### Contexto
Gates uniformes produzem *approval fatigue* (utilizadores experientes auto-aprovam >40%) e anulam a governação. O blueprint muda o eixo do gate: já não é só "destrutivo", mas **sensibilidade de dados + egress + reversibilidade** — porque o risco real é a exfiltração via tools "benignas", não o `rm -rf`. Aplica-se o modelo SA-ROC (ADR-013): *safe* corre, *gray* agrupa, *danger/irreversível* confirma com preview.

### Objectivo
Implementar o gate de risco que classifica cada acção proposta num eixo sensibilidade + egress + reversibilidade e aplica a fricção proporcional (safe corre; gray agrupa em lote; danger/irreversível escala para confirmação individual com preview do efeito concreto e timeout fail-closed).

### Critérios de Aceitação
- [ ] Cada acção recebe uma **classe de risco** derivada de sensibilidade dos dados + egress + reversibilidade.
- [ ] **safe** corre sem gate; **gray** é agrupado em lote com resumo; **danger/irreversível** exige confirmação individual com **preview do efeito concreto resolvido**.
- [ ] Acções irreversíveis têm **timeout fail-closed** (a ausência de aprovação nega, nunca permite).
- [ ] O **override-rate** é medido e exposto (anti rubber-stamping).
- [ ] A classificação e a decisão do gate são registadas no audit tamper-evident.
- [ ] Auto-aprovação é configurável por classe e maturidade, mas nunca aplicável a danger/irreversível sem confirmação.

### Detalhes Técnicos
- Componentes: RM (aplica o gate), GOV/PDP (classificação e política), OBS (override-rate, audit).
- Ficheiros/módulos: `risk/classifier` (sensibilidade+egress+reversibilidade), `risk/gate` (SA-ROC), integração com HITL do EPIC-09.
- Reutiliza o sinal de egress (AOS-067) e o taint (AOS-069) como entradas da classificação.

### Testes Requeridos
- Classificação: acção com egress de dados sensíveis é classificada danger; acção local reversível é safe.
- Fluxo SA-ROC: gray agrupa; danger exige confirmação com preview; timeout nega (fail-closed).
- Métrica: override-rate é contabilizado e exposto.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Gate aplicado no Reference Monitor; decisões auditadas (ADR-002/010/013).
- [ ] Política de classificação como policy-as-code versionada e testada.
- [ ] Sem segredos; revisão por dois revisores (P0/segurança).

### Handoff para Claude Code
```text
Implementa AOS-074 (EPIC-07). Cria o gate de risco SA-ROC (ADR-013) que classifica
cada acção por sensibilidade + egress + reversibilidade (não só "destrutivo").
safe corre; gray agrupa em lote com resumo; danger/irreversível exige confirmação
individual com preview do efeito concreto e timeout fail-closed. Mede o
override-rate (anti rubber-stamping) e audita a decisão. Usa egress (AOS-067) e
taint (AOS-069) como entradas. Auto-approve por classe/maturidade, nunca para
danger sem confirmação. Segue _BRIEF.md. Não expandas escopo.
```

---

## AOS-075 — Testes de segurança (prompt injection, exfiltração)

| Campo | Valor |
|---|---|
| Epic | EPIC-07 — Segurança e Isolamento |
| Fase | 1 — Fronteira de segurança |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-066, AOS-067, AOS-068, AOS-069, AOS-070 |
| Bloqueia | — |
| Responsável sugerido | QA / Engenheiro de Segurança |
| Documentos de referência | `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-11_Testes_Qualidade.md`, ADR-004, ADR-005, ADR-006 |

### Contexto
As propriedades de segurança do epic só valem se forem continuamente verificadas. É preciso uma **suite adversarial** que exercite os vectores prioritários — prompt injection (OWASP LLM01 / ASI01) e exfiltração via tools "benignas" (padrão CamoLeak, CVSS 9.6) — e que corra como gate bloqueante no CI, impedindo regressões da fronteira de segurança.

### Objectivo
Construir e integrar no CI uma suite de testes de segurança adversariais que valide, de forma repetível, o isolamento (AOS-064/066), a rede default-deny e a filtragem DNS (AOS-067/068), o taint tracking (AOS-069) e o não-vazamento de segredos (AOS-070), bloqueando o merge em caso de regressão.

### Critérios de Aceitação
- [ ] A suite inclui casos de **prompt injection** (injecção em tool result, web e memória) que provam que untrusted não origina acção privilegiada.
- [ ] A suite inclui casos de **exfiltração** (egress fora da allowlist, DNS tunneling, tool "benigna" que tenta enviar dados sensíveis) — todos bloqueados.
- [ ] A suite verifica que **nenhum segredo downstream** é observável no contexto do agente, logs ou spans (AOS-070).
- [ ] A suite verifica **isolamento** (escrita não persiste, syscall fora do seccomp bloqueada, sem socket do host).
- [ ] A suite corre no CI como **gate bloqueante**; qualquer falha impede o merge (fail-closed).
- [ ] Os casos são versionados e extensíveis (novos vectores adicionam-se sem reescrever a harness).

### Detalhes Técnicos
- Componentes: transversal (SBX, RM, BRK, taint), integra com o eval/test harness do EPIC-11.
- Ficheiros/módulos: `security-tests/prompt_injection`, `security-tests/exfiltration`, `security-tests/isolation`, `security-tests/secrets`.
- Corpus de payloads adversariais versionado; resultados anexados como evidência ao PR.

### Testes Requeridos
- Prompt injection: bateria de payloads não produz nenhuma acção privilegiada (esperado: 100% bloqueado).
- Exfiltração: egress/DNS/tool "benigna" para destino não permitido são todos negados e auditados.
- Segredos: varredura confirma ausência de segredo em contexto/logs/spans.
- Isolamento: overlay não persiste; seccomp bloqueia; sem host socket.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos; suite verde e integrada no CI como gate bloqueante.
- [ ] Evidências (relatórios da suite) anexadas ao PR.
- [ ] Cobertura dos vectores prioritários (LLM01, exfiltração) documentada.
- [ ] Sem segredos nos fixtures; revisão por dois revisores (P0/segurança).

### Handoff para Claude Code
```text
Implementa AOS-075 (EPIC-07). Constrói uma suite de testes de segurança
adversariais (prompt injection e exfiltração) e integra-a no CI como gate
bloqueante fail-closed. Cobre: injecção em tool result/web/memória (untrusted
não origina acção privilegiada — AOS-069); exfiltração por egress fora da
allowlist, DNS tunneling e tool "benigna" (AOS-067/068); não-vazamento de
segredos (AOS-070); isolamento (overlay não persiste, seccomp bloqueia, sem host
socket — AOS-066). Versiona o corpus de payloads e anexa relatórios como
evidência. Segue _BRIEF.md e EPIC-11. Não expandas escopo.
```

---

## Vista de qualidade

Este epic serve primariamente a dimensão **Segurança** e contribui para **Governação** (autoridade escopada, audit, gates de risco), **Observabilidade** (audit tamper-evident, spans de decisão) e **Arquitectura** (fronteiras nos sítios certos: kernel, identidade, egress). As sete dimensões canónicas são: Arquitectura · Segurança · Escalabilidade · Observabilidade · Governação · Experiência de utilização (UX/DX) · Manutenção evolutiva.

## Riscos e mitigações

| Risco | Impacto | Mitigação |
|---|---|---|
| Prompt injection → tool privilegiada | Exfiltração, goal hijack | Taint tracking (AOS-069) + Reference Monitor + egress default-deny (AOS-067) |
| Exfiltração via tool "benigna" (CamoLeak) | Fuga de dados sensíveis | Gate por sensibilidade+egress+reversibilidade (AOS-074) + DNS filtering (AOS-068) |
| Escape da sandbox (symlink/base64/metacaracteres) | Comprometimento do host | microVM + FS read-only + seccomp (AOS-064/066); jails só como defesa secundária |
| Agente vê segredo downstream | Roubo de credenciais | Credential Broker JIT server-side (AOS-070) |
| Confused deputy por delegação | Escalada de autoridade | Autoridade = utilizador ∩ classe imposta pelo kernel (AOS-071) |
| Audit adulterado (*The Audit Log Lied*) | Prova de conformidade impossível | Hash-chain + WORM + assinatura (AOS-072) |
| Latência da microVM na hot path | Sistema inutilizável | Pool com snapshot/restore < 125 ms (AOS-065) |
| Approval fatigue / rubber-stamping | Governação inefectiva | SA-ROC com preview e override-rate medido (AOS-074) |

## Glossário

- **microVM (Firecracker/gVisor):** fronteira de isolamento ao nível do kernel por execução; substitui *directory jails* como defesa primária (ADR-004).
- **Overlay efémero:** camada de escrita descartável sobre um rootfs read-only; nada persiste entre execuções.
- **seccomp:** filtro de syscalls por allowlist com negação por omissão.
- **Egress allowlist:** lista declarativa de destinos de saída permitidos por principal; tudo o resto é negado (default-deny).
- **Taint tracking:** marcação de conteúdo untrusted (tool results, web, memória, schemas MCP) para que não possa autorizar acções privilegiadas (ADR-005).
- **Credential Broker:** serviço que troca o token scoped do agente por credenciais downstream server-side; o agente nunca vê o segredo (ADR-006).
- **Confused deputy:** componente privilegiado induzido a agir com autoridade que não é do principal; prevenido por autoridade escopada (utilizador ∩ classe).
- **Tamper-evident:** propriedade do audit (hash-chain + WORM + assinatura) que torna qualquer adulteração detectável.
- **SA-ROC:** modelo de gate por risco (safe corre, gray agrupa, danger confirma) que combate a approval fatigue (ADR-013).

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
