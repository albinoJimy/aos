package referencemonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-485 — a lista-branca do run é imposta DENTRO do Reference Monitor.
//
// Até aqui o Agent Runtime negava a call antes de a entregar ao RM: a tool não corria, mas a
// recusa não deixava evento, selo nem contador. Medido em produção a 2026-10-02: um turno com
// `tool_calls_requested=1`, nenhum `tool.call.*` com esse pai, e `aos_mediation_denials_total 0`.
// ---------------------------------------------------------------------------

// hookQueConta permite sempre e conta as vezes que foi avaliado. Posto DEPOIS do gate, prova
// que a recusa pára a cadeia ali.
type hookQueConta struct{ vezes *int }

func (hookQueConta) Name() string { return "depois-do-gate" }
func (h hookQueConta) Evaluate(context.Context, *Call) (HookResult, error) {
	*h.vezes++
	return HookResult{Decision: HookAllow}, nil
}

// aos485Monitor monta um Monitor com a cadeia dada e a tool "echo" registada, e devolve o
// contador das suas execuções.
func aos485Monitor(t *testing.T, sink EventSink, hooks ...Hook) (*Monitor, *int) {
	t.Helper()
	opts := []Option{WithEventSink(sink)}
	if hooks != nil {
		opts = append(opts, WithHooks(hooks...))
	}
	m := New(opts...)
	execucoes := 0
	if err := m.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execucoes++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, &execucoes
}

func aos485Call(permitidas []string) Call {
	return Call{
		RunID: "run-485", StepID: "s1-tool-1", ParentStepID: "s1",
		ToolID: "echo", Capability: "cap:echo",
		Principal: Principal{
			NHIID: "nhi:agt", AgentID: "agt", AgentClass: "worker",
			DelegationChain: []DelegationHop{{Sub: "human:alice", ActAs: "agt"}},
		},
		Input:        []byte("x"),
		AllowedTools: permitidas,
	}
}

// O gate nega com o código e o `denied_by` da lista, pára a cadeia e deixa o registo.
func TestAOS485_GateNegaComOCodigoEODeniedByDaLista(t *testing.T) {
	sink := &sinkQueGuarda{}
	depois := 0
	m, execucoes := aos485Monitor(t, sink, IdentityStub{}, NewRunAllowlistGate(), hookQueConta{vezes: &depois})

	dec, err := m.Mediate(context.Background(), aos485Call([]string{"outra"}))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectDeny || dec.Code != CodeToolOutsideRunAllowlist || dec.DeniedBy != RunAllowlistHookName {
		t.Fatalf("queria deny %s por %s; veio effect=%s code=%s denied_by=%s",
			CodeToolOutsideRunAllowlist, RunAllowlistHookName, dec.Effect, dec.Code, dec.DeniedBy)
	}
	if *execucoes != 0 {
		t.Fatalf("a tool fora da lista EXECUTOU %d vez(es)", *execucoes)
	}
	if depois != 0 {
		t.Fatalf("a cadeia continuou depois do gate (%d avaliações): a recusa tinha de a parar ali", depois)
	}
	if sink.ultimo.Effect != EffectDeny || sink.ultimo.Code != CodeToolOutsideRunAllowlist || sink.ultimo.DeniedBy != RunAllowlistHookName {
		t.Fatalf("o registo da recusa não leva o código/denied_by: %+v", sink.ultimo)
	}
	if _, negadas, _ := m.Metrics().Snapshot(); negadas != 1 {
		t.Fatalf("a recusa tinha de contar em Denials: %d", negadas)
	}
	// CONTROLO do contrato com o modelo: o texto do tail (`denied_code=`/`denied_by=`) é o do
	// AOS-413 e não pode mudar por a recusa ter mudado de sítio.
	if CodeToolOutsideRunAllowlist != "E_TOOL_OUTSIDE_RUN_ALLOWLIST" || RunAllowlistHookName != "run_tool_allowlist" {
		t.Fatalf("o código/denied_by da lista mudou: %q / %q", CodeToolOutsideRunAllowlist, RunAllowlistHookName)
	}
}

