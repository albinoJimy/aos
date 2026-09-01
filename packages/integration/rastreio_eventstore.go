package integration

import (
	"context"

	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// rastreio_eventstore.go — a ponte entre a porta de rastreio do Event Store e o tracer
// OTel (EPIC-08 sobre AOS-100).
//
// # Porque a ponte vive AQUI
//
// `substrate/eventstore` tem ZERO dependências, e é essa propriedade que sustenta o
// binário zero-dep do nó (ADR-017 §1); `substrate/otel-genai` não conhece o Event Store,
// nem deve. O único sítio que pode importar os dois é o ápice de composição — que é
// exactamente o que ele existe para fazer.
//
// A ponte é fina de propósito: as duas interfaces têm a MESMA forma, e isso não é
// coincidência (ver [eventstore.Rastreador]). Uma ponte que precisasse de traduzir seria
// sinal de que uma das duas estava desenhada contra a outra.

// RastreioDoEventStore implementa [eventstore.Rastreador] sobre um [otelgenai.Tracer].
type RastreioDoEventStore struct{ tr otelgenai.Tracer }

// NovoRastreioDoEventStore devolve a ponte, ou nil se não houver tracer.
//
// Devolver nil é deliberado: o `ComRastreador` do adaptador ignora um rastreador nil e
// mantém o no-op, pelo que «sem tracer configurado» não precisa de um ramo no chamador.
func NovoRastreioDoEventStore(tr otelgenai.Tracer) *RastreioDoEventStore {
	if tr == nil {
		return nil
	}
	return &RastreioDoEventStore{tr: tr}
}

// Iniciar abre o span e põe-lhe o `gen_ai.operation.name`.
//
// O atributo é posto AQUI e não pelo Event Store porque é ele que faz o span cair sob o
// contrato semconv ([otelgenai.ValidateSpanData]): sem ele, a operação resolveria por
// fallback do nome e o span do Event Store seria o único da árvore isento de validação —
// que foi exactamente o defeito que o AOS-211 corrigiu no `aos.activity`.
func (r *RastreioDoEventStore) Iniciar(ctx context.Context, operacao string) (context.Context, eventstore.Rastro) {
	if r == nil || r.tr == nil {
		return eventstore.NopRastreador{}.Iniciar(ctx, operacao)
	}
	ctx, span := r.tr.StartSpan(ctx, operacao)
	span.SetAttribute(otelgenai.AttrOperationName, operacao)
	return ctx, rastroOTel{span: span}
}

type rastroOTel struct{ span otelgenai.Span }

func (r rastroOTel) Atributo(chave string, valor any) { r.span.SetAttribute(chave, valor) }
func (r rastroOTel) Fim()                             { r.span.End() }
