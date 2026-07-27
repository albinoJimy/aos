#!/usr/bin/env python3
"""
deferrals.py — Gate «deferrals»: todo o deferimento declarado no código tem eixo
verificável (AOS-196, achados DEF-01/DEF-03/DEF-06 da auditoria v4).

PORQUÊ ESTE GATE EXISTE
-----------------------
O padrão que a auditoria v4 encontrou NÃO foi «dívida escondida» — foi **eixo
errado**. Os deferimentos estão declarados, em voz alta, no sítio certo; o que
está errado é o *destino*: apontam para epics que não têm ticket nenhum para
eles.

  - a **cifra por-titular do substrato** é apontada a `EPIC-06/09/10` em
    `packages/cmd/aos/bootstrap.go` e a `EPIC-13` em `tecnica/02` — e o EPIC-13 é
    o epic de *Frontend*. Nenhum dos quatro epics tem ticket para ela;
  - o **anti-replay do ADR-012** é apontado a `EPIC-13` (Frontend);
  - a **assinatura de imagem do ADR-017** é apontada a `EPIC-10`, que também não
    tem ticket para ela.

Um eixo que aponta para um epic sem ticket é indistinguível, na prática, de não
ter eixo nenhum: ninguém o executa e nada o reporta. A recomendação 7 da
auditoria pede a propriedade que este gate impõe:

  «Todo o marcador DEFERIDO/demo-grade em `packages/**/*.go` (não-teste) deve ter
   entrada no registo com eixo que **contenha um ticket** para ele. Verificável
   por script.»

O QUE ESTE GATE VERIFICA (e só isto)
------------------------------------
1. **Cobertura.** Cada par (ficheiro `.go` de produção, MARCADOR) encontrado em
   `packages/**` tem pelo menos uma linha no registo
   `docs/governance/REGISTO-Deferimentos.md`. Marcador sem linha ⇒ FALHA.
1b. **Contagem declarada.** O §3.1 do registo declara QUANTAS ocorrências cada par
   tem. Observada > declarada ⇒ FALHA (dívida nova por registar); observada <
   declarada ⇒ FALHA (contagem por actualizar); par declarado que já não existe
   no código ⇒ FALHA. Sem isto, a chave (ficheiro, MARCADOR) deixava passar em
   silêncio um deferimento NOVO num ficheiro JÁ registado — que é o caminho mais
   comum de acumulação de dívida, não o ficheiro novo.
2. **Eixo verificável.** A célula `Eixo` de cada linha ou cita ≥1 `AOS-NNN` que
   EXISTE no backlog (`specs/EPIC-*.md`), ou traz o literal `POR ATRIBUIR`. Um
   `AOS-NNN` citado que não exista no backlog ⇒ FALHA (a mesma regra do gate
   `ref-lint`, aplicada à coluna que decide quem executa). Uma célula que só cite
   `EPIC-NN` falha nas duas condições ⇒ FALHA. **É esta regra que fecha DEF-01.**
3. **`POR ATRIBUIR` não é escotilha.** Toda a linha com `POR ATRIBUIR` tem de ser
   nomeada no CABEÇALHO de uma nota `### N-DEF-NNN — cobre DEF-…` no §6 do
   registo, onde o ticket em falta é descrito. Sem a nota ⇒ FALHA. O número de
   linhas `POR ATRIBUIR` é IMPRESSO em cada execução — é o contador da dívida sem
   dono de execução.
4. **Vocabulário controlado + campos obrigatórios.** `Marcador` e `Estado` só
   podem conter valores do vocabulário; `Dono` e `Gatilho` não podem estar
   vazios nem ser `—`. Qualquer outro valor ⇒ FALHA (não é ignorado em silêncio).
5. **O registo só encolhe.** Uma linha cujo `Marcador` é de código e cuja âncora
   `.go` já NÃO contém esse marcador é OBSOLETA ⇒ FALHA até ser removida. Uma
   linha `DOCUMENTAL` ancorada num `.go` que já não declara deferimento nenhum
   (nem marcador, nem a palavra) é igualmente OBSOLETA — sem esta cláusula, as
   linhas DOCUMENTAL sobre código tornavam-se permanentes por inércia no dia em
   que o texto do ficheiro fosse corrigido. Uma linha cuja âncora não existe na
   árvore é ÓRFÃ ⇒ FALHA. Sem estas regras, uma linha que nunca mais é visitada
   torna-se permanente em silêncio — o mecanismo exacto pelo qual a regra
   deixaria de ser verdadeira (mesma lição das baselines de AOS-198).
6. **Anti-eixo-fantasma no corpus.** Nenhuma linha de `packages/**/*.go`
   (não-teste), `docs/adr/**.md` ou `tecnica/**.md` pode deferir (`DEFERIDO`/
   `DIFERIDO`/`deferido`/`diferido`/`deferimento`/`dívida` — AS DUAS GRAFIAS)
   para um `EPIC-NN` **sem nomear um `AOS-NNN` na mesma linha**, salvo se constar
   de `scripts/ci/baseline/deferrals.txt` com `owner=`. É a forma textual exacta
   do defeito DEF-01. A baseline segue as regras de AOS-198: dono obrigatório por
   entrada, só encolhe, entrada obsoleta FALHA.

O QUE ESTE GATE **NÃO** VERIFICA (declarado — um gate que promete mais do que faz
seria o mesmo defeito noutro sítio)
--------------------------------------------------------------------------------
- **Identidade da linha.** A chave de cobertura é `(ficheiro, MARCADOR)`, não
  `(ficheiro, linha)`: a chave por linha ficava vermelha a cada edição de
  comentário e seria desligada. O buraco que isso abria — um deferimento NOVO num
  ficheiro já registado passar em silêncio — está fechado pela verificação 1b (a
  contagem declarada). O que continua fora é a IDENTIDADE da ocorrência: trocar um
  deferimento por outro dentro do mesmo ficheiro, sem mexer no total, não é
  distinguível. São permitidas VÁRIAS linhas para o mesmo par (é assim que
  `bootstrap.go|DEFERIDO` traz três eixos distintos).
- **`diferid*` em minúsculas** não é marcador de código: é a palavra portuguesa
  corrente e ocorre 23× em prosa técnica sem dívida nenhuma. Continua a ser
  apanhada pela verificação 6, onde a exigência de um `EPIC-NN` na mesma linha
  elimina o ruído.
- **Semântica do eixo.** Verifica que o ticket citado EXISTE, não que ele cobre
  aquele deferimento. Um `AOS-NNN` existente mas irrelevante passa. O que estava
  errado em DEF-01 era citar um destino sem ticket nenhum; é isso que fica
  fechado. A adequação do ticket é revisão humana, e a coluna `Gatilho` é o que a
  torna revisável.
- **Prosa.** A verificação 6 é por LINHA e ISENTA qualquer linha que traga um
  `AOS-NNN`, seja por que razão for. Um deferimento cuja frase se estende por duas
  linhas, com o `EPIC-NN` numa e um `AOS-NNN` na outra, escapa; e uma linha que
  cite um `AOS-NNN` por outra razão qualquer (ex.: o ADR-012 cita `AOS-096` na
  mesma frase em que defere para o EPIC-13; as próprias correcções de AOS-196
  citam `AOS-196`) também escapa. Foi medido: sem a exigência de `AOS-NNN` na
  linha, o varrimento produzia dezenas de falsos positivos sobre referências
  legítimas a epics. As duas ocorrências que escapam por esta razão estão
  corrigidas à mão por AOS-196 e cobertas pelas linhas DEF-401/DEF-501 do registo.
  A cobertura desta verificação é, portanto, MENOR do que o nome sugere: apanha a
  forma canónica do DEF-01, não toda a forma possível de o cometer.
- **`specs/`** está FORA da verificação 6: no backlog, escopar trabalho a um epic
  é a operação normal do documento, não um eixo de deferimento.

Uso:
    python3 scripts/ci/deferrals.py
"""

