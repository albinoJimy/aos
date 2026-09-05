# EPIC-05 — Skill/Tool Registry e Supply-chain

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Skill/Tool Registry e Supply-chain |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md`, `specs/EPIC-07_Seguranca_Isolamento.md`, `specs/EPIC-11_Testes_Qualidade.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

As tools são a superfície de ataque mais subestimada de um Agentic OS. Uma tool MCP adicionada "sem reiniciar" pode mutar o seu schema no Dia 7 e passar a reencaminhar credenciais — o cenário *rug-pull*. Este epic constrói o **Skill/Tool Registry (REG)** e a disciplina de **supply-chain** que tornam esta classe de falha *arquitecturalmente impossível* e não apenas politicamente desencorajada.

O REG é o serviço de plataforma que concretiza o Princípio 8 da fonte — *coerência por contrato, não por lock-in* — transformando cada capacidade que um agente pode invocar numa entrada versionada, com hash e assinatura. Nenhuma capacidade não-verificada atravessa o Reference Monitor (RM). O epic entrega o catálogo append-only versionado (AOS-045), a integração dos três transportes MCP (AOS-046), a tríade de integridade *pin + hash + assinatura* (AOS-047, AOS-048), o modelo TOFU com detecção de mudança de schema (AOS-049), o congelamento do *tool set* por run e a sua revalidação criptográfica por chamada (AOS-050, AOS-051), o versionamento SemVer (AOS-052), o ciclo de publicação/promoção de skills com eval-gate (AOS-053) e a suite de testes de supply-chain que prova o conjunto (AOS-054).

O epic serve directamente dois ADRs canónicos: **ADR-012** (SemVer + eval-gate para auto-modificação) — o versionamento, o pinning e a promoção estagiada — e **ADR-005** (separação control/data-plane + taint) — o tratamento de schemas e descrições MCP como conteúdo untrusted. Apoia-se ainda em ADR-002 (default-deny no RM: só executam tools no catálogo), ADR-009 (tool set congelado integra o prefixo imutável do prompt) e ADR-006 (scopes de credencial declarados no contrato, injectados server-side pelo broker).

A maioria do trabalho pertence à **Fase 1 — Fronteira de segurança** (supply-chain com pin+hash+assinatura), com os tickets de versionamento e promoção de skills a caírem na **Fase 4 — UX e evolução** (SemVer + eval-gate para auto-modificação). Componente-alvo primário: **REG**. Consumidores: **RT** (resolução por run) e **RM** (revalidação por chamada).

---

## 2. Critérios de Saída do Epic

> **QUALIFICAÇÃO DE COMPOSIÇÃO (AOS-326, 2026-09-05).** Como no EPIC-04, os critérios abaixo são
> propriedades **do sistema** e hoje são satisfeitos ao nível da **biblioteca**. O que o nó `aos`
> COMPÕE do REG é o congelamento de tool set por run e a **revalidação por chamada** na cadeia do
> Reference Monitor — e esses correm. O que NÃO compõe é o **catálogo event-sourced**
> (`bootstrap.go` usa `emptyCatalog{}`), o **host MCP** (`mcp.NewHost` sem chamador) e o **TOFU**
> (`tofu.NewMonitor` sem chamador); o trust store do revalidador de referência nasce vazio.
>
> Em consequência, lidos como propriedades do nó, são **falsos hoje**: o primeiro critério («o REG
> está operacional como catálogo append-only e versionado»), o segundo (os três transportes MCP
> integrados) e o quinto (TOFU activo). Um tool set vazio é default-deny — o nó não executa tools,
> não as executa mal.
>
> Não existe ADR que declare o REG deliberadamente fora do grafo de build. Eixo: **DEF-812**. O nó
> declara-o no arranque.


- [ ] O REG está operacional como catálogo **append-only e versionado** de skills, tools e servidores MCP, com os campos essenciais (`id`, `version`, `digest`, `signature`, `contract`, `provenance`, `status`).
- [ ] Os três transportes MCP — **STDIO, SSE e Streamable HTTP** — estão integrados, cada um com o seu enquadramento de confiança e isolamento (STDIO em microVM; remotos sob egress allowlist + TLS).
- [ ] Nenhum artefacto é resolvido por referência flutuante (`latest`/`main`); a resolução é **sempre por versão SemVer exacta + digest pinado**.
- [ ] Todo o artefacto admitido tem **assinatura verificada** sobre `(id, version, digest)` contra chave de confiança antes de entrar no catálogo (anti rug-pull, ADR-012).
- [ ] O modelo **TOFU com detecção de mudança de schema** está activo: `first_seen` → `pinned`; qualquer divergência posterior classifica-se `changed` e é tratada como incidente, exigindo re-aprovação (ADR-005).
- [ ] O **tool set é congelado por run** e materializado no prefixo imutável do prompt; novas tools só entram em runs novos (ADR-009).
- [ ] O RM executa **revalidação criptográfica do digest a cada tool call**; divergência bloqueia a execução e alerta.
- [ ] Todo o artefacto comportamental segue **SemVer** ancorado a contrato público, com manifesto de dependências imutável por trajectória e rollback atómico.
- [ ] O ciclo de **publicação/promoção de skills** (staging → verificação/eval-gate → active → deprecated/revoked) está implementado; nenhum artefacto salta directamente para `active`.
- [ ] Existe uma **suite de testes de supply-chain** que reproduz rug-pull, schema drift, tool poisoning e resolução por `latest`, e prova que cada um é bloqueado.
- [ ] Cada decisão de admissão/bloqueio é registada no **audit hash-chain + WORM** com `id`, `version`, `digest` e resultado (cruza com `specs/EPIC-08_Observabilidade_Evals.md`).

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-045 | Skill/Tool Registry (catálogo versionado) | feature | L | P0 | EPIC-01 (ES, RM) |
| AOS-046 | Integração MCP (STDIO / SSE / Streamable HTTP) | feature | L | P0 | AOS-045 |
| AOS-047 | Pin + hash de definições de tool | feature | M | P0 | AOS-045 |
| AOS-048 | Assinatura e verificação (anti rug-pull) [ADR-012] | feature | M | P0 | AOS-047 |
| AOS-049 | TOFU com detecção de mudança de schema [ADR-005] | feature | M | P0 | AOS-046, AOS-047 |
| AOS-050 | Congelamento de tool set por run | feature | M | P0 | AOS-045, AOS-047 |
| AOS-051 | Revalidação criptográfica por chamada | feature | M | P0 | AOS-047, AOS-048, AOS-050 |
| AOS-052 | SemVer de skills/tools | feature | S | P1 | AOS-045 |
| AOS-053 | Publicação/promoção de skills | feature | M | P1 | AOS-052, AOS-048 |
| AOS-054 | Testes de supply-chain | chore | M | P0 | AOS-048, AOS-049, AOS-051, AOS-053 |

