package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos481_fiabilidade_real_test.go — AOS-481: a fonte de fiabilidade da promoção de autonomia
// (AOS-090, ADR-025) recebe desfechos REAIS — gravados pelo Reference Monitor nos sinks do Event
// Store e lidos pela subscrição —, e não eventos sintetizados à mão como em
// aos090_promocao_test.go. Antes do AOS-481 o `tool.call.outcome` era engolido pelo selo do mesmo
// passo e a janela nunca ficava julgável: com o prefixo do desfecho vazio este teste avermelha.

// aos481HookAprovacao escala a call sem evidência de aprovação e permite a que a traz.
type aos481HookAprovacao struct{ escalar map[string]bool }

func (aos481HookAprovacao) Name() string { return "policy" }

func (h aos481HookAprovacao) Evaluate(_ context.Context, c *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if h.escalar[c.StepID] && len(c.ApprovalEvidence) == 0 {
		return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "aos481"}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

func TestAOS481_FiabilidadeRecebeDesfechosReaisPeloRMEEventStore(t *testing.T) {
	const (
		classe  = "worker"
		permits = 60
		erros   = 1
		escalam = 2
		run     = "run-aos481"
	)
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	janela := time.Hour
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	agora := base
	agg := novaFiabilidade(es, func() time.Time { return agora }, janela, time.Hour)
	// A observação já cobre a janela (o WindowOK honesto exige-o): só falta o que chega pelo stream.
	agora = base.Add(janela + time.Minute)

	// A MESMA subscrição que [fiabilidadeAgregada.correr] abre, com o mesmo handler.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := es.Subscribe(ctx, eventstore.Filter{Types: []string{
		referencemonitor.EventTypeOutcome, referencemonitor.EventTypeMediated,
		referencemonitor.EventTypeEscalated, referencemonitor.EventTypeDenied,
	}}, agg.observar)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()

	// O RM com os dois canais no Event Store, como o nó os compõe (integration/secured.go).
	escalar := map[string]bool{}
	for i := 0; i < escalam; i++ {
		escalar[fmt.Sprintf("step-%06d-tool-1", i)] = true
	}
	m := referencemonitor.New(
		referencemonitor.WithHooks(aos481HookAprovacao{escalar: escalar}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
		referencemonitor.WithOutcomeSink(referencemonitor.NewEventStoreOutcomeSink(es)),
	)
	if err := m.Register("fs.write", func(_ context.Context, in []byte) ([]byte, error) {
		if string(in) == "rebenta" {
			return nil, errors.New("falha de execução")
		}
		return []byte("ok"), nil
	}); err != nil {
		t.Fatal(err)
	}
	call := func(i int) referencemonitor.Call {
		in := []byte("ok")
		if i < erros {
			in = []byte("rebenta")
		}
		return referencemonitor.Call{
			RunID: run, StepID: fmt.Sprintf("step-%06d-tool-1", i), ParentStepID: fmt.Sprintf("step-%06d", i),
			ToolID: "fs.write", Capability: "cap:fs.write",
			Resource:  referencemonitor.Resource{Type: "path", Value: "/x"},
			Principal: referencemonitor.Principal{NHIID: "nhi-1", AgentID: "agt-1", AgentClass: classe},
			Input:     in,
		}
	}
	for i := 0; i < permits; i++ {
		c := call(i)
		if escalar[c.StepID] {
			if d, err := m.Mediate(ctx, c); err != nil || d.Effect != referencemonitor.EffectEscalate {
				t.Fatalf("escalada esperada no passo %s: eff=%q err=%v", c.StepID, d.Effect, err)
			}
			c.ApprovalEvidence = []byte("grant")
		}
		if d, err := m.Mediate(ctx, c); err != nil || d.Effect != referencemonitor.EffectPermit {
			t.Fatalf("permit esperado no passo %s: eff=%q err=%v", c.StepID, d.Effect, err)
		}
	}

	// A subscrição entrega de forma assíncrona: espera-se pelo que o STREAM tem, com prazo.
	chave := classeDominio{classe: classe, dominio: autonomy.DomainOf("cap:fs.write", "/x")}
	var desf, dec, nErr, nEsc int
	prazo := time.Now().Add(10 * time.Second)
	for {
		agg.mu.Lock()
		desf, dec, nErr, nEsc = 0, 0, 0, 0
		if j := agg.porChave[chave]; j != nil {
			desf, dec = len(j.desfechos), len(j.decisoes)
			for _, a := range j.desfechos {
				if a.erro {
					nErr++
				}
			}
			for _, a := range j.decisoes {
				if a.escala {
					nEsc++
				}
			}
		}
		agg.mu.Unlock()
		if (desf == permits && dec == permits+escalam) || time.Now().After(prazo) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if desf != permits || nErr != erros {
		t.Fatalf("desfechos observados %d (erros %d), quer %d (erros %d): o tool.call.outcome não chegou ao stream", desf, nErr, permits, erros)
	}
	// Cada escalada aprovada conta DUAS decisões (a escalada e o permit que a seguiu): antes do
	// AOS-481 o permit era engolido e só contava a escalada.
	if dec != permits+escalam || nEsc != escalam {
		t.Fatalf("decisões observadas %d (escaladas %d), quer %d (escaladas %d)", dec, nEsc, permits+escalam, escalam)
	}

	rel := agg.Reliability(autonomy.ClassPrefix+classe, chave.dominio, janela)
	if !rel.WindowOK {
		t.Fatalf("janela não julgável com %d desfechos reais: %+v", desf, rel)
	}
	if want := float64(erros) / float64(permits); rel.ErrorRate != want {
		t.Fatalf("ErrorRate=%v, quer %v", rel.ErrorRate, want)
	}
	if want := float64(escalam) / float64(permits+escalam); rel.OverrideRate != want {
		t.Fatalf("OverrideRate=%v, quer %v", rel.OverrideRate, want)
	}

	// E é isto que o controlador passa a ver: a promoção automática dispara (um nível, abaixo
	// de L4). É o efeito em produção que o relatório do AOS-481 declara.
	ctrl, reg := controladorComFonte(t, agg, janela, autonomy.L1)
	if _, mudou, err := ctrl.Evaluate(ctx, autonomy.ClassPrefix+classe, chave.dominio); err != nil || !mudou {
		t.Fatalf("Evaluate: mudou=%v err=%v", mudou, err)
	}
	if got := reg.LevelFor(autonomy.ClassPrefix+classe, chave.dominio); got != autonomy.L2 {
		t.Fatalf("nível depois da promoção %s, quer L2", got)
	}
}
