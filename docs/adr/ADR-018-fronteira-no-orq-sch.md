# ADR-018 — Fronteira nó↔ORQ/SCH: o loop de serviço como fonte única de verdade do ciclo de vida

| Campo | Valor |
|---|---|
| **ADR** | 018 |
| **Título** | Fronteira nó↔Orquestrador/Escalonador (v1 single-host): o loop de serviço do nó é a fonte única de verdade do ciclo de vida |
| **Estado** | Aceite |
| **Data** | 2026-07-23 |
| **Deciders** | Equipa AOS |
| **Contexto-fonte** | `specs/EPIC-15_*` (Revisão pós-painel, ponto **E8**); `specs/00_AOS_Carta.md §7` (emenda 1.2 — v1 single-host/sem-HA como non-goal DATADO; distribuído = EPIC-10); `packages/cmd/aos/service.go` (NodeService, ciclo de vida por lease — AOS-164a); `packages/control-plane/orchestrator/contract/ports.go` (portas EPIC-03 `Orchestrator`/`Scheduler` — AOS-012); ADR-015 (execução durável — contrato próprio); ADR-007 (Event Store como fonte de verdade) |
| ADRs relacionados | ADR-007 (uma só fonte de verdade), ADR-015 (durable execution própria), ADR-017 (fronteira de supply-chain do nó) |
| Supersede | — |

> Este ADR **REGISTA** (não re-litiga) a coerência de uma decisão de forma que a Carta já
> FIXA. A forma do produto — um nó `aos` deployável, single-host na v1 — não é reaberta
> aqui (§7, emenda 1.2). O que se sela é: **quem é dono do ciclo de vida de um run**, para
> não haver **duas fontes de verdade**.

---

## 1. Contexto

O EPIC-03 definiu duas portas estáveis do plano de controlo (`AOS-012`,
`packages/control-plane/orchestrator/contract/ports.go`):

- **`Orchestrator`** — `Submit(ctx, Goal) (RunID, error)`: decompõe um objectivo num DAG de
  tarefas e emite `run.created` + `task.ready`.
- **`Scheduler`** — `Start(ctx) error; Stop()`: consome `task.ready` e despacha cada tool
  call **SEMPRE via Reference Monitor**, conduzindo a máquina de estados
  `ready→running→complete|failed`. O ponto de extensão documentado do EPIC-03 acrescenta
  **leases/fencing/heartbeat** *por trás desta porta*.

O EPIC-15 graduou o nó para um **loop de serviço long-running**
(`packages/cmd/aos/service.go`, `NodeService`, AOS-164a) que hospeda N runs, isola falhas
por-run e encerra graciosamente. Nesse loop, a **posse de um run é por LEASE DURÁVEL**
(`durable.LeaseManager` sobre o Event Store do nó, via `worker.Assigner`), com token
monotónico e liveness por TTL (AOS-018) — reutilizando o mecanismo, sem reinventar o
escalonador.

Daqui nasce a pergunta do painel (E8): **há duas fontes de verdade do ciclo de vida?** Se o
nó corresse *também* um `Scheduler` do EPIC-03 (que já tem, por trás da sua porta,
leases/heartbeat/estado) **em paralelo** com o loop por-lease, duas autoridades disputariam
o ciclo de vida do mesmo run — a receita para dupla-execução, estado divergente e a "segunda
fonte de verdade" que o ADR-007 proíbe para o log e que o ADR-015 recusou para a durabilidade.

## 2. Decisão

**Na v1 single-host (Carta §7, emenda 1.2), o loop de serviço do nó (`NodeService`, posse
por lease durável) é a FONTE ÚNICA DE VERDADE do ciclo de vida de um run.** As portas
EPIC-03 `Orchestrator` e `Scheduler` são **consumidas DENTRO da execução de um run** — como
colaboradores da *decomposição* e do *despacho mediado pelo RM* — e **NUNCA como uma
autoridade concorrente do ciclo de vida**.

Concretização na topologia single-process:

1. **Um só dono do ciclo de vida.** Admissão, posse, heartbeat, cancelamento cooperativo e
   término de um run pertencem ao `NodeService`. O ciclo de vida é governado pelo **lease
   durável** (claim/renew/expire com token monotónico), não por um `Scheduler.Start` a
   correr um segundo loop de consumo de `task.ready`.
