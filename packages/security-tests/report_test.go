package securitytests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/broker"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
	"github.com/aos-ref/substrate/sandbox/seccomp"
)

// suiteReport é o veredicto agregado da suite adversarial (o "pass" é o ÚLTIMO campo,
// para o gate CI ancorar ao fim da linha — molde AOS_SUPPLYCHAIN_REPORT/AOS_ROUTING_REPORT).
type suiteReport struct {
	Suite            string `json:"suite"`
	PromptInjection  bool   `json:"prompt_injection"`
	ExfilEgress      bool   `json:"exfil_egress"`
	ExfilDNS         bool   `json:"exfil_dns"`
	Secrets          bool   `json:"secrets"`
	IsolationOverlay bool   `json:"isolation_overlay"`
	IsolationSeccomp bool   `json:"isolation_seccomp"`
	Pass             bool   `json:"pass"`
}

// TestSuiteReportEmitted re-corre CADA cenário como um PROBE puro (sem *testing.T nas
// asserções) e emite o veredicto agregado numa linha marcada AOS_SECURITY_REPORT que o
// gate scripts/ci/security.sh ancora ao "pass":true final (fail-closed sobre o agregado).
// Também FALHA o teste se o agregado não for pass — dupla salvaguarda com require_tests.
func TestSuiteReportEmitted(t *testing.T) {
	r := suiteReport{
		Suite:            SuiteVersion,
		PromptInjection:  probePromptInjectionBlocked(),
		ExfilEgress:      probeEgressBlocked(),
		ExfilDNS:         probeDNSBlocked(),
		Secrets:          probeSecretNotObservable(),
		IsolationOverlay: probeIsolationOverlayDoesNotPersist(),
		IsolationSeccomp: probeIsolationSeccompBlocks(),
	}
	r.Pass = r.PromptInjection && r.ExfilEgress && r.ExfilDNS && r.Secrets &&
		r.IsolationOverlay && r.IsolationSeccomp

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	fmt.Printf("AOS_SECURITY_REPORT %s\n", b)

	if !r.Pass {
		t.Fatalf("veredicto agregado da suite não é pass: %s", b)
	}
}

// ---------------------------------------------------------------------------
// Probes puros (t-free): constroem os controlos REAIS e devolvem o veredicto.
// Qualquer erro de construção resolve false (fail-closed — o relatório mostra-o).
// ---------------------------------------------------------------------------

func probePromptInjectionBlocked() bool {
	es, err := eventstore.New()
	if err != nil {
		return false
	}
	defer func() { _ = es.Close() }()
	rm := buildTaintRM(es, true)
	dec := mediatePrivileged(rm, taint.LabelFor(taint.OriginToolResult), "injecção untrusted")
	return dec.Effect == referencemonitor.EffectDeny && dec.DeniedBy == "taint"
}

func probeEgressBlocked() bool {
	store := audit.NewMemStore()
	es, err := eventstore.New()
	if err != nil {
		return false
	}
	defer func() { _ = es.Close() }()
	rm, err := buildEgressRM(store, es)
	if err != nil {
		return false
	}
	v := egressVector{ID: "probe", ResourceType: "url", Target: "https://attacker.example/collect", Capability: "cap:http.post"}
	dec, _ := rm.Mediate(context.Background(), egressCall(v))
	return dec.Effect == referencemonitor.EffectDeny && dec.DeniedBy == "egress"
}

func probeDNSBlocked() bool {
	store := audit.NewMemStore()
	filter, err := buildDNSFilter(store)
	if err != nil {
		return false
	}
	_, dec, err := filter.Resolve(context.Background(), egressPrincipal(), "exfil.attacker.example")
	if err != nil {
		return false
	}
	return !dec.Allow
}

func probeSecretNotObservable() bool {
	st, err := buildBrokerStack(time.Minute)
	if err != nil {
		return false
	}
	defer func() { _ = st.es.Close() }()
	inj, err := st.b.NewInjector(st.guest)
	if err != nil {
		return false
	}
	ctx := context.Background()
	h, err := st.b.Exchange(ctx, brokerRequest("run-1"))
	if err != nil {
		return false
	}
	if err := inj.Inject(ctx, string(h), sandbox.Instance{ID: "vm-1"}); err != nil {
		return false
	}
	if scanLeak("handle", string(h), brokerSentinel) {
		return false
	}
	sec, err := st.vault.Fetch(ctx, broker.VaultKey{Provider: "stripe", Region: "eu", Capability: "cap:pay.charge"})
	if err != nil {
		return false
	}
	if scanLeak("secret", sec.String(), brokerSentinel) {
		return false
	}
	evs, err := st.es.Read(ctx, "run-1", 1)
	if err != nil || len(evs) == 0 {
		return false
	}
	for _, e := range evs {
		raw, err := json.Marshal(e)
		if err != nil || scanLeak("event", string(raw), brokerSentinel) {
			return false
		}
	}
	return st.guest.Injections() == 1
}

func probeIsolationOverlayDoesNotPersist() bool {
	store, err := eventstore.New()
	if err != nil {
		return false
	}
	defer func() { _ = store.Close() }()
	snap, err := sandbox.NewSnapshot("img/probe", map[string][]byte{"etc/config": []byte("base-config")})
	if err != nil {
		return false
	}
	launcher, err := sandbox.NewLauncher(sandbox.NewFakeDriver(),
		sandbox.WithEventSink(sandbox.NewEventStoreSink(store)),
		sandbox.WithSnapshot(snap),
	)
	if err != nil {
		return false
	}
	ml, err := sandbox.NewMediatedLauncher(newProbeMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		return false
	}
	if _, err := ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{RunID: "run-n", StepID: "s", Call: sandbox.ToolCall{Command: "write", Path: "run/secret", Write: []byte("N")}}); err != nil {
		return false
	}
	res, err := ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{RunID: "run-n1", StepID: "s", Call: sandbox.ToolCall{Command: "read", Path: "run/secret"}})
	if err != nil {
		return false
	}
	return res.ExitCode != 0 // ausente em N+1 → isolado
}

func probeIsolationSeccompBlocks() bool {
	store, err := eventstore.New()
	if err != nil {
		return false
	}
	defer func() { _ = store.Close() }()
	restrictive, err := seccomp.Parse([]byte(`{"version":"probe/v1","default_action":"deny","allowed_syscalls":["read"]}`))
	if err != nil {
		return false
	}
	snap, err := sandbox.NewSnapshot("img/probe-sec", map[string][]byte{"etc/config": []byte("base-config")})
	if err != nil {
		return false
	}
	launcher, err := sandbox.NewLauncher(sandbox.NewFakeDriver(),
		sandbox.WithEventSink(sandbox.NewEventStoreSink(store)),
		sandbox.WithSnapshot(snap),
		sandbox.WithSeccompProfile(restrictive),
	)
	if err != nil {
		return false
	}
	ml, err := sandbox.NewMediatedLauncher(newProbeMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		return false
	}
	_, err = ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{RunID: "run-deny", StepID: "s", Call: sandbox.ToolCall{Command: "write", Path: "etc/config", Write: []byte("x")}})
	return err != nil // ErrSeccompDenied esperado
}

// newProbeMonitor é o RM permissivo dos probes (equivalente t-free a newPermitMonitor).
func newProbeMonitor(store *eventstore.Store) *referencemonitor.Monitor {
	return referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
}
