# EPIC-02 — Agent Runtime e Execução Durável

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Agent Runtime e Execução Durável |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-01_Fundacoes_Plano_Controlo.md`, `specs/EPIC-03_Orquestracao_Escalonamento.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/13_Modelo_Dados_Eventos.md` |

---

## 1. Visão do Epic

O **Agent Runtime (RT)** é o batimento cardíaco do AOS: o loop que monta o prompt, chama o modelo, despacha as tool calls e verifica o resultado. Mas um loop, por si só, é apenas o *chatbot com plugins* que o blueprint recusa. O que distingue o RT do AOS é executá-lo sobre uma das três fundações não-negociáveis do produto — a **execução durável ao nível do passo** (ADR-001): idempotência por passo com chave `f(run_id, step_id)`, checkpoint intra-iteração no Event Store, replay determinístico *resume-from-step*, e liveness por lease/heartbeat com *fencing tokens*, nunca por PID.

Este epic torna **arquitecturalmente impossível** o conjunto de falhas que colapsa os frameworks de 2025-2026: o `POST` não-idempotente que re-executa o efeito no retry; o worker "morto" cross-host que afinal está vivo e duplica trabalho; o gate humano (`waiting_on_human`) que a detecção de zumbis confunde com um worker pendurado; a saga sem compensação que deixa o mundo externo num estado inconsistente; e o replay infiel que invalida RCA e evals depois de o código evoluir. O RT trata o crash a meio como **normal, não excepção**.

O EPIC-02 entrega o loop durável e a sua máquina de estados de suspensão de primeira classe (`ready`, `running`, `waiting_on_tool`, `waiting_on_human`, `paused`, `complete`, `failed`, `compensating`, `killed`, `timed_out`), o contrato de idempotência e as *activities* que isolam efeitos externos, a integração (ou contrato próprio equivalente) com um engine de durable execution, o controlo bidireccional (steer/interrupt) que a Dimensão 6 exige, e o harness de testes que prova replay e idempotência de forma contínua. Assenta nas fundações do EPIC-01 (Reference Monitor mandatório, Event Store replicado, identidade por agente) e serve de substrato ao EPIC-03 (orquestração, escalonamento, admission control).

**Componentes-alvo:** RT (Agent Runtime), ES (Event Store). Fronteiras com RM (Reference Monitor), SCH (Escalonador) e OBS (Observabilidade).

---

## 2. Critérios de Saída do Epic

- [ ] O loop do RT (montar → chamar → despachar → verificar) corre sobre execução durável e grava cada turno no Event Store com hash do prompt materializado, model-id/params/seed e versões pinadas.
- [ ] Todo o efeito externo é executado como *activity* idempotente com chave `f(run_id, step_id)`; um teste que reexecuta o passo prova **zero efeitos duplicados** (ADR-001).
- [ ] Existe checkpoint intra-iteração no Event Store: uma iteração interrompida a meio retoma sem repetir passos já confirmados.
- [ ] O replay determinístico reproduz 100% dos passos de uma trajectória *resume-from-step*, com captura de todos os inputs não-determinísticos (alvo de fidelidade de replay: 100%).
- [ ] A máquina de estados durável está implementada com os dez estados canónicos e transições válidas; estados inválidos são rejeitados.
- [ ] A liveness é determinada por lease/heartbeat com TTL e *fencing tokens* monotónicos; nenhum caminho usa PID para decidir se um worker está vivo.
- [ ] `waiting_on_human` e `paused` são estados duráveis distintos que **não** disparam a detecção de zumbi enquanto aguardam legitimamente.
- [ ] As sagas de compensação revertem efeitos parciais de forma idempotente após `failed`.
- [ ] Está integrado um engine de durable execution (Temporal/Restate/DBOS) **ou** um contrato próprio explícito que satisfaz o mesmo conjunto de garantias, decidido por spike documentado (ADR).
- [ ] Existe o estado `paused` com canal de steer/interrupt fora-de-banda: qualquer superfície pausa no fim do turno, injecta correcção e retoma (ADR-013).
- [ ] O harness de testes de replay/idempotência corre em CI como gate fail-closed (cruza os gates 3, 8 de `01_Engineering_Standards_e_Handoff.md`).
- [ ] Toda a tool call do RT atravessa o Reference Monitor; nenhuma execução externa fora do gate (ADR-002).
- [ ] Spans OTel GenAI (`invoke_agent`/`execute_tool`/`chat`) emitidos com `gen_ai.usage.*` e custo por span (ADR-010).

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-013 | Loop do agente (montar → chamar → despachar → verificar) | feature | M | P0 | AOS-003 (RM), AOS-002 (ES) |
| AOS-014 | Contrato de execução durável: idempotency key = f(run_id, step_id) | feature | M | P0 | AOS-013, AOS-002 |
| AOS-015 | Checkpoint intra-iteração no Event Store | feature | M | P0 | AOS-013, AOS-014 |
| AOS-016 | Replay determinístico resume-from-step | feature | M | P0 | AOS-015 |
| AOS-017 | Máquina de estados durável | feature | M | P0 | AOS-013, AOS-015 |
| AOS-018 | Liveness por lease/heartbeat + fencing tokens | feature | M | P0 | AOS-017, AOS-002 |
| AOS-019 | `waiting_on_human` sem colidir com detecção de zumbi | feature | S | P1 | AOS-017, AOS-018 |
| AOS-020 | Sagas de compensação | feature | M | P1 | AOS-014, AOS-017 |
| AOS-021 | Activities: isolamento de efeitos externos | feature | M | P0 | AOS-013, AOS-014, AOS-002 |
| AOS-022 | Integração com engine de durable execution ou contrato próprio | spike | L | P1 | AOS-014, AOS-015, AOS-016 |
| AOS-023 | Estado `paused` + canal de steer/interrupt | feature | M | P2 | AOS-017 |
| AOS-024 | Harness de testes de replay/idempotência | chore | M | P1 | AOS-014, AOS-016 |
| AOS-396 | Manifesto do turno pina o modelo que respondeu (model_id vazio no nó) | fix | M | P1 | AOS-013, AOS-016 |
| AOS-411 | A re-varredura de órfãos exclui os runs vivos antes de os reconstituir | fix | S | P2 | AOS-253 |
| AOS-419 | As esperas não-humanas não tinham prazo nenhum (backstop de wall-clock) | fix | S | P2 | AOS-017, AOS-252 |

> **Notas de dependência.** Os tickets `AOS-003` (Reference Monitor) e `AOS-002` (Event Store replicado) pertencem ao `specs/EPIC-01_Fundacoes_Plano_Controlo.md` e devem estar `Done` antes do arranque efectivo de AOS-013. AOS-018 partilha o contrato de lease/fencing com o Escalonador (`specs/EPIC-03_Orquestracao_Escalonamento.md`); coordenar para não duplicar a implementação do token monotónico.

---

## AOS-013 — Loop do agente (montar → chamar → despachar → verificar)

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-003 (Reference Monitor), AOS-002 (Event Store replicado) |
| Bloqueia | AOS-014, AOS-015, AOS-017, AOS-021 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Fluxo de execução), ADR-001, ADR-002, ADR-009, ADR-010 |

### Contexto
O RT é o núcleo do plano de execução (ver modelo em camadas do `_BRIEF` §2). O loop `montar prompt → chamar modelo → despachar tools → verificar` é o batimento cardíaco descrito na fonte, mas cada efeito externo tem de ser uma *activity* durável, isolada, idempotente e mediada. O system prompt é **remontado a cada turno** para preservar a estabilidade de cache (ADR-009) **e** hasheado no evento para garantir replay fiel (ADR-010). Este ticket entrega o esqueleto do loop sobre o qual os restantes tickets do epic acrescentam durabilidade, estados e compensação.

### Objectivo
Implementar o loop base do Agent Runtime que, dado um objectivo com identidade e escopo, monta um prompt cache-estável, chama o Model Gateway, despacha cada tool call através do Reference Monitor e verifica o resultado, gravando cada turno como evento no Event Store.

### Critérios de Aceitação (SMART)
- [ ] O loop aceita um objectivo com `run_id`, identidade do agente e escopo, e produz uma resposta ou uma transição de estado terminal.
- [ ] Cada turno é gravado no Event Store com: hash do prompt materializado, `model-id`/params/seed, versão do código de montagem e versões pinadas de skills/tools (manifesto por trajectória).
- [ ] O prompt é montado com prefixo imutável (system + tool set congelado no run) e tail append-only, sem reordenar o prefixo (ADR-009); o cache-hit-rate é observável.
- [ ] **Nenhuma** tool call é executada directamente: todas atravessam o Reference Monitor (ADR-002); um teste prova que uma chamada directa é impossível pela API do RT.
- [ ] O resultado de cada tool é devolvido ao loop marcado como `untrusted` (taint), coerente com ADR-005.
- [ ] Spans OTel GenAI `invoke_agent` e `chat` são emitidos por turno com `gen_ai.usage.*` e custo em USD (ADR-010).

### Detalhes Técnicos
- Componente: `RT` (Agent Runtime). Ficheiros sugeridos: `runtime/loop.*`, `runtime/prompt_assembly.*`, `runtime/turn_recorder.*`.
- Interface com `GW` (Model Gateway) para a chamada ao modelo; interface com `RM` para o despacho de tools; escrita em `ES` (Event Store) via o cliente append-only definido no EPIC-01.
- O tail append-only recebe `memory_context`, timestamps e resultados; a compressão é fora da hot path (não neste ticket — ver EPIC-04).
- Não implementar aqui idempotência de passo nem checkpoint intra-iteração (AOS-014/AOS-015); expor os *hooks* onde estes se ligam.

### Testes Requeridos
- Unit: montagem de prompt produz prefixo byte-idêntico entre turnos com o mesmo tool set (regressão de cache).
- Unit: tentativa de chamar uma tool fora do RM falha em compilação/execução.
- Integração: um objectivo simples percorre montar → chamar (modelo mockado) → despachar (RM mockado) → verificar, gravando N eventos de turno no Event Store.
- Observabilidade: spans `invoke_agent`/`chat` emitidos com atributos de uso e custo.

> **Nota cruzada (AOS-077 — EPIC-08).** O *handoff* de um sub-agente DELEGADO desacopla dois artefactos da mesma execução: ao CONTEXTO do pai volta só um resumo higienizado (`agentruntime.TrajectorySummary`, ~1–2k tokens, via `Runtime.RunDelegated`); ao BACKEND vai SEMPRE a árvore de spans completa. O `invoke_agent` do sub-agente enraíza-se sob o pai por `parent_span_id` (não só por atributos NHI): o `Goal.ParentTraceParent` semeia o `ctx`-raiz do filho a partir do `traceparent` W3C da âncora aberta no `Orquestrador.Spawn` (`SpawnHandle.ChildSeedTraceParent`). Contexto ≠ registo (Princípio 4): descartar do contexto é legítimo, do backend nunca — nada da trajectória se perde no handoff. Ver `tecnica/08` §4.

### Definition of Done
- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Toda a tool call mediada pelo Reference Monitor (ADR-002); sem chamada directa.
- [ ] Manifesto por trajectória gravado por turno (hash do prompt, model-id/params/seed, versões) (ADR-010).
- [ ] Spans OTel GenAI + custo USD por span adicionados.
- [ ] Código revisto (dois revisores — artefacto P0); testes verdes; cobertura não regride.
- [ ] Documentação de `tecnica/02` actualizada com o contrato do loop.

### Handoff para Claude Code
```text
És o executor do ticket AOS-013 do Agentic OS de Referência (AOS).
Lê AOS-013 em specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, o
tecnica/02_Agent_Runtime_Execucao_Duravel.md e os ADR-001/002/009/010.
Confirma que AOS-003 (Reference Monitor) e AOS-002 (Event Store) estão Done.

Implementa o loop base do RT: montar prompt cache-estável (prefixo imutável +
tail append-only), chamar o Model Gateway, despachar TODAS as tool calls via
Reference Monitor (nunca directo), verificar e gravar cada turno no Event Store
com o manifesto por trajectória (hash do prompt, model-id/params/seed, versões).
Marca resultados de tools como untrusted (taint). Emite spans invoke_agent/chat
com gen_ai.usage.* e custo USD. NÃO implementes idempotência de passo nem
checkpoint (são AOS-014/AOS-015); expõe os hooks.

Testes: prefixo byte-idêntico entre turnos; chamada directa a tool impossível;
percurso end-to-end com modelo e RM mockados. Corre build/lint/unit/integração.
Abre PR com o template da secção 7 de 01_Engineering_Standards_e_Handoff.md.
Se algo contradiz um ADR, PÁRA e pergunta.
```

---

## AOS-014 — Contrato de execução durável: idempotency key = f(run_id, step_id) [ADR-001]

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-013, AOS-002 (Event Store replicado) |
| Bloqueia | AOS-015, AOS-020, AOS-021, AOS-022, AOS-024 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1, Riscos), ADR-001 |

### Contexto
O modo de falha mais corrosivo do plano-base é a *double-execution*: um crash a meio de um `POST` não-idempotente re-executa o efeito no retry e corrompe o mundo externo. A fonte fixa a defesa como primitivo (ADR-001): **idempotency key = f(run_id, step_id)**, determinística, estável entre tentativas e única por passo lógico. Este ticket define e implementa o contrato de idempotência que todos os efeitos externos (AOS-021) e sagas (AOS-020) vão respeitar.

### Objectivo
Definir e implementar a função de derivação da *idempotency key* a partir de `(run_id, step_id)` e o mecanismo de deduplicação que garante que reexecutar um passo com a mesma chave não produz efeitos duplicados nem novos eventos de efeito no Event Store.

### Critérios de Aceitação (SMART)
- [ ] Existe uma função pura `idempotency_key(run_id, step_id)` determinística: a mesma entrada produz sempre a mesma chave; entradas distintas produzem chaves distintas (sem colisão nos casos de teste).
- [ ] O `step_id` é monotónico e estável por passo lógico dentro de um `run_id`, e sobrevive a re-tentativas (não é reatribuído no replay).
- [ ] A primeira execução de um passo regista a chave e o resultado no Event Store; uma reexecução com a mesma chave devolve o resultado registado **sem** repetir o efeito externo.
- [ ] Um teste que injecta crash após o efeito mas antes do commit prova, no retry, **zero efeitos duplicados** (driver não-funcional: 0 efeitos duplicados no retry).
- [ ] O contrato está documentado como interface pública consumível por AOS-020, AOS-021 e AOS-022.

### Detalhes Técnicos
- Componente: `RT`, com persistência em `ES`. Ficheiros sugeridos: `runtime/durable/idempotency.*`, `runtime/durable/step_ledger.*`.
- A chave deve ser opaca e estável (ex.: hash determinístico de `run_id` + `step_id` normalizados); documentar a construção exacta e o espaço de colisão.
- O *ledger* de passos guarda `(key → {status, resultado, hash})`; a verificação `already-applied` precede qualquer efeito.
- Coordenar o significado de `step_id` com o checkpoint intra-iteração (AOS-015) e com o replay (AOS-016) para que o número do passo seja o mesmo em execução e em replay.

### Testes Requeridos
- Unit: determinismo e ausência de colisão da função de chave sobre um conjunto representativo.
- Idempotência: reexecução do mesmo passo → 0 efeitos duplicados, resultado idêntico devolvido do ledger.
- Fault-injection: crash antes/depois do commit do efeito; retry converge sem duplicar.
- Integração: o ledger persiste no Event Store e sobrevive a reinício do worker.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] **Idempotência por passo verificada** com teste de reexecução (0 efeitos duplicados) (ADR-001).
- [ ] Contrato publicado e referenciado por AOS-020/AOS-021/AOS-022.
- [ ] Spans/eventos do ledger observáveis; sem segredos em logs.
- [ ] Código revisto (dois revisores — P0); testes verdes; cobertura não regride.
- [ ] `tecnica/02` actualizado com a especificação do contrato.

### Handoff para Claude Code
```text
És o executor do ticket AOS-014 do AOS. Lê AOS-014 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, o tecnica/02 e o ADR-001.
Confirma AOS-013 e AOS-002 Done.

Implementa o contrato de execução durável: função pura determinística
idempotency_key(run_id, step_id), step_id monotónico estável por passo lógico,
e um step-ledger no Event Store que verifica already-applied antes de qualquer
efeito e devolve o resultado registado no retry. Publica o contrato como
interface consumível por AOS-020/021/022.

Testes obrigatórios: determinismo/sem-colisão da chave; reexecução com 0 efeitos
duplicados; fault-injection (crash antes/depois do commit) converge sem duplicar;
ledger sobrevive a reinício. Corre unit/integração/idempotência e o harness quando
existir. PR pelo template da secção 7. Se ambíguo face ao ADR-001, PÁRA e pergunta.
```

---

## AOS-015 — Checkpoint intra-iteração no Event Store

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-013, AOS-014 |
| Bloqueia | AOS-016, AOS-017, AOS-022 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1), ADR-001, ADR-007 |

### Contexto
A durabilidade *resume-from-step* (e não *resume-from-task*) exige que o estado do progresso seja persistido **dentro** de uma iteração do loop, não só nas fronteiras de turno. Sem checkpoint intra-iteração, um crash a meio de uma iteração com vários passos força a repetir passos já confirmados — o que só é seguro por causa da idempotência (AOS-014), mas desperdiça trabalho e custo. Este ticket materializa o checkpoint no Event Store replicado (ADR-007), fonte de verdade.

### Objectivo
Persistir checkpoints intra-iteração no Event Store de forma que o RT retome a partir do último passo confirmado dentro da iteração corrente, sem repetir passos já aplicados nem perder passos pendentes.

### Critérios de Aceitação (SMART)
- [ ] Cada passo confirmado dentro de uma iteração escreve um evento de checkpoint append-only com `run_id`, `step_id` e cursor de progresso.
- [ ] Após crash a meio de uma iteração multi-passo, o RT retoma no próximo `step_id` não confirmado, sem re-executar os já confirmados.
- [ ] O checkpoint é consistente com o step-ledger de AOS-014 (o mesmo `step_id` identifica o mesmo passo lógico).
- [ ] A escrita de checkpoint não muta o prefixo cache-estável do prompt (ADR-009) — só cresce o tail/registo.
- [ ] Um teste de recuperação demonstra retoma correcta em pelo menos três pontos de crash distintos de uma iteração.

### Detalhes Técnicos
- Componente: `RT` + `ES`. Ficheiros sugeridos: `runtime/durable/checkpoint.*`, `runtime/durable/resume.*`.
- O checkpoint referencia o cursor de progresso da iteração (índice de passo, estado das activities pendentes) sem duplicar o payload já no Event Store.
- Definir a granularidade: checkpoint por passo (efeito externo) e não por token; a compressão de contexto continua fora da hot path.
- Preparar o cursor para ser consumido pelo replay determinístico (AOS-016).

### Testes Requeridos
- Recuperação: crash em 3+ pontos de uma iteração multi-passo; retoma sem repetir nem perder.
- Integração: checkpoints persistem no Event Store replicado e sobrevivem a failover de worker.
- Consistência: o `step_id` do checkpoint casa com o do ledger de AOS-014.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] Retoma *resume-from-step* demonstrada por teste de recuperação.
- [ ] Consistência checkpoint↔ledger verificada.
- [ ] Eventos observáveis; sem regressão de cache de prompt.
- [ ] Código revisto (dois revisores — P0); testes verdes.
- [ ] `tecnica/02` actualizado.

### Handoff para Claude Code
```text
És o executor do ticket AOS-015 do AOS. Lê AOS-015 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, o tecnica/02 e ADR-001/007.
Confirma AOS-013 e AOS-014 Done.

Implementa checkpoint intra-iteração no Event Store: evento append-only por passo
confirmado com run_id/step_id/cursor de progresso, e lógica de resume que retoma
no próximo step_id não confirmado sem repetir os já aplicados. Mantém o step_id
consistente com o ledger de AOS-014 e não mutes o prefixo cache-estável do prompt.

Testes: crash em 3+ pontos de uma iteração multi-passo com retoma correcta;
persistência sobrevive a failover; consistência checkpoint↔ledger. Corre
unit/integração/recuperação e o harness de replay. PR pelo template da secção 7.
```

---

## AOS-016 — Replay determinístico resume-from-step

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-015 |
| Bloqueia | AOS-022, AOS-024 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 4, Riscos), ADR-001, ADR-010 |

### Contexto
O replay fiel é a base do RCA e do eval-driven development (Dimensão 4). O risco a mitigar é o *replay infiel após evolução de código*: se os inputs não-determinísticos não forem capturados, uma trajectória deixa de reproduzir-se e invalida auditoria e evals. A fonte exige captura de **todos** os inputs não-determinísticos e o hash do prompt materializado por turno, com alvo de fidelidade de replay de 100%. Este ticket entrega o motor de replay *resume-from-step* que reconstrói uma trajectória a partir do Event Store.

### Objectivo
Implementar o replay determinístico que, a partir do Event Store, reconstrói uma trajectória passo-a-passo reproduzindo exactamente as mesmas transições, reutilizando resultados de activities registados (sem re-executar efeitos externos) e validando por hash.

### Critérios de Aceitação (SMART)
- [ ] O replay reconstrói uma trajectória a partir de um `run_id` reproduzindo 100% dos passos na mesma ordem e com os mesmos `step_id`.
- [ ] Todos os inputs não-determinísticos (respostas do modelo, relógio, aleatoriedade, resultados de tools) são lidos do log, **nunca** re-obtidos ao vivo; nenhum efeito externo é re-executado durante o replay.
- [ ] O hash do prompt materializado por turno em replay coincide com o gravado na execução original.
- [ ] O replay pode arrancar de qualquer `step_id` (resume-from-step), não só do início.
- [ ] Uma divergência entre replay e original é detectada e reportada com o passo exacto onde ocorre.

### Detalhes Técnicos
- Componente: `RT` + `ES` + `OBS`. Ficheiros sugeridos: `runtime/replay/engine.*`, `runtime/replay/nondeterminism_capture.*`.
- Modo de replay força as activities a devolver o resultado registado (via ledger de AOS-014) em vez de executar; injecta relógio/seed a partir do manifesto por trajectória.
- Emite `gen_ai.evaluation.result` ou marcador de replay ligado ao trace original (ADR-010) para o eval e o RCA.
- Alinhar o cursor de passo com o checkpoint de AOS-015.

### Testes Requeridos
- Replay: trajectória gravada reproduz-se 100% (hashes de prompt coincidem, mesma sequência de passos).
- Negativo: injectar divergência (ex.: alterar um input não capturado) e verificar detecção do passo divergente.
- Resume: replay a partir de um `step_id` intermédio produz o mesmo estado que a execução original nesse ponto.
- Segurança: confirmar que nenhum efeito externo é emitido em modo replay.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] **Replay determinístico testado** (resume-from-step, hashes coincidem) (ADR-010).
- [ ] Zero efeitos externos em modo replay comprovado por teste.
- [ ] Divergências detectadas com localização do passo.
- [ ] Código revisto (dois revisores — P0); testes verdes.
- [ ] `tecnica/02` e nota em `tecnica/08` (observabilidade) actualizadas.

### Handoff para Claude Code
```text
És o executor do ticket AOS-016 do AOS. Lê AOS-016 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, o tecnica/02 e ADR-001/010.
Confirma AOS-015 Done.

Implementa o motor de replay determinístico resume-from-step: reconstrói a
trajectória a partir do Event Store, lê TODOS os inputs não-determinísticos do
log (nunca ao vivo), força activities a devolver o resultado registado (sem
re-executar efeitos), injecta relógio/seed do manifesto, valida por hash de
prompt por turno e detecta o passo onde há divergência. Suporta arranque de
qualquer step_id.

Testes: reprodução 100% com hashes coincidentes; detecção de divergência
localizada; resume de step_id intermédio; zero efeitos externos em replay.
Corre o harness de replay como gate. PR pelo template da secção 7.
```

---

## AOS-017 — Máquina de estados durável

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-013, AOS-015 |
| Bloqueia | AOS-018, AOS-019, AOS-020, AOS-023 |
| Responsável sugerido | Arquitecto de Plataforma / Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1, diagrama stateDiagram), ADR-001, ADR-013 |

### Contexto
A máquina de estados grosseira do plano-base (`ready → running → complete + blocked`) confundia estados de suspensão legítimos com falhas. A fonte substitui-a por estados duráveis distintos, com `waiting_on_human` e `paused` de primeira classe. Este ticket implementa a máquina de estados canónica que serve de espinha dorsal ao epic e ao Escalonador (EPIC-03).

### Objectivo
Implementar a máquina de estados durável do RT com os dez estados canónicos e as transições válidas, persistida no Event Store, rejeitando transições inválidas e sobrevivendo a crash.

### Critérios de Aceitação (SMART)
- [ ] Os estados implementados são exactamente: `ready`, `running`, `waiting_on_tool`, `waiting_on_human`, `paused`, `complete`, `failed`, `compensating`, `killed`, `timed_out`.
- [ ] As transições válidas seguem o diagrama da fonte (ex.: `ready → running` com fencing token; `running → waiting_on_tool → running`; `running → waiting_on_human → running|killed`; `running → paused → running`; `running → complete|failed|timed_out`; `failed → compensating → ready`).
- [ ] Uma transição inválida é rejeitada e não corrompe o estado persistido.
- [ ] Cada transição é um evento append-only no Event Store; o estado corrente é reconstruível por replay.
- [ ] `timed_out` dispara ao exceder o wall-clock configurado; `waiting_on_human` transita para `killed` em timeout fail-closed (ADR-013).

### Detalhes Técnicos
- Componente: `RT` + `ES`. Ficheiros sugeridos: `runtime/state/machine.*`, `runtime/state/transitions.*`.
- A entrada em `running` exige um fencing token válido (contrato partilhado com AOS-018 e o Escalonador do EPIC-03).
- Definir a tabela de transições como dados (declarativa) para testabilidade e para o replay reconstruir estado.
- Não implementar aqui o mecanismo de lease/heartbeat (AOS-018) nem o canal de steer (AOS-023); expor os eventos `pause`/`resume`/`kill`.

### Testes Requeridos
- Unit: matriz de transições — cada par válido aceite, cada par inválido rejeitado.
- Integração: sequência realista (`ready → running → waiting_on_tool → running → complete`) persistida e reconstruída por replay.
- Fail-closed: `waiting_on_human` sem aprovação dentro do TTL → `killed`.
- Recuperação: estado corrente reconstruído após crash a partir do log.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] Transições inválidas rejeitadas sem corrupção; estado reconstruível por replay.
- [ ] Timeout fail-closed para `waiting_on_human` verificado (ADR-013).
- [ ] Eventos de transição observáveis (spans/estado).
- [ ] Código revisto (dois revisores — P0); testes verdes.
- [ ] `tecnica/02` actualizado com o diagrama e a tabela de transições.

### Handoff para Claude Code
```text
És o executor do ticket AOS-017 do AOS. Lê AOS-017 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, o tecnica/02 e ADR-001/013,
incluindo o stateDiagram da fonte. Confirma AOS-013 e AOS-015 Done.

Implementa a máquina de estados durável com os dez estados canónicos
(ready/running/waiting_on_tool/waiting_on_human/paused/complete/failed/
compensating/killed/timed_out) e a tabela declarativa de transições válidas.
Persiste cada transição como evento append-only; reconstrói estado por replay;
rejeita transições inválidas; entra em running só com fencing token; aplica
timeout fail-closed (waiting_on_human -> killed) e timed_out por wall-clock.
Expõe eventos pause/resume/kill sem implementar lease (AOS-018) nem steer (AOS-023).

Testes: matriz de transições válidas/inválidas; sequência realista reconstruída
por replay; fail-closed do gate humano; recuperação de estado após crash.
PR pelo template da secção 7.
```

---

## AOS-018 — Liveness por lease/heartbeat + fencing tokens

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-017, AOS-002 (Event Store replicado) |
| Bloqueia | AOS-019 |
| Responsável sugerido | Engenheiro de Runtime / DevOps-SRE |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-03_Orquestracao_Escalonamento.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1, Riscos), ADR-001 |

### Contexto
A liveness por PID falha silenciosamente em containers remotos: um worker cross-host aparenta saudável ou aparenta morto sem o estar, e a tarefa acaba executada duas vezes. A fonte substitui o PID por **lease/heartbeat com TTL** e **fencing tokens** monotónicos que invalidam escritas de um worker obsoleto. Este ticket entrega o mecanismo de liveness distribuída do RT, coerente com o substrato replicado.

### Objectivo
Implementar leases com TTL renováveis por heartbeat e fencing tokens monotónicos, de modo que um worker cujo lease expirou não consiga confirmar escritas e o trabalho seja reatribuído sem duplicação.

### Critérios de Aceitação (SMART)
- [ ] Ao reclamar (`claim`) um run, o worker obtém um lease com TTL e um fencing token estritamente monotónico.
- [ ] O heartbeat renova o lease antes do TTL; a ausência de heartbeat leva à expiração e à disponibilização do run para reclamação.
- [ ] Escritas no Event Store carregam o fencing token; escritas com token inferior ao corrente são rejeitadas (worker obsoleto).
- [ ] Um teste cross-host simulado (worker "lento" que volta a escrever após reatribuição) demonstra **zero execução dupla** graças ao fencing.
- [ ] Nenhum caminho de código decide liveness por PID; auditável por revisão e teste.

### Detalhes Técnicos
- Componente: `RT` + `ES`; contrato partilhado com `SCH` (Escalonador, EPIC-03). Ficheiros sugeridos: `runtime/durable/lease.*`, `runtime/durable/fencing.*`.
- O fencing token é atribuído de forma monotónica pelo Event Store/coordenador; documentar a origem do contador e a sua durabilidade.
- Coordenar com AOS-025..AOS-034 (EPIC-03) para não duplicar a implementação do token; expor uma interface partilhável.
- Alinhar o `claim` com a transição `ready → running` de AOS-017.

### Testes Requeridos
- Integração: ciclo claim → heartbeat → renovação; expiração após ausência de heartbeat.
- Fencing: worker obsoleto tenta escrever com token antigo → rejeitado; sem duplicação.
- Concorrência: dois workers competem pelo mesmo run; só o de token corrente confirma.
- Auditoria: grep/inspecção confirma ausência de decisão de liveness por PID.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] Zero execução dupla sob reatribuição comprovado por teste de fencing.
- [ ] Contrato de fencing partilhável com o EPIC-03 documentado.
- [ ] Eventos de lease/heartbeat observáveis.
- [ ] Código revisto (dois revisores — P0); testes verdes.
- [ ] `tecnica/02` actualizado; nota cruzada em `tecnica/03`.

### Handoff para Claude Code
```text
És o executor do ticket AOS-018 do AOS. Lê AOS-018 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, o tecnica/02, a nota de
EPIC-03 e o ADR-001. Confirma AOS-017 e AOS-002 Done.

Implementa liveness distribuída: lease com TTL renovável por heartbeat e fencing
token monotónico atribuído pelo Event Store/coordenador. Escritas carregam o
token; escritas com token inferior ao corrente são rejeitadas. Alinha o claim com
a transição ready->running de AOS-017. NÃO uses PID para liveness. Expõe o
contrato de fencing de forma partilhável com o Escalonador (EPIC-03).

Testes: ciclo claim/heartbeat/expiração; worker obsoleto rejeitado sem duplicar;
concorrência de dois workers; auditoria confirma ausência de PID. PR secção 7.
```

---

## AOS-019 — waiting_on_human sem colidir com detecção de zumbi

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-017, AOS-018 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/08_Observabilidade_Evals.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1 e 4), ADR-001, ADR-013 |

### Contexto
No plano-base o gate HITL parecia um worker `running` pendurado, e a detecção de zumbis marcava-o como morto. A fonte separa os eixos: `waiting_on_human` é um estado durável de suspensão legítima, não um sintoma de worker preso. Este ticket garante que a detecção de zumbi (por lease e por circuit breaker multi-sinal) **não** dispara sobre estados de espera legítima, mantendo ao mesmo tempo o timeout fail-closed do gate.

### Objectivo
Assegurar que `waiting_on_human` (e, por extensão, `waiting_on_tool` e `paused`) suspende o relógio de liveness/zumbi apropriadamente, sem desactivar o TTL de aprovação fail-closed do gate humano.

### Critérios de Aceitação (SMART)
- [ ] Um run em `waiting_on_human` **não** é classificado como zumbi pela detecção baseada em lease enquanto o estado de espera for legítimo (o lease de espera é distinto do heartbeat de trabalho activo).
- [ ] O circuit breaker multi-sinal (cost/token velocity, wall-clock de trabalho, action-dedup, ausência de progresso) **não** conta o tempo de espera humana como "sem progresso" de trabalho activo.
- [ ] O TTL de aprovação do gate continua activo: sem aprovação dentro do prazo → `killed` (fail-closed, ADR-013).
- [ ] Um teste demonstra um run parado 100% do wall-clock de aprovação em `waiting_on_human` que **não** é morto por falso-positivo de zumbi, mas **é** morto ao exceder o TTL do gate.
- [ ] `waiting_on_tool` e `paused` recebem tratamento análogo (não são zumbis).

### Detalhes Técnicos
- Componente: `RT` + `OBS`. Ficheiros sugeridos: `runtime/liveness/waiting_states.*`.
- Distinguir "relógio de trabalho activo" de "relógio de espera": o heartbeat de trabalho pausa; um lease de espera com o seu próprio TTL governa o gate.
- Integrar com o circuit breaker multi-sinal do `tecnica/08`/EPIC-08 (contrato de sinais); aqui só se garante a exclusão dos estados de espera.

### Testes Requeridos
- Espera legítima: run em `waiting_on_human` por longa duração não é marcado zumbi.
- Fail-closed: exceder TTL do gate → `killed`.
- Não-regressão: um worker realmente preso em `running` continua a ser detectado como zumbi.
- Cobertura de `waiting_on_tool` e `paused`.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] Falso-positivo de zumbi sobre espera legítima eliminado; verdadeiro zumbi ainda detectado.
- [ ] Timeout fail-closed preservado (ADR-013).
- [ ] Código revisto; testes verdes; cobertura não regride.
- [ ] `tecnica/02` e nota em `tecnica/08` actualizadas.

### Handoff para Claude Code
```text
És o executor do ticket AOS-019 do AOS. Lê AOS-019 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, tecnica/02 e tecnica/08,
e ADR-001/013. Confirma AOS-017 e AOS-018 Done.

