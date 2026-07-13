package scheduler_test

// Testes do roteamento least-loaded / token-aware (AOS-033). Todos deterministas:
// relógio e IDs injectáveis, destinos iterados por ID, sem time.Now nem rand na
// decisão. Cobrem os Testes Requeridos do ticket:
//   - unit: selecção least-loaded sobre estados de carga sintéticos;
//   - unit: token-awareness — destino sem headroom é PRETERIDO;
//   - integração: distribuição MELHOR que round-robin sob carga HETEROGÉNEA
//     (menos hotspots — prova por max-load/variância, com um round-robin de
//     referência SÓ no teste);
//   - integração: tiering encaminha para o tier adequado, coerente com AOS-031;
//   - integração: decisões REPRODUZÍVEIS em replay (mesmos sinais ⇒ mesmo destino
//     e mesmos bytes de evento).

import (
	"context"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// newRouter constrói um Router de teste sobre um Event Store real (AOS-002) e um
// idGen determinístico.
func newRouter(t *testing.T, src scheduler.LoadSource, opts ...scheduler.RouterOption) (*scheduler.Router, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	base := time.Unix(1_700_000_000, 0)
	full := append([]scheduler.RouterOption{
		scheduler.WithRouteLog(es),
		scheduler.WithRouteClock(fixedClock(base)),
		scheduler.WithRouteIDGen(seqIDGen()),
	}, opts...)
	rt, err := scheduler.NewRouter(src, full...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return rt, es
}

// dest é um atalho para um destino sem tier.
func dest(id string) scheduler.Destination { return scheduler.Destination{ID: id} }

// ---------------------------------------------------------------------------
// Unit: selecção least-loaded sobre estados de carga sintéticos.
// ---------------------------------------------------------------------------

func TestRoute_LeastLoadedSelectsMinCost(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"), dest("w-c"))
	// w-b é o menos carregado (fila+tokens): tem de ser escolhido.
	src.SetLoad("w-a", scheduler.DestinationLoad{QueueDepth: 5, TokensInFlight: 500})
	src.SetLoad("w-b", scheduler.DestinationLoad{QueueDepth: 1, TokensInFlight: 100})
	src.SetLoad("w-c", scheduler.DestinationLoad{QueueDepth: 3, TokensInFlight: 300})

	rt, _ := newRouter(t, src)
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "job-1", EstimatedTokens: 50})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !dec.Routed || dec.Deferred {
		t.Fatalf("esperava Routed, veio %+v", dec)
	}
	if dec.Destination.ID != "w-b" {
		t.Fatalf("least-loaded devia escolher w-b, escolheu %q", dec.Destination.ID)
	}
	if dec.CostScore != 101 {
		t.Fatalf("custo de w-b = 1*1 + 1*100 = 101, veio %d", dec.CostScore)
	}
}

// Não é round-robin: duas rotas seguidas de trabalho barato, com o mesmo estado
// inicial e SEM reporter, escolhem o MESMO destino (o menos carregado) — a rotação
// cega escolheria destinos diferentes.
func TestRoute_NotRoundRobin_SameLeastLoadedTwice(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"))
	src.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 10})
	src.SetLoad("w-b", scheduler.DestinationLoad{TokensInFlight: 999})

	rt, _ := newRouter(t, src) // sem reporter: a carga não muda entre decisões
	for i := 0; i < 2; i++ {
		dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 1})
		if err != nil {
			t.Fatalf("Route[%d]: %v", i, err)
		}
		if dec.Destination.ID != "w-a" {
			t.Fatalf("Route[%d]: least-loaded devia repetir w-a, veio %q", i, dec.Destination.ID)
		}
	}
}

// Tie-break estável: com custos iguais, vence o menor id do destino.
func TestRoute_StableTieBreakByID(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-z"), dest("w-a"), dest("w-m"))
	load := scheduler.DestinationLoad{QueueDepth: 2, TokensInFlight: 200}
	src.SetLoad("w-z", load)
	src.SetLoad("w-a", load)
	src.SetLoad("w-m", load)

	rt, _ := newRouter(t, src)
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 1})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Destination.ID != "w-a" {
		t.Fatalf("empate devia resolver para o menor id (w-a), veio %q", dec.Destination.ID)
	}
}

