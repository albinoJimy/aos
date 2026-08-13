package otelgenai

// Contabilidade de tokens/custo por trajectória e por sub-árvore de delegação
// (AOS-078). Trabalha sobre os spans JÁ EMITIDOS (não altera a emissão): a unidade
// da verdade de cada turno de modelo é o span `chat`, que carrega os tokens reais
// (gen_ai.usage.input_tokens/output_tokens) e o custo derivado pela tabela de preços
// versionada do Model Gateway (aos.cost.micro_usd, inteiro; gen_ai.usage.cost_usd,
// float de conveniência).
//
// # Regra de NÃO-DUPLA-CONTAGEM (crítica)
//
// O span invoke_agent JÁ carrega o AGREGADO do seu run (soma dos turnos) e cada span
// chat carrega o SEU por-turno. Somar o custo/tokens sobre TODOS os spans de um trace
// DUPLICARIA (agregado do invoke_agent + por-turno dos chats). Além disso o
// execute_tool não tem custo de modelo. Por isso a agregação aqui conta o
// custo/tokens EXCLUSIVAMENTE ao nível dos spans `chat` — a única unidade-verdade de
// cada turno — e NUNCA lê o custo/tokens de invoke_agent nem de execute_tool. Sobre
// esses chats faz-se o rollup:
//
//   - OWN de um invoke_agent    = soma dos chats filhos DIRECTOS dele (parent_span_id);
//   - SUBTREE de um invoke_agent = soma dos chats em toda a sua sub-árvore (ele + os
//     invoke_agents descendentes, seguindo o parent_span_id para cima);
//   - por TRAJECTÓRIA (trace_id) = soma de TODOS os chats do trace.
//
// # Segunda regra de NÃO-DUPLA-CONTAGEM: chats ANINHADOS (AOS-259)
//
// A regra acima elimina a dupla-contagem VERTICAL entre operações diferentes
// (invoke_agent agregado vs. chat por-turno). Falta a HORIZONTAL, entre spans da MESMA
// operação: UM turno de modelo é observado por DUAS camadas quando ambas estão
// instrumentadas com o mesmo tracer — o Agent Runtime abre o seu span `chat` em volta
// da chamada (callModel) e o Model Gateway abre o SEU span `chat` em volta da mesma
// chamada, um ANINHADO no outro pelo ctx que atravessa a porta ModelClient. Os dois
// carregam os MESMOS tokens (o usage do provider) e — desde que o canal de custo de
// AOS-259 está ligado — o MESMO custo. Somar ambos daria TOKENS E CUSTO A DOBRAR: o
// burn-down por span avisaria ao dobro da velocidade e o prompt de exaustão dispararia
// cedo, uma REGRESSÃO de algo que hoje está certo (a contagem 1x com custo zero).
//
// A dedup é por PARENTESCO, não por heurística de valores (dois turnos legítimos podem
// ter tokens idênticos; comparar valores apagaria trabalho real). Um span `chat` NÃO é
// contado quando, subindo a cadeia de ancestrais, se encontra OUTRO `chat` ANTES de
// qualquer FRONTEIRA DE TURNO (invoke_agent ou execute_tool). Isto suprime exactamente
// a re-observação do mesmo turno pela camada de baixo (RT → GW) e preserva o caso
// legítimo da DELEGAÇÃO, em que o chat do sub-agente também tem um chat entre os seus
// ancestrais mas com as fronteiras execute_tool + invoke_agent pelo meio:
//
//	chat(RT) → chat(GW)                                 ⇒ conta 1 (o de fora)
//	chat(pai) → execute_tool → invoke_agent → chat(filho) ⇒ conta 2 (turnos distintos)
//
// Conta-se o span de FORA (o do RT) e não o de dentro: é o que carrega a correlação de
// trajectória (run_id/step_id) e é o único presente quando o GW não está a ser traçado —
// pelo que a leitura fica IDÊNTICA nas duas topologias, traçado ou não. Um chat cujo pai
// não esteja no conjunto de spans dado é tratado como o de fora (conta-se o que se vê).
//
// # Dinheiro em micro-USD INTEIRO
//
// O custo soma-se em micro-USD int64 (ADR-008): sem drift de vírgula flutuante. Um
// span que só traga o USD float ([AttrCostUSD]) é convertido com arredondamento
// round-half (tolerância de 1 micro-USD por span); o caminho normal traz o inteiro
// exacto [AttrCostMicroUSD], pelo que a agregação reconcilia com os totais do Model
// Gateway (Calculator/recorder) sem tolerância.

