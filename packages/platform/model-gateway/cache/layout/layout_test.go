package layout_test

import (
	"errors"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"

	"github.com/aos-ref/platform/model-gateway/cache/layout"
)

// --- helpers deterministas -------------------------------------------------

func toolsAB() []agentruntime.ToolSpec {
	return []agentruntime.ToolSpec{
		{Name: "tool.a", Version: "1.0.0", Digest: "sha256:da"},
		{Name: "tool.b", Version: "1.0.0", Digest: "sha256:db"},
	}
}

func toolsBA() []agentruntime.ToolSpec {
	return []agentruntime.ToolSpec{
		{Name: "tool.b", Version: "1.0.0", Digest: "sha256:db"},
		{Name: "tool.a", Version: "1.0.0", Digest: "sha256:da"},
	}
}

func seg(kind agentruntime.TailKind, s string) agentruntime.TailSegment {
	return agentruntime.TailSegment{Kind: kind, Content: []byte(s)}
}

// mkTurn materializa um turno a partir de um assembler cache-estável e projecta-o
// num layout.Turn (o que cache/freeze faz em produção).
func mkTurn(runID string, idx int, tsHash string, a *agentruntime.PromptAssembler, tail []agentruntime.TailSegment) layout.Turn {
	return layout.Turn{RunID: runID, Index: idx, ToolSetHash: tsHash, View: a.Assemble(idx, tail)}
}

// capturingSink guarda as violações sinalizadas (prova o "sinaliza" além de "rejeita").
type capturingSink struct{ got []*layout.Violation }

func (c *capturingSink) Signal(v *layout.Violation) { c.got = append(c.got, v) }

// --- Byte-identidade do prefixo + tail append-only (caminho feliz) ---------

func TestGuard_HappyPath_PrefixByteIdenticalTailAppendOnly(t *testing.T) {
	t.Parallel()
	a := agentruntime.NewPromptAssembler("SYS", toolsAB())
	g := layout.NewGuard(nil)
	const run = "run-1"

	// Turno 1: pina o prefixo e o tool set.
	if err := g.Admit(mkTurn(run, 1, "ts#1", a, []agentruntime.TailSegment{
		seg(agentruntime.TailObjective, "objetivo"),
	})); err != nil {
		t.Fatalf("turno 1: %v", err)
	}
	// Turno 2: MESMO prefixo, tail ESTENDE o do turno 1 (append-only).
	if err := g.Admit(mkTurn(run, 2, "ts#1", a, []agentruntime.TailSegment{
		seg(agentruntime.TailObjective, "objetivo"),
		seg(agentruntime.TailToolResult, "resultado"),
	})); err != nil {
		t.Fatalf("turno 2: %v", err)
	}
	// Turno 3: continua a estender.
	if err := g.Admit(mkTurn(run, 3, "ts#1", a, []agentruntime.TailSegment{
		seg(agentruntime.TailObjective, "objetivo"),
		seg(agentruntime.TailToolResult, "resultado"),
		seg(agentruntime.TailHistory, "historico"),
	})); err != nil {
		t.Fatalf("turno 3: %v", err)
	}

	pinned, ok := g.Pinned(run)
	if !ok {
		t.Fatalf("run nao pinado")
	}
	if pinned.PrefixHash != a.PrefixHash() {
		t.Errorf("PrefixHash pinado = %s, quer %s", pinned.PrefixHash, a.PrefixHash())
	}
	if pinned.ToolSetHash != "ts#1" {
		t.Errorf("ToolSetHash pinado = %s, quer ts#1", pinned.ToolSetHash)
	}
	if pinned.LastTurn != 3 {
		t.Errorf("LastTurn = %d, quer 3", pinned.LastTurn)
	}
}

// --- Rejeição/sinalização de REORDENAÇÃO do prefixo ------------------------

