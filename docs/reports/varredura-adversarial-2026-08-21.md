# Varredura adversarial do inventário de conceitos — 2026-08-21

Seis lentes independentes sobre `docs/conceitos-verificaveis.md` e o código que ele descreve,
seguidas de seis refutadores com o **ónus da prova invertido**: por omissão a hipótese está
refutada e o estado do documento mantém-se; só muda se a mecânica **e** a consequência forem
reproduzidas.

Das ~66 hipóteses levantadas, 18 chegaram à fase adversária. **13 sobreviveram, 5 caíram.**

> **Regra que governou tudo**, e é a do próprio inventário: um resultado verde que também
> apareceria com o mecanismo desligado não prova nada. Aplicou-se às hipóteses **e** aos testes
> que as guardam.

---

## 1. O que sobreviveu, por gravidade

### 1.1 Uma avaria do gateway sela o run como concluído com sucesso

Qualquer resposta `200` cujo corpo não produza `choices` — **incluindo um payload de erro do
provider** — resulta em `running → complete`, razão `run_complete`, texto final vazio, um turno,
**sem erro**. Reproduzido ponta a ponta pela função de composição de produção.

```
200 corpo vazio            → SELO FINAL: running → complete (run_complete)
200 choices null           → SELO FINAL: running → complete (run_complete)
200 payload de ERRO        → SELO FINAL: running → complete (run_complete)
```

O que o torna defeito e não decisão: **a defesa existe no ramo de streaming** — `CollectStream`
fabrica sempre um `Choice` e força `finish_reason="stop"`, e há um `ErrTruncatedStream` cujo
comentário diz *«nunca forjamos uma conclusão limpa»*. O runtime usa o caminho **síncrono**, onde
essa postura está invertida.

Sem compensação (a saga só dispara a partir de `failed`) e sem sinal para o operador. No log
durável, a avaria é indistinguível de um run que respondeu e concluiu.

### 1.2 A âncora de confiança da assinatura de release é redireccionável por ambiente

`ROSTER="${AOS_RELEASE_PUBKEYS:-…}"` em `verify-attestation.sh` e `sign.sh`, **sem guarda em CI**.
Demonstrado: statement forjado a atestar uma imagem inexistente, assinado por chave de atacante,
verificado contra o roster do atacante → `EXIT=0`. Contra o roster real → `EXIT=1`.

**O guarda existe no repositório e não foi aplicado aqui.** `lib.sh` define `gate_path`, cuja
docstring diz *«RECUSA o desvio em CI»*; o idioma está vivo em dois outros knobs. Consumidores de
`gate_path`: **zero**. A âncora de confiança é o botão menos protegido dos três.

### 1.3 Os 12 controlos negativos do verificador DSSE nunca correm

`discover_modules()` procura `go.mod` sob `packages/`; `scripts/ci/attest` está fora. Os únicos
usos do módulo em toda a cadeia são `go build`, nunca `go test`.

Demonstrado por mutação: neutralizar a verificação da assinatura (a) **é apanhado** pelos 12
testes — não são vacuosos; (b) **compila**, que é tudo o que a cadeia faz; (c) o binário mutado
**aceita uma falsificação total contra o roster real de produção**, com assinatura de 64 bytes de
zeros.

> Uma alteração que transforma o verificador de release num carimbo passa os 26 gates.

### 1.4 A proveniência assinada declara um builder falso

| | |
|---|---|
| O Dockerfile constrói com | `golang:1.25.13-bookworm@sha256:e401dae1…` |
| A proveniência **assinada** declara | `golang:1.24.5-bookworm@sha256:ef8c5c73…` |

Assim desde a v0.1.2. O comentário no `sbom.sh` diz «têm de casar com o Dockerfile — proveniência
honesta»: é uma frase, não um gate. A imagem de runtime casa, o que torna o desalinhamento mais
fácil de não ver.

É pior do que não ter proveniência: converte uma afirmação não verificada numa afirmação
**autenticada**.

### 1.5 `POST /dsar/erase` não confronta nada com o titular

Demonstrado com controlo:

```
GET /runs/{id} por chamador de outra região  → 404 (não pode LER)
POST /dsar/erase sobre o titular dessa região → 200 "erased"  (pode DESTRUIR)
CONTROLO: board desconhecido → 403, e a KEK sobrevive
```

`authorize` resolve a região de **quem lê**; `authorizeRead` acrescenta a do **recurso**. O erase
usa a primeira. Nem capability, nem `authority.json`, nem PDP no caminho.

**Custo real da correcção:** não existe conceito de residência **por titular** — só por run. A
confrontação teria de ser construída, não activada.

### 1.6 A cadeia sela `key_destroyed` para chaves vivas, e o desmentido é volátil

O `KeyVault.Delete` não tem canal de erro; o selo precede a confirmação.

