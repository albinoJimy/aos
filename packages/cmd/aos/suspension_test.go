package main

import (
	"context"
	"io"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// TestResumeRecord_NaoGuardaACredencial é a propriedade de segurança do registo de
// retoma: a credencial é um bearer token e este log é lido pelo read-path soberano e
// replicado. Além disso teria expirado (15 min de aprovação contra TTL de classe NHI de
// 5-15). A retoma exige credencial FRESCA.
func TestResumeRecord_NaoGuardaACredencial(t *testing.T) {
	goal := agentruntime.Goal{
		RunID:      "run-susp",
		Principal:  referencemonitor.Principal{NHIID: "agt-1", AgentClass: "agent-worker"},
		Credential: "SEGREDO-BEARER-NAO-PERSISTIR",
		Scope:      []string{"cap:http.post"},
		System:     "sistema",
		Objective:  "objectivo",
	}
	rec := resumeRecordFromGoal(goal)

	// O registo NÃO tem sequer campo para a credencial — a projecção não a transporta.
	if rec.RunID != "run-susp" || rec.Principal.NHIID != "agt-1" {
		t.Fatalf("o registo devia reconstituir o run: %+v", rec)
	}
	// E ao reconstruir o Goal, a credencial vem de FORA (fresca).
	reconstruido := rec.GoalWith("CREDENCIAL-FRESCA")
	if reconstruido.Credential != "CREDENCIAL-FRESCA" {
		t.Fatalf("a credencial da retoma tem de vir de fora; veio %q", reconstruido.Credential)
	}
	if reconstruido.Objective != goal.Objective || reconstruido.System != goal.System {
		t.Fatalf("o resto do Goal devia ser reconstituído: %+v", reconstruido)
	}
}

// TestResumeRecord_PersisteEResolve: o registo sobrevive no Event Store — é o que torna a
// retoma possível depois de o processo largar lease e goroutine.
func TestResumeRecord_PersisteEResolve(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()

	// Sem cifrador (dev): o corpo vai em claro. Em produção o nó injecta o MESMO cifrador
	// por-titular do capturer — ver a nota de privacidade em integration/resume_records.go.
	store, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	want := resumeRecordFromGoal(agentruntime.Goal{
		RunID:     "run-persist",
		Principal: referencemonitor.Principal{NHIID: "agt-9"},
		Objective: "ler o documento",
		MaxTurns:  8,
	})
	if err := store.Put(context.Background(), want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// "Restart": nova instância sobre o MESMO log.
	renascido, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords (restart): %v", err)
	}
	got, ok, err := renascido.Get(context.Background(), "run-persist")
	if err != nil || !ok {
		t.Fatalf("o registo devia sobreviver; ok=%t err=%v", ok, err)
	}
	if got.Objective != "ler o documento" || got.MaxTurns != 8 || got.Principal.NHIID != "agt-9" {
		t.Fatalf("registo não voltou intacto: %+v", got)
	}
}

// TestResumeRecord_RunDesconhecido: um run sem registo devolve (nada, false, nil).
func TestResumeRecord_RunDesconhecido(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()
	store, _ := integration.NewResumeRecords(es, nil)
	if _, ok, err := store.Get(context.Background(), "nunca-existiu"); ok || err != nil {
		t.Fatalf("run desconhecido devia dar (nada,false,nil); ok=%t err=%v", ok, err)
	}
}

// TestSuspensao_NaoEhTerminado sela a correcção da mentira operacional: um run suspenso
// não é arquivado como terminado, não é podado pela retenção FIFO de desfechos, e a
// re-submissão é recusada com um erro que diz COMO retomar.
func TestSuspensao_NaoEhTerminado(t *testing.T) {
	// Estado suspenso montado directamente (o caminho completo exige um run escalado a
	// sério, coberto pelos testes de kernel; aqui isola-se a CONTABILIDADE do serviço).
	svc := &NodeService{
		runs:      make(map[string]*runState),
		completed: make(map[string]*runState),
		suspended: make(map[string]*runState),
	}
	rs := &runState{
		runID:     "run-esperando",
		done:      make(chan struct{}),
		suspended: true,
		result:    agentruntime.Result{Escalated: true, Turns: 3, EscalatedPreview: []byte{0xAA}},
	}
	svc.suspended[rs.runID] = rs

	// (1) NÃO aparece como terminado.
	if _, done := svc.Outcome("run-esperando"); done {
		t.Fatal("um run suspenso NÃO pode aparecer como terminado (era a mentira operacional)")
	}
	// (2) Aparece como suspenso, com o desfecho parcial.
	oc, susp := svc.Suspended("run-esperando")
	if !susp {
		t.Fatal("o run devia constar como suspenso")
	}
	if !oc.Result.Escalated || oc.Result.Turns != 3 {
		t.Fatalf("o desfecho parcial devia vir com a escalada: %+v", oc.Result)
	}
	// (3) A re-submissão é recusada — e o erro DIZ como retomar.
	err := svc.Submit(context.Background(), agentruntime.Goal{RunID: "run-esperando"})
	if err == nil {
		t.Fatal("re-submeter um run suspenso devia ser recusado")
	}
	if err != ErrRunSuspended {
		t.Fatalf("devia ser ErrRunSuspended, veio: %v", err)
	}
}

// newSweeperHarness monta um serviço com o registo de pendentes ligado e o varrimento
// DESLIGADO (período 0) — os testes conduzem-no à mão para serem deterministas.
func newSweeperHarness(t *testing.T, ttl time.Duration) (*NodeService, *integration.PendingApprovals) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	svc := &NodeService{
		node:        &Node{PendingApprovals: pend},
		approvalTTL: ttl,
		logw:        io.Discard,
	}
	return svc, pend
}

