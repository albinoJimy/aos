#!/usr/bin/env python3
"""
event-catalog.py — Gate do catálogo de tipos de evento (AOS-198, absorve o CA3
residual de AOS-201; pendência registada em `tecnica/13` §8.1).

PORQUÊ ESTE GATE EXISTE
-----------------------
`tecnica/13` §3.3 decidiu — e a decisão mantém-se — que **não há ficheiro central
de constantes**: cada tipo de evento é declarado ao lado do seu emissor, e a
fonte de verdade do catálogo é o CONJUNTO das constantes declaradas. Essa decisão
só se sustenta se a forma da declaração for verificável por máquina; enquanto foi
verificada «por comando manual», o documento acumulou 80 entradas de deriva.
Este gate é o comando manual de §3.3 transformado em gate bloqueante, mais as
três verificações que §3.3 declarava «todas manuais hoje».

O QUE ESTE GATE VERIFICA
------------------------
E1. **Catálogo reproduzível.** Extrai as constantes de tipo de evento da árvore
    com o MESMO critério de §3.3 (identificador contém `Event`, declaração numa
    linha, valor `familia.segmento[.segmento]`). Zero constantes ⇒ FALHA (um
    parser partido não pode passar por «nada a verificar»).
E2. **Prefixo conhecido.** A família (primeiro segmento) de cada constante tem de
    constar da taxonomia declarada nas tabelas (a) e (b) de `tecnica/13` §3.3.
    Uma família nova obriga a actualizar o documento — é isto que impede a
    reincidência da deriva doc↔código.
E3. **Zero literais em `eventstore.EventInput{Type: …}`.** O campo `Type` de um
    literal composto `EventInput` não pode ser uma string literal nem uma
    expressão com concatenação. Os campos do literal são separados pelas
    VÍRGULAS DE TOPO (respeitando aninhamento e strings), não por linhas: um
    literal de uma só linha com `Type` a NÃO ser o primeiro campo é apanhado
    exactamente como o multi-linha, e a expressão capturada nunca arrasta os
    campos seguintes. Grupos aninhados (elementos de um slice de `EventInput`)
    são percorridos recursivamente.
E4. **Zero literais/concatenações do catálogo no caminho de emissão.** Num
    ficheiro de produção que importa `substrate/eventstore` (i.e. que PODE
    apendar), uma string literal cujo valor é exactamente um tipo catalogado —
    fora da linha que o declara — é uma segunda fonte de verdade; e um literal
    que é PREFIXO de um tipo catalogado numa expressão com `+` é o padrão de
    composição que §3.3 manda rejeitar.

O QUE ESTE GATE **NÃO** VERIFICA (declarado por honestidade)
------------------------------------------------------------
- NÃO resolve o valor do `Type` quando é um parâmetro de função (`evType string`)
  ou uma variável local: 19 dos 44 emissores da árvore são helpers genéricos que
  recebem o tipo do chamador. E4 cobre-os indirectamente (o literal apareceria no
  chamador, dentro do mesmo ficheiro/pacote que importa o eventstore), mas um
  literal passado a partir de um pacote que NÃO importa `substrate/eventstore`
  escapa.
- **NÃO verifica a separação das famílias (a)/(b)** pela importação de
  `substrate/eventstore`, apesar de `tecnica/13` §8.1 a listar. Foi implementada,
  medida contra a árvore, e **retirada por imprecisão**: a granularidade
  disponível é o PACOTE, não o emissor. Na direcção (a) produzia 11 falsos
  positivos contra a *própria* tabela de donos de §3.3 (as constantes de
  `task.*`/`deadlock.*`/`run.created` vivem, por decisão, em
  `control-plane/orchestrator/contract`, que não importa o store — quem importa é
  o emissor); na direcção (b) acusava `platform/audit`, um pacote grande que
  importa o store por outras razões que não estes rótulos. Um gate com esse ruído
  seria desligado, e um gate desligado é pior do que gate nenhum. As duas
  observações ficam reportadas no ticket, não silenciadas numa baseline.
- NÃO verifica que o payload corresponde ao schema do tipo.
- NÃO verifica a contagem por família da tabela (a)/(b) de `tecnica/13` (as
  colunas «Nº»): são um retrato datado, não um invariante.
- NÃO valida o dono/caminho declarado na coluna «Componente dono» da taxonomia.

Uso:
    python3 scripts/ci/event-catalog.py
Saída fail-closed: exit != 0 quando há violações não-baselinadas.
"""

