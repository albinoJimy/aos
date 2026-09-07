# Auditoria adversarial transversal — GOV + OBS

| Campo | Valor |
|---|---|
| Documento | `analises/13_Auditoria_GOV_OBS_Adversarial.md` |
| Data | 2026-09-06 |
| Estado auditado | Iniciada em HEAD `6613e47` no ramo `feature/AOS-128-ux-dx-tests`; concluída em HEAD `04f3d46` no ramo `docs/validacao-adversarial-epic-24` — **o HEAD e o ramo mudaram a meio, por acção de outra sessão no mesmo worktree** (ver §1.2). Sem alterações desta auditoria na árvore |
| Âmbito | **GOV** — políticas e PDP (`control-plane/pdp`), RBAC/identidade/autoridade (`platform/identity`, `cmd/aos-issuer`), autonomia L0–L5 (`control-plane/governance/autonomy`) · **OBS** — spans OTel GenAI (`substrate/otel-genai`), replay (`kernel/agent-runtime/replay`), audit WORM (`platform/audit`) · e as costuras com `packages/integration` e `packages/cmd/aos` |
| Tipo | Oito lentes independentes → **refutação adversarial** com o ónus da prova invertido (uma por bloco, mais uma **experimental**) → **medição executada** no módulo, no nó real e em cópias isoladas do trilho |
| Auditoria anterior | `analises/12_Auditoria_ES_SBX_Adversarial.md` (2026-09-06), cujo §7.2 declarou `otel-genai/` explicitamente fora de âmbito |
| Contratos verificados contra | `specs/00_AOS_Carta.md`, `specs/00_System_Spec.md` §11, `EPIC-08`, `EPIC-09`, `EPIC-16` a `EPIC-24`, `tecnica/08`, `09`, `12`, `13`, `14`, `16`, `17`, ADR-011/013/015/016/017/018, `docs/governance/REGISTO-Deferimentos.md`, `docs/adr/README.md` |
| Remediação | Por abrir — ver §6 |

---

## 1. Método

Três passagens, com o ónus da prova invertido a cada uma.

**Passagem 1 — oito lentes independentes.** GOV/política-e-PDP, GOV/RBAC-e-identidade, GOV/autonomia,
OBS/spans, OBS/replay, OBS/WORM, conformidade spec→código, e uma lente dedicada às **costuras** (o que
acontece *entre* os subsistemas, que nenhuma lente vertical vê). Instrução comum: âncora `ficheiro:linha`
para tudo; `UNKNOWN` como resposta legítima; comentários e documentação **não** são evidência de
comportamento; e procurar activamente **testes que passam pela razão errada**. Resultado: **75
hipóteses-defeito**, cada uma enunciada de forma falsificável.

**Passagem 2 — oito refutadores.** Regra única: *o objectivo é derrubar a hipótese, não confirmá-la*.
Nenhum viu o raciocínio de quem acusou — receberam a afirmação e a âncora, nunca a justificação. Cada um
teve de classificar em quatro estados — **defeito**, **deferimento declarado**, **não-requisito**,
**refutada** — e atribuir **alcançabilidade** (alcançável hoje na configuração que o nó realmente levanta,
latente, inalcançável). Leitura obrigatória antes de decidir: `docs/governance/REGISTO-Deferimentos.md`,
`tecnica/14` §5.2 e `tecnica/17` §5 — é aí que morre a maioria das hipóteses de uma primeira passagem.

Um dos oito foi **experimental**: em vez de argumentar, copiou os módulos para fora da árvore, escreveu
programas-experiência e correu-os. É a diferença entre «li o código e concluo» e «corri o cenário e aqui
está o output».

**Passagem 3 — medição.** Gates executados (`replay`, `policy-test`, `layer-lint`, `apex`,
`estado-citado`, `integration`, o contador do tripwire da Carta §6.6); suites dos módulos corridas; nó real
levantado e conduzido ponta-a-ponta (`driver.sh smoke`, 9/9 verde); e 30 corridas do smoke inspeccionadas
no WORM e no WAL.

### 1.1 O que cada passagem mudou

| | |
|---|---|
| Passagem 1 → 2 | **21 hipóteses caíram** como falsas ou lidas fora de contexto; **13 foram reclassificadas** como deferimentos já registados com DEF-NNN; **11** eram verdadeiras mas nenhuma fonte de verdade as exigia |
| Passagem 2 → 3 | A execução decidiu cinco hipóteses que nenhum argumento tinha fechado, **agravou duas** (H49 e H59 saíram piores do que a acusação) e **derrubou uma** que parecia sólida |
| Refutação → tese | **Os dois achados mais importantes deste relatório não vieram de nenhuma lente** — nasceram de refutadores a verificar o que as lentes tinham assumido ser verdade |

O saldo — **30 sobreviventes em 75, e apenas 19 alcançáveis hoje** — repete a proporção das auditorias 11 e
12, e pela mesma razão: a passagem 1 confunde sistematicamente «o mecanismo não está montado» com «o
mecanismo está partido», e «a caixa não está marcada» com «o trabalho não está feito».

### 1.2 Erros desta auditoria, declarados

Sete, porque escondê-los invalidaria o método.

1. **O HEAD e o ramo mudaram a meio da auditoria e eu não o detectei a tempo.** Fixei o estado auditado em
   `6613e47` / `feature/AOS-128-ux-dx-tests` e escrevi-o como se fosse estável; outra sessão levou o
   worktree para `04f3d46` / `docs/validacao-adversarial-epic-24` durante a passagem 2 — e a mudança de
   *ramo*, mais grosseira do que a de commit, só foi notada pela medição de §2.2, no fim. As auditorias 09–12
   fixam um HEAD exacto, e esta não pode fazê-lo com honestidade. Verifiquei o delta: toca `EPIC-24`,
   `specs/INDICE.md` e `tecnica/16` (refresh de contagem de tickets, 357→361), tudo fora do âmbito GOV/OBS,
   e nenhum achado deste relatório depende dele. Mas a asserção «árvore estável» era falsa enquanto a
   escrevi. O refutador experimental detectou o sintoma antes de mim e atribuiu-o a si próprio.
2. **Três hipóteses minhas caíram, e uma delas era a que eu achava mais elegante.** Acusei a
   não-materialização do ADR-010 e do ADR-014 (H63): `docs/adr/README.md` declara «Catálogo, por
   materializar» como estado legítimo do vocabulário e a Carta §4.1 nem sequer os lista como FIXAS — não há
   enganado, logo não há achado. A H62 sobreviveu, mas mais ampla do que a acusei: há uma segunda instância
   da mesma contradição que eu não tinha visto.
3. **Um refutador foi contaminado no último passo.** O bloco GOV-A correu duas vezes (a primeira sessão
   morreu por limite de quota depois de escrever o ficheiro). O segundo refutador foi instruído a não ler
   `refuta-*.md`, mas a ferramenta de escrita obriga a ler antes de sobrescrever, e ele leu a passagem
   anterior *depois* de fechar toda a sua verificação. Declarou-o, preservou o original em
   `refuta-R1-ronda-anterior-6613e47.md` e escreveu uma secção de divergências. Ficam as duas passagens;
   ambas chegaram ao mesmo saldo por caminhos diferentes.
4. **Sobredeclarei um achado no §2, e apanhei-o ao cruzar a fonte única que o sustentava.** Escrevi que a
   verificação retirada do gate `event-catalog` era «exactamente a verificação que apanharia C-01». Não é:
   o docstring do script diz que ela operava à granularidade do **pacote** e que acusava `platform/audit`
   por importar o Event Store por outras razões — logo teria passado sobre C-01. A sobredeclaração de
   cobertura em `tecnica/13:231` mantém-se; a ligação causal que eu lhe acrescentei não. Fica como aviso
   contra a tentação de fechar uma narrativa: o achado *quase* encaixava, e foi por isso que passou.
5. **Não apliquei à minha própria manchete a regra que impus aos oito refutadores.** A inércia do
   TaintGate está declarada em quatro entradas do registo, e a DEF-604 nomeia `AOS-181, AOS-183`
   juntos. Pela minha própria regra, isso seria deferimento declarado. A verificação salvou o achado
   e melhorou-o (§2.3), mas não me salva a mim: o viés de quem acusa é mais forte sobre o achado de
   que mais gosta, e o meu método não tinha nada que o apanhasse — foi a redacção dos tickets, feita
   por outra pessoa, que o apanhou.
