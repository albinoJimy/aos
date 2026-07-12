# ADR-015 — Durable execution: contrato próprio vs. engine externo

| Campo | Valor |
|---|---|
| Estado | **Proposta — a aguardar ratificação humana assinada** (AOS-022, fase spike) |
| Data | Julho de 2026 |
| Decisores | Arquitecto de Plataforma (+ ratificação assinada — ver Tabela de aprovação) |
| Tickets | AOS-022 (spike+feature); depende de AOS-014/015/016/021 |
| ADRs relacionados | ADR-001 (execução durável como primitivo), ADR-007 (Event Store replicado como fonte de verdade), Princípio 8 (coerência por contrato / anti lock-in) |
| Supersede | — |

> Este ADR resolve a decisão explícita da fonte: **adoptar um engine de durable
> execution (Temporal/Restate/DBOS) OU consolidar o contrato próprio** já
> materializado no EPIC-02. A fase *feature* (o `engine_adapter`) só arranca
> **após** este ADR ser ratificado (assinado).

---

## 1. Contexto

O ADR-001 fixa a execução durável ao nível do passo como primitivo não-negociável:
idempotência por passo `f(run_id, step_id)`, checkpoint intra-iteração, replay
determinístico *resume-from-step*, e efeitos externos isolados em *activities*. A
questão de AOS-022 **não** é *se* temos durable execution — é *que substrato* a
implementa: um engine de terceiros ou o contrato próprio.

Ponto de partida factual (o que já está entregue e testado no EPIC-02):

| Garantia (ADR-001) | Entregue por | Evidência (testes, `-race`) |
|---|---|---|
| Idempotência por passo | AOS-014 (`durable`) | `TestFaultInjection*`, `TestApplyIdempotentReexecution` — 0 efeitos observáveis duplicados |
| Checkpoint intra-iteração | AOS-015 (`durable`) | `TestResume_CrashPoints` (6 pontos), failover de worker |
| Replay determinístico | AOS-016 (`replay`) | `TestReplayFidelity100`, `TestReplayZeroExternalEffects`, detecção de drift/divergência |
| Máquina de estados durável | AOS-017 (`state`) | matriz 10×10, `Rebuild`, fail-closed |
| Liveness lease/fencing | AOS-018 (`durable`) | token monotónico, zero-dup sob reatribuição (com a fronteira documentada) |
| Sagas de compensação | AOS-020 (`saga`) | LIFO idempotente, crash-resume |
| Activities isoladas + mediadas | AOS-021 (`activity`) | no-bypass estrutural, replay-zero-efeito, lint de separação |

O contrato próprio **já corre** o cenário de referência (run multi-passo com crash
e retoma) com fidelidade de replay demonstrada e 0 efeitos observáveis duplicados,
tudo sem dependências externas e com o **Event Store replicado como única fonte de
verdade** (ADR-007).

---

## 2. Opções avaliadas

### Matriz de decisão

Eixos: idempotência/replay · operação & HA · custo · lock-in · latência · encaixe
com o Event Store replicado (ADR-007). Escala: ✅ forte · 🟡 médio/condicional · ❌ fraco.

| Eixo | **Contrato próprio** (AOS-014/015/016/021) | Temporal | Restate | DBOS |
|---|---|---|---|---|
| Idempotência / replay | ✅ já testado (replay 100%, 0-dup) | ✅ maduro (event history + activities) | ✅ (journal + virtual objects) | ✅ (workflow em Postgres) |
| Operação & HA | 🟡 herda o HA do ES replicado; sem componente extra | ❌ cluster próprio (frontend/history/matching) + persistência própria a operar | 🟡 runtime próprio, mais leve que Temporal | 🟡 acopla a durabilidade a Postgres (HA do Postgres) |
| Custo | ✅ zero deps; sem infra adicional | ❌ footprint operacional e computacional alto | 🟡 moderado | 🟡 moderado (Postgres) |
| Lock-in (Princípio 8) | ✅ nenhum; API é nossa | ❌ modelo de programação e formato de history proprietários | 🟡 SDK próprio | 🟡 acoplado ao Postgres/DBOS SDK |
| Latência | ✅ append ao ES (µs no ref-impl) | 🟡 round-trips ao cluster | 🟡 ao runtime | 🟡 ao Postgres |
| Encaixe com ES (ADR-007) | ✅ o ES **é** o log durável — uma só fonte de verdade | ❌ **duas** fontes de verdade (history do Temporal ≠ ES) | ❌ log próprio ≠ ES | ❌ estado em Postgres ≠ ES |
| Maturidade / suporte | 🟡 ref-impl; fronteiras documentadas (fencing por-escrita, adopção pelo loop) | ✅ ecossistema grande | 🟡 mais jovem | 🟡 mais jovem |

