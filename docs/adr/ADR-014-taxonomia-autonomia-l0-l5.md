| Campo | Valor |
|---|---|
| ADR | ADR-014 |
| Título | Taxonomia de autonomia L0–L5 |
| Estado | Aceite (2026-09-08, AOS-380 — materialização escolhida pelo dono) |
| Data | Setembro 2026 |
| Deciders | Equipa AOS |
| Contexto-fonte | Catálogo de ADRs em `specs/00_System_Spec.md` (linha 259) e `docs/adr/README.md` (linha 67); semântica por nível em `tecnica/09_Governacao_Conformidade.md` §7; materializado em EPIC-09 (AOS-089, AOS-090, AOS-095), endurecido em AOS-305/AOS-307/AOS-377 |

## Contexto

O desenho anterior era quase-binário — «HITL por *default*, autonomia *opt-in*» — e falha nos dois extremos: ou gera *approval fatigue* (o humano carimba sem ler) ou concede autonomia a granel sem oversight proporcional ao risco. O AOS substitui-o por uma **escada de seis níveis** com oversight proporcional ao impacto.

Até AOS-380 a taxonomia vivia só como **rótulos de um bloco mermaid** em `tecnica/09` §7 e uma linha de catálogo em `specs/00_System_Spec.md:259` — sem texto com autoridade que dissesse, por nível, o que é exigível. A ausência tornava a conformidade contra a escada **não mensurável**: as hipóteses da forma «o nível N não faz o que a spec diz» morriam por não existir a spec normativa. Este ADR fixa essa semântica em texto normativo (não em rótulos de diagrama), fiel à letra do catálogo — «oversight proporcional ao impacto; promoção por fiabilidade medida; demoção automática» — e **separa o que já é exigível do que está deferido**, para que a matriz de conformidade possa citar a escada sem sobre-reivindicar.

O nível é sempre uma propriedade do **par (agente, domínio)**: um agente pode operar a L4 num domínio de baixo risco e a L1 noutro sensível.

## Decisão

### 1. Os seis níveis (texto normativo)

A fricção de cada nível compõe-se com a classe de risco SA-ROC (ADR-013) — o nível **não substitui** o tiering, sobrepõe-se-lhe (ver §3):

- **L0 — Sugestão.** O agente propõe; o humano executa tudo. Nenhuma *tool call* corre sem execução humana. É o piso mais supervisionado (também o valor fail-closed — ver §4).
- **L1 — Aprovação por acção.** Cada *tool call* espera aprovação humana individual antes de correr.
- **L2 — Aprovação por lote.** Acções *gray* materialmente equivalentes são agrupadas com resumo e cobertas por uma confirmação de lote (o `BatchKey` de ADR-013); acções *danger* nunca são agrupadas.
- **L3 — Autonomia supervisionada.** É **exactamente o tiering SA-ROC base**: *safe* corre, *gray* segue a política de lote/maturidade, *danger* confirma. É a fronteira em que o overlay de autonomia coincide com o gate de risco.
- **L4 — Autonomia por excepção.** Só escala para humano em incerteza ou risco alto; o restante corre. É o primeiro nível em que `danger` deixa de exigir confirmação sistemática — e por isso o **limiar de cerimónia** (ver §5).
- **L5 — Autonomia plena por domínio.** Oversight amostral e *post-hoc*, não por-acção.

### 2. Promoção por fiabilidade medida; demoção automática em anomalia (semântica)

- **Promoção** nunca é concedida por opinião: exige uma métrica de fiabilidade **sustentada** (por exemplo, taxa de erro < 2% ao longo de 30 dias, com *override-rate* baixo). É sempre monótona um nível de cada vez na direcção de mais autonomia.
- **Demoção** é **automática e imediata** ao detectar anomalia — um pico de *override-rate*, uma acção insegura, ou deriva medida rebaixam o par para um nível mais supervisionado **sem esperar por revisão humana**. É a metade de **segurança** da escada.

### 3. O que É exigível hoje (composto e enforçado)

- **O overlay de oversight `nível × classe`** está composto e é imposto no PDP: `autonomy.Oversight(nível, classe)` devolve `Escalate` quando o modo exige gate humano, e o overlay **só aperta** (permit→escalate), nunca transforma um deny em permit. É a materialização enforçável da escada.
- **O nível por par** vive num `LevelRegistry` durável, reidratado do WORM no arranque (AOS-307), com a mudança **selada** na hash-chain.
- **A cerimónia de mudança de nível** é fail-closed: subir para L4/L5 exige dual-control — duas assinaturas de emissores distintos com `autonomy:set` — pela rota `POST /autonomy` (AOS-305) e, desde AOS-377, também pela via de provisionamento por ficheiro (`AOS_AUTONOMY_PROOFS`); um piso `AOS_AUTONOMY_DEFAULT >= L4` é recusado no arranque.

### 4. Fail-closed pelo tipo

O valor-zero de nível resolve para **L0** (o mais supervisionado): um par sem registo, ou um piso ausente, não concede autonomia. Um piso inválido é ignorado e fica em L0 (nunca abre a guarda).

