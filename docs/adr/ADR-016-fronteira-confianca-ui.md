# ADR-016 — Fronteira de confiança da camada de UI

| Campo | Valor |
|---|---|
| **ADR** | 016 |
| **Título** | Fronteira de confiança da camada de UI |
| **Estado** | Aceite |
| **Data** | Julho 2026 |
| **Deciders** | Equipa AOS |
| **Contexto-fonte** | Ratificação da `specs/EPIC-13_Frontend.md` v1.0 (ronda de painel adversarial); catálogo de ADRs em `_BRIEF.md` (linhas 74–82) e `specs/00_System_Spec.md` (linhas 248–256); ADR-005 (separação control/data-plane + taint), ADR-006 (Credential Broker JIT), ADR-010 (observabilidade OTel + audit WORM), ADR-011 (policy-as-code + GDPR), ADR-013 (gates de risco SA-ROC + controlo bidireccional) |

## Contexto

Os pilares de segurança e governança do AOS estão implementados como bibliotecas
in-process: o Reference Monitor medeia toda a acção (ADR-002), o Credential Broker
detém os segredos downstream (ADR-006), o PDP/PEP impõe política e soberania
(ADR-011), o gate HITL sela decisões humanas assinadas (ADR-013) e o audit encadeia
tudo num log WORM tamper-evident (ADR-010). Falta, porém, a **superfície humana** que
apresente estes estados e recolha as decisões dos operadores.

Uma camada de UI é, por natureza, o oposto da filosofia *reference-model* do resto do
AOS: é em-rede, *stateful*, dependente de um segundo *toolchain* e — se for um
*browser* — expõe uma superfície não-determinista e um vector de XSS. Introduzi-la sem
uma fronteira de confiança explícita **reabsorveria** para dentro da UI o enforcement
que os pilares mantêm fora dela. O erro clássico é deixar a superfície tornar-se
**autora** da segurança em vez de mera **apresentadora**: um BFF que assinasse *em nome
do humano* com uma chave server-side aniquilaria o não-repúdio; um canal *duplex* que
misturasse dados e controlo reabriria a via de *prompt injection* que o ADR-005 fecha;
um BFF global que terminasse tráfego de várias regiões consumaria a exportação
*cross-border* que o ADR-011 proíbe.

Este ADR fixa a **regra de ouro** transversal à EPIC-13: **a superfície APRESENTA,
nunca é autora do enforcement**. Deriva dela seis invariantes estruturais que
delimitam a fronteira de confiança da UI. Todas são ratificáveis hoje porque a sua
mecânica já existe no código dos pilares; o que fica **condicional** é apenas a
*autoridade de identidade* que activa o 4-eyes e o não-repúdio (ver §Decisão 4 e
§Alternativas), porque depende de uma organização e de utilizadores reais que ainda
não existem (zero implantações, zero *runs*, zero NHIs).

Nota de âmbito: este ADR governa a **fronteira de confiança**, não a *forma* da UI. A
forma canónica (demonstrador single-process, read-path sobre observabilidade-commodity,
no-build HTMX+`go:embed`, transporte SSE) é ratificada na EPIC-13 v1.0 e no ADR de
UI-governança (AOS-129); este ADR-016 assume-a e concentra-se nas seis invariantes de
segurança.

## Decisão

### 1. Custódia de chave — NUNCA no cliente; BFF *non-signing* pass-through

Distinguem-se **duas custódias** que jamais se confundem:

- **Chave de decisão HUMANA** (aprovação / ratificação): a assinatura é produzida por
  um **autenticador de hardware do humano** (credencial WebAuthn/FIDO2
  *non-extractable*). A chave privada **nunca** existe no *heap* JS do *browser*,
  **nunca** no Vault, **nunca** no BFF. «O cliente assina» significa: o secure element
  do dispositivo do humano assina `(run_id ‖ kind ‖ payload)`; o BFF é um
  **pass-through *non-signing*** que só transporta a decisão já assinada.
- **Credencial downstream da NHI do agente**: continua governada pelo Credential Broker
  JIT server-side (ADR-006, `messaging.Signer`). O broker mantém-se **inalterado** —
  mas é **proibido** usá-lo para produzir a assinatura de uma decisão/ratificação
  humana. O broker assina *como o agente*, nunca *como o humano*.

