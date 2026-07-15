package revalidation

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
	mcp "github.com/aos-ref/platform/registry/mcp"
)

// --- Construção fail-closed --------------------------------------------------

func TestNew_FailClosed(t *testing.T) {
	t.Parallel()
	au := audit.NewMemStore()
	trust := newTrust(t)
	if _, err := New(nil, au); err != ErrNoTrustStore {
		t.Fatalf("New(trust=nil) erro = %v, quer ErrNoTrustStore", err)
	}
	if _, err := New(trust, nil); err != ErrNoAuditStore {
		t.Fatalf("New(audit=nil) erro = %v, quer ErrNoAuditStore", err)
	}
	if _, err := New(trust, au); err != nil {
		t.Fatalf("New válido: %v", err)
	}
}

// --- Caminho feliz: os seis passos passam -----------------------------------

func TestRevalidate_Permit(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	c := contractWith("v1", domain.EgressInternal, "vault:db.read")
	entry := signedEntry("tool.http", v1, domain.KindTool, c, pub)
	frozen := freeze(t, "run-1", entry)

	h := newHarness(t, trust)
	dec, err := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http",
		Current: entry, Frozen: frozen,
		Policy: Policy{AllowedScopes: []string{"vault:db.read", "vault:db.write"}, MaxEgress: domain.EgressExternal},
	})
	if err != nil {
		t.Fatalf("Revalidate erro: %v", err)
	}
	if !dec.Allowed || dec.Reason != ReasonPermitted || dec.Stage != StageExec {
		t.Fatalf("decisão = %+v, quer allow/permitted/exec", dec)
	}
	// Permit GENUÍNO e não-forjável.
	p, ok := dec.Permit()
	if !ok || !p.Granted() {
		t.Fatalf("permit não concedido: ok=%v granted=%v", ok, p.Granted())
	}
	if p.ToolID() != "tool.http" || p.Digest() != entry.Digest || p.Version() != "1.0.0" {
		t.Fatalf("permit liga a identidade errada: %+v", p)
	}
	// Um Permit forjado directamente NÃO é concedido.
	if (Permit{toolID: "tool.http"}).Granted() {
		t.Fatal("Permit forjado directamente reporta Granted()=true")
	}
	// Sem quarentena nem alerta no caminho feliz.
	if h.quar.count() != 0 || h.alert.count() != 0 {
		t.Fatalf("caminho feliz gerou quarentena=%d alertas=%d", h.quar.count(), h.alert.count())
	}
}

// --- DOMÍNIO: rug-pull a meio do run ----------------------------------------

