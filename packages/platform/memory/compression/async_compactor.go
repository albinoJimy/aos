// Package compression implementa a COMPRESSÃO DE CONTEXTO ASSÍNCRONA do AOS
// (AOS-043): a sumarização auxiliar da memória de trabalho/episódica que faz caber
// a janela SEM destruir o prefix caching. A regra canónica (ADR-009, tabela de
// zonas do prompt) é inegociável: a compressão ocorre SÓ EM CHECKPOINTS
// ASSÍNCRONOS, FORA DA HOT PATH. Comprimir de forma síncrona no turno reordena/muta
// o prompt, invalida a cache do prefixo e degrada custo e latência silenciosamente
// (o risco "cache thrash invisível").
//
// # Duas peças, uma fronteira (hot path vs. checkpoint)
//
//   - [CheckpointTrigger] (checkpoint_trigger.go) — accionado pelo SINAL DE EXAUSTÃO
//     GRACIOSA a ~80% da memória de trabalho (AOS-037, working.ActionMarkForCompression).
//     NA HOT PATH só ENFILEIRA um pedido de compactação (O(1), sem tocar no Event
//     Store, na projecção nem no registo) — NUNCA corre o compactor no turno.
//   - [AsyncCompactor] (este ficheiro) — o trabalho PESADO, executado FORA do turno
//     como uma ACTIVITY DURÁVEL (ADR-001) quando o runtime chama RunCheckpoint num
//     checkpoint. É aqui, e SÓ aqui, que o compactor é invocado — provado por teste
//     (o contador de invocações fica a 0 durante os turnos).
//
// # Composição (não reimplementação)
//
// A compressão COMPÕE as fundações já entregues:
//
//   - registo (AOS-036, pacote record): a compressão NÃO trunca o registo — não há
//     descarte activo de turnos nesta via. Emite a árvore de spans da trajectória
//     (manifesto por turno + COMPRIMENTO do conteúdo cru como atributo, nunca o cru
//     em si) para o [agentruntime.Tracer] injectado ([record.Persist]). ATENÇÃO ao
//     alcance: a DURABILIDADE e RECUPERABILIDADE do conteúdo cru NÃO são fornecidas
//     por esta via — dependem de um sink durável a montante (a via de eviction /
//     EPIC-08 que recebe os spans); o Event Store desta via guarda SÓ o sumário
//     higienizado (a projecção), não o cru. Sem um Tracer real injectado (o default
//     é NoopTracer) nada do registo sai deste processo. A compressão não descarta o
//     audit trail, mas também não o torna recuperável por si só (Princípio 4: a
//     não-destruição é local; a recuperabilidade é do sink upstream);
//   - projecção (AOS-036, pacote projection): o SUMÁRIO resultante é a PROJECÇÃO
//     ([projection.ProjectContext]) — higienizada e limitada em tokens. É o que cabe
//     na janela; a via de registo emite tudo à parte;
//   - Event Store (AOS-002, ADR-007): o sumário é escrito append-only, idempotente
//     por f(run_id, checkpoint_id) — reaplicar a compactação é no-op (StatusDuplicate),
//     nunca uma dupla-compressão divergente;
//   - memória de trabalho (AOS-037): o SINAL de exaustão a ~80% acciona; a compactação
//     actua sobre o TAIL/sumários auxiliares e NUNCA sobre o prefixo (hash invariante).
//
// # Determinismo e reprodutibilidade
//
// A compactação é uma FUNÇÃO PURA da origem e da política versionada: mesma origem +
// mesma [CompressionPolicy] -> mesmo sumário byte-a-byte e mesmo Digest. Não há
// time.Now nem rand na decisão; a estimativa de tokens é pura (herdada da projecção);
// a ordem dos turnos é a de registo. Reaplicar = mesmo resultado.
package compression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/substrate/eventstore"
)

// CompressionStreamID é o stream do Event Store onde os sumários de compactação são
// escritos (append-only). Um stream dedicado dá um log ordenado e auditável das
// compactações, distinto dos streams por-run das trajectórias vivas.
const CompressionStreamID = "memory.compression.summaries"

// EventTypeContextCompacted é o tipo canónico do evento de compactação no Event Store.
const EventTypeContextCompacted = "memory.context.compacted"