Garante que waiting_on_human/waiting_on_tool/paused suspendem o relógio de
trabalho activo (heartbeat pausado) sem serem classificados como zumbi, enquanto
um lease de espera com TTL próprio mantém o gate humano fail-closed
(sem aprovação no prazo -> killed). Integra com o circuit breaker multi-sinal
apenas para excluir estados de espera.

Testes: espera longa não marcada zumbi; TTL do gate excedido -> killed; worker
realmente preso em running continua detectado; cobre waiting_on_tool e paused.
PR pelo template da secção 7.
```

---

## AOS-020 — Sagas de compensação

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-014, AOS-017 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1, Riscos), ADR-001 |

### Contexto
Os gates do plano-base preveniam acções mas não revertiam efeitos parciais já aplicados. A fonte adiciona **saga de compensação**: quando um passo falha após efeitos externos parciais, transita-se para `compensating` e executam-se as acções inversas de forma idempotente, devolvendo o sistema a um estado consistente antes do retry. Este ticket implementa o mecanismo de saga sobre a máquina de estados e o contrato de idempotência.

### Objectivo
Implementar sagas de compensação que, na falha de um run com efeitos parciais, executem as compensações registadas por ordem inversa e idempotente, transitando `failed → compensating → ready` para retry limpo.

### Critérios de Aceitação (SMART)
- [ ] Cada activity com efeito externo pode registar uma acção de compensação associada ao seu `step_id`.
- [ ] Na entrada em `compensating`, as compensações dos passos aplicados executam-se por ordem inversa, cada uma idempotente (chave `f(run_id, step_id)` de compensação).
- [ ] Uma compensação reexecutada não duplica a reversão (0 efeitos de compensação duplicados).
- [ ] Após compensação bem-sucedida, o run transita para `ready` (retry) ou para um terminal, conforme política; o estado é durável e reconstruível por replay.
- [ ] Um teste de falha a meio de uma sequência multi-passo demonstra reversão completa e consistente dos efeitos parciais.

### Detalhes Técnicos
- Componente: `RT` + `ES`. Ficheiros sugeridos: `runtime/saga/compensation.*`, `runtime/saga/registry.*`.
- Reutilizar o step-ledger de AOS-014 para idempotência das compensações; registar a compensação como evento append-only.
- Integrar com a transição `failed → compensating → ready` de AOS-017.
- Documentar a semântica quando uma compensação falha (retry idempotente da compensação; escalada para `killed`/alerta se irrecuperável).

### Testes Requeridos
- Saga feliz: falha após K de N passos → compensa os K por ordem inversa; estado consistente.
- Idempotência de compensação: reexecutar a saga não duplica reversões.
- Recuperação: crash durante a compensação → retoma a compensação sem repetir as já aplicadas.
- Transições de estado coerentes com AOS-017.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] Compensação idempotente verificada (0 reversões duplicadas) (ADR-001).
- [ ] Transições `failed → compensating → ready` coerentes e reconstruíveis por replay.
- [ ] Código revisto; testes verdes.
- [ ] `tecnica/02` actualizado com a semântica de saga.

### Handoff para Claude Code
```text
És o executor do ticket AOS-020 do AOS. Lê AOS-020 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, tecnica/02 e ADR-001.
Confirma AOS-014 e AOS-017 Done.

Implementa sagas de compensação: cada activity com efeito externo regista uma
compensação por step_id; na entrada em compensating, executa as compensações por
ordem inversa, idempotentes (chave f(run_id, step_id) de compensação), reutilizando
o step-ledger de AOS-014. Integra a transição failed->compensating->ready de
AOS-017 e define a semântica de compensação que falha (retry idempotente / escalada).

Testes: falha após K de N passos com reversão completa; reexecução sem duplicar;
crash durante compensação retoma sem repetir; transições coerentes. PR secção 7.
```

---

## AOS-021 — Activities: isolamento de efeitos externos

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-013, AOS-014, AOS-003 (Reference Monitor) |
| Bloqueia | AOS-022 |
| Responsável sugerido | Engenheiro de Runtime / Engenheiro de Segurança |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/07_Seguranca_Isolamento.md`, `_FONTE_agentic-os-ideal.md` (Fluxo de execução), ADR-001, ADR-002 |

### Contexto
Na fonte, "o loop é o batimento cardíaco, mas cada efeito externo é uma *activity* durável, isolada, idempotente e mediada". A *activity* é a unidade que separa a lógica determinística do loop (reproduzível) do efeito não-determinístico sobre o mundo externo (que tem de ser registado e não re-executado no replay). Este ticket define o contrato de *activity* e obriga a que todo o efeito externo passe por ele — e, através dele, pelo Reference Monitor.

### Objectivo
Implementar o contrato de *activity* que isola todo o efeito externo do RT: cada activity é idempotente (AOS-014), mediada pelo Reference Monitor (ADR-002), tem o seu resultado registado no Event Store para replay, e nunca é chamada fora deste contrato.

### Critérios de Aceitação (SMART)
- [ ] Todo o efeito externo (tool call, I/O, chamada de rede) é encapsulado numa *activity* com `step_id` e idempotency key.
- [ ] Cada activity é despachada através do Reference Monitor antes de executar (identidade, política, orçamento, egress, audit) — sem caminho directo (ADR-002).
- [ ] O resultado da activity é gravado como evento append-only no Event Store e devolvido ao loop marcado `untrusted`.
- [ ] Em modo replay (AOS-016), a activity devolve o resultado registado sem re-executar o efeito.
- [ ] A lógica determinística do loop não contém efeitos externos fora de activities; um teste/lint prova a separação.

### Detalhes Técnicos
- Componente: `RT` + `RM` + `ES`. Ficheiros sugeridos: `runtime/activity/contract.*`, `runtime/activity/dispatch.*`.
- A activity é o ponto de ligação entre AOS-014 (idempotência), AOS-016 (replay), AOS-020 (compensação) e o RM (mediação).
- Definir o formato do evento de resultado (inputs normalizados, hash, custo, taint) para alimentar replay e observabilidade.
- Não implementar o engine externo (AOS-022); a activity deve ser abstracta o suficiente para mapear tanto num engine (Temporal/Restate/DBOS) como no contrato próprio.

### Testes Requeridos
- Mediação: nenhuma activity executa sem passar pelo RM (teste de bypass falha).
- Idempotência: reexecução da activity não duplica efeito (cruza AOS-014).
- Replay: activity em modo replay devolve resultado registado, zero efeito.
- Separação: lint/teste que detecta efeito externo fora de activity.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] **Toda a tool call mediada pelo Reference Monitor** (ADR-002); sem bypass.
- [ ] Idempotência e replay das activities verificados.
- [ ] Resultados untrusted (taint) e observáveis (custo por span).
- [ ] Código revisto (dois revisores — P0); testes verdes.
- [ ] `tecnica/02` e nota em `tecnica/07` actualizadas.

### Handoff para Claude Code
```text
És o executor do ticket AOS-021 do AOS. Lê AOS-021 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, tecnica/02, tecnica/07 e
ADR-001/002. Confirma AOS-013, AOS-014 e AOS-002 Done.

Implementa o contrato de activity que isola TODO efeito externo: cada activity
tem step_id e idempotency key (AOS-014), é despachada pelo Reference Monitor
antes de executar (sem bypass), grava resultado append-only no Event Store,
devolve-o marcado untrusted, e em modo replay devolve o resultado registado sem
re-executar. A lógica determinística do loop não pode ter efeitos externos fora
de activities. Mantém a abstracção agnóstica ao engine (para AOS-022).

Testes: bypass do RM falha; reexecução sem duplicar; replay sem efeito; lint que
apanha efeito fora de activity. PR pelo template da secção 7.
```

---

## AOS-022 — Integração com engine de durable execution (Temporal/Restate/DBOS) ou contrato próprio

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | spike |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-014, AOS-015, AOS-016 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `specs/00_System_Spec.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 1), ADR-001, ADR-007 |

### Contexto
A fonte oferece uma decisão explícita: **adoptar durable execution como primitivo** integrando um engine (Temporal/Restate/DBOS) **ou** implementando o contrato de forma explícita (idempotência por passo, checkpoint intra-iteração, replay a partir do log, activities isoladas). Esta é uma decisão de arquitectura com trade-offs de operação, custo e lock-in que deve ser resolvida por um spike avaliativo antes de comprometer a implementação de produção. É um ticket **spike + feature**: o spike decide e produz um ADR; a parte de feature entrega o adaptador escolhido.

### Objectivo
Avaliar, através de um spike com provas de conceito comparáveis, se o AOS deve integrar um engine de durable execution ou consolidar o contrato próprio (AOS-014/015/016/021), registar a decisão num ADR (a numerar após ADR-014) e entregar o adaptador correspondente que satisfaz o contrato de activity.

### Critérios de Aceitação (SMART)
- [ ] Existe uma matriz de decisão comparando pelo menos Temporal, Restate, DBOS e o contrato próprio, sobre eixos: garantias de idempotência/replay, operação/HA, custo, lock-in, latência e ajuste ao Event Store replicado (ADR-007).
- [ ] Cada opção tem uma PoC mínima que corre o mesmo cenário de referência (run multi-passo com crash e retoma) e reporta fidelidade de replay e efeitos duplicados.
- [ ] A decisão é registada num ADR novo (contexto, decisão, consequências, alternativas) sem contradizer os ADRs canónicos, em particular ADR-001.
- [ ] O adaptador escolhido implementa o contrato de activity de AOS-021 e passa os testes de idempotência (AOS-014) e replay (AOS-016) sem alterações à API do RT. **REAVALIADO em AOS-296:** era verdadeiro do adaptador `OwnContractEngine`, que foi **removido** por não ter consumidor de produção. A segunda metade — «sem alterações à API do RT» — só se manteve verdadeira *porque* o RT nunca programou contra a porta; ligá-la exigiria mudar a assinatura de `ActivityDispatcher`, uma interface pública do kernel. Volta a `[ ]`: o que existe hoje é o despacho cablado individualmente, não um adaptador atrás de uma porta.
- [ ] Se a decisão for "contrato próprio", o spike confirma que AOS-014/015/016/021 cobrem integralmente o contrato e documenta o gap, se houver. **REAVALIADO em AOS-296:** a composição que o demonstrava era o `OwnContractEngine`, removido. A cobertura das quatro peças continua a existir individualmente e testada nos respectivos pacotes; o que deixou de existir é a demonstração de que **compostas atrás de uma porta única** cobrem o contrato.

### Detalhes Técnicos
- Componente: `RT` (camada de durabilidade). Ficheiros sugeridos: `runtime/durable/engine_adapter.*`, `docs/adr/ADR-0XX-durable-execution.md`.
- O adaptador expõe a mesma interface de activity/checkpoint/replay independentemente do backend; o RT não deve saber qual engine está por baixo (coerência por contrato, Princípio 8).
- Considerar o encaixe com o Escalonador (EPIC-03) para leases/fencing e com o Event Store como fonte de verdade.
- Time-box do spike explícito; a parte de feature só arranca após o ADR ratificado.

### Testes Requeridos
- PoC comparável: cenário de referência corre em cada opção; métricas de replay e duplicação recolhidas.
- Contrato: o adaptador escolhido passa a suíte de idempotência e replay (harness de AOS-024).
- Isolamento: trocar o backend não altera a API do RT (teste de contrato). **SEM TESTE desde AOS-296:** era o `engine_contract_test.go`, a única prova executável de backend-swap do repositório, removido com a porta. Fica declarado em vez de silenciosamente vazio — nenhum outro teste troca o backend de execução durável.

### Definition of Done
- [ ] Matriz de decisão e PoCs entregues; ADR novo escrito e ratificado (assinado).
- [ ] Adaptador do backend escolhido implementado e a passar idempotência + replay. **REAVALIADO em AOS-296:** o adaptador foi removido; não há adaptador de backend a passar coisa nenhuma. Idempotência e replay continuam provados nos pacotes próprios (`durable/`, `replay/`, `harness/`), fora de qualquer porta.
- [ ] Sem contradição com ADR-001/007; lock-in avaliado e documentado.
- [ ] Código revisto (dois revisores — decisão de arquitectura); testes verdes.
- [x] `tecnica/02` e `specs/00_System_Spec.md` actualizados com a decisão. **MANTÉM-SE:** é uma afirmação sobre documentação, e a remoção não a falsifica — `tecnica/02` §4.4 foi reescrito em AOS-296 para registar a porta, a razão da remoção e o que se perdeu.

### Handoff para Claude Code
```text
És o executor do ticket AOS-022 (spike+feature) do AOS. Lê AOS-022 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, tecnica/02, specs/00_System_Spec.md
e ADR-001/007. Confirma AOS-014, AOS-015 e AOS-016 Done.

Fase spike (time-boxed): constrói PoCs comparáveis (Temporal, Restate, DBOS e
contrato próprio) correndo o mesmo cenário de referência (run multi-passo com
crash e retoma), recolhe fidelidade de replay e efeitos duplicados, e produz uma
matriz de decisão (idempotência/replay, HA/operação, custo, lock-in, latência,
encaixe com Event Store). Escreve um ADR novo (após ADR-014) sem contradizer
ADR-001. PÁRA para ratificação humana assinada antes da feature.

Fase feature: implementa o engine_adapter escolhido cumprindo o contrato de
activity de AOS-021, passando idempotência (AOS-014) e replay (AOS-016) sem mudar
a API do RT. Testes de contrato garantem que trocar o backend não altera o RT.
PR pelo template da secção 7.
```

---

## AOS-023 — Estado paused + canal de steer/interrupt [ADR-013]

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-017 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `_FONTE_agentic-os-ideal.md` (Dimensão 6), ADR-013, ADR-001 |

### Contexto
O plano-base oferecia streaming read-only (observação passiva), não controlo. A fonte adiciona **controlo bidireccional**: qualquer superfície emite um sinal, o loop faz *graceful pause* no fim do turno, aceita uma correcção e retoma (estilo AgentScope 1.0). Isto materializa o estado durável `paused` e o canal de steer/interrupt fora-de-banda que o ADR-013 exige. Este ticket entrega esse canal sobre a máquina de estados de AOS-017.

### Objectivo
Implementar o estado `paused` com um canal de controlo fora-de-banda que permite pausar o run no fim do turno corrente, injectar uma correcção (steer) e retomar, preservando a durabilidade e o replay.

### Critérios de Aceitação (SMART)
- [ ] Qualquer superfície pode emitir um sinal `pause`/`steer`/`resume` associado a um `run_id` através de um canal fora-de-banda (não pelo prompt).
- [ ] O loop faz *graceful pause* no fim do turno corrente (não interrompe uma activity a meio) e transita `running → paused`.
- [ ] Em `paused`, uma correcção injectada é gravada como evento append-only e aplicada ao retomar; a retoma transita `paused → running`.
- [ ] O steer não é conteúdo untrusted: a correcção autenticada entra como instrução do canal de controlo, distinta dos dados untrusted (ADR-005), com identidade do emissor registada.
- [ ] O ciclo pause → steer → resume é durável (sobrevive a crash) e reconstruível por replay.

### Detalhes Técnicos
- Componente: `RT` + `ES`. Ficheiros sugeridos: `runtime/control/steer_channel.*`, `runtime/control/pause_resume.*`.
- O canal de controlo é separado do canal de dados; a correcção carrega identidade/assinatura do emissor para audit (não-repúdio).
- Integrar com o estado `paused` e as transições de AOS-017; a paridade de superfície (Slack/Telegram/etc.) é responsabilidade de camadas de UX, aqui expõe-se apenas a API do canal.
- Graceful pause respeita a fronteira de activity para não deixar efeitos parciais.

### Testes Requeridos
- Pause: sinal durante um turno → pausa no fim do turno, nunca a meio de uma activity.
- Steer: correcção injectada é aplicada na retoma; identidade do emissor registada.
- Durabilidade: crash em `paused` → retoma com a correcção intacta.
- Replay: o ciclo pause/steer/resume reproduz-se fielmente.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] `paused` durável e reconstruível por replay.
- [ ] Steer autenticado e registado (não-repúdio) (ADR-013); distinto de dados untrusted (ADR-005).
- [ ] Código revisto; testes verdes.
- [ ] `tecnica/02` actualizado; nota cruzada em `tecnica/09` (governação/HITL).

### Handoff para Claude Code
```text
És o executor do ticket AOS-023 do AOS. Lê AOS-023 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, tecnica/02 e ADR-013/001/005.
Confirma AOS-017 Done.

Implementa o estado paused e um canal de steer/interrupt fora-de-banda (não pelo
prompt): sinais pause/steer/resume por run_id; graceful pause no fim do turno
(nunca a meio de uma activity); correcção injectada gravada como evento e aplicada
na retoma; steer autenticado com identidade do emissor (não-repúdio), tratado como
instrução do canal de controlo e distinto de dados untrusted. O ciclo é durável e
reproduzível por replay.

Testes: pausa no fim do turno; steer aplicado na retoma; crash em paused mantém a
correcção; replay fiel do ciclo. PR pelo template da secção 7.
```

---

## AOS-024 — Harness de testes de replay/idempotência

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | 0 — Fundações |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-014, AOS-016 |
| Bloqueia | — |
| Responsável sugerido | QA |
| Documentos de referência | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-11_Testes_Qualidade.md`, `specs/01_Engineering_Standards_e_Handoff.md` (§4, gates 8), ADR-001, ADR-010 |

### Contexto
Os gates de CI/CD do AOS incluem o **teste de replay determinístico** (gate 8) e a idempotência por passo como Done não-negociável (§3 de `01_Engineering_Standards_e_Handoff.md`). Estas propriedades têm de ser verificáveis de forma repetível e automática, não por inspecção manual. Este ticket entrega o harness reutilizável que exercita replay e idempotência sobre trajectórias, servindo tanto o desenvolvimento local como o pipeline.

### Objectivo
Construir um harness de testes que, dado um run gravado, verifique automaticamente (a) idempotência por passo (reexecução sem efeitos duplicados) e (b) replay determinístico (reprodução 100% resume-from-step com hashes coincidentes), integrável no CI como gate fail-closed.

### Critérios de Aceitação (SMART)
- [ ] O harness carrega uma trajectória do Event Store e corre o replay determinístico, falhando se algum passo divergir (hash de prompt ou sequência).
- [ ] O harness reexecuta passos com efeitos e verifica **zero efeitos duplicados** via o step-ledger (AOS-014).
- [ ] O harness suporta fault-injection parametrizável (pontos de crash) e confirma retoma correcta.
- [ ] Corre em CI como gate fail-closed (mapeia gate 8 e o driver de replay-fidelity 100%); um relatório de fidelidade de replay é emitido.
- [ ] Fornece fixtures/golden trajectories reutilizáveis por outros epics e é documentado para uso local.

### Detalhes Técnicos
- Componente: transversal a `RT`/`ES`/`OBS`; ficheiros sugeridos: `tests/harness/replay_idempotency.*`, `tests/fixtures/trajectories/*`.
- Reutiliza o motor de replay de AOS-016 e o ledger de AOS-014; não reimplementa a lógica, orquestra e afere.
- Emite as métricas `replay-fidelity` e "efeitos duplicados" consumíveis pelas métricas operacionais do backlog (§9 de `01_Engineering_Standards_e_Handoff.md`).
- Alinhar com `specs/EPIC-11_Testes_Qualidade.md` para não duplicar o eval harness (foco distinto: replay/idempotência vs golden-set de comportamento).

### Testes Requeridos
- Meta-teste: o harness deteca uma trajectória propositadamente adulterada (divergência) e falha.
- Meta-teste: o harness deteca um efeito duplicado injectado e falha.
- Integração CI: o gate corre em pipeline e bloqueia em vermelho.
- Reprodutibilidade: as fixtures produzem resultados estáveis entre execuções.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos.
- [ ] Harness integrado no CI como gate fail-closed (gate 8); relatório de replay-fidelity emitido.
- [ ] Fixtures/golden trajectories versionadas e documentadas.
- [ ] Código revisto; meta-testes verdes.
- [ ] `tecnica/02` e nota em `specs/EPIC-11_Testes_Qualidade.md` actualizadas.

### Handoff para Claude Code
```text
És o executor do ticket AOS-024 (chore) do AOS. Lê AOS-024 em
specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md, tecnica/02, o §4 de
01_Engineering_Standards_e_Handoff.md e ADR-001/010. Confirma AOS-014 e AOS-016 Done.

Constrói um harness reutilizável que, dado um run gravado, verifica idempotência
por passo (reexecução -> 0 efeitos duplicados via step-ledger) e replay
determinístico (reprodução 100% resume-from-step, hashes coincidentes), com
fault-injection parametrizável. Integra-o no CI como gate fail-closed (gate 8),
emite relatório de replay-fidelity, e fornece fixtures/golden trajectories
documentadas. Reutiliza AOS-016 e AOS-014, não reimplementes.

Meta-testes: harness apanha trajectória adulterada e efeito duplicado injectado;
gate bloqueia em vermelho; fixtures estáveis. Não dupliques o eval harness do
EPIC-11. PR pelo template da secção 7.
```

---

## AOS-396 — Manifesto do turno pina o modelo que respondeu (model_id vazio no nó)

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-013 (loop e `turn.recorded`), AOS-016 (replay) — ambos fechados |
| Bloqueia | — |
| Relacionado | AOS-394 (correlação dos selos do gateway com o run; independente deste) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/kernel/agent-runtime/loop.go` (`recordTurn`), `packages/kernel/agent-runtime/model.go` (`ModelConfig`, `ModelResponse`), `packages/kernel/agent-runtime/replay/engine.go`, `packages/cmd/aos/api.go` (`submitRequest`, `handleSubmit`), `packages/platform/model-gateway/runtime_adapter.go` (`translateResponse`), `tecnica/13_Modelo_Dados_Eventos.md` (§3.3, §6.1) |

### Contexto

Medido em produção a 2026-09-15 no run `run-delegado-1789509858` (roteiro E2E manual, PR #299): os 6 eventos `turn.recorded` têm `manifest.model.model_id` vazio e `seed` 0, embora o gateway tenha selado cada chamada como `gpt-4o-mini`. O run de 14 de Setembro tem o mesmo, e o nó local com o modelo de referência também. O `tecnica/13` define `model` (`model_id`/`params`/`seed`) no manifesto como a âncora de *como* o passo foi produzido.

Confirmado no código:

- `recordTurn` grava `goal.Model.ModelID` e `goal.Model.Seed` (`loop.go:672-674`); o span `chat` usa o mesmo valor em `gen_ai.request.model` (`loop.go:636`).
- O nó nunca preenche `Goal.Model`: `submitRequest` não tem campo de modelo (`api.go:516-524`) e `handleSubmit` constrói o `Goal` sem ele (`api.go:619-627`). `AOS_MODEL_NAME` chega só ao adaptador do gateway e à tabela de preços.
- A resposta não traz o modelo de volta: `agentruntime.ModelResponse` não tem campo de modelo, e `translateResponse` não copia `port.ChatResponse.Model` (só o usa numa mensagem de erro).
- Consequência no replay: `replay/engine.go:585` só compara o modelo gravado com o esperado quando `spec.Model.ModelID != ""`. Com o manifesto vazio, uma troca de modelo entre a gravação e o replay passa sem ser detectada.

Não é um defeito do gateway: o campo fica vazio com qualquer `ModelClient` que o nó componha.

### Objectivo

O `turn.recorded` de cada turno regista o modelo que produziu a resposta e os parâmetros de amostragem realmente usados, e o replay volta a verificar o modelo.

### Critérios de Aceitação

- [x] **Decisão registada neste ticket:** o manifesto regista o modelo **pedido** (configuração do nó), o modelo **servido** (o que o provider devolveu, que pode diferir por troca de modelo no gateway), ou ambos. Se forem os dois, a forma fica acordada com `tecnica/13` (expand compatível, sem partir o `Manifest` existente). *(**DECISÃO: ambos.** `model_id` continua a ser o modelo **pedido** (`Goal.Model.ModelID`): é o que o span `chat`, o `invoke_agent` e a admissão do turno já lêem, e o que o replay compara — um nome estável, não a versão datada que o provider devolve (ex.: `gpt-4o-mini-2024-07-18`), que daria divergências falsas. O modelo **servido** entra num campo novo, `served_model_id`, `omitempty` (`ModelManifest`, `turn.go`), vindo de `ModelResponse.Model` ← `port.ChatResponse.Model` no adaptador do gateway. Expand compatível: um cliente que não reporta o modelo grava os bytes de antes (precedente `usage_ausente`, AOS-336) e o `schema_version` do manifesto fica `1.0`. **Rejeitadas**: só o pedido (perde a troca de modelo e a versão real); só o servido (o nome do provider não é estável, e um turno reproduzido na retoma vem sem ele). Forma descrita em `tecnica/13` §6.1.)*
- [x] Com o gateway composto, o `turn.recorded` de um turno real tem `model_id` não vazio e igual ao modelo decidido acima; o `seed` e os `params` só aparecem quando foram de facto enviados ao provider (nunca preenchidos com valores que não viajaram). *(No nó por ambiente, `main.go` põe `AOS_MODEL_NAME` em `Config.ModelID` (uma só leitura, `modelNameFromEnv`, partilhada com o `parseModelFromEnv`), e `NodeService.hostRun` escreve-o no goal por `Node.fixarModelo` antes do registo de crash-resume e do primeiro turno — a via única da submissão, da retoma e da varredura de crash-resume. Com o gateway o modelo é **autoritativo** e sobrepõe-se ao do goal, porque é o nome que o adaptador envia; isso cobre a retoma de um run gravado com outra configuração. `seed` e `params` não são tocados: nenhum cliente composto os envia ao provider, pelo que `params` fica ausente e `seed` fica `0`, o valor de ausência que o campo tem desde a 1.0 — torná-lo `omitempty` mudaria os bytes de todos os turnos. O nome do provider é saneado no adaptador (sem caracteres não imprimíveis, tecto de 256 bytes). Testes: `TestAOS396_PelaAPI_ManifestoEChatComOModeloDoNo` (cliente injectado com `Config.ModelID`, pela API), `TestAOS396_ArranquePorAmbienteDeclaraOModeloDoGateway` (o fio de ambiente), `TestAOS396_TranslateResponsePassaOModeloServido` e `TestAOS396_ModeloServidoESaneado` (o adaptador real). A cadeia com o adaptador real **e** o nó juntos fica para a evidência de sistema abaixo.)*
- [x] Com o modelo de referência, o `model_id` identifica-o como tal (valor estável e declarado), para não voltar a ficar vazio em nenhum nó. *(`ReferenceModelID = "aos-reference-model"` (`bootstrap.go`), nos dois campos. Sem `Config.Model` o nó declara-o, mas só preenche um goal sem modelo: embedders e testes que declaram o seu ficam intactos. Com um `Config.Model` injectado sem `Config.ModelID` o nó não declara nada. `TestAOS396_PelaAPI_ModeloDeReferenciaDeclaraOSeuNome`, `TestAOS396_FixarModelo_RegraPorOrigem`.)*
- [x] Teste que falha antes da correcção, pela cadeia real do nó: o payload do `turn.recorded` traz o `model_id` esperado. *(Os dois testes pela API passam por `POST /runs` → `handleSubmit` → `NodeService.hostRun` → runtime → Event Store. **FALHA-ANTES MEDIDA por mutação**: com `fixarModelo` desligado, os dois saem com `ModelID:""`; sem a escrita de `resp.Model` no `recordTurn`, o teste do kernel e o do modelo de referência saem com `ServedModelID:""`.)*
- [x] Teste de replay: uma trajectória gravada com um modelo e reproduzida com outro **diverge** (a verificação de `replay/engine.go` deixa de estar desligada no caminho do nó). *(No mesmo teste do nó, com a execução durável ligada (captura de não-determinismo) e o motor a ler o conteúdo selado com o opener do nó, como o `/reconstruct`: o replay com o modelo gravado não diverge e com outro modelo diverge por `model`. **FALHA-ANTES MEDIDA**: sem a correcção, o replay com o modelo **correcto** já divergia (`model_id=` contra `model_id=gpt-4o-mini`), porque o manifesto vinha vazio. Limite declarado: nenhum caminho de produção do nó corre o replay com modelo esperado; um run gravado antes do AOS-396 tem `model_id` vazio e diverge se lhe passarem um.)*
- [x] Os consumidores do mesmo valor ficam coerentes e verificados: o atributo `gen_ai.request.model` do span `chat` e o `ModelID` que a admissão do turno regista (`model_admission_wiring.go`), cuja origem se confirma durante a implementação. *(Origem confirmada: `callModel` (span `chat`), o span `invoke_agent` e `TurnAdmissionRequest.ModelID` lêem todos o `goal.Model.ModelID` já fixado. `TestAOS396_ManifestoGravaOModeloPedidoEOServido` (kernel) verifica o `invoke_agent`, o `chat` e o `ModelID` da admissão com um espião; o teste do nó verifica os dois spans exportados por OTLP. O `model_admission_wiring.go` só usa o valor na linha de log da negação.)*
- [x] Evidência de sistema: um run real (nó composto com gateway, ou produção) mostra `manifest.model.model_id` preenchido em todos os turnos. É o critério «`model_id` ≠ vazio» do passo 19 do roteiro E2E. *(**VERIFICADO EM PRODUÇÃO** a 2026-09-16 na `v0.1.17` (commit `5e3a672`, imagem `aos-node@sha256:dde06e55…`, deploy às 23:13Z). O run `run-delegado-1789604994`, submetido pelo roteiro E2E (`get-id-token.ps1 -Cunhar agt-e2e-19 -Submeter`), correu `ready → running → complete` entre 23:29:58Z e 23:30:51Z, com 45 eventos: 4 `turn.recorded`, 3 `tool.call.mediated` executadas na sandbox e 4 `replay.captured`. Os quatro turnos (`step-000001` a `step-000004`) gravaram `manifest.model = {"model_id":"gpt-4o-mini","served_model_id":"gpt-4o-mini","seed":0}`, sem `params`. No mesmo WAL, o run `run-delegado-1789569005`, anterior ao deploy, tem `{"model_id":"","seed":0}`. O LiteLLM de produção devolve o alias pedido e não uma versão datada, por isso o pedido e o servido coincidem neste provider. Leitura: cópia do `events.wal` do volume `aos_aos-data` aberta com `eventstore.OpenReadOnly`. Esta evidência cobre também a cadeia adaptador real + nó, que os testes só provavam separadamente.)*
- [x] `tecnica/13_Modelo_Dados_Eventos.md` descreve de onde vem o `model_id` no nó e a regra para `seed`/`params`. *(§6.1, «De onde vem o modelo» e «Regra de `params` e `seed`»; também o `schema_version` que não sobe e o saneamento do nome do provider. Actualizados na mesma linha `tecnica/02`, `tecnica/19`, o `README` do agent-runtime, a linha de `AOS_MODEL_NAME` no `deploy/node/README.md` e a regra do roteiro E2E.)*

### Estado

**IMPLEMENTADO e VALIDADO EM PRODUÇÃO (2026-09-16).** O `turn.recorded` grava o modelo pedido em `model_id` e o servido em `served_model_id`; o nó escreve o modelo no goal de cada run que hospeda, e o replay volta a verificar o modelo. Verificado: suites `-race` verdes no agent-runtime, no módulo do GW, em `integration` e em `cmd/aos`; `build`, `lint`, `layer-lint`, `replay` e `event-catalog` verdes; falha-antes medida por mutação no nó, no kernel e no replay. Revisão adversarial independente: nenhum crítico ou alto; os achados foram corrigidos. **VALIDADO EM PRODUÇÃO (2026-09-16, v0.1.17)**: os quatro turnos do run `run-delegado-1789604994` gravaram `model_id` e `served_model_id` `gpt-4o-mini`, contra `model_id` vazio no run anterior ao deploy.

---

## AOS-411 — A re-varredura de órfãos exclui os runs vivos antes de os reconstituir

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-253 (crash-resume e a re-varredura periódica A4) — fechado |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/crash_resume.go` (`resumeInterruptedRuns`, `crashResumeBanner`), `packages/cmd/aos/orphan_sweeper.go` (`StartOrphanSweeper`), `packages/cmd/aos/resume.go` (`replayPlanFor`), `packages/cmd/aos/service.go` (`submit`, o registo `s.runs`) |

### Contexto

Observado em produção a 2026-09-18, na `v0.1.22`, durante a evidência do AOS-407. O run
`run-delegado-1789775725` foi submetido e **terminou bem** (`ready → running → complete`, 5 tool
calls executadas). Enquanto corria, o nó escreveu:

```
crash-resume: capturas do run "run-delegado-1789775725" ILEGIVEIS — NAO retomado (fail-closed): replay: trajectória vazia (sem turn.recorded)
crash-resume / varredura de arranque (AOS-253): CORREU sobre 192 stream(s) — 1 run(s) orfaos em `running` … 0 RETOMADO(s) …
```

O contentor não tinha reiniciado (`restarts=0`, arranque às 22:19; as linhas são das 22:55). Quem as
escreveu foi a **re-varredura periódica** (`StartOrphanSweeper`, a cada TTL de lease), que usa o
mesmo banner da varredura de arranque sempre que encontra alguma coisa.

Confirmado no código:

- `resumeInterruptedRuns` classifica como órfão todo o stream cujo estado durável é `running`
  (`crash_resume.go`, passo 1). Um run **vivo** — hospedado por esta réplica, ou com lease vivo noutra
  — está nesse estado durante toda a execução.
- Para esse run, a varredura reconstrói o cursor (passo 2), lê o registo de retoma (passo 3) e
  **decifra as capturas por-titular** com o `ReconstructResumable` (passo 4, `replayPlanFor`) — tudo
  ANTES de saber se o run está vivo.
- Só no passo 5 (`submit`) é que o registo em memória `s.runs` devolve `ErrRunAlreadyInProgress`, ou
  o `TryAcquire` do lease salta um run de outra réplica. É essa guarda, no fim, que impede a dupla
  hospedagem. **Não houve dano**: o run não foi retomado nem estragado.

O que está errado, então, é a ORDEM:

