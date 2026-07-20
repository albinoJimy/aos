package testkit

import (
	"context"
	"sync"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

// ===========================================================================
// Reference Monitor (RM) — mocks de referência, alinhados ao _BRIEF §2
// {Monitor.Mediate, Register, Hook{Name,Evaluate}, EventSink{RecordMediation},
//  Call, Decision, Effect}. Consolida os fakes antes dispersos e não-exportados
// (fakeSink/spyHook/toolSpy/baseCall em reference-monitor/*_test.go).
// ===========================================================================

// FakeEventSink é o [rm.EventSink] de referência em memória, com falha injectável.
// Grava cada [rm.MediationRecord] que o RM lhe submete (permit E deny/escalate),
// atribui um seq monotónico e devolve-o — replicando a semântica do sink durável
// sem I/O. Seguro para uso concorrente (-race).
//
// A falha injectável ([FakeEventSink.FailWith] / [FakeEventSink.FailOnEffect])
// exercita o fail-closed de auditoria do RM: no caminho de permit, um erro do sink
// degrada a decisão para deny (ver referencemonitor.Monitor.Mediate).
type FakeEventSink struct {
	mu         sync.Mutex
	records    []rm.MediationRecord
	fail       error     // se != nil, RecordMediation falha
	failEffect rm.Effect // se != "", falha só para este Effect
	seq        uint64
}

// NewFakeEventSink constrói um sink vazio e funcional (sem falhas injectadas).
func NewFakeEventSink() *FakeEventSink { return &FakeEventSink{} }

// RecordMediation implementa [rm.EventSink].
func (f *FakeEventSink) RecordMediation(_ context.Context, rec rm.MediationRecord) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil && (f.failEffect == "" || rec.Effect == f.failEffect) {
		return 0, f.fail
	}
	f.seq++
	f.records = append(f.records, rec)
	return f.seq, nil
}

// FailWith faz TODAS as gravações subsequentes falharem com err (fail-closed).
func (f *FakeEventSink) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
	f.failEffect = ""
}

// FailOnEffect faz falhar apenas as gravações do Effect dado — ex.: falhar só o
// permit para provar que uma acção não-auditável não é despachada.
func (f *FakeEventSink) FailOnEffect(eff rm.Effect, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
	f.failEffect = eff
}

// Records devolve uma cópia dos registos gravados até agora.
func (f *FakeEventSink) Records() []rm.MediationRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]rm.MediationRecord, len(f.records))
	copy(out, f.records)
	return out
}

// Count devolve o número de registos gravados.
func (f *FakeEventSink) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

// ===========================================================================
// SpyHook — hook plugável de referência para a cadeia de mediação
// ===========================================================================

// SpyHook é um [rm.Hook] de referência: devolve um veredicto configurável e
// REGISTA a sua invocação (ordem na cadeia, o call que viu). Cobre os pontos de
// decisão plugáveis (identity/policy/budget/egress/audit) sem uma implementação
// real — ex.: um DenyHook("policy") simula o PDP a negar. Consolida o spyHook antes
// não-exportado do RM.
//
// Contrato do RM: um Hook NÃO deve entrar em panic; se Panic estiver ligado, o RM
// converte-o em deny fail-closed (é essa a propriedade que se testa).
type SpyHook struct {
	HookName string         // valor devolvido por Name()
	Result   rm.HookResult  // veredicto a devolver (default: HookAllow)
	Err      error          // se != nil, Evaluate devolve-o (fail-closed no RM)
	Panic    bool           // se true, Evaluate entra em panic (fail-closed no RM)
	Mutate   func(*rm.Call) // opcional: muta o call (ex.: hook de identidade)

	mu      sync.Mutex
	order   *[]string // recorder partilhado da ordem de invocação (opcional)
	seen    rm.Call
	invoked int
}

// Name implementa [rm.Hook].
func (h *SpyHook) Name() string { return h.HookName }

// Evaluate implementa [rm.Hook]: regista a invocação, muta o call se configurado,
// e devolve o veredicto/erro/panic programado.
func (h *SpyHook) Evaluate(_ context.Context, call *rm.Call) (rm.HookResult, error) {
	h.mu.Lock()
	h.invoked++
	if h.order != nil {
		*h.order = append(*h.order, h.HookName)
	}
	if h.Mutate != nil {
		h.Mutate(call)
	}
	h.seen = *call
	h.mu.Unlock()

	if h.Panic {
		panic("testkit.SpyHook: panic proposital de " + h.HookName)
	}
	return h.Result, h.Err
}

// Invocations devolve quantas vezes o hook foi avaliado.
func (h *SpyHook) Invocations() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.invoked
}

// LastCall devolve a última cópia do call que o hook observou.
func (h *SpyHook) LastCall() rm.Call {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seen
}

// HookRecorder é um registador partilhado da ORDEM de invocação de vários
// [SpyHook] na mesma cadeia — prova que a mediação corre pela ordem fornecida.
type HookRecorder struct {
	mu    sync.Mutex
	order []string
}

