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

# A EXCEPÇÃO DA ESCRITA LOCAL DO RUN (AOS-549; emenda de 2026-10-22 ao ADR-034, decisão do dono).
# Gravar no armazém de artefactos do PRÓPRIO run é permitido também com o contexto untrusted, e a
# regra Cedar dessa capability não pode ter a cláusula. Um permit SEM a cláusula passa só se:
#   - trouxer, na política ASSINADA, a anotação `@taint_exempt("local_run_write")` (visível a quem
#     assina, e não numa baseline); e
#   - a sua acção for EXACTAMENTE uma das capabilities desta lista FECHADA — a de escrita local do
#     run, que o registo do nó só aceita na tool de código que grava no armazém.
# Qualquer outro valor da anotação, ou a anotação noutra capability, é violação.
LOCAL_RUN_WRITE_CAPS = {"cap:artifact.write"}
TAINT_EXEMPT_LOCAL_RUN_WRITE = "local_run_write"

# AS DUAS FORMAS NOVAS DA POLÍTICA DE FLUXO DE DADOS (AOS-556; ADR-044 §2.6 e §2.8, decisão do dono
# de 2026-10-25 de PARTIR a `allow_fs_read` por classe de destino).
#
# Ambas valem SÓ se a regra constranger a classe de destino como conjunct de topo. Sem essa
# exigência, a anotação numa regra que não nomeia o destino isentava TODA a capability — incluindo
# a leitura com egress externo, que é precisamente o que a partição existe para separar. É o mesmo
# buraco que a revisão do AOS-549 fechou do lado da cabeça, pelo outro lado.
#
# (1) LEITURA LOCAL: `egress: none`, `mutation: none`, `reversibility: reversible`, os três
#     explícitos no registo de tools. Não sai nem muda nada, pelo que é permitida também com o
#     contexto untrusted (emenda de 2026-10-19 ao ADR-034). A regra NÃO tem a cláusula de taint, e
#     diz porquê na anotação.
LOCAL_READ_CAPS = {"cap:fs.read"}
TAINT_EXEMPT_LOCAL_READ = "local_read"

# (2) LEITURA COM EGRESS EXTERNO: o conteúdo sai para um terceiro que não muda nada no mundo (o
#     fornecedor de um modelo). É a REGRA DE FLUXO, e a única forma em que uma DISJUNÇÃO é aceita
#     neste gate: permitida com contexto limpo, OU com contexto untrusted quando a proveniência é
#     o handle que a própria call consome. Num run com media o piso já é untrusted e a proveniência
#     é `context`, pelo que os dois lados falham e a call é negada — é o controlo negativo da fase.
#
#     A forma é LITERAL. Não se aceita a disjunção numa cabeça livre, nem com `unless`, nem com
#     outro atributo, nem com os lados trocados por outra condição: o parser compara byte a byte
#     sem espaços. Uma regra de fluxo que precise de outra forma precisa de uma entrada nova aqui,
#     revista, e não de uma excepção mais larga.
EXTERNAL_READ_CAPS = {"cap:fs.read"}
FLOW_ANNOTATION = "flow_from_handle"
FLOW_DESTINATION = "external_read"
FLOW_CLAUSE_NOSPACE = '(context.taint!="untrusted"||context.provenance=="handle_argument")'


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


def _skip_to_top_level_semicolon(text: str, i: int, depth: int = 0) -> int:
    """A partir de `i`, avança até ao `;` de topo, respeitando aninhamento
    `{}`/`()`/`[]` e strings. Devolve o índice do `;` (ou fim).

    `depth` é a profundidade JÁ ABERTA em `i`, e o chamador tem de a declarar (AOS-556). O
    [parse_permits] entra DEPOIS do `(` da cabeça do permit, pelo que entra a 1: com `depth=0` o
    `)` da cabeça levava a contagem a -1, o `};` do `when` a -1 também, e o `;` do próprio permit
    NUNCA era visto como de topo — o corpo devolvido absorvia as regras SEGUINTES até calhar um
    `;` a zero.

    Era fail-closed e não fail-open (o `when` lido é sempre o PRIMEIRO, logo a conformidade era
    julgada na regra certa), mas vetava a excepção de uma regra por causa de um `||` de outra: a
    excepção da escrita local do run só funcionava por ser a ÚLTIMA regra do ficheiro. Apareceu
    quando o AOS-556 pôs uma regra com disjunção depois de outra com anotação."""
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
        # As ANOTAÇÕES imediatamente antes do permit (`@id`, e desde o AOS-549 `@taint_exempt`),
        # por qualquer ordem.
        anns = {}
        bloco = re.search(r'((?:@\w+\(\s*"[^"]*"\s*\)\s*)+)$', pre)
        if bloco:
            for nome, valor in re.findall(r'@(\w+)\(\s*"([^"]*)"\s*\)', bloco.group(1)):
                anns[nome] = valor
        pid = anns.get("id")
        i = m.end()
        end = _skip_to_top_level_semicolon(text, i, depth=1)  # `i` e DEPOIS do `(` da cabeca
        permits.append((pid, text[i:end], anns))
    return permits


def acao_do_permit(body: str):
    """A capability da CABEÇA do permit, ou None.

    Só a cabeça conta (o texto até ao primeiro `)` de topo), e só na forma exacta
    `principal…, action == Action::"<cap>", resource…`: um `action == …` no `when`/`unless`
    não fixa a acção do permit — uma cabeça `action` livre com
    `when { action == Action::"cap:artifact.write" || … }` deixaria passar outra capability
    (revisão do AOS-549). Um corpo com `unless` ou com `||` também não ganha a excepção: a regra
    da escrita local do run tem de ser um permit simples, que se lê de uma vez."""
    depth, fim = 0, None
    for i, ch in enumerate(body):
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            if depth == 0:
                fim = i
                break
            depth -= 1
    if fim is None:
        return None
    cabeca, resto = body[:fim], body[fim + 1 :]
    m = re.fullmatch(r'\s*principal\b[^,]*,\s*action\s*==\s*Action::"([^"]+)"\s*,\s*resource\b[^,]*', cabeca)
    if not m or re.search(r"\bunless\b", resto) or "||" in resto:
        return None
    return m.group(1)


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


