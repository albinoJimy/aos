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
| Documentos de referência | `packages/platform/model-gateway/metering/cost/recorder.go`, `packages/platform/model-gateway/metering/cache_sli.go` |

### Contexto

O `cost.Recorder` mantém o cumulativo de custo por run em `runAggs`, um mapa `RunKey{RunID,Tenant} → *Amount` **sem remoção, expiração nem tecto**. Enquanto o nó enviava o run vazio, o ramo que o alimenta nunca corria e o mapa ficava vazio; com o AOS-394 o run real passa a chegar em cada chamada, pelo que o mapa ganha uma entrada por run e mantém-na durante toda a vida do processo — um nó que corre semanas acumula-as. O mesmo padrão existe no eixo de árvore (`treeAggs`, hoje sem `TreeID` preenchido), no `MemoryBurndownSink` (que memoriza cumulativos por run) e no `cache_sli`, que agrega por (run, tenant) e não está composto no nó.

Impacto medido por leitura de código: é **memória**, não decisões — no nó de referência nenhum consumidor lê o cumulativo por run (não há `budgetbridge` composto). O custo por chamada e o canal de AOS-259 não dependem deste agregado.

### Objectivo

A retenção por run do metering do gateway é limitada e a política fica declarada, sem alterar o custo por chamada nem o canal de custo.

### Critérios de Aceitação

- [ ] Teste que mede a retenção antes da correcção: N runs distintos deixam N entradas retidas no `cost.Recorder`.
- [ ] Política escolhida e implementada, com a decisão registada neste ticket: fim-de-run explícito, expiração por inactividade, tecto com despejo, ou não retenção quando ninguém lê o cumulativo. Verificar primeiro quem lê o cumulativo por run (burn-down de AOS-259/AOS-261, span, sinks).
- [ ] O mesmo eixo verificado no `MemoryBurndownSink` e no `cache_sli`; onde não se fechar, o limite fica declarado.
- [ ] `go test -race -count=1 ./...` no módulo do GW e `bash scripts/ci/routing.sh` verdes.

### Estado

**ABERTO.** Criado a 2026-09-15 a partir da revisão adversarial do AOS-394.

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
| 1.1 | 2026-09-15 | AOS-394 e AOS-395: selos de governação do gateway sem run nem passo (achado do E2E em produção) | Equipa AOS |
| 1.2 | 2026-09-15 | AOS-394 implementado; AOS-397 aberto (retenção por run do metering) a partir da revisão adversarial | Equipa AOS |
| 1.3 | 2026-09-16 | AOS-395 implementado; AOS-399: o nó pede a posse exclusiva do caminho do audit de governação do gateway (residual da revisão do AOS-395, fechado) | Equipa AOS |
