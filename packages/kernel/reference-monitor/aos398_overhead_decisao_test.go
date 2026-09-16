package referencemonitor

import (
	"context"
	"sync"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// aos398_overhead_decisao_test.go — AOS-398 / DEF-281: a mediação passa a publicar QUANTO
// ACRESCENTA, separado de quanto a tool custou.
//
// O defeito que estes testes trancam foi observado em PRODUÇÃO a 2026-09-15/16 (run
// `run-delegado-1789519407`): uma única tool call `doc_read` num sandbox gVisor acendeu
// `mediation_overhead_high` e `mediation_overhead_p95_high`, ambos `critical` e ambos a
// apontar o RB-04 («Falha de PDP»), com 1,21 s contra um tecto de 15 ms. Não havia nada de
// errado com o PDP: a fonte do SLI era a janela do span `execute_tool`, que só fecha depois
// de a tool correr. O operador era mandado depurar a peça errada, sempre.

// relogioManual é um relógio que só avança quando alguém o manda avançar. É o que permite
// afirmar "esta janela EXCLUI aquela" sem depender do tempo real: a tool avança-o em 1,2 s e
// o teste verifica onde é que esse salto aparece — e onde NÃO aparece.
type relogioManual struct {
	mu sync.Mutex
	t  time.Time
}

func novoRelogioManual() *relogioManual {
	return &relogioManual{t: time.Unix(1_700_000_000, 0)}
}

func (r *relogioManual) agora() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *relogioManual) avancar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = r.t.Add(d)
}

// spanSpy recolhe os atributos anotados no span `execute_tool`, que é por onde a medida
// atravessa a fronteira até ao wide event e ao SLI.
type spanSpy struct {
	mu    sync.Mutex
	attrs map[string]any
}

func (s *spanSpy) SetAttribute(k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]any{}
	}
	s.attrs[k] = v
}

func (s *spanSpy) SpanContext() otelgenai.SpanContext { return otelgenai.SpanContext{} }

func (s *spanSpy) End() {}

func (s *spanSpy) atributo(k string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.attrs[k]
	return v, ok
}

type tracerSpy struct{ span *spanSpy }

func (t *tracerSpy) StartSpan(ctx context.Context, _ string) (context.Context, otelgenai.Span) {
	return ctx, t.span
}

// TestAOS398_DecisionLatencyExcluiODespacho é o teste que FALHA antes desta correcção.
//
// Cenário exacto do incidente: a cadeia de política custa 8 ms (a gama real medida nos runs
// de produção é 2–8,6 ms) e a tool corre 1,2 s no sandbox. O que a mediação ACRESCENTA são os
// 8 ms. Antes do AOS-398 o único número publicado era 1,208 s.
func TestAOS398_DecisionLatencyExcluiODespacho(t *testing.T) {
	const (
		custoDaPolitica = 8 * time.Millisecond
		custoDaTool     = 1200 * time.Millisecond
	)
	clk := novoRelogioManual()

	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}, mutate: func(*Call) {
		clk.avancar(custoDaPolitica)
	}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	// A tool é o sandbox: consome 1,2 s de relógio. É o salto que NÃO pode aparecer no overhead.
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) {
		clk.avancar(custoDaTool)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("o cenário exige um permit; veio %q [%s]", dec.Effect, dec.Reason)
	}

	if dec.DecisionLatency != custoDaPolitica {
		t.Fatalf("DecisionLatency = %v — tem de ser SÓ a cadeia de decisão (%v), sem o despacho",
			dec.DecisionLatency, custoDaPolitica)
	}
	if dec.Latency != custoDaPolitica+custoDaTool {
		t.Fatalf("Latency = %v — a janela TOTAL tem de continuar a incluir o despacho (%v)",
			dec.Latency, custoDaPolitica+custoDaTool)
	}
	// A propriedade que interessa ao SLO, dita sem números: o overhead não paga a tool.
	if dec.DecisionLatency >= dec.Latency {
		t.Fatal("o overhead de decisão tem de ser ESTRITAMENTE menor que a duração da tool call mediada")
	}
}

// TestAOS398_SpanPublicaAJanelaDaDecisao — a medida certa não serve de nada presa ao kernel.
// Tem de sair no span, que é o que o nó projecta em wide event e o SLI lê.
func TestAOS398_SpanPublicaAJanelaDaDecisao(t *testing.T) {
	clk := novoRelogioManual()
	spy := &spanSpy{}

	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}, mutate: func(*Call) {
		clk.avancar(8 * time.Millisecond)
	}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	m.SetTracer(&tracerSpy{span: spy})
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) {
		clk.avancar(1200 * time.Millisecond)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := m.Mediate(context.Background(), baseCall()); err != nil {
		t.Fatalf("Mediate: %v", err)
	}

	v, ok := spy.atributo(otelgenai.AttrMediationDecisionLatencyNanos)
	if !ok {
		t.Fatalf("o span execute_tool tem de anotar %s — sem ele o SLI não tem fonte honesta",
			otelgenai.AttrMediationDecisionLatencyNanos)
	}
	got, ok := v.(int64)
	if !ok {
		t.Fatalf("o atributo tem de ser int64 (nanos) para o bag o derivar; veio %T", v)
	}
	if got != int64(8*time.Millisecond) {
		t.Fatalf("o span publicou %v — devia publicar a janela da DECISÃO (8ms), não a do span",
			time.Duration(got))
	}
}

