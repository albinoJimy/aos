package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-213 (CON-02/DEF-903) — PROVAS FALSIFICÁVEIS da superfície de administração de legal hold e
// expiração. Cada teste é NÃO-VACUOSO e prova os DOIS sentidos. Correr SEMPRE com -race.
//
//   - Legal hold BLOQUEIA um /dsar/erase subsequente E um titular held é SALTADO pela expiração;
//     após release, o erase/expiração SUCEDE.
//   - A expiração é apagamento REAL: um titular expirado fica IRRECUPERÁVEL (OpenContent ⇒
//     ErrDecrypt) e a hash-chain do WORM continua a VALIDAR (a prova de AOS-093 pela via da
//     expiração por TTL).
//   - As rotas de administração exigem a credencial forte (readGov): sem/forjada ⇒ recusado;
//     válida ⇒ aceite. Cada acção de hold/release é selada no WORM SEM PII.

// retentionFarFuture é o relógio determinista do ExpirationJob nos testes: um instante muito à
// frente de qualquer captura real, pelo que a idade (clock − CreatedAt) ultrapassa SEMPRE o TTL —
// isolando o comportamento sob teste (hold suspende um registo que DE OUTRO MODO expiraria).
func retentionFarFuture() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }

// newRetentionNode arranca um nó com execução durável (para o capturer cifrar por-titular no ES),
// soberania de leitura ligada (para as rotas autenticarem) e uma política de retenção que expira a
// classe pii_operational — com um relógio determinista muito à frente.
func newRetentionNode(t *testing.T) *Node {
	t.Helper()
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{
		audit.ClassPIIOperational: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRetentionConfig: %v", err)
	}
	cfg.Retention = rc
	cfg.RetentionClock = retentionFarFuture
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (retention): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// -------------------------------------------------------------------------------------
// (CA4) EXPIRAÇÃO REAL — o titular expirado fica IRRECUPERÁVEL e a hash-chain valida.
// -------------------------------------------------------------------------------------

func TestNode_AOS213_ExpirationRealErasure(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	const subject = "nhi:agent-expire"
	const runID = "run-expire"

	captureSynthetic(t, node, subject, runID, "conteudo a expirar: EXP-777", "outExp")

	// ANTES (não-vácuo): o conteúdo é recuperável.
	sealed, sealedSubj := sealedContentOf(t, node, runID)
	if sealedSubj != subject {
		t.Fatalf("conteudo nao selado sob o titular: %q", sealedSubj)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("antes da expiracao devia recuperar: %v", err)
	}

	// EXPIRA via o ExpirationJob composto no nó.
	report, err := node.ExpirationJob.Run(ctx)
	if err != nil {
		t.Fatalf("ExpirationJob.Run: %v", err)
	}
	if report.Expired < 1 {
		t.Fatalf("esperava >=1 expirado, veio %+v", report)
	}

	// IRRECUPERÁVEL: a decifragem do MESMO blob agora falha (a KEK morreu).
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("apos expiracao devia ser irrecuperavel (ErrDecrypt), deu: %v", err)
	}

	// HASH-CHAIN da partição de retenção VALIDA (selada, não mutada).
	head, err := node.WORM.Head(ctx, retentionPartition)
	if err != nil || head < 1 {
		t.Fatalf("particao de retencao vazia: head=%d err=%v", head, err)
	}
	if err := audit.Verify(ctx, node.WORM, retentionPartition, 1, head); err != nil {
		t.Fatalf("hash-chain de retencao NAO valida apos a expiracao: %v", err)
	}

	// IDEMPOTÊNCIA: uma segunda passagem não re-expira nem falha (a key já foi vista).
	report2, err := node.ExpirationJob.Run(ctx)
	if err != nil {
		t.Fatalf("2a passagem: %v", err)
	}
	if report2.Expired != 0 {
		t.Fatalf("2a passagem NAO devia re-expirar, veio %+v", report2)
	}
}

// -------------------------------------------------------------------------------------
// (CA3) HOLD SALTA a expiração; RELEASE reabre-a.
// -------------------------------------------------------------------------------------

