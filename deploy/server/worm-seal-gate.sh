#!/usr/bin/env bash
# worm-seal-gate.sh — o ÚNICO comando que a chave da selagem diária do WORM pode correr no servidor.
#
# Instalado como comando FORÇADO no authorized_keys do `aos` (ver README §8, «A tarefa diária»):
#   restrict,command="bash /opt/aos/worm-seal-gate.sh" ssh-ed25519 AAAA… aos-worm-seal
#
# ─── PORQUÊ ─────────────────────────────────────────────────────────────────────────────────
# A selagem (selar-worm.ps1) corre sozinha, todos os dias, na máquina do operador — a chave não
# pode ter passphrase. O script antigo usava a `deploy_key`, que é shell no `aos`; e o `aos` está
# no grupo docker, onde uma shell é root no servidor. É o mesmo raciocínio do backup-pull-gate.sh,
# e o mesmo desenho: a chave faz o que a tarefa precisa e mais nada.
#
# O que a tarefa precisa são três pedidos, e são os únicos que passam:
#
#   worm                                        — o worm.wal VIVO, para stdout. Sem cópia no
#                                                 servidor: o antigo `~/selo/worm.wal` e a limpeza
#                                                 que tinha de o confirmar deixam de existir.
#   scp -t /opt/aos/ancoras/.checkpoints.novo   — recebe os checkpoints com nome temporário
#   scp -t /opt/aos/pisos/.heads.novo           — recebe os pisos com nome temporário
#   trocar                                      — valida o PAR e troca-o lado a lado
#
# `restrict` fecha pty, forwarding, agente e X11. O comando forçado substitui QUALQUER pedido,
# incluindo o subsistema sftp — por SFTP a chave escreveria onde o `aos` escreve. Por isso a
# entrega usa `scp -O` (protocolo clássico), que chega aqui como `scp -t <caminho>`: um caminho
# que se pode comparar com os dois únicos permitidos.
#
# ─── O QUE QUEM LEVAR A CHAVE CONSEGUE, e é bom que fique escrito ────────────────────────────
#  · LER o WORM. É o preço da selagem por SSH (ver selar-worm.ps1 -PorSSH): trocou-se a
#    exposição da backup.key, que decifra TUDO, pela do WORM, que é metadados de governação.
#  · Repor o par EM VIGOR (um no-op) ou entregar um par mais recente ASSINADO PELA CHAVE DO
#    SELADOR — o que só consegue quem também tiver a wormseal.key.
#  · NÃO consegue: shell, SFTP, forwarding, ler o .env/secrets, escrever noutro caminho, trocar
#    um ficheiro sem o outro, repor um par ANTERIOR (a troca recusa qualquer partição que recue),
#    nem instalar um par MAL ASSINADO.
#
# ─── A ASSINATURA VERIFICA-SE AQUI, e não só no arranque ────────────────────────────────────
# A primeira versão deste gate deixava a assinatura para o nó, «que a verifica no arranque,
# fail-closed», e dizia que um par mal assinado se recuperava com uma selagem legítima. As duas
# metades estavam erradas juntas: um par BEM FORMADO com AuditSeq enormes passava a forma e a
# não-regressão, o nó abortava no restart seguinte — e a partir daí a não-regressão RECUSAVA
# todas as selagens legítimas, que traziam números menores. A recuperação exigia shell, e o `mv`
# já tinha destruído o par anterior. Agora cada checkpoint é verificado contra a
# AOS_WORM_TRUST_ANCHOR do .env — a mesma que o nó usa — ANTES da não-regressão, e o par anterior
# fica em `.anterior` ao lado do em vigor.
#
# O que continua a ser só do nó: confirmar que o EntryHash assinado corresponde ao registo real
# do WORM. Isso exige o store composto; o gate não o reproduz.
#
# ─── E A TROCA VALIDA O QUE INSTALA ─────────────────────────────────────────────────────────
# Também na primeira versão, o `receber` não usava o lock e o `trocar` validava os `.novo` no
# próprio sítio e depois fazia `mv`. Uma sessão `scp -t` parada a meio podia escrever no mesmo
# inode DEPOIS da validação, e o `mv` instalava conteúdo que ninguém validou. Agora o lock é o
# mesmo nos dois verbos (um `receber` em curso faz o `trocar` recusar), e o `trocar` COPIA os
# `.novo` para ficheiros privados — inodes novos — e valida e instala essas cópias.
#
# O que não passa é recusado e registado no syslog (`aos-worm-seal`), tal como cada leitura do
# WORM, cada troca feita e cada falha a meio. Um pedido recusado com esta chave é, por definição,
# alguém que não é a tarefa.
set -euo pipefail
umask 077

