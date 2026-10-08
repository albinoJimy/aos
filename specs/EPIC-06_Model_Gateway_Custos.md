# EPIC-06 — Model Gateway e Custos

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Model Gateway e Custos |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/06_Model_Gateway_Custos.md`, `specs/EPIC-03_Orquestracao_Escalonamento.md`, `specs/EPIC-09_Governacao_Conformidade.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

O **Model Gateway (GW)** é o serviço de plataforma que unifica todo o acesso a modelos de linguagem (LLMs) sob um contrato de porta único, compatível com a API OpenAI. É, para as *model calls*, o que o Reference Monitor é para as *tool calls*: o gate obrigatório por onde toda a invocação de modelo passa antes de sair para um provedor. Nenhum caminho de código do Agent Runtime chama um provider directamente — atravessa o GW, que autentica cada chamada a um **principal** identificado, aplica uma **allowlist regional** de modelos, encaminha o pedido de forma sensível a custo e carga (*cost/load-aware*), impõe um **layout de prompt cache-estável** e contabiliza tokens e custo em USD por chamada.

Este epic resolve directamente uma das falhas mais citadas do documento-fonte: o *credential pool round-robin* que "responde o pool" quando o regulador pergunta *quem autorizou* uma acção. A decisão fundadora é **separar identidade de chaves de infra** (ADR-011) — dois eixos que o desenho ingénuo confunde: a identidade (o par utilizador, agente) é sempre atribuível, enquanto as chaves de conta do provider podem ser *pooled* para *throughput* sem nunca contaminar essa atribuição. Sobre esta base, o epic entrega OAuth multi-provedor, soberania por *board* com failover *fail-closed*, roteamento com *model tiering*, o contrato de cache do ADR-009 com o cache-hit-rate como SLI, e a contabilidade de custo que alimenta a observabilidade e o *admission control*.

O epic vive maioritariamente na **Fase 2** (governação e observabilidade — identidade, allowlist regional, custo) e na **Fase 3** (escala e controlo — roteamento, cache-estável com SLI). Concretiza os ADRs **ADR-011** (identidade e soberania), **ADR-009** (layout cache-estável), **ADR-006** (chaves de infra via broker JIT), com adjacência a **ADR-008** (admission control, imposto a montante pelo Escalonador — ver `specs/EPIC-03_Orquestracao_Escalonamento.md`) e **ADR-010** (span OTel GenAI com custo por chamada — ver `specs/EPIC-09_Governacao_Conformidade.md` e o epic de observabilidade). O desenho de solução detalhado está em `tecnica/06_Model_Gateway_Custos.md`.

---

## 2. Critérios de Saída do Epic

- [ ] O GW é o **único** caminho entre o Agent Runtime e qualquer provedor de LLM; não existe chamada directa a um provider fora do gateway (verificável por *lint* de arquitectura e teste de integração).
- [ ] Toda a *model call* é atribuível a um **principal** (utilizador, agente); nenhuma chamada regista "o pool" como origem (ADR-011).
- [ ] As chaves de infra do provider são obtidas via Credential Broker/Vault JIT *server-side*; o agente **nunca** vê a chave do provider (ADR-006).
- [ ] Existe **allowlist regional** *default-deny* por *board*; um modelo não permitido é recusado *fail-closed*.
- [ ] O **failover cross-border está bloqueado** por desenho: o router nunca encaminha para um endpoint fora da fronteira de soberania do *board*.
- [ ] O roteamento é *cost/load-aware* com *model tiering* e degradação graciosa (*shed → defer → degradar → rejeitar*), coordenado com o admission control global.
- [ ] O layout de prompt cache-estável (prefixo imutável + tail append-only) é imposto e o **cache-hit-rate é medido como SLI** com alerta abaixo de 80% (ADR-009).
- [ ] Cada chamada emite um **span OTel GenAI** com `gen_ai.usage.*` e **custo em USD**; a contabilidade reconcilia com a factura do provider dentro da tolerância acordada.
- [ ] A suite de testes de **roteamento e failover** cobre saturação, indisponibilidade regional e tentativa de failover cross-border, toda verde no CI.
- [ ] O contrato de porta é versionado em SemVer; a troca de modelo/provider é um evento de variância explícito, nunca silencioso.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-055 | Model Gateway unificado (compatível OpenAI) | feature | L | P0 | EPIC-01 (identidade), EPIC-05 (registry de tools) |
| AOS-056 | OAuth multi-provedor (Claude/Gemini/OpenAI) | feature | M | P1 | AOS-055 |
| AOS-057 | Identidade por principal vs chaves de infra pooled | feature | M | P0 | AOS-055, AOS-056 |
| AOS-058 | Allowlist regional + bloqueio de failover cross-border | feature | M | P0 | AOS-055, AOS-057, EPIC-09 (PDP) |
| AOS-059 | Roteamento cost/load-aware + model tiering | feature | L | P1 | AOS-055, AOS-058, EPIC-03 (admission) |
| AOS-060 | Layout de prompt cache-estável (prefixo imutável + tail) | feature | M | P0 | AOS-055 |
| AOS-061 | Cache-hit-rate como SLI | feature | S | P1 | AOS-060, EPIC-08 (observabilidade) |
| AOS-062 | Contabilidade de custo por chamada (USD) | feature | M | P1 | AOS-055, EPIC-08 (observabilidade) |
| AOS-063 | Testes de roteamento/failover | chore | M | P1 | AOS-058, AOS-059 |
| AOS-394 | Selos de governação do Model Gateway ligados ao run e ao passo | fix | M | P1 | AOS-265, AOS-278 |
| AOS-395 | aos-orq: selos de governação do gateway do planeador duráveis e ligados ao run | fix | M | P2 | AOS-394, AOS-391 |
| AOS-397 | Agregados por run do metering do GW sem remoção nem tecto | fix | M | P2 | AOS-394, AOS-062 |
| AOS-399 | O nó pede a posse exclusiva do caminho do audit de governação do gateway | fix | S | P2 | AOS-265, AOS-285 |
| AOS-406 | Sem fonte de preço o custo fica marcado como não derivado e o SLI de custo deixa de dar verde com zeros | fix | S | P1 | AOS-259, AOS-336 |

---

## AOS-055 — Model Gateway unificado (compatível OpenAI)

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 2 |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | EPIC-01 (identidade por agente), EPIC-05 (registry de tools) |
| Bloqueia | AOS-056, AOS-057, AOS-058, AOS-059, AOS-060, AOS-062 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§3), ADR-009, ADR-011, ADR-006 |

### Contexto

O AOS trata o modelo como a *menor* camada do sistema, substituível por contrato e não por lock-in. Para o conseguir, todo o acesso a LLMs tem de passar por um único serviço de plataforma — o Model Gateway — que normaliza a superfície de invocação entre provedores heterogéneos (Anthropic, OpenAI, Google, self-hosted, endpoints regionais). Sem este gate, cada agente falaria directamente com cada provider, tornando impossível impor identidade, soberania, roteamento e contabilidade de forma transversal. Este ticket estabelece o esqueleto do GW: o contrato de porta compatível com a API OpenAI e a *pipeline* determinística de processamento de cada chamada.

### Objectivo

Implementar o serviço Model Gateway com um contrato de porta único, versionado em SemVer e compatível com a API OpenAI (`chat/completions`, *streaming*, *tool calling*, *embeddings*), que serve de ponto de entrada obrigatório para toda a invocação de modelo, com adaptadores de provider por detrás de uma interface estável.

### Critérios de Aceitação

- [ ] O GW expõe uma superfície compatível com a API OpenAI para `chat/completions` (incluindo *streaming* e *tool calling*) e `embeddings`.
- [ ] Existe uma interface de adaptador de provider; pelo menos um adaptador real e um adaptador *fake* (para testes) implementam-na sem alterar o contrato de porta.
- [ ] Cada chamada atravessa a *pipeline* determinística: autenticação do principal → allowlist regional → roteamento → validação de layout de cache → *metering* (os pontos de extensão existem mesmo que preenchidos por tickets posteriores).
- [ ] Nenhum caminho de código fora do GW invoca um provider directamente; um teste/lint de arquitectura falha se tal acontecer.
- [ ] O contrato de porta tem versão SemVer e um *swap* de modelo/provider é registado como evento de variância explícito.
- [ ] Cada chamada emite um span OTel GenAI (`chat`) com `gen_ai.request.model` e `gen_ai.usage.*` (o custo USD é detalhado em AOS-062).

### Detalhes Técnicos

- **Componentes:** GW (Model Gateway). Consome identidade de EPIC-01 e o tool set congelado do registry (EPIC-05).
- **Ficheiros/módulos:** serviço `model-gateway` com `port` (contrato compatível OpenAI), `pipeline` (cadeia de estágios), `adapters/` (interface + adaptador real + fake).
- **Notas:** o serviço é *stateless*; estado (buckets, métricas) é externo. A porta é o único ponto de dependência dos consumidores — os adaptadores são detalhe de implementação.

### Testes Requeridos

- Unit: serialização/normalização do contrato compatível OpenAI; despacho pela *pipeline*.
- Integração: Agent Runtime → GW → adaptador fake devolve resposta normalizada; *streaming* e *tool calling* correctos.
- Arquitectura: teste que prova que não há invocação directa de provider fora do GW.
- Observabilidade: span `chat` emitido com atributos `gen_ai.*`.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Toda a *model call* passa pelo GW; sem chamada directa a provider (ADR-002 por analogia; verificado).
- [ ] Spans OTel GenAI emitidos por chamada (ADR-010).
- [ ] Sem segredos em código/logs/spans (ADR-006); *scan* de segredos limpo.
- [ ] Contrato de porta versionado em SemVer; documentação e ADRs afectados actualizados.
- [ ] Testes unitários, de integração e de arquitectura verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-055 do Agentic OS de Referência (AOS).
Lê specs/EPIC-06_Model_Gateway_Custos.md (bloco AOS-055) e tecnica/06_Model_Gateway_Custos.md (§3).
Objectivo: implementar o Model Gateway com contrato de porta compatível OpenAI (chat/completions,
streaming, tool calling, embeddings) como gate obrigatório de toda a model call.
- Define a interface de adaptador de provider; implementa um adaptador real e um fake.
- Constrói a pipeline determinística com pontos de extensão para auth de principal, allowlist,
  roteamento, guarda de cache e metering (podem ser no-op nesta fase).
- Garante que NENHUM código fora do GW chama um provider directamente; adiciona teste de arquitectura.
- Emite span OTel GenAI (chat) com gen_ai.request.model e gen_ai.usage.*.
- Versiona o contrato de porta em SemVer.
Não expandas escopo: roteamento, allowlist, cache e custo USD são tickets próprios.
Testes: unit, integração (runtime→GW→fake), arquitectura, observabilidade. Abre PR com o template.
```

---

## AOS-056 — OAuth multi-provedor (Claude/Gemini/OpenAI)

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 2 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-055 |
| Bloqueia | AOS-057 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§4), `tecnica/07_Seguranca_Isolamento.md`, ADR-006 |

### Contexto

Os provedores de LLM autenticam serviços por mecanismos distintos (OAuth de serviço, API keys, credenciais federadas por região). Para que o GW seja um contrato de porta estável, a aquisição e rotação destas credenciais de infra tem de estar centralizada e escondida do agente. Este ticket implementa a camada de OAuth multi-provedor que obtém e renova as chaves de infra dos vários provedores (Claude/Anthropic, Gemini/Google, OpenAI) através do Credential Broker/Vault, *server-side*, de forma que o agente nunca as veja (ADR-006). É o fundamento sobre o qual AOS-057 separa identidade de chaves de infra.

### Objectivo

Implementar a integração OAuth multi-provedor no GW, obtendo chaves de infra JIT via Credential Broker/Vault por provider e região, com rotação e revogação, sem nunca expor a chave ao agente nem a persistir em logs ou spans.

### Critérios de Aceitação

- [ ] O GW obtém credenciais de infra para pelo menos três provedores (Claude/Anthropic, Gemini/Google, OpenAI) via Credential Broker/Vault, *server-side*.
- [ ] As credenciais são JIT com TTL curto e são revogáveis; a rotação não interrompe chamadas em curso.
- [ ] A chave de infra **nunca** aparece em código, logs, spans ou na resposta ao agente; *scan* de segredos limpo.
- [ ] Cada provider é configurado por região (a chave escolhida respeita a fronteira de soberania — consumido por AOS-058).
- [ ] Um adaptador de provider sem credencial válida falha *fail-closed* com erro atribuível (nunca cai para outra conta/região silenciosamente).

### Detalhes Técnicos

- **Componentes:** GW, BRK (Credential Broker + Vault).
- **Ficheiros/módulos:** `adapters/oauth/` por provider; `credentials/` (cliente do broker, cache JIT com TTL, rotação).
- **Notas:** as chaves são *pooled* por conta/região (o *pooling* efectivo é responsabilidade de AOS-057); aqui garante-se apenas a aquisição segura e a rotação.

### Testes Requeridos

- Unit: fluxo OAuth por provider; renovação antes do TTL; revogação.
- Integração: GW pede chave ao broker fake, injecta *server-side*, executa via adaptador fake.
- Segurança: *scan* de segredos prova ausência de chave em logs/spans; teste que confirma que a resposta ao agente não contém credencial.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Credenciais downstream via Credential Broker/Vault com tokens JIT; agente nunca vê o segredo (ADR-006).
- [ ] *Scan* de segredos limpo; sem credenciais em código/logs/spans.
- [ ] Spans OTel GenAI mantêm-se sem fuga de credencial.
- [ ] Testes unitários, de integração e de segurança verdes; cobertura não regride.
- [ ] Documentação de configuração por provider/região actualizada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-056 do AOS.
Lê specs/EPIC-06 (AOS-056), tecnica/06 (§4), tecnica/07 e ADR-006.
Objectivo: integrar OAuth multi-provedor (Claude/Anthropic, Gemini/Google, OpenAI) no Model Gateway,
obtendo chaves de infra JIT via Credential Broker/Vault server-side.
- A chave de infra NUNCA pode chegar ao agente nem a logs/spans.
- TTL curto, rotação sem interromper chamadas em curso, revogação.
- Configura credenciais por região (input para a allowlist regional em AOS-058).
- Sem credencial válida => falha fail-closed atribuível; nunca cai para outra conta/região.
Testes: unit (OAuth, rotação, revogação), integração (broker fake), segurança (scan de segredos limpo).
Não implementes aqui a separação identidade/pool (AOS-057) nem a allowlist (AOS-058). Abre PR.
```

---

## AOS-057 — Identidade por principal vs chaves de infra pooled

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 2 |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-055, AOS-056 |
| Bloqueia | AOS-058, AOS-062 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§4), `specs/EPIC-09_Governacao_Conformidade.md`, ADR-011, ADR-003 |

### Contexto

O erro de desenho que o documento-fonte denuncia é o *credential pool round-robin*: um conjunto de chaves partilhado do qual cada chamada retira uma ao acaso. É óptimo para *throughput* e catastrófico para conformidade — destrói a atribuição de identidade, base de todo o audit trail (o cenário *The Audit Log Lied*, em que o audit responde "o pool"). O AOS separa dois eixos que este padrão funde: **identidade** (quem actua) e **chaves de infra** (com que conta se factura o provider). Este ticket é o coração governativo do epic: garante que, independentemente da chave de infra usada, cada chamada é sempre imputável a um principal numa cadeia de delegação até um humano responsável (ADR-011, ADR-003).

### Objectivo

Implementar no GW a separação entre a identidade do principal e as chaves de infra do provider: validar o token *scoped/time-bound* que codifica o par (utilizador, agente) e a política sob a qual actua, permitir o *pooling* das chaves de infra por conta/região para *throughput*, e garantir que **cada chamada regista o principal, o modelo e a região**, seja qual for a chave de infra escolhida.

### Critérios de Aceitação

- [ ] O GW valida, em cada chamada, um token OAuth *scoped/time-bound* que codifica (utilizador, agente) e a política aplicável; token inválido/expirado é recusado *fail-closed*.
- [ ] A autoridade efectiva é `utilizador ∩ classe de agente`; a cadeia de delegação *on-behalf-of* termina num humano responsável.
- [ ] As chaves de infra são *pooled* por conta/região, seleccionadas por *throughput* (TPM/RPM), **sem** relação com a identidade do principal.
- [ ] **Cada chamada regista o principal (utilizador, agente), o modelo e a região**, independentemente da chave de infra usada — nunca "o pool".
- [ ] Um teste demonstra que duas chamadas do mesmo principal servidas por chaves de infra diferentes mantêm o mesmo principal no registo, e que chamadas de principais diferentes servidas pela mesma chave permanecem distinguíveis.
- [ ] O registo de identidade liga-se ao span OTel GenAI e ao audit WORM (detalhe em `specs/EPIC-09_Governacao_Conformidade.md`).

### Detalhes Técnicos

- **Componentes:** GW, GOV (identidade/política), BRK (pool de chaves).
- **Ficheiros/módulos:** `pipeline/authn` (validação de token do principal), `routing/keypool` (selecção de chave de infra por *throughput*, desacoplada da identidade), `metering/attribution` (principal, modelo, região por chamada).
- **Notas:** este ticket cristaliza a tensão *round-robin de credenciais (throughput) vs. atribuição de identidade* da fonte; a resolução é o desacoplamento dos dois eixos.

### Testes Requeridos

- Unit: validação de token do principal; cálculo de autoridade `utilizador ∩ classe`; selecção de chave por *throughput* independente da identidade.
- Política/PDP: token expirado/scoped incorrecto → *deny* default-deny.
- Governação: teste de atribuição cruzada (mesmo principal / chaves diferentes; mesma chave / principais diferentes) — atribuição sempre correcta.
- Integração: chamada regista principal/modelo/região; ligação ao span e ao audit.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Toda a *model call* é atribuível a um principal; nunca "o pool" (ADR-011).
- [ ] Política de validação de token expressa como policy-as-code versionada com teste allow/deny default-deny (ADR-011).
- [ ] Spans OTel GenAI com principal/modelo/região; ligação ao audit WORM (ADR-010).
- [ ] Sem segredos; chaves de infra via broker (ADR-006); *scan* limpo.
- [ ] Testes de governação, política e integração verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-057 do AOS.
Lê specs/EPIC-06 (AOS-057), tecnica/06 (§4), specs/EPIC-09 e ADR-011, ADR-003.
Objectivo: separar identidade do principal das chaves de infra pooled no Model Gateway.
- Valida por chamada o token scoped/time-bound que codifica (utilizador, agente) + política.
- Autoridade = utilizador ∩ classe de agente; cadeia on-behalf-of até um humano.
- Pooling das chaves de infra por conta/região só para throughput, desacoplado da identidade.
- REGISTA sempre principal, modelo e região por chamada — nunca "o pool".
- Liga o registo ao span OTel GenAI e ao audit WORM.
Teste-chave: atribuição cruzada (mesmo principal/chaves diferentes; mesma chave/principais diferentes).
Política de token como policy-as-code com testes allow/deny default-deny. Abre PR com o template.
```

---

## AOS-058 — Allowlist regional + bloqueio de failover cross-border

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 2 |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-055, AOS-057, EPIC-09 (PDP) |
| Bloqueia | AOS-059, AOS-063 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§5), `specs/EPIC-09_Governacao_Conformidade.md`, ADR-011 |

### Contexto

A soberania de dados é imposta *por desenho* no GW, não confiada a configuração *ad-hoc* do provider. Cada *board* (unidade de tenancy/soberania) tem uma allowlist regional de modelos: o conjunto de modelos e endpoints permitidos dentro da sua fronteira legal, com regra *default-deny*. O ponto crítico é o **failover**: quando um endpoint regional está saturado ou indisponível, o router não pode encaminhar para fora da fronteira de soberania, mesmo que isso resolvesse a latência — um failover cross-border transferiria PII para fora da jurisdição (risco *fuga de soberania por failover*, violação potencial do GDPR). Este ticket implementa a allowlist e o bloqueio *fail-closed* do failover cross-border.

### Objectivo

Implementar a allowlist regional de modelos por *board* como policy-as-code *default-deny*, e restringir o failover à mesma fronteira de soberania, rejeitando *fail-closed* qualquer tentativa de encaminhamento cross-border.

### Critérios de Aceitação

- [ ] Cada *board* tem uma allowlist regional de modelos/endpoints; um modelo não explicitamente permitido é recusado *fail-closed*.
- [ ] A allowlist é policy-as-code (Rego/OPA ou Cedar) versionada e assinada, com o changelog no audit trail (ADR-011).
- [ ] O failover só ocorre entre endpoints/chaves da **mesma** fronteira de soberania; nunca cross-border.
- [ ] Sem capacidade intra-fronteira, o pedido é **rejeitado** (com *backpressure* graciosa a montante), nunca encaminhado para outra jurisdição.
- [ ] Uma tentativa de failover cross-border produz *deny* explícito, registado e atribuível ao principal e ao *board*.
- [ ] A decisão de allowlist e a rota escolhida são registadas por chamada (modelo, região, resultado).

### Detalhes Técnicos

- **Componentes:** GW, PDP/GOV (avaliação de política), consumido pelo router (AOS-059).
- **Ficheiros/módulos:** `policy/allowlist` (regras por *board*), `routing/sovereignty` (guarda de fronteira no caminho de failover).
- **Notas:** alinha com o diagrama de decisão de `tecnica/06` (§5): allowlist → saúde do endpoint primário → failover intra-região → rejeição se nenhum intra-região.

### Testes Requeridos

- Política/PDP: modelo fora da allowlist → *deny* default-deny; modelo permitido → *allow*.
- Failover: endpoint primário indisponível + alternativo intra-região → failover intra-fronteira; sem alternativo intra-região → rejeição.
- Governação: tentativa de failover cross-border → *deny* registado e atribuível.
- Integração: decisão de soberania coerente com o router (AOS-059).

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Allowlist como policy-as-code versionada/assinada com teste allow/deny default-deny (ADR-011).
- [ ] Failover cross-border bloqueado por desenho; rejeição *fail-closed* testada.
- [ ] Decisões e *denies* registados em spans/audit (ADR-010); atribuíveis ao principal e ao *board*.
- [ ] Sem segredos; *scan* limpo.
- [ ] Testes de política, failover e integração verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-058 do AOS.
Lê specs/EPIC-06 (AOS-058), tecnica/06 (§5), specs/EPIC-09 e ADR-011.
Objectivo: allowlist regional default-deny por board + bloqueio de failover cross-border.
- Allowlist como policy-as-code (Rego/OPA ou Cedar) versionada e assinada; changelog no audit trail.
- Modelo fora da allowlist => deny fail-closed.
- Failover SÓ intra-fronteira de soberania; sem capacidade intra-fronteira => rejeita (não cross-border).
- Tentativa cross-border => deny explícito, registado, atribuível a principal e board.
Segue o diagrama de decisão de tecnica/06 §5 (allowlist → saúde → failover intra-região → rejeição).
Testes: política allow/deny, failover intra vs rejeição, deny cross-border. Abre PR com o template.
```

---

## AOS-059 — Roteamento cost/load-aware + model tiering

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 3 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-055, AOS-058, EPIC-03 (admission control) |
| Bloqueia | AOS-063 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§6), `specs/EPIC-03_Orquestracao_Escalonamento.md`, ADR-008 |

### Contexto

O documento-fonte substitui o *round-robin cego* por roteamento **least-loaded / token-aware** com **cost-aware model tiering**, evitando o modo de falha "individualmente ok, agregadamente colapsa" em que múltiplos boards, cada um dentro do seu limite, saturam colectivamente o rate limit partilhado. O router do GW decide o destino de cada chamada com base em carga, custo, latência/prioridade e política de degradação graciosa, sempre dentro das restrições já impostas pela allowlist regional (AOS-058) e coordenado com o admission control global do Escalonador (ADR-008, `specs/EPIC-03`). O *model tiering* é também o mecanismo do prompt de exaustão graciosa a ~80% do orçamento: degradar para um tier mais barato é uma opção de continuação em vez do hard-stop cego.

> **Nota cruzada (AOS-031 — porta de tiering já definida).** O *downgrade* da cadeia de degradação graciosa (**shed → defer → degradar → rejeitar**) **já está implementado** no Escalonador (`packages/control-plane/scheduler/degradation.go`, `Degrader`, AOS-031), que encaminha para o tier mais barato através da **porta** `ModelTierRouter` (`Cheaper(req) → {downgraded, from_tier→to_tier, from_model→to_model}`) e regista o swap como **variância explícita** (`model_downgraded`) para o replay ser fiel (ADR-010). Entretanto, a impl de referência determinística `StaticModelTierRouter` (uma escada de tiers por `CostRank`) fecha o contrato — à imagem do `StaticQuotaProvider` de AOS-027. **AOS-059 é o implementador de produção desta porta:** o router *cost/load-aware* do GW deve satisfazer `scheduler.ModelTierRouter` (escolhendo o tier mais barato que satisfaz a capacidade da tarefa, com sinais de carga/latência/soberania reais), substituindo a impl de referência **sem** o Escalonador reimplementar a degradação. O Escalonador é dono da *cadeia* (shed/defer/downgrade/reject, reversibilidade, eventos append-only); o GW é dono da *escolha de tier* por trás da porta.

### Objectivo

Implementar o router *cost/load-aware* com *model tiering* e degradação graciosa (*shed → defer → degradar para modelo mais barato → rejeitar*), coordenado com o admission control global, decidindo o destino de cada chamada dentro da fronteira de soberania.

### Critérios de Aceitação

- [ ] O router selecciona o endpoint **menos carregado** por *headroom* real de TPM/RPM, coordenado com o admission control global de `specs/EPIC-03` (ADR-008).
- [ ] O *model tiering* escolhe o tier mais barato que satisfaz o requisito de capacidade da tarefa (ex.: *frontier* para raciocínio, económico para classificação/extracção).
- [ ] Chamadas interactivas favorecem menor latência; chamadas *batch* toleram tiers mais lentos e baratos.
- [ ] Sob pressão de orçamento/rate limit, o router aplica a política declarativa *shed → defer → degradar → rejeitar*, em coordenação com o *backpressure* do Escalonador.
- [ ] A ~80% do orçamento, a degradação para tier mais barato é oferecida como continuação (exaustão graciosa), nunca hard-stop cego.
- [ ] O router **nunca** viola a allowlist regional (AOS-058); todas as decisões ocorrem dentro da fronteira de soberania.
- [ ] Cada decisão de roteamento regista modelo, tier e razão, para análise de custo *post-hoc* e calibração da política.

### Detalhes Técnicos

- **Componentes:** GW (router), coordenação com SCH/admission (EPIC-03), sob restrição de AOS-058.
- **Ficheiros/módulos:** `routing/router` (sinais carga/custo/latência), `routing/tiering` (tabela de tiers e regras de selecção), `routing/degradation` (política declarativa).
- **Notas:** o router consome o *headroom* do token-bucket distribuído; não faz *spawn* nem invocação sem débito reservado a montante.

### Testes Requeridos

