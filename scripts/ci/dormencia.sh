#!/usr/bin/env bash
# dormencia.sh — RELATÓRIO DE SUITES DORMENTES (AOS-358 AC3).
#
# ─── O PROBLEMA ────────────────────────────────────────────────────────────────────────────
# Parte da bateria de testes deste repositório só corre quando existe substrato externo:
#
#   · `AOS_NATS_URL`  — os testes contra JetStream/NATS real. Nenhum ficheiro de CI o define;
#                       numa execução medida do módulo `eventstore`, 32 dos 150 testes eram
#                       saltados por esta razão.
#   · `-tags fclive`  — a microVM Firecracker real. Exige /dev/kvm; legitimamente manual.
#   · `-tags gvlive`  — o executor gVisor real (AOS-358). Opcional: `scripts/ci/isolation-live.sh`.
#
# Nada disto é defeito por si. O defeito é a DORMÊNCIA SILENCIOSA: uma suite que deixa de
# correr sem que nada o diga acaba por deixar de compilar, e ninguém dá por isso até precisar
# dela. `go test` não imprime `t.Skip` sem `-v`, e `scripts/ci/test.sh` corre sem `-v`.
#
# ─── O QUE ESTE RELATÓRIO PROVA ────────────────────────────────────────────────────────────
#   R1 As suites dormentes são NOMEADAS, uma a uma, com a razão pela qual não correram.
#   R2 As suites atrás de build tag COMPILAM (`go vet` com a tag). Uma suite dormente que
#      deixasse de compilar fica VERMELHA aqui — é o que separa dormência de apodrecimento.
#
# ─── O QUE NÃO PROVA ───────────────────────────────────────────────────────────────────────
#   N1 NÃO prova nada sobre o comportamento que essas suites medem. Compilar não é correr.
#   N2 NÃO substitui a re-medição contínua do substrato replicado, que `EPIC-10:202` declara
#      textualmente em falta e cuja cobertura em CI «subestima, por construção».
#
# É um gate: fica VERMELHO se uma suite dormente deixar de compilar, ou se o inventário
# abaixo deixar de bater com a árvore. Não fica vermelho por haver dormência — essa é a
# condição declarada, não uma avaria.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

rc=0

# =============================================================================================
# (1) INVENTÁRIO — quem depende de AOS_NATS_URL, e onde.
# =============================================================================================
log_gate "dormencia · suites que exigem AOS_NATS_URL (substrato replicado real)"
nats_files="$( cd "$REPO_ROOT" && grep -rl 'AOS_NATS_URL' --include='*_test.go' packages/ 2>/dev/null | sort || true )"
if [ -z "$nats_files" ]; then
  log_fail "nenhum ficheiro de teste menciona AOS_NATS_URL — o inventário deixou de bater com a árvore"
  rc=1
else
  nats_total=0
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    n="$( grep -c '^func Test' "$REPO_ROOT/$f" || true )"
    nats_total=$(( nats_total + n ))
    printf '   %-72s %3s teste(s)\n' "$f" "$n"
  done <<< "$nats_files"
  if [ -n "${AOS_NATS_URL:-}" ]; then
    log_ok "AOS_NATS_URL definido — estas suites CORREM nesta execução"
  else
    log_warn "AOS_NATS_URL NÃO definido: ~$nats_total teste(s) DORMENTES nestes ficheiros"
    log_warn "  razão declarada: nenhum ficheiro de CI define AOS_NATS_URL; ver EPIC-10:202 e specs/EPIC-24 AOS-358"
  fi
fi

# =============================================================================================
# (2) SUITES ATRÁS DE BUILD TAG — nomeadas, e obrigadas a COMPILAR.
#     É a metade que impede a dormência de virar apodrecimento: uma suite que ninguém corre
#     e que ninguém compila deixa de existir sem que nada o denuncie.
# =============================================================================================
log_gate "dormencia · suites atrás de build tag (compilam, mesmo sem correr)"
tags_conhecidas=("fclive" "gvlive")
for tag in "${tags_conhecidas[@]}"; do
  ficheiros="$( cd "$REPO_ROOT" && grep -rl "//go:build .*\b$tag\b" --include='*_test.go' packages/ 2>/dev/null | sort || true )"
  if [ -z "$ficheiros" ]; then
    log_warn "tag '$tag': nenhuma suite encontrada (se foi removida, actualize este inventário)"
    continue
  fi
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    printf '   %-72s [-tags %s]\n' "$f" "$tag"
  done <<< "$ficheiros"

  # Compila cada módulo que contenha uma dessas suites, com a tag ligada.
  # O módulo de um ficheiro é o directório ancestral mais próximo com go.mod. Resolvido
  # aqui e não por uma helper da lib: é a única utilização, e uma helper com um só
  # chamador esconde a regra em vez de a declarar.
  mods=""
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    d="$(dirname "$f")"
    while [ "$d" != "." ] && [ "$d" != "/" ]; do
      if [ -f "$REPO_ROOT/$d/go.mod" ]; then
        mods="$mods$d"$'
'
        break
      fi
      d="$(dirname "$d")"
    done
  done <<< "$ficheiros"
  mods="$( printf '%s' "$mods" | sort -u )"
  while IFS= read -r mod; do
    [ -n "$mod" ] || continue
    if ( cd "$REPO_ROOT/$mod" && go vet -tags "$tag" ./... >/dev/null 2>&1 ); then
      log_ok "$mod compila com -tags $tag"
    else
      log_fail "$mod NÃO compila com -tags $tag — dormência virou apodrecimento"
      ( cd "$REPO_ROOT/$mod" && go vet -tags "$tag" ./... 2>&1 | head -20 ) || true
      rc=1
    fi
  done <<< "$mods"
done

# =============================================================================================
# (3) PROCEDIMENTO MANUAL — onde está documentado o que só se corre à mão.
#     AOS-358 AC4: a exigência de KVM para o Firecracker é legítima; o que não pode é o
#     procedimento não existir.
# =============================================================================================
log_gate "dormencia · procedimento manual documentado"
for doc in "deploy/node/dev-hardened/firecracker/README.md" "deploy/server/README.md"; do
  if [ -f "$REPO_ROOT/$doc" ]; then
    log_ok "$doc presente"
  else
    log_fail "$doc AUSENTE — o procedimento manual deixou de estar documentado"
    rc=1
  fi
done
if ! grep -q 'fclive' "$REPO_ROOT/deploy/node/dev-hardened/firecracker/README.md" 2>/dev/null; then
  log_fail "o README do Firecracker não nomeia '-tags fclive' — o procedimento manual não é seguível"
  rc=1
fi

if [ "$rc" -eq 0 ]; then
  log_ok "dormencia: as suites dormentes estão NOMEADAS e as que vivem atrás de build tag COMPILAM"
  log_warn "LEMBRETE: compilar não é correr. Nada aqui prova o comportamento que estas suites medem."
fi
exit "$rc"
