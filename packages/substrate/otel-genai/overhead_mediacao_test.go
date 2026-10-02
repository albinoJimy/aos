package otelgenai

// A AMOSTRA DO SLI — qual dos `execute_tool` conta, e QUE NÚMERO desse span é lido.
//
// São duas escolhas independentes, e errou-se a segunda duas vezes.
//
// QUAL CONTA. `execute_tool` é emitido por TRÊS produtores:
//
//	worker   (agent-runtime/worker)  — gate fenced + ledger.Apply(mediação + despacho)
//	launcher (substrate/sandbox)     — ciclo de vida do sandbox
//	monitor  (reference-monitor)     — cadeia de política + selo + DESPACHO da tool
//
// Só o monitor decide, e o discriminador é a DECISÃO: [Monitor.Mediate] anota-a num `defer`
// que cobre todos os caminhos de retorno e a cadeia é fail-closed; os outros dois nunca a
// anotam. Inferir pela parentela seria mais frágil — o RM instrumenta QUALQUER chamador
// (ADR-002), pelo que o pai de uma mediação nem sempre é um `execute_tool`.
//
// QUE NÚMERO. O span do monitor tem QUATRO janelas encaixadas, e só a mais estreita é o que o
// SLO de 15 ms exprime:
//
//	política  ⊂  decisão (= política + escrita do selo)  ⊂  span (= decisão + execução da tool)
//
// Até AOS-398 o SLI lia o span inteiro: 1,21 s em produção, pela execução em gVisor (DEF-281).
// AOS-398 passou-o para a decisão: 30,8–32,7 ms em produção na v0.1.15, pela escrita durável do
// selo. AOS-401 deixou-o na POLÍTICA ([AttrMediationPolicyLatencyNanos]), que sempre coube em
// 2–8,6 ms. As outras duas continuam no span, observáveis e sem SLO.

import (
	"testing"
	"time"
)

