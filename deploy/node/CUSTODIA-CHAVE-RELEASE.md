# Custódia da chave de assinatura de release do nó `aos`

**Eixo:** AOS-207 — fecha o achado **DEF-06** e o **ponto 3 do ADR-017**, que a
§Consequências admitia entregar «na forma mínima (SBOM gerado, atestação **por assinar**)».

O **ponto 5 do ADR-017** já exigia custódia própria para a autoridade de **identidade** (o
issuer, trust-domain separado, chave nunca na imagem). A **imagem do nó** não tinha
equivalente: assinava-se nada e não havia procedimento. Este documento é esse equivalente —
**quem** assina, **onde** vive a chave, **como** se roda — e é a referência que
`scripts/ci/sign.sh` e `scripts/ci/verify-attestation.sh` citam quando recusam.

---

## 0. A decisão de ferramenta (e o que ela custa)

A assinatura da imagem é um passo de **entrega (CI)**, não código do nó: não entra no binário e
por isso **não consome** a excepção escopada da emenda 1.3 da Carta (essa é para o componente
**externo** de autoridade de identidade). Ainda assim, a escolha foi feita explicitamente:

| | (A) cosign/sigstore (externa) | **(B) ed25519 da stdlib — ESCOLHIDA** |
|---|---|---|
| Interoperabilidade | `cosign verify-attestation`, Kyverno, policy-controller consomem sem tradução | **só** `scripts/ci/verify-attestation.sh` verifica |
| Transparência | Rekor (log público, prova de *quando*) | **nenhuma** |
| Modelo de chave | keyless (cert efémero OIDC) ou chave em KMS | chave longa em cofre/HSM, custódia nossa (este documento) |
| Dependências | binário externo, que teria de ser ele próprio pinado por digest e verificado | **zero** — `crypto/ed25519`, compilado do próprio repo |
| Executável **aqui** | **não** (build offline, `GOPROXY=off`, sem rede para Rekor, sem cosign no PATH) | **sim** — a prova negativa corre no repositório |

**Porquê (B):** com (A), o gate ficaria permanentemente saltado neste ambiente e o DEF-06
fecharia com **outro** «declarado, não fingido» em vez de com uma garantia imposta. Um gate que
nunca corre não recusa nada.

**O custo de (B) está mitigado no formato, não escondido:** o que se assina é um **envelope
DSSE v1** com `payloadType = application/vnd.in-toto+json` e um **in-toto Statement v1** por
dentro — exactamente o envelope e o payload que o cosign usa para atestações. Migrar para
cosign é **re-embrulhar os mesmos bytes**, não remodelar a atestação.

**O que (B) não compra** (residual **nomeado** em ADR-017 §Consequências): assinatura anexada à
imagem no registry (OCI referrers) ⇒ um *admission controller* de cluster continua sem material
verificável; log de transparência; certificados efémeros; attestation de hardware.

---

## 1. QUEM assina

| Papel | Responsabilidade |
|---|---|
| **Arquitecto de Plataforma** (dono da linha `DEF-501` no registo de deferimentos) | Detentor da chave de release. Provisiona, roda e revoga. É quem consta de `holder` em `deploy/node/release-pubkeys.json`. |
| **Responsável de Segurança** | Co-aprova provisionamento, rotação e revogação. Nenhuma das três é acção de uma só pessoa. |
| **Runner de CI de release** | Executa `scripts/ci/sign.sh` com a chave **montada**, num job de release — nunca em CI de PR. |

**Ninguém assina a partir de uma estação de trabalho pessoal.** A assinatura é um passo do job
de release; um humano que assine à mão produz um artefacto que ninguém consegue reconstituir.

## 2. ONDE vive a chave

**Chave privada (seed ed25519, 32 bytes):**

- vive **fora do repositório**, num cofre — Azure Key Vault / HashiCorp Vault, coerente com o
  ADR-006 (segredos vêm do vault em runtime, nunca da imagem);
- é entregue ao job de release como **ficheiro montado read-only**, e o caminho é passado em
  `AOS_RELEASE_KEY_FILE`. **A variável transporta um CAMINHO, nunca o material** — o mesmo
  padrão já documentado para `AOS_ISSUER_KEY_PATH` no README deste directório;
