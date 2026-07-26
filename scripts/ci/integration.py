#!/usr/bin/env python3
"""
integration.py — Gate 4 «Integração»: conformidade dos contratos de porta C1–C5
(AOS-198, achado DAT-09).

PORQUÊ ESTE GATE EXISTE
-----------------------
`specs/01 §4` declara há muito um «gate 4 — Integração» BLOQUEANTE DE MERGE, e
`tecnica/12 §11` nomeia-o como a mitigação do risco «deriva silenciosa de schema
entre componentes». O gate nunca existiu em `scripts/ci/`. A auditoria mediu a
consequência: os dois contratos COM rastreio a `tecnica/12` no código (C1, C2)
estão fiéis; os TRÊS sem rastreio (C3, C4, C5) divergiram integralmente. A deriva
não foi acidental — foi a ausência do gate.

O QUE ESTE GATE VERIFICA (e só isto)
------------------------------------
1. Cada contrato C1–C5 declarado em `tecnica/12` tem uma linha na tabela de
   implementação (§3.1) que aponta para um pacote Go EXISTENTE na árvore.
   Mapeamento em falta ou caminho inexistente ⇒ FALHA (fail-closed: sem
   mapeamento não há verificação possível, e «não verificável» não é «verde»).
2. Cada secção de contrato tem um parágrafo «Semântica de erro» de onde se
   extrai pelo menos um código `E_*`. Uma secção de contrato de onde o parser
   não extrai NENHUM código é um ERRO — documento reformatado ou parser partido
   — e não «nada a verificar». Sem esta regra, renomear o marcador do parágrafo
   desliga o gate para esse contrato em silêncio (era o caso até AOS-198).
   O parágrafo é lido POR INTEIRO (até à linha em branco seguinte), não só na
   primeira linha, e o número de códigos extraídos por contrato é impresso em
   cada execução para que uma quebra de parse seja visível no log.
3. Cada código de erro de porta `E_*` documentado está presente, como STRING
   LITERAL, em pelo menos um ficheiro `.go` de produção (não-`_test.go`) sob o
   pacote mapeado para esse contrato. Comentários (`//` e `/* */`) são
   REMOVIDOS antes da procura: uma menção em comentário não é uma declaração e
   não pode fechar dívida de contrato. Ausente e fora da baseline ⇒ FALHA.
4. A baseline não apodrece, nas DUAS direcções:
   a) entrada cujo código JÁ está presente no código ⇒ OBSOLETA ⇒ FALHA;
   b) entrada que não corresponde a nenhum par contrato/código extraído de
      `tecnica/12` ⇒ ÓRFÃ ⇒ FALHA. Sem (b), uma entrada cujo contrato ou código
      desapareça do parse deixa de ser visitada e nunca mais é reportada — a
      regra «a baseline só encolhe» passaria a ser falsa por inércia.

O QUE ESTE GATE **NÃO** VERIFICA (declarado por honestidade — um gate que
promete mais do que faz seria o mesmo defeito noutro sítio)
-----------------------------------------------------------------------------
- NÃO compara a forma dos tipos Go campo-a-campo com os exemplos JSON de
  `tecnica/12` (nomes de campo, tipos, obrigatoriedade, aninhamento).
- NÃO verifica `port_version` nem compatibilidade SemVer de porta.
- NÃO verifica que o código de erro é DEVOLVIDO no caminho de execução certo —
  só que a literal existe fora de comentário. Uma constante declarada e nunca
  devolvida passa.
- NÃO verifica o sentido inverso: códigos `E_*` que existem no código e não
  estão documentados em `tecnica/12` não são reportados (os pacotes têm dezenas
  de códigos internos legítimos que não são códigos de porta).
- NÃO exercita nenhum contrato em runtime; é análise estática de texto.

É, portanto, o **mínimo verificável** do CA de AOS-198 — o suficiente para que a
divergência C3/C4/C5 deixe de ser invisível e para que uma NOVA divergência
bloqueie o merge. Alargá-lo à forma dos tipos é trabalho de outro ticket.

Uso:
    python3 scripts/ci/integration.py
Saída fail-closed: exit != 0 quando há divergências não-baselinadas.
"""

import os
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]

