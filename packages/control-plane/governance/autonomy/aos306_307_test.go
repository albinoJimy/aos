package autonomy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

// AOS-306 (selar antes de aplicar) e AOS-307 (rehidratação a partir do WORM).

// errSinkDown é a causa raiz devolvida pelo sink a falhar — tem de continuar visível
// por errors.Is através do embrulho em ErrSealFailed.
var errSinkDown = errors.New("WORM em baixo")

type downSink struct{}

func (downSink) SealLevelChange(context.Context, LevelChange) error { return errSinkDown }

// TestAOS306SealFailureDoesNotApply — AC2(a): sink a falhar ⇒ erro reconhecível, nível
// anterior intacto, histórico sem crescer.
func TestAOS306SealFailureDoesNotApply(t *testing.T) {
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// Registo com um nível prévio aplicado (sink ainda a funcionar) para provar que é o
	// nível ANTERIOR que sobrevive, não só o L0 de omissão.
	store := audit.NewMemStore()
	r := NewLevelRegistry(WithSink(NewAuditSink(store, "")), WithClock(fixedClock(at)))
	if _, err := r.SetLevel(ctx, "a", "http", L2, "base", "gov-admin"); err != nil {
		t.Fatal(err)
	}
	before := r.History()

	// Troca o sink por um que falha (mesmo registo, mesma memória).
	r.sink = downSink{}
	ch, err := r.SetLevel(ctx, "a", "http", L5, "promocao", "gov-admin")
	if err == nil {
		t.Fatal("selagem falhada devia devolver erro")
	}
	if !errors.Is(err, ErrSealFailed) {
		t.Errorf("errors.Is(err, ErrSealFailed) falso; err = %v", err)
	}
	if !errors.Is(err, errSinkDown) {
		t.Errorf("a causa do sink ficou escondida; err = %v", err)
	}
	if !reflect.DeepEqual(ch, LevelChange{}) {
		t.Errorf("change devia ser vazia; obteve %+v", ch)
	}
	if got := r.LevelFor("a", "http"); got != L2 {
		t.Errorf("LevelFor = %s; quer L2 (nível anterior)", got)
	}
	if after := r.History(); len(after) != len(before) {
		t.Errorf("history cresceu de %d para %d numa selagem falhada", len(before), len(after))
	}
	if head, _ := store.Head(ctx, DefaultAutonomyPartition); head != 1 {
		t.Errorf("WORM head = %d; quer 1 (só a primeira alteração selada)", head)
	}
}

// TestAOS306NilSinkApplies — sem sink, aplica sem selar (testes/dev).
func TestAOS306NilSinkApplies(t *testing.T) {
	r := NewLevelRegistry()
	ch, err := r.SetLevel(context.Background(), "a", "http", L3, "x", "actor")
	if err != nil {
		t.Fatal(err)
	}
	if ch.New != L3 || r.LevelFor("a", "http") != L3 || len(r.History()) != 1 {
		t.Errorf("sink nil devia aplicar: ch=%+v level=%s hist=%d", ch, r.LevelFor("a", "http"), len(r.History()))
	}
}

// blockingSink fica preso até o teste o libertar — simula um WORM lento/indisponível.
type blockingSink struct {
	entered chan struct{} // fechado quando a selagem começa
	release chan struct{} // o teste fecha para deixar a selagem terminar
}

func (s *blockingSink) SealLevelChange(context.Context, LevelChange) error {
	close(s.entered)
	<-s.release
	return nil
}

