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

O LEDGER DA FÁBRICA vive ao lado, no mesmo directório e pela mesma razão. A
skill `software-factory` declara seis indicadores; três deles — calibração,
taxa de retrabalho e escapes por portão — não se leem de nenhum artefacto que o
repositório já produza, e estavam a depender de alguém se lembrar deles. Os
subcomandos `medir` e `indicadores` dão-lhes um sítio onde acumulam.

Uso:
    python scripts/ci/sessoes.py registar --tickets AOS-318,AOS-319 \\
                                          --ficheiros scripts/ci/rtm-regenerate.py
    python scripts/ci/sessoes.py ver
    python scripts/ci/sessoes.py reservar 2
    python scripts/ci/sessoes.py sair
    python scripts/ci/sessoes.py medir achado --veredicto confirmado --onde revisao
    python scripts/ci/sessoes.py medir entrega --pr 566
    python scripts/ci/sessoes.py indicadores --desde 30

`ver` sai != 0 quando encontra uma colisão, para poder ser um gate. O `medir` e
o `indicadores` NÃO são gates e não entram no `run.sh`, pela mesma razão que o
`ver` não entra: no runner não haveria nada com que colidir nem ledger que ler,
e um gate que passa sempre é pior do que verificação nenhuma.

É `python`, não `python3`: em Windows o `python3` do PATH é o atalho da
Microsoft Store. Os gates provisionam um shim em `.tools/`, que só serve dentro
do PATH deles.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

# Idade a partir da qual uma sessão é dada como morta e deixa de contar para
# colisões. Uma sessão que não se actualiza há meio dia ou foi fechada sem
# `sair`, ou está parada — em qualquer dos casos, avisar sobre ela é ruído.
# Não é apagada: fica listada como «obsoleta», porque o rasto de quem tocou o
# quê continua a valer depois de a sessão morrer.
OBSOLETA_APOS_S = 12 * 3600


