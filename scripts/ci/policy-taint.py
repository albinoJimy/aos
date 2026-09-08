#!/usr/bin/env python3
"""
policy-taint.py — Gate taint-lint da política assinada (AOS-376).

PORQUÊ ESTE GATE EXISTE
-----------------------
A política de referência do PDP (`packages/control-plane/pdp/policies/aos_authz.cedar`)
nega por omissão (Cedar é DEFAULT-DENY). Cada `permit` é uma porta aberta: quando
o seu corpo `when {}` NÃO exige `context.taint != "untrusted"`, a porta admite um
pedido cuja proveniência é DADO NÃO-CONFIÁVEL — exactamente o vector de prompt
injection que a separação control/data-plane existe para fechar. Este gate obriga
cada `permit` a carregar a cláusula de taint, ou a declarar a excepção numa baseline
com dono e justificação (a propriedade «a baseline só encolhe», igual ao event-catalog).

Hoje a árvore tem dois `permit`:
  - `allow_http_post` — TEM a cláusula (aos_authz.cedar §when);
  - `allow_fs_read`   — NÃO a tem; está na baseline (owner=AOS-363), porque corrigi-la
    obriga a RE-ASSINAR o bundle e a chave privada de assinatura está fora do repo
    (re-assinar rotacionaria o trust anchor). É a mesma razão pela qual AOS-363 a diferiu.

O QUE ESTE GATE **NÃO** VERIFICA / O SEU LIMITE (declarado por honestidade)
--------------------------------------------------------------------------
A barreira ESTRUTURAL de taint EXISTE e está ligada (AOS-363, JÁ MERGED):
`NewProductionHardenedTaint` (`packages/kernel/reference-monitor/production.go:226`)
ESTÁ costurada em `packages/integration/secured.go:413`, é ARMADA por
`AOS_PRIVILEGED_CAPS`, e é observável via `HasActiveTaintGate`
(`bootstrap.go:2440`) e pelo banner de postura do RM (`posture_banner.go:111`).
MAS é INERTE POR OMISSÃO: com `AOS_PRIVILEGED_CAPS` vazio, a costura escolhe
`NewProductionSecure` (gate PRESENTE-MAS-INERTE). Este gate taint-lint é
DEFESA-EM-PROFUNDIDADE para o caso inerte-por-omissão: enquanto o conjunto está
por definir, o ÚNICO enforcement de taint é a cláusula Cedar por-`permit`. Este
gate NÃO fecha o eixo de taint — a barreira estrutural é o TaintGate com um
`AOS_PRIVILEGED_CAPS` não-vazio. (NÃO escrever «production.go:226 não tem
chamadores»: é FALSO desde AOS-363.)

Uso:
    python3 scripts/ci/policy-taint.py
Saída fail-closed: exit != 0 quando há `permit` sem a cláusula fora da baseline,
quando zero permits são extraídos (parser partido), ou quando o ficheiro falta.
"""

import os
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
POLICY_REL = "packages/control-plane/pdp/policies/aos_authz.cedar"
# Sobreponível por env APENAS para o self-test (§X) apontar o gate a uma .cedar-fixture
# temporária (p.ex. com a cláusula retirada de allow_http_post) sem MUTAR o ficheiro
# assinado committado. O job de CI não a define.
POLICY = Path(os.environ.get("AOS_POLICY_TAINT_POLICY") or (REPO_ROOT / "packages" / "control-plane" / "pdp" / "policies" / "aos_authz.cedar"))
# Sobreponível por env APENAS para o self-test (§X) provar que o gate bloqueia,
# apontando-o a uma baseline vazia / a uma .cedar-fixture. O job de CI não a define.
BASELINE = Path(
    os.environ.get("AOS_POLICY_TAINT_BASELINE")
    or (Path(__file__).resolve().parent / "baseline" / "policy-taint.txt")
)

# A cláusula exigida, sem espaços — a comparação é whitespace-insensível para que
# `context.taint!="untrusted"` e `context.taint != "untrusted"` contem como iguais.
TAINT_CLAUSE_NOSPACE = 'context.taint!="untrusted"'


def strip_cedar_comments(text: str) -> str:
    """
    Remove comentários `//` de uma política Cedar preservando as posições (cada
    carácter removido vira espaço; as quebras de linha mantêm-se). Cedar só tem
    comentários de linha `//`. Respeita strings entre aspas para não cortar um
    `//` que viva dentro de um literal.
    """
    out = []
    i = 0
    n = len(text)
    while i < n:
        c = text[i]
        if c == '"':
            out.append(c)
            i += 1
            while i < n:
                ch = text[i]
                out.append(ch)
                if ch == "\\":
                    i += 1
                    if i < n:
                        out.append(text[i])
                        i += 1
                    continue
                i += 1
                if ch == '"':
                    break
                if ch == "\n":
                    break  # string não terminada: não arrasta o resto
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "/":
            while i < n and text[i] != "\n":
                out.append(" ")
                i += 1
            continue
        out.append(c)
        i += 1
    return "".join(out)


