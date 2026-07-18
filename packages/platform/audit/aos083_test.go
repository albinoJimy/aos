package audit

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Este ficheiro cobre AOS-083 — o PIPELINE assemblado por cima da base de AOS-011/
// 072: (a) redação de PII na ingestão (cifrar sob chave por-titular + PayloadRef +
// tokenizar), (b) KeyVault por-titular + Shredder.Shred, (c) retenção + legal hold
// (legal hold impede o shred fail-closed), (d) Ingest→redigir→Append→Seal.

// detRand devolve uma RandSource determinística (contador) para reprodutibilidade
// dos testes — NUNCA usada em produção (que usa crypto/rand).
func detRand() RandSource {
	var n byte
	return func(p []byte) error {
		for i := range p {
			p[i] = n
			n++
		}
		return nil
	}
}

// newPipeline monta um pipeline in-memory determinístico e devolve as suas peças.
func newPipeline(t *testing.T) (*IngestPipeline, *MemStore, *InMemoryKeyVault, *InMemoryPayloadStore) {
	t.Helper()
	store := NewMemStore()
	vault := NewInMemoryKeyVault(detRand())
	payloads := NewInMemoryPayloadStore()
	p := NewIngestPipeline(store, vault, payloads, WithIngestRand(detRand()))
	return p, store, vault, payloads
}

// ---------------------------------------------------------------------------
// Unit — encadeamento de hash + deteção de adulteração de um registo INTERMÉDIO
// através do pipeline de ingestão (o caminho de produção real).
// ---------------------------------------------------------------------------

