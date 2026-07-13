package projection_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/record"
)

// buildTrajectory monta um registo de trajectória com n turnos. Cada turno leva um
// resumo higienizado curto e um conteúdo cru longo (que a projecção nunca vê).
func buildTrajectory(t *testing.T, traceID string, n int) *record.TrajectoryRecord {
	t.Helper()
	rec := record.NewTrajectoryRecord(traceID)
	for i := 1; i <= n; i++ {
		turn := record.Turn{
			Index:                 i,
			PromptHash:            "sha256:h",
			ModelID:               "claude-x",
			Params:                map[string]string{"seed": "0"},
			AssemblyVersion:       "1.0.0",
			ManifestSchemaVersion: "1.0",
			RawContent:            strings.Repeat("conteudo cru longo e detalhado ", 50),
			Summary:               "resumo do turno numero um dois tres",
		}
		if err := rec.AppendTurn(turn); err != nil {
			t.Fatal(err)
		}
		// Cada turno contribui vários spans para a árvore completa.
		for j := 0; j < 4; j++ {
			rec.AppendSpan(record.Span{ID: "s", Name: "op", Attributes: map[string]string{"k": "v"}})
		}
	}
	return rec
}

func TestProjectContext_Reproducible_ByteForByte(t *testing.T) {
	t.Parallel()
	pol := projection.DefaultPolicy()

	// Mesma trajectória (reconstruída de raiz) + mesma política -> mesmos bytes.
	recA := buildTrajectory(t, "trace-1", 6)
	recB := buildTrajectory(t, "trace-1", 6)

	a, err := projection.ProjectContext(record.View(recA), pol)
	if err != nil {
		t.Fatal(err)
	}
	b, err := projection.ProjectContext(record.View(recB), pol)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatalf("projecção não reproduzível byte-a-byte:\nA=%q\nB=%q", a.Bytes(), b.Bytes())
	}

	// Repetir a MESMA projecção também é estável.
	a2, err := projection.ProjectContext(record.View(recA), pol)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), a2.Bytes()) {
		t.Fatal("re-projecção da mesma vista divergiu")
	}
}

// TestProjectContext_DifferentPolicyVersion prova que a política versionada faz
// parte da identidade da injecção: a mesma trajectória sob versões diferentes
// produz bytes diferentes (a versão é observável).
func TestProjectContext_PolicyVersionInBytes(t *testing.T) {
	t.Parallel()
	rec := buildTrajectory(t, "trace-1", 3)
	p1 := projection.DefaultPolicy()
	p2 := projection.DefaultPolicy()
	p2.Version = "2.0.0"

	v1, err := projection.ProjectContext(record.View(rec), p1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := projection.ProjectContext(record.View(rec), p2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(v1.Bytes(), v2.Bytes()) {
		t.Fatal("versões de política diferentes deviam produzir injecções distintas")
	}
}

// TestProjectContext_TokenLimited prova que o resumo ao pai é LIMITADO em tokens:
// com um orçamento apertado, só alguns turnos entram (IncludedTurns < TotalTurns) e
// a contagem de tokens nunca excede o orçamento — enquanto o registo mantém todos.
func TestProjectContext_TokenLimited(t *testing.T) {
	t.Parallel()
	rec := buildTrajectory(t, "trace-1", 20)

	tests := []struct {
		name   string
		budget int
	}{
		{"orcamento apertado", 20},
		{"orcamento medio", 50},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pol := projection.DefaultPolicy().WithTokenBudget(tc.budget)
			v, err := projection.ProjectContext(record.View(rec), pol)
			if err != nil {
				t.Fatal(err)
			}
			if v.TokenCount > tc.budget {
				t.Fatalf("token_count=%d excede orçamento=%d", v.TokenCount, tc.budget)
			}
			if v.IncludedTurns >= v.TotalTurns {
				t.Fatalf("orçamento apertado devia descartar turnos do contexto (incl=%d total=%d)", v.IncludedTurns, v.TotalTurns)
			}
			if v.TotalTurns != 20 {
				t.Fatalf("a projecção devia observar os 20 turnos do registo, observou %d", v.TotalTurns)
			}
		})
	}
}

