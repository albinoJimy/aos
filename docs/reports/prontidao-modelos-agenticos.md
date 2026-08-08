# Prontidão do AOS para modelos agênticos

> **Data:** 2026-08-05 · **Revisto:** 2026-08-06 (ver [Revisão](#revisão-2026-08-06)) · **Natureza:** avaliação baseada em evidência viva (execução dos artefactos publicados e dos testes de aceitação nesta data), não apenas em documentação.
> **Âmbito:** imagem `aos-node:local` (17 MB, digest local `1c5e69cf488e` — hoje `bb0a8c00c4bf`), stack endurecida `aos-dev-hardened` (OIDC+Vault+LiteLLM), e testes de mediação dos módulos `kernel/reference-monitor`, `cmd/aos` e `integration`.
>
> ⚠️ **Este relatório foi REVISTO em 2026-08-06** por um painel adversarial de 5 lentes (verificação contra o HEAD). **Dois dos cinco gaps — incluindo o bloqueador nº1 — já estão fechados.** As secções afectadas estão marcadas em linha; o apuramento completo está em [Revisão](#revisão-2026-08-06).

## Veredicto

**Arquitecturalmente sim — e funcionalmente sim para efeitos locais governados.**

> **REVISTO (2026-08-06).** O veredicto original era «funcionalmente ainda não»: nenhuma tool call chegava a executar por omissão e o loop esgotava os turnos. **Isso deixou de valer**: o commit `303cf47` fez a autoridade de escopo derivar do token NHI verificado, e uma tool call legítima (`cap:fs.read`) **executa hoje** — provado em CI (`TestScopeTokenOnly_SemDirectorioExterno_ToolExecuta`, com o directório de autoridade VAZIO) e ao vivo (`run-fclive-2` → microVM Firecracker real). O que restava era o **feedback de negação** e o **circuito de aprovação humana** — ambos **fechados desde então**; ver [Revisão 2026-08-08](#revisão-2026-08-08--o-ciclo-de-aprovação-corrido-ao-vivo).

| Cenário | Pronto? |
|---|---|
| Correr modelos agênticos **sem que possam fazer mal** (sandbox governada, read-only, auditoria total) | **Sim** — é o que a stack hardened faz hoje |
| Agentes com **efeitos locais reais** (ler/escrever em sandbox) sob governo | **Sim** *(revisto)* — autoridade derivada do token + `MediatedLauncher` em microVM; `cap:fs.read` executa ponta a ponta |
| Agentes com **efeitos de REDE externa** (postar, chamar APIs) originados pelo modelo | **Não — por desenho.** `cap:http.post` é negada pelo taint-gate («untrusted não comanda», P4). Não é um gap de prontidão: é a propriedade a funcionar. Destravar exige o circuito de aprovação humana — que **passou a existir** (revisão 2026-08-08): uma acção escalada suspende o run, é aprovada por four-eyes e a **mesma** acção volta a atravessar a cadeia inteira. O taint NUNCA muda; a aprovação remove um obstáculo, não promove a autorização |
| Produção multi-tenant com soberania real | **Não** — tenant/IdP de soberania e HSM/KMS concreto são deferimentos nomeados (DEF-xxx) |

## Evidência recolhida (2026-08-05)

### 1. Imagem publicada — postura estática e fail-closed

- `USER 65532:65532` (non-root numérico), `ENTRYPOINT [/usr/local/bin/aos]`, zero volumes declarados, `HEALTHCHECK` por binário estático.
- Arranque sob `--read-only` + `--tmpfs /tmp` + volume explícito: `healthy` (3 sondas), `/healthz` e `/readyz` → 200.
- **Bind-guardrail**: bind a `0.0.0.0:8080` sem operadores ⇒ `exit 1` com shutdown gracioso.
- **Soberania de leitura**: `POST /runs` sem headers ⇒ `403`; com `X-Aos-Reader`/`X-Aos-Board` válidos ⇒ `201`.
- **Produção fail-closed**: `AOS_MODE=production` sem requisitos ⇒ `exit 1` («exige AOS_ISSUER_PUBKEY (trust-anchor-only)»).
- ~~⚠️ **Achado:** `Config.Labels` da imagem publicada está `null` — os labels OCI/ADR-017 do `deploy/node/Dockerfile` não estão nesta build. Republicar a imagem a partir do Dockerfile actual.~~ ✅ **RESOLVIDO (2026-08-06):** o achado era exacto para o digest avaliado (`1c5e69cf488e`, hoje *dangling*), mas o tag `aos-node:local` migrou para `bb0a8c00c4bf` com os **11 labels completos** (`org.opencontainers.image.*`, `org.aos.adr=ADR-017`, família `org.aos.supplychain.*`), definidos em `deploy/node/Dockerfile:85-95` desde `e23bcb6` (AOS-168). Residual REAL que se mantém: **atestação não anexada via OCI referrers** (ADR-017 residual 1, auto-declarado no próprio label).

### 2. Fluxo de negócio ponta a ponta (`demo-ciclo-completo.sh`, run `run-ciclo-223212`)

| Fase | Resultado |
|---|---|
| Saúde (produção) | ✅ 200/200 |
| Identidade (Bearer OIDC Keycloak + NHI ed25519 `agent-worker`) | ✅ |
| Submissão (residência selada, AOS-182) | ✅ 201 |
| Tool set frozen (catálogo assinado, `frozen_at` real) | ✅ |
| Modelo + mediação (Kimi via LiteLLM) | ⚠️ 16 turnos, **17 tool calls todas negadas** (`deny\|policy`), run morre em `MaxTurns` — **observação PRÉ-`303cf47`**: hoje uma capability dentro do `Scope` do token obtém `permit` e executa (as negações de `cap:http.post` mantêm-se, e correctamente, por taint). O `MaxTurns` sem saída antecipada continua por resolver (gap 2) |
| Leitura soberana (ID-token verificado) | ✅ 200; reuso do Bearer ⇒ 404 (fail-closed; código diverge do 401/403 esperado pelo demo) |
| Trajetória SSE | ✅ 113 eventos (`turn.recorded`, `step.checkpoint`, `replay.captured`, `run.toolset.frozen`) |
| Reconstrução soberana (AOS-214) | ✅ 200 |
| Auditoria (WORM + OTLP) | ✅ 16 decisões `run-*` + 18 registos `gov.read/*` + 17 spans `execute_tool` |
| Crypto-shred (DSAR) | ⚠️ sem shred ao vivo neste run — **mas a causa apontada estava errada** *(corrigido 2026-08-06)*. A cifra por-titular é conduzida pela **resposta do modelo capturada a cada turno** (`loop.go:357-366`, gatilho `sealer != nil && Subject != ""`), **não** por efeitos de tool — logo «nenhuma call produziu efeito» não explica a ausência de selo. Existe caminho de shred **ao vivo** (`demo-vault-shred.sh`: `POST /runs → /dsar/erase → chave Transit destruída`) e prova de nó mais forte que a citada: `aos093_substrate_erase_test.go` (seal → erase → `ErrDecrypt`, com a hash-chain WORM ainda válida). Se um run com durable-exec nada selou, investigar a **captura da resposta do modelo** |

### 3. Caminho de permit (tool call PERMITIDA e executada)

Verde nos três níveis, todos contra a cadeia de produção real (`NewProductionSecure`, sem stubs):

- `TestDemo_PermitEndToEndInputOutput` (`kernel/reference-monitor`) — `permit`, audit-before-effect, output real devolvido ao loop marcado `untrusted`.
- `TestAOS169_Mediation_PermitPath_ToolExecutes` (`cmd/aos`) — cadeia `identity → PDP(bundle assinado) → taint → scope → egress`, `permits ≥ 1`, `denials = 0`.
- `TestDemo_PermitNodeEndToEnd` (`integration`) — runtime seguro completo: modelo propõe `doc_read` no turno 1, executor corre 1×, output volta ao turno 2, run termina.

**Acrescentado na revisão de 2026-08-06** (fecha a margem «os e2e injectam autoridade populada, logo não distinguem token de directório»):

- `TestScopeTokenOnly_SemDirectorioExterno_ToolExecuta` (`integration`) — cadeia real com `SecuredConfig.Authority` **nil** ⇒ `AuthoritySource` **VAZIA** (o default do nó): `permits=1`, `denials=0`, tool executa 1×. Prova que a autoridade flui **só do token**.
- `TestScopeTokenOnly_CapForaDoTokenNegada` — controlo negativo com a mesma config: capability fora do `Scope` do token ⇒ `permits=0`, `denials=1`, **nenhum efeito**. Impede que o permit acima seja vacuoso.
- `TestDemo_SandboxNodeEndToEnd` (`integration`) — o mesmo permit, mas o efeito corre no substrato de **sandbox** (`MediatedLauncher`), com o output a voltar ao prompt do turno 2.

## O que está pronto

- **Mediação total de tool calls** — nenhuma execução fora do RM (ADR-002); revalidação de registry, PDP/Cedar com bundle assinado e trust anchor out-of-band, taint-gate, scope-gate, egress default-deny.
- **Identidade agêntica real** — NHI ed25519 por agente, cadeia `humano→agente`, classes com allowlist de capabilities, TTL, modo trust-anchor-only.
- **«Untrusted não comanda»** — taint-gate nega capabilities privilegiadas a calls originadas pelo modelo (comportamento correcto, verificado A/B).
- **Auditoria completa do loop agêntico** — audit-before-effect no WORM (hash-chain verificada no arranque, AOS-221), spans OTLP GenAI, trajetória SSE, residência soberana, anti-replay de credenciais de leitura.
- **Postura de produção fail-closed** — arranque recusado sem issuer externo, TLS, OIDC de soberania e board regions.

## O que NÃO está pronto (gaps, por ordem de impacto)

1. ~~**Nenhum efeito executa no deployment.**~~ ✅ **FECHADO (`303cf47`, 2026-08-06)** — *era o bloqueador nº1.*
   O diagnóstico original (o tecto do ScopeGate, `Config.Authority`, só ser escrito por testes) estava correcto, mas **o remédio proposto foi superado**. Um directório estático **nunca** poderia resolver o sujeito-**agente**, que é criado **por-mint (dinâmico)**: só o token o conhece. A correcção faz o hook de identidade — que já verifica o token assinado — povoar `Principal.SubjectAuthority` com o grant por-sujeito derivado do `Scope` **verificado**, e o ScopeGate resolve daí (intersectando com um directório externo **quando existir**, para revogação/RBAC).
   **Zero config nova.** Provado em CI: `TestScopeTokenOnly_SemDirectorioExterno_ToolExecuta` (Authority **vazia** ⇒ `permits=1`, tool executa) + controlo negativo (`TestScopeTokenOnly_CapForaDoTokenNegada`). Ao vivo: `run-fclive-1` `deny|scope` → `run-fclive-2` **permit** → microVM Firecracker real (`cmd=read`).
   ⚠️ **Nota honesta:** sem directório externo, o ScopeGate passa a verificar `capability ∈ token.Scope` — que o hook de identidade já impõe. Não é vulnerabilidade (o grant é assinado pelo issuer e o eixo user∩classe é computado no **mint**), mas a **defesa-em-profundidade do AOS-071 fica dormente** por omissão. Provisionar uma `AuthoritySource` externa restaura a segunda opinião independente e habilita revogação.
2. ~~**Feedback de negação para o loop.**~~ ✅ **FECHADO** (`60f5d64` marcador de negação no tail + `f0c2bd8` breaker cablado). O diagnóstico abaixo mantém-se registado por ser exacto. ⚠️ *(texto original)* **AINDA VÁLIDO — e mais grave do que aqui se dizia.** Não é «fraco»: é **descartado**. `mediateToolCall` (`loop.go:556`) devolve apenas `Untrusted(dec.Output)` + `dec.ToolErr`; num deny **ambos são vazios/nil**, pelo que o tail materializa corpo **vazio** — o modelo não distingue «negado» de «a tool não devolveu nada». Isto explica o Kimi repetir a mesma estratégia 16×. Os dados já existem (`Effect/Code/Reason/DeniedBy`, `decision.go:61-72`) e o padrão já vive noutro caminho: é **encanamento**, não construção. **Remédio (2 eixos):** (a) marcador estruturado no tail; (b) terminação por orçamento de negações — para a qual existe um **circuit breaker pronto e não-cablado** (`breaker/breaker.go`, `MaxStaleIterations` + `actiondedup`): `Observe` nunca é chamado e não há sequer uma `Option` no Runtime. **Cablar, não reescrever.**
3. ~~**Circuito negação→aprovação humana.**~~ ✅ **FECHADO E PROVADO AO VIVO (2026-08-08)** — escalar → suspender → four-eyes → retomar → executar numa microVM real, com o efeito já aplicado a NÃO repetir-se. Fechá-lo exigiu corrigir **seis** defeitos de composição e dois de retoma; ver [Revisão 2026-08-08](#revisão-2026-08-08--o-ciclo-de-aprovação-corrido-ao-vivo). ⚠️ *(texto original)* **PARCIALMENTE VÁLIDO — a afirmação original misturava três coisas.**
   - ✅ **Válido:** não existe bridge `deny → aprovação → reexecução` de uma tool call. `FourEyes` é um endpoint `/approve` **autónomo** para acções irreversíveis (`api.go:985-1068`), **não** um hook da cadeia de mediação (`secured.go:270-278` não o tem); nenhum código apanha `Decision.DeniedBy` e escala. **É o gap real.** ✅ **RESOLVIDO** — o `ApprovalGate` entra na cadeia (`aabf4eb`), o oráculo de autonomia passa a reconhecer a aprovação (`4214b3b`) e o loop suspende/retoma (`bbd89b9`/`de014a9`/`1a04174`).
   - ❌ **Falso já à data deste relatório:** «operadores de steer/pause … não estão ligados ponta a ponta ao loop». Foram ligados por **AOS-218 (`53c224d`, 2026-07-31)**, antes desta avaliação.
   - ❌ **Premissa inválida:** «é a via natural para destravar o gap 1» — o gap 1 foi destravado por outra via (o token), sem four-eyes no caminho.
4. **DEMO-GRADE declarados** — ⚠️ **severidade recalibrada: a lista misturava *default do nó nu* com *limitação do deployment avaliado*.**
   - **Vault in-memory «por omissão»:** verdade como default de código, mas **enganador** como limitação da stack avaliada — o deployment endurecido injecta `AOS_DSAR_VAULT_ADDR` (Vault externo **persistente**, storage `file`, TLS) e a produção é **fail-closed** (`ErrProductionNeedsDurableKEK`).
   - **Custódia de chaves «deferida»:** **exagerado** — a costura está entregue (adaptador Vault Transit, key-never-leaves, AOS-215/216); o que é infra-organizacional é o **HSM/KMS concreto**.
   - **Autenticação humana / autoridade co-localizada / tenant de soberania:** ✅ confirmados e honestamente declarados (DEF-201/212/213, eixo AOS-205, ligados a D4/EPIC-16). São **deferimentos rastreáveis com dono e gatilho**, não omissões — e o **mecanismo** já existe.
5. ~~**Cadeia de release:** labels OCI/ADR-017 ausentes da imagem publicada.~~ ✅ **FECHADO** — ver o achado corrigido na §1. Resta housekeeping (purgar o digest *dangling*; fixar no CI que o release republica sempre o tag) e o residual real: atestação por **OCI referrers**.

## Condições para uma tool call ser permitida e executar (checklist)

| Condição | Onde se configura | Estado no nó live |
|---|---|---|
| Tool no registry com face do modelo + binding de governança | `AOS_MODEL_TOOLS` | ✅ configurável |
| Catálogo assinado (revalidação, AOS-051) | `AOS_MODEL_TOOLS_REGISTER=1` | ✅ na stack hardened |
| NHI com a capability no scope | `aos-issuer mint --caps …` | ✅ |
| Capability na allowlist da classe (AOS-007) | `capabilities/allowlist.json` (bundle assinado) | ✅ (`agent-worker`) |
| Bundle PDP assinado carregado | `AOS_POLICY_BUNDLE_DIR` + `AOS_POLICY_TRUST_ANCHOR` | ✅ |
| Regra Cedar sem cláusula de taint (ou origem trusted) | política Cedar | ✅ `fs.read`; ❌ `http.post` pelo modelo |
| **Tecto user∩classe concede a capability (AOS-071)** | *(revisto)* **`Scope` do token NHI** — computado no mint pelo issuer (`UserAuthority ∩ ClassPolicy.Scope`) e verificado no nó. `Config.Authority` é hoje **opcional** (intersecta, para revogação/RBAC) | ✅ **flui do token** (`aos-issuer mint --caps …`) |
| Destino na allowlist de egress (só rede) | config de egress | só p/ rede |

## Recomendação

> **REVISTA (2026-08-06).** Os itens 1 e 4 originais **já estão feitos**; o item 1 foi resolvido por uma via melhor do que a proposta.

O AOS está pronto para **conter** modelos agênticos **e para os deixar trabalhar em efeitos locais governados**. O que falta é a camada de *diálogo* com o modelo e a escalada humana:

1. ~~`AuthoritySource` provisionável ligada ao `Config.Authority`~~ ✅ **FEITO** por `303cf47` (autoridade derivada do token — superou o remédio proposto).
2. ~~**Feedback de negação estruturado** devolvido ao modelo + terminação por orçamento de negações~~ ✅ **FEITO** (`60f5d64`, `f0c2bd8`).
3. ~~**Circuito negação→four-eyes→execução aprovada** ligado ao loop~~ ✅ **FEITO e provado ao vivo** (2026-08-08). Nota de rigor sobre a formulação original: a reexecução **não** passa a ter «autorização trusted» — o taint permanece `untrusted` e a aprovação é uma prova não-forjável num campo não-exportado que remove UM obstáculo. Promover o taint teria sido um bypass; não foi feito.
4. ~~Republicar a imagem com os labels OCI/ADR-017~~ ✅ **FEITO**; resta purgar o *dangling* e fixar a republicação no CI.
5. ~~*(novo)* **Provisionar uma `AuthoritySource` externa**~~ ✅ **FEITO** (`03afcbb`): `AOS_AUTHORITY_FILE`. Faltava a **via**, não a política — o campo existia e só era atribuível por código. ⚠️ **Revogar não é remover**: um sujeito ausente cai na autoridade do token; revoga-se com `"capabilities": []`.
6. *(novo, 2026-08-08)* **Ligar o billing/custos ao nó** — ver [Avaliação complementar — billing de tokens](#avaliação-complementar--billing-de-tokens-e-gestão-de-custo-de-modelos-2026-08-08): substituir o `BudgetStub` pelo `BudgetCheck` real na cadeia do RM, expor o `cost.Recorder` na montagem do gateway, e dar superfície de config aos limites.

A fundação é sólida e honesta — os gaps estão declarados no próprio código e banners, não escondidos.

---

## Revisão 2026-08-06

Painel adversarial de **5 lentes** (segurança/autorização, loop-runtime, SRE/release, auditoria de evidência, soberania de dados), cada uma a desafiar as alegações **contra o código no HEAD**, seguida de um verificador céptico por lente. **Os 5 desafios foram sustentados** (`upheld`); não houve divergência de veredicto.

| Gap original | Veredicto revisto |
|---|---|
| 1 · Nenhum efeito executa | **STALE — resolvido** (`303cf47`; confirmado por 3 lentes independentes) |
| 2 · Feedback de negação fraco | **Ainda válido — severidade agravada** (é descartado, não fraco) |
| 3 · Negação→aprovação humana | **Cindido:** bridge ausente ✅ válido · steer/pause ❌ já ligados (`53c224d`) · premissa causal inválida |
| 4 · DEMO-GRADE | **Severidade ajustada** (default do nó nu ≠ stack avaliada) |
| 5 · Labels OCI | **STALE — resolvido** (imagem `bb0a8c00c4bf`) |

**Ressalvas honestas registadas pelos verificadores** (não derrubam conclusões):

- A execução na microVM (`run-fclive-2`) foi observada ao vivo, **não em CI** — o mecanismo (ScopeGate corrigido) está provado em CI; a execução viva não.
- O «16 turnos, nada selado» da linha de crypto-shred **não é verificável a partir do código** (não há log do run no repo); o mecanismo causal e o caminho ao vivo estão estabelecidos.
- A margem «caminho token-only sem teste CI end-to-end», levantada pelo painel, foi **fechada** logo a seguir por `TestScopeTokenOnly_*` (commit `8281bcb`).

---

## Avaliação complementar — billing de tokens e gestão de custo de modelos (2026-08-08)

> **Pergunta:** «o processo de billing de tokens está pronto para gerir os modelos?»
> **Resposta:** os **componentes** existem, estão testados e são dos mais rigorosos do projecto — mas **nenhum está ligado ao nó**. No deployment actual não há gestão de custo activa. Verificado contra o HEAD `6c88a7a` **e** contra a branch mais recente (`feature/AOS-128-ux-dx-tests`, a que contém o fix do ScopeGate `303cf47`): o veredicto é o mesmo nas duas.

### Componentes prontos (testados, verdes)

- **`control-plane/budget` (AOS-008, ADR-008)** — orçamento hierárquico por árvore de execução em **duas dimensões** (tokens **e** micro-USD, inteiros, nunca float). Reserva atómica CAS `Reserve → Commit | Release` com rollback parcial; **0-overshoot provado** (200 goroutines sob `-race`); idempotência de commit/release sem leak; eventos `budget.reserved/committed/released` no Event Store com `Rebuild` só do log. O hook do RM `BudgetCheck` estima custo → circuit breaker → reserva; **sem headroom ⇒ deny fail-closed e auditado**.
- **`model-gateway/metering/cost` (AOS-062)** — custo por chamada em micro-USD inteiro (4 tipos de token × tabela global), emitido no span OTel `gen_ai.usage.cost_usd`, agregado por run/árvore. **Fail-closed**: custo não-calculável aborta a chamada (nunca 0 silencioso). Streaming correcto: o metering corre no EOF, sobre o `usage` final.
- **`pricing` (ADR-011)** — tabela versionada (`2026.07`, por modelo+região); **alteração de preço é evento explícito selado no WORM** (added/removed/updated com old→new) — nunca silenciosa, nunca falsifica o burn-down.
- **`control-plane/governance/progress-surface` (AOS-123)** — burn-down **lido** da agregação de custo (sem recontabilizar), prompt de exaustão a ~80% configurável com exactamente 3 opções (`extend`/`summarize_stop`/`abort`) e degradação graciosa por timeout; spans `aos.control.exhaustion_*` sem PII.
- **`orchestrator/planvalidate` (AOS-232, EPIC-19)** — orçamento re-preçado com tecto por nó na validação de planos do meta-orquestrador.

### O que NÃO está ligado (o gap)

| Peça | Evidência | Consequência |
|---|---|---|
| **Budget é STUB na cadeia do RM** | `packages/integration/secured.go:324` — `BudgetStub{}` («stub aceitável»); idêntico na branch `feature/AOS-128-ux-dx-tests` | **Zero admission control de custo/tokens ao vivo** — nenhum run tem tecto de gasto |
| **Gateway de produção sem cost recorder** | `AssembleModelGateway` (`integration/modelgateway.go:90`) → `NewProduction`: o `ProductionConfig` **nem sequer expõe** o seam de custo; `WithCost` só é alcançável em testes | Nenhum custo é calculado/emitido nos spans do nó real — e o burn-down de AOS-123 fica sem fonte |
| **progress-surface fora do nó** | só é consumida por `packages/qa/ux-dx` (testes de usabilidade) | O prompt de exaustão a ~80% não existe no runtime real |
| **Sem superfície de config** | nenhuma `AOS_BUDGET_*`/`AOS_*THRESHOLD*` na tabela env (AOS-203) | O operador não consegue definir limites por run/árvore |
| Estimador placeholder | `DefaultEstimator` declarado «placeholder honesto» | Falta a contagem real de tokens do prompt + tarifa do provider |
| Backend distribuído deferido | README do budget: token-bucket Redis/consenso sobre TPM/RPM do provider fora de âmbito | Só o in-memory de referência |

### Prova prática (observada em 2026-08-05)

No `demo-ciclo-completo`, o Kimi queimou **16 turnos de tokens reais** a retentar tool calls negadas — e o **único** travão foi `MaxTurns`. Nenhum orçamento interveio, porque não há nenhum ligado. É exactamente o cenário que o billing devia gerir: um loop agêntico a gastar sem rumo.

### Acções para fechar (ticket)

1. Substituir `BudgetStub{}` por `BudgetCheck` real na cadeia de `secured.go` (o hook, os testes e o fail-closed já existem).
2. Expor o `cost.Recorder` no `ProductionConfig`/`AssembleModelGateway` e ligá-lo com `WithCost` (fonte do burn-down).
3. Ligar a `progress-surface` ao runtime do nó e expor limites/limiar por env (superfície AOS-203).
4. Substituir o `DefaultEstimator` pela contagem real de tokens + tarifa do provider; planear o backend distribuído (ADR-008).

**Veredicto:** desenho e componentes — prontos e rigorosos. Gestão efectiva do custo dos modelos no deployment — **ainda não**; é o mesmo padrão já visto neste relatório (capacidade construída, costura de composição em falta), com a diferença de que aqui o remédio é puramente de *wiring*: todas as peças duras já estão verdes.

---

## Inventário — outras capacidades na mesma condição (2026-08-08)

> **Pergunta:** «que outro tópico/conceito está nas mesmas condições?» — ou seja, construído e testado, mas sem efeito no deployment. Varredura feita sobre os imports de código **não-teste** de `packages/cmd/aos` + `packages/integration`, os stubs da cadeia do RM e os banners de arranque do nó. Há **três grupos**, e distingui-los importa.

### Grupo A — Sem costura nenhuma (nenhum env liga; exige código novo)

Zero imports em código não-teste do nó/ápice:

| Componente | Epic | Estado |
|---|---|---|
| **budget / admission control** | AOS-008 | `BudgetStub{}` na cadeia (`integration/secured.go:324`) — ver secção de billing |
| **progress-surface** (burn-down + prompt de exaustão a ~80%) | AOS-123 | Construída e testada; consumida **só por testes de QA** (`qa/ux-dx`) |
| **Credential Broker (BRK)** | EPIC-06/07 | `platform/broker` **não é importado** pelo nó — credenciais entram por ficheiro montado; o broker não medeia nada no runtime |
| **Orquestrador (ORQ)** | EPIC-03 | 0 imports — o nó hospeda runs in-process (service loop com lease); o orquestrador distribuído não governa o deployment |
| **Escalonador (SCH)** | EPIC-03 | 0 imports — idem |
| **Attestation WebAuthn (componente)** | AOS-177 | `platform/attestation` nem está no `go.mod` do nó; enforcement **dormente** sem `AOS_ATTESTATION_VERIFIER_URL` |

### Grupo B — Ligados ao nó, mas desligados/inertes por omissão

Têm costura env (a capacidade liga-se por configuração), mas no arranque de referência estão off — o banner declara cada um:

- **Execução durável (AOS-180)** — checkpointer/capturer/step-ledger não compostos (no-op AOS-013); exige `AOS_DURABLE_EXECUTION=1` + `AOS_EVENTSTORE_PATH`. Sem ela, o conteúdo dos runs nem é persistido.
- **Observabilidade OTLP (AOS-173)** — `NoopTracer` por omissão (`AOS_OTLP_ENDPOINT`).
- **Four-eyes (AOS-162)** — `POST /runs/{id}/approve` responde 501 sem `AOS_APPROVERS_FILE`.
- **Promotion controller (AOS-159/206)** — composto sempre, mas sem `AOS_RATIFIERS` toda a promoção é negada.
- **Expiração TTL (AOS-092/213)** — `ExpirationJob` composto mas varre sem apagar sem `AOS_RETENTION_VERSION`+`AOS_RETENTION_PERIODS`.
- **mTLS do plano de controlo (DEF-012)** — desligado por omissão (`AOS_CONTROL_MTLS_CA_PATH`, exige TLS no nó).
- **Custódia externa da KEK (AOS-215)** — vault in-memory demo sem `AOS_DSAR_VAULT_*`; em produção com substrato durável é **obrigatória** (`ErrProductionNeedsDurableKEK`).
- **Velocidades de queima do breaker** (`cmd/aos/breaker_thresholds.go`) — desligadas por omissão, declarado como deliberado.

### Grupo C — Residual nomeado

- **Signer de checkpoints (AOS-221)** — não é composto (a chave privada não vive no nó, por desenho); a **truncatura do tail do WORM** fica aberta — a re-verificação detecta mutação/inserção/remoção-interna, não o corte do fim da cadeia.

### Leitura honesta

O Grupo A é a resposta à pergunta: **billing, burn-down/exaustão, broker de credenciais, orquestração e escalonamento distribuídos, e attestation** são capacidades verdes em teste que o deployment não consegue ligar nem por configuração. Nota de justiça: ORQ/SCH/broker serem externos ao nó é **coerente com a forma de produto v1** (runtime de referência single-node — a Carta adia a topologia multi-plano); não são bugs, são **deferimentos de composição**. Mas convém dizê-lo sem eufemismo: hoje são código à espera de um composition-root multi-nó. O Grupo B é configuração de deployment (risco operacional de «postura anunciada ≠ postura ligada» — mitigado pelos banners explícitos e pelo fail-closed de produção); o Grupo C é um residual de segurança nomeado e aceite.

---

## Revisão 2026-08-08 — o ciclo de aprovação corrido AO VIVO

> **Origem:** não foi uma auditoria, foi uma execução. Pediu-se «corra o ciclo de aprovação ao vivo na stack dev». O ciclo **não corria** — e não corria por seis razões independentes, nenhuma delas um bug num componente. Todas eram falhas de **encaixe**, num caminho cuja suite estava inteiramente verde.

### Gap 2 (feedback de negação) e gap 3 (circuito negação→aprovação): **FECHADOS**

O gap 3 — «não existe bridge `deny → aprovação → reexecução`» — está fechado e **provado ao vivo**, com o run a terminar com o conteúdo real do documento lido numa microVM Firecracker:

```
00:17:42  escalate  step-000001-tool-1                     → run SUSPENSO (waiting_on_human)
          cerimónia four-eyes: alice+bob, 3 eixos distintos, attestation WebAuthn → grant durável
          retoma com credencial NHI FRESCA (a original nunca é persistida)
00:18:03  allow     step-000001-tool-1                     ← atravessou a cadeia INTEIRA
00:18:03.764  [guest-agent] pedido: cmd="read" path="notes"  ← microVM real
00:18:08  escalate  step-000002-tool-1                     ← a acção seguinte exige a SUA aprovação
```

O gap 2 fechou antes (marcador de negação no tail + breaker cablado, `60f5d64`/`f0c2bd8`).

### Os seis defeitos de composição (todos corrigidos, todos com teste)

| # | defeito | porque a suite verde não o via | commit |
|---|---|---|---|
| 1 | a escalada retornava de dentro do laço e **não capturava o turno** ⇒ retoma sem trajectória | o teste de composição **simulava** o replay com um modelo determinista | `bbd89b9` |
| 2 | o **lease durável nunca é revogado** (só expira por TTL) ⇒ a própria réplica não re-hospedava o run que suspendeu | só aparece quando a mesma réplica tenta retomar | `de014a9` |
| 3 | a suspensão é durável ⇒ uma **segunda escalada** tentava `waiting_on_human→waiting_on_human` e o run morria FALHADO | só aparece no segundo ciclo | `de014a9` |
| 4 | o **`ApprovalGate` nunca foi ligado à cadeia** — a evidência viajava e ninguém a lia | o teste construía o seu próprio Reference Monitor | `aabf4eb` |
| 5 | o **oráculo de autonomia** não sabia da aprovação ⇒ aprovar nunca satisfazia quem exigira a aprovação | o `escalate` vinha de um hook de teste, não do PDP | `4214b3b` |
| 6 | o PEP **não sabia cumprir a obligation `autonomy`** ⇒ negava TODO o permit do oráculo | o `escalate` curto-circuita a cadeia ANTES do enforcement | `4214b3b` |

Mais um de fronteira: a **reescrita args→`ExecRequest`** corria no despacho, depois de a preview ter sido calculada — o humano aprovava os args do modelo e o RM mediava o `ExecRequest`. Duas descrições do mesmo passo; o grant era emitido, encontrado e **consumido**, e a amarra falhava (`aabf4eb`).

### Depois, os que os testes novos encontraram

Ao escrever o teste de composição **pela cadeia de produção** (`NewSecuredRuntime` e `Bootstrap`, sem hooks de teste), a asserção «um turno reproduzido não pode ir ao modelo» deu 2 em vez de 1:

- o decorador de retoma **nunca envolvia um `cfg.Model` injectado** (vivia no construtor por-ambiente do `main`);
- o **plano de replay morria no detach do contexto** de `Submit` (`context.Background()`): **a retoma nunca reproduziu trajectória nenhuma**.

Ao vivo isto ficara escondido porque o modelo, reinterrogado com o mesmo prompt, devolveu **por acaso** a mesma tool call — a preview coincidiu. Determinismo por sorte, não por desenho (`d676e04`, `1735d07`).

### Gap 5 (`AuthoritySource` externa): **FECHADO**

`Config.Authority` existia no ScopeGate mas só era atribuível **por código** — nenhum deployment o conseguia provisionar. Consequência: **não havia revogação**; um token válido valia até expirar. `AOS_AUTHORITY_FILE` liga um directório JSON montado (`03afcbb`).

Provado ao vivo — o **mesmo token**, o mesmo objectivo, só o directório mudou:

```
antes:  seq=1 allow tool=doc_read cap=cap:fs.read
depois: seq=1 deny  tool=doc_read cap=cap:fs.read denied_by=scope
        reason="capability fora do escopo efectivo utilizador ∩ classe (default-deny)"
```

Duas notas que não são detalhe:

- **Revogar não é remover.** Um sujeito **ausente** do directório NÃO é restringido — cai na autoridade do token. É o que torna seguro ligar um directório parcial, mas revoga-se listando-o com `"capabilities": []`.
- **Não é assinado**, e é escolha: o directório só pode RESTRINGIR. Adulterá-lo nega acções (negação de serviço, visível e auditável) mas não concede nenhuma — a ampliação está estruturalmente fora do alcance do gate, que intersecta com o grant assinado pelo issuer.

### Achado NOVO (não constava de nenhuma revisão): o audit não sabia atribuir

O `MediationRecord` do RM traz `Effect`/`Code`/`DeniedBy`/`Reason`; o `AuditRecord` selado guardava **só o veredicto**. O log inviolável provava QUE uma acção foi recusada, não **por quem** nem **porquê** — e responder a isso obrigou, nesta sessão, a subir um segundo nó com o oráculo desligado e comparar.

Fechado em `1b326d1` com **versão por-registo** (os selos de um WORM não se re-escrevem, e o arranque re-verifica a hash-chain fail-closed): cada registo verifica-se com as regras da SUA época. Compatibilidade provada sobre um log real — **236 partições** do WORM da stack dev, escrito pelo binário anterior, re-verificadas ao arranque pelo novo.

Via de acesso em `6c88a7a` (`aos audit-trail`), porque selar sem dar por onde ler seria repetir o padrão que a sessão fechou.

### Outro achado NOVO, revelado pelo anterior

A atribuição recém-selada expôs, numa linha, um defeito introduzido nesta mesma sessão: ao impor a obligation `autonomy`, foi reconstruída no kernel uma **segunda tabela** de «que modos exigem humano», adivinhando os nomes (`sample` em vez de `post_hoc_sample`). L5 sobre uma acção `danger` — que por desenho **corre** — era negada. Corrigido eliminando a segunda tabela: o PDP emite o **veredicto**, o PEP impõe-no, e a igualdade das chaves entre camadas é verificada por teste (`9b32092`).

### Suspensão durável

O balde `suspended` do serviço era **in-memory** enquanto o registo de retoma, o pendente, o grant e a transição de estado eram todos duráveis. Um restart perdia a única peça volátil: `GET` passava a 404, a retoma dava `ErrRunNotSuspended`, e re-submeter o mesmo RunID **recomeçava do zero** um run à espera de um humano. Fechado em `1a04174` (o balde passa a cache; a fonte de verdade é a máquina de estados durável), provado com um `restart` a meio do ciclo.

### Padrões, para não se repetirem

Dois, e ambos apareceram mais do que uma vez:

1. **Mecanismo sem via de acesso.** O `/approve` existia e nada produzia a perna assinada (→ `aos-issuer approve-sign`). O `Config.Authority` existia e nenhum deployment o provisionava (→ `AOS_AUTHORITY_FILE`). O `ResumeFromHuman` existia sem chamador. A atribuição, mal selada, não tinha por onde ser lida (→ `aos audit-trail`).
2. **Duas tabelas da mesma verdade.** A preview calculada em dois sítios sobre `Call`s diferentes; o vocabulário de oversight reconstruído no kernel. Corrigidos eliminando a segunda cópia, não sincronizando-a.

### Nota metodológica (a mais importante desta revisão)

> **Um teste de composição que substitui a peça vizinha por um duplo NÃO é um teste de composição.**

Os seis primeiros defeitos sobreviveram exactamente aí: cada peça testada isoladamente, e o encaixe testado contra um Reference Monitor construído à mão. A rede que ficou está em duas camadas — `integration/approval_chain_real_test.go` (cadeia `NewSecuredRuntime` com bundle assinado e oráculo ligado) e `cmd/aos/approval_cycle_node_test.go` (nó completo via `Bootstrap`, dois ciclos, a contar as idas ao modelo) — com âncoras de não-vacuidade explícitas, incluindo uma que omite o `ApprovalVerifier` e exige que o ciclo **não** feche.

### Estado dos gaps após esta revisão

| Gap | Estado |
|---|---|
| 1 · Nenhum efeito executa | ✅ fechado (`303cf47`, revisão anterior) |
| 2 · Feedback de negação | ✅ fechado (`60f5d64` + breaker `f0c2bd8`) |
| 3 · Negação→aprovação humana | ✅ **fechado e provado ao vivo** (7 commits desta sessão) |
| 4 · DEMO-GRADE declarados | severidade já recalibrada; inalterado |
| 5 · Labels OCI | ✅ fechado (revisão anterior) |
| 5-bis · `AuthoritySource` externa | ✅ **fechado** (`03afcbb`) — revogação a funcionar |
| **NOVO** · audit não atribuía a negação | ✅ **fechado** (`1b326d1` + `6c88a7a`) |

Fora do âmbito desta sessão e **em aberto, com dono**: **D4/EPIC-16** (autoridade de identidade), **critério 42** (SAST/SCA), e o conteúdo de produção do directório de autoridade — que é decisão de deployment, não de código.

---

## Análise aprofundada por tópico (2026-08-08)

> Varredura em 6 frentes, cada alegação verificada contra o código (não apenas docs). Formato por tópico: **propósito · provado · costura em falta (ficheiro:linha) · risco · fecho (esforço)**.

### ⚠️ Correcções ao inventário acima (a verificação mudou a classificação)

1. **Attestation NÃO é Grupo A** — o verificador liga-se por env (`AOS_ATTESTATION_VERIFIER_URL` → `bootstrap.go:961-970`; empacotado na stack dev-hardened). Os verdadeiros itens Grupo A são as portas **`ChallengeIssuance`** (frescura/liveness) e **`DeviceEnrollment`** (atribuição dispositivo↔aprovador): implementadas e testadas em `integration/`, **zero ocorrências em `cmd/aos`**. Sem elas, a attestation prova modelo+posse mas **não liveness** (replay possível) nem atribuição — aquém de ADR-016 §4.
2. **Promotion controller é pior do que «inerte»** — mesmo **com** `AOS_RATIFIERS` definido, **não existe endpoint HTTP nem subcomando CLI** para submeter uma ratificação (`Promote` só in-process; declarado em `bootstrap.go:1395` como deferido para AOS-096). Capacidade verde-em-teste sem superfície operacional.
3. **Breaker — dois achados novos, um GRAVE (fail-open):**
   - **5a (grave):** ligar as velocidades de queima (`AOS_BREAKER_MAX_*_PER_SEC`) **desliga o disjuntor inteiro em silêncio** — o nó nunca cabla uma `VelocitySource` (`breaker_wiring.go:70-72`), `NewBreaker` devolve `ErrVelocitySourceMissing`, o `resolve()` engole o erro e devolve `nil` (`breaker_wiring.go:73-77`): o run fica **sem no-progress, sem wall-clock, sem velocidade**. O operador que segue o README fica *menos* protegido.
   - **5b:** `observeAction` (`breaker_wiring.go:83`) é **código morto** (zero chamadores) ⇒ o detector nunca observa ⇒ `MadeProgress()` é sempre `true` ⇒ **o sinal no-progress nunca dispara em produção**; só o wall-clock (30 min) funciona. Não existe nenhum teste ao nível do nó que prove o disparo do breaker.
4. **Execução durável:** ligável por env (Grupo B confirmado), mas **o `Resumer` (resume-from-step, AOS-015) nunca é composto no nó** — o nó *escreve* checkpoints e **nunca os lê**; crash-resume é manual (re-submeter o RunID) e recomeça o loop do turno 1.
5. **ORQ/SCH:** a exclusão do nó é **enforced por teste** (`boundary_orq_sch_test.go`, directo + transitivo) — ADR-018 com guarda falsificável, não acidente. Única excepção produtiva: `model-gateway/routing/tieradapter` (importa tipos do SCH), ele próprio **não composto** em `NewProduction`.
6. **OTLP:** ligável por env, mas **SLOs/alertas/dashboards (AOS-085/086) não têm produtor em runtime** — as funções puras existem, nada no nó as alimenta; o export é só traces (`/v1/traces`). E `otel-genai/doc.go` ainda diz exporter «DIFERIDO» (obsoleto desde AOS-173).

---

### Grupo A aprofundado

#### A1. budget / admission control (AOS-008, ADR-008)

- **Propósito:** admission control de custo — orçamento hierárquico por árvore em tokens **e** micro-USD (inteiros), reserva atómica CAS antes de cada spawn/tool call.
- **Provado:** `TestReserve_ConcurrentCAS_ZeroOvershoot` (200 goroutines, `-race`), `_RollbackOnAncestorFailure`, `TestSettle_ConcurrentCommitRelease`, `TestRebuild_FromEventStore`, `TestBudgetCheck_DenyNoHeadroom_FailClosedAndAudit` (+commit-em-permit, release-em-deny, breaker). **Não é código órfão:** tem consumidores reais — `orchestrator/delegation.go` (AOS-026), `scheduler/breaker.go`, `metering/cost/budgetbridge` (AOS-062) — que, por sua vez, não estão ligados ao nó.
- **Costura em falta:** `integration/secured.go:324` (`BudgetStub{}`, allow-incondicional). Três lacunas exactas: (a) `SecuredConfig` **não tem campo** de orçamento — nem por código se injecta; (b) falta o chamador de `BudgetCheck.Settle` pós-`Mediate` no loop; (c) nenhuma env `AOS_BUDGET_*`. **O banner não declara o stub** — diverge da disciplina AOS-203.
- **Risco:** loop agêntico sem tecto de gasto (o caso observado: 16 turnos, só `MaxTurns` como travão). Efeito em cadeia: sem budget, a progress-surface não tem fonte; sem `WithCost` no gateway, o burn-down não tem dados.
- **Fecho:** banner (**pequeno**); campo em `SecuredConfig` + compor o hook + ponto de `Settle` (**médio** — hook e testes já existem); envs + estimador real (**médio**); backend distribuído (**grande/deferido**, ADR-008).

#### A2. progress-surface (AOS-123)

- **Propósito:** burn-down lido da agregação EPIC-08 (sem recontabilizar) + prompt de exaustão a ~80% com 3 opções (`extend`/`summarize_stop`/`abort`) delegadas ao admission; degradação graciosa por timeout.
- **Provado:** `TestComputeBurndown_MatchesAggregateByTrace_NoReaccounting`, `TestResolvePrompt_Extend_DelegatesAndDoesNotMutateBudget`, `TestOnPromptTimeout_Degrades`, `_NilDegrader_FailClosed` + cobertura UX (`qa/ux-dx/usability_test.go:190-242`). **As 4 portas têm implementações concretas do outro lado** (`budget.Budget`, `scheduler.HeadroomController.Admit`, `scheduler.Degrader.ExecuteChain`, `controlsurface.StateProjector`) — faltam só os adaptadores (padrão `budgetbridge`/AOS-121).
- **Costura em falta:** zero consumidores de produção (só QA); o ponto de invocação (`Evaluate` por turno no loop + devolução da decisão via canal de controlo/SSE) não existe; nenhuma env. **Bloqueio duplo:** sem budget ligado (A1) e sem `WithCost` no gateway, a superfície composta só mostraria 0/0.
- **Risco:** quando A1 fechar, sem esta superfície o operador volta ao hard-stop cego — o run morre ao esgotar sem aviso nem escolha (o anti-padrão que AOS-123 elimina).
- **Fecho:** adaptadores das 4 portas + composição no nó (**médio**); ponto de invocação por turno + env do limiar (**médio**). Pré-requisito: A1.

#### A3. Credential Broker (BRK, AOS-070, ADR-006)

- **Propósito:** trocar o NHI do agente por credenciais downstream **server-side** — handle opaco de 128 bits, valor nunca observável, TTL curto revogável, injecção directa no mount da microVM.
- **Provado:** 10 ficheiros de teste — `TestSeguranca_SegredoNuncaObservavel` (invariante rainha), `TestExchange_Handle_NaoAdivinhavel`, `TestExchange_MediadaPeloRM_RegistadaSemValor`, TTL/revogação, `TestInjector_ImplementaPortaSBX`. Cenários adversariais em `security-tests/secrets_test.go`.
- **Costura em falta:** zero imports não-teste fora do módulo. Ponto exacto: `cmd/aos/modelgatewaywiring.go:71-83` — `staticModelCredential` lê o segredo de `AOS_MODEL_API_KEY_PATH` e o comentário admite «em produção o composition root liga aqui o vault/broker (EPIC-07)». O GW tem a sua própria porta com `ReferenceBroker` que falha `ErrNotWired`. O vault do broker é só in-memory («NUNCA usar em produção»). **Nenhuma env, nenhuma linha de banner.**
- **Risco:** a chave do provider LLM é um ficheiro estático lido em claro, sem TTL/revogação/rotação — o princípio 5 do AGENTS.md («segredos só via Broker/Vault, JIT, TTL curto») descreve a tese, **não o deployment**. As specs são honestas (checkboxes `[ ]` em EPIC-06); o gap não é assinalado em banner/tabela.
- **Fecho:** cliente Vault real atrás de `vault.Client` (**médio**); adaptar à porta `CredentialProvider` (**pequeno-médio**); wiring de injecção no executor (**médio**); env + banner (**pequeno**). Global: **grande**; deferimento coerente com a v1, mas devia ser declarado.

#### A4. Orquestrador (ORQ, AOS-012 + EPIC-18/19)

- **Propósito:** decomposição goal→DAG, publicação `run.created`/`task.ready`; sobre o esqueleto, toda a meta-orquestração (planner governado com NHI própria, validador puro, intake, dispatch/materialização/migração/replan, autonomia L0–L5, capability-gap).
- **Provado:** aciclicidade incremental fail-closed, Kahn determinístico, `RebuildDAG` idêntico, deadlock `abort_lowest_priority_victim`, delegação com reserva CAS + NHI filha, planner com mediação RM antes de decompor, suite adversarial (`planadversarial`).
- **Costura em falta:** **proibido por teste** (`boundary_orq_sch_test.go:26-29`, directo + transitivo). O nó corre `Runtime.Run(ctx, goal)` directamente (`service.go:552`) — um goal, um agente, N turnos. Nenhuma env; banner não anuncia orquestração (sem promessa falsa; ADR-018 §4 regista «a v1 corre a forma mínima, declarada — não fingida»).
- **Risco:** nenhum meta-objectivo multi-passo; decomposição, sub-agentes com orçamento hierárquico, pipeline plano→gate→materialização — tudo verde em teste, sem efeito.
- **Fecho:** para a v1 single-node, **nada** (é a Carta; fechar violaria o guarda). Consumo *dentro* de um run via colaborador dedicado (ADR-018 §2): **grande** (5+ portas, env nova, revisão do boundary test). Multi-nó: EPIC-10, sem horizonte datado.

#### A5. Escalonador (SCH, AOS-012, EPIC-03)

- **Propósito:** consumo de `task.ready`, dispatch sempre via RM; governação de carga completa — admission token-bucket (AOS-027), `max_spawn` por headroom (AOS-028), breaker de orçamento por árvore (AOS-029), filas+política hot-reload (AOS-030), degradação shed→defer→downgrade→reject (AOS-031), prioridade com aging (AOS-032), routing least-loaded (AOS-033), escala por SLIs (AOS-107).
- **Provado:** `TestSchedulerOnlyExecutesViaRM`, `TestAdmit_ConcurrentNoOversubscription` (`-race`), breaker com Rebuild por replay, `TestDispatch_ReplayByteForByte`, routing ≥90% contra round-robin, game days RB-01/RB-03.
- **Costura em falta:** mesmo guarda de fronteira. Nuance: `tieradapter` importa tipos do SCH em código produtivo, mas `NewProduction` do GW monta `failover.NewStage` **sem** `AdmissionCoordinator` nem tier ladder — nem via Model Gateway o admission chega ao deployment. As envs `AOS_BREAKER_*` são **outro mecanismo** (breaker do agente vivo, AOS-080/081). `scale.go:17-19` declara o link ao pool «diferido».
- **Risco:** sem TPM/RPM agregado, sem backpressure, sem degradação graciosa, sem prioridades. Ressalva honesta já documentada: a não-oversubscription só vale in-process com o `Store` de referência.
- **Fecho:** admission/budget como colaborador do run no composition-root (**médio**, exige emendar o boundary test de «módulo proibido» para «sem `Scheduler.Start`»); breaker por árvore (**médio**); filas/prioridade/routing/escala (**grande**, depende de EPIC-10).

#### A6. Attestation — as portas não-ligadas (AOS-177, ADR-016 §4)

- **O que ESTÁ ligado:** o verificador externo via `AOS_ATTESTATION_VERIFIER_URL` (fail-closed: não-https fora de loopback aborta; componente em baixo nega). Isolamento CBOR guardado por `dep_isolation_test.go` executável.
- **O que NÃO está:** `WithChallengeIssuance` (frescura por cerimónia) e `WithDeviceEnrollment` (atribuição dispositivo↔aprovador) — zero ocorrências em `cmd/aos`. Sem elas: replay de attestation capturada é possível; qualquer autenticador allowlisted serve qualquer aprovador. E o wiring inteiro está dentro de `if len(cfg.Approvers) > 0` — URL definida sem approvers é **ignorada em silêncio**; o banner não declara o estado da attestation (viola a disciplina AOS-203).
- **Fecho:** linha de banner (**pequeno**); wiring de `ChallengeIssuance` (**médio** — porta e registo já existem em `integration/challenge_issuance.go`); `AOS_DEVICE_ENROLLMENT_FILE` no padrão de `AOS_APPROVERS_FILE` (**pequeno-médio** — impl estática existe); empacotamento do componente para produção (**médio**, depende de trust store FIDO real).

---

### Grupo B aprofundado

#### B1. Execução durável (AOS-180/191/203)

- **Ligável por env, confirmado** — e o dev-hardened liga-a (`docker-compose.yml:48-50`). Fail-closed de config provado (`ErrDurableExecutionNeedsDurableSubstrate`).
- **Provado:** idempotência injectiva (incl. adversarial), dedup no commit, `TestResume_CrashPoints` (6 pontos), failover 3-réplicas, fencing anti-zombie (game day RB-02), `TestNode_DurableExecution_NoDoubleExecAfterRestart` (não-vacuoso).
- **Gap residual MESMO ligada:** o **`Resumer` nunca é composto** — checkpoints escritos, nunca lidos; crash-resume é manual (re-submeter o RunID) e o loop recomeça do turno 1 a re-interrogar o modelo (a reprodução de turnos só existe na retoma de suspensão AOS-021). Fencing opt-in não protege os caminhos internos (débito documentado em `durable/README.md:228-234`).
- **Risco:** desligada, um restart re-executa efeitos externos (ledger no-op não deduplica) e desactiva a cifra por-titular — declarado no banner, fail-closed em produção. Ligada, o risco é o operador assumir «retoma automática» que não existe.
- **Fecho:** crash-resume com `Resumer` + varrimento de runs órfãos no arranque do serviço (**médio→grande**); activação no manifesto (**pequeno**, decisão operacional).

#### B2. Observabilidade OTLP (AOS-173/076/085/086/210)

- **Ligável por env, confirmado** — dev-hardened corre com colector real; fail-open genuíno (colector em baixo nunca quebra o run); auth por ficheiro provada (mTLS cliente + bearer, nunca logado).
- **Provado:** `TestObservabilityEndToEndExportsWellFormedOTLPWithCost`, selo WORM↔trajectória no colector, `TestOTLPExporterDeterministicWireFormat`, `TestAOS210_DurableExecutionExportsActivitySpanAsParentOfExecuteTool`.
- **Gap residual:** **SLOs/alertas/dashboards sem produtor em runtime** — `EvaluateAlerts`/`BuildDashboard`/`EvaluateOperationalAlerts` só correm em testes; nada no nó constrói `WideEvent`s nem avalia; `platform/runbooks` mapeia alertas→runbooks, mas ninguém produz os alertas. Export só `/v1/traces` (sem métricas/logs). `otel-genai/doc.go` desactualizado («DIFERIDO» fechado por AOS-173).
- **Fecho:** loop avaliador periódico no nó (**médio** — funções puras já existem); `/v1/metrics` (**médio**); doc (**trivial**).

#### B3. Four-eyes (AOS-162/021/193)

- **Totalmente ligado por env** — e o dev-hardened **já o liga** (`docker-compose.yml:37`). Ciclo completo provado ao nível do nó: `TestApprovalCycleNode_DoisCiclosSemRepetirEfeitos`, `_SemAprovacaoARetomaNaoDestrava`; 501 declarado sem gate; em produção exige `AOS_DURABLE_EXECUTION=1`.
- **Risco de ficar desligado:** uma acção escalada fica suspensa até expirar (15 min) e é negada — nenhum humano a destrava. Honesto (banner + 501 + README).
- **Fecho:** montar `approvers.json` + env (**pequeno**, já demonstrado).

#### B4. Promotion controller (AOS-159/206)

- **Composto incondicionalmente** (AOS-206 fechou o «zero chamadores»); roster com validação fail-closed; `TestNodePromotionController_ReplayBlockedThroughNode` (anti-replay selado no WORM).
- **Gap:** **sem endpoint nem CLI** — ver correcção 2 acima. Secundário: o eval-gate de promoção usa `FailClosedGate{MinScore: 0.8}` fixo; `Config.PromotionEval` sem costura env.
- **Fecho:** `POST /promote` (molde de `/approve`) + pipeline AOS-096 de candidatos (**médio**; sem AOS-096 o endpoint seria inútil — dependência real).

#### B5. Retenção TTL (AOS-092/213)

- **Provado:** `TestNode_AOS213_ExpirationRealErasure` (crypto-shred real), `_HeldSkippedByExpirationThenReleased` (legal hold respeitado), anti-concorrência (409), verificação pós-shred da hash-chain.
- **Gap:** (a) sem as duas env, o job varre sem apagar (banner declara); (b) **não há scheduler interno** — só corre sob `POST /dsar/expire`, que exige credencial forte de governação. Mesmo no dev-hardened (que define a política), nada corre sem cron externo autenticado.
- **Risco:** *storage limitation* (RGPD) silenciosamente não cumprida se o operador assumir «a retenção existe».
- **Fecho:** cron externo autenticado (**pequeno**) ou ticker interno no loop de serviço (**pequeno-médio**, exige decisão sobre credencial em nome próprio).

#### B6. Custódia externa da KEK (AOS-215/216)

- **Totalmente ligável por env** — adaptador Transit *key-never-leaves* completo, readiness em `/readyz`, guarda de produção `ErrProductionNeedsDurableKEK`. Stack OIDC liga-o.
- **Risco do default:** modo referência + substrato durável + KEK em memória ⇒ restart torna o conteúdo selado **permanentemente indecifrável** (over-erasure silenciosa). Declarado, fail-closed em produção.
- **Fecho:** config/infra (**pequeno**); dependência operacional: unseal do Vault.

#### B7. Breaker do agente vivo (AOS-080/081) — os dois achados

Ver correcção 3 acima. **Fecho:** (a) chamar `observeAction` no fecho do `execute_tool` — o hash canónico já existe (`agentruntime.AttrToolCallHash`) (**pequeno-médio**); (b) cablar `VelocitySource` real **ou** abortar o boot quando velocity>0 sem fonte — nunca engolir o erro (**médio**); (c) teste de ápice que dispara o breaker pelo caminho do nó; (d) corrigir o README (**pequeno**). **Tratar 5a como defeito fail-open, não como «desligado por escolha».**

#### B8. mTLS do plano de controlo (DEF-012, EIXO 1)

- **Ligável por env, provado** (`TestControlMTLSEndToEnd`, `...NotBypassOfEd25519` — é aditivo, nunca bypass).
- **Gap estrutural:** **incompatível com a topologia dev-hardened** — essa stack usa `AOS_TLS_EXTERNAL_TERMINATION=1`, e o mTLS exige terminação TLS **no nó** (`ErrControlMTLSNeedsNodeTLS`). Na topologia de referência (edge termina TLS) é inalcançável sem mudar a terminação; repasse de identidade do edge não é suportado (deliberadamente recusado).
- **Fecho:** decisão de topologia + PKI de cliente (**pequeno-médio**, infra, não código).

---

### Grupo C aprofundado — signer de checkpoints / truncatura do tail (AOS-221/072)

- **O que a verificação detecta (provado):** mutação de conteúdo, remoção interna (gap de seq), inserção/reordenação — inclusive com CRC recalculado pelo atacante (`TestWORM_LoadRejectsTamperedChain_CRCValid`, `TestNode_RestartRejectsTamperedWORM`, verificação pós-shred nos dois caminhos DSAR).
- **O ataque aberto:** **truncatura do tail** — apagar os N registos *mais recentes* deixa uma cadeia-prefixo internamente consistente que verifica verde no arranque (`verify.go:16-23` documenta-o). Concreto: apagar os `deny` mais recentes do PDP, acções de admin, selos DSAR/legal-hold, reiniciar — e o nó arranca limpo. Também aberta: reescrita total desde a génese (F1 de AOS-072, limitação inerente sem âncora).
- **Estado:** o `Signer`/`VerifyFromCheckpointAtHead` existem e estão testados na biblioteca; único consumidor real é `platform/dr/recover.go`. **Nenhum ticket/DEF possui o wiring no nó** — o residual vive só na spec EPIC-18 e no banner (declarado honestamente, sem promessa falsa).
- **Fecho:** (1) verificação ancorada no nó — env no molde `AOS_POLICY_TRUST_ANCHOR` + `VerifyFromCheckpointAtHead` com `expectedHead` persistido (**médio**); (2) selagem periódica out-of-process com chave sob custódia KMS/HSM (**médio-grande**, maioritariamente infra-org — o mesmo bloqueio D4/AOS-156 já escalado ao dono).

---

### Achados novos desta varredura (não estavam no inventário)

| # | Achado | Severidade |
|---|---|---|
| N1 | Breaker: ligar velocidades **desliga o disjuntor inteiro em silêncio** (`ErrVelocitySourceMissing` engolido) | **Alta — fail-open** |
| N2 | Breaker: no-progress nunca dispara (`observeAction` é código morto); só wall-clock protege | Média — promessa falsa parcial |
| N3 | Promotion controller sem endpoint/CLI mesmo com ratificadores | Média — capacidade sem superfície |
| N4 | SLOs/alertas/dashboards sem produtor em runtime | Média — sinal prometido que não existe |
| N5 | `Resumer` nunca composto: checkpoints escritos, nunca lidos (crash-resume manual) | Média — expectativa «durável» > realidade |
| N6 | Attestation: `ChallengeIssuance` + `DeviceEnrollment` sem wiring (replay/atribuição abertos) | Média — garantia aquém de ADR-016 §4 |
| N7 | Banner mudo sobre budget/broker/attestation — diverge da disciplina AOS-203 | Baixa — honestidade operacional |