6. **Atribuí um `deny` à causa errada, e a atribuição estava na manchete.** O §2.2 dizia que o
   `denied_by=dispatch` da tool call untrusted vinha da ausência de um executor de sandbox. Vinha do
   **registo da tool** (`E_TOOL_NOT_REGISTERED`): com bloco `sandbox` no manifesto e sem executor
   nenhum, o Reference Monitor já permite (§2.4). Duas condições confundidas numa só frase, e a
   frase servia de conclusão. Só caiu porque a medição do último salto montou três células em vez
   das duas que eu tinha pedido — pedir o controlo negativo certo não me ocorreu.
7. **Uma medição ficou por fazer e não é substituível por leitura.** O comportamento do `/readyz` com o
   WORM montado só-de-leitura *a meio de um run* não foi medido: em Windows um handle já aberto mantém
   acesso de escrita, e forçá-lo exigiria alterar código do repositório. Está declarado como NÃO DECIDIDA,
   não inferido.

---

## 2. O achado central

> **A cadeia de governação está verificada em toda a parte excepto onde actua.**

O nó `aos` tem gates verdes, banners de arranque honestos e um registo de deferimentos exemplar. E o
caminho único que tudo isso existe para proteger — uma tool call realmente mediada, decidida pelo PDP e
selada de forma durável — não é exercitado por nada:

- **O smoke ponta-a-ponta nunca medeia uma tool call.** *(E é do smoke, não do nó: §2.2 obteve três
  mediações reais e seladas assim que o catálogo de tools deixou de estar vazio.)* 30 corridas inspeccionadas: 302 registos no WORM,
  **todos `allow`**, e os únicos `tool_id` são governação HTTP (`gov.control`, `gov.read`,
  `gov.residency`, `gov.sovereignty`). Partições `smoke-*` — o nome que uma mediação do RM produziria:
  **zero**. `tool.call.mediated/denied/escalated` no Event Store: **zero**. O modelo de referência conclui
  em um turno, sem tools. Todo o rigor anti-binário-obsoleto do driver protege um teste que não toca no
  ponto de mediação.
- **O gate `replay` escreve o campo anti-verde-fraco e nunca o lê.** `AnchorsVerified` e `OutcomeAnchored`
  não entram em `rep.Pass` (`replay_idempotency.go:245-250`), e ambos têm `omitempty`. Largar a âncora
  `Model` de `fixtures.go:122` degrada o gate a prompt-hash-apenas **e mantém-no verde**. Output real do
  gate: `"anchors_verified":["model","assembly_version"]` — `step_id` nunca lá esteve.
- **Um gate declara cobertura que não tem.** `tecnica/13:231` afirma que o `event-catalog` verifica «que um
  nome catalogado em (a) é mesmo apendado ao Event Store (o pacote importa `substrate/eventstore`)»; o
  próprio `event-catalog.py` declara, na secção «O QUE ESTE GATE **NÃO** VERIFICA», que essa verificação
  «foi implementada, medida contra a árvore, e **retirada por imprecisão**». É sobredeclaração de cobertura
  de gate — a direcção perigosa. **Ressalva de precisão:** a verificação retirada não teria apanhado C-01 —
  operava à granularidade do **pacote**, e o docstring regista que acusava `platform/audit` por importar o
  store por outras razões. O buraco de C-01 é de *caminho de chamada*, e continua sem gate que o cubra.
- **A invariante nuclear não tem gate.** O `layer-lint` valida o grafo de *imports*, não o de *chamadas*.
  Isto **não** é um defeito (§3.7): a invariante é imposta pelo compilador — `dispatch` é não-exportado e
  exige um `Permit` inconstruível fora do pacote (`monitor.go:521-524`). Mas significa que a verificação
  vive em dois testes do ápice sobre *fakes*, e não em execução real.

É esta vacuidade que permite que os três defeitos mais graves deste relatório existam sem ninguém tropeçar
neles.

### 2.1 O defeito mais material: a ordem de segurança está invertida, e o nó convida o operador a completá-la

`specs/EPIC-18` §5 não deixa margem: *«Isto **não é uma recomendação, é uma restrição de ordem**»*. A ordem
obrigatória é **AOS-183 (activar TaintGate) → correcção de CON-04 → AOS-181 (carregar bundle PDP real)**. O
racional escrito é explícito: carregar a política antes de ligar a barreira de taint «transforma um nó
*seguro-mas-inerte* num nó **permissivo com a defesa estrutural desligada**».

O que está entregue é a segunda metade, e não a primeira. Verificado de primeira mão:

- `packages/cmd/aos` **nunca** preenche `Privileged` — zero ocorrências fora de testes.
- `packages/integration/secured.go:301-303` cai no fallback `NewStaticPrivilegedSet()`, cujo próprio
  comentário diz «classificador real (vazio)».
- `secured.go:394` compõe `NewProductionSecure`, que **aceita** o conjunto vazio. O construtor que o
  recusaria — `NewProductionHardenedTaint`, com `ErrTaintGateInert` («conjunto privileged vazio ⇒ nenhuma
  promoção tainted é barrada; exige um PrivilegedAuthorizer não-vazio (AOS-183)»,
  `reference-monitor/production.go:29,226-232`) — existe, está testado e tem **zero chamadores**.
- A superfície de AOS-181 foi entregue por AOS-220: `AOS_POLICY_BUNDLE_DIR` + `AOS_POLICY_TRUST_ANCHOR`
  (`cmd/aos/main.go:746,1288`).

E o agravante que torna isto alcançável em vez de teórico: o **banner de arranque instrui o operador a
fazê-lo**. `bootstrap.go:2381` diz literalmente «*defina AOS_POLICY_BUNDLE_DIR + AOS_POLICY_TRUST_ANCHOR
(pubkey ed25519 out-of-band) para carregar um bundle assinado*». Seguir a instrução do próprio nó põe-no no
estado que o epic declara proibido, e nenhuma linha do banner o avisa.

### 2.2 A medição — o que acontece quando se segue a instrução do banner

Isto deixou de ser derivado por leitura. O nó foi levantado duas vezes, com e sem bundle carregado, e
conduzido até obter **três decisões de mediação reais** (gateway OpenAI-compatible construído fora da
árvore, mais `AOS_MODEL_TOOLS`). O resultado **confirma** o achado, agrava-o num ponto e atenua-o noutro —
e é a atenuação que produz o achado mais fino de todo o relatório.

**Agrava-se: não é wiring por ligar, é superfície de configuração inexistente.** Enumeradas as **102
variáveis `AOS_*`** que o binário lê: **nenhuma consegue povoar o conjunto `privileged`**. Um operador que
leia a auditoria, concorde com ela e queira fechar o buraco **não tem como**. E não há aviso em superfície
nenhuma — banner, log, `/healthz`, `/readyz`, `/metrics`, nenhuma das 25 rotas. Os dois banners (54 → 55
linhas) diferem em exactamente duas linhas substantivas, ambas sobre o PDP (`NAO-CARREGADO` →
`BUNDLE CARREGADO ... versao "1.0.0"`, mais o selo AOS-310 no WORM). **Nada muda no eixo do taint:** o nó
atravessa a fronteira que a `EPIC-18` §5 diz que não devia atravessar, em silêncio. Não existe predicado
observável — `HasActiveTaintGate()` existe, está testado e tem **zero chamadores**; o `posture_banner.go`
tem treze funções de postura e nenhuma para o Reference Monitor.

**Atenua-se: o nó não fica cegamente permissivo.** A política Cedar committada traz o seu próprio predicado
de taint e **negou de facto** uma tool call untrusted:

| Tool call | Desfecho medido |
|---|---|
| `cap:http.post`, `taint=untrusted` | **`denied_by=policy`** — negada pelo Cedar, cláusula `context.taint != "untrusted"` da regra `allow_http_post` |
| `cap:fs.read`, `taint=untrusted` | **`denied_by=dispatch`** — **todos os hooks permitiram**, PDP e TaintGate incluídos. Só não executou por não haver executor de sandbox provisionado |

