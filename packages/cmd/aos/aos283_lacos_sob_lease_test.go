package main

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos283_lacos_sob_lease_test.go — PROVAS FALSIFICÁVEIS da posse dos laços de serviço.
//
// O DEFEITO, MEDIDO ANTES DE SER FECHADO (2026-09-01). Duas réplicas sobre o MESMO
// substrato — Event Store, cadeia WORM e custódia da KEK partilhados — selavam o MESMO
// facto DUAS vezes:
//
//	MEDIÇÃO — selos retention.expired por record_id: map[01M1FC6QYCRXYSM36B8VE0Z0JJ:2]
//
// Não era o guard `expireInFlight` estar mal escrito: é ele ser um `atomic.Bool` POR
// PROCESSO, e a idempotência do [audit.ExpirationJob] viver num seen-set in-memory
// re-hidratado UMA vez no arranque. Duas réplicas que arrancam antes da primeira passagem
// têm ambas o seen-set vazio e ambas selam.
//
// Cada teste aqui é NÃO-VACUOSO e prova os dois sentidos. Correr SEMPRE com -race.

// -------------------------------------------------------------------------------------
// Substrato partilhado por duas réplicas
// -------------------------------------------------------------------------------------

// substratoPartilhado é o que faz de dois nós DUAS RÉPLICAS e não dois sistemas: o Event
// Store (onde os registos expiráveis vivem e onde os streams de posse são arbitrados), a
// cadeia WORM (onde os `retention.expired` são selados) e a custódia da KEK (o alvo do
// crypto-shred). Se cada uma tivesse a sua cadeia, um segundo selo não seria uma
// duplicação — seria outro facto, e o teste não mediria nada.
type substratoPartilhado struct {
	es    *eventstore.Store
	worm  audit.Store
	vault audit.KeyVault
}

func novoSubstratoPartilhado(t *testing.T) *substratoPartilhado {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return &substratoPartilhado{
		es:    es,
		worm:  audit.NewMemStore(),
		vault: audit.NewInMemoryKeyVault(nil),
	}
}

// replica arranca UMA réplica sobre o substrato partilhado, com a política de retenção
// armada e o relógio de retenção muito à frente (o molde de [newRetentionNode]).
func (s *substratoPartilhado) replica(t *testing.T) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStore = s.es
	cfg.WORM = s.worm
	cfg.DSARVault = s.vault
	rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{
		audit.ClassPIIOperational: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRetentionConfig: %v", err)
	}
	cfg.Retention = rc
	cfg.RetentionClock = retentionFarFuture
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (replica sobre substrato partilhado): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// selosDeExpiracaoPorRegisto conta, na cadeia de retenção, quantos `retention.expired`
// existem por `record_id`. É a MEDIÇÃO: >1 para o mesmo id é o facto selado duas vezes.
func selosDeExpiracaoPorRegisto(t *testing.T, worm audit.Store) map[string]int {
	t.Helper()
	ctx := context.Background()
	head, err := worm.Head(ctx, retentionPartition)
	if err != nil {
		t.Fatalf("Head(%q): %v", retentionPartition, err)
	}
	out := map[string]int{}
	if head == 0 {
		return out
	}
	recs, err := worm.Read(ctx, retentionPartition, 1, head)
	if err != nil {
		t.Fatalf("Read(%q): %v", retentionPartition, err)
	}
	for _, rec := range recs {
		if rec.Resource.Type != audit.RetentionExpiredEventType {
			continue
		}
		for _, ob := range rec.Obligations {
			if ob.Type != audit.RetentionExpiredEventType {
				continue
			}
			if id := ob.Params["record_id"]; id != "" {
				out[id]++
			}
		}
	}
	return out
}

// -------------------------------------------------------------------------------------
// (CA4) NENHUM FACTO SELADO DUAS VEZES — a medição que abriu o ticket.
// -------------------------------------------------------------------------------------

