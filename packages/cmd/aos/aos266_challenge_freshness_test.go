package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
)

// AOS-266 (achado F10) — FRESCURA POR-CERIMÓNIA: prova de NÓ, ponta-a-ponta pela composição
// REAL do Bootstrap, de que a porta integration.WithChallengeIssuance passa a estar LIGADA e
// INDIVISÍVEL do endpoint de emissão. Distingue-se do estado DORMENTE histórico exercitando o
// caminho que só o modo issue-then-consume fecha: um challenge NÃO EMITIDO é recusado, e um
// challenge emitido é de USO-ÚNICO.

// aos266FreshnessConfig devolve uma config de nó com UM aprovador (reversível: 1 perna basta) e
// a frescura por-cerimónia LIGADA.
func aos266FreshnessConfig(t *testing.T, approverID string, pub ed25519.PublicKey) Config {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.Approvers = []ApproverConfig{{
		Principal: approverID,
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassSafe.String()},
	}}
	cfg.ChallengeIssuance = true
	return cfg
}

// issueChallenge pede ao endpoint POST /runs/{id}/challenge um challenge fresco para
// (request_id, approver) e devolve-o em bytes. Falha o teste se o endpoint não emitir.
//
// O pedido é ASSINADO pelo aprovador (AOS-308): desde então a rota recusa pedidos anónimos.
func issueChallenge(t *testing.T, h http.Handler, requestID, approver string, priv ed25519.PrivateKey) []byte {
	t.Helper()
	rec := postJSON(h, "POST", "/runs/run-x/challenge", challengeBody(t, priv, "run-x", requestID, approver))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /challenge devia emitir (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta de /challenge ilegivel: %v", err)
	}
	ch, err := base64.StdEncoding.DecodeString(resp.Challenge)
	if err != nil || len(ch) == 0 {
		t.Fatalf("challenge emitido nao e base64 utilizavel")
	}
	return ch
}

// challengeBody constrói o corpo assinado de POST /runs/{run}/challenge (AOS-308): o aprovador
// assina (run, request_id, approver) com a sua chave pinada; o emissor É o aprovador.
func challengeBody(t *testing.T, priv ed25519.PrivateKey, runID, requestID, approver string) map[string]any {
	t.Helper()
	em, err := integration.SignEmitter(approver, priv, integration.ChallengeRequestScope, control.SignalChallenge,
		integration.CanonicalChallengePayload(runID, requestID, approver), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"emitter":    emissorDeWire(em),
		"request_id": requestID,
		"approver":   approver,
	}
}

// approveBody constrói o corpo de /approve para uma perna reversível assinada.
func approveBody(req integration.FourEyesRequest, leg integration.ApprovalLeg) map[string]any {
	return map[string]any{
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
}

// TestAOS266ChallengeEndpointDisabledWhenDormant prova que sem AOS_CHALLENGE_ISSUANCE o endpoint
// de emissão está DESLIGADO (501) — o par simétrico da porta do gate estar dormente. É a metade
// negativa da invariante de indivisibilidade: nem porta nem endpoint.
func TestAOS266ChallengeEndpointDisabledWhenDormant(t *testing.T) {
	approverID := "human:approver-1"
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := aos266FreshnessConfig(t, approverID, pub)
	cfg.ChallengeIssuance = false // DORMENTE
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if node.ChallengeIssuer != nil {
		t.Fatal("ChallengeIssuer devia ser nil com a frescura dormente")
	}
	_, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs/run-x/challenge", map[string]any{"request_id": "req-1", "approver": approverID})
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("/challenge com frescura dormente devia dar 501, veio %d", rec.Code)
	}
}

