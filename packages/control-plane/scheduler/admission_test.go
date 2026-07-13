package scheduler_test

// Testes do admission control global (AOS-027). Todos deterministas: relógio e
// IDs injectáveis, sem time.Now nem rand no caminho de decisão. Cobrem os Testes
// Requeridos do ticket:
//   - unit: reserva/refill nunca excede o limite configurado;
//   - integração distribuída: N workers concorrentes sem oversubscription (-race);
//   - integração: cenário "15 boards" — excesso ADIADO, não rejeitado;
//   - replay: sequência de admissões reconstrói-se dos eventos;
//   - quota por tenant preserva o tecto global.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// fixedClock devolve sempre o mesmo instante (congela o refill: nenhuma reserva
// expira durante o teste). Seguro para concorrência (sem estado mutável).
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// mutClock é um relógio mutável e seguro para concorrência (para o teste de
// refill, que avança o tempo).
type mutClock struct{ nanos atomic.Int64 }

func (c *mutClock) now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *mutClock) advance(d time.Duration) { c.nanos.Add(d.Nanoseconds()) }
func (c *mutClock) set(t time.Time)         { c.nanos.Store(t.UnixNano()) }

// seqIDGen devolve um gerador determinístico de reservation IDs.
func seqIDGen() func() string {
	var n atomic.Int64
	return func() string { return fmt.Sprintf("r%03d", n.Add(1)) }
}

// key de teste.
var testKey = scheduler.ProviderKey{Provider: "anthropic", Model: "claude", Region: "eu"}

// newAdm constrói uma Admission de teste sobre um Event Store real (AOS-002).
func newAdm(t *testing.T, qp scheduler.QuotaProvider, opts ...scheduler.AdmissionOption) (*scheduler.Admission, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	adm, err := scheduler.NewAdmission(es, qp, opts...)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	return adm, es
}

// staticQP de conveniência.
func qpTPM(tpm, rpm int64, window time.Duration) *scheduler.StaticQuotaProvider {
	return scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: tpm, RPM: rpm, Window: window})
}

// ---------------------------------------------------------------------------
// Unit: reserva/refill nunca excede o limite configurado.
// ---------------------------------------------------------------------------

