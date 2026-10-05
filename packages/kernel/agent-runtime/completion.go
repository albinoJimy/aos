package agentruntime

import (
	"errors"
	"fmt"
	"strings"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
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
	// OutcomeOutputSourceMissing — o run declarou a origem da saída como vinculativa
	// ([OutputSourceBinds]) e nenhuma chamada é designável (AOS-497, ADR-038).
	OutcomeOutputSourceMissing OutcomeReason = "output_source_missing"
	// OutcomeOutputSourceAmbiguous — idem, e mais de uma chamada podia ser a origem.
	OutcomeOutputSourceAmbiguous OutcomeReason = "output_source_ambiguous"
)

// OutcomeReasons devolve as razões de veredicto negativo, numa ordem fixa.
func OutcomeReasons() []OutcomeReason {
	return []OutcomeReason{
		OutcomeContractNoCall, OutcomeContractAfterDenial, OutcomeContractAfterToolError,
		OutcomeTruncated, OutcomeEmptyOutput,
		OutcomeOutputSourceMissing, OutcomeOutputSourceAmbiguous,
	}
}

// NoVocabulario diz se a razão pertence ao vocabulário fechado: a vazia ([OutcomeFulfilled]) ou
// uma de [OutcomeReasons]. É o que a máquina de estados pergunta antes de gravar um
// `outcome_reason` — o campo nunca leva texto de terceiros.
func (r OutcomeReason) NoVocabulario() bool {
	if r == OutcomeFulfilled {
		return true
	}
	for _, conhecida := range OutcomeReasons() {
		if r == conhecida {
			return true
		}
	}
	return false
}

// Desfechos de uma tool call, como o veredicto os nomeia.
const (
	// ToolOutcomeEffective — despachada, sem recusa e sem erro de tool.
	ToolOutcomeEffective = "effective"
	// ToolOutcomeDenied — recusada antes de produzir efeito.
	ToolOutcomeDenied = "denied"
	// ToolOutcomeFailed — permitida, e a execução falhou.
	ToolOutcomeFailed = "tool_error"
	// ToolOutcomeNone — o run não despachou nenhuma tool call. Só existe em
	// [RunEvidence.LastToolOutcome]; a linha de uma tool nunca pedida fica com `Last` vazio.
	ToolOutcomeNone = "none"
)

// LastToolOutcomes devolve, numa ordem fixa, os valores de [RunEvidence.LastToolOutcome]. É um
// vocabulário FECHADO: serve de rótulo de métrica.
func LastToolOutcomes() []string {
	return []string{ToolOutcomeNone, ToolOutcomeEffective, ToolOutcomeDenied, ToolOutcomeFailed}
}

// ErrImpossibleCompletionContract — o contrato de conclusão exige uma tool que o run não pode
// chamar: não está no tool set do run ([Goal.Tools]) ou a lista-branca do run
// ([Goal.AllowedTools]) não a admite. Fail-closed: o run não arranca. Deixá-lo correr gastava
// todos os turnos para acabar em `contract_unmet_no_call`, indistinguível de «o modelo não
// chamou» — quando o defeito é de quem compôs o run.
var ErrImpossibleCompletionContract = errors.New("agentruntime: contrato de conclusao impossivel de cumprir — exige uma tool que o run nao pode chamar")

// Completion é o que o manifesto de cada turno grava sobre o veredicto do run: o modo com que
// o run correu e o contrato. É por aqui que o motor de replay chega ao mesmo desfecho que o
// loop sem conhecer a configuração do nó. Ausente do manifesto ⇒ modo [CompletionOff].
//
// OutputFrom e OutputBinding são a declaração de origem da saída do run (AOS-497, ADR-038): a
// tool e o vínculo. `omitempty`: um run sem declaração grava os bytes de antes.
type Completion struct {
	Mode          CompletionMode      `json:"mode"`
	Requires      []string            `json:"requires,omitempty"`
	OutputFrom    string              `json:"output_from,omitempty"`
	OutputBinding OutputSourceBinding `json:"output_binding,omitempty"`
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
	return &Completion{
		Mode:          modo,
		Requires:      contratoNormalizado(goal.CompletionRequires),
		OutputFrom:    goal.OutputFromTool,
		OutputBinding: goal.OutputSourceBinding,
	}, nil
}

// contratoPossivel verifica, ANTES do primeiro turno, que cada tool do contrato pode ser chamada
// neste run: consta do tool set oferecido ([Goal.Tools], por nome) e a lista-branca do run
// admite-a ([referencemonitor.RunAllowsTool] — a mesma regra que o Reference Monitor impõe em
// cada chamada e com que a oferta é cortada, AOS-485/AOS-486). c nil (modo desligado) ⇒ o
// contrato é ignorado, como em todo o resto.
//
// A COMPARAÇÃO É EXACTA, como a do Reference Monitor: `Doc_Read` ou ` doc_read` não são
// `doc_read`, e caem aqui. Normalizar a caixa ou aparar espaços deste lado punha o contrato a
// aceitar um nome que a mediação recusa.
//
// O erro diz todas as tools em falta e, para cada uma, qual das duas condições falhou. Os
// nomes vêm de quem compõe o run, nunca de conteúdo do modelo.
func contratoPossivel(c *Completion, goal Goal) error {
	if c == nil {
		return nil
	}
	oferecidas := make(map[string]bool, len(goal.Tools))
	for _, spec := range goal.Tools {
		oferecidas[spec.Name] = true
	}
	var faltas []string
	for _, tool := range c.Requires {
		switch {
		case !oferecidas[tool]:
			faltas = append(faltas, fmt.Sprintf("%q nao esta no tool set do run", tool))
		case !referencemonitor.RunAllowsTool(goal.AllowedTools, tool):
			faltas = append(faltas, fmt.Sprintf("%q esta fora da lista-branca do run", tool))
		}
	}
	if len(faltas) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrImpossibleCompletionContract, strings.Join(faltas, "; "))
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
	// ultimoTurno é o desfecho do último turno que despachou tool calls — ver
	// [RunEvidence.LastToolOutcome]. Vazio enquanto nenhum despachou.
	ultimoTurno string
	// primeiro são os factos do primeiro turno que despachou tool calls: é sobre eles que a
	// origem da saída se designa ([RunEvidence.designar], AOS-497). nil enquanto nenhum despachou.
	primeiro *primeiroDespacho
}

