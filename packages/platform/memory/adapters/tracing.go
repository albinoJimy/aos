// Package adapters fornece dois backends para a MemoryPort: o adaptador de Event
// Store (fonte de verdade, ADR-007) e o adaptador in-memory (teste). Ambos
// passam o MESMO contract test — a prova do backend-swap por configuração.
package adapters

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/domain"
)

// Operações de porta como nomes de span. Namespace próprio aos.memory.*: as
// operações CRUD de memória NÃO são inferência GenAI, pelo que gen_ai.* fica
// reservado para atributos genuinamente GenAI. O SDK OTel real é EPIC-08; aqui
// usamos a porta Tracer zero-dep do Agent Runtime.
const (
	opPut    = "memory.put"
	opGet    = "memory.get"
	opQuery  = "memory.query"
	opDelete = "memory.delete"
)

// Atributos de span (subconjunto estável).
const (
	attrOperation  = "aos.memory.operation"
	attrSystem     = "aos.memory.system"
	attrClass      = "aos.memory.class"
	attrID         = "aos.memory.id"
	attrProvenance = "aos.memory.provenance"
	attrResult     = "aos.memory.result"
	attrCount      = "aos.memory.result.count"
	attrPortVer    = "aos.memory.port_version"
)

// startSpan abre um span de operação de porta via o Tracer injectado. Um tracer
// nil cai no NoopTracer (sem observabilidade, sem dependências). Devolve o
// contexto derivado e o span (o chamador fecha com End).
func startSpan(ctx context.Context, tracer agentruntime.Tracer, op string, class domain.MemoryClass, id string) (context.Context, agentruntime.Span) {
	if tracer == nil {
		tracer = agentruntime.NoopTracer{}
	}
	ctx, span := tracer.StartSpan(ctx, op)
	span.SetAttribute(attrOperation, op)
	span.SetAttribute(attrSystem, "aos.memory")
	span.SetAttribute(attrPortVer, portVersion)
	span.SetAttribute(attrClass, class.String())
	if id != "" {
		span.SetAttribute(attrID, id)
	}
	return ctx, span
}
