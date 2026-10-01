#!/usr/bin/env python3
"""
adr_citacoes.py — O que um bloco de ticket IMPLEMENTA e o que só MENCIONA (AOS-318).

O DEFEITO. A §4 da RTM e a verificação 2 do `ref-lint` («todo o ADR do canon tem
≥ 1 ticket implementador») liam os ADRs de um ticket por correspondência textual:
cada `ADR-NNN` no bloco era uma ALEGAÇÃO de implementação. Citar uma decisão para
a discutir, para delimitar âmbito ou para dizer que é uma restrição era, por isso,
alegar que se entrega.

O MEIO-CAMINHO QUE JÁ EXISTIA. AOS-313 introduziu `<!-- rtm: adrs-mencionados -->`,
que declara o bloco INTEIRO como menção. Resolve o ticket que fala de ADRs e não
implementa nenhum, e só esse: é tudo-ou-nada. Um ticket que implementa o ADR-028 e
tem de nomear o ADR-018 como restrição tinha de escolher entre perder a cobertura
do primeiro ou inventar a do segundo — e o corpus escolheu a segunda, por escrito,
em AOS-417, AOS-423, AOS-424, AOS-427 e AOS-430 («o parser é textual e o marcador
é tudo-ou-nada: não há forma de separar os dois papéis no mesmo bloco»).

O MECANISMO (marcador inline, aditivo). Duas formas, ambas comentários HTML —
invisíveis no Markdown renderizado — e só estas, escritas EXACTAMENTE assim (espaços
e tabs à volta são livres; maiúsculas, acentos e pontuação não):

  <!-- rtm: adrs-mencionados -->      o bloco inteiro é menção (forma de AOS-313)
  <!-- rtm: menção --> … <!-- /rtm: menção -->
                                       só o TRECHO entre os dois é menção; um ADR
                                       citado também FORA do trecho continua a ser
                                       implementado

Não há alias sem acento: `mencao` é erro, como qualquer outra grafia. O texto é
normalizado para NFC antes de comparar, pelo que um «ç» decomposto (NFD) conta como
o composto — é o mesmo carácter para quem o lê.

Porquê este e não os outros dois candidatos do ticket, medido sobre o corpus de
2026-10-01 (445 pares (ticket, ADR) na §4): o marcador inline não desloca NENHUM
par existente, porque nenhum bloco o usa até alguém o escrever; «semântica por
secção» (só os Critérios de Aceitação alegam) deslocaria 332 dos 445; um «campo
explícito» (a linha `Documentos de referência` da tabela do ticket como fonte
única) deslocaria 214. Método e variantes na Entrega do AOS-318. O custo do inline
é o oposto: as atribuições falsas que já estão escritas ficam até alguém as reler
e marcar — é opt-in.

FAIL-CLOSED. Todo o comentário HTML cujo conteúdo comece por `rtm` ou `/rtm` —
sem distinguir maiúsculas, com ou sem espaços, com ou sem dois-pontos, com hífenes
a mais na abertura — é uma directiva CANDIDATA, e uma candidata que não seja uma
das formas canónicas é ERRO. Também é erro um trecho aberto sem fecho, um fecho
sem abertura, duas aberturas encadeadas e uma forma de fecho do marcador de bloco.
Uma gralha ignorada devolvia o ADR à coluna de implementadores em silêncio — o
defeito que isto existe para fechar, a entrar pela porta do lado (foi o que a
revisão adversarial de 50ef14d mostrou: `RTM:`, `rtm :`, `rtm menção` e `<!---`
passavam como texto). Um comentário que comece por OUTRA coisa («<!-- O marcador
`rtm: …` SAIU -->») é prosa e não é lido.

CÓDIGO NÃO É DIRECTIVA. O que está dentro de código é texto — é o que permite a um
ticket documentar o mecanismo sem o accionar. A varredura segue o CommonMark
nestes pontos, e só nestes:

  - blocos cercados: abertura com ≤ 3 espaços de indentação e ≥ 3 crases ou tis; com
    crases, a info string não pode ter crases (senão é código em linha, não cerca);
    fecho com o MESMO carácter, comprimento ≥ ao da abertura, ≤ 3 espaços e nada
    depois além de espaços; sem fecho, o bloco cercado vai até ao fim do texto;
  - código em linha: uma sequência de N crases abre, e fecha na próxima sequência de
    EXACTAMENTE N crases; pode atravessar linhas, mas não uma linha em branco nem o
    início de um bloco cercado; sem fecho, as crases são literais;
  - comentários HTML: `<!--` até ao primeiro `-->`; o que está lá dentro não abre
    código, e um `<!--` dentro de código não abre comentário.

APROXIMAÇÕES DECLARADAS (nenhuma tem caso no corpus de 2026-10-01): blocos de
código INDENTADOS (4 espaços) não são reconhecidos — no corpus, indentação de 4+ é
continuação de item de lista; cercas dentro de itens de lista com 4+ espaços de
indentação também não; crases escapadas (`\\``) contam como crases; e um cabeçalho
ou outro bloco que não seja linha em branco ou cerca não interrompe código em
linha. Em todos, o erro vai para o lado de LER uma directiva onde o CommonMark
veria texto — que, sozinha, falha fechado.

Os códigos `ADR-NNN` dentro de código continuam a contar, como sempre contaram.

Este módulo é importado por `rtm-regenerate.py` E por `ref-lint.py`: os dois
leitores do corpus não podem discordar sobre o que um ticket implementa, e duas
cópias desta regra envelheceriam em separado (o mesmo raciocínio de
`adr_register.py`). Também lhes dá `blocos_cercados`, a mesma detecção de cercas
de que dependem para delimitar o bloco de um ticket (`mascarar_fences`), e
`verificar_cercas` (AOS-472), que os dois chamam sobre cada `specs/EPIC-*.md` antes
de o ler: uma cerca que não feche, que atravesse o cabeçalho de outro ticket ou que
contenha uma abertura do seu próprio tipo é ERRO, porque desloca pares (ticket, ADR)
sem que a RTM regenerada deixe de bater com o corpus — nenhum gate compara o
conjunto de pares de um commit com o do anterior.
"""