// ---------------------------------------------------------------------------
// Unit: token-awareness — destino sem headroom LOCAL é PRETERIDO.
// ---------------------------------------------------------------------------

func TestRoute_TokenAware_SkipsNoHeadroom(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-cheap"), dest("w-full"))
	// w-cheap parece mais carregado no custo, MAS w-full não tem headroom para o
	// custo estimado (capacidade 1000, 990 em voo, resta 10 < 50). Preterido.
	src.SetLoad("w-full", scheduler.DestinationLoad{TokensInFlight: 990, CapacityTokens: 1000})
	src.SetLoad("w-cheap", scheduler.DestinationLoad{TokensInFlight: 995, CapacityTokens: 100000})

	rt, _ := newRouter(t, src)
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "big", EstimatedTokens: 50})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Destination.ID != "w-cheap" {
		t.Fatalf("destino sem headroom devia ser preterido; esperava w-cheap, veio %q", dec.Destination.ID)
	}
}

// Nenhum destino com headroom local e sem tiering ⇒ ADIA (observável, não silencioso).
func TestRoute_TokenAware_DefersWhenAllFull(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"))
	src.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 100, CapacityTokens: 100})
	src.SetLoad("w-b", scheduler.DestinationLoad{TokensInFlight: 100, CapacityTokens: 100})

	rt, es := newRouter(t, src)
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 50})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !dec.Deferred || dec.Routed {
		t.Fatalf("esperava Deferred, veio %+v", dec)
	}
	// O adiamento é OBSERVÁVEL: há um evento work_route_deferred.
	recs, err := rt.ReplayRouting(context.Background())
	if err != nil {
		t.Fatalf("ReplayRouting: %v", err)
	}
	if len(recs) != 1 || recs[0].Type != scheduler.EventWorkRouteDeferred {
		t.Fatalf("esperava 1 work_route_deferred, veio %+v", recs)
	}
	_ = es
}

// CONTRATO explícito (não implícito): SEM tecto local (CapacityTokens<=0) e SEM
// AdmissionGate, o custo estimado NÃO filtra destino nenhum — o router encaminha
// independentemente do headroom (a headroom só é porta quando há CapacityTokens>0 ou
// admissão). Torna contratual o comportamento "sem tecto = sem filtro" documentado
// em NewRouter, para que uma frota mal configurada (sem qualquer tecto) não presuma
// uma porta token-aware que não existe.
func TestRoute_NoCapNoAdmission_NoTokenFilter(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"))
	// Nenhum destino define CapacityTokens ⇒ headroom = -1 (sem filtro local). Mesmo
	// um custo estimado ASTRONÓMICO (muito acima de qualquer carga) tem de rotear.
	src.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 10})
	src.SetLoad("w-b", scheduler.DestinationLoad{TokensInFlight: 20})

	rt, _ := newRouter(t, src) // sem WithRouteAdmission
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "huge", EstimatedTokens: 1_000_000_000})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !dec.Routed || dec.Deferred {
		t.Fatalf("sem tecto e sem admissão, o custo estimado NÃO filtra: esperava Routed, veio %+v", dec)
	}
	if dec.Destination.ID != "w-a" {
		t.Fatalf("sem filtro, vence o menor custo (w-a), veio %q", dec.Destination.ID)
	}
	if dec.HeadroomTokens != -1 {
		t.Fatalf("sem tecto local a headroom devia ser -1 (sem porta), veio %d", dec.HeadroomTokens)
	}
}

// ---------------------------------------------------------------------------
// Unit: token-awareness GLOBAL via admission control (AOS-027) — reserva no Admit,
// destino sem headroom global é preterido.
// ---------------------------------------------------------------------------

