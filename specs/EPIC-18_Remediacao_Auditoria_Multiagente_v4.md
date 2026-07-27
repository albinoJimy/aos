# EPIC-18 — Remediação dos Achados da Auditoria Multiagente v4

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | EPIC-18 — Remediação dos Achados da Auditoria Multiagente v4 |
| Versão | 1.0 |
| Data | 2026-07-26 |
| Estatuto | **PROPOSTA** — carece de ratificação do dono (ver §4 e o DoR em §9) |
| Epic anterior | `specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md` |
| Fontes de verdade | `analises/08_Relatorio_Auditoria_Multiagente_v4.md`, `specs/00_AOS_Carta.md`, `specs/00_System_Spec.md`, `docs/runbooks/RB-Auditoria_Multiagente_CartavCodebase.md` |
| Intervalo de tickets | **AOS-190 … AOS-211** (22 tickets). AOS-204 acrescentado por AOS-192 (eixo residual de VAC-01); **AOS-205…209 acrescentados pelo registo de deferimentos e pela análise STRIDE** (§8-bis) — não são remediação, são o trabalho substantivo que os deferimentos apontavam sem executor; **AOS-210 acrescentado por AOS-204** (o residual nomeado em §6.3 do relatório de aceitação sistémica, que a própria §6.3 declarava «sem dono atribuído»); **AOS-211 acrescentado por AOS-210** (os dois atributos que faltam ao `aos.activity` que AOS-210 pôs na árvore exportada — encaminhar em vez de voltar a nomear sem dono) |

---

## 1. Contexto

A auditoria multiagente **v4** (2026-07-26, `analises/08_Relatorio_Auditoria_Multiagente_v4.md`) correu 8 perspectivas
deliberadamente ortogonais às 12 da v3, seguidas de contra-exame adversarial. Dos 78 achados brutos, 30 eram
CRÍTICO/ALTO e **29 sobreviveram** à refutação (8 ALTO, 21 MÉDIO). **Nenhum achado sobreviveu como CRÍTICO.**

O veredicto é **AMARELO-ESCURO**: melhoria real face ao VERMELHO da v3, mas o eixo central mantém-se —
*a remediação da v3 foi efectiva na documentação, nos gates e nas bibliotecas; **não no nó `aos`***.

Dois factos enquadram este epic:

1. **Não houve fraude de checkbox.** Nenhum dos oito auditores encontrou um único CA marcado sobre trabalho
   inexistente. Os quatro tickets que fechariam os achados mais graves da v3 (AOS-181/182/183/184) têm **todos**
   os CA honestamente `[ ]`. Os desvios encontrados são de **ênfase** (títulos de commit, `doc.go`) e de
   **activação** — não de falsificação de estado. Este epic remedeia activação e verificabilidade, não desonestidade.

2. **A classe de defeito dominante é nova e específica:** *capacidade implementada, correcta ao nível do
   componente, e **inalcançável no artefacto entregue***. Não é «falta implementar» — é «está implementado e
   não há forma de o ligar». O custo de fecho é tipicamente de poucas linhas; o custo de **não** fechar é uma
   documentação que descreve um produto que o binário não é.

## 2. Objectivo

Tornar **verificável** o que a v3 tornou **documentado**. Em concreto:

- ligar à CI que bloqueia merges os gates anti-recorrência que a EPIC-17 criou mas não activou;
- dar superfície de configuração às capacidades que existem no código e são inalcançáveis pelo binário;
- corrigir as provas que não provam (teste vacuoso, evidência citada errada) antes que sustentem critérios verdes;
- reparar os artefactos de auditoria que deixaram de auditar (STRIDE, matriz de conformidade, modelo de eventos, gate 4);
- pôr os deferimentos num registo único com eixo, dono e gatilho falsificáveis.

## 3. Não-objectivos

- **Não** re-litigar decisões FIXAS da Carta §4 (as correcções de registo de ADRs fazem-se por **emenda §7**, ver §4).
- **Não** reescrever componentes entregues. Todo o trabalho é **activação, verificabilidade e correcção documental**.
- **Não** implementar o que está legitimamente deferido com eixo nomeado (sandbox real, HSM, IdP corporativo, HA).
- **Não** substituir a EPIC-17: os seus tickets P0/P1 por executar (AOS-181/182/183/184) mantêm-se válidos e
  **prioritários** sobre este epic, sujeitos ao aviso de sequenciamento em §5.

## 4. Decisões do dono requeridas (NÃO são tickets de engenharia)

Três itens da auditoria **não podem** ser fechados por um ticket. São decisão/governação e ficam bloqueados até o dono agir:

| # | Decisão | Porquê não é ticket | Achado |
|---|---|---|---|
| **D-18.1** | **Fechar o DoR item 3 da EPIC-17** — «v1 zero-dep-com-stubs vs real-wiring» — com dono e escalada, e registá-la na Carta §4.2 | Condiciona os quatro tickets P0/P1 que fecham o DoD da v1 e nunca foi decidida; vive numa checkbox no fundo de um epic não ratificado. Carta §6.3 exige dono e escalada para decisões ABERTAS | PLA (governação a montante) |
| **D-18.2** | **Ratificar ou retirar a EPIC-17** e marcar os critérios de saída já demonstravelmente satisfeitos | Oito tickets estão em trunk contra um plano com Estatuto «PROPOSTA» | PLA |
| **D-18.3** | **Inscrever ADR-003, ADR-018 e ADR-019 na tabela §4.1 da Carta** e corrigir o cabeçalho «todas FIXAS» | Alterar a Carta exige **emenda datada** (§7) com arbitragem §6.5 — não é entregável de engenharia | PLA-02, COE-03 (agravado) |

> **Nota de coerência:** a mesma regra aplica-se a este epic. O seu Estatuto é **PROPOSTA**; executá-lo antes de
> ratificado repetiria exactamente o padrão que a v4 apanhou na EPIC-17.

## 5. AVISO — sequenciamento crítico de segurança

Isto **não é uma recomendação, é uma restrição de ordem**. Extraído de `analises/08…v4.md` §8:

> **AOS-181** (carregar bundle PDP real) **NÃO deve** ser executado antes de **AOS-183** (activar TaintGate) e da
> correcção de **CON-04** (origem de `Resource.Region`).

**Racional:** hoje o `pdp.NewUnloaded()` (deny-all) e o catálogo de tools vazio **mascaram** três lacunas —
STR-02 (separação control/data-plane deferida, apoiada num TaintGate inerte), STR-03 (tools despachadas
in-process, sem sandbox) e CON-04 (`Resource.Region` proveniente da fronteira *untrusted*, sem a invariante que
protege `AuthorizationTaint`). Carregar a política **antes** de ligar a barreira de taint e de fixar a origem da
região transforma um nó *seguro-mas-inerte* num nó **permissivo com a defesa estrutural desligada e com a região
do recurso ditada pela saída do modelo**.

Ordem obrigatória: **AOS-183 → correcção de CON-04\* → AOS-181**.
*(\* a correcção de CON-04 pertence ao âmbito de AOS-182 na EPIC-17; se for separada, recebe número próprio —
ainda **não atribuído**, pelo que não é citado aqui: um `AOS-NNN` inexistente quebraria o gate `ref-lint`.)*

## 6. Critérios de saída do epic

Todos falsificáveis por comando — é condição de aceitação que cada um seja verificável sem juízo humano:

- [ ] **Os gates anti-recorrência bloqueiam merges**: `layer-lint`, `rtm` e `ref-lint` aparecem em
      `.github/workflows/ci.yml` **e** no `needs:` do agregador `gates`; um PR com inversão de camada torna
      `gates` vermelho (prova negativa executada e registada).
- [ ] **Nenhuma capacidade documentada como entregue é inalcançável pelo binário**: para cada `Config.*` que a
      documentação apresente como activável, existe caminho de configuração (env/ficheiro/flag) e o banner de
      arranque regista o estado resultante.
- [ ] **Nenhum critério sistémico (§13) verde assenta em prova vacuosa**: cada prova citada em
      `docs/reports/AOS-169-aceitacao-sistemica.md` resiste à *prova negativa* (partir o mecanismo torna o teste vermelho).
- [ ] **Os artefactos de auditoria auditam**: `tecnica/17` (STRIDE) tem rastreabilidade ticket correcta e cobre a
      superfície real (HTTP/SSE/DSAR/OTLP/contentor); `tecnica/14` não marca «Coberto» nenhuma linha cujo
      mecanismo não seja alcançável a partir de `packages/cmd/aos`; o gate 4 (Integração) existe ou a sua
      declaração é retirada de `specs/01:83` e `tecnica/12:351`.
- [ ] **Todo o deferimento tem eixo válido**: cada marcador `DEFERIDO`/`demo-grade` em `packages/**/*.go`
      (não-teste) tem entrada num registo único com eixo que **contenha um ticket real**.
- [ ] **O tripwire da Carta §6.6 é instrumentável**: existe registo de decisões FIXAS reabertas e de arbitragens §6.5.
- [ ] **Os limiares de gate têm piso**: `EVAL_PASS_RATE_MIN=0 make ci` **falha** por violação de piso.
- [ ] Todos os achados ALTO da v4 estão encerrados ou convertidos em ticket justificado com dono.

## 7. Tabela resumo de tickets

| ID | Título | Tipo | Est. | Prio | Achado | Bloco |
|---|---|---|---|---|---|---|
| AOS-190 | Ligar `layer-lint`/`rtm`/`ref-lint` à CI que bloqueia merges — **ENTREGUE** (5/5 CA; elo agregador→merge = *branch protection*, configuração de plataforma fora da árvore — ver §0 do relatório de prova negativa) | fix | **S** | **P0** | PLA-01 | A |
| AOS-191 | Superfície de configuração para `DurableExecution` (`AOS_DURABLE_EXECUTION`) — **ENTREGUE** (5/5 CA; semântica fail-closed **sempre** sobre `AOS_EVENTSTORE_PATH`; postura de produção deferida com eixo em AOS-203) | feature | **S** | **P0** | REG-01 ≡ STR-09 ≡ PLA-03 | B |
| AOS-192 | Corrigir o teste de aceitação vacuoso de AOS-180 e reabrir §13.3 — **ENTREGUE** (5/5 CA; §13.3 reaberto e **re-marcado VERDE** com evidência nova ao nível do nó + prova negativa executada; §13.1/§13.7 mantêm-se VERDES com a citação corrigida; §13.6 **REABERTO 🟡** por falta de prova, com dono em AOS-204) | fix | **S** | **P0** | VAC-01 | C |
| AOS-193 | Caminho de configuração para `Operators`/`Approvers` (plano de controlo operável) — **ENTREGUE** (5/5 CA; `AOS_OPERATORS` por env + `AOS_APPROVERS_FILE` por ficheiro montado, ambos fail-closed; bind-guardrail passa a exigir ≥1 operador — mudança de comportamento declarada; prova positiva e negativa executadas no **contentor real**) | feature | M | **P0** | ORF-02, STR-04 | B |
| AOS-194 | Corrigir rastreabilidade do STRIDE e cobrir a superfície real do nó — **ENTREGUE (3/5 CA)**; residual: órfandade + automatização (→ AOS-198) | docs | M | P1 | STR-01, STR-06 | D |
| AOS-195 | Corrigir a regressão documental de `redaction/doc.go` e reabrir o CA de AOS-188 | fix | **S** | P1 | VAC-02 ≡ DEF-02 ≡ CON-03 | C |
| AOS-196 | Registo único de deferimentos + correcção dos eixos inválidos | feature | M | P1 | DEF-01, DEF-03, DEF-06 | E |
| AOS-197 | Reclassificar a matriz de conformidade e nomear as lacunas de âmbito — **ENTREGUE (3/4 CA)**; residual: eixo/dono do legal hold (decisão do dono) | docs | M | P1 | CON-01, DEF-01 | D |
| AOS-198 | Criar o «gate 4 — Integração» (ou retirar a sua declaração) | feature | M | P1 | DAT-09 | A |
| AOS-199 | Pisos aos limiares de gate sobreponíveis por ambiente | fix | **S** | P1 | ORF-06 | A |
| AOS-200 | Instrumentar o tripwire da Carta §6.6 e o registo de arbitragens §6.5 | feature | S | P2 | DEF-07 | A |
| AOS-201 | Reconciliar `tecnica/13` (envelope e catálogo de eventos) com o código — **ENTREGUE (2/3 CA + 1 parcial)**; residual: gate de CI do catálogo (→ AOS-198) | docs | M | P2 | DAT-01, DAT-02, DAT-03 | D |
| AOS-202 | Decidir o destino dos módulos `*/contract` órfãos (1763 LOC, 0 importadores) | chore | S | P2 | ORF-01 | E |
| AOS-203 | Documentar as variáveis de ambiente do nó e endurecer o kill-switch de soberania | fix | M | P1 | ORF-03/04/05 | B |
| AOS-204 | Exportar por OTLP, a partir do nó real, a árvore de um run **com tool call** (ramo `execute_tool`) | fix | **S** | P1 | VAC-01 (eixo residual de §13.6) | C |

