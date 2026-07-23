package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
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
//	(b) ENUMERAÇÃO de RunID (run EXISTENTE-não-observável vs INEXISTENTE) ⇒ MESMO 404 (não vaza existência)
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

	// (b) ENUMERAÇÃO de RunID ⇒ 404 UNIFORME, exercitando o caso DISCRIMINANTE. A ameaça de
	// enumeração não é "um inexistente dá 404" (trivial: cai sempre no mesmo 404 final); é a
	// INDISTINGUIBILIDADE entre um run que EXISTE mas o chamador não pode observar e um que nunca
	// existiu. Prova-se sobre um nó com SOBERANIA de leitura ligada (gate D7 fail-closed): um
	// leitor AUTORIZADO vê o run existente (200 — o run é mesmo real, não-vacuoso), um leitor
	// NÃO-AUTORIZADO do MESMO run existente recebe 404, e esse 404 é BYTE-A-BYTE igual ao de um
	// run inexistente — logo o status/corpo nunca serve de oráculo de existência.
	t.Run("enumeracao_runid_404_uniforme", func(t *testing.T) {
		node := newGovNode(t, &countingModel{})
		svc, h := newAPI(t, node)

		const existing = "run-abuse-existente-secreto"
		submitAndWait(t, svc, existing)

		// NÃO-VACUOSO: o run EXISTE e é observável a um leitor AUTORIZADO (senão a
		// indistinguibilidade abaixo seria trivial — dois 404 sobre nada).
		if authz := getReq(h, "/runs/"+existing, govHeaders()); authz.Code != http.StatusOK {
			t.Fatalf("leitor autorizado devia ver o run existente (200), veio %d (%s)", authz.Code, authz.Body.String())
		}

		// Caso DISCRIMINANTE: leitor NÃO-autorizado de um run EXISTENTE vs leitor autorizado de um
		// run INEXISTENTE — têm de ser indistinguíveis (mesmo status E mesmo corpo).
		deniedExisting := getReq(h, "/runs/"+existing, map[string]string{
			HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard,
		})
		missing := getReq(h, "/runs/run-abuse-nunca-existiu", govHeaders())

		for _, rec := range []struct {
			name string
			r    *httptest.ResponseRecorder
		}{{"existente-nao-observavel", deniedExisting}, {"inexistente", missing}} {
			if rec.r.Code != http.StatusNotFound {
				t.Fatalf("%s devia dar 404, veio %d (%s)", rec.name, rec.r.Code, rec.r.Body.String())
			}
			if !strings.Contains(rec.r.Body.String(), "not found") {
				t.Fatalf("%s: 404 devia ser UNIFORME (\"not found\"), veio %q", rec.name, rec.r.Body.String())
			}
		}
		// A não-enumerabilidade REAL: os dois desfechos são byte-a-byte idênticos.
		if deniedExisting.Body.String() != missing.Body.String() {
			t.Fatalf("run existente-nao-observavel (%q) distinguivel de inexistente (%q) — ENUMERAVEL",
				deniedExisting.Body.String(), missing.Body.String())
		}
		// E a negação nunca vaza o RunID existente.
		if strings.Contains(deniedExisting.Body.String(), existing) {
			t.Fatalf("a negacao vaza o RunID existente: %q", deniedExisting.Body.String())
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