Corrige-se a letra de AOS-132: o BFF é *non-signing* para **ambas** as chaves. Não há
caminho de código em que a superfície detenha material de assinatura.

### 2. WYSIWYS — a assinatura cobre um digest do efeito EXIBIDO

*What You See Is What You Sign*: a assinatura humana tem de cobrir um **digest canónico
determinista do efeito concretamente apresentado** ao operador —
`digest = H(Preview ‖ Capability ‖ Resource ‖ Class ‖ Irreversible)`. O verificador
**recomputa** este digest a partir do que **vai realmente executar** e recusa em
*mismatch*; o **mesmo digest** é o *challenge* WebAuthn assinado pelo hardware. Assim o
WYSIWYS sobrevive intacto até ao secure element: nenhum intermediário (BFF, transporte,
JS) pode trocar o efeito depois de exibido sem invalidar a assinatura. Estende-se a
serialização canónica de aprovação (`hitl.canonicalApproval`) para incorporar este
digest — a base determinista length-prefixed já existe (`hitl/encode.go`) e os campos
do efeito já são apresentados na `hitl.Presentation`.

### 3. *Deadline* imposta pelo SERVIDOR, não pelo *browser*

O prazo de decisão de um gate é uma **propriedade de segurança fail-closed**, não uma
conveniência de UI. É imposto **server-side** pelo `context.Context` do gate de risco;
o silêncio (timeout, desconexão, aba fechada) **NEGA** — em particular para acções
irreversíveis. O *browser*/cliente **não** pode prolongar, encurtar nem contornar o
prazo: uma UI comprometida ou ausente resulta em *deny*, nunca em *permit* por
omissão. O relógio do cliente é irrelevante para a decisão.

### 4. 4-eyes real por *attestation* de dispositivo distinto

Para acções intrinsecamente *danger* ou irreversíveis, exige-se **dupla aprovação
genuína**: duas credenciais WebAuthn **atestadas e distintas** (credential-id / AAGUID
distintos), **duas sessões vivas distintas**, com *challenge* **por-perna** emitido
pelo servidor; o servidor **recusa** uma segunda assinatura do mesmo principal, sessão
ou credencial. O 4-eyes amarra-se ao **risco intrínseco** (Class/Irreversible), não ao
*mode* do tiering — uma política mal-configurada não pode rebaixar uma acção
não-desfazível para contornar o dual-control.

**CONDICIONAL** — a *autoridade de identidade* que activa este requisito (qual o IdP
que faz *enrollment*/attestation do WebAuthn, a *allowlist* de AAGUID, e o *binding*
humano↔NHI) fica **diferida** para quando existir um directório de identidade real
(substituto de `hitl.MemApproverRegistry` / `IdentityStub` de AOS-005), aprovadores
humanos nomeados com as suas NHIs, e uma política de dispositivos/attestation da
organização. A verificação actual por **igualdade de string** (`appr.Approver ==
pres.Requester` em `hitl/channel.go`) fica marcada como **STUB necessário-mas-
insuficiente**: garante a estrutura (recusa auto-aprovação), não a distinção física de
dispositivo. Ratifica-se a **invariante**; a **autoridade** desbloqueia com informação
humana ausente.

### 5. Read-path SOBERANO + auditoria WORM das leituras sensíveis

Toda a leitura servida pelo BFF (estado, custo, trajectória) é **soberana e
fail-closed**: cada endpoint resolve `board → região` via a **mesma** autoridade
imutável que o PEP usa (`sovereignty.Registry.Authorized`) e **recusa** servir fora da
região autorizada — fechando o buraco de que o Registry hoje só alimenta o PEP de
*efeitos*, deixando o *read-path* por cobrir. Em multi-região a topologia é **BFF
por-região** (os dados nunca deixam a região); um **BFF global agregador é PROIBIDO**
por consumar a exportação *cross-border* na própria terminação.