// TestAOS306ReadsNeverWaitForSeal — AC2(a), concorrência: enquanto uma selagem está
// presa, LevelFor/Get/History respondem (leituras nunca bloqueiam em I/O).
func TestAOS306ReadsNeverWaitForSeal(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	r := NewLevelRegistry(WithSink(sink))
	ctx := context.Background()

	setDone := make(chan error, 1)
	go func() {
		_, err := r.SetLevel(ctx, "a", "http", L4, "x", "actor")
		setDone <- err
	}()
	<-sink.entered // a selagem está presa AGORA

	readDone := make(chan Level, 1)
	go func() {
		_, _ = r.Get("a", "http")
		_ = r.History()
		readDone <- r.LevelFor("a", "http")
	}()
	select {
	case got := <-readDone:
		if got != L0 {
			t.Errorf("LevelFor durante selagem presa = %s; quer L0 (ainda não aplicado)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LevelFor ficou preso atrás da selagem — as leituras não podem esperar pelo WORM")
	}

	close(sink.release)
	if err := <-setDone; err != nil {
		t.Fatal(err)
	}
	if got := r.LevelFor("a", "http"); got != L4 {
		t.Errorf("após selagem = %s; quer L4", got)
	}
}

// TestAOS307RehydrateRoundTrip — AC1: N alterações seladas por um registo A num
// MemStore; um registo B novo rehidrata do mesmo store e fica IGUAL a A em
// LevelFor/History/LastChange.
func TestAOS307RehydrateRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	tick := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { tick = tick.Add(time.Minute); return tick }

	a := NewLevelRegistry(WithSink(NewAuditSink(store, "")), WithClock(clock))
	steps := []struct {
		agent, domain string
		lvl           Level
		reason, actor string
	}{
		{"agent-1", "http", L3, "arranque", "config:node"},
		{"agent-1", "mail", L1, "sensivel", "config:node"},
		{"agent-1", "http", L5, "promocao por API", "gov-admin"},
		{"class:worker", "fs", L2, "classe", "config:node"},
	}
	for _, s := range steps {
		if _, err := a.SetLevel(ctx, s.agent, s.domain, s.lvl, s.reason, s.actor); err != nil {
			t.Fatal(err)
		}
	}

	b := NewLevelRegistry()
	rep, err := b.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if rep.Records != len(steps) || rep.Applied != len(steps) {
		t.Errorf("report = %+v; quer Records=Applied=%d", rep, len(steps))
	}
	if rep.LastActorByPair[Pair{"agent-1", "http"}] != "gov-admin" {
		t.Errorf("LastActorByPair[agent-1/http] = %q; quer gov-admin", rep.LastActorByPair[Pair{"agent-1", "http"}])
	}

	ha, hb := a.History(), b.History()
	if len(ha) != len(hb) {
		t.Fatalf("history len A=%d B=%d", len(ha), len(hb))
	}
	for i := range ha {
		if !reflect.DeepEqual(ha[i], hb[i]) {
			t.Errorf("history[%d] diverge:\n A=%+v\n B=%+v", i, ha[i], hb[i])
		}
	}
	for _, s := range steps {
		if la, lb := a.LevelFor(s.agent, s.domain), b.LevelFor(s.agent, s.domain); la != lb {
			t.Errorf("LevelFor(%s/%s) A=%s B=%s", s.agent, s.domain, la, lb)
		}
		ca, oka := a.LastChange(s.agent, s.domain)
		cb, okb := b.LastChange(s.agent, s.domain)
		if !oka || !okb || !reflect.DeepEqual(ca, cb) {
			t.Errorf("LastChange(%s/%s) A=%+v,%v B=%+v,%v", s.agent, s.domain, ca, oka, cb, okb)
		}
	}
	if _, ok := b.LastChange("ninguem", "nada"); ok {
		t.Error("LastChange de par sem histórico devia devolver false")
	}
	// Rehidratar NÃO volta a selar.
	if head, _ := store.Head(ctx, DefaultAutonomyPartition); head != uint64(len(steps)) {
		t.Errorf("WORM head = %d após Rehydrate; quer %d (sem selos novos)", head, len(steps))
	}
}

// TestAOS307RehydrateEmptyPartitionIsFirstBoot — partição inexistente ⇒ Applied=0, nil.
func TestAOS307RehydrateEmptyPartitionIsFirstBoot(t *testing.T) {
	r := NewLevelRegistry()
	rep, err := r.Rehydrate(context.Background(), audit.NewMemStore(), "autonomy")
	if err != nil || rep.Applied != 0 || rep.Records != 0 {
		t.Errorf("rep=%+v err=%v; quer Applied=0, nil", rep, err)
	}
}