func TestRoute_AdmissionHeadroom_SkipsGloballySaturated(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	// Duas chaves distintas: a "cheia" já está saturada no bucket global; a "livre"
	// tem headroom. O router deve preterir a cheia e reservar na livre.
	keyFull := scheduler.ProviderKey{Provider: "p", Model: "m", Region: "full"}
	keyFree := scheduler.ProviderKey{Provider: "p", Model: "m", Region: "free"}

	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 1000, RPM: 1_000_000, Window: time.Minute})
	adm, _ := newAdm(t, qp, scheduler.WithClock(fixedClock(base)), scheduler.WithIDGen(seqIDGen()))
	// Enche o bucket da chave "full" com uma reserva pré-existente de 1000 tokens.
	if _, err := adm.Admit(context.Background(), scheduler.AdmitRequest{Key: keyFull, EstimatedTokens: 1000, RequestID: "pre"}); err != nil {
		t.Fatalf("pré-reserva: %v", err)
	}

	src := scheduler.NewStaticLoadSource(
		scheduler.Destination{ID: "d-full", Key: keyFull},
		scheduler.Destination{ID: "d-free", Key: keyFree},
	)
	// d-full parece menos carregado localmente (custo menor), mas está saturado no
	// bucket global — tem de ser preterido pela admissão.
	src.SetLoad("d-full", scheduler.DestinationLoad{TokensInFlight: 0})
	src.SetLoad("d-free", scheduler.DestinationLoad{TokensInFlight: 5})

	rt, _ := newRouter(t, src, scheduler.WithRouteAdmission(adm))
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 100})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Destination.ID != "d-free" {
		t.Fatalf("destino globalmente saturado devia ser preterido; esperava d-free, veio %q", dec.Destination.ID)
	}
	if dec.ReservationID == "" {
		t.Fatalf("esperava reserva de headroom global no destino escolhido")
	}
}

// Rollback SIMÉTRICO no erro pós-decisão: se o emit falhar DEPOIS de a admissão
// conceder a reserva, (a) a reserva GLOBAL é libertada E (b) a carga LOCAL do destino
// NÃO é incrementada (sem carga fantasma). Antes da correcção, o Reserve local corria
// ANTES do emit e deixava +cost tokens/+1 fila permanentes no destino quando o emit
// falhava — enviesando o least-loaded seguinte. Este teste falha nessa ordem antiga.
func TestRoute_NoPhantomLoadAndReleasesReservationOnEmitError(t *testing.T) {
	t.Parallel()
	key := scheduler.ProviderKey{Provider: "p", Model: "m", Region: "r"}
	src := scheduler.NewStaticLoadSource(scheduler.Destination{ID: "w-a", Key: key})
	src.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 0, QueueDepth: 0})
	gate := &grantingGate{}
	rt, err := scheduler.NewRouter(src,
		scheduler.WithRouteLog(failingDispatchLog{}), // Append falha sempre ⇒ emit falha
		scheduler.WithRouteClock(fixedClock(time.Unix(1, 0))),
		scheduler.WithRouteIDGen(seqIDGen()),
		scheduler.WithRouteAdmission(gate),
		scheduler.WithLoadReporter(src),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if _, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 100}); err == nil {
		t.Fatalf("esperava erro propagado do emit falhado, veio nil")
	}
	// (a) reserva GLOBAL libertada — sem fuga de headroom.
	if len(gate.released) != 1 {
		t.Fatalf("esperava 1 reserva global libertada, veio %d (%v)", len(gate.released), gate.released)
	}
	// (b) SEM carga fantasma LOCAL: o emit falhou ANTES do Reserve local, logo os
	// tokens em voo e a fila do destino ficam intactos.
	l, _ := src.Load(context.Background(), "w-a")
	if l.TokensInFlight != 0 || l.QueueDepth != 0 {
		t.Fatalf("carga fantasma no destino após emit falhado: %+v (esperava tudo a zero)", l)
	}
}

// ---------------------------------------------------------------------------
// Integração: distribuição MELHOR que round-robin sob carga HETEROGÉNEA.
// ---------------------------------------------------------------------------

// roundRobin é um selector de REFERÊNCIA (só para o teste): rotação cega por
// índice, ignorando a carga real — o baseline que o least-loaded tem de bater.
type roundRobin struct {
	dests []scheduler.Destination
	next  int
}

func (r *roundRobin) pick() scheduler.Destination {
	d := r.dests[r.next%len(r.dests)]
	r.next++
	return d
}

// maxAndVariance devolve o max-load (hotspot) e a variância (×N, inteira) dos
// tokens finais em voo por destino.
func maxAndVariance(tokens []int64) (max int64, varTimesN int64) {
	var sum int64
	for _, v := range tokens {
		if v > max {
			max = v
		}
		sum += v
	}
	n := int64(len(tokens))
	mean := sum / n
	for _, v := range tokens {
		d := v - mean
		varTimesN += d * d
	}
	return max, varTimesN
}

