# Carta do AOS — Forma do Produto, Registo de Decisões e Definition-of-Done da v1

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | **Carta** — autoridade sobre a forma do produto, as decisões congeladas e o *done* da v1 |
| Versão | **1.0 — RATIFICADA (CONGELADA)** |
| Data | 2026-07-22 |
| Estatuto | **É a fonte única** sobre *o que construímos* e *o que está decidido*. Supera qualquer ambiguidade em documentos subordinados. A partir daqui, a execução mede-se contra o §5 e as decisões só mudam pelo §6 (emenda datada). |

---

## 0. Porque esta Carta existe

O AOS foi construído **bottom-up e com rigor** — mas três coisas nunca foram fixadas, e é
por isso que cada epic reabria a discussão e o programa entrava em **retrabalho sem fim**:

1. **A forma do produto** ("o que entregamos e consideramos *feito*") nunca foi decidida — o
   próprio `00_System_Spec.md` hesita entre *"blueprint"* e *"produto/plataforma standalone"*.
2. **O `_FONTE_agentic-os-ideal.md` é uma visão *ideal*, não congelada** — o alvo deslocava-se.
3. **As decisões estavam espalhadas e re-litigadas** (ADRs, D1–D7, D-TAIL, D4) — sem um
   registo único congelado, voltava-se sempre atrás. O **PR-0** (dívida de integração
   descoberta a meio) foi o sintoma clássico.

Esta Carta resolve os três pontos de uma vez. É deliberadamente curta: não repete o
*quê/como* técnico (isso é o `00_System_Spec.md` e os `tecnica/`), fixa o **enquadramento**.

## 1. Hierarquia de documentos (quem manda sobre o quê)

| Camada | Documento | Autoridade |
|---|---|---|
| **Visão (congelada)** | `_FONTE_agentic-os-ideal.md` | O ideal ratificado. **Só muda por emenda** (§6). Não se toca em execução. |
| **Carta (esta)** | `specs/00_AOS_Carta.md` | **Forma do produto + registo de decisões + DoD da v1 + regra de congelamento.** |
| **Especificação** | `specs/00_System_Spec.md` | O *quê/como* detalhado (domínio, arquitectura, capacidades). Subordinado à Carta na forma do produto. |
| **Decisões arquitecturais** | `docs/adr/ADR-*.md` | Decisões técnicas estruturantes. Referenciadas no registo (§4). |
| **Execução** | `specs/EPIC-*.md` → tickets AOS-NNN | Decompõem e implementam. Medem-se contra o **DoD da Carta**, não contra o "ideal". |
| **Engenharia** | `specs/01_Engineering_Standards_e_Handoff.md` | Padrões de código/DoD-de-domínio por ticket. |

Em caso de divergência sobre **a forma do produto ou o estado de uma decisão**, prevalece
esta Carta. Em caso de divergência técnica de detalhe, prevalece o `_FONTE`/`tecnica/`.

## 2. A forma do produto (DECIDIDA — 2026-07-22)

> **O AOS v1 é um RUNTIME DE REFERÊNCIA DEPLOYÁVEL: um nó `aos` que se instala e corre,
> hospedando *runs* de agentes sob a cadeia de governança REAL, com uma interface externa
> mínima (CLI + API stdlib).**

- **É:** um binário/serviço que se executa (`aos`), que compõe os pilares (o composition-root
  `packages/integration` graduado), que hospeda *runs* de agentes através do loop base + o
  Reference Monitor de produção, e que expõe uma superfície estável (CLI + API `net/http`
  stdlib) para submeter *goals*, observar trajectórias e conduzir/aprovar (steer/HITL).
- **NÃO é:** *só bibliotecas* (as bibliotecas são o kernel + drivers; o "OS" é o nó montado
  que corre). NÃO é (na v1) um SaaS multi-tenant gerido, nem um produto com UI web bespoke
  (isso é condicional — ver D1(b)).