// TestAOS283_UmFactoDeRetencaoNaoESeladoDuasVezesPorDuasReplicas é o teste central.
//
// FALSIFICÁVEL, e foi falsificado: sem a posse de `lease:svc:retention`, esta bateria
// mediu `record_id:2` no código de 2026-09-01 — o defeito que o comentário do
// `expireInFlight` nomeia e que um guard por-processo não pode fechar.
func TestAOS283_UmFactoDeRetencaoNaoESeladoDuasVezesPorDuasReplicas(t *testing.T) {
	sub := novoSubstratoPartilhado(t)

	// As DUAS réplicas arrancam ANTES de qualquer passagem — é o caso real (duas réplicas
	// lado a lado) e é o que deixa os dois seen-sets in-memory vazios.
	a := sub.replica(t)
	b := sub.replica(t)

	captureSynthetic(t, a, "nhi:agent-283", "run-283", "conteudo replicado: DUP-283", "out283")

	svcA := newScheduledRetentionService(t, a, time.Hour)
	svcB := newScheduledRetentionService(t, b, time.Hour)

	ctx := context.Background()
	if !svcA.SweepRetentionNow(ctx) {
		t.Fatal("passagem da replica A parou por incidente de integridade")
	}
	if !svcB.SweepRetentionNow(ctx) {
		t.Fatal("passagem da replica B parou por incidente de integridade")
	}

	selos := selosDeExpiracaoPorRegisto(t, sub.worm)
	if len(selos) == 0 {
		t.Fatal("VÁCUO: nenhuma expiração foi selada — a bateria não mediu nada")
	}
	for id, n := range selos {
		if n > 1 {
			t.Fatalf("facto %q selado %d vezes por duas réplicas — a exclusão entre processos não está a valer", id, n)
		}
	}

	// NÃO-VÁCUO do outro lado: a expiração ACONTECEU mesmo (a líder correu), não é um
	// «zero selos» a passar por «zero duplicados».
	if svcA.varrimentosTotal.Load() != 1 {
		t.Errorf("a réplica LÍDER devia ter concluído 1 passagem, tem %d", svcA.varrimentosTotal.Load())
	}
}

// -------------------------------------------------------------------------------------
// (CA1) SEM POSSE, O LAÇO NÃO CORRE — fail-closed, e observável na réplica.
// -------------------------------------------------------------------------------------

func TestAOS283_SemPosseOLacoDeRetencaoNAOCorre(t *testing.T) {
	sub := novoSubstratoPartilhado(t)
	a := sub.replica(t)
	b := sub.replica(t)
	captureSynthetic(t, a, "nhi:agent-283-excl", "run-283-excl", "conteudo: EXCL-283", "outExcl")

	svcA := newScheduledRetentionService(t, a, time.Hour)
	svcB := newScheduledRetentionService(t, b, time.Hour)

	ctx := context.Background()
	svcA.SweepRetentionNow(ctx) // A toma a posse
	svcB.SweepRetentionNow(ctx) // B encontra-a detida
	svcB.SweepRetentionNow(ctx) // e continua a encontrá-la detida

	if !svcA.posseRetencao.souLider() {
		t.Fatal("a réplica A devia deter a posse depois de a reclamar primeiro")
	}
	if svcB.posseRetencao.souLider() {
		t.Fatal("a réplica B declara-se LÍDER com o lease detido por A — a posse não está a arbitrar")
	}
	if n := svcB.varrimentosTotal.Load(); n != 0 {
		t.Fatalf("a réplica SEM posse concluiu %d passagem(ns) — o laço de exclusão obrigatória tem de ser fail-closed sem posse", n)
	}
	if n := svcA.varrimentosTotal.Load(); n != 1 {
		t.Fatalf("a réplica COM posse concluiu %d passagens, esperava 1 — sem isto o teste passaria com AMBAS paradas", n)
	}

	// O SELO DE ATRIBUIÇÃO confirma-o na cadeia: uma passagem, um `retention.sweep.started`.
	// Sem a posse seriam DOIS, e a cadeia registaria duas autoridades a destruir o mesmo.
	if n := len(retentionSweepRecords(t, a, retentionSweepStartedEvent)); n != 1 {
		t.Fatalf("%d selos retention.sweep.started na cadeia partilhada, esperava 1 (só a líder sela)", n)
	}
}

