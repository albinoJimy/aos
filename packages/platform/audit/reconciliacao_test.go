package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// A «OUTRA PASSAGEM» PASSA A EXISTIR.
//
// Achado 1.7 da varredura adversarial de 2026-08-21:
//
//	passagem 2:                     tentado outra vez? false
//	após restart COM re-hidratação: tentado outra vez? false
//
// O docstring do [ExpirationJob.expireOne] prometia que «o sink, idempotente por contrato, é
// RECONCILIADO NOUTRA PASSAGEM». Essa passagem nunca acontecia: a marca de idempotência acompanha
// o FACTO SELADO (correcto — a WORM é append-only e re-selar duplicaria o evento), e a
// re-hidratação reconstrói-a dos próprios eventos selados. Ter selado «expirado» tornava a falha
// do sink PERMANENTE.
//
// O efeito é uma KEK viva com a cadeia a afirmar que morreu — o mesmo desfecho do achado 1.6,
// noutro caminho.
// ---------------------------------------------------------------------------------------------

// cenarioSinkFalhado monta um job cujo sink falha para o registo "a", com reconciliação ligada.
func cenarioSinkFalhado(t *testing.T) (*ExpirationJob, *fakeSink, Store, *InMemoryReconciliationSet, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, SubjectID: "nhi:titular-1.7", CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	sink.failIDs["a"] = true
	sink.failWith = errors.New("crypto-shred indisponivel")
	store := NewMemStore()
	rec := NewInMemoryReconciliationSet()
	job := NewExpirationJob(cfg, nil, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(store, DefaultRetentionPartition),
		WithExpirationReconciliation(rec),
	)
	return job, sink, store, rec, now
}

func TestUmSinkFalhadoERETENTADONaPassagemSeguinte(t *testing.T) {
	job, sink, store, pend, _ := cenarioSinkFalhado(t)
	ctx := context.Background()

	// PASSAGEM 1 — sela e o sink falha.
	rep1, err := job.Run(ctx)
	if err == nil {
		t.Fatal("esperava o erro agregado do sink a falhar")
	}
	if rep1.Expired != 1 {
		t.Fatalf("a expiracao SELADA conta como expirada mesmo com o sink a falhar: %+v", rep1)
	}
	if !pend.Pending("a") {
		t.Fatal("o registo devia ficar POR RECONCILIAR — sem isso a falha e permanente")
	}
	chamadas1 := sink.calls

	// O sink deixa de falhar (a custódia voltou).
	delete(sink.failIDs, "a")

	// PASSAGEM 2 — TEM de tentar outra vez.
	rep2, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("passagem 2: %v", err)
	}
	if sink.calls <= chamadas1 {
		t.Fatalf("o sink NAO foi chamado outra vez (%d -> %d) — ter selado «expirado» tornou a "+
			"falha eterna, e a KEK fica viva com a cadeia a dizer que morreu", chamadas1, sink.calls)
	}
	if !sink.expired["a"] {
		t.Error("a destruicao nao aconteceu na reconciliacao")
	}
	if pend.Pending("a") {
		t.Error("a pendencia nao foi limpa depois de o sink confirmar")
	}
	if rep2.Expired != 1 {
		t.Errorf("a passagem que REALMENTE destruiu devia contar Expired=1; veio %+v — contar como "+
			"skipped esconderia o unico momento em que o trabalho foi feito", rep2)
	}

	// A CADEIA NÃO GANHOU UM SEGUNDO `retention.expired`, e ganhou o desfecho positivo.
	expirados, desmentidos := contarPorTipo(t, store, DefaultRetentionPartition)
	if expirados != 1 {
		t.Errorf("a reconciliacao re-selou `retention.expired` (%d) — e o segundo evento para o "+
			"mesmo facto que toda a idempotencia existe para impedir", expirados)
	}
	if desmentidos != 1 {
		t.Errorf("desmentidos = %d, esperava exactamente 1", desmentidos)
	}
	if !temTipo(t, store, DefaultRetentionPartition, RetentionExpireConfirmedEventType) {
		t.Error("a reconciliacao bem-sucedida NAO ficou selada — um desmentido que nunca e " +
			"levantado deixa a cadeia a acusar para sempre uma destruicao que ja aconteceu")
	}
}

// TestUmSinkQueCONTINUAAFalharNaoEnchesACadeia — o desmentido é UM, não um por passagem.
//
// Sem este teste, selar o desmentido em cada re-tentativa encheria a partição de hora a hora, e a
// cadeia gapless passaria a ter mais ruído do que factos.
func TestUmSinkQueCONTINUAAFalharNaoEnchesACadeia(t *testing.T) {
	job, _, store, pend, _ := cenarioSinkFalhado(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _ = job.Run(ctx)
	}
	if !pend.Pending("a") {
		t.Fatal("continua a falhar, logo continua pendente")
	}
	expirados, desmentidos := contarPorTipo(t, store, DefaultRetentionPartition)
	if expirados != 1 {
		t.Errorf("`retention.expired` = %d apos 3 passagens, esperava 1", expirados)
	}
	if desmentidos != 1 {
		t.Errorf("`retention.expire_unconfirmed` = %d apos 3 passagens falhadas, esperava 1 — a "+
			"cadeia gapless nao pode ganhar ruido de hora a hora", desmentidos)
	}
}

// TestSemReconciliacaoLigadaOComportamentoEOAnterior — a opção é opt-in, e isso fica provado.
//
// Um nó que não a componha continua a selar o desmentido (isso acontece sempre, e é o que torna a
// reconstrução possível) mas NÃO re-tenta. Sem este teste, «re-tentar sempre» passaria nos
// anteriores e a opção seria decorativa.
func TestSemReconciliacaoLigadaOComportamentoEOAnterior(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	sink.failIDs["a"] = true
	sink.failWith = errors.New("crypto-shred indisponivel")
	store := NewMemStore()
	job := NewExpirationJob(cfg, nil, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(store, DefaultRetentionPartition),
	)
	ctx := context.Background()
	_, _ = job.Run(ctx)
	chamadas := sink.calls
	delete(sink.failIDs, "a")
	rep2, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("passagem 2: %v", err)
	}
	if sink.calls != chamadas {
		t.Errorf("sem reconciliacao ligada o sink NAO devia ser re-chamado (%d -> %d)", chamadas, sink.calls)
	}
	if rep2.Skipped != 1 {
		t.Errorf("sem reconciliacao o registo devia ser SALTADO; veio %+v", rep2)
	}
}

// temTipo diz se a partição tem pelo menos um registo do tipo dado.
func temTipo(t *testing.T, store Store, partition, tipo string) bool {
	t.Helper()
	ctx := context.Background()
	head, err := store.Head(ctx, partition)
	if err != nil || head == 0 {
		return false
	}
	recs, err := store.Read(ctx, partition, 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, r := range recs {
		if r.Resource.Type == tipo {
			return true
		}
	}
	return false
}
