# Encerramento do EPIC-21 — remediação da auditoria RT/RM

> **Porque existe.** Doze defeitos vieram da auditoria adversarial ao Agent Runtime e ao
> Reference Monitor (`analises/09_Auditoria_RT_RM_Adversarial.md`). Fechá-los produziu **mais
> cinco**. Este documento fecha o epic: diz o que ficou feito, o que não ficou e porquê, e
> — porque o método deste epic é falsificar afirmações — **quais das minhas não sobreviveram**.
>
> Não substitui o `specs/EPIC-21_Remediacao_Auditoria_RT_RM.md`, que continua a ser a fonte
> ticket a ticket. Este é o corte transversal: o que se aprendeu a atravessar dezassete deles.
>
> **Data:** 2026-09-03 · **Branch:** `docs/auditoria-rt-rm-adversarial` · **HEAD:** `1df973c`
> **Versão navegável:** <https://claude.ai/code/artifact/5223c512-d499-4cf8-bd32-01ec2bf00c91>

---

## 0. Veredicto

| | |
|---|---|
| Tickets fechados | **17** (AOS-288..304) |
| Commits | 32 |
| Ficheiros | 100 (+7311 / −2979) |
| Linhas removidas pelas quatro remoções | 2807 |
| **Afirmações sem prova de execução** | **1** |

**A única coisa que este relatório não pode afirmar:** a AC2 do AOS-292 — o ciclo
`pause → steer → resume` por HTTP contra um nó real — tem teste escrito
(`packages/cmd/aos/aos292_retoma_live_test.go`, build-tag `aoslive`) e **nunca foi corrido**.
Exige cluster. Não é evidência de que a AC passa; é o sítio onde produzi-la.

---

## 1. O padrão que atravessa os doze

Nenhum dos doze era código simplesmente errado. Eram **propriedades que o sistema anunciava e
não cumpria** — e a forma dominante era sempre a mesma: um mecanismo composto, testado,
documentado, **e sem chamador**.

- a verificação de revogação de NHI corria dentro de um `if` sempre falso, enquanto o banner
  anunciava «revogacao» (AOS-288);
- `SteerChannel.Rebuild` tinha testes e zero consumidores de produção (AOS-293);
- a porta `engine/` sustentava os únicos `[x]` do EPIC-02 e nada a consumia (AOS-296);
- toda a cadeia de eviction — porta, `EvictToTailBudget`, sink, drenagem da fila — estava a
  zero **em simultâneo** (AOS-298);
- `worker.NewWorker` e `NewFencedAppender` não eram compostos no nó (AOS-299).

O que separa isto de uma lista de *code smells* é a auditoria ter **medido em execução**: um
token revogado aceite com `err=<nil>` e um `Principal` completo é um facto, não uma suspeita.

---

## 2. Inventário

### 2.1 Os doze da auditoria

| Ticket | P | Defeito | Estado |
|---|---|---|---|
| AOS-288 | P0 | A verificação de revogação de NHI nunca corre, e o banner anuncia que corre | implementado (3 ressalvas) |
| AOS-289 | P1 | O `admit()` do replay aceita uma captura com menos resultados do que tool calls | implementado, nos dois caminhos |
| AOS-290 | P0 | O texto claro do step-ledger fica fora do alcance do crypto-shredding | implementado (AC2 re-fundamentada) |
| AOS-291 | P1 | O mutex do disjuntor cobre I/O durável e o `AlertSink` | implementado (AC2 em parte ⇒ AOS-301) |
| AOS-292 | P1 | A retoma contorna o canal de steer e não consome a correcção | implementado; **AC2 não corrida** |
| AOS-293 | P2 | A projecção do canal de controlo não é reconstruída | implementado |
| AOS-294 | P3 | A tabela de `neutralizarDelimitadores` contradiz a função que ilustra | implementado |
| AOS-295 | P3 | O deferimento é declarado sem a ressalva de modo | implementado |
| AOS-296 | P2 | A porta `engine/` não tem consumidor, e sustenta os únicos `[x]` do EPIC-02 | **removido** (1113 linhas) |
| AOS-297 | P3 | `WithLeaseHeartbeat` aceita um intervalo superior ao TTL sem validar | implementado |
| AOS-298 | P2 | Uma divergência de replay por eviction sairia inatribuível | **porta removida** |
| AOS-299 | P2 | As escritas do ledger e do checkpointer não carregam o fencing token | implementado (AC2 emendada) |

### 2.2 Os cinco que a remediação produziu

Registados como **tickets** e não como deferimentos, pela regra do §0 do epic: nenhum é «sabemos
e aceitamos» — são «isto ainda não é verdade».

