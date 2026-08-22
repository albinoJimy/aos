package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
	identity "github.com/aos-ref/platform/identity"
)

// Estes testes provam a superfície de rede de AOS-166 SEM afrouxar nenhuma invariante: o
// canal de controlo é AUTENTICADO na fronteira real (assinatura ed25519 inválida/replay/
// stale ⇒ sem efeito), o ingresso tem admission + limite de corpo, e o bind não-loopback é
// RECUSADO sem authn. Cada asserção é não-vacuosa: distingue o comportamento aceite do
// recusado, e verifica o EFEITO (a correcção chega — ou NÃO chega — ao SteerChannel).

const apiOperatorID = "human:operator-api"

// newAPINode compõe um Node de referência com o modelo dado e, opcionalmente, um operador
// ed25519 registado no canal de controlo (para os testes de steer autenticado). Devolve o
// nó e a chave PRIVADA do operador (que vive só do lado do emissor — a API é non-signing).
func newAPINode(t *testing.T, model agentruntime.ModelClient, withOperator bool) (*Node, ed25519.PrivateKey) {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.SteerClock = tnClock() // frescura determinística
	var opPriv ed25519.PrivateKey
	if withOperator {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		cfg.Operators = map[string]ed25519.PublicKey{apiOperatorID: pub}
		opPriv = priv
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return node, opPriv
}

// newAPI compõe serviço + handler sobre um nó, com as opções de API dadas.
func newAPI(t *testing.T, node *Node, opts ...APIOption) (*NodeService, http.Handler) {
	t.Helper()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node, opts...)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return svc, h
}

// postJSON serializa body e faz um pedido ao handler, devolvendo o recorder.
func postJSON(h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// steerBody constrói o corpo JSON de um steer com o emitter assinado e a correcção base64.
func steerBody(em control.Emitter, correction []byte) map[string]any {
	return map[string]any{
		"emitter": map[string]any{
			"id":        em.ID,
			"signature": base64.StdEncoding.EncodeToString(em.Signature),
			"nonce":     base64.StdEncoding.EncodeToString(em.Nonce),
			"issued_at": em.IssuedAt.Format(time.RFC3339Nano),
		},
		"payload": base64.StdEncoding.EncodeToString(correction),
	}
}

// ---------------------------------------------------------------------------
// (a) POST /runs válido ⇒ 201 + RunID e o run é hospedado/executado.
// ---------------------------------------------------------------------------

func TestAPISubmitValidHostsRun(t *testing.T) {
	model := &countingModel{}
	node, _ := newAPINode(t, model, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-api-1",
		"objective":     "trabalho de referencia",
		"principal_nhi": "nhi:run-api-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs valido devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta 201 nao descodifica: %v", err)
	}
	if resp.RunID != "run-api-1" {
		t.Fatalf("resposta devia trazer o RunID, veio %q", resp.RunID)
	}

	// O run foi REALMENTE hospedado e executado (não-vacuoso).
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oc, ok, werr := svc.Wait(waitCtx, "run-api-1")
	if werr != nil || !ok {
		t.Fatalf("run devia ter sido hospedado: ok=%v err=%v", ok, werr)
	}
	if !oc.Result.Terminated {
		t.Fatalf("run devia ter concluido, veio %+v", oc)
	}

	// GET reflecte o desfecho.
	grec := postJSON(h, "GET", "/runs/run-api-1", nil)
	if grec.Code != http.StatusOK {
		t.Fatalf("GET do run terminado devia dar 200, veio %d", grec.Code)
	}
	var st runStateResponse
	if err := json.Unmarshal(grec.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET nao descodifica: %v", err)
	}
	if st.Status != "completed" || !st.Terminated {
		t.Fatalf("estado devia ser completed/terminated, veio %+v", st)
	}
}

// POST /runs sem run_id ⇒ 400 (fail-closed).
func TestAPISubmitMissingRunID(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/runs", map[string]any{"objective": "sem run id"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /runs sem run_id devia dar 400, veio %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// (b) steer com emitter ed25519 VÁLIDO ⇒ 202 e a correcção chega ao SteerChannel.
// ---------------------------------------------------------------------------

func TestAPISteerValidReachesChannel(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	const runID = "run-steer-api"
	correction := []byte("aperta o ambito ao ticket")
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())

	rec := postJSON(h, "POST", "/runs/"+runID+"/steer", steerBody(em, correction))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("steer valido devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// EFEITO: a correcção chegou REALMENTE ao SteerChannel do nó (não-vacuoso).
	got, ok := node.Steer.PendingCorrection(runID)
	if !ok || string(got) != string(correction) {
		t.Fatalf("correccao pendente = (%q,%v); quer (%q,true) — o steer autenticado devia ter chegado ao canal", got, ok, correction)
	}
}

// ---------------------------------------------------------------------------
// (c) steer com assinatura INVÁLIDA / replay / stale ⇒ 403 SEM efeito.
// ---------------------------------------------------------------------------

func TestAPISteerInvalidSignatureRejected(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	const runID = "run-steer-bad"
	correction := []byte("correccao maliciosa")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())
	// ADULTERA a assinatura (um bit): deixa de validar como ed25519.
	em.Signature[0] ^= 0xFF

	rec := postJSON(h, "POST", "/runs/"+runID+"/steer", steerBody(em, correction))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("steer com assinatura invalida devia dar 403, veio %d", rec.Code)
	}
	if _, ok := node.Steer.PendingCorrection(runID); ok {
		t.Fatal("assinatura invalida NAO devia ter efeito no canal (correccao nao pode ficar pendente)")
	}
}

