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
- [x] O adaptador escolhido implementa o contrato de activity de AOS-021 e passa os testes de idempotência (AOS-014) e replay (AOS-016) sem alterações à API do RT.
- [x] Se a decisão for "contrato próprio", o spike confirma que AOS-014/015/016/021 cobrem integralmente o contrato e documenta o gap, se houver.

### Detalhes Técnicos
- Componente: `RT` (camada de durabilidade). Ficheiros sugeridos: `runtime/durable/engine_adapter.*`, `docs/adr/ADR-0XX-durable-execution.md`.
- O adaptador expõe a mesma interface de activity/checkpoint/replay independentemente do backend; o RT não deve saber qual engine está por baixo (coerência por contrato, Princípio 8).
- Considerar o encaixe com o Escalonador (EPIC-03) para leases/fencing e com o Event Store como fonte de verdade.
- Time-box do spike explícito; a parte de feature só arranca após o ADR ratificado.

### Testes Requeridos
- PoC comparável: cenário de referência corre em cada opção; métricas de replay e duplicação recolhidas.
- Contrato: o adaptador escolhido passa a suíte de idempotência e replay (harness de AOS-024).
- Isolamento: trocar o backend não altera a API do RT (teste de contrato).

### Definition of Done
- [ ] Matriz de decisão e PoCs entregues; ADR novo escrito e ratificado (assinado).
- [x] Adaptador do backend escolhido implementado e a passar idempotência + replay.
- [ ] Sem contradição com ADR-001/007; lock-in avaliado e documentado.
- [ ] Código revisto (dois revisores — decisão de arquitectura); testes verdes.
- [x] `tecnica/02` e `specs/00_System_Spec.md` actualizados com a decisão.

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
