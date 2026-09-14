package modelgateway_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Prova NO SEAM de que [modelgateway.ProductionConfig.EgressTimeout] chega ao transporte que
// faz as chamadas ao modelo. Complementa egress_timeout_internal_test.go, que exerce o helper
// do cliente endurecido directamente: sem esta prova, deixar de passar o campo em NewProduction
// deixaria os testes verdes e a produção de volta aos 30 s.

// parSilencioso abre um listener TCP que aceita ligações e nunca escreve nada — nem o
// handshake TLS. Um pedido contra ele só termina por timeout do cliente.
func parSilencioso(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

// TestNewProduction_EgressTimeout_ChegaAoTransporte — com EgressTimeout de 200 ms, uma chamada a
// um par que nunca responde falha depressa. Se o campo não chegasse ao cliente, o corte só viria
// do TLSHandshakeTimeout (10 s) ou do default (30 s). O limiar de 5 s separa os dois mundos com
// folga larga; é a única observação possível de um timeout de rede, e por isso usa o relógio real.
func TestNewProduction_EgressTimeout_ChegaAoTransporte(t *testing.T) {
	t.Parallel()
	addr := parSilencioso(t)
	cfg := ssrfSeamConfig(audit.NewMemStore(), "https://"+addr+"/v1", []string{addr})
	cfg.EgressTimeout = 200 * time.Millisecond

	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}

	inicio := time.Now()
	_, err = gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	decorrido := time.Since(inicio)

	if err == nil {
		t.Fatal("uma chamada a um par que nunca responde tinha de falhar")
	}
	if decorrido > 5*time.Second {
		t.Fatalf("a chamada demorou %v a falhar: o EgressTimeout (200ms) não chegou ao transporte", decorrido)
	}
}
