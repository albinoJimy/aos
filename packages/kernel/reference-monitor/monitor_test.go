package referencemonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Helpers de teste
// ---------------------------------------------------------------------------

// fakeSink é um EventSink em memória, com falha injectável por Effect.
type fakeSink struct {
	mu         sync.Mutex
	records    []MediationRecord
	fail       error  // se != nil, RecordMediation falha
	failEffect Effect // se != "", falha só para este Effect
	seq        uint64
}

func (f *fakeSink) RecordMediation(_ context.Context, rec MediationRecord) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil && (f.failEffect == "" || rec.Effect == f.failEffect) {
		return 0, f.fail
	}
	f.seq++
	f.records = append(f.records, rec)
	return f.seq, nil
}

func (f *fakeSink) all() []MediationRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]MediationRecord, len(f.records))
	copy(out, f.records)
	return out
}

// spyHook regista a sua invocação (ordem) e o call que viu, com resultado/erro/
// panic configuráveis.
type spyHook struct {
	name    string
	order   *[]string
	seen    *Call
	result  HookResult
	err     error
	doPanic bool
	mutate  func(*Call)
}

func (h *spyHook) Name() string { return h.name }

func (h *spyHook) Evaluate(_ context.Context, call *Call) (HookResult, error) {
	if h.order != nil {
		*h.order = append(*h.order, h.name)
	}
	if h.mutate != nil {
		h.mutate(call)
	}
	if h.seen != nil {
		*h.seen = *call
	}
	if h.doPanic {
		panic("boom no hook")
	}
	return h.result, h.err
}

// toolSpy devolve uma ToolFunc que sinaliza a sua execução.
func toolSpy(called *bool, out []byte) ToolFunc {
	return func(_ context.Context, _ []byte) ([]byte, error) {
		*called = true
		return out, nil
	}
}

func baseCall() Call {
	return Call{
		RunID: "run-1", StepID: "step-1", ParentStepID: "step-0",
		ToolID: "tool.echo", Capability: "cap:echo",
		Resource:  Resource{Type: "url", Value: "https://ex/eu", Region: "eu"},
		Principal: Principal{NHIID: "nhi-1", AgentID: "agt-1", Authority: []string{"cap:echo"}},
		Context:   CallContext{Taint: "trusted", BudgetTokensRemaining: 1000},
		Input:     []byte("payload"),
	}
}

// ---------------------------------------------------------------------------
// Caminho permit feliz
// ---------------------------------------------------------------------------

func TestMediate_PermitDespachaERegista(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	var called bool
	m := New(WithEventSink(sink))
	if err := m.Register("tool.echo", toolSpy(&called, []byte("ok"))); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate erro inesperado: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava Permitted, obtive Effect=%q", d.Effect)
	}
	if !called {
		t.Fatal("a tool devia ter sido despachada")
	}
	if string(d.Output) != "ok" {
		t.Errorf("Output=%q, esperava ok", d.Output)
	}
	if d.MediationSeq == 0 {
		t.Error("MediationSeq devia ser != 0 num permit auditado")
	}
	if d.Latency < 0 {
		t.Errorf("Latency invalida: %v", d.Latency)
	}
	recs := sink.all()
	if len(recs) != 1 || recs[0].Effect != EffectPermit {
		t.Fatalf("esperava 1 registo EffectPermit, obtive %+v", recs)
	}
	if recs[0].RunID != "run-1" || recs[0].StepID != "step-1" {
		t.Errorf("registo sem run/step corretos: %+v", recs[0])
	}
	p, dn, es := m.Metrics().Snapshot()
	if p != 1 || dn != 0 || es != 0 {
		t.Errorf("metricas erradas: permits=%d denials=%d esc=%d", p, dn, es)
	}
}

// ---------------------------------------------------------------------------
// Ordem de hooks + propagação de contexto
// ---------------------------------------------------------------------------

