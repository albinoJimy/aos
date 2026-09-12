| Campo | Valor |
|---|---|
| ADR | ADR-025 |
| Título | Fiabilidade medida e o controlador de autonomia: registo de desfecho pós-efeito, promoção automática abaixo de L4, demoção durável por classe |
| Estado | **Ratificado (2026-09-12, AOS-090)** — pelo dono, antes da alteração ao read-path soberano |
| Data | Setembro 2026 |
| Deciders | Equipa AOS |
| Contexto-fonte | ADR-014 §2/§5 (promoção por fiabilidade medida; demoção automática — deferida em `DEF-908`); AOS-090 (o controlador); `analises/10` e `analises/13` (auditorias que mediram a ausência de chamador); `docs/governance/REGISTO-Deferimentos.md` (`DEF-908`) |

## Contexto

O ADR-014 fixou a semântica da escada L0–L5: **promoção por fiabilidade medida** (taxa de erro sustentada baixa, override-rate baixo) e **demoção automática em anomalia**. Deixou a *composição* — o `autonomy.Controller` a ser chamado — explicitamente como trabalho de engenharia fora do ADR, deferida em `DEF-908`. O AOS-090 compõe-a. Ao fazê-lo, três factos medidos obrigam a decisões que o ADR-014 não tomou, e é isso que este ADR fixa.

**Facto 1 — não há sinal de fiabilidade honesto hoje.** O selo de mediação do WORM é escrito **antes** do efeito (`audit-before-effect`, invariante deliberado — `monitor.go`), com `Effect=Permit` e **sem campo de resultado de execução**; o `step.ledger.applied` do Event Store só é escrito em **sucesso** (o `runEffect` devolve antes do `Append` quando a tool falha). Logo a taxa de erro derivada de qualquer trilho existente é **cega ao erro de execução** — seria sempre ~0. O override-rate tem maquinaria (`hitl.Channel`) mas **não está composto** no nó. Em consequência, a `autonomy.ReliabilitySource` não tem implementação de produção e o `Controller.Evaluate` com `src==nil` **nunca promove** (fail-safe). Promover sobre um número cego seria decidir uma elevação sobre ruído.

**Facto 2 — a chave do nó vive no mesmo disco do WORM.** O `EntryHash` da hash-chain é um SHA-256 **sem chave**; quem escreve o ficheiro do WORM consegue apender um registo bem-formado que a re-verificação aceita. Uma assinatura do NÓ sobre uma transição não é fronteira nenhuma contra esse adversário. Portanto, **um registo que ELEVA a autonomia não pode ser durável sem uma assinatura de OPERADOR** — senão a rehidratação é a porta das traseiras da elevação por forja.

**Facto 3 — a unidade estável é a classe.** Os `agent_id` são cunhados por run; o PDP resolve `instância → classe → piso` (`LevelForAgentOrClass`). Uma decisão do controlador sobre a instância é efémera e, por sombrear a classe, pode elevar o efectivo.

## Decisão

### 1. Registo de desfecho pós-efeito (aditivo; NÃO enfraquece o audit-before-effect)

O nó passa a registar o **desfecho** de cada tool call **depois** do efeito, num evento próprio do Event Store (`tool.call.outcome`), emitido pelo Reference Monitor a seguir ao `dispatch`. O evento sela `{run_id, step_id, tool_id, agent_class, capability, resource, outcome ∈ {ok, error}, error_code?}` — **rótulos e números, nunca segredos nem o input/output da tool**.

O invariante **audit-before-effect mantém-se intacto**: o selo da DECISÃO continua a ser escrito ANTES do efeito, no WORM tamper-evident. Este é um **segundo** registo, DEPOIS do efeito, e a sua semântica de falha é OPOSTA à do selo de decisão:

- Falhar a gravar o **selo de decisão** (permit) degrada a decisão para **DENY** (fail-closed de enforcement) — inalterado.
- Falhar a gravar o **desfecho** NÃO muda decisão nenhuma (o efeito já aconteceu): degrada a **medição de fiabilidade**, não o enforcement. É registado como lacuna de medição, e a `ReliabilitySource` trata uma janela com buracos como `WindowOK=false` (não promove).

O desfecho vai para o Event Store (não para o WORM): não é uma prova de responsabilização (o WORM já sela quem autorizou o quê), é telemetria de fiabilidade agregável por (classe, domínio).

### 2. `ReliabilitySource` de produção

Uma implementação agrega, por (classe de agente, domínio) e sobre a janela da política, os desfechos de §1 (taxa de erro) e o override-rate (do `hitl.Channel`, composto no nó). Devolve `Reliability{ErrorRate, OverrideRate, WindowOK}`. `WindowOK` exige cobertura suficiente da janela (ex.: 30 dias de amostras); uma janela incompleta ou com buracos ⇒ `WindowOK=false` ⇒ **não promove** (conservador).

### 3. Promoção — o que passa a ser exigível

