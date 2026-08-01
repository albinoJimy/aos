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
| Intervalo de tickets | **AOS-190 … AOS-225** (36 tickets). AOS-204 acrescentado por AOS-192 (eixo residual de VAC-01); **AOS-205…209 acrescentados pelo registo de deferimentos e pela análise STRIDE** (§8-bis) — não são remediação, são o trabalho substantivo que os deferimentos apontavam sem executor; **AOS-210 acrescentado por AOS-204** (o residual nomeado em §6.3 do relatório de aceitação sistémica, que a própria §6.3 declarava «sem dono atribuído»); **AOS-211 acrescentado por AOS-210** (os dois atributos que faltam ao `aos.activity` que AOS-210 pôs na árvore exportada — encaminhar em vez de voltar a nomear sem dono); **AOS-212 acrescentado por AOS-211** (o eixo do custo por efeito real que AOS-211 deferiu com razão nomeada em DEF-810 — a porta que forneceria o custo do efeito não existia, e nomear sem encaminhar seria a mesma deriva); **AOS-213 acrescentado por CON-02/DEF-903** (a superfície de administração de legal hold/expiração, cuja Opção C sequenciou a execução para DEPOIS de o apagamento ser real — agora desbloqueada pela entrega do núcleo do AOS-093); **AOS-214 acrescentado por AOS-093** (o residual nomeado «replay soberano de conteúdo selado»: a cifra por-titular tornou o conteúdo dos runs ciphertext no Event Store, pelo que um leitor que reconstrói/inspecciona um run selado precisa de decifração autorizada por soberania — o resume in-process já ficou resolvido em AOS-093); **AOS-215 acrescentado por AOS-093/DEF-302** (a KEK por-titular vive num `InMemoryKeyVault` demo-grade e o nó não tem costura para injectar um KMS/HSM — a porta `audit.KeyVault` existe mas `Config.DSARVault` é o tipo concreto; o KMS real é infra-org, a costura de injeção é código do nó); **AOS-217 acrescentado por auditoria adversarial** (achado A1: um run submetido sem `principal_nhi` em modo soberano persiste o conteúdo em CLARO no WAL — a cifra por-titular de AOS-093 só corre com titular != "" — e fica não-shreddable; sem fail-closed no submit soberano; o titular é ainda um campo de corpo desacoplado da credencial verificada do submissor); **AOS-218 acrescentado por auditoria adversarial** (achados ACHADO-2+ACHADO-1: o plano de controlo steer/pause é autenticado e persistido mas GRAVADO-MAS-INERTE — `WithSteerSource`/`NewLoopSteer` sem chamador de produção, o loop nunca consome a correcção; e ligar o steer activa a divergência espúria de `prompt_hash` porque o replay não capta a correcção); **AOS-219…225 acrescentados pelo programa de fixes das auditorias adversariais** — o padrão meta confirmado nas 9 auditorias (mecanismos sãos; lacuna sistemática no **wiring/imposição/veracidade** do composition-root do nó): guarda do taint por eficácia (AOS-219), superfície de bundle PDP sem a qual o nó nega toda a tool call (AOS-220), tamper-evidence WORM imposta (AOS-221), veracidade do fencing do lease (AOS-222), seam SSRF do Model Gateway (AOS-223), escopo de recall da memória por principal (AOS-224) e defesa-em-profundidade da identidade (AOS-225) |

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
| AOS-194 | Corrigir rastreabilidade do STRIDE e cobrir a superfície real do nó — **ENTREGUE (5/5 CA)**: órfandade fechada dos dois lados (RTM §6 + «Documentos relacionados» de EPIC-07/15) e automatização por AOS-198 | docs | M | P1 | STR-01, STR-06 | D |
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
- [x] O documento passa a ser referenciado (RTM e/ou epic), deixando de ser órfão.
      *(ENTREGUE em duas frentes: (1) **RTM** — a tabela §6 (rasto descendente) de `scripts/ci/rtm-regenerate.py` (`generate_section6`) ganhou as linhas `tecnica/15/16/17` e o mermaid passou de `tecnica/00..14` para `tecnica/00..17`; a entrada foi na FONTE do gerador, não no ficheiro gerado, e `tecnica/16` regenerada (`--check` sincronizada) já mapeia `tecnica/17` → EPIC-07/15/16. (2) **Epics** — `tecnica/17` consta dos «Documentos relacionados» de `specs/EPIC-07` e `specs/EPIC-15` (higiene referencial `256e186`). O documento deixou de ser órfão em ambas as direcções.)*
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
- [x] Legal hold e job de expiração (CON-02) recebem eixo/dono/data declarados ou superfície de administração.
      *(ENTREGUE — **decisão do dono** (Opção C, 2026-07-29): eixo/dono/gatilho declarados no registo (`DEF-903`, âncora `bootstrap.go`) — eixo **AOS-093**, dono **Dono do produto**, gatilho «AOS-093 entregue». A superfície de administração (rotas de hold + `ExpirationJob` composto) sequencia-se DEPOIS de a cifra por-titular tornar o apagamento real; princípio registado: **obrigação de produto**. Ver `docs/governance/DOSSIE-CON-02-legal-hold.md`. A dívida deixa de estar sem eixo/dono/data.)*

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

## 8-bis. Tickets gerados pelo registo de deferimentos, pela análise STRIDE e pelos residuais nomeados (AOS-205 … AOS-225)

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

O **AOS-212** é a terceira aplicação da mesma regra, agora ao próprio AOS-211: dos seus dois eixos, o do
`gen_ai.operation.name` foi entregue, mas o do **custo por efeito real** foi **deferido com razão nomeada**
(`DEF-810`) — não há, na via durável do nó, uma **fonte declarada** do custo do efeito: nem
`referencemonitor.Call`/`Decision`, nem o desfecho do `Apply` (`durable.Result`), nem o `activity.Result`
o transportam. Deferir com razão nomeada é a resposta que o próprio CA de AOS-211 abençoou; mas deixar o
`DEF-810` em `POR ATRIBUIR` seria repetir a deriva. AOS-212 é o executor: entrega a **porta** que faltava
(o custo a fluir do desfecho do efeito para o span, em tempo de `Apply`), deixando o **produtor real** de
custo (Model Gateway / tools pagas) explicitamente em EPIC-06.

Enquanto não existiam, 19 linhas do registo diziam `POR ATRIBUIR` — o que é honesto, mas não é
executável. Criá-los completa o objectivo §2 deste epic («pôr os deferimentos num registo único com
eixo, dono e gatilho **falsificáveis**»): um eixo sem executor não é falsificável.

**Não foi criado de propósito:** a cifra por-titular do substrato (família `3xx` do registo) **não**
recebe ticket novo. A arbitragem `A-DEF-301` (2026-07-27) apontou-a ao **AOS-093 existente**, cujo
primeiro CA diz «toda a PII persistida é cifrada com uma chave por titular» — sem restrição ao audit —
e cujos Detalhes Técnicos já nomeiam o Event Store. Criar um ticket novo **duplicaria** AOS-093 e daria
dois donos à mesma propriedade: *trocar um eixo errado por um eixo inflacionado é o mesmo defeito ao
contrário*. O que falta é **refinar** o CA de AOS-093 (pendência `P-3b`), não criar outro ticket.

Os **AOS-219 … AOS-225** fecham a **última camada de código do nó** que o programa de fixes das
auditorias adversariais deixou em aberto. Não são mecanismos novos: as nove auditorias confirmaram que os
mecanismos do nó são **sãos e testados** e que a lacuna é sistematicamente de **wiring/imposição/veracidade
no composition-root** — capacidades anunciadas mas **não-ligadas** (bundle PDP, steer), **não-impostas**
(tamper-evidence WORM, eficácia do taint) ou **falsamente alegadas** (o log de fencing). Cada um liga/impõe
um controlo na via fail-closed do nó, ou corrige um claim que anuncia o que não compõe. Depois destes, o nó
**impõe** cada controlo que declara; o que resta para uma **execução real** é **provisionamento de infra**
(D4/identidade, bundle+trust-anchor real, KMS/HSM, provider de modelo) — decisões do dono e ops, **não**
código do nó.

| ID | Título | Tipo | Est. | Prio | Epic de EXECUÇÃO | Origem |
|---|---|---|---|---|---|---|
| AOS-205 | Provisionamento do IdP de soberania: registo board→região e credencial forte do leitor/operador | feature | L | P1 | **EPIC-09** | nota `N-DEF-201` (10 linhas do registo) |
| AOS-206 | Compor o *promotion controller* do nó com `NewProductionRatificationGate` | feature | M | P1 | **EPIC-14** | nota `N-DEF-401` (DEF-03) |
| AOS-207 | Assinatura e atestação da imagem do nó (chave de release, in-toto/SLSA, verificação na entrega) | feature | M | P2 | **EPIC-05** | nota `N-DEF-501` (DEF-06) |
| AOS-208 | Ligação substantiva do motor de redacção ao Event Store, memória, `otel-genai` e audit | feature | M | P1 | **EPIC-09** | pendência de AOS-195 |
| AOS-209 | **Terminação TLS do nó** (ingresso HTTP/SSE/DSAR + perna OTLP) | feature | M | **P0** | **EPIC-15** | `tecnica/17` §5.2-b (AOS-194) |
| AOS-210 | Tracer do **dispatcher durável** no composition root: o span `aos.activity` na árvore do nó | fix | S | P1 | **EPIC-14** | residual §6.3 de `AOS-169-aceitacao-sistemica.md` (AOS-204) |
| AOS-211 | Os dois atributos em falta no `aos.activity`: **custo por efeito real** e `gen_ai.operation.name` sob contrato semconv | fix | S | P2 | **EPIC-08** | residual §6.3 de `AOS-169-aceitacao-sistemica.md` (AOS-210) |
| AOS-212 | Fonte declarada do custo por efeito real da tool: porta do desfecho do efeito → span `aos.activity` | feature | M | P2 | **EPIC-08** | `DEF-810` (eixo do custo deferido por AOS-211) |
| AOS-213 | Superfície de administração de legal hold e expiração (CON-02): rotas autenticadas de hold/release + `ExpirationJob` composto (crypto-shred no TTL) | feature | M | P1 | **EPIC-09** | `CON-02`/`DEF-903` (Opção C, sequenciada após AOS-093) |
| AOS-214 | Replay soberano de conteúdo selado: `ContentOpener` autorizado por soberania na reconstrução de um run selado (leitor autorizado decifra; não-autorizado nunca vê claro; shred aguenta) | feature | M | P1 | **EPIC-09** | residual (b) de `DEF-301`/AOS-093 |
| AOS-215 | Costura de custódia externa da KEK por-titular (DEF-302): `Config.DSARVault` injectável pela porta `audit.KeyVault` + custódia documentada; KMS/HSM real fica infra-org | feature | M | P2 | **EPIC-09** | `DEF-302`/AOS-093 |
| AOS-216 | Porta de envelope `WrapDEK`/`UnwrapDEK` para custódia HSM key-never-leaves: a KEK nunca sai do vault; impl de referência prova o contrato; fallback à via KEK-crua | feature | M | P2 | **EPIC-09** | residual HSM de `DEF-302`/AOS-215 |
| AOS-217 | Fail-closed do titular no submit soberano (achado A1): sem `principal_nhi` recusa; o titular é derivado/validado da credencial verificada — nenhum conteúdo persiste sem cifra por-titular | fix | S | **P1** | **EPIC-09** | achado A1 (auditoria adversarial da soberania) |
| AOS-218 | Ligar o plano de controlo steer/pause ao loop (achado ACHADO-2) + captar a correcção no replay (ACHADO-1): a correcção humana chega ao loop e o replay de um run com steer não diverge | fix | M | **P1** | **EPIC-02** | achados ACHADO-2+ACHADO-1 (auditoria adversarial durável) |
| AOS-219 | Guarda do taint com **eficácia, não presença**: `hasActiveTaintGate` exige conjunto `privileged` **não-vazio** + compor o `PrivilegedAuthorizer` real no ápice endurecido (o nó não pode alegar "produção" com TaintGate inerte) | fix | S | P2 | **EPIC-07** | achado RM (auditoria adversarial) / `DEF-808`/`DEF-809`/`DEF-604` |
| AOS-220 | **Superfície de carregamento do bundle PDP** (`AOS_POLICY_BUNDLE_DIR`→`pdp.Open(dir, WithTrustAnchor)`, fail-closed): sem bundle o nó entregue nega TODA a tool call mediada (`NewUnloaded`) — o trabalho real fica inalcançável | feature | M | **P1** | **EPIC-07** | achado #5 PDP (auditoria adversarial) / `DEF-604` |
| AOS-221 | **Imposição da tamper-evidence do WORM**: ligar `audit.Verify` no restart e pós-shred, re-encadear o hash no load (não só CRC), compor `audit.Signer`/checkpoint — o bootstrap alega "hash-chain valida" sem validar | fix | M | P1 | **EPIC-09** | achado #7 WORM/tamper (auditoria adversarial) |
| AOS-222 | **Veracidade do fencing** no lease de posse: corrigir os claims falsos (`service.go:468` log + comentário `hostRun`) que anunciam um `FencedAppender` não-composto, e declarar o limite real (cancel cooperativo + idempotência do ledger) em `ADR-018` | fix | S | P2 | **EPIC-02** | achado #10 lease/posse (auditoria adversarial) / `ADR-018` |
| AOS-223 | Endurecimento do seam do Model Gateway (**SSRF/transport**): `http.DefaultClient` nu → cliente com timeout/TLS/limite de redirect; validar `BaseURL` (https + allowlist) antes de qualquer chamada | fix | S | P2 | **EPIC-06** | achado #9 egress (auditoria adversarial) / eixo `AOS-184` |
| AOS-224 | **Escopo do recall da memória por principal**: a leitura de memória é escopada pela identidade verificada do principal (não write-only/global) — fechar antes de qualquer recall ser ligado no loop | fix | S | P3 | **EPIC-04** | achado #8 memória (auditoria adversarial) |
| AOS-225 | Defesa-em-profundidade da identidade: validar `len(IssuerPubKey)==32` no modo endurecido (ed25519) — o boundary já rejeita chave partilhada (AOS-193); esta é a asserção estrutural redundante no ápice | fix | S | P3 | **EPIC-16** | defesa-em-profundidade (auditoria adversarial) / eixo `AOS-193` |

