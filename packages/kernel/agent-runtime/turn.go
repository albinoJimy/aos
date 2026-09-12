package agentruntime

import (
	"context"
	"encoding/json"

	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeTurnRecorded é o tipo canónico do evento de turno (tecnica/13 §3).
const EventTypeTurnRecorded = "turn.recorded"

// ManifestSchemaVersion é a versão do schema do manifesto por trajectória
// serializado no payload (tecnica/13 §6).
const ManifestSchemaVersion = "1.0"

// EventAppender é o subconjunto do Event Store de que o [TurnRecorder] depende.
// Mantê-lo mínimo desacopla o RT da superfície completa do store e permite fakes
// em teste. *eventstore.Store satisfaz esta interface.
type EventAppender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// ModelManifest é a parte do manifesto que pina o modelo (model_id/params/seed).
type ModelManifest struct {
	ModelID string            `json:"model_id"`
	Params  map[string]string `json:"params,omitempty"`
	Seed    int64             `json:"seed"`
}

// PinnedDep é uma dependência pinada no manifesto (tool ou skill): nome+versão+
// digest (+ servidor MCP). Espelha tecnica/13 §6.
type PinnedDep struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Digest    string `json:"digest,omitempty"`
	MCPServer string `json:"mcp_server,omitempty"`
}

// Manifest é o MANIFESTO por trajectória gravado em cada turno (ADR-010): o
// conjunto mínimo que torna o replay fiel mesmo após evolução de código —
// prompt_hash, modelo, versão do assembler, hash do system e dependências
// pinadas (tools/skills). Correlaciona-se por run_id/step_id no envelope.
type Manifest struct {
	SchemaVersion   string        `json:"schema_version"`
	PromptHash      string        `json:"prompt_hash"`
	SystemHash      string        `json:"system_hash"`
	AssemblyVersion string        `json:"assembly_version"`
	Model           ModelManifest `json:"model"`
	Tools           []PinnedDep   `json:"tools,omitempty"`
	Skills          []PinnedDep   `json:"skills,omitempty"`
}

// turnPayload é o corpo JSON do evento "turn.recorded". Contém o manifesto por
// trajectória e o burn-down de custo/uso do turno. NÃO contém segredos nem o
// prompt materializado cru — apenas o seu hash (contexto ≠ registo, tecnica/13).
type turnPayload struct {
	Turn         int      `json:"turn"`
	Manifest     Manifest `json:"manifest"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	CostMicroUSD int64    `json:"cost_micro_usd"`
	// UsageAusente marca que este turno NÃO FOI MEDIDO: os três campos acima não são
	// uma leitura de zero, são a ausência de leitura (AOS-336). Quem soma o ledger tem
	// de os distinguir — contar um turno não medido como gratuito é o zero silencioso
	// que faz um run queimar o tecto com o burn-down a marcar 0%.
	//
	// `omitempty` é DELIBERADO e não cosmética: um turno medido grava exactamente os
	// mesmos bytes que gravava antes desta mudança, pelo que nenhum golden de replay
	// se move e o campo só aparece onde diz alguma coisa.
	UsageAusente bool `json:"usage_ausente,omitempty"`
	// ToolCallsRequested é o nº de tool calls que o modelo pediu neste turno
	// (despachadas via RM, cada uma auditada no seu próprio evento de mediação).
	ToolCallsRequested int `json:"tool_calls_requested"`
	// Final indica se este turno produziu a resposta final.
	Final bool `json:"final"`
}

// TurnRecord é o input que o [Runtime] passa ao [TurnRecorder] por turno.
type TurnRecord struct {
	RunID        string
	StepID       string
	ParentStepID string
	Turn         int
	Manifest     Manifest
	Usage        Usage
	CostMicroUSD int64
	ToolCalls    int
	Final        bool
	Producer     eventstore.Producer
}

// TurnRecorder grava cada turno como um evento "turn.recorded" no Event Store,
// com o manifesto por trajectória. É a materialização durável do critério de
// aceitação AOS-013 ("cada turno é gravado com hash do prompt, model-id/params/
// seed, versão do assembler e versões pinadas").
type TurnRecorder struct {
	store EventAppender
}

// NewTurnRecorder constrói um recorder sobre o Event Store dado.
func NewTurnRecorder(store EventAppender) *TurnRecorder {
	return &TurnRecorder{store: store}
}

// Record grava um turno. O stream_id é o run_id; o step_id é DISTINTO por turno
// (o [Runtime] garante-o) para não colidir com a deduplicação por idempotency_key
// do Event Store (run_id:step_id). Devolve o seq atribuído.
func (r *TurnRecorder) Record(ctx context.Context, rec TurnRecord) (uint64, error) {
	m := rec.Manifest
	if m.SchemaVersion == "" {
		m.SchemaVersion = ManifestSchemaVersion
	}
	if m.AssemblyVersion == "" {
		m.AssemblyVersion = AssemblyVersion
	}
	payload := turnPayload{
		Turn:               rec.Turn,
		Manifest:           m,
		InputTokens:        rec.Usage.InputTokens,
		OutputTokens:       rec.Usage.OutputTokens,
		CostMicroUSD:       rec.CostMicroUSD,
		UsageAusente:       !rec.Usage.Definido(),
		ToolCallsRequested: rec.ToolCalls,
		Final:              rec.Final,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	in := eventstore.EventInput{
		Type:         EventTypeTurnRecorded,
		Payload:      raw,
		RunID:        rec.RunID,
		StepID:       rec.StepID,
		ParentStepID: rec.ParentStepID,
		Producer:     rec.Producer,
	}
	res, err := r.store.Append(ctx, rec.RunID, in)
	if err != nil {
		return 0, err
	}
	return res.Seq, nil
}