# Ambos os caminhos são sobreponíveis por env APENAS para o self-test
# (scripts/ci/selftest.sh §N) poder provar que o gate BLOQUEIA sem tocar nos
# ficheiros reais. Por omissão apontam para o corpus real, e o job de CI NÃO
# define nenhuma das variáveis — o gate de CI corre sempre contra tecnica/12 e a
# baseline committada.
CONTRACTS_DOC = Path(os.environ.get("AOS_CONTRACTS_DOC") or (REPO_ROOT / "tecnica" / "12_Contratos_de_Interface.md"))
BASELINE = Path(os.environ.get("AOS_CONTRACT_BASELINE") or (Path(__file__).resolve().parent / "baseline" / "contract-codes.txt"))

DOC_REL = "tecnica/12_Contratos_de_Interface.md"

# Cabeçalho de secção de contrato: "## 4. Contrato C1 — RM ↔ PDP (Autorização)"
RE_CONTRACT_HEADING = re.compile(r"^#{2,3}\s*\d+\.\s*Contrato\s+(C\d)\s*[-–—]\s*(.+?)\s*$")
# Linha da tabela de implementação de §3.1: "| C1 | RM↔PDP | `packages/...` |"
RE_IMPL_ROW = re.compile(r"^\|\s*`?(C\d)`?\s*\|(.+)\|\s*$")
RE_BACKTICKED_PATH = re.compile(r"`(packages/[A-Za-z0-9_./-]+)`")
RE_ERR_CODE = re.compile(r"`(E_[A-Z0-9_]+)`")
# Início do parágrafo que fixa os códigos de porta de um contrato. O parágrafo
# INTEIRO é lido a partir daqui (até à linha em branco seguinte).
RE_ERR_PARAGRAPH = re.compile(r"^\*\*Semântica de erro\.\*\*")


def read_lines(path: Path) -> list:
    return path.read_text(encoding="utf-8").splitlines()


def strip_go_comments(text: str) -> str:
    """
    Devolve o texto Go sem comentários `//` e `/* */`, preservando o número de
    linhas (cada carácter removido vira espaço, cada `\\n` mantém-se), para que
    a linha reportada continue a ser a linha real do ficheiro.

    Existe porque uma menção a um código de porta DENTRO de um comentário não é
    uma declaração: aceitá-la permitiria fechar dívida de contrato escrevendo um
    comentário e removendo a entrada da baseline.
    """
    out = []
    i = 0
    n = len(text)
    while i < n:
        c = text[i]
        if c == '"' or c == "'" or c == "`":
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


def parse_impl_map(lines: list) -> dict:
    """Lê a tabela §3.1 de `tecnica/12`: contrato -> caminho do pacote Go."""
    impl = {}
    for i, line in enumerate(lines, start=1):
        m = RE_IMPL_ROW.match(line)
        if not m:
            continue
        contract = m.group(1)
        paths = RE_BACKTICKED_PATH.findall(m.group(2))
        if not paths:
            continue
        # A última célula com um caminho `packages/...` é a implementação.
        impl[contract] = {"path": paths[-1], "line": i}
    return impl


def parse_contract_codes(lines: list) -> dict:
    """
    Percorre as secções de contrato e recolhe, por contrato, os códigos `E_*`
    declarados no parágrafo «Semântica de erro» — o parágrafo COMPLETO, até à
    linha em branco seguinte — com a linha do documento, para que a violação
    seja reportada como ficheiro:linha.
    """
    codes = {}
    current = None
    i = 0
    total = len(lines)
    while i < total:
        line = lines[i]
        h = RE_CONTRACT_HEADING.match(line)
        if h:
            current = h.group(1)
            codes.setdefault(current, {"codes": {}, "title": h.group(2), "line": i + 1})
            i += 1
            continue
        if line.startswith("## "):
            # Saiu da zona dos contratos (§9 em diante).
            current = None
            i += 1
            continue
        if current and RE_ERR_PARAGRAPH.match(line):
            j = i
            while j < total and lines[j].strip():
                for code in RE_ERR_CODE.findall(lines[j]):
                    codes[current]["codes"].setdefault(code, j + 1)
                j += 1
            i = j
            continue
        i += 1
    return codes


