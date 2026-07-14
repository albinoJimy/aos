package provenance

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/domain"
)

// Provenance é a classificação de confiança canónica, reutilizada de AOS-035
// (domain). Não se redefine o modelo — este pacote IMPÕE-o e adiciona a
// quarentena/barreira. Trusted e Untrusted são os únicos valores canónicos.
type Provenance = domain.Provenance

const (
	// Trusted — origem confiável (system / utilizador autenticado). SÓ memória
	// trusted alimenta o planeador (control-plane).
	Trusted = domain.ProvenanceTrusted
	// Untrusted — origem não-confiável; é dados, nunca instruções (ADR-005).
	Untrusted = domain.ProvenanceUntrusted
)

// Source é o TIPO de origem de conteúdo ingerido. A classificação de taint é
// feita pela FONTE, estruturalmente — nunca por uma tag in-band que o próprio
// conteúdo carregue (uma tag é trivial de forjar e não é separação de privilégio).
type Source string

const (
	// SourceSystem — conteúdo produzido pelo próprio sistema (prompts de sistema,
	// configuração confiável). Trusted.
	SourceSystem Source = "system"
	// SourceAuthenticatedUser — input directo do utilizador autenticado. Trusted.
	SourceAuthenticatedUser Source = "authenticated_user"
	// SourceToolResult — resultado de uma tool call (o output de uma ferramenta é
	// conteúdo externo não-confiável). Untrusted.
	SourceToolResult Source = "tool_result"
	// SourceWeb — conteúdo obtido da web (páginas, respostas HTTP). Untrusted.
	SourceWeb Source = "web"
	// SourceMCPSchema — descrições/schemas de servidores MCP (descrições de tools
	// são conteúdo controlado por terceiros). Untrusted.
	SourceMCPSchema Source = "mcp_schema"
	// SourceDerivedMemory — memória derivada de OUTRA memória. A sua proveniência
	// NÃO se decide pela fonte mas pela derivação (ver [TaintController.Derive]);
	// classificá-la directamente por [Classify] cai, fail-closed, em untrusted.
	SourceDerivedMemory Source = "derived_memory"
)

// trustedSources é o conjunto FECHADO de fontes confiáveis. Tudo o resto — tool
// results, web, schemas MCP, fontes desconhecidas — é untrusted (fail-closed: o
// default é o lado seguro, não trusted).
var trustedSources = map[Source]bool{
	SourceSystem:            true,
	SourceAuthenticatedUser: true,
}

// Classify mapeia uma fonte DIRECTA de ingestão para a sua proveniência. É a
// marcação automática de untrusted: só as fontes explicitamente confiáveis são
// trusted; qualquer outra (incluindo uma fonte vazia/desconhecida) é untrusted.
// Deriva memória NÃO passa por aqui — usa [TaintController.Derive].
func Classify(src Source) Provenance {
	if trustedSources[src] {
		return Trusted
	}
	return Untrusted
}

// TaintController é a PORTA para o mecanismo de taint control de EPIC-07
// (SBX / dual-LLM / CaMeL). A camada de memória depende SÓ desta interface e não
// reimplementa EPIC-07; [DefaultTaintController] é a implementação de referência
// que EPIC-07 substituirá em produção.
type TaintController interface {
	// Classify determina a proveniência de conteúdo ingerido de uma fonte directa.
	Classify(src Source) Provenance
	// Derive computa a proveniência de memória derivada dos taints dos PAIS,
	// propagando o taint transitivamente (untrusted é contagioso).
	Derive(parents ...Provenance) Provenance
}

// DefaultTaintController é a implementação de referência de [TaintController]. É
// sem estado, determinística e fail-closed.
type DefaultTaintController struct{}

// Classify implementa [TaintController] delegando em [Classify].
func (DefaultTaintController) Classify(src Source) Provenance { return Classify(src) }

// Derive implementa [TaintController] com propagação TRANSITIVA de taint:
//
//   - sem pais → untrusted (fail-closed: uma derivação sem base não é confiável);
//   - qualquer pai não-trusted (untrusted ou valor inválido) → untrusted (o taint
//     é contagioso: uma mistura trusted+untrusted resulta untrusted — não há
//     lavagem de taint);
//   - todos os pais trusted → trusted.
func (DefaultTaintController) Derive(parents ...Provenance) Provenance {
	return Derive(parents...)
}