Selam-se no audit WORM **apenas** as leituras **sensíveis**, com gatilho
*producer-bound* idêntico aos rótulos que o audit já sela: `Taint == "untrusted"` **OU**
`Sensitivity == "confidential"` **OU** presença de `PayloadRef` (PII/`SubjectID`)
**OU** leitura de um titular com DSAR aberto. Estes registos vão para uma **partição
de audit dedicada** (`read:<run|board>`), **separada** da cadeia de decisões e da
telemetria transmitida — a telemetria de alta frequência **nunca** entra na hash-chain.
Cada registo de leitura carrega `PayloadRef{ContentHash, KeyRef, SubjectID}` para ser
**crypto-shreddable** (Art. 17 RGPD) sem quebrar a cadeia, e é projectado *query-time*
pelo relatório de conformidade existente. O **override-rate** é agregado
**por-aprovador** (chave `AttrApprover` / `Principal.NHIID`), com o alarme 0.40 por
aprovador e um piso de amostra mínima para ser accionável.

### 6. Separação física canal-controlo (*trusted*) / canal-dados (*untrusted*)

Herda-se directamente do ADR-005 a separação de *taint*, materializada em **dois canais
fisicamente distintos**:

- **Canal de dados (untrusted)** — *stream* **SSE** unidireccional (`net/http` +
  `http.Flusher`, stdlib). Transporta estado/custo/trajectória para o cliente. É
  *untrusted*: nada que chegue por aqui pode tornar-se instrução.
- **Canal de controlo (trusted)** — **POST** autenticado. Cada sinal (pause / steer /
  resume / approve) é uma decisão **assinada** verificada por um `Authenticator`
  server-side; conteúdo *untrusted* (resultado de tool, web) não carrega credencial de
  emissor válida e é **rejeitado** — nunca se torna sinal de controlo.

Rejeita-se **WebSocket**: exige dependência externa (quebra zero-dep e cega o SCA) e a
sua bidireccionalidade **re-fundiria** dados+controlo num só socket, violando a
separação de *taint*. O resume do SSE é nativo por `Last-Event-ID`, mapeado 1:1 no
cursor de `seq` do Event Store. Semântica de resumibilidade ratificada com honestidade:
**resume-from-seq, at-least-once com dedup por `seq` no cliente**, durabilidade = a do
próprio Event Store. **Não** se afirma *exactly-once* através de reinício do processo
— em BFF single-process o Event Store in-memory perde o log no restart; *exactly-once*
inter-restart fica **condicional** à graduação do Event Store para um backend durável
(fora de âmbito, ver ADR-013/EPIC-10 e AOS-101).

## Consequências

### Positivas

- **Não-repúdio real**: a chave de decisão humana vive num secure element
  *non-extractable*; nem um BFF comprometido nem uma exfiltração de Vault produzem uma
  aprovação forjada. A superfície nunca é ponto único de falha da autoridade.
- **WYSIWYS até ao hardware**: o *digest* do efeito exibido é o *challenge* assinado;
  um atacante que troque o efeito depois de exibido invalida a assinatura.
- **Fail-closed por construção**: *deadline* server-side, timeout→deny, selagem
  audit-before-effect e resolução de soberania recusam por omissão — a ausência de UI
  nunca degrada para permissão.
- **Superfície de gates 100% legível pelo SCA/SAST**: sem *toolchain* JS, sem
  WebSocket, os gates *build/sca/sast* mantêm-se *fail-closed* com significado sobre
  toda a superfície.
- **RGPD por desenho preservado**: leituras sensíveis são *crypto-shreddable* via
  `PayloadRef`, numa partição dedicada, sem quebrar a hash-chain de decisões.
- **Soberania estendida ao read-path**: o mesmo `sovereignty.Registry` que o PEP usa
  passa a cobrir também a leitura — o *cross-border* fecha na terminação.

### Negativas / trade-offs

- **Dependência de hardware**: exigir WebAuthn *non-extractable* + attestation impõe
  dispositivos compatíveis aos aprovadores; aprovadores não-técnicos ou sem hardware
  ficam sem via até existir política de dispositivos — custo real de operação.
- **Autoridade de identidade em aberto**: sem IdP/attestation, o 4-eyes fica em
  **STUB** (igualdade de string) — estruturalmente correcto mas **insuficiente** contra
  um adversário com dois principais lógicos no mesmo dispositivo. O sistema não deve ser
  operado em produção danger-crítica antes de fechar esta condicional.
- **Sem exactly-once inter-restart**: em single-process, uma morte do processo perde o
  *stream* não persistido; a resumibilidade forte depende de graduar o Event Store —
  explicitamente adiado.
