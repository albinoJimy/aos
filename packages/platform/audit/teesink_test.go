package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// recordingSink é um EventSink de teste que conta chamadas e devolve um seq fixo.
type recordingSink struct {
	calls int
	seq   uint64
	last  referencemonitor.MediationRecord
}

func (r *recordingSink) RecordMediation(_ context.Context, rec referencemonitor.MediationRecord) (uint64, error) {
	r.calls++
	r.last = rec
	return r.seq, nil
}

// errSink é um EventSink que falha sempre (para exercitar o fail-closed do tee).
type errSink struct{ calls int }

func (e *errSink) RecordMediation(context.Context, referencemonitor.MediationRecord) (uint64, error) {
	e.calls++
	return 0, errors.New("sink indisponivel")
}

// TestTeeSinkFanout — AOS-011-Q4: o tee escreve em TODOS os sinks (Event Store E
// cadeia de audit em simultâneo) e devolve o seq do primário (primeiro sink).
func TestTeeSinkFanout(t *testing.T) {
	ctx := context.Background()
	primary := &recordingSink{seq: 42}
	auditStore := NewMemStore()
	chain := NewMediationSink(auditStore, withSinkClock(func() time.Time { return fixedTime }))

	tee := NewTeeSink(primary, chain)
	rec := referencemonitor.MediationRecord{
		RunID: "run-1", StepID: "s1", Effect: referencemonitor.EffectPermit,
		ToolID: "tool.http", Capability: "cap:x",
		Principal: referencemonitor.Principal{NHIID: "nhi:a"},
	}
	seq, err := tee.RecordMediation(ctx, rec)
	if err != nil {
		t.Fatalf("RecordMediation: %v", err)
	}
	if seq != 42 {
		t.Fatalf("seq do primario=%d, esperado 42", seq)
	}
	if primary.calls != 1 {
		t.Fatalf("primario chamado %d vezes, esperado 1", primary.calls)
	}
	// A cadeia de audit tem de ter recebido e selado o registo.
	if h, _ := auditStore.Head(ctx, "run-1"); h != 1 {
		t.Fatalf("cadeia de audit nao recebeu o registo: head=%d", h)
	}
	if r, ok, _ := auditStore.At(ctx, "run-1", 1); !ok || r.ToolID != "tool.http" {
		t.Fatalf("registo de audit ausente/errado: ok=%v rec=%+v", ok, r)
	}
}

// TestTeeSinkFailClosed — se QUALQUER sink falhar, o tee devolve erro (fail-closed):
// no caminho de permit, o RM degrada a decisão para Deny.
func TestTeeSinkFailClosed(t *testing.T) {
	ctx := context.Background()
	auditStore := NewMemStore()
	chain := NewMediationSink(auditStore, withSinkClock(func() time.Time { return fixedTime }))
	bad := &errSink{}

	// Ordem: cadeia OK primeiro, depois o sink que falha → o tee tem de propagar erro.
	tee := NewTeeSink(chain, bad)
	_, err := tee.RecordMediation(ctx, referencemonitor.MediationRecord{
		RunID: "run-2", Effect: referencemonitor.EffectPermit,
		Principal: referencemonitor.Principal{NHIID: "nhi:a"},
	})
	if err == nil {
		t.Fatal("esperado erro fail-closed quando um sink falha")
	}
	if bad.calls != 1 {
		t.Fatalf("sink com falha chamado %d vezes, esperado 1", bad.calls)
	}
}

// TestTeeSinkStopsOnFirstFailure — um sink que falha à cabeça impede os seguintes
// de serem tentados (fail-fast, fail-closed).
func TestTeeSinkStopsOnFirstFailure(t *testing.T) {
	ctx := context.Background()
	bad := &errSink{}
	after := &recordingSink{seq: 7}

	tee := NewTeeSink(bad, after)
	if _, err := tee.RecordMediation(ctx, referencemonitor.MediationRecord{RunID: "r"}); err == nil {
		t.Fatal("esperado erro")
	}
	if after.calls != 0 {
		t.Fatalf("sink posterior nao devia ser chamado apos falha: calls=%d", after.calls)
	}
}

// TestTeeSinkEmpty — zero sinks é um no-op que devolve seq 0 sem erro.
func TestTeeSinkEmpty(t *testing.T) {
	seq, err := NewTeeSink().RecordMediation(context.Background(), referencemonitor.MediationRecord{})
	if err != nil || seq != 0 {
		t.Fatalf("tee vazio: seq=%d err=%v, esperado 0/nil", seq, err)
	}
}

// TestTeeSinkViaMonitor — integração: o RM com um TeeSink audita na cadeia
// tamper-evident E num sink durável simultaneamente, e a cadeia verifica.
func TestTeeSinkViaMonitor(t *testing.T) {
	ctx := context.Background()
	durable := &recordingSink{seq: 100}
	auditStore := NewMemStore()
	chain := NewMediationSink(auditStore, withSinkClock(func() time.Time { return fixedTime }))
	tee := NewTeeSink(durable, chain)

	mon := referencemonitor.New(
		referencemonitor.WithHooks(policyHook{name: "policy", decide: referencemonitor.HookAllow, version: "1.0.0"}),
		referencemonitor.WithEventSink(tee),
	)
	_ = mon.Register("tool.http", func(context.Context, []byte) ([]byte, error) { return nil, nil })

	dec, err := mon.Mediate(ctx, referencemonitor.Call{
		RunID: "run-3", StepID: "s1", ToolID: "tool.http", Capability: "cap:x",
		Principal: referencemonitor.Principal{NHIID: "nhi:a"},
	})
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("esperado permit, veio %s", dec.Effect)
	}
	if durable.calls != 1 {
		t.Fatalf("sink duravel nao recebeu a mediacao: calls=%d", durable.calls)
	}
	if err := Verify(ctx, auditStore, "run-3", 1, 1); err != nil {
		t.Fatalf("cadeia tamper-evident devia verificar: %v", err)
	}
}
