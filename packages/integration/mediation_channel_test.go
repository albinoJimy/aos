package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
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

// AOS-379 — CANAL DE EVENTOS DE MEDIAÇÃO alcançável como Event Store.
//
// Estes testes exercitam a PORTA nova ([SecuredConfig.MediationEvents]) SEMPRE pela via de
// PRODUÇÃO ([NewSecuredRuntime] → [referencemonitor.NewProductionSecure] → sec.Run), nunca
// construindo [referencemonitor.NewEventStoreSink] à mão — precisamente o anti-padrão em que o
// AOS-332 caiu (testar o caminho que produção não usa). A cadeia permitente é a MESMA de
// [TestDemo_PermitNodeEndToEnd]: bundle Cedar assinado committado + identidade demo-emitida +
// Authority user∩classe que concede cap:fs.read — todos os gates ACTIVOS, a call passa por ser
// legítima.

// mediationErrStore é um [eventstore.EventStore] cujo Append falha SEMPRE. Serve para provar o
// fail-closed do canal: o Event Store é o sink SECUNDÁRIO do TeeSink que [NewSecuredRuntime]
// compõe (o WORM é o primário), mas o tee propaga o erro de QUALQUER sink — logo uma falha a
// materializar o evento no caminho de PERMIT degrada a decisão para Deny (a tool nunca executa).
type mediationErrStore struct{ appends int }

func (s *mediationErrStore) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.appends++
	return eventstore.AppendResult{}, errors.New("event store de mediacao indisponivel (fail-closed)")
}
func (s *mediationErrStore) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return nil, nil
}
func (s *mediationErrStore) Subscribe(context.Context, eventstore.Filter, eventstore.Handler) (eventstore.Subscription, error) {
	return nil, errors.New("nao suportado")
}
func (s *mediationErrStore) Close() error { return nil }

// aos379PermitConfig devolve uma [SecuredConfig] permitente (a call cap:fs.read PASSA a cadeia
// real) SEM a porta MediationEvents preenchida — cada teste decide o que lhe injecta —, mais a
// credencial e o goal do run. O modelo emite a tool call `doc_read` no turno 1 e conclui no 2.
func aos379PermitConfig(t *testing.T) (SecuredConfig, string, agentruntime.Goal, func()) {
	t.Helper()
	ctx := context.Background()
	const (
		issuerID = "iss:test-idp"
		userID   = "human:alice"
		agentID  = "agt-1"
		class    = "agent-worker" // classe ALLOWLISTED para cap:fs.read no bundle de referência
		capRead  = "cap:fs.read"
	)

	// PDP REAL: bundle de referência ASSINADO, verificado contra o trust anchor out-of-band.
	anchor, err := base64.StdEncoding.DecodeString("tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=")
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	policyDP, err := pdp.Open("../control-plane/pdp/policies", pdp.WithTrustAnchor(ed25519.PublicKey(anchor)))
	if err != nil {
		t.Fatalf("pdp.Open (bundle de referência): %v", err)
	}

	// Identidade REAL: issuer → verifier → credencial NHI (cap:fs.read).
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

	// Revalidação REAL: catálogo assinado + trust store + revalidator.
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
		t.Fatalf("eventstore.New (trajectória): %v", err)
	}

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
		{Text: "concluo", Final: true, Usage: agentruntime.Usage{InputTokens: 6, OutputTokens: 3}},
	}}

	cfg := SecuredConfig{
		Model:         model,
		Recorder:      agentruntime.NewTurnRecorder(trajStore),
		Catalog:       catalog,
		Revalidator:   rv,
		Policy:        StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:          audit.NewMemStore(),
		Verifier:      verifier,
		Authority:     authority,
		PDP:           policyDP,
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
	}
	goal := agentruntime.Goal{
		RunID: "run-permit",
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
	return cfg, tok.Compact, goal, func() { _ = trajStore.Close() }
}

// TestAOS379_MediationChannel_CountsEvents é o AC6: pela via de PRODUÇÃO (NewSecuredRuntime com a
// porta preenchida + sec.Run) uma tool call mediada PERMITIDA passa a ser CONTÁVEL no Event Store
// do canal — tool.call.mediated. Antes de AOS-379 o Event Store não estava composto no sink de
// mediação e a contagem seria ZERO; com a porta preenchida é o nº de mediações.
func TestAOS379_MediationChannel_CountsEvents(t *testing.T) {
	ctx := context.Background()
	cfg, _, goal, cleanup := aos379PermitConfig(t)
	defer cleanup()

	medEvents, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New (canal de mediação): %v", err)
	}
	defer medEvents.Close()
	cfg.MediationEvents = medEvents // <-- a porta de AOS-379

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	execCount := 0
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execCount != 1 {
		t.Fatalf("a tool devia ter executado 1x (permit ponta-a-ponta); execCount=%d", execCount)
	}

	// CONTAGEM no Event Store do canal: pelo menos uma tool.call.mediated para a call permitida.
	events, err := medEvents.Read(ctx, goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read(%q): %v", goal.RunID, err)
	}
	mediated := 0
	for _, e := range events {
		if e.Type == "tool.call.mediated" {
			mediated++
		}
	}
	if mediated < 1 {
		t.Fatalf("tool.call.mediated no Event Store = %d, quero >=1 — o canal nao foi materializado pela via de producao (eventos: %+v)", mediated, events)
	}
}

