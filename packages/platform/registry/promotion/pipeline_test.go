package promotion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// ---------------------------------------------------------------------------
// Construção fail-closed.
// ---------------------------------------------------------------------------

func TestNewPipeline_FailClosedConstruction(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ledger := NewApprovalLedger()
	if _, err := NewPipeline(nil, nil, ledger, h.auditStore); !errors.Is(err, ErrNilRegistry) {
		t.Fatalf("reg nil: err = %v, quer ErrNilRegistry", err)
	}
	if _, err := NewPipeline(h.reg, nil, ledger, h.auditStore); !errors.Is(err, ErrNilIntegrity) {
		t.Fatalf("integrity nil: err = %v, quer ErrNilIntegrity", err)
	}
	if _, err := NewPipeline(h.reg, allowIntegrity{}, ledger, nil); !errors.Is(err, ErrNoAudit) {
		t.Fatalf("audit nil: err = %v, quer ErrNoAudit", err)
	}
}

// allowIntegrity é um verificador de integridade que admite tudo (só para o teste
// de construção acima; os testes de comportamento usam o signing.Verifier real).
type allowIntegrity struct{}

func (allowIntegrity) Verify(context.Context, domain.Entry) error { return nil }

// ---------------------------------------------------------------------------
// DOMÍNIO: nenhum artefacto aparece active sem verificação.
// ---------------------------------------------------------------------------

func TestPublish_AlwaysStagingNeverActive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	e := h.mustPublish(h.toolReq("tool.http", ver(1, 0, 0), contract(domain.EgressExternal)))
	if e.Status != domain.StatusStaging {
		t.Fatalf("publish status = %s, quer staging", e.Status)
	}
	if h.isAdmissible("tool.http", ver(1, 0, 0)) {
		t.Fatal("artefacto staging NÃO deve ser admissível (default-deny)")
	}
	actives, err := h.reg.ActiveEntries(context.Background())
	if err != nil {
		t.Fatalf("ActiveEntries: %v", err)
	}
	if len(actives) != 0 {
		t.Fatalf("nenhuma versão devia estar active; obteve %d", len(actives))
	}
	if !h.hasStage(stagePublished) {
		t.Fatal("transição published não selada no audit")
	}
}

func TestPromote_IntegrityIsPrecondition_BadSignatureRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Assinatura forjada por uma chave DIFERENTE da confiável: a verificação de
	// origem (AOS-048) recusa; a promoção é rejeitada e o artefacto nunca vai active.
	c := contract(domain.EgressExternal)
	d := digest.SHA256Digester{}.Digest(domain.KindTool, c)
	req := registry.PublishRequest{
		ID: "tool.evil", Version: ver(1, 0, 0), Kind: domain.KindTool,
		Contract: c, Origin: "git+https://x", Publisher: pubKeyID,
		Signature: mustSignWrong(d), // assinada por keyFromSeed(9)
	}
	h.mustPublish(req)

	_, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.evil", Version: ver(1, 0, 0)})
	if !errors.Is(err, ErrIntegrityRejected) {
		t.Fatalf("Promote err = %v, quer ErrIntegrityRejected", err)
	}
	if h.isAdmissible("tool.evil", ver(1, 0, 0)) {
		t.Fatal("artefacto com assinatura inválida NÃO deve ser admissível")
	}
	if !h.hasStage(stageIntegrityRejected) {
		t.Fatal("rejeição de integridade não selada no audit")
	}
}

func TestPromote_SelfAuthoredSkillFailingEvalGate_Rejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.evalFn = func(string, domain.Version) (float64, int) { return 0.10, 7 } // falha
	c := contract(domain.EgressInternal)
	h.mustPublish(h.skillReq("skill.summarize", ver(1, 0, 0), c))

	rat := h.ratify("skill.summarize", ver(1, 0, 0), digest.SHA256Digester{}.Digest(domain.KindSkill, c))
	_, err := h.pipe.Promote(context.Background(), PromoteRequest{
		ID: "skill.summarize", Version: ver(1, 0, 0), Ratification: rat,
	})
	if !errors.Is(err, ErrEvalGateRejected) {
		t.Fatalf("Promote err = %v, quer ErrEvalGateRejected", err)
	}
	if h.isAdmissible("skill.summarize", ver(1, 0, 0)) {
		t.Fatal("skill que falha o eval-gate NÃO deve ir a produção (active)")
	}
	if !h.hasStage(stageEvalRejected) {
		t.Fatal("rejeição do eval-gate não selada no audit")
	}
}