---

### AOS-205 — Provisionamento do IdP de soberania

**Origem:** nota `N-DEF-201` do registo — **um só** ticket em falta, replicado por dez linhas em seis
ficheiros. O AOS-203 cobre a documentação e o kill-switch das variáveis; o **provisionamento** é este.

**Porque não existia:** o eixo estava escrito como «EPIC-09/10». O EPIC-09 entrega a *regra* de
soberania (AOS-094) e o EPIC-10 entrega topologia/DR (AOS-098…108) — nenhum dos onze tickets entrega
**provisionamento de identidade regional**. A Carta §4.2 marca **D7 como CONDICIONAL** a esse
provisionamento: a decisão estava registada, o ticket é que nunca existiu.

**Critérios de aceitação**

- [x] O registo board→região deixa de ser lido de `AOS_BOARD_REGIONS` e passa a vir de uma fonte de
      autoridade da organização, com **rotação** e **auditoria de alterações**. — **FEITO**:
      `SovereignRegionAuthority` (`packages/cmd/aos/sovereign_authority.go`) embrulha a regra
      `govsov.Registry` (AOS-094, NÃO duplicada) numa porta com `Rotate` (revisão monotónica) e SELA
      cada provisionamento/rotação na hash-chain WORM (partição `gov.sovereignty.authority`, sem PII);
      `AOS_BOARD_REGIONS` passou a ser a **semente**, não a verdade congelada. Prova:
      `TestSovereignAuthorityProvisionAndRotateAudited` (selos verificáveis, revisão sobe, resolução
      muda) e `TestSovereignAuthorityRequiresWORM` (sem WORM não é autoridade).
- [x] Os headers `X-Aos-Reader`/`X-Aos-Board` deixam de ser **auto-declarados**: o leitor de governação
      e o operador DSAR apresentam credencial forte (OIDC/mTLS) verificada contra esse IdP. — **FEITO**:
      `readGovernance.authorize` (`packages/cmd/aos/sovereignty.go`) deriva o board/reader das CLAIMS
      VERIFICADAS quando a credencial está composta; `oidcReadCredential`
      (`packages/cmd/aos/read_credential.go`) reutiliza o verifier REAL de AOS-174
      (`oidc.Verifier.Validate`, claim `board` acrescentado a `oidc.Claims`), sem reimplementar
      verificação. O endpoint DSAR (`dsar.go`) reutiliza o mesmo gate. mTLS fica como impl alternativa
      da mesma porta (`readCredentialVerifier`).
- [x] Em `AOS_MODE=production`, arrancar sem essa fonte **recusa** — hoje já recusa sem soberania
      configurada, mas aceita a configuração *self-hosted* como se fosse autoridade. — **FEITO**:
      `ErrProductionNeedsSovereignAuthority` (`packages/cmd/aos/main.go`) — produção exige
      `AOS_SOVEREIGN_OIDC_ISSUER`+`AOS_SOVEREIGN_OIDC_AUDIENCE` (config incompleta ⇒ `ErrBadSovereignOIDC`).
      Prova: `TestRunProductionRequiresSovereignAuthority` e `TestRunRejectsIncompleteSovereignOIDC`.
- [x] **Falsificável:** um pedido com `X-Aos-Board` forjado (board válido, credencial ausente ou de
      outro titular) é **recusado**; hoje passa. — **FEITO**:
      `TestReadPathForgedBoardRejectedCredentialAccepted` — (A) header `X-Aos-Board=govBoard` sem
      credencial ⇒ 404; (B) header a forjar `govBoard` mas credencial verificada afirma outro board ⇒
      404 (o header é ignorado, a claim decide); (C) credencial VÁLIDA com `board=govBoard` ⇒ 200. Os
      tokens são mintados em runtime por um IdP OIDC de teste com chave EFÉMERA (molde de AOS-174, sem
      segredos em código). Não-vacuo: prova o DENY e o PERMIT.

**Dependências:** AOS-094 (regra de soberania), AOS-174 (`HumanDirectory` OIDC), AOS-182.
**Fecha no registo:** DEF-201, DEF-203…DEF-211 (⇒ `MITIGADO`); acrescenta DEF-212/DEF-213
(`sovereign_authority.go`, residual do tenant concreto). Deferido com eixo: o **tenant concreto** do
IdP de soberania (o nó fica com o CONTRATO — fonte rotacionável+auditada e verificação de credencial).

---

### AOS-206 — Compor o *promotion controller* do nó com `NewProductionRatificationGate`

**Origem:** nota `N-DEF-401` — é o achado **DEF-03**.

**Porque não existia:** o AOS-159 entregou o mecanismo e o CA do *wiring* foi marcado `[x]` **sem
chamador de produção existir** (desmarcado por AOS-196, `d33c0ff`). O ADR-012 apontava o endurecimento
à **EPIC-13 — que é o epic de Frontend**.

**Critérios de aceitação**

- [x] O nó `aos` compõe um *promotion controller* real. — **FEITO**: `PromotionController`
      (`packages/cmd/aos/promotion.go`), composto INCONDICIONALMENTE no `Bootstrap` passo (5b) e
      exposto em `Node.Promotion`; `Promote` delega no gate de produção. `grep promotion|Promote`
      em `packages/cmd/aos/*.go` não-teste passou de **zero** a não-zero.
- [x] Esse controller usa a via sancionada `hitl.NewProductionRatificationGate`, que **força**
      `WithRatifyFreshness` + `WithRatifyNonceStore` e recusa a construção sem eles — **não**
      `NewRatificationGate` cru. — **FEITO**: `newPromotionController` chama só a via sancionada
      (nonce-store durável `hitl.NewEventStoreNonceStore(es)` + janela de frescura); guarda de fonte
      `TestNode_UsesSanctionedRatificationPathOnly` prova que `NewRatificationGate(` cru não é
      chamado em nenhum ficheiro de produção do nó (prova negativa, CA4).
- [x] **Falsificável:** um teste de ápice em que a mesma ratificação, re-submetida após consumo,
      devolve `ReasonRatificationReplayed` **através do caminho do nó**, não do gate isolado. —
      **FEITO**: `TestNodePromotionController_ReplayBlockedThroughNode` promove por
      `node.Promotion.Promote` (1ª ⇒ `ratified`) e, re-submetendo a MESMA `SignedApproval`, obtém
      `admit=false` com `ratification_replayed` selado no WORM do nó (só alcançável com o nonce-store
      durável da via sancionada composto — não-vácuo).

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

- [x] O motor entra no fecho transitivo de `packages/cmd/aos` e de `packages/integration` —
      `go list -deps ./...` devolve agora `github.com/aos-ref/substrate/redaction` em **ambos**
      os módulos (antes ausente; presente só em `aos-demo`, via `approval-card`). Cablagem no
      `Bootstrap` (`bootstrap.go`) via `integration.NewIngestionGateway` (`integration/ingestion.go`).
- [x] Está ligado ao Event Store, `platform/memory`, `substrate/otel-genai` e `platform/audit`, usando
      o **mesmo `Ingestor` e a mesma política** (`redaction.RemoveAllPolicy`) — uma única passagem de
      redacção alimenta as quatro portas em `IngestionGateway.IngestObjective`: registo de memória
      EPISÓDICA sobre o `EventStoreAdapter`, span `aos.ingest.redacted`, e selo WORM do HASH do payload
      já tratado. É a consistência que o `doc.go` promete.
- [x] **Falsificável:** `TestNodeRedactsRunObjectiveEndToEnd` (`cmd/aos/ingestion_redaction_test.go`)
      corre o nó com objectivo contendo PII sintética (RFC 2606) e exige o valor **redigido**
      (`[REDACTED:email]`) no span exportado, no registo de memória/Event Store e no prompt materializado
      que o LOOP consome, com a PII crua **ausente**. Provado não-vácuo: desligar `goal.Objective =
      ing.Redacted` (`service.go`) faz o teste FALHAR (`ingestion_redaction_test.go:156` — PII crua
      alcança o loop); restaurada, volta a verde.
- [x] O `doc.go` é actualizado: a secção «Como verificar as afirmações acima» do AOS-195 mantém-se e
      passa a **confirmar a presença** (o motor ESTÁ no fecho transitivo dos dois roots + demo; os
      comandos `go list -deps` devolvem 1 linha cada), sem reintroduzir over-claim.

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

- [x] **ENTREGUE** — O nó **serve TLS** no ingresso (API, SSE de trajectória, `/dsar/erase`) com certificado e chave
      configuráveis por ficheiro montado, no padrão de `AOS_ISSUER_KEY_PATH` — e **sem material
      privado em variáveis de ambiente**.
      *(`WithTLSFiles(certPath, keyPath)` + `serveListener` que escolhe TLS endurecido; a chave privada
      entra por `AOS_TLS_KEY_PATH` (ficheiro montado), nunca por env. Certificados de teste gerados em
      runtime em `t.TempDir()` (`genTLSCertFiles`, ecdsa P256+x509), zero material committado — gate
      secrets EXIT=0. **Residual NÃO-material nomeado:** a prova positiva sobre TLS
      (`TestTLSServesEncryptedAndRejectsCleartext`) exercita `/healthz` (200) e `/runs/{id}/steer` (202);
      SSE e `/dsar/erase` partilham o **mesmo** `http.Server`/listener/config TLS, cobertos **por
      construção** — asserção independente das três superfícies fica como melhoria de cobertura, não
      correcção de defeito.)*
