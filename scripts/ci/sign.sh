#!/usr/bin/env bash
# sign.sh — ATESTAÇÃO DE ENTREGA ASSINADA do nó `aos` (AOS-207 / ADR-017 ponto 3).
#
# O QUE ESTE PASSO FECHA
# ----------------------
# O ponto 3 do ADR-017 pedia SBOM + atestação de proveniência ASSINADOS; a §Consequências
# admitia entregá-lo «na forma MÍNIMA (SBOM gerado, atestação POR ASSINAR)» e o `sbom.sh`
# escrevia literalmente `signature.status = DEFERIDO-EPIC-10`. Este script é o passo que faz
# a atestação passar de GERADA a ASSINADA E VERIFICÁVEL; o `verify-attestation.sh` é o gate
# que RECUSA a entrega quando a assinatura não valida.
#
# FERRAMENTA DE ASSINATURA — a decisão (declarada, não assumida)
# --------------------------------------------------------------
# ed25519 da STDLIB (`crypto/ed25519`, via `scripts/ci/attest`), NÃO cosign/sigstore. A
# justificação completa e o CUSTO da escolha estão no cabeçalho de `scripts/ci/attest/main.go`
# e em ADR-017 §Consequências. Em duas linhas: uma ferramenta externa daria interoperabilidade
# com verificadores padrão mas seria inexecutável neste ambiente (build offline, sem rede para
# Rekor, sem cosign no PATH) — DEF-06 fecharia com outro diferimento em vez de uma garantia
# imposta. O formato do que se assina É padrão (envelope DSSE v1 + in-toto Statement v1), pelo
# que migrar para cosign é re-embrulhar os mesmos bytes.
#
# CUSTÓDIA (o que este script exige e o que RECUSA)
# ------------------------------------------------
#   - a chave PRIVADA entra por CAMINHO (`AOS_RELEASE_KEY_FILE`), nunca por valor: nenhuma
#     variável de ambiente transporta material de chave, nada de chave entra no repositório,
#     em testes ou em fixtures (`aos-attest keygen` RECUSA escrever dentro de uma árvore git);
#   - a chave PÚBLICA vive no registo `deploy/node/release-pubkeys.json` (material público);
#   - assinar com uma chave que o registo NÃO declara confiar é VERMELHO, não um aviso: uma
#     assinatura que ninguém consegue verificar é indistinguível de não haver assinatura;
#   - procedimento completo (quem assina, onde vive, como se roda):
#     `deploy/node/CUSTODIA-CHAVE-RELEASE.md`.
#
# SEM CHAVE CONFIGURADA (o caminho honesto)
# -----------------------------------------
# Um build local/PR não tem — nem deve ter — a chave de release. Nesse caso o script NÃO finge:
# escreve `signature.status = NAO-ASSINADA` na proveniência, NÃO emite envelope, REMOVE
# qualquer envelope obsoleto (um envelope de uma corrida anterior seria um falso-positivo pior
# do que a ausência dele) e regista um SKIP declarado. Quem transforma isso em «não publicável»
# é `verify-attestation.sh` (saída 4) + `package.sh` (VERDE PARCIAL, saída 3).
#
# SEM IMAGEM CONSTRUÍDA (o caso que se assinava em silêncio)
# ---------------------------------------------------------
# Havendo chave mas não havendo imagem, assina-se um statement SEM subject `image:` — e o
# ticket chama-se «assinatura e atestação da IMAGEM». Antes, o manifesto saía com
# `attestation.present=true` e nada distinguia esse envelope de um que cobrisse a imagem: a
# verificação a jusante ficava verde com a garantia central ausente. Passa a haver um estado
# PRÓPRIO — `signature.status = ASSINADA-SEM-IMAGEM`, `attestation.imageBound=false`,
# `attestation.publishable=false` — que o `verify-attestation.sh` trata como NÃO publicável
# (saída 4). Um manifesto que declare `imageBound=true` sem subject de imagem no statement é
# VERMELHO: a incoerência entre o que se afirma e o que se assinou é o próprio ataque.
#
# ORDEM (há uma dependência circular a evitar): a proveniência é FINALIZADA primeiro (o bloco
# `signature` muda o seu digest); só depois se escreve o MANIFESTO DE ENTREGA (que lista o
# digest da proveniência); e só depois o STATEMENT in-toto (que lista o digest do manifesto).
# Assinar antes de finalizar produziria uma atestação sobre bytes que já não existem.
#
# Uso:  bash scripts/ci/sign.sh [OUT_DIR]
#   OUT_DIR default deploy/node/build (artefacto, ignorado pelo .gitignore).
#   IMAGE_TAG             default aos-node:local — imagem cujo digest é atestado.
#   AOS_RELEASE_KEY_FILE  caminho da seed ed25519 (FORA do repo). Ausente => não assina.
#   AOS_RELEASE_PUBKEYS   registo de chaves públicas (default deploy/node/release-pubkeys.json).
#   AOS_SKIP_SINK         (opcional) ficheiro onde este processo ANEXA os skips declarados, para
#                         que o processo PAI (package.sh) os reabsorva — ver `skip_declared`.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

