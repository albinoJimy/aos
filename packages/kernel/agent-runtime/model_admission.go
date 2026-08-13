package agentruntime

import "context"

// ---------------------------------------------------------------------------
// AOS-260 — ModelAdmission (ADMISSÃO DO TURNO DE MODELO)
// ---------------------------------------------------------------------------

// ModelAdmission é a porta de ADMISSÃO da chamada ao modelo — o controlo que faltava na
// única linha de custo que atravessava o loop sem passar por lado nenhum.
//
// # O buraco que esta porta fecha
//
// O hook de orçamento (AOS-008/AOS-256/AOS-257) é um hook do Reference Monitor, e a cadeia
// do RM só é atravessada POR TOOL CALL. A inferência é invocada AQUI, directamente
// ([Runtime.callModel] → [ModelClient.Call]), fora da cadeia: nenhuma reserva a admitia e
// nenhum tecto a travava — só o TEMPO (wall-clock do disjuntor, no-progress, MaxTurns). Era
// o risco 1 do desafio A1, e a decisão do dono (D1 = opção B, 2026-08-13) manda cobri-la:
// o orçamento passa a cobrir tool calls E o turno de modelo.
//
// # A porta é RESERVA + SALDO, e as duas metades são obrigatórias
//
// Não basta contar depois. Contar depois é burn-down (AOS-261/AOS-262), que já existe e que
// declaradamente NÃO decide; o que aqui se acrescenta é ADMISSÃO — decidir ANTES de gastar:
//
//   - [ModelAdmission.AdmitTurn] corre IMEDIATAMENTE antes de [ModelClient.Call], com o
//     prompt do turno já materializado (a única base honesta para estimar o input). Devolve
//     um VEREDICTO, não um erro: negar não é avariar;
//   - [ModelAdmission.SettleTurn] corre IMEDIATAMENTE depois, com o consumo REAL da resposta
//     (`Usage` + `CostMicroUSD`, o canal que AOS-259 ligou ponta a ponta). É o que impede a
//     estimativa de virar contabilidade: a provisão feita antes é substituída pelo que o
//     turno de facto custou. Sem ela, um tecto composto por provisões esgotava-se com
//     consumo fantasma e negava runs saudáveis.
//
// # NEGAR NÃO É ERRAR — e é por isso que o veredicto é um valor
//
// Um erro desta porta é FATAL (aborta o run), como no [LivenessBreaker] e no
// [ProgressObserver]: significa que o admission control ficou CEGO, e correr um agente
// autónomo com o tecto cego é a superfície verde que estes tickets removem. Mas a NEGAÇÃO —
// o caso normal do esgotamento — não é um erro: é [TurnAdmissionVerdict] com `Admitted`
// false e uma razão atribuível, e o loop PÁRA com essa razão em [Result.BudgetExhausted] /
// [Result.BudgetExhaustionReason].
//
// O QUE O LOOP NUNCA FAZ, e é a razão de a porta ter esta forma: RETENTAR. Um loop que
// negasse e voltasse a tentar o mesmo turno queimaria wall-clock sem progresso e morreria
// pelo disjuntor — com a causa ERRADA no log (`no-progress` em vez de `orçamento`). O
// esgotamento devolve o controlo ao chamador de imediato, UMA vez, com o nome certo.
//
// # DEGRADAÇÃO DECLARADA (quem escala é o adaptador, não o kernel)
//
// O kernel pára o run e diz porquê. O que se FAZ com isso é do adaptador, e o nó tem já dois
// caminhos compostos para o efeito (AOS-262/AOS-263): o adaptador pode levantar o PROMPT DE
// EXAUSTÃO — suspendendo o run em `waiting_on_human` com uma decisão humana durável — e
// nesse caso devolve o erro que o nó reconhece como suspensão; ou, sem essa maquinaria,
// devolve simplesmente a negação e o run pára com razão própria. Nenhum destes caminhos é
// inventado aqui: são os que AOS-262/AOS-263 entregaram.
//
// # REPLAY (a armadilha nomeada na decisão do dono)
//
// A retoma (AOS-021/AOS-253) REPRODUZ os turnos já dados a partir da captura: o
// [ModelClient] devolve a resposta registada e nenhum provider é chamado. Se a admissão
// corresse nesses turnos, o run seria cobrado DUAS VEZES pelo mesmo turno e o tecto passaria
// a mentir. O contrato desta porta é, por isso, explícito: um turno REPRODUZIDO é admitido
// com `AlreadyAdmitted` true e SEM reserva nova — a dedup é por `run_id:step_id`, a mesma
// chave de idempotência do Event Store e do step-ledger (`already-applied`). O loop chama a
// porta em todos os turnos, sempre da mesma maneira; quem sabe distinguir é o adaptador, que
// é quem sabe se está a reproduzir.
//
// # CAMINHO QUENTE
//
// Isto corre em CADA turno de CADA run. O contrato exige das implementações que sejam
// O(prompt) sem I/O síncrono novo e sem locks de contenção global, e que não introduzam
// não-determinismo no replay (o veredicto de um turno reproduzido é sempre o mesmo:
// admitido, sem reserva).
//
// ADITIVA: sem [WithModelAdmission] o loop nunca a consulta e o comportamento de AOS-013 é
// byte-idêntico (mesmo padrão de [WithLivenessBreaker]/[WithProgressObserver]).
type ModelAdmission interface {
	// AdmitTurn decide se o turno pode chamar o modelo, reservando o headroom estimado.
	// Um erro é FATAL (cegueira do admission control); a negação viaja no veredicto.
	AdmitTurn(ctx context.Context, req TurnAdmissionRequest) (TurnAdmissionVerdict, error)
	// SettleTurn salda a reserva do turno com o consumo REAL da resposta. É chamado
	// EXACTAMENTE UMA VEZ por AdmitTurn que tenha admitido — incluindo quando a chamada
	// ao modelo FALHOU (ver [TurnSettlement.Failed]), para que a provisão não fique presa.
	SettleTurn(ctx context.Context, s TurnSettlement) error
}