- [x] **ENTREGUE** — **Extensão do bind-guardrail (quarta conjunção).** O `controlAuthenticated()` de
      `packages/cmd/aos/api.go:946` já exige `SteerAuth` + identidade real + ≥1 operador (AOS-193).
      Passa a exigir também **transporte cifrado**: um bind **não-loopback em texto-claro** é
      **RECUSADO** com o erro tipado próprio. É a mesma disciplina, não um mecanismo novo.
      *(`guardCleartext(addr, transportEncrypted)` (`api.go:1175`, função pura) devolve `ErrRefuseCleartextBind`
      no não-loopback em claro; loopback sempre permitido (retro-compat). `TestTLSCleartextNonLoopbackRefused`
      prova a recusa e `TestTLSServesEncryptedAndRejectsCleartext`/`TestTLSNodeTerminationSatisfiesGuard`
      provam o sentido oposto — não-vacuoso.)*
- [x] **ENTREGUE** — **Opt-out explícito e ruidoso** para quem termina TLS a montante: variável dedicada que
      **declara** a terminação externa, com **aviso proeminente no banner** (no modelo do kill-switch de
      soberania, AOS-203) a dizer que o nó está a servir em claro e **por decisão de quem o configurou**.
      Em `AOS_MODE=production`, sem TLS **nem** opt-out declarado, o arranque **recusa**.
      *(`AOS_TLS_EXTERNAL_TERMINATION` (`parseTLSExternalTermination`, `main.go:483`) — valor não-reconhecido
      **aborta** (`ErrBadTLSExternalTermination`), nunca degrada para false; banner ruidoso de aviso.
      `TestProductionRefusesWithoutTLSOrOptOut` (recusa `ErrProductionNeedsTLS`),
      `TestProductionAcceptsOptOutWithBanner`, `TestProductionAcceptsNodeTLSNoBanner`,
      `TestParseTLSExternalTerminationRejectsGarbage`.)*
- [x] **ENTREGUE** — A **perna OTLP** do exporter usa TLS e autentica-se perante o colector (ou declara a ausência com
      eixo nomeado). Mantém-se o **fail-open** de AOS-173: uma falha de telemetria **nunca** quebra um run.
      *(`hardenedOTLPTransport` (MinVersion TLS 1.2, suites AEAD/ECDHE, sem `InsecureSkipVerify`) no
      `http.Client` da goroutine de flush, **fora** do caminho do run. `TestOTLPExporterOverTLS` prova
      `Failed>0` contra colector não-confiável **sem quebrar o run** — fail-open preservado. A **autenticação
      forte** perante o colector (mTLS/bearer) fica deferida com eixo — DEF-012, ver CA seguinte.)*
- [x] **DEFERIDO (eixo nomeado)** — **mTLS do plano de controlo:** ou é entregue, ou fica **deferido com eixo nomeado** e entrada no
      `docs/governance/REGISTO-Deferimentos.md`. Não pode voltar a ficar numa nota entre parênteses.
      *(**Não entregue — deferido** por decisão do próprio ticket: `DEF-012` no registo (eixo **POR ATRIBUIR**
      com nota `N-DEF-012`; gate deferrals EXIT=0 reconhece-o). A terminação TLS cifra e autentica o
      **servidor** perante o cliente; a autenticação **mútua** por certificado de cliente do plano de controlo
      e a autenticação forte perante o colector OTLP ficam por entregar. Não é lacuna aberta: o plano de
      controlo já é autenticado na APLICAÇÃO por assinatura ed25519 no corpo (AOS-160), independente do
      transporte.)*
- [x] **ENTREGUE** — `deploy/node/README.md` documenta certificados, rotação, o opt-out e a postura por `AOS_MODE`,
      no estilo das secções de AOS-191/193/203.
      *(Secção «Terminação TLS do ingresso» + linhas `AOS_TLS_CERT_PATH`/`AOS_TLS_KEY_PATH`/
      `AOS_TLS_EXTERNAL_TERMINATION` na tabela de config, incluindo a exigência de produção `ErrProductionNeedsTLS`.)*

**Critérios falsificáveis (provas negativas exigidas)**

- [x] **ENTREGUE** — Bind a `0.0.0.0` **sem** TLS e **sem** opt-out declarado ⇒ **recusa** (`ErrRefuse…`), com o
      output capturado. Hoje **aceita**.
      *(`TestTLSCleartextNonLoopbackRefused` — `guardCleartext` devolve `ErrRefuseCleartextBind` no não-loopback em claro.)*
- [x] **ENTREGUE** — Com TLS ligado, um cliente que fale HTTP em claro contra a porta TLS **falha**; o `aos steer`
      assinado sobre TLS **é aceite** (prova dos dois sentidos — não basta apertar).
      *(`TestTLSServesEncryptedAndRejectsCleartext` — cliente em claro contra porta TLS não obtém 200; `/steer`
      assinado sobre TLS ⇒ 202 e a correcção chega ao `SteerChannel` (não-vacuoso).)*
- [x] **ENTREGUE** — `AOS_MODE=production` sem TLS nem opt-out ⇒ **não arranca**.
      *(`TestProductionRefusesWithoutTLSOrOptOut` — `ErrProductionNeedsTLS`.)*

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

- [x] `startSpan` de `activity/dispatch.go` anota `gen_ai.operation.name = aos.activity`, e `OpActivity`
      passa a ter entrada em `otelgenai.requiredAttrs` com os atributos que o span **sempre** carrega
      (`operation.name`, `tool`, `run_id`, `step_id`) — **não** o custo, que é opcional por desenho.
      *(ENTREGUE: `activity/dispatch.go` `startSpan` emite `AttrOperationName=OpActivity` como 1.º atributo; `OpActivity` nasce em `otel-genai/semconv.go` (folha substrato, para poder ser chave sem o substrato importar o kernel) e o kernel `activity.OpActivity` REFERENCIA-o (single-source, anti-deriva por construção); entrada em `otel-genai/contract.go` `requiredAttrs` com os 4 atributos, sem o custo. layer-lint VERDE.)*
- [x] Teste que falha ANTES da mudança: `ValidateSpanData` sobre um `aos.activity` sem
      `gen_ai.operation.name` deixa de ser **vacuosamente** aceite (hoje é aceite por não ter contrato).
      *(ENTREGUE: `TestAOS211_ActivityUnderContractIsNotVacuouslyAccepted` (`otel-genai/contract_test.go`); não-vacuidade provada empiricamente — remover a linha de `requiredAttrs` faz o teste FALHAR (aceitava por fallback ao `Name`), repô-la volta a verde.)*
- [~] O adaptador `integration.DurableDispatcher` propaga um custo por efeito real para
      `Activity.CostMicroUSD` **a partir de uma fonte declarada** (ou o eixo fica **explicitamente
      deferido** com a razão escrita: a porta que o forneceria não existe). Nomear a ausência da fonte é
      resposta aceitável; deixar o campo em branco **sem** a nomear não é.
      *(**DEFERIDO com razão nomeada** (a via aceitável da CA): `referencemonitor.Call`/`CallContext` e a `Decision` devolvida não carregam custo do efeito, e o `activity.Result` também não — não há fonte declarada na via durável do nó. `CostMicroUSD` fica a 0 DELIBERADAMENTE (0 não emite: custo gratuito e desconhecido indistintos), nomeado num comentário em `runtime_ports.go` + **DEF-810** no `REGISTO-Deferimentos.md` (eixo POR ATRIBUIR). Não é campo em branco mudo.)*
- [x] Prova ao nível do NÓ, com `-race` e colector OTLP: o `aos.activity` exportado traz
      `gen_ai.operation.name`; e, se o custo for propagado, `gen_ai.usage.cost_usd` aparece **uma só vez por
      efeito real** — nunca em `dedup`/`replay` (senão um agregador soma N retries como N custos).
      *(ENTREGUE: `TestAOS211_ExportedActivitySpanCarriesOperationName` (`packages/cmd/aos`, molde AOS-210, `-race` + colector OTLP `httptest`): o `aos.activity` exportado traz `gen_ai.operation.name=aos.activity` e NÃO traz `gen_ai.usage.cost_usd` — sela o eixo do custo deferido e impede uma regressão que emitisse custo zero/forjado.)*
- [x] Retro-compatibilidade: nenhum span deixa de ser emitido e nenhum consumidor existente passa a falhar
      (os testes de conformidade semconv de AOS-076 continuam verdes).
      *(ENTREGUE: a mudança só ACRESCENTA um atributo (via `startSpan`, fonte única) e uma const/entrada de contrato; suites `-race` verdes em `otel-genai`, `agent-runtime/activity`, `integration` e `cmd/aos`; conformidade semconv de AOS-076 verde.)*

**Dependências:** AOS-210 (que pôs o span na árvore exportada), AOS-021 (o span), AOS-076 (contrato semconv).
**Não duplica:** AOS-078 (contabilidade de tokens/custo por span **do modelo** — aqui é o custo do **efeito**
de uma tool, outro eixo) nem AOS-210 (que fecha a ligação do tracer, não o conteúdo do span).

---

### AOS-212 — Fonte declarada do custo por efeito real da tool: porta do desfecho do efeito → span `aos.activity`

**Origem:** `DEF-810` — o eixo do custo que **AOS-211 deferiu com razão nomeada**. A metade
`gen_ai.operation.name` foi entregue; esta é a metade que faltava um **produtor**.

**A lacuna (precisa).** O `aos.activity` **sabe** emitir `gen_ai.usage.cost_usd`: `activity/dispatch.go`
anota-o quando `Activity.CostMicroUSD != 0`, **uma vez por efeito real** (`applied`), nunca em
`dedup`/`replay` — essa disciplina já existe (AOS-021/AOS-211). Falta o **valor**: na via durável do nó
nenhum caminho o preenche. O efeito corre em `d.ledger.Apply(...)` e devolve `durable.Result{Status,
Payload}` — **sem custo**; `referencemonitor.Decision` e `activity.Result` também não o transportam; o
`integration.DurableDispatcher` (`runtime_ports.go`) constrói o `Activity` com `CostMicroUSD == 0`, hoje de
propósito e nomeado (`DEF-810`).

**O que NÃO é (não-duplicação — o eixo de risco).** O custo do **modelo** (tokens da inferência) já flui
por `resp.CostMicroUSD` (`loop.go`) para o span `chat` — é AOS-078, o custo do **turno LLM**. AOS-212 é o
custo do **efeito**: a acção com efeito colateral que o RM permite, no span `aos.activity`. Eixo diferente,
span diferente. O invariante a preservar é **sem dupla-contagem**: custo-de-modelo no `chat`,
custo-de-efeito no `aos.activity`, agregado no `invoke_agent` = soma.

**Decisão de desenho (a tomar e justificar).** Custo **medido** (do desfecho do efeito, no momento do
`Apply`) vs **declarado** (estimativa fixa por tool, à cabeça no `Call`/catálogo). Recomendação: **medido** —
`gen_ai.usage.cost_usd` afirma semanticamente um custo **real**; uma estimativa seria um valor errado num
atributo sob contrato semconv. **Subtileza crítica:** o custo é **sinal de observabilidade em tempo de
`Apply`, não estado durável** — é essa a razão de só se emitir em `applied` e nunca em `replay` (o replay
não re-incorre o efeito). Logo o custo **não** deve viver no `durable.Result` gravado no ledger (senão o
replay teria de o re-emitir ou suprimir): deve ser um **canal lateral** da closure do `Apply` para o span.

