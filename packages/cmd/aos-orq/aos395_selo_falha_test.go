package main

// AOS-395 — FAIL-CLOSED na selagem. Um WORM de governação que recusa o selo `model:invoke`
// tem de impedir a chamada ao upstream (audit-before-effect, ADR-010) e devolver erro ao
// decompositor, pelo gateway REAL composto por construirModeloGateway. O controlo positivo,
// com o mesmo store sem avaria, prova que o teste não passa por outro motivo (authn, config).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// storeQueRecusaInvoke delega no MemStore, mas recusa o Append dos selos `model:invoke` quando
// `avariado` — o changelog de activação da allowlist continua a selar, para o gateway compor.
type storeQueRecusaInvoke struct {
	*audit.MemStore
	avariado bool
}

func (s *storeQueRecusaInvoke) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	if s.avariado && rec.Capability == modelInvokeCapability {
		return audit.AuditRecord{}, errors.New("disco cheio (simulado)")
	}
	return s.MemStore.Append(ctx, rec)
}

func TestAOS395_SeloFalha_NaoChamaOModeloNemDecompoe(t *testing.T) {
	for _, avariado := range []bool{false, true} {
		t.Run(fmt.Sprintf("avariado=%v", avariado), func(t *testing.T) {
			var chamadas atomic.Int32
			plano := upstreamComPlano(t).Config.Handler
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chamadas.Add(1)
				plano.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)

			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			iss, err := identity.NewIssuer("iss:aos-orq", priv, map[string]identity.ClassPolicy{
				classeCoordenador: {TTL: tokenTTL, Scope: []string{modelInvokeCapability}},
			})
			if err != nil {
				t.Fatalf("NewIssuer: %v", err)
			}
			ctx := context.Background()
			tok, err := iss.Issue(ctx, identity.IssueRequest{
				UserID: "human:p1", AgentID: "agt-run-aos395", AgentClass: classeCoordenador,
				UserAuthority: []string{modelInvokeCapability},
			})
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			verifier := identity.NewVerifier(identity.WithTrustedIssuer("iss:aos-orq", iss.PublicKey()))

			keyPath := filepath.Join(t.TempDir(), "model.key")
			escrever(t, keyPath, "sk-teste-aos395")
			cfg := &gatewayConfig{endpoint: srv.URL, model: "gpt-4o", apiKeyPath: keyPath, region: "eu", board: "board-eu"}

			st := &storeQueRecusaInvoke{MemStore: audit.NewMemStore(), avariado: avariado}
			m, err := construirModeloGateway(ctx, cfg, verifier, tok.Compact, st)
			if err != nil {
				t.Fatalf("construirModeloGateway: %v", err)
			}

			callCtx := agentruntime.ContextWithModelCall(ctx, "run-aos395", "planstep:decompose:1")
			_, err = m.Complete(callCtx, "system", "user")
			if !avariado {
				if err != nil || chamadas.Load() != 1 {
					t.Fatalf("controlo positivo: err=%v chamadas=%d; quero nil e 1", err, chamadas.Load())
				}
				return
			}
			if err == nil {
				t.Fatal("o selo falhou e a decomposição recebeu resposta — fail-open")
			}
			if n := chamadas.Load(); n != 0 {
				t.Fatalf("o upstream foi chamado %d vezes com o selo recusado (audit-before-effect)", n)
			}
		})
	}
}