- Unit: selecção por menor carga; escolha de tier por custo/capacidade; ramo latência vs batch.
- Degradação: sob saturação, sequência *shed → defer → degradar → rejeitar* correcta.
- Exaustão graciosa: a ~80% do orçamento oferece degradação em vez de parar.
- Integração: coordenação com admission control (EPIC-03); respeito da allowlist (AOS-058).
- Custo: decisões de tiering registadas para análise *post-hoc*.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Roteamento coordenado com admission control global; sem colapso agregado (ADR-008).
- [ ] Decisões de roteamento registadas em spans (modelo, tier, razão) (ADR-010).
- [ ] Nunca viola a allowlist regional (AOS-058); testado.
- [ ] Sem segredos; *scan* limpo.
- [ ] Testes de roteamento, degradação e integração verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-059 do AOS.
Lê specs/EPIC-06 (AOS-059), tecnica/06 (§6), specs/EPIC-03 e ADR-008.
Objectivo: router cost/load-aware + model tiering com degradação graciosa no Model Gateway.
- Selecciona endpoint menos carregado por headroom real de TPM/RPM (coordena com admission de EPIC-03).
- Tiering por custo/capacidade; interativo favorece latência, batch favorece tiers baratos.
- Sob pressão: shed → defer → degradar para modelo mais barato → rejeitar (política declarativa).
- ~80% do orçamento => oferece degradação (exaustão graciosa), nunca hard-stop cego.
- NUNCA viola a allowlist regional de AOS-058.
- Regista modelo, tier e razão por decisão para análise post-hoc.
Testes: selecção por carga, tiering, degradação, exaustão graciosa, integração com admission. Abre PR.
```

---

## AOS-060 — Layout de prompt cache-estável (prefixo imutável + tail)

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 3 |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-055 |
| Bloqueia | AOS-061 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§7), `specs/EPIC-05_Registry_Supply_Chain.md`, ADR-009 |

### Contexto

A maior fonte de desperdício silencioso num Agentic OS é o *cache thrash*: reivindicar 85–95% de poupança de *prefix caching* e, ao mesmo tempo, adoptar práticas que a destroem — prompt remontado com reordenação, compressão na *hot path*, tools MCP adicionadas a meio do run. O GW impõe o contrato de layout do ADR-009, que divide o prompt em três zonas com regras estritas: prefixo imutável (system + tool set congelado no run), tail append-only (memory_context, timestamps, resultados) e compressão só em checkpoints assíncronos fora da *hot path*. Este ticket implementa a guarda de layout que preserva a estabilidade de cache sem contradizer o replay fiel (o manifesto por turno grava o hash do prompt materializado).

### Objectivo

Implementar no GW a guarda de layout de prompt cache-estável: prefixo imutável byte-idêntico entre turnos do mesmo run, tail append-only, tool set congelado por run (novas tools só em runs novos), e compressão restrita a checkpoints assíncronos.

### Critérios de Aceitação

- [ ] O prompt é estruturado em três zonas: **prefixo imutável** (system + tool set congelado), **tail append-only**, **compressão** (só checkpoints assíncronos).
- [ ] O prefixo é **byte-idêntico** entre turnos do mesmo run; o GW rejeita ou sinaliza montagens que o reordenem.
- [ ] O tail só cresce; nunca muta o prefixo.
- [ ] O **tool set é congelado por run**; novas tools MCP só entram em *runs novos* (alinhado com o pinning de supply-chain de `specs/EPIC-05`).
- [ ] A compressão/sumarização corre fora da *hot path*, em checkpoints assíncronos.
- [ ] O hash do prompt materializado é gravado por turno (manifesto), preservando cache-hit *e* replay fiel (ADR-009/ADR-010).

### Detalhes Técnicos

- **Componentes:** GW (guarda de layout), consumindo tool set congelado do registry (EPIC-05).
- **Ficheiros/módulos:** `cache/layout` (zonas e validação de byte-identidade), `cache/freeze` (congelamento do tool set por run), `cache/compaction` (checkpoints assíncronos).
- **Notas:** a métrica em si (cache-hit-rate como SLI) é AOS-061; aqui garante-se o layout que a sustenta.

### Testes Requeridos

- Unit: validação de byte-identidade do prefixo; rejeição/sinalização de reordenação.
- Comportamento: nova tool MCP não altera um run em curso; entra só em run novo.
- Compressão: sumarização não ocorre na *hot path*.
- Replay: hash do prompt materializado gravado por turno; reprodução coincide.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Layout cache-estável imposto (prefixo imutável + tail append-only) (ADR-009).
- [ ] Tool set congelado por run; novas tools só em runs novos (pinning, EPIC-05).
- [ ] Hash do prompt materializado gravado por turno; replay determinístico testado (ADR-010).
- [ ] Sem segredos; *scan* limpo.
- [ ] Testes de layout, congelamento, compressão e replay verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-060 do AOS.
Lê specs/EPIC-06 (AOS-060), tecnica/06 (§7), specs/EPIC-05 e ADR-009.
Objectivo: guarda de layout de prompt cache-estável no Model Gateway.
- Três zonas: prefixo imutável (system + tool set congelado), tail append-only, compressão assíncrona.
- Prefixo byte-idêntico entre turnos do mesmo run; rejeita/sinaliza reordenação.
- Tool set congelado por run; nova tool MCP só entra em run NOVO (pinning EPIC-05).
- Compressão só em checkpoints assíncronos, fora da hot path.
- Grava hash do prompt materializado por turno (cache-hit + replay fiel sem contradição).
Não implementes a métrica SLI aqui (AOS-061). Testes: byte-identidade, congelamento, replay. Abre PR.
```

---

## AOS-061 — Cache-hit-rate como SLI

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 3 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-060, EPIC-08 (observabilidade) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§7), ADR-009, ADR-010 |

### Contexto

Impor o layout cache-estável (AOS-060) não basta se a sua eficácia não for medida: o *cache thrash* é uma explosão de custo silenciosa. O documento-fonte eleva o **cache-hit-rate a SLI** com alvo > 80% e alerta, tornando visível o que de outro modo passaria despercebido. Este ticket instrumenta o GW para medir o cache-hit-rate por run e por tenant e emitir alerta quando cai abaixo do limiar, ligando a métrica ao pilar de observabilidade (`specs/EPIC-08`).

### Objectivo

Instrumentar o GW para medir o cache-hit-rate (fracção de tokens de prompt servidos por cache de prefixo) por run e por tenant, expô-lo como SLI e emitir alerta abaixo de 80%.

### Critérios de Aceitação

- [ ] O GW calcula o cache-hit-rate por chamada a partir dos tokens de cache read/write reportados pelo provider.
- [ ] A métrica é agregada por **run** e por **tenant** e exposta como SLI.
- [ ] Existe alerta quando o cache-hit-rate desce abaixo de **80%** (alvo canónico do driver não-funcional).
- [ ] A métrica é emitida no formato de observabilidade do AOS (OTel), ligada à trajectória (`specs/EPIC-08`).
- [ ] Um teste demonstra que uma montagem que quebra o prefixo faz o SLI descer e disparar o alerta.

### Detalhes Técnicos

- **Componentes:** GW (metering de cache), OBS (observabilidade, EPIC-08).
- **Ficheiros/módulos:** `metering/cache_sli` (cálculo e agregação), integração com o pilar de métricas de observabilidade.
- **Notas:** consome os campos de cache read/write dos adaptadores de provider (AOS-055/AOS-056).

### Testes Requeridos

- Unit: cálculo do cache-hit-rate a partir de tokens read/write; agregação por run/tenant.
- Alerta: SLI abaixo de 80% dispara alerta.
- Regressão: prefixo quebrado → queda do SLI observável (liga a AOS-060).

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Cache-hit-rate exposto como SLI por run/tenant com alerta < 80% (ADR-009).
- [ ] Métrica emitida em OTel, ligada à trajectória (ADR-010).
- [ ] Sem segredos; *scan* limpo.
- [ ] Testes de cálculo, alerta e regressão verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-061 do AOS.
Lê specs/EPIC-06 (AOS-061), tecnica/06 (§7), specs/EPIC-08 e ADR-009, ADR-010.
Objectivo: cache-hit-rate como SLI no Model Gateway.
- Calcula o cache-hit-rate por chamada a partir dos tokens cache read/write do provider.
- Agrega por run e por tenant; expõe como SLI em OTel, ligado à trajectória.
- Alerta quando cai abaixo de 80% (alvo canónico).
- Teste de regressão: prefixo quebrado faz o SLI descer e o alerta disparar.
Depende de AOS-060 (layout) e do pilar de observabilidade de EPIC-08. Abre PR com o template.
```

---

## AOS-062 — Contabilidade de custo por chamada (USD)

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 2 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-055, EPIC-08 (observabilidade) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§4), `specs/EPIC-09_Governacao_Conformidade.md`, ADR-010, ADR-008 |

### Contexto

O AOS contabiliza orçamento em **tokens e custo (USD)**, não em iterações — porque uma iteração pode arrastar 200K tokens (ADR-008). Para que o admission control global e o burn-down de custo funcionem, cada *model call* tem de produzir uma medida de custo em USD fiável, emitida como span OTel GenAI e reconciliável com a factura do provider. Este ticket implementa a contabilidade de custo por chamada, ligada ao principal, modelo e região (AOS-057), e alimenta tanto a observabilidade (`specs/EPIC-08`) como o orçamento a montante (`specs/EPIC-03`).

### Objectivo

Implementar a contabilidade de custo por chamada em USD no GW: derivar o custo a partir dos tokens de entrada/saída, cache read/write e do preço por modelo/região, emiti-lo por span OTel GenAI e disponibilizá-lo para o burn-down de orçamento e para reconciliação com a factura.

### Critérios de Aceitação

- [ ] Cada chamada regista tokens de entrada/saída, cache read/write e **custo em USD** derivado do preço por modelo/região.
- [ ] O custo é emitido no span OTel GenAI (`gen_ai.usage.*` + custo USD) e ligado ao principal, modelo e região (AOS-057) e à trajectória (`specs/EPIC-08`).
- [ ] Uma tabela de preços por modelo/região, versionada, alimenta o cálculo; uma alteração de preço é um evento explícito.
- [ ] O custo agregado por run/árvore está disponível para o burn-down e para o admission control global (`specs/EPIC-03`, ADR-008).
- [ ] A contabilidade reconcilia com a factura do provider dentro de uma tolerância acordada (teste de reconciliação).

### Detalhes Técnicos

- **Componentes:** GW (metering de custo), OBS (EPIC-08), coordenação com admission/orçamento (EPIC-03).
- **Ficheiros/módulos:** `metering/cost` (cálculo USD), `pricing/table` (tabela versionada por modelo/região), integração com spans e burn-down.
- **Notas:** o custo é insumo do orçamento em tokens/$ do ADR-008; este ticket produz a medida, não o *enforcement* (que é EPIC-03).

### Testes Requeridos

- Unit: cálculo de custo a partir de tokens e tabela de preços; casos com cache read/write.
- Observabilidade: span com `gen_ai.usage.*` e custo USD; ligação a principal/modelo/região.
- Reconciliação: custo agregado vs factura simulada dentro da tolerância.
- Integração: custo disponível para burn-down/admission (EPIC-03).

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Custo em USD por span OTel GenAI, ligado ao principal/modelo/região (ADR-010).
- [ ] Custo agregado disponível para orçamento em tokens/$ (ADR-008, EPIC-03).
- [ ] Tabela de preços versionada; alteração como evento explícito.
- [ ] Sem segredos; *scan* limpo.
- [ ] Testes de cálculo, observabilidade e reconciliação verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-062 do AOS.
Lê specs/EPIC-06 (AOS-062), tecnica/06 (§4), specs/EPIC-08, specs/EPIC-03 e ADR-010, ADR-008.
Objectivo: contabilidade de custo por chamada (USD) no Model Gateway.
- Regista tokens in/out, cache read/write e custo USD por chamada, via tabela de preços versionada.
- Emite no span OTel GenAI (gen_ai.usage.* + custo USD), ligado a principal/modelo/região.
- Disponibiliza custo agregado por run/árvore para burn-down e admission global (EPIC-03).
- Reconcilia com factura simulada dentro de tolerância acordada (teste).
Produz a medida de custo; o enforcement de orçamento é EPIC-03. Abre PR com o template.
```

---

## AOS-063 — Testes de roteamento/failover

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | 3 |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-058, AOS-059 |
| Bloqueia | — |
| Responsável sugerido | QA |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` (§5, §6, §9), `specs/EPIC-11_Testes_Qualidade.md`, ADR-011, ADR-008 |

### Contexto

O roteamento e o failover do GW concentram dois riscos críticos da fonte: o colapso agregado de rate limit e a fuga de soberania por failover. Estes comportamentos são difíceis de validar *ad-hoc* porque emergem sob saturação e indisponibilidade — condições que raramente ocorrem em testes felizes. Este ticket constrói a suite de testes dedicada que exercita, de forma determinística e repetível, os caminhos de roteamento *cost/load-aware*, o *model tiering*, a degradação graciosa e o bloqueio *fail-closed* de failover cross-border, servindo de rede de segurança para regressões futuras.

### Objectivo

Construir uma suite de testes de roteamento e failover que valide, sob condições controladas de carga, custo e indisponibilidade, o comportamento correcto do router (AOS-059) e da guarda de soberania (AOS-058), incluindo os caminhos *fail-closed*.

### Critérios de Aceitação

- [ ] A suite simula saturação de endpoints e valida a selecção *least-loaded/token-aware* sem colapso agregado (ADR-008).
- [ ] Valida o *model tiering*: a tarefa recebe o tier mais barato que satisfaz a sua capacidade; interativo vs batch distinguidos.
- [ ] Valida a degradação graciosa *shed → defer → degradar → rejeitar* sob pressão de orçamento/rate limit.
- [ ] Valida o failover **intra-fronteira** e a **rejeição** quando não há capacidade intra-fronteira.
- [ ] Valida que qualquer tentativa de failover **cross-border** é bloqueada *fail-closed*, com *deny* registado e atribuível.
- [ ] Os testes são determinísticos e repetíveis (provedores fake, relógio/carga controlados) e correm no CI como gate.

### Detalhes Técnicos

- **Componentes:** GW (router e soberania), harness de teste (EPIC-11).
- **Ficheiros/módulos:** `tests/routing/` (cenários de carga/custo/latência), `tests/failover/` (intra-região, rejeição, cross-border), *fakes* de provider por região com carga injectável.
- **Notas:** alinha com os cenários de risco de `tecnica/06` (§9); reutiliza os *fakes* de AOS-055/AOS-056.

### Testes Requeridos

- Roteamento: menor carga, tiering por custo/capacidade, ramo latência vs batch.
- Degradação: sequência *shed → defer → degradar → rejeitar* sob pressão.
- Failover: intra-região (sucesso), sem alternativa intra-região (rejeição), cross-border (deny fail-closed).
- CI: suite integrada como gate, determinística.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos.
- [ ] Suite cobre roteamento, tiering, degradação e failover (incl. bloqueio cross-border).
- [ ] Testes determinísticos e repetíveis; integrados no CI como gate (fail-closed).
- [ ] Cenários de risco de `tecnica/06` (§9) cobertos; sem regressão de cobertura.
- [ ] Sem segredos; *scan* limpo.
- [ ] Documentação da suite e dos *fakes* de provider actualizada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-063 do AOS.
Lê specs/EPIC-06 (AOS-063), tecnica/06 (§5, §6, §9), specs/EPIC-11 e ADR-011, ADR-008.
Objectivo: suite de testes de roteamento/failover do Model Gateway (determinística, no CI).
- Simula saturação e valida selecção least-loaded/token-aware sem colapso agregado.
- Valida model tiering (tier mais barato suficiente; interativo vs batch).
- Valida degradação graciosa: shed → defer → degradar → rejeitar.
- Valida failover intra-fronteira, rejeição sem capacidade intra-fronteira, e bloqueio cross-border fail-closed.
- Usa fakes de provider por região com carga injectável e relógio controlado.
Cobre os cenários de risco de tecnica/06 §9. Integra a suite como gate de CI. Abre PR com o template.
```

---

## AOS-394 — Selos de governação do Model Gateway ligados ao run e ao passo

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-265 (audit de governação do GW durável), AOS-278 (principal por ctx) — ambos fechados |
| Bloqueia | AOS-395 |
| Fecha | O critério residual de AOS-264 «o pipeline do GW passa `WithRun`» (a parte do principal foi fechada por AOS-278) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/platform/model-gateway/runtime_adapter.go`, `packages/platform/model-gateway/policy/allowlist/stage.go` e `audit.go`, `packages/platform/model-gateway/routing/failover/failover.go`, `packages/platform/model-gateway/production_routing.go`, `packages/platform/model-gateway/port/port.go`, `packages/cmd/aos/modelgatewaywiring.go`, `tecnica/06_Model_Gateway_Custos.md` |

### Contexto

Medido em produção a 2026-09-15 no run `run-delegado-1789509858` (roteiro E2E manual, PR #299): o gateway selou as 6 chamadas ao modelo em `modelgw-gov:board-eu` (`AuditSeq` 96–101, `model:invoke`, `allow`, principal `agt-e2e-19` com a cadeia até ao humano), mas **todos os selos têm `RunID`, `StepID`, `RequestID` e `ParentStepID` vazios**. O run de 14 de Setembro (`run-delegado-1789394468`, 4 selos) tem a mesma lacuna. A ligação chamada ao modelo ↔ run só se reconstrói pelo NHI do agente e pela hora; com dois runs do mesmo agente em paralelo a atribuição fica ambígua, contra a trajectória correlacionável que o ADR-010 pede.

A discovery encontrou três causas que se somam:

1. **O nó não entrega o run ao gateway.** O `ModelClientAdapter` só preenche `ChatRequest.RunID` a partir de um valor fixado na construção (`WithRun`, `runtime_adapter.go:79-81`, usado em `:112`). O nó não o liga de propósito (`cmd/aos/modelgatewaywiring.go:296-299`): o adaptador é construído uma vez por nó e um run de construção agregaria todos os runs no mesmo balde. O comentário remete a amarra por run para o AOS-265, que fechou sem a entregar ao GW.
2. **Mesmo com run, o selo não o copia.** `allowlist.GovRecord` tem `RunID`/`StepID` e o `Seal` copia-os, mas os três sítios que constroem o registo não os preenchem: `allowlist.(*Stage).record` (`stage.go:151-165`), `failover.(*Stage).sealCrossBorderDeny` (`failover.go:229-243`) e `modelSwapRecorder.Process` (`production_routing.go:506-519`).
3. **O passo não tem onde viajar.** Nem `port.ChatRequest` nem `pipeline.Exchange` têm `StepID`; o `PromptView` que atravessa a porta `ModelClient.Call(ctx, PromptView)` só leva o turno e os hashes.

O `run_id` e o `step_id` existem no ponto da chamada: `Runtime.callModel` recebe-os e põe-nos no span `chat` antes de chamar o modelo.

### Objectivo

Cada selo de governação do gateway escrito durante um run identifica o run e o passo que o originaram, em todos os veredictos (allow, deny de allowlist, deny cross-border, troca de modelo), sem fixar o run na construção do adaptador.

### Critérios de Aceitação

- [x] O `RunID` e o `StepID` do turno chegam ao gateway **por chamada** — pelo ctx, à imagem de `WithPrincipalFromContext`, ou pela porta — e nunca fixados na construção do adaptador. A escolha fica registada neste ticket. *(DECISÃO: pelo **ctx**. `agentruntime.ContextWithModelCall`/`ModelCallFromContext` (`packages/kernel/agent-runtime/model_call_context.go`) são escritas por `Runtime.callModel` sobre o `chatCtx`, com o mesmo `stepID` dos checkpoints e do `turn.recorded`; o `ModelClientAdapter.Call` lê-as e o run do ctx tem **precedência** sobre `WithRun`, que fica como fallback de um adaptador construído por run. A chave vive no pacote raiz do agent-runtime, o único sítio que a baseline do `layer-lint` autoriza o gateway a importar (ADR-019 §2.3, que nomeia `RunID`/`StepID`). Alternativas rejeitadas: pôr o par na `PromptView` (muda um tipo da porta do kernel que a captura e o replay tratam) e derivar o passo no nó a partir de `view.Turn` (duplicaria o formato e divergiria de um `StepIdentity` injectado).)*
- [x] `port.ChatRequest`, `port.EmbeddingsRequest` e `pipeline.Exchange` transportam `StepID` como metadado de plataforma (`json:"-"`, nunca no wire do provider). *(`port/port.go`, `pipeline/pipeline.go`; `newExchange` passa-o nos três caminhos (chat, stream, embeddings). `port.Version` sobe a `1.1.0` — campo aditivo, MINOR pelo critério do próprio pacote. `TestChatRequest_MarshalWire_NaoVazaMetadados` passa a proibir no wire o run, o passo, `run_id` e `step_id`.)*
- [x] Os três construtores de `allowlist.GovRecord` copiam `RunID` e `StepID` do `Exchange`. *(`policy/allowlist/stage.go` (allow e deny), `routing/failover/failover.go` (deny cross-border), `production_routing.go` (troca de modelo). O `Seal` já os copiava para o registo de audit. A atribuição (`Gateway.attribute`) passa também a levar o `StepID`, que o `attribution.Record` já tinha.)*
- [x] Teste que falha antes da correcção, pela cadeia real do nó (molde de `TestGatewayModelClient_EndToEnd`): uma chamada de turno sela em `modelgw-gov:<board>` com `RunID` igual ao run e `StepID` igual ao passo do turno; o deny de allowlist e o deny cross-border também. *(`packages/cmd/aos/aos394_model_gateway_run_test.go`: Agent Runtime real + `newGatewayModelClient` + upstream httptest; dois runs do mesmo agente saem distinguíveis e o passo selado é o mesmo do `turn.recorded`; um modelo fora da allowlist sela o deny com run e passo. **FALHA-ANTES MEDIDA**: comentar a escrita do ctx em `callModel` faz falhar `TestAOS394_CallModel_AnexaRunEPassoDeCadaTurno` («viu (\"\", \"\")») e os dois testes do nó («selo 1 tem RunID \"\"»). O deny cross-border e a troca de modelo não são alcançáveis pelo nó de referência (uma só conta na região pedida; sem escada de tiers, DEF-280-NO), pelo que são provados na composição `NewProduction`: `packages/platform/model-gateway/aos394_production_selos_test.go`.)*
- [x] Chamada sem run no ctx: o comportamento é decidido e testado. O selo de governação continua a ser escrito (a governação não depende da correlação) e a ausência fica visível, nunca preenchida com um valor inventado. *(`TestAOS394_NoGateway_ChamadaSemRunSelaNaMesmaComAusenciaVisivel` e o caso «sem correlacao» de `TestAOS394_Adaptador_CorrelacaoPorChamada`.)*
- [~] Os consumidores que já leem `ex.RunID` (cache-hit-rate por run, atribuição e custo por run) passam a receber o run real no nó, sem regressão nos seus testes. *(Sem regressão: `go test -race ./...` verde no módulo do GW, no kernel e em `cmd/aos`; `apex` e `routing` verdes. **Consequência declarada, não fechada aqui**: com o run real, o `cost.Recorder` passa a manter um cumulativo por run em `runAggs`, um mapa sem remoção nem tecto — num nó de vida longa cresce com o número de runs. Fica no **AOS-397** (o eixo é o agregador de custo, não a correlação); o `cache_sli` tem o mesmo padrão e não está composto no nó.)*
- [x] O comentário de `cmd/aos/modelgatewaywiring.go` que remete a amarra por run para o AOS-265 é corrigido, e o critério residual de AOS-264 é marcado com a evidência deste ticket. *(Comentário reescrito; nota acrescentada ao critério de `specs/EPIC-20` — a caixa lá **não** é marcada porque a outra metade do critério, a capability da troca no bundle assinado, é do broker.)*
- [x] Evidência de sistema: um run real (nó composto com gateway, ou produção) deixa no `model-audit.wal` um selo por turno com `RunID` e `StepID` preenchidos. É o ponto «Selo do gateway ligado ao run» do roteiro E2E. *(**VERIFICADO EM PRODUÇÃO, 2026-09-15**. A **v0.1.14** (`ghcr.io/albinojimy/aos-node@sha256:52d1e913812c691da815d5922045e00e7c3d5e4cb8c0f86efb6bf94b66d4c9b6`) entrou às 23:40Z pelo release da tag, com os 28 gates verdes e a atestação assinada e verificada. O run `run-delegado-1789519407` — 2 turnos, uma `doc_read` executada no gVisor, `ready→running` (seq 1) e `running→complete` (seq 21) — deixou em `modelgw-gov:board-eu` os selos `#102` (`RunID=run-delegado-1789519407`, `StepID=step-000001`) e `#103` (`StepID=step-000002`): **um por turno, e cada `StepID` igual ao do `turn.recorded` correspondente** (seq 5 e seq 17). O antes e o depois estão no MESMO ficheiro: os selos `#99–101`, do run de 21:04 sobre a imagem anterior, continuam com `RunID` e `StepID` vazios.)*
- [x] `tecnica/06_Model_Gateway_Custos.md` descreve os campos do selo de governação e a correlação por run e passo. *(§5, «Selo de governação por chamada».)*

### Estado

**IMPLEMENTADO e VALIDADO EM PRODUÇÃO (2026-09-15).** A correlação viaja no ctx por chamada e entra nos quatro veredictos selados; `port.Version` 1.1.0. Verificado antes da entrega: suites `-race` verdes no kernel, no módulo do GW, em `cmd/aos` e no `testkit`; `build`, `lint`, `layer-lint`, `apex`, `routing` e `integration` verdes; smoke do nó composto 10/10; falha-antes medida por mutação; revisão adversarial independente com 3 achados ALTO e 4 MÉDIO, todos tratados. Verificado em produção com a **v0.1.14**: os selos do run de validação levam run e passo, contra os selos vazios do run anterior no mesmo trilho (ver o último critério).

Fica declarada a consequência do agregado de custo por run (**AOS-397**). **Observação colateral da validação, alheia a este ticket:** a tool call real fez disparar em produção o alerta crítico `mediation_overhead_p95` (1,21 s contra um SLO de 15 ms) porque o SLI usa a latência do span `execute_tool`, que inclui a execução no sandbox (`packages/substrate/otel-genai/slo.go`, fora deste changeset) — o overhead da decisão do RM foi de milissegundos. O alerta calou-se quando a janela rolou; o defeito de medição está por abrir como ticket.

---

