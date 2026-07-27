#!/usr/bin/env bash
# sbom.sh — SBOM + PROVENIÊNCIA do binário do nó `aos` (ADR-017 ponto 3).
#
# ÂMBITO (mudou com AOS-207): este script GERA o SBOM e a proveniência; NÃO assina. A
# assinatura e a recusa da entrega são de `scripts/ci/sign.sh` e
# `scripts/ci/verify-attestation.sh`, encadeados a seguir a este por `scripts/ci/package.sh`.
# O bloco `signature` que sai daqui é TRANSITÓRIO (`POR-FINALIZAR`) — é o sign.sh que o
# fecha. Antes de AOS-207 este campo dizia `DEFERIDO-EPIC-10`, o que era um eixo ERRADO:
# nenhum dos onze tickets do EPIC-10 assina imagens (corrigido por AOS-196 e fechado aqui).
#
# Este script:
#   (a) determina o SUBJECT — o binário que a IMAGEM REALMENTE carrega. Quando a imagem
#       (IMAGE_TAG) existe, EXTRAI /usr/local/bin/aos dela (docker create + docker cp) e
#       hasheia ESSE artefacto. A proveniência tem de bindar-se ao que SHIPA, não a um
#       rebuild do host (toolchain/cache do host divergem byte-a-byte do build da imagem);
#   (b) extrai o SBOM dos MÓDULOS embebidos NESSE binário com `go version -m` (Go tooling);
#   (c) emite o registo de PROVENIÊNCIA (quem/o-quê/quando) com o bloco `signature` ainda
#       POR FINALIZAR — quem o fecha é o `sign.sh` (não se finge aqui uma garantia que só
#       existe depois de assinada e verificada).
#
# Reprodutibilidade HONESTA: `reproducible:true` NUNCA é emitido sem verificação. Só quando o
# rebuild estático do host bate BYTE-A-BYTE com o binário da imagem é que se afirma true; caso
# contrário reproducible:false + `reproducibilityCheck` explica porquê (o campo é auditável).
#
# Zero-dep: só stdlib/go tooling + coreutils (+ docker para extrair o subject). Fail-closed.
#
# Uso:  bash scripts/ci/sbom.sh [OUT_DIR]
#   OUT_DIR default: deploy/node/build (ignorado pelo .gitignore — artefacto, não fonte).
#   IMAGE_TAG default aos-node:local — imagem de onde se extrai o subject (se existir/houver docker).
#   AOS_SKIP_SINK (opcional) ficheiro onde este processo ANEXA os skips declarados, para que o
#     processo PAI (package.sh) os reabsorva — ver `skip_declared`.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

# skip_declared <etapa> <motivo> <garantia por verificar>
#   `gate_skip` regista num ARRAY do PROCESSO, e este script corre como processo FILHO de
#   package.sh — o array do pai nunca veria o registo. O sink é o canal por ficheiro que
#   atravessa a fronteira de processo; sem ele, o veredicto do pai podia dizer «nada saltado»
#   enquanto o filho declarava que o subject não ficou bindado à imagem.
skip_declared() {
  gate_skip "$1" "$2" "$3"
  if [ -n "${AOS_SKIP_SINK:-}" ]; then
    mkdir -p "$( dirname "$AOS_SKIP_SINK" )" 2>/dev/null || true
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$AOS_SKIP_SINK"
  fi
}

NODE_MOD="packages/cmd/aos"
OUT_DIR="${1:-$REPO_ROOT/deploy/node/build}"
IMAGE_TAG="${IMAGE_TAG:-aos-node:local}"
mkdir -p "$OUT_DIR"

# Bases pinadas por digest (têm de casar com deploy/node/Dockerfile — proveniência honesta).
BUILDER_IMAGE="golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40"
RUNTIME_IMAGE="gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35"

log_gate "sbom · binário estático + SBOM + proveniência (ADR-017 ponto 3; assina-se em sign.sh)"

# ---------------------------------------------------------------------------
# Rebuild estático do host (CGO off, GOPROXY=off, trimpath) — o MESMO comando do Dockerfile.
# NÃO é (por si) o subject: serve (a) de fallback quando não há imagem para extrair e (b) de
# referência para a verificação de reprodutibilidade byte-a-byte contra o binário da imagem.
host_bin="$OUT_DIR/aos.hostbuild"
log_step "rebuild estático do host CGO_ENABLED=0 GOPROXY=off (referência de reprodutibilidade)"
if ! ( cd "$REPO_ROOT/$NODE_MOD" && CGO_ENABLED=0 GOOS=linux GOPROXY=off \
        go build -trimpath -ldflags="-s -w -buildid=" -o "$host_bin" . ); then
  log_fail "build estático do nó falhou (cache primo? corre scripts/ci/cache-prime.sh com rede)"
  exit 1
