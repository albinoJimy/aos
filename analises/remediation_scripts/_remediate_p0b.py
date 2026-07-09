#!/usr/bin/env python3
# -*- coding: utf-8 -*-
import os
ROOT = os.path.dirname(os.path.abspath(__file__))

def apply(path, old, new):
    full = os.path.join(ROOT, path)
    with open(full, encoding="utf-8") as fh: t = fh.read()
    c = t.count(old)
    if c:
        with open(full, "w", encoding="utf-8") as fh: fh.write(t.replace(old, new))
    return c

# correcao de prosa remanescente (EPIC-02 handoff/criterios)
c = apply("specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md",
          "AOS-002 (Reference Monitor) e AOS-005 (Event Store)",
          "AOS-003 (Reference Monitor) e AOS-002 (Event Store)")
print(f"[prosa] EPIC-02 dep-label: x{c}")

RHET_FILES = ["tecnica/00_Arquitectura_Solucao.md","tecnica/04_Memoria_Persistencia.md",
              "tecnica/07_Seguranca_Isolamento.md","specs/EPIC-04_Memoria_Persistencia.md",
              "specs/EPIC-07_Seguranca_Isolamento.md"]
tot=0
for f in RHET_FILES:
    tot += apply(f, "fisicamente incapazes", "estruturalmente impedidos")
    tot += apply(f, "fisicamente incapaz", "estruturalmente impedido")
print(f"[retorica] 'fisicamente incapaz' -> 'estruturalmente impedido': x{tot}")
print("FEITO.")
