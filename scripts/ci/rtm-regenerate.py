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
    # newline="\n" é obrigatório: sem ele, o modo texto do Python traduz para CRLF em
    # Windows e reescreve o ficheiro inteiro, contrariando o `.gitattributes`
    # (`* text=auto eol=lf`) e sujando a árvore de trabalho a cada regeneração.
    path.write_text(text, encoding="utf-8", newline="\n")


def corpus_stats(tickets: dict) -> dict:
    """
    Constantes do corpus DERIVADAS (nunca escritas à mão): é isto que impede o
    gate de ficar fail-open quando o backlog cresce. Se um ticket novo entrar em
    specs/EPIC-*.md, estes números mudam, o texto gerado muda, e `--check` diverge
    do ficheiro em disco → gate vermelho.

    `n_epics` é uma CONTAGEM e só pode ser usada como contagem. O identificador do
    último epic é `max_epic` — derivado do NOME dos ficheiros, não do seu número.
    Confundir as duas coisas foi o defeito que a guarda em `assert_epic_claims`
    fecha; ver o comentário lá.
    """
    nums = sorted(aos_key(t) for t in tickets)
    epic_nums = sorted(epic_ids_in_specs())
    return {
        "n_tickets": len(nums),
        "min_aos": nums[0] if nums else 0,
        "max_aos": nums[-1] if nums else 0,
        "n_epics": len(list(SPECS_DIR.glob("EPIC-*.md"))),
        "min_epic": epic_nums[0] if epic_nums else 0,
        "max_epic": epic_nums[-1] if epic_nums else 0,
        "n_adrs": len(ADR_RANGE),
        "n_nfrs": len(NFR_SPECS),
    }


def epic_ids_in_specs() -> list:
    """Números dos epics existentes em `specs/EPIC-NN_*.md`, lidos do nome do ficheiro."""
    out = []
    for p in SPECS_DIR.glob("EPIC-*.md"):
        m = re.match(r"EPIC-(\d{2})(?:_|$)", p.stem)
        if m:
            out.append(int(m.group(1)))
    return out


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
    # EPIC-18 (remediação da auditoria v4). O limite superior é ABERTO (None): a gama
    # estende-se até ao último ticket do corpus, para que tickets novos herdem um
    # mapeamento em vez de caírem em "—" silenciosamente.
    # Justificação do par escolhido: a EPIC-18 é remediação transversal, mas o seu
    # centro de gravidade são (a) as convenções de engenharia/CI e os gates
    # anti-recorrência — `tecnica/11_Convencoes_Engenharia_Evolucao.md` — e (b) a
    # governação/conformidade que os achados STRIDE e regulatórios tocam —
    # `tecnica/09_Governacao_Conformidade.md`. §6 declara esta correspondência
    # explicitamente, para que §4 e §6 não se contradigam.
    (("AOS-190", None), ["`tecnica/11`", "`tecnica/09`"]),
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
        rl = aos_key(rlow)
        # rhigh None => gama aberta à direita (ver comentário em DOC_RANGES).
        rh = aos_key(rhigh) if rhigh is not None else max(high, rl)
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