- **BFF por-região multiplica operação**: proibir o agregador global significa operar
  um BFF por região de soberania, com o custo de topologia associado (aceite como preço
  do fecho *cross-border*).
- **SSE unidireccional**: qualquer necessidade futura de *duplex* real (fan-out
  multi-nó) exigirá reabrir esta decisão sob o gate de excepção do ADR-012/AOS-096.

## Alternativas consideradas

- **BFF assina em nome do humano com chave server-side (broker-only)** — *rejeitada*.
  Reduz o não-repúdio a «confie no servidor»; uma comprometência do BFF/Vault forja
  aprovações. O broker (ADR-006) só assina *como o agente*.
- **Chave de decisão no *browser* (WebCrypto software key)** — *rejeitada*. Uma chave
  no *heap* JS é exfiltrável por XSS; viola a invariante «nunca no cliente».
- **WebSocket duplex** — *rejeitada*. Dependência externa (quebra zero-dep, cega o SCA)
  e re-fusão dados+controlo num socket (viola ADR-005). SSE + POST cobre o caso do
  reference-model (sem fan-out multi-nó).
- **Deadline no cliente (timer JS)** — *rejeitada*. Torna o prazo contornável por uma
  UI comprometida; o gate deixa de ser fail-closed.
- **4-eyes por dois logins na mesma sessão/dispositivo** — *rejeitada* como suficiente;
  aceite apenas como STUB. Sem attestation de dispositivo distinto, não há dupla
  aprovação genuína.
- **Selar TODA a leitura no WORM** — *rejeitada*. Poluiria a hash-chain com telemetria
  de alta frequência e inflacionaria o WORM; sela-se só o sensível, *producer-bound*,
  em partição dedicada.
- **BFF global agregador com gate por board** — *rejeitada*. A terminação de tráfego
  multi-região no mesmo nó **é** a exportação *cross-border*; a topologia soberana é
  por-região.
- **Fixar agora a autoridade de identidade (IdP/AAGUID/binding humano↔NHI)** —
  *adiada* (condicional). Sem utilizadores, NHIs nem organização reais, nomear o
  registador que atesta «este autenticador pertence a `human:` e cobre `approve:danger`»
  seria fingir certeza. Ratifica-se a invariante; congela-se a autoridade até existir
  informação humana ausente.

## Conformidade / Enforcement

Onde cada invariante é (ou será) imposta no código real do repositório:

- **Custódia / BFF non-signing (Decisão 1)** — `packages/platform/messaging/ports.go`
  (`Signer.Sign(ctx, nhi, message)` assina server-side; a chave privada «nunca entra
  neste módulo nem no runtime do agente») e `packages/platform/broker/doc.go`
  («o agente nunca vê o segredo»; `vault.Secret.DeliverTo` entrega server-side e não
  devolve o valor). A chave de decisão humana é disjunta do broker por construção: o
  aprovador assina via `hitl.SignApproval` (`hitl/approval.go`), que recebe só a
  assinatura de um `messaging.Signer` do lado do custodiante do **humano**, nunca do
  broker do agente.
- **WYSIWYS (Decisão 2)** — `hitl.canonicalApproval` (`hitl/approval.go`) e o molde
  determinista length-prefixed de `hitl/encode.go`; os campos do efeito exibido já
  existem em `hitl.Presentation` (`Preview`, `Capability`, `Resource`, `Class`,
  `Irreversible` em `hitl/channel.go`). A extensão do canónico para incorporar o
  `digest` do efeito e usá-lo como *challenge* WebAuthn é o item de trabalho AOS-131.
- **Deadline server-side (Decisão 3)** — `hitl.Channel.Confirm` (`hitl/channel.go`):
  `ApprovalSource.Await` respeita o `ctx` do gate; `ctx.Err()` /
  `context.DeadlineExceeded` ⇒ `ReasonTimeout` ⇒ *deny* (e `Timeouts++`). A decisão de
  timeout «não depende do relógio injectável — depende do ctx que o gate de risco
  impõe».
