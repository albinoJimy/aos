package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// AOS-497 — UM RUN SEM DECLARAÇÃO DE ORIGEM GRAVA OS BYTES DA BASE.
//
// A origem da saída entra «às escuras»: ninguém a declara ainda, e um run que não a declara tem
// de deixar no log exactamente o que deixava. A fixture `testdata/aos497_base_sem_origem.json`
// foi gravada por ESTE teste sobre a base do ticket (commit d2488386, antes de qualquer linha do
// AOS-497), com `AOS497_GRAVAR_BASE=1`. O teste corre os mesmos guiões com o código corrente e
// compara, evento a evento, o tipo, o `step_id` e os BYTES do payload: os turnos
// (`turn.recorded`), as capturas (`replay.captured`), as mediações do Reference Monitor e a
// transição terminal (`run.state.transition`), selada como o nó a sela — com o veredicto do
// `Result`.
//
// Os guiões cobrem os três modos (`off`, `observe`, `enforce`), com e sem contrato, com uma e
// com várias tool calls no primeiro turno.

const aos497FixtureDaBase = "aos497_base_sem_origem.json"

type aos497EventoDaBase struct {
	Type    string
	StepID  string
	Payload json.RawMessage
}

type aos497RunDaBase struct {
	RunID   string
	Eventos []aos497EventoDaBase
}

// aos497GuioesDaBase devolve os runs da fixture: o goal (sem declaração de origem) e o guião.
func aos497GuioesDaBase() []struct {
	goal  agentruntime.Goal
	guiao []agentruntime.ModelResponse
} {
	uso := agentruntime.Usage{InputTokens: 10, OutputTokens: 5}
	echo := func(in string) agentruntime.ToolInvocation {
		return agentruntime.ToolInvocation{ToolID: "echo", Capability: "cap:echo", ResourceType: "doc", ResourceValue: "notes", Input: []byte(in)}
	}
	falha := agentruntime.ToolInvocation{ToolID: "falha", Capability: "cap:echo", Input: []byte(`{"url":"http://exemplo.invalid"}`)}
	pede := func(texto string, calls ...agentruntime.ToolInvocation) agentruntime.ModelResponse {
		return agentruntime.ModelResponse{Text: texto, ToolCalls: calls, StopReason: agentruntime.StopToolCalls, Usage: uso}
	}
	fim := func(texto string) agentruntime.ModelResponse {
		return agentruntime.ModelResponse{Text: texto, Final: true, StopReason: agentruntime.StopStop, Usage: uso}
	}

	leitura := aos489Goal("run-aos497-base-observe")
	leitura.CompletionRequires = []string{"echo"}
	leitura.CompletionMode = agentruntime.CompletionObserve

	varias := aos489Goal("run-aos497-base-enforce")
	varias.CompletionMode = agentruntime.CompletionEnforce

	desligado := aos489Goal("run-aos497-base-off")
	desligado.CompletionRequires = []string{"echo"}

	semTexto := aos489Goal("run-aos497-base-negativo")
	semTexto.CompletionRequires = []string{"echo"}
	semTexto.CompletionMode = agentruntime.CompletionEnforce

	return []struct {
		goal  agentruntime.Goal
		guiao []agentruntime.ModelResponse
	}{
		{leitura, []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("lido")}},
		{varias, []agentruntime.ModelResponse{pede("tres", echo(`{"n":1}`), falha, echo(`{"n":2}`)), pede("mais uma", echo(`{"n":3}`)), fim("feito")}},
		{desligado, []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("lido")}},
		{semTexto, []agentruntime.ModelResponse{fim("nao chamei nada")}},
	}
}