import os
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
EVENTS_DOC = REPO_ROOT / "tecnica" / "13_Modelo_Dados_Eventos.md"
PACKAGES = REPO_ROOT / "packages"
# Sobreponível por env APENAS para o self-test (§O) provar que o gate bloqueia,
# apontando-o a uma baseline vazia contra a árvore REAL. O job de CI não a define.
BASELINE = Path(os.environ.get("AOS_EVENT_BASELINE") or (Path(__file__).resolve().parent / "baseline" / "event-catalog.txt"))

DOC_REL = "tecnica/13_Modelo_Dados_Eventos.md"
EVENTSTORE_IMPORT = "substrate/eventstore"

# Delimitadores das tabelas de taxonomia de §3.3 (tabela (a) e tabela (b)).
TAXONOMY_START = "**(a) Tipos do envelope"
TAXONOMY_TABLE_B = "**(b) Rótulos de"
TAXONOMY_END = "#### Regras do catálogo"

# Critério de declaração de §3.3, transcrito do comando reproduzível do documento:
# identificador contendo `Event`, declaração numa linha, valor dotted minúsculo.
RE_CONST_DECL = re.compile(
    r'^[ \t]*(?:const )?[A-Za-z0-9_]*[Ee]vent[A-Za-z0-9_]*[ \t]*'
    r'(?:=|[A-Za-z][A-Za-z0-9_.]* =)[ \t]*'
    r'"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"[ \t]*$'
)
# Primeira célula de uma linha de tabela com um padrão de família: | `admission.*` | ...
RE_TAXONOMY_ROW = re.compile(r"^\|\s*`([a-z][a-z0-9_]*(?:\.[a-z0-9_*]+)+)`")
# Literal composto `EventInput{…}`. SEM espaço antes da chaveta, de propósito: com
# `\s*` a expressão também casa `func f() EventInput {`, e o corpo da FUNÇÃO passa
# a ser analisado como se fosse o corpo do literal (mediu-se: a mesma violação
# reportada duas vezes). O gofmt — que o gate de lint impõe — escreve sempre o
# literal colado (`EventInput{`) e a assinatura separada (`EventInput {`), pelo
# que a distinção é estável neste repositório.
RE_EVENTINPUT = re.compile(r"\bEventInput\{")
# Campo `Type` de um literal composto. Aplicado ao CAMPO já isolado pelas
# vírgulas de topo — nunca ancorado ao início de linha: a âncora `^` deixava
# passar `EventInput{StreamID: "s", Type: "x", Payload: nil}` (uma só linha, com
# `Type` a não ser o primeiro campo), que é precisamente a forma trivial de
# evasão, e nada em Go/gofmt obriga à forma multi-linha.
RE_TYPE_FIELD = re.compile(r"\s*Type\s*:\s*(.*)", re.S)
RE_STRING_LIT = re.compile(r'"([^"\\\n]*)"')
RE_WS = re.compile(r"\s+")


