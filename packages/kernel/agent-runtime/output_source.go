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
	// OutputSourceDesignated — a tool declarada foi pedida exactamente uma vez no primeiro turno
	// que despachou tools, com contexto trusted, e essa chamada foi efectiva.
	OutputSourceDesignated OutputSourceState = "designated"
	// OutputSourceMissing — nenhuma chamada é a origem: a tool declarada não foi pedida nesse
	// turno, ou foi pedida uma vez e a chamada não foi efectiva, ou nenhum turno despachou tools.
	OutputSourceMissing OutputSourceState = "missing"
	// OutputSourceAmbiguous — a tool declarada foi PEDIDA mais de uma vez nesse turno, qualquer
	// que seja o desfecho de cada chamada. Não se escolhe nem se concatena: seria o runtime a
	// compor conteúdo, ou quem controla a falha de uma chamada a escolher a outra.
	OutputSourceAmbiguous OutputSourceState = "ambiguous"
	// OutputSourceInapplicable — a regra não se pode aplicar a este run: o contexto do primeiro
	// turno que despachou tools já era untrusted. Como um turno sem tool calls termina o run, não
	// há resultado de tool antes desse turno; o que o tornou untrusted foram as ENTRADAS do run
	// (um `plan_input`, memória). É um defeito de quem compôs o run — um consumidor declarado
	// como produtor — e não «o modelo não chamou»: por isso tem estado próprio.
	OutputSourceInapplicable OutputSourceState = "inapplicable"
)

// OutputSourceStates devolve os estados da designação, numa ordem fixa.
func OutputSourceStates() []OutputSourceState {
	return []OutputSourceState{OutputSourceDesignated, OutputSourceMissing, OutputSourceAmbiguous, OutputSourceInapplicable}
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
	// StepID é o passo da chamada designada — o `step_id` dos seus eventos de mediação
	// ([ToolStepID]). Só em [OutputSourceDesignated]. Num run retomado o mesmo passo pode ter
	// mais de um evento (uma escalada e, depois da aprovação, a mediação que executou): o passo
	// identifica a CHAMADA, não um evento.
	StepID string `json:"step_id,omitempty"`
	// Digest é `sha256:<hex>` dos bytes do resultado tal como o despacho os devolveu NESTA vida
	// do run — o `result_hash` do step-ledger na via durável. Só em [OutputSourceDesignated].
	Digest string `json:"digest,omitempty"`
	// Bytes é o tamanho desse resultado. Só em [OutputSourceDesignated]; ausente no JSON quer
	// dizer zero.
	Bytes int `json:"bytes,omitempty"`
}