func TestRoute_BeatsRoundRobin_HeterogeneousLoad(t *testing.T) {
	t.Parallel()
	ids := []string{"w-0", "w-1", "w-2", "w-3"}
	mkDests := func() []scheduler.Destination {
		ds := make([]scheduler.Destination, len(ids))
		for i, id := range ids {
			ds[i] = dest(id)
		}
		return ds
	}
	// Workload HETEROGÉNEO: custos de token muito variados (o que torna a rotação
	// cega desequilibrada — ignora que jobs grandes já foram para um worker).
	costs := []int64{200, 5, 300, 10, 250, 5, 400, 8, 150, 6, 350, 12, 220, 7, 500, 9}

	// --- Least-loaded (com reporter: a carga evolui a cada decisão) ---
	llSrc := scheduler.NewStaticLoadSource(mkDests()...)
	rt, _ := newRouter(t, llSrc, scheduler.WithLoadReporter(llSrc))
	for i, c := range costs {
		if _, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: c}); err != nil {
			t.Fatalf("least-loaded Route[%d]: %v", i, err)
		}
	}
	llTokens := make([]int64, len(ids))
	for i, id := range ids {
		l, _ := llSrc.Load(context.Background(), id)
		llTokens[i] = l.TokensInFlight
	}

	// --- Round-robin de referência (mesmo workload) ---
	rr := &roundRobin{dests: mkDests()}
	rrTokens := make([]int64, len(ids))
	idx := map[string]int{}
	for i, id := range ids {
		idx[id] = i
	}
	for _, c := range costs {
		d := rr.pick()
		rrTokens[idx[d.ID]] += c
	}

	llMax, llVar := maxAndVariance(llTokens)
	rrMax, rrVar := maxAndVariance(rrTokens)

	if llMax >= rrMax {
		t.Fatalf("least-loaded devia ter MENOR max-load (hotspot): ll=%d rr=%d (ll=%v rr=%v)", llMax, rrMax, llTokens, rrTokens)
	}
	if llVar >= rrVar {
		t.Fatalf("least-loaded devia ter MENOR variância: ll=%d rr=%d (ll=%v rr=%v)", llVar, rrVar, llTokens, rrTokens)
	}
	t.Logf("least-loaded max=%d var=%d %v | round-robin max=%d var=%d %v", llMax, llVar, llTokens, rrMax, rrVar, rrTokens)
}