// TestRevalidate_RugPull_DigestDrift é o teste central de AOS-051: a definição em
// backing store DIVERGE do congelado (um servidor MCP mutou o schema a meio do run)
// → BLOQUEIO + QUARENTENA + ALERTA, com a decisão selada no audit.
func TestRevalidate_RugPull_DigestDrift(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)

	// Congelado no arranque: a definição íntegra "v1".
	frozenEntry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal), pub)
	frozen := freeze(t, "run-1", frozenEntry)

	// A MEIO do run o backing store devolve uma definição MUTADA (schema drift):
	// mesmo id/version, contrato diferente → digest recalculado diverge do congelado.
	// (Assinamos a versão mutada para provar que NEM uma assinatura válida sobre o
	// conteúdo novo salva o rug-pull — o digest já não casa com o congelado.)
	mutated := signedEntry("tool.http", v1, domain.KindTool, contractWith("MUTATED", domain.EgressInternal), pub)

	h := newHarness(t, trust)
	dec, err := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http",
		Current: mutated, Frozen: frozen,
		Policy: Policy{MaxEgress: domain.EgressExternal},
	})
	if err != nil {
		t.Fatalf("Revalidate erro: %v", err)
	}
	if dec.Allowed || dec.Reason != ReasonDigestMismatch || dec.Stage != StageDigest {
		t.Fatalf("decisão = %+v, quer block/digest_mismatch/digest", dec)
	}
	// Sem permit.
	if _, ok := dec.Permit(); ok {
		t.Fatal("bloqueio devolveu permit")
	}
	// QUARENTENA do artefacto divergente, com a identidade CONGELADA e a razão.
	if h.quar.count() != 1 {
		t.Fatalf("quarentena count=%d, quer 1", h.quar.count())
	}
	art, _ := h.quar.last()
	if art.ID != "tool.http" || art.Digest != frozenEntry.Digest || art.Reason != ReasonDigestMismatch {
		t.Fatalf("artefacto em quarentena = %+v", art)
	}
	// ALERTA emitido.
	if h.alert.count() != 1 {
		t.Fatalf("alertas=%d, quer 1", h.alert.count())
	}
	al, _ := h.alert.last()
	if al.Reason != ReasonDigestMismatch || al.Stage != StageDigest || al.ToolID != "tool.http" {
		t.Fatalf("alerta = %+v", al)
	}
	// Decisão SELADA no audit com id/version/digest/resultado=deny.
	recs := h.auditRecords(t, DefaultPartition)
	if len(recs) != 1 {
		t.Fatalf("audit records=%d, quer 1", len(recs))
	}
	assertAuditRecord(t, recs[0], "tool.http", "1.0.0", frozenEntry.Digest, audit.DecisionDeny)
}

// TestRevalidate_VersionSwap_IdentityDrift: um swap de versão a meio do run (mesmo
// contrato, version diferente) é drift → bloqueio + quarentena + alerta.
func TestRevalidate_VersionSwap_IdentityDrift(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	c := contractWith("v1", domain.EgressInternal)
	frozenEntry := signedEntry("tool.http", ver(1, 0, 0), domain.KindTool, c, pub)
	frozen := freeze(t, "run-1", frozenEntry)

	// backing store agora devolve v2.0.0 (mesmo contrato, digest igual, mas VERSÃO
	// pinada diferente da congelada) — mesmo com assinatura válida sobre v2.
	swapped := signedEntry("tool.http", ver(2, 0, 0), domain.KindTool, c, pub)

	h := newHarness(t, trust)
	dec, _ := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http",
		Current: swapped, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	})
	if dec.Allowed || dec.Reason != ReasonIdentityDrift || dec.Stage != StageDigest {
		t.Fatalf("decisão = %+v, quer block/identity_drift/digest", dec)
	}
	if h.quar.count() != 1 || h.alert.count() != 1 {
		t.Fatalf("quarentena=%d alertas=%d, quer 1/1", h.quar.count(), h.alert.count())
	}
}

// --- SEGURANÇA: assinatura / scope / egress ---------------------------------

