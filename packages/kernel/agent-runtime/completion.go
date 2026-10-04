package agentruntime

import (
	"errors"
	"fmt"
	"strings"
)

// O DESFECHO DE UM RUN É UM VEREDICTO DO KERNEL (AOS-493, ADR-037).
//
// Até aqui uma só condição respondia a três perguntas: «o modelo não pediu tools», «o run
// acabou» e «o run acabou bem». Um turno sem tool calls fechava o run como concluído, fosse o
// texto uma resposta, uma tool call escrita como texto, nada, ou uma resposta cortada.
//
// [TurnEndsRun] continua a responder à segunda pergunta, e só a ela. A terceira passa a ter
// resposta própria: [ConcludeRun] calcula o desfecho do run a partir do que foi de facto
// executado ([RunEvidence], os contadores do próprio loop) contra o contrato de conclusão que
// quem compõe o run declarou ([Goal.CompletionRequires]).
//
// TRÊS COISAS QUE ESTE FICHEIRO NÃO FAZ, DE PROPÓSITO:
//
//   - não olha para a FORMA do texto do modelo. Um classificador de conteúdo untrusted a
//     decidir o desfecho seria um canal do plano de dados para o plano de controlo;
//   - não lê evento nenhum. O desfecho de uma tool gravado pelo Reference Monitor
//     (`tool.call.outcome`) é opcional e fail-open: uma evidência lida de lá perdia-se quando
//     essa escrita falhasse;
//   - não repara. Um veredicto negativo fecha o run; dar outro turno ao modelo é outra fase.

// CompletionMode é o MODO DE APLICAÇÃO do veredicto, fixado por run.
type CompletionMode string

const (
	// CompletionOff — o veredicto não se calcula. É o valor-zero, o de todo o run gravado antes
	// deste ticket, e o de um [Goal] que não diga outra coisa.
	CompletionOff CompletionMode = "off"
	// CompletionObserve — o veredicto calcula-se e fica no [Result], mas o desfecho do run é o
	// de sempre.
	CompletionObserve CompletionMode = "observe"
	// CompletionEnforce — um veredicto negativo fecha o run como não cumprido.
	CompletionEnforce CompletionMode = "enforce"
)

// ErrUnknownCompletionMode — um modo de aplicação fora do vocabulário fechado. Fail-closed: o
// run não arranca, e um log cujo manifesto o traga não se reproduz «no modo mais parecido».
var ErrUnknownCompletionMode = errors.New("agentruntime: modo de aplicacao do veredicto desconhecido (aceites: off, observe, enforce)")

// ParseCompletionMode valida um modo escrito por fora (configuração do nó). Sem normalização
// de caixa nem valor por omissão: quem quer um valor por omissão decide-o antes de chamar.
func ParseCompletionMode(s string) (CompletionMode, error) {
	switch m := CompletionMode(s); m {
	case CompletionOff, CompletionObserve, CompletionEnforce:
		return m, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownCompletionMode, s)
}

// resolvido devolve o modo com o vazio lido como [CompletionOff], ou o erro de vocabulário.
func (m CompletionMode) resolvido() (CompletionMode, error) {
	if m == "" {
		return CompletionOff, nil
	}
	return ParseCompletionMode(string(m))
}

// OutcomeReason é a razão de um veredicto negativo, num vocabulário FECHADO. É o texto do
// `outcome_reason` da transição terminal e o rótulo da métrica: nunca leva texto de terceiros.
type OutcomeReason string

const (
	// OutcomeFulfilled — veredicto positivo. É o valor-zero.
	OutcomeFulfilled OutcomeReason = ""
	// OutcomeContractNoCall — o contrato não foi cumprido e a tool em falta nunca foi pedida.
	OutcomeContractNoCall OutcomeReason = "contract_unmet_no_call"
	// OutcomeContractAfterDenial — o contrato não foi cumprido e o último pedido da tool em
	// falta foi recusado (pelo Reference Monitor, pela reescrita do efeito, ou escalado).
	OutcomeContractAfterDenial OutcomeReason = "contract_unmet_after_denial"
	// OutcomeContractAfterToolError — o contrato não foi cumprido e o último pedido da tool em
	// falta foi permitido e falhou na execução.
	OutcomeContractAfterToolError OutcomeReason = "contract_unmet_after_tool_error"
	// OutcomeTruncated — o turno que acabou o run foi cortado pelo limite de tokens.
	OutcomeTruncated OutcomeReason = "truncated"
	// OutcomeEmptyOutput — o turno que acabou o run não trouxe texto nenhum.
	OutcomeEmptyOutput OutcomeReason = "empty_output"
)