1. **Sinal falso de órfão.** Um run vivo cujo primeiro turno ainda não foi capturado falha no passo 4
   e sai como «capturas ILEGÍVEIS — NÃO retomado (fail-closed)», conta como falha e faz o banner
   anunciar «1 run órfão». Um operador — e foi o caso — lê isso como um crash que não houve.
2. **Trabalho e acesso a PII sem necessidade.** A cada ciclo, para cada run vivo, a varredura decifra
   as capturas por-titular sob a chave do titular, só para depois descobrir que não tinha nada a
   retomar. É o mesmo gate de decifração do read-path soberano, usado sem razão.
3. **Correcção dependente de uma guarda tardia.** A não-duplicação assenta inteira no último passo.
   Qualquer mudança futura na ordem ou no `submit` (por exemplo, uma retoma que escreva antes de
   verificar) passa a correr sobre runs vivos.
4. **O banner mente sobre a origem.** A re-varredura periódica anuncia-se como «varredura de
   arranque».

### Objectivo

A varredura (de arranque e periódica) exclui, **antes** de reconstituir qualquer coisa, os runs que
estão vivos — hospedados por esta réplica ou com lease vivo noutra —, e só conta como órfão o que
não tem dono.

### Critérios de Aceitação

- [x] Um run hospedado por esta réplica (`s.runs`) é saltado sem ler cursor, registo de retoma nem
      capturas, e sem contar como órfão nem como falha. Teste com um run a correr e o varredor
      chamado a meio: nenhuma decifração de capturas, banner sem órfãos. **FALHA-ANTES:** hoje o teste
      vê a linha «capturas ILEGÍVEIS» (antes do 1.º turno) ou a leitura das capturas (depois dele).
      *(Guarda (1-bis) em `crash_resume.go`, antes do passo 2: `hospedadoNestaReplica` lê o registo
      de em-curso sob o MESMO mutex que o `submit` usa. `TestAOS411_RunVivoNestaReplicaNaoEOrfao`
      hospeda um run real, prende-o no 1.º turno por canal, verifica a precondição (`running`
      durável + presente em `s.runs`) e exige `orfaos=0` e um log SEM as palavras `ILEGIVEIS`,
      `orfao` e `crash-resume`. **FALHA-ANTES MEDIDA** contra o ficheiro da base: `orfaos=1` e a
      saída reproduz o incidente — «capturas do run "run-411-vivo-aqui" ILEGIVEIS — NAO retomado
      (fail-closed): replay: trajectória vazia (sem turn.recorded)».)*
- [x] Um run com lease vivo noutra réplica é saltado pelo MESMO critério antes do passo 2, e contado
      à parte («vivo noutra réplica»), como hoje o passo 5 já distingue. A verificação do lease no
      `submit` mantém-se como defesa em profundidade.
      *(`leaseAindaVivo` lê `durable.LeaseManager.CurrentLeaseExpired` — o predicado que faz o
      `Claim` devolver `ErrLeaseHeld` —, sem mintar, renovar ou mutar nada; o `submit` e o seu
      `TryAcquire` ficam intactos, e `heldElsewhere` continua a contar à parte o que a defesa em
      profundidade ainda apanhar. `TestAOS411_LeaseVivoNoutraReplicaNaoEOrfao` põe uma segunda
      autoridade de lease (`replica-B`) a reclamar o run sobre o mesmo log e o mesmo relógio manual,
      com registo de retoma presente de propósito — para a varredura antiga chegar mesmo às
      capturas — e exige `orfaos=0`, banner com «0 run(s) orfaos» e «1 com LEASE VIVO noutra
      replica». **FALHA-ANTES MEDIDA**: `orfaos=1` e «capturas ... ILEGIVEIS».)*
- [x] Um órfão verdadeiro (lease expirado, sem dono) continua a ser retomado exactamente como hoje:
      os testes do AOS-253 e do A4 (`aos253_crash_resume_test.go`, `aos_a4_revarredura_test.go`)
      ficam verdes sem alteração de asserções.
      *(Nenhum dos dois ficheiros foi tocado — a assinatura de `crashResumeBanner` e o texto da
      linha de ARRANQUE ficaram iguais. `TestAOS253_CrashResumeScanCompletesWithoutDoubleExecution`,
      `TestAOS253_CrashResumeBannerDeclaresResult` e `TestA4_ReVarreduraRetomaOrfaoSemSegundoArranque`
      verdes. Acresce `TestAOS411_OrfaoVerdadeiroContinuaAVerSeOLeaseExpirou`: uma réplica reclama o
      lease, o relógio MANUAL avança um TTL, e o run volta a contar como órfão — guarda que passa
      dos dois lados da correcção, de propósito, porque o que ela mede é a ausência de regressão.)*
- [x] O banner distingue a origem da passagem — varredura de ARRANQUE ou RE-VARREDURA periódica — e
      um ciclo periódico sem órfãos continua silencioso.
      *(`crashResumeBannerDaPassagem` escolhe a origem pelo `anuncia` que já era passado; a periódica
      diz «RE-VARREDURA periodica (AOS-253/A4)» e a de arranque mantém a forma anterior, que é a
      pegada declarada do roteiro E2E (`docs/testing/e2e-pegadas-visao-19.md`). O silêncio é agora
      REAL e não só por acaso: `scanned` conta órfãos verdadeiros, pelo que o ciclo que só encontrou
      runs vivos não escreve nada. `TestAOS411_BannerDistingueAOrigemDaPassagem` (**FALHA-ANTES
      MEDIDA**: a passagem periódica anunciava-se «varredura de arranque») e
      `TestAOS411_CicloPeriodicoSemOrfaosContinuaMudo`.)*
- [x] Evidência de sistema: um run real em produção atravessa pelo menos um ciclo da re-varredura
      sem produzir linhas de crash-resume.
      *(**VERIFICADO em produção a 2026-09-21** (`v0.1.28`). O run `run-delegado-1789995086`
      esteve vivo durante DUAS passagens e foi saltado nas duas:
      `aos_orphan_live_skipped_total{dono="esta_replica"} 2`, sem nenhuma linha «capturas
      ILEGIVEIS» — o sintoma exacto do incidente de 2026-09-18. A prova é POSITIVA, e não a
      ausência de um sintoma: foi preciso o **AOS-422** para a tornar observável, porque a
      passagem periódica só escreve no log quando encontra órfãos verdadeiros, e este ticket fez
      um run vivo deixar de o ser. Ver o bloco de Estado do AOS-422 para a medição inteira,
      incluindo por que razão foi preciso baixar a cadência do varredor para a apanhar.)*

### Fora de âmbito

A política da retoma em si (o que se reproduz, a credencial vazia, o replay-then-continue) não muda.

### Estado

**IMPLEMENTADO (2026-09-20), COM A EVIDÊNCIA DE PRODUÇÃO POR RECOLHER.** A varredura pergunta pelo
DONO antes de reconstituir o que quer que seja: um run hospedado por esta réplica, ou com lease ainda
válido noutra, é saltado sem se lhe ler cursor, registo de retoma nem capturas por-titular, não conta
como órfão e não conta como falha. O `submit` fica onde estava, como defesa em profundidade, e nenhum
lease é reclamado mais cedo do que era — a guarda lê o MESMO predicado que o `Claim` usa. `scanned`
passa a contar órfãos VERDADEIROS, o que devolve o silêncio ao ciclo periódico que só encontra runs a
correr, e o banner passa a nomear a passagem. Verificado: suite `-race` verde em `cmd/aos` (incluindo
AOS-253 e A4, sem tocar nos seus ficheiros); `layer-lint`, `rtm`, `ref-lint`, `deferrals` e
`estado-citado` verdes. **FALHA-ANTES MEDIDA** contra o ficheiro da base: três dos cinco testes novos
falham, e a saída do primeiro reproduz o incidente de produção à letra. **FICA POR FAZER**: a
evidência de sistema (5.º critério), que precisa de um run real a atravessar um ciclo da re-varredura
em produção.

---

## AOS-419 — As esperas não-humanas não tinham prazo nenhum (backstop de wall-clock)

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Remediação pós-auditoria |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-017 (máquina de estados durável) — fechado; AOS-252 (varrimento de deadlines, o chamador de `CheckDeadlines`) — fechado |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/governance/REGISTO-Deferimentos.md` (DEF-906), `analises/09_Auditoria_RT_RM_Adversarial.md` §3.2, `tecnica/02_Agent_Runtime_Execucao_Duravel.md` §5.1, `tecnica/08_Observabilidade_Evals.md` §6, `packages/kernel/agent-runtime/state/machine.go` (`CheckDeadlines`), `packages/kernel/agent-runtime/state/transitions.go` (`validTransitions`), `packages/kernel/agent-runtime/liveness/doc.go`, `packages/cmd/aos/deadline_sweeper.go` |

### Contexto

O deferimento **DEF-906** declara, medido: com `humanTTL` e `wallClock` ligados e o relógio
injectado avançado dez anos, `state.Machine.CheckDeadlines` **não transita** um run em
`paused` nem em `waiting_on_tool`. Confirmado no código antes de se lhe tocar:

- o `switch` de `CheckDeadlines` (`state/machine.go`) tinha exactamente **dois** ramos —
  `waiting_on_human` (→ `killed`, ADR-013) e `running` (→ `timed_out`). As duas esperas
  NÃO-humanas caíam no `default` implícito e devolviam `(estado, false, nil)`;
- e não havia sequer **para onde** as transitar: a tabela declarativa de AOS-017 dava a
  `waiting_on_tool` e a `paused` uma única saída, `→ running`. Sem aresta para um terminal,
  o backstop não era uma linha esquecida no `switch` — era uma transição que não existia.

A segunda via, que `tecnica/08` §6 designava como rede de segurança, também não cobre: a
guarda de entrada do disjuntor (`breaker.Observe` → `liveness.CountsAsActiveWork`, que só
admite `running`) devolve cedo, pelo que o sinal wall-clock **absoluto** é recolhido e nunca
avaliado nestes estados — e, mesmo que fosse, o disjuntor só é observado na fronteira de
fim-de-turno, que um run suspenso nunca alcança.

O efeito é o que o registo diz: **um run em `paused` ou `waiting_on_tool` pode ficar
pendurado sem prazo que o feche** — uma activity externa que nunca responde, ou um steer
aceite e esquecido, ficam suspensos indefinidamente, com o lease largado e sem terminal no
log durável.

**Porquê um ticket NOVO.** O eixo declarado do DEF-906 cita AOS-080 e AOS-017, ambos
ENTREGUES — um eixo fechado é operacionalmente indistinguível de não ter eixo (§1 do
registo). Este ticket é o eixo novo, pelo precedente do DEF-274/275, reapontados para
AOS-281 em vez de ficarem presos a um ticket fechado.

### Objectivo

As duas esperas NÃO-humanas passam a ter prazo: excedido o tecto de wall-clock, o run
transita **duravelmente** para `timed_out` (fail-closed), com razão de auditoria distinta da
do `running`. `waiting_on_human` fica intacto — a deliberação humana tem prazo próprio
(ADR-013) e não morre pelo tecto de máquina.

### Decisões tomadas (e porquê)

1. **O prazo é o tecto que já existe** (`state.WithRunWallClock`, alimentado por
   `AOS_BREAKER_MAX_WALL_CLOCK`), não um valor novo. Um segundo tecto seria mais uma variável
   de ambiente para o operador esquecer — e o ticket seria uma opção dormente. Assim, todo o
   nó que já configura o tecto fica com o backstop **armado**, sem wiring novo.
2. **O destino é `timed_out`, não `killed`.** É o terminal que `tecnica/08` §6 nomeia para o
   sinal wall-clock absoluto, e `killed` é a saída de política/gate humano (ADR-013).
3. **A razão é distinta:** `suspension_wall_clock_exceeded` e não `wall_clock_exceeded`. O
   tecto é o mesmo, mas «morreu a trabalhar» e «morreu pendurado numa espera» são incidentes
   diferentes com donos diferentes (o loop vs. a activity externa ou o operador).
4. **Por segmento, como o resto da máquina:** conta desde a ENTRADA no estado, pelo que uma
   espera que retoma a tempo leva o tecto inteiro consigo. O backstop só morde quem lá fica.
5. **`0` continua a desligar** — a semântica dos outros dois prazos. Quem não configurou
   tecto nenhum não ganha um kill novo por actualizar.

### Critérios de Aceitação

- [x] Um run em `waiting_on_tool` além do tecto transita para `timed_out` no varrimento
      seguinte, com a razão `suspension_wall_clock_exceeded`, e o terminal SOBREVIVE a crash
      (reconstrução por replay).
      *(`TestAOS419_BackstopMataWaitingOnToolPendurado`,
      `packages/kernel/agent-runtime/state/aos419_backstop_suspensao_test.go:32`. **FALHA-ANTES
      MEDIDA** contra os ficheiros da base: «no tecto o backstop TEM de disparar:
      s="waiting_on_tool" fired=false err=<nil>».)*
- [x] Idem para `paused`, com a mesma razão e a mesma fronteira INCLUSIVA (`>=`) dos prazos
      já existentes.
      *(`TestAOS419_BackstopMataPausedEsquecido`, `…/aos419_backstop_suspensao_test.go:64`.
      **FALHA-ANTES MEDIDA**: «s="paused" fired=false err=<nil>».)*
- [x] A tabela declarativa ganha EXACTAMENTE as duas arestas (`waiting_on_tool → timed_out`,
      `paused → timed_out`) e nenhuma outra: cada uma das duas esperas fica com duas saídas
      (`running`, `timed_out`), e a matriz 10×10 é re-varrida contra um oráculo escrito à mão
      (15 válidos, 85 inválidos).
      *(`TestAOS419_ArestasDeBackstopNaTabela` (`…:89`) e o oráculo de
      `transitions_test.go:29` (`TestTransitionMatrix10x10`, `TestTableMatchesOracle`).
      **FALHA-ANTES MEDIDA**: «"waiting_on_tool"→timed_out TEM de ser válida» e
      «"paused" deve ter exactamente 2 saídas (running, timed_out), tem [running]».)*
- [x] `waiting_on_human` NÃO é afectado: com o tecto configurado e sem TTL, uma deliberação
      de 24 h não mata o run; com TTL, continua a ir para `killed` (ADR-013), nunca para
      `timed_out`.
      *(`TestAOS419_DeliberacaoHumanaNaoMorrePeloTectoDeMaquina` (`…:195`) — **CONTROLO
      NEGATIVO declarado**: passa dos dois lados da correcção, porque o que mede é a ausência
      de regressão na fronteira que AOS-263 fixou.)*
- [x] O backstop é fail-closed como as restantes transições: com o Event Store a recusar, o
      estado NÃO avança (persistido nem in-memory) e o erro sobe para quem varre, que
      re-tenta no tick seguinte.
      *(`TestAOS419_BackstopFailClosedNaFalhaDoEventStore` (`…:147`). **FALHA-ANTES MEDIDA**:
      «err=<nil>; quero o erro do Event Store (fail-closed, não engolido)».)*
- [x] O tecto é POR SEGMENTO no lado da espera: cinco ciclos de espera-e-retoma dentro do
      tecto atravessam mais de sete minutos com um tecto de um minuto e o run fica vivo.
      *(`TestAOS419_RetomaLevaOTectoInteiro` (`…:113`) e `TestAOS419_SemTectoNaoHaBackstop`
      (`…:179`) — ambos **controlos** que passam dos dois lados: o primeiro guarda a
      semântica por-segmento, o segundo a compatibilidade de quem não configurou tecto.)*
- [x] O corpus deixa de afirmar o contrário: `tecnica/02` §5.1 (diagrama, tabela e contagem),
      `tecnica/00` (diagrama), `tecnica/19` (tabela derivada), `tecnica/08` §6 (o contrato do
      backstop), `state/doc.go`, `state/README.md` e `liveness/doc.go` — que declarava
      explicitamente «sem backstop». RTM regenerada.
      *(Verificado pelos gates `rtm`, `ref-lint`, `deferrals` e `estado-citado`.)*
- [ ] ALCANCE no nó: um run que se suspende e larga a posse SAI do registo de em-curso
      (`s.runs`) e fecha o gate, pelo que o varrimento de AOS-252 deixa de lhe tocar. O
      backstop vem armado na máquina (o nó já abre as máquinas com o tecto), mas no nó de hoje
      só alcança um run que suspenda e FIQUE hospedado.
      *(**NÃO VERIFICADO — fica por fazer.** Fechá-lo é varrer os streams SUSPENSOS, no molde
      do `approval_sweeper` (um varrimento durável, não mais uma opção na máquina), e não cabe
      neste ticket: é trabalho no `packages/cmd/aos` com registo próprio de suspensos. O que
      se mede quando for feito: um run pausado e largado pelo nó a transitar para `timed_out`
      num tick do varrimento, com `reason=suspension_wall_clock_exceeded` no log durável.)*

### Fora de âmbito

O timeout da *activity* externa (AOS-018) — primeira linha de `waiting_on_tool` — não muda. O
disjuntor de EPIC-08 não é tocado: continua a medir TRABALHO ACTIVO e a ser *no-op* fora de
`running`, que é o desenho de AOS-019 e a razão pela qual não podia ser ele a fechar isto.

### Estado

**IMPLEMENTADO** (2026-09-20), **com efeito prático NULO hoje** — e isso é o mais importante
deste bloco.

**O que foi entregue.** `paused` e `waiting_on_tool` não tinham saída para terminal nenhum na
tabela canónica: não era um ramo esquecido no `switch`, era uma transição que não existia, e por
isso um run suspenso não tinha para onde ser morto. A tabela passou de 13 para 15 pares
(matriz 10×10 re-varrida: 15 válidos / 85 inválidos), e o `CheckDeadlines` ganhou o backstop de
`waiting_on_tool` com razão de auditoria própria (`suspension_wall_clock_exceeded`), fail-closed
como os restantes.

**O que a revisão adversarial independente mediu, e que muda a leitura do ticket:**

| Achado | Consequência |
|---|---|
| **Nenhum código de produção transita um run para `waiting_on_tool`** — as duas ocorrências fora de testes são *switches de classificação*, não transições | A aresta que ganhou disparo automático **nunca pode disparar hoje** |
| **Um run que se suspende sai do registo de em-curso** (`finish` faz `delete(s.runs, …)`) e o varrimento exige lá estar | A janela em que um `paused` é varrido é de microssegundos, contra um tecto de 30 min |
| **O default do NÓ não é 0, é 30 minutos** (`DefaultBreakerMaxWallClock`), e vai direito ao `WithRunWallClock` | «Quem não configurou não ganha kill novo» era falso ao nível do produto |
| **`paused` tem dois produtores, e um é humano** (`steer_graceful_pause` vs `budget_breaker_tripped`), com razões duráveis distintas que a máquina **não retém** | Um `/pause` de operador morreria pelo tecto do TRABALHO, irrecuperável (`timed_out` é absorvente) |

**A decisão que o último achado forçou.** O `paused` foi **retirado do backstop automático**. É
palavra por palavra o argumento com que este ticket isenta o `waiting_on_human`: a deliberação
humana não paga o tecto da máquina. A aresta fica na tabela — a ausência de saída terminal era o
defeito estrutural —, o disparo não. Estender o backstop a `paused` exige reter a razão de entrada
e isentar a pausa humana, e essa é **decisão do dono**, não deste ticket.
`TestAOS419_PausaNaoMorrePeloTectoDoTrabalho` passou a guardar isso.

**Honestamente: o que isto vale hoje.** Fecha um defeito estrutural da tabela e deixa o mecanismo
pronto. **Não** fecha nenhum caso observável em produção, porque os dois estados que nomeia ou não
são entrados, ou saem do alcance do varrimento antes de o prazo correr. Por isso o DEF-906 fica
**MITIGADO** e não fechado, e o último critério fica por marcar.

- [ ] **Alcance:** o varrimento alcançar um run suspenso que já largou a posse (molde do
      `approval_sweeper`, varrendo streams em vez do registo em memória). É este trabalho, e não
      mais mecanismo, que dá ao backstop um caso real. **POR FAZER**, em `cmd/aos`.

**Nota de âmbito, declarada:** este diff **não altera uma única linha de código de produção** em
`packages/cmd/aos` — as edições de `deadline_sweeper.go` e `steer_gates.go` são de comentário e de
uma linha de log que afirmava o que não sabia («o run estava preso a meio de um turno», que é falso
para um disparo do backstop).


## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

## AOS-422 — A guarda do AOS-411 passa a ter prova: os runs vivos saltados vão ao `/metrics`

<!-- rtm: adrs-mencionados -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Observabilidade da remediação |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-411 (a guarda), AOS-253/A4 (o varredor) |
| Bloqueia | A verificação em produção do AOS-411 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/crash_resume.go` (`resumeInterruptedRuns`), `packages/cmd/aos/api.go` (`handleMetrics`), `packages/cmd/aos/orphan_sweeper.go` |

### Contexto

**O AOS-411 tornou a sua própria evidência inobservável**, e isso foi medido ao tentar
verificá-lo em produção.

A correcção fez um run VIVO deixar de contar como órfão. Isso calou o ruído certo — a passagem
periódica que só encontrava runs a correr deixou de escrever no log —, mas o `if` que a cala é
`anuncia || scanned > 0`, e `scanned` conta agora **órfãos verdadeiros**. Logo, no caso que
interessa — varredura passa, encontra um run vivo, salta-o correctamente — **o varredor não
escreve nada**. E os contadores `vivosAqui`/`vivosNoutra` eram variáveis locais da função.

O `aos_orphan_sweeps_total` já prova que o varredor CORREU. O que faltava era provar o que ele
**saltou** — a diferença entre «correu e não havia nada» e «correu e protegeu um run a
trabalhar».

Sem isto, a verificação do AOS-411 em produção só produz **evidência negativa**: a ausência da
linha «capturas ILEGIVEIS», que é o sintoma do incidente de 2026-09-18. Ausência de sintoma num
run é mais fraco do que aquilo a que este repositório chama verificado.

### Objectivo

Um run vivo saltado pela varredura é visível no `/metrics`, sem depender de uma linha de log que,
no caso que interessa, não é escrita.

### Critérios de aceitação

- [x] `aos_orphan_live_skipped_total` existe, com a label `dono` a separar `esta_replica` de
      `outra_replica` (`TestAOS422_OsVivosSaltadosChegamAoMetrics`).
- [x] A série está **ausente** antes da primeira passagem do varredor — um `0` num nó que nunca
      varreu leria-se como «varreu e não havia nada», que é a mentira simétrica
      (`TestAOS422_SerieAusenteAntesDaPrimeiraPassagem`). É a mesma regra que o bloco dos
      varredores já impõe.
- [x] Depois da primeira passagem, `0` é um zero **verdadeiro** e conta como amostra
      (`TestAOS422_ZeroDepoisDaPrimeiraPassagemEUmZeroVerdadeiro`).
- [x] Uma família, duas amostras: `# HELP`/`# TYPE` **uma** vez
      (`TestAOS422_UmaFamiliaDuasAmostras`) — dois blocos para o mesmo nome fazem o Prometheus
      rejeitar o payload INTEIRO, não só a família.
- [x] Verificado em produção: com um run vivo, o `/metrics` mostrou
      `aos_orphan_live_skipped_total{dono="esta_replica"} 2` *(ver abaixo)*. Fecha também a
      verificação do AOS-411.

### O que este ticket NÃO faz, e porquê

**Não muda quando o varredor fala.** Baixar a condição de silêncio traria de volta o ruído que o
AOS-411 calou de propósito. O log é para acontecimentos; um facto contínuo — «a guarda protegeu N
runs» — pertence ao `/metrics`. Separar os dois é a razão de este ticket existir.

### Estado

**FEITO.**

**Verificado em produção a 2026-09-21** (`v0.1.28`, imagem `sha256:0b49dd71…`), em duas medições
que provam coisas diferentes.

**1. A regra da ausência, medida sem nada de especial.** Com o nó a 58 segundos de vida, o
`/metrics` **não tinha nenhuma família `aos_orphan*`** — nem sequer o `aos_orphan_sweeps_total`,
que já existia antes deste ticket. Aos 4 minutos, com duas passagens feitas:

```console
aos_orphan_sweeps_total 2
aos_orphan_last_sweep_age_seconds 10.6
aos_orphan_live_skipped_total{dono="esta_replica"} 0
aos_orphan_live_skipped_total{dono="outra_replica"} 0
```

O mesmo `0` significa coisas opostas nos dois momentos, e agora distinguem-se: antes era
**ausência de dados**, depois é um **facto**. Era exactamente esta ambiguidade que impedia a
verificação do AOS-411.

**2. A guarda do AOS-411 a actuar, e isto exigiu mudar a cadência.** Um run real
(`run-delegado-1789995086`, submetido com NHI cunhado pelo operador) vive **segundos**; o varredor
passa a cada **120**. A primeira tentativa não cruzou os dois e a métrica ficou a `0` — o que
**não** prova que a guarda falhou, só que não foi exercitada, e ficou dito como tal antes de se
tentar outra vez.

Baixou-se `AOS_CRASH_RESUME_INTERVAL` para `10s` **temporariamente**, com confirmação pelo banner
(`LIGADA a cada 10s`) antes de medir — uma medição com a cadência antiga voltaria a dar zero e não
se saberia porquê. Com a cadência baixa:

```console
aos_orphan_sweeps_total 17
aos_orphan_live_skipped_total{dono="esta_replica"} 2
aos_orphan_live_skipped_total{dono="outra_replica"} 0
```

**A guarda actuou duas vezes.** O run esteve vivo durante duas passagens e foi SALTADO nas duas:
não foi tratado como órfão, não lhe foram lidas as capturas por-titular, e **não apareceu nenhuma
linha «capturas ILEGIVEIS»** — que é o sintoma exacto do incidente de 2026-09-18, onde um run vivo
o produziu com o contentor a `restarts=0`.

A cadência foi **reposta** e confirmada pelo banner (`a cada 2m0s`); o `.env` não ficou com a
variável.

**Um falso positivo apanhado na leitura, e vale a pena ficar escrito:** a primeira varredura do
sintoma acusou uma ocorrência. Era o banner da varredura de ARRANQUE a dizer `0 run(s) orfaos em
`running``, que o padrão apanhava pelo texto. Sem olhar para a linha, teria sido reportado um
sintoma que não existia.

Segue o molde que já existe: campo `atomic.Int64` no `NodeService`, incrementado no ponto de
agregação da passagem, lido em `handleMetrics`. O nó **não tem** registo de métricas nem
Prometheus — o `/metrics` é texto gerado à mão —, e este ticket não introduziu nenhum.

**Uma correcção ao diagnóstico inicial, que estreitou o ticket:** eu tinha afirmado que o silêncio
não distinguia «correu e saltou bem» de «não correu». Metade disso estava errado — o
`aos_orphan_sweeps_total` já distinguia. O que faltava era só a prova do que foi saltado.

---

## AOS-454 — A via durável perde o `parent_step_id` do evento de mediação

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Remediação (achado da fase 1 do AOS-069) |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-021 (a porta `ActivityDispatcher` e o `DurableDispatcher`), AOS-157 (o loop despacha pela porta) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/integration/runtime_ports.go` (`DurableDispatcher.Dispatch`), `packages/kernel/agent-runtime/activity/contract.go` (`Activity`, `toCall`), `packages/kernel/reference-monitor/eventsink.go` (`MediationRecord`) |

### Contexto

**Achado no diagnóstico da fase 1 do AOS-069 (2026-09-26).** O `DurableDispatcher` traduz o
`referencemonitor.Call` que o loop constrói numa `activity.Activity`, e `Activity.toCall` volta a
construir o `Call` que o Reference Monitor medeia. A `Activity` não tinha campo para o passo pai:
o `Call` re-hidratado chegava ao RM com `ParentStepID` vazio.

Num nó com `AOS_DURABLE_EXECUTION=1` — **produção** — o evento `tool.call.mediated` saía sem
`parent_step_id`. A via directa (o default do kernel, a de quase todos os testes) grava-o. Perde-se
a ligação de auditoria entre a tool call e o turno que a pediu; **não é autorização** — nenhum hook
decide pelo passo pai.

**É a quarta vez que a mesma tradução perde um campo do `Call`:** o `Credential` (AOS-152), a
`ApprovalEvidence` (AOS-021), o taint da autorização (AOS-069) e agora o passo pai. Cada correcção
acrescentou o campo que faltava e nenhuma fixou a propriedade. Este ticket fixa-a.

### Objectivo

O `parent_step_id` do evento de mediação é o mesmo nas duas implementações da porta, e qualquer
campo novo do `Call` é propagado pela via durável ou declarado perdido, com razão.

### Critérios de aceitação

- [x] Paridade medida no evento gravado no Event Store: o `parent_step_id` do `tool.call.mediated`
      é igual pela via directa e pela durável, e não-vazio
      (`TestAOS454_ViaDuravelPreservaOParentStepIDNoEvento`, `packages/integration`). **Vermelho na
      base 9fd4c87**: via directa `step-000001`, via durável vazio.
- [x] `Activity.ParentStepID` chega ao evento de mediação
      (`TestAOS454_ParentStepIDChegaAoEventoDeMediacao`, `activity`).
- [x] O passo pai **não** entra na idempotency key nem na impressão da acção: o mesmo
      `(RunID, StepID)` com outro pai deduplica e não re-executa
      (`TestAOS454_ParentStepIDForaDaChaveDeIdempotencia`).
- [x] Auditoria campo a campo `Call → Activity → toCall` por reflexão: um `Call` com **todos** os
      campos exportados preenchidos passa pelo `DurableDispatcher`, e cada campo chega igual ao RM
      ou está declarado em `camposNaoPropagados` com a razão — e, se declarado, tem de chegar
      diferente (`TestAOS454_AuditoriaCampoACampoCallActivityCall`). Um campo novo no `Call` que
      ninguém propague avermelha este teste.
- [x] Verificado em produção: num nó durável, um `tool.call.mediated` de um run novo traz
      `parent_step_id` *(ver Estado)*.

### Auditoria campo a campo

Medida pelo teste de reflexão, não lida no código. Na base, só o `ParentStepID` se perdia para
além do que fica declarado:

| Campo do `Call` | Via durável | Razão |
|---|---|---|
| `RunID`, `StepID`, `ToolID`, `Capability`, `Resource`, `Principal`, `Credential`, `Input`, `ApprovalEvidence` | propagado | — |
| `Context.BudgetTokensRemaining`, `Context.Reversibility`, `Context.Sensitivity` | propagado | — |
| `ParentStepID` | **perdido → propagado (este ticket)** | — |
| `Context.Taint` | traduzido, não copiado | Correcção do AOS-069 (fase 1): `Activity.AuthorizationTaint` com `taint.ParseLabel`, fail-closed. Tem teste próprio. Na base 9fd4c87 era fixado em untrusted; fundido no #394. |
| `RequestID` | perdido — **latente** | Nenhum chamador o preenche: o loop não põe `RequestID` na `Call`, e o `DurableDispatcher` só é chamado pelo loop. Se passar a ter produtor, o teste de paridade não o apanha, mas a declaração tem de ser revista. |
| `Context.RiskClass`, `Context.RiskApprover`, `Context.RiskDecisionMode` | descartado — **correcto** | São saídas do `RiskGate`, escritas dentro de `Mediate`. Deixá-las atravessar vindas do chamador permitiria pré-preencher a atribuição (`RiskApprover`) de uma acção que nenhum humano aprovou, num RM sem `RiskGate` na cadeia. |
| `humanApproved` (não exportado) | não aplicável | Só o `ApprovalGate` o escreve, dentro do RM; nenhum chamador o consegue passar em nenhuma das vias. |

### Coordenação com o AOS-069 (fase 1)

A correcção do taint na via durável (`Activity.AuthorizationTaint`) estava por fundir quando este
ticket foi feito, e toca os mesmos dois ficheiros. Os hunks deste ticket ficam longe dos dela (o
campo a seguir a `StepID`; os dela no fim da `Activity` e no `Taint` do `toCall`). Verificado:
`git merge-file` de cada ficheiro (base × AOS-454 × AOS-069) sai com **zero conflitos**, e na árvore
integrada as suites de `activity` e os testes `TestAOS069_*`, `TestAOS454_*` e
`TestDurableDispatcher_*` de `packages/integration` passam juntos.

O AOS-069 fundiu-se primeiro (#394). O merge da base neste ramo confirmou a previsão: o código
fundiu sem conflito, e o único conflito foi a RTM gerada (`tecnica/16`), resolvido regenerando-a.

### O que este ticket NÃO faz, e porquê

**Não propaga o `RequestID`.** Sem produtor, propagá-lo seria código sem caso. Fica declarado
como perda latente na tabela e no teste.

**Não mexe no taint.** É do AOS-069.

### Estado

**FEITO.**

**Verificado em produção a 2026-09-27** (`v0.1.39`, imagem `sha256:95e41051…`, o digest que o
`publish` da release anunciou), com `AOS_DURABLE_EXECUTION=1` no contentor e o banner da execução
durável (AOS-180) `LIGADA`. As contagens são do `events.wal`, lido só em leitura.

**Antes do deploy (v0.1.38) — o defeito vivo.** 61 eventos de mediação (51 `tool.call.mediated`,
10 `tool.call.denied`), **nenhum** com `parent_step_id`. Contar ausências num WAL codificado não
chega sozinho: a chave podia simplesmente não se gravar em texto. O controlo foi contá-la noutros
tipos — aparece em 1281 eventos (`step.checkpoint`, `replay.captured`, `sandbox.*`). A ausência é
do evento de mediação, não da codificação.

**Depois do deploy — um run novo.** Um plano de prova pela fila real
(`medir-latencia-fila.sh --ate-ao-fim`, `plan-e2e-447-1790511800`, `terminal`, `exit_code 0`) cujo
nó `n1` chamou `doc_read` duas vezes:

```console
tool.call.mediated  run=plan-e2e-447-1790511800~n1  step=step-000001-tool-1  parent_step_id=step-000001
tool.call.denied    run=plan-e2e-447-1790511800~n1  step=step-000002-tool-1  parent_step_id=step-000002
```

As duas mediações do run trazem o passo pai, e cada uma aponta para o **seu** turno. Os 61 eventos
anteriores continuam sem ele, porque o WAL é append-only. O total passou a 52 `mediated` e 11
`denied`, exactamente um com `parent_step_id` em cada tipo — os deste run.

---

## AOS-485 — A recusa de uma tool call pela lista-branca do run não deixa evento nem selo

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: dá pegada a uma recusa que o ADR-027 descreve como imposta pelo RM e que hoje acontece antes dele. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 |
| Fase | Prontidão para utilizadores reais |
| Tipo | fix |
| Prioridade | P2: falha do lado seguro (nada é despachado), mas uma tool call negada fica fora do log, do WORM e das métricas, e só se reconstitui por inferência |
| Estimativa | M |
| Dependências | AOS-413 (lista-branca do run), AOS-454 (`parent_step_id` na mediação), AOS-478 (producer do envelope), AOS-481 (`step_id` dos desfechos) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/kernel/agent-runtime/loop.go` (`toolPermitidaNoRun`, `CodeToolOutsideRunAllowlist`), `packages/kernel/reference-monitor/eventsink.go`, `packages/cmd/aos/api.go` (`POST /runs`, campo `tools`), `docs/reports/e2e-plano-multi-no-prod-2026-10-02.md` |

