# Desafio ao plano A3 — Credential Broker (BRK, AOS-070, ADR-006)

> **O que é:** avaliação adversarial do subtópico `#### A3. Credential Broker` de
> [`prontidao-modelos-agenticos.md`](./prontidao-modelos-agenticos.md). Terceiro da série,
> depois de [A1](./desafio-A1-budget-admission-control.md) e
> [A2](./desafio-A2-progress-surface.md), com o mesmo método.
>
> **Data:** 2026-08-08 · **HEAD avaliado:** `d6a334a` (branch `feature/AOS-128-ux-dx-tests`)

## ⚠️ Achado que não pertence ao A3 e é o mais grave da série

**O step-ledger persiste o OUTPUT de cada tool call EM CLARO no WAL.** Verificado à mão:

- `packages/cmd/aos/bootstrap.go:825` — `durable.NewStepLedger(es, durable.WithContentSealer(contentCipher))`
  passa o cifrador mas **não passa produtor**;
- `packages/kernel/agent-runtime/durable/step_ledger.go:346-347` — `subject := l.producer.NHIID`
  e a selagem só corre `if l.cipher != nil && subject != "" && len(res.Payload) > 0`;
- `grep WithProducer` em `packages/` — **zero chamadores não-teste**;
- o capturer faz o contrário: `packages/kernel/agent-runtime/loop.go:384` passa
  `Subject: goal.Principal.NHIID` e a captura é selada.

O cifrador por-titular está **ligado e inerte**. Os mesmos bytes ficam cifrados em
`replay.captured` e em claro em `step.ledger.applied`. Activo com `AOS_DURABLE_EXECUTION=1`
— a configuração da stack endurecida e a **exigida em produção** com four-eyes.

**Não depende do broker.** Qualquer PII ou segredo que hoje passe por uma tool já cai aqui.
Tem ticket próprio; está aqui porque foi esta avaliação que o encontrou.

## Proveniência e método

Seis lentes independentes contra o código, cada uma seguida de um céptico encarregado de a
**refutar**. 13 agentes, 0 erros, 437 chamadas a ferramentas, ~20 min.

| lente | a pergunta que só ela fez |
|---|---|
| `factos` | os «10 ficheiros de teste» e o ponto de costura são o que o documento diz? |
| `invariante` | **o valor tem de chegar ao destino** — onde deixa de ser opaco? |
| `vault` | o nó já tem um cliente Vault: isso reduz o esforço? |
| `microvm` | «injecção directa no mount» contra um executor **remoto por HTTP** |
| `ciclovida` | TTL curto contra a suspensão de 15 min e a retoma |
| `sequência` | o que o deferimento custa hoje, e o que falta ao plano |

### Uma hipótese minha que o fluxo refutou

Ao desenhar as lentes, sugeri que o cliente Vault já composto no nó
(`packages/cmd/aos/vaultkeyvault.go`, custódia de KEK) tornaria a estimativa «cliente Vault
real (MÉDIO)» exagerada. **Estava errado, e a evidência é explícita:** aquele cliente usa o
motor **Transit** e recusa por contrato devolver material —
`vaultkeyvault.go:207`: `func (v *vaultKeyVault) Key(keyRef string) ([]byte, bool) { return nil, false }`,
com o comentário «key-never-leaves: a KEK crua NUNCA é surrendida». O broker precisa
exactamente do oposto: **ler** segredo. Reaproveitável é só o transporte (`do()`, `ready()`),
e por cópia — vive em `package main`. Registo isto porque a hipótese entrou no prompt e
podia ter enviesado o resultado; não enviesou.

### O que verifiquei à mão

| achado | verificação | resultado |
|---|---|---|
| step-ledger em claro | `bootstrap.go:825`, `step_ledger.go:346`, grep `WithProducer` | confirmado (acima) |
| a hipótese do Vault | `vaultkeyvault.go:207` | **refutada** — Transit, `Key()` devolve `nil,false` |
| fallback de dev sem guarda | `modelgatewaywiring.go:78-81` | confirmado: devolve `"aos-dev-omniroute"` sem verificar `AOS_MODE` |
| mis-atribuição do risco | `docker-compose.oidc.yml:103,142,249` | confirmado: a env aponta ao bearer do LiteLLM; as keys de provider vivem em `model.env`, fora do nó |

O resto vem das lentes já passadas pelo céptico.

## Sumário executivo

1. **O eixo RISCO do A3 está mal atribuído.** «A chave do provider LLM é um ficheiro
   estático lido em claro» — não é: `AOS_MODEL_API_KEY_PATH` aponta ao **bearer
   nó→LiteLLM**; as chaves dos providers vivem em `secrets/model.env`, consumidas pelo
   LiteLLM, **fora do perímetro** que o A3 propõe fechar. Fechar o A3 como descrito não põe
   a chave do provider sob TTL/rotação, porque ela nunca entra no nó.
