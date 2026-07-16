package audit

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Este ficheiro NÃO reimplementa o audit tamper-evident de AOS-011: COMPÕE e
// DEMONSTRA que a base já existente satisfaz os seis Critérios de Aceitação de
// AOS-072, cobrindo os vectores dos "Testes Requeridos" que ainda não tinham
// demonstração explícita — prova de WORM (reescrita/remoção via aplicação
// falha/é impossível), deteção de REORDENAÇÃO, independência face aos
// diagnósticos efémeros e compatibilidade com crypto-shredding (EPIC-09).

// ---------------------------------------------------------------------------
// Critério 2 — WORM: nenhuma reescrita/remoção pela via aplicacional.
// ---------------------------------------------------------------------------

// TestWORMStoreExposesNoMutators é a PROVA ESTRUTURAL de WORM: o contrato [Store]
// só expõe operações append-only (Append) e de leitura (Read/Head/At). Não há —
// e não pode haver, sem quebrar o contrato — nenhum método de reescrita ou
// remoção (Update/Delete/Remove/Truncate/Overwrite/Set/Put). "Write-once" é,
// portanto, imposto pela SUPERFÍCIE da API: a aplicação não tem sequer como
// exprimir uma reescrita/remoção. Produção liga storage WORM real por trás desta
// mesma fronteira sem alargar a superfície.
func TestWORMStoreExposesNoMutators(t *testing.T) {
	storeType := reflect.TypeOf((*Store)(nil)).Elem()

	allowed := map[string]bool{"Append": true, "Read": true, "Head": true, "At": true}
	forbidden := []string{"update", "delete", "remove", "truncate", "overwrite", "set", "put", "replace", "rewrite"}

	got := make(map[string]bool, storeType.NumMethod())
	for i := 0; i < storeType.NumMethod(); i++ {
		name := storeType.Method(i).Name
		got[name] = true
		if !allowed[name] {
			t.Fatalf("Store expõe método inesperado %q — a superfície WORM só admite %v", name, keysOf(allowed))
		}
		low := strings.ToLower(name)
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Fatalf("Store expõe mutador proibido %q (contém %q): WORM exige superfície sem reescrita/remoção", name, bad)
			}
		}
	}
	for name := range allowed {
		if !got[name] {
			t.Fatalf("Store deixou de expor a operação append-only/leitura %q", name)
		}
	}
}

// TestWORMAppendNeverOverwrites é a PROVA COMPORTAMENTAL de WORM: a única mutação
// possível (Append) NUNCA reescreve um registo existente — atribui sempre um novo
// audit_seq gapless e deixa os anteriores byte-a-byte intactos. Uma "tentativa de
// reescrita via aplicação" (re-submeter conteúdo para um seq já selado) é
// inexprimível: o produtor não escolhe o seq; escrever mais só ESTENDE a cadeia.
func TestWORMAppendNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()

	// Selar o registo 1 e capturar o seu estado imutável.
	first, err := store.Append(ctx, sampleRecord("p", DecisionAllow))
	if err != nil {
		t.Fatalf("Append inicial: %v", err)
	}
	origHash := append([]byte(nil), first.EntryHash...)

	// "Tentativa de reescrita": re-submeter conteúdo DIFERENTE alegadamente para o
	// mesmo registo. O Store não aceita um seq-alvo; só pode acrescentar. O registo
	// 1 permanece intacto e a escrita nova recebe seq=2.
	rewriteAttempt := sampleRecord("p", DecisionDeny)
	rewriteAttempt.AuditSeq = 1 // o produtor "pede" o seq 1; o Store ignora e atribui o próximo.
	rewriteAttempt.Capability = "fs:write:/etc/shadow"
	sealed, err := store.Append(ctx, rewriteAttempt)
	if err != nil {
		t.Fatalf("Append de reescrita: %v", err)
	}
	if sealed.AuditSeq != 2 {
		t.Fatalf("reescrita foi aceite como seq=%d; WORM exige extensão (seq=2), nunca overwrite do 1", sealed.AuditSeq)
	}

	// O registo 1 continua o original (não foi reescrito).
	got1, ok, err := store.At(ctx, "p", 1)
	if err != nil || !ok {
		t.Fatalf("At(1)=ok?%v err=%v", ok, err)
	}
	if got1.Decision != DecisionAllow || string(got1.EntryHash) != string(origHash) {
		t.Fatalf("registo 1 foi reescrito: decision=%s hash-alterado=%v", got1.Decision, string(got1.EntryHash) != string(origHash))
	}
	// A cadeia inteira (1..2) permanece íntegra e verificável.
	if err := Verify(ctx, store, "p", 1, 2); err != nil {
		t.Fatalf("cadeia devia permanecer íntegra após tentativa de reescrita: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Critério 4 — adulteração detectável: o vector REORDENAÇÃO (além de
// edição/remoção/inserção já cobertos em verify_test.go).
// ---------------------------------------------------------------------------

// TestVerifyDetectsReordering — trocar fisicamente a posição de dois registos
// (reordenação) é DETECTADO: cada registo carrega o seu audit_seq e PrevHash
// selados, pelo que a ordem de armazenamento deixa de casar com a ordem total e a
// verificação da cadeia falha (contiguidade de seq e/ou encadeamento quebram).
func TestVerifyDetectsReordering(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	// Trocar as posições dos registos seq=3 e seq=4 (reordenação adjacente).
	p := store.parts["p"]
	p[2], p[3] = p[3], p[2] // ordem de armazenamento passa a 1,2,4,3,5

	err := Verify(ctx, store, "p", 1, 5)
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("reordenação não detectada: %v", err)
	}
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("esperado *VerifyError, veio %T", err)
	}
	// A reordenação manifesta-se como seq fora de ordem (remoção/inserção) ou como
	// encadeamento quebrado — qualquer um destes prova a deteção.
	switch ve.Type {
	case TamperRemoval, TamperInsertion, TamperChainBroken:
	default:
		t.Fatalf("tipo inesperado para reordenação: %s", ve.Type)
	}
}