def load_baseline() -> dict:
    """
    Baseline com DONO e JUSTIFICAÇÃO por entrada (padrão de baseline/govulncheck.txt).
    Formato: `C3|E_NO_DECISION|packages/platform/broker # owner=...; justificação`
    A chave de comparação é `contrato|código`; o caminho é informativo.
    """
    entries = {}
    malformed = []
    ownerless = []
    if not BASELINE.exists():
        return {"entries": entries, "malformed": malformed, "ownerless": ownerless}
    for n, raw in enumerate(BASELINE.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        data, _, comment = line.partition("#")
        parts = [p.strip() for p in data.split("|")]
        if len(parts) < 2 or not parts[0] or not parts[1]:
            print(f"ERRO: baseline malformada em {BASELINE.name}:{n}: {raw!r}")
            malformed.append(n)
            continue
        comment = comment.strip()
        key = f"{parts[0]}|{parts[1]}"
        if "owner=" not in comment:
            ownerless.append((n, key))
        entries[key] = {"line": n, "comment": comment, "seen": False}
    return {"entries": entries, "malformed": malformed, "ownerless": ownerless}


def go_sources(pkg_dir: Path):
    """Ficheiros .go de PRODUÇÃO (exclui _test.go) sob o pacote."""
    for path in sorted(pkg_dir.rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        yield path


def find_code(pkg_dir: Path, code: str):
    """
    Devolve (caminho_relativo, linha) da primeira ocorrência da literal FORA de
    comentário, ou None.
    """
    needle = f'"{code}"'
    for path in go_sources(pkg_dir):
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        if needle not in text:
            continue
        code_only = strip_go_comments(text)
        if needle not in code_only:
            continue
        for n, line in enumerate(code_only.splitlines(), start=1):
            if needle in line:
                return (path.relative_to(REPO_ROOT).as_posix(), n)
    return None


def main() -> int:
    if not CONTRACTS_DOC.exists():
        print(f"ERRO: documento de contratos ausente: {DOC_REL}")
        return 1

    lines = read_lines(CONTRACTS_DOC)
    impl = parse_impl_map(lines)
    contracts = parse_contract_codes(lines)
    bl = load_baseline()
    baseline = bl["entries"]

    exit_code = 0

    if bl["malformed"]:
        exit_code = 1
    if bl["ownerless"]:
        print(f"ERRO: {len(bl['ownerless'])} entrada(s) de baseline sem `owner=` (dono obrigatório):")
        for n, key in bl["ownerless"]:
            print(f"  - {BASELINE.name}:{n}: {key}")
        exit_code = 1

    if not contracts:
        print(f"ERRO: nenhum contrato C* encontrado em {DOC_REL} (parser ou documento partido).")
        return 1

    # 0. Fail-closed de parse: uma secção de contrato SEM códigos extraídos é um
    #    documento reformatado (ou um parser partido), não «nada a verificar».
    silent = [c for c in sorted(contracts) if not contracts[c]["codes"]]
    if silent:
        print(
            f"ERRO: {len(silent)} secção(ões) de contrato sem NENHUM código `E_*` extraído do "
            f"parágrafo «Semântica de erro» — documento reformatado ou parser partido:"
        )
        for contract in silent:
            print(
                f"  - {contract} ({DOC_REL}:{contracts[contract]['line']}): o parágrafo tem de "
                f"começar por `**Semântica de erro.**` e citar os códigos entre crases."
            )
        exit_code = 1

    # Universo de pares contrato|código realmente documentados — base da detecção
    # de entradas de baseline ÓRFÃS.
    documented = {f"{c}|{code}" for c in contracts for code in contracts[c]["codes"]}

    print("Códigos de porta extraídos de " + DOC_REL + " (por contrato):")
    for contract in sorted(contracts):
        got = sorted(contracts[contract]["codes"])
        print(f"  {contract}: {len(got)} — {', '.join(got) if got else '(NENHUM)'}")

    # 1. Integridade do mapeamento contrato -> pacote Go.
    missing_map = []
    bad_path = []
    for contract in sorted(contracts):
        if not contracts[contract]["codes"]:
            continue  # já reportado como secção silenciosa (exit_code == 1)
        row = impl.get(contract)
        if row is None:
            missing_map.append(contract)
            continue
        pkg_dir = REPO_ROOT / row["path"]
        if not pkg_dir.is_dir():
            bad_path.append((contract, row["path"], row["line"]))

    if missing_map:
        print(
            f"ERRO: {len(missing_map)} contrato(s) com códigos de porta documentados e SEM linha "
            f"na tabela de implementação de {DOC_REL} §3.1:"
        )
        for contract in missing_map:
            print(f"  - {contract} ({DOC_REL}:{contracts[contract]['line']})")
        exit_code = 1

    if bad_path:
        print(f"ERRO: {len(bad_path)} mapeamento(s) apontam para um pacote inexistente:")
        for contract, path, line in bad_path:
            print(f"  - {contract} -> {path} ({DOC_REL}:{line})")
        exit_code = 1

    # 2. Presença dos códigos de erro documentados.
    violations = []   # (contrato, código, pacote, linha_doc)
    baselined = []    # (contrato, código, pacote, comentário)
    obsolete = []     # (chave, ficheiro, linha, linha_baseline)
    verified = 0
    for contract in sorted(contracts):
        row = impl.get(contract)
        if row is None or not (REPO_ROOT / row["path"]).is_dir():
            continue
        pkg_dir = REPO_ROOT / row["path"]
        for code, doc_line in sorted(contracts[contract]["codes"].items()):
            key = f"{contract}|{code}"
            hit = find_code(pkg_dir, code)
            if key in baseline:
                baseline[key]["seen"] = True
            if hit is not None:
                verified += 1
                if key in baseline:
                    obsolete.append((key, hit[0], hit[1], baseline[key]["line"]))
                continue
            if key in baseline:
                baselined.append((contract, code, row["path"], baseline[key]["comment"]))
                continue
            violations.append((contract, code, row["path"], doc_line))

    if obsolete:
        print(f"ERRO: {len(obsolete)} entrada(s) de baseline OBSOLETA(s) — o código já está presente:")
        for key, rel, n, bl_line in obsolete:
            print(f"  - {key} já está presente em {rel}:{n}. Remova a linha {BASELINE.name}:{bl_line}.")
        exit_code = 1

    # 3. Entradas de baseline ÓRFÃS: nunca avaliadas porque o par contrato|código
    #    não existe em `tecnica/12`. Sem esta verificação, renomear o parágrafo de
    #    um contrato (ou inventar uma entrada) esconde a dívida em silêncio.
    orphan = [
        (key, meta) for key, meta in baseline.items()
        if key not in documented
    ]
    if orphan:
        print(
            f"ERRO: {len(orphan)} entrada(s) de baseline ÓRFÃ(s) — o par contrato|código não existe "
            f"em {DOC_REL} (contrato removido, código renomeado, ou entrada inventada):"
        )
        for key, meta in sorted(orphan):
            print(f"  - {BASELINE.name}:{meta['line']}: {key} (remova a linha ou corrija o documento)")
        exit_code = 1

    # 4. Rede de segurança: uma entrada documentada que, mesmo assim, nunca foi
    #    avaliada (mapeamento em falta / caminho inexistente) não pode passar por
    #    «tolerada» — a sua dívida deixou de estar a ser medida.
    unevaluated = [
        (key, meta) for key, meta in baseline.items()
        if key in documented and not meta["seen"]
    ]
    if unevaluated:
        print(
            f"ERRO: {len(unevaluated)} entrada(s) de baseline NÃO AVALIADA(s) — o contrato existe "
            f"mas não foi possível verificá-lo (mapeamento §3.1 em falta ou pacote inexistente):"
        )
        for key, meta in sorted(unevaluated):
            print(f"  - {BASELINE.name}:{meta['line']}: {key}")
        exit_code = 1

    if baselined:
        print(
            f"\nDÍVIDA RECONHECIDA ({len(baselined)}) — divergências de contrato toleradas pela "
            f"baseline, com dono declarado (NÃO são verde; ver {BASELINE.name}):"
        )
        for contract, code, path, comment in baselined:
            print(f"  ~ {contract} {code} ausente em {path} — {comment}")

    if violations:
        print(f"\nERRO: {len(violations)} código(s) de porta documentado(s) e AUSENTE(s) do código:")
        for contract, code, path, doc_line in violations:
            print(f"  - {contract} {code}: documentado em {DOC_REL}:{doc_line}, ausente em {path}/**.go")
        print(
            "\n  Corrija o código (declarar o código de porta) OU corrija o contrato em "
            f"{DOC_REL} OU acrescente entrada com dono à baseline {BASELINE.name}."
        )
        exit_code = 1

    if exit_code == 0:
        print(
            f"\nGate 4 (Integração) OK: {len(impl)} contratos mapeados, "
            f"{len(documented)} códigos de porta documentados, "
            f"{verified} presente(s) no código, "
            f"{len(baselined)} em dívida reconhecida com dono."
        )
        print(
            "  Âmbito: presença dos códigos E_* documentados, fora de comentário. NÃO verifica "
            "forma dos tipos, port_version, caminho de retorno nem runtime (ver cabeçalho do script)."
        )

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