Cada decisão foi selada no WORM em partição não-`gov.*` (partição = RunID) — o que, de passagem, mostra que
a vacuidade do §2 é do *smoke*, não do nó: com tools no catálogo, a mediação regista.

**O achado que a medição produziu, e que nenhuma lente tinha:** com o TaintGate inerte, a única aplicação
de taint que resta é **uma cláusula opcional dentro do texto da política**. Uma regra que a esqueça fica sem
rede — e a rede está inerte sem que nada o diga. `allow_fs_read` é exactamente essa regra. A defesa
estrutural, que por desenho vale para todas as regras, foi substituída em silêncio por uma disciplina de
escrita de política que ninguém verifica.

### 2.3 A correcção que a redacção dos tickets me obrigou a fazer — e que melhora o achado

Impus aos oito refutadores a regra de que uma hipótese já presente no `REGISTO-Deferimentos.md` é
**deferimento declarado, não defeito novo**. Não a apliquei à minha própria manchete, e ela falha o
teste: a inércia do TaintGate é o eixo de saída de **quatro** entradas — `DEF-604:222`,
`DEF-606:224`, `DEF-808:238`, `DEF-809:239` — e a DEF-604 nomeia **`AOS-181, AOS-183` juntos**,
descrevendo em texto o estado que o §2.1 apresentava como descoberta: «o PDP não carrega bundle e o
conjunto `Privileged` é vazio».

Cai, portanto, a alegação implícita de que isto era desconhecido. O que a verificação encontrou em
vez dela é mais grave, e passa a ser a manchete:

> **A mitigação que essas entradas invocam para se darem por contidas não existe.**

A DEF-606 está classificada **MITIGADO** com este argumento textual: o ápice arranca com um gate
«wired-mas-inerte, **declarando a postura via `Monitor.HasActiveTaintGate`** (sem alegar
endurecimento que não tem)». Medido: `HasActiveTaintGate`
(`kernel/reference-monitor/production.go:123`) tem **zero chamadores** fora do ficheiro que o
define, e o `posture_banner.go` não tem função de postura para o Reference Monitor. A declaração
honesta que sustenta o estado MITIGADO **nunca acontece**.

O achado sobrevive com fronteira nítida — quatro pernas, todas verificadas:

| | |
|---|---|
| (a) | A DEF-606 afirma uma mitigação sem chamadores; o estado MITIGADO não se sustenta |
| (b) | O gatilho de saída das quatro entradas («conjunto `Privileged` real no ápice») **não tem superfície de configuração** — o operador não o pode satisfazer nem que queira |
| (c) | A metade AOS-181 foi entregue (AOS-220) sem a metade AOS-183, contra a ordem da `EPIC-18` §5 — e é a DEF-604 que as nomeia juntas, pelo que a violação da ordem estava registada e ninguém reparou |
| (d) | Medido: `allow_fs_read` não tem a cláusula de taint, e uma tool call untrusted passou todos os hooks |

**Severidade: alta. Alcançável hoje, e medida.**

### 2.4 O último salto, medido — e a atribuição do §2.2 estava errada

O §6.2 declarava por medir o que separava aquela tool call de um efeito real, e dizia que medi-lo
exigiria o `AOS-103`. **Não exigiu.** Provisionou-se um executor **conformante e descartável** — um
gravador que fala o contrato de fio de `cmd/aos/gvisorexecutor.go:44-62` e não executa nada — e a
call chegou lá:

```json
{"run_id":"gv-e3-fsread-step-000001-tool-1-1","step_id":"",
 "call":{"tool_id":"ler_ficheiro","command":"read","path":"/etc/hosts"}}
```

O selo WORM da mesma decisão guarda o taint **dentro** da hash-chain, ao lado do `allow` que a
permitiu — evidência mais forte do que a do §2.2, onde o taint era inferido do contexto devolvido ao
modelo:

```json
"Decision":"allow","Capability":"cap:fs.read","PolicyVersion":"1.0.0",
"Resource":{"Type":"file","Value":"file:///etc/hosts","Region":"eu"},
"Context":{"Taint":"untrusted","Reversibility":"reversible"}
```

E o `stdout` do executor voltou ao contexto do modelo como `<tool_result taint=untrusted>`. Ciclo
fechado.

**A correcção, que é contra este relatório.** O §2.2 atribuía o `denied_by=dispatch` à ausência de
executor. É falso, e três células medidas separam as duas condições que ele confundia:

| Bloco `sandbox` no manifesto | `AOS_SANDBOX_GVISOR_URL` | Veredicto do Reference Monitor | Chegou ao executor |
|---|---|---|---|
| não | presente | deny `denied_by=dispatch` (`E_TOOL_NOT_REGISTERED`) | não |
| **sim** | **ausente** | **allow** | não — morre depois, no `Create` do driver |
| sim | presente | **allow** | **sim** |

O `deny` do §2.2 vinha do **registo da tool**, não do executor. Com bloco `sandbox` e sem executor
nenhum, o Reference Monitor **já permite**. A cadeia de governação nunca foi a barreira — nem
sequer parcialmente —, e o que faltava era configuração de operador: dois valores num ficheiro e uma
variável de ambiente. **O achado central fecha e agrava-se um grau.**

**O que esta medição NÃO prova, e é preciso dizê-lo.** O alvo não é gVisor: é um gravador sem
`runsc` que não toca no sistema de ficheiros do host. Nada a jusante do POST foi medido — isolamento,
interposição de syscalls, fuga. A medição vale para «o nó despacha até à fronteira do executor» e
para mais nada. Confundir as duas coisas seria cometer, aqui, o erro que este relatório acusa o
sistema de cometer.

**Nota lateral com valor próprio:** a cadeia fail-closed até chegar ao PDP é densa e honesta — exigiu quatro
correcções sucessivas (NHI, `model:invoke`, allowlist regional, path `/v1`), cada uma com erro atribuível e
accionável. É esse padrão que faz o silêncio sobre o taint destoar: o nó explica tudo o resto.

---

## 3. Achados por eixo

Severidades: **A** alta · **M** média · **B** baixa. Alcançabilidade: **hoje** · **latente** (exige
configuração que ninguém usa) · **inalcançável** (o caminho não está composto).

### 3.1 GOV — política e PDP

Sete das dez hipóteses caíram. As três que sobrevivem são todas baixas, e nenhuma na forma acusada.

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| G-01 | `NewProductionSecure` não tem guarda nenhuma no slot `policy` — nem anti-stub nem de presença — quando AOS-355 acabou de criar exactamente essa guarda para o egress (`reference-monitor/production.go:193-213`). O `PolicyStub` default-allow não chega à produção (`secured.go:381` fixa `NewPolicyCheck`); o que falta é a simetria da guarda | B | latente |
| G-02 | O assert de cobertura de deny por-regra usa `contains(reason, "default-deny")`, substring presente **nas duas camadas** (`pdp/capabilities.go:161` e `engine_cedar.go:158`): um deny que nunca chega ao Cedar conta como cobertura da regra Cedar. Está num gate **bloqueante** (`policy-test.sh` GATE 7) | B | hoje |
| G-03 | `SignBundle` só **compila** a política, não a avalia; `policy-sign` imprime «verificacao OK» sem ter avaliado nada. Uma política assinada com atributo não-mapeado nega tudo em runtime (`pdp/engine_cedar.go:121-144`) | B | hoje |

**O que caiu, e porquê importa.** As duas hipóteses mais chamativas — *rollback para um bundle antigo
validamente assinado* e *ausência de frescura* — são **não-requisitos**: o ADR-011 circunscreve a monotonia
ao *hot-reload* (linhas 46/104/125), os cinco critérios de AOS-088 não a pedem, e o adversário exigido já
detém o nó (quem escreve no directório do bundle escreve também o ficheiro que define
`AOS_POLICY_TRUST_ANCHOR` — troca o anchor em vez de fazer downgrade). Além disso o downgrade é sempre
selado no WORM (AOS-310). A acusação de que o Model Gateway tem política própria não assinada é
**falsa**: a allowlist é ed25519-assinada com trust anchor pinado por fingerprint em código
(`model-gateway/policy/allowlist/allowlist.go:81,110,159`) e o nó compõe-a.