---

## AOS-045 — Skill/Tool Registry (catálogo versionado)

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | EPIC-01 (Event Store replicado, Reference Monitor) |
| Bloqueia | AOS-046, AOS-047, AOS-050, AOS-052 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§3), ADR-012, ADR-002 |

### Contexto

O REG é o serviço de plataforma que transforma "coerência por contrato" numa fronteira concreta. Sem um catálogo versionado append-only, as capacidades vivem dispersas em configuração mutável e nenhuma garantia de supply-chain é possível. Este ticket estabelece o modelo de dados e o serviço base sobre os quais todos os controlos seguintes (pin, hash, assinatura, TOFU, congelamento) assentam.

### Objectivo

Implementar o REG como catálogo **append-only e versionado** de três tipos de artefacto — **skill**, **tool** e **servidor MCP** — com entrada estruturada e ciclo de vida de admissão (`staging` → `active` → `deprecated`/`revoked`), consultável pelo RT (resolução por run) e pelo RM (revalidação por chamada).

### Critérios de Aceitação

- [ ] Cada entrada do REG expõe os campos essenciais: `id` + `version` (SemVer), `digest`, `signature`, `contract` (schema de I/O, scopes de credencial, classe de egress), `provenance` (origem, publicador, timestamp, estado de confiança) e `status`.
- [ ] O catálogo é **append-only**: uma versão nunca é editada in-place; alterações produzem novas versões.
- [ ] Os três tipos de artefacto (skill, tool, servidor MCP) são representáveis e distinguíveis.
- [ ] O ciclo de vida suporta os estados `staging`, `active`, `deprecated`, `revoked`; **nenhum artefacto salta directamente para `active`** sem passar por verificação (integração com AOS-047/048/053).
- [ ] Existe API de consulta que devolve uma entrada **por versão pinada** (nunca `latest`) para o RT, e outra que devolve o `digest` esperado para o RM.
- [ ] Uma capacidade **fora do catálogo** é recusada por default (default-deny, ADR-002).

### Detalhes Técnicos

- Componente-alvo: **REG**. Persistência sobre o Event Store replicado (ADR-007), não SQLite single-writer.
- Modelo de dados conforme `tecnica/05` §3 (tabela de campos essenciais e diagrama de ciclo de vida).
- API mínima: `publish(artefacto→staging)`, `resolve(id, version)→entrada pinada`, `getDigest(id, version)`, `setStatus(id, version, status)`.
- Canonicalização determinística do conteúdo (schema/binário/manifesto) para o cálculo de `digest` (a lógica de hashing é AOS-047; aqui reserva-se o campo e o ponto de extensão).

### Testes Requeridos

- Unit: criação, consulta por versão pinada, imutabilidade append-only (tentativa de editar versão existente falha).
- Integração: RT resolve tool set a partir do REG; RM obtém digest esperado; capacidade fora do catálogo é negada.
- Domínio: default-deny verificado — nenhuma tool ausente do REG é despachável.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Testes unitários e de integração verdes; cobertura não regride.
- [ ] Toda a consulta de resolução devolve versão pinada (nunca flutuante).
- [ ] Default-deny no RM para capacidades fora do catálogo, testado (ADR-002).
- [ ] Spans OTel GenAI nas operações de consulta do REG; sem segredos em logs/spans.
- [ ] Documentação e cross-ref para `tecnica/05` actualizadas; ADRs citados.

### Handoff para Claude Code

```text
És o executor do ticket AOS-045 (Skill/Tool Registry) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05_Skill_Tool_Registry_Supply_Chain.md
§3 e os ADR-012 e ADR-002. Implementa o REG como catálogo append-only e
versionado de skills, tools e servidores MCP.

Entrega:
- Modelo de dados com campos id+version (SemVer), digest, signature, contract,
  provenance, status, persistido sobre o Event Store replicado (não SQLite).
- Ciclo de vida staging→active→deprecated/revoked; nenhum artefacto salta para
  active sem verificação.
- API resolve(id,version) que devolve SEMPRE versão pinada (nunca latest) e
  getDigest para o RM.
- Default-deny: capacidade fora do catálogo é recusada no RM (ADR-002).

Reserva o ponto de extensão para hashing/assinatura (AOS-047/048) mas não os
implementes aqui. Testes: unit + integração + default-deny. Não expandas escopo.
Abre PR com o template da secção 7 do 01_Engineering_Standards_e_Handoff.md.
```

---

