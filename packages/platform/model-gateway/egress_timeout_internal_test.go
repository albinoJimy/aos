package modelgateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Testes do tempo máximo configurável do cliente de egress REAL ([ProductionConfig.EgressTimeout]).
// White-box, no molde de ssrf_internal_test.go: exercem os helpers do pacote directamente.

// TestHardenedEgressClient_TimeoutZeroUsaODefault — sem tecto configurado o cliente fica com o
// default (egressTimeout). É a garantia de que nada muda para quem não configura.
func TestHardenedEgressClient_TimeoutZeroUsaODefault(t *testing.T) {
	t.Parallel()
	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}), 0)
	if client.Timeout != egressTimeout {
		t.Fatalf("timeout 0 devia dar o default %v; got %v", egressTimeout, client.Timeout)
	}
}

// TestHardenedEgressClient_TimeoutConfiguradoPrevalece — um tecto configurado é o que o cliente usa.
func TestHardenedEgressClient_TimeoutConfiguradoPrevalece(t *testing.T) {
	t.Parallel()
	const pedido = 120 * time.Second
	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}), pedido)
	if client.Timeout != pedido {
		t.Fatalf("timeout configurado %v não chegou ao cliente; got %v", pedido, client.Timeout)
	}
}

// TestHardenedEgressClient_TimeoutCortaUmServidorLento — prova de COMPORTAMENTO, não de campo: um
// servidor que demora mais do que o tecto é cortado com um erro de timeout. Sem isto, um campo
// preenchido mas ignorado pelo transporte passaria nos dois testes acima.
func TestHardenedEgressClient_TimeoutCortaUmServidorLento(t *testing.T) {
	t.Parallel()
	libertar := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-libertar:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(libertar) // corre ANTES de srv.Close: não deixa o handler preso a segurar o Close

	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}), 150*time.Millisecond)
	trustServer(t, client, srv)

	resp, err := client.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("um servidor mais lento do que o tecto devia ser cortado; o pedido completou")
	}
	var ue *url.Error
	if !errors.As(err, &ue) || !ue.Timeout() {
		t.Fatalf("esperava um erro de timeout; got %v", err)
	}
}

// TestNewProviderAdapter_TimeoutNegativoRecusa — um tecto negativo é recusado na composição.
// Deixá-lo passar dava um http.Client sem timeout nenhum (<= 0 é "sem limite" para o net/http).
func TestNewProviderAdapter_TimeoutNegativoRecusa(t *testing.T) {
	t.Parallel()
	_, err := newProviderAdapter("openai", "https://127.0.0.1/v1", nil, []string{"127.0.0.1"}, -time.Second)
	if !errors.Is(err, ErrBadEgressTimeout) {
		t.Fatalf("timeout negativo devia dar ErrBadEgressTimeout; got %v", err)
	}
}