func TestAdmit_ReserveNeverExceedsLimit(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	qp := qpTPM(1000, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	granted, deferred := 0, 0
	var reserved int64
	// 20 tentativas de 100 tokens sobre um tecto de 1000 ⇒ exactamente 10 grants.
	for i := 0; i < 20; i++ {
		res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if err != nil {
			t.Fatalf("Admit[%d]: %v", i, err)
		}
		if res.Granted {
			granted++
			reserved += 100
		} else {
			deferred++
			if res.RetryAfter <= 0 {
				t.Fatalf("Admit[%d] adiado sem retry_after > 0", i)
			}
		}
	}
	if granted != 10 {
		t.Fatalf("granted=%d, esperado 10 (1000/100)", granted)
	}
	if deferred != 10 {
		t.Fatalf("deferred=%d, esperado 10", deferred)
	}
	if reserved > 1000 {
		t.Fatalf("reservado=%d excede o tecto TPM=1000 (oversubscription)", reserved)
	}
}

func TestAdmit_RefillReleasesAfterWindow(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	qp := qpTPM(300, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(clk.now),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	// Enche o bucket: 3 grants, o 4º adia.
	for i := 0; i < 3; i++ {
		res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if err != nil || !res.Granted {
			t.Fatalf("grant inicial[%d]: granted=%v err=%v", i, res.Granted, err)
		}
	}
	res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
	if res.Granted {
		t.Fatalf("esperado defer com bucket cheio")
	}
	if res.RetryAfter != time.Minute {
		t.Fatalf("retry_after=%v, esperado a janela inteira (1m)", res.RetryAfter)
	}

	// Avança o relógio para lá da janela: o refill temporizado liberta tudo.
	clk.advance(time.Minute + time.Second)
	res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
	if err != nil {
		t.Fatalf("admit pós-refill: %v", err)
	}
	if !res.Granted {
		t.Fatalf("esperado grant após o refill temporizado")
	}
}

func TestAdmit_RequestsDimensionBinds(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	// TPM folgado, RPM=3 ⇒ o RPM é a dimensão que trava.
	qp := qpTPM(1_000_000, 3, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 1}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()
	granted := 0
	for i := 0; i < 10; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if res.Granted {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("granted=%d, esperado 3 (RPM=3)", granted)
	}
}

// ---------------------------------------------------------------------------
// Integração distribuída: N workers concorrentes, sem oversubscription (-race).
// ---------------------------------------------------------------------------

func TestAdmit_ConcurrentNoOversubscription(t *testing.T) {
	t.Parallel()
	base := time.Unix(2_000_000, 0)
	const tpm = 1000
	const cost = 100
	const workers = 200
	qp := qpTPM(tpm, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)), // relógio congelado: nada expira
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: cost}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			res, err := adm.Admit(ctx, scheduler.AdmitRequest{
				Key:       testKey,
				RequestID: fmt.Sprintf("w%03d", id),
			})
			if err != nil {
				t.Errorf("Admit worker %d: %v", id, err)
				return
			}
			if res.Granted {
				granted.Add(1)
			}
		}(i)
	}
	wg.Wait()

	g := granted.Load()
	// Invariante central: a soma das reservas activas nunca excede o TPM.
	if g*cost > tpm {
		t.Fatalf("OVERSUBSCRIPTION: %d grants * %d = %d > TPM=%d", g, cost, g*cost, tpm)
	}
	// Utilização plena: com cost a dividir TPM, exactamente tpm/cost grants.
	if g != tpm/cost {
		t.Fatalf("granted=%d, esperado exactamente %d", g, tpm/cost)
	}

	// Confirmação independente pela projecção do Event Store (fonte de verdade).
	recs, err := adm.Replay(ctx, testKey)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var sum int64
	for _, r := range recs {
		if r.Type == scheduler.EventAdmitGranted {
			sum += r.CostTokens
		}
	}
	if sum > tpm {
		t.Fatalf("soma reservada no ES = %d > TPM=%d", sum, tpm)
	}
	if sum != g*cost {
		t.Fatalf("soma no ES=%d != grants observados=%d", sum, g*cost)
	}
}

// ---------------------------------------------------------------------------
// Integração: cenário "15 boards" — agregado <= rate limit; excesso ADIADO.
// ---------------------------------------------------------------------------

func TestAdmit_FifteenBoards_ExcessDeferredNotRejected(t *testing.T) {
	t.Parallel()
	base := time.Unix(3_000_000, 0)
	const tpm = 1000
	const cost = 100
	const boards = 15
	// Rate limit PARTILHADO por todos os boards (mesma ProviderKey).
	qp := qpTPM(tpm, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: cost}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	type outcome struct {
		granted bool
		retry   time.Duration
	}
	results := make([]outcome, boards)
	var wg sync.WaitGroup
	for b := 0; b < boards; b++ {
		wg.Add(1)
		go func(board int) {
			defer wg.Done()
			res, err := adm.Admit(ctx, scheduler.AdmitRequest{
				Key:       testKey,
				Tenant:    fmt.Sprintf("tenant-%d", board),
				Board:     fmt.Sprintf("board-%d", board),
				RequestID: fmt.Sprintf("b%02d", board),
			})
			if err != nil {
				t.Errorf("board %d: %v", board, err)
				return
			}
			results[board] = outcome{granted: res.Granted, retry: res.RetryAfter}
		}(b)
	}
	wg.Wait()

	granted, deferred := 0, 0
	for b, o := range results {
		if o.granted {
			granted++
		} else {
			deferred++
			// Excesso é ADIADO (não rejeitado cegamente): retry_after > 0.
			if o.retry <= 0 {
				t.Fatalf("board %d adiado SEM retry_after (rejeição cega)", b)
			}
		}
	}
	if granted != tpm/cost {
		t.Fatalf("granted=%d, esperado %d (agregado no rate limit partilhado)", granted, tpm/cost)
	}
	if deferred != boards-tpm/cost {
		t.Fatalf("deferred=%d, esperado %d", deferred, boards-tpm/cost)
	}
	// O agregado nunca ultrapassa o rate limit partilhado.
	if int64(granted*cost) > tpm {
		t.Fatalf("agregado %d excede o rate limit partilhado %d", granted*cost, tpm)
	}
}

