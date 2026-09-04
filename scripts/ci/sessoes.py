#!/usr/bin/env python3
"""
sessoes.py — O sítio partilhado onde as sessões concorrentes se lêem.

O DEFEITO QUE FECHA, e é o mesmo do resto deste directório. Quatro worktrees
deste repositório estiveram activos ao mesmo tempo, em ramos que não se tinham
fundido, a editar os mesmos ficheiros. O resultado, medido e não suposto:

  - `claude/lucid-payne-e9fc91` e `claude/kind-heyrovsky-955e59` fecharam o
    MESMO defeito do canon de ADRs por caminhos diferentes, um com
    `range(1, 24)` e outro com derivação da fonte;
  - `AOS-313` e `AOS-314` nomeiam tickets DIFERENTES em ramos diferentes;
  - `AOS-317` idem, aberto duas vezes no mesmo dia por duas sessões que nunca
    se viram;
  - uma base foi reescrita debaixo de um ramo que já assentava nela, e só se
    deu por isso ao tentar abrir o PR.

Nada disto é descuido de quem escreveu. É a mesma classe de defeito que a RTM
tinha: uma lista mantida em CÓPIAS que ninguém confronta — ali `ADR_RANGE` em
dois ficheiros, aqui o estado do trabalho em quatro worktrees.

PORQUE O ESTADO NÃO É VERSIONADO. Um ficheiro em `specs/` ou `docs/` seria a
resposta errada, e pela razão exacta que produziu o problema: cada ramo veria a
SUA cópia, e as sessões só se leriam depois do merge — que é precisamente o
momento em que a colisão já custou o trabalho todo. O estado vive no
**git-dir comum** (`git rev-parse --git-common-dir`), que os worktrees de um
repositório partilham fisicamente: escrito num, lido em todos, sem merge e sem
rede. A FERRAMENTA é versionada, porque é código que se revê; o ESTADO não é,
porque é observação do agora.

ESCRITA CONCORRENTE. Cada sessão escreve **só o seu próprio ficheiro**
(`<id>.json`), pelo que o caso comum não tem contenção nenhuma. Só a atribuição
de números — que é a única operação que decide algo global — toma um lock, e é
um lock que falha fechado em vez de esperar para sempre.

Uso:
    python3 scripts/ci/sessoes.py registar --tickets AOS-317,AOS-318 \\
                                           --ficheiros scripts/ci/rtm-regenerate.py
    python3 scripts/ci/sessoes.py ver
    python3 scripts/ci/sessoes.py reservar 2
    python3 scripts/ci/sessoes.py sair

`ver` sai != 0 quando encontra uma colisão, para poder ser um gate.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

# Idade a partir da qual uma sessão é dada como morta e deixa de contar para
# colisões. Uma sessão que não se actualiza há meio dia ou foi fechada sem
# `sair`, ou está parada — em qualquer dos casos, avisar sobre ela é ruído.
# Não é apagada: fica listada como «obsoleta», porque o rasto de quem tocou o
# quê continua a valer depois de a sessão morrer.
OBSOLETA_APOS_S = 12 * 3600


def _git(*args: str) -> str:
    return subprocess.run(
        ["git", *args], capture_output=True, text=True, check=False
    ).stdout.strip()


def _dir_estado() -> Path:
    """
    `<git-dir-comum>/aos-sessoes/`. É o único sítio deste repositório que os
    worktrees partilham sem passar por um merge — ver o docstring do módulo.
    """
    comum = _git("rev-parse", "--git-common-dir")
    if not comum:
        sys.stderr.write("ERRO: não estou dentro de um repositório git\n")
        sys.exit(1)
    d = Path(comum).resolve() / "aos-sessoes"
    d.mkdir(parents=True, exist_ok=True)
    return d


def _id_sessao() -> str:
    """
    Identidade da sessão = o worktree em que corre. É estável entre invocações
    (ao contrário de um PID) e é exactamente a granularidade que interessa:
    duas sessões no mesmo worktree pisavam-se de qualquer maneira.
    """
    topo = _git("rev-parse", "--show-toplevel")
    nome = Path(topo).name if topo else "desconhecido"
    return re.sub(r"[^A-Za-z0-9._-]", "_", nome)


def _agora() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _idade_s(reg: dict) -> float:
    try:
        t = datetime.fromisoformat(reg.get("actualizado", ""))
    except ValueError:
        return float("inf")
    return (datetime.now(timezone.utc) - t).total_seconds()


def _ler_todos(d: Path) -> list:
    regs = []
    for f in sorted(d.glob("*.json")):
        try:
            reg = json.loads(f.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue  # ficheiro a meio de uma escrita, ou lixo: ignora-se
        reg["_ficheiro"] = f
        reg["_obsoleta"] = _idade_s(reg) > OBSOLETA_APOS_S
        regs.append(reg)
    return regs


def _escrever(path: Path, dados: dict) -> None:
    """Escrita atómica: ficheiro temporário + replace, para nunca se ler meio JSON."""
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(dados, ensure_ascii=False, indent=2), encoding="utf-8")
    os.replace(tmp, path)


# --- numeração de tickets ----------------------------------------------------

def _max_no_corpus() -> int:
    """
    Maior `AOS-NNN` que existe em `specs/EPIC-*.md`. É a mesma fonte que o
    `rtm-regenerate.py` usa para `max_aos`: a numeração não pode ser derivada de
    dois sítios diferentes, ou volta a divergir.
    """
    topo = _git("rev-parse", "--show-toplevel")
    maior = 0
    for f in (Path(topo) / "specs").glob("EPIC-*.md"):
        for m in re.finditer(r"AOS-(\d{3})", f.read_text(encoding="utf-8")):
            maior = max(maior, int(m.group(1)))
    return maior


def _max_reservado(regs: list) -> int:
    maior = 0
    for reg in regs:
        for t in reg.get("tickets", []):
            m = re.fullmatch(r"AOS-(\d{3})", t)
            if m:
                maior = max(maior, int(m.group(1)))
    return maior


def _com_lock(d: Path, fn):
    """
    Lock por criação exclusiva de ficheiro — portável, sem dependências, e que
    FALHA em vez de esperar indefinidamente. Um lock preso é um bug visível; um
    lock que espera para sempre é uma sessão pendurada sem explicação.
    """
    lock = d / ".lock"
    for _ in range(50):
        try:
            fd = os.open(str(lock), os.O_CREAT | os.O_EXCL | os.O_WRONLY)
        except FileExistsError:
            # Lock órfão de uma sessão que morreu a meio: passados 30s, toma-se.
            try:
                if time.time() - lock.stat().st_mtime > 30:
                    lock.unlink()
                    continue
            except OSError:
                pass
            time.sleep(0.1)
            continue
        try:
            os.write(fd, str(os.getpid()).encode())
            os.close(fd)
            return fn()
        finally:
            try:
                lock.unlink()
            except OSError:
                pass
    sys.stderr.write(
        f"ERRO: não consegui o lock de {lock} em 5s. Se nenhuma outra sessão "
        f"está a reservar tickets, apaga o ficheiro à mão.\n"
    )
    sys.exit(1)


# --- comandos ----------------------------------------------------------------

def cmd_registar(args) -> int:
    d = _dir_estado()
    path = d / f"{_id_sessao()}.json"
    antigo = {}
    if path.exists():
        try:
            antigo = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            pass

    tickets = [t.strip().upper() for t in (args.tickets or "").split(",") if t.strip()]
    ficheiros = [f.strip() for f in (args.ficheiros or "").split(",") if f.strip()]
    if args.acrescentar:
        tickets = sorted(set(antigo.get("tickets", [])) | set(tickets))
        ficheiros = sorted(set(antigo.get("ficheiros", [])) | set(ficheiros))

    reg = {
        "sessao": _id_sessao(),
        "worktree": _git("rev-parse", "--show-toplevel"),
        "ramo": _git("rev-parse", "--abbrev-ref", "HEAD"),
        "head": _git("rev-parse", "--short", "HEAD"),
        "assunto": _git("log", "-1", "--format=%s"),
        "tarefa": args.tarefa or antigo.get("tarefa", ""),
        "tickets": tickets,
        "ficheiros": ficheiros,
        "criado": antigo.get("criado", _agora()),
        "actualizado": _agora(),
    }
    _escrever(path, reg)
    print(f"registada: {reg['sessao']} · {reg['ramo']} @ {reg['head']}")
    if tickets:
        print(f"  tickets:   {', '.join(tickets)}")
    if ficheiros:
        print(f"  ficheiros: {', '.join(ficheiros)}")
    return _relatar(d, silencioso_se_limpo=True)


def cmd_sair(_args) -> int:
    path = _dir_estado() / f"{_id_sessao()}.json"
    if path.exists():
        path.unlink()
        print(f"saiu: {_id_sessao()}")
    else:
        print("nada registado para esta sessão")
    return 0


def cmd_reservar(args) -> int:
    d = _dir_estado()

    def _faz():
        regs = _ler_todos(d)
        base = max(_max_no_corpus(), _max_reservado(regs))
        novos = [f"AOS-{n:03d}" for n in range(base + 1, base + 1 + args.quantos)]
        path = d / f"{_id_sessao()}.json"
        reg = {}
        if path.exists():
            try:
                reg = json.loads(path.read_text(encoding="utf-8"))
            except json.JSONDecodeError:
                pass
        reg.setdefault("sessao", _id_sessao())
        reg.setdefault("criado", _agora())
        reg["worktree"] = _git("rev-parse", "--show-toplevel")
        reg["ramo"] = _git("rev-parse", "--abbrev-ref", "HEAD")
        reg["head"] = _git("rev-parse", "--short", "HEAD")
        reg["tickets"] = sorted(set(reg.get("tickets", [])) | set(novos))
        reg["actualizado"] = _agora()
        _escrever(path, reg)
        return novos

    novos = _com_lock(d, _faz)
    print(" ".join(novos))
    return 0


def _colisoes(regs: list) -> list:
    """
    As três colisões que custaram trabalho hoje. Só entre sessões VIVAS: uma
    sessão obsoleta já não está a escrever, e avisar sobre ela seria ruído que
    ensina a ignorar o aviso.
    """
    vivas = [r for r in regs if not r["_obsoleta"]]
    out = []

    por_ticket = {}
    for r in vivas:
        for t in r.get("tickets", []):
            por_ticket.setdefault(t, []).append(r["sessao"])
    for t, ss in sorted(por_ticket.items()):
        if len(ss) > 1:
            out.append(f"TICKET  {t} reivindicado por {len(ss)}: {', '.join(sorted(ss))}")

    por_ficheiro = {}
    for r in vivas:
        for f in r.get("ficheiros", []):
            por_ficheiro.setdefault(f, []).append(r["sessao"])
    for f, ss in sorted(por_ficheiro.items()):
        if len(ss) > 1:
            out.append(f"FICHEIRO {f} tocado por {len(ss)}: {', '.join(sorted(ss))}")

    # Bases divergentes: dois HEADs em que nenhum é ancestral do outro. É o que
    # aconteceu quando `405e01c` foi reescrito para `0d62961` — descoberto tarde,
    # ao abrir o PR, com o trabalho já feito sobre a base morta.
    for i, a in enumerate(vivas):
        for b in vivas[i + 1:]:
            ha, hb = a.get("head"), b.get("head")
            if not ha or not hb or ha == hb:
                continue
            anc_ab = subprocess.run(["git", "merge-base", "--is-ancestor", ha, hb],
                                    capture_output=True).returncode == 0
            anc_ba = subprocess.run(["git", "merge-base", "--is-ancestor", hb, ha],
                                    capture_output=True).returncode == 0
            if not anc_ab and not anc_ba:
                out.append(
                    f"BASE    {a['sessao']} ({ha}) e {b['sessao']} ({hb}) divergiram — "
                    f"nenhum é ancestral do outro"
                )
    return out


def _relatar(d: Path, silencioso_se_limpo: bool = False) -> int:
    regs = _ler_todos(d)
    cols = _colisoes(regs)
    if not silencioso_se_limpo:
        if not regs:
            print("nenhuma sessão registada.")
        for r in regs:
            marca = "  (obsoleta)" if r["_obsoleta"] else ""
            print(f"\n· {r.get('sessao')}{marca}")
            print(f"    ramo:      {r.get('ramo')} @ {r.get('head')}")
            if r.get("tarefa"):
                print(f"    tarefa:    {r['tarefa']}")
            if r.get("tickets"):
                print(f"    tickets:   {', '.join(r['tickets'])}")
            if r.get("ficheiros"):
                print(f"    ficheiros: {', '.join(r['ficheiros'])}")
            print(f"    visto:     {r.get('actualizado')}")
    if cols:
        print("\nCOLISÕES:")
        for c in cols:
            print(f"  - {c}")
        return 1
    if not silencioso_se_limpo:
        print("\nsem colisões entre sessões vivas.")
    return 0


def cmd_ver(_args) -> int:
    return _relatar(_dir_estado())


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    sub = p.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("registar", help="declara o que esta sessão está a fazer")
    r.add_argument("--tarefa", help="uma linha sobre o que estás a fazer")
    r.add_argument("--tickets", help="AOS-NNN separados por vírgula")
    r.add_argument("--ficheiros", help="caminhos separados por vírgula")
    r.add_argument("--acrescentar", action="store_true",
                   help="junta aos já declarados, em vez de substituir")
    r.set_defaults(fn=cmd_registar)

    v = sub.add_parser("ver", help="mostra as outras sessões e as colisões")
    v.set_defaults(fn=cmd_ver)

    res = sub.add_parser("reservar", help="atribui os próximos AOS-NNN livres")
    res.add_argument("quantos", type=int)
    res.set_defaults(fn=cmd_reservar)

    s = sub.add_parser("sair", help="remove o registo desta sessão")
    s.set_defaults(fn=cmd_sair)

    args = p.parse_args()
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