// aos497CorrerEVerter corre os guiões sobre uma bancada nova, sela cada run como o nó o sela e
// devolve os eventos de cada um.
func aos497CorrerEVerter(t *testing.T) []aos497RunDaBase {
	t.Helper()
	b := novaBancada(t)
	relogio := state.ClockFunc(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	var out []aos497RunDaBase
	for _, c := range aos497GuioesDaBase() {
		res, err := b.correr(c.goal, c.guiao)
		if err != nil {
			t.Fatalf("Run %s: %v", c.goal.RunID, err)
		}
		m, err := state.NewMachine(b.store, c.goal.RunID, state.WithClock(relogio))
		if err != nil {
			t.Fatalf("NewMachine: %v", err)
		}
		if err := m.Transition(context.Background(), state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
			t.Fatalf("claim: %v", err)
		}
		destino, razao := state.Complete, "run_complete"
		if res.Unfulfilled {
			destino, razao = state.Failed, "objective_unfulfilled"
		}
		if err := m.Transition(context.Background(), destino, state.TransitionEvent{Reason: razao, Verdict: res.Verdict}); err != nil {
			t.Fatalf("selo terminal: %v", err)
		}
		run := aos497RunDaBase{RunID: c.goal.RunID}
		for _, ev := range b.eventos(c.goal.RunID) {
			run.Eventos = append(run.Eventos, aos497EventoDaBase{Type: ev.Type, StepID: ev.StepID, Payload: append(json.RawMessage(nil), ev.Payload...)})
		}
		out = append(out, run)
	}
	return out
}

func TestAOS497_SemDeclaracaoDeOrigem_OsEventosSaoOsDaBase(t *testing.T) {
	caminho := filepath.Join("testdata", aos497FixtureDaBase)
	obtido := aos497CorrerEVerter(t)

	if os.Getenv("AOS497_GRAVAR_BASE") == "1" {
		raw, err := json.MarshalIndent(obtido, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(caminho, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("fixture gravada em %s", caminho)
	}

	raw, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ler a fixture da base: %v", err)
	}
	var base []aos497RunDaBase
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("a fixture da base nao e JSON valido: %v", err)
	}
	if len(base) != len(obtido) || len(base) != 4 {
		t.Fatalf("a fixture tem %d run(s) e o codigo corrente gravou %d; queria 4 e 4", len(base), len(obtido))
	}
	tipos := map[string]int{}
	for i, b := range base {
		o := obtido[i]
		if b.RunID != o.RunID {
			t.Fatalf("run %d: fixture %q, corrente %q", i, b.RunID, o.RunID)
		}
		if len(b.Eventos) != len(o.Eventos) {
			t.Fatalf("%s: a base gravou %d eventos e o codigo corrente %d", b.RunID, len(b.Eventos), len(o.Eventos))
		}
		for j, eb := range b.Eventos {
			eo := o.Eventos[j]
			if eb.Type != eo.Type || eb.StepID != eo.StepID {
				t.Fatalf("%s evento %d: a base tem %s/%s e o codigo corrente %s/%s", b.RunID, j, eb.Type, eb.StepID, eo.Type, eo.StepID)
			}
			// A fixture passa por MarshalIndent; compara-se a forma compacta dos dois lados, que
			// preserva a ordem e o conteúdo de cada campo.
			var cb, co bytes.Buffer
			if err := json.Compact(&cb, eb.Payload); err != nil {
				t.Fatal(err)
			}
			if err := json.Compact(&co, eo.Payload); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cb.Bytes(), co.Bytes()) {
				t.Fatalf("%s evento %d (%s %s): o payload mudou face a base\n base     %s\n corrente %s", b.RunID, j, eb.Type, eb.StepID, cb.Bytes(), co.Bytes())
			}
			if !bytes.Equal(co.Bytes(), eo.Payload) {
				t.Fatalf("%s evento %d: o payload gravado nao esta na forma compacta — a comparacao acima deixaria de ser byte a byte", b.RunID, j)
			}
			tipos[eb.Type]++
		}
	}
	// A fixture cobre os quatro tipos que este ticket podia ter mudado. Sem isto, uma fixture
	// regravada sem transições passava a não provar nada sobre elas.
	for _, tipo := range []string{agentruntime.EventTypeTurnRecorded, EventTypeCaptured, state.EventTypeTransition} {
		if tipos[tipo] == 0 {
			t.Fatalf("a fixture da base nao tem nenhum evento %s", tipo)
		}
	}
	for _, campo := range []string{"output_from", "output_source"} {
		if bytes.Contains(raw, []byte(campo)) {
			t.Fatalf("a fixture da base tem o campo %q: nao foi gravada na base", campo)
		}
	}
}