// TestIngestChainsAndDetectsMiddleTamper — registos ingeridos formam uma cadeia
// gapless verificável; mutar um registo INTERMÉDIO (seq=3 de 5) é detectado como
// mutação e identificado pelo verificador.
func TestIngestChainsAndDetectsMiddleTamper(t *testing.T) {
	ctx := context.Background()
	p, store, _, _ := newPipeline(t)

	for i := 0; i < 5; i++ {
		if _, err := p.Ingest(ctx, RawRecord{Record: sampleRecord("p", DecisionAllow)}); err != nil {
			t.Fatalf("Ingest #%d: %v", i, err)
		}
	}
	if err := Verify(ctx, store, "p", 1, 5); err != nil {
		t.Fatalf("cadeia ingerida devia verificar: %v", err)
	}

	// Adulterar o registo intermédio seq=3 (mutação ingénua, sem recalcular o hash).
	store.parts["p"][2].Capability = "fs:write:/etc/shadow"

	err := Verify(ctx, store, "p", 1, 5)
	assertTamper(t, err, TamperMutation, 3)
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("esperado ErrTampered, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// Redação na ingestão — a PII em claro NUNCA entra na cadeia (só ref/ciphertext).
// ---------------------------------------------------------------------------

// TestIngestRedactsPIINeverInChain — ao ingerir um registo com PII, o claro é
// cifrado sob a chave do titular; a cadeia sela apenas o PayloadRef (ContentHash+
// KeyRef+SubjectID). O plaintext não aparece em NENHUM ponto do conteúdo canónico
// selado, e a KeyRef é a derivada do titular.
func TestIngestRedactsPIINeverInChain(t *testing.T) {
	ctx := context.Background()
	p, store, _, payloads := newPipeline(t)

	pii := []byte("Alice Silva, NIF 123456789, alice@example.com")
	subject := "subject-alice"
	sealed, err := p.Ingest(ctx, RawRecord{
		Record:    sampleRecord("run-pii", DecisionAllow),
		SubjectID: subject,
		PII:       pii,
	})
	if err != nil {
		t.Fatalf("Ingest com PII: %v", err)
	}

	// A referência selada existe e é bem-formada.
	if sealed.PayloadRef == nil {
		t.Fatal("PayloadRef devia ter sido preenchido na ingestão")
	}
	if sealed.PayloadRef.SubjectID != subject {
		t.Fatalf("SubjectID=%q, esperado %q", sealed.PayloadRef.SubjectID, subject)
	}
	if sealed.PayloadRef.KeyRef != KeyRefFor(subject) {
		t.Fatalf("KeyRef=%q, esperado %q", sealed.PayloadRef.KeyRef, KeyRefFor(subject))
	}
	if len(sealed.PayloadRef.ContentHash) != 32 {
		t.Fatalf("ContentHash deve ser SHA-256 (32 bytes), tem %d", len(sealed.PayloadRef.ContentHash))
	}

	// O plaintext NUNCA entra na cadeia: nem no registo lido do store, nem no seu
	// conteúdo canónico (o que é hasheado).
	got, ok, _ := store.At(ctx, "run-pii", 1)
	if !ok {
		t.Fatal("registo ingerido não encontrado")
	}
	if bytesContains(canonicalContent(got), pii) {
		t.Fatal("PII em claro NÃO deve aparecer no conteúdo canónico selado")
	}
	// Também não deve aparecer no ContentHash selado (é hash do CIPHERTEXT).
	if bytesContains(got.PayloadRef.ContentHash, pii) {
		t.Fatal("PII em claro não deve aparecer no ContentHash")
	}

	// A PII é recuperável ANTES do shred (o ciphertext está no PayloadStore e a
	// chave existe): prova que a redação é reversível enquanto a chave viver.
	recovered, err := p.Recover(*sealed.PayloadRef)
	if err != nil {
		t.Fatalf("Recover pré-shred: %v", err)
	}
	if !bytes.Equal(recovered, pii) {
		t.Fatalf("PII recuperada=%q, esperado %q", recovered, pii)
	}

	// O ciphertext guardado fora da cadeia não contém o plaintext.
	blob, ok := payloads.Get(hexOf(sealed.PayloadRef.ContentHash))
	if !ok || bytesContains(blob, pii) {
		t.Fatal("o ciphertext no PayloadStore não deve conter o plaintext")
	}
}

// TestIngestPIIRequiresSubject — ingerir PII sem SubjectID é fail-closed: sem
// titular não há chave por-titular a que cifrar/apagar.
func TestIngestPIIRequiresSubject(t *testing.T) {
	ctx := context.Background()
	p, _, _, _ := newPipeline(t)

	_, err := p.Ingest(ctx, RawRecord{
		Record: sampleRecord("p", DecisionAllow),
		PII:    []byte("dados"),
	})
	if !errors.Is(err, ErrNoSubject) {
		t.Fatalf("esperado ErrNoSubject, veio %v", err)
	}
}

// TestIngestDropsProducerPayloadRef — um PayloadRef pré-fabricado pelo produtor é
// descartado: a referência é SEMPRE derivada da cifra desta etapa (ou ausente se
// não há PII), impedindo a injeção de uma âncora forjada.
func TestIngestDropsProducerPayloadRef(t *testing.T) {
	ctx := context.Background()
	p, _, _, _ := newPipeline(t)

	rec := sampleRecord("p", DecisionAllow)
	rec.PayloadRef = &PayloadRef{ContentHash: []byte("forjado"), KeyRef: "kms:forjado", SubjectID: "x"}
	sealed, err := p.Ingest(ctx, RawRecord{Record: rec}) // sem PII
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if sealed.PayloadRef != nil {
		t.Fatal("PayloadRef fornecido pelo produtor (sem PII) devia ter sido descartado")
	}
}

// ---------------------------------------------------------------------------
// Integração — crypto-shredding por titular remove o payload (PII irrecuperável)
// MAS mantém a cadeia verificável (Verify* passa após o Shred).
// ---------------------------------------------------------------------------

// TestCryptoShreddingRemovesPIIKeepsChain — o cenário GDPR Art. 17 ponta-a-ponta
// pelo pipeline: ingere PII no meio da cadeia, sela um checkpoint, executa o shred
// por titular e prova que (1) a PII é irrecuperável e (2) a cadeia E o checkpoint
// continuam a verificar.
func TestCryptoShreddingRemovesPIIKeepsChain(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	vault := NewInMemoryKeyVault(detRand())
	payloads := NewInMemoryPayloadStore()
	signer := newSigner(t)
	p := NewIngestPipeline(store, vault, payloads, WithIngestRand(detRand()), WithIngestSigner(signer))

	// Registo normal, depois o pessoal (seq=2), depois outro normal — o registo com
	// PII fica no MEIO da cadeia.
	if _, err := p.Ingest(ctx, RawRecord{Record: sampleRecord("run-cs", DecisionAllow)}); err != nil {
		t.Fatalf("Ingest #1: %v", err)
	}
	pii := []byte("dados pessoais do titular")
	subject := "subject-123"
	personal, err := p.Ingest(ctx, RawRecord{
		Record: sampleRecord("run-cs", DecisionAllow), SubjectID: subject, PII: pii,
	})
	if err != nil {
		t.Fatalf("Ingest pessoal: %v", err)
	}
	if _, err := p.Ingest(ctx, RawRecord{Record: sampleRecord("run-cs", DecisionAllow)}); err != nil {
		t.Fatalf("Ingest #3: %v", err)
	}

	// Selo periódico assinado no head (parte do pipeline).
	cp, err := p.Seal(ctx, "run-cs", 3)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Pré-shred: cadeia verifica, checkpoint verifica, PII recuperável.
	if err := Verify(ctx, store, "run-cs", 1, 3); err != nil {
		t.Fatalf("cadeia devia verificar pré-shred: %v", err)
	}
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 3); err != nil {
		t.Fatalf("checkpoint devia verificar pré-shred: %v", err)
	}
	if _, err := p.Recover(*personal.PayloadRef); err != nil {
		t.Fatalf("PII devia ser recuperável pré-shred: %v", err)
	}

	// CRYPTO-SHREDDING (DSAR): destruir a chave do titular.
	shredder := NewShredder(vault, NewLegalHold(), NewRetentionPolicy(nil))
	if err := shredder.Shred(subject); err != nil {
		t.Fatalf("Shred: %v", err)
	}

	// A PII é agora IRRECUPERÁVEL.
	if _, err := p.Recover(*personal.PayloadRef); !errors.Is(err, ErrShredded) {
		t.Fatalf("PII devia ser irrecuperável após shred (ErrShredded), veio %v", err)
	}
	if _, ok := vault.Key(personal.PayloadRef.KeyRef); ok {
		t.Fatal("a chave devia ter sido destruída no vault")
	}

	// A cadeia mantém-se ÍNTEGRA: o EntryHash selou o ContentHash do ciphertext, não
	// o plaintext — a destruição da chave não muda nenhum registo selado.
	if err := Verify(ctx, store, "run-cs", 1, 3); err != nil {
		t.Fatalf("cadeia devia permanecer íntegra após shred: %v", err)
	}
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 3); err != nil {
		t.Fatalf("checkpoint assinado devia verificar após shred: %v", err)
	}
	// Shred é idempotente (repetir não falha nem muda a cadeia).
	if err := shredder.Shred(subject); err != nil {
		t.Fatalf("Shred idempotente devia ser no-op: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Verificação — o verificador valida cadeia íntegra E FALHA em cadeia adulterada
// (através do pipeline, incluindo um registo com PII redigida).
// ---------------------------------------------------------------------------

// TestVerifierPassesIntactFailsTampered — cadeia ingerida (com um registo de PII)
// verifica íntegra; qualquer adulteração de um campo selado do registo pessoal (o
// próprio PayloadRef) é detectada.
func TestVerifierPassesIntactFailsTampered(t *testing.T) {
	ctx := context.Background()
	p, store, _, _ := newPipeline(t)

	if _, err := p.Ingest(ctx, RawRecord{Record: sampleRecord("p", DecisionAllow)}); err != nil {
		t.Fatalf("Ingest #1: %v", err)
	}
	if _, err := p.Ingest(ctx, RawRecord{
		Record: sampleRecord("p", DecisionAllow), SubjectID: "s", PII: []byte("pii"),
	}); err != nil {
		t.Fatalf("Ingest pessoal: %v", err)
	}

	// Íntegra: passa.
	if err := Verify(ctx, store, "p", 1, 2); err != nil {
		t.Fatalf("cadeia íntegra devia verificar: %v", err)
	}

	// Adulterar o ContentHash selado do registo pessoal (o atacante troca a âncora do
	// payload sem recalcular o EntryHash) → mutação detectada.
	store.parts["p"][1].PayloadRef.ContentHash[0] ^= 0xFF
	err := Verify(ctx, store, "p", 1, 2)
	assertTamper(t, err, TamperMutation, 2)
}

// ---------------------------------------------------------------------------
// Legal hold impede o shred (fail-closed), mesmo após a retenção expirar.
// ---------------------------------------------------------------------------

// TestLegalHoldBlocksShred — um titular sob legal hold não pode ser shredded: o
// Shred é rejeitado (ErrLegalHold) e a chave PERMANECE, pelo que a PII continua
// recuperável. Após levantar o hold, o shred procede e a PII torna-se irrecuperável.
func TestLegalHoldBlocksShred(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	vault := NewInMemoryKeyVault(detRand())
	payloads := NewInMemoryPayloadStore()
	p := NewIngestPipeline(store, vault, payloads, WithIngestRand(detRand()))

	subject := "subject-held"
	pii := []byte("prova sob preservacao")
	sealed, err := p.Ingest(ctx, RawRecord{
		Record: sampleRecord("p", DecisionAllow), SubjectID: subject, PII: pii,
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	holds := NewLegalHold()
	holds.HoldSubject(subject)
	shredder := NewShredder(vault, holds, NewRetentionPolicy(nil))

	// FAIL-CLOSED: shred sob legal hold é recusado; a chave permanece.
	if err := shredder.Shred(subject); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("shred sob legal hold devia dar ErrLegalHold, veio %v", err)
	}
	if _, ok := vault.Key(sealed.PayloadRef.KeyRef); !ok {
		t.Fatal("a chave NÃO devia ter sido destruída sob legal hold")
	}
	if _, err := p.Recover(*sealed.PayloadRef); err != nil {
		t.Fatalf("PII devia continuar recuperável sob legal hold: %v", err)
	}

	// Levantado o hold, o shred procede e a PII torna-se irrecuperável.
	holds.ReleaseSubject(subject)
	if err := shredder.Shred(subject); err != nil {
		t.Fatalf("shred após levantar hold: %v", err)
	}
	if _, err := p.Recover(*sealed.PayloadRef); !errors.Is(err, ErrShredded) {
		t.Fatalf("PII devia ser irrecuperável após shred, veio %v", err)
	}
}

// TestLegalHoldBlocksPurgeAfterRetentionExpired — o legal hold impede a eliminação
// MESMO depois de a retenção expirar: um purge automático de um dado já fora do
// período é recusado (ErrLegalHold) enquanto o hold vigora.
func TestLegalHoldBlocksPurgeAfterRetentionExpired(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	if _, _, err := vault.EnsureKey("s-held"); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}

	// Retenção de PII operacional = 30 dias; o dado tem 90 dias (bem expirado).
	retention := NewRetentionPolicy(map[DataClass]time.Duration{
		ClassPIIOperational: 30 * 24 * time.Hour,
	})
	holds := NewLegalHold()
	holds.HoldSubject("s-held")
	shredder := NewShredder(vault, holds, retention)

	age := 90 * 24 * time.Hour
	if !retention.Expired(ClassPIIOperational, age) {
		t.Fatal("precondição: o dado devia estar expirado")
	}
	// Expirado, mas sob legal hold → fail-closed, chave preservada.
	if err := shredder.PurgeExpired("s-held", ClassPIIOperational, age); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("purge de dado expirado sob legal hold devia dar ErrLegalHold, veio %v", err)
	}
	if _, ok := vault.Key(KeyRefFor("s-held")); !ok {
		t.Fatal("chave sob legal hold não devia ter sido purgada")
	}

	// Sem hold: purge de dado ainda dentro do período é recusado (retenção activa)...
	holds.ReleaseSubject("s-held")
	if err := shredder.PurgeExpired("s-held", ClassPIIOperational, 10*24*time.Hour); !errors.Is(err, ErrRetentionActive) {
		t.Fatalf("purge dentro do período devia dar ErrRetentionActive, veio %v", err)
	}
	if _, ok := vault.Key(KeyRefFor("s-held")); !ok {
		t.Fatal("chave dentro da retenção não devia ter sido purgada")
	}
	// ...mas expirado e sem hold procede.
	if err := shredder.PurgeExpired("s-held", ClassPIIOperational, age); err != nil {
		t.Fatalf("purge de dado expirado sem hold devia proceder: %v", err)
	}
	if _, ok := vault.Key(KeyRefFor("s-held")); ok {
		t.Fatal("chave expirada e sem hold devia ter sido purgada")
	}
}

// TestPartitionLegalHoldBlocksShred — um titular com dados selados numa partição
// sob legal hold NÃO pode ser shredded, mesmo sem hold por-titular: o índice
// titular→partição faz valer o hold por-partição (fail-closed). Levantado o hold da
// partição, o shred procede. Espelha TestLegalHoldBlocksShred no eixo da partição.
// Salvaguarda fail-closed: se há holds por-partição em vigor mas o índice NÃO foi
// ligado ao shredder (wiring em falta), o shred é recusado — nunca fail-open
// silencioso sobre um controlo de preservação legal.
func TestPartitionHoldWithoutIndexFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	vault := NewInMemoryKeyVault(detRand())
	payloads := NewInMemoryPayloadStore()
	p := NewIngestPipeline(store, vault, payloads, WithIngestRand(detRand()))

	subject := "alice"
	sealed, err := p.Ingest(ctx, RawRecord{Record: sampleRecord("board-42", DecisionAllow), SubjectID: subject, PII: []byte("prova em litigio")})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	holds := NewLegalHold()
	holds.HoldPartition("board-42")
	// Shredder SEM índice ligado (wiring em falta) — não consegue mapear titular→partição.
	shredder := NewShredder(vault, holds, NewRetentionPolicy(nil))

	if err := shredder.Shred(subject); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("shred com holds por-partição e sem índice devia ser recusado fail-closed (ErrLegalHold), veio %v", err)
	}
	if _, ok := vault.Key(sealed.PayloadRef.KeyRef); !ok {
		t.Fatal("a chave NÃO devia ter sido destruída (fail-closed)")
	}

	// Sem quaisquer holds por-partição, o shred procede normalmente (nada a preservar por essa via).
	holds.ReleasePartition("board-42")
	if err := shredder.Shred(subject); err != nil {
		t.Fatalf("sem holds por-partição, o shred devia proceder, veio %v", err)
	}
	if _, ok := vault.Key(sealed.PayloadRef.KeyRef); ok {
		t.Fatal("a chave devia ter sido destruída após o shred")
	}
}

