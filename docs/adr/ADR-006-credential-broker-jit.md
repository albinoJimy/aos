# ADR-006 — Credential Broker com tokens JIT

| Campo | Valor |
|---|---|
| **ADR** | 006 |
| **Título** | Credential Broker com tokens JIT |
| **Estado** | Aceite |
| **Data** | Julho 2026 |
| **Deciders** | Equipa AOS |
| **Contexto-fonte** | Catálogo de ADRs em `_BRIEF.md` (linha 75) e `specs/00_System_Spec.md` (linha 249); `_FONTE_agentic-os-ideal.md` (§ Credential broker); `specs/EPIC-07_Seguranca_Isolamento.md` |

## Contexto

Um agente autónomo precisa, para agir a jusante (chamar modelos, APIs, provedores de
infra), de credenciais downstream. O anti-padrão clássico é entregar esse segredo ao
agente — em variáveis de ambiente, no prompt, num ficheiro de configuração ou como
retorno de uma tool. Num sistema onde parte do contexto do agente é *untrusted*
(resultados de tools, conteúdo web, memória, schemas MCP — ADR-005) e onde o agente
pode auto-modificar-se, qualquer segredo que o agente **veja** é um segredo
exfiltrável: entra em logs, em spans de observabilidade, em checkpoints de replay, ou
é directamente extraído por *prompt injection* (OWASP LLM01).

O enunciado canónico do catálogo é explícito: **«Segredos no vault; o broker injecta
credenciais downstream server-side, TTL curto, revogáveis»** — e a invariante
transversal registada em `_BRIEF.md` (linha 98) e `specs/00_System_Spec.md` (linha
181): **«o agente nunca vê o segredo downstream»**.

Existe ainda uma segunda fronteira que depende desta decisão: a superfície humana
(EPIC-13/BFF). Um BFF que assine *em nome do humano* com uma chave server-side
destruiria o modelo de não-repúdio. `specs/EPIC-13_Frontend.md` (linha 38) fixa que
«a chave privada nunca vive no browser — ADR-006». É preciso separar com clareza
**duas custódias distintas** que este ADR delimita: (i) a custódia da credencial
*downstream* da identidade não-humana (NHI) do agente — governada por este broker; e
(ii) a custódia da chave de *aprovação humana* — que este broker está **proibido** de
produzir (ver secção Consequências e o ADR de governança de UI, AOS-129).

## Decisão

Adopta-se um **Credential Broker + Vault (BRK)** server-side com emissão de tokens
**just-in-time (JIT)**. As invariantes ratificadas são:

1. **Segredos no vault, nunca no agente.** O material bruto de infra vive no cofre
   (Vault). O agente possui apenas um token *scoped/time-bound* (identidade NHI,
   ADR-003) que troca junto do broker; **nunca** recebe o segredo downstream. O
   segredo é resolvido, trocado e usado inteiramente do lado servidor.

2. **JIT com TTL curto.** A credencial é obtida no momento em que é precisa (não
   pré-provisionada), guardada num cache de vida curta, **renovada antes de expirar**
   e limitada pela expiração real (o menor de TTL-de-cache, expiração do lease e
   expiração do token do provedor). Sem cofre/lease válido, nada é servido.

3. **Revogável e rotacionável.** Cada emissão é um **lease** identificado e revogável.
   A rotação do segredo corrente não interrompe chamadas em curso (a chamada
   *in-flight* completa com a cópia que já recebeu); as chamadas seguintes passam a
   ver o novo material por troca atómica da referência em cache.

4. **Fail-closed e atribuível.** Ausência de material, região não configurada, token
   vazio ou broker não-ligado resultam sempre em **falha atribuível** (identifica
   *provider* + região) — **nunca** num *fallback* silencioso para outra conta ou
   região. A fronteira de soberania (ADR-011) é respeitada no próprio ponto de
   aquisição.

5. **Redação imposta pelo tipo.** O segredo é um campo **não-exportado**; os tipos que
   o transportam redigem-no em `String()` **e** em `MarshalJSON()`, de modo que um log
   ou span acidental — texto ou JSON — nunca o revela. A garantia é estrutural, não
   depende de disciplina do chamador.

6. **A superfície nunca é autora do enforcement.** O broker JIT server-side
   (`messaging.Signer`/broker de credenciais) serve **exclusivamente** as credenciais
   downstream da NHI do agente. É **proibido** usá-lo para produzir a assinatura de
   decisão/ratificação humana — essa chave é uma credencial WebAuthn/FIDO2
   *non-extractable* no dispositivo do humano, nunca no Vault, nunca no BFF, nunca no
   browser (ver AOS-129, ADR de governança de UI, e `specs/EPIC-13_Frontend.md`
   AOS-132: o BFF é *non-signing*).

## Consequências

### Positivas

- **O agente deixa de ser um vetor de exfiltração de segredos**: nada que ele veja,
  logue ou serialize contém material downstream; *prompt injection* não alcança o
  cofre.
- **Superfície de auditoria/observabilidade limpa por construção**: como a redação é
  imposta pelo tipo, a trajectória OTel (ADR-010) e o audit WORM podem registar
  *leases* e correlacionar rotação/revogação sem tocar no segredo.
- **Contenção de fuga temporal**: TTL curto + revogação central reduzem a janela de
  exploração de uma credencial comprometida a minutos.
- **Soberania preservada na aquisição**: a recusa atribuível por região fecha o buraco
  de servir a chave de outra região por defeito.
- **Frontend seguro por desenho**: a separação das duas custódias garante que a chave
  de aprovação humana nunca alcança o servidor/browser — WYSIWYS e 4-eyes sobrevivem
  até ao *secure element*.