// readErrStore devolve erro em Read (WORM ilegível).
type readErrStore struct{ audit.Store }

var errReadDown = errors.New("ficheiro WORM ilegivel")

func (readErrStore) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, errReadDown
}

// TestAOS307RehydrateReadErrorFailsClosed — AC4: erro de Read ⇒ Rehydrate devolve erro.
func TestAOS307RehydrateReadErrorFailsClosed(t *testing.T) {
	r := NewLevelRegistry()
	_, err := r.Rehydrate(context.Background(), readErrStore{audit.NewMemStore()}, "")
	if !errors.Is(err, errReadDown) {
		t.Errorf("err = %v; quer errors.Is(errReadDown)", err)
	}
}

// TestAOS307RehydrateMalformedRecordNamesSeq — AC4, na forma FAIL-CLOSED NO NÍVEL: um
// registo com tipo certo mas new_level inválido é SALTADO e declarado com a seq e o motivo;
// os registos válidos à volta CONTINUAM a ser aplicados. Abortar (a primeira versão) dava um
// modo de tijolo a quem escreve no ficheiro — ver o bloco em [LevelRegistry.Rehydrate].
func TestAOS307RehydrateMalformedRecordNamesSeq(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	at := time.Date(2026, 9, 3, 13, 0, 0, 0, time.UTC)

	// seq=1: válido.
	good := BuildLevelChangedRecord(LevelChange{Agent: "a", Domain: "http", Old: L0, New: L3, Reason: "x", Actor: "y", At: at}, "")
	if _, err := store.Append(ctx, good); err != nil {
		t.Fatal(err)
	}
	// seq=2: new_level inválido.
	bad := BuildLevelChangedRecord(LevelChange{Agent: "a", Domain: "http", Old: L3, New: L5, Reason: "x", Actor: "y", At: at}, "")
	bad.Obligations[0].Params["new_level"] = "L9"
	if _, err := store.Append(ctx, bad); err != nil {
		t.Fatal(err)
	}

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("um registo malformado e SALTADO, nao aborta: %v", err)
	}
	if len(rep.Rejeitados) != 1 || rep.Rejeitados[0].AuditSeq != 2 || !strings.Contains(rep.Rejeitados[0].Motivo, "new_level") {
		t.Fatalf("o salto tem de nomear a seq=2 e o motivo: %+v", rep.Rejeitados)
	}
	if rep.Rejeitados[0].LevelRaw != "L9" {
		t.Errorf("o literal ilegivel tem de ser preservado para o banner: %q", rep.Rejeitados[0].LevelRaw)
	}
	// O registo VÁLIDO (seq=1) continua a ser aplicado: saltar o mau não descarta o bom.
	if got := r.LevelFor("a", "http"); got != L3 || rep.Applied != 1 {
		t.Errorf("o registo valido devia ter sido aplicado: level=%s applied=%d", got, rep.Applied)
	}

	// Param em falta também é salto nomeado.
	store2 := audit.NewMemStore()
	missing := BuildLevelChangedRecord(LevelChange{Agent: "a", Domain: "http", New: L3, Reason: "x", Actor: "y", At: at}, "")
	delete(missing.Obligations[0].Params, "actor")
	if _, err := store2.Append(ctx, missing); err != nil {
		t.Fatal(err)
	}
	rep2, err := NewLevelRegistry().Rehydrate(ctx, store2, "")
	if err != nil || len(rep2.Rejeitados) != 1 || rep2.Rejeitados[0].AuditSeq != 1 || !strings.Contains(rep2.Rejeitados[0].Motivo, "actor") {
		t.Errorf("param em falta: err=%v rejeitados=%+v; quer salto com seq=1 e 'actor'", err, rep2.Rejeitados)
	}
}