// checkpointStepPrefix namespaceia o step_id do evento de sumário no Event Store. A
// idempotency_key da activity é f(run_id, checkpoint_id) = run_id + ":" +
// checkpointStepPrefix + checkpoint_id (ver [eventstore.EventInput]). Um único
// ponto de verdade para a chave — usado tanto na escrita (persistSummary) como na
// verificação de dedup ANTES do trabalho pesado (existingSummary).
const checkpointStepPrefix = "compression:checkpoint:"

// checkpointStepID deriva o step_id (namespaced) do sumário de um checkpoint. É a
// metade step_id da idempotency_key f(run_id, checkpoint_id).
func checkpointStepID(checkpointID string) string {
	return checkpointStepPrefix + checkpointID
}

// summaryEnvelopeSchemaVersion é a versão SemVer do envelope de sumário persistido.
const summaryEnvelopeSchemaVersion = "1.0.0"

// DefaultCompressionPolicyVersion é a versão SemVer da política de compressão
// default. A política é VERSIONADA (SemVer): a reprodutibilidade exige que o sumário
// seja função da origem E da versão da política — mudá-la é uma alteração observável
// e versionada, nunca silenciosa (à imagem da política de projecção de AOS-036).
const DefaultCompressionPolicyVersion = "1.0.0"

// Nomes/atributos de span da compressão (namespace próprio aos.memory.compression.*;
// a compressão não é inferência GenAI — mas o CHECKPOINT emite spans, ADR-009/DoD).
const (
	spanCompact           = "memory.compression.compact"
	attrRunID             = "aos.memory.compression.run_id"
	attrCheckpointID      = "aos.memory.compression.checkpoint_id"
	attrTraceID           = "aos.memory.compression.trace_id"
	attrPolicyVersion     = "aos.memory.compression.policy_version"
	attrPrefixHash        = "aos.prefix_hash"
	attrPrefixInvariant   = "aos.memory.compression.prefix_invariant"
	attrCacheThrash       = "aos.memory.compression.cache_thrash"
	attrFullRecordSpans   = "aos.memory.compression.full_record_spans"
	attrSummaryTokens     = "aos.memory.compression.summary_tokens"
	attrSummaryTurns      = "aos.memory.compression.summary_turns"
	attrTotalTurns        = "aos.memory.compression.total_turns"
	attrDigest            = "aos.memory.compression.digest"
	attrResult            = "aos.memory.compression.result"
	attrOffHotPathCheckpt = "aos.memory.compression.checkpoint"
)

// EventLog é o subconjunto do Event Store de que a compressão depende: Append
// (escrita append-only idempotente) e Read (reconstrução por replay). *eventstore.Store
// satisfaz esta interface.
type EventLog interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// CompressionPolicy é a POLÍTICA de compressão VERSIONADA (SemVer). Governa como o
// tail/episódica é sumarizado: a versão (para reprodutibilidade) e a política de
// projecção subjacente (orçamento de tokens + separador do sumário auxiliar). A
// mesma origem sob a mesma política produz sempre o mesmo sumário.
type CompressionPolicy struct {
	// Version é a versão SemVer da política de compressão (obrigatória, validada).
	Version string
	// Projection é a política de projecção que produz o sumário auxiliar (AOS-036).
	// Zero-value usa projection.DefaultPolicy.
	Projection projection.Policy
}

// DefaultCompressionPolicy devolve a política default (v1.0.0, projecção default ~2k
// tokens).
func DefaultCompressionPolicy() CompressionPolicy {
	return CompressionPolicy{
		Version:    DefaultCompressionPolicyVersion,
		Projection: projection.DefaultPolicy(),
	}
}

// normalized devolve a política com defaults preenchidos (projecção default se zero).
func (p CompressionPolicy) normalized() CompressionPolicy {
	if p.Version == "" {
		p.Version = DefaultCompressionPolicyVersion
	}
	if p.Projection.Version == "" {
		p.Projection = projection.DefaultPolicy()
	}
	return p
}

// Validate impõe uma política bem-formada (fail-closed): versão SemVer válida e
// política de projecção válida. Uma política inválida rejeita a compactação — nunca
// há default silencioso a meio de um checkpoint.
func (p CompressionPolicy) Validate() error {
	if !validSemVer(p.Version) {
		return ErrInvalidPolicyVersion
	}
	return p.Projection.Validate()
}

