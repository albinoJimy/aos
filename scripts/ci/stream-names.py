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
S2. **Representabilidade.** Nenhum valor pode conter um carácter que o contrato do
    Event Store recusa. **A regra é LIDA DA FONTE** — a constante
    `eventstore.CaracteresNaoRepresentaveis`, que é a declaração canónica desde o
    aperto do contrato (AOS-424, decisão 1) e que o backend JetStream também usa —
    e não duplicada aqui: duplicá-la daria um gate verde no dia em que a regra
    apertasse. A extracção tem âncora e piso (ver S4).
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
FONTE_DA_REGRA = PACKAGES / "substrate" / "eventstore" / "stream_id.go"
BASELINE = Path(
    os.environ.get("AOS_STREAM_NAMES_BASELINE")
    or (Path(__file__).resolve().parent / "baseline" / "stream-names.txt")
)

# PISO MÍNIMO da regra. A extracção lê a regra da fonte, mas «ler da fonte» não basta:
# uma revisão adversarial provou que bastava acrescentar a `store.go`, ACIMA do `subjectDe`,
# um segundo `ContainsAny(streamID, ".")` — um helper plausível — para o gate passar a medir
# só o ponto e continuar VERDE com nomes contendo espaço e `>` na árvore.
#
# O piso fecha isso: a regra extraída tem de ser um SUPERCONJUNTO deste conjunto. A fonte é o
# TECTO (se apertar, o gate aperta com ela) e isto é o CHÃO (se a extracção enfraquecer, o gate
# FALHA em vez de medir menos). Um valor de controlo único não servia: só exercitava o `.` e
# deixava passar uma extracção que tivesse perdido os outros seis.
PISO_DA_REGRA = ". *>\t\r\n"

# Declaração de um nome de stream: `const nomeStream = …` / `nomeStream := …` / `var …`.
#
# A versão anterior exigia que a linha TERMINASSE na string, e isso deixava passar a forma que
# produziu quatro dos nomes deste próprio ticket: a CONCATENAÇÃO
# (`streamPrefix + string(class)`). Uma revisão adversarial classificou-a como a evasão mais
# plausível de todas, e com razão — é o idioma que já está na árvore.
#
# Agora casa-se só o INÍCIO da declaração e inspeccionam-se TODOS os literais da linha
# ([RE_LITERAL]), pelo que `"aos-internal/" + "memory.semantic"` é apanhado nos dois pedaços.
RE_CONST = re.compile(
    r'^[ \t]*(?:const[ \t]+|var[ \t]+)?([A-Za-z0-9_]*[Ss]tream[A-Za-z0-9_]*)[ \t]*(?::?=)[ \t]'
)
# Um literal Go entre aspas, PRESERVANDO os escapes (`\t`, `\r`, `\n`) para depois os decodificar.
# A versão anterior excluía a barra invertida do valor, pelo que uma constante com `\t`/`\r`/`\n`
# — três dos sete caracteres que a regra proíbe — não casava, não era contada e nunca era
# acusada. Eram indetectáveis por construção.
RE_LITERAL = re.compile(r'"((?:[^"\\\n]|\\.)*)"')
# Literal raw (backtick): pode conter um TAB ou uma nova linha REAIS.
RE_RAW = re.compile(r"`([^`]*)`")
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
#
# `Err*` entrou pela mesma razão, e foi medido: ao alargar a regex para ver todos os
# literais de uma linha (para apanhar concatenações), apareceram `ErrStreamNotFound`,
# `ErrForeignStream` e `ErrTruncatedStream` — MENSAGENS de erro, prosa com espaços, cujo
# identificador contém «Stream». A convenção Go reserva o prefixo `Err` para valores de
# erro, o que torna o filtro razoavelmente seguro; um nome de stream chamado `ErrX` seria
# um nome infeliz, e ficaria invisível a este gate.
RE_ATRIBUTO = re.compile(r"^(?:Attr|Atributo|Err)")
# Argumento de stream LITERAL no ponto de uso: `.Append(ctx, "x"` / `.Read(ctx, "x"`.
#
# O primeiro argumento aceita PARÊNTESES. A versão anterior usava `[^,)]+`, que só casava uma
# variável nua (`ctx`) — e por isso NÃO via `Append(r.Context(), "gov.x", …)`, que é exactamente
# a forma que o `plan_ingress.go` usa. Uma revisão adversarial mediu-o com três sondas: só uma
# foi acusada.
RE_USO_LITERAL = re.compile(
    r'\.(?:Append|Read|StreamHead|IngestStream)\('
    r'[A-Za-z0-9_.]+(?:\([^()]*\))?[ \t]*,[ \t]*'
    r'"((?:[^"\\\n]|\\.)*)"'
)
# A regra, tal como a FONTE canónica a declara.
#
# Era extraída do `ContainsAny` do `jetstream.Store.subjectDe` — uma de TRÊS cópias da
# mesma lista. O aperto do contrato (AOS-424, decisão 1) concentrou-a em
# `eventstore.ValidarStreamID`, e este gate passou a ler de lá. Quando a mudança foi feita,
# o gate FALHOU FECHADO com «nao encontrei ContainsAny(...)» — que é o comportamento certo e
# a prova de que a âncora não é decorativa.
RE_REGRA = re.compile(r'CaracteresNaoRepresentaveis\s*=\s*"([^"]*)"')


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


