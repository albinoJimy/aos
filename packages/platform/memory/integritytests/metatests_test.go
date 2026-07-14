package integritytests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/memory/working"
)

// Os meta-testes são a PROVA de que a suite NÃO é green-vazio: cada um INJECTA uma
// violação de uma invariante e assere que o verificador correspondente a DETECTA
// (devolve o erro esperado). Se um detector deixasse de apanhar a violação (regressão
// para green-vazio), o meta-teste fica VERMELHO — o gate bloqueia.

// TestMetaSuiteDetectsRecordErasure — injecta um sink que NÃO preserva (droppingSink):
// a eviction procede mas o registo desaparece do backend; o verificador de integridade
// tem de o apanhar.
func TestMetaSuiteDetectsRecordErasure(t *testing.T) {
	ctx := context.Background()
	port := newInMemoryPort() // backend vazio (o dropping sink nunca lá escreve)
	wm := newWindow(t, "run-meta-erase", droppingSink{})
	for i := 1; i <= 5; i++ {
		wm.Append(working.TailInput{
			Kind:     working.TailMemory,
			Content:  "segment-content-value-" + itoa(i),
			Priority: i,
			RecordID: "rec-" + itoa(i),
		})
	}
	evicted, err := wm.EvictToTailBudget(ctx, 12)
	if err != nil {
		t.Fatalf("EvictToTailBudget: %v", err)
	}
	if err := verifyEvictedRecordsPreserved(ctx, port, evicted); !errors.Is(err, errRecordErased) {
		t.Fatalf("a suite NÃO detectou a erasure do registo: %v", err)
	}
}

// TestMetaSuiteDetectsQuarantineBreach — injecta um TaintController que classifica
// tool_result como TRUSTED (quarentena furada); o verificador de proveniência tem de
// apanhar que untrusted chegou ao control-plane.
func TestMetaSuiteDetectsQuarantineBreach(t *testing.T) {
	ctx := context.Background()
	if err := verifyToolResultQuarantined(ctx, alwaysTrustedTC{}); !errors.Is(err, errQuarantineBreached) {
		t.Fatalf("a suite NÃO detectou a quarentena furada: %v", err)
	}
}

// TestMetaSuiteDetectsLossyMigration — injecta uma migração COM PERDA (Up descarta o
// conteúdo); o verificador de migração tem de a recusar (round-trip devolve erro).
func TestMetaSuiteDetectsLossyMigration(t *testing.T) {
	lossy := makeLossyMigration("m-lossy", "1.0.0", "1.1.0")
	// Assere o SENTINELA concreto (não apenas err != nil): a migração com perda tem de
	// ser recusada pelo backstop de reversibilidade (Down(Up(x)) != x), alinhando com os
	// restantes meta-testes que provam QUAL invariante foi violada, não só que houve erro.
	if err := verifyMigrationRoundTrip(initialRecords(), lossy); !errors.Is(err, migrations.ErrIrreversibleMigration) {
		t.Fatalf("a suite NÃO detectou a migração com perda como irreversível: %v", err)
	}
}

