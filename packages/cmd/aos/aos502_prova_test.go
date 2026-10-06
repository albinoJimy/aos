package main

// AOS-502 — A PROVA DA NOVA TENTATIVA, FACTO A FACTO, E A CONFIGURAÇÃO DO TECTO.
//
// O [TestAOS502_AProvaRecusa_ComOLogReal] prepara um run real em cada estado que um run real
// produz. Este ficheiro parte do log REAL de um run que a prova admite e muda UM facto de cada
// vez: é assim que cada condição da prova fica presa sozinha, incluindo as que nenhum run
// produz a pedido (dois turnos sem tool calls, `timed_out`, uma origem de outro pedido).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos502StoreQueFalha é o Event Store do nó com a leitura de UM stream a falhar (`falha`) ou a
// responder que o stream não existe (`ausente`).
type aos502StoreQueFalha struct {
	EventStorePort
	falha   string
	ausente string
}

func (s aos502StoreQueFalha) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	switch streamID {
	case "":
	case s.falha:
		return nil, fmt.Errorf("substrato indisponivel (teste)")
	case s.ausente:
		return nil, eventstore.ErrStreamNotFound
	}
	return s.EventStorePort.Read(ctx, streamID, fromSeq)
}

// aos502Interno é o handler do nó com o Event Store dado, para chamar a prova directamente: o
// serviço e o gate soberano são os do nó composto.
func aos502Interno(t *testing.T, n *aos502No, store EventStorePort) *apiHandler {
	t.Helper()
	var gov *readGovernance
	switch {
	case n.node.SovereignAuthority != nil:
		gov = newReadGovernance(n.node.SovereignAuthority, n.node.SovereignReadCredential, n.node.WORM, time.Now)
	case n.node.SovereignReadRegions != nil:
		gov = newReadGovernance(n.node.SovereignReadRegions, nil, n.node.WORM, time.Now)
	default:
		t.Fatal("o no destes testes tem o gate soberano de leitura")
	}
	return &apiHandler{svc: n.svc, node: &Node{EventStore: store}, cfg: apiConfig{runRetryMax: 2}, readGov: gov}
}

// aos502LogLimpo devolve os eventos, o estado e o desfecho de um run REAL que fechou failed por
// contract_unmet_no_call sem tool calls, e o quesito da tentativa 2 sobre ele.
func aos502LogLimpo(t *testing.T) ([]eventstore.Event, state.State, state.Outcome, quesitoDaTentativa) {
	t.Helper()
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-limpo"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	anterior := idDaTentativa(plano, aos502No_, 1)
	evs, err := n.node.EventStore.Read(context.Background(), anterior, 1)
	if err != nil {
		t.Fatal(err)
	}
	estado, desfecho, err := n.svc.DurableOutcome(context.Background(), anterior)
	if err != nil {
		t.Fatal(err)
	}
	return evs, estado, desfecho, quesitoDaTentativa{anterior: anterior, plano: plano, nodeID: aos502No_, tentativa: 2, geracao: ger}
}

// aos502Trocar devolve uma cópia dos eventos em que o payload do primeiro evento do tipo dado é
// reescrito por `mudar`.
func aos502Trocar(t *testing.T, evs []eventstore.Event, tipo string, mudar func(map[string]any)) []eventstore.Event {
	t.Helper()
	copia := append([]eventstore.Event(nil), evs...)
	for i, ev := range copia {
		if ev.Type != tipo {
			continue
		}
		var campos map[string]any
		if err := json.Unmarshal(ev.Payload, &campos); err != nil {
			t.Fatalf("payload de %s ilegivel: %v", tipo, err)
		}
		mudar(campos)
		novo, err := json.Marshal(campos)
		if err != nil {
			t.Fatal(err)
		}
		copia[i].Payload = novo
		return copia
	}
	t.Fatalf("o log nao tem nenhum evento %s", tipo)
	return nil
}

// aos502ComPayload devolve uma cópia dos eventos em que TODOS os eventos do tipo dado ficam com
// o payload dado.
func aos502ComPayload(evs []eventstore.Event, tipo, payload string) []eventstore.Event {
	out := append([]eventstore.Event(nil), evs...)
	for i := range out {
		if out[i].Type == tipo {
			out[i].Payload = []byte(payload)
		}
	}
	return out
}

// aos502NoRun devolve uma cópia dos eventos como se fossem do run `runID`.
func aos502NoRun(evs []eventstore.Event, runID string) []eventstore.Event {
	out := append([]eventstore.Event(nil), evs...)
	for i := range out {
		out[i].RunID = runID
	}
	return out
}

