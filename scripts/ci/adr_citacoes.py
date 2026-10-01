#!/usr/bin/env python3
"""
adr_citacoes.py — O que um bloco de ticket IMPLEMENTA e o que só MENCIONA (AOS-318).

O DEFEITO. A §4 da RTM e a verificação 2 do `ref-lint` («todo o ADR do canon tem
≥ 1 ticket implementador») liam os ADRs de um ticket por correspondência textual:
cada `ADR-NNN` no bloco era uma ALEGAÇÃO de implementação. Citar uma decisão para
a discutir, para delimitar âmbito ou para dizer que é uma restrição era, por isso,
reclamar que se a entrega.

O MEIO-CAMINHO QUE JÁ EXISTIA. AOS-313 introduziu `<!-- rtm: adrs-mencionados -->`,
que declara o bloco INTEIRO como menção. Resolve o ticket que fala de ADRs e não
implementa nenhum, e só esse: é tudo-ou-nada. Um ticket que implementa o ADR-028 e
tem de nomear o ADR-018 como restrição tinha de escolher entre perder a cobertura
do primeiro ou inventar a do segundo — e o corpus escolheu a segunda, por escrito,
em AOS-417, AOS-423, AOS-424, AOS-427 e AOS-430 («o parser é textual e o marcador
é tudo-ou-nada: não há forma de separar os dois papéis no mesmo bloco»).

O MECANISMO (marcador inline, aditivo). Duas formas, ambas comentários HTML —
invisíveis no Markdown renderizado:

  <!-- rtm: adrs-mencionados -->      o bloco inteiro é menção (forma de AOS-313,
                                       inalterada)
  <!-- rtm: menção --> … <!-- /rtm: menção -->
                                       só o TRECHO entre os dois é menção; um ADR
                                       citado também FORA do trecho continua a ser
                                       implementado

Porquê este e não os outros dois candidatos do ticket, medido sobre o corpus de
2026-10-01 (445 pares (ticket, ADR) na §4): o marcador inline não desloca NENHUM
par existente, porque nenhum bloco o usa até alguém o escrever; «semântica por
secção» (só os Critérios de Aceitação alegam) deslocaria 332 dos 445; um «campo
explícito» (a linha `Documentos de referência` da tabela do ticket como fonte
única) deslocaria 214. O custo do inline é o oposto: as atribuições falsas que já
estão escritas ficam até alguém as reler e marcar — é opt-in.

FAIL-CLOSED. Uma directiva `rtm:` que não seja uma das três acima, um trecho
aberto sem fecho, um fecho sem abertura, ou duas aberturas encadeadas são ERRO,
não silêncio: um marcador mal escrito que fosse ignorado devolvia o ADR à coluna
de implementadores sem ninguém dar por isso — o defeito que isto existe para
fechar, a entrar pela gralha.

Directivas dentro de código (`…` em linha ou blocos cercados) são TEXTO, não
directivas — é o que o CommonMark diz, e é o que permite a um ticket documentar o
mecanismo sem o accionar. Os códigos `ADR-NNN` dentro de código continuam a
contar, como sempre contaram.

Este módulo é importado por `rtm-regenerate.py` E por `ref-lint.py`: os dois
leitores do corpus não podem discordar sobre o que um ticket implementa, e duas
cópias desta regra envelheceriam em separado (o mesmo raciocínio de
`adr_register.py`).
"""

import re

RE_ADR = re.compile(r"ADR-\d{3}")

# Qualquer comentário HTML que comece por `rtm:` ou `/rtm:` é uma directiva para
# os leitores do corpus — conhecida ou não.
_RE_DIRECTIVA = re.compile(r"<!--\s*(/?)rtm:\s*(.*?)\s*-->", re.DOTALL)

BLOCO = "adrs-mencionados"
TRECHO = ("menção", "mencao")  # com e sem acento: o corpus escreve das duas formas

_RE_FENCE = re.compile(r"^[ \t]*(```|~~~)")
_RE_CODIGO_EM_LINHA = re.compile(r"`[^`\n]*`")


class CitacaoError(ValueError):
    """Directiva `rtm:` desconhecida ou trecho de menção desequilibrado."""


def _sem_codigo(texto: str) -> str:
    """`texto` com o MESMO comprimento, com o conteúdo de código (cercado e em
    linha) trocado por espaços. Os offsets continuam válidos sobre o original."""
    linhas = texto.split("\n")
    dentro = False
    for i, ln in enumerate(linhas):
        if _RE_FENCE.match(ln):
            dentro = not dentro
            linhas[i] = " " * len(ln)
            continue
        if dentro:
            linhas[i] = " " * len(ln)
        else:
            linhas[i] = _RE_CODIGO_EM_LINHA.sub(lambda m: " " * len(m.group(0)), ln)
    return "\n".join(linhas)


def classificar(bloco: str, onde: str = "bloco") -> tuple:
    """
    Devolve `(implementa, mencionados)`, dois conjuntos disjuntos de `ADR-NNN`.

    `implementa` é o que entra na coluna da §4 e conta para a invariante
    «≥ 1 ticket implementador»; `mencionados` é o que o bloco cita SÓ como menção.
    Levanta `CitacaoError` (com `onde` na mensagem) se as directivas estiverem
    mal formadas.
    """
    limpo = _sem_codigo(bloco)
    bloco_inteiro = False
    trechos = []  # [(inicio, fim)] em offsets de `bloco`
    aberto = None
    for m in _RE_DIRECTIVA.finditer(limpo):
        fecho, nome = m.group(1) == "/", m.group(2)
        if nome == BLOCO and not fecho:
            bloco_inteiro = True
        elif nome in TRECHO and not fecho:
            if aberto is not None:
                raise CitacaoError(
                    "%s: «<!-- rtm: %s -->» aberto dentro de outro trecho de menção "
                    "ainda por fechar" % (onde, nome)
                )
            aberto = m.end()
        elif nome in TRECHO and fecho:
            if aberto is None:
                raise CitacaoError(
                    "%s: «<!-- /rtm: %s -->» fecha um trecho de menção que nunca "
                    "abriu" % (onde, nome)
                )
            trechos.append((aberto, m.start()))
            aberto = None
        else:
            raise CitacaoError(
                "%s: directiva desconhecida «%s» — as conhecidas são "
                "«<!-- rtm: adrs-mencionados -->» e o par «<!-- rtm: menção -->» / "
                "«<!-- /rtm: menção -->»" % (onde, m.group(0).strip())
            )
    if aberto is not None:
        raise CitacaoError(
            "%s: trecho de menção aberto e nunca fechado — sem o fecho, tudo o que "
            "se lhe segue deixaria de alegar implementação" % onde
        )

    todos = set(RE_ADR.findall(bloco))
    if bloco_inteiro:
        return set(), todos
    implementa = set()
    for m in RE_ADR.finditer(bloco):
        if not any(i <= m.start() < f for i, f in trechos):
            implementa.add(m.group(0))
    return implementa, todos - implementa