func TestMediate_OrdemDeHooksEPropagacao(t *testing.T) {
	t.Parallel()
	var order []string
	var seenByEgress Call
	hooks := []Hook{
		&spyHook{name: "identity", order: &order, mutate: func(c *Call) { c.Principal.AgentID = "resolvido" }},
		&spyHook{name: "policy", order: &order, result: HookResult{Decision: HookAllow, Obligations: []Obligation{{Type: "audit"}}}},
		&spyHook{name: "budget", order: &order},
		&spyHook{name: "egress", order: &order, seen: &seenByEgress},
		&spyHook{name: "audit", order: &order},
	}
	sink := &fakeSink{}
	var called bool
	m := New(WithHooks(hooks...), WithEventSink(sink))
	_ = m.Register("tool.echo", toolSpy(&called, nil))

	d, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != EffectPermit {
		t.Fatalf("esperava EffectPermit, obtive %q (%s)", d.Effect, d.Reason)
	}
	want := []string{"identity", "policy", "budget", "egress", "audit"}
	if len(order) != len(want) {
		t.Fatalf("ordem=%v, esperava %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("ordem[%d]=%q, esperava %q (ordem completa=%v)", i, order[i], want[i], order)
		}
	}
	// Propagação: identity resolveu AgentID e egress viu-o; run/step propagam.
	if seenByEgress.Principal.AgentID != "resolvido" {
		t.Errorf("propagacao de principal falhou: egress viu AgentID=%q", seenByEgress.Principal.AgentID)
	}
	if seenByEgress.RunID != "run-1" || seenByEgress.StepID != "step-1" {
		t.Errorf("run/step nao propagaram: %+v", seenByEgress)
	}
	// Obrigações acumuladas propagam para a Decision.
	if len(d.Obligations) != 1 || d.Obligations[0].Type != "audit" {
		t.Errorf("obrigacoes nao propagaram: %+v", d.Obligations)
	}
}

// ---------------------------------------------------------------------------
// Fail-closed
// ---------------------------------------------------------------------------