func pendenteAntigo(runID string, idade time.Duration) integration.PendingRecord {
	return integration.PendingRecord{
		RunID: runID, StepID: "s1-tool-1", Turn: 1,
		ToolID: "web_post", Capability: "cap:http.post",
		Preview:   []byte{0x11, 0x22},
		CreatedAt: time.Now().Add(-idade).UTC().Format(time.RFC3339Nano),
	}
}

// TestSweeper_ExpiraPassadoOTTL: um pendente sem decisão além do TTL deixa de aparecer ao
// operador. É o que impede a lista de crescer com coisas que já ninguém vai decidir.
func TestSweeper_ExpiraPassadoOTTL(t *testing.T) {
	svc, pend := newSweeperHarness(t, 15*time.Minute)
	ctx := context.Background()
	if err := pend.Put(ctx, pendenteAntigo("run-velho", 20*time.Minute)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if lista, _ := pend.ListForRun(ctx, "run-velho"); len(lista) != 1 {
		t.Fatalf("antes do varrimento devia estar pendente, n=%d", len(lista))
	}

	svc.SweepApprovalsNow(ctx)

	lista, err := pend.ListForRun(ctx, "run-velho")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(lista) != 0 {
		t.Fatalf("depois do varrimento o pendente devia ter EXPIRADO, n=%d", len(lista))
	}
}

// TestSweeper_NaoExpiraDentroDoTTL é o contraste (prova que o varrimento não é indiscriminado).
func TestSweeper_NaoExpiraDentroDoTTL(t *testing.T) {
	svc, pend := newSweeperHarness(t, 15*time.Minute)
	ctx := context.Background()
	if err := pend.Put(ctx, pendenteAntigo("run-recente", 2*time.Minute)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.SweepApprovalsNow(ctx)
	if lista, _ := pend.ListForRun(ctx, "run-recente"); len(lista) != 1 {
		t.Fatalf("um pendente DENTRO do TTL não pode ser expirado; n=%d", len(lista))
	}
}

// TestSweeper_SemCreatedAtNuncaExpira sela o fail-safe: não se expira sozinho aquilo cuja
// idade se desconhece (um registo antigo, anterior a este campo, fica à espera de decisão
// explícita em vez de desaparecer).
func TestSweeper_SemCreatedAtNuncaExpira(t *testing.T) {
	svc, pend := newSweeperHarness(t, 1*time.Nanosecond) // TTL agressivo de propósito
	ctx := context.Background()
	rec := pendenteAntigo("run-sem-data", time.Hour)
	rec.CreatedAt = "" // sem âncora de tempo
	if err := pend.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.SweepApprovalsNow(ctx)
	if lista, _ := pend.ListForRun(ctx, "run-sem-data"); len(lista) != 1 {
		t.Fatalf("sem CreatedAt o pendente NÃO pode expirar sozinho; n=%d", len(lista))
	}
}

// TestSweeper_EhIdempotente: varrer duas vezes não duplica nada nem falha (a expiração é
// um facto append-only com chave derivada de run+step).
func TestSweeper_EhIdempotente(t *testing.T) {
	svc, pend := newSweeperHarness(t, 15*time.Minute)
	ctx := context.Background()
	if err := pend.Put(ctx, pendenteAntigo("run-idem", 20*time.Minute)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.SweepApprovalsNow(ctx)
	svc.SweepApprovalsNow(ctx) // 2.ª vez: já não há expiráveis
	if lista, _ := pend.ListForRun(ctx, "run-idem"); len(lista) != 0 {
		t.Fatalf("o pendente devia continuar expirado; n=%d", len(lista))
	}
}

// TestSweeper_SemFourEyesNaoFazNada: sem registo composto o varrimento é no-op (não
// entra em pânico nem falha).
func TestSweeper_SemFourEyesNaoFazNada(t *testing.T) {
	svc := &NodeService{node: &Node{}, logw: io.Discard}
	svc.SweepApprovalsNow(context.Background()) // não deve entrar em pânico
}
