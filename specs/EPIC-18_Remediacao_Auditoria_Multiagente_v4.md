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
| Intervalo de tickets | **AOS-190 … AOS-203** (14 tickets) |

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

Ordem obrigatória: **AOS-183 → AOS-205\*/CON-04 → AOS-181**.
*(\* a correcção de CON-04 pertence ao âmbito de AOS-182 na EPIC-17; se for separada, recebe número próprio.)*

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
| AOS-190 | Ligar `layer-lint`/`rtm`/`ref-lint` à CI que bloqueia merges | fix | **S** | **P0** | PLA-01 | A |
| AOS-191 | Superfície de configuração para `DurableExecution` (`AOS_DURABLE_EXECUTION`) | feature | **S** | **P0** | REG-01 ≡ STR-09 ≡ PLA-03 | B |
| AOS-192 | Corrigir o teste de aceitação vacuoso de AOS-180 e reabrir §13.3 | fix | **S** | **P0** | VAC-01 | C |
| AOS-193 | Caminho de configuração para `Operators`/`Approvers` (plano de controlo operável) | feature | M | **P0** | ORF-02, STR-04 | B |
| AOS-194 | Corrigir rastreabilidade do STRIDE e cobrir a superfície real do nó | docs | M | P1 | STR-01, STR-06 | D |
| AOS-195 | Corrigir a regressão documental de `redaction/doc.go` e reabrir o CA de AOS-188 | fix | **S** | P1 | VAC-02 ≡ DEF-02 ≡ CON-03 | C |
| AOS-196 | Registo único de deferimentos + correcção dos eixos inválidos | feature | M | P1 | DEF-01, DEF-03, DEF-06 | E |
| AOS-197 | Reclassificar a matriz de conformidade e nomear as lacunas de âmbito | docs | M | P1 | CON-01, DEF-01 | D |
| AOS-198 | Criar o «gate 4 — Integração» (ou retirar a sua declaração) | feature | M | P1 | DAT-09 | A |
| AOS-199 | Pisos aos limiares de gate sobreponíveis por ambiente | fix | **S** | P1 | ORF-06 | A |
| AOS-200 | Instrumentar o tripwire da Carta §6.6 e o registo de arbitragens §6.5 | feature | S | P2 | DEF-07 | A |
| AOS-201 | Reconciliar `tecnica/13` (envelope e catálogo de eventos) com o código | docs | M | P2 | DAT-01, DAT-02, DAT-03 | D |
| AOS-202 | Decidir o destino dos módulos `*/contract` órfãos (1763 LOC, 0 importadores) | chore | S | P2 | ORF-01 | E |
| AOS-203 | Documentar as variáveis de ambiente do nó e endurecer o kill-switch de soberania | fix | M | P1 | ORF-03/04/05 | B |

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
- [ ] `grep -c "layer-lint\|rtm\|ref-lint" .github/workflows/ci.yml` ≥ 3.
- [ ] Os três jobs aparecem no `needs:` do agregador `gates`.
- [ ] `layer-lint` corre contra `packages/` (não contra árvore sintética) no job de CI.
- [ ] **Prova negativa registada:** um PR de teste com `import ".../control-plane/..."` dentro de `platform/`
      torna `gates` **vermelho**; o output é anexado ao ticket.
- [ ] A baseline de `layer-lint` deixa de dizer «Serão resolvidas pelo ticket AOS-179» nas inversões que o
      ADR-019 decidiu **legitimar** (texto desalinhado com a decisão).

---

### AOS-191 — Superfície de configuração para `DurableExecution`

**Achado:** REG-01 ≡ STR-09 ≡ PLA-03 (ALTO, v3-AINDA-ABERTO — DUR-01). `bootstrap.go:186` declara
`DurableExecution bool`; `:410` e `:580` são os únicos consumidores; `main.go:119-148` nunca o escreve;
`grep AOS_DURABLE .` → **0**; o único escritor em toda a árvore é `bootstrap_durable_execution_test.go:129`.
Agravante: `bootstrap.go` é `package main`, pelo que **nem um embedder externo** o pode preencher.