**Porque está genuinamente por-fazer.** No nó de referência as tools são stubs (`emptyCatalog`,
`referenceModel`) e **nenhuma incorre custo mensurável**. O produtor real (Model Gateway com custo, tools
pagas) é **EPIC-06**, fora do escopo zero-dep do nó. AOS-212 entrega a **porta** + a **prova** com uma tool
de referência que reporta um custo; o produtor real fica declarado em EPIC-06.

**Recorte.** DENTRO: o contrato do desfecho do efeito passa a transportar `CostMicroUSD` (canal lateral
`Apply`→span); o dispatcher anota o `aos.activity` a partir do **resultado do efeito** (não do `Activity`
de entrada), só em `applied`; uma tool de referência que reporta custo para provar o fio ponta-a-ponta.
FORA (deferido, EPIC-06/08): custo real por-tool do Model Gateway; consumo pelo `control-plane/budget`.

**Critérios de aceitação**

- [x] O contrato do desfecho do efeito transporta `CostMicroUSD` **por um canal lateral** (não no
      `durable.Result` gravado no ledger — replay não re-incorre custo); o dispatcher anota o `aos.activity`
      a partir do **resultado do efeito**, não do `Activity` de entrada, **só** em `applied`. *(Evidência:
      `referencemonitor.Decision.CostMicroUSD` — novo campo alimentado por `m.dispatch` via
      `Monitor.RegisterCosting`/`CostingToolFunc`, `reference-monitor/decision.go`+`monitor.go`;
      `activity/dispatch.go` capta-o na variável exterior `effectCostMicroUSD` fechada pela closure de
      `Apply` e anota o span só no ramo `applied`, NUNCA no `durable.Result`. Provas dedicadas:
      `TestRegisterCosting_CostSurfacesInDecision`, `TestDispatch_CustoSoNoApplied`,
      `TestDispatch_ReplayEmiteZeroCusto`.)*
- [x] **Falsificável, ao nível do NÓ** (`-race` + colector OTLP): um run cuja tool de referência reporta
      custo `C` produz `gen_ai.usage.cost_usd == C` no `aos.activity` exportado, **exactamente uma vez por
      efeito real**; um `dedup`/`replay` do **mesmo** `step_id` emite **zero** custo. Falha-antes: hoje lê o
      `Activity` de entrada, que é `0` na via do nó. *(Evidência ao nível do nó, `-race`:
      `TestAOS212_ExportedActivitySpanCarriesEffectCost` (applied → `C`) e
      `TestAOS212_DedupExportsZeroEffectCost` (commit-dedup cross-restart → `decision=='dedup'`, ZERO custo:
      a closure re-corre e re-reporta `C` na 2.ª vida, mas `applied==false` ⇒ o ramo que anota o custo não
      corre), `packages/cmd/aos/observability_activity_cost_test.go`.)*
- [x] **Sem dupla-contagem:** o custo-de-modelo permanece no span `chat`; o agregado no `invoke_agent`
      iguala `chat + efeito` sem duplicação (estende `TestObservabilityEndToEndExportsWellFormedOTLPWithCost`).
      *(Evidência: `TestAOS212_NoDoubleCountingModelVsEffectCost` — custo do modelo no `chat`, custo do efeito
      no `aos.activity`, agregado no `invoke_agent` = soma sem sobreposição.)*
- [x] **Retro-compatibilidade:** um efeito sem custo (`CostMicroUSD == 0`) não emite o atributo (inalterado);
      o custo continua **fora** de `otelgenai.requiredAttrs` (opcional por desenho, como AOS-211 fixou) e a
      conformidade semconv de AOS-076 mantém-se verde. *(Evidência: `dispatch.go` guarda `if
      effectCostMicroUSD != 0`; `otel-genai/contract.go` `requiredAttrs[OpActivity]` continua
      `{operation.name, tool.name, run.id, step.id}` sem atributo de custo; gate policy/semconv verde.)*
- [x] **Produtor real declarado, não fingido:** a tool de referência que reporta custo é rotulada como tal;
      o custo real por-tool (Model Gateway / tools pagas) fica **explicitamente deferido em EPIC-06**, não
      marcado entregue. *(Evidência: a prova usa `referenceCostingCounter` — tool de referência ROTULADA
      registada por `RegisterCosting`; as tools de referência de produção do nó usam `Register` e reportam 0
      (honesto — sem custo mensurável). O produtor real por-tool fica **DEFERIDO em EPIC-06**, não entregue:
      `Recorte` FORA + registo `N-DEF-810` como RESOLVIDO quanto à porta, produtor real em EPIC-06.)*
- [x] Zero dependências externas; sem segredos. *(stdlib + cedar-go apenas; gate `secrets` verde.)*

**Dependências:** AOS-021 (o span + disciplina dedup/replay), AOS-210 (tracer no dispatcher), AOS-211
(`operation.name` + custo opcional, uma vez por efeito real). **Não duplica:** AOS-078 (custo do **modelo**).
**Relação:** EPIC-06 (produtor real do custo) e `control-plane/budget` (consumidor eventual do agregado).
**Nota de tamanho:** **M, não S** — mexe num contrato durável transversal do kernel (o desfecho do `Apply`),
com atenção a **layering** e **fidelidade de replay**; é a diferença face à metade `operation.name` do AOS-211.

---

### AOS-213 — Superfície de administração de legal hold e expiração (CON-02)

**Origem:** `CON-02`/`DEF-903`. A decisão do dono (Opção C, 2026-07-29) **sequenciou** a superfície de
administração para DEPOIS de o apagamento ser real. O núcleo do **AOS-093** entregou essa realidade
(o conteúdo dos runs é hoje cifrado por-titular e o `/dsar/erase` torna-o irrecuperável), pelo que o
gatilho está desbloqueado: já **há** apagamento real para suspender (hold) e para expirar (TTL).

**A lacuna (verificada em código):** o `audit.LegalHold` está composto (`Node.DSARHolds`,
`HoldSubject`/`ReleaseSubject`/`HoldPartition`/`ReleasePartition` existem) mas **sem rota de
administração** — um operador não tem como colocar/levantar um hold sem código. E o
`audit.ExpirationJob` (AOS-092, TTL-por-classe, já salta titulares/partições sob hold) **não é
composto** no nó (0 chamadores de produção).

**O que entregar**

- [x] **Rotas autenticadas de legal hold:** `POST /dsar/hold` e `POST /dsar/release` (colocar/levantar
      por titular e/ou partição), autenticadas pela **mesma credencial forte** do `/dsar/erase`
      (`readGov.authorize`, AOS-205), com o **contrato subject_id = pseudónimo opaco** (rejeita PII),
      fail-closed, e cada acção **selada no WORM sem PII** (quem/quando/subject-pseudónimo).
      *(ENTREGUE: `packages/cmd/aos/legalhold.go` — `handleLegalHold`/`sealLegalHold`, registadas em
      `api.go`; partição `governance.legalhold`. Provas: `TestLegalHoldSealsWORMWithoutPII` (selo
      verificável + `assertNoPIIInPartition`), `TestLegalHoldRejectsNonPseudonymTarget`.)*
- [x] **`ExpirationJob` composto no nó** sobre um `RecordSource` dos registos classificados do Event
      Store e um `ExpirationSink` que **crypto-shred a chave por-titular** no fim do TTL (reutiliza o
      envelope de AOS-093 — `audit.SealContent`/KeyVault — a expiração é apagamento real, não no-op),
      **respeitando o legal hold** (o job já salta held). Conduzido por rota administrativa e/ou
      agendamento; o modo é declarado no banner.
      *(ENTREGUE: `bootstrap.go` (7c-bis) compõe `Node.ExpirationJob`; `retention.go` traz
      `eventStoreRecordSource` (eventos `replay.captured` por-titular) + `cryptoShredSink`
      (`vault.Delete`); conduzido por `POST /dsar/expire`; banner declara o modo. **Granularidade
      resolvida = POR-TITULAR**, residual nomeado com eixo — ver nota abaixo.)*
- [x] **Falsificável (dois sentidos):** (a) um hold colocado **bloqueia** um `/dsar/erase` subsequente
      e um titular held é **saltado** pela expiração; (b) após `release`, o erase/expiração **sucede**.
      *(ENTREGUE: `TestHoldRouteBlocksEraseThenReleaseAllows` (hold via rota bloqueia o erase; release
      via rota reabre-o) e `TestNode_AOS213_HeldSkippedByExpirationThenReleased` (held ⇒ `report.Held`,
      nada expira; após release ⇒ expira).)*
- [x] **Falsificável (expiração real):** um titular expirado pelo `ExpirationJob` fica **irrecuperável**
      (`audit.OpenContent` → `ErrDecrypt`) e a **hash-chain valida** — a mesma prova de AOS-093, agora
      pela via da expiração por TTL.
      *(ENTREGUE: `TestNode_AOS213_ExpirationRealErasure` (-race, nível do nó): após `ExpirationJob.Run`,
      `OpenContent`→`ErrDecrypt` E `audit.Verify(governance.retention)` valida; idempotência na 2ª
      passagem.)*
- [x] **Autorização:** um `/dsar/hold` ou `/dsar/release` **sem credencial forte** (ou forjado) é
      **recusado**; com credencial válida é aceite (dois sentidos).
      *(ENTREGUE: `TestLegalHoldRoutesRequireStrongCredential` (anónimo/board forjado ⇒ 403, sem hold
      aplicado; válida ⇒ 200 aplicado), `TestExpireRouteRequiresStrongCredentialAndExpires` e
      `TestLegalHoldRoutesOffWithoutSovereignty` (501 sem gate soberano).)*
- [x] `deploy/node/README.md` documenta as rotas, o TTL/retenção, o hold, e a postura por `AOS_MODE`.
      *(ENTREGUE: secção «DSAR / conformidade — apagamento, legal hold e expiração (AOS-172 / AOS-093 /
      AOS-213)» — tabela de rotas, wire JSON, retenção/granularidade e postura por `AOS_MODE`.)*

> **Residual nomeado (granularidade, eixo AOS-093/envelope).** O TTL é por-registo/classe mas o
> crypto-shred é por-CHAVE-DE-TITULAR (uma KEK embrulha todas as DEKs do titular), pelo que a
> expiração entregue é **POR-TITULAR**: a retenção diferencial por-classe DENTRO de um titular
> colapsa para a classe que expira primeiro. A granularidade fina por-registo exigiria custódia de
> chave por-registo ou *tombstones* no Event Store (re-arquitectura do envelope de AOS-093) — não
> previsto. Registado em `DEF-903` (`FECHADO-RESIDUAL`) e em `retention.go`.

**Fecha:** `CON-02`/`DEF-903` (a superfície que a Opção C sequenciou). **Depende de:** AOS-092
(mecanismo hold/expiração), AOS-093 (apagamento real), AOS-205 (credencial forte), AOS-166/193 (API).
**Não duplica:** AOS-093 (que entrega a cifra/erase; aqui é a **administração** de hold/expiração sobre ela).

---

### AOS-214 — Replay soberano de conteúdo selado

**Origem:** residual **(b)** de `DEF-301`/AOS-093 — «o REPLAY de um run selado exige acesso do leitor
ao vault do titular». A cifra por-titular do AOS-093 tornou o conteúdo dos runs **ciphertext** no Event
Store; um **leitor** que reconstrói/inspecciona um run selado precisa de **decifração autorizada**.

**O que JÁ está resolvido (não re-fazer):** o **resume durável in-process** — o step-ledger decifra no
`Rebuild` via `ContentOpener` (o run continua como si próprio, sob o vault do nó) e é **fail-closed sem
cipher** (`ErrSealedResultNoCipher`, provado em `TestLedger_SealedRecordWithoutCipher_FailsClosed`). Este
ticket **não** duplica isso — trata do lado do **LEITOR** (reconstrução/inspecção por um terceiro).

