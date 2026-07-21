package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// Estes testes exercitam o WIRING REAL do par de segurança (AOS-050 + AOS-051)
// através do loop base real do Agent Runtime (AOS-013) e do Reference Monitor real
// (AOS-003): congelamento do tool set por run, revalidação criptográfica por
// chamada e a garantia de mediação total — uma tool call cuja definição divergiu do
// congelado NÃO pode executar.

const testKeyID = "pub:test-publisher"

// fixedTime é o relógio DETERMINÍSTICO dos testes (constante; nunca time.Now num
// caminho de decisão). Os carimbos de audit/freeze são observacionais.
func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// testSigner constrói um assinante Ed25519 DETERMINÍSTICO (seed fixo) — sem
// aleatoriedade, para reprodutibilidade.
func testSigner(t *testing.T) *signing.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	s, err := signing.NewSigner(testKeyID, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// signedEntry constrói uma [domain.Entry] COERENTE: digest = SHA-256 do contrato e
// assinatura sobre (id, version, digest) com a chave do publicador. É o que um
// artefacto legítimo e admitido no REG teria.
func signedEntry(t *testing.T, signer *signing.Signer, id, ver string, contract domain.Contract) domain.Entry {
	t.Helper()
	v, err := domain.ParseVersion(ver)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", ver, err)
	}
	dig := digest.SHA256Digester{}.Digest(domain.KindTool, contract)
	return domain.Entry{
		ID:        id,
		Version:   v,
		Kind:      domain.KindTool,
		Digest:    dig,
		Signature: signer.Sign(id, v, dig),
		Contract:  contract,
		Provenance: domain.Provenance{
			Origin:    "mcp://test",
			Publisher: signer.KeyID(),
			Timestamp: "2023-11-14T00:00:00Z",
		},
		Status: domain.StatusActive,
	}
}

// fakeCatalog é um [toolset.Catalog] em memória, mutável entre congelamentos (para
// simular o REG a evoluir). Devolve clones (o congelamento nunca partilha o array).
type fakeCatalog struct{ entries []domain.Entry }

func (c *fakeCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	out := make([]domain.Entry, len(c.entries))
	for i, e := range c.entries {
		out[i] = e.Clone()
	}
	return out, nil
}

// staticResolver é um [CurrentResolver] que devolve sempre a MESMA definição — o
// veículo para injectar uma definição ACTUAL divergente (rug-pull) sem depender de
// mutar o catálogo a meio de um Run().
type staticResolver struct {
	entry   domain.Entry
	present bool
	err     error
}

func (r staticResolver) Current(context.Context, string) (domain.Entry, bool, error) {
	return r.entry, r.present, r.err
}

// scriptedModel é um [agentruntime.ModelClient] determinístico indexado por turno,
// que também captura o prefixo materializado de cada turno (para asserção de
// estabilidade/igualdade de prefixo).
type scriptedModel struct {
	responses []agentruntime.ModelResponse
	prefixes  [][]byte
}

func (m *scriptedModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.prefixes = append(m.prefixes, append([]byte(nil), view.Prefix...))
	i := view.Turn - 1
	if i < 0 || i >= len(m.responses) {
		return agentruntime.ModelResponse{}, fmt.Errorf("turno %d fora do guião (%d respostas)", view.Turn, len(m.responses))
	}
	return m.responses[i], nil
}

// toolThenFinal é o guião de dois turnos: turno 1 chama toolID; turno 2 responde
// final. Serve tanto o caminho permitido (a tool executa e o turno 2 conclui) como
// o bloqueado (a tool NÃO executa, o resultado vazio untrusted vai ao tail e o
// turno 2 conclui na mesma) — em ambos o run termina limpo em 2 turnos.
func toolThenFinal(toolID string, input []byte) []agentruntime.ModelResponse {
	return []agentruntime.ModelResponse{
		{
			Text:      "chamo a tool",
			ToolCalls: []agentruntime.ToolInvocation{{ToolID: toolID, Capability: "cap:echo", Input: input}},
			Usage:     agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{Text: "feito", Final: true, Usage: agentruntime.Usage{InputTokens: 4, OutputTokens: 2}},
	}
}

// testGoal é o goal base (sem Tools — o freeze materializa-os).
func testGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID: runID,
		Principal: referencemonitor.Principal{
			NHIID:      "nhi:test",
			AgentID:    "agent-test",
			AgentClass: "researcher",
			Authority:  []string{"cap:echo"},
		},
		Scope:     []string{"cap:echo"},
		Model:     agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
		System:    "sistema de teste",
		Objective: "faz echo",
	}
}

