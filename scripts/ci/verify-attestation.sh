#!/usr/bin/env bash
# verify-attestation.sh — GATE que RECUSA a entrega de uma imagem cuja atestação não valide
# (AOS-207, CA «a entrega recusa uma imagem cuja assinatura não valide» / ADR-017 ponto 3).
#
# O QUE VERIFICA, POR ORDEM (e porquê nesta ordem)
# ------------------------------------------------
#   1. CRIPTOGRAFIA E CONFIANÇA — `aos-attest verify`: o envelope DSSE tem assinatura válida
#      sobre o PAE do payload, de uma chave que `deploy/node/release-pubkeys.json` declara
#      confiar, não revogada e dentro da janela de validade. Só DEPOIS de a assinatura valer
#      é que o payload é lido: ler primeiro e verificar depois seria confiar em bytes não
#      autenticados para decidir o que verificar.
#   2. COBERTURA — o statement autenticado TEM de trazer o subject `image:` (é este o objecto
#      do ticket). Uma atestação criptograficamente impecável que não cobre imagem nenhuma
#      não prova nada sobre a imagem: NÃO sai verde (ver o vocabulário de saída, abaixo).
#   3. REALIDADE — cada `subject` do statement autenticado é RECOMPUTADO contra o artefacto
#      que está no disco (binário, SBOM, proveniência, manifesto) e contra o digest REAL da
#      imagem (`docker image inspect`). Uma atestação internamente coerente sobre bytes que já
#      não existem não é uma garantia — e um subject que não foi recomputado NÃO é contado
#      como verificado (a mensagem final distingue os dois, ver «CONTAGEM HONESTA»).
#   4. MANIFESTO — os campos do manifesto de entrega (digest da imagem, digests dos ficheiros)
#      têm de bater com o statement assinado. É a comparação que dá o DIAGNÓSTICO legível: o
#      passo 3 já apanharia a adulteração pelo sha256 do próprio manifesto, mas diria apenas
#      «o manifesto mudou»; este passo diz QUAL campo mudou e de quê para quê.
#
# CONTAGEM HONESTA (porque a mensagem final mudou)
# ------------------------------------------------
# A versão anterior contava o subject da imagem como «bate com a realidade» mesmo quando a
# imagem não estava presente para ser recomparada — o veredicto afirmava mais do que tinha
# verificado. A contagem passa a separar RECOMPARADO COM A REALIDADE de COMPARADO COM O
# MANIFESTO, e tudo o que não foi recomputado sai declarado em `AOS_SKIPPED_STEPS`.
#
# CÓDIGOS DE SAÍDA (o vocabulário já usado por package.sh)
#   0 = VERDE — atestação assinada, a cobrir a imagem, e TUDO recomparado com a realidade.
#   1 = VERMELHO — a entrega está BLOQUEADA (assinatura inválida, chave não confiável, digest
#       divergente, manifesto adulterado, manifesto que afirma uma garantia inexistente).
#   4 = POR VERIFICAR, declarado — a entrega NÃO é publicável, mas nada mentiu. Três casos:
#       (a) não há envelope e o manifesto também não o afirma (build sem chave de release);
#       (b) o envelope é válido mas NÃO tem subject `image:` — assinou-se sem imagem
#           construída, logo a garantia central do AOS-207 está ausente;
#       (c) o envelope cobre uma imagem que não está presente para ser recomparada.
#       Antes, (b) e (c) saíam 0 — um falso-verde exactamente na garantia que o ticket entrega.
#       `package.sh` converte o 4 em VERDE PARCIAL (saída 3), que é o sinal de «não publicável».
#
# Uso:  bash scripts/ci/verify-attestation.sh [OUT_DIR]
#   IMAGE_TAG           default aos-node:local
#   AOS_RELEASE_PUBKEYS default deploy/node/release-pubkeys.json
#   AOS_SKIP_SINK       (opcional) ficheiro onde este processo ANEXA os skips declarados, para
#                       que o processo PAI (package.sh) os reabsorva — ver `skip_declared`.
#
# NOTA sobre modos do shell: `lib.sh` impõe `set -euo pipefail` a toda a cadeia (é a política
# do programa: nada de `set +e` a mascarar falhas). Quem quiser INSPECCIONAR um código de saída
# tem, por isso, de o capturar em contexto de condição (`|| rc=$?`, `if !`) — caso contrário o
# script morre no ponto da falha e o tratamento escrito a seguir é código morto.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

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