## AOS-046 — Integração MCP (STDIO / SSE / Streamable HTTP)

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-045 |
| Bloqueia | AOS-049 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§4), `specs/EPIC-07_Seguranca_Isolamento.md`, ADR-005, ADR-004 |

### Contexto

O Model Context Protocol (MCP) é o mecanismo pelo qual o AOS integra tools de terceiros sem os acoplar ao núcleo. Cada transporte tem um enquadramento de confiança e isolamento distinto. Sem uma camada de host que os trate uniformemente sob taint e egress allowlist, os servidores MCP tornam-se um vector directo de prompt injection e exfiltração.

### Objectivo

Implementar o AOS como **host MCP** capaz de integrar servidores por três transportes — **STDIO** (subprocesso local), **SSE** (streaming HTTP legado) e **Streamable HTTP** (transporte remoto recomendado) — registando cada servidor como artefacto no REG e tratando os seus schemas/descrições como **conteúdo untrusted**.

### Critérios de Aceitação

- [ ] **STDIO:** o servidor local corre como subprocesso **dentro do Sandbox Substrate (microVM)**, nunca com o socket do host; o binário é registado como artefacto do REG.
- [ ] **SSE:** suportado como transporte de transição, com **TLS obrigatório** e endpoint em **egress allowlist**.
- [ ] **Streamable HTTP:** suportado como transporte recomendado (request/response + streaming num único endpoint), com **TLS + autenticação do host** e suporte a sessões.
- [ ] Os schemas, descrições de tools e resources devolvidos por **qualquer** servidor MCP são marcados como **untrusted** e passam pelo pipeline de taint (ADR-005) — não podem comandar o planeador.
- [ ] Servidores remotos (SSE, Streamable HTTP) têm o endpoint sujeito a **egress allowlist**; servidores locais (STDIO) executam sempre em sandbox isolada (ADR-004).
- [ ] A descoberta de tools de um servidor produz entradas candidatas no REG em `staging` (nunca directamente `active`).

### Detalhes Técnicos

- Componente-alvo: **REG** + integração com **SBX** (microVM) e egress allowlist (ver `specs/EPIC-07`).
- Tabela de transportes conforme `tecnica/05` §4.
- Handshake MCP: `initialize`, `tools/list`, `resources/list`; o manifesto de capabilities devolvido alimenta o `contract` e o `digest` (hashing em AOS-047).
- Marcação de taint das descrições/schemas: cruza com o pipeline de `specs/EPIC-07_Seguranca_Isolamento.md`.

### Testes Requeridos

- Integração: ligação e `tools/list` funcional nos três transportes contra servidores de teste.
- Segurança: descrição de tool contendo texto do tipo "ignora instruções anteriores…" é tratada como dados inertes (taint), não comanda o planeador.
- Isolamento: STDIO corre em microVM sem socket do host; endpoint remoto fora da allowlist é bloqueado.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; três transportes demonstráveis.
- [ ] Schemas/descrições MCP marcados untrusted e testados contra tool poisoning (ADR-005).
- [ ] STDIO isolado em microVM; remotos sob TLS + egress allowlist (ADR-004).
- [ ] Spans OTel GenAI para descoberta/ligação MCP; sem segredos expostos.
- [ ] Testes de integração e de segurança verdes; cross-ref para `specs/EPIC-07` documentada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-046 (Integração MCP) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §4, specs/EPIC-07_Seguranca_Isolamento.md
e ADR-005/ADR-004. Implementa o AOS como host MCP com três transportes:
STDIO (subprocesso em microVM, sem socket do host), SSE (TLS + egress allowlist,
transição) e Streamable HTTP (TLS + auth do host, recomendado).

Regras não-negociáveis:
- Schemas e descrições devolvidos por servidores MCP são UNTRUSTED: passam pelo
  pipeline de taint, nunca comandam o planeador (ADR-005).
- Endpoints remotos só através de egress allowlist; STDIO sempre em sandbox.
- A descoberta produz entradas no REG em staging, nunca active.

