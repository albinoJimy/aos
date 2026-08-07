# Desenho — suspensão como estado de primeira classe e retoma explícita

> **Estado:** DESENHO (nenhum código escrito ao abrigo desta proposta) · **Data:** 2026-08-07
> **Substitui** a §4 de `AOS-021-desenho-retoma-aprovacao.md`, cuja mecânica de retoma assentava num pressuposto que a investigação invalidou.
> **Decisões do dono já tomadas:** retoma **explícita**; ao fim do TTL o run fica **retomável** (não terminal); documento **antes** do código.

## 1. O achado que invalidou o desenho anterior

Quando o loop escala uma tool call, `Runtime.Run` **retorna** (`loop.go:385-387`). A partir daí, em `service.go:396-461`:

```
Run() → Escalated=true
  └─ hostRun termina
       ├─ finish(rs)                 service.go:510-524
       │    ├─ assigner.Release()      ← LIBERTA O LEASE
       │    ├─ delete(s.runs, runID)   ← deixa de estar "em curso"
       │    └─ s.completed[runID] = rs ← ARQUIVA COMO TERMINADO
       └─ stateGates.Close(runID)    service.go:453  ← o gate desaparece
```

**Um run suspenso é, para o serviço, indistinguível de um run que acabou.** Verificado no código:

| Facto | Onde | Consequência |
|---|---|---|
| Reporta `status: "completed"` | `api.go:754` | Mentira operacional: está à espera de um humano |
| Liberta o lease | `service.go:511` | Outra réplica pode reclamar o RunID |
| Recusa re-submissão | `service.go:300-306` (`ErrRunAlreadyCompleted`) | **Não há como voltar a correr o run** |
| O Goal vive só em memória | argumento de `hostRun` | Perde-se quando a goroutine termina |

**Consequência prática:** o operador aprova, o grant é persistido correctamente… e **nada nunca o consome**. Um sweeper que apenas transitasse a máquina de estados seria contabilidade cosmética sobre um run operacionalmente morto.

### 1-bis. Dois achados de segunda ordem

- **O loop não sabe retomar a meio.** `loop.go:277` é `for turn := 1; turn <= maxTurns; turn++` — não há offset de arranque. O `durable.ResumePoint.NextTurn` existe, mas nenhum caminho do loop o consome.
- **O motor de replay VERIFICA, não retoma.** `ReplayEngine.Replay` compara `prompt_hash` para detectar divergência; não executa efeitos nem continua o run.

## 2. Princípio da solução

> Um run à espera de humano **não terminou** e **não deve segurar recursos**. Suspender = arquivar o que é preciso para retomar, libertar tudo o resto, e exigir uma **retoma explícita e re-autenticada**.

## 3. Desenho

### 3.1 Suspensão (terceiro balde no serviço)

`hostRun` passa a distinguir três desfechos: terminado, **suspenso**, falhado.

Com `res.Escalated`:
1. Persiste um **registo de retoma** com o `Goal` — **sem a `Credential`** (§3.3).
2. **Liberta o lease** e a goroutine de heartbeat: nenhum recurso fica preso durante minutos de latência humana.
3. Arquiva em `suspended` (não em `completed`), de modo que `Submit` do mesmo RunID continue a ser recusado como duplicado, mas o run seja **retomável**.

`GET /runs/{id}` passa a reportar `status: "waiting_on_human"` com os pendentes — a verdade, em vez de `"completed"`.

### 3.2 Retoma: replay-then-continue (sem tocar no kernel)

O loop começa sempre no turno 1 e não aceita offset. **Não é preciso mudá-lo.** A retoma é:

```
POST /runs/{id}/resume  { credential: <NHI fresco> }
  1. carrega o registo de retoma (Goal) + as capturas do run
  2. constrói um ModelClient HÍBRIDO:
        turnos 1..N  → devolve a ModelResponse REGISTADA (nunca chama o modelo)
        turno  N+1.. → modelo ao vivo
  3. Runtime.Run(ctx, goal-com-credencial-fresca)
```

Porque funciona sem alterar o loop:

- **Turnos 1..N-1**: as tool calls voltam a ser mediadas, mas o `StepLedger` verifica *already-applied* **antes de qualquer efeito** (chave `run_id:step_id`, com `step_id` determinista `<step>-tool-<idx>`). Devolvem o resultado memorizado **sem re-executar**. O tail reconstrói-se idêntico.
- **Turno N**: a call escalada **nunca foi aplicada** (a escalada pára *antes* do `cpActivity` — selado por `TestEscalation_NaoConfirmaAActivity`), logo não está no ledger. É re-mediada e, desta vez, o `ApprovalEvidenceSource` encontra o grant pela preview → o `ApprovalGate` verifica → o `TaintGate` deixa passar → **executa**.
- **Turno N+1 em diante**: modelo ao vivo, run continua normalmente.