**Blocos:** **A** — anti-recorrência (o mecanismo que impede a deriva voltar); **B** — operabilidade do nó
(capacidade inalcançável); **C** — provas que não provam; **D** — artefactos de auditoria que não auditam;
**E** — higiene de deferimentos.

**Caminho crítico:** AOS-190 primeiro, sempre. Sem ele, todo o restante trabalho deste epic (e os fechos da v3)
pode ser silenciosamente desfeito no PR seguinte.

---

## 8. Tickets

### AOS-190 — Ligar `layer-lint`/`rtm`/`ref-lint` à CI que bloqueia merges

**Achado:** PLA-01 (ALTO, NOVO). `.github/workflows/ci.yml` invoca 20 gates via `bash scripts/ci/*.sh` e
**nenhum** é `layer-lint.sh`, `rtm.sh` ou `ref-lint.py`; o agregador `gates` (`:266`) tem 18 `needs:` sem nenhum
deles. Existem apenas em `scripts/ci/run.sh:30` e `Makefile:71-78`. O self-test §L corre `layer-lint` contra uma
árvore sintética (`--root "$LAYER_TMP"`), nunca contra `packages/`. Isto contradiz `CONTRIBUTING.md:6,73` e
`AGENTS.md:211` («qualquer gate vermelho bloqueia merge»).

**Impacto:** os três mecanismos criados pela EPIC-17 para impedir a reincidência dos achados da v3 **não impedem
nada**. Um PR com inversão de camada ou RTM desactualizada obtém `gates` verde.

**Critérios de aceitação**
- [x] `grep -c "layer-lint\|rtm\|ref-lint" .github/workflows/ci.yml` ≥ 3. *(14 ocorrências; três blocos de job
      reais em `ci.yml:72-107`, não só comentários.)*
- [x] Os três jobs aparecem no `needs:` do agregador `gates` (`ci.yml`, 21 entradas, todas resolvidas para jobs
      existentes).
- [x] `layer-lint` corre contra `packages/` (não contra árvore sintética) no job de CI: o job invoca
      `bash scripts/ci/layer-lint.sh --root "$GITHUB_WORKSPACE"`, e `--root` tem precedência sobre a variável
      `LAYER_LINT_ROOT` que o self-test §L usa. O §L mantém-se como teste complementar, não substituto.
- [x] **Prova negativa registada** em
      [`docs/reports/AOS-190-prova-negativa-gates-anti-recorrencia.md`](../docs/reports/AOS-190-prova-negativa-gates-anti-recorrencia.md):
      violação injectada, comando, output literal, código de saída e reversão, para cada um dos três gates.
      **Ressalva de honestidade — ler o §0 do relatório:** o texto deste CA pressupõe um «PR de teste», que
      **não existe nem pode existir** (não há remote configurado, logo não há execução do workflow no GitHub
      para observar). A prova executada é **local ao nível do script** (violação ⇒ `exit != 0`); o elo
      script→job→agregador é verificado por **inspecção estática** (`needs:` do agregador + `if: always()`),
      e o elo agregador→merge é configuração de *branch protection*, fora da árvore.
      *(Colateral necessário: `gates` foi endurecido com `if: always()` + avaliação de `needs.*.result`. Sem
      isso o agregador seria **saltado** — conclusão `skipped`, que a branch protection trata como passagem —
      e este CA seria literalmente impossível de satisfazer: `gates` não conseguiria ficar vermelho. Também
      se fechou um fail-open no gate `rtm`, cujas contagens eram literais e não detectavam o crescimento do
      corpus — ver §4 do relatório.)*
- [x] A baseline de `layer-lint` deixa de dizer «Serão resolvidas pelo ticket AOS-179» nas inversões que o
      ADR-019 decidiu **legitimar** (texto desalinhado com a decisão). *(`scripts/ci/baseline/layer-lint-exceptions.txt`;
      só linhas de comentário mudaram — as chaves `pacote|import` são idênticas, a superfície tolerada não aumentou.)*

---

### AOS-191 — Superfície de configuração para `DurableExecution`

**Achado:** REG-01 ≡ STR-09 ≡ PLA-03 (ALTO, v3-AINDA-ABERTO — DUR-01). `bootstrap.go:186` declara
`DurableExecution bool`; `:410` e `:580` são os únicos consumidores; `main.go:119-148` nunca o escreve;
`grep AOS_DURABLE .` → **0**; o único escritor em toda a árvore é `bootstrap_durable_execution_test.go:129`.
Agravante: `bootstrap.go` é `package main`, pelo que **nem um embedder externo** o pode preencher.

**Impacto:** `tecnica/02:~465` afirma que a execução durável está «exposta no nó `aos`». O código existe e está
correcto; é **inalcançável**. DUR-01 da v3 continua aberto na prática.

**Critérios de aceitação**
- [x] `AOS_DURABLE_EXECUTION` (padrão de `AOS_EVENTSTORE_PATH`) é lida em `main.go` e escreve `Config.DurableExecution`.
      *(`nodeConfigFromEnv` — `main.go`: `parseDurableExecution(os.Getenv("AOS_DURABLE_EXECUTION"))` → `Config{… DurableExecution: durableExecution …}`.
      `run` foi refactorizada para chamar `nodeConfigFromEnv`, tornando a costura env→Config testável. Parser
      fail-closed no padrão de `parseBoardRegions`: `1/true/t/yes/y/on` ligam, `0/false/f/no/n/off`/vazio desligam,
      **lixo aborta** com `ErrBadDurableExecution` em vez de degradar para `false`.)*
- [x] O banner de arranque regista se checkpointer/capturer/step-ledger estão compostos.
      *(`bootstrap.go`: a condição do banner é `checkpointer != nil && capturer != nil && ledger != nil` — declara o
      estado **realmente composto**, não a intenção da config — e nomeia o substrato via `describeSubstrate`. **Nota
      deliberada de retro-compatibilidade:** o caminho por omissão passa a emitir **uma linha nova**
      (`execucao duravel (AOS-180): DESLIGADA — …`). É exigido por este CA e prevalece sobre a leitura literal de
      «byte-a-byte», que se aplica à **composição** (os três continuam `nil`, exit 0, run inalterado); registado aqui
      para que uma auditoria futura não o leia como regressão.)*
- [x] `deploy/node/README.md` documenta a variável, o seu efeito e a interacção com `AOS_EVENTSTORE_PATH`.
      *(Secção «Estado durável — variáveis de ambiente»: tabela `AOS_EVENTSTORE_PATH`/`AOS_WORM_PATH`/
      `AOS_DURABLE_EXECUTION` com valores aceites, default e efeito; regra fail-closed **sempre** (não só em
      produção); ressalva explícita do que a guarda **não** detecta (um caminho fora do mount `-v aos-data:/var/lib/aos`,
      p.ex. sob `--tmpfs /tmp`, passa a guarda e continua volátil — resíduo só fechável por documentação); as duas
      linhas de banner como verificação do operador; receita de produção actualizada com os três `-e`.)*
- [x] Teste: um nó arrancado com a variável activa compõe os três; sem ela, permanecem `nil` (comportamento actual).
      *(`packages/cmd/aos/durable_execution_env_test.go`. Positivo ao nível de `Bootstrap`:
      `TestAOS191_DurableExecutionReachableFromEnv` assere os **tipos concretos** (`*durable.EventStoreCheckpointer`,
      `*replay.EventStoreCapturer`) e o **efeito no substrato** (`step.ledger.applied` + `step.checkpoint` no stream do
      run). Positivo ao nível do **entrypoint**: `TestAOS191_RunComposesDurableExecutionFromEnv` corre `run` e sela o
      elo `run`→`Bootstrap` no caminho feliz. Negativos: colaboradores `nil` e **ausência** desses eventos sem a
      variável; `run` a declarar `DESLIGADA`; `ErrBadDurableExecution` para lixo; `ErrDurableExecutionNeedsDurableSubstrate`
      no entrypoint e no composition-root. `go test -race -count=1 ./` verde.)*
- [x] **Emenda ao AC de AOS-180** (ou nota no seu DoD) registando que «quando configurado» exigia superfície de
      configuração — o defeito era de *suficiência do critério*, e deve ficar registado para não se repetir.
      *(`specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md`, secção AOS-180: bloco «EMENDA AOS-191» + linha nova de
      DoD. Regra registada: um CA de wiring TEM de nomear a **via de activação no artefacto** e a sua documentação de
      operador, não só o campo de `Config`.)*

**Semântica decidida — `AOS_DURABLE_EXECUTION` sem substrato durável.** Opção **(a) fail-closed SEMPRE**, a mais
estrita das três em aberto. A execução durável compõe-se **sobre** o Event Store; sem `AOS_EVENTSTORE_PATH` o store
é in-memory e checkpoints/capturas/ledger evaporariam no reinício — durabilidade **anunciada e não cumprida**, que é
a própria classe de defeito desta auditoria. Guarda canónica no composition-root
(`ErrDurableExecutionNeedsDurableSubstrate`, avaliada **antes** de abrir qualquer store) + guarda na fronteira de
ambiente. Recusar só em `AOS_MODE=production` (opção b) deixaria a promessa falsa de pé em staging, que é onde ela
seria acreditada; avisar apenas (opção c) contradiz `ErrBadBoardRegions`, onde config auto-contraditória aborta
sempre. **Fronteira declarada, não escondida:** um `EventStore` injectado por `Config` não dispara a guarda (a sua
durabilidade é do chamador e o nó não a pode atestar) — o banner **declara-o**.

**Deferimento com eixo.** `AOS_MODE=production` **não** passa a exigir execução durável: não há promessa falsa (o
banner declara `DESLIGADA`) e exigi-la quebraria a retro-compatibilidade que este ticket impõe. A promoção a
exigência de produção, a par de `ErrProductionNeedsHardenedIdentity`/`ErrProductionNeedsSovereignRead`, é decidida
em **AOS-203**. Registado no código (`main.go`, bloco de `AOS_DURABLE_EXECUTION`) e no README do nó.

---

### AOS-192 — Corrigir o teste de aceitação vacuoso de AOS-180 e reabrir §13.3

**Achado:** VAC-01 (ALTO, reclassificado de CRÍTICO, NOVO). `bootstrap_durable_execution_test.go:133` cria **uma**
instância de `twoTurnToolModel` partilhada pelas duas vidas do nó (`:159`, `:193`); o contador é monotónico
(`:85`), logo a 2.ª vida entra em `turn=3` e devolve `Final` **sem emitir tool call**. A asserção `execs == 1`
(`:217-219`) passa porque a tool **nunca foi re-tentada** — não porque o ledger deduplicou.
`durable/resume.go:60-95` confirma que o `ResumePoint` não carrega a invocação.

