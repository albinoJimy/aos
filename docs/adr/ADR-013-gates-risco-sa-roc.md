| Campo | Valor |
|---|---|
| ADR | ADR-013 |
| Título | Gates de risco SA-ROC + controlo bidireccional |
| Estado | Aceite |
| Data | Julho 2026 |
| Deciders | Equipa AOS |
| Contexto-fonte | Catálogo de ADRs em `_BRIEF.md` (linha 82) e `specs/00_System_Spec.md` (linha 256); materializado em EPIC-08/09/12 (AOS-023, AOS-074, AOS-089, AOS-095, AOS-096, AOS-119..128) |

## Contexto

Um agente autónomo executa *tool calls* cujo impacto varia de uma leitura local trivial a um `delete`/`send`/`transfer` irreversível ou à exfiltração de dados sensíveis por egress externo (o vector CamoLeak). Um gate binário — "tudo pede confirmação" ou "tudo corre" — falha nos dois extremos: gera *approval fatigue* (o humano carimba sem ler, anulando a governação) ou deixa passar efeitos destrutivos sem supervisão. O eixo errado é "a acção é destrutiva?"; o eixo certo é o **risco real** composto por sensibilidade × egress × reversibilidade.

Simultaneamente, um agente em execução tem de ser **controlável em tempo real**: um operador precisa de o poder pausar, corrigir e retomar sem matar o run nem perder o estado durável — e essa correcção tem de entrar por um canal SEPARADO do prompt, sob pena de conteúdo *untrusted* (resultado de tool, página web) se promover a instrução de controlo (a escalada de privilégio que o ADR-005 impede).

Este ADR fixa a decisão, já **implementada**, de duas metades acopladas:

1. **Gates de risco SA-ROC** (Safe / gRAy / danger + Reversibility · Ownership · Confirmation) — fricção proporcional à classe de risco, com anti-rubber-stamping medido.
2. **Controlo bidireccional** — o canal out-of-band `steer/interrupt` (pausar → corrigir → retomar), durável e reproduzível por replay.

Ambas são as fundações que a camada de superfícies (EPIC-12/EPIC-13) **consome**: a superfície APRESENTA o preview e recolhe a decisão, mas NUNCA é autora do enforcement — este vive no Reference Monitor e no plano de governança.

## Decisão

### 1. Tiering SA-ROC por três eixos, fail-closed

Cada acção é classificada numa `risk.Class` — `ClassSafe` < `ClassGray` < `ClassDanger` — a partir de três eixos, **todos fail-closed pelo tipo** (o valor-zero resolve para o pior caso):

- **Sensibilidade** (`risk.Sensitivity`): `Unknown` resolve para `Sensitive` (topo).
- **Egress** (`risk.Egress`): `Unknown` resolve para `External` (pior caso de exfiltração).
- **Reversibilidade** (`risk.Reversibility`): `Unknown` resolve para `Irreversible`.

O taint *untrusted* (ADR-005/AOS-069) **eleva** a sensibilidade efectiva um nível (`Action.effectiveSensitivity`). O mapa de decisão é puro e determinista (`risk.Classify`): DANGER se irreversível OU egress externo de dados sensíveis; SAFE se local, reversível e sensibilidade ≤ interna; GRAY para o risco residual agrupável. A política é **policy-as-code versionada** por um digest sha256 canónico (`risk.Policy.Version` → `"saroc-v1#<digest12>"`), selado no audit.

Os eixos derivados da capability/recurso são um **piso não-mentível**: `capabilityIrreversible` marca `delete/send/transfer/…` como irreversíveis independentemente do texto do chamador, e `egressForCall` resolve egress a partir de uma allowlist de capabilities locais (tudo o resto → `EgressUnknown` → externo). O texto auto-declarado só pode **elevar** o risco, nunca baixá-lo (`maxSensitivity`, `reversibilityForCall`).

### 2. Fricção proporcional (SA-ROC)

O `risk.Gate` aplica a fricção por classe:

