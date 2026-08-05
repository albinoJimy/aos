package integration

import (
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
)

// TestDemo_PermitNodeEndToEnd demonstra, através da CADEIA DE PRODUÇÃO REAL do nó
// ([NewSecuredRuntime] → [referencemonitor.NewProductionSecure]: identity → revalidation → taint →
// scope → egress + audit-before-effect + dispatch), uma tool call PERMITIDA que EXECUTA de ponta a
// ponta, com o INPUT e o OUTPUT visíveis, e o output a voltar ao turno seguinte do loop.
//
// É o "nó completo" SEM tocar no binário do nó nem relaxar a sua postura: todos os gates estão
// ACTIVOS e a call passa porque a identidade é verificada, o catálogo está assinado, e a Authority
// concede LEGITIMAMENTE o escopo cap:fs.read (user∩classe) — exactamente o que uma deployment com
// uma AuthoritySource real faria. Determinista e offline (modelo-stub, relógios fixos).
func TestDemo_PermitNodeEndToEnd(t *testing.T) {
	ctx := context.Background()
	const (
		issuerID = "iss:test-idp"
		userID   = "human:alice"
		agentID  = "agt-1"
		class    = "agent-worker" // classe ALLOWLISTED para cap:fs.read no bundle de referência (AOS-007)
		capRead  = "cap:fs.read"
	)

	// PDP REAL: carrega o bundle de referência ASSINADO (allow_fs_read + allowlist de classes),
	// verificado contra o trust anchor out-of-band — exactamente como o nó faz de AOS_POLICY_BUNDLE_DIR.
	anchor, err := base64.StdEncoding.DecodeString("tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=")
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	policyDP, err := pdp.Open("../control-plane/pdp/policies", pdp.WithTrustAnchor(ed25519.PublicKey(anchor)))
	if err != nil {
		t.Fatalf("pdp.Open (bundle de referência): %v", err)
	}

	// --- IDENTIDADE REAL: issuer → verifier → credencial NHI (cap:fs.read) ---
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
	credential := tok.Compact

	// Authority user∩classe que CONCEDE cap:fs.read a cada sujeito do ScopeGate (humano, NHI, classe).
	authority := authz.NewStaticAuthoritySource().
		Set(userID, capRead).
		Set(agentID, capRead).
		Set("agent:"+class, capRead)

	// --- REVALIDAÇÃO REAL: catálogo assinado + trust store + revalidator ---
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

	// --- MODELO-STUB: turno 1 chama doc_read com input; turno 2 conclui (após receber o output) ---
	input := []byte(`{"doc_id":"notes"}`)
	model := &scriptedModel{responses: []agentruntime.ModelResponse{
		{
			Text: "vou ler o documento",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "doc_read", Capability: capRead, Input: input,
				ResourceType: "doc", ResourceValue: "notes", ResourceRegion: "eu",
			}},
			Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{Text: "li o documento e concluo", Final: true, Usage: agentruntime.Usage{InputTokens: 6, OutputTokens: 3}},
	}}

	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:         model,
		Recorder:      agentruntime.NewTurnRecorder(trajStore),
		Catalog:       catalog,
		Revalidator:   rv,
		Policy:        StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:          worm,
		Verifier:      verifier,
		Authority:     authority,
		PDP:           policyDP,
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	// --- EXECUTOR REAL da tool: recebe o input, devolve o conteúdo do documento ---
	var gotInput, gotOutput []byte
	execCount := 0
	docs := map[string]string{"notes": "Reuniao 3a: rever o plano de migracao. Owner: alice."}
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		gotInput = append([]byte(nil), in...)
		var q struct {
			DocID string `json:"doc_id"`
		}
		_ = json.Unmarshal(in, &q)
		out, _ := json.Marshal(map[string]string{"doc_id": q.DocID, "content": docs[q.DocID]})
		gotOutput = out
		return out, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	goal := agentruntime.Goal{
		RunID: "run-permit",
		Principal: referencemonitor.Principal{
			NHIID: agentID, AgentID: agentID, AgentClass: class, Authority: []string{capRead},
			// Cadeia de delegação (humano→agente) — no nó a API deriva-a do NHI; aqui construo-a
			// para o ScopeGate (chainSubjects) computar user∩classe sobre [human:alice, agt-1].
			DelegationChain: []referencemonitor.DelegationHop{{Sub: userID, ActAs: agentID}},
		},
		Scope:      []string{capRead},
		Credential: credential,
		Model:      agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
		System:     "assistente de leitura de documentos",
		Objective:  "le o documento notes",
	}

	res, _, err := sec.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- ASSERÇÕES: a tool EXECUTOU (permit atravessou toda a cadeia) e produziu o output ---
	if execCount != 1 {
		t.Fatalf("a tool devia ter executado 1x (permit ponta-a-ponta); execCount=%d", execCount)
	}
	var out struct{ Content string }
	_ = json.Unmarshal(gotOutput, &out)
	if out.Content != docs["notes"] {
		t.Fatalf("output inesperado: %s", gotOutput)
	}

	t.Logf("\n"+
		"  CADEIA       : identity -> revalidation -> taint -> scope -> egress (NewProductionSecure)\n"+
		"  TOOL         : doc_read (cap:fs.read) — proposta pelo modelo no turno 1\n"+
		"  INPUT        : %s\n"+
		"  EXECUÇÃO     : permit -> audit-before-effect -> dispatch -> executor correu (execCount=%d)\n"+
		"  OUTPUT       : %s\n"+
		"  TURNO 2      : o output (untrusted) volta ao loop; o modelo conclui (Final=%t)\n"+
		"  RESULTADO    : run terminado (Terminated=%t)",
		gotInput, execCount, gotOutput, true, res.Terminated)
}