// TestContextVsRecord_BackendCompleteContextLimited é o teste central do Princípio
// 4: o resumo ao pai é limitado em tokens (descarta turnos do contexto) enquanto o
// backend recebe a trajectória COMPLETA — contagem de spans no backend estritamente
// maior do que a vista injectada, e o conteúdo cru presente no registo mas AUSENTE
// do contexto.
func TestContextVsRecord_BackendCompleteContextLimited(t *testing.T) {
	t.Parallel()
	rec := buildTrajectory(t, "trace-77", 12)

	// Via 1: projecção limitada em tokens (o que o pai vê).
	pol := projection.DefaultPolicy().WithTokenBudget(15)
	injected, err := projection.ProjectContext(record.View(rec), pol)
	if err != nil {
		t.Fatal(err)
	}

	// Via 2: persist da trajectória completa ao backend de observabilidade.
	tr := &agentruntime.RecordingTracer{}
	ev, err := record.Persist(context.Background(), rec, tr)
	if err != nil {
		t.Fatal(err)
	}

	// Contagem de spans no backend > vista injectada.
	backendSpans := len(tr.Spans())
	if backendSpans <= injected.IncludedTurns {
		t.Fatalf("backend (%d spans) devia exceder a vista injectada (%d turnos)", backendSpans, injected.IncludedTurns)
	}
	// A vista foi de facto limitada (descartou turnos do contexto).
	if injected.IncludedTurns >= injected.TotalTurns {
		t.Fatalf("a vista injectada não foi limitada (incl=%d total=%d)", injected.IncludedTurns, injected.TotalTurns)
	}
	// Backend tem TODOS os 12 turnos com conteúdo cru; contexto não tem conteúdo cru.
	if len(ev.Turns) != 12 {
		t.Fatalf("backend devia ter 12 turnos, tem %d", len(ev.Turns))
	}
	// Conteúdo cru presente no registo/backend...
	if ev.Turns[0].RawContent == "" {
		t.Fatal("conteúdo cru ausente do registo (trajectória não completa)")
	}
	// ...mas AUSENTE do contexto injectado (higiene legítima).
	if strings.Contains(injected.Summary, "conteudo cru longo") {
		t.Fatal("conteúdo cru vazou para o contexto injectado (higiene violada)")
	}
	// O resumo liga-se ao trace do backend.
	if injected.TraceID != ev.TraceID {
		t.Fatalf("resumo não ligado ao trace do backend: %q vs %q", injected.TraceID, ev.TraceID)
	}
}

func TestProjectContext_FailClosed(t *testing.T) {
	t.Parallel()
	rec := buildTrajectory(t, "trace-1", 2)

	tests := []struct {
		name    string
		view    record.RecordView
		policy  projection.Policy
		wantErr error
	}{
		{"vista nil", nil, projection.DefaultPolicy(), projection.ErrNilView},
		{"versao invalida", record.View(rec), projection.Policy{Version: "x", TokenBudget: 10}, projection.ErrInvalidPolicyVersion},
		{"orcamento zero", record.View(rec), projection.Policy{Version: "1.0.0", TokenBudget: 0}, projection.ErrInvalidTokenBudget},
		{"orcamento negativo", record.View(rec), projection.Policy{Version: "1.0.0", TokenBudget: -5}, projection.ErrInvalidTokenBudget},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := projection.ProjectContext(tc.view, tc.policy)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("esperava %v, obtive %v", tc.wantErr, err)
			}
		})
	}
}

// TestPolicy_SemVerValidation cobre a validação SemVer da política versionada.
func TestPolicy_SemVerValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v  string
		ok bool
	}{
		{"1.0.0", true},
		{"2.13.4", true},
		{"1.0", false},
		{"1.0.0.0", false},
		{"1..0", false},
		{"v1.0.0", false},
		{"1.0.x", false},
		{"", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.v, func(t *testing.T) {
			t.Parallel()
			p := projection.Policy{Version: tc.v, TokenBudget: 10}
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("versão %q devia ser válida, erro=%v", tc.v, err)
			}
			if !tc.ok && !errors.Is(err, projection.ErrInvalidPolicyVersion) {
				t.Fatalf("versão %q devia falhar SemVer, erro=%v", tc.v, err)
			}
		})
	}
}

// TestProjectContext_NilRecordView é o teste de regressão do fail-OPEN: um registo
// nil embrulhado por record.View(nil) NÃO pode atravessar o guarda como uma interface
// typed-nil e provocar um nil-deref — tem de rejeitar com ErrNilView. Antes da
// correcção, record.View(nil) devolvia readOnlyRecord{rec: nil} (interface NÃO-nil),
// passava `view == nil` e entrava em panic em TurnSummaries.
func TestProjectContext_NilRecordView(t *testing.T) {
	t.Parallel()

	// A vista construída a partir de um registo nil é ela própria nil (fail-closed
	// no ponto de entrada), pelo que a projecção a rejeita sem panic.
	view := record.View(nil)
	if view != nil {
		t.Fatalf("record.View(nil) devia ser uma RecordView nil, obtive %#v", view)
	}

	_, err := projection.ProjectContext(view, projection.DefaultPolicy())
	if !errors.Is(err, projection.ErrNilView) {
		t.Fatalf("ProjectContext(View(nil)) devia devolver ErrNilView, obtive %v", err)
	}
}