def go_production_files():
    for path in sorted(PACKAGES.rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        yield path


def strip_go_comments(text: str) -> str:
    """
    Remove comentários `//` e `/* */` de um ficheiro Go PRESERVANDO as posições
    (cada carácter removido vira espaço; as quebras de linha mantêm-se), para que
    a linha reportada continue a ser a linha real do ficheiro.

    Substitui a versão linha-a-linha anterior: um `EventInput{…}` comentado em
    bloco, ou um `Type:` dentro de um `/* … */` multi-linha, não é código e não
    pode fazer o gate ficar vermelho — nem, no sentido inverso, servir de sítio
    onde esconder um literal.
    """
    out = []
    i = 0
    n = len(text)
    while i < n:
        c = text[i]
        if c in ('"', "'", "`"):
            quote = c
            out.append(c)
            i += 1
            while i < n:
                ch = text[i]
                out.append(ch)
                if ch == "\\" and quote != "`":
                    i += 1
                    if i < n:
                        out.append(text[i])
                        i += 1
                    continue
                i += 1
                if ch == quote:
                    break
                if ch == "\n" and quote != "`":
                    break  # string não terminada: não arrasta o resto do ficheiro
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "/":
            while i < n and text[i] != "\n":
                out.append(" ")
                i += 1
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "*":
            out.append("  ")
            i += 2
            while i < n and not (text[i] == "*" and i + 1 < n and text[i + 1] == "/"):
                out.append("\n" if text[i] == "\n" else " ")
                i += 1
            out.append("  ")
            i += 2
            continue
        out.append(c)
        i += 1
    return "".join(out)


def parse_taxonomy() -> dict:
    """
    Lê as tabelas (a) e (b) de `tecnica/13` §3.3 e devolve
    {familia: 'a'|'b'} — a família é o primeiro segmento do padrão.
    """
    if not EVENTS_DOC.exists():
        return {}
    text = EVENTS_DOC.read_text(encoding="utf-8")
    try:
        i = text.index(TAXONOMY_START)
        b = text.index(TAXONOMY_TABLE_B, i)
        j = text.index(TAXONOMY_END, b)
    except ValueError:
        return {}
    families = {}
    for table, segment in (("a", text[i:b]), ("b", text[b:j])):
        for line in segment.splitlines():
            m = RE_TAXONOMY_ROW.match(line)
            if not m:
                continue
            families.setdefault(m.group(1).split(".")[0], table)
    return families


def load_baseline() -> dict:
    """Baseline com dono por entrada. Chave = a linha antes do `#`."""
    entries = {}
    problems = []
    if not BASELINE.exists():
        return {"entries": entries, "problems": problems}
    for n, raw in enumerate(BASELINE.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, _, comment = line.partition("#")
        key = key.strip()
        if not key:
            continue
        if "owner=" not in comment:
            problems.append((n, key))
        entries[key] = {"line": n, "comment": comment.strip(), "seen": False}
    return {"entries": entries, "problems": problems}


def collect_constants(files):
    """
    Devolve (valores, declaracoes) onde declaracoes é
    {(ficheiro_rel, linha): valor} e valores é {valor: [(ficheiro_rel, linha)]}.
    """
    values = {}
    decl_lines = set()
    for path in files:
        rel = path.relative_to(REPO_ROOT).as_posix()
        for n, line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
            m = RE_CONST_DECL.match(line)
            if not m:
                continue
            values.setdefault(m.group(1), []).append((rel, n))
            decl_lines.add((rel, n))
    return values, decl_lines


def split_top_level_fields(body: str):
    """
    Divide o corpo de um literal composto Go pelas vírgulas de TOPO, devolvendo
    [(offset_no_corpo, texto_do_campo)]. Respeita aninhamento `{}`/`[]`/`()` e
    literais de string/rune, para que uma vírgula dentro de `map[string]any{...}`
    ou dentro de `"a,b"` não parta um campo ao meio.
    """
    fields = []
    depth = 0
    start = 0
    i = 0
    n = len(body)
    while i < n:
        c = body[i]
        if c in ('"', "'", "`"):
            quote = c
            i += 1
            while i < n:
                if body[i] == "\\" and quote != "`":
                    i += 2
                    continue
                if body[i] == quote:
                    break
                i += 1
            i += 1
            continue
        if c in "{[(":
            depth += 1
        elif c in "}])":
            depth -= 1
        elif c == "," and depth == 0:
            fields.append((start, body[start:i]))
            start = i + 1
        i += 1
    fields.append((start, body[start:]))
    return fields


def type_field_expressions(text: str):
    """
    Devolve [(linha, expressao_do_campo_Type)] de cada `EventInput{…}`.

    A expressão é o campo `Type` isolado pelas vírgulas de topo — logo não
    depende de o campo estar no início de uma linha nem de ser o primeiro campo,
    e nunca inclui os campos seguintes. Um `EventInput` sem campo `Type`
    (construção posicional ou por atribuição posterior) devolve None.
    """
    out = []

    def walk(body: str, base: int, depth: int) -> None:
        for off, field in split_top_level_fields(body):
            m = RE_TYPE_FIELD.fullmatch(field)
            if m and m.group(1).strip():
                pos = base + off + m.start(1)
                out.append((text[:pos].count("\n") + 1, RE_WS.sub(" ", m.group(1).strip())))
                continue
            # Elemento aninhado de um slice/array de EventInput: `{ Type: …, … }`.
            # SÓ quando o campo É um grupo de chavetas (o elemento do slice), nunca
            # quando é um campo com valor composto (`Payload: map[string]any{…}`) —
            # descer aí produziria falsos positivos sobre dados, não sobre o tipo.
            if depth < 1:
                stripped = field.strip()
                if stripped.startswith("{") and stripped.endswith("}"):
                    s = field.index("{")
                    e = field.rindex("}")
                    walk(field[s + 1 : e], base + off + s + 1, depth + 1)

    for m in RE_EVENTINPUT.finditer(text):
        i = m.end() - 1
        depth = 0
        while i < len(text):
            if text[i] == "{":
                depth += 1
            elif text[i] == "}":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        before = len(out)
        walk(text[m.end() : i], m.end(), 0)
        if len(out) == before:
            out.append((text[: m.start()].count("\n") + 1, None))
    return out


def main() -> int:
    files = list(go_production_files())
    if not files:
        print("ERRO: nenhum ficheiro .go de produção encontrado sob packages/ — parser partido.")
        return 1

    taxonomy = parse_taxonomy()
    if not taxonomy:
        print(
            f"ERRO: taxonomia de famílias não extraível de {DOC_REL} §3.3 "
            f"(tabelas (a)/(b) ausentes ou renomeadas). Fail-closed: sem taxonomia não há gate."
        )
        return 1

    values, decl_lines = collect_constants(files)
    if not values:
        print("ERRO: zero constantes de tipo de evento encontradas — parser ou convenção partida.")
        return 1

    bl = load_baseline()
    baseline = bl["entries"]
    exit_code = 0

    if bl["problems"]:
        print(f"ERRO: {len(bl['problems'])} entrada(s) de baseline sem `owner=`:")
        for n, key in bl["problems"]:
            print(f"  - {BASELINE.name}:{n}: {key}")
        exit_code = 1

    violations = []   # (chave, descrição)

    def add(key, desc):
        if key in baseline:
            baseline[key]["seen"] = True
            return
        violations.append((key, desc))

    # E2 — prefixo/família conhecida.
    for value, sites in sorted(values.items()):
        family = value.split(".")[0]
        if family not in taxonomy:
            for rel, n in sites:
                add(
                    f"prefix|{value}|{rel}",
                    f"{rel}:{n}: família `{family}` do tipo `{value}` não consta da taxonomia de {DOC_REL} §3.3",
                )

    # E3 / E4 — literais e concatenações.
    for path in files:
        rel = path.relative_to(REPO_ROOT).as_posix()
        raw = path.read_text(encoding="utf-8", errors="replace")
        text = strip_go_comments(raw)

        # E3: campo Type de um literal EventInput.
        if "EventInput" in text:
            for line_no, expr in type_field_expressions(text):
                if expr is None:
                    continue
                lit = RE_STRING_LIT.search(expr)
                if lit and "+" not in expr:
                    # A chave inclui o VALOR (como já acontecia em E4): sem ele,
                    # uma única entrada de baseline mascararia todas as violações
                    # futuras da mesma regra no mesmo ficheiro, e a propriedade
                    # «a baseline só encolhe» deixaria de valer para esse ficheiro.
                    add(
                        f"literal-type|{rel}|{lit.group(1)}",
                        f"{rel}:{line_no}: `EventInput.Type` recebe uma string literal ({expr}) "
                        f"em vez de uma constante catalogada",
                    )
                elif "+" in expr:
                    add(
                        f"concat-type|{rel}|{expr}",
                        f"{rel}:{line_no}: `EventInput.Type` é composto por concatenação ({expr})",
                    )

        # E4: literais/concatenações do catálogo no caminho de emissão.
        if EVENTSTORE_IMPORT not in text:
            continue
        for n, code in enumerate(text.splitlines(), 1):
            if (rel, n) in decl_lines:
                continue
            if '"' not in code:
                continue
            has_plus = "+" in code
            for lit in RE_STRING_LIT.findall(code):
                if lit in values:
                    add(
                        f"literal-catalog|{rel}|{lit}",
                        f"{rel}:{n}: literal \"{lit}\" duplica um tipo de evento catalogado "
                        f"(declarado em {values[lit][0][0]}:{values[lit][0][1]}) num ficheiro que importa "
                        f"{EVENTSTORE_IMPORT}",
                    )
                elif (
                    has_plus
                    and lit
                    and (lit.endswith(".") or lit.endswith("_"))
                    and any(v.startswith(lit) for v in values)
                ):
                    add(
                        f"concat-catalog|{rel}|{lit}",
                        f"{rel}:{n}: nome de evento COMPOSTO por concatenação a partir de \"{lit}\" "
                        f"— um `type` composto não aparece em nenhum índice de constantes ({DOC_REL} §3.3)",
                    )

    # Baseline obsoleta: entradas que já não ocorrem.
    stale = [(k, v) for k, v in baseline.items() if not v["seen"]]
    if stale:
        print(f"ERRO: {len(stale)} entrada(s) de baseline OBSOLETA(s) — a violação já não ocorre:")
        for key, meta in sorted(stale):
            print(f"  - {BASELINE.name}:{meta['line']}: {key} (remova a linha)")
        exit_code = 1

    tolerated = [(k, v) for k, v in baseline.items() if v["seen"]]
    if tolerated:
        print(
            f"\nDÍVIDA RECONHECIDA ({len(tolerated)}) — violações do catálogo toleradas pela baseline, "
            f"com dono declarado (NÃO são verde; ver {BASELINE.name}):"
        )
        for key, meta in sorted(tolerated):
            print(f"  ~ {key} — {meta['comment']}")

    if violations:
        print(f"\nERRO: {len(violations)} violação(ões) do catálogo de tipos de evento:")
        for _, desc in sorted(violations, key=lambda v: v[1]):
            print(f"  - {desc}")
        exit_code = 1

    if exit_code == 0:
        n_a = sum(1 for f, t in taxonomy.items() if t == "a")
        print(
            f"Catálogo de eventos OK: {len(values)} tipos declarados como constante junto do emissor, "
            f"{len(taxonomy)} famílias na taxonomia ({n_a} do envelope), "
            f"zero literais/concatenações não-baselinadas no caminho de emissão, "
            f"{len(tolerated)} em dívida reconhecida."
        )
        print(
            "  Âmbito: não resolve `Type` quando é parâmetro/variável (helpers genéricos), "
            "nem valida payload/schema (ver cabeçalho do script)."
        )
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
