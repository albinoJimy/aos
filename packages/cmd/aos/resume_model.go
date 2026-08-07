package main

// CLIENTE DE MODELO PARA A RETOMA (AOS-021) — replay-then-continue.
//
// Retomar um run suspenso NÃO pode ser reinterrogar o modelo: a aprovação está amarrada à
// [referencemonitor.ApprovalPreview], que inclui o hash do Input da tool call. Se o modelo
// voltasse a decidir, emitiria outra call (o prompt já leva o marcador de negação do turno
// escalado) — outra preview, e a aprovação NUNCA se aplicaria. Fail-closed, mas
// funcionalmente inútil: o humano teria aprovado algo irrepetível.
//
// A solução usa o que já existe: o capturer de replay persiste a ModelResponse COMPLETA de
// cada turno, com os Inputs das tool calls. Na retoma, os turnos já dados são REPRODUZIDOS
// da captura e só depois o modelo volta a ser consultado.
//
// PORQUE POR CONTEXTO E NÃO POR UM RUNTIME NOVO: o [integration.SecuredRuntime] é composto
// UMA vez no arranque, com um ModelClient fixo. Reconstruí-lo por retoma duplicaria toda a
// cadeia de mediação. O [agentruntime.PromptView] só transporta o Turn (não o RunID), pelo
// que um decorador global não saberia que run está a servir — mas o ctx SIM: ele flui de
// Run(ctx, goal) até model.Call(ctx, view) e é POR-RUN por construção. O plano de replay
// viaja no ctx da retoma.

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// replayPlanKey é a chave (tipo privado — nenhum pacote externo a forja) do plano de
// replay no contexto.
type replayPlanKey struct{}

// replayPlan mapeia turno → resposta do modelo REGISTADA. Um turno ausente ⇒ o modelo é
// consultado ao vivo.
type replayPlan map[int]agentruntime.ModelResponse

// withReplayPlan devolve um ctx que carrega o plano de replay da retoma. Só o caminho de
// retoma o chama; um run normal nunca tem plano e o cliente é totalmente transparente.
func withReplayPlan(ctx context.Context, plan replayPlan) context.Context {
	if len(plan) == 0 {
		return ctx
	}
	return context.WithValue(ctx, replayPlanKey{}, plan)
}

// replayPlanFrom extrai o plano do ctx (nil se não houver).
func replayPlanFrom(ctx context.Context) replayPlan {
	plan, _ := ctx.Value(replayPlanKey{}).(replayPlan)
	return plan
}

// resumeAwareModelClient decora o [agentruntime.ModelClient] do nó: num turno COBERTO pelo
// plano de replay devolve a resposta REGISTADA (sem tocar no modelo); nos restantes delega
// no cliente real.
//
// ADITIVO: sem plano no ctx — o caso de todo o run normal — o comportamento é
// byte-idêntico ao do cliente decorado.
type resumeAwareModelClient struct {
	inner agentruntime.ModelClient
}

// newResumeAwareModelClient decora o cliente do nó. inner nil devolve nil (nada a decorar).
func newResumeAwareModelClient(inner agentruntime.ModelClient) agentruntime.ModelClient {
	if inner == nil {
		return nil
	}
	return &resumeAwareModelClient{inner: inner}
}

// Call implementa [agentruntime.ModelClient].
func (c *resumeAwareModelClient) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if plan := replayPlanFrom(ctx); plan != nil {
		if resp, ok := plan[view.Turn]; ok {
			// Turno JÁ DADO: devolve-se o que ficou registado. As tool calls voltam a ser
			// mediadas — as já aplicadas batem no already-applied do step-ledger e NÃO
			// re-executam; a escalada, que nunca chegou a aplicar-se, é re-mediada e
			// encontra agora a aprovação.
			return resp, nil
		}
	}
	return c.inner.Call(ctx, view)
}

var _ agentruntime.ModelClient = (*resumeAwareModelClient)(nil)
