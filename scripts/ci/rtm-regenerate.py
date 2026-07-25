#!/usr/bin/env python3
"""
rtm-regenerate.py — Regenera as secções §4 (Matriz ADR × ticket) e §5
(Matriz NFR × ticket de verificação) do `tecnica/16_Rastreabilidade_RTM.md`
a partir do corpus de specs, ADRs e código Go.

Uso:
    python3 scripts/ci/rtm-regenerate.py [--check]

Com --check, valida que a RTM actual está sincronizada com o corpus e sai != 0
se divergir (útil para gate CI). Sem --check, reescreve o ficheiro.
"""

import argparse
import os
import re
import sys
from collections import defaultdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
RTM_PATH = REPO_ROOT / "tecnica" / "16_Rastreabilidade_RTM.md"
SPECS_DIR = REPO_ROOT / "specs"
DOCS_ADR_DIR = REPO_ROOT / "docs" / "adr"

# --- ADRs canónicos (AOS-186: cobertura ADR-001..ADR-019) ---
ADR_RANGE = [f"ADR-{i:03d}" for i in range(1, 20)]

# --- NFRs (ordem e ADRs de origem conforme System Spec §7 / _BRIEF §4) ---
NFR_SPECS = [
    ("NFR-01", "Latência de avaliação do PDP (p95)", "< 15 ms", {"ADR-002", "ADR-011"}),
    ("NFR-02", "Cold-start de sandbox", "< 125 ms", {"ADR-004"}),
    ("NFR-03", "Cache-hit-rate de prompt", "> 80% (SLI)", {"ADR-009"}),
    ("NFR-04", "Disponibilidade do plano de controlo", "99,9%", {"ADR-007"}),
    ("NFR-05", "Durabilidade de execução", "0 efeitos observáveis duplicados", {"ADR-001"}),
    ("NFR-06", "Fidelidade de replay", "100% dos passos reproduzíveis", {"ADR-010"}),
    ("NFR-07", "Overhead total de mediação por tool call", "orçamento decomposto por sub-passo", set()),
    ("NFR-08", "Isolamento de segredos", "Agente nunca vê segredo downstream", {"ADR-006"}),
    ("NFR-09", "Conformidade regulatória", "GDPR/EU AI Act por desenho", {"ADR-011", "ADR-013"}),
    ("NFR-10", "Segurança de auto-evolução", "0 auto-modificações não avaliadas em prod", {"ADR-012"}),
]

# Mapeamento NFR -> tickets de verificação preferidos (justificados no corpus).
# Gerado semi-automaticamente: para NFRs com ADRs, procura tickets que citam
# esses ADRs; estes são os tickets de verificação canónicos.
NFR_MANUAL_TICKETS = {
    "NFR-01": {"AOS-113", "AOS-116"},
    "NFR-02": {"AOS-065", "AOS-116"},
    "NFR-03": {"AOS-085", "AOS-086", "AOS-115"},
    "NFR-04": {"AOS-118", "AOS-116"},
    "NFR-05": {"AOS-112", "AOS-118"},
    "NFR-06": {"AOS-111", "AOS-118"},
    "NFR-07": {"AOS-116"},
    "NFR-08": {"AOS-117"},
    "NFR-09": {"AOS-113", "AOS-091", "AOS-092"},
    "NFR-10": {"AOS-114", "AOS-115"},
}


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def _write(path: Path, text: str) -> None:
    path.write_text(text, encoding="utf-8")


def extract_adr_titles() -> dict:
    """Lê docs/adr/README.md e devolve {ADR-NNN: título}."""
    titles = {}
    text = _read(DOCS_ADR_DIR / "README.md")
    # Tabela de ADRs materializados
    for line in text.splitlines():
        m = re.match(r"\| (ADR-\d{3}) \| (.*?) \|", line)
        if m:
            titles[m.group(1)] = m.group(2).strip()
    # Fallback: catálogo do System Spec §11 para ADRs ainda não materializados
    if len(titles) < len(ADR_RANGE):
        sys_text = _read(SPECS_DIR / "00_System_Spec.md")
        for line in sys_text.splitlines():
            m = re.match(r"\| (ADR-\d{3}) \| (.*?) \|", line)
            if m and m.group(1) not in titles:
                titles[m.group(1)] = m.group(2).strip()
    return titles