```
/dsar/erase com vault que aceita o DELETE e mantém a chave:
  HTTP 500 ao requerente          ← o chamador NÃO recebe afirmação falsa
  WORM: dsar.key_destroyed / allow ← byte-idêntico ao caso honesto
  prontidão: 1 chave por confirmar
DEPOIS DE UM RESTART:
  chave viva? true    prontidão? VERDE    por confirmar: 0
```

O contador vive em memória. O log fica a afirmar sozinho, com um registo indistinguível de uma
destruição real.

### 1.7 Um shred falhado nunca é re-tentado — e o restart torna isso permanente

Ordem em `expiration.go`: sela → marca idempotência → **só então** chama o sink. A marca é
in-memory, mas a re-hidratação reconstrói o *seen-set* **a partir dos próprios eventos selados**.

```
passagem 2:                    tentado outra vez? false
após restart COM re-hidratação: tentado outra vez? false
```

Ter selado «expirado» torna a marca permanente.

### 1.8 Duas vias humanas de destruição não selam quem as accionou

```
governance.dsar      dsar.key_destroyed  Principal.NHIID=""
governance.legalhold legalhold:place     Principal.NHIID="nhi:reader-eu"   ← reversível
governance.retention retention:sweep     Principal.NHIID="nhi:aos-node/…"  ← automático
```

Pior no `/dsar/expire`: não sela atribuição **nenhuma** — `sealRetentionSweep` só tem chamadores
no varredor automático, que recusa correr sem ter selado quem é.

> **A auditoria é mais forte no que se pode desfazer do que no que não se pode.**

### 1.9 Um *legal hold* respondido `200` não protege durante a janela do selo

Medido, não estimado:

```
selo do retention.expired  : ~30 ms  ← janela held()→destruição, POR REGISTO
POST /dsar/hold            : 21–58 ms ← hold pedido, ainda NÃO em vigor
```

Presos a `fsync`, não encolhem, e acumulam N×30 ms. Demonstrado: hold selado no WORM, `200` ao
operador, material destruído na mesma — e o relatório declara `Held=0`, **afirmando que nenhuma
preservação foi respeitada**.

O guard `expireInFlight` serializa varrimento-vs-varrimento e nada mais.

### 1.10 O número de aprovadores é escolhido pelo cliente

`dual_control_required` é um campo do corpo do pedido, passado directo ao gate. O classificador de
reversibilidade que o nó tem nunca é consultado para essa decisão. Está provada a **cerimónia** de
duas pernas; não a **obrigatoriedade**.

### 1.11 Divergência de atribuição na segunda cerimónia

`Put` ignora o `Status` que o `Consume` inspecciona. Mesma preview, par de aprovadores diferente:

```
cerimónia 1 (alice,bob)  → grant persistido
cerimónia 2 (alice,carol) → 200 OK
VerifyApproval: destrava, PROVA REGISTADA approvers=[alice bob]
```

A amarra de preview é fail-closed, portanto **não** há escalada. Há sucesso falso a dois humanos e
uma prova que nomeia quem não assinou.

### 1.12 Duas retomas concorrentes deixam um fantasma

`delete` sem CAS e reposição incondicional do estado velho. O `GET /runs/{id}` responde
`waiting_on_human` para um run `complete`, e o `RunID` fica bloqueado. A única escotilha exige
estado durável `waiting_on_human` — o do fantasma é `complete`, logo responde 409.

Afinação honesta do refutador: **permanente durante a vida do processo**; um restart limpa-o.

### 1.13 Três tectos temporais não são exercidos por teste nenhum

Mutações reproduzidas de forma independente, em cópia isolada:

| constante | mutação | resultado |
|---|---|---|
| `assertionMaxAge` (5 min) | → 10 anos | **verde** |
| `sovereignReadDefaultMaxAge` (5 min) | → 10 anos | **verde** |
| `leeway` do verificador (60 s) | → 299 s | **verde** (300 s vermelho) |

E a guarda de «carimbo no futuro» do canal de controlo não é exercida ao nível do nó — com o
controlo que o prova: desligar a frescura **inteira** torna `cmd/aos` vermelho, logo o caminho é
exercido, mas só na direcção «velho de mais».

---

## 2. O que caiu — e porque importa tanto quanto o que sobreviveu

**O restauro de backup desfaz o crypto-shred.** Refutada. É trivialmente verdade que um backup
anterior a um apagamento contém o que foi apagado — vale para qualquer sistema. E o repositório
**declara** a janela, nomeando-a pelo artigo: a matriz de conformidade diz que backups produzidos
fora do nó ficam fora do alcance do crypto-shredding, «uma perna clássica do Art. 17». A linha
acusada é literalmente verdadeira: o *drill* **é** estanque. Sobra uma formulação imprecisa.

**O deploy aceita tags móveis.** Refutada quanto ao efeito. O caminho automático deriva do
`RepoDigests` pós-push e o `deploy.sh` resolve para digest **antes** de persistir, portanto o
contentor e a base do rollback ficam ambos presos. O efeito alegado — «o rollback deixa de ser
garantido» — não se segue.