func TestNode_AOS213_HeldSkippedByExpirationThenReleased(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	const subject = "nhi:agent-held-exp"
	const runID = "run-held-exp"

	captureSynthetic(t, node, subject, runID, "conteudo retido exp: HELDEXP-888", "outHeldExp")
	sealed, _ := sealedContentOf(t, node, runID)

	// SOB HOLD: o titular é saltado (Held), nada expira, o conteúdo continua decifrável.
	node.DSARHolds.HoldSubject(subject)
	report, err := node.ExpirationJob.Run(ctx)
	if err != nil {
		t.Fatalf("Run (held): %v", err)
	}
	if report.Held < 1 {
		t.Fatalf("esperava >=1 held, veio %+v", report)
	}
	if report.Expired != 0 {
		t.Fatalf("sob hold NADA devia expirar, veio %+v", report)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("sob hold o conteudo devia continuar decifravel: %v", err)
	}

	// LIBERTADO: a expiração procede e o conteúdo torna-se irrecuperável.
	node.DSARHolds.ReleaseSubject(subject)
	report2, err := node.ExpirationJob.Run(ctx)
	if err != nil {
		t.Fatalf("Run (released): %v", err)
	}
	if report2.Expired < 1 {
		t.Fatalf("apos release devia expirar, veio %+v", report2)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("apos release+expiracao devia ser irrecuperavel: %v", err)
	}
}

// -------------------------------------------------------------------------------------
// (CA3) HOLD via ROTA bloqueia o /dsar/erase; RELEASE via rota reabre-o.
// -------------------------------------------------------------------------------------

func TestHoldRouteBlocksEraseThenReleaseAllows(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	const subject = "subject-hold-route"
	ref := seedPII(t, node, subject)

	// HOLD via rota.
	rec := postReq(h, "/dsar/hold", holdRequestWire{RequestID: "h1", SubjectID: subject}, govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("hold devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if !node.DSARHolds.HeldSubject(subject) {
		t.Fatal("apos POST /dsar/hold o titular devia estar sob hold")
	}

	// ERASE agora BLOQUEADO.
	er := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "e1", SubjectID: subject}, govHeaders())
	if er.Code != http.StatusOK {
		t.Fatalf("erase bloqueado devia dar 200 (desfecho legitimo), veio %d", er.Code)
	}
	var resp dsarResponse
	_ = json.Unmarshal(er.Body.Bytes(), &resp)
	if !resp.Blocked {
		t.Fatalf("erase sob hold devia estar BLOQUEADO, veio %+v", resp)
	}
	if _, ok := node.DSARVault.Key(ref); !ok {
		t.Fatal("sob hold a KEK devia SOBREVIVER")
	}

	// RELEASE via rota.
	rel := postReq(h, "/dsar/release", holdRequestWire{RequestID: "r1", SubjectID: subject}, govHeaders())
	if rel.Code != http.StatusOK {
		t.Fatalf("release devia dar 200, veio %d (%s)", rel.Code, rel.Body.String())
	}
	if node.DSARHolds.HeldSubject(subject) {
		t.Fatal("apos POST /dsar/release o titular NAO devia continuar sob hold")
	}

	// ERASE agora SUCEDE.
	er2 := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "e2", SubjectID: subject}, govHeaders())
	if er2.Code != http.StatusOK {
		t.Fatalf("erase apos release devia dar 200, veio %d", er2.Code)
	}
	var resp2 dsarResponse
	_ = json.Unmarshal(er2.Body.Bytes(), &resp2)
	if resp2.Blocked || resp2.Status != "erased" {
		t.Fatalf("apos release o erase devia SUCEDER, veio %+v", resp2)
	}
	if _, ok := node.DSARVault.Key(ref); ok {
		t.Fatal("apos release+erase a KEK devia estar destruida")
	}
}

// -------------------------------------------------------------------------------------
// (CA5) AUTORIZAÇÃO — sem/forjada credencial ⇒ recusado; válida ⇒ aceite (dois sentidos).
// -------------------------------------------------------------------------------------

func TestLegalHoldRoutesRequireStrongCredential(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	for _, route := range []string{"/dsar/hold", "/dsar/release"} {
		anon := postReq(h, route, holdRequestWire{SubjectID: "subject-auth"}, nil)
		if anon.Code != http.StatusForbidden {
			t.Fatalf("%s anonimo devia dar 403, veio %d", route, anon.Code)
		}
		bad := postReq(h, route, holdRequestWire{SubjectID: "subject-auth"},
			map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard})
		if bad.Code != http.StatusForbidden {
			t.Fatalf("%s com board desconhecido (forjado) devia dar 403, veio %d", route, bad.Code)
		}
	}
	// Um hold não-autenticado NÃO foi aplicado.
	if node.DSARHolds.HeldSubject("subject-auth") {
		t.Fatal("um hold nao-autenticado NAO devia ter sido aplicado")
	}
	// Com credencial válida ⇒ aceite e aplicado.
	ok := postReq(h, "/dsar/hold", holdRequestWire{RequestID: "h", SubjectID: "subject-ok"}, govHeaders())
	if ok.Code != http.StatusOK {
		t.Fatalf("hold autenticado devia dar 200, veio %d (%s)", ok.Code, ok.Body.String())
	}
	if !node.DSARHolds.HeldSubject("subject-ok") {
		t.Fatal("hold autenticado devia ter sido aplicado")
	}
}

