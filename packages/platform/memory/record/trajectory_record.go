// Package record materializa o lado REGISTO do Princípio 4 (contexto ≠ registo,
// AOS-036): a trajectória COMPLETA persistida no backend. É uma das duas vias
// fisicamente separadas da projecção de contexto (ver o pacote irmão projection):
//
//   - persist (aqui): grava SEMPRE a trajectória completa — cada turno com o seu
//     conteúdo cru E o manifesto por turno (hash do prompt materializado, model-id,
//     versões), mais a árvore de spans completa. Descartar do registo NUNCA é
//     legítimo e NÃO existe operação que o permita;
//   - project_context (pacote projection): produz o que o MODELO vê (resumo
//     higienizado, limitado em tokens). Recebe apenas uma vista READ-ONLY do
//     registo — [RecordView] — pelo que é ARQUITECTURALMENTE impossível apagar ou
//     mutar o registo a partir da camada de projecção.
//
// A barreira é a NÍVEL DE TIPO, não por convenção: [RecordView] não expõe qualquer
// método de escrita/apagamento, e [View] devolve um wrapper de tipo NÃO exportado
// (não reconvertível para o registo mutável por type-assertion). É provado por
// teste (barrier_test / record_barrier).
package record

import (
	"sort"
)

// RecordSchemaVersion é a versão SemVer do schema do registo de trajectória.
// Alterações ao layout serializado seguem SemVer (MAJOR para quebras).
const RecordSchemaVersion = "1.0.0"

// Turn é UM turno da trajectória, com o manifesto por turno que alimenta o replay
// fiel (ADR-010) INDEPENDENTEMENTE do que foi higienizado no contexto. Reutiliza o
// conceito do manifesto por trajectória do Agent Runtime (AOS-013/016): o hash do
// prompt materializado, o model-id/params e as versões são gravados por turno.
type Turn struct {
	// Index é o índice (1-based) do turno no run.
	Index int
	// PromptHash é o hash do PROMPT MATERIALIZADO do turno ("sha256:<hex>"), a
	// âncora de replay (ADR-010). Gravado por turno mesmo que o contexto injectado
	// tenha higienizado/descartado o conteúdo — o registo mantém-no sempre.
	PromptHash string
	// ModelID é o identificador do modelo do turno (gen_ai.request.model).
	ModelID string
	// Params são os parâmetros do modelo (temperatura, top_p, seed, …). Emitidos
	// por ordem estável de chave (determinismo); um mapa vazio é aceite.
	Params map[string]string
	// AssemblyVersion é a versão do assembler que materializou o prompt (ADR-010).
	AssemblyVersion string
	// ManifestSchemaVersion é a versão do schema do manifesto por turno.
	ManifestSchemaVersion string
	// RawContent é o conteúdo CRU e COMPLETO do turno. É o que NUNCA é descartado do
	// registo (contexto ≠ registo). A projecção nunca lê este campo.
	RawContent string
	// Summary é o resumo higienizado do turno — a matéria-prima da projecção
	// (contexto injectado). Descartar/truncar isto na projecção é legítimo.
	Summary string
}

// manifestComplete indica se o turno traz o manifesto mínimo por turno exigido
// pela AOS-036 (prompt_hash + model-id + versões). Fail-closed: um turno sem estes
// campos é rejeitado por [TrajectoryRecord.AppendTurn].
func (t Turn) manifestComplete() bool {
	return t.PromptHash != "" && t.ModelID != "" && t.AssemblyVersion != "" && t.ManifestSchemaVersion != ""
}

// clone devolve uma cópia independente do turno (o mapa Params é copiado) para que
// o registo nunca partilhe estado mutável com o chamador nem com a projecção.
func (t Turn) clone() Turn {
	cp := t
	if t.Params != nil {
		cp.Params = make(map[string]string, len(t.Params))
		for k, v := range t.Params {
			cp.Params[k] = v
		}
	}
	return cp
}

