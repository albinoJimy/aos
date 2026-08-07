# Desenho da retoma — etapa 3 do bridge negação → aprovação → reexecução

> **Estado:** DESENHO (nenhum código escrito) · **Data:** 2026-08-07
> **Contexto:** etapas 1 (`21547d8`, ApprovalGate no kernel) e 2 (`8ec566d`, ApprovalBroker) estão feitas. Esta é a etapa que liga o ciclo de vida do run — e a de maior risco.
> **Decisão pedida:** aprovar a forma da retoma (§4) e a resposta ao §6 antes de escrever código.

## 1. Onde estamos e o que falta

Hoje, com as etapas 1-2, uma call aprovada **já destrava** se a evidência lhe chegar. O que falta é o ciclo:

```
escalate → run pára → humano aprova → run retoma → a MESMA call volta a ser mediada
```

Hoje o `escalate` chega ao loop indistinguível de uma negação (o meu commit `60f5d64` materializa-o no tail e o loop **continua para o turno seguinte**). Ninguém pára o run, ninguém pede aprovação, e a evidência nunca é anexada.

## 2. A restrição que define o desenho

O log durável **não regista os inputs das tool calls**. Está escrito no próprio código (`durable/resume.go:34-38`):

> o `turn.recorded` grava só a CONTAGEM de tool calls (não os inputs); o evento de mediação do RM grava o ToolID mas **NÃO o Input**; e o cursor REFERENCIA por posição, não copia.

**Consequência directa e crítica:** a aprovação está amarrada à `ApprovalPreview`, que inclui `hash(Input)`. Se, ao retomar, o loop simplesmente **voltar a chamar o modelo**, o modelo pode emitir uma call diferente — e diferente ela será, porque o prompt do turno seguinte já leva o marcador de negação que a etapa anterior introduziu. Preview diferente ⇒ a aprovação **não se aplica**.

O resultado seria fail-closed (seguro), mas **funcionalmente inútil**: o humano aprovaria algo que nunca mais volta a ser apresentado de forma idêntica.

**Portanto: retomar não pode ser «voltar a perguntar ao modelo».**

## 3. A peça que resolve — e já existe

O **capturer de replay** persiste a `ModelResponse` COMPLETA de cada turno, com as tool calls **incluindo o `Input`** (`replay/nondeterminism_capture.go`, `toolCallCapture.Input`; cifrada por-titular quando há sealer). E o motor de replay já sabe devolver a resposta registada em vez de chamar o modelo (`replayModelClient`).

Ou seja: **o material para re-apresentar a call escalada byte-a-byte já é gravado hoje**, e o mecanismo para o reproduzir já existe. Não é preciso inventar persistência nova.

## 4. Desenho proposto

### 4.1 Princípio

> Retomar um turno escalado é **reproduzir** esse turno a partir da captura — nunca reinterrogar o modelo. Assim a call que o humano aprovou é exactamente a call que volta a ser mediada, e a amarra da preview funciona.

### 4.2 Fluxo

```
1. Turno N: o modelo emite a call privilegiada
2. RM: RiskGate classifica gray/danger  →  ESCALATE
      └─ audit sela tool.call.escalated (nenhum efeito ocorreu)
3. Loop: reconhece escalate (≠ deny) na fronteira de fim-de-turno
      ├─ transição durável running → waiting_on_human   (JÁ é transição válida)
      ├─ regista o PENDENTE: preview + descrição legível + classe de risco
      └─ Result.Escalated = true, EscalatedPreview = <digest>   → o loop PÁRA
4. Operador faz polling de /runs/{id}: vê o pendente (o que vai executar + risco)
5. POST /runs/{id}/approve com as pernas assinadas sobre a preview
      └─ ApprovalBroker.Approve → cerimónia four-eyes → grant (TTL 15 min)
6. Resume: waiting_on_human → running   (JÁ é transição válida)
      └─ o turno N é REPRODUZIDO a partir da captura (não há chamada ao modelo)
      └─ a call escalada é re-mediada COM a evidência do grant anexada
7. ApprovalGate verifica → TaintGate consulta a prova → cadeia COMPLETA decide
8. Efeito executa sob permit, com os aprovadores no registo de mediação
```