2. **Há um fail-open real e não declarado:** sem `AOS_MODEL_API_KEY_PATH`, o nó arranca em
   `AOS_MODE=production` a apresentar um bearer de dev embebido no binário, em vez de
   recusar.
3. **A invariante rainha é sobre o AGENTE, não sobre o processo** — e o fecho proposto
   (adaptar à porta `CredentialProvider`) **não a preserva**, porque essa porta devolve o
   valor em `string`.
4. **A estimativa 2 («adaptar à porta, pequeno-médio») está errada:** não é adaptação. O
   broker nunca devolve valor, e a porta alvo não transporta identidade.
5. **A estimativa 3 («wiring de injecção, médio») está subestimada:** quatro roturas
   independentes, três módulos Go e um limite de confiança.
6. **«Injecção directa no mount da microVM» descreve um executor in-process** que não é o
   que está composto — o real é remoto por HTTP, e quem monta o rootfs é o orchestrator
   host-side.

---

## Veredicto sobre o A3 tal como está escrito

| Alegação | Veredicto | Evidência (HEAD d6a334a) |
|---|---|---|
| **PROPÓSITO** — handle opaco 128 bits, TTL curto revogável, troca server-side | **exacto** | `packages/platform/broker/exchange.go:200-218` (mediação + `Handle(dec.Output)`), `lease.go:88-96`, `injection.go:53-56` |
| **PROPÓSITO** — «valor **nunca observável**» | **incompleto** | A invariante ratificada é perante o **agente**, não perante o processo do nó: `docs/adr/ADR-006-credential-broker-jit.md:26` («o agente nunca vê o segredo downstream») e :43-45 («resolvido, trocado e usado inteiramente do lado servidor»). O tipo garante isso por construção (`internal/vault/vault.go:47-50`, campos não-exportados; :68-75 `DeliverTo` é a única saída). O documento apresenta a propriedade sem qualificar o titular da garantia — e o fecho por `CredentialProvider` não a preserva (ver §3-I) |
| **PROPÓSITO** — «injecção directa no **mount da microVM**» | **incompleto** | Reproduz fielmente o contrato do módulo (`packages/platform/broker/vault_client.go:16-18`, «o mount de credencial da microVM») mas descreve um executor **in-process**. O executor composto é remoto (`packages/cmd/aos/firecrackerexecutor.go`, `AOS_SANDBOX_FIRECRACKER_URL`) e quem monta o rootfs é o orchestrator host-side, read-only e partilhado (`deploy/node/dev-hardened/firecracker/orchestrator/main.go:118-124`; residual já admitido em `.../firecracker/README.md:66-68, :71`) |
| **«10 ficheiros de teste»** | **exacto** | 8 em `packages/platform/broker/*_test.go` + `internal/vault/vault_test.go` = 9 no módulo, + `packages/security-tests/secrets_test.go` = 10 |
| Nomes dos testes citados | **exacto** | `security_test.go` (`TestSeguranca_SegredoNuncaObservavel`), `exchange_test.go`, `injection_test.go` (grep confirmou os três) |
| `TestInjector_ImplementaPortaSBX` como prova de injecção | **incompleto** | Prova **conformidade de tipo** com a porta in-process: `injection_test.go:65-68` → `packages/substrate/sandbox/credentials.go:15-20`, consumida por chamada Go em `lifecycle.go:269-273`. Não prova nada sobre o executor composto |
| **«zero imports não-teste fora do módulo»** | **exacto** | `grep "aos-ref/platform/broker" --include=*.go` fora de testes devolve só os 5 auto-imports do próprio módulo (`errors.go:6`, `exchange.go:13`, `injection.go:8`, `lease.go:8`, `vault_client.go:4`) |
| **`modelgatewaywiring.go:71-83`** — `staticModelCredential` + comentário | **exacto** | Verificado à letra: `staticModelCredential struct{ secret string }` (:76), `Fetch` (:78-83), comentário «Em produção o composition root liga aqui o vault/broker (EPIC-07)» (:72-73). Leitura em `:112-119`, congelamento em `:134` |
| **`ReferenceBroker` / `ErrNotWired`** | **exacto mas incompleto** | Literalmente verdadeiro e correctamente colocado sob «costura em falta». Omite que a porta é **inalcançável**: `packages/platform/model-gateway/internal/credentials` tem zero importadores; o caminho realmente composto é `Credentials: staticModelCredential{}` → `production.go:176` → `credProviderSource` (`production.go:436-448`). O fail-closed do broker JIT nunca executa. Omite também que esse `internal/credentials` já tem uma `Source` com cache JIT, TTL/RefreshLead, singleflight e allowlist regional fail-closed — reaproveitável por construtor exportado **dentro** do módulo do GW |
| **«vault do broker é só in-memory (NUNCA usar em produção)»** | **exacto mas incompleto** | `vault_client.go:21-23`, `internal/vault/vault.go:94-122`. Omite o bloqueio estrutural: `vault.Secret` (vault.go:47-50) tem campos não-exportados **e não há construtor exportado** — um cliente real que implemente `vault.Client` tem de nascer **dentro** de `packages/platform/broker/internal/vault/` |
| **«nenhuma env, nenhuma linha de banner»** | **exacto** | Zero `AOS_BROKER_*` no repositório; o banner (`bootstrap.go:1361-1514`) não tem linha de broker. **Agrava-se**: também não tem linha de **modelo/gateway** — a única menção é incidental dentro da linha de DSAR (`bootstrap.go:1488`) |
| **RISCO** — «a **chave do provider LLM** é um ficheiro estático lido em claro» | **errado na atribuição** | No deployment endurecido `AOS_MODEL_API_KEY_PATH` aponta para a **LITELLM_MASTER_KEY** (bearer nó→LiteLLM): `docker-compose.oidc.yml:103` + `:142`, gerada por `up-oidc.sh:144-145`. A chave do **provider** vive em `secrets/model.env`, consumida por `env_file` do LiteLLM (`docker-compose.oidc.yml:249`) — **fora** do perímetro que o A3 propõe fechar |
| **RISCO** — «sem TTL/revogação/rotação» | **exacto e subdeclarado** | `modelgatewaywiring.go:112-119` lê **uma vez**, `:134` congela; `Fetch` (:78-83) devolve sempre o mesmo valor. Nenhum caminho de recarga sem reiniciar. Não declarado: `Fetch` devolve um bearer de dev literal quando o segredo é vazio (`:79-81`), **sem guarda de `AOS_MODE=production`** |
| **Estimativa 1** — cliente Vault real atrás de `vault.Client` (**médio**) | **exacto** | A hipótese de que `packages/cmd/aos/vaultkeyvault.go` já o resolve é falsa: é **Transit** (5 call sites: `:126, :148, :172, :220, :221`) e recusa por contrato devolver material — `:207 func (v *vaultKeyVault) Key(keyRef string) ([]byte, bool) { return nil, false }`. O broker precisa exactamente do oposto (`internal/vault/vault.go:80-82`). Reaproveitável: só `do()` (:71-95) e `ready()` (:103-121), **por cópia** (é `package main`) |
| **Estimativa 2** — adaptar à porta `CredentialProvider` (**pequeno-médio**) | **errado** | Não é adaptação, é mudança de assinatura pública. (a) Incompatibilidade de forma: `*broker.Broker` não expõe nenhum caminho que devolva o valor (`exchange.go:200-218` devolve `Handle`; `lease.go` → `Secret.DeliverTo`) e `CredentialProvider.Fetch` exige `(secret string, error)` (`production.go:82-84`). (b) A porta **não transporta identidade** — `gateway.go:515-518` comenta-o e `:527` chama `Fetch(ctx, provider, region)`; o broker exige `Principal` (`exchange.go:62-67`) |
| **Estimativa 3** — wiring de injecção no executor (**médio**) | **errado (subestimado)** | Quatro roturas independentes: (1) sem produtor de handle — `packages/substrate/sandbox/argbinding.go:80` devolve `ExecRequest{RunID, StepID, Call}` e é o único construtor de produção; (2) sem injector — `sandboxwiring.go:119-122` compõe o Launcher só com `WithEventSink`+`WithSnapshot`; (3) o driver **descarta** o handle antes do fio — `driver_firecracker.go:104` passa só `req.Call`; (4) o `Injector` descarta a `Instance` — `injection.go:39` — logo nenhum sink consegue endereçar uma microVM. Atravessa 3 módulos Go e um limite de confiança (orchestrator `privileged` com `/dev/kvm`) |
| **Estimativa 4** — env + banner (**pequeno**) | **exacto** | — |
| **Global «GRANDE»** | **exacto no rótulo, errado na decomposição** | O que domina não é o cliente Vault: é o **canal de entrega até ao guest remoto** (contrato de wire + colocação host-side) e o **passo zero de política/identidade** que o fecho não lista |
| «deferimento coerente com a v1, mas devia ser declarado» | **exacto** | — |

