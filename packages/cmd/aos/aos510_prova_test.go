package main

// AOS-510 — A PROVA DA TENTATIVA POR RESPOSTA VAZIA, FACTO A FACTO.
//
// O [TestAOS510_AProvaRecusa_ComOLogReal] prepara um run real em cada estado que um run real
// produz. Este ficheiro parte do log REAL de um run que a prova admite e muda UM facto de cada
// vez — incluindo os estados que o kernel não escreve (um contrato de tools com `empty_output` e
// zero chamadas, a origem vinculativa sem tool call), que só assim ficam presos.

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos510LogLimpo devolve o nó, os eventos, o estado e o desfecho de um run REAL que fechou failed
// por empty_output sem tool calls, e o quesito da tentativa 2 sobre ele.
func aos510LogLimpo(t *testing.T) (*aos502No, []eventstore.Event, state.State, state.Outcome, quesitoDaTentativa, int) {
	t.Helper()
	n := aos510Compor(t, agentruntime.CompletionEnforce, 2, true, false)
	const plano = "plano-510-limpo"
	ger := n.pedir(t, plano)
	aos510FalharVazio(t, n, plano, ger)
	anterior := idDaTentativa(plano, aos510No_, 1)
	evs, err := n.node.EventStore.Read(context.Background(), anterior, 1)
	if err != nil {
		t.Fatal(err)
	}
	estado, desfecho, err := n.svc.DurableOutcome(context.Background(), anterior)
	if err != nil {
		t.Fatal(err)
	}
	return n, evs, estado, desfecho, quesitoDaTentativa{anterior: anterior, plano: plano, nodeID: aos510No_, tentativa: 2, geracao: ger}, ger
}