// O critério que não depende de composição: um Monitor SEM o gate nega na mesma, com o mesmo
// código e o mesmo `denied_by`, e deixa o mesmo registo.
func TestAOS485_BackstopNegaSemOGateNaCadeia(t *testing.T) {
	cadeias := map[string][]Hook{
		"DefaultHooks":    nil, // New() sem WithHooks
		"cadeia sem gate": {IdentityStub{}, PolicyStub{}},
	}
	for nome, hooks := range cadeias {
		t.Run(nome, func(t *testing.T) {
			sink := &sinkQueGuarda{}
			m, execucoes := aos485Monitor(t, sink, hooks...)
			dec, err := m.Mediate(context.Background(), aos485Call([]string{"outra"}))
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if dec.Effect != EffectDeny || dec.Code != CodeToolOutsideRunAllowlist || dec.DeniedBy != RunAllowlistHookName {
				t.Fatalf("sem o gate, o RM tinha de negar na mesma: effect=%s code=%s denied_by=%s", dec.Effect, dec.Code, dec.DeniedBy)
			}
			if *execucoes != 0 {
				t.Fatalf("a tool fora da lista EXECUTOU %d vez(es) num Monitor sem o gate", *execucoes)
			}
			if sink.ultimo.Code != CodeToolOutsideRunAllowlist || sink.ultimo.DeniedBy != RunAllowlistHookName {
				t.Fatalf("o backstop não deixou o registo da recusa: %+v", sink.ultimo)
			}
			if permitidas, negadas, _ := m.Metrics().Snapshot(); permitidas != 0 || negadas != 1 {
				t.Fatalf("contadores: permits=%d denials=%d, quero 0 e 1", permitidas, negadas)
			}
		})
	}
}

// nil não restringe, vazia nega tudo, e a tool listada passa — com o gate e só com o backstop.
// A distinção nil/vazia é a que um `len(x) == 0` desfazia.
func TestAOS485_NilNaoRestringeEVaziaNegaTudo(t *testing.T) {
	casos := []struct {
		nome       string
		permitidas []string
		quero      Effect
	}{
		{"nil: sem restricao", nil, EffectPermit},
		{"vazia: nega tudo", []string{}, EffectDeny},
		{"listada: passa", []string{"outra", "echo"}, EffectPermit},
		{"nao listada: nega", []string{"outra"}, EffectDeny},
	}
	cadeias := map[string][]Hook{
		"com gate":    {IdentityStub{}, NewRunAllowlistGate()},
		"so backstop": {IdentityStub{}},
	}
	for nomeCadeia, hooks := range cadeias {
		for _, c := range casos {
			t.Run(nomeCadeia+"/"+c.nome, func(t *testing.T) {
				m, execucoes := aos485Monitor(t, &sinkQueGuarda{}, hooks...)
				dec, err := m.Mediate(context.Background(), aos485Call(c.permitidas))
				if err != nil {
					t.Fatalf("Mediate: %v", err)
				}
				if dec.Effect != c.quero {
					t.Fatalf("lista %#v: effect=%s (code=%s), quero %s", c.permitidas, dec.Effect, dec.Code, c.quero)
				}
				queroExec := 0
				if c.quero == EffectPermit {
					queroExec = 1
				}
				if *execucoes != queroExec {
					t.Fatalf("lista %#v: a tool executou %d vez(es), quero %d", c.permitidas, *execucoes, queroExec)
				}
			})
		}
	}
	if !RunAllowsTool(nil, "qualquer") || RunAllowsTool([]string{}, "qualquer") {
		t.Fatal("RunAllowsTool: nil tem de admitir e a lista vazia tem de negar")
	}
}

// Uma tool fora da lista E não registada sai com o código da lista: é a razão mais estreita, e
// é a que o gate daria. Sem isto o código dependeria de o gate estar ou não na cadeia.
func TestAOS485_ForaDaListaPrecedeAToolNaoRegistada(t *testing.T) {
	for nome, hooks := range map[string][]Hook{"com gate": {IdentityStub{}, NewRunAllowlistGate()}, "so backstop": {IdentityStub{}}} {
		t.Run(nome, func(t *testing.T) {
			m, _ := aos485Monitor(t, &sinkQueGuarda{}, hooks...)
			call := aos485Call([]string{"echo"})
			call.ToolID = "nao-registada"
			dec, err := m.Mediate(context.Background(), call)
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if dec.Code != CodeToolOutsideRunAllowlist {
				t.Fatalf("code=%s, quero %s", dec.Code, CodeToolOutsideRunAllowlist)
			}
		})
	}
}