import re
import unicodedata

RE_ADR = re.compile(r"ADR-\d{3}")

BLOCO = "adrs-mencionados"
TRECHO = "menção"

# Candidata: conteúdo do comentário que comece por `rtm` / `/rtm` como palavra.
_RE_CANDIDATA = re.compile(r"\s*-*\s*/?\s*rtm\b", re.IGNORECASE)
# Canónica: só estas, depois de NFC. `[ \t]` e não `\s`: um NBSP ou uma quebra de
# linha no meio da directiva também é gralha.
_RE_CANONICA = re.compile(r"[ \t]*(/?)rtm:[ \t]*(adrs-mencionados|menção)[ \t]*")

_RE_ABRE_CERCA = re.compile(r" {0,3}(`{3,}|~{3,})(.*)")
_RE_PROXIMO = re.compile(r"<!--|`|\n")
_RE_CRASES = re.compile(r"`+")


class CitacaoError(ValueError):
    """Directiva `rtm` mal escrita ou trecho de menção desequilibrado."""


def _fim_da_linha(texto: str, pos: int) -> int:
    f = texto.find("\n", pos)
    return len(texto) if f == -1 else f


def _cerca_em(texto: str, pos: int):
    """Se a linha que começa em `pos` abre um bloco cercado, devolve `(pos, fim, fechou)`, com
    `fim` no fim da linha de fecho (ou do texto) e `fechou` falso quando não há linha de fecho
    e a cerca corre até ao fim do texto. Senão, None."""
    fl = _fim_da_linha(texto, pos)
    m = _RE_ABRE_CERCA.fullmatch(texto, pos, fl)
    if not m:
        return None
    marca, info = m.group(1), m.group(2)
    if marca[0] == "`" and "`" in info:
        return None  # ```x``` é código em linha, não cerca
    fecho = re.compile(r" {0,3}%s{%d,}[ \t]*" % (re.escape(marca[0]), len(marca)))
    p = fl + 1
    while p <= len(texto):
        f = _fim_da_linha(texto, p)
        if fecho.fullmatch(texto, p, f):
            return (pos, f, True)
        if f >= len(texto):
            break
        p = f + 1
    return (pos, len(texto), False)


def _interrompe_paragrafo(conteudo: str) -> bool:
    """Uma linha em branco ou o início de uma cerca acabam o parágrafo, e com ele qualquer
    código em linha que tentasse atravessá-los."""
    if re.search(r"\n[ \t]*\n", conteudo):
        return True
    return bool(re.search(r"\n {0,3}(```|~~~)", conteudo))


