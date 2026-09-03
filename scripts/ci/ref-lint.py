#!/usr/bin/env python3
"""
ref-lint.py — Gate 2b: validação de referências cruzadas do corpus documental.

Verifica:
1. Todos os AOS-NNN citados em specs/, docs/adr/ e tecnica/ (a RTM incluída, AOS-313)
       existem no backlog (specs/EPIC-*.md).
2. Todos os ADR-001..ADR-023 canónicos têm pelo menos um ticket implementador
       no backlog.
3. Todos os ADR-NNN citados em specs/ existem no catálogo de ADRs
       (docs/adr/README.md e specs/00_System_Spec.md §11).
4. (AOS-198, residual de AOS-194 CA5) Onde um documento DECLARA EXPLICITAMENTE o
       título de um AOS-NNN, esse título casa com o título real do ticket na sua
       EPIC. Ver «critério de correspondência» abaixo.

Saída fail-closed: exit != 0 quando há referências quebradas.

CRITÉRIO DE CORRESPONDÊNCIA TÍTULO↔TICKET (verificação 4)
---------------------------------------------------------
O defeito que esta verificação impede é o STR-01: a coluna «Ticket» de
`tecnica/17` estava sistematicamente desviada 1–2 posições (≈3 mapeamentos
correctos em ≈55), e nada o detectava. O risco simétrico é um critério estrito
que enche a CI de falsos positivos e acaba desligado — o que seria pior do que
não ter verificação. Por isso o critério é deliberadamente assimétrico:

*Onde* verifica — SÓ onde o documento declara o título, em duas formas
inequívocas:
  (a) linha de tabela cuja PRIMEIRA célula é exactamente `AOS-NNN` e cuja
      segunda célula é o título;
  (b) CABEÇALHO `## AOS-NNN — Título`.
Uma citação nua (`AOS-069`), uma lista (`AOS-069, AOS-183`), um comentário
(`AOS-183: o TaintGate está inerte`) ou um aposto em prosa (`AOS-034 — o último
do EPIC-03`) NÃO declaram título e NÃO são verificados — seria adivinhação.

*Contra o quê* compara — LEAVE-ONE-OUT, e é isto que a torna falsificável. As
duas formas acima são também as formas de onde o título canónico é extraído do
backlog; comparar uma declaração contra um conjunto de referência que a INCLUI
seria comparar o texto consigo mesmo, e nenhuma troca de título dentro da
própria EPIC ficaria vermelha (era o caso até esta versão: 395/395 declarações
provinham de `specs/EPIC-*.md`, logo a verificação era tautológica e não podia
falhar). A referência de cada declaração é, por isso, a união das palavras de
TODAS AS OUTRAS formas do título do ticket, excluindo a própria linha que está a
ser testada. Neste corpus 195 dos 204 tickets têm duas formas independentes
(cabeçalho detalhado + linha da tabela-resumo), pelo que a troca do STR-01 fica
vermelha; os tickets com uma só forma ficam sem referência independente e a
verificação **abstém-se** — o número de abstenções é IMPRESSO em cada execução
para que a cobertura seja visível em vez de presumida.

O QUE ESTA VERIFICAÇÃO **NÃO** APANHA (medido, não presumido)
-------------------------------------------------------------
Não apanha a forma exacta em que o STR-01 ocorreu. A tabela de `tecnica/17` cita
os tickets numa coluna «Ticket» **sem título nenhum** (`| … | AOS-069, AOS-183 |
…`), pelo que não há título declarado para comparar. Foi testada a alternativa
semântica — exigir que cada ticket citado numa linha de tabela partilhe uma
palavra significativa com a *descrição* dessa linha — contra as duas versões de
`tecnica/17` (antes e depois de `ff9761f`, a correcção de AOS-194): 32/55
discordâncias (58%) na versão errada contra 35/213 (16%) na versão corrigida. O
sinal existe, mas 16% de falsos positivos numa versão SABIDAMENTE correcta faria
um gate que ninguém aguenta — e um gate desligado é pior do que gate nenhum. A
heurística fica registada aqui, com os números, em vez de ser ligada e depois
silenciada por uma baseline de 35 entradas.

Consequência honesta para o residual AOS-194 CA5: fica fechado para as citações
que DECLARAM título (que são as verificáveis sem adivinhação) e **por fechar**
para a coluna «Ticket» nua de `tecnica/17`. Essa parte está devolvida como
pendência no ticket, não dada por absorvida.

*Como* compara — por PALAVRAS SIGNIFICATIVAS, não byte-a-byte: acentos
removidos, minúsculas, stopwords e palavras com menos de 4 letras descartadas.
Casa se houver ≥1 palavra significativa em comum, aceitando variação morfológica
por prefixo comum de ≥5 caracteres (`configuração`/`configurar`). O título de um
OUTRO ticket tem, na prática, zero palavras em comum — que é exactamente o
desvio do STR-01. Se o título citado não tiver nenhuma palavra significativa, a
verificação abstém-se (não há como julgar).

Uso:
    python3 scripts/ci/ref-lint.py
"""