// TestAOS502_AProvaRecusa_UmFactoDeCadaVez parte do log REAL de um run que a prova admite e muda
// UM facto de cada vez. Cada linha é uma condição da prova: sem ela, a mudança passava.
func TestAOS502_AProvaRecusa_UmFactoDeCadaVez(t *testing.T) {
	evs, estado, desfecho, q := aos502LogLimpo(t)
	if causa, hash := julgarTentativaAnterior(evs, estado, desfecho, q); causa != "" || hash == "" {
		t.Fatalf("controlo: o log real de um run failed por contract_unmet_no_call, sem tool calls e com stop, e ADMITIDO e devolve o hash do prompt; veio causa=%q hash=%q", causa, hash)
	}
	veredicto := func(mudar func(*agentruntime.Verdict)) state.Outcome {
		v := *desfecho.Verdict
		mudar(&v)
		return state.Outcome{Verdict: &v}
	}
	var umTurno, umaOrigem eventstore.Event
	for _, ev := range evs {
		switch ev.Type {
		case agentruntime.EventTypeTurnRecorded:
			umTurno = ev
		case EventTypeRunPlanOrigin:
			umaOrigem = ev
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
	falsa := umaOrigem
	falsa.Producer.NHIID = "nhi:outro-escritor"
	origemDeOutroProdutor := append(semTipo(EventTypeRunPlanOrigin), falsa)

	q3 := q
	q3.tentativa = 3
	q3.anterior = idDaTentativa(q.plano, q.nodeID, 2)
	comoTentativa2 := func(retryOf string) []eventstore.Event {
		return aos502NoRun(aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { c["attempt"], c["retry_of"] = 2, retryOf }), q3.anterior)
	}
	if causa, _ := julgarTentativaAnterior(comoTentativa2(q.anterior), estado, desfecho, q3); causa != "" {
		t.Fatalf("controlo: a tentativa 3 sobre uma tentativa 2 admitida e limpa e ADMITIDA; veio %q", causa)
	}
	deOutroRun := q
	deOutroRun.anterior = "outro-run"
	pedido := func(c map[string]any) map[string]any { return c["plan_request"].(map[string]any) }
	turno := agentruntime.EventTypeTurnRecorded

	for _, c := range []struct {
		nome     string
		evs      []eventstore.Event
		estado   state.State
		desfecho state.Outcome
		q        quesitoDaTentativa
		causa    string
	}{
		{"StreamVazio", nil, estado, desfecho, q, causaRetryInexistente},
		{"EventosDeOutroRun", evs, estado, desfecho, deOutroRun, causaRetryInexistente},
		{"SemOrigem", semTipo(EventTypeRunPlanOrigin), estado, desfecho, q, causaRetrySemOrigem},
		{"OrigemQueONoNaoEscreveu", origemDeOutroProdutor, estado, desfecho, q, causaRetrySemOrigem},
		{"OrigemIlegivel", aos502ComPayload(evs, EventTypeRunPlanOrigin, `{"v":`), estado, desfecho, q, causaRetryIlegivel},
		{"OutroPlano", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { pedido(c)["run_id"] = "outro-plano" }), estado, desfecho, q, causaRetryOutroPedido},
		{"OutraFila", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { pedido(c)["stream"] = "outra/fila" }), estado, desfecho, q, causaRetryOutroPedido},
		{"OutroNo", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { c["node_id"] = "outro_no" }), estado, desfecho, q, causaRetryOutroPedido},
		{"SemNodeIDNaOrigem", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { delete(c, "node_id") }), estado, desfecho, q, causaRetryOutroPedido},
		{"GeracaoPosteriorADoPedido", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { pedido(c)["generation"] = q.geracao + 1 }), estado, desfecho, q, causaRetryOutroPedido},
		{"GeracaoZeroNaOrigem", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { pedido(c)["generation"] = 0 }), estado, desfecho, q, causaRetryOutroPedido},
		{"Tentativa2SobreUmaTentativa", aos502Trocar(t, evs, EventTypeRunPlanOrigin, func(c map[string]any) { c["attempt"] = 2 }), estado, desfecho, q, causaRetrySequencia},
		{"Tentativa3SobreUmRunQueNaoFoiAdmitidoComoTentativa", aos502NoRun(evs, q3.anterior), estado, desfecho, q3, causaRetrySequencia},
		{"Tentativa3ComRetryOfErrado", comoTentativa2("outro-run"), estado, desfecho, q3, causaRetrySequencia},
		{"EstadoRunning", evs, state.Running, desfecho, q, causaRetryEmCurso},
		{"EstadoReady", evs, state.Ready, desfecho, q, causaRetryEmCurso},
		{"EstadoPaused", evs, state.Paused, desfecho, q, causaRetryEmCurso},
		{"EstadoCompensating", evs, state.Compensating, desfecho, q, causaRetryEmCurso},
		{"EstadoComplete", evs, state.Complete, desfecho, q, causaRetryNaoFalhou},
		{"EstadoTimedOut", evs, state.TimedOut, desfecho, q, causaRetryNaoFalhou},
		{"EstadoKilled", evs, state.Killed, desfecho, q, causaRetryNaoFalhou},
		{"FailedSemVeredicto", evs, estado, state.Outcome{}, q, causaRetryOutraRazao},
		{"FailedSemRazao", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = "" }), q, causaRetryOutraRazao},
		{"VeredictoCumprido", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Fulfilled = true }), q, causaRetryOutraRazao},
		{"RazaoAfterDenial", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeContractAfterDenial }), q, causaRetryOutraRazao},
		{"RazaoAfterToolError", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeContractAfterToolError }), q, causaRetryOutraRazao},
		{"RazaoTruncated", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeTruncated }), q, causaRetryOutraRazao},
		{"RazaoEmptyOutput", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.Reason = agentruntime.OutcomeEmptyOutput }), q, causaRetryOutraRazao},
		{"VectorComUmaToolCallPedida", evs, estado, veredicto(func(v *agentruntime.Verdict) { v.ToolCallsRequested = 1 }), q, causaRetryPediuTools},
		{"UmToolCallMediated", comEvento(referencemonitor.EventTypeMediated), estado, desfecho, q, causaRetryPediuTools},
		{"UmToolCallDenied", comEvento(referencemonitor.EventTypeDenied), estado, desfecho, q, causaRetryPediuTools},
		{"UmToolCallEscalated", comEvento(referencemonitor.EventTypeEscalated), estado, desfecho, q, causaRetryPediuTools},
		{"UmToolCallOutcome", comEvento(referencemonitor.EventTypeOutcome), estado, desfecho, q, causaRetryPediuTools},
		{"UmTipoNovoDeToolCall", comEvento("tool.call.um_tipo_que_ainda_nao_existe"), estado, desfecho, q, causaRetryPediuTools},
		{"TurnoComToolCallsPedidas", aos502Trocar(t, evs, turno, func(c map[string]any) { c["tool_calls_requested"] = 1 }), estado, desfecho, q, causaRetryPediuTools},
		{"SemTurnos", semTipo(turno), estado, desfecho, q, causaRetryTurnos},
		{"DoisTurnos", append(append([]eventstore.Event(nil), evs...), umTurno), estado, desfecho, q, causaRetryTurnos},
		{"TurnoIlegivel", aos502ComPayload(evs, turno, `{"turn":`), estado, desfecho, q, causaRetryIlegivel},
		{"MotivoLength", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "length" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoContentFilter", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "content_filter" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoOther", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "other" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoToolCalls", aos502Trocar(t, evs, turno, func(c map[string]any) { c["stop_reason"] = "tool_calls" }), estado, desfecho, q, causaRetryParagem},
		{"MotivoNaoReportado", aos502Trocar(t, evs, turno, func(c map[string]any) { delete(c, "stop_reason") }), estado, desfecho, q, causaRetryParagem},
	} {
		t.Run(c.nome, func(t *testing.T) {
			causa, hash := julgarTentativaAnterior(c.evs, c.estado, c.desfecho, c.q)
			if causa != c.causa || hash != "" {
				t.Fatalf("queria a recusa %q e nenhum hash; veio causa=%q hash=%q", c.causa, causa, hash)
			}
			achou := false
			for _, v := range causasDeRecusaDaTentativa {
				achou = achou || v == causa
			}
			if !achou {
				t.Fatalf("a causa %q esta fora do vocabulario fechado", causa)
			}
		})
	}
}

