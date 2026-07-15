package allowlist_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// signedCustom carrega a policy de teste assinada (helper local).
func signedCustom(t *testing.T) *allowlist.Policy {
	t.Helper()
	priv, pub := testKeys()
	p, err := allowlist.LoadSignedPolicy([]byte(customPolicy), sign(t, priv, []byte(customPolicy)), pub)
	if err != nil {
		t.Fatalf("LoadSignedPolicy: %v", err)
	}
	return p
}

// newExchange constrói um Exchange com identidade resolvida (como após o authn) e
// relógio determinista.
func newExchange(op pipeline.Op, board, model, region string) *pipeline.Exchange {
	ex := &pipeline.Exchange{
		Op:              op,
		Board:           board,
		Principal:       "alice/agent-42",
		RequestedModel:  model,
		RequestedRegion: region,
		PrincipalUser:   "alice",
		PrincipalAgent:  "agent-42",
		AgentClass:      "reader",
		HumanRoot:       "alice",
	}
	pipeline.WithClock(ex, func() time.Time { return time.Unix(1_700_000_000, 0) })
	return ex
}

// TestStage_Allow — modelo permitido atravessa e sela a decisão de allow (rota).
func TestStage_Allow(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	st := allowlist.NewStage(signedCustom(t), allowlist.WithRecorder(rec))

	ex := newExchange(pipeline.OpChat, "board-eu", "gpt-4o", "eu")
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("allow devia passar; got %v", err)
	}
	// Decisão registada no rasto.
	last := ex.Decisions[len(ex.Decisions)-1]
	if last.Stage != "allowlist-regional" || last.Result != "allow" {
		t.Fatalf("rasto = %+v; quero allow", last)
	}
	// Allow selado no WORM (registo por chamada), atribuível a board+principal.
	at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1)
	if !ok || at.Decision != audit.DecisionAllow {
		t.Fatalf("allow devia estar selado no WORM; ok=%v dec=%v", ok, at.Decision)
	}
	if at.Resource.Value != "gpt-4o" || at.Resource.Region != "eu" {
		t.Fatalf("rota selada = %+v; quero gpt-4o/eu", at.Resource)
	}
}

// TestStage_Deny_FailClosed — modelo fora da allowlist é DENY fail-closed com seal WORM.
func TestStage_Deny_FailClosed(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	st := allowlist.NewStage(signedCustom(t), allowlist.WithRecorder(rec))

	ex := newExchange(pipeline.OpChat, "board-eu", "claude-3", "eu")
	err := st.Process(context.Background(), ex)
	if !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("modelo fora da allowlist devia falhar ErrModelNotAllowed; got %v", err)
	}
	// Deny selado no WORM, atribuível a principal + board.
	at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1)
	if !ok || at.Decision != audit.DecisionDeny {
		t.Fatalf("deny devia estar selado; ok=%v dec=%v", ok, at.Decision)
	}
	if at.Principal.NHIID != "agent-42" {
		t.Fatalf("deny.Principal = %q; quero agent-42", at.Principal.NHIID)
	}
	if at.Obligations[0].Params["board"] != "board-eu" {
		t.Fatalf("deny devia selar o board")
	}
}

// TestStage_DenyRegionOutsideBoundary — região fora da fronteira do board é deny.
func TestStage_DenyRegionOutsideBoundary(t *testing.T) {
	st := allowlist.NewStage(signedCustom(t))
	ex := newExchange(pipeline.OpChat, "board-eu", "gpt-4o", "us-east")
	if err := st.Process(context.Background(), ex); !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("regiao fora da fronteira devia falhar; got %v", err)
	}
}

// TestStage_Unconfigured_FailClosed — estágio sem policy recusa toda a chamada.
func TestStage_Unconfigured_FailClosed(t *testing.T) {
	st := allowlist.NewStage(nil)
	ex := newExchange(pipeline.OpChat, "board-eu", "gpt-4o", "eu")
	if err := st.Process(context.Background(), ex); !errors.Is(err, allowlist.ErrStageUnconfigured) {
		t.Fatalf("estágio sem policy devia falhar fail-closed; got %v", err)
	}
}

