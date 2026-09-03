# platform/dr — DR por replay determinístico (AOS-102)

Plano de **Disaster Recovery** do AOS por **replay determinístico** com **RPO/RTO
definidos** e validados por **game day**. O módulo **compõe** as peças já Done — **sem
reimplementar** restauro, replay, resume, verificação, idempotência ou soberania.

- **Restaura o log** (via AOS-101) até ao último evento íntegro;
- **verifica** o backup (manifesto hash-chain) e o **audit WORM** (hash-chain) antes de
  restabelecer;
- **prova a fidelidade** do replay (100% dos passos, `Fidelity==1.0`, sem divergência);
- **retoma** *resume-from-step* (worker AOS-099) com o `StepLedger` a garantir **0
  efeitos duplicados**;
- **dentro da fronteira de soberania** (o failover **não** cruza a fronteira, ADR-011).

## A tese de DR do AOS

Como o Event Store é a fonte de verdade **append-only** (ADR-007) e a execução é durável
ao nível do passo (ADR-001), a recuperação primária é o **replay determinístico** a
partir do log, não a restauração de estado mutável. **Restaura-se o log; o estado
reconstrói-se *resume-from-step*.** A fidelidade exige que todos os inputs
não-determinísticos tenham sido capturados por trajectória (model-id/params/seed/
prompt_hash — ADR-010/AOS-016).

## Peças compostas (nada reimplementado)

| Etapa | Peça Done | Garantia reutilizada |
|---|---|---|
| Restaurar log + PITR | `platform/backup` (AOS-101) | `Restorer.RestoreTo` — `VerifyManifest` aborta em adulteração antes de escrever |
| Verificar WORM | `platform/audit` (AOS-072/083) | `VerifyFromCheckpointAtHead` — rejeita *stale*/rollback |
| Provar fidelidade | `agent-runtime/replay` (AOS-016) | `Replay` — 100% dos passos, zero efeitos externos |
| Retomar *resume-from-step* | worker AOS-099 + `durable` (AOS-014/015) | `StepLedger` — 0 efeitos duplicados (chave f(run_id,step_id)) |
| Store de DR na fronteira | `substrate/eventstore` (AOS-100) | `WithSovereigntyBoard` — recusa *cross-border* por construção |
| Medir RPO | `Exporter.RPOWindow`/`WithinRPO` (AOS-101) | RPO já medido |

## Layering (ADR-011)

`platform/dr` **não** importa `control-plane/governance/*` (seria um up-import ilegal
`platform→control-plane`). A resolução **board→região** é **injectada**
(`BoundaryResolver`); a soberania é **reforçada** pelo próprio guard do `eventstore`
(`WithSovereigntyBoard`) mais a **asserção** do orquestrador de que `região(Store de
DR)==região-alvo`.

## Uso

```go
rec, _ := dr.NewRecoverer(resolver, storeFactory, restorer, dr.WithClock(clock))
ev, err := rec.Recover(ctx, dr.Recovery{
    Board: "board-eu", RunID: "run-42",
    Manifest: exp.Manifest(), Backup: exp.Checkpoint(), ExpectedHead: 0,
    Spec:  spec,                         // inputs determinísticos do replay
    Audit: dr.AuditCheck{Store: worm, Public: pub, Checkpoint: cp, ExpectedHead: h, To: h},
    Resume: resumeFromStep,              // worker.Adopt/Run sobre o Store restaurado
})
// ev: RestoreEvidence + fidelidade + retoma + RTO, dentro da fronteira.

gd, _ := dr.NewGameDay(rec, exp, time.Minute /*RPO*/, 30*time.Minute /*RTO*/, 24*time.Hour /*cadência*/,
    dr.WithEvidencePersister(save))
gde, err := gd.Run(ctx, recovery)       // mede RPO/RTO, persiste a evidência do exercício
```

---

# RUNBOOK — Recuperação de desastre por replay (liga a AOS-106)

> **Pré-condições operacionais.** Backup imutável AOS-101 a correr (exportação
> contínua `Periodicity() <= 1 min`); audit WORM AOS-072/083 disponível na região;
> chaves públicas (ed25519) do backup e do WORM distribuídas aos verificadores; a
> atribuição **board→região** disponível (fonte no plano de governação).

