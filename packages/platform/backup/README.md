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
- **O arranque verifica antes de confiar — o ÚLTIMO elo.** Assinatura do checkpoint, índice
  e região do registo, `EntryHash` recomputado (que **cobre** os `StreamHeads`) e head
  assinado == elo. Falha qualquer uma ⇒ `ErrResumeUnverifiable` e o exportador **não é
  construído**. Sem isto, um registo forjado com o cursor à frente faria o exportador
  **saltar eventos**. O `PrevHash` desse elo não é confrontado com o anterior — a cadeia
  inteira só se verifica no restauro. Um registo ilegível também é `ErrResumeUnverifiable`,
  e um destino que não responde aborta a construção: nenhum dos dois se lê como «virgem».
- **E o segmento desse elo tem de ABRIR com a KEK deste exportador.** (Desde o AOS-453 a
  custódia Vault do nó sela segmentos por envelope — ver §«Custódia da KEK do backup»; o vault
  de referência em memória continua a morrer com o processo.) A assinatura prova
  quem selou; não prova que esta custódia de chaves ainda decifra o que foi selado. Sem esta
  verificação, um processo com outra KEK (o vault de referência nasce vazio a cada arranque)
  retomava, o manifesto verificava, e o restauro falhava com `ErrSegmentTampered` — lido
  como adulteração. Segmento em falta ou que não abre ⇒ `ErrResumeUnverifiable`.
- **Buracos e prefixos expirados.** A retenção apaga os ciclos mais antigos primeiro: a
  sondagem procura o primeiro ciclo presente nas potências de dois (1, 2, 4, …), e um prefixo
  expirado nem se lê como destino virgem nem se continua — RECUSA-SE, porque uma cadeia
  incremental sem génese já não se restaura. Uma retenção finita exige snapshots completos
  periódicos, que este módulo não faz. Depois do último
  ciclo encontrado, procura-se ainda em last+2, last+4, …: um ciclo presente para lá de um
  em falta é um **buraco**, e retomar escreveria nele um elo divergente ⇒
  `ErrResumeUnverifiable`.
- **O log contra o cursor confere-se no CICLO, não no arranque.** Uma cadeia de outro Event
  Store (mesma chave, mesma região) ou de um log **rebobinado** tem o cursor à frente da
  fonte; o ciclo que o encontre — ou que encontre um stream do cursor que a fonte deixou de
  enumerar — devolve `ErrSourceBehindBackup` **sem escrever nada**. No arranque a mesma
  recusa impediria o nó de subir depois de um PITR ou de um DR, que é quando o log fica,
  legitimamente, atrás do cursor.
- **A colisão no registo de ciclo tem três causas.** Um registo que não verifica com a nossa
  chave é `ErrCycleRecordInvalid` (adulteração, lixo, ou outra chave); um que é EXACTAMENTE
  o que este exportador tentou escrever numa escrita ambígua (commit + timeout) é
  **adoptado**; qualquer outro autêntico é `ErrChainOwned` — outro escritor, mesmo que
  continue o nosso head (num destino condicional, todos os escritores reais o continuam).
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
- **Uma cadeia de outro log com a mesma chave e custódia, ou um log rebobinado que já voltou
  a crescer, com o head ≥ cursor, é continuada.** A história é outra e nada o acusa.
  Distingui-lo exigiria o elo selar a identidade do último evento de cada stream.
- **Uma janela retida sem nenhuma potência de dois lê-se como destino virgem**, e um buraco
  cujo resto da cadeia não caia em last+2^k escapa. Fechá-los exigiria uma âncora que não
  expire; a porta não tem listagem.

### O contrato que uma implementação DURÁVEL da porta tem de cumprir

A retoma assenta em duas respostas da porta, e um backend que as dê mal parte-a em silêncio.
A primeira é provada na construção — `NewExporter` escreve duas vezes `<região>/probe-conditional`
e recusa com `ErrDestinationNotConditional` se a segunda for aceite:

- **`Put` numa ref existente devolve `ErrImmutable`.** Não basta *reter* a versão antiga:
  em S3, um `PUT` sobre uma chave com Object Lock cria uma versão NOVA e devolve sucesso. A
  escrita tem de ser **condicional** (`If-None-Match: *` ou equivalente), senão dois
  exportadores sobre o mesmo destino deixam de colidir em `ErrChainOwned` e bifurcam.
- **`Get` de uma ref inexistente devolve `ErrNotFound`, e só nesse caso.** A sondagem do
  último ciclo lê `ErrNotFound` como «não existe» e qualquer outro erro como «o destino não
  respondeu» (aborta). Um backend que devolva `ErrNotFound` num erro de rede faria o
  exportador retomar de um ciclo antigo — e parar em `ErrChainOwned` no ciclo seguinte,
  a apontar dois escritores onde há um backend a mentir.

**Consequência para quem compõe:** o nó compõe o destino a partir de `AOS_BACKUP_DEST`
(AOS-453 F2), vazio por omissão. A primeira implementação durável da porta é a de disco local
(abaixo); S3 Object Lock / GCS retention / Azure immutable blob ligam-se por trás da mesma
interface, sem alterar o exportador nem o restaurador (S3 é a fase F4, não implementada).

### Destino em disco local — `FileImmutableStore` (AOS-453 F2)