// NewRunEvidence devolve os contadores de um run que ainda não despachou nada.
func NewRunEvidence() *RunEvidence {
	return &RunEvidence{porTool: make(map[string]*ToolEvidence)}
}

// Observe soma as tool calls de um turno, na ordem de despacho. Uma chamada é EFECTIVA quando
// foi despachada sem recusa e sem erro de tool — a mesma condição com que o loop confirma a
// activity no checkpoint.
//
// stepID é o passo do turno e authority o rótulo do contexto que o modelo viu nele
// ([ContextAuthority], lido a seguir ao Assemble). Só se guardam do PRIMEIRO turno que despachou
// tool calls, e servem a designação da origem da saída (AOS-497). Nada é lido nem calculado
// sobre os resultados aqui: o digest só se calcula em [ConcludeRun], e só num run que declarou
// a origem.
func (e *RunEvidence) Observe(stepID string, authority taint.Label, results []CapturedToolResult) {
	if len(results) == 0 {
		// Um turno sem tool calls não muda nada: o «último turno que despachou» continua a
		// ser o anterior.
		return
	}
	if e.primeiro == nil {
		e.primeiro = &primeiroDespacho{stepID: stepID, authority: authority, results: append([]CapturedToolResult(nil), results...)}
	}
	negadas, falhadas := 0, 0
	defer func() {
		switch {
		case negadas > 0:
			e.ultimoTurno = ToolOutcomeDenied
		case falhadas > 0:
			e.ultimoTurno = ToolOutcomeFailed
		default:
			e.ultimoTurno = ToolOutcomeEffective
		}
	}()
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
			negadas++
			linha.Denied++
			linha.Last = ToolOutcomeDenied
		case r.ToolError != nil:
			falhadas++
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

// LastToolOutcome diz SOBRE O QUE o run acabou: o desfecho do último turno que despachou tool
// calls, de QUALQUER tool (não só as do contrato). [ToolOutcomeNone] quando o run não despachou
// nenhuma; [ToolOutcomeDenied] quando pelo menos uma chamada desse turno foi recusada;
// [ToolOutcomeFailed] quando nenhuma foi recusada e pelo menos uma falhou;
// [ToolOutcomeEffective] quando todas foram efectivas.
//
// PORQUE O PIOR DO TURNO E NÃO A ÚLTIMA CHAMADA. As chamadas de um turno são paralelas para o
// modelo: ele vê os resultados todos de uma vez. Num turno `[lida recusada, escrita efectiva]`
// a última chamada é efectiva e o modelo acabou de ver uma recusa.
//
// É MEDIÇÃO (AOS-493, ADR-037 §5): não entra no veredicto nem em evento nenhum. Um run que
// acaba sobre uma recusa com o contrato cumprido, ou sem contrato, tem veredicto positivo; é
// por aqui que essa classe se conta.
func (e *RunEvidence) LastToolOutcome() string {
	if e == nil || e.ultimoTurno == "" {
		return ToolOutcomeNone
	}
	return e.ultimoTurno
}

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
	// OutputSource é a âncora da saída (AOS-497). nil quando o run não declarou a origem ou
	// corria com o modo desligado. Acompanha o desfecho nos dois sentidos: concluído ou não.
	OutputSource *OutputSource
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
// A ORIGEM DA SAÍDA (AOS-497, ADR-038). Com a origem declarada no [Completion], a âncora
// calcula-se sempre e vai na [Conclusion]. Só entra no VEREDICTO quando a declaração é
// vinculativa ([OutputSourceBinds]) e o modo é de imposição: aí uma origem em falta ou ambígua
// é razão negativa, DEPOIS do contrato (que diz porque não há chamada efectiva) e antes da saída
// vazia — e a saída vazia passa a ser a dos bytes designados, não a do texto. Em qualquer outra
// combinação o veredicto é exactamente o de um run sem declaração.
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
	var origem *OutputSource
	if c.OutputFrom != "" {
		origem = e.designar(c.OutputFrom, c.OutputBinding)
	}
	vincula := origem != nil && c.OutputBinding == OutputSourceBinds && modo == CompletionEnforce
	switch {
	case resp.StopReason.Normalizado() == StopLength:
		v.Reason = OutcomeTruncated
	case doContrato != OutcomeFulfilled:
		v.Reason = doContrato
	case vincula && origem.State == OutputSourceMissing:
		v.Reason = OutcomeOutputSourceMissing
	case vincula && origem.State == OutputSourceAmbiguous:
		v.Reason = OutcomeOutputSourceAmbiguous
	case vincula:
		// A saída é o resultado designado: vazia quer dizer zero bytes. O texto não conta.
		if origem.Bytes == 0 {
			v.Reason = OutcomeEmptyOutput
		}
	case strings.TrimSpace(resp.Text) == "":
		v.Reason = OutcomeEmptyOutput
	}
	v.Fulfilled = v.Reason == OutcomeFulfilled

	if !v.Fulfilled && modo == CompletionEnforce {
		return Conclusion{Unfulfilled: true, Verdict: v, OutputSource: origem}, nil
	}
	concluido.Verdict = v
	concluido.OutputSource = origem
	return concluido, nil
}