// TestAOS266FreshnessIssueThenConsume prova o caminho FELIZ e o REPLAY:
//   - um challenge EMITIDO pelo nó autoriza a perna (200);
//   - reapresentá-lo (mesmo challenge, mesmo pedido) é recusado (403) — uso-único durável.
func TestAOS266FreshnessIssueThenConsume(t *testing.T) {
	approverID := "human:approver-1"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	node, err := Bootstrap(context.Background(), aos266FreshnessConfig(t, approverID, pub), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if node.ChallengeIssuer == nil {
		t.Fatal("ChallengeIssuer devia estar composto com AOS_CHALLENGE_ISSUANCE=1")
	}
	_, h := newAPI(t, node)

	req := integration.FourEyesRequest{
		RequestID:           "req-fresh-1",
		Preview:             []byte("efeito exibido ao humano"),
		RiskClass:           risk.ClassSafe,
		DualControlRequired: false,
	}
	// EMITE o challenge server-side e assina a perna com ele.
	challenge := issueChallenge(t, h, req.RequestID, approverID, priv)
	leg := integration.SignFourEyesLeg(priv, req, approverID, "sess-1", "cred-1", challenge, nil)

	body := approveBody(req, leg)
	semearPendente(t, node, "run-x", capReversivelDeTeste, []byte("efeito exibido ao humano"))
	rec := postJSON(h, "POST", "/runs/run-x/approve", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("/approve com challenge EMITIDO devia autorizar (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
	// REPLAY: o mesmo challenge já foi consumido ⇒ 403.
	rec2 := postJSON(h, "POST", "/runs/run-x/approve", body)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("/approve replayado (challenge consumido) devia dar 403, veio %d", rec2.Code)
	}
}

// TestAOS266FreshnessRejectsUnissuedChallenge é a asserção NÃO-VÁCUA da frescura: uma perna com
// assinatura VÁLIDA mas com um challenge que o nó NUNCA emitiu é recusada (403). Sem a porta
// WithChallengeIssuance composta, este challenge inédito PASSARIA (dedup-only) — é exactamente o
// vector de replay que o modo issue-then-consume fecha.
func TestAOS266FreshnessRejectsUnissuedChallenge(t *testing.T) {
	approverID := "human:approver-1"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	node, err := Bootstrap(context.Background(), aos266FreshnessConfig(t, approverID, pub), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	req := integration.FourEyesRequest{
		RequestID:           "req-fresh-2",
		Preview:             []byte("efeito exibido ao humano"),
		RiskClass:           risk.ClassSafe,
		DualControlRequired: false,
	}
	// Challenge escolhido pelo CLIENTE, NUNCA emitido pelo nó (o vector histórico).
	unissued := make([]byte, 32)
	if _, err := rand.Read(unissued); err != nil {
		t.Fatalf("rand: %v", err)
	}
	leg := integration.SignFourEyesLeg(priv, req, approverID, "sess-1", "cred-1", unissued, nil)
	semearPendente(t, node, "run-x", capReversivelDeTeste, []byte("efeito exibido ao humano"))
	rec := postJSON(h, "POST", "/runs/run-x/approve", approveBody(req, leg))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/approve com challenge NAO-EMITIDO devia dar 403 (ErrChallengeNotIssued), veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAOS266ChallengeTTLHonoured prova, ao nível do NÓ, que o ChallengeTTL configurado alimenta de
// facto o emissor composto: um challenge emitido pelo endpoint é aceite DENTRO da janela de validade.
// Isto NÃO prova o decaimento por expiração — o nó não expõe injecção de relógio, e o relógio real só
// avança segundos dentro do teste, não o minuto do TTL. O ramo de decaimento (challenge emitido cujo
// prazo esgotou ⇒ recusado) é provado deterministicamente, com relógio injectado via WithClock, pelo
// teste unitário TestEventStoreChallengeIssuer_ExpiryDecays em control-plane/governance/hitl.
func TestAOS266ChallengeTTLHonoured(t *testing.T) {
	approverID := "human:approver-1"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := aos266FreshnessConfig(t, approverID, pub)
	cfg.ChallengeTTL = time.Minute
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	req := integration.FourEyesRequest{
		RequestID:           "req-ttl-1",
		Preview:             []byte("efeito exibido ao humano"),
		RiskClass:           risk.ClassSafe,
		DualControlRequired: false,
	}
	challenge := issueChallenge(t, h, req.RequestID, approverID, priv)
	leg := integration.SignFourEyesLeg(priv, req, approverID, "sess-1", "cred-1", challenge, nil)
	// Dentro do TTL (o relógio real avança segundos, não um minuto) ⇒ autoriza.
	semearPendente(t, node, "run-x", capReversivelDeTeste, []byte("efeito exibido ao humano"))
	rec := postJSON(h, "POST", "/runs/run-x/approve", approveBody(req, leg))
	if rec.Code != http.StatusOK {
		t.Fatalf("/approve dentro do TTL devia autorizar (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
}