def _git(*args: str) -> str:
    """
    Corre um comando git e devolve o stdout, SEMPRE decodificado como UTF-8.

    O `encoding` é explícito de propósito. Sem ele o `text=True` decodifica pela
    codepage da consola — cp1252 no Windows —, e o git emite UTF-8: o assunto de
    um commit em PT-PT com `Í`, `Á` ou `Ó` (`c3 8d`, `c3 81`, `c3 93`) traz um
    segundo byte que o cp1252 NÃO define, e o `registar` rebentava com
    `UnicodeDecodeError` antes de escrever o que quer que fosse (AOS-571). O
    `errors="replace"` fecha o resto: este stdout é texto para mostrar e para
    comparar, nunca autoridade, pelo que um byte indecifrável vale mais como `?`
    do que como excepção que mata a ferramenta que a disciplina deste repo manda
    correr ANTES de tocar em código.
    """
    return subprocess.run(
        ["git", *args], capture_output=True, text=True, check=False,
        encoding="utf-8", errors="replace",
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


# --- ledger da fábrica -------------------------------------------------------

# O QUE ELE É, E O QUE NÃO É. Não é um gate: continua a depender de alguém o
# chamar. O que muda é que o número passa a ACUMULAR e a ser LEGÍVEL — uma
# entrada em falta vê-se como lacuna ao ler o ledger contra os PRs do dia,
# enquanto uma contagem na cabeça de quem mediu evapora-se no fim da sessão. É
# menos do que um instrumento automático e mais do que prosa, e a skill
# `software-factory` declara-o nesses termos em vez de o apresentar como medição.
#
# DOIS TIPOS DE REGISTO, E OS TRÊS INDICADORES DERIVAM DELES. Não se guarda um
# contador por indicador: três contadores mantidos à mão discordam entre si, que
# é exactamente a classe de defeito que este directório existe para fechar.
#
#   achado   um achado de revisão, com o veredicto do vocabulário da skill
#            (confirmado | plausivel | morto) e ONDE apareceu — em revisão, ou
#            depois de alguém já ter declarado o trabalho feito
#   entrega  «declarei isto feito»; é o denominador do retrabalho, e sem ele a
#            taxa não existe
LEDGER = "fabrica.jsonl"
VEREDICTOS = ("confirmado", "plausivel", "morto")
ONDES = ("revisao", "pos-feito")


def _acrescentar_ledger(d: Path, reg: dict) -> None:
    """Append sob lock: duas sessões a medir ao mesmo tempo não interleavam linhas."""
    linha = json.dumps(reg, ensure_ascii=False, sort_keys=True) + "\n"

    def _fn():
        with (d / LEDGER).open("a", encoding="utf-8") as fh:
            fh.write(linha)
        return 0

    _com_lock(d, _fn)


def _ler_ledger(d: Path, desde_dias=None) -> list:
    path = d / LEDGER
    if not path.exists():
        return []
    corte = None
    if desde_dias:
        corte = datetime.now(timezone.utc) - timedelta(days=desde_dias)
    regs = []
    for linha in path.read_text(encoding="utf-8").splitlines():
        linha = linha.strip()
        if not linha:
            continue
        try:
            reg = json.loads(linha)
        except json.JSONDecodeError:
            continue  # linha truncada por uma escrita morta: ignora-se
        if corte is not None:
            try:
                if datetime.fromisoformat(reg.get("t", "")) < corte:
                    continue
            except ValueError:
                continue  # sem data legível não se pode situar na janela
        regs.append(reg)
    return regs


def _taxa(num: int, den: int) -> str:
    """
    Uma taxa sem denominador não é 0% nem 100%: é desconhecida. Imprimir «0%»
    sobre zero amostras é a mesma frase tranquilizadora que o `NÃO VERIFICADO`
    da skill existe para evitar — e aqui enganaria de forma simétrica, porque
    uma calibração de 0% e uma sem dados lêem-se de maneiras opostas.
    """
    if den <= 0:
        return "sem amostras"
    return f"{100.0 * num / den:.0f}%  ({num}/{den})"


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


def cmd_medir(args) -> int:
    if args.tipo == "achado" and not args.veredicto:
        sys.stderr.write(
            "ERRO: um achado sem veredicto não é um achado medido — é uma nota. "
            "Usa --veredicto confirmado|plausivel|morto.\n"
        )
        return 2
    if args.tipo == "entrega" and args.veredicto:
        sys.stderr.write(
            "ERRO: uma entrega não tem veredicto. O veredicto é do achado.\n"
        )
        return 2
    d = _dir_estado()
    reg = {
        "t": _agora(),
        "sessao": _id_sessao(),
        "ramo": _git("rev-parse", "--abbrev-ref", "HEAD") or "desconhecido",
        "tipo": args.tipo,
    }
    if args.tipo == "achado":
        reg["veredicto"] = args.veredicto
        reg["onde"] = args.onde
        if args.portao:
            reg["portao"] = args.portao
    for campo in ("ticket", "pr", "nota"):
        valor = getattr(args, campo, None)
        if valor:
            reg[campo] = valor
    _acrescentar_ledger(d, reg)
    resumo = reg["tipo"]
    if reg.get("veredicto"):
        resumo += f" {reg['veredicto']} ({reg['onde']})"
    if reg.get("portao"):
        resumo += f" · portão {reg['portao']}"
    # ASCII na seta de propósito. Em Windows, com a saída redireccionada ou num
    # pipe, o Python escreve em cp1252 e um `→` (U+2192) aborta o comando — o que
    # só acontece quando alguém o usa num script, que é o uso que interessa. O
    # resto do ficheiro já era seguro; esta linha não era, e o teste apanhou-a.
    #
    # E A FALHA DE IMPRESSÃO NÃO PODE AVERMELHAR O COMANDO, porque o registo JÁ
    # FOI ESCRITO acima. Um `medir` que grava e depois sai != 0 convida a uma
    # repetição que conta o mesmo achado duas vezes — e um ledger que
    # sobre-conta é pior do que um ledger vazio, porque parece medido. Foi assim
    # que o U+2192 se descobriu: a primeira corrida rebentou no print e deixou o
    # registo lá. O ledger é a operação; o eco é cosmético.
    try:
        sys.stdout.write(f"medido: {resumo}  ->  {d / LEDGER}\n")
        sys.stdout.flush()
    except OSError:
        # Não basta apanhar o `write`: com `| head -1` a falha aparece no FLUSH,
        # e um flush que falha no encerramento do interpretador devolve 120 —
        # medido. Reaponta-se o descritor para o vazio para que o flush final
        # não volte a falhar, e devolve-se 0, porque o registo está escrito.
        try:
            os.dup2(os.open(os.devnull, os.O_WRONLY), sys.stdout.fileno())
        except OSError:
            pass
    return 0


def cmd_indicadores(args) -> int:
    d = _dir_estado()
    regs = _ler_ledger(d, args.desde)
    achados = [r for r in regs if r.get("tipo") == "achado"]
    entregas = [r for r in regs if r.get("tipo") == "entrega"]
    confirmados = [r for r in achados if r.get("veredicto") == "confirmado"]
    pos_feito = [r for r in achados if r.get("onde") == "pos-feito"]

    janela = f"últimos {args.desde:g} dia(s)" if args.desde else "todo o ledger"
    print(f"indicadores da fábrica — {janela}; {len(regs)} registo(s)")
    print()
    print(f"  calibração          {_taxa(len(confirmados), len(achados))}"
          f"   confirmados / achados levantados")
    print(f"  taxa de retrabalho  {_taxa(len(pos_feito), len(entregas))}"
          f"   achados pós-feito / entregas declaradas")

    escapes = {}
    for r in achados:
        portao = r.get("portao")
        if portao:
            escapes[portao] = escapes.get(portao, 0) + 1
    print()
    if escapes:
        print("  escapes por portão")
        for portao, n in sorted(escapes.items(), key=lambda kv: (-kv[1], kv[0])):
            print(f"    {portao:<22} {n}")
    else:
        print("  escapes por portão  sem amostras")

    print()
    print("Os outros três indicadores da fábrica NÃO vivem aqui, porque já têm "
          "artefacto:\n"
          "  verde à primeira   `gh pr checks <n>` na primeira corrida\n"
          "  etapas saltadas    AOS_SKIPPED_STEP no veredicto dos gates (exit 3)\n"
          "  tempo até merge    da abertura do PR ao squash")
    return 0


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

    m = sub.add_parser("medir", help="acrescenta um achado ou uma entrega ao ledger da fábrica")
    m.add_argument("tipo", choices=["achado", "entrega"])
    m.add_argument("--veredicto", choices=list(VEREDICTOS),
                   help="obrigatório num achado; proibido numa entrega")
    m.add_argument("--onde", choices=list(ONDES), default="revisao",
                   help="revisao (apanhado antes de entregar) ou pos-feito "
                        "(entrou depois de alguém declarar feito)")
    m.add_argument("--portao", help="o portão que o devia ter apanhado, se havia um")
    m.add_argument("--ticket", help="AOS-NNN, se houver")
    m.add_argument("--pr", help="número do PR, se houver")
    m.add_argument("--nota", help="uma linha, para quem ler o ledger daqui a um mês")
    m.set_defaults(fn=cmd_medir)

    i = sub.add_parser("indicadores",
                       help="lê o ledger: calibração, taxa de retrabalho e escapes por portão")
    i.add_argument("--desde", type=float, help="janela em dias (omitido = todo o ledger)")
    i.set_defaults(fn=cmd_indicadores)

    args = p.parse_args()
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