Testes: integração nos 3 transportes + teste de tool poisoning + isolamento.
Não expandas escopo (o hashing/assinatura é AOS-047/048). Abre PR com o template.
```

---

## AOS-047 — Pin + hash de definições de tool

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-045 |
| Bloqueia | AOS-048, AOS-049, AOS-050, AOS-051 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§5), ADR-012, ADR-009 |

### Contexto

O primeiro pilar da defesa de supply-chain é eliminar a resolução flutuante. Um artefacto resolvido por `latest`/`main` pode ser substituído silenciosamente por upstream. O pin fixa a versão exacta; o hash prova que o conteúdo dessa versão não mudou entre a aprovação e a execução.

### Objectivo

Implementar o **pinning** (resolução obrigatória por versão SemVer exacta + `digest`) e o **hashing** (cálculo de digest SHA-256 sobre o conteúdo canonicalizado do artefacto — schema da tool, binário do servidor ou manifesto de capabilities), comparado no momento da resolução.

### Critérios de Aceitação

- [ ] A resolução por **`latest`, `main` ou qualquer referência flutuante é proibida** e rejeitada com erro explícito.
- [ ] O RT resolve sempre uma **versão SemVer exacta com `digest` associado**.
- [ ] O `digest` é um **SHA-256 do conteúdo canonicalizado** (schema/binário/manifesto), com canonicalização determinística e reproduzível.
- [ ] No momento da resolução, o **digest calculado é comparado com o digest esperado** no REG; divergência bloqueia a admissão do artefacto no run.
- [ ] O par `(version, digest)` é registado no manifesto de dependências da trajectória (base do replay fiel, cruza com AOS-052).

### Detalhes Técnicos

- Componente-alvo: **REG** (cálculo/armazenamento de digest) + **RT** (resolução pinada).
- Canonicalização: ordenação estável de chaves, normalização de whitespace/encoding, para que o mesmo schema produza sempre o mesmo digest.
- O digest é o campo `digest` da entrada REG (AOS-045); este ticket implementa o seu cálculo e comparação.
- Suporta a revalidação por chamada (AOS-051) e o congelamento por run (AOS-050) reutilizando o mesmo digest.

### Testes Requeridos

- Unit: canonicalização determinística (mesmo input → mesmo digest; mudança mínima → digest diferente).
- Domínio: resolução por `latest` é rejeitada; resolução por versão exacta com digest correcto passa; digest divergente é bloqueado.
- Integração: manifesto de dependências grava `(version, digest)` por run.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; `latest` proibido e testado.
- [ ] Hashing canonicalizado determinístico verificado.
- [ ] `(version, digest)` gravado no manifesto imutável por trajectória (ADR-012).
- [ ] Spans OTel na resolução; sem segredos.
- [ ] Testes verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-047 (Pin + hash de definições de tool) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §5, ADR-012 e ADR-009. Implementa:
- Pinning: resolução SEMPRE por versão SemVer exacta + digest; latest/main/
  referência flutuante são REJEITADOS com erro explícito.
- Hashing: SHA-256 do conteúdo canonicalizado (schema/binário/manifesto) com
  canonicalização determinística; comparação com o digest esperado no REG na
  resolução.
- Grava (version, digest) no manifesto de dependências da trajectória.

O digest calculado aqui é reutilizado pela revalidação por chamada (AOS-051) e
pelo congelamento por run (AOS-050) — desenha para reutilização, não os
implementes. Testes: determinismo do hash + rejeição de latest + bloqueio de
digest divergente. Abre PR com o template.
```

---

## AOS-048 — Assinatura e verificação (anti rug-pull) [ADR-012]

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-047 |
| Bloqueia | AOS-051, AOS-053, AOS-054 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§5), ADR-012, ADR-006 |

### Contexto

Pin e hash provam **integridade** — que o conteúdo não mudou — mas não **origem**. A assinatura autentica quem publicou o artefacto. É a peça que fecha a porta ao rug-pull: mesmo que um atacante calcule um novo hash, não consegue assiná-lo com a chave de confiança do publicador legítimo.

### Objectivo

Implementar a **assinatura** do tuplo `(id, version, digest)` pelo publicador e a sua **verificação** contra uma chave de confiança antes de qualquer artefacto ser admitido no catálogo, autenticando a *origem* além da *integridade*.

### Critérios de Aceitação

- [ ] Cada artefacto tem uma `signature` sobre o tuplo **`(id, version, digest)`**, verificável contra uma chave de confiança.
- [ ] O REG **recusa admitir** (não passa de `staging` a `active`) qualquer artefacto com assinatura ausente, inválida ou de chave não-confiável.
- [ ] Existe um **trust store** de chaves de publicadores confiáveis, gerível e auditável.
- [ ] A verificação de assinatura é pré-condição de promoção (integra o gate de admissão, cruza com AOS-053).
- [ ] Cada decisão de verificação (aceite/recusada) é registada no **audit hash-chain + WORM** com `id`, `version`, `digest` e resultado.
- [ ] Os scopes de credencial declarados no `contract` são os únicos que o broker aceitará conceder à tool (cruza com ADR-006; o agente nunca vê o segredo).

### Detalhes Técnicos

- Componente-alvo: **REG** (verificação na admissão) + integração com **RM** (recusa em runtime se a assinatura deixar de validar).
- Esquema de assinatura assimétrica (ex.: Ed25519/ECDSA); o trust store guarda chaves públicas dos publicadores.
- A assinatura cobre `(id, version, digest)` — depende do digest de AOS-047.
- Registo no audit WORM cruza com `specs/EPIC-08_Observabilidade_Evals.md`.

### Testes Requeridos

- Unit: assinatura válida verifica; assinatura adulterada, digest trocado ou chave desconhecida falham.
- Domínio (rug-pull): artefacto com conteúdo alterado e re-hasheado mas sem assinatura válida do publicador legítimo é bloqueado.
- Integração: decisão de verificação aparece no audit WORM com os campos exigidos.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; verificação de origem operacional.
- [ ] Trust store de chaves de publicador auditável.
- [ ] Artefacto sem assinatura válida não passa de `staging` a `active`.
- [ ] Decisões registadas no audit hash-chain + WORM (ADR-010).
- [ ] Sem segredos em código/logs/spans; credenciais só via Broker/Vault (ADR-006).
- [ ] Testes de rug-pull verdes.

### Handoff para Claude Code

```text
És o executor do ticket AOS-048 (Assinatura e verificação, anti rug-pull) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §5, ADR-012 e ADR-006. Implementa:
- Assinatura do tuplo (id, version, digest) pelo publicador (Ed25519/ECDSA).
- Trust store de chaves públicas confiáveis, auditável.
- Verificação como pré-condição de admissão: artefacto sem assinatura válida
  NUNCA passa de staging a active.
- Registo de cada decisão (aceite/recusada) no audit hash-chain + WORM com
  id, version, digest e resultado.
- Os scopes de credencial ficam no contract; nunca entregar segredo à tool em
  claro (broker JIT, ADR-006).

Testes: assinatura adulterada/digest trocado/chave desconhecida falham; teste
de rug-pull (conteúdo re-hasheado sem assinatura legítima é bloqueado). Abre PR.
```

---

## AOS-049 — TOFU com detecção de mudança de schema [ADR-005]

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-046, AOS-047 |
| Bloqueia | AOS-054 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§5), ADR-005, ADR-012 |