func TestMediate_FailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		hooks      []Hook
		sinkFail   error
		failEffect Effect
		toolReg    bool
		wantEffect Effect
		wantDenyBy string
		wantCalled bool
	}{
		{
			name: "deny_de_policy_impede_efeito",
			hooks: []Hook{
				IdentityStub{},
				&spyHook{name: "policy", result: HookResult{Decision: HookDeny, Reason: "capability nao permitida"}},
			},
			toolReg: true, wantEffect: EffectDeny, wantDenyBy: "policy", wantCalled: false,
		},
		{
			name: "erro_de_hook_e_deny",
			hooks: []Hook{
				&spyHook{name: "identity", err: errors.New("resolucao falhou")},
			},
			toolReg: true, wantEffect: EffectDeny, wantDenyBy: "identity", wantCalled: false,
		},
		{
			name: "panic_de_hook_e_deny",
			hooks: []Hook{
				&spyHook{name: "budget", doPanic: true},
			},
			toolReg: true, wantEffect: EffectDeny, wantDenyBy: "budget", wantCalled: false,
		},
		{
			name: "escalate_impede_efeito",
			hooks: []Hook{
				&spyHook{name: "policy", result: HookResult{Decision: HookEscalate, Reason: "acao irreversivel"}},
			},
			toolReg: true, wantEffect: EffectEscalate, wantDenyBy: "policy", wantCalled: false,
		},
		{
			name:    "tool_nao_registada_e_deny",
			hooks:   DefaultHooks(),
			toolReg: false, wantEffect: EffectDeny, wantDenyBy: "dispatch", wantCalled: false,
		},
		{
			name:       "falha_de_auditoria_no_permit_e_deny",
			hooks:      DefaultHooks(),
			sinkFail:   errors.New("ES indisponivel"),
			failEffect: EffectPermit,
			toolReg:    true, wantEffect: EffectDeny, wantDenyBy: "audit-sink", wantCalled: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &fakeSink{fail: tc.sinkFail, failEffect: tc.failEffect}
			var called bool
			m := New(WithHooks(tc.hooks...), WithEventSink(sink))
			if tc.toolReg {
				_ = m.Register("tool.echo", toolSpy(&called, []byte("efeito")))
			}

			d, err := m.Mediate(context.Background(), baseCall())
			if err != nil {
				t.Fatalf("Mediate erro: %v", err)
			}
			if d.Effect != tc.wantEffect {
				t.Errorf("Effect=%q, esperava %q (reason=%s)", d.Effect, tc.wantEffect, d.Reason)
			}
			if d.DeniedBy != tc.wantDenyBy {
				t.Errorf("DeniedBy=%q, esperava %q", d.DeniedBy, tc.wantDenyBy)
			}
			if called != tc.wantCalled {
				t.Errorf("tool called=%v, esperava %v (o efeito nao devia ocorrer em fail-closed)", called, tc.wantCalled)
			}
			if d.Permitted() {
				t.Error("Permitted() devia ser false em fail-closed")
			}
			// A negação/escalonamento é auditada (best-effort): há sempre >= 1
			// registo com o Effect final.
			recs := sink.all()
			foundFinal := false
			for _, r := range recs {
				if r.Effect == tc.wantEffect {
					foundFinal = true
				}
			}
			if !foundFinal {
				t.Errorf("esperava registo de auditoria com Effect=%q, registos=%+v", tc.wantEffect, recs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// No-bypass estrutural: EffectPermit não-forjável + dispatcher interno
// ---------------------------------------------------------------------------

func TestNoBypass_PermitNaoForjavel(t *testing.T) {
	t.Parallel()
	var called bool
	m := New()
	_ = m.Register("tool.echo", toolSpy(&called, []byte("x")))
	call := baseCall()
	ctx := context.Background()

	t.Run("permit_zero_e_rejeitado", func(t *testing.T) {
		called = false
		_, _, err := m.dispatch(ctx, &Permit{}, call)
		if !errors.Is(err, ErrInvalidPermit) {
			t.Fatalf("esperava ErrInvalidPermit, obtive %v", err)
		}
		if called {
			t.Error("tool nao devia executar com permit forjado")
		}
	})

	t.Run("permit_nil_e_rejeitado", func(t *testing.T) {
		called = false
		_, _, err := m.dispatch(ctx, nil, call)
		if !errors.Is(err, ErrInvalidPermit) {
			t.Fatalf("esperava ErrInvalidPermit, obtive %v", err)
		}
		if called {
			t.Error("tool nao devia executar com permit nil")
		}
	})

	t.Run("permit_valido_uso_unico", func(t *testing.T) {
		called = false
		p := m.mint(call)
		if _, _, err := m.dispatch(ctx, p, call); err != nil {
			t.Fatalf("permit valido devia despachar: %v", err)
		}
		if !called {
			t.Error("tool devia ter executado com permit valido")
		}
		// Reutilização: uso único.
		called = false
		if _, _, err := m.dispatch(ctx, p, call); !errors.Is(err, ErrInvalidPermit) {
			t.Fatalf("reutilizacao devia dar ErrInvalidPermit, obtive %v", err)
		}
		if called {
			t.Error("tool nao devia reexecutar (uso unico)")
		}
	})

	t.Run("permit_de_outro_call_e_rejeitado", func(t *testing.T) {
		called = false
		p := m.mint(call)
		other := call
		other.StepID = "step-outro"
		if _, _, err := m.dispatch(ctx, p, other); !errors.Is(err, ErrInvalidPermit) {
			t.Fatalf("permit de outro call devia dar ErrInvalidPermit, obtive %v", err)
		}
		if called {
			t.Error("tool nao devia executar com permit de outro call")
		}
	})
}

// ---------------------------------------------------------------------------
// Registo de tools
// ---------------------------------------------------------------------------

func TestRegister_Validacoes(t *testing.T) {
	t.Parallel()
	m := New()
	if err := m.Register("", func(context.Context, []byte) ([]byte, error) { return nil, nil }); !errors.Is(err, ErrInvalidRegistration) {
		t.Errorf("tool_id vazio devia dar ErrInvalidRegistration, obtive %v", err)
	}
	if err := m.Register("t", nil); !errors.Is(err, ErrInvalidRegistration) {
		t.Errorf("fn nil devia dar ErrInvalidRegistration, obtive %v", err)
	}
	if err := m.Register("t", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Errorf("registo valido: %v", err)
	}
	if err := m.Register("t", func(context.Context, []byte) ([]byte, error) { return nil, nil }); !errors.Is(err, ErrToolAlreadyRegistered) {
		t.Errorf("re-registo devia dar ErrToolAlreadyRegistered, obtive %v", err)
	}
}

// TestRegisterCosting_CostSurfacesInDecision prova AOS-212 ao NÍVEL DO RM: uma tool
// registada por RegisterCosting REPORTA o custo medido do efeito, e esse custo surge em
// Decision.CostMicroUSD; uma tool de Register (produtor de referência honesto) reporta 0.
func TestRegisterCosting_CostSurfacesInDecision(t *testing.T) {
	t.Parallel()

	t.Run("costing_reporta_custo", func(t *testing.T) {
		m := New()
		const want = int64(4_200_000) // 4.2 USD
		if err := m.RegisterCosting("tool.echo", func(_ context.Context, in []byte) ([]byte, int64, error) {
			return in, want, nil
		}); err != nil {
			t.Fatalf("RegisterCosting: %v", err)
		}
		d, err := m.Mediate(context.Background(), baseCall())
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if d.Effect != EffectPermit {
			t.Fatalf("esperava permit, veio %q (%s)", d.Effect, d.Reason)
		}
		if d.CostMicroUSD != want {
			t.Errorf("Decision.CostMicroUSD = %d, esperava %d (custo reportado pela tool)", d.CostMicroUSD, want)
		}
	})

	t.Run("register_simples_reporta_zero", func(t *testing.T) {
		m := New()
		var called bool
		if err := m.Register("tool.echo", toolSpy(&called, []byte("x"))); err != nil {
			t.Fatalf("Register: %v", err)
		}
		d, err := m.Mediate(context.Background(), baseCall())
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if d.Effect != EffectPermit {
			t.Fatalf("esperava permit, veio %q", d.Effect)
		}
		if d.CostMicroUSD != 0 {
			t.Errorf("uma tool de Register (sem custo) devia reportar 0, veio %d", d.CostMicroUSD)
		}
	})

	t.Run("validacoes", func(t *testing.T) {
		m := New()
		if err := m.RegisterCosting("", func(context.Context, []byte) ([]byte, int64, error) { return nil, 0, nil }); !errors.Is(err, ErrInvalidRegistration) {
			t.Errorf("tool_id vazio devia dar ErrInvalidRegistration, veio %v", err)
		}
		if err := m.RegisterCosting("t", nil); !errors.Is(err, ErrInvalidRegistration) {
			t.Errorf("fn nil devia dar ErrInvalidRegistration, veio %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Contexto cancelado
// ---------------------------------------------------------------------------

func TestMediate_ContextoCancelado(t *testing.T) {
	t.Parallel()
	var called bool
	m := New()
	_ = m.Register("tool.echo", toolSpy(&called, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := m.Mediate(ctx, baseCall())
	if err == nil {
		t.Error("esperava erro de contexto cancelado")
	}
	if d.Effect != EffectDeny {
		t.Errorf("Effect=%q, esperava EffectDeny (fail-closed)", d.Effect)
	}
	if called {
		t.Error("tool nao devia executar com contexto cancelado")
	}
}

// TestMediate_CadeiaDeHooksVaziaNegaFailClosed assevera que uma cadeia de hooks
// vazia (misconfiguração) NÃO permite tudo silenciosamente: é negada fail-closed
// com o código estável CodeEmptyHookChain e a tool não é despachada.
func TestMediate_CadeiaDeHooksVaziaNegaFailClosed(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	var called bool
	m := New(WithHooks(), WithEventSink(sink)) // cadeia vazia
	_ = m.Register("tool.echo", toolSpy(&called, []byte("x")))

	d, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if d.Effect != EffectDeny {
		t.Fatalf("cadeia vazia devia negar, obtive Effect=%q", d.Effect)
	}
	if d.Code != CodeEmptyHookChain {
		t.Errorf("Code=%q, esperava %q", d.Code, CodeEmptyHookChain)
	}
	if d.Permitted() {
		t.Error("Permitted() devia ser false com cadeia vazia")
	}
	if called {
		t.Error("a tool NAO devia ter sido despachada com cadeia vazia")
	}
	if recs := sink.all(); len(recs) != 1 || recs[0].Effect != EffectDeny {
		t.Errorf("esperava 1 registo deny, obtive %+v", recs)
	}
}

// TestMediate_ContextoCanceladoNaoAudita documenta que a negação por contexto
// já cancelado é deliberadamente NÃO-auditada (gravar exigiria o mesmo contexto
// morto) e expõe o código estável CodeContextCanceled.
func TestMediate_ContextoCanceladoNaoAudita(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	m := New(WithEventSink(sink))
	_ = m.Register("tool.echo", func(context.Context, []byte) ([]byte, error) { return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d, err := m.Mediate(ctx, baseCall())
	if err == nil {
		t.Fatal("esperava erro de contexto cancelado")
	}
	if d.Effect != EffectDeny || d.Code != CodeContextCanceled {
		t.Errorf("esperava deny/%q, obtive Effect=%q Code=%q", CodeContextCanceled, d.Effect, d.Code)
	}
	if recs := sink.all(); len(recs) != 0 {
		t.Errorf("negacao por contexto morto nao deve ser auditada, obtive %+v", recs)
	}
}

// TestDecision_CodigosEstaveis assevera que cada caminho de negação expõe um
// Decision.Code estável, permitindo ao chamador ramificar sem parse de strings.
func TestDecision_CodigosEstaveis(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		hooks    []Hook
		sinkFail error
		toolReg  bool
		wantCode string
	}{
		{"deny_de_hook", []Hook{&spyHook{name: "policy", result: HookResult{Decision: HookDeny}}}, nil, true, CodeDeniedByHook},
		{"escalate_de_hook", []Hook{&spyHook{name: "policy", result: HookResult{Decision: HookEscalate}}}, nil, true, CodeEscalated},
		{"erro_de_hook", []Hook{&spyHook{name: "identity", err: errors.New("x")}}, nil, true, CodeHookError},
		{"panic_de_hook", []Hook{&spyHook{name: "budget", doPanic: true}}, nil, true, CodeHookError},
		{"tool_nao_registada", DefaultHooks(), nil, false, CodeToolNotRegistered},
		{"audit_indisponivel", DefaultHooks(), errors.New("ES down"), true, CodeAuditUnavailable},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &fakeSink{fail: tc.sinkFail, failEffect: EffectPermit}
			m := New(WithHooks(tc.hooks...), WithEventSink(sink))
			if tc.toolReg {
				_ = m.Register("tool.echo", func(context.Context, []byte) ([]byte, error) { return nil, nil })
			}
			d, err := m.Mediate(context.Background(), baseCall())
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if d.Code != tc.wantCode {
				t.Errorf("Code=%q, esperava %q", d.Code, tc.wantCode)
			}
		})
	}
}

// TestMediate_Concurrent dispara N goroutines a mediar contra o MESMO Monitor,
// em simultâneo com Registers concorrentes de tools distintas, para que o -race
// apanhe regressões de concorrência no tools map / métricas / sink.
func TestMediate_Concurrent(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	m := New(WithEventSink(sink))
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx := context.Background()

	const goroutines = 64
	const perG = 200
	var wg sync.WaitGroup
	var permits int64

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				d, err := m.Mediate(ctx, baseCall())
				if err != nil {
					t.Errorf("Mediate concorrente: %v", err)
					return
				}
				if d.Permitted() {
					atomic.AddInt64(&permits, 1)
				}
			}
		}()
	}
	// Registers concorrentes de tools distintas: exercita o caminho de escrita do
	// tools map sob o mesmo lock que os leitores em Mediate usam.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = m.Register(fmt.Sprintf("tool.dyn.%d", id), func(_ context.Context, in []byte) ([]byte, error) { return in, nil })
		}(g)
	}
	wg.Wait()

	if permits != int64(goroutines*perG) {
		t.Fatalf("esperava %d permits, obtive %d", goroutines*perG, permits)
	}
	p, dn, es := m.Metrics().Snapshot()
	if p != uint64(goroutines*perG) || dn != 0 || es != 0 {
		t.Errorf("metricas concorrentes: permits=%d denials=%d esc=%d", p, dn, es)
	}
	if len(sink.all()) != goroutines*perG {
		t.Errorf("esperava %d registos de mediacao, obtive %d", goroutines*perG, len(sink.all()))
	}
}

// ---------------------------------------------------------------------------
// Integração com o Event Store REAL (AOS-002)
// ---------------------------------------------------------------------------

func TestMediate_EventoNoEventStoreReal(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := New(WithEventSink(NewEventStoreSink(store)))
	_ = m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	call := baseCall()
	call.RequestID = "req-42"
	call.Principal.DelegationChain = []DelegationHop{{Sub: "human:alice", ActAs: "agt-root"}, {Sub: "agt-root", ActAs: "agt-1"}}
	ctx := context.Background()

	d, err := m.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava EffectPermit, obtive %q", d.Effect)
	}

	events, err := store.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("esperava 1 evento no stream run-1, obtive %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventTypeMediated {
		t.Errorf("Type=%q, esperava %q", ev.Type, EventTypeMediated)
	}
	if ev.RunID != "run-1" || ev.StepID != "step-1" {
		t.Errorf("run/step no evento errados: run=%q step=%q", ev.RunID, ev.StepID)
	}
	if ev.Producer.NHIID != "nhi-1" || len(ev.Producer.DelegationChain) != 2 {
		t.Errorf("producer/cadeia de delegacao errados: %+v", ev.Producer)
	}
	if uint64(d.MediationSeq) != ev.Seq {
		t.Errorf("MediationSeq=%d != Seq do evento=%d", d.MediationSeq, ev.Seq)
	}

	var payload mediationPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if payload.Decision != string(EffectPermit) {
		t.Errorf("payload.Decision=%q, esperava permit", payload.Decision)
	}
	if payload.ToolID != "tool.echo" {
		t.Errorf("payload.ToolID=%q", payload.ToolID)
	}
	if payload.LatencyNanos < 0 {
		t.Errorf("payload.LatencyNanos invalido: %d", payload.LatencyNanos)
	}
	if payload.Principal.NHIID != "nhi-1" {
		t.Errorf("payload.Principal.NHIID=%q", payload.Principal.NHIID)
	}
	// Convenção transversal C1 + completude forense: request_id, port_version,
	// recurso alvo e contexto de decisão ficam no próprio evento.
	if payload.RequestID != "req-42" {
		t.Errorf("payload.RequestID=%q, esperava req-42", payload.RequestID)
	}
	if payload.PortVersion != PortVersion {
		t.Errorf("payload.PortVersion=%q, esperava %q", payload.PortVersion, PortVersion)
	}
	if payload.Resource.Type != "url" || payload.Resource.Value != "https://ex/eu" || payload.Resource.Region != "eu" {
		t.Errorf("payload.Resource incompleto: %+v", payload.Resource)
	}
	if payload.Context.Taint != "trusted" {
		t.Errorf("payload.Context.Taint=%q, esperava trusted", payload.Context.Taint)
	}
}

func TestMediate_DenyNoEventStoreReal(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := New(
		WithHooks(IdentityStub{}, &spyHook{name: "policy", result: HookResult{Decision: HookDeny, Reason: "negado"}}),
		WithEventSink(NewEventStoreSink(store)),
	)
	_ = m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	ctx := context.Background()
	d, err := m.Mediate(ctx, baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != EffectDeny {
		t.Fatalf("esperava EffectDeny, obtive %q", d.Effect)
	}
	events, err := store.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 || events[0].Type != EventTypeDenied {
		t.Fatalf("esperava 1 evento denied, obtive %+v", events)
	}
}

// TestMediate_LatenciaCapturada usa um relógio injectado que avança de forma
// determinística para provar que a latência é medida e propagada ao registo de
// auditoria (cobre withClock/withNonce).
func TestMediate_LatenciaCapturada(t *testing.T) {
	t.Parallel()
	var ticks int64
	clock := func() time.Time {
		ticks++
		return time.Unix(0, ticks*int64(time.Millisecond))
	}
	sink := &fakeSink{}
	m := New(
		WithEventSink(sink),
		withClock(clock),
		withNonce(func() uint64 { return 42 }),
	)
	var called bool
	_ = m.Register("tool.echo", toolSpy(&called, nil))

	d, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != EffectPermit {
		t.Fatalf("esperava permit, obtive %q", d.Effect)
	}
	if d.Latency <= 0 {
		t.Errorf("com relogio injectado a latencia devia ser > 0, obtive %v", d.Latency)
	}
	recs := sink.all()
	if len(recs) != 1 || recs[0].Latency <= 0 {
		t.Fatalf("registo devia carregar latencia > 0: %+v", recs)
	}
}

// ---------------------------------------------------------------------------
// Overhead p95 < 15 ms
// ---------------------------------------------------------------------------

// TestMediate_P95Overhead é um SMOKE bound do overhead de mediação em memória
// contra o alvo NFR-01 (p95 < 15 ms). O alvo é folgado por ~4 ordens de
// grandeza face ao custo real (~1 µs), pelo que o gate não é flaky na prática.
// A medição AUTORITATIVA de performance é o BenchmarkMediate (ns/op, imune à
// granularidade do relógio); este teste é apenas um tripwire barato. Quando o
// relógio monotónico não resolve latências sub-µs, reporta honestamente a média
// agregada como limite superior conservador em vez de um p95 espúrio.
func TestMediate_P95Overhead(t *testing.T) {
	t.Parallel()
	m := New() // stubs neutros + discardSink (política em memória)
	_ = m.Register("noop", func(context.Context, []byte) ([]byte, error) { return nil, nil })
	call := Call{RunID: "r", StepID: "s", ToolID: "noop", Capability: "cap:noop"}
	ctx := context.Background()

	const n = 20000
	lat := make([]time.Duration, n)
	// Aquecimento.
	for i := 0; i < 200; i++ {
		_, _ = m.Mediate(ctx, call)
	}
	// Medição per-call (para p95) e agregada (para uma média fiável, imune à
	// granularidade grosseira do relógio monotónico em alguns hosts).
	aggStart := time.Now()
	for i := 0; i < n; i++ {
		d, _ := m.Mediate(ctx, call)
		lat[i] = d.Latency
	}
	mean := time.Since(aggStart) / n

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p95 := lat[int(float64(n)*0.95)]
	p50 := lat[n/2]
	// p95 efectivo reportado: usa a medição per-call quando o relógio a resolve,
	// senão recorre à média agregada (limite superior conservador a esta escala).
	effP95 := p95
	if effP95 == 0 {
		effP95 = mean
	}
	t.Logf("overhead de mediacao em memoria: p50=%v p95=%v media-agregada=%v p95-efectivo=%v (n=%d)",
		p50, p95, mean, effP95, n)
	if effP95 >= 15*time.Millisecond {
		t.Fatalf("overhead p95=%v excede o alvo de 15 ms", effP95)
	}
}

func BenchmarkMediate(b *testing.B) {
	m := New()
	_ = m.Register("noop", func(context.Context, []byte) ([]byte, error) { return nil, nil })
	call := Call{RunID: "r", StepID: "s", ToolID: "noop", Capability: "cap:noop"}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, _ := m.Mediate(ctx, call)
		if d.Effect != EffectPermit {
			b.Fatalf("esperava EffectPermit, obtive %q", d.Effect)
		}
	}
}