// Erros da declaração de origem. Os dois recusam o ARRANQUE do run, antes de qualquer evento.
var (
	// ErrImpossibleOutputSource — a tool declarada como origem não pode ser a origem deste run:
	// não está no tool set ([Goal.Tools]), a lista-branca ([Goal.AllowedTools]) não a admite, ou
	// o seu nome não tem a forma que a âncora selada admite ([OutputSource.BemFormada]).
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
// no manifesto: vínculo no vocabulário, nome com a forma que o selo admite e tool chamável neste
// run. c nil (veredicto desligado) ⇒ a declaração é ignorada, como o contrato. Vale para os dois
// vínculos: uma medição sobre uma tool que o run não pode chamar mede um defeito de composição
// como se fosse do modelo.
//
// A FORMA DO NOME VALIDA-SE AQUI COM A FUNÇÃO DO SELO ([nomeDeToolImprimivel], a que
// [OutputSource.BemFormada] usa). Um nome que o tool set aceita e a máquina de estados recusa
// deixava o run correr, terminar em memória e ficar sem transição terminal — em `running` no
// log, indistinguível de um crash. As duas pontas perguntam o mesmo à mesma função, pelo que uma
// âncora que este run produza é sempre selável.
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
	if !nomeDeToolImprimivel(c.OutputFrom) {
		// O nome recusado não vai na mensagem: pode ser comprido ou trazer quebras de linha.
		return fmt.Errorf("%w: o nome da tool de origem nao tem a forma que a ancora selada admite (ate %d bytes, sem espacos nem caracteres de controlo)", ErrImpossibleOutputSource, maxNomeDeTool)
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

// ErrOutputSourceNotFollowed — [ConcludeRun] recebeu uma origem declarada que a evidência não
// estava a seguir quando observou o primeiro turno com tools ([RunEvidence.FollowOutputFrom]).
// Os factos da designação — e o digest — guardam-se quando o turno é observado; sem eles não há
// âncora honesta a calcular depois. Fail-closed: nem «em falta» nem «designada».
var ErrOutputSourceNotFollowed = errors.New("agentruntime: a evidencia do run nao seguiu a tool declarada como origem da saida quando observou o primeiro turno com tools")

// primeiroDespacho são os factos do PRIMEIRO turno do run que despachou tool calls, tirados no
// momento em que o turno foi observado: o passo do turno, o rótulo do contexto que o modelo viu
// nele e, sobre a tool declarada como origem, quantas vezes foi pedida e — quando foi pedida uma
// só vez — o índice, o desfecho e o digest dessa chamada.
//
// NÃO GUARDA OS RESULTADOS. O digest calcula-se aqui, uma vez, sobre os bytes que o despacho
// acabou de devolver; uma escrita posterior nesses bytes (que o tail e a captura partilham) já
// não o pode mudar.
type primeiroDespacho struct {
	stepID    string
	authority taint.Label
	// origem é a tool que a evidência seguia quando o turno foi observado. Vazia ⇒ o run não
	// declarou a origem e nada mais se calculou.
	origem string
	// pedidas é o número de chamadas da tool declarada neste turno, com qualquer desfecho.
	pedidas int
	// indice, efectiva, digest e bytes são os da PRIMEIRA chamada da tool declarada; só contam
	// quando pedidas == 1. O digest só existe quando essa chamada foi efectiva.
	indice   int
	efectiva bool
	digest   string
	bytes    int
}

// observarPrimeiroDespacho tira os factos do primeiro turno que despachou tool calls. origem
// vazia (run sem declaração) ⇒ nenhum resultado é lido.
func observarPrimeiroDespacho(stepID string, authority taint.Label, origem string, results []CapturedToolResult) *primeiroDespacho {
	p := &primeiroDespacho{stepID: stepID, authority: authority, origem: origem, indice: -1}
	if origem == "" {
		return p
	}
	for i, r := range results {
		if r.Invocation.ToolID != origem {
			continue
		}
		p.pedidas++
		if p.pedidas == 1 {
			p.indice = i
			p.efectiva = r.Denial == nil && r.ToolError == nil
		}
	}
	if p.pedidas == 1 && p.efectiva {
		// Os bytes do resultado tal como o despacho os devolveu nesta vida do run: os do
		// segmento `tool_result` do tail e, na via durável, os do `result_hash` do step-ledger.
		valor := results[p.indice].Result.Value
		soma := sha256.Sum256(valor)
		p.digest = outputDigestAlgo + hex.EncodeToString(soma[:])
		p.bytes = len(valor)
	}
	return p
}

// designar aplica a REGRA DE DESIGNAÇÃO (ADR-038 §2.2) aos factos guardados:
//
//	no primeiro turno do run que despachou tool calls, e só se o contexto desse turno era
//	trusted, a tool declarada foi PEDIDA exactamente uma vez e essa chamada foi EFECTIVA
//	(despachada, sem recusa e sem erro de tool) ⇒ designada.
//	Pedida mais de uma vez nesse turno, qualquer que seja o desfecho de cada chamada ⇒ ambígua.
//	Pedida uma vez e não efectiva, não pedida, ou nenhum turno despachou tools ⇒ em falta.
//	Contexto desse turno untrusted ⇒ não aplicável.
//
// PORQUE O CONTEXTO TRUSTED. É o que garante que os argumentos da chamada foram escolhidos por
// um modelo que só tinha visto o prefixo e o objectivo. Uma chamada pedida depois de conteúdo
// untrusted entrar no tail (um `plan_input`, memória, um resultado de tool) pode ter sido
// provocada por esse conteúdo, e nunca é a origem.
//
// PORQUE SE CONTAM AS PEDIDAS E NÃO AS EFECTIVAS. Com duas chamadas da tool declarada no mesmo
// turno, contar só as efectivas deixava quem controla a falha de uma delas (um servidor que
// devolve erro, um recurso que a política nega) escolher qual dos dois resultados fica selado
// como a saída. As duas foram pedidas pelo modelo; o desfecho honesto desse turno é «ambígua».
//
// LIMITE CONHECIDO. Se a tool declarada falha (ou é recusada) no primeiro turno com tools e tem
// êxito num turno posterior, o estado é «em falta»: o resultado da primeira tentativa já tornou
// o contexto untrusted.
func (e *RunEvidence) designar(tool string, binding OutputSourceBinding) (*OutputSource, error) {
	src := &OutputSource{Tool: tool, Binding: binding, State: OutputSourceMissing}
	if e == nil || e.primeiro == nil {
		return src, nil
	}
	p := e.primeiro
	if p.origem != tool {
		return nil, ErrOutputSourceNotFollowed
	}
	switch {
	case !p.authority.IsTrusted():
		src.State = OutputSourceInapplicable
	case p.pedidas > 1:
		src.State = OutputSourceAmbiguous
	case p.pedidas == 1 && p.efectiva:
		src.State = OutputSourceDesignated
		src.StepID = ToolStepID(p.stepID, p.indice)
		src.Digest = p.digest
		src.Bytes = p.bytes
	}
	return src, nil
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
	case OutputSourceMissing, OutputSourceAmbiguous, OutputSourceInapplicable:
		return s.StepID == "" && s.Digest == "" && s.Bytes == 0
	}
	return false
}

// maxNomeDeTool é o comprimento máximo, em bytes, do nome de tool que uma âncora leva.
const maxNomeDeTool = 128

// nomeDeToolImprimivel recusa um nome vazio, comprido ou com espaços e caracteres de controlo.
// É a ÚNICA regra de forma do nome da tool de origem, perguntada nas duas pontas: no arranque
// ([origemPossivel], que recusa o run) e no selo ([OutputSource.BemFormada], que recusa a
// transição). O que ela impede é texto com quebras de linha num evento em claro.
func nomeDeToolImprimivel(nome string) bool {
	if nome == "" || len(nome) > maxNomeDeTool {
		return false
	}
	for _, r := range nome {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
