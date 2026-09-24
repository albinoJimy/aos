#!/usr/bin/env bash
# nats.sh — GATE do SUBSTRATO REPLICADO REAL (AOS-431).
#
# ─── O PROBLEMA QUE ESTE GATE FECHA ────────────────────────────────────────────────────────
#
# Nenhum ficheiro de CI definia `AOS_NATS_URL`. A consequência não era «alguns testes não
# correm»: era que TUDO o que este repositório afirma sobre o JetStream era uma afirmação
# sobre a NOSSA regra, nunca sobre o servidor. E o eixo mediu isso três vezes:
#
#   · o AOS-424 encontrou NOVE nomes de stream irrepresentáveis que tinham passado dez gates,
#     uma revisão adversarial e o smoke — porque todos correm sobre ficheiro;
#   · o aperto do `Append` revelou TRÊS streams vivos no caminho de autorização que, sobre
#     JetStream, teriam negado toda a emissão de challenges e toda a ratificação;
#   · o AOS-425 fechou a composição em runtime e deixou escrito que «este nome é
#     representável» continuava a ser uma afirmação sobre nós, não sobre o NATS.
#
# ─── O QUE ESTE GATE PROVA ─────────────────────────────────────────────────────────────────
#
#   G1 As suites que exigem substrato replicado CORRERAM — e o número de testes que correram,
#      falharam e SALTARAM é contado na EXECUÇÃO, não por grep.
#   G2 NENHUM teste salta por falta de substrato. Com o cluster de pé, um skip não declarado é
#      um defeito: significa que o teste pede uma condição que este gate não fornece, e um
#      teste que salta em silêncio é indistinguível de um que não existe. Os skips legítimos
#      — hoje um só, o teste-veneno do `selftest.sh` — estão nomeados um a um na lista
#      `skips_legitimos`, com a razão. Não há limiar numérico: o que importa é QUAIS saltam.
#   G3 A cerimónia four-eyes sobrevive a um restart REAL sobre JetStream — o critério que o
#      AOS-424 deixou por marcar.
#
# ─── O QUE ESTE GATE TOLERA, E PORQUÊ ──────────────────────────────────────────────────────
#
# Duas falhas DECLARADAS (`falhas_conhecidas`, AOS-432): sobre substrato replicado, quem perde
# a corrida ao lease sai com um erro de transporte em vez do código da posse. A arbitragem
# está certa — exactamente 1 vencedor, sempre —; o que falha é a distinção entre «recusado» e
# «avariado». Foi este gate que o encontrou, e a alternativa (tirar `cmd/aos-orq` daqui) seria
# deixar de o ver. A lista auto-reforma-se: um teste dela que PASSE avermelha o gate.
#
# ─── PORQUE É QUE A CONTAGEM É POR EXECUÇÃO E NÃO POR GREP ─────────────────────────────────
#
# O `dormencia.sh` inventaria por `grep -rl 'AOS_NATS_URL'`, e isso SUBESTIMA: quatro ficheiros
# do pacote `jetstream` (19 funções de teste) saltam pelo helper partilhado `servidor(t)` e
# nunca escrevem o nome da variável. O gate reportava 47 testes em 9 ficheiros; a árvore tinha
# 45 skips em 13 ficheiros. Nenhum grep fecha isto de forma estável — a contagem certa vem de
# correr com `-v` e contar `--- SKIP`.
#
# ─── O QUE NÃO PROVA ───────────────────────────────────────────────────────────────────────
#
#   N1 NÃO prova nada sobre PRODUÇÃO. O nó de produção corre sobre ficheiro; migrá-lo para
#      JetStream é outra decisão (fora de âmbito do AOS-431, declarado no ticket).
#   N2 O cluster é local e de quatro nós num só host. Partição de rede real, latência entre
#      regiões e perda de disco não são observáveis aqui.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

CLUSTER="$(dirname "${BASH_SOURCE[0]}")/nats-cluster.sh"
rc=0

# =============================================================================================
# (0) O CLUSTER
# =============================================================================================
log_gate "nats · cluster JetStream de 4 nós (3 no board + 1 fora, para a fronteira soberana)"

if ! command -v docker >/dev/null 2>&1; then
  # SEM DOCKER SALTA-SE, E DECLARA-SE. É a mesma política dos outros gates que dependem de
  # contentores (`SKIP_DOCKER`): registar não é impedir, e o veredicto final redeclara-o.
  gate_skip "nats" "docker não disponível" \
    "o substrato replicado real NÃO foi exercitado; tudo o que o repositório afirma sobre JetStream continua por confirmar nesta execução"
  gate_skip_report || true
  exit 0
fi

if ! eval "$(bash "$CLUSTER" up)"; then
  log_fail "nats: o cluster não subiu"
  exit 1
fi
# O `trap` derruba SEMPRE — incluindo em falha. Um cluster deixado de pé num runner partilhado
# rouba as portas ao job seguinte, e localmente confunde a execução seguinte com streams de
# uma anterior (foi exactamente assim que um `subjects overlap` apareceu durante o AOS-431 e
# custou um diagnóstico a apontar para o sítio errado).
trap 'bash "$CLUSTER" down >/dev/null 2>&1 || true' EXIT

