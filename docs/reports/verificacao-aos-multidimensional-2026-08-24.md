# Relatório de Verificação Multidimensional do AOS

**Data:** 2026-08-24  
**Escopo:** Verificação do funcionamento do AOS com exploração de múltiplas dimensões e perspetivas, submetida a validação adversarial com subagentes especializados.  
**Autor:** Kimi Code CLI  
**Referência:** execução agregada `scripts/ci/run.sh secrets build lint ref-lint deferrals rtm layer-lint integration event-catalog policy-test sast selftest`.

---

## 1. Resumo executivo

| Dimensão | Gate(s) / Verificação | Resultado |
|---|---|---|
| Segredos em repositório | `secrets` | ✅ verde |
| Compilação | `build` (46 módulos Go) | ✅ verde |
| Formatação / vet / staticcheck / arch-lint | `lint` | ✅ verde |
| Rastreabilidade título↔ticket | `ref-lint`, `rtm` | ✅ verde |
| Deferimentos com dono | `deferrals` | ✅ verde — 4 itens `POR ATRIBUIR` |
| Fronteiras de camadas | `layer-lint` | ✅ verde |
| Contratos de interface C1–C5 | `integration` | ✅ verde — 10 divergências reconhecidas na baseline |
| Catálogo de eventos | `event-catalog` | ✅ verde — 4 dívidas reconhecidas |
| Política Cedar allow/deny + assinatura | `policy-test` | ✅ verde |
| SAST gosec HIGH/CRITICAL | `sast` | ✅ verde |
| Auto-testes dos gates | `selftest` (A–Q) | ✅ verde |
| **SCA** (`govulncheck`) | **Não incluído na corrida final** | 🔴 **vermelho por si só** |
| **Testes unitários com `-race`** | **Não corridos** | ⚠️ **bloqueado por falta de gcc/mingw** |
| **Testes sem `-race`** | Corridos via subagente | ⚠️ **43/46 passaram** |

O conjunto de gates executado terminou com **todos verdes** (520 s). No entanto, a validação adversarial não permite eliminar algumas hipóteses: SCA (`govulncheck`) falha autonomamente, testes com `-race` não foram possíveis no ambiente Windows atual, e um teste unitário em `packages/substrate/bus` falha deterministicamente.

---

## 2. Comando e resultado da execução agregada

```bash
PATH="$PWD/.tools:$PATH" bash scripts/ci/run.sh \
  secrets build lint ref-lint deferrals rtm layer-lint \
  integration event-catalog policy-test sast selftest
```

Resumo emitido pelo agregador:

```text
PASS  secrets        2s
PASS  build          30s
PASS  lint           127s
PASS  ref-lint       0s
PASS  deferrals      5s
PASS  rtm            0s
PASS  layer-lint     29s
PASS  integration    1s
PASS  event-catalog  4s
PASS  policy-test    10s
PASS  sast           126s
PASS  selftest       186s
-----------------------------------------------
RESULTADO: TODOS OS GATES VERDES
```

---

## 3. Exploração por dimensão e hipóteses testadas

### 3.1 Build e compilação

- **Hipótese:** todos os módulos compilam isoladamente.
- **Teste:** `go build ./...` em cada um dos 46 módulos.
- **Veredicto:** aceite — nenhum erro de compilação.

### 3.2 Lint e arquitetura

- **Hipótese A:** o código está formatado e livre de descobertas novas do `staticcheck`.
- **Hipótese B:** a regra AOS-003 (proibição de despacho direto fora do Reference Monitor) é respeitada.
- **Teste:** `gofmt`, `go vet`, `staticcheck` contra baseline, e 5 testes de arch-lint em `reference-monitor`.
- **Veredicto:** ambas aceites.

### 3.3 Fronteiras de camadas

- **Hipótese:** não existem violações de dependência `control-plane → kernel → platform/substrate` fora da baseline.
- **Teste:** `layer-lint` AOS-178.
- **Veredicto:** aceite — nenhuma violação nova.

### 3.4 Contratos de interface (C1–C5)

- **Hipótese:** os códigos de erro documentados em `tecnica/12_Contratos_de_Interface.md` estão presentes no código.
- **Teste adversarial:** o gate `integration` extraiu 16 códigos documentados e comparou-os com as implementações.
- **Veredicto:** parcial — apenas 6/16 encontram-se exatos; 10 divergências estão na baseline como dívida reconhecida (`owner=AOS-196`). Não são regressões, mas consolidam a hipótese de que os contratos C3/C4/C5 ainda não estão totalmente alinhados.

### 3.5 Catálogo de eventos

- **Hipótese:** os eventos são emitidos por constantes, sem literais ou concatenações no caminho quente.
- **Teste adversarial:** o gate `event-catalog` rastreou 113 tipos constantes.
- **Veredicto:** parcial — 113 tipos estão corretos, mas 4 concatenações/literais estão na baseline (`owner=AOS-201`), incluindo um achado novo em `packages/control-plane/scheduler/breaker.go`.

### 3.6 Política e assinatura

