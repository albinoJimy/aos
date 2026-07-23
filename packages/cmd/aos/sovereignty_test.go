package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-172 (E7) — testes de SOBERANIA/CONFORMIDADE do read-path do nó. Cada teste é
// NÃO-VACUOSO: prova o PERMIT e o DENY (D7), o selo verificável e a negação fail-closed
// quando o WORM não sela (D6), e o crypto-shredding + preservação sob legal hold (DSAR).
// Nenhum selo/resposta carrega PII. Correr SEMPRE com -race.

const (
	govBoard    = "board:aos-172"
	govRegion   = "eu"
	govReader   = "nhi:reader-172"
	govBadBoard = "board:desconhecido-999"
)

// newGovNode compõe um nó de teste COM soberania de leitura ligada (registo board→região
// DEMO-GRADE) e o modelo dado. O fluxo DSAR é composto pelo Bootstrap.
func newGovNode(t *testing.T, model agentruntime.ModelClient) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// govHeaders devolve os headers de leitura de um leitor AUTORIZADO (board conhecido).
func govHeaders() map[string]string {
	return map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBoard}
}

// getReq faz um GET ao handler com os headers dados.
func getReq(h http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postReq faz um POST JSON ao handler com os headers dados.
func postReq(h http.Handler, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(http.MethodPost, target, r)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// submitAndWait submete um run e espera que termine (para GET devolver o desfecho).
func submitAndWait(t *testing.T, svc *NodeService, runID string) {
	t.Helper()
	if err := svc.Submit(context.Background(), svcGoal(runID, "")); err != nil {
		t.Fatalf("Submit(%s): %v", runID, err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(waitCtx, runID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%t err=%v", runID, ok, err)
	}
}

// ---------------------------------------------------------------------------
// (D7) READ-PATH SOBERANO FAIL-CLOSED — permit E deny, não-enumerável.
// ---------------------------------------------------------------------------

// TestReadPathSovereignAuthorized prova o PERMIT: um leitor com board AUTORIZADO lê o
// desfecho (200) — a authz por-chamador que AOS-167 deferiu está ligada e NÃO barra o
// legítimo.
func TestReadPathSovereignAuthorized(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-d7-ok")

	rec := getReq(h, "/runs/run-d7-ok", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("leitor autorizado devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var st runStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("resposta 200 nao descodifica: %v", err)
	}
	if st.Status != "completed" {
		t.Fatalf("desfecho devia ser completed, veio %q", st.Status)
	}
}

// TestReadPathSovereignDeniedAndNonEnumerable prova o DENY fail-closed E a
// não-enumerabilidade: um board DESCONHECIDO nega (404) e a resposta é BYTE-A-BYTE igual à
// de um run inexistente lido por um leitor autorizado — um leitor não autorizado não
// distingue "existe mas nao e seu" de "nunca existiu", nem vê PII.
func TestReadPathSovereignDeniedAndNonEnumerable(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-d7-secret")

	// Board desconhecido a ler um run EXISTENTE ⇒ 404 (deny fail-closed).
	denied := getReq(h, "/runs/run-d7-secret", map[string]string{
		HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard,
	})
	if denied.Code != http.StatusNotFound {
		t.Fatalf("board desconhecido devia dar 404, veio %d (%s)", denied.Code, denied.Body.String())
	}
	// Leitor AUTORIZADO a ler um run INEXISTENTE ⇒ 404 (mesmo desfecho).
	missing := getReq(h, "/runs/run-nao-existe", govHeaders())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("run inexistente devia dar 404, veio %d", missing.Code)
	}
	// NÃO-ENUMERÁVEL: as duas negações são indistinguíveis (mesmo status e corpo).
	if denied.Body.String() != missing.Body.String() {
		t.Fatalf("resposta de deny (%q) difere da de inexistente (%q) — enumeravel!",
			denied.Body.String(), missing.Body.String())
	}
	// A negação NÃO revela o RunID nem PII.
	if strings.Contains(denied.Body.String(), "run-d7-secret") {
		t.Fatalf("resposta de deny vaza o RunID: %q", denied.Body.String())
	}
}

// TestReadPathSovereignDeniedMissingCredential prova que a AUSÊNCIA da credencial de leitura
// (principal e/ou board) é NEGADA fail-closed — nunca uma leitura anónima resolve uma região.
func TestReadPathSovereignDeniedMissingCredential(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-d7-anon")

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"sem headers", nil},
		{"so principal", map[string]string{HeaderReaderPrincipal: govReader}},
		{"so board", map[string]string{HeaderReaderBoard: govBoard}},
		{"board vazio", map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: "   "}},
	}
	for _, c := range cases {
		rec := getReq(h, "/runs/run-d7-anon", c.headers)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: leitura sem credencial devia dar 404, veio %d", c.name, rec.Code)
		}
	}
}