// TestAOS379_MediationChannel_FailClosed é o AC3: com a porta preenchida por um Event Store cujo
// Append FALHA, o EventStoreSink (sink SECUNDÁRIO do TeeSink composto por NewSecuredRuntime) falha
// no caminho de PERMIT, o audit-before-effect do RM propaga o erro e a decisão degrada para DENY — a
// tool NUNCA executa. É a semântica fail-closed do TeeSink provada na composição de produção.
func TestAOS379_MediationChannel_FailClosed(t *testing.T) {
	ctx := context.Background()
	cfg, _, goal, cleanup := aos379PermitConfig(t)
	defer cleanup()

	badStore := &mediationErrStore{}
	cfg.MediationEvents = badStore

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	execCount := 0
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// O run termina limpo (o deny devolve resultado untrusted ao loop, o turno 2 conclui), mas a
	// tool não pode ter executado — o permit foi degradado por não se poder auditar no Event Store.
	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execCount != 0 {
		t.Fatalf("fail-closed violado: a tool executou (%dx) apesar de o Event Store de mediacao falhar — o permit devia ter degradado para deny", execCount)
	}
	if badStore.appends == 0 {
		t.Fatal("o EventStoreSink nunca foi chamado — a porta MediationEvents nao entrou no caminho de audit-before-effect")
	}
}

// wormErrStore é um [audit.Store] cujo Append falha SEMPRE (embute um MemStore para os restantes
// métodos). Serve o teste de regressão do achado adversarial: WORM em baixo, Event Store de pé.
type wormErrStore struct{ *audit.MemStore }

func (wormErrStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("WORM indisponivel (fail-closed)")
}

// TestAOS379_MediationChannel_WormDownNaoDeixaMediatedFalso é a REGRESSÃO do achado da revisão
// adversarial: com o WORM em baixo e o Event Store de pé, o canal NÃO pode ficar com um
// `tool.call.mediated` falso. Porque o WORM é o sink PRIMÁRIO do TeeSink, uma falha do WORM no
// caminho de permit PÁRA o tee ANTES de tocar o Event Store — logo o ES nunca ganha o `mediated`
// que depois seria contradito por um deny (e que a dedup por (run_id, step_id) do ES impediria de
// corrigir). Prova: a tool não executa (fail-closed) E o Event Store não tem NENHUM
// tool.call.mediated para a call negada.
func TestAOS379_MediationChannel_WormDownNaoDeixaMediatedFalso(t *testing.T) {
	ctx := context.Background()
	cfg, _, goal, cleanup := aos379PermitConfig(t)
	defer cleanup()

	cfg.WORM = wormErrStore{audit.NewMemStore()} // WORM em baixo (sink primário do tee)
	medEvents, err := eventstore.New()           // Event Store de pé (sink secundário)
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer medEvents.Close()
	cfg.MediationEvents = medEvents

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	execCount := 0
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Fail-closed: WORM em baixo ⇒ a mediação não pôde ser auditada ⇒ deny ⇒ a tool não executa.
	if execCount != 0 {
		t.Fatalf("fail-closed violado: a tool executou (%dx) com o WORM em baixo", execCount)
	}
	// E o essencial do achado: NENHUM tool.call.mediated falso no Event Store. Um erro de Read
	// (E_STREAM_NOT_FOUND) é o resultado ESPERADO — o WORM (sink primário) caiu, o tee parou antes
	// de tocar o ES, e o stream nem chegou a existir: trivialmente zero mediated.
	events, err := medEvents.Read(ctx, goal.RunID, 1)
	if err != nil {
		return // stream inexistente ⇒ o ES nunca foi tocado ⇒ sem mediated falso (fix confirmado)
	}
	for _, e := range events {
		if e.Type == "tool.call.mediated" {
			t.Fatalf("REGRESSÃO: o Event Store ficou com um tool.call.mediated FALSO de uma call negada (WORM em baixo) — a ordem do tee não protege o ES; eventos=%+v", events)
		}
	}
}

// TestAOS379_MediationChannel_NilControl é o AC4 (controlo negativo): com a porta NIL o
// comportamento NÃO muda — o sink de mediação é só o WORM (audit.NewMediationSink), o TeeSink não
// é composto, e a MESMA call permitida executa exactamente como antes de AOS-379. É a prova de que
// a porta é ADITIVA e retro-compatível.
func TestAOS379_MediationChannel_NilControl(t *testing.T) {
	ctx := context.Background()
	cfg, _, goal, cleanup := aos379PermitConfig(t)
	defer cleanup()
	// cfg.MediationEvents deixado NIL de propósito — o estado de todo o caminho pré-AOS-379.

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	execCount := 0
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execCount != 1 {
		t.Fatalf("com a porta nil o comportamento devia ser o de sempre (tool executa 1x); execCount=%d", execCount)
	}
}
