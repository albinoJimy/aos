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
| **Modo de identidade real endurecido** (AOS-156) — nenhuma chave de assinatura entra no runtime do nó | **Imposto, não deduzido.** `AOS_MODE=production` exige a âncora (`ErrProductionNeedsHardenedIdentity`, main.go:519); com a âncora activa, `IssuerSigningKey` **ou** `IssuerKeyPath` fazem o nó **recusar arrancar** (`ErrConflictingIssuerKey`, bootstrap.go:778) — carregar a chave de disco para o processo derrotaria a propriedade tanto como recebê-la em memória. **Controlos:** os dois ramos abortam (`bootstrap_test.go:396`, `durable_test.go:100`); e o modo de REFERÊNCIA aceita a mesma `IssuerKeyPath` sem se queixar (dezenas de testes), que é o controlo simétrico — a recusa é da âncora, não de tudo. Em produção, o processo tem `AOS_ISSUER_PUBKEY` e **nenhum** `AOS_ISSUER_KEY_PATH` (verificado em `/proc/<pid>/environ`) | ✅ |
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
| **Residência do run na criação** (AOS-182) — a região sela-se na submissão | Selo na partiÃ§Ã£o `gov.residency/<run>` com `Resource.Region` (`readResidencyPartition`, sovereignty.go). **NÃ£o** Ã© `gov.read/<run>`: essa Ã© a partiÃ§Ã£o do selo de LEITURA sensÃ­vel (D6), e o cÃ³digo namespaceia-as separadamente de propÃ³sito | ✅ |
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
| **Sem bypass estrutural** — as tools só entram pelo caminho mediado | **Gate sobre o repositório INTEIRO** (`TestArchLint_NenhumBypassNoRepositorio`, `AnalyzeTree`): 166 pacotes, e **invocação directa de `ToolFunc` = ZERO** — é essa a metade que importa, porque `ToolFunc` é *exportado* e uma chamada directa executa o efeito **sem PDP, sem orçamento e sem selo**. Restam 3 colisões de NOME (`dispatch` da CLI, método do Scheduler, o `m.dispatch` do próprio RM), cada uma listada **com a razão**, e uma excepção que deixe de reproduzir **falha** o teste — a lista não pode apodrecer. **Controlos (mutação):** bypass real noutro pacote → cai; `dispatchTool` num pacote não listado → cai; excepção obsoleta → cai. Imposto em CI via `require_tests` (fail-closed contra passagem vacuosa) | ✅ |

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
| **Verificação ancorada** (AOS-268/072) — fecha a truncatura do tail e a reescrita desde a génese, que o re-encadeamento sem chave NÃO apanha | **MECANISMO COMPLETO desde 2026-08-20; por exercer no nó que serve.** Esta linha dizia que «hoje ancoraria 1 em 108» — ancorava **zero**: o nó sabia CONSUMIR a âncora e nada no repositório a PRODUZIA, pelo que as três envs nunca podiam ser preenchidas. Entregue `aos-issuer worm-seal` (sela out-of-process, chave privada fora do nó, contra a cópia do backup) e a ancoragem **multi-partição** no consumo. **Controlos:** ciclo fechado (o que o selador assina é o que `VerifyFromCheckpointAtHead` — a função do arranque — aceita) e o simétrico (outra chave não valida); selar sobre um WORM que RECUOU é recusado (`--anterior`); toda a partição ancorada TEM de trazer piso; **todos** os checkpoints são verificados, não só o primeiro. **Limites declarados:** a âncora prova que a cadeia não mudou DESDE a selagem, não que era honesta antes; e a cobertura NUNCA é completa por desenho — as partições nascem por run, pelo que o banner declara `N de M` e selar mais vezes encolhe a janela sem a fechar. Falta a **cadência** (DEF-268) e exercê-la em produção | 🧪 |

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
| **Four-eyes** (AOS-162) — a cerimónia inteira, não só os aprovadores pinados | **PROVADO NO NÓ QUE SERVE, com controlo, em 2026-08-19.** `POST /challenge` emitiu challenges **distintos** por aprovador; duas pernas assinadas fora do nó (`aos-issuer approve-sign`) com aprovador, sessão e credencial distintos ⇒ `authorized`, `approvers:[alice,bob]`, `grant_id`; `POST /resume` com credencial fresca fez a acção **antes escalada** correr até `complete`. Selado em `governance.control` com `Capability: control:approve`, `NHIID: "human:alice,human:bob"` e as obrigações `four_eyes.approvers` / `four_eyes.grant`; o stream `gov.approvals` regista `pending → expired → granted → consumed`. **Controlo:** as MESMAS duas pernas com o mesmo aprovador nos dois lados — sessões, credenciais e challenges todos distintos, muda só a identidade — deram **`403`**. É a única variável alterada | ✅ |
| **Frescura por-cerimónia** (AOS-266) | `POST /runs/{id}/challenge` emite por (pedido, aprovador), TTL 5 min, *issue-then-consume*. Passou de `501` a `200` com challenges distintos | ✅ |
| **Ratificação de promoção** (AOS-159/206/275) — assinatura produzida **fora** do nó | `aos-issuer ratify-sign`; freshness + nonce store durável forçados | 🧪 |
| **Attestation de dispositivo** (AOS-177) | Sem `AOS_ATTESTATION_VERIFIER_URL`, o four-eyes é **só estrutural** — não prova modelo nem posse | 💤 |
| **Atribuição dispositivo↔aprovador** (AOS-266) | Sem dispositivos registados, a attestation não prova **de quem** é o autenticador | 💤 |
| **Rota de autonomia assinada e selada** (AOS-087) — mudar níveis deixou de ser editar um ficheiro no servidor | `POST /autonomy` com assinatura ed25519 sobre o payload canónico (agente‖domínio‖nível‖motivo), *nonce* de uso único durável, motivo **obrigatório**. O `actor` selado vem do **emissor verificado**, nunca do corpo. **Controlos:** emissor não registado → `403`; mesmo *nonce* duas vezes → `403`; assinatura de `L1` reapresentada como `L5` → `403`; o selo nomeia quem assinou e guarda o motivo; `GET` reflecte o `POST` | 🧪 |
| **Piso de autonomia DECLARADO** (`AOS_AUTONOMY_DEFAULT`) — um par ausente cai no que alguém escolheu, não no valor-zero | `WithDefaultLevel`. **Controlos:** com piso L1, par desconhecido dá **L1**; **sem** piso continua **L0** (fail-closed inalterado); um registo explícito **ganha** ao piso; piso fora do domínio é ignorado e fica em L0 | 🧪 |
| **Resolução em cascata por CLASSE** (`class:`) — instância → classe → piso | `LevelForAgentOrClass`. **Controlos:** a instância ganha à classe quando ambas existem; a classe aplica-se a um agente **nunca visto**; classe vazia não herda nada; quem só usa `LevelFor` não é afectado | 🧪 |
| **Ensaio antes de virar o interruptor** — `POST /autonomy/simular` | Relê os selos de mediação do WORM e re-classifica com o **mesmo** classificador do nó, num registo efémero **sem sink**. **Controlos:** a simulação e o classificador real **concordam** para os mesmos factos; usa o **parser do arranque** (recusa o que o nó recusaria); **não sela** — o ensaio não contamina o trilho. Limite declarado na resposta: avalia **só** o overlay de autonomia | 🧪 |
| **Autonomia / escalate** (AOS-087) — o overlay nível × classe de risco rebaixa um `permit` para `escalate` | **PROVADO NO NÓ QUE SERVE, com controlo, em 2026-08-19.** Duas submissões idênticas em tudo — `cap:fs.read`, tool `doc_read`, recurso `doc://notes`, taint `untrusted`, reversibilidade `reversible`, classe de risco `gray` — mudando **uma só** variável, a classe do agente: `agent-reader` (na tabela) resolveu **L4** e correu até `complete`; `agent-break-glass` (fora dela) caiu no piso **L0** e o selo diz `Decision: escalate`, `Code: E_ESCALATED`, `Reason: "autonomia L0 x gray -> suggest (gate humano)"`, com o run em `waiting_on_human`. Nenhum dos dois agentes existia antes: o L4 veio da **regra de CLASSE**, que é a propriedade que a cascata acrescenta | ✅ |
| **A decisão de autonomia fica SELADA por tool call** | No caminho `allow`, o selo de mediação carrega uma obrigação `autonomy` com `domain`, `level`, `oversight`, `requires_human` e `risk_class` — a decisão é **auditável**, não inferida da ausência de escalada. ⚠️ **Assimetria declarada:** no caminho `escalate` o registo traz `Obligations: null` e a mesma informação em texto livre no campo `Reason`. Um auditor que percorra obrigações vê as autorizações e **não vê** as escaladas; tem de ler também o `Reason` | ✅ |
> ✅ **Dois achados que só a cerimónia REAL revelou — CORRIGIDOS e PROVADOS em produção**
> (encontrados 2026-08-19, fechados no mesmo dia):
>
> **1. A retoma re-classificava mais severamente.** `toolCallCapture` não guardava a
> reversibilidade declarada; na retoma voltava vazia e `IsIrreversible()` trata o desconhecido
> como irreversível, pelo que a mesma tool call passava de `gray` a `danger`. **Provado corrigido
> no nó que serve:** os dois selos do mesmo run dizem `"Reversibility":"reversible"` e
> `L0 x gray`. O CONTROLO é histórico — a mesma experiência, horas antes, com o código antigo,
> deu `reversible → gray` e depois `"" → danger`.
>
> **2. Uma re-escalada depois de expirar não anunciava pendência nova.** Duas causas compostas: o
> Event Store deduplicava por `(run_id, step_id)` e a listagem retirava por CONJUNTO de chaves,
> sem ordem. A correcção dá identidade à ENCARNAÇÃO — `(run, step, geração)` — mantendo a chave da
> geração ZERO byte-idêntica, para o log já selado continuar a ser lido pelas mesmas regras.
> **Provado no nó que serve**, ciclo completo sobre duas encarnações:
>
> ```
> seq 35  approval.pending   pending-…-tool-1        (geração 0)
> seq 36  approval.expired   expired-…-tool-1
> seq 37  approval.pending   pending-…-tool-1#1      (geração 1)
> seq 38  approval.expired   expired-…-tool-1#1
> ```
>
> O `seq 38` é o que fecha a segunda metade: a encarnação nova **expirou sozinha**, logo o
> varrimento VIU-A — e o varrimento usa a mesma função de retirada que a listagem do operador.
> Ver o evento no stream provaria só que foi escrito, não que alguém o vê.

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
| **Autenticação OTLP** (DEF-012) | O canal é `http://otel:4318`, **em claro** na rede do compose. Um *bearer* aí seria teatro — viaja em claro na mesma rede de onde vem a ameaça. O que autentica é **mTLS**: variante pronta em [`otel-collector-mtls.yaml`](../deploy/server/otel-collector-mtls.yaml), **provada** num coletor descartável (sem cert de cliente → handshake recusado; com o do nó → `200`; HTTP claro → `400`). Por activar. ⚠️ **Correcção de 2026-08-20:** esta linha dizia que uma má configuração pára os spans «em silêncio». **Não pára** — cada lote falhado escreve no log. Faltavam outras duas coisas, agora feitas: os contadores do exporter passam a sair em `/metrics` (`aos_otlp_spans_failed_total`, `_dropped_total`, `_exported_total`, `_batches_total`), o que torna a falha **alertável** em vez de legível só à mão; e a linha de falha passou a ser **represada** (a 1.ª sai já, as seguintes uma vez por minuto, dizendo quantas) — antes saía por CADA lote e inundava o log, afogando-se nas próprias repetições | 💤 |

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
| **Reversão por digest** | `rollback.sh` com o digest anterior, guardado em `image.env.prev` pelo `deploy.sh` (verificado presente no servidor). **NUNCA foi exercido**: não há registo de um rollback executado, pelo que o provado é que o digest anterior fica guardado — não que a reversão funciona | ⚠️ |
| **O backup levanta o sistema** | Não é o mesmo que "o backup decifra". Ensaio completo do artefacto: Vault desselado com a chave de dentro do backup, WORM re-encadeado (108 partições), `idp-db.sql` restaurado (87 tabelas), Keycloak sobre ele, e **token do IdP restaurado a ler no nó restaurado** → `200`. Controlos no sistema restaurado: sem credencial `404`, header forjado `404`, replay do `jti` `404` | ✅ |
| **Alerta de recência do backup** | Verifica a idade dos **dois** lados: a cópia local (apanha "a máquina esteve desligada") e o backup **remoto** (apanha "o cron do servidor morreu" — o caso invisível, porque a recolha continua a correr sem erro e a dizer `0 novo(s)`). Alerta por código de saída, `ESTADO.txt` e Registo de Eventos. Controlos: tecto de 1h → saída `3`; servidor inalcançável → saída `2`; normal → `0` | ✅ |