// TestReadPathLegacyWhenSovereigntyOff prova que um nó SEM soberania de leitura configurada
// mantém o read-path LEGADO (sem authz por-chamador): a regra é fixa, a topologia é
// condicional ao provisioning (deferido). Um GET sem headers ainda dá 200.
func TestReadPathLegacyWhenSovereigntyOff(t *testing.T) {
	node := newTestNode(t, &countingModel{}) // sem BoardRegions ⇒ readGov nil
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-legacy")

	rec := getReq(h, "/runs/run-legacy", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read-path legado devia dar 200 sem headers, veio %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// (D6) SELO WORM DE LEITURA SENSÍVEL — verificável, sem PII, fail-closed.
// ---------------------------------------------------------------------------

// assertNoPIIInPartition percorre a cadeia da partição e exige que NENHUM registo carregue
// um PayloadRef (a via da PII no audit). Os selos de conformidade são metadados puros.
func assertNoPIIInPartition(t *testing.T, worm audit.Store, partition string) {
	t.Helper()
	head, err := worm.Head(context.Background(), partition)
	if err != nil {
		t.Fatalf("Head(%s): %v", partition, err)
	}
	recs, err := worm.Read(context.Background(), partition, 1, head)
	if err != nil {
		t.Fatalf("Read(%s): %v", partition, err)
	}
	for _, r := range recs {
		if r.PayloadRef != nil {
			t.Fatalf("selo em %s (seq %d) carrega PayloadRef — PII num selo de conformidade!", partition, r.AuditSeq)
		}
	}
}

// TestSensitiveReadSealsVerifiableWORM prova D6: uma leitura sensível (GET de desfecho) emite
// UM selo WORM VERIFICÁVEL (Append + Verify da cadeia) com principal/run/região e SEM PII —
// a leitura deixa de ser silenciosa.
func TestSensitiveReadSealsVerifiableWORM(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-d6")

	part := readAuditPartition("run-d6")
	// ANTES da leitura a partição de leitura está VAZIA (não-vacuosidade: o selo é do read).
	if head, _ := node.WORM.Head(context.Background(), part); head != 0 {
		t.Fatalf("particao de leitura devia estar vazia antes do GET, head=%d", head)
	}

	rec := getReq(h, "/runs/run-d6", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET autorizado devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// UM selo foi encadeado e a cadeia VERIFICA.
	head, err := node.WORM.Head(context.Background(), part)
	if err != nil || head < 1 {
		t.Fatalf("apos GET devia haver >=1 selo, head=%d err=%v", head, err)
	}
	if err := audit.Verify(context.Background(), node.WORM, part, 1, head); err != nil {
		t.Fatalf("cadeia do selo de leitura NAO verifica: %v", err)
	}
	// O selo regista QUEM/QUE run/REGIÃO — sem PII.
	seal, ok, err := node.WORM.At(context.Background(), part, 1)
	if err != nil || !ok {
		t.Fatalf("At(%s,1): ok=%t err=%v", part, ok, err)
	}
	if seal.Principal.NHIID != govReader {
		t.Fatalf("selo devia registar o leitor %q, veio %q", govReader, seal.Principal.NHIID)
	}
	if seal.RunID != "run-d6" || seal.Resource.Value != "run-d6" {
		t.Fatalf("selo devia ligar ao run, veio RunID=%q resource=%q", seal.RunID, seal.Resource.Value)
	}
	if seal.Resource.Region != govRegion {
		t.Fatalf("selo devia registar a regiao resolvida %q, veio %q", govRegion, seal.Resource.Region)
	}
	if seal.Capability != capReadOutcome {
		t.Fatalf("capability do selo devia ser %q, veio %q", capReadOutcome, seal.Capability)
	}
	// O board (identificador de governação) vai na obrigação; nunca PayloadRef.
	foundBoard := false
	for _, ob := range seal.Obligations {
		if ob.Type == readBoardObligation {
			for _, f := range ob.Fields {
				if f == govBoard {
					foundBoard = true
				}
			}
		}
	}
	if !foundBoard {
		t.Fatalf("selo devia carregar o board na obrigacao %q", readBoardObligation)
	}
	assertNoPIIInPartition(t, node.WORM, part)
}

// TestSensitiveTrajectoryReadSeals prova que a ABERTURA do stream SSE de trajectória sela a
// leitura no WORM (D6) — a leitura tempo-real deixa de ser silenciosa.
func TestSensitiveTrajectoryReadSeals(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	_, h := newAPI(t, node)
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Semeia eventos no stream do run (o stream existe ⇒ backfill, sem exigir posse).
	appendTraj(t, node.EventStore, "run-d6-traj", "s1")

	part := readAuditPartition("run-d6-traj")
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/run-d6-traj/trajectory", nil)
	req.Header.Set(HeaderReaderPrincipal, govReader)
	req.Header.Set(HeaderReaderBoard, govBoard)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET trajectory: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("trajectory autorizada devia dar 200, veio %d", resp.StatusCode)
	}
	// Lê o primeiro evento (garante que o handler passou o selo + commitSSE).
	br := bufio.NewReader(resp.Body)
	if _, err := readSSE(br); err != nil {
		resp.Body.Close()
		cancel()
		t.Fatalf("readSSE: %v", err)
	}
	// O selo de trajectória foi encadeado.
	waitUntil(t, 2*time.Second, func() bool {
		head, _ := node.WORM.Head(context.Background(), part)
		return head >= 1
	}, "selo de leitura de trajectoria")
	cancel()
	resp.Body.Close()

	seal, ok, err := node.WORM.At(context.Background(), part, 1)
	if err != nil || !ok {
		t.Fatalf("At(%s,1): ok=%t err=%v", part, ok, err)
	}
	if seal.Capability != capReadTrajectory {
		t.Fatalf("capability do selo de trajectoria devia ser %q, veio %q", capReadTrajectory, seal.Capability)
	}
	if seal.Principal.NHIID != govReader || seal.Resource.Region != govRegion {
		t.Fatalf("selo devia registar leitor/regiao, veio %q/%q", seal.Principal.NHIID, seal.Resource.Region)
	}
	assertNoPIIInPartition(t, node.WORM, part)
}

// sealFailWORM é um audit.Store cujo Append FALHA sempre — para provar que, se o WORM não
// selar a leitura sensível, a leitura é NEGADA fail-closed (D6 é pré-condição, não telemetria).
type sealFailWORM struct{}

func (sealFailWORM) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("worm indisponivel")
}
func (sealFailWORM) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (sealFailWORM) Head(context.Context, string) (uint64, error) { return 0, nil }
func (sealFailWORM) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}

