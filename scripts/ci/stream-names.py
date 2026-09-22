#!/usr/bin/env python3
"""
stream-names.py — Gate dos nomes de `stream_id` do Event Store (AOS-424).

PORQUÊ ESTE GATE EXISTE
-----------------------
O `jetstream.Store.subjectDe` RECUSA qualquer `stream_id` que contenha um
carácter não representável num subject NATS (`. * >`, espaço, tab, CR, LF) — em
vez de escapar em silêncio para um subject vizinho onde outro stream leria os
nossos eventos. A recusa é a escolha certa.

O defeito não é essa recusa: é a ASSIMETRIA. O store de FICHEIRO
(`eventstore.Store.Append`) não valida `streamID` nenhum. Enquanto os dois
backends discordarem, um nome inválido funciona em desenvolvimento, em CI e em
produção-sobre-ficheiro, e só falha na topologia que nada exercita — que é
precisamente a única que arbitra entre processos (DEF-282) e portanto a única em
que um consumidor de fila pode existir.

Mediu-se em 2026-09-21: NOVE streams da árvore não eram representáveis, dois
grupos deles compostos em produção. O primeiro caso (AOS-417) foi apanhado por
acaso, na discovery do ticket seguinte, DEPOIS de dez gates verdes.

O QUE ESTE GATE VERIFICA
------------------------
S1. **Constantes de nome de stream.** Extrai da árvore as constantes cujo
    identificador contém `stream` (qualquer capitalização), declaradas numa linha
    e com valor string literal. Zero constantes ⇒ FALHA: um parser partido não
    pode passar por «nada a verificar».
S2. **Representabilidade.** Nenhum valor pode conter um carácter que o
    `subjectDe` recusa. **A regra é LIDA DA FONTE** — extraída do
    `strings.ContainsAny(streamID, "…")` do próprio `jetstream/store.go` — e não
    duplicada aqui: duplicá-la daria um gate verde no dia em que a regra
    apertasse. A extracção tem controlo de não-vacuidade (ver S4).
S3. **Literais no ponto de uso.** Um argumento de stream literal em
    `.Append(ctx, "…"` / `.Read(ctx, "…"` cai na mesma regra. Apanha o caso em
    que alguém escreve o nome à mão em vez de referenciar a constante.
S4. **Não-vacuidade da regra.** A regra extraída TEM de rejeitar um valor de
    controlo conhecidamente mau (`aos.internal/plan-requests`, o nome que o
    AOS-417 usou e que tornava a rota inutilizável sobre JetStream). Se a
    extracção partir, o gate falha em vez de passar a medir nada.

O QUE ESTE GATE **NÃO** VERIFICA (declarado por honestidade)
------------------------------------------------------------
- **NÃO apanha composição em RUNTIME.** Um `stream_id` formado a partir de um
  valor que entra por configuração, por ficheiro de política ou por token externo
  — o `run_id` do cliente, o nome do modelo na chave de admissão, o `scope` de um
  `RatificationID` — é invisível a um gate estático. Essa metade é o **AOS-425**,
  e a correcção lá é de outra natureza: validar onde o valor ENTRA, não onde é
  usado. Um gate que parecesse cobrir a classe inteira e não cobrisse seria pior
  do que um gate que declara o seu alcance.
- NÃO verifica se um stream interno vive sob o prefixo reservado `aos-internal/`.
  Decidir estaticamente «isto é interno» exige saber quem o escreve; o que
  protege o read-path dessa confusão é a trava do **AOS-426**, que lê os dados.
- NÃO valida o conteúdo nem o schema dos eventos (isso é o `event-catalog`).
"""

import os
import re
import sys
from pathlib import Path

RAIZ = Path(__file__).resolve().parents[2]
PACKAGES = RAIZ / "packages"
FONTE_DA_REGRA = PACKAGES / "substrate" / "eventstore" / "jetstream" / "store.go"
BASELINE = Path(
    os.environ.get("AOS_STREAM_NAMES_BASELINE")
    or (Path(__file__).resolve().parent / "baseline" / "stream-names.txt")
)

# O valor de controlo: o nome que o AOS-417 usou e que o `subjectDe` recusa.
# Se a regra extraída NÃO o apanhar, a extracção está errada (S4).
CONTROLO_MAU = "aos.internal/plan-requests"