// (CA2) O SHUTDOWN ANUNCIA A LARGADA — a réplica seguinte assume sem esperar o TTL.
//
// É a metade do critério que vive no [NodeService], e não na máquina de posse: um
// `Shutdown` que só fechasse o `sweepStop` deixaria o lease vivo até ao fim do TTL e a
// réplica seguinte ficaria sem expirar nada durante todo esse tempo — com a política de
// retenção a anunciar um prazo que ninguém estaria a aplicar.
//
// O relógio do lease destes serviços é FIXO ([svcClock]), pelo que nenhuma expiração por
// TTL pode acontecer: se B assume, assume porque A anunciou.
func TestAOS283_ShutdownAnunciaALargadaEAReplicaSeguinteAssume(t *testing.T) {
	sub := novoSubstratoPartilhado(t)
	a := sub.replica(t)
	b := sub.replica(t)
	captureSynthetic(t, a, "nhi:agent-283-hs", "run-283-hs", "conteudo: HS-283", "outHS")

	svcA := newScheduledRetentionService(t, a, time.Hour)
	svcB := newScheduledRetentionService(t, b, time.Hour)

	ctx := context.Background()
	svcA.SweepRetentionNow(ctx)
	// NÃO-VÁCUO: com A viva, B não entra.
	svcB.SweepRetentionNow(ctx)
	if svcB.posseRetencao.souLider() {
		t.Fatal("B assumiu com A viva — o cenário não está montado")
	}

	sc, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := svcA.Shutdown(sc); err != nil {
		t.Fatalf("Shutdown de A: %v", err)
	}

	svcB.SweepRetentionNow(ctx)
	if !svcB.posseRetencao.souLider() {
		t.Fatal("B não assumiu depois do shutdown de A — o shutdown não anunciou a largada e B teria de esperar o TTL inteiro")
	}
}

// -------------------------------------------------------------------------------------
// (CA6) `/metrics` DISTINGUE «NÃO SOU LÍDER» de «armado e à espera» e de «parado».
// -------------------------------------------------------------------------------------

// TestAOS283_MetricasDistinguemNaoSouLiderDeArmadoEDeParado prova a distinção que faltava.
//
// Com N réplicas, N−1 publicam `sweeps_total=0` e nenhuma `age` — indistinguível, sem esta
// série, de «armado e nunca correu», que é o sintoma de um laço MORTO. As três leituras
// exigem acções diferentes e o operador estaria a agir sobre a réplica errada.
func TestAOS283_MetricasDistinguemNaoSouLiderDeArmadoEDeParado(t *testing.T) {
	sub := novoSubstratoPartilhado(t)
	a := sub.replica(t)
	b := sub.replica(t)
	captureSynthetic(t, a, "nhi:agent-283-met", "run-283-met", "conteudo: MET-283", "outMet")

	svcA := newScheduledRetentionService(t, a, time.Hour)
	svcB := newScheduledRetentionService(t, b, time.Hour)
	hA := &apiHandler{node: a, svc: svcA}
	hB := &apiHandler{node: b, svc: svcB}

	// ANTES de qualquer passagem: ambas ARMADAS e nenhuma líder ainda — «armado e à espera
	// do primeiro tick», que é uma leitura legítima e distinta de «não sou líder».
	for nome, h := range map[string]*apiHandler{"A": hA, "B": hB} {
		corpo := metricasDe(t, h)
		if v, ok := valorDe(t, corpo, "aos_retention_scheduler_armed"); !ok || v != 1 {
			t.Fatalf("replica %s: armed = %v (presente=%v), o cenario nao esta montado", nome, v, ok)
		}
		if v, ok := valorDe(t, corpo, "aos_retention_scheduler_leader"); !ok || v != 0 {
			t.Fatalf("replica %s antes do primeiro tick: leader = %v (presente=%v), queria 0 presente", nome, v, ok)
		}
	}

	ctx := context.Background()
	svcA.SweepRetentionNow(ctx)
	svcB.SweepRetentionNow(ctx)

	corpoA, corpoB := metricasDe(t, hA), metricasDe(t, hB)
	if v, ok := valorDe(t, corpoA, "aos_retention_scheduler_leader"); !ok || v != 1 {
		t.Errorf("A (com posse): leader = %v (presente=%v), queria 1", v, ok)
	}
	if v, ok := valorDe(t, corpoB, "aos_retention_scheduler_leader"); !ok || v != 0 {
		t.Errorf("B (sem posse): leader = %v (presente=%v), queria 0", v, ok)
	}
	// A CONFUSÃO QUE ESTA SÉRIE DESFAZ: sem ela, B é indistinguível de um laço morto.
	if v, ok := valorDe(t, corpoB, "aos_retention_sweeps_total"); !ok || v != 0 {
		t.Errorf("B: sweeps_total = %v (presente=%v), queria 0 — e e exactamente por isso que leader e preciso", v, ok)
	}
	if _, ok := valorDe(t, corpoB, "aos_retention_last_sweep_age_seconds"); ok {
		t.Error("B: a idade saiu numa replica que nunca varreu — leria-se como «varreu ha pouco»")
	}
	// «PARADO» continua a ser a sua própria leitura, e não se confunde com nenhuma das outras.
	svcA.varredorParado.Store(true)
	if v, ok := valorDe(t, metricasDe(t, hA), "aos_retention_scheduler_stopped"); !ok || v != 1 {
		t.Errorf("A: stopped = %v (presente=%v), queria 1", v, ok)
	}
}

