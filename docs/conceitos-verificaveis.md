# Conceitos verificáveis do AOS

Inventário de **tudo o que este sistema afirma** e, para cada afirmação, **como se verifica** e
**em que estado está**. Levantado em 2026-08-18 contra o nó em produção (`37.60.241.150`), o
banner de arranque, e os 674 ficheiros de teste do repositório.

## A regra que decide o que entra aqui

> **Um teste que "passa" sem exercitar a propriedade é uma FALHA.** (AOS-169)

Um conceito só conta como *verificável* quando existe um **controlo**: uma variante do mesmo
exercício que **teria de falhar**. Sem isso, um resultado verde é compatível com "o mecanismo
aceita tudo", e não distingue funcionar de estar desligado.

É a diferença entre "li o run e deu `200`" e "li o run e deu `200`, **e** o mesmo pedido sem
credencial deu `404`". Só a segunda diz alguma coisa.

## Legenda de estado

| | Significado |
|---|---|
| ✅ | **Provado em produção** — exercido no nó real, com controlo |
| 🧪 | **Provado por teste, ou em clone restaurado** — o mecanismo está exercido com controlos, mas não no nó que serve |
| ⚠️ | **Armado, não exercido** — o mecanismo está ligado e nunca disparou |
| 💤 | **Dormente** — desligado por configuração, declarado no banner |
| ❌ | **Ausente** — lacuna declarada |

---

## A. Identidade e não-forjabilidade

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Modo de identidade real endurecido** (AOS-156) — nenhuma chave de assinatura entra no runtime do nó | Banner de arranque: `trust-anchor-only`. Controlo: só a *pubkey* entra na cadeia; código comprometido in-process não tem material com que cunhar | ✅ |
| **Raiz humana da delegação AUTORIZADA** (ADR-003) — o humano autorizou *esta* delegação, não apenas "esteve presente" | `nonce` = digest de (agente, classe, caps, TTL); o issuer calcula-o das flags que cunha. Controlo: token do mesmo humano para outra delegação → **recusado** (`delegationbinding_test.go`) | ✅ |
| **Rótulo honesto do método** — `manual` / `oidc:` / `oidc-bound:` | O rótulo desce quando a ligação não é possível (`--assertion-unbound`), e fica escrito no registo de binding | ✅ |
| **Autoridade de escopo** (AOS-071) — o escopo efectivo é o token **intersectado** com o directório externo | Banner: 4 sujeitos, revisão 1, fingerprint. Revogar = listar com `"capabilities": []` | 🧪 |
| **Anti-replay por `jti`** — um ID-token não vale duas vezes | Mesma leitura com o mesmo token: `200` depois `404` | ✅ |
| **Audiências separadas** — um token para *ler* não serve para *cunhar* | Clientes `aos-node` e `aos-issuer` distintos; o issuer verifica contra o seu | ✅ |
| **Tecto de idade da asserção** (5 min) | Token dentro do `exp` mas fora do tecto → recusado; o mesmo token dentro do tecto → aceite (controlo) | 🧪 |

> ❗ **Limite declarado:** o nó **não verifica** a autenticação humana — confia no issuer. A âncora
> do nó é a pubkey, e `auth_method` é uma afirmação *dele*. Com o issuer comprometido,
> `oidc-bound:` é tão forjável como `manual`.

---

## B. Soberania de leitura

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Read-path soberano fail-closed** (AOS-172/205, D7) — o `board` vem das *claims*, não de um header | `X-Aos-Board: board:prod` forjado **sem token** → `404`. É o ponto todo | ✅ |
| **Residência do run na criação** (AOS-182) — a região sela-se na submissão | Selo `gov.read/<run>` com `Resource.Region` | ✅ |
| **`404` uniforme e não-enumerável** — "não existe" e "existe noutra soberania" são indistinguíveis | Sem credencial, credencial inválida e outra região dão **todos** `404` | ✅ |
| **Soberania POR-LEITOR** — cada leitor traz a sua fronteira | Mesma cadeia de hash, mesma capability, principals distintos: service account (`91a30a69-…`) e humano (`a2b5947c-…`) | ✅ |
| **Recusa cross-region** | Leitor de outra região, token igualmente válido → `404` | ✅ |

> ❗ **Por exercer:** há **um só board** (`board:prod=eu-west`). A recusa cross-region está provada,
> mas não é reproduzível hoje com leitores humanos — exige uma segunda região no mapa.

---

## C. Mediação de política