def _varrer(texto: str):
    """Uma passagem da esquerda para a direita. Devolve `(cercas, comentarios)`:
    `cercas` = [(ini, fim, fechou)] dos blocos cercados; `comentarios` = [(ini, fim)] dos
    comentários HTML FORA de código (`ini` no `<`, `fim` depois do `-->`)."""
    n = len(texto)
    cercas, comentarios = [], []
    pos = 0
    while pos < n:
        if pos == 0 or texto[pos - 1] == "\n":
            c = _cerca_em(texto, pos)
            if c:
                cercas.append(c)
                pos = c[1]
                continue
        m = _RE_PROXIMO.search(texto, pos)
        if not m:
            break
        tok, ini = m.group(0), m.start()
        if tok == "\n":
            pos = m.end()
        elif tok == "<!--":
            f = texto.find("-->", ini + 4)
            fim = n if f == -1 else f + 3
            comentarios.append((ini, fim))
            pos = fim
        else:
            k = len(_RE_CRASES.match(texto, ini).group(0))
            fecho = None
            for r in _RE_CRASES.finditer(texto, ini + k):
                if len(r.group(0)) == k:
                    fecho = r
                    break
            if fecho and not _interrompe_paragrafo(texto[ini + k:fecho.start()]):
                pos = fecho.end()  # código em linha: saltado
            else:
                pos = ini + k  # crases literais
    return cercas, comentarios


def blocos_cercados(texto: str) -> list:
    """[(ini, fim)] dos blocos de código cercados de `texto`, pelas regras acima."""
    return [(ini, fim) for ini, fim, _ in _varrer(texto)[0]]


# Cabeçalho de ticket, tal como os dois leitores o procuram — sobre o texto CRU, não mascarado.
_RE_CABECALHO_TICKET = re.compile(r"^#{2,3} (AOS-\d{3})\s*[-–—]", re.MULTILINE)


def verificar_cercas(texto: str, onde: str) -> None:
    """
    Guarda de AOS-472: levanta `CitacaoError` se uma cerca de `texto` (um `specs/EPIC-*.md`
    inteiro) puder estar a deslocar pares (ticket, ADR) sem que nenhum gate dê por isso.

    Uma linha de prosa que comece por três crases ou três tis abre, pelo CommonMark, uma
    cerca que só fecha numa linha do mesmo carácter. Tudo o que fica lá dentro deixa de ser
    lido como directiva, e os `#` lá dentro deixam de terminar blocos — um ticket absorve o
    seguinte, ou uma menção volta a implementação. A RTM regenerada fica sincronizada com o
    corpus mal lido, e o `rtm.sh` verde. A invariante que se impõe é a de que **uma cerca
    nunca atravessa a fronteira de um ticket**, em três condições, todas medidas a zero no
    corpus de 2026-10-01 (212 cercas):

      1. toda a cerca fecha — sem linha de fecho, corre até ao fim do ficheiro e engole a
         fronteira de todos os tickets que se lhe seguem (ou, no último, a prosa de cauda);
      2. nenhuma cerca contém um cabeçalho `## AOS-NNN —` / `### AOS-NNN —`: os leitores
         acham os cabeçalhos no texto cru e os terminadores no mascarado, pelo que essa
         linha seria ao mesmo tempo um ticket novo e não-fronteira do anterior;
      3. nenhuma cerca contém uma linha que, fora dela, ABRIRIA uma cerca do mesmo carácter
         (comprimento ≥ ao da abertura e com info string — «```bash» dentro de uma cerca de
         três crases). É o sinal de dessincronização: o autor julgava estar fora de código.
         Mostrar uma cerca dentro de outra faz-se com uma abertura mais comprida (quatro
         crases à volta de três) ou com o outro carácter, e isso continua permitido. Sem
         esta condição, uma linha solta de três crases emparelhava com o fecho da cerca
         legítima seguinte do mesmo bloco, ficava fechada sem atravessar nada, e escondia
         como código as directivas que houvesse entre as duas.

    O que fica de fora, declarado: duas linhas soltas que emparelhem uma com a outra dentro do
    mesmo ticket são, para qualquer leitor, uma cerca legítima.
    """
    cabecalhos = [(m.start(), m.group(1)) for m in _RE_CABECALHO_TICKET.finditer(texto)]
    problemas = []

    def linha(pos: int) -> int:
        return texto.count("\n", 0, pos) + 1

    for ini, fim, fechou in _varrer(texto)[0]:
        fl = _fim_da_linha(texto, ini)
        marca = _RE_ABRE_CERCA.fullmatch(texto, ini, fl).group(1)
        rotulo = "a cerca «%s» aberta na linha %d" % (marca, linha(ini))
        dentro = [(p, aos) for p, aos in cabecalhos if ini < p < fim]
        if dentro:
            p, aos = dentro[0]
            problemas.append(
                "%s atravessa o cabeçalho de %s (linha %d) — os dois tickets deixam de ter "
                "fronteira" % (rotulo, aos, linha(p))
            )
        if not fechou:
            problemas.append(
                "%s nunca fecha e corre até ao fim do ficheiro" % rotulo
            )
        abre_igual = re.compile(r" {0,3}%s{%d,}(.*)" % (re.escape(marca[0]), len(marca)))
        fim_interior = fim if not fechou else texto.rfind("\n", ini, fim)
        p = fl + 1
        while p < fim_interior:
            f = min(_fim_da_linha(texto, p), fim_interior)
            m = abre_igual.fullmatch(texto, p, f)
            if m and m.group(1).strip() and not (marca[0] == "`" and "`" in m.group(1)):
                problemas.append(
                    "%s contém na linha %d uma abertura do mesmo tipo («%s») — a cerca está "
                    "dessincronizada: uma linha solta abriu-a antes do tempo"
                    % (rotulo, linha(p), texto[p:f].strip())
                )
                break
            p = f + 1
    if problemas:
        raise CitacaoError(
            "%s: cerca de código atravessa a fronteira de um ticket (AOS-472) — %s. Uma "
            "linha de prosa que comece por ``` ou ~~~ abre uma cerca pelo CommonMark; "
            "reescreva-a ou feche a cerca dentro do mesmo ticket"
            % (onde, "; ".join(problemas))
        )