// ---------------------------------------------------------------------------
// Critério 3 — assinatura: um LOTE (checkpoint) com assinatura inválida é
// rejeitado ao nível da verificação de assinatura (VerifyCheckpoint), de forma
// independente da verificação de intervalo.
// ---------------------------------------------------------------------------

// TestVerifyCheckpointRejectsTamperedBatch — o checkpoint sela (assina) o
// EntryHash acumulado de um LOTE. Adulterar QUALQUER campo assinado depois da
// assinatura (aqui o EntryHash do lote) invalida a assinatura: VerifyCheckpoint
// devolve ErrCheckpointSignature. Prova a origem+integridade por assinatura.
func TestVerifyCheckpointRejectsTamperedBatch(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)
	signer := newSigner(t)

	cp, err := signer.Seal(ctx, store, "p", 4)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Assinatura válida antes da adulteração.
	if err := VerifyCheckpoint(signer.Public(), cp); err != nil {
		t.Fatalf("assinatura de lote válida rejeitada: %v", err)
	}

	// Adulterar o EntryHash do lote assinado (o atacante forja a âncora do lote).
	tampered := cp
	tampered.EntryHash = append([]byte(nil), cp.EntryHash...)
	tampered.EntryHash[0] ^= 0xFF
	if err := VerifyCheckpoint(signer.Public(), tampered); !errors.Is(err, ErrCheckpointSignature) {
		t.Fatalf("lote com EntryHash adulterado devia ser rejeitado: %v", err)
	}

	// Adulterar o audit_seq do lote (reetiquetar o lote) — igualmente rejeitado.
	tampered2 := cp
	tampered2.AuditSeq = 99
	if err := VerifyCheckpoint(signer.Public(), tampered2); !errors.Is(err, ErrCheckpointSignature) {
		t.Fatalf("lote com audit_seq adulterado devia ser rejeitado: %v", err)
	}
}

