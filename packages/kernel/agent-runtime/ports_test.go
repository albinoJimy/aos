package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// echoToolset é o tool set de teste usado pelos testes das portas: uma tool "echo".
func echoToolset() map[string]referencemonitor.ToolFunc {
	return map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	}
}

// toolThenFinalModel é o guião de dois turnos: turno 1 chama echo; turno 2 conclui.
func toolThenFinalModel() ModelClient {
	turn := 0
	return ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		turn++
		if turn == 1 {
			return ModelResponse{
				Text:      "penso",
				ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}},
			}, nil
		}
		return ModelResponse{Final: true, Text: "fim"}, nil
	})
}

// ---------------------------------------------------------------------------
// AOS-037 — WindowPort (dono único do tail/assembly, D-TAIL)
// ---------------------------------------------------------------------------

// TestLoop_DefaultWindow_ByteIdentical prova que, sem WindowFactory ligada, o prompt
// montado pelo loop é BYTE-IDÊNTICO ao de um [PromptAssembler] directo com o mesmo tail
// semeado — a garantia de AOS-013 do default [inlineWindow].
func TestLoop_DefaultWindow_ByteIdentical(t *testing.T) {
	h := newHarness(t, nil)
	var got PromptView
	model := ModelClientFunc(func(_ context.Context, pv PromptView) (ModelResponse, error) {
		got = pv
		return ModelResponse{Final: true, Text: "fim"}, nil
	})
	rt := New(model, h.rm, h.recorder) // defaults: inlineWindow + directDispatcher
	g := sampleGoal()
	if _, err := rt.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Referência: montagem directa com o MESMO assembler e o tail semeado (só objective —
	// sampleGoal não tem MemoryContext).
	asm := NewPromptAssembler(g.System, g.Tools)
	want := asm.Assemble(1, []TailSegment{{Kind: TailObjective, Content: []byte(g.Objective)}})

	if !bytes.Equal(got.Materialized, want.Materialized) {
		t.Fatalf("prompt do default diverge do PromptAssembler directo:\n got=%q\nwant=%q", got.Materialized, want.Materialized)
	}
	if got.PrefixHash != want.PrefixHash || got.PromptHash != want.PromptHash {
		t.Fatalf("hashes divergem: prefix (got=%s want=%s) prompt (got=%s want=%s)", got.PrefixHash, want.PrefixHash, got.PromptHash, want.PromptHash)
	}
}

// TestLoop_SinglePrefixHash é a PROVA da decisão D-TAIL: existe UM só prefix-hash por
// run — byte-idêntico em TODOS os turnos —, materializado pelo dono único (a WindowPort).
func TestLoop_SinglePrefixHash(t *testing.T) {
	h := newHarness(t, echoToolset())
	var prefixHashes []string
	turn := 0
	model := ModelClientFunc(func(_ context.Context, pv PromptView) (ModelResponse, error) {
		prefixHashes = append(prefixHashes, pv.PrefixHash)
		turn++
		if turn < 3 { // 2 turnos com tool, depois final
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
		}
		return ModelResponse{Final: true, Text: "fim"}, nil
	})
	rt := New(model, h.rm, h.recorder)
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prefixHashes) < 3 {
		t.Fatalf("esperava >= 3 turnos, tive %d", len(prefixHashes))
	}
	for i, ph := range prefixHashes {
		if ph != prefixHashes[0] {
			t.Fatalf("prefix-hash do turno %d diverge do turno 1 (%s != %s) — D-TAIL exige um só prefix-hash por run", i+1, ph, prefixHashes[0])
		}
	}
}

// fakeWindow é uma [WindowPort] que delega num [PromptAssembler] real (vistas válidas)
// enquanto REGISTA cada Append e Assemble para asserção de que o loop delega a posse.
type fakeWindow struct {
	asm           *PromptAssembler
	tail          []TailSegment
	appended      []TailKind
	assembleTurns []int
}

func (w *fakeWindow) Append(seg TailSegment) {
	w.tail = append(w.tail, seg)
	w.appended = append(w.appended, seg.Kind)
}
func (w *fakeWindow) Assemble(_ context.Context, turn int) PromptView {
	w.assembleTurns = append(w.assembleTurns, turn)
	return w.asm.Assemble(turn, w.tail)
}
func (w *fakeWindow) SystemHash() string { return w.asm.SystemHash() }

type fakeWindowFactory struct {
	win    *fakeWindow
	runID  string
	system string
	tools  int
}

