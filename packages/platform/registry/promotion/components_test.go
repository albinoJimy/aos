package promotion

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// ---------------------------------------------------------------------------
// Classificação estrutural.
// ---------------------------------------------------------------------------

func TestDefaultSelfAuthoredClassifier(t *testing.T) {
	t.Parallel()
	entry := func(k domain.ArtifactKind, origin string) domain.Entry {
		return domain.Entry{Kind: k, Provenance: domain.Provenance{Origin: origin}}
	}
	// AOS-053 Q1: a classificação é ESTRUTURAL só por kind. TODA a skill é
	// auto-escrita, INDEPENDENTEMENTE da origem declarada (o Origin é forjável e não
	// é coberto pela assinatura); nenhum não-skill é auto-escrito, mesmo com origem
	// "self".
	cases := []struct {
		name string
		e    domain.Entry
		want bool
	}{
		{"skill self", entry(domain.KindSkill, "self"), true},
		{"skill self-prefixo", entry(domain.KindSkill, "self:agent-42"), true},
		{"skill agent-prefixo", entry(domain.KindSkill, "agent:planner"), true},
		{"skill origem externa forjada", entry(domain.KindSkill, "git+https://x"), true},
		{"skill sem origem", entry(domain.KindSkill, ""), true},
		{"tool self", entry(domain.KindTool, "self"), false},
		{"mcp self", entry(domain.KindMCPServer, "self"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultSelfAuthoredClassifier(tc.e); got != tc.want {
				t.Fatalf("classificacao = %v, quer %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RatifierStore.
// ---------------------------------------------------------------------------

func TestRatifierStore_AuthorizeInvalid(t *testing.T) {
	t.Parallel()
	s := NewRatifierStore()
	if err := s.Authorize("", keyFromSeed(3).Public().(ed25519.PublicKey)); !errors.Is(err, ErrRatificationInvalid) {
		t.Fatalf("id vazio: err = %v", err)
	}
	if err := s.Authorize("h", ed25519.PublicKey{1, 2, 3}); !errors.Is(err, ErrRatificationInvalid) {
		t.Fatalf("chave curta: err = %v", err)
	}
}

func TestRatifierStore_RevokeBlocks(t *testing.T) {
	t.Parallel()
	s := NewRatifierStore()
	priv := keyFromSeed(4)
	if err := s.Authorize("h", priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	rat := SignRatification(priv, "h", "id", ver(1, 0, 0), "dig")
	if err := s.Verify(rat, "id", ver(1, 0, 0), "dig"); err != nil {
		t.Fatalf("Verify autorizado: %v", err)
	}
	s.Revoke("h")
	if err := s.Verify(rat, "id", ver(1, 0, 0), "dig"); !errors.Is(err, ErrRatificationInvalid) {
		t.Fatalf("após revogação: err = %v, quer ErrRatificationInvalid", err)
	}
}

func TestRatifierStore_VerifyEdgeCases(t *testing.T) {
	t.Parallel()
	s := NewRatifierStore()
	priv := keyFromSeed(5)
	_ = s.Authorize("h", priv.Public().(ed25519.PublicKey))
	valid := SignRatification(priv, "h", "id", ver(1, 0, 0), "dig")

	// tuplo divergente (versão).
	if err := s.Verify(valid, "id", ver(2, 0, 0), "dig"); !errors.Is(err, ErrRatificationInvalid) {
		t.Fatalf("versão divergente: %v", err)
	}
	// assinatura de tamanho errado.
	bad := valid
	bad.Signature = []byte{1, 2, 3}
	if err := s.Verify(bad, "id", ver(1, 0, 0), "dig"); !errors.Is(err, ErrRatificationInvalid) {
		t.Fatalf("sig curta: %v", err)
	}
	// ratifierID vazio.
	empty := valid
	empty.RatifierID = ""
	if err := s.Verify(empty, "id", ver(1, 0, 0), "dig"); !errors.Is(err, ErrRatificationInvalid) {
		t.Fatalf("ratifier vazio: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EvalGate de referência.
// ---------------------------------------------------------------------------

func TestThresholdEvalGate(t *testing.T) {
	t.Parallel()
	// sem metrics: fail-closed.
	res, _ := ThresholdEvalGate{}.Evaluate(context.Background(), EvalRequest{})
	if res.Passed {
		t.Fatal("sem metrics devia falhar (fail-closed)")
	}
	g := ThresholdEvalGate{
		MinGoldenSetScore:       0.9,
		MaxTraceDiffRegressions: 1,
		Metrics:                 func(string, domain.Version) (float64, int) { return 0.95, 1 },
	}
	if r, _ := g.Evaluate(context.Background(), EvalRequest{ID: "s", Version: ver(1, 0, 0)}); !r.Passed {
		t.Fatalf("devia passar: %+v", r)
	}
	gFail := ThresholdEvalGate{
		MinGoldenSetScore:       0.9,
		MaxTraceDiffRegressions: 1,
		Metrics:                 func(string, domain.Version) (float64, int) { return 0.5, 5 },
	}
	if r, _ := gFail.Evaluate(context.Background(), EvalRequest{}); r.Passed {
		t.Fatalf("devia falhar: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// CompositeVerifier.
// ---------------------------------------------------------------------------

func TestCompositeVerifier(t *testing.T) {
	t.Parallel()
	ledger := NewApprovalLedger()
	skill := domain.Entry{ID: "s", Version: ver(1, 0, 0), Kind: domain.KindSkill, Digest: "dig", Provenance: domain.Provenance{Origin: "self"}}
	tool := domain.Entry{ID: "t", Version: ver(1, 0, 0), Kind: domain.KindTool, Digest: "dig", Provenance: domain.Provenance{Origin: "git"}}

	// integrity nil → ErrNilIntegrity.
	if err := (&CompositeVerifier{ledger: ledger}).Verify(context.Background(), tool); !errors.Is(err, ErrNilIntegrity) {
		t.Fatalf("integrity nil: %v", err)
	}
	// integridade que erra propaga o erro.
	sentinel := errors.New("boom")
	cvErr := NewCompositeVerifier(errIntegrity{sentinel}, ledger, nil)
	if err := cvErr.Verify(context.Background(), tool); !errors.Is(err, sentinel) {
		t.Fatalf("integridade erra: %v", err)
	}
	// skill auto-escrita sem aprovação → ErrNotApproved; tool passa.
	cv := NewCompositeVerifier(allowIntegrity{}, ledger, nil)
	if err := cv.Verify(context.Background(), skill); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("skill não-aprovada: %v", err)
	}
	if err := cv.Verify(context.Background(), tool); err != nil {
		t.Fatalf("tool: %v", err)
	}
	// após aprovação a skill passa.
	ledger.Approve("s", ver(1, 0, 0), "dig")
	if err := cv.Verify(context.Background(), skill); err != nil {
		t.Fatalf("skill aprovada: %v", err)
	}
}

type errIntegrity struct{ err error }

func (e errIntegrity) Verify(context.Context, domain.Entry) error { return e.err }

// ---------------------------------------------------------------------------
// validateContract.
// ---------------------------------------------------------------------------

func TestValidateContract(t *testing.T) {
	t.Parallel()
	if err := validateContract(contract(domain.EgressNone)); err != nil {
		t.Fatalf("contrato válido: %v", err)
	}
	if err := validateContract(domain.Contract{Egress: "bogus"}); !errors.Is(err, domain.ErrInvalidEgress) {
		t.Fatalf("egress inválido: %v", err)
	}
	bad := domain.Contract{Egress: domain.EgressNone, InputSchema: json.RawMessage(`{bad`)}
	if err := validateContract(bad); err == nil {
		t.Fatal("schema malformado devia falhar")
	}
}

// ---------------------------------------------------------------------------
// Wiring alternativo: sem eval-gate / sem ratificadores (fail-closed).
// ---------------------------------------------------------------------------

func TestPromote_SelfAuthoredSkill_NoEvalGate_FailClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pipe := h.pipelineWith(WithRatifiers(h.ratifiers)) // sem eval-gate
	h.mustPublish(h.skillReq("skill.x", ver(1, 0, 0), contract(domain.EgressNone)))
	_, err := pipe.Promote(context.Background(), PromoteRequest{
		ID: "skill.x", Version: ver(1, 0, 0),
		Ratification: h.ratify("skill.x", ver(1, 0, 0), digest.SHA256Digester{}.Digest(domain.KindSkill, contract(domain.EgressNone))),
	})
	if !errors.Is(err, ErrNoEvalGate) {
		t.Fatalf("sem eval-gate: err = %v, quer ErrNoEvalGate", err)
	}
}

func TestPromote_SelfAuthoredSkill_NoRatifiers_FailClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	evalGate := ThresholdEvalGate{MinGoldenSetScore: 0.9, Metrics: func(string, domain.Version) (float64, int) { return 1.0, 0 }}
	pipe := h.pipelineWith(WithEvalGate(evalGate)) // sem ratificadores
	h.mustPublish(h.skillReq("skill.y", ver(1, 0, 0), contract(domain.EgressNone)))
	_, err := pipe.Promote(context.Background(), PromoteRequest{ID: "skill.y", Version: ver(1, 0, 0)})
	if !errors.Is(err, ErrNoRatifiers) {
		t.Fatalf("sem ratificadores: err = %v, quer ErrNoRatifiers", err)
	}
}

// ---------------------------------------------------------------------------
// Custom options: classifier, partição, tracer.
// ---------------------------------------------------------------------------

func TestOptions_CustomClassifierPartitionTracer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tr := &agentruntime.RecordingTracer{}
	// Todas as opções aplicadas (classifier/digester/partição/tracer injectáveis).
	// Promovemos uma TOOL — não-auto-escrita para o classifier default do Registry E
	// para o classifier custom do Pipeline — pelo que a promoção só precisa da
	// verificação de integridade e não colide com o gate estrutural.
	pipe := h.pipelineWith(
		WithClassifier(func(domain.Entry) bool { return false }),
		WithDigester(digest.SHA256Digester{}),
		WithPartition("custom.promo"),
		WithTracer(tr),
	)
	h.mustPublish(h.toolReq("tool.z", ver(1, 0, 0), contract(domain.EgressNone)))
	if _, err := pipe.Promote(context.Background(), PromoteRequest{ID: "tool.z", Version: ver(1, 0, 0)}); err != nil {
		t.Fatalf("Promote (opções custom): %v", err)
	}
	// selado na partição CUSTOM, não na default.
	head, _ := h.auditStore.Head(context.Background(), "custom.promo")
	if head == 0 {
		t.Fatal("transições não seladas na partição custom")
	}
	if len(tr.SpansByOperation(opPromote)) == 0 {
		t.Fatal("span de promoção não registado pelo tracer custom")
	}
}

// ---------------------------------------------------------------------------
// Fail-closed sobre audit indisponível.
// ---------------------------------------------------------------------------

func TestSeal_AuditFailedFailsClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pipe := h.pipelineWith(withFailingAudit())
	_, err := pipe.Publish(context.Background(), h.toolReq("tool.a", ver(1, 0, 0), contract(domain.EgressNone)))
	if !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("Publish com audit em falha: err = %v, quer ErrAuditFailed", err)
	}
}

// withFailingAudit substitui o audit store por um que falha sempre (via um Pipeline
// construído à mão, já que a opção pública não expõe o store).
func withFailingAudit() PipelineOption {
	return func(p *Pipeline) { p.audit = failingAudit{} }
}

type failingAudit struct{}

func (failingAudit) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("audit down")
}
func (failingAudit) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (failingAudit) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingAudit) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}

// ---------------------------------------------------------------------------
// Rollback: ramos de erro.
// ---------------------------------------------------------------------------

func TestRollback_InvalidTarget(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// alvo inexistente no catálogo → Resolve falha.
	if _, err := h.pipe.Rollback(context.Background(), "tool.none", ver(9, 0, 0)); err == nil {
		t.Fatal("rollback de alvo inexistente devia falhar")
	}
	// alvo em staging (não deprecated) → ErrRollbackTarget (via Lifecycle).
	h.mustPublish(h.toolReq("tool.s", ver(1, 0, 0), contract(domain.EgressNone)))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.s", Version: ver(1, 0, 0)}); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	// tentar rollback para uma versão staging nunca promovida.
	h.mustPublish(h.toolReq("tool.s", ver(2, 0, 0), contract(domain.EgressNone)))
	if _, err := h.pipe.Rollback(context.Background(), "tool.s", ver(2, 0, 0)); !errors.Is(err, ErrRollbackTarget) {
		t.Fatalf("rollback para staging: err = %v, quer ErrRollbackTarget", err)
	}
}

// ---------------------------------------------------------------------------
// Deprecate: transição inválida.
// ---------------------------------------------------------------------------

func TestDeprecate_InvalidTransitionFromStaging(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mustPublish(h.toolReq("tool.d", ver(1, 0, 0), contract(domain.EgressNone)))
	// staging→deprecated não é permitido pela máquina de estados.
	if _, err := h.pipe.Deprecate(context.Background(), "tool.d", ver(1, 0, 0)); err == nil {
		t.Fatal("deprecate de staging devia falhar (transição inválida)")
	}
}

// ---------------------------------------------------------------------------
// CanonicalRatification: determinismo.
// ---------------------------------------------------------------------------

func TestCanonicalRatification_Deterministic(t *testing.T) {
	t.Parallel()
	a := CanonicalRatification("h", "id", ver(1, 2, 3), "dig")
	b := CanonicalRatification("h", "id", ver(1, 2, 3), "dig")
	if string(a) != string(b) {
		t.Fatal("mensagem canónica não determinista")
	}
	c := CanonicalRatification("h", "id2", ver(1, 2, 3), "dig")
	if string(a) == string(c) {
		t.Fatal("mensagens de alvos diferentes não deviam colidir")
	}
}
