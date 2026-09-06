#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""estado-citado.py — Gate «estado-citado» (AOS-329).

Uma declaração no código que nomeia um ticket como BLOQUEADOR continua a passar depois de esse
ticket fechar. Foi assim que a `analises/11` §5 encontrou seis declarações caducadas de uma vez,
o AOS-325 corrigiu sete, e mesmo assim escapou uma oitava — encontrada só na validação
adversarial. Sete correcções pontuais não impediram a oitava, e não impedirão a nona: o padrão é
sempre o mesmo — uma declaração nomeia o ticket que a fecharia, o ticket fecha, e a declaração
fica a comprar confiança que já não sustenta.

O `deferrals` impõe que todo o marcador de dívida tenha eixo VERIFICÁVEL; não impõe que esse eixo
esteja ABERTO. O `ref-lint` resolve tickets contra as epics, mas não lê estado nem varre código.
Este gate liga as duas coisas.

════════════════════════════════════════════════════════════════════════════════════════════════
POR QUE É QUE ISTO É OPT-IN, E NÃO UMA VARREDURA
════════════════════════════════════════════════════════════════════════════════════════════════

Porque foi MEDIDO que a alternativa não funciona. Os números, sobre a árvore de 2026-09-05:

  citações `AOS-NNN` em packages/ ........................ 7 103
  vermelhos de um gate INGÉNUO (qualquer citação a fechado)  1 818  em 366 ficheiros
  citações com vocabulário de BLOQUEIO (12 expressões) ....    97
    dessas, a apontar para ticket FECHADO .................    46
    dessas, a apontar para ticket ABERTO ..................     0

Os 1 818 são a definição do gate que se desliga, e o corpus já registou essa lição duas vezes
(`ref-lint.py`: «16% de falsos positivos numa versão SABIDAMENTE correcta faria um gate que
ninguém aguenta»). Mas o vocabulário fechado também não serve: dos 46, a leitura um-a-um mostra
que a esmagadora maioria são falsos positivos por ambiguidade do português —

  «a prova que FALTAVA a AOS-262»            faltava é sobre uma prova, não um bloqueio
  «ATÉ AOS-308 esta frase dizia…»            nota HISTÓRICA, o oposto de um bloqueio
  «ler PENDENTES de exaustão (AOS-263)»      pendentes é substantivo do domínio
  «AGUARDA decisão humana (AOS-263)»         mensagem de erro; o ticket é a ORIGEM
  «BLOQUEADOR: DEF-218 (AOS-265 já aterrou)» a forma JÁ CORRIGIDA

— e ZERO citações de bloqueio apontam para um ticket aberto, ou seja não há sinal a preservar.
Cerca de 90% de falsos positivos: pior do que os 16% que o repositório já rejeitou por escrito.

A conclusão é que a relação «isto espera por aquilo» não está no texto. Tem de ser DECLARADA. É
o mesmo raciocínio do `<!-- rtm: adrs-mencionados -->` do `ref-lint`, que existe porque «isto é
menção, não implementação» também não se infere.

════════════════════════════════════════════════════════════════════════════════════════════════
O QUE ESTE GATE **NÃO** VERIFICA
════════════════════════════════════════════════════════════════════════════════════════════════

Declarar o alcance é parte do molde (ver o §«O QUE ESTE GATE NÃO VERIFICA» do `deferrals.py`):

  1. NÃO verifica declarações SEM marcador. Uma frase que diga «só medeia algo em AOS-265» e não
     traga `BLOQUEADOR:` passa. É o preço do opt-in, e é deliberado — a alternativa foi medida
     acima.
  2. NÃO verifica declarações que não citem ticket nenhum. Das oito que a EPIC-23 corrigiu, TRÊS
     eram factos falsos sem citação («o nó não importa platform/broker») ou contradições entre
     ficheiros. Nenhum gate deste tipo as apanha.
  3. NÃO cobre os 224 tickets de EPIC-01..18, que não têm `### Estado`. São ~74% das citações do
     repositório. Estão contados como ABSTENÇÕES e impressos em cada execução — a cegueira é
     declarada, não escondida.
  4. NÃO verifica `deploy/`. Uma das oito vivia em `docker-compose.prod.yml`. O âmbito é o do
     `deferrals` (packages + tecnica + docs/adr), para que os dois gates não divirjam.
  5. NÃO julga se o bloqueio é VERDADEIRO. Só se o ticket que o sustenta ainda está aberto.
