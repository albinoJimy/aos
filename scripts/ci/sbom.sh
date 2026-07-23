#!/usr/bin/env bash
# sbom.sh — SBOM + PROVENIÊNCIA do binário do nó `aos` (ADR-017 ponto 3, forma MÍNIMA).
#
# ADR-017 §Consequências: o ponto 3 fica na FORMA MÍNIMA até ao endurecimento de EPIC-10 —
# "SBOM gerado, atestação POR ASSINAR — declarado, não fingido". Este script:
#   (a) determina o SUBJECT — o binário que a IMAGEM REALMENTE carrega. Quando a imagem
#       (IMAGE_TAG) existe, EXTRAI /usr/local/bin/aos dela (docker create + docker cp) e
#       hasheia ESSE artefacto. A proveniência tem de bindar-se ao que SHIPA, não a um
#       rebuild do host (toolchain/cache do host divergem byte-a-byte do build da imagem);
#   (b) extrai o SBOM dos MÓDULOS embebidos NESSE binário com `go version -m` (Go tooling);
#   (c) emite um registo de PROVENIÊNCIA NÃO-ASSINADO (quem/o-quê/quando) declarando
#       explicitamente que a ASSINATURA da atestação + o registry de imagens assinado ficam
#       DEFERIDOS para EPIC-10 (não se finge uma garantia que ainda não existe).
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
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

NODE_MOD="packages/cmd/aos"
OUT_DIR="${1:-$REPO_ROOT/deploy/node/build}"
IMAGE_TAG="${IMAGE_TAG:-aos-node:local}"
mkdir -p "$OUT_DIR"

# Bases pinadas por digest (têm de casar com deploy/node/Dockerfile — proveniência honesta).
BUILDER_IMAGE="golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40"
RUNTIME_IMAGE="gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35"

log_gate "sbom · binário estático + SBOM + proveniência (ADR-017 ponto 3, forma mínima)"

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
  log_warn "imagem $IMAGE_TAG indisponível (docker ausente ou imagem não construída) — subject = rebuild do host (não verificável contra a imagem)"
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
  printf '  "note": "Forma minima (ADR-017 ponto 3). Componentes extraidos de `go version -m` do binario que a imagem carrega. Atestacao assinada DEFERIDA para EPIC-10.",\n'
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
log_ok "SBOM: $sbom ($ncomp componentes)"

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
  printf '  "signature": { "status": "DEFERIDO-EPIC-10", "note": "Atestacao por assinar (ADR-017 ponto 3). Registry de imagens assinado + attestation de hardware sao endurecimento datado de EPIC-10. Declarado, nao fingido." }\n'
  printf '}\n'
} > "$prov"
log_ok "proveniência (não-assinada): $prov"

# Limpeza do rebuild de referência (artefacto intermédio; o subject fica em $bin).
rm -f "$host_bin"

log_ok "sbom: verde (SBOM + proveniência mínima; subject = $subject_source; reproducible=$reproducible/$repro_check; assinatura DEFERIDA-EPIC-10)"