AOS_DIR=/opt/aos
ENV_FILE="${AOS_DIR}/.env"
CK="${AOS_DIR}/ancoras/checkpoints.json"
HD="${AOS_DIR}/pisos/heads.json"
CK_NOVO="${AOS_DIR}/ancoras/.checkpoints.novo"
HD_NOVO="${AOS_DIR}/pisos/.heads.novo"
LOCK="${AOS_DIR}/.worm-seal.lock"
# Fixada por DIGEST, e não por tag: esta imagem lê o volume inteiro como 65532. Com --pull=never
# o gate não a vai buscar; uma tag móvel refrescada à mão entregaria outro binário ao mesmo papel.
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
# Tecto de cada ficheiro recebido, em KiB (o `ulimit -f` do bash conta blocos de 1024). Hoje o
# checkpoints.json tem ~70 KiB para 234 partições; 16 MiB são ~50 mil. Sem tecto, a chave enchia
# o disco de um host que não é dedicado.
LIMITE_KIB=16384
PEDIDO="${SSH_ORIGINAL_COMMAND:-}"

regista() { logger -t aos-worm-seal "$1" 2>/dev/null || true; }

recusa() {
  regista "recusado: ${PEDIDO:0:200}${1:+ — $1}"
  printf 'worm-seal-gate: recusado%s — esta chave só lê o WORM e entrega a âncora\n' "${1:+ ($1)}" >&2
  exit 1
}

# Uma falha a meio (um `cp`, um `chmod`, um `mv`) sai pelo `set -e` sem passar pelo `recusa`. Sem
# isto, o caso mais grave — uma troca interrompida entre os dois `mv` — não deixava rasto no syslog.
trap 'regista "FALHOU a meio (linha ${LINENO}): ${PEDIDO:0:80}"' ERR

# O MESMO lock para receber e trocar: enquanto um ficheiro está a chegar, não se troca; enquanto
# se troca, não chega nenhum. O `exec scp` herda o descritor 9, pelo que o lock dura a transferência
# inteira. Recusa-se em vez de esperar: uma tarefa diária não fica pendurada atrás de outra.
trancar() {
  exec 9>"${LOCK}"
  flock -n 9 || recusa "outra operação da selagem em curso"
}

receber() {
  local alvo="$1"
  trancar
  # Nada pode estar já naquele nome a desviar a escrita: um directório faria o `scp -t` escrever
  # LÁ DENTRO com o nome que o cliente escolhesse, e uma ligação simbólica seria seguida.
  # `rm -f` num directório falha, e o set -e recusa.
  [[ -d "${alvo}" ]] && recusa "o destino temporário é um directório"
  rm -f -- "${alvo}"
  ulimit -f "${LIMITE_KIB}"
  exec scp -t "${alvo}"
}