// TestSensitiveReadDeniedWhenSealFails prova o CONTRASTE com AOS-173 (fail-open da
// observabilidade): se o selo WORM da leitura sensível FALHA, a leitura é NEGADA fail-closed
// e o desfecho NÃO é servido.
func TestSensitiveReadDeniedWhenSealFails(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion})
	// O gate de leitura usa um WORM que FALHA o Append (precedência sobre o auto-wiring).
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, sealFailWORM{}))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	submitAndWait(t, svc, "run-d6-fail")

	rec := getReq(h, "/runs/run-d6-fail", govHeaders())
	if rec.Code == http.StatusOK {
		t.Fatalf("leitura sensivel com WORM em falha devia ser NEGADA, veio 200: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("negacao por selo em falha devia dar 503, veio %d", rec.Code)
	}
	// O desfecho NÃO foi servido (fail-closed): o corpo não é a fotografia do run.
	if strings.Contains(rec.Body.String(), "completed") {
		t.Fatalf("resposta negada NAO devia conter o desfecho, veio %q", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// (DSAR) DSAR / CRYPTO-SHREDDING — apagamento, legal hold, idempotência.
// ---------------------------------------------------------------------------

// seedPII provisiona a KEK de um titular no vault DEMO-GRADE do nó e devolve a sua KeyRef —
// simula PII cifrada por-titular. Após o shred, a KEK desaparece e a PII fica irrecuperável.
func seedPII(t *testing.T, node *Node, subject string) string {
	t.Helper()
	_, ref, err := node.DSARVault.EnsureKey(subject)
	if err != nil {
		t.Fatalf("EnsureKey(%s): %v", subject, err)
	}
	if _, ok := node.DSARVault.Key(ref); !ok {
		t.Fatalf("KEK de %s devia existir apos EnsureKey", subject)
	}
	return ref
}

// TestDSARErasesAndSeals prova o crypto-shredding: um pedido de apagamento destrói a KEK do
// titular (dados ilegíveis) e sela received/key_destroyed no WORM — sem PII.
func TestDSARErasesAndSeals(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}

	ref := seedPII(t, node, "subject-erase")

	rec := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "req-1", SubjectID: "subject-erase"}, govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("DSAR erase devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp dsarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta DSAR nao descodifica: %v", err)
	}
	if resp.Status != "erased" || resp.Blocked {
		t.Fatalf("DSAR devia ser 'erased' nao-bloqueado, veio status=%q blocked=%t", resp.Status, resp.Blocked)
	}
	// CRYPTO-SHREDDING: a KEK foi destruída ⇒ a PII fica ILEGÍVEL (a chave já não existe).
	if _, ok := node.DSARVault.Key(ref); ok {
		t.Fatalf("apos DSAR a KEK devia estar destruida (dados ilegiveis)")
	}
	// received + key_destroyed selados e a cadeia VERIFICA; sem PII.
	part := "governance.dsar"
	head, err := node.WORM.Head(context.Background(), part)
	if err != nil || head < 2 {
		t.Fatalf("DSAR devia selar received+key_destroyed (>=2), head=%d err=%v", head, err)
	}
	if err := audit.Verify(context.Background(), node.WORM, part, 1, head); err != nil {
		t.Fatalf("cadeia DSAR NAO verifica: %v", err)
	}
	recs, _ := node.WORM.Read(context.Background(), part, 1, head)
	sawDestroyed := false
	for _, r := range recs {
		if r.Capability == dsar.EventKeyDestroyed {
			sawDestroyed = true
		}
	}
	if !sawDestroyed {
		t.Fatalf("faltou o evento %q selado", dsar.EventKeyDestroyed)
	}
	assertNoPIIInPartition(t, node.WORM, part)
}

