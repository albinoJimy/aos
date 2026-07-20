package securitytests

import (
	"context"
	"strings"
	"testing"

	pdp "github.com/aos-ref/control-plane/pdp"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// ===========================================================================
// CENÁRIO 8 — CENÁRIO 1 REFORÇADO COM O PDP REAL (AOS-113, ADR-005)
//
// Reforça a prova de mediação compondo o PDP REAL (bundle Cedar assinado + allowlist
// default-deny keyed por agent_class) COMO o hook "policy" do Reference Monitor, ALÉM do
// TaintGate inserido logo após a política (a composição recomendada de AOS-069). Prova
// DEFESA-EM-CAMADAS e ASSERE QUAL a camada nega:
//
//   - ALLOWLIST/PDP (default-deny primário): uma capability NÃO concedida à agent_class é
//     negada pelo PDP → DeniedBy=="policy";
//   - TAINT GATE (defesa-em-profundidade): uma capability QUE O PDP PERMITE mas cuja
//     autorização é untrusted é negada pelo TaintGate → DeniedBy=="taint".
//
// Liga o AOS-113 (PDP) ao red-team. ORQUESTRA pdp.NewPolicyCheck + rm.NewTaintGate reais;
// não os reimplementa. O bundle assinado é o committado em control-plane/pdp/policies.
// ===========================================================================

// pdpPolicyDir é o caminho (relativo ao directório do pacote — o CWD de `go test`) do
// bundle de política assinado e committado do PDP (AOS-113). É a fonte de verdade única:
// a suite lê-o, não o duplica.
const pdpPolicyDir = "../control-plane/pdp/policies"

// pdpToolID é o id da tool privilegiada mediada no cenário.
const pdpToolID = "agent.fs.tool"

// layeredPrivilegedCaps são as capabilities classificadas privilegiadas pelo TaintGate.
// Inclui cap:fs.read — que o PDP PERMITE mesmo sob taint untrusted (a regra Cedar
// allow_fs_read não condiciona o taint) — para que a camada de taint seja exercitada de
// forma DISTINTA da camada de política.
var layeredPrivilegedCaps = []string{"cap:fs.read", "cap:http.post", "cap:payments.charge"}

// buildLayeredRM compõe o RM com o PDP REAL no slot "policy" e o TaintGate logo a seguir
// (identity → policy(PDP) → taint → budget → egress → audit), espelhando
// DefaultHooksWithTaint mas substituindo o PolicyStub pelo PDP real. withTaint=false
// remove o TaintGate (usado pelo meta-teste para provar que a camada de taint é
// load-bearing).
func buildLayeredRM(t *testing.T, es eventstore.EventStore, withTaint bool) *referencemonitor.Monitor {
	t.Helper()
	p, err := pdp.Open(pdpPolicyDir)
	if err != nil {
		t.Fatalf("pdp.Open(%q): %v", pdpPolicyDir, err)
	}
	hooks := []referencemonitor.Hook{
		referencemonitor.IdentityStub{},
		pdp.NewPolicyCheck(p), // PDP real (default-deny primário) no lugar do PolicyStub
	}
	if withTaint {
		hooks = append(hooks, referencemonitor.NewTaintGate(
			referencemonitor.NewStaticPrivilegedSet(layeredPrivilegedCaps...),
		))
	}
	hooks = append(hooks, referencemonitor.BudgetStub{}, referencemonitor.EgressStub{}, referencemonitor.AuditStub{})

	rm := referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	_ = rm.Register(pdpToolID, func(_ context.Context, in []byte) ([]byte, error) {
		return append([]byte("ok:"), in...), nil
	})
	return rm
}

// layeredCall monta um Call para uma agent_class, capability, taint e região dados. A
// Authority inclui a capability (a regra Cedar exige que a autoridade delegada a cubra) —
// o gate que morde é a ALLOWLIST (classe) ou o TAINT, não uma autoridade em falta.
func layeredCall(agentClass, capability, taint, region string) referencemonitor.Call {
	return referencemonitor.Call{
		RunID: "run-pdp", StepID: "step-1", RequestID: "req-1",
		ToolID:     pdpToolID,
		Capability: capability,
		Resource:   referencemonitor.Resource{Type: "file", Value: "/etc/data", Region: region},
		Principal: referencemonitor.Principal{
			NHIID: "nhi-1", AgentClass: agentClass, Authority: []string{capability},
		},
		Context:    referencemonitor.CallContext{Taint: taint},
		Credential: "tok-test",
	}
}

// TestPDPLayered_AllowlistDeniesUnpermittedCapability prova a camada ALLOWLIST/PDP: uma
// capability NÃO concedida à agent_class (agent-worker não tem cap:payments.charge) é
// negada pelo PDP default-deny — DeniedBy=="policy", com "default-deny" na razão.
func TestPDPLayered_AllowlistDeniesUnpermittedCapability(t *testing.T) {
	t.Parallel()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm := buildLayeredRM(t, es, true)

	// agent-worker + cap:payments.charge (fora da allowlist) → deny pela política.
	dec, _ := rm.Mediate(context.Background(), layeredCall("agent-worker", "cap:payments.charge", "trusted", "eu"))
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("capability fora da allowlist: efeito=%q, quer deny", dec.Effect)
	}
	if dec.DeniedBy != "policy" {
		t.Fatalf("DeniedBy=%q, quer \"policy\" (camada allowlist/PDP)", dec.DeniedBy)
	}
	if !strings.Contains(dec.Reason, "default-deny") {
		t.Fatalf("razão=%q, quer conter \"default-deny\"", dec.Reason)
	}

	// Camada allowlist também morde por CLASSE: uma classe SEM allowlist (agent-unknown)
	// é negada mesmo para uma capability que o Cedar permitiria (cap:fs.read).
	decClass, _ := rm.Mediate(context.Background(), layeredCall("agent-unknown", "cap:fs.read", "trusted", "eu"))
	if decClass.Effect != referencemonitor.EffectDeny || decClass.DeniedBy != "policy" {
		t.Fatalf("classe sem allowlist: efeito=%q DeniedBy=%q, quer deny/policy", decClass.Effect, decClass.DeniedBy)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "pdp_layered_allowlist", pdpToolID, dec.Reason)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestPDPLayered_TaintGateDeniesUntrustedForPermittedCapability prova a camada TAINT: uma
// capability que o PDP PERMITE (agent-worker + cap:fs.read; a regra Cedar allow_fs_read
// não condiciona o taint) mas cuja autorização é UNTRUSTED é negada pelo TaintGate —
// DeniedBy=="taint". A não-tautologia é o par: a MESMA call com taint TRUSTED é PERMITIDA
// e despachada.
func TestPDPLayered_TaintGateDeniesUntrustedForPermittedCapability(t *testing.T) {
	t.Parallel()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm := buildLayeredRM(t, es, true)

	// agent-worker + cap:fs.read + untrusted: o PDP permite, o TAINT nega (defesa-em-profundidade).
	dec, _ := rm.Mediate(context.Background(), layeredCall("agent-worker", "cap:fs.read", "untrusted", "eu"))
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("cap permitida + untrusted: efeito=%q, quer deny", dec.Effect)
	}
	if dec.DeniedBy != "taint" {
		t.Fatalf("DeniedBy=%q, quer \"taint\" (defesa-em-profundidade sobre o PDP)", dec.DeniedBy)
	}

	// Não-tautologia: a MESMA call com autorização TRUSTED passa as duas camadas e é despachada.
	permit, _ := rm.Mediate(context.Background(), layeredCall("agent-worker", "cap:fs.read", "trusted", "eu"))
	if permit.Effect != referencemonitor.EffectPermit {
		t.Fatalf("cap permitida + trusted: efeito=%q, quer permit (camadas seriam tautológicas): reason=%s", permit.Effect, permit.Reason)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "pdp_layered_taint", pdpToolID, dec.Reason)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestPDPLayered_PDPOwnTaintRuleDeniesHTTPPost prova que a PRÓPRIA política Cedar é
// taint-aware onde o exige: cap:http.post sob taint untrusted é negada pelo PDP (a regra
// allow_http_post condiciona context.taint != "untrusted") — DeniedBy=="policy", ANTES
// sequer do TaintGate. Distingue a barreira PRIMÁRIA (política) da defesa-em-profundidade.
func TestPDPLayered_PDPOwnTaintRuleDeniesHTTPPost(t *testing.T) {
	t.Parallel()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm := buildLayeredRM(t, es, true)

	// cap:http.post exige region=eu na regra; usamos uma call http com region eu mas taint untrusted.
	call := layeredCall("agent-worker", "cap:http.post", "untrusted", "eu")
	call.Resource.Type = "url"
	call.Resource.Value = "https://api.example.com/orders"
	dec, _ := rm.Mediate(context.Background(), call)
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "policy" {
		t.Fatalf("http.post untrusted: efeito=%q DeniedBy=%q, quer deny/policy (regra Cedar taint-aware)", dec.Effect, dec.DeniedBy)
	}
}

// TestMetaDetects_PDPLayered_WhenTaintGateAbsent — com o TaintGate AUSENTE da cadeia
// (defesa-em-profundidade contornada), a MESMA cap:fs.read autorizada por untrusted é
// PERMITIDA pelo PDP: o ataque passa. Prova que (i) o bloqueio de taint do cenário vem
// MESMO do TaintGate e (ii) o PDP sozinho permite fs.read sob untrusted — logo a
// defesa-em-profundidade é necessária, não redundante.
func TestMetaDetects_PDPLayered_WhenTaintGateAbsent(t *testing.T) {
	t.Parallel()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm := buildLayeredRM(t, es, false) // sem TaintGate

	dec, _ := rm.Mediate(context.Background(), layeredCall("agent-worker", "cap:fs.read", "untrusted", "eu"))
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("sem o TaintGate, a cap permitida sob untrusted devia PASSAR (permit); got %q DeniedBy=%q (deteção vácua?)", dec.Effect, dec.DeniedBy)
	}
}