// Derive é a função pura de propagação transitiva de taint (ver
// [DefaultTaintController.Derive]).
func Derive(parents ...Provenance) Provenance {
	if len(parents) == 0 {
		return Untrusted
	}
	for _, p := range parents {
		// Qualquer coisa que não seja EXACTAMENTE trusted contamina (untrusted ou
		// um valor não-canónico). É o "contágio" sem lavagem.
		if p != Trusted {
			return Untrusted
		}
	}
	return Trusted
}

// DataPlane é a PORTA para o data-plane de EPIC-07: renderiza memória untrusted
// como DADOS taint-marcados para o modelo, nunca como instruções e nunca no
// caminho do planeador. [ReferenceDataPlane] é a implementação de referência;
// EPIC-07 (SBX/dual-LLM/CaMeL) fornece a isoladora.
type DataPlane interface {
	// Serve renderiza um registo untrusted como um [DataItem] taint-marcado. O
	// [DataItem] devolvido NÃO tem capacidade de autorizar uma acção privilegiada.
	Serve(rec domain.Record) DataItem
}

// ReferenceDataPlane é a implementação de referência de [DataPlane]. Marca o
// conteúdo como untrusted e devolve-o como dados; a isolação real (sandbox,
// dual-LLM) é EPIC-07.
type ReferenceDataPlane struct{}

// Serve implementa [DataPlane].
func (ReferenceDataPlane) Serve(rec domain.Record) DataItem {
	return DataItem{rec: rec.Clone(), taint: Untrusted}
}

// Ingested é um registo admitido pela porta de proveniência, com a proveniência
// SELADA e imutável. O campo prov é não exportado e não há mutador: uma vez
// admitido, o estatuto de confiança não muda. O construtor ([Ingestor.Ingest],
// [Ingestor.IngestDerived], [Seal]) é a ÚNICA via de construção — clona o registo
// para que uma mutação posterior do registo do chamador não altere o selo.
type Ingested struct {
	rec  domain.Record
	prov Provenance
}

// Provenance devolve a proveniência SELADA (imutável) do registo admitido.
func (in Ingested) Provenance() Provenance { return in.prov }

// IsTrusted indica se o registo admitido é trusted (control-plane).
func (in Ingested) IsTrusted() bool { return in.prov == Trusted }

// Record devolve um CLONE do registo admitido (o estado selado nunca é partilhado
// por referência).
func (in Ingested) Record() domain.Record { return in.rec.Clone() }

// Ingestor é a porta de ENTRADA da memória: classifica a fonte, IMPÕE a
// proveniência (a marcação automática de untrusted) e sela-a. Depende da porta
// [TaintController] (EPIC-07) e, opcionalmente, de um Tracer para os spans.
type Ingestor struct {
	tc     TaintController
	tracer agentruntime.Tracer
}

// IngestorOption configura o [Ingestor].
type IngestorOption func(*Ingestor)

// WithIngestTracer injecta o Tracer dos spans de ingestão (default: NoopTracer).
func WithIngestTracer(tr agentruntime.Tracer) IngestorOption {
	return func(i *Ingestor) {
		if tr != nil {
			i.tracer = tr
		}
	}
}

// NewIngestor constrói um Ingestor. Uma porta tc nil cai no
// [DefaultTaintController] (impl de referência de EPIC-07).
func NewIngestor(tc TaintController, opts ...IngestorOption) *Ingestor {
	if tc == nil {
		tc = DefaultTaintController{}
	}
	in := &Ingestor{tc: tc, tracer: agentruntime.NoopTracer{}}
	for _, o := range opts {
		o(in)
	}
	return in
}

// Ingest admite um registo classificando a sua FONTE. A proveniência resultante
// é IMPOSTA sobre os metadados (a classificação da fonte prevalece sobre o que o
// chamador tenha posto no campo — a marcação automática é a autoridade) e depois
// selada. Valida os metadados obrigatórios (fail-closed) e devolve o registo
// admitido. Um registo inválido (ex.: sem id/classe/schema_version) é rejeitado.
func (in *Ingestor) Ingest(ctx context.Context, rec domain.Record, src Source) (Ingested, error) {
	prov := in.tc.Classify(src)
	return in.seal(ctx, rec, prov, string(src))
}

