package record_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/record"
)

// completeTurn devolve um turno com o manifesto por turno completo.
func completeTurn(idx int, summary, raw string) record.Turn {
	return record.Turn{
		Index:                 idx,
		PromptHash:            "sha256:deadbeef",
		ModelID:               "claude-x",
		Params:                map[string]string{"temperature": "0", "top_p": "1"},
		AssemblyVersion:       "1.0.0",
		ManifestSchemaVersion: "1.0",
		RawContent:            raw,
		Summary:               summary,
	}
}

func TestAppendTurn_FailClosedOnIncompleteManifest(t *testing.T) {
	t.Parallel()
	base := completeTurn(1, "s", "r")

	tests := []struct {
		name   string
		mut    func(record.Turn) record.Turn
		wantOK bool
	}{
		{"completo", func(x record.Turn) record.Turn { return x }, true},
		{"sem prompt_hash", func(x record.Turn) record.Turn { x.PromptHash = ""; return x }, false},
		{"sem model_id", func(x record.Turn) record.Turn { x.ModelID = ""; return x }, false},
		{"sem assembly_version", func(x record.Turn) record.Turn { x.AssemblyVersion = ""; return x }, false},
		{"sem manifest_schema", func(x record.Turn) record.Turn { x.ManifestSchemaVersion = ""; return x }, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := record.NewTrajectoryRecord("trace-1")
			err := rec.AppendTurn(tc.mut(base))
			if tc.wantOK {
				if err != nil {
					t.Fatalf("esperava sucesso, obtive %v", err)
				}
				if rec.TurnCount() != 1 {
					t.Fatalf("esperava 1 turno, obtive %d", rec.TurnCount())
				}
				return
			}
			if !errors.Is(err, record.ErrIncompleteTurnManifest) {
				t.Fatalf("esperava ErrIncompleteTurnManifest, obtive %v", err)
			}
			if rec.TurnCount() != 0 {
				t.Fatalf("turno incompleto NÃO devia ser registado (count=%d)", rec.TurnCount())
			}
		})
	}
}