func TestGuard_RejectsPrefixReorder(t *testing.T) {
	t.Parallel()
	aAB := agentruntime.NewPromptAssembler("SYS", toolsAB())
	aBA := agentruntime.NewPromptAssembler("SYS", toolsBA()) // MESMAS tools, ordem trocada

	// Sanidade: a reordenação muda o prefixo byte-a-byte.
	if aAB.PrefixHash() == aBA.PrefixHash() {
		t.Fatalf("pre-condicao: reordenacao deveria mudar o prefixo")
	}

	sink := &capturingSink{}
	g := layout.NewGuard(nil, layout.WithSink(sink))
	const run = "run-reorder"

	if err := g.Admit(mkTurn(run, 1, "ts#1", aAB, nil)); err != nil {
		t.Fatalf("turno 1: %v", err)
	}
	// Turno 2 com o prefixo REORDENADO ⇒ rejeição fail-closed.
	err := g.Admit(mkTurn(run, 2, "ts#1", aBA, nil))
	if !errors.Is(err, layout.ErrPrefixReordered) {
		t.Fatalf("turno 2: err = %v, quer ErrPrefixReordered", err)
	}
	// SINALIZADO (para além de rejeitado).
	if len(sink.got) != 1 || sink.got[0].Kind != layout.KindPrefixReordered {
		t.Fatalf("sink = %+v, quer 1 sinal prefix_reordered", sink.got)
	}
	// A mensagem tipada identifica o run/turno/natureza (diagnóstico + variância).
	if msg := sink.got[0].Error(); msg == "" {
		t.Errorf("Violation.Error() vazio")
	}
	// O estado NÃO avançou (turno 2 não foi gravado).
	if _, ok := g.PromptHash(run, 2); ok {
		t.Errorf("turno 2 rejeitado NAO devia ter entrada no manifesto")
	}
}

// --- Tail append-only: reescrever/encolher o tail é rejeitado --------------

func TestGuard_TailAppendOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		turn2   []agentruntime.TailSegment
		wantErr error
	}{
		{
			// Conteúdo DIFERENTE mas não mais curto (>= comprimento do turno 1), para
			// isolar o REESCREVER do ENCOLHER: os primeiros bytes divergem do pinado.
			name:    "reescrever o primeiro segmento",
			turn2:   []agentruntime.TailSegment{seg(agentruntime.TailObjective, "OUTRO conteudo bem mais longo do que o original")},
			wantErr: layout.ErrTailRewritten,
		},
		{
			name:    "encolher o tail (mais curto que o anterior)",
			turn2:   nil,
			wantErr: layout.ErrTailShrunk,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := agentruntime.NewPromptAssembler("SYS", toolsAB())
			g := layout.NewGuard(nil)
			const run = "run-tail"
			// Turno 1: tail não-vazio.
			if err := g.Admit(mkTurn(run, 1, "ts#1", a, []agentruntime.TailSegment{
				seg(agentruntime.TailObjective, "objetivo original"),
			})); err != nil {
				t.Fatalf("turno 1: %v", err)
			}
			err := g.Admit(mkTurn(run, 2, "ts#1", a, tc.turn2))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("turno 2: err = %v, quer %v", err, tc.wantErr)
			}
		})
	}
}

// --- Tool set congelado: um tool-set-hash divergente a meio do run é rejeitado.
// (Complementa o teste de comportamento de cache/freeze — aqui é a guarda pura.)

func TestGuard_RejectsToolSetDrift(t *testing.T) {
	t.Parallel()
	a := agentruntime.NewPromptAssembler("SYS", toolsAB())
	g := layout.NewGuard(nil)
	const run = "run-drift"

	if err := g.Admit(mkTurn(run, 1, "ts#congelado", a, nil)); err != nil {
		t.Fatalf("turno 1: %v", err)
	}
	// MESMO prefixo, MAS tool-set-hash divergente ⇒ drift rejeitado.
	err := g.Admit(mkTurn(run, 2, "ts#novo", a, nil))
	if !errors.Is(err, layout.ErrToolSetDrift) {
		t.Fatalf("turno 2: err = %v, quer ErrToolSetDrift", err)
	}
}