import os
import re
import sys
import unicodedata
from collections import defaultdict
from pathlib import Path

# Raiz do corpus. Sobreponível por env APENAS para o self-test (§P2) poder provar
# ponta-a-ponta, sobre uma CÓPIA do corpus, que uma troca de título fica vermelha
# — sem tocar em `specs/`. O job de CI não define a variável.
REPO_ROOT = Path(os.environ.get("AOS_REFLINT_ROOT") or Path(__file__).resolve().parents[2])
SPECS_DIR = REPO_ROOT / "specs"
DOCS_ADR_DIR = REPO_ROOT / "docs" / "adr"
TECNICA_DIR = REPO_ROOT / "tecnica"

ADR_RANGE = [f"ADR-{i:03d}" for i in range(1, 24)]

# --- Verificação 4: título↔ticket (AOS-198) ----------------------------------

# Declaração de título em linha de tabela: primeira célula = só o ID.
RE_TITLE_TABLE = re.compile(r"^\|\s*\**\s*(AOS-\d{3})\s*\**\s*\|([^|]*)\|")
# Declaração de título em CABEÇALHO: `## AOS-198 — Criar o gate 4 ...`.
# Restringida a cabeçalhos por MEDIÇÃO, não por gosto: a forma `AOS-NNN — texto`
# em prosa corrida é, neste corpus, um separador de COMENTÁRIO, não um título
# («AOS-034 — o último do EPIC-03», «AOS-110 — não o implementes aqui»). Aceitá-la
# produzia 16 falsos positivos em specs/ e tecnica/, e zero verdadeiros.
RE_TITLE_HEADING = re.compile(r"^#{1,6}\s+\**\s*(AOS-\d{3})\s*\**\s*[-–—]\s*(.+?)\s*$")
# Cabeçalho de secção detalhada de ticket na EPIC (fonte do título canónico).
RE_TICKET_HEADING = re.compile(r"^#{2,3} (AOS-\d{3})\s*[-–—]\s*(.*?)$", re.MULTILINE)

# Stopwords PT + ruído editorial recorrente no corpus (estado, prioridade,
# marcadores de entrega). Não são discriminantes entre tickets.
STOPWORDS = {
    "para", "pela", "pelo", "pelas", "pelos", "como", "onde", "quando", "esta",
    "este", "essa", "esse", "isto", "isso", "aquele", "aquela", "sobre", "entre",
    "sem", "com", "dos", "das", "nos", "nas", "que", "uma", "uns", "umas",
    "seus", "suas", "sua", "seu", "mais", "menos", "todo", "toda", "todos",
    "todas", "cada", "ainda", "apenas", "tambem", "porque", "assim", "outro",
    "outra", "outros", "outras", "nova", "novo", "novos", "novas",
    "feature", "chore", "docs", "spike", "refactor", "test", "fix",
    "entregue", "aberto", "fechado", "parcial", "residual", "residuais",
    "ticket", "tickets", "epic", "epics", "critério", "criterio",
}