// ---------------------------------------------------------------------------
// GOVERNAÇÃO: ratificação humana assinada + revogação de emergência.
// ---------------------------------------------------------------------------

func TestPromote_SelfAuthoredSkillRequiresSignedRatification(t *testing.T) {
	t.Parallel()
	c := contract(domain.EgressInternal)
	dig := digest.SHA256Digester{}.Digest(domain.KindSkill, c)

	cases := []struct {
		name    string
		rat     func(h *harness) *Ratification
		wantErr error
	}{
		{
			name:    "sem ratificacao",
			rat:     func(*harness) *Ratification { return nil },
			wantErr: ErrRatificationRequired,
		},
		{
			name: "ratificador nao-autorizado",
			rat: func(h *harness) *Ratification {
				// assinada por uma chave humana FORA da allowlist.
				r := SignRatification(keyFromSeed(77), "human:mallory", "skill.plan", ver(1, 0, 0), dig)
				return &r
			},
			wantErr: ErrRatificationInvalid,
		},
		{
			name: "assinatura sobre digest errado",
			rat: func(h *harness) *Ratification {
				r := SignRatification(h.ratPriv, ratifierID, "skill.plan", ver(1, 0, 0), "sha256:deadbeef")
				return &r
			},
			wantErr: ErrRatificationInvalid,
		},
		{
			name: "ratificacao valida",
			rat: func(h *harness) *Ratification {
				return h.ratify("skill.plan", ver(1, 0, 0), dig)
			},
			wantErr: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.mustPublish(h.skillReq("skill.plan", ver(1, 0, 0), c))
			_, err := h.pipe.Promote(context.Background(), PromoteRequest{
				ID: "skill.plan", Version: ver(1, 0, 0), Ratification: tc.rat(h),
			})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Promote err = %v, quer nil", err)
				}
				if !h.isAdmissible("skill.plan", ver(1, 0, 0)) {
					t.Fatal("skill ratificada devia ficar active/admissível")
				}
				if !h.hasStage(stageRatified) {
					t.Fatal("ratificação não selada no audit")
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Promote err = %v, quer %v", err, tc.wantErr)
			}
			if h.isAdmissible("skill.plan", ver(1, 0, 0)) {
				t.Fatal("skill sem ratificação válida NÃO deve ficar active")
			}
		})
	}
}

func TestRevoke_EmergencyBlocksImmediately(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mustPublish(h.toolReq("tool.db", ver(1, 0, 0), contract(domain.EgressInternal)))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.db", Version: ver(1, 0, 0)}); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !h.isAdmissible("tool.db", ver(1, 0, 0)) {
		t.Fatal("tool promovida devia ser admissível")
	}
	// Revogação de emergência.
	if _, err := h.pipe.Revoke(context.Background(), "tool.db", ver(1, 0, 0)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if h.isAdmissible("tool.db", ver(1, 0, 0)) {
		t.Fatal("revogação de emergência devia bloquear IMEDIATAMENTE no RM (IsAdmissible=false)")
	}
	if !h.hasStage(stageRevoked) {
		t.Fatal("revogação não selada no audit")
	}
	// revoked é terminal: nova promoção recusada pela máquina de estados.
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.db", Version: ver(1, 0, 0)}); err == nil {
		t.Fatal("promoção de artefacto revogado devia falhar")
	}
}

// ---------------------------------------------------------------------------
// INTEGRAÇÃO: audit WORM, SemVer, ValidateBump.
// ---------------------------------------------------------------------------