func TestRevalidate_Security_Blocks(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	attacker := newSigner(t, "pub:attacker", 9)
	v1 := ver(1, 0, 0)

	tests := []struct {
		name       string
		build      func() (current domain.Entry, frozenEntry domain.Entry, trust TrustStore, policy Policy)
		wantReason Reason
		wantStage  Stage
		wantQuar   bool
	}{
		{
			name: "assinatura_invalida",
			build: func() (domain.Entry, domain.Entry, TrustStore, Policy) {
				c := contractWith("v1", domain.EgressInternal)
				fe := signedEntry("tool.http", v1, domain.KindTool, c, pub)
				cur := fe
				cur.Signature = "AAAA" // assinatura corrompida sobre o tuplo congelado
				return cur, fe, newTrust(t, pub), Policy{MaxEgress: domain.EgressExternal}
			},
			wantReason: ReasonSignatureInvalid, wantStage: StageSignature, wantQuar: true,
		},
		{
			name: "assinatura_de_chave_nao_confiavel",
			build: func() (domain.Entry, domain.Entry, TrustStore, Policy) {
				c := contractWith("v1", domain.EgressInternal)
				// Congelado íntegro (assinado por pub), mas backing store devolve a
				// MESMA definição assinada pelo ATACANTE (mesmo digest, outra chave).
				fe := signedEntry("tool.http", v1, domain.KindTool, c, pub)
				cur := signedEntry("tool.http", v1, domain.KindTool, c, attacker)
				// trust só confia em pub — a chave do atacante é desconhecida.
				return cur, fe, newTrust(t, pub), Policy{MaxEgress: domain.EgressExternal}
			},
			wantReason: ReasonUntrustedKey, wantStage: StageSignature, wantQuar: true,
		},
		{
			name: "assinatura_ausente",
			build: func() (domain.Entry, domain.Entry, TrustStore, Policy) {
				c := contractWith("v1", domain.EgressInternal)
				fe := signedEntry("tool.http", v1, domain.KindTool, c, pub)
				cur := fe
				cur.Signature = ""
				return cur, fe, newTrust(t, pub), Policy{MaxEgress: domain.EgressExternal}
			},
			wantReason: ReasonSignatureMissing, wantStage: StageSignature, wantQuar: true,
		},
		{
			name: "scope_fora_do_permitido",
			build: func() (domain.Entry, domain.Entry, TrustStore, Policy) {
				// digest congela COM o scope; a política do run NÃO o permite → passa
				// digest+assinatura, bloqueia em scope.
				c := contractWith("v1", domain.EgressInternal, "vault:db.admin")
				fe := signedEntry("tool.http", v1, domain.KindTool, c, pub)
				return fe, fe, newTrust(t, pub), Policy{AllowedScopes: []string{"vault:db.read"}, MaxEgress: domain.EgressExternal}
			},
			wantReason: ReasonScopeDenied, wantStage: StageScopeEgress, wantQuar: true,
		},
		{
			name: "egress_excede_tecto",
			build: func() (domain.Entry, domain.Entry, TrustStore, Policy) {
				c := contractWith("v1", domain.EgressExternal)
				fe := signedEntry("tool.http", v1, domain.KindTool, c, pub)
				return fe, fe, newTrust(t, pub), Policy{MaxEgress: domain.EgressInternal}
			},
			wantReason: ReasonEgressDenied, wantStage: StageScopeEgress, wantQuar: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cur, fe, trust, policy := tc.build()
			frozen := freeze(t, "run-1", fe)
			h := newHarness(t, trust)
			dec, err := h.rv.Revalidate(context.Background(), Request{
				RunID: "run-1", StepID: "s1", ToolID: "tool.http",
				Current: cur, Frozen: frozen, Policy: policy,
			})
			if err != nil {
				t.Fatalf("Revalidate erro: %v", err)
			}
			if dec.Allowed || dec.Reason != tc.wantReason || dec.Stage != tc.wantStage {
				t.Fatalf("decisão = %+v, quer block/%s/%s", dec, tc.wantReason, tc.wantStage)
			}
			if got := h.quar.count() > 0; got != tc.wantQuar {
				t.Fatalf("quarentena presente=%v, quer %v", got, tc.wantQuar)
			}
			if tc.wantQuar && h.alert.count() != 1 {
				t.Fatalf("alertas=%d, quer 1", h.alert.count())
			}
			// Toda a decisão de bloqueio é selada no audit (deny).
			recs := h.auditRecords(t, DefaultPartition)
			if len(recs) != 1 || recs[0].Decision != audit.DecisionDeny {
				t.Fatalf("audit = %+v, quer 1 deny", recs)
			}
		})
	}
}

// --- SEGURANÇA: egress allowlist por host (EPIC-07) -------------------------