### 4.3 Os quatro riscos e como cada um fecha

| Risco | Como fecha |
|---|---|
| **R1 — dupla execução.** O turno N tinha 3 tool calls; a #1 e a #2 **executaram**, a #3 escalou. Ao reproduzir o turno, as duas primeiras não podem correr outra vez. | O `StepLedger` verifica *already-applied* **antes de qualquer efeito**, com chave `run_id:step_id`. O `step_id` de cada activity é **determinista** (`<step>-tool-<idx>`), logo ao reproduzir o turno as activities #1 e #2 batem na chave já aplicada e devolvem o resultado memorizado **sem re-executar**. Só a #3 (nunca aplicada) corre. *Pré-condição: execução durável ligada.* |
| **R2 — aprovação aplicada a outra acção.** | A amarra da preview (etapa 1) + o uso-único do grant (etapa 2). Já fechado e testado. |
| **R3 — o modelo produz outra call na retoma.** | Não há chamada ao modelo na retoma: o turno é reproduzido da captura. É o ponto central de §4.1. |
| **R4 — aprovação a envelhecer.** | TTL de 15 min; e `{WaitingOnHuman, Killed}` já existe como transição para o timeout fail-closed. |

### 4.4 O que NÃO muda

- A call re-mediada atravessa a **cadeia inteira** (identidade, revalidação, PDP, taint, escopo, egress). A aprovação remove **um** obstáculo.
- O **taint continua untrusted** — como selado na etapa 1.
- Sem execução durável/capturer, o escalate **degrada para negação** (não há como reproduzir o turno com fidelidade). Fail-closed e honesto.

## 5. Superfície nova

| Onde | O quê |
|---|---|
| `agent-runtime` | `Result.Escalated` + `EscalatedPreview`; reconhecer `EffectEscalate` no loop; porta para registar o pendente |
| `integration` | Ligar o `ApprovalBroker` ao `SecuredConfig`; resume que reproduz o turno da captura |
| `cmd/aos` | Pendente exposto em `GET /runs/{id}`; `POST /runs/{id}/approve` a chamar `Approve`; resume; env documentada (gate AOS-203) |

## 6. A decisão que preciso de ti

**O escalate exige execução durável (checkpointer + capturer + ledger) ligada.** Sem ela não há como reproduzir o turno nem como impedir a dupla execução da R1. Duas posturas:

- **(A) Exigir.** Com `AOS_MODE=production`, ligar o four-eyes sem execução durável **aborta o arranque** (a par das outras exigências fail-closed já existentes). Coerente com a postura do nó; obriga o operador a ligar o que a funcionalidade precisa.
- **(B) Degradar.** Sem execução durável, o escalate comporta-se como negação e o banner declara-o. Mais permissivo; o risco é o operador julgar que tem aprovação humana quando na prática tem só negações.

**Recomendo (A)** — pela mesma razão que o nó já recusa arrancar em produção sem issuer, TLS e OIDC: uma funcionalidade de segurança meio-ligada é pior do que desligada, porque cria uma expectativa falsa.

Preciso também de confirmar §7 da proposta anterior, ponto 2, agora que o desenho está concreto: **no fim dos 15 min sem aprovação**, o run vai a `Killed` (a transição existe e é o fail-closed natural) ou volta a `running` com a call negada, deixando o agente continuar sem aquele efeito? A primeira é mais segura; a segunda é mais útil. **Recomendo a segunda** — o agente sabe que foi negado (o marcador do `60f5d64`) e pode tentar outro caminho, e nada executou.

## 7. Esforço e ordem

1. Loop reconhece escalate + `waiting_on_human` + Result (pequeno; testável isoladamente)
2. Registo do pendente + exposição em `GET /runs/{id}` (pequeno)
3. **Retoma reproduzindo o turno da captura (o item difícil e o de maior risco)**
4. `POST /approve` a ligar ao broker + wiring do nó (médio)
5. Testes de composição: R1 (não re-executar), R3 (call idêntica), TTL, e um end-to-end negado→aprovado→executado

O item 3 é o que justificou parar aqui. Só avanço depois do teu aval a §6.