// newTrust constrói um trust store com a chave pública do assinante confiada.
func newTrust(t *testing.T, ctx context.Context, auditStore audit.Store, signer *signing.Signer) *signing.TrustStore {
	t.Helper()
	trust, err := signing.NewTrustStore(auditStore, signing.WithTrustClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	if err := trust.Add(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("trust.Add: %v", err)
	}
	return trust
}

// newRevalidator constrói o revalidador com quarentena e alerta ligados.
func newRevalidator(t *testing.T, trust revalidation.TrustStore, auditStore audit.Store, quar revalidation.Quarantiner, alerter revalidation.Alerter) *revalidation.Revalidator {
	t.Helper()
	rv, err := revalidation.New(trust, auditStore,
		revalidation.WithQuarantiner(quar),
		revalidation.WithAlerter(alerter),
		revalidation.WithClock(fixedClock()),
	)
	if err != nil {
		t.Fatalf("revalidation.New: %v", err)
	}
	return rv
}

// TestTotalMediation_DriftedToolCannotExecute é a PROVA central de AOS-051: uma
// tool cuja definição ACTUAL divergiu da congelada (digest recalculado ≠ esperado —
// um rug-pull) é BLOQUEADA na mediação e NUNCA é despachada. Corre o loop real.
func TestTotalMediation_DriftedToolCannotExecute(t *testing.T) {
	ctx := context.Background()
	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)

	// Definição legítima congelada.
	pristine := signedEntry(t, signer, "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	toolsets := NewRunToolSets()
	frozen, err := toolset.FreezeToolSet(ctx, &fakeCatalog{entries: []domain.Entry{pristine}}, "run-drift", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	toolsets.Put(frozen)

	// Rug-pull: a definição ACTUAL mantém id/version/assinatura mas mutou o contrato
	// (scope novo) — o digest recalculado sobre os bytes reais deixa de casar com o
	// congelado. É a divergência que a revalidação existe para apanhar.
	tampered := pristine.Clone()
	tampered.Contract.CredentialScopes = []string{"vault:db.write"}

	quar := NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock()))
	alerter := NewRecordingAlerter()
	rv := newRevalidator(t, trust, auditStore, quar, alerter)

	hook, err := NewRevalidationHook(rv, toolsets, staticResolver{entry: tampered, present: true},
		StaticPolicy{MaxEgress: domain.EgressExternal})
	if err != nil {
		t.Fatalf("NewRevalidationHook: %v", err)
	}

	rmStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer rmStore.Close()
	rm := referencemonitor.New(
		referencemonitor.WithHooks(
			referencemonitor.IdentityStub{}, hook, referencemonitor.PolicyStub{},
			referencemonitor.BudgetStub{}, referencemonitor.EgressStub{}, referencemonitor.AuditStub{},
		),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(rmStore)),
	)

	execCount := 0
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer trajStore.Close()
	model := &scriptedModel{responses: toolThenFinal("echo", []byte("ola"))}
	rt := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(trajStore))

	goal := testGoal("run-drift")
	goal.Tools = frozen.Specs()

	res, err := rt.Run(ctx, goal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 2 {
		t.Fatalf("desfecho inesperado: terminated=%v turns=%d", res.Terminated, res.Turns)
	}

	// MEDIAÇÃO TOTAL: a tool NUNCA correu.
	if execCount != 0 {
		t.Fatalf("tool não-revalidada EXECUTOU (execCount=%d) — mediação total violada", execCount)
	}
	permits, denials, _ := rm.Metrics().Snapshot()
	if permits != 0 {
		t.Fatalf("permits=%d, esperado 0 (a tool divergente não devia ser permitida)", permits)
	}
	if denials < 1 {
		t.Fatalf("denials=%d, esperado >=1 (o bloqueio devia contar como negação)", denials)
	}

	// O resultado devolvido ao loop é untrusted e vazio (deny → sem output).
	if len(res.ToolResults) != 1 {
		t.Fatalf("esperava 1 tool result, tenho %d", len(res.ToolResults))
	}
	if len(res.ToolResults[0].Value) != 0 {
		t.Fatalf("tool result não-vazio num deny: %q", res.ToolResults[0].Value)
	}

	// DIVERGÊNCIA: alerta emitido e artefacto em quarentena.
	alerts := alerter.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("esperava 1 alerta, tenho %d", len(alerts))
	}
	if alerts[0].Reason != revalidation.ReasonDigestMismatch || alerts[0].Stage != revalidation.StageDigest {
		t.Fatalf("alerta inesperado: stage=%s reason=%s", alerts[0].Stage, alerts[0].Reason)
	}
	if n := quar.Partition().Quarantine().Len(); n != 1 {
		t.Fatalf("esperava 1 artefacto em quarentena, tenho %d", n)
	}

	// A decisão de bloqueio foi selada no audit de revalidação (tamper-evident).
	assertAuditHas(t, ctx, auditStore, revalidation.DefaultPartition, audit.DecisionDeny, "echo")
}