- **SAFE** → corre sem gate (sem HITL, sem contagem de override).
- **GRAY** → auto-aprovável por maturidade (anti-fatigue) OU **confirmação de lote**: uma única confirmação por `BatchKey` cobre acções materialmente equivalentes (mesmo run + capability + destino resolvido — `batchKeyForCall`, SAROC-03); acções gray diferentes re-solicitam confirmação.
- **DANGER** → **NUNCA auto-aprovável** (prova estrutural em `AutoApprovePolicy.Allows`: o caso `ClassDanger` devolve `false` incondicionalmente). Escala para **confirmação individual** com o **preview do efeito concreto resolvido** (`buildPreview`: capability + tipo:valor:região do recurso, sem segredos).

### 3. Gate de aprovação-de-plano

A "aprovação-de-plano" do catálogo materializa-se como o caminho danger do SA-ROC aplicado ao efeito concreto resolvido, reforçado por duas invariantes de enforcement:

- **SAROC-04**: uma acção danger com egress mas **sem destino concreto** (`Resource.Value` vazio) é **negada fail-closed** (`RiskGate.Evaluate`) — proíbe-se aprovar egress com um preview genérico que esconda o destino.
- **Escada de autonomia** (ADR-014/AOS-089): os níveis L0 (sugestão: humano executa tudo) e L1 (aprovação por acção) são o gate de plano na sua forma mais forte; L3 é EXACTAMENTE o tiering SA-ROC base (`autonomy.Oversight`).

A superfície de *plan-approval* dedicada (drill-down de plano multi-passo antes do arranque) é **wiring de superfície de EPIC-13 que consome este ADR** — pendente do apex de integração (PR-0); o mecanismo de enforcement (preview concreto + confirmação danger + fail-closed sem destino) já existe e é a autoridade.

### 4. Timeout fail-closed

Para toda a classe danger o gate impõe um **timeout de guarda**: a ausência de aprovação dentro do intervalo NEGA, nunca permite (`Gate.evaluateDanger`). Para acções **irreversíveis** o bound é **não-desactivável**: mesmo com `WithTimeout(≤0)` aplica-se o piso `DefaultTimeout` (30 s), garantindo um limite temporal independente do ctx do chamador. O silêncio do canal HITL resolve sempre em `OutcomeDeny` com `TimedOut=true`.

### 5. Controlo bidireccional out-of-band (steer/interrupt)

O `control.SteerChannel` (AOS-023) é o canal de controlo **separado do canal de dados** (o prompt). Vocabulário mínimo: `pause`, `steer`, `resume`.

- **Pausa graciosa**: `Pause` marca uma pausa PENDENTE; a transição `running→paused` só se materializa no **fim do turno** (`GracefulPause` sobre o `StateGate`/AOS-017), nunca a meio de uma activity — sem efeitos parciais.
- **Steer**: `Steer` injecta uma correcção **autenticada** e gravada como evento append-only `control.steer` com a identidade do emissor. A correcção é aplicada na retoma como **instrução confiável** (`Correction.Tainted()` → `TaintTrusted`, `TailSteer`), o oposto exacto de um dado *untrusted*.
- **Resume**: `Resume` autentica, consome a correcção pendente, grava `control.resume` **antes** de materializar `paused→running` (ordem **audit-first**: se o append falha, nada transita — a máquina fica paused e a correcção intacta).

**Fronteira de segurança**: cada sinal passa por um `Authenticator` ANTES de tocar no log (`record`). Sem assinatura de emissor válida, o sinal é rejeitado com `ErrUnauthenticated` — conteúdo *untrusted* não carrega credencial de emissor, logo NUNCA se torna um sinal de controlo. O `Emitter` (ID + assinatura) é o registo de **não-repúdio**: o log prova quem pausou, corrigiu ou retomou.

**Durabilidade e replay**: cada sinal aceite é um evento append-only no Event Store (AOS-002); a projecção corrente é uma dobra reconstruível por `Rebuild`, pelo que o ciclo pause→steer→resume **sobrevive a crash** e reproduz-se por replay (AOS-016).

### 6. Override-rate medido (anti rubber-stamping)