**Impacto:** §13.3 (DURABILIDADE) está marcado **VERDE** em `docs/reports/AOS-169-aceitacao-sistemica.md:77-107`
com base nesta prova. A propriedade **está** provada ao nível do componente
(`durable/step_ledger_test.go:142,214,270,314,481`) — falta a prova ao nível do **nó**.

**Critérios de aceitação**
- [x] A 2.ª vida do nó usa uma instância **nova** do modelo e emite ≥1 tool call. *(`restartToolModel`,
      uma instância por vida; asserção explícita `model2.emitted() >= 1`.)*
- [x] A asserção passa a verificar que o ledger devolveu «already-applied», não apenas `execs == 1`.
      *(`StepLedger.Applied(key)` é **falso** antes de `RebuildLedger` e **verdadeiro** depois, com o
      resultado canónico; o efeito da 2.ª vida fica a zero; a tool re-registada devolve bytes
      diferentes que NÃO aparecem no prompt seguinte; o WAL mantém um só `step.ledger.applied`.)*
- [x] **Prova negativa:** partir `StepLedger.Apply` torna o teste **vermelho** (registar o output).
      *(Executada e registada em `docs/reports/AOS-169-aceitacao-sistemica.md` §3.0: teste corrigido
      `--- FAIL` exit 1; a réplica da forma antiga `--- PASS` exit 0 com o mesmo mecanismo partido —
      demonstração empírica da vacuidade. Alteração revertida.)*
- [x] `AOS-169-aceitacao-sistemica.md` §13.3 fica **reaberto** (🟡) até este ticket fechar, e é re-marcado com a
      evidência nova. *(Reabertura e re-marcação registadas no corpo (§3.0-§3.2) e na tabela-resumo,
      coerentes entre si.)*
- [x] Revisão dos outros três eixos com evidência citada errada (§13.1 cadeia sem hook de revalidação;
      §13.6 `execute_tool` com modelo que não emite tool call; §13.7 KEK que nunca cifrou nada) — corrigir a
      citação ou reabrir o eixo. *(§13.1 e §13.7: citação corrigida, eixos permanecem VERDES com a
      âncora certa. §13.6: a citação era FALSA e o que falta é PROVA — eixo **REABERTO 🟡** com o eixo
      nomeado.)*

---

### AOS-193 — Caminho de configuração para `Operators`/`Approvers`

**Achado:** ORF-02 (ALTO, NOVO). `grep "Operators\|Approvers" packages/cmd/aos/*.go` (não-teste) devolve apenas
declarações (`bootstrap.go:140-152`), consumos (`:520`,`:530`), logs (`:660`,`:662`) e um comentário
(`main.go:145`) — **zero caminhos de leitura**. Consequência: `steer_authenticator.go:172-178` devolve
`ErrUnknownEmitter` a **todo** o steer/pause e `api.go:684-686` devolve **sempre 501** ao approve. Como
`Config`/`Bootstrap` estão em `package main`, registar uma pubkey exige **forkar e recompilar**.

**Impacto:** dois dos seis subcomandos da CLI (`steer`, `pause`) e dois endpoints de controlo são inatingíveis no
binário entregue, apesar de `specs/EPIC-15:440` e `:490-491` os darem como CA `[x]`. `deploy/node/README.md:46-47`
descreve uma condição de guardrail («identidade real **+ operadores**») que o código não impõe — relacionado com
STR-04, onde `controlAuthenticated()` (`api.go:909-914`) é identicamente verdadeiro.

**Critérios de aceitação**
- [x] Existe caminho de configuração para pubkeys de operador (ficheiro ou env, no padrão de `AOS_ISSUER_PUBKEY`).
      — `AOS_OPERATORS="emitterID=hexpubkey,…"` (`packages/cmd/aos/main.go`, `parseOperators` + `ErrBadOperators`),
      gramática de `AOS_BOARD_REGIONS` com o valor codificado como `AOS_ISSUER_PUBKEY`. Fail-closed: entrada sem
      `=`, `emitterID` vazio, pubkey não-hex/de tamanho errado, `emitterID` **duplicado** ou **a mesma pubkey em
      dois `emitterID`s** ABORTAM o arranque (a partilha de chave não eleva privilégio no steer — destrói a
      **atribuição**: um sinal assinado por A seria selado no WORM como sendo de B).
      `Bootstrap` recusa ainda (`ErrBadOperatorEntry`) qualquer entrada que o `Register` descartaria em silêncio,
      e as mesmas colisões de material de chave — `Config` é alcançável **in-process**, não só por ambiente.
- [x] Existe caminho equivalente para `Approvers` (4-eyes) ou fica declarado como deferimento **com ticket**.
      — **IMPLEMENTADO**, não deferido: `AOS_APPROVERS_FILE` (ficheiro JSON montado, ADR-017 ponto 2), com
      `parseApproversFile` + `ErrBadApproversFile` (ficheiro ilegível, JSON inválido, **campo desconhecido**,
      lista vazia, principal duplicado, pubkey inválida ou autoridade vazia ABORTAM). Escolheu-se ficheiro — e não
      env — porque `ApproverConfig` não é escalar (`Authority []string`) e uma env exigiria um terceiro nível de
      delimitador; a coerência com `AOS_OPERATORS` mantém-se na codificação do material público e na disciplina
      fail-closed. Com ele, `POST /runs/{id}/approve` deixa de responder sempre `501`
      (`TestBootstrapComposesFourEyesFromConfig`) e **autoriza de facto** um pedido dual-control legítimo
      (`TestApproversFileAuthorizesDualControlEndToEnd`: roster do ficheiro → `Bootstrap` → handler real → `200`
      com os dois aprovadores; uma perna assinada pela chave errada ⇒ `403`).
      **Duas guardas de invariante** que o roster montado torna necessárias (o ficheiro é copy-paste-ável, ao
      contrário do fork+recompile anterior): (a) **duas entradas com a mesma pubkey ABORTAM** — a distinção do
      `authorizeDual` é sobre `approver`/`session`/`credential`, três *strings* escolhidas pelo **cliente**, pelo
      que a pubkey pinada é a **única** âncora criptográfica de "duas pessoas" e a sua partilha faria **uma** chave
      privada satisfazer o 4-eyes com o banner a declarar "2 aprovador(es) pinados"; (b) `authority` é validada
      contra o **vocabulário fechado** `approve:{safe,gray,danger}` (derivado de `hitl.RequiredAuthority`, não de
      uma lista literal) — a comparação em runtime é de *string* exacta, logo um *typo* daria um aprovador contado
      no banner que nunca aprova nada. Ambas valem também em `Bootstrap` (`ErrBadApproverEntry`), que passa ainda a
      recusar o **principal duplicado** que o `MemApproverRegistry.Register` sobreporia em silêncio.
- [x] **Prova positiva:** um nó lançado por `deploy/node/Dockerfile` aceita um `aos steer` assinado.
      — [`deploy/node/aos193-control-plane-harness.sh`](../deploy/node/aos193-control-plane-harness.sh) **EXECUTADO
      e VERDE** contra a imagem distroless real: `aos steer` assinado pelo operador registado em `AOS_OPERATORS` é
      ACEITE (`steer enviado a run-aos193-control`); um emissor não registado leva `403`. Equivalente in-process:
      `TestEnvConfiguredOperatorSteerAcceptedEndToEnd` (seed → `aos operator-pubkey` → `AOS_OPERATORS` →
      `nodeConfigFromEnv` → `Bootstrap` → handler HTTP real → `aos steer` → `SteerChannel`).
- [x] **Prova negativa:** bind a `0.0.0.0` com **zero** operadores registados **recusa** (`ErrRefuseNonLoopbackBind`)
      — ou seja, `controlAuthenticated()` passa a exigir ≥1 operador.
      — o MESMO harness prova-o no contentor (exit != 0, `bind-guardrail RECUSOU "0.0.0.0:8080" … operadores
      registados=0`); em Go, `TestBindGuardrailRequiresAtLeastOneOperator` assere que o **predicado antigo é
      verdadeiro nos DOIS nós** (a definição de vácuo) e que só o novo os separa, e
      `TestServeAPIRefusesNonLoopbackWithoutOperators` leva-o ao caminho de produção (`serveAPI`) sobre um nó
      INTACTO. Consequência operacional documentada como mudança de comportamento.
- [x] `deploy/node/README.md:46-47` fica verdadeiro, ou é corrigido para descrever o que o código impõe.
      — o README (reestruturado por AOS-191) passa a enunciar a condição REAL do guardrail como conjunção de três
      (SteerAuth ∧ identidade real ∧ ≥1 operador), com secção nova «Plano de controlo — operadores e aprovadores»
      e aviso destacado de MUDANÇA DE COMPORTAMENTO para quem já fazia bind não-loopback sem operadores.
      A **descrição** foi alinhada com o **âmbito** do predicado nos dois sentidos: a mensagem de
      `ErrRefuseNonLoopbackBind` diz «steer/pause INOPERAVEIS» (e não «canal de controlo não operável»), porque um
      nó configurado só com `AOS_APPROVERS_FILE` tem o `/approve` operável e é, ainda assim, recusado — o âmbito
      conservador é deliberado e está justificado no README e em `controlAuthenticated`. Documenta-se também o
      encoding do wire (`risk_class: 0 == danger`, valor-zero fail-closed).

**Mudança de comportamento (declarada).** Um nó que fazia bind a `0.0.0.0` **sem** operadores passa a recusar o
arranque. A correcção do operador é `-e AOS_OPERATORS="<id>=<hexpubkey>"`; a alternativa honesta é fazer bind ao
loopback. `deploy/node/aos169-durability-harness.sh` foi o **primeiro afectado** — e a sua correcção é, ela própria,
prova de que o predicado antigo não discriminava (o comentário do harness afirmava que o bind era permitido «porque
o SteerAuth está composto»).

---

### AOS-194 — Corrigir rastreabilidade do STRIDE e cobrir a superfície real

**Achado:** STR-01 (ALTO, CONTRADITÓRIO, NOVO) + STR-06. A coluna «Ticket» de `tecnica/17_Analise_STRIDE.md`
está **sistematicamente** errada (desvio de 1–2 posições; só 3 mapeamentos correctos), verificado contra
`specs/EPIC-07:48-59`: prompt injection→AOS-067 (é «rede default-deny»; o taint é AOS-069); sandbox
escape→AOS-068 (é «filtragem DNS»; a microVM é AOS-064); audit tamper-evident→AOS-071 (é «autoridade escopada»;
o audit é AOS-072); mensagens assinadas→AOS-074 (é «gate de risco»); runaway/admission→AOS-073 (são mensagens
assinadas). Além disso o documento é **órfão** (nenhuma EPIC/ADR/RTM lhe aponta) e
`grep -i "http|api|sse|dsar|otlp|container"` → **zero ocorrências**, apesar de a **Carta emenda 1.2 ter mandado
reavaliar o modelo de ameaça para o nó exposto como serviço de rede**.

**Critérios de aceitação**
- [x] Cada `AOS-NNN` citado em `tecnica/17` casa com o título do ticket em `specs/EPIC-*.md` (diff mecânico limpo).
      *(ENTREGUE `ff9761f`: 78 `AOS-NNN` distintos verificados; a v1.0 tinha ~3 correctos em ~55 linhas. Causa-raiz registada: `AOS-064` usado como carimbo por secção em vez de por controlo. `ref-lint` verde.)*
- [x] Acrescentada coluna de **estado** por mitigação: entregue / por-fazer / deferido-com-eixo.
      *(ENTREGUE `ff9761f`: legenda em §1.5; estado derivado de código+commits, NÃO da presença de ticket — os CA das EPICs estão por marcar e não servem de fonte.)*
