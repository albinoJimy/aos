package pdp

import (
	"context"
	"fmt"

	"github.com/aos-ref/platform/audit"
)

// AOS-088 — adaptador PolicyChangeEvent → changelog `policy.changed` selado na
// hash-chain WORM do audit (AC2/AC5, ADR-010/ADR-011).
//
// CAMADAS. O adaptador vive AQUI (control-plane/pdp) e importa platform/audit —
// a mesma direcção descendente já estabelecida no plano de controlo (ex.
// orchestrator → platform/identity). O inverso (audit importar o pdp para
// conhecer o [PolicyChangeEvent]) seria uma INVERSÃO de camada (platform a
// depender do control-plane) e é evitado. O [WithReloadAuditSink] continua a ser o
// ponto de ligação genérico do PDP: quem compõe o sistema decide se liga este
// sink concreto — o PDP em si permanece agnóstico ao subsistema de audit.

// PolicyChangedEventType é o rótulo estável do evento de alteração de política no
// audit (ADR-011: "evento policy.changed no audit com diff e versões").
const PolicyChangedEventType = "policy.changed"

// policyReloadCapability é o direito exercido registado no changelog: o
// carregamento de nova política. Nomeado como uma capability para uniformidade
// com o vocabulário de accountability do audit.
const policyReloadCapability = "policy:reload"

// DefaultPolicyPartition é a partição de hash-chain por omissão do changelog de
// política. Isolar a política numa partição própria dá-lhe uma cadeia contígua
// (gapless) verificável de forma independente das mediações de tool call.
const DefaultPolicyPartition = "policy"

// BuildPolicyChangedRecord traduz um [PolicyChangeEvent] num [audit.AuditRecord]
// `policy.changed`, SEM o selar (não atribui AuditSeq/PrevHash/EntryHash — isso é
// do [audit.Store.Append]). Tudo o que constitui o changelog — versões
// (old→new), autor, motivo, content_hash e o diff — entra em campos que o
// conteúdo canónico do audit sela no EntryHash, logo tamper-evident:
//   - Principal.NHIID = autor (o principal que efectuou o load, AC5);
//   - PolicyVersion   = versão nova (rastreável à assinada, AC3);
//   - Resource        = {policy.changed, content_hash} (liga o registo ao conteúdo);
//   - Obligation      = {policy.changed, Fields=diff, Params={old,new,author,reason,hash}}.
//
// Expõe-se como helper (além do sink) para que a TRADUÇÃO seja testável sem um
// store e para composição alternativa (ex. via [audit.IngestPipeline]).
func BuildPolicyChangedRecord(ev PolicyChangeEvent, partition string) audit.AuditRecord {
	if partition == "" {
		partition = DefaultPolicyPartition
	}
	params := map[string]string{
		"old_version":  ev.OldVersion,
		"new_version":  ev.NewVersion,
		"author":       ev.Author,
		"reason":       ev.Reason,
		"content_hash": ev.ContentHash,
	}
	return audit.AuditRecord{
		Partition:     partition,
		Timestamp:     ev.At,
		Decision:      audit.DecisionAllow, // a nova política foi aplicada
		Principal:     audit.Principal{NHIID: ev.Author},
		Capability:    policyReloadCapability,
		PolicyVersion: ev.NewVersion,
		Resource: audit.Resource{
			Type:  PolicyChangedEventType,
			Value: ev.ContentHash,
		},
		Obligations: []audit.Obligation{{
			Type:   PolicyChangedEventType,
			Fields: ev.Diff.Summary(),
			Params: params,
		}},
	}
}

// AuditReloadSink devolve o sink para [WithReloadAuditSink] que sela cada
// alteração de política como um registo `policy.changed` na hash-chain WORM
// (store). É o adaptador que fecha o AC2 (changelog no audit) e o AC5 (evento
// auditável com o principal).
//
// O sink DEVOLVE o erro de [audit.Store.Append] em vez de o engolir: registá-lo em
// [WithReloadAuditSink] faz o [PDP.Reload] anotar a falha de selagem no span do
// reload (`aos.policy.audit_sealed=false`), pelo que um Store WORM indisponível já
// não produz uma alteração de política SEM changelog de forma silenciosa (fecha o
// fail-open do AC2/AC5). O reload em si mantém-se aplicado (a selagem corre após a
// troca atómica do motor — AC4, sem janela de política ausente). Em produção
// compor um [audit.Store] durável/WORM. Um store nil é no-op (devolve nil).
func AuditReloadSink(store audit.Store, partition string) func(PolicyChangeEvent) error {
	return func(ev PolicyChangeEvent) error {
		if store == nil {
			return nil
		}
		if _, err := store.Append(context.Background(), BuildPolicyChangedRecord(ev, partition)); err != nil {
			return fmt.Errorf("selar changelog policy.changed (%s→%s): %w", ev.OldVersion, ev.NewVersion, err)
		}
		return nil
	}
}
