package eventstore

import "context"

// ---------------------------------------------------------------------------
// AOS-478 — o `producer` de um facto CAUSADO por outro, transportado no contexto
// ---------------------------------------------------------------------------
//
// Alguns factos são gravados por quem não sabe quem os causou: a sandbox recebe só o input
// opaco da tool, e o verificador de aprovações só a evidência. Quem sabe é o Reference
// Monitor, que resolve o principal da tool call. Em vez de alargar as portas de cada um, o RM
// anexa ao contexto o `producer` do facto e o emissor a jusante lê-o daqui.
//
// Vive no Event Store, e não no RM, de propósito: é o tipo do envelope, e assim o substrato
// (a sandbox) lê-o sem importar o kernel (ADR-019 — o substrato não conhece camadas acima).
//
// É ATRIBUIÇÃO, NUNCA AUTORIZAÇÃO. Nenhum gate lê este valor para decidir, e o [Store] NÃO o
// aplica sozinho: só o emissor que chama [ProducerFromContext] o usa, no seu `EventInput`.

// producerKey é a chave (tipo privado, não-colidível) do producer no contexto.
type producerKey struct{}

// ContextWithProducer anexa ao contexto o `producer` dos factos que o trabalho a jusante
// vai gravar. O valor é copiado: mutar o original depois não o altera.
func ContextWithProducer(ctx context.Context, p Producer) context.Context {
	return context.WithValue(ctx, producerKey{}, p.clone())
}

// ProducerFromContext devolve o `producer` anexado por [ContextWithProducer], ou o valor zero
// quando não há (o facto não foi causado por uma tool call mediada).
func ProducerFromContext(ctx context.Context) Producer {
	p, ok := ctx.Value(producerKey{}).(Producer)
	if !ok {
		return Producer{}
	}
	return p.clone()
}
