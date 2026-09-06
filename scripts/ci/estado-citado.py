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
AS TRÊS PERGUNTAS SEPARADAS, E POR QUE É QUE JUNTÁ-LAS PARTIA O GATE
════════════════════════════════════════════════════════════════════════════════════════════════

A primeira versão respondia às três numa expressão só — `BLOQUEADOR:\\s*(AOS-\\d{3})` — e uma
revisão adversarial mediu que ela falhava nos DOIS sentidos ao mesmo tempo.

FUGIA, por exigir o ticket COLADO aos dois-pontos: `BLOQUEADOR: aguarda AOS-265`,
`**BLOQUEADOR:** AOS-265`, `BLOQUEADOR: (AOS-265)` e — a pior de todas — um comentário Go com um
`//` em branco a separar parágrafos, que é a forma mais comum de comentário longo. Todos passavam
em SILÊNCIO, que é o pior tipo de fuga: quem escreve o marcador julga estar coberto e não está.

E AVERMELHAVA texto que dizia o CONTRÁRIO, porque `re.IGNORECASE` sobre linhas unidas apanha «a
ausência nunca foi bloqueador: AOS-265 já cobriu o caso». Um gate que acusa uma frase de afirmar
o oposto do que ela afirma é um gate que alguém desliga — e desligado não protege nada.

As duas falhas têm a mesma origem: uma expressão a responder a três perguntas distintas. Agora
são três passos, cada um com o seu critério:

  1. ONDE ESTÁ O MARCADOR      `BLOQUEADOR:`, insensível à caixa, com plural e com adornos de
                               markdown (`**BLOQUEADOR:**`).

  2. ELE ABRE UMA DECLARAÇÃO?  Só conta quando ABRE a declaração — na SUA linha física, o que o
                               precede são caracteres estruturais (`//`, `-`, `*`, `|`, `>`, `#`,
                               numeração) ou o fim de uma frase anterior. É isto que distingue
                               «BLOQUEADOR: AOS-265» de «nunca foi bloqueador: AOS-265», sem
                               precisar de perceber português: uma frase que MENCIONA a palavra
                               não declara um bloqueio.

  3. QUAL É O EIXO             O eixo é o PRIMEIRO identificador `XXX-NNN` depois dos dois-pontos,
                               numa janela curta. Se for `AOS-NNN`, é um ticket e verifica-se; se
                               for outra coisa — `DEF-218`, `ADR-019` — o eixo NÃO é um ticket e
                               este gate ABSTÉM-SE, em vez de saltar à frente até encontrar um
                               `AOS-` qualquer. Sem esta regra, a forma JÁ CORRIGIDA que vive em
                               `packages/cmd/aos/broker_vault_env.go:67` —
                               «Bloqueador: DEF-218 (AOS-265 já aterrou sem o fechar)» — ficaria
                               VERMELHA por citar, dentro de um parêntesis, o ticket que ela
                               própria diz estar fechado.

════════════════════════════════════════════════════════════════════════════════════════════════
O QUE ESTE GATE **NÃO** VERIFICA
════════════════════════════════════════════════════════════════════════════════════════════════