// TestAOS510_AProvaRecusa_UmFactoDeCadaVez: cada linha é uma condição da prova; sem ela, a
// mudança passava.
func TestAOS510_AProvaRecusa_UmFactoDeCadaVez(t *testing.T) {
	_, evs, estado, desfecho, q, _ := aos510LogLimpo(t)
	if causa, hash := julgarTentativaVazia(evs, estado, desfecho, q); causa != "" || hash == "" {
		t.Fatalf("controlo: o log real de um run failed por empty_output, sem tool calls, com um turno e stop, sem contrato e sem origem, e ADMITIDO; veio causa=%q hash=%q", causa, hash)
	}
	// A prova do AOS-502 recusa o MESMO log, com a causa de sempre: as classes não se confundem.
	if causa, _ := julgarTentativaAnterior(evs, estado, desfecho, q); causa != causaRetryOutraRazao {
		t.Fatalf("a prova do AOS-502 recusa um run empty_output com anterior_outra_razao; veio %q", causa)
	}
	veredicto := func(mudar func(*agentruntime.Verdict)) state.Outcome {
		v := *desfecho.Verdict
		mudar(&v)
		return state.Outcome{Verdict: &v, OutputSource: desfecho.OutputSource}
	}
	var umTurno eventstore.Event
	for _, ev := range evs {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			umTurno = ev
		}
	}
	semTipo := func(tipo string) []eventstore.Event {
		var out []eventstore.Event
		for _, ev := range evs {
			if ev.Type != tipo {
				out = append(out, ev)
			}
		}
		return out
	}
	comEvento := func(tipo string) []eventstore.Event {
		extra := umTurno
		extra.Type, extra.Payload = tipo, []byte(`{}`)
		return append(append([]eventstore.Event(nil), evs...), extra)
	}
	turno := agentruntime.EventTypeTurnRecorded
	manifesto := func(c map[string]any) map[string]any { return c["manifest"].(map[string]any) }
	completion := func(c map[string]any) map[string]any { return manifesto(c)["completion"].(map[string]any) }
	comOrigemSelada := state.Outcome{Verdict: desfecho.Verdict, OutputSource: &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds}}
	q3 := q
	q3.tentativa, q3.anterior = 3, idDaTentativa(q.plano, q.nodeID, 2)

	for _, c := range []struct {
		nome     string
		evs      []eventstore.Event
		estado   state.State
		desfecho state.Outcome
		q        quesitoDaTentativa
		causa    string
	}{
		// O ELO, que é o do AOS-502: a prova desta classe fá-lo inteiro, sem depender de quem chama.
		{"StreamVazio", nil, estado, desfecho, q, causaRetryInexistente},
		{"SemOrigem", semTipo(EventTypeRunPlanOrigin), estado, desfecho, q, causaRetrySemOrigem},
		{"OutroPlano", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { c["plan_request"].(map[string]any)["run_id"] = "outro-plano" }), estado, desfecho, q, causaRetryOutroPedido},
		{"OutroNo", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { c["node_id"] = "outro_no" }), estado, desfecho, q, causaRetryOutroPedido},
		{"Tentativa3SobreUmRunQueNaoFoiAdmitidoComoTentativa", aos502NoRun(evs, q3.anterior), estado, desfecho, q3, causaRetrySequencia},
		{"EstadoRunning", evs, state.Running, desfecho, q, causaRetryEmCurso},
		{"EstadoComplete", evs, state.Complete, desfecho, q, causaRetryNaoFalhou},
		{"EstadoTimedOut", evs, state.TimedOut, desfecho, q, causaRetryNaoFalhou},
		// A RAZÃO.
		{"FailedSemVeredicto", evs, estado, state.Outcome{}, q, causaRetryOutraRazao},
		{"VeredictoCumprido", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Fulfilled = true }), q, causaRetryOutraRazao},
		{"RazaoNoCall", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeContractNoCall }), q, causaRetryOutraRazao},
		{"RazaoAfterDenial", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeContractAfterDenial }), q, causaRetryOutraRazao},
		{"RazaoTruncated", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeTruncated }), q, causaRetryOutraRazao},
		{"RazaoOrigemEmFalta", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeOutputSourceMissing }), q, causaRetryOutraRazao},
		// ZERO TOOL CALLS, pelas três fontes.
		{"VectorComUmaToolCallPedida", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.ToolCallsRequested = 1 }), q, causaRetryPediuTools},
		{"UmToolCallMediated", comEvento(referencemonitor.EventTypeMediated), estado, desfecho, q, causaRetryPediuTools},
		{"UmToolCallDenied", comEvento(referencemonitor.EventTypeDenied), estado, desfecho, q, causaRetryPediuTools},
		{"UmToolCallOutcome", comEvento(referencemonitor.EventTypeOutcome), estado, desfecho, q, causaRetryPediuTools},
		{"UmTipoNovoDeToolCall", comEvento("tool.call.um_tipo_que_ainda_nao_existe"), estado, desfecho, q, causaRetryPediuTools},
		{"TurnoComToolCallsPedidas", aos502Trocar(t, evs, turno, func(c map[string]any) { c["tool_calls_requested"] = 1 }), estado, desfecho, q, causaRetryPediuTools},
		// UM SÓ TURNO, COM `stop`.
		{"SemTurnos", semTipo(turno), estado, desfecho, q, causaRetryTurnos},
		{"DoisTurnos", append(append([]eventstore.Event(nil), evs...), umTurno), estado, desfecho, q, causaRetryTurnos},
		{"TurnoIlegivel", aos502ComPayload(evs, turno, `{"turn":`), estado, desfecho, q, causaRetryIlegivel},
		{"MotivoLength", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "length" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoContentFilter", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "content_filter" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoToolCalls", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "tool_calls" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoOther", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "other" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoNaoReportado", aos502Trocar(t, evs, turno, func(c map[string]any) { delete(c, "stop_reason") }), estado, desfecho, q, causaRetryParagem},
		// SEM CONTRATO DE TOOLS: o estado que o kernel não produz.
		{"ManifestoComContrato", aos502Trocar(t, evs, turno, func(c map[string]any) { completion(c)["requires"] = []string{"doc_read"} }), estado, desfecho, q, causaVaziaEstadoImpossivel},
		{"VectorSeladoComLinhaDeContrato", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Tools = []agentruntime.ToolEvidence{{Tool: "doc_read"}} }), q, causaVaziaEstadoImpossivel},
		{"ManifestoSemVeredicto", aos502Trocar(t, evs, turno, func(c map[string]any) { delete(manifesto(c), "completion") }), estado, desfecho, q, causaVaziaEstadoImpossivel},
		// SEM ORIGEM VINCULATIVA.
		{"ManifestoComOrigemVinculativa", aos502Trocar(t, evs, turno, func(c map[string]any) {
			completion(c)["output_from"], completion(c)["output_binding"] = "doc_read", "binding"
		}), estado, desfecho, q, causaVaziaOrigemVinculativa},
		{"AncoraSeladaComOrigemVinculativa", evs, estado, comOrigemSelada, q, causaVaziaOrigemVinculativa},
	} {
		t.Run(c.nome, func(t *testing.T) {
			causa, hash := julgarTentativaVazia(c.evs, c.estado, c.desfecho, c.q)
			if causa != c.causa || hash != "" {
				t.Fatalf("queria a recusa %q e nenhum hash; veio causa=%q hash=%q", c.causa, causa, hash)
			}
		})
	}
	// A origem só a MEDIR não obriga: o run que a declarou e respondeu vazio sem chamar nada
	// fechou pelo texto, e é admitido.
	soMede := aos502Trocar(t, evs, turno, func(c map[string]any) {
		completion(c)["output_from"], completion(c)["output_binding"] = "doc_read", "measure"
	})
	if causa, _ := julgarTentativaVazia(soMede, estado, desfecho, q); causa != "" {
		t.Fatalf("uma origem com o vinculo measure nao obriga, e a prova admite; veio %q", causa)
	}
}