---

## O que este inventário mostra

**Provado em produção com controlo:** os eixos que carregam o argumento do sistema — soberania de
leitura por-leitor, mediação de política, isolamento, auditabilidade da cadeia, DSAR, e a raiz da
delegação.

**Dormente por configuração, não por defeito:** attestation de dispositivo, atribuição
dispositivo↔aprovador, broker vault, autenticação OTLP. Cada um está nomeado no banner de arranque
com o que perde por estar desligado.

**Ligado, e agora com efeito PROVADO.** A autonomia esteve doze horas ligada e inerte: o banner
dizia `ORACULO LIGADO` sobre uma regra que apontava a um agente que nunca correu, pelo que tudo
escalava e nada o mostrava. Não era o nó a fingir — era o banner a afirmar com verdade um facto
(o oráculo está composto) que se lia como outro (a postura está a ser aplicada). A frase que aqui
estava — «o nó nunca finge» — era verdadeira sobre o mecanismo e enganadora sobre o efeito.

Fechou-se em 2026-08-19 com regras de **classe** e duas submissões que diferem numa variável só.
A categoria continua a fazer falta ao vocabulário: **ligado ≠ a aplicar-se**, e a única coisa que
distingue os dois é um par de execuções em que uma corre e a outra escala.

**Ausente e declarado:** credential broker, selo do Vault, e o DSAR sem barreira de transporte.
A verificação ancorada do WORM SAIU desta lista em 2026-08-20 — o mecanismo ficou completo
(selador + ancoragem multi-partição + cobertura declarada) e passou a 🧪, à espera de ser exercida
no nó que serve. (O alerta de recência do backup **não** pertence a esta lista — está
provado, com controlos; ver eixo M.)