O `risk.Metrics` mede o **override-rate** = `Overrides/Prompted` (fracção de acções gray/danger *prompted* que o humano aprova). Contadores atómicos separam auto-aprovadas (não contam — não houve prompt) das cobertas por lote (`BatchCovered`, que expõe a amplificação aprovação→efeitos). O gate HITL concreto (`hitl.Channel`, AOS-095) expõe o sinal OTel `approval.override_rate` e dispara `SignalHighOverrideRate` acima de `DefaultOverrideRateThreshold` (0.40) — o limiar documentado no Art. 14 do EU AI Act para *approval theater*. A classe, o aprovador (`RiskApprover`) e o modo de decisão (`RiskDecisionMode`: auto/batch/human/timeout/denied) são **selados no audit** para atribuição tamper-evident.

## Consequências

### Positivas

- **Fricção calibrada ao risco real**: uma leitura sensível seguida de egress externo é danger mesmo sem operação destrutiva — o eixo passa de "destrutivo" para "risco de exfiltração".
- **Anti-fatigue sem bypass**: a auto-aprovação e o lote reduzem prompts, mas danger/irreversível NUNCA é auto-aprovável (prova estrutural, não configuração).
- **Fail-closed em toda a fronteira**: eixos desconhecidos, gate ausente, canal ausente, timeout, panic do canal e egress sem destino — todos resolvem para deny/pior-caso.
- **Controlo em tempo real sem perda de estado**: pausar/corrigir/retomar preserva a durabilidade e sobrevive a crash por replay.
- **Não-repúdio e explicabilidade**: quem corrigiu/aprovou, com que política versionada e por que via, ficam selados no audit tamper-evident.
- **Superfície desacoplada**: o enforcement vive no kernel; qualquer superfície (TUI, desktop, Slack/Telegram) é apenas consumidora — não pode enfraquecer o gate.

### Negativas / Trade-offs

- **Latência e bloqueio**: acções danger bloqueiam à espera de um humano até ao timeout; um canal HITL lento degrada o throughput (mitigado pelo timeout fail-closed, mas ao custo de negar por silêncio).
- **Custo de calibração**: as allowlists de capabilities locais/de rede e os tokens irreversíveis são um superset deliberado que exige manutenção; um verbo destrutivo novo não catalogado cai (com segurança) em `EgressUnknown`/irreversível, gerando fricção até ser classificado.
- **Amplificação por lote**: uma confirmação de lote demasiado larga cobre muitas acções (`BatchCovered`) — mede-se, mas não se impede por construção; exige vigilância operacional.
- **Autenticador de referência não-produção**: o `HMACAuthenticator` prova a fronteira e serve testes deterministas, mas não defende contra replay do MESMO sinal (sem nonce/sequência) — a identidade ed25519 real com cadeia de delegação (AOS-005) tem de ser ligada por adaptador antes de produção.
- **Wiring pendente**: a composição `DefaultHooksWithRisk` é opt-in e o canal HITL real, o `SteerChannel` e o gate estão INERTES até o composition root ápice (`packages/integration`, PR-0) os montar — a superfície de plan-approval dedicada depende disso.

## Alternativas consideradas

- **Gate binário destrutivo/não-destrutivo**: rejeitado — cega para exfiltração (leitura sensível + egress não é "destrutivo" mas é danger) e gera fatigue ou passa efeitos irreversíveis.
- **Confiar no texto auto-declarado do chamador** (sensibilidade/reversibilidade declaradas): rejeitado — mentível; adoptou-se o piso derivado da capability/recurso, que o texto só pode elevar.
- **Steer pelo prompt** (injectar a correcção no canal de dados): rejeitado — refunde controlo e dados, viola a separação de taint (ADR-005) e abre a escalada de privilégio; o canal out-of-band autenticado é a defesa.
- **Interrupção dura a meio do turno**: rejeitada em favor da pausa graciosa no fim do turno — evita efeitos parciais de activities incompletas.
- **WebSocket duplex para o controlo**: fora do âmbito deste ADR (tratado no ADR de UI-governança); a separação canal-de-dados/canal-de-controlo aqui fixada é o fundamento que o transporte da superfície tem de respeitar.
- **Timeout desactivável para irreversível**: rejeitado — o pior caso (efeito não-desfazível) nunca pode ficar sem guarda temporal por misconfiguração; daí o piso não-desactivável.