func TestRevalidate_EgressHostAllowlist(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	c := contractWith("v1", domain.EgressExternal)
	entry := signedEntry("tool.http", v1, domain.KindTool, c, pub)
	frozen := freeze(t, "run-1", entry)
	allow := mcp.NewStaticEgressAllowlist("api.trusted.example")

	h := newHarness(t, trust, WithEgressAllowlist(allow))
	base := Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http",
		Current: entry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	}

	// Host permitido → passa.
	okReq := base
	okReq.EgressHost = "api.trusted.example"
	if dec, _ := h.rv.Revalidate(context.Background(), okReq); !dec.Allowed {
		t.Fatalf("host permitido bloqueado: %+v", dec)
	}
	// Host FORA da allowlist → bloqueio + quarentena + alerta.
	badReq := base
	badReq.EgressHost = "evil.example"
	dec, _ := h.rv.Revalidate(context.Background(), badReq)
	if dec.Allowed || dec.Reason != ReasonEgressHostDenied || dec.Stage != StageScopeEgress {
		t.Fatalf("host fora da allowlist = %+v, quer block/egress_host_denied", dec)
	}
	if h.quar.count() != 1 {
		t.Fatalf("quarentena=%d, quer 1", h.quar.count())
	}
}

// TestRevalidate_EgressHostMissing_FailClosed: com allowlist activa e egress não-none,
// um EgressHost AUSENTE não pode saltar a verificação por-host (fail-open por omissão
// de argumento) — bloqueia fail-closed com egress_host_denied + quarentena + alerta.
func TestRevalidate_EgressHostMissing_FailClosed(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	c := contractWith("v1", domain.EgressExternal)
	entry := signedEntry("tool.http", v1, domain.KindTool, c, pub)
	frozen := freeze(t, "run-1", entry)
	allow := mcp.NewStaticEgressAllowlist("api.trusted.example")

	h := newHarness(t, trust, WithEgressAllowlist(allow))
	// Sem EgressHost, mas egress da tool é External e há allowlist activa → bloqueio.
	dec, _ := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http",
		Current: entry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	})
	if dec.Allowed || dec.Reason != ReasonEgressHostDenied || dec.Stage != StageScopeEgress {
		t.Fatalf("host ausente com allowlist activa = %+v, quer block/egress_host_denied", dec)
	}
	if h.quar.count() != 1 || h.alert.count() != 1 {
		t.Fatalf("quarentena=%d alertas=%d, quer 1/1", h.quar.count(), h.alert.count())
	}
}

// TestRevalidate_EgressNone_HostOptional: uma tool com egress "none" NÃO requer host
// mesmo com allowlist activa — a verificação por-host só se aplica quando há egress.
func TestRevalidate_EgressNone_HostOptional(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	c := contractWith("v1", domain.EgressNone)
	entry := signedEntry("tool.local", v1, domain.KindTool, c, pub)
	frozen := freeze(t, "run-1", entry)
	allow := mcp.NewStaticEgressAllowlist("api.trusted.example")

	h := newHarness(t, trust, WithEgressAllowlist(allow))
	dec, _ := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.local",
		Current: entry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	})
	if !dec.Allowed {
		t.Fatalf("egress none sem host bloqueado indevidamente: %+v", dec)
	}
}

