#!/usr/bin/env bash
# stream-names.sh — Gate dos nomes de `stream_id` do Event Store (AOS-424).
#
# Toda a lógica vive em stream-names.py (Python 3 stdlib, zero dependências).
# Fail-closed: sem `|| true`, sem `continue-on-error`, e sem caminho que devolva
# 0 quando não consegue correr.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

log_gate "stream-names — todo o stream_id e representavel num subject NATS"
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de
# ferramenta e nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 "$CI_DIR/stream-names.py"
