package otelgenai

import "testing"

// AOS-259 — DEDUP DE CHATS ANINHADOS POR PARENTESCO.
//
// Ligar o canal de custo (o custo derivado pelo Model Gateway a viajar até ao runtime)
// faz nascer, quando AMBAS as camadas estão traçadas com o mesmo tracer, DOIS spans
// `chat` para UM só turno de modelo: o do Agent Runtime (em volta de callModel) e o do
// Model Gateway (em volta da mesma chamada, aninhado pelo ctx). Ambos carregam os MESMOS
// tokens e o MESMO custo. Somá-los duplicaria as duas grandezas — e a de tokens JÁ ESTAVA
// CERTA antes deste ticket, pelo que a duplicação seria uma REGRESSÃO introduzida pelo
// próprio canal de custo.
//
// Estes testes fixam a regra: suprime-se o chat ANINHADO (o de dentro) e conta-se o de
// fora, MAS só quando não há fronteira de turno pelo meio — a delegação
// (chat → execute_tool → invoke_agent → chat) são turnos DISTINTOS e tem de continuar a
// contar dois.

// TestAOS259_ChatAninhado_TokensUmaVez_ComCustoReal é o teste que a AC2 exige: prova, NA
// MESMA PASSAGEM, que os tokens contam 1x E que o custo que sobrevive é o real (não-nulo,
// exactamente o do turno). Provar só um dos dois deixaria passar a metade errada — um
// agregador que zerasse o custo também daria "tokens 1x".
func TestAOS259_ChatAninhado_TokensUmaVez_ComCustoReal(t *testing.T) {
	t.Parallel()

	// UM turno de modelo, observado por duas camadas:
	//   invoke_agent(0x10) → chat RT(0x11) → chat GW(0x12)
	// Os dois chats carregam os MESMOS valores porque são a MESMA chamada ao provider.
	const (
		in       = int64(1200)
		out      = int64(340)
		microUSD = int64(8_700) // custo REAL derivado da tabela de preços, não uma constante decorativa
	)
	spans := []SpanData{
		agentSpan(0x01, 0x10, 0x00, in, out, microUSD), // agregado do run — nunca somado
		chatSpan(0x01, 0x11, 0x10, in, out, microUSD),  // chat do Agent Runtime (o de fora)
		chatSpan(0x01, 0x12, 0x11, in, out, microUSD),  // chat do Model Gateway (aninhado)
	}

	got := AggregateByTrace(spans)[SpanContext{TraceID: traceID(0x01)}.TraceIDHex()]

	// (a) TOKENS uma vez só.
	if got.InputTokens != in || got.OutputTokens != out {
		t.Errorf("tokens duplicados: got in=%d out=%d, quer in=%d out=%d (um turno observado por RT e GW conta UMA vez)",
			got.InputTokens, got.OutputTokens, in, out)
	}
	// (b) CUSTO REAL na MESMA passagem — não-nulo e igual ao do turno.
	if got.CostMicroUSD != microUSD {
		t.Errorf("custo = %d micro-USD, quer %d (o custo real do turno, contado uma vez)", got.CostMicroUSD, microUSD)
	}
	if got.CostMicroUSD == 0 {
		t.Error("custo ZERO: a dedup nao pode apagar o custo — a AC exige tokens 1x COM custo real")
	}

	// GÉMEO INDEPENDENTE da soma NAIVE (soma de todos os chats sem olhar ao parentesco),
	// construído aqui e não derivado da função sob teste: se a dedup não estivesse a
	// funcionar, o agregado seria igual a este.
	var naive UsageTotals
	for _, sd := range spans {
		if operationOf(sd) != OpChat {
			continue
		}
		naive.InputTokens += in
		naive.OutputTokens += out
		naive.CostMicroUSD += microUSD
	}
	if got == naive {
		t.Fatalf("dupla-contagem: agregado %+v == soma naive dos chats %+v", got, naive)
	}
}

