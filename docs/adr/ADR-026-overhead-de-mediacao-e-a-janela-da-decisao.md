| Campo | Valor |
|---|---|
| ADR | ADR-026 |
| Título | O overhead de mediação é a janela da DECISÃO: o SLO de 15 ms exclui o despacho, e a duração da tool call mediada fica deliberadamente sem alvo |
| Estado | **Ratificado (2026-09-16, AOS-398)** |
| Data | Setembro 2026 |
| Deciders | Equipa AOS |
| Contexto-fonte | `tecnica/19` §4 e §7 (P-02/S-02f — o número normativo e a sua contradição interna); `tecnica/08` §7.1; AOS-085 (o SLI), AOS-086 (o alerta), AOS-274 (o avaliador no nó); `docs/governance/REGISTO-Deferimentos.md` (`DEF-281`); incidente de produção de 2026-09-15/16 (run `run-delegado-1789519407`) |

## Contexto

O `_BRIEF` e o `tecnica/19` §4 fixam um número normativo para o Reference Monitor: **overhead p95 < 15 ms**. O AOS-085 materializou-o como SLI `mediation_overhead_p95` e o AOS-086 pendurou-lhe dois alertas `critical` com rota para o **RB-04 («Falha de PDP»)**. O número, o SLI e o alerta existem desde então.

O que nunca foi decidido é **onde a janela fecha** — e as duas leituras possíveis diferem por duas ordens de grandeza.

**Facto 1 — o span da mediação envolve a execução da tool.** `Monitor.Mediate` abre o span `execute_tool` e anota a decisão num `defer`; `Monitor.evaluate` chama `m.dispatch` **antes** de devolver a decisão. O span só fecha depois de a tool correr. A sua latência é, por construção, *decisão + execução no sandbox*.

**Facto 2 — as duas populações são separadas por duas ordens de grandeza, e foram medidas.** O selo `tool.call.mediated.latency_ns`, escrito antes do efeito, registou **2–8,6 ms** nos runs reais. A execução da mesma tool em gVisor no nó de produção mediu **0,6–1,8 s** no E2E de 2026-09-15. Um tecto de 15 ms discrimina perfeitamente a primeira população e é violado por toda a segunda.

**Facto 3 — o documento normativo contradizia-se.** No `tecnica/19` §4, a linha do PDP aplica os 15 ms à *avaliação de política*. No §7, o sub-processo S-02f listava `EXEC` **dentro** da cadeia orçamentada. Nenhuma das duas leituras estava marcada como a vinculativa, e a implementação seguiu a segunda sem que ninguém o tivesse decidido.

**Consequência medida.** Em 2026-08-27, com tráfego real: 1 amostra, `3,047 s` contra `15 ms`. Em 2026-09-15/16, o run `run-delegado-1789519407` — dois turnos, **uma** tool call `doc_read` — acendeu `mediation_overhead_high` (catálogo `mediation`) e `mediation_overhead_p95_high` (catálogo `operational`), ambos `critical`, com `1,21099128e+09` ns contra `1,5e+07`, streak a subir até 4. Nada estava avariado. **Em qualquer nó com sandbox real, uma tool call normal produzia um `critical` que mandava o operador depurar a peça sã.** Era `DEF-281`, aberto e declarado no código desde 2026-08-27.

Um alerta que toca sempre não é um alerta caro: é um alerta que ensina a ignorar a classe inteira. O custo real do defeito é o `critical` verdadeiro que ninguém vai ver.

## Decisão

### 1. O overhead de mediação é a janela da DECISÃO — arbitragem do `tecnica/19`

A leitura vinculativa dos 15 ms é a da **linha do PDP**: o alvo governa a cadeia de decisão — resolução de identidade e cadeia NHI, avaliação PDP, validação de orçamento, egress, imposição de obrigações e **escrita do selo pré-efeito** — e **termina onde o despacho da tool começa**.

O `EXEC` sai do orçamento. A ordem do S-02f passa a `LOOKUP→DIGEST→ASSINATURA→SCOPE/EGRESS→AUDIT→EXEC`, que é também a ordem real do código (audit-before-effect), com o alvo declarado até ao `AUDIT`.

**A escrita do selo fica DENTRO da janela.** Está no caminho crítico — atrasa o efeito —, logo é overhead. Um sink de auditoria lento tem de aparecer no SLO, não esconder-se atrás dele. Isto torna a janela do SLI **deliberadamente mais larga** que a do `latency_ns` do selo, que pára imediatamente antes da escrita.

### 2. O kernel publica a janela; o SLI lê-a

O Reference Monitor mede a janela de §1 e anota-a no span `execute_tool` em `aos.mediation.decision_latency_ns`. O valor sai também em `Decision.DecisionLatency`, distinto de `Decision.Latency` (que continua a ser a janela total, com despacho).

