package hitl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// --- Fixtures de ratificação ---

// passingEval devolve um EvaluationResult que o FailClosedGate ADMITE (pass, score
// acima do limiar, ligado a um trace-alvo e a um eval-id).
func passingEval() otelgenai.EvaluationResult {
	return otelgenai.EvaluationResult{
		Suite:         "golden.summarize",
		EvalID:        "eval-abc-123",
		Dataset:       otelgenai.EvalDatasetGolden,
		Verdict:       otelgenai.EvalPass,
		Score:         0.95,
		TargetTraceID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	}
}

// passingArtifact devolve um artefacto que passou eval-gate + canary (pré-condição OK).
func passingArtifact() SelfModArtifact {
	return SelfModArtifact{
		ID:           "skill.summarize",
		Kind:         ArtifactSkill,
		Version:      "1.4.0",
		EvalResult:   passingEval(),
		CanaryPassed: true,
		ContentHash:  []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

// signRatificationFor assina (via fakeVault) uma ratificação ligada à identidade do
// artefacto (RequestID = artifact.RatificationID) — reutiliza SignApproval de AOS-095.
func signRatificationFor(t *testing.T, vault *fakeVault, ratifier string, approved bool, artifact SelfModArtifact) SignedApproval {
	t.Helper()
	a := SignedApproval{
		RequestID: artifact.RatificationID(),
		Approver:  ratifier,
		Approved:  approved,
		Nonce:     newNonce(t),
		IssuedAt:  fixedClock()(),
	}
	signed, err := SignApproval(context.Background(), vault, a)
	if err != nil {
		t.Fatalf("SignApproval: %v", err)
	}
	return signed
}

// ratHarness reúne o gate e os colaboradores para os testes.
type ratHarness struct {
	gate     *RatificationGate
	store    *audit.MemStore
	registry *MemApproverRegistry
	vault    *fakeVault
}

// newRatHarness constrói um gate com um ratificador AUTORIZADO registado ("ratifier")
// e o eval-gate FailClosedGate (limiar 0.8).
func newRatHarness(t *testing.T) ratHarness {
	t.Helper()
	vault := newFakeVault()
	registry := NewMemApproverRegistry()
	pub := vault.provision("ratifier", 0x11)
	registry.Register("ratifier", pub, DefaultRatifyAuthority)

	store := audit.NewMemStore()
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		registry, store,
		WithRatifyClock(fixedClock()),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	return ratHarness{gate: gate, store: store, registry: registry, vault: vault}
}

// decisionObligation devolve os Params da obrigação "ratification_decision" do único (ou
// último) registo na partição, mais o Decision do registo.
func decisionObligation(t *testing.T, store *audit.MemStore, partition string) (audit.Decision, map[string]string, audit.Principal, bool) {
	t.Helper()
	recs, err := store.Read(context.Background(), partition, 1, 1<<62)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) == 0 {
		return "", nil, audit.Principal{}, false
	}
	rec := recs[len(recs)-1]
	for _, ob := range rec.Obligations {
		if ob.Type == "ratification_decision" {
			return rec.Decision, ob.Params, rec.Principal, true
		}
	}
	return rec.Decision, nil, rec.Principal, true
}

func hasObligation(recs []audit.AuditRecord, typ string) bool {
	for _, r := range recs {
		for _, ob := range r.Obligations {
			if ob.Type == typ {
				return true
			}
		}
	}
	return false
}

// --- Testes ---

// TestNewRatificationGate_NilDeps: dependências obrigatórias em falta ⇒ fail-closed.
func TestNewRatificationGate_NilDeps(t *testing.T) {
	eg := otelgenai.FailClosedGate{}
	reg := NewMemApproverRegistry()
	st := audit.NewMemStore()
	cases := []struct {
		name string
		eval otelgenai.EvalGate
		reg  ApproverRegistry
		st   audit.Store
	}{
		{"nil-eval", nil, reg, st},
		{"nil-registry", eg, nil, st},
		{"nil-sealer", eg, reg, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewRatificationGate(c.eval, c.reg, c.st)
			if !errors.Is(err, ErrNilDeps) {
				t.Fatalf("esperado ErrNilDeps, obtido %v", err)
			}
		})
	}
}