import os
import re
import sys
from pathlib import Path

if hasattr(sys.stdout, "reconfigure"):  # Windows: consola cp1252 não parte o gate
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

REPO_ROOT = Path(os.environ.get("AOS_DEFERRALS_ROOT") or Path(__file__).resolve().parents[2])
PACKAGES_DIR = REPO_ROOT / "packages"
SPECS_DIR = REPO_ROOT / "specs"
REGISTO = REPO_ROOT / "docs" / "governance" / "REGISTO-Deferimentos.md"
BASELINE = REPO_ROOT / "scripts" / "ci" / "baseline" / "deferrals.txt"
CORPUS_MD = [REPO_ROOT / "docs" / "adr", REPO_ROOT / "tecnica"]

# --- Vocabulário controlado --------------------------------------------------

# Marcadores de CÓDIGO. A ancoragem em MAIÚSCULAS de STUB, CONDICIONAL e
# DIFERIDO não é gosto: é medida. Em minúsculas, `stub` aparece 30× neste código
# em NEGAÇÕES («nunca um stub», «recusa stubs») e em identificadores Go
# (`BudgetStub`), e `condicional` aparece em prosa comum — registá-los produzia
# linhas sem significado e um gate que seria desligado. A forma MAIÚSCULA é a que
# o corpus usa deliberadamente para DECLARAR a fronteira («STUBS NEUTROS», «é o
# STUB histórico»). `demo-grade`/`NUNCA usar em produção` são declarações em
# qualquer caixa.
#
# DEFERIDO vs DIFERIDO (as DUAS grafias, medidas em 2026-07-27):
#   - `deferid*` (com E) ocorre 5× em `packages/**/*.go` de produção e as 5 são
#     declarações de fronteira ⇒ detectado em QUALQUER caixa;
#   - `DIFERID*` em MAIÚSCULAS ocorre 25× em 16 ficheiros e as 25 são declarações
#     deliberadas ⇒ detectado como marcador próprio;
#   - `diferid*` em minúsculas ocorre 23× e é a palavra portuguesa CORRENTE: pelo
#     menos quatro dessas ocorrências são prosa técnica sem dívida nenhuma («a
#     libertação do sealMu é diferida», «a materialização é diferida», «a
#     compactação é diferida para o checkpoint») e uma é a NEGAÇÃO exacta («deixa
#     de ser diferida»). Registá-las produzia linhas sem significado ⇒ NÃO é
#     marcador de código. Fica declarado como limite no §4 do registo, e a
#     verificação 6 (anti-eixo-fantasma) apanha-a na mesma, porque aí a exigência
#     adicional de um `EPIC-NN` na linha elimina o ruído.
CODE_MARKERS = {
    "DEFERIDO": re.compile(r"\bDEFERID[OA]S?\b|\bdeferid[oa]s?\b"),
    "DIFERIDO": re.compile(r"\bDIFERID[OA]S?\b"),
    "DEMO-GRADE": re.compile(r"\bdemo[- ]grade\b", re.IGNORECASE),
    "NUNCA-EM-PRODUCAO": re.compile(r"\bNUNCA\s+usar\s+em\s+produ", re.IGNORECASE),
    "CONDICIONAL": re.compile(r"\bCONDICIONAL\b"),
    "STUB": re.compile(r"\bSTUBS?\b"),
}
# Marcador de linha DOCUMENTAL: o deferimento vive num documento (ADR/tecnica/
# spec) ou numa forma textual que não é um dos marcadores de código. Não é
# escotilha: uma linha DOCUMENTAL **não** satisfaz a cobertura de nenhum marcador
# de código (verificação 1), só é validada nas verificações 2/3/4.
MARKER_VOCAB = set(CODE_MARKERS) | {"DOCUMENTAL"}