func (f *fakeWindowFactory) NewWindow(runID, system string, tools []ToolSpec) (WindowPort, error) {
	f.runID, f.system, f.tools = runID, system, len(tools)
	f.win.asm = NewPromptAssembler(system, tools)
	return f.win, nil
}

// TestLoop_WindowFactory_OwnsTail prova que o loop delega a posse do tail à WindowPort
// injectada: recebe os inputs do prefixo, e cada segmento (seed objective + history +
// tool_result + history) passa por Append; Assemble corre uma vez por turno com o número
// de turno do loop.
func TestLoop_WindowFactory_OwnsTail(t *testing.T) {
	h := newHarness(t, echoToolset())
	fw := &fakeWindow{}
	ff := &fakeWindowFactory{win: fw}
	rt := New(toolThenFinalModel(), h.rm, h.recorder, WithWindowFactory(ff))
	g := sampleGoal()
	if _, err := rt.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ff.runID != g.RunID || ff.system != g.System || ff.tools != len(g.Tools) {
		t.Fatalf("factory recebeu (runID=%q system=%q tools=%d), quero (%q,%q,%d)", ff.runID, ff.system, ff.tools, g.RunID, g.System, len(g.Tools))
	}
	// Objective (seed) + history(t1) + tool_result(t1) + history(t2).
	want := []TailKind{TailObjective, TailHistory, TailToolResult, TailHistory}
	if !kindsEqual(fw.appended, want) {
		t.Fatalf("appends=%v, quero %v (o loop delega a posse do tail à WindowPort)", fw.appended, want)
	}
	if len(fw.assembleTurns) != 2 || fw.assembleTurns[0] != 1 || fw.assembleTurns[1] != 2 {
		t.Fatalf("assembleTurns=%v, quero [1 2] (uma montagem por turno, com o turno do loop)", fw.assembleTurns)
	}
}

// TestLoop_WindowFactoryError_FailClosed prova que uma falha da fábrica aborta o run
// ANTES do primeiro turno (fail-closed: sem janela não há prompt).
func TestLoop_WindowFactoryError_FailClosed(t *testing.T) {
	h := newHarness(t, nil)
	sentinel := errors.New("prefixo inválido")
	rt := New(toolThenFinalModel(), h.rm, h.recorder, WithWindowFactory(failingWindowFactory{err: sentinel}))
	_, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrWindow) || !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, quero ErrWindow envolvendo a sentinela (fail-closed)", err)
	}
}

type failingWindowFactory struct{ err error }

func (f failingWindowFactory) NewWindow(string, string, []ToolSpec) (WindowPort, error) {
	return nil, f.err
}

// ---------------------------------------------------------------------------

type fakeDispatcher struct {
	rm    *referencemonitor.Monitor
	calls []referencemonitor.Call
}

func (d *fakeDispatcher) Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error) {
	d.calls = append(d.calls, call)
	return d.rm.Mediate(ctx, call)
}

// TestLoop_ActivityDispatcher_ReceivesFullCall prova o cerne da decisão de escopo do
// AOS-021: a porta recebe o [referencemonitor.Call] COMPLETO já construído pelo loop —
// com o Credential (o token NHI, AOS-152) e o taint —, pelo que um dispatcher durável
// nunca perde a identidade. O default seria Mediate directo; aqui um fake regista o Call.
func TestLoop_ActivityDispatcher_ReceivesFullCall(t *testing.T) {
	h := newHarness(t, echoToolset())
	d := &fakeDispatcher{rm: h.rm}
	rt := New(toolThenFinalModel(), h.rm, h.recorder, WithActivityDispatcher(d))
	g := sampleGoal()
	g.Credential = "nhi-token-abc" // o token NHI do run (AOS-152)
	if _, err := rt.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("dispatch calls=%d, quero 1 (uma tool call no turno 1)", len(d.calls))
	}
	if d.calls[0].Credential != "nhi-token-abc" {
		t.Fatalf("dispatcher recebeu Credential=%q, quero preservado (AOS-152 — identidade não se perde no despacho durável)", d.calls[0].Credential)
	}
	if d.calls[0].ToolID != "echo" {
		t.Fatalf("dispatcher recebeu ToolID=%q, quero echo (Call completo)", d.calls[0].ToolID)
	}
}

// kindsEqual compara duas sequências de TailKind.
func kindsEqual(a, b []TailKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