**A lacuna (verificada):** as capturas de não-determinismo (`capturePayload.SealedContent` +
`SealedSubject`) são seladas na escrita, mas a reconstrução **do lado do leitor** não tem um
`ContentOpener` **gated por soberania** — um leitor que reconstrua um run selado obtém ciphertext (ou a
reconstrução parte). O `ReplayEngine` já traz o padrão certo: `WithPayloadResolver(store, accessor)` com
um **Accessor AUTORIZADO** e `ErrPayloadAccessDenied` — falta ligar o opener por-titular ATRÁS desse gate,
e o gate tem de ser o **soberano** (a mesma autoridade do read-path de AOS-172/205).

**Peças a reutilizar (não reinventar):** `agentruntime.ContentOpener`/`ContentCipher` (a porta de
decifração já existe); `audit.OpenContent` (decifra o envelope, **fail-closed após shred** →
`ErrDecrypt`); o read-path soberano (`sovereignty.go`, `readGov.authorize` + `RegionFor` + selo D6 no
WORM); o `Accessor`/`WithPayloadResolver` do `ReplayEngine`.

**Critérios de aceitação**

- [x] Um **leitor autorizado por soberania** (credencial forte de AOS-205 + região resolvida pelo board do
      leitor) que reconstrói um run selado obtém o **conteúdo REAL decifrado**; a leitura sensível é
      **selada no WORM (D6)** sem PII. *(GET /runs/{id}/reconstruct atrás de D7+D6 — `sovereign_replay.go`;
      `replay.ReplayEngine.Reconstruct` + `WithContentOpener`; prova `TestNode_AOS214_AuthorizedReaderDecrypts`.)*
      **Nota (alcance do gate — RECONCILIADA por AOS-182/DEF-202):** a âncora de região passou a ser
      **por-run**. O deferimento AOS-182 (a ressalva original desta linha) foi entregue: a residência do run
      é **selada na criação** (`POST /runs` → `readGovernance.sealResidency`) a partir da resolução soberana
      **board→região** do SUBMISSOR (`RegionFor` fail-closed, a mesma regra de AOS-172/AOS-094 — não uma
      auto-declaração), durável e tamper-evidente numa partição WORM POR-RunID (`gov.residency/<run>`), e a
      reconstrução resolve-a via `readGovernance.authorizeRead` (em `admitSovereignRead`) exigindo
      **`leitor.região == run.região`** ANTES de compor o opener — cross-region ⇒ 404 uniforme
      não-enumerável, **nunca** decifrado. Fecha-se assim a coarseness anterior (um leitor cujo board resolve
      para região válida já **não** reconstrói um run residente noutra região). Ver a nota reconciliada em
      `sovereignty.go` (`readGovernance.authorizeRead`/`seal`, obrigação `gov.read.residency`). A âncora de
      autorização é **real, não-vácua E granular por-run** (board não resolvível ⇒ região negada ⇒ 404).
      RESIDUAL nomeado (retro-compat): um run SEM residência selada (legado/in-process, ou pré-existente ao
      deploy da soberania) é servido sem check — em modo soberano todo o run ingressado via `POST /runs`
      tem-na. Ver DEF-202 (FECHADO-RESIDUAL) no `REGISTO-Deferimentos`.
- [x] **Falsificável (dois sentidos):** um leitor **não-autorizado** (região errada, ou sem credencial)
      obtém **`ErrPayloadAccessDenied`** (ou ciphertext) — **nunca o texto em claro**; o autorizado obtém
      o claro. A prova exercita a âncora de autorização (não é vácua). *(`TestNode_AOS214_UnauthorizedReaderDenied`
      — endpoint 404 não-enumerável + motor sem opener ⇒ `ErrPayloadAccessDenied`; `TestReconstruct_*` no módulo replay.)*
- [x] **O shred aguenta o replay:** após `POST /dsar/erase` (KEK destruída), **mesmo** o leitor autorizado
      obtém **`ErrDecrypt`** na reconstrução — o direito ao apagamento vale também contra o replay. Prova
      ao nível do nó (`-race`). *(`TestNode_AOS214_ShredSurvivesReplay` — in-process `audit.ErrDecrypt` + endpoint 410 Gone.)*
- [x] **Legal hold:** um titular sob hold **não** é shredded, pelo que o replay autorizado **reconstrói**
      normalmente (o hold preserva a reconstruibilidade). *(`TestNode_AOS214_LegalHoldReconstructs`.)*
- [x] **Superfície de leitor:** decide e justifica ONDE vive a reconstrução do leitor no nó (compor o
      `ReplayEngine` atrás de um endpoint soberano, ou ligar o opener à via de leitura de trajectória que
      já sela D6) — sem abrir uma via que devolva claro sem passar pelo gate soberano. *(ESCOLHA: endpoint
      soberano dedicado GET /runs/{id}/reconstruct; a via de trajectória SSE foi rejeitada — transporta o
      log cru por seq e misturaria transporte com reconstrução. Justificação em `sovereign_replay.go`.)*
- [x] Reutiliza `ContentOpener`/`audit.OpenContent` e o read-path soberano — **sem** cripto nova, **sem**
      dependências externas, **sem** um segundo mecanismo de autorização. *(o opener é o mesmo
      `contentSealer`/`ContentCipher` que sela; o gate é `readGov.authorize` (AOS-205) + o selo D6 de AOS-172.)*

**Fecha:** o residual (b) de `DEF-301` (que passa a nomear só o resume, já resolvido). **Depende de:**
AOS-093 (cifra/opener), AOS-172/205 (read-path soberano + credencial forte), AOS-016/180 (replay).
**Não duplica:** AOS-093 (resume in-process, já fail-closed) nem AOS-213 (administração de hold/expiração).

---

### AOS-215 — Costura de custódia externa da KEK por-titular (DEF-302)

**Origem:** `DEF-302` (residual de AOS-093): o `DSARVault` é um `audit.InMemoryKeyVault` — as KEK por-titular
vivem em **memória do processo** (perdem-se no restart, sem custódia durável). Produção deve ligar um KMS/HSM.

**A lacuna (verificada):** a porta `audit.KeyVault` (`EnsureKey`/`Key`/`Delete`) **já existe** e a sua doc diz
«produção liga um KMS/HSM pela mesma porta». MAS o nó **não tem a costura**: `Config.DSARVault` é o **tipo
concreto** `*audit.InMemoryKeyVault` (não a interface) e `bootstrap.go` **hardcoda** `NewInMemoryKeyVault` —
uma deployment **não pode injectar** um vault externo sem tocar o binário. É a mesma classe de defeito que a
auditoria v4 nomeia (porta existe, artefacto não a expõe).

**Nuance de desenho a resolver honestamente (não varrer):** a porta `KeyVault` devolve a **KEK CRUA**
(`Key(keyRef) → []byte`) e `audit.sealPayload` embrulha a DEK **in-process** com essa KEK. Um KMS/HSM
**key-never-leaves** (o objectivo de um HSM) **não** devolve a chave crua. Logo: a costura de injeção serve
directamente um **key-service/software-KMS** que devolve chaves (custódia externa, melhor que memória), mas um
**HSM verdadeiro** exigiria uma porta de **envelope** (`WrapDEK`/`UnwrapDEK`, a operação corre DENTRO do HSM).
O ticket entrega a costura de injeção e **ou** acrescenta a porta de envelope **ou** nomeia-a como residual com eixo.