- [x] O documento cobre a superfície actual: API HTTP, SSE, DSAR, OIDC, attestation, exporter OTLP, contentor.
      *(ENTREGUE `ff9761f`: Bloco B (§4.10–§4.18), 9 elementos × 6 categorias = 54 linhas novas; DFD estendido. Enacta a Carta emenda 1.2. Nova §5.2 com as ameaças sem mitigação completa.)*
- [ ] O documento passa a ser referenciado (RTM e/ou epic), deixando de ser órfão.
      *(POR FAZER — exigia escrever fora da pista do agente na execução paralela. Estado verificado: `tecnica/16` não menciona `tecnica/17`. A entrada tem de entrar na FONTE que `scripts/ci/rtm-regenerate.py` lê, não no ficheiro gerado; e `tecnica/17` deve constar dos «Documentos relacionados» de `specs/EPIC-07` e `specs/EPIC-15`.)*
- [x] **Automatização — ENTREGUE por AOS-198** (`7d16c4e`): a verificação ticket↔título foi acrescentada ao `ref-lint.py`.
      *(395 declarações verificadas, 0 discordâncias. Calibração MEDIDA: a 1.ª versão aceitava a forma em prosa e deu 16 falsos positivos e zero verdadeiros, pelo que foi restringida a cabeçalhos e células de tabela. LIMITE DECLARADO: não apanha a forma exacta do STR-01, porque a tabela de `tecnica/17` cita tickets numa coluna **sem título** para comparar.)*
      *(texto original do CA:)* a verificação ticket↔título é acrescentada ao `ref-lint.py` (extensão natural), para
      esta correcção não voltar a derivar.

---

### AOS-195 — Corrigir a regressão documental de `redaction/doc.go`

**Achado:** VAC-02 ≡ DEF-02 ≡ CON-03 (MÉDIO, NOVO — regressão introduzida por AOS-188).
`packages/substrate/redaction/doc.go:11-12` afirma que o motor está «cablado nos composition-roots (cmd/aos,
cmd/aos-demo, integration)»; `go list -deps` mostra-o **ausente** em `cmd/aos` e `integration` (está presente em
`aos-demo`, via `approval-card`). Três lentes independentes chegaram aqui por caminhos diferentes.

**Critérios de aceitação**
- [x] `doc.go:11-12` deixa de afirmar cablagem inexistente (mantendo `:13-15`, que declara o limite real).
      *(ENTREGUE `d355551`: discrimina por composition-root — ausente em `cmd/aos` e `integration`, presente em `aos-demo` via `approval-card`. Corrigido também um over-claim adjacente: o `dsar` era listado como consumidor de produção quando só o é em teste. Nova secção «Como verificar as afirmações acima» com os comandos e as duas armadilhas.)*
- [ ] O CA de AOS-188 é reaberto **ou** a fronteira fica registada como deferimento com ticket nomeado.
      *(POR FAZER — exigia `specs/EPIC-17`, fora da pista na execução paralela. Via RECOMENDADA pelo agente: **não** desmarcar. O CA de AOS-188 tem porta de escape disjuntiva («ou o `doc.go` é actualizado para reflectir o escopo real») que até `d355551` **não** estava genuinamente satisfeita; desmarcar torná-lo-ia falso no sentido inverso. O que fica aberto é a ligação SUBSTANTIVA do motor ao Event Store/`platform/memory`/`otel-genai`/`platform/audit` — escopo próprio, ticket por criar.)*
- [x] **Verificação:** `go list -deps ./...` em `cmd/aos` e `integration` não contradiz nenhuma afirmação do `doc.go`.
      *(ENTREGUE `d355551`. MECANISMO do quase-acerto identificado: `cmd/aos` arrasta `governance/dsar`, mas o `dsar` importa o motor **apenas em `flow_test.go`** — e `go list -deps` NÃO segue dependências só-de-teste.)*

---

### AOS-196 — Registo único de deferimentos + correcção dos eixos inválidos

**Achado:** DEF-01/DEF-03/DEF-06 (ALTO/MÉDIO, NOVO). O padrão sistemático não é «dívida escondida» — é **eixo
errado**: a cifra do substrato é apontada a três epics inconsistentes (`bootstrap.go:626` diz EPIC-06/09/10;
`tecnica/02:175` diz **EPIC-13**, que é o epic de *Frontend*) e **nenhum** tem ticket para ela; o anti-replay do
ADR-012 é apontado a EPIC-13; a assinatura de imagem do ADR-017 é apontada a EPIC-10, que não tem ticket para ela.
`NewRatificationGate`/`NewProductionRatificationGate` não têm chamador de produção, e o CA `EPIC-14:901` `[x]`
(«ligados no promotion controller») é falso.

**Critérios de aceitação**
- [x] Existe um registo único de deferimentos com colunas: id, descrição, **eixo (ticket real)**, dono, gatilho, estado.
      *(ENTREGUE `d33c0ff`: `docs/governance/REGISTO-Deferimentos.md`, 51 linhas cobrindo 38 pares (ficheiro, marcador) e 77 ocorrências em Go de produção + 11 documentais. **Nenhum número inventado**: 19 linhas dizem `POR ATRIBUIR` com nota a descrever o ticket necessário — citar um `AOS-NNN` fantasma avermelharia o `ref-lint`.)*
- [x] Os três eixos inválidos acima são corrigidos (ou recebem ticket novo se nenhum existir).
      *(ENTREGUE `d33c0ff`: `bootstrap.go` (comentários), `tecnica/02:~175`, ADR-012 (4 ocorrências) e ADR-017. O ADR-017 passou a enumerar os onze tickets do EPIC-10 para mostrar que **nenhum assina imagens**.)*
- [x] O CA falso de `EPIC-14:901` é corrigido.
      *(ENTREGUE `d33c0ff`: **DESMARCADO** de `[x]` para `[ ]` com evidência — `NewProductionRatificationGate` só tem chamadores em `_test.go` e o nó não compõe promotion controller nenhum. Direcção oposta ao over-claim.)*
- [x] **Verificação por script — ENTREGUE** `d33c0ff`: gate `deferrals` (Python stdlib, fail-closed, 6 verificações incl. anti-eixo-fantasma e «só encolhe»), ligado aos TRÊS sítios (`ALL_GATES`, job em `ci.yml`, `needs:` do agregador).
      *(Detalhe MEDIDO: `STUB` e `CONDICIONAL` só contam em MAIÚSCULAS — em minúsculas ocorrem ~30× em negações («nunca um stub») e em identificadores Go (`BudgetStub`), o que produziria linhas sem significado e um gate que seria desligado.)*
      *(texto original do CA:)* todo o marcador `DEFERIDO`/`demo-grade` em `packages/**/*.go` (não-teste) tem
      entrada no registo cujo eixo contém um `AOS-NNN` existente.

---

### AOS-197 — Reclassificar a matriz de conformidade e nomear as lacunas de âmbito

**Achado:** CON-01 + DEF-01 (MÉDIO/ALTO, NOVO). `tecnica/14_Matriz_Conformidade.md:92` marca o Art. 17 (direito
ao apagamento) como «**Coberto**» sem ressalva, num documento dirigido a «DPO, equipas jurídicas e auditores
externos» (`:29`) cuja §5 enumera 7 lacunas e **omite esta**. O próprio nó contradiz a matriz em runtime
(`bootstrap.go:672`: o conteúdo dos runs no Event Store «fica **FORA** do alcance do shredding»).
`audit.IngestPipeline` (o que cifra PII por titular) nunca é composto fora de testes; o vault do nó é
`NewInMemoryKeyVault(nil)` e nada chama `EnsureKey` em produção — o WORM chega a selar `dsar.key_destroyed`
para uma *erasure* vacuosa.

**Nota de justiça (do contra-exame):** `tecnica/14:70` define a legenda («Coberto = o mecanismo AOS existe e
suporta o requisito») e a §2 transfere para o operador a responsabilidade de activar. Pela **própria escada do
documento**, a linha devia ler «Parcial» — não é falsificação, é calibração.

**Critérios de aceitação**
- [x] `tecnica/14:92` (Art. 17) e `:91` (Art. 5) passam de «Coberto» para «**Parcial**», com a lacuna acrescentada à §5.
      *(ENTREGUE `d7dd962`: e também o Art. 30; o Art. 32 qualificado. É calibração pela escada que o próprio documento define (:70 + §2), não acusação de falsidade.)*
- [x] A §5 passa a nomear também as lacunas de **âmbito da própria matriz**: Art. 15/16/20 (o «DSAR» do produto só
      faz apagamento), **Art. 22** (decisão individual automatizada — o artigo mais directamente convocado pela
      forma do produto), Art. 33/34, e AI Act Art. 50.
- [x] **Verificação:** toda a linha «Coberto» tem o mecanismo alcançável a partir de `packages/cmd/aos`
      (`go list -deps` sobre o pacote que a implementa).
- [ ] Legal hold e job de expiração (CON-02) recebem eixo/dono/data declarados ou superfície de administração.
      *(POR FAZER — **decisão do dono**: são as únicas dívidas de conformidade sem eixo/dono/data declarados. `grep NewExpirationJob` → 0 chamadores de produção; nenhuma rota de hold em `api.go`.)*

---

### AOS-198 — Criar o «gate 4 — Integração» (ou retirar a sua declaração)

**Achado:** DAT-09 (MÉDIO, NOVO) — **causa-raiz identificada**. Os dois contratos com rastreio a `tecnica/12` no
código (C1, C2) estão **fiéis**; os três **sem** rastreio (C3, C4, C5) divergiram integralmente. O «gate 4 —
Integração», declarado bloqueante de merge em `specs/01:83` e nomeado em `tecnica/12:351` como a mitigação de
«deriva silenciosa de schema», **não existe** em `scripts/ci/`. A deriva não é acidental — é a ausência do gate.

**Critérios de aceitação**
- [x] **VIA ESCOLHIDA — ENTREGUE** `7d16c4e`: `scripts/ci/run.sh` inclui um gate que falha se um tipo Go de porta divergir do contrato declarado em
      `tecnica/12` (mínimo verificável: presença dos códigos de erro `E_*` documentados), **ou**
- [—] *(VIA NÃO ESCOLHIDA — o CA é disjuntivo; criar o gate foi preferido porque retirar a declaração converteria um achado num deferimento permanente)* a declaração é **retirada** de `specs/01:83` e `tecnica/12:351` e a deriva C3/C4/C5 fica registada como
      deferimento com eixo (AOS-196).
- [x] Qualquer que seja a via, o resultado é falsificável: não pode ficar um gate declarado e inexistente.
      *(ENTREGUE `7d16c4e`. O que o gate NÃO faz está declarado em TRÊS sítios (cabeçalho do script, `specs/01` linha 4, `tecnica/12` §11); corrigidas as duas afirmações que sobre-prometiam. Baselines com regras mais apertadas que as de scanner: dono obrigatório, só encolhem, entrada obsoleta **falha**.)*

---

### AOS-199 — Pisos aos limiares de gate

**Achado:** ORF-06 (MÉDIO, NOVO). `EVAL_PASS_RATE_MIN`, `KERNEL_COVERAGE_MIN`, `APEX_COVERAGE_MIN` e `SKIP_DOCKER`
são sobreponíveis por ambiente **sem piso nem registo** — «gates verdes» deixa de ser prova reproduzível.

**Critérios de aceitação**
- [x] Cada limiar tem um **piso** abaixo do qual o gate falha por violação de piso.
      *(ENTREGUE `be53411`: **7 limiares + 1 interruptor** — a auditoria nomeava 4; três (`MEMORY`/`ROUTING`/`REGISTRY_COVERAGE_MIN`) eram invisíveis ao ORF-06. Piso == default, deliberadamente: um piso mais baixo seria uma segunda barra não documentada. Nenhum default foi baixado; os knobs passam a **ratchets** (só apertam). `COVERAGE_MIN` tem piso PRÓPRIO, não herdado — senão baixar o knob histórico arrastava o gate dos 10 módulos.)*