### Contexto

Nem todos os servidores MCP trazem uma cadeia de assinatura pré-estabelecida. O TOFU (*Trust On First Use*) permite registar a identidade e o schema de um servidor na primeira ligação e, a partir daí, tratar **qualquer** divergência como incidente. É o controlo que apanha o rug-pull do "Dia 7": o schema que muta silenciosamente depois de ganhar confiança.

### Objectivo

Implementar o modelo **TOFU com detecção de mudança de schema**: registar (`first_seen`) a identidade e o digest do manifesto de capabilities de um servidor na primeira ligação, ratificar para `pinned`, e classificar qualquer divergência posterior como `changed` — um incidente que bloqueia e exige re-aprovação explícita.

### Critérios de Aceitação

- [ ] Na primeira ligação a um servidor MCP, o AOS regista o estado **`first_seen`** com a identidade e o digest do manifesto de capabilities.
- [ ] O operador **ratifica** o estado, promovendo-o a **`pinned`**.
- [ ] Qualquer divergência posterior do digest (mudança de schema, tools diferentes) é classificada **`changed`** e **tratada como incidente de segurança**, não como actualização de rotina.
- [ ] Um estado `changed` **bloqueia** a utilização do artefacto até re-aprovação explícita, que exige uma **nova versão SemVer** (nunca aceite in-band).
- [ ] As descrições/schemas continuam a ser tratados como **untrusted** durante todo o processo (ADR-005).
- [ ] Cada transição de estado de confiança é registada no audit WORM.

### Detalhes Técnicos

- Componente-alvo: **REG** (campo `provenance` com estado de confiança) + **RM** (bloqueio em `changed`).
- Estados de confiança conforme `tecnica/05` §5: `first_seen` → `pinned` → (`changed`).
- Reutiliza o digest de AOS-047 e a integração MCP de AOS-046.
- Uma mudança `changed` dispara o mesmo fluxo de re-aprovação que uma versão MAJOR (cruza com AOS-052).

### Testes Requeridos

- Domínio (schema drift): servidor devolve manifesto idêntico → passa; servidor muta o schema → detectado como `changed` e bloqueado.
- Fluxo: `first_seen` → ratificação → `pinned`; re-aprovação exige nova versão SemVer.
- Segurança: schema alterado com texto injectado permanece untrusted e é bloqueado antes de qualquer efeito.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; ciclo `first_seen`→`pinned`→`changed` operacional.
- [ ] `changed` bloqueia e exige re-aprovação com nova versão SemVer (ADR-012).
- [ ] Schemas MCP untrusted durante todo o processo (ADR-005).
- [ ] Transições registadas no audit WORM.
- [ ] Testes de schema drift verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-049 (TOFU com detecção de mudança de schema) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §5, ADR-005 e ADR-012. Implementa
TOFU sobre servidores MCP:
- first_seen na primeira ligação (identidade + digest do manifesto de capabilities).
- Ratificação do operador → pinned.
- Qualquer divergência posterior → changed = incidente: BLOQUEIA e exige
  re-aprovação explícita com nova versão SemVer (nunca in-band).
- Schemas/descrições permanecem untrusted todo o tempo (ADR-005).
- Regista cada transição no audit WORM.

Reutiliza o digest de AOS-047 e a integração MCP de AOS-046. Testes: schema
drift detectado e bloqueado; fluxo first_seen→pinned; re-aprovação exige SemVer
novo. Não expandas escopo. Abre PR com o template.
```

---

## AOS-050 — Congelamento de tool set por run

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-045, AOS-047 |
| Bloqueia | AOS-051 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§6), ADR-009, ADR-012 |

### Contexto

O congelamento do tool set por run concilia integridade de supply-chain com estabilidade de cache. É a resolução da tensão da fonte *prefix caching vs tools dinâmicas*: o conjunto de tools é resolvido uma vez, no arranque do run, e materializado no prefixo imutável do prompt. Novas tools que surjam a meio não podem alterar a superfície nem partir o cache.

### Objectivo

Implementar a **resolução e o congelamento do tool set no arranque de cada run**: o RT resolve o conjunto completo de tools disponíveis a partir do REG (todas pinadas), materializa-as no **prefixo imutável do prompt**, e garante que esse conjunto permanece imutável durante toda a vida do run.

### Critérios de Aceitação

- [ ] No arranque de um run, o RT resolve o **conjunto completo de tools** a partir do REG, **todas por versão pinada** (AOS-047).
- [ ] O tool set (definições exactas + digests) é materializado no **prefixo imutável do prompt** e fica **congelado** durante todo o run (ADR-009).
- [ ] **Novas tools/servidores adicionados, actualizados ou re-aprovados no REG não alteram runs em curso** — só são visíveis a partir do próximo run.
- [ ] O tool set congelado (versões + digests) é gravado no **manifesto de dependências da trajectória**.
- [ ] O prefixo imutável não é reordenado nem mutado durante o run (preserva cache-hit-rate como SLI).

### Detalhes Técnicos

- Componente-alvo: **RT** + **REG**; integra o layout de prompt cache-estável de ADR-009 (ver `specs/EPIC-06_Model_Gateway_Custos.md` para o gateway e `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md` para o loop).
- O conjunto congelado é a entrada da revalidação por chamada (AOS-051): a expectativa contra a qual cada digest é verificado.
- Zona do prompt: prefixo imutável (system + tool set congelado) vs tail append-only, conforme tabela de `tecnica/05` §6.

### Testes Requeridos

- Domínio: adicionar/actualizar uma tool no REG a meio de um run não altera o tool set desse run; o run seguinte vê-a.
- Integração: tool set congelado gravado no manifesto de dependências.
- Cache: o prefixo imutável mantém-se byte-idêntico ao longo do run (verificar cache-hit-rate/estabilidade).

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; congelamento por run operacional.
- [ ] Novas tools só entram em runs novos, testado (ADR-009).
- [ ] Tool set congelado gravado no manifesto imutável por trajectória.
- [ ] Prefixo imutável estável verificado (SLI de cache-hit-rate não regride).
- [ ] Spans OTel na resolução do tool set; sem segredos.
- [ ] Testes verdes; cobertura não regride.

### Handoff para Claude Code

```text
És o executor do ticket AOS-050 (Congelamento de tool set por run) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §6, ADR-009 e ADR-012. Implementa:
- No arranque de cada run, o RT resolve o tool set completo a partir do REG,
  todas as tools por versão pinada (AOS-047).