`NewFileImmutableStore(raizAbsoluta, região)` sobre um directório que **já existe** (o adaptador
não o cria: um caminho mal escrito criaria em silêncio um destino vazio). Cumpre o contrato acima
só com a stdlib:

- **`Put` condicional e atómico:** escreve num temporário (`.tmp-*`) no mesmo directório, `fsync`,
  e publica com `os.Link(tmp, final)`. O link falha se o nome existir (`EEXIST` ⇒ `ErrImmutable`) —
  o equivalente local do `If-None-Match: *`; depois faz `fsync` do directório. Nunca há um objecto
  meio escrito à vista, e de N escritas concorrentes na mesma ref vence exactamente uma.
- **`Get` fiel:** `ErrNotFound` só para ficheiro inexistente; um objecto sem o cabeçalho
  `AOS-BACKUP-OBJ/1` (lixo, adulteração, outro escritor) é erro, e não «virgem».
- **Object-lock:** o instante de retenção viaja no cabeçalho do próprio objecto (um só ficheiro,
  publicado atomicamente com o blob); `Delete` antes dele é `ErrObjectLocked`. Objectos `0440`.

**O que NÃO protege, e fica dito:** a perda do host ou do disco (é uma cópia local — a cópia fora
do host é a F4), **root** e quem tenha escrita no directório (o write-once é disciplina da porta,
não um object-lock do armazenamento), e um restauro do volume para um ponto anterior.

## Custódia da KEK do backup (AOS-453)

Cada segmento é cifrado com uma DEK fresca, e a DEK é embrulhada pela KEK do titular
`aos.backup:<região>`. **Dois formatos, escolhidos pela custódia** (molde de
`audit.SealContent`/`OpenContent`):

| Custódia | Embrulho da DEK | Formato no destino |
|---|---|---|
| implementa `audit.KeyWrapper` (Vault Transit, HSM — *key-never-leaves*) | **dentro** da custódia (`WrapDEK`/`UnwrapDEK`); a KEK nunca entra no processo | `key_ref`, `wrapped_dek`, `ciphertext`, `nonce`, **`"wrap":"envelope"`** — sem `dek_nonce` |
| só `audit.KeyVault` (entrega a KEK crua de 32 bytes) | in-process, AES-GCM com `dek_nonce` | o de sempre, **byte a byte** (fixado por `TestAOS453_FormatoKEKCruaByteAByte`) |

O discriminador é **explícito** (`wrap`): o formato KEK-crua serializa sempre `key_ref` e
`dek_nonce`, pelo que a presença de `key_ref` (o discriminador do audit) não serviria. Um `wrap`
desconhecido é recusado.

**Na composição, e não ciclo a ciclo:** `NewExporter` prova a custódia **antes da retoma** —
uma volta `WrapDEK→UnwrapDEK` sob o titular do backup (envelope), ou `EnsureKey` a devolver 32
bytes (KEK-crua). Uma custódia que não entrega a KEK nem embrulha é `ErrKEKCustodyUnsupported`
(antes: `crypto/aes: invalid key size 0` a cada ciclo); uma que não responde ou recusa é
`ErrKEKCustodyUnavailable` — **não** «a KEK não é a que selou» (`ErrResumeUnverifiable`), que
mandaria abandonar um backup bom por causa de um Vault em baixo.

**Abertura:** o segmento tem de ser do titular pedido (`key_ref == KeyRefFor(aos.backup:<região>)`)
nos dois formatos. KEK destruída/ausente/indisponível ⇒ `ErrRestoreVerify` (não há como abrir, e
não é adulteração); conteúdo que não autentica ⇒ `ErrSegmentTampered`.

**Crypto-shred do titular do backup** (destruir a KEK `aos.backup:<região>` na custódia) torna
todos os segmentos da região irrecuperáveis: o restauro aborta com `ErrRestoreVerify` sem escrever
nada, e a retoma é recusada a nomear a KEK. No nó, essa KEK vive num **mount Transit próprio**
(`AOS_BACKUP_VAULT_TRANSIT_MOUNT`) onde a política do nó **não tem `delete`** — só o dono, com a
raiz, a destrói; e o DSAR **reserva o prefixo `aos.`** nos `subject_id` (um `/dsar/erase` de
`aos.backup:eu` é recusado). A garantia do Art. 17 continua nas KEKs dos titulares: o conteúdo
deles vai selado por titular dentro dos eventos do backup.

## Retenção finita e épocas

A retenção do destino é **finita** (`WithRetention`; no nó `AOS_BACKUP_RETENTION`, obrigatória).
Como a cadeia é **incremental**, um prefixo expirado torna-a irrestaurável e a retoma **recusa**
um destino sem o ciclo 1. A resposta é a **época**: um destino novo (no disco, um subdirectório
novo de `AOS_BACKUP_DEST`) começa uma génese nova — o primeiro ciclo exporta o log inteiro, que é
o snapshot completo. Regra operacional: **rodar de época antes de a anterior expirar**, com folga
de pelo menos um ciclo verificado da nova; a época anterior fica restaurável até ao fim do seu
object-lock e só então se remove (`Delete` respeita o lock). O módulo não roda épocas sozinho —
é um passo de operação (`deploy/server/README.md` §Backup imutável).

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