Declarar o alcance é parte do molde (ver o §«O QUE ESTE GATE NÃO VERIFICA» do `deferrals.py`):

  1. NÃO verifica declarações SEM marcador. Uma frase que diga «só medeia algo em AOS-265» e não
     traga `BLOQUEADOR:` passa. É o preço do opt-in, e é deliberado — a alternativa foi medida.
  2. NÃO verifica declarações que não citem ticket nenhum. Das oito que a EPIC-23 corrigiu, TRÊS
     eram factos falsos sem citação («o nó não importa platform/broker») ou contradições entre
     ficheiros. Nenhum gate deste tipo as apanha.
  3. NÃO cobre os 224 tickets de EPIC-01..18, que não têm `### Estado`. São ~74% das citações do
     repositório. Estão contados como ABSTENÇÕES e impressos em cada execução — a cegueira é
     declarada, não escondida.
  4. NÃO verifica `deploy/`, e isso NÃO foi resolvido por abrir mais extensões. Uma das oito
     declarações caducadas vivia em `docker-compose.prod.yml`, que está em `deploy/` — fora do
     âmbito, que é o do `deferrals` (packages + tecnica + docs/adr) para que os dois gates não
     divirjam sobre o que é «o código». A lista de extensões deixou de ser só `.go`/`.md` porque
     um marcador é uma convenção de TEXTO e não devia depender da linguagem do ficheiro; mas foi
     MEDIDO que hoje isso cobre exactamente MAIS ZERO ficheiros — dentro deste âmbito só existem
     `.go` e `.md` (1 705 ficheiros, antes e depois). É um buraco fechado para a frente, não uma
     cobertura ganha agora, e a declaração fica assim para não comprar confiança que não sustenta.
  5. NÃO julga se o bloqueio é VERDADEIRO. Só se o ticket que o sustenta ainda está aberto.
  6. NÃO apanha um marcador ENTERRADO no meio de uma frase — «o que falta aqui é BLOQUEADOR:
     AOS-265» é ignorado, pela regra 2 acima. É deliberado, e é uma TROCA, não um descuido: é o
     mesmo critério que impede o falso positivo por inversão de sentido, e não há forma de ter um
     sem perder o outro. A convenção é que o marcador ABRE a declaração, como o `Eixo:` dos
     banners de postura.
  7. NÃO decide sobre um lexema de estado que não conheça. Um `### Estado` que diga uma palavra
     fora do vocabulário é ABSTENÇÃO IMPRESSA, não «aberto» silencioso — a versão anterior lia
     `**Implementado**` (caixa de título) como o lexema `I`, tratava-o como aberto e não dizia
     nada, que é a cegueira exacta que este ficheiro promete não ter.
  8. NÃO tem hoje NENHUMA declaração viva para verificar. A única ocorrência do marcador no âmbito
     é a do `broker_vault_env.go:67`, cujo eixo é um `DEF-`, logo abstenção. O gate é um guarda de
     convenção PARA A FRENTE, e a sua não-vacuidade vem do teste-veneno do `selftest.sh` §W, não
     do corpus. Está dito aqui porque um gate verde com cobertura zero parece uma prova e não é.
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

# O prefixo de comentário POR EXTENSÃO. A varredura era só `.go` e `.md` — o marcador deixava de
# valer por causa da linguagem do ficheiro, o que não é uma propriedade de uma convenção de texto.
# Medido: dentro deste âmbito isto cobre hoje MAIS ZERO ficheiros (ver §4); é um buraco fechado
# para a frente. `None` = a linguagem não tem comentário de linha a remover (Markdown, onde `#` é
# cabeçalho e não comentário).
PREFIXO_COMENTARIO = {
    ".go": "//", ".ts": "//", ".tsx": "//", ".js": "//", ".proto": "//",
    ".py": "#", ".sh": "#", ".bash": "#", ".yml": "#", ".yaml": "#", ".toml": "#",
    ".sql": "--",
    ".md": None,
}

# 1) ONDE ESTÁ O MARCADOR. Insensível à caixa, com plural, e tolerante aos adornos de markdown
#    que qualquer autor escreve num documento (`**BLOQUEADOR:**`).
RE_MARCADOR = re.compile(r"BLOQUEADOR(?:ES)?\*{0,2}\s*:", re.IGNORECASE)
# 3) QUAL É O EIXO. Um identificador de eixo GENÉRICO — `AOS-329`, `DEF-218`, `ADR-019` —, porque
#    a pergunta «qual é o eixo» tem de poder ser respondida com «não é um ticket».
RE_IDENT = re.compile(r"\b([A-Z]{2,4})-(\d{2,4})\b")
RE_TICKET = re.compile(r"AOS-\d{3}")
# A lista contígua de tickets que se segue ao primeiro, para `BLOQUEADOR: AOS-262, AOS-263`.
RE_LISTA = re.compile(r"AOS-\d{3}(?:\s*(?:,|/|\be\b)\s*AOS-\d{3})*")

