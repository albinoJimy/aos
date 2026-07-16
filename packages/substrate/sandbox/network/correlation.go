package network

import "context"

// correlationKey é a chave não-exportada do contexto que transporta a correlação
// (run_id/step_id) da trajectória até à selagem do evento de egress no WORM. É
// opcional: sem correlação, o evento sela na mesma (atribuído ao principal), apenas
// sem os ids de trajectória.
type correlationKey struct{}

type correlation struct {
	runID  string
	stepID string
}

// WithCorrelation anexa run_id/step_id ao contexto para que [EgressFilter.Decide]
// os sele no evento de segurança. O [EgressHook] usa-o para propagar a correlação da
// [referencemonitor.Call] sem alterar a assinatura de Decide (que é
// principal + destino, o contrato de AOS-067).
func WithCorrelation(ctx context.Context, runID, stepID string) context.Context {
	return context.WithValue(ctx, correlationKey{}, correlation{runID: runID, stepID: stepID})
}

// correlationFrom extrai a correlação do contexto (vazia se ausente).
func correlationFrom(ctx context.Context) correlation {
	c, _ := ctx.Value(correlationKey{}).(correlation)
	return c
}