O padrão que se repete em todos os achados desta auditoria — A1 a A4, a contagem de partições, a
âncora do WORM — é o mesmo: **a coisa parecia funcionar**. O que a distinguiu de funcionar foi
sempre um controlo que teria de falhar, e falhou.

---

## Contagem

**73 conceitos** em 13 eixos:

| Estado | Nº |
|---|---|
| ✅ Provado em produção | 47 |
| 🧪 Provado por teste (ou em clone restaurado) | 15 |
| ⚠️ Armado, não exercido | 4 |
| 💤 Dormente por configuração | 4 |
| ❌ Ausente e declarado | 3 |

Os **11 que não estão provados** (⚠️ + 💤 + ❌) estão todos nomeados acima, e cada um diz o que
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

---

## Auditoria adversarial do próprio inventário (2026-08-19)

Este documento foi confrontado, linha a linha, com o código — não com a memória de quem o escreveu.
O que se procurou foi o oposto do que ele afirma: nomes que não existem, mecanismos que não fazem o
descrito, e ✅ sem o controlo que a regra da casa exige.

**Corrigido nesta passagem**

| O que dizia | O que o código diz |
|---|---|
| a residência sela em `gov.read/<run>` | sela em `gov.residency/<run>` (`readResidencyPartition`); `gov.read` é o selo de LEITURA sensível — o código namespaceia-as separadamente **de propósito**, e o documento reintroduzia a colisão que o código evita |
| «Reversão por digest» ✅ | o digest anterior fica guardado em `image.env.prev` (verificado no servidor), mas **nenhum rollback foi executado**. Pela legenda deste documento, ✅ é «exercido no nó real» ⇒ passa a ⚠️ — a mesma leitura que já se aplicava ao burn-down |
| «Ausente: … alerta de recência do backup» | esse alerta está ✅ com três controlos (eixo M). A quarta ausência é o **DSAR sem barreira de transporte** |
| «Dormente: … autonomia/escalate … o nó nunca finge» | a autonomia está **LIGADA** desde 2026-08-19 00:03. E a frase final era verdadeira sobre o mecanismo e enganadora sobre o efeito |