def _conjunct_presente(wb: str, alvo: str) -> bool:
    """True sse `alvo` (comparado sem espaços) é um conjunct AND de TOPO de `wb`."""
    a = re.sub(r"\s+", "", alvo)
    return any(re.sub(r"\s+", "", c) == a for c in _top_level_and_conjuncts(wb))


def permit_constrange_destino(body: str, classe: str) -> bool:
    """True sse o permit exige `context.destination == "<classe>"` como conjunct AND de topo.

    É a amarra das duas formas novas (AOS-556): a anotação diz a INTENÇÃO, e esta cláusula é o que
    a torna verdadeira. Sem ela a anotação isentaria toda a capability, e a partição da
    `allow_fs_read` por destino não separava nada."""
    wb = _when_body(body)
    if wb is None:
        return False
    return _conjunct_presente(wb, 'context.destination=="%s"' % classe)


def acao_do_permit_de_fluxo(body: str):
    """A capability da CABEÇA de um permit de FLUXO, ou None.

    Como [acao_do_permit], com uma diferença e só uma: aceita a disjunção, e aceita-a SÓ na forma
    literal de [FLOW_CLAUSE_NOSPACE], como UM conjunct de topo, e com mais nenhum `||` em todo o
    corpo. `unless` continua a desqualificar — uma regra de fluxo tem de se ler de uma vez."""
    depth, fim = 0, None
    for i, ch in enumerate(body):
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            if depth == 0:
                fim = i
                break
            depth -= 1
    if fim is None:
        return None
    cabeca, resto = body[:fim], body[fim + 1 :]
    m = re.fullmatch(r'\s*principal\b[^,]*,\s*action\s*==\s*Action::"([^"]+)"\s*,\s*resource\b[^,]*', cabeca)
    if not m or re.search(r"\bunless\b", resto):
        return None
    wb = _when_body(body)
    if wb is None:
        return None
    conjuncts = _top_level_and_conjuncts(wb)
    alvo = re.sub(r"\s+", "", FLOW_CLAUSE_NOSPACE)
    de_fluxo = [c for c in conjuncts if re.sub(r"\s+", "", c) == alvo]
    if len(de_fluxo) != 1:
        return None
    # Nenhum OUTRO `||` em todo o corpo: a única disjunção do permit é a da forma de fluxo.
    if resto.count("||") != 1:
        return None
    return m.group(1)


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

    for pid, body, anns in permits:
        if permit_cumpre_taint(body):
            continue  # cumpre — a cláusula é conjunct AND de topo do `when {}`
        if FLOW_ANNOTATION in anns:
            acao = acao_do_permit_de_fluxo(body)
            if (
                anns[FLOW_ANNOTATION] == FLOW_DESTINATION
                and acao in EXTERNAL_READ_CAPS
                and pid is not None
                and permit_constrange_destino(body, FLOW_DESTINATION)
            ):
                continue  # a regra de FLUXO (AOS-556), anotada na política assinada
            violations.append(
                (
                    pid or "<anonimo>",
                    f'{POLICY_REL}: @{FLOW_ANNOTATION}("{anns[FLOW_ANNOTATION]}") em `{acao}` — a regra de fluxo '
                    f'só vale como @{FLOW_ANNOTATION}("{FLOW_DESTINATION}"), com @id, numa das capabilities de '
                    f"leitura com egress externo ({', '.join(sorted(EXTERNAL_READ_CAPS))}), com "
                    f'`context.destination == "{FLOW_DESTINATION}"` como conjunct de topo e com a disjunção '
                    f"EXACTAMENTE na forma `{FLOW_CLAUSE_NOSPACE}` — AOS-556, ADR-044 §2.8",
                )
            )
            continue
        if "taint_exempt" in anns:
            acao = acao_do_permit(body)
            valor = anns["taint_exempt"]
            if pid is not None and (
                (valor == TAINT_EXEMPT_LOCAL_RUN_WRITE and acao in LOCAL_RUN_WRITE_CAPS)
                or (
                    valor == TAINT_EXEMPT_LOCAL_READ
                    and acao in LOCAL_READ_CAPS
                    and permit_constrange_destino(body, TAINT_EXEMPT_LOCAL_READ)
                )
            ):
                continue  # escrita local do run (AOS-549) ou leitura local (AOS-556), na política assinada
            violations.append(
                (
                    pid or "<anonimo>",
                    f'{POLICY_REL}: @taint_exempt("{valor}") em `{acao}` — a excepção só vale como '
                    f'@taint_exempt("{TAINT_EXEMPT_LOCAL_RUN_WRITE}") numa das capabilities de escrita local do '
                    f"run ({', '.join(sorted(LOCAL_RUN_WRITE_CAPS))}), ou como "
                    f'@taint_exempt("{TAINT_EXEMPT_LOCAL_READ}") numa das de leitura local '
                    f"({', '.join(sorted(LOCAL_READ_CAPS))}) e com "
                    f'`context.destination == "{TAINT_EXEMPT_LOCAL_READ}"` como conjunct de topo — '
                    f"AOS-549, AOS-556",
                )
            )
            continue
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
            1 for _, b, _ in permits if TAINT_CLAUSE_NOSPACE in re.sub(r"\s+", "", b)
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
