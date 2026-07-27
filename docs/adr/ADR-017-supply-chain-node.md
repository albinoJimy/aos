# ADR-017 — Fronteira de supply-chain do nó e da distribuição

| Campo | Valor |
|---|---|
| **ADR** | 017 |
| **Título** | Fronteira de supply-chain do nó `aos` e da sua distribuição |
| **Estado** | Aceite |
| **Data** | 2026-07-22 |
| **Deciders** | Equipa AOS |
| **Contexto-fonte** | Painel adversarial `wamnbffrk` (achado: ADR-017 aberto antes de empacotar o nó — AOS-168); `specs/00_AOS_Carta.md §4.1/§7 (emenda 1.2)`; EPIC-05 (Registry/Supply-chain), EPIC-10 (Topologia/entrega), ADR-011 (policy-as-code), ADR-016 (fronteira de confiança da UI); baseline SAST/SCA em `scripts/ci/baseline/` |

## Contexto

Até agora o AOS impôs supply-chain **para dentro** (o que o agente executa): o Registry
(EPIC-05) é um catálogo append-only, versionado e assinado (`id`, `version`, `digest`,
`signature`, `provenance`, `status`); a revalidação por chamada (AOS-051) apanha rug-pulls; a
dependência externa Go é **cedar-go apenas** (build offline, zero-dep); e os gates `sast.sh`/
`sca.sh` correm com uma baseline multiset documentada. Faltava a fronteira **para fora**: quando
o nó `aos` (EPIC-15/AOS-168) passa a ser **distribuível** (um contentor que se entrega e corre),
a sua própria cadeia de fornecimento — imagem, binário, proveniência — torna-se superfície de
ataque. Empacotar o nó sem uma fronteira explícita reabsorveria para a distribuição o rigor que
o REG mantém para as tools.

## Decisão

A supply-chain do **próprio nó e da sua distribuição** obedece às mesmas invariantes que o AOS
impõe às tools, materializadas na entrega:

1. **Binário reprodutível, zero-dep externa.** O nó compila estático (CGO off), só stdlib +
   cedar-go; o build é reprodutível offline (`GOPROXY=off`, `go.sum` pinado). Nenhuma dependência
   de runtime na imagem.
2. **Imagem endurecida (coerente com AOS-168).** Contentor **distroless, non-root, root-fs
   read-only**, sem shell nem package-manager; superfície mínima. Config por env/ficheiro montado —
   **sem segredos na imagem** (as chaves vêm do vault/KeyVault em runtime, ADR-006).
3. **Proveniência e SBOM assinados e verificáveis.** A entrega produz um **SBOM** e uma
   **atestação de proveniência** (quem/o quê/quando a produziu), **assinados** — a mesma exigência
   de `provenance`+`signature` que o REG faz a uma tool, agora aplicada ao artefacto do nó.
   **Entregue por AOS-207** (deixou de estar «na forma mínima»):
   - **Formato:** envelope **DSSE v1** (`payloadType application/vnd.in-toto+json`) sobre um
     **in-toto Statement v1**. Os *subjects* assinados são o **digest da imagem**, o binário que
     ela carrega, o `sbom.json`, o `provenance.json` e o **manifesto de entrega**.
   - **Primitiva: `crypto/ed25519` da stdlib**, não cosign/sigstore — decisão declarada, com o
     custo em §Consequências e a matriz comparativa em `deploy/node/CUSTODIA-CHAVE-RELEASE.md §0`.
     O assinador (`scripts/ci/attest`) é um passo de **entrega (CI)**: não entra no binário do nó,
     não é importado por `packages/**` e não tem dependência externa — logo **não** consome a
     excepção escopada da emenda 1.3 da Carta.
   - **Recusa, não aviso:** `scripts/ci/verify-attestation.sh` valida a assinatura contra a âncora
     de confiança e **recompara cada digest assinado com o artefacto real**; divergência ⇒
     entrega **bloqueada**. Sem chave de release (PR/local) a atestação fica `NAO-ASSINADA` e a
     entrega é **verde parcial — não publicável**, nunca verde.
   - **Custódia:** a chave privada vive **fora do repositório** e entra por **caminho**
     (`AOS_RELEASE_KEY_FILE`); a pública é versionada em `deploy/node/release-pubkeys.json`.
     Ver o ponto 5 e `deploy/node/CUSTODIA-CHAVE-RELEASE.md`.
4. **Gates fail-closed na entrega.** `sast.sh`/`sca.sh` (baseline multiset, nunca `sort -u`) e o
   gate de segredos correm na cadeia de entrega; uma descoberta NOVA fora da baseline avermelha
   (fail-closed), como no resto do programa.