// TestRatify_FailClosed_SemRatificacao: um artefacto que passou a pré-condição mas SEM
// ratificação assinada NÃO chega a prod (admit=false, fica em canary/staging). É o AC
// central: sem ratificação, a promoção é bloqueada fail-closed.
func TestRatify_FailClosed_SemRatificacao(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()

	// Ratificação AUSENTE (SignedApproval zero) — nenhuma assinatura.
	admit, err := h.gate.Ratify(context.Background(), art, SignedApproval{})
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("FAIL-CLOSED violado: promoveu sem ratificação assinada")
	}

	// O bloqueio é auditável na quarentena (não há principal autenticado).
	dec, params, principal, ok := decisionObligation(t, h.store, partitionUnratified)
	if !ok {
		t.Fatal("bloqueio não selado no audit")
	}
	if dec != audit.DecisionDeny {
		t.Fatalf("esperado deny, obtido %v", dec)
	}
	if principal.NHIID != "" {
		t.Fatalf("bloqueio não deve ter principal autenticado, obtido %q", principal.NHIID)
	}
	if params["reason"] != ReasonRatifierUnknown {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_RatificacaoAssinada_Promove: a promoção (admit=true) só ocorre após uma
// assinatura VERIFICÁVEL de um humano responsável AUTORIZADO.
func TestRatify_RatificacaoAssinada_Promove(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)

	admit, err := h.gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if !admit {
		t.Fatal("ratificação assinada válida devia promover (admit=true)")
	}

	dec, params, principal, ok := decisionObligation(t, h.store, "ratification:"+art.ID)
	if !ok {
		t.Fatal("ratificação não selada")
	}
	if dec != audit.DecisionAllow {
		t.Fatalf("esperado allow, obtido %v", dec)
	}
	if principal.NHIID != "ratifier" {
		t.Fatalf("ratificação deve ser atribuível ao ratificador, obtido %q", principal.NHIID)
	}
	if params["reason"] != ReasonRatified {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_AssinaturaForjada_Bloqueia: uma assinatura feita por OUTRA chave (não a
// pinada do ratificador) NÃO valida ⇒ block fail-closed.
func TestRatify_AssinaturaForjada_Bloqueia(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()

	// O atacante assina com material PRÓPRIO mas clama ser "ratifier".
	h.vault.provision("attacker-key", 0x99)
	forged := SignedApproval{
		RequestID: art.RatificationID(),
		Approver:  "ratifier", // clama o ratificador legítimo…
		Approved:  true,
		Nonce:     newNonce(t),
		IssuedAt:  fixedClock()(),
	}
	sig, err := h.vault.Sign(context.Background(), "attacker-key", canonicalApproval(forged))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	forged.Signature = sig

	admit, err := h.gate.Ratify(context.Background(), art, forged)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("assinatura forjada não devia promover")
	}
	_, params, _, _ := decisionObligation(t, h.store, partitionUnratified)
	if params["reason"] != ReasonRatificationForged {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_NaoAutorizado_Bloqueia: um ratificador AUTÊNTICO mas cuja autoridade NÃO
// cobre a ratificação de produção é recusado (AC2).
func TestRatify_NaoAutorizado_Bloqueia(t *testing.T) {
	h := newRatHarness(t)
	// Regista um segundo humano SEM a capability "ratify:production".
	pub := h.vault.provision("observer", 0x22)
	h.registry.Register("observer", pub, "read:audit")
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "observer", true, art)

	admit, err := h.gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("ratificador sem autoridade não devia promover")
	}
	_, params, _, _ := decisionObligation(t, h.store, partitionUnratified)
	if params["reason"] != ReasonRatifierUnauthorized {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_RecusaAssinada_Bloqueia: uma RECUSA assinada (Approved=false) verificável é
// não-repúdio, é selada e atribuível ao ratificador — mas a promoção é DENY.
func TestRatify_RecusaAssinada_Bloqueia(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", false, art)

	admit, err := h.gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("recusa assinada não devia promover")
	}
	dec, params, principal, _ := decisionObligation(t, h.store, "ratification:"+art.ID)
	if dec != audit.DecisionDeny {
		t.Fatalf("esperado deny, obtido %v", dec)
	}
	if principal.NHIID != "ratifier" {
		t.Fatalf("recusa assinada deve ser atribuível ao ratificador, obtido %q", principal.NHIID)
	}
	if params["reason"] != ReasonRatificationRefused {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_PrecondicaoEvalFalhada_NaoApresenta: um artefacto que FALHOU o eval-gate
// NÃO é apresentado a ratificação — bloqueia ANTES dela, mesmo com uma assinatura
// válida em mãos (AC4). Prova-se que o registo NEM é consultado.
func TestRatify_PrecondicaoEvalFalhada_NaoApresenta(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	art.EvalResult.Verdict = otelgenai.EvalFail // reprovado no eval-gate

	// Mesmo com uma ratificação assinada válida, não deve ser apresentada.
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	admit, err := h.gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("artefacto reprovado no eval-gate não devia ser promovível")
	}
	_, params, principal, _ := decisionObligation(t, h.store, partitionUnratified)
	if params["reason"] != ReasonPreconditionFailed {
		t.Fatalf("motivo esperado precondition_failed, obtido %q", params["reason"])
	}
	if principal.NHIID != "" {
		t.Fatal("bloqueio de pré-condição não deve autenticar ratificador")
	}
}

// TestRatify_PrecondicaoCanaryFalhado_NaoApresenta: canary reprovado ⇒ block antes da
// ratificação (AC4), ainda que o eval-gate admita.
func TestRatify_PrecondicaoCanaryFalhado_NaoApresenta(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	art.CanaryPassed = false

	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	admit, err := h.gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("artefacto que falhou o canary não devia ser promovível")
	}
	_, params, _, _ := decisionObligation(t, h.store, partitionUnratified)
	if params["reason"] != ReasonPreconditionFailed {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_Auditoria_LigaEvalESemVer: a ratificação selada liga QUEM ratificou, a
// versão SemVer, o resultado do eval (verdict/score/eval-id/trace-alvo) e o timestamp,
// mais a assinatura de não-repúdio (AC5).
func TestRatify_Auditoria_LigaEvalESemVer(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)

	if _, err := h.gate.Ratify(context.Background(), art, signed); err != nil {
		t.Fatalf("Ratify: %v", err)
	}

	recs, err := h.store.Read(context.Background(), "ratification:"+art.ID, 1, 1<<62)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("esperado 1 registo, obtido %d", len(recs))
	}
	rec := recs[0]
	if rec.PolicyVersion != art.Version {
		t.Fatalf("versão SemVer não selada: %q", rec.PolicyVersion)
	}
	if rec.Timestamp.IsZero() {
		t.Fatal("timestamp não selado")
	}

	var dec map[string]string
	for _, ob := range rec.Obligations {
		if ob.Type == "ratification_decision" {
			dec = ob.Params
		}
	}
	if dec == nil {
		t.Fatal("obrigação ratification_decision ausente")
	}
	if dec["eval_id"] != art.EvalResult.EvalID {
		t.Fatalf("eval_id não ligado: %q", dec["eval_id"])
	}
	if dec["eval_verdict"] != string(art.EvalResult.Verdict) {
		t.Fatalf("eval_verdict não ligado: %q", dec["eval_verdict"])
	}
	if dec["eval_target_trace_id"] != art.EvalResult.TargetTraceIDHex() {
		t.Fatalf("trace-alvo do eval não ligado: %q", dec["eval_target_trace_id"])
	}
	if dec["version"] != art.Version {
		t.Fatalf("SemVer não ligado na obrigação: %q", dec["version"])
	}
	if dec["eval_result_attribute"] != string(otelgenai.OpEvaluation) {
		t.Fatalf("ligação a gen_ai.evaluation.result ausente: %q", dec["eval_result_attribute"])
	}
	// Não-repúdio: a assinatura tem de estar selada e re-verificável.
	if !hasObligation(recs, "ratification_signature") {
		t.Fatal("assinatura de não-repúdio não selada")
	}
	// Sem segredos: o conteúdo do artefacto nunca é selado, só o seu hash.
	for _, ob := range rec.Obligations {
		for k, v := range ob.Params {
			if k == "content" || k == "artifact_content" {
				t.Fatalf("conteúdo do artefacto vazado no audit: %q=%q", k, v)
			}
		}
	}
}

// TestRatify_AntiTransplante: uma ratificação assinada de um artefacto NÃO vale para
// OUTRO — o canónico amarra o artefacto+eval. Transplantar a assinatura de A para B é
// bloqueado.
func TestRatify_AntiTransplante(t *testing.T) {
	h := newRatHarness(t)
	artA := passingArtifact()
	artB := passingArtifact()
	artB.ID = "skill.translate"
	artB.Version = "2.0.0"
	artB.EvalResult.EvalID = "eval-xyz-999"

	if artA.RatificationID() == artB.RatificationID() {
		t.Fatal("pré-condição do teste: os artefactos deviam ter identidades distintas")
	}

	// Ratificação legítima, assinada, PARA A.
	signedForA := signRatificationFor(t, h.vault, "ratifier", true, artA)

	// Tentativa de a usar para promover B (transplante).
	admit, err := h.gate.Ratify(context.Background(), artB, signedForA)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("ANTI-TRANSPLANTE violado: ratificação de A promoveu B")
	}
	_, params, _, _ := decisionObligation(t, h.store, partitionUnratified)
	if params["reason"] != ReasonRatificationTransplant {
		t.Fatalf("motivo esperado transplant, obtido %q", params["reason"])
	}

	// Sanidade: a mesma ratificação promove A (a assinatura é válida para A).
	admitA, err := h.gate.Ratify(context.Background(), artA, signedForA)
	if err != nil {
		t.Fatalf("Ratify A: %v", err)
	}
	if !admitA {
		t.Fatal("a ratificação legítima de A devia promover A")
	}
}

// TestRatify_PartitionerCustom: WithRatifyPartitioner redireciona a cadeia de audit da
// ratificação verificada para a partição derivada do artefacto.
func TestRatify_PartitionerCustom(t *testing.T) {
	h := newRatHarness(t)
	store := audit.NewMemStore()
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		h.registry, store,
		WithRatifyClock(fixedClock()),
		WithRatifyPartitioner(func(a SelfModArtifact) string { return "prod-chain/" + string(a.Kind) + "/" + a.ID }),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	admit, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if !admit {
		t.Fatal("ratificação válida devia promover")
	}
	dec, _, principal, ok := decisionObligation(t, store, "prod-chain/skill/"+art.ID)
	if !ok || dec != audit.DecisionAllow || principal.NHIID != "ratifier" {
		t.Fatalf("registo na partição custom ausente/incorrecto: ok=%v dec=%v principal=%q", ok, dec, principal.NHIID)
	}
}

// TestRatify_Malformada_Bloqueia: uma decisão com nonce curto/timestamp zero/assinatura
// truncada é malformada ⇒ block (mesmo com RequestID correcto).
func TestRatify_Malformada_Bloqueia(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	signed.Nonce = signed.Nonce[:4] // nonce demasiado curto

	admit, err := h.gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("decisão malformada não devia promover")
	}
	_, params, _, _ := decisionObligation(t, h.store, partitionUnratified)
	if params["reason"] != ReasonRatificationMalformed {
		t.Fatalf("motivo inesperado: %q", params["reason"])
	}
}

// TestRatify_SelagemFalha_ForcaDeny: se a selagem no audit falhar, a promoção é forçada
// a DENY (audit-before-effect) mesmo com ratificação válida. Reutiliza o failingStore
// de channel_test.go (audit.Store cujo Append falha sempre).
func TestRatify_SelagemFalha_ForcaDeny(t *testing.T) {
	vault := newFakeVault()
	registry := NewMemApproverRegistry()
	pub := vault.provision("ratifier", 0x11)
	registry.Register("ratifier", pub, DefaultRatifyAuthority)

	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		registry, failingStore{},
		WithRatifyClock(fixedClock()),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, vault, "ratifier", true, art)

	admit, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("audit-before-effect violado: promoveu com selagem falhada")
	}
}

