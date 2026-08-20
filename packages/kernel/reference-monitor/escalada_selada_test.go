package referencemonitor

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// A SEGUNDA METADE: o REGISTO da escalada tem de levar as obrigações.
//
// O adaptador RM↔PDP deitava-as fora no ramo `Escalate`; mas mesmo propagadas, `Monitor.fail`
// construía o [MediationRecord] SEM elas. Corrigir só um dos dois lados deixaria a cadeia a perder
// na mesma — e o teste do lado do PDP ficaria verde a apontar para um trilho que continuava vazio.
//
// É por isso que esta metade tem teste próprio, no pacote onde o registo é construído.
// ---------------------------------------------------------------------------

// sinkQueGuarda captura o registo de mediação para o teste o poder ler.
type sinkQueGuarda struct{ ultimo MediationRecord }

func (s *sinkQueGuarda) RecordMediation(_ context.Context, rec MediationRecord) (uint64, error) {
	s.ultimo = rec
	return 1, nil
}

// hookQueEscala devolve sempre escalada, com a obrigação que a decisão de autonomia carregaria.
type hookQueEscala struct{ obs []Obligation }

func (hookQueEscala) Name() string { return "autonomia-de-teste" }
func (h hookQueEscala) Evaluate(context.Context, *Call) (HookResult, error) {
	return HookResult{
		Decision:    HookEscalate,
		Reason:      "autonomia L0 x gray -> suggest (gate humano)",
		Obligations: h.obs,
	}, nil
}

// hookQueNega é o par do anterior para o controlo da assimetria.
type hookQueNega struct{ obs []Obligation }

func (hookQueNega) Name() string { return "nega-de-teste" }
func (h hookQueNega) Evaluate(context.Context, *Call) (HookResult, error) {
	return HookResult{Decision: HookDeny, Reason: "negado", Obligations: h.obs}, nil
}

func obrigacaoDeAutonomiaDeTeste() []Obligation {
	return []Obligation{{
		Type: ObligationAutonomy,
		Params: map[string]string{
			"level": "L0", "domain": "fs", "oversight": "suggest", "risk_class": "gray",
		},
	}}
}

func monitorComHook(t *testing.T, h Hook) (*Monitor, *sinkQueGuarda) {
	t.Helper()
	s := &sinkQueGuarda{}
	m := New(WithHooks(h), WithEventSink(s))
	return m, s
}

func chamadaDeTeste() Call {
	c := Call{
		RunID: "run-1", StepID: "step-1", ToolID: "doc_read",
		Capability: "cap:fs.read",
		Resource:   Resource{Type: "file", Value: "doc://notes", Region: "eu"},
	}
	c.Context.Taint = "untrusted"
	return c
}

// TestEscaladaSelaAsObrigacoes é o teste da segunda camada.
func TestEscaladaSelaAsObrigacoes(t *testing.T) {
	m, sink := monitorComHook(t, hookQueEscala{obs: obrigacaoDeAutonomiaDeTeste()})

	dec, err := m.Mediate(context.Background(), chamadaDeTeste())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectEscalate {
		t.Fatalf("preparacao: queria EffectEscalate, veio %v", dec.Effect)
	}
	if len(sink.ultimo.Obligations) == 0 {
		t.Fatalf("o registo da ESCALADA saiu com Obligations vazio — quem percorrer obrigacoes ve "+
			"as autorizacoes e NAO ve as escaladas; o porque fica so em texto livre: %q",
			sink.ultimo.Reason)
	}
	ob := sink.ultimo.Obligations[0]
	if ob.Type != ObligationAutonomy || ob.Params["level"] != "L0" || ob.Params["oversight"] != "suggest" {
		t.Errorf("a obrigacao selada nao e a da decisao: %+v", ob)
	}
	// CONTROLO: a razão em texto livre CONTINUA lá. A correcção acrescenta um canal legível por
	// máquina, não substitui o que já se lia — quem depende do `Reason` não pode partir.
	if sink.ultimo.Reason == "" {
		t.Error("o registo perdeu a razao em texto livre")
	}
}

// TestNegacaoNaoSelaObrigacoes fixa a assimetria DELIBERADA, no sítio onde ela é imposta.
//
// Numa negação nenhum efeito aconteceu. Registar obrigações da base — redacção, ttl — sugeriria
// que algo lhes foi aplicado, e o trilho passaria a afirmar um facto falso sobre um efeito que
// nunca existiu. Se um dia esta assimetria for para mudar, muda-se aqui e por decisão escrita.
func TestNegacaoNaoSelaObrigacoes(t *testing.T) {
	m, sink := monitorComHook(t, hookQueNega{obs: []Obligation{{Type: "redact"}}})

	dec, err := m.Mediate(context.Background(), chamadaDeTeste())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectDeny {
		t.Fatalf("queria EffectDeny, veio %v", dec.Effect)
	}
	if len(sink.ultimo.Obligations) != 0 {
		t.Errorf("a NEGACAO selou %d obrigacao(oes) — o trilho sugeriria uma redaccao aplicada a um "+
			"efeito que nunca aconteceu: %+v", len(sink.ultimo.Obligations), sink.ultimo.Obligations)
	}
}
