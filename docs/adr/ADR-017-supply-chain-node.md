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
3. **Proveniência e SBOM.** A imagem carrega um **SBOM** e uma **atestação de proveniência**
   (quem/o quê/quando a produziu), assinados — a mesma exigência de `provenance`+`signature` que o
   REG faz a uma tool, agora aplicada ao artefacto do nó.
4. **Gates fail-closed na entrega.** `sast.sh`/`sca.sh` (baseline multiset, nunca `sort -u`) e o
   gate de segredos correm na cadeia de entrega; uma descoberta NOVA fora da baseline avermelha
   (fail-closed), como no resto do programa.
5. **A autoridade de identidade é um domínio de supply-chain SEPARADO.** O issuer (AOS-156,
   self-hosted Nível 2) é um artefacto/trust-domain distinto do nó — a sua chave e distribuição têm
   custódia própria (nunca na imagem do nó). Endurecimento posterior: HSM/sign-in-place, attestation
   de imagem por registry assinado.

## Consequências

- **Positivas:** o artefacto do nó é tão auditável quanto uma tool do REG; a fronteira para-fora
  fica explícita ANTES de o nó ser empacotado (fecha o achado do painel); nenhuma dependência
  externa nova entra sem passar pelos gates.
- **Custos/risco residual:** SBOM+atestação assinada exige infra de assinatura de imagem (parte do
  endurecimento de EPIC-10); até lá, os pontos 1/2/4 são impostos e o 3 fica na forma mínima
  (SBOM gerado, atestação por assinar) — declarado, não fingido. A garantia mais alta (registry de
  imagens assinado, attestation de hardware) é endurecimento datado.

## Estado na Carta

Registada como **FIXA** no registo de decisões da Carta (§4.1). É pré-condição de AOS-168
(empacotamento do nó): o nó não é distribuído antes de os pontos 1/2/4 estarem verdes.