case "${PEDIDO}" in
  worm)
    regista "worm lido"
    # O worm.wal é 600 do uid 65532 (distroless): lê-se COMO esse uid, e não como root. Sem rede,
    # sem capabilities, raiz só de leitura, e sem pull — um gate não vai buscar imagens à
    # Internet. `--log-driver none`: sem ele, o json-file do docker guardava uma SEGUNDA cópia do
    # WORM em /var/lib/docker até o --rm a apagar.
    #
    # Apanha-se um PREFIXO se o nó estiver a escrever — e um prefixo de hash-chain é uma cadeia
    # válida truncada: o OpenFileStore descarta um registo rasgado no fim. Não produz falsos
    # recuos, porque o WAL é append-only e o prefixo de hoje contém o de ontem.
    exec docker run --rm --pull=never --log-driver none --network none --read-only \
      --cap-drop ALL --security-opt no-new-privileges --user 65532:65532 \
      -v aos_aos-data:/aos:ro "${ALPINE}" cat /aos/worm.wal
    ;;
  "scp -t ${CK_NOVO}")
    receber "${CK_NOVO}"
    ;;
  "scp -t ${HD_NOVO}")
    receber "${HD_NOVO}"
    ;;
  trocar)
    trancar

    for f in "${CK_NOVO}" "${HD_NOVO}"; do
      [[ -f "${f}" && ! -L "${f}" ]] || recusa "falta ${f##*/} (entregar os DOIS antes de trocar)"
    done

    # CÓPIAS PRIVADAS, nos mesmos directórios (o `mv` final tem de ser um rename no mesmo sistema de
    # ficheiros). `cat >` escreve num inode NOVO: uma escrita tardia no `.novo` já não chega aqui.
    ck_priv="$(mktemp "${AOS_DIR}/ancoras/.checkpoints.troca.XXXXXX")"
    hd_priv="$(mktemp "${AOS_DIR}/pisos/.heads.troca.XXXXXX")"
    trap 'rm -f -- "${ck_priv}" "${hd_priv}"' EXIT
    cat -- "${CK_NOVO}" > "${ck_priv}"
    cat -- "${HD_NOVO}" > "${hd_priv}"
    rm -f -- "${CK_NOVO}" "${HD_NOVO}"

    # A validação corre ANTES de qualquer `mv`. Python do sistema em modo isolado (-I: ignora o
    # ambiente e o site do utilizador). Sem python3, ou sem biblioteca ed25519, NÃO se salta a
    # validação: recusa-se.
    command -v python3 >/dev/null 2>&1 || recusa "python3 ausente — a validação do par não corre, e sem ela não se troca"
    if ! motivo="$(python3 -I - "${ck_priv}" "${hd_priv}" "${CK}" "${HD}" "${ENV_FILE}" 2>&1 <<'PY'
import json, re, sys, base64, binascii, calendar, datetime

ck_novo, hd_novo, ck_atual, hd_atual, env_file = sys.argv[1:6]
CAMPOS = {"Partition", "AuditSeq", "EntryHash", "Timestamp", "Signature"}
TS = re.compile(r"^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|([+-])(\d{2}):(\d{2}))\Z")
DOMINIO = b"aos.audit.checkpoint.v1"

def falha(msg):
    print(msg)
    sys.exit(3)

# ── verificador ed25519: a biblioteca do sistema, e nunca uma implementação à mão ──────────────
try:
    import nacl.signing, nacl.exceptions
    def verifica(pub, msg, sig):
        try:
            nacl.signing.VerifyKey(pub).verify(msg, sig)
            return True
        except nacl.exceptions.BadSignatureError:
            return False
except ImportError:
    try:
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
        from cryptography.exceptions import InvalidSignature
        def verifica(pub, msg, sig):
            try:
                Ed25519PublicKey.from_public_bytes(pub).verify(sig, msg)
                return True
            except InvalidSignature:
                return False
    except ImportError:
        falha("sem biblioteca ed25519 (python3-nacl ou cryptography) — as assinaturas não se verificam, e sem isso não se troca")

def sem_duplicados(pares):
    d = {}
    for k, v in pares:
        if k in d:
            raise ValueError("chave duplicada %r" % k)
        d[k] = v
    return d

def constante(c):
    raise ValueError("constante que não é JSON: %s" % c)

def ler(caminho, entrega):
    try:
        with open(caminho, "rb") as f:
            raw = f.read()
    except FileNotFoundError:
        if entrega:
            falha("%s: não existe" % caminho)
        return None
    if raw.startswith(b"\xef\xbb\xbf"):
        if entrega:
            # O selador escreve SEM BOM. Um BOM aqui é outra coisa a entregar.
            falha("%s: tem BOM" % caminho)
        raw = raw[3:]
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=sem_duplicados, parse_constant=constante)
    except (UnicodeDecodeError, ValueError) as e:
        falha("%s: não é JSON válido (%s)" % (caminho, e))

def uint_positivo(v):
    return isinstance(v, int) and not isinstance(v, bool) and 0 < v < 2 ** 64