// TestRevalidate_AuditCorrelation_RunStep: as decisões seladas no audit carregam o
// RunID/StepID do PEDIDO concreto (correlação com a trajectória), não a partição nem
// um id@version sintético — tanto no allow como no deny, mantendo a Partition dedicada.
func TestRevalidate_AuditCorrelation_RunStep(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	entry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal), pub)
	frozen := freeze(t, "run-42", entry)
	h := newHarness(t, trust)

	// allow
	if dec, _ := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-42", StepID: "step-7", ToolID: "tool.http",
		Current: entry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	}); !dec.Allowed {
		t.Fatalf("allow bloqueado: %+v", dec)
	}
	// deny (drift): outro step do mesmo run.
	mutated := signedEntry("tool.http", v1, domain.KindTool, contractWith("MUTATED", domain.EgressInternal), pub)
	if dec, _ := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-42", StepID: "step-8", ToolID: "tool.http",
		Current: mutated, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	}); dec.Allowed {
		t.Fatalf("drift não bloqueado: %+v", dec)
	}

	recs := h.auditRecords(t, DefaultPartition)
	if len(recs) != 2 {
		t.Fatalf("audit records=%d, quer 2", len(recs))
	}
	for _, rec := range recs {
		if rec.Partition != DefaultPartition {
			t.Fatalf("Partition=%q, quer %q (fronteira de encadeamento dedicada)", rec.Partition, DefaultPartition)
		}
		if rec.RunID != "run-42" {
			t.Fatalf("audit RunID=%q, quer run-42 (correlação da trajectória perdida)", rec.RunID)
		}
	}
	if recs[0].Decision != audit.DecisionAllow || recs[0].StepID != "step-7" {
		t.Fatalf("registo allow = {decision:%q step:%q}, quer {allow step-7}", recs[0].Decision, recs[0].StepID)
	}
	if recs[1].Decision != audit.DecisionDeny || recs[1].StepID != "step-8" {
		t.Fatalf("registo deny = {decision:%q step:%q}, quer {deny step-8}", recs[1].Decision, recs[1].StepID)
	}
}

// --- LOOKUP: fora do congelado (default-deny, sem quarentena) ----------------

func TestRevalidate_NotFrozen(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	entry := signedEntry("tool.other", ver(1, 0, 0), domain.KindTool, contractWith("v1", domain.EgressNone), pub)
	frozen := freeze(t, "run-1", entry)
	h := newHarness(t, trust)

	// Nil frozen set.
	dec, _ := h.rv.Revalidate(context.Background(), Request{RunID: "run-1", ToolID: "tool.http", Frozen: nil})
	if dec.Allowed || dec.Reason != ReasonNotFrozen || dec.Stage != StageLookup {
		t.Fatalf("frozen nil = %+v, quer block/not_frozen/lookup", dec)
	}

	// Tool não pertence ao conjunto congelado.
	dec2, _ := h.rv.Revalidate(context.Background(), Request{RunID: "run-1", ToolID: "tool.http", Current: entry, Frozen: frozen})
	if dec2.Allowed || dec2.Reason != ReasonNotFrozen {
		t.Fatalf("tool fora do congelado = %+v, quer block/not_frozen", dec2)
	}
	// Sem quarentena (não há artefacto conhecido a isolar), mas auditado (deny).
	if h.quar.count() != 0 {
		t.Fatalf("lookup-miss gerou quarentena=%d", h.quar.count())
	}
	recs := h.auditRecords(t, DefaultPartition)
	if len(recs) != 2 { // um por chamada
		t.Fatalf("audit records=%d, quer 2", len(recs))
	}
}

// --- INTEGRAÇÃO: audit não-disponível degrada permit para deny --------------

func TestRevalidate_UnauditablePermit_DegradesToDeny(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	entry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressNone), pub)
	frozen := freeze(t, "run-1", entry)

	// audit indisponível → mesmo com todos os passos a passar, a autorização não é
	// auditável e degrada para deny (fail-closed, ADR-002/010).
	q := &recordingQuarantine{}
	al := &recordingAlerter{}
	rv, err := New(trust, failingAudit{}, WithClock(fixedClock()), WithQuarantiner(q), WithAlerter(al))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dec, _ := rv.Revalidate(context.Background(), Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http",
		Current: entry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	})
	if dec.Allowed || dec.Reason != ReasonAuditFailed || dec.Stage != StageExec {
		t.Fatalf("decisão = %+v, quer block/audit_failed/exec", dec)
	}
	if _, ok := dec.Permit(); ok {
		t.Fatal("autorização não-auditável devolveu permit")
	}
}

// --- Quarentena falhada NÃO desbloqueia -------------------------------------