// TestRoute_BeatsRoundRobin_AcrossPermutations prova a superioridade ESTRUTURAL do
// least-loaded (e não uma ordem hand-picked adversarial ao round-robin): o MESMO
// multiset heterogéneo é apresentado por várias ORDENS — a ordem dada (neutra),
// ordenado asc, ordenado desc e 30 permutações baralhadas DETERMINISTICAMENTE
// (semente fixa). Afirma-se que o least-loaded tem menor max-load MÉDIO e menor
// variância MÉDIA sobre TODAS as ordens (bound honesto, não um demonstrador cozinhado
// numa única ordem), e que vence individualmente na esmagadora maioria delas.
func TestRoute_BeatsRoundRobin_AcrossPermutations(t *testing.T) {
	t.Parallel()
	ids := []string{"w-0", "w-1", "w-2", "w-3"}
	mkDests := func() []scheduler.Destination {
		ds := make([]scheduler.Destination, len(ids))
		for i, id := range ids {
			ds[i] = dest(id)
		}
		return ds
	}
	idx := map[string]int{}
	for i, id := range ids {
		idx[id] = i
	}
	// O MESMO multiset heterogéneo do teste anterior — só a ORDEM varia.
	base := []int64{200, 5, 300, 10, 250, 5, 400, 8, 150, 6, 350, 12, 220, 7, 500, 9}

	// runOrder corre o least-loaded (com reporter, carga a evoluir) e o round-robin de
	// referência sobre uma dada ORDEM do multiset e devolve (llMax, llVar, rrMax, rrVar).
	runOrder := func(costs []int64) (llMax, llVar, rrMax, rrVar int64) {
		llSrc := scheduler.NewStaticLoadSource(mkDests()...)
		rt, _ := newRouter(t, llSrc, scheduler.WithLoadReporter(llSrc))
		for i, c := range costs {
			if _, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: c}); err != nil {
				t.Fatalf("least-loaded Route[%d]: %v", i, err)
			}
		}
		llTokens := make([]int64, len(ids))
		for i, id := range ids {
			l, _ := llSrc.Load(context.Background(), id)
			llTokens[i] = l.TokensInFlight
		}
		rr := &roundRobin{dests: mkDests()}
		rrTokens := make([]int64, len(ids))
		for _, c := range costs {
			d := rr.pick()
			rrTokens[idx[d.ID]] += c
		}
		llMax, llVar = maxAndVariance(llTokens)
		rrMax, rrVar = maxAndVariance(rrTokens)
		return llMax, llVar, rrMax, rrVar
	}

	// Ordens: neutra (como dada), asc, desc e permutações baralhadas com semente fixa
	// (deterministas ⇒ estáveis sob -race -count=N).
	orders := [][]int64{append([]int64(nil), base...)}
	asc := append([]int64(nil), base...)
	sort.Slice(asc, func(i, j int) bool { return asc[i] < asc[j] })
	orders = append(orders, asc)
	desc := append([]int64(nil), base...)
	sort.Slice(desc, func(i, j int) bool { return desc[i] > desc[j] })
	orders = append(orders, desc)
	rng := rand.New(rand.NewSource(20260712))
	for p := 0; p < 30; p++ {
		perm := append([]int64(nil), base...)
		rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		orders = append(orders, perm)
	}

	var sumLLMax, sumRRMax, sumLLVar, sumRRVar int64
	wins := 0
	for _, costs := range orders {
		llMax, llVar, rrMax, rrVar := runOrder(costs)
		sumLLMax += llMax
		sumRRMax += rrMax
		sumLLVar += llVar
		sumRRVar += rrVar
		if llMax < rrMax && llVar < rrVar {
			wins++
		}
	}
	n := len(orders)
	// Superioridade ESTRUTURAL: menor max-load e variância MÉDIOS sobre TODAS as ordens.
	if sumLLMax >= sumRRMax {
		t.Fatalf("least-loaded devia ter MENOR max-load MÉDIO sobre %d ordens: somaLL=%d somaRR=%d", n, sumLLMax, sumRRMax)
	}
	if sumLLVar >= sumRRVar {
		t.Fatalf("least-loaded devia ter MENOR variância MÉDIA sobre %d ordens: somaLL=%d somaRR=%d", n, sumLLVar, sumRRVar)
	}
	// A garantia HONESTA é a superioridade MÉDIA acima (o bound estrutural). Como
	// reforço, o least-loaded vence também individualmente (max E variância) na CLARA
	// maioria das ordens — não só numa ordem cozinhada.
	if wins*4 < n*3 {
		t.Fatalf("least-loaded devia bater o round-robin (max E variância) em >=75%% das %d ordens, venceu %d", n, wins)
	}
	t.Logf("least-loaded venceu %d/%d ordens; max médio ll=%d rr=%d; var média ll=%d rr=%d", wins, n, sumLLMax/int64(n), sumRRMax/int64(n), sumLLVar/int64(n), sumRRVar/int64(n))
}

// ---------------------------------------------------------------------------
// Integração: cost-aware model tiering coerente com AOS-031.
// ---------------------------------------------------------------------------

// tierLadder é a MESMA escada usada pelo downgrade de AOS-031 (StaticModelTierRouter,
// por CostRank). O router reutiliza-a — coerência por reutilização.
func tierLadder() *scheduler.StaticModelTierRouter {
	return scheduler.NewStaticModelTierRouter(
		scheduler.ModelTier{Tier: "premium", Model: "big", CostRank: 3},
		scheduler.ModelTier{Tier: "standard", Model: "mid", CostRank: 2},
		scheduler.ModelTier{Tier: "economy", Model: "small", CostRank: 1},
	)
}