**Os dois que ficaram em aberto — RESOLVIDOS na mesma passagem**

Nenhum era uma afirmação falsa. Ambos eram uma afirmação **verdadeira apoiada na evidência
errada** — que é pior do que parece, porque uma prova errada não avisa quando a propriedade se
perde.

- **«Sem bypass estrutural».** A prova citada era uma linha de BANNER, e o único gate em CI
  analisava um directório: `AnalyzeDir` **não recorre**. Acrescentou-se `AnalyzeTree` e um gate
  sobre os **166 pacotes** do repositório. O número que interessa: **invocação directa de
  `ToolFunc` = ZERO** — `ToolFunc` é *exportado*, e chamá-lo directamente executa o efeito sem
  PDP, sem orçamento e sem selo. As 3 violações restantes são colisões de nome (`dispatch` da CLI,
  método do Scheduler, o dispatcher do próprio RM), listadas com a razão de cada uma; uma
  excepção que deixe de reproduzir **falha** o teste, para a lista não apodrecer.

- **«Modo de identidade real endurecido».** O «controlo» era uma dedução da configuração. O
  controlo a sério já existia e não estava citado: `AOS_MODE=production` **exige** a âncora
  (`ErrProductionNeedsHardenedIdentity`) e a âncora **proíbe** qualquer chave de assinatura, seja
  em memória ou por caminho de ficheiro (`ErrConflictingIssuerKey`). Os dois ramos têm teste, e o
  modo de referência aceita a mesma chave — o controlo simétrico, sem o qual «recusa» seria
  indistinguível de «recusa tudo».

