package referencemonitor_test

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// TestNewProductionWiresActiveTaintGate prova END-TO-END que o Monitor devolvido
// por NewProduction tem a barreira control/data-plane REALMENTE activa: a mesma
// injecção que passaria num RM default ([DefaultHooks]) é BLOQUEADA aqui, atribuída
// ao gate "taint". Não é uma verificação de estrutura em vácuo — medeia uma call.
func TestNewProductionWiresActiveTaintGate(t *testing.T) {
	sink := &spySink{}
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)

	m, err := referencemonitor.NewProduction(priv, referencemonitor.WithEventSink(sink))
	if err != nil {
		t.Fatalf("NewProduction erro inesperado: %v", err)
	}
	if err := m.Register("exfil", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), privilegedCall("exfil", taint.StringUntrusted))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("privilegiada+untrusted devia ser DENY, got %s", dec.Effect)
	}
	if dec.DeniedBy != "taint" {
		t.Errorf("DeniedBy=%q want \"taint\" (a defesa deve vir do TaintGate)", dec.DeniedBy)
	}
}

// TestNewProductionNilPrivilegedFailsClosed: sem PrivilegedAuthorizer o TaintGate
// seria um no-op — NewProduction recusa a construção em vez de devolver um RM
// com defesa ASI01 silenciosamente desligada.
func TestNewProductionNilPrivilegedFailsClosed(t *testing.T) {
	m, err := referencemonitor.NewProduction(nil, referencemonitor.WithEventSink(&spySink{}))
	if !errors.Is(err, referencemonitor.ErrNoPrivilegedAuthorizer) {
		t.Fatalf("erro=%v want ErrNoPrivilegedAuthorizer", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil quando a construção falha fail-closed")
	}
}

// TestNewProductionRejectsTaintStrippingOverride é o guard central de F5: um
// override via WithHooks que remova o TaintGate (a cadeia neutra default) NÃO
// produz um RM de produção — a construção falha, tornando a misconfiguração
// impossível de introduzir silenciosamente pela via sancionada.
func TestNewProductionRejectsTaintStrippingOverride(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	m, err := referencemonitor.NewProduction(
		priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...), // remove o taint
	)
	if !errors.Is(err, referencemonitor.ErrTaintGateMissing) {
		t.Fatalf("erro=%v want ErrTaintGateMissing", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil quando o TaintGate é removido")
	}
}

// TestNewProductionRejectsInactiveTaintGate: um TaintGate com authorizer nil é um
// no-op e não conta como enforcement — a verificação é a de gate ACTIVO, não só
// a de um hook chamado "taint".
func TestNewProductionRejectsInactiveTaintGate(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	// Cadeia com um TaintGate no-op (authorizer nil) — não é enforcement real.
	inactive := append(referencemonitor.DefaultHooks(), referencemonitor.NewTaintGate(nil))
	m, err := referencemonitor.NewProduction(
		priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(inactive...),
	)
	if !errors.Is(err, referencemonitor.ErrTaintGateMissing) {
		t.Fatalf("erro=%v want ErrTaintGateMissing (gate no-op não conta)", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil com TaintGate no-op")
	}
}

// TestNewProductionRejectsNoDurableAudit: sem WithEventSink o sink é o discard
// (não-durável) e o fail-closed de auditoria nunca disparava — recusado.
func TestNewProductionRejectsNoDurableAudit(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	m, err := referencemonitor.NewProduction(priv) // sem WithEventSink ⇒ discardSink
	if !errors.Is(err, referencemonitor.ErrNoDurableAudit) {
		t.Fatalf("erro=%v want ErrNoDurableAudit", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil sem auditoria durável")
	}
}

// TestNewProductionAcceptsExplicitTaintChain: passar explicitamente a cadeia
// canónica-com-taint via WithHooks (com um authorizer real) é aceite — o guard
// verifica a invariante, não impõe uma composição literal única.
func TestNewProductionAcceptsExplicitTaintChain(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	m, err := referencemonitor.NewProduction(
		priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(referencemonitor.DefaultHooksWithTaint(priv)...),
	)
	if err != nil {
		t.Fatalf("NewProduction erro inesperado: %v", err)
	}
	if m == nil {
		t.Fatal("Monitor não devia ser nil numa construção válida")
	}
}