// TestVerifyCheckpointInvalidKey — material de chave pública com dimensão inválida
// é rejeitado (não há caminho fail-open na verificação de assinatura).
func TestVerifyCheckpointInvalidKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 2)
	signer := newSigner(t)
	cp, _ := signer.Seal(ctx, store, "p", 2)

	if err := VerifyCheckpoint([]byte{1, 2, 3}, cp); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("chave pública inválida devia dar ErrInvalidKey, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// Frescura de checkpoint (rollback): VerifyFromCheckpointAtHead impõe um piso de
// frescura conhecido fora do store, fechando o vector "The Audit Log Lied" em que
// um checkpoint antigo validamente assinado é reapresentado para mascarar
// truncatura do tail dos registos posteriores.
// ---------------------------------------------------------------------------

// TestVerifyAtHeadRejectsRollbackCheckpoint — o cenário do finding: a cadeia
// atingiu o seq=8 e o verificador sabe (fora do store) que o head é 8. O atacante
// trunca o tail para 5 e reapresenta um checkpoint LEGÍTIMO e bem-assinado no
// seq=3 (anterior aos registos removidos). VerifyFromCheckpoint nu aceitaria; o
// piso de frescura rejeita com ErrCheckpointStale.
func TestVerifyAtHeadRejectsRollbackCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 8)
	signer := newSigner(t)

	// Checkpoint antigo, legítimo e bem-assinado, no seq=3.
	cpOld, err := signer.Seal(ctx, store, "p", 3)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// A assinatura do checkpoint antigo é, de facto, válida.
	if err := VerifyCheckpoint(signer.Public(), cpOld); err != nil {
		t.Fatalf("checkpoint antigo devia ter assinatura valida: %v", err)
	}

	// Truncatura do tail: a cadeia real cai para o seq=5.
	store.parts["p"] = store.parts["p"][:5]

	// VerifyFromCheckpoint NU aceita a reapresentação do checkpoint antigo (só
	// verifica o intervalo que resta), ilustrando o vector.
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cpOld, 3); err != nil {
		t.Fatalf("VerifyFromCheckpoint nu nao deveria detetar o rollback: %v", err)
	}

	// Com o piso de frescura conhecido (o verificador sabe que o head era 8), o
	// checkpoint anterior a 8 é rejeitado como rollback.
	err = VerifyFromCheckpointAtHead(ctx, store, signer.Public(), cpOld, 8, 8)
	if !errors.Is(err, ErrCheckpointStale) {
		t.Fatalf("esperado ErrCheckpointStale para checkpoint anterior ao head conhecido, veio %v", err)
	}
}

// TestVerifyAtHeadFreshCheckpointPasses — um checkpoint no head conhecido (ou
// acima) passa o piso de frescura e verifica normalmente cp+1..to.
func TestVerifyAtHeadFreshCheckpointPasses(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 8)
	signer := newSigner(t)

	// Checkpoint fresco no head conhecido (seq=8).
	cp, err := signer.Seal(ctx, store, "p", 8)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// expectedHead==8, cp.AuditSeq==8: passa o piso; to==8 (nada além da âncora).
	if err := VerifyFromCheckpointAtHead(ctx, store, signer.Public(), cp, 8, 8); err != nil {
		t.Fatalf("checkpoint fresco no head devia passar: %v", err)
	}

	// Checkpoint no seq=4 com um piso de frescura igualmente 4: passa e verifica 5..8.
	cp4, _ := signer.Seal(ctx, store, "p", 4)
	if err := VerifyFromCheckpointAtHead(ctx, store, signer.Public(), cp4, 4, 8); err != nil {
		t.Fatalf("checkpoint no piso devia passar e verificar o intervalo: %v", err)
	}
}

// TestVerifyAtHeadDetectsTruncationBelowExpected — mesmo com um checkpoint que
// satisfaz o piso, pedir a verificação até ao head esperado expõe a truncatura do
// tail: os registos até `to` já não existem no store → ErrRangeBeyondHead.
func TestVerifyAtHeadDetectsTruncationBelowExpected(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 8)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 4) // checkpoint no seq=4

	// Truncatura do tail: cai para 5 (removidos 6,7,8).
	store.parts["p"] = store.parts["p"][:5]

	// O verificador sabe que o head deve ser 8 e ancora a verificação nesse head.
	// cp.AuditSeq(4) >= expectedHead? Não: expectedHead é 8 → o checkpoint no 4 é
	// ele próprio anterior ao head conhecido → stale.
	err := VerifyFromCheckpointAtHead(ctx, store, signer.Public(), cp, 8, 8)
	if !errors.Is(err, ErrCheckpointStale) {
		t.Fatalf("esperado ErrCheckpointStale, veio %v", err)
	}

	// Se, em vez disso, o verificador tivesse um checkpoint fresco no 8 mas o store
	// estivesse truncado, a verificação até 8 expõe a truncatura por ErrRangeBeyondHead.
	cp8Store := NewMemStore()
	appendN(t, cp8Store, "p", 8)
	cp8, _ := signer.Seal(ctx, cp8Store, "p", 8)
	cp8Store.parts["p"] = cp8Store.parts["p"][:5] // trunca DEPOIS de selar
	err = VerifyFromCheckpointAtHead(ctx, cp8Store, signer.Public(), cp8, 8, 8)
	if !errors.Is(err, ErrRangeBeyondHead) {
		t.Fatalf("truncatura abaixo do head esperado devia dar ErrRangeBeyondHead, veio %v", err)
	}
}

