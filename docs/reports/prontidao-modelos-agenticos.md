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
