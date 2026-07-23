package main

// AOS-173 (EPIC-15, E7) — a LIGAÇÃO do custo e do WORM à observabilidade por OTLP.
//
// O nó já tem toda a mecânica de spans (substrate/otel-genai): o Agent Runtime abre
// os spans invoke_agent/chat/execute_tool (o span chat JÁ carrega o custo do turno,
// AttrCostMicroUSD/AttrCostUSD — ver kernel/agent-runtime/loop.go callModel) e o
// Reference Monitor partilha o mesmo tracer. O que faltava (§13 não-verde por
// omissão) era: (a) um tracer REAL ligado a um exporter OTLP em vez do NoopTracer, e
// (b) tornar o WORM de audit OBSERVÁVEL por OTLP. Este ficheiro dá (b) — o (a) é o
// wiring em bootstrap.go.
//
// # WORM ligado à observabilidade (não duplicado — LIGADO)
//
// [auditTracingStore] decora o audit.Store: a CADA selo (Append), emite UM span de
// observabilidade que LIGA a trajectória ao registo WORM — run_id/step_id + o
// audit_seq gapless + o entry_hash da hash-chain + o veredicto/tool. Não reimplementa
// nem duplica o WORM (a hash-chain tamper-evident continua a ser a fonte de verdade da
// prova): só expõe a existência e a âncora do selo como telemetria, para o §13 ficar
// observável ponta-a-ponta. Quando o ctx do Append carrega o span de execute_tool, o
// span de selo aninha-se no MESMO trace (mesmo trace_id, parent = execute_tool); senão
// correlaciona-se por run_id/step_id.
//
// # Sem segredos (invariante semconv preservada)
//
// O span de selo só transporta IDS/METADADOS/HASHES — partição, seq, entry_hash (hex),
// run_id, step_id, decisão, tool_id. NUNCA o payload/recurso concreto nem qualquer
// conteúdo. É a mesma disciplina dos spans do substrato (só ids/metadados, ADR-005).

import (
	"context"
	"encoding/hex"

	audit "github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Atributos de observabilidade do WORM (namespace aos.audit.*). São METADADOS de
// audit, nunca segredos: ligam o selo tamper-evident à trajectória sem expor conteúdo.
const (
	// opAuditSeal é o nome de operação do span de selo de audit. Não tem contrato
	// semconv obrigatório (ValidateSpanData aceita-o), por ser um span de LIGAÇÃO
	// WORM↔trajectória específico do nó, não uma operação GenAI.
	opAuditSeal = "audit_seal"
	// attrAuditPartition — a partição de encadeamento do WORM (run_id/tenant/global).
	attrAuditPartition = "aos.audit.partition"
	// attrAuditSeq — o audit_seq gapless do selo dentro da partição.
	attrAuditSeq = "aos.audit.seq"
	// attrAuditEntryHash — o EntryHash (hex) da hash-chain: a âncora tamper-evident.
	attrAuditEntryHash = "aos.audit.entry_hash"
)

// auditTracingStore decora um [audit.Store] emitindo um span de observabilidade por
// selo. Delega TODAS as operações no store subjacente sem as alterar — a hash-chain,
// a atribuição de seq e a prova continuam integralmente no store real. Só acrescenta
// telemetria (fail-open a montante: o exporter nunca quebra o run).
type auditTracingStore struct {
	inner  audit.Store
	tracer otelgenai.Tracer
}

// newAuditTracingStore embrulha inner com emissão de spans via tracer. Se qualquer um
// for nil devolve o inner tal-qual (sem overhead) — o chamador só embrulha quando a
// observabilidade está ligada.
func newAuditTracingStore(inner audit.Store, tracer otelgenai.Tracer) audit.Store {
	if inner == nil || tracer == nil {
		return inner
	}
	return &auditTracingStore{inner: inner, tracer: tracer}
}

// Append sela no store real e, SÓ DEPOIS (para ter o audit_seq e o entry_hash
// atribuídos), emite o span de ligação. Um erro do selo propaga-se inalterado (o selo
// de audit É caminho crítico de conformidade — ao contrário da telemetria); nesse caso
// não se emite span de sucesso.
func (s *auditTracingStore) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	sealed, err := s.inner.Append(ctx, rec)
	if err != nil {
		return sealed, err
	}
	// O span de selo aninha-se no trace corrente se o ctx o carregar (ex.: dentro do
	// execute_tool da mediação); senão é raiz correlacionada por run_id/step_id.
	_, span := s.tracer.StartSpan(ctx, opAuditSeal)
	span.SetAttribute(otelgenai.AttrOperationName, opAuditSeal)
	span.SetAttribute(attrAuditPartition, sealed.Partition)
	span.SetAttribute(attrAuditSeq, int64(sealed.AuditSeq))
	if len(sealed.EntryHash) > 0 {
		span.SetAttribute(attrAuditEntryHash, hex.EncodeToString(sealed.EntryHash))
	}
	if sealed.RunID != "" {
		span.SetAttribute(otelgenai.AttrRunID, sealed.RunID)
	}
	if sealed.StepID != "" {
		span.SetAttribute(otelgenai.AttrStepID, sealed.StepID)
	}
	if d := string(sealed.Decision); d != "" {
		span.SetAttribute(otelgenai.AttrDecision, d)
	}
	if sealed.ToolID != "" {
		span.SetAttribute(otelgenai.AttrToolName, sealed.ToolID)
	}
	span.End()
	return sealed, nil
}

// Read delega no store subjacente (sem telemetria — leitura não é um selo).
func (s *auditTracingStore) Read(ctx context.Context, partition string, from, to uint64) ([]audit.AuditRecord, error) {
	return s.inner.Read(ctx, partition, from, to)
}

// Head delega no store subjacente.
func (s *auditTracingStore) Head(ctx context.Context, partition string) (uint64, error) {
	return s.inner.Head(ctx, partition)
}

// At delega no store subjacente.
func (s *auditTracingStore) At(ctx context.Context, partition string, seq uint64) (audit.AuditRecord, bool, error) {
	return s.inner.At(ctx, partition, seq)
}

// compile-time: a decoração continua a satisfazer a porta WORM.
var _ audit.Store = (*auditTracingStore)(nil)