---

## Risco REAL hoje vs risco que só existe depois de ligar

### Exposto AGORA no deployment

1. **Segredo de infra estático, congelado, sem TTL nem rotação.** `modelgatewaywiring.go:112-119` (`os.ReadFile`) → `:134`. Atravessa a rede como bearer para o upstream a cada chamada. Nenhuma recarga sem reiniciar o nó. É o único risco de credenciais que o A3 descreve e que existe de facto — mas sobre o segredo **errado** (LITELLM_MASTER_KEY, não a chave do provider).
2. **Fallback de dev sem guarda de produção.** `modelgatewaywiring.go:78-81`: sem `AOS_MODEL_API_KEY_PATH` o nó arranca em `AOS_MODE=production` a apresentar um bearer de dev embebido no binário, em vez de recusar. Não declarado em lado nenhum.
3. **Ficheiro de segredo com permissões largas na stack de dev.** `up-oidc.sh:145` faz `chmod 644` sobre o ficheiro apontado por `AOS_MODEL_API_KEY_PATH`; `up-oidc.sh:126` fixa um valor por omissão em claro no próprio script. Dev, git-ignored — mas é o molde que o operador copia.
4. **O step-ledger persiste o OUTPUT de cada tool call em claro no WAL** (severidade **alta**, e é o achado mais consequente do conjunto). `bootstrap.go:825` compõe `durable.NewStepLedger(es, durable.WithContentSealer(contentCipher))` **sem** `WithProducer`; `step_ledger.go:346-347` só sela quando `l.producer.NHIID != ""`; sem isso `persistRec == clearRec` (:344-345) e o `Append` (:360-370) leva o payload em claro. `grep WithProducer` confirma zero chamadores de produção para o ledger. O capturer faz o contrário (`loop.go:384` passa `Subject`; `replay/nondeterminism_capture.go:275` sela). Os **mesmos bytes** ficam cifrados em `replay.captured` e em claro em `step.ledger.applied` — e fora do alcance do crypto-shredding (`retention.go` só projecta `replay.captured` com `sealed_subject`). Activa-se com `AOS_DURABLE_EXECUTION=1` + substrato durável. **Não depende do broker**: qualquer PII ou segredo que passe hoje por uma tool já cai aqui.
5. **Eco do resultado/erro de tool para o tail do prompt sem redacção.** `prompt.go:239` (`tool_error=<msg>`) e `:243` (`r.Value` verbatim). A mensagem vem de string arbitrária do componente host-side. Contido em **autoridade** pelo taint gate (`taint_gate.go`), não em **conteúdo**.
6. **`AOS_SANDBOX_FIRECRACKER_URL` sem validação de esquema nem autenticação** (`firecrackerexecutor.go:96-101`, `:70`), quando o gémeo `AOS_ATTESTATION_VERIFIER_URL` no mesmo binário exige https-ou-loopback e token de ficheiro. Hoje não transporta segredo — mas é o canal candidato a transportá-lo.
7. **Banner sem linha de modelo/gateway.** Um operador não distingue pelo arranque se fala com um LLM real ou com o `referenceModel{}` (`bootstrap.go:1043-1045`).