// ---------------------------------------------------------------------------
// Replay: a sequência de admissões reconstrói-se fielmente dos eventos.
// ---------------------------------------------------------------------------

func TestAdmit_ReplayReconstructsSequence(t *testing.T) {
	t.Parallel()
	base := time.Unix(4_000_000, 0)
	qp := qpTPM(500, 1_000_000, time.Minute)
	idgen := seqIDGen()
	adm, es := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithIDGen(idgen),
	)
	ctx := context.Background()

	// 7 admits sobre tecto 500/100 ⇒ 5 grants, 2 defers.
	var wantGranted []string
	for i := 0; i < 7; i++ {
		res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if err != nil {
			t.Fatalf("Admit[%d]: %v", i, err)
		}
		if res.Granted {
			wantGranted = append(wantGranted, res.ReservationID)
		}
	}

	// Replay do stream de reserva: grants por ordem de seq.
	recs, err := adm.Replay(ctx, testKey)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var gotGranted []string
	var lastSeq uint64
	for _, r := range recs {
		if r.Seq <= lastSeq {
			t.Fatalf("seq fora de ordem no replay: %d <= %d", r.Seq, lastSeq)
		}
		lastSeq = r.Seq
		if r.Type == scheduler.EventAdmitGranted {
			gotGranted = append(gotGranted, r.ReservationID)
		}
	}
	if len(gotGranted) != len(wantGranted) {
		t.Fatalf("replay grants=%d, esperado %d", len(gotGranted), len(wantGranted))
	}
	for i := range wantGranted {
		if gotGranted[i] != wantGranted[i] {
			t.Fatalf("replay[%d]=%s, esperado %s", i, gotGranted[i], wantGranted[i])
		}
	}

	// Auditoria: admit_requested por cada pedido; admit_deferred pelos adiados.
	// (Antes da reconstrução por adm2, que acrescentaria mais eventos ao stream.)
	audit, err := adm.ReplayAudit(ctx, testKey)
	if err != nil {
		t.Fatalf("ReplayAudit: %v", err)
	}
	reqCount, deferCount := 0, 0
	for _, r := range audit {
		switch r.Type {
		case scheduler.EventAdmitRequested:
			reqCount++
		case scheduler.EventAdmitDeferred:
			deferCount++
		}
	}
	if reqCount != 7 {
		t.Fatalf("admit_requested=%d, esperado 7", reqCount)
	}
	if deferCount != 2 {
		t.Fatalf("admit_deferred=%d, esperado 2", deferCount)
	}

	// Reconstrução INDEPENDENTE por um segundo controlador sobre o MESMO Event
	// Store: o estado do bucket é derivado só dos eventos (sem estado em memória).
	adm2, err := scheduler.NewAdmission(es, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
	)
	if err != nil {
		t.Fatalf("NewAdmission#2: %v", err)
	}
	// O bucket está cheio (500/100=5 grants); um novo admit no mesmo instante adia.
	res, err := adm2.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
	if err != nil {
		t.Fatalf("Admit adm2: %v", err)
	}
	if res.Granted {
		t.Fatalf("adm2 devia ver o bucket cheio pela projecção do ES e adiar")
	}
}

// ---------------------------------------------------------------------------
// Quota por tenant preserva o tecto global.
// ---------------------------------------------------------------------------

