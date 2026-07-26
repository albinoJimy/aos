#!/usr/bin/env bash
# event-catalog.sh — Gate do catálogo de tipos de evento (AOS-198; CA3 residual
# de AOS-201, pendência registada em tecnica/13 §8.1).
#
# Toda a lógica vive em event-catalog.py (Python 3 stdlib, zero dependências).
# Fail-closed: sem `|| true`, sem `continue-on-error`, e sem caminho que devolva
# 0 quando não consegue correr.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

log_gate "event-catalog — constantes junto do emissor · prefixo conhecido · zero literais"
python3 "$CI_DIR/event-catalog.py"
