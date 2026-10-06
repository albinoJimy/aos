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

**IMPLEMENTADO (2026-10-06), desligado por omissão; por rever e por medir em produção.** A 1.1.0
não está seleccionada em lado nenhum: `AOS_MODEL_PROJECTION_VERSION` ausente é a 1.0.0.

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
      arranque; com a governação ligada, um modelo sem perfil também.
- [x] Com o interruptor em `off`, o nó é byte a byte o anterior, provado contra goldens medidos na
      base do ticket (`TestAOS505_No_Off_SaoOsBytesDaBase`): corpos dos pedidos, `turn.recorded`,
      tipos e ordem dos eventos, parte em claro das capturas, famílias do `/metrics`. **Sem
      diferença declarada:** o campo do perfil fica ausente em `off`.
- [x] Métricas no `/metrics` do nó: `aos_model_route_checks_total{result,served}`, com `result`
      em `igual`, `diferente`, `nao_reportado`. O modelo servido só entra no rótulo se for um dos
      modelos esperados dos perfis; outro texto conta como `outro`. A regra de alerta sobre
      `diferente` maior do que zero é o `deploy/server/alerta-rota.sh`.
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
- [ ] Revisão adversarial independente com mutações, antes da fusão.
- [ ] Verificação em produção, em `observe`: numa série de pelo menos 20 planos, todos os turnos
      têm o modelo servido reportado e igual ao esperado (`diferente` e `nao_reportado` a zero),
      e a taxa de planos falhados não sobe em relação à série anterior. A passagem a `enforce` é
      decisão do dono, com estes números.

### Fora de âmbito

- Enviar `tool_choice`, parâmetros de amostragem ou `max_tokens`: este ticket só torna visível um
  descarte; a capacidade declarada por rota vem depois.
- O perfil do modelo como artefacto assinado do registo e a entrada automática de modelos (fase
  A3).
- Assinar o `config.yaml` do proxy.
- Mais de um modelo por nó.

### O que isto detecta, e o que não detecta

- **Detecta** uma troca de **configuração no proxy**: outro modelo por baixo do mesmo nome
  pedido, ou outro endpoint (este só com `AOS_MODEL_ROUTE_API_HOST` definida).
- **Não detecta** uma troca feita pelo **provider** por trás do mesmo nome e do mesmo endpoint:
  os cabeçalhos dizem o que o proxy está configurado para pedir, não o que o provider serviu.
- **Os cabeçalhos não são atestação.** São emitidos pelo proxy sem prova de origem, e valem
  enquanto o canal entre o nó e o proxy for de confiança.
- **O streaming e os embeddings** não são comparados.

### Passos de produção — por decisão do dono

Runbook em `deploy/server/README.md`, «Rota do modelo sob governação». Nenhum é feito pelo deploy.

1. `drop_params: false` no `config.yaml` do servidor, com um plano de verificação antes e outro
   depois. Rollback de uma linha.
2. `AOS_MODEL_ROUTE_GOVERNANCE=observe` e `AOS_MODEL_ROUTE_API_HOST`, o cron do `alerta-rota.sh`, e
   a série de pelo menos 20 planos.
3. A troca do nome pedido pelo nome real: o proxy serve os dois nomes, a allowlist é re-assinada
   com os dois, o nó passa a pedir o novo, e só então o antigo sai. **Exige a chave custodiada da
   allowlist** (ou um bundle externo assinado pelo operador).
4. A passagem a `enforce`.

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

**Por fazer.** A revisão adversarial independente; os passos de produção, por decisão do dono; e
confirmar contra o provider real o que ele devolve sobre si próprio.

### Estado

**IMPLEMENTADO (2026-10-07), desligado por omissão; por rever e por verificar em produção.**
`AOS_MODEL_ROUTE_GOVERNANCE` ausente é `off`. Os passos de produção (o `config.yaml` do servidor,
`observe`, a troca do nome pedido e `enforce`) ligam-se por decisão do dono.

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
