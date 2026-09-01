package integration

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestGuard_ConstantesDoEventStoreNaoDivergem é o guard-test que a duplicação deliberada
// das constantes exige.
//
// `substrate/eventstore` é ZERO-DEP e não pode importar `substrate/otel-genai` para
// partilhar as strings (é essa propriedade que sustenta o binário zero-dep do nó); e o
// `otel-genai` não conhece o Event Store nem deve. Logo as strings estão nos dois sítios.
//
// Este pacote é o ÚNICO que pode importar ambos, e por isso o único que pode comparar.
// Sem este teste, a duplicação seria uma cópia à espera de divergir — e a divergência não
// dava erro nenhum: o span cairia numa operação sem contrato e passaria a ser aceite SEM
// VALIDAR, que é exactamente o defeito que o AOS-211 corrigiu no `aos.activity`.
func TestGuard_ConstantesDoEventStoreNaoDivergem(t *testing.T) {
	pares := []struct {
		nome            string
		naFolha, noOTel string
	}{
		{"operação append", eventstore.OperacaoAppend, otelgenai.OpLogAppend},
		{"operação read", eventstore.OperacaoRead, otelgenai.OpLogRead},
		{"atributo stream", eventstore.AtributoStream, otelgenai.AttrLogStream},
		{"atributo seq", eventstore.AtributoSeq, otelgenai.AttrLogSeq},
		{"atributo desfecho", eventstore.AtributoDesfecho, otelgenai.AttrLogOutcome},
		{"atributo eventos", eventstore.AtributoContagem, otelgenai.AttrLogCount},
	}
	for _, p := range pares {
		if p.naFolha != p.noOTel {
			t.Errorf("%s DIVERGIU: eventstore=%q otel-genai=%q — o span deixaria de casar com o "+
				"contrato semconv e passaria a ser aceite sem validação", p.nome, p.naFolha, p.noOTel)
		}
	}
}

// TestRastreio_SpanDoEventStoreECumpreOContratoSemconv prova que o span que a ponte emite
// é CONFORME — e não apenas que é emitido.
//
// Um span que passasse pelo validador por falta de contrato seria pior do que nenhum:
// daria a impressão de observabilidade sem a disciplina que a torna útil.
func TestRastreio_SpanDoEventStoreECumpreOContratoSemconv(t *testing.T) {
	exp := &otelgenai.RecordingExporter{}
	tr := otelgenai.NewTracer(exp)
	ponte := NovoRastreioDoEventStore(tr)

	_, span := ponte.Iniciar(context.Background(), eventstore.OperacaoAppend)
	span.Atributo(eventstore.AtributoStream, "run-1")
	span.Atributo(eventstore.AtributoDesfecho, "committed")
	span.Atributo(eventstore.AtributoSeq, uint64(1))
	span.Fim()

	spans := exp.Spans()
	if len(spans) != 1 {
		t.Fatalf("exportados %d spans, quer 1", len(spans))
	}
	if err := otelgenai.ValidateSpanData(spans[0]); err != nil {
		t.Fatalf("o span do Event Store NÃO cumpre a semconv: %v", err)
	}
}

// TestRastreio_SemTracerNaoRebenta — a ponte devolve nil sem tracer, e um nil tem de
// atravessar o caminho quente sem ramo no chamador.
func TestRastreio_SemTracerNaoRebenta(t *testing.T) {
	var ponte *RastreioDoEventStore // deliberadamente nil
	if p := NovoRastreioDoEventStore(nil); p != nil {
		t.Fatal("sem tracer, a ponte devia ser nil para o ComRastreador a ignorar")
	}
	ctx, span := ponte.Iniciar(context.Background(), eventstore.OperacaoRead)
	span.Atributo("x", 1)
	span.Fim()
	if ctx == nil {
		t.Fatal("Iniciar devolveu ctx nil")
	}
}