## AOS-395 — aos-orq: selos de governação do gateway do planeador duráveis e ligados ao run

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-394 (transporte de run e passo até ao selo), AOS-391 (gateway composto no aos-orq) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos-orq/model_gateway_wiring.go`, `packages/control-plane/orchestrator/decompose/decompose.go`, `packages/control-plane/orchestrator/planner/planner.go`, `packages/cmd/aos-orq/aos391_gateway_test.go` |

### Contexto

O mesmo defeito do AOS-394 repete-se no caminho `--goal` do `aos-orq`, com um agravante. `gatewayDecomposeModel.Complete` (`model_gateway_wiring.go`) envia o `port.ChatRequest` sem `RunID`, e a porta `decompose.Model.Complete(ctx, system, user)` (`decompose.go:40`) nem sequer recebe o run, embora `planner.runAttempt` o tenha. O agravante: o gateway do planeador é composto com `Audit: audit.NewMemStore()` (`model_gateway_wiring.go:185`), pelo que os selos `modelgw-gov:*` das chamadas de decomposição **perdem-se no fim do processo** — não há rasto durável de que modelo o planeador invocou, sob que principal e com que veredicto.

### Objectivo

As chamadas de decomposição do planeador deixam selos de governação duráveis, ligados ao run e à tentativa de planeamento que as originou.

### Critérios de Aceitação

- [x] Os selos `modelgw-gov:*` do planeador são escritos num WORM durável (configuração à imagem de `AOS_MODEL_AUDIT_PATH` no nó) e o modo fica declarado no arranque; sem store durável, a postura volátil é declarada, nunca silenciosa. *(`packages/cmd/aos-orq/model_audit_env.go`: a mesma variável `AOS_MODEL_AUDIT_PATH` abre um `audit.FileStore`, e o `construirModeloGateway` recebe-o no lugar do `MemStore`. A variável resolve-se no `serve` **antes** de reclamar o run e só quando a decomposição vai pelo gateway (`--goal` sem fixture, gateway configurado). Um caminho que não abre, ou cujo directório não existe, aborta com `ErrBadModelAudit` sem tomar posse. A linha de postura (`modelAuditPostureBanner`) sai quando a decomposição vai pelo gateway: `DURAVEL` com o caminho, ou `IN-MEMORY (VOLATIL)` a dizer que os selos se perdem. **Acrescento à discovery**: o WORM do audit não arbitra entre processos, e duas réplicas `aos-orq` no mesmo caminho bifurcariam a hash-chain. A abertura pede primeiro a posse exclusiva ao SO (`eventstore.LockWAL`, o árbitro do AOS-285), e um segundo escritor sai com o código 5 antes de reclamar o run. O nó `aos` pede a mesma posse desde o AOS-399, pelo que um nó e um `aos-orq` no mesmo caminho recusam-se um ao outro; os caminhos continuam a ter de ser distintos, o que fica no runbook `PROC-DESPACHO-MULTIPROC` e em `tecnica/06` §5.)*
- [x] A chamada de decomposição leva o `RunID` do run e um `StepID` estável da tentativa de planeamento, pelo mesmo mecanismo escolhido no AOS-394. *(Pelo ctx: `planner.runAttempt` anexa `agentruntime.ContextWithModelCall(ctx, req.RunID, "planstep:decompose:<tentativa>")` e `gatewayDecomposeModel.Complete` lê o par e passa-o ao `port.ChatRequest`. A porta `decompose.Model` não muda. Sem anexo, os dois campos ficam vazios. Testes: `TestAOS395_RunAttempt_AnexaRunEPassoPorTentativa` (duas tentativas, dois passos distintos), `TestAOS395_Complete_LevaRunEPassoDoCtx` e `TestAOS395_Complete_SemCtxMostraAAusencia`.)*
- [x] Teste por processo real (molde de `aos391_gateway_test.go`, com gateway falso): `serve --goal` sela com `RunID` igual a `--run` e o `StepID` da tentativa, e o selo é relido do ficheiro depois de o processo terminar. *(`TestAOS395_ProcessoReal_SeloDuravelComRunEPasso`: o binário compõe o gateway REAL (`NewProduction`) contra um upstream OpenAI em httptest. Depois de o processo sair, o selo `model:invoke` é relido de `modelgw-gov:board-eu` por `OpenFileStoreReadOnly`, com `RunID=run-aos395` e `StepID=planstep:decompose:1`. **FALHA-ANTES MEDIDA por mutação**: sem a anexação em `runAttempt`, o teste do planeador vê `SEM-ANEXO` e o de processo real lê `RunID=""`, com o ficheiro durável presente. As duas metades do ticket falham de forma independente.)*
- [x] Fail-closed preservado: falha a selar ⇒ a decomposição não prossegue; nenhum nó é materializado. *(`TestAOS395_SeloFalha_NaoChamaOModeloNemDecompoe`: com o gateway composto por `construirModeloGateway` e um store que recusa o selo `model:invoke`, o `Complete` devolve erro e o upstream recebe **zero** pedidos (audit-before-effect do estágio de allowlist). O controlo positivo, com o mesmo store sem avaria, recebe um pedido. O erro do decompositor faz `decomporEMaterializar` sair antes da validação e da materialização. Na config: `TestAOS395_ProcessoReal_AuditMalConfiguradoAbortaSemPosse` (caminho que não abre ⇒ exit ≠ 0, sem nós) e `TestAOS395_ProcessoReal_CaminhoDetidoPorOutroEscritorSai5` (caminho detido ⇒ exit 5, sem `posse:`). **FALHA-ANTES MEDIDA** do último: sem o `LockWAL`, o segundo processo reclama o run e abre o WORM do outro. `TestAOS395_ProcessoReal_ServeSemGatewayIgnoraOAudit` guarda o inverso: um `serve` sem `--goal`, com o caminho detido, sai 0. Medido: com a abertura incondicional sai 5, e réplicas `--nats` que partilham o ambiente seriam recusadas sem chamarem o modelo.)*

### Estado

**IMPLEMENTADO (2026-09-16).** Os selos de governação das decomposições do `aos-orq` ficam num WORM em ficheiro, com um só escritor por caminho arbitrado pelo SO, e levam o run e a tentativa pelo mecanismo do AOS-394. Verificado: suites `-race` verdes no módulo do orchestrator e em `cmd/aos-orq`; `build`, `layer-lint` e `gofmt` verdes; `lint`, RTM, `ref-lint`, `deferrals` e `estado-citado` verdes; falha-antes medida por mutação na correlação, na posse e no âmbito da abertura. Revisão adversarial independente: nenhum crítico ou alto; os achados médios e baixos foram corrigidos, excepto o residual abaixo. A posse do caminho no nó `aos`, que ficou como residual na revisão, fechou com o AOS-399 (tomada em `parseModelAuditFromEnv`, antes da abertura, e não em `tomarPosseDoWAL`, que corre depois).

**VERIFICADO EM PRODUÇÃO (2026-09-16, v0.1.16).** O deploy da `v0.1.16` (commit `3a5aaa6`, imagem `aos-node@sha256:7e35a476…`) não põe este caminho a correr, porque o `aos-orq` não vem na release nem na imagem do nó. A prova fez-se por um run avulso no servidor: o `aos-orq` linux compilado do `3a5aaa6` correu uma vez num contentor efémero na rede `aos_default`, contra o litellm de produção (`gpt-4o-mini`), com `AOS_MODEL_AUDIT_PATH` numa pasta temporária. O arranque declarou a postura `DURAVEL`. Depois de o processo sair, o WORM foi aberto com `OpenFileStoreReadOnly`, que valida a cadeia, e a partição `modelgw-gov:board-eu` tinha três selos `allow` de `model:invoke`:

| Run | Seq | StepID |
|---|---|---|
| `run-aos395-prod-1789586701` | 1, 2, 3 | `planstep:decompose:1`, `:2`, `:3` |
| `run-aos395-prod-1789587285` | 1, 2, 3 | `planstep:decompose:1`, `:2`, `:3` |

Todos levam o `RunID` do seu run e a cadeia liga-se (o `PrevHash` de cada selo é o `EntryHash` do anterior). As três tentativas por run aconteceram porque o decompositor recusou os três planos devolvidos pelo modelo (`plan: objective de topo em falta`; o litellm respondeu 200 às seis chamadas). Nada foi materializado. A recusa não é deste ticket: é uma lacuna do prompt do planeador, registada no **AOS-400**. As pastas temporárias foram apagadas do servidor.

---

## AOS-397 — Agregados por run do metering do GW sem remoção nem tecto

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-394 (faz o run real chegar ao gateway), AOS-062 (contabilidade de custo) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/platform/model-gateway/metering/cost/recorder.go`, `packages/platform/model-gateway/metering/cache_sli/cache_sli.go`, `packages/platform/model-gateway/internal/lru/lru.go` |

### Contexto

O `cost.Recorder` mantém o cumulativo de custo por run em `runAggs`, um mapa `RunKey{RunID,Tenant} → *Amount` **sem remoção, expiração nem tecto**. Enquanto o nó enviava o run vazio, o ramo que o alimenta nunca corria e o mapa ficava vazio; com o AOS-394 o run real passa a chegar em cada chamada, pelo que o mapa ganha uma entrada por run e mantém-na durante toda a vida do processo — um nó que corre semanas acumula-as. O mesmo padrão existe no eixo de árvore (`treeAggs`, hoje sem `TreeID` preenchido), no `MemoryBurndownSink` (que memoriza cumulativos por run) e no `cache_sli`, que agrega por (run, tenant) e não está composto no nó.

Impacto medido por leitura de código: é **memória**, não decisões — no nó de referência nenhum consumidor lê o cumulativo por run (não há `budgetbridge` composto). O custo por chamada e o canal de AOS-259 não dependem deste agregado.

### Objectivo

A retenção por run do metering do gateway é limitada e a política fica declarada, sem alterar o custo por chamada nem o canal de custo.

### Critérios de Aceitação

- [x] Teste que mede a retenção antes da correcção: N runs distintos deixam N entradas retidas no `cost.Recorder`. *(**MEDIDO antes da correcção** (2026-09-16), com um teste temporário sobre o código da base: 10 000 runs distintos ⇒ 10 000 entradas em `runAggs`. Depois da correcção, `TestAOS397_RetencaoPorOmissaoTemTecto` mede os mesmos 10 000 runs (com árvore) ⇒ 4096 runs e 4096 árvores retidos, o run mais recente presente e o mais antigo despejado. **FALHA-ANTES reprodutível por mutação**: com o tecto a `1 << 30`, o mesmo teste falha com «retidos runs=10000 arvores=10000».)*
- [x] Política escolhida e implementada, com a decisão registada neste ticket: fim-de-run explícito, expiração por inactividade, tecto com despejo, ou não retenção quando ninguém lê o cumulativo. Verificar primeiro quem lê o cumulativo por run (burn-down de AOS-259/AOS-261, span, sinks). *(**Quem lê** (discovery): nenhum consumidor composto. O `cmd/aos` constrói o recorder sem sinks (`model_pricing_env.go`, «um canal, não dois») e o gateway do nó não tem tracer, pelo que o atributo `aos.cost.run_micro_usd` e as métricas de escopo run/árvore se perdem; o burn-down de AOS-259/261/262 soma o custo **por chamada** de cada `turn.recorded`; o `budgetbridge` só tem o seu teste; o `aos-orq` e o `packages/integration` não compõem recorder. **DECISÃO: tecto com despejo do menos-recentemente-usado**, `cost.DefaultRetainedKeys = 4096` chaves por eixo (runs e árvores), `WithRetention` para mudar (valores < 1 ignorados: não há opção sem tecto), sobre um mapa genérico novo em `internal/lru` que não toca a ordem numa leitura. Porquê: limita a memória por construção em qualquer composição (nó, `aos-orq`, integration) sem canalizar o fim-de-run do `NodeService` até ao gateway, e um run activo, tocado a cada chamada, só perde o cumulativo depois de 4096 outros runs. **Rejeitadas**: *fim-de-run explícito* — o gateway não recebe sinal de fim, o recorder não fica no `NodeService`, e não servia os outros compositores; *expiração por inactividade* — precisa de relógio e varrimento, e reduz o cumulativo de um run parado que retome; *não retenção* — partia o contrato documentado de `CostForRun`/`CostForTree` e ~8 testes que o lêem sem sinks. Um run despejado que volte recomeça do zero, incluindo a verificação de overflow do cumulativo, que só dispara perto de MaxInt64. **Correcção no mesmo `Observe`** (encontrada na discovery): um overflow no eixo árvore deixava o run já gravado; as duas somas passam a calcular-se antes de gravar qualquer uma. `TestAOS397_OverflowNaArvoreNaoActualizaORun` falha com a ordem antiga (medido por mutação: «ficou {Tokens:15 CostMicroUSD:105}»); `TestAOS397_OverflowNoRunNaoActualizaAArvore` guarda o caso simétrico, que já era correcto.)*
- [x] O mesmo eixo verificado no `MemoryBurndownSink` e no `cache_sli`; onde não se fechar, o limite fica declarado. *(`cache_sli`: fechado com o mesmo tecto (`cache_sli.DefaultRetainedKeys = 4096`, `WithRetention`) e despejo pelo uso; um run despejado recomeça do zero, incluindo o estado anti-flapping (`Breached`), pelo que o alerta pode voltar a disparar para ele. Não está composto em produção (`NewProduction` não passa `WithCacheSLI`). Testes: `TestAOS397_CacheSLI_RetencaoTemTecto` e `TestAOS397_CacheSLI_RunActivoMantemOAgregado`. `MemoryBurndownSink`: **limite declarado, não fechado** — é o sink de referência que guarda todos os incrementos para os testes inspeccionarem, não está composto em produção e o `cmd/aos` proíbe ligá-lo; o comentário do tipo diz que não serve para um processo de vida longa. O eixo árvore (`treeAggs`) leva o mesmo tecto, embora o `TreeID` continue vazio em produção.)*
- [x] `go test -race -count=1 ./...` no módulo do GW e `bash scripts/ci/routing.sh` verdes. *(Suite `-race` do módulo do GW verde; `routing.sh` verde — 11 cenários, 22+13 testes obrigatórios, cobertura do módulo 87,8% ≥ 80%. Também verdes: `build`, `lint`, `layer-lint` e os testes de preços/gateway do `cmd/aos`.)*

### Estado

**IMPLEMENTADO (2026-09-16).** Os agregados por run e por árvore do `cost.Recorder` e o agregado por (run, tenant) do `cache_sli` têm tecto de 4096 chaves com despejo do menos-recentemente-usado; o custo por chamada e o canal de custo de AOS-259 não mudam. Medido: 10 000 runs deixavam 10 000 entradas, agora deixam 4096. Revisão adversarial independente: nenhum defeito crítico, alto ou médio no código; os baixos (comentários imprecisos, overflow no run sem teste) foram corrigidos. **Limite declarado**: o `MemoryBurndownSink` de referência continua sem tecto.

---

## AOS-399 — O nó pede a posse exclusiva do caminho do audit de governação do gateway

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-265 (audit de governação durável do gateway), AOS-285 (posse de escrita arbitrada pelo SO) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/model_audit_env.go`, `packages/cmd/aos/wal_posse.go`, `packages/substrate/eventstore/wallock.go`, `packages/cmd/aos/aos285_guard_arranque_test.go` |

### Contexto

O nó abre o WORM de governação do gateway a partir de `AOS_MODEL_AUDIT_PATH` em `parseModelAuditFromEnv`, chamado por `parseModelFromEnv` antes do `Bootstrap`. O guard de arranque do AOS-285 (`tomarPosseDoWAL`, no `Bootstrap`) pede a posse do Event Store e do WORM do nó, mas não a deste caminho. Dois processos apontados ao mesmo ficheiro abriam-no ambos: o segundo corria o replay da abertura, que trunca uma cauda incompleta e pode assim cortar a escrita em curso do primeiro, e selava a sua activação da allowlist na mesma partição `modelgw-gov:<board>`. A hash-chain bifurca, e a reabertura recusa a cadeia (medido para o WORM do nó no AOS-284). A variável tem o mesmo nome no `aos-orq` (AOS-395), pelo que a colisão pode vir de outro nó ou de um `aos-orq` no mesmo host. Até aqui só a documentação a impedia. Achado da revisão do AOS-395, onde ficou como residual declarado.

### Objectivo

Um segundo escritor do mesmo `AOS_MODEL_AUDIT_PATH` é recusado no arranque do nó, pela mesma via do Event Store e do WORM detidos, antes de o ficheiro ser aberto.

### Critérios de Aceitação

- [x] A posse exclusiva do caminho é pedida ao SO antes do `audit.OpenFileStore`, pelo mecanismo do AOS-285. *(`parseModelAuditFromEnv` chama `tomarPosse`, o laço de `tomarPosseDoWAL` extraído para ser partilhado, com o caminho como alvo. A posse fica aqui e não na tabela do `Bootstrap` porque este store abre-se antes dele: trancar no `Bootstrap` deixaria o replay correr sobre o ficheiro de outro escritor. O store devolvido (`modelAuditDetido`) fecha o WAL e só depois larga a posse; em produção ambos vivem até ao fim do processo, como antes.)*
- [x] O segundo escritor é recusado pela saída existente de posse detida: `ErrEventStoreJaDetido`, com o ficheiro e a razão, e não `ErrBadModelAudit`. *(A acção do operador é parar o outro escritor, não corrigir o caminho. O processo sai pelo `main` como nas outras recusas de posse: código 1 e a mensagem no stderr.)*
- [x] Teste por processo real. *(`TestAOS399_ProcessoReal_SegundoNoNoMesmoModelAuditRecusa`: compila o nó; o nó A serve com o caminho, o nó B, com Event Store e WORM próprios, é recusado com a mensagem que nomeia `AOS_MODEL_AUDIT_PATH` e o ficheiro, e o WAL de A fica byte a byte igual. Morto A, o mesmo B arranca e reabre a cadeia. Complementos no processo do teste: `TestAOS399_ModelAuditDetidoRecusaSemAbrir` (erro classificado e WAL não criado) e `TestAOS399_CloseLargaAPosseEOWORMDoNoNaoPartilhaOCaminho` (o `Close` larga a posse; um `AOS_WORM_PATH` igual ao caminho do audit é recusado pelo guard do WORM, porque a posse do audit já foi tomada no mesmo processo). **FALHA-ANTES MEDIDA por mutação**: sem a posse, B sai 0 e os três testes falham; com a posse tomada depois do `OpenFileStore`, o teste da ordem vê o WAL criado.)*
- [x] O teste de reabertura do AOS-265 (`TestParseModelAuditFromEnv_Duravel_AbreWORM`) fecha o primeiro store antes de reabrir, como num restart. *(Com o primeiro aberto, a posse recusa a segunda abertura no mesmo processo, que é o comportamento pedido.)*

### Estado

**IMPLEMENTADO (2026-09-16).** O nó recusa arrancar sobre um `AOS_MODEL_AUDIT_PATH` detido por outro processo, antes de abrir o WAL. Verificado: suite de `cmd/aos` verde; falha-antes medida por mutação (sem posse, e com a posse depois da abertura). **Limites declarados**: a posse só protege entre processos que a pedem (o nó desde este ticket, o `aos-orq` desde o AOS-395, onde sai com o código 5); um lock de SO sobre um volume partilhado por rede depende do sistema de ficheiros, como para o Event Store e o WORM do nó.

---

## AOS-406 — Sem fonte de preço o custo fica marcado como não derivado e o SLI de custo deixa de dar verde com zeros

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-259 (canal de custo), AOS-336 (turno não medido) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/model_pricing_env.go`, `packages/kernel/agent-runtime/{model.go,loop.go,turn.go}`, `packages/kernel/agent-runtime/replay/nondeterminism_capture.go`, `packages/substrate/otel-genai/{semconv.go,cost_aggregation.go,slo.go}`, `deploy/server/README.md`, `deploy/node/README.md` |

### Contexto

Em produção o custo de todos os turnos é zero. O nó pede `gpt-4o-mini` em `eu`, e a tabela de preços
embebida não tem esse par; `AOS_MODEL_PRICING_PATH` está vazia. O alias `gpt-4o-mini` é do LiteLLM de
produção e encaminha para `openai/kimi-for-coding` em `api.kimi.com/coding/v1` — lido na configuração
do LiteLLM do servidor a 2026-09-17 (o `deploy/server/litellm/config.yaml` do repositório é um modelo
com as entradas comentadas) —, que o operador paga por **subscrição**, sem preço por token (decisão
do dono na conversa de 2026-09-17). Não há, por isso, tabela a
montar: pôr o preço da OpenAI daria um custo preciso e falso.

O problema é o que o zero fazia a jusante. O span `chat` emitia `aos.cost.micro_usd=0`, o
`turn.recorded` gravava `cost_micro_usd: 0` sem marca, e o SLI `cost_per_trajectory` contava esses
traces como amostras e dava o SLO por cumprido — um verde sem dados, contra a regra anti-vacuidade do
AOS-085. O banner dizia que o zero era ausência de dados; o `/metrics` e o evento durável não.

### Objectivo

Sem fonte de preço, cada turno sai marcado como custo **não derivado** em toda a travessia, e o SLI
de custo por trajectória não conta esses traces.

### Critérios de Aceitação

- [x] **Kernel.** `ModelResponse.CustoNaoDerivado`. O span `chat` leva `aos.cost.undefined=true` e não
      leva `aos.cost.micro_usd` nem `gen_ai.usage.cost_usd`; os tokens continuam no span. O agregado do
      run no `invoke_agent` também sai marcado e sem número (`Result.CustoNaoDerivado`). O
      `turn.recorded` leva `custo_nao_derivado: true`, com `omitempty` — um turno com preço grava os
      mesmos bytes de sempre. É ortogonal ao `usage_ausente` do AOS-336: aqui os tokens foram medidos.
      A captura canónica do replay guarda a marca (também `omitempty`), para um turno retomado não
      voltar a parecer gratuito. *(`TestAOS406_SpanChatSemCustoDerivadoNaoEmiteCusto`, que cobre o `chat` e o `invoke_agent`,
      `TestAOS406_TurnRecordedMarcaOCustoNaoDerivado`, `TestAOS406_CustoNaoDerivadoSobreviveACaptura`.)*
- [x] **Substrato.** `aos.cost.undefined` no vocabulário; a agregação por trace propaga a marca
      (`UsageTotals.CostUndefined`); o `cost_per_trajectory` retira da amostra **o trace inteiro** que
      tenha um chat sem custo derivado — uma soma parcial subestimaria a trajectória — e, sem traces com
      custo, fica sem amostras (`avaliavel="0"`). *(`TestAOS406_SLIDeCustoSemPrecoNaoEAvaliado`, que
      falha antes: os dois traces contavam como amostras de custo zero; `TestAOS406_TraceMistoSaiInteiro`.
      **FALHA-ANTES por mutação**: sem a exclusão no SLI, os dois falham.)*
- [x] **Nó.** Quando a tabela em vigor não cobre `(AOS_MODEL_NAME, AOS_MODEL_REGION)`, `parseModelFromEnv`
      envolve o cliente do gateway em `custoNaoDerivadoClient`, que marca cada resposta e zera o custo;
      com preço, o cliente não é decorado. O banner da postura de custo passa a descrever a marca, a
      exclusão do SLI e a postura de subscrição. *(`TestAOS406_SemPrecoOClienteMarcaOCusto` — o caso de
      produção, `gpt-4o-mini` em `eu` com a tabela embebida, sem tools —,
      `TestAOS406_ComToolsODecoradorFicaPorDentroDoEnriquecedor` — a cadeia real, com tools: o decorador
      fica por dentro do enriquecedor e a marca atravessa-o —, `TestAOS406_ComPrecoOClienteNaoEDecorado`,
      `TestAOS406_DecoradorMarcaEZeraOCusto`, `TestAOS406_BannerDeclaraAPosturaDeSubscricao`.)*
- [x] **`/metrics` honesto.** Sem fonte de preço o SLI de custo nunca tem amostras neste nó; o
      `aos_alert_firing` do alerta de custo sai com `produtor="0"` («a regra nunca dispara») e não com
      `produtor="1"` («janela vazia, pode disparar»). `Config.CustoSemFontePreco` é escrito pelo caminho
      por ambiente com o mesmo juízo que compõe o decorador; um `cfg.Model` injectado deixa-o a false.
      *(`TestAOS406_AlertaDeCustoSemFonteDePrecoNaoTemProdutor`.)*
- [x] **Limites declarados.** (1) Um run capturado **antes** do deploy e retomado depois devolve as
      respostas capturadas sem a marca; se o trace só tiver turnos reproduzidos, entra no SLI como custo
      zero — só na transição. (2) A exclusão do trace misto é inalcançável no nó (a postura de preço é
      do processo inteiro), mas a função é genérica: uma soma parcial já acima do tecto também sai da
      amostra. (3) Superfícies de biblioteca que não distinguem «sem custo» (`BuildRunView`,
      `trajectory-surface`, `TraceDiff`) não são compostas no nó e ficam fora deste ticket.
- [x] O orçamento em tokens e o tecto em dólares não mudam: sem preço, `AOS_BUDGET_MAX_COST_MICRO_USD`
      continua recusado no arranque (DEF-277), e a dimensão que decide é tokens.
- [x] `deploy/server/README.md` (ponto 9), `deploy/node/README.md` (`AOS_MODEL_PRICING_PATH`) e
      `tecnica/08` §7.1 descrevem a marca e a postura de subscrição.
- [ ] **Evidência de sistema.** Depois de um deploy, um run em produção grava `custo_nao_derivado: true`
      nos `turn.recorded`, e o `/metrics` dá `aos_slo_samples{sli="cost_per_trajectory"}` a 0 com o
      alerta de custo `avaliavel="0"` e `produtor="0"`, em vez de amostras com custo zero.

### Estado

**IMPLEMENTADO** a 2026-09-17; a evidência de sistema fica pendente do deploy.

---

## AOS-421 — O nó declara a escada de tiers, e o refino de roteamento passa a correr no binário que está em produção

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa o ADR-021: o scoring ponderado está entregue (AOS-269) e composto
     no módulo do gateway (AOS-280). O que falta é a fonte de verdade da ESCADA, que vive no
     deployment. As citações ao ADR-021 são menções. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 — Model Gateway e Custos |
| Fase | Prontidão de produção |
| Milestone | v1.1 |
| Tipo | feature (exige fonte de verdade nova) |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-269 (scoring), AOS-280 (composição do refino no GW) |
| Bloqueia | DEF-280-NO e DEF-280-REGIAO |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/modelgatewaywiring.go` (composição do `ProductionConfig`), `packages/platform/model-gateway/production_routing.go` (`newRefineStage`, `composeRoutingStage`, `unpricedLadderPairs`, `modelSwapRecorder`), `docs/adr/ADR-021-*.md` |

### Contexto

O refino de roteamento **existe, está composto e provado** no módulo do gateway — e **nunca corre
no binário do nó**. A cadeia `failover → refino` só se arma quando o deployment declara
`RoutingConfig.Tiers`; sem isso, `newRefineStage` devolve `(nil, nil)` e o `composeRoutingStage`
devolve só o failover.

Medido: **nenhum ficheiro não-teste do repositório preenche `Routing:`** — nem o nó nem o
composition root —, e `AOS_MODEL_TIERS` não existe em lado nenhum. O binário que está em produção
roteia só pelo failover.

**O que fica desligado por causa disso**, tudo já escrito e testado do lado do gateway:

| Desligado hoje | O que faz quando armar |
|---|---|
| O scoring assinado do ADR-021 | escolha por custo, carga, latência e saúde, com tabela de pesos pinada e carregamento fail-closed |
| O classificador de produção | candidatos = inventário ∩ regiões legais ∩ saudáveis |
| A validação de perfis no arranque | um perfil por classe que não bata com a tabela assinada recusa o arranque |
| **`unpricedLadderPairs` / `ErrRoutingPriceCoverage`** | **recusa de ARRANQUE** quando falta preço a um par alcançável, em vez de uma chamada recusada a meio de um run |

Esta última é o ganho que mais conta, e é também a razão pela qual o **DEF-279** deixa de ter eixo
de modelo: a verificação «todos os pares alcançáveis» que ele pede já está escrita aqui — modelos
da escada × regiões das contas, filtrada pela allowlist — e arma com este ticket.

### Porque é que o DEF-280-REGIAO vem no mesmo ticket

O `modelSwapRecorder` é construído **na mesma expressão** que compõe o refino. Enquanto o nó não
declarar tiers, estender a sua condição de selagem **não muda um único byte do WORM**: seria código
que nada executa. Os dois eixos são o mesmo trabalho, e separá-los produzia um PR sem efeito.

### O que este ticket tem de CRIAR, e é o que o torna um ticket e não uma limpeza

Não existe fonte de verdade para a escada: **não há env, não há formato, não há artefacto
assinado**. É o mesmo vazio que bloqueou o AOS-409 (o 4.º eixo do `IsEffectTool` não tinha de onde
vir) e, antes dele, o DEF-275. O ticket tem de a criar antes de poder ligar o
`ProductionConfig.Routing`.

### Decisões a tomar primeiro (do dono)

1. **Que modelos entram na escada.** Não se adivinha: é decisão de deployment. Cada modelo
   declarado tem de estar coberto **pela allowlist regional do board** E **pela tabela de preços da
   região** — senão o arranque passa a ser recusado por `ErrRoutingPriceCoverage`, que é a direcção
   certa do erro mas tem de ser uma escolha consciente.
2. **Por onde entra a escada.** (a) Variável de ambiente (`AOS_MODEL_TIERS`), no molde do resto da
   superfície do nó — simples, e a postura é declarada no banner; (b) artefacto **assinado**, no
   molde da tabela de pesos do ADR-021 — mais caro, e coerente com o facto de a escada decidir para
   onde vai dinheiro e que fronteira de soberania se atravessa. A (b) é a que combina com o resto
   da postura do gateway; a (a) é a que se entrega esta semana.
3. **DEF-280-REGIAO: selo por chamada ou correlação.** Ou a resolução de região do failover passa a
   selar um `GovRecord` próprio — custo: **+1 registo WORM em cada chamada com failover** — ou
   aceita-se que a correlação com o registo de atribuição, selado na mesma chamada, basta ao
   auditor. É uma decisão sobre volume de trilho em produção, não sobre código.

### Critérios de aceitação

- [ ] A escada tem uma fonte de verdade declarada, com formato fixado e a postura no banner de
      arranque.
- [ ] Com a escada declarada, o `ProductionConfig.Routing` é preenchido e o refino **arma** —
      provado no binário do nó, não só no módulo do gateway.
- [ ] Um modelo da escada sem preço na região alcançável **recusa o arranque**, e o teste prova-o
      pela mensagem de `ErrRoutingPriceCoverage`.
- [ ] Um modelo da escada fora da allowlist regional do board não é candidato, e há teste negativo.
- [ ] A decisão (3) fica implementada e declarada — selo próprio ou correlação, com a razão escrita.
- [ ] Verificado em produção: uma chamada cujo modelo efectivo difere do pedido, com o trilho de
      governação a mostrá-lo.

### Fora de âmbito, declarado

- **DEF-279** (cobertura de preço por região). O seu eixo de MODELO fica resolvido por este ticket;
  o que sobra é o eixo de REGIÃO, cujo gatilho continua a ser **a segunda conta no inventário do
  keypool**, que o nó não tem. Fica aberto, com o gatilho já documentado no registo.
- A mudança do estágio de roteamento em si: o `failover` continua a ser o primeiro elo, e este
  ticket acrescenta o refino a seguir — não o substitui.

### Riscos

| Risco | Mitigação |
|---|---|
| Declarar a escada recusa um arranque que hoje funciona, por lacuna de preço ou de allowlist | É a direcção certa do erro, mas tem de ser verificada em staging antes de produção — a recusa é no arranque, e um nó que não arranca é uma interrupção |
| A escada por env é configuração não assinada a decidir para onde vai dinheiro | É a decisão (2); se a resposta for (a), fica declarado como resíduo com eixo próprio |
| Armar o refino muda o caminho quente de TODAS as chamadas de modelo | O `failover` mantém-se como primeiro elo; o refino só decide entre candidatos que ele já validou |

---

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

## AOS-490 — O adaptador do gateway projecta o tail em mensagens nativas, com continuidade do raciocínio

<!-- Implementa o ADR-036 §2.4 a §2.7. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Prontidão para utilizadores reais |
| Tipo | feature |
| Prioridade | P1: é a forma para a qual os modelos de function-calling são treinados, e a que suporta chamadas paralelas e modelos de raciocínio |
| Estimativa | L |
| Dependências | AOS-489 (tail estruturado na `PromptView`), AOS-278 e AOS-394 (identidade e correlação por run no adaptador), AOS-486 (oferta de tools por run) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-protocolo-tool-use-2026-10-03.md` §5, `packages/platform/model-gateway/runtime_adapter.go`, `port/port.go`, `port/normalize.go`, `packages/kernel/agent-runtime/model.go`, `replay/nondeterminism_capture.go` |

### Contexto

