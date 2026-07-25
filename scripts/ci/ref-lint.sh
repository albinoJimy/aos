#!/usr/bin/env bash
# ref-lint.sh — Gate 2b: validação de referências cruzadas AOS/ADR (AOS-186).
set -uo pipefail
python3 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ref-lint.py"