MANIFEST="$OUT_DIR/delivery-manifest.json"
ENVELOPE="$OUT_DIR/attestation.dsse.json"

# skip_declared <etapa> <motivo> <garantia por verificar>
#   `gate_skip` regista num ARRAY do PROCESSO. Este script corre como processo FILHO de
#   package.sh (`bash "$CI_DIR/verify-attestation.sh"`), pelo que o array do pai nunca veria
#   estes registos: o pai podia concluir «VERDE, nenhuma etapa saltada» enquanto o filho
#   declarava que a imagem não tinha sido comparada. O sink é o canal por FICHEIRO que
#   atravessa a fronteira de processo; o pai lê-o e reabsorve-o no seu próprio veredicto.
skip_declared() {
  gate_skip "$1" "$2" "$3"
  if [ -n "${AOS_SKIP_SINK:-}" ]; then
    mkdir -p "$( dirname "$AOS_SKIP_SINK" )" 2>/dev/null || true
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$AOS_SKIP_SINK"
  fi
}

log_gate "verify-attestation · a entrega recusa o que não valida (AOS-207 / ADR-017 ponto 3)"

if [ ! -f "$MANIFEST" ]; then
  log_fail "manifesto de entrega ausente: $MANIFEST"
  log_fail "  Não há entrega para verificar. Corra scripts/ci/package.sh (ou scripts/ci/sbom.sh"
  log_fail "  seguido de scripts/ci/sign.sh, que é quem escreve o manifesto)."
  exit 1
fi

# Um manifesto que AFIRMA ter atestação sem ela existir é uma mentira do artefacto, não uma
# ausência — e por isso é VERMELHO, não saída 4.
claims_attestation="$( python3 -c 'import json,sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    print("1" if json.load(f).get("attestation", {}).get("present") else "0")' "$MANIFEST" 2>/dev/null || echo "erro" )"
if [ "$claims_attestation" = "erro" ]; then
  log_fail "manifesto de entrega ilegível/malformado: $MANIFEST — fail-closed"
  exit 1
fi

if [ ! -f "$ENVELOPE" ]; then
  if [ "$claims_attestation" = "1" ]; then
    log_fail "o manifesto declara attestation.present=true mas $ENVELOPE NÃO existe."
    log_fail "  Um manifesto que afirma uma garantia inexistente é pior do que não a ter: VERMELHO."
    exit 1
  fi
  skip_declared "verificação da atestação assinada (não há atestação)" \
                "não existe $ENVELOPE e o manifesto também não a afirma (build sem AOS_RELEASE_KEY_FILE)" \
                "ADR-017 ponto 3 — NADA foi verificado; esta entrega NÃO é publicável"
  log_warn "  Ver deploy/node/CUSTODIA-CHAVE-RELEASE.md (provisionar a chave de release)."
  gate_skip_report || true
  exit 4
fi

if [ ! -f "$ROSTER" ]; then
  log_fail "registo de chaves públicas ausente: $ROSTER — sem âncora de confiança não há verificação"
  exit 1
fi

# --- (1) Criptografia e confiança --------------------------------------------
TOOLDIR="$( mktemp -d )"
trap 'rm -rf "$TOOLDIR"' EXIT
ATTEST="$TOOLDIR/aos-attest$( go env GOEXE )"
log_step "compilar o verificador scripts/ci/attest (stdlib-only, GOPROXY=off)"
if ! ( cd "$CI_DIR/attest" && GOFLAGS="" GOPROXY=off go build -trimpath -o "$ATTEST" . ); then
  log_fail "compilação do verificador falhou — fail-closed (sem verificador não se publica)"
  exit 1
fi

PAYLOAD="$TOOLDIR/statement.authenticated.json"
log_step "verificar a assinatura DSSE contra $( basename "$ROSTER" )"
if ! "$ATTEST" verify -envelope "$ENVELOPE" -roster "$ROSTER" -payload-out "$PAYLOAD"; then
  log_fail "ASSINATURA NÃO VALIDA — entrega RECUSADA (ADR-017 ponto 3)."
  exit 1
fi

# --- (2)+(3)+(4) Cobertura, realidade e manifesto -----------------------------
# A DISPONIBILIDADE da imagem é um facto do ambiente; o que se FAZ com essa ausência é decidido
# no bloco abaixo, junto do statement — porque a consequência depende de o statement atestar (ou
# não) uma imagem. Registar aqui um skip incondicional declararia «imagem não comparada» mesmo
# quando o problema é mais grave (não há sequer o que comparar).
image_id=""
image_available=0
if command -v docker >/dev/null 2>&1 && docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  image_id="$( docker image inspect --format '{{.Id}}' "$IMAGE_TAG" 2>/dev/null || true )"
  if [ -n "$image_id" ]; then image_available=1; fi   # `[ ... ] && x=1` abortaria sob errexit