O nó envia ao modelo o prompt inteiro como uma mensagem de utilizador. O contrato do gateway já tem
`assistant` com `tool_calls` e `tool` com `tool_call_id`, e o planeador do `aos-orq` já envia
`system` e `user` separados pela mesma pipeline; o adaptador do nó não os usa. Com o AOS-489 o tail
passa a ter a conversa estruturada, e este ticket projecta-a na forma nativa do provider.

**Decidido pelo dono (2026-10-03, D1):** as duas fases saem numa só entrega. Para conter o risco, a
projecção nativa é seleccionável por configuração, e o texto único continua disponível.

### Medição de 2026-10-03 (autorizada pelo dono)

Três pedidos ao LiteLLM de produção, de dentro da rede `aos_default`, com conteúdo inócuo e a chave
do modelo montada só-de-leitura. **Fora da cadeia de governação do nó:** sem NHI, sem allowlist e
sem selo `modelgw-gov`. Alias `gpt-4o-mini`, encaminhado para `openai/kimi-for-coding`.

| Pedido | Forma | Resultado |
|---|---|---|
| 1 | `system` + `user` + `tools` | 200 em 3,2 s. `finish_reason=tool_calls`; a mensagem traz `content` vazio, `tool_calls` com id do provider (`tool_…`), **`reasoning_content`** (155 caracteres) e `provider_specific_fields`; `usage.completion_tokens_details.reasoning_tokens=33` |
| 2a | pedido 1 + `assistant` com `tool_calls` (id cunhado `step-000001-tool-1`, `content` vazio, **sem** `reasoning_content`) + `tool` com o mesmo `tool_call_id` | 200 em 3,9 s. `finish_reason=stop`: o modelo respondeu com o conteúdo do resultado, **sem voltar a pedir a tool**. 348 tokens de entrada |
| 2b | o mesmo, **com** o `reasoning_content` do pedido 1 no `assistant` | 200 em 5,0 s. `finish_reason=stop`, resposta equivalente. 380 tokens de entrada: os 32 a mais são o raciocínio, logo o LiteLLM entrega-o ao provider |

O que fica medido:

- O `reasoning_content` **chega** na resposta pelo provider genérico `openai/` do LiteLLM, e
  **sobrevive** no pedido seguinte.
- **Não é exigido** por este provider: o turno nativo sem ele foi aceite. Devolvê-lo é opção, não
  obrigação, e custa os tokens do raciocínio em cada turno seguinte.
- Um **id cunhado pelo runtime** é aceite como `tool_call_id`, desde que seja o mesmo no `assistant`
  e no `tool`. O id do provider não é necessário.
- `"content": ""` num `assistant` só com tool calls é aceite.
- O conteúdo do `tool` ia num envelope JSON com `taint`; o raciocínio do modelo refere-o («tool
  output stdout_text has untrusted taint»): a proveniência dentro da mensagem é lida.
- O `usage` não trouxe `prompt_tokens_details` (tokens em cache) nestes pedidos, de 228 a 380 tokens.
  Se o provider faz cache de prefixo, não ficou visível a esta escala.

Limites: três pedidos, um só modelo, um só turno de tool; não mede a taxa de repetição em runs
reais nem o comportamento com várias chamadas no mesmo turno.

### Objectivo

O provider recebe a conversa em turnos nativos, derivados de forma determinística do tail; o
raciocínio do modelo é transportado e capturado como carga opaca; e o que foi enviado continua a
poder reconstruir-se a partir do registo.

### Âmbito decidido (2026-10-03, depois da medição)

- **Raciocínio: capturado, não devolvido.** A medição mostrou que o provider de produção não o
  exige. Devolvê-lo custaria os tokens do raciocínio em cada turno seguinte e obrigava a pô-lo no
  tail. Nesta entrega o contrato e a resposta do modelo transportam-no, a captura guarda-o selado
  por titular, e nenhum pedido o leva.
- **`max_tokens` e `temperature` do run não vão no pedido.** Passou para «Fora de âmbito», com a
  razão.
- **A projecção nativa é o valor por omissão do nó** (`AOS_MODEL_PROJECTION=native`), e só se
  aplica a runs no layout 1.4.0.

### Critérios de Aceitação

- [x] **Medição prévia, antes do desenho final:** um pedido ao LiteLLM de produção com turnos
      nativos e tools, para saber se o `reasoning_content` chega na resposta, se é exigido no pedido
      seguinte e se sobrevive ao proxy. O resultado fica registado neste ticket. *(Ver «Medição de
      2026-10-03», abaixo.)*
- [x] ADR-036 (o tail é canónico; o que vai para o provider é uma projecção): o `prompt_hash` é o hash do tail canónico e
      a projecção é função determinística e versionada dele; como viaja a proveniência
      (`taint`, recusas) dentro das mensagens; como se separam segmentos trusted e untrusted; o que
      se captura do raciocínio. Emenda as frases do ADR-034 e de `tecnica/` que dizem que o hash é o
      dos bytes enviados.
- [x] O adaptador projecta: `system` (o protocolo nativo e o `system` do run), `user`
      (entradas e objectivo, cada segmento com a sua linha de cabeçalho), e por turno `assistant`
      com `tool_calls` e um `tool` por chamada, com o `id` do tail como `tool_call_id`. Cada
      `tool_call` do tail tem exactamente um `tool`, incluindo as negadas, as falhadas e a
      escalada; as chamadas que o loop não chegou a despachar depois de uma escalada não têm
      segmento no tail e não entram no `assistant`. Um tail que não o permita não produz pedido
      (`ErrNativeProjection`). *(Evidência: `projection.go`;
      `TestAOS490_PedidoNativo_GoldenDerivadoAMao`, `TestAOS490_Agrupamento_PorTurno`,
      `TestAOS490_Escalada_SoAsChamadasDespachadas`, `TestAOS490_Invariante_TailInvalidoNaoSai`
      (inclui o id repetido entre turnos e o turno só com texto a meio do tail),
      `TestAOS490_Argumentos_FormaNoWire`, `TestAOS490_NomeDeFuncaoNoWire`,
      `TestAOS490_ToolFalhada_RotuloNoCabecalho`, `TestAOS490_AvisoDeSerieEsteril_EntreTurnos` no
      `model-gateway`; `TestAOS490_NoNativo_ChamadaPermitida` no nó, com o corpo dos dois pedidos
      derivado à mão.)*
- [x] A proveniência não se perde: o conteúdo de cada mensagem `tool` é o segmento `tool_result`
      renderizado pelo kernel — a linha de cabeçalho com `taint`, `id`, `name` e os rótulos de
      recusa, e o corpo neutralizado; os segmentos de uma mensagem `user` levam cada um o seu
      cabeçalho; o texto do modelo no `assistant` é neutralizado. A projecção não tem saneamento
      seu (`agentruntime.RenderTailSegment`, `NeutralizeContent`). *(Evidência:
      `TestAOS490_Forja_NenhumCabecalhoNasceDeConteudo`, `TestAOS490_KindDesconhecido_EDados`,
      `TestAOS490_Protocolo_Restricoes`; `TestAOS490_RenderTailSegment_ReproduzOMaterializado` no
      kernel; `TestAOS490_NoNativo_RecusaPelaListaBranca` no nó.)*
- [x] Raciocínio: o contrato do gateway (`port.Message.ReasoningContent`, contrato 1.2.0) e a
      `ModelResponse` (`Reasoning`) transportam o raciocínio do turno como carga opaca, byte a
      byte, em qualquer forma JSON que o provider lhe dê (uma string guarda-se como string, outra
      forma como os bytes JSON que vieram; nunca derruba a resposta); a captura guarda-o (`omitempty`; dentro do conteúdo cifrado por titular no modo
      selado, redigido no modo sensível); a retoma e o replay devolvem-no igual; **nenhum pedido
      o leva** (`ChatRequest.MarshalWire` retira-o). `TestModelBoundaryCarriesNoAuthority`
      continua verde. *(Evidência: `TestAOS490_Travessia_RaciocinioETokensEmCache`,
      `TestAOS490_MarshalWire_NaoEnviaRaciocinio`, `TestAOS490_Raciocinio_QualquerFormaJSON`,
      `TestAOS490_Travessia_RaciocinioQueNaoEString`, `TestAOS490_Captura_*` em `replay`,
      `TestAOS490_Raciocinio_ForaDoPromptDosEventosEDosSpans` no kernel, e o ponto (6) de
      `TestAOS490_NoNativo_ChamadaPermitida`.)*
- [x] A projecção é seleccionável por configuração do nó (`AOS_MODEL_PROJECTION`: `native`, por
      omissão, ou `text`; com o gateway ligado, outro valor recusa o arranque), declarada no banner, e o modo usado
      fica no manifesto do turno (`projection`, `projection_version`, `omitempty`). Com o texto
      único, e para qualquer run no layout 1.3.0, o pedido é byte-idêntico ao anterior.
      *(Evidência: `TestAOS490_Env_VocabularioFechadoEBanner`,
      `TestAOS490_TextoUnico_ByteIdentico`, `TestAOS486_NoComGateway_SemLista_ByteIdentico` — que
      passou a fixar `text` e prova que, no layout 1.4.0, o pedido em texto único é o de antes do
      AOS-490 (um só `user` com o prompt materializado; os bytes do preâmbulo são os do AOS-489,
      que mudou face à base `4f4d419`). Byte-idêntico à base só o layout 1.3.0 —,
      `TestAOS490_RetomaECrashResumeReproduzemAsMensagens`,
      `TestAOS490_Manifesto_ProjeccaoETokensEmCache`.)*
- [x] Desempenho: o gateway lê os tokens em cache do wire do provider
      (`prompt_tokens_details.cached_tokens`) para `CacheReadTokens`, e o valor chega a
      `cache_read_tokens` do `turn.recorded`. A estimativa de admissão continua sobre o prompt
      materializado nas duas projecções; a diferença está declarada em `tecnica/06` §5 e em
      `integration/model_admission.go`, e não é corrigida nesta entrega. *(Evidência:
      `TestAOS490_Resposta_RaciocinioETokensEmCacheDoWire`, `TestAOS490_TokensEmCache_Regra`
      (as duas vias — o campo do wire e o `cache_read_tokens` de topo — com a mesma regra: sem
      sinal negativo e com tecto em `prompt_tokens`), `TestAOS490_TokensEmCache_EmbeddingsEStream`, e o
      ponto (7) de `TestAOS490_NoNativo_ChamadaPermitida`.)*
- [x] Gates: `routing`, `replay`, `security`, `apex`, `lint`, `build`, `layer-lint`.
      *(Evidência: corridos sobre a árvore da entrega, 2026-10-03.)*
- [ ] Verificado em produção com o modelo vivo: o objectivo multi-nó cumpre-se com a projecção
      nativa, sem repetições, e com a taxa de tokens em cache medida.

### Fora de âmbito

- Streaming, `parallel_tool_calls` e despacho paralelo de tools. O caminho de streaming do
  adaptador HTTP não lê o `reasoning_content` nem o `prompt_tokens_details`.
- Levar `max_tokens` e `temperature` do run ao pedido. A documentação do provider de produção
  desaconselha `temperature` e exige um `max_tokens` alto com raciocínio activo: que valores o nó
  envia é uma decisão à parte, com medição própria.
- Devolver o raciocínio ao provider no `assistant` do turno seguinte. Fica capturado; devolvê-lo
  exige pô-lo no tail (é de lá que a projecção sai) e paga os seus tokens em cada turno.
- Corrigir a estimativa de admissão para a forma nativa do pedido.
- Uma ferramenta que reconstrua, do registo de um run, o pedido enviado em cada turno. O registo
  permite-o (o replay refaz o tail e `ProjectNative` está exportada), mas nada lê
  `manifest.projection`/`projection_version`, e o replay não recusa uma versão de projecção que
  não conheça.

### Notas da revisão adversarial (2026-10-03)

- **Raciocínio em claro fora de produção.** Sem selo por titular nem modo sensível, a captura
  guarda o raciocínio em claro, como guarda o texto do modelo. «Fora de eventos em claro» vale
  para a captura selada (produção) e para a sensível.
- **`function.arguments`.** JSON válido vai cru; omitido por tamanho vai como
  `{"aos_args_omitted_bytes":N,"aos_args_digest":…}`; o que não é JSON válido vai como
  `{"aos_args_invalid_bytes":N,"aos_args_digest":…}` e vazio como `{}`. Um modelo pode emitir
  esse mesmo objecto; não muda autorização nenhuma.
- **`function.name`.** Um nome que não cabe no wire sai como `aos_invalid_tool_name`, que o nó
  recusa em `AOS_MODEL_TOOLS` (`TestAOS490_NomeReservadoRecusadoEmModelTools`).
- Blocos de raciocínio assinados (Anthropic) e itens cifrados (OpenAI Responses): o contrato fica
  preparado para carga opaca, mas só a forma de texto por mensagem é implementada e medida.
- A confirmação contratual do uso do `kimi-for-coding` em produção.

### Verificação em produção (2026-10-04, v0.1.45)

- **Banner do nó:** `projeccao do pedido ao modelo (EPIC-06/AOS-490): MENSAGENS NATIVAS
  (versao 1.0.0)`. O `.env` do servidor não foi alterado; a forma nativa é a de omissão.
- **Manifesto dos turnos** do plano `plan-e2e-v0145-1791114237` (três `turn.recorded`, dois em
  `~n1` e um em `~n2`): `"projection": "native"`, `"projection_version": "1.0.0"`,
  `"assembly_version": "1.4.0"` em todos.
- **O provider aceitou a forma nativa:** o segundo turno do `~n1` levou um `assistant` com
  `tool_calls` e a mensagem `tool` correspondente, com o id cunhado pelo runtime, e devolveu a
  conclusão. Nenhum `400`.
- **Tokens em cache:** o segundo turno do `~n1` regista `cache_read_tokens=512` em 871 tokens de
  entrada. O primeiro turno de cada nó não regista o campo.
- **Efeito:** o nó que lê o documento não repetiu a tool call (ver a tabela no AOS-489).

**Por verificar:** várias tool calls num mesmo turno e uma tool call no histórico cujo nome
não esteja em `tools` não ocorreram neste run.

### Estado

**ABERTO.** Entregue e em produção na v0.1.45; a forma nativa foi aceite pelo provider e verificada num run (2026-10-04). Fecha com o AOS-489.

---

## AOS-491 — O motivo de paragem do modelo chega ao runtime, à captura e ao registo do turno

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: é instrumentação. Transporta um campo que o adaptador hoje descarta; a decisão sobre o que o runtime faz com ele é do AOS-493. O ADR-036 é citado só como contexto do contrato do gateway. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A0 |
| Tipo | feat |
| Prioridade | P0: sem este campo o runtime não distingue uma conclusão de uma resposta truncada, e o defeito medido a 2026-10-04 não aparece em nenhuma métrica |
| Estimativa | S |
| Dependências | AOS-490 (projecção nativa e contrato 1.2.0 da porta) |
| Bloqueia | AOS-493 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/analise-fronteira-runtime-modelo-2026-10-04.md` §3 e §7, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md`, `packages/platform/model-gateway/runtime_adapter.go`, `packages/platform/model-gateway/port/port.go`, `packages/kernel/agent-runtime/model.go`, `packages/kernel/agent-runtime/turn.go` |

### Contexto

O adaptador do nó lê a resposta do provider e deita fora o `finish_reason`
(`runtime_adapter.go`, na conversão para `ModelResponse`). O runtime recebe só o texto, as tool
calls e um booleano `Final`. Uma resposta cortada por limite de tokens, uma recusa por filtro de
conteúdo e uma conclusão legítima chegam iguais, e o `turn.recorded` não regista nenhuma delas.

Na série de 2026-10-04 em produção (v0.1.45), dois planos em dez fecharam verdes sem cumprir o
objectivo. O observador de tool calls só corre quando há pelo menos uma chamada, por isso o defeito
não é visível em nenhum contador.

### Objectivo

O motivo de paragem de cada turno, normalizado num vocabulário fechado, viaja do provider até ao
runtime, à captura do turno e ao `turn.recorded`. O comportamento do loop não muda neste ticket.

### Critérios de Aceitação

- [x] `ModelResponse` ganha o motivo de paragem num vocabulário fechado: `stop`, `tool_calls`,
      `length`, `content_filter`, `other`, e vazio quando o provider não o envia. Um valor do
      provider fora do mapa conhecido vira `other`; o valor bruto não entra no runtime.
- [x] O adaptador do gateway preenche-o a partir do `finish_reason` da primeira escolha da
      resposta, tanto na projecção nativa como na de texto único.
- [x] A captura do turno guarda o motivo, e o replay devolve-o igual. Uma captura gravada antes
      deste ticket reproduz-se com o motivo vazio, sem divergência de `prompt_hash` nem de
      trajectória.
- [x] O `turn.recorded` grava o motivo de paragem e o número de tools oferecidas ao modelo no
      turno. Os dois campos são aditivos: quem lê eventos antigos não parte.
- [x] Contador novo no `/metrics` do nó: turnos por motivo de paragem.
- [x] O loop termina exactamente nos mesmos turnos que antes (teste de não-regressão sobre os
      goldens de replay existentes).
- [x] O catálogo de eventos e a documentação do contrato da porta registam os campos novos; a
      versão do contrato sobe em MINOR.

### Fora de âmbito

- Mudar a regra de terminação ou o desfecho do run (AOS-492, AOS-493).
- Parâmetros de amostragem, `tool_choice` e `max_tokens` no pedido.
- Streaming.

### Verificação em produção (2026-10-05, v0.1.46)

Imagem `sha256:f9a66635…` em produção, nó em observação (banner «veredicto de conclusao (EPIC-02/AOS-493): EM OBSERVACAO»). Um pedido de plano real pela fila, com o objectivo multi-nó de sempre: `plan-e2e-v0146-1791189316`, terminal, `exit_code=0`, 26 s.

- Os três `turn.recorded` do plano levam `stop_reason`: `tool_calls` no turno que pediu a
  tool, `stop` nos dois que concluíram. É o primeiro registo do vocabulário que o provider de
  produção envia; só estes dois valores foram observados.
- `tools_offered` é 1 nos turnos do nó com tool e está ausente no nó sem tools.
- `/metrics`: `aos_model_turns_total{stop_reason="stop"} 2` e `{stop_reason="tool_calls"} 1`.

**Por observar:** `length`, `content_filter` e valores fora do mapa nunca ocorreram.

### Estado

**IMPLEMENTADO (2026-10-04); por verificar em produção.** Revisão adversarial independente sem
achados bloqueantes (44 mutações; as que escaparam ganharam teste).

Por verificar em produção: o contador `aos_model_turns_total` no `/metrics` e o `stop_reason`
nos `turn.recorded`, com o vocabulário que o provider de produção envia de facto.

Resíduos declarados:

- `tools_offered` vem do pedido ao provider e **não está na captura**: numa retoma, o turno
  reproduzido volta com zero em memória (o evento original fica no log). Se o AOS-493 precisar
  deste número de forma reproduzível, tem de ir para a captura ou ser derivado do manifesto.
- Só o adaptador do gateway declara o motivo; os outros clientes do modelo somam em
  `unreported`. O caminho de streaming não o transporta.
- O motivo de paragem fica em claro numa captura selada e sobrevive ao crypto-shredding. É um
  facto sobre o turno, já em claro no `turn.recorded`, e não contém conteúdo do titular.
- Depois de um crash entre as duas escritas, o `turn.recorded` e a captura podem discordar no
  motivo. É a mesma classe que já existia para os outros campos do turno.

---