| Conceito | Como se verifica | Estado |
|---|---|---|
| **PDP com bundle carregado** (AOS-220) — *trust anchor* forçado out-of-band, **não** lido do bundle | Banner: política `1.0.0`, `AOS_POLICY_TRUST_ANCHOR` | ✅ |
| **ScopeGate** — capability fora do escopo é negada | Teste negativo com capability ausente → deny | ✅ |
| **Taint gate** — conteúdo `untrusted` restringe o efeito seguinte | `Context.Taint: untrusted` nos selos | ✅ |
| **O que o PDP autoriza é o que o sandbox executa** (A1) | Antes: o PDP autorizava `doc://notes` constante enquanto o sandbox lia o argumento. Corrigido com *slots* `{arg}`; **não opt-in** — um efeito parametrizado com `resource_value` constante **aborta o arranque** | ✅ |
| **Sem bypass estrutural** — as tools só entram pelo `MediatedLauncher` | Banner: `args→ExecRequest pelo EffectRewriter` | ✅ |

---

## D. Execução isolada

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Sandbox gVisor** (AOS-005/064) | `Linux version 4.19.0-gvisor` **dentro** do sandbox; `runsc` systrap; bundle OCI efémero por chamada | ✅ |
| **Tool set congelado** (AOS-155) — o conjunto de tools de um run não muda a meio | Persiste no event store com o run | 🧪 |

---

## E. Auditabilidade

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Tamper-evidence do WORM** (AOS-221) — hash-chain **re-encadeada** no arranque, não só CRC de enquadramento | Uma cadeia adulterada **aborta** o arranque. 108 partições re-encadeadas | ✅ |
| **Selo de leitura sensível** (D6) — o desfecho não se serve sem o registo | O selo é **pré-condição**: se o WORM não selar, nega | ✅ |
| **Selo de acções de controlo** (A3) — `pause`/`steer`/`approve` na partição `governance.control` | Antes ficavam só no Event Store, e o four-eyes não registava *quem* aprovou | ✅ |
| **Audit do model gateway** (AOS-265) — WORM próprio em disco | `/var/lib/aos/model-audit.wal`; sobrevive a restart | ✅ |
| **Verificação ancorada** (AOS-268/072) — fecharia truncatura do tail e reescrita desde a génese | Exige um selador out-of-process (**DEF-268**) e um checkpoint **por partição**: hoje ancoraria 1 em 108 | ❌ |

> 🔍 **Nota de método:** contar partições com `grep -ao` sobre o `worm.wal` devolve **69** e está
> errado — o WAL é binário enquadrado e o `grep` processa-o por linhas. `strings -n 8` devolve
> **108**, que fecha com o banner. O erro era plausível e silencioso; só apareceu por confrontar a
> contagem com o que o próprio nó declara.

---

## F. Privacidade e DSAR

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Redacção de PII no fecho transitivo** (AOS-091/208) | O objectivo é redigido **antes** do Event Store, memory, spans e audit — mesmo ingestor em todas as portas | ✅ |
| **Cifra por-titular do conteúdo** (AOS-093) — envelope DEK/KEK antes de tocar o Event Store | 65 ligações titular→partição repostas no arranque | ✅ |
| **Crypto-shredding** (Art. 17) — destruir a KEK torna o conteúdo irrecuperável | Exercido sobre um titular descartável; `key_destroyed` selado sem PII | ✅ |
| **Legal hold bloqueia o shred** (AOS-213) | `/dsar/hold` → `/dsar/erase` recusa | ✅ |
| **Expiração por TTL** (AOS-092/213) | `ExpirationJob` + scheduler interno (AOS-267) | ✅ |
| **Custódia da KEK externa** (AOS-215) | Vault Transit; as KEK vivem **fora** do processo | ✅ |

> ❗ **Granularidade declarada:** o TTL é por-registo/classe, mas o crypto-shred é **por-chave-de-
> titular** — logo a expiração é por-titular. Retenção diferencial por classe dentro de um titular
> não é alcançável por este mecanismo.

---