**Deferimentos confirmados:** soberania por board desligada = DEF-909; `policydiff` inerte = residual
declarado de AOS-310; `agent_class` forjável sob `IdentityStub` = DEF-602.

### 3.2 GOV — RBAC e identidade

**Não existe RBAC.** Não há entidade «papel» em lado nenhum; o modelo é *capabilities* escopadas + ABAC
Cedar, com três vocabulários desligados entre si (NHIs, operadores, e o plano de governação — que não tem
capability nenhuma). Isto não é, por si, um defeito: nenhuma fonte exige RBAC nominal. É a consequência
que o é.

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| G-04 | **O plano de governação não tem autorização por capability.** `readGov.authorize` exige apenas ID-token OIDC válido, `sub` não-vazio e uma claim `board` que resolva para uma região (`cmd/aos/sovereignty.go:164-212`, `read_credential.go:62-82`). Um token emitido para **ler** runs autoriza também `POST /dsar/erase` (crypto-shred irreversível), `/dsar/expire` e `/dsar/release`. Não há capability a extrair porque `oidc.Claims` **não tem campo de escopo** (`integration/oidc/oidc.go:151-164`), e um só par issuer/audience serve leitor e operador DSAR. Contraste que fecha o argumento: `/autonomy` — reversível — exige capability `autonomy:set`, pubkey e assinatura; o apagamento irreversível não exige nenhuma | **A** | **hoje** |
| G-05 | `/dsar/release` e `/dsar/expire` não fazem **nenhuma** verificação de região, contornando a barreira de residência de AOS-182/DEF-202 num caminho de destruição | M | hoje |
| G-06 | No emissor de produção a `ClassPolicy` é fabricada da **mesma** flag `--caps` que alimenta a `UserAuthority` — `intersect(s,s) == s`, logo não há tecto de classe (`cmd/aos-issuer/main.go:197,207-208,218`). Mitigação real e parcial: com `--assertion` o nonce OIDC é o digest de `(agent,class,caps,ttl)` | B/M | hoje (exige autoridade de emissão) |
| G-07 | A revogação é só por `jti`: revogar um pai não revoga filhos, e `verifyParent` não consulta a revogação (`identity/issuer_child.go:126-155`) | B | inalcançável (`IssueChild` não composto no nó) |

**O que caiu.** «Retirar um operador impede o arranque» é **falsa** — `events.go:281-286` faz `continue`
sob um bloco intitulado «FAIL-CLOSED NO NÍVEL, NÃO NO ARRANQUE», e o teste que o prova passa. «O hook
`policy` antes do `scope` deixa o PDP ver autoridade não-estreitada» cai como defeito de autorização: a
cadeia é conjuntiva e o `ScopeGate` nega a seguir. Ausência de rotação de chave de issuer: verdadeira, mas
**nenhuma fonte a exige**. Four-eyes contornável por insider com duas chaves: **DEF-107, ABERTO**.

O four-eyes, aliás, é dos melhores mecanismos do repositório e merece registo: fecha auto-aprovação, mesma
sessão, mesma credencial, downgrade dual→single, *previews* que o nó nunca escalou, replay de perna e
reutilização de grant — e a âncora real de «duas pessoas» é uma guarda que **aborta o arranque** se duas
entradas do roster partilharem pubkey (`main.go:2323-2327`).

### 3.3 GOV — autonomia L0–L5

Este é o bloco onde a refutação mais mudou o quadro, e a razão é normativa, não técnica.

**A escada L0–L5 não tem fonte normativa.** A Carta não contém a palavra «autonomia». O ADR-014, que a
`EPIC-09` invoca como autoridade, **não existe como documento** — `docs/adr/README.md` classifica-o
«Catálogo, por materializar», e todo o seu conteúdo normativo é uma linha em `specs/00_System_Spec.md:259`.
O texto com semântica por nível vive em `tecnica/09` §7, e as frases que o definem são **rótulos de nós de
um bloco mermaid**. Os critérios de aceitação de AOS-089/090/095 estão todos por marcar, ou seja o corpus
não reclama entrega.

A consequência é desconfortável e é o resultado mais transferível deste eixo: **não se encontraram
divergências L0–L5 porque a implementação corresponde à spec; não se encontraram porque não há texto com
autoridade que diga a que deveria corresponder.** As seis hipóteses da forma «o nível N não faz o que a
spec diz» são todas **não-requisito**.

O que sobra, verificado ao longo de dezassete saltos de `loop.go:470` até ao dispatch:

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| G-08 | **O dual-control de L4/L5 é detectivo, não preventivo.** `autonomy_levels.go:369-372` implementa «ambiente DIFERENTE do último provisionamento ⇒ o ambiente GANHA, **em qualquer direcção**». O comentário justifica-o com a alavanca de resposta a incidente e o exemplo que dá é sempre *descer* (linha 375) — mas a regra também deixa **subir**: pôr `agt:dom=L5` em `AOS_AUTONOMY_LEVELS` e reiniciar aplica L5 **sem assinatura nenhuma**, quando a mesma mudança por `POST /autonomy` exige duas assinaturas distintas de detentores de `autonomy:set` (AOS-305). A mudança **é** selada no WORM como `config:node`, logo fica rastreável — o que cai é a prevenção, não a detecção. Nenhum documento a declara assim | M | hoje |
| G-09 | **O selo WORM não guarda a classe de agente** (`audit/record.go:32-35,64-73`). Por isso `POST /autonomy/simular:126` passa `""` onde `pdp/autonomy.go:58` lê `in.Principal.AgentClass`, e **responde sobre uma política diferente da que vigora** em qualquer deployment governado por regras `class:` — uma simulação que mente ao operador. *Correcção a uma versão anterior deste relatório, que acusava também a perda da `risk_class`: essa metade está mitigada de propósito — `autonomy_simular.go:124` chama `reclassificar` (`:196-224`), que reconstrói um `rm.Call` dos factos selados e corre o classificador real, precisamente para não divergir.* | M | hoje |
| G-10 | O oráculo de autonomia não tem guarda `AOS_MODE=production`, ao contrário do TLS, da identidade e da sandbox. A perna «inerte em silêncio» da acusação é **falsa**: `posture_banner.go:312` imprime literalmente a armadilha («*sozinha, AOS_AUTONOMY_LEVELS e IGNORADA em silencio, ate malformada*») | B | hoje |
| G-11 | O `PendingApproval` que chega ao humano (`loop.go:485-492`) não leva o nível de autonomia nem o modo de oversight — o aprovador decide sem saber sob que regime está a aprovar. Relevante para o Art. 14 | B | hoje |

**Medido, e a favor do sistema:** com bundle carregado e `AOS_AUTONOMY_LEVELS` definida, o veredicto
`escalate` **é alcançável** e funciona ponta-a-ponta — `autonomia L1 x danger -> confirm (gate humano)`, com
o run a parar em `waiting_on_human` (§2.2). O oráculo é opt-in e mal sinalizado (G-10), mas quando ligado
faz o que promete.

**Deferimentos confirmados:** promoção/demoção automática não composta (`autonomy.NewController` sem
chamadores) = **DEF-908**; `RiskClassifier` sem `RiskGate` no ápice = **DEF-905**.

**O que caiu por medição.** «Scheduler, planner, delegation e sandbox colapsam `Escalate` em falha»: `go
list -deps` a partir de `packages/cmd/aos` mostra que `orchestrator` e `scheduler` **não estão no grafo**, e
`MediatedLauncher.Execute` tem zero chamadores não-teste. Quatro pernas fora do produto. «A espera humana
não é durável»: `hitl.NewChannel` não tem chamadores de produção e `plan-approval` não está no binário; o
caminho real do nó **é** durável (Event Store + ordem deliberada em `escalation_sink.go:57-70`).