### 5. O que NÃO é exigível hoje (deferido, `DEF-908`)

A **automação** de §2 — o `autonomy.Controller`, que ligaria promoção-por-fiabilidade (`Evaluate`) e demoção-por-anomalia (`OnAnomaly`) — **não está composta no nó**: tem zero chamadores de produção (`DEF-908`, `docs/governance/REGISTO-Deferimentos.md`). O nó compõe apenas o `LevelRegistry`. Consequência declarada: **um par promovido a L5 fica a L5 até um humano intervir** — a demoção automática em anomalia, a metade de segurança, ainda não vigora. Enquanto `DEF-908` estiver aberto, a escada NÃO pode ser citada como controlo regulatório na parte da demoção automática (ver `tecnica/14_Matriz_Conformidade.md`, Art. 9).

Este ADR **materializa a semântica**; não fecha `DEF-908`. Compor o `Controller` (ligando `Evaluate` **e** `OnAnomaly` — ligar só o primeiro reproduz o «preso a L5») é trabalho de engenharia próprio, fora deste ADR.

> **ACTUALIZAÇÃO (2026-09-12, AOS-090 / ADR-025):** o `Controller` FOI composto no nó e `DEF-908` está **FECHADO-RESIDUAL**. A demoção automática (a metade de segurança) vigora — o TRIP do disjuntor demove a CLASSE, sem gate humano; a promoção automática vigora **abaixo de L4** (L4/L5 continuam a exigir a cerimónia assinada de §3) e **não é durável através de reinício** (reverte à base assinada — a chave do nó partilha o disco do WORM, logo uma elevação durável sem assinatura seria forjável). O sinal de fiabilidade honesto vem de um registo de desfecho pós-efeito novo, sem enfraquecer o audit-before-effect. Ver `ADR-025-fiabilidade-medida-e-controlador-autonomia.md` para o desenho e os residuais.

## Consequências

### Positivas

- A escada passa a ter **base normativa** mensurável: cada nível tem texto com autoridade, e a conformidade contra ele deixa de ser vacuamente inatacável.
- O overlay `nível × classe`, a durabilidade do registo e a cerimónia de dual-control ficam ancorados a um ADR, em vez de a rótulos de um diagrama.
- A matriz de conformidade pode citar a escada **sem sobre-reivindicar**, porque o ADR separa o composto do deferido.

### Negativas / Trade-offs

- **Metade de segurança inerte**: a demoção automática em anomalia está definida mas não composta (`DEF-908`) — a promoção não é revertida por máquina, só por decisão humana. É a lacuna mais consequente, e está declarada, não escondida.
- **Promoção também manual hoje**: sem o `Controller`, a promoção-por-fiabilidade é uma decisão de provisionamento assinada, não uma consequência automática de uma métrica.

## Alternativas consideradas

- **Via B de AOS-380 — aceitar por escrito que a escada não tem base normativa** (emenda à Carta declarando a conformidade não-mensurável): rejeitada pelo dono. O sistema já *enforça* a escada (overlay no PDP, dual-control L4/L5), pelo que formalizar a semântica de facto é mais coerente do que declará-la sem base.
- **Semântica só em diagrama** (o estado anterior): rejeitada — rótulos de mermaid não são texto exigível e tornam a conformidade não mensurável.

## Conformidade / Enforcement

- **Níveis e overlay**: `packages/control-plane/governance/autonomy/level.go` (L0–L5, fail-closed L0), `oversight.go` (`Oversight(nível, classe)`).
- **Overlay imposto no PDP**: `packages/control-plane/pdp/autonomy.go` (só aperta; permit→escalate).
- **Registo durável por par + reidratação selada**: `packages/control-plane/governance/autonomy/registry.go`, `packages/cmd/aos/autonomy_levels.go`, `autonomy_rehydrate.go` (AOS-307).
- **Cerimónia de mudança de nível (dual-control L4/L5)**: `packages/cmd/aos/autonomy_route.go`, `autonomy_setters.go` (`autonomyDualControlRequired`), `AOS_AUTONOMY_PROOFS` (AOS-377).
- **Metade deferida (não composta)**: `packages/control-plane/governance/autonomy/` `Controller` — `DEF-908` (zero chamadores de produção).

## Referências

- Catálogo: `specs/00_System_Spec.md:259`; `docs/adr/README.md:67`. Semântica por nível: `tecnica/09_Governacao_Conformidade.md` §7.
- ADR-013 — Gates de risco SA-ROC + controlo bidireccional (o tiering que L3 iguala e sobre o qual o overlay se compõe).
- Tickets: AOS-089 (escada L0–L5), AOS-090 (promoção/demoção automática — deferida em `DEF-908`), AOS-095 (gate HITL / Art. 14), AOS-305 (dual-control L4/L5), AOS-307 (reidratação selada), AOS-377 (dual-control na via de ficheiro + piso `>= L4` recusado), AOS-380 (esta decisão de materialização).
- `DEF-908` — `docs/governance/REGISTO-Deferimentos.md` (a demoção automática por anomalia sem chamador).