func TestAdmit_TenantQuota(t *testing.T) {
	base := time.Unix(5_000_000, 0)
	const cost = 100

	tests := []struct {
		name        string
		globalTPM   int64
		tenantCap   int64 // TPM do cap do tenant
		tenant      string
		attempts    int
		wantGranted int
	}{
		{
			name:        "cap do tenant trava abaixo do global",
			globalTPM:   2000,
			tenantCap:   600, // 6 grants pelo cap do tenant, apesar de o global permitir 20
			tenant:      "A",
			attempts:    20,
			wantGranted: 6,
		},
		{
			name:        "sem cap de tenant, so o global trava",
			globalTPM:   500,
			tenantCap:   0, // sem cap próprio
			tenant:      "B",
			attempts:    20,
			wantGranted: 5,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qp := qpTPM(tc.globalTPM, 1_000_000, time.Minute)
			if tc.tenantCap > 0 {
				qp.SetTenant(testKey, tc.tenant, scheduler.ProviderLimits{TPM: tc.tenantCap, RPM: 1_000_000, Window: time.Minute})
			}
			adm, _ := newAdm(t, qp,
				scheduler.WithClock(fixedClock(base)),
				scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: cost}),
				scheduler.WithIDGen(seqIDGen()),
			)
			ctx := context.Background()
			granted := 0
			for i := 0; i < tc.attempts; i++ {
				res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: tc.tenant})
				if err != nil {
					t.Fatalf("Admit[%d]: %v", i, err)
				}
				if res.Granted {
					granted++
				}
			}
			if granted != tc.wantGranted {
				t.Fatalf("granted=%d, esperado %d", granted, tc.wantGranted)
			}
		})
	}
}

// TestAdmit_TenantNeverExceedsGlobal prova que o GLOBAL domina: mesmo com um cap
// de tenant FOLGADO, um tenant nunca ultrapassa o tecto global quando outro
// tenant já consumiu parte do bucket partilhado.
func TestAdmit_TenantNeverExceedsGlobal(t *testing.T) {
	t.Parallel()
	base := time.Unix(6_000_000, 0)
	const cost = 100
	const globalTPM = 1000
	qp := qpTPM(globalTPM, 1_000_000, time.Minute)
	// Cada tenant tem um cap folgado (900) — maior do que a fatia que sobra do
	// global depois do outro tenant.
	qp.SetTenant(testKey, "A", scheduler.ProviderLimits{TPM: 900, RPM: 1_000_000, Window: time.Minute})
	qp.SetTenant(testKey, "B", scheduler.ProviderLimits{TPM: 900, RPM: 1_000_000, Window: time.Minute})
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: cost}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	// Tenant A consome 7 (700) — dentro do seu cap de 900.
	grantedA := 0
	for i := 0; i < 7; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "A"})
		if res.Granted {
			grantedA++
		}
	}
	if grantedA != 7 {
		t.Fatalf("tenant A granted=%d, esperado 7", grantedA)
	}
	// Tenant B tem cap 900, mas o global só tem 300 livres ⇒ B obtém 3, não 9.
	grantedB := 0
	for i := 0; i < 9; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "B"})
		if res.Granted {
			grantedB++
		}
	}
	if grantedB != 3 {
		t.Fatalf("tenant B granted=%d, esperado 3 (global domina o cap folgado)", grantedB)
	}
	// Agregado nunca excede o global.
	if int64((grantedA+grantedB)*cost) > globalTPM {
		t.Fatalf("agregado %d excede o global %d", (grantedA+grantedB)*cost, globalTPM)
	}
}

// ---------------------------------------------------------------------------
// Release / reconciliação, spans OTel, portas e validações.
// ---------------------------------------------------------------------------

func TestAdmit_ReleaseFreesQuota(t *testing.T) {
	t.Parallel()
	base := time.Unix(7_000_000, 0)
	qp := qpTPM(300, 1_000_000, time.Minute)
	ids := seqIDGen()
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithIDGen(ids),
	)
	ctx := context.Background()

	var firstRes string
	for i := 0; i < 3; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if i == 0 {
			firstRes = res.ReservationID
		}
	}
	// Bucket cheio: adia.
	if res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey}); res.Granted {
		t.Fatalf("esperado defer com bucket cheio")
	}
	// Liberta uma reserva ⇒ abre headroom para mais uma.
	if err := adm.Release(ctx, testKey, firstRes, 100, 1); err != nil {
		t.Fatalf("Release: %v", err)
	}
	res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
	if err != nil {
		t.Fatalf("Admit pós-release: %v", err)
	}
	if !res.Granted {
		t.Fatalf("esperado grant após libertar quota")
	}
}

