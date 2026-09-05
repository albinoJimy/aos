# Contribuir para o AOS — gates de CI e execução local

Todos os *merges* passam pelos **gates de qualidade** de `specs/01 §4`. O
*pipeline* é **fail-closed**: qualquer gate vermelho bloqueia. A **fonte de
verdade** dos gates é **local e testável** — os scripts em `scripts/ci/` — e a CI
(`.github/workflows/ci.yml`) apenas os **invoca**; nada é duplicado no YAML.

## Reproduzir os gates localmente — UM comando

```bash
make ci
# equivalente (sem make instalado):
bash scripts/ci/run.sh
```

Corre, por ordem canónica, `secrets → build → lint → test → replay → sast → sca → policy-test`
e termina com `exit != 0` se qualquer gate falhar. Provar que as falhas **são
bloqueadas**:

```bash
make ci-selftest          # ou: bash scripts/ci/selftest.sh
make ci-all               # gates + self-tests
```

Correr um gate isolado: `make ci-secrets | ci-build | ci-lint | ci-test | ci-replay | ci-sast | ci-sca | ci-policy`
(ou `bash scripts/ci/<gate>.sh`).

## Pré-requisitos

| Ferramenta | Versão | Notas |
|---|---|---|
| **Go** | 1.24 | módulos em `packages/**` (descobertos por `find packages -name go.mod`) |
| **gcc** | qualquer | exigido pelo `go test -race` (CGO). Windows: mingw do scoop; Linux: gcc do sistema |
| **bash** | 4+ | Git Bash em Windows |
| staticcheck / gosec / govulncheck | pinadas | **auto-instaladas** por `go install` (idempotente) em `$(go env GOPATH)/bin`; nunca committadas |

Pins das ferramentas em `scripts/ci/lib.sh` (`*_PIN`). Em Windows, o runner
acrescenta ao `PATH` o mingw/shims do scoop e o `bin` do GOPATH, e força
`CGO_ENABLED=1` — não é preciso configuração manual.

## Os gates