// TestAOS259_Delegacao_NaoESuprimida guarda o falso-positivo que a regra ingénua ("existe
// um chat entre os ancestrais ⇒ suprime") causaria: o chat de um SUB-AGENTE tem sempre um
// chat entre os ancestrais (o turno do pai que decidiu delegar), mas é um turno de modelo
// REAL e distinto. Suprimi-lo apagaria trabalho pago — o modo de falha oposto e igualmente
// grave. A fronteira que os separa é execute_tool/invoke_agent.
func TestAOS259_Delegacao_NaoESuprimida(t *testing.T) {
	t.Parallel()

	// invoke_agent pai(0x10) → chat pai(0x11) → execute_tool(0x12) → invoke_agent filho(0x13) → chat filho(0x14)
	spans := []SpanData{
		agentSpan(0x02, 0x10, 0x00, 0, 0, 0),
		chatSpan(0x02, 0x11, 0x10, 100, 20, 1_000),
		toolSpan(0x02, 0x12, 0x11),
		agentSpan(0x02, 0x13, 0x12, 0, 0, 0),
		chatSpan(0x02, 0x14, 0x13, 300, 40, 3_000),
	}

	got := AggregateByTrace(spans)[SpanContext{TraceID: traceID(0x02)}.TraceIDHex()]
	if got.InputTokens != 400 || got.OutputTokens != 60 || got.CostMicroUSD != 4_000 {
		t.Fatalf("delegacao mal contada: got %+v, quer {In:400 Out:60 Cost:4000} (dois turnos DISTINTOS, ambos contam)", got)
	}
}

// TestAOS259_ChatSemPaiVisivel_Conta fixa a postura para um conjunto de spans PARCIAL (o
// caso real de quem filtra os spans de um run antes de agregar): sem o pai à vista não se
// pode concluir que o chat é aninhado, e a leitura conta o que vê. A alternativa —
// descartar por dúvida — perderia consumo real.
func TestAOS259_ChatSemPaiVisivel_Conta(t *testing.T) {
	t.Parallel()

	spans := []SpanData{chatSpan(0x03, 0x21, 0x20, 7, 3, 500)} // pai 0x20 ausente do conjunto
	got := AggregateByTrace(spans)[SpanContext{TraceID: traceID(0x03)}.TraceIDHex()]
	if got.InputTokens != 7 || got.OutputTokens != 3 || got.CostMicroUSD != 500 {
		t.Fatalf("chat sem pai visivel foi descartado: got %+v, quer {In:7 Out:3 Cost:500}", got)
	}
}

// TestAOS259_RollupEVelocity_HerdamADedup garante que a regra vale em TODAS as leituras do
// mesmo trace. Se valesse só na agregação por trajectória, o rollup por sub-árvore e a
// velocidade que o disjuntor lê continuariam ao dobro — e duas leituras da mesma
// trajectória discordariam entre si, que é pior do que ambas estarem erradas.
func TestAOS259_RollupEVelocity_HerdamADedup(t *testing.T) {
	t.Parallel()

	const (
		in       = int64(50)
		out      = int64(10)
		microUSD = int64(2_500)
	)
	spans := []SpanData{
		agentSpan(0x04, 0x30, 0x00, in, out, microUSD),
		chatSpan(0x04, 0x31, 0x30, in, out, microUSD), // RT
		chatSpan(0x04, 0x32, 0x31, in, out, microUSD), // GW (aninhado)
	}
	traceHex := SpanContext{TraceID: traceID(0x04)}.TraceIDHex()
	agentHex := spanIDHex(spanID(0x30))

	roll := RollupByTrace(spans)[traceHex]
	if roll.Chats != 1 {
		t.Errorf("rollup contou %d chats, quer 1 (o turno e um so)", roll.Chats)
	}
	if roll.Total.CostMicroUSD != microUSD || roll.Total.TotalTokens() != in+out {
		t.Errorf("rollup total = %+v, quer custo=%d tokens=%d", roll.Total, microUSD, in+out)
	}
	if own := roll.OwnByAgent[agentHex]; own.CostMicroUSD != microUSD || own.TotalTokens() != in+out {
		t.Errorf("OWN do invoke_agent = %+v, quer custo=%d tokens=%d", own, microUSD, in+out)
	}
	if sub := roll.SubtreeByAgent[agentHex]; sub.CostMicroUSD != microUSD || sub.TotalTokens() != in+out {
		t.Errorf("SUBTREE do invoke_agent = %+v, quer custo=%d tokens=%d", sub, microUSD, in+out)
	}

	vel := VelocityByTrace(spans)[traceHex]
	if vel.Turns != 1 {
		t.Errorf("velocity contou %d turnos, quer 1", vel.Turns)
	}
	if vel.Totals.CostMicroUSD != microUSD || vel.Totals.TotalTokens() != in+out {
		t.Errorf("velocity totals = %+v, quer custo=%d tokens=%d", vel.Totals, microUSD, in+out)
	}
}