- **nunca** entra: no repositório, na imagem do nó, em fixtures, em testes (os testes de
  `scripts/ci/attest` geram pares **efémeros** em `t.TempDir()`), nem no valor de qualquer
  variável de ambiente;
- o ficheiro montado é apagado com o runner efémero no fim do job.

Três defesas, não uma recomendação:

1. `aos-attest keygen` **RECUSA** escrever dentro de uma árvore git (sobe a hierarquia à procura
   de `.git`). Ver `TestAOS207KeygenRecusaDentroDoRepo`.
2. `scripts/ci/secrets.sh` avermelha qualquer bloco PEM de chave privada ou ficheiro
   `*.key`/`*.pem`/`*.p12`/`*.pfx` **rastreado** pelo git.
3. Nada em `scripts/ci/sign.sh` ecoa material privado: o que se imprime é o **keyid**, que é
   `sha256(chave pública)`.

**Chave pública:** vive **no repositório**, em `deploy/node/release-pubkeys.json` — é a âncora
de confiança da verificação. Material público, versionado de propósito: quem verifica tem de
poder ver, em revisão de código, **que chaves passaram a ser confiáveis e quando**.

## 3. COMO se provisiona

Hoje `deploy/node/release-pubkeys.json` tem `keys: []` — **nenhuma chave de release
provisionada**, e por isso a verificação **recusa** qualquer envelope (fail-closed por omissão,
`TestAOS207RegistoVazioRecusa`). O gatilho de provisionamento é o **primeiro release distribuído
fora do repositório**, exactamente como a linha `DEF-501` do registo já declarava.

```bash
# 1. Gerar o par NUM AMBIENTE EFÉMERO E FORA DE QUALQUER ÁRVORE GIT.
#    (Preferível: gerar dentro do HSM/KMS e exportar só a pública — ver §6.)
go build -o /tmp/aos-attest ./scripts/ci/attest
/tmp/aos-attest keygen \
  -out /run/secrets/aos-release.seed \
  -roster-entry /tmp/entrada.json \
  -holder  "Arquitecto de Plataforma — <nome>" \
  -custody "Azure Key Vault <cofre>/<segredo>, acesso co-aprovado com Segurança"

# 2. Depositar /run/secrets/aos-release.seed NO COFRE. Apagar a cópia local.
# 3. Acrescentar a entrada de /tmp/entrada.json ao array `keys` de
#    deploy/node/release-pubkeys.json, por PR revisto (é material público).
# 4. Confirmar que o gate verifica:
IMAGE_TAG=aos-node:local bash scripts/ci/package.sh
```

Campos obrigatórios da entrada (o verificador recusa o que não os respeite):

| Campo | Regra imposta |
|---|---|
| `keyid` | `sha256(publicKey)` em hex. O verificador **recalcula-o**: uma entrada cujo `keyid` não bate com o material é recusada como `INCOERENTE` (a confiança é na chave, não no rótulo). |
| `algorithm` | `ed25519` — único suportado; não há negociação nem *downgrade*. |
| `status` | `active` \| `rotated` \| `revoked`. Fora deste vocabulário ⇒ recusa. |
| `notBefore` | RFC3339. Verificar antes do início da janela ⇒ recusa. |
| `notAfter` | RFC3339. **Obrigatório** quando `status = rotated`: uma chave rodada sem fim de janela é uma chave activa com outro nome. |

## 4. COMO se roda

A rotação é **por sobreposição**, para que atestações antigas continuem verificáveis e não haja
janela em que nada verifique:

1. Provisionar a chave **nova** (§3) e acrescentá-la ao registo com `status: "active"` e
   `notBefore` = agora.
2. Na **mesma** alteração, passar a chave antiga a `status: "rotated"` com
   `notAfter` = agora + janela de sobreposição (recomendado: **30 dias**, ≥ o tempo de vida do
   release mais longo em circulação).
3. O job de release passa a montar a seed nova; a antiga fica no cofre, **sem acesso do runner**,
   até ao fim da janela.
4. Findo `notAfter`, a chave antiga deixa de verificar sozinha (`TestAOS207JanelaExpiradaERecusada`).
   Só então se destrói o material no cofre e se remove a entrada do registo.