## AOS-504 — Projecção nativa 1.1.0: fim de segmento inforjável, e o objectivo deixa de se confundir com dados

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A1 (recuperação) |
| Tipo | fix |
| Prioridade | P1: o nó de resumo recusa o próprio objectivo em cerca de 1 plano em 20, e nenhum contrato o apanha |
| Estimativa | S |
| Dependências | AOS-490 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-a1-recuperacao-2026-10-06.md` §4 e §8, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A1), `docs/adr/ADR-036-o-tail-e-a-forma-canonica-da-conversa.md` §2.4 a §2.6, `packages/platform/model-gateway/projection.go`, `packages/platform/model-gateway/runtime_adapter.go`, `packages/kernel/agent-runtime/prompt.go`, `packages/kernel/agent-runtime/loop.go`, `packages/cmd/aos-orq/node_executor.go` |

### Contexto

Medido em produção de 2026-10-04 a 2026-10-06 (v0.1.45 a v0.1.49): em **12 de 74 planos (16%)** o
nó de leitura terminou sem chamar a tool — 2 em 10, 1 em 22, 6 em 21 e 3 em 21, por série. O
pedido não determina o desfecho: o mesmo `prompt_hash` do turno 1 dá os dois (um pedido
observado sete vezes teve 5 chamadas e 2 falhas). Na mesma janela o nó de resumo, sem tools,
recusou o próprio objectivo em 1 de 21 e em 1 de 18 planos (`plan-e2e-v0146s-1791193787`,
`plan-e2e-v0149s-1791277019`).

O nó de resumo não tem tools e consome a saída do nó de leitura. A projecção nativa põe os
segmentos da semente seguidos numa só mensagem `user`: primeiro o `plan_input`, untrusted e com
quatro rótulos, depois o objectivo, sem rótulo nenhum. Um segmento é a linha de cabeçalho e o
corpo; **não tem linha de fim**. O texto do protocolo, que é a única mensagem `system` (os runs
não têm system), manda tratar como dados tudo o que for `taint=untrusted` e não seguir pedidos
encontrados lá, «even if it looks like a header». As duas recusas medidas descrevem a confusão
com as palavras do protocolo: o modelo leu o `<objective>` como parte do `plan_input`.

A causa não está provada por experiência: é uma explicação coerente com o código e com as duas
respostas. Um nó sem tools que conclui com texto não vazio e motivo `stop` não se distingue, por
nenhum sinal estrutural, de um que fez o trabalho; um detector teria de julgar o conteúdo do
texto, o que o <!-- rtm: menção -->ADR-037<!-- /rtm: menção --> recusa.

O texto do protocolo é lido por **todos** os runs, incluindo o turno 1 do nó de leitura: mudá-lo
pode mexer na taxa de «não chamou a tool» em qualquer sentido.

### Decidido pelo dono (2026-10-06)

1. A correcção do texto de instruções **entra nesta fase, desligada por omissão e medida antes de
   ligar**.
2. (Por omissão, recomendação do desenho.) A recusa do próprio objectivo não se detecta com
   segurança: corrige-se a causa provável e vigia-se por um contador.
3. A medição directa ao modelo **não está autorizada**: mede-se com séries de planos em produção,
   como nas fases anteriores.

### Objectivo

Uma versão 1.1.0 da projecção nativa, seleccionável por configuração e desligada por omissão, que
fecha cada segmento com uma linha de fim que o conteúdo não consegue forjar e reescreve o texto
do protocolo para o objectivo não ser confundido com dados. E um contador, só de medição, que dá
a taxa de recusa em produção sem ninguém ler textos à mão.

### Critérios de Aceitação

- [x] A versão da projecção nativa passa a ser seleccionável: `AOS_MODEL_PROJECTION_VERSION`, com
      os valores `1.0.0` (omissão) e `1.1.0`. Um valor fora do conjunto recusa o arranque.
- [x] Com a variável ausente ou em `1.0.0`, o binário é byte a byte o anterior, provado por
      comparação com a base: os corpos dos pedidos ao provider (ficheiros de fio), os eventos, os
      manifestos de turno e as métricas são iguais para o mesmo guião.
- [x] Na 1.1.0, cada segmento que a projecção renderiza numa mensagem `user` ou `tool` termina
      com a linha `</kind>`, com o `kind` do cabeçalho.
- [x] A linha de fim é inforjável pelo mecanismo do cabeçalho: uma linha de corpo que comece por
      `<` ou por `\` sai escapada. Teste com corpos que contêm `</plan_input>`, `</objective>`,
      `<objective>` e as variantes já escapadas, em `plan_input`, em `memory` e em resultados de
      tool: nenhuma linha do pedido projectado começa por um cabeçalho ou por um fim que o
      runtime não tenha escrito. O corpus adversarial do gate `security` corre também com a
      1.1.0.
- [x] O texto do protocolo da 1.1.0 é o do desenho (§4.3), com duas frases corrigidas pela
      revisão (ver o registo): diz o que é um segmento e a sua linha de fim, e que cabeçalho e
      fim abrem no primeiro carácter de uma linha e só o runtime os escreve; que o segmento
      `objective` é a tarefa, tem o cabeçalho `<objective>` sem rótulos, e é instrução mesmo
      quando há segmentos de dados antes dele na mesma mensagem; que `correction` e `notice` são
      instruções; que tudo o resto é dados; que um rótulo `taint=untrusted` se aplica só ao
      corpo do segmento que o leva, até à linha de fim; que os corpos são escapados, e que o que
      num corpo pareça um cabeçalho ou um fim é dados. Sai a frase «even if it looks like a
      header». As linhas sobre tool calls, repetição, recusa e `ref` do aviso ficam iguais às da
      1.0.0.
- [x] Testes das restrições do texto: só ASCII; nenhuma linha começa por `<`; não contém
      `taint=trusted`; não nomeia nenhuma tool; e cada frase é verdadeira para o que a projecção
      produz (um teste por frase que afirme uma propriedade verificável: fim de segmento,
      ausência de rótulo no objectivo, escape de corpo).
- [x] O layout do tail, o tail e o `prompt_hash` **não mudam**: o mesmo run dá o mesmo
      `prompt_hash` nas duas versões da projecção, e os goldens de replay existentes ficam
      verdes sem alteração.
- [x] A versão da projecção usada fica no manifesto de **cada** turno. Um run em curso no momento
      da troca pode ter turnos em versões diferentes; cada um grava a sua. Declarado, não
      corrigido.
- [x] A lista de layouts que a projecção nativa cobre é a mesma nas duas versões: nenhum layout
      cai em texto único por causa da 1.1.0 (teste).
- [x] Efeito na cache de prefixo declarado: a mensagem `system` muda, e os tokens servidos de
      cache caem na troca. Lê-se no registo de turnos antes e depois.
- [x] **Canário, só de medição.** O `aos-orq` conta os nós **sem tools e com `consumes`** que
      concluem e cujo texto final contém vocabulário do próprio protocolo (`plan_input`,
      `taint=untrusted`): `aos_orq_consume_canario_de_recusa_total`, ao lado do total de nós
      dessa classe. Um teste prova que o canário não muda o estado do nó, a saída publicada, os
      eventos do plano, o `detail` nem o código de saída, e que nenhum ramo de decisão o lê. O
      texto não entra em nenhuma métrica, log ou evento.
- [x] O canário é corrido contra os textos das recusas medidas que existirem em cópia local, e o
      resultado fica registado no ticket: é um limite inferior, e uma recusa que não use as
      palavras do protocolo escapa-lhe.
- [x] A emenda ao ADR-036 (§2.4 a §2.6) regista a 1.1.0, a linha de fim, o texto do protocolo e a
      versão escolhida por configuração. A RTM é regenerada no mesmo PR.
- [x] Revisão adversarial independente com mutações, antes da fusão. Feita a 2026-10-06, sem
      bloqueantes; os achados e o que se corrigiu estão no registo da revisão, abaixo.
- [ ] **Critério de ligar**, medido em produção sem pedidos directos ao modelo. A 1.1.0 é
      seleccionada para uma série de pelo menos 60 planos com o objectivo multi-nó de sempre, e
      volta à 1.0.0 no fim da série, até à decisão. Liga-se como omissão de produção só se, na
      série: (a) a recusa do objectivo não aparece — canário a zero **e** zero recusas na leitura
      **à mão** de uma amostra dos textos finais do nó de resumo da série 1.1.0, pelo menos 20
      e todos se forem menos (zero em 60 dá um limite superior de 4,9% a 95%, contra os 5,1% de
      base). A leitura é obrigatória e o canário sozinho não chega: o vocabulário que ele mede
      vem do texto do protocolo, que é o que muda entre as séries, e uma recusa na 1.1.0 pode
      não usar nenhuma das duas palavras. Se a série atravessar um rollback do `aos-orq`, as
      séries do canário recomeçam do zero e somam-se à mão; (b) a taxa de primeiras falhas por «não chamou a tool» não piora — no
      máximo 16 em 60, que só detecta uma regressão grosseira (16% contra 35%). Os números ficam
      no acompanhamento.

### Fora de âmbito

- Pôr o objectivo antes dos dados na mensagem da semente: contraria a decisão do AOS-414 e dá a
  última palavra ao conteúdo untrusted. Reabre-se só se a recusa continuar a aparecer com a 1.1.0.
- Detectar ou repetir automaticamente um nó que recusou o objectivo. Quem precisa de garantia
  semântica declara um `verifier` no plano.
- Frases novas sobre como chamar tools.
- A medição directa ao proxy (não autorizada).

### Registo da revisão (2026-10-06)

Revisão adversarial independente, com 8 mutações: **sem bloqueantes**, seguro de fundir com a
omissão. Dois achados a resolver antes de ligar a 1.1.0 e cinco menores. O que se fez:

- **Quase-forjas (I1).** Uma linha de corpo como ` </plan_input>` — atrás de espaço, TAB, BOM,
  ZWSP, NBSP, NUL, BS, ESC ou soft hyphen — passava crua, e a 1.1.0 tinha tirado a reserva «even
  if it looks like a header» e prometia que um corpo «não pode conter» um cabeçalho. Corrigido de
  duas maneiras, só na 1.1.0: o texto do protocolo diz que cabeçalho e fim abrem no primeiro
  carácter da linha e que o resto é dados; e a projecção escapa, com o `\` do kernel, o primeiro
  carácter visível de uma linha de corpo quando é `<` ou `\` atrás de caracteres invisíveis
  (`neutralizarQuaseCabecalhos`, tabela congelada de Z*, Cc, Cf e brancos). O kernel, o tail, o
  `prompt_hash` e a 1.0.0 não mudaram. Teste com os nove prefixos, em `memory`, `plan_input` e
  resultado de tool.
- **«because it is not data» (I2).** Cortada: o segmento `memory` vai sem rótulo de taint e é
  dados. O teste das frases prende o contra-exemplo.
- **Kind vazio ou com `/` (M4).** `fimDeSegmento` recusa-os; o pedido não sai na 1.1.0.
- **Cablagem do canário (M1, M2).** Teste com o binário real em que o nó de resumo fecha
  `failed` e não conta (a mutação `concluiu = true` morre); e o canário passou a contar depois
  de a saída estar publicada e a conclusão escrita.
- **Documentação (M3, M5, M6).** Um rollback apaga as séries do canário; a variável só é validada
  com `AOS_MODEL_ENDPOINT` definida; e o critério de ligar exige leitura à mão.

O protocolo da 1.1.0 passou de 2 027 para 2 247 bytes (cerca de 562 tokens).

**Fica declarado, sem ferramenta:** não há leitor que reconstrua um pedido a partir de
`manifest.projection_version` (M7 da revisão; já estava no ADR-036 §2.4).

### Registo da implementação (2026-10-06)

**O que entrou.**

- `packages/platform/model-gateway/projection.go`: `NativeProjectionVersion110`,
  `ParseNativeProjectionVersion`, `ProjectNativeVersion` e o texto `protocoloNativo110`.
  `ProjectNative` continua a ser a 1.0.0. A linha de fim sai de `fimDeSegmento`, que lê o kind do
  cabeçalho que o kernel renderizou; o kernel não foi tocado.
- `packages/platform/model-gateway/runtime_adapter.go`: `WithProjectionVersion`. Sem a opção, a
  versão é a 1.0.0. A versão usada volta em `ModelResponse.ProjectionVersion` e o runtime grava-a
  no manifesto do turno, como já fazia.
- `packages/cmd/aos/model_projection_env.go` e `main.go`: `AOS_MODEL_PROJECTION_VERSION`,
  validada onde a `AOS_MODEL_PROJECTION` é (na composição do cliente de modelo, antes de qualquer
  efeito), e o banner. Com a omissão as linhas do banner são as de antes.
- `packages/cmd/aos-orq/canario_de_recusa.go`: o canário, chamado no fecho do nó depois de o
  desfecho estar decidido. Duas séries sem rótulos: `aos_orq_consume_canario_de_recusa_total` e o
  denominador `aos_orq_consume_canario_de_recusa_nos_total`.
- Emenda ao ADR-036 (§2.4 a §2.6, consequências e
  resíduos), `deploy/node/README.md`, `deploy/server/README.md`, `.env.example`,
  `docker-compose.prod.yml` (a variável passa ao serviço do nó) e `tecnica/06`.

**Como se prova «a omissão são os bytes de hoje».** Contra os goldens que já existiam, escritos à
mão no AOS-490 e não alterados: o pedido de exemplo do adaptador (`aos490PedidoDeExemplo`) e os
dois pedidos do run de referência do nó composto (`aos490Pedido1`, `aos490Pedido2`), que são o
corpo que o provider recebe. Com a variável ausente, vazia e em `1.0.0`, os pedidos são esses
bytes; os manifestos de turno são iguais byte a byte entre os três casos; os eventos do run são
os mesmos, pela mesma ordem. As suites existentes do gateway, do kernel, do nó e do `aos-orq`
ficam verdes sem alteração de nenhum teste nem de nenhum golden de replay. **Limite:** não foi
feita a comparação de dois binários (base e novo) sobre o mesmo guião, com eventos e métricas
inteiros, como no AOS-501; e o ficheiro de métricas do `aos-orq` ganha as duas séries do canário
em qualquer plano com um nó de resumo que conclua, qualquer que seja a versão da projecção.

**O canário sobre as recusas medidas** (cópias locais dos textos finais das séries v0.1.46 e
v0.1.49, lidos do `GET /runs/{id}`; contagens por ficheiro, que se podem sobrepor):

| Cópia | Nós de resumo concluídos | Marcados pelo canário |
|---|---|---|
| série de 2026-10-05, primeira parte | 9 | 0 |
| série de 2026-10-05 (`v0146s`) | 21 | 1 — `plan-e2e-v0146s-1791193787~n2` |
| série de 2026-10-06 (`v0149s`) | 18 | 1 — `plan-e2e-v0149s-1791277019~n2` |

Os dois marcados são as duas recusas medidas, e nenhum outro nó de resumo foi marcado. É um
limite inferior: uma recusa que não use `plan_input` nem `taint=untrusted` escapa-lhe. Fora da
classe, o texto final de um nó de leitura (`plan-e2e-v0149s-1791276573~read_notes`, com tools)
também usa o vocabulário; o canário não o conta, por não ser um nó sem tools com `consumes`.

**O corpus adversarial.** O módulo `packages/security-tests` não depende do Model Gateway, e o
gate `security` não projecta nada. O corpus dele (`testdata/corpus.json`, as injecções) corre
pelas duas versões da projecção num teste do gateway
(`TestAOS504_CorpusAdversarial_NasDuasVersoes`), que entra no gate `test` e não no `security`.

**Por fazer.** A revisão adversarial independente; a série de medição em produção e a decisão de
ligar; e o que o ADR-036 já declarava — o replay não lê
nem recusa uma `projection_version`.

### Estado

**EM PRODUÇÃO, LIGADO (v0.1.50, 2026-10-07).** `AOS_MODEL_PROJECTION_VERSION=1.1.0` em produção,
por decisão do dono, depois da série `v0150p`: 60 planos, 58 com código 0 e 2 com código 13; zero
recusas do próprio objectivo em 58 resumos lidos à mão (contra 3 em 39 na série `v0150r`, com a
1.0.0). **A outra metade do critério de ligar não foi cumprida:** a primeira tentativa acabou sem
tool call em 19 de 60 planos (32%), contra 4 de 40 (10%) com a 1.0.0 — acima do máximo de 16 em
60. O dono ligou-a na mesma, por a recuperação estar ligada (17 dos 19 recuperaram), e abriu o
AOS-506 para a causa. O canário marcou 4 nós na série `v0150r` e a leitura à mão deu 3 recusas:
um falso positivo em 4. O código continua com a 1.0.0 como omissão.

**Substituída em produção pela 1.2.0 (v0.1.51, 2026-10-07).** A 1.1.0 correu mais uma série com
o aviso do AOS-506 ligado (`v0151a`, 58 planos): zero recusas do próprio objectivo em 56 resumos
lidos à mão, e 19 primeiras tentativas sem tool call (33%) — a metade (b) do critério de ligar
continua por cumprir com a 1.1.0, e a caixa fica por marcar. A produção passou depois à 1.2.0,
que mantém o que a 1.1.0 corrigiu: na série `v0151b`, zero recusas em 61 resumos e 1 primeira
tentativa sem tool call em 62 (AOS-506).

---

## AOS-505 — Rota sob governação: o proxy deixa de descartar parâmetros e o modelo que serviu cada turno é comparado com o esperado

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A1 (recuperação) |
| Tipo | feat |
| Prioridade | P1: hoje o AOS não sabe que modelo serviu um turno, e uma troca de modelo no proxy não deixa rasto nenhum |
| Estimativa | M |
| Dependências | AOS-490, AOS-491; decisão do dono para cada mudança de configuração de produção |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | ADR-036 §2.8 (emenda deste ticket), `docs/reports/desenho-a1-recuperacao-2026-10-06.md` §6, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A1), `deploy/server/litellm/config.yaml`, `deploy/server/README.md`, `packages/platform/model-gateway/runtime_adapter.go`, `packages/platform/model-gateway/port/port.go` |

### Contexto

Medido em produção de 2026-10-04 a 2026-10-06 (v0.1.45 a v0.1.49): em **12 de 74 planos (16%)** o
nó de leitura terminou sem chamar a tool — 2 em 10, 1 em 22, 6 em 21 e 3 em 21, por série. O
pedido não determina o desfecho: o mesmo `prompt_hash` do turno 1 dá os dois (um pedido
observado sete vezes teve 5 chamadas e 2 falhas). Na mesma janela o nó de resumo, sem tools,
recusou o próprio objectivo em 1 de 21 e em 1 de 18 planos (`plan-e2e-v0146s-1791193787`,
`plan-e2e-v0149s-1791277019`).

Estas taxas são de **um** modelo, e o AOS não consegue dizer qual. O nó governa que nome se pode
pedir (a allowlist assinada); o que o nome significa decide-se no `config.yaml` do proxy, fora de
qualquer assinatura (`deploy/server/litellm/config.yaml`). Medido nos dados locais: o
`served_model_id` gravado no manifesto é `gpt-4o-mini` nos 195 turnos analisados — o proxy
devolve o alias pedido, e não o modelo real. Trocar o modelo por baixo do alias não muda nenhum
evento, nenhuma métrica e nenhum hash.

O proxy está configurado com `drop_params: true` (`deploy/server/litellm/config.yaml:50`): um
parâmetro que o provider não suporte é descartado em silêncio. Hoje o adaptador não envia nenhum
parâmetro opcional; no dia em que enviar (`tool_choice`, amostragem, `max_tokens`), o envio pode
não ter efeito nenhum sem o AOS saber.

### Decidido pelo dono (2026-10-06)

1. A rota sob governação faz parte da fase A1.
2. A medição directa ao modelo **não está autorizada**; os pedidos de verificação deste ticket são
   planos reais pela fila, como nas fases anteriores.
3. Este ticket muda configuração de produção. Cada mudança liga-se por decisão do dono, no
   momento de a ligar.

### Objectivo

O proxy deixa de descartar parâmetros em silêncio; o nome que o nó pede ao proxy passa a ser o do
modelo real; e o modelo que serviu cada turno é comparado com o esperado e fica no registo. Uma
troca de modelo por baixo é detectada e visível.

### Medição de 2026-10-06 — o que o proxy expõe sobre o modelo real

**Como foi medido, e porque não foi um plano pela fila.** O primeiro critério pedia um plano real
pela fila. O nó não guarda cabeçalhos de resposta em lado nenhum, pelo que um plano não os mostra.
Por decisão do dono de 2026-10-06, mediu-se **localmente**, com a imagem de produção do proxy
pelo digest — `ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63`,
`litellm` 1.96.2 — à frente de **providers falsos**. Nenhum contacto com produção nem com o
provider real.

| O que se leu | O que é | Muda com a troca de configuração? |
|---|---|---|
| `model` do corpo | **Sempre o nome pedido** — o proxy carimba-o, com e sem stream. O que o provider devolveu é descartado | Não. **Não serve** |
| `x-litellm-model-name` | O `litellm_params.model` do deployment (ex.: `openai/kimi-for-coding`) | Sim, com a troca de modelo |
| `x-litellm-model-api-base` | O `api_base` do deployment | Sim, com a troca de endpoint |
| `x-litellm-model-id` | SHA-256 **sem sal** de todos os `litellm_params`, **incluindo a `api_key`** | Sim, com qualquer mudança — e com uma rotação de chave. **É derivado de segredo: o nó não o lê, não o grava, não o regista e não o põe em métricas** |
| `x-litellm-model-group` | O nome pedido | Não |

`drop_params` a `true` e a `false` não deu diferença para um `openai/<nome>` com `api_base`
próprio: o proxy reencaminha tudo (`tool_choice`, `top_k`, uma chave inventada), e quem aceita ou
recusa é o provider. Em produção o nome `gpt-4o-mini` vai para `openai/kimi-for-coding` e `gpt-4o`
para `openai/k3`, no mesmo `api_base`.

**Conclusão do critério:** há sinal que distingue o modelo configurado do nome pedido, nos
cabeçalhos; a comparação por turno prova a detecção de uma **troca de configuração no proxy**.

**Por confirmar** (não medido): o que o provider real devolve sobre si próprio, e se o proxy de
produção — que corre a etiqueta `main-stable`, não o digest medido — emite os mesmos cabeçalhos. O
segundo lê-se na verificação em `observe`: `nao_reportado` a zero.

### Critérios de Aceitação

- [x] **Antes de qualquer código:** registar o que o proxy de produção expõe sobre o modelo real
      — o campo `model` da resposta e os cabeçalhos de resposta —, com o proxy na versão que está
      em produção. **Feito por medição local** com a imagem de produção e providers falsos, por
      decisão do dono, em vez de um plano pela fila (secção acima). O proxy expõe o que distingue
      o modelo configurado; o que o provider real devolve fica por confirmar.
- [ ] `drop_params: false`, com o comentário a dizer porquê; um plano de verificação **antes** e
      outro **depois**; rollback de uma linha no runbook. **No repositório:** a semente
      `deploy/server/litellm/config.yaml` passa a `false`, com o porquê e o que foi medido, e o
      runbook tem os passos e o rollback. **Por fazer, por decisão do dono:** a mudança no
      `config.yaml` do servidor (o deploy nunca o reescreve) e os dois planos.
- [x] Teste que prende o que o adaptador envia: o corpo do pedido ao provider tem exactamente os
      campos de hoje (`model`, `messages` e, com tools, `tools`), e nenhum parâmetro opcional —
      nos três modos do interruptor (`TestAOS505_CorpoDoPedido_SoOsCamposDeHoje`; no nó composto,
      os pedidos são os goldens do AOS-490 em `off` e em `observe`).
- [ ] O nome que o nó pede ao proxy passa a ser o do modelo real. **No repositório:** os perfis
      dos nomes reais (`kimi-for-coding`, `k3`) estão no binário, e o nó composto corre com eles
      e com nomes com `/` e `.` por um bundle de allowlist externo assinado com uma chave de teste
      — pedido, `model_id`, selo de governação, `stream_id` de admissão
      (`TestAOS505_NomeRealDoModelo_EmTudoOQueCompoeNomes`, `TestAOS505_No_NomeRealDoModelo`). Um
      nome com `.` numa escada de tiers recusa o arranque nomeando o modelo (AOS-425): é o limite
      conhecido, e fica no runbook. **Por fazer, por decisão do dono:** a allowlist assinada (a
      embebida só se re-assina com a chave custodiada, que este trabalho não tem nem contornou) e
      o `model_name` do proxy. A fonte de preço e a escada de tiers não têm o nome: produção não
      monta tabela de preços nem declara escada.
- [ ] Ordem de entrada sem janela de recusa, e rollback pela inversa. **No repositório:** o
      runbook (`deploy/server/README.md`, «Rota do modelo sob governação», passo 3). O replay
      compara cada turno com o `model_id` que esse turno gravou, pelo que o que foi gravado com o
      nome antigo se reproduz. **Por fazer:** executar, e o teste de replay de um run de produção
      gravado antes da troca.
- [x] Um perfil mínimo da rota, em código: nome pedido, modelo esperado, classe de wire e
      capacidades declaradas (`route.go`). O digest fica em `manifest.model.route_profile_digest`
      de cada turno comparado — campo aditivo e `omitempty`. O perfil não contém segredos nem
      endereços: o host esperado do endpoint é configuração do nó.
- [x] O `served_model_id` do manifesto passa a guardar o modelo que o proxy declarou
      (`x-litellm-model-name`), **num turno comparado**. Quando o proxy não o envia, o campo fica
      ausente e o turno conta como `nao_reportado` — nunca se preenche com o nome pedido nem com
      o `model` do corpo. Com o interruptor em `off` o campo é o de antes.
- [x] O gateway compara, em cada turno, o modelo servido com o esperado do perfil.
      `AOS_MODEL_ROUTE_GOVERNANCE`: `off` (omissão), `observe` (o resultado no `turn.recorded`,
      um selo de variância no audit de governação do gateway e o contador, sem mudar o turno) e
      `enforce` (o turno falha com causa em vocabulário fechado). Um valor inválido recusa o
      arranque; com a governação ligada, um modelo sem perfil também; e `enforce` sem
      `AOS_MODEL_ROUTE_API_HOST` também. Compara-se o valor cru de cada cabeçalho, lidas todas as
      ocorrências.
- [x] Com o interruptor em `off`, o nó é byte a byte o anterior, provado contra goldens medidos na
      base do ticket (`TestAOS505_No_Off_SaoOsBytesDaBase`): corpos dos pedidos, `turn.recorded`,
      tipos e ordem dos eventos, parte em claro das capturas, famílias do `/metrics`. **Sem
      diferença declarada:** o campo do perfil fica ausente em `off`.
- [x] Métricas no `/metrics` do nó: `aos_model_route_checks_total{result,served}`, com `result`
      em `igual`, `diferente`, `nao_reportado`. O modelo servido só entra no rótulo se for um dos
      modelos esperados dos perfis; outro texto conta como `outro`. A regra de alerta sobre
      `diferente` maior do que zero é o `deploy/server/alerta-rota.sh`, que avisa também, com
      outra mensagem, com `nao_reportado` maior do que zero.
- [x] **Uma troca de modelo por baixo é detectada**, com o proxy real: a imagem de produção à
      frente de dois providers falsos, e a configuração trocada por baixo do nome a meio de um run
      — o modelo, e depois o endpoint. O turno seguinte regista a variância em `observe` e falha
      com causa em `enforce` (`make ci-rota-live`; evidência no registo abaixo). O replay devolve
      o mesmo modelo servido e a mesma variância: provado no nó composto, com os mesmos
      cabeçalhos em httptest (`TestAOS505_No_Observe_TrocaPorBaixoAMeioDoRun`).
- [x] A captura de um turno comparado guarda o modelo servido e o replay devolve-o igual; uma
      captura anterior a este ticket descodifica com a rota vazia e reproduz-se sem divergência
      de `prompt_hash` nem de trajectória (o gate `replay` continua verde).
- [x] O `deploy/server/README.md` e o cabeçalho do `config.yaml` deixam de dizer que o roteamento
      é livre por baixo do nome: descrevem o que fica governado e o que não fica.
- [x] Revisão adversarial independente com mutações, antes da fusão. **Feita em 2026-10-07, sem
      achados bloqueantes** («Registo da revisão adversarial», abaixo): com `off` nada muda face
      à base, medido de forma independente; 7 mutações novas, 5 mortas e 2 vivas, ambas com teste
      dirigido agora; os achados a corrigir antes de `observe` e de `enforce` foram corrigidos, e
      o que ficou de fora está escrito como limite.
- [x] Verificação em produção, em `observe`: numa série de pelo menos 20 planos, todos os turnos
      têm o modelo servido reportado e igual ao esperado (`diferente` e `nao_reportado` a zero),
      e a taxa de planos falhados não sobe em relação à série anterior. A passagem a `enforce` é
      decisão do dono, com estes números. **Cumprido a 2026-10-07 na série `v0151g`:** 20
      planos, 60 turnos `igual`, `diferente` e `nao_reportado` a zero; 0 planos falhados em 20
      (1 em 62 na série anterior).

### Fora de âmbito

- Enviar `tool_choice`, parâmetros de amostragem ou `max_tokens`: este ticket só torna visível um
  descarte; a capacidade declarada por rota vem depois.
- O perfil do modelo como artefacto assinado do registo e a entrada automática de modelos (fase
  A3).
- Assinar o `config.yaml` do proxy.
- Mais de um modelo por nó.

### O que isto detecta, e o que não detecta

- **Detecta** uma troca de **configuração no proxy**: outro modelo por baixo do mesmo nome
  pedido, ou outro endpoint (este só com `AOS_MODEL_ROUTE_API_HOST` definida — obrigatória em
  `enforce`, opcional em `observe`).
- **Não detecta** uma troca feita pelo **provider** por trás do mesmo nome e do mesmo endpoint:
  os cabeçalhos dizem o que o proxy está configurado para pedir, não o que o provider serviu.
- **Os cabeçalhos não são atestação.** São emitidos pelo proxy sem prova de origem, e valem
  enquanto o canal entre o nó e o proxy for de confiança.
- **O streaming e os embeddings** não são comparados.
- **As chamadas ao modelo feitas pelo `aos-orq` (o planeador) não são comparadas.** O `aos-orq`
  compõe o seu próprio gateway (`packages/cmd/aos-orq/model_gateway_wiring.go`) sem governação
  da rota, e no compose de produção as duas variáveis só vão para o nó. A decomposição de um
  plano (AOS-391/395) passa pelo mesmo proxy sem ser comparada, em qualquer modo: `enforce` no nó
  não impede que um plano seja decomposto por outro modelo. **Resíduo nomeado deste ticket:**
  ligar a governação da rota no gateway do `aos-orq`. Não foi implementado aqui.

### Passos de produção — por decisão do dono

Runbook em `deploy/server/README.md`, «Rota do modelo sob governação». Nenhum é feito pelo deploy.

1. `drop_params: false` no `config.yaml` do servidor, com um plano de verificação antes e outro
   depois. Rollback de uma linha.
2. `AOS_MODEL_ROUTE_GOVERNANCE=observe` e `AOS_MODEL_ROUTE_API_HOST` (com a porta, se o
   `api_base` do proxy a tiver), o cron do `alerta-rota.sh`, e a série de pelo menos 20 planos.
3. A troca do nome pedido pelo nome real: o proxy serve os dois nomes, a allowlist é re-assinada
   com os dois, o nó passa a pedir o novo, e só então o antigo sai. **Exige a chave custodiada da
   allowlist** (ou um bundle externo assinado pelo operador).
4. A passagem a `enforce`, que exige `AOS_MODEL_ROUTE_API_HOST` definida.

### Registo da implementação (2026-10-07)

**O que entrou.**

- Contrato da porta do gateway `1.4.0`: `port.ChatResponse.Route` (`port.ServedRoute`), fora do
  wire. O adaptador HTTP lê os dois cabeçalhos na chamada síncrona de chat.
- `route.go` do gateway: o perfil da rota e o seu digest, a comparação, o interruptor, o erro de
  imposição, o rótulo fechado do modelo servido. `allowlist.Recorder.SealRouteVariance` sela a
  variância no audit de governação, com o run e o passo.
- Kernel: `ModelResponse.RouteCheck` e `RouteProfileDigest`; `route_check` e
  `manifest.model.route_profile_digest` no `turn.recorded`; `served_model`, `route_check` e
  `route_profile_digest` na captura de um turno comparado. O kernel fecha o vocabulário e a forma
  do digest à entrada.
- Nó: `AOS_MODEL_ROUTE_GOVERNANCE`, `AOS_MODEL_ROUTE_API_HOST`, a linha do banner e
  `aos_model_route_checks_total`.
- `deploy/server/alerta-rota.sh`, `scripts/ci/rota-live.sh` (`make ci-rota-live`), a semente do
  LiteLLM e o runbook.

**Onde a comparação acontece.** No `Gateway.Chat`, depois de o custo da chamada estar contado, e
sobre o nome que o gateway de facto pediu ao proxy (o resolvido pelo roteamento). O host do
endpoint é retirado da resposta antes de qualquer outro passo e não sai do gateway.

**O «evento de variância».** Em `observe` são dois registos duráveis: o `route_check: diferente`
do `turn.recorded`, no stream do run, e um selo no audit de governação do gateway (partição
`modelgw-gov:<board>`), com o run, o passo, o modelo esperado e o servido. Em `enforce` o turno
falha e não há `turn.recorded` desse turno: fica o selo, com decisão `deny`. Não se criou um tipo
novo de evento no stream do run.

**Como se prova «`off` são os bytes de hoje».** O run de referência do AOS-490 foi corrido na
base do ticket (`ce189122`), antes de qualquer alteração, com o provider de ensaio a emitir os
cabeçalhos do proxy; o que ele gravou está em `aos505_goldens_da_base_test.go`. O teste repete o
run com a variável ausente, vazia e em `off`, com o host esperado definido e o proxy a declarar
outro modelo, e compara: os dois pedidos (contra os goldens do AOS-490), os dois `turn.recorded`
byte a byte, os 18 tipos de evento pela ordem, a parte em claro das duas capturas e as chaves do
seu payload, e as 37 famílias do `/metrics`. No gateway, o audit tem os mesmos registos que sem
cabeçalhos, e nem o observador nem o sink de variância são chamados.

**A troca por baixo, com o proxy real (2026-10-07).** `bash scripts/ci/rota-live.sh`, com a
imagem de produção pelo digest: três arranques do proxy; partida `openai/k3` no provider A
(`igual`); (i) `openai/outro-nome` no mesmo endpoint — `diferente`, causa `modelo_diferente`;
(ii) `openai/k3` no provider B — `diferente`, causa `endpoint_diferente`. Em `observe` o turno
seguiu; em `enforce` falhou com a causa. Um selo por variância, do turno certo; e nem o
identificador do deployment de cada configuração, nem o endereço dos providers, nem as chaves
aparecem nos selos, nas observações ou nos eventos de variância.

**Uma rota sem perfil** conta como `diferente` (causa `rota_sem_perfil`) e falha em `enforce`:
está fora do que é governado. O arranque recusa-a para o modelo do nó; só é alcançável com uma
escada de tiers, que o nó de referência não declara.

**Mutações à mão (2026-10-07), cada uma contra o teste dirigido: 17 em 17 mortas.** Entre elas:
`off` não limpa a rota lida dos cabeçalhos; o turno usa o modelo dos cabeçalhos sem comparação;
`nao_reportado` preenchido com o `model` do corpo; `enforce` não falha; o identificador do
deployment lido quando o nome falta; o `api_base` guardado inteiro; o host a sair do gateway; um
nome fora do perfil no rótulo; um modelo por reportar a passar por `igual`; o endpoint não
comparado; um modo ilegível a observar em vez de impor; a variância não selada; a captura a
guardar o modelo de um turno não comparado; o recorder sem fechar o vocabulário; e, no nó, `off`
a publicar a família da rota, `off` tratado como `observe`, e um valor inválido a cair para
`off`. Não substitui a revisão adversarial independente.

**Por fazer.** Os passos de produção, por decisão do dono; e confirmar contra o provider real o
que ele devolve sobre si próprio.

### Registo da revisão adversarial (2026-10-07)

Revisão independente sobre `2b8bb0b3`, em árvores descartáveis: **sem achados bloqueantes**; com
`off`, nenhuma diferença face à base do ticket (o mesmo run nas duas árvores, 191 linhas de
pedidos, eventos e `/metrics`, a diferir só em relógios, latências e envelopes selados).

**Corrigido depois da revisão.**

- **A comparação era feita sobre o nome já saneado.** O proxy a declarar
  `openai/k<largura zero>3<inversão de direcção>` dava `igual`. Compara-se agora o valor cru: se
  o saneamento alterar alguma coisa (caracteres não imprimíveis, de largura zero, de direcção do
  texto, ou o corte a 256 bytes), o resultado é `diferente`, causa `modelo_diferente`. O que se
  grava continua saneado. Medido em `observe` e em `enforce`, pelo adaptador HTTP real
  (`TestAOS505_ValorCruDoCabecalho_OSaneamentoNaoFazIgual`). Um host com caracteres invisíveis
  nunca chega a ser um host: fica por reportar.
- **Cabeçalho repetido: ganhava a primeira ocorrência.** Lêem-se todas; valores diferentes entre
  si dão `diferente`, iguais seguem (`TestAOS505_Enforce_CabecalhoRepetido`).
- **`enforce` sem `AOS_MODEL_ROUTE_API_HOST` recusa o arranque**
  (`ErrModelRouteEnforceWithoutHost`, a mensagem nomeia a variável). `observe` sem ela continua
  permitido, e o banner diz que o endpoint não é comparado
  (`TestAOS505_Env_EnforceExigeOHostDoEndpoint`).
- **O host compara-se normalizado dos dois lados**: minúsculas e sem ponto final. A porta
  compara-se como está — se o proxy declara a porta, a variável leva a porta
  (`TestAOS505_Enforce_HostNormalizadoDosDoisLados`).
- **O alerta era cego a `nao_reportado`.** O `alerta-rota.sh` avisa agora também com
  `nao_reportado` maior do que zero, com título e mensagem próprios e estado separado do de
  `diferente`. Ensaiado contra um `/metrics` e um ntfy locais, 13 passos: primeiro aviso, sem
  repetição, as duas regras ao mesmo tempo, lembrete às 24 h só da regra em causa, recuperação de
  cada uma, e um aviso que não sai a não mudar o estado.
- **Com `off`, um `AOS_MODEL_ROUTE_API_HOST` inválido abortava o arranque.** Deixa de ser lida
  nesse modo; fica um aviso no banner, sem repetir o valor
  (`TestAOS505_Env_OffIgnoraHostInvalidoComAviso`).
- **Duas mutações da revisão tinham sobrevivido**, e têm agora teste dirigido, cada uma aplicada
  à mão contra ele e revertida: o perfil escolhido pelo nome pedido em vez do resolvido
  (`TestAOS505_GovernRoute_PerfilPeloNomeResolvido`), e o atributo de span da falha do selo
  removido (`TestAOS505_GovernRoute_FalhaDoSeloFicaNoSpan`). Mais duas à mão, mortas: a marca de
  nome inexacto ignorada na comparação, e a de endpoint repetido ignorada.

**Declarado, e não corrigido.**

- **As chamadas do `aos-orq` ao modelo não são comparadas** (secção «O que isto detecta, e o que
  não detecta»). Resíduo nomeado.
- **Em `observe`, se o selo da variância falhar, o turno segue** só com o atributo de span
  `aos.route.seal_failed`: fica o `route_check` no `turn.recorded`, mas não o selo. Preso por
  teste; em `enforce` o turno falha com as duas causas.
- **Num run recusado em `enforce`, a causa não fica no stream do run**: o terminal é
  `run_failed`, como em qualquer falha de modelo antes deste ticket; a causa fica no selo `deny`
  do audit do gateway e no erro do `GET /runs`.
- **O contador é por processo:** um reinício do nó zera-o, e o alerta dá a regra por recuperada
  até ao turno seguinte.

**Por verificar** (dito pela revisão, e não fechado aqui): o replay cruzado `observe`/`off` e o de
uma captura real gravada pela base, corridos como teste separado; o que o `aos-orq` faz a um nó
cujo run falhou por rota em `enforce` (se re-planeia, e quantas chamadas pagas gera); e se a
tabela de perfis corresponde ao `config.yaml` real do servidor.

### Estado

**EM PRODUÇÃO, DESLIGADO (v0.1.50, 2026-10-07).** O código está em produção com
`AOS_MODEL_ROUTE_GOVERNANCE` ausente (`off`): nada é comparado. Implementado e revisto a 2026-10-07, sem bloqueantes. Os passos de produção (o
`config.yaml` do servidor, `observe`, a troca do nome pedido e `enforce`) ligam-se por decisão do
dono. Limites: não detecta uma troca feita pelo provider; as chamadas do `aos-orq` ficam de fora;
`enforce` exige o host do endpoint.

**EM PRODUÇÃO, EM `observe` (v0.1.51, 2026-10-07).** As séries `v0151a` e `v0151b` ainda
correram com a rota por ligar. Depois, por decisão do dono, o `.env` de produção passou a ter
`AOS_MODEL_ROUTE_GOVERNANCE=observe` e `AOS_MODEL_ROUTE_API_HOST=api.kimi.com` (cópia anterior em
`/opt/aos/.env.antes-rota-observe-20261007`). O banner do nó declara o perfil da rota:
`gpt-4o-mini` pedido, `openai/kimi-for-coding` esperado.

Série `v0151g`: 20 planos, 20 com código 0.
`aos_model_route_checks_total{result="igual",served="openai/kimi-for-coding"}` = 60, e todas as
outras séries da métrica a zero (`diferente` 0, `nao_reportado` 0). Zero novas tentativas:
nenhuma das 20 primeiras tentativas falhou, com a projecção 1.2.0. Tempo médio por plano: 39 s.
O proxy de produção (`litellm:main-stable`, a mesma imagem medida localmente) envia os
cabeçalhos em todos os turnos.

O que a série prova e o que não prova: a comparação corre em produção e não dá falsos alarmes.
Nenhuma troca de modelo ocorreu, pelo que a detecção em si continua provada só com o proxy real
em `make ci-rota-live`. Em `observe` uma diferença seria contada e não recusada.

**Por fazer, por decisão do dono:** `drop_params: false` no `config.yaml` do servidor (a caixa
fica por marcar), o nome real na allowlist assinada, a passagem a `enforce`, e instalar o cron
do `alerta-rota.sh`.

---

## AOS-506 — O modelo escreve a tool call como texto: projecção nativa 1.2.0 e aviso constante na nova tentativa

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A1 (recuperação) |
| Tipo | fix |
| Prioridade | P1: o critério da fase A1 não foi cumprido (2,5% e 3,3% de «não cumprido», contra menos de 2%), e a causa medida é uma só |
| Estimativa | M |
| Dependências | AOS-502, AOS-504 |
| Bloqueia | O critério de prova da fase A1 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A1, medições de 2026-10-07), `docs/adr/ADR-036-o-tail-e-a-forma-canonica-da-conversa.md` §2.4, `docs/adr/ADR-039-a-recuperacao-e-uma-nova-tentativa-do-no-do-plano-autorizada-pelo-no.md` §2.7, `packages/platform/model-gateway/projection.go`, `packages/kernel/agent-runtime/layout.go`, `packages/kernel/agent-runtime/loop.go`, `packages/cmd/aos/nova_tentativa.go`, `packages/cmd/aos/aviso_da_tentativa.go` |

**Porque na EPIC-06 e não na EPIC-19.** O defeito está na fronteira com o modelo: o que o nó diz
ao modelo sobre como pedir uma tool. A parte A é só do Model Gateway. A parte B acrescenta um
segmento ao prompt de uma tentativa; o contrato do `POST /runs`, a prova do nó e o `aos-orq`
(o que a EPIC-19 descreve) não mudam.

### Contexto

Medido em produção a 2026-10-07, na v0.1.50, com o modelo real por trás do alias (Kimi,
`kimi-for-coding`), em três séries com o objectivo multi-nó de sempre:

| Série | Configuração | Planos | Código 0 | Código 13 | 1.ª tentativa sem tool call | Novas tentativas (feitas / voltaram a falhar) | Recuperados | Resumos que recusaram o próprio objectivo (lidos à mão) |
|---|---|---|---|---|---|---|---|---|
| `v0150o` | recuperação em `observe`, projecção 1.0.0 | 10 | 9 | 1 | 1 | — | — | 0 de 9 |
| `v0150r` | recuperação em `on` (tecto 2), projecção 1.0.0 | 40 | 39 | 1 | 4 (10%) | 6 / 3 | 3 | 3 de 39 (os planos saíram 0: verde sem cumprir); o canário marcou 4, um falso positivo |
| `v0150p` | recuperação em `on`, projecção 1.1.0 | 60 | 58 | 2 | 19 (32%) | 25 / 8 | 17 | 0 de 58 |

No nó, na série `v0150r`: 6 tentativas admitidas, 0 recusadas, e
`aos_runs_retry_prompt_hash_diferente_total` a zero.

**A causa das falhas é uma só.** Lidas, por `GET /runs/{id}/reconstruct` e com autorização do
dono, as respostas dos 34 runs falhados das duas séries com recuperação: em **33** o texto
inteiro do turno é uma tool call **escrita como texto**, com a tool e o argumento certos, em mais
de dez notações inventadas pelo modelo — todas marcação com sinais de menor e de maior, com nomes
como `functions.<tool>`, `tool_call`, `invoke` e `tool_use`. Uma foi outra coisa (`<correct>`). O
motivo de paragem é `stop`, e a resposta não tem nenhuma tool call nativa.

**Hipótese, não testada isoladamente.** O texto do protocolo mostra ao modelo uma notação de
cabeçalhos `<kind ...>` e, na 1.1.0, de linhas de fim `</kind>`, e fala-lhe de tool calls; o
modelo imita a notação quando quer pedir uma tool. A 1.1.0 triplicou a taxa de primeiras falhas
(de 10% para 32%) e acabou com as recusas do próprio objectivo (de 3 em 39 para 0 em 58).

**O critério da fase A1 não foi cumprido:** «não cumprido» em 2,5% dos planos com a 1.0.0 e em
3,3% com a 1.1.0, contra menos de 2%. A promessa da troca de modelo fica na forma estreita:
detecta-se uma troca de configuração no proxy.

**Estado de produção a 2026-10-07:** v0.1.50, com `AOS_COMPLETION_VERDICT=enforce`,
`AOS_ORQ_SAIDA_POR_REFERENCIA=on`, `AOS_ORQ_NOVA_TENTATIVA=on`, `AOS_RUN_RETRY_MAX=2` e
`AOS_MODEL_PROJECTION_VERSION=1.1.0` (decisão do dono: fica ligada); `AOS_MODEL_ROUTE_GOVERNANCE`
por ligar.

### Decidido pelo dono (2026-10-07)

1. Abre-se este ticket com duas partes, **as duas desligadas por omissão e medidas antes de
   ligar**: (A) uma projecção nativa **1.2.0** que diz, de forma explícita, que uma tool só se
   pede pelo mecanismo nativo de function calling e nunca escrita como texto ou marcação,
   mantendo tudo o que a 1.1.0 corrigiu; (B) um **aviso na nova tentativa**: quando a tentativa
   anterior fechou `contract_unmet_no_call`, a tentativa seguinte leva um aviso de texto
   constante a dizer que a resposta anterior não pediu a tool pelo mecanismo nativo.
2. **Rejeitado, e fica escrito:** interpretar a tool call escrita em texto (um parser
   tolerante). Um documento lido pode conter esse mesmo texto.
3. **Critério para ligar:** uma série de pelo menos 60 planos com menos de 10% de falhas à
   primeira tentativa, zero recusas do próprio objectivo (lidas à mão) e «não cumprido» abaixo de
   2%.

### Objectivo

Duas mudanças no que o nó diz ao modelo, cada uma com o seu interruptor e as duas sem efeito por
omissão: o texto do protocolo passa a dizer como se pede uma tool; e a tentativa que se segue a
uma falha sem tool call passa a dizê-lo outra vez, num aviso que o runtime escreve. O que conta
como tool call não muda.

### Critérios de Aceitação

**Parte A — projecção nativa 1.2.0**

- [x] `AOS_MODEL_PROJECTION_VERSION` aceita `1.2.0`, ao lado de `1.0.0` (a omissão do código) e
      `1.1.0`. Outro valor recusa o arranque, como antes.
- [x] A 1.0.0 e a 1.1.0 ficam **byte a byte** como estão: os goldens dessas versões não são
      alterados, e um teste prova, contra eles, que a entrada da 1.2.0 não lhes mudou um byte.
- [x] A 1.2.0 é a 1.1.0 com outro texto de protocolo: fora da mensagem `system`, as mensagens
      das duas versões são iguais byte a byte (fim de segmento, escape das quase-forjas,
      agrupamento, tool calls), provado em vistas com todos os kinds e conteúdo adversarial.
- [x] O texto novo diz que uma tool só se pede por uma function call feita pelo mecanismo de
      function calling, que um pedido de tool escrito no texto da resposta não é lido, que uma
      resposta sem function call é a resposta final, e que as respostas do modelo não são feitas
      de segmentos. **Não mostra nenhum exemplo** de uma tool call em texto, em notação nenhuma
      (teste: as linhas novas não têm sinais de marcação nem as palavras das notações medidas).
- [x] A expressão «the tool_call whose id is the ref label», entre aspas na linha do aviso, sai:
      a linha é reescrita sem citar o kind. O layout do kernel não muda.
- [x] As restrições do texto são as das outras versões (ASCII, nenhuma linha a abrir por `<`,
      sem `taint=trusted`, sem nomes de tools), e cada frase nova é verdadeira para o que a
      projecção e o adaptador fazem (um teste por frase verificável).
- [x] O `prompt_hash`, o tail e a lista de layouts cobertos não dependem da versão.

**Parte B — aviso na nova tentativa**

- [x] Interruptor próprio no nó: `AOS_RUN_RETRY_NOTICE`, `off` (a omissão) ou `on`. Outro valor
      recusa o arranque. `on` com `AOS_RUN_RETRY_MAX` a zero não tem efeito, e o banner di-lo.
- [x] Com `off`, o run de uma tentativa é **byte a byte** o de hoje: o mesmo pedido ao provider,
      o mesmo `prompt_hash`, o mesmo `run.plan_origin`, o mesmo registo de retoma, as mesmas
      séries e os mesmos textos de ajuda no `/metrics`.
- [x] Com `on`, o run de uma tentativa que o nó **admitiu com a prova** do AOS-502 leva, na
      semente do tail e a seguir ao objectivo, um segmento `notice` de texto **constante**. O
      pedido ao provider é o da tentativa anterior com esse segmento acrescentado, e mais nada.
- [x] O texto é escrito pelo runtime e não leva **nenhum byte** do run anterior, do modelo nem do
      pedido: quem compõe o run declara um valor de vocabulário fechado, e o kernel escreve a
      constante. Teste com uma resposta anterior marcada: nenhum byte dela chega ao pedido
      seguinte.
- [x] Quem o acrescenta é o **nó**, e só numa tentativa que ele próprio admitiu. O `POST /runs`
      não ganha campo nenhum (um corpo com `retry_notice` é recusado), e o `aos-orq` não muda.
      Um run que não é tentativa, um `POST /runs` directo e uma tentativa recusada nunca o levam.
- [x] O segmento é o `notice` que o layout 1.4.0 já tem — um aviso do runtime. O `correction`,
      que é de um humano autenticado, não se usa. **Sem versão nova de layout.** A autoridade do
      contexto (<!-- rtm: menção -->ADR-034<!-- /rtm: menção -->) é a de antes, com e sem
      entradas.
- [x] A medição `aos_runs_retry_prompt_hash_diferente_total` continua a ter de ser zero e a
      detectar qualquer outra diferença: com aviso, compara com o hash **esperado** — o nó
      recalcula o prompt com o aviso da tentativa anterior (tem de dar o hash que ela gravou) e
      com o desta (tem de dar o que esta gravou). Teste com outro objectivo, outras entradas,
      outro system, outras tools e outro layout.
- [x] A prova do nó (zero tool calls, um turno, `stop`, `contract_unmet_no_call`) não muda: uma
      tentativa 3 é admitida sobre uma tentativa 2 com aviso.
- [x] A retoma e o replay de uma tentativa com aviso reproduzem sem divergir: o registo de retoma
      leva o valor, e a semente do replay também.
- [x] Um valor de aviso fora do vocabulário, ou um layout sem `notice`, recusa o run antes de
      qualquer efeito.

**Comuns**

- [x] Métricas em vocabulário fechado; nenhum conteúdo do titular em logs, métricas, eventos ou
      argumentos de linha de comando.
- [x] Emendas ao ADR-036 (§2.4: a 1.2.0) e ao ADR-039 (§2.7: a tentativa repete o pedido *mais*
      um aviso constante do runtime, quando ligado). RTM regenerada.
- [x] Runbook em `deploy/server/README.md`, incluindo o falso positivo do canário do AOS-504 (1
      em 4); `.env.example`; a variável nova passa ao serviço do nó no compose; superfície de
      ambiente documentada.
- [x] Revisão adversarial independente, antes de ligar. Feita a 2026-10-07 sobre `dd8ba1c5`
      contra a base `3fdb6588`: **sem bloqueantes**; com as omissões nada muda face à base
      (medido em três níveis); 2 achados importantes e 6 menores, corrigidos ou registados
      abaixo em «Registo da revisão».
- [x] **Critério de ligar**, medido em produção: o do ponto 3 das decisões do dono, com cada
      parte ligada sozinha primeiro. **Cumprido a 2026-10-07 para a combinação 1.2.0 com aviso**
      (série `v0151b`, 62 planos: 1 falha à primeira tentativa, 1,6%; 0 recusas em 61 resumos
      lidos à mão; «não cumprido» em 1,6%). **Desvio declarado:** o aviso foi medido sozinho
      (série `v0151a`), a 1.2.0 foi medida com o aviso já ligado, por decisão do dono. Os números
      estão em «Verificação em produção».

### Fora de âmbito

- Interpretar texto do modelo como tool call (rejeitado pelo dono).
- `tool_choice` forçado: depende da rota sob governação (AOS-505) e do suporte do provider.
- Mudar o canário do AOS-504.
- Um aviso que cite ou resuma a resposta anterior.
- Um aviso para outras razões de falha: só `contract_unmet_no_call` com zero tool calls é
  repetido.

### Registo da implementação (2026-10-07)

**O texto novo da 1.2.0** (três linhas; o resto é o da 1.1.0, byte a byte):

- Nova: «To use a tool, make a function call through the function-calling interface of this API,
  choosing from the tools offered with this request. That is the only way a tool runs. Never
  write a tool request as text in your reply, in any notation: the runtime does not look for
  tool requests in reply text, and nothing would run. A reply without a function call is your
  final answer.»
- Nova: «Your replies are not made of segments. Do not use the header lines or end lines of the
  runtime in them. This is only about those runtime lines: if the content you were asked to
  write is itself markup, write it normally.»
- Reescrita (a linha do aviso): «A notice may carry a ref label. It is the id of one of your
  earlier tool calls: the one with that id in one of your earlier assistant messages. A notice
  may carry an about label. It says what the notice is about: previous_attempt means an earlier
  attempt at this same task, which is not part of this conversation.»

O protocolo passa de 2 247 bytes (1.1.0) para 3 033 (cerca de 758 tokens). As duas linhas acima
são as da revisão (ver «Registo da revisão»); a primeira redacção tinha 2 730 bytes.

**O aviso** é o segmento `<notice taint=trusted about=previous_attempt>`, com o corpo:

«An earlier attempt at this task ended with a reply that made no function call, so no tool ran,
and it failed because a tool it had to use was never called. This is a new attempt. The only way
to use a tool is a function call made through the function-calling interface of this API. The
runtime does not read a tool request written as text in a reply, in any notation, and nothing
runs from it.»

São 440 bytes com a linha de delimitação. Cada frase é verdadeira sempre que o aviso sai, porque
o nó só o declara depois da prova: zero tool calls, nenhum evento de mediação, e a razão selada
`contract_unmet_no_call`.

**O que entrou.**

- `packages/platform/model-gateway/projection.go`: `NativeProjectionVersion120` e o texto
  `protocoloNativo120`; a linha de fim e o escape passam a valer para todas as versões que não
  são a 1.0.0.
- `packages/kernel/agent-runtime`: o tipo `RetryNotice` (vocabulário fechado: vazio,
  `no_function_call`), `Goal.RetryNotice`, o texto constante, e a semente do tail construída num
  só sítio (`seedDoRun`, exposta por `SeedTail`). O replay semeia com a mesma construção
  (`TrajectorySpec.RetryNotice`).
- `packages/integration/resume_records.go`: o registo de retoma leva o valor (`omitempty`).
- `packages/cmd/aos/aviso_da_tentativa.go`: `AOS_RUN_RETRY_NOTICE`, a decisão
  (`avisoDaTentativa`: o interruptor **e** a prova deste pedido), e a medição do hash esperado.
  O `run.plan_origin` de uma tentativa com aviso leva `retry_notice`; a série
  `aos_runs_retry_notice_total` conta-as.

**Sem versão nova de layout, e porquê.** O layout 1.4.0 já tem o kind `notice`, e o preâmbulo
diz que é instrução sem lhe exigir o rótulo `ref`. O que muda é a **semente** do tail, que é
função do `Goal`: ganha um segmento opcional, como ganhou os payloads do plano no AOS-414. A
forma de um segmento, o preâmbulo e a neutralização são os de antes, e um run sem aviso
materializa os mesmos bytes. Custo aceite: um binário anterior não conhece o campo, e a retoma
de uma tentativa com aviso feita por ele **diverge em silêncio** — semeia o tail sem o aviso,
envia o turno seguinte sem ele, o run pode fechar `complete` e nada alerta (medido na revisão).
O recuo de imagem faz-se por isso com a variável primeiro e sem tentativas com aviso em voo.
**Resíduo nomeado:** não se cria a 1.5.0 agora; se o aviso passar a ligado em permanência (ou a
ser a omissão), a semente-com-aviso é promovida a versão de layout nesse ticket (ADR-036 §2.4).

**Como se prova que a omissão e as versões anteriores não mudaram.** A 1.0.0 contra o pedido
golden do AOS-490 e a 1.1.0 contra o protocolo e as mensagens goldens do AOS-504, não alterados.
O prompt de um run sem aviso contra o que o assembler monta sobre a semente escrita à mão. No nó
composto, com `off`, o corpo do primeiro pedido da tentativa 2 é igual ao da primeira, e o
`run.plan_origin` é a cadeia de bytes de antes. As listas de versões **inválidas** de dois
testes do AOS-504 trocaram `1.2.0` por `1.3.0` — a 1.2.0 passou a ser válida.

**Limites.**

- A eficácia não está medida: nem a do texto novo, nem a do aviso. As duas entram desligadas.
- A hipótese da imitação da notação não foi testada isoladamente; a 1.2.0 mantém os cabeçalhos e
  as linhas de fim, que são o que a 1.1.0 corrigiu.
- O aviso é um segmento trusted que conteúdo untrusted consegue **provocar** (num nó com
  entradas, levando o modelo a não chamar a tool). Não lhe consegue escrever nada, e não dá
  autoridade: o contexto de um run com entradas já é untrusted.
- A medição do hash vive na memória do nó, como antes; um reinício entre a admissão e o fim da
  tentativa perde a comparação desse run.
- A recusa de arrancar com `AOS_RUN_RETRY_NOTICE` inválido está provada na leitura da variável;
  não há teste que levante o nó inteiro com o valor errado.

### Registo da revisão (2026-10-07)

Revisão adversarial independente, em worktrees descartáveis, com sete mutações (todas mortas).
Veredicto: seguro fundir com as omissões; **não ligar o aviso antes do I-1** (corrigido).

- **I-1 (corrigido).** Com o aviso ligado, `aos_runs_retry_prompt_hash_diferente_total` dava
  falso positivo em qualquer nó cujo objectivo a ingestão redige: o handler tirava a semente do
  Goal do pedido, e o serviço minimiza o objectivo depois. A medição passa a recalcular sobre o
  Goal que o serviço **hospeda** (`NodeService.SubmitObservando`). Teste com um objectivo com
  e-mail e telefone: com `on` a série fica a zero numa tentativa que só difere pelo aviso, e sobe
  com uma diferença real. O registo de retoma, sugerido na revisão como fonte, não serve: só é
  composto num nó com four-eyes.
- **M-2 (corrigido com o I-1).** A medição retinha o objectivo em claro e as entradas até ao fim
  do run, também com `off`. Sem aviso em jogo não guarda nada do pedido; com aviso guarda a
  semente hospedada, com o objectivo já redigido.
- **I-2 (corrigido, documental).** O runbook e os ADR diziam que um binário anterior «diverge no
  `prompt_hash` do turno 1». Medido: diverge **em silêncio**. Frases corrigidas, e a ordem do
  recuo passa a incluir esperar pelas tentativas com aviso em voo.
- **M-1 (corrigido).** Duas verificações negativas dos testes do nó procuravam `<notice` no corpo
  JSON cru, onde o `<` vai escapado: passavam com qualquer pedido. Procuram agora no conteúdo
  descodificado das mensagens; confirmado com uma mutação à mão que mordem.
- **M-3 (corrigido).** `gravarOrigemDoRunFilho` tinha ficado sem chamador de produção. Passa a
  ser a única função, com o aviso por parâmetro, e os testes do AOS-477 exercitam a que o
  `POST /runs` chama.
- **M-4 (fica).** `harness/fixtures.go` passou a copiar `Inputs` para a spec de replay. Não é do
  âmbito do ticket, mas é **correcção necessária** ao replay das tentativas: os nós de um plano
  têm entradas, e sem elas na spec o replay de qualquer tentativa divergia no turno 1.
- **M-5 (corrigido).** A linha das respostas da 1.2.0 lia-se como proibição de marcação no
  produto; fala agora só das linhas do runtime. A linha do aviso descreve o rótulo `about`.
  `AOS_RUN_RETRY_NOTICE` apara espaços como as variáveis irmãs — fica, e está documentado.
- **M-6 (corrigido).** O motor de replay tratava um aviso desconhecido como «sem aviso»; recusa
  agora com `ErrUnknownRetryNotice`, pela decisão do kernel.
- **Ponto em aberto da revisão, verificado.** Uma function call nativa a uma tool desconhecida,
  não oferecida ou de nome inválido fecha o run com a razão `contract_unmet_no_call` (a razão é
  por tool do contrato: a tool exigida nunca foi chamada) **mas com `tool_calls_requested` = 1**,
  no veredicto e no turno, e `stop_reason = tool_calls`; a tentativa seguinte é recusada (403) e
  o aviso não sai. A frase «made no function call» é verdadeira sempre que o aviso sai, porque
  quem o condiciona é a prova (zero tool calls, um turno, `stop`), e não a razão. Fica igual.
- **Decisão sobre o layout.** Não se cria a 1.5.0 agora; a condição em que passa a ser devida
  está em «Sem versão nova de layout» e no ADR-036 §2.4.

**Por verificar depois desta revisão:** a via durável sobre JetStream (tudo correu sobre o Event
Store em memória); a re-submissão do mesmo id de tentativa depois de um reinício com o
interruptor trocado; e o efeito dos textos no modelo, que só a série em produção mede.

### Estado

**EM PRODUÇÃO, AS DUAS PARTES LIGADAS (v0.1.51, 2026-10-07).** `AOS_RUN_RETRY_NOTICE=on` e
`AOS_MODEL_PROJECTION_VERSION=1.2.0` no `.env` de produção. Implementado e revisto a 2026-10-07,
sem bloqueantes. No código, as duas continuam desligadas por omissão: `AOS_RUN_RETRY_NOTICE`
ausente é `off`, e a omissão da projecção é a 1.0.0.

### Verificação em produção (2026-10-07, v0.1.51)

Imagem `ghcr.io/albinojimy/aos-node@sha256:a3d06030ad173736eef94dd1943caf38605058b38099f3571d139edcf55b7e29`
(PR #454, release 37636422928). O modelo real por trás do alias é o Kimi (`kimi-for-coding`).
Recuperação ligada nas duas séries (`AOS_ORQ_NOVA_TENTATIVA=on`, `AOS_RUN_RETRY_MAX=2`). As
séries da v0.1.50 (`v0150o`, `v0150r`, `v0150p`) estão no Contexto e não se repetem aqui.

| | `v0151a` | `v0151b` |
|---|---|---|
| Projecção | 1.1.0 | **1.2.0** |
| Aviso na nova tentativa | ligado | ligado |
| Planos | 58 (2 das 60 submissões não saíram do cliente) | 62 |
| Código 0 / código 13 | 56 / 2 (3,4%) | 61 / 1 (**1,6%**) |
| Primeira tentativa sem tool call | 19 de 58 (33%) | **1 de 62 (1,6%)** |
| Novas tentativas / voltaram a falhar | 22 / 3 (14%) | 1 / 0 |
| Nós recuperados | 19 de 19 (16 à segunda, 3 à terceira) | 1 de 1 |
| Planos falhados por tool não chamada | 0 | 0 |
| Recusas do próprio objectivo (resumos lidos à mão) | 0 em 56 | 0 em 61 |
| Causa dos planos falhados | `empty_output` no nó de resumo (2) | `empty_output` no nó de resumo (1) |

**Parte B, o aviso (série `v0151a`).** Sem aviso, na série `v0150p`, 8 de 25 tentativas
voltaram a falhar (32%); com aviso, 3 de 22 (14%). O nó admitiu 22 tentativas, as 22 com aviso,
recusou 0, e `aos_runs_retry_prompt_hash_diferente_total` ficou a zero. A primeira tentativa não
muda, e não era de esperar: o aviso só entra na segunda.

**Parte A, a 1.2.0 (série `v0151b`).** A primeira tentativa sem tool call passa de 33% para
1,6%. Os 61 resumos contêm os três factos do documento (gVisor, 89 pods, Kimi) e nenhum recusa
o objectivo: a 1.2.0 mantém o que a 1.1.0 corrigiu. Tempo médio por plano: 40 s, contra 45 a
47 s nas séries anteriores.

**O critério de ligar** (pelo menos 60 planos, menos de 10% de falhas à primeira tentativa, zero
recusas lidas à mão, «não cumprido» abaixo de 2%) **está cumprido para a combinação 1.2.0 com
aviso.** Desvio declarado: o ticket previa cada parte ligada sozinha. O aviso foi medido
sozinho; a 1.2.0 foi medida com o aviso já ligado, por decisão do dono de 2026-10-07. Como o
aviso só actua na segunda tentativa, a taxa à primeira mede só a 1.2.0; a recorrência e o «não
cumprido» medem a combinação. A 1.2.0 sem aviso não foi medida.

**O critério da fase A1** («não cumprido» abaixo de 2% em pelo menos 40 planos) foi cumprido
pela primeira vez na série `v0151b`, com 1 em 62.

**O que estes números não fecham.**

- As 3 falhas das duas séries são `empty_output` no nó de resumo, que não tem tools:
  `plan-e2e-v0151a-1791385126~n2` e `plan-e2e-v0151a-1791385255~n2` (um turno,
  `stop_reason=stop`, 161 e 77 tokens de saída, texto vazio), e uma na série `v0151b` com o
  mesmo padrão. A recuperação não as cobre. Hipótese não confirmada: o conteúdo veio no campo
  de raciocínio, que o adaptador não lê. É matéria da fase A2.
- Com 1 falha em 62, mais uma na mesma série dava 3,2%: a margem sobre os 2% é de um plano.
- As séries não têm nenhum nó com tools e `consumes`. O plano de três passos correu depois
  (`plan-e2e-v0151t-1791401536`, código 13): o nó com `consumes` pediu a tool por function call
  nativa — a 1.2.0 fez o seu papel — e o Reference Monitor negou-a por taint, com `cap:fs.read`
  armada (<!-- rtm: menção -->ADR-034<!-- /rtm: menção -->, fase 1). Não houve nova tentativa nem aviso: a negação não é o caso que este
  ticket trata. O registo está no AOS-503 e no acompanhamento.
- Acumulado com a 1.2.0, contando a série `v0151g` do AOS-505: 82 planos, 1 falha à primeira
  tentativa (1,2%), 1 `empty_output`.
- Risco de desenho aberto, não provado: os separadores `<kind>` e `</kind>` do protocolo podem
  ser a causa de fundo da tool call escrita como texto. A 1.2.0 corrige com uma instrução e
  mantém os separadores; a hipótese não foi testada isoladamente. O texto foi afinado para um
  só modelo.

Recuo: as cópias do `.env` de cada passo estão em `/opt/aos`
(`.env.antes-aviso-20261007`, `.env.antes-projeccao-120-20261007`).

---

## AOS-507 — A forma da resposta do provider fica registada em cada turno, em vocabulário fechado e sem conteúdo

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: é instrumentação. Acrescenta um campo de medição ao registo do turno; não muda o que o runtime decide. O ADR-036 e o ADR-037 são citados só como contexto. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A2 (estado opaco do provider) |
| Tipo | feat |
| Prioridade | P0: a única causa de «não cumprido» que resta em produção (`empty_output`) não tem causa conhecida, e nada do que hoje se grava a distingue |
| Estimativa | M |
| Dependências | AOS-491 |
| Bloqueia | AOS-509; o critério P1 da fase A2 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §1, §2 e §8, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A2), `packages/platform/model-gateway/port/port.go`, `packages/platform/model-gateway/port/normalize.go`, `packages/platform/model-gateway/runtime_adapter.go`, `packages/platform/model-gateway/internal/adapters/openai_http.go`, `packages/kernel/agent-runtime/model.go`, `packages/kernel/agent-runtime/turn.go`, `packages/kernel/agent-runtime/replay/nondeterminism_capture.go` |

### Contexto

Medido em produção a 2026-10-07, na v0.1.51, com a recuperação ligada: **3 planos em 140 (2,1%)**
saíram com o código 13 por `empty_output` — 2 em 58 na série `v0151a`
(`plan-e2e-v0151a-1791385126~n2` e `plan-e2e-v0151a-1791385255~n2`), 1 em 62 na série `v0151b` e
0 em 20 na série `v0151g`. Os três são o nó de resumo, que não tem tools: um só turno,
`stop_reason=stop`, `output_tokens` contados (161 e 77 nos dois da `v0151a`) e texto vazio. É a
única causa de «não cumprido» que resta nessas séries.

O modelo gastou tokens de saída e a resposta visível veio vazia. Lido no código da base
(`docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §1 e §10):