def particao_ok(p):
    return isinstance(p, str) and 0 < len(p) <= 512 and not any(ord(c) < 32 or ord(c) == 127 for c in p)

def b64(v, n):
    if not isinstance(v, str):
        return None
    try:
        b = base64.b64decode(v, validate=True)
    except (binascii.Error, ValueError):
        return None
    return b if len(b) == n else None

def unix_nano(ts):
    # time.Time.UTC().UnixNano() do Go, a partir do RFC 3339 do JSON. Os segundos vêm do calendário
    # (sem a ambiguidade de fuso do datetime local), a fracção é completada à direita até 9 dígitos,
    # e o desvio do fuso é subtraído. Uma data que o calendário recuse falha como Timestamp inválido.
    m = TS.match(ts)
    y, mo, d, h, mi, s = (int(m.group(i)) for i in range(1, 7))
    try:
        datetime.datetime(y, mo, d, h, mi, s)
    except ValueError:
        return None
    seg = calendar.timegm((y, mo, d, h, mi, s, 0, 0, 0))
    if m.group(8) != "Z":
        desvio = int(m.group(10)) * 3600 + int(m.group(11)) * 60
        seg -= desvio if m.group(9) == "+" else -desvio
    return seg * 10 ** 9 + int((m.group(7) or "").ljust(9, "0"))

def uvarint(n):
    out = bytearray()
    while n >= 0x80:
        out.append((n & 0x7F) | 0x80)
        n >>= 7
    out.append(n)
    return bytes(out)

def put_bytes(b):
    return uvarint(len(b)) + b

def canonico(p, seq, entry_hash, nanos):
    # audit.canonicalCheckpoint: domínio, partição e EntryHash com prefixo uvarint; AuditSeq e
    # UnixNano em big-endian de 8 bytes (o int64 em complemento para dois).
    return (put_bytes(DOMINIO) + put_bytes(p.encode("utf-8")) + seq.to_bytes(8, "big")
            + put_bytes(entry_hash) + (nanos & (2 ** 64 - 1)).to_bytes(8, "big"))

def ancora():
    valor = None
    try:
        with open(env_file, encoding="utf-8") as f:
            for linha in f:
                linha = linha.strip()
                if linha.startswith("AOS_WORM_TRUST_ANCHOR="):
                    valor = linha.split("=", 1)[1].strip().strip("\"'")
    except OSError as e:
        falha("não consegui ler a AOS_WORM_TRUST_ANCHOR (%s)" % e)
    if not valor or not re.fullmatch(r"[0-9a-f]{64}", valor):
        falha("AOS_WORM_TRUST_ANCHOR ausente ou inválida no .env — sem âncora não há contra o que verificar")
    return bytes.fromhex(valor)

def checkpoints(dados, nome, pub=None):
    if not isinstance(dados, list) or not dados:
        falha("%s: esperado um array NÃO vazio de checkpoints" % nome)
    seqs = {}
    for i, cp in enumerate(dados):
        if not isinstance(cp, dict) or set(cp) != CAMPOS:
            falha("%s[%d]: campos diferentes de %s" % (nome, i, sorted(CAMPOS)))
        p = cp["Partition"]
        if not particao_ok(p):
            falha("%s[%d]: Partition inválida" % (nome, i))
        if p in seqs:
            falha("%s: partição %r repetida" % (nome, p))
        if not uint_positivo(cp["AuditSeq"]):
            falha("%s[%d]: AuditSeq não é um inteiro positivo" % (nome, i))
        eh = b64(cp["EntryHash"], 32)
        if eh is None:
            falha("%s[%d]: EntryHash não é base64 de 32 bytes" % (nome, i))
        sig = b64(cp["Signature"], 64)
        if sig is None:
            falha("%s[%d]: Signature não é base64 de 64 bytes (ed25519)" % (nome, i))
        if not isinstance(cp["Timestamp"], str) or not TS.match(cp["Timestamp"]):
            falha("%s[%d]: Timestamp não é RFC 3339" % (nome, i))
        nanos = unix_nano(cp["Timestamp"])
        if nanos is None:
            falha("%s[%d]: Timestamp com data inválida" % (nome, i))
        if pub is not None and not verifica(pub, canonico(p, cp["AuditSeq"], eh, nanos), sig):
            falha("%s[%d]: ASSINATURA de %r não verifica contra a AOS_WORM_TRUST_ANCHOR em vigor" % (nome, i, p))
        seqs[p] = cp["AuditSeq"]
    return seqs