func TestPromote_TransitionsSealedInAuditWORM(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := contract(domain.EgressInternal)
	dig := digest.SHA256Digester{}.Digest(domain.KindSkill, c)
	h.mustPublish(h.skillReq("skill.route", ver(1, 0, 0), c))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{
		ID: "skill.route", Version: ver(1, 0, 0), Ratification: h.ratify("skill.route", ver(1, 0, 0), dig),
	}); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	want := []string{
		capPromotionPrefix + stagePublished,
		capPromotionPrefix + stageIntegrityVerified,
		capPromotionPrefix + stageEvalPassed,
		capPromotionPrefix + stageRatified,
		capPromotionPrefix + stagePromoteIntent,
		capPromotionPrefix + stagePromoted,
	}
	got := h.auditStages()
	if len(got) != len(want) {
		t.Fatalf("stages = %v, quer %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stage[%d] = %s, quer %s (todos: %v)", i, got[i], want[i], got)
		}
	}
	// A hash-chain do WORM tem de verificar (tamper-evident, AOS-011).
	head, _ := h.auditStore.Head(context.Background(), DefaultPromotionPartition)
	if err := audit.Verify(context.Background(), h.auditStore, DefaultPromotionPartition, 1, head); err != nil {
		t.Fatalf("audit.Verify: %v", err)
	}

	// Critério 7 (AOS-048): cada registo selado tem de trazer o tuplo (id, version,
	// digest) ao nível dos CAMPOS — ToolID==id, PolicyVersion==version, Resource.
	// Value==digest — não só a sequência de stages. Os selos de INTENÇÃO de estado
	// (stage*Intent) que ainda não conhecem o digest são a única excepção admitida.
	recs, err := h.auditStore.Read(context.Background(), DefaultPromotionPartition, 1, head)
	if err != nil {
		t.Fatalf("audit Read: %v", err)
	}
	for _, r := range recs {
		if r.ToolID != "skill.route" {
			t.Fatalf("ToolID selado = %q, quer skill.route (cap %s)", r.ToolID, r.Capability)
		}
		if r.PolicyVersion != ver(1, 0, 0).String() {
			t.Fatalf("PolicyVersion selada = %q, quer 1.0.0 (cap %s)", r.PolicyVersion, r.Capability)
		}
		if r.Resource.Type != "artifact.digest" {
			t.Fatalf("Resource.Type selado = %q, quer artifact.digest (cap %s)", r.Resource.Type, r.Capability)
		}
		if r.Resource.Value != dig {
			t.Fatalf("digest selado = %q, quer %q (cap %s)", r.Resource.Value, dig, r.Capability)
		}
	}
}

func TestPromote_AssignsSemVer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mustPublish(h.toolReq("tool.calc", ver(1, 2, 3), contract(domain.EgressNone)))
	res, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.calc", Version: ver(1, 2, 3)})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !res.Version.Equal(ver(1, 2, 3)) {
		t.Fatalf("SemVer atribuída = %s, quer 1.2.3", res.Version)
	}
	if res.SelfAuthored {
		t.Fatal("tool de terceiros não é auto-escrita")
	}
}

func TestPromote_ValidateBump_IncompatibleRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// v1 active (baseline).
	h.mustPublish(h.toolReq("tool.api", ver(1, 0, 0), contract(domain.EgressInternal)))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.api", Version: ver(1, 0, 0)}); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}

	// Quebra de contrato (scope acrescentado ⇒ MAJOR exigido) declarada como MINOR.
	broken := contract(domain.EgressInternal, "vault:new")
	h.mustPublish(h.toolReq("tool.api", ver(1, 1, 0), broken))
	_, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.api", Version: ver(1, 1, 0)})
	if !errors.Is(err, ErrIntegrityRejected) {
		t.Fatalf("bump incompatível: err = %v, quer ErrIntegrityRejected", err)
	}
	if h.isAdmissible("tool.api", ver(1, 1, 0)) {
		t.Fatal("bump incompatível NÃO deve chegar a active")
	}

	// A MESMA quebra declarada como MAJOR (2.0.0) é aceite.
	h.mustPublish(h.toolReq("tool.api", ver(2, 0, 0), broken))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.api", Version: ver(2, 0, 0)}); err != nil {
		t.Fatalf("Promote v2 (MAJOR): %v", err)
	}
	if !h.isAdmissible("tool.api", ver(2, 0, 0)) {
		t.Fatal("bump MAJOR correcto devia chegar a active")
	}
}