// OutcomeReasons devolve as razões de veredicto negativo, numa ordem fixa.
func OutcomeReasons() []OutcomeReason {
	return []OutcomeReason{
		OutcomeContractNoCall, OutcomeContractAfterDenial, OutcomeContractAfterToolError,
		OutcomeTruncated, OutcomeEmptyOutput,
	}
}

// Desfechos de uma tool call, como o veredicto os nomeia.
const (
	// ToolOutcomeEffective — despachada, sem recusa e sem erro de tool.
	ToolOutcomeEffective = "effective"
	// ToolOutcomeDenied — recusada antes de produzir efeito.
	ToolOutcomeDenied = "denied"
	// ToolOutcomeFailed — permitida, e a execução falhou.
	ToolOutcomeFailed = "tool_error"
)

// Completion é o que o manifesto de cada turno grava sobre o veredicto do run: o modo com que
// o run correu e o contrato. É por aqui que o motor de replay chega ao mesmo desfecho que o
// loop sem conhecer a configuração do nó. Ausente do manifesto ⇒ modo [CompletionOff].
type Completion struct {
	Mode     CompletionMode `json:"mode"`
	Requires []string       `json:"requires,omitempty"`
}

// completionDoGoal resolve o modo e o contrato do run. Devolve nil com o modo desligado, para
// o manifesto de um run sem veredicto ficar com os bytes de antes.
func completionDoGoal(goal Goal) (*Completion, error) {
	modo, err := goal.CompletionMode.resolvido()
	if err != nil {
		return nil, err
	}
	if modo == CompletionOff {
		return nil, nil
	}
	return &Completion{Mode: modo, Requires: contratoNormalizado(goal.CompletionRequires)}, nil
}

// contratoNormalizado devolve o contrato sem nomes vazios nem repetidos, na ordem dada.
func contratoNormalizado(requires []string) []string {
	var out []string
	visto := make(map[string]bool, len(requires))
	for _, nome := range requires {
		if nome == "" || visto[nome] {
			continue
		}
		visto[nome] = true
		out = append(out, nome)
	}
	return out
}

// ToolEvidence é a linha do vector do veredicto para UMA tool exigida.
type ToolEvidence struct {
	// Tool é o nome da tool (o `ToolID`), como o contrato a declara.
	Tool string `json:"tool"`
	// Requested são as chamadas que o modelo pediu e o loop despachou.
	Requested int `json:"requested"`
	// Effective são as despachadas sem recusa e sem erro de tool.
	Effective int `json:"effective"`
	// Denied são as recusadas (incluindo as escaladas: nenhum efeito ocorreu).
	Denied int `json:"denied"`
	// Failed são as permitidas cuja execução falhou.
	Failed int `json:"failed"`
	// Last é o desfecho da última chamada desta tool. Vazio quando nunca foi pedida.
	Last string `json:"last,omitempty"`
}

// Verdict é o veredicto do kernel sobre a conclusão de um run.
type Verdict struct {
	// Mode é o modo com que o run correu ([CompletionObserve] ou [CompletionEnforce]).
	Mode CompletionMode `json:"mode"`
	// Fulfilled diz se o run concluiu. Reason é a razão quando não concluiu.
	Fulfilled bool          `json:"fulfilled"`
	Reason    OutcomeReason `json:"reason,omitempty"`
	// Tools é o vector: uma linha por tool do contrato, na ordem do contrato.
	Tools []ToolEvidence `json:"tools,omitempty"`
	// ToolCallsRequested é o total de tool calls pedidas no run, de qualquer tool.
	ToolCallsRequested int `json:"tool_calls_requested"`
}

// RunEvidence são os contadores de tool calls de um run, por tool. O loop alimenta-os com o
// que despachou em cada turno; o motor de replay, com o que a captura registou. Não é seguro
// para uso concorrente: pertence a um run.
type RunEvidence struct {
	porTool map[string]*ToolEvidence
	total   int
}

// NewRunEvidence devolve os contadores de um run que ainda não despachou nada.
func NewRunEvidence() *RunEvidence {
	return &RunEvidence{porTool: make(map[string]*ToolEvidence)}
}