def _skip_to_top_level_semicolon(text: str, i: int) -> int:
    """A partir de `i`, avança até ao `;` de topo (depth 0), respeitando
    aninhamento `{}`/`()`/`[]` e strings. Devolve o índice do `;` (ou fim)."""
    depth = 0
    n = len(text)
    while i < n:
        c = text[i]
        if c == '"':
            i += 1
            while i < n:
                if text[i] == "\\":
                    i += 2
                    continue
                if text[i] == '"':
                    break
                i += 1
            i += 1
            continue
        if c in "{[(":
            depth += 1
        elif c in "}])":
            depth -= 1
        elif c == ";" and depth == 0:
            break
        i += 1
    return i


def parse_permits(text: str):
    """
    Devolve [(permit_id, corpo)] para CADA `permit` da política — COM ou SEM
    `@id("...")`. `corpo` é o texto entre `permit` e o `;` de topo (inclui
    `when`/`unless`). O `permit_id` vem do `@id("...")` imediatamente antes; um
    `permit` ANÓNIMO fica com id `None` e é tratado como VIOLAÇÃO a jusante (um
    permit não-rastreável não pode ser baselinado nem auditado — e ignorá-lo,
    como a 1ª versão fazia com `@id\\s*permit`, deixava passar green um permit sem
    a cláusula de taint, o defeito que este gate existe para fechar). `forbid` não
    é apanhado: o regex ancora em `permit\\s*\\(`, a forma de uma regra permit.
    """
    permits = []
    for m in re.finditer(r"\bpermit\s*\(", text):
        pre = text[: m.start()]
        idm = re.search(r'@id\(\s*"([^"]+)"\s*\)\s*$', pre)
        pid = idm.group(1) if idm else None
        i = m.end()
        end = _skip_to_top_level_semicolon(text, i)
        permits.append((pid, text[i:end]))
    return permits


def _when_body(body: str):
    """Extrai o conteúdo do PRIMEIRO bloco `when { ... }` de um corpo de permit,
    IGNORANDO qualquer `unless { ... }`. Devolve None se não houver `when`. É
    deliberado só olhar para o `when`: uma cláusula dentro de `unless {}` INVERTE
    o sentido (permite quando taint É untrusted), e a 1ª versão, que fazia
    substring sobre o corpo inteiro, era enganada por isso."""
    m = re.search(r"\bwhen\b", body)
    if not m:
        return None
    i = m.end()
    n = len(body)
    while i < n and body[i] != "{":
        i += 1
    if i >= n:
        return None
    start = i + 1
    depth = 0
    while i < n:
        c = body[i]
        if c == '"':
            i += 1
            while i < n and body[i] != '"':
                if body[i] == "\\":
                    i += 1
                i += 1
            i += 1
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return body[start:i]
        i += 1
    return None


def _top_level_and_conjuncts(s: str):
    """Parte `s` pelos `&&` de NÍVEL DE TOPO (parênteses/chaves balanceados,
    fora de strings). Uma cláusula que só apareça DISJUNTA (`|| ...`) ou NEGADA
    (`!(...)`) fica DENTRO de um conjunct e nunca é igual à cláusula exacta — pelo
    que exigir a cláusula como um conjunct de topo rejeita a inversão semântica."""
    parts = []
    cur = []
    depth = 0
    i = 0
    n = len(s)
    while i < n:
        c = s[i]
        if c == '"':
            cur.append(c)
            i += 1
            while i < n and s[i] != '"':
                if s[i] == "\\":
                    cur.append(s[i])
                    i += 1
                cur.append(s[i])
                i += 1
            if i < n:
                cur.append(s[i])
                i += 1
            continue
        if c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
        if depth == 0 and s[i : i + 2] == "&&":
            parts.append("".join(cur))
            cur = []
            i += 2
            continue
        cur.append(c)
        i += 1
    parts.append("".join(cur))
    return parts


def permit_cumpre_taint(body: str) -> bool:
    """True sse o permit exige `context.taint != "untrusted"` como um conjunct AND
    de topo do seu `when {}` — a única colocação que garante que o permit NÃO
    admite proveniência não-confiável."""
    wb = _when_body(body)
    if wb is None:
        return False
    alvo = re.sub(r"\s+", "", TAINT_CLAUSE_NOSPACE)
    for conj in _top_level_and_conjuncts(wb):
        if re.sub(r"\s+", "", conj) == alvo:
            return True
    return False


def load_baseline() -> dict:
    """Baseline com dono por entrada. Chave = permit-id (a linha antes do `#`)."""
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