def decodificar_go(valor: str) -> str:
    """
    Decodifica os escapes de uma string Go entre aspas.

    Sem isto, `"gov\\tx"` chega aqui com uma barra e um `t` — dois caracteres que a regra não
    proíbe —, e o TAB que o Go vai realmente pôr no `stream_id` passa invisível.
    """
    pares = {"t": "\t", "r": "\r", "n": "\n", "\\": "\\", '"': '"', "'": "'", "a": "\a",
             "b": "\b", "f": "\f", "v": "\v", "0": "\0"}
    saida, i = [], 0
    while i < len(valor):
        if valor[i] == "\\" and i + 1 < len(valor):
            saida.append(pares.get(valor[i + 1], valor[i + 1]))
            i += 2
        else:
            saida.append(valor[i])
            i += 1
    return "".join(saida)


def autoteste() -> str:
    """
    Verifica que as regex do gate apanham as formas que DEVEM apanhar, e ignoram as que não.

    Existe porque S3 não tinha piso: com `0 literal(is) verificados` na saída, uma regex
    partida e uma árvore limpa eram indistinguíveis. É o mesmo raciocínio de S1 (zero
    constantes ⇒ falha), aplicado ao resto do parser. Devolve "" quando está tudo bem.
    """
    deve_casar_uso = [
        '\ts.Append(ctx, "gov.x", nil)',
        '\ts.Append(context.Background(), "gov.x", nil)',
        '\th.node.EventStore.Append(r.Context(), "gov.x", eventstore.EventInput{',
        '\tevs, err := s.Read(ctx, "gov.x", 1)',
    ]
    for linha in deve_casar_uso:
        if not RE_USO_LITERAL.search(linha):
            return f"RE_USO_LITERAL nao casou uma forma que devia: {linha.strip()!r}"

    deve_casar_const = [
        '\tconst nomeStream = "gov.x"',
        '\tstreamPrefix = "memory."',
        '\tKnowledgeStreamID = "a.b"',
        '\tconst streamX = "aos-internal/" + "memory.semantic"',
        '\tvar streamY = "gov.y"',
    ]
    for linha in deve_casar_const:
        if not RE_CONST.match(linha):
            return f"RE_CONST nao casou uma declaracao que devia: {linha.strip()!r}"

    # E os escapes têm de ser decodificados, senão três dos sete caracteres da regra são
    # indetectáveis por construção.
    if decodificar_go("gov\\tx") != "gov\tx":
        return "decodificar_go nao decodifica \\t"

    # CONTROLO NEGATIVO: uma chave de span não pode ser tratada como nome de stream.
    if not RE_ATRIBUTO.match("AtributoStream"):
        return "RE_ATRIBUTO deixou de filtrar uma chave de span"
    return ""


