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

# `scripts/ci` no path ANTES de importar o modulo irmao: correr este ficheiro
# como script ja la punha a sua directoria, mas o §P1 do selftest carrega-o por
# caminho (`importlib.spec_from_file_location`) a partir de um processo cujo
# `sys.path[0]` e outro, e ai o import falhava com `ModuleNotFoundError`.
sys.path.insert(0, str(Path(__file__).resolve().parent))

import adr_register  # noqa: E402  (depende do sys.path acima)

# Raiz do corpus. Sobreponível por ambiente APENAS para o self-test (§R/§S)
# poder injectar falhas numa CÓPIA em vez de mutar a árvore real — o job de CI
# não define a variável. Mesmo molde de `AOS_REFLINT_ROOT` em `ref-lint.py:90`.
# Antes de AOS-316 não existia, e era por isso que §R e §S tinham de mutar este
# próprio ficheiro no sítio: dois runs sobrepostos corrompiam-se um ao outro.
REPO_ROOT = Path(os.environ.get("AOS_RTM_ROOT") or Path(__file__).resolve().parents[2])
RTM_PATH = REPO_ROOT / "tecnica" / "16_Rastreabilidade_RTM.md"
SPECS_DIR = REPO_ROOT / "specs"
DOCS_ADR_DIR = REPO_ROOT / "docs" / "adr"

# --- ADRs canónicos, DERIVADOS do registo (AOS-319) --------------------------
# O canon GATED é este, e é o mesmo em `ref-lint.py`: os dois leitores do corpus
# não podem discordar sobre o que exigem. Alargá-lo obriga cada ADR novo a ter
# ticket implementador — consequência aceite ao decidir GAP-07.
#
# AOS-314 alargou-o de `range(1, 20)` para `range(1, 24)` e fechou o sintoma; o
# AOS-319 fecha a causa. Um literal novo é um literal: no dia do ADR-024 o canon
# volta a ficar curto, nos MESMOS dois ficheiros, e nada o diz — foi assim que o
# 019 sobreviveu quatro ADRs. A gama passa a DERIVAR da tabela de
# `docs/adr/README.md`, o registo que se declara canónico e o único que regista
# o estado de cada decisão. Ver `adr_register.py` para a fonte e o fail-closed.
try:
    ADR_REGISTER = adr_register.read_register(REPO_ROOT)
except adr_register.RegisterError as _exc:
    sys.stderr.write("ERRO: %s\n" % _exc)
    sys.exit(1)
ADR_RANGE = [a.code for a in ADR_REGISTER]

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
    # NFR-11 e NFR-12 entraram no catálogo §3 pela EPIC-19 e nunca chegaram a esta
    # lista, pelo que a §5 gerava 10 linhas e afirmava «10/10» ao lado de um
    # catálogo de 12. Faltava-lhes a linha, não a prova: AOS-242 fixa o SLI de
    # fracção de planeamento ≤ 5% e AOS-232 deriva o risco das tools pinadas.
    ("NFR-11", "Custo de planeamento", "≤ 5% do orçamento da árvore", {"ADR-008"}),
    ("NFR-12", "Integridade do risco do plano", "0 nós irreversíveis auto-aprovados por rótulo *self-declared*", {"ADR-013", "ADR-005"}),
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
    "NFR-11": {"AOS-242"},
    "NFR-12": {"AOS-232"},
}


# Marcador opcional, escrito no bloco de um ticket: declara que os códigos
# ADR-NNN que ele cita são MENÇÃO — o ticket FALA sobre eles — e não
# implementação. Sem isto, um ticket sobre a própria rastreabilidade, que tem
# de nomear os ADRs de que fala, entra na matriz §4 como implementador deles: a
# matriz passaria a afirmar precisamente o que este epic existe para impedir.
# Primeiro utilizador: AOS-313 (que discute ADR-003, ADR-014 e ADR-020…023 sem
# realizar nenhum). `ref-lint.py` honra o mesmo marcador, para que os dois
# leitores do corpus nunca discordem sobre o que um ticket implementa.
RE_ADRS_MENCIONADOS = re.compile(r"<!--\s*rtm:\s*adrs-mencionados\s*-->")


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def _write(path: Path, text: str) -> None:
    # newline="\n" é obrigatório: sem ele, o modo texto do Python traduz para CRLF em
    # Windows e reescreve o ficheiro inteiro, contrariando o `.gitattributes`
    # (`* text=auto eol=lf`) e sujando a árvore de trabalho a cada regeneração.
    path.write_text(text, encoding="utf-8", newline="\n")


# Cabeçalhos dos catálogos de requisitos DENTRO do RTM. São a fonte dos
# identificadores `RF-NN`/`NFR-NN` — ver `requirement_catalogue`.
RF_HEADING = "## 2. Catálogo de Requisitos Funcionais (RF)"
NFR_HEADING = "## 3. Catálogo de Requisitos Não-Funcionais (NFR)"


def _section_body(text: str, heading: str, where: str) -> str:
    """Corpo de uma secção de topo, do cabeçalho até ao próximo `## ` ou ao fim."""
    m = re.search(
        rf"^{re.escape(heading)}$(.*?)(?=^## |\Z)", text, re.MULTILINE | re.DOTALL
    )
    if not m:
        sys.stderr.write(f"ERRO: não encontrou «{heading}» em {where}\n")
        sys.exit(1)
    return m.group(1)


def requirement_catalogue(rtm_text: str, heading: str, prefix: str) -> list:
    """
    Identificadores de uma família de requisitos, lidos das linhas da tabela do
    catálogo respectivo.

    QUAL É A FONTE AUTORITATIVA. Para os `RF-NN` e `NFR-NN` é o próprio RTM
    (§2 e §3), não `specs/00_System_Spec.md`. É o que a §1.1 declara — a RTM é
    o artefacto que estabelece o catálogo com identificadores estáveis — e é o
    que os factos impõem: RF-12/RF-13 e NFR-11/NFR-12 entraram pela EPIC-19 e
    não têm contrapartida em `specs/00` §4 nem §7. A System Spec continua
    autoritativa para *quantas capacidades top-level ela lista*, e para nada
    mais: confundir esse número com o extremo do intervalo de RF era o defeito
    que esta derivação fecha.

    Falha fechado se o catálogo não for contíguo `PREFIX-01`..`PREFIX-NN`: sem
    contiguidade, a contagem e o extremo do intervalo deixam de coincidir, e
    tudo o que se segue assume que coincidem.
    """
    body = _section_body(rtm_text, heading, "o RTM")
    ids = re.findall(rf"^\|\s*\*\*({prefix}-\d{{2}})\*\*\s*\|", body, re.MULTILINE)
    nums = [int(i.split("-")[1]) for i in ids]
    if not nums:
        sys.stderr.write(
            f"ERRO: o catálogo «{heading}» não tem nenhuma linha {prefix}-NN\n"
        )
        sys.exit(1)
    if nums != list(range(1, len(nums) + 1)):
        sys.stderr.write(
            f"ERRO: catálogo {prefix} descontínuo ou desordenado em «{heading}»: "
            f"{', '.join(ids)}\n"
        )
        sys.exit(1)
    return ids