### 3.4 OBS — spans OTel GenAI

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| O-01 | **O OTLP sai sem atributos de recurso.** Não há `service.name` — zero ocorrências em todo o repositório — e `MarshalOTLP(spans, scope)` (`otlp.go:74`) não tem sequer parâmetro por onde injectar recurso. Tudo chega ao backend como `unknown_service`. **Precisão acrescentada na redacção dos tickets:** o struct de wire JÁ TEM o campo (`otlp.go:23-30`, `otlpResource.Resource`) — não falta representação, falta quem a preencha, o que reduz o custo da correcção sem reduzir o achado. As configs de colector entregues não compensam. Verificado lendo os dois ficheiros de serialização por inteiro | **A** | hoje |
| O-02 | `SpanKind` fixo em `INTERNAL`, e é **pior** do que o acusado: `SpanData` **não tem campo `Kind`** — a porta não tem o conceito. A chamada ao modelo, que a convenção trata como CLIENT, sai como INTERNAL | **A** | hoje |
| O-03 | `error.type` recebe a mensagem **crua** da tool (`monitor.go:253`). O argumento decisivo é interno ao ficheiro: `monitor.go:226` hasheia o Input com o comentário «o Input jamais é gravado no span», e a linha 253 escreve o erro cru da tool **na mesma função**. A correcção já existe escrita um pacote ao lado (`spanErrorType`, `worker.go:433-450`). Cardinalidade: baixa (cardinalidade alta é o desenho declarado). **Fuga de dados: média-alta** | M/A | hoje |
| O-04 | A instrumentação do PDP existe e está **morta**: `pdp.WithTracer` tem call-sites só em `pdp/aos088_test.go`. O nó nunca o passa, logo `NoopTracer`. Ficam mortos o span `policy.reload` e o span do overlay de autonomia — que é literalmente o critério «exposição do nível corrente na observabilidade» | M | hoje |
| O-05 | Um run de topo retomado após crash **começa outro trace**: persiste-se `ParentTraceParent` (do pai da delegação, `resume_records.go:51`), nunca o SpanContext do próprio run. Sem teste de continuidade | M | hoje |
| O-06 | O `operational_alerts.json` exportado não carrega o rótulo `produtor` que o `/metrics` carrega: cinco regras inertes, três delas `critical`, chegam ao alertmanager sem aviso | M | hoje |

**O que caiu.** «A redacção não está no caminho e o objectivo sai cru»: **refutada** —
`bootstrap.go:2243-2248` constrói o ingestor incondicionalmente e o caminho nil é declarado só-para-prova.
«Não há sampling»: a ausência de sampling é o **requisito** (`EPIC-08:415`, «sem sampling que perca
cardinalidade útil»). «`gen_ai.usage.cost` diverge de `cost_usd`»: **refutada** — são coisas diferentes,
uma métrica em micro-USD inteiro e um atributo de span em USD float. «`PDP.Decide` não abre span»: a
manchete cai, porque o veredicto **está** no trace (`aos.decision` + `aos.decision.denied_by`,
`monitor.go:243-249`).

Sobre conformidade semconv, a resposta honesta é **UNKNOWN**: nem o código nem a `EPIC-08` nomeiam uma
revisão da convenção. Os atributos `aos.*` (~350 chaves) estão correctamente em namespace próprio — isso é
a prática certa, não um desvio. A aritmética da passagem 1 estava errada: são **5 de 8** regras de alerta
que não podem disparar neste binário, não 4 de 8, e metade do achado é **DEF-281**.

O adaptador OTLP fail-open, com fila não-bloqueante e contadores `aos_otlp_spans_{exported,failed,dropped}`
expostos em `/metrics`, é melhor do que a média dos que usam o SDK oficial. O zero-dep (ADR-017) é decisão
congelada e nada aqui o contesta.

### 3.5 OBS — replay

**O que «replay» significa aqui.** O ADR-015 §1 desambigua a linha de uma frase do ADR-010: *replay
determinístico* = **resume-from-step**, o primitivo do ADR-001. Essa leitura **está** composta no nó
(`resume.go:201` → `Reconstruct` → `replayPlan`). A leitura «re-execução auditável por um terceiro» não é
pedida pelo ADR-015, nem pela `tecnica/17` §5.2, nem por nenhum DEF. Por isso as duas hipóteses maiores
deste eixo — «o replay existe como motor mas não como capacidade» e «a retoma re-executa efeitos reais sem
modo verify-only» — são **não-requisito**, e o gate mede a garantia que os ADR fixam
(`total_duplicated_effects: 0`).

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| O-07 | **Um turno cuja captura falte é invisível para a reconstrução — MEDIDO.** `admit()` (`replay/engine.go:375-390`) itera `tr.turns`, alimentado por `turn.recorded`, e tem `if !ok → ErrIncompleteCapture`; `Reconstruct` (`replay/sovereign_content.go:124,169`) constrói `order` **só** a partir de `EventTypeCaptured`, pelo que não há nada a recusar. Medido no pacote `replay` com o `runOriginal` real (3 turnos) e o `captureDroppingReader` do próprio repositório, suprimindo a captura do turno 2: `Replay`/`admit` → `ErrIncompleteCapture`; `Reconstruct` → **`err=nil`, `n=2`, turnos `[1 3]`**. E no `cmd/aos`, com `replayPlanFor` real sobre um Event Store que suprime o evento: `len(plan)=2` e o cliente de modelo devolve resposta nova ao vivo no turno 2. Corrupção e truncagem (AOS-289) são **simétricas** nos dois caminhos; a lacuna é exclusivamente o evento **inteiramente ausente**. Não existe verificação de hash na retoma, contagem contra o manifesto, uso do cursor durável, nem gate no selo de AOS-304 | M | hoje |
| O-08 | `aos audit-trail` abre o trilho em **escrita**, não pede `LockWAL` e herda a truncatura de §3.6: pode correr contra um nó vivo e destruir o tail parcial que é o registo em voo. O «nó parado» é convenção em prosa, sem guarda | M/A | hoje |
| O-09 | A única superfície de replay que o nó **serve** é inspecção, e não o declara: `GET /runs/{id}/reconstruct` devolve conteúdo decifrado sem nenhum campo a dizer que nada foi verificado — e é a rota que um auditor real vai usar | M | hoje |
| O-10 | `Manifest.Skills` é pinado e nunca comparado; `captureSchemaVersion` só é comparado no caminho mode 3. Mesma classe de drift invisível que motivou as âncoras `model`/`assembly_version` | B | latente |

**Duas coisas que a medição de O-07 corrigiu contra a auditoria.** Primeira: a passagem 1 afirmou que
`resume.go:264` «loga sucesso com o N errado». **É falso** — `len(plan)=2` e são de facto dois os turnos
reproduzidos da captura. O defeito não é o número mentir; é o log **não denunciar** a lacuna, porque nunca
compara com a contagem de `turn.recorded`. Segunda: existe uma **barreira parcial no efeito** que a
acusação ignorava. A chave do step-ledger é puramente posicional (`durable/step_id.go:70-80`, sem hash do
input) e o `already-applied` precede a mediação (`step_ledger.go:131`), pelo que uma tool call *diferente*
produzida pelo modelo ao vivo colide na mesma chave e **não executa**. O pior cenário implícito — efeito
não-aprovado executado — **cai**.

O que sobra é dano de **fidelidade e prova**, não de efeito: trajectória fabricada (resposta nova colada a
resultado antigo), possível terminação antecipada se a resposta ao vivo trouxer `Final=true`, e — sem
mitigação nenhuma — o read-path soberano `GET /runs/{id}/reconstruct` a devolver **200 com uma trajectória
curta e silenciosa**, que é a rota que um auditor externo usa.

**Correcção de uma prescrição errada desta auditoria.** A primeira versão deste parágrafo dizia que
bastaria «recusar no laço qualquer turno sem entrada em `caps`». Isso é um **no-op**: o laço de
`replay/sovereign_content.go:183` itera `order`, que é construído *a partir de* `caps` (`:168-170`),
pelo que a condição nunca pode ser verdadeira. A recusa correcta compara dois conjuntos — os turnos
conhecidos por `turn.recorded` (o `stepByTurn`, povoado em `:140-145`) contra as capturas presentes
em `caps` — e recusa os que estão no primeiro e faltam no segundo. É a mesma decisão que a EPIC-21 já
tomou («RECUSAR NOS DOIS CAMINHOS»), aplicada à **presença** e não só à **completude**. Uma
prescrição errada num relatório de auditoria é pior do que nenhuma: manda escrever um teste que passa
sem corrigir nada.

