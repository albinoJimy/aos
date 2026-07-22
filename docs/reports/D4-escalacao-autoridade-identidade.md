# Escalada D4 — Autoridade de Identidade (bloqueio não-técnico de PR-0.c)

| Campo | Valor |
|---|---|
| Data | 2026-07-22 |
| Origem | EPIC-14 (Integração e Composition-Root), Fase 5 / PR-0.c |
| Tipo | Decisão humana / provisionamento de infra — **não resolúvel por código** |
| Dono sugerido | Responsável de Segurança + Dono de Produto/Plataforma (identidade) |
| Tickets bloqueados | AOS-156 (espinha de token), AOS-160 (Authenticator ed25519), AOS-162 (4-eyes) |
| Estado do código | Enforcement de identidade **completo e provado**; identidade em si **demo-only self-minted** |
| Urgência | Bloqueia o *long-pole* do programa e o go-live com garantia de identidade; **não** bloqueia trabalho de código restante (não há) |

---

## 1. O que é o D4 (em uma frase)

**Quem — ou o quê — é a fonte de verdade da identidade no AOS**, de modo a que os tokens de
identidade não-humana (NHI) sejam **não-forjáveis em produção**. Hoje essa autoridade não
existe (nem IdP, nem allowlist de attestation de dispositivo, nem binding humano↔NHI reais),
e **isso é informação/infra organizacional em falta, não um problema de programação**.

## 2. Porque não é resolúvel por código

A espinha de token (AOS-156) exige que o *trust-anchor* do verifier seja a **chave pública de
um issuer real**, cuja chave de assinatura **vem do vault (EPIC-07)** e **nunca é controlada
pelo apex**. Se o apex controlasse a chave, seria *teatro criptográfico*: o sistema "verificaria"
tokens que ele próprio mintou, sem qualquer garantia real de não-forjabilidade. Logo o pré-requisito
é uma **cadeia de confiança ancorada fora do código** — decisão e provisionamento humanos.

## 3. Estado actual (o que JÁ funciona vs o que falta)

**A ESTRUTURA de enforcement de identidade está construída e provada** (não é o que falta):

| Peça | Ticket | Estado |
|---|---|---|
| O loop preenche `Call.Credential` (token NHI chega ao RM) | AOS-152 | ✅ Feito |
| `NewProductionSecure` recusa cadeia com stubs de identidade/egress | AOS-153 | ✅ Feito |
| Cadeia real de hooks (identity→…→egress) no ápice | AOS-154 | ✅ Feito |
| Guard-test: o RM de produção **nega** anónimo/raiz-forjada/taint/egress/scope | AOS-161 | ✅ Feito |
| Portas RT/RM + D-TAIL (WindowPort/Dispatcher/Compaction) | AOS-157 | ✅ Feito |

**O que falta é a IDENTIDADE em si — hoje "demo-only self-minted":**
- Em testes, um issuer de teste assina tokens com uma chave que o teste controla.
- Em **produção**, o default (`identity.NewVerifier()` **sem** trust-anchors) **nega toda a NHI
  fail-closed** ⇒ na postura de produção, **cada tool call é negada** (anónimo → deny).
- Consequência: o sistema está **seguro mas inerte** no eixo de identidade — não permite
  nenhuma acção externa real. **Não se reivindica não-forjabilidade** (e não se deve).

## 4. O que está bloqueado (e o efeito de cada bloqueio)

| Ticket | O que faz | Sem D4 |
|---|---|---|
| **AOS-156** (L, P1) | Issuer com chave do vault + estágio authn (AOS-057) minta o NHI a montante do ScopeGate; verifier trust-anchor = pubkey do issuer | Não há issuer real; identidade fica demo-only |
| **AOS-160** (M) | `Authenticator` ed25519 de produção (SteerChannel) + nonce-store durável — substitui o HMAC demo-grade | Depende da identidade real (AOS-156) |
| **AOS-162** (M) | 4-eyes real + runtime da fronteira de assinatura (liga à EPIC-13, AOS-132/138); attestation de dispositivo | Depende da espinha de token (AOS-156) |