// IngestDerived admite memória DERIVADA de outra, propagando o taint dos pais
// transitivamente (ver [TaintController.Derive]). Se qualquer pai for untrusted, o
// derivado é untrusted (sem lavagem de taint).
//
// Os pais são [Ingested] JÁ SELADOS — não proveniências soltas afirmadas pelo
// chamador: a derivação lê o taint SELADO de cada pai real, pelo que é impossível
// "afirmar" um taint que não corresponde à origem (lavagem por afirmação
// incorrecta). Sem pais, o derivado é untrusted (fail-closed).
func (in *Ingestor) IngestDerived(ctx context.Context, rec domain.Record, parents ...Ingested) (Ingested, error) {
	taints := make([]Provenance, len(parents))
	for i, p := range parents {
		taints[i] = p.prov
	}
	prov := in.tc.Derive(taints...)
	return in.seal(ctx, rec, prov, string(SourceDerivedMemory))
}

// seal impõe prov sobre os metadados, valida (fail-closed) e devolve o registo
// selado, emitindo um span de ingestão.
func (in *Ingestor) seal(ctx context.Context, rec domain.Record, prov Provenance, src string) (Ingested, error) {
	_, span := in.tracer.StartSpan(ctx, opIngest)
	defer span.End()
	span.SetAttribute(attrOperation, opIngest)
	span.SetAttribute(attrSource, src)
	span.SetAttribute(attrProvenance, string(prov))

	rec.Metadata.Provenance = prov
	// Estampa a FONTE no registo persistido: a classificação trusted|untrusted
	// sozinha perde o "de onde veio". Persistir a fonte completa o triplo (fonte,
	// classificação, run_id) do AOS-042 e habilita o forense de memory poisoning.
	rec.Metadata.Source = domain.ProvenanceSource(src)
	if err := rec.Validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return Ingested{}, err
	}
	span.SetAttribute(attrResult, "admitted")
	span.SetAttribute(attrQuarantined, prov != Trusted)
	return Ingested{rec: rec.Clone(), prov: prov}, nil
}

// Seal admite um registo UNTRUSTED cuja proveniência já está decidida (ex.:
// reconstruído do log de quarentena), selando-a imutavelmente SEM a reclassificar.
//
// Seal NUNCA produz memória trusted: a tag in-band Metadata.Provenance NÃO é
// separação de privilégio (é trivial de forjar) e Seal não passa por [Classify]
// (fonte), nem por audit, nem por validação de origem independente. Confiar nela
// para fabricar um [Ingested] trusted contradiz o invariante central da barreira.
// Por isso, um registo marcado trusted é REJEITADO com [ErrSealTrustedForbidden]:
// a promoção a trusted só é legítima via [Ingestor.Ingest] (classificação pela
// fonte) ou via [Promoter.Promote] (validação explícita + hash-chain de audit).
//
// Fail-closed: um registo com proveniência ausente ou não-canónica é rejeitado
// com [domain.ErrMissingProvenance] (por rec.Validate). É esta a prova de "escrita
// sem proveniência rejeitada" ao nível desta camada.
func Seal(rec domain.Record) (Ingested, error) {
	if err := rec.Validate(); err != nil {
		return Ingested{}, err
	}
	if rec.Metadata.Provenance == Trusted {
		return Ingested{}, ErrSealTrustedForbidden
	}
	return Ingested{rec: rec.Clone(), prov: rec.Metadata.Provenance}, nil
}

// Nomes de operação e atributos de span (namespace próprio aos.memory.provenance.*).
const (
	opIngest  = "memory.provenance.ingest"
	opPromote = "memory.provenance.promote"

	attrOperation   = "aos.memory.provenance.operation"
	attrSource      = "aos.memory.provenance.source"
	attrProvenance  = "aos.memory.provenance.provenance"
	attrQuarantined = "aos.memory.provenance.quarantined"
	attrResult      = "aos.memory.provenance.result"
	attrFrom        = "aos.memory.provenance.from"
	attrTo          = "aos.memory.provenance.to"
	attrValidator   = "aos.memory.provenance.validator"
	attrMethod      = "aos.memory.provenance.method"
	attrAuditSeq    = "aos.memory.provenance.audit_seq"
)
