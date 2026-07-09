#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Remediação P0 — substituições PRECISAS (string-a-string, com contagem esperada).
NÃO é find-replace cego: cada par visa uma string específica identificada na auditoria."""
import os, sys

ROOT = os.path.dirname(os.path.abspath(__file__))

# (ficheiro, antigo, novo, ocorrências_esperadas)
DEPS = [
    # EPIC-02 — RM é AOS-003 (não 002); Event Store é AOS-002 (não 005)
    ("specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md",
     "AOS-002 (Reference Monitor), AOS-005 (Event Store replicado)",
     "AOS-003 (Reference Monitor), AOS-002 (Event Store replicado)", 1),
    ("specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md",
     "AOS-013, AOS-005 (Event Store replicado)",
     "AOS-013, AOS-002 (Event Store replicado)", 1),
    ("specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md",
     "AOS-017, AOS-005 (Event Store replicado)",
     "AOS-017, AOS-002 (Event Store replicado)", 1),
    ("specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md",
     "AOS-013, AOS-014, AOS-002 (Reference Monitor)",
     "AOS-013, AOS-014, AOS-003 (Reference Monitor)", 1),
    # EPIC-03
    ("specs/EPIC-03_Orquestracao_Escalonamento.md",
     "AOS-025 (grafo de tarefas), AOS-002 (Reference Monitor mandatório)",
     "AOS-025 (grafo de tarefas), AOS-003 (Reference Monitor mandatório)", 1),
    ("specs/EPIC-03_Orquestracao_Escalonamento.md",
     "AOS-008 (Event Store replicado / transporte push), AOS-026",
     "AOS-002 (Event Store replicado / transporte push), AOS-026", 1),
    # EPIC-07 — (RM,Event Store) mislabel aparece 2x; +2 correcções pontuais
    ("specs/EPIC-07_Seguranca_Isolamento.md",
     "AOS-001 (Reference Monitor), AOS-005 (Event Store)",
     "AOS-003 (Reference Monitor), AOS-002 (Event Store)", 2),
    ("specs/EPIC-07_Seguranca_Isolamento.md",
     "AOS-005 (taint/identidade), AOS-070",
     "AOS-005 (identidade NHI), AOS-070 (credential broker)", 1),
    ("specs/EPIC-07_Seguranca_Isolamento.md",
     "AOS-001 (Event Store)",
     "AOS-002 (Event Store)", 1),
]

# Calibração da retórica: "fisicamente incapaz" → "estruturalmente impedido"
RHET_FILES = [
    "tecnica/00_Arquitectura_Solucao.md", "tecnica/04_Memoria_Persistencia.md",
    "tecnica/07_Seguranca_Isolamento.md",
    "specs/EPIC-04_Memoria_Persistencia.md", "specs/EPIC-07_Seguranca_Isolamento.md",
]
RHET = [("fisicamente incapazes", "estruturalmente impedidos"),
        ("fisicamente incapaz", "estruturalmente impedido")]

def apply(path, old, new, expected=None):
    full = os.path.join(ROOT, path)
    with open(full, encoding="utf-8") as fh: t = fh.read()
    n = t.count(old)
    if expected is not None and n != expected:
        print(f"  !! ABORTAR {path}: esperado {expected}, encontrado {n} de: {old[:50]!r}")
        return False, 0
    if n == 0: return True, 0
    t = t.replace(old, new)
    with open(full, "w", encoding="utf-8") as fh: fh.write(t)
    return True, n

print("== P0.1 — reconciliação de dependências ==")
ok_all = True
for f, o, n2, exp in DEPS:
    ok, cnt = apply(f, o, n2, exp)
    ok_all = ok_all and ok
    if ok and cnt: print(f"  OK  {os.path.basename(f):45s} x{cnt}  {o[:38]}…")
if not ok_all:
    print("ABORTADO na fase de dependências — nenhuma alteração de retórica aplicada.")
    sys.exit(1)

print("== P0.4 — calibração 'fisicamente incapaz' → 'estruturalmente impedido' ==")
tot = 0
for f in RHET_FILES:
    for o, n2 in RHET:
        _, c = apply(f, o, n2)
        tot += c
        if c: print(f"  OK  {os.path.basename(f):45s} x{c}  {o}")
print(f"total retórica: {tot}")
print("FEITO.")
