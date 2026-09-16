package otelgenai

// A AMOSTRA DO SLI — qual dos `execute_tool` conta, e QUE NÚMERO desse span é lido.
//
// São duas escolhas independentes, e durante um ano só a primeira estava feita.
//
// QUAL CONTA. `execute_tool` é emitido por TRÊS produtores:
//
//	worker   (agent-runtime/worker)  — gate fenced + ledger.Apply(mediação + despacho)
//	launcher (substrate/sandbox)     — ciclo de vida do sandbox
//	monitor  (reference-monitor)     — cadeia de política + DESPACHO da tool
//
// Só o monitor decide, e o discriminador é a DECISÃO: [Monitor.Mediate] anota-a num `defer`
// que cobre todos os caminhos de retorno e a cadeia é fail-closed; os outros dois nunca a
// anotam. Inferir pela parentela seria mais frágil — o RM instrumenta QUALQUER chamador
// (ADR-002), pelo que o pai de uma mediação nem sempre é um `execute_tool`.
//
// QUE NÚMERO. A latência do span do monitor NÃO É overhead de mediação: `Monitor.evaluate`
// despacha ANTES de devolver a decisão, pelo que o span fecha depois de a tool correr. Era
// DEF-281, e custou um alerta `critical` (RB-04) a tocar em cada tool call de qualquer nó
// com sandbox real — 1,21 s contra 15 ms no incidente de produção de 2026-09-15/16, 3,047 s
// na medição de 2026-08-27. A partir de AOS-398 o span traz [AttrMediationDecisionLatencyNanos],
// a janela da decisão medida no kernel ANTES do despacho, e é ESSA que o SLI lê.

import (
	"testing"
	"time"
)

// spanDoMonitor é o span do Reference Monitor: decidiu, e traz as DUAS durações — a da
// decisão (o que a mediação acrescenta) e a do span inteiro (decisão + execução da tool).
func spanDoMonitor(trace string, decisao, total time.Duration) WideEvent {
	return WideEvent{
		Operation:    OpExecuteTool,
		TraceIDHex:   trace,
		Decision:     DecisionPermit,
		LatencyNanos: int64(total),
		Attributes: map[string]any{
			AttrMediationDecisionLatencyNanos: int64(decisao),
		},
	}
}

// spanDoWorker é o span exterior: mesma operação, SEM decisão. Não é mediação e não pode
// entrar na amostra.
func spanDoWorker(trace string, d time.Duration) WideEvent {
	return WideEvent{Operation: OpExecuteTool, TraceIDHex: trace, LatencyNanos: int64(d)}
}

