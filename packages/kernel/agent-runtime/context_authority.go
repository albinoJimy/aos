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
//   - notice ⇒ trusted (AOS-489): é texto FIXO do runtime — o modelo consegue provocá-lo,
//     não escrevê-lo. Como o join é monótono não devolve autoridade a um contexto untrusted, e
//     na prática nunca a muda: sai sempre a seguir a um `tool_result`;
//   - histórico ⇒ producedUnder: o modelo não tem autoridade própria, só a do contexto que viu;
//   - tool_call ⇒ producedUnder, pela MESMA razão (AOS-489): é o pedido que o modelo fez, e o
//     modelo só tem a autoridade do contexto que viu. Não ELEVA — os argumentos são texto do
//     modelo, e um pedido de tool não é um humano autenticado. Não BAIXA — registar no tail
//     uma chamada que o modelo já tinha feito não acrescenta conteúdo de terceiros; quem torna
//     o contexto untrusted é o `tool_result` que vem logo a seguir. O caso é EXPLÍCITO, e não
//     o `default`, para que a autoridade de nenhuma trajectória mude com a entrada do
//     segmento: com `producedUnder`, inseri-lo em qualquer ponto do tail deixa a dobra igual;
//   - plan_input, tool_result ⇒ untrusted: produto de outro run ou de uma tool;
//   - memória ⇒ untrusted, FAIL-CLOSED: nenhum caminho de produção a preenche hoje e, quando o
//     EPIC-04 a ligar, a proveniência que a elevaria ainda não chega aqui;
//   - qualquer outro kind (timestamp, ou um kind futuro por classificar) ⇒ untrusted.
//
// O prefixo (system + tool set congelado) é trusted e não passa por aqui: é o ponto de partida
// de [ContextAuthority].
func SegmentAuthority(kind TailKind, producedUnder taint.Label) taint.Label {
	switch kind {
	case TailObjective, TailCorrection, TailNotice:
		return taint.Trusted
	case TailHistory, TailToolCall:
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

// SegmentFloor é o PISO que um segmento põe na autoridade de uma call — o rótulo que a
// autoridade por argumento (AOS-551) NÃO pode levantar.
//
// POR QUE EXISTE UM SEGUNDO ACUMULADOR, e não uma emenda a [SegmentAuthority]. A autoridade do
// contexto pergunta «o que é que este modelo VIU?», e por isso um `plan_input` ou um
// `tool_result` caem no `default: untrusted` dela — e bem, nos layouts em que levam o payload.
// Num layout POR HANDLE (a 1.8.0) esses segmentos têm corpo FIXO escrito pelo runtime: o modelo
// não viu conteúdo nenhum neles, e é esse o limite que a autoridade por argumento levanta.
// Emendar [SegmentAuthority] mudaria o rótulo de TODOS os layouts, incluindo os que estão em
// produção; o piso é uma pergunta diferente e tem acumulador próprio.
//
// O QUE CONTRIBUI PARA O PISO é «conteúdo que o modelo LÊ e que o runtime não escreveu».
//
// A REGRA É DIRIGIDA PELO RÓTULO, não pelo kind nem pelo nome da tool, e foi uma revisão
// adversarial que a obrigou. A primeira versão perguntava «é um `plan_input`?» e respondia
// trusted, assumindo que num layout por handle o corpo é sempre fixo. Não é:
//
//   - a `media_view` é EXCEPÇÃO ao caminho dos handles (`segmentoDeResultado`): o seu
//     `tool_result` leva a `MediaRef` INLINE e não tem rótulo `tool_error`, pelo que a regra por
//     kind declarava trusted um corpo que o runtime não escreveu. O que tapava o buraco era uma
//     coincidência noutro ficheiro — o segmento [TailMedia] que a 1.7.0 acrescenta a seguir —, e
//     uma coincidência não é uma garantia;
//   - `entradasPorHandle` e `resultadosPorHandle` são flags SEPARADAS. Hoje a 1.8.0 liga as duas,
//     mas um layout futuro que ligue só uma fazia a regra por kind declarar trusted o segmento
//     que ainda leva o payload.
//
// O rótulo do handle ([PlanInputHandleLabel], [ToolResultHandleLabel]) está presente EXACTAMENTE
// quando o corpo é o texto fixo — é o mesmo sítio que o escreve. Perguntar pelo rótulo é
// perguntar pela propriedade, e não por um proxy dela: os dois casos acima fecham-se juntos, sem
// nomes de tool e sem dependência de outro ficheiro.
//
// AS DUAS ENTRADAS QUE A ENUMERAÇÃO ENCONTROU, e que uma suposição teria perdido:
//
//   - [TailMemory] — o contexto de memória entra INLINE, e a 1.8.0 não o põe atrás de um handle.
//     Um piso só com media deixaria um run com memória levantar para trusted tendo o modelo lido
//     conteúdo de fora.
//   - [TailToolResult] COM `tool_error` — a mensagem de um erro de tool é `error.Error()`, texto
//     LIVRE de uma tool. Hoje já não tem o rótulo do handle (o corpo não é fixo), pelo que a regra
//     nova o apanha sem caso próprio.
//
// E [TailMedia] contribui sempre: a projecção anexa a imagem à mensagem (a funcionalidade da R1a,
// `projection_media.go`), pelo que num run com media NADA se levanta — a decisão (c) do dono de
// 2026-10-23 e a correcção 1 do AOS-551.
//
// O `steer` (`control.TailSteer`) cai no `default` e contribui UNTRUSTED. É conservador de
// propósito: o [SegmentAuthority] também o põe lá hoje, e mudar isso é uma decisão de segurança
// que não pertence a esta fatia. CONSEQUÊNCIA DECLARADA: num run da 1.8.0 um steer põe o piso no
// chão para sempre, isto é, a fase não levanta nada em runs esteerados. A constante vive no
// pacote `control`, que importa este — por isso não há cá referência a ela.
//
// FORA DE UM LAYOUT POR HANDLE o piso É a autoridade, byte a byte: sem as duas flags delega em
// [SegmentAuthority]. Assim usar o piso num layout que leva os payloads inline não muda nada — a
// utilização errada não é expressável, em vez de ser desencorajada.
func SegmentFloor(seg TailSegment, producedUnder taint.Label, entradasPorHandle, resultadosPorHandle bool) taint.Label {
	if !entradasPorHandle && !resultadosPorHandle {
		return SegmentAuthority(seg.Kind, producedUnder)
	}
	switch seg.Kind {
	case TailObjective, TailCorrection, TailNotice:
		// Escritos pelo submissor, por um humano autenticado pelo canal de controlo, ou pelo
		// runtime com corpo de vocabulário fechado.
		return taint.Trusted
	case TailHistory, TailToolCall:
		// O texto e os argumentos do próprio modelo: herdam, como em [SegmentAuthority].
		return producedUnder
	case TailPlanInput:
		// Trusted SÓ com o rótulo do handle, que é o que diz que o corpo é o texto fixo.
		if entradasPorHandle && segmentoTemRotulo(seg, PlanInputHandleLabel) {
			return taint.Trusted
		}
		return taint.Untrusted
	case TailToolResult:
		// Idem. Apanha de uma vez o erro de tool, a excepção da `media_view` e um layout que
		// ponha só as entradas por handle: em todos eles o rótulo não está lá.
		if resultadosPorHandle && segmentoTemRotulo(seg, ToolResultHandleLabel) {
			return taint.Trusted
		}
		return taint.Untrusted
	default:
		// [TailMemory], [TailMedia], [TailTimestamp], o `steer`, e qualquer kind que venha a
		// existir: FAIL-CLOSED. O `timestamp` está aqui e não no caso trusted porque o kernel
		// NÃO o escreve — quem o emite é a memória de trabalho (`platform/memory/working`), fora
		// do seu controlo; classificar por nome um kind que não se produz é a excepção exacta
		// que este `default` existe para não ter.
		return taint.Untrusted
	}
}

// segmentoTemRotulo diz se o segmento leva o rótulo dado na linha de delimitação.
func segmentoTemRotulo(seg TailSegment, chave string) bool {
	for _, m := range seg.Meta {
		if m.Key == chave {
			return true
		}
	}
	return false
}

// ContextFloor dobra [SegmentFloor] sobre o tail, a partir do prefixo trusted — o análogo de
// [ContextAuthority] para o piso. É o que quem RECONSTRÓI um tail precisa: o motor de replay não
// sabe que handles cada call resolveu (a captura não o grava), mas sabe o piso, e o piso é o
// LIMITE que a autoridade de qualquer call desse turno tem de respeitar.
//
// Determinista e sem estado, como [ContextAuthority]. Sem segmentos devolve trusted.
func ContextFloor(tail []TailSegment, entradasPorHandle, resultadosPorHandle bool) taint.Label {
	label := taint.Trusted
	for _, seg := range tail {
		label = taint.Join(label, SegmentFloor(seg, label, entradasPorHandle, resultadosPorHandle))
	}
	return label
}

// authorityWindow decora a [WindowPort] do run com o acumulado de [ContextAuthority]. É o
// ÚNICO sítio por onde o loop acrescenta ao tail, pelo que nenhum segmento pode entrar no
// prompt sem entrar também no rótulo — esquecer-se de o actualizar não é expressável.
//
// Acumula DOIS rótulos pela mesma razão: o piso ([SegmentFloor]) tem de passar pelo mesmo ponto
// único, senão um segmento podia entrar no prompt sem entrar no piso.
type authorityWindow struct {
	WindowPort
	label taint.Label
	piso  taint.Label
	// entradasPorHandle/resultadosPorHandle são as DUAS flags do layout, separadas. Cada `case`
	// de [SegmentFloor] consulta a sua: um layout que ligue só uma não pode declarar trusted o
	// segmento que ainda leva o payload.
	entradasPorHandle   bool
	resultadosPorHandle bool
}

// newAuthorityWindow começa no prefixo: trusted, nos dois acumuladores.
func newAuthorityWindow(w WindowPort, entradasPorHandle, resultadosPorHandle bool) *authorityWindow {
	return &authorityWindow{
		WindowPort: w, label: taint.Trusted, piso: taint.Trusted,
		entradasPorHandle: entradasPorHandle, resultadosPorHandle: resultadosPorHandle,
	}
}

// Append junta o rótulo do segmento ao do contexto E ao piso, e só depois o entrega à janela.
func (w *authorityWindow) Append(seg TailSegment) {
	w.label = taint.Join(w.label, SegmentAuthority(seg.Kind, w.label))
	w.piso = taint.Join(w.piso, SegmentFloor(seg, w.piso, w.entradasPorHandle, w.resultadosPorHandle))
	w.WindowPort.Append(seg)
}

// authority é o rótulo do contexto NESTE momento. O loop lê-o logo a seguir ao Assemble do
// turno: é esse o contexto que o modelo viu, e é esse o rótulo das tool calls que pedir.
func (w *authorityWindow) authority() taint.Label { return w.label }

// floor é o PISO da autoridade NESTE momento — o rótulo que a autoridade por argumento não pode
// levantar ([SegmentFloor]). Fora de um layout por handle é igual a [authorityWindow.authority].
func (w *authorityWindow) floor() taint.Label { return w.piso }