## G. Governação humana

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Canal de controlo Ed25519** (AOS-160) | 1 operador registado; HMAC demo **desligado** | ✅ |
| **Four-eyes** (AOS-162) — a cerimónia inteira, não só os aprovadores pinados | 2 aprovadores pinados por ficheiro. **Exercitada num clone restaurado:** *challenges* distintos por aprovador, duas pernas assinadas com sessões e credenciais distintas, `authorized` nomeando ambos + `grant_id`. **Controlo:** a primeira tentativa foi **recusada** (`403`) por o `risk_class` do pedido divergir do assinado nas pernas — a assinatura cobre o tuplo canónico, e apanhou-o | 🧪 |
| **Frescura por-cerimónia** (AOS-266) | `POST /runs/{id}/challenge` emite por (pedido, aprovador), TTL 5 min, *issue-then-consume*. Passou de `501` a `200` com challenges distintos | ✅ |
| **Ratificação de promoção** (AOS-159/206/275) — assinatura produzida **fora** do nó | `aos-issuer ratify-sign`; freshness + nonce store durável forçados | 🧪 |
| **Attestation de dispositivo** (AOS-177) | Sem `AOS_ATTESTATION_VERIFIER_URL`, o four-eyes é **só estrutural** — não prova modelo nem posse | 💤 |
| **Atribuição dispositivo↔aprovador** (AOS-266) | Sem dispositivos registados, a attestation não prova **de quem** é o autenticador | 💤 |
| **Rota de autonomia assinada e selada** (AOS-087) — mudar níveis deixou de ser editar um ficheiro no servidor | `POST /autonomy` com assinatura ed25519 sobre o payload canónico (agente‖domínio‖nível‖motivo), *nonce* de uso único durável, motivo **obrigatório**. O `actor` selado vem do **emissor verificado**, nunca do corpo. **Controlos:** emissor não registado → `403`; mesmo *nonce* duas vezes → `403`; assinatura de `L1` reapresentada como `L5` → `403`; o selo nomeia quem assinou e guarda o motivo; `GET` reflecte o `POST` | 🧪 |
| **Piso de autonomia DECLARADO** (`AOS_AUTONOMY_DEFAULT`) — um par ausente cai no que alguém escolheu, não no valor-zero | `WithDefaultLevel`. **Controlos:** com piso L1, par desconhecido dá **L1**; **sem** piso continua **L0** (fail-closed inalterado); um registo explícito **ganha** ao piso; piso fora do domínio é ignorado e fica em L0 | 🧪 |
| **Resolução em cascata por CLASSE** (`class:`) — instância → classe → piso | `LevelForAgentOrClass`. **Controlos:** a instância ganha à classe quando ambas existem; a classe aplica-se a um agente **nunca visto**; classe vazia não herda nada; quem só usa `LevelFor` não é afectado | 🧪 |
| **Ensaio antes de virar o interruptor** — `POST /autonomy/simular` | Relê os selos de mediação do WORM e re-classifica com o **mesmo** classificador do nó, num registo efémero **sem sink**. **Controlos:** a simulação e o classificador real **concordam** para os mesmos factos; usa o **parser do arranque** (recusa o que o nó recusaria); **não sela** — o ensaio não contamina o trilho. Limite declarado na resposta: avalia **só** o overlay de autonomia | 🧪 |
| **Autonomia / escalate** (AOS-087) | **LIGADO em produção desde 2026-08-19 00:03** (banner: `ORACULO LIGADO`) — e a configuração **não tem efeito**: a única regra é `agt-rotina-01:fs=L4`, uma INSTÂNCIA que nunca correu (aparece só no selo de provisionamento; os agentes reais são outros, e os `agent_id` são cunhados por run). Todo o run real cai no piso **L0** ⇒ **tudo escala**. Nenhum run foi submetido desde então, pelo que o WORM não tem UM ÚNICO `escalate` — ninguém observou a diferença. **O mecanismo está provado** num clone restaurado (agente não registado ⇒ `waiting_on_human` com o `resource_value` real). Fica **por provar no nó que serve** | ⚠️ |

---

## H. Orçamento e disjuntores

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Orçamento composto** (AOS-008) — cobre tool calls **e** o turno de modelo, em tokens | Reserva antes da inferência, saldo pelo consumo medido. Verificado forçando o tecto a 400 tokens → suspensão | ✅ |
| **Esgotamento = degradação declarada**, nunca deny-loop | Suspende em `waiting_on_human`, retomável; ou sela `timed_out`/`budget_exhausted` | 🧪 |
| **Replay não re-reserva** | Dedup por `run_id:step_id` | 🧪 |
| **Burn-down** (AOS-261/262) — aviso aos 80% | Lê o ledger durável; fail-closed se o ledger somou zero | ⚠️ |
| **Tecto em dólares recusado sem fonte de preço** | Definir `AOS_BUDGET_MAX_COST_MICRO_USD` sem preço na tabela **aborta o arranque** | 🧪 |
| **Custo do modelo** (AOS-259) | Canal ligado, **fonte ausente**. E a chave da tabela é o **alias** `(gpt-4o-mini, eu)`, não o modelo real (`kimi-for-coding`) — pôr aí o preço do gpt-4o-mini daria um custo numericamente preciso e factualmente falso. Ver §"o modelo que o nó nomeia não é o modelo que corre" | ⚠️ |

