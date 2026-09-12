package referencemonitor

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// A PRIMEIRA METADE do AOS-341: o registo de uma negação POR obrigação tem de selar a
// obrigação que a CAUSOU.
//
// A segunda metade vive no `control-plane/governance/compliance`, onde a projecção de
// soberania a lê. Cada uma tem teste no seu pacote pela razão que o #87 já tinha medido:
// corrigir só um dos lados deixa o teste do lado corrigido verde a apontar para o vazio.
// ---------------------------------------------------------------------------

// hookComObrigacoes devolve permit e anexa à cadeia as obrigações dadas. É o molde de um
// PDP que colecta obrigações sobre uma base permit — que é a única forma de chegarem ao
// `enforceObligations`.
type hookComObrigacoes struct{ obs []Obligation }

func (hookComObrigacoes) Name() string { return "pdp-de-teste" }
func (h hookComObrigacoes) Evaluate(context.Context, *Call) (HookResult, error) {
	return HookResult{Decision: HookAllow, Obligations: h.obs}, nil
}

// monitorComTool monta um RM com a tool registada — sem isso a negação vinha do
// default-deny e nunca chegaria ao enforcement das obrigações.
func monitorComTool(t *testing.T, h Hook) (*Monitor, *sinkQueGuarda, *bool) {
	t.Helper()
	s := &sinkQueGuarda{}
	despachou := false
	m := New(WithHooks(h), WithEventSink(s))
	if err := m.Register("doc_read", func(context.Context, []byte) ([]byte, error) {
		despachou = true
		return nil, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, s, &despachou
}

func chamadaAOS341(regiao string) Call {
	c := Call{
		RunID: "run-1", StepID: "step-1", ToolID: "doc_read",
		Capability: "cap:fs.read",
		Resource:   Resource{Type: "file", Value: "doc://notas", Region: regiao},
	}
	c.Context.Taint = "untrusted"
	return c
}

func obrigacaoDeRegiao() Obligation {
	return Obligation{Type: ObligationRegion, Params: map[string]string{"region": "eu"}}
}

// TestAOS341_NegacaoSelaAObrigacaoCausadora é o caso que se perdia: sem região resolvida no
// recurso, o registo saía com Obligations vazio E Resource.Region vazio — as DUAS fontes de
// que a projecção de soberania dispõe. A negação por soberania ficava invisível como tal.
func TestAOS341_NegacaoSelaAObrigacaoCausadora(t *testing.T) {
	m, sink, despachou := monitorComTool(t, hookComObrigacoes{obs: []Obligation{obrigacaoDeRegiao()}})

	dec, err := m.Mediate(context.Background(), chamadaAOS341(""))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectDeny || dec.Code != CodeObligationUnsatisfied {
		t.Fatalf("preparacao: queria deny/%s, veio %v/%s", CodeObligationUnsatisfied, dec.Effect, dec.Code)
	}
	// CONTROLO (AC5): selar mais no registo não pode mudar o que o nó deixa acontecer.
	if *despachou {
		t.Fatal("a tool foi despachada numa negacao — o selo mudou o enforcement")
	}
	if len(sink.ultimo.Obligations) != 1 {
		t.Fatalf("a negacao selou %d obrigacao(oes); quero exactamente a causadora: %+v",
			len(sink.ultimo.Obligations), sink.ultimo.Obligations)
	}
	ob := sink.ultimo.Obligations[0]
	if ob.Type != ObligationRegion || ob.Params["region"] != "eu" {
		t.Errorf("a obrigacao selada nao e a que negou: %+v", ob)
	}
	// A região EXIGIDA tem de sair do registo sem ler o texto livre (AC2).
	if sink.ultimo.Resource.Region != "" {
		t.Errorf("preparacao invalida: o recurso devia estar sem regiao resolvida, veio %q",
			sink.ultimo.Resource.Region)
	}
	// CONTROLO: a razão em texto livre CONTINUA lá — o canal novo acrescenta, não substitui.
	if sink.ultimo.Reason == "" {
		t.Error("o registo perdeu a razao em texto livre")
	}
}

// TestAOS341_SelaSoACausadoraNaoAsCumpridas é o controlo que impede a correcção preguiçosa
// de passar `obligations` (a lista acumulada) em vez da causa. Selar as CUMPRIDAS afirmaria
// que uma redacção foi aplicada a um efeito que nunca aconteceu — o erro que a assimetria do
// ramo `HookDeny` existe para não cometer.
func TestAOS341_SelaSoACausadoraNaoAsCumpridas(t *testing.T) {
	cumprida := Obligation{Type: ObligationAudit}
	m, sink, _ := monitorComTool(t, hookComObrigacoes{obs: []Obligation{cumprida, obrigacaoDeRegiao()}})

	if _, err := m.Mediate(context.Background(), chamadaAOS341("us")); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if len(sink.ultimo.Obligations) != 1 {
		t.Fatalf("selou %d obrigacao(oes); a `audit` foi CUMPRIDA e nao pertence ao registo da "+
			"negacao: %+v", len(sink.ultimo.Obligations), sink.ultimo.Obligations)
	}
	if sink.ultimo.Obligations[0].Type != ObligationRegion {
		t.Errorf("selou a obrigacao errada: %+v", sink.ultimo.Obligations[0])
	}
}

// TestAOS341_CausaValeParaQualquerTipo prova que o selo não é um caso especial da região: o
// ramo fail-closed do tipo desconhecido — o que garante que [Obligation] é vocabulário
// fechado — sela igualmente a obrigação que recusou.
func TestAOS341_CausaValeParaQualquerTipo(t *testing.T) {
	inventada := Obligation{Type: "obrigacao_inventada"}
	m, sink, _ := monitorComTool(t, hookComObrigacoes{obs: []Obligation{inventada}})

	dec, err := m.Mediate(context.Background(), chamadaAOS341("eu"))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Code != CodeObligationUnsatisfied {
		t.Fatalf("preparacao: queria %s, veio %s", CodeObligationUnsatisfied, dec.Code)
	}
	if len(sink.ultimo.Obligations) != 1 || sink.ultimo.Obligations[0].Type != "obrigacao_inventada" {
		t.Errorf("a causa nao foi selada no ramo do tipo desconhecido: %+v", sink.ultimo.Obligations)
	}
}

// TestAOS341_PermitContinuaASelarAsColetadas é o controlo do outro lado: a correcção não pode
// estreitar o caminho de permit, onde o registo leva as obrigações TODAS porque foram mesmo
// aplicadas ao efeito.
func TestAOS341_PermitContinuaASelarAsColetadas(t *testing.T) {
	m, sink, despachou := monitorComTool(t, hookComObrigacoes{
		obs: []Obligation{{Type: ObligationAudit}, {Type: ObligationTTL}},
	})

	dec, err := m.Mediate(context.Background(), chamadaAOS341("eu"))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("preparacao: queria permit, veio %v (%s)", dec.Effect, dec.Reason)
	}
	if !*despachou {
		t.Error("o permit nao despachou a tool")
	}
	if len(sink.ultimo.Obligations) != 2 {
		t.Errorf("o permit devia selar as 2 obrigacoes coletadas, selou %d: %+v",
			len(sink.ultimo.Obligations), sink.ultimo.Obligations)
	}
}
