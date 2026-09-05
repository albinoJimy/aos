#!/usr/bin/env bash
# estado-citado.sh — Gate «estado-citado»: uma declaração marcada como BLOQUEADOR não pode
# nomear um ticket já fechado (AOS-329, eixo DEF-814).
#
# Toda a lógica vive em estado-citado.py (Python 3 stdlib, zero dependências), no molde do
# `deferrals`. Fail-closed: sem `|| true`, sem `continue-on-error`, e sem caminho que devolva 0
# quando não consegue correr.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

log_gate "estado-citado — declaracao com BLOQUEADOR: AOS-NNN cruzada com o estado desse ticket"
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de ferramenta e
# nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 "$CI_DIR/estado-citado.py"
