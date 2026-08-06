# Prontidão do AOS para modelos agênticos

> **Data:** 2026-08-05 · **Revisto:** 2026-08-06 (ver [Revisão](#revisão-2026-08-06)) · **Natureza:** avaliação baseada em evidência viva (execução dos artefactos publicados e dos testes de aceitação nesta data), não apenas em documentação.
> **Âmbito:** imagem `aos-node:local` (17 MB, digest local `1c5e69cf488e` — hoje `bb0a8c00c4bf`), stack endurecida `aos-dev-hardened` (OIDC+Vault+LiteLLM), e testes de mediação dos módulos `kernel/reference-monitor`, `cmd/aos` e `integration`.
>
> ⚠️ **Este relatório foi REVISTO em 2026-08-06** por um painel adversarial de 5 lentes (verificação contra o HEAD). **Dois dos cinco gaps — incluindo o bloqueador nº1 — já estão fechados.** As secções afectadas estão marcadas em linha; o apuramento completo está em [Revisão](#revisão-2026-08-06).

## Veredicto

**Arquitecturalmente sim — e funcionalmente sim para efeitos locais governados.**

> **REVISTO (2026-08-06).** O veredicto original era «funcionalmente ainda não»: nenhuma tool call chegava a executar por omissão e o loop esgotava os turnos. **Isso deixou de valer**: o commit `303cf47` fez a autoridade de escopo derivar do token NHI verificado, e uma tool call legítima (`cap:fs.read`) **executa hoje** — provado em CI (`TestScopeTokenOnly_SemDirectorioExterno_ToolExecuta`, com o directório de autoridade VAZIO) e ao vivo (`run-fclive-2` → microVM Firecracker real). O que resta é o **feedback de negação** e o **circuito de aprovação humana**, não a execução.

| Cenário | Pronto? |
|---|---|
| Correr modelos agênticos **sem que possam fazer mal** (sandbox governada, read-only, auditoria total) | **Sim** — é o que a stack hardened faz hoje |
| Agentes com **efeitos locais reais** (ler/escrever em sandbox) sob governo | **Sim** *(revisto)* — autoridade derivada do token + `MediatedLauncher` em microVM; `cap:fs.read` executa ponta a ponta |
| Agentes com **efeitos de REDE externa** (postar, chamar APIs) originados pelo modelo | **Não — por desenho.** `cap:http.post` é negada pelo taint-gate («untrusted não comanda», P4). Não é um gap de prontidão: é a propriedade a funcionar. Destravar exige o circuito de aprovação humana (gap 3) |
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
2. **Feedback de negação para o loop.** ⚠️ **AINDA VÁLIDO — e mais grave do que aqui se dizia.** Não é «fraco»: é **descartado**. `mediateToolCall` (`loop.go:556`) devolve apenas `Untrusted(dec.Output)` + `dec.ToolErr`; num deny **ambos são vazios/nil**, pelo que o tail materializa corpo **vazio** — o modelo não distingue «negado» de «a tool não devolveu nada». Isto explica o Kimi repetir a mesma estratégia 16×. Os dados já existem (`Effect/Code/Reason/DeniedBy`, `decision.go:61-72`) e o padrão já vive noutro caminho: é **encanamento**, não construção. **Remédio (2 eixos):** (a) marcador estruturado no tail; (b) terminação por orçamento de negações — para a qual existe um **circuit breaker pronto e não-cablado** (`breaker/breaker.go`, `MaxStaleIterations` + `actiondedup`): `Observe` nunca é chamado e não há sequer uma `Option` no Runtime. **Cablar, não reescrever.**
3. **Circuito negação→aprovação humana.** ⚠️ **PARCIALMENTE VÁLIDO — a afirmação original misturava três coisas.**
   - ✅ **Válido:** não existe bridge `deny → aprovação → reexecução` de uma tool call. `FourEyes` é um endpoint `/approve` **autónomo** para acções irreversíveis (`api.go:985-1068`), **não** um hook da cadeia de mediação (`secured.go:270-278` não o tem); nenhum código apanha `Decision.DeniedBy` e escala. **É o gap real.**
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
2. **Feedback de negação estruturado** devolvido ao modelo (encanar `Decision` até ao tail) **+ terminação por orçamento de negações** (cablar o `breaker` existente). *O maior item que resta; ambos com primitivos prontos.*
3. **Circuito negação→four-eyes→execução aprovada** ligado ao loop — sem criar bypass da mediação (a reexecução tem de voltar a atravessar o RM com autorização trusted).
4. ~~Republicar a imagem com os labels OCI/ADR-017~~ ✅ **FEITO**; resta purgar o *dangling* e fixar a republicação no CI.
5. *(novo)* **Provisionar uma `AuthoritySource` externa** para restaurar a defesa-em-profundidade do AOS-071 e habilitar **revogação/RBAC organizacional** (restringe, nunca amplia).

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
