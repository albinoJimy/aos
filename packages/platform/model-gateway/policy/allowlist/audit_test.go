package allowlist_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/substrate/eventstore"
)

func denyRecord() allowlist.GovRecord {
	return allowlist.GovRecord{
		Board:          "board-eu",
		PrincipalUser:  "alice",
		PrincipalAgent: "agent-42",
		AgentClass:     "reader",
		HumanRoot:      "alice",
		Model:          "claude-3",
		Region:         "eu",
		Decision:       audit.DecisionDeny,
		Reason:         "modelo fora da allowlist",
		PolicyVersion:  "gw-allowlist/v1#abc123",
		Operation:      "chat",
		Timestamp:      time.Unix(1_700_000_000, 0),
	}
}

// TestSealDeny_AttributableToPrincipalAndBoard — um deny sela no WORM atribuível ao
// PRINCIPAL (agente) E ao BOARD (partição + obligation). Nunca anónimo.
func TestSealDeny_AttributableToPrincipalAndBoard(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)

	sealed, err := rec.Seal(context.Background(), denyRecord())
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.Decision != audit.DecisionDeny {
		t.Fatalf("Decision = %q; quero deny", sealed.Decision)
	}
	if sealed.Partition != "modelgw-gov:board-eu" {
		t.Fatalf("Partition = %q; quero modelgw-gov:board-eu", sealed.Partition)
	}
	if sealed.Principal.NHIID != "agent-42" {
		t.Fatalf("Principal.NHIID = %q; quero agent-42", sealed.Principal.NHIID)
	}
	if sealed.Resource.Value != "claude-3" || sealed.Resource.Region != "eu" {
		t.Fatalf("Resource = %+v; quero model=claude-3 region=eu", sealed.Resource)
	}
	// A obligation sela board + razão + utilizador na cadeia (tamper-evident).
	if len(sealed.Obligations) != 1 {
		t.Fatalf("obligations = %d; quero 1", len(sealed.Obligations))
	}
	ob := sealed.Obligations[0]
	if ob.Params["board"] != "board-eu" || ob.Params["principal_user"] != "alice" {
		t.Fatalf("obligation params = %+v; quero board+principal_user", ob.Params)
	}
	if ob.Params["reason"] == "" {
		t.Fatalf("razao do deny nao selada")
	}
	// Está realmente no WORM (append-only, verificável).
	head, _ := store.Head(context.Background(), "modelgw-gov:board-eu")
	if head != 1 {
		t.Fatalf("head da particao = %d; quero 1", head)
	}
}

// TestSeal_NoBoard_FailClosed — deny sem board é RECUSADO (soberania é por board).
func TestSeal_NoBoard_FailClosed(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	r := denyRecord()
	r.Board = ""
	if _, err := rec.Seal(context.Background(), r); !errors.Is(err, allowlist.ErrNoBoard) {
		t.Fatalf("deny sem board devia falhar ErrNoBoard; got %v", err)
	}
	// Nada foi selado.
	if h, _ := store.Head(context.Background(), "modelgw-gov:"); h != 0 {
		t.Fatalf("nada devia ter sido selado")
	}
}

// TestSeal_NoPrincipal_FailClosed — deny sem principal (user nem agent) é recusado.
func TestSeal_NoPrincipal_FailClosed(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	r := denyRecord()
	r.PrincipalUser, r.PrincipalAgent = "", ""
	if _, err := rec.Seal(context.Background(), r); !errors.Is(err, allowlist.ErrNoAttribution) {
		t.Fatalf("deny sem principal devia falhar ErrNoAttribution; got %v", err)
	}
}

// TestRecord_AnnotatesSpanAndSeals — Record anota o span e sela no WORM + sink.
func TestRecord_AnnotatesSpanAndSeals(t *testing.T) {
	store := audit.NewMemStore()
	var captured []allowlist.GovRecord
	rec := allowlist.NewRecorder(store, allowlist.WithSink(allowlist.SinkFunc(
		func(_ context.Context, r allowlist.GovRecord) { captured = append(captured, r) },
	)))
	tr := &agentruntime.RecordingTracer{}
	_, span := tr.StartSpan(context.Background(), "chat")

	if err := rec.Record(context.Background(), span, denyRecord()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	span.End()

	if len(captured) != 1 {
		t.Fatalf("sink recebeu %d; quero 1", len(captured))
	}
	spans := tr.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d; quero 1", len(spans))
	}
	a := spans[0].Attributes
	if a[allowlist.AttrAllowlistResult] != "deny" {
		t.Fatalf("span result = %v; quero deny", a[allowlist.AttrAllowlistResult])
	}
	if a[allowlist.AttrBoard] != "board-eu" {
		t.Fatalf("span board = %v; quero board-eu", a[allowlist.AttrBoard])
	}
	if a[allowlist.AttrModel] != "claude-3" {
		t.Fatalf("span model = %v; quero claude-3", a[allowlist.AttrModel])
	}
}

// TestRecord_NoStore_NoOp — sem store, Record anota/emite mas não sela (no-op WORM).
func TestRecord_NoStore_NoOp(t *testing.T) {
	rec := allowlist.NewRecorder(nil)
	if err := rec.Record(context.Background(), nil, denyRecord()); err != nil {
		t.Fatalf("Record sem store devia ser no-op; got %v", err)
	}
}