O SLI `mediation_overhead_p95` deriva **desse atributo**. Mantém o filtro da decisão (`aos.decision` não-vazio), que é o que separa o produtor Reference Monitor dos outros dois emissores de `execute_tool` (worker e Launcher), e acrescenta a exigência do atributo.

**Um span que decide mas não traz a medida cai FORA da amostra — não há fallback para a latência do span.** A ausência de fallback é a decisão, não um detalhe: cair para o span devolveria exactamente o número errado que este ADR existe para deixar de publicar. `Samples == 0` faz o rótulo `avaliavel="0"` dizer a verdade, e o operador vê que não há sinal em vez de ver um sinal falso.

### 3. O selo `tool.call.mediated` NÃO muda

O `latency_ns` do selo é contrato de fio ancorado no WORM (`tecnica/12` §4). Continua a medir o que sempre mediu. Redefini-lo reescreveria o significado de rasto já ancorado, e a medida nova não vale esse preço.

### 4. A duração da tool call mediada fica SEM SLO — deliberadamente

A janela inteira (decisão + execução) continua observável: é a latência do próprio span `execute_tool`, presente no wide event, com drill-down até ao trace.

**Não recebe SLO nem alerta.** Nenhum documento normativo ratifica um orçamento para a execução de uma tool, que depende do runtime de sandbox e da tool concreta; o intervalo medido em gVisor (0,6–1,8 s) é uma observação, não um alvo. Fixar um limiar por estimativa recriaria — com outro nome e a mesma rota RB-04 — o alerta mal calibrado que este ADR apaga. Quando existir alvo ratificado, entra como SLI próprio; o vocabulário para o exprimir já existe.

## Alternativas consideradas

**(A) Renomear o SLO para «duração da tool call mediada» e subir o alvo.** Rejeitada. Preserva o alerta e perde o sinal: o overhead da decisão — a única coisa que o RB-04 sabe depurar — deixaria de ser medido, e o alvo novo teria de ser inventado (ver §4). Trocava um número errado por um número arbitrário.

**(B) Manter a fonte e subir o tecto para ~2 s.** Rejeitada. Calibra o alerta para o sandbox mais lento observado e torna-o cego a uma cadeia de política que degrade de 8 ms para 500 ms — uma avaria de PDP de duas ordens de grandeza que passaria despercebida. É o pior resultado possível: o alerta cala-se **e** deixa de servir.

**(C) Dois SLIs com SLO desde já.** Rejeitada pela §4, não por princípio. É o destino natural assim que houver um alvo ratificado para a execução.

**(D) Inferir o produtor pela parentela em vez de pela decisão.** Rejeitada. O RM instrumenta **qualquer** chamador (ADR-002), pelo que o pai de uma mediação nem sempre é um `execute_tool`. O atributo de decisão é anotado num `defer` que cobre todos os caminhos de retorno de uma cadeia fail-closed — é o discriminador completo.

## Consequências

**Positivas.** O SLI mede o que o nome promete e o SLO passa a ser alcançável e discriminante. Os dois `critical` deixam de disparar em tráfego normal e voltam a significar o que o RB-04 descreve. O caveat que `packages/cmd/aos/api.go` declarava para o `mediation_overhead_p95` fica satisfeito: a instrumentação que faltava existe. `DEF-281` fecha.

**Negativas / custos aceites.**

- **Um nó com um Reference Monitor anterior ao AOS-398 deixa de alimentar o SLI** (`Samples == 0`, `avaliavel="0"`) em vez de o alimentar com o número errado. É a direcção conservadora, e é visível no `/metrics`.
- **A janela do SLI e a do selo divergem** (§1 vs §3): a primeira inclui a escrita de auditoria, a segunda não. A divergência é deliberada e está documentada nos dois sítios; quem comparar os dois números tem de saber porquê.
- **A duração da tool call mediada não tem alerta** (§4). Uma tool patologicamente lenta não produz sinal por esta via — produz pelo circuit breaker multi-sinal (AOS-080) e pelos timeouts do sandbox, que é onde esse sintoma pertence.

## Rastreabilidade

- Ticket: **AOS-398** (`specs/EPIC-08_Observabilidade_Evals.md`).
- Fecha: `DEF-281` (`docs/governance/REGISTO-Deferimentos.md`).
- Documentos reconciliados: `tecnica/19` §4 (linha RM), §7 (P-02/S-02f), §8 (números normativos); `tecnica/08` §7.1 (tabela dos quatro SLIs, criada por este ticket).
- Código: `packages/kernel/reference-monitor/monitor.go`, `decision.go`; `packages/substrate/otel-genai/semconv.go`, `wide_event.go`, `slo.go`.
- Testes: `packages/kernel/reference-monitor/aos398_overhead_decisao_test.go`; `packages/substrate/otel-genai/overhead_mediacao_test.go`.
- Antecedentes: ADR-002 (mediação total), ADR-010 (observabilidade), AOS-085/AOS-086/AOS-274.
