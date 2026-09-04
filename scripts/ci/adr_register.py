#!/usr/bin/env python3
"""
adr_register.py — O registo canónico de ADRs, DERIVADO de `docs/adr/README.md`.

QUAL É A FONTE AUTORITATIVA, e porquê esta (AOS-317). Três sítios do corpus
enumeram ADRs e os três divergem entre si:

  - `_BRIEF.md` §3 pára em ADR-014, hoje por ÂMBITO DECLARADO: fixa o enunciado
    do núcleo fundacional e remete o inventário para o registo (AOS-317);
  - `specs/00_System_Spec.md` §11 esteve parada em ADR-019 e foi completada em
    2026-09-04, mas continua sem estado: e enunciado, nao inventario;
  - `docs/adr/README.md` vai em ADR-023, com o ESTADO de cada decisão.

Os dois primeiros são, pela política escrita no próprio README, **referência de
enunciado**: catálogo histórico que materializar um ADR «não revoga» e que, por
desenho, não cresce com os ADRs novos. O terceiro declara-se «registo canónico»,
é o único completo e o único que diz se uma decisão está em vigor. É ele a fonte.

O DEFEITO QUE ESTE MÓDULO FECHA. `ADR_RANGE` era um LITERAL escrito à mão, e
vivia em DOIS ficheiros — `rtm-regenerate.py` e `ref-lint.py`. Envelheceram
juntos: enquanto o registo ia em ADR-023, o canon parou em `range(1, 20)`, a §4
da RTM afirmava «19/19 ADRs» a setenta linhas de uma §7 que dizia «20/20», e o
`ref-lint` deixava de exigir ticket implementador a quatro ADRs sem que nada
avermelhasse — verde por não olhar.

AOS-314 corrigiu o número (`range(1, 24)`) e fechou o sintoma. Isto fecha a
causa: um literal novo é um literal, e no dia do ADR-024 o canon volta a ficar
curto nos mesmos dois sítios. Uma constante duplicada que ninguém compara com a
fonte é a mesma classe de defeito que a §6 tinha antes de AOS-312 — uma máquina
a afirmar com autoridade o que nunca leu. Por isso a derivação vive AQUI, num
só sítio, e não em cópias.
"""

import re
from collections import namedtuple
from pathlib import Path

Adr = namedtuple("Adr", "code title state")

# Vocabulário FECHADO de estados, tal como o README os enumera na secção
# «Processo (convenção desta pasta)». Fechado de propósito: um estado novo tem
# de passar por aqui — e por quem lê este ficheiro — em vez de entrar por
# escrita livre numa célula de tabela e ser propagado em silêncio para a RTM.
# A ordem importa: o prefixo mais longo primeiro, para que «Ratificado» não
# capture uma célula que começa por «Referenciado/Recomendado…».
STATES = (
    "Referenciado/Recomendado em auditoria, por materializar",
    "Catálogo, por materializar",
    "Substituído por",
    "Ratificado",
    "Proposto",
    "Aceite",
)

# Cabeçalho da tabela do registo. Ancorar no cabeçalho — e não em «qualquer
# linha que comece por | ADR-» — evita apanhar as tabelas de outros documentos
# se a fonte um dia mudar de sítio, e falha ruidosamente em vez de devolver
# menos ADRs do que existem.
_HEADER = "| Código | Título | Estado | Ficheiro |"


class RegisterError(Exception):
    """Erro de leitura do registo. Quem chama traduz para exit != 0."""


def _cells(line: str) -> list:
    """Células de uma linha de tabela markdown, sem os delimitadores externos."""
    return [c.strip() for c in line.strip().strip("|").split("|")]


def _normalise_state(raw: str, code: str) -> str:
    """
    Reduz a célula de estado ao estado canónico, descartando os qualificadores
    que o README lhe acrescenta («**Ratificado (AOS-176)**», «**Ratificado
    (2026-08-30) e ASSINADO (2026-08-31, AOS-281)**»). O qualificador é
    proveniência e vive no registo; a matriz precisa da classe.
    """
    text = raw.replace("*", "").strip()
    for state in STATES:
        if text.startswith(state):
            return state
    raise RegisterError(
        f"estado desconhecido em {code}: «{raw}». Os estados admitidos são: "
        + " · ".join(STATES)
    )


def read_register(repo_root: Path) -> list:
    """
    Devolve [Adr(code, title, state), ...] por ordem crescente de código.

    FAIL-CLOSED em três eixos, porque tudo o que se segue (a §4 da RTM, a
    invariante «todo o ADR tem ≥1 ticket» do ref-lint) assume os três:
      1. a tabela existe e tem linhas;
      2. os códigos são contíguos de ADR-001 até ao maior — um buraco significa
         registo corrompido ou código reutilizado, e a numeração do README
         promete explicitamente que «códigos nunca são reutilizados»;
      3. cada estado pertence ao vocabulário fechado.
    """
    path = repo_root / "docs" / "adr" / "README.md"
    if not path.exists():
        raise RegisterError(f"registo de ADRs não encontrado: {path}")

    lines = path.read_text(encoding="utf-8").splitlines()
    try:
        start = lines.index(_HEADER)
    except ValueError:
        raise RegisterError(
            f"não encontrou o cabeçalho «{_HEADER}» em {path} — a tabela do "
            "registo mudou de forma e a derivação ficaria silenciosamente curta"
        )

    entries = []
    for line in lines[start + 1:]:
        if not line.startswith("|"):
            break  # fim da tabela
        cells = _cells(line)
        if not cells or not re.fullmatch(r"ADR-\d{3}", cells[0]):
            continue  # separador `|---|` ou linha de continuação
        if len(cells) < 3:
            raise RegisterError(f"linha do registo com colunas a menos: {line}")
        entries.append(Adr(cells[0], cells[1], _normalise_state(cells[2], cells[0])))

    if not entries:
        raise RegisterError(f"a tabela do registo em {path} não tem nenhuma linha ADR-NNN")

    entries.sort(key=lambda a: int(a.code.split("-")[1]))
    nums = [int(a.code.split("-")[1]) for a in entries]
    if nums != list(range(1, len(nums) + 1)):
        raise RegisterError(
            "registo de ADRs descontínuo ou com códigos repetidos: "
            + ", ".join(a.code for a in entries)
        )
    return entries


def adr_codes(repo_root: Path) -> list:
    """Só os códigos, na ordem do registo — o que substitui o antigo `ADR_RANGE`."""
    return [a.code for a in read_register(repo_root)]