// --- Fronteira prefixo/tail corrompida -------------------------------------

func TestGuard_RejectsMaterializedSplit(t *testing.T) {
	t.Parallel()
	g := layout.NewGuard(nil)
	// View forjada: o materializado NÃO começa pelo prefixo.
	bad := layout.Turn{
		RunID:       "run-split",
		Index:       1,
		ToolSetHash: "ts#1",
		View: agentruntime.PromptView{
			Turn:         1,
			Prefix:       []byte("PREFIXO-IMUTAVEL"),
			Materialized: []byte("XXX diferente"),
		},
	}
	if err := g.Admit(bad); !errors.Is(err, layout.ErrMaterializedSplit) {
		t.Fatalf("err = %v, quer ErrMaterializedSplit", err)
	}
}

// --- Replay fiel: o hash materializado por turno reproduz-se ----------------

func TestGuard_Replay_DeterministicHashPerTurn(t *testing.T) {
	t.Parallel()
	a := agentruntime.NewPromptAssembler("SYS", toolsAB())
	g := layout.NewGuard(nil)
	const run = "run-replay"

	tail1 := []agentruntime.TailSegment{seg(agentruntime.TailObjective, "obj")}
	tail2 := []agentruntime.TailSegment{seg(agentruntime.TailObjective, "obj"), seg(agentruntime.TailToolResult, "res")}

	t1 := mkTurn(run, 1, "ts#1", a, tail1)
	t2 := mkTurn(run, 2, "ts#1", a, tail2)
	if err := g.Admit(t1); err != nil {
		t.Fatalf("admit 1: %v", err)
	}
	if err := g.Admit(t2); err != nil {
		t.Fatalf("admit 2: %v", err)
	}

	// O hash gravado por turno = o hash do materializado.
	h1, ok := g.PromptHash(run, 1)
	if !ok || h1 != t1.View.PromptHash {
		t.Fatalf("PromptHash(1) = %q,%v, quer %q", h1, ok, t1.View.PromptHash)
	}

	// Reproduzir o turno (re-materializar os MESMOS inputs) recomputa o MESMO hash.
	replay := a.Assemble(1, tail1)
	if replay.PromptHash != t1.View.PromptHash {
		t.Fatalf("re-materializacao nao determinista: %q != %q", replay.PromptHash, t1.View.PromptHash)
	}
	if err := g.Replay(run, 1, replay); err != nil {
		t.Fatalf("Replay(1) fiel devia passar: %v", err)
	}

	// Um materializado DIVERGENTE para o turno 1 é detectado (replay infiel).
	divergent := a.Assemble(1, tail2) // tail do turno 2 no slot do turno 1
	if err := g.Replay(run, 1, divergent); !errors.Is(err, layout.ErrReplayMismatch) {
		t.Fatalf("Replay divergente: err = %v, quer ErrReplayMismatch", err)
	}
	// Replay de um turno inexistente.
	if err := g.Replay(run, 99, replay); !errors.Is(err, layout.ErrUnknownTurn) {
		t.Fatalf("Replay(99): err = %v, quer ErrUnknownTurn", err)
	}
}