// TestExpireRouteRequiresStrongCredential prova a autorização da rota de expiração (dois sentidos)
// E que a expiração via rota é REAL (torna o conteúdo irrecuperável).
func TestExpireRouteRequiresStrongCredentialAndExpires(t *testing.T) {
	node := newRetentionNode(t)
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)
	const subject = "nhi:agent-expire-route"
	captureSynthetic(t, node, subject, "run-expire-route", "conteudo rota exp: EXPROUTE-999", "outExpRoute")
	sealed, _ := sealedContentOf(t, node, "run-expire-route")

	// ANÓNIMO ⇒ 403 e nada expira.
	anon := postReq(h, "/dsar/expire", nil, nil)
	if anon.Code != http.StatusForbidden {
		t.Fatalf("expire anonimo devia dar 403, veio %d", anon.Code)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("apos expire anonimo (recusado) o conteudo devia continuar decifravel: %v", err)
	}

	// AUTENTICADO ⇒ 200 e expira de facto.
	ok := postReq(h, "/dsar/expire", nil, govHeaders())
	if ok.Code != http.StatusOK {
		t.Fatalf("expire autenticado devia dar 200, veio %d (%s)", ok.Code, ok.Body.String())
	}
	var rep expireResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &rep); err != nil {
		t.Fatalf("resposta expire nao descodifica: %v", err)
	}
	if rep.Expired < 1 {
		t.Fatalf("expire via rota devia expirar >=1, veio %+v", rep)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("apos expire via rota o conteudo devia ser irrecuperavel: %v", err)
	}
}

// TestExpireRouteConcurrentPassesDoNotDoubleSeal prova a SERIALIZAÇÃO da rota de expiração
// (AOS-213): sob invocações CONCORRENTES de POST /dsar/expire, cada facto é selado UMA só vez na
// cadeia WORM de retenção — o guard CAS admite uma passagem activa e recusa as concorrentes com
// 409 (no-op). Falsificável: sem o guard, duas passagens que vissem o MESMO registo como não-visto
// poderiam selar DOIS eventos retention.expired para o mesmo facto (head > nº de factos). Correr
// com -race.
func TestExpireRouteConcurrentPassesDoNotDoubleSeal(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	// Semeia N titulares/runs distintos ⇒ N registos expiráveis distintos (um replay.captured por
	// run, EventID único ⇒ idempotency key única ⇒ exactamente N selos retention.expired esperados).
	const n = 4
	subjects := make([]string, n)
	sealedBlobs := make([][]byte, n)
	for i := 0; i < n; i++ {
		subj := "nhi:agent-conc-" + string(rune('a'+i))
		runID := "run-conc-" + string(rune('a'+i))
		subjects[i] = subj
		captureSynthetic(t, node, subj, runID, "conteudo concorrente CONC-"+string(rune('A'+i)), "outConc")
		blob, gotSubj := sealedContentOf(t, node, runID)
		if gotSubj != subj {
			t.Fatalf("conteudo nao selado sob o titular: %q != %q", gotSubj, subj)
		}
		sealedBlobs[i] = blob
	}

	// FOGO CONCORRENTE: mais invocações do que registos, todas em paralelo.
	const fired = 12
	var wg sync.WaitGroup
	codes := make([]int, fired)
	expiredByResp := make([]int, fired)
	start := make(chan struct{})
	for i := 0; i < fired; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // alinha o arranque para maximizar a sobreposição
			rec := postReq(h, "/dsar/expire", nil, govHeaders())
			codes[idx] = rec.Code
			if rec.Code == http.StatusOK {
				var rep expireResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &rep); err == nil {
					expiredByResp[idx] = rep.Expired
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// Cada resposta é 200 (passagem que correu) OU 409 (guard recusou a concorrente). Nunca 5xx.
	got200, got409 := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			got200++
		case http.StatusConflict:
			got409++
		default:
			t.Fatalf("codigo inesperado sob concorrencia: %d", c)
		}
	}
	if got200 == 0 {
		t.Fatal("esperava pelo menos uma passagem 200 (nao-vacuo)")
	}

	// INVARIANTE CENTRAL: a cadeia WORM de retenção tem EXACTAMENTE n selos — nenhum facto foi
	// selado duas vezes, apesar das passagens concorrentes.
	head, err := node.WORM.Head(ctx, retentionPartition)
	if err != nil {
		t.Fatalf("Head retencao: %v", err)
	}
	if head != n {
		t.Fatalf("esperava EXACTAMENTE %d selos retention.expired (um por facto), veio %d (duplicacao sob concorrencia?)", n, head)
	}
	if err := audit.Verify(ctx, node.WORM, retentionPartition, 1, head); err != nil {
		t.Fatalf("hash-chain de retencao NAO valida apos passagens concorrentes: %v", err)
	}

	// A soma dos Expired reportados pelas passagens 200 iguala n (cada facto contado uma vez no
	// total — nenhuma passagem re-expirou um facto ja expirado por outra).
	totalExpired := 0
	for _, e := range expiredByResp {
		totalExpired += e
	}
	if totalExpired != n {
		t.Fatalf("soma de Expired das passagens devia ser %d, veio %d", n, totalExpired)
	}

	// Todos os titulares ficam IRRECUPERÁVEIS (apagamento real via a expiração).
	for i, subj := range subjects {
		if _, err := audit.OpenContent(node.DSARVault, subj, sealedBlobs[i]); !errors.Is(err, audit.ErrDecrypt) {
			t.Fatalf("titular %q devia ficar irrecuperavel apos expiracao, deu: %v", subj, err)
		}
	}
}