## Conformidade / Enforcement

Onde a decisão é imposta no código:

- **Classificação por três eixos + policy versionada**: `packages/kernel/reference-monitor/risk/classifier.go` (`Sensitivity`/`Egress`/`Reversibility` fail-closed, `Class`, `Classify`, `Policy.Version`/`DefaultPolicy`).
- **Fricção SA-ROC + auto-aprovação anti-bypass + timeout fail-closed + override-rate**: `packages/kernel/reference-monitor/risk/gate.go` (`Gate.Evaluate`/`evaluateDanger`/`evaluateBatch`, `AutoApprovePolicy.Allows`, `DefaultTimeout`, `Metrics.OverrideRate`, `DenyChannel`).
- **Hook de enforcement e pisos não-mentíveis**: `packages/kernel/reference-monitor/risk_gate.go` (`RiskGate.Evaluate`, `egressForCall`, `capabilityIrreversible`, `sensitivityForCall`, `buildPreview`, `batchKeyForCall`, SAROC-04, selagem `RiskClass`/`RiskApprover`/`RiskDecisionMode`, `DefaultHooksWithRisk`).
- **Controlo bidireccional out-of-band**: `packages/kernel/agent-runtime/control/steer_channel.go` (`SteerChannel`, `Authenticator`/`Emitter`, eventos `control.*` append-only, `Rebuild`, `HMACAuthenticator`) e `packages/kernel/agent-runtime/control/pause_resume.go` (`GracefulPause`, `StateGate`/`MachineGate`, `Correction`/`TailSteer`, ordem audit-first do `Resume`). Contrato documentado em `packages/kernel/agent-runtime/control/doc.go`.
- **Gate HITL concreto (Art. 14) + tiering policy-as-code + dual-control 4-eyes + não-repúdio assinado**: `packages/control-plane/governance/hitl/` (`channel.go` dual-control `Approver ≠ Requester`, `tiering.go` safe→run/gray→batch/danger→confirm com digest, `approval.go`/`approver.go` `SignedApproval` ed25519 + `ApproverRegistry` autoridade `approve:<classe>`, `metrics.go` `DefaultOverrideRateThreshold=0.40`).
- **Escada de autonomia (oversight proporcional, L3 = SA-ROC base)**: `packages/control-plane/governance/autonomy/` (`level.go` L0–L5, `oversight.go` nível × classe).
- **Ratificação assinada da auto-modificação** (predecessor de risco máximo, ADR-012): `packages/control-plane/governance/hitl/ratification.go` (`RatificationGate`, AOS-096).

## Referências

- Catálogo de ADRs: `_BRIEF.md` (linha 82); `specs/00_System_Spec.md` (linha 256).
- ADR-005 — Separação control/data-plane + taint (a base da separação canal-de-controlo/canal-de-dados e do taint que eleva o risco).
- ADR-011 — Policy-as-code + GDPR por desenho (o audit tamper-evident onde a decisão é selada).
- ADR-012 — SemVer + eval-gate para auto-modificação (a ratificação assinada composta em `ratification.go`).
- ADR-014 — Taxonomia de autonomia L0–L5 (oversight proporcional que compõe o tiering SA-ROC).
- Tickets: AOS-023 (canal steer/interrupt), AOS-074 (gate de risco SA-ROC), AOS-089 (autonomia L0–L5), AOS-095 (gate HITL concreto / Art. 14), AOS-096 (gate de ratificação); AOS-069 (taint), AOS-017 (máquina de estados durável), AOS-002/016 (Event Store / replay).
- EPIC-12 (AOS-119..128) — superfícies HITL/UX que **consomem** este ADR; EPIC-13 — operacionalização da camada de superfície (apex/PR-0 pendente).