ESTADO_VOCAB = {
    "ABERTO",           # o deferimento está em vigor e o eixo não foi entregue
    "MITIGADO",         # o mecanismo existe e é fail-closed; falta wiring/provisionamento
    "FECHADO-RESIDUAL",  # a fronteira já fechou noutro caminho; o marcador é contraste
}

RE_AOS = re.compile(r"AOS-\d{3}")
RE_EPIC = re.compile(r"EPIC-\d{2}(?:/\d{2})*")
# Palavra que denuncia um deferimento na verificação 6. AQUI as duas grafias
# entram em qualquer caixa — incluindo `diferid*` em minúsculas, que NÃO é
# marcador de código — porque a verificação 6 exige ADICIONALMENTE um `EPIC-NN`
# na mesma linha, e essa exigência elimina o ruído da palavra corrente: das 23
# ocorrências de `diferid*` em minúsculas, só 1 traz um `EPIC-NN` sem ticket.
RE_DEFER_WORD = re.compile(
    r"\bD[EI]FERID[OA]S?\b|\bd[ei]ferid[oa]s?\b|\bd[ei]feriment[oa]s?\b"
    r"|\bd[íi]vida\b|\bdivida\b")
RE_ROW = re.compile(r"^\|\s*(DEF-\d{3})\s*\|")
# Bloco das contagens declaradas (§3.1). Delimitado por comentários HTML para a
# leitura por máquina ser inequívoca e não colidir com as tabelas de prosa.
RE_COUNT_ROW = re.compile(r"^\|\s*([^|]+?)\s*\|\s*([A-Z-]+)\s*\|\s*(\d+)\s*\|\s*$")
COUNT_BEGIN = "<!-- CONTAGENS:INICIO -->"
COUNT_END = "<!-- CONTAGENS:FIM -->"
# Cabeçalho de nota do §6. Uma nota cobre TODOS os DEF-NNN que o seu CABEÇALHO
# nomeia — `### N-DEF-201 — cobre DEF-201, DEF-203, DEF-204` —, e não os que a
# prosa mencione. É deliberado: um deferimento sem eixo replicado por oito
# âncoras é UM ticket em falta, não oito, e escrever a mesma nota oito vezes
# convidava-as a divergir. Pôr a lista no cabeçalho (e não na prosa) mantém a
# leitura por máquina inequívoca e a por humano óbvia.
RE_NOTE = re.compile(r"^#{2,4}\s+N-DEF-\d{3}\b")
RE_DEF_ID = re.compile(r"DEF-\d{3}")