### Só existe DEPOIS de ligar (hipotético hoje)

Nada do broker está composto — zero importadores não-teste. Portanto **não existe hoje** nenhuma lease, nenhum handle, nenhum segredo downstream em memória do nó, nenhuma credencial em trânsito. São riscos **pós-wiring**, não exposições:

- no-op silencioso do injector (guarda `l.injector != nil && req.CredentialsHandle != ""`, `lifecycle.go:269`: com handle sempre vazio, ligar o injector compila, passa testes e não injecta nada);
- negação da troca pelo PDP/identidade a matar todos os turnos de modelo;
- ausência de reaper de leases (`lease.go:118-128` declara-o como diferido) e `Broker.Revoke` sem chamador de produção;
- TTL verificado só no instante da entrega (`lease.go:104-111`), sem re-verificação durante o uso;
- segredo a atravessar o fio para o orchestrator.

**Confundir estas duas listas é a decisão errada disponível aqui**: o A3 gasta a sua secção de RISCO no eixo do broker (hipotético) e não regista o item 4, que é real, activo e de severidade superior a tudo o resto nesta página.

---

## O que o documento NÃO diz e devia

### I. A invariante rainha não sobrevive à composição da forma que o documento sugere (alta)

O A3 apresenta «valor nunca observável» como propriedade provada e trata a distância até ao deployment como wiring. Sob composição há **duas rotas de fecho e ambas mudam a propriedade**:

- **Rota GW (`CredentialProvider`)**: `production.go:82-84` devolve o valor **nu como `string`** ao processo do nó; só depois é encapsulado (`production.go:447`) e a única saída é o header (`internal/adapters/openai_http.go:176-180`). Isto **não viola** ADR-006 (o agente continua sem ver nada; o processo do nó é o lado servidor) mas a garantia que sobrevive é **redacção em logs/JSON**, não não-observabilidade por tipo. Um cliente HTTP em-processo não pode autenticar-se sem o valor em memória — nenhum desenho de broker evita isto sem mover o egress para fora do processo.
- **Rota broker (`DeliverTo`→`Sink`)**: preserva a garantia por tipo, mas o único `Sink` existente é `MemoryGuest` (`injection.go:70-91`), in-process, que descarta o valor. O guest real está atrás de HTTP. **Aqui é que a decisão nasce** (§4-ii).

**Onde o valor deixaria de ser opaco, e que superfícies duráveis o capturariam** — o documento não nomeia nenhuma:

| Superfície | Cifrada por-titular? | Alcançada por erasure? |
|---|---|---|
| `step.ledger.applied` (`step_ledger.go:360-370`) | **Não** — produtor nunca injectado (`bootstrap.go:825`) | **Não** |
| `replay.captured` (`replay/nondeterminism_capture.go:275`) | Sim, quando `tc.Subject != ""` (`loop.go:384` povoa-o) | Sim (`retention.go`) |
| Tail do prompt (`prompt.go:239-243`) | n/a — volta ao **modelo**, e daí à captura | n/a |
| `AuditRecord.Reason` no WORM (`audit/record.go:275-279`) | **Não** — sem `PayloadRef`, logo sem `KeyRef` | **Não** (append-only, sem Delete/Prune no `eventstore`) |
| `LifecycleEvent.credentials_handle` (`sandbox/events.go:104`) | Handle opaco, não valor — correcto por desenho | — |

Duas destas são irreparáveis a posteriori. **O A3 deve declarar que a invariante rainha é provada dentro do módulo e que a sua preservação sob composição é uma pré-condição de wiring, não um dado.**

### II. Falta o passo zero: política e identidade antes do wiring (média)

`exchange.go:208` medeia a troca pelo RM e `:213-215` devolve `DeniedError` para qualquer efeito ≠ permit. O bundle assinado montado (`packages/control-plane/pdp/policies`) tem exactamente `cap:http.post` e `cap:fs.read` em `allowlist.json` e dois permits em `aos_authz.cedar`; zero menções a broker/credencial. Acresce que a troca exige um `referencemonitor.Principal`, e o pipeline do GW fabrica identidade própria (`modelgatewaywiring.go:88-99`), não um principal do RM — a troca seria negada por **identidade** antes de chegar ao Cedar. *Correcção ao que uma das lentes propôs*: **não é obrigatório re-assinar o bundle** — a `Downstream.Capability` é escolhida pelo composition root (`exchange.go:53-58`) e declarar a troca sob `cap:http.post` satisfaz literalmente o permit já assinado. Mas a decisão tem de ser tomada **antes** do wiring, não depois.

### III. Ligar o injector sem produtor de handle é um falso-«feito» (média)

Quatro roturas (§tabela, estimativa 3). A ausência **é** visível no Event Store (`lifecycle.go:243, :257, :289` selam o handle nos eventos; `events.go:104` serializa-o) — o que falta é o **fail-closed e o alarme**. Mitigação no molde já usado no repositório: uma guarda que recuse arrancar com injector configurado e nenhuma binding a produzir handle (o padrão de `approval_wiring_test.go`).

### IV. O cliente Vault já composto não serve o `Fetch` do broker (média)

`vaultkeyvault.go:207` recusa devolver material por contrato — é o motor **Transit** (wrap/unwrap/destroy), o oposto do que `vault.Client.Fetch` precisa. Reaproveitável: transporte (`do()`, `ready()`), por cópia. E a decisão de **motor** determina a semântica que o A3 promete: com KV v2 o segredo é estático e `Broker.Revoke` corta o **handle**, não a credencial a jusante; só *dynamic secrets* (lease_id/renew/revoke do Vault) dão corte downstream. Nota: ADR-006:52 e :86 dizem exactamente isto («cada **emissão** é um lease revogável», «TTL curto **reduz** a janela») — o A3 espelha o ADR fielmente; o que falta é a nota operacional.

### V. A porta alvo não transporta atribuição (média)

`gateway.go:515-518` comenta explicitamente que o principal não é passado. Pior: o adaptador RT→GW do nó nunca usa `WithRun`/`WithPrincipal` (`modelgatewaywiring.go:144`), logo `ex.RunID` e `ex.Principal` são estruturalmente vazios. Um broker ligado aqui produziria `recordExchange` (`exchange.go:293-315`) com `PrincipalNHI` constante e `RunID` vazio — auditoria que não distingue runs. Os dados **já existem** no call site (`pipeline.go:70, :75, :105-110`); falta passá-los. *Correcção*: o registo da troca vai para o Event Store do broker (`exchange.go:315`), não para o `audit.NewMemStore()` do GW (`modelgatewaywiring.go:133`) — o que degrada é o **conteúdo**, não a durabilidade. O MemStore perde outra coisa: o audit de **governação** do GW.

### VI. Mis-atribuição do segredo no eixo RISCO (média)

Ver tabela. Fechar o A3 tal como escrito daria TTL ao bearer nó→LiteLLM e deixaria a chave do provider — o segredo de maior valor — fora do perímetro.

### VII. `vault.Secret` não é construível fora do pacote (baixa-média)

`vault.go:47-50` + ausência de construtor exportado. Existe um caminho compilável de outro módulo (`broker.NewMemoryVault()` + `MemoryVault.Put`, `vault_client.go:26` e `vault.go:104-108`), mas é **push-only** e obriga a manter o valor em claro num mapa do processo — exactamente o objecto rotulado «NUNCA usar em produção». A decisão real (cliente HTTP dentro de `internal/vault/` vs exportar um construtor de `Secret`) não está nomeada.