// --- TOCTOU: check-and-advance atómico sob turnos concorrentes do mesmo run --
//
// Fecha a janela entre observar o cursor e avançá-lo (AOS-060-Q1). Dois turnos do
// MESMO run entram CONCORRENTEMENTE, ambos estendem byte-a-byte o baseline do turno
// 1 mas DIVERGEM no segmento acrescentado. Com o check-and-advance atómico, cada um
// é validado contra o cursor IMEDIATAMENTE anterior (não contra um cursor obsoleto),
// pelo que exactamente um é admitido e tails divergentes NUNCA coexistem no manifesto.
// -race fica limpo (o mutex do ledger protege o mapa); este teste prova a invariante
// LÓGICA, não a ausência de data race.
func TestGuard_ConcurrentTurnsSameRun_DivergentTailsCannotCoexist(t *testing.T) {
	t.Parallel()
	a := agentruntime.NewPromptAssembler("SYS", toolsAB())
	g := layout.NewGuard(nil)
	const run = "run-concurrent"

	base := []agentruntime.TailSegment{seg(agentruntime.TailObjective, "AAAAAAAAAA")}
	if err := g.Admit(mkTurn(run, 1, "ts#1", a, base)); err != nil {
		t.Fatalf("turno 1: %v", err)
	}

	// Ambos estendem o baseline do turno 1 (segmento "AAAAAAAAAA" byte-idêntico) mas
	// divergem no resultado acrescentado (B... vs X..., de comprimentos diferentes).
	t2 := mkTurn(run, 2, "ts#1", a, []agentruntime.TailSegment{
		seg(agentruntime.TailObjective, "AAAAAAAAAA"),
		seg(agentruntime.TailToolResult, "BBBBB"),
	})
	t3 := mkTurn(run, 3, "ts#1", a, []agentruntime.TailSegment{
		seg(agentruntime.TailObjective, "AAAAAAAAAA"),
		seg(agentruntime.TailToolResult, "XXXXXXXXXXXXXXX"),
	})

	var wg sync.WaitGroup
	var err2, err3 error
	wg.Add(2)
	go func() { defer wg.Done(); err2 = g.Admit(t2) }()
	go func() { defer wg.Done(); err3 = g.Admit(t3) }()
	wg.Wait()

	success := 0
	if err2 == nil {
		success++
	}
	if err3 == nil {
		success++
	}
	if success != 1 {
		t.Fatalf("esperado exactamente 1 turno admitido, obtive %d (err2=%v err3=%v)", success, err2, err3)
	}
	// O rejeitado é uma violação de tail append-only (rewritten se o mais curto venceu,
	// shrunk se o mais longo venceu) — nunca um silêncio.
	loserErr := err2
	if err2 == nil {
		loserErr = err3
	}
	if !errors.Is(loserErr, layout.ErrTailRewritten) && !errors.Is(loserErr, layout.ErrTailShrunk) {
		t.Fatalf("turno rejeitado: err = %v, quer tail_rewritten ou tail_shrunk", loserErr)
	}
	// Tails divergentes NÃO coexistem: exactamente um dos turnos ficou no manifesto.
	_, has2 := g.PromptHash(run, 2)
	_, has3 := g.PromptHash(run, 3)
	if has2 == has3 {
		t.Fatalf("tails divergentes coexistem/ausentes no manifesto (has2=%v has3=%v)", has2, has3)
	}
}

