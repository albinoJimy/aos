package audit

import (
	"context"
	"fmt"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TeeSink faz fan-out de cada MediationRecord por vários [referencemonitor.EventSink]
// em sequência, permitindo ter SIMULTANEAMENTE o Event Store durável (AOS-002/009)
// E a cadeia de audit tamper-evident deste pacote (AOS-011-Q4) — sem regredir um
// para adoptar o outro. Antes deste adaptador, o RM tinha um único EventSink e
// [referencemonitor.WithEventSink] substituía-o, forçando a escolha de UM destino.
//
// Fail-closed (ADR-002/010): se QUALQUER sink devolver erro, RecordMediation
// devolve erro e para no primeiro que falha. No caminho de permit, o RM degrada
// então a decisão para Deny — uma acção que não pôde ser registada em TODOS os
// destinos duráveis não é permitida. A ordem dos sinks é a ordem de escrita.
//
// O seq devolvido é o do PRIMEIRO sink (o "primário"): componha com o sink que
// materializa o seq durável canónico à cabeça (tipicamente o Event Store), ex.:
//
//	sink := audit.NewTeeSink(
//		referencemonitor.NewEventStoreSink(store), // primário: seq durável
//		audit.NewMediationSink(auditStore),        // cadeia tamper-evident
//	)
//	mon := referencemonitor.New(referencemonitor.WithEventSink(sink), ...)
type TeeSink struct {
	sinks []referencemonitor.EventSink
}

// NewTeeSink constrói um TeeSink sobre os sinks dados, na ordem de escrita. O
// primeiro sink é o primário (o seu seq é o devolvido). Aceita zero sinks
// (no-op que devolve seq 0), mas produção deve fornecer pelo menos o Event Store
// durável e a cadeia de audit.
func NewTeeSink(sinks ...referencemonitor.EventSink) *TeeSink {
	return &TeeSink{sinks: sinks}
}

// RecordMediation implementa [referencemonitor.EventSink]: escreve o registo em
// todos os sinks por ordem e devolve o seq do primário. Fail-closed: para e
// devolve erro no primeiro sink que falhar (nenhum sink posterior é tentado).
func (t *TeeSink) RecordMediation(ctx context.Context, rec referencemonitor.MediationRecord) (uint64, error) {
	var primarySeq uint64
	for i, s := range t.sinks {
		seq, err := s.RecordMediation(ctx, rec)
		if err != nil {
			return 0, fmt.Errorf("audit: tee sink %d falhou (fail-closed): %w", i, err)
		}
		if i == 0 {
			primarySeq = seq
		}
	}
	return primarySeq, nil
}