### VIII. Higiene operacional pré-wiring (baixa)

Sem reaper de leases (`lease.go:118-128`, declarado como diferido no próprio código); `Broker.Revoke` sem chamador fora de testes; TTL verificado só na entrega (`lease.go:104-111`), com `Sink` sem `Unplace`. Nada disto morde hoje; tudo tem de estar fechado antes do wiring.

### IX. Eixos adjacentes que a secção A3 não cobre e que ninguém cobre (média, outra linha da tabela)

`parseVaultDSARFromEnv` (`main.go:1082-1104`) **não valida o esquema** de `AOS_DSAR_VAULT_ADDR` — no mesmo binário, `production.go:64-67`/`:383-395` recusam egress não-https fail-closed. E o token do Vault é lido uma vez e **nunca renovado** (zero ocorrências de `renew-self` no repositório), enquanto `deploy/node/README.md:96` recomenda tokens de curta duração: seguir a recomendação faz a custódia da KEK morrer silenciosamente, indistinguível de crypto-shred, com o `/readyz` verde (a sonda `ready()` usa `/v1/sys/seal-status`, não-autenticado). Eixo AOS-215/216.

---

## Decisões que são do dono (não minhas)

### (i) Partilhar o cliente/token Vault já composto, ou separar?

| Opção | Trade-off |
|---|---|
| **A. Partilhar** `AOS_DSAR_VAULT_ADDR`/token com o broker | Barato, uma única sonda de saúde. Mas um só token acumula **destruir chaves Transit** (crypto-shred, GDPR) e **ler credenciais downstream** — over-privilege, e um comprometimento único dá exfiltração *e* over-erasure. Além disso os motores são diferentes (Transit vs KV/dynamic): a partilha é só de endereço e credencial, não de código |
| **B. Separar** `AOS_BROKER_VAULT_*` com política/token próprios | Duas configurações, duas sondas, duas rotações. Isola o blast radius e permite escolher *dynamic secrets* só do lado do broker |

**Recomendação (minha, não decisão): B.** O código partilhável são ~50 linhas de transporte, que se copiam; o que se partilharia de verdade é autoridade — e é isso que não se deve partilhar. Se ficar A, tem de ficar registado que o token do nó passa a ter poder de leitura de credenciais downstream.

### (ii) Qual é a regra de composição que mantém a invariante rainha quando o broker liga?

| Opção | Trade-off |
|---|---|
| **A. O nó resolve e empurra o valor no fio** para o orchestrator | Menos trabalho de protocolo. Destrói a garantia de tipo: o segredo passa a existir como string no processo do nó e em trânsito num canal que hoje não tem TLS nem autenticação (§I-6). Exige endurecer esse canal *primeiro* |
| **B. O nó propaga só o handle opaco; o orchestrator resolve contra o broker por canal próprio** | Preserva `DeliverTo`→`Sink` através da fronteira; precedente existente no repositório (verificador de attestation remoto com token de ficheiro e validação de esquema). Custo: o orchestrator passa a ser cliente do broker, com identidade própria e um segundo caminho de autenticação |
| **C. Não ligar credenciais ao executor remoto na v1**: broker só para credenciais consumidas **em-processo** (ex.: egress do GW) | Fecha o gap declarado sem contrato novo. Deixa a promessa «injecção no mount» explicitamente deferida |

**Recomendação: C para a v1, B como desenho-alvo declarado.** A também exige (i) endurecer `AOS_SANDBOX_FIRECRACKER_URL` e (ii) resolver o overlay por-call (AOS-066) — o rootfs é read-only e partilhado, não há onde escrever um mount por-microVM.

### (iii) Revogação de autoridade e revogação de credencial devem estar ligadas?

Hoje são independentes por desenho declarado: a injecção autoriza-se por **posse do handle** (`exchange.go:21-36`, mitigado por 128 bits não-adivinháveis), enquanto a emissão é mediada pelo RM. Ligar as duas significaria: revogar um principal no directório revoga as suas leases.

| Opção | Trade-off |
|---|---|
| **A. Manter independentes** | Coerente com ADR-006; a mediação já acontece na emissão. Mas uma autoridade revogada mantém leases vivas até ao TTL |
| **B. Ligar** | Corte imediato. Custo: a injecção passa a depender do RM no caminho quente, e cria acoplamento entre dois eixos |

**Bloqueio prévio que muda a pergunta:** o directório de autoridade é lido **uma vez no arranque** (`main.go:404`), sem watcher. Ligar as duas revogações não produz revogação a quente de coisa nenhuma. **Recomendação: fechar primeiro a recarga do directório** (eixo AOS-071, hoje afecta *todas* as tool calls e o banner não declara que o directório é imutável até reiniciar); decidir B só depois. Entretanto, declarar em banner que a revogação de autoridade exige reinício.

