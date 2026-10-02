package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-485 — a recusa de uma tool call pela lista-branca do run sai do Reference Monitor.
//
// Medido em produção a 2026-10-02 (run `plan-e2e-pegadas-1790956072~n2_summarize`, lista vazia):
// o turno pediu `doc_read`, a call foi negada, e do lado do registo não ficou nada — nenhum
// `tool.call.*` no stream, nenhuma partição de decisão no WORM, `aos_mediation_denials_total 0`.
// A recusa acontecia no ciclo do runtime, antes do RM.
//
// O primeiro teste corre pelo composition-root ([NewSecuredRuntime] → sec.Run), com a cadeia
// real, o Event Store e o WORM — a mesma preparação do AOS-379, em que a call `doc_read` é
// legítima e PASSA quando não há lista. Corre nas DUAS vias de despacho: a directa e a durável
// (`cfg.Ledger`), que é a combinação de produção (AOS_DURABLE_EXECUTION=1).

// aos485Corrida é o que se mede de um run pelo nó composto.
type aos485Corrida struct {
	execucoes       int
	eventos         []eventstore.Event
	selos           []audit.AuditRecord
	permits         uint64
	denials         uint64
	selosDeRevalida uint64 // registos que ESTE run acrescentou à partição da revalidação
}

// aos485Correr corre o goal do AOS-379 com a lista-branca dada. O gravador de turnos e o canal
// de mediação partilham o MESMO Event Store, como no nó: o stream do run é um só.
func aos485Correr(t *testing.T, duravel bool, permitidas []string, declarado *referencemonitor.Principal) (aos485Corrida, agentruntime.Goal) {
	t.Helper()
	ctx := context.Background()
	cfg, _, _, goal, cleanup := aos379PermitConfig(t)
	defer cleanup()

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	cfg.Recorder = agentruntime.NewTurnRecorder(store)
	cfg.MediationEvents = store
	if duravel {
		// A via de produção: o loop despacha pelo DurableDispatcher sobre o step-ledger.
		if cfg.Ledger, err = durable.NewStepLedger(store); err != nil {
			t.Fatalf("NewStepLedger: %v", err)
		}
	}

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	var c aos485Corrida
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		c.execucoes++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	revalAntes, err := cfg.WORM.Head(ctx, revalidation.DefaultPartition)
	if err != nil {
		t.Fatalf("Head(revalidação): %v", err)
	}

	goal.AllowedTools = permitidas
	if declarado != nil {
		goal.Principal = *declarado
	}
	if _, _, err := sec.Run(ctx, goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if c.eventos, err = store.Read(ctx, goal.RunID, 1); err != nil {
		t.Fatalf("Read(%q): %v", goal.RunID, err)
	}
	head, err := cfg.WORM.Head(ctx, goal.RunID)
	if err != nil {
		t.Fatalf("Head(%q): %v", goal.RunID, err)
	}
	if head > 0 {
		if c.selos, err = cfg.WORM.Read(ctx, goal.RunID, 1, head); err != nil {
			t.Fatalf("WORM.Read(%q): %v", goal.RunID, err)
		}
	}
	revalDepois, err := cfg.WORM.Head(ctx, revalidation.DefaultPartition)
	if err != nil {
		t.Fatalf("Head(revalidação): %v", err)
	}
	c.selosDeRevalida = revalDepois - revalAntes
	c.permits, c.denials, _ = sec.Metrics().Snapshot()
	return c, goal
}

func TestAOS485_RecusaPelaListaFicaNoEventStoreNoWORMENoContador(t *testing.T) {
	for nome, duravel := range map[string]bool{"via directa": false, "via duravel": true} {
		t.Run(nome, func(t *testing.T) { aos485RecusaNoNoComposto(t, duravel) })
	}
}

func aos485RecusaNoNoComposto(t *testing.T, duravel bool) {
	t.Helper()
	// O principal que o run DECLARA não tem cadeia de delegação nem classe: se o evento os
	// levar, vieram do token VERIFICADO — a recusa sai depois do hook de identidade.
	declarado := referencemonitor.Principal{NHIID: "agt-1", AgentID: "agt-1"}
	c, goal := aos485Correr(t, duravel, []string{}, &declarado)

	if c.execucoes != 0 {
		t.Fatalf("a tool EXECUTOU %d vez(es) com a lista-branca vazia", c.execucoes)
	}
	if c.permits != 0 || c.denials != 1 {
		t.Fatalf("contadores do RM: permits=%d denials=%d, quero 0 e 1", c.permits, c.denials)
	}

	// --- Event Store: um tool.call.denied, e nada que diga que algo correu.
	turno1 := ""
	var negado *eventstore.Event
	for i := range c.eventos {
		ev := c.eventos[i]
		switch {
		case ev.Type == agentruntime.EventTypeTurnRecorded && turno1 == "":
			turno1 = ev.StepID
		case ev.Type == referencemonitor.EventTypeDenied:
			if negado != nil {
				t.Fatalf("mais do que um tool.call.denied no stream: %+v", c.eventos)
			}
			negado = &c.eventos[i]
		case ev.Type == referencemonitor.EventTypeMediated, ev.Type == referencemonitor.EventTypeOutcome,
			ev.Type == referencemonitor.EventTypeEscalated, ev.Type == durable.EventTypeLedgerApplied, strings.HasPrefix(ev.Type, "sandbox."):
			t.Fatalf("o stream tem um %s: nada podia ter sido autorizado, despachado, memorizado nem lançado em sandbox", ev.Type)
		}
	}
	if turno1 == "" || negado == nil {
		t.Fatalf("queria o turn.recorded do turno 1 e um tool.call.denied; tipos=%v", aos485Tipos(c.eventos))
	}
	if negado.StepID != turno1+"-tool-1" || negado.ParentStepID != turno1 {
		t.Fatalf("step_id=%q parent_step_id=%q, quero %q e %q", negado.StepID, negado.ParentStepID, turno1+"-tool-1", turno1)
	}
	var p struct {
		Decision  string `json:"decision"`
		Code      string `json:"code"`
		DeniedBy  string `json:"denied_by"`
		ToolID    string `json:"tool_id"`
		Principal struct {
			NHIID           string `json:"nhi_id"`
			AgentClass      string `json:"agent_class"`
			DelegationChain []struct {
				Sub   string `json:"sub"`
				ActAs string `json:"act_as"`
			} `json:"delegation_chain"`
		} `json:"principal"`
	}
	if err := json.Unmarshal(negado.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.Decision != "deny" || p.Code != "E_TOOL_OUTSIDE_RUN_ALLOWLIST" || p.DeniedBy != "run_tool_allowlist" || p.ToolID != "doc_read" {
		t.Fatalf("payload da recusa: %s", negado.Payload)
	}
	if p.Principal.NHIID != "agt-1" || p.Principal.AgentClass != "agent-worker" ||
		len(p.Principal.DelegationChain) == 0 || p.Principal.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("o principal da recusa tinha de ser o do token verificado, com a cadeia de delegação: %s", negado.Payload)
	}
	if len(negado.Producer.DelegationChain) == 0 || negado.Producer.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("o envelope do evento não identifica a cadeia de delegação: %+v", negado.Producer)
	}

	// --- WORM: um selo deny na partição do run, com o código, o denied_by e o principal.
	if len(c.selos) != 1 {
		t.Fatalf("queria 1 selo na partição %q do WORM, vieram %d", goal.RunID, len(c.selos))
	}
	selo := c.selos[0]
	if selo.Decision != audit.DecisionDeny || selo.Code != "E_TOOL_OUTSIDE_RUN_ALLOWLIST" || selo.DeniedBy != "run_tool_allowlist" {
		t.Fatalf("selo da recusa: decision=%v code=%q denied_by=%q", selo.Decision, selo.Code, selo.DeniedBy)
	}
	if selo.RunID != goal.RunID || selo.StepID != turno1+"-tool-1" || selo.ParentStepID != turno1 || selo.ToolID != "doc_read" {
		t.Fatalf("o selo não está ligado à call: %+v", selo)
	}
	if selo.Principal.NHIID != "agt-1" || len(selo.Principal.DelegationChain) == 0 || selo.Principal.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("o selo não leva o principal verificado: %+v", selo.Principal)
	}

	// --- Posição na cadeia: a recusa vem ANTES da revalidação, que não chega a selar.
	if c.selosDeRevalida != 0 {
		t.Fatalf("a call negada pela lista selou %d revalidação(ões): o gate tem de vir antes da revalidação", c.selosDeRevalida)
	}
}