def extract_all_tickets() -> dict:
    """
    Percorre specs/EPIC-*.md e devolve:
      {AOS-NNN: {"epic": str, "title": str, "adrs": set, "file": Path}}

    Tickets são identificados quer por cabeçalhos de detalhe (##/### AOS-NNN — Título)
    quer por linhas de tabela de resumo (| AOS-NNN | Título | ... |). A primeira fonte
    prevalece para o título e ADRs; a tabela garante que tickets sem secção detalhada
    (ex.: AOS-170..177) ainda constam do inventário.
    """
    tickets = {}
    for epic_file in sorted(SPECS_DIR.glob("EPIC-*.md")):
        text = _read(epic_file)
        epic = epic_file.stem

        # 1. Tabelas de resumo de tickets (fonte secundária)
        for line in text.splitlines():
            # | AOS-163 | Bootstrap do nó ... | feature | M | P0 | ... |
            # | AOS-174 ✅ | `HumanDirectory` ... | feature | M | P1 | ... |
            m = re.match(r"\|\s*(AOS-\d{3})(\s*[^|]*)?\s*\|\s*(.*?)\s*\|\s*(feature|chore|spike|fix|docs)", line, re.IGNORECASE)
            if not m:
                continue
            aos, title = m.group(1), m.group(3).strip()
            # Limpar marcadores de estado (✅, ENTREGUE, etc.) do título
            title = re.sub(r"\s*\b(ENTREGUE|✅)\b", "", title, flags=re.IGNORECASE).strip()
            title = re.sub(r"\s*[-–—]\s*\*\*ENTREGUE.*", "", title, flags=re.IGNORECASE).strip()
            if aos not in tickets:
                tickets[aos] = {
                    "epic": epic,
                    "title": title,
                    "adrs": set(),
                    "file": epic_file,
                }
            else:
                tickets[aos]["title"] = title  # título da tabela é mais limpo

        # 2. Secções detalhadas (fonte primária para ADRs)
        for m in re.finditer(r"^#{2,3} (AOS-\d{3})\s*[-–—]\s*(.*?)$", text, re.MULTILINE):
            aos = m.group(1)
            title = m.group(2).strip()
            start = m.end()
            # Fim do bloco: próximo cabeçalho de mesmo nível ou fim
            next_h = re.search(r"\n#{2,3} (AOS-\d{3})\s*[-–—]", text[start:])
            block = text[start : start + next_h.start()] if next_h else text[start:]
            adrs = set(re.findall(r"ADR-\d{3}", block))
            if aos not in tickets:
                tickets[aos] = {
                    "epic": epic,
                    "title": title,
                    "adrs": adrs,
                    "file": epic_file,
                }
            else:
                tickets[aos]["adrs"] |= adrs
                if title:
                    tickets[aos]["title"] = title
    return tickets


def build_adr_matrix(tickets: dict, adr_titles: dict) -> list:
    """Devolve lista de dicts com colunas da tabela §4."""
    adr_to_tickets = defaultdict(list)
    for aos, info in tickets.items():
        for adr in info["adrs"]:
            adr_to_tickets[adr].append(aos)
    rows = []
    for adr in ADR_RANGE:
        tickets_for = sorted(set(adr_to_tickets.get(adr, [])))
        # Doc técnico: inferido a partir do range de tickets
        docs = infer_docs_for_tickets(tickets_for, tickets)
        rows.append({
            "adr": adr,
            "title": adr_titles.get(adr, "*título não encontrado*"),
            "count": len(tickets_for),
            "tickets": tickets_for,
            "docs": docs,
        })
    return rows