// --- Conflito de manifesto sinalizado como variância (AOS-060-Q2) -----------
//
// Regravar um turno já gravado com um hash materializado DIVERGENTE (replay infiel,
// ADR-010) é REJEITADO E SINALIZADO ao sink como as demais violações — não devolvido
// como erro cru sem Kind nem testemunhas.
func TestGuard_ManifestConflictSignalledAsVariance(t *testing.T) {
	t.Parallel()
	a := agentruntime.NewPromptAssembler("SYS", toolsAB())
	sink := &capturingSink{}
	g := layout.NewGuard(nil, layout.WithSink(sink))
	const run = "run-manifest-conflict"

	tail1 := []agentruntime.TailSegment{seg(agentruntime.TailObjective, "obj")}
	tail2 := []agentruntime.TailSegment{seg(agentruntime.TailObjective, "obj"), seg(agentruntime.TailToolResult, "res")}
	// tail2b ESTENDE tail2 byte-a-byte (passa append-only) mas muda o hash materializado.
	tail2b := []agentruntime.TailSegment{
		seg(agentruntime.TailObjective, "obj"),
		seg(agentruntime.TailToolResult, "res"),
		seg(agentruntime.TailHistory, "hist"),
	}

	if err := g.Admit(mkTurn(run, 1, "ts#1", a, tail1)); err != nil {
		t.Fatalf("turno 1: %v", err)
	}
	if err := g.Admit(mkTurn(run, 2, "ts#1", a, tail2)); err != nil {
		t.Fatalf("turno 2: %v", err)
	}

	err := g.Admit(mkTurn(run, 2, "ts#1", a, tail2b)) // regrava o índice 2 com hash divergente
	if !errors.Is(err, layout.ErrManifestConflictSignal) {
		t.Fatalf("regravar turno 2 divergente: err = %v, quer ErrManifestConflictSignal", err)
	}
	if len(sink.got) != 1 || sink.got[0].Kind != layout.KindManifestConflict {
		t.Fatalf("sink = %+v, quer 1 sinal manifest_conflict", sink.got)
	}
	// Testemunhas Want/Got: hash gravado vs. novo (diagnóstico preservado).
	if sink.got[0].Want == "" || sink.got[0].Got == "" || sink.got[0].Want == sink.got[0].Got {
		t.Errorf("testemunhas Want/Got do conflito ausentes ou iguais: %+v", sink.got[0])
	}
	// O manifesto do turno 2 mantém-se IMUTÁVEL (o hash original, não sobreposto).
	h2, ok := g.PromptHash(run, 2)
	if !ok {
		t.Fatalf("turno 2 devia continuar gravado")
	}
	orig := mkTurn(run, 2, "ts#1", a, tail2)
	if h2 != orig.View.PromptHash {
		t.Errorf("manifesto do turno 2 mutou apos conflito: %q != %q", h2, orig.View.PromptHash)
	}
}

// --- Ledger: idempotência do manifesto e conflito --------------------------

func TestMemoryLedger_ManifestImmutablePerTurn(t *testing.T) {
	t.Parallel()
	l := layout.NewMemoryLedger()
	rec, created, err := l.Pin("run-x", "sha256:pfx", "ts#1")
	if err != nil || !created {
		t.Fatalf("Pin: created=%v err=%v", created, err)
	}
	if rec.RunID != "run-x" {
		t.Fatalf("rec.RunID = %q", rec.RunID)
	}
	// Segundo Pin é idempotente (created=false), devolve o registo existente.
	if _, created2, _ := l.Pin("run-x", "sha256:OUTRO", "ts#OUTRO"); created2 {
		t.Fatalf("segundo Pin devia ser idempotente (created=false)")
	}

	if err := l.Advance("run-x", 1, 10, "sha256:tail1", "sha256:mat1"); err != nil {
		t.Fatalf("Advance(1): %v", err)
	}
	// Reaplicar o MESMO turno com o MESMO hash é no-op idempotente.
	if err := l.Advance("run-x", 1, 10, "sha256:tail1", "sha256:mat1"); err != nil {
		t.Fatalf("Advance(1) idempotente: %v", err)
	}
	// Reaplicar o MESMO turno com hash DIVERGENTE é conflito (manifesto imutável).
	if err := l.Advance("run-x", 1, 12, "sha256:tail1b", "sha256:matDIFERENTE"); !errors.Is(err, layout.ErrManifestConflict) {
		t.Fatalf("Advance conflito: err = %v, quer ErrManifestConflict", err)
	}

	// Advance sobre run não pinado ⇒ fail-closed.
	if err := l.Advance("run-inexistente", 1, 1, "h", "h"); !errors.Is(err, layout.ErrRunNotPinned) {
		t.Fatalf("Advance run inexistente: err = %v, quer ErrRunNotPinned", err)
	}
	// Pin com runID vazio ⇒ fail-closed.
	if _, _, err := l.Pin("", "p", "t"); !errors.Is(err, layout.ErrNoRunID) {
		t.Fatalf("Pin vazio: err = %v, quer ErrNoRunID", err)
	}
}