**O banner do PDP não é evidência.** Refutada. Aquela linha **só é impressa depois** de `pdp.Open`
verificar a assinatura contra a âncora, e a versão que mostra vem do manifesto verificado. Se a
verificação falha, o nó não arranca. Ali o banner **é** evidência — ao contrário do caso do WORM,
onde a contagem de partições sairia igual de um nó que só validasse CRC.

**A cascata por classe devia ser ✅.** Rejeitada, por uma distinção mais fina: a prova de produção
exercitou o degrau da **classe** e o do **piso**, mas nunca a **precedência instância→classe**, que
é a propriedade definidora de uma cascata.

**O credential broker devia ser 💤.** Rejeitada, e a premissa do proponente estava **errada**:
`AOS_BROKER_VAULT_ADDR` passa vazia em produção. Ligá-la não acenderia a linha — continuaria a não
haver troca mediada. Isso é lacuna, não interruptor desligado.

---

## 3. Reclassificações aceites

| linha | conceito | de | para | razão |
|---|---|---|---|---|
| 96 | Tool set congelado | ✅ | 🧪 | os «0 em 23» são uma partição do mesmo censo, não um segundo braço |
| 104 | Tamper-evidence do WORM | ✅ | 🧪 | nunca adulterada no nó nem no clone; o banner diz «abortaria» |

O argumento decisivo da primeira, e vale para as duas: **o WAL sairia byte a byte igual de um nó
que congela, persiste e nunca compara na revalidação.** A observação é insensível à propriedade que
o conceito afirma.

Correcções factuais: a frase «os 11 que não estão provados» soma **10**; a linha 53 tem
codificação duplamente codificada; a linha 200 é um fragmento órfão que afirma como facto presente
o oposto do que o parágrafo acima corrige.

---

## 4. Os três padrões transversais

**Ninguém protege o recurso.** O repositório resolve muito bem a concorrência sobre a **mesma
chave** — CAS do Event Store, dedup por chave de idempotência, single-flight. Falha onde a
invariante é sobre um **recurso lógico que nenhuma chave nomeia**: o par agente/domínio, o run a
retomar, o titular sob hold, o `request_id` como identidade de uma decisão. O *nonce* e a dedup
protegem o **pedido**.

**As portas existem e estão trocadas.** A tese de partida era «falta uma porta humana». É outra: o
varredor automático regista quem destruiu e a rota manual não; mudar o TTL que dispara apagamento
em massa não exige assinatura enquanto mudar um nível de autonomia exige três coisas; esperar
quinze minutos substitui uma assinatura de operador. Cada uma é invisível de dentro do ficheiro
onde vive e evidente ao pô-las lado a lado.

**Audit-before-effect sobre um log append-only.** O facto é selado antes de o efeito ser
confirmado, e onde há remediação ela **aflora** o erro em canais voláteis — a resposta HTTP do
momento, um mapa em memória — em vez de o registar **ao lado do selo**. Só um sítio em todo o
sistema durabiliza o sinal negativo.

---

## 5. A lição de método

Os testes que guardam `assertionMaxAge` e `sovereignReadDefaultMaxAge` **derivam os seus próprios
parâmetros da constante sob teste**. Constroem o relógio a `assertionMaxAge + 10 min` e passam
`MaxAge: assertionMaxAge`.

> Não são testes fracos. São testes que **não podem falhar**.

É a forma mais pura da regra que o inventário se impõe, e apareceu no sítio onde ela devia ser mais
óbvia: nos testes escritos de propósito para guardar um valor.

E duas notas contra mim próprio, porque o método também se audita:

- Pus seis refutadores a trabalhar na **mesma árvore**. Colidiram: um apagou o ficheiro de teste de
  outro, um terceiro apanhou uma compilação partida, e um quarto teve de descartar a primeira
  execução e repetir tudo em cópia isolada — onde o baseline caiu de 118 s para 53 s, medindo o
  ruído que eu causei. Cada refutador devia ter tido o seu `git worktree` desde o início.
- Duas hipóteses que eu tinha promovido a ✅ hoje não sobreviveram à regra que eu próprio aplico.

---

## 6. O que fazer a seguir, por ordem

1. **O `200` que sela `complete`** — é o único achado onde uma avaria produz um desfecho positivo
   selado. A postura certa já está escrita no ramo de streaming.
2. **`gate_path` sobre o roster** — o guarda existe, tem docstring, e nunca foi ligado.
3. **`scripts/ci/attest` na descoberta de módulos** — os 12 controlos são bons e estão desligados.
4. **Alinhar o `BUILDER_IMAGE` com o Dockerfile**, com gate que compare os dois.
5. **Autoridade e atribuição no `/dsar/erase` e `/dsar/expire`** — a mais cara, porque exige
   construir residência por titular.
6. **Os três tectos temporais**, com testes que não derivem os parâmetros da constante sob teste.

E uma decisão que não é minha: o `environment` do deploy é um input de texto livre. Quem dispara o
workflow escolhe-o, e um environment sem *required reviewers* não tem porta de aprovação nenhuma.
