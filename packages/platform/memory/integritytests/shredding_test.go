package integritytests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/episodic"
	"github.com/aos-ref/substrate/eventstore"
)

// newEpisodicStore constrói a memória episódica sobre um ES real, KeyStore in-memory
// e a hash-chain de audit, com cripto/relógio deterministas. Devolve também o
// KeyStore (para o crypto-shredding) e a chain (para verificação independente).
func newEpisodicStore(t *testing.T, es *eventstore.Store) (*episodic.TrajectoryStore, *episodic.InMemoryKeyStore, *audit.MemStore) {
	t.Helper()
	keys := episodic.NewInMemoryKeyStore((&seqRand{}).fill)
	chain := audit.NewMemStore()
	s, err := episodic.NewTrajectoryStore(es, keys, chain,
		episodic.WithClock(fixedClock()),
		episodic.WithRandSource((&seqRand{n: 1000}).fill),
	)
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	return s, keys, chain
}

// episodeInput constrói um EpisodeInput golden determinístico.
func episodeInput(episodeID, subject, run, goal string, ttl domain.TTLClass, nTurns int) episodic.EpisodeInput {
	return episodic.EpisodeInput{
		EpisodeID: episodeID,
		SubjectID: subject,
		AgentID:   "agent-1",
		RunID:     run,
		Goal:      goal,
		Tags:      []string{"tag-a"},
		Outcome:   "success",
		TTLClass:  ttl,
		Record:    buildTrajectory("trace-"+run, nTurns),
		CreatedAt: fixedTime,
	}
}

// TestShreddingIrrecoverableChainIntact — apagar a chave do titular torna o episódio
// IRRECUPERÁVEL (o conteúdo cifrado deixa de abrir), mas a hash-chain de audit — que
// sela o HASH do ciphertext, não o plaintext — CONTINUA a verificar (ADR-011). A
// recuperação nunca devolve o conteúdo cru.
func TestShreddingIrrecoverableChainIntact(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	store, keys, _ := newEpisodicStore(t, es)

	if err := store.Enqueue(episodeInput("ep-1", "subject-A", "run-1", "do-x", domain.TTLStandard, 3)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// ANTES do shredding: recuperável (projecção, não o cru) e cadeia íntegra.
	before, err := store.Recall(ctx, episodic.Query{PrincipalID: "agent-1", Goal: "do-x"})
	if err != nil {
		t.Fatalf("Recall (antes): %v", err)
	}
	if len(before) != 1 || !before[0].Recoverable {
		t.Fatalf("episódio devia ser recuperável antes do shredding: %+v", before)
	}
	if strings.Contains(before[0].Projection.Summary, rawSecretPrefix) {
		t.Fatal("a projecção recuperada vazou o conteúdo cru")
	}
	if err := store.VerifyChain(ctx); err != nil {
		t.Fatalf("cadeia devia verificar antes do shredding: %v", err)
	}

	// CRYPTO-SHREDDING: apaga a chave do titular.
	keys.DeleteKey("subject-A")

	// DEPOIS: irrecuperável, mas a cadeia CONTINUA a verificar (não foi mutada).
	if _, perr := store.Project(ctx, "agent-1", "ep-1"); !errors.Is(perr, episodic.ErrEpisodeShredded) {
		t.Fatalf("Project após shredding devolveu %v, quero ErrEpisodeShredded", perr)
	}
	after, err := store.Recall(ctx, episodic.Query{PrincipalID: "agent-1", Goal: "do-x"})
	if err != nil {
		t.Fatalf("Recall (depois): %v", err)
	}
	if len(after) != 1 || after[0].Recoverable {
		t.Fatalf("episódio devia ser IRRECUPERÁVEL após shredding: %+v", after)
	}
	if err := store.VerifyChain(ctx); err != nil {
		t.Fatalf("hash-chain partida pelo crypto-shredding (nunca deve acontecer): %v", err)
	}
}

// TestTTLSweepShredsExpired — o Sweep aplica o TTL por classe via crypto-shredding: um
// episódio expirado torna-se irrecuperável (chave do titular apagada) SEM partir a
// cadeia; um episódio permanente (outro titular) NÃO é varrido.
func TestTTLSweepShredsExpired(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	store, keys, _ := newEpisodicStore(t, es)

	// Titular efémero (TTL 1h) e titular permanente (sem expiração), titulares distintos.
	if err := store.Enqueue(episodeInput("ep-eph", "subj-eph", "run-eph", "goal-eph", domain.TTLEphemeral, 2)); err != nil {
		t.Fatalf("Enqueue(eph): %v", err)
	}
	if err := store.Enqueue(episodeInput("ep-perm", "subj-perm", "run-perm", "goal-perm", domain.TTLPermanent, 2)); err != nil {
		t.Fatalf("Enqueue(perm): %v", err)
	}
	if _, err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Sweep num instante muito depois da criação: o efémero (TTL 1h) expirou.
	now := fixedTime.Add(2 * time.Hour)
	swept, err := store.Sweep(ctx, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(swept) != 1 || swept[0].EpisodeID != "ep-eph" {
		t.Fatalf("swept = %+v, quero só ep-eph", swept)
	}

	// O efémero: chave apagada, irrecuperável.
	if _, ok := keys.Key("subj-eph"); ok {
		t.Fatal("a chave do titular efémero devia ter sido apagada pelo Sweep")
	}
	if _, perr := store.Project(ctx, "agent-1", "ep-eph"); !errors.Is(perr, episodic.ErrEpisodeShredded) {
		t.Fatalf("Project(ep-eph) = %v, quero ErrEpisodeShredded", perr)
	}
	// O permanente: intacto (a chave sobrevive; continua recuperável).
	if _, ok := keys.Key("subj-perm"); !ok {
		t.Fatal("a chave do titular permanente NÃO devia ser apagada")
	}
	if _, perr := store.Project(ctx, "agent-1", "ep-perm"); perr != nil {
		t.Fatalf("Project(ep-perm) devia ser recuperável, deu %v", perr)
	}
	// A cadeia continua íntegra após o TTL-shredding.
	if err := store.VerifyChain(ctx); err != nil {
		t.Fatalf("hash-chain partida pelo TTL-shredding: %v", err)
	}
}