- Materializa o conjunto (definições + digests) no PREFIXO IMUTÁVEL do prompt;
  fica congelado durante todo o run.
- Novas tools/servidores no REG NÃO alteram runs em curso — só o próximo run
  os vê (serve pinning de supply-chain E estabilidade de prefix cache).
- Grava o tool set congelado no manifesto de dependências da trajectória.

O conjunto congelado é a expectativa da revalidação por chamada (AOS-051).
Testes: mudança no REG a meio do run não afecta o run; prefixo imutável estável.
Não expandas escopo. Abre PR com o template.
```

---

## AOS-051 — Revalidação criptográfica por chamada

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-047, AOS-048, AOS-050 |
| Bloqueia | AOS-054 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§5, §6), ADR-002, ADR-012 |

### Contexto

O congelamento é a *expectativa*; a revalidação é a *verificação*. Ainda que o tool set esteja congelado no prompt, um servidor MCP pode mutar o seu schema em backing store a meio do run. A revalidação criptográfica por chamada é a última linha que apanha esse drift no exacto momento antes da execução — fechando definitivamente a janela do rug-pull.

### Objectivo

Implementar no **Reference Monitor** a **revalidação do digest de cada tool no momento da chamada**: recalcular/consultar o digest da definição em backing store e compará-lo com o digest congelado no run; qualquer divergência bloqueia a execução, alerta e coloca o artefacto em quarentena.

### Critérios de Aceitação

- [ ] A cada tool call, o RM **revalida o digest** da definição prestes a executar contra o **digest congelado** no run (AOS-050).
- [ ] Divergência (schema drift, rug-pull a meio do run) **bloqueia a execução**, emite alerta e coloca o artefacto em **quarentena**.
- [ ] A **assinatura** sobre `(id, version, digest)` é revalidada (AOS-048); assinatura inválida bloqueia.
- [ ] Os **scopes e a classe de egress** do `contract` são verificados dentro do permitido antes do despacho (cruza com ADR-006 e egress allowlist de `specs/EPIC-07`).
- [ ] Cada decisão (despacho ou bloqueio) é registada no **audit hash-chain + WORM** com `id`, `version`, `digest` e resultado.
- [ ] O overhead da revalidação mantém-se dentro do alvo de mediação do RM (p95 < 15 ms, ADR-002).

### Detalhes Técnicos

- Componente-alvo: **RM**; sequência conforme diagrama de `tecnica/05` §5 (LOOKUP → digest → assinatura → scope → EXEC → AUDIT).
- Reutiliza hash (AOS-047), assinatura (AOS-048) e o conjunto congelado (AOS-050).
- Verificação de scope/egress cruza com `specs/EPIC-07_Seguranca_Isolamento.md`.
- Digest cacheável em memória para respeitar o orçamento de latência (ADR-002), invalidando em mudança detectada.

### Testes Requeridos

- Domínio (rug-pull a meio do run): definição em backing store diverge do congelado → bloqueio + quarentena + alerta.
- Segurança: assinatura inválida bloqueia; scope fora do contrato bloqueia.
- Desempenho: overhead de revalidação p95 < 15 ms.
- Integração: decisão registada no audit WORM com os campos exigidos.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; revalidação por chamada operacional.
- [ ] Toda a tool call mediada pelo RM; nenhuma execução directa (ADR-002).
- [ ] Bloqueio + quarentena + alerta em divergência de digest/assinatura/scope.
- [ ] Decisões no audit hash-chain + WORM (ADR-010).
- [ ] Overhead p95 < 15 ms verificado.
- [ ] Testes de rug-pull e de desempenho verdes.

### Handoff para Claude Code

```text
És o executor do ticket AOS-051 (Revalidação criptográfica por chamada) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §5-§6, ADR-002 e ADR-012. Implementa
no Reference Monitor a revalidação por tool call:
1. Consulta a entrada pinada no REG.
2. Recalcula/consulta o digest actual e compara com o digest congelado do run.
3. Revalida a assinatura sobre (id, version, digest).
4. Verifica scopes e classe de egress dentro do contract.
Se qualquer passo falhar: BLOQUEIA, coloca o artefacto em quarentena, alerta.
Regista sempre a decisão no audit hash-chain + WORM (id, version, digest, resultado).