### Observação decisiva (ADR-007)

Todos os engines externos trazem **o seu próprio log de durabilidade** (event
history / journal / tabelas Postgres). Isso cria **duas fontes de verdade** — a do
engine e o Event Store replicado que o ADR-007 fixa como *a* fonte de verdade. Ou
se reconcilia (complexidade e risco de divergência), ou se subordina o ES ao engine
(contradiz ADR-007). O contrato próprio **não** tem este problema: a durabilidade *é*
o log append-only do ES.

### Nota sobre as PoCs (honesta, âmbito)

- **Contrato próprio:** PoC = a suíte real do EPIC-02 (crash+retoma, replay 100%,
  0-dup) — corre neste ambiente, `-race` limpo.
- **Temporal / Restate / DBOS:** uma PoC *executável* exigiria, respectivamente, um
  cluster Temporal, o runtime Restate e um Postgres+DBOS — dependências pesadas,
  não instaláveis neste ambiente e em tensão com a tese de supply-chain mínima
  (ADR-005). A avaliação acima assenta nas garantias publicadas de cada engine e no
  encaixe arquitectural, **não** numa PoC executada — limitação declarada. A prova
  de que o *contrato de activity* é agnóstico ao backend é entregue pela interface
  `engine_adapter` (fase feature), não por correr os 3 engines.

---

## 3. Decisão (proposta)

**Consolidar o contrato próprio (AOS-014/015/016/021) como o substrato de durable
execution do AOS, expondo uma interface `engine_adapter` estável que mantém o RT
agnóstico ao backend (Princípio 8).** Um engine externo (Temporal/Restate/DBOS)
fica como **backend plugável** opcional, não como um rewrite — a adoptar só se
necessidades de escala/operação o justificarem, e sempre subordinando-se ao ES
como fonte de verdade.

Racional: (a) o contrato próprio já satisfaz ADR-001 com evidência testada; (b) é o
único que honra ADR-007 (uma só fonte de verdade); (c) zero lock-in e zero infra
adicional; (d) a interface `engine_adapter` preserva a opção de trocar de backend
sem tocar na API do RT.

---

## 4. Consequências

**Positivas:** uma só fonte de verdade (ES); sem infra/deps extra; sem lock-in; o
RT permanece agnóstico ao backend por contrato.

**Negativas / custos assumidos:** somos donos da correcção do substrato (o engine
não a oferece de graça) — mitigado pela suíte de replay/idempotência (AOS-024) como
gate contínuo. Fronteiras já documentadas a fechar em tickets próprios:
- *enforcement* de fencing **por-escrita** exige tornar o ES fencing-aware (AOS-018, item aberto);
- adopção do `activity.Dispatcher` **pelo loop** (AOS-021, wiring diferido);
- HA de produção depende do ES replicado real (NATS/JetStream), validado em staging.

**Reversível?** Sim, por desenho: a interface `engine_adapter` permite introduzir
Temporal/Restate/DBOS como backend sem alterar o RT.

---

## 5. Alternativas rejeitadas (resumo)

- **Temporal:** rejeitado como substrato *primário* por footprint operacional,
  lock-in de modelo e, sobretudo, a segunda fonte de verdade vs ADR-007. Permanece
  candidato a backend plugável.
- **Restate / DBOS:** mais leves que Temporal, mas partilham a objecção da segunda
  fonte de verdade e adicionam dependência de runtime/Postgres. Candidatos a backend.

---

## 6. Fase feature (após ratificação)

Só arranca depois deste ADR ratificado (assinado). Entrega: `engine_adapter` que
implementa o contrato de activity de AOS-021 e passa a suíte de idempotência
(AOS-014) e replay (AOS-016) **sem alterar a API do RT**; um teste de contrato prova
que trocar o backend (ref-impl próprio ↔ stub de engine) não muda o RT.

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
| 0.1 (proposta) | Julho 2026 | Emissão da proposta para ratificação (AOS-022 fase spike) | Equipa AOS |