**Âmbito — código do nó vs infra-org:** o KMS/HSM **real** (AWS KMS, Vault, PKCS#11…) é **infra-org**,
análogo à custódia da chave do issuer (AOS-175) e ao tenant de soberania (DEF-201/212). O nó entrega o
**CONTRATO + a costura + um double de referência**; a impl concreta vive fora do binário.

**Critérios de aceitação**

- [x] `Config.DSARVault` passa a ser a **interface** `audit.KeyVault` (injectável); `Node.DSARVault` é a
      interface. Precedência de composição no molde do Event Store/WORM: injectado ⇒ usa-o tal-qual; senão ⇒
      `InMemoryKeyVault` de referência (declarado no banner como demo-grade, KEK em memória).
- [x] **Falsificável:** um vault **injectado** (double de referência/spy) é o que o caminho de cifra/shred usa —
      `EnsureKey`/`Key`/`Delete` passam por ELE, não pelo in-memory hardcoded; o `/dsar/erase` destrói a KEK
      **no vault injectado** e o conteúdo fica irrecuperável por essa via. Prova ao nível do nó (`-race`).
- [x] **Custódia documentada** (`deploy/node/`): quem detém as KEK, rotação, o seam KMS/HSM, e a **postura**
      (referência in-memory = KEK em memória, não-durável, demo-grade — declarada, não escondida).
- [x] **Nuance HSM resolvida:** ou uma porta de envelope (`WrapDEK`/`UnwrapDEK`) que um HSM possa suportar, com
      double de referência; **ou** o residual «HSM key-never-leaves exige porta de envelope» nomeado com eixo no
      registo (o key-service que devolve chaves funciona pela porta actual). — RESOLVIDO pela 2.ª via: o
      residual está nomeado com eixo em `DEF-302` (a porta actual serve um key-service/software-KMS que devolve
      chaves; o HSM key-never-leaves fica deferido com eixo AOS-093/envelope).
- [x] Zero dependências externas no binário do nó; o KMS/HSM real declarado **infra-org**, não entregue.

**Fecha:** `DEF-302` (a costura; o KMS real fica infra-org com eixo). **Depende de:** AOS-093 (envelope/vault),
AOS-070 (broker de credenciais). **Não duplica:** AOS-175 (custódia da chave do **issuer** — outra chave).

---

### AOS-216 — Porta de envelope `WrapDEK`/`UnwrapDEK` para custódia HSM key-never-leaves

**Origem:** o residual que **AOS-215** nomeou ao fechar `DEF-302`. A costura de custódia externa (AOS-215)
serve um key-service/software-KMS que **devolve chaves** (`audit.KeyVault.Key(keyRef) → []byte`), mas um
**HSM verdadeiro** (o objectivo de um HSM) **não** devolve a chave crua: o embrulho corre **DENTRO** do HSM.

**A lacuna:** `audit.SealContent` faz `vault.EnsureKey(subject)` para obter a **KEK crua** e `sealPayload`
embrulha a DEK **in-process** com essa KEK; `OpenContent` obtém a KEK crua e desembrulha. Um HSM
key-never-leaves não pode participar — a KEK teria de sair para o processo.

**O que entregar**

- [x] Uma porta de **envelope** — `audit.KeyWrapper` (ou extensão de `KeyVault`) — com `WrapDEK(subjectID,
      dek) → (wrapped, keyRef, err)` e `UnwrapDEK(keyRef, wrapped) → (dek, ok)`, em que **o wrap/unwrap corre
      dentro do vault** e a **KEK nunca entra no processo do nó**. — `packages/platform/audit/keywrapper.go`.
- [x] `SealContent`/`OpenContent` usam a porta de envelope **quando o vault a implementa** (a DEK é gerada e
      entregue ao wrapper; o `WrappedDEK` do `encryptedPayload` passa a ser o embrulho do HSM), com **fallback**
      à via KEK-crua actual quando o vault só implementa `KeyVault` — **sem quebrar** AOS-093/213/214/215. O
      formato de envelope é versionado retro-compativelmente por `key_ref` (`omitempty`) — a via KEK-crua
      serializa BYTE-A-BYTE como antes. — `packages/platform/audit/crypto.go`.
- [x] **Impl de referência** `InMemoryKeyWrapper` que prova o CONTRATO (embrulha/desembrulha internamente, a
      KEK **nunca** é devolvida ao chamador; `Delete` destrói a KEK ⇒ `UnwrapDEK` falha, crypto-shred aguenta).
      — `packages/platform/audit/keywrapper.go` (`Key()` devolve sempre `(nil,false)`).
- [x] **Falsificável (`-race`):** com um wrapper injectado, a cifra/decifração passa por `WrapDEK`/`UnwrapDEK`
      (a KEK crua nunca é pedida — um wrapper-spy que **falha `Key()`** ainda cifra/decifra); após shred
      (`Delete`), `UnwrapDEK` falha e o conteúdo é irrecuperável; a hash-chain valida. — `aos216_hsm_envelope_test.go`
      (ao nível do audit: gate que PANICA em `Key()`/`EnsureKey()`; hash do blob estável) E
      `packages/cmd/aos/aos216_hsm_envelope_test.go` (ao nível do nó: `key_ref` no blob do substrato; `Key`/`EnsureKey`
      a ZERO; `/dsar/erase` ⇒ `ErrDecrypt`).
- [x] **Custódia key-never-leaves documentada** e o HSM **real** declarado **infra-org** (o wrapper de
      referência é in-process, prova o seam; o HSM concreto — PKCS#11/KMS — vive fora do binário). —
      `deploy/node/README.md` (§Custódia da KEK — AOS-215/AOS-216/DEF-302).
- [x] Zero dependências externas no binário do nó. — stdlib `crypto/aes`,`crypto/cipher`,`crypto/rand` apenas.

**Fecha:** o residual HSM de `DEF-302` (a porta de envelope; o HSM concreto fica infra-org). **Depende de:**
AOS-215 (a costura de injeção), AOS-093 (o envelope DEK/KEK). **Não duplica:** AOS-215 (que injecta o vault
KEK-crua; aqui é a porta de embrulho que um HSM suporta).

---

### AOS-217 — Fail-closed do titular no submit soberano (achado A1)

**Origem:** achado **A1** da auditoria adversarial da cadeia de soberania/apagamento.

**O defeito (verificado):** a cifra por-titular de AOS-093 só corre com `Subject != ""`
(`nondeterminism_capture.go:246`); o titular do run é `goal.Principal.NHIID`, preenchido do **campo de
corpo** `req.PrincipalNHI` (`api.go:582`), **desacoplado** da credencial verificada do submissor. Em modo
soberano, `handleSubmit` autentica o submissor (`readGov.authorize`) mas **não** exige nem liga o titular:
um `POST /runs` com `principal_nhi:""` (credencial válida) + `DurableExecution` persiste **texto do modelo e
outputs de tools em CLARO no WAL** e fica **não-shreddable** (sem KEK por-titular). A produção fail-closed
(TLS/OIDC/board) **não** cobre o titular por-run — degrada em silêncio para o legado em claro.

**Critérios de aceitação**

- [x] Em modo **soberano** (`readGov` composto), o submit **RECUSA fail-closed** (`400`/`403`) um run sem
      titular resolúvel — nenhum run soberano é hospedado sem um `Subject` sob o qual cifrar.
      *(Evidência: `api.go` handleSubmit — `authorize` nega 403 sem principal resolvível + guarda de defesa
      em profundidade; `TestNode_AOS217_FailClosedNoResolvableTitular` — submit anónimo ⇒ 403, sem residência
      selada, run não hospedado, WAL sem conteúdo.)*
- [x] O titular do run é **derivado/validado contra a credencial verificada do submissor** (`submitter.principal`
      de `readGov.authorize`), não um campo de corpo auto-declarado — fecha também o achado **A7** (titular
      desacoplado da credencial). **Decisão (justificada): DERIVAR** — `req.PrincipalNHI = submitter.principal`,
      o campo de corpo é IGNORADO (opção mais simples e segura: o submissor não pode escolher um titular
      arbitrário nem vazio). *(Evidência: `api.go:594`; `TestNode_AOS217_SovereignSubmitDerivesTitular` — um
      DECOY no corpo é inerte, o conteúdo sela sob o submissor verificado.)*
- [x] **Falsificável (dois sentidos, `-race`):** (a) submit soberano sem/`""` titular ⇒ recusado, nada
      persiste; (b) submit soberano com titular válido ⇒ o conteúdo do run no WAL está **cifrado** (a PII
      sintética **não** aparece em claro — reutiliza a prova de grep-no-WAL de AOS-093) e é **shreddable**
      (`/dsar/erase` ⇒ `ErrDecrypt`). Um teste que prova a **fuga ANTES** do fix (não-vacuidade).
      *(Evidência: `aos217_titular_failclosed_test.go` — T1 `LeakWithEmptyTitular_Falsifiable` (fuga-antes:
      titular vazio ⇒ PII em claro no WAL), T2 `SovereignSubmitDerivesTitular` (grep-no-WAL cifrado +
      `ErrDecrypt` após erase), T3 fail-closed; suite `-race` verde.)*
- [x] **Retro-compat:** o modo **legado** (sem `readGov`) mantém-se; runs fora de produção não são forçados.
      *(Evidência: `TestNode_AOS217_LegacyModeTitularUnforced` — sem `BoardRegions` o titular do corpo é
      honrado; testes de AOS-093/182/208/213/214 verdes.)*
- [x] Sem segredos; gates bloqueantes verdes. *(secrets, deferrals, layer-lint, ref-lint, rtm verdes.)*

**Fecha:** o achado A1 (+A7). **Depende de:** AOS-093 (cifra por-titular), AOS-182/205 (submissor
autenticado). **Não duplica:** AOS-208 (redação — outra camada) nem AOS-182 (residência — outro atributo).

---

### AOS-218 — Ligar o steer/pause ao loop + captar a correcção no replay (achados ACHADO-2 + ACHADO-1)

**Origem:** achados **ACHADO-2** (ALTO) e **ACHADO-1** (latente) da auditoria adversarial da execução durável.

**O defeito (verificado):**
- **ACHADO-2 — gravado-mas-inerte.** `POST /runs/{id}/steer` autentica (ed25519, anti-replay durável, o
  aparato AOS-160/193/DEF-012 + a guarda de bind de 4 conjunções) e **persiste** um `control.steer`,
  marcando `PendingCorrection`. Mas `control.NewLoopSteer`/`WithSteerSource` **não têm chamador de produção**
  (grep vazio em `integration`/`cmd/aos`): `integration/secured.go` runtimeOpts acrescenta
  `WithCheckpointer`/`WithCapturer`/`WithActivityDispatcher` mas **nunca** `WithSteerSource`; `bootstrap.go`
  runtimeOpts = só `WithTracer`. Logo `rt.steer == nil` (`loop.go:381`) e o loop **nunca** consome a
  correcção nem pausa. O operador crê que corrigiu/pausou o run; não teve efeito.
- **ACHADO-1 — replay não capta a correcção.** A correcção entra no tail (`loop.go:395`
  `win.Append(tailFromCorrection(corr))`), alterando o `prompt_hash` do turno seguinte; mas o `TurnCapture`
  (`loop.go:350`) **não** a persiste e o `ReplayEngine.load()` **ignora** os eventos `control.steer`,
  reconstruindo o tail só de texto+tool-results. Ligar o steer (ACHADO-2) **activa** este bug: o replay de
  um run com correcção reporta uma divergência **falsa** de `prompt_hash` (RCA na direcção errada; run fiel
  marcado eval-fail; e em `FromStepID` estado de retoma silenciosamente errado).

**Restrição de sequência:** os dois têm de ir juntos — ligar o steer sem captar a correcção introduz a
divergência de replay. É a mesma disciplina de AOS-211 (anotar-depois-exigir): completo, ou nenhum.

**Critérios de aceitação**

- [x] **Steer chega ao loop:** o runtime de produção compõe `control.NewLoopSteer(node.Steer, gates)` e
      liga-o via `agentruntime.WithSteerSource` (em `bootstrap.go`/`secured.go`). O `gates func(runID) StateGate`
      resolve o `StateGate` durável por-run (a máquina de estados AOS-017) da fonte real do nó — decidir e
      justificar de onde vem (registo de runs / substrato durável), sem inventar mecanismo novo.
      *(Evidência: `bootstrap.go:940` `control.NewLoopSteer(steer, stateGates.Resolve)` → `SecuredConfig.SteerSource` → `secured.go:294` `WithSteerSource`; a costura por-run `runStateGates` em `packages/cmd/aos/steer_gates.go` reusa a `state.Machine` de AOS-017 sobre o Event Store do nó, com lazy-claim ready→running sob o fencing token do lease.)*
- [x] **Falsificável (steer efectivo, `-race`, ao nível do nó):** um `POST /runs/{id}/steer` assinado durante
      um run faz o loop **aplicar a correcção** (a correcção entra no tail do turno seguinte) e um `/pause`
      **pausa** o run (transição durável running→paused). Prova dos dois sentidos: sem a correcção, o loop
      não muda.
      *(Evidência: `TestNodeSteerCorrectionReachesLoop` + `TestNodeSteerPauseIsDurable` em `packages/cmd/aos/aos218_steer_wiring_test.go`, `-race` verdes.)*
- [x] **Replay fiel de um run com steer:** o `ReplayEngine` **capta/reconstrói** a correcção (consome
      `control.steer` do stream, ou capta-a no `TurnCapture`) e o replay de um run steerado **reproduz o
      `prompt_hash` SEM divergência**. Teste que **falha ANTES** do fix (a divergência espúria) e passa depois.
      *(Evidência: `TestReplaySteeredRunIsFaithful` — não-vácuo (prompt_hash do turno 2 steerado ≠ baseline) + falha-antes provado empiricamente; análogo selado `TestReplaySteeredSealedRunIsFaithful`; captura via `TurnCapture` de `LeadingCorrection` em `nondeterminism_capture.go`/`sovereign_content.go`.)*
- [x] **Resume-from-step** com correcção pré-segmento produz o **mesmo** `FinalStateHash` que o replay
      completo (fecha a janela de retoma silenciosamente-errada).
      *(Evidência: `TestReplaySteeredResumeEqualsFullReplay`, `-race` verde.)*
- [x] **Retro-compat:** runs **sem** steer são byte-idênticos (replay/resume inalterados); os testes de
      AOS-016/021/180/210/211 continuam verdes.
      *(Evidência: `TestReplayNoSteerByteIdenticalCapture`; lazy-claim em `steer_gates.go` não gera transição sem pause de facto; suites AOS-016/021/180/210/211 verdes com `-race`.)*
- [x] Reconciliar as afirmações de `steer_channel.go:38`/`aos-demo/main.go:292` («reproduz-se por replay») com
      o wiring agora real. Sem segredos; gates verdes.
      *(Evidência: doc-comment de `steer_channel.go` distingue sobreviver-a-crash (Rebuild) de fidelidade-de-replay do run steerado; `aos-demo/main.go:292` nota que a pausa através do loop já existe via `WithSteerSource`. Gates bloqueantes verdes, `secrets.sh` verde.)*

**Fecha:** ACHADO-2 + ACHADO-1. **Depende de:** AOS-158 (`LoopSteer`/`WithSteerSource`), AOS-160 (steer
autenticado), AOS-016/AOS-017 (durabilidade/StateGate), AOS-180 (replay/capturer). **Não duplica:** AOS-193
(auth do canal — aqui é o consumo pelo loop).

---

### AOS-219 — Guarda do taint com eficácia, não presença (achado RM)

**Origem:** achado MÉDIO da auditoria adversarial do Reference Monitor.

**O defeito (verificado):** `hasActiveTaintGate` (`packages/kernel/reference-monitor/production.go:72`) valida
**presença** (`g.privileged != nil`), não **eficácia**: o nó passa a guarda de "produção" com um `TaintGate`
**inerte** — conjunto `privileged` vazio ⇒ nenhuma promoção de escopo é jamais barrada, mas o ápice alega
estar endurecido. (O `TaintGate` inerte em si é deferimento declarado — `DEF-808`/`DEF-809`/`DEF-604`; este
ticket é a **guarda de eficácia** + compor o `PrivilegedAuthorizer` real, não a semântica do gate.)

**Critérios de aceitação**

- [x] `hasActiveTaintGate` (ou o predicado de "modo endurecido") **exige** o conjunto `privileged`
      **não-vazio** — um `TaintGate` com conjunto vazio **não** conta como activo; o nó **recusa fail-closed**
      arrancar em modo endurecido com um taint gate inerte (ou degrada o claim de postura de forma honesta e visível).
      *(Evidência: `hasActiveTaintGate` (`production.go:104`) chama `privilegedIsEffective(g.privileged)` →
      `StaticPrivilegedSet.HasPrivileged()` = `len(caps)>0` (`taint_gate.go`); o check **estrutural** separou-se em
      `hasWiredTaintGate:88` (retro-compat). Costura fail-closed `NewProductionHardenedTaint`+`ErrTaintGateInert`
      recusa o gate inerte. O ápice **não** faz alegação falsa de taint-hardened (os "hardened" em `bootstrap.go`
      são todos de identidade). `TestHasActiveTaintGate_EmptySetIsInert` — falha-antes provada por neutralização.)*
- [ ] O ápice de produção compõe um `PrivilegedAuthorizer` **real** (a fonte do conjunto privilegiado), não o
      vazio por defeito — decidir/justificar a fonte sem inventar mecanismo novo (eixo `DEF-808`/AOS-183).
      *(**EM FALTA POR DESIGN — honestamente deferido a `AOS-183`/`DEF-808`**: a fonte do conjunto privilegiado
      real não existe no código (`secured.go:209-211` cai em `NewStaticPrivilegedSet()` vazio; `bootstrap.go` nem
      fornece `Privileged`). Não foi fabricado — `bootstrap.go` **não** tocado. `NewProductionHardenedTaint` é a
      costura de adopção pronta para quando AOS-183 fornecer o conjunto real.)*
- [x] **Falsificável (`-race`):** um teste prova que, com conjunto vazio, o predicado de postura endurecida é
      **falso** (falha-antes: hoje é verdadeiro); e que uma promoção de escopo tainted é **efectivamente barrada**
      quando o autorizador está composto. *(Evidência: `production_efficacy_test.go` — `…EmptySetIsInert` +
      `…HardenedTaint_FailClosedOnInert` falham-antes (neutralização confirmada por QA+completude independentes);
      a barra efectiva de promoção tainted (vazio vs não-vazio) por `TestTaintEnforcement_EmptyVsNonEmpty`
      (corroboração **material**, rotulada com precisão — não é falha-antes do predicado); `-race` verde.)*
- [x] **Retro-compat:** modos não-endurecidos (demo/legado) inalterados. Sem segredos; gates verdes.
      *(Evidência: `NewProduction`/`NewProductionSecure` constroem com conjunto vazio via `hasWiredTaintGate`
      (lógica byte-idêntica à guarda antiga); `cmd/aos` + `integration` `-race`/build verdes; `go.mod`/`go.sum`
      intactos; secrets/deferrals/layer-lint verdes.)*

**Fecha:** o achado RM de eficácia-vs-presença (a metade de wiring de `DEF-808`/`DEF-809`; a semântica do gate
já existe). **Depende de:** AOS-183 (conjunto `Privileged`), AOS-157 (portas RT/RM). **Não duplica:** AOS-220
(bundle PDP — outra guarda) nem a semântica do `TaintGate` (AOS-069/ADR-005).

**Estado: PARCIAL — fecha o achado de veracidade; CA2 deferido.** O defeito do achado RM (o predicado alegar
endurecimento com um gate **inerte**) está **fechado**: o predicado passou a exigir eficácia e o ápice não mente.
O **conjunto privilegiado real** permanece deferido a `AOS-183`/`DEF-808` (fonte inexistente, não fabricada).
**Residual nomeado:** `Monitor.HasActiveTaintGate()` está exportado mas ainda **não é consultado no ápice** para
emitir um banner honesto de postura — ligá-lo exige editar `bootstrap.go`, a par da adopção de
`NewProductionHardenedTaint` quando AOS-183 fornecer o conjunto. Sem alegação falsa em disco entretanto.

---

### AOS-220 — Superfície de carregamento do bundle PDP (achado #5)

**Origem:** achado MÉDIO (alto impacto) da auditoria adversarial do PDP.

**O defeito (verificado):** `cfg.PDP` nunca é preenchido no ápice ⇒ o nó cai em `pdp.NewUnloaded` ⇒
**default-deny de TODA a tool call mediada**. O nó entregue **nega todo o trabalho real** pela via mediada —
o inverso silencioso do risco: seguro, mas inútil, e sem superfície para carregar política. (`DEF-604`: "o PDP
não carrega bundle".)

**Critérios de aceitação**

- [x] O ápice expõe uma superfície de carregamento fail-closed: `AOS_POLICY_BUNDLE_DIR` →
      `pdp.Open(dir, WithTrustAnchor(...))`; sem a variável, o comportamento default-deny mantém-se
      **explícito e declarado** (não um acidente silencioso). *(Evidência: `main.go:479-483` liga
      `loadPolicyBundleFromEnv()` a `nodeConfigFromEnv` preenchendo `cfg.PDP` (campo antes inalcançável pelo
      binário); loader `main.go:691-712` faz `pdp.Open(dir, pdp.WithTrustAnchor(anchor))`; sem env ⇒ `cfg.PDP`
      nil ⇒ `secured.go:205-207` compõe `pdp.NewUnloaded()`. `pdp.Open`/`WithTrustAnchor` existem — não inventados.)*
- [x] O **trust anchor** é forçado **out-of-band** (não do próprio bundle) — um bundle sem assinatura
      verificável pela âncora é **recusado** (fail-closed), não carregado. *(Evidência: `pdp.Open` só lê
      `trust_anchor.pub` do dir quando `len(anchor)==0` (`pdp.go:154`), logo a âncora de `AOS_POLICY_TRUST_ANCHOR`
      sobrepõe-se ao dir mutável; subteste "anchor VÁLIDO mas ERRADO" recusa o MESMO bundle que a âncora correcta
      carrega ⇒ `ErrPolicyBundleLoad`.)*
- [x] **Falsificável (`-race`, ao nível do nó):** com bundle válido carregado, uma tool call permitida pela
      política **passa** a mediação (hoje seria negada); com bundle ausente/assinatura inválida, **toda** a tool
      call é negada. Prova dos dois sentidos. *(Evidência: `aos220_pdp_bundle_surface_test.go` — subteste "bundle
      CARREGADO ⇒ tool call PERMITIDA passa a mediação e EXECUTA" assere `permits>0` (falha-antes: sem o fix
      `cfg.PDP` nil ⇒ `NewUnloaded` ⇒ deny); QA cortou a costura e confirmou empiricamente a falha-antes; `-race` verde.)*
- [x] **Retro-compat:** o binário sem `AOS_POLICY_BUNDLE_DIR` continua a arrancar (default-deny explícito), não
      quebra os testes existentes. Sem segredos (a âncora é chave pública, 32 bytes); gates verdes. *(Evidência:
      assere `cfg.PDP` nil sem env; suites `cmd/aos` `-race` verdes; `go.mod`/`go.sum` intactos; secrets/deferrals/
      layer-lint verdes; env documentado em `deploy/node/README.md`.)*

**Fecha:** `DEF-604` (metade do bundle PDP). **Depende de:** AOS-181 (o `pdp.Open`/bundle), AOS-005 (contrato
PDP). **Não duplica:** AOS-219 (taint) nem AOS-206 (promotion gate — outra via).

---

### AOS-221 — Imposição da tamper-evidence do WORM (achado #7)

**Origem:** achado MÉDIO da auditoria adversarial do WORM/Event Store.

**O defeito (verificado):** `audit.Verify` **nunca** é chamado em `cmd/aos` (nem no restart nem pós-shred); o nó
**não compõe** `audit.Signer`/checkpoint; `OpenFileStore` valida só **CRC** (não **re-encadeia o hash** no
load). A tamper-evidence é **latente, não imposta** — e o `bootstrap.go` comenta "hash-chain valida" **sem**
validar. Um WAL adulterado passa despercebido no arranque.

**Critérios de aceitação**

- [x] O arranque do nó **re-encadeia e verifica** a hash-chain do Event Store no load (não só CRC); uma cadeia
      adulterada **impede** o arranque fail-closed (ou marca o store como comprometido de forma visível e recusa servir).
      *(Evidência: `filestore.go:98` — `OpenFileStore` corre `verifyReplayedChain` por partição após o replay CRC e
      recusa o `Open` na 1ª cadeia partida; `bootstrap.go:696` — `audit.VerifyStore` no restart aborta o arranque
      (erro ≠ `ErrPartitionsUnavailable`). `TestWORM_LoadRejectsTamperedChain_CRCValid` + `TestNode_RestartRejectsTamperedWORM`.)*
- [x] `audit.Verify` é chamado nos pontos-chave (restart e **pós-shred**, para provar que o shred preservou a
      cadeia); o nó compõe `audit.Signer`/checkpoint (ou justifica a ausência honestamente, sem alegar o que não compõe).
      *(Evidência: restart `bootstrap.go:696`; pós-shred em **AMBOS** os caminhos — `handleErase` (`dsar.go:226`) e
      `handleExpire`/TTL (`legalhold.go:274`, ligado pela remediação em paridade). `audit.Signer`/checkpoint **NÃO**
      composto — justificado honestamente: o re-encadeamento é SHA-256, **sem chave privada no runtime**; a âncora
      assinada de frescura (que apanharia a truncatura do TAIL) fica no eixo **AOS-072**. `TestNode_VerifyWORM_PostShredPositive`
      + `TestExpireRoute_PostShredVerifiesWORM_FailClosed` + `TestVerifyStore_DetectsTamperedPartition`.)*
- [x] **Falsificável (`-race`):** um teste adultera um registo do WAL e prova que a verificação **detecta**
      (falha-antes: hoje o load passa com CRC intacto mas hash-chain partida). O comentário "hash-chain valida"
      só permanece se a validação **existir**. *(Evidência: `aos221_worm_tamper_test.go` — o tamper **recalcula o CRC**
      do frame (framing intacto) e parte a hash-chain, atingindo o vector "CRC-válido, hash-chain-partida";
      `TestWORM_LoadRejectsTamperedChain_CRCValid` prova o sentido-antes (`replayAuditWAL` aceita) e o sentido-depois
      (`OpenFileStore` recusa); a doc-comment de `VerifyWORM` tornou-se **verdadeira por wiring**, não reescrita; `-race` verde.)*
- [x] Sem segredos; gates verdes (incl. selftest). *(Evidência: sem chave privada no runtime do audit;
      `go test -race` verde em `cmd/aos` + `platform/audit`; `bash scripts/ci/selftest.sh` — TODOS OS SELF-TESTS OK;
      `deferrals`/`layer-lint` verdes; `go.mod`/`go.sum` intactos.)*

**Fecha:** o achado #7 (tamper-evidence imposta). **Depende de:** AOS-093 (hash-chain/envelope), AOS-170 (Event
Store durável). **Não duplica:** AOS-214/AOS-215 (decifração/custódia — outra propriedade).

**Residual nomeado (não defeitos):** (1) a **truncatura do TAIL** (remover os registos mais recentes) é o único
vector que o re-encadeamento SHA-256 não apanha — exige uma **âncora de frescura assinada** (`audit.Signer`/checkpoint
com `AuditSeq==head`), cuja selagem usaria a chave **privada** do operador que, pela regra do nó, **não vive no
runtime** → deferido a **AOS-072** (custódia out-of-process, molde AOS-156). (2) Um WORM **injectado por config** que
não implemente `audit.PartitionLister` devolve `ErrPartitionsUnavailable` e não é verificável pelo nó (carve-out
**declarado** no banner) — os stores próprios do nó (`FileStore`/`MemStore`) implementam-no sempre e o `FileStore`
auto-verifica no `Open`; fronteira de custódia do chamador, não fail-open silencioso.

---

### AOS-222 — Veracidade do fencing no lease de posse (achado #10)

**Origem:** achado MÉDIO da auditoria adversarial do lease/posse (`worker.Assigner`) — a lacuna que o dono
insistiu em cobrir.

**O defeito (verificado):** o nó **anuncia** "fencing" — `service.go:468` (log de heartbeat) e o comentário de
`hostRun` — mas **não compõe** `FencedAppender`/`worker.Worker` (dark-code). O anti-duplo-efeito real é **cancel
cooperativo + idempotência do step-ledger**, declarado noutro sítio. É um defeito de **veracidade do log**:
alega uma barreira que não existe naquele caminho.

**Critérios de aceitação**

- [x] Os claims falsos são corrigidos: o log (`service.go:491`) e a doc-comment do caminho de posse **deixam de afirmar**
      fencing que não compõem; passam a nomear o mecanismo **real** (lease por CAS atómico `worker.Assigner` +
      idempotência do step-ledger `f(RunID,StepID)` + cancel cooperativo). *(Evidência: os DOIS claims falsos viviam
      em `heartbeat` — a spec dizia `hostRun`/`:468` imprecisamente; ambos corrigidos e a **negar** o fencing de
      escritas. A menção legítima `hostRun:442` ("fencing token do lease", threaded a `stateGates.Open`, AOS-218)
      mantida. Adicionalmente afinei uma sobre-atribuição suave adjacente em `Shutdown` (`:567`): "seria fenced" →
      supersede real + idempotência nomeada.)*
- [x] `ADR-018` (ou o ADR do lease) **declara o limite**: v1 usa lease+idempotência, **não** `FencedAppender`; o
      `FencedAppender` fica eixo nomeado (threading do lease token ao `Runtime.Run`) para quando for composto.
      *(Evidência: `ADR-018 §5-bis` — nomeia (1)+(2)+(3) reais; declara que o `FencedAppender` existe no kernel e é
      exercitado em integração/DR mas **não** é cablado no nó (sem chamador de produção de `NewFencedAppender`/
      `worker.NewWorker`); eixo nomeado (threading do `rs.lease.Token`); distingue-o do uso do token em `ready→running` do AOS-218.)*
- [x] **Falsificável:** um teste/asserção garante que nenhum log ou comentário do caminho de posse afirma
      "fencing" enquanto o `FencedAppender` não estiver composto (guarda de veracidade). Sem segredos; gates verdes.
      *(Evidência: `aos222_fencing_truthfulness_test.go` — varre por **AST** `hostRun`+`heartbeat` e recusa a
      assinatura semântica "fencing + escrit + verbo-de-barreira" enquanto o `FencedAppender` não for composto
      (skip condicional se for); falha-antes provada (reintroduzir o claim ⇒ FAIL em `service.go:491`); detector
      two-sided (4 positivos / 6 negativos, não trip o "fencing token" legítimo); guarda-da-guarda `pieces==0`⇒Fatal;
      `-race` + `deferrals` verdes; `go.mod` intacto.)*

**Fecha:** o achado #10 (veracidade). **Depende de:** AOS-164/AOS-170 (NodeService/lease). **Não duplica:** a
composição do `FencedAppender` (eixo maior, deixado nomeado).

**Residual nomeado (não defeito):** a guarda de veracidade cobre **apenas** `hostRun`/`heartbeat` (o caminho de
posse do ticket) — um futuro claim falso de fencing noutra função (ex.: `Shutdown`, comentário de topo) não seria
apanhado automaticamente (direcção **fail-closed**: o detector, sendo lexical por-linha, tende a falso-positivo
over-strict, não a falso-negativo). Alargar a varredura a mais funções fica como melhoria incremental, não bug.

---

### AOS-223 — Endurecimento do seam do Model Gateway (SSRF/transport) (achado #9)

**Origem:** achado da auditoria adversarial do egress — dois defeitos **latentes** no seam do Model Gateway (o
egress-de-tools do nó já é são: hook RM default-deny fail-closed provado).

**O defeito (verificado):** no seam (`packages/platform/model-gateway`): (a) `http.DefaultClient` **nu** — sem
timeout, sem política de TLS, sem limite de redirect; (b) `BaseURL` **sem validação** (https/allowlist) ⇒
superfície de **SSRF**. Disjunto do nó (`platform/model-gateway`), pelo que **paraleliza** com os fixes do
composition-root.

**Critérios de aceitação**

- [x] O cliente HTTP do gateway tem **timeout**, política de **TLS** explícita e **limite de redirect** (não
      `http.DefaultClient`). *(Evidência: `newHardenedEgressClient` (`production.go:365-380`) — `Timeout:
      egressTimeout` (30s), `TLSClientConfig{MinVersion: TLS12}`, `CheckRedirect` que impõe `egressMaxRedirects=5`;
      `newHardenedClient` (`openai_http.go:76`) substitui o `http.DefaultClient` nu; `TestHardenedEgressClient_*`.)*
- [x] `BaseURL` é **validado** antes de qualquer chamada: esquema **https** obrigatório + **allowlist** de hosts;
      um host fora da allowlist / esquema não-https é **recusado** fail-closed. *(Evidência: `newProviderAdapter`
      (`production.go:286-303`) valida SEMPRE no caminho de egress real (`client==nil`); sentinelas
      `ErrInsecureBaseURL`/`ErrHostNotAllowed`.)*
- [x] **Falsificável (`-race`):** um teste prova que um `BaseURL` malicioso (http, host interno, redirect para
      host não-permitido) é **recusado** (falha-antes: hoje passaria); e que um endpoint legítimo continua a funcionar.
      *(Evidência: `ssrf_seam_test.go` — `TestNewProduction_MaliciousBaseURL_RefusedAtSeam` (http, `169.254.169.254`,
      fora-da-allowlist, allowlist-vazia ⇒ sentinela certo **e** gateway `nil`) + controlo positivo legítimo;
      QA quebrou o wiring e confirmou o fail-OPEN antes do fix, restaurou byte-idêntico (`bbcad9f5`); `-race` verde.)*
- [x] Sem segredos (sem credenciais reais no teste); gates verdes. Zero-dep preservado. *(Evidência:
      `go.mod`/`go.sum` intactos; suite `model-gateway` `-race` verde; secrets/layer-lint/deferrals verdes.)*

**Fecha:** os dois defeitos de seam do achado #9. **Depende de:** AOS-184 (o Model Gateway). **Não duplica:** o
produtor real de custo/credenciais (EPIC-06, infra) nem o egress-de-tools do nó (já são).

---

### AOS-224 — Escopo do recall da memória por principal (achado #8)

**Origem:** achado **latente** da auditoria adversarial da memória (provenance/redação são sãos e ligados; o
recall é que não é escopado).

**O defeito (verificado):** o recall da memória é **write-only/não-escopado-por-principal** — uma leitura não é
restringida à identidade do principal. Latente hoje (sem recall ligado no loop), mas **tem de fechar antes** de
qualquer recall ser ligado, sob pena de vazamento cross-principal.

**Critérios de aceitação**

- [x] A leitura/recall da memória **episódica** é **escopada pela identidade verificada do principal** (não
      auto-declarada); um recall de um principal **não** devolve memória de outro. *(Evidência: `retrieval.go` —
      guarda `if q.PrincipalID == "" { return nil, ErrMissingPrincipal }` (1ª instrução, antes de ler o log) +
      filtro `if env.AgentID != q.PrincipalID { continue }`; a chave de escopo vem da request verificada a
      montante, não do conteúdo in-band. `Project(ctx, principalID, episodeID)` fecha também a leitura por-id:
      episódio de outro principal ⇒ `ErrEpisodeNotFound` (não-oráculo de existência).)*
- [x] **Falsificável (`-race`):** dois principais escrevem memória; o recall de A **não** vê a de B (falha-antes se
      o escopo não existir). Recuo cross-principal ⇒ vazio, não claro alheio. *(Evidência:
      `recall_principal_scope_test.go` (`TestRecallScopedByPrincipalNoCrossLeak`/`…EmptyPrincipalFailClosed`/
      `…UnknownPrincipalEmpty`) + `project_principal_scope_test.go` (`TestProjectScopedByPrincipalNoCrossLeak`/
      `…EmptyPrincipalFailClosed`); falha-antes provada por neutralização (números exactos: `recall(A)=3, esp 2`),
      restaurado byte-idêntico; `-race` verde.)*
- [x] **Retro-compat:** o caminho de escrita/provenance/redação (já são) inalterado. Sem segredos; gates verdes.
      *(Evidência: só a assinatura de leitura mudou; escrita/seal/hash-chain intactos; `integritytests` `-race`
      verde (`TestShredding…`, `TestTTLSweep…`); `go.mod`/`go.sum` intactos.)*

**Fecha:** o achado #8 (escopo de recall **episódico**). **Depende de:** o subsistema de memória (EPIC-04) e a
identidade verificada (AOS-205/174). **Não duplica:** a provenance/redação (já ligadas).

**Residual nomeado (decisões do dono, não defeitos):** (1) `semantic.KnowledgeBase.Recall`/`ControlPlaneView`
**não** foram escopados por principal — **deliberado**: a memória semântica é, por design, uma **base de
conhecimento partilhada** (a barreira é trusted/untrusted do Princípio 5/ADR-005, não por-principal; factos como
`capital:france` são conhecimento partilhado, não trajectórias privadas). Escopá-la redefiniria a semântica e é
um **follow-up com decisão de arquitectura** (eixo EPIC-04), não um bug deste entregável. (2) Episódios legados
gravados com `agent_id` vazio ficam irrecuperáveis via leitura escopada (direcção **fail-closed**) — nota de
migração; a escrita/hash-chain não muda e a cadeia permanece verificável.

---

### AOS-225 — Defesa-em-profundidade da identidade: `len(IssuerPubKey)==32` (auditoria adversarial)

**Origem:** recomendação de **defesa-em-profundidade** da auditoria adversarial da identidade (o boundary já
está protegido — AOS-193 rejeita chave partilhada).

**O defeito (verificado):** no modo endurecido, a chave pública do emissor (ed25519) **não** tem asserção
estrutural de comprimento no ápice. Não é um furo (AOS-193 protege no boundary), mas a asserção redundante
`len(IssuerPubKey)==32` fecha a via de uma chave malformada/vazia passar em silêncio.

**Critérios de aceitação**

- [ ] No modo endurecido, o ápice **valida** `len(IssuerPubKey)==32` (ed25519) — chave de comprimento
      errado/vazia ⇒ **recusa fail-closed** de arrancar.
- [ ] **Falsificável (`-race`):** uma `IssuerPubKey` de comprimento ≠ 32 ⇒ arranque recusado (falha-antes: hoje
      passaria). Sem segredos; gates verdes.

**Fecha:** a recomendação de defesa-em-profundidade da identidade. **Depende de:** AOS-193 (rejeição de chave
partilhada no boundary). **Não duplica:** o four-eyes atestado (`DEF-107`, ABERTO — exige a porta de attestation,
**infra**, não código do nó).

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
| 1.4 | 2026-07-31 | **AOS-219…AOS-225** acrescentados a §8-bis — a última camada de código do nó do programa de fixes das auditorias adversariais: guarda do taint por eficácia (AOS-219, EPIC-07), superfície de bundle PDP (AOS-220, EPIC-07), tamper-evidence WORM imposta (AOS-221, EPIC-09), veracidade do fencing (AOS-222, EPIC-02), seam SSRF do Model Gateway (AOS-223, EPIC-06), escopo de recall por principal (AOS-224, EPIC-04) e defesa-em-profundidade da identidade (AOS-225, EPIC-16). Fecham o wiring/imposição/veracidade no composition-root; o que resta para execução real é provisionamento de infra (D4, bundle+anchor, KMS, provider), **não** código do nó. | auditorias adversariais |