- O adaptador **lê** `reasoning_content` (`packages/platform/model-gateway/port/port.go:92`,
  `runtime_adapter.go:379`) e o valor vai para a captura do turno, como conteúdo. A hipótese
  registada a 2026-10-07 — «o conteúdo veio no campo de raciocínio, que o adaptador não lê» —
  está errada nessa forma: se veio em `reasoning_content`, foi lido.
- O que o adaptador **não** descodifica: os outros nomes do raciocínio (`reasoning`,
  `thinking`, `thinking_blocks`, `reasoning_details`), `refusal`, `message.function_call`, as
  `choices` além da primeira, `usage.completion_tokens_details.reasoning_tokens` e
  `system_fingerprint`. Um `finish_reason` fora do mapa vira `other` e o valor bruto perde-se.
- Oito formas de resposta diferentes dão hoje o mesmo registo (desenho §2.2, H1 a H8): um turno,
  `stop`, zero tool calls, tokens de saída, texto vazio. Nada do que se grava as separa.

### Decidido pelo dono (2026-10-07)

1. **D1 — sim.** Mede-se a forma das respostas em produção durante uma ou duas séries: que
   partes vieram e o tamanho de cada uma, **nunca o texto**.

### Objectivo

Em cada turno, o gateway calcula do corpo cru da resposta uma **ficha da forma** — presença,
forma JSON e tamanho de cada campo, em vocabulário fechado e em inteiros — e o runtime grava-a no
`turn.recorded`. A ficha não leva nenhum byte de valor nem nenhum nome de chave do provider.
Atrás de um interruptor, desligado por omissão; com ele desligado, os bytes são os de antes.

