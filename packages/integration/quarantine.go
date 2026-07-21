package integration

import (
	"context"
	"time"

	memdomain "github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/registry/revalidation"
)

// ProvenanceQuarantiner liga a porta [revalidation.Quarantiner] (AOS-051) à
// quarentena de proveniência de AOS-042 ([provenance.Partition]). Quando a
// revalidação bloqueia por DIVERGÊNCIA (drift de identidade/digest/assinatura/
// scope/egress), o artefacto divergente é ISOLADO como memória UNTRUSTED no
// data-plane de quarentena — estruturalmente incapaz de autorizar qualquer acção
// (a barreira de AOS-042: um [provenance.DataItem] não implementa
// [provenance.PrivilegedAuthorizer]).
//
// # Porquê um adaptador
//
// As duas costuras modelam a quarentena de ângulos diferentes: o REG vê-a como uma
// ACÇÃO ("isola este artefacto id/version/digest") e a proveniência como um
// CONTENTOR de registos de memória untrusted. O adaptador traduz a identidade
// pública do artefacto divergente ([revalidation.Artifact]) num registo de memória
// semântica untrusted e ADMITE-o na partição — a rota untrusted da admissão sela-o
// como dado, nunca como control-plane. Nenhum segredo nem payload de tool entra no
// registo, só a identidade pinada e a razão do isolamento.
type ProvenanceQuarantiner struct {
	partition *provenance.Partition
	agentID   string
	runID     string
	schemaVer string
	now       func() time.Time
}

// QuarantinerOption configura o [ProvenanceQuarantiner].
type QuarantinerOption func(*ProvenanceQuarantiner)

// WithQuarantineAgentID sobrepõe o AgentID forense estampado no registo de
// quarentena (default "registry.revalidation").
func WithQuarantineAgentID(id string) QuarantinerOption {
	return func(q *ProvenanceQuarantiner) {
		if id != "" {
			q.agentID = id
		}
	}
}

// WithQuarantineRunID sobrepõe o RunID forense estampado no registo (default
// "registry.revalidation"). A correlação REAL do incidente com a trajectória vive
// no audit de bloqueio do revalidator (que carrega o RunID/StepID do pedido); o
// registo de quarentena é um marcador do ARTEFACTO, não da run.
func WithQuarantineRunID(runID string) QuarantinerOption {
	return func(q *ProvenanceQuarantiner) {
		if runID != "" {
			q.runID = runID
		}
	}
}

// WithQuarantineClock injecta o relógio do carimbo CreatedAt (determinismo em
// testes). O carimbo é observacional — nunca entra numa decisão.
func WithQuarantineClock(now func() time.Time) QuarantinerOption {
	return func(q *ProvenanceQuarantiner) {
		if now != nil {
			q.now = now
		}
	}
}

// NewProvenanceQuarantiner constrói o adaptador sobre uma partição de proveniência.
// Uma partição nil cai numa partição nova com o data-plane de referência.
func NewProvenanceQuarantiner(partition *provenance.Partition, opts ...QuarantinerOption) *ProvenanceQuarantiner {
	if partition == nil {
		partition = provenance.NewPartition(nil)
	}
	q := &ProvenanceQuarantiner{
		partition: partition,
		agentID:   "registry.revalidation",
		runID:     "registry.revalidation",
		schemaVer: "1.0.0",
		now:       time.Now,
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Partition expõe a partição subjacente (para inspecção da quarentena via
// Quarantine().Items() — cada item é um [provenance.DataItem] taint-marcado).
func (q *ProvenanceQuarantiner) Partition() *provenance.Partition { return q.partition }

// Quarantine implementa [revalidation.Quarantiner]: sela a identidade do artefacto
// divergente como memória semântica UNTRUSTED e admite-a na quarentena. Um erro de
// selagem (registo inválido — não deveria acontecer) é devolvido; o revalidator
// trata-o como best-effort (nunca desbloqueia a chamada, agrava o alerta).
func (q *ProvenanceQuarantiner) Quarantine(_ context.Context, art revalidation.Artifact) error {
	rec := memdomain.Record{
		// Identidade única do artefacto divergente dentro da classe semântica.
		ID:    "quarantine:" + art.ID + "@" + art.Version + "#" + art.Digest,
		Class: memdomain.ClassSemantic,
		Body: memdomain.SemanticBody{
			// Asserção factual: <artefacto> foi <razão> em <version @ digest>.
			Subject:    art.ID,
			Predicate:  "quarantined:" + string(art.Reason),
			Object:     art.Version + " " + art.Digest,
			Confidence: 1.0,
		},
		Metadata: memdomain.Metadata{
			AgentID:    q.agentID,
			RunID:      q.runID,
			Provenance: memdomain.ProvenanceUntrusted,
			// Um rug-pull é, na prática, um schema MCP mutado — a fonte forense
			// canónica untrusted que melhor descreve a origem do artefacto divergente.
			Source:        memdomain.SourceMCPSchema,
			CreatedAt:     q.now(),
			TTLClass:      memdomain.TTLStandard,
			SchemaVersion: q.schemaVer,
		},
	}
	ingested, err := provenance.Seal(rec)
	if err != nil {
		return err
	}
	// A rota untrusted da admissão sela o registo como DADO (data-plane), nunca como
	// control-plane — a barreira estrutural de AOS-042.
	q.partition.Admit(ingested)
	return nil
}