fi

UNVER="$TOOLDIR/unverified.tsv"
: > "$UNVER"

log_step "cobertura + recomparação dos subjects ASSINADOS com os artefactos reais e o manifesto"
# `|| rc=$?` NÃO é cosmética: `lib.sh` impõe `set -euo pipefail` a TODA a cadeia de gates, pelo
# que uma saída não-zero deste bloco abortaria o script AQUI e tudo o que vem a seguir seria
# código morto — o veredicto, o registo dos skips e o sink que o package.sh reabsorve nunca
# correriam (o processo saía com o código certo e sem dizer porquê). Capturar em condição é o
# que mantém o `errexit` ligado e o diagnóstico vivo.
rc=0
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de
# ferramenta e nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 - "$PAYLOAD" "$MANIFEST" "$OUT_DIR" "$image_id" "$image_available" "$UNVER" "$IMAGE_TAG" <<'PY' || rc=$?
import hashlib, json, os, sys

# O resto da cadeia (lib.sh) escreve UTF-8; sem isto o Python herda a codepage do host e o
# veredicto sai com caracteres substituídos — um relatório de gate ilegível é um relatório
# que ninguém lê.
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8")
    except Exception:
        pass

payload_path, manifest_path, out_dir, image_id, image_avail, unver_path, image_tag = sys.argv[1:8]
image_available = image_avail == "1"

def load(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)

def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

stmt = load(payload_path)
man = load(manifest_path)
errs = []
unver = []          # (etapa, motivo, garantia POR VERIFICAR) -> saída 4, nunca 0
n_real = 0          # subjects RECOMPUTADOS contra o artefacto real
n_manifest = 0      # campos do manifesto comparados com o statement assinado

subjects = {s.get("name"): s.get("digest", {}).get("sha256") for s in stmt.get("subject", [])}

# --- (3) subject -> ficheiro real no disco.
# O manifesto NÃO decide o que é verdade aqui: o mapa é fixo, para que remover uma entrada do
# manifesto não faça uma verificação desaparecer em silêncio.
FILES = {
    "usr/local/bin/aos": "aos",
    "sbom.json": "sbom.json",
    "provenance.json": "provenance.json",
    "delivery-manifest.json": "delivery-manifest.json",
}
for name, rel in FILES.items():
    if name not in subjects:
        errs.append(f"subject AUSENTE do statement assinado: {name} — a atestação não cobre este artefacto")
        continue
    path = os.path.join(out_dir, rel)
    if not os.path.exists(path):
        errs.append(f"artefacto atestado AUSENTE do disco: {rel}")
        continue
    real = sha256_file(path)
    if real != subjects[name]:
        errs.append(f"DIGEST DIVERGENTE em {rel}: assinado={subjects[name]} real={real}")
    else:
        n_real += 1

# --- (2) COBERTURA DA IMAGEM — fail-closed.
# O objecto do AOS-207 é a atestação da IMAGEM. Um envelope válido SEM subject `image:` deixava
# passar a verde uma entrega em que a garantia central estava integralmente ausente. Passa a
# ser: VERMELHO se o manifesto afirmar bindagem que o statement não tem (mentira do artefacto),
# e POR VERIFICAR (saída 4) quando o manifesto é honesto quanto à ausência.
img_subject = None
for name, dig in subjects.items():
    if name.startswith("image:"):
        img_subject = (name, dig)
        break

man_img = (man.get("image") or {}).get("id") or ""
att = man.get("attestation") or {}
# `imageBound` é o campo explícito (sign.sh, AOS-207). Um manifesto antigo/sem o campo não é
# tratado como «não bindado» em silêncio: deriva-se de image.id, e a ausência de ambos com um
# subject de imagem presente é incoerência, não conveniência.
if "imageBound" in att:
    man_bound = bool(att.get("imageBound"))
else:
    man_bound = bool(man_img)