- **A resposta ao ponto de partida:** "corres o nó AOS", não "importas um conjunto de
  bibliotecas". O `cmd/aos-demo` (superfície canónica de referência, AOS-130) é o **embrião**
  deste nó; a v1 gradua-o de demonstrador single-process para um nó de serviço.

## 3. Como se usa o `_FONTE`

O `_FONTE_agentic-os-ideal.md` é a **visão ratificada e congelada**. A Carta + o System Spec +
os ADRs são o **subconjunto CONSTRUÍVEL** dessa visão. O `_FONTE` **não se edita durante a
execução**; se a realidade obrigar a desviar do ideal (ex.: D4), regista-se uma **emenda**
(§6) — nunca uma alteração silenciosa. Isto elimina o "alvo móvel".

## 4. Registo único de decisões (a fonte anti-retrabalho)

Cada decisão tem um estado — **FIXA** (não se re-litiga sem emenda), **CONDICIONAL** (fixada
na invariante, dependente de um gatilho externo nomeado) ou **ABERTA** (por decidir/escalada).

### 4.1 Decisões arquitecturais (ADRs) — todas FIXAS

| ADR | Assunto | Estado |
|---|---|---|
| ADR-006 | Credential broker JIT (segredos que o agente nunca vê) | FIXA |
| ADR-011 | Policy-as-code + GDPR/soberania | FIXA |
| ADR-012 | SemVer + eval-gate para auto-modificação | FIXA |
| ADR-013 | Gates de risco SA-ROC (tiering) | FIXA |
| ADR-015 | Execução durável (idempotência/replay/liveness) | FIXA |
| ADR-016 | Fronteira de confiança da UI (BFF non-signing, WYSIWYS, 4-eyes) | FIXA |
| ADR-017 | Supply-chain do nó e da distribuição (binário zero-dep, imagem distroless/non-root, SBOM+proveniência, gates fail-closed na entrega) | FIXA (emitido 2026-07-22, emenda 1.2). **Exceção ESCOPADA (emenda 1.3):** a lib WebAuthn/CBOR vetada da autoridade de identidade EXTERNA (EPIC-16 frente 4) NÃO entra no binário do nó — o nó mantém-se zero-dep (só stdlib + cedar-go). A dep passa pelos gates (sca/govulncheck, go.sum pinado, SBOM). |

*(Correcção — emenda 1.2: a linha "todas FIXAS" acima só é literalmente verdadeira DESDE que o ADR-017 foi emitido. Antes, o painel `wamnbffrk` apanhou a afirmação como falsa — o 017 estava reservado-mas-não-redigido. Está agora emitido `docs/adr/ADR-017-supply-chain-node.md`.)*

### 4.2 Decisões de programa (D-series)

| D | Assunto | Estado | Dono | Referência |
|---|---|---|---|---|
| **D1(b)** | Superfície web SPA bespoke | **CONDICIONAL** — condicional a utilizadores reais + TCO de ingress + dono de 2.ª supply-chain | Produto | EPIC-13 §25 |
| **D2** | Stack no-build (HTMX/`go:embed`) | **FIXA** | Plataforma | EPIC-13 §25 |
| **D3** | Transporte SSE stdlib (não gRPC/WS/GraphQL) | **FIXA** | Plataforma | EPIC-13 §25 |
| **D4** | **Autoridade de identidade** (IdP + AAGUID + binding humano↔NHI + custódia de chave no vault) | **EM PROVISIONAMENTO — OPÇÃO A** (emenda 1.3, 2026-07-23): o token spine (AOS-156, **Camada A** — enforcement) está CONSTRUÍDO; o dono escolheu provisionar a **autoridade REAL completa** (**Camada B**) — (1) IdP humano via `HumanDirectory` OIDC real, (2) custódia de chave externa (issuer como processo separado + contrato KMS/HSM), (3) binding humano↔NHI + ADR-003 formal, (4) attestation WebAuthn/AAGUID (AOS-162 sai de stub) — executada como **EPIC-16**. Exceção escopada ao zero-dep para a frente WebAuthn (só no componente de autoridade externo; o binário do nó fica zero-dep). Partes org (tenant IdP, HSM, allowlist AAGUID) = config de deployment; o código entrega adaptadores/contratos. | Segurança + Produto | `docs/reports/D4-escalacao-autoridade-identidade.md`; §7 emendas 1.1/1.3 |
| **D5** | BFF single-process | **FIXA** (postura); gatilho de graduação CONDICIONAL a SLO/utilizadores | Plataforma | EPIC-13 §25 |
| **D6** | Auditoria de leitura sensível | **FIXA** | Governança | EPIC-13 §25 |
| **D7** | Read-path soberano fail-closed | **FIXA** (regra); topologia CONDICIONAL a regiões/boards reais | Governança | EPIC-13 §25 |
| **D-TAIL** | Dono do tail/assembly (um só prefix-hash por run) | **FIXA — RESOLVIDA**: o loop delega a `WindowPort` | Plataforma | EPIC-14/AOS-157 |