### Contexto

Medido em produção a 2026-10-02 (imagem `sha256:afb6a95a…`), no run
`plan-e2e-pegadas-1790956072~n2_summarize`, filho de um plano e com a lista-branca vazia.

No turno 1 o modelo pediu `doc_read`: o `turn.recorded` tem `tool_calls_requested=1`. A chamada foi
negada, e o texto final do run diz porquê (`E_TOOL_OUTSIDE_RUN_ALLOWLIST`). Do lado do registo:

| Onde | O que ficou |
|---|---|
| Stream do run (16 eventos) | nenhum `tool.call.*`; a seguir ao `turn.recorded` vêm o checkpoint, a captura e o turno 2 |
| WORM | nenhuma partição de decisão para o run (o `n1`, que teve um permit, tem a sua com 1 selo) |
| `/metrics` | `aos_mediation_denials_total 0`, `aos_mediation_permits_total 1` |

A recusa acontece em `mediateToolCall`, antes da reescrita e da mediação (`loop.go`,
`toolPermitidaNoRun`): devolve um `ToolDenial` com `DeniedBy: "run_tool_allowlist"` que só existe
no tail do prompt. O Reference Monitor nunca vê a chamada, e por isso não há evento, selo nem
contador.

Duas coisas não batem:

- **O ADR-027 diz as duas coisas.** O §2.3 decide que o ciclo do runtime impõe a lista «antes da
  mediação — uma tool fora dela é negada sem chegar ao Reference Monitor», e é o que o código faz.
  O §4 fala da «sua imposição no RM», e o AOS-413 regista a decisão como «lista-branca **imposta
  pelo RM**». Mudar o código é mudar o §2.3, e isso é uma emenda ao ADR.
- **As outras recusas de tool call do RM deixam pegada:** o WORM de produção tem 33 selos `deny`, e
  a recusa por taint do run `plan-e2e-447-1790511800~n1` tem `tool.call.denied` e selo. A da
  lista-branca não se vê. (A recusa por reescrita falhada, `E_EFFECT_REWRITE`, tem a mesma forma e
  fica fora deste ticket.)

Hoje a única forma de saber que houve uma recusa é contar os `tool_calls_requested` de um turno e
reparar que não há `tool.call.*` com esse `parent_step_id`. O texto do modelo que a nomeia está
cifrado por titular.

### Decisão a tomar primeiro (do dono)

- **(A) A lista-branca passa a ser um hook do RM.** A chamada chega ao Reference Monitor, um hook
  nega-a, e a recusa ganha `tool.call.denied`, selo e contador pelo caminho de todas as outras.
  Alinha o código com o texto do ADR-027. Custa: a chamada tem de ser construída como efeito antes
  de ser negada (hoje a recusa vem antes da reescrita, de propósito).
- **(B) O loop grava o seu próprio evento**, sem passar pelo RM. Custa menos, mas cria um segundo
  produtor de `tool.call.denied` fora do Reference Monitor, e o selo fica por decidir.

A recomendação à partida é **(A)**. Selar ou não cada recusa é parte da decisão: um modelo que
insista numa tool negada escreve um selo por turno, limitado pelo tecto de turnos do run.

**Decidido pelo dono (2026-10-02): (A), com emenda ao ADR-027 §2.3.** A lista-branca passa a ser
imposta dentro do Reference Monitor, logo a seguir à identidade (para o evento levar o principal
verificado e a cadeia de delegação), e a recusa sai pelo caminho de todas as outras: evento, selo e
contador. Uma tool fora da lista continua a não ser construída como efeito: a reescrita não corre
para ela.

Efeitos aceites com a decisão:

- **Um selo por recusa**, limitado pelo tecto de turnos do run (e mais um por retoma do passo).
- **A call negada passa a ser vista pelo disjuntor de no-progress** e a entrar no denominador da
  taxa de override da autonomia (`autonomy_fiabilidade.go`), como qualquer outra recusa do RM.
- **A recusa ocupa a chave `run_id:step_id` do sub-passo**, com a mesma classe de colisão que o
  AOS-481 descreve para as outras decisões.

**Precisões medidas na implementação e na revisão adversarial (2026-10-02).** O que a decisão
acima diz por alto, e o que o código faz de facto:

- **O limite dos selos não é o tecto de turnos.** É turnos × tool calls por turno, e não há tecto
  de calls por turno: um modelo que peça N tools negadas num turno escreve N selos, cada um com a
  sua escrita durável no WORM. O limite real é o orçamento do run.
- **O denominador da taxa de override passa a ser inflável por recusas garantidas.** A taxa é
  escaladas ÷ decisões (`autonomy_fiabilidade.go`), e as recusas contam como decisões. Um run com
  a lista vazia produz recusas certas, que baixam a taxa sem dizerem nada sobre a fiabilidade do
  agente. Hoje não tem efeito: o AOS-481 deixa a promoção sem amostras de desfecho e ela nunca
  dispara. Passa a ter quando o AOS-481 fechar, e tem de ser decidido aí.
- **A colisão na chave não é a que o AOS-481 descrevia.** Os critérios dele só cobriam o
  `outcome` e a escalada seguida de decisão. O caso novo é recusa → permit no mesmo `step_id`
  (retoma sem captura, modelo re-interrogado que pede, na mesma posição, uma tool da lista): o
  `tool.call.mediated` é engolido pelo `tool.call.denied` no Event Store, e o WORM fica certo.
  Foi acrescentado aos critérios do AOS-481; não é resolvido aqui.
- **A identidade decide antes da lista.** Uma tool fora da lista pedida com um token expirado, ou
  com uma capability fora do escopo do token, sai `E_DENIED_BY_HOOK` com `denied_by=identity`, e
  não com o código da lista. Antes deste ticket saía sempre com o código da lista. O modelo não
  vê sempre o mesmo texto.
- **O backstop não promete o código.** Num Reference Monitor sem o hook, a recusa vem no fim da
  cadeia e sai com o código da lista só se nenhum hook anterior negar ou escalar primeiro. O que
  o backstop garante é que a tool não executa.
- **Na via durável o step-ledger respondia antes do RM.** Um passo já aplicado sem lista devolvia
  o output memorizado a um despacho do mesmo passo com a lista a negá-lo — um permit sem
  mediação, que este ticket teria introduzido (antes, o ciclo negava antes do dispatcher).
  Corrigido: uma call fora da lista não entra no ledger e vai directamente ao RM.

### Objectivo

Uma tool call negada pela lista-branca do run fica no registo como qualquer outra recusa de tool
call: quem pediu, o quê, em que passo, e porque foi negada.

### Critérios de Aceitação

- [x] Decisão (A)/(B) registada. *(Ver «Decidido pelo dono», acima.)*
- [x] O ADR-027 §2.3 é emendado para dizer onde a lista é imposta, e o §4, o `docs/adr/README.md`
      e o texto do AOS-413 ficam coerentes com ele. *(Evidência: emenda datada de 2026-10-02 no
      §2.3 do ADR-027 e remissão no §4; linha do ADR-027 no `docs/adr/README.md` e RTM
      regenerada; nota no AOS-413 (EPIC-19) a remeter para este ticket, sem reescrever a evidência
      do ticket fechado.)*
- [x] A imposição não depende de a cadeia de hooks estar bem composta: um Reference Monitor sem o
      hook nega na mesma uma tool fora da lista. *(Evidência: backstop em `Monitor.evaluate`, com
      o mesmo código e `denied_by` do `RunAllowlistGate`; `TestAOS485_BackstopNegaSemOGateNaCadeia`
      (`DefaultHooks` e cadeia sem o gate), `TestAOS485_NilNaoRestringeEVaziaNegaTudo` e
      `TestAOS485_ForaDaListaPrecedeAToolNaoRegistada`, em `packages/kernel/reference-monitor`.
      No nó composto, retirar só o hook não deixa a tool executar; retirar o hook e o backstop
      deixa — mutação sobre `TestAOS485_NoComposto_ListaVaziaNegaPelaListaEConta`. O canal por
      onde o hook dá o código é fechado: `TestAOS485_CodigoDoHookEFechadoESoValeNaNegacao`.)*
- [x] A lista chega igual pela via directa e pela via durável, e uma lista **vazia** não se
      transforma em ausente pelo caminho (teste de paridade próprio, com `[]`). *(Evidência:
      `TestAOS485_ListaVaziaChegaIgualPelasDuasVias`, com o controlo
      `TestAOS485_SemListaEComAToolNaListaAsDuasViasPermitem`, e
      `TestAOS485_DurableDispatcherNaoTransformaVaziaEmNil`, em `packages/integration`; a
      auditoria campo a campo `TestAOS454_AuditoriaCampoACampoCallActivityCall` cobre o campo
      novo. Mutação: sem a linha do `DurableDispatcher`, os três avermelham. No pacote
      `activity`: `TestAOS485_ListaChegaAoCallTalComoFoiDespachada` (nil, `[]` e lista). O
      step-ledger não responde por uma call fora da lista:
      `TestAOS485_ForaDaListaNaoRecebeOResultadoMemorizado` (`activity`) e
      `TestAOS485_PassoJaAplicadoNaoRespondePorUmaCallForaDaLista` (`integration`, pelo ciclo: o
      output memorizado não chega ao modelo). A lista sobrevive à projecção Goal → registo de
      retoma do nó: `TestAOS485_RegistoDeRetomaLevaAListaDoGoal` (`cmd/aos`).)*
- [x] A recusa grava `tool.call.denied` no stream do run com o código
      `E_TOOL_OUTSIDE_RUN_ALLOWLIST`, o `denied_by`, o principal com a cadeia de delegação, o
      `step_id` da chamada (`<passo>-tool-N`) e o `parent_step_id` do turno. *(Evidência:
      `TestAOS485_RecusaPelaListaFicaNoEventStoreNoWORMENoContador` (`packages/integration`, pelo
      `NewSecuredRuntime` com o Event Store e o WORM do composition-root, nas duas vias de
      despacho — a directa e a durável com `cfg.Ledger`; o principal declarado pelo run não tem
      cadeia, pelo que a do evento vem do token verificado);
      `TestAOS485_NoComposto_ListaVaziaNegaPelaListaEConta` (`cmd/aos`, nó com execução durável
      e a tool no catálogo assinado, por `POST /runs`);
      `TestAOS485_RecusaGravaToolCallDeniedNoStreamDoRun` (RM);
      `TestAOS485_RecusaPelaListaDeixaEventoEContador` (ciclo do runtime).)*
- [x] `aos_mediation_denials_total` conta-a. *(Evidência:
      `TestAOS485_NoComposto_ListaVaziaNegaPelaListaEConta` lê a série do `/metrics` do nó antes
      e depois do run: sobe 1, e `aos_mediation_permits_total` fica igual. O controlo
      `TestAOS485_NoComposto_ToolNaListaExecuta` lê o contrário.)*
- [x] A recusa sela no WORM, na partição do run, com o código, o `denied_by` e o principal.
      *(Evidência: `TestAOS485_RecusaPelaListaFicaNoEventStoreNoWORMENoContador` lê a partição do
      run no WORM do nó composto: um selo `deny` com o código, o `denied_by`, o `step_id`, o
      `parent_step_id` e o principal com a cadeia.)*
- [x] Nada é despachado nem construído como efeito, e não há evento de sandbox. Os testes do
      AOS-413 continuam a provar que a tool não executa; a asserção de
      `TestAOS413_ToolsDoPostRunsCortaAToolForaDaLista` que exigia que a call **não chegasse ao RM**
      inverte-se (recusas +1, permits igual). *(Evidência: os quatro testes de
      `aos413_allowlist_test.go` passam sem alteração; `TestAOS485_ToolForaDaListaNaoEReescrita`
      (a reescrita não corre para a tool fora da lista, com o controlo de que corre para a da
      lista) e `TestAOS485_ReescritaNaoTiraAListaDaCall`; a asserção do teste do nó foi invertida
      e passou a exigir também o `tool.call.denied` e a ausência de `tool.call.mediated`,
      `tool.call.outcome` e `sandbox.*` no stream. A posição antes da revalidação mede-se no
      WORM: a recusa não sela revalidação, e a mesma call sem lista sela —
      `TestAOS485_SemListaAMesmaCallPassaACadeiaInteira`. Que é a LISTA a impedir, e não outro
      gate: no nó composto com a tool no catálogo, `[]` não executa e `[counter]` executa
      (`TestAOS485_NoComposto_ToolNaListaExecuta`) — o teste do AOS-413 não o provava, porque
      ali a `echo` não está no catálogo e a revalidação negava-a de qualquer modo. Na via
      durável não fica `step.ledger.applied`. Limite: o nó destes testes não tem bindings de
      sandbox, pelo que a ausência de `sandbox.*` decorre de a tool não ser despachada, e não
      foi medida num nó com sandbox composta.)*
- [x] Replay: um log antigo, sem o evento, reconstrói o mesmo estado; um run novo com a recusa é
      reproduzido de forma determinista. *(Evidência:
      `TestAOS485_LogAntigoSemOEventoReconstroiOMesmoEstado` e
      `TestAOS485_ReplayDeUmRunComRecusaPelaListaEFielEDeterminista` (com a âncora de autoridade
      ligada), em `packages/kernel/agent-runtime/replay`; `scripts/ci/replay.sh` verde.)*
- [x] O catálogo de eventos e `tecnica/13` ficam coerentes com o produtor e o `step_id` da recusa.
      *(Evidência: `tecnica/13` §3.1, parágrafo «A recusa pela lista-branca do run»; o tipo
      `tool.call.denied` já estava no catálogo, na classe `cadeia`, e não muda —
      `scripts/ci/event-catalog.sh` verde.)*
- [ ] Verificado em produção: um run filho com lista-branca vazia que peça uma tool deixa o
      `tool.call.denied` no `events.wal`.

### Fora de âmbito

- Deixar de oferecer ao modelo as tools que a lista-branca nega (AOS-486).
- Os dados que o nó dependente não recebeu (AOS-484).

### Estado

**ABERTO.** Implementado; a verificação em produção não é alcançável pelo caminho normal.

**Verificado em produção a 2026-10-03** (`v0.1.43`, imagem `sha256:feca01e6…`), com o modelo
vivo e o mesmo objectivo do relatório de 2026-10-02, submetido por `POST /plans` e drenado pela
fila: plano `plan-e2e-v0143-1790987272`. **O critério não se cumpriu, e por uma razão que não é defeito.**

Nenhum run do plano deixou uma recusa pela lista-branca (`E_TOOL_OUTSIDE_RUN_ALLOWLIST`: 0
ocorrências). Com o AOS-486 na mesma release, o modelo de um run filho deixou de receber o schema
das tools que a lista nega, e por isso não as pede: o `n2`, com a lista vazia, teve
`tool_calls_requested=0`. A imposição no Reference Monitor passou a ser defesa em profundidade, e
um run com o modelo vivo já não a exercita.

O que fica provado em produção é só o caminho de mediação do mesmo run: as três recusas por taint
do `n1` deixaram `tool.call.denied` e selo `deny` na partição do run, como o desenho exige para
qualquer recusa do RM. A recusa pela lista-branca em si está provada pelos testes do nó composto
(`TestAOS485_NoComposto_ListaVaziaNegaPelaListaEConta`, com o `/metrics`). Exercê-la em produção
exige um modelo que peça uma tool que não lhe foi oferecida, o que o caminho normal já não produz.
Fechar o ticket sem esta prova é decisão do dono.

---

## AOS-489 — O tail do prompt regista a tool call do modelo e identifica o resultado (assembler 1.4.0)