- [x] **Prova negativa:** `EVAL_PASS_RATE_MIN=0 make ci` **falha** (não passa verde).
      *(ENTREGUE `be53411`. Dois diagnósticos distintos: «VIOLAÇÃO DE PISO» (config inválida) vs «LIMIAR NÃO ATINGIDO» (o código falhou). Validação de domínio apanha confusão de unidade (`EVAL_PASS_RATE_MIN=90` numa fracção 0..1). Escape hatch exige justificação e é **RECUSADO em CI** — a CI não consegue descer um piso nem de propósito.)*
- [x] Os limiares e os seus pisos ficam documentados em `CONTRIBUTING.md` ou `AGENTS.md`.
      *(ENTREGUE `be53411`: tabela dos 7 limiares (default/piso/domínio/ficheiro/justificação) + tabela dos dois diagnósticos em `CONTRIBUTING.md`; nota da regra «só apertam» em `AGENTS.md`.)*
- [x] `SKIP_DOCKER` regista no output que etapas foram saltadas (sem falso-verde silencioso).
      *(ENTREGUE `be53411` + `82cd7f4`: `'true'`/`'yes'` eram tratados como 0 **em silêncio** — agora saem 2 como config inválida. Veredicto passa a «VERDE PARCIAL» que nega explicitamente ser prova do ponto 2 do ADR-017. O mesmo modelo aplicado ao `sbom.sh`, que a CI invoca como step autónomo.)*

---

### AOS-200 — Instrumentar o tripwire da Carta §6.6

**Achado:** DEF-07 (MÉDIO, NOVO). A Carta §6.6 declara: «≥2 decisões FIXAS reabertas em 30 dias ⇒ o congelamento
falhou … **este contador é o SLI do próprio processo**». Não existe contador, nem registo de arbitragens §6.5, em
lado nenhum do corpus. A promessa anti-retrabalho não está falsificada — está **infalsificável**, que é
precisamente a condição que o §6.6 foi escrito para evitar.

**Critérios de aceitação**
- [x] **ENTREGUE** `5c01d7c` — existe um registo (ficheiro versionado) com: data, decisão FIXA tocada, natureza (emenda/arbitragem),
      veredicto do árbitro §6.5.
- [x] O contador de reaberturas em janela de 30 dias é calculável por comando.
      *(ENTREGUE `5c01d7c`: python3 inline sobre TODAS as janelas deslizantes (a §6.6 diz «numa janela de 30 dias», não «na corrente»). **Prova negativa**: sobre cópia com as três PENDENTE mutadas para RECUSA, imprime `TRIPWIRE DISPARADO` e sai com 1 — um contador que nunca dispara não é um SLI.)*
- [x] As emendas 1.1/1.2/1.3 e a arbitragem que originou o ADR-019 ficam registadas retroactivamente.
      *(ENTREGUE `5c01d7c`: 10 eventos (REG-000..009). Contador: reaberturas=0, recusas=0, **PENDENTES=3**. A §6.5 exige decisão POR ESCRITO pelos DOIS papéis; o rótulo «reavaliação de contexto, não re-litígio» foi **auto-atribuído por quem propôs a alteração**, na mesma emenda que cria o árbitro. As três (D3, D5, excepção zero-dep do ADR-017) caem na MESMA janela de 30 dias: se duas forem arbitradas como re-litígio, a §6.6 **dispara retroactivamente**.)*

---

### AOS-201 — Reconciliar `tecnica/13` com o modelo de eventos real

**Achado:** DAT-01/02/03 (MÉDIO, NOVO). Envelope de `tecnica/13:60-89` vs `eventstore/event.go:59-72`: 8 campos
documentados ausentes, 5 reais não documentados (o schema publicado tem `additionalProperties:false`). 81
constantes de tipo de evento no código, 80 não registadas; 3 dos 4 nomes «canónicos» documentados **não são
emitidos** (`tool.result.received`, `state.transition`, `tool.call.dispatched`). O campo `taint` do contrato C2
não existe no envelope (é persistido no payload da mediação, `eventsink.go:96,159`).

**Critérios de aceitação**
- [x] O envelope documentado casa com `eventstore/event.go` (campo a campo), ou as diferenças são declaradas
      como «desenho, não wire» de forma explícita e localizada.
- [x] Os nomes canónicos citados são emitidos, ou substituídos pelos reais.
      *(ENTREGUE `7b69c27`: os 3 nomes nunca emitidos corrigidos; a citação original era ilustrativa («ex.:»), não normativa. Taint localizado nos 3 sítios reais (§3.4) em vez de declarado em falta — e corrigido o caminho que a auditoria citava (`kernel/reference-monitor/eventsink.go`, não `platform/audit/`).)*
- [x] O catálogo de tipos de evento é gerado ou verificado por script (evitar nova deriva de 80 entradas).
      *(ENTREGUE por **AOS-198** `7d16c4e`: gate `event-catalog` (85 constantes vs as 29 famílias da taxonomia de `tecnica/13` §3.3; rejeita literais e concatenações em `EventInput.Type`), ligado aos três sítios. Fecha o residual que `7b69c27` deixou como PARCIAL.)*
      *(**PARCIAL** `7b69c27`: catálogo por família/prefixo (29 prefixos, 85 tipos — não 81: o código mexeu-se, mais uma razão para não fixar lista) com o pacote dono e um comando `grep` reproduzível validado. Falta o **gate de CI** (exigia `scripts/**`, fora da pista na execução paralela) — **fecha em AOS-198**.)*

---

### AOS-202 — Destino dos módulos `*/contract` órfãos

**Achado:** ORF-01 (MÉDIO, NOVO). `packages/{kernel,substrate}/contract` — **1763 LOC, 22 ficheiros, 0
importadores, 0 testes, 0 menções documentais** — auto-declaram-se «contrato canónico». Com zero importadores,
não entram em nenhum binário (daí a reclassificação de CONTRADITÓRIO para MÉDIO), mas a auto-declaração colide
com a leitura literal do ADR-019 §3.

**Critérios de aceitação**
- [x] Decisão registada: **REMOVER** — `c78f431`.
      *(Os 22 ficheiros nasceram TODOS em `4d90a58` (AOS-177), cuja mensagem nunca os menciona: não houve decisão, houve arrasto. Contradiziam o ADR-019 §3, aceite no MESMO dia, que rejeita `kernel/contract` para a v1 — e não existe ADR de supersessão. Não eram referência: eram um **fork silencioso já a divergir**, com as cópias ATRASADAS (a de audit perdeu o bloco do bump v1→v2).)*
- [—] *(N/A — não ficaram.)* Se ficarem, a auto-declaração «contrato canónico» é calibrada para não colidir com o ADR-019 §3.
      *(Em vez disso, o ADR-019 §5 ganhou um item que torna a AUSÊNCIA verificável: se os directórios reaparecerem sem ADR de supersessão, é violação da §3, não implementação dela.)*
- [x] Se saírem, a remoção não quebra nenhum build (verificado por `go build ./...` em todos os módulos).
      *(ENTREGUE `c78f431`: ~34 módulos compilam; `layer-lint` verde; a baseline não continha entradas `contract`, logo não foi tocada.)*

---

### AOS-203 — Documentar as variáveis de ambiente e endurecer o kill-switch de soberania

**Achado:** ORF-03/04/05 (MÉDIO, NOVO). `AOS_HUMANS` e `AOS_ISSUER_ID` não têm **uma linha** de documentação em
todo o repo. Pior: **`AOS_BOARD_REGIONS` definido-vazio é um kill-switch do read-path soberano**, documentado
apenas num script de harness — uma variável de ambiente que desliga um controlo de conformidade sem registo.

**Critérios de aceitação**
- [x] **ENTREGUE** `f8354ae` — todas as variáveis lidas por `packages/cmd/aos` (`os.Getenv`) estão documentadas em `deploy/node/README.md`,
      com efeito, default e impacto de segurança.
- [x] **ENTREGUE** `f8354ae` — `AOS_BOARD_REGIONS` vazio **não** desliga silenciosamente o read-path soberano: ou recusa arrancar em
      `AOS_MODE=production` (padrão de `ErrProductionNeedsSovereignRead`), ou regista um aviso proeminente no banner.
- [x] **Verificação por script:** o conjunto de `os.Getenv` em `cmd/aos` é subconjunto do documentado (gate ou teste).
      *(ENTREGUE `f8354ae`: `TestAOS203EnvSurfaceIsDocumented` parseia o código com **`go/parser` (AST), não grep** — um `grep AOS_` apanharia menções em comentários e strings de erro, de que o pacote está cheio, e exigiria documentar variáveis que ninguém lê. Allowlist explícita hoje VAZIA. As 16 variáveis documentadas, incluindo `AOS_HUMANS` e `AOS_ISSUER_ID`, que não tinham UMA linha em todo o repo.)*
- [x] **Postura de produção de `AOS_DURABLE_EXECUTION` — DECIDIDA** `f8354ae`: mantém-se **opt-in**, também em produção.
      *(Critério da promessa falsa: sem `AOS_ISSUER_PUBKEY` ou `AOS_BOARD_REGIONS` o nó SERVIRIA com postura mais fraca do que a anunciada; com a durabilidade desligada o nó **não anuncia durabilidade nenhuma** — o banner declara DESLIGADA em cada arranque. Decisão explícita e registada, não omissão.)*
      *(texto original do CA:)* Decidir se `AOS_MODE=production`
      passa a **exigir** execução durável (padrão de `ErrProductionNeedsHardenedIdentity` /
      `ErrProductionNeedsSovereignRead`) ou se permanece **opt-in** por decisão registada. AOS-191 deixou-a opt-in
      deliberadamente — não há promessa falsa (o banner declara `DESLIGADA`) e exigi-la teria quebrado a
      retro-compatibilidade que aquele ticket impunha —, mas a **assimetria** face às outras duas posturas de
      produção tem de ficar decidida aqui, não tácita.
      *(Nota: AOS-191 já documentou `AOS_DURABLE_EXECUTION`, `AOS_EVENTSTORE_PATH` e `AOS_WORM_PATH` em
      `deploy/node/README.md`; o 1.º CA acima cobre as restantes, incluindo `AOS_HUMANS` e `AOS_ISSUER_ID`.)*

---

### AOS-204 — Exportar por OTLP, a partir do nó real, a árvore de um run com tool call

**Achado:** VAC-01 (eixo residual). Ao corrigir a terceira citação errada do checklist §13, AOS-192 apurou que
`TestObservabilityEndToEndExportsWellFormedOTLPWithCost` (`packages/cmd/aos/observability_test.go`) **não** exporta
nenhum span `execute_tool`: usa `obsConfig()` → `tnBaseConfig()`, cujo `Config.Model` é nil, pelo que o `Bootstrap`
injecta o `referenceModel` (`bootstrap.go:830-837`), que devolve `Final: true` **sem tool calls**. A string
`execute_tool` nem sequer ocorre nesse ficheiro. O ramo `execute_tool` está provado ao nível de **componente**
(`packages/kernel/agent-runtime/activity/dispatch_test.go`) e a hierarquia é reconstruída em
`trajectory_surface_test.go` — falta a exportação OTLP **a partir do nó**.

**Impacto:** §13.6 (OBSERVABILIDADE) está **REABERTO 🟡** em `docs/reports/AOS-169-aceitacao-sistemica.md` com este
eixo nomeado. Sem ticket que o possua, o eixo tende a ser re-arredondado para VERDE pelo próximo agregador de
estado — o ciclo que AOS-192 veio quebrar. Este ticket é o dono do eixo (CA de AOS-196: todo o deferimento tem
eixo válido **com um ticket real**).