// ---------------------------------------------------------------------------
// DISTINÇÃO: tool de terceiros vs skill auto-escrita.
// ---------------------------------------------------------------------------

func TestDistinction_ThirdPartyToolSkipsEvalAndRatification(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Mesmo com o eval-gate a FALHAR e sem ratificação, uma TOOL de terceiros
	// promove só com verificação de integridade (não é skill auto-escrita).
	h.evalFn = func(string, domain.Version) (float64, int) { return 0.0, 99 }
	h.mustPublish(h.toolReq("tool.fetch", ver(1, 0, 0), contract(domain.EgressExternal)))
	res, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.fetch", Version: ver(1, 0, 0)})
	if err != nil {
		t.Fatalf("Promote tool: %v", err)
	}
	if res.SelfAuthored {
		t.Fatal("tool não deve ser classificada auto-escrita")
	}
	if !h.isAdmissible("tool.fetch", ver(1, 0, 0)) {
		t.Fatal("tool verificada devia ficar active")
	}
	if h.hasStage(stageEvalPassed) || h.hasStage(stageEvalRejected) {
		t.Fatal("tool de terceiros NÃO deve atravessar o eval-gate")
	}
}

func TestDistinction_SelfAuthoredSkillNeedsFullGovernance(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := contract(domain.EgressInternal)
	dig := digest.SHA256Digester{}.Digest(domain.KindSkill, c)
	h.mustPublish(h.skillReq("skill.compose", ver(1, 0, 0), c))
	res, err := h.pipe.Promote(context.Background(), PromoteRequest{
		ID: "skill.compose", Version: ver(1, 0, 0), Ratification: h.ratify("skill.compose", ver(1, 0, 0), dig),
	})
	if err != nil {
		t.Fatalf("Promote skill: %v", err)
	}
	if !res.SelfAuthored {
		t.Fatal("skill de origem 'self' devia ser classificada auto-escrita")
	}
	if !res.Eval.Passed {
		t.Fatal("eval-gate devia ter passado")
	}
	if !h.hasStage(stageEvalPassed) || !h.hasStage(stageRatified) {
		t.Fatal("skill auto-escrita tem de atravessar eval-gate E ratificação")
	}
}

// NoJumpToActive: fecho ESTRUTURAL — nem uma chamada directa a SetStatus promove uma
// skill auto-escrita sem a aprovação de governação (eval-gate + ratificação).
func TestNoJumpToActive_DirectSetStatusBlockedForUnapprovedSkill(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mustPublish(h.skillReq("skill.bypass", ver(1, 0, 0), contract(domain.EgressInternal)))
	// Tentativa de ignorar o Pipeline: SetStatus directo para active.
	_, err := h.reg.SetStatus(context.Background(), "skill.bypass", ver(1, 0, 0), domain.StatusActive)
	if !errors.Is(err, registry.ErrAdmissionDenied) {
		t.Fatalf("SetStatus directo: err = %v, quer ErrAdmissionDenied", err)
	}
	// O Registry embrulha a causa com %v (texto), pelo que a identidade não propaga
	// por errors.Is; confirmamos que a causa é a falta de aprovação pelo código.
	if !strings.Contains(err.Error(), ErrNotApproved.Code) {
		t.Fatalf("SetStatus directo: err = %v, quer causa %s", err, ErrNotApproved.Code)
	}
	if h.isAdmissible("skill.bypass", ver(1, 0, 0)) {
		t.Fatal("skill não-aprovada NÃO pode chegar a active por bypass")
	}
}

// ---------------------------------------------------------------------------
// ROLLBACK atómico (via Lifecycle de AOS-052).
// ---------------------------------------------------------------------------