// CompactionSource é a ORIGEM de uma compactação: os turnos do tail/episódica a
// sumarizar, com o snapshot do hash do prefixo no momento do enfileiramento. Os
// turnos carregam o conteúdo cru E o manifesto por turno — são o REGISTO completo
// (Princípio 4); a compactação emite-os na íntegra para o backend e projecta o
// sumário resumido.
type CompactionSource struct {
	// RunID é o run que congela o prefixo (raiz de idempotência f(run_id, checkpoint_id)).
	RunID string
	// CheckpointID identifica o checkpoint (discriminador de idempotência da activity).
	CheckpointID string
	// TraceID liga o registo completo ao backend de observabilidade (EPIC-08).
	TraceID string
	// AgentID é a NHI que produziu a trajectória (responsabilização no Event Store).
	AgentID string
	// PrefixHash é o hash do prefixo imutável no momento do enfileiramento. A
	// compactação NÃO o muta; é a âncora de invariância/detecção de cache thrash.
	PrefixHash string
	// Turns são os turnos do tail a compactar (conteúdo cru + manifesto por turno).
	// São o REGISTO completo — nada é descartado pela via de registo.
	Turns []record.Turn
}

// validate impõe os campos mínimos (fail-closed).
func (src CompactionSource) validate() error {
	if src.RunID == "" {
		return ErrMissingRunID
	}
	if src.CheckpointID == "" {
		return ErrMissingCheckpointID
	}
	if src.TraceID == "" {
		return ErrMissingTraceID
	}
	if len(src.Turns) == 0 {
		return ErrNoTurns
	}
	return nil
}

// CompactionResult é o produto DETERMINÍSTICO de uma compactação. Reúne a prova das
// invariantes de AOS-043: sumário como projecção, registo completo intacto, prefixo
// invariante e ausência de cache thrash.
type CompactionResult struct {
	// RunID/CheckpointID/TraceID identificam a compactação.
	RunID        string
	CheckpointID string
	TraceID      string
	// PolicyVersion é a versão da política que produziu o sumário (reprodutibilidade).
	PolicyVersion string
	// PrefixHashBefore/After são o hash do prefixo antes/depois. Iguais quando o
	// prefixo é invariante (o esperado): a compressão actua no tail, nunca no prefixo.
	PrefixHashBefore string
	PrefixHashAfter  string
	// PrefixInvariant é true sse o prefixo NÃO mudou (before == after). A prova de
	// que a compressão não tocou no prefixo imutável (ADR-009).
	PrefixInvariant bool
	// CacheThrash é true sse o prefixo mudou entre checkpoint e compactação — um
	// ALERTA: a poupança de prefix caching estaria em risco.
	CacheThrash bool
	// Summary é a PROJECÇÃO resumida (o sumário auxiliar que cabe na janela). NUNCA a
	// trajectória crua — o resumo higienizado e limitado em tokens de AOS-036.
	Summary projection.InjectedView
	// FullRecordSpans é o nº de spans emitidos para o backend (registo COMPLETO). É
	// sempre estritamente MAIOR do que os turnos incluídos no sumário — a prova de que
	// o registo completo permanece enquanto o contexto recebe só o resumo (Princípio 4).
	FullRecordSpans int
	// TotalTurns é o nº total de turnos da origem (o registo mantém todos).
	TotalTurns int
	// Digest é a impressão digital reproduzível da compactação (sha256 hex de uma
	// serialização estável de política+origem+sumário). Mesma origem + política ->
	// mesmo Digest. É o que prova idempotência/reprodutibilidade em teste.
	Digest string
	// Duplicate indica que a activity já tinha sido persistida (f(run_id, checkpoint_id)):
	// reaplicar foi no-op no Event Store (sem dupla-compressão divergente).
	Duplicate bool
}

// summaryEnvelope é a forma PERSISTIDA de uma compactação no Event Store. Os campos
// de índice ficam em claro; o sumário (projecção) é serializado estável. É o REGISTO
// durável da activity (ADR-001).
type summaryEnvelope struct {
	SchemaVersion   string                  `json:"schema_version"`
	RunID           string                  `json:"run_id"`
	CheckpointID    string                  `json:"checkpoint_id"`
	TraceID         string                  `json:"trace_id"`
	PolicyVersion   string                  `json:"policy_version"`
	PrefixHash      string                  `json:"prefix_hash"`
	FullRecordSpans int                     `json:"full_record_spans"`
	TotalTurns      int                     `json:"total_turns"`
	SummaryTokens   int                     `json:"summary_tokens"`
	SummaryTurns    int                     `json:"summary_turns"`
	Digest          string                  `json:"digest"`
	Summary         projection.InjectedView `json:"summary"`
}