# skip_declared <etapa> <motivo> <garantia por verificar>
#   `gate_skip` regista num ARRAY do PROCESSO, e este script corre como processo FILHO de
#   package.sh: sem este canal por ficheiro, um skip declarado aqui («a atestação NÃO ficou
#   bindada à imagem») nunca chegaria ao veredicto do pai, que concluiria «verde, nada saltado».
skip_declared() {
  gate_skip "$1" "$2" "$3"
  if [ -n "${AOS_SKIP_SINK:-}" ]; then
    mkdir -p "$( dirname "$AOS_SKIP_SINK" )" 2>/dev/null || true
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$AOS_SKIP_SINK"
  fi
}

OUT_DIR="${1:-$REPO_ROOT/deploy/node/build}"
IMAGE_TAG="${IMAGE_TAG:-aos-node:local}"
# A ÂNCORA DE CONFIANÇA NÃO SE DESVIA EM CI (AOS-199).
#
# `AOS_RELEASE_PUBKEYS` decide CONTRA QUE CHAVES a assinatura é verificada. Sem guarda, quem a
# definisse apontava a verificação para um roster próprio e obtinha VERDE sobre um envelope
# forjado — demonstrado a 2026-08-21: statement a atestar uma imagem inexistente, assinado por
# chave de atacante, `EXIT=0` contra o roster do atacante e `EXIT=1` contra o real.
#
# O `gate_path` existia para isto e a sua docstring nomeia cinco gates candidatos à adopção. O
# roster da assinatura NÃO estava entre eles: o botão mais consequente da cadeia nunca chegou à
# lista. Passa a estar, e é o primeiro a adoptá-lo.
#
# Fora de CI o desvio continua a ser aceite — é como se testa um roster novo antes de o commitar.
gate_path AOS_RELEASE_PUBKEYS "$REPO_ROOT/deploy/node/release-pubkeys.json" || exit 4
ROSTER="$AOS_RELEASE_PUBKEYS"
KEY_FILE="${AOS_RELEASE_KEY_FILE:-}"

PROV="$OUT_DIR/provenance.json"
SBOM="$OUT_DIR/sbom.json"
BIN="$OUT_DIR/aos"
MANIFEST="$OUT_DIR/delivery-manifest.json"
ENVELOPE="$OUT_DIR/attestation.dsse.json"

log_gate "sign · atestação de entrega assinada (ADR-017 ponto 3 / AOS-207)"

# --- Pré-condições: não se assina o que não existe ---------------------------
for f in "$SBOM" "$PROV" "$BIN"; do
  if [ ! -f "$f" ]; then
    log_fail "artefacto de entrega ausente: $f — corra scripts/ci/sbom.sh primeiro (fail-closed)"
    exit 1
  fi
done

sha_of() { sha256sum "$1" | awk '{print $1}'; }

# --- Identidade da imagem atestada -------------------------------------------
# `.Id` é o digest de configuração da imagem LOCAL — existe sempre que a imagem foi
# construída. `RepoDigests` só existe DEPOIS de um push e é o digest de manifesto que um
# registry serve; sem push fica vazio, e isso é declarado em vez de inventado.
image_id=""; image_repo_digest=""; image_bound=0
if command -v docker >/dev/null 2>&1 && docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  image_id="$( docker image inspect --format '{{.Id}}' "$IMAGE_TAG" 2>/dev/null || true )"
  image_repo_digest="$( docker image inspect --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}' "$IMAGE_TAG" 2>/dev/null || true )"
  if [ -n "$image_id" ]; then image_bound=1; fi   # `[ ... ] && x=1` abortaria sob `set -e`
  log_ok "imagem atestada: $IMAGE_TAG (${image_id:-<sem id>})"