// memNonceStore é um RatificationNonceStore de referência para os testes: consome
// atomicamente (scope, nonce) e reporta se era fresco. Uso-único por (scope+nonce).
type memNonceStore struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error // se != nil, ConsumeNonce devolve-o (modela falha de backend)
}

func newMemNonceStore() *memNonceStore { return &memNonceStore{seen: map[string]bool{}} }

func (s *memNonceStore) ConsumeNonce(_ context.Context, scope string, nonce []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	key := scope + "\x00" + string(nonce)
	if s.seen[key] {
		return false, nil
	}
	s.seen[key] = true
	return true, nil
}

// TestRatify_Idempotente_SemControlos: por omissão (sem frescura nem nonce-store), a
// MESMA ratificação assinada promove repetidamente — documenta o comportamento
// idempotente que a defesa-em-profundidade opcional endurece.
func TestRatify_Idempotente_SemControlos(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)

	for i := 0; i < 3; i++ {
		admit, err := h.gate.Ratify(context.Background(), art, signed)
		if err != nil {
			t.Fatalf("Ratify #%d: %v", i, err)
		}
		if !admit {
			t.Fatalf("sem controlos, a ratificação devia continuar a promover (#%d)", i)
		}
	}
}

// TestRatify_Frescura_ForaDaJanela_Bloqueia: com WithRatifyFreshness, uma ratificação
// cujo IssuedAt está fora da janela é rejeitada como stale (defesa-em-profundidade).
func TestRatify_Frescura_ForaDaJanela_Bloqueia(t *testing.T) {
	vault := newFakeVault()
	registry := NewMemApproverRegistry()
	pub := vault.provision("ratifier", 0x11)
	registry.Register("ratifier", pub, DefaultRatifyAuthority)
	store := audit.NewMemStore()

	// Relógio "agora" = IssuedAt + 2h; janela de frescura de 1h ⇒ stale.
	issued := fixedClock()()
	now := func() time.Time { return issued.Add(2 * time.Hour) }
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		registry, store,
		WithRatifyClock(now),
		WithRatifyFreshness(time.Hour, time.Minute),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, vault, "ratifier", true, art) // IssuedAt = issued

	admit, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("ratificação fora da janela de frescura não devia promover")
	}
	// Atribuível ao ratificador (assinatura válida) na cadeia do artefacto.
	dec, params, principal, ok := decisionObligation(t, store, "ratification:"+art.ID)
	if !ok || dec != audit.DecisionDeny {
		t.Fatalf("stale devia ser deny selado: ok=%v dec=%v", ok, dec)
	}
	if params["reason"] != ReasonRatificationStale {
		t.Fatalf("motivo esperado stale, obtido %q", params["reason"])
	}
	if principal.NHIID != "ratifier" {
		t.Fatalf("stale deve ser atribuível ao ratificador, obtido %q", principal.NHIID)
	}
}