def system_spec_capabilities() -> int:
    """Itens numerados de `specs/00_System_Spec.md` §4 — a ORIGEM de RF-01..RF-11."""
    body = _section_body(
        _read(SPECS_DIR / "00_System_Spec.md"),
        "## 4. Capacidades funcionais top-level",
        "specs/00_System_Spec.md",
    )
    return len(re.findall(r"^\d+\. \*\*", body, re.MULTILINE))


def system_spec_drivers() -> int:
    """Linhas da tabela de `specs/00_System_Spec.md` §7 — a ORIGEM de NFR-01..NFR-10."""
    body = _section_body(
        _read(SPECS_DIR / "00_System_Spec.md"),
        "## 7. Drivers não-funcionais",
        "specs/00_System_Spec.md",
    )
    rows = [
        ln
        for ln in body.splitlines()
        if ln.startswith("| ")
        and not ln.startswith("|---")
        and not ln.startswith("| Driver ")
    ]
    return len(rows)


# Números de ticket ATRIBUÍDOS, mas cujo bloco vive noutro ramo ainda não fundido.
#
# A guarda de contiguidade abaixo existe para apanhar um ticket apagado ou
# renumerado, e vale. Mas pressupõe que todo o backlog vive NESTE ramo — e com
# várias sessões a trabalhar em worktrees paralelos isso deixou de ser verdade:
# `AOS-317` foi aberto em `claude/exciting-maxwell-aec36d` no mesmo dia em que
# esta sessão abriu o seu, e a colisão foi resolvida renumerando o desta para
# `AOS-319`. O 317 existe e está tomado; o que não existe é aqui.
#
# Molde das baselines deste arnês (`scripts/ci/baseline/*.txt`): entrada
# explícita, com dono e com data de saída. Cada linha SAI quando o ramo
# respectivo for fundido — se ficar depois disso, a guarda deixa de proteger o
# número que ela nomeia, e é por isso que a lista tem de ser curta e revista.
# Ver a convenção de sessões concorrentes no `AGENTS.md`.
#
# A decisão de o que fazer se o ramo for ABANDONADO em vez de fundido — o número
# fica queimado, ou reatribui-se — está deferida com critério escrito em
# **DEF-908** (`docs/governance/REGISTO-Deferimentos.md`), para ser tomada no
# merge por quem o fizer, e não adivinhada agora por quem abriu a entrada.
ATRIBUIDOS_NOUTRO_RAMO = {
    # AOS-317 — `claude/exciting-maxwell-aec36d` (b26966c, 2026-09-04).
    # Sai quando esse ramo for fundido.
    "AOS-317",
}


def corpus_stats(tickets: dict) -> dict:
    """
    Constantes do corpus DERIVADAS (nunca escritas à mão): é isto que impede o
    gate de ficar fail-open quando o backlog cresce. Se um ticket novo entrar em
    specs/EPIC-*.md, estes números mudam, o texto gerado muda, e `--check` diverge
    do ficheiro em disco → gate vermelho.
    """
    nums = sorted(aos_key(t) for t in tickets)
    return {
        "n_tickets": len(nums),
        "min_aos": nums[0] if nums else 0,
        "max_aos": nums[-1] if nums else 0,
        "n_epics": len(list(SPECS_DIR.glob("EPIC-*.md"))),
        "n_adrs": len(ADR_RANGE),
        "n_nfrs": len(NFR_SPECS),
    }