- **Hipótese A:** as decisões default-deny estão corretas.
- **Hipótese B:** bundles adulterados ou não assinados são rejeitados.
- **Teste adversarial:** golden allow/deny, cobertura por regra, e verificação de assinatura.
- **Veredicto:** ambas aceites — 8 + 3 testes obrigatórios passaram.

### 3.7 Segurança SAST

- **Hipótese:** não existem findings HIGH/CRITICAL novos do `gosec`.
- **Teste:** `gosec` em todos os pacotes contra baseline.
- **Veredicto:** aceite.

### 3.8 Auto-testes dos gates (validação adversarial da infraestrutura de CI)

- **Hipótese:** os gates são realmente *fail-closed* e detetam adulterações.
- **Testes A–Q:**
  - módulo `gofmt`-sujo → bloqueado ✅
  - teste que falha → bloqueado ✅
  - bundle PDP adulterado → bloqueado ✅
  - vulnerabilidade afetante fora da baseline → bloqueado ✅
  - trajetória replay adulterada → bloqueado ✅
  - violação de integridade na gate memory → bloqueado ✅
  - vetor supply-chain desbloqueado → bloqueado ✅
  - cenário routing desbloqueado → bloqueado ✅
  - controlo security desligado → bloqueado ✅
  - cobertura abaixo do limiar → bloqueado ✅
  - invariante vacuosa no ápice → bloqueado ✅
  - egress desbloqueado no ápice → bloqueado ✅
  - inversão de camada → bloqueado ✅
  - listas de required checks coerentes ✅
  - divergências de contrato / baseline → bloqueado ✅
  - literais de eventos → bloqueado ✅
  - títulos fora do STR-01 / leave-one-out → bloqueado ✅
  - smoke `/healthz` vs `/readyz` → bloqueado ✅
- **Veredicto:** aceite — a infraestrutura de CI demonstra comportamento fail-closed robusto.

---

## 4. Achados que ainda não são verdes

| Problema | Gate/Área | Estado | Risco |
|---|---|---|---|
| **SCA `govulncheck`** | `scripts/ci/sca.sh` | 🔴 vermelho | Vulnerabilidades afetantes novas e baseline obsoleta. Bloqueia merge até atualização. |
| **Testes com `-race`** | todos os módulos | ⚠️ não corridos | Impossível verificar condições de corrida sem `gcc`/mingw. |
| **`TestBackpressureDropOldest`** | `packages/substrate/bus` | 🔴 falha | Falha determinística fora de `-race`; requer correção. |
| **Antivírus / timeout** | `packages/kernel/agent-runtime`, `packages/cmd/aos` | ⚠️ instável no Windows | Binários gerados nos testes são interceptados ou excedem timeout. |
| **Dívidas de contrato C3/C4/C5** | `integration` baseline | ⚠️ reconhecida | 10 códigos de porta não implementados exatamente conforme contrato. |
| **Dívidas do catálogo de eventos** | `event-catalog` baseline | ⚠️ reconhecida | 4 literais/concatenações. |
| **Deferimentos `POR ATRIBUIR`** | `deferrals` | ⚠️ 4 itens | Precisam de tickets novos no backlog. |

---

## 5. Notas sobre o ambiente Windows

Para desbloquear os gates documentais no Git Bash, foram necessárias duas adaptações locais:

1. **Normalização de caminhos em `scripts/ci/lib.sh`**: a função `tool_module_version` passou a aceitar separadores Windows, o que desbloqueou `lint` e `sast`.
2. **Wrapper `.tools/python3`**: aponta para o launcher `py` do Windows, permitindo executar scripts documentais sem instalação separada de Python.

O wrapper foi recriado antes da execução agregada; se for removido, os gates `ref-lint`, `rtm`, `deferrals`, `event-catalog` e `integration` voltam a falhar por falta de interpretador Python.

---

## 6. Veredicto global

- **Hipótese principal aceite:** a infraestrutura de CI e os gates essenciais de build, lint, arquitetura, política, SAST, contratos (com baseline) e auto-testes adversariais estão operacionais e verdes.
- **Hipóteses não eliminadas:** SCA, testes com `-race`, e o teste específico `TestBackpressureDropOldest` continuam fora do conjunto verde. Estes constituem os bloqueios reais à confiança total no sistema no ambiente atual.

---

## 7. Recomendações imediatas

1. **Atualizar a baseline do `govulncheck`** (`scripts/ci/baseline/govulncheck.txt`) e remediar ou mitigar as vulnerabilidades afetantes novas.
2. **Instalar um toolchain C** (mingw-w64 via MSYS2 ou `zig cc`) para correr `go test -race`.
3. **Corrigir `TestBackpressureDropOldest`** em `packages/substrate/bus` e rever por que falha sem `-race`.
4. **Criar tickets** para os 4 deferimentos `POR ATRIBUIR` identificados.
5. **Decidir** se as 10 divergências de contrato C3/C4/C5 e as 4 do catálogo de eventos devem ser resolvidas ou mantidas como dívida reconhecida no âmbito de AOS-196/AOS-201.

---

## 8. Anexo: detalhes da corrida agregada

### Gates incluídos e tempos