### Negativas / Trade-offs

- **Dependência de disponibilidade do broker/vault**: sendo fail-closed, uma
  indisponibilidade do cofre **pára** o trabalho a jusante (por desenho — nunca
  degrada para material inseguro). Exige operar o Vault com a robustez adequada.
- **Custódia por-ambiente não-trivial**: o modo dev usa root token in-memory
  descartável; o modo server (staging/produção) **arranca selado** e exige
  `vault operator init/unseal` fora-de-banda — a IaC não pode automatizar o unseal sem
  reintroduzir segredos, logo há um passo operacional manual assumido.
- **Wiring de produção ainda inerte**: o broker de **referência** é fail-closed
  (`ErrNotWired`) até ser ligado a um Vault real; a materialização do injector
  server-side na sandbox (ADR-004) é `CredentialInjector` opcional — a fronteira está
  provada, o wiring concreto pertence a EPIC-07/PR-0 e não deve ser dado como pronto.
- **TLS desativado no listener do módulo IaC** por simplicidade de ambiente — produção
  real exige TLS; está documentado como dívida explícita no módulo.

## Alternativas consideradas

- **Segredo entregue ao agente (env/prompt/ficheiro)** — Rejeitada. Torna cada log,
  span, checkpoint e resultado de tool um canal de exfiltração; incompatível com
  ADR-005 (contexto untrusted) e com auto-modificação.
- **Credenciais estáticas de longa duração pré-provisionadas** — Rejeitada. Sem TTL
  curto nem revogação prática; a janela de comprometimento é permanente e a rotação é
  disruptiva.
- **Segredos colocados pela IaC no cofre (var-files/state)** — Rejeitada. Colocaria
  material secreto no repositório/estado do Terraform; viola o DoD «sem segredos». A
  IaC provisiona apenas o **cofre** (topologia), nunca o conteúdo.
- **Broker a assinar também a decisão humana (chave server-side reutilizada)** —
  Rejeitada. Fundiria as duas custódias e destruiria o não-repúdio da aprovação
  humana; a chave de decisão humana fica em WebAuthn *non-extractable* (AOS-129).
- **Unseal automático do Vault via IaC** — Rejeitada para o modo server: reintroduziria
  material secreto no automatismo; o unseal permanece operação fora-de-banda.

## Conformidade / Enforcement

Onde a decisão é imposta no código e na infra:

- **Porta do broker + emissão JIT, TTL, rotação, revogação, fail-closed atribuível:**
  `packages/platform/model-gateway/internal/credentials/broker.go` (`CredentialBroker`,
  `Lease`, `ReferenceBroker` fail-closed com `ErrNotWired`, `ErrNoMaterial`).
- **Cache JIT com refresh-antes-de-expirar, TTL limitado pela expiração real, rotação
  sem corte, revogação e recusa por região:**
  `packages/platform/model-gateway/internal/credentials/source.go` (`Source.Fetch`,
  `Source.Revoke`, `ErrRegionNotConfigured`, `ErrEmptyToken`, `CredentialError`).
- **Troca OAuth server-side (o agente nunca participa no fluxo) e redação imposta pelo
  tipo em texto e JSON:**
  `packages/platform/model-gateway/internal/adapters/oauth/oauth.go` (pacote sob
  `internal/`, `Material`/`Token` com `String`/`MarshalJSON` redigidos).
- **Injeção server-side na sandbox — «handle, nunca segredo»:**
  `packages/substrate/sandbox/credentials.go` (`CredentialInjector.Inject`: o segredo
  nunca regressa ao chamador, nem entra em `ExecResult`, eventos ou spans).
- **Provisão do cofre (topologia, sem segredos): dev in-memory com root token gerado em
  runtime; staging server persistente que arranca selado:**
  `infra/modules/secrets/main.tf`, `infra/modules/secrets/outputs.tf`
  (`dev_root_token` como output `sensitive`, vazio em staging),
  `infra/modules/secrets/README.md` («Sem segredos no var-file»).
- **Fronteira *non-signing* para a superfície humana (o BFF nunca assina em nome do
  humano; a chave nunca vive no browser):** `specs/EPIC-13_Frontend.md` (AOS-132) e o
  ADR de governança de UI (AOS-129), que selam a separação das duas custódias.
- **DoD de handoff:** `specs/01_Engineering_Standards_e_Handoff.md` («Sem segredos —
  credenciais downstream via Credential Broker/Vault com tokens JIT; scan de segredos
  limpo»).

## Referências

- Catálogo de ADRs: `_BRIEF.md` (linhas 75, 98, 165); `specs/00_System_Spec.md`
  (linhas 162, 181, 249, 313).
- Fonte de visão: `_FONTE_agentic-os-ideal.md` (Credential broker; Fase 1 — fronteira
  de segurança).
- ADRs relacionados: **ADR-003** (identidade NHI / cadeia de delegação que termina num
  humano — a autoridade que o token *scoped* codifica); **ADR-004** (isolamento ao
  nível do kernel — onde a credencial é injectada); **ADR-005** (separação
  control/data-plane e taint — porque o segredo não pode tocar o agente); **ADR-011**
  (soberania por board — a recusa por região no ponto de aquisição); **ADR-010**
  (observabilidade/audit — redação e correlação de leases).
- Epics: `specs/EPIC-07_Seguranca_Isolamento.md` (broker/vault real, AOS-070);
  `specs/EPIC-05_Registry_Supply_Chain.md` (os scopes do `contract` como único
  conjunto que o broker concede); `specs/EPIC-13_Frontend.md` (fronteira *non-signing*
  do BFF).