5. **Cada domínio de assinatura tem custódia PRÓPRIA.** O issuer de identidade (AOS-156,
   self-hosted Nível 2) é um artefacto/trust-domain distinto do nó — a sua chave e distribuição têm
   custódia própria (nunca na imagem do nó). **A imagem do nó passou a ter o equivalente**
   (AOS-207): a chave de **release** é um terceiro trust-domain, com detentor, cofre, janela de
   validade, rotação por sobreposição e revogação documentados em
   `deploy/node/CUSTODIA-CHAVE-RELEASE.md` — antes não existia procedimento nenhum. As duas chaves
   **não** se substituem: assinar a imagem não emite identidade, emitir identidade não atesta o
   artefacto. Endurecimento posterior (por fazer, ver §Consequências): HSM/sign-in-place,
   attestation de imagem por registry assinado.

## Consequências

- **Positivas:** o artefacto do nó é tão auditável quanto uma tool do REG; a fronteira para-fora
  fica explícita ANTES de o nó ser empacotado (fecha o achado do painel); nenhuma dependência
  externa nova entra sem passar pelos gates.

### Recalibração do risco residual (AOS-207)

**Histórico do eixo — porque estava errado.** Esta secção admitia entregar o ponto 3 «na forma
mínima (SBOM gerado, atestação **por assinar**)» e diferia a infra de assinatura para «o
endurecimento de EPIC-10». **AOS-196 (achado DEF-06)** mostrou que o eixo não existia: **nenhum**
dos onze tickets do EPIC-10 assina imagens (AOS-098 IaC, 099 workers, 100 replicação, 101 backup,
102 DR, 103 microVMs, 104/105/106 dashboards/alertas/runbooks, 107 escala, 108 hipercare), e
passou a linha `DEF-501` a **POR ATRIBUIR**. **AOS-207** é o eixo real e fecha-a.

**O que passa a estar ENTREGUE e IMPOSTO** (não «na forma mínima»):

| Garantia | Onde é imposta |
|---|---|
| Atestação **assinada** (DSSE v1 + in-toto Statement v1, ed25519) | `scripts/ci/sign.sh` + `scripts/ci/attest/` |
| A entrega **recusa** o que não valida: assinatura, confiança, digests dos ficheiros vs. realidade, coerência do manifesto | `scripts/ci/verify-attestation.sh` (saída 1) |
| A atestação **tem de cobrir a imagem**: um envelope sem subject `image:` **não sai verde** | `verify-attestation.sh` (saída 4 se declarado; **saída 1** se o manifesto afirmar cobertura que o statement assinado não tem) |
| O digest da imagem **só conta como verificado quando é recomparado com a imagem real**; imagem ausente ⇒ **saída 4** (por verificar), nunca 0 | `verify-attestation.sh` + `package.sh` (saída 3) |
| Chave privada **fora do repositório** — invariante **imposta**, não recomendada | `aos-attest keygen` recusa escrever dentro de uma árvore git |
| Âncora de confiança revista em código (keyid recalculado, estados, janelas, revogação) | `deploy/node/release-pubkeys.json` + `verify` |
| Custódia: quem assina, onde vive, como se roda/revoga | `deploy/node/CUSTODIA-CHAVE-RELEASE.md` |
| Falha honesta sem chave: `NAO-ASSINADA` ⇒ **verde parcial, não publicável** | `package.sh` (saída 3) |
| Um skip declarado num gate **filho** chega ao veredicto do **pai** (fronteira de processo) | sink `AOS_SKIP_SINK` em `sbom.sh`/`sign.sh`/`verify-attestation.sh`, reabsorvido por `package.sh` |

**Qualificação honesta da linha «recusa».** A recomparação com a **realidade** é integral para os
ficheiros (binário, SBOM, proveniência, manifesto) e **condicional para a imagem**: exige `docker`
e a imagem presente no host que verifica. Quando não está, o gate **não finge** — declara o que não
recomputou e devolve **4** (⇒ verde parcial, não publicável). Uma versão anterior deste ADR não
qualificava esta linha, e o gate contava o subject da imagem como «bate com a realidade» mesmo sem
a ter comparado: afirmação a mais, corrigida (ver residual 8).

**O que FICA por fazer — residual NOMEADO, com eixo, não diferido para um epic vago:**

1. **A assinatura não está ANEXADA à imagem no registry** (OCI referrers / `cosign attach`). Quem
   faz `docker pull` não traz a atestação consigo e um *admission controller* de cluster continua
   sem material que consiga verificar. É o custo directo da escolha de ed25519-stdlib sobre
   cosign — mitigado no **formato** (o payload é o mesmo que o cosign embrulha), não anulado.
