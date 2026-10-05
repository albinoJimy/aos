package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// A ORIGEM DA SAÍDA DE UM RUN (AOS-497, ADR-038).
//
// O veredicto do AOS-493 prova que a tool exigida correu com êxito. Não prova que a saída do run
// é o que ela devolveu: um run que chama a tool e escreve um resumo cumpre o contrato. Um run
// pode por isso DECLARAR que a sua saída é o resultado de uma tool ([Goal.OutputFromTool]); o
// kernel designa qual chamada é essa origem e devolve a ÂNCORA — a tool, o passo, o digest dos
// bytes do resultado, o tamanho e o estado — para ser selada na transição terminal, ao lado do
// veredicto.
//
// A DESIGNAÇÃO NÃO LÊ TEXTO NEM CONTEÚDO. Decide-se por três factos que o próprio runtime
// produziu: o nome da tool de cada chamada, o seu desfecho (efectiva, recusada, falhada) e o
// rótulo de autoridade do contexto do turno ([ContextAuthority], ADR-034). O texto do modelo e o
// conteúdo do resultado não entram: o resultado só é lido para lhe calcular o digest, depois de
// escolhido.
//
// O QUE ESTE FICHEIRO NÃO FAZ: não entrega a saída a ninguém, não muda o texto final do run e
// não acaba o run mais cedo. A âncora é um registo; quem a usa são os tickets seguintes.

// OutputSourceBinding é o VÍNCULO da declaração de origem, dado por quem compõe o run: diz se
// a falta de uma origem designável pode fechar o run como não cumprido.
type OutputSourceBinding string

const (
	// OutputSourceMeasure — só medição. A âncora calcula-se e sela-se; o veredicto e o desfecho
	// do run são exactamente os de um run sem declaração, qualquer que seja o modo do nó.
	OutputSourceMeasure OutputSourceBinding = "measure"
	// OutputSourceBinds — vinculativa. Com o veredicto em imposição ([CompletionEnforce]), uma
	// origem em falta ou ambígua é veredicto negativo. Fora da imposição comporta-se como
	// [OutputSourceMeasure].
	OutputSourceBinds OutputSourceBinding = "binding"
)

// OutputSourceBindings devolve os vínculos, numa ordem fixa. É um vocabulário FECHADO: serve de
// rótulo de métrica.
func OutputSourceBindings() []OutputSourceBinding {
	return []OutputSourceBinding{OutputSourceMeasure, OutputSourceBinds}
}

// OutputSourceState é o estado da designação, num vocabulário FECHADO.
type OutputSourceState string

const (
	// OutputSourceDesignated — exactamente uma chamada é a origem.
	OutputSourceDesignated OutputSourceState = "designated"
	// OutputSourceMissing — nenhuma chamada é a origem.
	OutputSourceMissing OutputSourceState = "missing"
	// OutputSourceAmbiguous — mais de uma chamada podia ser a origem. Não se escolhe nem se
	// concatena: seria o runtime a compor conteúdo.
	OutputSourceAmbiguous OutputSourceState = "ambiguous"
)

// OutputSourceStates devolve os estados da designação, numa ordem fixa.
func OutputSourceStates() []OutputSourceState {
	return []OutputSourceState{OutputSourceDesignated, OutputSourceMissing, OutputSourceAmbiguous}
}

// outputDigestAlgo é o prefixo do digest da âncora — a forma do digest de um `plan_input`, para
// quem transporta os bytes os conferir sem conversão.
const outputDigestAlgo = "sha256:"

// OutputSource é a ÂNCORA da saída de um run: o que se sela na transição terminal. Nunca leva
// conteúdo do titular — só o digest e metadados.
type OutputSource struct {
	// Tool é a tool declarada como origem ([Goal.OutputFromTool]).
	Tool string `json:"tool"`
	// Binding é o vínculo com que o run a declarou.
	Binding OutputSourceBinding `json:"binding"`
	// State é o estado da designação.
	State OutputSourceState `json:"state"`
	// StepID é o passo da chamada designada — o `step_id` do seu evento de mediação
	// ([ToolStepID]). Só em [OutputSourceDesignated].
	StepID string `json:"step_id,omitempty"`
	// Digest é `sha256:<hex>` dos bytes do resultado tal como a tool os devolveu. Só em
	// [OutputSourceDesignated].
	Digest string `json:"digest,omitempty"`
	// Bytes é o tamanho desse resultado. Só em [OutputSourceDesignated]; ausente no JSON quer
	// dizer zero.
	Bytes int `json:"bytes,omitempty"`
}

// Erros da declaração de origem. Os dois recusam o ARRANQUE do run, antes de qualquer evento.
var (
	// ErrImpossibleOutputSource — a tool declarada como origem não pode ser chamada neste run:
	// não está no tool set ([Goal.Tools]) ou a lista-branca ([Goal.AllowedTools]) não a admite.
	// É a regra de [ErrImpossibleCompletionContract]: deixá-lo correr acabava em «origem em
	// falta», que diz «o modelo não chamou» de um defeito de quem compôs o run.
	ErrImpossibleOutputSource = errors.New("agentruntime: origem da saida impossivel — a tool declarada nao pode ser chamada neste run")
	// ErrBadOutputSourceBinding — a declaração de origem está mal formada: tool declarada com
	// um vínculo fora do vocabulário (o vazio incluído), ou vínculo sem tool. Fail-closed: não há
	// vínculo por omissão, porque os dois valores decidem coisas opostas.
	ErrBadOutputSourceBinding = errors.New("agentruntime: declaracao de origem da saida mal formada (vinculos aceites: measure, binding; e so com a tool declarada)")
)

