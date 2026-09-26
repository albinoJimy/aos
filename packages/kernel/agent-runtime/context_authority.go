package agentruntime

import (
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AUTORIZAÇÃO DERIVADA DO CONTEXTO (AOS-069, ADR-034 — opção C).
//
// A autoridade de uma tool call NÃO é um campo da resposta do modelo. Até ao ADR-034 era:
// `ToolInvocation.AuthorizationTaint`, uma string que o adaptador do [ModelClient] podia
// preencher e que ninguém preenchia — pelo que TODAS as tool calls saíam untrusted, e o
// sistema não distinguia um nó cujo contexto só tinha o objectivo de um nó que tinha lido um
// `plan_input` untrusted (DEF-807). A autoridade passa a ser CUNHADA NO RUNTIME, a partir do
// que o próprio runtime pôs no contexto:
//
//	autorização(call pedida no turno t) = ⊔ { rótulo(s) : s entrou no tail antes do Assemble de t }
//
// O join é o do reticulado canónico ([taint.Join], {trusted ⊑ untrusted}): MONÓTONO — uma vez
// untrusted, o contexto do run nunca volta a trusted, nem por uma correcção de steer trusted.
// A fronteira UNTRUSTED (o modelo) não tem por onde o influenciar: o rótulo é função do TIPO dos
// segmentos que o runtime acrescenta, nunca do conteúdo deles nem de qualquer campo da resposta.
//
// # PORQUE É UMA FUNÇÃO DO TAIL, E NÃO ESTADO GUARDADO
//
// [ContextAuthority] é uma dobra PURA sobre a sequência de segmentos. O loop mantém o
// acumulado incrementalmente ([authorityWindow]), e o motor de replay recalcula-o sobre o tail
// que reconstrói — a mesma função sobre a mesma sequência dá o mesmo rótulo. Não há nada a
// capturar nem a gravar: a retoma (replay-then-continue, AOS-021) re-dobra o tail desde o
// turno 1 a partir das mesmas entradas (objectivo, `plan_input`, memória, respostas
// registadas), e o rótulo sai igual por construção.

// SegmentAuthority é o rótulo de AUTORIDADE com que um segmento do tail contribui para o
// contexto. producedUnder é o rótulo do contexto em que o segmento foi PRODUZIDO — só conta
// para o histórico (o texto do modelo herda a autoridade do contexto que o gerou).
//
//   - objectivo e correcção de steer ⇒ trusted: vêm de um humano autenticado (a submissão e o
//     canal de controlo), nunca do modelo nem de uma tool;
//   - histórico ⇒ producedUnder: o modelo não tem autoridade própria, só a do contexto que viu;
//   - plan_input, tool_result ⇒ untrusted: produto de outro run ou de uma tool;
//   - memória ⇒ untrusted, FAIL-CLOSED: nenhum caminho de produção a preenche hoje e, quando o
//     EPIC-04 a ligar, a proveniência que a elevaria ainda não chega aqui;
//   - qualquer outro kind (timestamp, ou um kind futuro por classificar) ⇒ untrusted.
//
// O prefixo (system + tool set congelado) é trusted e não passa por aqui: é o ponto de partida
// de [ContextAuthority].
func SegmentAuthority(kind TailKind, producedUnder taint.Label) taint.Label {
	switch kind {
	case TailObjective, TailCorrection:
		return taint.Trusted
	case TailHistory:
		return producedUnder
	default:
		return taint.Untrusted
	}
}

// ContextAuthority dobra [SegmentAuthority] sobre o tail, a partir do prefixo trusted. É o
// rótulo que autoriza as tool calls pedidas por um modelo que viu exactamente este tail. Sem
// segmentos devolve trusted (só o prefixo). Determinista e sem estado.
func ContextAuthority(tail []TailSegment) taint.Label {
	label := taint.Trusted
	for _, seg := range tail {
		label = taint.Join(label, SegmentAuthority(seg.Kind, label))
	}
	return label
}

// authorityWindow decora a [WindowPort] do run com o acumulado de [ContextAuthority]. É o
// ÚNICO sítio por onde o loop acrescenta ao tail, pelo que nenhum segmento pode entrar no
// prompt sem entrar também no rótulo — esquecer-se de o actualizar não é expressável.
type authorityWindow struct {
	WindowPort
	label taint.Label
}

// newAuthorityWindow começa no prefixo: trusted.
func newAuthorityWindow(w WindowPort) *authorityWindow {
	return &authorityWindow{WindowPort: w, label: taint.Trusted}
}

// Append junta o rótulo do segmento ao do contexto e só depois o entrega à janela.
func (w *authorityWindow) Append(seg TailSegment) {
	w.label = taint.Join(w.label, SegmentAuthority(seg.Kind, w.label))
	w.WindowPort.Append(seg)
}

// authority é o rótulo do contexto NESTE momento. O loop lê-o logo a seguir ao Assemble do
// turno: é esse o contexto que o modelo viu, e é esse o rótulo das tool calls que pedir.
func (w *authorityWindow) authority() taint.Label { return w.label }