func TestRoute_CostAwareTiering_DescendsWhenTierSaturated(t *testing.T) {
	t.Parallel()
	// Um destino por tier. O tier premium está sem headroom local; economy tem
	// margem. Trabalho elegível deve DESCER premium→standard→economy (coerente com
	// a escada de AOS-031) e rotear no economy, registando a variância.
	src := scheduler.NewStaticLoadSource(
		scheduler.Destination{ID: "d-premium", Tier: "premium", Model: "big"},
		scheduler.Destination{ID: "d-standard", Tier: "standard", Model: "mid"},
		scheduler.Destination{ID: "d-economy", Tier: "economy", Model: "small"},
	)
	src.SetLoad("d-premium", scheduler.DestinationLoad{TokensInFlight: 100, CapacityTokens: 100})  // cheio
	src.SetLoad("d-standard", scheduler.DestinationLoad{TokensInFlight: 100, CapacityTokens: 100}) // cheio
	src.SetLoad("d-economy", scheduler.DestinationLoad{TokensInFlight: 0, CapacityTokens: 100000}) // livre

	rt, _ := newRouter(t, src, scheduler.WithTierRouter(tierLadder()))
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{
		ID: "j", EstimatedTokens: 50, CurrentTier: "premium", CurrentModel: "big", TierEligible: true,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Destination.ID != "d-economy" {
		t.Fatalf("tiering devia descer até economy, roteou para %q", dec.Destination.ID)
	}
	if !dec.Downgraded || dec.FromTier != "premium" || dec.ToTier != "economy" {
		t.Fatalf("esperava variância premium→economy, veio downgraded=%v %s→%s", dec.Downgraded, dec.FromTier, dec.ToTier)
	}
	if dec.ToModel != "small" {
		t.Fatalf("modelo do tier economy devia ser small, veio %q", dec.ToModel)
	}
}

// Trabalho NÃO elegível a tiering fica no tier corrente: se saturado, ADIA (não
// desce silenciosamente para um tier mais barato).
func TestRoute_NotTierEligible_StaysInTierAndDefers(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(
		scheduler.Destination{ID: "d-premium", Tier: "premium", Model: "big"},
		scheduler.Destination{ID: "d-economy", Tier: "economy", Model: "small"},
	)
	src.SetLoad("d-premium", scheduler.DestinationLoad{TokensInFlight: 100, CapacityTokens: 100})
	src.SetLoad("d-economy", scheduler.DestinationLoad{TokensInFlight: 0, CapacityTokens: 100000})

	rt, _ := newRouter(t, src, scheduler.WithTierRouter(tierLadder()))
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{
		ID: "j", EstimatedTokens: 50, CurrentTier: "premium", CurrentModel: "big", TierEligible: false,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !dec.Deferred {
		t.Fatalf("trabalho não-elegível em tier saturado devia ADIAR, veio %+v", dec)
	}
}

// ---------------------------------------------------------------------------
// Integração: decisões REPRODUZÍVEIS em replay (mesmos sinais ⇒ mesmo destino e
// mesmos bytes de evento).
// ---------------------------------------------------------------------------

func TestRoute_DeterministicReplay_SameSignalsSameDecisions(t *testing.T) {
	t.Parallel()
	build := func() ([]scheduler.RouteRecord, [][]byte) {
		src := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"), dest("w-c"))
		src.SetLoad("w-a", scheduler.DestinationLoad{QueueDepth: 4, TokensInFlight: 400})
		src.SetLoad("w-b", scheduler.DestinationLoad{QueueDepth: 1, TokensInFlight: 100})
		src.SetLoad("w-c", scheduler.DestinationLoad{QueueDepth: 2, TokensInFlight: 250})

		es, err := eventstore.New()
		if err != nil {
			t.Fatalf("eventstore.New: %v", err)
		}
		base := time.Unix(1_700_000_000, 0)
		rt, err := scheduler.NewRouter(src,
			scheduler.WithRouteLog(es),
			scheduler.WithRouteClock(fixedClock(base)),
			scheduler.WithRouteIDGen(seqIDGen()),
			scheduler.WithLoadReporter(src),
		)
		if err != nil {
			t.Fatalf("NewRouter: %v", err)
		}
		for _, c := range []int64{50, 50, 50, 50} {
			if _, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: c}); err != nil {
				t.Fatalf("Route: %v", err)
			}
		}
		recs, err := rt.ReplayRouting(context.Background())
		if err != nil {
			t.Fatalf("ReplayRouting: %v", err)
		}
		raw, err := es.Read(context.Background(), rt.RoutingStreamID(), 1)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		payloads := make([][]byte, len(raw))
		for i, ev := range raw {
			payloads[i] = ev.Payload
		}
		return recs, payloads
	}

	recs1, pl1 := build()
	recs2, pl2 := build()

	if len(recs1) != len(recs2) || len(recs1) == 0 {
		t.Fatalf("contagens divergentes: %d vs %d", len(recs1), len(recs2))
	}
	for i := range recs1 {
		if recs1[i].Destination != recs2[i].Destination {
			t.Fatalf("decisão[%d] divergiu: %q vs %q (não determinístico)", i, recs1[i].Destination, recs2[i].Destination)
		}
		if string(pl1[i]) != string(pl2[i]) {
			t.Fatalf("bytes de evento[%d] divergiram:\n%s\n%s", i, pl1[i], pl2[i])
		}
	}
	// Sanidade: com reporter, 4 jobs iguais espalham-se (least-loaded equaliza), não
	// vão todos para o mesmo destino.
	seen := map[string]bool{}
	for _, r := range recs1 {
		seen[r.Destination] = true
	}
	if len(seen) < 2 {
		t.Fatalf("least-loaded com reporter devia espalhar por >1 destino, foi %v", seen)
	}
}