// TestAOS502_Tecto_DoAmbiente: `AOS_RUN_RETRY_MAX` aceita 0, 1 e 2; vazia é zero; tudo o resto
// recusa o arranque.
func TestAOS502_Tecto_DoAmbiente(t *testing.T) {
	for valor, quer := range map[string]int{"": 0, "0": 0, "1": 1, "2": 2, " 2 ": 2} {
		t.Setenv("AOS_RUN_RETRY_MAX", valor)
		opt, n, err := apiRunRetryMaxOptionFromEnv()
		if err != nil || n != quer || (opt == nil) != (quer == 0) {
			t.Fatalf("AOS_RUN_RETRY_MAX=%q: queria %d (opcao so acima de zero); veio n=%d opt=%v err=%v", valor, quer, n, opt != nil, err)
		}
		if opt != nil {
			var cfg apiConfig
			opt(&cfg)
			if cfg.runRetryMax != quer {
				t.Fatalf("a opcao tinha de por o tecto a %d; ficou %d", quer, cfg.runRetryMax)
			}
		}
	}
	for _, valor := range []string{"3", "-1", "dois", "1.0", "02", "+1", "1 2", "0x1"} {
		t.Setenv("AOS_RUN_RETRY_MAX", valor)
		if _, n, err := apiRunRetryMaxOptionFromEnv(); err == nil || n != 0 || !errors.Is(err, ErrBadRunRetryMax) {
			t.Fatalf("AOS_RUN_RETRY_MAX=%q tinha de recusar o arranque; veio n=%d err=%v", valor, n, err)
		}
	}
	var cfg apiConfig
	for _, fora := range []int{-1, 0, 3, 100} {
		WithRunRetryMax(fora)(&cfg)
		if cfg.runRetryMax != 0 {
			t.Fatalf("WithRunRetryMax(%d) tinha de deixar o tecto a zero; ficou %d", fora, cfg.runRetryMax)
		}
	}
}