# Tickets cuja verificação de título se abstém: o corpus usa-os como rótulo
# genérico. Vazio por omissão — existe para ser preenchido com dono, não em massa.
TITLE_CHECK_SKIP = set()


def _norm_tokens(text: str) -> set:
    """Palavras significativas: sem acentos, minúsculas, >= 4 letras, sem stopwords."""
    text = re.sub(r"AOS-\d{3}|ADR-\d{3}", " ", text)
    text = unicodedata.normalize("NFD", text)
    text = "".join(c for c in text if unicodedata.category(c) != "Mn")
    words = re.split(r"[^0-9A-Za-z_]+", text.lower())
    return {w for w in words if len(w) >= 4 and w not in STOPWORDS and not w.isdigit()}


def _tokens_match(a: str, b: str) -> bool:
    """Igualdade ou variação morfológica (prefixo comum de >= 5 caracteres)."""
    if a == b:
        return True
    n = 0
    for ca, cb in zip(a, b):
        if ca != cb:
            break
        n += 1
    return n >= 5


def titles_agree(cited: str, reference_tokens: set) -> bool:
    """True se o título citado partilhar >= 1 palavra significativa com o real."""
    cited_tokens = _norm_tokens(cited)
    if not cited_tokens:
        return True  # sem palavras significativas: a verificação abstém-se
    for a in cited_tokens:
        for b in reference_tokens:
            if _tokens_match(a, b):
                return True
    return False


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
            adrs = (
                set()
                if RE_ADRS_MENCIONADOS.search(block)
                else set(re.findall(r"ADR-\d{3}", block))
            )
            entry = tickets.setdefault(aos, {"adrs": set(), "file": epic_file, "titles": []})
            entry["adrs"] = adrs
            entry["file"] = epic_file
            # A PROVENIÊNCIA (ficheiro:linha) é guardada porque a verificação 4 é
            # leave-one-out: sem ela não se sabe qual das formas do título é a que
            # está a ser testada, e a comparação degenera em texto-contra-si-mesmo.
            line_no = text[: m.start()].count("\n") + 1
            entry["titles"].append({"title": m.group(2).strip(), "path": epic_file, "line": line_no})

        # Tabelas de resumo (garantem que tickets sem secção detalhada constam,
        # e são a segunda fonte do título canónico — verificação 4).
        for line_no, line in enumerate(text.splitlines(), start=1):
            m = re.match(r"\|\s*(AOS-\d{3})(?:\s*[^|]*)?\s*\|", line)
            if m:
                aos = m.group(1)
                entry = tickets.setdefault(aos, {"adrs": set(), "file": epic_file, "titles": []})
                mt = RE_TITLE_TABLE.match(line)
                if mt:
                    entry["titles"].append(
                        {"title": mt.group(2).strip(), "path": epic_file, "line": line_no}
                    )
    return tickets


def reference_title_forms(backlog: dict) -> dict:
    """
    {AOS-NNN: [(caminho, linha, tokens)]} — uma entrada por FORMA do título no
    backlog, com proveniência. É a base do leave-one-out da verificação 4.
    """
    out = {}
    for aos, info in backlog.items():
        forms = []
        for t in info.get("titles", []):
            forms.append((str(t["path"]), t["line"], _norm_tokens(t["title"])))
        out[aos] = forms
    return out


def reference_title_tokens(backlog: dict) -> dict:
    """
    Vocabulário canónico por ticket: união das palavras significativas de TODAS
    as formas do título no backlog (cabeçalho detalhado + célula da tabela de
    resumo). A união é deliberada — as duas formas divergem em anotações de
    estado, e exigir a coincidência das duas seria estrito de mais.

    NOTA: esta união inclui todas as formas. Na verificação 4 usa-se a variante
    leave-one-out (`reference_tokens_excluding`); esta função continua a existir
    porque é o vocabulário completo do ticket, útil para inspecção e para o
    subteste §P do self-test, que exercita o predicado com pares escritos à mão.
    """
    out = {}
    for aos, forms in reference_title_forms(backlog).items():
        tokens = set()
        for _, _, toks in forms:
            tokens |= toks
        out[aos] = tokens
    return out