func TestPartitionLegalHoldBlocksShred(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	vault := NewInMemoryKeyVault(detRand())
	payloads := NewInMemoryPayloadStore()
	index := NewInMemorySubjectPartitionIndex()
	// O MESMO índice é alimentado pela ingestão e consultado pelo shredder.
	p := NewIngestPipeline(store, vault, payloads,
		WithIngestRand(detRand()), WithIngestSubjectIndex(index))

	subject := "alice"
	partition := "board-42"
	rec := sampleRecord(partition, DecisionAllow)
	sealed, err := p.Ingest(ctx, RawRecord{Record: rec, SubjectID: subject, PII: []byte("prova em litigio")})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// A ingestão registou o titular na partição.
	if got := index.Partitions(subject); len(got) != 1 || got[0] != partition {
		t.Fatalf("índice devia ligar %q→%q, veio %v", subject, partition, got)
	}

	holds := NewLegalHold()
	holds.HoldPartition(partition) // litigation hold sobre a partição inteira.
	// NB: SEM hold por-titular — só a partição está retida.
	shredder := NewShredder(vault, holds, NewRetentionPolicy(nil), WithShredderSubjectIndex(index))

	// FAIL-CLOSED: o shred é recusado porque a partição do titular está retida.
	if err := shredder.Shred(subject); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("shred sob hold por-partição devia dar ErrLegalHold, veio %v", err)
	}
	if _, ok := vault.Key(sealed.PayloadRef.KeyRef); !ok {
		t.Fatal("a chave NÃO devia ter sido destruída sob hold por-partição")
	}
	if _, err := p.Recover(*sealed.PayloadRef); err != nil {
		t.Fatalf("PII devia continuar recuperável sob hold por-partição: %v", err)
	}

	// PurgeExpired herda a mesma barreira (delega em Shred).
	if err := shredder.PurgeExpired(subject, ClassPIIOperational, 0); !errors.Is(err, ErrRetentionActive) {
		t.Fatalf("purge sem período definido devia dar ErrRetentionActive, veio %v", err)
	}
	retention := NewRetentionPolicy(map[DataClass]time.Duration{ClassPIIOperational: 24 * time.Hour})
	purger := NewShredder(vault, holds, retention, WithShredderSubjectIndex(index))
	if err := purger.PurgeExpired(subject, ClassPIIOperational, 72*time.Hour); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("purge de dado expirado sob hold por-partição devia dar ErrLegalHold, veio %v", err)
	}
	if _, ok := vault.Key(sealed.PayloadRef.KeyRef); !ok {
		t.Fatal("a chave NÃO devia ter sido purgada sob hold por-partição")
	}

	// Levantado o hold da partição, o shred procede e a PII torna-se irrecuperável.
	holds.ReleasePartition(partition)
	if err := shredder.Shred(subject); err != nil {
		t.Fatalf("shred após levantar hold da partição: %v", err)
	}
	if _, err := p.Recover(*sealed.PayloadRef); !errors.Is(err, ErrShredded) {
		t.Fatalf("PII devia ser irrecuperável após shred, veio %v", err)
	}
}

