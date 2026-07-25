package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
)

const (
	durClass = "agent-worker" // classe presente na allowlist da política de referência
	durCap   = "cap:fs.read"  // capability permitida por allowlist + regra Cedar
	durAgent = "agt-1"        // agentID do token NHI; tem de constar da AuthoritySource
)

// catalogStub é um [toolset.Catalog] determinista para testes de AOS-180.
type catalogStub struct{ entries []domain.Entry }

func (c catalogStub) ActiveEntries(context.Context) ([]domain.Entry, error) {
	out := make([]domain.Entry, len(c.entries))
	copy(out, c.entries)
	return out, nil
}

// durPublisherKeyID identifica o publicador de confiança do teste.
const durPublisherKeyID = "pub:aos-180-test"

// durSigner devolve um assinante Ed25519 determinístico para os artefactos do teste.
func durSigner(t *testing.T) *signing.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	s, err := signing.NewSigner(durPublisherKeyID, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// counterEntry constrói uma entry coerente: digest SHA-256 do contrato e assinatura
// sobre (id, version, digest), como exige a revalidação de AOS-051.
func counterEntry(t *testing.T, signer *signing.Signer) domain.Entry {
	t.Helper()
	contract := domain.Contract{Egress: domain.EgressNone}
	dig := digest.SHA256Digester{}.Digest(domain.KindTool, contract)
	return domain.Entry{
		ID:        "counter",
		Version:   domain.Version{Major: 1, Minor: 0, Patch: 0},
		Kind:      domain.KindTool,
		Digest:    dig,
		Signature: signer.Sign("counter", domain.Version{Major: 1, Minor: 0, Patch: 0}, dig),
		Contract:  contract,
		Provenance: domain.Provenance{
			Origin:    "mcp://aos-180-test",
			Publisher: signer.KeyID(),
			Timestamp: "2026-07-25T00:00:00Z",
			Trust:     domain.TrustFirstSeen,
		},
		Status: domain.StatusActive,
	}
}

// twoTurnToolModel emite uma tool call no primeiro turno e uma resposta final no
// segundo — o mínimo para exercitar a execução e deduplicação duráveis.
type twoTurnToolModel struct{ calls int32 }

func (m *twoTurnToolModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	turn := atomic.AddInt32(&m.calls, 1)
	if turn == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:     "counter",
				Capability: durCap,
				Input:      []byte("tick"),
			}},
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{
		Text:  "run concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// TestNode_DurableExecution_NoDoubleExecAfterRestart prova AOS-180: quando
// DurableExecution está activo, o nó persiste o step-ledger no Event Store e, após
// um restart (Close + Bootstrap sobre o mesmo WAL), uma tool call já executada é
// deduplicada em vez de re-executada.
func TestNode_DurableExecution_NoDoubleExecAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	signer := durSigner(t)
	entry := counterEntry(t, signer)

	// Revalidador com trust store que confia no publicador do teste.
	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		t.Fatalf("trust store: %v", err)
	}
	if err := trust.Add(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	revalidator, err := revalidation.New(trust, auditStore)
	if err != nil {
		t.Fatalf("revalidator: %v", err)
	}

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed") // chave estável entre reinícios
	cfg.Model = &twoTurnToolModel{}
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.Revalidator = revalidator
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP, err = pdp.Open("../../control-plane/pdp/policies")
	if err != nil {
		t.Fatalf("abrir bundle de política de referência: %v", err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	var execs int64
	runID := "run-durable-restart"
	goal := agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:" + runID},
		Objective: "contar",
		MaxTurns:  4,
	}

	// PRIMEIRA VIDA: arranca o nó, emite o token, regista a tool e corre o run.
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	goal.Credential = tok.Compact

	if err := node.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("register counter: %v", err)
	}

	res, _, err := node.Runtime.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("primeira execução: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("primeira execução não terminou: %+v", res)
	}
	if got := atomic.LoadInt64(&execs); got != 1 {
		t.Fatalf("na primeira execução a tool devia ter corrido 1 vez, correu %d", got)
	}

	if err := node.Close(); err != nil {
		t.Fatalf("close (simula crash): %v", err)
	}

	// SEGUNDA VIDA: novo processo sobre o mesmo WAL e chave de issuer.
	node2, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap pós-restart: %v", err)
	}
	defer node2.Close()

	if err := node2.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("register counter pós-restart: %v", err)
	}

	if err := node2.Runtime.RebuildLedger(ctx, runID); err != nil {
		t.Fatalf("rebuild ledger: %v", err)
	}

	res2, _, err := node2.Runtime.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("segunda execução (retoma): %v", err)
	}
	if !res2.Terminated {
		t.Fatalf("segunda execução não terminou: %+v", res2)
	}
	if got := atomic.LoadInt64(&execs); got != 1 {
		t.Fatalf("tool re-executada após restart: esperava 1 execução no total, obtive %d (ledger não deduplicou)", got)
	}
}