// CONTROLO do teste acima: sem lista, a MESMA call passa a cadeia inteira — executa, sela a
// revalidação e grava o `mediated`. Sem isto, «não executou» e «não selou revalidação»
// passariam num nó onde a call nunca passa.
func TestAOS485_SemListaAMesmaCallPassaACadeiaInteira(t *testing.T) {
	for nome, duravel := range map[string]bool{"via directa": false, "via duravel": true} {
		t.Run(nome, func(t *testing.T) { aos485SemLista(t, duravel) })
	}
}

func aos485SemLista(t *testing.T, duravel bool) {
	t.Helper()
	c, _ := aos485Correr(t, duravel, nil, nil)
	if c.execucoes != 1 || c.permits != 1 || c.denials != 0 {
		t.Fatalf("sem lista a call tinha de executar: execucoes=%d permits=%d denials=%d", c.execucoes, c.permits, c.denials)
	}
	if c.selosDeRevalida == 0 {
		t.Fatal("controlo: a call permitida tinha de selar a revalidação — o teste da recusa mediria uma partição que ninguém escreve")
	}
	tipos := aos485Tipos(c.eventos)
	if !strings.Contains(strings.Join(tipos, ","), referencemonitor.EventTypeMediated) {
		t.Fatalf("controlo: queria um tool.call.mediated; tipos=%v", tipos)
	}
}

