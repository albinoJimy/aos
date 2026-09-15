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
#  · Escrever um par BEM FORMADO mas mal assinado. O gate não verifica assinaturas — o nó
#    verifica-as no arranque, fail-closed. O pior desfecho é um nó que NÃO ARRANCA no próximo
#    restart, nunca um que arranca com uma âncora falsa. Recupera-se com uma selagem legítima.
#  · NÃO consegue: shell, SFTP, forwarding, ler o .env/secrets, escrever noutro caminho, trocar
#    um ficheiro sem o outro, nem REPOR um par ANTERIOR (legítimo e assinado) para mascarar uma
#    truncatura — a troca recusa qualquer partição que recue face ao par em vigor.
#
# O que não passa é recusado e registado no syslog (`aos-worm-seal`), tal como cada leitura do
# WORM e cada troca feita. Um pedido recusado com esta chave é, por definição, alguém que não é
# a tarefa.
set -euo pipefail
umask 077

AOS_DIR=/opt/aos
CK="${AOS_DIR}/ancoras/checkpoints.json"
HD="${AOS_DIR}/pisos/heads.json"
CK_NOVO="${AOS_DIR}/ancoras/.checkpoints.novo"
HD_NOVO="${AOS_DIR}/pisos/.heads.novo"
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

receber() {
  local alvo="$1"
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
      -v aos_aos-data:/aos:ro alpine:3.20 cat /aos/worm.wal
    ;;
  "scp -t ${CK_NOVO}")
    receber "${CK_NOVO}"
    ;;
  "scp -t ${HD_NOVO}")
    receber "${HD_NOVO}"
    ;;
  trocar)
    # Duas selagens simultâneas trocariam pares cruzados. A segunda recusa em vez de esperar.
    exec 9>"${AOS_DIR}/.worm-seal.lock"
    flock -n 9 || recusa "outra troca em curso"

    for f in "${CK_NOVO}" "${HD_NOVO}"; do
      [[ -f "${f}" && ! -L "${f}" ]] || recusa "falta ${f##*/} (entregar os DOIS antes de trocar)"
    done

    # A validação corre ANTES de qualquer `mv`. Python do sistema em modo isolado (-I: ignora o
    # ambiente e o site do utilizador). Sem python3 NÃO se salta a validação: recusa-se.
    command -v python3 >/dev/null 2>&1 || recusa "python3 ausente — a validação do par não corre, e sem ela não se troca"
    if ! motivo="$(python3 -I - "${CK_NOVO}" "${HD_NOVO}" "${CK}" "${HD}" 2>&1 <<'PY'
import json, re, sys, base64, binascii

ck_novo, hd_novo, ck_atual, hd_atual = sys.argv[1:5]
CAMPOS = {"Partition", "AuditSeq", "EntryHash", "Timestamp", "Signature"}
TS = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$")

def falha(msg):
    print(msg)
    sys.exit(3)

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
        return False
    try:
        return len(base64.b64decode(v, validate=True)) == n
    except (binascii.Error, ValueError):
        return False

def checkpoints(dados, nome):
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
        if not b64(cp["EntryHash"], 32):
            falha("%s[%d]: EntryHash não é base64 de 32 bytes" % (nome, i))
        if not b64(cp["Signature"], 64):
            falha("%s[%d]: Signature não é base64 de 64 bytes (ed25519)" % (nome, i))
        if not isinstance(cp["Timestamp"], str) or not TS.match(cp["Timestamp"]):
            falha("%s[%d]: Timestamp não é RFC 3339" % (nome, i))
        seqs[p] = cp["AuditSeq"]
    return seqs

def pisos(dados, nome):
    if not isinstance(dados, dict) or not dados:
        falha("%s: esperado um objecto NÃO vazio {\"particao\": audit_seq}" % nome)
    for p, h in dados.items():
        if not particao_ok(p) or not uint_positivo(h):
            falha("%s: entrada inválida para %r" % (nome, p))
    return dados

novos_cp = checkpoints(ler(ck_novo, True), "checkpoints")
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
# run e o WAL é append-only: nenhuma desaparece e nenhum head desce.
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

print("%d partições" % len(novos_cp))
PY
)"; then
      rm -f -- "${CK_NOVO}" "${HD_NOVO}"
      recusa "${motivo##*$'\n'}"
    fi

    # O nó corre como 65532 e lê por uma montagem :ro — os ficheiros têm de ser legíveis por ele.
    chmod 0644 -- "${CK_NOVO}" "${HD_NOVO}"
    # A JANELA que não é zero: entre os dois `mv`. Um arranque do nó exactamente aí apanharia um par
    # incoerente e recusaria arrancar (fail-closed). Recupera-se trocando outra vez. O nó só lê a
    # âncora no ARRANQUE: esta troca não o obriga a reiniciar, e ele não a vê até lá.
    mv -f -- "${CK_NOVO}" "${CK}"
    mv -f -- "${HD_NOVO}" "${HD}"
    sck="$(sha256sum "${CK}" | cut -d' ' -f1)"
    shd="$(sha256sum "${HD}" | cut -d' ' -f1)"
    regista "trocado: ${motivo} checkpoints=${sck} pisos=${shd}"
    printf 'TROCADO %s %s\n' "${sck}" "${shd}"
    ;;
  *)
    recusa
    ;;
esac