DOC_RANGES = [
    (("AOS-001", "AOS-012"), ["`tecnica/01`"]),
    (("AOS-013", "AOS-024"), ["`tecnica/02`"]),
    (("AOS-025", "AOS-034"), ["`tecnica/03`"]),
    (("AOS-035", "AOS-044"), ["`tecnica/04`"]),
    (("AOS-045", "AOS-054"), ["`tecnica/05`"]),
    (("AOS-055", "AOS-063"), ["`tecnica/06`"]),
    (("AOS-064", "AOS-075"), ["`tecnica/07`"]),
    (("AOS-076", "AOS-086"), ["`tecnica/08`"]),
    (("AOS-087", "AOS-097"), ["`tecnica/09`"]),
    (("AOS-098", "AOS-108"), ["`tecnica/10`"]),
    (("AOS-109", "AOS-118"), ["`tecnica/11`"]),
    (("AOS-119", "AOS-128"), ["`tecnica/15`"]),
    (("AOS-129", "AOS-143"), ["`tecnica/15`", "`tecnica/12`"]),
    (("AOS-144", "AOS-162"), ["`tecnica/12`", "`tecnica/02`"]),
    (("AOS-163", "AOS-173"), ["`tecnica/10`", "`tecnica/12`"]),
    (("AOS-174", "AOS-189"), ["`tecnica/09`", "`tecnica/12`"]),
]


def aos_key(aos: str) -> int:
    return int(aos.split("-")[1])


def infer_docs_for_tickets(tickets_for: list, tickets: dict) -> str:
    if not tickets_for:
        return "—"
    nums = [aos_key(t) for t in tickets_for]
    low, high = min(nums), max(nums)
    docs = set()
    for (rlow, rhigh), doc_list in DOC_RANGES:
        rl, rh = aos_key(rlow), aos_key(rhigh)
        if max(rl, low) <= min(rh, high):
            docs.update(doc_list)
    if not docs:
        return "—"
    return ", ".join(sorted(docs, key=lambda x: x.lower()))


def generate_section4(rows: list) -> str:
    lines = [
        "## 4. Matriz ADR × ticket",
        "",
        "Para cada ADR-001…019, os tickets `AOS-NNN` cujo bloco de especificação o cita explicitamente (extracção por correspondência textual sobre `specs/EPIC-*.md`) e o(s) documento(s) técnico(s) que o desenvolvem. A coluna **Nº** é a contagem de tickets implementadores distintos.",
        "",
        "| ADR | Decisão | Nº | Tickets `AOS-NNN` que o implementam | Doc(s) técnico(s) |",
        "|---|---|---|---|---|",
    ]
    for r in rows:
        tickets_str = ", ".join(r["tickets"]) if r["tickets"] else "—"
        lines.append(f"| **{r['adr']}** | {r['title']} | {r['count']} | {tickets_str} | {r['docs']} |")

    # Linha de cobertura
    uncovered = [r["adr"] for r in rows if r["count"] == 0]
    if uncovered:
        lines.append("")
        lines.append(f"**Cobertura: {len(rows)-len(uncovered)}/{len(rows)} ADRs têm ≥ 1 ticket implementador.** ADRs sem tickets: {', '.join(uncovered)}.")
    else:
        lines.append("")
        lines.append(f"**Cobertura: {len(rows)}/{len(rows)} ADRs têm ≥ 1 ticket implementador.**")
    lines.append("")

    # Sub-cobertura: ADRs com <= 3 tickets
    sub = [r for r in rows if 0 < r["count"] <= 3]
    if sub:
        lines.append("- **Sub-cobertura (≤3 tickets):** ")
        for r in sub:
            lines.append(f"  - **{r['adr']}** ({r['title']}) — {r['count']} ticket(s): {', '.join(r['tickets'])}.")
    lines.append("")
    lines.append("")
    return "\n".join(lines)