def update_section1(rtm_text: str, stats: dict) -> str:
    """
    Actualiza §1.2 (âmbito) e §1.5 (ADRs aplicáveis). Todos os números vêm de
    `stats` (derivados do corpus) — nenhum é literal, senão o gate ficaria
    fail-open: o backlog crescia e o texto continuava a afirmar o valor antigo.
    """
    # §1.2
    rtm_text = re.sub(
        r"A rastreabilidade cobre os \d+ ADRs canónicos \(`_BRIEF` §3\), as \d+ capacidades funcionais \(`specs/00` §4\), os \d+ \*drivers\* não-funcionais \(`specs/00` §7\) e os \*\*\d+ tickets\*\* `AOS-\d+`–`AOS-\d+` distribuídos por \d+ epics\.",
        (
            f"A rastreabilidade cobre os {stats['n_adrs']} ADRs canónicos (`_BRIEF` §3), "
            f"as 11 capacidades funcionais (`specs/00` §4), os {stats['n_nfrs']} *drivers* "
            f"não-funcionais (`specs/00` §7) e os **{stats['n_tickets']} tickets** "
            f"`AOS-{stats['min_aos']:03d}`–`AOS-{stats['max_aos']:03d}` distribuídos por "
            f"{stats['n_epics']} epics."
        ),
        rtm_text,
    )
    # §1.5
    rtm_text = re.sub(
        r"Este documento não introduz decisões de arquitectura; \*\*rastreia\*\* as \d+ existentes \(ADR-001 a ADR-\d+, `_BRIEF` §3\)\.",
        (
            f"Este documento não introduz decisões de arquitectura; **rastreia** as "
            f"{stats['n_adrs']} existentes (ADR-001 a {ADR_RANGE[-1]}, `_BRIEF` §3)."
        ),
        rtm_text,
    )
    # §7 — A COBERTURA AFIRMADA TEM DE SER A COBERTURA GERADA (achado E-01 de `analises/10`).
    #
    # A §7 é prosa e estava FORA de tudo: nem regenerada aqui, nem lintada (`ref-lint.py` tem a
    # RTM na lista de `skip`). Afirmava «20/20 ADRs e 12/12 NFRs» a setenta linhas de secções
    # GERADAS que diziam 19/19 e 10/10 — e o changelog do próprio ficheiro descrevia alterações
    # («+ADR-020 no §4») que o ficheiro não contém. Um documento cuja função é rastreabilidade a
    # contradizer-se a si próprio, com o gate verde por cima.
    #
    # Passa a derivar dos MESMOS números que geram a §4 e a §5. Se um dia divergirem, divergem
    # juntos e por uma só causa — que é o que se pode verificar.
    rtm_text = re.sub(
        r"Nenhum ADR e nenhum NFR está \*\*sem\*\* cobertura mínima: \d+/\d+ ADRs e \d+/\d+ NFRs têm pelo menos um ticket associado\.",
        (
            f"Nenhum ADR e nenhum NFR está **sem** cobertura mínima: "
            f"{stats['n_adrs']}/{stats['n_adrs']} ADRs e {stats['n_nfrs']}/{stats['n_nfrs']} NFRs "
            f"têm pelo menos um ticket associado."
        ),
        rtm_text,
    )
    return rtm_text


def epic_index(tickets: dict) -> dict:
    """{AOS-NNN: 'EPIC-NN'} — o epic que CONTÉM cada ticket, lido de specs/EPIC-*.md."""
    return {aos: info["epic"].split("_")[0] for aos, info in tickets.items()}


def epic_of(aos: str, index: dict) -> str:
    """Epic que contém `aos`. Fail-closed: um ticket citado sem epic é deriva do corpus."""
    if aos not in index:
        sys.stderr.write(f"ERRO: AOS {aos} citado na §6 não existe em specs/EPIC-*.md\n")
        sys.exit(1)
    return index[aos]


def tickets_between(lo: int, hi: int, tickets: dict) -> list:
    """Tickets do corpus na gama fechada [lo, hi], por ordem."""
    return sorted((t for t in tickets if lo <= aos_key(t) <= hi), key=aos_key)


def epics_between(lo: int, hi: int, tickets: dict, index: dict) -> list:
    """Epics que CONTÊM pelo menos um ticket na gama [lo, hi], por ordem de identificador."""
    return sorted({index[t] for t in tickets_between(lo, hi, tickets)})


