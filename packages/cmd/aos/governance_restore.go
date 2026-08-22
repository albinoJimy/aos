package main

// RE-HIDRATAÇÃO DO ESTADO DE GOVERNAÇÃO A PARTIR DO SUBSTRATO DURÁVEL (remediação da W6,
// eixo AOS-267).
//
// O DEFEITO QUE FECHA. Três estruturas que decidem se material pessoal é DESTRUÍDO viviam
// exclusivamente na memória do processo, enquanto o facto que as origina vive numa cadeia
// tamper-evident DURÁVEL:
//
//	(1) [audit.LegalHold] — construído vazio a cada arranque, apesar de cada `POST /dsar/hold`
//	    ficar selado para sempre na cadeia [legalHoldPartition]. Depois de um restart (deploy,
//	    rollout, OOM), o WORM continuava a AFIRMAR que o titular estava sob preservação e o nó
//	    já não o sabia.
//	(2) [audit.InMemorySubjectPartitionIndex] — o mapa titular→partições que faz valer o hold
//	    POR-PARTIÇÃO nas DEMAIS partições do titular. Vazio ao arranque, só se repovoava à
//	    medida que conteúdo NOVO era selado: um titular antigo perdia a cobertura por-partição.
//	(3) o seen-set da idempotência do [audit.ExpirationJob] — sem ele, o primeiro varrimento
//	    após cada restart RE-SELA um segundo `retention.expired` para cada facto já selado
//	    (os eventos cifrados continuam no Event Store; o crypto-shred destrói a KEK, não os
//	    registos), poluindo a cadeia gapless e re-contando expirações.
//
// PORQUE É QUE A W6 TORNOU ISTO UM INCIDENTE, e não uma lacuna latente. Até AOS-267 a
// expiração exigia `POST /dsar/expire` — um humano com credencial forte de governação, que
// sabe do hold e o assume. O scheduler interno remove o humano e torna a destruição periódica
// e não-supervisionada: um nó reiniciado apagaria, até uma hora depois e sem ninguém no
// caminho, a KEK de um titular sob ordem de preservação — irreversível — e selaria na cadeia
// `legal_hold: enforced`, uma afirmação FALSA na passagem que destruiu material retido.
//
// A DISCIPLINA. Nada aqui INFERE estado: cada hold reposto vem de um registo `place`/`release`
// SELADO, por ordem de audit_seq (a cadeia gapless é ela própria o log de reprodução — o
// último facto sobre um alvo ganha, que é a semântica de [audit.LegalHold]). Nada aqui APAGA:
// as três funções só ACRESCENTAM ao estado em memória. E FAIL-CLOSED: um erro de leitura NÃO
// aborta o arranque (a via com operador continua a servir) mas deixa a re-hidratação por
// PROVAR, e [retentionSchedulerArmed] recusa armar o varredor automático sem essa prova — a
// não-nulidade do objecto nunca foi prova de que o CONTEÚDO sobreviveu ao restart.