func TestAdmit_IdempotentByRequestID(t *testing.T) {
	t.Parallel()
	base := time.Unix(8_000_000, 0)
	qp := qpTPM(1000, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	r1, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, RequestID: "same"})
	r2, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, RequestID: "same"})
	if !r1.Granted || !r2.Granted {
		t.Fatalf("ambos deviam ser granted: %v %v", r1.Granted, r2.Granted)
	}
	if r1.ReservationID != r2.ReservationID {
		t.Fatalf("reservation IDs diferentes: %s vs %s", r1.ReservationID, r2.ReservationID)
	}
	// Só UMA reserva materializada (sem duplo débito).
	recs, _ := adm.Replay(ctx, testKey)
	grants := 0
	for _, r := range recs {
		if r.Type == scheduler.EventAdmitGranted {
			grants++
		}
	}
	if grants != 1 {
		t.Fatalf("grants materializados=%d, esperado 1 (idempotência)", grants)
	}
}

func TestAdmit_TracerSpansHeadroomAndCost(t *testing.T) {
	t.Parallel()
	base := time.Unix(9_000_000, 0)
	qp := qpTPM(100, 1_000_000, time.Minute)
	tr := &agentruntime.RecordingTracer{}
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithIDGen(seqIDGen()),
		scheduler.WithTracer(tr),
	)
	ctx := context.Background()
	_, _ = adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey}) // grant
	_, _ = adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey}) // defer

	spans := tr.SpansByOperation("admission_control")
	if len(spans) != 2 {
		t.Fatalf("spans=%d, esperado 2", len(spans))
	}
	for _, s := range spans {
		if !s.Ended {
			t.Fatalf("span não terminado")
		}
		if _, ok := s.Attributes["aos.admission.cost_tokens"]; !ok {
			t.Fatalf("span sem custo")
		}
		if _, ok := s.Attributes["aos.admission.headroom_tokens"]; !ok {
			t.Fatalf("span sem headroom")
		}
	}
}

func TestNewAdmission_Validation(t *testing.T) {
	t.Parallel()
	qp := qpTPM(1, 1, time.Minute)
	if _, err := scheduler.NewAdmission(nil, qp); err == nil {
		t.Fatalf("esperado erro com log nil")
	}
	es, _ := eventstore.New()
	if _, err := scheduler.NewAdmission(es, nil); err == nil {
		t.Fatalf("esperado erro com quota provider nil")
	}
}

func TestAdmit_FailClosedOnBadLimits(t *testing.T) {
	t.Parallel()
	base := time.Unix(10_000_000, 0)
	// Window <= 0 ⇒ fail-closed.
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 100, RPM: 100, Window: 0})
	adm, _ := newAdm(t, qp, scheduler.WithClock(fixedClock(base)))
	if _, err := adm.Admit(context.Background(), scheduler.AdmitRequest{Key: testKey}); err == nil {
		t.Fatalf("esperado erro fail-closed com janela inválida")
	}
}

func TestAdmit_EstimatedTokensPrecedence(t *testing.T) {
	t.Parallel()
	base := time.Unix(11_000_000, 0)
	qp := qpTPM(1000, 1_000_000, time.Minute)
	// Estimador devolveria 1, mas o pedido fixa 250.
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 1}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()
	granted := 0
	for i := 0; i < 10; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, EstimatedTokens: 250})
		if res.Granted {
			granted++
		}
	}
	if granted != 4 {
		t.Fatalf("granted=%d, esperado 4 (1000/250)", granted)
	}
}

func TestAdmit_OptionsAndPerKeyLimits(t *testing.T) {
	t.Parallel()
	base := time.Unix(12_000_000, 0)
	// Limite por omissão folgado, mas a chave específica trava em 200 (2 grants).
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 1_000_000, RPM: 1_000_000, Window: time.Minute})
	qp.SetKey(testKey, scheduler.ProviderLimits{TPM: 200, RPM: 1_000_000, Window: time.Minute})
	es, _ := eventstore.New()
	// Sem WithIDGen: exercita o gerador por omissão. Com produtor e cap de CAS.
	adm, err := scheduler.NewAdmission(es, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
		scheduler.WithAdmissionProducer(eventstore.Producer{NHIID: "nhi:test"}),
		scheduler.WithMaxCASRetries(50),
	)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	ctx := context.Background()
	granted := 0
	var ids []string
	for i := 0; i < 5; i++ {
		res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if err != nil {
			t.Fatalf("Admit[%d]: %v", i, err)
		}
		if res.Granted {
			granted++
			if res.ReservationID == "" {
				t.Fatalf("reservation ID vazio do gerador por omissão")
			}
			ids = append(ids, res.ReservationID)
		}
	}
	if granted != 2 {
		t.Fatalf("granted=%d, esperado 2 (cap por chave = 200/100)", granted)
	}
	// IDs do gerador por omissão são únicos.
	if len(ids) == 2 && ids[0] == ids[1] {
		t.Fatalf("gerador por omissão devolveu IDs iguais: %s", ids[0])
	}
}