Custo de re-materializar 1..N-1: montagem de prompt + hits no ledger. **Zero chamadas ao modelo, zero efeitos externos.**

### 3.3 Porque a credencial NÃO é persistida

Duas razões independentes, ambas decisivas:

1. **É um bearer token.** Guardá-lo no Event Store põe material de autenticação num log append-only que é lido pelo read-path soberano e replicado. Não se faz.
2. **Teria expirado.** O TTL de aprovação são 15 min; os TTL de classe NHI que o repo usa andam nos 5–15 min. Uma retoma automática com a credencial guardada falharia no hook de identidade **precisamente no caso normal** — o pior tipo de avaria: aparece só quando o humano demora.

Exigir credencial **fresca** no `POST /resume` resolve as duas de uma vez. O nó valida que o principal é o mesmo do registo de retoma antes de aceitar.

### 3.4 O sweeper (loop de serviço — decisão do dono)

Com a suspensão bem modelada, o sweeper fica pequeno e honesto:

- Varre periodicamente os pendentes com `CreatedAt` mais velho que o TTL.
- Marca-os **expirados** (facto append-only): deixam de aparecer ao operador.
- O run **permanece retomável** (decisão do dono). Quando for retomado, a call escalada já não encontra grant e é **negada** — o agente vê o marcador de negação (`60f5d64`) e pode seguir outro caminho.

O sweeper **não** re-executa nada. Retomar é sempre um acto explícito.

## 4. Riscos e como cada um fecha

| Risco | Fecho |
|---|---|
| **Dupla execução** dos efeitos dos turnos 1..N-1 na retoma | `StepLedger` verifica *already-applied* antes de qualquer efeito; `step_id` determinista. **Exige execução durável** — já imposto em produção por `ErrProductionNeedsDurableApproval` (`35ce50a`) |
| **A call aprovada é saltada** na retoma | A escalada não confirma a activity no cursor (`TestEscalation_NaoConfirmaAActivity`) |
| **O modelo produz outra call** na retoma | Não há chamada ao modelo nos turnos reproduzidos — vêm da captura |
| **Aprovação usada duas vezes** | Uso-único atómico pelo dedup do Event Store (`e8c2bb4`) |
| **Retoma por outro principal** | O `/resume` valida o principal contra o registo de retoma; e a cadeia completa volta a correr (identidade, PDP, escopo, egress) |
| **Credencial expirada / vazada** | Não é persistida; exige-se fresca no resume |
| **Run preso para sempre** | Sweeper expira o pendente ao fim do TTL; o run fica retomável, não pendurado |

## 5. O que NÃO muda

- A call retomada atravessa a **cadeia inteira**. A aprovação remove **um** obstáculo, não emite permit.
- O **taint continua untrusted** (selado na etapa 1).
- Sem execução durável, o four-eyes **aborta o arranque** em produção — a retoma seria impossível de fazer com segurança.

## 6. Superfície nova

| Onde | O quê |
|---|---|
| `cmd/aos/service.go` | Balde `suspended`; `hostRun` distingue o desfecho; sweeper periódico |
| `integration` | Registo de retoma durável (Goal sem credencial); `PendingApprovals.Expire`/`ListExpirable` (**já escritos**, por committar) |
| `cmd/aos/api.go` | `POST /runs/{id}/resume`; `status: "waiting_on_human"` |
| `cmd/aos` | ModelClient híbrido (capturado até N, vivo depois) |
| `deploy/node/README.md` | Env do período de varrimento (gate AOS-203) |

## 7. Ordem proposta

1. Registo de retoma durável + balde `suspended` + `status: "waiting_on_human"` *(sem retoma ainda — já corrige a mentira operacional)*
2. Sweeper no loop de serviço *(usa as peças de expiração já escritas)*
3. **ModelClient híbrido + `POST /resume`** — o item difícil
4. Teste de composição ponta a ponta: escalar → aprovar → retomar → executar **uma só vez**, com os efeitos anteriores **não** repetidos

## 8. O que peço antes de codificar

Nada — as três decisões (retoma explícita, run retomável, documento primeiro) estão tomadas. Este documento existe para ser **contestado** antes de eu escrever a etapa 1. Se a forma estiver certa, avanço pela ordem do §7.
