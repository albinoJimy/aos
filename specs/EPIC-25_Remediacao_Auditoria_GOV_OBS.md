# EPIC-25 — Remediação dos defeitos da auditoria adversarial GOV/OBS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos defeitos apurados na auditoria adversarial transversal de Governação e Observabilidade |
| Versão | 1.0 |
| Data | 2026-09-06 |
| Classificação | Documento de Referência — **Por executar** |
| Documento-fonte | `analises/13_Auditoria_GOV_OBS_Adversarial.md` (§2, §3, §4, §5) |
| Documentos relacionados | `docs/governance/REGISTO-Deferimentos.md`, `docs/adr/README.md`, `tecnica/08`, `09`, `13`, `14`, `16`, `17`, `specs/EPIC-08`, `EPIC-09`, `EPIC-17`, `EPIC-18`, ADR-010 (catálogo), ADR-011, ADR-013, ADR-014 (catálogo), ADR-015, ADR-017 |
| Âmbito | `packages/kernel/reference-monitor`, `packages/kernel/agent-runtime/replay`, `packages/control-plane/pdp`, `packages/control-plane/governance/autonomy`, `packages/platform/audit`, `packages/substrate/otel-genai`, `packages/integration`, `packages/cmd/aos`, `tecnica/`, `docs/governance/`, `scripts/ci/` |

---

## 0. Porque este epic existe

`analises/13` produziu **75 hipóteses-defeito** em oito lentes independentes, atacou-as com o ónus da
prova invertido em oito refutações — uma delas **experimental**, que correu os cenários em cópias
isoladas em vez de os argumentar — e mediu o resultado no nó real, incluindo duas medições feitas
**depois** de o relatório estar escrito, para fechar lacunas que ele próprio declarava.

**Sobreviveram 30**, das quais 19 alcançáveis hoje e 6 de alta severidade. Caíram 45: vinte e uma
refutadas como falsas, treze já declaradas com `DEF-NNN`, e onze verdadeiras sem que nenhuma fonte de
verdade as exija. Mais **sete achados nasceram da própria refutação**, e são os melhores do
relatório — nenhum deles foi visto por qualquer lente.

O que define o âmbito deste epic é a assimetria do saldo. No eixo da autonomia, onde a passagem 1
via dois defeitos **críticos**, sobreviveu **um de severidade baixa** — porque a escada L0–L5 não tem
fonte normativa: a Carta não contém a palavra «autonomia» e o ADR-014 nunca passou de uma linha de
catálogo. Não se encontraram divergências porque a implementação corresponde à especificação; não se
encontraram porque não há texto com autoridade que diga a que deveria corresponder. No eixo do
trilho de auditoria e das costuras, onde as hipóteses eram sobre o interior de código que corre todos
os dias, sobreviveram quase todas.

**Este epic não reabre o que já está declarado com eixo e dono.** DEF-905 (RiskGate), DEF-908
(controlador de autonomia), DEF-909 (soberania por board), DEF-107 (four-eyes atestado), DEF-281
(SLI de overhead), DEF-602, DEF-012, DEF-217 e DEF-302 ficam onde estão. Entra aqui o que nenhum
documento vigente descreve: defeitos dentro de caminhos que correm, artefactos que afirmam algo falso
sobre o estado do sistema, e uma ordem de segurança invertida que o próprio nó convida o operador a
completar.

### 0.1 O que a §5 da auditoria omitiu — e este epic corrige

A §5 de `analises/13` listou treze remediações. A §3 do mesmo documento lista **trinta** achados
sobreviventes. Aplicando à auditoria o mesmo teste que a `EPIC-24` §0.1 aplicou à anterior, o
resultado é desfavorável e fica registado: **dois defeitos confirmados de severidade média ficaram
fora do plano de remediação**, e vários menores ficaram sem menção.

| Achado | Onde está na auditoria | Porque a §5 o perdeu |
|---|---|---|
| O registry e a supply-chain auditam para um armazém descartável | §3.6, O-14 | Agrupado na linha genérica «lacunas de cobertura do trilho», que a §5 não desdobrou |
| Dois gates bloqueantes que passam por razões erradas | §3.7, C-05 | A §5 nomeou os gates no P2 como «veracidade documental», que é a categoria errada — não é o documento que mente, é o gate que não verifica |

Entram como **AOS-381** e **AOS-382**.

**Adenda (medição posterior).** A medição do último salto (`analises/13` §2.4), feita depois deste
epic ser escrito, produziu mais dois defeitos confirmados no contrato com o executor de sandbox.
Entram como **AOS-383** e **AOS-384**. Ambos foram encontrados a instrumentar o caminho, não a
lê-lo — é o terceiro sítio nesta execução em que exercitar produziu o que a leitura não deu. O segundo é o mais incómodo dos dois: um *required check* cujo
output real é `0 declaracao(oes) verificada(s)` está a comprar confiança que não produz.

Um plano de remediação que deixa cair achados confirmados repete, em miniatura, o defeito que a
auditoria fecha. A omissão foi da §5, não da análise.

### 0.2 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-363, AOS-364, AOS-365, AOS-366, AOS-367 | Uma barreira estrutural de segurança inerte sem forma de a ligar, e cujo estado o nó não declara; um trilho de auditoria que apaga registos válidos em silêncio; a única configuração de produção que arranca ser a insegura; a perna de egress paga com o endurecimento activamente desarmado; e um token de leitura que autoriza apagamento irreversível |
| **P1** | AOS-368, AOS-369, AOS-370, AOS-371, AOS-372, AOS-373 | A evidência existe mas não serve para investigar: traces não atribuíveis, mediação sem contadores, fuga de payload para o backend, instrumentação morta, e duas ferramentas de leitura que podem destruir aquilo que leem |
| **P2** | AOS-374, AOS-375, AOS-376, AOS-377, AOS-378, AOS-379, AOS-381, AOS-382, AOS-383, AOS-384 | Declarações de cobertura que não se sustentam, o caminho de mediação não exercitado por teste de sistema nenhum, endurecimento de guardas, e gates que passam por razões erradas |
| **Decisão** | AOS-380 | Não é engenharia: é decidir se a escada L0–L5 passa a ter base normativa ou se se aceita por escrito que a conformidade contra ela não é mensurável |

### 0.3 Tabela-resumo

| Ticket | Defeito | P | Alcance | Prova |
|---|---|---|---|---|
| AOS-363 | TaintGate inerte sem superfície que o ligue; a mitigação que quatro entradas do registo invocam não tem chamadores; a ordem da `EPIC-18` §5 está invertida no binário | P0 | **nó** | **Executado** |
| AOS-364 | `OpenFileStore` apaga fisicamente registos válidos em silêncio, e a verificação de adulteração corre depois da amputação | P0 | **nó** (com WORM em ficheiro) | **Executado** |
| AOS-365 | A única configuração de produção do WORM que arranca é a volátil | P0 | **nó** | **Executado** |
| AOS-366 | O nó desarma incondicionalmente o cliente endurecido do gateway de modelo | P0 | **nó** | Leitura verificada |
| AOS-367 | Um token OIDC de leitura autoriza crypto-shred irreversível; duas rotas de destruição não verificam região | P0 | **nó** (modo soberano) | Leitura verificada |
| AOS-368 | Documento OTLP sem atributos de recurso e com espécie de span fixa em INTERNAL | P1 | **nó** (com OTLP) | Leitura verificada |
| AOS-369 | Nenhum contador de mediação no `/metrics`; a falha de selagem do RM não chega ao `/readyz` | P1 | **nó** | **Executado** |
| AOS-370 | `error.type` leva a mensagem crua da tool no mesmo span onde o input é hasheado por princípio | P1 | **nó** (com OTLP) | Leitura verificada |
| AOS-371 | A instrumentação do PDP existe, está testada, e o nó nunca a liga | P1 | **nó** | Leitura verificada |
| AOS-372 | `Reconstruct` não recusa um turno sem captura; o read-path soberano devolve 200 com trajectória curta | P1 | **nó** | **Executado** (ao nível dos pacotes) |
| AOS-373 | `aos audit-trail` abre o trilho forense em escrita e herda a truncatura | P1 | ferramenta | Leitura verificada |
| AOS-374 | Uma entrada do registo caduca e um gate que declara o que o script diz não fazer | P2 | documental | Leitura verificada |
| AOS-375 | Matriz que se contradiz, dois epics com estados opostos do mesmo trabalho, tripwire descrito pelo output que já não produz | P2 | documental | **Executado** (contador do tripwire) |
| AOS-376 | O caminho de mediação não é exercitado por teste de sistema nenhum; a única barreira de taint que resta é uma cláusula opcional na política | P2 | CI / arnês | **Executado** (30 corridas) |
| AOS-377 | Dual-control de L4/L5 detectivo e não preventivo; a simulação responde sobre outra política | P2 | **nó** | Leitura verificada |
| AOS-378 | Guarda do `AOS-355` não replicada no slot `policy`; assert de cobertura conta a camada errada; assinar não avalia | P2 | **nó** / CI | Leitura verificada |
| AOS-379 | Canal de eventos de mediação inalcançável nos dois armazéns, com justificação falsa a fechar a discussão | P2 | **nó** | Leitura verificada |
| AOS-380 | A escada L0–L5 não tem base normativa e a conformidade contra ela não é mensurável | Decisão | governação | Leitura verificada |
| AOS-381 | Registry e supply-chain selam para um armazém volátil que ninguém lê | P2 | **nó** | Leitura verificada |
| AOS-382 | Dois gates bloqueantes passam por razões erradas: um verifica zero declarações, o outro perdoa 10 de 16 | P2 | CI | **Executado** (com mutação) |
| AOS-383 | O contrato de fio do executor declara `run_id` e `step_id` e não transporta nenhum dos dois | P2 | **nó** (com sandbox) | **Executado** |
| AOS-384 | `ErrDriverUnavailable` manda procurar `/dev/kvm` a quem só lhe falta uma variável | P2 | **nó** (com sandbox) | **Executado** |

### 0.4 Paralelismo — o que pode e o que não pode correr junto

**Pode correr em paralelo:**

| Conjunto | Porquê |
|---|---|
| AOS-364 · AOS-372 · AOS-382 | Verificado por `grep replace` nos `go.mod`: `platform/audit` e `kernel/agent-runtime` não têm aresta entre si, e `scripts/ci` não é módulo Go |
| AOS-374 · AOS-375 · AOS-380 | Só corpus documental e registos de governação |
| AOS-365 · AOS-366 · AOS-367 · AOS-381 | Todos em `cmd/aos` mas em ficheiros distintos; confirmar com `git diff --stat` antes de integrar |

**Não pode:**

| Conjunto | Porquê |
|---|---|
| AOS-369 com AOS-370 | Ambos alteram `kernel/reference-monitor/monitor.go`, e em regiões próximas |
| AOS-363 com AOS-378 | Ambos tocam `reference-monitor/production.go` e `integration/secured.go` — e AOS-378 muda a guarda que AOS-363 precisa de ver estável |
| AOS-364 → AOS-373 | AOS-373 depende da correcção da truncatura; corrigir a ferramenta antes do armazém deixaria a ferramenta a proteger-se de um defeito que ainda existe |
| AOS-363 → AOS-376 | O gate da cláusula de taint só faz sentido depois de decidida a superfície que povoa `privileged` |
| AOS-377 com AOS-364 | AOS-377 altera `platform/audit/record.go`, que AOS-364 lê para decidir o que a truncatura destrói |
| **AOS-368 com AOS-364 ou AOS-372** | **Muda contrato partilhado.** Corrigir o `SpanKind` obriga a acrescentar um campo ao `SpanData`, e `packages/platform/audit/go.mod` e `packages/kernel/agent-runtime/go.mod` têm ambos aresta `replace` para `aos-ref/substrate/otel-genai` — verificado por `grep`. Uma primeira versão desta tabela dava-os como disjuntos; era falso, e é o género de erro que custa uma tarde a quem a seguir. AOS-368 sequencia-se sozinho, ou antes dos dois |

### 0.5 O que este epic NÃO cobre

Achados **confirmados** que ficam deliberadamente fora, com a razão nomeada — declarar é legítimo,
omitir não é:

| Achado | Porque fica fora |
|---|---|
| Tecto de classe no emissor de produção (§3.2, G-06) | Exige autoridade de emissão para ser explorado, e a mitigação real (o nonce OIDC amarra a delegação exacta) já existe. Eixo: `EPIC-16` |
| Revogação não propaga a filhos (§3.2, G-07) | **Inalcançável** — `IssueChild` não está composto no nó |
| Oráculo de autonomia sem guarda de produção (§3.3, G-10) | Sobrepõe-se a AOS-363: a mesma decisão de postura resolve os dois, e separá-los criaria conflito de ficheiro |
| `PendingApproval` sem nível nem modo de oversight (§3.3, G-11) | Superfície HITL; pertence ao eixo da `EPIC-12`, não à remediação da governação |
| Retoma começa outro trace (§3.4, O-05) | Depende da decisão de AOS-368 sobre a forma do `SpanData`; reabre-se depois |
| Rótulo `produtor` ausente nos alertas exportados (§3.4, O-06) | Configuração de alerting do deployment, não código do nó |
| `reconstruct` não declara que nada foi verificado (§3.5, O-09) | Absorvido pelos critérios de AOS-372 |
| `Manifest.Skills` e `schema_version` (§3.5, O-10) | Latente e de baixa severidade; eixo declarado em AOS-372 |

E, por decisão explícita, **não se reabre** o que `tecnica/14` §5.2 e o `REGISTO-Deferimentos.md` já
declaram com eixo e dono.

### 0.6 Limites de evidência

Cada ticket declara no seu Contexto se a base é **leitura verificada** ou **execução**. Da auditoria
de origem herdam-se três limites que condicionam os critérios de aceitação:

- **Nenhuma medição correu contra um provider de modelo real.** As três decisões de mediação que
  sustentam AOS-363 usaram um gateway OpenAI-compatible construído fora da árvore. O caminho é o do
  nó; o interlocutor não é.
- **O último salto FOI medido depois de este epic ser escrito** (`analises/13` §2.4), com um executor
  conformante descartável: a tool call `cap:fs.read` com taint não-confiável **chega ao executor**, e
  o selo WORM guarda o taint ao lado do `allow`. A medição corrigiu a atribuição: o `denied_by=dispatch`
  do §2.2 vinha do **registo da tool** (`E_TOOL_NOT_REGISTERED`), não do executor — com bloco `sandbox`
  no manifesto e sem executor nenhum, o Reference Monitor já permite. **E o isolamento a jusante do
  POST foi medido** (`analises/13` §2.5): componente gVisor real num Docker descartável, guest de
  diagnóstico, `/proc/version` a confirmar o `runsc` — todos os casos de contenção contidos, nenhuma
  fuga. Fica por correr só o bloco de esgotamento de recursos e o gVisor sobre virtualização real.
  Nenhum dos dois é código do nó, e nenhum condiciona os tickets deste epic.
- **A retoma HTTP ponta-a-ponta de AOS-372 não foi medida** — exige um Model Gateway real sob a
  build-tag `aoslive`. A assimetria foi medida ao nível dos pacotes.

---


Tickets AOS-363 a AOS-367. Origem: `analises/13_Auditoria_GOV_OBS_Adversarial.md` §2.1, §2.2, §3.2,
§3.6 e §3.7. Todas as âncoras `ficheiro:linha` foram re-verificadas contra a árvore em HEAD `04f3d46`;
as correcções face ao relatório estão assinaladas em nota no fim de cada Contexto.

---

## AOS-363 — O TaintGate está inerte, nenhuma variável o consegue ligar, e o banner instrui o operador a inverter a ordem de segurança da EPIC-18 §5

### Contexto

`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md:70` não deixa margem: *«Isto **não é uma
recomendação, é uma restrição de ordem**»*. A ordem obrigatória, fixada em `:82`, é **AOS-183
(activar TaintGate) → correcção de CON-04 → AOS-181 (carregar bundle PDP real)**, e o racional de
`:74-81` é explícito sobre o que se ganha por invertê-la: carregar a política antes de ligar a
barreira de taint «transforma um nó *seguro-mas-inerte* num nó **permissivo com a defesa estrutural
desligada**».

O que está entregue é a segunda metade, e não a primeira.

A segunda metade está composta e alcançável: `packages/cmd/aos/main.go:746` chama
`loadPolicyBundleFromEnv` (`main.go:1308`), que lê `AOS_POLICY_BUNDLE_DIR` (`:1309`) e
`AOS_POLICY_TRUST_ANCHOR` (`:1313`) e devolve um PDP verificado para `Config.PDP`.

A primeira não existe em nenhuma forma alcançável:

- `packages/cmd/aos` **nunca** preenche `Privileged`. A única ocorrência em todo o pacote é
  `acceptance_mediation_test.go:157`; `grep -rn "Privileged:"` na árvore devolve zero.
- `packages/integration/secured.go:301-303` cai no fallback `NewStaticPrivilegedSet()`, cujo próprio
  comentário na linha 303 diz «classificador real (vazio)».
- `secured.go:394` compõe `NewProductionSecure`, que **aceita** o conjunto vazio:
  `reference-monitor/production.go:193-212` verifica identidade real, ausência do neutro de egress,
  presença de hook de egress e um ScopeGate com autoridade — e nada sobre a eficácia do taint.
- O construtor que o recusaria existe, está testado e tem **zero chamadores não-teste**:
  `NewProductionHardenedTaint` (`production.go:226-233`) e `ErrTaintGateInert` (`production.go:29`,
  «conjunto privileged vazio ⇒ nenhuma promoção tainted é barrada; exige um PrivilegedAuthorizer
  não-vazio (AOS-183)»). Os únicos chamadores são `production_efficacy_test.go:107,124`.
- **Não há superfície de configuração.** `NewStaticPrivilegedSet(capabilities ...string)`
  (`reference-monitor/taint_gate.go:41`) aceita a lista, mas nenhuma fronteira de ambiente a
  constrói: a medição de §2.2 enumerou as 102 variáveis `AOS_*` que o binário lê e nenhuma povoa
  `privileged`; `grep -rn PRIVILEG packages/cmd/aos deploy` não devolve uma única ocorrência
  relacionada. Um operador que leia a auditoria, concorde com ela e queira fechar o buraco **não
  tem como**.
- **Não há predicado observável.** `Monitor.HasActiveTaintGate()` (`production.go:123`) tem zero
  chamadores não-teste, e `packages/cmd/aos/posture_banner.go` tem treze funções de postura
  (`:99, :193, :210, :267, :274, :309, :382, :423, :471, :516, :568, :588, :602`) — nenhuma sobre o
  Reference Monitor.

O agravante que torna isto alcançável em vez de teórico: **o banner de arranque instrui o operador a
fazê-lo**. `bootstrap.go:2381` diz literalmente «*defina AOS_POLICY_BUNDLE_DIR +
AOS_POLICY_TRUST_ANCHOR (pubkey ed25519 out-of-band) para carregar um bundle assinado*». Seguir a
instrução do próprio nó atravessa a fronteira que a EPIC-18 §5 declara proibida, e nenhuma das duas
linhas do banner (`bootstrap.go:2379` e `:2381`) menciona o taint.

**O dano real, e o que a medição atenuou.** O nó **não** fica cegamente permissivo. §2.2 obteve duas
decisões de mediação reais com `taint=untrusted`:

| Tool call | Desfecho medido | Porquê |
|---|---|---|
| `cap:http.post` | `denied_by=policy` | `pdp/policies/aos_authz.cedar:33` traz `context.taint != "untrusted"` na regra `allow_http_post` |
| `cap:fs.read` | `denied_by=dispatch` — todos os hooks permitiram, PDP e TaintGate incluídos | `aos_authz.cedar:41-49` (`allow_fs_read`) **não tem cláusula de taint nenhuma** |

Com o TaintGate inerte, a única aplicação de taint que resta é uma cláusula opcional dentro do texto
da política. A defesa estrutural, que por desenho vale para todas as regras, foi substituída por uma
disciplina de escrita de política que nada verifica. E a cadeia de governação **não é** a barreira:
medido em `analises/13` §2.4, com bloco `sandbox` no manifesto o Reference Monitor permite a call
untrusted mesmo sem executor nenhum, e com executor ela chega lá.

**Duas ressalvas que reduzem o alcance sem eliminar o defeito.** O catálogo de tools tem default
vazio, pelo que a mediação só acontece quando o deployment o preenche; e a política committada cobre
uma das duas regras — o buraco é a regra que a esquece, não a política inteira.

**Dívida já declarada que este ticket encerra.** `docs/governance/REGISTO-Deferimentos.md:222`
(DEF-604), `:224` (DEF-606), `:238` (DEF-808) e `:239` (DEF-809) nomeiam todas o mesmo conjunto
`Privileged` vazio e o mesmo eixo de saída, AOS-183. O que é novo face a essas quatro entradas é
que (i) não existe superfície de configuração alguma por onde o gatilho de saída seja accionável,
(ii) nada declara a postura em superfície, e (iii) a medição mostrou que uma regra sem cláusula de
taint fica sem rede.

**Porque sobreviveu.** Três verificações passaram por cima, cada uma por uma razão diferente. O gate
`deferrals` (`scripts/ci/deferrals.py`) verifica que cada marcador tem linha no registo — verifica a
*documentação* do deferimento, nunca o seu cumprimento, exactamente o padrão que AOS-344 nomeou. O
`layer-lint` valida o grafo de imports, não a eficácia da composição. E
`production_efficacy_test.go` prova que `NewProductionHardenedTaint` recusa um conjunto vazio — mas
testa um construtor que o ápice não adopta, pelo que a suite verde é sobre código que não corre. A
`EPIC-18:1489-1491` regista o residual honestamente («`Monitor.HasActiveTaintGate()` está exportado
mas ainda **não é consultado no ápice**»), e é precisamente essa honestidade em disco que fez ninguém
tropeçar nele durante uma auditoria.

*Nota de correcção de âncoras: o relatório cita `secured.go:302-303`; a atribuição começa em `:301`.
Cita `main.go:746,1288`; `:746` está certo, mas `:1288` cai no comentário — a função
`loadPolicyBundleFromEnv` começa em `:1308`. As restantes âncoras (`production.go:29,226-232`,
`bootstrap.go:2381`) batem certo.*

### Critérios de Aceitação

- [ ] Existe superfície de ambiente que povoa `Config.Privileged` (ex. `AOS_PRIVILEGED_CAPS`, lista
      separada por vírgulas): com ela definida, `nodeConfigFromEnv` produz uma `Config` cujo
      `Privileged.IsPrivileged(c)` devolve `true` para cada capability listada e `false` para uma
      não listada — provado por teste nos dois sentidos
- [ ] Controlo negativo da superfície: com a variável ausente ou vazia, o RM composto pelo ápice tem
      `HasActiveTaintGate() == false`, e com ela definida tem `true`; o teste avermelha se a leitura
      da variável for neutralizada
- [ ] `Monitor.HasActiveTaintGate()` passa a ter pelo menos um chamador não-teste em
      `packages/cmd/aos`: `grep -rn "HasActiveTaintGate" packages/cmd/aos --include=*.go` com
      `grep -v _test.go` devolve ≥1 linha
- [ ] `posture_banner.go` ganha uma função de postura para o Reference Monitor e o banner de arranque
      passa a declarar o eixo do taint: um teste captura o banner nas duas configurações (conjunto
      vazio e conjunto não-vazio) e exige que as linhas resultantes sejam diferentes e que a do
      conjunto vazio nomeie a inércia
- [ ] O ápice adopta `NewProductionHardenedTaint` quando o conjunto é não-vazio: um teste constrói o
      nó com a variável definida, prova que arranca, e prova que ao esvaziá-la sob a mesma postura o
      erro devolvido é `ErrTaintGateInert` (ou a recusa equivalente que a decisão fixar)
- [ ] Existe gate que verifique que toda a regra `permit` de um bundle carregado traz a cláusula
      `context.taint != "untrusted"`: no estado actual `pdp/policies/aos_authz.cedar:41-49`
      (`allow_fs_read`) fá-lo falhar; depois de corrigida a regra, o gate passa; e uma política mutada
      que retire a cláusula de `allow_http_post` (`:33`) volta a avermelhá-lo — o controlo negativo
      que prova que o gate não é vácuo
- [ ] `specs/EPIC-18` §5 e as entradas DEF-604 / DEF-606 / DEF-808 / DEF-809 são reavaliadas contra o
      estado resultante: o gatilho de saída que declaram fica accionável ou fica escrito porque não
      ficou

### Estado