// TestAOS510_ProvarTentativa_OInterruptorEOLog: a prova que o handler chama. Com o interruptor
// desligado o run `empty_output` é recusado como sempre e SEM classe; ligado, é admitido com a
// classe; e as avarias de leitura respondem como no AOS-502 (transitória ⇒ 503).
func TestAOS510_ProvarTentativa_OInterruptorEOLog(t *testing.T) {
	n, _, _, _, q, ger := aos510LogLimpo(t)
	v := *aos502Vinculo(q.plano, aos510No_, ger, 2)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	original := n.node.EventStore

	desligado := aos502Interno(t, n, original)
	if prova, causa, transitoria := desligado.provarTentativa(context.Background(), chamador, v); causa != causaRetryOutraRazao || transitoria || prova.vazia {
		t.Fatalf("com o interruptor desligado: anterior_outra_razao, definitiva e sem classe; veio causa=%q transitoria=%t vazia=%t", causa, transitoria, prova.vazia)
	}
	ligado := aos502Interno(t, n, original)
	ligado.cfg.runRetryEmpty = true
	prova, causa, _ := ligado.provarTentativa(context.Background(), chamador, v)
	if causa != "" || !prova.vazia || prova.anterior != q.anterior || prova.promptHash == "" {
		t.Fatalf("com o interruptor ligado a prova passa, com a classe e o hash; veio causa=%q prova=%+v", causa, prova)
	}
	// O aviso do AOS-506 não entra numa prova desta classe, mesmo ligado; e entra na outra.
	ligado.cfg.runRetryNotice = true
	if aviso := ligado.avisoDaTentativa(&prova); aviso != agentruntime.RetryNoticeNone {
		t.Fatalf("a tentativa por vazio nao leva aviso; veio %q", aviso)
	}
	if aviso := ligado.avisoDaTentativa(&provaDaTentativa{}); aviso != agentruntime.RetryNoticeNoFunctionCall {
		t.Fatalf("controlo: a tentativa do AOS-502 leva o aviso; veio %q", aviso)
	}
	if razaoDaClasse(&prova) != "empty_output" || razaoDaClasse(&provaDaTentativa{}) != "" || razaoDaClasse(nil) != "" {
		t.Fatal("o retry_reason sai da prova: empty_output so na classe da resposta vazia")
	}
	// Tecto: com o tecto a zero nem o interruptor ligado deixa ler o log.
	semTecto := aos502Interno(t, n, original)
	semTecto.cfg.runRetryMax, semTecto.cfg.runRetryEmpty = 0, true
	if prova, causa, _ := semTecto.provarTentativa(context.Background(), chamador, v); causa != causaRetryTecto || prova.vazia {
		t.Fatalf("com o tecto a zero a recusa e o tecto, sem classe; veio %q", causa)
	}
	// Fail-closed na leitura: o log que não se lê agora é transitório; o podado é definitivo.
	falha := aos502Interno(t, n, aos502StoreQueFalha{EventStorePort: original, falha: q.anterior})
	falha.cfg.runRetryEmpty = true
	if _, causa, transitoria := falha.provarTentativa(context.Background(), chamador, v); causa != causaRetryIndisponivel || !transitoria {
		t.Fatalf("um log que nao se le agora e indisponivel e TRANSITORIO; veio causa=%q transitoria=%t", causa, transitoria)
	}
	podado := aos502Interno(t, n, aos502StoreQueFalha{EventStorePort: original, ausente: q.anterior})
	podado.cfg.runRetryEmpty = true
	if _, causa, transitoria := podado.provarTentativa(context.Background(), chamador, v); causa != causaRetryInexistente || transitoria {
		t.Fatalf("um stream que ja nao existe e uma recusa; veio causa=%q transitoria=%t", causa, transitoria)
	}
	// Outra região: a residência do run anterior não é a de quem pede — recusa DESTA classe.
	deFora := readerIdentity{principal: govReader, board: govBoard, region: "us"}
	if prova, causa, _ := ligado.provarTentativa(context.Background(), deFora, v); causa != causaRetryResidencia || !prova.vazia {
		t.Fatalf("a residencia do run anterior tem de ser a regiao de quem pede, e a recusa leva a classe; veio causa=%q vazia=%t", causa, prova.vazia)
	}
	// O vocabulário das duas séries é fechado, e todas as causas desta classe têm contador.
	var c contagemDasTentativasVazias
	for _, causa := range causasDeRecusaDaTentativaVazia {
		c.recusar(causa)
		if c.recusadasPor(causa) != 1 {
			t.Fatalf("a causa %q nao tem contador", causa)
		}
	}
	c.recusar("uma_causa_de_fora")
	if c.recusadasPor("uma_causa_de_fora") != 0 {
		t.Fatal("uma causa fora do vocabulario nao soma")
	}
}
