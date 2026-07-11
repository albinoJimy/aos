# platform/audit — Base de audit tamper-evident (AOS-011)

Registo de **responsabilização** (accountability) do AOS, encadeado por hash
criptográfico e **fisicamente separado** do Event Store (AOS-002) e dos
diagnósticos efémeros (ADR-010). Onde o Event Store responde *"o que aconteceu
no run"*, o audit responde *"quem autorizou e sob que política"*.

Cada decisão do PDP mediada pelo Reference Monitor (AOS-003) produz um registo
`append-only`, encadeado ao anterior pelo hash — tornando **qualquer adulteração
(mutação, remoção ou inserção) detectável** por re-verificação da cadeia.

> Estimativa **M**. Escopo estrito: hash-chain + `verify(from,to)` + checkpoints
> assinados + `AuditSink` do RM. WORM real e crypto-shredding/KMS são EPIC-08/09
> — aqui só **interfaces estáveis** (append-only + payload separado).

## Modelo

```
AuditRecord {
  AuditSeq      uint64        // ordem total monotónica por partição (gapless, começa em 1)
  Partition     string        // fronteira de encadeamento (run_id, tenant/board ou "global")
  Timestamp     time.Time     // observacional (a ordem é AuditSeq, não o tempo)
  Decision      allow|deny|escalate
  Principal     { NHIID, DelegationChain[] }   // só metadados de responsabilização
  Capability    string        // ex.: "fs:write:/reports/*"
  PolicyVersion string        // versão assinada da policy-as-code
  PayloadRef?   { ContentHash, KeyRef, SubjectID }  // payload pessoal NUNCA in-line
  PrevHash      []byte        // EntryHash do anterior (génese no primeiro)
  EntryHash     []byte        // selo desta entrada
}
```

## Hash-chain

```
EntryHash = SHA-256( PrevHash || serialização_canónica_determinística(conteúdo) )
```

- **conteúdo** = todos os campos EXCEPTO `EntryHash`. O `PrevHash` entra pela
  concatenação (não é re-serializado).
- **Serialização canónica**: ordem de campos fixa, inteiros big-endian de largura
  fixa, strings/blobs com *length-prefixing* (uvarint), separador de domínio
  versionado. Sem mapas nem ordenação dependente de runtime → **estável cross-SO**,
  mesmo conteúdo ⇒ mesmo `EntryHash`.
- **Génese por partição**: `PrevHash` do 1.º registo = `SHA-256("aos.audit.genesis:"+partition)`
  — determinística e distinta por partição.
- **Contígua (gapless)**: `audit_seq` começa em 1 e incrementa de 1 por partição.

## Verificação (100% de detecção)

```go
err := audit.Verify(ctx, store, partition, from, to)
```

Percorre a cadeia e detecta:

| Adulteração | Como é detectada | `TamperType` |
|---|---|---|
| **Mutação** de um campo | `EntryHash` recalculado ≠ armazenado | `mutation` |
| **Mutação** + rehash local | `PrevHash` do seguinte deixa de encadear | `chain_broken` |
| **Remoção** | gap ascendente em `audit_seq` / falta no fim | `removal` |
| **Inserção** | `audit_seq` duplicado/fora de ordem / elo forjado | `insertion` / `chain_broken` |

Devolve um `*VerifyError` (que desembrulha para `ErrTampered`) identificando a
partição, o `audit_seq` e o tipo.

## Checkpoints assinados (âncoras ed25519)

Verificação eficiente de grandes intervalos sem reprocessar desde a génese:

```go
signer, _ := audit.NewSigner(priv)          // chave privada FORA do repo (KMS/HSM)
cp, _ := signer.Seal(ctx, store, part, seq)  // sela EntryHash acumulado, assina
err := audit.VerifyFromCheckpoint(ctx, store, signer.Public(), cp, to)
```

`VerifyFromCheckpoint`:
1. valida a **assinatura** do checkpoint (raiz de confiança) — assinatura inválida
   ⇒ `ErrCheckpointSignature`;
2. confirma que o `EntryHash` do registo em `cp.AuditSeq` == `cp.EntryHash`
   (`ErrCheckpointAnchor` caso contrário);
3. verifica **só** `cp.AuditSeq+1 .. to`.

A chave privada nunca é committada; os testes usam pares efémeros.

## Integração com o Reference Monitor (AuditSink)

`MediationSink` implementa `referencemonitor.EventSink` — alteração **ZERO** ao RM:
o sink já recebe a decisão **final** pós-cadeia (permit/deny/escalate) com
principal, capability e `policy_version`.

```go
sink := audit.NewMediationSink(store)          // partição = run_id por omissão
mon := referencemonitor.New(referencemonitor.WithEventSink(sink), /* hooks */)
```

**Fail-closed**: no caminho de *permit*, se o `Append` falhar (auditoria
indisponível) o `RecordMediation` devolve erro e o RM degrada a decisão para
**Deny** — uma acção não-auditável não é permitida (ADR-002/010).

## WORM e crypto-shredding — interfaces estáveis (EPIC-08/09)

- **WORM**: `Store` é `append-only` por contrato (sem update/delete). `MemStore`
  é a referência in-memory (MVP); produção liga storage WORM real **por trás da
  mesma interface**. Fronteira estável, produtores/verificadores inalterados.
- **Crypto-shredding (GDPR Art. 17)**: o `EntryHash` sela o `ContentHash` do
  payload (ciphertext/metadados), **nunca** o plaintext. Destruir a chave por
  titular (`KeyRef`) num DSAR torna o payload ilegível **sem quebrar a cadeia** e
  sem mudar a cardinalidade do log. KMS/storage WORM reais não são implementados
  aqui — só as interfaces que os acomodam.

## Testes

```bash
export PATH="$HOME/scoop/apps/mingw/current/bin:$HOME/scoop/shims:$PATH"
export CGO_ENABLED=1   # -race exige o gcc do mingw
go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
go test -bench=. -benchmem -run=^$
```

Cobre: determinismo/sensibilidade da serialização canónica, génese por partição,
selagem da cadeia, detecção de **mutação/remoção/inserção** (100%), checkpoints
(âncora assinada, assinatura inválida rejeitada, âncora não-correspondente,
escopo cp+1..to), integração RM (decisão allow/deny + principal + capability +
policy_version chega ao audit; fail-closed) e *benchmarks* de append/verify.

## Zero dependências externas

Só stdlib (`crypto/sha256`, `crypto/ed25519`, `encoding/binary`). O RM é integrado
por `replace` local (o Event Store entra transitivamente via RM, também por
`replace` local) — build offline.
