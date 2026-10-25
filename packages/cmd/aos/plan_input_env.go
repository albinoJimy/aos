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

// Os dois valores de AOS_CONTEUDO_POR_HANDLE.
const (
	conteudoPorHandleOff = "off"
	conteudoPorHandleOn  = "on"
)

// ErrBadConteudoPorHandle — AOS_CONTEUDO_POR_HANDLE com um valor fora do vocabulário. O nó recusa
// arrancar, pela razão das outras variáveis de vocabulário fechado: um valor mal escrito não pode
// cair em silêncio para nenhum dos lados.
var ErrBadConteudoPorHandle = errors.New("aos: AOS_CONTEUDO_POR_HANDLE invalida — valores aceites: off (por omissao; nenhum run pode pedir o layout por handle) e on (o no HABILITA o layout por handle, e um run passa a poder pedi-lo com conteudo_por_handle no POST /runs)")

// parseConteudoPorHandleFromEnv lê AOS_CONTEUDO_POR_HANDLE. Vazia ⇒ false (`off`).
func parseConteudoPorHandleFromEnv() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AOS_CONTEUDO_POR_HANDLE")); raw {
	case "", conteudoPorHandleOff:
		return false, nil
	case conteudoPorHandleOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w (veio %q)", ErrBadConteudoPorHandle, raw)
	}
}

// conteudoPorHandleBanner é a linha de arranque do interruptor. Com `off` não sai linha nenhuma.
//
// O INTERRUPTOR HABILITA E NÃO MOVE NADA SOZINHO: nenhum run muda de layout por esta variável
// estar `on`. Quem escolhe é o pedido, por run, e é isso que torna as duas vias úteis em vez de
// redundantes (ADR-044 §2.9) — o interruptor é o recuo do operador, sem release; o campo é a
// escolha do plano, nó a nó, que é o que o caso de uso precisa (é o nó 2 do plano que consome o
// que o nó 1 leu).
func conteudoPorHandleBanner(ligado bool) []string {
	if !ligado {
		return nil
	}
	return []string{fmt.Sprintf("conteudo por handle (AOS-556, ADR-043 fase R2): AOS_CONTEUDO_POR_HANDLE=on — este no HABILITA o layout %s, e um run passa a poder pedi-lo com conteudo_por_handle=true no POST /runs. ESTA VARIAVEL NAO MOVE RUN NENHUM: sem o campo o layout e o de sempre, e com o campo num no em off a submissao e RECUSADA com 400 — nunca aceita e ignorada. Nesse layout o plan_input e o resultado de cada tool call saem do contexto do modelo e viajam por HANDLE opaco, e a autoridade de uma call passa a ser o join do piso com o taint de cada handle que ela resolve nos argumentos. CUSTO: a %s deriva da 1.7.0, logo herda o adiamento do plan_input (pelo menos DOIS turnos de modelo) e a media no turno; e cada tool call permitida consome uma posicao do tecto de handles do run. O QUE AINDA NAO COMPRA: a politica de fluxo de dados NAO esta em vigor (bundle 1.2.0 por assinar), pelo que uma call que consome um handle untrusted continua NEGADA pela regra binaria — seguro, e sem ganho de produto ate a assinatura. Medicao: a serie assembly_version=%q de aos_runs_hosted_total. Runs retomados nao mudam: continuam no layout do seu registo. Recuo: off",
		agentruntime.AssemblyVersion180, agentruntime.AssemblyVersion180, agentruntime.AssemblyVersion180)}
}

// layoutDoRunNovo é o layout em que um run NOVO deste nó fica fixado: o 1.6.0 quando o nó pede
// antes de ler e o run tem `plan_input`; senão o de [layoutDosRunsNovos]. Um run retomado não
// passa por aqui: continua no layout do seu registo de retoma.
//
// Com a media no turno ligada (AOS-546) todos os runs novos ficam no 1.7.0 — o 1.6.0 com a media:
// um run sem `plan_input` nem media materializa nele os bytes de sempre.
//
// A 1.8.0 (AOS-556) vem PRIMEIRO e só a PEDIDO: `goal.ConteudoPorHandle` é o campo do `POST /runs`,
// que a porta só aceita num nó que a habilite (`AOS_CONTEUDO_POR_HANDLE=on`) e que de outro modo
// RECUSA com 400 — ver [apiHandler.handleSubmit]. Vem primeiro porque a 1.8.0 DERIVA da 1.7.0 e
// herda-a: um run que a peça não precisa de pedir também a media.
func layoutDoRunNovo(capturaDoEstado, pedirAntesDeLer, media bool, goal agentruntime.Goal) string {
	if goal.ConteudoPorHandle {
		return agentruntime.AssemblyVersion180
	}
	if media {
		return agentruntime.AssemblyVersion170
	}
	if pedirAntesDeLer && len(goal.Inputs) > 0 {
		return agentruntime.AssemblyVersion160
	}
	return layoutDosRunsNovos(capturaDoEstado)
}