func TestAdmit_DefaultEstimatorFloor(t *testing.T) {
	t.Parallel()
	base := time.Unix(13_000_000, 0)
	// Sem WithCostEstimator: usa o default (1 token). RPM alto, TPM=3 ⇒ 3 grants.
	qp := qpTPM(3, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()
	granted := 0
	for i := 0; i < 6; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if res.Granted {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("granted=%d, esperado 3 (default estimator=1 token, TPM=3)", granted)
	}
}

func TestProviderKey_String(t *testing.T) {
	t.Parallel()
	k := scheduler.ProviderKey{Provider: "p", Model: "m", Region: "r"}
	if k.String() != "p:m:r" {
		t.Fatalf("String()=%q", k.String())
	}
}

// ---------------------------------------------------------------------------
// AOS-027 remediação: reconciliação PARCIAL de Release (Q1/C1). Uma libertação
// parcial só devolve o montante indicado — nunca a reserva inteira — pelo que
// nunca abre headroom fantasma nem oversubscreve o TPM real.
// ---------------------------------------------------------------------------

func TestAdmit_PartialReleaseNoPhantomHeadroom(t *testing.T) {
	t.Parallel()
	base := time.Unix(14_000_000, 0)
	const tpm = 1000
	// Uma única reserva que enche o TPM inteiro (1000).
	qp := qpTPM(tpm, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: tpm}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
	if err != nil || !res.Granted {
		t.Fatalf("grant inicial: granted=%v err=%v", res.Granted, err)
	}
	resID := res.ReservationID

	// Reconcilia devolvendo APENAS 100 (custo real ficou 100 abaixo do estimado).
	// Ficam 900 ainda in-flight contra o provider.
	if err := adm.Release(ctx, testKey, resID, 100, 0); err != nil {
		t.Fatalf("Release parcial: %v", err)
	}

	// Agora só há 100 de headroom. Um pedido de 1000 tem de ADIAR (900+1000>1000).
	res2, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, EstimatedTokens: tpm})
	if err != nil {
		t.Fatalf("Admit pós-release parcial (1000): %v", err)
	}
	if res2.Granted {
		t.Fatalf("OVERSUBSCRIPTION: grant de 1000 concedido com 900 ainda activos (soma=1900 > TPM=1000)")
	}

	// Um pedido de exactamente 100 cabe no headroom libertado.
	res3, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, EstimatedTokens: 100})
	if err != nil {
		t.Fatalf("Admit pós-release parcial (100): %v", err)
	}
	if !res3.Granted {
		t.Fatalf("esperado grant de 100 no headroom libertado")
	}

	// Invariante: a soma efectiva das reservas activas nunca excede o TPM.
	// (reserva original reduzida a 900) + (nova de 100) = 1000.
	recs, err := adm.Replay(ctx, testKey)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var grant, rel int64
	for _, r := range recs {
		switch r.Type {
		case scheduler.EventAdmitGranted:
			grant += r.CostTokens
		case scheduler.EventQuotaReleased:
			rel += r.CostTokens
		}
	}
	if grant-rel > tpm {
		t.Fatalf("débito efectivo %d (grants=%d - releases=%d) excede TPM=%d", grant-rel, grant, rel, tpm)
	}
}

