# PROC-ESCALA — Escala horizontal e degradação graciosa

| Campo | Valor |
|---|---|
| ID | PROC-ESCALA |
| Versão | 1.0 |
| Tipo | Procedimento operacional (forward-ref fechado pelo AOS-107) |
| Modo de falha | Fila e latência p95 a subir; risco de acumulação ilimitada e cascata de timeouts |
| ADR | ADR-008 (admission control + degradação graciosa), ADR-010 (observabilidade) |
| Alerta ligado (AOS-105) | `sandbox_cold_start_p95_high` (`SLISandboxColdStartP95`) → DevOps/SRE; cruza com RB-01 (headroom=0) / RB-03 (headroom<0) |
| Componentes | SCH (`packages/control-plane/scheduler`: `scale.go`, `degradation.go`, `policy.go`, `queue.go`), SBX (`substrate/sandbox` autoscaler, AOS-103) |
| Referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §5 (escala + degradação), §7 (SLIs), `specs/EPIC-03` |

## Sinal

- A **profundidade de fila** (`aos.scheduler.queue.depth`, agregada) e o **p95 de wait** (`aos.scheduler.dispatch.wait_p95_ms`) sobem em conjunto — trabalho a acumular e a esperar mais.
- O gauge de **réplicas desejadas** (`aos.scheduler.scale.desired_replicas`) diverge do actual (`aos.scheduler.scale.actual_replicas`): há pressão de carga que o plano de dados corrente não absorve.
- Se a subida coincide com **headroom no chão**, dispara também RB-01 (headroom fixado em 0, rate-limit) ou RB-03 (headroom negativo, orçamento) — a escala já não é a resposta; entra a degradação.

## Diagnóstico

1. Ler os **SLIs de escala** no dashboard: profundidade de fila total/máxima por partição, p95 de wait, e o **headroom** (`aos.scheduler.headroom.free_tokens` / `utilization`). O `HorizontalScaler.Tick` decide a partir exactamente destes três sinais.
2. Confirmar se **HÁ headroom**: `max_spawn = deriveMaxSpawn(headroom, custo_por_réplica)` > 0. Com headroom, o caminho correcto é **scale-out** — o alvo `desired_replicas` cresce com a fila e o p95, **limitado pelo `max_spawn`** (nunca ultrapassa o headroom real, ADR-008).
3. Se o **headroom se esgotou** (`max_spawn = 0`), o scaler **não escala** (fail-closed: 0-crescimento sob headroom nulo) e entra na **escada de degradação global**. Ler o gauge `aos.scheduler.degradation.level` (0=nenhum, 1=shed, 2=defer, 3=downgrade, 4=reject): o degrau activo sobe conforme a pressão agregada, seleccionado pela política declarativa (`PolicyEngine.Select`, AOS-030).
4. Confirmar que a fila **não cresce ilimitada**: as `PartitionedQueues` têm `MaxLen` por partição (backpressure) e a escada descarta/adia/degrada/rejeita cada item — a acumulação ilimitada e a cascata de timeouts são substituídas por uma resposta previsível (AC6).
5. Verificar que novas réplicas **não exigem rebalancing**: o estado é particionado por *run* e o `Assigner` (AOS-099) reclama leases livres — uma réplica nova corre `TryAcquire` sobre runs livres, sem roubo nem coordenação intra-processo.

## Mitigação

1. **Com headroom (scale-out).** Deixar o `HorizontalScaler` emitir o `ReplicaTarget` (sinal de escala) e o autoscaler do pool (`substrate/sandbox`, AOS-103) ampliar o pool de microVMs pré-aquecidas — a carga é absorvida **sem cold-start** visível até ao limite do headroom. A aplicação real das réplicas de worker vive no *composition root* (ápice): confirmar que o `ReplicaSink` está ligado e que o alvo desejado está a ser aplicado.
2. **Sem acção manual no caminho feliz.** O crescimento é **automático e dirigido por SLIs**; `max_spawn` é derivado do headroom (reavaliado a cada avaliação), nunca uma constante. Nunca desligar o admission control nem fixar `max_spawn` para "escalar mais depressa" — isso reintroduz o colapso agregado (RB-01).
3. **Headroom esgotado (degradar).** Deixar a escada actuar na ordem canónica **shed → defer → degradar → rejeitar**: shed descarta trabalho não-essencial (fail-closed: nunca crítico/irreversível), defer adia trabalho diferível preservando-o, downgrade encaminha para um modelo mais barato (variância explícita, reversível), reject é o terminal fail-closed com sinal ao utilizador. A entrada na escada sobe com a pressão (a política escolhe o degrau); `Degrader.ExecuteChain` escala a partir daí.
4. **Recuperação.** Quando o refill do token-bucket repõe headroom (`max_spawn` volta a > 0), o `Tick` volta ao scale-out e **normaliza** (`Degrader.Normalize`): os downgrades reversíveis restauram o tier original (`tier_restored`); shed/reject não são reversíveis (documentado). O alerta recupera sozinho quando os SLIs saem da zona sustentada.
5. Se a pressão persiste **com headroom** mas o pool não absorve, investigar cold-start (RB do pool) e o tecto absoluto do `PoolSizer` — um tecto mal dimensionado limita a absorção antes do headroom.

## Conformidade (ADR-008 / ADR-010)

- **Escala limitada pelo headroom**: `desiredReplicas` nunca ultrapassa o `max_spawn` real; 0-crescimento sob headroom nulo (fail-closed) — não se serve para lá do headroom.
- **Escada declarativa e observável**: cada degrau emite métricas/spans (`aos.scheduler.degradation.*`, `degradation.level`) e liga-se ao alerta de headroom (RB-01/RB-03). Sem segredos nos atributos (só dimensões operacionais).
- **Sem acumulação ilimitada**: filas limitadas + backpressure + escada garantem uma resposta previsível em vez de fila infinita e cascata de timeouts.
- Game day / prova: `packages/control-plane/scheduler/scale_test.go` (scale-out por SLI limitado pelo headroom; `max_spawn` acompanha o headroom; escada na ordem correcta sob esgotamento; ausência de fila ilimitada; emissão de métricas de degradação/nível).
