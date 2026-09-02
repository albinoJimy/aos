package replay

// AOS-289 — UMA CAPTURA COM MENOS RESULTADOS DO QUE TOOL CALLS É INADMISSÍVEL.
//
// O defeito: ao ESCALAR, o loop sai do laço de tool calls tendo capturado `j+1` resultados, mas
// grava a `Response` INTEIRA, com as M tool calls. O motor de replay itera sobre as M e o
// dispatcher devolve um resultado untrusted VAZIO para os índices sem registo — um segmento que
// o run original nunca produziu, dobrado no tail. A auditoria mediu: `Fidelity=1`,
// `Divergence=nil`, e um `FinalStateHash` diferente do original.
//
// Uma fidelidade de 1,0 sobre uma trajectória fabricada é pior do que uma recusa: é uma prova a
// afirmar o que não observou.
//
// # ESTES TESTES NÃO CONSTROEM A CAPTURA À MÃO
//
// A captura truncada é produzida pelo LOOP REAL, com o Reference Monitor a escalar a primeira de
// duas tool calls. Um literal `capturePayload` provaria que a guarda compara dois inteiros;
// isto prova que o produtor existe, que é o que faltava — **nenhum teste do repositório
// combinava escalada com replay**, e foi por isso que o defeito passou.
//
// # OS DOIS CAMINHOS, E PORQUE SÃO DOIS
//
// `admit()` só corre em [ReplayEngine.Replay]. A RETOMA usa [ReplayEngine.Reconstruct]
// (`cmd/aos/resume.go`), que nunca passou pelo gate. Corrigir só o `admit` fecharia o replay de
// DR e deixaria aberto o caso de uso que o defeito descreve — a escalada existe para ser
// retomada. Por isso há um teste para cada caminho.

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos289Hook escala a capability dada e permite as outras — o veredicto que um RiskGate daria.
type aos289Hook struct{ capability string }

func (aos289Hook) Name() string { return "risk-aos289" }

func (h aos289Hook) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call != nil && call.Capability == h.capability {
		return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "requer aval humano"}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// aos289Sink aceita a escalada sem erro — o adaptador real vive no composition root.
type aos289Sink struct{ n int }

func (s *aos289Sink) Escalate(context.Context, agentruntime.PendingApproval) error {
	s.n++
	return nil
}

// runEscalado corre um turno com DUAS tool calls em que a PRIMEIRA escala. O loop captura o
// resultado dessa primeira (com o marcador de negação) e sai — a segunda nunca é despachada,
// mas viaja na `Response` gravada. É a assimetria, produzida pelo caminho real.
func runEscalado(t *testing.T, runID string) (*eventstore.Store, agentruntime.Goal) {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rm := referencemonitor.New(
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
		referencemonitor.WithHooks(aos289Hook{capability: "cap:echo"}),
	)
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	capturer, err := NewCapturer(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}

	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		return agentruntime.ModelResponse{
			Text: "chamo echo duas vezes",
			ToolCalls: []agentruntime.ToolInvocation{
				{ToolID: "echo", Capability: "cap:echo", Input: []byte("um")},
				{ToolID: "echo", Capability: "cap:echo", Input: []byte("dois")},
			},
			Usage:        agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
			CostMicroUSD: 1200,
		}, nil
	})

	sink := &aos289Sink{}
	rt := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(store),
		agentruntime.WithCapturer(capturer), agentruntime.WithEscalationSink(sink))
	goal := sampleGoal(runID)
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// PRÉ-CONDIÇÕES: sem elas, um teste verde não prova nada — mediria um run que não escalou.
	if !res.Escalated {
		t.Fatalf("premissa: o run tinha de escalar; res=%+v", res)
	}
	if sink.n != 1 {
		t.Fatalf("premissa: esperava 1 escalada registada, obtive %d", sink.n)
	}
	return store, goal
}

