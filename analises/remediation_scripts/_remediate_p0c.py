#!/usr/bin/env python3
# -*- coding: utf-8 -*-
import os, sys
ROOT = os.path.dirname(os.path.abspath(__file__))

def rep(path, old, new, exp):
    full = os.path.join(ROOT, path)
    with open(full, encoding="utf-8") as fh: t = fh.read()
    n = t.count(old)
    if n != exp:
        print(f"  ABORT {os.path.basename(path)}: esperado {exp}, achou {n}  key={old[:45]!r}")
        return False
    with open(full, "w", encoding="utf-8") as fh: fh.write(t.replace(old, new))
    print(f"  OK    {os.path.basename(path):42s} x{n}")
    return True

CALIB = ("\n\n> **Nota de calibração (rigor técnico).** Neste conjunto, *«arquitecturalmente impossível»* "
    "designa um **objectivo de desenho** — a eliminação estrutural do *caminho* de falha (não existe via "
    "de código que a produza) — e **não** uma garantia absoluta. Permanece **risco residual** (defeitos de "
    "implementação, comprometimento do TCB, canais laterais) que é **gerido e medido**, não negado.")

NFR_A_OLD = "| Durabilidade de execução | 0 efeitos duplicados no retry | Idempotency key por passo; saga de compensação (ADR-001) |"
NFR_A_NEW = "| Durabilidade de execução | Entrega *at-least-once* com idempotência downstream (0 efeitos **observáveis** duplicados) | Idempotency key por passo propagada e honrada pelo serviço externo; saga de compensação (ADR-001) |"
NFR_B_OLD = "| Overhead de mediação (RM) p95 | < 15 ms | PDP em memória com política compilada (ADR-002/011) |"
NFR_B_NEW = ("| Latência de avaliação do PDP p95 | < 15 ms | Política compilada avaliada em memória (ADR-002/011) |\n"
    "| Overhead total de mediação por *tool call* | orçamento decomposto por sub-passo (PDP + CAS de admissão + broker->vault + append ao ES + egress/DNS) | Alvo agregado a ratificar por benchmark |")

jobs = [
  # notas de calibração (P0.4)
  ("tecnica/00_Arquitectura_Solucao.md", "Tudo o resto assenta sobre estas três fronteiras.",
   "Tudo o resto assenta sobre estas três fronteiras." + CALIB, 1),
  ("specs/00_System_Spec.md", "o modelo é a *menor* camada; o fosso é o *runtime*, a coordenação e a governação.",
   "o modelo é a *menor* camada; o fosso é o *runtime*, a coordenação e a governação." + CALIB, 1),
  ("_BRIEF.md", "**identidade por agente** com delegação até um humano responsável, e **execução durável ao nível do passo**.",
   "**identidade por agente** com delegação até um humano responsável, e **execução durável ao nível do passo**. "
   "*(«arquitecturalmente impossível» é objectivo de desenho — eliminação estrutural do caminho de falha com risco residual gerido, não garantia absoluta.)*", 1),
  # NFR na fonte (P0.3) x3 ficheiros
  ("_BRIEF.md", NFR_A_OLD, NFR_A_NEW, 1),
  ("_BRIEF.md", NFR_B_OLD, NFR_B_NEW, 1),
  ("tecnica/00_Arquitectura_Solucao.md", NFR_A_OLD, NFR_A_NEW, 1),
  ("tecnica/00_Arquitectura_Solucao.md", NFR_B_OLD, NFR_B_NEW, 1),
  ("specs/00_System_Spec.md", NFR_A_OLD, NFR_A_NEW, 1),
  ("specs/00_System_Spec.md", NFR_B_OLD, NFR_B_NEW, 1),
  ("specs/00_System_Spec.md", "| Latência de mediação (RM) p95 | < 15 ms | overhead do gate por *tool call* |",
   "| Latência de avaliação do PDP p95 | < 15 ms | só avaliação de política; o overhead total de mediação decompõe-se por sub-passo |", 1),
  # correcções técnicas tecnica/07 (P0.4 / RIG-01,02)
  ("tecnica/07_Seguranca_Isolamento.md",
   "A rede do substrato é **default-deny**: uma execução não fala com nada que não esteja explicitamente na allowlist. Isto ataca directamente a exfiltração, o vector de maior severidade (CamoLeak). O controlo tem três camadas:",
   "A rede do substrato é **default-deny**: uma execução não fala com nada que não esteja explicitamente na allowlist. Isto **reduz** a superfície de exfiltração, o vector de maior severidade (CamoLeak), mas **não a elimina**: o padrão CamoLeak explora *canais permitidos* (domínios na allowlist, imagens/links remotos renderizados). Por isso o egress allowlist é complementado por **content-security** — bloqueio de renderização automática de recursos remotos e sanitização de markdown/HTML de saída — e pela filtragem DNS. O controlo tem três camadas:", 1),
  ("tecnica/07_Seguranca_Isolamento.md",
   "- **Hallucination gate reforçado:** deixa de apenas verificar a existência de um ID e passa a **autenticar origem + autoridade + referência** via assinatura — um sub-agente que alucina um resumo plausível não consegue fazer o pai agir sobre uma mentira sem assinatura válida.",
   "- **Hallucination gate reforçado:** deixa de apenas verificar a existência de um ID e passa a **autenticar origem + autoridade + integridade** via assinatura. Ressalva de rigor: a assinatura garante *origem e não-repúdio* (a mensagem vem mesmo daquele sub-agente e não foi adulterada), **não** a *veracidade* do conteúdo — uma mensagem validamente assinada pode conter uma alucinação. Impedir o pai de agir sobre uma mentira exige adicionalmente *grounding*/verificação por evals (ver `tecnica/08`), não apenas assinatura.", 1),
]

print("== P0.3/P0.4 — NFR na fonte + calibração + correcções técnicas ==")
ok = True
for f,o,n,e in jobs:
    ok = rep(f,o,n,e) and ok
sys.exit(0 if ok else 1)