func TestRevalidate_QuarantineFailure_StillBlocks(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	frozenEntry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal), pub)
	frozen := freeze(t, "run-1", frozenEntry)
	mutated := signedEntry("tool.http", v1, domain.KindTool, contractWith("MUTATED", domain.EgressInternal), pub)

	q := &recordingQuarantine{err: errAuditDown} // quarentena falha
	al := &recordingAlerter{}
	rv, _ := New(trust, audit.NewMemStore(), WithClock(fixedClock()), WithQuarantiner(q), WithAlerter(al))
	dec, _ := rv.Revalidate(context.Background(), Request{
		RunID: "run-1", ToolID: "tool.http", Current: mutated, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal},
	})
	if dec.Allowed {
		t.Fatal("quarentena falhada NÃO deve desbloquear")
	}
	// O alerta reflecte a quarentena falhada.
	al2, _ := al.last()
	if al2.QuarantineErr == nil {
		t.Fatal("alerta não reflectiu a falha de quarentena")
	}
}

// --- Determinismo: mesma entrada, mesmo veredicto ---------------------------

func TestRevalidate_Deterministic(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	entry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal, "vault:db.read"), pub)
	frozen := freeze(t, "run-1", entry)
	h := newHarness(t, trust)
	req := Request{RunID: "run-1", ToolID: "tool.http", Current: entry, Frozen: frozen,
		Policy: Policy{AllowedScopes: []string{"vault:db.read"}, MaxEgress: domain.EgressInternal}}

	var first Decision
	for i := 0; i < 20; i++ {
		d, err := h.rv.Revalidate(context.Background(), req)
		if err != nil {
			t.Fatalf("Revalidate: %v", err)
		}
		if i == 0 {
			first = d
			continue
		}
		if d.Allowed != first.Allowed || d.Reason != first.Reason || d.Stage != first.Stage || d.Digest != first.Digest {
			t.Fatalf("veredicto não-determinista na iteração %d: %+v vs %+v", i, d, first)
		}
	}
	if !first.Allowed {
		t.Fatalf("esperava permit, veio %+v", first)
	}
}

// --- Observabilidade: spans públicos, sem segredos --------------------------

func TestRevalidate_SpanAttributes(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	entry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal, "vault:db.read"), pub)
	frozen := freeze(t, "run-1", entry)
	tr := &agentruntime.RecordingTracer{}
	h := newHarness(t, trust, WithTracer(tr))
	if _, err := h.rv.Revalidate(context.Background(), Request{
		RunID: "run-1", ToolID: "tool.http", Current: entry, Frozen: frozen,
		Policy: Policy{AllowedScopes: []string{"vault:db.read"}, MaxEgress: domain.EgressInternal},
	}); err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	spans := tr.SpansByOperation(opRevalidate)
	if len(spans) != 1 {
		t.Fatalf("spans=%d, quer 1", len(spans))
	}
	// O digest é público (está no span); NENHUM scope pode aparecer nos atributos.
	for k, v := range spans[0].Attributes {
		if s, ok := v.(string); ok && s == "vault:db.read" {
			t.Fatalf("scope vazou para o atributo de span %q", k)
		}
	}
	if spans[0].Attributes[attrDecision] != "permitted" {
		t.Fatalf("atributo decisão = %v, quer permitted", spans[0].Attributes[attrDecision])
	}
}

// assertAuditRecord confirma que um registo tem o tuplo id/version/digest/resultado
// exigido por AOS-051.
func assertAuditRecord(t *testing.T, rec audit.AuditRecord, id, ver, dig string, dec audit.Decision) {
	t.Helper()
	if rec.ToolID != id {
		t.Fatalf("audit ToolID=%q, quer %q", rec.ToolID, id)
	}
	if rec.PolicyVersion != ver {
		t.Fatalf("audit PolicyVersion=%q, quer %q", rec.PolicyVersion, ver)
	}
	if rec.Resource.Value != dig {
		t.Fatalf("audit Resource.Value=%q, quer %q", rec.Resource.Value, dig)
	}
	if rec.Decision != dec {
		t.Fatalf("audit Decision=%q, quer %q", rec.Decision, dec)
	}
}
