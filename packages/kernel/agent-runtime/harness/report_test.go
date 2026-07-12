package harness

import (
	"bytes"
	"testing"
)

// TestFidelityReportErr cobre todos os ramos de [FidelityReport.Err]: cada modo de
// falha produz um erro fail-closed distinto, e um relatório Pass devolve nil.
func TestFidelityReportErr(t *testing.T) {
	cases := []struct {
		name    string
		rep     FidelityReport
		wantErr bool
		substr  string
	}{
		{
			name:    "pass",
			rep:     FidelityReport{Pass: true, ReplayFidelity: 1.0},
			wantErr: false,
		},
		{
			name:    "diverged",
			rep:     FidelityReport{Diverged: true, DivergenceStepID: "step-000002", DivergenceReason: "prompt_hash", ReplayFidelity: 0.5},
			wantErr: true,
			substr:  "divergiu",
		},
		{
			name:    "fidelidade parcial sem flag diverged",
			rep:     FidelityReport{ReplayFidelity: 0.75},
			wantErr: true,
			substr:  "replay-fidelity",
		},
		{
			name:    "efeitos duplicados",
			rep:     FidelityReport{ReplayFidelity: 1.0, DuplicatedEffects: 3},
			wantErr: true,
			substr:  "duplicado",
		},
		{
			name:    "retoma divergente",
			rep:     FidelityReport{ReplayFidelity: 1.0, ResumeMismatches: 1},
			wantErr: true,
			substr:  "retoma",
		},
		{
			name:    "falha sem causa específica",
			rep:     FidelityReport{ReplayFidelity: 1.0},
			wantErr: true,
			substr:  "falhou",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rep.Err()
			if tc.wantErr && err == nil {
				t.Fatalf("esperava erro, obtive nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("esperava nil, obtive %v", err)
			}
			if tc.wantErr && tc.substr != "" && !bytes.Contains([]byte(err.Error()), []byte(tc.substr)) {
				t.Fatalf("erro %q não contém %q", err.Error(), tc.substr)
			}
		})
	}
}

// TestReportSerialization cobre JSON/CompactJSON de ambos os relatórios: estável,
// não-vazio e com os campos-chave.
func TestReportSerialization(t *testing.T) {
	rep := FidelityReport{Name: "x", RunID: "run_x", Turns: 3, ReplayFidelity: 1.0, EffectsVerified: 2, Pass: true}
	if !bytes.Contains(rep.JSON(), []byte(`"replay_fidelity": 1`)) {
		t.Fatalf("FidelityReport.JSON sem replay_fidelity: %s", rep.JSON())
	}
	if !bytes.Contains(rep.CompactJSON(), []byte(`"run_id":"run_x"`)) {
		t.Fatalf("FidelityReport.CompactJSON sem run_id: %s", rep.CompactJSON())
	}
	// Estabilidade: dois marshals idênticos.
	if !bytes.Equal(rep.CompactJSON(), rep.CompactJSON()) {
		t.Fatalf("CompactJSON não estável")
	}

	agg := AggregateReport{Cases: []FidelityReport{rep}, Runs: 1, MeanReplayFidelity: 1.0, TotalEffectsVerified: 2, Pass: true}
	if !bytes.Contains(agg.JSON(), []byte(`"mean_replay_fidelity": 1`)) {
		t.Fatalf("AggregateReport.JSON sem mean_replay_fidelity: %s", agg.JSON())
	}
	if !bytes.Contains(agg.CompactJSON(), []byte(`"pass":true`)) {
		t.Fatalf("AggregateReport.CompactJSON sem pass: %s", agg.CompactJSON())
	}
}

// TestAggregateReportErr cobre [AggregateReport.Err]: Pass ⇒ nil; falha ⇒ o
// primeiro erro de caso descritivo (com o nome do caso).
func TestAggregateReportErr(t *testing.T) {
	ok := AggregateReport{Pass: true}
	if err := ok.Err(); err != nil {
		t.Fatalf("agregado Pass devia dar nil, obtive %v", err)
	}
	bad := AggregateReport{
		Pass: false,
		Cases: []FidelityReport{
			{Name: "ok-case", Pass: true, ReplayFidelity: 1.0},
			{Name: "bad-case", Diverged: true, DivergenceStepID: "step-000001", DivergenceReason: "prompt_hash"},
		},
	}
	err := bad.Err()
	if err == nil {
		t.Fatalf("agregado falhado devia dar erro")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("bad-case")) {
		t.Fatalf("erro do agregado devia nomear o caso falhado: %v", err)
	}

	// Falha sem caso concreto (defensivo): Pass=false, sem casos falhados listados.
	inconsistent := AggregateReport{Pass: false, Cases: []FidelityReport{{Name: "c", Pass: true, ReplayFidelity: 1.0}}}
	if inconsistent.Err() == nil {
		t.Fatalf("Pass=false devia sempre dar erro, mesmo sem caso falhado explícito")
	}
}