// TestStage_Name — preserva o slot canónico da pipeline.
func TestStage_Name(t *testing.T) {
	if n := allowlist.NewStage(signedCustom(t)).Name(); n != "allowlist-regional" {
		t.Fatalf("Name = %q; quero allowlist-regional", n)
	}
}

// TestStage_DenySealFailure_StillDenies — se a selagem do deny falhar, a chamada
// continua a ser NEGADA (fail-closed duplo), e o erro carrega ErrModelNotAllowed.
func TestStage_DenySealFailure_StillDenies(t *testing.T) {
	st := allowlist.NewStage(signedCustom(t), allowlist.WithRecorder(allowlist.NewRecorder(failStore{})))
	ex := newExchange(pipeline.OpChat, "board-eu", "claude-3", "eu")
	err := st.Process(context.Background(), ex)
	if !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("mesmo com falha de selagem, o veredicto deve ser deny; got %v", err)
	}
}

// TestStage_AllowSealFailure_FailClosed — se a selagem do ALLOW falhar, a chamada
// FALHA-FECHA (uma decisão de soberania não-auditável aborta antes do efeito).
func TestStage_AllowSealFailure_FailClosed(t *testing.T) {
	st := allowlist.NewStage(signedCustom(t), allowlist.WithRecorder(allowlist.NewRecorder(failStore{})))
	ex := newExchange(pipeline.OpChat, "board-eu", "gpt-4o", "eu")
	if err := st.Process(context.Background(), ex); err == nil {
		t.Fatal("falha de selagem do allow devia abortar fail-closed")
	}
}

// TestStage_NoRecorder_AllowsWithoutSeal — sem recorder, o estágio decide na mesma
// (só não sela); o rasto de decisão fica registado.
func TestStage_NoRecorder_AllowsWithoutSeal(t *testing.T) {
	st := allowlist.NewStage(signedCustom(t))
	ex := newExchange(pipeline.OpChat, "board-eu", "gpt-4o", "eu")
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("allow sem recorder devia passar; got %v", err)
	}
}

// TestLoadAndActivate_SealsChangelog (AOS058-Q3) — a activação da allowlist embebida
// SELA a versão no changelog WORM ANTES de servir (audit-before-effect) e devolve o
// estágio REAL fail-closed. Prova o wiring end-to-end da activação, não só o mecanismo.
func TestLoadAndActivate_SealsChangelog(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)

	stage, pol, err := allowlist.LoadAndActivate(context.Background(), rec, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("LoadAndActivate: %v", err)
	}
	if stage == nil || pol == nil {
		t.Fatal("LoadAndActivate devia devolver estágio e policy")
	}
	// A activação selou a versão em vigor no changelog dedicado (ADR-011).
	head, _ := store.Head(context.Background(), "modelgw-gov:allowlist-changelog")
	if head != 1 {
		t.Fatalf("activação devia selar 1 registo no changelog; head=%d", head)
	}
	at, ok, _ := store.At(context.Background(), "modelgw-gov:allowlist-changelog", 1)
	if !ok || at.PolicyVersion != pol.Version() {
		t.Fatalf("changelog devia selar a versão em vigor %q; got ok=%v ver=%q", pol.Version(), ok, at.PolicyVersion)
	}
	// O estágio devolvido é o REAL fail-closed: um modelo fora da allowlist é negado.
	ex := newExchange(pipeline.OpChat, "board-eu", "modelo-inexistente", "eu")
	if err := stage.Process(context.Background(), ex); !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("estágio activado devia ser default-deny fail-closed; got %v", err)
	}
}

// TestLoadAndActivate_ChangelogSealFailure_FailClosed (AOS058-Q3) — se a selagem do
// changelog falhar, a activação FALHA-FECHA e NÃO devolve estágio (o gateway não
// arranca sobre uma activação não-auditada).
func TestLoadAndActivate_ChangelogSealFailure_FailClosed(t *testing.T) {
	rec := allowlist.NewRecorder(failStore{})
	stage, _, err := allowlist.LoadAndActivate(context.Background(), rec, time.Unix(1, 0))
	if err == nil {
		t.Fatal("falha de selagem do changelog devia abortar a activação fail-closed")
	}
	if stage != nil {
		t.Fatal("activação falhada NÃO devia devolver estágio")
	}
}