<!-- Implementa o ADR-036 §2.1 a §2.3 (o tail canónico, o segmento `tool_call`, o layout por versão) e emenda o ADR-034 §2.1. A projecção em mensagens nativas (ADR-036 §2.4 a §2.7) é do AOS-490. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 |
| Fase | Prontidão para utilizadores reais |
| Tipo | fix |
| Prioridade | P0: 60% das tool calls mediadas em produção são o modelo a repetir uma chamada que já fez, e uma em três validações acabou com o objectivo por cumprir |
| Estimativa | L |
| Dependências | AOS-013 (loop), AOS-016 (captura e replay), AOS-069 e ADR-034 (autoridade derivada do contexto), AOS-414 (`plan_input`), AOS-487 (resultado da sandbox em texto) |
| Bloqueia | AOS-490 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-protocolo-tool-use-2026-10-03.md` (desenho e decisões), `packages/kernel/agent-runtime/prompt.go`, `loop.go`, `context_authority.go`, `replay/engine.go`, `replay/nondeterminism_capture.go`, `packages/integration/resume_records.go` |

### Contexto

Medido sobre o `events.wal` de produção inteiro a 2026-10-03: desde que há eventos de mediação, 17
de 32 runs que pediram tools repetiram a mesma tool sobre o mesmo recurso, e 45 das 75 tool calls
registadas são repetições. Na validação `plan-e2e-v0143r-1790988359` o nó desistiu depois da recusa
da releitura e o plano saiu 0 sem o objectivo cumprido.

A causa está no que o modelo recebe. O prompt vai inteiro numa mensagem de utilizador, e no turno a
seguir a uma tool call o tail tem o objectivo e um `<tool_result taint=untrusted>` sem mais nada: a
chamada do próprio modelo não é registada (o loop só acrescenta o texto da resposta, se não for
vazio), o resultado não diz de que chamada é, e o `system` dos runs filhos de um plano é vazio. A
documentação do provider de produção lista esta forma como a primeira causa de tool calls
repetidas. O desenho completo, com as cinco frentes de análise, está no documento de referência.

### Decidido pelo dono (2026-10-03)

- **D2 — layout por versão, fixado por run.** O assembler monta o layout 1.3.0 e o 1.4.0; o replay
  escolhe por turno pelo `assembly_version` gravado; um run que atravesse o deploy continua no
  layout em que começou.
- **D3 — preâmbulo de protocolo no prefixo**, na 1.4.0.
- **D4 — os argumentos vão no prompt**, com tecto de tamanho e digest acima dele. Aceita-se o
  reenvio dos argumentos nos turnos seguintes e a proximidade entre argumentos e código de recusa.

### Objectivo

O modelo vê, no turno seguinte, que chamada fez e que resultado lhe corresponde; os runs gravados
antes continuam a reproduzir-se; e a repetição passa a ser medida.

### Critérios de Aceitação

- [x] Por cada tool call do modelo o tail ganha um segmento `tool_call` antes do resultado, com o
      `id` cunhado pelo runtime (`<passo>-tool-<n>`) e o `name` na linha de delimitação, e os
      argumentos tal como o modelo os emitiu (antes da reescrita do efeito) no corpo. O `tool_result`
      leva o mesmo `id` e `name`. Capability, recurso, região, reversibilidade, `Reason` e metadados
      de hook não entram.
      *(Evidência: `packages/kernel/agent-runtime/layout.go` (`tailFromToolCall`, `tailFromIdentifiedResult`,
      `ToolStepID`). `TestAOS489_OModeloVeAChamadaEOResultado` fixa o tail do turno seguinte byte a
      byte, com uma chamada permitida e uma negada, um `CallRewriter` que troca o input e os campos de
      política preenchidos com valores que não podem aparecer; `TestAOS489_IdDoPromptEODoEventoDeMediacao`
      (o `id` é o `step_id` da mediação); `TestAOS489_Forja_ReasonEMetadataForaDoPromptComAChamadaRegistada`.)*
- [x] O prefixo ganha um preâmbulo de protocolo fixo e versionado: o que é cada segmento, que o
      resultado com o mesmo `id` responde à chamada, que uma recusa não se repete com os mesmos
      argumentos, e que conteúdo `taint=untrusted` é dados.
      *(Evidência: `preambuloDeProtocolo140` em `layout.go`, à cabeça do prefixo (`buildPrefix`): 1 144
      bytes ASCII (cerca de 286 tokens), sem dados do run. A frase da recusa é condicionada
      («Unless something has changed since, …»): há recusas transitórias (audit indisponível,
      erro de hook, contexto cancelado, escalada). Diz também que só `objective`,
      `correction` e `notice` são instruções, e que `memory` — que não tem rótulo — é dados. `TestPreambuloDeProtocolo_RestricoesDoTexto` (igual à cópia
      selada à mão, ASCII, nenhuma linha a abrir por `<`, sem rótulos de recusa nem `taint=trusted`,
      prefixo byte-idêntico entre turnos) e o golden `promptSelado140`.)*
- [x] `AssemblyVersion` sobe para 1.4.0, com o golden do layout derivado à mão a cobrir o segmento
      novo, o hash e a versão selados, e a entrada no comentário da constante (incluindo a da 1.3.0,
      que falta).
      *(Evidência: `packages/kernel/agent-runtime/layout_do_prompt_selado_test.go` — `promptSelado140` escrito à mão
      (preâmbulo, três chamadas num turno, recusa, nome hostil, CR seguido de delimitador forjado,
      argumentos acima do tecto) e `hashSelado140` calculado sobre o literal;
      `TestLayoutDoPromptSelado_BytesMaterializados`, `TestLayoutDoPromptSelado_VersaoCorresponde`.
      O golden da 1.3.0 ficou como estava, com o mesmo hash
      (`TestLayoutDoPromptSelado_130ContinuaByteIdentico`). As entradas 1.3.0 e 1.4.0 estão no
      comentário de `AssemblyVersion` em `prompt.go`.)*
- [x] A sequência de segmentos de um turno vive numa função única, usada pelo loop e pelo motor de
      replay, incluindo o caminho de escalada e o de várias chamadas no mesmo turno.
      *(Evidência: `TailSequence` (`Turn`, `Correction`) em `layout.go`; o loop usa-a em
      `fecharTail` (fim do turno e saída por escalada) e o motor em `dobrasPorLayout`, uma por
      dobra. `TestAOS489_OLoopEOMotorDobramOMesmoTail` compara o tail que o loop
      construiu com o estado final do motor, nos dois layouts, com três chamadas num turno, recusa,
      erro de tool, steer e escalada; `TestAOS489_EscaladaAMeioDoTurno`; `TestAOS489_SequenciaDoTurno`.)*
- [x] **Layout por versão:** um log gravado em 1.3.0 reproduz-se com fidelidade 1.0 (teste com um
      log 1.3.0 fixado em disco, não gerado pelo código corrente); um log misto por turno
      reproduz-se; um run iniciado em 1.3.0 e retomado depois continua em 1.3.0 (a versão fica no
      registo de retoma, com `omitempty`); a divergência por versão sai atribuída a
      `assembly_version`.
      *(Evidência: `packages/kernel/agent-runtime/replay/testdata/aos489_log_1_3_0.json` — três runs corridos pela
      árvore do commit `4253c70` extraída com `git archive`, com o gerador ao lado —, e
      `TestAOS489_Log130FixadoEmDiscoReproduzSe` (fidelidade 1.0 com e sem âncora, autoridade
      verificada, e a âncora errada atribuída a `assembly_version` mesmo quando o prompt também
      diverge). `TestAOS489_LogMistoPorTurnoReproduzSe` (nos dois sentidos).
      `TestAOS489_VersaoDesconhecidaNoLogFalhaFechada`. Pelo nó:
      `TestAOS489_RetomaContinuaNoLayoutEmQueORunComecou` em `packages/cmd/aos` (retoma e
      crash-resume; um registo sem versão continua em 1.3.0 e um run novo em 1.4.0), sobre
      `integration.ResumeRecord.AssemblyVersion` e `fixarLayout` em `service.go`;
      `TestAOS489_RegistoAntigoSemLayoutERetomadoEm130` em `packages/integration`. Um registo num
      layout que o binário não conhece não é retomado, e o run fica como estava:
      `TestAOS489_LayoutDesconhecidoNoRegistoNaoERetomado`.)*
- [x] Um run sem tool calls grava em 1.4.0 os mesmos bytes de `turn.recorded` e `replay.captured`,
      salvo a versão e o prefixo; as capturas antigas descodificam como antes.
      *(Evidência: `TestAOS489_RunSemToolCalls_MesmosBytesSalvoAVersaoEOPrefixo` compara os payloads
      que o código antigo gravou (a fixture em disco) com os de agora: a captura é byte-idêntica, e o
      `turn.recorded` é-o depois de trocar a `assembly_version` e o `prompt_hash`.
      `TestAOS489_ReconstructDeUmLog130` descodifica as capturas antigas. O esquema de captura não
      mudou.)*
- [x] O tipo novo é classificado explicitamente em `SegmentAuthority` como output do modelo (não
      eleva nem baixa a autoridade), com caso em `TestSegmentAuthority` e linha na tabela do ADR-034
      §2.1; a autoridade de cada turno e a decisão do TaintGate não mudam (replay com
      `VerifyAuthority`).
      *(Evidência: `context_authority.go` (`case TailHistory, TailToolCall`); `TestSegmentAuthority`;
      a emenda de 2026-10-03 ao ADR-034 §2.1. `TestAOS489_ToolCallNaoMudaAAutoridade` (inserir o
      segmento em qualquer posição não muda a dobra de nenhum prefixo) e
      `TestAOS489_MesmasMediacoesNosDoisLayouts` (o mesmo taint e o mesmo veredicto em cada chamada,
      com o TaintGate armado, em 1.3.0 e em 1.4.0). Replay com `VerifyAuthority` sobre o log 1.3.0 em
      disco, o 1.4.0 e o misto. Os `TestAOS069_*` continuam verdes, sem alteração.)*
- [x] Segurança do segmento: argumentos só no corpo e neutralizados; `id` e `name` só na linha de
      delimitação, saneados e com tecto de comprimento; tecto de tamanho dos argumentos, com digest
      acima dele; testes de forja (argumentos, nome e resultado que imitam `<tool_call>`,
      `<tool_result>` e `<correction taint=trusted>`), e a `Reason` continua fora do prompt.
      *(Evidência: o `tool_result` de uma tool que falhou leva o rótulo `tool_error=1` na linha de
      delimitação (`TestAOS489_ToolErrorERotuloNa140`). `MaxToolCallLabelBytes` (256) e `MaxToolCallArgBytes` (4 KiB) em `layout.go`; acima
      do tecto o corpo fica vazio e a linha de delimitação leva `args_omitted_bytes` e `args_digest`.
      `TestAOS489_Forja_ArgumentosNaoAbremSegmentos`, `TestAOS489_Forja_NomeDeToolHostil`,
      `TestAOS489_Forja_ResultadoImitaChamadaEResultado`,
      `TestAOS489_Forja_ReasonEMetadataForaDoPromptComAChamadaRegistada` — todos pelo loop real, a
      contar as linhas de delimitação do tail —, e `TestAOS489_TectoDosArgumentos`.
      `TestModelBoundaryCarriesNoAuthority` continua verde: `ToolInvocation` e `ModelResponse` não
      ganharam campos.)*
- [x] A neutralização de delimitadores reconhece `\r` e os separadores de linha Unicode como início
      de linha, com a tabela de neutralização actualizada.
      *(Evidência: `inicioDeLinha` em `prompt.go` — no layout 1.4.0 abrem linha `\r`, VT, FF, U+0085,
      U+2028 e U+2029; o 1.3.0 fica como estava. `packages/kernel/agent-runtime/tabela_de_neutralizacao_test.go`:
      `TestNeutralizarDelimitadores_QuebrasDeLinhaPorLayout` (nos dois sentidos),
      `TestNeutralizarDelimitadores_EInjectiva` e `TestNeutralizarDelimitadores_Reversivel`.)*
- [x] `PromptView` passa a transportar o tail estruturado (aditivo), para o AOS-490; a janela gerida
      (`working.TailInput`) transporta os campos novos, com a paridade inline/gerida provada.
      *(Evidência: `PromptView.Tail`, `.System` e `.AssemblyVersion` em `prompt.go`
      (`TestAOS489_PromptViewEstruturada`: cópia profunda, conteúdo cru). O `TailSegment` não ganhou
      campos — o `id` e o `name` são rótulos e atravessam `working.TailInput.Meta`; o que a janela
      gerida ganhou foi o layout (`working.Config.AssemblyVersion`, e o parâmetro novo de
      `WindowFactory.NewWindow`). `TestAOS489_JanelaGeridaByteIdenticaAInline_NosDoisLayouts`
      compara a vista inteira nos dois layouts; `TestWindowManagerFactory_ByteIdenticalToInline`
      continua verde.)*
- [x] Métrica de eficiência de trajectória em `/metrics`: tool calls repetidas (mesma tool, mesmos
      argumentos) por processo, e aviso trusted no tail à terceira repetição idêntica num run.
      *(Evidência: `aos_tool_calls_total` e `aos_tool_calls_repeated_total` no `/metrics` do nó
      (`packages/cmd/aos/aos489_metricas.go`, `api.go`), alimentados por
      `agentruntime.WithToolCallStats`; a contagem vive na `TailSequence` do kernel, que é quem tem
      a história das chamadas do run. `TestAOS489_MedicaoDeRepeticoes` (permitidas e negadas, nos
      dois layouts), `TestAOS489_MetricasDoLayoutEDasRepeticoes`. A repetição mede-se sobre os
      argumentos do MODELO, mesmo com um `CallRewriter` que varia o input por chamada. O aviso é o
      segmento `notice` (`tailFromRepeatNotice`), só na 1.4.0 e só em repetições ESTÉREIS: a
      mesma chamada com o mesmo desfecho (resultado, falha ou recusa) três vezes seguidas; um
      resultado diferente recomeça a série. `TestAOS489_AvisoATerceiraChamadaIdentica`,
      `TestAOS489_AvisoSoEmRepeticoesEstereis` (três negadas, três falhadas, e
      leitura/escrita/leitura sem aviso), `TestAOS489_ChaveDaChamadaNaoColide` (limitação
      declarada: chave repetida num objecto JSON), `TestAOS489_DesfechoDaChamada`,
      `TestAOS489_AvisoDeRepeticaoAtravessaAAprovacaoHumana` (pelo nó, com quatro suspensões e
      retomas), `TestAOS489_AvisoNaoMudaAAutoridadeNemEForjavel`,
      o golden `promptSelado140` e o caso «tres chamadas identicas» de
      `TestAOS489_OLoopEOMotorDobramOMesmoTail` (o motor reconstrói-o sem captura). O layout em
      uso também se vê: `aos_runs_hosted_total{assembly_version}` e uma linha de log quando um
      run é hospedado fora do layout dos runs novos
      (`TestAOS489_RetomaContinuaNoLayoutEmQueORunComecou`).)*
- [x] Gates: `replay` (os 12 testes pelo mesmo nome), `security`, `apex`, `lint`, `build`.
      *(Evidência: corridos localmente a 2026-10-03 sobre a árvore do ramo, todos verdes — `replay`
      («12 testes obrigatórios correram e passaram», fidelidade 100%), `security`, `apex` (piso do
      ápice 83,6%), `lint`, `build`; e ainda `layer-lint`, `dr-e2e`, `ref-lint`, `deferrals`,
      `event-catalog`, `secrets` e `sast`. O `rtm` fica vermelho só pela gama de tickets
      descontínua — falta um número, de um ticket que está noutro ramo por fundir —, pelo que a RTM não foi regenerada
      neste ramo.)*
- [ ] Verificado em produção: em pelo menos dez runs com tools, repetições da mesma tool sobre o
      mesmo recurso perto de zero, medidas com o script da linha de base.

### Fora de âmbito

- A projecção em mensagens nativas e a continuidade do raciocínio (AOS-490).

### Resíduos conhecidos (revisão adversarial de 2026-10-03)

- **Erro de tool com mensagem vazia.** Uma tool que falhe com um erro cuja mensagem é a string
  vazia diverge entre o loop e o replay: a captura grava `tool_error` com `omitempty`, o campo
  vazio desaparece, e o motor reconstrói o resultado como bem-sucedido — sem o rótulo
  `tool_error=1` e sem a linha do corpo. É anterior a este ticket (o `omitempty` é o da captura
  original) e não foi corrigido aqui.
- **A chave de «chamada idêntica» com bytes que não são UTF-8** foi corrigida na integração com o
  AOS-490: argumentos que não são UTF-8 válido comparam-se pelos bytes crus, sem forma canónica
  (`TestAOS489_ChaveDaChamadaNaoColide`).
- A separação de planos por handle (DEF-806): o segmento de argumentos fica compatível com ela, mas
  não a implementa.
- Os achados laterais do documento de desenho §6.

### Verificação em produção (2026-10-04, v0.1.45)

Imagem `sha256:006c50db…` em produção. Um pedido de plano real pela fila, o mesmo objectivo
multi-nó das validações da v0.1.43 e da v0.1.44 (ler `notes` com `doc_read`; num passo
dependente, resumir em três pontos): `plan-e2e-v0145-1791114237`, terminal, `exit_code=0`,
32,8 s.

| | v0.1.44 (`plan-e2e-v0144-1791017397`) | v0.1.45 (`plan-e2e-v0145-1791114237`) |
|---|---|---|
| Turnos do nó que lê (`~n1`) | 6 | 2 |
| Tool calls pedidas pelo modelo | 5 | 1 |
| `tool.call.mediated` | 1 | 1 |
| `tool.call.denied` (releituras negadas por taint) | 4 | 0 |
| Turnos do nó que resume (`~n2`) | 1 | 1 |
| `assembly_version` nos `turn.recorded` | 1.3.0 | 1.4.0 |

- O nó `~n1` leu o documento uma vez e concluiu no turno seguinte. O `final_text` reproduz o
  conteúdo de `notes`, e o do `~n2` resume-o em três pontos.
- Invariantes das pegadas (chave de idempotência, `seq` sem buracos, encadeamento dos selos):
  íntegros.

**Por cumprir:** o critério «repetições perto de zero em pelo menos 10 runs» tem um run. A
linha de base recalculada sobre o `events.wal` inteiro mostra este run com zero repetições; os
restantes nove acumulam-se com o uso. As métricas `aos_tool_calls_repeated_total` e
`aos_runs_hosted_total{assembly_version}` não foram lidas no `/metrics` de produção.

### Estado

**ABERTO.** Entregue e em produção na v0.1.45; verificado num run (2026-10-04). Fecha quando houver pelo menos 10 runs com tools no layout 1.4.0 e as repetições estiverem perto de zero.

---

## AOS-492 — A regra de terminação do run vive num só sítio, partilhado pelo loop e pelo replay

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: é uma refactorização sem mudança de comportamento, que prepara o AOS-493. O ADR-036 §2.3 (layout por versão) é citado como restrição: nada muda nos layouts 1.3.0 e 1.4.0. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A0 |
| Tipo | refactor |
| Prioridade | P0: a regra está copiada à mão no motor de replay; mudá-la só no loop faz o replay parar no turno errado sem avisar |
| Estimativa | S |
| Dependências | AOS-489 (layout por versão) |
| Bloqueia | AOS-493 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/analise-fronteira-runtime-modelo-2026-10-04.md` §3, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md`, `packages/kernel/agent-runtime/loop.go`, `packages/kernel/agent-runtime/replay/engine.go`, `packages/kernel/agent-runtime/layout.go` |

### Contexto

O loop decide que um run terminou com `resp.Final || len(resp.ToolCalls) == 0` (`loop.go`). O
motor de replay tem a mesma condição escrita outra vez (`replay/engine.go`). São duas cópias sem
nada que as mantenha iguais.

O AOS-493 vai mudar o que essa decisão significa. Se a mudança entrar numa cópia e não na outra, o
replay reproduz uma trajectória diferente da que correu, com fidelidade aparente de 100%.

### Objectivo

Uma única função decide se um turno termina o run, usada pelo loop e pelo motor de replay. O
comportamento nos layouts 1.3.0 e 1.4.0 fica byte a byte igual.

### Critérios de Aceitação

- [x] A decisão de terminação é uma função única do pacote do runtime, que recebe a resposta do
      modelo e o layout do run. O loop e o motor de replay chamam-na; nenhum dos dois tem a
      condição escrita localmente.
- [x] Um teste estrutural falha se a condição voltar a aparecer escrita à mão no loop ou no motor
      de replay.
- [x] Todos os goldens de replay existentes passam sem alteração, incluindo a fixture do layout
      1.3.0.
- [x] A suite do runtime, do replay e do nó passa com `-race` sem mudança de asserções.

### Fora de âmbito

- Qualquer mudança de comportamento (AOS-493).
- O motivo de paragem (AOS-491).

### Estado

**FEITO (2026-10-04).** A função é `TurnEndsRun`. Dois testes guardam-na, e garantem coisas
diferentes:

- O teste estrutural apanha a cópia da regra escrita à mão (verbatim, em closure local, negada)
  e a remoção da chamada. Escapa-lhe uma cópia através de variável intermédia, de `switch` ou
  de função auxiliar.
- O teste diferencial corre o loop e reprodu-lo no motor de replay, para sete tipos de resposta
  nos dois layouts, e exige o mesmo turno de terminação e o mesmo texto final. É este que
  garante a igualdade de comportamento, e falha se entrar um layout que a tabela não cobre.

A revisão adversarial mostrou que o teste estrutural sozinho deixava passar o motor a ignorar
o resultado da função; o diferencial nasceu desse achado.

---

## AOS-493 — O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão

<!-- Este ticket escreve e implementa o ADR-037 (o desfecho de um run é um veredicto do kernel sobre um contrato de conclusão, e a ausência de tool calls não é conclusão). -->
<!-- rtm: menção -->
<!-- O ADR-034 e o ADR-036 são citados como contexto e NÃO são implementados nem emendados aqui — a reparação, que exigiria emendá-los, é da fase A1. -->
<!-- /rtm: menção -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A0 |
| Tipo | fix |
| Prioridade | P0: em 2 de 10 planos em produção o run fechou concluído sem ter executado a tool de que a sua saída dependia, e o plano saiu com `exit_code=0` |
| Estimativa | L |
| Dependências | AOS-491 (motivo de paragem), AOS-492 (regra de terminação única), AOS-485 (lista-branca do run no Reference Monitor) |
| Bloqueia | AOS-494, AOS-495 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/analise-fronteira-runtime-modelo-2026-10-04.md` §5.1 e §7, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md`, `packages/kernel/agent-runtime/loop.go`, `packages/kernel/agent-runtime/replay/engine.go`, `packages/cmd/aos/terminal_states.go` |

### Contexto

Uma só condição no loop responde a três perguntas: «o modelo não pediu tools», «o run acabou» e
«o run acabou bem». Qualquer turno sem tool calls fecha o run como concluído: texto legítimo, uma
tool call escrita como texto, texto vazio, resposta truncada, recusa.

Medido em produção a 2026-10-04 (v0.1.45): nos planos `plan-e2e-v0145-1791115918` e
`plan-e2e-v0145-1791117087` o nó de leitura respondeu num turno, com a chamada a `doc_read` escrita
como texto e nenhuma tool call. O run fechou `completed`, o texto foi publicado como saída do nó, e
os dois planos saíram verdes sem cumprir o objectivo.

### Decidido pelo dono (2026-10-04)

- O contrato de conclusão é **inferido das tools atribuídas ao nó**, em âmbito estreito, sem
  mudar o schema do plano.
- O «não cumprido» grava-se como **`failed` com razão própria**. Não há `status` novo.

### Objectivo

O kernel calcula o desfecho de um run a partir do que foi de facto executado, contra um contrato
de conclusão que o chamador declara, e sela-o na transição terminal com uma razão em vocabulário
fechado. Um run que não cumpre o contrato termina `failed`, nunca `completed`.

### Critérios de Aceitação

- [x] O objectivo de um run pode levar um **contrato de conclusão**: a lista de tools de que a
      conclusão depende. Vazio quer dizer o comportamento de hoje.
- [x] O kernel conta, por tool, as chamadas **efectivas** do run: despachadas, sem recusa e sem
      erro de tool. A contagem vem dos contadores do próprio loop; não se lê do evento de desfecho
      da tool, que é opcional e fail-open.
- [x] O veredicto é um vector por tool exigida, e distingue pelo menos estas razões: contrato não
      cumprido sem nenhuma chamada pedida; contrato não cumprido depois de uma recusa; contrato não
      cumprido depois de uma falha de tool; resposta truncada; saída vazia.
- [x] Um turno com motivo de paragem `length` deixa de ser conclusão, com ou sem contrato.
- [x] Um run com veredicto negativo termina no estado durável `failed`, com `outcome_reason` no
      vocabulário fechado e sem texto final. O evento da transição terminal leva a razão e o
      vector do veredicto.
- [x] Modo de aplicação por configuração do nó, com três valores: desligado, observação (calcula,
      regista e conta, mas não muda o desfecho) e imposição. A omissão é observação. Um valor
      desconhecido recusa o arranque.
- [x] Contadores no `/metrics`: runs terminados por desfecho e razão; runs concluídos sem nenhuma
      tool call tendo tools no tool set do run.
- [x] O replay reproduz o veredicto **em runs de uma só vida**: o motor usa a mesma função
      (AOS-492) e chega ao mesmo desfecho a partir da captura. Um run gravado antes deste ticket
      reproduz-se com o desfecho que teve. Num run **retomado** cujo turno re-executado mudou de
      desfecho (falha de tool que na retoma tem êxito ou é recusada), o replay pára em divergência
      de `prompt_hash` e não reproduz veredicto nenhum: é uma classe anterior a este ticket, que
      ele não fecha.
- [x] A retoma depois de um crash preserva o contrato e os contadores (o contrato fica no registo
      de retoma; os contadores reconstroem-se do log).
- [x] As duas respostas de produção de 2026-10-04 entram como fixtures: com contrato e em
      imposição, cada uma termina `failed` com a razão «sem nenhuma chamada pedida».
- [x] ADR novo escrito e registado: o desfecho de um run é um veredicto do kernel; a ausência de
      tool calls não é conclusão; a evidência de tool call não é fidelidade da saída (resíduo
      declarado).
- [x] Revisão adversarial independente com mutações, antes da fusão. Feita a 2026-10-05: nenhum
      bloqueante; `observe` e `off` medidos contra a base (desfecho, eventos, log e WORM iguais).
      Seis achados a fechar antes de `enforce` e 6 de 29 mutações vivas (cablagem da variável e do
      banner, saga, métrica, modo do turno terminal no replay); corrigidos neste ticket.

### Fora de âmbito

- Reparação (dar outro turno ao modelo): fase A1.
- Detectar a forma do texto como critério. Pode existir como rótulo de medição, nunca como
  entrada de controlo.
- Garantir que a saída corresponde ao que a tool devolveu: fase de saída por referência.
- O campo no `POST /runs` e a resposta do `GET /runs` (AOS-494); o lado do `aos-orq` (AOS-495).

### Verificação em produção (2026-10-05, v0.1.46)

Imagem `sha256:f9a66635…` em produção, nó em observação (banner «veredicto de conclusao (EPIC-02/AOS-493): EM OBSERVACAO»). Um pedido de plano real pela fila, com o objectivo multi-nó de sempre: `plan-e2e-v0146-1791189316`, terminal, `exit_code=0`, 26 s.

- O manifesto de cada turno grava o modo e o contrato: `{"mode":"observe","requires":["doc_read"]}`
  no nó de leitura e `{"mode":"observe"}` no nó de resumo.
- A transição terminal do nó de leitura leva o vector: `doc_read` pedida 1, efectiva 1,
  `last=effective`, `fulfilled=true`. A do nó de resumo: `fulfilled=true`, zero tool calls.
- `/metrics`: `aos_runs_finished_total{outcome="complete",reason="none"} 2`; um run com
  `last="effective"` e outro com `last="none"`.

**Por observar:** um veredicto negativo. Neste plano o modelo chamou a tool; o caso do verde
falso (cerca de um plano em cinco, medido a 2026-10-04) ainda não ocorreu depois do deploy.

### Estado

**IMPLEMENTADO (2026-10-04) e REVISTO (2026-10-05); por verificar em produção.**
A decisão está no ADR-037 (`docs/adr/ADR-037-o-desfecho-de-um-run-e-um-veredicto-do-kernel.md`).

- **Contrato e modo.** `Goal.CompletionRequires` (nomes de tool) e `Goal.CompletionMode`. O nó
  fixa o modo por run a partir de `AOS_COMPLETION_VERDICT` (`observe` por omissão, `enforce`,
  `off`; outro valor recusa o arranque). Os dois vão no registo de retoma e em
  `manifest.completion` de cada `turn.recorded`. Um registo de retoma sem modo é de um run
  anterior e retoma-se sem veredicto.
- **Veredicto.** `agentruntime.ConcludeRun`, chamada pelo loop e pelo motor de replay no turno
  terminal, sobre contadores (`RunEvidence`) alimentados das tool calls despachadas em cada turno.
  `TurnEndsRun` não mudou.
- **Razões** (`outcome_reason`): `truncated`, `contract_unmet_no_call`,
  `contract_unmet_after_denial`, `contract_unmet_after_tool_error`, `empty_output`, por esta
  precedência. A razão do contrato é a da primeira tool em falta.
- **Estado durável.** `failed` com razão de auditoria `objective_unfulfilled`; a transição
  terminal leva `outcome_reason` e `verdict`, também em observação (aí com `run_complete`).
- **Métricas.** `aos_runs_finished_total{outcome,reason}`,
  `aos_runs_completed_without_tool_call_total` (tools no tool set do run, não o que o gateway
  enviou) e `aos_runs_finished_by_last_tool_outcome_total{outcome,verdict,last}`.
- **Entrou por campos, não por layout novo.** O prompt não muda um byte, e um layout novo teria
  de ser conhecido da projecção nativa.

Decisões de implementação que o dono ou o revisor devem validar:

1. `empty_output` e `truncated` valem com ou sem contrato. O ticket só o pede para `length`.
2. Saída só com espaços conta como vazia.
3. `content_filter` e motivos de paragem fora do mapa conhecido **não** são veredicto negativo.
4. Um run não cumprido entra na saga de compensação, como qualquer `failed`. Hoje não há
   compensações registadas: o efeito das tools que correram bem fica aplicado e a ausência é
   declarada no WORM. O que a saga deve fazer a esses efeitos quando houver compensações está
   por decidir (ADR-037 §4).
5. Em observação o manifesto de cada turno de um run novo ganha `completion`. Os bytes do
   `turn.recorded` mudam para runs novos; com `off` ficam os de antes.

O que a revisão de 2026-10-05 mudou:

- **Contrato impossível.** Uma tool exigida que não está no tool set do run ou na sua
  lista-branca recusa o arranque (`ErrImpossibleCompletionContract`), em `observe` e em
  `enforce`. Em `off` o contrato não é lido.
- **Medição.** Os runs que acabam sobre uma recusa ou uma falha de tool com veredicto positivo
  contam-se em `aos_runs_finished_by_last_tool_outcome_total`. Não é uma razão negativa nova.
- **Retoma depois de uma falha de tool.** Na retoma por crash, sem credencial, a tool que falhou
  é negada na segunda vida e, em `enforce`, o run sela `contract_unmet_after_denial`; com
  credencial volta a correr. Fixado por teste.
- **Fail-closed.** O registo de retoma recusa um Goal sem o modo fixado; a máquina de estados só
  aceita o veredicto numa transição que termina o run e com `outcome_reason` do vocabulário; o
  log só diz «Estado duravel: failed» depois de o selo ter sido escrito.

Por fechar, da mesma revisão (menores, não corrigidos aqui): um selo que foi no-op conta como
selado por este processo no contador; o rollback do binário com a deduplicação do Event Store
perdida daria um registo de retoma divergente.

O que este ticket não fecha, além do «Fora de âmbito»: enquanto o desfecho estiver em memória, o
`GET /runs/{id}` de um run não cumprido responde `status: "completed"` com `terminated=false`
(AOS-494). O contrato só entra por código: até ao AOS-494 nenhum run submetido pela API o leva.

---

## AOS-497 — O kernel designa e sela a origem da saída de um run: o resultado da chamada efectiva da tool declarada

<!-- Este ticket escreve e implementa o ADR-038 (a saída de um nó de passagem directa é o resultado da tool: o kernel designa e sela a origem). -->
<!-- rtm: menção -->
<!-- O ADR-037 é emendado aqui (as razões novas do veredicto). O ADR-022, o ADR-027, o ADR-034 e o ADR-036 são citados como contexto e NÃO são implementados neste ticket; as emendas ao ADR-027 e ao ADR-022 cabem aos tickets que mudam o que eles descrevem (AOS-501 e AOS-500), como o ADR-038 regista. -->
<!-- /rtm: menção -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — saída por referência |
| Tipo | feat |
| Prioridade | P0: em 2 de 21 planos em produção o nó de leitura chamou a tool com sucesso e entregou um resumo em vez do conteúdo; o nó seguinte herdou a perda e o veredicto deu «cumprido» |
| Estimativa | M |
| Dependências | AOS-493 (veredicto do kernel e contrato de conclusão), AOS-492 (regra de terminação única) |
| Bloqueia | AOS-498 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A0.5), `docs/reports/analise-fronteira-runtime-modelo-2026-10-04.md`, `docs/adr/ADR-037-o-desfecho-de-um-run-e-um-veredicto-do-kernel.md`, `packages/kernel/agent-runtime/loop.go`, `packages/kernel/agent-runtime/completion.go`, `packages/kernel/agent-runtime/context_authority.go`, `packages/kernel/agent-runtime/replay/engine.go`, `packages/integration/resume_records.go` |

### Contexto

A saída de um run é hoje o texto final do modelo. O resultado real da tool fica selado no run
(captura do turno e, na via durável, o step-ledger) e nunca sai dele pelo caminho do plano. O
veredicto do AOS-493 conta chamadas efectivas por tool do contrato e, por desenho, não olha para o
texto: prova que a tool foi chamada, não que a saída é o que ela devolveu. É o resíduo «evidência
de tool call não é fidelidade da saída» do ADR-037.

Medido em produção a 2026-10-05 (v0.1.46, série de 21 planos): nos planos
`plan-e2e-v0146s-1791192287` e `plan-e2e-v0146s-1791192627` o nó de leitura chamou a tool com
sucesso e escreveu um resumo em vez do conteúdo. Perderam-se factos do documento (num, um número;
no outro, um número e uma tarefa inteira), o nó seguinte herdou a perda, e o veredicto de conclusão
deu «cumprido». Na mesma série o nó de leitura foi declarado `record` em 17 planos, `summary` em 3
e `artifact` em 1: o tipo da saída não diz se ela é o resultado da tool.

Não existe hoje um digest do resultado amarrado a um evento de mediação. O único digest do
resultado em claro é o `result_hash` do step-ledger, e só existe na via durável.

### Decidido pelo dono (2026-10-05)

- A origem de uma saída **declara-se** (no plano, AOS-500) e chega ao run como configuração do
  chamador. Não se infere da estrutura do run; a inferência estrutural existe só como medição
  (AOS-499).
- Sem origem designável (nenhuma chamada candidata, ou mais de uma), o run **falha com causa
  própria** — quando a declaração é vinculativa e o nó está em imposição (ver «Vínculo por
  run»). Nunca se entrega o texto do modelo no lugar do resultado da tool.
- A saída por referência é o resultado **tal como a tool o devolveu** (o envelope), byte a byte,
  conferível contra o digest selado.
- O texto final do modelo continua a ser capturado e selado, e deixa de ser a saída (por omissão,
  recomendação do desenho).

### Objectivo

Um run pode declarar que a sua saída é o resultado de uma tool. O kernel designa, de factos
estruturais e sem ler texto nem conteúdo, qual chamada é a origem, e sela a designação e o digest
desse resultado na transição terminal, ao lado do veredicto. Às escuras: enquanto nenhum chamador
declarar a origem (AOS-498), nenhum run muda.

### Critérios de Aceitação

- [x] O objectivo de um run pode levar a **origem da saída**: o nome de uma tool
      (`Goal.OutputFromTool`). Vazio quer dizer o comportamento de hoje, sem mudar um byte de
      eventos, manifesto ou registo de retoma.
- [x] Uma origem declarada que não está no tool set do run ou na sua lista-branca recusa o
      arranque, pela mesma regra do contrato impossível do AOS-493. **Acrescentado pela revisão:**
      também o recusa um nome de tool que a âncora selada não admite (mais de 128 bytes, espaços
      ou caracteres de controlo) — a mesma função valida a forma no arranque e no selo, pelo que
      nenhum run termina em memória e fica sem transição terminal. O `GET /runs/{id}` em memória
      responde `failed` a um run recusado assim.
- [x] **Regra de designação**, calculada no kernel (**apertada pela revisão**): no **primeiro
      turno do run que despachou tool calls**, e só se o contexto desse turno era **trusted**, a
      tool declarada foi **pedida exactamente uma vez** e essa chamada foi **efectiva** (sem
      recusa e sem erro de tool) ⇒ `designated`. Pedida **mais de uma vez** nesse turno, qualquer
      que seja o desfecho de cada chamada ⇒ `ambiguous`. Pedida uma vez e não efectiva, ou não
      pedida ⇒ `missing`. Contexto desse turno já untrusted pelas entradas do run ⇒
      `inapplicable`. Não se concatenam resultados. (A redacção original — «exactamente uma
      efectiva» — deixava quem controla a falha de uma chamada escolher qual resultado fica
      selado.)
- [x] A designação não depende do texto do modelo, do conteúdo do resultado nem de nada que o
      chamador envie além do nome da tool. Fixado por teste: mudar o texto final ou o conteúdo do
      resultado não muda o estado da designação.
- [x] A designação é a **mesma função** no loop e no motor de replay. Teste de paridade: para o
      mesmo run, o loop e a reconstrução a partir da captura dão o mesmo estado, o mesmo passo e o
      mesmo digest. Vale para os runs de uma só vida e para os retomados cujo turno designado não
      mudou de desfecho; os outros são limite declarado no ADR-038 §5.
- [x] Na transição terminal fica selado, ao lado do veredicto e **sem conteúdo**:
      `{tool, binding, state, step_id, digest, bytes}`. O digest é o SHA-256 dos bytes que o
      despacho devolveu nesta vida do run, calculado por quem executou a tool, no momento em que
      o turno é observado. Na via durável é o `result_hash` do step-ledger do passo designado
      (fixado por teste numa vida, depois de escalada e aprovação, e depois de falha e retoma); a
      captura do turno só tem esses bytes quando o turno designado não mudou de desfecho entre
      vidas. O mesmo registo vai no `Result` do run.
- [x] A origem declarada vai no manifesto de cada turno e no registo de retoma, com `omitempty`.
      Os goldens de runs sem origem declarada não mudam um byte. O `prompt_hash` não muda: o
      modelo não é avisado.
- [x] A designação é um **facto**, calculado e selado em qualquer modo de aplicação do veredicto
      excepto desligado. O vocabulário fechado do veredicto ganha duas razões,
      `output_source_missing` e `output_source_ambiguous`, que **só entram no veredicto com a
      declaração vinculativa e o nó em imposição** (ver «Vínculo por run»): aí fecham `failed`.
      Em observação, ou em «só medição», não há razão no veredicto e o que a imposição teria
      feito lê-se no estado da âncora. A precedência (corte, contrato, origem, saída vazia) fica
      fixada no ADR-038 §2.4 e por teste.
- [x] Num run com origem declarada **vinculativa e o nó em imposição**, `empty_output` avalia os
      bytes designados e não o texto final. Nas outras combinações avalia o texto, como sempre.
- [x] Testes, cada um com o estado esperado: zero chamadas (`missing`); uma chamada efectiva
      (`designated`); duas chamadas da tool no mesmo turno, com qualquer desfecho (`ambiguous`);
      chamada negada (`missing`); erro de tool (`missing`); segunda leitura num turno posterior,
      provocada pelo conteúdo da primeira (continua `designated` pela primeira, e a segunda nunca
      é a origem); run com `plan_input` ou com memória no contexto (`inapplicable`: o contexto é
      untrusted desde o turno 1), ao nível da função e do loop.
- [x] Retoma depois de um crash: a origem declarada sobrevive (registo de retoma), a designação
      recalcula-se do log e um resultado com êxito memorizado no ledger reproduz os mesmos bytes e
      o mesmo digest. Fixado por teste.
- [x] Um run gravado antes deste ticket reproduz-se no replay com o desfecho que teve.
- [x] ADR novo deste ticket escrito (MADR) e registado no catálogo: a saída de um nó de passagem
      directa é o resultado da tool, por referência. Decide a declaração no plano, a regra de
      designação, o transporte pelo `aos-orq` com âncora selada pelo kernel, o destino do texto
      final, e regista como **rejeitada agora** a alternativa em que o nó consumidor resolve a
      referência, com os seus gatilhos (o primeiro payload legítimo acima do tecto; a fase A5 ou
      A6; a reabertura do DEF-806).
- [x] Emendas no mesmo PR ao ADR-037: §2.4 (as duas razões novas) e §5 (o resíduo «evidência não é
      fidelidade» fecha para a passagem directa). As emendas aos outros ADR que o desenho lista
      ficam para os tickets que mudam o que eles descrevem, como o ADR novo regista: a leitura de
      desfecho de um run por referência (AOS-498), a origem declarada de um output (AOS-500) e a
      origem do payload (AOS-501). A RTM (`tecnica/16`) é regenerada no mesmo commit.
- [x] Revisão adversarial independente com mutações, antes da fusão. Feita a 2026-10-05: nenhum
      bloqueante; um run sem declaração medido contra a base (eventos, WORM e log iguais).
      Quatro achados a fechar antes de existir um chamador e 5 de 26 mutações vivas (duas
      equivalentes, três por falta de teste); corrigidos neste ticket, com a regra de designação
      apertada e o estado `inapplicable`.

### Vínculo por run (acrescentado a 2026-10-05, antes de implementar)

As razões novas não dependem só do modo de aplicação do nó. A declaração de origem leva um
vínculo por run, dado pelo chamador: «vinculativa» ou «só medição».

- [x] Com declaração, o kernel designa e sela sempre a âncora na transição terminal, com o
      estado designada, em falta, ambígua ou não aplicável, em qualquer modo do nó excepto
      desligado.
- [x] As razões `output_source_missing` e `output_source_ambiguous` só entram no veredicto
      quando a declaração é vinculativa e o nó está em imposição. «Não aplicável» fecha com
      `output_source_missing`: não há terceira razão.
- [x] Em «só medição», o desfecho de um run **que arranca** é exactamente o que seria sem
      declaração, também com o nó em imposição; o estado da âncora conta-se numa métrica de
      cardinalidade fechada (`aos_runs_output_source_total{binding,state}`, 8 séries). **Não é
      estritamente neutra:** uma declaração impossível ou mal formada recusa o arranque nos dois
      vínculos; quem mede (AOS-499) tem de declarar uma tool que esteja no tool set do nó.
- [x] O vínculo fica gravado com a declaração (manifesto de cada turno e registo de retoma); o
      replay reproduz sem ler configuração.

Motivo: o nó de produção está em imposição, e o AOS-499 tem de medir a designação em produção
sem falhar nenhum run.

### Fora de âmbito

- O campo no `POST /runs`, a resposta do `GET /runs/{id}` e o anúncio (AOS-498).
- O schema do plano (AOS-500), a medição no `aos-orq` (AOS-499) e a entrega (AOS-501).
- O transporte em que o nó consumidor resolve a referência: rejeitado agora, com gatilho.
- Concluir o run logo a seguir à chamada designada: mexe na regra única de terminação (AOS-492).
- Nós que transformam o conteúdo (resumir, classificar, extrair): o modelo continua no caminho.
- Agregar várias chamadas numa só saída.

### Limites conhecidos à partida

- A fidelidade é ao resultado da chamada **feita**. Se o modelo leu o documento errado, a origem
  designada é esse resultado. O `step_id` liga ao `tool.call.mediated` do mesmo passo, onde um
  auditor vê o recurso.
- Uma tool que falha no primeiro turno com tools e tem êxito num turno posterior dá `missing`: o
  resultado da falha já tornou o contexto untrusted. É uma falha honesta e conta contra a taxa
  de «não cumprido»; a observação (AOS-499) mede-a antes de se impor.
- As duas razões novas **não** seguem só o modo de aplicação do veredicto do nó (em produção, em
  imposição desde 2026-10-05): exigem também a declaração vinculativa. É o que a secção «Vínculo
  por run» decide, e o que deixa o AOS-499 medir em produção sem falhar runs.
- Uma segunda tentativa da tool declarada no mesmo turno é `ambiguous`, mesmo que a primeira
  falhe.
- Os limites que a revisão acrescentou estão no ADR-038 §5: a captura não tem os bytes de um
  turno designado que mudou de desfecho entre vidas (tem-nos o step-ledger); a captura de
  referência pode dar outra âncora no replay sem divergência visível quando o turno designado é
  o terminal; um envelope de sandbox com código de saída diferente de zero é designável; o
  `step_id` da âncora não identifica um só evento num run retomado; com bytes designados e texto
  final vazio o run é cumprido com texto vazio (o lado do `aos-orq` é do AOS-501).

### Estado

**IMPLEMENTADO e REVISTO (2026-10-05); por verificar em produção (invisível até haver
chamador).** A decisão está no ADR-038
(`docs/adr/ADR-038-a-saida-de-um-no-de-passagem-directa-e-o-resultado-da-tool.md`).

- **Declaração.** `Goal.OutputFromTool` e `Goal.OutputSourceBinding` (`measure` ou `binding`),
  gravados em `manifest.completion` e no registo de retoma. Nenhum chamador os envia: o campo no
  `POST /runs` é do AOS-498.
- **Designação e âncora.** `RunEvidence.FollowOutputFrom` e `RunEvidence.Observe` tiram os
  factos e o digest quando o primeiro turno com tools é observado; `ConcludeRun` devolve a
  âncora (`Result.OutputSource`) e o nó sela-a em `output_source`, na transição terminal.
  `Machine.RebuildOutcome` lê-a de volta.
- **Sem declaração nada muda.** Fixado por uma fixture gravada sobre a base, antes do código, e
  confirmado pelo revisor com um diferencial de nó próprio. O `/metrics` ganha 8 séries a zero.
- **Por fazer noutros tickets:** servir os bytes, lidos do step-ledger e conferidos contra o
  digest (AOS-498); medir (AOS-499); o plano (AOS-500); a entrega (AOS-501).
- **Por verificar:** nada disto correu em produção nem sobre JetStream; não há como o observar
  em produção enquanto nenhum chamador declarar a origem.

---

## AOS-533 — O replay verificável de produção: `GET /runs/{id}/replay` recalcula os `prompt_hash` de um run e devolve o veredicto de fidelidade, sem conteúdo

<!-- rtm: adrs-mencionados -->

| Campo | Valor |
|---|---|
| Epic | EPIC-02 |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — resíduo da sub-fase A5.1 |
| Tipo | feat |
| Prioridade | P1: a fidelidade de replay de produção só estava provada em testes e no gate `replay`; o critério P6 e o resíduo da A5.1 não têm por onde ser medidos num run real |
| Estimativa | M |
| Dependências | AOS-214 (replay soberano: as portas e o opener por-titular), AOS-530 (a série A5.1 e o resíduo), ADR-037 §5 (o replay de um run retomado diverge) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` §7, `docs/adr/ADR-037-o-desfecho-de-um-run-e-um-veredicto-do-kernel.md` §5, `packages/kernel/agent-runtime/replay/engine.go`, `packages/cmd/aos/sovereign_replay.go`, `packages/integration/resume_records.go`, `tecnica/12_Contratos_de_Interface.md` §5.1 |

### Contexto

Resíduo da sub-fase A5.1 (2026-10-18, §7 do acompanhamento): **o replay de produção não recalcula
os `prompt_hash`**. O `GET /runs/{id}/reconstruct` (AOS-214) é o `ReplayEngine` em modo de
reconstrução — decifra as capturas, sem efeitos e sem chamar o modelo nem a tool — e na série A5.1
bateu com a resposta servida em 8 de 8 runs. A verificação (`ReplayEngine.Replay`) não tem rota de
produção: está provada nos testes (`TestAOS530_No_ReplaySemNovaChamada`) e no gate `replay`.

O que a descoberta estabeleceu:

- `Replay(ctx, runID, Options{Spec})` devolve `ReplayResult` (turnos com `prompt_hash` gravado e
  re-materializado, fidelidade, âncoras corridas, divergência localizada, veredicto do AOS-493,
  hash do estado final). Divergência não é erro. Sem modelo, sem tools, sem escrita.
- **A spec não está no log** (só os hashes). Está no registo de retoma do run
  (`ResumeRecords.Get`), que o nó escreve no arranque de cada hospedagem, cifrado por titular, e
  que só existe com o four-eyes composto e com titular. Em produção (v0.1.59) os 40 runs de nós da
  série A5.1 têm `resume-<run>`.
- O tool set mais seguro é o `manifest.tools` do primeiro turno (o que o run ofereceu, AOS-486) —
  o precedente do `hashDaSementeCom` (AOS-506), que já recalcula o turno 1 em produção.
- O `manifest.model.params` funde os params do perfil da rota: a âncora `model` com os do registo
  daria falsos «model».

### Objectivo

Uma rota de leitura soberana que corre o `ReplayEngine.Replay` sobre um run e devolve o veredicto
de fidelidade, sem conteúdo decifrado.

### Âmbito

- `GET /runs/{id}/replay`, no plano de dados, com as **mesmas portas do `/reconstruct` pela mesma
  ordem** (uma só função partilhada) e selo WORM próprio, `read:replay`.
- A spec montada no servidor: system, objectivo, memory_context, inputs do plano e aviso da nova
  tentativa do registo de retoma; tools do manifesto do primeiro turno. Modelo, versão do
  assembler, sequência de step_id e autoridade ficam fora, e a resposta declara-os.
- Run retomado marcado na resposta; não amostra o SLI.
- O SLI `replay_fidelity` passa a ter produtor a pedido (o span `replay`, pelo tracer do nó).

### Critérios de Aceitação

- [x] A rota responde só com hashes, contagens e vocabulário fechado: `run_id`, `fidelity`
      (valor, coincidentes, verificados), `turns` verificados, `diverged`, `divergence` (turno,
      step, motivo, hash esperado e obtido), `anchors_verified`, `anchors_not_verified`,
      `final_state_hash`, `verdict`, `spec_origin` (`resume_record`), `retomado`, `nota`,
      `sli_amostrado`. Nunca o texto do modelo, os resultados das tools, o objectivo nem os inputs
      (sonda de marcadores, com o `/reconstruct` como controlo de que são decifráveis).
- [x] Portas: leitor de outro board ⇒ 404 igual ao do run inexistente, sem selo; sem gate soberano,
      sem cifra ou sem registo de retoma composto ⇒ 501 com causa, sem selo; o selo `read:replay`
      está no WORM antes da resposta, sem PII.
- [x] Recusas com causa própria e sem 500 genérico: sem registo de retoma ⇒ 422
      `sem_registo_de_retoma`; registo recusado ⇒ 422; captura incompleta, corrompida, layout ou
      aviso desconhecidos ⇒ 422; captura de referência sem PayloadStore ⇒ 501 (limitação do nó, não
      defeito do run); depois do `/dsar/erase` ⇒ 410; custódia sem resposta ⇒ 503.
- [x] Fidelidade total, no nó composto, para um run normal, um nó de plano com inputs, um run com
      a `ocr_read` e uma nova tentativa com aviso; os controlos in-process provam que sem os inputs
      ou sem o aviso a spec diverge no turno 1, e que outro objectivo diverge por `prompt_hash`.