import (
	"context"
	"fmt"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// restoreLegalHolds repõe em `holds` os legal holds que a cadeia [legalHoldPartition] ainda
// AFIRMA estarem em vigor, reproduzindo por ordem de audit_seq os selos de place/release que
// [apiHandler.sealLegalHold] escreveu. Devolve o número de alvos (titulares + partições) sob
// preservação no fim da reprodução.
//
// O alvo de cada selo vem dos Params da obrigação [legalHoldTargetObl] (`subject_id`/
// `partition`), que é onde a selagem os põe; o [audit.Resource] é usado como recurso de
// recurso para um registo cuja obrigação não seja projectável (nunca acontece na via actual,
// mas um selo antigo não pode fazer perder uma preservação).
func restoreLegalHolds(ctx context.Context, store audit.Store, holds *audit.LegalHold) (int, error) {
	if store == nil || holds == nil {
		return 0, nil
	}
	head, err := store.Head(ctx, legalHoldPartition)
	if err != nil {
		return 0, fmt.Errorf("legal hold: head da cadeia %q: %w", legalHoldPartition, err)
	}
	if head == 0 {
		return 0, nil // cadeia vazia: nunca houve hold neste substrato
	}
	recs, err := store.Read(ctx, legalHoldPartition, 1, head)
	if err != nil {
		return 0, fmt.Errorf("legal hold: leitura da cadeia %q: %w", legalHoldPartition, err)
	}
	// Estado final por alvo: o último facto selado sobre ele ganha. Reproduz-se por ordem de
	// armazenamento (a cadeia é append-only e gapless, logo a ordem de leitura É a cronologia).
	subjects := make(map[string]bool)
	partitions := make(map[string]bool)
	for _, rec := range recs {
		var place bool
		switch rec.Capability {
		case capLegalHoldPlace:
			place = true
		case capLegalHoldRelease:
			place = false
		default:
			continue // outro registo nesta partição: não é um facto de preservação
		}
		subject, partition := legalHoldTargetOf(rec)
		if subject != "" {
			subjects[subject] = place
		}
		if partition != "" {
			partitions[partition] = place
		}
	}
	n := 0
	for subject, held := range subjects {
		if held {
			holds.HoldSubject(subject)
			n++
		}
	}
	for partition, held := range partitions {
		if held {
			holds.HoldPartition(partition)
			n++
		}
	}
	return n, nil
}

// legalHoldTargetOf extrai o alvo (titular pseudónimo / partição opaca) de um selo de legal
// hold. Ambos são identificadores OPACOS por contrato de rota ([validPseudonym]) — nunca PII.
func legalHoldTargetOf(rec audit.AuditRecord) (subject, partition string) {
	for _, ob := range rec.Obligations {
		if ob.Type != legalHoldTargetObl {
			continue
		}
		subject, partition = ob.Params["subject_id"], ob.Params["partition"]
	}
	if subject == "" && partition == "" {
		// Recurso de recurso: o Resource nomeia o alvo PRIMÁRIO do selo.
		switch rec.Resource.Type {
		case subjectResourceType:
			subject = rec.Resource.Value
		case partitionResourceType:
			partition = rec.Resource.Value
		}
	}
	return subject, partition
}

// restoreSubjectIndex repõe o mapa titular→partições a partir do MESMO substrato que
// [eventStoreRecordSource.List] varre: cada evento cifrado por-titular do Event Store liga o
// seu titular ao stream onde vive. Sem isto, um legal hold POR-PARTIÇÃO colocado sobre uma
// partição do titular deixaria de cobrir, após um restart, os registos desse titular noutras
// partições — a via exacta que [audit.ExpirationJob.held] consulta pelo índice.
//
// Só ACRESCENTA ligações (Link é idempotente) e nunca lê conteúdo (que é opaco/cifrado).
func restoreSubjectIndex(ctx context.Context, es *eventstore.Store, index *audit.InMemorySubjectPartitionIndex) (int, error) {
	if es == nil || index == nil {
		return 0, nil
	}
	n := 0
	for _, stream := range es.Streams() {
		events, err := es.Read(ctx, stream, 1)
		if err != nil {
			return n, fmt.Errorf("indice titular->particao: leitura do stream: %w", err)
		}
		for _, e := range events {
			subject := subjectOf(e)
			if subject == "" {
				continue
			}
			index.Link(subject, stream)
			n++
		}
	}
	return n, nil
}

// restoreExpirationIdempotency reconstrói o seen-set do [audit.ExpirationJob] a partir dos
// eventos `retention.expired` JÁ SELADOS na cadeia de retenção. A chave é a do próprio pacote
// de auditoria ([audit.IdempotencyKeyFor]) sobre os campos que o evento sela (`record_id` +
// `class`), pelo que não há um segundo formato a divergir.
//
// Efeito: um registo cuja expiração já foi selada NÃO é re-selado no primeiro varrimento após
// o restart — é contado em `skipped`, como numa segunda passagem do mesmo processo. O
// crypto-shred permanece idempotente e continua a ser reconciliado pelo sink.
func restoreExpirationIdempotency(ctx context.Context, store audit.Store, partition string) (audit.IdempotencyStore, int, error) {
	idem := audit.NewInMemoryIdempotencyStore()
	if store == nil {
		return idem, 0, nil
	}
	head, err := store.Head(ctx, partition)
	if err != nil {
		return idem, 0, fmt.Errorf("idempotencia de retencao: head da cadeia %q: %w", partition, err)
	}
	if head == 0 {
		return idem, 0, nil
	}
	recs, err := store.Read(ctx, partition, 1, head)
	if err != nil {
		return idem, 0, fmt.Errorf("idempotencia de retencao: leitura da cadeia %q: %w", partition, err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Resource.Type != audit.RetentionExpiredEventType {
			continue // selos do varrimento (started/completed) não são factos por-registo
		}
		for _, ob := range rec.Obligations {
			if ob.Type != audit.RetentionExpiredEventType {
				continue
			}
			id, class := ob.Params["record_id"], ob.Params["class"]
			if id == "" || class == "" {
				continue
			}
			idem.Add(audit.IdempotencyKeyFor(id, audit.DataClass(class)))
			n++
		}
	}
	return idem, n, nil
}

// ---------------------------------------------------------------------------------------------
// O DESMENTIDO SOBREVIVE AO RESTART.
//
// Segunda metade do achado 1.6 da varredura adversarial de 2026-08-21, demonstrada com output:
//
//	DEPOIS DE UM RESTART:  chave viva? true    prontidão? VERDE    por confirmar: 0
//
// O conjunto de destruições por confirmar vivia só em memória, no adaptador do Vault. Um restart
// — um deploy, um OOM, uma máquina que reinicia — apagava o desmentido e deixava a cadeia sozinha
// a afirmar uma irrecuperabilidade por provar, com o /readyz verde a dizer que estava tudo bem.
//
// É O MESMO EIXO QUE O `holdsRestored` JÁ FECHOU para o legal hold, e a lição repete-se: a
// barreira EXISTIA, o CONTEÚDO dela é que não sobrevivia. Uma guarda cujo estado se perde no
// arranque não é uma guarda; é uma guarda até ao próximo deploy.
//
// PORQUE É QUE ISTO SÓ AGORA É DERIVÁVEL: a cadeia passou a distinguir os dois casos
// ([dsar.EventShredUnconfirmed]). Antes, o único sinal disponível era «a cadeia diz destruída e a
// chave existe» — que é AMBÍGUO: um titular apagado pode voltar a gerar dados e o `EnsureKey`
// re-provisiona uma KEK NOVA, legitimamente. Reconstruir a pendência a partir dessa conjunção
// teria posto o /readyz vermelho para sempre sobre um apagamento que correu bem.
// ---------------------------------------------------------------------------------------------

// shredPendingMarker é a porta de RE-MARCAÇÃO da pendência no arranque. Só a custódia que sabe
// CONFIRMAR sabe re-marcar — as outras não têm pendência nenhuma a restaurar.
type shredPendingMarker interface {
	marcarShredPorConfirmar(subjectID string)
}

// restoreShredPending reconstrói o conjunto de destruições POR CONFIRMAR a partir da cadeia DSAR.
//
// Reproduz por ordem de armazenamento — a cadeia é append-only e gapless, logo a ordem de leitura
// É a cronologia — e o ÚLTIMO facto sobre cada titular ganha:
//
//	dsar.shred_unconfirmed  ⇒ pendente
//	dsar.key_destroyed      ⇒ confirmado (limpa uma tentativa anterior falhada)
//
// A segunda regra não é simetria decorativa: sem ela, uma destruição que falhou e foi REPETIDA
// com sucesso continuaria a pôr o nó UNREADY após cada restart, para sempre. Um alarme que não
// sabe desligar-se deixa de ser lido.
func restoreShredPending(ctx context.Context, store audit.Store, partition string, vault audit.KeyVault) (int, error) {
	marker, ok := vault.(shredPendingMarker)
	if !ok || store == nil {
		return 0, nil // custódia que não sabe confirmar não tem pendência a restaurar
	}
	head, err := store.Head(ctx, partition)
	if err != nil {
		return 0, fmt.Errorf("shred por confirmar: head da cadeia %q: %w", partition, err)
	}
	if head == 0 {
		return 0, nil
	}
	recs, err := store.Read(ctx, partition, 1, head)
	if err != nil {
		return 0, fmt.Errorf("shred por confirmar: leitura da cadeia %q: %w", partition, err)
	}
	pendente := make(map[string]bool)
	for _, rec := range recs {
		switch rec.Capability {
		case dsar.EventShredUnconfirmed:
			pendente[rec.Resource.Value] = true
		case dsar.EventKeyDestroyed:
			pendente[rec.Resource.Value] = false
		default:
			continue // dsar.received / dsar.blocked não decidem sobre a custódia
		}
	}
	n := 0
	for subject, pend := range pendente {
		if pend && subject != "" {
			marker.marcarShredPorConfirmar(subject)
			n++
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------------------------
// A RECONCILIAÇÃO SOBREVIVE AO RESTART.
//
// Segunda metade do achado 1.7, e é a que o relatório mede explicitamente:
//
//	após restart COM re-hidratação: tentado outra vez? false
//
// O conjunto de registos por reconciliar vive em memória do job. Sem o reconstruir, um deploy
// bastava para o nó esquecer que uma destruição tinha ficado por confirmar — e a cadeia ficava
// sozinha a afirmar uma irrecuperabilidade que não aconteceu.
//
// É a TERCEIRA vez que este eixo aparece: `holdsRestored` para o legal hold, `restoreShredPending`
// para a custódia da KEK, e agora este. Sempre a mesma forma — a barreira existia, o CONTEÚDO
// dela é que não sobrevivia ao arranque.
// ---------------------------------------------------------------------------------------------

// restoreReconciliation reconstrói, a partir da cadeia de retenção, os registos cuja expiração
// ficou SELADA e cuja destruição o sink NÃO confirmou.
//
// Último facto por `record_id` ganha:
//
//	retention.expire_unconfirmed ⇒ por reconciliar
//	retention.expire_confirmed   ⇒ reconciliado (limpa)
//
// O `retention.expired` NÃO decide: ele existe em ambos os casos, e é precisamente por ser
// indistinguível que o achado 1.7 existia.
func restoreReconciliation(ctx context.Context, store audit.Store, partition string) (*audit.InMemoryReconciliationSet, int, error) {
	set := audit.NewInMemoryReconciliationSet()
	if store == nil {
		return set, 0, nil
	}
	head, err := store.Head(ctx, partition)
	if err != nil {
		return set, 0, fmt.Errorf("reconciliacao de retencao: head da cadeia %q: %w", partition, err)
	}
	if head == 0 {
		return set, 0, nil
	}
	recs, err := store.Read(ctx, partition, 1, head)
	if err != nil {
		return set, 0, fmt.Errorf("reconciliacao de retencao: leitura da cadeia %q: %w", partition, err)
	}
	pendente := make(map[string]bool)
	for _, rec := range recs {
		switch rec.Resource.Type {
		case audit.RetentionExpireUnconfirmedEventType:
			pendente[rec.Resource.Value] = true
		case audit.RetentionExpireConfirmedEventType:
			pendente[rec.Resource.Value] = false
		default:
			continue
		}
	}
	n := 0
	for id, pend := range pendente {
		if pend && id != "" {
			set.Add(id)
			n++
		}
	}
	return set, n, nil
}