A lição é a mesma nos dois: **o que estava frágil não era o sistema, era a prova.** Um `✅` apoiado
num banner ou numa dedução descreve o mundo tal como está e cala-se no dia em que mudar.

**Um achado que era meu erro, e fica escrito porque o método falhou antes de acertar.** Li em
`resolveResourceValue` que «sem slots, o valor é a constante de sempre» e estive a um passo de
acusar o eixo C de afirmar um aborto que não existe. Existe: é `validateResourceBinding`
(`modeltools.go:156`), chamada no carregamento, e recusa exactamente o caso descrito. Tinha lido o
caminho de RUNTIME e concluído sobre o de CONFIGURAÇÃO. Verificar contra o código só vale se for
contra o código **certo**.

**Confirmado correcto, e vale dizê-lo:** o `409` de `run_id` duplicado é mesmo condicionado a quem
poderia LER o run (residência selada **e** coincidente, sem re-verificar a credencial para não
consumir o `jti`); `assertionMaxAge` e o TTL do challenge são ambos 5 min; os números do ingresso
(64/128/512) batem com o `.env` de produção; as 108 partições batem com o banner; o selo de leitura
sensível é mesmo uma **pré-condição** do desfecho; e a interseção do escopo «restringe e REVOGA,
nunca amplia» está imposta, não só narrada.