// TurnAdmissionRequest é o que a porta vê ANTES da chamada ao modelo. Transporta a chave de
// dedup (`RunID`+`StepID`) e a única base honesta para estimar o input: o prompt JÁ
// MATERIALIZADO do turno — o mesmo bytes-a-bytes que vai ser enviado ao provider, e o mesmo
// cujo hash o manifesto por trajectória grava.
type TurnAdmissionRequest struct {
	// RunID é o run (e, no nó, também o id do nó da árvore de orçamento).
	RunID string
	// StepID é o passo do turno. `RunID:StepID` é a chave de idempotência/dedup.
	StepID string
	// Turn é o índice 1-based do turno (para a razão da negação ser diagnosticável).
	Turn int
	// ModelID é o modelo pinado do run (o preço/tarifa depende dele).
	ModelID string
	// View é o prompt materializado do turno. O adaptador estima o INPUT a partir dela;
	// o OUTPUT é desconhecido antes da chamada e é matéria da POLÍTICA DE PROVISÃO do
	// adaptador, declarada por ele.
	View PromptView
}

// TurnAdmissionVerdict é a decisão de admissão. O valor-zero é NEGAÇÃO — fail-closed por
// construção: uma implementação que se esqueça de preencher nega, nunca admite.
type TurnAdmissionVerdict struct {
	// Admitted autoriza a chamada ao modelo.
	Admitted bool
	// Reason é o rótulo ATRIBUÍVEL da negação (nunca segredo): vai a [Result] e ao log do
	// nó, e é o que distingue «parou por orçamento» de «parou por tempo». Ignorado quando
	// Admitted.
	Reason string
	// AlreadyAdmitted marca um turno REPRODUZIDO (replay da captura): admitido SEM reserva
	// nova, porque o consumo original já foi cobrado na incarnação que o produziu. O loop
	// não o lê para decidir — lê-o para NÃO saldar o que não reservou.
	AlreadyAdmitted bool
}

// TurnSettlement é o consumo REAL de um turno admitido, entregue à porta logo após a
// resposta do modelo.
type TurnSettlement struct {
	// RunID/StepID identificam a reserva a saldar (a mesma chave da admissão).
	RunID  string
	StepID string
	// Turn é o índice 1-based do turno.
	Turn int
	// Usage é o consumo de tokens MEDIDO (o que o provider ecoou).
	Usage Usage
	// CostMicroUSD é o custo MEDIDO do turno em micro-USD inteiro (AOS-259).
	CostMicroUSD int64
	// Failed indica que a chamada ao modelo FALHOU: não há consumo a confirmar e a
	// provisão tem de ser LIBERTADA. Sem este caminho, um provider intermitente
	// esgotaria o tecto de um run com reservas que nunca ninguém saldou.
	Failed bool
}

// WithModelAdmission injecta a porta de admissão do turno de modelo (AOS-260). Um valor nil
// é ignorado (mantém o comportamento byte-idêntico de AOS-013, sem admissão).
func WithModelAdmission(a ModelAdmission) Option {
	return func(rt *Runtime) {
		if a != nil {
			rt.admission = a
		}
	}
}

// admitTurn consulta a porta de admissão. Sem porta ligada devolve o veredicto NEUTRO
// (admitido, sem reserva) — o comportamento de AOS-013.
func (rt *Runtime) admitTurn(ctx context.Context, goal Goal, stepID string, turn int, view PromptView) (TurnAdmissionVerdict, error) {
	if rt.admission == nil {
		return TurnAdmissionVerdict{Admitted: true, AlreadyAdmitted: true}, nil
	}
	return rt.admission.AdmitTurn(ctx, TurnAdmissionRequest{
		RunID:   goal.RunID,
		StepID:  stepID,
		Turn:    turn,
		ModelID: goal.Model.ModelID,
		View:    view,
	})
}

// settleTurn salda a reserva do turno com o consumo real. É NO-OP quando não há porta ou
// quando o turno foi REPRODUZIDO (nada foi reservado, logo nada há a saldar — saldar aqui
// seria libertar/confirmar a reserva de outro turno).
func (rt *Runtime) settleTurn(ctx context.Context, goal Goal, stepID string, turn int, verdict TurnAdmissionVerdict, resp ModelResponse, failed bool) error {
	if rt.admission == nil || verdict.AlreadyAdmitted {
		return nil
	}
	return rt.admission.SettleTurn(ctx, TurnSettlement{
		RunID:        goal.RunID,
		StepID:       stepID,
		Turn:         turn,
		Usage:        resp.Usage,
		CostMicroUSD: resp.CostMicroUSD,
		Failed:       failed,
	})
}