if img_subject is None:
    if man_bound or man_img:
        errs.append(
            "COBERTURA MENTIDA — o manifesto declara uma imagem atestada "
            f"(imageBound={att.get('imageBound')!r}, image.id={man_img or None!r}) mas o statement "
            "ASSINADO não tem subject `image:`: a assinatura não cobre imagem nenhuma")
    else:
        unver.append((
            "bindagem da atestação à IMAGEM",
            f"o statement assinado não tem subject `image:` (assinado sem a imagem {image_tag} construída)",
            "AOS-207 / ADR-017 ponto 3 — a atestação cobre os ficheiros mas NÃO a imagem; não publicável"))
else:
    if not man_bound:
        errs.append(
            "MANIFESTO INCOERENTE — o statement assinado atesta "
            f"{img_subject[0]} mas o manifesto declara attestation.imageBound=false")
    # (3') imagem: subject vs imagem realmente presente no docker.
    if image_available:
        real_hex = image_id.split(":", 1)[-1]
        if img_subject[1] != real_hex:
            errs.append(f"DIGEST DA IMAGEM DIVERGENTE: assinado={img_subject[1]} real={real_hex}")
        else:
            n_real += 1
    else:
        unver.append((
            "recomparação do digest da IMAGEM com a imagem real",
            f"imagem {image_tag} indisponível (docker ausente ou imagem não construída neste host)",
            "AOS-207 / ADR-017 ponto 3 — o subject da imagem foi comparado com o MANIFESTO mas NÃO "
            "com a imagem: uma imagem TROCADA sob a mesma tag não seria detectada; não publicável"))
    # (4) manifesto vs statement assinado (diagnóstico legível).
    if not man_img:
        errs.append("o manifesto NÃO declara image.id mas o statement assinado atesta uma imagem")
    elif man_img.split(":", 1)[-1] != img_subject[1]:
        errs.append(
            "MANIFESTO ADULTERADO — image.id não bate com o subject assinado:\n"
            f"       manifesto = {man_img}\n"
            f"       assinado  = sha256:{img_subject[1]}")
    else:
        n_manifest += 1

for art in man.get("artifacts", []):
    name = art.get("name")
    key = "usr/local/bin/aos" if name == "aos" else name
    signed = subjects.get(key)
    if signed is None:
        errs.append(f"o manifesto lista o artefacto {name!r} que o statement assinado NÃO cobre")
    elif art.get("sha256") != signed:
        errs.append(
            f"MANIFESTO ADULTERADO — sha256 de {name} não bate com o subject assinado:\n"
            f"       manifesto = {art.get('sha256')}\n"
            f"       assinado  = {signed}")
    else:
        n_manifest += 1

with open(unver_path, "w", encoding="utf-8") as f:
    for etapa, motivo, garantia in unver:
        f.write("\t".join(x.replace("\t", " ").replace("\n", " ") for x in (etapa, motivo, garantia)) + "\n")

if errs:
    print("DIVERGENCIAS (a entrega e RECUSADA):", file=sys.stderr)
    for e in errs:
        print("     - " + e, file=sys.stderr)
    sys.exit(1)

print(f"   {n_real} subject(s) RECOMPUTADO(S) contra o artefacto REAL | "
      f"{n_manifest} campo(s) do manifesto batem com o statement ASSINADO")
if unver:
    print(f"   {len(unver)} verificacao(oes) NAO realizada(s) -- declarada(s) abaixo "
          "(a entrega NAO e publicavel)")
    sys.exit(4)
sys.exit(0)
PY

if [ "$rc" -eq 4 ]; then
  # Skips vindos do bloco autenticado: registados no array LOCAL e no sink que o pai reabsorve.
  while IFS=$'\t' read -r etapa motivo garantia; do
    [ -n "${etapa:-}" ] || continue
    skip_declared "$etapa" "$motivo" "$garantia"
  done < "$UNVER"
  gate_skip_report || true
  log_warn "verify-attestation: POR VERIFICAR (saída 4) — a assinatura vale e nada divergiu,"
  log_warn "  mas a garantia da IMAGEM não ficou provada nesta execução. VERDE PARCIAL a jusante"
  log_warn "  (package.sh saída 3): esta entrega NÃO é publicável."
  exit 4
fi

if [ "$rc" -ne 0 ]; then
  log_fail "verify-attestation: VERMELHO — a atestação não corresponde ao que se entrega."
  log_fail "  A entrega está BLOQUEADA (ADR-017 ponto 3 / AOS-207)."
  exit 1
fi

gate_skip_report || true
log_ok "verify-attestation: VERDE — atestação assinada, a cobrir a IMAGEM, e recomparada com o artefacto real"
exit 0