// A lista restringe a call, não a identifica: fica fora do fingerprint do permit e da preview
// que o humano aprova.
func TestAOS485_ListaForaDoFingerprintEDaPreview(t *testing.T) {
	sem, com := aos485Call(nil), aos485Call([]string{"echo"})
	if fingerprint(sem) != fingerprint(com) {
		t.Fatal("a lista-branca entrou no fingerprint do call")
	}
	if !bytes.Equal(ApprovalPreview(sem), ApprovalPreview(com)) {
		t.Fatal("a lista-branca entrou na ApprovalPreview")
	}
}

// [HookResult.Code] é um canal FECHADO e só vale na negação: é honrado o código da lista-branca
// e mais nenhum. Um código inventado, ou um dos códigos do próprio RM, sai com o genérico — um
// hook não pode pôr no trilho «auditoria indisponível» ou «tool não registada». Uma escalada
// sai sempre com o código de escalada.
func TestAOS485_CodigoDoHookEFechadoESoValeNaNegacao(t *testing.T) {
	casos := []struct {
		nome  string
		res   HookResult
		quero string
	}{
		{"deny com o codigo da lista", HookResult{Decision: HookDeny, Code: CodeToolOutsideRunAllowlist}, CodeToolOutsideRunAllowlist},
		{"deny com codigo inventado", HookResult{Decision: HookDeny, Code: "E_PROPRIO"}, CodeDeniedByHook},
		{"deny a vestir a auditoria", HookResult{Decision: HookDeny, Code: CodeAuditUnavailable}, CodeDeniedByHook},
		{"deny a vestir a tool nao registada", HookResult{Decision: HookDeny, Code: CodeToolNotRegistered}, CodeDeniedByHook},
		{"deny a vestir a escalada", HookResult{Decision: HookDeny, Code: CodeEscalated}, CodeDeniedByHook},
		{"deny sem codigo", HookResult{Decision: HookDeny}, CodeDeniedByHook},
		{"escalate com codigo", HookResult{Decision: HookEscalate, Code: CodeToolOutsideRunAllowlist}, CodeEscalated},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			sink := &sinkQueGuarda{}
			m := New(WithHooks(hookDeResultadoFixo{res: c.res}), WithEventSink(sink))
			dec, err := m.Mediate(context.Background(), aos485Call(nil))
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if dec.Code != c.quero {
				t.Fatalf("code=%s, quero %s", dec.Code, c.quero)
			}
			if sink.ultimo.Code != c.quero {
				t.Fatalf("o registo levou code=%s, quero %s", sink.ultimo.Code, c.quero)
			}
		})
	}
}

type hookDeResultadoFixo struct{ res HookResult }

func (hookDeResultadoFixo) Name() string { return "resultado-fixo" }
func (h hookDeResultadoFixo) Evaluate(context.Context, *Call) (HookResult, error) {
	return h.res, nil
}

// O evento, no Event Store real: `tool.call.denied` no stream do run, com o código, o
// `denied_by`, o principal com a cadeia de delegação, o `step_id` da call e o do turno.
func TestAOS485_RecusaGravaToolCallDeniedNoStreamDoRun(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	m, _ := aos485Monitor(t, NewEventStoreSink(store), IdentityStub{}, NewRunAllowlistGate())

	call := aos485Call([]string{})
	if _, err := m.Mediate(context.Background(), call); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	evs, err := store.Read(context.Background(), call.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != EventTypeDenied {
		t.Fatalf("queria 1 evento %s no stream do run, vieram %+v", EventTypeDenied, evs)
	}
	ev := evs[0]
	if ev.StepID != "s1-tool-1" || ev.ParentStepID != "s1" {
		t.Fatalf("step_id=%q parent_step_id=%q, quero s1-tool-1 e s1", ev.StepID, ev.ParentStepID)
	}
	var p struct {
		Decision  string `json:"decision"`
		Code      string `json:"code"`
		DeniedBy  string `json:"denied_by"`
		ToolID    string `json:"tool_id"`
		Principal struct {
			NHIID           string `json:"nhi_id"`
			DelegationChain []struct {
				Sub   string `json:"sub"`
				ActAs string `json:"act_as"`
			} `json:"delegation_chain"`
		} `json:"principal"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.Decision != "deny" || p.Code != CodeToolOutsideRunAllowlist || p.DeniedBy != RunAllowlistHookName || p.ToolID != "echo" {
		t.Fatalf("payload da recusa: %s", ev.Payload)
	}
	if p.Principal.NHIID != "nhi:agt" || len(p.Principal.DelegationChain) != 1 || p.Principal.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("o principal da recusa não leva a cadeia de delegação: %s", ev.Payload)
	}
}