# `const nomeStream = "valor"` / `nomeStream = "valor"` / `NomeStreamID = "valor"`,
# numa linha e a terminar na string. Mesmo critério de forma do `event-catalog`.
RE_CONST = re.compile(
    r'^[ \t]*(?:const[ \t]+)?([A-Za-z0-9_]*[Ss]tream[A-Za-z0-9_]*)[ \t]*'
    r'(?::?=)[ \t]*"([^"\\\n]*)"[ \t]*$'
)
# NOMES DE ATRIBUTO NÃO SÃO NOMES DE STREAM, e a distinção não é decorativa: medido
# na primeira execução deste gate, `AtributoStream`/`AttrLogStream` (o atributo OTel
# `aos.eventstore.stream_id`, em `eventstore/rastreio.go` e `otel-genai/semconv.go`)
# vinham acusados. São CHAVES de span — os pontos são a convenção do OpenTelemetry e
# nunca chegam a um `stream_id`. Deixá-los no relatório treinaria quem lê o gate a
# ignorá-lo, que é como um gate morre.
#
# O filtro é por identificador, e é uma heurística — declarada como tal. Um atributo
# cujo identificador não comece por `Attr`/`Atributo` voltaria a ser acusado, e o
# remédio é corrigir este filtro, nunca baselinar o achado: uma baseline diz «isto é
# dívida real», e uma chave de span não é dívida nenhuma.
RE_ATRIBUTO = re.compile(r"^(?:Attr|Atributo)")
# Argumento de stream LITERAL no ponto de uso: `.Append(ctx, "x"` / `.Read(ctx, "x"`.
RE_USO_LITERAL = re.compile(r'\.(?:Append|Read|StreamHead|IngestStream)\([^,)]+,[ \t]*"([^"\\\n]*)"')
# A regra do subject NATS, tal como o `subjectDe` a escreve.
RE_REGRA = re.compile(r'ContainsAny\(streamID,\s*"([^"]*)"\)')


def ficheiros_go():
    for caminho in sorted(PACKAGES.rglob("*.go")):
        if caminho.name.endswith("_test.go"):
            continue
        yield caminho


def sem_comentarios(texto: str) -> str:
    """Substitui comentários por espaços, preservando posições e linhas."""
    saida = []
    i, n = 0, len(texto)
    while i < n:
        c = texto[i]
        if c == '"' or c == "`":
            fim = i + 1
            while fim < n and texto[fim] != c:
                if c == '"' and texto[fim] == "\\":
                    fim += 1
                fim += 1
            saida.append(texto[i:min(fim + 1, n)])
            i = fim + 1
        elif texto.startswith("//", i):
            fim = texto.find("\n", i)
            fim = n if fim < 0 else fim
            saida.append(" " * (fim - i))
            i = fim
        elif texto.startswith("/*", i):
            fim = texto.find("*/", i)
            fim = n if fim < 0 else fim + 2
            saida.append("".join(ch if ch == "\n" else " " for ch in texto[i:fim]))
            i = fim
        else:
            saida.append(c)
            i += 1
    return "".join(saida)


def carregar_regra() -> str:
    """Lê da FONTE o conjunto de caracteres que o `subjectDe` recusa."""
    if not FONTE_DA_REGRA.exists():
        print(f"ERRO: nao encontrei a fonte da regra em {FONTE_DA_REGRA}", file=sys.stderr)
        print("      sem ela este gate nao tem o que impor. Fail-closed.", file=sys.stderr)
        return ""
    m = RE_REGRA.search(FONTE_DA_REGRA.read_text(encoding="utf-8"))
    if not m:
        print(f"ERRO: nao encontrei `ContainsAny(streamID, ...)` em {FONTE_DA_REGRA}", file=sys.stderr)
        print("      a guarda do subject NATS mudou de forma. Actualize este gate para ler a", file=sys.stderr)
        print("      regra nova — NAO o relaxe: o que ele impede e uma superficie que responde", file=sys.stderr)
        print("      erro a tudo no unico substrato que arbitra entre processos.", file=sys.stderr)
        return ""
    # Desfaz os escapes Go do literal.
    return m.group(1).replace(r"\t", "\t").replace(r"\r", "\r").replace(r"\n", "\n")


