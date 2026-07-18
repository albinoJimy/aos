package redaction

// Source é a VIA de ingestão de onde um payload chega. É metadado de proveniência —
// as três vias exigidas pela spec (AOS-091) redigem com o MESMO motor e a MESMA
// política; a via serve o audit/observabilidade, não altera a redação.
type Source string

const (
	// SourceUserInput — input directo do utilizador (prompt).
	SourceUserInput Source = "user_input"
	// SourceToolResult — resultado de uma tool call (conteúdo untrusted).
	SourceToolResult Source = "tool_result"
	// SourceMemory — conteúdo que entra na memória (episódica/semântica).
	SourceMemory Source = "memory"
)

// Ingestor é o ponto de integração das vias de entrada: aplica o motor de redação na
// FRONTEIRA de ingestão, ANTES de qualquer persistência. As três vias
// ([Ingestor.UserInput], [Ingestor.ToolResult], [Ingestor.Memory]) delegam TODAS em
// [Ingestor.ingest] → [Engine.Redact] — a prova, por construção, de que a redação é
// consistente em todas as vias (AC "aplicação consistente em todas as vias").
//
// O wiring de produção em cada consumidor (Event Store, Memory Service, PEP) liga-se
// a este Ingestor; onde o wiring completo é grande, prova-se a aplicação por teste em
// cada via (padrão do repo — ver aos091_integration_test.go).
type Ingestor struct {
	engine *Engine
	policy Policy
}

// NewIngestor constrói um ingestor sobre um motor e uma política.
func NewIngestor(engine *Engine, policy Policy) *Ingestor {
	return &Ingestor{engine: engine, policy: policy}
}

// Ingested é o resultado de uma ingestão redigida: o payload TRATADO (pronto a
// persistir/selar) e os tokens produzidos (para o índice por-titular do audit).
type Ingested struct {
	Source  Source
	Subject string
	Payload any
	Tokens  []TokenRef
}

// ingest é o caminho ÚNICO partilhado pelas três vias.
func (in *Ingestor) ingest(src Source, subject string, payload any) (Ingested, error) {
	out, refs, err := in.engine.Redact(payload, subject, in.policy)
	if err != nil {
		return Ingested{}, err
	}
	return Ingested{Source: src, Subject: subject, Payload: out, Tokens: refs}, nil
}

// UserInput redige um input de utilizador na ingestão.
func (in *Ingestor) UserInput(subject string, payload any) (Ingested, error) {
	return in.ingest(SourceUserInput, subject, payload)
}

// ToolResult redige um resultado de tool call na ingestão.
func (in *Ingestor) ToolResult(subject string, payload any) (Ingested, error) {
	return in.ingest(SourceToolResult, subject, payload)
}

// Memory redige conteúdo que entra na memória na ingestão.
func (in *Ingestor) Memory(subject string, payload any) (Ingested, error) {
	return in.ingest(SourceMemory, subject, payload)
}