// TestSecuredRuntime_PristineToolExecutes é o caminho FELIZ ponta-a-ponta via o
// composition root [SecuredRuntime]: sem divergência, a tool passa a revalidação e é
// despachada. Prova também a materialização do freeze (AOS-050) no prefixo imutável.
func TestSecuredRuntime_PristineToolExecutes(t *testing.T) {
	ctx := context.Background()
	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)

	entry := signedEntry(t, signer, "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	catalog := &fakeCatalog{entries: []domain.Entry{entry}}

	quar := NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock()))
	alerter := NewRecordingAlerter()
	rv := newRevalidator(t, trust, auditStore, quar, alerter)

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer trajStore.Close()
	rmStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer rmStore.Close()

	model := &scriptedModel{responses: toolThenFinal("echo", []byte("ola"))}
	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:         model,
		Recorder:      agentruntime.NewTurnRecorder(trajStore),
		Catalog:       catalog,
		Revalidator:   rv,
		Policy:        StaticPolicy{MaxEgress: domain.EgressExternal},
		EventSink:     referencemonitor.NewEventStoreSink(rmStore),
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	execCount := 0
	if err := sec.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	goal := testGoal("run-ok")
	res, frozen, err := sec.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 2 {
		t.Fatalf("desfecho inesperado: terminated=%v turns=%d", res.Terminated, res.Turns)
	}
	if execCount != 1 {
		t.Fatalf("a tool devia ter corrido exactamente uma vez, execCount=%d", execCount)
	}
	permits, denials, _ := sec.Metrics().Snapshot()
	if permits < 1 || denials != 0 {
		t.Fatalf("métricas inesperadas: permits=%d denials=%d", permits, denials)
	}
	if alerter.Len() != 0 {
		t.Fatalf("não deviam existir alertas no caminho feliz, tenho %d", alerter.Len())
	}

	// AOS-050 materializado: o prefixo do loop é BYTE-idêntico ao do assembler do
	// snapshot congelado (mesmo system + mesmo tool set na ordem congelada).
	if frozen.Len() != 1 {
		t.Fatalf("frozen.Len=%d, esperado 1", frozen.Len())
	}
	if len(model.prefixes) == 0 {
		t.Fatalf("o modelo não capturou nenhum prefixo")
	}
	wantPrefix := frozen.Assembler(goal.System).Prefix()
	if !bytes.Equal(model.prefixes[0], wantPrefix) {
		t.Fatalf("o prefixo do loop diverge do tool set congelado:\n loop=%q\nfrozen=%q", model.prefixes[0], wantPrefix)
	}
	// Prefixo estável entre turnos (cache-hit).
	if len(model.prefixes) == 2 && !bytes.Equal(model.prefixes[0], model.prefixes[1]) {
		t.Fatalf("prefixo instável entre turnos (regressão de cache)")
	}

	// A decisão de DESPACHO foi selada no audit de revalidação.
	assertAuditHas(t, ctx, auditStore, revalidation.DefaultPartition, audit.DecisionAllow, "echo")
}