def carregar_baseline() -> dict:
    """Divida reconhecida: `chave # owner=... razao`. Sem `owner=` ⇒ o gate falha."""
    aceites, erros = {}, []
    if not BASELINE.exists():
        return aceites
    for n, linha in enumerate(BASELINE.read_text(encoding="utf-8").splitlines(), 1):
        crua = linha.strip()
        if not crua or crua.startswith("#"):
            continue
        chave, _, nota = crua.partition("#")
        chave = chave.strip()
        if "owner=" not in nota:
            erros.append(f"{BASELINE.name}:{n}: entrada sem `owner=` — {chave}")
            continue
        aceites[chave] = nota.strip()
    if erros:
        for e in erros:
            print("ERRO: " + e, file=sys.stderr)
        sys.exit(1)
    return aceites


def main() -> int:
    proibidos = carregar_regra()
    if not proibidos:
        return 1

    # S4 — NÃO-VACUIDADE. A regra tem de apanhar o valor de controlo.
    if not any(ch in CONTROLO_MAU for ch in proibidos):
        print(f"ERRO: a regra lida ({proibidos!r}) NAO apanha o valor de controlo", file=sys.stderr)
        print(f"      {CONTROLO_MAU!r} — a extraccao esta errada e este gate nao mede nada.", file=sys.stderr)
        return 1

    baseline = carregar_baseline()
    achados, declaradas, usados = [], 0, 0

    for caminho in ficheiros_go():
        rel = caminho.relative_to(RAIZ).as_posix()
        try:
            texto = sem_comentarios(caminho.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError):
            continue

        for n, linha in enumerate(texto.splitlines(), 1):
            m = RE_CONST.match(linha)
            if m:
                nome, valor = m.group(1), m.group(2)
                if RE_ATRIBUTO.match(nome):
                    continue  # chave de span, não nome de stream — ver [RE_ATRIBUTO]
                declaradas += 1
                mau = [ch for ch in valor if ch in proibidos]
                if mau:
                    achados.append((f"const|{rel}|{nome}|{valor}", rel, n,
                                    f"constante {nome} = {valor!r} contem {mau!r}"))
                continue
            for u in RE_USO_LITERAL.finditer(linha):
                valor = u.group(1)
                usados += 1
                mau = [ch for ch in valor if ch in proibidos]
                if mau:
                    achados.append((f"uso|{rel}|{valor}", rel, n,
                                    f"stream literal {valor!r} no ponto de uso contem {mau!r}"))

    # S1 — fail-closed: zero constantes ⇒ o parser partiu.
    if declaradas == 0:
        print("ERRO: zero constantes de nome de stream encontradas na arvore.", file=sys.stderr)
        print("      Um parser partido nao pode passar por «nada a verificar». Fail-closed.", file=sys.stderr)
        return 1

    novos = [a for a in achados if a[0] not in baseline]
    reconhecidos = [a for a in achados if a[0] in baseline]

    if reconhecidos:
        print(f"\nDIVIDA RECONHECIDA ({len(reconhecidos)}) — nomes nao representaveis tolerados pela "
              f"baseline, com dono declarado (NAO sao verde; ver stream-names.txt):")
        for chave, rel, n, desc in reconhecidos:
            print(f"  ~ {rel}:{n} — {desc} — {baseline[chave]}")

    if novos:
        print(f"\nFALHA ({len(novos)}) — nome(s) de stream nao representavel(eis) num subject NATS:",
              file=sys.stderr)
        for _, rel, n, desc in novos:
            print(f"  x {rel}:{n} — {desc}", file=sys.stderr)
        print("\n  O `stream_id` do AOS e livre, mas um subject NATS nao e: o ponto separa tokens e",
              file=sys.stderr)
        print("  `*`/`>` sao curingas. Sobre JetStream o Append recusa com E_CONFIG — e o JetStream",
              file=sys.stderr)
        print("  e o unico substrato que arbitra entre processos (DEF-282). Use `-` em vez de `.`,", file=sys.stderr)
        print("  e `/` para namespacing (a barra e representavel E mantem o stream fora do alcance", file=sys.stderr)
        print("  de GET /runs/{id}/... — ver AOS-426).", file=sys.stderr)
        return 1

    print(f"\nNomes de stream OK: {declaradas} constante(s) e {usados} literal(is) no ponto de uso "
          f"verificados contra a regra lida de {FONTE_DA_REGRA.relative_to(RAIZ).as_posix()} "
          f"({len(reconhecidos)} em divida reconhecida).")
    print("  Ambito: NAO cobre composicao em runtime (run_id, nome de modelo, scope) — ver AOS-425.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