// AsyncCompactor executa a compactação FORA da hot path. É seguro para concorrência
// (o contador de invocações é atómico); as operações são deterministas e idempotentes.
type AsyncCompactor struct {
	es     EventLog
	tracer agentruntime.Tracer

	// invocations conta quantas vezes o trabalho PESADO de compactação correu. Fica a
	// 0 enquanto só há turnos/Observe (hot path) — é a asserção de caminho de AOS-043.
	invocations atomic.Int64
}

// CompactorOption configura o AsyncCompactor.
type CompactorOption func(*AsyncCompactor)

// WithCompactorTracer injecta a porta Tracer (default NoopTracer, zero-dep).
func WithCompactorTracer(t agentruntime.Tracer) CompactorOption {
	return func(c *AsyncCompactor) {
		if t != nil {
			c.tracer = t
		}
	}
}

// NewAsyncCompactor constrói o compactor sobre o Event Store. O log é obrigatório
// (fail-closed): a compactação é uma activity DURÁVEL (ADR-001).
func NewAsyncCompactor(es EventLog, opts ...CompactorOption) (*AsyncCompactor, error) {
	if es == nil {
		return nil, ErrNilEventLog
	}
	c := &AsyncCompactor{
		es:     es,
		tracer: agentruntime.NoopTracer{},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Invocations devolve o nº de vezes que o trabalho PESADO de compactação correu. É a
// asserção de caminho de AOS-043: durante os turnos (hot path) fica a 0; só sobe num
// checkpoint (Compact / RunCheckpoint).
func (c *AsyncCompactor) Invocations() int64 { return c.invocations.Load() }

// Compact é o trabalho PESADO de uma compactação — a ACTIVITY DURÁVEL executada FORA
// do turno. NÃO deve ser chamado na hot path: incrementa o contador de invocações
// (asserção de caminho). Compõe as duas vias do Princípio 4:
//
//  1. REGISTO: emite a trajectória COMPLETA para o backend ([record.Persist]) — nada
//     descartado; FullRecordSpans > turnos do sumário.
//  2. PROJECÇÃO: produz o SUMÁRIO auxiliar ([projection.ProjectContext]) — o resumo
//     higienizado e limitado em tokens que cabe na janela.
//
// Prefixo: NUNCA é tocado. Compara o hash do prefixo capturado na origem
// (PrefixHash) com o hash CORRENTE (nowPrefixHash, tipicamente WindowManager.PrefixHash):
// se divergiu, é CACHE THRASH — sinaliza (ErrPrefixMutated) e aborta fail-closed.
//
// Durabilidade/idempotência: escreve o sumário append-only, idempotente por
// f(run_id, checkpoint_id) — reaplicar é no-op (Duplicate=true), sem dupla-compressão
// divergente. Determinística: mesmo (origem, política) -> mesmo Summary e mesmo Digest.
func (c *AsyncCompactor) Compact(ctx context.Context, src CompactionSource, policy CompressionPolicy, nowPrefixHash string) (CompactionResult, error) {
	c.invocations.Add(1)

	if err := src.validate(); err != nil {
		return CompactionResult{}, err
	}
	policy = policy.normalized()
	if err := policy.Validate(); err != nil {
		return CompactionResult{}, err
	}

	ctx, span := c.tracer.StartSpan(ctx, spanCompact)
	defer span.End()
	span.SetAttribute(attrRunID, src.RunID)
	span.SetAttribute(attrCheckpointID, src.CheckpointID)
	span.SetAttribute(attrTraceID, src.TraceID)
	span.SetAttribute(attrPolicyVersion, policy.Version)
	span.SetAttribute(attrOffHotPathCheckpt, true)

	// DETECÇÃO DE CACHE THRASH (ADR-009): a compressão NUNCA muta o prefixo. Se o hash
	// corrente divergir do capturado no enfileiramento, algo externo invalidou o
	// prefixo — a poupança de prefix caching cai. Sinaliza e aborta fail-closed.
	after := nowPrefixHash
	if after == "" {
		after = src.PrefixHash // sem re-leitura disponível: assume invariante (por construção)
	}
	invariant := after == src.PrefixHash
	span.SetAttribute(attrPrefixHash, src.PrefixHash)
	span.SetAttribute(attrPrefixInvariant, invariant)
	if !invariant {
		span.SetAttribute(attrCacheThrash, true)
		span.SetAttribute(attrResult, "cache_thrash")
		return CompactionResult{
			RunID:            src.RunID,
			CheckpointID:     src.CheckpointID,
			TraceID:          src.TraceID,
			PolicyVersion:    policy.Version,
			PrefixHashBefore: src.PrefixHash,
			PrefixHashAfter:  after,
			PrefixInvariant:  false,
			CacheThrash:      true,
		}, ErrPrefixMutated
	}

	// IDEMPOTÊNCIA ANTES DO EFEITO COLATERAL (f(run_id, checkpoint_id)): se esta
	// activity já foi persistida (retry/failover), reidrata o resultado a partir do
	// sumário DURÁVEL e devolve no-op — SEM re-emitir record.Persist. Isto é crucial:
	// record.Persist re-emite a árvore de spans do registo completo A CADA invocação;
	// verificar a dedup só DEPOIS (no Append) deduplicaria o sumário mas deixaria o
	// tracer/backend real (EPIC-08) a receber os spans do registo duplicados a cada
	// retry. Verificar aqui torna a reaplicação um verdadeiro no-op ponta-a-ponta.
	if env, found, lerr := c.existingSummary(ctx, src.RunID, src.CheckpointID); lerr != nil {
		return CompactionResult{}, lerr
	} else if found {
		res := resultFromEnvelope(env)
		span.SetAttribute(attrFullRecordSpans, res.FullRecordSpans)
		span.SetAttribute(attrSummaryTokens, res.Summary.TokenCount)
		span.SetAttribute(attrSummaryTurns, res.Summary.IncludedTurns)
		span.SetAttribute(attrTotalTurns, res.TotalTurns)
		span.SetAttribute(attrDigest, res.Digest)
		span.SetAttribute(attrResult, "duplicate")
		return res, nil
	}

	// Constrói o REGISTO (trajectória completa) a partir dos turnos da origem. Os
	// turnos carregam RawContent + manifesto por turno — a via de registo emite tudo.
	rec := record.NewTrajectoryRecord(src.TraceID)
	for _, t := range src.Turns {
		if err := rec.AppendTurn(t); err != nil {
			return CompactionResult{}, err
		}
	}

	// (1) REGISTO: emite a árvore de spans da trajectória para o Tracer injectado
	// (manifesto por turno + comprimento do cru como atributos — NÃO o cru em si).
	// Nada é descartado nesta via; mas a recuperabilidade durável do conteúdo cru é
	// do sink a montante (EPIC-08), não do Event Store desta via (que guarda só o
	// sumário higienizado). Sem Tracer real (default Noop) o registo não sai daqui.
	ev, err := record.Persist(ctx, rec, c.tracer)
	if err != nil {
		return CompactionResult{}, err
	}

	// (2) PROJECÇÃO: o SUMÁRIO auxiliar — resumo higienizado e limitado em tokens.
	// NUNCA a trajectória crua. É o que cabe na janela.
	iv, err := projection.ProjectContext(record.View(rec), policy.Projection)
	if err != nil {
		return CompactionResult{}, err
	}

	res := CompactionResult{
		RunID:            src.RunID,
		CheckpointID:     src.CheckpointID,
		TraceID:          src.TraceID,
		PolicyVersion:    policy.Version,
		PrefixHashBefore: src.PrefixHash,
		PrefixHashAfter:  after,
		PrefixInvariant:  true,
		CacheThrash:      false,
		Summary:          iv,
		FullRecordSpans:  ev.EmittedSpans,
		TotalTurns:       len(src.Turns),
	}
	res.Digest = computeDigest(policy, src, iv)

	span.SetAttribute(attrFullRecordSpans, res.FullRecordSpans)
	span.SetAttribute(attrSummaryTokens, iv.TokenCount)
	span.SetAttribute(attrSummaryTurns, iv.IncludedTurns)
	span.SetAttribute(attrTotalTurns, res.TotalTurns)
	span.SetAttribute(attrDigest, res.Digest)

	// (3) ACTIVITY DURÁVEL (ADR-001): escreve o sumário append-only, idempotente por
	// f(run_id, checkpoint_id). Reaplicar a compactação é no-op (StatusDuplicate) —
	// sem dupla-compressão divergente.
	dup, persisted, err := c.persistSummary(ctx, src, res)
	if err != nil {
		return CompactionResult{}, err
	}
	if dup {
		// CORRIDA: outro executor persistiu esta activity entre a verificação de dedup
		// e este Append. O resultado devolvido tem de COINCIDIR com o estado durável
		// (não com o recém-computado, que poderia divergir se o conteúdo mudou sob a
		// mesma chave) — reidrata a partir do sumário REALMENTE guardado (o evento que
		// o Event Store devolve em StatusDuplicate é o committed original).
		if env, ok := decodeSummaryEnvelope(persisted.Payload); ok {
			res = resultFromEnvelope(env)
		} else {
			res.Duplicate = true
		}
		span.SetAttribute(attrResult, "duplicate")
	} else {
		span.SetAttribute(attrResult, "committed")
	}
	return res, nil
}

// persistSummary escreve o envelope de sumário no Event Store (append-only,
// idempotente por f(run_id, checkpoint_id)). Devolve true se foi um duplicado
// (activity já persistida) e o evento DURÁVEL correspondente — em StatusDuplicate o
// Event Store devolve o committed original, do qual o chamador reidrata o resultado
// para coincidir com o estado persistido (nunca com o recém-computado divergente).
func (c *AsyncCompactor) persistSummary(ctx context.Context, src CompactionSource, res CompactionResult) (bool, eventstore.Event, error) {
	env := summaryEnvelope{
		SchemaVersion:   summaryEnvelopeSchemaVersion,
		RunID:           res.RunID,
		CheckpointID:    res.CheckpointID,
		TraceID:         res.TraceID,
		PolicyVersion:   res.PolicyVersion,
		PrefixHash:      res.PrefixHashAfter,
		FullRecordSpans: res.FullRecordSpans,
		TotalTurns:      res.TotalTurns,
		SummaryTokens:   res.Summary.TokenCount,
		SummaryTurns:    res.Summary.IncludedTurns,
		Digest:          res.Digest,
		Summary:         res.Summary,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return false, eventstore.Event{}, err
	}
	appendRes, err := c.es.Append(ctx, CompressionStreamID, eventstore.EventInput{
		Type:     EventTypeContextCompacted,
		Payload:  payload,
		RunID:    src.RunID,
		StepID:   checkpointStepID(src.CheckpointID),
		Producer: eventstore.Producer{NHIID: src.AgentID},
	})
	if err != nil {
		return false, eventstore.Event{}, err
	}
	return appendRes.Status == eventstore.StatusDuplicate, appendRes.Event, nil
}

// existingSummary procura, por replay do stream de compactações, o sumário DURÁVEL
// de f(run_id, checkpoint_id) — a verificação de idempotência ANTES do trabalho
// pesado (record.Persist/projecção). Devolve (env, true, nil) se a activity já foi
// persistida; (·, false, nil) se ainda não (stream inexistente = log vazio). É a
// mesma chave que persistSummary escreve (run_id + step_id namespaced), pelo que a
// verificação e a escrita nunca divergem.
func (c *AsyncCompactor) existingSummary(ctx context.Context, runID, checkpointID string) (summaryEnvelope, bool, error) {
	events, err := c.es.Read(ctx, CompressionStreamID, 1)
	if err != nil {
		if errorsIsStreamNotFound(err) {
			return summaryEnvelope{}, false, nil
		}
		return summaryEnvelope{}, false, err
	}
	stepID := checkpointStepID(checkpointID)
	// Do mais recente para o mais antigo: o primeiro committed com a chave é o
	// autoritativo (o append-only garante um único committed por idempotency_key).
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != EventTypeContextCompacted {
			continue
		}
		if ev.RunID != runID || ev.StepID != stepID {
			continue
		}
		env, ok := decodeSummaryEnvelope(ev.Payload)
		if !ok {
			return summaryEnvelope{}, false, ErrCorruptSummary
		}
		return env, true, nil
	}
	return summaryEnvelope{}, false, nil
}

// decodeSummaryEnvelope desserializa um envelope de sumário persistido. Devolve
// false se o payload não for um envelope válido (corrupção/schema incompatível).
func decodeSummaryEnvelope(payload []byte) (summaryEnvelope, bool) {
	var env summaryEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return summaryEnvelope{}, false
	}
	return env, true
}

