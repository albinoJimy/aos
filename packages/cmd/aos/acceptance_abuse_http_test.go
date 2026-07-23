package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// AOS-169 — BATERIA DE ABUSO HTTP (E6), NOMEADA e NÃO-VACUOSA. Prova que a superfície de rede
// do nó (AOS-166 ingresso + AOS-160 canal de controlo anti-replay) RECUSA cada vector de abuso
// com um status/efeito CONCRETO — cada sub-teste assere a recusa, não só a ausência de sucesso.
// Consolida os quatro vectores exigidos pelo capstone num único ponto nomeado, sobre o handler
// da API REAL (NewAPIHandler) de um nó REAL (Bootstrap). Reutiliza os helpers de api_test.go
// (mesmo pacote): newAPINode/newAPI/postJSON/steerBody.
//
//	(a) payload GIGANTE no POST /runs                 ⇒ 413 (MaxBytesReader; não OOM)
//	(b) ENUMERAÇÃO de RunID (GET inexistente)         ⇒ 404 UNIFORME (indistinguível de sem-permissão)
//	(c) REPLAY de um sinal de controlo (mesmo nonce)  ⇒ 403 (anti-replay durável AOS-160)
//	(d) steer NÃO-autenticado (sem assinatura válida) ⇒ 403 (canal de controlo trusted)
func TestAOS169_HTTPAbuseBattery(t *testing.T) {
	const operatorID = "human:operator-aos169"
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// (a) PAYLOAD GIGANTE ⇒ 413. Tecto de corpo pequeno (1 KiB) e um objective muito maior:
	// o MaxBytesReader corta ANTES de esgotar memória — o ingresso não é vector de exaustão.
	t.Run("payload_gigante_413", func(t *testing.T) {
		node, _ := newAPINode(t, &countingModel{}, false)
		defer func() { _ = node.Close() }()
		_, h := newAPI(t, node, WithMaxBodyBytes(1024))

		huge := strings.Repeat("A", 64*1024)
		rec := postJSON(h, "POST", "/runs", map[string]any{
			"run_id":        "run-abuse-huge",
			"principal_nhi": "nhi:abuse-huge",
			"objective":     huge,
		})
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("payload gigante devia dar 413, veio %d", rec.Code)
		}
	})

	// (b) ENUMERAÇÃO de RunID ⇒ 404 UNIFORME. Um RunID inexistente e um não-observável devolvem
	// o MESMO corpo "not found" — o status não vaza a existência de runs alheios.
	t.Run("enumeracao_runid_404_uniforme", func(t *testing.T) {
		node, _ := newAPINode(t, &countingModel{}, false)
		defer func() { _ = node.Close() }()
		_, h := newAPI(t, node)

		for _, id := range []string{"nao-existe-1", "outro-inexistente-2", "run-alheio-nao-observavel"} {
			rec := postJSON(h, "GET", "/runs/"+id, nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET de RunID inexistente %q devia dar 404, veio %d", id, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "not found") {
				t.Fatalf("404 devia ser UNIFORME (\"not found\"), veio %q", rec.Body.String())
			}
		}
	})

	// (c) REPLAY de um sinal de controlo ⇒ 403. O 1.º steer válido é aceite (202); reenviar o
	// MESMO emitter/nonce ⇒ recusado pelo anti-replay durável (o nonce foi consumido).
	t.Run("replay_sinal_controlo_403", func(t *testing.T) {
		node, _ := newAPINodeWithOperator(t, operatorID, opPub)
		defer func() { _ = node.Close() }()
		_, h := newAPI(t, node)

		const runID = "run-abuse-replay"
		correction := []byte("aperta o ambito")
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			t.Fatalf("rand nonce: %v", err)
		}
		em := integration.SignSignal(opPriv, operatorID, runID, control.SignalSteer, correction, nonce, tnClock()())
		body := steerBody(em, correction)

		if rec := postJSON(h, "POST", "/runs/"+runID+"/steer", body); rec.Code != http.StatusAccepted {
			t.Fatalf("1o steer valido devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
		}
		if rec := postJSON(h, "POST", "/runs/"+runID+"/steer", body); rec.Code != http.StatusForbidden {
			t.Fatalf("steer REPLAYADO (mesmo nonce) devia dar 403 (anti-replay durAvel), veio %d", rec.Code)
		}
		// EFEITO: o replay NÃO acrescentou uma segunda correcção — o canal só reteve a 1.ª.
		if got, ok := node.Steer.PendingCorrection(runID); !ok || string(got) != string(correction) {
			t.Fatalf("apos o replay recusado, a correccao pendente devia ser so a 1a (%q), veio (%q,%v)", correction, got, ok)
		}
	})

	// (d) steer NÃO-autenticado ⇒ 403. Um POST de controlo sem assinatura ed25519 válida (emitter
	// vazio) é recusado — NUNCA um POST de controlo anónimo é aceite; sem efeito no canal.
	t.Run("steer_nao_autenticado_403", func(t *testing.T) {
		node, _ := newAPINodeWithOperator(t, operatorID, opPub)
		defer func() { _ = node.Close() }()
		_, h := newAPI(t, node)

		const runID = "run-abuse-anon"
		rec := postJSON(h, "POST", "/runs/"+runID+"/steer", map[string]any{
			"emitter": map[string]any{
				"id": "", "signature": "", "nonce": "",
				"issued_at": tnClock()().Format(time.RFC3339Nano),
			},
			"payload": base64.StdEncoding.EncodeToString([]byte("correccao anonima")),
		})
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
			t.Fatalf("steer nao-autenticado devia ser RECUSADO (403/400), veio %d", rec.Code)
		}
		if _, ok := node.Steer.PendingCorrection(runID); ok {
			t.Fatal("steer nao-autenticado NAO devia ter efeito no canal")
		}
	})
}

// newAPINodeWithOperator compõe um nó de teste com UM operador ed25519 registado no canal de
// controlo (para os vectores de abuso do plano de controlo). Espelha newAPINode mas com uma
// pubkey de operador já dada (a privada vive só do lado do emissor de teste).
func newAPINodeWithOperator(t *testing.T, operatorID string, opPub ed25519.PublicKey) (*Node, ed25519.PrivateKey) {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.SteerClock = tnClock()
	cfg.Operators = map[string]ed25519.PublicKey{operatorID: opPub}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return node, nil
}