// -------------------------------------------------------------------------------------
// A MÁQUINA DE POSSE — handoff por anúncio, morte por TTL, renovação por heartbeat.
//
// Exercitada directamente sobre [posseDeLaco] porque é aqui que as três propriedades
// vivem, e porque simular a MORTE de uma réplica pelo `Shutdown` seria simular o
// contrário: o `Shutdown` ANUNCIA, e a morte é precisamente o caso em que ninguém anuncia.
// -------------------------------------------------------------------------------------

// relogioMovel é um relógio avançável, seguro para uso concorrente (o renovador lê-o
// noutra goroutine).
type relogioMovel struct {
	mu    sync.Mutex
	agora time.Time
}

func novoRelogioMovel() *relogioMovel {
	return &relogioMovel{agora: time.Unix(1_700_000_000, 0).UTC()}
}

func (r *relogioMovel) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agora
}

func (r *relogioMovel) avancar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agora = r.agora.Add(d)
}

// duasPosses devolve duas posses do MESMO laço sobre o MESMO Event Store, com o MESMO
// relógio — duas réplicas, uma autoridade de lease cada, um só árbitro (o log).
func duasPosses(t *testing.T, ttl, hb time.Duration) (*relogioMovel, *posseDeLaco, *posseDeLaco) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rel := novoRelogioMovel()
	nova := func(worker string) *posseDeLaco {
		lm, err := durable.NewLeaseManager(es, ttl,
			durable.WithLeaseClock(durable.ClockFunc(rel.Now)), durable.WithWorkerID(worker))
		if err != nil {
			t.Fatalf("NewLeaseManager(%s): %v", worker, err)
		}
		return novaPosseDeLaco(lacoRetencao, lm, hb, nil)
	}
	return rel, nova("replica-A"), nova("replica-B")
}

// (CA2) O ANÚNCIO PASSA A POSSE SEM ESPERAR O TTL.
func TestAOS283_LargarPorAnuncioPassaAPosseSemEsperarOTTL(t *testing.T) {
	ctx := context.Background()
	rel, a, b := duasPosses(t, time.Minute, time.Hour) // hb longo: nada renova durante o teste
	parar := make(chan struct{})
	defer close(parar)

	if !a.assumir(ctx, parar) {
		t.Fatal("A devia assumir um laço livre")
	}
	// NÃO-VÁCUO: com o lease vivo e SEM anúncio, B não entra. Sem esta asserção o teste
	// passaria mesmo que `assumir` deixasse entrar toda a gente.
	if b.assumir(ctx, parar) {
		t.Fatal("B assumiu um laço com lease VIVO detido por A — não há exclusão nenhuma")
	}

	a.largar(ctx)

	// O RELÓGIO NÃO ANDOU: se B entrar agora, entrou pelo ANÚNCIO e não pelo TTL.
	if antes := rel.Now(); !antes.Equal(rel.Now()) {
		t.Fatal("relógio inconsistente")
	}
	if !b.assumir(ctx, parar) {
		t.Fatal("B não assumiu depois do anúncio de largada — teria de esperar o TTL inteiro, que é o que o anúncio existe para evitar")
	}
	if a.souLider() {
		t.Error("A continua a declarar-se líder depois de ter largado")
	}
	if !b.souLider() {
		t.Error("B não se declara líder depois de assumir")
	}
	b.largar(ctx)
}