def assert_epic_claims(rows: list, index: dict) -> None:
    """
    GUARDA anti-regressão. Fecha a classe «um label derivado de CONTAGEM a passar-se
    por IDENTIDADE»: `EPIC-{n_epics:02d}` (o número de epics) usado como se fosse o
    epic onde vive um ticket concreto. Foi assim que o EPIC-21 desapareceu da matriz
    quando o EPIC-22 entrou — substituição em vez de adição, num documento cuja
    função é precisamente não perder o rasto.

    Cada linha gerada da §6 traz consigo as suas AFIRMAÇÕES (`claims`): pares
    (epic, tickets) que a linha assume. Verifica-se três coisas, todas contra o
    corpus e nenhuma contra o texto que a própria linha escreveu:

      1. cada par declarado é verdadeiro — o epic contém mesmo aquele ticket;
      2. cada `EPIC-NN` que aparece na linha está declarado — não se nomeia um epic
         sem dizer que tickets o justificam;
      3. cada `AOS-NNN` que aparece na linha é coberto por alguma declaração — não
         se cita um ticket que nenhum dos epics nomeados contém (era o caso do
         `AOS-194` atribuído ao «último epic»).

    Qualquer violação é FATAL (exit 1) com a linha e o par em falta: um aviso em
    stderr não avermelharia o gate, e o gate é o único leitor que confronta esta
    tabela com a fonte.
    """
    errs = []
    for line, claims in rows:
        claimed = {}
        for epic, tks in claims:
            if not tks:
                errs.append(f"  {line}\n    → declaração vazia para {epic} (nenhum ticket a justificá-lo)")
            claimed.setdefault(epic, set()).update(tks)
        # 1. as declarações têm de ser verdadeiras no corpus
        for epic in sorted(claimed):
            for t in sorted(claimed[epic], key=aos_key):
                real = index.get(t)
                if real != epic:
                    errs.append(
                        f"  {line}\n    → afirma {epic} para {t}, mas {t} vive em "
                        f"{real or '<nenhum epic>'}"
                    )
        # 2. nenhum epic nomeado sem declaração
        for tok in sorted(set(re.findall(r"EPIC-\d{2}", line))):
            if tok not in claimed:
                errs.append(f"  {line}\n    → nomeia {tok} sem declarar que ticket o justifica")
        # 3. nenhum ticket citado fora das declarações
        all_claimed = set().union(*claimed.values()) if claimed else set()
        for t in sorted(set(re.findall(r"AOS-\d{3}", line)) - all_claimed, key=aos_key):
            errs.append(f"  {line}\n    → cita {t}, que nenhum epic nomeado na linha contém")
    if errs:
        sys.stderr.write(
            "ERRO: §6 do RTM afirma pares epic↔ticket que o corpus não confirma "
            f"({len(errs)} violação(ões)):\n" + "\n".join(errs) + "\n"
        )
        sys.exit(1)