def reference_tokens_excluding(forms: list, path, line_no: int) -> set:
    """
    Referência leave-one-out: união das palavras de todas as formas do título
    EXCEPTO a que está em `path:line_no` — a linha que está a ser testada. Um
    conjunto vazio significa «não há forma independente»: a verificação abstém-se
    em vez de comparar o texto consigo mesmo.
    """
    tokens = set()
    for form_path, form_line, toks in forms:
        if form_path == str(path) and form_line == line_no:
            continue
        tokens |= toks
    return tokens


def extract_title_claims(paths: list, skip_paths: set) -> list:
    """
    Devolve [(path, linha, AOS-NNN, titulo_citado, forma)] para cada sítio onde
    um documento DECLARA o título de um ticket. Só as duas formas inequívocas
    documentadas no cabeçalho deste ficheiro.
    """
    claims = []
    for base in paths:
        for path in base.rglob("*.md"):
            if path in skip_paths or not path.is_file():
                continue
            for n, line in enumerate(_read(path).splitlines(), start=1):
                mt = RE_TITLE_TABLE.match(line)
                if mt:
                    title = mt.group(2).strip()
                    if title:
                        claims.append((path, n, mt.group(1), title, "tabela"))
                    continue
                mh = RE_TITLE_HEADING.match(line)
                if mh:
                    claims.append((path, n, mh.group(1), mh.group(2).strip(), "cabeçalho"))
    return claims


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

    # Garantir que o catálogo inclui os ADRs canónicos, mesmo que ainda não
    # materializados individualmente.
    adr_catalog |= set(ADR_RANGE)

    # A RTM esteve aqui em `skip` desde sempre, com a justificação implícita de
    # que cita todos os AOS-NNN e ADR-NNN do corpus e verificá-la seria redundante.
    # Não era: significava que uma referência PARTIDA na RTM era a única do corpus
    # que ninguém lia — e a §7 derivou precisamente aí (`analises/10` §5, AOS-313).
    # Verificar a RTM custa um ficheiro a mais e fecha a segunda metade da lacuna;
    # a primeira é a §7 passar a ser gerada.
    skip = set()
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

    # 4. Título citado vs. título real do ticket (AOS-198; residual de AOS-194 CA5).
    #    Leave-one-out: a referência NUNCA inclui a linha que está a ser testada.
    ref_forms = reference_title_forms(backlog)
    claims = extract_title_claims([SPECS_DIR, DOCS_ADR_DIR, TECNICA_DIR], skip)
    mismatched = []
    checked_titles = 0
    abstained = 0
    for path, line_no, aos, cited, form in claims:
        if aos not in ref_forms or aos in TITLE_CHECK_SKIP:
            continue  # inexistência já é reportada pela verificação 1
        reference = reference_tokens_excluding(ref_forms[aos], path, line_no)
        if not reference:
            # Sem NENHUMA outra forma do título: não há referência independente e
            # comparar seria tautológico. Abstém-se — e conta-se.
            abstained += 1
            continue
        checked_titles += 1
        if not titles_agree(cited, reference):
            mismatched.append((path, line_no, aos, cited, form, sorted(reference)))

    if mismatched:
        print(
            f"ERRO: {len(mismatched)} título(s) citado(s) não casam com o título do ticket "
            f"na EPIC (zero palavras significativas em comum):"
        )
        for path, line_no, aos, cited, form, reference in mismatched:
            print(f"  - {path}:{line_no} [{form}] {aos}")
            print(f"      citado no documento: {cited}")
            print(f"      palavras do título real: {', '.join(reference) or '(nenhuma)'}")
        exit_code = 1

    if exit_code == 0:
        print(
            f"Referências cruzadas OK: "
            f"{len(backlog_set)} tickets no backlog, "
            f"{len(ADR_RANGE)} ADRs canónicos com cobertura, "
            f"{len(citations)} ficheiros verificados, "
            f"{checked_titles} declaração(ões) de título verificada(s) contra a EPIC "
            f"(leave-one-out; {abstained} abstenção(ões) por falta de forma independente)."
        )

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