**Impacto:** `tecnica/02:~465` afirma que a execução durável está «exposta no nó `aos`». O código existe e está
correcto; é **inalcançável**. DUR-01 da v3 continua aberto na prática.

**Critérios de aceitação**
- [ ] `AOS_DURABLE_EXECUTION` (padrão de `AOS_EVENTSTORE_PATH`) é lida em `main.go` e escreve `Config.DurableExecution`.
- [ ] O banner de arranque regista se checkpointer/capturer/step-ledger estão compostos.
- [ ] `deploy/node/README.md` documenta a variável, o seu efeito e a interacção com `AOS_EVENTSTORE_PATH`.
- [ ] Teste: um nó arrancado com a variável activa compõe os três; sem ela, permanecem `nil` (comportamento actual).
- [ ] **Emenda ao AC de AOS-180** (ou nota no seu DoD) registando que «quando configurado» exigia superfície de
      configuração — o defeito era de *suficiência do critério*, e deve ficar registado para não se repetir.

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
- [ ] A 2.ª vida do nó usa uma instância **nova** do modelo e emite ≥1 tool call.
- [ ] A asserção passa a verificar que o ledger devolveu «already-applied», não apenas `execs == 1`.
- [ ] **Prova negativa:** partir `StepLedger.Apply` torna o teste **vermelho** (registar o output).
- [ ] `AOS-169-aceitacao-sistemica.md` §13.3 fica **reaberto** (🟡) até este ticket fechar, e é re-marcado com a
      evidência nova.
- [ ] Revisão dos outros três eixos com evidência citada errada (§13.1 cadeia sem hook de revalidação;
      §13.6 `execute_tool` com modelo que não emite tool call; §13.7 KEK que nunca cifrou nada) — corrigir a
      citação ou reabrir o eixo.

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
- [ ] Existe caminho de configuração para pubkeys de operador (ficheiro ou env, no padrão de `AOS_ISSUER_PUBKEY`).
- [ ] Existe caminho equivalente para `Approvers` (4-eyes) ou fica declarado como deferimento **com ticket**.
- [ ] **Prova positiva:** um nó lançado por `deploy/node/Dockerfile` aceita um `aos steer` assinado.
- [ ] **Prova negativa:** bind a `0.0.0.0` com **zero** operadores registados **recusa** (`ErrRefuseNonLoopbackBind`)
      — ou seja, `controlAuthenticated()` passa a exigir ≥1 operador.
- [ ] `deploy/node/README.md:46-47` fica verdadeiro, ou é corrigido para descrever o que o código impõe.

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
- [ ] Cada `AOS-NNN` citado em `tecnica/17` casa com o título do ticket em `specs/EPIC-*.md` (diff mecânico limpo).
- [ ] Acrescentada coluna de **estado** por mitigação: entregue / por-fazer / deferido-com-eixo.
- [ ] O documento cobre a superfície actual: API HTTP, SSE, DSAR, OIDC, attestation, exporter OTLP, contentor.
- [ ] O documento passa a ser referenciado (RTM e/ou epic), deixando de ser órfão.
- [ ] **Automatização:** a verificação ticket↔título é acrescentada ao `ref-lint.py` (extensão natural), para
      esta correcção não voltar a derivar.

---

### AOS-195 — Corrigir a regressão documental de `redaction/doc.go`

**Achado:** VAC-02 ≡ DEF-02 ≡ CON-03 (MÉDIO, NOVO — regressão introduzida por AOS-188).
`packages/substrate/redaction/doc.go:11-12` afirma que o motor está «cablado nos composition-roots (cmd/aos,
cmd/aos-demo, integration)»; `go list -deps` mostra-o **ausente** em `cmd/aos` e `integration` (está presente em
`aos-demo`, via `approval-card`). Três lentes independentes chegaram aqui por caminhos diferentes.

**Critérios de aceitação**
- [ ] `doc.go:11-12` deixa de afirmar cablagem inexistente (mantendo `:13-15`, que declara o limite real).
- [ ] O CA de AOS-188 é reaberto **ou** a fronteira fica registada como deferimento com ticket nomeado.
- [ ] **Verificação:** `go list -deps ./...` em `cmd/aos` e `integration` não contradiz nenhuma afirmação do `doc.go`.