### Critérios de Aceitação

- [ ] Interruptor `AOS_MODEL_RESPONSE_SHAPE` com dois valores: `off` (a omissão) e `observe`. Um
      valor inválido recusa o arranque. Não há `enforce`: é medição e não decide nada.
- [ ] Com `off` ou sem a variável, a sonda não corre e o binário é byte a byte o anterior, provado
      por comparação com a base pelo guião dos tickets anteriores (eventos `turn.recorded`,
      captura, métricas, corpos de resposta e desfechos, depois de normalizar pastas temporárias
      e durações).
- [ ] A ficha é calculada por uma **sonda própria** sobre o corpo cru, no molde das duas que já
      existem (`wireUsageProbe` e `wireCachedProbe`): conhece só os campos que lhe interessam e
      **não pode fazer falhar a resposta**. Um corpo que a sonda não consiga ler dá a ficha
      `ilegivel`, e o turno segue como hoje. Teste com corpo truncado, com JSON inválido e com
      tipos inesperados em todos os campos.
- [ ] Campos da ficha, todos de vocabulário fechado ou inteiros:
  - `content`: `ausente`, `nulo`, `vazio`, `so_brancos`, `texto`, `partes`, `outro`; e
    `content_bytes`;
  - `reasoning`: `nenhum`, `reasoning_content`, `reasoning`, `thinking`, `thinking_blocks`,
    `reasoning_details`, `varios`; `reasoning_form` (`string`, `objecto`, `lista`, `outro`);
    `reasoning_bytes` (bytes do valor JSON); `reasoning_signed` (`sim`, `nao`);
  - `refusal`: `ausente`, `nulo`, `texto`;
  - `tool_calls_n`; `tool_call_id` (`nenhum`, `call_`, `functions_ponto`, `uuid`, `numerico`,
    `vazio`, `outro`) e `tool_call_id_max_bytes`; `arguments_form` (`string`, `objecto`,
    `outro`);
  - `legacy_function_call` (`sim`, `nao`); `choices_n`;
  - `finish_reason_mapped` (`sim`, `nao`): se o valor bruto está no mapa do AOS-491;
  - `reasoning_tokens` (inteiro, ou ausente); `system_fingerprint` (`sim`, `nao`);
  - `unknown_keys_n`: quantas chaves de `message` a sonda não conhece;
  - `shape_digest`: `sha256` da lista ordenada de pares (caminho da chave, tipo JSON) da
    resposta. Agrupa formas iguais sem guardar texto do provider.
- [ ] Cada uma das formas H1 a H8 do desenho §2.2 dá uma **classe distinta** na ficha. Um teste
      por forma, com o corpo de resposta em ficheiro.
- [ ] **Sem conteúdo.** Um corpo com sentinelas em todos os valores e em nomes de chave
      desconhecidos não deixa nenhuma sentinela no `turn.recorded`, na captura, nas métricas, em
      spans nem em logs. O `shape_digest` é calculado sobre caminhos e tipos; o teste prova que
      dois corpos que só diferem nos valores têm o mesmo digest, e que um nome de chave
      desconhecido não aparece em claro em lado nenhum.
- [ ] A ficha grava-se no `turn.recorded` como campo opcional (`omitempty`), ao lado de
      `stop_reason` e `tools_offered`. **Não entra na captura**: segue o precedente do
      `ToolsOffered` — a captura não muda de bytes nem de digest, nenhuma golden muda, e um turno
      reproduzido numa retoma volta sem o campo. O replay de um run gravado com a ficha é fiel
      (teste diferencial loop/replay do AOS-492).
- [ ] Atravessa a fronteira como um campo de `ModelResponse` declarado por quem fez o pedido, no
      molde de `Projection` e `RouteCheck`; na porta é um campo `json:"-"` de `ChatResponse`,
      como o `Route`. Versão da porta MINOR. O runtime não lê a ficha para decidir nada (teste:
      a regra de terminação e o veredicto são os mesmos com e sem ficha).
- [ ] Métrica do nó `aos_model_response_shape_total{content,reasoning,stop_reason}`, só com os
      valores do vocabulário fechado; a cardinalidade máxima fica escrita no ticket e presa por
      teste. Com o interruptor desligado, o `/metrics` é byte a byte o de antes.
- [ ] O caminho de streaming não é alterado e fica declarado como não coberto (os runs não o
      usam).
- [ ] O `.env.example`, o `docker-compose.prod.yml` e o runbook de deploy registam a variável, a
      omissão e a ordem de recuo (voltar a `off`; os eventos já gravados com a ficha continuam
      legíveis por um binário anterior, provado por teste de leitura).
- [x] Revisão adversarial independente com mutações, antes da fusão. *(2026-10-08, sobre `5cac15f3`: sem bloqueantes; diferencial de 152 corpos entre a base e o ramo, 8 mutações mortas em 8. Dois achados a fechar antes de ligar `observe` — o digest e o raciocínio vazio —, corrigidos no mesmo ramo; ver Estado.)*
- [ ] **Verificação em produção, critério P1 da fase:** com `observe` ligado, uma série em
      produção em que 100% dos turnos fechados `empty_output` têm a ficha gravada, em pelo menos
      3 ocorrências, e **a classe dominante fica escrita no acompanhamento** — a série nomeia a
      causa de um `empty_output`. Com a taxa medida, uma série de 60 planos tem cerca de 73% de
      probabilidade de conter um vazio e duas séries cerca de 93%; os turnos não vazios da mesma
      série respondem logo a metade da pergunta (se o nó de resumo traz ou não raciocínio).

### Fora de âmbito

- Ler os campos que a ficha conta (AOS-509 trata os que hoje fazem recusar a resposta e os
  outros nomes do raciocínio).
- Decidir o que quer que seja com a ficha: recusar um modelo, escolher uma rota, repetir um
  turno.
- Decifrar a captura dos três runs já gravados para ler o tamanho do raciocínio (o «passo zero»
  do desenho §2.3): exige autorização do dono e uma ferramenta de leitura; fica de fora enquanto
  este ticket puder responder pela mesma pergunta.
- Guardar o valor bruto de um `finish_reason` fora do mapa.
- Streaming.

### Estado

**IMPLEMENTADO, DESLIGADO POR OMISSÃO (2026-10-07); produção por verificar.** Decisão D1 do dono
tomada no mesmo dia.

- **Sonda e travessia.** `port.ProbeResponseShape` (`packages/platform/model-gateway/port/shape.go`)
  calcula a ficha do corpo cru; o adaptador HTTP corre-a só com a medição ligada, depois de a
  resposta estar descodificada; segue por `port.ChatResponse.Shape` (`json:"-"`, contrato `1.5.0`)
  e `agentruntime.ModelResponse.Shape` até `response_shape` do `turn.recorded`. Não entra na
  captura. O runtime fecha o vocabulário outra vez à entrada do registo: um valor fora dele torna
  a ficha `{"ilegivel":true}`.
- **Interruptor.** `AOS_MODEL_RESPONSE_SHAPE=off|observe`; vazio é `off`; outro valor recusa o
  arranque (`ErrBadModelResponseShape`).
- **Métrica.** `aos_model_response_shape_total{content,reasoning,stop_reason}`, **342 séries no
  máximo** desde a revisão (eram 300): 7 formas de conteúdo × 8 valores do raciocínio × 6 motivos
  de paragem (336), mais a ficha ilegível, que só existe com `reasoning="nenhum"` (6). Preso por
  teste.
- **Prova de `off`.** O run de referência do AOS-490 no nó composto, com a variável ausente,
  vazia e em `off`: pedidos, `turn.recorded`, parte em claro das capturas, sequência de eventos e
  famílias do `/metrics` são os goldens medidos na base pelo AOS-505
  (`TestAOS507_No_Off_SaoOsBytesDaBase`); e, para os 87 casos de wire, o turno é o mesmo com e
  sem medição, tirando a ficha (`TestAOS507_Off_ASondaNaoCorre`).
- **Desvios e decisões de implementação, a confirmar.** (1) `reasoning_bytes` são os bytes do
  valor JSON, como o critério diz: um raciocínio em string vazia conta 2 (as aspas);
  `content_bytes` são os bytes do texto já descodificado. (2) `refusal` em string vazia conta
  `nulo`. (3) `tool_call_id` e `arguments_form` de uma resposta com tool calls de classes
  diferentes dão `outro`. (4) O `shape_digest` fica ausente acima de 4096 pares distintos
  (caminho, tipo); a sonda desce até 32 níveis. (5) `ilegivel` só acontece com um corpo que não é
  um objecto JSON — que a descodificação também recusa —, pelo que na prática não chega a um
  `turn.recorded`; fica coberto por teste na sonda. (6) A prova «sem sentinelas» cobre os eventos
  do run e o `/metrics`; spans e logs não foram varridos por teste (a ficha não é escrita em
  nenhum dos dois).
- **Revisão adversarial (2026-10-08) e correcções.** (O-1) O `shape_digest` deixou de cobrir
  nomes de chave escritos pelo modelo: do interior de `arguments`, dos campos de raciocínio, de
  `content` em partes e de `provider_specific_fields` entra só o tipo do valor (dois corpos com
  `arguments:{"iban":…}` e `{"nif":…}` têm o mesmo digest). (O-2) Um campo de raciocínio presente
  e vazio (`""`, `[]`, `{}`, `false`, `0`) não é raciocínio: `reasoning` ganha o valor `vazio`,
  distinto de `nenhum` e de um nome de campo — sem isto, `content=""` com
  `reasoning_content=""` caía na série de H1. A métrica passa a ter **342 séries no máximo**
  (7 × 8 × 6, mais 6 da ficha ilegível). Resíduos registados e não corrigidos: uma resposta
  RECUSADA não tem ficha (a sonda corre depois da descodificação; fica o contador por causa); um
  valor inválido da variável só recusa o arranque com `AOS_MODEL_ENDPOINT` definida, como as
  variáveis irmãs.
- **A ficha pela rota de produção** (medido com a imagem do proxy, duas corridas:
  `docs/reports/wire-live-aos508-2026-10-07.md`). A primeira corrida concluiu que o proxy
  «retira» `refusal`, `thinking` e `reasoning_details`, e que a ficha não separava por isso
  H2-`thinking`, H2-`reasoning_details` e H5 de H6. **A segunda (2026-10-08) corrigiu-o:** o proxy
  MOVE esses campos, com o valor, para `message.provider_specific_fields`, e copia `reasoning`
  para `reasoning_content` deixando o original no mesmo objecto. A ficha ganhou por isso dois
  campos, fora da lista do critério e nos mesmos vocabulários fechados: **`psf_refusal`** e
  **`psf_reasoning`** (a recusa e o raciocínio dentro de `provider_specific_fields`; ausentes
  quando a mensagem não traz o objecto). Com eles as formas H1 a H8 continuam separadas depois do
  proxy, preso por teste sobre os corpos que ele entregou
  (`TestAOS507_PosProxy_AsFormasDoVazioContinuamSeparadas`). Medido na mesma corrida:
  `usage.completion_tokens_details.reasoning_tokens` e as chaves de assinatura **sobrevivem** ao
  proxy. O que continua invisível ao gateway por esta rota: o `finish_reason` bruto (o proxy
  normaliza-o), e `content` em partes ou raciocínio em objecto (o proxy responde 500).
- **Medição de 2026-10-08 sobre as capturas seladas dos 3 runs `empty_output`** (feita pela
  coordenação; **sem decifrar nada — só o tamanho do criptograma**): 852, 450 e 628 bytes para
  161, 77 e 114 tokens de saída, isto é, 5,3 a 5,8 bytes por token, contra 3,5 a 4,8 (mediana 4,1
  a 4,2) nos 117 resumos bem-sucedidos das mesmas séries. A captura dos turnos vazios tem
  conteúdo proporcional aos tokens: a resposta veio toda no raciocínio (`reasoning_content`, que
  é também para onde o proxy leva `reasoning`), com `content` vazio. **H1 ou H2-`reasoning` é a
  hipótese fortemente apoiada; não é prova** — não se decifrou nada. A ficha em `observe`
  confirma-o numa série: a classe esperada é `content` em `nulo` ou `vazio` com
  `reasoning="reasoning_content"`.
- **Por fazer.** Ligar `observe` em produção e a verificação do critério P1 (decisão do dono).

---

## AOS-508 — Providers falsos de wire para CI, e um gate opcional que os põe atrás da imagem real do proxy

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: são fixtures e um gate de CI. O ADR-036 é citado só como contexto do contrato do gateway. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A2 (estado opaco do provider) |
| Tipo | test |
| Prioridade | P1: sem isto, cada diferença de wire de um modelo novo só se descobre em produção |
| Estimativa | M |
| Dependências | AOS-505 (o molde do gate opcional com a imagem do proxy) |
| Bloqueia | AOS-509; o critério P5 da fase A2 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §1, §4 e §5.2, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A2, §6), `scripts/ci/rota-live.sh`, `packages/testkit/README.md`, `packages/platform/model-gateway/port/port.go`, `packages/platform/model-gateway/internal/adapters/openai_http.go` |

### Contexto

A promessa da arquitectura-alvo é que um modelo de uma classe de wire qualificada entra sem
código. Hoje só uma rota foi exercitada (Kimi por LiteLLM), e as diferenças de wire conhecidas
entre providers não têm teste nenhum: descobriu-se em produção, a 2026-10-07, que **3 planos em
140** fecham `empty_output` no nó de resumo (`plan-e2e-v0151a-1791385126~n2`,
`plan-e2e-v0151a-1791385255~n2` e um na série `v0151b`; um turno, `stop`, `output_tokens` 161 e
77), sem que se saiba que forma tinha a resposta.

Lido no código da base (desenho §1.1 e §10): uma resposta com `content` em lista de partes, ou
com `function.arguments` em objecto, faz recusar a resposta **inteira**; os outros nomes do
raciocínio, `refusal` e `function_call` não são descodificados; só a primeira `choice` é lida; o
id de tool call do provider é descartado e o que volta é o do runtime, `<passo>-tool-<n>`.

O `scripts/ci/rota-live.sh` (AOS-505) já corre a imagem de produção do proxy num gate opcional.
Falta o outro lado: um provider falso por trás dela, que devolva cada forma conhecida, para se
ver o que o proxy faz a cada uma antes de uma série em produção.

### Decidido pelo dono (2026-10-07)

1. **D1 — sim** (medir a forma das respostas). Este ticket é a metade local dessa medição: não
   toca no modelo real nem em produção.
2. A medição directa ao modelo real ficou autorizada **só para o posto de ensaio** (decisão D5,
   2026-10-07, que revê a de 2026-10-06). Este ticket não depende dela.

### Objectivo

Um conjunto de providers falsos, deterministas e sem rede, que respondem no wire de chat
completions com cada variação conhecida, utilizáveis pelos testes do gateway; e um gate opcional
que os põe **atrás da imagem real do proxy**, para registar o que o proxy entrega ao gateway em
cada caso. Só CI: nenhum binário de produção muda.

### Critérios de Aceitação

- [ ] Os falsos vivem em `packages/testkit` (ou num pacote de teste do gateway, se o `layer-lint`
      o exigir — decide-se na implementação e fica escrito), respondem por `net/http` da stdlib,
      sem rede externa e sem relógio real. Cada um é um caso nomeado, com o corpo de resposta em
      ficheiro versionado.
- [ ] Variações cobertas, uma por caso, cada uma com e sem tool calls quando faz sentido:
  - `content`: `null`, ausente, `""`, só brancos, texto, **lista de partes** (só texto; texto e
    uma parte que não é texto);
  - `function.arguments`: string JSON, **objecto JSON**, string que não é JSON, vazio;
  - raciocínio em cada nome — `reasoning_content`, `reasoning`, `thinking`, `thinking_blocks`,
    `reasoning_details` — em string, objecto e lista, com e sem assinatura
    (`signature`, `thought_signature`), e vários nomes na mesma resposta;
  - `refusal` preenchido com `content` nulo;
  - `message.function_call` (a forma antiga), com `finish_reason` `function_call` e com `stop`;
  - **várias `choices`**, com a resposta na segunda;
  - **ids de tool call**: `call_…`, `functions.<tool>:<n>`, numérico, UUID, vazio, ausente,
    repetido na mesma resposta, e de comprimento fixo;
  - `finish_reason` fora do mapa (`end_turn`, `tool_use`, `STOP`, ausente, `null`);
  - `usage` ausente, vazio, só com o total, e com `completion_tokens_details.reasoning_tokens`;
  - tool calls paralelas.
- [ ] As oito formas H1 a H8 do desenho §2.2 (as que encaixam num `empty_output`) têm cada uma o
      seu caso, e os casos são os que o AOS-507 usa para provar que dá classes distintas.
- [ ] **Falsos que validam o pedido**, para o segundo turno: um que recusa com 400 um
      `tool_call_id` que não emitiu ou que excede um comprimento; e um que dá 400 ao segundo
      turno se o `assistant` com tool calls não trouxer o raciocínio ou a assinatura do
      primeiro. Com **controlo negativo real**: o teste que hoje prova que o gateway não devolve
      esse estado fica vermelho contra o falso (regista-se como comportamento esperado de hoje,
      classe «não suportada»), e passa a verde só quando o trabalho do estado opaco existir.
- [ ] Para cada caso fica registado, num ficheiro versionado, **o que o gateway faz hoje**:
      turno aceite (com que texto, tool calls e motivo de paragem), resposta recusada (com que
      erro) ou turno com veredicto negativo. É a linha de base que o AOS-509 muda, e só nos
      casos que ele nomeia.
- [ ] Gate opcional `ci-wire-live`, no molde de `scripts/ci/rota-live.sh`: sobe a imagem do
      proxy com o digest fixado, com os falsos como provider, e regista para cada caso o corpo
      que o proxy entrega (nomes de campo, `content: null` reescrito ou não, campos
      acrescentados). Salta sem Docker e **redeclara o salto** no veredicto
      (`AOS_SKIPPED_STEP`); não entra no `run.sh` como gate obrigatório. O resultado de uma
      corrida fica num relatório em `docs/reports/`, com o digest da imagem.
- [ ] Nenhum caso contém dados de titular nem texto de produção: os corpos são escritos à mão.
- [ ] Os falsos não são alcançáveis por código de produção: o `layer-lint` e um teste de
      importações provam que nenhum pacote fora de testes os importa.
- [ ] A matriz de suporte (§6 do acompanhamento) ganha, por classe, a referência ao caso que a
      representa. Uma classe só pode ser marcada «qualificada» com o seu caso verde (critério P5
      da fase).

### Fora de âmbito

- Mudar o que o gateway faz a qualquer forma (AOS-509).
- Um provider falso que valide assinaturas criptográficas: um falso não as valida; isso só o
  modelo real prova.
- Taxas de comportamento do modelo (tool call em texto, resposta vazia, recusa): são do banco de
  ensaio, planeado e por numerar.
- Streaming.

### Estado

**IMPLEMENTADO (2026-10-07).**

- **Onde vivem.** Em `packages/platform/model-gateway/internal/wirefake`, e não em
  `packages/testkit`: o `layer-lint` não deixa uma camada de produção importar o testkit, e são
  os testes da porta e do gateway que usam os casos. `internal/` impede a importação de fora do
  módulo, e `TestAOS508_NenhumCodigoDeProducaoImportaOsFalsos` varre o módulo.
- **Casos.** 94 corpos em ficheiro (`casos/*.json`; 87 na entrega e sete da revisão), um servidor `net/http` (`Servidor`) e três
  falsos que validam o segundo turno (`Validador`: id emitido e comprimento, raciocínio,
  assinatura), com controlo negativo — o estado reposto à mão é aceite, sem ele dá 400. O teste
  regista o 400 como o comportamento de hoje (`TestAOS508_SegundoTurno_EstadoOpacoNaoSuportadoHoje`).
- **Linha de base.** `linha_de_base_aos508.json` é o que o gateway fazia a cada caso antes do
  AOS-509 (75 turnos, 19 recusas; os sete casos da revisão entraram com o que o campo string da
  base lhes fazia), gerado na base e congelado; `comportamento.json` é o de hoje,
  preso por teste.
- **Gate `ci-wire-live`** (`scripts/ci/wire-live.sh`, fora do `run.sh`). Uma corrida, com a imagem
  de produção do proxy: `docs/reports/wire-live-aos508-2026-10-07.md`. O proxy entregou 200 em 64
  casos e 500 em 23 (69 e 25 na segunda corrida, com 94 casos); copia `reasoning` para
  `reasoning_content`, move `thinking`, `reasoning_details` e `refusal` para
  `provider_specific_fields`, entrega `arguments` em objecto como string, normaliza o
  `finish_reason` e responde 500 a `content` em partes.
- **Revisão (2026-10-08), M-8.** Os falsos são fiéis como PROVIDER, e não como «o que o gateway
  vê em produção». Os 69 corpos que a imagem de produção do proxy ENTREGOU na segunda corrida do
  gate ficaram congelados em `casos_pos_proxy/`, e o que o gateway faz a cada um está em
  `comportamento_pos_proxy.json` (68 turnos, uma recusa), preso por
  `TestAOS508_PosProxy_OQueOGatewayFazAoQueOProxyEntrega`. A segunda corrida regista também os
  tokens de raciocínio e as assinaturas (sobrevivem os dois), e corrige a leitura da primeira:
  `refusal`, `thinking` e `reasoning_details` são movidos para `provider_specific_fields`, não
  retirados.
- **Por fazer.** A matriz de suporte (§6 do acompanhamento) ainda não refere os casos por
  classe; os casos não cobrem streaming (fora de âmbito).

---

