#!/usr/bin/env bash
# deferrals.sh — Gate «deferrals»: eixo verificável para todo o deferimento
# declarado no código (AOS-196, achados DEF-01/DEF-03/DEF-06 da auditoria v4).
#
# Toda a lógica vive em deferrals.py (Python 3 stdlib, zero dependências).
# Fail-closed: sem `|| true`, sem `continue-on-error`, e sem caminho que devolva
# 0 quando não consegue correr.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

log_gate "deferrals — marcador com entrada no registo · eixo com ticket EXISTENTE · POR ATRIBUIR com nota"
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de
# ferramenta e nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 "$CI_DIR/deferrals.py"
