package dsar

import (
	"context"

	audit "github.com/aos-ref/platform/audit"
)

// ShreddableKeyStore é a PORTA de um store de PII cifrada por-titular que suporta
// crypto-shredding. O fluxo DSAR destrói a chave do titular em CADA store ligado,
// para a erasure ser UNIFICADA — não deixar PII recuperável num store esquecido.
// Implementações de referência: [AuditStore] (KeyVault do audit, AOS-083) e
// [RedactionStore] (KeySource de tokenização, AOS-091).
type ShreddableKeyStore interface {
	// Name identifica o store nos rótulos do evento auditável. É um rótulo estável
	// (ex.: "audit", "redaction") — NUNCA PII nem material de chave.
	Name() string
	// Shred destrói a chave por-titular (crypto-shredding, Art. 17). Idempotente:
	// apagar uma chave ausente é no-op SEM erro. NUNCA devolve nem expõe a chave — o
	// segredo é destruído server-side (ADR-006). Devolve erro só se a destruição foi
	// RECUSADA (ex.: legal hold re-checado no store) — o fluxo trata-o fail-closed.
	//
	// A enforcement do legal hold NÃO depende de cada store a re-checar: o [Flow]
	// re-consulta o [HoldOracle] imediatamente ANTES de invocar Shred em cada store
	// (enforcement autoritativa por-store, independente da ordem de wiring). Um store
	// que também re-check o hold (ex.: o audit.Shredder) acrescenta defesa-em-
	// profundidade, mas a segurança do hold não repousa nessa ordem.
	Shred(subjectID string) error
}

// HoldOracle indica se um titular está sob legal hold (obrigação de preservação que
// SUSPENDE o apagamento, AOS-092/083). O [*audit.Shredder] satisfá-la via Held
// (subject OU qualquer partição com dados do titular, fail-closed). O fluxo DSAR
// consulta-a ANTES de destruir qualquer chave: retido ⇒ bloqueia.
type HoldOracle interface {
	Held(subjectID string) bool
}

// EventSealer sela um evento de conformidade na hash-chain de audit tamper-evident.
// O [*audit.IngestPipeline] satisfá-la: Ingest de um RawRecord SEM PII sela apenas
// os metadados (sem PayloadRef) — é assim que dsar.received/blocked/key_destroyed
// entram na cadeia sem expor qualquer valor pessoal.
type EventSealer interface {
	Ingest(ctx context.Context, raw audit.RawRecord) (audit.AuditRecord, error)
}