// TestMetaSuiteDetectsBrokenHashChain — injecta um audit.Store que adultera um registo
// na leitura; o verificador de cadeia tem de o apanhar (audit.Verify falha).
func TestMetaSuiteDetectsBrokenHashChain(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	store, _, chain := newEpisodicStore(t, es)
	if err := store.Enqueue(episodeInput("ep-chain", "subj-chain", "run-chain", "goal-chain", domain.TTLStandard, 2)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// Controlo: a cadeia REAL verifica.
	if err := verifyChainIntact(ctx, chain, store.Partition()); err != nil {
		t.Fatalf("a cadeia real devia verificar: %v", err)
	}
	// Injecção: um store adulterado é DETECTADO.
	tampered := tamperingStore{Store: chain}
	if err := verifyChainIntact(ctx, tampered, store.Partition()); !errors.Is(err, errChainBroken) {
		t.Fatalf("a suite NÃO detectou a hash-chain partida: %v", err)
	}
}

// TestMetaSuiteDetectsMutatedPrefix — injecta um prefixo divergente entre a origem e a
// compactação (cache thrash); a compactação aborta e o verificador de estabilidade de
// cache tem de o apanhar.
func TestMetaSuiteDetectsMutatedPrefix(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	comp, err := compression.NewAsyncCompactor(es)
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	src := compression.CompactionSource{
		RunID:        "run-meta-prefix",
		CheckpointID: "cp-1",
		TraceID:      "trace-mp",
		AgentID:      "agent-1",
		PrefixHash:   "sha256:original-prefix",
		Turns:        turnsOf("trace-mp", 3),
	}
	res, cerr := comp.Compact(ctx, src, compression.DefaultCompressionPolicy(), "sha256:MUTATED-prefix")
	if !errors.Is(cerr, compression.ErrPrefixMutated) {
		t.Fatalf("Compact com prefixo divergente devolveu %v, quero ErrPrefixMutated", cerr)
	}
	if err := verifyCompressionPrefixStable(res); !errors.Is(err, errPrefixMutated) {
		t.Fatalf("a suite NÃO detectou o prefixo mutado: %v", err)
	}
}

// TestMetaSuiteDetectsRawLeak — injecta uma compactação cujo SUMÁRIO vaza o conteúdo
// cru (rawSecretPrefix) na via de projecção/compressão; o verificador de integridade
// tem de o apanhar (higiene, Princípio 4). Os campos de REGISTO ficam válidos
// (FullRecordSpans > turnos do sumário, TotalTurns > 0) para isolar o ramo da fuga: uma
// regressão que remova/inverta a verificação strings.Contains(Summary, rawSecretPrefix)
// passaria verde no happy-path (as fixtures limpas nunca vazam) — este meta-teste é o
// único que a apanharia.
func TestMetaSuiteDetectsRawLeak(t *testing.T) {
	leaked := compression.CompactionResult{
		FullRecordSpans: 5, // registo completo (> turnos do sumário): passa o registo
		TotalTurns:      5,
		Summary: projection.InjectedView{
			IncludedTurns: 2,
			TotalTurns:    5,
			Summary:       "resumo que VAZA " + rawSecretPrefix + "trace-leak-turn-1",
		},
	}
	if err := verifyCompressionPreservesRecord(leaked); !errors.Is(err, errRawLeaked) {
		t.Fatalf("a suite NÃO detectou a fuga de conteúdo cru para o sumário: %v", err)
	}
}

// TestMetaSuiteDetectsRegisterIncomplete — injecta uma via de registo que DESCARTA
// turnos (FullRecordSpans <= turnos do sumário): o registo deixou de ser estritamente
// mais completo do que o contexto injectado. O verificador tem de o apanhar. O sumário
// fica higienizado (sem cru) para isolar o ramo do registo incompleto: uma regressão que
// remova/inverta a verificação FullRecordSpans <= IncludedTurns passaria verde no
// happy-path — este meta-teste é o único que a apanharia.
func TestMetaSuiteDetectsRegisterIncomplete(t *testing.T) {
	incomplete := compression.CompactionResult{
		FullRecordSpans: 2, // registo DESCARTOU: <= turnos do sumário (violação)
		TotalTurns:      5,
		Summary: projection.InjectedView{
			IncludedTurns: 2,
			TotalTurns:    5,
			Summary:       "resumo higienizado sem conteúdo cru",
		},
	}
	if err := verifyCompressionPreservesRecord(incomplete); !errors.Is(err, errRegisterIncomplete) {
		t.Fatalf("a suite NÃO detectou o registo incompleto (turnos descartados): %v", err)
	}
}

// boolStr formata um booleano para o relatório JSON compacto do gate.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestSuiteReportEmitted emite o RELATÓRIO da suite (linha marcada AOS_MEMORY_REPORT),
// que o gate CI (scripts/ci/memory.sh) captura e sobre o qual falha-fecha
// (pass agregado != true). Corre os verificadores sobre fixtures limpas (devem passar)
// E confirma que os detectores apanham violações injectadas (metatests) — o veredicto
// agregado só é true se AMBOS se verificarem.
func TestSuiteReportEmitted(t *testing.T) {
	ctx := context.Background()
	checks := []struct {
		name string
		ok   bool
	}{
		{"integrity_projection", verifyProjectionPreservesRecord(ctx, buildTrajectory("trace-report", 4), tinyBudgetPolicy()) == nil},
		{"migration_roundtrip", verifyMigrationRoundTrip(initialRecords(), makeReversibleMigration("m-report", "1.0.0", "1.1.0")) == nil},
		{"provenance_quarantine", verifyToolResultQuarantined(ctx, provenance.DefaultTaintController{}) == nil},
		// Detecção (meta): uma migração com perda TEM de ser recusada (erro != nil).
		{"metatests_detect", verifyMigrationRoundTrip(initialRecords(), makeLossyMigration("m-report-lossy", "1.0.0", "1.1.0")) != nil},
	}

	pass := true
	var b strings.Builder
	b.WriteString("AOS_MEMORY_REPORT {")
	for i, c := range checks {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\"" + c.name + "\":" + boolStr(c.ok))
		if !c.ok {
			pass = false
		}
	}
	b.WriteString(",\"pass\":" + boolStr(pass) + "}")

	if !pass {
		t.Fatalf("relatório da suite indica falha: %s", b.String())
	}
	// Stdout para o gate CI capturar por grep (à imagem do AOS_REPLAY_REPORT de AOS-024).
	fmt.Println(b.String())
}
