package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	authz "github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/memory/provenance"
	domain "github.com/aos-ref/platform/registry/domain"
	toolset "github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
)

// capturingModel devolve respostas roteirizadas e GRAVA o prompt materializado de cada turno,
// para se observar o output (untrusted) da tool a voltar ao modelo no turno seguinte.
type capturingModel struct {
	responses []agentruntime.ModelResponse
	calls     int
	lastView  []byte
}

func (m *capturingModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.lastView = append([]byte(nil), view.Materialized...)
	r := m.responses[m.calls]
	m.calls++
	return r, nil
}

// TestDemo_SandboxNodeEndToEnd prova o WIRING DE EXECUÇÃO EM SANDBOX no nó (AOS-005/AOS-064)
// pela CADEIA DE PRODUÇÃO REAL: o [SecuredConfig.EffectRewriter] traduz os args OPACOS do modelo
// num ExecRequest (com o RunID/StepID reais da Call) e um [sandbox.MediatedLauncher] REAL,
// registado no RM do nó, executa a tool sob permit — sem bypass. Diferente do executor canned de
// [TestDemo_PermitNodeEndToEnd]: aqui o efeito corre no substrato de sandbox (jail funcional) e o
// seu OUTPUT (untrusted) volta ao prompt do turno 2. Determinista e offline.
func TestDemo_SandboxNodeEndToEnd(t *testing.T) {
	ctx := context.Background()
	const (
		issuerID = "iss:test-idp"
		userID   = "human:alice"
		agentID  = "agt-1"
		class    = "agent-worker"
		capRead  = "cap:fs.read"
	)

	anchor, err := base64.StdEncoding.DecodeString("tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=")
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	policyDP, err := pdp.Open("../control-plane/pdp/policies", pdp.WithTrustAnchor(ed25519.PublicKey(anchor)))
	if err != nil {
		t.Fatalf("pdp.Open: %v", err)
	}

	pub, priv := enfKeys(0x22)
	classes := map[string]identity.ClassPolicy{class: {TTL: 5 * time.Minute, Scope: []string{capRead}}}
	iss, err := identity.NewIssuer(issuerID, priv, classes, identity.WithIssuerClock(enfClock()))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier := identity.NewVerifier(identity.WithTrustedIssuer(issuerID, pub), identity.WithVerifierClock(enfClock()))
	tok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID: userID, AgentID: agentID, AgentClass: class,
		PolicyRef: "policy://agent-worker@1", UserAuthority: []string{capRead},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	authority := authz.NewStaticAuthoritySource().
		Set(userID, capRead).
		Set(agentID, capRead).
		Set("agent:"+class, capRead)

	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)
	entry := signedEntry(t, signer, "doc_read", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	catalog := &fakeCatalog{entries: []domain.Entry{entry}}
	rv := newRevalidator(t, trust, auditStore,
		NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock())),
		NewRecordingAlerter())

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer trajStore.Close()
	worm := audit.NewMemStore()

	// EffectRewriter: os args do modelo → ExecRequest (Command FIXO "read"; Path<-doc_id). É o
	// MESMO shape que o nó compõe em sandboxwiring.go (newSandboxEffectRewriter) — aqui inline
	// para exercitar o SEAM genérico sem importar package main.
	binding := sandbox.SandboxBinding{Command: "read", PathArg: "doc_id"}
	rewriter := func(call referencemonitor.Call) (referencemonitor.Call, error) {
		if call.ToolID != "doc_read" {
			return call, nil
		}
		req, rerr := sandbox.BuildExecRequest(call.RunID, call.StepID, call.ToolID, call.Input, binding)
		if rerr != nil {
			return referencemonitor.Call{}, rerr
		}
		raw, rerr := json.Marshal(req)
		if rerr != nil {
			return referencemonitor.Call{}, rerr
		}
		call.Input = raw
		return call, nil
	}

	content := []byte("Reuniao 3a: rever o plano de migracao. Owner: alice.")
	model := &capturingModel{responses: []agentruntime.ModelResponse{
		{
			Text: "vou ler o documento",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "doc_read", Capability: capRead, Input: []byte(`{"doc_id":"notes"}`),
				ResourceType: "doc", ResourceValue: "notes", ResourceRegion: "eu",
			}},
			Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{Text: "li o documento e concluo", Final: true, Usage: agentruntime.Usage{InputTokens: 6, OutputTokens: 3}},
	}}

	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:          model,
		Recorder:       agentruntime.NewTurnRecorder(trajStore),
		Catalog:        catalog,
		Revalidator:    rv,
		Policy:         StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:           worm,
		Verifier:       verifier,
		Authority:      authority,
		PDP:            policyDP,
		EffectRewriter: rewriter, // AOS-005: o seam sob teste
		FreezeOptions:  []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	// EXECUTOR REAL = sandbox: um MediatedLauncher registado no RM do nó. O RootFS base é semeado
	// com o documento; a leitura mediada devolve o conteúdo real (Stdout).
	snap, err := sandbox.NewSnapshot("img/doc-v1", map[string][]byte{"notes": content})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	driver, err := sandbox.NewDriver(sandbox.DriverFake)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	launcher, err := sandbox.NewLauncher(driver, sandbox.WithSnapshot(snap))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	if _, err := sandbox.NewMediatedLauncher(sec.Monitor(), launcher, "doc_read"); err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	goal := agentruntime.Goal{
		RunID: "run-sbx-node",
		Principal: referencemonitor.Principal{
			NHIID: agentID, AgentID: agentID, AgentClass: class, Authority: []string{capRead},
			DelegationChain: []referencemonitor.DelegationHop{{Sub: userID, ActAs: agentID}},
		},
		Scope:      []string{capRead},
		Credential: tok.Compact,
		Model:      agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
		System:     "assistente de leitura de documentos",
		Objective:  "le o documento notes",
	}

	res, _, err := sec.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A tool EXECUTOU sob permit pela cadeia toda (rewriter + sandbox). Se o rewriter ou o
	// binding estivessem errados, BuildExecRequest falharia → Deny (denials++).
	permits, denials, _ := sec.Metrics().Snapshot()
	if permits != 1 || denials != 0 {
		t.Fatalf("esperava permits=1 denials=0 (permit ponta-a-ponta via sandbox), veio permits=%d denials=%d", permits, denials)
	}

	// O OUTPUT da sandbox (untrusted) VOLTOU ao modelo: o conteúdo lido aparece (base64, no
	// envelope ExecResult) no prompt materializado do turno 2.
	wantB64 := base64.StdEncoding.EncodeToString(content)
	if !bytes.Contains(model.lastView, []byte(wantB64)) {
		t.Fatalf("o output da sandbox devia voltar ao prompt do turno 2 (base64 do conteúdo); tail=%dB", len(model.lastView))
	}

	t.Logf("\n"+
		"  CADEIA     : identity -> revalidation -> taint -> scope -> egress (NewProductionSecure)\n"+
		"  SEAM       : EffectRewriter traduz {\"doc_id\":\"notes\"} -> ExecRequest{cmd=read,path=notes}\n"+
		"  EXECUTOR   : sandbox.MediatedLauncher (jail funcional; no-bypass estrutural no RM do nó)\n"+
		"  PERMITS    : %d  DENIALS: %d\n"+
		"  OUTPUT->LLM: o conteúdo lido volta ao prompt do turno 2 (Terminated=%t)",
		permits, denials, res.Terminated)
}