> ❗ **Assimetria declarada:** o tecto é por-run **e por-incarnação**, e o aviso é cumulativo. Um run
> em ciclo de escalada/retoma pode gastar até N × tecto. Fechar isto exige estado de orçamento
> durável por run, que este nó não tem.

---

## I. Durabilidade e retoma

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Execução durável** (AOS-180) | Checkpointer + capturer + step-ledger sobre o event store em disco | ✅ |
| **Retoma após crash** (AOS-253) | Runs interrompidos re-hospedados no arranque | ✅ |
| **Re-varredura periódica de órfãos** (A4) | Antes: um nó só precisava de **um segundo restart**. A re-varredura respeita a *lease* — reclamar cedo causaria dupla execução | ✅ |
| **`run_id` duplicado não perde a submissão** (A2) | Antes devolvia `201` e descartava. Agora `409` — mas **só** a quem conseguiria ler o run: um `409` a um chamador de outra região revelaria existência que o `GET` esconde | ✅ |
| **Fencing / lease** (AOS-222) | Só o detentor da lease escreve | 🧪 |
| **Saga / compensação** (AOS-254) | Efeitos irreversíveis compensados por passo | 🧪 |

---

## J. Transporte e admissão

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Ingresso / admission** (AOS-166/277) | 64 pedidos/s, burst 128, máx. 512 runs em curso | ✅ |
| **Terminação TLS a montante** (AOS-209) | **Declarada**: o nó serve em texto-claro por decisão de quem o configurou; a cifra depende do ingress | ⚠️ |
| **Classificação de PLANO por rota** — as barreiras derivam do registo, não da memória de quem escreve | Tabela em `packages/cmd/aos/planos.go`: 3 abertas, 4 de dados, 4 de governação, 10 de controlo. O **valor-zero é inválido** e **aborta o arranque** (`ErrRotaPorClassificar`). **Controlos (por mutação, todos caem):** mTLS fora do ramo de controlo; balde fora do ramo de controlo; balde fora do ramo de governação; rota despromovida de plano; rota registada fora de `registar` | 🧪 |
| **DSAR sem barreira de TRANSPORTE** — as 4 rotas de governação autenticam-se por asserção OIDC verificada mas **não** exigem certificado de cliente | É a única superfície de governação sem mTLS, e inclui o `/dsar/erase` (crypto-shred, que nenhum restore drill desfaz). **Decisão em aberto, escrita** em `barreirasDe` e no README: promovê-la obriga a emitir PKI de cliente a operadores DSAR — a provisão que o DEF-012 defere para fora do nó | ❌ |
| **PKCE S256 obrigatório** | Sem `code_challenge` → `Missing parameter`; com `plain` → `not matching the configured one` | ✅ |
| **`redirect_uri` registado** | Porta não registada → `400` | ✅ |

> ❗ **Ordem declarada:** a admissão actua **antes** da autenticação e o balde é global. 500 pedidos
> sem credencial produziram 442 `403` + 58 `429` — um chamador anónimo consome orçamento de
> admissão de todos. É *tradeoff* nomeado no banner, agravado por não haver firewall no host.

---

## K. Observabilidade

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Tracer OTLP real** (AOS-173) | Spans `invoke_agent`/`chat`/`execute_tool`/`freeze` + selos WORM | ✅ |
| **Autenticação OTLP** (DEF-012) | O canal é `http://otel:4318`, **em claro** na rede do compose. Um *bearer* aí seria teatro — viaja em claro na mesma rede de onde vem a ameaça. O que autentica é **mTLS**: variante pronta em [`otel-collector-mtls.yaml`](../deploy/server/otel-collector-mtls.yaml), **provada** num coletor descartável (sem cert de cliente → handshake recusado; com o do nó → `200`; HTTP claro → `400`). Por activar — o exportador é *fail-open* e uma má configuração pára os spans **em silêncio**. | 💤 |

---