def carregar_regra() -> str:
    """Lê da FONTE o conjunto de caracteres que o `subjectDe` recusa."""
    if not FONTE_DA_REGRA.exists():
        print(f"ERRO: nao encontrei a fonte da regra em {FONTE_DA_REGRA}", file=sys.stderr)
        print("      sem ela este gate nao tem o que impor. Fail-closed.", file=sys.stderr)
        return ""
    fonte = FONTE_DA_REGRA.read_text(encoding="utf-8")
    # ANCORA A EXTRACÇÃO NA DECLARAÇÃO CANÓNICA, e não no ficheiro. Um `search()` cego apanha a
    # primeira ocorrência que casar, que pode não ser a regra — foi assim que, na versão
    # anterior, um segundo `ContainsAny` acima do `subjectDe` fazia o gate medir só o ponto.
    i = fonte.find("const CaracteresNaoRepresentaveis")
    if i < 0:
        print(f"ERRO: nao encontrei `const CaracteresNaoRepresentaveis` em {FONTE_DA_REGRA}", file=sys.stderr)
        print("      a fonte canonica da regra mudou de nome ou de sitio. Fail-closed: sem ancora,", file=sys.stderr)
        print("      a regra extraida pode nao ser a que os backends aplicam.", file=sys.stderr)
        return ""
    m = RE_REGRA.search(fonte, i)
    if not m:
        print(f"ERRO: nao encontrei a lista de caracteres em {FONTE_DA_REGRA}", file=sys.stderr)
        print("      a fonte canonica da regra mudou de forma. Actualize este gate para ler a", file=sys.stderr)
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

    # S4 — PISO DA REGRA. A regra extraída tem de ser um SUPERCONJUNTO do piso conhecido.
    # Não é um valor de controlo: é o conjunto inteiro. Ver [PISO_DA_REGRA].
    em_falta = [ch for ch in PISO_DA_REGRA if ch not in proibidos]
    if em_falta:
        print(f"ERRO: a regra lida da fonte ({proibidos!r}) NAO cobre o piso conhecido:", file=sys.stderr)
        print(f"      faltam {em_falta!r}.", file=sys.stderr)
        print("      Ou a extraccao esta a ler a expressao errada (ha outro `ContainsAny` no", file=sys.stderr)
        print("      caminho?), ou o backend RELAXOU a regra. Nos dois casos este gate deixaria", file=sys.stderr)
        print("      de medir o que afirma medir, e por isso FALHA em vez de medir menos.", file=sys.stderr)
        return 1

    # S5 — AUTO-TESTE DAS REGEX. Sem isto, uma regex partida e uma árvore limpa são
    # indistinguíveis na saída: o gate imprimiria «0 literais verificados» nos dois casos.
    # É o mesmo piso fail-closed que S1 tem para as constantes, aplicado ao resto.
    if falha := autoteste():
        print(f"ERRO: auto-teste das regex do gate FALHOU: {falha}", file=sys.stderr)
        print("      um parser partido nao pode passar por «nada a verificar». Fail-closed.", file=sys.stderr)
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
            if m := RE_CONST.match(linha):
                nome = m.group(1)
                if RE_ATRIBUTO.match(nome):
                    continue  # chave de span, não nome de stream — ver [RE_ATRIBUTO]
                # TODOS os literais da linha, e não só um: é o que apanha a concatenação
                # `"prefixo/" + "nome.com.ponto"`, a evasão mais plausível de todas.
                literais = [decodificar_go(v) for v in RE_LITERAL.findall(linha)]
                literais += RE_RAW.findall(linha)  # raw: pode trazer um TAB real
                if not literais:
                    continue  # declaração sem literal nenhum (ex.: `= outraConst`)
                declaradas += 1
                for valor in literais:
                    if mau := [ch for ch in valor if ch in proibidos]:
                        achados.append((f"const|{rel}|{nome}|{valor}", rel, n,
                                        f"constante {nome} com o literal {valor!r} contem {mau!r}"))
                continue
            for u in RE_USO_LITERAL.finditer(linha):
                valor = decodificar_go(u.group(1))
                usados += 1
                if mau := [ch for ch in valor if ch in proibidos]:
                    achados.append((f"uso|{rel}|{valor}", rel, n,
                                    f"stream literal {valor!r} no ponto de uso contem {mau!r}"))

    # S6 — A COSTURA DE SEMENTE NAO TEM CHAMADORES DE PRODUCAO.
    #
    # `eventstore.SemearStreamLegado` escreve num `stream_id` que a regra RECUSA, para que um
    # teste possa construir o mundo «antes» de uma migracao. Em runtime ela recusa fora de um
    # binario de teste (`testing.Testing()`), que e a barreira que conta; esta e barata e
    # apanha a intencao antes do CI, onde o erro ainda custa um minuto.
    #
    # Sem isto, a forma obvia de «resolver» uma recusa de nome seria chamar a costura — e a
    # regra voltaria a ter um buraco, desta vez com a forma de uma API suportada.
    fora_de_teste = []
    for caminho in ficheiros_go():
        if caminho.name.endswith("_test.go"):
            continue
        rel = caminho.relative_to(RAIZ).as_posix()
        if rel.startswith("packages/substrate/eventstore/"):
            continue  # o pacote que a define
        try:
            texto = sem_comentarios(caminho.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError):
            continue
        for n, linha in enumerate(texto.splitlines(), 1):
            if "SemearStreamLegado" in linha:
                fora_de_teste.append((rel, n))

    if fora_de_teste:
        print("\nFALHA — `SemearStreamLegado` chamada de codigo que NAO e de teste:", file=sys.stderr)
        for rel, n in fora_de_teste:
            print(f"  x {rel}:{n}", file=sys.stderr)
        print("\n  Essa costura existe para um teste poder semear um nome LEGADO e provar que a", file=sys.stderr)
        print("  migracao o transporta. Em producao um `stream_id` tem de ser representavel nos", file=sys.stderr)
        print("  dois backends: renomeie o stream e migre os factos com `eventstore.CopiarStream`.", file=sys.stderr)
        return 1

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
    print("  Ambito: NAO cobre composicao em runtime — ver AOS-425. Isto NAO e teorico: o aperto")
    print("  do Append (AOS-424) revelou tres nomes COMPOSTOS irrepresentaveis e vivos no caminho")
    print("  de autorizacao (`ratify-nonce:` e `4eyes-challenge:`, alimentados por constantes de")
    print("  dominio com ponto, por um request_id de cliente, e por um separador \\x00), que este")
    print("  gate nunca poderia ter visto porque nenhum deles e um literal.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
