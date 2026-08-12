package main

// AOS-253 — CRASH-RESUME POR VARREDURA DE ARRANQUE (achados F9+F13).
//
// Estes testes trancam os critérios de aceitação AO NÍVEL DO NÓ, pela CADEIA REAL
// (obsPermitNodeWith = Bootstrap/NewSecuredRuntime com verifier, catálogo assinado, PDP e
// dispatcher durável REAIS), e NÃO com doubles — a invariante do EPIC-20 exige-o. A varredura
// [NodeService.ResumeInterruptedRuns] é a peça sob teste; o resto é a maquinaria que já existia e
// que ela LIGA (Resumer de AOS-015, lease de AOS-018, estados terminais de AOS-252, replay-then-
// continue de AOS-021).
//
//   - [TestAOS253_HostRunSeedsCrashResumeRecordAtStart] — a metade de PRODUÇÃO: o hostRun semeia o
//     registo de retoma no ARRANQUE de cada run (não só na escalada), sem o qual um crash a meio
//     não seria reconstituível (não há Goal em mais lado nenhum do log).
//   - [TestAOS253_CrashResumeScanCompletesWithoutDoubleExecution] — CA1/CA2/CA3: um run que aplicou
//     o efeito do turno 1 e "crashou" (estado `running` sem desfecho terminal) é RECLAMADO pela
//     varredura de um nó NOVO sobre o mesmo substrato, RETOMADO pelo replay-then-continue e
//     COMPLETADO — com o efeito já aplicado a NÃO repetir (dedup do step-ledger) e o modelo a NÃO
//     ser re-interrogado no turno já capturado.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// crashResumeModel emite uma tool call `counter` em cada turno ANTES de finalFrom e conclui
// (Final) a partir dele. REGISTA os turnos em que foi de facto interrogado — é essa lista que
// prova que a retoma NÃO re-interroga o modelo nos turnos já capturados (reproduzidos do plano).
type crashResumeModel struct {
	mu        sync.Mutex
	asked     []int
	finalFrom int
}

func (m *crashResumeModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.mu.Lock()
	m.asked = append(m.asked, view.Turn)
	m.mu.Unlock()
	if view.Turn >= m.finalFrom {
		return agentruntime.ModelResponse{Text: "done", Final: true, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	return agentruntime.ModelResponse{
		ToolCalls: []agentruntime.ToolInvocation{{ToolID: "counter", Capability: durCap, Input: []byte("tick")}},
		Usage:     agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

func (m *crashResumeModel) askedTurns() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.asked...)
}

// crashResumeApprovers compõe o four-eyes MÍNIMO cujo único efeito relevante aqui é fazer o
// Bootstrap compor o registo de retoma (AOS-021) — de que a varredura de AOS-253 depende para
// reconstituir o Goal. Não se aprova nada nestes testes (sem oráculo de autonomia não há escalada).
func crashResumeApprovers(t *testing.T) []ApproverConfig {
	t.Helper()
	pubA, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave A: %v", err)
	}
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave B: %v", err)
	}
	return []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}
}

// TestAOS253_HostRunSeedsCrashResumeRecordAtStart prova a metade de PRODUÇÃO de AOS-253: o
// hostRun escreve o registo de retoma no ARRANQUE de cada run hospedado — não só quando o run
// escala (AOS-021) —, e é isso que torna um crash a meio reconstituível. O registo persiste mesmo
// depois de o run COMPLETAR (dedup por RunID, nunca apagado).
func TestAOS253_HostRunSeedsCrashResumeRecordAtStart(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	vault := audit.NewInMemoryKeyVault(nil)
	model := &crashResumeModel{finalFrom: 1} // turno 1 conclui de imediato (sem tool calls)
	node, cred := obsPermitNodeWith(t, "", model, func(cfg *Config) {
		cfg.EventStore = store
		cfg.DSARVault = vault
		cfg.DurableExecution = true
		cfg.Approvers = crashResumeApprovers(t)
	})
	t.Cleanup(func() { _ = node.Close() })
	if node.ResumeRecords == nil {
		t.Fatal("ResumeRecords devia estar composto (four-eyes pinado) — sem ele a semente de AOS-253 e um no-op")
	}

	svc, err := NewNodeService(node, WithDeadlineSweepInterval(0), WithServiceLog(io.Discard))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() {
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(sc)
	})

	const runID = "run-253-seed"
	if err := svc.Submit(ctx, agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: cred,
		Objective:  "prova de semente de retoma no arranque",
		MaxTurns:   4,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	wc, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(wc, runID); werr != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, werr)
	}

	rec, ok, err := node.ResumeRecords.Get(ctx, runID)
	if err != nil {
		t.Fatalf("ResumeRecords.Get: %v", err)
	}
	if !ok {
		t.Fatal("o hostRun devia ter SEMEADO o registo de retoma no arranque (AOS-253) — nenhum foi encontrado; um crash a meio deste run seria irretomavel")
	}
	if rec.RunID != runID || rec.Principal.NHIID != durAgent {
		t.Fatalf("o registo de retoma nao corresponde ao run hospedado: %+v", rec)
	}
}