def pisos(dados, nome):
    if not isinstance(dados, dict) or not dados:
        falha("%s: esperado um objecto NÃO vazio {\"particao\": audit_seq}" % nome)
    for p, h in dados.items():
        if not particao_ok(p) or not uint_positivo(h):
            falha("%s: entrada inválida para %r" % (nome, p))
    return dados

# Forma e ASSINATURA de cada checkpoint novo, contra a mesma âncora que o nó usa no arranque.
novos_cp = checkpoints(ler(ck_novo, True), "checkpoints", ancora())
novos_hd = pisos(ler(hd_novo, True), "pisos")

# O PAR tem de ser da MESMA selagem. `worm-seal` emite o checkpoint de cada partição no seu head,
# e o piso dessa partição é esse mesmo head: um par cruzado de duas selagens não bate.
if set(novos_cp) != set(novos_hd):
    falha("o par não é da mesma selagem: %d partições nos checkpoints, %d nos pisos, %d em comum"
          % (len(novos_cp), len(novos_hd), len(set(novos_cp) & set(novos_hd))))
for p, seq in novos_cp.items():
    if novos_hd[p] != seq:
        falha("o par não é da mesma selagem: %r ancorada em %d com piso %d" % (p, seq, novos_hd[p]))

# NÃO-REGRESSÃO face ao par EM VIGOR. É o que impede esta chave de repor uma âncora antiga —
# legítima e assinada — para mascarar a truncatura do que veio depois. As partições nascem por
# run e o WAL é append-only: nenhuma desaparece e nenhum head desce. Corre DEPOIS da assinatura:
# senão um par mal assinado com números enormes passava aqui e trancava as selagens seguintes.
atual_cp = ler(ck_atual, False)
if atual_cp is not None:
    for p, seq in checkpoints(atual_cp, "checkpoints em vigor").items():
        if p not in novos_cp:
            falha("RECUO: a partição %r, ancorada em vigor, desapareceu" % p)
        if novos_cp[p] < seq:
            falha("RECUO: %r ancorada em %d, a nova âncora traz %d" % (p, seq, novos_cp[p]))
atual_hd = ler(hd_atual, False)
if atual_hd is not None:
    for p, h in pisos(atual_hd, "pisos em vigor").items():
        if p not in novos_hd or novos_hd[p] < h:
            falha("RECUO: o piso de %r desceu ou desapareceu" % p)

print("%d partições, assinaturas verificadas" % len(novos_cp))
PY
)"; then
      recusa "${motivo##*$'\n'}"
    fi

    # O par ANTERIOR fica ao lado, com um nome que o nó não lê. Uma troca que se revele errada
    # recupera-se com dois `mv` feitos por quem tem shell — em vez de não haver para onde voltar.
    [[ -f "${CK}" ]] && cp -p -- "${CK}" "${CK}.anterior"
    [[ -f "${HD}" ]] && cp -p -- "${HD}" "${HD}.anterior"

    # O nó corre como 65532 e lê por uma montagem :ro — os ficheiros têm de ser legíveis por ele.
    chmod 0644 -- "${ck_priv}" "${hd_priv}"
    # A JANELA que não é zero: entre os dois `mv`. Um arranque do nó exactamente aí apanharia um par
    # incoerente e recusaria arrancar (fail-closed). Recupera-se trocando outra vez. O nó só lê a
    # âncora no ARRANQUE: esta troca não o obriga a reiniciar, e ele não a vê até lá.
    mv -f -- "${ck_priv}" "${CK}"
    mv -f -- "${hd_priv}" "${HD}"
    sck="$(sha256sum "${CK}" | cut -d' ' -f1)"
    shd="$(sha256sum "${HD}" | cut -d' ' -f1)"
    regista "trocado: ${motivo} checkpoints=${sck} pisos=${shd}"
    printf 'TROCADO %s %s\n' "${sck}" "${shd}"
    ;;
  *)
    recusa
    ;;
esac