2. **ORQ/SCH consumidos *dentro* de um run.** A responsabilidade da porta `Orchestrator`
   (decompor `goal→DAG`) e da porta `Scheduler` (despachar `task.ready` **via Reference
   Monitor**, honrando a máquina de estados) exerce-se **sob o run que o loop já possui** —
   o despacho mediado pelo RM é o *no-bypass* de sempre (ADR-002/AOS-021), agora subordinado
   à posse por lease. O loop **substitui** o *arranque autónomo* do `Scheduler` (não há um
   `Scheduler.Start` a arrancar um segundo dono); **consome** a *função* de decompor+despachar
   dentro do run que possui. Não há dois donos do mesmo run.
3. **Uma só liveness.** A liveness é por **lease/TTL** (nunca por PID). O `Scheduler` do
   EPIC-03 não corre um heartbeat concorrente sobre o mesmo run — o único renovador de posse
   é o `heartbeat()` do loop, que cancela o run cooperativamente se perder o lease
   (`ErrLeaseSuperseded`/`ErrLeaseExpired`), fechando a janela de dupla-execução.
4. **Uma só fonte durável.** Coerente com ADR-007/ADR-015: turnos, sinais de controlo e
   registos de lease vivem todos no **mesmo Event Store durável** (AOS-170). Não se
   introduz um segundo log/estado de escalonamento paralelo.

### O que muda no distribuído (DEFERIDO — AOS-098, AOS-099, AOS-100)

A v1 single-host/sem-HA é um **non-goal DATADO** (Carta §7, emenda 1.2), não uma regressão.
Num deployment distribuído (EPIC-10), a decomposição (ORQ) e o despacho (SCH) podem tornar-se
**componentes autónomos** que coordenam *através* do Event Store replicado (a fonte de verdade
partilhada). Mesmo aí, a invariante **não** muda: **um só dono por run**, arbitrado pelo
**lease durável de token monotónico** — o mecanismo de fencing que já existe é precisamente o
que permite mover a autoridade entre processos sem dupla-execução. O que a v1 fixa é que, no
único processo, **não há duas autoridades**; o que o EPIC-10 acrescenta é **coordenação entre
processos** sob a mesma disciplina de lease, não uma segunda fonte de verdade.

## 3. Alternativas consideradas

- **Correr um `Scheduler` do EPIC-03 em paralelo com o loop por-lease.** Rejeitada: duas
  autoridades de ciclo de vida sobre o mesmo run = duas fontes de verdade (dupla-execução,
  estado divergente). Contradiz ADR-007/ADR-015.
- **Substituir o loop por-lease pelo `Scheduler` como dono único.** Rejeitada para a v1: o
  `Scheduler` autónomo do EPIC-03 pressupõe um barramento/consumo durável que, em
  single-process, reintroduziria um segundo mecanismo de posse a par do lease já usado pelo
  loop (AOS-164a). O loop por-lease é a materialização mínima e testada da posse (AOS-018);
  a porta `Scheduler` é consumida por baixo, não promovida a dono.
- **Não registar nada (deixar implícito).** Rejeitada: o painel (E8) exige o registo
  explícito para evitar re-litígio e para o próximo implementador não recompor um segundo
  escalonador por engano — daí o ADR **mais** o guarda de compilação.

## 4. Consequências

- **Positivas:** uma só autoridade de ciclo de vida por run; sem dupla-execução por dois
  donos; coerência com ADR-007 (um log), ADR-015 (uma durabilidade) e AOS-018 (uma
  liveness). A fronteira fica **verificável** (ver §5), não só documentada.
- **Custos/risco residual:** a decomposição real do DAG (ORQ) e o despacho multi-tarefa (SCH)
  ricos ficam por *cablar dentro do run* em tickets próprios; a v1 corre a forma mínima (o
  run conclui via runtime), declarada — não fingida. O guarda de importação (§5) recusa a
  co-residência acidental dos módulos ORQ/SCH; consumir uma porta *dentro* de um run, quando
  chegar, far-se-á num colaborador dedicado e obrigará a rever conscientemente a fronteira
  (o teste sinaliza a mudança).

## 5. Conformidade / Enforcement

- **Guarda de compilação/importação** (`packages/cmd/aos/boundary_orq_sch_test.go`), em
  **duas camadas** que juntas cobrem o PROCESSO do nó (não só os ficheiros deste comando):
  - `TestBoundary_NodeDoesNotImportConcurrentOrchestratorOrScheduler` — o código de PRODUÇÃO
    do nó (ficheiros `.go` não-teste) **não importa DIRECTAMENTE** os módulos
    `github.com/aos-ref/control-plane/orchestrator` nem `.../scheduler`.
  - `TestBoundary_NodeBuildGraphExcludesConcurrentOrchestratorOrScheduler` — o **grafo de
    build efectivo** do binário do nó (`go list -deps .`, o fecho **transitivo**, incluindo a
    raiz de composição `integration` que o comando requer) **não contém** nenhum pacote sob
    esses módulos. Fecha o ponto-cego de uma co-residência arrastada *transitivamente* (p.ex.
    via `integration`), tornando o guarda tão abrangente quanto esta invariante do processo.
  Confirmado também pelo `go.mod` do nó (e da `integration`), que não requerem esses módulos.
