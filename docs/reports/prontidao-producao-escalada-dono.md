# Escalada ao dono — os 3 bloqueios de go-live que não são código

| Campo | Valor |
|---|---|
| Data | 2026-08-05 |
| Origem | Revisão adversarial de prontidão para produção (workflow 13 agentes) |
| Veredito da revisão | **NO-GO** — 6 blockers; 3 já remediados por código, **3 dependem de si** |
| Tipo | Decisão humana / provisionamento de infra / ratificação — **não resolúvel por código** |
| Donos sugeridos | Responsável de Segurança + Dono de Produto/Plataforma |
| Estado do código | Todo o *enforcement* está construído e provado, fail-closed; falta a **fonte de verdade** e a **aceitação formal** |

---

## 0. Contexto em três frases

Uma revisão adversarial multi-perspetiva (segurança, SRE, compliance, arquitetura, verificação,
produto) concluiu **NO-GO para produção**, com 6 blockers. **Três eram código-fixável e já foram
fechados** (guarda de KEK durável, `/readyz` a sondar o Vault + hardening operacional, via de
métricas `/metrics`) — verificados em-container e live. **Os outros três não são programação**: são
uma **decisão + provisionamento seus**. Enquanto não fecharem, o veredito mantém-se NO-GO — mas o
nó fica **seguro e inerte** (recusa fail-closed), nunca inseguro.

O que já não bloqueia (remediado; para referência):

| # | Era | Fechado por |
|---|---|---|
| #3 | Produção aceitava KEK em memória (over-erasure silenciosa) | `ErrProductionNeedsDurableKEK` (recusa arrancar sem custódia durável) — commit `9ef381e` |
| #2 | `/readyz` cego ao Vault; sem restart/limites/métricas de SRE | Sonda de KEK no readyz `2eededc` + hardening compose `99de622` |
| #5 | Sem via de métricas (SLOs indetectáveis) | Endpoint `/metrics` + scrape do collector `2bc4c29` |

---

## 1. Os três bloqueios que precisam de si

### #1 — Autoridade de Identidade (D4 / EPIC-16) — *o long-pole*

**O que é:** quem/o quê é a **fonte de verdade** da identidade, para que os tokens não-humanos
(NHI) sejam **não-forjáveis em produção**. Hoje essa autoridade não existe como infra real.

**Porque não é código:** a espinha de token (AOS-156) exige que o *trust-anchor* do verifier seja
a chave pública de um **issuer real cuja chave privada vive num vault/HSM e NUNCA é controlada pelo
nó**. Se o nó controlasse a chave, seria *teatro criptográfico* (verificaria tokens que ele próprio
mintou). A cadeia de confiança tem de ser **ancorada fora do código** — decisão + provisionamento
humanos.

**Estado:** o *enforcement* está **completo e provado** (AOS-152/153/154/157/161 ✅ — o RM de
produção nega anónimo/raiz-forjada/taint/egress/scope). O que falta é a **identidade em si**: em
produção, `identity.NewVerifier()` sem trust-anchors **nega toda a NHI** → cada tool call é negada
(seguro mas **inerte**). **Não se reivindica** não-forjabilidade, e não se deve.

**Bloqueia:** AOS-156 (espinha de token), AOS-160 (Authenticator ed25519), AOS-162 (4-eyes).
**Detalhe completo:** [`D4-escalacao-autoridade-identidade.md`](D4-escalacao-autoridade-identidade.md).
A emenda 1.1 da Carta já **decidiu desbloquear** (Opção A — provisionar a autoridade completa); falta
**executar o provisionamento**.

**O que precisamos de si:** confirmar a Opção A e **provisionar/nomear**: (a) o issuer real + custódia
da sua chave (vault/HSM, EPIC-07), (b) o IdP humano corporativo para o binding humano↔NHI, (c) a
allowlist de *device attestation* (se aplicável). Depois, AOS-156/160/162 desbloqueiam-se e executam-se.

---

### #2 — Taint-gate P4 armável — a *fonte* `Privileged` (AOS-183)

**O que é:** o invariante-bandeira **"untrusted não comanda"** (P4 / ADR-005) — uma autorização
com proveniência untrusted (o plano do LLM) não pode originar uma capability privilegiada.