# Quanto texto depois dos dois-pontos conta como eixo. Curto de propósito: o eixo é o que o
# marcador aponta, não tudo o que vem a seguir no parágrafo.
JANELA = 120

# Caracteres que podem preceder o marcador sem que ele deixe de ABRIR a declaração: marcas de
# lista, células de tabela, citação, cabeçalho, numeração, parêntesis, adornos de markdown.
ESTRUTURAIS = " \t*-–—>#|()[]{}0123456789.:+=\"'`"
# Aberturas de comentário DENTRO de uma linha: `var _ = 1 // BLOQUEADOR: AOS-265` tem o marcador a
# abrir o comentário, ainda que com código à esquerda.
ABERTURAS = ("//", "/*", "#", "--", "<!--")
# Fim de frase: uma declaração nova pode começar a seguir a uma frase anterior, na mesma linha. É
# o que mantém legível «apenas a linha de postura. Bloqueador: DEF-218 (…)».
FIM_DE_FRASE = (". ", "! ", "? ", "; ", ".\t")

# Qualquer nível de cabeçalho, e sem exigir travessão. O `^#{2,3} (AOS-\\d{3})\\s*[-–—]` anterior
# não via `## AOS-902: título`, e o bloco do ticket ANTERIOR engolia-o com o `### Estado` dentro —
# o que fazia o gate declarar IMPLEMENTADO um ticket ABERTO. Medido: alargar isto é NO-OP sobre o
# corpus actual (325 tickets, os mesmos 101 estados, zero mudados), logo fecha o buraco latente
# sem mexer em nenhuma leitura de hoje.
RE_HDR = re.compile(r"^#{1,6}\s*(AOS-\d{3})\b", re.M)
# O estado tolera caixa de título, ausência de `**`, dois-pontos no próprio cabeçalho e marcas de
# lista ou citação. O lexema é normalizado para maiúsculas ANTES de ser comparado — a versão
# anterior exigia maiúsculas na origem e lia `**Implementado**` como o lexema `I`.
RE_ESTADO = re.compile(
    r"^#{2,4}\s+Estado\s*:?[^\S\n]*\n{0,2}[^\S\n]*[-*>]*[^\S\n]*\**[^\S\n]*([A-Za-zÀ-ÿ]+)", re.M)

# OS OITO LEXEMAS que o corpus usa de facto, reduzidos ao primeiro token em maiúsculas. As 27
# formas literais medidas em `specs/EPIC-*.md` colapsam nestes.
FECHADO = {"IMPLEMENTADO", "FECHADO", "ENTREGUE", "FEITO"}
# REMOVIDO é fechado por uma razão DIFERENTE — o ticket foi cancelado, não entregue —, e uma
# declaração que o cite como bloqueador está errada de forma pior: espera por algo que nunca vem.
CANCELADO = {"REMOVIDO"}
# PARCIAL/ABERTO/POR não fecham: um bloqueio sobre um ticket parcialmente entregue pode continuar
# verdadeiro, e negá-lo obrigaria a julgar QUAL parte — que este gate não sabe fazer.
ABERTO = {"POR", "ABERTO", "PARCIAL"}
CONHECIDOS = FECHADO | CANCELADO | ABERTO


def estados_dos_tickets():
    """AOS-NNN -> primeiro lexema do `### Estado` em MAIÚSCULAS, ou None quando indeterminável."""
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
            e = RE_ESTADO.search(texto[m.end():fim])
            out[m.group(1)] = e.group(1).strip().upper() if e else None
    return out


def ficheiros_alvo():
    for base in ALVOS:
        if not os.path.isdir(base):
            continue
        for pasta, dirs, ficheiros in os.walk(base):
            dirs[:] = [d for d in dirs if d not in EXCLUIR_DIRS]
            for f in sorted(ficheiros):
                ext = os.path.splitext(f)[1].lower()
                if ext in PREFIXO_COMENTARIO:
                    yield os.path.join(pasta, f), PREFIXO_COMENTARIO[ext]


def rel(p):
    return os.path.relpath(p, ROOT).replace(os.sep, "/")