// TestAdmit_PartialReleaseClampsOverRelease prova que sobre-libertar (devolver
// mais do que o reservado) faz clamp em 0 — nunca abre headroom além do TPM.
func TestAdmit_PartialReleaseClampsOverRelease(t *testing.T) {
	t.Parallel()
	base := time.Unix(15_000_000, 0)
	const tpm = 1000
	qp := qpTPM(tpm, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 200}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	// 5 grants de 200 = 1000 (TPM cheio).
	var first string
	for i := 0; i < 5; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if i == 0 {
			first = res.ReservationID
		}
	}
	// Sobre-liberta: devolve 999999 de uma reserva de 200. Clamp ⇒ só 200 voltam.
	if err := adm.Release(ctx, testKey, first, 999999, 999999); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Cabem exactamente mais 200, não mais.
	granted := 0
	for i := 0; i < 5; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if res.Granted {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("granted após sobre-release=%d, esperado exactamente 1 (clamp em 0)", granted)
	}
}

// TestAdmit_PartialReleaseConcurrentInvariant estressa releases parciais e
// admits concorrentes sob -race, verificando que o débito efectivo nunca excede
// o TPM.
func TestAdmit_PartialReleaseConcurrentInvariant(t *testing.T) {
	t.Parallel()
	base := time.Unix(16_000_000, 0)
	const tpm = 1000
	const cost = 100
	qp := qpTPM(tpm, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: cost}),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	// Enche o bucket: 10 grants de 100.
	var ids []string
	for i := 0; i < 10; i++ {
		res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey})
		if res.Granted {
			ids = append(ids, res.ReservationID)
		}
	}
	if len(ids) != 10 {
		t.Fatalf("setup: grants=%d, esperado 10", len(ids))
	}

	// Concorrentemente: metade das reservas liberta parcialmente 50; ao mesmo
	// tempo, novos admits tentam ocupar o headroom que se abre.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = adm.Release(ctx, testKey, id, 50, 0)
		}(ids[i])
	}
	var newGrants atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			res, err := adm.Admit(ctx, scheduler.AdmitRequest{
				Key:             testKey,
				RequestID:       fmt.Sprintf("new%03d", n),
				EstimatedTokens: 50,
			})
			if err != nil {
				t.Errorf("Admit concorrente: %v", err)
				return
			}
			if res.Granted {
				newGrants.Add(1)
			}
		}(i)
	}
	wg.Wait()

	// Invariante final via projecção do ES: débito efectivo <= TPM.
	recs, err := adm.Replay(ctx, testKey)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	byID := map[string]int64{}
	rel := map[string]int64{}
	for _, r := range recs {
		switch r.Type {
		case scheduler.EventAdmitGranted:
			byID[r.ReservationID] += r.CostTokens
		case scheduler.EventQuotaReleased:
			rel[r.ReservationID] += r.CostTokens
		}
	}
	var eff int64
	for id, g := range byID {
		e := g - rel[id]
		if e < 0 {
			e = 0
		}
		eff += e
	}
	if eff > tpm {
		t.Fatalf("OVERSUBSCRIPTION: débito efectivo %d > TPM=%d", eff, tpm)
	}
}

// ---------------------------------------------------------------------------
// AOS-027 remediação: rejeição PERMANENTE de pedido oversized (Q2/C2). Um custo
// maior que o tecto TPM/RPM nunca será admissível — rejeita distintamente em vez
// de aconselhar um retry eterno (defer infinito / starvation).
// ---------------------------------------------------------------------------

func TestAdmit_CostExceedsTPMRejectedNotDeferred(t *testing.T) {
	t.Parallel()
	base := time.Unix(17_000_000, 0)
	const tpm = 1000
	qp := qpTPM(tpm, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	// Custo 5000 > TPM 1000 ⇒ rejeição permanente, retry_after = 0.
	res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, EstimatedTokens: 5000})
	if err != nil {
		t.Fatalf("Admit oversized: %v", err)
	}
	if res.Granted {
		t.Fatalf("pedido oversized não devia ser concedido")
	}
	if !res.Rejected {
		t.Fatalf("esperado Rejected=true (rejeição permanente), não defer")
	}
	if res.RetryAfter != 0 {
		t.Fatalf("retry_after=%v, esperado 0 (nunca admissível — não fazer poll)", res.RetryAfter)
	}

	// O bucket permanece vazio: um pedido normal ainda é aceite (o oversized não
	// consumiu nem reservou nada).
	res2, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, EstimatedTokens: 100})
	if !res2.Granted {
		t.Fatalf("pedido normal devia caber (oversized não deve ter reservado quota)")
	}

	// Auditoria: o admit_deferred do oversized ficou marcado unsatisfiable.
	audit, err := adm.ReplayAudit(ctx, testKey)
	if err != nil {
		t.Fatalf("ReplayAudit: %v", err)
	}
	deferCount := 0
	for _, r := range audit {
		if r.Type == scheduler.EventAdmitDeferred {
			deferCount++
		}
	}
	if deferCount != 1 {
		t.Fatalf("admit_deferred=%d, esperado 1 (o oversized)", deferCount)
	}
}