def generate_section5(tickets: dict) -> str:
    lines = [
        "## 5. Matriz NFR × ticket de verificação",
        "",
        "Para cada NFR, o(s) ticket(s) que o **testam/verificam** com o limiar respectivo. Os testes de domínio residem em EPIC-11 (`specs/EPIC-11_Testes_Qualidade.md`) e são os *gates* 3, 4, 7, 8 e 9 do *pipeline* fail-closed (`specs/01` §4); alguns limiares são também medidos em produção via SLIs de EPIC-08.",
        "",
        "| NFR | Alvo | Ticket(s) de verificação | Como se prova |",
        "|---|---|---|---|",
    ]
    proofs = {
        "NFR-01": "Benchmark de avaliação de política sob carga; p95 reportado como sinal",
        "NFR-02": "AOS-065 fixa o alvo <125 ms; AOS-116 valida sob concorrência",
        "NFR-03": "SLI de *prefix caching* com alerta de *thrash*; regressão apanhada por trace-diff",
        "NFR-04": "Falha de nó → promoção de réplica → *resume-from-step* sem perda",
        "NFR-05": "Injecção de crash por passo; ausência de efeito duplicado no retry",
        "NFR-06": "Reprodução passo-a-passo vs. baseline; `Replay-fidelity`",
        "NFR-07": "Decomposição do overhead p95 por sub-passo sob saturação",
        "NFR-08": "Tentativa de exfiltração de credencial downstream falha",
        "NFR-09": "DSAR satisfeito por crypto-shredding sem quebrar o log encadeado",
        "NFR-10": "Eval-gate barra promoção sem *golden-set* aprovado",
    }
    for nfr, name, target, adrs in NFR_SPECS:
        verif = sorted(NFR_MANUAL_TICKETS.get(nfr, set()))
        # Validação: todos os tickets manuais existem no corpus
        missing = [t for t in verif if t not in tickets]
        if missing:
            sys.stderr.write(f"AVISO: {nfr} referencia tickets inexistentes: {missing}\n")
        verif_str = ", ".join(verif) if verif else "—"
        lines.append(f"| **{nfr}** | {target} | {verif_str} | {proofs[nfr]} |")
    lines.append("")
    lines.append("**Cobertura: 10/10 NFRs têm ≥ 1 ticket de verificação.**")
    lines.append("")
    return "\n".join(lines)


def update_section1(rtm_text: str) -> str:
    """Actualiza §1.2 (âmbito) e §1.5 (ADRs aplicáveis) para 17 epics / 19 ADRs."""
    # §1.2
    rtm_text = re.sub(
        r"A rastreabilidade cobre os \d+ ADRs canónicos \(`_BRIEF` §3\), as \d+ capacidades funcionais \(`specs/00` §4\), os \d+ \*drivers\* não-funcionais \(`specs/00` §7\) e os \*\*\d+ tickets\*\* `AOS-001`–`AOS-\d+` distribuídos por \d+ epics\.",
        "A rastreabilidade cobre os 19 ADRs canónicos (`_BRIEF` §3), as 11 capacidades funcionais (`specs/00` §4), os 10 *drivers* não-funcionais (`specs/00` §7) e os **189 tickets** `AOS-001`–`AOS-189` distribuídos por 17 epics.",
        rtm_text,
    )
    # §1.5
    rtm_text = re.sub(
        r"Este documento não introduz decisões de arquitectura; \*\*rastreia\*\* as \d+ existentes \(ADR-001 a ADR-\d+, `_BRIEF` §3\)\.",
        "Este documento não introduz decisões de arquitectura; **rastreia** as 19 existentes (ADR-001 a ADR-019, `_BRIEF` §3).",
        rtm_text,
    )
    return rtm_text