import (
	"math"
	"time"
)

// microUSDPerUSD é o factor micro-USD por USD (1 USD = 1_000_000 micro-USD), usado só
// na conversão do fallback float→inteiro. O caminho normal não passa por float.
const microUSDPerUSD = 1_000_000

// UsageTotals é o custo/tokens acumulado de um conjunto de turnos de modelo. O custo é
// SEMPRE micro-USD int64 (fonte de verdade, sem float); o USD deriva-se por [CostUSD].
type UsageTotals struct {
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
}

// TotalTokens é a soma input+output (o volume de tokens de modelo do conjunto).
func (u UsageTotals) TotalTokens() int64 { return u.InputTokens + u.OutputTokens }

// CostUSD converte o custo acumulado (micro-USD inteiro) para USD — só para
// apresentação; a fonte de verdade é [UsageTotals.CostMicroUSD].
func (u UsageTotals) CostUSD() float64 { return MicroUSDToUSD(u.CostMicroUSD) }

// add soma componente-a-componente (sem overflow-check: a contabilidade de custo
// exacta e overflow-checked é o Model Gateway; aqui é a projecção de leitura sobre
// spans já emitidos, cujos valores já couberam em int64 na emissão).
func (u UsageTotals) add(o UsageTotals) UsageTotals {
	return UsageTotals{
		InputTokens:  u.InputTokens + o.InputTokens,
		OutputTokens: u.OutputTokens + o.OutputTokens,
		CostMicroUSD: u.CostMicroUSD + o.CostMicroUSD,
	}
}

// TraceRollup é o rollup de custo/tokens de UMA trajectória (trace_id): o total do
// trace e, por cada invoke_agent, o seu OWN (chats filhos directos) e o seu SUBTREE
// (todos os chats descendentes). As chaves dos mapas são o span_id hex do
// invoke_agent.
type TraceRollup struct {
	// TraceIDHex é o trace_id da trajectória (hex minúsculo, formato OTLP).
	TraceIDHex string
	// Total é a soma de TODOS os chats do trace (a contabilidade por trajectória).
	Total UsageTotals
	// Chats é o nº de spans chat contados (turnos de modelo da trajectória).
	Chats int
	// OwnByAgent[spanHex] é a soma dos chats filhos DIRECTOS do invoke_agent spanHex.
	OwnByAgent map[string]UsageTotals
	// SubtreeByAgent[spanHex] é a soma dos chats de TODA a sub-árvore do invoke_agent
	// spanHex (ele + invoke_agents descendentes).
	SubtreeByAgent map[string]UsageTotals
}

// CostVelocity é o SINAL de cost/token velocity consumível pelo circuit breaker
// (AOS-080) e pelo orçamento por árvore (ADR-008). É uma estrutura PURA derivada dos
// spans chat de uma trajectória — NÃO implementa o disjuntor (isso é AOS-080), só
// expõe o custo/tokens acumulados e as taxas (por segundo de wall-clock e por turno).
type CostVelocity struct {
	// Totals é o custo/tokens acumulado dos turnos considerados.
	Totals UsageTotals
	// Turns é o nº de turnos de modelo (spans chat) considerados.
	Turns int
	// WallClock é a janela de tempo coberta pelos turnos: max(end)-min(start) sobre os
	// spans chat com timestamps válidos. Zero se os spans não modelam relógio (ex.:
	// [RecordingTracer]) — nesse caso as taxas por-segundo devolvem 0 e as por-turno
	// mantêm-se válidas.
	WallClock time.Duration
}

// TokensPerSecond é a token velocity (tokens de modelo por segundo de wall-clock). 0
// se a janela é não-positiva (sem relógio).
func (v CostVelocity) TokensPerSecond() float64 {
	s := v.WallClock.Seconds()
	if s <= 0 {
		return 0
	}
	return float64(v.Totals.TotalTokens()) / s
}

// CostMicroUSDPerSecond é a cost velocity em micro-USD por segundo de wall-clock. 0 se
// a janela é não-positiva.
func (v CostVelocity) CostMicroUSDPerSecond() float64 {
	s := v.WallClock.Seconds()
	if s <= 0 {
		return 0
	}
	return float64(v.Totals.CostMicroUSD) / s
}