func TestAdmit_RPMZeroRejectsPermanently(t *testing.T) {
	t.Parallel()
	base := time.Unix(18_000_000, 0)
	// RPM=0 ⇒ nenhuma request cabe jamais (reqCost=1 > 0).
	qp := qpTPM(1_000_000, 0, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
	)
	res, err := adm.Admit(context.Background(), scheduler.AdmitRequest{Key: testKey, EstimatedTokens: 1})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !res.Rejected || res.Granted {
		t.Fatalf("esperado rejeição permanente com RPM=0: %+v", res)
	}
}

func TestAdmit_TenantCapExceededRejectedPermanently(t *testing.T) {
	t.Parallel()
	base := time.Unix(19_000_000, 0)
	// Global folgado, cap do tenant = 200; pedido de 300 > cap ⇒ rejeição permanente.
	qp := qpTPM(1_000_000, 1_000_000, time.Minute)
	qp.SetTenant(testKey, "small", scheduler.ProviderLimits{TPM: 200, RPM: 1_000_000, Window: time.Minute})
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
	)
	res, err := adm.Admit(context.Background(), scheduler.AdmitRequest{Key: testKey, Tenant: "small", EstimatedTokens: 300})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !res.Rejected || res.Granted || res.RetryAfter != 0 {
		t.Fatalf("esperado rejeição permanente (custo>cap tenant): %+v", res)
	}
}

// ---------------------------------------------------------------------------
// AOS-027 remediação: idempotência sensível ao custo (Q3). Reusar um RequestID
// activo com custo DIFERENTE não é um retry — rejeita em vez de conceder cego.
// ---------------------------------------------------------------------------

func TestAdmit_IdempotencyCostConflict(t *testing.T) {
	t.Parallel()
	base := time.Unix(20_000_000, 0)
	qp := qpTPM(1_000_000, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
	)
	ctx := context.Background()

	// Primeiro admit reserva 100 sob RequestID "dup".
	r1, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, RequestID: "dup", EstimatedTokens: 100})
	if err != nil || !r1.Granted {
		t.Fatalf("primeiro admit: granted=%v err=%v", r1.Granted, err)
	}

	// Reusar "dup" com custo MAIOR (100000) tem de falhar — conceder cegamente
	// deixaria consumir 100000 contra apenas 100 debitados (oversubscription).
	_, err = adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, RequestID: "dup", EstimatedTokens: 100000})
	if err == nil {
		t.Fatalf("esperado erro de conflito de idempotência com custo divergente")
	}
	if !errors.Is(err, scheduler.ErrIdempotencyConflict) {
		t.Fatalf("esperado ErrIdempotencyConflict, obtido: %v", err)
	}

	// Reusar "dup" com o MESMO custo (100) continua idempotente ⇒ granted, sem
	// re-debitar.
	r3, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, RequestID: "dup", EstimatedTokens: 100})
	if err != nil || !r3.Granted {
		t.Fatalf("retry idempotente com mesmo custo: granted=%v err=%v", r3.Granted, err)
	}
	if r3.ReservationID != r1.ReservationID {
		t.Fatalf("reservation IDs divergem no retry idempotente")
	}
	// Só uma reserva materializada.
	recs, _ := adm.Replay(ctx, testKey)
	grants := 0
	for _, r := range recs {
		if r.Type == scheduler.EventAdmitGranted {
			grants++
		}
	}
	if grants != 1 {
		t.Fatalf("grants materializados=%d, esperado 1", grants)
	}
}
