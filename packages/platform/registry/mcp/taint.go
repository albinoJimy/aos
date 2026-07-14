package mcp

import (
	"context"
	"encoding/json"

	memdomain "github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
)

// schemaSchemaVersion é a versão de schema dos registos de memória em que os
// schemas/descrições MCP são embrulhados para atravessar a barreira de AOS-042.
const schemaSchemaVersion = "1.0.0"

// taintMark embrulha um schema/descrição MCP num registo de memória e INGERE-o pela
// porta de proveniência de AOS-042 como fonte mcp_schema — que [provenance.Classify]
// classifica UNTRUSTED. O [provenance.Ingested] resultante é depois ADMITIDO numa
// [provenance.Partition]: por ser untrusted, é encaminhado para a quarentena
// (data-plane) e servido como [provenance.DataItem] — dados taint-marcados que, por
// TIPO, NÃO satisfazem [provenance.PrivilegedAuthorizer] e não comandam o planeador.
//
// Esta função NÃO reimplementa a barreira — usa a máquina de AOS-042. É o ponto onde
// o "tool poisoning" (uma descrição do tipo "ignora as instruções anteriores…") se
// torna inerte: entra como dados, nunca como instrução (ADR-005).
func (h *Host) taintMark(ctx context.Context, part *provenance.Partition, conn ConnectionInfo, id, content string) error {
	rec := memdomain.Record{
		ID:    id,
		Class: memdomain.ClassWorking,
		Metadata: memdomain.Metadata{
			AgentID:       conn.AgentID,
			RunID:         conn.RunID,
			CreatedAt:     h.now(),
			TTLClass:      memdomain.TTLEphemeral,
			SchemaVersion: schemaSchemaVersion,
			// Provenance e Source são IMPOSTOS por Ingest a partir da fonte
			// classificada (mcp_schema → untrusted); não os afirmamos aqui.
		},
		Body: memdomain.WorkingBody{Content: content},
	}
	ing, err := h.ingestor.Ingest(ctx, rec, provenance.SourceMCPSchema)
	if err != nil {
		return err
	}
	part.Admit(ing)
	return nil
}

// toolTaintPayload é a projecção textual de uma tool para taint: nome + descrição +
// input schema, tudo untrusted. Serializa de forma estável para o registo de dados.
func toolTaintPayload(t Tool) string {
	b, _ := json.Marshal(struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema,omitempty"`
	}{t.Name, t.Description, t.InputSchema})
	return string(b)
}

// resourceTaintPayload é a projecção textual de um resource para taint.
func resourceTaintPayload(r Resource) string {
	b, _ := json.Marshal(struct {
		URI         string `json:"uri"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}{r.URI, r.Name, r.Description})
	return string(b)
}