Respeita o orçamento de latência do RM (p95 < 15 ms) — usa cache em memória do
digest, invalidando em mudança. Testes: rug-pull a meio do run bloqueado;
assinatura/scope inválidos bloqueiam; desempenho p95. Abre PR com o template.
```

---

## AOS-052 — SemVer de skills/tools

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-045 |
| Bloqueia | AOS-053 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§7), ADR-012, `specs/EPIC-11_Testes_Qualidade.md` |

### Contexto

SemVer ancorado a contrato público é o que torna um *swap* de tool ou servidor um evento de variância **explícito**, nunca silencioso. Sem disciplina de versionamento, o manifesto de dependências por trajectória perde significado e o replay fiel torna-se impossível.

### Objectivo

Aplicar **SemVer** (`MAJOR.MINOR.PATCH`) a todo o artefacto comportamental mutável do REG — skills, schemas de tools, manifestos de servidores MCP — com semântica de contrato bem definida, manifesto de dependências imutável por trajectória, ciclo de deprecação formal e rollback atómico.

### Critérios de Aceitação

- [ ] Cada artefacto tem versão **`MAJOR.MINOR.PATCH`** ancorada a um contrato público.
- [ ] **MAJOR** = mudança incompatível de contrato (schema de I/O alterado, scopes acrescentados, semântica quebrada) — exige re-aprovação e é a única classe que justifica novo estado de confiança TOFU (cruza com AOS-049).
- [ ] **MINOR** = capacidade retro-compatível; **PATCH** = correcção sem alteração de contrato.
- [ ] O **manifesto de dependências** grava, por trajectória, as versões e digests exactos de todas as skills/tools/servidores usados, junto do model-id e do hash do prompt.
- [ ] O ciclo de **deprecação é formal**: uma versão passa por `deprecated` antes de qualquer retirada e nunca é removida enquanto houver trajectórias que a referenciem.
- [ ] O **rollback para uma versão anterior é atómico**.

### Detalhes Técnicos

- Componente-alvo: **REG**; regras de SemVer conforme `tecnica/05` §7 e `01_Engineering_Standards_e_Handoff.md` §5.
- O manifesto imutável cruza com `specs/EPIC-08_Observabilidade_Evals.md` (replay) e `tecnica/11`.
- Validação de bump de versão: uma mudança de contrato detectada que não incremente MAJOR é rejeitada.

### Testes Requeridos

- Unit: classificação de mudança → bump correcto (contrato quebrado exige MAJOR).
- Domínio: manifesto de dependências grava versões+digests por trajectória; rollback atómico restaura versão anterior.
- Integração: versão `deprecated` não é removível enquanto referenciada.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; semântica SemVer aplicada.
- [ ] Manifesto de dependências imutável por trajectória (ADR-012).
- [ ] Deprecação formal e rollback atómico operacionais.
- [ ] Testes verdes; cross-ref para `specs/EPIC-11` documentada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-052 (SemVer de skills/tools) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §7, ADR-012 e §5 do
01_Engineering_Standards_e_Handoff.md. Aplica SemVer MAJOR.MINOR.PATCH a
skills, schemas de tools e manifestos de servidores MCP:
- MAJOR = contrato quebrado (I/O, scopes, semântica) → re-aprovação + novo
  estado de confiança TOFU. MINOR = retro-compatível. PATCH = correcção.
- Manifesto de dependências imutável por trajectória (versões+digests+model-id+
  hash do prompt).
- Deprecação formal (deprecated antes de retirar; nunca remover se referenciada)
  e rollback atómico.

Valida que uma mudança de contrato sem bump MAJOR é rejeitada. Testes: bump
correcto, manifesto por trajectória, rollback atómico. Abre PR com o template.
```

---

## AOS-053 — Publicação/promoção de skills

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-052, AOS-048 |
| Bloqueia | AOS-054 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§3), ADR-012, `specs/EPIC-11_Testes_Qualidade.md` |

### Contexto

Uma skill auto-escrita é a mudança de maior risco do sistema — *misevolution*/drift ocorre mesmo sem atacante. O ciclo de publicação/promoção é o admission control que impede que um artefacto comportamental chegue a produção sem passar por eval-gate e ratificação. É a materialização, no REG, do fluxo de ADR-012.

### Objectivo

Implementar o **ciclo de publicação e promoção** de skills e tools no REG: `staging` → verificação de integridade (hash + assinatura + contrato) → eval-gate para skills auto-escritas → `active` (com SemVer) → `deprecated`/`revoked`, garantindo que **nenhum artefacto salta directamente para `active`**.

### Critérios de Aceitação

- [ ] A publicação coloca sempre o artefacto em **`staging`**, nunca directamente em `active`.
- [ ] A **verificação de integridade** (hash de AOS-047 + assinatura de AOS-048 + validação de contrato) é pré-condição de promoção; falha → rejeitado, não entra no catálogo.
- [ ] **Skills auto-escritas** passam por **eval-gate sobre golden-set + trace-diffing vs baseline** (ADR-012) antes de `active` (cruza com `specs/EPIC-11`).
- [ ] A promoção a **`active`** atribui uma **versão SemVer** (AOS-052) e disponibiliza rollback atómico.
- [ ] A **revogação de emergência** (`revoked`) bloqueia imediatamente o artefacto no RM.
- [ ] A promoção de skills auto-escritas exige **ratificação humana assinada** (não-repúdio), coerente com o fluxo staging → eval-gate → canary → ratificação → prod da fonte.
- [ ] Cada transição de estado é registada no audit WORM.

### Detalhes Técnicos

- Componente-alvo: **REG** + **GOV** (ratificação); fluxo conforme diagrama de `tecnica/05` §3 e do `_FONTE` (Dimensão 7).
- O eval-gate reutiliza o harness de `specs/EPIC-11_Testes_Qualidade.md`.
- Distinção clara entre tools/servidores de terceiros (verificação + TOFU) e skills auto-escritas (verificação + eval-gate + ratificação).

### Testes Requeridos