fi
host_sha="$( sha256sum "$host_bin" | awk '{print $1}' )"
log_ok "rebuild do host: $host_bin ($host_sha)"

# ---------------------------------------------------------------------------
# SUBJECT = binário que a IMAGEM carrega. Extrai-o da imagem quando disponível; senão, cai para
# o rebuild do host (declarando a fonte). subject_source torna a origem explícita e auditável.
bin="$OUT_DIR/aos"
image_id=""
if command -v docker >/dev/null 2>&1 && docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  log_step "extrair /usr/local/bin/aos da imagem $IMAGE_TAG (subject = artefacto que SHIPA)"
  cid="$( docker create "$IMAGE_TAG" )"
  if ! docker cp "$cid:/usr/local/bin/aos" "$bin" >/dev/null 2>&1; then
    docker rm -f "$cid" >/dev/null 2>&1 || true
    log_fail "falha a extrair o binário da imagem $IMAGE_TAG — fail-closed"
    exit 1
  fi
  docker rm -f "$cid" >/dev/null 2>&1 || true
  image_id="$( docker image inspect --format '{{.Id}}' "$IMAGE_TAG" 2>/dev/null || echo unknown )"
  subject_source="image:$IMAGE_TAG"
  log_ok "subject extraído da imagem: $bin"
else
  # DEGRADAÇÃO DECLARADA (AOS-199): a CI invoca este script TAMBÉM como step autónomo
  # (fora do package.sh), e nesse caminho um WARN solto a meio do output não é registo —
  # o veredicto do próprio script tem de redeclarar o que ficou por verificar. O registo
  # de skips passa a ser propriedade do runner, não de um script.
  skip_declared "SBOM subject bindado à imagem" \
                "imagem $IMAGE_TAG indisponível (docker ausente ou imagem não construída)" \
                "ADR-017 ponto 3 — a proveniência NÃO fica ligada ao binário que shipa (subject = rebuild do host)"
  cp -f "$host_bin" "$bin"
  subject_source="host-build"
fi
bin_sha="$( sha256sum "$bin" | awk '{print $1}' )"
log_ok "subject sha256: $bin_sha (fonte: $subject_source)"

# ---------------------------------------------------------------------------
# Verificação de reprodutibilidade: só afirma reproducible=true se o rebuild do host bater
# BYTE-A-BYTE com o subject (o binário da imagem). Nunca se finge a garantia.
if [ "$subject_source" = "host-build" ]; then
  reproducible="false"
  repro_check="unverified-no-image-to-compare"
elif [ "$host_sha" = "$bin_sha" ]; then
  reproducible="true"
  repro_check="verified-equal-to-host-rebuild"
  log_ok "reprodutibilidade VERIFICADA: rebuild do host == binário da imagem"
else
  reproducible="false"
  repro_check="host-rebuild-differs-from-image"
  log_warn "reprodutibilidade NÃO verificada: rebuild do host ($host_sha) != imagem ($bin_sha) — reproducible=false (honesto; toolchain/cache do host divergem do builder pinado)"
fi

# ---------------------------------------------------------------------------
# (b) SBOM a partir dos módulos embebidos NO SUBJECT (go version -m). Formato: JSON minimalista
# com componentes {path,version,sum}. Não inventa CycloneDX/SPDX completo — forma MÍNIMA honesta;
# o campo "format" declara-o.
log_step "go version -m (módulos embebidos no subject)"
gvm="$( go version -m "$bin" )"