func TestAPISteerReplayRejected(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	const runID = "run-steer-replay"
	correction := []byte("primeira correccao")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())
	body := steerBody(em, correction)

	// 1ª vez: aceite.
	if rec := postJSON(h, "POST", "/runs/"+runID+"/steer", body); rec.Code != http.StatusAccepted {
		t.Fatalf("1o steer devia dar 202, veio %d", rec.Code)
	}
	// 2ª vez (MESMO nonce): replay ⇒ 403 (o nonce durável foi consumido).
	if rec := postJSON(h, "POST", "/runs/"+runID+"/steer", body); rec.Code != http.StatusForbidden {
		t.Fatalf("steer replayado (mesmo nonce) devia dar 403, veio %d", rec.Code)
	}
}

func TestAPISteerStaleRejected(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	const runID = "run-steer-stale"
	correction := []byte("correccao velha")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	// issued_at 10min ANTES do relógio do nó (TTL default 5min) ⇒ fora da janela ⇒ stale.
	stale := tnClock()().Add(-10 * time.Minute)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, stale)

	rec := postJSON(h, "POST", "/runs/"+runID+"/steer", steerBody(em, correction))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("steer stale (fora da frescura) devia dar 403, veio %d", rec.Code)
	}
	if _, ok := node.Steer.PendingCorrection(runID); ok {
		t.Fatal("sinal stale NAO devia ter efeito no canal")
	}
}

// steer anónimo (sem emitter/assinatura) ⇒ 403 (NUNCA um POST de controlo anónimo aceite).
func TestAPISteerAnonymousRejected(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/runs/run-anon/steer", map[string]any{
		"emitter": map[string]any{"id": "", "signature": "", "nonce": "", "issued_at": tnClock()().Format(time.RFC3339Nano)},
		"payload": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Fatalf("steer anonimo devia ser recusado (403/400), veio %d", rec.Code)
	}
	if _, ok := node.Steer.PendingCorrection("run-anon"); ok {
		t.Fatal("steer anonimo NAO devia ter efeito")
	}
}

// ---------------------------------------------------------------------------
// (d) bind não-loopback sem authn ⇒ Serve RECUSA (erro); loopback OK.
// ---------------------------------------------------------------------------

func TestAPIGuardBindPure(t *testing.T) {
	cases := []struct {
		addr        string
		authed      bool
		wantRefused bool
	}{
		{"0.0.0.0:8080", false, true},      // todas as interfaces, sem authn ⇒ RECUSA
		{"192.168.1.10:8080", false, true}, // IP público, sem authn ⇒ RECUSA
		{":8080", false, true},             // host vazio ⇒ todas as interfaces ⇒ RECUSA
		{"host.example:8080", false, true}, // hostname não confirmável ⇒ conservador ⇒ RECUSA
		{"127.0.0.1:8080", false, false},   // loopback ⇒ OK mesmo sem authn
		{"localhost:8080", false, false},   // loopback nominal ⇒ OK
		{"[::1]:8080", false, false},       // loopback IPv6 ⇒ OK
		{"0.0.0.0:8080", true, false},      // authn ligada ⇒ qualquer bind OK
		{"192.168.1.10:8080", true, false}, // authn ligada ⇒ OK
	}
	for _, c := range cases {
		err := guardBind(c.addr, c.authed)
		if c.wantRefused && err == nil {
			t.Errorf("guardBind(%q, authed=%v) devia RECUSAR, veio nil", c.addr, c.authed)
		}
		if !c.wantRefused && err != nil {
			t.Errorf("guardBind(%q, authed=%v) devia permitir, veio %v", c.addr, c.authed, err)
		}
	}
}

