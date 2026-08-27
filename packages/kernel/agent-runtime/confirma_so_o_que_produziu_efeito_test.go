package agentruntime_test

// O CHECKPOINT SÓ CONFIRMA O QUE PRODUZIU EFEITO.
//
// [Runtime.cpActivity] promete, no seu próprio doc, «consistência checkpoint↔ledger»: o
// `ConfirmedStepID` que ele grava é «EXACTAMENTE o step_id que o step-ledger usa para o mesmo
// passo lógico». Mas corria para TODAS as tool calls do turno, incluindo as que o ledger NÃO
// memoriza:
//
//	negada/escalada ⇒ o efeito nem chega a correr
//	tool falhada    ⇒ nada memorizado, passo declarado RETRIÁVEL
//
// Confirmar um desses põe o cursor de retoma a dizer «feito» sobre um passo por aplicar, e a
// retoma SALTA-O — a acção nunca executa e ninguém dá por isso. É a mesma corrupção que o ramo
// da escalada já evitava retornando antes; a diferença é que a negação e o erro de tool
// continuam o turno, pelo que não bastava retornar.
//
// Este teste observa a porta [Checkpointer], que é onde o cursor se materializa.

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// checkpointsObservados grava os `ConfirmedStepID` que o laço declarou confirmados.
type checkpointsObservados struct{ confirmados []string }

func (c *checkpointsObservados) Checkpoint(_ context.Context, cp agentruntime.Checkpoint) error {
	if cp.ConfirmedStepID != "" {
		c.confirmados = append(c.confirmados, cp.ConfirmedStepID)
	}
	return nil
}

// coletorMudo satisfaz o EventAppender do TurnRecorder sem substrato.
type coletorMudo struct{}

func (coletorMudo) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, nil
}

// corridaComUmaToolCall corre um turno com UMA tool call e devolve o que ficou confirmado.
// O comportamento da tool é decidido pelo `registar`, que o chamador fornece.
func corridaComUmaToolCall(t *testing.T, registar func(rm *referencemonitor.Monitor)) []string {
	t.Helper()

	rm := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.IdentityStub{},
		referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{},
		referencemonitor.EgressStub{},
		referencemonitor.AuditStub{},
	))
	registar(rm)

	guiao := []agentruntime.ModelResponse{
		{Text: "vou usar a tool", ToolCalls: []agentruntime.ToolInvocation{{
			ToolID: "t", Capability: "cap:fs.read", ResourceType: "file", ResourceValue: "x",
			Input: []byte("x"),
		}}},
		{Text: "acabei", Final: true},
	}
	i := 0
	modelo := agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		r := guiao[i]
		i++
		return r, nil
	})

	obs := &checkpointsObservados{}
	rt := agentruntime.New(modelo, rm, agentruntime.NewTurnRecorder(coletorMudo{}),
		agentruntime.WithCheckpointer(obs))

	_, _ = rt.Run(context.Background(), agentruntime.Goal{
		RunID: "run-cp", Principal: referencemonitor.Principal{NHIID: "a"},
		Scope: []string{"cap:fs.read"}, System: "s", Objective: "o", MaxTurns: 4,
	})
	return obs.confirmados
}

// contemSubPasso diz se algum confirmado é de tool (a convenção "-tool-N").
func contemSubPasso(confirmados []string) bool {
	for _, c := range confirmados {
		if strings.Contains(c, "-tool-") {
			return true
		}
	}
	return false
}

// TestCheckpoint_ToolFalhadaNaoEConfirmada é o caso que a correcção do despacho durável torna
// alcançável: a tool é PERMITIDA, falha em runtime, o laço continua — e o passo NÃO pode ficar
// confirmado, porque o ledger não o memorizou e declara-o retriável.
func TestCheckpoint_ToolFalhadaNaoEConfirmada(t *testing.T) {
	confirmados := corridaComUmaToolCall(t, func(rm *referencemonitor.Monitor) {
		_ = rm.Register("t", func(context.Context, []byte) ([]byte, error) {
			return nil, errors.New("HTTP 500 a jusante")
		})
	})

	if contemSubPasso(confirmados) {
		t.Errorf("uma tool FALHADA ficou confirmada no cursor — a retoma vai SALTA-LA e a accao "+
			"nunca executa.\nconfirmados: %v", confirmados)
	}
}

// TestCheckpoint_ToolNegadaNaoEConfirmada cobre o caso que já existia antes desta correcção: a
// tool não está registada, o RM nega por default-deny, e o efeito nem chega a correr.
func TestCheckpoint_ToolNegadaNaoEConfirmada(t *testing.T) {
	confirmados := corridaComUmaToolCall(t, func(*referencemonitor.Monitor) {
		// Não regista nada: default-deny.
	})

	if contemSubPasso(confirmados) {
		t.Errorf("uma tool NEGADA ficou confirmada no cursor — o efeito nunca correu.\nconfirmados: %v", confirmados)
	}
}

// TestCheckpoint_ToolPermitidaEConfirmada é a metade que impede a guarda de se tornar um
// silenciador. Sem ela, `if false` passaria os dois testes acima — e o cursor deixaria de
// confirmar QUALQUER passo, fazendo a retoma repetir efeitos já aplicados.
func TestCheckpoint_ToolPermitidaEConfirmada(t *testing.T) {
	confirmados := corridaComUmaToolCall(t, func(rm *referencemonitor.Monitor) {
		_ = rm.Register("t", func(_ context.Context, in []byte) ([]byte, error) {
			return append([]byte("ok:"), in...), nil
		})
	})

	if !contemSubPasso(confirmados) {
		t.Errorf("uma tool PERMITIDA que correu TEM de ficar confirmada — senao a retoma repete o "+
			"efeito.\nconfirmados: %v", confirmados)
	}
}