| # | Gate | Script | O que valida | Bloqueia |
|---|---|---|---|---|
| 1 | build | `build.sh` | `go build ./...` em cada módulo | merge |
| 2 | lint | `lint.sh` | `gofmt -l`, `go vet`, `staticcheck` **+ arch-lint AOS-003** (proibição de despacho directo) | merge |
| 2b | ref-lint | `ref-lint.sh` | referências cruzadas do corpus (AOS-186): todo o `AOS-NNN` citado existe no backlog; todo o `ADR-NNN` citado existe no catálogo; cada ADR canónico tem ≥ 1 ticket implementador. Só precisa de **Python 3** | merge |
| 2c | rtm | `rtm.sh` | sincronia da matriz de rastreabilidade `tecnica/16` com o corpus (AOS-186), via `rtm-regenerate.py --check`. Só precisa de **Python 3** | merge |
| 2d | layer-lint | `layer-lint.sh` | fronteiras canónicas de camada sobre `packages/` (AOS-178); excepções intencionais na baseline, decididas no **ADR-019**. Precisa de **Go** (`go list`) | merge |
| 3 | test | `test.sh` | `go test ./... -race -covermode=atomic`; **cobertura do kernel ≥ 80%** (limiar com piso — ver [Limiares e pisos](#limiares-dos-gates-e-os-seus-pisos)) | merge |
| 8 | replay | `replay.sh` | **harness de replay/idempotência (AOS-024)**: golden trajectories reproduzem-se *resume-from-step* (replay-fidelity 100%) + **zero efeitos duplicados** (step-ledger AOS-014); emite o relatório de fidelidade | merge |
| 4 | sast | `sast.sh` | `gosec` — falha em findings **HIGH/CRITICAL** | merge |
| 5 | sca | `sca.sh` | `govulncheck` — falha em vuln que **afecta** o código | merge |
| 6 | policy-test | `policy-test.sh` | bundle do PDP (AOS-004): golden allow/deny (default-deny) **+ verificação de assinatura**; bundle não-assinado/adulterado é rejeitado | merge |

### Baselines (dívida pré-existente triada)

`lint`, `sast` e `sca` comparam as descobertas com uma **baseline**
(`scripts/ci/baseline/*.txt`): são **fail-closed para código NOVO** e apenas
toleram dívida já existente de outros tickets (AOS-010 é *chore* de CI e não
altera código dos módulos). Cada entrada da baseline está documentada, com dono e
remediação, em [`scripts/ci/baseline/README.md`](scripts/ci/baseline/README.md).
Qualquer descoberta fora da baseline faz o gate ficar vermelho.

## Limiares dos gates e os seus PISOS

Os limiares são **sobreponíveis por ambiente**, mas **só para APERTAR**. Cada um tem um
**piso** — o compromisso já documentado em `specs/01 §4` / ADR-012 — e descer abaixo dele
não é afinar um gate: é **renegociar uma spec em silêncio a partir do ambiente**. Antes de
AOS-199 (achado ORF-06) qualquer pipeline podia exportar `EVAL_PASS_RATE_MIN=0` e obter
verde sem exercitar nada; um gate cujo limiar se pode zerar em silêncio não é um gate.

| Variável | Default | **Piso** | Domínio | Onde | Porquê este piso |
|---|---|---|---|---|---|
| `KERNEL_COVERAGE_MIN` | 80 | **80** | 0–100 (%) | `lib.sh` | `specs/01 §4` compromete «cobertura do kernel ≥ 80%». O knob existe para **subir** a barra (ratchet), não para a baixar. |
| `COVERAGE_MIN` | herda de `KERNEL_COVERAGE_MIN` | **80** | 0–100 (%) | `lib.sh` | Gate generalizado (AOS-109). Piso **próprio e independente**: herdar só o *default* impediria que uma descida do knob histórico arrastasse o gate generalizado com ela. |
| `APEX_COVERAGE_MIN` | 80 | **80** | 0–100 (%) | `apex.sh` | O ápice é o composition-root onde o enforcement é **montado**; a barra não pode ser inferior à dos pilares que compõe. |
| `MEMORY_COVERAGE_MIN` | 80 | **80** | 0–100 (%) | `memory.sh` | «Igual ao limiar do kernel» (§4). |
| `ROUTING_COVERAGE_MIN` | 80 | **80** | 0–100 (%) | `routing.sh` | «Igual ao limiar do kernel» (§4). |
| `REGISTRY_COVERAGE_MIN` | 80 | **80** | 0–100 (%) | `supplychain.sh` | «Igual ao limiar do kernel» (§4). |
| `EVAL_PASS_RATE_MIN` | 0.90 | **0.90** | 0–1 (**fracção**) | `evalgate.sh` | ADR-012 / AOS-114 fixam o alvo de eval-pass-rate em ≥ 90%. É o gate de *admission control*: abaixo do alvo, promover é admitir regressão comportamental. |

**Piso = default é deliberado.** O default **é** o compromisso documentado; um piso mais
baixo seria uma segunda barra, não documentada, a autorizar em silêncio exactamente o que
ORF-06 aponta. O que estes knobs continuam a permitir é o que faz falta: **apertar**
(`COVERAGE_MIN=90 make ci`).

O **domínio** também é validado: `EVAL_PASS_RATE_MIN=90` (percentagem onde se espera
fracção) é recusado como confusão de unidade em vez de tornar o gate impossível de passar.

### Dois diagnósticos, duas mensagens

Distinguir isto importa para quem lê a CI — as acções correctivas são opostas:

| Mensagem | Significa | Quem corrige o quê |
|---|---|---|
| `FAIL VIOLAÇÃO DE PISO: <VAR>=<v> < piso <p>` | A **configuração** é inválida. **Nenhum código foi avaliado.** | Corrigir o **pipeline/ambiente**. Mexer no código não resolve. |
| `FAIL LIMIAR NÃO ATINGIDO: <métrica> < <limiar>` | A configuração é válida e o **código** ficou abaixo. | Corrigir o **código** (ou subir a cobertura). |

Uma violação de piso em `KERNEL_COVERAGE_MIN`/`COVERAGE_MIN` aborta **qualquer** gate no
momento em que faz `source lib.sh` (incluindo o agregador `run.sh`, que sai `!= 0` antes de
correr o primeiro gate) — a configuração é global, logo a recusa é global.

### Escape hatch para bisect local

```bash
AOS_GATE_FLOOR_OVERRIDE="bisect da regressao de cobertura AOS-146" APEX_COVERAGE_MIN=40 make ci-apex
```

- exige uma **justificação legível** (≥ 8 caracteres — um `1` não serve);
- é **recusado em CI** (`CI` / `GITHUB_ACTIONS` definidos): a CI nunca desce um piso;
- exige **sinal positivo de sessão local**: `stdout` num terminal interactivo, ou
  `AOS_LOCAL_SESSION=1` declarado. A *ausência* de `CI`/`GITHUB_ACTIONS` **não** é prova de
  sessão local — um Jenkins, um runner *self-hosted* com ambiente limpo ou um
  `env -u CI -u GITHUB_ACTIONS` não os definem. **A recusa é o comportamento por omissão**;
  só um sinal positivo a levanta;
- imprime um banner `AOS_FLOOR_BREACH` que declara, **no próprio output**, que a execução
  **não é prova de gates verdes**. Um verde obtido assim é *grep*-ável e não se pode citar
  como evidência. Uma linha do banner sai também em **stdout** — o canal onde viaja o
  veredicto agregado —, para que uma captura que separe os canais
  (`bash run.sh > evidencia.log`) não produza um log verde sem a ressalva.

Cada limiar efectivamente em uso é registado no output como
`AOS_GATE_THRESHOLD <VAR>=<valor> piso=<piso> origem=default|env(override)`. Um *override*
imprime **sempre**; os dois limiares **globais** (`KERNEL_COVERAGE_MIN`, `COVERAGE_MIN`) são
resolvidos em silêncio no `source lib.sh` — seriam repetidos pelos ~22 gates que nunca lêem
o valor — e registados **uma vez, no ponto de consumo**, por `gate_threshold_report` no
`test.sh`. O marcador é um **inventário**: numa corrida normal do gate que consome o limiar
ele aparece, mesmo em `default`.

### Anti-recorrência: o mecanismo testa-se a si próprio

O piso é uma propriedade **não-regressiva**, e um mecanismo cuja desactivação não é
detectada volta ao ponto de partida em silêncio: repor `APEX_COVERAGE_MIN="${APEX_COVERAGE_MIN:-80}"`
ou apagar um `|| exit 1` reabriria ORF-06 **passando em todos os gates**. `gate_floor_selftest`
(`lib.sh`, invocado pelo `evalgate.sh` — o gate que a CI corre e que o CA nomeia) assere, em
milissegundos e sem rede:

1. injecta `0` em **cada** piso e exige recusa **com a mensagem `VIOLAÇÃO DE PISO`** — assere
   a mensagem, não só o exit: um `exit 1` por outra razão qualquer não o satisfaria;
2. **ratchet dos próprios pisos**: baixar um `FLOOR_*` abaixo do compromisso documentado
   avermelha (apertar não exige tocar no teste);
3. estático — nenhum limiar volta a resolver-se por `VAR="${VAR:-…}"` fora de `gate_threshold`;
4. estático — toda a invocação de `gate_threshold` é fail-closed (`|| exit 1`);
5. `gate_path` continua a **recusar** um desvio de raiz/baseline em CI (o mecanismo está
   pronto antes dos consumidores — sem este guard apodrecia por usar).

Verificado por **mutação**: repor o padrão `${VAR:-80}`, apagar um `|| exit 1`, baixar um
`FLOOR_*` ou neutralizar a recusa do `gate_path` faz cada um dos cinco detectores disparar
com a sua própria mensagem.

### Knobs de CAMINHO (raízes e baselines) — a mesma classe, por outra via

Um gate cuja **raiz de varrimento** ou cuja **baseline** se desvia por ambiente sai *verde
vacuoso* sem exercitar nada — uma árvore vazia não tem violações; uma baseline que contém
tudo não tem descobertas novas. É a consequência que ORF-06 enuncia, por *knob* de caminho
em vez de *knob* numérico. `gate_path <VAR> <default>` (`lib.sh`) é o simétrico de
`gate_threshold`: **recusa** o desvio quando `CI`/`GITHUB_ACTIONS` estão definidos e regista
sempre `AOS_GATE_ROOT <VAR>=<valor> origem=default|env(override)`.

> **PENDÊNCIA (fora do âmbito do AOS-199).** O mecanismo existe; os consumidores
> (`layer-lint.sh`, `ref-lint.py`, `deferrals.py`, `event-catalog.py`, `integration.py`)
> pertencem a outras pistas de escrita e **ainda não o adoptaram**. Enquanto não adoptarem,
> `LAYER_LINT_ROOT`, `AOS_REFLINT_ROOT`, `AOS_DEFERRALS_ROOT`, `AOS_EVENT_BASELINE`,
> `AOS_CONTRACT_BASELINE` e `AOS_CONTRACTS_DOC` continuam desviáveis. Reprodução:
> `mkdir -p /tmp/fakeroot/packages && LAYER_LINT_ROOT=/tmp/fakeroot bash scripts/ci/layer-lint.sh`
> → `exit 0` sem varrer uma linha do repo.

### Etapas saltadas — `SKIP_DOCKER` e afins

Uma etapa que não corre e não aparece no veredicto é um **falso-verde**. `package.sh`:

- só aceita `SKIP_DOCKER=0|1`; qualquer outro valor (`true`, `yes`) sai `2` como
  **configuração inválida** em vez de ser tratado como `0` em silêncio;
- regista cada etapa saltada com **motivo** e **garantia que ficou por verificar**, e
  **redeclara-as no veredicto final** (`AOS_SKIPPED_STEP …`), não apenas num `WARN` a meio
  do output;
- quando algo foi saltado, o veredicto é `VERDE PARCIAL` e diz explicitamente que **não é
  prova do ponto 2 do ADR-017** (imagem endurecida). Quando nada foi saltado imprime
  `AOS_SKIPPED_STEPS none` — a ausência de skips é ela própria uma afirmação falsificável.

O mesmo vale para o *docker indisponível no ambiente*: skip declarado, nunca silencioso.
`sbom.sh` — que a CI invoca **também** como step autónomo — declara pela mesma via a queda
do *subject* para *rebuild do host* (ADR-017 ponto 3), mas **mantém o exit 0**: o SBOM foi
genuinamente emitido e o JSON já regista `subject.source`/`reproducible=false`; avermelhar
um ambiente sem docker seria um falso vermelho.

#### Registar não é impedir — códigos de saída do `package.sh`

O único sinal que a automação a jusante consome é o **código de saída**, e o passo seguinte
publica `deploy/node/build`. Um verde parcial indistinguível de um verde publicaria o
artefacto com o ponto 2 do ADR-017 por verificar:

| Saída | Significa |
|---|---|
| `0` | Verde: nenhuma etapa falhou e **nenhuma foi saltada**. |
| `1` | Vermelho: uma etapa falhou. Entrega bloqueada. |
| `2` | **Configuração inválida** do interruptor (`SKIP_DOCKER=true`), não etapa vermelha. |
| `3` | **VERDE PARCIAL**: nada falhou, mas algo **não correu**. Nem verde, nem vermelho. |

O `3` não precisa de alteração na CI para morder: um *step* que sai `!= 0` interrompe o job,
pelo que o `upload-artifact` seguinte não chega a correr. **PENDÊNCIA** (ficheiro fora desta
pista): tornar essa dependência explícita no job `delivery` de `.github/workflows/ci.yml` —
hoje o `upload-artifact` é incondicional e depende do encadeamento por omissão.

Complementarmente, o registo fica **máquina-legível** ao lado do artefacto em
`deploy/node/build/SKIPPED.txt` (presente **sse** houve skips; removido quando não houve,
para que um marcador obsoleto não seja um falso-positivo). Quem publica condiciona por
`[ -e … ]` em vez de ler o log.

Aceitar o verde parcial é possível — `AOS_ALLOW_PARTIAL_DELIVERY=1` força a saída `0` — com
o **mesmo modelo do escape hatch dos pisos**: imprime `AOS_PARTIAL_ACCEPTED` no output e é
**recusado em CI**. A CI não publica entrega por verificar.

### Auto-testes dos gates (`selftest.sh`)

Provam, de forma isolada e reversível (sem rasto no repo), que os gates bloqueiam:

- **A** — módulo mau injectado (gofmt sujo + teste que falha) ⇒ `lint`/`test` vermelhos;
- **B** — assinatura do bundle do PDP adulterada (backup+restore) ⇒ `policy-test` vermelho;
- **C** — vuln afetante fora da baseline ⇒ comparador do `sca` vermelho (determinista, offline).
- **D** — golden trajectory **adulterada** ⇒ o harness de replay (gate 8) vermelho (determinista, offline, sem rasto).
- **L** — inversão de camada numa árvore sintética ⇒ `layer-lint` vermelho (AOS-178).
- **M** — *(coerência, não injecção)* as três listas de required checks — `needs:` do agregador,
  `REQUIRED-CHECKS:` de `ci.yml` e `REQUIRED-CHECKS:` deste ficheiro — têm de coincidir entrada a
  entrada e na mesma ordem, e o agregador `gates` tem de manter `if: always()` + avaliação de
  `needs.*.result`. Divergirem em silêncio já aconteceu (auditoria v4, AOS-190) e nada o detectava.

## Required checks (bloqueiam o merge em `main`)

Configurar em *branch protection* de `main` os checks (lista completa, na mesma ordem
do `needs:` do agregador — o self-test §M compara-a com `.github/workflows/ci.yml` e
fica vermelho se divergir):

REQUIRED-CHECKS: secrets · build · lint · ref-lint · deferrals · estado-citado · rtm · layer-lint · test · integration · event-catalog · replay · memory · supplychain · routing · apex · security · evalgate · scale · dr-e2e · ux-dx · sast · sca · policy-test · selftest

…ou, em alternativa, o agregador único **`gates`**. O **scan de segredos** (regra
transversal de `specs/01 §4`) tem o seu próprio job e é pré-condição de merge.

Os três gates **anti-recorrência** (`ref-lint`, `rtm`, `layer-lint`, AOS-190) constam
do `needs:` do agregador `gates` — é isso, e só isso, que os torna bloqueantes. Um
gate que não consiga sequer arrancar (ex.: interpretador Python ausente) sai `!= 0` e
avermelha o job: **nunca** há caminho de "skip" verde.

O agregador `gates` só é substituto legítimo da lista completa porque é **fail-closed
por construção**: tem `if: always()` e avalia `needs.*.result`, terminando `!= 0` se
qualquer gate não ficar `success`. Sem isso, um `needs:` vermelho fá-lo-ia ser
**saltado** pelo GitHub Actions, e a protecção de branch trata `skipped` como
passagem — o agregador nunca conseguiria ficar vermelho. Se alguém remover o
`if: always()` ou o passo de avaliação, esta alternativa deixa de ser válida e a
lista completa de 21 checks passa a ser obrigatória.

## Tempo aceitável

**Local (sequencial):** ~10 min numa máquina de programador — dominado por
`sast`/gosec e `sca`/govulncheck (que descarrega a base de vulnerabilidades).
Repartição observada: `secrets` ~1 s · `build` ~10 s · `lint` ~30 s ·
`test -race` ~107 s · `replay` ~15 s (harness AOS-024) · `sast` ~278 s (gosec,
dominante) · `sca` ~151 s (govulncheck, rede) · `policy-test` ~10 s · `selftest` ~78 s.

**Na CI:** os jobs correm em **paralelo** com cache de módulos (`actions/setup-go`),
pelo que o *wall-clock* é ≈ o gate mais lento (~5 min, `sast`), não a soma.
Para iteração rápida local, correr gates isolados (`make ci-lint`, `make ci-test`).