- [x] Uma captura adulterada escrita crua no stream diverge no turno seguinte por `prompt_hash`.
- [x] Um run retomado por aprovação sai `retomado: true`, diverge no turno 2 por `prompt_hash`
      (medido: confirma a inferência do ADR-037 §5 para a retoma por aprovação) e leva a nota de
      que a divergência num run retomado não prova adulteração; não amostra o SLI.
- [x] «Retomado» não se forja com uma linha (1.ª revisão): exige a cadeia de transições coerente
      da máquina de estados e, para cada claim de lease k ≥ 2, um par na cadeia — por ORDEM, a
      (k−1)-ésima transição de retoma (`waiting_on_human → running` ou `paused → running`, que não
      levam token) ou, sem ela (re-hospedagem por crash), a transição que leve o token k. A PoC da
      1.ª revisão (captura forjada + `waiting_on_human → running` depois do `complete`) dá
      divergência normal, com amostra no SLI e o alerta `replay_fidelity_low` a disparar; um claim
      forjado sem par não corrobora. Só o primeiro turno gravado DEPOIS (por seq) do par de cada
      vida é desculpado; noutro turno é divergência normal, mesmo com `retomado: true`.
- [x] Nenhum cenário legítimo medido dispara o alerta (2.ª revisão): duas retomas por aprovação
      (consultado a meio, em `waiting_on_human`, e depois do `complete`), retomado e morto em
      `waiting_on_human` (a forma do abort de exaustão e do human-TTL), retoma por crash pela
      varredura de arranque (fiel, com amostra de valor 1). Um erro a ler o lease é 503
      `lease_indisponivel`, sem amostra. A retoma por pausa está coberta pela forma da cadeia
      (unitário), não pelo nó: nenhum teste do repositório consegue uma retoma de pausa pelo HTTP.
- [x] System e memory_context guardados por teste: fidelidade 1 com eles, divergência no turno 1
      sem cada um.
- [x] SLI `replay_fidelity`: com a torneira composta, uma chamada é uma amostra com o valor da
      fidelidade; `produtor="1"`; o banner do avaliador diz que a amostragem é a pedido, por
      chamada e não por run.
- [x] Documentado em `tecnica/12_Contratos_de_Interface.md` §5.1 (o `/replay` e o `/reconstruct`,
      que não estava) e na tabela de planos do `deploy/node/README.md`.
- [x] Uma série em produção que use a rota. É o que fecha o resíduo da §7 — não este ticket.
      *(2026-10-18, v0.1.60: os 40 runs de nós da série A5.1 lidos pelo dono por
      `GET /runs/{id}/replay` — fidelidade 1 em 40/40, 60/60 turnos, 0 divergências, 0 retomados.
      O resíduo da A5.1 fica fechado; acompanhamento §4 e §7.)*

### Fora de âmbito

- Reproduzir com fidelidade um run retomado cujo turno re-executado mudou de desfecho (ADR-037 §5):
  a rota só o distingue.
- As âncoras `model`, `assembly_version`, `step_id` e `authority`.
- O PayloadStore de mode 3 no nó.
- Amostragem periódica do SLI (por run, sem chamada).

### Notas

- O detector de «retomado» cruza duas fontes: a cadeia de transições da máquina de estados e o
  stream de lease, um claim por vida. As transições de retoma não levam token (o kernel não muda
  por isto: mudaria payloads, goldens e streams gravados), pelo que o par de cada claim é por
  ordem; só a re-hospedagem por crash se corrobora pelo token, e aí o par é posterior aos turnos
  dessa vida — nenhum turno dela é desculpado, e uma divergência nela (a do ADR-037 §5 depois de
  uma falha de tool) sai normal. Com crashes e aprovações intercalados, o emparelhamento por ordem
  pode atribuir a desculpa à vida errada. Quem escreve cru nos dois streams com a cadeia inteira
  certa ainda forja uma retoma, mas a desculpa fica presa a um turno por retoma.
- **Resíduo (3.ª revisão): a divergência desculpada pára o motor nesse turno.** Os turnos
  seguintes de um run retomado ficam por verificar, e uma captura adulterada a partir do turno
  desculpado fica silenciada nessa chamada (200, `retomado`, nota, sem amostra). Fica visível —
  `fidelity.turns_recorded` ao lado de `verified`, e a nota diz que os turnos depois do desculpado
  não foram verificados —, mas não fechado. A correcção estrutural é gravar a captura da
  re-execução sob uma chave por vida, ou reproduzir o turno re-executado do `applied_result` do
  step-ledger: ticket próprio, não este.
- Uma vida de crash que acaba em `waiting_on_human → killed` (sem token) não é corroborada: o run
  sai `retomado: false`, com amostra.
- `anchors_not_verified` declara sempre `tools_from_log` (o tool set vem do `manifest.tools` do
  próprio log: coerência com o log, não fonte independente) e `projection_version` (a versão da
  projecção do AOS-513 não tem âncora).
- Custo: o `ResumeRecords.Get` lê o stream das aprovações inteiro a cada pedido — O(aprovações);
  a rota está no balde de leituras do AOS-458.
- O `memory_context` não tem caminho de produção no nó (nenhum handler o escreve no Goal); o
  `system` chega pelo campo `system` do `POST /runs`.
- A amostra do SLI é por chamada: o mesmo run verificado duas vezes conta duas.

### Estado

**IMPLEMENTADO (2026-10-18), por verificar em produção.** `packages/cmd/aos/replay_verificavel.go`
(a rota), `packages/cmd/aos/sovereign_replay.go` (as portas partilhadas com o `/reconstruct`),
`packages/cmd/aos/sli_produtor.go` e `slo_evaluator.go` (o SLI e o banner). Testes em
`packages/cmd/aos/aos533_replay_verificavel_test.go`; mutações (objectivo, system ou memória
tirados da spec, selo retirado, detector de retoma desligado, transição aceite em qualquer posição,
claim de lease não usado aceite, divergência desculpada em qualquer turno, emparelhamento por
ordem retirado, carimbo de tempo em vez de seq, erro do lease tratado como «sem corroboração»,
desculpar qualquer turno ≥ ao desculpado, `turns_recorded` a zero, tools vazias, tracer retirado)
avermelham-nos todas. Um run retomado cujo turno desculpável bate (a autonomia sobe para L5 antes
da retoma e a captura do turno 1 é reposta com o resultado real) e uma captura posterior forjada
dão divergência normal no turno 3, com amostra e alerta.

---

## AOS-551 — A separação de planos, o MECANISMO: layout 1.8.0, `plan_input` e `tool_result` por handle, autoridade por argumento que conta a media (fase R2 do ADR-043)

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Harness por capacidades (ADR-043), fase R2 |
| Tipo | feat |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-550 (o handle e a sua resolução) e AOS-552 (a linhagem), ambos fundidos. **Não depende da cerimónia do dono:** a política de fluxo de dados, o interruptor accionável e o ADR próprio são o AOS-556, e é ele que fecha o DEF-806 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-r2-entradas-por-handle-2026-10-23.md` (§3, §4 e §5) — **lê a §10, «o que a v1 dizia e caiu»**; `docs/adr/ADR-034-autorizacao-derivada-do-contexto.md` (§3 opção A e alternativa E, §5.4); `docs/governance/REGISTO-Deferimentos.md` (DEF-806) |

### Contexto

**PARTIDO EM DOIS a 2026-10-24.** O ticket original juntava o mecanismo e a política de fluxo de
dados; a política exige regras Cedar novas, re-assinatura do bundle e **cerimónia da chave do dono**,
e um ticket com uma dependência de cerimónia a meio é um ticket que ninguém pode fechar — a caixa
fica por marcar sem que isso signifique trabalho por fazer. O mecanismo fica aqui, **desligado e com
o interruptor fail-closed**; a política, o interruptor accionável, o ADR próprio e o fecho do
**DEF-806** são o **AOS-556**.

O que o mecanismo sozinho NÃO compra, e é honesto dizê-lo: sem as regras de fluxo, uma call que
consome um handle untrusted continua negada pela regra binária que já existe. É seguro, e é
exactamente a v1 que a revisão derrubou por «não comprar nada». **O ganho de produto chega com o
AOS-556.** O que este ticket entrega é a máquina, provada e inacessível.

O **DEF-806** nomeia o defeito que esta máquina existe para fechar: «a AUTORIZAÇÃO já é derivada do
contexto (opção C) … mas o conteúdo untrusted continua a entrar **INLINE no tail que o modelo lê**,
pelo que um efeito cujos argumentos venham desse conteúdo não tem separação». O código declara-o no
mesmo sítio (`loop.go`, `prompt.go`). **O fecho do deferimento é do AOS-556**, porque só com a
política ligada é que a separação muda alguma decisão.

**Três correcções da v2, todas de revisão adversarial confirmada por leitura do código:**

1. **A autoridade conta a media.** A premissa «o modelo que decide deixa de ver conteúdo untrusted» é
   **falsa** no layout em que a R2 vive: `projection_media.go:91-111` resolve a `MediaRef` e faz
   `m.AttachMedia(parte)` — é a funcionalidade da R1a (`media_turno.go:16-19`). Se a `PlannerView`
   tornasse o contexto trusted por construção, o texto do próprio modelo passaria a segmento trusted
   e uma injecção visual lavava-se para trusted — caminho que **hoje morre**.
2. **A política decide por fluxo de dados, não por um binário.** Com a regra binária da v1, uma call
   que consome um handle untrusted continuava negada — logo a R2 não compraria «gerar a partir do que
   se viu», que é o seu motivo de existir.
3. **A 1.8.0 deriva da 1.7.0.** Os layouts são cumulativos (`layout.go:113-131`) e a 1.7.0 exige a
   1.6.0 (`media_turno.go:70`, `:107-113`). A 1.8.0 herda o adiamento do AOS-540 — pelo que **não há
   ganho em turnos** — e deixa o anúncio do AOS-542 e a regra P4 do `aos-orq` intactos.

### Critérios de Aceitação

- [x] Layout **1.8.0, derivado da 1.7.0**: `plan_input` e `tool_result` entram no tail **como
      handle** — rótulo com o digest e corpo fixo —, nunca como conteúdo. A mecânica é a que já existe
      duas vezes: o `args_digest` da 1.4.0 (`layout.go:344-367`) e a `media_view` da 1.7.0
      (`media.go:143-155`).
- [ ] **DECISÃO DO DONO PENDENTE (medido a 2026-10-25).** Este critério não é cablagem adiada: é
      a arquitectura dual-LLM inteira (`SeparatePlanes` → `PlannerView` → `ControlPlanner`), com
      ZERO chamadores de produção hoje. E a sua segunda metade contradiz a **correcção 1 deste
      ticket**: a media continua PROJECTADA para o modelo (`projection_media.go`), logo a
      `PlannerView` não pode ser «exactamente o que o modelo vê» sem desfazer a R1a. Fica a decidir
      se sai para ticket próprio ou se o enunciado é emendado — não é leitura de código.
- [ ] `SeparatePlanes` ganha chamador de produção, e a `PlannerView` é exactamente o que o modelo vê.
- [x] **A autoridade de uma call é**
      `join(trusted, taint de cada handle resolvido nos argumentos, taint de cada segmento de media
      projectado no run)`, calculada **no `CallRewriter`**, sobre a mesma `Call` que o RM vê. Uma call
      sem handles e num run sem media é trusted mesmo depois de o run ter lido — é o limite que se
      levanta. **Num run com media, nada se levanta**, e isso tem guard-test.
- [x] **O interruptor da 1.8.0 não fica accionável.** A restrição vale nos **dois** sentidos: a
      separação sem a autoridade por argumento troca «negar a mais» por «permitir a mais», e a
      autoridade por argumento com o conteúdo ainda inline é um **downgrade** da protecção actual —
      o modelo copia o que leu para um argumento literal e o `join` autoriza. **Correcção de 2026-10-24,
      por medição:** o ticket dizia que a exigência vive no construtor do layout, e não pode —
      `layoutFor` é uma função PURA sem colaboradores, e o molde citado (`comporMediaNoTurno`) vive
      no `cmd/aos`. **Segunda emenda, de 2026-10-24, por medição:** a imposição também NÃO pertence
      à composição do nó. O `Runtime.Run` não é puro e tem as duas coisas na mão — o layout do run
      e as portas injectadas —, pelo que o portão vive LÁ (`ErrArgumentAuthorityRequired`) e é
      estrutural: entregar ao RM um rótulo mais baixo e confiar que o nó se lembra de o levantar é
      uma promessa sobre os chamadores, o padrão que derrubou a v1 das duas fatias anteriores. O
      banner do nó DECLARA a postura; não é ele que a impõe. E a declaração não é a garantia: o
      kernel **re-junta o piso** depois da reescrita, pelo que ela só consegue SUBIR. Enquanto o AOS-556 não ligar a política, o interruptor fica **sem forma de
      ser accionado em produção**, e isso é declarado.
- [ ] **Autoridade por call no replay.** `ReplayedTurn.Authority` é hoje **uma** string por turno
      (`replay/engine.go:611-621`) e `authorityDivergence` exige que todos os taints selados do turno
      sejam iguais a ela (`:841-848`): um turno misto dispararia `Reason="authority"` falso. Passa a
      recalcular por call, chaveado pelo `ToolStepID`, e a comparar listas. **O deferimento é MAIOR
      do que esta frase, medido a 2026-10-24 pela fatia 2:** desde que a autoridade é por argumento,
      o motor NÃO consegue recalculá-la — a captura não grava, por call, que handles a reescrita
      resolveu. O que ele reconstrói é o PISO, e por isso a verificação num layout por handle passa
      a ser do LIMITE (um selo abaixo do piso é divergência; acima é a autoridade por argumento).
      Verificar por igualdade exige gravar a resolução por call na captura, o que é schema novo —
      e é esse o trabalho real deste critério.
- [x] **O PRODUTOR das duas metades da 1.8.0** — a peça que faltava a este ticket e que não era
      critério de NENHUM dos dois (medido a 2026-10-25: o AOS-551 dizia-a «fora de âmbito» por
      implicação nas linhas 1.19 e 1.21, e o critério 4 do AOS-556 pressupõe-na sem a nomear).
      Sem ela a 1.8.0 não tem run em que corra, e três fatias entregues implementaram LEITORES de
      uma forma que nada escreve. **Duas metades, e nenhuma tem hoje um só escritor de produção:**
      (i) o `tool_result` — o resultado de cada call PERMITIDA é selado em handle e o corpo passa a
      ser a referência; (ii) o `plan_input` — `PlanInput.Handle` não é atribuído em nenhum ficheiro
      de produção (só no helper do diferencial de replay), e `conferirEntradas` recusa o arranque.
      **O ASSENTO DO PRODUTOR DO `tool_result` É ÚNICO, e está medido:** dentro do closure de
      `ledger.Apply` em `activity/dispatch.go`, entre o `rm.Mediate` que permitiu a call e o
      `durable.Result{Payload: dec.Output}` que o ledger grava. É o único ponto que é, ao mesmo
      tempo, (a) DEPOIS da decisão do RM, logo não viola o AOS-003; (b) ANTES da escrita no ledger,
      logo o ledger, o loop, o replay e a rota que serve os bytes designados vêem os MESMOS bytes e
      a âncora é confirmável; (c) com contexto POR RUN — o `ctx` já transporta o titular pelo mesmo
      mecanismo (`durable.ContextWithTitular`), e a quarentena é por `(titular, run)`. Um envelope
      que CHAME `Dispatch` — o que a linha 1.19 e o comentário da `layout.go` descrevem — corre
      DEPOIS da escrita no ledger: o ledger ficaria com o conteúdo e o loop com a referência, e a
      âncora deixa de conferir. **Entra na mesma fatia que as decisões do kernel que desbloqueia**
      (a recusa da 1b, que só é seguro apagar com produtor; a designação pelo piso), com testes que
      passem pelo LOOP. E inclui `projecaoNativaSuporta` (`platform/model-gateway/projection.go`),
      que não conhece a 1.8.0 e que também não era critério de nenhum dos dois tickets.
      **FEITO a 2026-10-25, e com uma mudança de desenho sobre o enunciado:** a recusa da fatia 1b
      não se apaga — passa a medir a **FORMA DO VALOR**. Num layout por handle designa-se se, e só
      se, os bytes que o despacho devolveu forem uma referência bem formada
      ([ParseHandleRef], o mesmo vocabulário do tail). Isso cobre os dois lados sem depender de
      ninguém se lembrar de compor o selo: com produtor designa (a âncora atesta a referência, que
      é o que o ledger tem e o que os dois verificadores re-hasheiam); **sem** produtor não designa
      (e era este o caso fail-OPEN que o enunciado deixava aberto — recusar por LAYOUT também o
      cobria, mas só enquanto nada escrevesse). A versão do layout chega à fronteira do despacho
      pelo **`ctx`, posto pelo `Runtime.Run`** e não pelo nó: é o mesmo `layout` que dá o prefixo e
      o manifesto, pelo que o gate do produtor é idêntico POR CONSTRUÇÃO ao do tail; derivá-la no
      `hostRun` ou no `integration` (do `Goal.AssemblyVersion`, que vem vazio num run novo e cai no
      default do runtime) era uma segunda derivação e uma promessa sobre os chamadores. A ausência
      da versão **RECUSA** (`ErrSealWithoutAssemblyVersion`), nunca assume o layout antigo.
- [x] **A âncora da designação de origem.** `designar` exige contexto trusted no primeiro turno
      com tools «para garantir que os argumentos foram escolhidos por um modelo que só tinha visto o
      prefixo e o objectivo», e num layout por handle isso é verdade em mais turnos — a medida passa
      a ser o **piso** e não a autoridade do contexto.
      **EMENDA AO CRITÉRIO, medida a 2026-10-25 em duas tentativas falhadas, em três partes.**
      (a) **A âncora NÃO se redefine no digest do conteúdo**, que é o que a fatia 1b declarou que a
      fatia seguinte faria. Não pode: os dois únicos sítios que a conferem re-hasheiam os bytes que
      detêm — `cmd/aos/origem_na_api.go` o payload do step-ledger e
      `cmd/aos-orq/entrega_por_referencia.go` os bytes que o nó serviu —, pelo que uma âncora com o
      digest do conteúdo é inconfirmável pelos dois e o run fecharia `fulfilled` com a saída
      designada **indisponível de vez**. A âncora atesta, e tem de continuar a atestar, os bytes que
      o despacho devolveu. (b) **Este critério é INSEPARÁVEL do produtor.** Apagar a recusa da 1b
      sem produtor é **fail-OPEN**: a rejeição de um corpo não-referência acontece na materialização
      do tail e não toca no resultado CAPTURADO, pelo que a chamada continua efectiva e a designação
      ancora no **conteúdo cru** — conteúdo que o tail esconde do modelo e que o `aos-orq` entregaria
      a jusante como saída do run. Enquanto não houver produtor, a recusa da 1b é a postura correcta
      e tem de ficar. (c) **O veredicto não muda para `output_source_missing`, muda para
      `fulfilled`:** `output_source_missing` é exactamente a razão com que o `inapplicable` já fecha
      (`ConcludeRun` fecha os dois com a mesma razão) e `output_source_inapplicable` não existe em
      `OutcomeReasons` — quem o procurasse num log não o encontrava.
- [x] Um só rótulo para todos os leitores (ADR-034 §2.3): o classificador de risco lê o mesmo valor
      (`risk_gate.go:373`, `:511`).
- [x] **Paridade nas duas vias**, provada como o AOS-069, o AOS-538, o AOS-540 e o AOS-541 a provaram.
      O comportamento em `ModeReplay` e em dedup fica declarado: o `Dispatcher` devolve o output
      registado sem efeito e sem mediação (`activity/contract.go:195-207`).
      **EMENDA, medida a 2026-10-25:** a frase original dizia «pelo que uma call deduplicada nunca
      resolve nada», e é FALSA. A reescrita — e com ela a resolução do handle e o cálculo da
      autoridade — corre na CONSTRUÇÃO da Call, antes de `rt.dispatcher.Dispatch`, enquanto a dedup
      decide-se DENTRO do `Dispatch`. Uma call deduplicada **resolve**; o que ela não faz é mediar
      nem produzir efeito. O código di-lo em voz alta no sítio oposto (`cmd/aos/handles.go`: «a
      reescrita corre em TODAS as construções de Call, também numa retoma ou numa call que o
      dispatcher durável vai deduplicar do step-ledger»), e é essa a declaração que este critério
      pede.
- [x] A bateria de injecção continua verde e ganha dois casos: uma injecção dentro de um handle que
      **é** consumido por uma call privilegiada — que com a regra binária de hoje continua NEGADA, e
      é essa a afirmação que se fixa —; e uma injecção visual numa imagem num run em 1.8.0, que
      prova que a autoridade a contar a media não levanta nada nesse run.
- [x] Desligado por omissão.

### Fora de âmbito

- **R7 do ADR-034 permanece aberto:** conteúdo colado no `objective` continua a comandar no turno 1
  (`api.go:1178-1188`, `TestAOS069_InjeccaoNoObjectivoPassaAClausulaDeTaint_R7`).
- **Fluxo de controlo dependente de dados:** o planeador vê a ordem, a contagem e o tipo dos handles
  e escolhe qual passa a que tool. Classe declarada pela CaMeL; fica resíduo.
- **Injecção visual numa imagem** não é fechada por este desenho. O que a contém é a autoridade
  contar a media.
- A travessia da aresta do plano (AOS-553).
- A retirada do AOS-540 e do AOS-542: não se aplica, porque a 1.8.0 herda o adiamento.

---

## AOS-556 — A política de fluxo de dados e o interruptor da 1.8.0 (fase R2 do ADR-043; fecha o DEF-806)

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Harness por capacidades (ADR-043), fase R2 |
| Tipo | feat |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-551 (o mecanismo, desligado). **Cerimónia do dono: regras Cedar novas e re-assinatura do bundle.** Nasce com um **ADR próprio** — o ADR-038 §3 exigiu-o ao rejeitar «o nó consumidor resolve a referência», e o número atribui-se na emissão |
| Responsável sugerido | Arquitecto de Plataforma + dono (assinatura) |
| Documentos de referência | **`docs/adr/ADR-044-a-politica-de-fluxo-de-dados-decide-por-destino-e-proveniencia.md`** — o ADR próprio que este ticket exigia, com as duas incógnitas medidas e a decisão do dono de partir a `allow_fs_read`; `docs/reports/desenho-r2-entradas-por-handle-2026-10-23.md` (§4.2 e §6); `docs/adr/ADR-034-autorizacao-derivada-do-contexto.md` (§3 alternativa E); `docs/governance/REGISTO-Deferimentos.md` (DEF-806) |

### Contexto

O AOS-551 entrega a separação de planos como **máquina desligada**: o conteúdo untrusted sai do tail
e viaja por handle, e a autoridade de uma call calcula-se por argumento. Com isso sozinho, uma call
que consome um handle untrusted continua **negada** pela regra binária que existe hoje
(`allow_http_post`, a única que lê o taint) — ou seja, a separação não muda nenhuma decisão, e o
motivo de existir da R2 («gerar a partir do que se viu sem uma pessoa a aprovar cada chamada») não
se realiza.

Este ticket é o que o realiza, e é o que fecha o **DEF-806**.

**Duas incógnitas medidas a 2026-10-24, e que este ticket tem de resolver antes de escrever a regra:**

1. **Pode não ser só política.** O `context` declarado no bundle é `{ taint : String, sensitivity :
   String }`. Uma decisão por *(capability, destino, proveniência do que entra)* pode exigir campos
   novos no `Input` do PDP — isto é, mudança de **motor**, não só do ficheiro assinado. Medir primeiro.
2. **O gate `policy-taint.py` exige a cláusula do taint como conjunção de topo** e tem uma lista
   FECHADA de isenções por capability. Uma condição composta cai no caminho «não cumpre» e precisa
   ou de baseline (que só encolhe) ou de uma forma nova reconhecida pelo gate. A sintaxe da regra
   desenha-se **com** o gate, não contra ele.

### Critérios de Aceitação

- [ ] **Política de fluxo de dados em Cedar assinado:** a decisão é por (capability, destino,
      proveniência do que entra), e não por um binário. Um handle untrusted pode fluir para o gateway
      de modelo e **não** para o `web_post`. O SA-ROC do ADR-013 **não muda** e continua a classificar
      pela sensibilidade.
      **AS DUAS CAMADAS APRENDERAM A COORDENADA (decisão do dono, 2026-10-25).** A revisão
      adversarial mediu que a regra, sozinha, não mudava nenhuma decisão: a política permitia e o
      **TaintGate negava a seguir**, porque a `ocr_read` declara `egress: external` e o gate corre
      depois da política. O dono decidiu ensinar-lhe a mesma coordenada — a terceira isenção,
      `fluxo_por_handle`, com as três condições do **ADR-044 §2.10** —, o que RELAXA a invariante P0
      do ADR-005 para a classe `external_read` e está emendado no ADR-034.
      **O que falta deste critério é só a ASSINATURA**, e o seu valor mudou: com a isenção no gate o
      ganho já existe com a 1.1.0 em vigor, pelo que assinar a 1.2.0 passa a ser o que torna a
      decisão explícita e o que fecha a dívida do critério 6 do AOS-363 — governação, não
      capacidade. Medido em `TestAOS556_Bateria_ACadeiaRealDecideOQue` (as duas camadas permitem) e
      em `TestAOS556_IsencaoDoFluxoNaoSeEstende` (as três condições, com os controlos negativos).
- [x] O gate `policy-taint` conhece a forma nova das regras de fluxo, e continua a recusar um
      `permit` sem cláusula fora da baseline. A baseline **não cresce** para acomodar este ticket.
- [x] Se a decisão exigir campos novos no `Input` do PDP, a mudança de motor é declarada e tem
      guard-test próprio; se não exigir, fica declarado que foi medido e não exigiu.
- [ ] **O interruptor da 1.8.0 passa a accionável**: o colaborador da autoridade por argumento
      compõe-se, o nó arranca com a política nova em vigor, e o banner declara a postura.
      **PARCIAL (2026-10-25), e o que falta é da cerimónia.** FEITO: o colaborador compõe-se
      (medido — entra sempre na cadeia e corre em cada tool call); o interruptor existe nas DUAS
      vias, `AOS_CONTEUDO_POR_HANDLE` a habilitar e `conteudo_por_handle` no `POST /runs` a
      escolher por run, com **400** num nó que não habilite e com a habilitação a exigir também o
      produtor do `tool_result`; e a postura sai no banner, com as três frases que a 1.8.0 tornava
      falsas corrigidas. FALTA: «o nó arranca com a política nova EM VIGOR», que é a assinatura.
- [x] **OS QUATRO RESÍDUOS DO AOS-551 QUE BLOQUEIAM ESTE INTERRUPTOR**, medidos a 2026-10-25 e
      declarados na linha 1.22. Nenhum é defeito vivo hoje, porque a 1.8.0 é inalcançável; todos
      passam a ser no instante em que este critério fecha.
      (a) **A aresta do plano entrega um token em vez de falhar com causa.** Com produtor, um run
      que VINCULE a saída designa e os três passos de `aos-orq/entrega_por_referencia.go` conferem
      — os dois lados hasheiam a referência —, pelo que o nó consumidor recebe `aos-handle/v1:…`
      como conteúdo do seu `plan_input` e nunca o pode abrir. Antes era `output_source_missing`,
      uma causa explícita. É do **AOS-553**, e sem ele este interruptor troca uma falha legível por
      uma entrega silenciosa de lixo.
      (b) **A `artifact_write` sela duas vezes**: o modelo recebe a referência de um documento que
      CONTÉM a referência do artefacto, e a de dentro nunca se resolve. Resolve-se por contrato de
      tool (uma tool que já devolve referência declara-o), não por um discriminador por `ToolID` no
      produtor — a linha 1.19 mediu que esse não generaliza.
      (c) **O observador do Goal hospedado não vê os handles** (`SubmitObservando` corre antes do
      selo, porque o layout só é fixado no `hostRun`), o que dá um `prompt_diferente` espúrio num
      run com entradas E aviso de nova tentativa. Conservador — lê-se sempre como DIFERE, nunca
      como igual —, mas é ruído numa medição que existe para ser lida.
      (d) **Um `handle.created` por entrada é escrito antes do registo de retoma**, pelo que uma
      recusa a meio ou um crash deixa declarações órfãs e bytes no espaço até alguém limpar a
      pasta.
- [x] A bateria de injecção ganha o caso que o AOS-551 não pode ter: uma injecção dentro de um handle
      consumido por uma call privilegiada **cujo destino a política permite** — e prova que o que
      flui é o que a regra autoriza, não o que o modelo pede.
- [ ] **O DEF-806 fecha**, com a entrada do registo actualizada e a prova nomeada.
- [ ] Prova em produção: um plano em que o nó 2 usa uma tool protegida sobre o que o nó 1 leu, sem
      aprovação humana por chamada.
      **NÃO É POSSÍVEL COM AS TOOLS DE HOJE, e é medição e não suposição (2026-10-25).** A `ocr_read`
      e a `video_read` têm um só parâmetro (`doc_id`) e ele preenche o `resource_value`, logo é um
      **slot** — e a resolução de handles RECUSA uma referência num slot do recurso
      (`handleCausaSlot`), porque o recurso auditado e o recurso tocado deixariam de ser o mesmo.
      Nenhum handle chega a essas duas tools. A isenção está correcta e **não tem hoje tool que a
      exerça**: abre uma decisão, não um caminho. O caminho chega com uma tool cujo argumento de
      conteúdo não seja o slot do recurso — a `model_invoke` genérica da fase **R3** —, pelo que este
      critério não pode fechar antes dela. Ver ADR-044 §7.4.
- [x] Desligado por omissão até o bundle assinado estar em vigor.

### Fora de âmbito

- O mecanismo (AOS-551): layout, tail por handle, autoridade por argumento, replay, âncora da
  designação de origem.
- A travessia da aresta do plano (AOS-553).
- **R7 do ADR-034**, a injecção visual e o fluxo de controlo dependente de dados, que o desenho
  declara como classes abertas.


## AOS-557 — Os portões fail-closed do run correm ANTES do selo das entradas por handle

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Harness por capacidades (ADR-043), fase R2 |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-556 (os quatro resíduos) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/adr/ADR-043-harness-por-capacidades.md` (§2.6); o bloco do selo em `packages/cmd/aos/service.go` ([NodeService.hostRun]), que declara esta janela |

### Contexto

Achado da revisão adversarial do AOS-556, e é uma **terceira causa** de declarações órfãs, distinta
das duas que o critério 5 desse ticket nomeia (a recusa a meio, fechada; e o crash, infechável).

`selarEntradasDoPlano` sela as entradas na hospedagem. Depois dele, `agentruntime.Runtime.Run` tem
seis portões fail-closed que recusam **sem um único efeito**: o layout, o portão da autoridade por
argumento, a abertura do tail, a janela, o contrato de conclusão e a origem da saída. Dois são
accionáveis por quem submete — `contratoPossivel` e `origemPossivel` —, e a porta HTTP declara em voz
alta que não vê a metade que o kernel vê (`validarContratoDeConclusao`: «O QUE ISTO NÃO VÊ: se o nó
oferece a tool»). Logo: um `POST /runs` num layout por handle, com entradas e um `completion_requires`
que o nó não pode cumprir, sela todas as entradas e fecha o run sem turno. Os bytes ficam no espaço
de handles e nada os apaga. Repetível por submissão, até o tecto do volume fazer a selagem recusar em
**todos** os runs.

**Não é defeito vivo:** a 1.8.0 continua inalcançável. Passa a ser quando o interruptor fechar.

**A correcção ingénua é pior, e está medida:** desfazer no erro do `Run` acontece depois de
`persistCrashResumeRecord`, pelo que deixaria um run RETOMÁVEL cujas entradas não têm bytes — perda
silenciosa de capacidade em vez de bytes a mais.

### Critérios de Aceitação

- [x] Os portões que são função apenas do Goal e da oferta do nó correm **antes** de
      `selarEntradasDoPlano`, de modo que uma recusa deles não sele nada. No mínimo o contrato de
      conclusão e a origem da saída, que são os accionáveis por quem submete.
- [x] A ordem fica imposta por teste, e não por comentário: um run com um contrato impossível e
      entradas num layout por handle não deixa `handle.created` nenhum no stream nem bytes no espaço.
- [x] O que não puder subir fica nomeado, com a razão, no bloco do selo em `hostRun`.
- [x] A janela do **crash** continua declarada e não é fechada aqui.
- [x] Nenhum portão passa a correr duas vezes com semânticas diferentes: se a conferência subir, o
      portão do `Run` mantém-se (é ele a garantia) e a de cima é a que evita o selo.

### Como ficou (2026-10-26)

A conferência corre em `hostRun`, entre a travessia da aresta (AOS-553) e o selo (AOS-551), e é a
**mesma função** que decide nos dois sítios: `contratoPossivel` e `origemPossivel` passaram a ter um
chamador único (`recusaDeArranque`), que o `Runtime.Run` usa e que `agentruntime.RefuseAtStartup`
expõe a quem hospeda o run. O que difere entre as duas chamadas é só a **oferta**.

**A medição que mudou o desenho.** O portão do kernel decide a pertença contra `Goal.Tools`, que na
hospedagem está **vazio** — o nó nunca o escreve, e quem o preenche é o `ApplyFrozenToGoal` *dentro*
do `Run`. Erguer o portão com o Goal tal como está recusaria **todos** os contratos. A oferta vem
por isso do catálogo (`SecuredRuntime.OfferedToolNames`), que é a mesma fonte do congelamento: o
`FreezeToolSet` projecta uma spec por entrada activa e as suas duas recusas abortam o congelamento
inteiro, nunca produzindo um conjunto menor — logo os dois conjuntos de nomes são iguais. E vai
**cortada pela lista-branca do run** (`ofertaDoNo`, a regra do `specsOferecidas`), porque é cortada
que o `Run` a vê: sem o corte o veredicto era o mesmo e a **causa** mudava — «fora da lista-branca»
em cima, «não está no tool set» em baixo —, e é a causa que o `GET /runs/{id}` serve.