// TestDSARBlockedByLegalHold prova a preservação P0: um titular sob LEGAL HOLD NÃO é apagado
// (fail-closed do apagamento) e o bloqueio é selado — a KEK sobrevive.
func TestDSARBlockedByLegalHold(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	ref := seedPII(t, node, "subject-hold")
	node.DSARHolds.HoldSubject("subject-hold") // preservação legal

	rec := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "req-hold", SubjectID: "subject-hold"}, govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("DSAR bloqueado devia devolver 200 (desfecho legitimo), veio %d", rec.Code)
	}
	var resp dsarResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Blocked || resp.Status != "blocked" {
		t.Fatalf("titular sob hold devia ser BLOQUEADO, veio status=%q blocked=%t", resp.Status, resp.Blocked)
	}
	// A KEK NÃO foi destruída (preservada sob hold).
	if _, ok := node.DSARVault.Key(ref); !ok {
		t.Fatalf("sob legal hold a KEK devia SOBREVIVER (nao apagada)")
	}
	// dsar.blocked foi selado.
	recs, _ := node.WORM.Read(context.Background(), "governance.dsar", 1, 1<<20)
	sawBlocked := false
	for _, r := range recs {
		if r.Capability == dsar.EventBlocked {
			sawBlocked = true
		}
	}
	if !sawBlocked {
		t.Fatalf("faltou o evento %q selado", dsar.EventBlocked)
	}
}

// TestDSARIdempotent prova que re-submeter um titular já apagado NÃO falha (o shred de uma
// chave ausente é no-op idempotente).
func TestDSARIdempotent(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	seedPII(t, node, "subject-idem")
	first := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "r1", SubjectID: "subject-idem"}, govHeaders())
	if first.Code != http.StatusOK {
		t.Fatalf("1o DSAR devia dar 200, veio %d", first.Code)
	}
	second := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "r2", SubjectID: "subject-idem"}, govHeaders())
	if second.Code != http.StatusOK {
		t.Fatalf("2o DSAR (idempotente) devia dar 200, veio %d (%s)", second.Code, second.Body.String())
	}
	var resp dsarResponse
	_ = json.Unmarshal(second.Body.Bytes(), &resp)
	if resp.Status != "erased" {
		t.Fatalf("2o DSAR devia continuar 'erased', veio %q", resp.Status)
	}
}

// TestDSAREndpointRequiresAuth prova que o endpoint DSAR é fail-closed: sem credencial de
// governação (principal+board) ⇒ 403; board desconhecido ⇒ 403.
func TestDSAREndpointRequiresAuth(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)
	seedPII(t, node, "subject-auth")

	anon := postReq(h, "/dsar/erase", dsarRequestWire{SubjectID: "subject-auth"}, nil)
	if anon.Code != http.StatusForbidden {
		t.Fatalf("DSAR anonimo devia dar 403, veio %d", anon.Code)
	}
	bad := postReq(h, "/dsar/erase", dsarRequestWire{SubjectID: "subject-auth"},
		map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard})
	if bad.Code != http.StatusForbidden {
		t.Fatalf("DSAR com board desconhecido devia dar 403, veio %d", bad.Code)
	}
	// A KEK NÃO foi tocada (nenhum apagamento não-autenticado).
	if _, ok := node.DSARVault.Key(audit.KeyRefFor("subject-auth")); !ok {
		t.Fatalf("um DSAR nao-autenticado NAO devia ter apagado a KEK")
	}
}

// TestDSAREndpointOffWithoutSovereignty prova que o endpoint DSAR está DESLIGADO (501) num nó
// SEM o gate soberano composto — a governança de identidade de leitura é pré-requisito.
func TestDSAREndpointOffWithoutSovereignty(t *testing.T) {
	node := newTestNode(t, &countingModel{}) // sem BoardRegions ⇒ readGov nil
	svc, _ := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	h, _ := NewAPIHandler(svc, node)

	rec := postReq(h, "/dsar/erase", dsarRequestWire{SubjectID: "x"}, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("DSAR sem gate soberano devia dar 501, veio %d", rec.Code)
	}
}