---

### AOS-196 — Registo único de deferimentos + correcção dos eixos inválidos

**Achado:** DEF-01/DEF-03/DEF-06 (ALTO/MÉDIO, NOVO). O padrão sistemático não é «dívida escondida» — é **eixo
errado**: a cifra do substrato é apontada a três epics inconsistentes (`bootstrap.go:626` diz EPIC-06/09/10;
`tecnica/02:175` diz **EPIC-13**, que é o epic de *Frontend*) e **nenhum** tem ticket para ela; o anti-replay do
ADR-012 é apontado a EPIC-13; a assinatura de imagem do ADR-017 é apontada a EPIC-10, que não tem ticket para ela.
`NewRatificationGate`/`NewProductionRatificationGate` não têm chamador de produção, e o CA `EPIC-14:901` `[x]`
(«ligados no promotion controller») é falso.

**Critérios de aceitação**
- [ ] Existe um registo único de deferimentos com colunas: id, descrição, **eixo (ticket real)**, dono, gatilho, estado.
- [ ] Os três eixos inválidos acima são corrigidos (ou recebem ticket novo se nenhum existir).
- [ ] O CA falso de `EPIC-14:901` é corrigido.
- [ ] **Verificação por script:** todo o marcador `DEFERIDO`/`demo-grade` em `packages/**/*.go` (não-teste) tem
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
- [ ] `tecnica/14:92` (Art. 17) e `:91` (Art. 5) passam de «Coberto» para «**Parcial**», com a lacuna acrescentada à §5.
- [ ] A §5 passa a nomear também as lacunas de **âmbito da própria matriz**: Art. 15/16/20 (o «DSAR» do produto só
      faz apagamento), **Art. 22** (decisão individual automatizada — o artigo mais directamente convocado pela
      forma do produto), Art. 33/34, e AI Act Art. 50.
- [ ] **Verificação:** toda a linha «Coberto» tem o mecanismo alcançável a partir de `packages/cmd/aos`
      (`go list -deps` sobre o pacote que a implementa).
- [ ] Legal hold e job de expiração (CON-02) recebem eixo/dono/data declarados ou superfície de administração.

---

### AOS-198 — Criar o «gate 4 — Integração» (ou retirar a sua declaração)

**Achado:** DAT-09 (MÉDIO, NOVO) — **causa-raiz identificada**. Os dois contratos com rastreio a `tecnica/12` no
código (C1, C2) estão **fiéis**; os três **sem** rastreio (C3, C4, C5) divergiram integralmente. O «gate 4 —
Integração», declarado bloqueante de merge em `specs/01:83` e nomeado em `tecnica/12:351` como a mitigação de
«deriva silenciosa de schema», **não existe** em `scripts/ci/`. A deriva não é acidental — é a ausência do gate.

**Critérios de aceitação**
- [ ] `scripts/ci/run.sh` inclui um gate que falha se um tipo Go de porta divergir do contrato declarado em
      `tecnica/12` (mínimo verificável: presença dos códigos de erro `E_*` documentados), **ou**
- [ ] a declaração é **retirada** de `specs/01:83` e `tecnica/12:351` e a deriva C3/C4/C5 fica registada como
      deferimento com eixo (AOS-196).
- [ ] Qualquer que seja a via, o resultado é falsificável: não pode ficar um gate declarado e inexistente.

---

### AOS-199 — Pisos aos limiares de gate

**Achado:** ORF-06 (MÉDIO, NOVO). `EVAL_PASS_RATE_MIN`, `KERNEL_COVERAGE_MIN`, `APEX_COVERAGE_MIN` e `SKIP_DOCKER`
são sobreponíveis por ambiente **sem piso nem registo** — «gates verdes» deixa de ser prova reproduzível.

**Critérios de aceitação**
- [ ] Cada limiar tem um **piso** abaixo do qual o gate falha por violação de piso.
- [ ] **Prova negativa:** `EVAL_PASS_RATE_MIN=0 make ci` **falha** (não passa verde).
- [ ] Os limiares e os seus pisos ficam documentados em `CONTRIBUTING.md` ou `AGENTS.md`.
- [ ] `SKIP_DOCKER` regista no output que etapas foram saltadas (sem falso-verde silencioso).