**A D4 está EM PROVISIONAMENTO (emenda 1.3, Opção A):** o token spine (AOS-156, Camada A —
enforcement) está construído; a **autoridade real completa** (Camada B — IdP OIDC, custódia de chave
externa, binding humano↔NHI/ADR-003, attestation WebAuthn/AAGUID) está a ser provisionada via
**EPIC-16**. Todas as outras decisões estão FIXAS ou CONDICIONAIS-a-gatilho-externo (não a código
pendente).

**Notas de coerência (emenda 1.2, do painel `wamnbffrk`):**
- **Não-repúdio HITL e identidade fim-a-fim — DEFERIDOS com o eixo D4.** A superfície de aprovação
  (steer/approve) fica *estruturalmente completa mas criptograficamente demo-grade* até AOS-160
  (assinatura ed25519) e AOS-162 (4-eyes atestado) — que dependem do token spine (AOS-156). Não se
  declara "HITL feito" com este eixo desligado.
- **D3 (SSE stdlib) e D5 (single-process) — REAVALIAR para o modelo de ameaça do nó-serviço.**
  Foram fixadas no contexto BFF-atrás-de-SPA; num nó exposto como serviço de rede o modelo de
  ameaça é diferente (a separação "dois canais" passa a ser de protocolo/taint, não física; a SSE
  precisa de validação sob N ligações longas). A regra mantém-se, mas a sua aplicação ao nó é
  revista na EPIC-15 (não re-litígio — reavaliação de contexto).
- **Entrypoint canónico (fim da deriva de nomes).** O composition-root canónico é
  **`integration.NewSecuredRuntime`**, que constrói o RM via **`referencemonitor.NewProductionSecure`**;
  o demonstrador/embrião do nó é **`packages/cmd/aos-demo`**. Estes três nomes designam camadas
  distintas, não alternativas.

## 5. Definition-of-Done da v1

A **v1 do AOS** (o nó `aos` deployável) está *feita* quando **todos** os seguintes forem verdes:

- [ ] **O nó `aos` corre**: um binário que compõe o `packages/integration` (RM de produção via
      `NewProductionSecure`, cadeia real de hooks, WORM único) e hospeda um *run* fim-a-fim.
- [ ] **Interface externa mínima estável**: CLI + API `net/http` stdlib para submeter *goal*,
      observar trajectória (SSE, D3) e conduzir/aprovar (steer/HITL).
- [ ] **Cadeia de governança REAL a mediar** cada tool call (mediação total; guard-test das 5
      negações — AOS-161, já feito).