// TestTotalMediation_UnfrozenRunDenied prova o default-deny do arranque: um run cujo
// tool set NÃO foi congelado (sem entrada em [RunToolSets]) não executa tool nenhuma
// — não há expectativa contra a qual revalidar.
func TestTotalMediation_UnfrozenRunDenied(t *testing.T) {
	ctx := context.Background()
	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)

	quar := NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock()))
	alerter := NewRecordingAlerter()
	rv := newRevalidator(t, trust, auditStore, quar, alerter)

	// Registo de tool sets VAZIO — o run nunca foi congelado.
	toolsets := NewRunToolSets()
	entry := signedEntry(t, signer, "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	hook, err := NewRevalidationHook(rv, toolsets, staticResolver{entry: entry, present: true},
		StaticPolicy{MaxEgress: domain.EgressExternal})
	if err != nil {
		t.Fatalf("NewRevalidationHook: %v", err)
	}

	rmStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer rmStore.Close()
	rm := referencemonitor.New(
		referencemonitor.WithHooks(
			referencemonitor.IdentityStub{}, hook, referencemonitor.PolicyStub{},
			referencemonitor.BudgetStub{}, referencemonitor.EgressStub{}, referencemonitor.AuditStub{},
		),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(rmStore)),
	)
	execCount := 0
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer trajStore.Close()
	model := &scriptedModel{responses: toolThenFinal("echo", []byte("x"))}
	rt := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(trajStore))

	goal := testGoal("run-unfrozen")
	goal.Tools = []agentruntime.ToolSpec{{Name: "echo", Version: "1.0.0", Digest: entry.Digest}}

	res, err := rt.Run(ctx, goal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("run não terminou")
	}
	if execCount != 0 {
		t.Fatalf("tool executou num run não-congelado (execCount=%d)", execCount)
	}
	if _, denials, _ := rm.Metrics().Snapshot(); denials < 1 {
		t.Fatalf("esperava >=1 negação, denials=%d", denials)
	}
}

// TestFreeze_PerRunImmutable prova que o snapshot congelado é POR RUN e imune a
// mudanças posteriores no catálogo: congelar o run B depois de o REG evoluir não
// altera o snapshot do run A.
func TestFreeze_PerRunImmutable(t *testing.T) {
	ctx := context.Background()
	signer := testSigner(t)

	e1 := signedEntry(t, signer, "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	cat := &fakeCatalog{entries: []domain.Entry{e1}}

	fzA, err := toolset.FreezeToolSet(ctx, cat, "runA", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet A: %v", err)
	}

	// O REG evolui: nova versão do mesmo id, contrato diferente → digest diferente.
	e2 := signedEntry(t, signer, "echo", "2.0.0", domain.Contract{Egress: domain.EgressInternal})
	cat.entries = []domain.Entry{e2}

	fzB, err := toolset.FreezeToolSet(ctx, cat, "runB", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet B: %v", err)
	}

	expA, ok := fzA.Expectation("echo")
	if !ok {
		t.Fatalf("runA sem expectativa para echo")
	}
	if expA.Version.String() != "1.0.0" || expA.Digest != e1.Digest {
		t.Fatalf("runA mutou após o freeze de runB: version=%s digest=%s", expA.Version, expA.Digest)
	}
	expB, ok := fzB.Expectation("echo")
	if !ok {
		t.Fatalf("runB sem expectativa para echo")
	}
	if expB.Version.String() != "2.0.0" || expB.Digest != e2.Digest {
		t.Fatalf("runB não reflecte a nova definição: version=%s digest=%s", expB.Version, expB.Digest)
	}
	if fzA.Hash() == fzB.Hash() {
		t.Fatalf("snapshots de runs diferentes têm o mesmo hash (%s) — deviam divergir", fzA.Hash())
	}
}

// assertAuditHas falha se a partição de audit não contiver um registo com a decisão
// e o toolID esperados. Confirma que a decisão de revalidação foi de facto selada.
func assertAuditHas(t *testing.T, ctx context.Context, store audit.Store, partition string, want audit.Decision, toolID string) {
	t.Helper()
	head, err := store.Head(ctx, partition)
	if err != nil {
		t.Fatalf("audit.Head(%s): %v", partition, err)
	}
	if head == 0 {
		t.Fatalf("audit partition %q vazia — nenhuma decisão selada", partition)
	}
	recs, err := store.Read(ctx, partition, 1, head)
	if err != nil {
		t.Fatalf("audit.Read(%s): %v", partition, err)
	}
	for _, r := range recs {
		if r.Decision == want && r.ToolID == toolID {
			return
		}
	}
	t.Fatalf("audit partition %q sem registo decision=%s toolID=%s (%d registos)", partition, want, toolID, len(recs))
}
