package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/memory/provenance"
	domain "github.com/aos-ref/platform/registry/domain"
	toolset "github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// TestScopeTokenOnly_SemDirectorioExterno_ToolExecuta é a prova CI end-to-end de que a
// autoridade de escopo (AOS-071) flui SÓ do TOKEN NHI verificado (AOS-156), sem qualquer
// directório externo — a configuração REAL do nó, onde SecuredConfig.Authority é nil.
//
// PORQUÊ ESTE TESTE EXISTE (margem fechada): o fix de AOS-071/AOS-156 estava provado por um
// unit test isolado do gate (TestScope_SubjectAuthority_DerivadaDoToken) e por um run vivo
// não-CI; mas os testes e2e existentes ([TestDemo_PermitNodeEndToEnd],
// [TestDemo_SandboxNodeEndToEnd]) injectam uma AuthoritySource POPULADA, consistente com o
// token — pelo que NÃO distinguiam "a autoridade veio do token" de "veio do directório".
// Aqui o directório está DELIBERADAMENTE AUSENTE (nil ⇒ NewStaticAuthoritySource() VAZIA, o
// default de [NewSecuredRuntime]): se a autoridade não viesse do token, a dobra colapsaria
// para ∅ e a call morreria em deny|scope, como acontecia antes do fix.
//
// Corre contra a cadeia de produção REAL (NewProductionSecure: identity → revalidation →
// policy(PDP com bundle assinado) → taint → scope → egress), sem stubs. Determinista e offline.
func TestScopeTokenOnly_SemDirectorioExterno_ToolExecuta(t *testing.T) {
	ctx := context.Background()
	const (
		issuerID = "iss:test-idp"
		userID   = "human:alice"
		agentID  = "agt-token-only"
		class    = "agent-worker" // classe allowlisted para cap:fs.read no bundle de referência
		capRead  = "cap:fs.read"
	)

	// PDP REAL: bundle de referência ASSINADO, trust anchor forçado out-of-band.
	anchor, err := base64.StdEncoding.DecodeString("tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=")
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	policyDP, err := pdp.Open("../control-plane/pdp/policies", pdp.WithTrustAnchor(ed25519.PublicKey(anchor)))
	if err != nil {
		t.Fatalf("pdp.Open: %v", err)
	}

	// IDENTIDADE REAL: o issuer computa Scope = UserAuthority ∩ ClassPolicy.Scope no mint.
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

	// REVALIDAÇÃO REAL: catálogo assinado + trust store.
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

	model := &scriptedModel{responses: []agentruntime.ModelResponse{
		{
			Text: "vou ler o documento",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "doc_read", Capability: capRead, Input: []byte(`{"doc_id":"notes"}`),
				ResourceType: "doc", ResourceValue: "notes", ResourceRegion: "eu",
			}},
		},
		{Text: "li o documento e concluo", Final: true},
	}}

	// *** O PONTO DO TESTE ***: Authority NÃO é fornecida (nil). NewSecuredRuntime cai em
	// authz.NewStaticAuthoritySource() VAZIA — o default do nó. Nenhum sujeito é resolúvel
	// pelo directório; a única autoridade possível é a que o hook de identidade deriva do token.
	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:       model,
		Recorder:    agentruntime.NewTurnRecorder(trajStore),
		Catalog:     catalog,
		Revalidator: rv,
		Policy:      StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:        audit.NewMemStore(),
		Verifier:    verifier,
		PDP:         policyDP,
		// Authority: DELIBERADAMENTE OMITIDA (nil ⇒ fonte estática vazia).
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	execCount := 0
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return []byte(`{"content":"Reuniao 3a: rever o plano de migracao."}`), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	goal := agentruntime.Goal{
		RunID: "run-token-only",
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

	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ASSERÇÃO CENTRAL: permit + execução real, com o directório de autoridade VAZIO. Antes do
	// fix (AOS-071/AOS-156) isto era deny|scope — a dobra colapsava para ∅.
	permits, denials, _ := sec.Metrics().Snapshot()
	if permits != 1 || denials != 0 {
		t.Fatalf("com Authority VAZIA, um NHI legítimo devia obter permit (a autoridade vem do token); veio permits=%d denials=%d", permits, denials)
	}
	if execCount != 1 {
		t.Fatalf("a tool devia ter EXECUTADO 1x sob permit; execCount=%d", execCount)
	}

	t.Logf("\n"+
		"  CONFIG     : SecuredConfig.Authority = nil  => AuthoritySource estática VAZIA (default do nó)\n"+
		"  CADEIA     : identity -> revalidation -> policy(PDP assinado) -> taint -> scope -> egress\n"+
		"  AUTORIDADE : derivada do Scope do token NHI verificado (issuer: UserAuthority ∩ ClassPolicy.Scope)\n"+
		"  RESULTADO  : permits=%d denials=%d, tool executou %dx\n"+
		"  SIGNIFICADO: sem o fix AOS-071/AOS-156 isto seria deny|scope (fonte vazia => escopo ∅)",
		permits, denials, execCount)
}

