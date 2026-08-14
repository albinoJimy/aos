# ADR-021 — Roteamento por scoring ponderado determinístico no Model Gateway

| Campo | Valor |
|---|---|
| **ADR** | 021 |
| **Título** | Roteamento por scoring ponderado determinístico no Model Gateway (selecção automática de modelo com sinal de qualidade, sem exploração online) |
| **Estado** | Aceite (emenda 1.2, 2026-08-14) |
| **Data** | 2026-08-05 (ratificado 2026-08-13; emendas 1.1 em 2026-08-13 e 1.2 em 2026-08-14) |
| **Deciders** | Equipa AOS (**ratificação de dono**, 2026-08-13; **emendas 1.1 e 1.2 de dono**, 2026-08-13 / 2026-08-14) |
| **Contexto-fonte** | Análise comparativa do conceito «Auto-Combo» do OmniRoute ([AUTO-COMBO-GUIDE](https://github.com/diegosouzapw/OmniRoute/blob/release/v3.8.50/docs/getting-started/AUTO-COMBO-GUIDE.md)) contra o router AOS-059 em `packages/platform/model-gateway/routing/router/router.go` |
| **ADRs relacionados** | ADR-008 (admission tokens/$), ADR-010 (observabilidade/replay), ADR-011 (policy-as-code + soberania por board), ADR-012 (SemVer + eval-gate para artefactos comportamentais) |
| **Supersede** | — |

> **RATIFICADO (2026-08-13, autoridade de dono).** O estado passou de *Proposto* a
> *Aceite*: a decisão da §2 (as cinco regras) é agora **autoridade congelada** e não se
> re-litiga sem emenda datada (Carta §6). O que fica fora — a gramática dos perfis, os
> pesos iniciais e o formato da tabela assinada (§4 «Fora de escopo») — **continua fora**:
> é trabalho de `tecnica/06` e do ticket de implementação (**AOS-269**, EPIC-20), não
> decisão re-aberta por esta ratificação.
>
> Este ADR **propõe** evoluir a decisão de roteamento do Model Gateway (GW) de uma
> composição lexicográfica de regras com prioridades fixas (AOS-059) para um **scoring
> ponderado determinístico** com pesos declarados como policy-as-code e um sinal de
> qualidade (*task-fit*) calibrado offline. **Não** propõe exploração online tipo
> *bandit* nem variantes mágicas que contornem soberania, allowlist ou admission: os
> invariantes de fronteira do GW são pré-condições, não factores do score.

---

## 1. Contexto

O router do GW (AOS-059, `routing/router/router.go:378-502`) decide hoje por composição
ordenada de regras determinísticas: soberania (partição estrutural) → carga (headroom
inteiro TPM/RPM) → tiering (tier mais barato capaz; Fast para interactivo) → orçamento
(degradação a ~80%). É fail-closed, determinístico (sem relógio nem aleatoriedade,
desempate estável — `router.go:31-34`) e regista cada decisão com razão (span
`model_routing`, `DecisionSink`).

Gateways de referência da indústria (caso estudado: OmniRoute «Auto-Combo») oferecem
selecção automática por **soma ponderada de factores** — health, quota, custo, latência,
*task-fit*/qualidade, estabilidade — com perfis de pesos nomeados (`auto`, `auto/fast`,
`auto/cheap`, `auto/coding`), fallback automático, exclusão temporária de providers
doentes e **exploração *bandit*** (5–10% dos pedidos desviados aleatoriamente para
aprender).

O gap real do AOS face a esse modelo não é o mecanismo de fallback nem o circuit breaker
(já cobertos: failover intra-fronteira no GW; admission/breaker no Escalonador, ADR-008).
São dois:

1. **Ausência de sinal de qualidade.** O router só vê custo, capacidade, latência e
   carga. Não há *task-fit*: nada distingue «o tier mais barato capaz» do «o tier que
   historicamente resolve bem este tipo de tarefa» — apesar de o eval harness (EPIC-08)
   já produzir exactamente essa medida.
2. **Prioridades fixas.** A ordem lexicográfica carga > custo > latência está gravada em
   código. Um pedido *batch* que queira «o mais barato possível» ou um interactivo que
   queira «qualidade acima de tudo» não tem forma declarada de o exprimir — apenas a
   classe `interactivo/batch` e a `Capability`.

## 2. Decisão

Introduzir no estágio de roteamento do GW um **scoring ponderado determinístico** que
substitui o ordenamento lexicográfico *após* as guardas estruturais, sob cinco regras:

1. **Guardas primeiro, score depois.** A partição de soberania (cross-border descartado),
   a allowlist do board (default-deny, assinada) e o piso de capacidade do tier
   (`capableAllowlistFilter`) continuam a executar **antes** de qualquer ranking e não
   são factores do score. O scoring apenas **ordena sobreviventes** intra-fronteira,
   permitidos e capazes.
2. **Factores como portas injectáveis.** Cada factor (health, headroom/quota, custo,
   latência, *task-fit*, estabilidade) entra por porta com implementação de referência
   determinística, à imagem de `LoadProvider`/`BudgetProvider`/`HealthFunc`. Aritmética
   inteira/ponto-fixo — **sem floats** no data-plane (zero-dep preservado).
3. **Pesos como policy-as-code versionada e assinada.** A tabela de pesos (e os perfis
   nomeados, p. ex. `balanced`/`fast`/`cheap`/`quality`) é um **artefacto comportamental**
   (SemVer + eval-gate, ADR-012), carregada fail-closed como a allowlist — sem tabela
   válida, o router recusa (não assume pesos implícitos).
4. **Calibração offline, nunca online.** O sinal de *task-fit* e a evolução dos pesos
   derivam de análise **offline** do `DecisionSink`/spans e dos resultados do eval
   harness, promovida por uma nova versão do artefacto de pesos ratificada pelo
   eval-gate. **Não há *bandit* nem aprendizagem online em runtime** — a decisão
   permanece função pura dos inputs (sem `rand`, sem relógio), preservando o replay
   (ADR-010).
5. **Swap continua variância explícita.** Qualquer escolha divergente do modelo pedido é
   registada como variância atribuível (`model_swap`) com razão — o scoring nunca troca
   em silêncio, e a razão passa a incluir o score e o perfil de pesos aplicado.

A cadeia `shed → defer → degradar → rejeitar` permanece no Escalonador
(`scheduler.ModelTierRouter`); o GW continua a *oferecer* escolha/degradação, não a
impor admissão.

## 3. Alternativas consideradas

- **(a) Manter o lexicográfico puro (estado actual).** Rejeitada: não admite sinal de
  qualidade nem intenção declarada do consumidor; cada novo «perfil» exigiria código, e
  a ordem fixa carga > custo > latência não serve simultaneamente batch-custo e
  interactivo-qualidade.
- **(b) Port directo do Auto-Combo (soma de pesos + *bandit* online + fallback de
  emergência global).** Rejeitada em três pontos inegociáveis: a exploração online é
  não-determinística e quebra o replay (ADR-010); um *fallback* de emergência para
  providers externos violaria a soberania por board (ADR-011) e a allowlist
  default-deny; e pesos ajustados em runtime seriam um artefacto comportamental não
  versionado nem avaliado (ADR-012).
- **(c) Scoring antes das guardas (score global e filtragem posterior).** Rejeitada:
  tornaria soberania e allowlist «factores» em vez de invariantes estruturais — um
  score alto nunca pode ressuscitar um candidato cross-border. A ordem guardas → score
  é a única compatível com ADR-011.
- **(d) Scoring determinístico pós-guardas com pesos versionados e calibração offline
  (a decisão).** Captura o valor real do Auto-Combo (*task-fit* + perfis declarativos)
  sobre sinais que o sistema já recolhe, sem introduzir não-determinismo nem novas
  fronteiras de confiança.

## 4. Consequências

- **Positivas:** o *task-fit* dos evals passa a influenciar o roteamento (fecha o loop
  EPIC-08 → EPIC-06); perfis de pesos declarativos exprimem intenção sem código novo;
  o `DecisionSink` ganha um consumidor canónico de calibração; replay e determinismo
  intactos; nenhuma dependência nova no data-plane.
- **Custos aceites:** mais um artefacto assinado para gerir (tabela de pesos) com o
  ciclo SemVer/eval-gate; a superfície de teste do AOS-063 cresce (novos cenários e
  meta-testes de detecção); a decisão passa a depender de sinais dinâmicos (latência,
  estabilidade, *task-fit*) que exigem impls de referência determinísticas e fixtures
  no `testkit`.
- **Fora de escopo (não-decidido aqui):** a gramática concreta dos perfis nomeados, os
  pesos iniciais, e o formato da tabela assinada ficam para o documento técnico
  (`tecnica/06`) e para o ticket de implementação que este ADR, se ratificado, originar
  no EPIC-06.

## 5. Conformidade / Enforcement

- **Determinismo:** guard-test ou meta-teste no `routingtests` prova que a decisão é
  função pura dos inputs (sem `rand`/relógio) e que dois *runs* com os mesmos sinais
  produzem o mesmo candidato — condição do gate `ci-routing`.
- **Soberania/allowlist:** cenário AOS-063 alargado prova que nenhum peso elege um
  candidato cross-border ou fora da allowlist (deny atribuível registado).
- **Fail-closed:** sem tabela de pesos válida/assinada, o router recusa (à imagem de
  `allowNone`, `router.go:95-102`); sinal de factor ausente resolve pelo lado seguro.
- **Artefacto comportamental:** a tabela de pesos entra no ciclo ADR-012 — alteração de
  pesos = bump de versão + passagem no eval-gate antes de produção.
- **Observabilidade:** o span `model_routing` e o `DecisionSink` registam perfil,
  factores e score final; variância `model_swap` inclui a razão de scoring.
- **Cobertura:** o módulo GW não pode regredir abaixo de `ROUTING_COVERAGE_MIN` (piso
  AOS-199); os novos cenários entram no `require_tests` de `scripts/ci/routing.sh`.

## 5-bis. Emenda 1.1 (2026-08-13, autoridade de dono) — alcance da regra 3 na v1

**O que se emenda.** A regra 3 da §2 diz que «sem tabela válida, o router recusa», e a §2
abre com «scoring ponderado determinístico que **substitui** o ordenamento lexicográfico».
Lida à letra, essa frase implicaria que o scoring fosse o caminho de roteamento **por
omissão** do gateway. Esta emenda fixa que, na **v1**, o scoring é **composto por opção**
(`router.WithScoring`) e a regra 3 aplica-se **quando o scoring está composto**: sem tabela
válida/assinada, um scoring armado **recusa** (é o que o código faz, `scoreUnarmed`); sem
`WithScoring`, o router mantém o ordenamento lexicográfico de AOS-059, inalterado.

**Porque se emenda — o facto que a implementação revelou.** Ao implementar AOS-269
descobriu-se que o `router.Router` (AOS-059) **nunca esteve composto no pipeline de
produção do gateway**: `NewProduction` compõe `failover.NewStage` como estágio de
roteamento, e `router.New` não tem, em todo o repositório, um único chamador fora de
testes. O scoring foi construído **sobre** esse router. Logo, «substituir o lexicográfico
por omissão» não é uma linha de wiring: exigiria compor o estágio de roteamento inteiro no
pipeline — substituindo ou complementando o failover —, uma mudança arquitectural do
caminho quente de **todas** as chamadas de modelo, muito para além do âmbito de AOS-269.

**O que NÃO se emenda.** As cinco regras da §2 mantêm-se congeladas na sua substância:
guardas antes do score e nunca como factores; aritmética inteira; pesos assinados e
versionados com carregamento fail-closed; calibração offline sem *bandit*; swap como
variância explícita. A emenda altera **o alcance da composição**, não o desenho.

**Dívida registada, não escondida.** A lacuna real — compor o estágio de roteamento
(`routingstage` + `router`, com o scoring armado) no pipeline do gateway, hoje servido pelo
`failover` — fica registada como deferimento com eixo (**DEF-271**, `REGISTO-Deferimentos`)
e é **pré-existente a AOS-269**: o ticket entregou a máquina completa (portas de factores,
tabela assinada, guard-tests de determinismo, cenários de soberania), não o wiring de um
router que nunca esteve ligado. Enquanto essa dívida não fechar, o scoring **não tem efeito
em produção**, e é isso — não outra coisa — que o ticket declara.

> ⚠️ **SUPERADO pela emenda 1.2 (§5-ter).** O parágrafo acima descreve o estado de
> 2026-08-13. **DEF-271 fechou** em 2026-08-14 (AOS-280) e o scoring **passou a ter efeito**
> no caminho de produção do gateway. O texto fica por baixo, e não reescrito, para que a
> revisão que o encontrar veja *quando* deixou de valer — a emenda 1.1 continua a ser a
> autoridade sobre a **regra 3**; o que a 1.2 corrige é o **estado de facto**.

## 5-ter. Emenda 1.2 (2026-08-14, autoridade de dono) — o scoring passou a ter efeito

**O que se emenda: o ESTADO DE FACTO, não a decisão.** A emenda 1.1 (§5-bis) declarava que o
scoring estava construído mas **sem efeito em produção**, porque o `router.Router` nunca fora
composto no pipeline do gateway. Em **2026-08-14** o **AOS-280** compôs-o e **DEF-271 fechou**:
o estágio de roteamento passou a ser `failover` **→** `routingstage`+`router`, por
**encadeamento** (decisão de dono) — o `failover` mantém a soberania, o *failover* por saúde e
a selagem do deny cross-border no WORM; o `router` refina dentro dessa fronteira.

**A regra 3 deixou de ser postura e passou a recusa de ARRANQUE.** «Sem tabela válida/assinada
o router recusa» era, na v1 opt-in, uma afirmação sobre um caminho que ninguém percorria. Com o
estágio composto: um perfil de pesos desconhecido, uma tabela não-verificável ou **um modelo da
escada sem preço na tabela de custo** fazem o gateway **recusar arrancar** — não falhar por
chamada. Esta última guarda nasceu de uma regressão que a auditoria de AOS-280 apanhou e que o
próprio ticket introduzira: sem ela, o refino podia degradar para um modelo sem preço, **o
provider era invocado e facturado** e só depois a chamada falhava — o chamador retentava e
repetia o gasto.

**O que NÃO se emenda.** As cinco regras da §2 mantêm-se congeladas na substância — guardas
antes do score e nunca como factores; aritmética inteira; pesos assinados e versionados com
carregamento fail-closed; calibração offline sem *bandit*; swap como variância explícita. E
mantém-se o mecanismo da 1.1: o scoring **compõe-se por declaração do deployment**
(`RoutingConfig.Tiers`) — o que muda é que essa declaração agora **liga mesmo** o refino, em vez
de o deixar inerte.

**Alcance honesto, para não trocar uma imprecisão por outra.** «Tem efeito em produção»
significa **no gateway**, quando o deployment declara a escada. O binário do nó
(`packages/cmd/aos`) **não a declara**, pelo que aí o refino **ainda não corre** — deferido com
eixo em **DEF-280-NO**. Declarar a escada é decisão de deployment (que modelos, com que
cobertura de preço e de credencial), não uma linha de código em falta.

## 6. Referências

- OmniRoute, [AUTO-COMBO-GUIDE](https://github.com/diegosouzapw/OmniRoute/blob/release/v3.8.50/docs/getting-started/AUTO-COMBO-GUIDE.md) — conceito-fonte (scoring 12-factores, variantes, *bandit*).
- `specs/EPIC-06_Model_Gateway_Custos.md` — AOS-055 (pipeline), AOS-058 (allowlist regional), AOS-059 (roteamento cost/load-aware), AOS-063 (cenários de gate).
- `tecnica/06_Model_Gateway_Custos.md` §3, §5, §6 — pipeline, failover e decisão de routing actuais.
- `packages/platform/model-gateway/routing/` — `router/router.go`, `tiering/`, `sovereignty/`, `failover/`, `degradation/`, `routingtests/`.
- `scripts/ci/routing.sh` — gate `ci-routing` (cenários + meta-testes + cobertura).
- ADR-008, ADR-010, ADR-011, ADR-012.