// SortedParamKeys devolve as chaves de Params por ordem lexicográfica estável.
// Serve a emissão determinística dos parâmetros como atributos de span (persist) —
// nunca se itera o mapa directamente (a ordem de iteração de um mapa Go é aleatória).
func (t Turn) SortedParamKeys() []string {
	keys := make([]string, 0, len(t.Params))
	for k := range t.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Span é UM nó da árvore de spans da trajectória. A árvore COMPLETA vai sempre para
// o backend de observabilidade (EPIC-08) na via persist, ligada ao trace por
// TraceID; a projecção ao pai leva apenas o resumo. Attributes é emitido por ordem
// estável de chave.
type Span struct {
	// ID é o identificador do span.
	ID string
	// ParentID é o span pai ("" para a raiz).
	ParentID string
	// Name é o nome da operação do span.
	Name string
	// Attributes são os atributos do span (emitidos por ordem estável de chave).
	Attributes map[string]string
}

// SortedAttrKeys devolve as chaves de Attributes por ordem lexicográfica estável.
func (s Span) SortedAttrKeys() []string {
	keys := make([]string, 0, len(s.Attributes))
	for k := range s.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s Span) clone() Span {
	cp := s
	if s.Attributes != nil {
		cp.Attributes = make(map[string]string, len(s.Attributes))
		for k, v := range s.Attributes {
			cp.Attributes[k] = v
		}
	}
	return cp
}

// TrajectoryRecord é o REGISTO: a trajectória completa de um run. É APPEND-ONLY —
// os únicos mutadores são [TrajectoryRecord.AppendTurn] e
// [TrajectoryRecord.AppendSpan]. NÃO existe qualquer método que apague ou mute um
// turno/span já registado: descartar do registo nunca é legítimo (Princípio 4).
//
// A projecção NUNCA recebe este tipo — recebe a vista read-only [RecordView] via
// [View], que não expõe os mutadores. É esta segregação a nível de tipo que torna a
// higiene de contexto incapaz de corroer o audit trail.
type TrajectoryRecord struct {
	traceID string
	turns   []Turn
	spans   []Span
}

// NewTrajectoryRecord cria um registo vazio ligado ao trace dado. O TraceID liga a
// trajectória à árvore de spans OTel (run_id/trace_id) e o resumo do sub-agente ao
// pai ao mesmo trace (EPIC-08).
func NewTrajectoryRecord(traceID string) *TrajectoryRecord {
	return &TrajectoryRecord{traceID: traceID}
}

// AppendTurn regista um turno (append-only). Fail-closed: um turno sem o manifesto
// mínimo por turno (prompt_hash + model-id + assembly_version + manifest_schema)
// é rejeitado com [ErrIncompleteTurnManifest] e NÃO é registado. Guarda um clone,
// para que o registo não partilhe estado mutável com o chamador.
func (r *TrajectoryRecord) AppendTurn(t Turn) error {
	if !t.manifestComplete() {
		return ErrIncompleteTurnManifest
	}
	r.turns = append(r.turns, t.clone())
	return nil
}

// AppendSpan regista um nó da árvore de spans (append-only). Guarda um clone.
func (r *TrajectoryRecord) AppendSpan(s Span) {
	r.spans = append(r.spans, s.clone())
}

// TraceID devolve o trace a que a trajectória está ligada.
func (r *TrajectoryRecord) TraceID() string { return r.traceID }

// TurnCount devolve o nº de turnos registados.
func (r *TrajectoryRecord) TurnCount() int { return len(r.turns) }

// SpanCount devolve o nº de nós de span registados.
func (r *TrajectoryRecord) SpanCount() int { return len(r.spans) }

// turnsClone devolve cópias independentes de todos os turnos (ordem de registo).
func (r *TrajectoryRecord) turnsClone() []Turn {
	out := make([]Turn, len(r.turns))
	for i, t := range r.turns {
		out[i] = t.clone()
	}
	return out
}

// spansClone devolve cópias independentes de todos os spans (ordem de registo).
func (r *TrajectoryRecord) spansClone() []Span {
	out := make([]Span, len(r.spans))
	for i, s := range r.spans {
		out[i] = s.clone()
	}
	return out
}
