package agentruntime

import "context"

// ---------------------------------------------------------------------------
// AOS-262 — ProgressObserver (burn-down + AVISO na fronteira de fim-de-turno)
// ---------------------------------------------------------------------------

// ProgressObserver é a PORTA de OBSERVAÇÃO do burn-down, consultada na MESMA FRONTEIRA DE
// FIM DE TURNO da pausa graciosa e do disjuntor (todas as activities do turno confirmadas,
// entre turnos e nunca a meio).
//
// # É LEITURA, NÃO DECISÃO — e a diferença é o ticket inteiro
//
// O observador NÃO devolve veredicto: não pára o run, não pede escolha, não estende nada.
// A primeira entrega de AOS-262 é SÓ burn-down + aviso. As opções de exaustão
// (`extend`/`summarize_stop`/`abort`) não são apresentadas porque nenhuma tem executor nem
// autoridade no nó — apresentá-las seria prometer o que não existe. Quem PÁRA um run
// continua a ser o [LivenessBreaker] (com veredicto durável) ou o steer do operador; esta
// porta não lhes rouba o papel nem o duplica.
//
// Por isso a assinatura devolve só `error` — não há `(tripped bool, ...)` como no
// [LivenessBreaker]. Um contrato que não pode parar o run é um contrato que não engana o
// leitor sobre quem manda.
//
// # PORQUE UM ERRO É FATAL
//
// Mesmo sendo leitura, um erro ABORTA o run, como no [LivenessBreaker] e na compactação. A
// razão é a de AOS-261: a fonte do burn-down devolve erro exactamente quando NÃO TEM DADOS,
// e continuar a correr um agente autónomo com o burn-down cego — depois de o nó ter
// anunciado ao operador que o run é avisado a ~80% — é a superfície verde a mentir que
// estes dois tickets removem. O adaptador do nó é quem decide o que é erro: erros
// TRANSITÓRIOS não devem chegar aqui.
//
// ADITIVA: sem [WithProgressObserver] o loop nunca a consulta e o comportamento de AOS-013
// é byte-idêntico (mesmo padrão de [WithSteerSource]/[WithLivenessBreaker]).
type ProgressObserver interface {
	// ObserveProgress reporta o fecho de UMA iteração do agente. O adaptador lê o
	// consumo acumulado do run, compara-o com o tecto e — a partir do limiar — emite o
	// AVISO (uma vez por run). Não devolve veredicto: o loop continua.
	ObserveProgress(ctx context.Context, runID string, turn int) error
}

// WithProgressObserver injecta o observador de burn-down (AOS-262). Um valor nil é
// ignorado (mantém o comportamento byte-idêntico de AOS-013, sem observação).
func WithProgressObserver(o ProgressObserver) Option {
	return func(rt *Runtime) {
		if o != nil {
			rt.progressObserver = o
		}
	}
}