// CostUSDPerSecond é a cost velocity em USD por segundo (conveniência de apresentação).
func (v CostVelocity) CostUSDPerSecond() float64 {
	return v.CostMicroUSDPerSecond() / float64(microUSDPerUSD)
}

// CostMicroUSDPerTurn é o custo médio por turno de modelo (micro-USD). 0 sem turnos.
func (v CostVelocity) CostMicroUSDPerTurn() float64 {
	if v.Turns == 0 {
		return 0
	}
	return float64(v.Totals.CostMicroUSD) / float64(v.Turns)
}

// TokensPerTurn é o nº médio de tokens de modelo por turno. 0 sem turnos.
func (v CostVelocity) TokensPerTurn() float64 {
	if v.Turns == 0 {
		return 0
	}
	return float64(v.Totals.TotalTokens()) / float64(v.Turns)
}

// chatSample é a projecção da unidade-verdade (um span chat): o seu custo/tokens, a
// sua posição na árvore (trace/parent) e a sua janela temporal.
type chatSample struct {
	traceHex  string
	parentHex string
	totals    UsageTotals
	startNano int64
	endNano   int64
}

// spanNode é a projecção de TOPOLOGIA de um span: o pai e a operação. Basta isto para
// resolver a cadeia de ancestrais (rollup por sub-árvore) e para decidir se um `chat` é
// uma re-observação ANINHADA do mesmo turno (dedup de AOS-259).
type spanNode struct {
	parentHex string
	op        string
}

// spanTopology indexa span_id hex → [spanNode] sobre TODOS os spans dados (chat,
// invoke_agent, execute_tool e quaisquer outros): a cadeia de ancestrais só se resolve
// com o grafo completo, não apenas com os chats.
func spanTopology(spans []SpanData) map[string]spanNode {
	nodes := make(map[string]spanNode, len(spans))
	for _, sd := range spans {
		nodes[sd.SpanContext.SpanIDHex()] = spanNode{parentHex: parentHexOf(sd), op: operationOf(sd)}
	}
	return nodes
}

// nestedChat indica que um `chat` com este pai é uma RE-OBSERVAÇÃO ANINHADA do MESMO
// turno de modelo (tipicamente o span do Model Gateway dentro do span do Agent Runtime)
// e portanto NÃO deve ser contado — ver a regra de dedup no cabeçalho do ficheiro.
//
// Sobe a cadeia de ancestrais e pára no primeiro veredicto: outro `chat` ⇒ aninhado
// (true); uma FRONTEIRA DE TURNO (invoke_agent/execute_tool) ⇒ turno próprio (false);
// pai fora do conjunto dado ou raiz ⇒ turno próprio (conta-se o que se vê). O guard
// `visited` protege contra um ciclo hipotético de parent_span_id (a árvore é acíclica
// por construção; a leitura é defensiva).
func nestedChat(parentHex string, nodes map[string]spanNode) bool {
	visited := make(map[string]bool)
	for cur := parentHex; cur != "" && !visited[cur]; {
		visited[cur] = true
		n, ok := nodes[cur]
		if !ok {
			return false
		}
		switch n.op {
		case OpChat:
			return true
		case OpInvokeAgent, OpExecuteTool:
			return false
		}
		cur = n.parentHex
	}
	return false
}

// countableChatSamples projecta os spans `chat` CONTÁVEIS do conjunto — os que são a
// unidade-verdade de um turno de modelo — descartando as re-observações aninhadas
// ([nestedChat]). Devolve também o índice de topologia, que o rollup por sub-árvore
// reutiliza sem o reconstruir. É o ÚNICO ponto por onde a contabilidade lê chats, para
// que a regra de dedup valha igualmente em [AggregateByTrace], [RollupByTrace] e
// [VelocityByTrace] — uma regra que valesse só num deles seria uma incoerência entre
// leituras do mesmo trace.
func countableChatSamples(spans []SpanData) ([]chatSample, map[string]spanNode) {
	nodes := spanTopology(spans)
	out := make([]chatSample, 0, len(spans))
	for _, sd := range spans {
		cs, ok := chatSampleFromSpanData(sd)
		if !ok {
			continue
		}
		if nestedChat(cs.parentHex, nodes) {
			continue
		}
		out = append(out, cs)
	}
	return out, nodes
}