// TestSealChangelog — a activação de uma versão da allowlist sela no changelog WORM
// dedicado (ADR-011), com a versão tamper-evident.
func TestSealChangelog(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)

	sealed, err := rec.SealChangelog(context.Background(), "gw-allowlist/v1#deadbeef1234", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("SealChangelog: %v", err)
	}
	if sealed.PolicyVersion != "gw-allowlist/v1#deadbeef1234" {
		t.Fatalf("versao selada = %q", sealed.PolicyVersion)
	}
	if sealed.Partition != "modelgw-gov:allowlist-changelog" {
		t.Fatalf("particao do changelog = %q", sealed.Partition)
	}
	head, _ := store.Head(context.Background(), "modelgw-gov:allowlist-changelog")
	if head != 1 {
		t.Fatalf("changelog devia ter 1 registo; head=%d", head)
	}
	// Versão vazia é recusada.
	if _, err := rec.SealChangelog(context.Background(), "", time.Unix(1, 0)); err == nil {
		t.Fatal("versao vazia devia falhar")
	}
}

// TestSealChangelog_NoStore_NoOp — sem store, é no-op.
func TestSealChangelog_NoStore_NoOp(t *testing.T) {
	rec := allowlist.NewRecorder(nil)
	if _, err := rec.SealChangelog(context.Background(), "v1", time.Unix(1, 0)); err != nil {
		t.Fatalf("no-op esperado; got %v", err)
	}
}

// failStore devolve erro em Append (simula falha de selagem WORM).
type failStore struct{}

func (failStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("worm indisponivel")
}
func (failStore) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (failStore) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failStore) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}

// TestSeal_StoreError_Propagates — uma falha de selagem propaga-se (fail-closed a montante).
func TestSeal_StoreError_Propagates(t *testing.T) {
	rec := allowlist.NewRecorder(failStore{})
	if _, err := rec.Seal(context.Background(), denyRecord()); err == nil {
		t.Fatal("falha de selagem devia propagar erro")
	}
}

// AOS-519: o selo do carregamento de um perfil de rota NÃO é no-op sem store, e recusa um
// carregamento sem o que o identifica.
func TestSealRouteProfileLoaded_FailClosed(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0).UTC()
	if _, err := allowlist.NewRecorder(nil).SealRouteProfileLoaded(ctx, at, "gpt-4o", "openai/x", "sha256:p", "sha256:e", "sha256:r"); !errors.Is(err, allowlist.ErrRouteProfileSeal) {
		t.Fatalf("sem store o selo tinha de falhar; veio %v", err)
	}
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	for _, c := range [][4]string{{"", "p", "e", "r"}, {"a", "", "e", "r"}, {"a", "p", "", "r"}, {"a", "p", "e", ""}} {
		if _, err := rec.SealRouteProfileLoaded(ctx, at, c[0], "openai/x", c[1], c[2], c[3]); !errors.Is(err, allowlist.ErrRouteProfileSeal) {
			t.Fatalf("%v: tinha de falhar; veio %v", c, err)
		}
	}
	if head, _ := store.Head(ctx, "modelgw-gov:route-profiles"); head != 0 {
		t.Fatalf("selos recusados deixaram %d registos", head)
	}
	if _, err := rec.SealRouteProfileLoaded(ctx, at, "gpt-4o", "openai/x", "sha256:p", "sha256:e", "sha256:r"); err != nil {
		t.Fatal(err)
	}
	if head, _ := store.Head(ctx, "modelgw-gov:route-profiles"); head != 1 {
		t.Fatalf("selos = %d", head)
	}
	// A partição do selo é representável como nome de stream do Event Store (sem ponto, AOS-425):
	// um audit de governação sobre JetStream não a recusa.
	if err := eventstore.ValidarStreamID("modelgw-gov:route-profiles"); err != nil {
		t.Fatalf("a particao do selo nao cabe num nome de stream: %v", err)
	}
}

// AOS-520: o selo do fim do canary NÃO é no-op sem store, recusa um fim sem o que o identifica ou
// com contagens que não são as de um canary terminado, e fica na cadeia dos perfis de rota.
func TestSealCanaryClosed_FailClosed(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0).UTC()
	if _, err := allowlist.NewRecorder(nil).SealCanaryClosed(ctx, at, "gpt-4o-mini", "sha256:p", "sha256:e", 40, 40); !errors.Is(err, allowlist.ErrCanaryClosedSeal) {
		t.Fatalf("sem store o selo tinha de falhar; veio %v", err)
	}
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	for _, c := range []struct {
		requested, perfil, registo string
		atribuidos, limite         int
	}{{"", "p", "e", 40, 40}, {"a", "", "e", 40, 40}, {"a", "p", "", 40, 40}, {"a", "p", "e", 40, 0}, {"a", "p", "e", 39, 40}} {
		if _, err := rec.SealCanaryClosed(ctx, at, c.requested, c.perfil, c.registo, c.atribuidos, c.limite); !errors.Is(err, allowlist.ErrCanaryClosedSeal) {
			t.Fatalf("%+v: tinha de falhar; veio %v", c, err)
		}
	}
	if head, _ := store.Head(ctx, "modelgw-gov:route-profiles"); head != 0 {
		t.Fatalf("selos recusados deixaram %d registos", head)
	}
	ar, err := rec.SealCanaryClosed(ctx, at, "gpt-4o-mini", "sha256:p", "sha256:e", 40, 40)
	if err != nil {
		t.Fatal(err)
	}
	params := ar.Obligations[0].Params
	if ar.Capability != "model-canary:close" || params["model_canary"] != "closed" || params["plans_assigned"] != "40" || params["plan_limit"] != "40" ||
		params["requested"] != "gpt-4o-mini" || params["profile_digest"] != "sha256:p" || params["entry_digest"] != "sha256:e" {
		t.Fatalf("selo do fim do canary = %+v", ar)
	}
	if head, _ := store.Head(ctx, "modelgw-gov:route-profiles"); head != 1 {
		t.Fatalf("selos = %d", head)
	}
}