// TestPartitionHoldRequiresIndex — sem o índice ligado ao shredder, o hold
// por-partição não é executável (só o por-titular): documenta explicitamente a
// dependência de wiring, para o controlo não ser advertido como activo sem estar.
func TestPartitionHoldRequiresIndex(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	if _, _, err := vault.EnsureKey("bob"); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	holds := NewLegalHold()
	holds.HoldPartition("board-9")

	// Shredder SEM índice: não consegue mapear "bob" às suas partições. Como HÁ um
	// hold por-partição em vigor, o shred é recusado FAIL-CLOSED (não é possível
	// provar que "bob" não está sob preservação por-partição) — em vez de o violar
	// em silêncio. O índice tem de ser ligado a ambos os lados para permitir o shred
	// enquanto existem holds por-partição.
	noIndex := NewShredder(vault, holds, NewRetentionPolicy(nil))
	if err := noIndex.Shred("bob"); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("sem índice e com hold por-partição, o shred devia ser recusado fail-closed, veio %v", err)
	}
	if _, ok := vault.Key(KeyRefFor("bob")); !ok {
		t.Fatal("a chave de bob não devia ter sido destruída (fail-closed)")
	}

	// Com índice ligado a mapear bob→board-9, o mesmo hold já bloqueia.
	if _, _, err := vault.EnsureKey("carol"); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	index := NewInMemorySubjectPartitionIndex()
	index.Link("carol", "board-9")
	withIndex := NewShredder(vault, holds, NewRetentionPolicy(nil), WithShredderSubjectIndex(index))
	if err := withIndex.Shred("carol"); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("com índice, o hold por-partição devia bloquear, veio %v", err)
	}
	if _, ok := vault.Key(KeyRefFor("carol")); !ok {
		t.Fatal("a chave de carol não devia ter sido destruída sob hold por-partição")
	}
}

