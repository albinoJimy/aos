# AOS-130 — Demonstrador single-process = superfície canónica de referência (evidência D1/D2/D3)

| Campo | Valor |
|---|---|
| Data | 2026-07-22 |
| Epic | EPIC-13 (Frontend), Fase 5 |
| Tipo | spike (realizado via EPIC-14/AOS-149/150/151) |
| Artefacto | `packages/cmd/aos-demo` (single-process, zero rede) |
| AC4 refutável | `packages/cmd/aos-demo/sufficiency_test.go` (`TestApexMinimalSufficiency`, AOS-151) |

---

## 1. O que é

O `cmd/aos-demo` é a **superfície canónica de referência** do AOS: um único processo, **zero
rede**, que compõe os pilares in-process e conduz o fluxo humano end-to-end **através das
interfaces reais** — não um mock descartável. É a lente frontend sobre o ápice mínimo de
PR-0.b, e a base contra a qual qualquer superfície (web ou outra) se mede.

## 2. Fluxo conduzido (via primitivas REAIS)

```
SPAWN            → máquina de estados durável ready → running (state.Machine)
PLAN-APPROVAL    → PlanGate.Approve real (plan-approval): Oracle L0 escala a tarefa
                   DANGER ao canal de confirmação, que aprova → VerdictApprove
(trajectória)    → um turno via o Agent Runtime (loop base) → turn.recorded no Event Store
RENDER           → surface-adapter.Renderer → DesktopComponent (data-only, sem API real)
APPROVE (4-eyes) → DualControlCollector.Authorize: dois aprovadores DISTINTOS (quorum
                   approver_1 != approver_2) — distinção ESTRUTURAL
STEER            → SteerChannel: correcção + pausa out-of-band, assinadas e relidas
```

Cada etapa usa o construtor de produção do pilar (`PlanGate`, `DualControlCollector`,
`Renderer`, `StateProjector`, `SteerChannel`), não um substituto. **Nenhum enforcement é
reimplementado** aqui (regra de ouro ADR-016: a superfície apresenta, não é autora).

**Fronteira de D4 (honesta):** o dual-control é ESTRUTURAL — duas identidades distintas
satisfazem o quorum, mas SEM attestation WebAuthn real (o 4-eyes atestado é AOS-138,
condicional a D4). O RM do ápice mínimo usa stubs neutros (enforcement real = PR-0.c).

## 3. Evidência para as decisões D1/D2/D3

| Decisão | Estado | Evidência do demo |
|---|---|---|
| **D2** — stack no-build (HTMX/`go:embed`) | **fixado** | O demo compõe e corre com **zero dependências externas** e sem passo de build além de `go build` (nenhum bundler/npm). A apresentação é **data-only** (`DesktopComponent` — estrutura, não markup imperativo), compatível com render server-side no-build. |
| **D3** — transporte SSE stdlib | **fixado** | O `StateProjector` já faz **fan-out in-process por push** (subscrição → callback) das reflexões de estado — exactamente a fonte que um BFF `net/http` faria ponte para SSE. Não é preciso gRPC-web/WebSocket/GraphQL (deps externas que cegam o SCA e re-fundem dados+controlo). |
| **D1(b)** — web SPA bespoke | **condicional** | O demo **é** a superfície de referência canónica **sem** um SPA web: conduz o fluxo completo via interfaces Go (`Renderer`, `plan-approval`, control-surface). Evidência de que um SPA bespoke **não é requisito** do núcleo — mantém-se condicional a utilizadores reais + TCO de um tier de ingress + dono de uma 2.ª supply-chain. |

## 4. AC4 — o apex mínimo (PR-0.b) chega? (refutável, não-vacuoso)

O `TestApexMinimalSufficiency` (AOS-151) classifica cada invariante de suficiência como
**PROVADA** (com assercção real) ou **DIFERIDA** (com seam nomeado), e o `classify()`
fail-closed **proíbe o vacuous pass** (uma invariante não-classificada avermelha o gate —
teste-veneno `TestSelftestApexSufficiencyReddensGate`, selftest.sh §J). Este demo **exercita**
a superfície que essa AC4 mede: a suficiência do ápice mínimo é uma conclusão *refutável*,
não uma afirmação por omissão. Conclusão corrente: o ápice mínimo **chega** para a superfície
de referência (spawn→plan-approval→steer→approve→trajectória correm in-process); o que fica
DIFERIDO é o enforcement de produção (PR-0.c, feito) e a identidade real (D4).

## 5. Conclusão

`cmd/aos-demo` satisfaz AOS-130: o demonstrador **corre** end-to-end zero-rede via primitivas
reais, **alimenta D1/D2/D3** com a evidência acima, e a **AC4 é refutável e não-vacuosa**. As
únicas lacunas são condicionais a decisões fora do código: o SPA web (D1(b)) e o 4-eyes
atestado + identidade real (D4). É a superfície canónica de referência ratificada.

---

*Referências: `packages/cmd/aos-demo/main.go` (fluxo), `sufficiency_test.go` (AC4, AOS-151),
`specs/EPIC-13_Frontend.md` (AOS-130, decisões D1–D7), `specs/EPIC-14_…md` (PR-0.b/PR-0.c),
ADR-016 (fronteira de confiança da UI). Sem segredos.*