def blocos_logicos(linhas, prefixo):
    """Agrupa linhas de CONTINUAÇÃO num bloco, devolvendo [(nº da linha FÍSICA, conteúdo)].

    Um comentário de duas linhas é UMA declaração, e a primeira versão deste gate lia-o como duas
    — deixando escapar o marcador quando o ticket ficava na segunda.

    O nº é o da linha FÍSICA de cada fragmento, não o do início do bloco: a versão seguinte unia
    tudo numa string e reportava sempre a primeira linha, pelo que um marcador na linha 28 de um
    comentário de 30 aparecia como `:1`. Um `file:line` que não abre no sítio certo é ruído.

    Uma linha SÓ com o prefixo (`//` ou `#` sozinho) é uma quebra de PARÁGRAFO dentro do mesmo
    comentário — continua o bloco. Era a fuga mais natural de todas, porque é assim que se separam
    parágrafos num comentário Go longo, e a versão anterior fechava o bloco nela.
    """
    bloco = []
    for n, bruta in enumerate(linhas, 1):
        t = bruta.strip()
        if prefixo and t.startswith(prefixo):
            conteudo = t[len(prefixo):].strip()
            if not conteudo:              # `//` sozinho: parágrafo novo, MESMO comentário.
                continue
        else:
            conteudo = t
            if not conteudo:              # linha vazia a sério: fim do bloco.
                if bloco:
                    yield bloco
                    bloco = []
                continue
        bloco.append((n, conteudo))
    if bloco:
        yield bloco


def abre_declaracao(conteudo, i):
    """O marcador em `conteudo[i:]` ABRE a declaração, ou está enterrado numa frase?

    É esta pergunta — e não o vocabulário — que separa «BLOQUEADOR: AOS-265» de «a ausência nunca
    foi bloqueador: AOS-265 já cobriu o caso». Sem ela o gate acusa de bloqueio caduco uma frase
    que diz exactamente o contrário, que é o falso positivo que faz desligar gates.
    """
    prefixo = conteudo[:i]
    corte = 0
    for a in ABERTURAS:                   # `var _ = 1 // BLOQUEADOR:` abre no `//`.
        p = prefixo.rfind(a)
        if p >= 0:
            corte = max(corte, p + len(a))
    resto = prefixo[corte:]
    if resto.strip(ESTRUTURAIS) == "":    # só estrutura à esquerda: abre.
        return True
    return resto.endswith(FIM_DE_FRASE)   # frase anterior fechada: abre uma nova.


def eixo_do_marcador(conteudo, fim, seguintes):
    """Devolve o eixo: ('tickets', [...]), ('outro', 'DEF-218') ou ('nenhum', '').

    A janela atravessa para as linhas SEGUINTES do mesmo bloco, porque o marcador e o ticket podem
    estar em linhas físicas diferentes — mas o eixo é o PRIMEIRO identificador que aparece, e não
    o primeiro `AOS-` que se encontre à frente. É essa distinção que mantém VERDE a forma já
    corrigida «Bloqueador: DEF-218 (AOS-265 já aterrou sem o fechar)».
    """
    janela = " ".join([conteudo[fim:]] + [c for _, c in seguintes])[:JANELA]
    for t in FIM_DE_FRASE:                # o eixo não atravessa para a frase seguinte.
        p = janela.find(t)
        if p >= 0:
            janela = janela[:p]
    m = RE_IDENT.search(janela)
    if not m:
        return "nenhum", ""
    if m.group(1) != "AOS":
        return "outro", m.group(0)
    lista = RE_LISTA.match(janela, m.start())
    return "tickets", RE_TICKET.findall(lista.group(0) if lista else m.group(0))