def classificar(bloco: str, onde: str = "bloco") -> tuple:
    """
    Devolve `(implementa, mencionados)`, dois conjuntos disjuntos de `ADR-NNN`.

    `implementa` é o que entra na coluna da §4 e conta para a invariante
    «≥ 1 ticket implementador»; `mencionados` é o que o bloco cita SÓ como menção.
    Levanta `CitacaoError` (com `onde` na mensagem) se as directivas estiverem
    mal formadas.
    """
    bloco_inteiro = False
    trechos = []  # [(inicio, fim)] em offsets de `bloco`
    aberto = None
    for ini, fim in _varrer(bloco)[1]:
        conteudo = bloco[ini + 4:fim - 3] if bloco.endswith("-->", 0, fim) else bloco[ini + 4:fim]
        if not _RE_CANDIDATA.match(conteudo):
            continue  # comentário de prosa
        m = _RE_CANONICA.fullmatch(unicodedata.normalize("NFC", conteudo))
        if not m:
            raise CitacaoError(
                "%s: directiva desconhecida ou mal escrita «%s» — as únicas formas são "
                "«<!-- rtm: adrs-mencionados -->» e o par «<!-- rtm: menção -->» / "
                "«<!-- /rtm: menção -->», em minúsculas e com dois-pontos"
                % (onde, bloco[ini:fim].strip())
            )
        fecho, nome = m.group(1) == "/", m.group(2)
        if nome == BLOCO:
            if fecho:
                raise CitacaoError(
                    "%s: «<!-- /rtm: adrs-mencionados -->» não existe — o marcador de bloco "
                    "não se fecha; para um trecho use «<!-- rtm: menção -->»" % onde
                )
            bloco_inteiro = True
        elif not fecho:
            if aberto is not None:
                raise CitacaoError(
                    "%s: «<!-- rtm: menção -->» aberto dentro de outro trecho de menção "
                    "ainda por fechar" % onde
                )
            aberto = fim
        else:
            if aberto is None:
                raise CitacaoError(
                    "%s: «<!-- /rtm: menção -->» fecha um trecho de menção que nunca "
                    "abriu" % onde
                )
            trechos.append((aberto, ini))
            aberto = None
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