fi
if [ "$image_bound" -ne 1 ]; then
  skip_declared "bindagem da atestação à IMAGEM" \
                "imagem $IMAGE_TAG indisponível (docker ausente ou imagem não construída)" \
                "AOS-207 / ADR-017 ponto 3 — a atestação cobre o binário e o SBOM mas NÃO o digest da imagem; entrega NÃO publicável"
fi

# --- Ferramenta de assinatura (stdlib-only, compilada do próprio repo) --------
TOOLDIR="$( mktemp -d )"
trap 'rm -rf "$TOOLDIR"' EXIT
ATTEST="$TOOLDIR/aos-attest$( go env GOEXE )"
log_step "compilar scripts/ci/attest (stdlib-only, GOPROXY=off)"
if ! ( cd "$CI_DIR/attest" && GOFLAGS="" GOPROXY=off go build -trimpath -o "$ATTEST" . ); then
  log_fail "compilação de scripts/ci/attest falhou — fail-closed (sem assinador não há entrega)"
  exit 1
fi

# --- Estado da chave ----------------------------------------------------------
signed=0
keyid=""
if [ -n "$KEY_FILE" ]; then
  if [ ! -f "$KEY_FILE" ]; then
    log_fail "AOS_RELEASE_KEY_FILE='$KEY_FILE' não existe — configuração inválida, não se degrada em silêncio"
    exit 1
  fi
  # O keyid é derivado da chave PÚBLICA (sha256); o material privado nunca é impresso.
  if ! keyid="$( "$ATTEST" pubkey -key "$KEY_FILE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["keyid"])' )"; then
    log_fail "não foi possível derivar a chave pública de AOS_RELEASE_KEY_FILE — fail-closed"
    exit 1
  fi
  signed=1
  log_ok "chave de release carregada (keyid $keyid) — material privado NÃO ecoado"
else
  skip_declared "assinatura da atestação de proveniência" \
                "AOS_RELEASE_KEY_FILE não definida (build sem chave de release — o esperado em PR/local)" \
                "ADR-017 ponto 3 — a atestação fica NÃO-ASSINADA; a entrega NÃO é publicável"
fi

# --- (1) FINALIZAR a proveniência: o bloco `signature` deixa de ser um diferimento.
log_step "finalizar o bloco signature de provenance.json"
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de
# ferramenta e nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 - "$PROV" "$signed" "$keyid" "$ROSTER" "$image_bound" <<'PY'
import json, sys, os
prov_path, signed, keyid, roster = sys.argv[1], sys.argv[2] == "1", sys.argv[3], sys.argv[4]
image_bound = sys.argv[5] == "1"
with open(prov_path, "r", encoding="utf-8") as f:
    doc = json.load(f)
if signed:
    doc["signature"] = {
        # Estado PROPRIO quando se assinou sem imagem: `ASSINADA` sem qualificacao afirmaria a
        # garantia central do AOS-207 (a atestacao da IMAGEM) que este envelope NAO tem.
        "status": "ASSINADA" if image_bound else "ASSINADA-SEM-IMAGEM",
        "imageBound": image_bound,
        "publishable": image_bound,
        "algorithm": "ed25519",
        "keyid": keyid,
        "envelope": "attestation.dsse.json",
        "envelopeFormat": "DSSE v1 (payloadType application/vnd.in-toto+json)",
        "statementFormat": "in-toto Statement v1",
        "roster": os.path.basename(roster),
        "verify": "bash scripts/ci/verify-attestation.sh",
        "note": ("Atestacao ASSINADA e VERIFICAVEL (AOS-207, fecha o ponto 3 do ADR-017). "
                 "Residual NOMEADO: a assinatura NAO esta anexada a imagem no registry (OCI "
                 "referrers) nem inscrita num log de transparencia; ver ADR-017 Consequencias.")
        if image_bound else
        ("Assinada SEM subject de imagem: nao havia imagem construida nesta execucao. A "
         "assinatura vale para o binario, o SBOM, a proveniencia e o manifesto, mas NAO cobre "
         "imagem nenhuma. scripts/ci/verify-attestation.sh devolve 4 (POR VERIFICAR) e "
         "package.sh devolve 3: esta entrega NAO e publicavel."),
    }
