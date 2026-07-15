package promotion

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/substrate/eventstore"
)

// signWrongTuple assina o tuplo canónico (id, version, digest) com uma chave
// ARBITRÁRIA (potencialmente não confiável) e devolve a assinatura base64 — usado
// para forjar assinaturas inválidas nos testes de rejeição de integridade.
func signWrongTuple(priv ed25519.PrivateKey, id string, v domain.Version, dig string) string {
	sig := ed25519.Sign(priv, signing.SigningInput(id, v, dig))
	return base64.RawStdEncoding.EncodeToString(sig)
}

// --- determinismo ----------------------------------------------------------

// ver constrói uma versão pinada com campos nomeados.
func ver(mj, mn, p int) domain.Version {
	return domain.Version{Major: mj, Minor: mn, Patch: p}
}

// fixedClock devolve um relógio determinista (nunca time.Now numa decisão).
func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// keyFromSeed deriva um par ed25519 DETERMINÍSTICO de um seed de 1 byte (chaves de
// teste reproduzíveis; sem rand no caminho de decisão).
func keyFromSeed(b byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	return ed25519.NewKeyFromSeed(seed)
}

// --- harness ---------------------------------------------------------------

const (
	pubKeyID   = "pub:acme"
	ratifierID = "human:alice"
)

// harness compõe o Pipeline com peças REAIS de AOS-045/048 (Registry + signing.
// Verifier + trust store) sobre stores in-memory, mais o eval-gate e a allowlist de
// ratificadores. O eval-gate é controlável por evalFn.
type harness struct {
	t          *testing.T
	reg        *registry.Registry
	pipe       *Pipeline
	auditStore audit.Store
	signer     *signing.Signer
	trust      *signing.TrustStore
	integrity  registry.AdmissionVerifier
	ledger     *ApprovalLedger
	ratifiers  *RatifierStore
	ratPriv    ed25519.PrivateKey
	evalFn     func(id string, v domain.Version) (float64, int)
}

// pipelineWith constrói um Pipeline sobre o MESMO reg/integrity/ledger/audit do
// harness mas com as opções dadas (para exercitar wiring alternativo: sem eval-gate,
// sem ratificadores, partição/tracer custom).
func (h *harness) pipelineWith(opts ...PipelineOption) *Pipeline {
	h.t.Helper()
	base := append([]PipelineOption{WithClock(fixedClock())}, opts...)
	p, err := NewPipeline(h.reg, h.integrity, h.ledger, h.auditStore, base...)
	if err != nil {
		h.t.Fatalf("NewPipeline: %v", err)
	}
	return p
}

// evalMetrics é o adaptador do eval-gate para evalFn (mutável entre chamadas).
func (h *harness) evalMetrics(id string, v domain.Version) (float64, int) {
	if h.evalFn == nil {
		return 1.0, 0 // default: passa
	}
	return h.evalFn(id, v)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := fixedClock()

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	auditStore := audit.NewMemStore()

	trust, err := signing.NewTrustStore(auditStore, signing.WithTrustClock(clk))
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	signerPriv := keyFromSeed(1)
	signer, err := signing.NewSigner(pubKeyID, signerPriv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	if err := trust.Add(context.Background(), pubKeyID, signer.PublicKey()); err != nil {
		t.Fatalf("trust.Add: %v", err)
	}
	integrity, err := signing.NewVerifier(trust, auditStore, signing.WithVerifierClock(clk))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	ledger := NewApprovalLedger()
	reg, err := GovernedRegistry(store, integrity, ledger, nil, registry.WithClock(clk))
	if err != nil {
		t.Fatalf("GovernedRegistry: %v", err)
	}

	ratifiers := NewRatifierStore()
	ratPriv := keyFromSeed(2)
	if err := ratifiers.Authorize(ratifierID, ratPriv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	h := &harness{
		t:          t,
		reg:        reg,
		auditStore: auditStore,
		signer:     signer,
		trust:      trust,
		integrity:  integrity,
		ledger:     ledger,
		ratifiers:  ratifiers,
		ratPriv:    ratPriv,
	}

	evalGate := ThresholdEvalGate{
		MinGoldenSetScore:       0.9,
		MaxTraceDiffRegressions: 0,
		Metrics:                 h.evalMetrics,
	}
	pipe, err := NewPipeline(reg, integrity, ledger, auditStore,
		WithClock(clk),
		WithEvalGate(evalGate),
		WithRatifiers(ratifiers),
	)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	h.pipe = pipe
	return h
}

// contract devolve um contrato mínimo bem-formado com a classe de egress dada.
func contract(egress domain.EgressClass, scopes ...string) domain.Contract {
	return domain.Contract{Egress: egress, CredentialScopes: scopes}
}

// signedReq constrói um PublishRequest ASSINADO: calcula o digest canónico do
// (kind, contract), assina o tuplo (id, version, digest) com o signer do harness e
// preenche Publisher com o key id confiável.
func (h *harness) signedReq(id string, v domain.Version, kind domain.ArtifactKind, origin string, c domain.Contract) registry.PublishRequest {
	d := digest.SHA256Digester{}.Digest(kind, c)
	sig := h.signer.Sign(id, v, d)
	return registry.PublishRequest{
		ID:        id,
		Version:   v,
		Kind:      kind,
		Contract:  c,
		Origin:    origin,
		Publisher: pubKeyID,
		Signature: sig,
	}
}

// skillReq é um PublishRequest de SKILL AUTO-ESCRITA (origem "self").
func (h *harness) skillReq(id string, v domain.Version, c domain.Contract) registry.PublishRequest {
	return h.signedReq(id, v, domain.KindSkill, "self", c)
}

// toolReq é um PublishRequest de TOOL DE TERCEIROS.
func (h *harness) toolReq(id string, v domain.Version, c domain.Contract) registry.PublishRequest {
	return h.signedReq(id, v, domain.KindTool, "git+https://example/tools", c)
}

// ratify assina uma ratificação humana VÁLIDA para (id, version, digest).
func (h *harness) ratify(id string, v domain.Version, digestVal string) *Ratification {
	r := SignRatification(h.ratPriv, ratifierID, id, v, digestVal)
	return &r
}

// mustPublish publica e falha o teste em erro.
func (h *harness) mustPublish(req registry.PublishRequest) domain.Entry {
	h.t.Helper()
	e, err := h.pipe.Publish(context.Background(), req)
	if err != nil {
		h.t.Fatalf("Publish(%s): %v", req.ID, err)
	}
	return e
}

// isAdmissible é um atalho para o veredicto de despachabilidade do RM.
func (h *harness) isAdmissible(id string, v domain.Version) bool {
	ok, _, err := h.reg.IsAdmissible(context.Background(), id, v)
	if err != nil {
		h.t.Fatalf("IsAdmissible(%s): %v", id, err)
	}
	return ok
}

// auditStages devolve, por ordem, os stages (capabilities) selados na partição de
// promoção — a evidência de que cada transição entrou no WORM.
func (h *harness) auditStages() []string {
	head, err := h.auditStore.Head(context.Background(), DefaultPromotionPartition)
	if err != nil {
		h.t.Fatalf("audit Head: %v", err)
	}
	recs, err := h.auditStore.Read(context.Background(), DefaultPromotionPartition, 1, head)
	if err != nil {
		h.t.Fatalf("audit Read: %v", err)
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Capability)
	}
	return out
}

// hasStage indica se um stage foi selado no audit de promoção.
func (h *harness) hasStage(stage string) bool {
	want := capPromotionPrefix + stage
	for _, s := range h.auditStages() {
		if s == want {
			return true
		}
	}
	return false
}
