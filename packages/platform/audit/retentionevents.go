package audit

import (
	"context"
	"time"
)

// AOS-092 — a expiração por TTL e a alteração da política de retenção como
// EVENTOS AUDITÁVEIS selados na hash-chain WORM (AC5/AC4). Segue o molde do
// changelog `policy.changed` do PDP (AOS-088): traduz o facto num [AuditRecord]
// cujos campos o conteúdo canónico sela no EntryHash — logo tamper-evident, não
// apenas registado. A tradução vive AQUI (platform/audit) para ser reutilizável e
// testável sem depender do control-plane.

// Rótulos estáveis dos eventos de retenção no audit.
const (
	// RetentionExpiredEventType — um registo expirou por TTL (AC5: o quê/quando/classe).
	RetentionExpiredEventType = "retention.expired"
	// RetentionConfigChangedEventType — a política de TTL-por-classe mudou (AC4).
	RetentionConfigChangedEventType = "retention.config.changed"
)

// Capabilities registadas nos eventos (uniformidade com o vocabulário do audit).
const (
	retentionExpireCapability = "retention:expire"
	retentionConfigCapability = "retention:config"
)

// DefaultRetentionPartition é a partição de hash-chain por omissão dos eventos de
// retenção. Isolá-los numa partição própria dá-lhes uma cadeia contígua (gapless)
// verificável de forma independente das mediações de tool call e do changelog de
// política.
const DefaultRetentionPartition = "retention"

// Nomes de operação e atributos dos spans OTel de retenção (DoD AOS-092). São
// aos.* (nunca gen_ai.* de outra operação) e NUNCA transportam PII/segredos — só
// identificadores e metadados de classe/TTL/versão.
const (
	opRetentionSweep  = "aos.retention.sweep"
	opRetentionExpire = "aos.retention.expire"

	attrRetentionClass        = "aos.retention.class"
	attrRetentionTTL          = "aos.retention.ttl"
	attrRetentionConfigVer    = "aos.retention.config_version"
	attrRetentionRecordID     = "aos.retention.record_id"
	attrRetentionResult       = "aos.retention.result"
	attrRetentionExpiredCount = "aos.retention.expired_count"
	attrRetentionHeldCount    = "aos.retention.held_count"
	attrRetentionSkippedCount = "aos.retention.skipped_count"

	retentionResultExpired    = "expired"
	retentionResultSealFailed = "seal_failed"
	retentionResultSinkFailed = "sink_failed"
)

// BuildRetentionExpiredRecord traduz uma expiração num [AuditRecord]
// `retention.expired`, SEM o selar (AuditSeq/PrevHash/EntryHash são atribuídos por
// [Store.Append]). Sela o QUE expirou (record_id), QUANDO (at) e por que
// CLASSE/TTL/versão-de-política (AC5), em campos que o conteúdo canónico do audit
// liga ao EntryHash:
//   - PolicyVersion = versão da política de retenção (rastreabilidade da regra);
//   - Resource      = {retention.expired, record_id} (liga o registo ao alvo);
//   - Obligation    = {retention.expired, Params={record_id, class, ttl, subject_id,
//     partition, config_version}}.
//
// subject_id/partition são IDENTIFICADORES de responsabilização (já selados noutros
// registos via [PayloadRef]/Partition), nunca o payload pessoal.
func BuildRetentionExpiredRecord(rec ExpirableRecord, ttl time.Duration, configVersion string, at time.Time, partition string) AuditRecord {
	if partition == "" {
		partition = DefaultRetentionPartition
	}
	params := map[string]string{
		"record_id":      rec.ID,
		"class":          string(rec.Class),
		"ttl":            ttl.String(),
		"subject_id":     rec.SubjectID,
		"partition":      rec.Partition,
		"config_version": configVersion,
	}
	return AuditRecord{
		Partition:     partition,
		Timestamp:     at,
		Decision:      DecisionAllow, // a expiração foi executada conforme a política
		Capability:    retentionExpireCapability,
		PolicyVersion: configVersion,
		Resource: Resource{
			Type:  RetentionExpiredEventType,
			Value: rec.ID,
		},
		Obligations: []Obligation{{
			Type:   RetentionExpiredEventType,
			Fields: []string{string(rec.Class)},
			Params: params,
		}},
	}
}

// BuildRetentionConfigChangedRecord traduz uma ALTERAÇÃO da política de retenção
// (TTL-por-classe) num [AuditRecord] `retention.config.changed` (AC4) — tornando
// observável/auditável qualquer mudança de TTL. Sela as versões (old→new), o
// content_hash da nova política, o autor, o motivo e o diff determinista das
// classes alteradas. É o análogo, para a retenção, do `policy.changed` do PDP.
func BuildRetentionConfigChangedRecord(old, cur RetentionConfig, author, reason string, at time.Time, partition string) AuditRecord {
	if partition == "" {
		partition = DefaultRetentionPartition
	}
	params := map[string]string{
		"old_version":  old.Version(),
		"new_version":  cur.Version(),
		"author":       author,
		"reason":       reason,
		"content_hash": cur.ContentHash(),
	}
	return AuditRecord{
		Partition:     partition,
		Timestamp:     at,
		Decision:      DecisionAllow,
		Principal:     Principal{NHIID: author},
		Capability:    retentionConfigCapability,
		PolicyVersion: cur.Version(),
		Resource: Resource{
			Type:  RetentionConfigChangedEventType,
			Value: cur.ContentHash(),
		},
		Obligations: []Obligation{{
			Type:   RetentionConfigChangedEventType,
			Fields: RetentionConfigDiff(old, cur),
			Params: params,
		}},
	}
}

// SealRetentionConfigChange sela uma alteração de política de retenção como um
// registo `retention.config.changed` na hash-chain WORM (store). Devolve o erro de
// [Store.Append] (não o engole), para que uma alteração de TTL sem changelog
// selado seja detectável. Um store nil é no-op (devolve nil).
func SealRetentionConfigChange(store Store, old, cur RetentionConfig, author, reason string, at time.Time, partition string) error {
	if store == nil {
		return nil
	}
	rec := BuildRetentionConfigChangedRecord(old, cur, author, reason, at, partition)
	_, err := store.Append(context.Background(), rec)
	return err
}