def norm(cell: str) -> str:
    return cell.strip().strip("*`").strip()


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def go_production_files():
    """Ficheiros .go de produção sob packages/ (sem _test.go, sem testdata/)."""
    out = []
    for root, dirs, files in os.walk(PACKAGES_DIR):
        dirs[:] = [d for d in dirs if d not in (".git", "testdata", "vendor")]
        for f in files:
            if not f.endswith(".go") or f.endswith("_test.go"):
                continue
            out.append(Path(root) / f)
    return sorted(out)


def rel(path: Path) -> str:
    return str(path.relative_to(REPO_ROOT)).replace("\\", "/")


def scan_code_markers():
    """{(ficheiro, MARCADOR): [linhas]} sobre packages/**/*.go de produção."""
    found = {}
    for path in go_production_files():
        r = rel(path)
        for i, line in enumerate(read(path).splitlines(), start=1):
            for marker, rx in CODE_MARKERS.items():
                if rx.search(line):
                    found.setdefault((r, marker), []).append(i)
    return found


def backlog_tickets():
    """Todos os AOS-NNN que existem no backlog (mesma extracção do ref-lint)."""
    tickets = set()
    for epic in sorted(SPECS_DIR.glob("EPIC-*.md")):
        text = read(epic)
        for m in re.finditer(r"^#{2,3} (AOS-\d{3})\s*[-–—]", text, re.MULTILINE):
            tickets.add(m.group(1))
        for line in text.splitlines():
            m = re.match(r"\|\s*\**\s*(AOS-\d{3})", line)
            if m:
                tickets.add(m.group(1))
    return tickets


