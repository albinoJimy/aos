package main

// AOS-262 — O LIMIAR DO AVISO DE BURN-DOWN.
//
// A superfície é de UMA variável, `AOS_PROGRESS_THRESHOLD`, e é uma FRACÇÃO consumida
// (0 < t < 1). Vazia ⇒ o default de [progresssurface.DefaultThreshold] (~0.80) — um default
// que o nó SABE justificar, ao contrário dos tectos de queima do disjuntor: 80% é a
// convenção do próprio EPIC-12 e o aviso não nega nem pára nada, pelo que errar por 5
// pontos custa uma linha de telemetria, não um run morto.
//
// FAIL-CLOSED NA CONFIGURAÇÃO (molde de [ErrBadBreakerThresholds]/[ErrBadBudget]/
// [ErrBadRetention]): um valor ilegível ou fora de (0,1) ABORTA o arranque. Isto é
// DELIBERADAMENTE mais estrito do que [progresssurface.WithThreshold], que ignora um limiar
// inválido e mantém o default — o fallback silencioso que o ticket nomeia. A diferença de
// postura tem uma razão: uma opção de biblioteca com um valor mau é um erro do programador,
// que o teste apanha; uma ENV com um valor mau é um erro do OPERADOR, que ninguém apanha —
// ele escreveu `0.9`, recebeu `0.80` e fica convencido de que configurou o aviso. Escrever
// `0` ou `1` merece a nota explícita: `0` faria o aviso disparar sempre (no primeiro turno,
// com 0 tokens gastos) e `1` faria nunca disparar antes de o tecto estar esgotado — os dois
// modos de falha que um limiar existe para evitar.
//
// A validação usa [progresssurface.ValidThreshold] — a MESMA função que a superfície usa —
// para que não exista uma segunda noção de "limiar válido" a divergir desta.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
)

// ErrBadProgressThreshold — o limiar do aviso de burn-down está definido mas é inválido. O
// nó recusa arrancar em vez de correr com um limiar diferente do que o operador escreveu.
var ErrBadProgressThreshold = errors.New("aos: limiar do aviso de burn-down mal configurado — AOS_PROGRESS_THRESHOLD tem de ser uma FRACCAO estritamente entre 0 e 1 (ex.: 0.8). Deixe a variavel POR DEFINIR para usar o default ~0.80; 0 avisaria em todos os turnos e 1 nunca avisaria antes do tecto esgotado")

// progressThresholdFromEnv resolve o limiar do aviso.
//
// Devolve (limiar, definida?, erro). O booleano distingue "o operador PEDIU o aviso" de "o
// nó usa o default": é ele que arma o gate de cablagem do arranque
// ([ErrProgressBudgetUnwired]) — pedir explicitamente um aviso que não pode disparar é uma
// configuração impossível, enquanto herdar o default num nó sem orçamento é apenas o estado
// por omissão, e esse é declarado no banner.
func progressThresholdFromEnv() (float64, bool, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_PROGRESS_THRESHOLD"))
	if raw == "" {
		return progresssurface.DefaultThreshold, false, nil
	}
	t, err := strconv.ParseFloat(raw, 64)
	if err != nil || !progresssurface.ValidThreshold(t) {
		return 0, false, fmt.Errorf("%w: AOS_PROGRESS_THRESHOLD=%q", ErrBadProgressThreshold, raw)
	}
	return t, true, nil
}