log_ok "nats: cluster de pé — AOS_NATS_URL=$AOS_NATS_URL"

# =============================================================================================
# (1) AS SUITES, COM A CONTAGEM FEITA NA EXECUÇÃO
# =============================================================================================
#
# Os módulos estão listados um a um, e não descobertos, porque a lista é o INVENTÁRIO: se um
# módulo novo passar a depender do substrato replicado e ninguém o acrescentar aqui, ele
# continua dormente — e o `dormencia.sh` é que tem de o acusar. Duas defesas, não uma.
modulos_nats=(
  "packages/substrate/eventstore|./jetstream/ ./natsjs/"
  "packages/integration|./..."
  "packages/cmd/aos|./..."
  "packages/cmd/aos-orq|./..."
)

# SKIPS LEGÍTIMOS, NOMEADOS UM A UM.
#
# «Nenhum teste salta» era forte demais, e foi a execução que o mostrou: o
# `TestSelftestApexEnforcementBypassReddensGate` é um teste-VENENO do `selftest.sh` e salta de
# propósito sem `AOS_APEX_SELFTEST=1` — não tem nada a ver com substrato.
#
# A lista existe em vez de um limiar numérico porque o que importa não é QUANTOS saltam, é
# QUAIS. Um teste novo a saltar por falta de cluster tem de avermelhar isto mesmo que o total
# não mude. Cada entrada traz a razão, e uma entrada que deixe de saltar não é problema —
# problema é um skip que não esteja aqui.
skips_legitimos=(
  "TestSelftestApexEnforcementBypassReddensGate"  # veneno do selftest.sh; exige AOS_APEX_SELFTEST=1
)

# SKIPS QUE SÓ EXISTEM FORA DE LINUX, e que em CI NÃO acontecem.
#
# Estes três medem bits POSIX de um ficheiro de segredo, e saltam em Windows porque lá os bits
# não são significativos — o alvo é o contentor Linux. Aceitá-los INCONDICIONALMENTE seria
# abrir um buraco permanente: se amanhã saltassem no runner, o gate calava-se. São aceites
# apenas quando o host NÃO é Linux, que é a condição que os faz saltar.
skips_so_fora_de_linux=(
  "TestAOS416_NHIIlegivelRecusaNoArranque"
  "TestAOS416_OModoDaConvencaoNaoERecusado"
  "TestAOS416_SegredoIlegivelRecusaNoArranque"
)
em_linux=0
[ "$(uname -s 2>/dev/null)" = "Linux" ] && em_linux=1

# FALHAS CONHECIDAS, COM TICKET — e o gate avermelha quando DEIXAREM de falhar.
#
# Ligar o cluster encontrou um defeito REAL que não é deste ticket: sobre substrato replicado,
# quem PERDE a corrida ao lease sai com `natsjs: ninguém serve este subject (503)` em vez de
# `ErrLeaseHeld`. A arbitragem está certa (exactamente 1 vencedor, 3/3); o que está errado é o
# modo de falha dos perdedores — «recusar tem de ser distinguível de avariar». É o AOS-432.
#
# A alternativa era não pôr `cmd/aos-orq` neste gate, e seria pior: a falha deixaria de ser
# vista no dia seguinte. Declarada, ela é contada, nomeada e tem dono.
#
# A lista AUTO-REFORMA-SE: um teste aqui que passe avermelha o gate. Uma falha declarada que
# se cure sem ninguém dar por isso é dívida que fica a pesar sem razão, e é o modo de falha
# das baselines que ninguém revisita.
falhas_conhecidas=(
  "TestAOS392_DespachoMultiProcessoSobreSubstratoReplicado"  # AOS-432
  "TestAOS100_NServeEmParaleloSobreOSubstratoReplicado"      # AOS-432
)

total_pass=0
total_fail=0
total_skip=0
total_skip_inesperado=0
total_fail_conhecida=0