**O que caiu.** «`resume-from-step` não verifica os turnos anteriores»: o próprio `engine.go:78-87` declara
a janela e o harness corre o replay **completo** antes dos resumes. «O Event Store não é tamper-evident»:
nenhuma fonte lhe atribui essa propriedade — o ADR-010 atribui-a ao audit. «`ComPosseDeParticao` sem
call-sites»: deliberado e declarado, e o mitigante `tomarPosseDoWAL` **está** composto
(`bootstrap.go:1084`), trancando WORM e ES ao nível do SO.

### 3.6 OBS — audit WORM

**Em que sentido é WORM.** É um ficheiro normal — `O_CREATE|O_WRONLY|O_APPEND`, 0600
(`audit/filestore.go:75`) — sem object-lock, HSM ou FS append-only. A garantia é **detecção** por SHA-256
sem chave, não prevenção; o próprio `errors.go:102-107` di-lo. Isto **não é um defeito**: as
fontes exigem *tamper-evident* (`tecnica/17` §5.1), não prevenção física, e object-lock/HSM não aparecem
em nenhum DEF nem no catálogo de mitigações incompletas.

O defeito está do lado da detecção, e foi medido.

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| O-11 | **O `OpenFileStore` apaga registos válidos, fisicamente e em silêncio.** Medido numa cópia isolada: corrompidos 4 bytes do trailer de CRC do registo do meio (idx 2 de 6), `OpenFileStore` devolve **`err=nil`**, o ficheiro passa de **3528 para 1176 bytes** — quatro registos apagados do disco — `head` cai de 6 para 2, `VerifyStore` fica **verde**, e o `Append` seguinte devolve `audit_seq=3`, **reemitindo sequências já atribuídas**. Três descobertas que nenhuma leitura tinha visto: (a) o dano **cruza partições** — três partições caem juntas, porque a fronteira do dano é a posição no ficheiro, não a partição; (b) a verificação de adulteração de AOS-221 é **estruturalmente inalcançável** para este vector, porque `os.Truncate` corre **antes** de `verifyReplayedChain` — mediu-se uma mutação clássica de payload a sair verde com quatro registos apagados; (c) `FileStoreOption` tem uma única opção no pacote, logo o silêncio é propriedade da assinatura, não omissão de call-site. **Desenho ou defeito?** É a mesma linha a servir os dois casos e ela não os distingue: a recuperação de escrita truncada por crash funciona correctamente e é legítima, mas nos casos medidos o ficheiro está fisicamente **completo** e os registos seguintes estão inteiros e encadeados — e `Open` apaga-os na mesma. A heurística «pára no primeiro CRC mau» só é válida para uma cauda rasgada, porque uma cauda rasgada só pode estar no fim. Alcançável em `bootstrap.go:1144`. A âncora assinada apanha-o (medido), mas é opt-in por três variáveis que o banner declara ausentes. **Porque sobreviveu:** existe teste para o caso complementar e não para este — `aos221_worm_tamper_test.go:45-46` **recalcula o CRC** depois de mutar o payload, de propósito, para que o registo chegue a `verifyReplayedChain`; ou seja, cobre-se o caso em que a truncagem NÃO dispara e não o caso em que dispara | **A** | **hoje** |
| O-12 | **A configuração de produção que arranca é a insegura.** Medido no binário real: produção + Event Store durável + WORM vazio **arranca**, com o banner a declarar `worm=in-memory de referencia (nao-duravel)`; produção + `AOS_WORM_PATH` definido **recusa arrancar** (`ErrProductionNeedsDurableKEK`). Sem Vault externo, a única configuração de produção que arranca é a do WORM volátil. O incentivo está invertido, e `AOS_MODE=production` recusa um Event Store in-memory mas não tem guarda equivalente para o WORM | **A** | **hoje** |
| O-13 | Um `deny` cujo `RecordMediation` falha continua a negar e **incrementa** o contador — mas perde o registo durável, e `Mediate` devolve `err=nil` (`monitor.go:478`, `seq, _ :=`). Achado novo da medição: com PERMIT e sink partido faz duas tentativas e grava zero. Um WORM em baixo nega 100% das tool calls sem deixar rasto nenhum, e de fora é indistinguível de um nó ocioso. `/metrics` expõe 17 séries, **nenhuma de mediação** | M | hoje |
| O-14 | Lacunas de cobertura do trilho, na alínea que sobrevive: o registry/supply-chain audita para um `MemStore` descartável (`bootstrap.go:2803`, `modelcatalog.go:148`) — trust store e revalidações são seladas numa cadeia que ninguém lê, ninguém verifica, e que morre no shutdown. É a lacuna mais enganadora, porque o código *parece* auditado | M | hoje |

**O que caiu.** As KEK in-memory são **DEF-302** (`DEMO-GRADE`, `FECHADO-RESIDUAL`) e a guarda de produção
dispara — medida. O `/dsar/erase` sobre titular inexistente devolve 200 e sela `key_destroyed`, mas
`flow.go:204-206` documenta-o como idempotência deliberada e AOS-322 fechou o caso perigoso.

Merece registo o que está bem: serialização canónica determinística e versionada por-registo com
fail-closed para versões desconhecidas; verificação ancorada completa nos dois lados; concorrência
in-process correcta com selo+fsync sob o mesmo lock, provada com `-race`; barreira de destruição do legal
hold panic-safe; crypto-shredding que reconcilia genuinamente imutabilidade com o Art. 17; e a chave de
assinatura genuinamente fora do nó.

### 3.7 Costuras e corpus normativo