---

### AOS-200 — Instrumentar o tripwire da Carta §6.6

**Achado:** DEF-07 (MÉDIO, NOVO). A Carta §6.6 declara: «≥2 decisões FIXAS reabertas em 30 dias ⇒ o congelamento
falhou … **este contador é o SLI do próprio processo**». Não existe contador, nem registo de arbitragens §6.5, em
lado nenhum do corpus. A promessa anti-retrabalho não está falsificada — está **infalsificável**, que é
precisamente a condição que o §6.6 foi escrito para evitar.

**Critérios de aceitação**
- [ ] Existe um registo (ficheiro versionado) com: data, decisão FIXA tocada, natureza (emenda/arbitragem),
      veredicto do árbitro §6.5.
- [ ] O contador de reaberturas em janela de 30 dias é calculável por comando.
- [ ] As emendas 1.1/1.2/1.3 e a arbitragem que originou o ADR-019 ficam registadas retroactivamente.

---

### AOS-201 — Reconciliar `tecnica/13` com o modelo de eventos real

**Achado:** DAT-01/02/03 (MÉDIO, NOVO). Envelope de `tecnica/13:60-89` vs `eventstore/event.go:59-72`: 8 campos
documentados ausentes, 5 reais não documentados (o schema publicado tem `additionalProperties:false`). 81
constantes de tipo de evento no código, 80 não registadas; 3 dos 4 nomes «canónicos» documentados **não são
emitidos** (`tool.result.received`, `state.transition`, `tool.call.dispatched`). O campo `taint` do contrato C2
não existe no envelope (é persistido no payload da mediação, `eventsink.go:96,159`).

**Critérios de aceitação**
- [ ] O envelope documentado casa com `eventstore/event.go` (campo a campo), ou as diferenças são declaradas
      como «desenho, não wire» de forma explícita e localizada.
- [ ] Os nomes canónicos citados são emitidos, ou substituídos pelos reais.
- [ ] O catálogo de tipos de evento é gerado ou verificado por script (evitar nova deriva de 80 entradas).

---

### AOS-202 — Destino dos módulos `*/contract` órfãos

**Achado:** ORF-01 (MÉDIO, NOVO). `packages/{kernel,substrate}/contract` — **1763 LOC, 22 ficheiros, 0
importadores, 0 testes, 0 menções documentais** — auto-declaram-se «contrato canónico». Com zero importadores,
não entram em nenhum binário (daí a reclassificação de CONTRADITÓRIO para MÉDIO), mas a auto-declaração colide
com a leitura literal do ADR-019 §3.

**Critérios de aceitação**
- [ ] Decisão registada: **remover**, **documentar como referência não-vinculativa**, ou **adoptar** (com importadores).
- [ ] Se ficarem, a auto-declaração «contrato canónico» é calibrada para não colidir com o ADR-019 §3.
- [ ] Se saírem, a remoção não quebra nenhum build (verificado por `go build ./...` em todos os módulos).

---

### AOS-203 — Documentar as variáveis de ambiente e endurecer o kill-switch de soberania

**Achado:** ORF-03/04/05 (MÉDIO, NOVO). `AOS_HUMANS` e `AOS_ISSUER_ID` não têm **uma linha** de documentação em
todo o repo. Pior: **`AOS_BOARD_REGIONS` definido-vazio é um kill-switch do read-path soberano**, documentado
apenas num script de harness — uma variável de ambiente que desliga um controlo de conformidade sem registo.

**Critérios de aceitação**
- [ ] Todas as variáveis lidas por `packages/cmd/aos` (`os.Getenv`) estão documentadas em `deploy/node/README.md`,
      com efeito, default e impacto de segurança.
- [ ] `AOS_BOARD_REGIONS` vazio **não** desliga silenciosamente o read-path soberano: ou recusa arrancar em
      `AOS_MODE=production` (padrão de `ErrProductionNeedsSovereignRead`), ou regista um aviso proeminente no banner.
- [ ] **Verificação por script:** o conjunto de `os.Getenv` em `cmd/aos` é subconjunto do documentado (gate ou teste).

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