// TestOverheadP95_ExecucaoLongaNoSandboxNaoViolaOSLODaDecisao é o teste de regressão do
// incidente de produção de 2026-09-15/16 (run `run-delegado-1789519407`).
//
// Os números são os medidos: a decisão custou 8,6 ms e a tool `doc_read` correu 1,2 s em
// gVisor. Contra um SLO de 15 ms que exprime OVERHEAD DE DECISÃO, isto é uma tool call
// perfeitamente saudável. Antes do AOS-398 o SLI lia a janela do span e publicava
// `1.21099128e+09` ns — 80× o tecto —, com `mediation_overhead_high` e
// `mediation_overhead_p95_high` a acender em `critical` e a mandar o operador para o RB-04
// («Falha de PDP»). Um alerta que toca sempre ensina a ignorá-lo.
func TestOverheadP95_ExecucaoLongaNoSandboxNaoViolaOSLODaDecisao(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoMonitor("t1", 8600*time.Microsecond, 1210991280*time.Nanosecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Samples != 1 {
		t.Fatalf("a mediação aconteceu e foi medida: esperava 1 amostra, vieram %d", sli.Samples)
	}
	if got := time.Duration(sli.Value); got != 8600*time.Microsecond {
		t.Fatalf("p95 = %v — o SLI tem de publicar a janela da DECISÃO (8,6ms), não a do span (1,21s)", got)
	}
	if !sli.Met {
		t.Fatalf("uma tool call com 8,6ms de decisão CUMPRE um SLO de 15ms; o SLI diz que não (valor=%v)",
			time.Duration(sli.Value))
	}
	if len(sli.Offenders) != 0 {
		t.Fatalf("um SLO cumprido não tem infractores; vieram %v", sli.Offenders)
	}
}

// TestOverheadP95_ContinuaADispararQuandoADecisaoEstaLenta é a metade que impede a correcção
// de se tornar um silenciador. Separar a medida remove ruído; não pode remover o SINAL.
//
// Sem este caso, `return sli` logo no início passaria os outros testes — um SLI que nunca
// dispara cumpre sempre.
func TestOverheadP95_ContinuaADispararQuandoADecisaoEstaLenta(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		// A tool é rápida; é a CADEIA DE POLÍTICA que degradou. É o caso que o RB-04 descreve.
		spanDoMonitor("t1", 50*time.Millisecond, 55*time.Millisecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Met {
		t.Fatal("uma decisão de 50ms VIOLA um SLO de 15ms — o SLI tinha de o dizer")
	}
	if len(sli.Offenders) == 0 {
		t.Fatal("um SLI degradado tem de nomear os trace_ids para o drill-down")
	}
}

// TestOverheadP95_ContaSoOSpanDoMonitor tranca a escolha da POPULAÇÃO: entre os spans com o
// mesmo nome, conta-se o de quem decidiu. Sem o filtro o percentil mistura duas populações.
func TestOverheadP95_ContaSoOSpanDoMonitor(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoWorker("t1", 4017*time.Millisecond), // a tool a correr em gVisor, vista de fora
		spanDoMonitor("t1", 5*time.Millisecond, 4010*time.Millisecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Samples != 1 {
		t.Fatalf("a amostra tem de conter SÓ o span do monitor: %d amostras", sli.Samples)
	}
	if got := time.Duration(sli.Value); got != 5*time.Millisecond {
		t.Fatalf("p95 = %v — devia ser a decisão do MONITOR (5ms)", got)
	}
	if !sli.Met {
		t.Fatalf("5ms de decisão CUMPREM um SLO de 15ms; o SLI diz que não (valor=%v)", time.Duration(sli.Value))
	}
}

// TestOverheadP95_SpanQueDecideMasNaoMedeADecisaoNaoEntra — um RM anterior ao AOS-398 decide
// mas não publica a janela da decisão. NÃO se cai para a latência do span: seria exactamente
// o número errado que este SLI deixou de publicar.
//
// `Samples == 0` é a direcção honesta — faz o rótulo `avaliavel="0"` dizer a verdade e o
// operador vê que não há sinal, em vez de ver um sinal falso.
func TestOverheadP95_SpanQueDecideMasNaoMedeADecisaoNaoEntra(t *testing.T) {
	semMedida := WideEvent{
		Operation:    OpExecuteTool,
		TraceIDHex:   "t1",
		Decision:     DecisionPermit,
		LatencyNanos: int64(3 * time.Second),
	}

	sli := overheadP95SLI([]WideEvent{semMedida}, int64(15*time.Millisecond))

	if sli.Samples != 0 {
		t.Fatalf("sem a janela da decisão não há amostra: %d", sli.Samples)
	}
	if sli.Value != 0 {
		t.Fatalf("sem amostra o valor fica em zero, não herdado do span: %v", sli.Value)
	}
	if !sli.Met {
		t.Fatal("ausência de dados não é violação (AOS-085): um SLI sem amostras não pode disparar")
	}
}

// TestOverheadP95_DecisaoInstantaneaEAmostraLegitima separa "medida zero" de "sem medida".
// Um relógio de teste pode devolver zero, e isso é uma decisão medida — conta.
func TestOverheadP95_DecisaoInstantaneaEAmostraLegitima(t *testing.T) {
	sli := overheadP95SLI([]WideEvent{spanDoMonitor("t1", 0, 2*time.Second)}, int64(15*time.Millisecond))

	if sli.Samples != 1 {
		t.Fatalf("uma decisão medida em zero é uma amostra: %d", sli.Samples)
	}
	if !sli.Met {
		t.Fatal("zero cumpre qualquer tecto positivo")
	}
}

// TestOverheadP95_SemSpansComDecisaoNaoEAvaliado: um nó cujo tráfego só produziu spans
// exteriores não tem nada para reportar.
func TestOverheadP95_SemSpansComDecisaoNaoEAvaliado(t *testing.T) {
	sli := overheadP95SLI([]WideEvent{spanDoWorker("t1", time.Second)}, int64(15*time.Millisecond))

	if sli.Samples != 0 {
		t.Fatalf("sem spans com decisão não há amostra: %d", sli.Samples)
	}
	if sli.Value != 0 {
		t.Fatalf("sem amostra o valor tem de ficar em zero, não inventado: %v", sli.Value)
	}
}

// TestOverheadP95_DerivaAJanelaDoBagDoSpan fecha o percurso ponta-a-ponta: o atributo que o
// Reference Monitor anota no span sobrevive à projecção em [WideEvent] e chega ao SLI. Sem
// esta ligação a correcção do kernel ficava presa ao kernel.
func TestOverheadP95_DerivaAJanelaDoBagDoSpan(t *testing.T) {
	sd := SpanData{
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrDecision, Value: DecisionPermit},
			{Key: AttrMediationDecisionLatencyNanos, Value: int64(6 * time.Millisecond)},
		},
		StartUnixNano: 1,
		EndUnixNano:   1 + int64(1200*time.Millisecond),
	}

	we := WideEventFromSpanData(sd)

	if we.MediationDecisionLatencyNanos != int64(6*time.Millisecond) {
		t.Fatalf("o campo tipado não derivou do bag: %v", time.Duration(we.MediationDecisionLatencyNanos))
	}
	if we.LatencyNanos != int64(1200*time.Millisecond) {
		t.Fatalf("a latência do span tem de continuar a ser a janela INTEIRA: %v", time.Duration(we.LatencyNanos))
	}

	sli := overheadP95SLI([]WideEvent{we}, int64(15*time.Millisecond))
	if !sli.Met || time.Duration(sli.Value) != 6*time.Millisecond {
		t.Fatalf("o SLI devia ler 6ms e cumprir; leu %v (met=%v)", time.Duration(sli.Value), sli.Met)
	}
}

// TestAOS398_OIncidenteDeProducaoJaNaoAcendeNenhumCritico percorre a cadeia INTEIRA — SLI,
// catálogo de mediação e catálogo operacional — com os dados do run `run-delegado-1789519407`.
//
// É o teste que separa "o SLI reporta outro número" de "o operador deixa de ser acordado".
// Sem ele, um alerta ligado à latência do span noutro sítio do catálogo passaria despercebido.
func TestAOS398_OIncidenteDeProducaoJaNaoAcendeNenhumCritico(t *testing.T) {
	// Uma tool call `doc_read`: decisão em 8,6ms, execução em gVisor em 1,21s.
	incidente := []WideEvent{spanDoMonitor("t1", 8600*time.Microsecond, 1210991280*time.Nanosecond)}

	dash := BuildDashboard(incidente, nil, DefaultSLOConfig())
	for _, a := range EvaluateAlerts(dash, DefaultAlertConfig()) {
		if a.Fired && a.SLI == SLIMediationOverheadP95 {
			t.Errorf("catálogo `mediation`: %q disparou (%s) — uma tool call normal não é uma avaria do PDP",
				a.Name, a.Message)
		}
	}

	opSnap := DefaultDashboardCatalog().Render(OperationalInputs{Events: incidente})
	for _, a := range EvaluateOperationalAlerts(opSnap, DefaultOperationalAlertConfig()) {
		if a.Fired && a.SLI == SLIMediationOverheadP95 {
			t.Errorf("catálogo `operational`: %q disparou (%s) — era este o alerta que acordava o turno",
				a.Name, a.Message)
		}
	}
}

// TestAOS398_UmaDecisaoDegradadaContinuaAAcenderOCritico é a outra metade, e a que impede
// esta correcção de ser um silenciador: quando a CADEIA DE POLÍTICA é que está lenta — o
// sintoma que o RB-04 descreve —, o `critical` tem de acender como sempre acendeu.
func TestAOS398_UmaDecisaoDegradadaContinuaAAcenderOCritico(t *testing.T) {
	// A tool é rápida (30ms); é o PDP que demora 120ms a decidir.
	degradado := []WideEvent{spanDoMonitor("t1", 120*time.Millisecond, 150*time.Millisecond)}

	dash := BuildDashboard(degradado, nil, DefaultSLOConfig())
	if !temAlertaDisparado(EvaluateAlerts(dash, DefaultAlertConfig()), AlertMediationOverheadHigh) {
		t.Error("catálogo `mediation`: uma decisão de 120ms contra 15ms tinha de acender mediation_overhead_high")
	}

	opSnap := DefaultDashboardCatalog().Render(OperationalInputs{Events: degradado})
	if !temAlertaDisparado(EvaluateOperationalAlerts(opSnap, DefaultOperationalAlertConfig()), AlertMediationOverheadP95High) {
		t.Error("catálogo `operational`: uma decisão de 120ms contra 15ms tinha de acender mediation_overhead_p95_high")
	}
}

func temAlertaDisparado(alerts []Alert, nome string) bool {
	for _, a := range alerts {
		if a.Name == nome && a.Fired {
			return true
		}
	}
	return false
}
