# Leitura técnica do mérito — pendências da arbitragem §6.5

> **Estatuto deste documento — LER PRIMEIRO.** Isto é **input técnico não-vinculativo**, redigido
> pelo assistente (Claude), para acompanhar o [`DOSSIE-Arbitragem-6.5.md`](DOSSIE-Arbitragem-6.5.md).
> **Não é uma pronúncia do §6.5** — os árbitros são dois papéis humanos (Arquitecto de Plataforma +
> Responsável de Segurança) e a decisão é deles; a definição de «reaberta» é do dono (§8.4). O que
> aqui faço é uma coisa estreita e verificável: **o teste do §6.5 pergunta se existe um _facto novo
> verificável (código/build/painel)_ que não existia à data da FIXA** — e *facto de código* é
> precisamente o que posso confirmar. Confirmo os factos e dou uma **recomendação de mérito**;
> aceitá-la, temperá-la ou recusá-la é dos árbitros. Onde este documento e a Carta divergirem, a
> Carta manda.

> **Distinção que atravessa tudo (não a percam):** o §6.6 tem **duas** pernas de disparo — (a) **≥2
> FIXAS reabertas** e (b) **≥2 invocações do §6.4 recusadas como re-litígio**. O **mérito** que
> avalio abaixo só toca a perna **(b)**: mérito forte ⇒ não é re-litígio ⇒ não alimenta (b). A perna
> **(a)** conta o **acto de tocar a linha da FIXA**, independentemente do mérito — uma reabertura
> meritória **continua a contar como reabertura**. Logo, mesmo que o mérito das quatro seja bom, a
> perna (a) continua a depender só da definição de «reaberta» do dono. **Mérito bom protege contra
> (b), não contra (a).**

---

## Resumo (a minha recomendação, numa tabela)

| Pendência | Facto novo verificável? | Força do mérito | Recomendação de mérito (não-vinculativa) |
|---|---|---|---|
| REG-008 — ADR-017 excepção zero-dep | **SIM, forte** (verificado em código) | **Alta** | **Dívida-escondida** — invariante do nó literalmente intacto |
| REG-010 — ADR-016 não-repúdio/identidade | **SIM** + facto adicional: **já realizada** | **Alta** | **Dívida-escondida** e, hoje, **em larga medida cumprida** |
| REG-005 — D3 SSE stdlib | **SIM** | **Média-alta** | **Dívida-escondida** — reavaliada e endurecida, não revertida |
| REG-006 — D5 single-process | SIM (mesmo facto de D3) | **Média** | **Dívida-escondida**, mas foi a que **menos precisou** de reabrir |

**Nenhuma das quatro me parece re-litígio no mérito.** Isto, a ser aceite pelos árbitros, **zera a
perna (b)** do tripwire (0 recusas). A perna (a) fica na mão do dono (§8.4). Detalhe abaixo.

---

## REG-008 — ADR-017 ponto 1 (excepção escopada ao zero-dep) — **mérito mais forte, e a única REABERTURA certa**

**A FIXA:** «o binário do nó é zero-dep (stdlib + cedar-go)».

**Facto verificado (código, hoje):**
- `packages/cmd/aos/go.mod` — as **únicas** deps externas do binário do nó são `cedar-policy/cedar-go`
  e `golang.org/x/exp` (transitiva de cedar, indirect). **Zero** WebAuthn/CBOR.
- A lib da excepção (`go-webauthn` / `fxamacker/cbor`) aparece em **um só** `go.mod`:
  `packages/platform/attestation/go.mod` — o componente de autoridade de identidade **externo**, tal
  como a emenda 1.3 a escopou.
- `packages/integration/dep_isolation_test.go` — guard-test que **prova** que o nó não ganha
  transitivamente as deps da attestation.

**Leitura:** o **invariante normativo** de ADR-017 ponto 1 mantém-se **literalmente verdadeiro** no
artefacto que a decisão governa (o binário). O facto novo — «a frente 4 (attestation WebAuthn/AAGUID)
não é implementável só com a stdlib» — é real e verificável. No mérito, é o caso mais limpo de
**dívida-escondida**: não se re-litigou a regra por preferência; escopou-se uma excepção fora do
artefacto protegido, com o invariante intacto e sob gates (sca/govulncheck, `go.sum` pinado, SBOM).

**O que o mérito NÃO cura (e não me compete curar):** REG-008 **editou a própria linha da FIXA** no
§4.1 (`-`/`+` no diff, `a16c0b6`) — é **REABERTURA** na 1.ª perna, sem dúvida, e conta para a perna
(a) do §6.6 **haja mérito ou não**. E a excepção a uma FIXA de **supply-chain** foi aprovada **sem o
Responsável de Segurança**, que é um dos dois árbitros obrigatórios. Mérito forte **não substitui** a
assinatura em falta — só torna provável que, quando ela vier, seja de legitimação e não de recusa.

---

## REG-010 — ADR-016 (não-repúdio HITL / identidade fim-a-fim) — **o item onde o código diz mais**

**A FIXA:** «fronteira de confiança da UI — BFF non-signing, WYSIWYS, 4-eyes».

**O que a emenda 1.2 fez:** declarou não-repúdio HITL e identidade fim-a-fim **DEFERIDOS com D4**;
aprovação «demo-grade» até AOS-160/AOS-162.

