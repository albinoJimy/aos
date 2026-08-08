# Desafio ao plano A6 — Attestation WebAuthn (AOS-177 / achado F10)

> **O que é:** avaliação adversarial das alegações sobre attestation em
> [`prontidao-modelos-agenticos.md`](./prontidao-modelos-agenticos.md). Sexto e último da
> série do Grupo A, depois de [A1](./desafio-A1-budget-admission-control.md),
> [A2](./desafio-A2-progress-surface.md), [A3](./desafio-A3-credential-broker.md),
> [A4](./desafio-A4-orquestrador.md) e [A5](./desafio-A5-escalonador.md).
>
> **Data:** 2026-08-08 · **HEAD avaliado:** `2e4f274` (branch `feature/AOS-128-ux-dx-tests`)

## Nota de alvo: o subtópico «A6» já não existe

O documento foi reestruturado. Não há `#### A6`; as alegações sobre attestation vivem em
três sítios, e foi esse conjunto que se desafiou: a **linha 94** (tabela do Grupo A), o
achado **F10** (linha 133) e a **acção de fecho** (linha 182).

## Veredicto: o documento está certo, e nenhum achado sobreviveu acima de «baixa»

É o primeiro da série com este desfecho, e regista-se sem enfeitar. O gap está
**correctamente nomeado**: a attestation prova hoje **modelo + posse** do dispositivo, e
falta **frescura** (liveness) e **atribuição** (dispositivo↔aprovador). O verificador **já
está ligado** por `AOS_ATTESTATION_VERIFIER_URL` (`bootstrap.go:961-969`, fail-closed no
arranque) e empacotado no dev-hardened.

O que falta ao texto é precisão em dois pontos — e um deles importa.

O céptico **refutou quatro achados** por inflação de segurança, incluindo dois que a
primeira leitura tornaria manchete. Ficam registados no fim.

---

## O ponto que importa: «replay possível» é impreciso

O F10 diz *«sem liveness (replay possível)»*. São **duas camadas distintas**, e o próprio
código já as separa melhor do que o relatório:

**Anti-replay do challenge — EXISTE e está SEMPRE ligado.** `bootstrap.go:971` compõe o
gate com `hitl.NewEventStoreNonceStore(es)` **incondicionalmente**; `foureyes.go:558`
consome cada challenge uma vez no scope `"4eyes:"+RequestID`. Reapresentar a mesma prova no
mesmo pedido devolve `ErrReplayedChallenge`. **Replay por observador passivo é falso hoje.**

**Frescura server-side — FALTA, e está deferida.** O challenge é escolhido pelo *cliente*;
`checkIssued` (`foureyes.go:491-503`) só exige emissão quando `g.issuance != nil`, e o nó
nunca chama `WithChallengeIssuance`. O emissor durável existe (`challenge_issuer.go:104`)
com zero chamadores, e não há endpoint de emissão.

**Contra que adversário o replay é verdade:** apenas contra quem **já detém a chave ed25519
de um aprovador**. Como a assinatura da perna cobre o `request_id` (`foureyes.go:583-598`),
um `request_id` novo reabre o espaço de nonces e permite reapresentar uma attestation
capturada.

> E aqui está a razão de a severidade ser baixa: **quem detém essa chave já forja a perna**,
> com ou sem attestation. A frescura ausente **não cria** um bypass novo do four-eyes —
> apenas **não acrescenta** o segundo factor físico anti-clonagem que a porta existe para
> dar. O four-eyes continua a exigir duas assinaturas de aprovadores distintos.

O código de `foureyes.go:182-187` já documenta exactamente esta distinção («sem
`WithChallengeIssuance`: só anti-replay de uso-único durável; com: issue-then-consume
real»). **O relatório é menos preciso do que o código que descreve.**

Correcção proposta ao F10: trocar «replay possível» por «falta frescura server-side», e
nomear o adversário.

## A segunda metade: atribuição