// Knobs de configuração: ponderação própria (latência conta), função de custo
// própria, NHI emissora e nome de instância (stream próprio).
func TestRoute_CustomWeightsAndCostAndNaming(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"))
	// Sem latência no custo, w-a (menos tokens) venceria; com peso de latência alto,
	// w-b (menos latência) passa a ser o menos "carregado".
	src.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 10, RecentLatencyMs: 900})
	src.SetLoad("w-b", scheduler.DestinationLoad{TokensInFlight: 20, RecentLatencyMs: 5})

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rt, err := scheduler.NewRouter(src,
		scheduler.WithRouteLog(es),
		scheduler.WithRouteClock(fixedClock(time.Unix(1, 0))),
		scheduler.WithRouteIDGen(seqIDGen()),
		scheduler.WithRouteWeights(scheduler.RouteWeights{Queue: 0, Token: 1, Latency: 10}),
		scheduler.WithRouteProducer(eventstore.Producer{NHIID: "nhi:test/router"}),
		scheduler.WithRouteName("router-x"),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	dec, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 1})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Destination.ID != "w-b" {
		t.Fatalf("peso de latência devia favorecer w-b, veio %q", dec.Destination.ID)
	}
	// O nome próprio isola o stream de eventos.
	if rt.RoutingStreamID() != "routing/router-x" {
		t.Fatalf("stream devia ser routing/router-x, veio %q", rt.RoutingStreamID())
	}

	// Função de custo própria: inverte o critério (favorece MAIS tokens em voo).
	src2 := scheduler.NewStaticLoadSource(dest("w-a"), dest("w-b"))
	src2.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 10})
	src2.SetLoad("w-b", scheduler.DestinationLoad{TokensInFlight: 20})
	rt2, err := scheduler.NewRouter(src2,
		scheduler.WithRouteClock(fixedClock(time.Unix(1, 0))),
		scheduler.WithRouteIDGen(seqIDGen()),
		scheduler.WithLoadCost(func(l scheduler.DestinationLoad) int64 { return -l.TokensInFlight }),
	)
	if err != nil {
		t.Fatalf("NewRouter2: %v", err)
	}
	dec2, err := rt2.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 1})
	if err != nil {
		t.Fatalf("Route2: %v", err)
	}
	if dec2.Destination.ID != "w-b" {
		t.Fatalf("função de custo própria devia escolher w-b, veio %q", dec2.Destination.ID)
	}
}

// Span OTel por decisão: destino/carga observáveis via a porta zero-dep.
func TestRoute_EmitsSpanPerDecision(t *testing.T) {
	t.Parallel()
	src := scheduler.NewStaticLoadSource(dest("w-a"))
	src.SetLoad("w-a", scheduler.DestinationLoad{TokensInFlight: 7})
	tr := &agentruntime.RecordingTracer{}
	rt, _ := newRouter(t, src, scheduler.WithRouteTracer(tr))
	if _, err := rt.Route(context.Background(), scheduler.WorkRequest{ID: "j", EstimatedTokens: 3}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	spans := tr.SpansByOperation("routing_decision")
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span routing_decision, veio %d", len(spans))
	}
	if got := spans[0].Attributes["aos.routing.destination"]; got != "w-a" {
		t.Fatalf("span devia registar o destino w-a, veio %v", got)
	}
}