def contar_adrs_por_catalogo() -> dict:
    """
    Quantos ADRs cada catálogo enuncia. São três, e divergem.

    `docs/adr/README.md` é o catálogo de DOCUMENTOS e a fonte do canon gated
    (AOS-314). `_BRIEF` §3 e `specs/00` §11 são os catálogos de ENUNCIADO — o
    próprio README declara-os «a referência de enunciado para todos os ADRs» — e
    estão atrás. GAP-08 regista a divergência com estes números, em vez de a
    afirmar à mão: contá-los aqui é a diferença entre uma lacuna que se mede e uma
    que envelhece.
    """
    def _entre(texto: str, inicio: str, fim: str) -> str:
        i = texto.find(inicio)
        if i < 0:
            return ""
        j = texto.find(fim, i + len(inicio))
        return texto[i:] if j < 0 else texto[i:j]

    def _adrs(bloco: str) -> set:
        return set(re.findall(r"^\| (ADR-\d{3}) \|", bloco, re.MULTILINE))

    brief = _adrs(_entre(_read(REPO_ROOT / "_BRIEF.md"), "## 3. Decis", "## 4."))
    sysspec = _adrs(_entre(_read(SPECS_DIR / "00_System_Spec.md"), "## 11. ADRs em vigor", "## 12."))
    readme = _adrs(_read(DOCS_ADR_DIR / "README.md"))
    return {"_BRIEF §3": brief, "`specs/00` §11": sysspec, "`docs/adr/README.md`": readme}


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
            adrs = (
                set()
                if RE_ADRS_MENCIONADOS.search(block)
                else set(re.findall(r"ADR-\d{3}", block))
            )
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
    for entry in ADR_REGISTER:
        tickets_for = sorted(set(adr_to_tickets.get(entry.code, [])))
        # Doc técnico: inferido a partir do range de tickets
        docs = infer_docs_for_tickets(tickets_for, tickets)
        rows.append({
            "adr": entry.code,
            "title": adr_titles.get(entry.code, "*título não encontrado*"),
            # O ESTADO vem do registo (AOS-319). Sem ele a matriz punha um
            # *Proposto* e um *Ratificado* na mesma coluna, com a mesma
            # autoridade aparente — e dois dos vinte e três estão Propostos.
            "state": entry.state,
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
    # EPIC-19 (planeador). Sem esta entrada, `tecnica/18_Planner_Meta_Orquestracao.md`
    # não existia em DOC_RANGES e era invisível à §4: os tickets do planeador caíam na
    # gama aberta abaixo, cuja justificação escrita é a remediação da EPIC-18, e a
    # matriz atribuía as decisões do planeador aos documentos de governação e de
    # convenções de engenharia (AOS-315).
    (("AOS-230", "AOS-244"), ["`tecnica/18`"]),
    # EPIC-18 (remediação da auditoria v4). O limite superior é ABERTO (None) e esta
    # entrada é o RECURSO: aplica-se apenas aos tickets que nenhuma gama explícita
    # cobre, para que tickets novos herdem um mapeamento em vez de caírem em "—"
    # silenciosamente — sem o alastrar a tickets que já têm documento próprio
    # (AOS-315). Tem de ser a ÚLTIMA entrada da lista.
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


def epic_label(epic_stem: str) -> str:
    """`EPIC-18_Remediacao_Auditoria_Multiagente_v4` -> `EPIC-18`."""
    return epic_stem.split("_")[0]


def last_epic_label() -> str:
    """
    Maior número de epic REALMENTE presente em `specs/`. Não é a contagem de
    ficheiros: se um dia faltar um número no meio, a contagem mente e este não.
    Serve apenas para a gama `EPIC-01..N` do diagrama — nunca para atribuir um
    ticket a um epic (ver `epic_of`).
    """
    nums = [int(epic_label(p.stem).split("-")[1]) for p in SPECS_DIR.glob("EPIC-*.md")]
    return f"EPIC-{max(nums):02d}"


def epic_of(tickets: dict, aos: str) -> str:
    """
    Epic que CONTÉM o ticket, lido do corpus.

    É esta derivação que substitui o antigo `last_epic` na §6: o último epic do
    backlog não é o epic de um ticket concreto, e usá-lo como tal escrevia uma
    afirmação falsa NOVA a cada epic acrescentado — a linha do STRIDE dizia
    «EPIC-21», passou a dizer «EPIC-22», e AOS-194 sempre viveu na EPIC-18.
    """
    if aos not in tickets:
        sys.stderr.write(
            f"ERRO: a §6 cita {aos}, que não existe em specs/EPIC-*.md\n"
        )
        sys.exit(1)
    return epic_label(tickets[aos]["epic"])


def epics_covering(tickets: dict, low: int, high: int, exclude=frozenset()) -> list:
    """
    Rótulos dos epics que contêm pelo menos um ticket na gama [low, high].

    Uma gama aberta como AOS-190–AOS-311 atravessa vários epics de remediação e
    ainda tickets acrescentados a epics antigos; nomear só um deles — fosse o
    último ou o primeiro — é o mesmo defeito com outra roupagem. `exclude`
    remove os rótulos que a linha já nomeia literalmente, para não os repetir.
    """
    labels = {
        epic_label(info["epic"])
        for aos, info in tickets.items()
        if low <= aos_key(aos) <= high
    }
    return sorted(labels - set(exclude))


# Gama de tickets numa célula da §6: "AOS-064–075", "AOS-001 – AOS-012".
_RANGE_RE = re.compile(r"AOS-(\d{3})\s*[–—-]\s*(?:AOS-)?(\d{3})")


def _cited_tickets(cell: str) -> list:
    """
    Tickets citados numa célula da §6, com as gamas expandidas. As continuações
    abreviadas (`AOS-003, 004` e `AOS-072, 076–097`) herdam o prefixo: sem isso
    metade das citações escapava à validação.
    """
    cell = re.sub(r"(?<![\w-])(\d{3})(?![\d\w])", lambda m: f"AOS-{m.group(1)}", cell)
    nums = []
    for m in _RANGE_RE.finditer(cell):
        nums.extend(range(int(m.group(1)), int(m.group(2)) + 1))
    for m in re.finditer(r"AOS-(\d{3})", _RANGE_RE.sub(" ", cell)):
        nums.append(int(m.group(1)))
    return [f"AOS-{n:03d}" for n in sorted(set(nums))]


def validate_section6(section: str, tickets: dict) -> None:
    """
    Asserção anti-recorrência: nenhuma linha gerada pode nomear um epic que não
    contenha os tickets que a própria linha cita.

    É a peça que faltava. Quem escreve a §6 é uma máquina, e nada comparava o
    que ela escreve com a fonte — exactamente o meta-achado de `analises/10` §5
    («números escritos à mão derivam onde nenhum gate os lê»), agravado por o
    autor ser automático. Falha fechado: sai != 0 e o gate `scripts/ci/rtm.sh`
    fica vermelho antes de a afirmação falsa chegar ao ficheiro.
    """
    errors = []
    for line in section.splitlines():
        if not line.startswith("| `tecnica/"):
            continue
        cols = [c.strip() for c in line.strip("|").split("|")]
        if len(cols) < 3:
            continue
        doc, epics_cell, range_cell = cols[0], cols[1], cols[2]

        # Pares explícitos `EPIC-NN/AOS-NNN` (ex.: «análise em EPIC-18/AOS-194»).
        for m in re.finditer(r"(EPIC-\d{2})/(AOS-\d{3})", epics_cell):
            claimed, aos = m.group(1), m.group(2)
            real = epic_label(tickets[aos]["epic"]) if aos in tickets else None
            if real != claimed:
                errors.append(
                    f"{doc}: a linha diz {claimed}/{aos}, mas {aos} vive em "
                    f"{real or '(inexistente)'}."
                )

        declared = set(re.findall(r"EPIC-\d{2}", epics_cell))
        missing = []
        misplaced = defaultdict(list)
        for aos in _cited_tickets(range_cell):
            if aos not in tickets:
                # Um número atribuído noutro ramo cai DENTRO das gamas que a §6
                # escreve (`AOS-190→AOS-NNN`), porque as gamas assumem um backlog
                # contíguo. Não é citação partida: é um ticket que existe e que
                # este ramo ainda não viu. Ver `ATRIBUIDOS_NOUTRO_RAMO`.
                if aos not in ATRIBUIDOS_NOUTRO_RAMO:
                    missing.append(aos)
            elif declared and epic_label(tickets[aos]["epic"]) not in declared:
                misplaced[epic_label(tickets[aos]["epic"])].append(aos)
        if missing:
            errors.append(f"{doc}: cita {', '.join(missing)}, que não existe(m) no corpus.")
        for real, aos_list in sorted(misplaced.items()):
            sample = ", ".join(aos_list[:4]) + ("…" if len(aos_list) > 4 else "")
            errors.append(
                f"{doc}: cita {len(aos_list)} ticket(s) de {real} ({sample}), "
                f"mas a linha só nomeia {', '.join(sorted(declared))}."
            )

    if errors:
        sys.stderr.write("ERRO: a §6 atribui tickets a epics que não os contêm:\n")
        for e in errors:
            sys.stderr.write(f"  - {e}\n")
        sys.exit(1)


# Afirmações numéricas escritas no RTM, nas três notações que o documento usa:
#   `RF-01`–`RF-13`  (§1.2)   RF-01 … RF-13  (§2)   RF-01..RF-13  (mermaid §6)
# Os acentos graves são removidos antes de correr o padrão.
# A QUARTA notacao — o separador « a » por extenso, que a §1.5 usa — entrou com
# AOS-319. A ausencia era buraco com consequencia medida: «ADR-001..014» era
# recusado e «ADR-001 a ADR-014» passava incolume a afirmar o mesmo. Um padrao
# que so ve tres das quatro notacoes do proprio documento ENSINA a usar a quarta.
_RANGE_CLAIM_RE = re.compile(
    r"\b(RF|NFR|ADR)-0*1"
    r"(?:\s*(?:\.\.|…|–|—|-)\s*|\s+a\s+)"
    r"(?:(?:RF|NFR|ADR)-)?(\d{2,3})\b"
)
# Contagens: «**Total: 13 requisitos funcionais**» (§2/§3) e «os 13 requisitos
# funcionais» (§1.2). A segunda forma foi acrescentada depois de a prova da
# guarda mostrar que um 11 literal na §1.2 passava incólume — o extremo do
# intervalo estava certo e mais nada era lido.
_TOTAL_CLAIM_RE = re.compile(
    r"(?:\*\*Total:|\bos)\s+(\d+)\s+requisitos\s+(não-)?funcionais"
)
# «as 11 capacidades funcionais (`specs/00` §4)» é uma afirmação sobre a System
# Spec, não sobre o catálogo §2 — foi confundi-las que produziu o defeito. Fica
# guardada contra a SUA fonte, para que a confusão não regresse por reescrita.
# Sem exigir artigo antes do número: a §1.2 diz «das 11 capacidades», e um `\bas`
# não casa dentro de «das». Foi §U4 a apanhá-lo — a guarda tinha um ponto cego
# exactamente na frase que a motivou.
_CAP_CLAIM_RE = re.compile(r"(\d+)\s+capacidades\s+funcionais")
_COVERAGE_CLAIM_RE = re.compile(r"(\d+)/(\d+)\s*(ADRs|NFRs)\b")


def assert_numeric_claims(rtm_text: str, rf_ids: list, nfr_ids: list) -> None:
    """
    Guarda fail-closed para as CONTAGENS e os EXTREMOS DE INTERVALO do RTM.

    Irmã de `validate_section6` e `validate_section7`, para a metade do mesmo
    meta-achado (`analises/10` §5) que nenhuma das duas cobre: ali validam-se
    pares epic↔ticket e citações inventadas, aqui validam-se NÚMEROS. Um número
    escrito numa linha gerada — «11 capacidades», «RF-01..RF-11», «10/10 NFRs»,
    «Para cada ADR-001…019» com vinte e três linhas na tabela — não tinha quem o
    comparasse com a fonte, e apodrecia em silêncio enquanto os catálogos
    cresciam. AOS-314 alargou `ADR_RANGE` a 023 e o cabeçalho da §4 ficou nos
    019: é o mesmo defeito a nascer da própria correcção que o combatia.

    Compara com a fonte derivada:
      - `RF-01..NN`  → último RF do catálogo §2;
      - `NFR-01..NN` → último NFR do catálogo §3;
      - `ADR-001..N` → último ADR de `ADR_RANGE`;
      - `N requisitos (não-)funcionais` → tamanho do catálogo §2/§3;
      - `N capacidades funcionais` → itens de `specs/00` §4, que é outra coisa;
      - `A/B ADRs|NFRs` → B é o tamanho da família, e A ≤ B.

    Corre sobre o corpo TODO do documento — desde AOS-313 até a §7 é gerada, e o
    glossário é lido por quem audita tanto como as matrizes. Pára no **controlo
    de versões**, e só aí: aquela tabela regista o que cada revisão AFIRMOU na
    data, não o que o documento afirma hoje. A entrada 1.2 diz «cobertura 20/20
    ADRs, 12/12 NFRs» e está correcta enquanto história — vem anotada com a
    regeneração que a desfez (AOS-313). Alinhá-la com os números de hoje seria
    falsificar o registo, que é o contrário do que uma RTM existe para fazer.
    """
    probe = rtm_text.partition("\n### Controlo de versões")[0].replace("`", "")
    expected = {"RF": len(rf_ids), "NFR": len(nfr_ids), "ADR": len(ADR_RANGE)}
    errors = []

    for m in _RANGE_CLAIM_RE.finditer(probe):
        family, claimed = m.group(1), int(m.group(2))
        if claimed != expected[family]:
            errors.append(
                f"«{m.group(0)}» termina em {claimed}, mas o catálogo de "
                f"{family} vai até {expected[family]}."
            )

    for m in _TOTAL_CLAIM_RE.finditer(probe):
        claimed = int(m.group(1))
        family = "NFR" if m.group(2) else "RF"
        if claimed != expected[family]:
            errors.append(
                f"«{m.group(0).strip()}…» conta {claimed}, mas o catálogo de {family} "
                f"tem {expected[family]} entradas."
            )

    for m in _CAP_CLAIM_RE.finditer(probe):
        claimed, real = int(m.group(1)), system_spec_capabilities()
        if claimed != real:
            errors.append(
                f"«{m.group(0)}» conta {claimed}, mas `specs/00` §4 enumera {real}."
            )

    for m in _COVERAGE_CLAIM_RE.finditer(probe):
        num, den, family = int(m.group(1)), int(m.group(2)), m.group(3)[:-1]
        if den != expected[family]:
            errors.append(
                f"«{m.group(0)}» tem denominador {den}, mas há "
                f"{expected[family]} {family}s."
            )
        if num > den:
            errors.append(f"«{m.group(0)}» afirma cobrir mais do que existe.")

    if errors:
        sys.stderr.write(
            "ERRO: o RTM afirma números que não batem certo com a sua fonte:\n"
        )
        for e in errors:
            sys.stderr.write(f"  - {e}\n")
        sys.exit(1)


def infer_docs_for_tickets(tickets_for: list, tickets: dict) -> str:
    """
    Documentos técnicos que desenvolvem uma decisão, resolvidos TICKET A TICKET.

    Resolvia-se antes pela AMPLITUDE do conjunto: `min(nums)`–`max(nums)` reduzido a
    um intervalo, e depois a união de todas as gamas que o intervalo intersectasse.
    Um ADR com dois tickets afastados herdava tudo o que estivesse entre eles —
    ADR-014 (autonomia L0–L5), com AOS-022 e AOS-125 nos extremos, era declarado
    desenvolvido em onze documentos, entre eles orquestração e model gateway. Por
    ticket são três. Dezassete das dezanove linhas da §4 estavam assim (AOS-315).
    """
    if not tickets_for:
        return "—"
    # A última entrada de DOC_RANGES é o recurso (ver comentário lá): só se aplica a
    # tickets que nenhuma gama explícita cobre.
    explicitas, (_, recurso) = DOC_RANGES[:-1], DOC_RANGES[-1]
    docs = set()
    for aos in tickets_for:
        n = aos_key(aos)
        do_ticket = set()
        for (rlow, rhigh), doc_list in explicitas:
            if aos_key(rlow) <= n <= aos_key(rhigh):
                do_ticket.update(doc_list)
        docs |= do_ticket or set(recurso)
    if not docs:
        return "—"
    return ", ".join(sorted(docs, key=lambda x: x.lower()))


def validate_section4_docs(rows: list) -> None:
    """
    Asserção anti-recorrência da §4: um documento nomeado na coluna tem de EXISTIR.

    Não prova que o documento desenvolve a decisão — isso continua a vir de
    `DOC_RANGES`, escrito à mão (ressalva registada em AOS-315). Prova que a coluna
    é confrontável com o disco: um `tecnica/NN` renomeado ou apagado deixa de poder
    sobreviver em silêncio numa tabela gerada.
    """
    existentes = {p.stem.split("_")[0] for p in (REPO_ROOT / "tecnica").glob("*.md")}
    errors = []
    for r in rows:
        for doc in re.findall(r"`tecnica/(\d{2})`", r["docs"]):
            if doc not in existentes:
                errors.append(f"{r['adr']}: nomeia `tecnica/{doc}`, que não existe em tecnica/.")
    if errors:
        sys.stderr.write("ERRO: a §4 nomeia documentos técnicos inexistentes:\n")
        for e in sorted(set(errors)):
            sys.stderr.write(f"  - {e}\n")
        sys.exit(1)


def generate_section4(rows: list) -> str:
    validate_section4_docs(rows)
    lines = [
        "## 4. Matriz ADR × ticket",
        "",
        f"Para cada ADR-001…{ADR_RANGE[-1].split('-')[1]}, os tickets `AOS-NNN` cujo bloco de especificação o cita explicitamente (extracção por correspondência textual sobre `specs/EPIC-*.md`) e o(s) documento(s) técnico(s) que o desenvolvem. A coluna **Nº** é a contagem de tickets implementadores distintos.",
        "",
        "A coluna **Estado** vem do registo. Rastrear um ADR *Proposto* não o promove: a matriz mostra que tickets já o citam, e o estado diz com que autoridade (AOS-319).",
        "",
        "| ADR | Decisão | Estado | Nº | Tickets `AOS-NNN` que o implementam | Doc(s) técnico(s) |",
        "|---|---|---|---|---|---|",
    ]
    for r in rows:
        tickets_str = ", ".join(r["tickets"]) if r["tickets"] else "—"
        lines.append(f"| **{r['adr']}** | {r['title']} | {r['state']} | {r['count']} | {tickets_str} | {r['docs']} |")

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


def generate_section5(tickets: dict, nfr_ids: list) -> str:
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
        "NFR-11": "*Burn-down* da reserva de planeamento; exceder a fracção demove a autonomia",
        "NFR-12": "Risco derivado das tools pinadas; o rótulo do LLM só eleva, nunca reduz",
    }
    for nfr, name, target, adrs in NFR_SPECS:
        verif = sorted(NFR_MANUAL_TICKETS.get(nfr, set()))
        # Validação: todos os tickets manuais existem no corpus
        missing = [t for t in verif if t not in tickets]
        if missing:
            sys.stderr.write(f"AVISO: {nfr} referencia tickets inexistentes: {missing}\n")
        verif_str = ", ".join(verif) if verif else "—"
        lines.append(f"| **{nfr}** | {target} | {verif_str} | {proofs[nfr]} |")
    # A linha de cobertura dizia «10/10» à mão. O denominador é o catálogo §3,
    # que tem 12 NFRs desde a EPIC-19; o numerador são os NFRs com pelo menos um
    # ticket de verificação. Escrever 10/10 apagava a lacuna em vez de a mostrar,
    # que é precisamente o oposto do que uma RTM serve para fazer.
    verified = [nfr for nfr, _, _, _ in NFR_SPECS if NFR_MANUAL_TICKETS.get(nfr)]
    unverified = [nfr for nfr in nfr_ids if nfr not in verified]
    coverage = f"**Cobertura: {len(verified)}/{len(nfr_ids)} NFRs têm ≥ 1 ticket de verificação.**"
    if unverified:
        coverage += (
            f" Sem ticket de verificação nesta matriz: {', '.join(unverified)}"
            " — lacuna real, não arredondamento."
        )
    lines.append("")
    lines.append(coverage)
    lines.append("")
    return "\n".join(lines)


def update_section1(rtm_text: str, stats: dict, rf_ids: list, nfr_ids: list) -> str:
    """
    Actualiza §1.2 (âmbito) e §1.5 (ADRs aplicáveis). Todos os números vêm de
    `stats` (derivados do corpus) ou dos catálogos §2/§3 — nenhum é literal,
    senão o gate ficaria fail-open: o backlog crescia e o texto continuava a
    afirmar o valor antigo.
    """
    # §1.2. A frase dizia «as 11 capacidades funcionais (`specs/00` §4)» com o 11
    # escrito à mão, e contradizia a §2 do mesmo ficheiro, que cataloga RF-01..RF-13.
    # Passa a afirmar o catálogo (a fonte dos identificadores) e a nomear à parte a
    # origem em `specs/00`, que é outro número e outra coisa. O padrão é frouxo de
    # propósito — apanha a frase antiga e a nova, para a regeneração continuar
    # idempotente depois desta migração.
    rtm_text = re.sub(
        r"A rastreabilidade cobre .*?(?= Os dados das matrizes)",
        lambda _: (
            # A fonte citada para os ADRs é `docs/adr/README.md`: é o único
            # catálogo que tem a gama toda (AOS-314; `_BRIEF` §3 lista catorze).
            f"A rastreabilidade cobre os {stats['n_adrs']} ADRs canónicos (`docs/adr/README.md`), "
            f"os {len(rf_ids)} requisitos funcionais `RF-01`–`{rf_ids[-1]}` (§2), "
            f"os {len(nfr_ids)} requisitos não-funcionais `NFR-01`–`{nfr_ids[-1]}` (§3) "
            f"e os **{stats['n_tickets']} tickets** "
            f"`AOS-{stats['min_aos']:03d}`–`AOS-{stats['max_aos']:03d}` distribuídos por "
            f"{stats['n_epics']} epics. Os catálogos §2 e §3 partem das "
            f"{system_spec_capabilities()} capacidades funcionais de `specs/00` §4 e dos "
            f"{system_spec_drivers()} *drivers* de `specs/00` §7 e estendem-nos com os "
            f"requisitos entrados depois; os identificadores `RF-NN`/`NFR-NN` são "
            f"estáveis e vivem aqui, não na System Spec."
        ),
        rtm_text,
    )
    # §1.5
    rtm_text = re.sub(
        r"Este documento não introduz decisões de arquitectura; \*\*rastreia\*\* as \d+ existentes \(ADR-001 a ADR-\d+, [^)]*\)\.",
        (
            f"Este documento não introduz decisões de arquitectura; **rastreia** as "
            f"{stats['n_adrs']} existentes (ADR-001 a {ADR_RANGE[-1]}, `docs/adr/README.md`)."
        ),
        rtm_text,
    )
    return rtm_text


def generate_section6(tickets: dict, stats: dict, rf_ids: list, nfr_ids: list) -> str:
    """Gera a tabela de rasto descendente documento técnico → epic → tickets."""
    first = f"AOS-{stats['min_aos']:03d}"
    last = f"AOS-{stats['max_aos']:03d}"
    last_epic = last_epic_label()
    # Gama da EPIC-18 (remediação v4): derivada da última entrada de DOC_RANGES,
    # para que §4 (que usa DOC_RANGES) e §6 (esta tabela) nunca se contradigam.
    epic18_low = DOC_RANGES[-1][0][0]
    epic18_range = f"{epic18_low} – {last}"
    # Os epics que REALMENTE contêm os tickets dessa gama, varridos do corpus. A
    # gama é aberta à direita e atravessa vários epics — os de remediação e ainda
    # tickets acrescentados a epics antigos (ex.: AOS-287 na EPIC-01). A versão
    # anterior escrevia aqui `last_epic`, que era falso e ficava falso de forma
    # NOVA a cada epic acrescentado.
    rem_low = aos_key(epic18_low)
    rem_epics = epics_covering(tickets, rem_low, stats["max_aos"])
    # A união é ordenada por número de epic: a célula é lida por humanos e uma
    # lista fora de ordem esconde omissões.
    gov_epics = ", ".join(sorted({"EPIC-09", *rem_epics}))
    conv_epics = ", ".join(
        # `EPIC-05` entra pela auto-modificação, não pela gama de remediação.
        e + " (auto-mod)" if e == "EPIC-05" else e
        for e in sorted({"EPIC-05", "EPIC-11", *rem_epics})
    )
    # AOS-194 é o ticket que corrigiu a rastreabilidade do STRIDE: o epic dele
    # lê-se do corpus, não se assume.
    stride_epic = epic_of(tickets, "AOS-194")
    lines = [
        "## 6. Rasto descendente: documento técnico → epic → tickets",
        "",
        "O *back-link* que faltava (RAST). Cada documento de `tecnica/` mapeia para o(s) epic(s) e a gama de tickets que o realizam. As gamas por epic seguem `_BRIEF` §8.",
        "",
        "| Doc técnico | Epic(s) implementador(es) | Gama de tickets |",
        "|---|---|---|",
        f"| `tecnica/00_Arquitectura_Solucao.md` | Todos (transversal) | {first} – {last} |",
        "| `tecnica/01_Reference_Monitor_Plano_Controlo.md` | EPIC-01 | AOS-001 – AOS-012 |",
        "| `tecnica/02_Agent_Runtime_Execucao_Duravel.md` | EPIC-02 | AOS-013 – AOS-024 |",
        "| `tecnica/03_Orquestracao_Escalonamento.md` | EPIC-03 | AOS-025 – AOS-034 |",
        "| `tecnica/04_Memoria_Persistencia.md` | EPIC-04 | AOS-035 – AOS-044 |",
        "| `tecnica/05_Skill_Tool_Registry_Supply_Chain.md` | EPIC-05 | AOS-045 – AOS-054 |",
        "| `tecnica/06_Model_Gateway_Custos.md` | EPIC-06 | AOS-055 – AOS-063 |",
        "| `tecnica/07_Seguranca_Isolamento.md` | EPIC-07 | AOS-064 – AOS-075 |",
        "| `tecnica/08_Observabilidade_Evals.md` | EPIC-08 | AOS-076 – AOS-086 |",
        f"| `tecnica/09_Governacao_Conformidade.md` | {gov_epics} | AOS-087 – AOS-097 (+ {epic18_range}) |",
        "| `tecnica/10_Topologia_Implantacao_Operacao.md` | EPIC-10, EPIC-11 | AOS-098 – AOS-108 (+ AOS-118) |",
        f"| `tecnica/11_Convencoes_Engenharia_Evolucao.md` | {conv_epics} | AOS-109 – AOS-118 (+ AOS-045–054, + {epic18_range}) |",
        "| `tecnica/12_Contratos_de_Interface.md` | EPIC-01, EPIC-05, EPIC-06, EPIC-14 | AOS-003, 004; AOS-045–054; AOS-055–063; AOS-144–162 |",
        "| `tecnica/13_Modelo_Dados_Eventos.md` | EPIC-04, EPIC-05, EPIC-08 | AOS-035–044, AOS-045–054, AOS-076–086 |",
        "| `tecnica/14_Matriz_Conformidade.md` | EPIC-07, EPIC-08, EPIC-09 | AOS-072, 076–097 |",
        "| `tecnica/15_Experiencia_HITL_UX.md` | EPIC-12 (+ EPIC-13 frontend) | AOS-119 – AOS-143 |",
        f"| `tecnica/16_Rastreabilidade_RTM.md` | Todos (transversal — meta-rastreabilidade) | {first} – {last} |",
        f"| `tecnica/17_Analise_STRIDE.md` | EPIC-07, EPIC-15, EPIC-16 (análise em {stride_epic}/AOS-194) | AOS-064–075, AOS-163–173, AOS-174–177 |",
        "",
        "```mermaid",
        "flowchart LR",
        # Extremos derivados dos catálogos §2/§3, nunca da contagem de entradas de
        # uma constante deste ficheiro: `NFR-{n_nfrs}` era a contagem de NFR_SPECS
        # (10) a passar-se por identidade, e a §3 já ia em NFR-12.
        f'    RF["RF-01..{rf_ids[-1]} (capacidades)"] --> ADR["ADR-001..{ADR_RANGE[-1].split("-")[1]} (decisoes)"]',
        f'    NFR["NFR-01..{nfr_ids[-1]} (drivers)"] --> ADR',
        f'    ADR --> EPIC["EPIC-01..{last_epic} (entregas)"]',
        f'    EPIC --> TICK["{first}..{last} (tickets)"]',
        '    DOC["tecnica/00..17 (docs)"] --> EPIC',
        '    TICK --> TEST["EPIC-11: AOS-109..118 (verificacao)"]',
        "    NFR --> TEST",
        "```",
        "",
    ]
    section = "\n".join(lines)
    validate_section6(section, tickets)
    return section




# --- §7: lacunas de cobertura -------------------------------------------------
#
# A PROSA de cada lacuna é editorial — qual é a lacuna e o que fazer com ela é
# juízo humano, e gerá-la seria inventá-la. Os NÚMEROS e as EXISTÊNCIAS não: são
# interpolados a partir dos mesmos dados que produzem §§4–5 (`{...}` preenchido
# por `factos`), e `validate_section7` confronta com o corpus tudo o que a secção
# acabe por citar. É a disciplina de `validate_section6` aplicada à secção que a
# auditoria (`analises/10` §5) apontou como «o exemplar mais limpo»: afirmava
# 20/20 ADRs e 12/12 NFRs a setenta linhas de secções geradas, no mesmo ficheiro,
# que diziam 19/19 e 10/10.
# Tickets que realizam o mecanismo de steer/interrupt (RF-10), citados por GAP-04.
# A SELECÇÃO é editorial; a EXISTÊNCIA de cada um é verificada por
# `validate_section7`, pelo que um ticket renumerado ou apagado fica vermelho.
GAP04_STEER = ["AOS-023", "AOS-119", "AOS-158", "AOS-218", "AOS-292"]

GAPS = [
    {
        "id": "GAP-02",
        "lacuna": (
            "**NFR-07 (*overhead* de mediação) sem alvo ratificado** — verificado por "
            "{nfr07}, com alvo agregado «a ratificar por benchmark» e sem SLO numérico fixado"
        ),
        "evidencia": "§3, §5",
        "accao": "Ratificar orçamento por sub-passo com benchmark e fixar SLO numérico",
    },
    {
        "id": "GAP-04",
        "lacuna": (
            "**RF-10 (controlo bidireccional) sem verificação e2e dedicada** — o mecanismo "
            "tem tickets ({steer}), mas nenhum deles é um teste e2e de pausar→corrigir→retomar "
            "em EPIC-11; a verificação mais próxima é AOS-117 (*red-team*)"
        ),
        "evidencia": "§5",
        "accao": "Adicionar caso de teste e2e de pausar→corrigir→retomar em EPIC-11",
    },
    {
        "id": "GAP-05",
        "lacuna": (
            "**Ausência de coluna de estado** — a RTM regista cobertura de *especificação*, "
            "não de *implementação concluída*: nenhum dos {n_tickets} tickets do corpus "
            "traz estado Done/WIP para esta matriz"
        ),
        "evidencia": "§4–5",
        "accao": "Ligar a RTM ao *tracker* (estado por ticket) na próxima revisão",
    },
    {
        "id": "GAP-06",
        "lacuna": (
            "**NFR-09 (DSAR) verificado indirectamente** — provado por {nfr09}; o "
            "*crypto-shredding* tem ticket próprio (AOS-093) e um defeito conhecido de alcance "
            "(AOS-290), mas nenhum teste e2e exercita um DSAR sobre o log encadeado"
        ),
        "evidencia": "§5",
        "accao": "Criar teste e2e de *crypto-shredding* preservando integridade da hash-chain",
    },
    {
        "id": "GAP-08",
        "lacuna": (
            "**Os catálogos de enunciado estão atrás do catálogo de documentos** — "
            "`docs/adr/README.md` enuncia {n_readme} ADRs e é a fonte do canon que os gates "
            "lêem (AOS-314), mas `_BRIEF` §3 enuncia {n_brief} (faltam {faltam_brief}) e "
            "`specs/00` §11 enuncia {n_sysspec} (faltam {faltam_sysspec}). O próprio README "
            "declara os dois «a referência de enunciado para todos os ADRs», pelo que a "
            "divergência é, pela sua própria regra, um defeito e não uma actualização"
        ),
        "evidencia": "`_BRIEF` §3, `specs/00` §11, `docs/adr/README.md`",
        "accao": (
            "Completar os dois catálogos de enunciado com os ADRs em falta, ou emendar o "
            "README para deixar de os declarar referência de enunciado"
        ),
    },
]

# Lacunas que o corpus FECHOU. Ficam registadas com a evidência que as fechou, em
# vez de desaparecerem: uma lacuna que some sem explicação é indistinguível de uma
# lacuna varrida para debaixo do tapete.
GAPS_FECHADAS = [
    {
        "id": "GAP-01",
        "porque": (
            "ADR-014 (L0–L5) foi registado como sub-coberto com 3 tickets; §4 conta agora "
            "{adr014_n} ({adr014_tickets}), acima do limiar de sub-cobertura (≤3), e a acção "
            "recomendada — medição de fiabilidade e demoção automática — é AOS-090, em EPIC-09"
        ),
    },
    {
        "id": "GAP-07",
        "porque": (
            "registava que ADR-020…023 estavam fora do canon lido pelos gates e deixava a "
            "decisão por tomar. **Decidido em AOS-314: o canon passa a ADR-001…{ultimo_adr_n}.** "
            "ADR-020 tinha zero tickets e teria posto o `ref-lint` vermelho; passou a ser citado "
            "pelos cinco tickets que o próprio ADR nomeia (§5 e §6), em `specs/EPIC-19` — a "
            "lacuna era da citação, não da cobertura"
        ),
    },
    {
        "id": "GAP-03",
        "porque": (
            "ADR-003 foi registado como concentrado em AOS-005/006; §4 conta agora "
            "{adr003_n} tickets, e a rotação/revogação tem eixo próprio em AOS-288 e AOS-300"
        ),
    },
]


def _adr_catalogo_completo() -> dict:
    """
    {ADR-NNN: estado} lido de `docs/adr/README.md`. Serve para §7 poder falar de
    ADRs FORA de `ADR_RANGE` sem os inventar — e para `validate_section7` recusar
    uma citação a um ADR que não exista em lado nenhum.
    """
    catalogo = {}
    readme = DOCS_ADR_DIR / "README.md"
    if not readme.exists():
        return catalogo
    for line in _read(readme).splitlines():
        m = re.match(r"\| (ADR-\d{3}) \| (.*?) \| \*\*(.*?)\*\*", line)
        if m:
            catalogo[m.group(1)] = m.group(3).strip()
    return catalogo


def _factos_catalogos() -> dict:
    """Números de GAP-08, contados dos ficheiros (ver `contar_adrs_por_catalogo`)."""
    cat = contar_adrs_por_catalogo()
    readme = cat["`docs/adr/README.md`"]
    def _faltam(conjunto):
        em_falta = sorted(readme - conjunto)
        return ", ".join(em_falta) if em_falta else "nenhum"
    return {
        "n_readme": len(readme),
        "n_brief": len(cat["_BRIEF §3"]),
        "n_sysspec": len(cat["`specs/00` §11"]),
        "faltam_brief": _faltam(cat["_BRIEF §3"]),
        "faltam_sysspec": _faltam(cat["`specs/00` §11"]),
    }


def generate_section7(rows: list, tickets: dict, stats: dict, nfr_ids: list) -> str:
    """
    Gera a §7 a partir dos MESMOS dados que produzem §§4–5.

    A frase de cobertura era, até aqui, escrita à mão — e afirmava 20/20 e 12/12
    contra as 19 e 10 linhas geradas setenta linhas acima. Passa a ser derivada:
    não há forma de a fazer discordar de §4/§5 sem mudar §4/§5.
    """
    contagens = {r["adr"]: r["count"] for r in rows}
    por_adr = {r["adr"]: r["tickets"] for r in rows}

    # ADRs citados por tickets mas fora de ADR_RANGE — a lacuna que GAP-07 regista.
    fora = defaultdict(list)
    for aos, info in tickets.items():
        for adr in info["adrs"]:
            if adr not in set(ADR_RANGE):
                fora[adr].append(aos)
    catalogo = _adr_catalogo_completo()
    extra = sorted(a for a in catalogo if a not in set(ADR_RANGE))
    extra_contagens = ", ".join(
        f"{adr} com {len(fora.get(adr, []))} ticket(s)" for adr in extra
    ) or "nenhum ADR fora da gama"

    factos = {
        "n_tickets": stats["n_tickets"],
        "steer": ", ".join(GAP04_STEER),
        **_factos_catalogos(),
        "ultimo_adr": ADR_RANGE[-1].split("-")[1],
        "ultimo_adr_n": ADR_RANGE[-1].split("-")[1],
        "adr_extra_contagens": extra_contagens,
        "adr014_n": contagens.get("ADR-014", 0),
        "adr014_tickets": ", ".join(por_adr.get("ADR-014", [])) or "—",
        "adr003_n": contagens.get("ADR-003", 0),
        "nfr07": ", ".join(sorted(NFR_MANUAL_TICKETS.get("NFR-07", set()))) or "—",
        "nfr09": ", ".join(sorted(NFR_MANUAL_TICKETS.get("NFR-09", set()))) or "—",
    }

    n_adrs_cobertos = sum(1 for r in rows if r["count"] > 0)
    n_nfrs_cobertos = sum(1 for nfr, *_ in NFR_SPECS if NFR_MANUAL_TICKETS.get(nfr))

    lines = [
        "## 7. Lacunas de cobertura",
        "",
        "Sinalizadas a partir dos dados reais das §§4–5, e **geradas com elas**: os números "
        "desta secção são interpolados das mesmas matrizes, não reafirmados à mão. A prosa de "
        "cada lacuna é editorial; tudo o que ela cite — ticket, ADR ou NFR — é confrontado com "
        "o corpus antes de a secção ser escrita (AOS-313).",
        "",
        "| ID | Lacuna | Evidência | Acção recomendada |",
        "|---|---|---|---|",
    ]
    for gap in GAPS:
        lines.append(
            f"| {gap['id']} | {gap['lacuna'].format(**factos)} | {gap['evidencia']} | "
            f"{gap['accao'].format(**factos)} |"
        )
    lines.append("")
    lines.append(
        f"Nenhum ADR do canon gated e nenhum NFR está **sem** cobertura mínima: "
        f"{n_adrs_cobertos}/{len(rows)} ADRs (ADR-001…{ADR_RANGE[-1].split('-')[1]}) e "
        # Denominador e extremo saem do catálogo §3, não de `len(NFR_SPECS)`: a
        # contagem das linhas desta matriz não é a identidade do último NFR.
        f"{n_nfrs_cobertos}/{len(nfr_ids)} NFRs (NFR-01…{nfr_ids[-1]}) têm pelo "
        f"menos um ticket associado. As lacunas acima são de **profundidade e verificação** — "
        f"excepto GAP-08, que é de **coerência entre catálogos**."
    )
    lines.append("")
    lines.append("**Lacunas fechadas pelo corpus** (registadas com a evidência que as fechou):")
    lines.append("")
    for gap in GAPS_FECHADAS:
        lines.append(f"- **{gap['id']}** — {gap['porque'].format(**factos)}.")
    lines.append("")
    section = "\n".join(lines)
    validate_section7(section, tickets, catalogo)
    return section


def validate_section7(section: str, tickets: dict, catalogo: dict) -> None:
    """
    Asserção anti-recorrência da §7: nada do que a secção cite pode ser inventado.

    Cada `AOS-NNN` tem de existir no backlog, cada `ADR-NNN` no catálogo e cada
    `NFR-NN` em `NFR_SPECS`. Falha fechado. Sem isto a §7 continuaria a ser o que
    `analises/10` §5 descreveu: a única secção da RTM excluída *tanto* da
    regeneração *quanto* do `ref-lint` — nada a lia, e derivou.
    """
    errors = []
    conhecidos_adr = set(catalogo) | set(ADR_RANGE)
    conhecidos_nfr = {nfr for nfr, *_ in NFR_SPECS}

    for aos in sorted(set(re.findall(r"AOS-\d{3}", section))):
        if aos not in tickets:
            errors.append(f"cita {aos}, que não existe em specs/EPIC-*.md.")
    for adr in sorted(set(re.findall(r"ADR-\d{3}", section))):
        if adr not in conhecidos_adr:
            errors.append(f"cita {adr}, que não existe no catálogo de ADRs.")
    for nfr in sorted(set(re.findall(r"NFR-\d{2}", section))):
        if nfr not in conhecidos_nfr:
            errors.append(f"cita {nfr}, que não existe em NFR_SPECS.")

    if errors:
        sys.stderr.write("ERRO: a §7 cita entidades que não existem no corpus:\n")
        for e in errors:
            sys.stderr.write(f"  - {e}\n")
        sys.exit(1)


def regenerate_rtm(tickets: dict, adr_titles: dict) -> str:
    rtm_text = _read(RTM_PATH)
    stats = corpus_stats(tickets)
    # Catálogos de requisitos lidos do RTM ANTES de qualquer substituição: são a
    # fonte dos identificadores RF/NFR e o gerador não os reescreve.
    rf_ids = requirement_catalogue(rtm_text, RF_HEADING, "RF")
    nfr_ids = requirement_catalogue(rtm_text, NFR_HEADING, "NFR")

    # Actualiza §1
    rtm_text = update_section1(rtm_text, stats, rf_ids, nfr_ids)

    # Substitui §4–§5
    sec4 = generate_section4(build_adr_matrix(tickets, adr_titles))
    sec5 = generate_section5(tickets, nfr_ids)
    new_middle = sec4 + sec5
    pattern = re.compile(r"(## 4\. Matriz ADR × ticket.*?)(?=\n## 6\. )", re.DOTALL)
    m = pattern.search(rtm_text)
    if not m:
        sys.stderr.write("ERRO: não encontrou secções §4–§5 no RTM\n")
        sys.exit(1)
    rtm_text = rtm_text[:m.start()] + new_middle + rtm_text[m.end():]

    # Substitui §6
    sec6 = generate_section6(tickets, stats, rf_ids, nfr_ids)
    pattern6 = re.compile(r"## 6\. Rasto descendente: documento técnico → epic → tickets.*?\n---", re.DOTALL)
    m6 = pattern6.search(rtm_text)
    if not m6:
        sys.stderr.write("ERRO: não encontrou secção §6 no RTM\n")
        sys.exit(1)
    rtm_text = rtm_text[:m6.start()] + sec6 + "\n---" + rtm_text[m6.end():]

    # Substitui §7. Era a única secção da RTM fora da regeneração E fora do
    # ref-lint (`analises/10` §5); a frase de cobertura afirmava 20/20 ADRs e
    # 12/12 NFRs contra as 19 e 10 linhas geradas no mesmo ficheiro.
    sec7 = generate_section7(build_adr_matrix(tickets, adr_titles), tickets, stats, nfr_ids)
    pattern7 = re.compile(r"## 7\. Lacunas de cobertura.*?\n---", re.DOTALL)
    m7 = pattern7.search(rtm_text)
    if not m7:
        sys.stderr.write("ERRO: não encontrou secção §7 no RTM\n")
        sys.exit(1)
    rtm_text = rtm_text[:m7.start()] + sec7 + "\n---" + rtm_text[m7.end():]

    # Última porta antes de escrever ou comparar: nenhum número afirmado no
    # ficheiro pode divergir da fonte que o gerador acabou de derivar.
    assert_numeric_claims(rtm_text, rf_ids, nfr_ids)
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
    missing = sorted(expected - all_aos - ATRIBUIDOS_NOUTRO_RAMO)
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