**Alvos (proposta, ADR-001/007/011):** **RPO ≤ 1 min** · **RTO ≤ 30 min** · **failover
NUNCA cruza a fronteira de soberania**.

### 1. Declarar o desastre e isolar

1. Confirmar a perda (SLI *Integridade do audit WORM* / *Fidelidade de replay* /
   indisponibilidade do cluster).
2. **NÃO** escrever em produção. O DR opera sempre sobre um **Event Store de DR limpo e
   descartável** — nunca sobre o Store de produção degradado.

### 2. Resolver a fronteira-alvo (soberania — AC6)

- Resolver `board → região` pela fonte de governação. **Fronteira desconhecida ⇒
  aborta** (`ErrUnknownBoard`). O failover fica **preso** à fronteira: o Store de DR é
  construído `WithSovereigntyBoard(board, região)` e recusa *cross-border* por
  construção; o orquestrador **asserta** `região(Store)==região-alvo`.

### 3. Restaurar o log verificado (AOS-101 — AC1)

- `Restorer.RestoreTo(manifesto, checkpoint, expectedHead, target, StoreDeDR)`.
- O `VerifyManifest` **embutido** aborta **antes de escrever** em adulteração:
  `ErrSegmentTampered` (blob), `ErrChainBroken` (manifesto), `ErrCheckpointStale`
  (rollback). `target` fixa o **instante de PITR** por stream (último evento íntegro).

### 4. Verificar o audit WORM (AC5)

- `audit.VerifyFromCheckpointAtHead(worm, pub, checkpoint, expectedHead, to)` **antes**
  de retomar. Rejeita *stale*/rollback/adulteração. **Falha ⇒ serviço não
  restabelecido.**

### 5. Provar a fidelidade do replay (AC3)

- `replay.Replay(runID, Spec)` — só-leitura, **zero efeitos**. Exigir
  `Fidelity==1.0 && Divergence==nil`. Uma divergência (`prompt_hash`/`model`/
  `assembly_version`/`step_id`) sinaliza *drift* de código face à captura: **aborta**
  (`ErrReplayInfidelity`) — recuperar do commit correspondente à trajectória.

### 6. Retomar *resume-from-step* (AC1/AC4)

- Worker AOS-099 (`Adopt`/`Run`) sobre o log restaurado: `ledger.Rebuild` +
  `resumer.Resume` saltam os passos confirmados; o passo de fronteira re-executado é
  **deduplicado** pelo `StepLedger` (chave f(run_id,step_id)) — **0 efeitos
  duplicados**. Medir e exigir `DuplicatedEffects==0` (senão **aborta**).

### 7. Restabelecer e registar evidência

- Só com **todas** as etapas verdes: WORM verificado, `Fidelity==1.0`, 0 duplicados,
  fronteira respeitada, **RPO/RTO dentro dos alvos**. Persistir a `GameDayEvidence`
  combinada.

### 8. Game day periódico (AC7)

- Agendar `GameDay.Run` na cadência (`Periodicity()`), contra um Store descartável.
  Revalida RPO/RTO e a fidelidade; persiste a evidência do **último exercício**.
  `GameDay.Due(now)` sinaliza um exercício em atraso.

### Falhas → abort (fail-closed)

| Sinal | Erro | Acção |
|---|---|---|
| Segmento/manifesto adulterado | `ErrSegmentTampered`/`ErrChainBroken` | Abortar; usar backup íntegro anterior |
| Checkpoint (backup/WORM) rollback | `ErrCheckpointStale` | Abortar; investigar truncatura de tail |
| Replay diverge | `ErrReplayInfidelity` | Abortar; recuperar do commit da trajectória |
| Captura incompleta | `ErrIncompleteCapture` | Abortar; trajectória não reproduzível |
| Failover *cross-border* | `ErrSovereigntyViolation`/`ErrRegionMismatch` | Abortar; recuperar dentro da fronteira |
| Efeitos duplicados | `ErrDuplicatedEffects` | Abortar; investigar idempotência do downstream |

Em **todos** os casos: o serviço **não** é dado por restabelecido; produção intacta.