// TestAOS253_CrashResumeScanCompletesWithoutDoubleExecution é a prova central (CA1/CA2/CA3):
// kill a meio → restart → o run COMPLETA, sem double-execution e sem re-interrogar o modelo nos
// turnos já capturados.
func TestAOS253_CrashResumeScanCompletesWithoutDoubleExecution(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0") // isola: sem o disjuntor a interferir com o desfecho
	ctx := context.Background()

	// SUBSTRATO PARTILHADO entre as duas incarnacoes do no — simula o restart sobre o MESMO Event
	// Store e a MESMA custodia de KEK. Sem a KEK partilhada, a incarnacao NOVA nao decifraria as
	// capturas nem o registo de retoma selados por-titular pela incarnacao antiga (AOS-093).
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	vault := audit.NewInMemoryKeyVault(nil)
	approvers := crashResumeApprovers(t)
	shared := func(cfg *Config) {
		cfg.EventStore = store
		cfg.DSARVault = vault
		cfg.DurableExecution = true
		cfg.Approvers = approvers
	}

	const runID = "run-253-crash"
	// EFEITO PARTILHADO pelas tools das duas incarnacoes: se a varredura RE-executasse o efeito,
	// este contador iria a 2. Fica em 1 <=> o dedup do step-ledger impediu a repeticao.
	var counter int64

	// ===================== INCARNACAO 1: aplica o efeito do turno 1 e "crasha" =====================
	model1 := &crashResumeModel{finalFrom: 99} // turno 1 -> tool call (nunca conclui nesta passagem)
	node1, cred1 := obsPermitNodeWith(t, "", model1, shared)
	if err := node1.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(&counter, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter) na incarnacao 1: %v", err)
	}

	goal := agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: cred1,
		Model:      agentruntime.ModelConfig{ModelID: "model:253"},
		Objective:  "crash-resume por varredura de arranque",
		MaxTurns:   4, // o registo de retoma leva o tecto REAL (a continuacao ao vivo precisa dele)
	}
	// A passagem de PRODUCAO das artefactos do turno 1: MaxTurns=1 aplica o turno 1 (counter uma
	// vez, capturado, checkpointado) e devolve ErrMaxTurns SEM selar — o Runtime.Run DIRECTO nao
	// materializa estado terminal (o selo vive no hostRun), pelo que representa fielmente um
	// processo que morreu a meio. O estado `running` e semeado a seguir (o claim que um hostRun
	// teria feito), fechando o rasto exacto de um crash (AOS-252).
	prod := goal
	prod.MaxTurns = 1
	if _, _, rerr := node1.Runtime.Run(ctx, prod, nil); rerr != nil && !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("passagem de producao do turno 1: %v", rerr)
	}
	if got := atomic.LoadInt64(&counter); got != 1 {
		t.Fatalf("o turno 1 devia ter aplicado o efeito EXACTAMENTE 1x antes do crash; counter=%d", got)
	}

	// CRASH SIMULADO: reclama a maquina de estados (ready->running) sem desfecho terminal — o
	// rasto de um processo morto a meio (identico ao crash de AOS-252) — e SEMEIA o registo de
	// retoma que o hostRun de producao teria escrito no arranque (aqui nao ha hostRun, pois a
	// passagem foi Runtime.Run directo).
	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim do crash simulado: %v", err)
	}
	if err := node1.ResumeRecords.Put(ctx, resumeRecordFromGoal(goal)); err != nil {
		t.Fatalf("semear o registo de retoma: %v", err)
	}
	_ = node1.Close() // a incarnacao 1 "morre"; o store partilhado sobrevive (nao e propriedade do no)

	// ===================== INCARNACAO 2: arranque NOVO; a varredura retoma =====================
	model2 := &crashResumeModel{finalFrom: 2} // turno 1 reproduzido do plano (nao chamado); turno 2 -> Final
	node2, _ := obsPermitNodeWith(t, "", model2, shared)
	t.Cleanup(func() { _ = node2.Close() })
	if err := node2.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(&counter, 1) // se a retoma re-executasse o efeito ja aplicado, iria a 2
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter) na incarnacao 2: %v", err)
	}

	svc2, err := NewNodeService(node2, WithDeadlineSweepInterval(0), WithServiceLog(io.Discard))
	if err != nil {
		t.Fatalf("NewNodeService na incarnacao 2: %v", err)
	}
	t.Cleanup(func() {
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc2.Shutdown(sc)
	})

	// A VARREDURA DE ARRANQUE — a peca sob teste, pela cadeia REAL.
	scanned, resumed, err := svc2.ResumeInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("ResumeInterruptedRuns: %v", err)
	}
	if scanned != 1 || resumed != 1 {
		t.Fatalf("a varredura devia VER 1 orfao e RETOMAR 1; scanned=%d resumed=%d", scanned, resumed)
	}

	// O run RETOMADO prossegue pela continuacao AO VIVO (turno 2 Final) e COMPLETA.
	wc, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	oc, ok, werr := svc2.Wait(wc, runID)
	if werr != nil || !ok {
		t.Fatalf("Wait do run retomado: ok=%v err=%v", ok, werr)
	}
	if oc.Err != nil || !oc.Result.Terminated {
		t.Fatalf("o run retomado devia COMPLETAR; result=%+v err=%v", oc.Result, oc.Err)
	}

	// --- AS ASSERCOES QUE IMPORTAM (CA2/CA3) ---
	// (a) SEM DOUBLE-EXECUTION: o efeito ja aplicado do turno 1 NAO voltou a correr na retoma.
	if got := atomic.LoadInt64(&counter); got != 1 {
		t.Fatalf("PROPRIEDADE CENTRAL (AOS-253): o efeito ja aplicado NAO pode repetir na retoma; counter=%d (esperado 1)", got)
	}
	// (b) O modelo NAO foi re-interrogado no turno 1 (ja capturado — reproduzido do plano de replay).
	for _, tn := range model2.askedTurns() {
		if tn == 1 {
			t.Fatalf("o modelo foi re-interrogado no turno 1 (ja capturado); a retoma devia reproduzi-lo do plano — asked=%v", model2.askedTurns())
		}
	}
	// (c) A continuacao AO VIVO aconteceu de facto: o turno 2 foi interrogado ao modelo.
	sawTurn2 := false
	for _, tn := range model2.askedTurns() {
		if tn == 2 {
			sawTurn2 = true
		}
	}
	if !sawTurn2 {
		t.Fatalf("a continuacao ao vivo devia interrogar o modelo no turno 2; asked=%v", model2.askedTurns())
	}
}

// TestAOS253_CrashResumeBannerDeclaresResult tranca o AC4: o banner declara o RESULTADO da
// varredura (N runs retomados) e a postura DESLIGADA quando o substrato falta — funções puras,
// como as restantes linhas de banner do nó.
func TestAOS253_CrashResumeBannerDeclaresResult(t *testing.T) {
	got := crashResumeBanner(7, 2, 1, 1, 0)
	for _, want := range []string{"7 stream", "1 RETOMADO", "LEASE VIVO", "AOS-253", "Resumer (AOS-015)"} {
		if !contains(got, want) {
			t.Fatalf("o banner de resultado devia declarar %q; veio: %q", want, got)
		}
	}
	off := crashResumeDisabledBanner()
	for _, want := range []string{"DESLIGADA", "recomecaria do turno 1"} {
		if !contains(off, want) {
			t.Fatalf("o banner desligado devia declarar %q; veio: %q", want, off)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