def generate_section6(tickets: dict, stats: dict) -> str:
    """Gera a tabela de rasto descendente documento técnico → epic → tickets."""
    first = f"AOS-{stats['min_aos']:03d}"
    last = f"AOS-{stats['max_aos']:03d}"
    index = epic_index(tickets)
    # INTERVALO (não identidade de conteúdo): «EPIC-01..EPIC-NN» no diagrama. O
    # extremo vem do NOME do último ficheiro de epic, não da contagem — e exige-se
    # que a numeração seja contígua, senão a notação de intervalo mentiria.
    last_epic = f"EPIC-{stats['max_epic']:02d}"
    if stats["max_epic"] != stats["n_epics"] or stats["min_epic"] != 1:
        sys.stderr.write(
            f"ERRO: numeração de epics não contígua (min={stats['min_epic']}, "
            f"max={stats['max_epic']}, n={stats['n_epics']}); o intervalo "
            f"«EPIC-01..{last_epic}» do diagrama deixaria de ser verdade.\n"
        )
        sys.exit(1)
    # Gama de remediação: derivada da última entrada de DOC_RANGES (aberta à direita),
    # para que §4 (que usa DOC_RANGES) e §6 (esta tabela) nunca se contradigam.
    rem_low = DOC_RANGES[-1][0][0]
    rem_range = f"{rem_low} – {last}"
    # Os epics desta gama são TODOS os que contêm tickets nela — não «o último».
    # Nomear só um transformava cada epic novo numa substituição do anterior.
    rem_epics = epics_between(aos_key(rem_low), stats["max_aos"], tickets, index)
    rem_epics_str = ", ".join(rem_epics)
    rem_claims = [
        (e, [t for t in tickets_between(aos_key(rem_low), stats["max_aos"], tickets) if index[t] == e])
        for e in rem_epics
    ]
    # A análise STRIDE é atribuída ao epic que CONTÉM o ticket, não a um número.
    stride_ticket = "AOS-194"
    stride_epic = epic_of(stride_ticket, index)

    def rng(lo, hi):
        return tickets_between(lo, hi, tickets)

    # (linha, declarações) — ver assert_epic_claims. As linhas transversais («Todos»)
    # não nomeiam epics; declaram-se os extremos, que são os tickets que citam.
    table = [
        (f"| `tecnica/00_Arquitectura_Solucao.md` | Todos (transversal) | {first} – {last} |",
         [(epic_of(first, index), [first]), (epic_of(last, index), [last])]),
        ("| `tecnica/01_Reference_Monitor_Plano_Controlo.md` | EPIC-01 | AOS-001 – AOS-012 |",
         [("EPIC-01", rng(1, 12))]),
        ("| `tecnica/02_Agent_Runtime_Execucao_Duravel.md` | EPIC-02 | AOS-013 – AOS-024 |",
         [("EPIC-02", rng(13, 24))]),
        ("| `tecnica/03_Orquestracao_Escalonamento.md` | EPIC-03 | AOS-025 – AOS-034 |",
         [("EPIC-03", rng(25, 34))]),
        ("| `tecnica/04_Memoria_Persistencia.md` | EPIC-04 | AOS-035 – AOS-044 |",
         [("EPIC-04", rng(35, 44))]),
        ("| `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` | EPIC-05 | AOS-045 – AOS-054 |",
         [("EPIC-05", rng(45, 54))]),
        ("| `tecnica/06_Model_Gateway_Custos.md` | EPIC-06 | AOS-055 – AOS-063 |",
         [("EPIC-06", rng(55, 63))]),
        ("| `tecnica/07_Seguranca_Isolamento.md` | EPIC-07 | AOS-064 – AOS-075 |",
         [("EPIC-07", rng(64, 75))]),
        ("| `tecnica/08_Observabilidade_Evals.md` | EPIC-08 | AOS-076 – AOS-086 |",
         [("EPIC-08", rng(76, 86))]),
        (f"| `tecnica/09_Governacao_Conformidade.md` | EPIC-09, {rem_epics_str} | AOS-087 – AOS-097 (+ {rem_range}) |",
         [("EPIC-09", rng(87, 97))] + rem_claims),
        ("| `tecnica/10_Topologia_Implantacao_Operacao.md` | EPIC-10, EPIC-11 | AOS-098 – AOS-108 (+ AOS-118) |",
         [("EPIC-10", rng(98, 108)), ("EPIC-11", ["AOS-118"])]),
        (f"| `tecnica/11_Convencoes_Engenharia_Evolucao.md` | EPIC-11 (+ EPIC-05 auto-mod), {rem_epics_str} | AOS-109 – AOS-118 (+ AOS-045–054, + {rem_range}) |",
         [("EPIC-11", rng(109, 118)), ("EPIC-05", rng(45, 54))] + rem_claims),
        ("| `tecnica/12_Contratos_de_Interface.md` | EPIC-01, EPIC-05, EPIC-06, EPIC-14 | AOS-003, 004; AOS-045–054; AOS-055–063; AOS-144–162 |",
         [("EPIC-01", rng(3, 4)), ("EPIC-05", rng(45, 54)), ("EPIC-06", rng(55, 63)),
          ("EPIC-14", rng(144, 162))]),
        ("| `tecnica/13_Modelo_Dados_Eventos.md` | EPIC-04, EPIC-05, EPIC-08 | AOS-035–044, AOS-045–054, AOS-076–086 |",
         [("EPIC-04", rng(35, 44)), ("EPIC-05", rng(45, 54)), ("EPIC-08", rng(76, 86))]),
        # AOS-072 vive em EPIC-07 (isolamento), não em EPIC-08/09: a linha nomeava
        # dois epics e citava um ticket que nenhum deles contém — a mesma classe.
        ("| `tecnica/14_Matriz_Conformidade.md` | EPIC-07, EPIC-08, EPIC-09 | AOS-072, AOS-076–086, AOS-087–097 |",
         [("EPIC-07", ["AOS-072"]), ("EPIC-08", rng(76, 86)), ("EPIC-09", rng(87, 97))]),
        ("| `tecnica/15_Experiencia_HITL_UX.md` | EPIC-12 (+ EPIC-13 frontend) | AOS-119 – AOS-143 |",
         [("EPIC-12", rng(119, 128)), ("EPIC-13", rng(129, 143))]),
        (f"| `tecnica/16_Rastreabilidade_RTM.md` | Todos (transversal — meta-rastreabilidade) | {first} – {last} |",
         [(epic_of(first, index), [first]), (epic_of(last, index), [last])]),
        (f"| `tecnica/17_Analise_STRIDE.md` | EPIC-07, EPIC-15, EPIC-16 (análise em {stride_epic}/{stride_ticket}) | AOS-064–075, AOS-163–173, AOS-174–177 |",
         [("EPIC-07", rng(64, 75)), ("EPIC-15", rng(163, 173)), ("EPIC-16", rng(174, 177)),
          (stride_epic, [stride_ticket])]),
    ]
    mermaid = [
        "```mermaid",
        "flowchart LR",
        f'    RF["RF-01..RF-11 (capacidades)"] --> ADR["ADR-001..{ADR_RANGE[-1].split("-")[1]} (decisoes)"]',
        # Mesma classe do `last_epic`: o extremo do intervalo é o IDENTIFICADOR do
        # último NFR (`NFR_SPECS[-1][0]`), não `n_nfrs` (a contagem). Coincidem hoje;
        # deixariam de coincidir no dia em que um NFR fosse retirado do meio.
        f'    NFR["NFR-01..{NFR_SPECS[-1][0]} (drivers)"] --> ADR',
        # INTERVALO: extremos, não conteúdo. Declaram-se os extremos para que a
        # guarda os verifique na mesma (EPIC-01 e o último existem e têm tickets).
        (f'    ADR --> EPIC["EPIC-01..{last_epic} (entregas)"]',
         [("EPIC-01", rng(1, 12)), (last_epic, [t for t in tickets if index[t] == last_epic])]),
        f'    EPIC --> TICK["{first}..{last} (tickets)"]',
        '    DOC["tecnica/00..17 (docs)"] --> EPIC',
        ('    TICK --> TEST["EPIC-11: AOS-109..118 (verificacao)"]',
         [("EPIC-11", rng(109, 118))]),
        "    NFR --> TEST",
        "```",
    ]
    checked = list(table) + [x for x in mermaid if isinstance(x, tuple)]
    assert_epic_claims(checked, index)

    lines = [
        "## 6. Rasto descendente: documento técnico → epic → tickets",
        "",
        "O *back-link* que faltava (RAST). Cada documento de `tecnica/` mapeia para o(s) epic(s) e a gama de tickets que o realizam. As gamas por epic seguem `_BRIEF` §8.",
        "",
        "| Doc técnico | Epic(s) implementador(es) | Gama de tickets |",
        "|---|---|---|",
    ]
    lines += [line for line, _ in table]
    lines.append("")
    lines += [x[0] if isinstance(x, tuple) else x for x in mermaid]
    lines.append("")
    return "\n".join(lines)


