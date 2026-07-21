# ADR-011 — Policy-as-code + GDPR por desenho

| Campo | Valor |
|---|---|
| **ADR** | 011 |
| **Título** | Policy-as-code + GDPR por desenho (PDP/PEP Cedar versionado e assinado; minimização, TTL, redação de PII, crypto-shredding; soberania por board) |
| **Estado** | Aceite |
| **Data** | Julho 2026 |
| **Deciders** | Equipa AOS |
| **Contexto-fonte** | Catálogo de ADRs — `_BRIEF.md` (linha ADR-011) e `specs/00_System_Spec.md` (§ catálogo de decisões); enunciado técnico `tecnica/12 §4/§9` (contrato C1 RM↔PDP), `tecnica/09 §6` (soberania). Implementado em EPIC-09 (governança) e AOS-091/093/094/097. Extensão ao read-path exigida pela EPIC-13 (superfície frontend). |

---

## Contexto

O AOS media **toda** a acção de agente através do Reference Monitor (ADR-002): nenhum caminho de código chama tools directamente. A autorização dessa mediação não pode ser código imperativo disperso — tem de ser **política declarativa, versionada, assinada e verificável**, decidida num ponto único (PDP, *Policy Decision Point*) e imposta noutro (PEP, *Policy Enforcement Point*, dentro do RM). Ao mesmo tempo, o sistema processa dados que podem ser pessoais e está sujeito ao RGPD por desenho: minimização, limitação de conservação (TTL), redação de PII e o direito ao apagamento (Art. 17) têm de ser **primitivos estruturais**, não anexos. Finalmente, a residência de dados é regulada **por board**: cada fronteira organizacional tem uma região de soberania onde os seus dados podem residir, e nenhum efeito — nem um failover, nem uma leitura de UI — pode exportar dados cross-border.

O enunciado canónico do catálogo (ADR-011) é:

> **PDP/PEP com Cedar versionado e assinado; minimização, TTL, redação de PII e crypto-shredding (Art. 17); soberania por board.**

Este ADR documenta essa decisão **já tomada e implementada**. O catálogo lista "Rego/OPA **ou** Cedar"; a implementação de referência **fixou Cedar** (via `cedar-go`, a única dependência externa permitida além da stdlib — coerente com a filosofia reference-model, build offline/determinista). A semântica é idêntica à do golden Rego de `tecnica/12 §9` (default-deny + obrigações derivadas), mas expressa em Cedar.