// vinculoValido diz se b é um dos vínculos do vocabulário.
func vinculoValido(b OutputSourceBinding) bool {
	return b == OutputSourceMeasure || b == OutputSourceBinds
}

// origemPossivel verifica, ANTES do primeiro turno, a declaração de origem que o run vai gravar
// no manifesto: vínculo no vocabulário e tool chamável neste run. c nil (veredicto desligado) ⇒
// a declaração é ignorada, como o contrato. Vale para os dois vínculos: uma medição sobre uma
// tool que o run não pode chamar mede um defeito de composição como se fosse do modelo.
func origemPossivel(c *Completion, goal Goal) error {
	if c == nil {
		return nil
	}
	if c.OutputFrom == "" {
		if c.OutputBinding != "" {
			return fmt.Errorf("%w: vinculo sem tool de origem", ErrBadOutputSourceBinding)
		}
		return nil
	}
	if !vinculoValido(c.OutputBinding) {
		// O valor recusado não vai na mensagem: vem de fora.
		return ErrBadOutputSourceBinding
	}
	oferecida := false
	for _, spec := range goal.Tools {
		if spec.Name == c.OutputFrom {
			oferecida = true
			break
		}
	}
	switch {
	case !oferecida:
		return fmt.Errorf("%w: %q nao esta no tool set do run", ErrImpossibleOutputSource, c.OutputFrom)
	case !referencemonitor.RunAllowsTool(goal.AllowedTools, c.OutputFrom):
		return fmt.Errorf("%w: %q esta fora da lista-branca do run", ErrImpossibleOutputSource, c.OutputFrom)
	}
	return nil
}

// primeiroDespacho são os factos do PRIMEIRO turno do run que despachou tool calls: o passo do
// turno, o rótulo do contexto que o modelo viu nele, e os resultados pela ordem de despacho.
type primeiroDespacho struct {
	stepID    string
	authority taint.Label
	results   []CapturedToolResult
}

// designar aplica a REGRA DE DESIGNAÇÃO (ADR-038 §2.2) aos factos guardados:
//
//	a origem é a chamada EFECTIVA (despachada, sem recusa e sem erro de tool) da tool declarada,
//	feita no primeiro turno do run que despachou tool calls, e só se o contexto desse turno era
//	trusted. Exactamente uma ⇒ designada. Nenhuma ⇒ em falta. Mais de uma ⇒ ambígua.
//
// PORQUE O CONTEXTO TRUSTED. É o que garante que os argumentos da chamada foram escolhidos por
// um modelo que só tinha visto o prefixo e o objectivo. Uma chamada pedida depois de conteúdo
// untrusted entrar no tail (um `plan_input`, memória, um resultado de tool) pode ter sido
// provocada por esse conteúdo, e nunca é a origem.
//
// LIMITE CONHECIDO. Se a tool declarada falha (ou é recusada) no primeiro turno com tools e tem
// êxito num turno posterior, o estado é «em falta»: o resultado da primeira tentativa já tornou
// o contexto untrusted.
func (e *RunEvidence) designar(tool string, binding OutputSourceBinding) *OutputSource {
	src := &OutputSource{Tool: tool, Binding: binding, State: OutputSourceMissing}
	if e == nil || e.primeiro == nil || !e.primeiro.authority.IsTrusted() {
		return src
	}
	candidata, candidatas := -1, 0
	for i, r := range e.primeiro.results {
		if r.Invocation.ToolID != tool || r.Denial != nil || r.ToolError != nil {
			continue
		}
		candidatas++
		if candidata < 0 {
			candidata = i
		}
	}
	switch {
	case candidatas == 0:
		return src
	case candidatas > 1:
		src.State = OutputSourceAmbiguous
		return src
	}
	// Os bytes do resultado tal como o despacho os devolveu: os mesmos de que saem o segmento
	// `tool_result` do tail e a captura do turno.
	valor := e.primeiro.results[candidata].Result.Value
	soma := sha256.Sum256(valor)
	src.State = OutputSourceDesignated
	src.StepID = ToolStepID(e.primeiro.stepID, candidata)
	src.Digest = outputDigestAlgo + hex.EncodeToString(soma[:])
	src.Bytes = len(valor)
	return src
}

// BemFormada diz se a âncora tem a forma que [RunEvidence.designar] produz: vocabulários
// fechados, e os campos da chamada designada presentes só em [OutputSourceDesignated]. É o que
// a máquina de estados pergunta antes de a gravar num evento em claro.
func (s *OutputSource) BemFormada() bool {
	if s == nil || !nomeDeToolImprimivel(s.Tool) || !vinculoValido(s.Binding) {
		return false
	}
	switch s.State {
	case OutputSourceDesignated:
		if _, _, ok := ToolStepParent(s.StepID); !ok || s.Bytes < 0 {
			return false
		}
		hexa, temAlgo := strings.CutPrefix(s.Digest, outputDigestAlgo)
		if !temAlgo || len(hexa) != sha256.Size*2 {
			return false
		}
		for _, c := range hexa {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return true
	case OutputSourceMissing, OutputSourceAmbiguous:
		return s.StepID == "" && s.Digest == "" && s.Bytes == 0
	}
	return false
}

// nomeDeToolImprimivel recusa um nome vazio, comprido ou com espaços e caracteres de controlo.
// O nome vem de quem compõe o run e o kernel já o confrontou com o tool set; isto só impede
// texto com quebras de linha de chegar a um evento em claro por outro chamador.
func nomeDeToolImprimivel(nome string) bool {
	if nome == "" || len(nome) > 128 {
		return false
	}
	for _, r := range nome {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