| Gate | Tempo | Resultado |
|---|---|---|
| secrets | 2 s | PASS |
| build | 30 s | PASS |
| lint | 127 s | PASS |
| ref-lint | 0 s | PASS |
| deferrals | 5 s | PASS |
| rtm | 0 s | PASS |
| layer-lint | 29 s | PASS |
| integration | 1 s | PASS |
| event-catalog | 4 s | PASS |
| policy-test | 10 s | PASS |
| sast | 126 s | PASS |
| selftest | 186 s | PASS |

**Tempo total:** 520 s  
**Resultado:** TODOS OS GATES VERDES

### Gates não incluídos nesta execução

- `test` (unitários sem `-race`)
- `test-race` (unitários com `-race`)
- `sca` (`govulncheck`)
- `replay`, `memory`, `supplychain`, `routing`, `security`, `evalgate`, `scale`, `dr-e2e`, `ux-dx`, `apex`, `coverage`, `deploy-gate`

A exclusão não implica que estas dimensões estejam verdes; apenas que não foram reexecutadas nesta passagem final.

---

# Adendo — revalidação adversarial em 2026-08-26

As sete hipóteses da secção 4 foram re-testadas **contra a realidade actual**, não contra este
documento. Três não sobreviveram, duas mudaram de carácter, duas mantêm-se.

| # | Hipótese de 24-08 | Veredicto | Prova |
|---|---|---|---|
| H1 | `TestBackpressureDropOldest` vermelho | **REFUTADA** | verde 3/3; corrigido no PR #162 |
| H2 | `-race` impossível | **CONFIRMADA** | `CGO_ENABLED=0`, sem compilador C na máquina |
| H3 | SCA vermelho, bloqueia merge | **REFUTADA** | `sca.sh` exit 0; baseline exacta |
| H4 | Antivírus/timeout instável | **CONFIRMADA, redefinida** | determinístico, não instável — ver abaixo |
| H5 | 10 dívidas de contrato C3/C4/C5 | **REFINADA** | gate verde; são divergências de NOME |
| H6 | 4 dívidas do catálogo de eventos | **REFINADA** | gate verde; estão no ANDAIME de teste |
| H7 | 4 deferimentos `POR ATRIBUIR` | **CONFIRMADA** | inalterados; gate passa por terem nota |

## H3 — a baseline não é permissiva (verificado, não assumido)

Não bastou o gate dizer «verde». `baseline_diff` compara a **linha inteira** `módulo|GO-ID` com
`comm -23`, pelo que a mesma vulnerabilidade num módulo NOVO apareceria como entrada nova e
bloquearia — não é comparação por ID solto. Calibração medida: **74 entradas correntes = 74 na
baseline, zero obsoletas**. Nem dívida morta, nem achado mascarado.

## H4 — não é instabilidade, e tem workaround

O Windows Defender põe em quarentena o binário de teste de **um** pacote:

```
actiondedup.test.exe: ... o ficheiro contém um vírus ou software potencialmente indesejável
```

Falha **3/3** (determinístico, não intermitente), isolado — os outros 13 pacotes do módulo
passam. Relocalizar o `GOTMPDIR` **não ajuda**: a heurística é sobre o CONTEÚDO do binário, não
sobre a localização.

**Workaround verificado:** `go test -ldflags="-s -w" ./breaker/actiondedup/` → **18 testes, todos
verdes, 3/3**. O pacote nunca teve problema; o que faltava era poder correr os testes.

**Não pôr no CI:** os símbolos são o que dá stack traces legíveis num panic. Trocá-los por uma
contornagem de antivírus local seria pagar diagnóstico de CI para resolver um problema de uma
máquina. O CI corre em Linux e não tem o sintoma.

## H5/H6 — o risco estava sobredimensionado

As 10 «dívidas de contrato» existem todas no código, com outro nome: `E_HASH_MISMATCH` é
`E_DIGEST_MISMATCH`, `E_SIGNATURE_INVALID` é `E_SIG_INVALID`, `E_REGION_DENIED` é
`ErrModelNotAllowed`. Semântica presente, nome divergente. O gate expõe ainda uma inconsistência
interna que vale um ticket: *«C1 usa o nome do contrato e passa; C5 não»*.

Os 4 literais do catálogo estão em `packages/testkit/env/seed.go` — andaime de teste. O gate é
explícito: «zero literais/concatenações não-baselinadas **no caminho de emissão**».

## O que fica mesmo por resolver

1. **`-race`** — precisa de toolchain C. O chocolatey está instalado, logo `choco install mingw`
   é o caminho. Instalação de sistema: decisão do dono. É o gate que mais falta faz num sistema
   concorrente.
2. **Quarentena do Defender** — exclusão para o binário, ou correr esse pacote noutro ambiente.
   Requer privilégios de administrador.
3. **4 deferimentos sem eixo** — os tickets continuam por criar no backlog.

## Nota de método

A recomendação nº 1 deste relatório («actualizar a baseline do govulncheck… bloqueia merge») era
**falsa à data desta revalidação**, e teria feito perder tempo a resolver um problema inexistente.
Um relatório de estado envelhece mais depressa do que parece: as suas conclusões são hipóteses
com data, não factos permanentes.