**Critérios de aceitação**
- [x] **ENTREGUE** `339992d` — um teste de `packages/cmd/aos` compõe o nó com um modelo que **EMITE** uma tool call e assere que o
      *collector* OTLP recebe um span `execute_tool` bem-formado (atributos `gen_ai.*`/`aos.*`, sem segredos),
      filho do `invoke_agent` do mesmo run.
- [x] **ENTREGUE** `339992d` — o teste é NÃO-VACUOSO: falha se o run não chegar a despachar a tool (asserção explícita de que a tool call
      foi emitida), e o span nasce **também** sob veredicto de negação do PDP (o span é do Reference Monitor,
      não do caminho feliz).
- [x] **ENTREGUE** `339992d` — §13.6 re-marcado **VERDE**, com o «dono: AOS-204» retirado do corpo, da tabela-resumo e da conclusão.
      *(Prova negativa em DUAS variantes; a (B) reconstitui o defeito ORIGINAL de AOS-192 e devolve `[registry.freeze_toolset chat invoke_agent]` — EXACTAMENTE o conjunto que a evidência antiga citava como se incluísse `execute_tool`. O teste novo pinta de vermelho o falso-verde que AOS-192 apanhou. Topologia asserida (traceId + parentSpanId), não só presença; e o selo WORM `audit_seal` é FILHO do `execute_tool`, ligando trajectória e registo por parentesco. **RESIDUAL NOMEADO**: `secured.go:250` compõe o dispatcher durável sem tracer ⇒ no modo durável o span `aos.activity` não é exportado. Correcção de UMA linha em PRODUÇÃO: declarada, não feita.)*
      *(texto original do CA:)* `docs/reports/AOS-169-aceitacao-sistemica.md` §13.6 é re-marcado com esta evidência (ou mantém-se 🟡
      com o eixo actualizado — nunca VERDE sem o teste).
- [x] Gates `layer-lint`/`rtm`/`ref-lint` verdes; testes com `-race`.
      *(Verificado pelo orquestrador sobre árvore estável, com os gates novos incluídos: `deferrals`, `integration`, `event-catalog`, `selftest`, `secrets` e `cmd/aos -race` — todos verdes.)*

---

## 8-bis. Tickets gerados pelo registo de deferimentos, pela análise STRIDE e pelos residuais nomeados (AOS-205 … AOS-211)

Estes tickets **não são remediação da auditoria** — são o trabalho substantivo para que os
deferimentos, a análise STRIDE e os **residuais nomeados** apontavam sem executor. Nascem aqui porque
foram o `docs/governance/REGISTO-Deferimentos.md` (AOS-196), o AOS-195, o AOS-194 e o **AOS-204** que os
identificaram; a **execução** pertence aos epics temáticos indicados.

O AOS-210 acrescenta uma **terceira origem** às duas do título original: um *residual nomeado* num
relatório de aceitação. É deliberado que ele apareça aqui e não fique só no relatório — a §6.3 de
`docs/reports/AOS-169-aceitacao-sistemica.md` escreveu, sobre a sua própria pendência, que ela «**não tem
dono atribuído**: tem de ser entregue a quem detém `packages/integration`, sob pena de reproduzir a
deriva "residual nomeado que nunca é encaminhado"». Criar o ticket é a acção que essa frase pedia.

O **AOS-211** nasce da mesma regra aplicada ao próprio AOS-210: ao pôr o `aos.activity` na árvore
exportada pelo nó, AOS-210 deixou dois atributos por cobrir nesse span (o custo por efeito real e o
`gen_ai.operation.name`), e nomeá-los no relatório **sem dono** seria repetir exactamente a deriva que
AOS-210 veio terminar. A regra vale para quem a invoca: **residual nomeado ⇒ ticket**, ou não se nomeia.

Enquanto não existiam, 19 linhas do registo diziam `POR ATRIBUIR` — o que é honesto, mas não é
executável. Criá-los completa o objectivo §2 deste epic («pôr os deferimentos num registo único com
eixo, dono e gatilho **falsificáveis**»): um eixo sem executor não é falsificável.

**Não foi criado de propósito:** a cifra por-titular do substrato (família `3xx` do registo) **não**
recebe ticket novo. A arbitragem `A-DEF-301` (2026-07-27) apontou-a ao **AOS-093 existente**, cujo
primeiro CA diz «toda a PII persistida é cifrada com uma chave por titular» — sem restrição ao audit —
e cujos Detalhes Técnicos já nomeiam o Event Store. Criar um ticket novo **duplicaria** AOS-093 e daria
dois donos à mesma propriedade: *trocar um eixo errado por um eixo inflacionado é o mesmo defeito ao
contrário*. O que falta é **refinar** o CA de AOS-093 (pendência `P-3b`), não criar outro ticket.

| ID | Título | Tipo | Est. | Prio | Epic de EXECUÇÃO | Origem |
|---|---|---|---|---|---|---|
| AOS-205 | Provisionamento do IdP de soberania: registo board→região e credencial forte do leitor/operador | feature | L | P1 | **EPIC-09** | nota `N-DEF-201` (10 linhas do registo) |
| AOS-206 | Compor o *promotion controller* do nó com `NewProductionRatificationGate` | feature | M | P1 | **EPIC-14** | nota `N-DEF-401` (DEF-03) |
| AOS-207 | Assinatura e atestação da imagem do nó (chave de release, in-toto/SLSA, verificação na entrega) | feature | M | P2 | **EPIC-05** | nota `N-DEF-501` (DEF-06) |
| AOS-208 | Ligação substantiva do motor de redacção ao Event Store, memória, `otel-genai` e audit | feature | M | P1 | **EPIC-09** | pendência de AOS-195 |
| AOS-209 | **Terminação TLS do nó** (ingresso HTTP/SSE/DSAR + perna OTLP) | feature | M | **P0** | **EPIC-15** | `tecnica/17` §5.2-b (AOS-194) |
| AOS-210 | Tracer do **dispatcher durável** no composition root: o span `aos.activity` na árvore do nó | fix | S | P1 | **EPIC-14** | residual §6.3 de `AOS-169-aceitacao-sistemica.md` (AOS-204) |
| AOS-211 | Os dois atributos em falta no `aos.activity`: **custo por efeito real** e `gen_ai.operation.name` sob contrato semconv | fix | S | P2 | **EPIC-08** | residual §6.3 de `AOS-169-aceitacao-sistemica.md` (AOS-210) |

---

### AOS-205 — Provisionamento do IdP de soberania

**Origem:** nota `N-DEF-201` do registo — **um só** ticket em falta, replicado por dez linhas em seis
ficheiros. O AOS-203 cobre a documentação e o kill-switch das variáveis; o **provisionamento** é este.

**Porque não existia:** o eixo estava escrito como «EPIC-09/10». O EPIC-09 entrega a *regra* de
soberania (AOS-094) e o EPIC-10 entrega topologia/DR (AOS-098…108) — nenhum dos onze tickets entrega
**provisionamento de identidade regional**. A Carta §4.2 marca **D7 como CONDICIONAL** a esse
provisionamento: a decisão estava registada, o ticket é que nunca existiu.

**Critérios de aceitação**

- [ ] O registo board→região deixa de ser lido de `AOS_BOARD_REGIONS` e passa a vir de uma fonte de
      autoridade da organização, com **rotação** e **auditoria de alterações**.
- [ ] Os headers `X-Aos-Reader`/`X-Aos-Board` deixam de ser **auto-declarados**: o leitor de governação
      e o operador DSAR apresentam credencial forte (OIDC/mTLS) verificada contra esse IdP.
- [ ] Em `AOS_MODE=production`, arrancar sem essa fonte **recusa** — hoje já recusa sem soberania
      configurada, mas aceita a configuração *self-hosted* como se fosse autoridade.
- [ ] **Falsificável:** um pedido com `X-Aos-Board` forjado (board válido, credencial ausente ou de
      outro titular) é **recusado**; hoje passa.

**Dependências:** AOS-094 (regra de soberania), AOS-174 (`HumanDirectory` OIDC), AOS-182.
**Fecha no registo:** DEF-201, DEF-203…DEF-211.

---

### AOS-206 — Compor o *promotion controller* do nó com `NewProductionRatificationGate`

**Origem:** nota `N-DEF-401` — é o achado **DEF-03**.

**Porque não existia:** o AOS-159 entregou o mecanismo e o CA do *wiring* foi marcado `[x]` **sem
chamador de produção existir** (desmarcado por AOS-196, `d33c0ff`). O ADR-012 apontava o endurecimento
à **EPIC-13 — que é o epic de Frontend**.

**Critérios de aceitação**

- [ ] O nó `aos` compõe um *promotion controller* real. Facto de partida: procurar `promotion` ou
      `Promote` em `packages/cmd/aos/*.go` (não-teste) devolve **zero** ocorrências.
- [ ] Esse controller usa a via sancionada `hitl.NewProductionRatificationGate`, que **força**
      `WithRatifyFreshness` + `WithRatifyNonceStore` e recusa a construção sem eles — **não**
      `NewRatificationGate` cru.
- [ ] **Falsificável:** um teste de ápice em que a mesma ratificação, re-submetida após consumo,
      devolve `ReasonRatificationReplayed` **através do caminho do nó**, não do gate isolado.

**Dependências:** AOS-159 (mecanismo), AOS-096.
**Fecha no registo:** DEF-401, DEF-402.

---

### AOS-207 — Assinatura e atestação da imagem do nó

**Origem:** nota `N-DEF-501` — é o achado **DEF-06**. Fecha o **ponto 3 do ADR-017**, que a própria
§Consequências admitia entregar «na forma mínima (SBOM gerado, atestação por assinar)».

**Porque não existia:** o eixo dizia «parte do endurecimento de EPIC-10». **Nenhum** dos onze tickets
do EPIC-10 assina imagens (AOS-098 IaC, 099 workers, 100 replicação, 101 backup, 102 DR, 103 microVMs,
104/105/106 dashboards/alertas/runbooks, 107 escala, 108 hipercare). O AOS-168 entregou o
**empacotamento** e o AOS-187 ligou os gates `package`/`sbom` — nenhum assina.

**Critérios de aceitação**

- [x] **ENTREGUE** `0cb8d5d` — Custódia da chave de assinatura de release documentada (**quem** assina, **onde** vive a chave,
      **como** se roda). O ADR-017 ponto 5 já exige custódia própria para a autoridade de identidade;
      a imagem do nó não tem equivalente.
- [x] **ENTREGUE** `0cb8d5d` — a atestação passa de gerada a **assinada e verificável** (envelope DSSE v1 + in-toto Statement, `crypto/ed25519` stdlib, `scripts/ci/attest` zero-dep + build offline); a entrega **RECUSA** assinatura inválida. `release-pubkeys.json` = `keys:[]` (fail-closed por omissão). A atestação de proveniência passa de **gerada** a **assinada e verificável**, e a entrega
      **recusa** uma imagem cuja assinatura não valide.
- [x] **ENTREGUE** `0cb8d5d` — **Falsificável (verificado):** `verify-attestation` recomputa cada subject assinado contra o artefacto e o digest REAIS; uma atestação válida sobre bytes que já não existem é recusada. Os **3 ALTO de fail-open** da auditoria foram remediados e verificados por reprodução. **Pendência declarada** (outra pista): encadear `sign.sh`/`verify-attestation.sh` no `ci.yml`/`run.sh` e distinguir a saída 3 (verde parcial) da 1. **Falsificável:** substituir o digest da imagem no manifesto de entrega faz o gate ficar
      **vermelho**.

**Dependências:** AOS-168 (empacotamento), AOS-187 (gates package/sbom).
**Fecha no registo:** DEF-501.
**Nota:** pode exigir dependência externa de assinatura — se exigir, aplica-se a disciplina da emenda
1.3 da Carta (excepção escopada, fora do binário do nó).