// TestVerifyAtHeadRejectsBadSignatureBeforeFreshness — a assinatura é validada
// ANTES do piso de frescura: um checkpoint mal-assinado é rejeitado como tal, não
// mascarado por (nem confundido com) stale.
func TestVerifyAtHeadRejectsBadSignatureBeforeFreshness(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 8)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 3)
	cp.Signature[0] ^= 0xFF // adulterar a assinatura

	// expectedHead=8 tornaria este cp "stale" pelo seq; mas a assinatura inválida
	// tem precedência.
	err := VerifyFromCheckpointAtHead(ctx, store, signer.Public(), cp, 8, 8)
	if !errors.Is(err, ErrCheckpointSignature) {
		t.Fatalf("assinatura invalida devia ter precedencia sobre a frescura, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// Critério 5 — separação física dos diagnósticos efémeros: destinos e retenção
// distintos; a INTEGRIDADE do audit não depende do canal efémero.
// ---------------------------------------------------------------------------

// ephemeralDiag modela um canal de DIAGNÓSTICOS EFÉMEROS (logs/spans): destino
// distinto do audit WORM, com retenção LIMITADA e LOSSY (descarta quando cheio) e
// SEM garantia de integridade (não é hash-chained nem verificável). Serve para
// demonstrar a separação: perder/descartar diagnósticos NÃO afecta a cadeia de
// audit.
type ephemeralDiag struct {
	mu      sync.Mutex
	cap     int
	kept    []string
	dropped int
}

func (e *ephemeralDiag) RecordMediation(_ context.Context, rec referencemonitor.MediationRecord) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.kept) >= e.cap {
		e.dropped++ // retenção efémera: descarta silenciosamente (best-effort, não fail-closed).
		return 0, nil
	}
	e.kept = append(e.kept, rec.RunID+"/"+rec.StepID)
	return 0, nil
}