// TestShredderHeldQuery — Held expõe, sem mutar nada, a MESMA decisão de legal hold
// (subject OU partição) que o Shred faz valer fail-closed. É a query que o fluxo
// DSAR (AOS-093) consulta antes de iniciar o apagamento multi-store.
func TestShredderHeldQuery(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	if _, _, err := vault.EnsureKey("alice"); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	holds := NewLegalHold()
	index := NewInMemorySubjectPartitionIndex()
	index.Link("alice", "board-7")
	shredder := NewShredder(vault, holds, NewRetentionPolicy(nil), WithShredderSubjectIndex(index))

	// Sem holds: não retido.
	if shredder.Held("alice") {
		t.Fatal("Held devia ser false sem qualquer hold")
	}
	// Hold por-titular.
	holds.HoldSubject("alice")
	if !shredder.Held("alice") {
		t.Fatal("Held devia ser true sob hold por-titular")
	}
	holds.ReleaseSubject("alice")
	// Hold por-partição (via índice).
	holds.HoldPartition("board-7")
	if !shredder.Held("alice") {
		t.Fatal("Held devia ser true sob hold por-partição (via índice)")
	}
	// Held é uma query pura: a chave permanece.
	if _, ok := vault.Key(KeyRefFor("alice")); !ok {
		t.Fatal("Held não devia destruir a chave")
	}
}