Uma decisão adjacente que a EPIC-13 (camada frontend) força a explicitar: **o PEP governa efeitos, mas o read-path de uma superfície de leitura também tem de respeitar a fronteira regional**. Um BFF que sirva estado/custo/trajectória de um run fora da sua região consumaria a exportação cross-border na própria terminação de leitura — logo a mesma autoridade de soberania (`sovereignty.Registry`) que alimenta o PEP tem de governar também o read-path. Esta extensão é ratificada aqui como invariante (ver ## Decisão, ponto 6, e ## Consequências).

---

## Decisão

### 1. PDP determinista, fail-closed, com política Cedar compilada em memória

O PDP (`packages/control-plane/pdp/pdp.go`) avalia a política de referência compilada e devolve decisões **puras e sem efeitos** (contrato C1: a mesma `(Input, policy_version)` produz sempre a mesma `Decision`). É **fail-closed em todos os pontos**:

- Sem política carregada (`engine == nil`) → `Deny` com `ErrPolicyUnavailable`. `NewUnloaded()` devolve exactamente esse estado seguro por omissão.
- Request malformado (ex.: `Capability` vazia) → `Deny` com `ErrMalformedRequest`.
- Qualquer erro de avaliação → `Deny`. **Nunca** devolve ausência de resposta: `Decide` retorna sempre uma `Decision` (em negação, uma `Decision{Effect: Deny}`).

A política Cedar (`packages/control-plane/pdp/policies/aos_authz.cedar`) é **default-deny**: sem um `permit` explícito aplicável, a decisão é `deny`. Cada `permit` é o equivalente exacto de uma regra `allow if { ... }` do golden Rego de `§9`. O mapeamento de entidades é: `principal.authority : Set<String>` (capabilities delegadas), `action` = a capability pedida (ex.: `Action::"cap:http.post"`), `resource.region : String`, `context.{taint, sensitivity}`.

### 2. Política **versionada e assinada** (integridade + autenticidade)

O bundle de política (`packages/control-plane/pdp/bundle.go`) é assinado com **ed25519** contra um *trust anchor*. A mensagem assinada (`signingMessage`) liga o domínio (`aos.policy.bundle.v1`), a `PolicyVersion` (SemVer) **e** o `ContentHash` canónico dos ficheiros `.cedar` — adulterar qualquer um invalida a verificação. `RawBundle.Verify` é fail-closed: `content_hash` recomputado ≠ declarado → `ErrSignatureInvalid`; assinatura que não verifica → `ErrSignatureInvalid`. O hash canónico (`canonicalContentHash`) ordena os ficheiros por nome e mistura `nome\0 sha256(conteúdo)\0`, pelo que **renomear** um ficheiro também invalida a assinatura.

O *trust anchor* deve ser provisionado **out-of-band** via `WithTrustAnchor` (fonte confiável: VCS/deploy, read-only) — nunca lido do mesmo directório mutável do bundle hot-reloaded (o `Open` documenta este trust model e o vector de cold-start que ele fecha).

O hot-reload (`PDP.Reload`) é **monotónico e atómico**: só troca o motor se a nova `PolicyVersion` for estritamente mais recente (SemVer), rejeitando versões não-crescentes com uma sentinela dedicada (`ErrStalePolicyVersion`, distinta de falha criptográfica, para nunca **regredir** política em vigor). A verificação de monotonia e a troca ocorrem sob o mesmo write-lock (sem TOCTOU). Cada reload bem-sucedido emite um `PolicyChangeEvent` (`OldVersion`, `NewVersion`, `ContentHash`, `At`) via callback `WithReloadAudit`, para o RM gravar um registo de audit de **primeira classe** da transição — sem acoplar o PDP ao Event Store. Isto é o elo com ADR-010 (audit WORM) e ADR-012 (auto-modificação ratificada).

### 3. PEP no RM impõe as obrigações **antes** do efeito (fail-closed)

O PDP devolve `Obligations` num permit; o PEP (`packages/kernel/reference-monitor/obligations.go`, `enforceObligations`) cumpre-as **antes de o efeito ser libertado**, dirigido por `Obligation.Type`:

- `audit` — satisfeita pelo audit-before-effect do RM (`RecordMediation` grava a mediação, incluindo as obrigações, ANTES do dispatch).
- `redact_pii` — o PEP redige os campos nomeados nos args (`enforceRedactPII`) **antes** do dispatch, para o efeito nunca ver PII em claro. Fail-closed: `redact_pii` sem campos, ou `Input` não-JSON e não-vazio (blob opaco onde não se garante localizar a PII) → **deny**.
- `region` — soberania de dados (ver ponto 5).
- `ttl` — propagada na `Decision.Obligations` ao consumidor a impor (limitação de conservação).
- **Qualquer tipo desconhecido → deny fail-closed** (uma obrigação que o PEP não sabe cumprir não liberta o efeito).

As obrigações são derivadas deterministicamente da decisão (`packages/control-plane/pdp/engine_cedar.go`, `obligationsFor`): `audit(level=full)` **sempre** que a decisão é permit; `redact_pii(email, phone)` quando `context.sensitivity == "confidential"`. O adaptador RM↔PDP (`packages/control-plane/pdp/rmadapter.go`, `PolicyCheck`) traduz `Call↔Input` e `Decision↔HookResult`.

### 4. GDPR por desenho: minimização, redação de PII e **crypto-shredding (Art. 17)**

O motor de redação (`packages/substrate/redaction/redact.go`, `Engine.Redact`) é puro e recursivo sobre payloads JSON-shaped. A política de tratamento (`packages/substrate/redaction/policy.go`, `Policy`) decide **por classe** entre duas acções:

- **`ActionRemove` (minimização)** — a ocorrência é substituída por `[REDACTED:<classe>]` e o valor original é **descartado, irrecuperável por desenho** (não revela comprimento nem fragmento).
- **`ActionTokenize`** — substituição por um token reversível `tok:<titular>:<classe>:<opaco>`, ancorado numa **chave por-titular** (`packages/substrate/redaction/token.go`): o `<opaco>` é AES-256-GCM sob a chave do titular, com AAD ligando `(titular, classe)`. Reversível **só** com essa chave.

A `Policy` é **fail-closed**: uma classe conhecida sem decisão explícita faz `NewPolicy` falhar (`ErrPolicyIncomplete`); uma classe imprevista em runtime é tratada como `ActionRemove` (a decisão mais conservadora). A tokenização sem `KeySource` falha-fecha (`ErrTokenizeNoKeys`).

O **crypto-shredding (Art. 17)** materializa-se na porta `KeySource` (`packages/substrate/redaction/keysource.go`): destruir a chave por-titular torna **todos** os tokens desse titular irresolúveis, sem tocar no motor nem nos dados selados. A impl de referência `InMemoryKeySource.Shred(subject)` remove a chave (idempotente); em produção liga-se o KeyVault do audit (AOS-083) ou um KMS/HSM por trás da mesma porta. A `KeyRef` é derivável do titular (`KeyRefFor`, prefixo `aos.redaction.pii:`), pelo que o shredder localiza a chave a destruir sem indexação lateral.

O direito ao apagamento é orquestrado pelo fluxo DSAR (`packages/control-plane/governance/dsar/flow.go`, `Flow.Receive`): destrói a(s) chave(s) do titular sobre os stores shreddable e **sela os eventos na hash-chain do audit** (`dsar.received`, `dsar.key_destroyed`, `dsar.blocked`) — o apagamento é ele próprio auditável e verificável. O `Request` **nunca** carrega valor pessoal: o `SubjectID` é o identificador pseudónimo do titular (o mesmo que ancora a chave), nunca o dado. O `Result.Partial` sinaliza honestamente uma erasure parcial e irreversível (janela TOCTOU de um legal hold colocado a meio) — não se finge atomicidade que a fronteira não garante.

A auditabilidade cruza-se com o crypto-shredding sem quebrar a hash-chain: o `AuditRecord` (`packages/platform/audit/record.go`) grava um `PayloadRef{ContentHash, KeyRef, SubjectID}` — **nunca o payload in-line**; o `EntryHash` sela o `ContentHash` (hash do ciphertext), não o plaintext, pelo que destruir a `KeyRef` torna o payload ilegível **sem** invalidar a cadeia (ADR-010).

### 5. Soberania **por board**, fail-closed

A residência de dados é imposta por board. A cadeia é PDP→PEP:

1. O escopo de identidade da NHI codifica o board (ADR-003 / EPIC-01).
2. O `sovereignty.Registry` (`packages/control-plane/governance/sovereignty/registry.go`) resolve **board→região autorizada**. É a **autoridade** do primeiro elo, **imutável** após construção (cópia defensiva) e segura para concorrência (só leitura). É **fail-closed**: um board vazio, não registado, ou com região vazia **nunca** resolve para uma região autorizada (`RegionFor`/`Authorized` devolvem `false`) — "região desconhecida ⇒ deny", nunca "qualquer região por omissão". Um `Registry` nil é válido e resolve tudo para não-autorizado. A comparação é normalizada (case-insensitive), coerente com o PEP.
3. O PDP anexa a região resolvida como **obrigação `region`** a um permit.
4. O PEP (`enforceRegion`) **recusa fail-closed** qualquer efeito cujo recurso-alvo esteja fora dessa região — incluindo roteamento/failover cross-border. Fail-closed em três frentes: obrigação de região sem região exigida, recurso sem região resolvida, ou região diferente da exigida → **deny** antes do dispatch.

A política Cedar reforça a fronteira na própria regra (ex.: `allow_http_post` exige `resource.region == "eu"`).

### 6. Extensão ratificada: o **read-path da UI** é soberano e fail-closed (EPIC-13)

O PEP governa **efeitos**; o read-path de uma superfície de leitura (BFF: estado, custo, trajectória) **não** passa pelo PEP de efeitos — logo, sem esta invariante, seria um buraco de exportação cross-border. Ratifica-se, como parte de ADR-011:

- **Cada endpoint de leitura do BFF resolve o board→região do run via a MESMA autoridade `sovereignty.Registry.Authorized`** que o PEP usa, e **recusa servir fora da região** (fail-closed, idêntica postura "região desconhecida ⇒ deny"). A superfície **apresenta**, nunca é autora do enforcement — reutiliza a autoridade imutável, não a reimplementa.
- **Topologia:** para multi-região, **BFF por-região** (os dados nunca deixam a região). Um **BFF global agregador é proibido** por consumar a exportação cross-border na própria terminação de leitura.
- **Auditoria de leitura sensível, producer-bound:** selar no WORM **apenas** leituras sensíveis, com o mesmo gatilho que o audit já usa em `record.go` — `Context.Taint == "untrusted"`, `Context.Sensitivity == "confidential"`, presença de `PayloadRef` (PII/`SubjectID`), ou leitura de um subject com DSAR aberto. Estes registos vão para uma **partição de audit dedicada** (`read:<run|board>`, via o campo `Partition` já existente), separada da cadeia de decisões e da telemetria de alta-frequência (que **nunca** entra na hash-chain). Cada registo carrega `PayloadRef` para ser crypto-shreddable (Art. 17) sem quebrar a cadeia, e é projectado query-time pelo `compliance/report.go` existente.

> **Condicional (informação humana ausente).** A **regra** arquitectural acima é fixada já (a mecânica está no código). Fica condicional a **topologia operacional**: o número de regiões/boards reais (o repo tem zero implantações/boards/NHIs; num deployment single-region "por-região vs global" é discutível na prática) e a **federação de identidade de operador cross-região**, que é irresolúvel até se escolher a autoridade de identidade que faz o binding humano↔NHI (ADR-003 / AOS-005 `IdentityStub`, ainda diferido). Não se region-pina um operador que não se consegue identificar. Fixa-se a regra; não se congela o número de BFFs regionais.

---

## Consequências

### Positivas

- **Autorização auditável e não-ambígua.** A política é declarativa, default-deny, versionada (SemVer) e assinada (ed25519); nenhuma decisão de autorização vive em código imperativo disperso. Cada decisão carrega a `PolicyVersion` que a produziu, gravada no audit (rastreabilidade regulatória).
- **Fail-closed em profundidade.** PDP sem política, request malformado, obrigação desconhecida, board/região desconhecidos, ausência de trust anchor — todos resolvem para deny. A ausência de decisão explícita é sempre recusa.
- **Integridade da política verificável.** Adulterar conteúdo, versão ou nome de ficheiro invalida a assinatura; o hot-reload nunca regride versão (monotonia atómica).
- **RGPD estrutural, não anexo.** Minimização (remove irrecuperável), tokenização por-titular, TTL propagado, e crypto-shredding que satisfaz o Art. 17 **sem** quebrar a hash-chain do audit (o `EntryHash` sela o `ContentHash`, não o plaintext). O DSAR é ele próprio auditável.
- **Soberania imposta na mesma autoridade em todos os caminhos.** Efeitos (PEP) e leitura de UI (read-path BFF) resolvem board→região pela **mesma** `sovereignty.Registry` imutável; o failover fica preso à fronteira; o BFF global agregador é proibido.
- **Separação de responsabilidades limpa.** PDP decide (puro), PEP impõe (efeitos), redaction trata (dados), sovereignty resolve (fronteira), audit sela (prova). O PDP não depende do Event Store; a superfície não é autora do enforcement.

### Negativas / Trade-offs

- **Dependência externa `cedar-go`.** Fixar Cedar introduz a única dependência não-stdlib permitida no PDP; é uma superfície de supply-chain a manter pinada e coberta pelos gates SCA/SAST. (O catálogo admitia Rego/OPA como alternativa — ver ## Alternativas.)
- **Custódia do trust anchor é operacional.** A garantia de assinatura só é tão forte quanto o provisionamento out-of-band do anchor. Um anchor lido de directório mutável reabre o vector de cold-start; a segurança depende de disciplina de deploy (read-only).
- **Redação depende da forma do payload.** A garantia "o efeito não vê PII em claro" exige `Input` JSON-shaped ou vazio; um blob opaco força deny (fail-closed correcto, mas restringe efeitos com payloads não-estruturados). O token GCM preserva o comprimento (oráculo de comprimento residual, mitigável com padding, não feito por omissão).
- **Erasure DSAR pode ser parcial.** Um legal hold colocado a meio da erasure unificada deixa `Result.Partial=true` (a PII de parte dos stores já é irrecuperável) — honestamente sinalizado, mas exige atenção operacional; não há rollback do que já foi destruído.
- **Durabilidade do crypto-shredding herdada do vault.** A impl de referência (`InMemoryKeySource`) perde as chaves no restart do processo; a durabilidade real do shred depende do KeyVault/KMS ligado por trás da porta (fora do reference-model in-memory).
- **A topologia de soberania da UI fica condicional.** A regra é fixa, mas o número de regiões e a federação de operador cross-região dependem de utilizadores/identidade que ainda não existem (ver nota condicional em ## Decisão §6).

---

## Alternativas consideradas

- **Rego/OPA em vez de Cedar.** O catálogo (ADR-011) admite "Rego/OPA **ou** Cedar". Rejeitou-se OPA como dependência de runtime (binário/servidor externo, quebra do build offline/determinista); fixou-se `cedar-go` embebido, mantendo a **semântica** do golden Rego de `§9` (default-deny + obrigações derivadas). O golden Rego permanece como referência de equivalência de comportamento.
- **Autorização imperativa no RM (sem PDP/PEP).** Rejeitada: dispersaria a política por código, tornando-a não-versionável, não-assinável e não-auditável; violaria a separação decide/impõe e a rastreabilidade da `policy_version`.
- **Política não-assinada / mutável no disco do bundle.** Rejeitada: reabriria o vector de substituição do bundle + re-assinatura com chave adversária. A assinatura ed25519 sobre `(domínio, versão, content_hash)` + trust anchor out-of-band fecha-o.
- **Hot-reload sem monotonia.** Rejeitada: permitiria regredir para uma política anterior (mais permissiva) por race ou rollback; a sentinela `ErrStalePolicyVersion` + troca atómica impede-o.
- **Anonimização/remoção total em vez de tokenização + crypto-shredding.** A remoção pura satisfaz minimização mas destrói utilidade operacional reversível; a tokenização por-titular preserva utilidade e **delega o apagamento à destruição da chave** (Art. 17) sem reescrever dados selados nem quebrar a hash-chain. Ambas coexistem, escolhidas por classe na `Policy`.
- **BFF global agregador para a UI multi-região.** Rejeitada: agregar leituras de várias regiões numa terminação global consuma a exportação cross-border no ponto de leitura. Fixou-se **BFF por-região** com o read-path a resolver board→região pela mesma `sovereignty.Registry` do PEP.
- **Read-path da UI fora do âmbito da soberania.** Rejeitada: deixaria um buraco de exportação por onde o PEP de efeitos não passa. A extensão do §6 fecha-o reutilizando a autoridade de soberania no read-path.

---

## Conformidade / Enforcement

Onde a decisão é imposta no código real:

| Invariante | Ficheiro(s) | Mecanismo |
|---|---|---|
| PDP determinista, fail-closed, default-deny | `packages/control-plane/pdp/pdp.go` (`Decide`, `NewUnloaded`) | `engine == nil`, request inválido, ou erro → `Decision{Effect: Deny}`; nunca ausência de resposta |
| Política Cedar default-deny | `packages/control-plane/pdp/policies/aos_authz.cedar` | Cedar nega por omissão; `permit`s explícitos = golden Rego `§9`; exigem `resource.region`, `context.taint != "untrusted"` |
| Bundle versionado e assinado | `packages/control-plane/pdp/bundle.go` (`RawBundle.Verify`, `signingMessage`, `canonicalContentHash`) + `policies/{manifest.json, aos_authz.sig, trust_anchor.pub}` | ed25519 sobre `(domínio, versão, content_hash)`; hash canónico; `ErrSignatureInvalid` fail-closed |
| Trust anchor out-of-band; hot-reload monotónico e atómico | `packages/control-plane/pdp/pdp.go` (`WithTrustAnchor`, `Reload`, `ErrStalePolicyVersion`, `PolicyChangeEvent`, `WithReloadAudit`) | Anchor de fonte confiável; troca só se SemVer estritamente maior; auditável via callback |
| PEP impõe obrigações antes do efeito | `packages/kernel/reference-monitor/obligations.go` (`enforceObligations`, `enforceRegion`, `enforceRedactPII`) | `audit`/`redact_pii`/`region`/`ttl`; tipo desconhecido → deny fail-closed |
| Derivação determinista de obrigações | `packages/control-plane/pdp/engine_cedar.go` (`obligationsFor`) + `pdp/rmadapter.go` (`PolicyCheck`) | `audit(full)` sempre; `redact_pii(email,phone)` se `sensitivity=="confidential"` |
| Minimização + tokenização por classe | `packages/substrate/redaction/{redact.go, policy.go, token.go}` | `ActionRemove` (irrecuperável) / `ActionTokenize` (AES-256-GCM por-titular); `Policy` fail-closed |
| Crypto-shredding (Art. 17) | `packages/substrate/redaction/keysource.go` (`KeySource`, `InMemoryKeySource.Shred`, `KeyRefFor`) | Destruir a chave por-titular torna os tokens irresolúveis |
| DSAR auditável | `packages/control-plane/governance/dsar/flow.go` (`Flow.Receive`, `EventKeyDestroyed`, `EventBlocked`) | Sela `dsar.*` na hash-chain; `SubjectID` pseudónimo; `Result.Partial` honesto |
| Shred sem quebrar a cadeia | `packages/platform/audit/record.go` (`PayloadRef{ContentHash, KeyRef, SubjectID}`, `Partition`) | `EntryHash` sela o `ContentHash`, não o plaintext |
| Soberania por board, fail-closed | `packages/control-plane/governance/sovereignty/registry.go` (`NewRegistry`, `RegionFor`, `Authorized`) | Board/região desconhecidos → `false`; imutável; case-insensitive |
| Read-path soberano (UI/EPIC-13) | `sovereignty.Registry.Authorized` (mesma autoridade) + partição de audit `read:<run\|board>` + `compliance/report.go` | Cada endpoint do BFF resolve board→região e recusa fora-da-região; BFF por-região; agregador global proibido |

**Nota sobre o estado do enforcement.** A composição de produção (o wiring que monta `referencemonitor.NewProduction`, o hook de identidade NHI→humano e a activação do read-path soberano do BFF) faz parte da dívida de integração de backend (PR-0 / EPIC de plataforma), não desta decisão. ADR-011 ratifica a **forma** e as **invariantes**, impostas no código citado; a **autoridade de identidade** que activa o binding humano↔NHI (ADR-003 / AOS-005) e a **topologia operacional multi-região** ficam condicionais a informação humana ainda ausente (utilizadores, boards e IdP reais).

---

## Referências

- Catálogo de ADRs: `_BRIEF.md` (ADR-011); `specs/00_System_Spec.md` (§ catálogo de decisões).
- Contrato C1 RM↔PDP e golden Rego: `tecnica/12 §4` (porta) e `§9` (política de referência). Soberania: `tecnica/09 §6`.
- ADRs relacionados: **ADR-002** (Reference Monitor mandatório — o PEP vive no RM), **ADR-003** (Identidade não-humana / cadeia de delegação que termina num humano), **ADR-005** (separação control/data-plane + taint — `context.taint` alimenta a política), **ADR-010** (Observabilidade OTel + audit WORM hash-chain — sela decisões, obrigações e DSARs), **ADR-012** (SemVer + eval-gate + ratificação assinada — governa a alteração de política), **ADR-013** (gates de risco SA-ROC — o efeito `Escalate` e o gate humano).
- Implementação: EPIC-09 (governança); AOS-091 (redação/ingestão de PII), AOS-093 (crypto-shredding + DSAR), AOS-094 (soberania por board), AOS-097 (relatórios de conformidade derivados do audit).
- Extensão read-path: EPIC-13 (camada frontend) e o ADR novo de UI-governança (AOS-129), que sela o read-path soberano fail-closed e a auditoria producer-bound de leitura sensível.
