package failover_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
)

// loadPolicy carrega a allowlist embebida assinada (board-eu: gpt-4o em {eu, eu-west}).
func loadPolicy(t *testing.T) *allowlist.Policy {
	t.Helper()
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	return pol
}

// euExchange constrói um Exchange de uma chamada board-eu/gpt-4o com o principal já
// resolvido (como o faria o estágio de authn a montante).
func euExchange(region string) *pipeline.Exchange {
	ex := &pipeline.Exchange{
		Op:              pipeline.OpChat,
		Board:           "board-eu",
		Principal:       "alice/agent-42",
		PrincipalUser:   "alice",
		PrincipalAgent:  "agent-42",
		HumanRoot:       "alice",
		RequestedModel:  "gpt-4o",
		RequestedRegion: region,
	}
	pipeline.WithClock(ex, func() time.Time { return time.Unix(1_700_000_000, 0) })
	return ex
}

var allHealthy = func(sovereignty.Endpoint) bool { return true }

// TestPrimary_HealthyIntraBorder — o primário saudável e na fronteira é usado (sem
// failover): a região resolve para a pedida.
func TestPrimary_HealthyIntraBorder(t *testing.T) {
	inv := failover.StaticInventory{"openai": {
		{KeyID: "acct-eu-1", Region: "eu"},
		{KeyID: "acct-euw-1", Region: "eu-west"},
	}}
	st := failover.NewStage(loadPolicy(t), inv, failover.WithHealth(allHealthy))

	ex := euExchange("eu")
	ex.ResolvedProvider = "openai"
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("primário saudável devia passar; got %v", err)
	}
	if ex.ResolvedRegion != "eu" {
		t.Fatalf("ResolvedRegion = %q; quero eu", ex.ResolvedRegion)
	}
	if ex.ResolvedModel != "gpt-4o" {
		t.Fatalf("ResolvedModel = %q; quero gpt-4o", ex.ResolvedModel)
	}
}

// TestFailover_IntraBorder — primário indisponível; failover para uma região
// allowlisted intra-fronteira (eu-west), NUNCA cross-border.
func TestFailover_IntraBorder(t *testing.T) {
	inv := failover.StaticInventory{"openai": {
		{KeyID: "acct-euw-1", Region: "eu-west"}, // única capacidade, intra-fronteira
		{KeyID: "acct-us-1", Region: "us-east"},  // cross-border (será descartada)
	}}
	// Sem endpoint em "eu" → primário indisponível → avalia failover.
	st := failover.NewStage(loadPolicy(t), inv, failover.WithHealth(allHealthy))

	ex := euExchange("eu")
	ex.ResolvedProvider = "openai"
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("failover intra-fronteira devia passar; got %v", err)
	}
	if ex.ResolvedRegion != "eu-west" {
		t.Fatalf("ResolvedRegion = %q; quero eu-west (nunca us-east)", ex.ResolvedRegion)
	}
}

// TestCrossBorder_BlockedAndSealed — o TESTE-CHAVE: só há capacidade cross-border
// (us-east) para um board-eu; o router BLOQUEIA fail-closed e sela um deny atribuível
// a principal + board no WORM. Nunca resolve para us-east.
func TestCrossBorder_BlockedAndSealed(t *testing.T) {
	store := audit.NewMemStore()
	rec := allowlist.NewRecorder(store)
	inv := failover.StaticInventory{"openai": {
		{KeyID: "acct-us-1", Region: "us-east"}, // só capacidade fora da fronteira
	}}
	st := failover.NewStage(loadPolicy(t), inv, failover.WithHealth(allHealthy), failover.WithRecorder(rec))

	ex := euExchange("eu")
	ex.ResolvedProvider = "openai"
	err := st.Process(context.Background(), ex)
	if !errors.Is(err, failover.ErrCrossBorderBlocked) {
		t.Fatalf("failover cross-border devia bloquear fail-closed; got %v", err)
	}
	if ex.ResolvedRegion == "us-east" {
		t.Fatalf("NUNCA devia resolver para a jurisdição cross-border; got %q", ex.ResolvedRegion)
	}
	// Deny selado no WORM, atribuível a principal (agente) E board.
	at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1)
	if !ok || at.Decision != audit.DecisionDeny {
		t.Fatalf("deny cross-border devia estar selado; ok=%v dec=%v", ok, at.Decision)
	}
	if at.Principal.NHIID != "agent-42" || at.Obligations[0].Params["board"] != "board-eu" {
		t.Fatalf("deny devia ser atribuível a principal+board; got principal=%q board=%q", at.Principal.NHIID, at.Obligations[0].Params["board"])
	}
}

// TestCrossBorder_BlockedEvenWithoutRecorder — mesmo sem recorder, o cross-border
// FALHA-FECHA (a selagem é no-op, mas o veredicto é sempre deny).
func TestCrossBorder_BlockedEvenWithoutRecorder(t *testing.T) {
	inv := failover.StaticInventory{"openai": {{KeyID: "acct-us-1", Region: "us-east"}}}
	st := failover.NewStage(loadPolicy(t), inv, failover.WithHealth(allHealthy))

	ex := euExchange("eu")
	ex.ResolvedProvider = "openai"
	if err := st.Process(context.Background(), ex); !errors.Is(err, failover.ErrCrossBorderBlocked) {
		t.Fatalf("sem recorder o cross-border ainda deve falhar-fechar; got %v", err)
	}
}

// TestNoCapacity_Backpressure — sem qualquer capacidade (inventário vazio para o
// provider) → backpressure fail-closed (não é o caso cross-border).
func TestNoCapacity_Backpressure(t *testing.T) {
	inv := failover.StaticInventory{"openai": nil}
	st := failover.NewStage(loadPolicy(t), inv, failover.WithHealth(allHealthy))

	ex := euExchange("eu")
	ex.ResolvedProvider = "openai"
	if err := st.Process(context.Background(), ex); !errors.Is(err, failover.ErrNoIntraCapacity) {
		t.Fatalf("sem capacidade devia dar backpressure fail-closed; got %v", err)
	}
}

// TestUnconfigured_FailClosed — policy ou inventário nil → fail-closed (nunca roteia
// às cegas).
func TestUnconfigured_FailClosed(t *testing.T) {
	nilPolicy := failover.NewStage(nil, failover.StaticInventory{})
	if err := nilPolicy.Process(context.Background(), euExchange("eu")); !errors.Is(err, failover.ErrRouterUnconfigured) {
		t.Fatalf("policy nil devia ser fail-closed; got %v", err)
	}
	nilInv := failover.NewStage(loadPolicy(t), nil)
	if err := nilInv.Process(context.Background(), euExchange("eu")); !errors.Is(err, failover.ErrRouterUnconfigured) {
		t.Fatalf("inventário nil devia ser fail-closed; got %v", err)
	}
}

// TestName_PreservesRoutingSlot — o estágio mantém o nome canónico do slot para que a
// variância/rasto continuem a indexá-lo como "roteamento".
func TestName_PreservesRoutingSlot(t *testing.T) {
	st := failover.NewStage(loadPolicy(t), failover.StaticInventory{})
	if st.Name() != "roteamento" {
		t.Fatalf("Name = %q; quero roteamento", st.Name())
	}
}