// TestShredEmptySubject — shred sem titular é fail-closed.
func TestShredEmptySubject(t *testing.T) {
	shredder := NewShredder(NewInMemoryKeyVault(detRand()), NewLegalHold(), NewRetentionPolicy(nil))
	if err := shredder.Shred(""); !errors.Is(err, ErrNoSubject) {
		t.Fatalf("shred sem titular devia dar ErrNoSubject, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// Recover — casos de fronteira do caminho de recuperação.
// ---------------------------------------------------------------------------

// TestRecoverMissingAndTampered — Recover distingue payload ausente, ref vazia e
// ciphertext adulterado (fail-closed em todos).
func TestRecoverMissingAndTampered(t *testing.T) {
	ctx := context.Background()
	p, _, _, payloads := newPipeline(t)

	// Ref vazia → ausente.
	if _, err := p.Recover(PayloadRef{}); !errors.Is(err, ErrPayloadMissing) {
		t.Fatalf("ref vazia devia dar ErrPayloadMissing, veio %v", err)
	}
	// Ref para blob inexistente → ausente.
	if _, err := p.Recover(PayloadRef{ContentHash: []byte{1, 2, 3}, KeyRef: "kms:x"}); !errors.Is(err, ErrPayloadMissing) {
		t.Fatalf("blob inexistente devia dar ErrPayloadMissing, veio %v", err)
	}

	// Ingerir e depois adulterar o ciphertext no PayloadStore → decifragem falha.
	sealed, err := p.Ingest(ctx, RawRecord{
		Record: sampleRecord("p", DecisionAllow), SubjectID: "s", PII: []byte("segredo"),
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	ref := hexOf(sealed.PayloadRef.ContentHash)
	blob, _ := payloads.Get(ref)
	blob[len(blob)-1] ^= 0xFF // corromper o último byte do JSON/ciphertext
	payloads.Put(ref, blob)
	if _, err := p.Recover(*sealed.PayloadRef); err == nil {
		t.Fatal("ciphertext adulterado devia falhar a recuperação")
	}
}

// ---------------------------------------------------------------------------
// Separação física do efémero (AOS-082): destinos distintos; a integridade do
// audit não depende do WideEventStore efémero com TTL.
// ---------------------------------------------------------------------------

// TestAuditSeparatedFromWideEventStore — demonstra a separação FÍSICA face ao
// WideEventStore efémero de AOS-082: o mesmo facto vai para dois destinos com
// naturezas opostas. O audit é hash-chained/WORM/verificável; o wide event é
// efémero (Ephemeral=true, TTL) e é DESCARTADO por eviction — sem afetar a cadeia.
func TestAuditSeparatedFromWideEventStore(t *testing.T) {
	ctx := context.Background()
	p, store, _, _ := newPipeline(t)

	var now int64 // relógio lógico do store efémero (unix-nano).
	clock := func() time.Time { return time.Unix(0, now).UTC() }
	ephemeral := otelgenai.NewWideEventStore(time.Minute, clock)

	for i := 0; i < 3; i++ {
		if _, err := p.Ingest(ctx, RawRecord{Record: sampleRecord("run-sep", DecisionAllow)}); err != nil {
			t.Fatalf("Ingest #%d: %v", i, err)
		}
		// O MESMO facto, como diagnóstico efémero, no destino separado.
		ev := ephemeral.Add(otelgenai.WideEvent{RunID: "run-sep", Operation: "tool.call"})
		if !ev.Ephemeral {
			t.Fatal("um wide event é sempre efémero (Ephemeral=true)")
		}
	}

	// Ambos os destinos têm os 3 eventos vivos agora.
	if head, _ := store.Head(ctx, "run-sep"); head != 3 {
		t.Fatalf("audit devia reter os 3 registos, head=%d", head)
	}
	if ephemeral.Len() != 3 {
		t.Fatalf("efémero devia ter 3 eventos vivos, tem %d", ephemeral.Len())
	}

	// Avançar o relógio para além do TTL: o efémero é DESTRUÍDO por eviction...
	now = time.Hour.Nanoseconds()
	if reaped := ephemeral.Reap(); reaped != 3 {
		t.Fatalf("efémero devia descartar os 3 por TTL, descartou %d", reaped)
	}
	if ephemeral.Len() != 0 {
		t.Fatal("efémero devia estar vazio após eviction")
	}

	// ...mas o audit permanente é INDEPENDENTE: a cadeia continua completa e verificável.
	if head, _ := store.Head(ctx, "run-sep"); head != 3 {
		t.Fatalf("audit não devia perder registos com a eviction do efémero, head=%d", head)
	}
	if err := Verify(ctx, store, "run-sep", 1, 3); err != nil {
		t.Fatalf("cadeia de audit devia verificar independentemente do efémero: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Retenção — política por classe.
// ---------------------------------------------------------------------------

// TestRetentionPolicyPerClass — período por classe; classe sem período nunca expira.
func TestRetentionPolicyPerClass(t *testing.T) {
	r := NewRetentionPolicy(map[DataClass]time.Duration{
		ClassDiagnostic:     time.Hour,
		ClassAudit:          365 * 24 * time.Hour,
		ClassPIIOperational: 0, // <=0 tratado como indefinido.
	})
	if d, ok := r.Period(ClassDiagnostic); !ok || d != time.Hour {
		t.Fatalf("Period(diagnostic)=%v,%v", d, ok)
	}
	if _, ok := r.Period(ClassPIIOperational); ok {
		t.Fatal("período <=0 devia ser indefinido")
	}
	if _, ok := r.Period(ClassTrajectory); ok {
		t.Fatal("classe ausente devia ser indefinida")
	}
	if !r.Expired(ClassDiagnostic, 2*time.Hour) {
		t.Fatal("diagnóstico de 2h devia estar expirado (retenção 1h)")
	}
	if r.Expired(ClassDiagnostic, 30*time.Minute) {
		t.Fatal("diagnóstico de 30m não devia estar expirado")
	}
	// Classe sem período nunca expira (fail-closed).
	if r.Expired(ClassTrajectory, 100*365*24*time.Hour) {
		t.Fatal("classe sem período não devia expirar nunca")
	}
}

// TestLegalHoldPartition — legal hold também por partição (suspensão por board/run).
func TestLegalHoldPartition(t *testing.T) {
	h := NewLegalHold()
	if h.HeldPartition("run-x") {
		t.Fatal("partição não devia começar retida")
	}
	h.HoldPartition("run-x")
	if !h.HeldPartition("run-x") {
		t.Fatal("partição devia estar retida")
	}
	h.ReleasePartition("run-x")
	if h.HeldPartition("run-x") {
		t.Fatal("partição devia ter sido libertada")
	}
}

// TestKeyVaultEnsureIdempotent — EnsureKey é idempotente por titular (mesma KEK e
// KeyRef nas escritas seguintes); Delete torna a KeyRef ausente.
func TestKeyVaultEnsureIdempotent(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	k1, ref1, err := vault.EnsureKey("s")
	if err != nil {
		t.Fatalf("EnsureKey #1: %v", err)
	}
	k2, ref2, err := vault.EnsureKey("s")
	if err != nil {
		t.Fatalf("EnsureKey #2: %v", err)
	}
	if ref1 != ref2 || ref1 != KeyRefFor("s") {
		t.Fatalf("KeyRef não estável: %q vs %q", ref1, ref2)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("EnsureKey devia devolver a mesma KEK para o mesmo titular")
	}
	vault.Delete("s")
	if _, ok := vault.Key(ref1); ok {
		t.Fatal("Delete devia ter removido a KEK")
	}
}

// TestPipelineDefaultCryptoRand — sem RandSource injectada, o vault e o pipeline
// caem em crypto/rand (produção): a ingestão de PII e o round-trip de recuperação
// funcionam com entropia real (não determinística — só se verifica o round-trip).
func TestPipelineDefaultCryptoRand(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	vault := NewInMemoryKeyVault(nil)                               // nil ⇒ crypto/rand.
	p := NewIngestPipeline(store, vault, NewInMemoryPayloadStore()) // sem WithIngestRand ⇒ crypto/rand.

	pii := []byte("entropia real")
	sealed, err := p.Ingest(ctx, RawRecord{
		Record: sampleRecord("p", DecisionAllow), SubjectID: "s", PII: pii,
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	got, err := p.Recover(*sealed.PayloadRef)
	if err != nil || !bytes.Equal(got, pii) {
		t.Fatalf("round-trip com crypto/rand falhou: got=%q err=%v", got, err)
	}
	if err := Verify(ctx, store, "p", 1, 1); err != nil {
		t.Fatalf("cadeia devia verificar: %v", err)
	}
}

// hexOf é um atalho local para o hex do ContentHash (índice do PayloadStore).
func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}