func TestRollback_AtomicRestoresPreviousActive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// v1 e v2 (mesmo contrato; 2.0.0 > 1.0.0) promovidas.
	c := contract(domain.EgressInternal)
	h.mustPublish(h.toolReq("tool.svc", ver(1, 0, 0), c))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.svc", Version: ver(1, 0, 0)}); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}
	h.mustPublish(h.toolReq("tool.svc", ver(2, 0, 0), c))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "tool.svc", Version: ver(2, 0, 0)}); err != nil {
		t.Fatalf("Promote v2: %v", err)
	}
	// v1 deprecated (deprecação formal) para ser alvo de rollback.
	if _, err := h.pipe.Deprecate(context.Background(), "tool.svc", ver(1, 0, 0)); err != nil {
		t.Fatalf("Deprecate v1: %v", err)
	}
	// Rollback para v1.
	if _, err := h.pipe.Rollback(context.Background(), "tool.svc", ver(1, 0, 0)); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !h.isAdmissible("tool.svc", ver(1, 0, 0)) {
		t.Fatal("após rollback, v1 devia estar active/admissível")
	}
	if h.isAdmissible("tool.svc", ver(2, 0, 0)) {
		t.Fatal("após rollback, v2 devia estar deprecated (não-admissível)")
	}
	if !h.hasStage(stageRolledBack) {
		t.Fatal("rollback não selado no audit")
	}
}

// ---------------------------------------------------------------------------
// AOS-053 Q1: auto-autoria estrutural (por kind), não pela Origin forjável.
// ---------------------------------------------------------------------------

// TestPromote_SkillWithForgedExternalOrigin_StillRequiresGovernance prova que uma
// skill que DECLARA uma origem não-self (ex.: "git+https://…"), assinada por uma
// chave de publicador CONFIÁVEL, continua a ser tratada como auto-escrita: exige
// eval-gate + ratificação e nunca chega a active por baixo desses gates. É o cenário
// exacto de misevolution (o agente que escreve a própria skill é um publicador
// confiável) que a classificação por Origin deixava passar.
func TestPromote_SkillWithForgedExternalOrigin_StillRequiresGovernance(t *testing.T) {
	t.Parallel()
	c := contract(domain.EgressInternal)
	dig := digest.SHA256Digester{}.Digest(domain.KindSkill, c)
	const forgedOrigin = "git+https://attacker/repo"

	// (a) eval-gate a FALHAR (0.0, 999) e SEM ratificação: rejeitada, nunca active.
	t.Run("eval falha sem ratificacao => rejeitada", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.evalFn = func(string, domain.Version) (float64, int) { return 0.0, 999 }
		h.mustPublish(h.signedReq("skill.trojan", ver(1, 0, 0), domain.KindSkill, forgedOrigin, c))
		_, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "skill.trojan", Version: ver(1, 0, 0)})
		if !errors.Is(err, ErrEvalGateRejected) {
			t.Fatalf("Promote err = %v, quer ErrEvalGateRejected", err)
		}
		if h.isAdmissible("skill.trojan", ver(1, 0, 0)) {
			t.Fatal("skill de origem externa forjada NÃO pode chegar a active sem eval-gate")
		}
		if h.hasStage(stagePromoted) {
			t.Fatal("skill rejeitada no eval-gate NÃO deve ter selo de promoção")
		}
	})

	// (b) eval-gate a passar mas SEM ratificação: continua recusada.
	t.Run("eval passa sem ratificacao => recusada", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mustPublish(h.signedReq("skill.trojan2", ver(1, 0, 0), domain.KindSkill, forgedOrigin, c))
		_, err := h.pipe.Promote(context.Background(), PromoteRequest{ID: "skill.trojan2", Version: ver(1, 0, 0)})
		if !errors.Is(err, ErrRatificationRequired) {
			t.Fatalf("Promote err = %v, quer ErrRatificationRequired", err)
		}
		if h.isAdmissible("skill.trojan2", ver(1, 0, 0)) {
			t.Fatal("skill de origem externa forjada NÃO pode chegar a active sem ratificação")
		}
	})

	// (c) governação completa: é promovida, mas SEMPRE como auto-escrita.
	t.Run("governacao completa => auto-escrita promovida", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mustPublish(h.signedReq("skill.trojan3", ver(1, 0, 0), domain.KindSkill, forgedOrigin, c))
		res, err := h.pipe.Promote(context.Background(), PromoteRequest{
			ID: "skill.trojan3", Version: ver(1, 0, 0), Ratification: h.ratify("skill.trojan3", ver(1, 0, 0), dig),
		})
		if err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if !res.SelfAuthored {
			t.Fatal("uma skill (kind=skill) é auto-escrita mesmo com Origin externa declarada")
		}
		if !h.hasStage(stageEvalPassed) || !h.hasStage(stageRatified) {
			t.Fatal("a skill tem de atravessar eval-gate E ratificação")
		}
		if !h.isAdmissible("skill.trojan3", ver(1, 0, 0)) {
			t.Fatal("skill com governação completa devia ficar active")
		}
	})
}