**Cadência:** rotação **anual** de calendário, e **imediata** em qualquer um destes casos —
saída do detentor, suspeita de exposição do cofre, ou alteração do modelo de acesso ao runner de
release.

## 5. COMO se revoga (compromisso)

1. `status: "revoked"` na entrada da chave, com PR imediato. **A revogação vence a validade
   criptográfica**: um envelope assinado por chave revogada é recusado mesmo que a assinatura
   esteja matematicamente correcta, e **mesmo que outra assinatura válida co-assine o mesmo
   envelope** — co-assinar com material revogado é sinal de compromisso, não um detalhe a
   ignorar por haver quórum (`TestAOS207ChaveRevogadaERecusada`).
2. Destruir o material no cofre; auditar os acessos ao segredo.
3. Re-assinar e **re-publicar** os artefactos ainda em circulação com a chave nova.
4. Registar o incidente. A revogação **não** é reversível por edição: uma chave revogada não
   volta a `active` — provisiona-se uma nova.

## 6. Residual honesto desta custódia

O que está **entregue**: chave privada fora do repositório (invariante **imposta** pela
ferramenta, não recomendada), chave pública versionada e revista, keyid recalculado, estados e
janelas verificados, assinatura DSSE/ed25519 sobre um in-toto Statement, e uma entrega que
**recusa** o que não valida.

O que **não** está, e fica nomeado aqui e no ADR-017 §Consequências:

- **A chave é de software, não de HSM.** `keygen` produz uma seed em ficheiro. A forma mais
  forte — gerar e assinar **dentro** do HSM/KMS, com a privada a nunca existir em memória do
  runner — exige um caminho `sign` remoto que ainda não existe; o formato do envelope já o
  suporta (basta trocar quem produz os 64 bytes de assinatura).
- **A assinatura não está anexada à imagem no registry** (OCI referrers/`cosign attach`): quem
  faz `docker pull` não traz a atestação consigo, e um *admission controller* de cluster
  continua sem material verificável.
- **Não há log de transparência**: não existe prova de terceiro sobre *quando* se assinou; um
  detentor com acesso ao cofre pode re-assinar retroactivamente sem deixar rasto externo.
- **Sem `push`, o subject da imagem é o digest de configuração local** (`.Id`), não o digest de
  manifesto que um registry serve (`repoDigest`). O manifesto declara-o em `image.idNote` em vez
  de o inventar.
- **Um só signatário.** Não há *m-de-n* (threshold) na assinatura da atestação; o dual-control
  vive no processo humano (§1), não na criptografia. O verificador aceita quórum de 1 por
  desenho — mudar isso é alterar o `verify`, não o formato.
- **A comparação do digest da imagem com a imagem REAL exige a imagem no host que verifica.**
  Sem ela, `verify-attestation.sh` declara o que não recomputou e devolve **4** (não publicável) —
  já não devolve 0. Uma imagem trocada sob a mesma tag só é detectada onde a imagem exista.
- **Esta cadeia nunca correu com uma chave de release REAL.** `release-pubkeys.json` tem
  `keys: []`: com o roster vazio **qualquer** envelope é recusado, logo nenhuma entrega é
  publicável até ao provisionamento (§3). A prova end-to-end foi feita com chave efémera,
  gerada e destruída fora da árvore do repositório.

A estes juntam-se três residuais **de outras pistas**, nomeados em ADR-017 §Consequências
(residuais 7, 9 e 10): a ordem dos passos em `.github/workflows/ci.yml` (que regenera a
proveniência **depois** de a assinar — mitigado por o `sbom.sh` remover a atestação obsoleta),
a não-distinção entre a saída `3` e a `1` nesse mesmo workflow, e a reconciliação do STRIDE/RTM
com o novo estado do ponto 3.

---

**Ficheiros desta cadeia:** `scripts/ci/attest/` (assinador/verificador, stdlib-only) ·
`scripts/ci/sign.sh` (assina) · `scripts/ci/verify-attestation.sh` (recusa) ·
`scripts/ci/sbom.sh` (gera SBOM/proveniência) · `scripts/ci/package.sh` (cadeia de entrega) ·
`deploy/node/release-pubkeys.json` (âncora de confiança).