// TestAppendTurn_ClonesInput prova que o registo não partilha estado mutável com o
// chamador: mutar o mapa Params após o append não altera o registado.
func TestAppendTurn_ClonesInput(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-1")
	turn := completeTurn(1, "s", "r")
	if err := rec.AppendTurn(turn); err != nil {
		t.Fatal(err)
	}
	turn.Params["temperature"] = "0.9" // mutação após o append

	ev, err := record.Persist(context.Background(), rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ev.Turns[0].Params["temperature"]; got != "0" {
		t.Fatalf("registo partilhou estado mutável: temperature=%q", got)
	}
}

// TestPersist_EmitsCompleteTrajectory prova que o backend recebe a trajectória
// COMPLETA — todos os turnos com conteúdo cru e manifesto, mais a árvore de spans —
// e que o nº de spans emitidos é estritamente maior do que o nº de turnos (raiz +
// um por turno + a árvore). É a base da comparação "backend > vista injectada".
func TestPersist_EmitsCompleteTrajectory(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-42")
	for i := 1; i <= 3; i++ {
		if err := rec.AppendTurn(completeTurn(i, "resumo", "CONTEUDO-CRU-COMPLETO")); err != nil {
			t.Fatal(err)
		}
	}
	// Árvore de spans completa (5 nós).
	for i := 0; i < 5; i++ {
		rec.AppendSpan(record.Span{ID: "sp" + string(rune('a'+i)), Name: "op", Attributes: map[string]string{"k": "v"}})
	}

	tr := &agentruntime.RecordingTracer{}
	ev, err := record.Persist(context.Background(), rec, tr)
	if err != nil {
		t.Fatal(err)
	}

	if len(ev.Turns) != 3 {
		t.Fatalf("evento devia ter 3 turnos, tem %d", len(ev.Turns))
	}
	if len(ev.Spans) != 5 {
		t.Fatalf("evento devia ter 5 spans, tem %d", len(ev.Spans))
	}
	// Conteúdo cru completo preservado no registo.
	for _, et := range ev.Turns {
		if et.RawContent != "CONTEUDO-CRU-COMPLETO" {
			t.Fatalf("conteúdo cru truncado/descartado no registo: %q", et.RawContent)
		}
	}
	// Spans emitidos = 1 raiz + 3 turnos + 5 spans = 9.
	wantEmitted := 1 + 3 + 5
	if ev.EmittedSpans != wantEmitted {
		t.Fatalf("EmittedSpans=%d, esperava %d", ev.EmittedSpans, wantEmitted)
	}
	if got := len(tr.Spans()); got != wantEmitted {
		t.Fatalf("tracer capturou %d spans, esperava %d", got, wantEmitted)
	}
	// Todos os spans capturados foram fechados (End).
	for _, s := range tr.Spans() {
		if !s.Ended {
			t.Fatalf("span %q não foi fechado", s.Operation)
		}
	}
}

// TestPersist_RecordsPerTurnManifest prova que o hash do prompt materializado,
// model-id e versões são gravados por turno no backend, INDEPENDENTEMENTE do que a
// projecção tenha higienizado no Summary (aqui o Summary é vazio, mas o manifesto
// mantém-se intacto no registo/backend).
func TestPersist_RecordsPerTurnManifest(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-9")
	turn := completeTurn(1, "" /* summary higienizado a vazio */, "raw")
	turn.PromptHash = "sha256:abc123"
	turn.ModelID = "claude-test"
	if err := rec.AppendTurn(turn); err != nil {
		t.Fatal(err)
	}

	tr := &agentruntime.RecordingTracer{}
	ev, err := record.Persist(context.Background(), rec, tr)
	if err != nil {
		t.Fatal(err)
	}

	// No evento persistido.
	if ev.Turns[0].PromptHash != "sha256:abc123" || ev.Turns[0].ModelID != "claude-test" {
		t.Fatalf("manifesto por turno não gravado no evento: %+v", ev.Turns[0])
	}
	if ev.Turns[0].AssemblyVersion == "" || ev.Turns[0].ManifestSchemaVersion == "" {
		t.Fatalf("versões do manifesto por turno em falta: %+v", ev.Turns[0])
	}

	// Nos atributos do span do turno emitido ao backend.
	turnSpans := tr.SpansByOperation("trajectory.turn")
	if len(turnSpans) != 1 {
		t.Fatalf("esperava 1 span de turno, obtive %d", len(turnSpans))
	}
	attrs := turnSpans[0].Attributes
	if attrs[agentruntime.AttrPromptHash] != "sha256:abc123" {
		t.Fatalf("prompt_hash não emitido ao backend: %v", attrs[agentruntime.AttrPromptHash])
	}
	if attrs[agentruntime.AttrRequestModel] != "claude-test" {
		t.Fatalf("model-id não emitido ao backend: %v", attrs[agentruntime.AttrRequestModel])
	}
}

func TestPersist_NilRecord(t *testing.T) {
	t.Parallel()
	if _, err := record.Persist(context.Background(), nil, nil); !errors.Is(err, record.ErrNilRecord) {
		t.Fatalf("esperava ErrNilRecord, obtive %v", err)
	}
}

// TestPersist_DeterministicParamOrder prova que os parâmetros do modelo são
// emitidos por ordem estável de chave (sem não-determinismo de iteração de mapa).
func TestPersist_DeterministicParamOrder(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-x")
	turn := completeTurn(1, "s", "r")
	turn.Params = map[string]string{"z": "1", "a": "2", "m": "3"}
	if err := rec.AppendTurn(turn); err != nil {
		t.Fatal(err)
	}
	if keys := turn.SortedParamKeys(); strings.Join(keys, ",") != "a,m,z" {
		t.Fatalf("ordem de chaves não estável: %v", keys)
	}
}