"""

import os
import re
import sys

# Raiz sobreescrevível para o self-test poder correr sobre uma cópia sem tocar na árvore. É o
# mesmo seam de `AOS_DEFERRALS_ROOT`.
ROOT = os.path.abspath(os.environ.get("AOS_ESTADO_CITADO_ROOT") or
                       os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

SPECS = os.path.join(ROOT, "specs")
# O MESMO âmbito do `deferrals`, para que os dois gates não divirjam sobre o que é «o código».
ALVOS = [os.path.join(ROOT, "packages"), os.path.join(ROOT, "tecnica"), os.path.join(ROOT, "docs", "adr")]
EXCLUIR_DIRS = {".git", "vendor", "testdata", "node_modules", ".claude"}

# O MARCADOR, no molde de `Eixo:` dos banners de postura e dos marcadores do `deferrals`.
#
# INSENSÍVEL À CAIXA E COM PLURAL, e a varredura é sobre LINHAS LÓGICAS, não físicas. A primeira
# versão exigia maiúsculas e o ticket na MESMA linha física, e mediu-se logo a seguir ao merge que
# isso deixava escapar a forma mais natural de todas: um comentário Go partido em duas linhas —
#
#     // BLOQUEADOR:
#     // AOS-265
#
# — que o `gofmt` não impede e que qualquer autor produz ao escrever um comentário longo. Era uma
# fuga SILENCIOSA, que é o pior tipo: quem a escreve julga estar coberto pelo gate e não está.
RE_MARCADOR = re.compile(r"BLOQUEADOR(?:ES)?\s*:\s*((?:AOS-\d{3})(?:\s*[,/e]\s*AOS-\d{3})*)", re.IGNORECASE)
RE_TICKET = re.compile(r"AOS-\d{3}")

RE_HDR = re.compile(r"^#{2,3} (AOS-\d{3})\s*[-–—]", re.M)
RE_ESTADO = re.compile(r"^### Estado\s*\n+\*\*([A-ZÁÉÍÓÚÂÊÔÃÕÇ][A-ZÁÉÍÓÚÂÊÔÃÕÇ ]*)", re.M)

# OS OITO LEXEMAS que o corpus usa de facto, reduzidos ao primeiro token em maiúsculas. As 27
# formas literais medidas em `specs/EPIC-*.md` colapsam nestes.
FECHADO = {"IMPLEMENTADO", "FECHADO", "ENTREGUE", "FEITO"}
# REMOVIDO é fechado por uma razão DIFERENTE — o ticket foi cancelado, não entregue —, e uma
# declaração que o cite como bloqueador está errada de forma pior: espera por algo que nunca vem.
CANCELADO = {"REMOVIDO"}
# PARCIAL/ABERTO/POR não fecham: um bloqueio sobre um ticket parcialmente entregue pode continuar
# verdadeiro, e negá-lo obrigaria a julgar QUAL parte — que este gate não sabe fazer.
ABERTO = {"POR", "ABERTO", "PARCIAL"}


def estados_dos_tickets():
    """AOS-NNN -> primeiro lexema do `### Estado`, ou None quando indeterminável."""
    out = {}
    if not os.path.isdir(SPECS):
        return out
    for nome in sorted(os.listdir(SPECS)):
        if not (nome.startswith("EPIC-") and nome.endswith(".md")):
            continue
        with open(os.path.join(SPECS, nome), encoding="utf-8") as fh:
            texto = fh.read()
        cabecalhos = list(RE_HDR.finditer(texto))
        for i, m in enumerate(cabecalhos):
            fim = cabecalhos[i + 1].start() if i + 1 < len(cabecalhos) else len(texto)
            bloco = texto[m.end():fim]
            e = RE_ESTADO.search(bloco)
            out[m.group(1)] = e.group(1).strip().split()[0] if e else None
    return out


def ficheiros_alvo():
    for base in ALVOS:
        if not os.path.isdir(base):
            continue
        for pasta, dirs, ficheiros in os.walk(base):
            dirs[:] = [d for d in dirs if d not in EXCLUIR_DIRS]
            for f in sorted(ficheiros):
                if f.endswith(".go") or f.endswith(".md"):
                    yield os.path.join(pasta, f)


def rel(p):
    return os.path.relpath(p, ROOT).replace(os.sep, "/")


def linhas_logicas(linhas):
    """Junta linhas de CONTINUAÇÃO numa só, e devolve (nº da primeira linha, texto unido).

    Um comentário Go de duas linhas é UMA declaração, e a primeira versão deste gate lia-o como
    duas — deixando escapar o marcador quando o ticket ficava na segunda. Continuação é uma linha
    cujo conteúdo aparado começa por `//` (Go) ou uma linha não-vazia a seguir a outra não-vazia
    (Markdown, onde um parágrafo é a unidade).

    O prefixo `//` é removido ao juntar, para que `BLOQUEADOR:` e o ticket fiquem separados por
    espaço e não por `// `.
    """
    bloco, inicio = [], 0
    for n, bruta in enumerate(linhas, 1):
        t = bruta.strip()
        comentario = t.startswith("//")
        conteudo = t[2:].strip() if comentario else t
        if not conteudo:
            if bloco:
                yield inicio, " ".join(bloco)
                bloco = []
            continue
        if not bloco:
            inicio = n
        bloco.append(conteudo)
    if bloco:
        yield inicio, " ".join(bloco)


def main():
    estados = estados_dos_tickets()
    if not estados:
        print("FAIL estado-citado: nenhum ticket lido de specs/EPIC-*.md — corpus ausente ou ilegível", file=sys.stderr)
        return 1

    caducadas, canceladas, desconhecidos, abstencoes = [], [], [], []
    marcadas = 0

    for caminho in ficheiros_alvo():
        try:
            with open(caminho, encoding="utf-8") as fh:
                linhas = fh.read().split("\n")
        except (OSError, UnicodeDecodeError):
            continue
        for n, linha in linhas_logicas(linhas):
            for m in RE_MARCADOR.finditer(linha):
                for ticket in RE_TICKET.findall(m.group(1)):
                    marcadas += 1
                    estado = estados.get(ticket, "__INEXISTENTE__")
                    onde = (rel(caminho), n, ticket, linha.strip()[:120])
                    if estado == "__INEXISTENTE__":
                        desconhecidos.append(onde)
                    elif estado is None:
                        abstencoes.append(onde)
                    elif estado in FECHADO:
                        caducadas.append(onde + (estado,))
                    elif estado in CANCELADO:
                        canceladas.append(onde + (estado,))
                    # ABERTO e qualquer lexema novo ⇒ nada a dizer. Um lexema NOVO é tratado
                    # como aberto de propósito: inventar que uma palavra desconhecida significa
                    # «fechado» avermelharia o gate por uma mudança de redacção.

    rc = 0
    if caducadas:
        rc = 1
        print("FAIL estado-citado: declaracao nomeia como BLOQUEADOR um ticket ja FECHADO:", file=sys.stderr)
        for f, n, t, texto, est in caducadas:
            print("       %s:%d — %s esta %s\n         %s" % (f, n, t, est, texto), file=sys.stderr)
    if canceladas:
        rc = 1
        print("FAIL estado-citado: declaracao espera por um ticket REMOVIDO (que nunca vai aterrar):", file=sys.stderr)
        for f, n, t, texto, est in canceladas:
            print("       %s:%d — %s esta %s\n         %s" % (f, n, t, est, texto), file=sys.stderr)
    if desconhecidos:
        rc = 1
        print("FAIL estado-citado: BLOQUEADOR nomeia um ticket que NAO EXISTE no backlog:", file=sys.stderr)
        for f, n, t, texto in desconhecidos:
            print("       %s:%d — %s\n         %s" % (f, n, t, texto), file=sys.stderr)

    # A CEGUEIRA É IMPRESSA, sempre. Um gate que se cala sobre o que não sabe verificar sugere uma
    # cobertura que não tem — é o molde das «abstenções» do `ref-lint`.
    print("estado-citado: %d declaracao(oes) com BLOQUEADOR verificada(s); %d abstencao(oes) "
          "(ticket sem `### Estado` no corpus — EPIC-01..18 nao o tem)" % (marcadas, len(abstencoes)))
    if abstencoes:
        for f, n, t, _ in abstencoes:
            print("   ABSTENCAO %s:%d — %s sem estado determinavel" % (f, n, t))
    if rc == 0:
        print("OK: nenhuma declaracao marcada como BLOQUEADOR cita um ticket ja fechado.")
    return rc


if __name__ == "__main__":
    sys.exit(main())