- A promoção é **automática abaixo de L4**: quando a fiabilidade sustentada é satisfeita (CA1), o `Controller.Evaluate` sobe **um nível de cada vez**, até um tecto que **nunca cruza L4**. Chegar a L4/L5 exige a **cerimónia assinada** (dual-control, AOS-305/AOS-377) — o controlador NUNCA auto-promove para lá do limiar em que `danger` deixa de esperar por um humano. Isto fecha a via de contorno do dual-control.
- A promoção aplica-se **em memória** na incarnação viva (a decisão do controlador vivo é fiável — não é forja) e é **selada** para audit (AC4). Mas **não é durável através de reinício**: o invariante de direcção da rehidratação (ver §5 e o núcleo do AOS-090) **recusa** qualquer registo do controlador que ELEVE. No arranque, o par reverte ao último nível **assinado** (a linha de base humana). É a leitura conservadora e honesta do Facto 2: autonomia ganhada por máquina não persiste sem assinatura, e reiniciar repõe a base ratificada por uma pessoa.
- Durabilidade de uma promoção (para qualquer nível) continua a ser a decisão de provisionamento **assinada** — ADR-014 §63.

### 4. Demoção — inalterada face ao núcleo do AOS-090

A demoção é **automática, imediata, durável e por CLASSE**, sem gate humano (ADR-014 §2). É a metade de **segurança**, e é a que persiste sem assinatura — porque só pode **descer**.

### 5. Os invariantes de direcção da rehidratação (o que torna §3 e §4 seguros sem custódia de chave)

Um registo cujo actor é o `autonomy-controller`, na rehidratação:
- só é aceite se a chave for de **classe** (`ClassPrefix`) — um registo de instância com este actor é forja e é recusado;
- só é aceite se **DESCER** face ao nível reconstruído até esse ponto — um registo que sobe (promoção) ou se mantém é recusado.

Assim uma demoção sobrevive a reinício sem assinatura (só desce), e uma promoção não (sobe) — a assimetria é a defesa, e não depende de custódia de chave.

## Consequências

### Positivas
- A promoção passa a ser **medível e automática** abaixo de L4, sobre um sinal **honesto** de fiabilidade — fecha a metade que faltava do `DEF-908` no que é seguro automatizar.
- O dual-control de L4/L5 fica **preservado** contra auto-promoção; a elevação por forja fica fechada em ambos os caminhos (runtime e rehidratação).
- `audit-before-effect` mantém-se; o desfecho é um registo **aditivo**, com semântica de falha explícita e distinta.

### Negativas / Trade-offs
- **Promoção não persiste através de reinício**: uma classe promovida por fiabilidade volta à base assinada no arranque e tem de re-ganhar o nível. É deliberado (Facto 2) e declarado — o custo de não ter uma raiz de confiança fora do disco do WORM no modo de referência.
- **O read-path ganha um registo pós-efeito**: mais uma escrita por tool call no Event Store. Mitigado por ser fail-open na medição (nunca degrada enforcement) e por não conter payload.

## Alternativas consideradas
- **Promoção durável sem assinatura** (o registo do controlador eleva e persiste): rejeitada — é o vector de elevação por forja do Facto 2.
- **Promoção durável assinada pela chave do nó**: rejeitada — a chave partilha o disco do WORM, logo não é fronteira contra quem escreve o ficheiro.
- **Derivar a taxa de erro do selo de decisão existente**: rejeitada — é cego ao erro de execução (Facto 1); um sinal verde que nunca dispara é o «teste a passar pela razão errada, na direcção perigosa».
- **Deixar a promoção manual (não fazer §1–§3)**: é o estado do ADR-014 §63; rejeitada aqui porque o objectivo do AOS-090 é a promoção medida — mas é a alternativa de recuo se a alteração ao read-path não for ratificada.

## Conformidade / Enforcement
- **Desfecho pós-efeito**: `packages/kernel/reference-monitor/` (emissão após `dispatch`), tipo de evento novo em `packages/substrate/eventstore` / o vocabulário de mediação; fail-open na medição, fail-closed no enforcement (inalterado).
- **`ReliabilitySource` de produção**: `packages/control-plane/governance/autonomy/` (agregador) + composição em `packages/cmd/aos`.
- **Promoção abaixo de L4 + tecto**: `autonomy.Controller.Evaluate` + `AutonomyControlConfig.promotionCeil` capado abaixo de L4 no nó.
- **Demoção durável por classe + invariantes de direcção**: `autonomy.LevelRegistry.Rehydrate` (`ErrControllerExigeClasse`, `ErrControllerDeveDescer`) + `packages/cmd/aos/autonomy_anomalia.go` (AOS-090, já composto).

## Referências
- ADR-014 (taxonomia L0–L5; §2 semântica, §5 `DEF-908`, §63 promoção manual), ADR-013 (SA-ROC), ADR-010 (audit WORM), ADR-023 (escritor único / lease).
- AOS-090 (o controlador e a sua composição), AOS-080 (disjuntor multi-sinal), AOS-095 (override-rate / hitl), AOS-305/AOS-377 (dual-control), AOS-307 (rehidratação selada).
- `DEF-908` — `docs/governance/REGISTO-Deferimentos.md`.