// TestAOS398_RecusaNaoDespachaLogoAsDuasJanelasCoincidem — num deny/escalate não há efeito, e
// portanto não há nada que separar. Tem de ser dito por teste: se as duas divergissem aqui,
// alguma delas estaria a medir trabalho que não aconteceu.
func TestAOS398_RecusaNaoDespachaLogoAsDuasJanelasCoincidem(t *testing.T) {
	clk := novoRelogioManual()
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookDeny, Reason: "negado"}, mutate: func(*Call) {
		clk.avancar(3 * time.Millisecond)
	}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))

	dec, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectDeny {
		t.Fatalf("esperava deny; veio %q", dec.Effect)
	}
	if dec.DecisionLatency != dec.Latency {
		t.Fatalf("sem despacho as duas janelas têm de coincidir: decisão=%v total=%v",
			dec.DecisionLatency, dec.Latency)
	}
	if dec.DecisionLatency != 3*time.Millisecond {
		t.Fatalf("DecisionLatency = %v — esperava os 3ms da cadeia", dec.DecisionLatency)
	}
}

// TestAOS398_OSeloDeMediacaoNaoMudou — o `latency_ns` de `tool.call.mediated` é contrato de
// fio ancorado no WORM (tecnica/12 §4). AOS-398 acrescenta uma medida NOVA; não reescreve
// esta. A janela do selo pára ANTES da escrita de auditoria, logo já excluía o despacho.
func TestAOS398_OSeloDeMediacaoNaoMudou(t *testing.T) {
	clk := novoRelogioManual()
	sink := &sinkEspiao{}
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}, mutate: func(*Call) {
		clk.avancar(8 * time.Millisecond)
	}}
	m := New(WithHooks(hook), WithEventSink(sink), withClock(clk.agora))
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) {
		clk.avancar(1200 * time.Millisecond)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := m.Mediate(context.Background(), baseCall()); err != nil {
		t.Fatalf("Mediate: %v", err)
	}

	recs := sink.todos()
	if len(recs) != 1 {
		t.Fatalf("esperava um registo de mediação; vieram %d", len(recs))
	}
	if recs[0].Latency != 8*time.Millisecond {
		t.Fatalf("o selo mudou de significado: latency_ns = %v, esperava os 8ms de sempre", recs[0].Latency)
	}
}

// TestAOS398_AEscritaDeAuditoriaContaComoOverhead tranca a decisão do ADR-026 §1 que os
// outros testes não distinguem: com um relógio que só a tool e os hooks fazem avançar, o selo
// e a decisão dão o mesmo número, e ninguém saberia se a escrita de auditoria está dentro ou
// fora. Aqui o SINK consome 2 ms. A janela da decisão tem de os incluir — o selo durável está no
// caminho crítico e atrasa o efeito — e o `latency_ns` do selo, que pára antes da escrita, não.
func TestAOS398_AEscritaDeAuditoriaContaComoOverhead(t *testing.T) {
	clk := novoRelogioManual()
	sink := &sinkEspiao{antes: func() { clk.avancar(2 * time.Millisecond) }}
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}, mutate: func(*Call) {
		clk.avancar(8 * time.Millisecond)
	}}
	m := New(WithHooks(hook), WithEventSink(sink), withClock(clk.agora))
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) {
		clk.avancar(1200 * time.Millisecond)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("o cenário exige um permit; veio %q [%s]", dec.Effect, dec.Reason)
	}

	if dec.DecisionLatency != 10*time.Millisecond {
		t.Fatalf("DecisionLatency = %v — tem de incluir a escrita de auditoria (8ms de política + 2ms de selo)",
			dec.DecisionLatency)
	}
	recs := sink.todos()
	if len(recs) != 1 || recs[0].Latency != 8*time.Millisecond {
		t.Fatalf("o selo tem de continuar a parar ANTES da escrita (8ms); veio %+v", recs)
	}
	if dec.Latency != 1210*time.Millisecond {
		t.Fatalf("Latency = %v — a janela total inclui política, selo e despacho (1,21s)", dec.Latency)
	}
}

// sinkEspiao guarda os registos de mediação para inspecção.
type sinkEspiao struct {
	mu   sync.Mutex
	recs []MediationRecord
	seq  uint64
	// antes, se definido, corre no início de cada escrita — é o custo do sink.
	antes func()
}

func (s *sinkEspiao) RecordMediation(_ context.Context, rec MediationRecord) (uint64, error) {
	if s.antes != nil {
		s.antes()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.recs = append(s.recs, rec)
	return s.seq, nil
}

func (s *sinkEspiao) todos() []MediationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MediationRecord, len(s.recs))
	copy(out, s.recs)
	return out
}