## L. Custódia de segredos

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Credencial do modelo por ficheiro montado** | `AOS_MODEL_API_KEY_PATH`, 0400 | ✅ |
| **Chave do issuer fora do servidor** | `issuer.key` vive na máquina do operador; só a pubkey chega ao nó | ✅ |
| **ACL do material de chave** | Estava `Utilizadores:(RX)` + `Utilizadores Autenticados:(M)` — legível e **modificável** por qualquer utilizador autenticado. Hoje ACE único do dono, **verificado pelo uso** (mint + SSH) | ✅ |
| **Credential broker** (AOS-070) | **Ausente**: as credenciais downstream entram por ficheiro montado, não por troca mediada | ❌ |
| **Broker vault** (AOS-070/264) | Sem `AOS_BROKER_VAULT_ADDR` | 💤 |
| **Selo do Vault** | Auto-unseal com a chave no host; um selo a sério exige KMS/HSM externo | ❌ |

---

## M. Operação

| Conceito | Como se verifica | Estado |
|---|---|---|
| **Backups cifrados** | PKCS#7 para um certificado cuja privada **nunca esteve no servidor** | ✅ |
| **Recolha off-host** | Tarefa diária `StartWhenAvailable`; verificado **decifrando** e confirmando a chave Transit lá dentro | ✅ |
| **Reversão por digest** | `rollback.sh` com digest da imagem | ✅ |
| **O backup levanta o sistema** | Não é o mesmo que "o backup decifra". Ensaio completo do artefacto: Vault desselado com a chave de dentro do backup, WORM re-encadeado (108 partições), `idp-db.sql` restaurado (87 tabelas), Keycloak sobre ele, e **token do IdP restaurado a ler no nó restaurado** → `200`. Controlos no sistema restaurado: sem credencial `404`, header forjado `404`, replay do `jti` `404` | ✅ |
| **Alerta de recência do backup** | Verifica a idade dos **dois** lados: a cópia local (apanha "a máquina esteve desligada") e o backup **remoto** (apanha "o cron do servidor morreu" — o caso invisível, porque a recolha continua a correr sem erro e a dizer `0 novo(s)`). Alerta por código de saída, `ESTADO.txt` e Registo de Eventos. Controlos: tecto de 1h → saída `3`; servidor inalcançável → saída `2`; normal → `0` | ✅ |

---

## O que este inventário mostra

**Provado em produção com controlo:** os eixos que carregam o argumento do sistema — soberania de
leitura por-leitor, mediação de política, isolamento, auditabilidade da cadeia, DSAR, e a raiz da
delegação.

**Dormente por configuração, não por defeito:** attestation de dispositivo, atribuição
dispositivo↔aprovador, autonomia/escalate, broker vault, autenticação OTLP. Cada um está nomeado
no banner de arranque com o que perde por estar desligado — o nó nunca finge.

**Ausente e declarado:** verificação ancorada do WORM, credential broker, selo do Vault, alerta de
recência do backup.

O padrão que se repete em todos os achados desta auditoria — A1 a A4, a contagem de partições, a
âncora do WORM — é o mesmo: **a coisa parecia funcionar**. O que a distinguiu de funcionar foi
sempre um controlo que teria de falhar, e falhou.

---

## Contagem

**72 conceitos** em 13 eixos:

| Estado | Nº |
|---|---|
| ✅ Provado em produção | 45 |
| 🧪 Provado por teste (ou em clone restaurado) | 15 |
| ⚠️ Armado, não exercido | 4 |
| 💤 Dormente por configuração | 4 |
| ❌ Ausente e declarado | 4 |

Os **12 que não estão provados** (⚠️ + 💤 + ❌) estão todos nomeados acima, e cada um diz o que
perde por não estar. Nenhum é uma surpresa que apareça em produção: ou está no banner de arranque
do nó, ou em §"O que continua por fechar" do
[`deploy/server/README.md`](../deploy/server/README.md).

> 🔍 **Segunda nota de método, pela mesma razão que a primeira.** A contagem inicial dizia **70 /
> 15**. Contava as linhas da própria **legenda** como conceitos — cinco linhas com o mesmo formato
> `| símbolo | texto |`. Contar só as linhas que começam por `| **` (o negrito do nome do
> conceito) dá **65 / 12**.
>
> **Terceira, em 2026-08-19.** Ao acrescentar os conceitos da autonomia e dos planos de rota, o
> mesmo `grep` deu **73** — e as parcelas somavam **72**. A linha a mais era `| **1.ª tentativa de
> aprovar** |`, um passo de uma adenda que começa como um conceito e não é um. O critério correcto
> é **começar por `| **` E terminar num símbolo de estado**; só o segundo troço distingue um
> conceito de uma linha de tabela qualquer. Três contagens erradas, sempre por medir o formato em
> vez da coisa — e as três apanhadas por cruzar o total com a soma das parcelas.