// TestAOS289_UmaCapturaTruncadaNaoEAdmitidaNoReplay é a AC3.
//
// Sem a correcção, isto devolvia `Fidelity=1.0` e `Divergence=nil` — com um segmento fabricado
// dentro. É essa afirmação falsa que o teste proíbe, e por isso verifica também que a fidelidade
// NÃO é reportada: um replay recusado não pode devolver um resultado que pareça uma prova.
func TestAOS289_UmaCapturaTruncadaNaoEAdmitidaNoReplay(t *testing.T) {
	store, goal := runEscalado(t, "run_aos289_replay")
	e, err := NewEngine(store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	res, err := e.Replay(context.Background(), goal.RunID, Options{Spec: specFromGoal(goal)})
	if !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("Replay de captura truncada = %v, quero ErrIncompleteCapture — sem isto o motor reporta Fidelity=1.0 sobre uma trajectoria com um segmento FABRICADO no tail", err)
	}
	if res.Fidelity != 0 || res.Divergence != nil || len(res.Steps) != 0 {
		t.Fatalf("um replay RECUSADO nao pode devolver resultado que pareca prova: %+v", res)
	}
	// A mensagem tem de dizer os dois números, senão quem depura não sabe qual turno olhar.
	if msg := err.Error(); !containsAll(msg, "2 tool calls", "1 resultados") {
		t.Errorf("o erro tem de nomear os dois comprimentos; veio: %s", msg)
	}
}

// TestAOS289_UmaCapturaTruncadaNaoEAdmitidaNaRETOMA é a AC4, e é a metade que a leitura do
// ticket não dava.
//
// `Reconstruct` é a função que `cmd/aos/resume.go` chama para reproduzir a trajectória de um run
// retomado, e NÃO passa por `admit()`. Sem esta guarda, a correcção fechava o replay de DR e
// deixava a captura truncada a alimentar o `prompt_hash` do turno seguinte — que é o defeito
// medido pela auditoria em `Fidelity=0.5`, `Divergence{Turn=2}`.
func TestAOS289_UmaCapturaTruncadaNaoEAdmitidaNaRETOMA(t *testing.T) {
	store, goal := runEscalado(t, "run_aos289_retoma")
	e, err := NewEngine(store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	turnos, err := e.Reconstruct(context.Background(), goal.RunID)
	if !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("Reconstruct de captura truncada = %v, quero ErrIncompleteCapture — este e o caminho da RETOMA, e a escalada existe para ser retomada", err)
	}
	if turnos != nil {
		t.Fatalf("uma reconstrucao recusada nao pode devolver turnos: %d", len(turnos))
	}
}

// TestAOS289_UmRunSemEscaladaContinuaAdmissivel é a âncora de não-vacuidade: a guarda tem de
// recusar o truncado e SÓ o truncado. Sem isto, uma guarda avariada que recusasse tudo passaria
// nos dois testes acima.
func TestAOS289_UmRunSemEscaladaContinuaAdmissivel(t *testing.T) {
	or := runOriginal(t, "run_aos289_intacto")
	e := mustEngine(t, or)

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("um run INTACTO tem de continuar admissivel: %v", err)
	}
	if res.Fidelity != 1.0 || res.Divergence != nil {
		t.Fatalf("fidelidade=%v divergencia=%+v; quero 1.0 e nil", res.Fidelity, res.Divergence)
	}
	if _, rerr := e.Reconstruct(context.Background(), or.goal.RunID); rerr != nil {
		t.Fatalf("Reconstruct de um run intacto tem de passar: %v", rerr)
	}
}

// TestAOS289_MaisResultadosDoQueChamadasNaoRecusa fixa o SENTIDO da comparação.
//
// A guarda recusa resultados a MENOS do que chamadas, que é o fabrico. O caso inverso não
// fabrica nada — o motor itera sobre as chamadas e nunca alcança o excedente —, e não há
// caminho no loop que o produza. Recusá-lo alargaria a guarda para além do defeito medido, e é
// isso que este teste impede.
func TestAOS289_MaisResultadosDoQueChamadasNaoRecusa(t *testing.T) {
	capt := capturePayload{
		Turn:        1,
		Response:    responseCapture{ToolCalls: []toolCallCapture{{ToolID: "echo"}}},
		ToolResults: []toolResultCapture{{}, {}},
	}
	if err := capturaCompleta(capt); err != nil {
		t.Fatalf("1 chamada e 2 resultados nao fabrica segmento nenhum e nao pode recusar: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