// TestRatify_Frescura_DentroDaJanela_Promove: dentro da janela (incl. tolerância de
// relógio adiantado), a ratificação fresca promove normalmente.
func TestRatify_Frescura_DentroDaJanela_Promove(t *testing.T) {
	h := newRatHarness(t)
	// h usa fixedClock; a ratificação é assinada com o MESMO instante ⇒ age=0.
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		h.registry, h.store,
		WithRatifyClock(fixedClock()),
		WithRatifyFreshness(time.Hour, time.Minute),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	admit, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if !admit {
		t.Fatal("ratificação fresca dentro da janela devia promover")
	}
}

// TestRatify_NonceStore_UsoUnico_BloqueiaReplay: com WithRatifyNonceStore, a PRIMEIRA
// ratificação promove e a REUTILIZAÇÃO da mesma assinatura é bloqueada como replayed —
// fecha o gap de re-promoção (incl. pós-rollback).
func TestRatify_NonceStore_UsoUnico_BloqueiaReplay(t *testing.T) {
	h := newRatHarness(t)
	nonces := newMemNonceStore()
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		h.registry, h.store,
		WithRatifyClock(fixedClock()),
		WithRatifyNonceStore(nonces),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)

	// 1ª vez: promove (nonce fresco).
	admit, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify #1: %v", err)
	}
	if !admit {
		t.Fatal("primeira ratificação devia promover")
	}

	// 2ª vez (replay): a mesma assinatura já consumiu o nonce ⇒ block.
	admit2, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify #2: %v", err)
	}
	if admit2 {
		t.Fatal("ANTI-REPLAY violado: a reutilização da ratificação re-promoveu")
	}
	dec, params, principal, ok := decisionObligation(t, h.store, "ratification:"+art.ID)
	if !ok || dec != audit.DecisionDeny {
		t.Fatalf("replay devia ser deny selado: ok=%v dec=%v", ok, dec)
	}
	if params["reason"] != ReasonRatificationReplayed {
		t.Fatalf("motivo esperado replayed, obtido %q", params["reason"])
	}
	if principal.NHIID != "ratifier" {
		t.Fatalf("replay deve ser atribuível ao ratificador, obtido %q", principal.NHIID)
	}
}

