package pipeline_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// TestChain_RunsInOrderOverSameExchange — a cadeia corre os estágios por ordem sobre
// o MESMO Exchange: o que um resolve é a entrada do seguinte (é o que torna o
// encadeamento failover → refino possível sem um segundo Exchange).
func TestChain_RunsInOrderOverSameExchange(t *testing.T) {
	var order []string
	mark := func(name, region string) pipeline.Stage {
		return pipeline.StageFunc{StageName: name, Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			order = append(order, name+":"+ex.ResolvedRegion)
			ex.ResolvedRegion = region
			return nil
		}}
	}
	ch := pipeline.Chain{StageName: "roteamento", Stages: []pipeline.Stage{
		mark("primeiro", "eu-west"), nil, mark("segundo", "eu-west"),
	}}
	ex := &pipeline.Exchange{RequestedRegion: "eu"}
	if err := ch.Process(context.Background(), ex); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(order) != 2 || order[0] != "primeiro:" || order[1] != "segundo:eu-west" {
		t.Fatalf("ordem/estado = %v; o segundo estágio tem de ver o que o primeiro resolveu", order)
	}
	if ch.Name() != "roteamento" {
		t.Fatalf("o nome é o do SLOT, não a concatenação: %q", ch.Name())
	}
}

// TestChain_StopsAtFirstRefusal — uma recusa aborta a cadeia (os seguintes não
// correm) e o erro propaga-se TAL QUAL, para que os sentinelas de cada estágio
// continuem comparáveis por errors.Is a jusante.
func TestChain_StopsAtFirstRefusal(t *testing.T) {
	sentinel := errors.New("recusa do primeiro estágio")
	ran := false
	ch := pipeline.Chain{StageName: "roteamento", Stages: []pipeline.Stage{
		pipeline.StageFunc{StageName: "nega", Fn: func(context.Context, *pipeline.Exchange) error { return sentinel }},
		pipeline.StageFunc{StageName: "nunca", Fn: func(context.Context, *pipeline.Exchange) error {
			ran = true
			return nil
		}},
	}}
	err := ch.Process(context.Background(), &pipeline.Exchange{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("o erro tem de propagar tal qual (errors.Is); got %v", err)
	}
	if ran {
		t.Fatal("um estágio a seguir a uma recusa NÃO pode correr (fail-closed)")
	}
}

// TestChain_EmptyIsNoop — uma cadeia sem estágios (ou só com nil) não é um panic nem
// uma recusa: é um slot não composto.
func TestChain_EmptyIsNoop(t *testing.T) {
	ch := pipeline.Chain{Stages: []pipeline.Stage{nil}}
	if err := ch.Process(context.Background(), &pipeline.Exchange{}); err != nil {
		t.Fatalf("cadeia vazia devia ser no-op: %v", err)
	}
	if ch.Name() != "chain" {
		t.Fatalf("nome por omissão = %q", ch.Name())
	}
}

// TestChain_FailClosedInsidePipeline — composta no SLOT de roteamento de uma
// Pipeline real, uma recusa da cadeia impede a invocação do provider e chega
// etiquetada com o nome do slot.
func TestChain_FailClosedInsidePipeline(t *testing.T) {
	sentinel := errors.New("sem rota admissível")
	p := pipeline.New(pipeline.Stages{Routing: pipeline.Chain{StageName: "roteamento", Stages: []pipeline.Stage{
		pipeline.StageFunc{StageName: "soberania", Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			ex.ResolvedRegion = "eu"
			return nil
		}},
		pipeline.StageFunc{StageName: "refino", Fn: func(context.Context, *pipeline.Exchange) error { return sentinel }},
	}}})
	invoked := false
	err := p.Execute(context.Background(), &pipeline.Exchange{}, func(context.Context, *pipeline.Exchange) error {
		invoked = true
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("erro = %v", err)
	}
	var se *pipeline.StageError
	if !errors.As(err, &se) || se.Stage != "roteamento" {
		t.Fatalf("a recusa tem de ser atribuída ao SLOT: %+v", se)
	}
	if invoked {
		t.Fatal("o provider NÃO pode ser invocado depois de uma recusa da cadeia")
	}
}
