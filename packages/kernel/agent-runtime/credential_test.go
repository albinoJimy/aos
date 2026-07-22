package agentruntime

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// credentialSpy é um hook que REGISTA a Credential da Call mediada e permite. Serve
// para provar o wiring de AOS-152 (Goal.Credential → Call.Credential) sem depender do
// verificador de identidade real: é a posição onde o hook de identidade LERIA o token.
type credentialSpy struct {
	saw        bool
	credential string
}

func (*credentialSpy) Name() string { return "cred-spy" }

func (s *credentialSpy) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	s.saw = true
	s.credential = call.Credential
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// runEchoWithCredential corre um turno que despacha a tool "echo" através de um RM cujo
// hook é o [credentialSpy], com o goal a declarar cred. Devolve o spy (o que a call
// mediada carregou).
func runEchoWithCredential(t *testing.T, cred string) *credentialSpy {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	spy := &credentialSpy{}
	rm := referencemonitor.New(
		referencemonitor.WithHooks(spy),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	turn := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		turn++
		if turn == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
		}
		return ModelResponse{Text: "fim", Final: true}, nil
	})

	rt := New(model, rm, NewTurnRecorder(store))
	goal := sampleGoal()
	goal.Credential = cred
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("Run(cred=%q): %v", cred, err)
	}
	return spy
}

// TestGoalCredentialFlowsToMediatedCall é o wiring de AOS-152: o token NHI do run
// (Goal.Credential) chega à [referencemonitor.Call] mediada como Call.Credential — a
// posição EXACTA onde o hook de identidade o verifica. Sem este wiring, toda a call
// mediada seria anónima (Credential="") e o RM composto com o hook de identidade
// negaria fail-closed TODA a tool call.
func TestGoalCredentialFlowsToMediatedCall(t *testing.T) {
	// Com credencial: a call mediada carrega-a.
	spy := runEchoWithCredential(t, "nhi-token-abc")
	if !spy.saw {
		t.Fatal("o hook nunca viu uma call mediada — a tool não foi despachada?")
	}
	if spy.credential != "nhi-token-abc" {
		t.Fatalf("Call.Credential=%q, quero o token do goal (wiring Goal.Credential→Call.Credential partido)", spy.credential)
	}

	// Sem credencial no goal: a call mediada é ANÓNIMA (Credential vazio) — que o hook
	// de identidade real NEGA fail-closed (aqui o spy só regista, não nega). Prova que
	// o comportamento fail-closed de origem é preservado: sem token, sem autoridade.
	anon := runEchoWithCredential(t, "")
	if !anon.saw {
		t.Fatal("tool não despachada no caso sem-credencial")
	}
	if anon.credential != "" {
		t.Fatalf("Call.Credential=%q sem credencial no goal, quero vazio (anónimo)", anon.credential)
	}
}
