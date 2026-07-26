#!/usr/bin/env bash
# integration.sh — Gate 4 «Integração» (AOS-198, DAT-09).
#
# Conformidade dos contratos de porta C1–C5 de tecnica/12 com o código Go.
# Toda a lógica vive em integration.py (Python 3 stdlib, zero dependências),
# tal como ref-lint.sh/ref-lint.py. Fail-closed: sem `|| true`, sem
# `continue-on-error`, e sem caminho que devolva 0 quando não consegue correr —
# se o python3 não existir, o exec falha e o gate fica vermelho.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

log_gate "integration — contratos de porta C1–C5 (tecnica/12) vs. código Go"
python3 "$CI_DIR/integration.py"