- **Doc do loop** (`packages/cmd/aos/service.go`): a fronteira de escopo do `NodeService`
  declara a posse por lease como o mecanismo de ciclo de vida; este ADR é a sua justificação
  registada.

## 5-bis. Limite de veracidade declarado (AOS-222): o fencing de ESCRITAS não está composto na v1

Este ADR também **fixa o limite honesto** do mecanismo anti-duplo-efeito do loop de serviço,
para que nem o código nem a documentação **anunciem uma barreira que não existe** naquele
caminho (achado #10 da auditoria adversarial do lease/posse).

**O que a v1 usa (real, composto, testado):**

1. **Posse por lease de CAS atómico.** `worker.Assigner` sobre `durable.LeaseManager` arbitra
   um só dono por run com token **estritamente monotónico** (AOS-018). Um run detido por outra
   réplica **não é roubado** (`TryAcquire` ⇒ `(_, false, nil)`).
2. **Cancelamento cooperativo.** Se o `heartbeat()` do loop perder a partição
   (`ErrLeaseSuperseded`/`ErrLeaseExpired`) ele **cancela o run** — que pára na **fronteira de
   fim-de-turno**, nunca a meio de uma escrita durável (§2.3, ADR-015).
3. **Idempotência do step-ledger.** A dedup por **`(RunID, StepID)`** no replay do WAL
   (AOS-180) garante que um efeito já aplicado **não** re-materializa numa 2ª execução do mesmo
   run.

**O que a v1 NÃO compõe (declarado, não fingido):** o `durable.FencedAppender` — o *enforcement
opt-in* que rejeitaria, no ponto de escrita, o `Append` de um detentor cujo token já foi
superado (`ErrStaleFencingToken`). Ele **existe** no kernel (`agent-runtime/durable`,
`agent-runtime/worker`) e é exercitado nos testes de integração/DR, mas o **nó não o cabla** no
caminho de escrita de `Runtime.Run`: não há chamador de produção de `durable.NewFencedAppender`
nem de `worker.NewWorker` no processo do nó. Por isso, **nenhum log ou comentário do caminho de
posse (`service.go`: `hostRun`/`heartbeat`) deve afirmar que um "fencing" barra as escritas
tardias** — a barreira real é a soma (1)+(2)+(3) acima.

**Eixo nomeado (para quando for composto):** cablar o `FencedAppender` no nó exige **threading
do fencing token do lease** (`rs.lease.Token`, já detido em `hostRun`) **até ao ponto de escrita
de efeito de `Runtime.Run`**, para que TODA a escrita de progresso passe pelo appender fenced
(padrão `Claim → token → FencedAppender.Append`, como o `worker.Worker` já faz). Enquanto esse
threading não existir, a defesa-em-profundidade do fencing de escritas fica **por compor** — não
anunciada. (A máquina de estados durável do steer (AOS-218) **já** usa o token do lease em
`ready→running`; isso é distinto do fencing das escritas de *efeito* do run.)

**Enforcement:** uma **guarda de veracidade falsificável**
(`packages/cmd/aos/aos222_fencing_truthfulness_test.go`) varre o *source* do caminho de posse e
**recusa** um comentário/log que afirme que um fencing **barra/rejeita escritas** enquanto o
`FencedAppender` **não** estiver composto no pacote do nó. Se um dia o nó compuser o
`FencedAppender`, a premissa da guarda inverte-se e o claim passa a ser legítimo.

## 6. Referências

- `specs/00_AOS_Carta.md §7` (emenda 1.2) — single-host non-goal datado; distribuído = EPIC-10.
- `packages/control-plane/orchestrator/contract/ports.go` — portas `Orchestrator`/`Scheduler` (AOS-012).
- `packages/cmd/aos/service.go` — `NodeService`, posse por lease durável (AOS-164a), shutdown gracioso durável (AOS-164b).
- ADR-007 (Event Store como fonte de verdade), ADR-015 (durable execution — contrato próprio), AOS-018 (lease/fencing).
- `packages/cmd/aos/aos222_fencing_truthfulness_test.go` — guarda de veracidade do §5-bis (AOS-222).
- `packages/kernel/agent-runtime/durable/fencing.go` (`FencedAppender`), `packages/kernel/agent-runtime/worker/worker.go` (`worker.Worker`) — o enforcement opt-in NÃO composto no nó (AOS-222).
