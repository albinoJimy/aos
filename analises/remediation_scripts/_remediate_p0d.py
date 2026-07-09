#!/usr/bin/env python3
# -*- coding: utf-8 -*-
import os, sys
ROOT = os.path.dirname(os.path.abspath(__file__))
def rep(path, old, new, exp):
    full = os.path.join(ROOT, path)
    with open(full, encoding="utf-8") as fh: t = fh.read()
    n = t.count(old)
    if n != exp:
        print(f"  ABORT {os.path.basename(path)}: esperado {exp} achou {n} key={old[:50]!r}"); return False
    with open(full, "w", encoding="utf-8") as fh: fh.write(t.replace(old, new))
    print(f"  OK    {os.path.basename(path):42s} x{n}"); return True

E2 = "specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md"
jobs = [
  # VIAB-01: rótulos residuais no EPIC-02 (AOS-005=identidade usado como Event Store=AOS-002; RM=AOS-003)
  (E2, "| AOS-013 | Loop do agente (montar → chamar → despachar → verificar) | feature | M | P0 | AOS-002 (RM), AOS-005 (ES) |",
       "| AOS-013 | Loop do agente (montar → chamar → despachar → verificar) | feature | M | P0 | AOS-003 (RM), AOS-002 (ES) |", 1),
  (E2, "| P0 | AOS-013, AOS-005 |", "| P0 | AOS-013, AOS-002 |", 1),
  (E2, "| P0 | AOS-017, AOS-005 |", "| P0 | AOS-017, AOS-002 |", 1),
  (E2, "Os tickets `AOS-002` (Reference Monitor) e `AOS-005` (Event Store replicado)",
       "Os tickets `AOS-003` (Reference Monitor) e `AOS-002` (Event Store replicado)", 1),
  (E2, "Confirma AOS-013 e AOS-005 Done.", "Confirma AOS-013 e AOS-002 Done.", 1),
  (E2, "Confirma AOS-017 e AOS-005 Done.", "Confirma AOS-017 e AOS-002 Done.", 1),
  # RAST-01: religar docs 12/13/14 aos "Documentos relacionados" dos epics
  ("specs/EPIC-01_Fundacoes_Plano_Controlo.md",
   "`specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md` |",
   "`specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/13_Modelo_Dados_Eventos.md` |", 1),
  (E2,
   "`specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md` |",
   "`specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/13_Modelo_Dados_Eventos.md` |", 1),
  ("specs/EPIC-04_Memoria_Persistencia.md",
   "`specs/EPIC-08_Observabilidade_Evals.md`, `specs/01_Engineering_Standards_e_Handoff.md` |",
   "`specs/EPIC-08_Observabilidade_Evals.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/13_Modelo_Dados_Eventos.md` |", 1),
  ("specs/EPIC-07_Seguranca_Isolamento.md",
   "`specs/EPIC-09_Governacao_Conformidade.md`, `specs/01_Engineering_Standards_e_Handoff.md` |",
   "`specs/EPIC-09_Governacao_Conformidade.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/13_Modelo_Dados_Eventos.md` |", 1),
  ("specs/EPIC-08_Observabilidade_Evals.md",
   "`tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/09_Governacao_Conformidade.md` |",
   "`tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/09_Governacao_Conformidade.md`, `tecnica/13_Modelo_Dados_Eventos.md` |", 1),
  ("specs/EPIC-09_Governacao_Conformidade.md",
   "`specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md` |",
   "`specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/14_Matriz_Conformidade.md` |", 1),
]
ok = True
print("== P0.1b (rótulos residuais) + RAST-01 (religar 12/13/14) ==")
for f,o,n,e in jobs: ok = rep(f,o,n,e) and ok
sys.exit(0 if ok else 1)
