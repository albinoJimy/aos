#!/usr/bin/env bash
# scale.sh — GATE de CARGA/ESCALA (AOS-116, EPIC-11), a defesa do ADR-008.
#
# Corre a suite de CARGA/ESCALA do scheduler (packages/control-plane/scheduler,
# loadscale_test.go) que COMPÕE os controlos REAIS do plano de controlo (admission
# control GLOBAL / AOS-027, spawn coordinator / AOS-028, política+filas / AOS-030,
# degradação / AOS-031, breaker de orçamento / AOS-029, métricas / AOS-034) e prova,
# por carga agregada DETERMINISTA, que o modo de falha "individualmente ok,
# agregadamente colapsa" é CONTIDO:
#   AC1 SATURAÇÃO AGREGADA  — N boards, cada um no seu max local, saturam o rate limit
#                             PARTILHADO; a soma dos grants nunca excede o headroom
#                             global (reserva atómica CAS), o excesso é ADIADO, e
#                             max_spawn deriva do headroom (0 sob headroom nulo);
#   AC2 DEGRADAÇÃO POR ORDEM — a escada segue shed→defer→downgrade→reject (ordem
#                             canónica), reject é terminal fail-closed, e o backpressure
#                             das filas propaga-se ao admit;
#   AC3 FILAS LIMITADAS      — sob sobrecarga a fila NUNCA cresce além de MaxLen (aplica
#                             a política em vez de acumular) — sem cascata de timeouts;
#   AC4 BREAKER DE ORÇAMENTO — dispara nos limiares (esgotamento E velocidade), nega
#                             fail-closed e é OBSERVÁVEL via replay + aviso ~80%;
#   AC5 NFRs COMO SINAIS      — deny-rate, degrau de degradação (0..4) e wait-p95 são
#                             MEDIÇÕES observadas, com a linha marcada AOS_SCALE_REPORT.
# O META-TESTE prova detecção NÃO-VÁCUA: com o admission control CONTORNADO (headroom
# ~infinito), a MESMA carga agregada OVERSUBSCREVE — a prova de que AC1 depende MESMO
# do enforcement (não passa trivialmente).
#
# É o análogo, para a CARGA/ESCALA, do gate routing (AOS-063): fail-closed e NÃO-VAZIO.
# Usa require_tests (lib.sh) para exigir que CADA teste obrigatório — os 5 cenários (AC1
# a AC5) + o meta-teste — tenha EFECTIVAMENTE corrido (não basta o exit 0; um -run que
# não casasse nada passaria vazio). Fail-closed: um cenário não-tratado OU o meta-teste
# a deixar de detectar torna o gate VERMELHO (exit != 0).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

SCHED_MOD="packages/control-plane/scheduler"
SUITE_PKG="./..."

# Testes OBRIGATÓRIOS: os 5 cenários (AC1..AC5) + o meta-teste (detecção não-vácua).
# require_tests exige que TODOS tenham corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestScale_AggregateSaturation_AtomicReservationNoOversubscribe
  TestScale_GracefulDegradationInOrder
  TestScale_QueuesRemainBoundedUnderOverload
  TestScale_BudgetBreakerTripsAtThreshold
  TestScale_NFRSignalsReported
  TestMetaDetects_AggregateCollapseWithoutAdmission
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios e
# nada por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "scale (AOS-116) · carga/escala: admission global + backpressure + degradação (ADR-008)"

# (1) require_tests: os testes obrigatórios (incl. meta-teste) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$SCHED_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa do scheduler (determinismo sob concorrência). A carga é
# in-process/determinista (contadores/iterações + relógios injectáveis), mas -race
# blinda contra qualquer regressão de corrida nos primitivos compostos.
log_step "go test $SUITE_PKG -race -count=2 (determinismo: 2x o MESMO veredicto)"
if ! ( cd "$REPO_ROOT/$SCHED_MOD" && go test "$SUITE_PKG" -race -count=2 ); then
  log_fail "suite de carga/escala vermelha (-race)"
  exit 1
fi

# (3) RELATÓRIO de NFRs (linha marcada AOS_SCALE_REPORT) + fail-closed sobre a AUSÊNCIA
# de oversubscription. À imagem do AOS_ROUTING_REPORT (AOS-063) e AOS_REPLAY_REPORT
# (AOS-024): a invariante central do ADR-008 é oversubscribed:false — a soma dos grants
# nunca excede o headroom global. Extrai-se a linha do teste de sinais e valida-se.
log_gate "scale · relatório de NFRs (deny-rate · degrau de degradação · wait-p95)"
report="$( cd "$REPO_ROOT/$SCHED_MOD" && go test "$SUITE_PKG" -run '^TestScale_NFRSignalsReported$' -v -count=1 2>/dev/null \
  | grep 'AOS_SCALE_REPORT' | sed 's/.*AOS_SCALE_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório de escala não emitido (AOS_SCALE_REPORT ausente)"
  exit 1
fi
printf '   %s\n' "$report"
# oversubscribed TEM de ser false (a defesa do ADR-008 conteve a carga agregada).
if ! printf '%s' "$report" | grep -Eq '"oversubscribed":false'; then
  log_fail "relatório indica OVERSUBSCRIPTION (a carga agregada colapsou o rate limit partilhado)"
  exit 1
fi

log_ok "scale: verde (5 cenários de carga/escala AC1..AC5 + meta-teste não-vácuo · oversubscribed:false)"