---

## Plano A3 revisto

**O que muda face ao documento:** a ordem, a decomposição e duas entradas novas. O cliente Vault existente **não reduz** o esforço (motor errado, `package main`, tipo não-exportado) — a estimativa «médio» do documento estava certa e a hipótese contrária é refutada. O que estava mal dimensionado é a porta (`pequeno-médio` → mudança de assinatura) e o executor (`médio` → multi-módulo com decisão de protocolo em aberto).

**Aviso em voz alta:** seguir a ordem escrita — «adaptar à porta `CredentialProvider`» antes de política e identidade — **introduz um risco que hoje não existe**. O GW é fail-closed em cada pedido (`production.go:176`; `credProviderSource.Fetch` propaga qualquer erro, `gateway.go:527`), e a troca seria negada por identidade e por default-deny do Cedar. O nó passa de «funciona com um ficheiro estático» para «nenhum turno de modelo executa».

| # | Passo | Depende de | Esforço | Nota |
|---|---|---|---|---|
| **0a** | **Selar o step-ledger por-titular** — propagar o titular por-`Apply` (como `tc.Subject` no capturer), não `WithProducer` global no bootstrap: o ledger é um por processo e partilhado por todos os runs | — | **médio** (mudança de assinatura) | **Fecha risco REAL de hoje.** Ortogonal ao broker; não deve esperar por ele |
| **0b** | Guarda de produção no fallback `aos-dev-omniroute` (`modelgatewaywiring.go:78-81`) + linha de banner do modelo/gateway | — | **pequeno** | Fecha risco real; o banner é o item mais barato do A3 |
| **0c** | Validação de esquema em `AOS_DSAR_VAULT_ADDR` (reutilizar o molde de `checkRemoteAttestationURL`) e renovação do token do Vault | — | **pequeno** / **médio** | Eixo AOS-215/216, não A3 — mas listar |
| **1** | **Decidir (ii)** e registá-lo: rota do valor sob composição | — | decisão | Bloqueia 4 e 5 |
| **2** | **Passo zero de política e identidade**: nomear a capability da troca (ou reutilizar `cap:http.post` já assinada), garantir um `referencemonitor.Principal` real no ponto de aquisição, e verificar contra `secured.go` (identity check + revalidação de catálogo) | 1 | **médio** — cerimónia, não código, se exigir re-assinatura | **Novo. Não está no documento.** Tem de vir antes de qualquer wiring |
| **3** | Cliente Vault real **dentro de** `packages/platform/broker/internal/vault/` (único sítio onde `Secret` se constrói sem alargar a superfície); decidir KV v2 vs dynamic secrets | (i) | **médio** | Igual ao documento em tamanho, diferente em localização. Transporte por cópia de `vaultkeyvault.go:71-121` |
| **4** | Higiene pré-wiring: reaper de leases no loop de serviço (molde `approval_sweeper.go`), superfície de operação para `Revoke`, guard-test de existência de chamador | 3 | **pequeno-médio** | **Novo.** `lease.go:118-123` já o declara como diferido |
| **5** | Porta de aquisição: alargar `CredentialProvider` com contexto de chamada (principal/run) + `WithRun`/`WithPrincipal` no adaptador + apontar o `Audit` do GW ao store durável | 2, 3 | **médio** (não «pequeno-médio») | Resolve também a incompatibilidade de forma: o que se liga é um adaptador que consome o broker, não `*broker.Broker` a implementar a porta |
| **6** | Injecção no executor — **só se (ii) = A ou B** | 1, 5 | **grande, multi-módulo** | Produtor de handle em `argbinding.go`; `WithCredentialInjector` no launcher; propagar handle no `GuestExecutor`; dar dimensão de instância ao `Sink`; campo no wire; colocação host-side; endurecer `AOS_SANDBOX_FIRECRACKER_URL`; overlay por-call (AOS-066). **Guarda obrigatória**: recusar arrancar com injector e sem produtor de handle |
| **7** | Envs (`AOS_BROKER_VAULT_*`) + linha de banner honesta | 3-6 | **pequeno** | Igual ao documento |

**Invariante a fixar antes do passo 6:** tudo o que entre em `Call.Input` tem de ser determinista por `(run, step)` — hoje `BuildExecRequest` (`argbinding.go:38-81`) é uma função pura, e um handle mintado na construção da call faria o ciclo escalar→aprovar→retomar nunca convergir (a retoma nunca **encontra** o grant, porque a evidência resolve-se por preview). Alternativa que o evita: resolver por `(run, step)` no `Inject`, já que a `Lease` carrega `RunID`/`Capability` (`lease.go:23-27`).

---

## O que foi REFUTADO