// TestRatify_NonceStore_ErroBackend_Bloqueia: um erro do nonce-store é tratado como
// bloqueio (fail-closed) — nunca promove.
func TestRatify_NonceStore_ErroBackend_Bloqueia(t *testing.T) {
	h := newRatHarness(t)
	nonces := newMemNonceStore()
	nonces.err = errors.New("backend indisponível")
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		h.registry, h.store,
		WithRatifyClock(fixedClock()),
		WithRatifyNonceStore(nonces),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	admit, err := gate.Ratify(context.Background(), art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("erro do nonce-store devia bloquear (fail-closed)")
	}
	_, params, _, _ := decisionObligation(t, h.store, "ratification:"+art.ID)
	if params["reason"] != ReasonRatificationReplayed {
		t.Fatalf("motivo esperado replayed, obtido %q", params["reason"])
	}
}

// TestRatify_SpanAtributos: o gate emite os atributos de decisão e a ligação ao eval no
// span (AC5), sem segredos.
func TestRatify_SpanAtributos(t *testing.T) {
	h := newRatHarness(t)
	tracer := &captureTracer{}
	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		h.registry, h.store,
		WithRatifyClock(fixedClock()),
		WithRatifyTracer(tracer),
		WithRatifyAuthority(DefaultRatifyAuthority),
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)
	if _, err := gate.Ratify(context.Background(), art, signed); err != nil {
		t.Fatalf("Ratify: %v", err)
	}

	sp := tracer.last()
	if sp == nil {
		t.Fatal("span não emitido")
	}
	if v, _ := sp.attr(AttrRatDecision); v != "allow" {
		t.Fatalf("decisão no span inesperada: %v", v)
	}
	if v, _ := sp.attr(AttrRatEvalVerdict); v != string(otelgenai.EvalPass) {
		t.Fatalf("veredicto do eval no span inesperado: %v", v)
	}
	if v, _ := sp.attr(AttrRatApprover); v != "ratifier" {
		t.Fatalf("ratificador no span inesperado: %v", v)
	}
	if v, _ := sp.attr(AttrRatVersion); v != art.Version {
		t.Fatalf("SemVer no span inesperado: %v", v)
	}
}