sbom="$OUT_DIR/sbom.json"
{
  printf '{\n'
  printf '  "format": "aos-sbom-minimal/v1",\n'
  printf '  "note": "Formato MINIMO (nao e CycloneDX/SPDX completo): componentes extraidos de `go version -m` do binario que a imagem carrega. Este SBOM e um dos subjects ATESTADOS em attestation.dsse.json (AOS-207); qualquer byte alterado aqui faz scripts/ci/verify-attestation.sh recusar a entrega.",\n'
  printf '  "subject": { "name": "aos", "sha256": "%s", "source": "%s" },\n' "$bin_sha" "$subject_source"
  printf '  "toolchain": "%s",\n' "$( go version | awk '{print $3}' )"
  # main module
  main_path="$( printf '%s\n' "$gvm" | awk '$1=="mod"{print $2; exit}' )"
  printf '  "main_module": "%s",\n' "${main_path:-github.com/aos-ref/cmd/aos}"
  printf '  "components": [\n'
  # dep + => (replaced) linhas. Colunas: <tipo> <path> <version> [<sum>]
  printf '%s\n' "$gvm" | awk '
    $1=="dep" { path=$2; ver=$3; sum=($4==""?"":$4); comp[++n]=path "\t" ver "\t" sum }
    $1=="=>"  { if (n>0) { comp[n]=comp[n] "\t=>\t" $2 "\t" $3 } }
    END {
      for (i=1;i<=n;i++) {
        split(comp[i], f, "\t")
        sep=(i<n?",":"")
        printf "    { \"path\": \"%s\", \"version\": \"%s\", \"sum\": \"%s\" }%s\n", f[1], f[2], f[3], sep
      }
    }'
  printf '  ]\n'
  printf '}\n'
} > "$sbom"
ncomp="$( grep -c '"path":' "$sbom" || true )"
log_ok "SBOM: $sbom ($ncomp componentes do subject)"

# ---------------------------------------------------------------------------
# (b') Cobertura do componente externo de autoridade (packages/platform/attestation).
# O nó principal ainda não cabla este módulo (EPIC-16/AOS-182), mas quando existir
# release a proveniência deve incluí-lo. Adiciona-se como extra_modules auditável.
ATT_MOD="packages/platform/attestation"
if [ -f "$REPO_ROOT/$ATT_MOD/go.mod" ]; then
  log_step "go list -m $ATT_MOD (componente externo de autoridade)"
  att_json="$( cd "$REPO_ROOT/$ATT_MOD" && go list -m -json all 2>/dev/null )" || att_json=""
  if [ -n "$att_json" ]; then
    python3 -c 'import json, sys
sbom_path = sys.argv[1]
data = sys.stdin.read()
extra = []
idx = 0
L = len(data)
dec = json.JSONDecoder()
while idx < L:
    ch = data[idx]
    if ch in " \t\n\r":
        idx += 1
        continue
    if ch != "{":
        break
    obj, end = dec.raw_decode(data, idx)
    idx = end
    path = obj.get("Path", "")
    ver = obj.get("Version", "")
    if not ver and obj.get("Main"):
        ver = "v0.0.0"
    sum_ = obj.get("Sum", "")
    if path and ver:
        extra.append({"path": path, "version": ver, "sum": sum_})
with open(sbom_path, "r", encoding="utf-8") as f:
    doc = json.load(f)
if extra:
    doc["extra_modules_note"] = (
        "Componentes de packages/platform/attestation (autoridade externa). "
        "Ainda nao embarcados no binario aos (EPIC-16/AOS-182); incluidos para "
        "cobertura de release e rastreabilidade de supply-chain (AOS-187)."
    )
    doc["extra_modules"] = extra
    with open(sbom_path, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2, ensure_ascii=False)
        f.write("\n")
' "$sbom" <<< "$att_json"
    natt="$( grep -c '"path":' "$sbom" || true )"
    log_ok "SBOM actualizado: $natt componentes no total (inclui $ATT_MOD)"
  fi
fi