// -------------------------------------------------------------------------------------
// FAIL-CLOSED — 501 sem gate soberano; SELO WORM sem PII.
// -------------------------------------------------------------------------------------

func TestLegalHoldRoutesOffWithoutSovereignty(t *testing.T) {
	node := newTestNode(t, &countingModel{}) // sem BoardRegions ⇒ readGov nil
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	for _, route := range []string{"/dsar/hold", "/dsar/release", "/dsar/expire"} {
		rec := postReq(h, route, holdRequestWire{SubjectID: "x"}, nil)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s sem gate soberano devia dar 501, veio %d", route, rec.Code)
		}
	}
}

func TestLegalHoldSealsWORMWithoutPII(t *testing.T) {
	ctx := context.Background()
	node := newGovNode(t, &countingModel{})
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	// HOLD por titular E partição (ambos opacos).
	rec := postReq(h, "/dsar/hold",
		holdRequestWire{RequestID: "h", SubjectID: "subj-seal", Partition: "part-seal"}, govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("hold devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// RELEASE — segunda entrada na cadeia.
	rel := postReq(h, "/dsar/release", holdRequestWire{RequestID: "r", SubjectID: "subj-seal"}, govHeaders())
	if rel.Code != http.StatusOK {
		t.Fatalf("release devia dar 200, veio %d", rel.Code)
	}

	head, err := node.WORM.Head(ctx, legalHoldPartition)
	if err != nil || head < 2 {
		t.Fatalf("legal hold devia selar hold+release (>=2), head=%d err=%v", head, err)
	}
	if err := audit.Verify(ctx, node.WORM, legalHoldPartition, 1, head); err != nil {
		t.Fatalf("cadeia de legal hold NAO verifica: %v", err)
	}
	// Sem PII: nenhum selo carrega PayloadRef.
	assertNoPIIInPartition(t, node.WORM, legalHoldPartition)
	// Os selos gravam o verbo esperado.
	recs, _ := node.WORM.Read(ctx, legalHoldPartition, 1, head)
	if recs[0].Capability != capLegalHoldPlace {
		t.Fatalf("1o selo devia ser %q, veio %q", capLegalHoldPlace, recs[0].Capability)
	}
	if recs[1].Capability != capLegalHoldRelease {
		t.Fatalf("2o selo devia ser %q, veio %q", capLegalHoldRelease, recs[1].Capability)
	}
}

// TestLegalHoldRejectsNonPseudonymTarget prova a defesa em profundidade: um subject_id/partition
// com forma de PII é rejeitado (400) ANTES de tocar o hold ou o WORM.
func TestLegalHoldRejectsNonPseudonymTarget(t *testing.T) {
	ctx := context.Background()
	node := newGovNode(t, &countingModel{})
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	for _, bad := range []string{"alice@example.com", "Ana Maria", "junk/with slash"} {
		rec := postReq(h, "/dsar/hold", holdRequestWire{RequestID: "b", SubjectID: bad}, govHeaders())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("subject_id nao-pseudonimo %q devia dar 400, veio %d", bad, rec.Code)
		}
	}
	// Sem alvo ⇒ 400.
	none := postReq(h, "/dsar/hold", holdRequestWire{RequestID: "n"}, govHeaders())
	if none.Code != http.StatusBadRequest {
		t.Fatalf("hold sem alvo devia dar 400, veio %d", none.Code)
	}
	// A partição de legal hold não ganhou nenhum selo.
	if head, _ := node.WORM.Head(ctx, legalHoldPartition); head != 0 {
		t.Fatalf("um hold rejeitado na fronteira NAO devia selar nada, head=%d", head)
	}
}