**IMPLEMENTADO** (2026-09-07, PR #242, commit `87f441b`), com **um critério deferido e declarado**.
`AOS_PRIVILEGED_CAPS` (lista de capabilities) povoa `Config.Privileged` (`cmd/aos`), e
`NewSecuredRuntime` passa a ESCOLHER o construtor: conjunto eficaz não-vazio ⇒ via ENDURECIDA
(`NewProductionHardenedTaint`); vazio ⇒ `NewProductionSecure`, arranca inerte como antes. Um banner
de postura do Reference Monitor consulta `HasActiveTaintGate()` — o único chamador não-teste do
predicado que AOS-219 exportou. **Retro-compatível por decisão do dono**: a variável ausente OU
definida-mas-vazia (o idioma `${VAR:-}` e o helper de teste que põe cada `AOS_*` a `""`) deixa o nó
inerte; só uma lista com ≥1 capability liga a barreira. Provado por `-race` em `cmd/aos` e
`integration`, `layer-lint`, os dois gates de documentação de env vars, e o smoke `run-aos` 9/9.

**DEFERIDO** (critério do gate «toda regra `permit` traz cláusula de taint» + correcção de
`allow_fs_read` no bundle Cedar): exige re-assinar o bundle, o que rodaria o *trust anchor* sem a
chave de assinatura do projecto. Fica para quem a tem; até lá, o banner INERTE nomeia o buraco
(`allow_fs_read` deixa passar `cap:fs.read` untrusted, e ligar `AOS_PRIVILEGED_CAPS` com
`cap:fs.read` fecha-o de forma estrutural). Registado no banner e no README, não escondido.

*Nota (2026-09-07): este bloco `### Estado` tinha ficado por actualizar quando o PR #242 fez merge —
o mecanismo foi entregue, só a spec não o registou. Corrigido à parte do AOS-365.*

---

## AOS-364 — `OpenFileStore` apaga fisicamente registos de auditoria válidos, em silêncio, e a verificação de adulteração corre depois da amputação

### Contexto

`packages/platform/audit/filestore.go:64` é a porta de reabertura do WORM durável. O que faz, pela
ordem em que o faz:

1. `replayAuditWAL` (`:316-357`) lê o log linear e **pára no primeiro registo cujo CRC não fecha**
   (`:346-348`), devolvendo `validEnd` — o offset imediatamente antes desse registo.
2. `OpenFileStore:69-74` compara `validEnd` com o tamanho do ficheiro e, se for menor, chama
   `os.Truncate(path, validEnd)` (`:70`). Sem erro, sem log, sem métrica.
3. Só em `:107-116` é que `verifyReplayedChain` (`:108`) re-encadeia cada partição — sobre a cadeia
   **já podada**, que naturalmente fecha.

O contrato escrito justifica o passo 2 para um caso legítimo e só para esse: `:30-35` fala de «um
registo final truncado (crash a meio de um write)» e de «um tail parcial». A heurística «pára no
primeiro CRC mau» só é válida para uma cauda rasgada, porque uma cauda rasgada **só pode estar no
fim**. A linha não distingue os dois casos, e serve ambos.

**Medido** (§3.6, O-11) numa cópia isolada fora da árvore: corrompidos 4 bytes do trailer de CRC do
registo do meio (índice 2 de 6), `OpenFileStore` devolve `err=nil`, o ficheiro passa de **3528 para
1176 bytes** — quatro registos apagados do disco —, `head` cai de 6 para 2, `VerifyStore`
(`verifystore.go:50`) fica **verde**, e o `Append` seguinte devolve `audit_seq=3`, **reemitindo
sequências já atribuídas**. Nos casos medidos o ficheiro está fisicamente **completo** e os registos
seguintes estão inteiros e encadeados — e `Open` apaga-os na mesma.

Três descobertas da medição que nenhuma leitura tinha visto, e que são o que separa este ticket de
uma nota sobre recuperação de crash:

- **(a) O dano cruza partições.** O replay reconstrói tudo a partir de um único log linear
  (`filestore.go:92-94`, `s.parts[rec.Partition] = append(...)`), pelo que a fronteira do dano é a
  posição no ficheiro e não a partição. Na medição, três partições caíram juntas.
- **(b) A verificação de adulteração de AOS-221 é estruturalmente inalcançável para este vector.**
  `verifyReplayedChain` corre em `:108`, trinta e oito linhas **depois** de `os.Truncate` em `:70`.
  Mediu-se uma mutação clássica de payload a sair verde com quatro registos apagados.
- **(c) O silêncio é propriedade da assinatura, não omissão de call-site.** `FileStoreOption`
  (`audit/posse.go:61`) tem **uma única** opção em todo o pacote, `ComPosseDeParticao`
  (`posse.go:70`). Não há por onde um chamador pedir modo estrito.

Alcançável hoje em `packages/cmd/aos/bootstrap.go:1144` — `audit.OpenFileStore(cfg.WORMPath)`, o
caminho que qualquer nó com `AOS_WORM_PATH` percorre em cada arranque.

**Ressalva que reduz o alcance sem eliminar o defeito.** A verificação ancorada de AOS-268 apanha-o
(medido), porque compara contra material assinado fora do nó; mas é opt-in por três variáveis de
ambiente que o banner declara ausentes por omissão. E o WORM não é adulterável remotamente: quem
corrompe bytes do ficheiro já tem acesso ao disco. O que o defeito destrói não é a
confidencialidade — é a **detecção**, que é a única garantia que este armazém reclama:
`audit/errors.go:102-107` di-lo em voz alta («*O EntryHash é um hash e não um MAC […] Quem quer prova
de integridade contra um adversário usa o checkpoint ASSINADO*»), e é justamente esse checkpoint
assinado — a âncora de AOS-268 — que a ressalva acima diz estar por omissão desligado.

**Porque sobreviveu.** AOS-221 acrescentou o re-encadeamento no load, e a leitura natural é que isso
fecha a adulteração. Fecha — mas apenas na fatia que sobrevive à truncagem. O teste que a prova,
`packages/cmd/aos/aos221_worm_tamper_test.go:45-46`, muta o payload e **recalcula o CRC**
(`binary.BigEndian.PutUint32(..., crc32.Checksum(payload, tbl))`), mantendo o framing intacto de
propósito, para que o registo chegue a `verifyReplayedChain`. É exactamente o complemento do vector
medido: o teste cobre o caso em que a truncagem não dispara, e ninguém escreveu o caso em que
dispara. Nenhum gate compara o tamanho do ficheiro antes e depois de um `Open`.

*Nota de correcção de âncoras: o relatório cita `audit/filestore.go:76` para a abertura
`O_CREATE|O_WRONLY|O_APPEND, 0600`; é `:75`. As restantes (`:64`, `:70`, `:108`, `bootstrap.go:1144`)
batem certo.*

### Critérios de Aceitação

- [ ] `OpenFileStore` distingue cauda rasgada de dano interior: só trunca quando o dano está
      fisicamente no fim do ficheiro (o registo mal formado esgota os bytes restantes). Dano
      interior — bytes válidos e enquadrados depois do ponto mau — devolve erro tipado e **não**
      altera o ficheiro
- [ ] Teste que reproduza a medição: WAL com 6 registos, corromper 4 bytes do trailer de CRC do
      registo de índice 2, provar que `OpenFileStore` devolve erro **e** que `os.Stat(path).Size()` é
      idêntico antes e depois da chamada
- [ ] **Controlo negativo:** um WAL com cauda genuinamente rasgada (últimos N bytes cortados a meio
      de um registo) continua a abrir com `err == nil`, a truncar o tail parcial e a servir os
      registos íntegros — a recuperação de crash de `:30-35` não regride, provada por teste próprio
- [ ] Teste que prove que a reemissão de sequência deixa de ser alcançável: no cenário de dano
      interior nenhum `Append` chega a ocorrer, e no cenário de cauda rasgada o primeiro `audit_seq`
      pós-abertura é `head+1` da cadeia preservada
- [ ] O dano interior fica observável: o erro nomeia partição, `audit_seq` e offset, e a ocorrência
      emite log de arranque — um teste captura ambos
- [ ] `verifyReplayedChain` deixa de ser inalcançável para este vector: teste que corrompa o CRC de
      um registo intermédio e prove que a recusa acontece **antes** de qualquer escrita ao ficheiro
- [ ] Guarda provada não-vacuosa por mutação: com a distinção cauda/interior desligada, os testes de
      dano interior avermelham

### Estado

**IMPLEMENTADO** (2026-09-07). `OpenFileStore` deixou de decidir pela posição do leitor e passa a
distinguir cauda rasgada de dano por **ressincronização** (`contaOrfaos`/`ressincroniza`, portado do
Event Store irmão AOS-346): há registos íntegros para lá da quebra? >0 ⇒ recusa com
`DanoInteriorError`/`ErrWORMDanoInterior` (nomeia partição/audit_seq/offset), sem tocar no ficheiro;
==0 ⇒ trunca a cauda parcial (crash-safety preservada). Um frame fisicamente completo com CRC/JSON
inválido recusa sempre. A **revisão adversarial** (duas passagens independentes) apanhou que a v1,
que classificava pela posição, reabria o O-11 pelo vector do **comprimento inflado** (corromper os 4
bytes de comprimento ⇒ short read ⇒ truncava em silêncio) e criava um falso positivo com zeros na
cauda; a v2 apanhou um **DoS de ressincronização** O(n²) no arranque — os três estão fechados
(comprimento e zeros pela ressincronização, DoS por um orçamento fail-closed `ressincOrcamentoBytes`).
Provado por `-race` em `platform/audit`, consumidores (`cmd/aos`, `cmd/aos-issuer`) verdes, e o smoke
`run-aos` 9/9. Deferido, declarado: uma escotilha de escape auditada (achado F3) fica como decisão de
dono — para um WORM, um override que trunque um trilho recusado é o silent-drop que este ticket
proíbe; a recuperação é o checkpoint assinado de AOS-268 ou o restauro, que o próprio erro aponta. O
mesmo DoS no Event Store irmão (herdado, não reaberto aqui) foi marcado para porte em tarefa própria.

---

## AOS-365 — A única configuração de produção do WORM que arranca é a volátil, porque a guarda que existe é a da chave e não a do trilho

### Contexto

`packages/cmd/aos/main.go` tem dez guardas `ErrProductionNeeds*`. Duas delas, lidas juntas, invertem
o incentivo do WORM.

`main.go:834` — `if production && cfg.DSARVault == nil && (durableExecution || cfg.WORMPath != "")`
⇒ `ErrProductionNeedsDurableKEK` (`main.go:316`). Definir `AOS_WORM_PATH` em produção **sem**
`AOS_DSAR_VAULT_ADDR` faz o nó **recusar arrancar**, e com razão: a KEK tem de ser tão durável quanto
o substrato que cifra.

`main.go:853` — `if production && eventStorePath == "" && eventStoreNATS == ""` ⇒
`ErrProductionNeedsDurableSubstrate` (`main.go:272`). O Event Store tem guarda de durabilidade
**incondicional** em produção.

O WORM não tem a simétrica. `bootstrap.go:1140-1156`: se `cfg.WORM` é nil e `cfg.WORMPath != ""`
abre o `FileStore` (`:1144`); senão cai em `worm = audit.NewMemStore()` (`:1154`) — **sem consultar
o modo**. O banner declara-o honestamente («in-memory de referencia (nao-duravel)»,
`bootstrap.go:2753`, composto em `:2731`) e o nó arranca.

**Medido no binário real** (§3.6, O-12): produção + Event Store durável + WORM vazio **arranca**;
produção + `AOS_WORM_PATH` definido **recusa arrancar**. Sem Vault externo, a única configuração de
produção que arranca é a do WORM volátil. O operador que tenta fazer a coisa certa recebe um erro; o
que não tenta nada recebe um nó.

**O dano real, e o dano imaginado.** O dano **não** é perda de dados de conteúdo: o Event Store é
durável por exigência de `:853`, e a KEK está correctamente protegida pela guarda que dispara
(DEF-302 regista a custódia in-memory e a guarda cumpre-o — medido). O dano é que a **hash-chain
tamper-evident morre com o processo**: o trilho a que `tecnica/17` §5.1 atribui a propriedade de
detecção, e sobre o qual assentam o selo de residência, o changelog de política de AOS-310, os selos
de legal hold e de expiração, e a atribuição de quem destruiu o quê. Um restart apaga a prova, não o
efeito.

**Ressalva que reduz o alcance sem eliminar o defeito.** Um operador que provisione Vault fecha os
dois lados de uma vez, e o deployment sancionado de `deploy/server` monta um caminho de WORM. O
defeito é que o **binário** não o exige, e que a configuração mais fácil de alcançar é a insegura.

**Porque sobreviveu.** As dez guardas `ErrProductionNeeds*` foram escritas uma a uma, cada uma a
fechar o achado que a motivou, e nenhuma varredura perguntou de forma sistemática *quais os
substratos que ainda não têm guarda*. É literalmente o mesmo buraco que AOS-344 fechou para o driver
de sandbox, e o comentário de `deploy/server/README.md:574` — «que é como a terceira tinha passado
despercebida» — antecipou-o involuntariamente. O agravante próprio deste caso é que o banner honesto
**desarma a suspeita**: quem lê `worm=in-memory de referencia (nao-duravel)` vê uma declaração
correcta e não pergunta se produção devia tê-la aceite. A honestidade do banner substituiu a guarda.

*Nota: todas as âncoras deste achado batem certo. O relatório não as cita ao nível da linha; ficam
fixadas aqui como contrato — `main.go:316`, `:834`, `:853`, `bootstrap.go:1154`, `:2753`.*

### Critérios de Aceitação

- [ ] Sob `AOS_MODE=production` o WORM não-durável deixa de arrancar: erro dedicado no molde das dez
      guardas `ErrProductionNeeds*` já existentes, exportado e comparável com `errors.Is`
- [ ] Teste: `run`/`nodeConfigFromEnv` com `AOS_MODE=production` + `AOS_EVENTSTORE_PATH` definido +
      `AOS_WORM_PATH` ausente devolve esse erro
- [ ] **Controlo negativo em dois sentidos:** (a) a mesma configuração **fora** de produção continua a
      arrancar com o `MemStore`, sem alteração de comportamento; (b) produção + `AOS_WORM_PATH` +
      `AOS_DSAR_VAULT_ADDR` arranca — a guarda nova não colide com `ErrProductionNeedsDurableKEK`
- [ ] Guarda provada não-vacuosa por mutação: com a recusa desligada, o teste de (a) mantém-se verde e
      o teste principal avermelha
- [ ] A mensagem de erro nomeia **as duas** variáveis (`AOS_WORM_PATH` e `AOS_DSAR_VAULT_ADDR`), para
      que o operador não troque um erro por outro; teste que assere a presença de ambas na string
- [ ] `deploy/server/README.md` ganha a linha do WORM durável na tabela de pré-requisitos de produção
      (`:576-583`, a mesma tabela que AOS-344 alargou)

### Estado

**IMPLEMENTADO** (2026-09-07). Uma guarda `ErrProductionNeedsDurableWORM` — exportada e comparável
com `errors.Is`, no molde INCONDICIONAL de `ErrProductionNeedsDurableSubstrate` — passa a recusar o
arranque quando `AOS_MODE=production` e `AOS_WORM_PATH` está ausente (`nodeConfigFromEnv`,
`packages/cmd/aos/main.go`). Fica DEPOIS da guarda do substrato (para não roubar o diagnóstico quando
faltam os dois) e DEPOIS da da KEK (pô-la antes tornaria o ramo `cfg.WORMPath != ""` da KEK
sempre-verdadeiro em produção e mudar-lhe-ia o significado). A mensagem nomeia **as duas** variáveis
(`AOS_WORM_PATH` e `AOS_DSAR_VAULT_ADDR`), porque um WORM durável arrasta uma KEK durável (AOS-215) —
e só essas duas: o Vault que `AOS_DSAR_VAULT_ADDR` compõe (`*vaultKeyVault`) já implementa a porta de
confirmação de shred, pelo que a guarda AOS-328 passa por si e o operador que segue a mensagem
arranca. Testes: recusa (`nodeConfigFromEnv`), controlo negativo em dois sentidos (fora de produção
arranca com o MemStore — provado pelo banner `worm=in-memory de referencia`; produção + WORM + vault
compõe sem colidir com a KEK), ordem fixada nos dois lados (substrato-antes e KEK-antes do WORM),
mensagem-nomeia-as-duas, e não-vacuidade por mutação (guarda desligada ⇒ recusa avermelha, controlo
verde). As seis fixtures de produção que o novo eixo passou a apanhar ganharam WORM+KEK duráveis
(`fixarSubstratoDuravelDeProducao`), o mesmo alargamento que AOS-300 fez com o Event Store. Provado
por `-race` em `cmd/aos`, `layer-lint`, e o smoke `run-aos` 9/9 (passo 8: WAL+WORM coerentes).
Revisto por revisão adversarial independente, que apanhou uma premissa falsa da primeira versão (o
vault de referência de teste ATÉ implementa a porta de confirmação, logo o
`AOS_DSAR_VAULT_DESTROY_UNCONDITIONAL` era redundante e enganador) — corrigida no helper e no README
antes do commit. Nenhum critério deferido.

---

## AOS-366 — O nó desarma incondicionalmente o cliente endurecido do gateway de modelo e chama-lhe seam de desenvolvimento num binário de produção

### Contexto

O contrato do gateway é explícito nos dois sentidos.
`packages/platform/model-gateway/production.go:108-114`: «*HTTPClient é opcional. Se nil, o gateway
constrói um cliente **ENDURECIDO** para o egress REAL (timeout + TLS 1.2 + limite de redirect +
re-validação de cada salto de redirect contra AllowedEgressHosts) e **VALIDA o BaseURL** (https +
allowlist) antes de arrancar*». E `:115-120`: a allowlist de egress é «*Ignorada quando um HTTPClient
é injectado*».

O caminho endurecido está inteiramente dentro de um `if`: `newProviderAdapter`
(`production.go:340`) só corre `validateEgressURL` (`:348`) e `newHardenedEgressClient` (`:351`)
quando `client == nil` (`:346`). O endurecimento em si é bom e está entregue — timeout de 30 s e
limite de 5 redirects (`:362-365`), allowlist com a **porta** na chave para fechar o desvio para uma
porta interna de um host allowlisted (`:367-374`).

O nó injecta um cliente **incondicionalmente**:
`packages/cmd/aos/modelgatewaywiring.go:225` — `HTTPClient: &http.Client{Timeout: 60 * time.Second},
// seam de dev: delega validação de egress`. **Não existe ramo não-dev.** `AllowedEgressHosts` nunca
é preenchido pelo wiring do nó (`grep AllowedEgressHosts packages/cmd/aos` devolve apenas a menção em
prosa do cabeçalho). Consequência: a validação de `BaseURL` (https + allowlist) e o endurecimento
SSRF de AOS-223 ficam fora do caminho em **qualquer** configuração, `AOS_MODE=production` incluída,
na única perna de egress paga do nó.

O próprio ficheiro diz o que devia acontecer e não acontece.
`modelgatewaywiring.go:10-14`: «*EGRESS EM DEV: injecta-se um `http.Client` simples […] Em produção
remove-se o HTTPClient e usa-se BaseURL https + AllowedEgressHosts (o SSRF fail-closed de AOS-223
volta a valer)*». É prosa; o código não tem a condição.

**O dano real, e o dano imaginado.** O dano **não** é ausência de endurecimento — o endurecimento
existe, está testado, e a allowlist regional do gateway continua ed25519-assinada com trust anchor
pinado por fingerprint em código
(`packages/platform/model-gateway/policy/allowlist/allowlist.go:81`, `:110`, `:159`), o que a
auditoria confirmou e não é posto em causa aqui. O
dano é o nó **desarmar activamente** um endurecimento já entregue, e declará-lo com um comentário que
diz «dev» dentro do binário que serve produção. Um `AOS_MODEL_ENDPOINT` em `http://` ou apontado a um
host arbitrário é aceite sem validação.

**Ressalva que reduz o alcance sem eliminar o defeito.** A classe de risco está declarada
(AOS-184), e o deployment endurecido põe um gateway externo à frente, pelo que na configuração
sancionada o destino real é interno e controlado. O que não está declarado em documento nenhum é que
o nó desarma o endurecimento em **todas** as configurações, incluindo aquelas em que o gateway
externo não existe.

**Porque sobreviveu.** As duas leituras que um revisor faz confirmam-se mutuamente sem que nenhuma
toque no código: o comentário na linha da injecção diz «seam de dev», e o cabeçalho do ficheiro
promete o ramo de produção onze linhas acima. Quem auditou o gateway viu o caminho endurecido
correcto e completo; quem auditou o nó viu um `http.Client` banal com timeout. É por isso que este
achado não nasceu de nenhuma lente vertical — nasceu de um refutador a verificar o que ambas tinham
assumido.

*Nota: todas as âncoras do relatório batem certo (`production.go:108`, `:118`,
`modelgatewaywiring.go:225`). Acrescenta-se aqui `production.go:340-353` — o `if client == nil` que
é o ramo efectivamente contornado — e `modelgatewaywiring.go:10-14`, a prosa que promete a
condição.*

### Critérios de Aceitação

- [ ] Sob `AOS_MODE=production`, `newGatewayModelClient` deixa `HTTPClient` a nil e preenche
      `AllowedEgressHosts` (do host de `AOS_MODEL_ENDPOINT`, ou de variável própria — a decisão fica
      escrita)
- [ ] Teste que construa o cliente de modelo sob produção com `AOS_MODEL_ENDPOINT` em `http://` e
      prove que a construção **recusa** (o erro de `validateEgressURL` propaga-se); e outro com um
      host fora da allowlist, que também recusa
- [ ] Teste que prove que em produção o transporte efectivo é o endurecido e não o injectado — por
      exemplo, que o timeout observado é `egressTimeout` (`production.go:363`, 30 s) e não os 60 s de
      `modelgatewaywiring.go:225`
- [ ] **Controlo negativo:** fora de produção o seam de httptest continua a funcionar — os testes
      existentes que apontam o nó a um `httptest.Server` em `http` mantêm-se verdes sem alteração
- [ ] Guarda provada não-vacuosa por mutação: com o ramo de produção desligado (voltando a injectar
      sempre), os testes de recusa avermelham
- [ ] `modelgatewaywiring.go:10-14` deixa de descrever um ramo que não existe, e passa a descrever o
      que o código faz
- [ ] `deploy/server` documenta a variável da allowlist de egress na tabela de portas de produção

### Estado

**POR IMPLEMENTAR.**

---

## AOS-367 — Um token OIDC emitido para ler runs autoriza o crypto-shred irreversível, e duas das três rotas de destruição não verificam região

### Contexto

**Não existe capability no plano de governação.** `readGovernance.authorize`
(`packages/cmd/aos/sovereignty.go:514`) delega em `authorizeComCausa` (`:534`), que aplica a regra de
`autorizarComCausa` (`:164-213`). A regra inteira é: ID-token OIDC verificado, principal e board
não-vazios (`:203-205`), e uma claim `board` que resolva para uma região via
`g.regions.RegionFor(board)` (`:210-213`). Nada mais.

Não há capability a extrair porque **`oidc.Claims` não tem campo de escopo**:
`packages/integration/oidc/oidc.go:151-164` declara quatro campos — `Subject`, `Email`, `Issuer`,
`Board` — e `oidcReadCredential.verify` (`packages/cmd/aos/read_credential.go:62-82`) devolve
exactamente `(sub, board)`. Um só par issuer/audience
(`AOS_SOVEREIGN_OIDC_ISSUER`/`AOS_SOVEREIGN_OIDC_AUDIENCE`, exigidos em produção por
`ErrProductionNeedsSovereignAuthority`, `main.go:63`) serve o leitor e o operador DSAR.

O mesmo `authorize` porteia **todo** o plano de governação. `planos.go:243-250` regista
`POST /dsar/erase`, `/dsar/hold`, `/dsar/release` e `/dsar/expire` em `planoGovernacao`, e os
handlers chamam o mesmo predicado: `dsar.go:177`, `legalhold.go:122` (que serve `/dsar/hold` e
`/dsar/release`) e `legalhold.go:273`. Um token emitido para **ler** runs autoriza o crypto-shred.

**O contraste que fecha o argumento.** `POST /autonomy` — uma acção **reversível**, e selada no
WORM — exige três coisas que a destruição irreversível não exige nenhuma:
capability `autonomy:set` sobre o emissor registado (`autonomy_route.go:113-121`), assinatura ed25519
sobre o payload canónico com nonce de uso único durável (`:122-131`), e uma **segunda** assinatura de
emissor distinto para mudar para L4/L5 (`:134-151`).

**Região: a barreira existe numa rota e não nas outras duas.** `/dsar/erase` tem-na —
`podeApagarTitular` (`residencia_do_titular.go:69`), chamada em `dsar.go:213`, quantifica a
residência sobre todos os runs do titular e nega fail-closed em cada perna. As irmãs não:

- `handleLegalHold` (`legalhold.go:108-186`), que serve `/dsar/hold` e `/dsar/release`, usa
  `reader.region` **apenas** para preencher o campo `Resource.Region` do selo
  (`sealLegalHold`, `:219`) — nunca para decidir.
- `handleExpire` (`legalhold.go:252`) autentica (`:273`), sela quem disparou (`:288`) e corre a
  passagem sobre **todos** os titulares fora do TTL, sem fronteira de região nenhuma.

**O dano real, e o dano imaginado.** O dano **não** é ausência de autenticação: a credencial é forte
e verificada (AOS-205), o gate é fail-closed em cada perna, o nó recusa arrancar em produção sem ela,
e cada acção é selada no WORM com principal e board. O que falta é **autorização**, não autenticação.
O dano é (i) não haver separação de deveres entre quem lê e quem destrói, com um só par
issuer/audience a servir os dois papéis, e (ii) `/dsar/release` permitir a um chamador de outra
região levantar a preservação de um titular cuja residência a fronteira devia proteger.

**Ressalva que reduz o alcance sem eliminar o defeito.** `/dsar/release` só levanta um hold; não
destrói. Mas o varredor **automático** de retenção (`retention_sweeper.go:222-226`, ticker próprio)
conduz a mesma `ExpirationJob` e salta os titulares em hold — pelo que levantar o hold é
precisamente remover o que travava a destruição sem qualquer acção humana subsequente.

**Porque sobreviveu.** O achado de 2026-08-21 que produziu `podeApagarTitular` está registado em
`residencia_do_titular.go:9-22` e foi enunciado **sobre o `/dsar/erase`** («*`POST /dsar/erase` sobre
o titular dessa região → 200 "erased"*»); foi fechado nessa rota, e as três irmãs registadas no mesmo
bloco de `planos.go` herdaram a autenticação e não a autorização. Além disso, o único documento que
olha para as quatro rotas em conjunto olha para o eixo errado: `planos.go:113-122` discute
explicitamente a ausência de mTLS neste plano e conclui que a asserção OIDC «*identifica o principal
de forma pelo menos tão forte*» — o que é verdade sobre **identificação** e desloca a atenção da
**autorização**, que é a metade em falta. Nenhum gate compara as rotas de um mesmo plano entre si.

*Nota de correcção de âncoras: o relatório cita `cmd/aos/sovereignty.go:164-212` para
`readGov.authorize`; `authorize` está em `:514` e `authorizeComCausa` em `:534` — o intervalo
`164-212` é o de `autorizarComCausa`, que é onde a regra vive. Cita `read_credential.go:63-80`; a
função `verify` começa em `:62` e termina em `:82`. A âncora `integration/oidc/oidc.go:151-164` bate
certo.*

### Critérios de Aceitação

- [ ] As quatro rotas de `planoGovernacao` (`/dsar/erase`, `/dsar/hold`, `/dsar/release`,
      `/dsar/expire`) passam a exigir prova de autoridade distinta do simples ID-token de leitura:
      uma capability num claim verificado, ou a cerimónia de assinatura que `/autonomy` já exige. A
      escolha e o seu racional ficam escritos no ticket de implementação
- [ ] Teste: um token OIDC válido, com `sub` e `board` que resolvem, **sem** essa prova, recebe 403
      nas quatro rotas
- [ ] **Controlo negativo em dois sentidos:** (a) o mesmo token continua a autorizar `GET /runs/{id}`
      da sua região com 200 — a leitura soberana não regride; (b) um token **com** a prova recebe
      resposta não-403 nas quatro rotas
- [ ] `/dsar/release` recusa quando a região do chamador não coincide com a residência do alvo, no
      molde de `podeApagarTitular`; teste com controlo positivo (mesma região ⇒ `released`) e negativo
      (região diferente ⇒ 403, e o hold **continua** em vigor, verificado no `audit.LegalHold`)
- [ ] `/dsar/expire` fica restrito à região do chamador ou exige prova adicional para varrer fora
      dela; a decisão fica escrita e é testada nos dois sentidos, incluindo a prova de que o varredor
      automático (`retention_sweeper.go:222`) mantém o comportamento actual
- [ ] Guardas provadas não-vacuosas por mutação: com a verificação de autoridade desligada, os testes
      de 403 avermelham; com a verificação de região desligada, o teste de `/dsar/release`
      cross-region avermelha
- [ ] `planos.go:113-122` é reconciliado: a decisão em aberto sobre mTLS passa a distinguir
      explicitamente identificação de autorização, e a nova barreira fica declarada

### Estado

**IMPLEMENTADO** (2026-09-07). As quatro rotas do `planoGovernacao` passam a exigir **prova de
autoridade distinta do id-token de leitura**: a cerimónia ed25519 que o `/autonomy` já usa
(assinatura do corpo sobre payload canónico `CanonicalDSARPayload(acção‖alvo‖request_id)` com nonce
durável de uso único), autorizada por um capability-set **dedicado** `AOS_DSAR_ERASERS` (⊆
`AOS_OPERATORS`, capability `dsar:erase`) — separação de deveres entre quem lê, quem muda autonomia e
quem destrói PII. **Racional da escolha** (a AC pedia-o escrito): a via da capability num claim OIDC
exigia alterar o contrato partilhado `oidc.Claims` (sem campo de scope), depender do IdP emitir o
claim, e não produzia prova *distinta* do token de leitura (não há segundo verificador); a cerimónia
ed25519 é o idioma já estabelecido do nó para operações de autoridade irreversíveis (`/autonomy`,
`/nhi/revoke`), é durável, dual-control-capaz, e a chave privada nunca entra no nó.

**Retro-compatível** (não quebra o cluster): a prova é *opt-in por composição* (conjunto vazio ⇒
comportamento legado, como o TaintGate de AOS-363); **em produção o arranque exige-a**
(`ErrProductionNeedsDurableWORM`-style `ErrProductionNeedsDSARErasers`, incondicional no bloco de
durabilidade). A `acção` entra no payload assinado ⇒ uma assinatura de "hold" não se reapresenta como
"erase"; `DSARScope`/`SignalDSAR` distintos ⇒ nenhuma assinatura de `/autonomy` ou `/nhi/revoke`
verifica aqui. **Região**: `/dsar/release` confronta a residência do alvo — titular (via
`podeApagarTitular`) **e** partição-só (via `podeLibertarParticao`) — cross-region ⇒ 403 e o hold
mantém-se. `/dsar/expire` (varrimento global de TTL, sem alvo único) exige **dual-control** — duas
assinaturas de erasers distintos, no molde de L4/L5 — que é a "prova adicional para varrer fora da
região" que a AC permite; o varredor automático (`retention_sweeper.go`) fica intacto.
`planos.go:113-122` reconciliado (distingue identificação de autorização). Docs: `AOS_DSAR_ERASERS`
em `deploy/node/README.md`, `docker-compose.prod.yml`, e a décima porta em `deploy/server/README.md`.

Provado por `-race` em `cmd/aos` (+ `integration`, `kernel/agent-runtime/control`), guardas
não-vacuosas por mutação (autoridade desligada ⇒ os 403 avermelham; região do release desligada ⇒
o cross-region avermelha, tanto por titular como por partição), e o smoke `run-aos`. **Revisão
adversarial de segurança independente**: sem bypass, sem replay, sem fail-open; apanhou um residual
(o release só-de-partição saltava a barreira de região) — **fechado** com `podeLibertarParticao` e
teste dedicado antes da integração, não aceite como residual. Nenhum critério deferido.


Tickets AOS-368 a AOS-373, redigidos a partir de `analises/13_Auditoria_GOV_OBS_Adversarial.md`
(§3.4 O-01/O-02/O-03/O-04, §3.5 O-07/O-08, §3.6 O-13). Todas as âncoras `ficheiro:linha` foram
reverificadas contra a árvore; as divergências face ao relatório estão assinaladas no próprio
Contexto de cada ticket.

---

## AOS-368 — O documento OTLP sai sem atributos de recurso e com a espécie de span fixa em INTERNAL

### Contexto

`packages/substrate/otel-genai/otlp.go:74` declara `MarshalOTLP(spans []SpanData, scope string)`:
os dois únicos parâmetros são os spans e o nome do instrumentation scope. O documento é montado
em `:82-89` com um `otlpResource{ScopeSpans: ...}` cujo campo `Resource` fica no valor-zero, pelo
que `resourceSpans[0].resource` chega ao backend vazio.

**Correcção ao relatório.** O O-01 diz que a serialização «não tem sequer parâmetro por onde
injectar recurso». Isso é verdade da *assinatura*, mas não do *formato*: o struct de wire já tem
o conceito — `otlpResource.Resource otlpResourceBody` (`otlp.go:23-30`), com
`Attributes []otlpKeyValue`. Não falta representação; falta quem a preencha e por onde a receber.
Isto reduz o trabalho, não o achado.

`service.name` tem **zero** ocorrências em toda a árvore (procura sobre `.go`, `.json`, `.yml`,
`.yaml`). Tudo chega ao backend como `unknown_service`.

Nada a jusante compensa, e verificou-se de primeira mão nos dois sítios onde poderia:

- O adaptador do nó não tem o valor: `packages/cmd/aos/otlpexporter.go:449` chama
  `otelgenai.MarshalOTLP(spans, e.scope)`, e o único campo de identidade que o exportador detém é
  `scope` (`:97`, sobreponível por `WithOTLPScope` em `:219-226`). Não há campo de recurso.
- As configs de colector entregues também não: `deploy/server/otel-collector.yaml:85,89` compõem
  os pipelines de traces e métricas com `processors: [memory_limiter, batch]` — não há processor
  `resource` que injecte `service.name` à passagem.

A espécie de span é literal: `otlp.go:108` escreve `Kind: 1, // SPAN_KIND_INTERNAL` para **todos**
os spans. E é pior do que ler só o serializador sugere: `SpanData`
(`packages/substrate/otel-genai/exporter.go:35-43`) tem `Name`, `SpanContext`, `ParentSpanID`,
`StartUnixNano`, `EndUnixNano`, `Attributes` e `Status` — **não tem campo `Kind`**. A porta não
tem o conceito, logo nenhum produtor de spans o poderia declarar mesmo que quisesse. A chamada ao
modelo, que a convenção trata como CLIENT, sai indistinguível de trabalho interno do processo.

**Enquadramento obrigatório, e é o que delimita o ticket.** O nó é zero-dep por ADR-017, e a
adopção do SDK oficial `go.opentelemetry.io` está fora **por decisão de arquitectura registada** —
DEF-002 a DEF-007 no `docs/governance/REGISTO-Deferimentos.md`, uma linha por ficheiro de
`otel-genai`, todas com estado `FECHADO-RESIDUAL` e gatilho de reavaliação próprio («deployment
que exija o SDK OTel oficial, o que quebraria a regra zero-dep»). DEF-004 cobre exactamente
`otlp.go`. Este ticket **não** propõe adoptar o SDK nem contesta a decisão: propõe que o
adaptador próprio — que existe, é entregue, é melhor do que a média (fila não-bloqueante,
fail-open, contadores `aos_otlp_spans_{exported,failed,dropped}` em `/metrics`) e é o que
realmente serializa — emita o recurso e a espécie de span que qualquer backend precisa para
atribuir um trace. É trabalho dentro da decisão zero-dep.

**Porque sobreviveu.** Nenhum gate lê o lado do recurso do que o serializador produz: os dois
testes do ficheiro (`otlp_test.go:8` `TestMarshalOTLPShape` e `:83`
`TestMarshalOTLPRootHasNoParent`) verificam a forma do documento e a ausência de parent na raiz,
sem afirmar nada sobre `resource` ou `kind`. E nada foi alguma vez corrido contra um colector
real — a própria auditoria declara-o em §6.2 («nada foi corrido contra um colector OTLP real»).
Um documento OTLP com `resource` vazio é sintacticamente válido: falha na atribuição, não na
serialização, e por isso passa em todo o lado onde só se verifica que serializa.

### Critérios de Aceitação

- [ ] `MarshalOTLP` ganha por onde receber os atributos de recurso (parâmetro ou tipo de opções) e
      povoa `otlpResource.Resource.Attributes`; um documento serializado com recurso passa a ter
      `resourceSpans[0].resource.attributes[]` não-vazio, verificável desserializando o output do
      teste
- [ ] `service.name` aparece no documento OTLP emitido pelo nó:
      `grep -rn 'service\.name' packages/ | grep -v _test` devolve pelo menos uma ocorrência de
      produção, e o valor é configurável por variável `AOS_*` com valor por omissão determinista
- [ ] `SpanData` (`otel-genai/exporter.go:35-43`) ganha campo de espécie de span, e
      `grep -n 'Kind: 1' packages/substrate/otel-genai/otlp.go` devolve zero — o literal deixa de
      existir
- [ ] O span da chamada ao modelo sai como CLIENT (`kind` 3 no OTLP/JSON) e o span
      `aos.execute_tool` mantém a espécie que o produtor declarar; verificável desserializando o
      corpo produzido por `MarshalOTLP` num teste que cubra os dois casos
- [ ] `packages/cmd/aos/otlpexporter.go` passa o recurso na chamada de `:449`, no mesmo molde de
      `WithOTLPScope` (`:219-226`) — o exportador deixa de poder serializar sem identidade
- [ ] Teste com controlo negativo: um teste que sirva um colector falso em processo, capture o
      corpo POSTado pelo exportador e afirme `service.name` presente mais a espécie correcta do
      span do modelo; **controlo negativo** — o mesmo teste, com a injecção de recurso retirada
      por mutação, tem de avermelhar, e um `SpanData` cuja espécie não seja declarada tem de
      continuar a serializar INTERNAL (compatibilidade dos produtores existentes)
- [ ] `deploy/server/otel-collector.yaml` e `deploy/node/dev-hardened/otel-collector.yaml` são
      reavaliados: ou ganham o processor `resource`, ou fica escrito no ficheiro que o recurso vem
      do produtor e não do colector

### Estado

**IMPLEMENTADO** (2026-09-07), dentro da decisão zero-dep (DEF-004 — sem SDK OTel). `MarshalOTLP`
ganha opções variádicas (`WithServiceName`) e povoa `resourceSpans[0].resource.attributes`; o literal
`Kind: 1` desaparece de `otlp.go` (mapeamento `otlpSpanKind`: INTERNAL→1, CLIENT→3). `SpanData` ganha
`Kind SpanKind` com **valor-zero = INTERNAL** (aditivo, todos os produtores são keyed ⇒ retro-compat),
e `StartSpan` define-o por `KindForOperation(operação)`: as chamadas de saída ao modelo — **`chat` e
`embeddings`** — são CLIENT, o resto INTERNAL. O exportador ganha `WithOTLPServiceName` e um default
determinista **`"aos"`** (nunca serializa `unknown_service`); `service.name` é configurável por
`AOS_OTLP_SERVICE_NAME`. Os três `otel-collector.yaml` (server, server-mtls, dev-hardened) ganham um
comentário que declara que a identidade do recurso vem do PRODUTOR, não de um processor `resource`.

Provado por `-race` em `otel-genai` e `cmd/aos`, guardas não-vacuosas por mutação (recurso removido ⇒
a asserção de `service.name` avermelha; mapa de kind partido ⇒ o caso INTERNAL avermelha), e o smoke
`run-aos`. Teste ponta-a-ponta contra um colector falso em processo (`aos368_test.go`) captura o
corpo POSTado e afirma `service.name` + o kind CLIENT do span do modelo. **Revisão adversarial
independente** (foco proporcional — telemetria): sem regressão, default `"aos"` inbypassável,
zero-dep preservado; apanhou que os **embeddings** saíam INTERNAL (mesma classe de defeito, operação
irmã) e uma inconsistência num terceiro colector-YAML — **ambos fechados** (embeddings → CLIENT com
`TestKindForOperation`; comentário no `otel-collector-mtls.yaml`) antes da integração. Nenhum critério
deferido.

---

## AOS-369 — Nenhum contador de mediação chega ao `/metrics`, e a falha de selagem do Reference Monitor não chega ao `/readyz`

### Contexto

`packages/kernel/reference-monitor/monitor.go:478` — dentro de `fail()` —
`seq, _ := m.sink.RecordMediation(regCtx, MediationRecord{...})`. O erro do registo durável é
descartado **por desenho declarado**, e o comentário de `:457-475` justifica-o bem: em
deny/escalate o efeito já está bloqueado, pelo que uma falha de auditoria não pode alterar uma
decisão já tomada, e o ctx do chamador não pode cancelar o registo de um facto consumado
(`context.WithoutCancel` + prazo próprio). Essa parte está certa. O que fica sem cobertura não é a
decisão — é o facto de **a prova se ter perdido**, que não é contado, exposto nem sinalizado em
lado nenhum.

Os contadores existem e estão a ser incrementados. `monitor.go:46-49` define
`Metrics{Permits, Denials, Escalations}` (`atomic.Uint64`), com `Snapshot()` em `:53` e o acessor
`Monitor.Metrics()` em `:195`; os incrementos estão em `:275` (deny do default-deny), `:409`
(permit) e `:490-492` (deny/escalate do `fail`). São alcançáveis a partir do nó por código de
produção: `integration/secured.go:630` — `func (s *SecuredRuntime) Monitor() *referencemonitor.Monitor` —
e `cmd/aos/bootstrap.go:677` — `Runtime *integration.SecuredRuntime`. Não falta API: falta
plumbing.

E ninguém os lê. `Metrics().Snapshot()` tem call-sites **só em testes** (`acceptance_mediation_test.go`,
`aos220_pdp_bundle_surface_test.go`, `aos258_budget_permit_node_test.go`, `devharness_test.go`,
`observability_durable_test.go`, `observability_test.go`). No handler de `/metrics`
(`packages/cmd/aos/api.go`, séries a partir de `:1169`) há 37 chamadas ao emissor `g(...)` e
nenhuma nomeia mediação; no scrape real são 17 séries, porque muitas são condicionais e só saem
quando o mecanismo respectivo existe.

O `/readyz` **já** reflecte falha de selagem — mas só a de três rotas. `api.go:1123-1128` lê
`h.svc.seloWORM.aRecusarEscritas()` e devolve 503; o mecanismo por trás
(`packages/cmd/aos/selo_worm_saude.go`) é bom e está honestamente delimitado: `:45-47` declara o
residual — «só se observam os `Append` DESTAS TRÊS VIAS» (residência de run em `POST /runs`, selo
de leitura sensível em `GET /runs/{id}`, legal hold em `POST /dsar/hold`). O `RecordMediation` do
Reference Monitor não passa por `saudeDeSelagem` em caminho nenhum.

**Correcção de âncora.** O achado citava `selo_worm_saude.go:43-46`; a declaração do residual está
em `:45-47`. A linha `:43` é «Um nó parado não tem incidente», que pertence ao parágrafo anterior.

**Medido (§3.6, O-13).** Com PERMIT e o sink partido o caminho faz **duas** tentativas e grava
zero: primeiro o audit-before-effect em `monitor.go:395` (`seq, err := m.sink.RecordMediation(ctx, rec)`),
cujo erro degrada correctamente para deny em `:397-401`; depois o `fail` desse deny volta a tentar
em `:478` e descarta o erro. Um WORM em baixo nega 100% das tool calls sem deixar rasto nenhum, e
de fora — banner, log, `/healthz`, `/readyz`, `/metrics` — é indistinguível de um nó ocioso.
Existem `aos_worm_seal_failures_total` e `aos_worm_seal_last_failure_age_seconds`
(`api.go:1378,1383`), mas contam só as três vias de governação, exactamente como o ficheiro
declara.

**Porque sobreviveu.** O eixo da observabilidade da selagem foi aberto e fechado para as três
rotas onde o dano tinha sido observado, e o residual foi escrito em voz alta no topo do ficheiro
— disciplina exemplar que teve o efeito colateral de fazer o buraco restante parecer endereçado.
Ninguém voltou para a via que é a razão de ser do nó. E o smoke nunca medeia uma tool call: 30
corridas inspeccionadas produziram 302 registos no WORM, todos `allow`, e os únicos `tool_id` são
governação HTTP (`gov.control`, `gov.read`, `gov.residency`, `gov.sovereignty`) — zero partições
`smoke-*`, zero `tool.call.mediated` no Event Store. Nenhuma corrida verde alguma vez exercitou o
caminho onde o contador faltaria.

### Critérios de Aceitação

- [ ] `GET /metrics` expõe os três contadores do Reference Monitor alimentados por
      `Monitor().Metrics().Snapshot()`: `curl -s localhost:PORT/metrics | grep -c '^aos_mediation_'`
      devolve pelo menos 3
- [ ] Existe série que conte as falhas de registo durável de mediação (o `_` de `monitor.go:478`
      deixa de ser descartado sem contador), com nome e HELP que digam o que se perde quando sobe
- [ ] `grep -n 'seq, _ :=' packages/kernel/reference-monitor/monitor.go` devolve zero
- [ ] `/readyz` devolve 503 quando o registo de mediação está a falhar, pelo mesmo mecanismo de
      último-desfecho de `saudeDeSelagem` (não por «alguma vez falhou»), e recupera para 200
      quando uma selagem seguinte tem êxito
- [ ] Teste ponta-a-ponta com **controlo negativo**: nó levantado com sink de auditoria partido e
      uma tool call mediada ⇒ o contador de falhas sobe e `/readyz` responde 503; **controlo
      negativo** — o mesmo teste com o sink são ⇒ contador de falhas a zero, contador de permits
      a subir e `/readyz` a 200. O teste tem de avermelhar se a ligação ao `/readyz` for retirada
      por mutação
- [ ] `selo_worm_saude.go:45-47` é reescrito para descrever a cobertura real depois desta
      alteração, sem sobredeclarar: o que passa a ser observado e o que continua a não ser

### Estado

**IMPLEMENTADO** (2026-09-07). O `Metrics` do Reference Monitor ganha `recordFailures` (contador) e
`recordingFailing` (último-desfecho, valor-zero = saudável), com acessores próprios
(`RecordFailures()`, `RecordingHealthy()`) — o `Snapshot()` de 3 valores fica intacto (≈20 chamadores
de teste). O `seq, _ :=` de `monitor.go:478` passa a `seq, err :=`: uma falha de registo pós-decisão
conta e marca `recordingFailing`; a decisão (deny/escalate) e o `WithoutCancel`+timeout ficam
inalterados. Sem dupla contagem: o site do permit (`:395`) só limpa o último-desfecho em sucesso; a
contagem vive no `fail()`. `GET /metrics` expõe `aos_mediation_{permits,denials,escalations}_total` +
`aos_mediation_record_failures_total` (HELP diz que a PROVA se perdeu, o deny aconteceu). `/readyz`
devolve 503 quando `!RecordingHealthy()` (último-desfecho, auto-recupera na selagem seguinte), e o
`aos_ready` reflecte o mesmo predicado (agora cinco condições). `selo_worm_saude.go:45-47` reescrito:
a saúde de selagem cobre as três rotas de governação; a falha de registo de mediação é um eixo
SEPARADO. A fronteira de camadas é respeitada (o RM não importa cmd/aos; o fluxo é cmd/aos → lê → RM).

Provado por `-race` em `kernel/reference-monitor` (inclui `archlint`) e `cmd/aos`, guardas
não-vacuosas por mutação (cláusula do /readyz removida ⇒ o 503 avermelha; `:478` a descartar de novo
⇒ a contagem avermelha), e o smoke `run-aos`. Teste ponta-a-ponta conduz uma tool call **realmente
mediada** (via `Runtime.Run`, não uma selagem de governação HTTP) com sink de auditoria partido
(`wormSoLeitura`): FASE saudável ⇒ permits sobe, record-failures 0, /readyz 200; FASE em baixo ⇒
record-failures sobe, /readyz 503; FASE recuperada ⇒ /readyz volta a 200 sem o contador recuar.
**Revisão adversarial independente** (foco no cenário «stuck-ready»): runtime SHIP-READY — sem
double-count, sem stuck-ready, último-desfecho equivalente ao `saudeDeSelagem`, decisão inalterada,
camada limpa. Apanhou dois eixos de mediação **não cobertos pelos testes-espelho** do achado F
(`TestEspelhos_ReadyzEAosReadyConcordam` e o HELP) — **fechados** antes da integração (emparelhamento
/readyz↔aos_ready afirmado no e2e das três fases; termo "mediacao" amarrado ao HELP, teste renomeado
para `...NomeiaAsCinco`). Nenhum critério deferido.

---

## AOS-370 — `error.type` recebe a mensagem crua da tool no mesmo span onde o input é hasheado por princípio

### Contexto

`packages/kernel/reference-monitor/monitor.go:253` —
`span.SetAttribute(otelgenai.AttrErrorType, dec.ToolErr.Error())`. O erro é o da tool a jusante:
uma string arbitrária, produzida fora do núcleo, escrita literalmente num atributo de span que sai
do processo.

**O argumento decisivo é interno ao ficheiro, e à mesma função.** `monitor.go:226` escreve
`AttrToolCallHash` com `toolCallHash(call.ToolID, call.Input)`, e o comentário imediatamente
acima (`:224-225`) diz porquê: «REFERÊNCIA por hash (âncora de action-dedup, AOS-081); o Input
jamais é gravado no span». Vinte e sete linhas abaixo, no `defer` da mesma `Mediate` (aberta em
`:220`), a mensagem crua da tool entra no span. Uma tool que ecoe o argumento na sua mensagem de
erro — o caso comum, não o exótico: `open /etc/shadow: permission denied`, `invalid token sk-…`,
`connect tcp 10.0.0.5:5432: connection refused` — reintroduz por `error.type` exactamente o que o
hash de `:226` foi lá posto para remover. O ficheiro defende a fronteira linha a linha e abre-a
num sítio.

**A correcção já existe escrita, um pacote ao lado.** `packages/kernel/agent-runtime/worker/worker.go:433-450`
define `spanErrorType(err error) string`, que mapeia o erro para um código estável e limitado —
`""`, `lease_lost`, `policy_denied`, `context_canceled`, `deadline_exceeded` e o catch-all
`step_error`. O doc dela (`:428-432`) nomeia este defeito com todas as letras: «em vez do
`err.Error()` cru — o único campo do span do worker que, de outra forma, derivaria de uma string
ARBITRÁRIA de uma tool a jusante (que poderia ecoar fragmentos de input/credenciais)». Tem quatro
call-sites, todos no worker (`:367`, `:375`, `:400`, `:422`), e zero no Reference Monitor.

**Correcção de caminho.** O relatório escreve `worker.go:433-450`; o ficheiro é
`packages/kernel/agent-runtime/worker/worker.go` — subpacote `worker`, não a raiz de
`agent-runtime`. O intervalo de linhas está exacto.

**As duas severidades avaliam-se em separado, e não se confundem.**

- **Cardinalidade: baixa.** Cardinalidade alta é o desenho declarado deste eixo — a `EPIC-08:415`
  recusa explicitamente sampling «que perca cardinalidade útil». Uma chave `error.type` com muitos
  valores distintos é desagradável num backend de séries temporais; não é uma violação de contrato
  deste sistema.
- **Fuga de dados: média-alta.** É a única via pela qual conteúdo não controlado pelo núcleo
  atravessa a fronteira que o resto do ficheiro protege, e o destino é externo: o adaptador OTLP é
  assíncrono e fail-open (`cmd/aos/otlpexporter.go:11-19`) e faz POST do corpo para um colector,
  sem que uma falha alguma vez propague ao run. Nada no caminho inspecciona o valor.

**Porque sobreviveu.** `spanErrorType` nasceu no worker, para o span do worker, depois do span do
Reference Monitor já existir; a assimetria nunca teve gate porque nenhum gate compara dois sítios
que escrevem a mesma chave semconv. E o atributo só é escrito quando `dec.ToolErr != nil`
(`:252`), ou seja, apenas numa tool permitida que falha em runtime — o caminho que o smoke, que
nunca medeia uma tool call, nunca produz.

### Critérios de Aceitação

- [ ] `monitor.go:253` deixa de escrever `dec.ToolErr.Error()`:
      `grep -n 'AttrErrorType, dec.ToolErr.Error()' packages/kernel/reference-monitor/monitor.go`
      devolve zero
- [ ] O valor escrito em `error.type` pelo Reference Monitor pertence a um conjunto fechado e
      enumerável no código, no molde de `spanErrorType`
      (`packages/kernel/agent-runtime/worker/worker.go:433-450`); ou a função é reutilizada, ou a
      do RM enumera os seus casos e o comentário diz porque não é a mesma
- [ ] O detalhe completo do erro continua a chegar ao chamador e ao registo de auditoria — nenhum
      teste existente de propagação de erro de tool avermelha
- [ ] Teste com **controlo negativo**: uma tool registada cujo erro contenha um marcador único
      (por exemplo `sk-CANARIO-1234`) é mediada e falha; o span capturado por `RecordingExporter`
      tem `error.type` num dos códigos do conjunto fechado e **não contém** o marcador em atributo
      nenhum; **controlo negativo** — o mesmo teste contra a versão anterior da função (ou com a
      correcção revertida por mutação) tem de avermelhar, provando que a asserção não passa por
      vacuidade
- [ ] Um teste afirma que uma tool que tem êxito continua a não escrever `error.type` de todo (o
      ramo `:252` mantém-se), para que a correcção não transforme um output vazio legítimo num
      erro

### Estado

**IMPLEMENTADO** (2026-09-08). `monitor.go` deixa de escrever `dec.ToolErr.Error()` em `error.type`
e passa por um `spanErrorType(err)` PRÓPRIO do Reference Monitor, que colapsa para o conjunto fechado
`""`/`context_canceled`/`deadline_exceeded`/`tool_error`. **Não se reutiliza o `spanErrorType` do
worker** por `agent-runtime` já importar `reference-monitor` (um import de volta seria ciclo de
módulo) — a decisão fica no comentário. O guard `if dec.ToolErr != nil` mantém-se (uma tool com
êxito não escreve `error.type`), e o erro CRU continua a chegar ao chamador (`Decision.ToolErr`) e ao
tail do modelo — só o atributo de span, que sai do processo para o colector, deixa de o carregar.

Provado por `-race` em `reference-monitor` e `agent-runtime`; teste canário (`sk-CANARIO-1234` no erro
da tool) prova que `error.type` fica `tool_error` e que o marcador **não aparece em atributo nenhum de
span nenhum**, não-vácuo por mutação (reverter para o erro cru avermelha). O único teste existente que
afirmava a mensagem crua (`agent-runtime/loop_test.go`) foi actualizado para o código fechado, deixando
intacta a asserção adjacente do tail (canal diferente). **Revisão adversarial de segurança
independente**: SHIP-READY — traçou todos os caminhos de span (atributos, nome, status) e o vector do
model-tail e confirmou que **nenhum** outro caminho exportado carrega o erro cru ou o input (o status
do span não tem Description populada; o prompt do tail é capturado por hash). Smoke saltado com
justificação: a mudança não toca arranque/HTTP e o smoke, por desenho, nunca medeia uma tool call.
Nenhum critério deferido.

---

## AOS-371 — A instrumentação do PDP existe, está testada, e o nó nunca a liga

### Contexto

`packages/control-plane/pdp/pdp.go:130` define `WithTracer(t otelgenai.Tracer) Option`, e o doc
imediatamente acima (`:125-129`) descreve o que ela compra: «cada [PDP.Reload] abre um span
"aos.policy.reload" com as versões, o content_hash e o resultado (applied/rejected) — NUNCA a
chave nem qualquer segredo. Sem esta opção o PDP usa o [otelgenai.NoopTracer]».

Os call-sites de `pdp.WithTracer` na árvore inteira são três, todos no mesmo ficheiro de teste:
`packages/control-plane/pdp/aos088_test.go:307,441,481`. **Zero em produção.**

O composition-root que abre o PDP está lido por inteiro: `packages/cmd/aos/main.go:1324` monta
`opts := []pdp.Option{pdp.WithTrustAnchor(anchor)}`, `:1345` acrescenta
`pdp.WithAutonomyOracle(cabling.oracle())` quando `AOS_AUTONOMY_LEVELS` produz níveis, e `:1348`
chama `pdp.Open(dir, opts...)`. `WithTracer` nunca é acrescentada. O caminho sem bundle é ainda
mais directo: `pdp.NewUnloaded()` (`pdp.go:189`) devolve `&PDP{}` — tracer nil por construção — e
é o que `integration/secured.go:299` compõe.

**O que fica morto, e são duas coisas distintas.**

1. **O span da recarga de política.** `pdp.go:337` abre `aos.policy.reload` (constante
   `opPolicyReload` em `policydiff.go:23`) e anota versão antiga, autor e resultado
   applied/rejected em todos os caminhos. Antes disso, `:332-334` substitui o tracer nil por
   `otelgenai.NoopTracer{}` — não falha, não emite. A operação que troca as regras que governam
   **todas** as tool calls do nó é invisível no trace.
2. **O span do overlay de autonomia.** `pdp/autonomy.go:65` chama
   `autonomy.ExposeLevel(ctx, p.tracer, agent, domain, level)`, seguido de `AnnotateOversight` em
   `:66`. O doc de `applyAutonomy` (`autonomy.go:42`) descreve-o como «expõe o nível corrente num
   span aos.autonomy.level (AC4/DoD)» — é literalmente o critério «exposição do nível corrente na
   observabilidade», e o comentário de `:63-64` confirma que «ExposeLevel trata um tracer nil como
   Noop». O critério é dado por satisfeito por código que, no binário entregue, não emite nada.

A consequência prática é medível e foi medida. Com bundle carregado e `AOS_AUTONOMY_LEVELS`
definida, o veredicto `escalate` é alcançável e funciona ponta-a-ponta — `autonomia L1 x danger ->
confirm`, com o run a parar em `waiting_on_human`. O **veredicto** está no trace, pelo lado do
Reference Monitor (`monitor.go:244` `aos.decision`, `:248` `aos.decision.denied_by`); o **regime**
sob o qual foi tomado — que nível de autonomia vigorava para aquele par agente/domínio, e que
modo de oversight a composição nível × classe produziu — não está em lado nenhum.

**Porque sobreviveu.** A instrumentação foi entregue *com* testes que a exercitam, e é assim que
`aos088_test.go` a usa: um teste que injecta o tracer prova que a instrumentação funciona, e não
prova que alguém a injecta. A distância entre «testado» e «composto» é uma linha no
composition-root, e nenhum gate mede composição de opções — o `layer-lint` valida o grafo de
imports, e o import de `otelgenai` pelo `pdp` existe. Nada distingue uma opção ligada de uma
opção que só os testes ligam.

### Critérios de Aceitação

- [ ] O nó passa o tracer ao PDP:
      `grep -rn 'pdp.WithTracer' packages/ --include=*.go | grep -v _test` devolve pelo menos uma
      ocorrência, no composition-root que compõe as `opts` de `main.go:1324-1348`
- [ ] O tracer passado é o mesmo que o Runtime usa, para que os spans do PDP e os do Reference
      Monitor cheguem ao mesmo exportador — verificável porque um único `RecordingExporter`
      injectado no nó de teste recolhe spans dos dois
- [ ] Uma recarga de política num nó com bundle carregado emite um span `aos.policy.reload` com o
      atributo de resultado (`aos.policy.reload.result`) preenchido, capturado pelo exportador
- [ ] Uma decisão escalada pelo overlay de autonomia emite o span `aos.autonomy.level` com o nível
      e o modo de oversight anotados
- [ ] Teste com **controlo negativo**: nó levantado com bundle e `AOS_AUTONOMY_LEVELS`, uma tool
      call mediada que escale ⇒ os dois spans acima aparecem no exportador; **controlo negativo** —
      um nó composto com `pdp.NewUnloaded()` (tracer nil, sem bundle) tem de continuar a decidir
      deny fail-closed sem panic e sem emitir esses spans, provando que a passagem do tracer não
      criou dependência de arranque
- [ ] O teste avermelha se a linha que passa `WithTracer` for retirada por mutação

### Estado

**IMPLEMENTADO** (2026-09-08). O nó passa o tracer partilhado ao PDP. Como o composition-root abre o
PDP (`nodeConfigFromEnv → loadPolicyBundleFromEnv → pdp.Open`) **antes** de o tracer partilhado
existir (composto em `Bootstrap`), não se usa a `Option` `WithTracer` (que só `Open` aceita) mas um
novo `pdp.SetTracer` chamado em `bootstrap.go` logo após o tracer nascer, no MESMO ponteiro `cfg.PDP`
que a cadeia de decisão do RM usa (`secured.go`) — é o MESMO `otelgenai.Tracer` que o RM/Runtime
recebem, gated a `tracingEnabled` (o caminho NoopTracer fica byte-idêntico). A decisão `SetTracer`
vs `WithTracer` fica documentada no código. Endurecimento de concorrência: `applyAutonomy` passa a
ler `p.tracer` sob `RLock` (como `Reload`), fechando a janela entre a injecção e uma decisão
concorrente — sem deadlock (`Decide` liberta o RLock antes de chamar `applyAutonomy`).

Provado por `-race` em `control-plane/pdp` e `cmd/aos` (sem data race), e o smoke `run-aos`. Testes:
um nó REAL (Bootstrap) com um único `RecordingExporter` recolhe o span `aos.policy.reload` (via um
`Reload` no ponteiro injectado) **e** o `execute_tool`/`aos.decision` do RM — um só exportador recolhe
PDP e RM (AC2); e — depois de fechar a lacuna que a revisão adversarial apontou — uma **decisão
escalada** (L4×danger) no mesmo ponteiro emite `aos.autonomy.level` com nível e oversight no recorder
do nó, provando o AC4 **end-to-end via Bootstrap** e não só por transitividade. Controlo negativo: um
nó com `pdp.NewUnloaded()` (tracer nil) decide deny fail-closed sem panic e sem emitir esses spans.
Não-vácuo por mutação (retirar `cfg.PDP.SetTracer(tracer)` avermelha as asserções de span).
**Revisão adversarial independente**: SHIP-READY — mesmo tracer que o RM, mesmo ponteiro na cadeia de
decisão, sem deadlock, NoopTracer inalterado. Nenhum critério deferido.

---

## AOS-372 — `Reconstruct` não recusa um turno cuja captura falte, e o read-path soberano devolve 200 com uma trajectória curta e silenciosa

### Contexto

Há dois caminhos de reprodução no motor de replay e só um tem gate de admissão.

`packages/kernel/agent-runtime/replay/engine.go:375-388` — `admit(tr trajectory)` itera
`tr.turns` e recusa com `ErrIncompleteCapture` qualquer turno sem entrada em `tr.capture` (`:377-379`)
ou sem `PromptHash` no manifesto (`:380-382`). O doc (`:365-374`) é inequívoco sobre a intenção:
«fidelidade é condição, não opção […] recusa fail-closed em vez de produzir silenciosamente uma
reprodução de baixa fidelidade». `admit` tem **um** call-site: `engine.go:444`, dentro de
`ReplayEngine.Replay`.

`packages/kernel/agent-runtime/replay/sovereign_content.go:124` — `Reconstruct` — não passa por
`admit`. Constrói a ordem dos turnos **só** a partir dos eventos `EventTypeCaptured`
(`:168-170`: `if _, seen := caps[p.Turn]; !seen { order = append(order, p.Turn) }`), pelo que um
turno cuja captura não exista simplesmente não entra em `order` e não há nada a recusar. O laço de
saída (`:183-209`) itera `order`, e o único gate que lá está — `capturaCompleta(capt)` em `:193`,
posto por AOS-289 — verifica a **completude** de uma captura presente, não a sua **presença**.

**Medido (§3.5, O-07).** No pacote `replay`, com o `runOriginal` real (3 turnos) e o
`captureDroppingReader` do próprio repositório, suprimindo a captura do turno 2: `Replay`/`admit`
devolvem `ErrIncompleteCapture`; `Reconstruct` devolve **`err=nil`, `n=2`, turnos `[1 3]`**. No
`cmd/aos`, com `replayPlanFor` real sobre um Event Store que suprime o evento: `len(plan)=2`, e o
cliente de modelo devolve resposta nova ao vivo no turno 2. A corrupção e a truncatura (AOS-289)
são simétricas nos dois caminhos; a lacuna é exclusivamente o evento **inteiramente ausente**.

**A informação para recusar já está lida.** `Reconstruct` já consome `EventTypeTurnRecorded` em
`:140-145`, povoando `stepByTurn[trp.Turn]` com o step canónico de cada turno registado. A
correcção mínima é comparar os dois conjuntos: qualquer turno com entrada em `stepByTurn` e sem
entrada em `caps` é uma lacuna de captura e tem de recusar. **Precisão face ao relatório:** o
achado diz «basta recusar no laço qualquer turno sem entrada em `caps`», mas o laço de `:183`
itera `order`, que é construído a partir de `caps` — recusar aí é vacuoso. A recusa tem de ser
sobre os turnos conhecidos por `turn.recorded`, antes ou fora desse laço. É a mesma decisão que a
EPIC-21 já tomou («RECUSAR NOS DOIS CAMINHOS»), aplicada à presença e não só à completude.

**Ressalva medida, e baixa a severidade honestamente.** Existe uma barreira parcial no efeito que
a acusação original ignorava. A chave do step-ledger é puramente posicional —
`packages/kernel/agent-runtime/durable/step_id.go:70` declara
`func (s *StepSequencer) StepID(_ string, turn int) string`, com o runID descartado e sem hash do
input — e a verificação `already-applied` precede qualquer efeito (`durable/step_ledger.go:131`,
implementada em `:388`). Uma tool call *diferente* produzida pelo modelo ao vivo colide na mesma
chave e **não executa**. O pior cenário implícito — efeito não-aprovado executado — cai. **O dano
é de fidelidade e de prova, não de efeito:** trajectória fabricada (resposta nova colada a
resultado antigo) e possível terminação antecipada se a resposta ao vivo trouxer `Final=true`.

**Agravante, e é a metade sem mitigação nenhuma.** `Reconstruct` tem dois consumidores de
produção: a retoma (`packages/cmd/aos/resume.go:312`) e o read-path soberano
(`packages/cmd/aos/sovereign_replay.go:150`, rota `GET /runs/{id}/reconstruct`, registada em
`planos.go:202`). O segundo devolve `writeJSON(w, http.StatusOK, resp)` em `:164`, e a
`reconstructResponse` (`:61-64`) tem exactamente dois campos — `run_id` e `turns`. Não há campo
nenhum a dizer que nada foi verificado, nem que a trajectória pode estar incompleta. É a rota que
um auditor externo vai usar, e ela responde 200 com menos turnos do que o run teve, sem uma
palavra.

Do lado da retoma, o log de `resume.go:264` diz «%d turno(s) reproduzidos da captura» com
`len(plan)` (`plan` construído em `:321`). **Correcção ao relatório:** a passagem 1 afirmou que
este log «loga sucesso com o N errado»; é falso — `len(plan)=2` e são de facto dois os turnos
reproduzidos da captura. O defeito não é o número mentir, é o log nunca comparar com a contagem de
`turn.recorded` e por isso não denunciar a lacuna.

**Porque sobreviveu.** O repositório *sabe* da assimetria e escreveu-a: o comentário de
`replay/aos289_captura_truncada_test.go:23` diz «`admit()` só corre em [ReplayEngine.Replay]. A
RETOMA usa [ReplayEngine.Reconstruct]», e `sovereign_content.go:185-192` explica que o gate de
`:193` foi posto ali precisamente por causa disso. Fechou-se metade do problema — a captura
truncada — com a outra metade documentada ao lado e por fechar. Não há verificação de hash na
retoma, nem contagem contra o manifesto, nem uso do cursor durável, nem gate no selo de AOS-304.

### Critérios de Aceitação

- [ ] `Reconstruct` recusa com `ErrIncompleteCapture` (ou erro dedicado) um run em que exista
      `turn.recorded` para um turno sem `EventTypeCaptured` correspondente — a comparação é entre
      o conjunto povoado em `sovereign_content.go:140-145` e o conjunto `caps`
- [ ] Teste no pacote `replay` com **controlo negativo**: com o `runOriginal` de 3 turnos e o
      `captureDroppingReader` do próprio repositório a suprimir a captura do turno 2,
      `Reconstruct` devolve erro (hoje devolve `err=nil, n=2, turnos [1 3]`); **controlo
      negativo** — o mesmo `runOriginal` sem supressão devolve `err=nil` e os 3 turnos, provando
      que a recusa não é indiscriminada
- [ ] Teste que prove que um run **sem nenhum** `turn.recorded` (capturas antigas, sem o evento)
      continua a reconstruir como antes — a recusa é sobre a divergência entre os dois conjuntos,
      não sobre a ausência do sinal
- [ ] `GET /runs/{id}/reconstruct` deixa de devolver 200 sobre uma trajectória incompleta: teste
      HTTP que suprima o evento de captura de um turno e afirme o status de erro e o corpo
      uniforme (nunca conteúdo decifrado parcial)
- [ ] O log de `resume.go:264` passa a comparar os turnos reproduzidos com os turnos registados e
      a denunciar a divergência, ou deixa de existir por a retoma passar a recusar antes
- [ ] `grep -rn 'Reconstruct(' packages/ --include=*.go | grep -v _test` continua a devolver os
      mesmos dois consumidores de produção (`resume.go`, `sovereign_replay.go`) — a correcção não
      introduz um terceiro caminho sem gate

### Estado

**IMPLEMENTADO** (2026-09-08). `Reconstruct` passa a comparar o conjunto `stepByTurn` (os
`turn.recorded`) com `caps` (os `replay.captured`) e recusa fail-closed a divergência com
`ErrIncompleteCapture` — a informação já estava lida, faltava a comparação. O read-path soberano
`GET /runs/{id}/reconstruct` já retornava antes do `writeJSON(200)` em qualquer erro; acrescentou-se
o mapeamento `ErrIncompleteCapture → 422` (trajectória existe mas incompleta, distinta do
`ErrNoTrajectory → 404`), com corpo uniforme que nunca vaza conteúdo decifrado. `resume.go`
propagava já o erro e recusa.

**Desvio deliberado do AC, ratificado pelo dono (conflito de fontes destapado pela revisão
adversarial):** os AC assumiam recusar QUALQUER `turn.recorded` sem captura com um único
`Reconstruct` partilhado. A revisão provou que a emissão NÃO é atómica — `recordTurn` grava
`turn.recorded` ANTES da dispatch de tools (`loop.go`), `captureTurn` grava `replay.captured` DEPOIS
— pelo que um crash a meio do ÚLTIMO turno deixa um `turn.recorded` TRAILING sem captura, que é
LEGÍTIMO e é precisamente o que o **crash-resume** (`crash_resume.go` via `replayPlanFor`, um 3.º
caminho de facto que o ticket não considerou) existe para recuperar. Recusá-lo cegamente deixaria
runs crashados ÓRFÃOS em `running` para sempre — uma regressão pior do que o defeito, e viola «não
quebres o cluster». Solução ratificada: **separar os consumidores**. `Reconstruct` (STRICT) recusa
qualquer incompletude — mid-trajectory OU trailing — e serve o read-path (o read-path não filtra por
estado, logo um run crashado chega lá; 422 fecha o defeito por completo). Um novo
`ReconstructResumable` recusa só o buraco MID-trajectory (corrupção genuína, com captura depois) e
TOLERA o trailing, devolvendo o prefixo capturado para o crash-resume o reproduzir e correr o turno
interrompido ao vivo (already-applied deduplica os efeitos). `replayPlanFor` passa a usar o
`ReconstructResumable`. **AC6 reinterpretado:** o grep `engine.Reconstruct(` devolve agora só o
read-path; o crash-resume usa `engine.ReconstructResumable(`. Não há um terceiro caminho SEM gate —
os dois métodos têm gate, com posturas deliberadamente distintas —, que é o espírito do AC6.

Provado por `-race` em `replay` e `cmd/aos`, e o smoke `run-aos`. Testes: mid-trajectory recusado nos
dois modos (`dropTurn:2` de 3); trailing recusado no strict e tolerado no resumable (`dropTurn:3`, o
par que fixa a fronteira = maior turno capturado); read-path 422 sobre trajectória incompleta com
corpo que não vaza o conteúdo decifrado; captura legada sem `turn.recorded` continua a reconstruir
(no-op). Não-vácuo por mutação. Revisão adversarial de segurança independente que apanhou a regressão
de crash-resume — fechada por este re-desenho antes da integração. Nenhum critério deferido.

---

## AOS-373 — `aos audit-trail` abre o trilho forense em escrita, não pede a posse da partição e herda a truncatura do `OpenFileStore`

### Contexto

`packages/cmd/aos/audit_trail.go:58` — `store, err := audit.OpenFileStore(*path)`. É a única
abertura do subcomando, é sem opções, e o que ela faz não é ler.

`packages/platform/audit/filestore.go:64` — `OpenFileStore(path string, opts ...FileStoreOption)`
faz, por esta ordem: replay do WAL (`:65`); se `fi.Size() > validEnd`, **`os.Truncate(path, validEnd)`**
seguido de `fsyncDir` (`:69-73`); e depois `os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)`
(`:74`). Correr a ferramenta de leitura sobre prova forense pode, portanto, **encurtar
fisicamente o ficheiro** antes de imprimir uma única linha. Contra um nó vivo, o que é truncado é
o tail parcial — que é exactamente o registo em voo, a prova do que estava a acontecer no momento
que se está a investigar.

A posse entre processos também não é pedida. `packages/platform/audit/posse.go:70` define
`ComPosseDeParticao(p PosseDeParticao) FileStoreOption`, e o campo correspondente no `FileStore`
(`filestore.go:69-71`) diz para que serve: «arbitra a EXCLUSIVIDADE de escrita por partição entre
PROCESSOS — o que o mutex acima não faz. nil mantém o comportamento anterior». `ComPosseDeParticao`
é a **única** `FileStoreOption` do pacote, e `audit_trail.go:58` não passa nenhuma. O `_ = store.Close()`
de `:62` fecha um handle de escrita que nunca devia ter sido aberto.

**A guarda é convenção em prosa.** O doc do ficheiro di-lo com todas as letras e sem se esconder:
`audit_trail.go:7-9` — «READ-ONLY, sobre um WORM em disco, para correr num contentor EFÉMERO da
mesma imagem com o nó principal PARADO (sem escritor concorrente)» — e `:15-18` acrescenta «O
volume tem de estar montado GRAVÁVEL: [audit.OpenFileStore] abre o ficheiro WORM para append e um
mount `:ro` falha a abrir». Nada no código verifica o pressuposto: não há flag, não há lock, não
há detecção de escritor vivo. A frase «READ-ONLY» descreve a intenção do operador, não uma
propriedade do programa.

**E o precedente já existe, no ficheiro ao lado, e o próprio doc reconhece que ficou para trás.**
`audit_trail.go:16-18` regista que «A analogia com o `wal-count` DEIXOU DE VALER nesta linha —
desde AOS-347 esse subcomando usa [eventstore.OpenReadOnly] e tolera `:ro`. Aqui a exigência
permanece, e é do store de auditoria, não uma convenção herdada.» O gémeo do Event Store foi
fechado: `packages/substrate/eventstore/durable.go:753` define `OpenReadOnly`, e
`cmd/aos/wal_inspect.go:68` e `cmd/aos/wal_summary.go:77` usam-na — com o comentário de
`wal_inspect.go:37` a explicar que «não anexa o WAL para append nem repõe a cauda parcial». O
mesmo trabalho, do lado do audit, não foi feito.

**Porque sobreviveu.** A assimetria está documentada, e documentá-la fê-la parecer decidida em vez
de pendente: quem lê `:16-18` vê uma diferença explicada, não uma dívida. O subcomando é de
diagnóstico e não corre em CI, pelo que nenhum gate o exercita; e o dano só se materializa contra
um trilho com tail parcial — isto é, contra um nó vivo, que é precisamente o caso que a convenção
proíbe e nada impede.

**Dependência.** A distinção entre cauda rasgada e dano interior no `OpenFileStore` — que é a
outra metade do problema, e a que apaga registos válidos em silêncio — é tratada em **AOS-364**
(bloco P0 desta epic). Este ticket depende dela: enquanto `OpenFileStore` truncar
indiscriminadamente, uma via de leitura que não truncasse continuaria a ser a excepção e não a
regra. Se AOS-364 introduzir uma abertura que distinga os dois casos, este ticket consome-a; se
não, este ticket tem de acrescentar a sua própria.

### Critérios de Aceitação

- [ ] Existe uma via de abertura só-de-leitura do store de auditoria, no molde de
      `eventstore.OpenReadOnly` (`packages/substrate/eventstore/durable.go:753`), que não abre o
      ficheiro para escrita e não chama `os.Truncate`
- [ ] `audit_trail.go` usa-a: `grep -n 'audit.OpenFileStore' packages/cmd/aos/audit_trail.go`
      devolve zero
- [ ] `aos audit-trail` corre com sucesso sobre um ficheiro WORM sem permissão de escrita e sobre
      um volume montado `:ro` — verificável por teste que retire a permissão de escrita ao
      ficheiro antes de invocar o subcomando
- [ ] Teste com **controlo negativo**: um ficheiro WORM com tail parcial (últimos bytes de um
      registo truncados) é lido por `aos audit-trail`; o tamanho do ficheiro antes e depois é
      **idêntico** e os registos íntegros anteriores são impressos; **controlo negativo** — o
      mesmo ficheiro aberto por `audit.OpenFileStore` continua a truncar (o comportamento do
      caminho de escrita não é alterado por este ticket), provando que o teste mede a via nova e
      não uma ausência de dano
- [ ] Se a via de leitura não puder garantir ausência de escritor concorrente, o subcomando pede a
      posse (`audit.ComPosseDeParticao`, `packages/platform/audit/posse.go:70`) ou recusa com erro
      atribuível — em nenhum caso prossegue em silêncio
- [ ] `audit_trail.go:7-18` é reescrito: o que hoje é convenção em prosa passa a descrever a
      propriedade que o código impõe, e a nota sobre a analogia com o `wal-count` deixa de
      declarar uma divergência que já não existe

### Estado

**IMPLEMENTADO** (2026-09-08). `OpenFileStore` foi refactorizado num corpo partilhado
`abrirFileStore(path, soLeitura, opts...)` (molde exacto do `eventstore.abrir`); `OpenFileStore` é o
caminho de escrita, e um novo `OpenFileStoreReadOnly` (`soLeitura=true`) abre para INSPECÇÃO. A
distinção cauda-rasgada/dano-interior e a **recusa fail-closed do dano (AOS-364) correm SEMPRE**,
mesmo em leitura — um WORM corrompido nunca se serve como íntegro; só a **truncatura** da cauda
parcial e a reabertura em `O_WRONLY|O_APPEND` ficam atrás de `if !soLeitura`. Em leitura os handles
`f`/`w` ficam nil e `Read`/`Head`/`At` servem de `parts` em memória; `Append` recusa com
`ErrAuditReadOnly` e `Close` tolera os nil. `audit_trail.go` passa a `OpenFileStoreReadOnly` e o
cabeçalho foi reescrito (deixa de exigir volume gravável / declarar que `:ro` falha). A ferramenta
forense já não pode encurtar a prova que foi ler.

**AC5 (posse):** a leitura não arma posse nem lock, no molde de `eventstore.OpenReadOnly` — um replay
read-only de um ficheiro append-only de escritor único lê um prefixo consistente e um registo em voo
é servido como o prefixo íntegro, não truncado. Um `OpenFileStoreReadOnly` **não escreve, trunca nem
fsync sob nenhum input** (verificado linha a linha pela revisão). Provado por `-race` em
`platform/audit` e `cmd/aos`, e o smoke `run-aos`: cauda rasgada lida em read-only ⇒ tamanho
IDÊNTICO antes/depois + prefixo íntegro servido (controlo negativo: o caminho de escrita continua a
truncar o mesmo ficheiro); dano interior ⇒ recusa fail-closed em leitura; `Append` ⇒
`ErrAuditReadOnly`; teste do subcomando `cmdAuditTrail` prova que a leitura não altera o tamanho.
Não-vácuo por mutação (desligar o gate ⇒ a leitura volta a truncar, avermelha).

**Revisão adversarial de segurança independente**: SHIP-READY no essencial (a leitura não pode
escrever/encurtar a prova). Apanhou um limite mal-declarado — sobre um nó VIVO, um registo GRANDE
(>~4 KB) em voo pode causar uma recusa `DanoInteriorError` **espúria** (fail-closed, erro atribuível,
nunca prova destruída, cumprindo o AC5) — **corrigido no comentário** para não sobre-afirmar a
segurança contra escritor concorrente. Fechar a recusa espúria exigiria alterar o replay partilhado
com a escrita, desproporcional a um caso raro e já fail-closed. Nenhum critério deferido.

## AOS-374 — Duas declarações de cobertura que não se sustentam: uma entrada do registo caduca e um gate que declara o que o script diz em voz alta não fazer

### Contexto

**(a) A entrada `DEF-405` afirma inalcançável um caminho que o binário serve há quase um mês.**
`docs/governance/REGISTO-Deferimentos.md:216` diz que a ratificação de promoção «é alcançável PELO
CAMINHO DO NÓ (`node.Promotion.Promote`, provada em teste), mas o binário não expõe I/O (endpoint
HTTP nem subcomando CLI) que a submeta — ao contrário do FourEyesGate (`POST /runs/{id}/approve`)».

`packages/cmd/aos/promotion_api.go:221` implementa `handlePromote` e `:242` chama
`h.node.Promotion.Promote` — o método exacto que a entrada declara sem I/O. A rota está registada em
`packages/cmd/aos/planos.go` como plano de controlo, e o **ficheiro-âncora da própria entrada**
(`packages/cmd/aos/bootstrap.go:2337`) diz, por palavras suas, «a submissao de ratificacoes DEIXOU de
ser deferida — ha rota externa».

A datação é verificável: a rota entrou a **2026-08-11** (`caa08ea`, EPIC-20 W6/AOS-275); a entrada foi
tocada pela última vez a **2026-07-27** (`77d4791`). Desde a rota, **32 commits** alteraram o registo
— um deles intitulado «seis declarações caducadas deixam de mentir» (`4b18364`). O relatório fala de
«≥12 commits»; a contagem de primeira mão é 32.

**Ressalva que mantém a correcção exacta.** O gatilho de saída da entrada nomeia **AOS-096** — «existir
pipeline de promoção/canary que submeta ratificações ao controller» — e esse pipeline continua a
montante e fora do nó, coisa que o próprio banner declara em `packages/cmd/aos/bootstrap.go:2340`. O
que caducou é a **caracterização** («o binário não expõe I/O»), não necessariamente o estado aberto da
entrada. Corrigir uma sem reavaliar a outra troca um erro por outro.

**Porque sobreviveu.** É a mesma razão que o AOS-344 nomeou para `DEF-701`: o gate `deferrals`
(`scripts/ci/deferrals.py`) verifica que cada marcador em código tem linha no registo — verifica que o
deferimento está **documentado**, nunca se a documentação continua **verdadeira**, nem se o gatilho de
saída que a própria entrada declara já ocorreu.

**(b) `tecnica/13:231` conta como automatizada uma verificação que o script declara retirada.**
A linha afirma «Três verificações complementares, **hoje automatizadas** no gate `event-catalog`», e a
terceira é «que um nome catalogado em (a) é mesmo apendado ao Event Store (o pacote importa
`substrate/eventstore`)».

`scripts/ci/event-catalog.py:41` abre a secção «O QUE ESTE GATE **NÃO** VERIFICA (declarado por
honestidade)» e `:49-51` diz dessa mesma verificação que «Foi implementada, medida contra a árvore, e
**retirada por imprecisão**: a granularidade disponível é o PACOTE, não o emissor». O script é honesto;
o documento é que sobredeclara — e fá-lo na frase que serve de **inventário de cobertura**, que é a
direcção perigosa. Há uma segunda instância em `tecnica/13:482` (§8.1), onde a mesma verificação
aparece dentro da descrição do que o AOS-198 entregou, com a entrada riscada como fechada.

**Ressalva obrigatória, e não é decorativa.** A verificação retirada **não** teria apanhado o canal de
mediação em falta do AOS-379. Operava à granularidade do **pacote** e o docstring regista que, na
direcção (b), «acusava `platform/audit`, um pacote grande que importa o store por outras razões que não
estes rótulos» — ou seja, teria **passado** sobre exactamente o pacote onde o buraco vive. O buraco do
AOS-379 é de **caminho de chamada** e continua sem gate que o cubra. Confundir as duas coisas fecha uma
narrativa que não fecha.

### Critérios de Aceitação

- [ ] A entrada `DEF-405` deixa de afirmar que o binário não expõe I/O de submissão:
      `grep -n "nao expoe I/O\|não expõe I/O" docs/governance/REGISTO-Deferimentos.md` deixa de casar na
      linha `DEF-405`, e `grep -n "POST /promote" docs/governance/REGISTO-Deferimentos.md` passa a casar
      nessa linha
- [ ] A entrada separa o que caducou do que continua verdade: a rota existe desde `caa08ea`
      (2026-08-11), o gatilho de saída continua a ser AOS-096, e o pipeline continua fora do nó como
      `packages/cmd/aos/bootstrap.go:2340` declara. O estado da entrada é reavaliado contra o gatilho,
      não contra a rota
- [ ] `tecnica/13:231` deixa de contar a terceira verificação como automatizada:
      `grep -n "é mesmo apendado ao Event Store" tecnica/13_Modelo_Dados_Eventos.md` ou deixa de casar,
      ou a linha que casa nomeia a retirada e remete para `scripts/ci/event-catalog.py:49-51`
- [ ] `tecnica/13:482` (§8.1) deixa de listar a separação das famílias (a)/(b) por importação como parte
      do que o AOS-198 entregou, ou declara-a explicitamente como não entregue
- [ ] A correcção escreve a ressalva por extenso — a verificação retirada era por-pacote, teria passado
      sobre `platform/audit`, e não cobre o caminho de chamada do AOS-379 — para que a próxima leitura
      não volte a ligar as duas
- [ ] Um teste ou verificação de coerência prova que a afirmação corrigida e o docstring do script dizem
      o mesmo, no molde do que `tecnica/14` §5.2 usa (nota de data e commit)

### Estado

**IMPLEMENTADO** (2026-09-08). Correcção puramente documental (não altera comportamento). **(a)**
`DEF-405` deixa de afirmar que o binário não expõe I/O de submissão: descreve a rota externa
`POST /promote` (`promotion_api.go` `handlePromote`, desde `caa08ea`/2026-08-11) e separa o que
caducou (a caracterização) do que continua verdade — o gatilho de saída AOS-096 (pipeline
promoção/canary a montante, fora do nó, como `bootstrap.go:2340` declara); o estado fica **ABERTO**,
reavaliado contra o gatilho e não contra a rota. **(b)** `tecnica/13:231` passa de «três verificações
automatizadas» a **duas**, nomeando a terceira (a separação (a)/(b) por importação de
`substrate/eventstore`) como implementada e **RETIRADA por imprecisão** (granularidade por-pacote),
com remissão para `scripts/ci/event-catalog.py:41-51`; `§8.1` (`:482`) deixa de a listar como
entregue por AOS-198. A **ressalva** fica escrita por extenso: a verificação retirada operava
por-pacote e teria **passado** sobre `platform/audit`, pelo que nunca teria apanhado o buraco de
caminho-de-chamada do AOS-379. **AC6:** nota de coerência com data (2026-09-08) e commits (base
`f366f08`, script `7d16c4e`) no molde de `tecnica/14 §5.2`, provando que o documento e o docstring do
gate declaram agora a mesma retirada.

Verificado por grep (AC1: `DEF-405` sem «não expõe I/O», com `POST /promote`; AC3: a linha nomeia a
retirada e remete para o script) e pelos gates `deferrals` e `event-catalog` (verdes localmente).
Nenhum critério deferido. Revisão adversarial dispensada com justificação: é reconciliação
documental verificável por grep + gates, sem código nem superfície de segurança.

---

## AOS-375 — Três reconciliações no corpus normativo: uma matriz que se contradiz a si própria, dois epics que declaram estados opostos do mesmo trabalho, e um tripwire descrito pelo output que já não produz

### Contexto

**(a) `tecnica/14` diz, em duas células, que o binário não carrega bundle — e o §4 da mesma matriz diz
o contrário.**

`tecnica/14_Matriz_Conformidade.md:161` (§5.2, o inventário de lacunas de cobertura do nó) declara o
carregamento de bundle **inerte**, com a justificação «`nodeConfigFromEnv` nunca o preenche e nenhuma
variável de ambiente carrega um bundle», e remata «O nó corre sempre `pdp.NewUnloaded()`».

As duas afirmações são falsas contra o binário: `packages/cmd/aos/main.go:746` chama
`loadPolicyBundleFromEnv()` de dentro de `nodeConfigFromEnv`, `:750` preenche `cfg.PDP`,
`packages/cmd/aos/main.go:1288` define a função, `packages/cmd/aos/main.go:98` fixa o fail-closed
(`ErrPolicyBundleNeedsTrustAnchor`), e `packages/cmd/aos/bootstrap.go:2378-2379` regista no banner «PDP
com BUNDLE CARREGADO (AOS_POLICY_BUNDLE_DIR)». O teste está na árvore:
`packages/cmd/aos/aos220_pdp_bundle_surface_test.go`.

E a **mesma matriz** já o sabe: `tecnica/14:111` (linha do Art. 5, §4) escreve que «o binário **EXPÕE**
a via de carregamento: `AOS_POLICY_BUNDLE_DIR` + `AOS_POLICY_TRUST_ANCHOR` (…AOS-220, entregue em
2026-07-31…)».

**Segunda instância, e a mais estranha.** `tecnica/14:113` (linha do Art. 25) afirma «o binário não
expõe via de carregamento de bundle — a perna *by design* está composta e sem accionamento (**falha
(c)**)». Nove linhas abaixo, `tecnica/14:122` (ressalva 3 da mesma secção) diz «a superfície de
configuração do binário **carrega** um bundle (`AOS_POLICY_BUNDLE_DIR` + `AOS_POLICY_TRUST_ANCHOR`,
AOS-220). **Cumpre o critério (c)**». A célula e a sua própria ressalva dizem o contrário uma da outra,
sobre o mesmo critério, na mesma página. *(O relatório escreveu «onze linhas abaixo»; são nove.)*

Direcção do erro: **subdeclaração** — a matriz declara menos capacidade do que existe. É menos grave
que o inverso, mas está na secção que o §7 da própria matriz vende como mitigação do risco «componente
confundido com nó».

**(b) A EPIC-17 declara aberto o que a EPIC-18 declara entregue, e está entregue.**

`specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md:9` mantém o documento em estatuto de proposta, e
`:130-134` deixa os **cinco** critérios de AOS-181 por marcar e `:145-148` deixa **três dos quatro** de
AOS-182. Os primeiros quatro critérios de AOS-181 estão satisfeitos pelo binário — aceitar
`AOS_POLICY_BUNDLE_DIR` + trust anchor, chamar `pdp.Open` com `WithTrustAnchor`, manter
`pdp.NewUnloaded()` quando não configurado (`packages/integration/secured.go:299`), e o teste de
aceitação — por via de AOS-220. E a EPIC-18 diz, em
`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md:1227-1228`, «O deferimento AOS-182 (a ressalva
original desta linha) foi entregue: a residência do run é **selada na criação**», o que o binário
confirma em `packages/cmd/aos/api.go:599` → `packages/cmd/aos/sovereignty.go:351` (`sealResidency`).

Isto não é «caixa por marcar = trabalho por fazer» — calibrador que a própria Carta §5 desmonta com os
seis critérios do seu DoD por marcar. É mais estreito e mais verificável: **dois documentos do corpus
afirmam estados opostos do mesmo ticket**, e o leitor não tem como saber qual vale.

**(c) O contador do tripwire da Carta §6.6 já não produz o output que três secções do ficheiro
descrevem — e a definição que o mudou nunca entrou na Carta.**

O contador foi corrido a partir da raiz do repositório contra
`docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md`. Output actual: **11 eventos,
`reaberturas=1`, `recusas=0`, zero pendentes**; nenhuma janela de 30 dias com ≥2 na leitura estrita nem
na ampla; **exit 0**, «tripwire: NAO disparado em nenhuma das duas leituras». A causa é o N-011
(`:283-289`): as três linhas que estavam pendentes — REG-005, REG-006, REG-010 — passaram a `NAO`.

Três secções do mesmo ficheiro continuam a descrever o estado anterior:

- **§5.4** (`:440-468`) reproduz o output antigo (`indecidiveis-1a-perna=3`, «FIXAs tocadas=4 … TERIA
  DISPARADO pela 1.ª perna», `AVISO: 4 invocacao(oes) … por arbitrar», `echo $?` = `3`) e a leitura
  que dele decorre;
- **§7** (`:540-564`) fecha com «e hoje já sai com código `3`»;
- **§8** pontos 2 e 4 (`:578-581`, `:586-591`) dizem que o árbitro «não está constituído», que as
  emendas 1.2 e 1.3 estão «aprovadas com Segurança/Arquitectura **pendente**» — quando
  `specs/00_AOS_Carta.md:170` já diz «assinado (2026-07-29 — arbitragem §6.5 … N-011)» — e que a
  definição de «reaberta» continua por fixar, «até lá o §5.1 reporta os dois números e sai com código
  `3`».

*(Residual da mesma família: `:600` cita «§7 (emendas 1.0 a 1.3)» e a Carta já vai na 1.4,
`specs/00_AOS_Carta.md:172`.)*

**E o defeito de forma, que é o que dá peso a isto.** A definição de «reaberta» foi fixada a
**2026-07-29**, no sentido estrito, dentro do N-011 — um documento de governação, não a Carta. O
`specs/00_AOS_Carta.md:177` diz «as decisões só mudam pelo §6 (emenda datada acima)», e a tabela do §7
(`:167-172`) não tem **nenhuma** linha de 2026-07-29: salta de 1.3 (2026-07-23) para 1.4 (2026-08-31).
O §8 do próprio registo (`:568`) abre com «Pendências para o dono (exigem emenda — §7 da Carta — não
trabalho de engenharia)» e o §8 ponto 1 (`:573-577`) avisa que, sem emenda, «o §2.2 deste ficheiro é
proposta, não obrigação». A definição que decide se o tripwire dispara foi fixada exactamente pela via
que o documento diz não bastar.

### Critérios de Aceitação

- [ ] `tecnica/14:161` (§5.2) deixa de afirmar «nenhuma variável de ambiente carrega um bundle» e «O nó
      corre sempre `pdp.NewUnloaded()`»: `grep -n "nenhuma variável de ambiente carrega um bundle"
      tecnica/14_Matriz_Conformidade.md` e `grep -n "corre sempre .pdp.NewUnloaded"
      tecnica/14_Matriz_Conformidade.md` deixam ambos de casar
- [ ] `tecnica/14:113` (Art. 25) deixa de afirmar «o binário não expõe via de carregamento de bundle» e
      deixa de marcar «falha (c)», passando a dizer o mesmo que a ressalva de `:122` e que a linha do
      Art. 5 em `:111`. Verificação: `grep -n "falha (c)" tecnica/14_Matriz_Conformidade.md` deixa de
      casar, ou casa só onde for verdade
- [ ] As correcções levam a nota de data e commit que a própria `tecnica/14` §5.2 usa como método,
      nomeando `packages/cmd/aos/main.go:746` e `packages/cmd/aos/bootstrap.go:2378-2379` como
      evidência
- [ ] `specs/EPIC-17_…_v3.md:127-148` deixa de declarar aberto o que está entregue: os critérios de
      AOS-181 e AOS-182 satisfeitos ficam marcados com o ticket e o commit que os satisfez (AOS-220 para
      a superfície de bundle; AOS-182/DEF-202 para a residência por-run), **ou** a secção ganha uma nota
      datada de reconciliação que remete para
      `specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md:1227-1228`
- [ ] Nenhum critério é marcado sem âncora verificável — a alínea (a) do AOS-361 é o precedente de que
      um `[x]` sem ficheiro tocado é a mesma classe de defeito que este ticket corrige
- [ ] `docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md` §5.4, §7 e §8 (pontos 2 e 4) passam
      a descrever o estado que o comando do §5.1 produz hoje. Verificação por comando: correr o bloco do
      §5.1 a partir da raiz devolve `eventos=11  reaberturas=1  recusas=0  indecidiveis-1a-perna=0`,
      «tripwire: NAO disparado em nenhuma das duas leituras» e exit `0`; `grep -n "TERIA DISPARADO\|sai
      com código \`3\`\|pendente\*\*" ` sobre as três secções deixa de casar com prosa que descreva o
      estado como corrente
- [ ] O output antigo, se for preservado, fica explicitamente datado como histórico (2026-07-26) e não
      como estado corrente
- [ ] `:600` deixa de citar «emendas 1.0 a 1.3» com a Carta na 1.4
- [ ] A definição de «reaberta» ganha a linha datada no §7 da Carta que `specs/00_AOS_Carta.md:177`
      exige, **ou** o registo declara em voz alta que a definição vigora sem emenda e que o §8 ponto 1
      do próprio ficheiro se aplica a ela. Verificação: `grep -n "2026-07-29" specs/00_AOS_Carta.md`
      devolve uma linha na tabela do §7, ou o registo tem uma linha nova a declarar a ausência
- [ ] O contador continua a poder dar mau resultado: os cinco cenários da prova negativa de
      `:470-487` são re-corridos sobre cópias fora da árvore e os códigos de saída mantêm-se (`1`, `1`,
      `1`, `2`, `2`)

### Estado

**IMPLEMENTADO** (2026-09-08). Reconciliação documental (nenhum `.go` alterado; a Carta **não** é
tocada). **(a)** `tecnica/14` §5.2 (linha «Carregamento de bundle») e a célula do Art. 25 deixam de
negar a via de carregamento e de marcar «falha (c)» — passam a dizer o mesmo que o Art. 5 (`:111`) e
a ressalva de `:122` (o binário EXPÕE `AOS_POLICY_BUNDLE_DIR`+`AOS_POLICY_TRUST_ANCHOR`, AOS-220; só a
*emissão* de obligations sem bundle é inerte), com nota de re-medição datada (2026-09-08, commit da
árvore, AOS-375) que nomeia `main.go`/`bootstrap.go` como evidência. **(b)** `EPIC-17_v3` ganha duas
notas de reconciliação datadas (AOS-181 satisfeito por AOS-220; AOS-182 pela residência selada na
criação, `api.go:599`→`sovereignty.go:351`), remetendo para `EPIC-18_v4:1227-1228` — sem marcar caixas
sem âncora (precedente AOS-361(a)). **(c)** `REGISTO-Decisoes-Reabertas` §5.4/§7/§8 passam a descrever
o estado corrente (contador: `eventos=11, reaberturas=1, recusas=0, indecidiveis=0`, tripwire **não**
disparado, exit `0` — o N-011 pôs REG-005/006/010 a `NAO`); o output antigo fica sob rótulo
«HISTÓRICO 2026-07-26 — caducou em 2026-07-29»; `:600` corrigido para «emendas 1.0 a 1.4».

**Decisão do dono (alínea c, metade não-documental):** a definição de «reaberta» foi fixada a
2026-07-29 dentro do N-011, e a Carta §177 exige que mudanças passem por emenda §7 datada — que não
existe. **Não se emenda a Carta** (é do dono). Em vez disso, o registo ganha uma linha (§8) que
**declara em voz alta** que a definição vigora sem essa emenda e que o §8 ponto 1 do próprio ficheiro
(«sem emenda, é proposta, não obrigação») se lhe aplica — a via que o próprio AC autoriza. O débito de
forma fica registado, não escondido nem usurpado.

Verificado por grep (AC1/AC2/AC8 = 0), pela contagem directa do contador (11 eventos, 1 reabertura, 0
recusas) e pelos gates `estado-citado`, `ref-lint` e `deferrals` (verdes localmente). Os cinco
cenários da prova negativa mantêm os exit `1,1,1,2,2`. Nenhum critério deferido.

---

## AOS-376 — O caminho de mediação não é exercitado por nenhum teste de sistema, e a única barreira de taint que resta é uma cláusula opcional dentro do texto da política

### Contexto

**O smoke ponta-a-ponta nunca medeia uma tool call.** `.claude/skills/run-aos/driver.sh:325-395`
percorre nove passos asseridos — submeter, observar, negar sem credencial, pause/steer assinado,
recusar emissor não pinado, autonomia assinada, SSE, WAL/WORM, métricas. Nenhum deles passa pelo ponto
de mediação. O passo 1 (`:337`) submete o objectivo «auditar o pipeline», e o modelo de referência
conclui em **um turno, sem tools**: `AOS_MODEL_TOOLS` não aparece uma única vez no driver. O passo 8
(`:386`) inspecciona o WORM na partição `governance.control` e assere um selo de `control:pause` — que
é governação HTTP, não mediação.

A medição confirma-o: 30 corridas inspeccionadas, **302 registos no WORM, todos `allow`**, e os únicos
`tool_id` são `gov.control`, `gov.read`, `gov.residency`, `gov.sovereignty`. Partições `smoke-*` — o
nome que uma mediação do Reference Monitor produziria, porque a partição é o RunID
(`packages/platform/audit/rmadapter.go:119-125`, `defaultPartition`): **zero**.
`tool.call.mediated`/`denied`/`escalated` no Event Store: **zero**.

E a distinção que salva o nó: **é do smoke, não do nó**. Com o catálogo de tools povoado, a mediação
regista — foram obtidas três decisões reais e seladas em partição não-`gov.*`. Todo o rigor
anti-binário-obsoleto do driver (`:329-334`, o pré-voo que recusa dar verde sobre binários velhos)
protege um teste que não toca no ponto que o sistema existe para proteger.

O catálogo necessário já está na árvore: `deploy/server/model-tools/tools.json` declara `doc_read`
(`cap:fs.read`) e `web_post` (`cap:http.post`) — exactamente as duas capabilities que a política
committada cobre (`packages/control-plane/pdp/policies/aos_authz.cedar`).

**A segunda metade: enquanto não existir superfície para povoar `privileged`, falta um gate sobre o
texto da política.** Com o TaintGate inerte — `packages/integration/secured.go:302-303` cai em
`NewStaticPrivilegedSet()`, cujo comentário diz «classificador real (vazio)», e
`packages/kernel/reference-monitor/production.go:226-234` (`NewProductionHardenedTaint`, o construtor
que recusaria com `ErrTaintGateInert`) tem **zero chamadores** — a única aplicação de taint que resta é
uma cláusula **opcional** dentro do texto da política. Uma regra que a esqueça fica sem rede, e a rede
está inerte sem que nada o diga.

`allow_fs_read` é exactamente essa regra.
`packages/control-plane/pdp/policies/aos_authz.cedar:41-49` traz apenas
`principal.authority.contains("cap:fs.read")`; a regra vizinha `allow_http_post` (`:24-34`) traz
`context.taint != "untrusted"`. Medido ao vivo: `cap:http.post` com `taint=untrusted` sai
`denied_by=policy`; `cap:fs.read` com `taint=untrusted` sai `denied_by=dispatch` — **todos os hooks
permitiram**, PDP e TaintGate incluídos. Medido depois (`analises/13` §2.4): esse `deny` vinha do
**registo da tool**, não do executor; com bloco `sandbox` no manifesto o Reference Monitor permite, e
com executor provisionado a call **chega lá**.

Nenhum gate vê isto. `scripts/ci/policy-test.sh` exige oito testes por nome (`:27-29`) e nenhum
inspecciona o **texto** da política.

**Porque sobreviveu.** A defesa estrutural, que por desenho vale para todas as regras, foi substituída
em silêncio por uma disciplina de escrita de política que ninguém verifica — e a substituição não tem
predicado observável: `Monitor.HasActiveTaintGate()` existe, está testado e tem zero chamadores;
`packages/cmd/aos/posture_banner.go` tem treze funções de postura e nenhuma para o Reference Monitor.

**Depende de AOS-363** (superfície de configuração que permita povoar `privileged`). O gate desta
alínea é a mitigação proporcional **enquanto AOS-363 não existir**, não um substituto dela.

### Critérios de Aceitação

- [ ] `driver.sh smoke` ganha um passo que medeia uma tool call real: com `AOS_MODEL_TOOLS` apontado a
      um catálogo (o `deploy/server/model-tools/tools.json` já serve) e um bundle carregado, o WORM
      passa a ter pelo menos um registo em partição **não-`gov.*`**. Verificação:
      `bash .claude/skills/run-aos/driver.sh worm <run-id>` devolve pelo menos uma entrada cuja partição
      é o RunID
- [ ] O passo assere as **duas** direcções, não só o verde: um `permit` selado e um `deny` por política
      com `denied_by=policy` — a regra `allow_http_post` com `taint=untrusted` produz-o hoje
- [ ] O smoke **recusa** dar verde se zero mediações forem seladas, no molde da função `tem` de
      `.claude/skills/run-aos/driver.sh:323` (captura para variável, nunca `cmd | grep -q`)
- [ ] Contraprova de que o passo não é vacuoso: com `AOS_MODEL_TOOLS` por definir, o novo passo
      avermelha em vez de saltar em silêncio — é a lição que `AOS-358` já fixou para o `dormencia`
- [ ] Existe um gate novo que verifica que **toda** a regra `permit` de um bundle assinado traz a
      cláusula `context.taint != "untrusted"`, ou é nomeada numa baseline com dono por entrada que só
      encolhe (o molde de `scripts/ci/event-catalog.py`)
- [ ] O gate está ligado aos três sítios que o tornam bloqueante — `ALL_GATES` de `scripts/ci/run.sh`,
      job em `.github/workflows/ci.yml`, e `needs:` do agregador `gates` — e é fail-closed sobre um
      bundle que não consiga parsear
- [ ] Teste-veneno do gate, nas duas direcções: acrescentar um `permit` sem a cláusula avermelha;
      retirar a cláusula de `allow_http_post` (`aos_authz.cedar:33`) avermelha. Controlo negativo: com o
      gate desligado, ambas as mutações passam
- [ ] `allow_fs_read` (`packages/control-plane/pdp/policies/aos_authz.cedar:41-49`) fica em conformidade
      com o gate — com a cláusula, ou nomeada na baseline com dono e razão
- [ ] O gate declara o próprio limite, no molde de «compilar não é correr» do `dormencia`: é mitigação
      enquanto AOS-363 não existir, e a barreira estrutural continua a ser o TaintGate com conjunto
      `privileged` não-vazio — `packages/kernel/reference-monitor/production.go:226-234` continua sem
      chamadores até lá, e o gate não pode ser lido como tendo fechado esse eixo

### Estado

**IMPLEMENTADO** (2026-09-08). P2. Alcance: arnês e CI; nenhum `.go` de produção alterado e a política
assinada **não** é tocada (re-assiná-la rotaria o trust anchor, cuja chave privada vive fora do repo).

**Metade (a) — o smoke passa a exercitar a mediação (decisão do dono: «passo que corre o teste de
sistema»).** O binário `aos` não consegue emitir uma tool call ao vivo: o `referenceModel` de produção
nunca emite e o único modelo que emite é o test-only `toolEmittingModel`, sem gateway mock que o injecte
por HTTP. Logo o caminho de mediação **alcançável a partir do smoke** é o system-test do nó completo. O
`driver.sh` ganha o passo `9/10` (`.claude/skills/run-aos/driver.sh:376-395`) que corre
`go test -run '^TestAOS169_Mediation_NoBypass_FullNodeAPI$'` (`packages/cmd/aos/acceptance_mediation_test.go:293`,
compõe um nó via Bootstrap com o RM real + bundle assinado e prova **permit + deny + no-bypass**). **AC2**
(duas direcções) e **AC3** (recusa dar verde se nada for mediado) ficam por construção do teste subjacente:
a call atravessa o Reference Monitor e o teste falha se a mediação não selar. **AC3/AC4** (fail-closed,
não-vacuoso): o passo captura para `$r` no molde da função `tem` (`:323`, nunca `cmd | grep -q`), chama
`fail` se o `go test` falhar, e — porque `go test -run <nome-inexistente>` sai `0` — exige a linha
`--- PASS: TestAOS169_Mediation_NoBypass_FullNodeAPI`, pelo que remover/renomear o teste avermelha o passo
em vez de o saltar em silêncio (a lição de AOS-358). Métricas renumeradas para `10/10`.

**Metade (b) — gate novo `policy-taint` sobre o texto da política (AC5-AC9).** `scripts/ci/policy-taint.py`
+ `.sh` exigem que **todo** o `permit` de `aos_authz.cedar` carregue `context.taint != "untrusted"` como
**conjunct AND de topo do bloco `when {}`**, ou seja nomeado numa baseline com dono por entrada que só
encolhe (o molde de `event-catalog.py`). **AC8:** `allow_fs_read` fica conforme via
`scripts/ci/baseline/policy-taint.txt` (`owner=AOS-363`, com a razão — re-assinar rotaria o anchor). **AC6:**
ligado aos três sítios — `ALL_GATES` (`scripts/ci/run.sh`), job em `.github/workflows/ci.yml` (+ comentário
REQUIRED-CHECKS e `needs:` do agregador `gates`), e `CONTRIBUTING.md` — e é fail-closed sobre um bundle que
não parseie. **AC9:** o gate imprime o próprio limite (defesa-em-profundidade enquanto AOS-363 estiver
inerte-por-omissão; a barreira estrutural continua a ser o TaintGate com `privileged` não-vazio).

**Anti-recorrência (revisão adversarial).** A revisão independente devolveu «NOT SHIP-READY» com dois
defeitos de parser: **MUST-1** — um `permit` **anónimo** (sem `@id`) era invisível ao gate; **MUST-2** —
a verificação por substring dava verde à cláusula quando ela vivia em `unless {}` (inverte o sentido),
estava negada, ou disjunta com `||` (não é conjunct de topo). Ambos fechados: `parse_permits` itera todos
os `permit\s*\(` e trata o anónimo como violação; `_when_body`/`_top_level_and_conjuncts`/`permit_cumpre_taint`
exigem a cláusula como conjunct AND de topo do `when`, ignorando `unless`/negação/disjunção. Travados em CI
pelo self-test §X: **X1** (baseline vazia ⇒ vermelho), **X2** (cláusula retirada de `allow_http_post` numa
cópia ⇒ vermelho), **X3** (controlo positivo: árvore real ⇒ verde), **X4** (permit anónimo ⇒ vermelho),
**X5** (cláusula em `unless` ⇒ vermelho), **X6** (cláusula disjunta ⇒ vermelho) — as mutações vivem em
`.cedar`-fixtures temporárias; a árvore real não é tocada.

Verificado: `policy-taint.sh` contra a árvore real exit `0`; as três fixtures adversariais (anónimo/`unless`/
disjunção) exit `1`; `py_compile` limpo; `bash -n selftest.sh` OK. Nenhum critério deferido. AOS-363 continua
a ser a mitigação estrutural; este ticket não fecha o eixo de taint — torna-o falsificável.

---

## AOS-377 — Duas fraquezas na superfície de autonomia: o dual-control de L4/L5 é detectivo e não preventivo, e a simulação responde sobre uma política diferente da que vigora

### Contexto

**(a) `AOS_AUTONOMY_LEVELS` mais reinício aplica qualquer nível sem assinatura nenhuma.**

`packages/cmd/aos/autonomy_levels.go:370-372` fixa a regra: «ambiente DIFERENTE (ou par novo no
ficheiro) ⇒ alguém editou o deployment e reiniciou, que é um acto deliberado com a mesma autoridade que
provisiona; o ambiente GANHA, **em qualquer direcção**». O ramo que a aplica está em `:384-387`, e cai
directamente no `SetLevel`.

A justificação escrita (`:374-376`) é a alavanca de resposta a incidente, e o exemplo que dá é sempre
**descer**: «repor um nível baixo no ficheiro e reiniciar funciona». Mas a regra também deixa **subir**.

O contraste que fecha o argumento vive no mesmo binário. `packages/cmd/aos/autonomy_route.go:133-160`
impõe, para `POST /autonomy` com destino L4 ou L5, «DUAS PESSOAS PARA REMOVER A SUPERVISÃO (AOS-305)»:
uma segunda assinatura, de um emissor **distinto** e também detentor de `autonomy:set` (`:113-118`),
sobre o mesmo payload canónico, com nonce anti-replay. Pôr `agt:dom=L5` em `AOS_AUTONOMY_LEVELS` e
reiniciar chega ao mesmo estado com **zero** assinaturas.

**O que cai é a prevenção, não a detecção.** A mudança **é** selada no WORM como `config:node`, logo
fica rastreável, e o `posture_banner.go` já declara a armadilha vizinha (`:304-308`: sem
`AOS_POLICY_BUNDLE_DIR`, a variável é ignorada em silêncio, «ATÉ se estiver malformada»). Nenhum
documento declara a assimetria — e é isso que a torna um defeito em vez de uma decisão.

**(b) `POST /autonomy/simular` responde sobre uma política diferente da que vigora.**

`packages/cmd/aos/autonomy_simular.go:126` faz
`hipotese.LevelForAgentOrClass(rec.Principal.NHIID, "", dominio)` — classe do agente **vazia**, em
literal. O caminho de produção faz o contrário: `packages/control-plane/pdp/autonomy.go:58` chama
`casc.LevelForAgentOrClass(agent, in.Principal.AgentClass, domain)`. Em qualquer deployment com regras
`class:` no `LevelRegistry`, a simulação resolve por um caminho que o nó real não usa, e responde ao
operador sobre um sistema que não é este.

A razão é que o selo não transporta a classe: `packages/platform/audit/record.go:32-35` (`Principal`)
tem apenas `NHIID` e `DelegationChain`, e `:64-73` (`CallContext`) tem `Taint`, `Reversibility` e
`Sensitivity`. Não há campo de classe de agente em `AuditRecord`.

**Onde discordo do relatório.** O relatório diz que o selo «perde `agent_class` **e** `risk_class`». A
metade da `risk_class` está mitigada e a mitigação é deliberada: `autonomy_simular.go:124` chama
`reclassificar`, que (`:196-224`) reconstrói um `rm.Call` a partir dos **mesmos factos que o selo
guardou** e corre o classificador real, «em vez de reimplementar as regras aqui. Uma segunda
implementação divergiria da primeira, e a simulação passaria a prever um sistema que não é este». A
classe de risco é derivada correctamente; a classe **de agente** é que é inventada como vazia. O
defeito falsificável é o `""` de `:126`, e é sobre ele que os critérios incidem.

**Ressalva que limita o alcance da correcção.** Selar `agent_class` no WORM não é acrescentar um campo:
`packages/platform/audit/rmadapter.go:97-105` explica que `canonicalContent` «fixa a ORDEM e a LARGURA
de cada campo, e acrescentar um lá muda os bytes canónicos de TODOS os registos, incluindo os já
escritos — as cadeias existentes deixariam de verificar. Isso é uma migração de `SchemaVersion`». A
correcção barata é a rota deixar de mentir; a cara é a migração.

### Critérios de Aceitação

- [ ] Uma **subida** de nível por `AOS_AUTONOMY_LEVELS` para L4 ou L5 deixa de ser aplicada com
      cerimónia menor do que a de `POST /autonomy`: ou o arranque recusa fail-closed no molde das nove
      guardas `ErrProductionNeeds*` de `packages/cmd/aos/main.go`, ou a subida exige prova assinada
- [ ] A **descida** continua a funcionar sem cerimónia — é a alavanca de resposta a incidente que
      `packages/cmd/aos/autonomy_levels.go:374-376` nomeia, e o ticket não a pode fechar
- [ ] Teste com controlo negativo nas duas direcções: par selado a L1 no WORM mais
      `AOS_AUTONOMY_LEVELS=agt:dom=L5` ⇒ recusa (ou não-aplicação declarada no banner e no log); o mesmo
      par com `=L0` ⇒ aplicado e selado como `config:node`
- [ ] A detecção não regride: tudo o que for aplicado continua selado como `config:node`, e um teste
      prova-o
- [ ] Enquanto a assimetria existir num deployment, alguma superfície a declara — o log de
      `autonomy_levels.go:384-385` (`ambienteEditado`) nomeia a **direcção** da mudança, e não só o par
      e o valor
- [ ] `POST /autonomy/simular` deixa de passar classe vazia em silêncio:
      `grep -n 'LevelForAgentOrClass(rec.Principal.NHIID, "", dominio)'
      packages/cmd/aos/autonomy_simular.go` deixa de casar
- [ ] A resposta da rota declara, por campo próprio, se a classe do agente foi resolvida; quando não
      foi, um teste prova que a resposta o diz em vez de responder como se regras `class:` não
      existissem
- [ ] Teste de divergência: um `LevelRegistry` com uma regra `class:` que se aplique ao registo
      simulado faz a simulação e o caminho de `packages/control-plane/pdp/autonomy.go:58` divergirem
      hoje, e deixarem de divergir (ou passarem a declarar a limitação) depois
- [ ] Se a via escolhida for selar a classe no `AuditRecord`, o ticket carrega a migração de
      `SchemaVersion` que `packages/platform/audit/rmadapter.go:97-105` exige, com um teste que prove
      que uma cadeia escrita antes da migração continua a verificar

### Estado

**IMPLEMENTADO** (2026-09-08). P2. Nó. Decisões do dono: **(a)** exigir prova assinada no ficheiro
(não recusa fail-closed); **(b)** a rota deixa de mentir (via barata, sem migração de `SchemaVersion`);
e, sobre um achado da revisão adversarial, **recusar fail-closed um piso `>= L4`**.

**Metade (a) — subida a L4/L5 por `AOS_AUTONOMY_LEVELS` exige a mesma prova de dual-control que
`POST /autonomy`.** Nova env var `AOS_AUTONOMY_PROOFS` (JSON, `"agente:dominio=Ln" -> [provas]`, os
mesmos campos de wire que a rota sela). Na `provision` (`packages/cmd/aos/autonomy_levels.go:510-538`),
uma SUBIDA que atravesse o limiar — `autonomyDualControlRequired(s.level)` **E** `s.level > anteriorNivel`
(`anteriorNivel = registry.LevelFor`, o reidratado ou o piso para par novo) — exige duas provas de
emissores DISTINTOS, **reutilizando** `autonomyProofVerifies` (o mesmo verificador da rehidratação:
direito `autonomy:set`, pubkey de `AOS_OPERATORS`, assinatura ed25519 sobre
`CanonicalAutonomyPayload(agente, domínio, nível, "provisionamento por AOS_AUTONOMY_LEVELS")`). Sem prova
válida a subida é **RECUSADA AO NÍVEL** (não aborta o boot; molde de `rejeitados` — o par fica no nível
anterior e a recusa é declarada em `recusadosPorProva` + banner). O selo mantém actor **`config:node`**
(AC4), agora com as provas. **AC2/retro-compat:** descidas e destinos `< L4` passam sem assinatura; o
gate senta-se **depois** dos ramos de salto idempotente (`:490-507`), pelo que um deployment inalterado
com L5 já selado reinicia sem pedir prova (não quebra o cluster). **AC5:** a linha de `ambienteEditado`
nomeia a direcção (`direcaoDaMudanca`). Os DOIS banners contraditórios de `autonomy_setters.go` (:105
sem-setters e :120 com-setters) foram **reconciliados**: o ficheiro baixa qualquer nível e sobe até L3
livre; subir a L4/L5 exige `AOS_AUTONOMY_PROOFS`. O gate é armado no composition-root
(`bootstrap.go:1335`, `armarGateDeProva(cfg.Operators, autonomySetters)`), onde vive a raiz de confiança.

**Metade (b) — `POST /autonomy/simular` deixa de fingir que modela a classe do agente.** O literal
`LevelForAgentOrClass(rec.Principal.NHIID, "", dominio)` desapareceu (AC6): passa por
`classeNaoSeladaNoWORM` (constante nomeada). Cada efeito declara `classe_modelada:false` (AC7 — o selo
WORM não carrega a classe e não pode sem migração de `SchemaVersion`), e quando a config proposta tem
regras `class:` a resposta ganha um campo `limitacao` de topo que nomeia a lacuna (AC8 — sem ele, um
operador que propusesse `class:...=L4` veria «escalariam 0» e concluiria que a regra não muda nada,
quando o que houve foi não ter sido avaliada). `reclassificar` (classe de RISCO) fica intocado — essa
metade já é derivada correctamente. **Não** se tocou em `AuditRecord`/`SchemaVersion`.

**Achado da revisão adversarial (SHIP com um SHOULD-change), fechado no mesmo PR.** Fechar a subida
por-par sem fechar o **piso** (`AOS_AUTONOMY_DEFAULT`) deixava a escalada MAIS larga — o piso vale para
todos os pares sem registo, e como os `agent_id` são por-run é quase toda a frota — na cerimónia MAIS
baixa (uma env var em branco, sem prova nem selo). `parseAutonomyDefault`
(`packages/cmd/aos/autonomy_levels.go:660`) passa a **abortar** um piso `>= L4` com
`ErrAutonomyDefaultDanger`; L0..L3 e ausente continuam a passar. Zero configs do repositório usam
piso `>= L4`, logo sem quebra.

Verificado: `packages/cmd/aos` `go test -race` verde (incl. `TestAOS377*`: subida sem prova recusada /
com duas provas aplicada e selada `config:node` / descida livre / uma-só-prova insuficiente / prova sem
`autonomy:set` não conta / retro-compat de L5 já selado / controlo negativo com o gate desarmado /
`ErrAutonomyDefaultDanger`); `control-plane/governance/autonomy` e `pdp` verdes; `build`, `lint`,
`layer-lint` verdes; `env_surface`/`manifesto_env` cobrem `AOS_AUTONOMY_PROOFS`. Smoke `run-aos` verde
nos 10 passos — o passo 6 confirma que o `POST /autonomy` L4→L5 assinado continua a aplicar-se, e o
passo 9 a mediação. Nenhum critério deferido. A migração de `SchemaVersion` (selar `agent_class`) fica
por fazer e **fora** de âmbito, por decisão do dono.

---

## AOS-378 — Três fraquezas do eixo da política: a guarda que o AOS-355 criou não foi replicada no slot `policy`, o assert de cobertura de deny conta a camada errada, e assinar um bundle não avalia nada

### Contexto

**(a) `NewProductionSecure` não tem guarda nenhuma no slot `policy`.**

`packages/kernel/reference-monitor/production.go:193-214` tem quatro guardas: `ErrIdentityStub`
(`:198`), `ErrEgressStub` (`:201`), `ErrEgressHookMissing` (`:207`) e `ErrScopeGateMissing` (`:210`).
Nenhuma sobre `policy`. Não existe predicado `ePolicyStub` nem `hasActivePolicyHook` no pacote —
`eEgressStub` (`:320`) e `eIdentityStub` (`:329`) existem, e o slot de política não tem o par.

E a guarda que falta foi criada há três semanas para o slot ao lado. `:204-206` declara-o por palavras
suas: «PRESENÇA, não só ausência-do-stub (AOS-355). A guarda acima só via a mutação por SUBSTITUIÇÃO;
esta vê a OMISSÃO».

**O que não é.** O par neutro de política não chega hoje à produção:
`packages/integration/secured.go:381` fixa `pdp.NewPolicyCheck(policyDP)` na cadeia. O defeito é a
**assimetria da guarda**, não um caminho aberto — é latente, e torna-se alcançável com uma mudança de
composição plausível, exactamente a família do AOS-362.

**(b) O assert de cobertura de deny por-regra aceita uma substring que as duas camadas produzem.**

`packages/control-plane/pdp/policy_suite_test.go:141` faz
`if !contains(d.Reason, "default-deny")`. Essa substring aparece em **duas** camadas distintas:
`packages/control-plane/pdp/capabilities.go:160` («capability %q nao consta da allowlist da classe %q
(default-deny)» — o gate da allowlist, que corre **antes** do Cedar) e
`packages/control-plane/pdp/engine_cedar.go:158` («capability %s negada por default-deny (sem permit
aplicavel)» — o Cedar). Um caso de deny que nunca chega ao motor Cedar conta como cobertura de uma
regra Cedar.

A assimetria com o caso allow é o que prova a intenção: `:136` faz `contains(d.Reason, c.ruleID)` — «o
permit da política nomeia a regra pelo seu `@id`, logo o caso allow exercita mesmo ESTA regra (não
outra qualquer)». O deny não tem esse cross-check.

E está num gate **bloqueante**: `scripts/ci/policy-test.sh:28-29` exige `TestPolicyRuleCoverage` por
nome, sob `require_tests` (que é fail-closed contra o passe vacuoso de um `-run` que não casa nada).

**(c) `SignBundle` só compila, e o `policy-sign` imprime «verificacao OK» sem ter avaliado nada.**

`packages/control-plane/pdp/bundle.go:190-218`: `SignBundle` lê os ficheiros (`:192`), chama
`compilePolicies` (`:196`) e aborta se a política não compilar — e depois assina. Nunca avalia.
`packages/control-plane/pdp/cmd/policy-sign/main.go:69-83` (`sign`) chama `SignBundle` e a seguir
`pdp.Open(dir)` (`:78`), que verifica assinatura e volta a compilar; nenhum dos dois chama `Decide`.
`:62` imprime então «verificacao OK: PDP carregado com policy_version=%s».

O que isso deixa passar: o mapeamento de atributos do motor é **fixo em código** —
`packages/control-plane/pdp/engine_cedar.go:121-144` monta `principal.authority`, `resource.region`,
`context.taint` e `context.sensitivity`, e mais nada. Uma regra assinada que refira qualquer outro
atributo compila, assina, imprime «verificacao OK», e em runtime cai em `diag.Errors` (`:147-149`),
devolvendo `ErrMalformedRequest` — que o Reference Monitor converte em deny. Um bundle validamente
assinado que nega tudo, com a ferramenta de assinatura a declarar-se verde.

### Critérios de Aceitação

- [ ] `NewProductionSecure` ganha guarda no slot `policy`, no molde exacto do AOS-355: ausência-do-stub
      (gémea de `ErrEgressStub`) e presença do hook (gémea de `ErrEgressHookMissing`), com sentinelas
      **distintos**. Verificação: `grep -n "ePolicyStub\|hasActivePolicyHook"
      packages/kernel/reference-monitor/production.go` passa a devolver linhas
- [ ] Teste-veneno com sentinelas distintos, como o AOS-355 provou para o egress: substituir
      `pdp.NewPolicyCheck` pelo hook neutro avermelha com um sentinela; retirar o hook do slot avermelha
      com o outro. Controlo negativo: a cadeia real de `packages/integration/secured.go:369-386`
      continua a compor sem erro
- [ ] O assert de deny de `packages/control-plane/pdp/policy_suite_test.go:141` deixa de aceitar uma
      substring produzida pelas duas camadas: o caso deny passa a provar que **alcançou** o Cedar, com o
      mesmo rigor que o caso allow já tem em `:136`. Verificação: `grep -n 'contains(d.Reason,
      "default-deny")' packages/control-plane/pdp/policy_suite_test.go` deixa de casar dentro do laço de
      `ruleCoverageTable`
- [ ] Os dois motivos deixam de ser indistinguíveis por substring: `capabilities.go:160` e
      `engine_cedar.go:158` passam a nomear a camada, ou o teste passa a discriminar por outro sinal
      (código, `denied_by`, ou o `@id` da regra)
- [ ] Teste-veneno do assert: um caso deny da tabela cuja negação venha da allowlist (classe sem a
      capability) deixa de contar como cobertura da regra Cedar e avermelha `TestPolicyRuleCoverage` —
      o teste que `scripts/ci/policy-test.sh:28-29` exige por nome num gate bloqueante
- [ ] `SignBundle` ou o `policy-sign` passam a **avaliar** antes de declarar verde: pelo menos uma
      decisão por regra `@id` do bundle, sobre um input mínimo, com o resultado a poder falhar.
      Verificação: `grep -n "Decide" packages/control-plane/pdp/bundle.go
      packages/control-plane/pdp/cmd/policy-sign/main.go` passa a devolver linhas
- [ ] `policy-sign` deixa de imprimir «verificacao OK» quando a avaliação não correu ou falhou:
      `grep -n "verificacao OK" packages/control-plane/pdp/cmd/policy-sign/main.go` continua a casar,
      mas num ramo que só é alcançado depois da avaliação
- [ ] Teste-veneno: um bundle cuja regra refira um atributo fora do mapeamento fixo de
      `packages/control-plane/pdp/engine_cedar.go:121-144` deixa de imprimir «verificacao OK» e sai com
      código diferente de zero. Controlo negativo:
      `packages/control-plane/pdp/policies/aos_authz.cedar` continua a assinar e a verificar sem
      alteração
- [ ] `scripts/ci/policy-test.sh` continua verde no fim, com os oito testes que já exige por nome

### Estado

**POR IMPLEMENTAR.** P2. Alcance: (a) latente — o par neutro não chega à produção porque
`secured.go:381` o fixa; (b) e (c) alcançáveis hoje. A (b) é a mais incómoda das três: está dentro de um
gate bloqueante, e um gate que conta a camada errada como cobertura mede a sua própria disciplina, não
a propriedade que diz medir.

---

## AOS-379 — O canal de eventos de mediação é inalcançável nos dois armazéns, e a justificação que fecha a discussão é falsa

### Contexto

O Reference Monitor define três tipos de evento de mediação em
`packages/kernel/reference-monitor/eventsink.go:14,16,18` — `tool.call.mediated`, `tool.call.denied`,
`tool.call.escalated` — e a porta que os grava, `EventSink` (`:52-58`). O adaptador para o Event Store
existe e chama-se `NewEventStoreSink` (`:144-146`).

**`referencemonitor.NewEventStoreSink` tem zero chamadores de produção.** Todas as ocorrências na árvore
são `_test.go`, README, ou o comentário de exemplo de `packages/platform/audit/teesink.go:25`. A única
composição real é `packages/integration/secured.go:390`, que monta
`audit.NewMediationSink(cfg.WORM)` — um sink só, para o WORM.

**E não há porta por onde ligar o segundo.** `SecuredConfig` (`packages/integration/secured.go:42`)
tem o campo `WORM audit.Store` (`:60`), declarado como «o `audit.Store` tamper-evident ÚNICO», e
**nenhum** campo para um Event Store de mediação. `audit.NewTeeSink`
(`packages/platform/audit/teesink.go:37`) — que faria o fan-out fail-closed para os dois — existe, está
testado em quatro cenários (`packages/platform/audit/teesink_test.go`), e tem **zero chamadores de
produção**. O canal é inalcançável nos dois armazéns: não está no Event Store porque nada compõe o
sink, e não está no WORM porque o `AuditRecord` não o carrega.

**A severidade vem da justificação, não do consumidor.** `packages/platform/audit/rmadapter.go:97-105`
explica, correctamente, por que o `rec.Metadata` do AOS-340 não é selado no WORM — a hash-chain fixa a
ordem e a largura dos campos em `canonicalContent`, e acrescentar um exigiria migração de
`SchemaVersion`. Esse argumento é sólido. O que não é sólido é o remate de `:103`: «O canal está no
Event Store (`tool.call.denied`), que é onde o AOS-332 lê.»

**Não está.** E o comentário existe explicitamente para evitar que alguém volte a investigar — «que é
precisamente o que este comentário existe para evitar que se re-investigue» (`:98-99`). Uma justificação
falsa que fecha a discussão é pior do que a lacuna que descreve: quem a lê pára de procurar.

Não há consumidor a jusante a partir-se com isto — os leitores de `tool.call.denied` são testes
(`packages/platform/broker/aos332_postura_selada_test.go:59`), não um componente composto. A gravidade é
inteiramente de veracidade e de rasto perdido: os metadados estruturados do hook que **terminou** a
mediação não existem em armazém nenhum.

**Nenhum gate cobre isto,** e o que parecia cobri-lo não cobria: a verificação retirada do
`event-catalog` (ver AOS-374) operava à granularidade do **pacote** e teria passado sobre
`platform/audit`. O buraco é de **caminho de chamada**.

### Critérios de Aceitação

- [ ] `SecuredConfig` ganha porta para um Event Store de mediação — um campo próprio, opcional e nil por
      omissão. Verificação: `grep -n "eventstore" packages/integration/secured.go` passa a devolver uma
      linha de campo em `SecuredConfig`
- [ ] Quando a porta é preenchida, `NewSecuredRuntime` compõe `audit.NewTeeSink` sobre
      `audit.NewMediationSink(cfg.WORM)` e `referencemonitor.NewEventStoreSink(...)`. Verificação:
      `grep -rn "NewEventStoreSink" packages --include=*.go | grep -v _test | grep referencemonitor`
      devolve hoje **zero** linhas e passa a devolver pelo menos uma
- [ ] A semântica fail-closed é preservada: um teste prova que a falha do sink do Event Store degrada o
      permit para deny, como `packages/platform/audit/teesink_test.go:66-68`
      (`TestTeeSinkFailClosed`) já exige do tee
- [ ] Controlo negativo: com a porta por preencher, o comportamento não muda — o `MediationSink` sobre o
      WORM continua a ser o único sink, e a suite de `packages/integration` continua verde
- [ ] `packages/cmd/aos` preenche a porta com o Event Store durável que o nó já compõe, **ou** declara
      no banner por que não a preenche — no molde honesto que o `posture_banner.go` já usa para as
      outras posturas
- [ ] Um teste conta os eventos `tool.call.mediated`/`tool.call.denied` no Event Store de um run com
      pelo menos uma tool call mediada: hoje devolve **zero**, e passa a devolver o número de mediações
- [ ] `packages/platform/audit/rmadapter.go:103` deixa de afirmar que o canal está no Event Store
      enquanto isso for falso no nó: ou a afirmação é retirada, ou passa a dizer que o canal existe como
      porta e **não está composto**, nomeando este ticket. Verificação: `grep -n "O canal está no Event
      Store" packages/platform/audit/rmadapter.go` deixa de casar, ou casa numa frase que declara a
      composição em falta
- [ ] O argumento correcto de `:99-102` — a migração de `SchemaVersion` que selar o campo exigiria — é
      **preservado**, porque continua a ser verdade e é a razão de o WORM não ser a via

### Estado

**POR IMPLEMENTAR.** P2. Alcance: nó, alcançável hoje. Nenhum consumidor composto se parte com isto — o
dano é de veracidade e de rasto: os metadados do hook que terminou a mediação não existem em armazém
nenhum, e o comentário que explica porquê remata com uma afirmação falsa, escrita precisamente para que
ninguém volte a verificar.

---

## AOS-380 — Materializar o ADR-014, ou aceitar por escrito que a escada L0–L5 não tem base normativa e que a conformidade contra ela não é mensurável

### Contexto

**Este ticket não é de engenharia.** Não tem critérios de implementação; tem critérios de **decisão
registada**. O que o fecha é uma emenda datada na Carta §7 ou um ADR emitido — nunca um commit de
código.

**O estado de facto, verificado.** A `specs/00_AOS_Carta.md` não contém a palavra «autonomia» — zero
ocorrências, medido. O ADR-014, que a `specs/EPIC-09_…md:21` invoca como autoridade («O epic concretiza
directamente ADR-011 … e **ADR-014** (taxonomia de autonomia L0–L5)») e que os tickets AOS-089 e
AOS-090 citam como documento de referência (`:218`, `:284`), **não existe como documento**:
`docs/adr/README.md:67` classifica-o «Catálogo, por materializar», e todo o seu conteúdo normativo é uma
linha de tabela em `specs/00_System_Spec.md:259` — «Oversight proporcional ao impacto; promoção por
fiabilidade medida; demoção automática».

O texto com semântica por nível vive em `tecnica/09_Governacao_Conformidade.md` §7 (`:143`), e as frases
que definem cada nível são **rótulos de nós de um bloco mermaid** (`:149-154`), com as transições em
`:160-162`. Os critérios de aceitação de AOS-089, AOS-090 e AOS-095 estão todos por marcar.

**Isto não é, por si, um defeito.** `docs/adr/README.md` declara «Catálogo, por materializar» como
estado legítimo do vocabulário, e a Carta §4.1 nem sequer lista o ADR-014 entre as decisões FIXAS. A
acusação de não-materialização caiu na refutação, e caiu bem.

**O que sobra, e é desconfortável.** Não se encontraram divergências L0–L5 entre implementação e spec —
mas não porque a implementação corresponda à spec. Não se encontraram porque **não há texto com
autoridade que diga a que deveria corresponder**. As seis hipóteses da forma «o nível N não faz o que a
spec diz» morreram todas como não-requisito, pela mesma razão. E dos dois achados classificados como
críticos neste eixo, ambos caíram por não existir texto que dissesse o que estaria certo.

**A consequência que obriga à decisão.** `tecnica/14_Matriz_Conformidade.md:86` cita, na linha do
**Art. 9 do AI Act** (gestão de risco), «Gates de risco SA-ROC (safe/gray/danger), circuit breaker
multi-sinal, **taxonomia L0–L5 com demoção automática em anomalia** (ADR-013/014)» como controlo, com
estado «Parcial». Duas metades dessa citação não se sustentam:

- **O controlador não está composto.** `DEF-908`
  (`docs/governance/REGISTO-Deferimentos.md:253`) regista que `autonomy.NewController` — «promoção só
  por fiabilidade sustentada e despromoção imediata em anomalia» — «tem ZERO chamadores em todo o
  repositório; o nó compõe apenas o `LevelRegistry`». Estado: aberto. A entrada nomeia o agravante:
  «um par promovido a L5 fica a L5 até alguém reparar».
- **A semântica não está fixada.** O ADR que a fixaria não existe, e o que existe são rótulos de um
  diagrama.

Citar como controlo regulatório um mecanismo cuja metade de segurança não está composta e cuja
semântica nunca foi ratificada é a única perna deste eixo que não é legítima. As duas saídas — emitir o
ADR, ou aceitar por escrito que a escada não tem base normativa — são **ambas** legítimas. Continuar a
citar sem escolher não é.

### Critérios de Decisão Registada

- [ ] O dono escolhe **uma** de duas vias, e a escolha fica datada por escrito. Nenhuma das duas é
      trabalho de engenharia
- [ ] **Via A — materializar.** `docs/adr/ADR-014-taxonomia-autonomia-l0-l5.md` existe, fiel à letra do
      enunciado do catálogo (`specs/00_System_Spec.md:259`), e `docs/adr/README.md:67` deixa de dizer
      «Catálogo, por materializar». Verificação: `grep -n "ADR-014" docs/adr/README.md` casa com uma
      linha que aponta para o ficheiro, e o ficheiro existe
- [ ] **Via A** — o ADR fixa a semântica por nível em texto normativo, não em rótulos de diagrama, e
      declara o que é exigível e o que não é. `tecnica/09` §7 passa a citar o ADR como fonte, em vez de
      ser a fonte
- [ ] **Via B — aceitar por escrito.** Uma emenda datada na tabela do §7 de `specs/00_AOS_Carta.md`
      regista que a escada L0–L5 não tem base normativa, que a conformidade contra ela não é mensurável,
      e que isso é aceite. Verificação: a tabela de `specs/00_AOS_Carta.md:165-172` ganha uma linha
      nova, com data e aprovação, no molde das emendas 1.1 a 1.4
- [ ] **Via B** — o registo declara a consequência aceite: as hipóteses da forma «o nível N não faz o
      que a spec diz» ficam formalmente não-requisito, e a EPIC-09 deixa de invocar o ADR-014 como
      autoridade (`specs/EPIC-09_…md:21`, `:218`, `:284`, `:255`, `:321`)
- [ ] **Em qualquer das vias**, `tecnica/14:86` deixa de citar «taxonomia L0–L5 com demoção automática
      em anomalia» como controlo do Art. 9 enquanto o `DEF-908` estiver aberto. Verificação:
      `grep -n "demoção automática em anomalia" tecnica/14_Matriz_Conformidade.md` ou deixa de casar, ou
      casa numa célula que nomeia `DEF-908` e declara a metade que não está composta
- [ ] **Em qualquer das vias**, a decisão nomeia `DEF-908` e diz se ele é pré-condição do controlo do
      Art. 9 ou se o controlo é declarado sem ele. Não é aceitável que a matriz continue a citar o
      mecanismo e o registo de deferimentos continue a declarar que a sua metade de segurança tem zero
      chamadores, sem que um dos dois documentos ceda
- [ ] A decisão é registada onde a Carta manda: `specs/00_AOS_Carta.md:177` diz que «as decisões só
      mudam pelo §6 (emenda datada acima)». Uma decisão fixada fora dessa via repete exactamente o
      defeito de forma que o AOS-375(c) corrige

### Estado

**POR DECIDIR.** P2. Não tem alcance de execução porque não tem execução. É a única entrada deste bloco
cujo desfecho não depende de código: as duas vias são legítimas e o dono escolhe. O que não é legítimo é
o estado actual — a matriz de conformidade a citar como controlo do Art. 9 um mecanismo cuja metade de
segurança está registada como não-composta e cuja semântica nunca foi ratificada por nenhum documento
com autoridade.


Ambos estão confirmados na §3 do relatório (`O-14` em §3.6, `C-05` em §3.7) e **nenhum dos dois
aparece na lista priorizada de §5**. Não caíram por refutação nem por deferimento: caíram por
omissão da própria remediação. É por isso que a redacção abaixo reverifica cada âncora e mede cada
gate em primeira mão.

---

## AOS-381 — O registry e a supply-chain selam para um armazém volátil que ninguém lê, e o gate mais adversarial do repositório prova a selagem sem nunca ver o destino

### Contexto

O nó constrói o revalidador de supply-chain por dois caminhos, e **os dois fabricam o seu próprio
armazém de audit in-memory**:

- `packages/cmd/aos/bootstrap.go:2802-2809` — `referenceRevalidator()` abre com
  `auditStore := audit.NewMemStore()` (`:2803`), passa-o a `signing.NewTrustStore` e a
  `revalidation.New`, e devolve **só** o revalidador. É o caminho por omissão, escolhido em
  `bootstrap.go:1763-1768` sempre que `Config.Revalidator` (`:409`) vem a nil.
- `packages/cmd/aos/modelcatalog.go:85-169` — `buildSignedToolRegistryFromEnv()` faz exactamente o
  mesmo em `:148`, e é o caminho que um operador **liga deliberadamente** com
  `AOS_MODEL_TOOLS_REGISTER`. O resultado sobe a `main.go:967` (`cfg.Revalidator = reval`).

As duas âncoras do relatório batem certo, sem correcção. O que se lhes acrescenta é que a segunda é
a pior das duas: aí o trust store **não** está vazio — `modelcatalog.go:150-156` acrescenta-lhe a
pubkey do assinante — e as selagens continuam a não ir a lado nenhum.

**O que é selado, e onde.** `signing/truststore.go:171-183` sela cada `Add`/`Revoke` na partição
`registry.truststore` (`:16`); `revalidation/revalidator.go:377-402` sela **cada decisão
allow/deny por chamada de tool** na partição `registry.revalidation` (`:17`). São nomes constantes
e estáveis, documentados no `CHANGELOG.md:657,672` como selados «no **audit WORM**» — afirmação
verdadeira da biblioteca e falsa do nó.

**Verificado: não existe leitor.** Em ambas as construções `auditStore` é variável local, nunca
devolvida nem guardada no `Node`. Uma varredura das duas constantes de partição em todo o
repositório (`.go`, `.py`, `.sh`, `.md`, excluindo `.claude/worktrees/`) devolve apenas: as duas
definições, dois parágrafos do `CHANGELOG.md`, um comentário de `deploy/node/dev-hardened/demo-pdp-tool-deny.sh:20`,
e `packages/integration/quarantine.go:80-81`, que usa a **string** `"registry.revalidation"` como
`agentID`/`runID` de um incidente — não como partição a ler. Nenhum `Read`, `Head`, `At` ou
`VerifyStore` sobre estes armazéns, em produção ou em teste do nó. As duas superfícies de leitura
que o nó tem são estruturalmente incapazes de lá chegar: `Node.VerifyWORM` (`bootstrap.go:2709-2712`)
re-encadeia `n.WORM` e mais nada, e `aos audit-trail` (`cli.go:113`, `audit_trail.go:3,43`) recebe
`--path` de um ficheiro — um `MemStore` não tem ficheiro. E morre no encerramento por construção:
`closeIfCloser` (`bootstrap.go:2716-2722`) regista em comentário que o `MemStore` de referência nem
sequer implementa `io.Closer`, porque não há nada para fechar.

**Porque é isto pior do que não auditar de todo.** Não é retórica; é uma propriedade da assinatura
dos construtores. `signing.NewTrustStore` recusa um store nil com `ErrNoAuditStore` e o comentário
diz «a auditabilidade é uma pré-condição» (`truststore.go:68-74`); `revalidation.New` faz o mesmo
(`revalidator.go:213-219`). O sistema de tipos **impõe que exista um armazém de audit e não tem como
impor que ele seja durável** — `audit.Store` é satisfeito por `MemStore`. O compilador certifica,
portanto, a propriedade errada, e certifica-a de forma visível: quem lê `referenceRevalidator` vê um
trust store auditável a ser construído fail-closed, e a leitura correcta do código produz a
conclusão errada. Se não houvesse audit nenhum, a ausência seria um buraco procurável — `grep` por
`audit` naquele caminho devolveria zero e qualquer auditoria tropeçava nele. Havendo, o buraco tem a
forma exacta da sua própria mitigação. É o que o relatório quer dizer com «a lacuna mais enganadora,
porque o código *parece* auditado», e o efeito mede-se: das oito lentes e oito refutadores da
auditoria 13, este caminho passou por várias e sobreviveu.

**A doutrina do ápice diz o contrário, e não tem porta por onde a impor.**
`packages/integration/secured.go:57-60` declara o `WORM` como «o `audit.Store` tamper-evident
**ÚNICO** (obrigatório)», e `NewSecuredRuntime` cumpre-o para os dois produtores de selo que
controla: o `EventSink` de mediação (`:389-390`, `audit.NewMediationSink(cfg.WORM)`) e o sink de
segurança de egress (`:334`, `network.NewWORMSecuritySink(cfg.WORM)`). O terceiro produtor entra
pela porta `Revalidator *revalidation.Revalidator` (`:53`) **já construído**, com o destino das suas
selagens fechado lá dentro. E não há como redireccioná-lo depois: `revalidation.New` recebe o
`audit.Store` por argumento posicional (`revalidator.go:213`) e as sete opções existentes —
`WithDigester`, `WithQuarantiner`, `WithAlerter`, `WithEgressAllowlist`, `WithTracer`,
`WithPartition`, `WithClock` (`:140-200`) — **não incluem um `WithAudit`**. Dois dos três produtores
obedecem à doutrina do WORM único; o terceiro não pode obedecer, e a validação de `:276-281`
verifica que ele não é nil sem poder verificar para onde sela.

**Dano real, separado do imaginado.** O dano **não** é de autorização: a decisão de revalidação
continua a ser imposta — o deny bloqueia, a quarentena e o alerta disparam
(`revalidator.go:356-375`), e a mediação da tool call é selada no WORM do nó pelo `EventSink` do
Reference Monitor. Também **não** é corrupção: os dois armazéns são disjuntos, pelo que a cadeia
volátil não pode partir a durável. O dano é **de prova**: perde-se quem foi confiado e quando, que
digest foi revalidado, e que artefacto foi bloqueado por que razão. Num incidente de supply-chain, «o
nó bloqueou o rug-pull» fica sem evidência durável nenhuma — e o efeito de segunda ordem é que a
única evidência que sobra, o registo de mediação no WORM, mostra a tool bloqueada sem mostrar o
motivo de supply-chain que a bloqueou.

**Porque é que o defeito sobreviveu — três razões, todas mensuráveis.**

1. **O gate mais adversarial do repositório mede o sítio errado.** `scripts/ci/supplychain.sh:1-30`
   corre sete vectores de ataque, sete meta-testes que provam a *detecção* (para não passar verde
   vazio) e re-verifica cada bloqueio «na hash-chain WORM tamper-evident com `audit.Verify` + os
   campos do registo». Corre sobre `packages/platform/registry/supplychaintests`, isto é, sobre o
   armazém que a própria suite constrói. Prova, com rigor invulgar, que **a biblioteca sela**.
   Ninguém escreveu a asserção seguinte — que o **nó** sela num sítio que sobrevive ao processo.
2. **O banner declara a metade errada.** `posture_banner.go:171-176,194-202` tem quatro ramos e
   declara em todos a postura do catálogo e do trust store («catalogo VAZIO (emptyCatalog) e
   revalidador de REFERENCIA com trust store VAZIO»). Nenhum dos quatro diz uma palavra sobre o
   destino das selagens — e o ramo que fica pior é «catalogo e revalidador INJECTADOS por config»,
   porque aí o operador tem toda a razão em concluir que ligou a supply-chain a sério.
3. **O deferimento vizinho descreve como saudável exactamente a parte que está partida.** `DEF-812`
   (`docs/governance/REGISTO-Deferimentos.md:241`, nota `N-DEF-812` em `:757-772`) regista que o nó
   não constrói o catálogo event-sourced, o host MCP nem o TOFU. Lido por inteiro, **não cobre isto**:
   o que a entrada declara em funcionamento — «o que CORRE e o congelamento por run e a revalidacao
   por chamada, ligados na cadeia do Reference Monitor» — é precisamente o caminho cuja cadeia de
   audit evapora. Isto não é dívida nova fora do registo; é o registo a atestar como sã a peça
   defeituosa. `DEF-812` tem de ser emendada, não invocada.

**O padrão da correcção existe, está documentado ao pormenor, e ninguém o replicou.** `AOS-265`
resolveu **esta mesma classe de defeito** para o audit de governação do Model Gateway:
`packages/cmd/aos/model_audit_env.go:1-25` descreve o antes («esse store era SEMPRE um
`audit.NewMemStore` … a governação do gateway NÃO sobrevivia a um restart … e nada o declarava») e
entrega três coisas — a variável `AOS_MODEL_AUDIT_PATH` que resolve um `audit.FileStore` durável
(`:57`), o fail-closed `ErrBadModelAudit` que aborta em vez de degradar em silêncio (`:41`), e a
linha de banner que declara a volatilidade quando a variável está ausente. O call site
(`modelgatewaywiring.go:203-210`) mantém o `MemStore` só como default declarado. O mesmo ficheiro
explica por que razão o gateway sela numa cadeia dedicada e não no WORM do nó — a ordem de
construção —, argumento que **não** se aplica aqui: o revalidador é composto dentro de `Bootstrap`,
depois de o WORM do nó existir (`bootstrap.go:1212`), pelo que a via mais simples está disponível.

### Critérios de Aceitação

- [ ] `grep -rn "audit.NewMemStore" packages/cmd/aos --include=*.go | grep -v _test.go` deixa de
      incluir `bootstrap.go:2803` e `modelcatalog.go:148`: as duas construções passam a receber o
      `audit.Store` por parâmetro em vez de o fabricarem. As restantes ocorrências
      (`bootstrap.go:1154`, WORM in-memory de referência; `modelgatewaywiring.go:210`, default
      declarado de `AOS-265`) mantêm-se e têm eixo próprio
- [ ] O revalidador composto pelo nó sela na **mesma** `audit.Store` que `SecuredConfig.WORM`, ou
      numa cadeia durável dedicada no molde de `AOS-265`. Prova por comando: após um arranque com
      `AOS_MODEL_TOOLS` e `AOS_MODEL_TOOLS_REGISTER` ligados e uma tool call revalidada,
      `aos audit-trail --path $AOS_WORM_PATH --run registry.revalidation` devolve pelo menos um
      registo; hoje devolve zero por o armazém não ter ficheiro
- [ ] Existe um teste no pacote `cmd/aos` que, sobre o nó composto, lê a partição
      `registry.truststore` do armazém do nó e assere ≥1 registo depois de um `Add` de publicador —
      isto é, o primeiro leitor não-teste-da-biblioteca desta cadeia
- [ ] **Prova de não-vacuidade por mutação:** com o novo encaminhamento revertido para
      `audit.NewMemStore()` numa cópia de trabalho, os testes do critério anterior avermelham. O
      número de testes que avermelham é registado no `### Estado`, no molde de `AOS-344`
- [ ] `revalidation.New` ganha `WithAudit` (ou o `audit.Store` deixa de ser posicional), de modo a
      que o ápice possa impor o WORM único que `secured.go:57-60` declara. Falsificável:
      `grep -n "^func With" packages/platform/registry/revalidation/revalidator.go` passa a listar
      oito opções
- [ ] `NewSecuredRuntime` recusa fail-closed um `Revalidator` cujo destino de audit não seja
      `cfg.WORM` (erro no molde de `ErrNoWORM`, `secured.go:34-38`), com teste negativo que prove a
      recusa e controlo positivo que prove que o caminho correcto passa
- [ ] O banner de plataforma (`posture_banner.go:194-202`) declara, nos quatro ramos, se as
      selagens de trust store e revalidação são duráveis ou voláteis. Falsificável por comando: um
      teste de banner assere a presença da linha em ambos os estados
- [ ] `DEF-812` e a nota `N-DEF-812` deixam de descrever a revalidação por chamada como parte «que
      CORRE» sem ressalva: ou a entrada é emendada para nomear a cadeia de audit volátil, ou a
      entrada é fechada por este ticket. O gate `deferrals` continua verde
- [ ] `scripts/ci/supplychain.sh` ganha uma verificação sobre o **nó composto** — não sobre
      `supplychaintests` — que falhe se o destino das selagens de supply-chain não sobreviver ao
      processo. Prova de não-vacuidade: com o destino revertido para in-memory, o gate avermelha
- [ ] `CHANGELOG.md:657,672` deixa de afirmar, sem ressalva, que as decisões de `registry.revalidation`
      são seladas «no audit WORM» — ou passa a ser verdade no nó, ou a frase é qualificada

### Estado

**POR IMPLEMENTAR.**

---

## AOS-382 — Dois gates bloqueantes passam por razões erradas: um verifica zero declarações, o outro perdoa 10 de 16 divergências com um dono já fechado

### Contexto

Os dois são *required checks*. `.github/workflows/ci.yml:17` lista-os na linha `REQUIRED-CHECKS:` e
`:417` põe-nos no `needs:` do agregador; os jobs vivem em `:123-131` (`estado-citado`) e `:165-173`
(`integration`). Verde nos dois é condição de merge.

Ambos foram corridos em primeira mão nesta árvore. **O output abaixo é medido, não citado.**

#### (a) `estado-citado` — verifica zero declarações

```
$ bash scripts/ci/estado-citado.sh
== GATE: estado-citado — declaracao com BLOQUEADOR: AOS-NNN cruzada com o estado desse ticket ==
estado-citado: 0 declaracao(oes) com BLOQUEADOR verificada(s); 1 abstencao(oes)
   ABSTENCAO packages/cmd/aos/broker_vault_env.go:67 — o eixo e `DEF-218`, que nao e um ticket AOS (e do `deferrals`)
OK: nenhuma declaracao marcada como BLOQUEADOR cita um ticket ja fechado.
$ echo $?
0
```

Diverge do relatório em dois pontos, ambos a favor da precisão e nenhum a favor do gate: a contagem
real diz «declaracao(oes) **com BLOQUEADOR** verificada(s)», e a abstenção é nomeada com ficheiro e
linha. O número é o mesmo: **zero**.

O corpus inteiro produz **um** marcador que abre declaração, e esse abstém-se. O gate percorre
`packages/`, `tecnica/` e `docs/adr/` (`scripts/ci/estado-citado.py:134`), cruza cada eixo contra o
`### Estado` dos tickets em `specs/EPIC-*.md` (`:132`) e classifica-o pelos oito lexemas de
`:187-193`. A varredura funciona; não há o que varrer. As outras quatro ocorrências de «bloqueador»
em Go de produção (`bootstrap.go:561`, `broker_vault_env.go:19`, `main.go:816`,
`platform/broker/resource_binding.go:27`) são todas da forma «o bloqueador real é…», que
deliberadamente **não** abre declaração — o gate está certo em não as contar.

**Isto não é o gate mal escrito.** O docstring (`estado-citado.py:16-45`) documenta a medição que
levou ao desenho opt-in: 7 103 citações `AOS-NNN` em `packages/`; um gate ingénuo daria 1 818
vermelhos; o vocabulário de bloqueio dá 97 citações, das quais 46 apontam a ticket fechado e ~90%
são falsos positivos por ambiguidade do português. A conclusão escrita é correcta: a relação «isto
espera por aquilo» tem de ser **declarada**, não inferida. O defeito é o passo seguinte, que ninguém
deu: **um gate opt-in cujo corpus tem zero adesões é indistinguível de um gate desligado**, e não há
nada que meça a adesão. `AOS-329` entregou o mecanismo e a disciplina de anotação nunca foi adoptada.

#### (b) `integration` — verde com 10 de 16 códigos de porta ausentes

```
$ bash scripts/ci/integration.sh
DIVIDA RECONHECIDA (10) — divergencias de contrato toleradas pela baseline, com dono declarado:
  ~ C3 E_NO_DECISION ausente em packages/platform/broker — owner=AOS-196; ...
  ~ C3 E_SCOPE_DENIED ausente em packages/platform/broker — owner=AOS-196; ...
  ~ C3 E_VAULT_UNAVAILABLE ausente em packages/platform/broker — owner=AOS-196; ...
  ~ C4 E_MODEL_UNAVAILABLE ausente em packages/platform/model-gateway — owner=AOS-196; ...
  ~ C4 E_RATE_LIMITED ausente em packages/platform/model-gateway — owner=AOS-196; ...
  ~ C4 E_REGION_DENIED ausente em packages/platform/model-gateway — owner=AOS-196; ...
  ~ C5 E_HASH_MISMATCH ausente em packages/platform/registry — owner=AOS-196; ...
  ~ C5 E_SCHEMA_CHANGED ausente em packages/platform/registry — owner=AOS-196; ...
  ~ C5 E_SIGNATURE_INVALID ausente em packages/platform/registry — owner=AOS-196; ...
  ~ C5 E_UNPINNED ausente em packages/platform/registry — owner=AOS-196; ...

Gate 4 (Integracao) OK: 5 contratos mapeados, 16 codigos de porta documentados,
  6 presente(s) no codigo, 10 em divida reconhecida com dono.
$ echo $?
0
```

Confirma o relatório na íntegra: 10 de 16, todos `owner=AOS-196`, exit 0. `grep -c "owner=AOS-196"
scripts/ci/baseline/contract-codes.txt` devolve **10** — não há uma única entrada com outro dono.

**O que o campo `owner=` vale hoje.** `scripts/ci/integration.py:224-225` verifica que a *substring*
`owner=` existe na linha; `:275-277` falha se faltar. **Não verifica que o identificador existe, nem
que aponta a um ticket, nem que esse ticket está aberto.** A baseline tem regras de honestidade
invulgarmente apertadas — declaradas em `contract-codes.txt:1-35`: só encolhe, entrada obsoleta
falha, entrada órfã falha, dono obrigatório. Nenhuma delas olha para o estado do dono.

**AOS-196 nunca teve este âmbito, e o próprio registo di-lo.** O ticket é
`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md:387` — «Registo único de deferimentos +
correcção dos eixos inválidos», cinco critérios todos `[x]` com commit de entrega citado
(`d33c0ff`). O seu trabalho foi criar `REGISTO-Deferimentos.md` e corrigir três eixos errados em
`tecnica/02` e nos ADR. Nada nele renomeia códigos de erro em `packages/platform/**`. E o registo
que ele próprio criou regista a pendência **P-5**
(`docs/governance/REGISTO-Deferimentos.md:982-986`), em texto que não deixa margem: «AOS-196 é o
**registo** de deferimentos, não o executor da reconciliação: renomear códigos em
`packages/platform/{broker,model-gateway,registry}` está fora do seu âmbito de escrita».

Pior: a atribuição vem da via que foi **explicitamente recusada**. O critério de aceitação de
`AOS-198` (`EPIC-18:444-448`) é disjuntivo e a via não escolhida — marcada `[—]` — era «a declaração
é retirada … e a deriva C3/C4/C5 fica registada como deferimento com eixo (AOS-196)». Escolheu-se
criar o gate. O campo `owner=` das dez entradas carrega, ainda assim, o eixo do caminho que não foi
tomado.

**Dano real, separado do imaginado.** Não há aqui deriva de contrato escondida: as dez divergências
estão escritas por extenso, com o nome real que o código usa, e nove das dez são de nomenclatura
(`E_DIGEST_MISMATCH` por `E_HASH_MISMATCH`, `E_SIG_INVALID` por `E_SIGNATURE_INVALID`, e por aí
fora). Também não é verdade que o gate não detecte nada — mediu-se o contrário (ver o critério de
mutação abaixo). O dano é que **o mecanismo de encolhimento não tem quem o accione**: a baseline só
encolhe quando alguém a faz encolher, esse alguém é o `owner=`, e o `owner=` é um ticket fechado
cujo registo declara por escrito que não é o executor. Uma dívida com dono inexistente é
indistinguível de uma decisão permanente, e o gate reporta-a como «dívida reconhecida com dono» a
cada corrida — a frase que faz a leitura errada parecer a leitura certa.

**Porque é que os dois sobreviveram — a mesma razão, medida.** Ambos têm self-tests fortes, e ambos
os self-tests correm sobre **corpora sintéticos injectados por variável de ambiente**:
`scripts/ci/selftest.sh:523,532,540,564,577` alimenta `integration.sh` com baselines fabricadas via
`AOS_CONTRACT_BASELINE`/`AOS_CONTRACTS_DOC`, e `:796,818,919` alimenta `estado-citado.py` com
árvores fabricadas via `AOS_ESTADO_CITADO_ROOT` (`estado-citado.py:129`). O self-test prova que o
*mecanismo* apanha o defeito. Nada prova que o *corpus real* dá ao mecanismo alguma coisa para
apanhar. Não-vacuidade contra entrada sintética não é não-vacuidade contra a árvore que o gate
guarda — e `selftest.sh:931-934` chega a asserir explicitamente que o `estado-citado` fica **verde**
contra a árvore real, tratando o verde vazio como o controlo desejado.

**E há um cruzamento que fecha o argumento.** O `estado-citado` existe, literalmente, para apanhar
«uma declaração nomeia o ticket que a fecharia, o ticket fecha, e a declaração fica a comprar
confiança que já não sustenta» (`estado-citado.py:6-11`). É a descrição exacta das dez entradas de
`contract-codes.txt`. O gate não as vê porque o seu âmbito de varredura é `packages/`, `tecnica/` e
`docs/adr/` (`:134`) — `scripts/ci/baseline/` fica de fora. As duas metades de `C-05` são o mesmo
defeito visto dos dois lados: o gate que verificaria zero declarações é o gate que deixaria de
verificar zero se olhasse para o sítio onde o defeito que ele procura está escrito dez vezes.

**Nota de precisão sobre «ticket fechado».** `EPIC-18` não tem uma única secção `### Estado`
(`grep -c "^### Estado"` devolve 0), pelo que o parser de estados (`estado-citado.py:196-213`)
resolveria `AOS-196` para indeterminado, não para fechado. Materialmente o ticket está entregue —
cinco critérios `[x]`, commit citado, e o registo a tratá-lo no passado. Formalmente, a máquina não
o consegue afirmar. Ambas as leituras condenam o `owner=`: ou aponta a um ticket fechado, ou aponta
a um ticket cujo estado o corpus não sabe dizer.

### Critérios de Aceitação

**Bloco (a) — `estado-citado` deixa de ser opt-in sem adesões**

- [ ] `bash scripts/ci/estado-citado.sh` passa a reportar **≥1 declaração verificada**. O número
      exacto é registado no `### Estado`; hoje é zero
- [ ] O gate falha quando o número de declarações verificadas cai abaixo de um piso declarado (no
      molde dos pisos de `AOS-199`), de modo a que a adesão futura não possa evaporar em silêncio.
      Falsificável: com o piso a 1 e o corpus a zero, o gate avermelha
- [ ] **Prova de não-vacuidade por mutação, já executada como linha de base.** Sobre uma árvore
      sintética fora do repositório (`AOS_ESTADO_CITADO_ROOT`) com um `packages/x.go` contendo
      um marcador `BLOQUEADOR` a citar um ticket inexistente e um epic sintético cujo `### Estado` diz `**IMPLEMENTADO**`, o gate
      devolve `FAIL … declaracao nomeia como BLOQUEADOR um ticket ja FECHADO`, `1 declaracao(oes)
      com BLOQUEADOR verificada(s); 0 abstencao(oes)`, exit **1**. O mecanismo está provado; o
      critério é que a mesma prova passe a existir **contra a árvore real**, com uma anotação real
      cuja remoção avermelhe o gate
- [ ] O âmbito de varredura (`estado-citado.py:134`) passa a incluir `scripts/ci/baseline/`, ou o
      gate ganha uma verificação irmã que cruze todo o `owner=AOS-NNN` de baseline contra o estado
      do ticket. Falsificável: com `owner=AOS-196` intacto, o gate avermelha; com o dono corrigido,
      fica verde

**Bloco (b) — `integration` deixa de perdoar com um dono que não executa**

- [ ] Nenhuma das 10 entradas de `scripts/ci/baseline/contract-codes.txt` mantém `owner=AOS-196`.
      Falsificável: `grep -c "owner=AOS-196" scripts/ci/baseline/contract-codes.txt` devolve **0**
      (hoje devolve 10). Cada entrada aponta a um ticket que existe, está aberto e tem no âmbito
      renomear códigos em `packages/platform/**`
- [ ] `scripts/ci/integration.py` deixa de aceitar `owner=` como mera substring: valida que o
      identificador resolve a um ticket existente em `specs/EPIC-*.md` e que esse ticket não está
      num dos lexemas de fecho de `estado-citado.py:187-190`. Falsificável por comando: uma baseline
      com `owner=AOS-196` faz o gate avermelhar
- [ ] **Prova de não-vacuidade por mutação da nova verificação:** com uma cópia da baseline em que
      um `owner=` aponta a ticket fechado, `bash scripts/ci/integration.sh` sai != 0; com o dono
      corrigido, sai 0. Ambos os ramos ficam no `scripts/ci/selftest.sh`, no molde das secções
      existentes (`:523-577`)
- [ ] **Linha de base já medida da detecção subjacente:** com `AOS_CONTRACT_BASELINE` a apontar para
      uma baseline vazia fora do repositório, `python scripts/ci/integration.py` sai **1** e nomeia
      as 10 divergências uma a uma (`ERRO: 10 codigo(s) de porta documentado(s) e AUSENTE(s) do
      codigo`). A detecção funciona: o que faz o verde é a baseline, não a ausência de divergência.
      Esta medição é registada no `### Estado` como controlo, para que a correcção não seja confundida
      com «ensinar o gate a detectar»
- [ ] A pendência **P-5** de `docs/governance/REGISTO-Deferimentos.md:982-986` deixa de estar sem
      executor: ou ganha ticket próprio citado nas 10 entradas, ou é fechada com a reconciliação
      feita. O gate `deferrals` continua verde
- [ ] O critério de aceitação `[—]` de `AOS-198` (`EPIC-18:444-448`) deixa de ser a origem do
      `owner=` de uma baseline viva: a menção a `AOS-196` como eixo da deriva C3/C4/C5 é corrigida
      ou anotada como histórica

**Bloco (c) — a razão comum**

- [ ] Existe uma verificação que distinga «gate verde porque a árvore está limpa» de «gate verde
      porque não olhou para nada»: cada um dos dois gates passa a imprimir e a fazer cumprir uma
      contagem de itens **efectivamente verificados na árvore real**, à semelhança do
      `require_tests` de `scripts/ci/lib.sh` que já impede o verde vazio no `supplychain` e no
      `replay`. Falsificável: com a contagem a zero, o gate avermelha
- [ ] `scripts/ci/selftest.sh:931-934` deixa de tratar o verde do `estado-citado` contra a árvore
      real como o controlo desejado sem qualificar que o verde de hoje é vazio

### Estado

**POR IMPLEMENTAR.**

---

## Anexo — verificação de âncoras

Todas as âncoras citadas nos dois tickets foram reverificadas contra a árvore em
`C:\Jimy\AOS` (ramo `docs/validacao-adversarial-epic-24`, HEAD `04f3d46`). Nenhuma escrita foi
feita no repositório; as duas mutações correram sobre ficheiros no directório temporário desta
sessão, via as variáveis de ambiente que os próprios gates expõem para self-test.

| Âncora do relatório | Estado |
|---|---|
| `bootstrap.go:2803` (`O-14`) | Confirmada — `auditStore := audit.NewMemStore()` |
| `modelcatalog.go:148` (`O-14`) | Confirmada — idem |
| `estado-citado`: «0 declaracao(oes) verificada(s); 1 abstencao», exit 0 (`C-05`) | Confirmada em substância; a redacção real é «0 declaracao(oes) **com BLOQUEADOR** verificada(s); 1 abstencao(oes)» e nomeia `broker_vault_env.go:67` |
| `integration`: 10 de 16 ausentes, todos `owner=AOS-196`, verde (`C-05`) | Confirmada na íntegra; exit 0 medido |
| «AOS-196, ticket de higiene documental já fechado» (`C-05`) | Confirmada em substância (cinco critérios `[x]`, commit `d33c0ff`), com a ressalva de que `EPIC-18` não tem secções `### Estado`, pelo que a máquina resolve o estado para indeterminado |


## AOS-383 — O contrato de fio do executor de sandbox declara `run_id` e `step_id` e não transporta nenhum dos dois

### Contexto

O nó delega a execução de uma tool call sandboxed num componente host-side externo, por HTTP. O
contrato de fio está declarado em `packages/cmd/aos/gvisorexecutor.go:44-56` e tem dois campos de
atribuição: `run_id` e `step_id`. Nenhum é preenchido com o que o nome diz.

`gvisorexecutor.go:72` preenche `RunID: inst.ID` — o identificador da **instância de sandbox**, não
do run — e **nunca preenche `StepID`**, que viaja sempre vazio. O `firecrackerexecutor.go:44-45,60`
repete o par exactamente.

O identificador da instância é construído em `packages/substrate/sandbox/driver_gvisor.go:62` como
`"gv-" + RunID + "-" + StepID + "-" + seq`. O delimitador é `-`, e `-` aparece **dentro** das duas
partes: um `RunID` `e3-fsread` com `StepID` `step-000001-tool-1` produz
`gv-e3-fsread-step-000001-tool-1-1`, de onde o par original não se recupera sem ambiguidade. O
componente que executa recebe, portanto, um campo mal-nomeado e outro vazio.

**Porque importa, e não é cosmético.** O componente externo é o único ponto do sistema onde a
execução acontece de facto, e é o primeiro sítio a que um investigador de incidente vai. Medido em
`analises/13` §2.4: o pedido chega ao executor com `"step_id":""`. Correlacionar o que o componente
executou com a decisão que o autorizou depende de desfazer à mão um identificador ambíguo.

**Porque sobreviveu.** Os campos existem e o JSON valida; nenhum teste asserta o *conteúdo* deles, e
o componente de referência não os usa. Um contrato cujos campos ninguém lê não falha — envelhece.

### Critérios de Aceitação

- [ ] `run_id` transporta o identificador do run e `step_id` o identificador do passo, ambos como
      valores próprios, em `gvisorexecutor.go` e `firecrackerexecutor.go`
- [ ] Um teste que asserta o **conteúdo** dos dois campos no corpo enviado, e não só a sua presença
- [ ] Controlo negativo: com os campos preenchidos à moda antiga (`inst.ID` em `run_id`, `step_id`
      vazio), o teste novo avermelha
- [ ] A ambiguidade do identificador de instância é fechada ou declarada: ou o delimitador deixa de
      poder ocorrer nas partes, ou `driver_gvisor.go:62` documenta que o ID não é decomponível e
      nomeia o que o substitui na correlação

### Estado

**POR IMPLEMENTAR.**

---

## AOS-384 — `ErrDriverUnavailable` manda procurar `/dev/kvm` a quem só lhe falta uma variável de ambiente

### Contexto

`packages/substrate/sandbox/errors.go:10` define `ErrDriverUnavailable` com o texto «sem KVM/host
support», e `driver_gvisor.go:60` devolve-o quando o driver gVisor não tem executor injectado.

O cabeçalho de `packages/cmd/aos/gvisorexecutor.go:14-17` declara, em maiúsculas, o contrário: o
gVisor **não exige `/dev/kvm`** — interpõe syscalls em user-space, e é por isso a única fronteira ao
nível do kernel disponível num host que seja ele próprio um convidado sem virtualização aninhada. É
a razão de o driver existir.

A condição real que produz o erro é a ausência de `AOS_SANDBOX_GVISOR_URL`. O operador que a leia vai
diagnosticar o host — procurar `/dev/kvm`, verificar virtualização aninhada, mudar de máquina — quando
lhe falta uma linha de configuração que o `deploy/server/README.md:149` documenta.

**Porque importa.** É o erro que separa um nó com sandbox provisionada de um nó sem ela, e o
`ErrProductionNeedsSandboxDriver` (`cmd/aos/main.go:314`) trata essa fronteira como crítica de
segurança. Uma mensagem que aponta para a causa errada nesse ponto custa tempo exactamente quando
alguém está a tentar fechar a fronteira.

**Porque sobreviveu.** O texto foi escrito para o Firecracker, onde é verdadeiro, e o driver gVisor
reutilizou o mesmo erro. Nenhum teste asserta mensagens de erro por conteúdo.

### Critérios de Aceitação

- [ ] O erro devolvido pelo driver gVisor sem executor nomeia a variável em falta
      (`AOS_SANDBOX_GVISOR_URL`) e não menciona KVM
- [ ] O erro do driver Firecracker continua a nomear KVM, que é verdadeiro para ele — os dois casos
      deixam de partilhar um texto que só serve um
- [ ] Um teste que asserta o conteúdo de cada uma das duas mensagens, com controlo negativo que
      avermelha se voltarem a ser o mesmo texto

### Estado

**POR IMPLEMENTAR.**