# ---------------------------------------------------------------------------
# (c) PROVENIÊNCIA NÃO-ASSINADA (quem/o-quê/quando). SLSA-ish minimalista; o campo
# "signature.status" declara o diferimento — NÃO se finge uma assinatura.
prov="$OUT_DIR/provenance.json"
commit="$( git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || echo unknown )"
branch="$( git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown )"
now="$( date -u +%Y-%m-%dT%H:%M:%SZ )"
builder_id="$( id -un 2>/dev/null || echo unknown )@$( hostname 2>/dev/null || echo unknown )"
{
  printf '{\n'
  printf '  "format": "aos-provenance-minimal/v1",\n'
  printf '  "buildType": "aos/node/docker-multistage",\n'
  printf '  "subject": { "name": "aos", "sha256": "%s", "source": "%s", "imageId": "%s" },\n' "$bin_sha" "$subject_source" "${image_id:-}"
  printf '  "builder": { "id": "%s", "toolchain": "%s" },\n' "$builder_id" "$( go version | awk '{print $3}' )"
  printf '  "source": { "repo": "github.com/aos-ref", "commit": "%s", "branch": "%s" },\n' "$commit" "$branch"
  printf '  "buildConfig": {\n'
  printf '    "builderImage": "%s",\n' "$BUILDER_IMAGE"
  printf '    "runtimeImage": "%s",\n' "$RUNTIME_IMAGE"
  printf '    "flags": "CGO_ENABLED=0 GOOS=linux GOPROXY=off -trimpath -ldflags=-s -w -buildid="\n'
  printf '  },\n'
  printf '  "metadata": { "buildFinishedOn": "%s", "offlineBuild": true, "subjectSource": "%s", "hostRebuildSha256": "%s", "reproducible": %s, "reproducibilityCheck": "%s" },\n' "$now" "$subject_source" "$host_sha" "$reproducible" "$repro_check"
  printf '  "signature": { "status": "POR-FINALIZAR", "eixo": "AOS-207", "note": "Estado TRANSITORIO: este ficheiro sai do sbom.sh por assinar. scripts/ci/sign.sh finaliza este bloco (ASSINADA com keyid, ou NAO-ASSINADA quando nao ha chave de release) e scripts/ci/verify-attestation.sh recusa a entrega que nao valide. Ver este valor numa entrega significa que o sign.sh NAO correu." }\n'
  printf '}\n'
} > "$prov"
log_ok "proveniência (não-assinada): $prov"

# ---------------------------------------------------------------------------
# INVALIDAÇÃO DOS ARTEFACTOS DE ASSINATURA DE UMA CORRIDA ANTERIOR.
#
# `sbom.json` e `provenance.json` acabaram de ser REESCRITOS: os seus sha256 mudaram (o bloco
# `signature` sozinho já muda o digest da proveniência). Ambos são SUBJECTS assinados, pelo que
# qualquer `attestation.dsse.json`/`delivery-manifest.json` que estivesse aqui deixou, neste
# instante, de cobrir o que está no disco. Deixá-los seria publicar um envelope dessincronizado
# do artefacto — pior do que não haver envelope, porque parece uma garantia.
#
# Isto NÃO é hipotético: `.github/workflows/ci.yml` (pista de OUTRO dono) corre `sbom.sh` como
# passo SEPARADO **depois** de `package.sh`, e nenhum gate corre a seguir para apanhar a
# divergência. Enquanto essa ordem não for corrigida, esta remoção é a defesa desta pista: a
# entrega degrada para NÃO-ASSINADA (honesta e recusada a jusante) em vez de mentir. A pendência
# está nomeada em ADR-017 §Consequências (residual 7).
for stale in "$OUT_DIR/attestation.dsse.json" "$OUT_DIR/delivery-manifest.json"; do
  if [ -f "$stale" ]; then
    rm -f "$stale"
    log_warn "REMOVIDO $stale — a proveniência foi regenerada e este artefacto já não a cobre."
    log_warn "        Corra scripts/ci/sign.sh (ou scripts/ci/package.sh) para reassinar."
  fi
done

# Limpeza do rebuild de referência (artefacto intermédio; o subject fica em $bin).
rm -f "$host_bin"

# REDECLARAÇÃO da degradação no veredicto (AOS-199). O exit NÃO muda: o SBOM foi
# genuinamente emitido e o JSON já regista subject.source/reproducible=false — avermelhar
# aqui seria um FALSO VERMELHO num ambiente sem docker. O que faltava era o registo
# uniforme: `AOS_SKIPPED_STEPS` no fim + o marcador máquina-legível ao lado do artefacto.
gate_skip_report || true
gate_skip_file "$OUT_DIR/SKIPPED.txt" || true
# O veredicto NÃO afirma nada sobre a assinatura: este script não assina. Dizia
# «assinatura DEFERIDA-EPIC-10» — eixo errado (AOS-196) e, desde AOS-207, estado errado.
# Quem tem autoridade para falar do estado da assinatura é o sign.sh/verify-attestation.sh.
log_ok "sbom: verde (SBOM + proveniência mínima; subject = $subject_source; reproducible=$reproducible/$repro_check; assinatura: fica para sign.sh — ver signature.status em provenance.json)"