| Ticket | P | O que a correcção deixou por cumprir | Estado |
|---|---|---|---|
| AOS-300 | P1 | A revogação ligada em AOS-288 não sobrevive a um restart sem substrato durável | implementado |
| AOS-301 | P2 | A `state.Machine` persiste com o mutex das leituras detido (residual de AOS-291) | implementado |
| AOS-302 | P3 | A poda do step-ledger não obrigaria a substrato durável | **fechado sem código** |
| AOS-303 | P3 | O payload do contrato de controlo ficou sem consumidor | **removido** (309 linhas) |
| AOS-304 | P2 | A retoma não entra na hash-chain, e a pausa entra | implementado |

---

## 3. Correcções ao meu próprio trabalho

Esta secção é a razão de ser do documento. Cada entrada é uma coisa que eu escrevi ou fiz e que
não sobreviveu à verificação.

### 3.1 Oito citações `file:line` do epic não sobreviveram

O epic exige que cada afirmação seja ancorada. Ao abrir cada âncora antes de implementar, **oito**
não resistiram — uma apontava para um `defer/recover` em vez do claim do lease
(`service.go:744` ⇒ `:829`/`:863`), outra para o meio de um literal
(`step_ledger.go:473` ⇒ `:483`). Estão corrigidas no §0.3 do epic.

**Duas suspeitas foram verificadas e o epic estava certo**, e ficam registadas: uma correcção que
não era precisa é informação tão útil como as que eram. A segunda foi minha: suspeitei de um
copiar-colar entre AOS-288 e AOS-299 que não existia — a suspeita nasceu de uma extracção mal
ancorada, não do documento.

### 3.2 O AOS-292 deixou um run pausado impossível de retomar

Escrevi que fechar o 292 antes do 293 deixava «os dois caminhos inconsistentes». Era pior: com a
projecção vazia, o `SteerChannel.Resume` recusava por não haver pausa pendente e a hospedagem
abortava fail-closed — **um run pausado não se conseguia retomar de todo** após um reinício. O
AOS-293 repô-lo, e a nota do 292 foi corrigida para dizer o que realmente acontecia.

### 3.3 Uma corrida de dados, apanhada pelo `-race`

No AOS-299, a primeira versão da linha que anexa o fencing token reatribuía `ctx` **depois** de a
goroutine do heartbeat ter fechado sobre a variável. Cinco testes vermelhos com «race detected».
Passou para antes de a goroutine arrancar, com a razão escrita no local.

### 3.4 Dois testes meus passavam pela razão errada

- **AOS-300**: o caso «four-eyes antes do substrato» apontava para um ficheiro de aprovadores
  **inexistente** — a config abortava na leitura, sem nunca chegar ao guarda que o caso dizia
  medir. Passava enquanto a asserção era só «não é este erro»; apareceu ao exigir o erro
  *esperado*.
- **AOS-299**: a primeira falsificação **não falhou**. Revertido o ledger para o store cru,
  nenhum dos meus testes do nó ficava vermelho — provavam que a autoridade era composta, não que
  o ledger escrevia por ela. É a mesma lacuna que o AOS-293 expôs: mecanismo provado, cablagem
  por provar.

### 3.5 Dois gates apanharam resíduos que eu não tinha corrido

- O `apex` mantém uma lista de testes **obrigatórios** e exigia um removido no AOS-298. Só
  avermelhou no ticket seguinte.
- O `deferrals` esteve **vermelho desde o AOS-293**: a palavra «CONDICIONAL» num comentário meu é
  lida como marcador de deferimento, e um marcador sem entrada no registo é um erro do gate.

**A lição, que vale mais do que os dois casos:** correr os gates do *eixo* que se tocou não
chega. Os gates que guardam **inventários** — marcadores de deferimento, listas de testes
obrigatórios, a RTM — são atravessados por qualquer alteração, e só se sabe correndo-os.

### 3.6 O AOS-302 fechou sem código: a premissa era minha e era falsa

Abri-o a dizer que a poda do step-ledger não obrigava a substrato durável em produção. O estado
que descreve **não é construível**: o `StepLedger` só é composto sob `DurableExecution`, e o
`Bootstrap` recusa execução durável sem substrato durável, sempre e nas duas fronteiras. Abri um
ticket a partir do comportamento do ledger sem verificar se a configuração que o torna perigoso
existia. É o §1 do epic aplicado a mim.

---

## 4. Três decisões em que o critério de aceitação cedeu à medição

### 4.1 O `admit()` não estava no caminho da retoma — AOS-289

A AC1 mandava corrigir o gate de admissão; a AC4 pedia prova de que a captura truncada deixava de
passar **na retoma**. Só que a retoma usa `Reconstruct`, que nunca passou pelo `admit()`. As duas
ACs, à letra, eram incompatíveis: corrigir só o `admit` fecharia o replay de DR e deixaria aberto
o caso de uso que o defeito descreve — a escalada existe para ser retomada.