---

### AOS-208 — Ligação substantiva do motor de redacção

**Origem:** pendência do AOS-195 (`d355551`). **Não** consta do registo de deferimentos porque o
defeito era um **over-claim no `doc.go`**, não um marcador `DEFERIDO` — e por isso escapou também ao
gate `deferrals`.

**Porque não existia:** o AOS-188 fechou o seu CA pela **porta de escape disjuntiva** («ou o `doc.go` é
actualizado para reflectir o escopo real») e o texto substituto passou a afirmar cablagem inexistente.
O AOS-195 corrigiu o texto; a **ligação** nunca teve ticket — procurar `redac` em `specs/` devolve
apenas menções em EPIC-09 e EPIC-17, nenhuma com este âmbito.

**Critérios de aceitação**

- [ ] O motor entra no fecho transitivo de `packages/cmd/aos` e de `packages/integration` — hoje
      `go list -deps ./...` mostra-o **ausente** em ambos (presente só em `aos-demo`, via
      `approval-card`).
- [ ] Está ligado ao Event Store, `platform/memory`, `substrate/otel-genai` e `platform/audit`, usando
      o **mesmo `Ingestor` e a mesma política** — é a consistência que o `doc.go` já promete.
- [ ] **Falsificável:** um run cujo objectivo contenha um padrão de PII produz, no Event Store e nos
      spans exportados, o valor **redigido** — e o teste falha se o motor for desligado.
- [ ] O `doc.go` é actualizado (a secção «Como verificar as afirmações acima» do AOS-195 mantém-se e
      passa a confirmar a presença, não a ausência).

**Dependências:** AOS-091 (motor), AOS-188.

---

### AOS-209 — Terminação TLS do nó (ingresso HTTP/SSE/DSAR + perna OTLP)

**Origem:** achado **(b) «Transporte em claro — sem dono»** de `tecnica/17_Analise_STRIDE.md` §5.2,
produzido pelo AOS-194. É a lacuna que a própria análise recomenda **escalar primeiro**.

**Porque não existia:** `specs/EPIC-15` registava «TLS/mTLS por endurecer» numa nota entre parênteses,
**sem lhe atribuir ticket**. Procurar `TLS` nas linhas de ticket de `specs/` devolve **zero**: nenhum
`AOS-NNN` do backlog possuía a terminação TLS do nó.

**O que está em causa.** Contra um atacante na rota, o transporte em claro degrada *Tampering*
(§4.10-T) e *Information disclosure* (trajectória e telemetria), e corrói o **valor prático** da
assinatura do canal de controlo: a assinatura ed25519 continua íntegra, mas o conteúdo transportado é
observável. Toda a identidade real (AOS-174/175/176/177), a soberania de leitura (AOS-172) e o canal
autenticado (AOS-160/193) assentam num transporte que qualquer intermediário lê.

**Âmbito — INGRESSO e a perna de saída da telemetria.** A saída do Model Gateway tem a **mesma raiz**
(`http.DefaultClient` sem timeout/TLS próprios, `BaseURL` sem validação de esquema — §4.7-T) mas **já
tem eixo: AOS-184**, com os quatro CA por satisfazer. Este ticket **não** o duplica.

**Nota de zero-dep:** `crypto/tls` é **stdlib**. A terminação TLS no próprio nó **não** colide com o
ponto 1 do ADR-017 nem exige a disciplina de excepção da emenda 1.3 da Carta.

**Decisão de desenho a tomar no ticket (e a justificar):** terminar TLS **no nó** (`crypto/tls`) ou
**a montante** (ingress/malha de serviço). A recomendação da análise é **suportar o primeiro e permitir
o segundo explicitamente** — um *opt-out* declarado e visível, nunca um default silencioso. O que não
se tolera é o estado actual: texto-claro por omissão, sem o operador o saber.

**Critérios de aceitação**

- [ ] O nó **serve TLS** no ingresso (API, SSE de trajectória, `/dsar/erase`) com certificado e chave
      configuráveis por ficheiro montado, no padrão de `AOS_ISSUER_KEY_PATH` — e **sem material
      privado em variáveis de ambiente**.
- [ ] **Extensão do bind-guardrail (quarta conjunção).** O `controlAuthenticated()` de
      `packages/cmd/aos/api.go:946` já exige `SteerAuth` + identidade real + ≥1 operador (AOS-193).
      Passa a exigir também **transporte cifrado**: um bind **não-loopback em texto-claro** é
      **RECUSADO** com o erro tipado próprio. É a mesma disciplina, não um mecanismo novo.
- [ ] **Opt-out explícito e ruidoso** para quem termina TLS a montante: variável dedicada que
      **declara** a terminação externa, com **aviso proeminente no banner** (no modelo do kill-switch de
      soberania, AOS-203) a dizer que o nó está a servir em claro e **por decisão de quem o configurou**.
      Em `AOS_MODE=production`, sem TLS **nem** opt-out declarado, o arranque **recusa**.
- [ ] A **perna OTLP** do exporter usa TLS e autentica-se perante o colector (ou declara a ausência com
      eixo nomeado). Mantém-se o **fail-open** de AOS-173: uma falha de telemetria **nunca** quebra um run.
- [ ] **mTLS do plano de controlo:** ou é entregue, ou fica **deferido com eixo nomeado** e entrada no
      `docs/governance/REGISTO-Deferimentos.md`. Não pode voltar a ficar numa nota entre parênteses.
- [ ] `deploy/node/README.md` documenta certificados, rotação, o opt-out e a postura por `AOS_MODE`,
      no estilo das secções de AOS-191/193/203.

**Critérios falsificáveis (provas negativas exigidas)**

- [ ] Bind a `0.0.0.0` **sem** TLS e **sem** opt-out declarado ⇒ **recusa** (`ErrRefuse…`), com o
      output capturado. Hoje **aceita**.
- [ ] Com TLS ligado, um cliente que fale HTTP em claro contra a porta TLS **falha**; o `aos steer`
      assinado sobre TLS **é aceite** (prova dos dois sentidos — não basta apertar).
- [ ] `AOS_MODE=production` sem TLS nem opt-out ⇒ **não arranca**.

**Dependências:** AOS-166 (API e bind-guardrail), AOS-193 (guardrail que discrimina), AOS-167 (SSE —
ligações longas sobre TLS), AOS-172 (DSAR), AOS-173 (exporter OTLP), AOS-168 (imagem/porta exposta).
**Não duplica:** AOS-184 (egresso do Model Gateway).
**Fecha:** a nota órfã de `specs/EPIC-15` e o achado §5.2-b de `tecnica/17`.

---

### AOS-210 — Tracer do dispatcher durável no composition root (`aos.activity` na árvore do nó)

**Origem:** o **residual NOMEADO** por AOS-204 em `docs/reports/AOS-169-aceitacao-sistemica.md` **§6.3**,
apurado por leitura de código ao fechar §13.6. A própria §6.3 declara que a pendência «**não tem dono
atribuído**» e tem de ser entregue a quem detém `packages/integration` — este ticket é esse dono.

**O defeito.** `packages/integration/secured.go` compõe o dispatcher durável com
`activity.NewDispatcher(rm, cfg.Ledger)` — **sem lhe passar tracer**. O default de `activity.Dispatcher`
é `agentruntime.NoopTracer{}` (`activity/dispatch.go`), e a opção existe (`activity.WithTracer`). Logo,
com `AOS_DURABLE_EXECUTION` ligado, o span **`aos.activity`** (`OpActivity`) **não é exportado**.

**Porque não existia.** O mecanismo é de EPIC-08 e **está entregue**: AOS-076 (semconv/árvore) e AOS-021
(o span de escopo durável) têm o comportamento provado ao nível de COMPONENTE em
`kernel/agent-runtime/activity/dispatch_test.go`. Nada em EPIC-08 falta. O que faltava era o
**composition root** passar o tracer que já constrói — a classe de dívida que EPIC-14 existe para
resolver («compõe as bibliotecas com o enforcement real, não *stubbed*»), e o mesmo padrão do AOS-206
(mecanismo entregue, chamador de produção inexistente). Por isso o **epic de EXECUÇÃO é EPIC-14**, não
EPIC-08: atribuí-lo a EPIC-08 mandaria re-entregar um mecanismo que já passa nos seus testes.

**O que está em causa (e o que NÃO está).** O ramo `execute_tool` **sai na mesma** — o RM recebe o tracer
do RT (`loop.go`, `rt.rm.SetTracer`) e é a **única** autoridade desse span (AOS-076) —, pelo que **§13.6
não reabre**. O que falta é a camada **INTERMÉDIA** da árvore: é no `aos.activity` que vivem o desfecho
durável (`permit|dedup|replay|denied|error`) e o **custo do efeito real**. Sem ela, um auditor de um nó
com execução durável vê o efeito mas **não vê se ele foi de facto executado ou deduplicado**.

**Restrição de desenho (o eixo central).** O comentário de `activity.OpActivity` diz que a separação
entre `aos.activity` e `execute_tool` é **DELIBERADA**, e nomeia o que ela evita: (a) **duplicar** o span
`execute_tool` — o duplo-contar em agregadores por-operação quando o mesmo tracer é partilhado com o RM —
e (b) apresentar um `execute_tool` sem os atributos obrigatórios de CA2 (`hash(tool+args)` +
`result_taint`), que só o RM anota. **A separação de operações é deliberada; não passar tracer nenhum não
é.** Passar o tracer tem de produzir `aos.activity` como **PAI** e `execute_tool` **uma só vez** — se
duplicar, o remédio é pior que a doença e a correcção reverte-se.

**Forma da mudança.** `SecuredConfig` **não** tem campo de tracer, e as vias existentes
(`RuntimeOptions []agentruntime.Option`, `FreezeOptions []toolset.Option`) são **fatias de funções
opacas**: não há como extrair delas o tracer sem as aplicar a um alvo falso. A via é, por isso, um campo
**explícito** em `SecuredConfig`, que o nó preenche com o **MESMO** tracer que já entrega às outras duas.

**Critérios de aceitação**

- [x] **ENTREGUE** — `SecuredConfig` expõe uma via **explícita** de tracer (campo próprio, não extracção de opções
      opacas) e `secured.go` passa-a a `activity.NewDispatcher` via `activity.WithTracer`.
      *(Campo `SecuredConfig.Tracer` (`packages/integration/secured.go`), documentado com a razão de NÃO
      extrair de `RuntimeOptions`/`FreezeOptions` e com a **invariante do chamador** — o mesmo valor de
      tracer nas duas vias; um tracer diferente produz uma árvore partida sem erro de construção.)*
- [x] **ENTREGUE** — `packages/cmd/aos/bootstrap.go` preenche-a com o **MESMO** tracer que já passa em
      `RuntimeOptions`/`FreezeOptions`, e **apenas** quando a observabilidade está ligada.
      *(`chainTracer` atribuída SÓ dentro do `if tracingEnabled` já existente, ao lado de
      `toolset.WithTracer(tracer)` e `agentruntime.WithTracer(tracer)` — é literalmente a mesma variável.)*
- [x] **ENTREGUE** — **Falsificável (árvore):** com `-race`, contra um colector OTLP `httptest` e com
      `AOS_DURABLE_EXECUTION` ligado, a árvore **exportada pelo nó** contém `aos.activity` e
      `parentSpanId(execute_tool) == spanId(aos.activity)` no mesmo `traceId`. A asserção é sobre a
      **topologia**, não sobre a presença de nomes.
      *(`TestAOS210_DurableExecutionExportsActivitySpanAsParentOfExecuteTool`,
      `packages/cmd/aos/observability_durable_test.go`: `obsAssertChildOf` verifica mesmo `traceId` **e**
      `child.ParentSpanID == parent.SpanID`. **Mutação executada** (aplicada e revertida): deixando
      `chainTracer` por atribuir em `bootstrap.go` — a ligação removida —, o teste fica VERMELHO com
      `vieram 0; nomes vistos: [registry.freeze_toolset chat audit_seal execute_tool chat invoke_agent]`,
      isto é, a restante árvore intacta e SÓ a camada intermédia em falta. Ao nível de pacote,
      `TestAOS210_DurableDispatcherTracerProducesActivityParentOfExecuteTool`,
      `packages/integration/durable_activity_tracing_test.go`.)*