def print_scope_block() -> None:
    """
    Bloco «declara o seu próprio limite» (AC9). Espelha o âmbito impresso pelo
    event-catalog: o gate diz o que NÃO fecha, para não se confundir com a
    barreira estrutural.
    """
    print(
        "  Âmbito (defesa-em-profundidade): a barreira ESTRUTURAL de taint EXISTE e está\n"
        "  ligada (AOS-363, merged) — NewProductionHardenedTaint (production.go:226) está\n"
        "  costurada em integration/secured.go:413, armada por AOS_PRIVILEGED_CAPS, observável\n"
        "  via HasActiveTaintGate (bootstrap.go:2440) e o banner de postura (posture_banner.go:111).\n"
        "  MAS é INERTE POR OMISSÃO (AOS_PRIVILEGED_CAPS vazio => NewProductionSecure). Este gate\n"
        "  é defesa-em-profundidade para o caso inerte-por-omissão: enquanto o conjunto está por\n"
        "  definir, o ÚNICO enforcement de taint é a cláusula Cedar por-permit. Este gate NÃO fecha\n"
        "  o eixo de taint - a barreira estrutural e o TaintGate com um AOS_PRIVILEGED_CAPS nao-vazio."
    )


def main() -> int:
    if not POLICY.exists():
        print(f"ERRO: política {POLICY_REL} ausente — fail-closed (sem política não há gate).")
        return 1

    raw = POLICY.read_text(encoding="utf-8")
    text = strip_cedar_comments(raw)
    permits = parse_permits(text)
    if not permits:
        print(
            f"ERRO: zero `permit` extraídos de {POLICY_REL} — parser ou convenção partida "
            f"(fail-closed: um parser partido não passa por «nada a verificar»)."
        )
        return 1

    bl = load_baseline()
    baseline = bl["entries"]
    exit_code = 0

    if bl["problems"]:
        print(f"ERRO: {len(bl['problems'])} entrada(s) de baseline sem `owner=`:")
        for n, key in bl["problems"]:
            print(f"  - {BASELINE.name}:{n}: {key}")
        exit_code = 1

    violations = []  # (permit_id, descrição)

    for pid, body in permits:
        if permit_cumpre_taint(body):
            continue  # cumpre — a cláusula é conjunct AND de topo do `when {}`
        if pid is None:
            # Permit ANÓNIMO sem a cláusula: não é rastreável nem baselinável (a
            # baseline indexa por @id). Recusa-se sempre — não se pode auditar o
            # que não tem nome, e ignorá-lo era o buraco MUST-1.
            violations.append(
                (
                    "<anonimo>",
                    f'{POLICY_REL}: um `permit` SEM @id("...") não exige '
                    f'`context.taint != "untrusted"` como conjunct de topo do `when {{}}` '
                    f"— um permit anónimo não é rastreável nem baselinável; toda a regra "
                    f"permit tem de ter @id E a cláusula (ou ser baselinada com dono)",
                )
            )
            continue
        if pid in baseline:
            baseline[pid]["seen"] = True
            continue
        violations.append(
            (
                pid,
                f'{POLICY_REL}: permit `{pid}` não exige `context.taint != "untrusted"` '
                f"como conjunct AND de topo do seu `when {{}}` — admite pedido de "
                f"proveniência não-confiável, ou coloca a cláusula em `unless`/negada/"
                f"disjunta (e não consta da baseline com dono)",
            )
        )

    # Baseline obsoleta/órfã: entradas que não foram visitadas (o permit cumpre
    # agora, foi removido, ou nunca existiu). A baseline só ENCOLHE.
    stale = [(k, v) for k, v in baseline.items() if not v["seen"]]
    if stale:
        print(f"ERRO: {len(stale)} entrada(s) de baseline OBSOLETA(s)/ÓRFÃ(s) — a excepção já não ocorre:")
        for key, meta in sorted(stale):
            print(f"  - {BASELINE.name}:{meta['line']}: {key} (remova a linha)")
        exit_code = 1

    tolerated = [(k, v) for k, v in baseline.items() if v["seen"]]
    if tolerated:
        print(
            f"\nDÍVIDA RECONHECIDA ({len(tolerated)}) — permits sem a cláusula de taint tolerados "
            f"pela baseline, com dono declarado (NÃO são verde; ver {BASELINE.name}):"
        )
        for key, meta in sorted(tolerated):
            print(f"  ~ {key} — {meta['comment']}")

    if violations:
        print(f"\nERRO: {len(violations)} `permit` sem a cláusula de taint fora da baseline:")
        for _, desc in sorted(violations, key=lambda v: v[1]):
            print(f"  - {desc}")
        exit_code = 1

    if exit_code == 0:
        compliant = sum(
            1 for _, b in permits if TAINT_CLAUSE_NOSPACE in re.sub(r"\s+", "", b)
        )
        print(
            f"Taint-lint da política OK: {len(permits)} permit(s) em {POLICY_REL}, "
            f'{compliant} com `context.taint != "untrusted"`, '
            f"{len(tolerated)} em dívida reconhecida (baseline com dono)."
        )
        print_scope_block()
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
