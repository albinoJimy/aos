package audit

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// A BARREIRA LARGA-SE MESMO QUANDO O SINK ENTRA EM PÂNICO.
//
// Achado da verificação de completude de 2026-08-23. A barreira era tomada e largada À MÃO em
// cinco sítios do laço. Auditados um a um, os cinco cobriam todos os `return`/`continue` — sem
// pânico não havia fuga. Mas os outros dois tomadores da MESMA barreira (`Shredder.Shred` e o
// handler de legal hold) usam `defer` e são panic-safe, e o contrato escrito em `retention.go`
// exige que ela seja largada «em TODOS os caminhos de saída».
//
// O QUE UM PÂNICO CUSTARIA: o `RLock` fica detido para sempre. O `POST /dsar/hold` seguinte
// bloqueia em `Lock()` SEM TIMEOUT e, com um escritor pendente, todo `BeginDestruction()`
// posterior bloqueia também — `/dsar/erase` e o varredor param, com as sondas a 200.
//
// GRAVIDADE HONESTA: nenhum pânico é hoje alcançável neste caminho com as implementações que o
// nó compõe. A porta abre-se com um `ExpirationSink` de TERCEIRO — que é precisamente o contrato
// que este pacote publica, e é o que este teste injecta.
// ---------------------------------------------------------------------------------------------

// sinkQuePanica é o `ExpirationSink` de terceiro que o contrato do pacote permite.
type sinkQuePanica struct{}

func (sinkQuePanica) Expire(context.Context, ExpirableRecord) error {
	panic("sink de terceiro em panico")
}

func TestABarreiraLARGASeMesmoComPanicoNoSink(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	holds := NewLegalHold()
	job := NewExpirationJob(cfg, holds, src, sinkQuePanica{},
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(NewMemStore(), DefaultRetentionPartition),
	)

	// O pânico sobe — é o comportamento correcto e não se muda aqui.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("o sink devia ter entrado em panico — o cenario nao esta montado")
			}
		}()
		_, _ = job.Run(context.Background())
	}()

	// A PROPRIEDADE: a barreira NÃO ficou detida. Sem o `defer`, esta colocação bloquearia para
	// sempre e o teste morreria por timeout do `go test` — que é a forma que um encravamento tem
	// de se manifestar.
	feito := make(chan struct{})
	go func() {
		fim := holds.BeginPlacement()
		holds.HoldSubject("nhi:depois-do-panico")
		fim()
		close(feito)
	}()
	select {
	case <-feito:
	case <-time.After(3 * time.Second):
		t.Fatal("a colocacao de um legal hold NUNCA foi libertada depois do panico — a barreira " +
			"ficou detida e toda a governacao de preservacao e destruicao encrava, com as sondas a 200")
	}

	// CONTROLO: e o hold ficou MESMO colocado. Sem este ramo, uma barreira que nunca tranca nada
	// passaria no teste acima.
	if !holds.HeldSubject("nhi:depois-do-panico") {
		t.Error("o hold nao ficou em vigor — o teste acima teria passado com uma barreira inerte")
	}
}
