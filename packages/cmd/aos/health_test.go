package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// AOS-171 (E5) — sondas de saúde/prontidão do nó `aos`. Estes testes provam a SEMÂNTICA
// que torna o drain seguro e o contentor operável, sem afrouxar invariantes:
//
//   - /healthz (LIVENESS) devolve 200 SEMPRE, inclusive depois de o Event Store fechar —
//     a liveness NÃO depende de dependências (senão causaria restart-loops);
//   - /readyz (READINESS) devolve 200 só quando saudável, e vira 503 tanto no drain
//     (shutdown iniciado) como com o Event Store fechado — provando as duas transições
//     ready→unready que fazem o orquestrador parar de encaminhar tráfego;
//   - ambas são acessíveis SEM emitter/assinatura (não exigem authn, ao contrário do
//     canal de controlo) e o corpo de /readyz NÃO revela estado interno sensível.

// getProbe faz um GET simples a target (sem corpo, sem emitter) e devolve o recorder.
func getProbe(h http.Handler, target string) (int, map[string]string) {
	rec := postJSON(h, "GET", target, nil)
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestHealthzAlwaysLive prova que /healthz devolve 200 mesmo APÓS o Event Store fechar:
// a liveness reflecte só que o processo está vivo e a servir, nunca a saúde de uma
// dependência (um /healthz que falhasse por dependência causaria restart-loops).
func TestHealthzAlwaysLive(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	// Saudável ⇒ 200.
	if code, _ := getProbe(h, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz saudavel devia dar 200, veio %d", code)
	}

	// Fecha o Event Store (dependência em degradação) — a liveness NÃO deve mudar.
	if err := node.EventStore.Close(); err != nil {
		t.Fatalf("Close do Event Store: %v", err)
	}
	if code, body := getProbe(h, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz devia continuar 200 apos o Event Store fechar (liveness nao depende de dependencias), veio %d (%v)", code, body)
	}
}

// TestReadyzHealthyIsReady prova que /readyz devolve 200 quando o nó está saudável (Event
// Store operacional e serviço não em drain).
func TestReadyzHealthyIsReady(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	code, body := getProbe(h, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("/readyz saudavel devia dar 200, veio %d (%v)", code, body)
	}
	if body["status"] != "ready" {
		t.Fatalf("/readyz 200 devia trazer status=ready, veio %q", body["status"])
	}
}

// TestReadyzUnreadyOnDrain prova a transição ready→unready no shutdown gracioso: ANTES do
// Shutdown /readyz é 200; DEPOIS de o drain começar (svc.Shutdown arma o flag `closed`
// antes de esperar o escoamento) /readyz vira 503 — é o sinal que faz o orquestrador
// parar de encaminhar tráfego novo. /healthz permanece 200 (o processo continua a servir).
func TestReadyzUnreadyOnDrain(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	// Pronto antes do drain.
	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz antes do drain devia dar 200, veio %d", code)
	}

	// Inicia o drain (sem runs em curso, escoa imediatamente).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Agora NÃO-pronto (503), mas ainda vivo (200) — o drain é seguro.
	if code, body := getProbe(h, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz durante o drain devia dar 503, veio %d (%v)", code, body)
	}
	if code, _ := getProbe(h, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz durante o drain devia continuar 200 (liveness), veio %d", code)
	}
}

// TestReadyzUnreadyOnStoreClosed prova que /readyz vira 503 quando o Event Store está
// fechado (NÃO operacional) — a outra condição de não-prontidão, independente do drain.
func TestReadyzUnreadyOnStoreClosed(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	// Pronto enquanto o store está operacional.
	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz com store operacional devia dar 200, veio %d", code)
	}

	// Fecha o Event Store: deixa de estar operacional (Append/Read dariam ErrClosed).
	if err := node.EventStore.Close(); err != nil {
		t.Fatalf("Close do Event Store: %v", err)
	}
	if code, body := getProbe(h, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz com o Event Store fechado devia dar 503, veio %d (%v)", code, body)
	}
}

// TestProbesNeedNoAuth prova que ambas as sondas são acessíveis SEM emitter/assinatura —
// ao contrário do canal de controlo (/steer,/pause,/approve), que rejeita um pedido
// anónimo. Os probes de orquestrador não assinam; exigir authn quebraria a operabilidade.
func TestProbesNeedNoAuth(t *testing.T) {
	// Nó COM operador registado (canal de controlo autenticado) — para tornar não-vacuoso
	// o contraste: mesmo num nó que exige authn no controlo, as sondas passam sem ela.
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	if code, _ := getProbe(h, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz sem authn devia dar 200, veio %d", code)
	}
	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz sem authn devia dar 200, veio %d", code)
	}
}

// TestReadyzBodyNoInfoLeak prova que o corpo de /readyz (em ambos os estados) NÃO revela
// estado interno sensível: nenhuma contagem de runs, RunID, modo de identidade ou detalhe
// de erro cru — só o campo `status` uniforme. É coerente com a filosofia não-enumerável
// dos restantes handlers.
func TestReadyzBodyNoInfoLeak(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	// Estado PRONTO: corpo só com {"status":"ready"} e nada mais.
	_, ready := getProbe(h, "/readyz")
	assertReadyzBody(t, ready, "ready")

	// Estado NÃO-PRONTO (drain): corpo só com {"status":"unready"} e nada mais.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_, unready := getProbe(h, "/readyz")
	assertReadyzBody(t, unready, "unready")
}

// assertReadyzBody verifica que o corpo tem EXACTAMENTE {"status": want} — um único campo,
// sem chaves adicionais que pudessem vazar contagens/IDs/modo/erro.
func assertReadyzBody(t *testing.T, body map[string]string, want string) {
	t.Helper()
	if len(body) != 1 {
		t.Fatalf("/readyz devia ter EXACTAMENTE 1 campo (status), veio %d campos: %v", len(body), body)
	}
	if body["status"] != want {
		t.Fatalf("/readyz status devia ser %q, veio %q", want, body["status"])
	}
}