// TestProjectContext_SeparatorInBytes prova que o separador comportamental faz parte
// da identidade da injecção no artefacto de reprodutibilidade: duas políticas com a
// MESMA versão mas separadores diferentes produzem Bytes() distintos — o parâmetro
// deixa de ficar sem rasto.
func TestProjectContext_SeparatorInBytes(t *testing.T) {
	t.Parallel()
	rec := buildTrajectory(t, "trace-sep", 3)

	p1 := projection.DefaultPolicy() // Separator = "\n"
	p2 := projection.DefaultPolicy()
	p2.Separator = " | "

	v1, err := projection.ProjectContext(record.View(rec), p1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := projection.ProjectContext(record.View(rec), p2)
	if err != nil {
		t.Fatal(err)
	}

	if v1.Separator != "\n" || v2.Separator != " | " {
		t.Fatalf("separador não propagado para a InjectedView: %q vs %q", v1.Separator, v2.Separator)
	}
	if bytes.Equal(v1.Bytes(), v2.Bytes()) {
		t.Fatal("separadores diferentes deviam produzir artefactos de reprodutibilidade distintos")
	}
	// O separador é carimbado (quoted) no artefacto, numa única linha e sem ambiguidade.
	if !strings.Contains(string(v1.Bytes()), `separator="\n"`) {
		t.Fatalf("Bytes() devia carimbar o separador quoted:\n%s", v1.Bytes())
	}
}

// TestProjectContext_BudgetSkipsOversizedTurn prova que um único turno grande no
// início NÃO suprime os turnos seguintes que caberiam: o laço de orçamento salta o
// que não cabe (continue) e continua a aproveitar o orçamento com os posteriores.
func TestProjectContext_BudgetSkipsOversizedTurn(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-skip")
	// Turno 1: grande (não cabe). Turnos 2 e 3: pequenos (cabem folgadamente).
	big := record.Turn{
		Index: 1, PromptHash: "sha256:h", ModelID: "claude-x",
		Params: map[string]string{"seed": "0"}, AssemblyVersion: "1.0.0", ManifestSchemaVersion: "1.0",
		RawContent: "x", Summary: strings.Repeat("palavra ", 200),
	}
	if err := rec.AppendTurn(big); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 3; i++ {
		small := record.Turn{
			Index: i, PromptHash: "sha256:h", ModelID: "claude-x",
			Params: map[string]string{"seed": "0"}, AssemblyVersion: "1.0.0", ManifestSchemaVersion: "1.0",
			RawContent: "x", Summary: "curto",
		}
		if err := rec.AppendTurn(small); err != nil {
			t.Fatal(err)
		}
	}

	// Orçamento que rejeita o turno 1 (~200 tokens) mas acomoda os turnos 2 e 3.
	pol := projection.DefaultPolicy().WithTokenBudget(20)
	v, err := projection.ProjectContext(record.View(rec), pol)
	if err != nil {
		t.Fatal(err)
	}

	if v.TokenCount > 20 {
		t.Fatalf("token_count=%d excede o orçamento=20", v.TokenCount)
	}
	if v.TotalTurns != 3 {
		t.Fatalf("a projecção devia observar 3 turnos, observou %d", v.TotalTurns)
	}
	// Os dois turnos pequenos entram apesar do turno grande inicial não caber.
	if v.IncludedTurns != 2 {
		t.Fatalf("esperava 2 turnos incluídos (grande saltado, pequenos aproveitados), obtive %d", v.IncludedTurns)
	}
	if !strings.Contains(v.Summary, "curto") || strings.Contains(v.Summary, "palavra") {
		t.Fatalf("resumo devia conter os turnos pequenos e não o grande: %q", v.Summary)
	}
}

// TestProjectContext_EmptyTrajectory cobre o caso limite de uma trajectória vazia.
func TestProjectContext_EmptyTrajectory(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-empty")
	v, err := projection.ProjectContext(record.View(rec), projection.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if v.IncludedTurns != 0 || v.TotalTurns != 0 || v.Summary != "" {
		t.Fatalf("trajectória vazia devia projectar injecção vazia: %+v", v)
	}
}