- **4-eyes (Decisão 4)** — verificação de assinatura contra a chave **pinada**
  (`verifyApproval`), cobertura de autoridade `approve:<classe>` (`RequiredAuthority`),
  e o dual-control em `hitl/channel.go` (`pres.Class == risk.ClassDanger ||
  pres.Irreversible` com `appr.Approver == pres.Requester` ⇒ `ReasonDualControl` /
  *deny*). O `hitl.ApproverRegistry` / `MemApproverRegistry` (`hitl/approver.go`) é a
  porta de identidade — «Produção substitui por um directório de identidade real»
  (a condicional AOS-005).
- **Read-path soberano (Decisão 5)** — `sovereignty.Registry.Authorized(board, region)`
  fail-closed (`governance/sovereignty/registry.go`); a mesma autoridade que o PDP anexa
  como obrigação `region` (`pdp/sovereignty.go`, `ObligationRegion`) e que o PEP impõe
  em `referencemonitor.enforceRegion` (`reference-monitor/obligations.go`). O BFF passa
  a chamar `Authorized` em cada endpoint de leitura.
- **Auditoria WORM de leituras sensíveis (Decisão 5)** — os rótulos *producer-bound*
  `CallContext.Taint` / `Sensitivity` e `PayloadRef{ContentHash, KeyRef, SubjectID}`
  em `packages/platform/audit/record.go`; a partição e o encadeamento em
  `audit/chain.go` (`ComputeEntryHash`, `GenesisHash(partition)`); a projecção
  *query-time* e o crypto-shredding no relatório de conformidade
  (`governance/compliance/report.go`). O override-rate por-aprovador usa `AttrApprover`
  e `DefaultOverrideRateThreshold = 0.40` (`hitl/metrics.go`).
- **Separação de canais (Decisão 6)** — canal de controlo em
  `packages/kernel/agent-runtime/control/steer_channel.go`: o `Emitter` assina
  `(run_id ‖ kind ‖ payload)`, o `Authenticator` («fronteira de segurança do canal,
  ADR-013 + ADR-005») rejeita fail-closed qualquer emissor não autenticado, e cada
  sinal é gravado (`EventTypeControlSteer`, …) para não-repúdio. O canal de dados (SSE)
  costura-se **apenas** sobre `eventstore.EventStore` (`Read(fromSeq)` + `Subscribe`,
  `packages/substrate/eventstore/store.go`), com resume por `Last-Event-ID` = último
  `seq` — nunca sobre internals do *Store*.

## Referências

- `specs/EPIC-13_Frontend.md` (v1.0 ratificada) — advertências W1–W3 e decisões D1–D7.
- ADR-005 — Separação control/data-plane + *taint* (`_BRIEF.md` linha 74;
  `specs/00_System_Spec.md` linha 248).
- ADR-006 — Credential Broker com tokens JIT (`docs/adr/ADR-006-credential-broker-jit.md`;
  catálogo `_BRIEF.md` linha 75).
- ADR-010 — Observabilidade OTel GenAI + audit WORM (`_BRIEF.md` linha 79;
  `specs/00_System_Spec.md` linha 253).
- ADR-011 — Policy-as-code + GDPR por desenho; soberania por board (`_BRIEF.md` linha
  80; `specs/00_System_Spec.md` linha 254).
- ADR-013 — Gates de risco SA-ROC + controlo bidireccional (`_BRIEF.md` linha 82;
  `specs/00_System_Spec.md` linha 256).
- ADR de UI-governança (AOS-129) — superfície canónica reference-model, read-path
  sobre observabilidade-commodity, no-build HTMX+`go:embed` e SSE stdlib.
- AOS-005 (`IdentityStub`) — **autoridade de identidade condicional**: IdP de
  *enrollment*/attestation WebAuthn, *allowlist* de AAGUID e *binding* humano↔NHI que
  activam o 4-eyes e o não-repúdio.
- `packages/control-plane/governance/hitl/` (channel, approval, approver, encode,
  metrics), `packages/platform/audit/` (record, chain),
  `packages/control-plane/governance/sovereignty/registry.go`,
  `packages/control-plane/pdp/sovereignty.go`,
  `packages/kernel/reference-monitor/obligations.go`,
  `packages/kernel/agent-runtime/control/steer_channel.go`,
  `packages/substrate/eventstore/store.go`,
  `packages/platform/messaging/ports.go`, `packages/platform/broker/`.