// resultFromEnvelope reconstrói o [CompactionResult] a partir do sumário DURÁVEL
// (idempotência: o resultado devolvido coincide sempre com o estado persistido, não
// com um valor recém-computado). Marca Duplicate=true; o prefixo é invariante por
// construção (só sumários com prefixo invariante são persistidos).
func resultFromEnvelope(env summaryEnvelope) CompactionResult {
	return CompactionResult{
		RunID:            env.RunID,
		CheckpointID:     env.CheckpointID,
		TraceID:          env.TraceID,
		PolicyVersion:    env.PolicyVersion,
		PrefixHashBefore: env.PrefixHash,
		PrefixHashAfter:  env.PrefixHash,
		PrefixInvariant:  true,
		CacheThrash:      false,
		Summary:          env.Summary,
		FullRecordSpans:  env.FullRecordSpans,
		TotalTurns:       env.TotalTurns,
		Digest:           env.Digest,
		Duplicate:        true,
	}
}

// CompactedSummary é um sumário de compactação reconstruído do log (recuperação).
type CompactedSummary struct {
	RunID           string
	CheckpointID    string
	TraceID         string
	PolicyVersion   string
	PrefixHash      string
	FullRecordSpans int
	TotalTurns      int
	Digest          string
	Summary         projection.InjectedView
}