// (CA3) A MORTE DO LÍDER — sem anúncio — É RECUPERADA POR EXPIRAÇÃO DE TTL.
func TestAOS283_MorteDoLiderSemAnuncioERecuperadaPorTTL(t *testing.T) {
	ctx := context.Background()
	const ttl = time.Minute
	rel, a, b := duasPosses(t, ttl, time.Hour) // hb longo: A nunca renova — é a "morte"
	parar := make(chan struct{})
	defer close(parar)

	if !a.assumir(ctx, parar) {
		t.Fatal("A devia assumir um laço livre")
	}
	// A MORRE aqui: não anuncia, não renova. Só o tempo passa.
	if b.assumir(ctx, parar) {
		t.Fatal("B assumiu ANTES de o TTL expirar — o TTL não estaria a valer")
	}
	rel.avancar(ttl + time.Second)
	if !b.assumir(ctx, parar) {
		t.Fatalf("B não assumiu depois de o TTL de %s expirar — a morte do líder ficaria irrecuperável e o laço nunca mais correria", ttl)
	}
	b.largar(ctx)
}

// (CA2) A POSSE É RENOVADA POR HEARTBEAT — e sem renovação NÃO sobrevive.
//
// Os dois sentidos no mesmo teste, porque é a comparação que prova alguma coisa: o mesmo
// avanço de relógio mantém a posse QUANDO há renovador e perde-a quando não há.
func TestAOS283_PosseSobreviveAoTTLPorRenovacaoENaoSemEla(t *testing.T) {
	ctx := context.Background()
	const ttl = time.Minute
	parar := make(chan struct{})
	defer close(parar)

	// (i) COM renovação: `renovar` é conduzido à mão, meio-TTL de cada vez, até o tempo
	// decorrido ultrapassar folgadamente o TTL.
	rel, a, b := duasPosses(t, ttl, time.Hour)
	if !a.assumir(ctx, parar) {
		t.Fatal("A devia assumir um laço livre")
	}
	for i := 0; i < 4; i++ {
		rel.avancar(ttl / 2)
		if !a.renovar(ctx) {
			t.Fatalf("renovação %d falhou — a posse não sobrevive ao próprio TTL", i+1)
		}
	}
	if b.assumir(ctx, parar) {
		t.Fatalf("B assumiu depois de %s (2x o TTL) apesar de A ter renovado — o heartbeat não está a estender a posse", 2*ttl)
	}

	// (ii) SEM renovação, o MESMO avanço: a posse cai. É o controlo que torna (i) não-vacuoso.
	rel2, c, d := duasPosses(t, ttl, time.Hour)
	if !c.assumir(ctx, parar) {
		t.Fatal("C devia assumir um laço livre")
	}
	rel2.avancar(2 * ttl)
	if !d.assumir(ctx, parar) {
		t.Fatal("D não assumiu sem renovação nenhuma de C — então (i) não provou que foi a renovação a segurar a posse")
	}
	a.largar(ctx)
	d.largar(ctx)
}

// O RENOVADOR DE FUNDO ARRANCA COM A POSSE — a metade de (CA2) que vive no `assumir`, e
// não no `renovar` conduzido à mão pelos testes acima.
func TestAOS283_AssumirArrancaORenovadorDeFundo(t *testing.T) {
	ctx := context.Background()
	const ttl = time.Minute
	rel, a, _ := duasPosses(t, ttl, 5*time.Millisecond)
	parar := make(chan struct{})
	defer close(parar)

	if !a.assumir(ctx, parar) {
		t.Fatal("A devia assumir um laço livre")
	}
	a.mu.Lock()
	expiraInicial := a.lease.ExpiresAt
	a.mu.Unlock()

	// O relógio avança MENOS de um TTL: o renovador de fundo tem de estender a expiração
	// sem que ninguém lhe toque.
	rel.avancar(ttl / 2)
	estendeu := false
	for fim := time.Now().Add(5 * time.Second); time.Now().Before(fim); {
		a.mu.Lock()
		nova := a.lease.ExpiresAt
		a.mu.Unlock()
		if nova.After(expiraInicial) {
			estendeu = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !estendeu {
		t.Fatal("o renovador de fundo não estendeu a expiração — com uma cadência de varrimento de 1h e um TTL de minutos, a posse expiraria entre dois ticks e a liderança saltaria de réplica a cada passagem")
	}
	a.largar(ctx)
}
