package pipeline_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
)

// TestPipeline_DespachoTodosOsEstagios prova que uma chamada atravessa os cinco
// estágios pela ORDEM determinística fixa: auth → allowlist → routing →
// cache-layout → [invoke] → metering.
func TestPipeline_DespachoTodosOsEstagios(t *testing.T) {
	t.Parallel()
	p := pipeline.NewDefault()
	ex := &pipeline.Exchange{Op: pipeline.OpChat, RequestedModel: "m", RequestedRegion: "eu"}

	invoked := false
	err := p.Execute(context.Background(), ex, func(_ context.Context, ex *pipeline.Exchange) error {
		// A invocação corre APÓS os estágios pré (routing já resolveu o modelo).
		if ex.ResolvedModel != "m" {
			t.Errorf("routing nao resolveu antes do invoke: %q", ex.ResolvedModel)
		}
		invoked = true
		ex.Usage = port.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}
		return nil
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !invoked {
		t.Fatal("invoke nao correu")
	}
	// Ordem exacta do rasto de decisões.
	wantOrder := []string{"auth-principal", "allowlist-regional", "roteamento", "cache-layout", "metering"}
	if len(ex.Decisions) != len(wantOrder) {
		t.Fatalf("decisoes = %d, quer %d: %+v", len(ex.Decisions), len(wantOrder), ex.Decisions)
	}
	for i, want := range wantOrder {
		if ex.Decisions[i].Stage != want {
			t.Errorf("decisao[%d] = %q, quer %q", i, ex.Decisions[i].Stage, want)
		}
	}
	// Metering correu DEPOIS do invoke (usage já preenchido no registo).
	if ex.Usage.TotalTokens != 3 {
		t.Errorf("usage nao propagado ao metering: %+v", ex.Usage)
	}
}

// TestPipeline_FailClosed_AbortaAntesDoInvoke prova que um estágio que recusa
// (allowlist deny) aborta a chamada ANTES da invocação do provider — fail-closed.
func TestPipeline_FailClosed_AbortaAntesDoInvoke(t *testing.T) {
	t.Parallel()
	denyErr := errors.New("modelo fora de soberania")
	ex := &pipeline.Exchange{Op: pipeline.OpChat, RequestedModel: "m"}

	invoked := false
	meteringRan := false
	// Injectamos um metering que marca se correu (não devia).
	p := pipeline.New(pipeline.Stages{
		Allowlist: pipeline.DenyStage{StageName: "allowlist-regional", Err: denyErr},
		Metering: pipeline.StageFunc{StageName: "metering", Fn: func(_ context.Context, _ *pipeline.Exchange) error {
			meteringRan = true
			return nil
		}},
	})
	err := p.Execute(context.Background(), ex, func(_ context.Context, _ *pipeline.Exchange) error {
		invoked = true
		return nil
	})
	if err == nil {
		t.Fatal("Execute devia falhar fail-closed")
	}
	var se *pipeline.StageError
	if !errors.As(err, &se) || se.Stage != "allowlist-regional" {
		t.Fatalf("erro nao atribuivel ao estagio allowlist: %v", err)
	}
	if !errors.Is(err, denyErr) {
		t.Errorf("erro nao envolve a causa: %v", err)
	}
	if invoked {
		t.Error("provider foi invocado apesar do deny (nao fail-closed)")
	}
	if meteringRan {
		t.Error("metering correu apesar do deny")
	}
}

// TestPipeline_InvokeErroPulaMetering garante que, se a invocação falhar, o
// metering não corre (o erro do provider propaga-se).
func TestPipeline_InvokeErroPulaMetering(t *testing.T) {
	t.Parallel()
	provErr := errors.New("provider indisponivel")
	meteringRan := false
	p := pipeline.New(pipeline.Stages{
		Metering: pipeline.StageFunc{StageName: "metering", Fn: func(_ context.Context, _ *pipeline.Exchange) error {
			meteringRan = true
			return nil
		}},
	})
	err := p.Execute(context.Background(), &pipeline.Exchange{RequestedModel: "m"},
		func(_ context.Context, _ *pipeline.Exchange) error { return provErr })
	if !errors.Is(err, provErr) {
		t.Fatalf("erro = %v, quer provErr", err)
	}
	if meteringRan {
		t.Error("metering correu apesar de o invoke ter falhado")
	}
}

// TestPipeline_EstagiosNilUsamPassthrough confirma que campos nil da Stages são
// preenchidos com os pass-through de referência (nunca há estágio ausente).
func TestPipeline_EstagiosNilUsamPassthrough(t *testing.T) {
	t.Parallel()
	p := pipeline.New(pipeline.Stages{}) // tudo nil
	s := p.Stages()
	if s.Auth == nil || s.Allowlist == nil || s.Routing == nil || s.CacheLayout == nil || s.Metering == nil {
		t.Fatalf("estagios nil nao foram preenchidos: %+v", s)
	}
	if err := p.Execute(context.Background(), &pipeline.Exchange{RequestedModel: "m"}, nil); err != nil {
		t.Fatalf("Execute com invoke nil: %v", err)
	}
}

// TestPipeline_RoutingSwap prova o ponto de extensão de roteamento: um routing
// que resolve outro modelo produz ResolvedModel != RequestedModel (a base do
// evento de variância registado pelo GW).
func TestPipeline_RoutingSwap(t *testing.T) {
	t.Parallel()
	p := pipeline.New(pipeline.Stages{
		Routing: pipeline.StageFunc{StageName: "roteamento", Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			ex.ResolvedModel = "modelo-mais-barato"
			ex.ResolvedRegion = ex.RequestedRegion
			return nil
		}},
	})
	ex := &pipeline.Exchange{RequestedModel: "modelo-frontier", RequestedRegion: "eu"}
	if err := p.Execute(context.Background(), ex, func(_ context.Context, _ *pipeline.Exchange) error { return nil }); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.ResolvedModel == ex.RequestedModel {
		t.Errorf("routing nao aplicou swap: %q", ex.ResolvedModel)
	}
}
