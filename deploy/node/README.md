# Empacotamento do nó `aos` (AOS-168 / ADR-017)

Imagem **distroless, non-root, root-fs read-only** do nó de referência. A fronteira de
supply-chain é ADR-017 (FIXA na Carta §4.1). Pontos **1/2/4 impostos**, ponto **3 mínimo**
(SBOM + proveniência geradas; **assinatura DEFERIDA para EPIC-10** — declarado, não fingido),
ponto **5 respeitado** (a chave do issuer **nunca** entra na imagem).

## Construir

O contexto de build é a **raiz do repo** (os 42 `replace ... => ../../` de
`packages/cmd/aos/go.mod` são relativos; a árvore `packages/` inteira tem de estar no contexto):

```bash
docker build -f deploy/node/Dockerfile -t aos-node:local .
```

Build **reprodutível/offline**: o `go build` corre com `CGO_ENABLED=0 GOPROXY=off -trimpath`.
O único passo de rede é `go mod download` (prime do cache, verificado contra o `go.sum` pinado —
o projecto não vendoriza por opção; ver `scripts/ci/cache-prime.sh`).

## Correr — root-fs READ-ONLY + estado durável em volume

O root-fs é read-only; o **estado durável** (Event Store / WORM, AOS-170), quando ligado,
escreve num **volume gravável EXPLÍCITO**, nunca no root-fs:

```bash
docker run --rm \
  --read-only \
  --tmpfs /tmp \
  -v aos-data:/var/lib/aos \
  -e AOS_API_ADDR=127.0.0.1:8080 \
  -p 8080:8080 \
  aos-node:local
```

O arranque de referência corre **in-memory** (não escreve no root-fs), pelo que roda limpo sob
`--read-only`. A durabilidade é opt-in e exige o **mount explícito** `-v aos-data:/var/lib/aos`.

> A imagem **não declara `VOLUME`** de propósito: sem ele, uma tentativa de durabilidade sob
> `--read-only` **sem** `-v` falha **visivelmente** em vez de escrever num volume anónimo órfão —
> a verificação de root-fs read-only não é mascarada. O directório `/var/lib/aos` é criado owned
> por `65532:65532`, pelo que o volume nomeado do operador herda a ownership certa.

### Superfície de configuração — TODAS as variáveis lidas pelo nó (AOS-203)

O ambiente é a **única** superfície de configuração do binário entregue (`Config` vive em
`package main`: um campo que `nodeConfigFromEnv` não escreva é **inalcançável** por quem corre a
imagem). A tabela abaixo é o **índice completo** — toda a variável lida pelos dois binários da
imagem (o nó, `packages/cmd/aos`, e o `aos-healthprobe` do `HEALTHCHECK`) está aqui.

O teste `TestAOS203EnvSurfaceIsDocumented` (`packages/cmd/aos/env_surface_test.go`) **avermelha**
se alguém acrescentar uma leitura de ambiente sem a documentar **nesta secção**. O que ele impõe,
exactamente: extrai por **AST** (não `grep`) as chamadas `os.Getenv`/`os.LookupEnv`/`envOr` das
duas árvores de código, **recursivamente**; **proíbe** `os.Environ` (leitura por enumeração
escaparia ao gate por construção); e exige, para cada variável, uma linha de tabela **dentro
desta secção** com as células **Default e Efeito preenchidas** — uma linha degenerada
``| `AOS_X` |  |  |`` **não** conta como documentação.

As cinco variáveis com secção própria abaixo (estado durável, plano de controlo) têm aqui a linha
de índice e o detalhe lá.

