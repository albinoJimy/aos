package securitytests

import (
	"context"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox/network"
)

// ===========================================================================
// CENÁRIO 2 — EXFILTRAÇÃO (AOS-067/068, padrão CamoLeak CVSS 9.6)
//
// O risco real não é o `rm -rf` mas a fuga de dados via tools "benignas". A defesa
// arquitectural é rede default-deny + DNS filtrado: egress fora da allowlist, DNS
// tunneling / domínio fora da allowlist, e tool com tipo de recurso mislabelado são
// TODOS negados E (onde o controlo sela nativamente) auditados no WORM tamper-evident.
// ORQUESTRA EgressFilter/EgressHook/DNSFilter reais; não os reimplementa.
// ===========================================================================

// TestExfiltration_EgressOutsideAllowlist_BlockedAndAudited prova que um egress para um
// destino FORA da allowlist do principal é BLOQUEADO pelo Reference Monitor (via o
// EgressHook real) e SELADO no audit WORM tamper-evident (AOS-072). Como controlo de
// não-tautologia, um destino DENTRO da allowlist é PERMITIDO.
func TestExfiltration_EgressOutsideAllowlist_BlockedAndAudited(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm, err := buildEgressRM(store, es)
	if err != nil {
		t.Fatalf("buildEgressRM: %v", err)
	}

	c := mustCorpus(t)
	var blocked int
	for _, v := range c.ExfilEgress {
		if v.Kind != "not_in_allowlist" {
			continue
		}
		dec, _ := rm.Mediate(context.Background(), egressCall(v))
		if dec.Effect != referencemonitor.EffectDeny {
			t.Fatalf("egress %q: efeito = %q, quer deny (fora da allowlist)", v.ID, dec.Effect)
		}
		if dec.DeniedBy != "egress" {
			t.Fatalf("egress %q: DeniedBy = %q, quer \"egress\"", v.ID, dec.DeniedBy)
		}
		blocked++
	}
	if blocked == 0 {
		t.Fatal("corpus sem vectores de egress fora da allowlist: cenário vácuo")
	}

	// AUDIT WORM NATIVO (AOS-067): os bloqueios selaram na partição por-principal.
	part := network.EgressAuditPartition(egressPrincipal())
	recs := verifyWORM(t, store, part)
	deny := findDeny(t, recs, "sandbox.network")
	if deny.Resource.Value == "" {
		t.Fatal("registo de egress bloqueado sem destino selado")
	}

	// Controlo (não-tautologia): um destino DENTRO da allowlist é PERMITIDO.
	allowed := egressVector{
		ID: "control-allowed", ResourceType: "url",
		Target: "https://api.github.com/repos", Capability: "cap:http.get",
	}
	dec, _ := rm.Mediate(context.Background(), egressCall(allowed))
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("destino na allowlist = %q, quer permit (allowlist seria tautológica): reason=%s", dec.Effect, dec.Reason)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "exfiltration_egress", "sandbox.network", network.ReasonNotInList)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestExfiltration_DNSTunneling_BlockedAndAudited prova que o DNSFilter (AOS-068) NEGA e
// SELA (a) uma consulta a um domínio fora da allowlist (nem chega a resolver — fecha o
// canal de exfiltração por DNS) e (b) uma consulta de alta entropia (tunneling de dados
// encapsulados). Controlo de não-tautologia: um nome na allowlist resolve.
func TestExfiltration_DNSTunneling_BlockedAndAudited(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	filter, err := buildDNSFilter(store)
	if err != nil {
		t.Fatalf("buildDNSFilter: %v", err)
	}
	ctx := context.Background()
	principal := egressPrincipal()

	c := mustCorpus(t)
	if len(c.ExfilDNS) == 0 {
		t.Fatal("corpus sem vectores de DNS: cenário vácuo")
	}
	for _, v := range c.ExfilDNS {
		ips, dec, err := filter.Resolve(ctx, principal, v.QName)
		if err != nil {
			t.Fatalf("dns %q: erro operacional (audit falhou?): %v", v.ID, err)
		}
		if dec.Allow || ips != nil {
			t.Fatalf("dns %q: resolvido (allow=%v, ips=%v), quer BLOQUEADO", v.ID, dec.Allow, ips)
		}
		wantReason := map[string]string{
			"not_in_allowlist":       network.ReasonDNSNotInList,
			"high_entropy_tunneling": network.ReasonDNSExfilEntropy,
		}[v.Kind]
		if wantReason == "" {
			t.Fatalf("dns %q: kind desconhecido %q", v.ID, v.Kind)
		}
		if dec.Reason != wantReason {
			t.Fatalf("dns %q: razão = %q, quer %q", v.ID, dec.Reason, wantReason)
		}
	}

	// AUDIT WORM NATIVO (AOS-068 reutiliza o sink de AOS-067): as negações selaram.
	part := network.EgressAuditPartition(principal)
	recs := verifyWORM(t, store, part)
	_ = findDeny(t, recs, "sandbox.network")

	// Controlo (não-tautologia): um nome na allowlist, coerente com o IP, RESOLVE.
	ips, dec, err := filter.Resolve(ctx, principal, "api.github.com")
	if err != nil {
		t.Fatalf("dns controlo: %v", err)
	}
	if !dec.Allow || len(ips) == 0 {
		t.Fatalf("nome na allowlist = (allow=%v, ips=%v), quer resolvido (filtro seria tautológico)", dec.Allow, ips)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "exfiltration_dns", "sandbox.network", network.ReasonDNSNotInList)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestExfiltration_BenignToolMislabeled_Blocked prova o vector CamoLeak: uma tool
// "benigna" que declara uma CAPABILITY de rede (cap:http.post) mas apresenta um
// Resource.Type NÃO-rede (file) — para escapar à verificação de destino — é NEGADA
// fail-closed pelo EgressHook (o destino não é derivável nem verificável contra a
// allowlist). Nunca se abstém (o que a deixaria exfiltrar sem verificação).
func TestExfiltration_BenignToolMislabeled_Blocked(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm, err := buildEgressRM(store, es)
	if err != nil {
		t.Fatalf("buildEgressRM: %v", err)
	}

	c := mustCorpus(t)
	var found bool
	for _, v := range c.ExfilEgress {
		if v.Kind != "mislabeled_camoleak" {
			continue
		}
		found = true
		dec, _ := rm.Mediate(context.Background(), egressCall(v))
		if dec.Effect != referencemonitor.EffectDeny {
			t.Fatalf("mislabeled %q: efeito = %q, quer deny fail-closed", v.ID, dec.Effect)
		}
		if dec.DeniedBy != "egress" {
			t.Fatalf("mislabeled %q: DeniedBy = %q, quer \"egress\"", v.ID, dec.DeniedBy)
		}
		if !strings.Contains(dec.Reason, "fail-closed") {
			t.Fatalf("mislabeled %q: razão = %q, quer conter \"fail-closed\"", v.ID, dec.Reason)
		}
	}
	if !found {
		t.Fatal("corpus sem vector mislabeled_camoleak: cenário vácuo")
	}

	// A barreira aqui é estrutural (deriva-antes-de-verificar): o EgressHook NEGA sem
	// selar nativamente (não há destino a selar). A suite atesta o bloqueio — já provado
	// pelas asserções acima — no seu ledger tamper-evident.
	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "exfiltration_mislabeled", "sandbox.network", network.ReasonUnverifiableEgress)
	verifyWORM(t, ledger, suiteLedgerPartition)
}