// Serve de um APIServer sobre um nó NÃO-autenticado (SteerAuth removido) a um addr
// não-loopback RECUSA com ErrRefuseNonLoopbackBind (o Listen nem acontece). Loopback OK.
func TestAPIServeBindGuardrail(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}

	// Nó autenticado (real): controlAuthenticated == true.
	authedSrv, err := NewAPIServer(svc, node)
	if err != nil {
		t.Fatalf("NewAPIServer: %v", err)
	}
	if !authedSrv.controlAuthenticated() {
		t.Fatal("um no com SteerAuth e identidade real devia ser controlAuthenticated")
	}

	// Simula um nó NÃO-autenticado: canal de controlo sem authenticator.
	unauth := *node
	unauth.SteerAuth = nil
	unauthSrv, err := NewAPIServer(svc, &unauth)
	if err != nil {
		t.Fatalf("NewAPIServer (unauth): %v", err)
	}
	if unauthSrv.controlAuthenticated() {
		t.Fatal("um no sem SteerAuth NAO devia ser controlAuthenticated")
	}

	// Bind não-loopback sem authn ⇒ Serve RECUSA (erro, não bloqueia num Listen).
	if err := unauthSrv.Serve("0.0.0.0:0"); err == nil {
		t.Fatal("Serve nao-loopback sem authn devia RECUSAR com erro")
	} else if !isRefuseBindErr(err) {
		t.Fatalf("Serve devia dar ErrRefuseNonLoopbackBind, veio %v", err)
	}

	// Bind loopback OK mesmo sem authn: o listener abre (fecha-se de imediato).
	ln, err := unauthSrv.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback sem authn devia abrir, veio %v", err)
	}
	_ = ln.Close()
}

func isRefuseBindErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "RECUSADO")
}

// ---------------------------------------------------------------------------
// (e) corpo gigante ⇒ 413 (MaxBytesReader).
// ---------------------------------------------------------------------------

func TestAPIBodyTooLarge(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node, WithMaxBodyBytes(1024))

	// Objective muito maior que o tecto de 1 KiB.
	huge := strings.Repeat("A", 8192)
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-huge",
		"principal_nhi": "nhi:run-huge",
		"objective":     huge,
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo gigante devia dar 413, veio %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// (f) admission — excesso de submits ⇒ 429.
// ---------------------------------------------------------------------------

func TestAPIAdmissionRateLimited(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	// Bucket de 2 tokens SEM reabastecimento (relógio fixo) ⇒ o 3º submit é recusado.
	fixed := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	_, h := newAPI(t, node, WithRateLimit(0, 2), WithAPIClock(fixed))

	ok := 0
	limited := 0
	for i := 0; i < 3; i++ {
		rec := postJSON(h, "POST", "/runs", map[string]any{
			"run_id":        "run-adm-" + string(rune('a'+i)),
			"principal_nhi": "nhi:adm",
		})
		switch rec.Code {
		case http.StatusCreated:
			ok++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("submit %d: codigo inesperado %d", i, rec.Code)
		}
	}
	if ok != 2 || limited != 1 {
		t.Fatalf("admission: aceites=%d limitados=%d; queria 2/1 (token-bucket de burst 2)", ok, limited)
	}
}

// ---------------------------------------------------------------------------
// (f2) admission do PLANO DE CONTROLO — inundação de /steer ⇒ 429 antes da verificação.
// ---------------------------------------------------------------------------