`verifyDevice` só cruza `deviceID ↔ aprovador` quando `g.enrollment != nil`
(`foureyes.go:531-538`), e o nó nunca passa `WithDeviceEnrollment`. Confirmado. No
dev-hardened a attestation está **enforçada sem enrollment**, pelo que o factor de
dispositivo vale hoje *«duas credenciais atestadas distintas»*, não *«o dispositivo de X»*.

Está correctamente declarado nos três sítios do documento. Não é garantia anunciada e não
imposta — é deferimento com dono (AOS-266, `EPIC-20:470-483`, aberto).

---

## O que o plano de fecho esconde

O rótulo «pequeno-médio / Médio» subestima **só a metade da liveness**. Duas peças que as
duas linhas-resumo do relatório não mencionam:

1. **O endpoint de emissão não existe.** `api.go:475-506` regista `/approve`, não
   `/challenge`. «Wiring de `ChallengeIssuance`» esconde um endpoint novo + o contrato de
   cerimónia (o aprovador passa a consumir um challenge emitido em vez de o derivar).
   *Mitigação já existente:* o `aos-issuer approve-sign` aceita `--challenge` explícito
   (`approvesign.go:133-143`) — derivar é só o default, não é preciso reescrever o cliente.

2. **A captura do `deviceID` não é escrita à mão.** É um digest opaco
   `SHA256(domínio‖AAGUID‖credID)` que só o componente remoto produz. É obtenível — `/verify`
   devolve `device_id` — mas falta a cerimónia empacotada de *enroll*. Pior para os testes:
   no dev-hardened o `/synth` cunha um `credID` aleatório a cada chamada, logo **o deviceID
   não é estável** e a atribuição não é testável ponta-a-ponta em dev sem fixar o credID.

### ⚠️ Aviso de sequência (fail-closed, não bypass)

Compor `WithChallengeIssuance` **sem** o endpoint de emissão faz `checkIssued` negar **toda**
a perna com `ErrChallengeNotIssued` (`foureyes.go:499`). Não é um bypass — nega, não concede
— mas parte todas as aprovações. Os passos emissão → composição → cerimónia são
**indivisíveis**.

---

## Uma nota de defesa-em-profundidade

O veredicto do componente de attestation vem **base64 nu, sem assinatura**
(`remote_attestation.go:101-121`), autenticado apenas pelo canal TLS. É o único verificador
externo do nó cujo «sim» não tem barreira criptográfica além do transporte — contraste com o
OIDC (JWT verificado por assinatura) e o Vault (GCM não desembrulha com KEK errada).

Não é inconsistência com o ADR-016 §2 (que é WYSIWYS sobre o *efeito*, coberto pela
assinatura do humano) nem resíduo não-declarado — o componente é autoridade externa
designada, com disciplina https-ou-loopback fail-closed. Mas comprometer só a chave TLS de
servidor do componente forjaria o veredicto. Vale registar; não é bloqueante.

---

## Decisões que são do dono (não minhas)

**(i) Liveness real vale para a v1 single-node?**
*Opção A — ficar no consumo-único:* custo zero, e o vector residual exige um adversário que
já detém uma chave de aprovador. *Opção B — ligar `ChallengeIssuance` (AOS-266):* fecha o
vector, mas exige endpoint + estado durável através de suspend/resume + contrato de
cerimónia novo.
**Recomendação:** A é **defensável para a v1**, desde que o gap fique escrito com o
adversário nomeado. B é o caminho certo para autenticadores físicos reais — e deve ser feita
**junto com o enrollment**, porque o segundo factor só vale com atribuição.

**(ii) O veredicto do componente deve ser assinado?**
*A — manter só-transporte* (coerente com tratar o componente como autoridade externa);
*B — o componente assina o `device_id`* com uma chave cuja pública o nó fixa, à imagem do
JWT do OIDC. O CBOR continua fora do nó; só o veredicto passa a ser assinado — compatível
com o zero-dep.
**Recomendação:** B, como melhoria de defesa-em-profundidade e **não** bloqueante — alinha a
attestation com a disciplina já aplicada ao OIDC e ao Vault.