> Duas contagens erradas no mesmo dia, ambas por medir o artefacto em vez da coisa: um `grep` de
> aspas num ficheiro binário, e um `grep` de símbolos numa tabela que também os usa para se
> explicar a si própria. O que as apanhou foi a mesma coisa — cruzar o número com uma fonte
> independente.

---

## Adenda: o modelo que o nó nomeia não é o modelo que corre

Levantado ao investigar o item do custo (AOS-259), e é maior do que o custo.

O nó pede `gpt-4o-mini`. Esse nome é um **alias governado** — tem de constar da allowlist
assinada e embebida do gateway — e o LiteLLM traduz-o para o modelo real:

| Alias que o nó pede | Modelo que corre | Onde |
|---|---|---|
| `gpt-4o-mini` | `openai/kimi-for-coding` | `api.kimi.com/coding/v1` |
| `gpt-4o` | `openai/k3` | `api.kimi.com/coding/v1` |

A separação é **deliberada e boa**: o nó governa *que nomes podem ser pedidos* (fronteira
imutável sem re-assinar), e o ficheiro de encaminhamento decide *o que eles significam*. Trocar de
provider não toca no nó.

Mas tem uma consequência que muda o item do custo. A tabela de preços é chaveada pelo par
**(alias, região)** — `(gpt-4o-mini, eu)`. Pôr aí o preço do `gpt-4o-mini` da OpenAI produziria um
custo **numericamente preciso e factualmente falso**, porque quem responde é o Kimi. Hoje o custo
é *zero por ausência de dados*, e isso é honesto; um preço errado seria pior do que nenhum, porque
teria a aparência de informação.

O que fecha o item, portanto, não é "acrescentar uma linha à tabela": é a tarifa **do Kimi**, e a
consciência de que a chave da tabela é o alias.

---

## Adenda: o *bridge* de aprovação humana, exercitado pela primeira vez

O ponto mais consequente do inventário era este: o oráculo de autonomia não está ligado, **logo
nenhum veredicto `escalate` é emitido, logo o four-eyes e todo o caminho de aprovação humana são
inalcançáveis**. Dois aprovadores pinados que nunca seriam chamados.

Ligá-lo em produção muda comportamento a sério — sem *wildcard*, um par (agente, domínio) não
registado resolve para L0 e **escala**. Em vez de pedir essa decisão, exercitei-o no clone
restaurado, contra dados reais:

| Passo | Resultado |
|---|---|
| `AOS_AUTONOMY_LEVELS` ligado no clone | banner passa a `ORACULO LIGADO`, o par selado na partição `autonomy` |
| Run com agente **não** registado | `waiting_on_human`, pendência com `resource_value: doc://notes` |
| `POST /challenge` por aprovador | challenges **distintos** para `human:alice` e `human:bob` |
| Duas pernas assinadas | sessões e credenciais distintas |
| **1.ª tentativa de aprovar** | **`403` recusada** |
| 2.ª tentativa | `authorized`, `approvers:[alice,bob]`, `grant_id` |
| `POST /resume` sem corpo | `400` — a credencial original **não é persistida** |
| `POST /resume` com credencial fresca | `202 resumed` |
| Estado final | `completed`, com o conteúdo real do documento |
| Selo em `governance.control` | `Capability: control:approve`, `four_eyes.approvers: [alice, bob]`, e o `grant_id` |

**A linha que faz isto valer é a do `403`.** A primeira tentativa foi recusada porque enviei
`risk_class: 2` no pedido enquanto as pernas tinham sido assinadas com `danger` — que é `0`, não
`2` (`ClassDanger = iota`). A assinatura cobre o **tuplo canónico**, e apanhou a divergência. Sem
essa recusa, o `authorized` seguinte seria compatível com "o gate carimba o que lhe derem".

E a pendência mostra `resource_value: doc://notes` — o argumento real, não a constante. É a
correcção A1 a aparecer no sítio onde um humano a lê antes de decidir.

> **O que continua verdade:** em produção isto está **desligado**. O que mudou é que deixou de ser
> "não sabemos se funciona" e passou a ser "sabemos que funciona, e não está ligado". Ligá-lo é
> uma decisão de operação, e o preço está nomeado: todo o agente não listado passa a escalar.