- [x] **ENTREGUE** — **Falsificável (não-duplicação):** nessa mesma árvore, `execute_tool` aparece **exactamente uma
      vez**. Duas ocorrências ⇒ o ticket falha e a mudança reverte-se.
      *(Contagem exacta nos DOIS níveis: no nó sobre o wire OTLP e no pacote sobre `RecordingTracer`. O mesmo
      teste do nó exige também exactamente 1 `aos.activity` e 1 `invoke_agent`, e verifica que o `execute_tool`
      mantém os DOIS atributos de CA2 — `aos.tool_call.hash` **e** `aos.tool.result_taint` —, que só o RM anota:
      a separação de operações mantém-se DELIBERADA, não colapsada.)*
- [x] **ENTREGUE** — **Retro-compatibilidade:** sem tracer configurado (nó sem `AOS_OTLP_ENDPOINT`), o dispatcher fica
      com `NoopTracer`, **nenhum** span novo é emitido e o desfecho observável do run é **idêntico**.
      Prova negativa directa exigida (com o tracer ligado ao RT/RM mas **não** ao dispatcher: zero
      `aos.activity`, `execute_tool` na mesma — sem isso a ausência seria vacuosa).
      *(`TestAOS210_WithoutChainTracerNoActivitySpanIsEmitted` é a prova negativa directa e não-vacuosa;
      `TestAOS210_DurableExecutionWithoutTracerKeepsBehaviourAndExportsNothing` compara o desfecho por
      igualdade de struct com o do nó instrumentado e verifica ZERO corpos num colector a correr ao lado;
      `TestAOS210_NodeWithoutObservabilityOpensNoExporter` sela `node.otlp == nil`.)*
- [x] **ENTREGUE** — `docs/reports/AOS-169-aceitacao-sistemica.md` §6.3 é actualizada: o residual **fecha** com a
      evidência, ou permanece aberto **com o que falta nomeado** (honestidade acima de verde).
      *(§6.3 passa a «FECHADO por AOS-210», com o texto ORIGINAL do achado preservado. Os DOIS residuais que
      sobram no `aos.activity` — custo por efeito real e `gen_ai.operation.name` — ficam nomeados **com dono**:
      **AOS-211**. Nomear sem encaminhar era a deriva que este ticket veio terminar.)*

> **Marcação e SHA.** Os CA acima são marcados na **mesma alteração** que os entrega: a spec — autoridade do
> backlog e fonte da RTM — não pode dizer «por cumprir» enquanto o relatório de aceitação diz «FECHADO»
> (a divergência entre fontes é precisamente o que AOS-192 veio impedir). A convenção `**ENTREGUE** <sha>`
> usada em AOS-204 só é aplicável **a posteriori** — o SHA não existe antes do commit —, pelo que a evidência
> aqui é o **nome do teste**, que é verificável sem ele: `go test ./ -race -run AOS210` em `packages/cmd/aos`
> e em `packages/integration`.

**Dependências:** AOS-021 (span de escopo durável), AOS-076 (semconv/árvore + autoridade do
`execute_tool`), AOS-173 (exporter OTLP do nó), AOS-180/AOS-191 (execução durável alcançável do
ambiente), AOS-204 (que nomeou o residual).
**Não duplica:** AOS-204 (que provou a árvore no despacho **directo**) nem AOS-078 (agregação de custo).
**Deixa em aberto (com dono):** AOS-211 — os dois atributos que faltam ao `aos.activity` agora exportado.

---

### AOS-211 — Os dois atributos em falta no `aos.activity`: custo por efeito real e `gen_ai.operation.name`

**Origem:** o **residual REMANESCENTE** que AOS-210 nomeou em `docs/reports/AOS-169-aceitacao-sistemica.md`
**§6.3** ao fechar o residual anterior. Existe porque a regra que justificou AOS-210 — *residual nomeado tem
de ter dono* — vale para quem a invoca: deixar estes dois pontos «nomeados, sem ticket atribuído» seria
reproduzir a deriva no mesmo parágrafo em que se declara tê-la terminado.

**O defeito (dois eixos, o mesmo span).** AOS-210 pôs o `aos.activity` na árvore **exportada pelo nó**. Uma
vez lá, duas lacunas do span deixaram de ser teóricas:

1. **Custo por efeito real ausente.** `activity/dispatch.go` anota `gen_ai.usage.cost_usd` quando
   `Activity.CostMicroUSD != 0`, mas o adaptador `integration.DurableDispatcher` (`runtime_ports.go`)
   traduz o `referencemonitor.Call` numa `activity.Activity` **sem** `CostMicroUSD` — o campo nem sequer é
   preenchido. Nesta via, a anotação **nunca dispara**. O **desfecho** durável (`permit|dedup|replay|denied|
   error`) é exportado; o **custo do efeito** não. É metade da razão de ser do span, segundo o comentário do
   próprio `OpActivity` («o `aos.activity` carrega o que o RM NÃO conhece: dedup, replay e o custo por efeito
   real»).
2. **`gen_ai.operation.name` ausente.** `startSpan` anota `tool`/`run_id`/`step_id` mas **não** o
   `AttrOperationName`. Consequências verificáveis: (a) `otelgenai.ValidateSpanData` resolve a operação por
   *fallback* ao `Name` do span, não encontra entrada em `requiredAttrs` e **aceita o span sem validar** — o
   único span da árvore durável isento do contrato semconv de AOS-076; (b) consumidores que leem
   estritamente o atributo, sem *fallback* — p. ex. o `operationOf` de `packages/platform/eval/spans.go` —
   **nunca** vêem este span como uma operação.

**O que NÃO está em causa.** Não é regressão nem foi introduzido por AOS-210: o conteúdo do span é de
AOS-021 e `registry.freeze_toolset` tem a mesma forma. Não há dupla-contagem em nenhum agregador — o custo do
turno vive no `chat` e o agregado do run no `invoke_agent`, ambos provados por
`TestObservabilityEndToEndExportsWellFormedOTLPWithCost`. **§13.6 não reabre por isto**; é uma lacuna de
COBERTURA do span, não uma afirmação falsa.

**Restrição de desenho.** Acrescentar `OpActivity` a `requiredAttrs` põe o span **sob contrato** — e um
contrato que o span não cumpra transforma um teste de conformidade verde em vermelho. Os dois eixos têm por
isso de andar juntos e na ordem certa: primeiro anotar, depois exigir. O custo é **opcional por natureza**
(`CostMicroUSD == 0` não emite: custo gratuito e custo desconhecido são indistintos) e por isso **não** pode
entrar na lista de obrigatórios — obrigá-lo faria falhar todo o `aos.activity` de tools sem custo apurado.

**Critérios de aceitação**

- [ ] `startSpan` de `activity/dispatch.go` anota `gen_ai.operation.name = aos.activity`, e `OpActivity`
      passa a ter entrada em `otelgenai.requiredAttrs` com os atributos que o span **sempre** carrega
      (`operation.name`, `tool`, `run_id`, `step_id`) — **não** o custo, que é opcional por desenho.
- [ ] Teste que falha ANTES da mudança: `ValidateSpanData` sobre um `aos.activity` sem
      `gen_ai.operation.name` deixa de ser **vacuosamente** aceite (hoje é aceite por não ter contrato).
- [ ] O adaptador `integration.DurableDispatcher` propaga um custo por efeito real para
      `Activity.CostMicroUSD` **a partir de uma fonte declarada** (ou o eixo fica **explicitamente
      deferido** com a razão escrita: a porta que o forneceria não existe). Nomear a ausência da fonte é
      resposta aceitável; deixar o campo em branco **sem** a nomear não é.
- [ ] Prova ao nível do NÓ, com `-race` e colector OTLP: o `aos.activity` exportado traz
      `gen_ai.operation.name`; e, se o custo for propagado, `gen_ai.usage.cost_usd` aparece **uma só vez por
      efeito real** — nunca em `dedup`/`replay` (senão um agregador soma N retries como N custos).
- [ ] Retro-compatibilidade: nenhum span deixa de ser emitido e nenhum consumidor existente passa a falhar
      (os testes de conformidade semconv de AOS-076 continuam verdes).

**Dependências:** AOS-210 (que pôs o span na árvore exportada), AOS-021 (o span), AOS-076 (contrato semconv).
**Não duplica:** AOS-078 (contabilidade de tokens/custo por span **do modelo** — aqui é o custo do **efeito**
de uma tool, outro eixo) nem AOS-210 (que fecha a ligação do tracer, não o conteúdo do span).

---

## 9. Definition of Ready (DoR) do epic

Antes de executar qualquer ticket:

- [ ] **D-18.1** fechada (decisão zero-dep-com-stubs vs real-wiring, com dono e registo na Carta §4.2).
- [ ] **D-18.2** resolvida (EPIC-17 ratificada ou retirada).
- [ ] Este epic **ratificado** (Estatuto deixa de dizer PROPOSTA).
- [ ] Confirmado que o aviso de sequenciamento §5 está entendido por quem executa AOS-181/183.

> A EPIC-17 foi executada com 8 tickets em trunk enquanto o seu Estatuto dizia «PROPOSTA» e o DoR estava por
> satisfazer. Registar aqui é a forma de não repetir.

## 10. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | 2026-07-26 | Emissão (PROPOSTA). Remedia os 29 achados sobreviventes da auditoria multiagente v4 (`analises/08_Relatorio_Auditoria_Multiagente_v4.md`): 14 tickets AOS-190..AOS-203 em cinco blocos, três decisões de dono (§4) e um aviso de sequenciamento de segurança (§5). | Equipa AOS |
| 1.1 | 2026-07-26 | **AOS-204** acrescentado (bloco C, P1/S) para possuir o eixo residual de VAC-01: exportar por OTLP, a partir do nó real, a árvore de um run **com tool call** (ramo `execute_tool`). Apurado ao executar AOS-192, que reabriu §13.6 do checklist de AOS-169 — um eixo reaberto sem ticket real viola o CA de AOS-196. | AOS-192 |
| 1.2 | 2026-07-27 | **AOS-210** acrescentado a §8-bis (P1/S, execução **EPIC-14**) para possuir o **residual NOMEADO** por AOS-204 em `AOS-169-aceitacao-sistemica.md` §6.3 — o dispatcher durável composto **sem tracer** em `integration/secured.go`, que suprimia o span `aos.activity` do nó com `AOS_DURABLE_EXECUTION` ligado. A própria §6.3 declarava a pendência «sem dono atribuído»; §8-bis passa a acolher também residuais nomeados, e o intervalo de tickets do cabeçalho (que ainda dizia AOS-208) é reconciliado com AOS-209/210. | AOS-204 |
| 1.3 | 2026-07-27 | **AOS-210 marcado ENTREGUE** (6/6 CA, evidência por nome de teste — a spec não pode dizer «por cumprir» enquanto §6.3 do relatório diz «FECHADO») e **AOS-211** acrescentado a §8-bis (P2/S, execução **EPIC-08**) para possuir o residual REMANESCENTE que AOS-210 nomeou: os dois atributos em falta no `aos.activity` agora exportado (custo por efeito real e `gen_ai.operation.name` sob contrato semconv). Nomear sem encaminhar, no mesmo parágrafo em que se declara ter terminado essa deriva, seria repeti-la. | AOS-210 |