---

## Plano F10 revisto

A mudança principal é **desagregar** as duas metades, que têm custos diferentes.

| # | Passo | Depende de | Esforço | Face ao documento |
|---|---|---|---|---|
| **A1** | Endpoint de emissão (`POST /runs/{id}/challenge`) → `IssueChallenge` com TTL | — | médio | **NOVO** — não existe; o «wiring» escondia-o |
| **A2** | Compor `WithChallengeIssuance` no gate | A1 | pequeno | ⚠️ **indivisível de A1** — sozinho nega tudo |
| **A3** | Cerimónia: consumir o challenge emitido (`--challenge` já existe) | A2 | pequeno | Menor do que parecia |
| **B1** | `AOS_DEVICE_ENROLLMENT_FILE` + loader fail-loud (molde AOS-193, **não** descartar em silêncio) | — | pequeno-médio | Igual |
| **B2** | Compor `WithDeviceEnrollment` | B1 | pequeno | Igual |
| **B3** | Cerimónia de captura do `deviceID` (correr `/verify` uma vez por autenticador) | B1 | pequeno | **NOVO** — «um ficheiro» escondia-o |
| **B4** | Fixar o `credID` no `/synth` de dev, senão a atribuição não é testável | B3 | pequeno | **NOVO** |
| **C** | Banner LIGADA/DORMENTE | A2, B2 | trivial | Tem de **seguir** o wiring, nunca precedê-lo |

**Nota importante:** o ticket que conduz o trabalho — **AOS-266** — **já tem** estas
acceptance criteria explícitas (emissão com TTL, enrollment por ficheiro, banner
LIGADA/DORMENTE). O sub-dimensionamento vive nas **duas linhas-resumo** do relatório
(L94/L182), não no plano do ticket.

## Sem tickets novos

Pela primeira vez na série, **esta avaliação não abre nenhum ticket**: não há defeito vivo.
Tudo o que se encontrou é deferimento declarado com dono, ou precisão de redacção.

---

## O que foi REFUTADO

- **«Attestation enforçada sem enrollment abre um ataque novo no dev-hardened.»** As
  citações estão certas, mas o «ataque» exige forjar a perna do aprovador X — o que exige a
  chave privada dele, que sozinha já forja a perna. Sem bypass novo.
- **«O TTL de 5 min colide com suspend/resume do HITL.»** Fabrica risco a partir de um
  desenho ingénuo ainda não construído. O TTL é parâmetro do construtor e o docstring já
  declara «o tempo de uma cerimónia, não uma sessão» — emitir à submissão, não à escalada.
- **«O fix parte todas as aprovações» como severidade média.** Só se alguém meio-implementar.
  Rebaixado a nota de sequência.
- **«Inconsistência com o ADR-016 §2.»** §2 é WYSIWYS sobre o efeito, não sobre o veredicto
  `device_id`. Sobrevive só como nota de defesa-em-profundidade.

---

## Ver também

- [`desafio-A1-budget-admission-control.md`](./desafio-A1-budget-admission-control.md)
- [`desafio-A2-progress-surface.md`](./desafio-A2-progress-surface.md)
- [`desafio-A3-credential-broker.md`](./desafio-A3-credential-broker.md)
- [`desafio-A4-orquestrador.md`](./desafio-A4-orquestrador.md)
- [`desafio-A5-escalonador.md`](./desafio-A5-escalonador.md)

## Rastreabilidade

Transcrições por agente: `.claude/projects/…/subagents/workflows/wf_9faacb5b-9e7/journal.jsonl`
(13 agentes, 0 erros). Verifiquei à mão o anti-replay incondicional (`bootstrap.go:971`,
`foureyes.go:558`) e a atribuição condicionada a `enrollment != nil` (`foureyes.go:531`).

> **Nota de âmbito:** este relatório NÃO altera `prontidao-modelos-agenticos.md`. As
> correcções propostas ao texto do F10 estão acima, para quem o escreveu decidir.
