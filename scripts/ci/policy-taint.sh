#!/usr/bin/env bash
# policy-taint.sh — Gate taint-lint da política assinada (AOS-376): cada `permit`
# de aos_authz.cedar exige `context.taint != "untrusted"` ou está numa baseline
# com dono.
#
# Toda a lógica vive em policy-taint.py (Python 3 stdlib, zero dependências).
# Fail-closed: sem `|| true`, sem `continue-on-error`, e sem caminho que devolva
# 0 quando não consegue correr.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

log_gate "policy-taint — cada permit da política assinada exige context.taint != untrusted"
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de
# ferramenta e nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 "$CI_DIR/policy-taint.py"