// ---------------------------------------------------------------------------
// AOS-053 Q2: fecho de governação DURÁVEL (reconstruído do WORM no arranque).
// ---------------------------------------------------------------------------

func TestRebuildApprovalLedger_FromWORM(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := contract(domain.EgressInternal)
	dig := digest.SHA256Digester{}.Digest(domain.KindSkill, c)
	h.mustPublish(h.skillReq("skill.persist", ver(1, 0, 0), c))
	if _, err := h.pipe.Promote(context.Background(), PromoteRequest{
		ID: "skill.persist", Version: ver(1, 0, 0), Ratification: h.ratify("skill.persist", ver(1, 0, 0), dig),
	}); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Simula um arranque a frio: um ledger NOVO reconstruído SÓ do audit WORM (o mapa
	// em memória do processo anterior desapareceu).
	rebuilt, err := RebuildApprovalLedger(context.Background(), h.auditStore, DefaultPromotionPartition)
	if err != nil {
		t.Fatalf("RebuildApprovalLedger: %v", err)
	}
	if !rebuilt.IsApproved("skill.persist", ver(1, 0, 0), dig) {
		t.Fatal("aprovação de governação devia ser reconstruída do WORM (fecho durável)")
	}
	// Fail-closed: uma (id, version, digest) nunca ratificada continua ausente.
	if rebuilt.IsApproved("skill.persist", ver(2, 0, 0), dig) {
		t.Fatal("uma versão nunca ratificada NÃO deve aparecer aprovada")
	}
	if rebuilt.IsApproved("skill.other", ver(1, 0, 0), dig) {
		t.Fatal("um id nunca ratificado NÃO deve aparecer aprovado")
	}
	// Um digest divergente (conteúdo adulterado) não reutiliza a aprovação.
	if rebuilt.IsApproved("skill.persist", ver(1, 0, 0), "sha256:deadbeef") {
		t.Fatal("aprovação não deve valer para um digest diferente")
	}

	// store nil é fail-closed.
	if _, err := RebuildApprovalLedger(context.Background(), nil, DefaultPromotionPartition); !errors.Is(err, ErrNoAudit) {
		t.Fatalf("store nil: err = %v, quer ErrNoAudit", err)
	}
	// partição vazia usa a default (mesma aprovação reconstruída).
	def, err := RebuildApprovalLedger(context.Background(), h.auditStore, "")
	if err != nil {
		t.Fatalf("RebuildApprovalLedger partição default: %v", err)
	}
	if !def.IsApproved("skill.persist", ver(1, 0, 0), dig) {
		t.Fatal("partição vazia devia resolver para a default e reconstruir a aprovação")
	}
}

// mustSignWrong assina o tuplo com uma chave NÃO confiável (keyFromSeed(9)) para
// forjar uma assinatura inválida.
func mustSignWrong(dig string) string {
	// Reutiliza o SigningInput real do pacote signing via um signer de chave errada.
	// Simplificação: assinamos com ed25519 directamente sobre o mesmo input.
	wrong := keyFromSeed(9)
	// O input canónico é (id, version, digest); usamos o helper do harness indirecto.
	return signWrongTuple(wrong, "tool.evil", ver(1, 0, 0), dig)
}