A falsificação é a evidência: removida a guarda de um dos caminhos, falha **só** o teste desse
caminho.

### 4.2 Consumir a porta tornaria falso o critério que ela sustentava — AOS-296

`EPIC-02:688` afirma que o adaptador cumpre o contrato «sem alterações à API do RT». Ligar a
porta ao loop exigiria mudar a assinatura de `ActivityDispatcher` — interface **pública** do
kernel —, tornando a frase falsa. Removê-la deixa-a sem prova executável, e com ela a única prova
de *backend-swap* do repositório.

Nenhuma das duas vias preservava o critério. Três dos quatro `[x]` voltaram a `[ ]` com a razão
ao lado; o quarto (documental) mantém-se, porque a remoção não o falsifica.

### 4.3 A regra não é «trouxe token?» — é «há detentor a superar?» — AOS-299

A AC pede «escritas com token inferior ao corrente são rejeitadas». Uma recusa cega de toda a
escrita sem token partiria a superfície de embedding sem fechar defeito nenhum: sem lease, não há
escritor superado.

Medido: com a regra pelo **detentor**, zero testes do repositório precisaram de edição. Com a
recusa cega, treze ficavam vermelhos.

---

## 5. O que não foi feito, e porquê

| O quê | Porquê |
|---|---|
| **AOS-292, AC2** — teste ao vivo do ciclo de controlo | Escrito, tag `aoslive`, **não corrido**. Exige nó real com Model Gateway; esta máquina não tem cluster |
| **AOS-289, AC5** — «captura completa» dos não despachados | Mexe num tipo público do kernel, e só é aditiva ao WAL enquanto `captureSchemaVersion` ficar em `"1.0"`: subi-la tornaria **irrecuperável toda a captura já gravada** (igualdade estrita, sem janela de compatibilidade) |
| **AOS-299, AC2** — guard-test do AOS-222 a fazer `t.Skip` | **Emendada.** Não deve fazer skip: o `worker.Worker` continua por compor. O adaptador vive em `durable` precisamente para o guarda continuar a vigiar — escrevê-lo em `cmd/aos` desarmá-lo-ia em silêncio, porque a condição é **textual** |
| **AOS-304, AC4** — o smoke exercer a retoma | O run do smoke completa num turno e `/resume` sobre um run completo dá 404; faltam ainda `aos resume` e `aos-issuer resume-sign` |
| **AOS-288, AC3** — durabilidade da revogação | Cumprida como «registo inutilizável ⇒ aborta». A durabilidade fechou depois, em **AOS-300** |

**Consequência sobre dados históricos, declarada:** os runs escalados **já gravados** têm captura
truncada e não são reescritos na retoma (dedup de `cap-<step_id>`). Passam a ser inadmissíveis ao
replay — é o fail-closed pretendido.

---

## 6. Evidência

Corridos no estado final da branch, não citados de memória.

| Gate | Resultado |
|---|---|
| `build` | verde — 50 módulos |
| `lint` | verde — sem descobertas novas |
| `layer-lint` | verde — sem violações fora da baseline |
| `ref-lint` | verde — 304 tickets, 584 declarações de título verificadas |
| `rtm` | sincronizada |
| `replay` | verde — fidelidade 100%, 0 efeitos duplicados |
| `apex` | verde — cobertura 82,6% |
| `memory` | verde |
| `security` | verde |
| `deferrals` | verde |
| `driver.sh smoke` | verde — 9/9 passos |
| `go test -race` | verde nos módulos tocados |

**NÃO VERIFICADO:** o ciclo `pause → steer → resume` contra um nó real (AOS-292 AC2), e o par de
selos `control:pause`/`control:resume` na hash-chain ao vivo (AOS-304, asserção incluída no mesmo
teste `aoslive`).

---

## 7. O que fica para quem continuar

1. **Correr o teste `aoslive` no cluster.** É a única lacuna de evidência do epic. O comando e as
   variáveis estão no cabeçalho do ficheiro.
2. **Reavaliar `EPIC-02:428`.** Ficou por marcar, e o âmbito decidido é o de AOS-299: as escritas
   do ledger e do checkpointer carregam token; as do worker não existem no nó.
3. **Os `[x]` de AOS-022 no EPIC-02** voltaram a `[ ]` com razão escrita — três deles. A decisão
   de os fechar de outra forma é de produto, não de código.
4. **A janela TOCTOU de `durable/fencing.go`** fica re-declarada como limite aceite, com a
   precisão de que o teste fixa o caso-fronteira token-**igual**, não uma escrita obsoleta.