| Variável | Default | Efeito |
|---|---|---|
| `AOS_MODE` | *(vazio ⇒ modo de **referência**)* | `production` (qualquer caixa) activa a **postura de produção fail-closed**. **Segurança:** é o interruptor que torna obrigatórias as duas exigências — `AOS_ISSUER_PUBKEY` (senão `ErrProductionNeedsHardenedIdentity`) e `AOS_BOARD_REGIONS` **não-vazio** (senão `ErrProductionNeedsSovereignRead`). Qualquer outro valor ⇒ modo de referência **sem** essas exigências: um nó exposto sem `AOS_MODE=production` não é um nó de produção, é um nó de referência a servir tráfego. |
| `AOS_API_ADDR` | *(vazio ⇒ **API não levantada**)* | Endereço de bind da API HTTP. Vazio ⇒ o nó faz bootstrap, declara o banner e **sai sem abrir socket**. Não-loopback ⇒ sujeito ao [bind-guardrail](#bind-guardrail-fail-closed) (recusa se não houver operadores). É também o **default do `--addr`** dos subcomandos cliente (`aos run/observe/steer/pause`) e a fonte da porta do `HEALTHCHECK`. |
| `AOS_ISSUER_ID` | `iss:aos-node` | Identificador da autoridade de identidade — **é o trust anchor** que o verifier exige no `iss` de cada credencial. **Segurança/operação:** no modo endurecido tem de ser **exactamente** o issuer que emitiu os tokens (o par `(AOS_ISSUER_ID, AOS_ISSUER_PUBKEY)` é o anchor completo); um valor errado não abre nada — faz o nó **rejeitar todas** as credenciais legítimas (fail-closed, mas silencioso do lado da config). Não é segredo: é um nome. |
| `AOS_ISSUER_PUBKEY` | *(vazio ⇒ modo de **referência**, autoridade **co-localizada**)* | Pubkey ed25519 do issuer em hex (**64 hex chars = 32 bytes**). Presente ⇒ **trust-anchor-only endurecido**: o nó compõe só o verifier e **nenhuma chave de assinatura entra no processo**. Malformada ⇒ **ABORTA** (`ErrBadIssuerPubKey`). Material **público** — pode viver na receita de deployment. |
| `AOS_ISSUER_KEY_PATH` | *(vazio ⇒ chave gerada por **CSPRNG a cada arranque**)* | **Só no modo de referência.** Ficheiro de *seed* ed25519 que a autoridade co-localizada carrega/persiste, para que os tokens emitidos **sobrevivam ao reinício**. ⚠️ **É o único caminho por onde material PRIVADO entra no processo do nó** — monte-o read-only e fora da imagem, e prefira o modo endurecido. Com `AOS_ISSUER_PUBKEY` definida esta variável **nem é lida** (no modo endurecido nenhuma chave de assinatura entra; um `Config` composto in-process com ambas aborta com `ErrConflictingIssuerKey`). |
| `AOS_HUMANS` | `operator` | Lista CSV dos **humanos autorizados** na allowlist da autoridade de identidade **de referência** (`integration.NewAllowlistDirectory`) — a raiz de delegação de onde a autoridade minta. **Só tem efeito no modo de referência**: no modo endurecido o directório de humanos vive **com a autoridade externa** e a variável é ignorada. **Fail-closed:** no modo de referência, uma lista definida mas **sem nenhuma entrada válida** (ex.: `AOS_HUMANS=","`) ⇒ **ABORTA** (`ErrNoHumans`) — a autoridade não teria quem autenticar. É `DEMO-GRADE-AUTH`: uma allowlist de nomes, **não** autenticação (OIDC/WebAuthn é a porta por preencher, EPIC-16); o banner declara a cardinalidade (`humanos autorizados na autoridade: N`). |
| `AOS_BOARD_REGIONS` | *(**não definida** ⇒ `board:aos-demo=eu`, soberania de leitura **LIGADA**)* | Registo `board=regiao,board2=regiao2` da soberania de leitura. **Impacto de conformidade — três estados, incluindo um kill-switch:** ver [Soberania de leitura](#soberania-de-leitura--aos_board_regions-e-o-kill-switch-aos-172--d7-endurecido-em-aos-203). |
| `AOS_EVENTSTORE_PATH` | *(vazio ⇒ Event Store **in-memory**)* | Estado durável — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180). |
| `AOS_WORM_PATH` | *(vazio ⇒ WORM **in-memory**)* | Trilho WORM tamper-evident — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180). **Conformidade:** in-memory, o trilho de auditoria **não sobrevive** ao contentor. |
| `AOS_DURABLE_EXECUTION` | *(vazio ⇒ **DESLIGADA**)* | Execução durável — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180) e a [postura de produção](#postura-de-produção-de-aos_durable_execution--decisão-aos-203) decidida em AOS-203. |
| `AOS_OPERATORS` | *(vazio ⇒ **default-deny**)* | Pubkeys dos operadores do canal de controlo — ver [Plano de controlo](#plano-de-controlo--operadores-e-aprovadores-aos-160--aos-162-config-em-aos-193). **Segurança:** vazio ⇒ `steer`/`pause` recusados **e** bind não-loopback recusado. |
| `AOS_APPROVERS_FILE` | *(vazio ⇒ **four-eyes DESLIGADO**)* | Ficheiro JSON montado com a *roster* do dual-control — ver [Plano de controlo](#plano-de-controlo--operadores-e-aprovadores-aos-160--aos-162-config-em-aos-193). |
| `AOS_OTLP_ENDPOINT` | *(vazio ⇒ **`NoopTracer`**, zero overhead)* | URL http(s) **absoluto** do colector OTLP/HTTP (ex.: `http://collector:4318`; o nó completa com `/v1/traces`). Presente ⇒ exporta os spans `invoke_agent`/`chat`[+custo]/`execute_tool`/`freeze` e os selos WORM. Um endpoint **malformado ABORTA** o arranque (`ErrBadOTLPEndpoint`) — o nó não sobe a fingir que exporta. A exportação em si é **fail-open** (a telemetria nunca derruba o nó). **Privacidade:** os spans transportam metadados de governação e custo, não conteúdo de *prompts*; ainda assim o destino é uma fronteira de dados — aponte-o para dentro do seu perímetro. |
| `AOS_READER` | *(vazio)* | **Lado CLIENTE** (`aos observe`): default da flag `--reader`, transportada no header `X-Aos-Reader`. É a **identidade de leitura** declarada pelo cliente; com a soberania de leitura ligada, o **nó** é que a exige e a resolve — a CLI só a transporta. Ausente contra um nó soberano ⇒ `404`. |
| `AOS_BOARD` | *(vazio)* | **Lado CLIENTE** (`aos observe`): default da flag `--board`, transportada no header `X-Aos-Board`. Board de governação do leitor, de onde o nó resolve a **região autorizada**. Ausente ou desconhecido contra um nó soberano ⇒ `404` (fail-closed). |
| `AOS_HEALTH_URL` | *(vazio ⇒ derivada de `AOS_API_ADDR`)* | **Override opcional** do URL sondado pelo `aos-healthprobe` do `HEALTHCHECK` (lida por `deploy/node/healthprobe`, **não** pelo nó). Sem ela o probe deriva `127.0.0.1:<porta de AOS_API_ADDR>/healthz` — ver [Health / probes](#health--probes). |

> **Nenhuma destas variáveis transporta segredos**, com a excepção declarada de
> `AOS_ISSUER_KEY_PATH` (que transporta um **caminho** para material privado, não o material). O
> banner de arranque não ecoa valores de chaves: as mensagens de erro de `AOS_OPERATORS` e do
> ficheiro de aprovadores identificam a entrada pelo `emitterID`/`principal` e **nunca** imprimem
> a pubkey.

> **Precedência e formato.** Todas as variáveis são lidas **uma vez, no arranque** (não há
> *reload* a quente: para mudar config, substitua o contentor). Todos os valores são
> `TrimSpace`-ados. A gramática plana `a=b,c=d` é partilhada por `AOS_BOARD_REGIONS` e
> `AOS_OPERATORS`, deliberadamente.

### Soberania de leitura — `AOS_BOARD_REGIONS` e o kill-switch (AOS-172 / D7, endurecido em AOS-203)

`AOS_BOARD_REGIONS` tem **três** estados, e a diferença entre "não definida" e "definida vazia"
é a diferença entre um controlo de conformidade **ligado** e **desligado**:

| Estado da variável | Registo `board→região` | Read-path |
|---|---|---|
| **NÃO definida** (ausente do ambiente) | default de referência `board:aos-demo=eu` | **SOBERANO** — authz por-chamador (D7) + selo WORM da leitura sensível (D6) |
| **DEFINIDA VAZIA** (`-e AOS_BOARD_REGIONS=`) | vazio | **LEGADO** — ⚠️ **KILL-SWITCH**: sem authz por-chamador e **sem selo** |
| **DEFINIDA com valor** (`board:prod=eu`) | o que for declarado | **SOBERANO** |
| **DEFINIDA malformada** (`aos-demo`, sem `=`) | — | **ABORTA** o arranque (`ErrBadBoardRegions`) |

**O que o kill-switch desliga**, exactamente:

1. **Authz POR-CHAMADOR das leituras de governação (D7).** Com ele desligado o nó serve
   **todas** as leituras sem exigir `X-Aos-Reader`/`X-Aos-Board` e sem resolver a região
   autorizada do board do leitor — qualquer chamador que alcance a porta lê qualquer *run*.
2. **Selo WORM da leitura sensível (D6).** Deixa de existir trilho *tamper-evident* de **quem
   leu o quê** — a evidência de acesso a dados de governação desaparece, não fica degradada.

**Postura por modo — o que este ticket mudou e o que não mudou:**

- Em **`AOS_MODE=production`** o estado vazio **RECUSA o arranque** (`ErrProductionNeedsSovereignRead`,
  `exit 1`). **Isto já existia e não foi tocado**: um nó de produção nunca serve o read-path legado.
- **Fora de produção** o estado vazio continua **permitido** (os *harnesses*
  `aos169-durability-harness.sh` e `aos193-control-plane-harness.sh` usam-no deliberadamente, para
  isolarem o eixo que testam) — mas **deixou de ser silencioso**. O banner passa a emitir um aviso
  proeminente (AOS-203):

```text
[aos] AVISO KILL-SWITCH (AOS-203): SOBERANIA DE LEITURA (AOS-172, D7) DESLIGADA — AOS_BOARD_REGIONS esta DEFINIDA-VAZIA (kill-switch explicito: a variavel existe no ambiente com valor vazio)
[aos] => FICA DESLIGADO: (1) AUTHZ POR-CHAMADOR das leituras de governacao (D7) — o no serve TODAS as leituras sem exigir X-Aos-Reader/X-Aos-Board nem resolver a regiao autorizada do board; (2) SELO WORM da leitura sensivel (D6) — nao fica trilho tamper-evident de QUEM leu o que
[aos] => PARA RELIGAR: defina AOS_BOARD_REGIONS="board=regiao" (ex.: AOS_BOARD_REGIONS="board:prod=eu") ou REMOVA a variavel do ambiente para voltar ao default de referencia "board:aos-demo=eu"
[aos] => IGNORE a linha "defina Config.BoardRegions" do banner acima: Config.BoardRegions e um campo de codigo (package main) que quem corre o binario/imagem NAO consegue escrever — o unico remedio alcancavel e AOS_BOARD_REGIONS, na linha anterior
[aos] => AOS_MODE=production RECUSA arrancar neste estado (ErrProductionNeedsSovereignRead) — este aviso so existe porque o no NAO esta em modo de producao
```

> ⚠️ **Uma linha do banner ainda aponta para um remédio inalcançável (residual conhecido).** Umas
> linhas acima do aviso, o *composition-root* imprime `soberania de leitura (AOS-172, D7): read-path
> LEGADO (sem authz por-chamador nem selo) — defina Config.BoardRegions …`. **`Config.BoardRegions`
> é um campo de código** (`package main`): quem corre o binário ou a imagem **não o consegue
> escrever**. É metade do próprio defeito que esta secção fecha — sintoma verdadeiro, remédio
> impossível. Enquanto essa linha não for reescrita (exige tocar em `packages/cmd/aos/bootstrap.go`,
> fora da propriedade de ficheiros de AOS-203), o aviso **neutraliza-a pelo nome** — é a linha
> `IGNORE a linha "defina Config.BoardRegions"` acima. **Se fizer `grep` ao banner, leia o bloco
> `AVISO KILL-SWITCH` inteiro, não só a linha do `read-path LEGADO`.**

> **O gate real do read-path são DUAS coisas.** A *read-governance* só é composta quando o registo
> `board→região` **e** o WORM existem ambos (o selo D6 não teria onde ser gravado). Hoje o
> `Bootstrap` nunca deixa o WORM ausente (cai para um WORM in-memory), pelo que a distinção não é
> alcançável por configuração; ainda assim o aviso avalia **a conjunção**, não só o registo — se um
> dia o WORM se tornar opcional, o nó **avisa** em vez de anunciar uma soberania que não aplica.

> **Porquê avisar e não recusar fora de produção?** Recusar quebraria a retro-compatibilidade da
> superfície de configuração e cortaria um estado que o próprio projecto usa nos *harnesses*. O
> critério é o de AOS-191: o que não se tolera é a **promessa falsa** — um nó que anuncia uma
> postura mais forte do que a que cumpre. Daí o aviso nomear, sem eufemismo, o que ficou
> desligado. Em produção, onde a promessa é implícita, a resposta continua a ser **recusar**.

> **Âmbito honesto do que fica ligado.** Com o registo não-vazio, o selo D6 grava a região do
> **board do leitor**, não a residência **por-run** do dado; a verificação
> `leitor.região == run.região` fica **DEFERIDA** até haver `board→região` por-*run* (EPIC-09/10),
> e o banner declara-o. O provisioning real de regiões/boards (IdP de soberania) é igualmente
> deferido: o registo aqui é **DEMO-GRADE self-hosted**. A **regra** fail-closed é que é fixa.

### Estado durável — variáveis de ambiente (AOS-170 / AOS-180)

| Variável | Default | Efeito |
|---|---|---|
| `AOS_EVENTSTORE_PATH` | *(vazio)* | **Vazio ⇒ Event Store IN-MEMORY** (volátil: perde tudo quando o processo/contentor morre). Definido ⇒ Event Store **durável** (WAL append-only + `fsync` + replay crash-safe no arranque) no caminho dado. **Tem de apontar para DENTRO do mount gravável** (`-v aos-data:/var/lib/aos`, ex.: `/var/lib/aos/events.wal`). |
| `AOS_WORM_PATH` | *(vazio)* | Vazio ⇒ WORM in-memory. Definido ⇒ trilho WORM **hash-chain tamper-evident** em disco. Mesmo requisito de mount (ex.: `/var/lib/aos/worm.wal`). |
| `AOS_DURABLE_EXECUTION` | *(vazio ⇒ **DESLIGADA**)* | Ligam: `1` `true` `t` `yes` `y` `on`. Desligam: `0` `false` `f` `no` `n` `off` (ou ausente/vazia). **Qualquer outro valor ABORTA o arranque** (`enabled`, `tru`, `sim`, … **não** são tratados como `false` — ver abaixo). Ligada ⇒ o nó compõe **checkpointer + capturer de não-determinismo + step-ledger** sobre o Event Store; o tool set congelado (AOS-155) passa a persistir no mesmo store. Desligada ⇒ os três ficam `nil` e o runtime usa os defaults no-op (AOS-013). |

**Interacção `AOS_DURABLE_EXECUTION` × `AOS_EVENTSTORE_PATH` — fail-closed SEMPRE.**
`AOS_DURABLE_EXECUTION=1` **sem** `AOS_EVENTSTORE_PATH` **RECUSA o arranque** (`exit 1`), em
**qualquer** modo — não só em `AOS_MODE=production`. Razão: a execução durável compõe-se
**sobre** o Event Store; sobre um store in-memory os checkpoints, as capturas e o step-ledger
**evaporariam no reinício** e o nó anunciaria uma durabilidade que não cumpre. A ambiguidade
**nega** o arranque em vez de degradar em silêncio — a mesma postura de `AOS_BOARD_REGIONS`
malformado. Um valor não-booleano da própria variável aborta pela mesma razão: quem escreveu
`AOS_DURABLE_EXECUTION=enabled` **tenciona** ligar a durabilidade, e receber um nó silenciosamente
não-durável seria pior do que não arrancar.

> **O que a guarda NÃO consegue detectar.** Ela só vê a *ausência* de caminho. Um
> `AOS_EVENTSTORE_PATH=/tmp/events.wal` — ou qualquer caminho **fora** de `-v aos-data:/var/lib/aos`,
> incluindo o `--tmpfs /tmp` das receitas acima — **passa** a guarda e continua a perder tudo
> quando o contentor é substituído. Apontar `AOS_EVENTSTORE_PATH` e `AOS_WORM_PATH` para dentro do
> volume nomeado é **responsabilidade do operador**; é por isso que está documentado aqui e não só
> imposto em código.

**Verificação pelo operador** — o banner de arranque declara o estado **realmente composto** (não a
intenção da config); uma destas duas linhas sai sempre:

```text
[aos] execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger COMPOSTOS sobre o event store (duravel em disco (AOS-170)); o tool set congelado (AOS-155) persiste no mesmo store
[aos] execucao duravel (AOS-180): DESLIGADA — checkpointer/capturer/step-ledger NAO compostos (defaults no-op AOS-013); defina AOS_DURABLE_EXECUTION=1 (exige AOS_EVENTSTORE_PATH) para ligar
```

A linha `[aos] substrato: ...` imediatamente antes diz `duravel em disco (AOS-170)` ou
`in-memory de referencia (nao-duravel)` — confirme-a **antes** de assumir que o estado sobrevive
a um reinício.

#### Postura de produção de `AOS_DURABLE_EXECUTION` — decisão (AOS-203)

**Decisão: mantém-se OPT-IN, também em `AOS_MODE=production`.** Um nó de produção **arranca sem
execução durável**. A assimetria face às outras duas posturas de produção
(`ErrProductionNeedsHardenedIdentity`, `ErrProductionNeedsSovereignRead`) é **deliberada e
registada aqui**, não tácita — AOS-191 deixou-a em aberto com eixo neste ticket, e é este o
registo que a fecha.

**Critério que separa os três casos: a promessa falsa.**

| Postura | Estado desligado em produção | Porquê |
|---|---|---|
| `AOS_ISSUER_PUBKEY` | **RECUSA** | O nó **serviria** identidade com a autoridade co-localizada — uma postura mais fraca do que a que um nó de produção implicitamente anuncia. |
| `AOS_BOARD_REGIONS` | **RECUSA** | O nó **serviria** leituras sem authz por-chamador nem selo — o mesmo tipo de promessa falsa, sobre um controlo de conformidade. |
| `AOS_DURABLE_EXECUTION` | **permite** (declara `DESLIGADA`) | O nó **não anuncia** durabilidade nenhuma. O banner diz `execucao duravel (AOS-180): DESLIGADA` em cada arranque, e nenhum endpoint promete sobrevivência de *checkpoints*. Não há capacidade anunciada e não cumprida — há uma capacidade **declaradamente ausente**. |

Os dois argumentos secundários, subordinados ao critério acima: (i) exigi-la converteria um
ticket de **superfície de configuração** numa mudança de postura de produção não anunciada aos
operadores existentes — a retro-compatibilidade que AOS-191 impôs; (ii) o eixo perigoso — ligar a
durabilidade sobre um substrato volátil — **já é fail-closed em qualquer modo**
(`AOS_DURABLE_EXECUTION=1` sem `AOS_EVENTSTORE_PATH` aborta), que é onde a promessa falsa
realmente estaria.

> **Consequência para o operador, dita sem rodeios:** se quer que um *run* interrompido retome
> onde ia — em vez de recomeçar — **tem de a ligar explicitamente**, mesmo em produção. Ligue
> `AOS_DURABLE_EXECUTION=1` com `AOS_EVENTSTORE_PATH` dentro do volume gravável, e confirme a
> linha `LIGADA` no banner. Nada no nó a liga por si.

### Plano de controlo — operadores e aprovadores (AOS-160 / AOS-162, config em AOS-193)

O canal de controlo (`POST /runs/{id}/steer`, `/pause`) e o *four-eyes* (`/approve`) são
**default-deny**: sem configuração, **nenhum** sinal autentica e `/approve` responde `501`. Estas
duas variáveis são o **único** caminho para os ligar no binário entregue.

| Variável | Default | Efeito |
|---|---|---|
| `AOS_OPERATORS` | *(vazio ⇒ **default-deny**)* | Registo `emitterID=hexpubkey,emitterID2=hexpubkey2` das **pubkeys** ed25519 dos operadores autorizados a emitir `steer`/`pause`. `hexpubkey` = **64 hex chars = 32 bytes**, a mesma codificação de `AOS_ISSUER_PUBKEY`. Vazio ⇒ o canal fica composto mas **inoperável** (todo o sinal leva `403`) **e o bind não-loopback é RECUSADO** (ver abaixo). |
| `AOS_APPROVERS_FILE` | *(vazio ⇒ **four-eyes DESLIGADO**)* | Caminho de um **ficheiro JSON montado** com a *roster* de aprovadores do dual-control. Vazio ⇒ o `FourEyesGate` não é composto e `POST /runs/{id}/approve` responde `501` (desligado **por declaração**, não por avaria). |

**Fail-closed, sem degradação silenciosa** (a postura de `AOS_BOARD_REGIONS`/`AOS_DURABLE_EXECUTION`):
uma entrada sem `=`, um `emitterID` vazio, uma pubkey que não seja hex de 32 bytes, ou um
`emitterID` **duplicado** ⇒ o arranque **ABORTA** (`exit 1`). Não se "registam os que der": um
operador silenciosamente descartado daria um nó que arranca a anunciar um canal de controlo e
depois recusa **todos** os sinais desse operador com `403`. O duplicado aborta em vez de "o último
ganha" — dois valores para o mesmo `emitterID` são um conflito de autoridade, não uma preferência.
O ficheiro de aprovadores segue a mesma regra: ilegível, JSON inválido, **campo desconhecido**
(esquema em *drift*), lista vazia, principal duplicado ou autoridade vazia ⇒ **ABORTA**.

Duas regras adicionais, que valem **nos dois** caminhos (env e ficheiro) e também para quem compõe
`Config` in-process — o `Bootstrap` impõe-as antes de compor seja o que for:

- **Material de chave não se partilha.** Duas entradas com a **mesma pubkey** ⇒ **ABORTA**, mesmo
  com identificadores diferentes. Nos *aprovadores* isto é **segurança**: a distinção do
  dual-control compara `approver`/`session`/`credential` — três *strings* que o **cliente** escolhe
  na perna —, pelo que a pubkey **pinada** é a única âncora criptográfica de "duas pessoas"; colada
  em duas linhas, **uma** chave privada assina as **duas** pernas e o 4-eyes é anulado em silêncio,
  com o banner a declarar "2 aprovador(es) pinados". Nos *operadores* é **atribuição**: um
  `aos steer --emitter ops:bob` assinado pela chave de `ops:alice` seria aceite e **selado no WORM**
  como sendo de `ops:bob` — o nome do emissor deixaria de ser evidência.
- **`authority` tem vocabulário fechado:** `approve:safe`, `approve:gray`, `approve:danger` (é o que
  `hitl.RequiredAuthority` produz, uma por classe de risco). Qualquer outro valor —
  `approve:dangerous`, `approve:*`, `approver:danger` — ⇒ **ABORTA**. A comparação em runtime é de
  *string* **exacta, sem wildcards**: um *typo* seria *fail-closed* mas **silencioso** — um aprovador
  contado no banner que nunca aprova nada.

> **Só entra material PÚBLICO.** A chave **privada** do operador vive na máquina do operador — é lá
> que `aos steer`/`aos pause` assinam (`--key <ficheiro-da-seed>`); o nó só detém pubkeys e por isso
> **verifica mas não forja**. **Limite honesto:** uma *seed* ed25519 tem também 32 bytes, pelo que o
> nó **não a distingue** estruturalmente de uma pubkey. Colar a seed em `AOS_OPERATORS` produz um
> registo que nunca valida assinatura nenhuma (fail-closed, sem elevação de privilégio) — mas terá
> exposto a chave privada ao ambiente do nó. **Derive sempre a pubkey**, não copie a seed.

Derivar a entrada a partir da seed do operador (só stdlib, sem ferramenta externa):

```bash
aos operator-pubkey --key ./operator.seed --emitter ops:alice
# ops:alice=1f8b…  (64 hex chars)  ← o valor a pôr em AOS_OPERATORS
```

Formato do ficheiro de `AOS_APPROVERS_FILE` (monte-o read-only, ex.: `-v $PWD/approvers.json:/etc/aos/approvers.json:ro`):

```json
{
  "approvers": [
    {"principal": "human:alice", "pubkey": "<64 hex>", "authority": ["approve:danger", "approve:gray"]},
    {"principal": "human:bob",   "pubkey": "<64 hex-DIFERENTE>", "authority": ["approve:danger"]}
  ]
}
```

> **Wire de `POST /runs/{id}/approve`:** `risk_class` é o valor **numérico** de `risk.Class` —
> **`0` = `danger`** (é o **valor-zero**, escolhido para que uma classe não computada seja tratada
> como o pior caso), `1` = `safe`, `2` = `gray`. A capability exigida ao aprovador é
> `approve:<classe>` da classe **do pedido**; um aprovador só com `approve:gray` **não** autoriza um
> pedido `risk_class: 0`.

> **Porquê ficheiro aqui e env ali?** `AOS_OPERATORS` é um mapa `id→escalar` e cabe sem perda na
> gramática plana que o nó já usa (`AOS_BOARD_REGIONS`). Um aprovador **não** é escalar — traz
> `authority[]` —, e espremê-lo numa env exigiria um terceiro nível de delimitador, ilegível e
> irrevisível. Um ficheiro montado é a via que o **ADR-017 ponto 2** já prevê ("config por
> env/ficheiro montado") e é versionável em *code-review*, que é o que uma *roster* de dual-control
> deve ser. Cada colaborador tem **um** caminho de configuração: não há precedência env-vs-ficheiro
> a divergir em silêncio. A codificação do material público (hex de 32 bytes) e a disciplina
> fail-closed são **as mesmas** nos dois.

**Verificação pelo operador** — o banner declara o estado **realmente composto**:

```text
[aos] canal de controlo: Ed25519Authenticator (AOS-160) — 2 operador(es) registado(s) via AOS_OPERATORS; HMACAuthenticator demo DESLIGADO
[aos] canal de controlo: Ed25519Authenticator (AOS-160) composto mas SEM OPERADORES — steer/pause serao TODOS recusados (ErrUnknownEmitter) e o bind NAO-loopback e RECUSADO; defina AOS_OPERATORS="emitterID=hexpubkey" para o tornar operavel
[aos] four-eyes gate (AOS-162) composto: 2 aprovador(es) pinado(s) via AOS_APPROVERS_FILE
[aos] four-eyes gate (AOS-162): DESLIGADO (sem aprovadores) — POST /runs/{id}/approve responde 501; defina AOS_APPROVERS_FILE=<ficheiro JSON montado> para o compor
```

### Bind-guardrail (fail-closed)

A API **recusa** bind a um endereço **não-loopback** (`0.0.0.0`, `:8080`, um IP público, ou um
*hostname* não confirmável como loopback) enquanto o **canal de sinais (`steer`/`pause`)** não
estiver **autenticado E operável**. A condição imposta pelo código é a **conjunção** de três coisas:

1. o autenticador ed25519 do canal de controlo está composto (`SteerAuth != nil`);
2. o modo de identidade é real (`real` ou `real-trust-anchor-only`);
3. **existe pelo menos um operador com pubkey registada** (`AOS_OPERATORS` não-vazia).

Falhar qualquer uma ⇒ `ErrRefuseNonLoopbackBind` **antes** do `Listen` (o socket nem chega a
abrir); o *loopback* continua sempre permitido. O log da recusa nomeia o modo de identidade e a
**cardinalidade de operadores**, que é a causa esmagadoramente mais provável.

> **Âmbito exacto (não é omissão):** o predicado **não** olha para os aprovadores. Um nó
> configurado **só** com `AOS_APPROVERS_FILE` tem o `/approve` plenamente operável e é, ainda
> assim, recusado no bind não-loopback — `AOS_OPERATORS` é obrigatória. A escolha é a
> conservadora: `steer`/`pause` são a superfície de **intervenção** (parar um *run* a correr), a
> que justifica expor a porta à rede. Por isso a mensagem do erro diz **"steer/pause
> INOPERAVEIS"** e não "canal de controlo não operável" — o texto nomeia o que a condição
> verifica, nem mais nem menos.

> ⚠️ **MUDANÇA DE COMPORTAMENTO (AOS-193) — leia se já faz bind não-loopback.** Até AOS-193 o
> guardrail exigia só (1)∧(2) — duas condições que o `Bootstrap` satisfaz **sempre**, pelo que a
> condição era **identicamente verdadeira** e nunca recusava nada. Um nó que hoje sobe em
> `0.0.0.0` **sem** `AOS_OPERATORS` **passa a RECUSAR arrancar** (`exit 1`). A correcção é
> acrescentar `-e AOS_OPERATORS="<id>=<hexpubkey>"`. É deliberado: expor à rede um plano de
> controlo que não consegue aceitar **um único** sinal legítimo dá toda a superfície de ataque e
> nenhum benefício. Quem precise de manter o comportamento anterior sem operadores tem uma opção
> honesta — fazer bind ao **loopback** e publicar a porta pelo orquestrador.

Para servir tráfego externo endurecido:

```bash
docker run --read-only --tmpfs /tmp -v aos-data:/var/lib/aos -p 8080:8080 \
  -e AOS_MODE=production \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_ISSUER_PUBKEY=<hex-32B-ed25519>   `# trust-anchor-only; a CHAVE PRIVADA fica no vault` \
  -e AOS_OPERATORS="ops:alice=<hex-32B-ed25519>"   `# PUBKEY do operador; a privada fica na maquina dele` \
  -e AOS_BOARD_REGIONS="board:prod=eu" \
  -e AOS_EVENTSTORE_PATH=/var/lib/aos/events.wal \
  -e AOS_WORM_PATH=/var/lib/aos/worm.wal \
  -e AOS_DURABLE_EXECUTION=1 \
  aos-node:local
```

Prova executável desta secção: [`aos193-control-plane-harness.sh`](aos193-control-plane-harness.sh)
— arranca o **contentor real** em `0.0.0.0` sem operadores (recusa), depois com um operador
(arranca) e envia um `aos steer` assinado (aceite); um emissor não registado leva `403`. O mesmo
contentor monta um `approvers.json` read-only (`-v … -e AOS_APPROVERS_FILE=…`): o banner declara o
*four-eyes* **composto** e `/approve` passa a **julgar** (`403` sem pernas válidas, já não `501`);
e um *roster* com a **mesma pubkey em dois principals** faz o nó **recusar arrancar**. A prova
positiva do `200` (duas pernas assinadas por **duas** chaves distintas ⇒ `authorized`) é
in-process, em `TestApproversFileAuthorizesDualControlEndToEnd` — assinar como aprovador é papel
do **dispositivo do humano**, que a CLI do nó deliberadamente não desempenha.

Os três últimos ligam o **estado durável** e a **execução durável** nos caminhos do volume
`aos-data` — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180) para a
semântica e o fail-closed. **A execução durável é opt-in mesmo em `AOS_MODE=production`**: o nó
arranca sem ela (declarando `DESLIGADA` no banner, sem anunciar durabilidade nenhuma). A promoção
a exigência de produção, a par de `AOS_ISSUER_PUBKEY`/`AOS_BOARD_REGIONS`, foi **decidida em
AOS-203 — mantém-se opt-in**, com o critério registado em
[Postura de produção de `AOS_DURABLE_EXECUTION`](#postura-de-produção-de-aos_durable_execution--decisão-aos-203).

O `HEALTHCHECK` deriva a porta de `AOS_API_ADDR` (aqui, `8080`) e sonda `127.0.0.1:8080/healthz`
no loopback do contentor — não é preciso definir `AOS_HEALTH_URL`.

**Sem segredos na imagem**: `AOS_ISSUER_PUBKEY` é material **público** (trust anchor). A chave de
assinatura do issuer (AOS-156) é um trust-domain separado (ADR-017 ponto 5) — vem do vault/KeyVault
em runtime (ADR-006), nunca da imagem.

## Health / probes

- Container `HEALTHCHECK`: binário estático `aos-healthprobe` (distroless não tem shell/curl) →
  `GET /healthz` (liveness, AOS-171). O probe **deriva a porta de `AOS_API_ADDR`**
  (`127.0.0.1:<porta>/healthz`), pelo que segue automaticamente qualquer porta não-8080 — sem
  acoplamento silencioso a uma segunda variável. `AOS_HEALTH_URL` é um **override opcional**.
- **Kubernetes**: prefira sondas `httpGet` nativas — `livenessProbe` em `/healthz`,
  `readinessProbe` em `/readyz` (drain-aware).

## Entrega fail-closed (ADR-017 ponto 4)

```bash
bash scripts/ci/package.sh      # secrets + sast + sca + sbom + docker build
bash scripts/ci/sbom.sh         # só SBOM + proveniência (deploy/node/build/)
```

Reutiliza `sast.sh`/`sca.sh`/`secrets.sh` (baseline **multiset**, nunca `sort -u`). Uma descoberta
nova fora da baseline **avermelha**.

## Repin dos digests

As bases estão **pinadas por digest** (não só tag). Para actualizar:

```bash
docker pull golang:1.24.5-bookworm && docker image inspect --format '{{index .RepoDigests 0}}' golang:1.24.5-bookworm
docker pull gcr.io/distroless/static-debian12:nonroot && docker image inspect --format '{{index .RepoDigests 0}}' gcr.io/distroless/static-debian12:nonroot
```

Actualizar os digests em `deploy/node/Dockerfile` **e** em `scripts/ci/sbom.sh` (proveniência).
