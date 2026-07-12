# `control` — Canal de steer/interrupt e estado `paused` (AOS-023)

Subpacote do Agent Runtime (`packages/kernel/agent-runtime`) que implementa o
**controlo bidireccional** do ADR-013: um canal **out-of-band** que pausa o run
no fim do turno, injecta uma correcção autenticada e retoma — materializando o
estado durável `paused` sobre a máquina de estados de AOS-017.

Fonte de verdade: `specs/EPIC-02_…` §AOS-023; `tecnica/02` §5.1; ADR-013
(steer/interrupt, gates), ADR-001 (durável), ADR-005 (untrusted vs instrução).

Zero dependências externas. Assenta em `substrate/eventstore` (AOS-002), na
máquina de `state` (AOS-017) e reusa a porta `Tracer` do Agent Runtime (AOS-013).

## O canal de CONTROLO é separado do canal de DADOS

Os sinais `pause`/`steer`/`resume` entram por um `SteerChannel` indexado por
`run_id` — **nunca pelo prompt**. É a distinção central: o prompt é o canal de
**dados** (conteúdo de tools/web, *untrusted* por construção — ADR-005); o
`SteerChannel` é o canal de **controlo** (sinais autenticados que dirigem o
agente). Confundi-los seria a escalada de privilégio que este pacote impede.

## API

| Método | Efeito |
|---|---|
| `Pause(ctx, run_id, emitter)` | Regista sinal e marca pausa **pendente** (não interrompe a meio). |
| `GracefulPause(ctx, run_id, gate)` | **Fronteira de fim de turno**: se pendente, `running → paused`. |
| `Steer(ctx, run_id, correction, emitter)` | Grava a correcção **autenticada** (não-repúdio); fica pendente. |
| `Resume(ctx, run_id, emitter, gate)` | Aplica a correcção (**trusted**) e `paused → running`. |
| `Rebuild(ctx, run_id)` | Reconstrói a projecção (pausa/correcção pendentes) do log. |
| `PendingPause` / `PendingCorrection` | Acessores da projecção (consultados pelo loop). |

## Graceful pause — nunca a meio de uma activity

`Pause` só marca a pausa; o loop chama `GracefulPause` na **fronteira de fim de
turno**. Só aí, com **todas** as activities do turno confirmadas (sem efeitos
parciais), a transição `running → paused` se materializa via o `StateGate`
(`MachineGate` sobre `state.Machine.Pause`/`Resume` de AOS-017). Um pause emitido
a meio de um turno respeita a fronteira de activity: só toma efeito no fim.

## Steer autenticado e NÃO-untrusted (fronteira de segurança)

Um `Steer` é **autenticado** (`Authenticator`) e gravado como `control.steer`
**com a identidade do emissor** — o registo de **não-repúdio** (ADR-013). É uma
**instrução do canal de controlo**, distinta de dados *untrusted* (ADR-005):

- conteúdo *untrusted* não tem credencial de emissor válida → `Authenticate`
  rejeita-o (`ErrUnauthenticated`) → **nunca** se torna um steer;
- um steer sem autenticação válida é **rejeitado** (não toca no log);
- na retoma, a correcção entra no loop com taint **`trusted`** — instrução
  confiável, nunca dado *untrusted*.

`HMACAuthenticator` é a realização de referência (HMAC-SHA256, só stdlib); a
assinatura cobre `(run_id ‖ kind ‖ payload)` — sem replay cross-run/kind. O
`Emitter` é um contrato de identidade **mínimo local**: **não** importa
`platform/identity` (AOS-005) de propósito — zero deps + sem ciclo de módulos;
liga-se por um adaptador de `Authenticator` quando o wiring de EPIC-12 o exigir.

## Durável + reproduzível por replay

Cada sinal é **um** evento append-only (`step_id` namespaced `ctrl-N`). A
projecção é uma **dobra** desses eventos, reconstruível por `Rebuild`: o ciclo
`pause → steer → resume` **sobrevive a crash** (crash em `paused` → um worker
novo relê o log e recupera a correcção **intacta**) e reproduz-se fielmente por
**replay** (AOS-016).

## Fora de âmbito

Paridade de superfícies (Slack/Telegram como cards) — UX de **EPIC-12**. Ligação
física do loop de AOS-013 ao canal (chamar `GracefulPause` na fronteira real e
injectar a `Correction` no tail) — wiring de integração; aqui fica o **contrato**
e a prova por testes determinísticos (relógio e canal injectáveis, sem sleeps).

## Testes

`go test ./... -race` — determinístico, `-race` limpo. Cobre: pausa no fim do
turno (nunca a meio de uma activity); steer aplicado na retoma + emissor
registado (não-repúdio); crash em `paused` → retoma com a correcção intacta;
replay fiel do ciclo; e a **segurança** — steer não autenticado rejeitado e
conteúdo *untrusted* que não pode tornar-se um steer (ADR-005, privilege
escalation).
