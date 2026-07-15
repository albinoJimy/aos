package compaction_test

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"

	"github.com/aos-ref/platform/model-gateway/cache/compaction"
)

func tail(s string) []agentruntime.TailSegment {
	return []agentruntime.TailSegment{
		{Kind: agentruntime.TailToolResult, Content: []byte(s)},
		{Kind: agentruntime.TailHistory, Content: []byte(s + "-hist")},
	}
}

// TestCompactor_NotInvokedOnHotPath é a prova ESTRUTURAL off-hot-path: correr N
// turnos de MONTAGEM (o assembler cache-estável) nunca invoca o compactor — a hot
// path não tem sequer uma referência a ele. O contador fica a 0.
func TestCompactor_NotInvokedOnHotPath(t *testing.T) {
	t.Parallel()
	c := compaction.New(nil)
	a := agentruntime.NewPromptAssembler("SYS", []agentruntime.ToolSpec{{Name: "t", Version: "1.0.0", Digest: "sha256:d"}})

	// Simula a hot path: 100 turnos de montagem, tail a crescer.
	var segs []agentruntime.TailSegment
	for i := 1; i <= 100; i++ {
		segs = append(segs, agentruntime.TailSegment{Kind: agentruntime.TailToolResult, Content: []byte("r")})
		_ = a.Assemble(i, segs)
	}
	if c.Runs() != 0 {
		t.Fatalf("compactor invocado na hot path: Runs = %d, quer 0", c.Runs())
	}
}

// TestCompactor_CheckpointProducesTailSummary prova que a compactação corre SÓ no
// checkpoint (contador incrementa), produz um segmento de TAIL (nunca prefixo) e
// que a invariância do prefixo é estrutural.
func TestCompactor_CheckpointProducesTailSummary(t *testing.T) {
	t.Parallel()
	c := compaction.New(nil)
	res, err := c.Compact(context.Background(), compaction.Source{
		RunID:        "run-1",
		CheckpointID: "ckpt-1",
		Prior:        tail("conteudo"),
		PrefixHash:   "sha256:pfx",
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if c.Runs() != 1 {
		t.Fatalf("Runs = %d, quer 1", c.Runs())
	}
	// O resultado é um segmento de TAIL (Kind "summary"), não um prefixo.
	if res.Summary.Kind != "summary" || len(res.Summary.Content) == 0 {
		t.Fatalf("Summary = %+v, quer segmento de tail nao-vazio", res.Summary)
	}
	// Invariância do prefixo: estrutural (before == after == origem).
	if !res.PrefixInvariant() {
		t.Errorf("PrefixInvariant falso: %s != %s", res.PrefixHashBefore, res.PrefixHashAfter)
	}
	if res.PrefixHashBefore != "sha256:pfx" {
		t.Errorf("PrefixHashBefore = %s, quer sha256:pfx", res.PrefixHashBefore)
	}
	if res.Digest == "" {
		t.Errorf("Digest vazio")
	}
}

// TestCompactor_SummaryEntersFutureTailNotPrefix prova que o sumário entra no TAIL
// de um turno FUTURO sem alterar o prefixo do run corrente (o prefixo mantém-se
// byte-idêntico com e sem o sumário no tail).
func TestCompactor_SummaryEntersFutureTailNotPrefix(t *testing.T) {
	t.Parallel()
	c := compaction.New(nil)
	a := agentruntime.NewPromptAssembler("SYS", []agentruntime.ToolSpec{{Name: "t", Version: "1.0.0", Digest: "sha256:d"}})

	base := []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("obj")}}
	before := a.Assemble(1, base)

	res, err := c.Compact(context.Background(), compaction.Source{RunID: "run-1", Prior: base, PrefixHash: before.PrefixHash})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Turno FUTURO: o sumário entra no tail append-only.
	future := a.Assemble(2, append(append([]agentruntime.TailSegment{}, base...), res.Summary))

	if before.PrefixHash != future.PrefixHash {
		t.Errorf("o prefixo mudou por causa da compactacao: %s != %s", before.PrefixHash, future.PrefixHash)
	}
	if string(before.Prefix) != string(future.Prefix) {
		t.Errorf("prefixo NAO byte-identico apos injeccao do sumario no tail")
	}
}

// TestCompactor_Deterministic prova que a sumarização é função pura (mesma origem ⇒
// mesmo sumário e mesmo Digest), preservando o replay fiel.
func TestCompactor_Deterministic(t *testing.T) {
	t.Parallel()
	c1 := compaction.New(nil)
	c2 := compaction.New(nil)
	src := compaction.Source{RunID: "run-1", Prior: tail("x"), PrefixHash: "sha256:p"}
	r1, err1 := c1.Compact(context.Background(), src)
	r2, err2 := c2.Compact(context.Background(), src)
	if err1 != nil || err2 != nil {
		t.Fatalf("Compact: %v %v", err1, err2)
	}
	if r1.Digest != r2.Digest {
		t.Errorf("Digest nao determinista: %s != %s", r1.Digest, r2.Digest)
	}
	if string(r1.Summary.Content) != string(r2.Summary.Content) {
		t.Errorf("sumario nao determinista")
	}
}

// TestCompactor_FailClosed cobre a recusa por origem inválida (sem incrementar o
// contador — nada corre).
func TestCompactor_FailClosed(t *testing.T) {
	t.Parallel()
	c := compaction.New(nil)
	if _, err := c.Compact(context.Background(), compaction.Source{RunID: ""}); !errors.Is(err, compaction.ErrNoSource) {
		t.Fatalf("err = %v, quer ErrNoSource", err)
	}
	if c.Runs() != 0 {
		t.Errorf("Runs = %d apos falha, quer 0", c.Runs())
	}
}

// TestCompactor_CustomSummarizer prova que a política de sumarização é injectável.
func TestCompactor_CustomSummarizer(t *testing.T) {
	t.Parallel()
	c := compaction.New(func(prior []agentruntime.TailSegment) []byte { return []byte("FIXO") })
	res, err := c.Compact(context.Background(), compaction.Source{RunID: "r", Prior: tail("y")})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if string(res.Summary.Content) != "FIXO" {
		t.Errorf("summarizer custom ignorado: %q", res.Summary.Content)
	}
}
