package agentruntime

import (
	"bytes"
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// fakeSteer é um [SteerSource] de teste: pausa na N-ésima fronteira de fim-de-turno e
// entrega uma correcção UMA vez.
type fakeSteer struct {
	pauseOnCall int // pausa quando GracefulPause é chamado esta vez (0 ⇒ nunca)
	calls       int
	correction  []byte
	corrGiven   bool
}

func (s *fakeSteer) GracefulPause(context.Context, string) (bool, error) {
	s.calls++
	return s.pauseOnCall > 0 && s.calls >= s.pauseOnCall, nil
}

func (s *fakeSteer) PendingCorrection(context.Context, string) ([]byte, bool) {
	if s.correction != nil && !s.corrGiven {
		s.corrGiven = true
		return s.correction, true
	}
	return nil, false
}

// TestTailFromCorrection_Trusted prova o marcador de proveniência: a correcção é
// dado de controlo TRUSTED (taint=trusted), com Kind [TailCorrection] — nunca untrusted.
func TestTailFromCorrection_Trusted(t *testing.T) {
	seg := tailFromCorrection([]byte("corrige o rumo"))
	if seg.Kind != TailCorrection {
		t.Fatalf("Kind=%q, quero %q", seg.Kind, TailCorrection)
	}
	// AssemblyVersion 1.3.0: o rotulo trusted vive na LINHA DE DELIMITACAO.
	if got := rotuloDe(seg, "taint"); got != TaintTrusted {
		t.Fatalf("taint=%q, quero %q", got, TaintTrusted)
	}
	if bytes.Contains(seg.Content, []byte("taint=")) {
		t.Fatalf("o rotulo TRUSTED voltou ao CORPO: %q", seg.Content)
	}
	if !bytes.Equal(seg.Content, []byte("corrige o rumo")) {
		t.Fatalf("o corpo devia ser so a correccao: %q", seg.Content)
	}
}

// TestLoop_SteerPauseStopsGracefully: um interrupt pendente na fronteira de fim-de-turno
// pára o loop GRACIOSAMENTE (entre turnos) — Result.Paused, não Terminated (AOS-158).
func TestLoop_SteerPauseStopsGracefully(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	})
	// Modelo que NUNCA termina (pede sempre a tool) — só o steer pára o loop.
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
	})
	steer := &fakeSteer{pauseOnCall: 2} // pausa na 2ª fronteira (fim do turno 2)
	rt := New(model, h.rm, h.recorder, WithSteerSource(steer))

	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Paused {
		t.Fatal("run devia estar Paused (interrupt no fim do turno)")
	}
	if res.Terminated {
		t.Fatal("run pausado não devia estar Terminated")
	}
	if res.Turns != 2 {
		t.Fatalf("Turns=%d, quero 2 (pausou na 2ª fronteira)", res.Turns)
	}
}

// TestLoop_SteerCorrectionInjectedTrusted: uma correcção pendente é injectada no tail do
// turno SEGUINTE como dado de controlo TRUSTED — o prompt do turno 2 contém-na marcada
// taint=trusted (nunca untrusted).
func TestLoop_SteerCorrectionInjectedTrusted(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	})
	var views []PromptView
	turn := 0
	model := ModelClientFunc(func(_ context.Context, pv PromptView) (ModelResponse, error) {
		views = append(views, pv)
		turn++
		if turn == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
		}
		return ModelResponse{Final: true, Text: "fim"}, nil
	})
	steer := &fakeSteer{correction: []byte("prioriza a superficie desktop")}
	rt := New(model, h.rm, h.recorder, WithSteerSource(steer))

	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(views) < 2 {
		t.Fatalf("esperava >= 2 turnos, tive %d", len(views))
	}
	mat := views[1].Materialized // prompt do turno seguinte à correcção
	if !bytes.Contains(mat, []byte("prioriza a superficie desktop")) {
		t.Fatal("correcção não injectada no prompt do turno seguinte")
	}
	// AssemblyVersion 1.3.0: o rotulo trusted esta na LINHA DE DELIMITACAO do segmento,
	// nao numa linha do corpo. Procurar so por "taint=trusted" passaria com o rotulo em
	// qualquer sitio — incluindo escrito por conteudo untrusted, que e o que a 1.3.0
	// impede. A asserção tem de ser sobre o DELIMITADOR.
	if !bytes.Contains(mat, []byte("<"+string(TailCorrection)+" taint="+TaintTrusted+">")) {
		t.Fatalf("correcção não marcada trusted na linha de delimitação:\n%s", mat)
	}
}

// TestLoop_NoSteerSource_Unchanged: sem [WithSteerSource] o loop nunca consulta o steer
// — o comportamento de AOS-013 permanece (nunca Paused; termina normalmente).
func TestLoop_NoSteerSource_Unchanged(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{})
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Final: true, Text: "fim"}, nil
	})
	rt := New(model, h.rm, h.recorder) // sem WithSteerSource
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Paused {
		t.Fatal("sem SteerSource o run nunca deve ficar Paused")
	}
	if !res.Terminated {
		t.Fatal("run devia terminar normalmente")
	}
}