// Summaries reconstrói, por replay do log, todos os sumários de compactação (ordem
// de escrita). O Event Store é a fonte de verdade; não há estado autoritativo em RAM.
func (c *AsyncCompactor) Summaries(ctx context.Context) ([]CompactedSummary, error) {
	events, err := c.es.Read(ctx, CompressionStreamID, 1)
	if err != nil {
		if errorsIsStreamNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]CompactedSummary, 0, len(events))
	for _, ev := range events {
		if ev.Type != EventTypeContextCompacted {
			continue
		}
		env, ok := decodeSummaryEnvelope(ev.Payload)
		if !ok {
			return nil, ErrCorruptSummary
		}
		out = append(out, CompactedSummary{
			RunID:           env.RunID,
			CheckpointID:    env.CheckpointID,
			TraceID:         env.TraceID,
			PolicyVersion:   env.PolicyVersion,
			PrefixHash:      env.PrefixHash,
			FullRecordSpans: env.FullRecordSpans,
			TotalTurns:      env.TotalTurns,
			Digest:          env.Digest,
			Summary:         env.Summary,
		})
	}
	return out, nil
}

// computeDigest calcula a impressão digital REPRODUZÍVEL da compactação: sha256 de
// uma serialização ESTÁVEL de (versão da política, manifesto por turno da origem,
// bytes do sumário). Determinística — mesma origem + política -> mesmo digest — sem
// depender da ordem de iteração de mapas nem de time.Now/rand.
func computeDigest(policy CompressionPolicy, src CompactionSource, iv projection.InjectedView) string {
	var b strings.Builder
	b.WriteString("compression-policy=")
	b.WriteString(policy.Version)
	b.WriteString("\nrun=")
	b.WriteString(src.RunID)
	b.WriteString("\ncheckpoint=")
	b.WriteString(src.CheckpointID)
	b.WriteString("\ntrace=")
	b.WriteString(src.TraceID)
	b.WriteString("\nprefix=")
	b.WriteString(src.PrefixHash)
	b.WriteString("\nturns=")
	b.WriteString(strconv.Itoa(len(src.Turns)))
	for _, t := range src.Turns {
		b.WriteString("\nturn=")
		b.WriteString(strconv.Itoa(t.Index))
		b.WriteString("|prompt=")
		b.WriteString(t.PromptHash)
		b.WriteString("|model=")
		b.WriteString(t.ModelID)
		b.WriteString("|asm=")
		b.WriteString(t.AssemblyVersion)
		b.WriteString("|manifest=")
		b.WriteString(t.ManifestSchemaVersion)
		b.WriteString("|rawlen=")
		b.WriteString(strconv.Itoa(len(t.RawContent)))
	}
	// Os bytes do sumário são já uma serialização estável e determinística (AOS-036).
	b.WriteString("\nsummary=\n")
	b.Write(iv.Bytes())
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// errorsIsStreamNotFound isola a dependência do sentinela do Event Store (um stream
// ainda sem eventos não é erro — é log vazio).
func errorsIsStreamNotFound(err error) bool {
	return errors.Is(err, eventstore.ErrStreamNotFound)
}

// validSemVer aceita um "MAJOR.MINOR.PATCH" numérico simples (sem pré-lançamento).
// Deliberadamente restrito — evita puxar dependências de parsing (coerente com a
// política de projecção de AOS-036).
func validSemVer(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}