**O âmbito são duas condições, e a segunda veio da revisão adversarial.** A conferência corre quando
o selo vai cunhar (`haEntradasPorSelar`) **e** quando esta é a primeira hospedagem do run
(`!rs.existente`). Num run re-hospedado o tool set vem do snapshot durável, que ganha de propósito
ao catálogo vivo (AOS-050/AOS-155), pelo que conferi-lo contra o catálogo recusaria um run que o
`Run` admite. **Derivar isso da forma do Goal era insuficiente**, e foi o achado: duas das três
re-hospedagens chegam com as entradas já seladas (a retoma pela API e a varredura de crash-resume
repõem os `Inputs` do registo), mas a terceira não — um `POST /runs` de um `run_id` que já existe e
está em curso é admitido como retoma por outro nome (AOS-520) e o Goal dele é o **do corpo**, com
entradas sem referência. Está provado por teste nas três direcções: sem a conferência o run
condenado deixa um `handle.created`; sem a bandeira, a re-hospedagem é recusada; sem o corte da
lista-branca, as causas divergem.

**O que isso deixa de fora, declarado:** um run re-hospedado por essa via com um contrato impossível
continua a selar antes de o `Run` o recusar. Fechá-lo exigia conferir contra o snapshot durável, que
só se lê repondo-o no registo de tool sets por-run — estado partilhado que o `Run` repõe e larga a
seguir.

**O que não subiu, com a razão de cada**, está no bloco do selo em `hostRun`, e são **cinco** (o
enunciado deste ticket contou seis portões onde há sete): a validação de pré-condições
(`rt.validate`, que o enunciado não contou) é só do Goal mas é inalcançável depois do selo; a
semente do tail é a única que *não pode* subir, porque exige as referências que o selo produz; o
layout não deixa resíduo (uma versão que ele recuse não é um layout por handle, e o selo sai sem
selar); o portão da autoridade por argumento é inalcançável nesta composição; e a janela é função da
composição do `Runtime`, que o nó não vê.

**A janela do crash continua aberta e declarada** — é o AOS-554.

### Fora de âmbito

- A política de fluxo de dados e o interruptor da 1.8.0 (AOS-556).
- A travessia da aresta do plano (AOS-553).

---

## AOS-561 — A VIA DE ENTRADA: um ficheiro no pedido de um run, conferido pelos bytes e selado no espaço de handles

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Harness por capacidades (ADR-043), a linha que falta ao roteiro |
| Tipo | feat |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-550 (o espaço de handles, em produção desde a v0.1.69). **NÃO depende da fase R2**: não mexe em política, não liga o layout 1.8.0 e não precisa da cerimónia da chave |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-via-de-entrada-2026-10-25.md` (o desenho, pedido pelo dono; este ticket **corrige-lhe duas medições** — ver o Contexto); `docs/adr/ADR-043-harness-por-capacidades.md` (§2.6, o espaço de handles); `docs/adr/ADR-042-uma-tool-cujo-efeito-e-uma-chamada-a-um-modelo.md` (§2.4, a pasta semeada que esta via não substitui) |

### Contexto

**Nenhuma rota do nó aceitava um ficheiro.** A única via de media eram as tools de modelo
(`ocr_read`, `media_view`, `video_read`) a ler de uma montagem só de leitura de uma pasta que o
**operador** semeia (`AOS_OCR_SEED_DIR`). Tudo o que está provado em produção — a série vid1–vid5, a
prova do OCR — depende de alguém com acesso ao servidor pôr lá o ficheiro.

O roteiro R0–R6 do ADR-043 tem sete fases e **nenhuma mete um ficheiro no sistema**: todas assumem a
pasta semeada. Três dos quatro exemplos do dono batem nisto. Este ticket é a linha que falta.

**DUAS MEDIÇÕES DO DESENHO QUE ESTE TICKET CORRIGE**, e são a razão de ele não ser o que a §6 pedia:

1. **O tecto que manda é o do HANDLE, não o do gateway.** O desenho manda alinhar os tectos de media
   com `port.MaxImageBytes` (4 MiB) e «nunca maiores». Mas `handleMaxBytes` é
   `agentruntime.MaxToolResultBytes` = **256 KiB**, imposto também pelo KERNEL em
   `Quarantine.Seal` — e é no espaço de handles que a entrada é selada. Uma imagem de 4 MiB **não
   cabe num handle**. O tecto efectivo é o mínimo do caminho, e são 256 KiB.
2. **E isso inverte a inversão da §3.4.** O desenho conclui «sem subir o tecto do corpo nenhuma
   fotografia entra». No tecto que realmente manda isso é falso: 256 KiB em base64 são ~341 KiB, que
   **cabem** no corpo de 1 MiB. Uma entrada entra hoje, sem mexer em nada. O que não entra é uma
   fotografia de telemóvel de 4 MiB — e para essa são precisas **duas** mudanças de postura, não uma:
   o tecto do corpo (AOS-562) **e** o tecto por handle.

**E A PEÇA A NÃO É ENTREGÁVEL SOZINHA**, o que funde as peças A e B da §6 num só ticket. Medido: um
`plan_input` é, em **todos** os layouts até à 1.7.0 — que é aquele em que a produção corre, porque a
media no turno está ligada —, um payload que entra no **contexto do modelo** (`tailFromPlanInput` põe
o `Content` no corpo do segmento). Dar `media_type` a um `plan_input` e nada mais punha bytes de
imagem no prompt; e o selo que a §6 nomeia para o impedir (`selarEntradasDoPlano`) só corre na
**1.8.0**, que é a R2 e está desligada. Não salvava a produção. É o erro que custou quatro revisões
ao AOS-551 — um leitor ou um validador de uma forma que nada escreve — pelo lado pior: uma porta que
admite bytes para um destino que não os sabe usar.

**Por isso a entrada de media é uma lista PRÓPRIA no mesmo pedido** (`media_inputs`), e o seu selo
corre em **todos** os layouts: o que mantém bytes de imagem fora do contexto do modelo não é o
layout, é o facto de serem media. Fica assim um produtor para esta lista, e não dois para o mesmo
campo.

### Critérios de Aceitação

- [x] O `POST /runs` aceita `media_inputs` — `media_type`, `digest`, `content` em base64 padrão —
      **desligado por omissão** (`AOS_ENTRADA_DE_MEDIA`). Num nó que não habilite a via, um pedido
      que a traga é **RECUSADO com 400**, nunca aceito e ignorado.
- [x] `AOS_ENTRADA_DE_MEDIA=on` sem `AOS_HANDLE_DIR` **recusa o arranque**: uma via ligada que
      nenhuma submissão pode usar é pior do que uma via desligada.
- [x] O vocabulário é **FECHADO** e é o do Model Gateway, pelas **mesmas constantes** (`image/png`,
      `image/jpeg`) — não uma segunda cópia das strings, que divergiria.
- [x] **O tipo é conferido PELOS BYTES**, pela mesma função que o gateway usa
      (`port.ImagePart.Validate`). Um ficheiro que declare `image/png` e não o seja é recusado com
      400, **antes de o run existir**. Não se corrige em silêncio e não se adivinha.
- [x] O `digest` é `sha256:<hex>` dos **bytes descodificados** e é conferido na porta. É o mesmo
      pré-imagem de que o espaço de handles deriva o nome do ficheiro, pelo que o digest da entrada e
      o do seu handle são o **mesmo número**.
- [x] Tectos **próprios, separados dos de texto**, contados antes de o run existir: **256 KiB por
      entrada** (o menor dos dois tectos a jusante — o do espaço de handles e o do gateway), **1 MiB
      no conjunto** e **16 entradas** (posições de handle, com folga de 4× contra as 64 do run). Um
      teste amarra o invariante: subir um dos dois tectos a jusante sem mover o da entrada fá-lo
      falar.
- [x] **O tecto AGREGADO existe e MORDE, e é um número e não uma derivação** — três tentativas, cada
      uma corrigida pela medição da seguinte, e as três ficam registadas no código porque o erro do
      meio foi uma CORRECÇÃO a abrir um defeito pior:
      (1) igual ao produto da contagem pelo tecto da entrada ⇒ **INERTE**, nunca disparava, e foi um
      teste que o apanhou;
      (2) **removido** ⇒ o que limitava o conjunto passava a ser o tecto do CORPO, que é do operador
      e sobe, pelo que o trabalho pré-autenticação ficava preso a um botão dele — e o canal de
      TEXTO prova o contrário, com o agregado 4× abaixo do produto de propósito;
      (3) **derivado** (`4 ×` o tecto da entrada) ⇒ o produto é sempre o do gateway, pelo que o
      agregado ultrapassava-o assim que o tecto por handle subisse, e **a mudança de postura que a
      documentação prescreve ao dono fazia o invariante FALHAR**.
      Fica um número com a sua razão — o tecto do trabalho pré-autenticação —, e um teste assere que
      morde, que uma entrada no tecto cabe, e que a contagem deixa folga.
- [x] Os bytes são **selados no espaço de handles do run antes de o run abrir**, cifrados pelo
      cifrador por titular com o run nos dados associados, declarados num `handle.created` com
      `tool=media_input` — é o que **distingue uma entrada ingerida de um `tool_result`** — e com o
      tipo nos factos do handle.
- [x] **O selo limpa os bytes do Goal.** O registo de retoma é escrito a seguir e serializa o Goal:
      deixá-los lá gravava uma segunda cópia do conteúdo em claro no corpo de um evento, sem a cifra
      por titular. O registo leva a referência, o digest e o tipo, e nenhum byte de conteúdo.
- [x] Uma recusa a meio **não deixa bytes** no espaço de handles (molde do AOS-556), e o
      `handle.created` das anteriores fica (o stream é append-only).
- [x] Os **portões fail-closed do arranque (AOS-557) cobrem este selo**. Sem isto um run só com media
      — o caso da via na 1.7.0 — selava os bytes antes deles, que é o defeito que o AOS-557 fechou
      pelo outro lado.
- [x] Uma entrada que já traz handle **não é re-selada** (re-hospedagem, AOS-559), e o desfazer fica
      desarmado.
- [x] **Nada do que a §3.5 do desenho proíbe nasce:** sem armazém novo, sem rota de leitura do
      espaço de handles, sem `multipart`, sem rota de upload de duas fases, e a entrada **não** vai
      ao armazém de artefactos (que é a SAÍDA, com rota de download).
- [x] A entrada **não entra no contexto do modelo em layout nenhum** — não é um segmento de tail, e o
      `prompt_hash` não depende dela. O modelo sabe dela pelo **objectivo**, que a nomeia pelo nome
      que o **nó** cunha (`media_input-1..n`, previsível pela ordem de submissão), nunca por um nome
      escolhido por quem submete.
- [x] O banner de arranque declara a postura: vocabulário, tectos, **qual tecto manda em cada eixo**
      com o número, que o conteúdo **não é redigido**, e que **nenhuma tool lê deste espaço** até ao
      AOS-563.
- [x] As duas variáveis novas estão documentadas em `deploy/node/README.md` com efeito, default e
      impacto de segurança (gate AOS-203).

### O que fica declarado como resíduo

- **NENHUMA TOOL CONSOME A ENTRADA.** A `ocr_read` e a `media_view` continuam a resolver o `doc_id`
  só contra a pasta semeada. Até ao **AOS-563** esta via **sela e declara**, e nenhum run a lê. É
  deliberado e está no banner: o produtor vem primeiro, para que o leitor do AOS-563 possa ser
  desmentido por bytes que existem.
- **O conteúdo ingerido NÃO É REDIGIDO**, e é **decisão do dono**, não defeito deste ticket. A
  fronteira de ingestão trata só o objectivo (`IngestObjective`) e bytes de imagem não passam por um
  redactor de texto. Já era verdade para o que o operador semeia; o que muda é **quem o pode causar**,
  que passa a ser quem submete. Redacção de imagem não existe, e desenhá-la não é trabalho desta via.
- **A `taint.Origin` não distingue uma entrada ingerida.** Por uma razão medida e já registada: a
  reconstrução da quarentena de um run adopta todos os handles com `OriginToolResult`, pelo que uma
  origem diferente na selagem tornava o handle vivo e o reconstruído dois objectos diferentes. Quem
  distingue é o `Tool` do `handle.created`, e as duas origens classificam untrusted de qualquer modo.
- **A VIA NÃO SE ANUNCIA no `GET /tools`**, ao contrário dos campos recusáveis cujo molde a recusa
  de 400 invoca (`completion_contract`, `output_source`, `run_retry`, `plan_input_deferral`,
  `handle_traversal`). Achado da segunda passagem adversarial, e a metade que falta ao molde é
  real: um submissor automatizado só descobre se o nó aceita media submetendo e colhendo um 400.
  **Não se fecha aqui** pela razão que ordena este ticket e o AOS-563: anunciar uma capacidade
  que nenhuma tool consome ainda é anunciar o que não se pode usar. **Gatilho:** o AOS-563, que a
  torna utilizável — e aí o anúncio entra com ele, pela MESMA condição que a porta confere, para
  a porta e o anúncio não poderem divergir.
- **A pasta semeada não sai.** Sai quando esta via tiver prova em produção, não antes: é a origem de
  tudo o que hoje funciona.
- **Vídeo e PDF ficam fora.** O vídeo tem outros números e é ticket próprio; o PDF é **decisão do
  dono** (§5 do desenho) e espera a R5 ou uma decisão de catálogo.
- **A janela do crash entre o selo e o registo de retoma** continua aberta, como no AOS-551/557: o
  ticket é o reaper por run, **AOS-554**.

### Como ficou (2026-10-26)

`packages/cmd/aos/aos561_entrada_de_media.go`: a porta (`validarMediaInputs`), o interruptor
(`parseEntradaDeMediaFromEnv`), o selo (`selarMediaDoPedido`) e o banner. O kernel ganha
`agentruntime.MediaInput` e `Goal.MediaInputs`, que **o runtime não lê** — é essa a propriedade que
elas compram. O selo corre em `hostRun`, **antes** de `selarEntradasDoPlano`, e a ordem é um
contrato: o nome cunhado é `<tool>-<ordinal>` com o ordinal global do run, pelo que selar a media
primeiro dá-lhe nomes previsíveis pela ordem de submissão.

**O que um teste encontrou e o código mudou:** o tecto agregado era inerte (ver os critérios), e os
portões do AOS-557 não cobriam este selo. Os dois estavam escritos e verdes antes de a asserção
existir.

## AOS-563 — A `ocr_read` e a `media_view` resolvem o `doc_id` no espaço de handles do run

| Campo | Valor |
|---|---|
| Epic | EPIC-02 — Agent Runtime e Execução Durável |
| Fase | Harness por capacidades (ADR-043), a linha que falta ao roteiro |
| Tipo | feat |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | **AOS-561** (o produtor: sem ele este ticket é um leitor de um espaço que nada enche — o erro que custou quatro revisões ao AOS-551) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/desenho-via-de-entrada-2026-10-25.md` (§3.3, a peça C); `docs/adr/ADR-042-uma-tool-cujo-efeito-e-uma-chamada-a-um-modelo.md` (§2.4, as regras de leitura da pasta semeada); `packages/cmd/aos/ocr_tool.go` (`lerConfinado`, que é o que não muda) |

### Contexto

O AOS-561 põe a entrada de media no espaço de handles do run e declara-a num `handle.created` com
`tool=media_input` e o tipo nos factos. **Nenhuma tool a lê.** A `ocr_read` e a `media_view` resolvem
o `doc_id` contra `AOS_OCR_SEED_DIR` e mais nada, pelo que a via de entrada sela conteúdo que nenhum
run consome — e é o que o banner do AOS-561 declara.

Este ticket fecha a volta: as duas tools passam a resolver o `doc_id` **primeiro** no espaço de
handles do run, depois na pasta semeada.

**PORQUE AS DUAS E NÃO A SUBSTITUIÇÃO.** A pasta semeada é o que o dono controla e é a origem de tudo
o que está provado em produção (as séries vid1–vid5, a prova do OCR). Trocá-la por esta via numa só
alteração junta uma capacidade nova com uma regressão possível no que já funciona.

**E NUNCA AS DUAS PARA O MESMO `doc_id`.** Um `doc_id` que resolva nos dois sítios é ambíguo, e a
ambiguidade decide-se fail-closed: o espaço do run ganha, e o caso é contado. Sem isto, um ficheiro
semeado com o nome que o nó cunha (`media_input-1`) mudava o que uma tool lê sem ninguém o pedir.

### Critérios de Aceitação

- [ ] A `ocr_read` e a `media_view` resolvem o `doc_id` **primeiro** contra os handles de entrada de
      media do run (os `handle.created` com `tool=media_input`), pelo **nome cunhado pelo nó**
      (`HandleFacts.Name`), e só depois contra a pasta semeada.
- [ ] **Nunca as duas para o mesmo `doc_id`:** uma colisão resolve pelo espaço do run e é contada no
      `/metrics` com causa própria. Um teste prova que um ficheiro semeado com o nome cunhado não
      muda o que a tool lê.
- [ ] Os bytes vêm do espaço de handles pelo caminho que já existe — decifrados com o run nos dados
      associados e **conferidos contra o digest** que um `handle.created` do próprio run declara. Sem
      o evento não há bytes (o guarda (b) do AOS-550 mantém-se).
- [ ] O tipo com que a parte vai ao gateway é o **conferido na porta** (dos factos do handle), não
      uma segunda inferência dos bytes: duas derivações divergiriam.
- [ ] A capability, o destino declarado e a classe de destino **não mudam** — `cap:fs.read`,
      `external_read`. O que muda é **de onde** a tool lê os bytes, e isso é config trusted do
      operador, não autorização. **Nenhuma cerimónia de chave.**
- [ ] A leitura da pasta semeada fica **byte a byte igual**: as regras do ADR-042 §2.4 (`os.Root`, só
      o nível de topo, só ficheiros regulares, uma só ligação física, o Stat do descritor, o tecto na
      leitura) não são tocadas. Um teste prova que um run sem entradas de media lê o que lia.
- [ ] Um `doc_id` que não resolva em nenhum dos dois falha com a causa que já existe, e a causa não
      revela qual dos dois espaços foi consultado nem um byte de conteúdo.
- [ ] O banner de arranque deixa de dizer que nenhuma tool lê deste espaço, e passa a declarar a
      ordem de resolução.

### O que fica declarado como resíduo

- A `video_read` fica de fora: lê da mesma pasta, mas o tecto de um vídeo não é o de uma imagem (e
  256 KiB não é um vídeo). A mesma máquina serve, com outros números, e é ticket próprio.