def parse_registo():
    """Devolve (linhas, notas, erros_de_formato)."""
    if not REGISTO.exists():
        return [], set(), [f"registo ausente: {rel(REGISTO)}"]
    rows, errs = [], []
    text = read(REGISTO)
    notes = set()
    for line in text.splitlines():
        if RE_NOTE.match(line):
            notes.update(RE_DEF_ID.findall(line))
    for raw in text.splitlines():
        if not RE_ROW.match(raw):
            continue
        cells = [c.strip() for c in raw.split("|")]
        # `| a | b |` → ['', 'a', 'b', ''] ⇒ 8 colunas = 10 fatias
        if len(cells) != 10:
            errs.append(f"colunas={len(cells) - 2} (esperado 8): {raw[:70]}")
            continue
        rid, marcador, ancora, deferimento, eixo, dono, gatilho, estado = (
            norm(cells[1]), norm(cells[2]), norm(cells[3]), norm(cells[4]),
            norm(cells[5]), norm(cells[6]), norm(cells[7]), norm(cells[8]),
        )
        rows.append({
            "id": rid, "marcador": marcador, "ancora": ancora,
            "deferimento": deferimento, "eixo": eixo, "dono": dono,
            "gatilho": gatilho, "estado": estado, "raw": raw,
        })
    return rows, notes, errs


def parse_counts():
    """
    §3.1: {(ficheiro, MARCADOR): contagem declarada}.

    Fecha o buraco de granularidade da verificação 1: a chave de COBERTURA é
    (ficheiro, marcador), pelo que um deferimento NOVO num ficheiro já registado
    passaria em silêncio — e é esse o caminho mais comum de acumulação de dívida.
    A contagem declarada torna cada ocorrência contável: acrescentar uma faz o
    gate ficar vermelho até o número ser actualizado *conscientemente*.
    """
    counts, errs = {}, []
    if not REGISTO.exists():
        return counts, errs
    dentro = False
    for raw in read(REGISTO).splitlines():
        if raw.strip() == COUNT_BEGIN:
            dentro = True
            continue
        if raw.strip() == COUNT_END:
            dentro = False
            continue
        if not dentro or not raw.startswith("|"):
            continue
        m = RE_COUNT_ROW.match(raw)
        if not m:
            if not re.match(r"^\|\s*[-: ]+\|", raw) and "Âncora" not in raw:
                errs.append(f"linha de contagem mal formada: {raw[:70]}")
            continue
        key = (norm(m.group(1)), norm(m.group(2)))
        if key in counts:
            errs.append(f"contagem repetida para {key[0]} [{key[1]}]")
        counts[key] = int(m.group(3))
    return counts, errs


def parse_baseline():
    """{chave: comentário} da baseline da verificação 6."""
    entries = {}
    if not BASELINE.exists():
        return entries
    for raw in read(BASELINE).splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, _, comment = line.partition("#")
        entries[key.strip()] = comment.strip()
    return entries


def scan_phantom_axes():
    """
    Verificação 6: linhas que deferem para um EPIC sem nomear ticket.
    Chave: `<ficheiro>|<EPICs da linha>` — estável face a drift de linhas.
    """
    hits = {}
    targets = [(p, rel(p)) for p in go_production_files()]
    for base in CORPUS_MD:
        if not base.exists():
            continue
        for p in sorted(base.rglob("*.md")):
            targets.append((p, rel(p)))
    for path, r in targets:
        for i, line in enumerate(read(path).splitlines(), start=1):
            if not RE_DEFER_WORD.search(line):
                continue
            epics = RE_EPIC.findall(line)
            if not epics or RE_AOS.search(line):
                continue
            key = f"{r}|{'+'.join(sorted(set(epics)))}"
            hits.setdefault(key, []).append(i)
    return hits


