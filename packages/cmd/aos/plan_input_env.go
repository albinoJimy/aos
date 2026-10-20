package main

// PEDIR ANTES DE LER (ADR-034 §4 e resíduo 3) — `AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO`.
//
// Pelo ADR-034, a autorização de uma tool call é o rótulo do contexto que o modelo viu, e um
// `plan_input` (os dados que outro nó do plano produziu) torna o contexto untrusted. No layout de
// sempre os `plan_input` vêm na semente, ANTES do objectivo: num nó que consome dados de outro nó,
// nenhuma tool call é pedida sobre um contexto trusted — nem a do primeiro turno, nem quando o
// objectivo, escrito por quem submeteu, nomeia o documento a ler.
//
//   - `off` (por omissão): nada muda. Os runs novos ficam no layout de sempre, e o prompt, os
//     eventos e o `/metrics` são os de antes, byte a byte.
//   - `on`: um run NOVO com `plan_input` fica no layout 1.6.0. O primeiro turno vê o objectivo e
//     um aviso de que os dados do passo anterior chegam no turno seguinte; as tool calls que o
//     modelo pedir nesse turno são autorizadas com o contexto trusted. Os `plan_input` entram a
//     seguir aos resultados dessas tool calls (ou logo a seguir, se o turno não pediu nenhuma), e
//     desse ponto em diante o contexto é untrusted, como sempre. Um run sem `plan_input` fica no
//     layout de sempre.
//
// CUSTO: com `on`, um nó com `plan_input` dá pelo menos dois turnos de modelo — o primeiro não
// termina o run, mesmo que responda só com texto. Um `max_turns` menor do que 2 num run com
// `plan_input` é recusado com 400.
//
// SEGURANÇA: no turno 1 o OBJECTIVO pode autorizar QUALQUER tool call privilegiada — uma leitura
// ou um efeito (`cap:http.post`, escrita) —, como num nó sem `plan_input`: é o resíduo R7 do
// ADR-034, alargado aos nós consumidores. O que um `plan_input` injectado consegue continua a ser
// nada: no turno 1 o modelo não o viu, e do turno 2 em diante o contexto é untrusted.
//
// RECUO: voltar a `off`. Os runs começados em 1.6.0 continuam nela até ao fim (o layout é fixado
// por run e vai no registo de retoma) e reproduzem-se como foram gravados.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Os dois valores de AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO.
const (
	planInputDepoisOff = "off"
	planInputDepoisOn  = "on"
)

// ErrBadPlanInputDepois — AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO está definida com um valor fora
// do vocabulário. O nó recusa arrancar: um valor mal escrito não pode cair em silêncio para `off`
// (o operador ficava convencido de que os nós pedem antes de ler) nem para `on`.
var ErrBadPlanInputDepois = errors.New("aos: AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO invalida — valores aceites: off (por omissao; os plan_input entram no primeiro turno, como sempre) e on (num run com plan_input, o primeiro turno ve so o objectivo e as tool calls dele sao autorizadas com o contexto trusted; os plan_input entram a partir do segundo turno)")

// parsePlanInputDepoisFromEnv lê AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO. Vazia ⇒ false (`off`).
// Sem normalização de caixa, pela regra das outras variáveis de vocabulário fechado do nó.
func parsePlanInputDepoisFromEnv() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO")); raw {
	case "", planInputDepoisOff:
		return false, nil
	case planInputDepoisOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w (veio %q)", ErrBadPlanInputDepois, raw)
	}
}

// planInputDepoisBanner é a linha de arranque do interruptor. Com `off` não sai linha nenhuma: o
// arranque é o de antes.
func planInputDepoisBanner(ligado bool) []string {
	if !ligado {
		return nil
	}
	return []string{fmt.Sprintf("pedir antes de ler (ADR-034 §4): AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO=on — um run NOVO com plan_input fica no layout %s: o primeiro turno ve so o objectivo (e um aviso de que os dados do passo anterior chegam no turno seguinte), e as tool calls que o modelo pedir nele sao autorizadas com o contexto TRUSTED — QUALQUER tool call privilegiada, leitura ou efeito, como num no sem plan_input: o objectivo de quem submete passa a poder autoriza-las num no consumidor (ADR-034 R7); os plan_input entram a partir do segundo turno, e dai em diante o contexto e untrusted, como sempre. CUSTO: um no com plan_input da pelo menos DOIS turnos de modelo — o primeiro nao termina o run, mesmo so com texto; max_turns < 2 com plan_input e recusado (400). Medicao: plan_inputs_deferred no turn.recorded do primeiro turno e a serie assembly_version=%q de aos_runs_hosted_total. Runs sem plan_input e runs retomados nao mudam. Recuo: off",
		agentruntime.AssemblyVersion160, agentruntime.AssemblyVersion160)}
}

// layoutDoRunNovo é o layout em que um run NOVO deste nó fica fixado: o 1.6.0 quando o nó pede
// antes de ler e o run tem `plan_input`; senão o de [layoutDosRunsNovos]. Um run retomado não
// passa por aqui: continua no layout do seu registo de retoma.
func layoutDoRunNovo(capturaDoEstado, pedirAntesDeLer bool, goal agentruntime.Goal) string {
	if pedirAntesDeLer && len(goal.Inputs) > 0 {
		return agentruntime.AssemblyVersion160
	}
	return layoutDosRunsNovos(capturaDoEstado)
}
