# Portões por eixo, custo e velocidade

Lê este ficheiro quando fores decidir que portões correr, por que ordem, e o que mandar para
background.

- [O agregador](#o-agregador)
- [Roteamento: eixo → portões](#roteamento-eixo--portões)
- [Custo e ordem](#custo-e-ordem)
- [Seis regras de velocidade](#seis-regras-de-velocidade)
- [O que não se acelera](#o-que-não-se-acelera)

---

## O agregador

`scripts/ci/run.sh` é a fonte de verdade da lista canónica e **aceita um subconjunto**:

```bash
bash scripts/ci/run.sh                      # tudo (lento; raramente é o que queres)
bash scripts/ci/run.sh build lint layer-lint
```

A lista completa vive no `ALL_GATES` do próprio `run.sh` — consulta-a lá, não de memória. São os
mesmos nomes que os jobs do `.github/workflows/ci.yml` e os *required checks* do
`CONTRIBUTING.md`; o `selftest` corre à parte (`bash scripts/ci/selftest.sh`) e a entrega
(`package.sh`, `sbom.sh`) tem job próprio.

**Três códigos de saída, e o terceiro é o que engana:**

```text
0   verde          nenhum gate falhou e nenhuma etapa foi saltada
1   vermelho       pelo menos um gate falhou — ganha a qualquer salto
3   VERDE PARCIAL  nada falhou, mas alguma etapa NÃO correu (AOS_SKIPPED_STEP)
```

Um `3` não é um `0`. O veredicto redeclara cada etapa saltada com o gate, o motivo e a garantia
que ficou por verificar. **Registar não é impedir** — uma etapa saltada é uma garantia que não
tens, e dizê-la em voz alta é o mínimo.

**Os limiares só apertam.** `COVERAGE_MIN`, `KERNEL_COVERAGE_MIN`, `APEX_COVERAGE_MIN`,
`MEMORY_COVERAGE_MIN`, `ROUTING_COVERAGE_MIN`, `REGISTRY_COVERAGE_MIN` e `EVAL_PASS_RATE_MIN`
têm piso: um valor abaixo dele avermelha por **violação de piso**, que é diagnóstico diferente
de «limiar não atingido» — ali é a configuração que está errada e nenhum código chegou a ser
avaliado. O mesmo vale para desviar a raiz ou a baseline de um gate: torna-o verde sem varrer
nada, e o `gate_path` recusa-o em CI.

---

## Roteamento: eixo → portões

A pergunta não é «que gates existem», é «o que é que eu toquei». A análise de impacto já
respondeu — esta tabela traduz a resposta.

| Tocaste em… | Portões |
|---|---|
| **Kernel / RM / política / taint** | `lint` `layer-lint` `test` `policy-test` `policy-taint` `security` `apex` |
| **`cmd/aos` — wiring, banner, env surface** | `build` `lint` `test` `apex` + `driver.sh smoke` |
| **`packages/integration/` — composition-root** | `build` `integration` `apex` `test` |
| **Eventos, streams, NATS** | `event-catalog` `stream-names` `nats` `replay` |
| **`specs/` · `docs/adr/` · README · Estado de ticket** | `ref-lint` `rtm` `deferrals` `estado-citado` |
| **`go.mod`, versões, toolchain** | `build` `sast` `sca` `dormencia` |
| **ORQ / planos / decomposição** | `build` `lint` `test` `integration` `replay` |
| **Sandbox, segredos, egress, isolamento** | `security` `supplychain` (+ `isolation-live`, opcional) |
| **Memória (MEM)** | `memory` |
| **Model Gateway, rotas, failover** | `routing` |
| **Durabilidade, DR, perda de nó** | `replay` `dr-e2e` `nats` |
| **Carga, admissão, backpressure** | `scale` |
| **Superfície de UX/DX dos gates** | `ux-dx` |
| **Qualquer coisa, sempre** | `secrets` |

Dois gates **anti-recorrência** merecem nota, porque fecham defeitos que já escaparam a tudo o
resto: `stream-names` (todo o `stream_id` tem de ser representável num subject NATS — um ponto
no nome passou dez gates, revisão adversarial e smoke) e `policy-taint` (cada `permit` da
política assinada exige `context.taint != untrusted`).

### Os dois que precisam de Docker

O `nats` levanta um cluster JetStream de quatro nós. **Sem Docker utilizável ele salta e
declara-o; em CI é vermelho**, porque é *required check* e o agregador lê `success`, não o
`AOS_SKIPPED_STEP`. «Definido» é «não vazio» — `CI=false` conta como CI. Para saltar localmente
apaga-se a variável, não se nega:

```bash
env -u CI -u GITHUB_ACTIONS bash scripts/ci/run.sh nats
NATS_GO_TEST_TIMEOUT=60 bash scripts/ci/nats.sh      # posto lento
```

O `isolation-live` precisa de Linux com Docker privilegiado e **não entra** na lista canónica —
invoca-se à mão. A assimetria com o `dormencia` é deliberada: esse corre em qualquer runner,
só compila as suites dormentes e conta os testes saltados, e é o que impede a dormência de
virar apodrecimento.

---

## Custo e ordem

```text
SEGUNDOS      secrets · ref-lint · deferrals · estado-citado · stream-names · event-catalog
DEZENAS DE S  build · lint · layer-lint · rtm · policy-taint · sast
MINUTOS       integration · replay · memory · supplychain · routing · apex · security
              evalgate · scale · ux-dx · sca · policy-test · dormencia
CAROS         test (~15 min em CI) · dr-e2e · nats (~25 min local; o aos-orq é ~10x
              mais lento aqui do que no runner)
```

A ordem canónica do `run.sh` já é fail-fast: `build` antes de `lint`, `lint` antes de `test`,
e os caros no fim. Respeita-a quando correres subconjuntos — um erro de compilação não precisa
de vinte minutos de `nats` para se revelar.

---

## Seis regras de velocidade

**1. Fail-fast, e por eixo.** `build` → `lint` → teste do módulo → portões do eixo. A suite
inteira é para antes de abrir PR, não para cada iteração.

**2. O lint corre-se depois da última edição.** Correr antes e editar a seguir é como não o ter
corrido. Já partiu o CI três vezes na mesma série de PRs.

**3. Os caros vão para background.** Lança `test` e `nats` em background e continua a trabalhar
no que não depende deles. **Não faças polling** — a notificação chega quando terminarem.

**4. Fan-out onde os módulos o permitem.** Duas tasks em módulos sem aresta `replace` entre si
são quase sempre independentes:

```bash
grep -rn "replace github.com/aos-ref" packages/<modulo>/go.mod
```

Lança os subagentes **na mesma mensagem**, senão correm em série e pagas a latência sem ganhar
nada. Cada escritor no seu worktree ([`isolamento.md`](isolamento.md)).

**5. Não refaças a discovery.** `KNOWN_FACTS` no contrato do agente é o campo que mais poupa.
Sem ele cada subagente redescobre o mesmo código e chega a conclusões ligeiramente diferentes —
o que produz conflitos que não existiam, e cada conflito custa uma ronda de arbitragem.

**6. Decide e avança.** Quando tens informação para agir, age. Não re-derives factos já
estabelecidos, não reabras decisões já tomadas, não enumeres opções que não vais seguir.

### Duas armadilhas de medição de tempo

- **Suites concorrentes mentem.** Duas suites do `aos-orq` ao mesmo tempo dão 25 minutos e
  timeouts falsos. Mede uma de cada vez antes de chamar lento a alguma coisa.
- **Validação dirigida não cobre o pacote.** `-run TestX|TestY` deixa passar testes do mesmo
  pacote que só correm noutro gate. Verde dirigido não é verde.

---

## O que não se acelera

Três coisas. Cortá-las não é velocidade, é dívida com juros:

- **O portão humano.** O `environment: production` tem revisor obrigatório por desenho.
- **A revisão adversarial.** É a primeira a ser sacrificada quando há pressa, e a que mais
  custa quando falta.
- **O smoke.** Uma suite verde sobre um nó que não arranca é o caso exemplar de teste a passar
  pela razão errada. E **gates e smoke só correm sobre aquilo a que o ficheiro os expõe** — um
  defeito de forma que não esteja num ficheiro exercitado passa por todos.

A evidência pública é consistente: com agentes, o *throughput* sobe e a estabilidade de entrega
desce — foi por isso que o DORA de 2025 acrescentou a taxa de retrabalho como quinta métrica. A
fábrica existe para que as duas subam juntas, e é isso que
[`metricas-e-realimentacao.md`](metricas-e-realimentacao.md) mede.
