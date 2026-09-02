# platform/backup — Backup imutável + PITR do Event Store (AOS-101)

Backup **imutável e contínuo** do Event Store e **Point-In-Time Recovery** (PITR)
validado por restauro de teste, verificação por hash-chain e conformidade de
soberania. Modelo de referência **zero-dep/offline** (EPIC-10; ADR-007/010/011/006).

## Porquê

A replicação por quórum (AOS-100) protege contra a falha de um nó, mas não contra
corrupção lógica, apagamento acidental ou desastre regional. Este módulo acrescenta
um segundo eixo de durabilidade — um backup imutável, cifrado, tamper-evident e
soberano — do qual se pode fazer PITR até ao último evento íntegro.

## Arquitectura

```
Event Store (AOS-100)                     platform/backup
┌───────────────────────┐   snapshot   ┌──────────────────────────────────────┐
│ BackupSource (porta)  │─────────────▶│ Exporter                             │
│  Streams()            │  envelope    │  · cifra em repouso (AES-256-GCM,     │
│  StreamHead()         │  intacto     │    KEK do audit.KeyVault)            │
│  SnapshotStream()     │              │  · segmento IMUTÁVEL → ImmutableStore │
│  Region()/Board()     │              │  · manifesto hash-chain (SHA-256)     │
└───────────────────────┘              │  · checkpoint assinado (ed25519)      │
┌───────────────────────┐   ingest     │                                      │
│ RestoreSink (porta)   │◀─────────────│ Restorer                             │
│  IngestStream()       │  envelope    │  · verifica hash-chain (tamper-evid.) │
│  (preserva envelope)  │  preservado  │  · decifra + PITR por seq-alvo        │
└───────────────────────┘              │  · evidência do restauro (AC6)        │
                                       └──────────────────────────────────────┘
```

- **Envelope preservado.** O snapshot exporta os `Event` crus (EventID/Ts/Seq
  originais); o restauro reinsere-os **sem reatribuir** o envelope (ao contrário de
  `Append`). As primitivas zero-dep vivem em `substrate/eventstore` (métodos do tipo
  concreto `*Store`, **fora** da interface `EventStore` append-only travada).
- **Cifra em repouso (ADR-006).** Nenhum plaintext de payload chega ao
  `ImmutableStore`: cada segmento é um envelope AES-256-GCM (DEK por segmento
  embrulhada pela KEK do titular do backup, do `audit.KeyVault`). Sem segredos no
  código; a chave privada de assinatura vive fora do repositório.
- **Imutabilidade (WORM).** `ImmutableStore.Put` é **write-once** (segunda escrita à
  mesma ref ⇒ `ErrImmutable`), com object-lock por período (reutiliza
  `audit.RetentionPolicy`) e legal hold (`audit.LegalHold`).
- **Tamper-evidence (ADR-010).** O Event Store não tem cadeia nativa: o manifesto
  constrói uma hash-chain SHA-256 sobre os segmentos (`EntryHash = SHA-256(PrevHash
  || conteúdo canónico)`), com o head selado num checkpoint ed25519. Uma adulteração
  de um segmento (blob) ou de qualquer campo do manifesto é **detectada** na
  verificação; o rollback de checkpoint é fail-closed (`ErrCheckpointStale`).
- **Soberania (ADR-011), fail-closed.** O destino tem uma região; se cruza a
  fronteira do board (região diferente, ausente ou desconhecida) o backup é
  **recusado** (`ErrSovereigntyViolation`). Backups e cópias **nunca** cruzam a
  fronteira regional.
- **RPO (AC4).** Sob um ciclo de exportação a cada `Periodicity()`, a janela efectiva
  de perda mantém-se `<= Periodicity()`; com periodicidade `<= 1 min` cumpre-se o
  RPO-alvo dentro de região.

## Uso (esboço)

```go
src, _ := eventstore.New(eventstore.WithReplicas(3),
    eventstore.WithSovereigntyBoard("board-eu", "eu-west"))
dst := backup.NewInMemoryImmutableStore("eu-west") // produção: S3 Object Lock, etc.
signer, _ := backup.NewEd25519Signer(priv)          // chave privada via KMS/Vault

exp, _ := backup.NewExporter(src, dst, signer,
    backup.WithPeriodicity(30*time.Second),
    backup.WithRetention(policy, audit.ClassAudit))

// Ciclo contínuo/incremental. QUEM O CORRE não é este módulo: o agendador vive no
// loop de serviço do nó (packages/cmd/aos/backup_scheduler.go), com a cadência lida
// de exp.Periodicity() — fonte única, para o RPO anunciado ser o RPO ligado.
exp.Export(ctx)

// PITR até um seq-alvo por stream, verificado por hash-chain. A cadeia vem dos registos de
// ciclo do destino — NÃO de exp.Manifest(), que num exportador retomado é só o sufixo deste
// processo (e seria recusado com ErrChainBroken).
rst, _ := backup.NewRestorer(dst, exp.Vault(), exp.Public())
manifesto, checkpoint, _ := rst.LoadManifest()
ev, _ := rst.RestoreTo(ctx, manifesto, checkpoint,
    knownHead, map[string]uint64{"run-a": 42}, freshStore)
// ev.Verified == true; ev é a evidência do restauro (AC6).
```

## Retoma de manifesto — o exportador atravessa a vida do processo

`NewExporter` **retoma** a cadeia que já exista no destino. A cada ciclo escreve-se, no
*mesmo* `ImmutableStore`, um segundo objecto pequeno com o elo e o checkpoint que o sela:

```
<região>/cycle-%08d                      ⇒ { entry, checkpoint }   (indexado)
<região>/seg-%08d-<16 hex do conteúdo>   ⇒ segmento cifrado        (endereçado por conteúdo)
```

- **Só o último elo é preciso para retomar.** O `PrevHash` do próximo elo, o cursor
  incremental e o próximo índice vivem todos no último `SegmentEntry`. Persistir o
  manifesto inteiro por ciclo seria O(n²) para reconstruir uma coisa de que o arranque só
  precisa da última linha.
- **A âncora é imutável, e não um ponteiro.** Um `latest-manifest` mutável seria o vector
  de rollback que `ErrCheckpointStale` existe para negar. Write-once sob object-lock não
  pode ser revertido, porque não pode ser apagado dentro da retenção.
- **O arranque verifica antes de confiar.** Assinatura do checkpoint, `EntryHash`
  recomputado (que **cobre** os `StreamHeads`) e head assinado == elo. Falha qualquer uma
  ⇒ `ErrResumeUnverifiable` e o exportador **não é construído**. Sem isto, um registo
  forjado com o cursor à frente faria o exportador **saltar eventos** — um buraco no
  backup que nada acusaria até ao dia do restauro.
- **A ref do segmento é endereçada por conteúdo**, e é o que torna a colisão *impossível*
  em vez de evitada: um ciclo que morra entre as duas escritas deixa um órfão retido e
  não referenciado, e a re-tentativa avança. A ref do *registo de ciclo* é indexada de
  propósito — é aí que dois exportadores se encontram, com `ErrChainOwned`.

`Restorer.LoadManifest()` reconstrói a cadeia completa a partir dos registos de ciclo:
segmentos duráveis passam a ser restauráveis sem um manifesto guardado à parte.

### O que fica de fora, deliberadamente

- **O arranque não percorre a cadeia toda.** Verifica um elo em O(1) e descobre o último
  ciclo em O(log N) gets de objectos pequenos. A verificação integral vive onde pertence:
  em `VerifyManifest`, fail-closed, antes de um restauro.
- **`Exporter.Manifest()` devolve só os elos deste processo.** Num exportador retomado é
  um *sufixo*, e um sufixo não é restaurável — mas falha **alto** (`ErrChainBroken` em
  `len(Segments) != cp.Cycle`), não em silêncio. Para restaurar, use `LoadManifest`.
- **`expectedHead` continua a vir de fora.** É a âncora anti-rollback, e uma âncora lida
  do mesmo sítio que se está a verificar não ancora nada.

**Consequência para quem compõe:** o nó continua a exigir o `ImmutableStore` **injectado**
(`Config.BackupDestination`) — mas pela razão ordinária, não por o destino durável ser
inutilizável. Este repositório não traz nenhuma **implementação** durável da porta: só a de
referência, em memória. S3 Object Lock / GCS retention / Azure immutable blob ligam-se por
trás da mesma interface, sem alterar o exportador nem o restaurador.

## Runbook — Restauro / PITR do Event Store (esboço, liga a AOS-106)

**Sinal.** Corrupção lógica, apagamento acidental, ou desastre regional detectados
por: quebra da hash-chain do audit WORM, perda de quórum irreversível, ou decisão de
DR (game day). Alerta associado: "Integridade do audit WORM" / "Perda de quórum".

**Pré-condições.** Acesso ao `ImmutableStore` da **região do board afectado** (nunca
cross-border), à KEK do backup no KMS/Vault, e à chave pública de verificação dos
checkpoints. Identificar o **instante-alvo** de recuperação (o último evento íntegro
antes do incidente) e resolvê-lo a um **seq-alvo por stream**.

**Passos.**
0. **Reconstruir a cadeia** a partir do destino: `manifest, checkpoint, err :=
   Restorer.LoadManifest()`. Lê os registos de ciclo por ordem. Não é preciso ter guardado
   o manifesto à parte; o `knownHead` (anti-rollback), esse, tem de vir de fora do backup.
1. **Verificar o backup** antes de tocar em produção: `Restorer.VerifyManifest(manifest,
   checkpoint, knownHead)`. Confirma a assinatura do checkpoint, a frescura
   (anti-rollback) e a hash-chain segmento-a-segmento. Um `ErrSegmentTampered` /
   `ErrChainBroken` **aborta** o DR — escalar para segurança (o backup não é fiável).
2. **Provisionar um Event Store limpo** na região de soberania correcta (mesma
   fronteira; `eventstore.New(..., WithSovereigntyBoard(board, região))`).
3. **PITR:** `Restorer.RestoreTo(ctx, manifest, checkpoint, knownHead, alvoPorStream,
   novoStore)`. O restauro re-verifica a cadeia, decifra os segmentos e reinsere os
   eventos **com o envelope preservado** até ao seq-alvo. Fail-closed: se a
   verificação falhar, nada é escrito.
4. **Registar a evidência** (`RestoreEvidence`: timestamp, head por stream, verdict,
   ciclo) no registo de conformidade — é a prova do "restauro testado" (AC6).
5. **Retoma por replay** (fora do âmbito deste módulo — **AOS-102**): a partir do log
   restaurado, o Agent Runtime faz o replay determinístico *resume-from-step*.

**Teste periódico (AC6).** Correr o passo 1–4 contra um Event Store descartável num
calendário (game day / cron), guardando a `RestoreEvidence` de cada exercício. A
ausência de evidência recente é, ela própria, um alerta.

**Rollback.** O restauro é para um Store novo; não muta o backup (imutável) nem o
Store original. Abortar é seguro — repetir com outro seq-alvo.

> Detalhe operacional completo (papéis, escalonamento, SLAs de RTO) em **AOS-106**.