def main() -> int:
    exit_code = 0
    tickets = backlog_tickets()
    code = scan_code_markers()
    rows, notes, fmt_errs = parse_registo()

    print(f"registo: {len(rows)} linha(s) · notas: {len(notes)} · "
          f"backlog: {len(tickets)} ticket(s)")
    print(f"código: {len(code)} par(es) (ficheiro, marcador), "
          f"{sum(len(v) for v in code.values())} ocorrência(s)")

    if fmt_errs:
        print(f"ERRO: {len(fmt_errs)} linha(s) mal formada(s) no registo:")
        for e in fmt_errs:
            print(f"  - {e}")
        exit_code = 1

    # --- 4. vocabulário + campos obrigatórios --------------------------------
    vocab_errs = []
    for row in rows:
        if row["marcador"] not in MARKER_VOCAB:
            vocab_errs.append(f"{row['id']}: Marcador '{row['marcador']}' fora do vocabulário")
        if row["estado"] not in ESTADO_VOCAB:
            vocab_errs.append(f"{row['id']}: Estado '{row['estado']}' fora do vocabulário")
        for campo in ("dono", "gatilho", "deferimento"):
            if row[campo] in ("", "-", "—", "n/a"):
                vocab_errs.append(f"{row['id']}: coluna '{campo}' vazia")
    ids = [r["id"] for r in rows]
    for dup in sorted({i for i in ids if ids.count(i) > 1}):
        vocab_errs.append(f"{dup}: ID repetido")
    if vocab_errs:
        print(f"ERRO: {len(vocab_errs)} violação(ões) de vocabulário/campos no registo:")
        for e in vocab_errs:
            print(f"  - {e}")
        exit_code = 1

    # --- 2/3. eixo verificável + POR ATRIBUIR com nota ------------------------
    eixo_errs, por_atribuir = [], []
    for row in rows:
        eixo = row["eixo"]
        cited = set(RE_AOS.findall(eixo))
        inexistentes = sorted(c for c in cited if c not in tickets)
        aberto = "POR ATRIBUIR" in eixo
        if inexistentes:
            eixo_errs.append(
                f"{row['id']}: eixo cita ticket inexistente no backlog: {', '.join(inexistentes)}")
        if not cited and not aberto:
            eixo_errs.append(
                f"{row['id']}: eixo '{eixo}' não cita nenhum AOS-NNN nem declara 'POR ATRIBUIR'"
                + (" (citar só um EPIC é exactamente o defeito DEF-01)" if RE_EPIC.search(eixo) else ""))
        if aberto:
            por_atribuir.append(row)
            if row["id"] not in notes:
                eixo_errs.append(
                    f"{row['id']}: eixo 'POR ATRIBUIR' sem nota no §6 — nenhum cabeçalho "
                    f"'### N-DEF-NNN — cobre …' nomeia {row['id']} "
                    "(o ticket em falta tem de ser descrito, não só declarado ausente)")
    if eixo_errs:
        print(f"ERRO: {len(eixo_errs)} eixo(s) inválido(s) no registo:")
        for e in eixo_errs:
            print(f"  - {e}")
        exit_code = 1

    # --- 1. cobertura --------------------------------------------------------
    covered = {(r["ancora"], r["marcador"]) for r in rows}
    sem_entrada = sorted(k for k in code if k not in covered)
    if sem_entrada:
        print(f"ERRO: {len(sem_entrada)} marcador(es) de código sem entrada no registo:")
        for f, m in sem_entrada:
            print(f"  - {f} [{m}] linhas {code[(f, m)]}")
        exit_code = 1

    # --- 1b. contagem declarada por par (granularidade) ----------------------
    counts, count_errs = parse_counts()
    for key in sorted(code):
        obs = len(code[key])
        if key not in counts:
            count_errs.append(
                f"{key[0]} [{key[1]}]: {obs} ocorrência(s) e NENHUMA contagem declarada "
                f"no §3.1 — acrescentar `| {key[0]} | {key[1]} | {obs} |`")
        elif obs > counts[key]:
            count_errs.append(
                f"{key[0]} [{key[1]}]: {obs} ocorrência(s) > {counts[key]} declarada(s) "
                f"no §3.1 — DÍVIDA NOVA nas linhas {code[key]}; registá-la e actualizar "
                f"a contagem")
        elif obs < counts[key]:
            count_errs.append(
                f"{key[0]} [{key[1]}]: {obs} ocorrência(s) < {counts[key]} declarada(s) "
                f"no §3.1 — a contagem só encolhe por edição consciente; actualizar para {obs}")
    for key in sorted(counts):
        if key not in code:
            count_errs.append(
                f"{key[0]} [{key[1]}]: contagem declarada no §3.1 mas o marcador já não "
                f"existe no ficheiro — remover a linha de contagem")
    if count_errs:
        print(f"ERRO: {len(count_errs)} divergência(s) de contagem (§3.1 do registo):")
        for e in count_errs:
            print(f"  - {e}")
        exit_code = 1

    # --- 5. o registo só encolhe ---------------------------------------------
    stale = []
    for row in rows:
        anchor = REPO_ROOT / row["ancora"]
        if not anchor.exists():
            stale.append(f"{row['id']}: ÓRFÃ — âncora inexistente na árvore: {row['ancora']}")
            continue
        if row["marcador"] in CODE_MARKERS and (row["ancora"], row["marcador"]) not in code:
            stale.append(
                f"{row['id']}: OBSOLETA — {row['ancora']} já não contém o marcador "
                f"{row['marcador']}; remover a linha")
        # Uma linha DOCUMENTAL ancorada num `.go` escapava ao teste de
        # obsolescência: `row['marcador'] in CODE_MARKERS` era falso e a linha
        # tornava-se permanente por inércia quando o texto do ficheiro fosse
        # corrigido. Se a âncora é código, o deferimento tem de continuar
        # VISÍVEL nela — por marcador ou pela palavra.
        elif row["marcador"] == "DOCUMENTAL" and row["ancora"].endswith(".go"):
            texto = read(anchor)
            visivel = RE_DEFER_WORD.search(texto) or any(
                rx.search(texto) for rx in CODE_MARKERS.values())
            if not visivel:
                stale.append(
                    f"{row['id']}: OBSOLETA — {row['ancora']} já não declara deferimento "
                    f"nenhum (linha DOCUMENTAL sobre código); remover a linha")
    if stale:
        print(f"ERRO: {len(stale)} linha(s) apodrecida(s) no registo:")
        for e in stale:
            print(f"  - {e}")
        exit_code = 1

    # --- 6. anti-eixo-fantasma ----------------------------------------------
    phantom = scan_phantom_axes()
    baseline = parse_baseline()
    sem_owner = sorted(k for k, c in baseline.items() if "owner=" not in c)
    if sem_owner:
        print(f"ERRO: {len(sem_owner)} entrada(s) da baseline sem 'owner=':")
        for k in sem_owner:
            print(f"  - {k}")
        exit_code = 1
    novas = sorted(k for k in phantom if k not in baseline)
    obsoletas = sorted(k for k in baseline if k not in phantom)
    if novas:
        print(f"ERRO: {len(novas)} deferimento(s) para EPIC sem ticket na linha "
              f"(fora da baseline):")
        for k in novas:
            print(f"  - {k}  linhas {phantom[k]}")
        exit_code = 1
    if obsoletas:
        print(f"ERRO: {len(obsoletas)} entrada(s) OBSOLETA(s) da baseline "
              f"(a violação já não ocorre — remover):")
        for k in obsoletas:
            print(f"  - {k}")
        exit_code = 1
    if baseline and not obsoletas:
        print(f"\nDÍVIDA RECONHECIDA (baseline {rel(BASELINE)}, {len(baseline)} entrada(s)):")
        for k in sorted(baseline):
            print(f"  - {k}  {baseline[k]}")

    # --- visibilidade --------------------------------------------------------
    if por_atribuir:
        print(f"\nDÍVIDA SEM EIXO ({len(por_atribuir)} linha(s) com 'POR ATRIBUIR' — "
              f"o ticket é descrito na nota, falta criá-lo no backlog):")
        for row in sorted(por_atribuir, key=lambda r: r["id"]):
            print(f"  - {row['id']} [{row['marcador']}] {row['ancora']} — dono: {row['dono']}")
    estados = {}
    for row in rows:
        estados[row["estado"]] = estados.get(row["estado"], 0) + 1
    print("\nestado do registo: " + " · ".join(
        f"{k}={estados[k]}" for k in sorted(estados)) or "vazio")

    if exit_code == 0:
        print("\nOK: todos os deferimentos declarados no código têm entrada no registo "
              "com eixo verificável.")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