for entrada in "${modulos_nats[@]}"; do
  modulo="${entrada%%|*}"
  alvos="${entrada##*|}"
  log_gate "nats · $modulo"

  saida="$(mktemp)"
  # `-count=1` porque um resultado em cache sobre um cluster que já não existe seria um
  # verde que não mediu nada. Sem `-race`: estes testes esperam por eleições de Raft e por
  # janelas de deduplicação, e o detector multiplica os tempos até ao limite do job — o
  # `-race` destes módulos corre no gate `test`, sem cluster.
  if (cd "$REPO_ROOT/$modulo" && eval "go test $alvos -count=1 -v") >"$saida" 2>&1; then
    estado="verde"
  else
    estado="vermelho"
  fi

  n_pass="$(grep -c '^--- PASS' "$saida" || true)"
  n_fail="$(grep -c '^--- FAIL' "$saida" || true)"
  n_skip="$(grep -c '^--- SKIP' "$saida" || true)"
  total_pass=$((total_pass + n_pass))
  total_fail=$((total_fail + n_fail))
  total_skip=$((total_skip + n_skip))

  printf '   %-34s PASS=%-4s FAIL=%-4s SKIP=%-4s (%s)\n' "$modulo" "$n_pass" "$n_fail" "$n_skip" "$estado"

  # FALHA NOVA vs FALHA DECLARADA.
  falhadas="$(grep '^--- FAIL' "$saida" | sed -E 's/^--- FAIL: ([^ ]+).*/\1/' | sort -u || true)"
  while IFS= read -r nome_teste; do
    [ -n "$nome_teste" ] || continue
    conhecida=0
    for c in "${falhas_conhecidas[@]}"; do
      [ "$nome_teste" = "$c" ] && conhecida=1 && break
    done
    if [ "$conhecida" -eq 1 ]; then
      printf '     falha DECLARADA (AOS-432): %s\n' "$nome_teste"
      total_fail_conhecida=$((total_fail_conhecida + 1))
    else
      log_fail "nats: $modulo — teste NOVO a falhar sobre substrato real: $nome_teste"
      grep -A8 "^--- FAIL: $nome_teste" "$saida" | head -12 || true
      rc=1
    fi
  done <<< "$falhadas"

  # A LISTA AUTO-REFORMA-SE: uma falha declarada que passou tem de sair da lista.
  for c in "${falhas_conhecidas[@]}"; do
    if grep -q "^--- PASS: $c" "$saida" 2>/dev/null; then
      log_fail "nats: $c está declarado como falha conhecida (AOS-432) e PASSOU"
      log_fail "     tira-o de falhas_conhecidas e fecha o AOS-432 — dívida curada que fica declarada é dívida que ninguém revisita"
      rc=1
    fi
  done

  # UM SKIP NÃO DECLARADO É UM DEFEITO AQUI, e é o critério do ticket: «um teste que salta em
  # silêncio é indistinguível de um que não existe». Com o cluster de pé, quem salta sem estar
  # na lista está a pedir uma condição que este gate não dá — e essa condição tem de ser
  # nomeada, não tolerada.
  if [ "$n_skip" -gt 0 ]; then
    # `|| true` em cada elo: sem ele, um `grep` sem correspondência mata o gate por `pipefail`
    # — e mata-o precisamente no ramo que existe para DIAGNOSTICAR. Aconteceu na primeira
    # execução deste ficheiro: o gate morreu a tentar imprimir o nome do teste que saltou.
    saltados="$(grep '^--- SKIP' "$saida" | sed -E 's/^--- SKIP: ([^ ]+).*/\1/' | sort -u || true)"
    while IFS= read -r nome_teste; do
      [ -n "$nome_teste" ] || continue
      esperado=0
      for conhecido in "${skips_legitimos[@]}"; do
        [ "$nome_teste" = "$conhecido" ] && esperado=1 && break
      done
      if [ "$esperado" -eq 0 ] && [ "$em_linux" -eq 0 ]; then
        for conhecido in "${skips_so_fora_de_linux[@]}"; do
          [ "$nome_teste" = "$conhecido" ] && esperado=1 && break
        done
      fi
      if [ "$esperado" -eq 1 ]; then
        printf '     salta por desenho, declarado: %s\n' "$nome_teste"
      else
        log_fail "nats: $modulo SALTOU um teste COM o cluster de pé, e o skip NÃO está declarado:"
        log_fail "     $nome_teste"
        grep -A2 "^--- SKIP: $nome_teste" "$saida" | grep -E '\.go:[0-9]+:' | sed 's/^ */       /' | head -3 || true
        total_skip_inesperado=$((total_skip_inesperado + 1))
        rc=1
      fi
    done <<< "$saltados"
  fi

  rm -f "$saida"
done

log_gate "nats · veredicto"
printf '   TOTAL sobre substrato replicado real: PASS=%s FAIL=%s (%s declaradas no AOS-432) SKIP=%s (%s não declarados)\n' \
  "$total_pass" "$total_fail" "$total_fail_conhecida" "$total_skip" "$total_skip_inesperado"

if [ "$total_pass" -lt 60 ]; then
  # CONTROLO DE NÃO-VACUIDADE. Um gate que levanta o cluster e corre zero testes fica verde
  # e não mede nada — é o modo de falha que este ficheiro existe para não ter. O piso vem da
  # medição do AOS-431 (67 passaram no módulo `eventstore` sozinho) e só APERTA.
  log_fail "nats: só $total_pass teste(s) passaram — o gate correu quase vazio, e um gate vazio é verde por acidente"
  rc=1
fi

if [ "$rc" -eq 0 ]; then
  log_ok "nats: substrato replicado real exercitado, $total_pass teste(s), nenhum skip por falta de substrato"
  if [ "$total_fail_conhecida" -gt 0 ]; then
    log_warn "  $total_fail_conhecida falha(s) DECLARADA(S) no AOS-432 — o gate está verde COM dívida nomeada, não sem ela"
  fi
fi

gate_skip_report || true
exit "$rc"