// NewHookRecorder constrói um registador de ordem vazio.
func NewHookRecorder() *HookRecorder { return &HookRecorder{} }

// Order devolve uma cópia da ordem de invocação registada.
func (r *HookRecorder) Order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Hook constrói um [SpyHook] ligado a este registador, com o veredicto dado.
func (r *HookRecorder) Hook(name string, result rm.HookResult) *SpyHook {
	return &SpyHook{HookName: name, Result: result, order: &r.order}
}

// AllowHook devolve um [SpyHook] neutro (permite; a cadeia prossegue).
func AllowHook(name string) *SpyHook {
	return &SpyHook{HookName: name, Result: rm.HookResult{Decision: rm.HookAllow}}
}

// DenyHook devolve um [SpyHook] que NEGA (mediação termina em deny, fail-closed) —
// o duplo canónico do PDP a recusar por política.
func DenyHook(name, reason string) *SpyHook {
	return &SpyHook{HookName: name, Result: rm.HookResult{Decision: rm.HookDeny, Reason: reason}}
}

// EscalateHook devolve um [SpyHook] que ESCALA a gate humano (ADR-013).
func EscalateHook(name, reason string) *SpyHook {
	return &SpyHook{HookName: name, Result: rm.HookResult{Decision: rm.HookEscalate, Reason: reason}}
}

// ===========================================================================
// ToolSpy — ToolFunc de referência que sinaliza execução e captura o input
// ===========================================================================

// ToolSpy é uma [rm.ToolFunc] de referência que regista se — e com que input — foi
// despachada. Como o dispatch só corre sob um permit válido, ToolSpy.Called == true
// prova que a mediação PERMITIU e despachou (e == false prova o não-despacho num
// deny). Seguro para uso concorrente.
type ToolSpy struct {
	mu     sync.Mutex
	called int
	inputs [][]byte
	out    []byte
	err    error
}

// NewToolSpy constrói uma tool que, ao ser despachada, devolve (out, err).
func NewToolSpy(out []byte, err error) *ToolSpy {
	return &ToolSpy{out: out, err: err}
}

// Func devolve a [rm.ToolFunc] a registar no Monitor via Register.
func (s *ToolSpy) Func() rm.ToolFunc {
	return func(_ context.Context, input []byte) ([]byte, error) {
		s.mu.Lock()
		s.called++
		s.inputs = append(s.inputs, append([]byte(nil), input...))
		out, err := s.out, s.err
		s.mu.Unlock()
		return out, err
	}
}

// Called indica se a tool foi despachada pelo menos uma vez.
func (s *ToolSpy) Called() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called > 0
}

// Calls devolve o número de despachos.
func (s *ToolSpy) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

// LastInput devolve o input do último despacho (nil se nunca despachada) — para
// asserir, p.ex., que uma obrigação redact_pii foi imposta ANTES do efeito.
func (s *ToolSpy) LastInput() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputs) == 0 {
		return nil
	}
	last := s.inputs[len(s.inputs)-1]
	return append([]byte(nil), last...)
}

// ===========================================================================
// Fixtures de Call + construtor de Monitor
// ===========================================================================

// BaseCall devolve um [rm.Call] canónico e válido, amigável ao caminho de permit:
// run_id/step_id das fixtures, taint "trusted", região "eu", autoridade que casa a
// capability. Consolida o baseCall antes não-exportado do RM. Ajuste os campos que
// o seu cenário exige (ex.: Context.Taint = "untrusted" para o gate de taint).
func BaseCall() rm.Call {
	return rm.Call{
		RequestID:  "req-testkit",
		RunID:      FixtureRunID,
		StepID:     FixtureStepID(1),
		ToolID:     "tool.echo",
		Capability: "cap:echo",
		Resource:   rm.Resource{Type: "url", Value: "https://api.example.com/x", Region: "eu"},
		Principal:  rm.Principal{NHIID: "nhi-testkit-1", AgentID: "agent-1", AgentClass: "worker", Authority: []string{"cap:echo"}},
		Context:    rm.CallContext{Taint: "trusted", BudgetTokensRemaining: 1000},
		Credential: "tok-testkit",
		Input:      []byte("payload"),
	}
}

// NewMonitor compõe um [rm.Monitor] pronto a mediar com um [FakeEventSink] e a
// cadeia de hooks dada; se nenhum hook for passado, usa a cadeia canónica neutra
// ([rm.DefaultHooks]). Devolve o monitor E o sink para inspecção dos registos.
// É o atalho para "quero um RM exercitável neste teste" sem boilerplate.
func NewMonitor(hooks ...rm.Hook) (*rm.Monitor, *FakeEventSink) {
	sink := NewFakeEventSink()
	opts := []rm.Option{rm.WithEventSink(sink)}
	if len(hooks) > 0 {
		opts = append(opts, rm.WithHooks(hooks...))
	}
	return rm.New(opts...), sink
}
