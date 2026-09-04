package autonomy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestFailClosedUnregisteredPairIsL0 prova o fail-closed exigido: um par (agente,
// domínio) SEM nível registado resolve para [L0] (o mais restritivo).
func TestFailClosedUnregisteredPairIsL0(t *testing.T) {
	r := NewLevelRegistry()
	if got := r.LevelFor("agent-x", "http"); got != L0 {
		t.Errorf("par não registado = %s; quer L0 (fail-closed)", got)
	}
	if _, ok := r.Get("agent-x", "http"); ok {
		t.Error("Get de par não registado devia devolver ok=false")
	}
}

// TestGranularityPerDomain é o teste de GRANULARIDADE exigido: o MESMO agente a L4
// num domínio e L1 noutro é tratado DIFERENTEMENTE (AC2).
func TestGranularityPerDomain(t *testing.T) {
	r := NewLevelRegistry()
	ctx := context.Background()
	if _, err := r.SetLevel(ctx, "agent-1", "http", L4, "dominio baixo risco", "gov-admin"); err != nil {
		t.Fatalf("SetLevel http: %v", err)
	}
	if _, err := r.SetLevel(ctx, "agent-1", "mail", L1, "dominio sensivel", "gov-admin"); err != nil {
		t.Fatalf("SetLevel mail: %v", err)
	}
	if got := r.LevelFor("agent-1", "http"); got != L4 {
		t.Errorf("agent-1/http = %s; quer L4", got)
	}
	if got := r.LevelFor("agent-1", "mail"); got != L1 {
		t.Errorf("agent-1/mail = %s; quer L1", got)
	}
	// A MESMA tool call de risco fixo (safe) é tratada diferentemente por domínio:
	// L4/safe corre sem gate, L1/safe confirma individualmente.
	if got := Oversight(r.LevelFor("agent-1", "http"), risk.ClassSafe); got != OversightRun {
		t.Errorf("agent-1/http (L4) com safe = %s; quer run", got)
	}
	if got := Oversight(r.LevelFor("agent-1", "mail"), risk.ClassSafe); got != OversightConfirm {
		t.Errorf("agent-1/mail (L1) com safe = %s; quer confirm", got)
	}
}

// TestSetLevelInvalidRejected prova que um nível inválido é REJEITADO sem mutar o
// registo, e um par incompleto também.
func TestSetLevelInvalidRejected(t *testing.T) {
	r := NewLevelRegistry()
	ctx := context.Background()
	if _, err := r.SetLevel(ctx, "a", "d", Level(9), "x", "actor"); !errors.Is(err, ErrInvalidLevel) {
		t.Errorf("nível inválido: err = %v; quer ErrInvalidLevel", err)
	}
	if _, ok := r.Get("a", "d"); ok {
		t.Error("um SetLevel rejeitado não deve registar o par")
	}
	if _, err := r.SetLevel(ctx, "", "d", L2, "x", "actor"); !errors.Is(err, ErrEmptyPair) {
		t.Errorf("agente vazio: err = %v; quer ErrEmptyPair", err)
	}
	if _, err := r.SetLevel(ctx, "a", "", L2, "x", "actor"); !errors.Is(err, ErrEmptyPair) {
		t.Errorf("domínio vazio: err = %v; quer ErrEmptyPair", err)
	}
}

// TestSetLevelMissingReasonOrActorRejected prova (AC5) que uma alteração de nível
// SEM motivo ou SEM actor é REJEITADA antes de mutar o registo e antes de selar —
// a hash-chain nunca contém uma promoção anónima ou sem justificação.
func TestSetLevelMissingReasonOrActorRejected(t *testing.T) {
	store := audit.NewMemStore()
	r := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	ctx := context.Background()

	// Motivo vazio -> ErrMissingReason, sem mutação.
	if _, err := r.SetLevel(ctx, "rogue-agent", "fs", L5, "", "gov-admin"); !errors.Is(err, ErrMissingReason) {
		t.Errorf("motivo vazio: err = %v; quer ErrMissingReason", err)
	}
	// Actor vazio -> ErrMissingActor, sem mutação.
	if _, err := r.SetLevel(ctx, "rogue-agent", "fs", L5, "promocao", ""); !errors.Is(err, ErrMissingActor) {
		t.Errorf("actor vazio: err = %v; quer ErrMissingActor", err)
	}
	// Ambos vazios -> rejeitado (motivo validado primeiro).
	if _, err := r.SetLevel(ctx, "rogue-agent", "fs", L5, "", ""); !errors.Is(err, ErrMissingReason) {
		t.Errorf("ambos vazios: err = %v; quer ErrMissingReason", err)
	}
	// Nenhuma das rejeições mutou o registo...
	if _, ok := r.Get("rogue-agent", "fs"); ok {
		t.Error("um SetLevel rejeitado por motivo/actor não deve registar o par")
	}
	if got := r.LevelFor("rogue-agent", "fs"); got != L0 {
		t.Errorf("par continua fail-closed = %s; quer L0", got)
	}
	// ...nem selou no audit.
	if head, err := store.Head(ctx, DefaultAutonomyPartition); err != nil || head != 0 {
		t.Errorf("head = %d err=%v; quer 0 (nada selado)", head, err)
	}
}

// TestSetLevelHistory prova que cada alteração regista a transição old→new/motivo/
// actor no histórico, por ordem de aplicação.
func TestSetLevelHistory(t *testing.T) {
	at := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	r := NewLevelRegistry(WithClock(fixedClock(at)))
	ctx := context.Background()

	// Primeira alteração: old é L0 (fail-closed, par não registado).
	ch1, err := r.SetLevel(ctx, "a", "http", L3, "promocao inicial", "gov-admin")
	if err != nil {
		t.Fatal(err)
	}
	if ch1.Old != L0 || ch1.New != L3 {
		t.Errorf("ch1 old→new = %s→%s; quer L0→L3", ch1.Old, ch1.New)
	}
	// Segunda alteração: old passa a ser L3.
	ch2, err := r.SetLevel(ctx, "a", "http", L4, "fiabilidade medida", "gov-admin")
	if err != nil {
		t.Fatal(err)
	}
	if ch2.Old != L3 || ch2.New != L4 {
		t.Errorf("ch2 old→new = %s→%s; quer L3→L4", ch2.Old, ch2.New)
	}
	if ch2.Reason != "fiabilidade medida" || ch2.Actor != "gov-admin" || !ch2.At.Equal(at) {
		t.Errorf("ch2 metadados errados: %+v", ch2)
	}

	all := r.History()
	if len(all) != 2 {
		t.Fatalf("history len = %d; quer 2", len(all))
	}
	perPair := r.HistoryFor("a", "http")
	if len(perPair) != 2 {
		t.Errorf("HistoryFor len = %d; quer 2", len(perPair))
	}
	if len(r.HistoryFor("a", "mail")) != 0 {
		t.Error("HistoryFor de par sem alterações devia ser vazio")
	}
}

// TestSetLevelSealsAudit é o teste de AUDITORIA exigido: uma alteração de nível
// gera um evento autonomy.level_changed SELADO na hash-chain WORM com o motivo
// (old→new, actor, reason).
func TestSetLevelSealsAudit(t *testing.T) {
	store := audit.NewMemStore()
	at := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	r := NewLevelRegistry(WithSink(NewAuditSink(store, "")), WithClock(fixedClock(at)))
	ctx := context.Background()

	if _, err := r.SetLevel(ctx, "agent-7", "db", L2, "onboarding supervisionado", "compliance-officer"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}

	head, err := store.Head(ctx, DefaultAutonomyPartition)
	if err != nil {
		t.Fatal(err)
	}
	if head != 1 {
		t.Fatalf("head = %d; quer 1 (um evento selado)", head)
	}
	rec, ok, err := store.At(ctx, DefaultAutonomyPartition, 1)
	if err != nil || !ok {
		t.Fatalf("At(1): ok=%v err=%v", ok, err)
	}
	if rec.Resource.Type != LevelChangedEventType {
		t.Errorf("resource.type = %q; quer %q", rec.Resource.Type, LevelChangedEventType)
	}
	if rec.Resource.Value != "agent-7/db" {
		t.Errorf("resource.value = %q; quer agent-7/db", rec.Resource.Value)
	}
	if rec.Principal.NHIID != "compliance-officer" {
		t.Errorf("principal = %q; quer compliance-officer", rec.Principal.NHIID)
	}
	if len(rec.EntryHash) == 0 {
		t.Error("EntryHash vazio: o evento não foi selado na cadeia")
	}
	if len(rec.Obligations) != 1 {
		t.Fatalf("obligations = %d; quer 1", len(rec.Obligations))
	}
	p := rec.Obligations[0].Params
	if p["old_level"] != "L0" || p["new_level"] != "L2" {
		t.Errorf("old/new = %s/%s; quer L0/L2", p["old_level"], p["new_level"])
	}
	if p["reason"] != "onboarding supervisionado" || p["actor"] != "compliance-officer" {
		t.Errorf("motivo/actor errados: %+v", p)
	}
}

// failingSink devolve sempre erro (para provar que uma selagem falhada é surfaced).
type failingSink struct{}

func (failingSink) SealLevelChange(context.Context, LevelChange) error {
	return errors.New("store WORM indisponivel")
}

// TestSetLevelSealFailureSurfaced prova que uma selagem falhada NÃO é engolida — é
// devolvida, embrulhada em [ErrSealFailed] — e que a alteração NÃO é aplicada (AOS-306,
// audit-before-effect). Até AOS-306 este teste fixava a semântica inversa («erro sobe E
// a mudança fica aplicada», molde policy.changed); essa decisão caducou porque o único
// consumidor de produção respondia «recusado» com o nível já em vigor.
func TestSetLevelSealFailureSurfaced(t *testing.T) {
	r := NewLevelRegistry(WithSink(failingSink{}))
	ctx := context.Background()
	ch, err := r.SetLevel(ctx, "a", "http", L5, "x", "actor")
	if err == nil {
		t.Fatal("selagem falhada devia devolver erro")
	}
	if !errors.Is(err, ErrSealFailed) {
		t.Errorf("err = %v; quer errors.Is(ErrSealFailed)", err)
	}
	if !reflect.DeepEqual(ch, LevelChange{}) {
		t.Errorf("change devia ser vazia numa selagem falhada; obteve %+v", ch)
	}
	// A alteração NÃO ficou aplicada: o par continua fail-closed.
	if got := r.LevelFor("a", "http"); got != L0 {
		t.Errorf("nível após selagem falhada = %s; quer L0 (não aplicado)", got)
	}
	if len(r.History()) != 0 {
		t.Error("histórico não devia crescer numa selagem falhada")
	}
}

// TestNilAuditSinkNoop prova que um AuditSink sobre store nil é no-op seguro.
func TestNilAuditSinkNoop(t *testing.T) {
	s := NewAuditSink(nil, "")
	if err := s.SealLevelChange(context.Background(), LevelChange{}); err != nil {
		t.Errorf("sink sobre store nil devia ser no-op; obteve %v", err)
	}
	var nilSink *AuditSink
	if err := nilSink.SealLevelChange(context.Background(), LevelChange{}); err != nil {
		t.Errorf("sink nil devia ser no-op; obteve %v", err)
	}
}

// TestRegistryConcurrent exercita o acesso concorrente (corre com -race).
func TestRegistryConcurrent(t *testing.T) {
	r := NewLevelRegistry()
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = r.SetLevel(ctx, "a", "http", L3, "x", "actor")
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = r.LevelFor("a", "http")
	}
	<-done
	if r.LevelFor("a", "http") != L3 {
		t.Error("nível final inesperado após concorrência")
	}
}