// TestAPIControlPlaneRateLimited prova que o canal de CONTROLO (/steer,/pause,/approve) tem
// admission DEDICADA: com um bucket de controlo de burst 1 sem reabastecimento, o 2º sinal é
// recusado (429) ANTES de qualquer decode/ed25519.Verify — um atacante não o pode inundar a
// taxa ilimitada forçando uma verificação por pedido. Não-vacuoso: o 1º steer VÁLIDO é
// admitido (202) e o 2º (também válido, nonce fresco) só falha por causa do tecto de taxa.
func TestAPIControlPlaneRateLimited(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	fixed := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	// Bucket de controlo de 1 token SEM reabastecimento; o plano de dados fica com o default.
	_, h := newAPI(t, node, WithControlRateLimit(0, 1), WithAPIClock(fixed))

	const runID = "run-ctrl-rl"
	correction := []byte("aperta o ambito")
	sign := func() control.Emitter {
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		return integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, fixed())
	}

	// 1º steer válido ⇒ admitido (202).
	if rec := postJSON(h, "POST", "/runs/"+runID+"/steer", steerBody(sign(), correction)); rec.Code != http.StatusAccepted {
		t.Fatalf("1o steer valido devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// 2º steer válido (nonce fresco) ⇒ bucket de controlo esgotado ⇒ 429 antes da verificação.
	if rec := postJSON(h, "POST", "/runs/"+runID+"/steer", steerBody(sign(), correction)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2o steer (bucket de controlo esgotado) devia dar 429, veio %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// (f3) POST /runs de um run_id já conhecido ⇒ 201 idempotente (não 409 — sem oráculo).
// ---------------------------------------------------------------------------

// TestAPISubmitIdempotentNonEnumerable prova que uma RE-SUBMISSÃO do MESMO run_id devolve o
// MESMO 201 "accepted" que a submissão fresca — o status NÃO revela a existência do run a um
// chamador anónimo (fecha o oráculo de existência que o 409 constituía). O run não é
// re-hospedado nem o desfecho sobrescrito.
func TestAPISubmitIdempotentNonEnumerable(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	submit := func() *httptest.ResponseRecorder {
		return postJSON(h, "POST", "/runs", map[string]any{
			"run_id":        "run-idem",
			"principal_nhi": "nhi:run-idem",
		})
	}
	// 1ª submissão ⇒ 201, run hospedado.
	if rec := submit(); rec.Code != http.StatusCreated {
		t.Fatalf("1a submissao devia dar 201, veio %d", rec.Code)
	}
	// Espera o run terminar (fica retido em `completed`).
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(waitCtx, "run-idem"); werr != nil || !ok {
		t.Fatalf("run devia ter sido hospedado: ok=%v err=%v", ok, werr)
	}
	// Re-submissão do MESMO run_id (agora terminado/retido): MESMO 201 "accepted" — não 409.
	rec := submit()
	if rec.Code != http.StatusCreated {
		t.Fatalf("re-submissao idempotente devia dar 201 (nao 409 — sem oraculo de existencia), veio %d", rec.Code)
	}
	var resp submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Status != "accepted" {
		t.Fatalf("re-submissao devia devolver o mesmo corpo 'accepted', veio %q (err=%v)", rec.Body.String(), err)
	}
}

// ---------------------------------------------------------------------------
// (g) GET de RunID inexistente ⇒ 404 uniforme (== não-autorizado, não vaza existência).
// ---------------------------------------------------------------------------

func TestAPIGetUnknownUniform404(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "GET", "/runs/nao-existe-de-todo", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET de RunID inexistente devia dar 404, veio %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 nao descodifica: %v", err)
	}
	if body["error"] != "not found" {
		t.Fatalf("404 devia ser uniforme (\"not found\"), veio %q", body["error"])
	}
}

// ---------------------------------------------------------------------------
// (h) /approve — desligado (501) sem aprovadores; autoriza via o gate quando composto.
// ---------------------------------------------------------------------------

func TestAPIApproveDisabledWhenNoGate(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/runs/run-x/approve", map[string]any{
		"request": map[string]any{"request_id": "req-1", "preview": base64.StdEncoding.EncodeToString([]byte("efeito"))},
	})
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("/approve sem four-eyes composto devia dar 501, veio %d", rec.Code)
	}
}

func TestAPIApproveAuthorizesViaGate(t *testing.T) {
	// Nó com um aprovador composto (reversível: 1 aprovação basta).
	approverID := "human:approver-1"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
	}
	cfg.Approvers = []ApproverConfig{{
		Principal: approverID,
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassSafe.String()},
	}}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if node.FourEyes == nil {
		t.Fatal("o gate four-eyes devia ter sido composto")
	}
	_, h := newAPI(t, node)

	// Pedido reversível (ClassSafe, dual=false) + 1 perna assinada pelo aprovador.
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	req := integration.FourEyesRequest{
		RequestID:           "req-approve-1",
		Preview:             []byte("efeito exibido ao humano"),
		RiskClass:           risk.ClassSafe,
		DualControlRequired: false,
	}
	leg := integration.SignFourEyesLeg(priv, req, approverID, "sess-1", "cred-1", challenge, nil)

	body := map[string]any{
		"request": map[string]any{
			"request_id":            req.RequestID,
			"preview":               base64.StdEncoding.EncodeToString(req.Preview),
			"risk_class":            uint8(req.RiskClass),
			"dual_control_required": req.DualControlRequired,
		},
		"legs": []map[string]any{{
			"approver":   leg.Approver,
			"session":    leg.Session,
			"credential": leg.Credential,
			"challenge":  base64.StdEncoding.EncodeToString(leg.Challenge),
			"signature":  base64.StdEncoding.EncodeToString(leg.Signature),
		}},
	}
	semearPendente(t, node, "run-x", capReversivelDeTeste, []byte("efeito exibido ao humano"))
	rec := postJSON(h, "POST", "/runs/run-x/approve", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("/approve com perna valida devia autorizar (200), veio %d (%s)", rec.Code, rec.Body.String())
	}

	// Uma segunda submissão com o MESMO challenge ⇒ replay ⇒ 403 (não-vacuoso: o gate real
	// consome o challenge).
	rec2 := postJSON(h, "POST", "/runs/run-x/approve", body)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("/approve replayado (mesmo challenge) devia dar 403, veio %d", rec2.Code)
	}
}