// Observe soma as tool calls de um turno, na ordem de despacho. Uma chamada é EFECTIVA quando
// foi despachada sem recusa e sem erro de tool — a mesma condição com que o loop confirma a
// activity no checkpoint.
func (e *RunEvidence) Observe(results []CapturedToolResult) {
	for _, r := range results {
		linha := e.porTool[r.Invocation.ToolID]
		if linha == nil {
			linha = &ToolEvidence{Tool: r.Invocation.ToolID}
			e.porTool[r.Invocation.ToolID] = linha
		}
		e.total++
		linha.Requested++
		switch {
		case r.Denial != nil:
			linha.Denied++
			linha.Last = ToolOutcomeDenied
		case r.ToolError != nil:
			linha.Failed++
			linha.Last = ToolOutcomeFailed
		default:
			linha.Effective++
			linha.Last = ToolOutcomeEffective
		}
	}
}

// ToolCallsRequested é o total de tool calls despachadas no run.
func (e *RunEvidence) ToolCallsRequested() int { return e.total }

// linha devolve a evidência de uma tool (a zero quando nunca foi pedida).
func (e *RunEvidence) linha(tool string) ToolEvidence {
	if l := e.porTool[tool]; l != nil {
		return *l
	}
	return ToolEvidence{Tool: tool}
}

// Conclusion é o desfecho do run no turno que o termina.
type Conclusion struct {
	// Terminated e FinalText são o desfecho de um run que concluiu.
	Terminated bool
	FinalText  string
	// Unfulfilled diz que o run acabou SEM concluir: veredicto negativo em modo de imposição.
	// Não há texto final — o que o modelo escreveu não é resposta de nada.
	Unfulfilled bool
	// Verdict é o veredicto calculado. nil com o modo desligado.
	Verdict *Verdict
}

// ConcludeRun calcula o desfecho do run no turno que o termina ([TurnEndsRun] disse que sim).
// É a ÚNICA função que o decide: o loop e o motor de replay chamam-na com a mesma resposta, o
// mesmo [Completion] (o do manifesto do turno) e contadores alimentados da mesma maneira.
//
// A ORDEM DAS RAZÕES. Uma resposta cortada vem primeiro: o corte explica o resto (o modelo
// podia ir pedir a tool). Depois o contrato, pela primeira tool em falta na ordem em que foi
// declarado. Por fim a saída vazia. O vector vai sempre completo, pelo que a razão escolhida
// não esconde as outras.
//
// O QUE NÃO É VEREDICTO NEGATIVO: um motivo de paragem `content_filter` ou fora do mapa
// conhecido. O vocabulário de outros providers não está medido, e tratar o desconhecido como
// falha poria vermelhos todos os runs de um provider que diga outra palavra para «acabei».
func ConcludeRun(resp ModelResponse, c *Completion, e *RunEvidence) (Conclusion, error) {
	concluido := Conclusion{Terminated: true, FinalText: resp.Text}
	if c == nil {
		return concluido, nil
	}
	modo, err := c.Mode.resolvido()
	if err != nil {
		return Conclusion{}, err
	}
	if modo == CompletionOff {
		return concluido, nil
	}
	if e == nil {
		e = NewRunEvidence()
	}

	v := &Verdict{Mode: modo, ToolCallsRequested: e.ToolCallsRequested()}
	var doContrato OutcomeReason
	for _, tool := range contratoNormalizado(c.Requires) {
		linha := e.linha(tool)
		v.Tools = append(v.Tools, linha)
		if linha.Effective > 0 || doContrato != OutcomeFulfilled {
			continue
		}
		switch linha.Last {
		case ToolOutcomeDenied:
			doContrato = OutcomeContractAfterDenial
		case ToolOutcomeFailed:
			doContrato = OutcomeContractAfterToolError
		default:
			doContrato = OutcomeContractNoCall
		}
	}
	switch {
	case resp.StopReason.Normalizado() == StopLength:
		v.Reason = OutcomeTruncated
	case doContrato != OutcomeFulfilled:
		v.Reason = doContrato
	case strings.TrimSpace(resp.Text) == "":
		v.Reason = OutcomeEmptyOutput
	}
	v.Fulfilled = v.Reason == OutcomeFulfilled

	if !v.Fulfilled && modo == CompletionEnforce {
		return Conclusion{Unfulfilled: true, Verdict: v}, nil
	}
	concluido.Verdict = v
	return concluido, nil
}