// TestAuditSeparatedFromEphemeralDiagnostics — o audit tamper-evident (WORM,
// hash-chained, verificável) e os diagnósticos efémeros são DESTINOS DISTINTOS
// com RETENÇÃO DISTINTA. A cadeia de audit mantém-se completa e verificável mesmo
// quando o canal efémero perde eventos ou é totalmente esvaziado — a integridade
// do audit NÃO depende do efémero.
func TestAuditSeparatedFromEphemeralDiagnostics(t *testing.T) {
	ctx := context.Background()

	auditStore := NewMemStore() // destino durável, WORM, verificável.
	auditChain := NewMediationSink(auditStore, withSinkClock(func() time.Time { return fixedTime }))
	diag := &ephemeralDiag{cap: 2} // destino efémero, lossy, não-verificável.

	// Fan-out para AMBOS os destinos distintos. O audit é o primário durável.
	tee := NewTeeSink(auditChain, diag)

	const n = 5
	for i := 0; i < n; i++ {
		_, err := tee.RecordMediation(ctx, referencemonitor.MediationRecord{
			RunID: "run-sep", StepID: string(rune('a' + i)),
			Effect:     referencemonitor.EffectPermit,
			ToolID:     "tool.http",
			Capability: "cap:x",
			Principal:  referencemonitor.Principal{NHIID: "nhi:a"},
		})
		if err != nil {
			t.Fatalf("RecordMediation #%d: %v", i, err)
		}
	}

	// Destinos distintos: o audit reteve TODOS os n; o efémero descartou o excesso.
	if head, _ := auditStore.Head(ctx, "run-sep"); head != n {
		t.Fatalf("audit WORM devia reter todos os %d registos, head=%d", n, head)
	}
	if diag.dropped == 0 || len(diag.kept) != diag.cap {
		t.Fatalf("canal efémero devia ter descartado por retenção limitada: kept=%d dropped=%d", len(diag.kept), diag.dropped)
	}

	// A integridade do audit é INDEPENDENTE do efémero: a cadeia completa verifica.
	if err := Verify(ctx, auditStore, "run-sep", 1, n); err != nil {
		t.Fatalf("cadeia de audit devia verificar independentemente do efémero: %v", err)
	}

	// Esvaziar completamente o canal efémero (perda total) NÃO afecta o audit.
	diag.mu.Lock()
	diag.kept = nil
	diag.dropped = 0
	diag.mu.Unlock()
	if err := Verify(ctx, auditStore, "run-sep", 1, n); err != nil {
		t.Fatalf("perda total do efémero não devia afectar a integridade do audit: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Compatibilidade com crypto-shredding (EPIC-09): a cadeia sela o HASH do
// ciphertext (nunca o plaintext), logo mantém-se íntegra mesmo com o payload
// irrecuperável.
// ---------------------------------------------------------------------------

// TestChainIntegrityAfterCryptoShredding — modela o crypto-shredding: o plaintext
// pessoal vive CIFRADO fora da cadeia, indexado por KeyRef num keystore; a cadeia
// sela apenas o ContentHash (hash do CIPHERTEXT) + KeyRef + SubjectID. Um DSAR
// destrói a chave (o plaintext torna-se IRRECUPERÁVEL), mas o registo selado não
// muda — a cadeia continua a verificar. "Imutável = íntegro, não eterno".
func TestChainIntegrityAfterCryptoShredding(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()

	// Keystore por titular (fora da cadeia). O plaintext só é recuperável enquanto
	// a chave existir.
	keystore := map[string][]byte{"kms:key:subject-123": {0x5A}}
	subjectID := "subject-123"
	keyRef := "kms:key:subject-123"

	plaintext := []byte("dados pessoais do titular")
	ciphertext := xorCipher(plaintext, keystore[keyRef][0])
	contentHash := sha256.Sum256(ciphertext) // o que a cadeia sela: hash do CIPHERTEXT.

	// Selar antes e depois um registo normal (o registo pessoal fica no meio da cadeia).
	appendN(t, store, "run-cs", 1)
	rec := sampleRecord("run-cs", DecisionAllow)
	rec.PayloadRef = &PayloadRef{
		ContentHash: contentHash[:],
		KeyRef:      keyRef,
		SubjectID:   subjectID,
	}
	if _, err := store.Append(ctx, rec); err != nil {
		t.Fatalf("Append do registo com PayloadRef: %v", err)
	}
	appendN(t, store, "run-cs", 1)

	// Pré-shred: a cadeia verifica e o ciphertext é decifrável.
	if err := Verify(ctx, store, "run-cs", 1, 3); err != nil {
		t.Fatalf("cadeia devia verificar antes do shred: %v", err)
	}
	sealed, _, _ := store.At(ctx, "run-cs", 2)
	if sealed.PayloadRef == nil || string(sealed.PayloadRef.ContentHash) != string(contentHash[:]) {
		t.Fatal("a cadeia devia selar o ContentHash do ciphertext")
	}
	// A cadeia NUNCA guardou o plaintext: o conteúdo canónico não o contém.
	if bytesContains(canonicalContent(sealed), plaintext) {
		t.Fatal("plaintext pessoal não deve aparecer no conteúdo selado")
	}

	// CRYPTO-SHREDDING (DSAR): destruir a chave por titular.
	delete(keystore, keyRef)

	// O plaintext é agora IRRECUPERÁVEL (a chave desapareceu).
	if _, ok := keystore[keyRef]; ok {
		t.Fatal("a chave devia ter sido destruída (shredding)")
	}

	// A cadeia mantém-se ÍNTEGRA e verificável: o EntryHash selou o ContentHash,
	// não o plaintext — a destruição da chave não muda nenhum registo selado.
	if err := Verify(ctx, store, "run-cs", 1, 3); err != nil {
		t.Fatalf("cadeia devia permanecer íntegra após crypto-shredding: %v", err)
	}
	// E um checkpoint assinado sobre a âncora continua a validar após o shred.
	signer := newSigner(t)
	cp, err := signer.Seal(ctx, store, "run-cs", 3)
	if err != nil {
		t.Fatalf("Seal pós-shred: %v", err)
	}
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 3); err != nil {
		t.Fatalf("verificação ancorada devia passar após shred: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers locais (só deste ficheiro).
// ---------------------------------------------------------------------------

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// xorCipher é uma cifra determinista trivial (só para teste): NÃO é criptografia
// real — serve apenas para produzir um "ciphertext" cujo hash a cadeia sela e um
// "plaintext" que se torna irrecuperável quando a chave (o byte) é destruída.
func xorCipher(data []byte, key byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ key
	}
	return out
}

func bytesContains(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}