// TestScopeTokenOnly_CapForaDoTokenNegada é o CONTROLO NEGATIVO do teste acima: com a MESMA
// configuração (directório de autoridade VAZIO), uma capability que o token NÃO concede é
// NEGADA e a tool NÃO executa. Sem este controlo, o permit acima poderia ser vacuoso ("nada é
// verificado"); com ele fica provado que a fronteira continua a ser o escopo ASSINADO.
//
// NOTA HONESTA sobre qual gate nega: aqui a negação é apanhada logo pelo hook de IDENTIDADE
// (rmadapter.go: a capability pedida tem de estar no escopo do token), a montante do ScopeGate
// — defesa-em-profundidade a funcionar. O que este teste assevera é o INVARIANTE observável:
// uma capability fora do token não produz efeito.
func TestScopeTokenOnly_CapForaDoTokenNegada(t *testing.T) {
	ctx := context.Background()
	const (
		issuerID = "iss:test-idp"
		userID   = "human:alice"
		agentID  = "agt-token-only-neg"
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

	// O token é mintado SEM cap:fs.read — a classe concede-a, mas a autoridade do UTILIZADOR
	// não, e o mint intersecta (Scope = UserAuthority ∩ ClassPolicy.Scope = ∅ para fs.read).
	pub, priv := enfKeys(0x22)
	classes := map[string]identity.ClassPolicy{class: {TTL: 5 * time.Minute, Scope: []string{capRead, "cap:doc.list"}}}
	iss, err := identity.NewIssuer(issuerID, priv, classes, identity.WithIssuerClock(enfClock()))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier := identity.NewVerifier(identity.WithTrustedIssuer(issuerID, pub), identity.WithVerifierClock(enfClock()))
	tok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID: userID, AgentID: agentID, AgentClass: class,
		PolicyRef: "policy://agent-worker@1", UserAuthority: []string{"cap:doc.list"}, // NÃO inclui fs.read
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

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

	model := &scriptedModel{responses: []agentruntime.ModelResponse{
		{
			Text: "vou tentar ler o documento",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "doc_read", Capability: capRead, Input: []byte(`{"doc_id":"notes"}`),
				ResourceType: "doc", ResourceValue: "notes", ResourceRegion: "eu",
			}},
		},
		{Text: "nao consegui, concluo", Final: true},
	}}

	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:       model,
		Recorder:    agentruntime.NewTurnRecorder(trajStore),
		Catalog:     catalog,
		Revalidator: rv,
		Policy:      StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:        audit.NewMemStore(),
		Verifier:    verifier,
		PDP:         policyDP,
		// Authority: omitida, como no teste positivo.
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	execCount := 0
	if err := sec.Register("doc_read", func(_ context.Context, _ []byte) ([]byte, error) {
		execCount++
		return []byte(`{}`), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	goal := agentruntime.Goal{
		RunID: "run-token-only-neg",
		Principal: referencemonitor.Principal{
			NHIID: agentID, AgentID: agentID, AgentClass: class,
			DelegationChain: []referencemonitor.DelegationHop{{Sub: userID, ActAs: agentID}},
		},
		Credential: tok.Compact,
		Model:      agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
		System:     "assistente de leitura de documentos",
		Objective:  "le o documento notes",
	}

	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	permits, denials, _ := sec.Metrics().Snapshot()
	if permits != 0 || denials < 1 {
		t.Fatalf("uma capability FORA do escopo do token devia ser negada; veio permits=%d denials=%d", permits, denials)
	}
	if execCount != 0 {
		t.Fatalf("a tool NÃO devia ter executado (nenhum efeito sob deny); execCount=%d", execCount)
	}

	t.Logf("\n"+
		"  CONTROLO NEG: token SEM cap:fs.read (mint intersectou UserAuthority ∩ ClassPolicy.Scope)\n"+
		"  RESULTADO   : permits=%d denials=%d, tool executou %dx (nenhum efeito)\n"+
		"  SIGNIFICADO : o permit do teste positivo NÃO é vacuoso — a fronteira é o escopo ASSINADO",
		permits, denials, execCount)
}
