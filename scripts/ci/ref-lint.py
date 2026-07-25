#!/usr/bin/env python3
"""
ref-lint.py — Gate 2b: validação de referências cruzadas do corpus documental.

Verifica:
1. Todos os AOS-NNN citados em specs/, docs/adr/ e tecnica/ (excepto a própria RTM)
       existem no backlog (specs/EPIC-*.md).
2. Todos os ADR-001..ADR-019 canónicos têm pelo menos um ticket implementador
       no backlog.
3. Todos os ADR-NNN citados em specs/ existem no catálogo de ADRs
       (docs/adr/README.md e specs/00_System_Spec.md §11).

Saída fail-closed: exit != 0 quando há referências quebradas.

Uso:
    python3 scripts/ci/ref-lint.py
"""

import os
import re
import sys
from collections import defaultdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SPECS_DIR = REPO_ROOT / "specs"
DOCS_ADR_DIR = REPO_ROOT / "docs" / "adr"
TECNICA_DIR = REPO_ROOT / "tecnica"

ADR_RANGE = [f"ADR-{i:03d}" for i in range(1, 20)]


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def extract_backlog() -> dict:
    """Extrai todos os AOS-NNN válidos do backlog e os ADRs que citam."""
    tickets = {}
    for epic_file in sorted(SPECS_DIR.glob("EPIC-*.md")):
        text = _read(epic_file)

        # Secções detalhadas (fonte primária para ADRs)
        for m in re.finditer(r"^#{2,3} (AOS-\d{3})\s*[-–—]\s*(.*?)$", text, re.MULTILINE):
            aos = m.group(1)
            start = m.end()
            next_h = re.search(r"\n#{2,3} (AOS-\d{3})\s*[-–—]", text[start:])
            block = text[start : start + next_h.start()] if next_h else text[start:]
            adrs = set(re.findall(r"ADR-\d{3}", block))
            tickets[aos] = {
                "adrs": adrs,
                "file": epic_file,
            }

        # Tabelas de resumo (garantem que tickets sem secção detalhada constam)
        for line in text.splitlines():
            m = re.match(r"\|\s*(AOS-\d{3})(?:\s*[^|]*)?\s*\|", line)
            if m:
                aos = m.group(1)
                if aos not in tickets:
                    tickets[aos] = {"adrs": set(), "file": epic_file}
    return tickets


def extract_adr_catalog() -> set:
    """Devolve o conjunto de ADRs conhecidos no catálogo."""
    known = set()
    readme = DOCS_ADR_DIR / "README.md"
    if readme.exists():
        for line in _read(readme).splitlines():
            for adr in re.findall(r"ADR-\d{3}", line):
                known.add(adr)
    sys_spec = SPECS_DIR / "00_System_Spec.md"
    if sys_spec.exists():
        for line in _read(sys_spec).splitlines():
            for adr in re.findall(r"ADR-\d{3}", line):
                known.add(adr)
    return known


def extract_citations(paths: list, skip_paths: set) -> dict:
    """
    Devolve dict {path: {"aos": set, "adr": set}} para todos os ficheiros .md
    e .go sob as directorias indicadas, excepto os caminhos em skip_paths.
    """
    citations = {}
    for base in paths:
        for path in base.rglob("*"):
            if path in skip_paths:
                continue
            if not path.is_file():
                continue
            if path.suffix not in (".md", ".go"):
                continue
            text = _read(path)
            aos = set(re.findall(r"AOS-\d{3}", text))
            adr = set(re.findall(r"ADR-\d{3}", text))
            citations[path] = {"aos": aos, "adr": adr}
    return citations


def main() -> int:
    backlog = extract_backlog()
    backlog_set = set(backlog.keys())
    adr_catalog = extract_adr_catalog()

    # Garantir que o catálogo inclui os 19 ADRs canónicos, mesmo que ainda não
    # materializados individualmente.
    adr_catalog |= set(ADR_RANGE)

    skip = {REPO_ROOT / "tecnica" / "16_Rastreabilidade_RTM.md"}
    citations = extract_citations([SPECS_DIR, DOCS_ADR_DIR, TECNICA_DIR], skip)

    broken_aos = defaultdict(list)
    for path, refs in citations.items():
        for aos in refs["aos"]:
            if aos not in backlog_set:
                broken_aos[aos].append(str(path))

    broken_adr_refs = defaultdict(list)
    for path, refs in citations.items():
        for adr in refs["adr"]:
            if adr not in adr_catalog:
                broken_adr_refs[adr].append(str(path))

    # ADR canónicos sem ticket implementador
    adrs_to_tickets = defaultdict(list)
    for aos, info in backlog.items():
        for adr in info["adrs"]:
            adrs_to_tickets[adr].append(aos)

    uncovered_adrs = [adr for adr in ADR_RANGE if not adrs_to_tickets[adr]]

    exit_code = 0

    if broken_aos:
        print(f"ERRO: {len(broken_aos)} AOS-NNN citado(s) não existem no backlog:")
        for aos in sorted(broken_aos):
            print(f"  - {aos}: {', '.join(broken_aos[aos])}")
        exit_code = 1

    if broken_adr_refs:
        print(f"ERRO: {len(broken_adr_refs)} ADR-NNN citado(s) não existem no catálogo:")
        for adr in sorted(broken_adr_refs):
            print(f"  - {adr}: {', '.join(broken_adr_refs[adr])}")
        exit_code = 1

    if uncovered_adrs:
        print(f"ERRO: {len(uncovered_adrs)} ADR canónico(s) sem ticket implementador:")
        for adr in uncovered_adrs:
            print(f"  - {adr}")
        exit_code = 1

    if exit_code == 0:
        print(
            f"Referências cruzadas OK: "
            f"{len(backlog_set)} tickets no backlog, "
            f"{len(ADR_RANGE)} ADRs canónicos com cobertura, "
            f"{len(citations)} ficheiros verificados."
        )

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
