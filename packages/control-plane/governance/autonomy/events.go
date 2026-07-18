package autonomy

import (
	"context"

	"github.com/aos-ref/platform/audit"
)

// AOS-089 — as ALTERAÇÕES DE NÍVEL como EVENTOS AUDITÁVEIS selados na hash-chain
// WORM (AC5). Segue o molde do changelog policy.changed do PDP (AOS-088) e do
// retention.config.changed (AOS-092): traduz o facto num [audit.AuditRecord] cujo
// conteúdo canónico sela old→new/motivo/actor no EntryHash — logo TAMPER-EVIDENT,
// não apenas registado em memória.

// LevelChangedEventType é o rótulo estável do evento de alteração de nível no audit.
const LevelChangedEventType = "autonomy.level_changed"

// autonomyLevelCapability é a capability registada no evento (uniformidade com o
// vocabulário do audit: retention:config, policy:reload, ...).
const autonomyLevelCapability = "autonomy:set_level"

// DefaultAutonomyPartition é a partição de hash-chain por omissão dos eventos de
// autonomia. Isolá-los numa partição própria dá-lhes uma cadeia contígua (gapless)
// verificável independentemente das mediações de tool call e dos outros changelogs.
const DefaultAutonomyPartition = "autonomy"

// BuildLevelChangedRecord traduz uma [LevelChange] num [audit.AuditRecord]
// autonomy.level_changed, SEM o selar (AuditSeq/PrevHash/EntryHash são atribuídos
// por [audit.Store.Append]). Sela QUAL o par (agent/domain), a transição old→new,
// o MOTIVO e o ACTOR — todos ligados ao EntryHash pelo conteúdo canónico. Sem
// segredos: só identificadores e metadados de responsabilização.
func BuildLevelChangedRecord(ch LevelChange, partition string) audit.AuditRecord {
	if partition == "" {
		partition = DefaultAutonomyPartition
	}
	params := map[string]string{
		"agent":     ch.Agent,
		"domain":    ch.Domain,
		"old_level": ch.Old.String(),
		"new_level": ch.New.String(),
		"reason":    ch.Reason,
		"actor":     ch.Actor,
	}
	return audit.AuditRecord{
		Partition:  partition,
		Timestamp:  ch.At,
		Decision:   audit.DecisionAllow, // a alteração foi executada conforme a governação
		Principal:  audit.Principal{NHIID: ch.Actor},
		Capability: autonomyLevelCapability,
		Resource: audit.Resource{
			Type:  LevelChangedEventType,
			Value: ch.Agent + "/" + ch.Domain,
		},
		Obligations: []audit.Obligation{{
			Type:   LevelChangedEventType,
			Fields: []string{ch.Old.String(), ch.New.String()},
			Params: params,
		}},
	}
}

// AuditSink adapta um [audit.Store] à porta [Sink]: sela cada [LevelChange] como
// um registo autonomy.level_changed na hash-chain WORM. É a ligação do registo de
// níveis (GOV) ao audit tamper-evident (platform), sem acoplar o [LevelRegistry] ao
// subsistema de audit concreto.
type AuditSink struct {
	store     audit.Store
	partition string
}

// NewAuditSink constrói um [AuditSink] sobre o store dado. Uma partition vazia usa
// [DefaultAutonomyPartition].
func NewAuditSink(store audit.Store, partition string) *AuditSink {
	if partition == "" {
		partition = DefaultAutonomyPartition
	}
	return &AuditSink{store: store, partition: partition}
}

// SealLevelChange implementa [Sink]: sela a alteração na hash-chain WORM. Devolve o
// erro de [audit.Store.Append] (NÃO o engole), para que uma alteração de nível sem
// changelog selado seja detectável. Um sink/store nil é no-op (devolve nil).
func (s *AuditSink) SealLevelChange(ctx context.Context, ch LevelChange) error {
	if s == nil || s.store == nil {
		return nil
	}
	rec := BuildLevelChangedRecord(ch, s.partition)
	_, err := s.store.Append(ctx, rec)
	return err
}
