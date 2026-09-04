package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// AOS-308 — POST /runs/{id}/challenge autentica o pedido com a assinatura do aprovador nomeado.
//
// A auditoria (`analises/10` §3.1) emitiu challenges sem assinatura, sem headers, contra um run
// inexistente, para um aprovador inventado e em rajada — cinco vectores, todos 200, com o
// registo durável a nomear uma constante do nó como produtor. Estes testes reproduzem os vectores
// e provam que passaram a ser recusados, e que o caminho legítimo continua a funcionar.

// TestAOS308_PedidoAnonimoERecusado — os vectores da auditoria: sem assinatura ⇒ 403, nada escrito.
func TestAOS308_PedidoAnonimoERecusado(t *testing.T) {
	approverID := "human:approver-1"
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node, err := Bootstrap(context.Background(), aos266FreshnessConfig(t, approverID, pub), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if node.ChallengeAuth == nil {
		t.Fatal("ChallengeAuth devia estar composto junto com o ChallengeIssuer (mesmo bloco)")
	}
	_, h := newAPI(t, node)

	casos := []struct {
		nome string
		body map[string]any
	}{
		{"sem emitter", map[string]any{"request_id": "req-anon", "approver": approverID}},
		{"aprovador inventado, sem emitter", map[string]any{"request_id": "req-anon-2", "approver": "quem-eu-quiser"}},
		{"emitter vazio", map[string]any{"emitter": map[string]any{}, "request_id": "req-anon-3", "approver": approverID}},
	}
	for _, k := range casos {
		rec := postJSON(h, "POST", "/runs/run-que-nao-existe/challenge", k.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: devolveu %d, quero 403: %s", k.nome, rec.Code, rec.Body.String())
		}
	}
	// Rajada: vinte pedidos anónimos, nenhum emitido.
	for i := 0; i < 20; i++ {
		rec := postJSON(h, "POST", "/runs/run-x/challenge", map[string]any{"request_id": "req-rajada", "approver": approverID})
		if rec.Code == http.StatusOK {
			t.Fatalf("pedido anonimo #%d emitiu um challenge", i)
		}
	}
}

// TestAOS308_AprovadorAssinaOSeuProprioChallenge — o caminho legítimo, e as três formas de o
// falsificar: chave errada, emissor diferente do aprovador nomeado, e replay do mesmo pedido.
func TestAOS308_AprovadorAssinaOSeuProprioChallenge(t *testing.T) {
	approverID := "human:approver-1"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, outraPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node, err := Bootstrap(context.Background(), aos266FreshnessConfig(t, approverID, pub), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	// (1) O próprio aprovador, com a sua chave ⇒ 200 (e o helper de AOS-266 valida o corpo).
	if ch := issueChallenge(t, h, "req-ok", approverID, priv); len(ch) == 0 {
		t.Fatal("challenge vazio")
	}

	// (2) Chave ERRADA (assinatura de outra pessoa em nome do aprovador) ⇒ 403.
	rec := postJSON(h, "POST", "/runs/run-x/challenge", challengeBody(t, outraPriv, "run-x", "req-chave-errada", approverID))
	if rec.Code != http.StatusForbidden {
		t.Errorf("assinatura de outra chave devolveu %d, quero 403", rec.Code)
	}

	// (3) Emissor DIFERENTE do aprovador nomeado — mesmo com uma assinatura válida do emissor,
	// não pede challenges em nome dos outros.
	em, err := integration.SignEmitter(approverID, priv, integration.ChallengeRequestScope, control.SignalChallenge,
		integration.CanonicalChallengePayload("run-x", "req-em-nome-de-outro", "human:outro"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	rec = postJSON(h, "POST", "/runs/run-x/challenge", map[string]any{
		"emitter": emissorDeWire(em), "request_id": "req-em-nome-de-outro", "approver": "human:outro",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("pedido em nome de OUTRO aprovador devolveu %d, quero 403", rec.Code)
	}

	// (4) REPLAY do mesmo corpo assinado ⇒ 403 (nonce de uso único).
	body := challengeBody(t, priv, "run-x", "req-replay", approverID)
	if rec := postJSON(h, "POST", "/runs/run-x/challenge", body); rec.Code != http.StatusOK {
		t.Fatalf("primeiro pedido devia emitir: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(h, "POST", "/runs/run-x/challenge", body); rec.Code != http.StatusForbidden {
		t.Errorf("replay do mesmo pedido assinado devolveu %d, quero 403", rec.Code)
	}

	// (5) A assinatura amarra o RUN: o mesmo corpo para outro run é recusado (payload diferente).
	body2 := challengeBody(t, priv, "run-x", "req-outro-run", approverID)
	if rec := postJSON(h, "POST", "/runs/run-y/challenge", body2); rec.Code != http.StatusForbidden {
		t.Errorf("pedido assinado para run-x apresentado em run-y devolveu %d, quero 403", rec.Code)
	}
}