Nota de cadeia: **AOS-160 e AOS-162 dependem de AOS-156**, que depende do D4. Resolver o D4
desbloqueia os três em sequência. A EPIC-13 (frontend: assinatura/4-eyes) também depende
deste eixo.

## 5. A decisão concreta pedida ao dono (o *ask*)

Para fechar o D4 é preciso **decidir e provisionar** quatro coisas:

1. **IdP (fonte de identidade humana).** Que Identity Provider ancora a identidade humana
   (SSO/OIDC corporativo? IdP interno?), e qual o contrato de integração do **estágio authn
   (AOS-057)** que minta o NHI a partir do humano autenticado.
2. **Política de attestation de dispositivo (allowlist AAGUID).** Que autenticadores de
   hardware são confiáveis para o humano na raiz da delegação (WebAuthn/FIDO2 AAGUID). Liga-se
   ao ADR-016 (fronteira de confiança da UI) e ao AOS-162.
3. **Modelo de binding humano↔NHI.** O processo/autoridade pelo qual um humano cria/autoriza uma
   NHI (agente) e esse binding fica registado — é a **raiz humana da cadeia de delegação** (ADR-003).
4. **Custódia da chave do issuer (EPIC-07 / vault).** Onde vive a chave de assinatura do issuer e
   quem tem custódia — **nunca controlada pelo apex**, nunca em código/log.

## 6. Risco de NÃO decidir

- **PR-0.c fica em identidade demo-only**; a produção permanece fail-closed-inerte (nenhuma
  tool call permitida) — segura, mas **não-funcional para cargas reais**.
- **AOS-156/160/162 não fecham**; a EPIC-13 (4-eyes/fronteira de assinatura, AOS-132/138)
  **fica bloqueada** (depende da espinha de token).
- Qualquer deployment que reivindicasse garantia de identidade seria **falso** — o guardrail
  ("marcar demo-only, não reivindicar não-forjabilidade") impede essa reivindicação, mas ao
  custo de **bloquear o go-live com identidade real**.

## 7. Opções para o dono

| Opção | O que implica | Resultado |
|---|---|---|
| **A — Provisionar a autoridade de identidade** | IdP + AAGUID + binding humano↔NHI + custódia de chave no vault | Desbloqueia AOS-156→160→162; identidade real não-forjável; go-live possível |
| **B — Aceitar demo-only num escopo limitado** | Piloto interno/não-produção, com o caveat explícito de não-forjabilidade | PR-0.c "feito até onde o código permite"; go-live com identidade real fica adiado |
| **C — Stopgap interno** | IdP interno mínimo + custódia de chave de hardware como ponte | Desbloqueio parcial; caminho incremental para A |

**Recomendação técnica:** a decisão é de **produto/segurança**, não de engenharia. Enquanto A/C
não acontecem, a postura correcta já está em vigor (fail-closed + marcação demo-only). O
próximo passo **não é código** — é o dono do D4 escolher entre A/B/C e, se A/C, iniciar o
provisionamento (IdP, AAGUID, binding, vault).

## 8. Resumo de uma linha para a chefia

> O enforcement de identidade do AOS está construído e provado; falta a **autoridade de
> identidade** (IdP + attestation + binding humano↔NHI + chave no vault) — infra/decisão humana
> que só o dono pode provisionar. Até lá a identidade é demo-only e a produção é fail-closed
> inerte. Decidir A (provisionar), B (aceitar demo-only limitado) ou C (stopgap).

---

*Referências: EPIC-14 §AOS-156/160/162; ADR-003 (raiz humana da delegação), ADR-006 (identidade),
ADR-016 (fronteira de confiança da UI); EPIC-07 (vault); AOS-005 (identidade), AOS-057 (authn).
Estado do enforcement provado em AOS-152/153/154/161. Este documento é uma escalada de decisão —
não contém segredos nem material de chave.*