**Porque não é código (a costura está pronta):** a barreira `NewProductionHardenedTaint` +
`ErrTaintGateInert` + `Monitor.HasActiveTaintGate` já existe e é fail-closed. Falta a **fonte real
do conjunto `Privileged`** — a lista autoritativa de que capabilities são privilegiadas — que
**AOS-183 deixa EM FALTA POR DESIGN**. Sem ela o conjunto é **vazio** e o gate arranca *wired-mas-inerte*
(declarado honestamente via `HasActiveTaintGate`, sem alegar endurecimento que não tem).

**Estado / risco real:** na config de referência **não há via de exploração** (PDP default-deny +
`Authority` vazia + a cláusula de taint presente no único cap de egress). O que fica falso é a
**alegação** do invariante de topo — não uma porta aberta. Registado em `REGISTO-Deferimentos.md`
como **DEF-604 / DEF-606 / DEF-808 / DEF-809** (todos *MITIGADO/DIFERIDO*, dono = Responsável de
Segurança, resolução = "conjunto `Privileged` real no ápice").

**O que precisamos de si:** o Responsável de Segurança **define a fonte autoritativa de capabilities
privilegiadas** (AOS-183) — que capabilities são privilegiadas, e de onde vem essa lista (bundle
assinado? classe? política?). Com ela ligada ao ápice, `HasActiveTaintGate` passa a verdadeiro e o
P4 fica **armado**, não só declarado.

---

### #6 — Aceitação formal da v1 (Carta §5) — *ratificação*

**O que é:** a **Definition of Done** da v1 (Carta §5) tem os seis critérios literalmente por marcar
`- [ ]`, incluindo **"D4 desbloqueado — pré-requisito da v1"** (emenda 1.1). A Carta é a **fonte única
e congelada** de "done"; por essa medida a v1 **não está aceite**, logo "production-ready" é prematuro.

**Porque não é código:** cinco dos seis critérios já têm o código feito (o nó corre, interface mínima,
cadeia de governança a mediar com as 5 negações provadas — AOS-161, gates fail-closed verdes). O que
falta é **não-código**: (a) fechar #1 (D4, o critério explícito), e (b) o **sign-off de Segurança e de
Arquitetura**, que a própria Carta (emenda 1.2) declara **pré-condição da aceitação da v1** e que fica
**por obter**.

**O que precisamos de si:** após #1 e #4, o **dono de produto ratifica** a §5 (marca os seis `[x]` com
evidência) e obtém o **sign-off escrito de Segurança + Arquitetura**. É um ato de governança, não um
commit.

---

## 2. Dependências (a ordem importa)

```
#1 Autoridade de Identidade (D4)  ──┐
   (provisionar issuer+custódia)    │
                                    ├──►  #6 Ratificação da v1 (Carta §5)
#4 Fonte Privileged (AOS-183)  ─────┘      (dono ratifica + sign-off Seg/Arq)
   (Segurança define a lista)
```

#6 **depende** de #1 (é um critério explícito da §5) e beneficia de #4 (o P4 armado sustenta o
sign-off de Segurança). #1 e #4 são **independentes entre si** e podem correr em paralelo — donos
diferentes (#1 = Produto/Plataforma + Segurança para a custódia; #4 = Segurança).

## 3. O pedido, em uma linha por bloqueio

- **#1** — Confirmar Opção A e **provisionar** o issuer real + custódia de chave (vault/HSM) + IdP humano.
- **#4** — Responsável de Segurança **define e liga** a fonte autoritativa do conjunto `Privileged` (AOS-183).
- **#6** — Após #1/#4, **ratificar** a Carta §5 e obter **sign-off** de Segurança + Arquitetura.

## 4. O que acontece entretanto

O nó continua **deployável e seguro em postura de produção fail-closed**: recusa arrancar sem
identidade endurecida, soberania forte, TLS declarado e — agora — custódia de KEK durável (#3); e
recusa dizer-se pronto sem o Vault operacional (#2). O que **não** faz até estes três fecharem é
**reivindicar** identidade não-forjável (nega tudo) ou o endurecimento P4 (declara-o inerte). A
honestidade da postura é o que sustenta que o gap é **fechável e delimitado**, não escondido.

---

*Referências: veredito e evidência da revisão em memória `prontidao-producao-revisao`;
[`D4-escalacao-autoridade-identidade.md`](D4-escalacao-autoridade-identidade.md);
`docs/governance/REGISTO-Deferimentos.md` (DEF-604/606/808/809); `specs/00_AOS_Carta.md` §5 (DoD) e
emendas 1.1/1.2.*