**Facto verificado (código, hoje) — e é o essencial:** a razão do deferimento era temporal (D4 não
estava desbloqueado). Essa condição **mudou** e é observável no `bootstrap.go` do nó:
- `NewVerifierFromAuthority` (linha 728) — **verifier REAL** do trust anchor AOS-156, nunca o stub.
- `NewEd25519Authenticator` (linha 745) — canal de controlo com **assinatura ed25519 real + anti-
  replay durável + frescura**; o banner declara **«HMACAuthenticator demo DESLIGADO»** (linha 992).
- `NewFourEyesGate` (linha 764) — **4-eyes atestado** (AOS-162), composto quando há aprovadores.
- E, desta série de trabalho, **AOS-205** acrescentou **credencial forte OIDC** no read-path (o header
  auto-declarado deixou de autorizar sozinho).

**Leitura:** o deferimento temporal foi **dívida-escondida legítima** (D4 genuinamente não estava
pronto), **e** a garantia do ADR-016 está **hoje em larga medida realizada** — a aprovação já não é
«demo-grade»: é ed25519 real + 4-eyes + identidade real. **Recomendo que o árbitro não trate REG-010
como uma garantia ainda suspensa, mas como uma que foi deferida e depois cumprida**, com **residuais
nomeados e deferidos**: attestation física WebAuthn/AAGUID (externa, emenda 1.3) e o tenant IdP
concreto (DEF-212/213, infra-org). É o item onde «reavaliar» tem hoje uma resposta factual, não uma
promessa.

---

## REG-005 — D3 (transporte SSE stdlib) — **reavaliada e endurecida**

**A FIXA:** «Transporte SSE stdlib (não gRPC/WS/GraphQL)».

**Facto verificado (código, hoje):** `packages/cmd/aos/trajectory.go` implementa a SSE de trajectória
com **RESUME-FROM-SEQ** (header `Last-Event-ID`), **backfill**, **dedup/sem-lacuna** na fronteira
backfill→live e **backpressure** (AOS-167, EPIC-15); e o transporte corre agora **sobre TLS** (AOS-209).

**Leitura:** o facto novo — «o nó é um serviço de rede, não um BFF-atrás-de-SPA» — é real, e a decisão
D3 foi **re-examinada contra ele e sobreviveu endurecida**, não revertida. Isto é o oposto de
re-litígio-por-preferência: houve um artefacto novo, aplicou-se-lhe a regra, e a regra aguentou com
reforço verificável. **Mérito de dívida-escondida defensável.** Ressalva menor: a força depende de o
árbitro aceitar que «endurecer a implementação de uma FIXA sob um artefacto novo» é reavaliação de
contexto e não uma segunda decisão — mas o texto da decisão (SSE stdlib) nunca mudou.

---

## REG-006 — D5 (BFF single-process) — **a que menos precisou de reabrir**

**A FIXA:** «BFF single-process» — postura FIXA com **gatilho de graduação CONDICIONAL** (SLO/
utilizadores).

**Facto verificado:** o nó da v1 é **single-process**; o **gatilho condicional não foi accionado**
(não há o facto SLO/utilizadores que o §6.2 exige para «abrir»).

**Leitura:** o mesmo facto novo de D3 (nó-serviço de rede) aplica-se, mas **D5 já trazia o seu próprio
mecanismo de evolução** — o gatilho condicional. A decisão não mudou e o gatilho não disparou, pelo
que a «reavaliação» **confirmou a postura sem a alterar**. É **dívida-escondida** no sentido fraco:
nada a forçou. **A minha recomendação técnica é a mais cautelosa das quatro** — o veredicto podia
legitimamente ser «não era preciso reabrir; a evolução de D5 é pelo gatilho §6.2, não por emenda», o
que a manteria FIXA sem sequer a classificar como reavaliação. Deixo aos árbitros: é a única onde eu
próprio hesitaria entre «dívida-escondida trivial» e «não-evento».

---

## Síntese para os árbitros (o que a minha leitura sugere e o que fica fora do meu alcance)

1. **No mérito**, as quatro assentam em facto novo verificável (o artefacto «nó-serviço deployável»
   e, para ADR-017, a impossibilidade de attestation só-stdlib). Recomendo, como input, **legitimar
   as quatro como dívida-escondida** — com REG-010 anotada como **já cumprida** e REG-006 como a mais
   fraca (possível «não-evento»). Se aceite, **a perna (b) do §6.6 fica a zero.**
2. **Fora do meu alcance — e que a minha recomendação não toca:**
   - A perna **(a)** do §6.6 (≥2 reaberturas) conta o **acto**, não o mérito. REG-008 já é 1. Se o
     dono definir «reaberta» em sentido lato (§8.4), D3/D5/ADR-016 juntam-se e são **4** ⇒ Carta
     revista na raiz. **Isto não depende do mérito que avaliei.**
   - As **assinaturas dos dois papéis** e o **sign-off de v1 (§5)** continuam por obter. Mérito forte
     não as fabrica — só as antecipa.
3. **Uma nota de honestidade sobre o meu próprio papel:** eu escrevi ou verifiquei muito do código que
   agora cito como «facto novo». Isso torna-me uma **fonte de facto**, não um árbitro — e é mais uma
   razão para os dois papéis confirmarem os factos por si (os comandos estão no dossiê e acima) em vez
   de tomarem a minha leitura como pronúncia.

*Documento de apoio, não-vinculativo. Não altera a Carta nem o registo. As pronúncias do §6.5 entram
por commit próprio, com autoria dos dois papéis humanos.*