// AggregateByTrace soma o custo/tokens dos spans `chat` por trajectória (trace_id).
// Conta SÓ chats — invoke_agent (agregado) e execute_tool são ignorados — e, entre os
// chats, SÓ os não-aninhados (um turno observado por RT e GW conta UMA vez). A chave do
// mapa é o trace_id hex.
func AggregateByTrace(spans []SpanData) map[string]UsageTotals {
	out := make(map[string]UsageTotals)
	samples, _ := countableChatSamples(spans)
	for _, cs := range samples {
		out[cs.traceHex] = out[cs.traceHex].add(cs.totals)
	}
	return out
}

// AggregateRecordedByTrace é [AggregateByTrace] sobre os spans de um [RecordingTracer].
func AggregateRecordedByTrace(spans []*RecordedSpan) map[string]UsageTotals {
	return AggregateByTrace(spanDataOf(spans))
}

// RollupByTrace calcula, por trajectória, o total e o OWN/SUBTREE de cada
// invoke_agent, contando SÓ os spans `chat` (sem dupla-contagem — o agregado do
// invoke_agent e o custo do execute_tool NUNCA são somados). Ver [TraceRollup].
func RollupByTrace(spans []SpanData) map[string]TraceRollup {
	// Chats CONTÁVEIS (sem re-observações aninhadas, AOS-259) + o índice de topologia:
	// span_id hex → (parent hex, operação), sobre TODOS os spans (chat, invoke_agent,
	// execute_tool) para resolver a cadeia de ancestrais.
	samples, nodes := countableChatSamples(spans)

	out := make(map[string]TraceRollup)
	for _, cs := range samples {
		tr, seen := out[cs.traceHex]
		if !seen {
			tr = TraceRollup{
				TraceIDHex:     cs.traceHex,
				OwnByAgent:     make(map[string]UsageTotals),
				SubtreeByAgent: make(map[string]UsageTotals),
			}
		}
		tr.Total = tr.Total.add(cs.totals)
		tr.Chats++

		// OWN: o pai imediato, se for invoke_agent.
		if p, ok := nodes[cs.parentHex]; ok && p.op == OpInvokeAgent {
			tr.OwnByAgent[cs.parentHex] = tr.OwnByAgent[cs.parentHex].add(cs.totals)
		}
		// SUBTREE: cada invoke_agent na cadeia de ancestrais do chat. O guard `visited`
		// protege contra um ciclo hipotético de parent_span_id (a árvore é acíclica por
		// construção, mas a leitura é defensiva).
		visited := make(map[string]bool)
		for cur := cs.parentHex; cur != "" && !visited[cur]; {
			visited[cur] = true
			n, ok := nodes[cur]
			if !ok {
				break
			}
			if n.op == OpInvokeAgent {
				tr.SubtreeByAgent[cur] = tr.SubtreeByAgent[cur].add(cs.totals)
			}
			cur = n.parentHex
		}

		out[cs.traceHex] = tr
	}
	return out
}

// RollupRecordedByTrace é [RollupByTrace] sobre os spans de um [RecordingTracer].
func RollupRecordedByTrace(spans []*RecordedSpan) map[string]TraceRollup {
	return RollupByTrace(spanDataOf(spans))
}

// VelocityByTrace deriva o sinal [CostVelocity] por trajectória, contando SÓ os spans
// `chat` não-aninhados (a mesma regra de dedup da agregação — caso contrário a
// velocidade que o disjuntor de AOS-080 lê viria ao dobro). A chave do mapa é o
// trace_id hex.
func VelocityByTrace(spans []SpanData) map[string]CostVelocity {
	samples, _ := countableChatSamples(spans)
	perTrace := make(map[string][]chatSample)
	for _, cs := range samples {
		perTrace[cs.traceHex] = append(perTrace[cs.traceHex], cs)
	}
	out := make(map[string]CostVelocity, len(perTrace))
	for tid, samples := range perTrace {
		out[tid] = velocityOf(samples)
	}
	return out
}

// VelocityRecordedByTrace é [VelocityByTrace] sobre um [RecordingTracer]. Nota: o
// RecordingTracer não modela relógio, pelo que [CostVelocity.WallClock] é 0 e as taxas
// por-segundo devolvem 0 (use um [SpanTracer] com clock para wall-clock real).
func VelocityRecordedByTrace(spans []*RecordedSpan) map[string]CostVelocity {
	return VelocityByTrace(spanDataOf(spans))
}