// E com a tool NA lista, o gate deixa passar: a lista estreita, não fecha.
func TestAOS485_ToolNaListaPassaOGate(t *testing.T) {
	for nome, duravel := range map[string]bool{"via directa": false, "via duravel": true} {
		c, _ := aos485Correr(t, duravel, []string{"doc_read"}, nil)
		if c.execucoes != 1 || c.permits != 1 || c.denials != 0 {
			t.Fatalf("%s: com a tool na lista a call tinha de executar: execucoes=%d permits=%d denials=%d", nome, c.execucoes, c.permits, c.denials)
		}
	}
}

func aos485Tipos(evs []eventstore.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// ---------------------------------------------------------------------------
// Paridade via directa / via durável
// ---------------------------------------------------------------------------

// aos485Negacao é o que se compara entre as duas vias: a recusa tal como ficou no evento.
type aos485Negacao struct {
	StepID, ParentStepID, Code, DeniedBy, ToolID string
}

// aos485PorVia corre o goal das portas com a lista dada, pela via directa ou pela durável, e
// devolve as recusas gravadas, os `mediated` e as execuções da tool. O RM é o de
// [referencemonitor.DefaultHooks] — sem o gate: quem nega é o backstop, o que prova de caminho
// que a paridade não depende da composição.
func aos485PorVia(t *testing.T, permitidas []string, duravel bool) (negacoes []aos485Negacao, mediadas, execucoes int) {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execucoes++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var opts []agentruntime.Option
	if duravel {
		opts = append(opts, agentruntime.WithActivityDispatcher(newDurableDispatcher(t, store, rm)))
	}
	g := portTestGoal()
	g.AllowedTools = permitidas
	if _, err := agentruntime.New(portModel(nil), rm, agentruntime.NewTurnRecorder(store), opts...).Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs, err := store.Read(context.Background(), g.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, ev := range evs {
		switch ev.Type {
		case referencemonitor.EventTypeMediated:
			mediadas++
		case referencemonitor.EventTypeDenied:
			var p struct {
				Code     string `json:"code"`
				DeniedBy string `json:"denied_by"`
				ToolID   string `json:"tool_id"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("payload: %v", err)
			}
			negacoes = append(negacoes, aos485Negacao{ev.StepID, ev.ParentStepID, p.Code, p.DeniedBy, p.ToolID})
		}
	}
	return negacoes, mediadas, execucoes
}

// TestAOS485_ListaVaziaChegaIgualPelasDuasVias é a paridade com `[]`: a lista PRESENTE e vazia
// nega pela via directa e pela durável, com o mesmo evento. Se a tradução Call → Activity → Call
// a transformasse em ausente, a via durável — a de produção — executava a tool.
func TestAOS485_ListaVaziaChegaIgualPelasDuasVias(t *testing.T) {
	directo, medD, execD := aos485PorVia(t, []string{}, false)
	duravel, medV, execV := aos485PorVia(t, []string{}, true)

	if len(directo) != 1 || directo[0].Code != "E_TOOL_OUTSIDE_RUN_ALLOWLIST" || directo[0].DeniedBy != "run_tool_allowlist" ||
		directo[0].ParentStepID == "" || directo[0].StepID != directo[0].ParentStepID+"-tool-1" {
		// Sem isto o teste passaria com as duas vias vazias — igualdade sem nada para comparar.
		t.Fatalf("via directa: esperava 1 recusa pela lista, com step_id e parent_step_id; vieram %+v", directo)
	}
	if !reflect.DeepEqual(duravel, directo) {
		t.Fatalf("via durável: %+v, via directa: %+v — a lista vazia não chegou igual", duravel, directo)
	}
	if execD != 0 || execV != 0 || medD != 0 || medV != 0 {
		t.Fatalf("com a lista vazia nada podia executar: directa exec=%d mediated=%d, durável exec=%d mediated=%d", execD, medD, execV, medV)
	}
}

// CONTROLO da paridade: sem lista (nil) e com a tool na lista, as duas vias permitem. É o que
// impede o teste acima de passar com uma via durável que negasse tudo.
func TestAOS485_SemListaEComAToolNaListaAsDuasViasPermitem(t *testing.T) {
	for nome, permitidas := range map[string][]string{"nil": nil, "tool na lista": {"echo"}} {
		for _, duravel := range []bool{false, true} {
			neg, med, exec := aos485PorVia(t, permitidas, duravel)
			if len(neg) != 0 || med != 1 || exec != 1 {
				t.Fatalf("%s (durável=%v): negacoes=%+v mediated=%d exec=%d, quero 0/1/1", nome, duravel, neg, med, exec)
			}
		}
	}
}

// TestAOS485_DurableDispatcherNaoTransformaVaziaEmNil mede a tradução no sítio exacto: o Call
// que o RM recebe da via durável tem a lista NÃO-nil e vazia, tal como foi despachada.
func TestAOS485_DurableDispatcherNaoTransformaVaziaEmNil(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rec := &callRecorder{}
	rm := referencemonitor.New(referencemonitor.WithHooks(rec), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))

	call := referencemonitor.Call{RunID: "run-485-vazia", StepID: "s1-tool-1", ParentStepID: "s1", ToolID: "echo", AllowedTools: []string{}}
	dec, err := newDurableDispatcher(t, store, rm).Dispatch(context.Background(), call)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("esperava 1 mediação, vieram %d", len(rec.calls))
	}
	if got := rec.calls[0].AllowedTools; got == nil || len(got) != 0 {
		t.Fatalf("a lista vazia chegou ao RM como %#v — nil abria todas as tools", got)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.Code != referencemonitor.CodeToolOutsideRunAllowlist || dec.DeniedBy != referencemonitor.RunAllowlistHookName {
		t.Fatalf("a via durável tinha de devolver a recusa da lista: effect=%s code=%s denied_by=%s", dec.Effect, dec.Code, dec.DeniedBy)
	}
}

// ---------------------------------------------------------------------------
// O dedup do step-ledger não pode responder por uma call fora da lista
// ---------------------------------------------------------------------------

// aos485Vistas guarda o prompt materializado de cada turno: o turno 1 pede `echo`, o 2 conclui.
type aos485Vistas struct {
	vistas [][]byte
}

func (m *aos485Vistas) Call(_ context.Context, pv agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.vistas = append(m.vistas, append([]byte(nil), pv.Materialized...))
	if len(m.vistas) == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}},
		}, nil
	}
	return agentruntime.ModelResponse{Final: true, Text: "fim"}, nil
}

// TestAOS485_PassoJaAplicadoNaoRespondePorUmaCallForaDaLista reproduz a sonda da revisão
// adversarial, pelo ciclo: o MESMO run corre duas vezes sobre o mesmo step-ledger. À primeira,
// sem lista, a tool executa e o output fica memorizado. À segunda, com a lista VAZIA, o mesmo
// passo pede a mesma call. Na via durável o already-applied do ledger corre ANTES do RM e a
// impressão da acção não tem a lista: sem a guarda do dispatcher, a segunda corrida recebia o
// output memorizado — um permit sem mediação, que o modelo via.
func TestAOS485_PassoJaAplicadoNaoRespondePorUmaCallForaDaLista(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	execucoes := 0
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execucoes++
		return append([]byte("SEGREDO:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dd := newDurableDispatcher(t, store, rm) // UM ledger para as duas corridas

	correr := func(permitidas []string) *aos485Vistas {
		m := &aos485Vistas{}
		g := portTestGoal()
		g.AllowedTools = permitidas
		rt := agentruntime.New(m, rm, agentruntime.NewTurnRecorder(store), agentruntime.WithActivityDispatcher(dd))
		if _, err := rt.Run(context.Background(), g); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return m
	}

	primeira := correr(nil)
	if execucoes != 1 || len(primeira.vistas) < 2 || !bytes.Contains(primeira.vistas[1], []byte("SEGREDO:x")) {
		t.Fatalf("preparação: sem lista a tool tinha de executar e o modelo ver o output; execucoes=%d", execucoes)
	}
	_, negadasAntes, _ := rm.Metrics().Snapshot()

	segunda := correr([]string{})
	if execucoes != 1 {
		t.Fatalf("a tool executou outra vez (%d)", execucoes)
	}
	if len(segunda.vistas) < 2 {
		t.Fatalf("a segunda corrida devia ter dois turnos, teve %d", len(segunda.vistas))
	}
	if bytes.Contains(segunda.vistas[1], []byte("SEGREDO")) {
		t.Fatalf("o output MEMORIZADO chegou ao modelo numa corrida com a lista vazia: %q", segunda.vistas[1])
	}
	if !bytes.Contains(segunda.vistas[1], []byte("denied_code=E_TOOL_OUTSIDE_RUN_ALLOWLIST")) {
		t.Fatalf("o modelo tinha de ver a negação pela lista: %q", segunda.vistas[1])
	}
	if _, negadas, _ := rm.Metrics().Snapshot(); negadas != negadasAntes+1 {
		t.Fatalf("a recusa tinha de passar pelo RM: denials %d→%d", negadasAntes, negadas)
	}
}
