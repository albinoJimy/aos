package main

// AOS-254 — SAGA/COMPENSACAO: prova, PELA CADEIA REAL (obsPermitNodeWith = Bootstrap/
// NewSecuredRuntime), o UNICO caminho ALCANCAVEL em producao do hook `failed`->saga.
//
// PORQUE SO ESTE CAMINHO: o registo de compensacoes (Node.Compensations) e composto mas o
// loop base NUNCA popula activity.Activity.Compensation — logo, em producao, o registo esta
// SEMPRE vazio e a conducao da saga cai, atribuivelmente, no ramo "declarar ausencia":
// span + selo WORM (reason=saga_no_compensation_registered, Decision=Deny) e o run PERMANECE
// em `failed`. E este o efeito que o AOS-254 entrega HOJE, e e este que aqui se tranca.
//
// A COMPENSACAO REAL (failed->compensating->ready com Action inversa) fica DEFERIDA com eixo
// (DEF-270): exige (a) um PRODUTOR de compensacoes a montante e (b) o registo RUN-SCOPED no
// kernel (a struct saga.Compensation nao carrega run_id; o gate global Len() compensaria o run
// errado quando houvesse compensacoes de varios runs). Ver a seccao AOS-254 do EPIC-20 e a nota
// "LIMITE conhecido" em saga_compensation.go. Nao se testa aqui um caminho que producao nao
// percorre — seria composicao sem alcance real, o oposto da invariante do epic.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// failingModel254 devolve erro em Call — o run FALHA e o hostRun sela o desfecho `failed`, a
// UNICA origem da saga de rollback (sealTerminalState). Sem tool calls: a falha e o eixo.
type failingModel254 struct{}

func (failingModel254) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{}, errors.New("aos254: falha injectada para forcar o selo `failed`")
}

// TestAOS254_SagaDeclaresAbsenceOnFailedWithEmptyRegistry tranca o AC2 no caminho alcancavel: um
// run que sela `failed` com o registo de compensacoes VAZIO (a postura de producao) DECLARA a
// ausencia de compensacao no WORM (span + selo) e o run permanece `failed` — nunca em silencio.
// Pela cadeia REAL: Bootstrap compoe o Node.Compensations e liga driveSagaCompensation ao selo
// terminal; o teste nao substitui nenhuma peca vizinha por um double.
func TestAOS254_SagaDeclaresAbsenceOnFailedWithEmptyRegistry(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0") // isola: o disjuntor nao interfere com o desfecho
	ctx := context.Background()

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	node, cred := obsPermitNodeWith(t, "", failingModel254{}, func(cfg *Config) {
		cfg.EventStore = store // Event Store explicito => DurableExecution aceite (molde do aos253)
		cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
		cfg.DurableExecution = true
		cfg.Approvers = crashResumeApprovers(t)
	})
	t.Cleanup(func() { _ = node.Close() })

	// PRE-CONDICAO da postura de producao: o registo E composto (a wiring de AOS-254) mas esta
	// VAZIO (nada o popula no loop base). E o que torna "declarar ausencia" o unico ramo real.
	if node.Compensations == nil {
		t.Fatal("pre-condicao AOS-254: o CompensationRegistry devia estar COMPOSTO pela cadeia real")
	}
	if n := node.Compensations.Len(); n != 0 {
		t.Fatalf("pre-condicao AOS-254: o registo devia estar VAZIO em producao; Len=%d", n)
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

	const runID = "run-254-absence"
	if err := svc.Submit(ctx, agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: cred,
		Model:      agentruntime.ModelConfig{ModelID: "model:254"},
		Objective:  "saga: declarar ausencia de compensacao no caminho de falha",
		MaxTurns:   4,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	wc, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	oc, ok, werr := svc.Wait(wc, runID)
	if werr != nil || !ok {
		t.Fatalf("Wait do run: ok=%v err=%v", ok, werr)
	}
	// O run FALHOU (o modelo errou) — pre-condicao do hook `failed`->saga.
	if oc.Err == nil {
		t.Fatalf("o run devia FALHAR (modelo a errar) para accionar a saga; oc=%+v", oc)
	}

	// --- A ASSERCAO QUE IMPORTA (AC2, caminho alcancavel): a AUSENCIA foi DECLARADA no WORM. ---
	head, err := node.WORM.Head(ctx, sagaCompensationPartition)
	if err != nil {
		t.Fatalf("Head da particao da saga: %v", err)
	}
	if head == 0 {
		t.Fatalf("AC2 (AOS-254): o caminho de falha com registo VAZIO devia SELAR algo na particao %q; particao vazia (nenhuma declaracao de ausencia — hook `failed`->saga NAO correu?)", sagaCompensationPartition)
	}
	recs, err := node.WORM.Read(ctx, sagaCompensationPartition, 1, head)
	if err != nil {
		t.Fatalf("ler a particao da saga no WORM: %v", err)
	}
	var found *audit.AuditRecord
	for i := range recs {
		if recs[i].RunID == runID && recs[i].Reason == reasonSagaNoCompensation {
			found = &recs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("AC2 (AOS-254): esperado um selo de AUSENCIA (reason=%q, run=%q) na particao %q; records=%+v", reasonSagaNoCompensation, runID, sagaCompensationPartition, recs)
	}
	if found.Decision != audit.DecisionDeny {
		t.Fatalf("o selo de ausencia devia ser Deny (nao compensou nada); decision=%v", found.Decision)
	}
	if found.Principal.NHIID != durAgent {
		t.Fatalf("o selo de ausencia devia ATRIBUIR o titular %q; got %q", durAgent, found.Principal.NHIID)
	}
}
