package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Prova de que newGatewayModelClient passa o tempo máximo ao cliente que faz as chamadas ao modelo,
// nos DOIS ramos (produção: transporte endurecido do gateway; dev: seam injectado). Sem esta prova,
// apagar a atribuição no wiring deixaria os testes verdes e a produção de volta aos 30 s.
//
// A chamada tem de passar o estágio de identidade e ter credencial de provider — senão falha ANTES
// de sair qualquer pedido HTTP, e o teste passaria pela razão errada. Daí o token real (AOS-278) e
// o ficheiro de API key.

// aosParSilencioso abre um listener TCP que aceita ligações e nunca escreve nada.
func aosParSilencioso(t *testing.T) string {
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

func TestModelEgressTimeout_WiringChegaAoCliente(t *testing.T) {
	casos := []struct {
		nome     string
		producao bool
		esquema  string
		semTecto string // o corte que se veria se o valor não chegasse ao cliente
	}{
		{"producao_transporte_endurecido", true, "https", "10s do handshake TLS ou 30s do default"},
		{"dev_seam_injectado", false, "http", "60s do seam de dev"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			addr := aosParSilencioso(t)
			auth, verifier := aos278AuthorityReader(t)
			chave := filepath.Join(t.TempDir(), "model-key")
			if err := os.WriteFile(chave, []byte("dev-secret"), 0o600); err != nil {
				t.Fatalf("escrever chave de teste: %v", err)
			}

			mc, err := newGatewayModelClient(verifier, c.esquema+"://"+addr+"/v1", "gpt-4o", chave,
				defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil, nil,
				c.producao, []string{addr}, 200*time.Millisecond)
			if err != nil {
				t.Fatalf("newGatewayModelClient: %v", err)
			}

			ctx := withModelCredential(context.Background(), aos278Mint(t, auth, "model:invoke"))
			inicio := time.Now()
			_, err = mc.Call(ctx, agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")})
			decorrido := time.Since(inicio)

			if err == nil {
				t.Fatal("uma chamada a um par que nunca responde tinha de falhar")
			}
			if decorrido > 5*time.Second {
				t.Fatalf("demorou %v a falhar: o tempo máximo (200ms) não chegou ao cliente — o corte veio dos %s", decorrido, c.semTecto)
			}
		})
	}
}