- **«O WORM passou hoje a introduzir uma classe nova de exposição ao selar o Reason.»** Refutado. O `eventstore` só expõe Append/Read/Healthy/Close — não é mutável nem apagável; o sink do RM já escrevia `Reason` em claro (`reference-monitor/eventsink.go:143-149`) antes do commit de hoje, e o `contentCipher` nunca esteve ligado a esse sink. Sobrevive como **invariante**, não regressão: `Reason` é texto livre e o WORM é a superfície mais cara de reparar — a acção é uma regra para autores de hooks, não uma reversão.
- **«Um handle fresco no `ExecRequest` queima o grant a cada tentativa.»** Refutado. A evidência resolve-se **por preview** (`integration/approval_store_durable.go`): preview divergente ⇒ não encontra grant ⇒ `VerifyApproval` nunca é invocado ⇒ `Consume` nunca corre. O grant sobrevive até ao TTL. Sobrevive a invariante de determinismo (acima).
- **«O `vaultkeyvault.go` já composto reduz a estimativa do cliente Vault.»** Refutado — é a hipótese do contexto que cai, não o documento. Transit, key-never-leaves, `Key()` devolve `(nil,false)`.
- **«O segredo teria de entrar em `call.Input`» / «a injecção é estruturalmente inalcançável».** Refutado. O desenho já encaminha o valor **fora** do `ExecRequest` (`Injector.Inject` → `vault.Sink`). É trabalho de protocolo num componente que o projecto já possui.
- **«Não existe barreira nenhuma entre o resultado de uma tool e o prompt seguinte.»** Refutado. A barreira existe e é de **taint** (resultado sempre untrusted; `prompt.go:221` carimba a proveniência; `taint_gate.go:158` nega fail-closed). O que falta é **redacção de conteúdo**, não contenção de autoridade.
- **«O `tool_error` é o furo da persistência da captura.»** Refutado. `WithSensitiveResults` não tem um único chamador de produção; onde há exposição real (`tc.Subject` vazio) o `Output` inteiro vai igualmente em claro. A inconsistência de `nondeterminism_capture.go:397-399` sobrevive apenas como armadilha latente.
- **«Leases expiradas acumulam-se com segredo em claro num nó de longa duração.»** Refutado como risco de hoje: o broker não é importado por código não-teste; e as únicas implementações de `vault.Client` são in-memory, que já retêm tudo — a lease não acrescenta exposição incremental.
- **«`vault.Client` é inimplementável, logo nunca devolve um `Secret` não-vazio.»** Parcialmente refutado: existe caminho compilável de outro módulo via `NewMemoryVault`/`Put`. Mas é push-only e degradado (§VII).
- **«Um `AOS_SANDBOX_FIRECRACKER_URL` forjado executa comandos arbitrários numa microVM privilegiada.»** Refutado: o guest-agent é um `switch` com um só caso (`read` confinado por prefixo) e `default: comando desconhecido`; o serviço não publica portas. Sobrevive a assimetria de disciplina (esquema + autenticação).
- **«`http://` no Vault é um risco entregue hoje.»** Refutado: o deployment endurecido e o runbook usam `https://`. Sobrevive a **ausência de validação**, como inconsistência com o precedente do próprio binário.
- **«O A3 afirma a injecção no mount como PROVADA.»** Refutado: o bullet «Provado» não a lista; o defeito é de **omissão** no bullet «Fecho».
- **«A assimetria entre revogação de autoridade e de credencial é um defeito do A3.»** Refutado: é residual de AOS-071, igual para todas as capabilities, e o A3 não afirma o contrário. Sobrevive como linha própria no eixo AUTORIDADE (recarga a quente do directório).
- **«O modelo é o único subsistema em silêncio total no banner.»** Refutado como superlativo (o registry de tools também não tem linha própria). Sobrevive o facto: nenhuma das linhas do banner menciona modelo ou gateway.

**Não verificado por mim nesta sessão** (aceite das lentes com as citações que apresentaram): `packages/integration/secured.go:319-320` (identity check + revalidação como gates adicionais à troca), `packages/integration/approval_store_durable.go:176-208` (resolução de evidência por preview), e o conteúdo de `packages/control-plane/pdp/policies` (allowlist e Cedar).
---

## Ver também

- [`desafio-A1-budget-admission-control.md`](./desafio-A1-budget-admission-control.md)
- [`desafio-A2-progress-surface.md`](./desafio-A2-progress-surface.md)

- [`desafio-A4-orquestrador.md`](./desafio-A4-orquestrador.md) — o quarto da série.

## Rastreabilidade

Transcrições por agente: `.claude/projects/…/subagents/workflows/wf_80dad371-e91/journal.jsonl`.
Script do fluxo: `…/workflows/scripts/desafio-a3-credential-broker-wf_80dad371-e91.js`.

> **Nota de âmbito:** este relatório NÃO altera `prontidao-modelos-agenticos.md` nem corrige
> nenhum dos defeitos que descreve. O achado do step-ledger tem ticket próprio.