// spanDoMonitor é o span do Reference Monitor: decidiu, e traz as janelas que o kernel
// publica — política, escrita do selo e a soma das duas — mais a do span inteiro.
func spanDoMonitor(trace string, politica, escrita, total time.Duration) WideEvent {
	return WideEvent{
		Operation:    OpExecuteTool,
		TraceIDHex:   trace,
		Decision:     DecisionPermit,
		LatencyNanos: int64(total),
		Attributes: map[string]any{
			AttrMediationPolicyLatencyNanos:     int64(politica),
			AttrMediationAuditWriteLatencyNanos: int64(escrita),
			AttrMediationDecisionLatencyNanos:   int64(politica + escrita),
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
// A política custou 8,6 ms e a tool `doc_read` correu 1,2 s em gVisor. Antes do AOS-398 o SLI
// lia a janela do span e publicava `1.21099128e+09` ns — 80× o tecto —, com
// `mediation_overhead_high` e `mediation_overhead_p95_high` a acender em `critical` e a mandar
// o operador para o RB-04 («Falha de PDP»). Um alerta que toca sempre ensina a ignorá-lo.
func TestOverheadP95_ExecucaoLongaNoSandboxNaoViolaOSLODaDecisao(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoMonitor("t1", 8600*time.Microsecond, 0, 1210991280*time.Nanosecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Samples != 1 {
		t.Fatalf("a mediação aconteceu e foi medida: esperava 1 amostra, vieram %d", sli.Samples)
	}
	if got := time.Duration(sli.Value); got != 8600*time.Microsecond {
		t.Fatalf("p95 = %v — o SLI tem de publicar a janela da POLÍTICA (8,6ms), não a do span (1,21s)", got)
	}
	if !sli.Met {
		t.Fatalf("8,6ms de política CUMPREM um SLO de 15ms; o SLI diz que não (valor=%v)", time.Duration(sli.Value))
	}
	if len(sli.Offenders) != 0 {
		t.Fatalf("um SLO cumprido não tem infractores; vieram %v", sli.Offenders)
	}
}

// TestAOS401_EscritaDuravelDoSeloNaoViolaOSLODaPolitica é o teste de regressão da SEGUNDA
// medição de produção, a 2026-09-16 com a v0.1.15 (run `run-delegado-1789569005`): o SLI da
// decisão mediu 30,8–32,7 ms e o streak dos dois `critical` subiu até 2 de 3.
//
// A decomposição é a do kernel: a política no intervalo que o selo sempre registou, e o resto
// é a escrita durável do selo. Contra um SLO que exprime o custo de DECIDIR, isto cumpre.
func TestAOS401_EscritaDuravelDoSeloNaoViolaOSLODaPolitica(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoMonitor("t1", 5*time.Millisecond, 27700*time.Microsecond, 1200*time.Millisecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if got := time.Duration(sli.Value); got != 5*time.Millisecond {
		t.Fatalf("p95 = %v — o SLI tem de ler a POLÍTICA (5ms), não a decisão com a escrita (32,7ms)", got)
	}
	if !sli.Met {
		t.Fatalf("5ms de política CUMPREM 15ms; a escrita do selo não entra no SLO (valor=%v)", time.Duration(sli.Value))
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
		// A tool é rápida e o sink também; é a CADEIA DE POLÍTICA que degradou (RB-04).
		spanDoMonitor("t1", 50*time.Millisecond, time.Millisecond, 55*time.Millisecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Met {
		t.Fatal("uma política de 50ms VIOLA um SLO de 15ms — o SLI tinha de o dizer")
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
		spanDoMonitor("t1", 5*time.Millisecond, 20*time.Millisecond, 4010*time.Millisecond),
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Samples != 1 {
		t.Fatalf("a amostra tem de conter SÓ o span do monitor: %d amostras", sli.Samples)
	}
	if got := time.Duration(sli.Value); got != 5*time.Millisecond {
		t.Fatalf("p95 = %v — devia ser a política do MONITOR (5ms)", got)
	}
	if !sli.Met {
		t.Fatalf("5ms de política CUMPREM um SLO de 15ms; o SLI diz que não (valor=%v)", time.Duration(sli.Value))
	}
}

// TestOverheadP95_SpanQueDecideMasNaoMedeADecisaoNaoEntra — um RM anterior ao AOS-398 decide
// mas não publica janela nenhuma. NÃO se cai para a latência do span.
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
		t.Fatalf("sem a janela da política não há amostra: %d", sli.Samples)
	}
	if sli.Value != 0 {
		t.Fatalf("sem amostra o valor fica em zero, não herdado do span: %v", sli.Value)
	}
	if !sli.Met {
		t.Fatal("ausência de dados não é violação (AOS-085): um SLI sem amostras não pode disparar")
	}
}

// TestAOS401_SpanDaV0115SoComAJanelaDaDecisaoNaoEntra — o RM da v0.1.15 publica a janela da
// decisão mas não a da política. Durante a janela de coexistência de versões, esse span NÃO
// pode alimentar o SLI com a decisão: seria o número de ~31 ms que o AOS-401 tirou do SLO.
func TestAOS401_SpanDaV0115SoComAJanelaDaDecisaoNaoEntra(t *testing.T) {
	v0115 := WideEvent{
		Operation:    OpExecuteTool,
		TraceIDHex:   "t1",
		Decision:     DecisionPermit,
		LatencyNanos: int64(1200 * time.Millisecond),
		Attributes: map[string]any{
			AttrMediationDecisionLatencyNanos: int64(32700 * time.Microsecond),
		},
	}

	sli := overheadP95SLI([]WideEvent{v0115}, int64(15*time.Millisecond))

	if sli.Samples != 0 {
		t.Fatalf("um span sem a janela da política não entra, mesmo trazendo a da decisão: %d amostras", sli.Samples)
	}
	if !sli.Met {
		t.Fatal("sem amostras o SLI não pode estar violado")
	}
}

// TestOverheadP95_DecisaoInstantaneaEAmostraLegitima separa "medida zero" de "sem medida".
// Um relógio de teste pode devolver zero, e isso é uma política medida — conta.
func TestOverheadP95_DecisaoInstantaneaEAmostraLegitima(t *testing.T) {
	sli := overheadP95SLI([]WideEvent{spanDoMonitor("t1", 0, 0, 2*time.Second)}, int64(15*time.Millisecond))

	if sli.Samples != 1 {
		t.Fatalf("uma política medida em zero é uma amostra: %d", sli.Samples)
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

// TestOverheadP95_DerivaAJanelaDoBagDoSpan fecha o percurso ponta-a-ponta: os atributos que o
// Reference Monitor anota no span sobrevivem à projecção em [WideEvent] e chegam ao SLI. Sem
// esta ligação a correcção do kernel ficava presa ao kernel.
func TestOverheadP95_DerivaAJanelaDoBagDoSpan(t *testing.T) {
	sd := SpanData{
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrDecision, Value: DecisionPermit},
			{Key: AttrMediationPolicyLatencyNanos, Value: int64(6 * time.Millisecond)},
			{Key: AttrMediationAuditWriteLatencyNanos, Value: int64(25 * time.Millisecond)},
			{Key: AttrMediationDecisionLatencyNanos, Value: int64(31 * time.Millisecond)},
		},
		StartUnixNano: 1,
		EndUnixNano:   1 + int64(1200*time.Millisecond),
	}

	we := WideEventFromSpanData(sd)

	if we.MediationPolicyLatencyNanos != int64(6*time.Millisecond) {
		t.Fatalf("a política não derivou do bag: %v", time.Duration(we.MediationPolicyLatencyNanos))
	}
	if we.MediationAuditWriteLatencyNanos != int64(25*time.Millisecond) {
		t.Fatalf("a escrita do selo não derivou do bag: %v", time.Duration(we.MediationAuditWriteLatencyNanos))
	}
	if we.MediationDecisionLatencyNanos != int64(31*time.Millisecond) {
		t.Fatalf("a decisão não derivou do bag: %v", time.Duration(we.MediationDecisionLatencyNanos))
	}
	if we.LatencyNanos != int64(1200*time.Millisecond) {
		t.Fatalf("a latência do span tem de continuar a ser a janela INTEIRA: %v", time.Duration(we.LatencyNanos))
	}

	sli := overheadP95SLI([]WideEvent{we}, int64(15*time.Millisecond))
	if !sli.Met || time.Duration(sli.Value) != 6*time.Millisecond {
		t.Fatalf("o SLI devia ler a política (6ms) e cumprir; leu %v (met=%v)", time.Duration(sli.Value), sli.Met)
	}
}

// TestAOS398_OIncidenteDeProducaoJaNaoAcendeNenhumCritico percorre a cadeia INTEIRA — SLI,
// catálogo de mediação e catálogo operacional — com os dados das DUAS medições de produção.
//
// É o teste que separa "o SLI reporta outro número" de "o operador deixa de ser acordado".
// Sem ele, um alerta ligado a outra janela noutro sítio do catálogo passaria despercebido.
func TestAOS398_OIncidenteDeProducaoJaNaoAcendeNenhumCritico(t *testing.T) {
	casos := map[string][]WideEvent{
		// v0.1.14: tool `doc_read` em gVisor, 1,21s de span.
		"execucao no sandbox (v0.1.14)": {spanDoMonitor("t1", 8600*time.Microsecond, 0, 1210991280*time.Nanosecond)},
		// v0.1.15: decisão de 32,7ms, quase toda escrita durável do selo.
		"escrita duravel do selo (v0.1.15)": {spanDoMonitor("t1", 5*time.Millisecond, 27700*time.Microsecond, 1200*time.Millisecond)},
	}
	for nome, eventos := range casos {
		t.Run(nome, func(t *testing.T) {
			dash := BuildDashboard(eventos, nil, DefaultSLOConfig())
			for _, a := range EvaluateAlerts(dash, DefaultAlertConfig()) {
				if a.Fired && a.SLI == SLIMediationOverheadP95 {
					t.Errorf("catálogo `mediation`: %q disparou (%s) — uma tool call normal não é uma avaria do PDP",
						a.Name, a.Message)
				}
			}

			opSnap := DefaultDashboardCatalog().Render(OperationalInputs{Events: eventos})
			for _, a := range EvaluateOperationalAlerts(opSnap, DefaultOperationalAlertConfig()) {
				if a.Fired && a.SLI == SLIMediationOverheadP95 {
					t.Errorf("catálogo `operational`: %q disparou (%s) — era este o alerta que acordava o turno",
						a.Name, a.Message)
				}
			}
		})
	}
}

// TestAOS398_UmaDecisaoDegradadaContinuaAAcenderOCritico é a outra metade, e a que impede
// esta correcção de ser um silenciador: quando a CADEIA DE POLÍTICA é que está lenta — o
// sintoma que o RB-04 descreve —, o `critical` tem de acender como sempre acendeu.
func TestAOS398_UmaDecisaoDegradadaContinuaAAcenderOCritico(t *testing.T) {
	// A tool é rápida (30ms) e o sink também (2ms); é o PDP que demora 120ms a decidir.
	degradado := []WideEvent{spanDoMonitor("t1", 120*time.Millisecond, 2*time.Millisecond, 150*time.Millisecond)}

	dash := BuildDashboard(degradado, nil, DefaultSLOConfig())
	if !temAlertaDisparado(EvaluateAlerts(dash, DefaultAlertConfig()), AlertMediationOverheadHigh) {
		t.Error("catálogo `mediation`: uma política de 120ms contra 15ms tinha de acender mediation_overhead_high")
	}

	opSnap := DefaultDashboardCatalog().Render(OperationalInputs{Events: degradado})
	if !temAlertaDisparado(EvaluateOperationalAlerts(opSnap, DefaultOperationalAlertConfig()), AlertMediationOverheadP95High) {
		t.Error("catálogo `operational`: uma política de 120ms contra 15ms tinha de acender mediation_overhead_p95_high")
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