def main():
    estados = estados_dos_tickets()
    if not estados:
        print("FAIL estado-citado: nenhum ticket lido de specs/EPIC-*.md — corpus ausente ou ilegível", file=sys.stderr)
        return 1

    caducadas, canceladas, desconhecidos = [], [], []
    sem_estado, lexema_novo, eixo_nao_ticket, sem_eixo = [], [], [], []
    marcadas = 0

    for caminho, prefixo in ficheiros_alvo():
        try:
            with open(caminho, encoding="utf-8") as fh:
                linhas = fh.read().split("\n")
        except (OSError, UnicodeDecodeError):
            continue
        for bloco in blocos_logicos(linhas, prefixo):
            for idx, (n, conteudo) in enumerate(bloco):
                for m in RE_MARCADOR.finditer(conteudo):
                    if not abre_declaracao(conteudo, m.start()):
                        continue
                    texto = conteudo.strip()[:120]
                    tipo, valor = eixo_do_marcador(conteudo, m.end(), bloco[idx + 1:])
                    if tipo == "nenhum":
                        sem_eixo.append((rel(caminho), n, texto))
                        continue
                    if tipo == "outro":
                        eixo_nao_ticket.append((rel(caminho), n, valor))
                        continue
                    for ticket in valor:
                        marcadas += 1
                        estado = estados.get(ticket, "__INEXISTENTE__")
                        alvo = (rel(caminho), n, ticket, texto)
                        if estado == "__INEXISTENTE__":
                            desconhecidos.append(alvo)
                        elif estado is None:
                            sem_estado.append(alvo)
                        elif estado in FECHADO:
                            caducadas.append(alvo + (estado,))
                        elif estado in CANCELADO:
                            canceladas.append(alvo + (estado,))
                        elif estado not in CONHECIDOS:
                            # Um lexema NOVO continua a NÃO avermelhar — inventar que uma palavra
                            # desconhecida significa «fechado» avermelharia o gate por uma mudança
                            # de redacção. Mas passa a ser IMPRESSO: a versão anterior calava-se, e
                            # foi assim que `**Implementado**` virou o lexema `I` e desapareceu.
                            lexema_novo.append(alvo + (estado,))

    rc = 0

    def falha(titulo, itens, com_estado=True):
        print(titulo, file=sys.stderr)
        for it in itens:
            cauda = " esta %s" % it[4] if com_estado else ""
            print("       %s:%d — %s%s\n         %s" % (it[0], it[1], it[2], cauda, it[3]), file=sys.stderr)

    if caducadas:
        rc = 1
        falha("FAIL estado-citado: declaracao nomeia como BLOQUEADOR um ticket ja FECHADO:", caducadas)
    if canceladas:
        rc = 1
        falha("FAIL estado-citado: declaracao espera por um ticket REMOVIDO (que nunca vai aterrar):", canceladas)
    if desconhecidos:
        rc = 1
        falha("FAIL estado-citado: BLOQUEADOR nomeia um ticket que NAO EXISTE no backlog:", desconhecidos, False)

    # A CEGUEIRA É IMPRESSA, sempre, e agora em QUATRO eixos e não num. Um gate que se cala sobre o
    # que não sabe verificar sugere uma cobertura que não tem — é o molde das «abstenções» do
    # `ref-lint`. Cada linha destas é um sítio onde o gate VIU um marcador e NÃO o julgou.
    abstencoes = len(sem_estado) + len(lexema_novo) + len(eixo_nao_ticket) + len(sem_eixo)
    print("estado-citado: %d declaracao(oes) com BLOQUEADOR verificada(s); %d abstencao(oes)"
          % (marcadas, abstencoes))
    for f, n, t, _ in sem_estado:
        print("   ABSTENCAO %s:%d — %s sem `### Estado` no corpus (EPIC-01..18 nao o tem)" % (f, n, t))
    for f, n, t, _, est in lexema_novo:
        print("   ABSTENCAO %s:%d — %s diz `%s`, lexema fora do vocabulario conhecido" % (f, n, t, est))
    for f, n, v in eixo_nao_ticket:
        print("   ABSTENCAO %s:%d — o eixo e `%s`, que nao e um ticket AOS (e do `deferrals`)" % (f, n, v))
    for f, n, _ in sem_eixo:
        print("   ABSTENCAO %s:%d — marcador BLOQUEADOR sem eixo nenhum a seguir" % (f, n))
    if rc == 0:
        print("OK: nenhuma declaracao marcada como BLOQUEADOR cita um ticket ja fechado.")
    return rc


if __name__ == "__main__":
    sys.exit(main())