else:
    doc["signature"] = {
        "status": "NAO-ASSINADA",
        "reason": "AOS_RELEASE_KEY_FILE ausente nesta execucao",
        "eixo": "AOS-207",
        "note": ("Declarado, nao fingido: sem chave de release nao ha atestacao assinada. "
                 "scripts/ci/verify-attestation.sh devolve 4 e package.sh devolve 3 (VERDE "
                 "PARCIAL) — esta entrega NAO e publicavel."),
    }
with open(prov_path, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY

# --- (2) MANIFESTO DE ENTREGA — o que se publica, e com que digests -----------
# É o documento que um passo de publicação consome. É também o alvo da PROVA NEGATIVA do
# CA de AOS-207: trocar aqui o digest da imagem tem de avermelhar o gate.
log_step "escrever o manifesto de entrega"
sbom_sha="$( sha_of "$SBOM" )"
prov_sha="$( sha_of "$PROV" )"
bin_sha="$( sha_of "$BIN" )"
commit="$( git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || echo unknown )"
now="$( date -u +%Y-%m-%dT%H:%M:%SZ )"

python3 - "$MANIFEST" "$IMAGE_TAG" "$image_id" "$image_repo_digest" \
          "$sbom_sha" "$prov_sha" "$bin_sha" "$commit" "$now" "$signed" "$keyid" "$image_bound" <<'PY'
import json, sys
(out, tag, image_id, repo_digest, sbom_sha, prov_sha, bin_sha, commit, now, signed, keyid,
 image_bound) = sys.argv[1:13]
is_signed = signed == "1"
is_bound = image_bound == "1"
doc = {
    "format": "aos-delivery-manifest/v1",
    "note": ("Manifesto de ENTREGA do no `aos` (AOS-207). Enumera o que se publica e com que "
             "digests. O mesmo conjunto esta ATESTADO em attestation.dsse.json; "
             "scripts/ci/verify-attestation.sh recusa a entrega se divergirem."),
    "image": {
        "tag": tag,
        "id": image_id or None,
        "repoDigest": repo_digest or None,
        "idNote": ("`id` e o digest de configuracao da imagem LOCAL. `repoDigest` (digest de "
                   "manifesto servido por um registry) so existe apos push — nulo aqui e "
                   "declarado, nao inventado."),
    },
    "artifacts": [
        {"name": "aos", "path": "aos", "sha256": bin_sha,
         "note": "binario que a imagem carrega em /usr/local/bin/aos"},
        {"name": "sbom.json", "path": "sbom.json", "sha256": sbom_sha},
        {"name": "provenance.json", "path": "provenance.json", "sha256": prov_sha},
    ],
    "attestation": {
        "present": is_signed,
        # `imageBound` e `publishable` sao a diferenca entre «ha uma assinatura» e «a imagem
        # esta atestada». Sem estes campos, um envelope sem subject de imagem era
        # indistinguivel de um que cobrisse a imagem — e a verificacao a jusante saia verde
        # com a garantia central do AOS-207 ausente.
        "imageBound": is_signed and is_bound,
        "publishable": is_signed and is_bound,
        "status": ("ASSINADA" if is_bound else "ASSINADA-SEM-IMAGEM") if is_signed else "NAO-ASSINADA",
        "file": "attestation.dsse.json" if is_signed else None,
        "algorithm": "ed25519" if is_signed else None,
        "keyid": keyid or None,
        "roster": "deploy/node/release-pubkeys.json",
        "verify": "bash scripts/ci/verify-attestation.sh",
        "note": None if (is_signed and is_bound) else (
            "NAO PUBLICAVEL: " + ("a atestacao nao cobre imagem nenhuma (assinada sem imagem "
            "construida)" if is_signed else "nao ha atestacao assinada nesta execucao") +
            ". scripts/ci/verify-attestation.sh devolve 4 e package.sh devolve 3."),
    },
    "source": {"repo": "github.com/aos-ref", "commit": commit},
    "producedOn": now,
}
with open(out, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY
log_ok "manifesto de entrega: $MANIFEST"

# --- (3) Sem chave: não há envelope. E o envelope ANTIGO é REMOVIDO -----------
if [ "$signed" -ne 1 ]; then
  if [ -f "$ENVELOPE" ]; then
    rm -f "$ENVELOPE"
    log_warn "envelope de uma corrida anterior REMOVIDO ($ENVELOPE) — deixá-lo seria um falso-positivo"
  fi
  gate_skip_report || true
  log_ok "sign: proveniência e manifesto emitidos; atestação NAO-ASSINADA (declarado)"
  exit 0
fi

# --- (4) STATEMENT in-toto v1 -------------------------------------------------
# Os `subject` são o conjunto ATESTADO: imagem (quando há), binário, SBOM, proveniência e o
# próprio MANIFESTO. Incluir o manifesto como subject é o que torna a prova negativa exacta:
# qualquer byte alterado nele muda o seu sha256 e a verificação avermelha.
manifest_sha="$( sha_of "$MANIFEST" )"
STATEMENT="$TOOLDIR/statement.json"
log_step "construir o in-toto Statement v1 (subjects = imagem + binário + SBOM + proveniência + manifesto)"
python3 - "$STATEMENT" "$PROV" "$IMAGE_TAG" "$image_id" "$image_repo_digest" \
          "$bin_sha" "$sbom_sha" "$prov_sha" "$manifest_sha" "$now" <<'PY'
import json, sys
(out, prov_path, tag, image_id, repo_digest, bin_sha, sbom_sha, prov_sha, manifest_sha, now) = sys.argv[1:11]
with open(prov_path, "r", encoding="utf-8") as f:
    prov = json.load(f)

subjects = []
if image_id:
    # `sha256:<hex>` -> o in-toto quer o hex nu no mapa de digests.
    subjects.append({"name": "image:" + tag, "digest": {"sha256": image_id.split(":", 1)[-1]}})
subjects += [
    {"name": "usr/local/bin/aos", "digest": {"sha256": bin_sha}},
    {"name": "sbom.json", "digest": {"sha256": sbom_sha}},
    {"name": "provenance.json", "digest": {"sha256": prov_sha}},
    {"name": "delivery-manifest.json", "digest": {"sha256": manifest_sha}},
]
stmt = {
    "_type": "https://in-toto.io/Statement/v1",
    "subject": subjects,
    "predicateType": "https://aos-ref.dev/attestations/node-delivery/v1",
    "predicate": {
        "buildType": prov.get("buildType"),
        "builder": prov.get("builder"),
        "source": prov.get("source"),
        "buildConfig": prov.get("buildConfig"),
        "metadata": prov.get("metadata"),
        "image": {"tag": tag, "id": image_id or None, "repoDigest": repo_digest or None},
        "attestedOn": now,
        "adr": "ADR-017 ponto 3 (fechado por AOS-207)",
    },
}
with open(out, "w", encoding="utf-8") as f:
    json.dump(stmt, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY

# --- (5) Assinar ---------------------------------------------------------------
log_step "assinar (DSSE v1 / ed25519)"
if ! "$ATTEST" sign -key "$KEY_FILE" -payload "$STATEMENT" -out "$ENVELOPE"; then
  log_fail "assinatura falhou — fail-closed"
  exit 1
fi

# --- (6) AUTO-VERIFICAÇÃO imediata contra o REGISTO DE CONFIANÇA ---------------
# Assinar com uma chave que o registo não declara confiar produziria um envelope que ninguém
# consegue verificar — indistinguível, na prática, de não haver assinatura. É VERMELHO aqui,
# no ponto onde o operador ainda pode corrigir, e não só na verificação a jusante.
log_step "auto-verificação: a chave que assinou é confiável no registo?"
if [ ! -f "$ROSTER" ]; then
  log_fail "registo de chaves públicas ausente: $ROSTER — fail-closed"
  exit 1
fi
if ! "$ATTEST" verify -envelope "$ENVELOPE" -roster "$ROSTER"; then
  log_fail "a atestação foi assinada com uma chave que $ROSTER NÃO declara confiar."
  log_fail "  Ver deploy/node/CUSTODIA-CHAVE-RELEASE.md (provisionar/rodar a chave de release)."
  exit 1
fi

gate_skip_report || true
if [ "$image_bound" -eq 1 ]; then
  log_ok "sign: atestação ASSINADA e auto-verificada (keyid $keyid), a COBRIR a imagem $IMAGE_TAG — $ENVELOPE"
else
  # Não é um `log_ok` liso: o envelope existe e a assinatura vale, mas o objecto do ticket
  # (a imagem) não está coberto. Dizer «assinada» sem qualificação aqui seria a mesma
  # afirmação a mais que o verify deixou de fazer.
  log_warn "sign: atestação ASSINADA-SEM-IMAGEM (keyid $keyid) — cobre binário/SBOM/proveniência/manifesto,"
  log_warn "      NÃO cobre imagem nenhuma. verify-attestation.sh devolve 4 e a entrega NÃO é publicável."
fi