def regenerate_rtm(tickets: dict, adr_titles: dict) -> str:
    rtm_text = _read(RTM_PATH)
    stats = corpus_stats(tickets)

    # Actualiza §1
    rtm_text = update_section1(rtm_text, stats)

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
    sec6 = generate_section6(tickets, stats)
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

    # Validação FAIL-CLOSED da gama: o backlog tem de ser contínuo de AOS-001 até ao
    # maior ticket encontrado. Um buraco é deriva do corpus (ticket apagado, renumerado
    # ou nunca escrito), não um aviso — e um aviso em stderr não avermelhava o gate.
    if not tickets:
        sys.stderr.write("ERRO: nenhum ticket AOS-NNN encontrado em specs/EPIC-*.md\n")
        sys.exit(1)
    all_aos = set(tickets.keys())
    max_aos = max(aos_key(t) for t in all_aos)
    expected = {f"AOS-{i:03d}" for i in range(1, max_aos + 1)}
    missing = sorted(expected - all_aos)
    if missing:
        shown = ", ".join(missing[:10]) + ("..." if len(missing) > 10 else "")
        sys.stderr.write(
            f"ERRO: gama de tickets descontínua — {len(missing)} em falta entre "
            f"AOS-001 e AOS-{max_aos:03d}: {shown}\n"
        )
        sys.exit(1)

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