- A prova em produção (a peça D do desenho — um `POST /runs` com uma fotografia e o OCR a correr sobre
  ela) é **do dono**, e precisa também da mudança de postura do tecto do corpo (AOS-562) se a
  fotografia passar de 256 KiB.

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
| 1.1 | 2026-09-15 | AOS-396: manifesto do turno com model_id vazio no nó (achado do E2E em produção) | Equipa AOS |
| 1.2 | 2026-09-19 | +AOS-411: a re-varredura de órfãos tratava um run vivo como órfão e decifrava-lhe as capturas antes de verificar o dono (observado em produção na v0.1.22) | Equipa AOS |
| 1.3 | 2026-09-20 | +AOS-419: `paused` e `waiting_on_tool` não tinham backstop de wall-clock nem aresta de saída para terminal (eixo novo do DEF-906) | Equipa AOS |
| 1.4 | 2026-09-21 | +AOS-422 (os runs vivos saltados vão ao `/metrics`): medido ao tentar verificar o AOS-411 em produção que a correcção tornou a sua própria evidência inobservável — a passagem periódica só fala com órfãos verdadeiros, e os contadores eram variáveis locais. | Equipa AOS |
| 1.5 | 2026-09-26 | +AOS-454 (a via durável perde o `parent_step_id` do evento de mediação): achado no diagnóstico da fase 1 do AOS-069; auditoria campo a campo `Call → Activity → toCall` fixada por teste de reflexão. | Equipa AOS |
| 1.6 | 2026-10-02 | +AOS-485: a recusa de uma tool call pela lista-branca do run não deixa evento nem selo (achado do E2E de plano multi-nó em produção) | Equipa AOS |
| 1.7 | 2026-10-03 | +AOS-489: o tail do prompt regista a tool call do modelo e identifica o resultado; 60% das tool calls em produção eram repetições | Equipa AOS |
| 1.8 | 2026-10-04 | AOS-489: verificação em produção da v0.1.45 (um run, zero repetições; critério dos 10 runs por cumprir) | Equipa AOS |
| 1.9 | 2026-10-04 | +AOS-492 (regra de terminação única) e +AOS-493 (o desfecho de um run é um veredicto do kernel): em 2 de 10 planos em produção o run fechou concluído sem executar a tool de que a saída dependia | Equipa AOS |
| 1.10 | 2026-10-04 | AOS-493 implementado (ADR-037): veredicto do kernel, contrato de conclusão no objectivo do run, modo de aplicação por nó (observação por omissão). Por rever e por verificar em produção | Equipa AOS |
| 1.11 | 2026-10-05 | AOS-493 revisto: contrato impossível recusa o arranque, medição dos runs que acabam sobre uma recusa ou falha de tool, critério do replay restrito aos runs de uma só vida, saga de compensação decidida e com limite declarado | Equipa AOS |
| 1.12 | 2026-10-05 | +AOS-497 (o kernel designa e sela a origem da saída de um run): em 2 de 21 planos em produção o nó de leitura chamou a tool com sucesso e entregou um resumo em vez do conteúdo; primeira peça da saída por referência | Equipa AOS |
| 1.13 | 2026-10-05 | AOS-497 implementado e revisto: a regra de designação conta as chamadas pedidas, o run com entradas tem o estado `inapplicable`, a forma do nome valida-se no arranque, e a fonte dos bytes é o step-ledger. Por verificar em produção | Equipa AOS |
| 1.14 | 2026-10-18 | +AOS-533 (o replay verificável de produção, `GET /runs/{id}/replay`): resíduo da sub-fase A5.1 — o replay de produção não recalculava os `prompt_hash` | Equipa AOS |
| 1.15 | 2026-10-23 | +AOS-551 (a separação de planos ligada: layout 1.8.0 e a autoridade por argumento), fase R2 do ADR-043 — **fecha o DEF-806**. A autoridade de uma call passa a ser `join(trusted, taint de cada handle resolvido nos seus argumentos)`: a separação sozinha trocaria «negar a mais» por «permitir a mais». Nasce com um ADR próprio | Equipa AOS |
| 1.16 | 2026-10-23 | AOS-551 reescrito para a v2 do desenho da R2: a premissa «o modelo que decide deixa de ver conteúdo untrusted» era **falsa** (a R1a projecta a imagem para ele, `projection_media.go:91-111`), logo a autoridade passa a contar também os segmentos de media e um run com media não ganha nada; a decisão passa a uma **política de fluxo de dados** em Cedar assinado, com cerimónia do dono, sem a qual a R2 não compra «gerar a partir do que se viu»; a 1.8.0 deriva da 1.7.0 e herda o adiamento, logo **não há ganho em turnos**; autoridade por call no replay e âncora nova da designação de origem | Equipa AOS |
| 1.17 | 2026-10-24 | AOS-551 **partido em dois**: o mecanismo fica no AOS-551 (layout 1.8.0 derivado da 1.7.0, `plan_input` e `tool_result` por handle, `SeparatePlanes` com chamador, autoridade por argumento que conta a media, autoridade por call no replay, âncora da designação de origem, paridade das duas vias), **desligado e com o interruptor fail-closed**; a política de fluxo de dados em Cedar, o interruptor accionável, o ADR próprio e o fecho do DEF-806 passam a +AOS-556, que exige cerimónia da chave do dono. Razão: um ticket com uma dependência de cerimónia a meio não pode ser fechado por quem o implementa, e a caixa por marcar passa a significar duas coisas diferentes. Medições que acompanham a partição: a costura da autoridade por argumento já existe (a Call é construída com o `Context.Taint` ANTES de o `CallRewriter` correr, e o valor sobrevive até ao RM, pelo que é cablagem e não kernel novo); o `ToolStepID` por call JÁ está no envelope do `tool.call.mediated` (o motor do replay usa o `ParentStepID` e ignora-o), logo a autoridade por call no replay não precisa de schema novo; e a exigência do colaborador NÃO pode viver no `layoutFor`, que é função pura — pertence à composição do nó | Equipa AOS |
| 1.18 | 2026-10-24 | AOS-551, **primeira fatia do mecanismo** (ramo `feature/AOS-551-tail-por-handle`): layout **1.8.0** derivado da 1.7.0, com o `plan_input` a entrar no tail **por handle** — corpo fixo e a referência do handle no rótulo —, nunca como payload. **A separação antes da autoridade, e a ordem não é de conveniência:** a separação sozinha é SEGURA (nega a mais), a autoridade por argumento sozinha seria INSEGURA (com o conteúdo ainda inline, o modelo copia o que leu para um argumento literal e o `join` autoriza). **Fronteira da fatia, medida:** o `plan_input` não exigiu costura nova — o nó sela antes de o run abrir e a referência viaja no `Goal` —, enquanto o `tool_result` por handle exige um sítio onde o nó sele um resultado produzido a meio do run, que **não existe**; é a fatia seguinte. Fail-closed no arranque do tail (`SeedTail` e `abrirTailDoRun`), e **sem interruptor por desenho**: a série `aos_runs_hosted_total` da 1.8.0 não sai a zero, porque sairia a dizer que o nó a oferece. **Revisão adversarial derrubou a v1 com quatro defeitos medidos**, dois nos BYTES do layout (onde corrigir depois de existir um run custa uma 1.9.0): o rótulo levava o token NU, que o `ParseHandleRef` não aceita e que o `CallRewriter` também **não recusa** — seguia ao RM como literal, autorizado e selado sobre a string do token, o estado que o banner do AOS-550 diz não existir; o corpo fixo dizia que a substituição é **depois** da autorização, quando é antes, invertendo a propriedade de que o RM decide sobre os BYTES e de que o AOS-556 vai depender; o digest das entradas adiadas passou a depender do `run_id` e fazia o sinal de AOS-502/506 disparar em toda a tentativa; e o gate só via a string vazia — **conferir não é cunhar**, e o comentário que justificava não cunhar no kernel foi usado para não fazer nem uma coisa nem outra, aceitando o estado exacto que dizia temer. Três testes próprios mediam menos do que o nome e foram refeitos. Confirmado são pela mesma revisão: nenhum caminho devolve o payload ao contexto do modelo (semente, entrega adiada, projecção nativa, replay, eventos, rotas do nó), e a 1.8.0 é genuinamente inalcançável — não há campo no wire do submit nem interruptor que a escolha | Equipa AOS |
| 1.19 | 2026-10-24 | AOS-551, **fatia 1b**: o `tool_result` de uma call PERMITIDA entra no tail por handle — corpo fixo e a referência no rótulo —, fechando a SEGUNDA superfície por onde conteúdo de fora entrava no contexto do modelo. **O kernel EXIGE, não fareja:** o corpo de um resultado vem de uma tool (untrusted), pelo que «se parecer uma referência, trata como referência» deixaria uma tool forjar uma; um corpo que não seja referência materializa-se como erro de tool, nunca inline. **Fronteira da fatia, medida:** quem sela é um envelope do `ActivityDispatcher` (chama `Dispatch`, que É o caminho do RM, logo não viola o AOS-003), mas essa cablagem vive em `integration/secured.go`, construída UMA VEZ e não por run — selaria em todos os runs, incluindo os da 1.4.0. Pertence à fatia da composição; esta é só o kernel. **Hipótese morta:** o molde da `media_view` não generaliza — `mediaDoResultado` discrimina por `ToolID`, discriminador fechado que não existe para todas as tools. **A v1 DESTA FATIA CAIU EM REVISÃO ADVERSARIAL (6 achados, 1 bloqueante), e os três que mudam o desenho:** (a) **BLOQUEANTE —** eu fiz a designação da origem da saída recusar deixando o digest vazio, mas `designar` põe `State=designated` sempre que a chamada é efectiva: `designated` + digest vazio é a ÚNICA combinação que `BemFormada` recusa, logo a máquina de estados recusava a transição terminal e o run ficava **SEM DESFECHO no log durável** — indistinguível de um crash, sem o `failed` que dispara a compensação e com a reserva de quota presa. Quebrava o invariante declarado no próprio ficheiro («uma âncora que este run produza é sempre selável»). A recusa passa a ser por **não-efectividade**, que resolve `missing` — bem formado, logo SELA. Isso fecha também o diagnóstico errado `empty_output` que um `designated` com zero bytes produzia sobre uma tool que devolveu conteúdo; (b) **a condição tinha de ser o LAYOUT e não a forma do valor:** condicionada ao valor, deixava de fora o nó que compõe a 1.8.0 SEM o envelope que sela — aí o tail mostra um erro ao modelo e esconde-lhe o conteúdo, mas a designação digeria esse conteúdo e entregava-o a jusante como saída do run. **Fail-OPEN**, e na direcção que a assimetria que eu invocava («recusar só reduz autoridade») não cobria; (c) **a `media_view` é EXCEPÇÃO ao caminho dos handles**, por desenho da fase: a 1.8.0 herda os segmentos de media e `mediaDoTurno` lê os resultados CRUS, pelo que sem selo o tail dizia que a chamada falhou E anexava a imagem, e com selo a imagem deixava silenciosamente de chegar (regressão da R1a). A correcção 1 deste ticket já diz que a media é PROJECTADA para o modelo — é por isso que a autoridade tem de a contar. Fecharam-se ainda: o output CRU a entrar inline pelo ramo do `tool_error` (o `CapturedToolResult` transporta `Result` e `ToolError` independentemente, e o meu comentário afirmava o contrário — o valor passa a nunca chegar ao corpo em ramo nenhum, ESTRUTURALMENTE), e três testes meus que mediam menos do que o nome, entre eles o da designação, que **nunca afirmava o `State`** e era por isso que passava com o bloqueante por baixo. **CONSEQUÊNCIA A REGISTAR antes de alguém ligar o interruptor:** na 1.8.0 nenhum resultado designa, logo um run que VINCULE a saída a uma tool não se cumpre — fecha sempre `output_source_missing`. É fixado em teste, e é mais uma razão por que a 1.8.0 fica inalcançável até a **fatia 4** redefinir a âncora no digest do próprio handle. **Resíduos declarados:** o aviso de repetição do AOS-489 morre na 1.8.0 (o desfecho de uma chamada é o hash do resultado e duas chamadas idênticas dão referências diferentes, porque o ordinal entra no selo — a série estéril nunca avança, e o contador de repetições continua a contar, o que torna a perda silenciosa); o texto de um `tool_error` é `error.Error()` e uma tool MCP pode lá pôr conteúdo (as tools do nó têm vocabulário fechado imposto por tipo, AOS-550); e o tecto `handleMaxPorRun` passa a vinculativo, porque selar todo o resultado consome uma posição por tool call | Equipa AOS |
| 1.20 | 2026-10-24 | AOS-551, **fatia 2: a autoridade por argumento**, e a primeira que NÃO é segura sozinha — só podia entrar depois de as duas superfícies estarem fora do prompt. A autoridade de uma call passa a ser `join(piso, untrusted se a call resolveu algum handle)`: uma call que não consome handle nenhum, num run sem memória nem media, fica **trusted mesmo depois de o run ter lido**, e é esse o limite que a fase R2 levanta. **O PISO É UM SEGUNDO ACUMULADOR**, e não uma emenda ao `SegmentAuthority` — emendá-lo mudaria o rótulo de TODOS os layouts, incluindo os que estão em produção. **EMENDA AO CRITÉRIO:** o portão fail-closed não pertence à composição do nó, como o critério dizia — o `Runtime.Run` não é puro e tem o layout e as portas injectadas na mão, pelo que o portão vive lá (`ErrArgumentAuthorityRequired`) e o banner do nó passa a DECLARAR a postura. E **a declaração não é a garantia**: a Call sai com o piso e o kernel RE-JUNTA o piso depois da reescrita, pelo que ela só consegue SUBIR. **A V1 DESTA FATIA CAIU EM REVISÃO ADVERSARIAL (10 achados, 1 bloqueante, 4 graves), e cinco mudam o desenho:** (a) **BLOQUEANTE —** a propriedade central («resolver um handle suja a call qualquer que seja o argumento») era FALSA. A autoridade lia `len(digestPorArgumento) > 0`, e esse mapa tem um guarda que deixa cair argumentos de **NOME VAZIO**, legítimo na linhagem do AOS-552. Mas o nome dos argumentos é escolhido pelo MODELO e `{"": "<referência>"}` é JSON válido: o handle resolvia-se, os bytes entravam na call, o bilhete ficava vazio e o RM autorizava, auditava e **selava como TRUSTED** uma call com conteúdo em quarentena nos argumentos. A lição é a FORMA: inferir uma propriedade de uma estrutura FILTRADA para outro fim herda os filtros dela. A autoridade passou a ter contador próprio, incrementado onde a resolução acontece, sem filtro; (b) o portão verificava um **booleano sem relação com a reescrita** — um Runtime com a opção posta e SEM `CallRewriter` arrancava, construía a Call com o piso e não tinha quem a levantasse. Passa a exigir as DUAS coisas, e isso apanhou de imediato a bancada dos diferenciais, que declarava sem compor; (c) a **re-junção vivia dentro de um `if`**: uma call a uma tool FORA da lista-branca do run (a reescrita não corre de propósito) chegava ao RM com o piso e sem re-junção — o RM nega-a pelo backstop, logo não havia escalada, mas o `tool.call.denied` ficava selado como TRUSTED uma call pedida de contexto untrusted. Saiu para fora do `if` e corre em todos os caminhos; (d) a tabela do piso era por KIND e declarava trusted um `tool_result` da **`media_view`**, que é excepção ao caminho dos handles e leva a `MediaRef` INLINE — o que tapava o buraco era uma coincidência noutro ficheiro (o segmento de media que a 1.7.0 acrescenta a seguir). Passa a ser dirigida pelo **RÓTULO do handle**, que está presente exactamente quando o corpo é fixo: fecha de uma vez a `media_view`, o `tool_error` e um layout futuro que ligue só uma das metades (`entradasPorHandle`/`resultadosPorHandle` passaram a ser consultadas SEPARADAMENTE — a v1 recebia um só booleano e assumia a conjunção); (e) o replay passaria a **divergir FALSAMENTE** em qualquer run 1.8.0 com `VerifyAuthority`: o motor compara por IGUALDADE a autoridade do contexto re-dobrada com o taint selado, e esses dois são agora deliberadamente diferentes. Num layout por handle a verificação passa a ser do **LIMITE** (`ContextFloor`): um selo abaixo do piso é divergência genuína, um selo acima é a autoridade por argumento a fazer o seu trabalho. Verificar por igualdade exigiria a captura gravar, por call, que handles ela resolveu — e não grava: **o deferimento «autoridade por call no replay» é maior do que o critério diz**, e fica declarado. Fecharam-se ainda: o `timestamp` saiu do caso trusted (o kernel NÃO o escreve — quem o emite é a memória de trabalho, fora do seu controlo), o `steer` fica no `default` fail-closed **com a consequência declarada** (um run esteerado na 1.8.0 não levanta nada), um parâmetro morto em `mediateToolCall`, e **três testes meus que mediam menos do que o nome** — entre eles um que fixava o buraco da `media_view` COMO DESEJADO e uma asserção vácua sobre o banner. 14 testes (7 kernel + 7 nó), com controlo negativo em cada propriedade. **Continua sem ganho de produto:** a política que decide SOBRE este rótulo é o AOS-556 | Equipa AOS |
| 1.21 | 2026-10-25 | AOS-551, **análise de fundo depois de a fatia 3 cair DUAS vezes em revisão adversarial** (14 e 10 achados, um bloqueante em cada). O código das duas tentativas foi **revertido**; o que fica deste dia é a medição, que vale mais. **A causa não era nenhuma das duas tentativas: era o CORTE DO TICKET.** As três fatias entregues implementam LEITORES de uma forma que nada ESCREVE — o produtor (o envelope que sela o resultado de cada call permitida) foi adiado três vezes como «pertence à fatia da composição». Sem produtor não existe caminho ponta-a-ponta para seguir, e por isso cada fatia ficou livre de AFIRMAR o que os bytes seriam: a 1a afirmou que o modelo conseguia usar o handle (não conseguia — o token nu não era a forma resolúvel), a 1b que a recusa falhava fechado (deixava o run sem desfecho terminal), a 2 que resolver um handle sujava a call (um filtro legítimo deixava cair o argumento de nome vazio), a 3 que a âncora atestava o conteúdo (os dois verificadores re-hasheiam o ledger). Quatro afirmações sobre segurança, nenhuma seguida até ao consumidor — e o consumidor era a única coisa que existia, que é também como as revisões as encontraram, para trás. **O ASSENTO DO PRODUTOR, medido, é ÚNICO:** dentro do closure de `ledger.Apply` em `activity/dispatch.go`, entre o `rm.Mediate` que permitiu a call e o `durable.Result{Payload: dec.Output}` que o ledger grava. É o único ponto que é, ao mesmo tempo, (i) DEPOIS da decisão do RM, logo não viola o AOS-003; (ii) ANTES da escrita no ledger, logo o ledger, o loop, o replay e a rota que serve os bytes designados vêem todos os MESMOS bytes e a âncora é confirmável; (iii) com contexto POR RUN — o `ctx` já transporta o titular pelo mesmo mecanismo (`durable.ContextWithTitular`). Um envelope que CHAME `Dispatch`, que é o que a 1b e o comentário da `layout.go` descrevem, corre **depois** da escrita no ledger: o ledger ficaria com o conteúdo e o loop com a referência, e a âncora deixa de conferir. **As duas metades da 1.8.0 estão sem produtor:** `PlanInput.Handle` não é escrito em nenhum ficheiro de produção (só no helper do diferencial de replay) e `conferirEntradas` recusa o arranque, pelo que a 1.8.0 continua genuinamente inalcançável — o risco de tudo isto é hoje zero, e é a postura fail-closed da 1b que o mantém. **O documento de referência NÃO entra neste ticket.** Foi desenhado para tornar a referência abrível a jusante e não a torna: o conteúdo vive em `pastaDoRun(<run produtor>)` cifrado com os dados associados desse run, o `OpenQuarantined` exige um `handle.created` do stream de quem pede, não há rota HTTP nem tool de leitura (fora de âmbito por desenho), e o `Adopt` só dobra os eventos do próprio run em sequência de ordinal. A travessia da aresta do plano é o **AOS-553** (EPIC-19), explicitamente fora de âmbito deste ticket e do AOS-556, com ADR próprio exigido pelo ADR-038 §3 — e DENTRO de um run o documento não compra nada sobre a referência nua, pelo que acrescentá-lo aqui era maquinaria especulativa. **PLANO DEFINITIVO: uma só fatia final** para o mecanismo, com o produtor das duas metades e as decisões do kernel que ele desbloqueia — apagar a recusa da 1b (que passa a ser segura, porque `dec.Output` já é a referência), a designação pelo piso, e testes que passem pelo LOOP e não pela evidência montada à mão (a revisão provou que o teste do piso passava idêntico contra a implementação anterior, e que a tabela do diferencial é insensível às duas mudanças porque a bancada nunca produz um resultado por handle) | Equipa AOS |
| 1.22 | 2026-10-25 | AOS-551, **o PRODUTOR, e com ele o mecanismo fechado**. O selo do `tool_result` vive numa porta nova do `activity.Dispatcher` (`WithResultSealer`) invocada DENTRO do closure de `ledger.Apply`, entre o permit do RM e o `durable.Result` que o ledger grava — o assento único que a linha 1.21 mediu. O corpo que o selo devolve é a REFERÊNCIA NUA, que é o que `tailFromResultByHandle` espera e o que o `CallRewriter` resolve de volta; o documento `HandleRef` NÃO entrou (dentro de um run não compra nada sobre a referência, e o digest é da travessia da aresta, AOS-553). **A versão do layout na fronteira vem do `ctx`, posta pelo `Runtime.Run`** — a MESMA `layout` que dá o prefixo, a sequência de segmentos e o `assembly_version` do manifesto —, e a ausência dela RECUSA (`ErrSealWithoutAssemblyVersion`): as duas alternativas medidas (um valor posto no `hostRun`, um registo por run no `integration`) re-derivavam a versão, e a segunda estaria ERRADA num run novo, cujo `Goal.AssemblyVersion` é vazio. A `media_view` é excepção pela MESMA função que o tail pergunta (`SealsToolResult`): duas cópias da excepção divergiam para um resultado que o tail esconde e que a âncora digere. O produtor do `plan_input` corre no `hostRun`, ANTES do registo de retoma — que leva os `Inputs` e é por isso que a referência sobrevive à retoma sem ser re-selada (re-selar cunhava outro ordinal, logo outro `prompt_hash`). **MUDANÇA DE DESENHO sobre o enunciado do critério:** a recusa da fatia 1b não se apagou; passou a medir a FORMA do valor, e é isso que fecha o fail-open que o enunciado deixava (sem produtor, designar ancorava no conteúdo cru e o `aos-orq` entregava-o a jusante). A designação passa a medir-se pelo PISO e não pela autoridade do contexto, no loop e no replay — fora de um layout por handle são o MESMO valor, pelo que nada muda em produção. CONSEQUÊNCIA: `turnAuthority` deixou de ter leitor no loop (a Call leva o piso desde a fatia 2); o acumulador da autoridade do contexto FICA na janela, pelo mesmo ponto único, porque é o rótulo que o AOS-556 vai interrogar. **A VERIFICAÇÃO QUE FALTAVA ÀS DUAS TENTATIVAS ANTERIORES**, e que é a razão de ambas terem passado os seus testes: um teste que, no MESMO run da 1.8.0 e pelo loop real, compara o payload do step-ledger (lido por `durable.ReadAppliedResult`, a função que a rota da origem usa) com o valor que o loop observou e com o digest da âncora. **SEIS ACHADOS DA REVISÃO ADVERSARIAL, corrigidos nesta fatia:** (i) uma recusa OPERACIONAL do espaço de handles (tecto de 64 handles por run, tecto de bytes, volume cheio) matava o run inteiro, porque o erro do selo não levava um `activity.ToolError` e o adaptador da via durável só converte esse — a 65.ª tool call de um run da 1.8.0 abortava com 64 passos aplicados; passa a materializar-se como `tool_error=` no tail e o run continua, enquanto um defeito de COMPOSIÇÃO (corpo que não é referência, contexto sem a versão) continua fatal; (ii) o payload selado não vinha marcado `durable.Result.Reference`, o vocabulário do próprio ledger, pelo que um nó em modo sensível SEM cifrador por-titular recusava toda a tool call de um run da 1.8.0 por não saber que o payload já era uma referência; (iii) o `handlesBanner` continuava a dizer que «o único produtor de handles é a `artifact_write`» e que «sem `AOS_ARTIFACT_DIR` nenhum handle é cunhado», duas frases que esta fatia tornou falsas e que o operador lia imediatamente antes do banner novo a dizer o contrário; (iv) um comentário novo afirmava que a `media_view` não é origem designável «porque o nó não a oferece como tool do run» — falso: com a media no turno ligada ela é registada no RM e exigida no registo de tools (o que torna o estado benigno é outra coisa, que a `MediaRef` crua não é uma referência de handle); (v) a frase «perguntar pela forma fecha os dois lados» era mais forte do que o código compra, e o limite ficou MEDIDO no sítio (num nó que componha a 1.8.0 sem produtor, uma tool cujo output cru seja exactamente a forma de um token designa-se e entrega 8 bytes de hexadecimal à sua escolha — exige um defeito de composição que o banner declara, e a alternativa de recusar por layout tem uma aresta maior, que não exige defeito nenhum); (vi) `gofmt` sujo no `bootstrap.go`. **SEGUNDA PASSAGEM da revisão, sobre o ramo novo, mais dois corrigidos e um declarado:** (vii) a recusa do selo materializa-se como `tool_error` no CORPO do segmento `tool_result`, que é o canal EXACTO que o `erro_de_tool.go` existe para fechar, e o selo não passava por nenhum dos seus dois pontos de imposição — saía `errors.New(causa)` em vez de `erroDaTool(tool, causa)`, pelo que a forma `<tool>: <causa>` não se cumpria, a substituição por `causa_nao_declarada` não corria, e — porque o vocabulário é POR TOOL e as causas do espaço de handles estavam declaradas só para a `artifact_write` — um `ocr_read` que batesse no tecto do run surgia com uma causa que não declara; o selo passa a construir a falha pelo TIPO e as causas do espaço entram no vocabulário de TODAS as tools (`causasDoSeloDeHandle`), com o que o `conferirVocabularioDeErro` do arranque volta a ser a rede; (viii) numa recusa do selo o efeito CORREU e foi descartado, e a acção inversa ficava sem registo porque o `Register` vive depois do return do erro — com a recusa a deixar o run CONTINUAR, o run podia fechar cumprido com um efeito irreversível aplicado que nenhuma saga sabia reverter, e era o ÚNICO eixo em que a postura nova era pior do que a fatal; a compensação passa a ser registada aí — e a condição mede a PROPRIEDADE («o efeito correu e o resultado foi descartado», `seloDescartouEfeito`) e não qual dos erros é: vale para os TRÊS do selo, fatais incluídos, porque nos fatais o run fecha `failed` e a saga corria sobre um registry sem este passo; não vale num deny (nenhum efeito ocorreu) nem num `ErrToolExecution` (o efeito falhou). A primeira versão desta correcção enumerava um só erro e a frase ao lado dizia «é o único» — era falso, e foi a terceira passagem da revisão que o mediu. **E um resíduo novo, declarado no sítio da decisão:** continuar custa o PISO do run — um `tool_error` é um `tool_result` sem o rótulo do handle, logo o piso cai para untrusted e, por o acumulador ser monótono, nunca volta a subir, pelo que toda a call seguinte chega ao Reference Monitor untrusted; é a semântica que um `tool_error` já tem num layout por handle, e o que o selo acrescenta é que a CAPACIDADE do espaço passa a poder decidi-la. Com 64 posições por run e um selo por call permitida, `handleMaxPorRun` passa a ser um tecto de TOOL CALLS por run na 1.8.0, e não está dimensionado contra o `MaxTurns` — a pergunta é do AOS-556. **Resíduos declarados:** o observador do Goal hospedado (`SubmitObservando`, AOS-506) corre ANTES do selo das entradas, porque o layout só é fixado dentro do `hostRun` — num run da 1.8.0 com entradas ele vê os `Inputs` sem `Handle`, e o consumidor, seguido até ao fim, é a medição do prompt de uma nova tentativa (`promptDaTentativaDifere` → `hashDaSementeCom` → `SeedTail`, que recusa entradas sem handle): ela lê «não consegui recalcular» como DIFERE e nunca como igual, pelo que o efeito é um `prompt_diferente` espúrio num run da 1.8.0 com entradas E aviso, nunca uma diferença escondida — fecha-o o AOS-556, que é quem torna a 1.8.0 accionável; **a aresta do plano passa a entregar um token em vez de falhar com causa** — com produtor, um run da 1.8.0 que VINCULE a saída designa e os três passos de `aos-orq/entrega_por_referencia.go` conferem (os dois lados hasheiam a referência), pelo que o nó consumidor recebe `aos-handle/v1:…` como conteúdo do seu `plan_input` e nunca o pode abrir (o `OpenQuarantined` exige um `handle.created` do stream de quem pede); antes era `output_source_missing`, uma causa explícita, e trocar «falha com causa» por «entrega de um token inútil» é do AOS-553 e tem de ser resolvido antes de o AOS-556 ligar o interruptor; um `handle.created` por entrada é escrito ANTES do registo de retoma, pelo que uma recusa a meio ou um crash entre os selos e o registo deixa declarações de um run que nunca vai correr, com os bytes no espaço até alguém limpar a pasta; um resultado selado sem media type declarado sai com `well_formed=false` (é o que `formaBemFormada` responde a um tipo que não conhece) e o facto só é lido por quem atravessa a aresta do plano; a `artifact_write` numa 1.8.0 SELA DUAS VEZES — consome duas posições do tecto de handles do run e, medido, o modelo passa a receber a referência de um documento que CONTÉM a referência do artefacto, cuja referência interna NUNCA se resolve (a recusa de aninhamento olha para os argumentos do modelo, não para os bytes resolvidos), pelo que resolver a de fora entrega o documento e não o artefacto: inerte hoje, e é do AOS-556 resolvê-lo antes de ligar o interruptor, por contrato de tool e não por um discriminador por `ToolID` neste produtor (a linha 1.19 mediu que esse não generaliza); a proveniência de um handle de `plan_input` diz `tool_result`, porque é o que a reconstrução da quarentena diz de todos os handles de um run, e as duas origens classificam untrusted; e um selo que falhe RECUSA sem memorizar, pelo que uma nova tentativa re-executa o efeito — o mesmo limite que o `ErrToolExecution` já tem | Equipa AOS |
| 1.23 | 2026-10-25 | **AOS-556, os quatro resíduos que bloqueavam o interruptor da 1.8.0** (critério 5), antes de qualquer política. Nenhum era defeito vivo — a 1.8.0 continua inalcançável —, e todos passavam a ser no instante em que o interruptor fechasse. **(a) A aresta do plano** ganha a causa nomeada `origem_por_handle` em `entregaDoRun`: num layout por handle o kernel sela o resultado e designa a PRÓPRIA referência, pelo que os dois lados a hasheiam, `origem_nao_confere` não dispara e `extrairEntrega` caía no `default` e publicava o token como payload do contrato — trocar «falha com causa» por lixo silencioso. Confere-se pela **FORMA COMPLETA** e não pelo prefixo, e a primeira versão tinha-o ao contrário: a revisão adversarial mostrou que «o prefixo não é autoridade» — o kernel di-lo no sítio da constante, «qualquer um pode escrever estes bytes, um documento lido inclusive» —, pelo que um teste de prefixo fechava nós do plano em layouts de PRODUÇÃO sobre conteúdo legítimo; e que no caso em que a causa tem de disparar o prefixo não acrescenta cobertura nenhuma, porque num layout por handle a designação só é efectiva sobre uma referência bem formada. Era uma regressão VIVA introduzida pela correcção de um defeito não-vivo, e tem controlo negativo próprio. A travessia continua a ser o AOS-553. **(b) A `artifact_write` deixa de selar duas vezes**, e fechou-se do lado da TOOL: ela pergunta pela MESMA função que o produtor e o tail usam (`SealsToolResult`, sobre a versão que o `Runtime.Run` põe no contexto — quatro saltos verificados até `t.fn(ctx, …)`) e não sela quando o produtor vai selar; o produtor continua a selar tudo o que lhe chega, sem conhecer tool nenhuma, e o discriminador por `ToolID` que a linha 1.19 mediu como não-generalizável não entrou. **A correcção ÓBVIA era a errada, e foi medida:** isentar a tool do selo do produtor — o molde da `media_view` — tira ao segmento o rótulo `tool_result_handle`, e sem ele `SegmentFloor` devolve untrusted (`context_authority.go` di-lo em voz alta), pelo que TODO o run que escrevesse um artefacto caía para untrusted para sempre: o oposto do que a fase existe para comprar, e precisamente no caso-bandeira («lê a planta, grava a lista de cómodos»). Consequência declarada: num layout por handle o conteúdo do artefacto não ganha handle próprio e não é resolúvel como argumento — não se perde nada que funcionasse, porque a referência de dentro nunca era resolúvel. **(c) O observador do Goal** fecha-se ONDE A MEDIÇÃO SE LÊ, e não movendo o selo: a contagem continua a conservadora (o que não se consegue comparar conta como diferente) e o log passa a dizer que não se conseguiu comparar, em vez de acusar a tentativa de ter mudado o pedido (`sementeSemReferencias`), e só quando o digest das entradas CONFERE — se ele divergir a divergência é real e dizê-lo esconderia-a. Mover o selo para antes do observador foi medido e RECUSADO: punha-o antes da fixação do modelo, e uma recusa dela passava a deixar handles selados de um run que nunca arranca — o resíduo (d) num caminho muito mais comum. **(d) As declarações órfãs:** uma entrada que recuse com as anteriores já seladas DESFAZ os bytes delas, que é o dano do achado M-4 pelo outro lado (ninguém os resolve, nada os apaga, e o tecto do VOLUME cheio recusa a selagem em TODOS os runs — até ao tecto AGREGADO do canal de entrada, `maxPlanInputsBytes` = 512 KiB, por hospedagem falhada — e não 16 × o tecto de um handle, que a primeira versão afirmou em três sítios sem seguir os valores até quem os fornece, repetível por quem submete). O `handle.created` de cada uma FICA: o stream é append-only e não há evento de compensação; uma declaração de um run que nunca corre é inofensiva, porque o que ela declarava deixou de existir. O desfazer fica de fora se alguma entrada vier já selada — mistura que o wire do submit não permite hoje —, para não tirar o chão a uma entrada do registo de retoma pelo reaproveitamento de ficheiro do mesmo conteúdo. Continuam declaradas DUAS janelas: o **crash** entre os selos e o registo de retoma, que nenhum `defer` cobre; e — achado da revisão adversarial, porque a primeira versão afirmava que só o crash ficava — a janela entre o selo e o primeiro efeito, em que os portões fail-closed do `Runtime.Run` recusam sem efeito nenhum e nada desfaz, dois deles accionáveis por quem submete (`contratoPossivel` e `origemPossivel`: a porta aceita um contrato de conclusão que o nó não pode cumprir, e di-lo). **Não se desfaz aí, e a razão é dirimente:** o registo de retoma já foi gravado, pelo que apagar deixava um run RETOMÁVEL cujas entradas não têm bytes — perda silenciosa de capacidade em vez de bytes a mais. Fechá-lo é correr esses portões antes do selo, e é o **AOS-557**. **Prova:** quatro mutações dirigidas, cada uma vermelha pela razão certa — e a de (a) devolveu `causa=""`, isto é, a entrega do token. Cinco superfícies que afirmavam o estado antigo foram corrigidas: o banner do espaço de handles, o do armazém de artefactos, o tecto efectivo por artefacto, o banner que ENUMERA os produtores de handles (achado da revisão: listava a `artifact_write` sem condição) e a ajuda da métrica `aos_runs_retry_prompt_hash_diferente_total`, que dizia «TEM DE SER ZERO» e passava a disparar deterministicamente num run da 1.8.0 com entradas e aviso — a série é a superfície que o operador vigia, e corrigir só o log deixava o alerta a desmentir o log. **O que falta do ticket é a política**, e a sua linha divisória está medida: a decisão de fluxo **não é um edit no `.cedar`** — o motor só conhece quatro atributos e em produção a `doc_read` e a `ocr_read` são a MESMA `action` (`cap:fs.read`), pelo que quem hoje barra a leitura com egress a um modelo é o TaintGate e não o Cedar; logo o critério 3 resolve-se no sentido afirmativo (campos novos no `Input`, mudança de motor com guard-test) e a decisão tem de entrar nas duas camadas, no molde do AOS-549. | Equipa AOS |
| 1.24 | 2026-10-25 | **AOS-556, o motor, a regra candidata e o interruptor** — seis das nove caixas fechadas, e as três que ficam são todas da cerimónia. Nasce o **ADR-044**, que o ticket exigia, com as duas incógnitas MEDIDAS em vez de supostas e as decisões do dono de 2026-10-25. **O que a medição inverteu:** o motor Cedar conhece QUATRO atributos e uma regra que leia outro assina e nega TUDO daquela capability (logo é mudança de MOTOR, com guard-test); a capability não distingue o destino, porque quatro das cinco tools de produção declaram `cap:fs.read` e quem hoje barra a `ocr_read` depois de dados de fora é o **TaintGate**, não o Cedar — a `allow_fs_read` já permite untrusted; e o `egress` TAMBÉM não distingue, porque a `ocr_read` e o `web_post` declaram os dois `external`, separando-os a `mutation`. Daí: o **destino** é uma CLASSE derivada dos três eixos que o registo já exige explícitos, sem eixo novo no manifesto; e a **proveniência** (`clean`, `handle_argument`, `context`, com `context` a dominar) é selada pelo kernel no sítio da re-junção, a única linha em que o piso e o valor da reescrita ainda são separáveis — depois do `join` um run com media e um run com handle nos argumentos ficam indistinguíveis, e é essa indistinguibilidade que tornava impossível o controlo negativo. **A política 1.2.0 CANDIDATA** vive em `docs/governance/` e não no bundle, porque pô-la lá sem assinatura nova faz o PDP recusar o bundle fail-closed; é assinada numa pasta temporária com chave descartável e provada par allow+deny por regra, mais o smoke. A `allow_fs_read` **parte-se por destino** (decisão do dono), com que a baseline do `policy-taint` fica **VAZIA** e o **critério 6 do AOS-363** fecha na mesma assinatura; a `allow_http_post` **não muda** — nenhuma proveniência satisfaz a sua cláusula, e é essa a assimetria pedida, imposta pela ausência de excepção. **O interruptor nas DUAS vias** (decisão do dono): `AOS_CONTEUDO_POR_HANDLE` HABILITA e é o recuo sem release, `conteudo_por_handle` no `POST /runs` escolhe por run, e **habilitar não é mover** — nenhum run muda de layout pela env, senão o campo não servia para nada. A habilitação exige também o produtor do `tool_result`: com a env `on` e sem ele o nó corre, NÃO habilita e recusa com 400, porque o layout seria alcançável mas inútil. **A bateria do critério 6** corre o caminho real — conteúdo selado no espaço de handles, referência resolvida pela reescrita que corre antes da mediação, destino selado pelo classificador a partir do registo, e decisão do PDP sobre a candidata — e prova as quatro coisas: a leitura externa sobre um handle untrusted PASSA (é o ganho da fase), o efeito que a injecção PEDE é NEGADO (o que flui é o que a regra autoriza, não o que o modelo pede), o mesmo caso num run com media é NEGADO (o controlo negativo), e a leitura local passa. Com um controlo que fixa que a 1.1.0 ainda nega, para o ganho não poder vir de outra coisa. **TRÊS DEFEITOS APANHADOS PELO CAMINHO, nenhum de origem nesta fatia:** a sonda do smoke montava o SEU contexto e um atributo num mapa e não no outro dava «record does not have the attribute» em toda a regra que o lesse — falso vermelho que faria a cerimónia parecer impossível; o corpo de um `permit` no gate NÃO acabava no `;` dele e absorvia as regras seguintes, pelo que um `||` de uma vetava a excepção de outra, e a excepção da escrita local do run só funcionava por ser a ÚLTIMA regra do ficheiro; e o manifesto dev-hardened deixava a `reversibility` da `doc_read` implícita, o que com a regra partida a punha em `unknown` e negava a leitura — declarar os três eixos passou a ser pré-condição da permissão, e os demos passaram a comportar-se como produção. **O que fica para a cerimónia:** assinar a 1.2.0, encolher a baseline (que o gate exige), mover os pares de cobertura, o R8 do ADR-034, e a prova em produção. Sem a assinatura o interruptor pode ligar-se e é SEGURO — nega a mais —, e não compra nada: a linha de postura di-lo em voz alta. | Equipa AOS |
| 1.25 | 2026-10-25 | **AOS-556, a revisão adversarial da fatia do motor: um CRÍTICO que muda o que se pede ao dono.** A regra de fluxo está escrita, assinável e provada — e **não basta**. Medido em `TestAOS556_Bateria_ACadeiaRealDecideOQue`, que compõe a cadeia na ORDEM REAL: a política PERMITE a leitura com egress externo sobre um handle untrusted e o **TaintGate NEGA a seguir**, porque a `ocr_read` declara `egress: external`, logo é privilegiada com o `AOS_PRIVILEGED_CAPS` armado em produção, e o gate corre DEPOIS da política numa cadeia que pára no primeiro não-permit. **Porque não apareceu antes:** a bateria do critério 6 compunha `classificador → política` e parava ali, e a prova de que o gate nega a MESMA call estava no teste ao lado, no mesmo ficheiro, sem as duas se encontrarem. É o caso-escola de teste a passar pela razão errada. **Consequência:** assinar a 1.2.0 como está esvazia a baseline, aperta `cap:fs.read` para quem não sela o destino, e não entrega o critério 1 nem a prova em produção; a nota da candidata abre agora com NÃO ASSINAR AINDA. A decisão que falta — dar ao gate a mesma coordenada, o que RELAXA a invariante P0 do ADR-005 para uma classe de destino — é o resíduo 1 do ADR-044, com os dois argumentos escritos, e é do dono. **Mais quatro achados aplicados:** o campo `conteudo_por_handle` furava o portão do `max_turns >= 2`, porque ele chaveava na CONFIG do nó e não no layout que o run vai ter — um run na 1.8.0 com inputs e um turno morria no tecto sem ver os dados, **pior do que sem o campo**; a derivação da proveniência não tinha teste nenhum e toda a asserção sobre ela era feita sobre o valor que o próprio teste fornecia (trocar os dois `case` não avermelhava nada, e com a política em vigor inverteria o controlo negativo do dono) — tem agora teste directo, provado por essa mutação; declarar a `reversibility` da `doc_read` dos demos era um ALARGAMENTO VIVO entregue sob um ticket que diz «desligado por omissão» (com a 1.1.0 em vigor, a segunda leitura de um run nos demos passava de DENY a PERMIT), pelo que foi **revertido** e a exigência dos três eixos passou para a nota da cerimónia; e a frase «vazia ou desconhecida ⇒ a regra de antes vale» era **falsa** em três comentários, porque na 1.2.0 a `allow_fs_read` inteira desaparece e `unknown` deixa `cap:fs.read` sem regra nenhuma. A nota da cerimónia passou de três linhas a cinco pontos, com os consumidores que avermelham enumerados. | Equipa AOS |
| 1.26 | 2026-10-25 | **AOS-556 — o gate aprende a coordenada (decisão do dono: «aceito a decisão, ensina a coordenada ao gate»).** O `TaintGate` ganha a terceira isenção, `fluxo_por_handle`, e é a primeira que RELAXA a invariante **P0 do ADR-005** — emendado no ADR-034, que é onde a semântica dela vive (o ADR-005 não está materializado como ficheiro). **Três condições, e nenhuma dispensável:** o destino declarado `external_read` com os três eixos explícitos; a capability e o recurso a **não** denunciarem rede; e a proveniência `handle_argument`. **A segunda é a que torna a isenção segura, e a formulação ingénua do achado esquecia-a:** uma tool `cap:http.post` com `mutation: none` e reversível TAMBÉM classifica `external_read`, e isentá-la deixava uma injecção exfiltrar para um host que ELA nomeia — com a condição, o destino é fixado pelo CÓDIGO da tool (o Model Gateway) e não por um argumento do modelo. A terceira dá de graça o controlo negativo do dono: num run com media a proveniência é `context` e nada se isenta. **O rótulo não muda** — a call continua untrusted no registo e o classificador continua a elevar a sensibilidade; «um só rótulo para todos os leitores» fica de pé. **E uma consequência que tem de estar escrita:** com a isenção no gate, o ganho da fase existe com a política **1.1.0** em vigor, porque a `allow_fs_read` dela permite `cap:fs.read` sem olhar ao taint. A 1.2.0 deixa de ser o que desbloqueia e passa a ser o que torna a decisão explícita e assinada, e o que fecha a dívida do critério 6 do AOS-363 — está em tensão com a §3(E) do ADR-044 («não uma classificação em código que muda com um deploy») e a tensão fica declarada. O que a torna aceitável é que as duas afirmações que importam continuam impostas pelo GATE e não pela política: o controlo negativo e a assimetria do efeito, medidos em `TestAOS556_IsencaoDoFluxoNaoSeEstende`. E o alcance continua governado pelo interruptor: a proveniência `handle_argument` só existe num layout por handle, que só se alcança a pedido e num nó que o habilite — desligado, a isenção nunca dispara. A via durável passa a VALIDAR o vocabulário da proveniência (`ErrBadAuthorizationProvenance`): agora que uma decisão de segurança depende dele, um valor que ninguém reconhece não deve chegar a um classificador que decide por igualdade de strings. | Equipa AOS |
| 1.27 | 2026-10-25 | **AOS-556 — a revisão de segurança da fatia do motor: zero achados ≥8, e três observações que valem mais.** **(a) O ADR afirmava uma coisa que o código não impunha.** A §2.10(2) dizia que o `!derivesEgress` garante que «o destino é fixado pelo CÓDIGO da tool»; o que ele confere são dois campos DECLARADOS pelo operador. Um manifesto com `capability: cap:doc.fetch` (prefixo dos locais provados), `resource_type: file` e `resource_value: https://{host}/x` passava as três condições da isenção e deixava o MODELO nomear o destino — uma afirmação num documento a servir de razão para ninguém verificar. Passa a ser IMPOSTA no arranque por `validarLeituraExterna`, no molde exacto do `validateResourceSensitivity`, que o repo já tinha para uma consequência MAIS FRACA (achado M-1 do AOS-539): uma tool que classifique `external_read` tem de ter o bloco `modelo` e não ter `sandbox`, ou o nó não arranca. **(b) A premissa do anúncio `first_turn_trusted`** — «o nó nega-as depois» — deixou de ser verdade para `external_read` na 1.8.0, e o `aos-orq` relaxa a P4 com base nela; o desfecho é o ratificado, a premissa é que já não é a que o nó cumpre. Declarado na §2.11, e fechá-lo pertence ao planeador. **(c) E a mais importante para quem espera o produto: a isenção não tem hoje tool que a exerça.** A `ocr_read` e a `video_read` têm um só parâmetro e ele é o slot do recurso, e um handle num slot é RECUSADO — nenhum handle chega a elas. A isenção abre uma decisão, não um caminho, e o caminho chega com a `model_invoke` da R3. É o que torna esta fase segura de fundir, e é a razão pela qual o critério 8 não pode fechar antes da R3 — está escrito no critério e na §7.4 do ADR. **O teste do guarda novo passou primeiro pela razão errada** e vale registar: `AOS_MODEL_TOOLS` é um CAMINHO e não JSON em linha, e a mensagem genérica de ficheiro ilegível contém a palavra «modelo» por acidente («o que o modelo chama»); a asserção ancora agora em `external_read`, que só o guarda novo nomeia. **E a suite completa do `aos-orq`, declarada duas vezes como não corrida, foi corrida: esgota os 30 minutos de `-timeout`** com o teste em voo a correr há 3 segundos — são os ~149 anteriores, a lentidão local documentada (~10× o CI). Quem a mede é o job `test` do CI, verde no #558. | Equipa AOS |
| 1.28 | 2026-10-26 | **AOS-557 — os portões fail-closed do run correm ANTES do selo das entradas por handle.** O contrato de conclusão e a origem da saída recusavam o run DEPOIS de `selarEntradasDoPlano`: um run condenado deixava `handle.created` no stream e bytes cifrados no espaço, que nada apaga, repetível por submissão até ao tecto do volume. Fecha-se com **uma só implementação da regra** (`recusaDeArranque`, que o `Runtime.Run` chama e que `RefuseAtStartup` expõe), chamada na hospedagem com a **oferta do catálogo cortada pela lista-branca** — a mesma fonte e o mesmo corte do congelamento. **A medição que mudou o desenho:** `Goal.Tools` está VAZIO na hospedagem (quem o escreve é o `ApplyFrozenToGoal`, dentro do `Run`), pelo que erguer o portão com o Goal tal como está recusaria todos os contratos. **O que a revisão adversarial corrigiu, e é o valor dela:** o âmbito estava derivado da FORMA do Goal (entradas por selar) e isso não distingue a terceira re-hospedagem — o `POST /runs` de um `run_id` em curso (AOS-520) chega com o Goal do corpo e entradas sem referência, e o seu tool set vem do snapshot durável; sem a bandeira `rs.existente` a conferência recusava um run que o `Run` admite. Mais: dois testes eram tautológicos (comparavam `RefuseAtStartup` consigo própria) e foram substituídos por um que mede contra o `SecuredRuntime.Run` REAL, que foi o que revelou a divergência de CAUSA no ramo da lista-branca. Os outros cinco portões ficam nomeados no bloco do selo, um a um, com a razão (o enunciado contava seis onde há sete: faltava o `rt.validate`); e a **janela do crash continua declarada e NÃO é fechada** (AOS-554) | Equipa AOS |