- [ ] **Critérios de aceitação sistémicos** do `00_System_Spec.md §13` verdes.
- [ ] **Gates fail-closed verdes** (`run.sh`: build/test/-race/lint/secrets/sast/sca/apex/selftest).
- [ ] **D4 DESBLOQUEADO — PRÉ-REQUISITO da v1** (emenda 1.1, 2026-07-22): o token spine real
      (AOS-156 — chave do issuer no vault, NÃO controlada pelo nó) dá ao nó um modo **REAL+SEGURO**;
      a v1 **não se declara em modo demo-only**. O endurecimento organizacional (IdP corporativo,
      attestation, HSM) é posterior e documentado, não bloqueia a v1.

**Nota (emenda 1.1):** a decisão do dono (após o painel adversarial `wamnbffrk`) foi **desbloquear
o D4 primeiro** — construir a identidade real ANTES de declarar a v1, para que exista de facto uma
configuração onde o nó corra trabalho **real E seguro** (resolve o achado ALTO "forma
sobre-reivindicada"). A forma "nó deployável" mantém-se; o token spine (AOS-156) passa à FRENTE do
trabalho do nó (EPIC-15), que fica a jusante dele.

## 6. Regra de congelamento (o mecanismo anti-retrabalho)

1. **Nenhuma decisão FIXA se re-litiga** sem uma **emenda** registada nesta Carta — uma linha
   datada no §7 (Controlo de versões) que diz *o quê* mudou, *porquê* e *quem* aprovou.
2. Uma decisão **CONDICIONAL** só "abre" quando o seu **gatilho nomeado** ocorre (ex.: D1(b)
   quando houver utilizadores reais + dono de supply-chain). Até lá, não se re-discute.
3. Uma decisão **ABERTA** tem sempre um **dono** e uma **escalada** (não fica em limbo).
4. Descobrir dívida escondida (como o PR-0) **não** é re-litigar — é uma emenda que se regista
   e se executa. O que se proíbe é reabrir o que já está FIXO por preferência ou dúvida.
5. **Árbitro da fronteira "dívida escondida vs re-litígio" (emenda 1.2).** A distinção do §6.4 é
   subjectiva; para não ser um *loophole* que re-legitime a doença que o §0 diagnostica, cada
   invocação do §6.4 é **arbitrada** pelo **Arquitecto de Plataforma + Responsável de Segurança**
   (dois papéis, não um), que decidem por escrito na emenda: uma "descoberta" é dívida escondida
   SÓ se assenta em **facto novo verificável** (código/build/painel) que não existia à data da
   decisão FIXA; caso contrário é re-litígio e é recusada.
6. **Tripwire de falsificação da promessa anti-retrabalho (emenda 1.2).** A afirmação "isto acaba
   o retrabalho" tem de ser falsificável, senão é fé. **Condição de falha declarada:** se, numa
   janela de 30 dias, **≥ 2 decisões FIXAS forem reabertas** OU **≥ 2 invocações do §6.4 forem
   recusadas como re-litígio pelo árbitro**, o mecanismo de congelamento **falhou** e a Carta é
   revista na raiz (não emendada à margem). Este contador é o SLI do próprio processo.

## 7. Controlo de versões (emendas)

| Versão | Data | Alteração | Aprovação |
|---|---|---|---|
| 0.1 | 2026-07-22 | Emissão inicial (PROPOSTA). Fixa a forma do produto (runtime de referência deployável — nó `aos`), consolida o registo de decisões (ADRs + D1–D7 + D-TAIL + D4), define o DoD da v1 e a regra de congelamento. | Proposta |
| **1.0** | **2026-07-22** | **RATIFICADA e CONGELADA.** A forma do produto, o registo de decisões, o DoD da v1 e a regra de congelamento passam a autoridade. A partir daqui, nenhuma decisão FIXA se re-litiga sem uma emenda datada nesta tabela. | **Ratificada pelo dono do produto** |
| **1.1** | **2026-07-22** | **EMENDA — decisão do dono após o painel adversarial `wamnbffrk`.** O **D4 passa de ABERTA-deferida a EM CURSO — PRÉ-REQUISITO da v1**: desbloquear a identidade real (token spine AOS-156, chave do issuer no vault, não controlada pelo nó) ANTES de declarar a v1, para que exista um modo real+seguro (resolve o achado ALTO "forma sobre-reivindicada"). A forma "nó deployável" mantém-se; AOS-156 passa à frente da EPIC-15. **Achados materiais do painel POR ENACTAR** (na revisão da EPIC-15/token-spine): autenticação do canal de controlo (AOS-166); durabilidade do Event Store/WORM (não in-memory); metodologia do e2e AOS-169 (modelo que emite tool calls + caminho permitido + contentor real); observabilidade + `/healthz`·`/readyz`; reconciliação do System Spec §1 e da colisão single-process vs substrato distribuído; ADR-017 (supply-chain) antes de empacotar; dimensionamento no-XL (dividir AOS-164, repromover AOS-167). | Dono do produto |
| **1.2** | **2026-07-22** | **EMENDA de COERÊNCIA (Passo 2 do roadmap pós-painel).** Enacta os achados de governança/coerência: **(E3)** reconciliado o **System Spec §1** (removida a hesitação "standalone/blueprint" e a precedência `_FONTE`; critério forma-vs-detalhe explícito); a **v1 single-host / sem-HA / com-SPOF** é declarada **non-goal DATADO** — o substrato distribuído/sem-SPOF (pressuposto do System Spec §2.4/§14) é milestone **posterior (EPIC-10)**, não regressão. **(E9)** emitido **ADR-017** (supply-chain do nó) e registado FIXA no §4.1; corrigida a afirmação "todas FIXAS"; adicionados o **árbitro do §6.4** (§6.5) e o **tripwire de falsificação** (§6.6). **(E10)** marcados não-repúdio HITL + identidade fim-a-fim **DEFERIDOS** com D4; **D3/D5 a reavaliar** para o modelo de ameaça do nó-serviço; **entrypoint canónico** uniformizado (§4.2). **Sign-off técnico PENDENTE:** a ratificação (v1.0/1.1/1.2) é do **dono do produto** (em sessão); o **sign-off de Segurança e de Arquitectura** fica **por obter** — é pré-condição da aceitação da v1 (§5), não fabricado aqui. | Dono do produto; Segurança/Arquitectura **pendente** |
| **1.3** | **2026-07-23** | **EMENDA — decisão do dono: enactar a OPÇÃO A do D4 (provisionar a autoridade de identidade completa).** Após a análise profunda do D4 (três camadas: *enforcement* CONSTRUÍDO; autoridade *demo-grade*; autoridade *real* por provisionar), o dono escolheu a **Opção A**: construir as impls reais das quatro frentes da **Camada B** — (1) IdP humano via `HumanDirectory` **OIDC** real; (2) custódia de chave externa (issuer como processo separado + contrato KMS/HSM); (3) binding humano↔NHI + **ADR-003** formal; (4) attestation **WebAuthn/AAGUID** (AOS-162 sai de stub). Executada como **EPIC-16** (AOS-174/175/176/177). **EXCEÇÃO ESCOPADA ao zero-dep (ADR-017 ponto 1):** o componente de **autoridade de identidade externo** ao nó pode usar uma biblioteca WebAuthn/CBOR vetada (`go-webauthn/webauthn`) para a frente 4; o **binário do nó mantém-se zero-dep** (só stdlib + cedar-go) — a verificação de attestation vive no issuer/autoridade externo, preservando o ADR-017 do artefacto do nó. A dep passa pelos gates (sca/govulncheck, go.sum pinado, SBOM). As partes org (tenant IdP, HSM, allowlist AAGUID) são **config de deployment**; o código entrega adaptadores/contratos + doubles de referência. **Sign-off de Segurança/Arquitectura continua PENDENTE** (pré-condição da v1, §5). | Dono do produto; Segurança/Arquitectura **pendente** |

---

*Esta Carta está RATIFICADA (v1.0) e é a autoridade congelada. A execução (epics/tickets)
mede-se contra o §5; as decisões só mudam pelo §6 (emenda datada acima).*