def generate_section6(tickets: dict) -> str:
    """Gera a tabela de rasto descendente documento técnico → epic → tickets."""
    lines = [
        "## 6. Rasto descendente: documento técnico → epic → tickets",
        "",
        "O *back-link* que faltava (RAST). Cada documento de `tecnica/` mapeia para o(s) epic(s) e a gama de tickets que o realizam. As gamas por epic seguem `_BRIEF` §8.",
        "",
        "| Doc técnico | Epic(s) implementador(es) | Gama de tickets |",
        "|---|---|---|",
        "| `tecnica/00_Arquitectura_Solucao.md` | Todos (transversal) | AOS-001 – AOS-189 |",
        "| `tecnica/01_Reference_Monitor_Plano_Controlo.md` | EPIC-01 | AOS-001 – AOS-012 |",
        "| `tecnica/02_Agent_Runtime_Execucao_Duravel.md` | EPIC-02 | AOS-013 – AOS-024 |",
        "| `tecnica/03_Orquestracao_Escalonamento.md` | EPIC-03 | AOS-025 – AOS-034 |",
        "| `tecnica/04_Memoria_Persistencia.md` | EPIC-04 | AOS-035 – AOS-044 |",
        "| `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` | EPIC-05 | AOS-045 – AOS-054 |",
        "| `tecnica/06_Model_Gateway_Custos.md` | EPIC-06 | AOS-055 – AOS-063 |",
        "| `tecnica/07_Seguranca_Isolamento.md` | EPIC-07 | AOS-064 – AOS-075 |",
        "| `tecnica/08_Observabilidade_Evals.md` | EPIC-08 | AOS-076 – AOS-086 |",
        "| `tecnica/09_Governacao_Conformidade.md` | EPIC-09 | AOS-087 – AOS-097 |",
        "| `tecnica/10_Topologia_Implantacao_Operacao.md` | EPIC-10, EPIC-11 | AOS-098 – AOS-108 (+ AOS-118) |",
        "| `tecnica/11_Convencoes_Engenharia_Evolucao.md` | EPIC-11 (+ EPIC-05 auto-mod) | AOS-109 – AOS-118 (+ AOS-045–054) |",
        "| `tecnica/12_Contratos_de_Interface.md` | EPIC-01, EPIC-05, EPIC-06, EPIC-14 | AOS-003, 004; AOS-045–054; AOS-055–063; AOS-144–162 |",
        "| `tecnica/13_Modelo_Dados_Eventos.md` | EPIC-04, EPIC-05, EPIC-08 | AOS-035–044, AOS-045–054, AOS-076–086 |",
        "| `tecnica/14_Matriz_Conformidade.md` | EPIC-08, EPIC-09 | AOS-072, 076–097 |",
        "",
        "```mermaid",
        "flowchart LR",
        '    RF["RF-01..RF-11 (capacidades)"] --> ADR["ADR-001..019 (decisoes)"]',
        '    NFR["NFR-01..NFR-10 (drivers)"] --> ADR',
        '    ADR --> EPIC["EPIC-01..EPIC-17 (entregas)"]',
        '    EPIC --> TICK["AOS-001..AOS-189 (tickets)"]',
        '    DOC["tecnica/00..14 (docs)"] --> EPIC',
        '    TICK --> TEST["EPIC-11: AOS-109..118 (verificacao)"]',
        "    NFR --> TEST",
        "```",
        "",
    ]
    return "\n".join(lines)


def regenerate_rtm(tickets: dict, adr_titles: dict) -> str:
    rtm_text = _read(RTM_PATH)

    # Actualiza §1
    rtm_text = update_section1(rtm_text)

    # Substitui §4–§5
    sec4 = generate_section4(build_adr_matrix(tickets, adr_titles))
    sec5 = generate_section5(tickets)
    new_middle = sec4 + sec5
    pattern = re.compile(r"(## 4\. Matriz ADR × ticket.*?)(?=\n## 6\. )", re.DOTALL)
    m = pattern.search(rtm_text)
    if not m:
        sys.stderr.write("ERRO: não encontrou secções §4–§5 no RTM\n")
        sys.exit(1)
    rtm_text = rtm_text[:m.start()] + new_middle + rtm_text[m.end():]

    # Substitui §6
    sec6 = generate_section6(tickets)
    pattern6 = re.compile(r"## 6\. Rasto descendente: documento técnico → epic → tickets.*?\n---", re.DOTALL)
    m6 = pattern6.search(rtm_text)
    if not m6:
        sys.stderr.write("ERRO: não encontrou secção §6 no RTM\n")
        sys.exit(1)
    rtm_text = rtm_text[:m6.start()] + sec6 + "\n---" + rtm_text[m6.end():]
    return rtm_text


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="Valida sincronia sem escrever")
    args = parser.parse_args()

    tickets = extract_all_tickets()
    adr_titles = extract_adr_titles()

    # Validação: tickets AOS-001..AOS-189 presentes
    all_aos = set(tickets.keys())
    expected = {f"AOS-{i:03d}" for i in range(1, 190)}
    missing = sorted(expected - all_aos)
    if missing:
        sys.stderr.write(f"AVISO: tickets esperados mas não encontrados: {missing[:10]}{'...' if len(missing) > 10 else ''}\n")

    new_text = regenerate_rtm(tickets, adr_titles)
    if args.check:
        current = _read(RTM_PATH)
        if current == new_text:
            print("RTM está sincronizada com o corpus.")
            sys.exit(0)
        else:
            sys.stderr.write("ERRO: RTM diverge do corpus. Correr sem --check para regenerar.\n")
            sys.exit(1)
    _write(RTM_PATH, new_text)
    print(f"RTM regenerada: {RTM_PATH}")


if __name__ == "__main__":
    main()
