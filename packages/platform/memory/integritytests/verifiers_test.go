package integritytests

import (
	"context"
	"reflect"
	"strings"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
	"github.com/aos-ref/platform/memory/ports"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/platform/memory/working"
)

// Os verificadores abaixo são o CORAÇÃO da suite: cada um afere UMA invariante
// orquestrando os subpacotes REAIS e devolve nil sse a invariante se mantém, ou um
// erro sentinela se foi VIOLADA. São partilhados pelos testes da suite (que exigem
// nil sobre fixtures limpas) e pelos meta-testes (que injectam uma violação e exigem
// um erro — a prova de que a detecção NÃO é green-vazio).

// verifyProjectionPreservesRecord prova o Princípio 4 na via de projecção: a
// projecção pode DESCARTAR do contexto (IncludedTurns < TotalTurns) mas o REGISTO
// (record.Persist) mantém SEMPRE todos os turnos com o conteúdo cru — e o resumo
// injectado NUNCA vaza o conteúdo cru.
func verifyProjectionPreservesRecord(ctx context.Context, rec *record.TrajectoryRecord, policy projection.Policy) error {
	iv, err := projection.ProjectContext(record.View(rec), policy)
	if err != nil {
		return err
	}
	// A projecção conhece o TOTAL de turnos do registo (não descartou do registo).
	if iv.TotalTurns != rec.TurnCount() {
		return errRegisterIncomplete
	}
	// A via de REGISTO emite todos os turnos, cada um com o conteúdo cru intacto.
	ev, err := record.Persist(ctx, rec, nil)
	if err != nil {
		return err
	}
	if len(ev.Turns) != rec.TurnCount() {
		return errRegisterIncomplete
	}
	for _, t := range ev.Turns {
		if !strings.Contains(t.RawContent, rawSecretPrefix) {
			return errRecordErased
		}
	}
	// O backend recebe estritamente mais do que a vista injectada (spans completos).
	if ev.EmittedSpans <= iv.IncludedTurns {
		return errRegisterIncomplete
	}
	// Higiene: o resumo injectado nunca leva o conteúdo cru.
	if strings.Contains(iv.Summary, rawSecretPrefix) {
		return errRawLeaked
	}
	return nil
}

// verifyEvictedRecordsPreserved prova o Princípio 4 na via de eviction: cada
// segmento que SAIU da vista tem de continuar recuperável no backend (byte-a-byte).
func verifyEvictedRecordsPreserved(ctx context.Context, port ports.MemoryPort, evicted []working.EvictedSegment) error {
	if len(evicted) == 0 {
		return errRecordErased // nada evictado quando se esperava eviction: cenário inválido
	}
	for _, ev := range evicted {
		got, err := port.Get(ctx, domain.ClassWorking, ev.RecordID)
		if err != nil {
			return errRecordErased
		}
		wb, ok := got.Body.(domain.WorkingBody)
		if !ok || wb.Content != ev.Content {
			return errRecordErased
		}
	}
	return nil
}

// verifyCompressionPreservesRecord prova o Princípio 4 na via de compressão: o
// backend recebe a trajectória COMPLETA (FullRecordSpans > turnos do sumário), o
// total de turnos é preservado e o sumário nunca vaza o conteúdo cru.
func verifyCompressionPreservesRecord(res compression.CompactionResult) error {
	if res.FullRecordSpans <= res.Summary.IncludedTurns {
		return errRegisterIncomplete
	}
	if res.TotalTurns <= 0 {
		return errRegisterIncomplete
	}
	if strings.Contains(res.Summary.Summary, rawSecretPrefix) {
		return errRawLeaked
	}
	return nil
}

// verifyCompressionPrefixStable prova a estabilidade de cache (ADR-009): a
// compressão não muta nem reordena o prefixo imutável (hash invariante, sem thrash).
func verifyCompressionPrefixStable(res compression.CompactionResult) error {
	if !res.PrefixInvariant || res.PrefixHashBefore != res.PrefixHashAfter || res.CacheThrash {
		return errPrefixMutated
	}
	return nil
}

// verifyToolResultQuarantined prova a barreira estrutural de AOS-042: conteúdo
// derivado de tool_result é untrusted, entra em quarentena (NUNCA no control-plane) e
// é servido como DataItem — estruturalmente incapaz de autorizar uma acção.
func verifyToolResultQuarantined(ctx context.Context, tc provenance.TaintController) error {
	ing := provenance.NewIngestor(tc)
	admitted, err := ing.Ingest(ctx, semanticRec("kb-tool-result"), provenance.SourceToolResult)
	if err != nil {
		return err
	}
	part := provenance.NewPartition(nil)
	part.Admit(admitted)
	// Untrusted nunca alcança o control-plane (planeador só lê a TrustedView).
	if part.TrustedView().Len() != 0 {
		return errQuarantineBreached
	}
	if part.Quarantine().Len() != 1 {
		return errQuarantineBreached
	}
	// Estrutural: um item em quarentena NÃO satisfaz PrivilegedAuthorizer.
	//
	// NOTA (salvaguarda estrutural INALCANÇÁVEL): este ramo NÃO tem — nem pode ter — um
	// meta-teste que o dispare. DataItem (o tipo servido pela quarentena) não implementa
	// PrivilegedAuthorizer, pelo que a asserção de tipo nunca casa: a barreira é garantida
	// em tempo de COMPILAÇÃO, não em runtime. Mantém-se como defesa-em-profundidade
	// documentada — se um refactor futuro fizer um item de quarentena satisfazer
	// PrivilegedAuthorizer, esta verificação passa a apanhá-lo. errQuarantineAuthorizes é,
	// por isso, deliberadamente sem cobertura de execução na sua condição de falha.
	for _, item := range part.Quarantine().Items() {
		if _, ok := any(item).(provenance.PrivilegedAuthorizer); ok {
			return errQuarantineAuthorizes
		}
	}
	return nil
}

// verifyMigrationRoundTrip prova a não-perda da MIGRAÇÃO (AOS-041): expand→migrate→
// contract e depois o revert completo devolvem o estado byte-idêntico ao inicial. Uma
// migração com perda é RECUSADA pelo motor (backstop de reversibilidade) — o erro
// propaga-se (detecção).
func verifyMigrationRoundTrip(initial []domain.Record, mig migrations.Migration) error {
	ctx := context.Background()
	r, err := migrations.NewRunner(mig, initial, migrations.WithGate(migrations.NewEvalGate(mig.ID)))
	if err != nil {
		return err
	}
	if err := r.Run(ctx); err != nil { // expand → migrate → contract (transacional)
		return err
	}
	// Revert completo: contract → migrate → expand → estado inicial.
	if err := r.RevertContract(ctx); err != nil {
		return err
	}
	if err := r.RevertMigrate(ctx); err != nil {
		return err
	}
	if err := r.RevertExpand(ctx); err != nil {
		return err
	}
	got := r.CanonicalRecords()
	if len(got) != len(initial) {
		return errMigrationLoss
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], initial[i]) {
			return errMigrationLoss
		}
	}
	return nil
}

// verifyChainIntact prova que a hash-chain de audit verifica de ponta a ponta. É o
// detector partilhado pelo crypto-shredding (a cadeia sobrevive ao shredding) e pelo
// meta-teste de hash-chain partida (um store adulterado é apanhado).
func verifyChainIntact(ctx context.Context, store audit.Store, partition string) error {
	head, err := store.Head(ctx, partition)
	if err != nil {
		return err
	}
	if head == 0 {
		return nil
	}
	if err := audit.Verify(ctx, store, partition, 1, head); err != nil {
		return errChainBroken
	}
	return nil
}