- Domínio: artefacto publicado nunca aparece `active` sem verificação; skill que falha o eval-gate é rejeitada e não vai a prod.
- Governação: promoção de skill auto-escrita exige ratificação humana assinada; revogação bloqueia imediatamente no RM.
- Integração: transições registadas no audit WORM; SemVer atribuída na promoção.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; ciclo de promoção operacional.
- [ ] Nenhum artefacto salta para `active` sem verificação; eval-gate verde para skills comportamentais (ADR-012).
- [ ] Ratificação humana assinada e revogação de emergência funcionais.
- [ ] Transições no audit hash-chain + WORM.
- [ ] Testes verdes; cross-ref para `specs/EPIC-11` documentada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-053 (Publicação/promoção de skills) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §3, o _FONTE (Dimensão 7), ADR-012 e
specs/EPIC-11_Testes_Qualidade.md. Implementa no REG o ciclo:
staging → verificação (hash+assinatura+contrato) → eval-gate (só skills
auto-escritas, golden-set + trace-diffing vs baseline) → active (com SemVer) →
deprecated/revoked.

Regras não-negociáveis:
- Nenhum artefacto salta para active sem verificação.
- Skill auto-escrita que falha o eval-gate é rejeitada, não vai a prod.
- Promoção de skill auto-escrita exige ratificação humana assinada (não-repúdio).
- revoked bloqueia imediatamente no RM. Regista transições no audit WORM.

Reutiliza o harness de eval de EPIC-11. Testes: verificação obrigatória,
eval-gate bloqueante, ratificação assinada, revogação imediata. Abre PR.
```

---

## AOS-054 — Testes de supply-chain

| Campo | Valor |
|---|---|
| Epic | EPIC-05 — Skill/Tool Registry e Supply-chain |
| Fase | 1 — Fronteira de segurança |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-048, AOS-049, AOS-051, AOS-053 |
| Bloqueia | — |
| Responsável sugerido | QA |
| Documentos de referência | `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` (§9), `specs/EPIC-11_Testes_Qualidade.md`, ADR-012, ADR-005 |

### Contexto

Uma defesa de supply-chain só existe se for provada por adversário. Este ticket transforma cada risco da tabela de `tecnica/05` §9 num teste executável que reproduz o ataque e verifica o bloqueio, integrando-os no pipeline fail-closed como gate de merge.

### Objectivo

Construir uma **suite de testes de supply-chain** que reproduz de forma controlada os vectores de ataque ao REG — rug-pull, schema drift, tool poisoning, resolução por `latest`, capacidade fora do catálogo e replay infiel — e prova que cada um é detectado e bloqueado, executando como gate de CI/CD.

### Critérios de Aceitação

- [ ] **Rug-pull:** teste que altera o conteúdo de uma tool e o re-hasheia sem assinatura legítima → provado bloqueado (AOS-048).
- [ ] **Schema drift:** teste em que um servidor MCP muta o schema após `pinned` → classificado `changed` e bloqueado (AOS-049).
- [ ] **Rug-pull a meio do run:** definição em backing store diverge do congelado → revalidação por chamada bloqueia + quarentena (AOS-051).
- [ ] **Tool poisoning:** descrição MCP com instrução injectada permanece untrusted e não comanda o planeador (ADR-005).
- [ ] **Resolução por `latest`/referência flutuante:** rejeitada (AOS-047).
- [ ] **Capacidade fora do catálogo:** recusada por default-deny no RM (ADR-002).
- [ ] **Replay infiel:** teste que prova que o manifesto de dependências por trajectória permite reproduzir o passado apesar de evolução posterior de tool (ADR-012).
- [ ] A suite corre no pipeline como **gate fail-closed**; qualquer falha bloqueia o merge.

### Detalhes Técnicos

- Componente-alvo: **REG** (testes end-to-end sobre os controlos dos tickets anteriores).
- Cobre a tabela de riscos de `tecnica/05` §9; integra-se no harness de `specs/EPIC-11_Testes_Qualidade.md`.
- Servidores MCP de teste (fixtures) que simulam mutação de schema e injecção em descrições.
- Verificação de que cada bloqueio produz o registo esperado no audit WORM.

### Testes Requeridos

- Este ticket **é** a suite de testes; cobre os sete vectores acima mais o registo em audit WORM de cada bloqueio.
- Regressão: a suite entra no conjunto de gates de merge de CI/CD (fail-closed).

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; os sete vectores reproduzidos e bloqueados.
- [ ] Suite integrada no pipeline como gate fail-closed (bloqueia merge).
- [ ] Cada bloqueio verificado contra o audit hash-chain + WORM.
- [ ] Cross-ref para `specs/EPIC-11` e `tecnica/05` §9 documentada.
- [ ] Sem segredos nos fixtures; cobertura dos controlos de supply-chain reportada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-054 (Testes de supply-chain) do AOS.

Lê specs/EPIC-05 (este ticket), tecnica/05 §9, specs/EPIC-11_Testes_Qualidade.md,
ADR-012, ADR-005 e ADR-002. Constrói a suite adversarial de supply-chain que
reproduz e prova o bloqueio de:
- Rug-pull (conteúdo re-hasheado sem assinatura legítima).
- Schema drift (servidor MCP muta schema após pinned → changed → bloqueado).
- Rug-pull a meio do run (revalidação por chamada bloqueia + quarentena).
- Tool poisoning (descrição untrusted não comanda o planeador).
- Resolução por latest (rejeitada).
- Capacidade fora do catálogo (default-deny).
- Replay infiel (manifesto de dependências permite reproduzir o passado).

Usa fixtures de servidores MCP que simulam mutação/injecção. Verifica que cada
bloqueio aparece no audit WORM. Integra a suite como gate fail-closed de CI/CD.
Não expandas escopo. Abre PR com o template.
```

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