| # | Achado | Sev. | Alc. |
|---|---|---|---|
| C-01 | O `TeeSink` não é composto e `SecuredConfig` **não tem porta nenhuma** para um Event Store de mediação. `rmadapter.go:103` descarta o `Metadata` de AOS-340 justificando que «o canal está no Event Store» — onde não está. Canal inalcançável nos dois armazéns. Sobrevive por razão diferente da acusada: a severidade não vem de quem lê `tool.call.*` a jusante (ninguém lê), vem da justificação falsa que fecha a discussão | M | hoje |
| C-02 | `tecnica/13:231` afirma que o gate `event-catalog` verifica que um nome catalogado em (a) é apendado ao Event Store; o `event-catalog.py` declara essa verificação como «implementada, medida contra a árvore, e **retirada por imprecisão**». **Sobredeclaração de cobertura de gate.** O documento afirma uma verificação que o script diz em voz alta não fazer — e fá-lo na secção que serve de inventário de cobertura. Não confundir com C-01: a verificação retirada era por-pacote e teria passado sobre `platform/audit`; o buraco de C-01 é de caminho de chamada e nenhum gate o cobre | M | hoje |
| C-03 | `tecnica/14` §5.2 (o inventário de lacunas) afirma que «nenhuma variável de ambiente carrega um bundle» e que «o nó corre sempre `pdp.NewUnloaded()`»; o §4 da **mesma matriz** e o código dizem o contrário (`main.go:746,749`, `bootstrap.go:2379`). Há uma segunda instância em `tecnica/14:113`, que contradiz a ressalva nove linhas abaixo. Direcção do erro: **subdeclaração** — declara menos capacidade do que existe. Menos grave do que o inverso, mas está na secção que o §7 da própria matriz vende como a mitigação do risco «componente confundido com nó» | B/M | hoje |
| C-04 | **DEF-405 está caduca**: continua a afirmar que o binário não expõe rota de promoção, quando `promotion_api.go:221,242` implementa `POST /promote` chamando o método exacto que a entrada diz ser inalcançável, e o ficheiro-âncora (`bootstrap.go:2337`) diz «a submissao de ratificacoes DEIXOU de ser deferida». Falsa desde 2026-08-11, com 32 commits ao registo desde então — incluindo um intitulado «seis declarações caducadas deixam de mentir». **Ressalva:** o que caducou é a CARACTERIZAÇÃO, não necessariamente o estado aberto — o gatilho de saída nomeia AOS-096, que continua a montante do nó. **E não é caso único:** a `DEF-812` descreve como «o que CORRE» exactamente a peça cuja cadeia de auditoria evapora no shutdown (O-14), e a `DEF-606` invoca uma mitigação sem chamadores (§2.3). Três entradas em uso que não descrevem o estado real | M | hoje |
| C-05 | O gate `estado-citado` é vacuoso e é um *required check*: output real `0 declaracao(oes) verificada(s); 1 abstencao`, exit 0. O gate `integration` sai verde com **10 de 16** códigos de porta ausentes, todos com `owner=AOS-196` — um ticket de higiene documental já fechado que nunca teve esse âmbito. **O cruzamento que fecha o argumento:** o `estado-citado` existe para apanhar «declaração cita ticket já fechado» — que é a descrição EXACTA dessas dez entradas — e não as vê porque `scripts/ci/baseline/` está fora do seu âmbito de varredura. Um gate de coerência que não varre o sítio onde a incoerência está declarada. Provado por mutação que ambos os mecanismos funcionam (baseline vazia ⇒ o `integration` sai 1 e nomeia as dez; corpus sintético ⇒ o `estado-citado` sai 1), logo o defeito é de âmbito, não de implementação | M | hoje |
| C-06 | A EPIC-17 (Estatuto PROPOSTA) declara aberto (AOS-181 a 0/5, AOS-182 a 1/4) o mesmo trabalho que a EPIC-18 declara entregue — e está entregue, verificado no binário (`bootstrap.go:2379`, `main.go:98`, `api.go:599 sealResidency`) | B/M | hoje |
| C-07 | O contador do tripwire da Carta §6.6 corre e dá `reaberturas=1`, exit 0 — **não disparado**. Mas três secções do mesmo ficheiro descrevem ainda «código 3» e «4 FIXAs tocadas ⇒ TERIA DISPARADO»; e a definição de «reaberta» que separa 1 de 4 foi fixada a 2026-07-29 **sem a linha datada no §7 da Carta** que o próprio documento exige | M | hoje |
| C-08 | **O nó desarma sempre o cliente endurecido da única perna de egress paga.** O contrato do gateway é explícito: `model-gateway/production.go:108` — «HTTPClient é opcional. Se nil, o gateway constrói um cliente **ENDURECIDO**» — e `:118` — a allowlist de egress é «**Ignorada quando um HTTPClient é injectado**». O nó injecta um **incondicionalmente** em `cmd/aos/modelgatewaywiring.go:225`, anotado «seam de dev: delega validação de egress», **e não existe ramo não-dev**: a validação de `BaseURL` (https + allowlist) e o SSRF de AOS-223 ficam fora do caminho em qualquer configuração, `AOS_MODE=production` incluída. A classe de risco está declarada (AOS-184); que o nó *desarme activamente* um endurecimento já entregue, e o declare como *seam* de desenvolvimento num binário de produção, não está | **A** | **hoje** |
| C-09 | **O contrato de fio do executor perde a atribuição run/step.** `cmd/aos/gvisorexecutor.go:53-54` declara os campos `run_id` e `step_id`; a linha 72 preenche `RunID` com o **ID da instância** e **nunca preenche `StepID`**. O `firecrackerexecutor.go:44-45,60` tem o mesmo par. E o ID composto em `substrate/sandbox/driver_gvisor.go:62` é `"gv-"+RunID+"-"+StepID+"-"+seq`, com `-` como delimitador — que aparece dentro das duas partes: de `gv-e3-fsread-step-000001-tool-1-1` não se recupera o par sem ambiguidade. O componente externo, que é quem executa, recebe dois campos cujos nomes não correspondem ao conteúdo — e é o único sítio onde a atribuição por passo faria falta numa investigação | M | hoje (medido) |
| C-10 | **`ErrDriverUnavailable` manda o operador procurar a coisa errada.** O texto é «sem KVM/host support» (`substrate/sandbox/errors.go:10`, devolvido em `driver_gvisor.go:60`), quando o cabeçalho do próprio `cmd/aos/gvisorexecutor.go:14-17` declara em maiúsculas que o gVisor **não precisa de KVM** — é a razão de o driver existir num host sem virtualização aninhada. Quem provisiona vai procurar `/dev/kvm` quando lhe falta uma variável de ambiente | B | hoje (medido) |

**O que caiu, e é importante que tenha caído.** «Span e audit não partilham chave de correlação»:
**refutada** na forma forte — `otel-genai/semconv.go:44-47` define `aos.run_id` e `aos.step_id`, e
`audit/rmadapter.go:79-83` escreve `RunID`/`StepID` em todo `AuditRecord`; a junção existe. «O `layer-lint`
não valida a invariante do RM»: **não-requisito** — a invariante é imposta pelo compilador, a Carta §5
descarrega-a por guard-test, e nenhum documento afirma que o `layer-lint` a cobre. «ADR-010 e ADR-014 nunca
materializados»: **não-requisito** — estado declarado legítimo. «AOS-097 sem DEF a registá-lo»: erro de
categoria, o registo cobre marcadores em código, e `tecnica/14` declara a ausência em três sítios.

E a leitura «caixa por marcar = trabalho por fazer» morre com um calibrador definitivo: **a própria Carta
§5 tem os seis critérios do DoD da v1 por marcar**, incluindo um cuja linha diz «já feito». Contagem de
primeira mão: `EPIC-08` **0 de 104**; `EPIC-09` **7 de 132** marcados (125 por marcar), os sete todos de
AOS-093; `specs/00_AOS_Carta.md` **0 de 6**. Amostrados AOS-083 e AOS-087:
ambos implementados e testados, com testes que citam os critérios por número. O achado é sobre o **registo
de estado**, não sobre entrega — e a RTM já o regista como **GAP-05**, com o risco pelo nome.

---

## 4. Achados nascidos na refutação

Sete dos achados acima não existiam na passagem 1. Nasceram de refutadores a verificar o que uma lente
tinha assumido ser verdade: **O-04** (instrumentação do PDP morta), **O-11(a)(b)(c)** (o dano cruza
partições; AOS-221 inalcançável para o vector; o silêncio é da assinatura), **C-02** (sobredeclaração do
`event-catalog`), **C-08** (o `HTTPClient` que desarma o SSRF), **G-08** e **G-09**, e o §2.1 inteiro.

É a mesma lição das auditorias 11 e 12, e vale a pena não a perder: **o retorno marginal de uma nona lente
é menor do que o de um refutador**. A passagem 1 gasta a maior parte da energia a redescobrir buracos que o
`REGISTO-Deferimentos.md` e a `tecnica/14` §5.2 já declaram com eixo e dono; o que ela quase não apanha são
os defeitos **dentro** do código que corre todos os dias.

---

## 5. Remediação proposta

Priorizada por *alcançável hoje × severidade*. Não abre tickets — nomeia o que os mereceria.

**P0 — o nó pode enganar quem o opera correctamente**

1. **§2.1/§2.2** — fechar a ordem invertida. São três passos, e a medição mostrou que o primeiro é o
   barato: **(a)** dar postura ao Reference Monitor no banner, usando o `HasActiveTaintGate()` que já existe
   e não tem chamadores — é o trabalho de um dia e acaba com o silêncio; **(b)** criar a **superfície de
   configuração** que permite povoar `privileged`, que hoje não existe em nenhuma das 102 variáveis
   `AOS_*` — sem ela nem um operador convencido consegue fechar o buraco; **(c)** o ápice adoptar
   `NewProductionHardenedTaint`, que é AOS-183 fechado. Enquanto (b) não existir, **um gate que verifique
   que toda a regra `permit` do bundle traz a cláusula `context.taint != "untrusted"`** é a mitigação
   proporcional — é o que apanharia `allow_fs_read`.
2. **O-11** — distinguir cauda rasgada de dano interior no `OpenFileStore`: truncar em silêncio só é
   legítimo quando o dano está fisicamente no fim. Nos restantes casos, erro, log e métrica.
3. **O-12** — dar ao WORM a mesma guarda de produção que o Event Store já tem, e desfazer o incentivo
   invertido.
4. **C-08** — deixar o `HTTPClient` a nil sob `AOS_MODE=production`, que é tudo o que é preciso para o
   gateway construir o cliente endurecido e voltar a validar o `BaseURL` contra a allowlist.