## AOS-509 — O gateway deixa de recusar a resposta inteira por formas válidas do wire, e lê os outros nomes do raciocínio como raciocínio

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum e não emenda nenhum: corrige a descodificação da porta do gateway dentro do contrato já decidido. O ADR-036 (§2.7: o raciocínio é carga opaca e o seu único destino é a captura) e o ADR-037 (o veredicto `empty_output`) são citados como o contrato que este ticket NÃO muda. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A2 (estado opaco do provider) |
| Tipo | fix |
| Prioridade | P1: um provider que mande `content` em partes ou `arguments` em objecto perde hoje a resposta inteira — a mensagem, as tool calls e o usage |
| Estimativa | M |
| Dependências | AOS-507 (a medição diz que formas existem em produção), AOS-508 (os casos e a linha de base) |
| Bloqueia | O trabalho do estado opaco, planeado e por numerar |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §0, §1.1 e §3(b), `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A2), `docs/adr/ADR-036-o-tail-e-a-forma-canonica-da-conversa.md` §2.7, `packages/platform/model-gateway/port/port.go`, `packages/platform/model-gateway/port/normalize.go`, `packages/platform/model-gateway/runtime_adapter.go`, `packages/kernel/agent-runtime/model.go`, `packages/kernel/agent-runtime/completion.go`, `packages/kernel/agent-runtime/replay/nondeterminism_capture.go` |

### Contexto

Lido no código da base e conferido a 2026-10-07 (desenho §0 e §10):

- `Message.Content` é `string` (`packages/platform/model-gateway/port/port.go:72`) e
  `FunctionCall.Arguments` é `string` (`port.go:56`). Uma resposta com `content` em lista de
  partes, ou com `arguments` em objecto JSON, faz o `encoding/json` devolver erro
  (`port.go:110-112`), `UnmarshalChatResponse` devolve a resposta vazia com esse erro
  (`port/normalize.go:165-169`) e o adaptador propaga-o (`internal/adapters/openai_http.go:122-125`).
  O turno falha. É o mesmo defeito que o AOS-490 fechou para o `reasoning_content`
  (`port.go:98-102`), ainda aberto para estes dois campos.
- O raciocínio só é lido quando vem em `reasoning_content`. Nos outros nomes (`reasoning`,
  `thinking`, `thinking_blocks`, `reasoning_details`) a chave não é declarada e o valor
  perde-se em silêncio.

Em produção não se observou nenhuma resposta recusada por estas formas. O que se observou, a
2026-10-07, foram **3 `empty_output` em 140 planos** (`plan-e2e-v0151a-1791385126~n2`,
`plan-e2e-v0151a-1791385255~n2` e um na série `v0151b`): um turno, `stop`, `output_tokens` 161 e
77, texto vazio. Se a causa for o texto ter vindo num campo de raciocínio (hipóteses H1 e H2 do
desenho), este ticket **não** a resolve, e não deve: ver a decisão D3 abaixo.

### Decidido pelo dono (2026-10-07)

1. **D3 — não: o raciocínio nunca é usado como resposta.** Se a resposta vier vazia e o modelo
   tiver deixado raciocínio, a resposta continua vazia e o run fecha `empty_output`. Porquê:
   - **O veredicto está certo.** O modelo não respondeu. Promover a resposta um texto que o
     modelo não deu como resposta refaz o verde falso que a fase A0 fechou.
   - **O raciocínio não é a resposta.** É deliberação: hipóteses abandonadas, cópias literais do
     `plan_input` untrusted, instruções que o modelo decidiu não seguir. Publicado como saída do
     nó, passava ao nó seguinte e a quem pediu material que o modelo excluiu.
   - **O contrato existente.** O runtime não lê nem interpreta o raciocínio; o único destino é a
     captura. O campo aceita qualquer forma JSON: a «resposta» podia ser um objecto de um
     fornecedor.
   - **O replay.** «Se o texto vier vazio, usa o raciocínio» seria uma regra nova de conclusão,
     com layout novo.
   - **O canal.** Alargava o que conta como saída a um canal que o provider controla.

   As respostas vazias tratam-se pela nova tentativa (decisão D2: AOS-510 e AOS-511). Se a
   medição do AOS-507 mostrar que o texto em `reasoning_content` é a resposta bem formada (um
   servidor que falhou a separar raciocínio de resposta), a correcção é **na rota** —
   configuração do proxy ou parâmetro do pedido —, e não uma regra do runtime.

### Objectivo

As formas do wire que são válidas e que hoje derrubam a resposta inteira passam a dar um turno:
`content` em partes de texto e `function.arguments` em objecto. Os outros nomes do raciocínio
passam a ser lidos **como raciocínio** — carga opaca, só para a captura, nunca como resposta.
Sem interruptor: a mudança só alcança respostas que hoje dão erro, com uma excepção delimitada e
declarada abaixo.

### Critérios de Aceitação

- [ ] **`content` em lista de partes.** Uma lista em que todas as partes são de texto
      (`{"type":"text","text":"…"}`) é lida como a concatenação dos textos, pela ordem, **sem
      separador**. Lista vazia é texto vazio. Testes: uma parte, várias, parte de texto vazia.
- [ ] **Partes que não são texto continuam a recusar, com causa própria.** Uma lista com
      qualquer parte que não seja texto (imagem, áudio, recusa em parte, tipo desconhecido,
      parte sem `type`) recusa a resposta com um erro nomeado e distinto do erro de JSON de
      hoje. Porquê: aceitar a resposta deitando fora a parte entregava como completa uma
      resposta a que falta conteúdo. A recusa conta em
      `aos_model_response_rejected_total{causa}`, em vocabulário fechado, sem nenhum byte da
      resposta.
- [ ] **`function.arguments` em objecto.** Um objecto JSON é lido como os **bytes JSON crus**
      que vieram, sem re-serializar. `null` continua a ser vazio, como hoje. Outra forma (lista,
      número, booleano) recusa a resposta com causa própria. Daí em diante os argumentos seguem
      o caminho de hoje: o schema da tool valida-os e o Reference Monitor medeia a chamada.
      Teste: a mesma chamada com os argumentos em string e em objecto dá o mesmo `Input`,
      byte a byte, quando o texto é o mesmo.
- [ ] **Outros nomes do raciocínio.** Quando `reasoning_content` está ausente ou é `null`, o
      gateway lê o primeiro que estiver presente de `reasoning`, `reasoning_details`,
      `thinking_blocks`, `thinking`, por esta ordem fixa, como carga opaca — a mesma regra do
      AOS-490 (string descodificada; qualquer outra forma, os bytes JSON crus). Nunca é erro.
      Quando `reasoning_content` está presente, vale só ele, **como hoje**.
- [ ] **O raciocínio nunca é a resposta (D3).** Em todos os casos, `Text` vem só de `content`.
      Testes, um por nome de campo e por forma: `content` nulo, vazio ou só com brancos, com
      raciocínio preenchido ⇒ `Text` vazio e o run fecha `empty_output`. Um teste de mutação
      prova que trocar a origem do `Text` para o raciocínio fica vermelho.
- [ ] **O raciocínio continua a não sair e a não aparecer.** O valor lido de qualquer nome vai
      só para a captura (selado com cifra por titular; referência em modo sensível), não entra
      no tail, no prompt, no `turn.recorded`, em spans nem em logs, e `MarshalWire` continua a
      retirá-lo de todos os pedidos. Teste com sentinelas em cada nome.
- [ ] **As respostas que hoje passam ficam byte a byte.** Para todo o caso da linha de base do
      AOS-508 que hoje dá um turno aceite: `Text`, tool calls, motivo de paragem, tokens,
      `turn.recorded`, tail, `prompt_hash`, veredicto **e captura** são byte a byte os de antes.
      Nenhuma golden muda. **Excepção única, declarada:** uma resposta que hoje passa e traz o
      raciocínio noutro nome **sem** `reasoning_content` passa a ter o campo `reasoning` na
      captura, onde hoje se perde; tudo o resto dela fica igual. A linha de base do AOS-508
      lista os casos em que isso acontece, e o AOS-507 diz se algum ocorre em produção antes
      de este ticket sair.
- [ ] Captura antiga: uma captura gravada antes deste ticket descodifica e reproduz como antes
      (teste de replay com goldens existentes).
- [ ] As formas que este ticket **não** passa a aceitar ficam com teste de que o comportamento é
      o de hoje: `refusal`, `message.function_call`, `choices` além da primeira, `finish_reason`
      fora do mapa, id de tool call do provider (continua descartado).
- [ ] Versão da porta do gateway: MINOR, aditiva; registada no contrato da porta e no
      `CHANGELOG.md`.
- [x] Revisão adversarial independente com mutações, antes da fusão. *(2026-10-08, sobre `5cac15f3`: sem bloqueantes; em 151 de 152 corpos nenhuma resposta que já passava muda fora da excepção declarada; a 152.ª — `content` com a chave repetida e `null` no fim — foi corrigida no mesmo ramo; ver Estado.)*
- [ ] Verificação em produção: numa série de pelo menos 40 planos depois da entrada,
      `aos_model_response_rejected_total` fica a zero e os desfechos são os da série anterior.

### Fora de âmbito

- **Usar o raciocínio como resposta: rejeitado** (decisão D3).
- Guardar o raciocínio de **todos** os nomes quando vêm vários, as assinaturas e o id de tool
  call do provider, e devolvê-los ao provider: é o trabalho do estado opaco, planeado e por
  numerar, para a segunda família escolhida na decisão D4 (Claude, 2026-10-07).
- Ler `refusal` e `message.function_call`, e escolher entre várias `choices`: abre-se ticket se
  a medição do AOS-507 mostrar que ocorrem.
- Conteúdo multimodal na resposta (fases A5 e A6).
- Streaming.

### Estado

**IMPLEMENTADO (2026-10-07); produção por verificar.** Decisão D3 do dono registada aqui e no
acompanhamento (§4).

- **Onde.** `Message.UnmarshalJSON` e `FunctionCall.UnmarshalJSON`
  (`packages/platform/model-gateway/port/port.go`), contrato da porta `1.6.0`. Erros nomeados
  `ErrContentPartNotText`, `ErrContentForm` e `ErrArgumentsForm`, sem bytes da resposta.
- **Métrica.** `aos_model_response_rejected_total{causa}`, cinco causas: `content_parte_nao_texto`,
  `content_forma`, `arguments_forma`, `json_invalido`, `sem_choices`. Existe sempre que o gateway
  está composto — é a única família que entra no `/metrics` sem interruptor.
- **Prova do «byte a byte».** Contra a linha de base do AOS-508: os 69 casos que já davam um
  turno dão hoje o mesmo turno (texto, tool calls, motivo, `Final`, modelo, tokens), e os 24 em
  que algo muda são exactamente a excepção declarada — só o raciocínio, de vazio a preenchido
  (`TestAOS509_OQueJaPassavaFicaByteAByte`). No nó, a resposta de referência dita nas formas
  novas dá os goldens da base no pedido seguinte, nos `turn.recorded` e nas capturas
  (`TestAOS509_No_FormasNovas_MesmosBytesDaBase`).
- **Casos da excepção** (resposta que já passava e traz raciocínio noutro nome sem
  `reasoning_content`): `h2_raciocinio_noutro_campo`, `rac_blocos_assinados`,
  `rac_reasoning_content_nulo_e_reasoning`, `rac_varios_sem_reasoning_content` e os casos
  `rac_reasoning_*`, `rac_reasoning_details_*`, `rac_thinking_blocks_*` e `rac_thinking_*`. Por
  trás do proxy de produção só `thinking_blocks` em lista chega com outro nome
  (`docs/reports/wire-live-aos508-2026-10-07.md`).
- **Decisões de implementação, a confirmar.** (1) `content` numa forma que não é string, `null`
  nem lista recusa com causa própria (`content_forma`), como as partes que não são texto. (2)
  Uma parte de texto sem `text`, ou com `text` que não é string, conta como parte que não é
  texto. (3) `arguments` em objecto guarda os bytes crus, sem re-serializar nem compactar: a
  ordem das chaves e os espaços são os que vieram. (4) `reasoning_content` presente e vazio vale
  sozinho, como antes.
- **Revisão adversarial (2026-10-08) e correcções.** (M-1) `content` e `function.arguments` com
  a chave REPETIDA lêem-se uma ocorrência de cada vez, como o campo string da base: uma string
  substitui, `null` não altera (`{"content":"a","content":null}` volta a dar `a`). (M-2) Uma
  parte com `type` ou `text` repetido não é de texto, e recusa. (M-3) Nos outros nomes do
  raciocínio só conta um campo COM conteúdo: `thinking:false`, `reasoning:0`,
  `reasoning_details:[]` e `""` não gravam nada na captura. `reasoning_content` fica como o
  AOS-490 o lia, em qualquer forma. Os sete casos entraram no wirefake e na linha de base.
- **`content: []` muda de classe de falha.** Uma lista de partes vazia era um erro de
  descodificação (o turno falhava) e passa a ser um turno com texto vazio, que fecha
  `empty_output`. Interessa ao AOS-510: uma resposta que antes falhava o turno passa a ser
  elegível para a nova tentativa por vazio. Pela rota de produção não acontece — o proxy
  responde 500 a `content` em lista.
- **O `/metrics` com tudo desligado.** `aos_model_response_rejected_total{causa}` (cinco séries)
  existe sempre que o gateway de modelo está composto, também com
  `AOS_MODEL_RESPONSE_SHAPE=off`: é a única diferença do `/metrics` face à base que não depende
  de um interruptor.
- **Alcance real pela rota de produção.** Medido com a imagem do proxy: `content` em partes e
  raciocínio em objecto dão 500 no proxy, e `arguments` em objecto chega em string. Por esta
  rota o ticket é quase inerte — o ganho é `thinking_blocks` em lista lido como raciocínio (o
  raciocínio em `thinking` e em `reasoning_details` chega dentro de `provider_specific_fields`, que
  o gateway não lê). O
  ganho inteiro é para uma rota sem este proxy e para a segunda família de modelos.
- **Resíduos da revisão, não corrigidos.** `reasoning_content:""` com `reasoning` preenchido
  continua a perder o raciocínio (é a letra do critério: «ausente ou `null`»; pelo proxy de
  produção não acontece). `arguments` em objecto com UTF-8 inválido dentro de uma string segue
  em bytes crus, sem a substituição que a forma string sofria (raciocinado, não testado). Os
  consumidores directos da porta fora do nó (o planeador do `aos-orq`, as evals) herdam a
  descodificação tolerante sem a métrica.
- **Por fazer.** A série de 40 planos em produção.

---

## AOS-513 — O perfil da rota declara os parâmetros do pedido, a versão da projecção e a classe de estado; sem perfil que os declare, o pedido é o de hoje

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum. Estende o perfil da rota do AOS-505 dentro do contrato já decidido; se a emenda ao ADR-036 §2.8 (o perfil da rota) for necessária, é entregável deste ticket e fica escrita no ADR, não aqui. O ADR-036 e o ADR-034 são citados como o contrato que se mantém. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-06 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A2 (estado opaco do provider) |
| Tipo | feat |
| Prioridade | P1: hoje o wire não tem onde levar um parâmetro de raciocínio, e a versão da projecção e o texto do protocolo são do nó inteiro — afinados para um só modelo |
| Estimativa | M |
| Dependências | AOS-505 (o perfil da rota e o seu digest), AOS-506 (as versões de projecção publicadas), AOS-507 (a ficha que mostra o efeito), AOS-512 (a qualificação de um perfil faz-se no banco) |
| Bloqueia | AOS-515 (a projecção só devolve estado se o perfil o exigir), AOS-516 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §3(c) e §6, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A2), `docs/adr/ADR-036-o-tail-e-a-forma-canonica-da-conversa.md` §2.8, `packages/kernel/agent-runtime/model.go`, `packages/platform/model-gateway/projection.go`, `packages/platform/model-gateway/port/normalize.go` |

### Contexto

- **O pedido não leva parâmetros de raciocínio.** O wire não tem onde os levar (desenho §1.2 e
  §3c). Se a resposta vazia se confirmar como «tudo no raciocínio, `content` vazio» (medição
  das capturas, 2026-10-08: 5,3 a 5,8 bytes por token de saída contra 3,5 a 4,8), a correcção
  na origem é um parâmetro do pedido — a ressalva da decisão D3 já o dizia: corrige-se **na
  rota**, não no runtime.
- **A versão da projecção é do nó inteiro** (`AOS_MODEL_PROJECTION_VERSION`). O texto do
  protocolo foi afinado para um só modelo (diagnóstico acordado com o dono, 2026-10-08). Com
  duas famílias, o que serve uma pode prejudicar a outra.
- **O proxy reencaminha tudo.** `drop_params` não fez diferença (medido no AOS-505): um
  parâmetro que o provider não conheça pode dar 400 em **todos** os turnos da rota.

**O que os fornecedores documentam** (consultado a 2026-10-08; o ticket assume só o que está
marcado «confirmado»):

| Regra | Estado | Fonte |
|---|---|---|
| Kimi: o `kimi-k2.6` aceita `thinking` com `type` `enabled` (omissão) ou `disabled`; o `kimi-k2.7-code` só aceita `enabled` e dá erro a `disabled`; o `kimi-k3` raciocina sempre, sem parâmetro `thinking`, e regula-se por `reasoning_effort` | confirmado na página | `https://platform.kimi.ai/docs/guide/use-kimi-k2-thinking-model` |
| Kimi: os tokens do `reasoning_content` contam para o `max_tokens`; a página recomenda `max_tokens` de 16000 ou mais e não definir `temperature` nos modelos com raciocínio | confirmado na página | idem |
| Kimi: o que o `kimi-for-coding` (a rota de produção, em `api.kimi.com/coding`) aceita para desligar ou regular o raciocínio | **por confirmar** — a página não nomeia este modelo; mede-se no banco (AOS-512) | — |
| Anthropic: nos modelos mais recentes o raciocínio é adaptativo (`thinking` com `type` `adaptive`) e a profundidade regula-se por `output_config.effort`; `type` `enabled` com `budget_tokens` é recusado com 400 nos modelos 4.7 e posteriores; em vários modelos o raciocínio está sempre ligado e `disabled` dá 400 | confirmado | `https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting`, `https://platform.claude.com/docs/en/build-with-claude/extended-thinking` |
| Anthropic: com raciocínio manual (`enabled`), `tool_choice` só pode ser `auto` ou `none` | confirmado | `https://platform.claude.com/docs/en/build-with-claude/thinking` («Thinking with tool use») |
| Anthropic: por omissão, nos modelos mais recentes, o texto do raciocínio vem **vazio** (`display` `omitted`) e só a assinatura vem preenchida | confirmado | idem («Controlling thinking display») |
| LiteLLM: o raciocínio liga-se por `reasoning_effort` ou por `thinking`; nos modelos Claude 4.6 e posteriores `reasoning_effort` é traduzido para raciocínio adaptativo mais `output_config.effort`; o nome do modelo leva o prefixo `anthropic/` | confirmado na documentação actual | `https://docs.litellm.ai/docs/providers/anthropic`, `https://docs.litellm.ai/docs/reasoning_content` |
| LiteLLM: a versão **fixada em produção** (1.96.2, pelo digest) faz essa tradução | **por confirmar** — a documentação descreve a versão corrente; mede-se atrás da imagem fixada | — |

### Decidido pelo dono

1. **D4 (2026-10-07) — o segundo modelo é o Claude, com o raciocínio ligado, pelo mesmo proxy.**
   Logo há pelo menos duas rotas com parâmetros diferentes.
2. **Diagnóstico de 2026-10-08:** o texto do protocolo passa a ser parte do perfil por modelo,
   e um perfil qualifica-se no banco de ensaio antes de ser ligado.
3. **D3 mantém-se:** nenhum parâmetro faz do raciocínio uma resposta.

### Objectivo

O perfil da rota (AOS-505) passa a poder declarar três coisas, todas opcionais: os
**parâmetros a enviar no pedido**, a **versão da projecção** a usar nessa rota, e a **classe de
estado** (`devolver`: `nunca`, `opcional`, `obrigatório`). Um perfil que não declare nenhuma dá
o pedido de hoje, byte a byte.

### Critérios de Aceitação

- [ ] **Inerte sem declaração.** Sem perfil, ou com um perfil que não declare parâmetros,
      versão nem classe, o corpo do pedido, o tail, o `prompt_hash`, o `turn.recorded`, a
      captura e o digest do perfil são byte a byte os de hoje. Nenhuma golden muda. A rota de
      produção fica assim até o dono assinar outro perfil.
- [ ] **Parâmetros do pedido em lista fechada.** O perfil declara parâmetros de um conjunto
      fechado e com tipo, validado no arranque (no mínimo `thinking`, `reasoning_effort` e
      `max_tokens`); um nome fora do conjunto ou um valor de tipo errado **recusa o arranque**
      (fail-closed). Não há «parâmetros livres».
- [ ] **Só do perfil assinado.** Os parâmetros vêm só da configuração assinada do nó. Nenhum
      caminho os aceita de um plano, de um manifesto de run, do corpo de um pedido HTTP, de
      conteúdo de um run nem de uma resposta do modelo. Teste por cada origem: o valor é
      ignorado ou recusado, e o pedido sai igual.
- [ ] **No manifesto do turno.** Os parâmetros enviados ficam em `ModelConfig.Params` do turno
      e entram no digest do perfil: mudar um parâmetro muda o digest. O replay reproduz o turno
      com os parâmetros com que correu, e uma captura antiga (sem parâmetros) reproduz como
      antes.
- [ ] **Versão da projecção por rota.** O perfil pode nomear uma versão **publicada** da
      projecção; vale para os turnos servidos por essa rota e prevalece sobre o interruptor do
      nó, que passa a ser a omissão. Uma versão desconhecida recusa o arranque. O texto do
      protocolo continua a ser função da versão: o perfil **escolhe** uma versão publicada, não
      transporta texto livre.
- [ ] **A versão fica presa ao run.** Um run que comece numa versão continua nela até ao fim,
      incluindo depois de uma retoma e depois de um failover para outra rota: a projecção de um
      run nunca muda a meio. Teste diferencial loop e replay igual.
- [ ] **Classe de estado declarada, sem efeito ainda.** `devolver` é lido, validado, entra no
      digest e fica no manifesto; neste ticket nada o consome (o AOS-515 é quem devolve).
      Omissão: `nunca`.
- [ ] **400 por parâmetro não aceite tem nome.** Uma resposta 4xx do provider num turno em que
      o perfil enviou parâmetros conta em métrica própria, em vocabulário fechado (rota e
      código), sem nenhum byte do corpo do erro. Em caso nenhum o nó retira o parâmetro e
      repete sozinho.
- [ ] **Qualificação no banco, antes de ligar.** Um perfil com parâmetros ou com versão própria
      só é assinado para produção depois de uma corrida do AOS-512 com esse perfil; o
      relatório leva o digest do perfil, e o runbook da rota exige a referência a esse
      relatório. O banco aceita um perfil candidato sem que ele exista em produção.
- [ ] **Medições obrigatórias no banco** (resolvem os dois «por confirmar» acima): (a) o que o
      `kimi-for-coding` responde a `thinking` com `type` `disabled` e a `reasoning_effort` —
      aceita, ignora ou dá 4xx —, e o efeito na taxa de resposta vazia e na de tool call em
      texto; (b) o que a imagem fixada do proxy envia ao fornecedor para cada parâmetro (atrás
      de um falso que regista o corpo recebido). Os resultados ficam na §5 do acompanhamento.
- [ ] Os valores dos parâmetros não aparecem em spans nem em logs além do nome e do digest; não
      são segredo, mas o perfil nunca transporta credenciais (teste: um campo com forma de
      chave recusa o arranque).
- [ ] Versão da porta do gateway: MINOR, aditiva; `CHANGELOG.md` e contrato da porta.
- [ ] Revisão adversarial independente antes da fusão.

### Fora de âmbito

- **Devolver estado ao provider**: AOS-515. Aqui a classe só é declarada.
- **Texto de protocolo livre no perfil.** Uma variante que ganhe no banco entra como versão de
  projecção publicada, com emenda ao ADR-036, em ticket próprio.
- Escolher os valores para a rota de produção: é decisão do dono, depois do banco.
- Mais de um modelo por nó com selecção automática, canary e disjuntor (fase A3).
- O perfil como artefacto do registo, assinado e versionado fora do código (fase A3).

### Estado

**ABERTO (2026-10-08).** Sem código.

---

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
| 1.1 | 2026-09-15 | AOS-394 e AOS-395: selos de governação do gateway sem run nem passo (achado do E2E em produção) | Equipa AOS |
| 1.2 | 2026-09-15 | AOS-394 implementado; AOS-397 aberto (retenção por run do metering) a partir da revisão adversarial | Equipa AOS |
| 1.3 | 2026-09-16 | AOS-395 implementado; AOS-399: o nó pede a posse exclusiva do caminho do audit de governação do gateway (residual da revisão do AOS-395, fechado) | Equipa AOS |
| 1.4 | 2026-09-21 | +AOS-421 (escada de tiers no nó): medido que NENHUM ficheiro não-teste preenche `RoutingConfig.Tiers` e que `AOS_MODEL_TIERS` não existe — o refino de roteamento, o scoring assinado e a recusa de arranque por lacuna de preço estão escritos e provados no módulo do GW, e nunca correm no binário do nó. Absorve DEF-280-NO e DEF-280-REGIAO, que são o mesmo trabalho. | Equipa AOS |
| 1.5 | 2026-10-03 | +AOS-490: o adaptador projecta o tail em mensagens nativas, com continuidade do raciocínio | Equipa AOS |
| 1.6 | 2026-10-03 | AOS-490 implementado: projecção nativa seleccionável (`AOS_MODEL_PROJECTION`), raciocínio capturado e não devolvido, tokens em cache lidos do wire; parâmetros de amostragem movidos para fora de âmbito; produção por verificar | Equipa AOS |
| 1.7 | 2026-10-04 | AOS-490: verificação em produção da v0.1.45 (projecção nativa aceite pelo provider, tokens em cache registados) | Equipa AOS |
| 1.8 | 2026-10-04 | +AOS-491: o motivo de paragem do modelo chega ao runtime, à captura e ao registo do turno (fase A0 da arquitectura-alvo da fronteira) | Equipa AOS |
| 1.9 | 2026-10-06 | +AOS-504 e +AOS-505 (fase A1, recuperação): projecção nativa 1.1.0 (fim de segmento inforjável e texto do protocolo reescrito, desligada por omissão) com o canário de medição da recusa do objectivo; e a rota sob governação (o proxy deixa de descartar parâmetros, o nome pedido é o do modelo real, o modelo servido é comparado por turno) | Equipa AOS |
| 2.0 | 2026-10-07 | AOS-505 implementado, desligado por omissão: medição local do que o proxy expõe (o `model` do corpo é o nome pedido; o modelo e o endpoint configurados vêm em cabeçalhos), perfil da rota em código, comparação por turno com `AOS_MODEL_ROUTE_GOVERNANCE` (`off`, `observe`, `enforce`), contrato da porta `1.4.0`, emenda ao ADR-036 §2.8; os passos de produção ficam por decisão do dono | Equipa AOS |
| 2.1 | 2026-10-07 | +AOS-506 (fase A1): o modelo escreve a tool call como texto — projecção nativa 1.2.0 e aviso constante na nova tentativa, as duas desligadas por omissão. AOS-504 em produção, ligado (v0.1.50); AOS-505 em produção, desligado | Equipa AOS |
| 2.2 | 2026-10-07 | AOS-506 implementado, desligado por omissão: `AOS_MODEL_PROJECTION_VERSION=1.2.0` e `AOS_RUN_RETRY_NOTICE`; emendas ao ADR-036 §2.4 e ao ADR-039 §2.7; sem versão nova de layout | Equipa AOS |
| 2.3 | 2026-10-07 | AOS-506 revisto (sem bloqueantes) e corrigido: a medição do hash recalcula sobre o Goal hospedado; o recuo de imagem diverge em silêncio (frase corrigida, ordem do recuo); duas linhas da 1.2.0 reescritas; resíduo nomeado sobre a versão de layout | Equipa AOS |
| 2.4 | 2026-10-07 | AOS-506 em produção e ligado (v0.1.51): verificação das séries `v0151a` (aviso sobre a 1.1.0) e `v0151b` (1.2.0 com aviso); critério de ligar cumprido para a combinação, com o desvio declarado; AOS-504 substituída em produção pela 1.2.0; AOS-505 continua desligado | Equipa AOS |
| 2.5 | 2026-10-07 | AOS-505 em produção em `observe`: série `v0151g` (20 planos, 60 turnos com o modelo servido igual ao esperado); `drop_params`, nome real, `enforce` e o cron do alerta por fazer. AOS-506: registo do plano de três passos, negado por taint no nó com `consumes` | Equipa AOS |
| 2.6 | 2026-10-07 | +AOS-507, +AOS-508 e +AOS-509 (fase A2, estado opaco do provider): a forma da resposta registada por turno, sem conteúdo (`AOS_MODEL_RESPONSE_SHAPE`, desligada por omissão); providers falsos de wire para CI e um gate opcional atrás da imagem do proxy; e a descodificação tolerante de `content` em partes de texto e de `arguments` em objecto, com os outros nomes do raciocínio lidos como raciocínio. Decisão D3 do dono registada no AOS-509: o raciocínio nunca é usado como resposta | Equipa AOS |
| 2.7 | 2026-10-07 | AOS-507, AOS-508 e AOS-509 implementados: ficha da forma em `response_shape` do `turn.recorded` (desligada por omissão, contrato da porta `1.5.0`); 87 casos de wire, linha de base e gate `ci-wire-live` com uma corrida contra a imagem de produção do proxy; descodificação tolerante e `aos_model_response_rejected_total` (contrato `1.6.0`). Produção por verificar | Equipa AOS |
| 2.8 | 2026-10-08 | AOS-507, AOS-508 e AOS-509 revistos (sem bloqueantes) e corrigidos: o digest da ficha não cobre chaves escritas pelo modelo; raciocínio presente e vazio dá `vazio` (342 séries); chaves repetidas lêem-se como na base e `type` repetido recusa; segunda corrida do `ci-wire-live` — o proxy move `refusal`, `thinking` e `reasoning_details` para `provider_specific_fields`, e a ficha lê-os lá (`psf_refusal`, `psf_reasoning`); corpos entregues pelo proxy congelados como casos; medição das capturas seladas dos três `empty_output` registada no AOS-507 | Equipa AOS |
| 2.9 | 2026-10-08 | +AOS-513 (fase A2): o perfil da rota passa a poder declarar parâmetros do pedido em lista fechada, a versão da projecção por rota e a classe de estado; inerte sem perfil que os declare; regras dos fornecedores consultadas a 2026-10-08 e citadas no ticket | Equipa AOS |
