#!/usr/bin/env bash
# ref-lint.sh — Gate 2b: validação de referências cruzadas AOS/ADR (AOS-186).
set -uo pipefail
# Este gate nao carregava a biblioteca; passa a carrega-la SO para o ensure_python.
# O interpretador e uma DEPENDENCIA: sem ele o vermelho seria por falta de ferramenta
# e nao por defeito. Ver [ensure_python] em lib.sh.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
ensure_python || exit 1
python3 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ref-lint.py"
