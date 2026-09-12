# PROC-DESPACHO-MULTIPROC — Topologia N-réplicas do despacho governado do Planeador

| Campo | Valor |
|---|---|
| ID | PROC-DESPACHO-MULTIPROC |
| Versão | 1.0 |
| Tipo | Procedimento operacional (v1.1 distribuído; entregue por AOS-392) |
| Modo de falha | Correr N réplicas do `aos-orq` sobre o mesmo substrato sem arbitragem ⇒ efeito duplicado (dois processos a despachar/spawnar o mesmo run) |
| ADR | ADR-023 (escritor único por-run: o lease arbitra), ADR-024 (o efeito vive no despacho, não na materialização), ADR-018 (fronteira nó↔ORQ/SCH) |
| Componentes | `packages/cmd/aos-orq` (`serve`, `dispatch_wiring.go`), `packages/control-plane/runlifecycle` (Tenure/lease/readers), `packages/control-plane/orchestrator/plandispatch` (Dispatcher), substrato JetStream replicado (AOS-100) |
| Referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §5, `packages/cmd/aos-orq/aos392_despacho_multiproc_test.go` (prova), `specs/EPIC-10` §AOS-392 |

## Modelo (o que torna N réplicas seguro)

O distribuído v1.1 do Planeador é **scale-out por run**, não co-planeamento do mesmo run (ADR-023 §2.1): **N processos `aos-orq`, cada um dono dos seus runs**, coordenados apenas pelo Event Store replicado. Para um dado run, a posse é arbitrada por um **lease durável**: exactamente um processo ganha (escreve o ciclo de vida e despacha); os outros saem **negados-pelo-lease** (exit 3). O efeito por-nó (spawn de papel / arranque de folha) nasce no **despacho governado** (ADR-024), sob a posse — logo nasce **uma só vez**, no processo dono. É esta invariante que a prova de AOS-392 exercita (`vencedores=1` por run).

## Topologia

```
N× aos-orq serve --nats <cluster-addr> --nats-stream <stream> --nats-replicas 3 [--nats-region <regiao-legal>]
```

- **`--nats-replicas 3`**: o stream JetStream é replicado (3 réplicas) — é o substrato que arbitra (AOS-100). Sem replicação não há a durabilidade que o lease assume.
- **Atribuição de runs**: por omissão, cada réplica reclama os runs que lhe chegam (claim por lease); dois processos que reclamem o MESMO run resolvem-se pelo lease (um ganha, o outro sai exit 3). Partição explícita de runs por réplica é opcional e não necessária para a correcção.
- **Substrato único**: `--wal` e `--nats` são mutuamente exclusivos; multi-processo é **só** com `--nats` (ver «Guard» abaixo).
- **Soberania**: `--nats-region` recusa arrancar numa região que o cluster não serve (fronteira regional, AOS-100).

## Arranque / paragem

1. **Arranque**: iniciar N instâncias com a MESMA `--nats`/`--nats-stream` e `--nats-replicas 3`. Cada uma reclama os seus runs. Confirmar no arranque: banner de postura verde, região aceite, substrato replicado ligado.
2. **Paragem graciosa**: parar uma réplica com o anúncio de largar a posse (`--release` no fim de um run, ou o shutdown que anuncia). A réplica seguinte assume o run **sem esperar o TTL** (posse sequencial — ver `TestAOS100_PosseSequencialContinuaAFuncionarNoReplicado`).
3. **Escala**: acrescentar réplicas é seguro a qualquer momento (cada uma pega runs livres). Reduzir: parar graciosamente para o handoff ser imediato.

## Recuperação da morte de uma réplica

- A morte **abrupta** de uma réplica dona de um run deixa o lease a expirar por **TTL** (`leaseTTL`). Outra réplica assume o run após a expiração e **re-hidrata** o grafo do log (`RebuildDAG`) — o despacho retoma sem re-executar o que já concluiu (idempotência por `(run_id, step_id)`).
- **NÃO** forçar a tomada de um run cujo lease ainda está vivo: o exit 3 («negado-pelo-lease») diz ao operador para **parar o outro dono do run**, não para o contornar. Roubar o lease violaria a invariante de escritor único (ADR-023).
- Janela conhecida: a janela TOCTOU do caso token-igual do `FencedAppender` mantém-se delegada ao CAS do substrato de produção (ADR-023 §4) — não é fechada por este procedimento.

## O que observar (OTel)

- **Laço de despacho**: spans do `plandispatch.Dispatcher` por passagem — outcomes por nó (`dispatched`, `deferred_headroom`, `waiting_deps`, `waiting_condition`, `branch_not_taken`). Um `branch_not_taken` é poda correcta (ADR-022 §2.1), não erro.
- **Sink**: spans do efeito por-nó — `subagent.spawned` (papel) e o arranque da folha (`task.node.state_changed → running`), sempre sob a posse (fenced).
- **Posse**: contagem de `vencedores`/`negados-pelo-lease` por run. Um run com **>1 vencedor** é um alarme grave (o substrato não está a arbitrar) — parar e investigar o substrato antes de continuar.
- **Duplicação**: nenhum `subagent.spawned` nem `retention.expired` duplicado para o mesmo facto. Duplicados indicam efeito a nascer fora da posse.

## Guard (não-regressão)

- Multi-processo **exige `--nats`**. Sobre `--wal`, um segundo processo é **recusado com exit 5** (`ErrWALHeld`, AOS-285/286) — o WAL de ficheiro não arbitra entre processos, e correr N sobre ele seria a corrupção que o guard existe para impedir.
- `--wal` **e** `--nats` em simultâneo são recusados (substrato ambíguo).

## Limitações declaradas (v1.1)

- **Headroom** de concorrência: neste binário é um semáforo bounded local; a integração com o `scheduler.SpawnCoordinator` (AOS-028, max_spawn=f(headroom) global) é follow-up.
- **Laços de serviço** partilhados (retenção): a exclusão entre réplicas é o eixo do AOS-283 (só o laço de retenção precisa; os restantes já são seguros por lease/partição/chave durável). Até AOS-283 aterrar, correr a retenção numa só réplica.
- **Model Gateway** (goal→DAG por LLM vivo): AOS-391 (bloqueado por identidade/cutover AOS-278); com `--decompose-fixture` o pipeline corre sem LLM vivo.