// velocityOf agrega os samples de uma trajectória num [CostVelocity], apurando a
// janela de wall-clock só sobre os samples com timestamps válidos (>0).
func velocityOf(samples []chatSample) CostVelocity {
	var v CostVelocity
	var minStart, maxEnd int64
	haveTime := false
	for _, s := range samples {
		v.Totals = v.Totals.add(s.totals)
		v.Turns++
		if s.startNano > 0 && s.endNano > 0 {
			if !haveTime || s.startNano < minStart {
				minStart = s.startNano
			}
			if !haveTime || s.endNano > maxEnd {
				maxEnd = s.endNano
			}
			haveTime = true
		}
	}
	if haveTime && maxEnd > minStart {
		v.WallClock = time.Duration(maxEnd - minStart)
	}
	return v
}

// chatSampleFromSpanData projecta um span `chat` na sua [chatSample]. ok=false se o
// span não é um chat (a operação vem de gen_ai.operation.name, ou do Name em fallback).
func chatSampleFromSpanData(sd SpanData) (chatSample, bool) {
	if operationOf(sd) != OpChat {
		return chatSample{}, false
	}
	var t UsageTotals
	if v, ok := sd.Attribute(AttrInputTokens); ok {
		if n, ok := attrInt64(v); ok {
			t.InputTokens = n
		}
	}
	if v, ok := sd.Attribute(AttrOutputTokens); ok {
		if n, ok := attrInt64(v); ok {
			t.OutputTokens = n
		}
	}
	t.CostMicroUSD = costMicroUSDOf(sd)
	return chatSample{
		traceHex:  sd.SpanContext.TraceIDHex(),
		parentHex: parentHexOf(sd),
		totals:    t,
		startNano: sd.StartUnixNano,
		endNano:   sd.EndUnixNano,
	}, true
}

// costMicroUSDOf lê o custo do span em micro-USD inteiro: prefere o inteiro exacto
// [AttrCostMicroUSD]; na sua ausência, converte o USD float [AttrCostUSD] com
// round-half (tolerância de 1 micro-USD). Ausente ambos ⇒ 0.
func costMicroUSDOf(sd SpanData) int64 {
	if v, ok := sd.Attribute(AttrCostMicroUSD); ok {
		if n, ok := attrInt64(v); ok {
			return n
		}
	}
	if v, ok := sd.Attribute(AttrCostUSD); ok {
		if f, ok := attrFloat64(v); ok {
			return int64(math.Round(f * float64(microUSDPerUSD)))
		}
	}
	return 0
}

// parentHexOf devolve o parent_span_id hex do span, ou "" se raiz (parent nulo).
func parentHexOf(sd SpanData) string {
	if sd.ParentSpanID == ([8]byte{}) {
		return ""
	}
	return spanIDHex(sd.ParentSpanID)
}

// spanDataOf projecta uma lista de [RecordedSpan] em [SpanData] (via ToSpanData). Os
// timestamps ficam a zero (o RecordingTracer não modela relógio) — só o eixo de
// custo/tokens/topologia é preservado, que é o que a agregação por trace usa.
func spanDataOf(recorded []*RecordedSpan) []SpanData {
	out := make([]SpanData, 0, len(recorded))
	for _, rs := range recorded {
		if rs == nil {
			continue
		}
		out = append(out, rs.ToSpanData())
	}
	return out
}

// attrInt64 lê um valor de atributo numérico inteiro (os tokens/custo são emitidos
// como int64, mas aceitamos os outros inteiros por robustez de leitura).
func attrInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case uint64:
		// Bounds-check antes da conversão: um uint64 acima de MaxInt64 transbordaria para
		// negativo. Tokens/custo emitidos cabem sempre em int64 (são int64 na emissão);
		// um valor fora de gama é leitura inválida ⇒ ok=false (não um total negativo).
		if x > math.MaxInt64 {
			return 0, false
		}
		return int64(x), true
	default:
		return 0, false
	}
}

// attrFloat64 lê um valor de atributo em vírgula flutuante (fallback de custo USD).
func attrFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	default:
		return 0, false
	}
}
