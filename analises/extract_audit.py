#!/usr/bin/env python3
# -*- coding: utf-8 -*-
import json, os, sys, re

OUT_FILE = sys.argv[1]
BASE = os.path.dirname(os.path.abspath(__file__))

with open(OUT_FILE, encoding="utf-8") as fh:
    raw = fh.read()

def find_obj(o, depth=0):
    """Encontra o objecto de retorno do workflow (que tem 'markdown' e 'audits')."""
    if depth > 6 or o is None:
        return None
    if isinstance(o, str):
        s = o.strip()
        if s.startswith('{'):
            try: return find_obj(json.loads(s), depth+1)
            except Exception: return None
        return None
    if isinstance(o, dict):
        if 'markdown' in o and 'audits' in o:
            return o
        for k in ('result','output','value','data'):
            if k in o:
                r = find_obj(o[k], depth+1)
                if r: return r
        for v in o.values():
            r = find_obj(v, depth+1)
            if r: return r
    return None

try:
    top = json.loads(raw)
except Exception:
    top = raw
obj = find_obj(top)
if not obj:
    print("ERRO: objecto de retorno não encontrado"); sys.exit(1)

SEV_ORDER = {'crítica':0, 'critica':0, 'alta':1, 'média':2, 'media':2, 'baixa':3}
def sev_key(s): return SEV_ORDER.get((s or '').lower().strip(), 4)

DIM_MAP = [
    ('completude',      '01_Completude.md',           'Completude'),
    ('coer',            '02_Coerencia_Interna.md',    'Coerência interna'),
    ('rigor',           '03_Rigor_Tecnico.md',        'Rigor técnico'),
    ('clareza',         '04_Clareza.md',              'Clareza'),
    ('rastreab',        '05_Rastreabilidade.md',      'Rastreabilidade de decisões'),
    ('viabilid',        '06_Viabilidade_Execucao.md', 'Viabilidade de execução'),
]
def dim_file(dimension):
    d = dimension.lower()
    for kw, fn, label in DIM_MAP:
        if kw in d: return fn, label
    return re.sub(r'\W+','_',d)[:30]+'.md', dimension

META = ("| Campo | Valor |\n|---|---|\n"
        "| Produto | AOS — Agentic OS de Referência |\n"
        "| Documento | Análise — {t} |\n"
        "| Versão | 1.0 |\n| Data | Julho de 2026 |\n"
        "| Classificação | Documento de Referência — Aberto |\n"
        "| Método | Auditoria multi-agente (painel de 6 + contra-auditoria) |\n")

# 00 — relatório consolidado (markdown da síntese)
with open(os.path.join(BASE, "00_Relatorio_Auditoria.md"), "w", encoding="utf-8") as fh:
    fh.write(obj['markdown'])

# 01–06 — relatório por dimensão
all_findings = []
for a in obj.get('audits', []):
    fn, label = dim_file(a['dimension'])
    findings = sorted(a.get('findings', []), key=lambda f: sev_key(f.get('severity')))
    for f in findings:
        all_findings.append({**f, 'dimensao': label})
    L = []
    L.append(f"# Análise de {label} — Documentação AOS\n")
    L.append(META.format(t=label) + "\n---\n")
    L.append(f"**Score:** {a.get('score','?')}/10\n")
    L.append(f"> **Veredicto:** {a.get('verdict','')}\n")
    L.append("## Pontos fortes\n")
    for s in a.get('strengths', []): L.append(f"- {s}")
    L.append("\n## Constatações\n")
    if findings:
        L.append("| ID | Severidade | Localização | Problema | Recomendação |")
        L.append("|---|---|---|---|---|")
        for f in findings:
            def c(x): return str(x or '').replace('|','\\|').replace('\n',' ')
            L.append(f"| {c(f.get('id'))} | {c(f.get('severity'))} | {c(f.get('location'))} | {c(f.get('issue'))} | {c(f.get('recommendation'))} |")
    else:
        L.append("_(sem constatações)_")
    L.append("\n## Lacunas identificadas\n")
    for g in a.get('gaps', []): L.append(f"- {g}")
    L.append("")
    with open(os.path.join(BASE, fn), "w", encoding="utf-8") as fh:
        fh.write("\n".join(L))

# Registo de constatações (todas + transversais)
ch = obj.get('challenge') or {}
for cc in ch.get('crossCutting', []):
    all_findings.append({'id':'TRANSV', 'severity':cc.get('severity'), 'location':'—',
                         'issue':cc.get('issue'), 'recommendation':cc.get('recommendation'), 'dimensao':'Transversal'})
all_findings.sort(key=lambda f: sev_key(f.get('severity')))
R = ["# Registo de Constatações — Auditoria à Documentação AOS\n",
     META.format(t="Registo de Constatações") + "\n---\n",
     f"**Total de constatações:** {len(all_findings)} "
     f"(críticas: {sum(1 for f in all_findings if sev_key(f['severity'])==0)}, "
     f"altas: {sum(1 for f in all_findings if sev_key(f['severity'])==1)}, "
     f"médias: {sum(1 for f in all_findings if sev_key(f['severity'])==2)}, "
     f"baixas: {sum(1 for f in all_findings if sev_key(f['severity'])==3)})\n",
     "| ID | Severidade | Dimensão | Localização | Recomendação |",
     "|---|---|---|---|---|"]
def c(x): return str(x or '').replace('|','\\|').replace('\n',' ')
for f in all_findings:
    R.append(f"| {c(f.get('id'))} | {c(f.get('severity'))} | {c(f.get('dimensao'))} | {c(f.get('location'))} | {c(f.get('recommendation'))} |")
R.append("\n## Riscos globais do conjunto documental\n")
for rr in ch.get('overallRisks', []): R.append(f"- {rr}")
R.append("\n## Constatações disputadas na contra-auditoria\n")
for d in ch.get('disputed', []): R.append(f"- **{c(d.get('finding'))}** — {c(d.get('reason'))}")
R.append("")
with open(os.path.join(BASE, "Registo_de_Constatacoes.md"), "w", encoding="utf-8") as fh:
    fh.write("\n".join(R))

print("OK. Ficheiros gerados em analises/:")
print(f"  overallScore={obj.get('overallScore')}  readiness={obj.get('readinessVerdict','')[:80]}")
print(f"  constatações totais={len(all_findings)}")