5. **G-04** — dar capability ao plano de governação, ou exigir para `/dsar/*` a mesma cerimónia que
   `/autonomy` já exige.

**P1 — a evidência não serve para investigar**

6. **O-01/O-02** — `service.name` e `SpanKind` no serializador OTLP. Sem o primeiro, o trace não é
   atribuível; sem o segundo, a chamada ao modelo não é distinguível.
7. **O-13** — expor os contadores de mediação em `/metrics` e fazer o `/readyz` reflectir a falha de
   selagem fora das três rotas de governação.
8. **O-03** — reutilizar `spanErrorType`, que já existe.
9. **O-04** — passar o tracer ao PDP.

**P2 — o corpus diz coisas que não são verdade**

10. **C-04** e **C-02** — a entrada caduca e a sobredeclaração de gate. São a classe de defeito que os
    gates de coerência existem para apanhar e não apanham.
11. **C-03**, **C-06**, **C-07** — reconciliar `tecnica/14` §5.2, o estatuto da EPIC-17 e as três secções
    do tripwire.
12. **§2 / smoke** — dar ao smoke uma tool call realmente mediada. Enquanto não tiver, nenhum verde do
    driver diz alguma coisa sobre o caminho que o sistema existe para proteger.

**Decisão de dono, não de engenharia**

13. **§3.3** — materializar o ADR-014, ou aceitar explicitamente que a escada L0–L5 não tem base
    normativa e que a conformidade contra ela não é mensurável. A segunda opção é legítima; o que não é
    legítimo é a `tecnica/14` continuar a citar «taxonomia L0–L5 com demoção automática» como controlo do
    Art. 9 do AI Act quando o controlador não está composto (DEF-908) e a semântica não está fixada.

---

## 6. Limites desta auditoria

### 6.1 Levantados

- Gates executados com output real: `replay` (exit 0), `policy-test` (verde), `layer-lint` (exit 0, 39
  módulos), `apex` (exit 0, 19 testes, cobertura 83.5%), `estado-citado`, `integration`, e o contador do
  tripwire §6.6.
- Suites corridas: `pdp`, `autonomy`, `reference-monitor`, `otel-genai`, `platform/audit`, `identity` —
  todas verdes.
- Nó real levantado e conduzido: `driver.sh smoke` 9/9, e **30 corridas** inspeccionadas no WORM e no WAL.
- H49, H51, H52, H59 e H60 foram **executadas** em cópias isoladas fora da árvore; `git status --short` em
  `C:\Jimy\AOS` ficou sem alterações dos refutadores.
- **§2.4 — o último salto foi medido** com um executor conformante descartável, em três células
  (com bloco `sandbox` e executor; com bloco e sem executor; sem bloco e com executor), o que
  corrigiu a atribuição do `deny` do §2.2. `git status` no repositório ficou sem alterações.
- **§2.2 — o nó foi levantado com um bundle de política real carregado**, nas duas configurações (com e sem
  bundle), com os banners capturados por inteiro e **três decisões de mediação reais** obtidas e seladas no
  WORM. Enumeradas as 102 variáveis `AOS_*` que o binário lê. O bundle usado foi sempre uma cópia fora da
  árvore. Confirmou-se também que `escalate` é alcançável com bundle + `AOS_AUTONOMY_LEVELS`
  (`autonomia L1 x danger -> confirm`, run parado em `waiting_on_human`).
- **O-07 foi medido depois de escrita a primeira versão deste relatório**, no pacote `replay` (com o
  `runOriginal` e o `captureDroppingReader` do próprio repositório) e no `cmd/aos` (com `replayPlanFor`
  real). A medição confirmou a assimetria, **falsificou** a asserção sobre o log de `resume.go:264` e
  descobriu a barreira do step-ledger que baixa a severidade — ver §3.5.

### 6.2 Por levantar

- **A retoma HTTP ponta-a-ponta de O-07 não foi medida.** A assimetria foi medida ao nível dos pacotes
  `replay` e `cmd/aos`; o ciclo completo por HTTP exige um Model Gateway real sob a build-tag `aoslive`,
  indisponível aqui — e o próprio repositório declara em `aos292_retoma_live_test.go:22-26` que as fixtures
  do pacote não têm capturas.
- **O `/readyz` com WORM só-de-leitura a meio de um run não foi medido** (§1.2, ponto 4). No arranque o nó
  recusa — essa metade foi medida e cai.
- **Nada foi corrido contra um colector OTLP real.** As conclusões sobre o que chega ao backend são
  derivadas do serializador lido por inteiro, não de captura de wire.
- **Nada foi corrido contra um provider de modelo real.** As três decisões de mediação de §2.2 usaram um
  gateway OpenAI-compatible construído fora da árvore. O caminho é o do nó; o interlocutor não é.
- **Nada a jusante do POST ao executor foi medido** — isolamento, `runsc`, interposição de syscalls.
  O alvo do §2.4 é um gravador conformante, não gVisor. Medir o isolamento real continua a exigir um
  host Linux com o componente `deploy/server/gvisor/` provisionado.
- **Não foi exercida uma tool `cap:http.post` com bloco `sandbox`.** É a célula que mostraria se a
  fronteira continua *governada* depois de alcançável, e não só alcançável. Nem a postura
  `AOS_MODE=production`, que muda a eleição do driver.
- **`ux-dx.sh`, `package.sh` e `evalgate.sh` não foram corridos** (docker / `-race` multi-módulo); nesses,
  leu-se o script e o workflow.
- Fora de âmbito por desenho: `platform/memory`, `platform/registry`, `platform/broker`,
  `control-plane/budget` e `control-plane/scheduler` — cobertos pelas auditorias 10 e 11.

---

## 7. Saldo

| Veredicto | Nº |
|---|---:|
| **Defeito** (sobrevive) | **30** |
| — dos quais **alcançáveis hoje** | **19** |
| — dos quais **alta severidade** | **6** |
| Deferimento declarado | 13 |
| Não-requisito | 11 |
| Refutada | 21 |
| **Achados novos, nascidos na refutação** | **7** |

Das hipóteses classificadas CRÍTICA pela passagem 1, **nenhuma** sobreviveu com essa severidade. Dos dois
«críticos» do eixo da autonomia, ambos caíram como não-requisito — não porque a implementação esteja certa,
mas porque não há texto com autoridade que diga o que estaria certo. E os seis achados de alta severidade
que ficaram de pé dividem-se em dois grupos exactos: três estão **dentro** de código que corre todos os
dias e nenhuma lente vertical os viu (O-11, O-12, C-08), e três são **costuras** entre subsistemas que cada
lente dava por garantidas (§2.1, G-04, O-01/O-02).

**Uma ressalva que enfraquece o próprio saldo, e que só apareceu na redacção dos tickets.** As treze
hipóteses classificadas «deferimento declarado» foram-no contra o `REGISTO-Deferimentos.md`. Três
entradas em uso desse registo **não descrevem o estado real**: `DEF-405` está caduca (C-04),
`DEF-812` descreve como «o que CORRE» a cadeia que evapora no shutdown (O-14), e `DEF-606` invoca uma
mitigação que não tem chamadores (§2.3). Um registo que é a autoridade para classificar dívida, e que
erra em três das entradas que esta auditoria consultou, torna a classificação «declarado» mais fraca
do que este relatório a tratou. Não recontei o saldo — não tenho base para o fazer sem reauditar as
treze —, mas quem o ler deve saber que o denominador não é sólido.

O resultado mais transferível deste relatório não é nenhum dos trinta. É a razão pela qual eles
sobreviveram: **este repositório verifica com um rigor invulgar tudo o que declara, e quase não exercita o
caminho que a declaração protege.** O smoke não medeia, o gate de replay não lê as suas próprias âncoras, o
gate que apanharia o canal de eventos em falta foi retirado, e o `/metrics` não expõe um único contador de
mediação. Um sistema de governação cuja evidência de funcionamento não passa pelo acto de governar tem
gates que medem a sua própria disciplina, não a sua propriedade.

---

*Auditoria adversarial multiagente — oito lentes, oito refutadores, uma passagem experimental. Ver
[Índice das análises](INDICE.md), [Índice Técnico](../tecnica/INDICE.md) e [Índice do Backlog](../specs/INDICE.md).*