2. **Sem log de transparência (Rekor).** Não há prova de terceiro sobre *quando* se assinou; um
   detentor com acesso ao cofre pode re-assinar retroactivamente sem rasto externo.
3. **Chave de software, não HSM.** A seed existe em ficheiro montado; a forma mais forte
   (gerar/assinar **dentro** do HSM/KMS) exige um caminho `sign` remoto que ainda não existe — o
   envelope já o suporta, basta trocar quem produz os 64 bytes.
4. **Sem `push`, o subject da imagem é o digest de configuração local** (`.Id`), não o
   `repoDigest` que um registry serve. Declarado em `image.idNote`, não inventado.
5. **Um só signatário** — sem *m-de-n*; o dual-control vive no processo humano, não na criptografia.
6. **Attestation de hardware** (a garantia mais alta) continua fora de âmbito.

Os seis primeiros são **endurecimento**, não fingimento: cada um está também listado em
`deploy/node/CUSTODIA-CHAVE-RELEASE.md §6`, onde é operacionalmente accionável.

Os quatro seguintes vieram da **auditoria** desta entrega e ficam nomeados aqui — não no relatório
de quem auditou — porque o instrumento durável tem de registar o defeito, não só a garantia:

7. **A cablagem da CI quebra a proveniência assinada.** `.github/workflows/ci.yml` corre
   `scripts/ci/sbom.sh` como passo **separado, DEPOIS** de `scripts/ci/package.sh`; o `sbom.sh`
   reescreve `provenance.json` (que é um *subject* assinado), pelo que o envelope deixaria de
   cobrir o que se faz upload — e nenhum gate corre a seguir para o apanhar. **Defesa desta
   pista** (já em vigor): ao regenerar a proveniência, o `sbom.sh` **REMOVE** `attestation.dsse.json`
   e `delivery-manifest.json` obsoletos, degradando a entrega para *não-assinada* (recusada a
   jusante) em vez de a deixar mentir. **Remédio definitivo — eixo: dono de `ci.yml`:** remover o
   passo `Gate sbom` do job `delivery` (o `package.sh` já o encadeia) ou movê-lo para **antes**.
8. **A recomparação do digest da imagem com a imagem real é condicional** (exige `docker` + imagem
   presente). Corrigido o falso-verde — hoje a ausência dá **saída 4**, não 0, e a mensagem final
   distingue «recomputado contra a realidade» de «comparado com o manifesto» — mas a garantia
   continua a **não ser prestada** nesse caminho: uma imagem trocada sob a mesma tag só é
   detectada onde a imagem exista. **Eixo:** endurecimento futuro (verificação a partir do
   registry, ligada ao residual 1).
9. **STRIDE e RTM por reconciliar.** `tecnica/17_Analise_STRIDE.md` continua a declarar a
   assinatura da atestação como «deferida-com-eixo (AOS-207, DEF-501)» e `tecnica/16_Rastreabilidade_RTM.md`
   descreve o ADR-017 como «SBOM+proveniência». Depois desta entrega, as duas afirmações
   contradizem este ADR. **Eixo: dono de `tecnica/**`** (pista proibida a AOS-207).
10. **A cadeia assinada nunca correu com uma chave de release REAL.** `deploy/node/release-pubkeys.json`
    tem `keys: []` de propósito (fail-closed: com o roster vazio **qualquer** envelope é recusado,
    logo **nenhuma** entrega é publicável hoje). A prova end-to-end existe, mas foi feita com chave
    efémera. Consequência operacional a declarar: enquanto não houver `AOS_RELEASE_KEY_FILE`
    montada, o `package.sh` devolve **3** e a CI — que não distingue 3 de 1 — deixa o job
    `delivery` **vermelho** em push para `main`. É o comportamento pretendido (não se publica o
    que não está verificado), mas exige decisão do **dono de `ci.yml`**: provisionar a chave no job
    de release, ou tratar o 3 como «não-fatal mas não publicável».

**Pendência de governação:** fechar a linha `DEF-501` em `docs/governance/REGISTO-Deferimentos.md`
com eixo AOS-207 — ficheiro de outra pista, não alterado aqui.

## Estado na Carta

Registada como **FIXA** no registo de decisões da Carta (§4.1). É pré-condição de AOS-168
(empacotamento do nó): o nó não é distribuído antes de os pontos **1/2/3/4** estarem verdes — o
ponto 3 entrou nesta lista com AOS-207, quando deixou de ser «gerado, por assinar» e passou a ser
uma **recusa** executável (`scripts/ci/verify-attestation.sh`). Uma entrega **não-assinada** é
verde parcial e **não é publicável**.
