package main

import (
	"io"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// «A EXPIRAÇÃO POR TTL ESTÁ A CORRER?» — uma pergunta que não tinha resposta em runtime.
//
// O escalonador declara-se no banner de ARRANQUE e depois é invisível. Se nunca for armado
// (política ausente, intervalo <= 0) ou se PARAR — e ele pára para sempre depois de um incidente
// de integridade da hash-chain, deliberadamente —, o único sinal era uma linha de log que ninguém
// está a ver. E o que deixa de acontecer é o apagamento de dados fora do TTL: uma obrigação com
// prazo, e a única que não dá sinal nenhum por si mesma.
// ---------------------------------------------------------------------------------------------

// TestSemEscalonadorArmadoODizEmMetrics — o caso mais banal, e o mais fácil de não notar.
func TestSemEscalonadorArmadoODizEmMetrics(t *testing.T) {
	h := &apiHandler{node: &Node{}, svc: &NodeService{retentionSweepInterval: time.Hour}}
	corpo := metricasDe(t, h)

	v, ok := valorDe(t, corpo, "aos_retention_scheduler_armed")
	if !ok {
		t.Fatal("aos_retention_scheduler_armed AUSENTE — sem ela, «nada expira» e indistinguivel de «expira bem»")
	}
	if v != 0 {
		t.Errorf("armed = %v num no sem ExpirationJob nem WORM, queria 0", v)
	}
	// CONTROLO: com o escalonador desarmado, as séries que descrevem PASSAGENS não saem. Emitir
	// `sweeps_total 0` e `age 0` faria um nó que nunca vai expirar nada parecer um nó acabado de
	// varrer — a leitura mais errada possível, e a que cala a única pergunta que interessa.
	for _, s := range []string{"aos_retention_sweeps_total", "aos_retention_last_sweep_age_seconds", "aos_retention_scheduler_stopped"} {
		if _, ok := valorDe(t, corpo, s); ok {
			t.Errorf("%s saiu com o escalonador DESARMADO — descreve passagens que nunca vao acontecer", s)
		}
	}
}

// TestIdadeSoSaiDEPOISDaPrimeiraPassagem — a distinção entre «à espera do primeiro tick» e «morto».
func TestIdadeSoSaiDEPOISDaPrimeiraPassagem(t *testing.T) {
	svc := &NodeService{retentionSweepInterval: time.Hour}
	h := &apiHandler{node: nodeComRetencaoArmada(t), svc: svc}

	// Armado, ainda sem passagens: a IDADE nao sai.
	corpo := metricasDe(t, h)
	if v, ok := valorDe(t, corpo, "aos_retention_scheduler_armed"); !ok || v != 1 {
		t.Fatalf("o cenario nao esta montado: armed = %v (presente=%v)", v, ok)
	}
	if _, ok := valorDe(t, corpo, "aos_retention_last_sweep_age_seconds"); ok {
		t.Error("a idade saiu ANTES da primeira passagem — 0 diria «acabou de varrer» sobre um no " +
			"que ainda nao varreu nada, e um no recem-arrancado ficaria indistinguivel de um saudavel")
	}
	if v, ok := valorDe(t, corpo, "aos_retention_sweeps_total"); !ok || v != 0 {
		t.Errorf("sweeps_total = %v (presente=%v) — o CONTADOR sai desde o inicio, e zero e a leitura certa", v, ok)
	}

	// Depois de uma passagem: a idade sai, e e recente.
	svc.varrimentosTotal.Add(1)
	svc.ultimoVarrimentoUnix.Store(time.Now().Add(-90 * time.Minute).Unix())
	corpo = metricasDe(t, h)
	idade, ok := valorDe(t, corpo, "aos_retention_last_sweep_age_seconds")
	if !ok {
		t.Fatal("a idade nao saiu DEPOIS de uma passagem")
	}
	if idade < 80*60 || idade > 100*60 {
		t.Errorf("idade = %.0f s, esperava ~5400 (90 min)", idade)
	}
}

// TestParagemDefinitivaEDITA — a paragem por incidente de integridade tem de se ver.
//
// É o desfecho mais grave: o nó DEIXA de apagar dados fora do TTL e não volta sozinho. Até aqui
// existia só numa linha de log.
func TestParagemDefinitivaEDita(t *testing.T) {
	svc := &NodeService{retentionSweepInterval: time.Hour}
	h := &apiHandler{node: nodeComRetencaoArmada(t), svc: svc}

	if v, ok := valorDe(t, metricasDe(t, h), "aos_retention_scheduler_stopped"); !ok || v != 0 {
		t.Fatalf("stopped = %v (presente=%v) antes de qualquer incidente", v, ok)
	}
	svc.varredorParado.Store(true)
	if v, ok := valorDe(t, metricasDe(t, h), "aos_retention_scheduler_stopped"); !ok || v != 1 {
		t.Errorf("stopped = %v apos a paragem definitiva, queria 1 — sem isto o incidente vive so no log", v)
	}
}

// nodeComRetencaoArmada devolve um nó que satisfaz [retentionSchedulerArmed].
//
// Compõe a conjunção completa à mão, no mesmo molde do `base()` de
// `aos267_retention_scheduler_test.go`. A alternativa — `Bootstrap` com a config base — NÃO arma
// o escalonador, e a primeira versão deste ficheiro fazia `t.Skip` nesse caso: dois testes verdes
// que não corriam. Um teste saltado não prova nada; é vacuosidade com outro nome.
func nodeComRetencaoArmada(t *testing.T) *Node {
	t.Helper()
	rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{
		audit.ClassDiagnostic: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{
		ExpirationJob: &audit.ExpirationJob{},
		DSARHolds:     &audit.LegalHold{},
		WORM:          audit.NewMemStore(),
		Retention:     rc,
		// holdsRestored espelha o antecedente que o bootstrap real estabelece: a re-hidratação
		// do legal hold correu. Sem ele o escalonador NÃO arma (fail-closed).
		holdsRestored: true,
	}
	// CONTROLO DO CENÁRIO: se isto não armar, os testes que dependem dele mediriam outra coisa.
	if !retentionSchedulerArmed(n, time.Hour) {
		t.Fatal("o cenario nao arma o escalonador — os testes seguintes mediriam outra coisa")
	}
	return n
}

// TestOVARREDORAlimentaAsMetricas é o teste de CABLAGEM, e apareceu por mutação.
//
// Os testes acima escrevem os campos à mão e saltam o varredor. Uma mutação que removesse o
// registo da passagem passava em todos eles: as métricas continuavam correctas e ninguém as
// alimentava. Em produção, `sweeps_total` ficaria eternamente a 0 e a idade nunca sairia — o
// operador leria «armado» e «nunca varreu», que é a assinatura exacta de um varredor morto.
//
// É a OITAVA vez que este padrão aparece no repositório.
func TestOVARREDORAlimentaAsMetricas(t *testing.T) {
	node := noRealComRetencao(t)
	svc := newScheduledRetentionService(t, node, time.Hour) // armado, mas nao tica durante o teste.
	h := &apiHandler{node: node, svc: svc}

	// ANTES: contador a zero e sem idade.
	corpo := metricasDe(t, h)
	// EXIGE PRESENCA, e nao so valor: a primeira versao deste teste passava com a serie AUSENTE,
	// porque `valorDe` devolve (0,false) e eu so comparava o valor. Um teste que nao distingue
	// «zero» de «nao existe» da verde sobre uma metrica que nunca sai.
	if v, ok := valorDe(t, corpo, "aos_retention_sweeps_total"); !ok || v != 0 {
		t.Fatalf("sweeps_total = %v (presente=%v) antes de qualquer passagem", v, ok)
	}
	if _, ok := valorDe(t, corpo, "aos_retention_last_sweep_age_seconds"); ok {
		t.Fatal("a idade saiu antes de qualquer passagem — o cenario nao esta montado")
	}

	// UMA passagem REAL, pela mesma via que o scheduler usa.
	if !svc.SweepRetentionNow(t.Context()) {
		t.Fatal("a passagem devia concluir")
	}

	corpo = metricasDe(t, h)
	if v, ok := valorDe(t, corpo, "aos_retention_sweeps_total"); !ok || v != 1 {
		t.Errorf("sweeps_total = %v (presente=%v) DEPOIS de uma passagem real — a metrica esta "+
			"certa e o varredor nao a alimenta", v, ok)
	}
	idade, ok := valorDe(t, corpo, "aos_retention_last_sweep_age_seconds")
	if !ok {
		t.Fatal("a idade nao saiu depois de uma passagem REAL")
	}
	if idade > 60 {
		t.Errorf("idade = %.0f s logo apos a passagem — o instante nao foi registado por ela", idade)
	}
}

// noRealComRetencao devolve um nó COMPOSTO PELO BOOTSTRAP com política de retenção.
//
// O nó construído à mão de [nodeComRetencaoArmada] serve para medir o PREDICADO, mas não tem
// Event Store — e o `NewNodeService` exige-o. Para exercitar o VARREDOR é preciso o nó real.
func noRealComRetencao(t *testing.T) *Node {
	t.Helper()
	rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{
		audit.ClassDiagnostic: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := tnBaseConfig()
	cfg.Retention = rc
	n, err := Bootstrap(t.Context(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	// CONTROLO DO CENÁRIO: sem isto, uma passagem que não conta nada passaria por «varredor
	// alimentou as métricas» só porque o predicado é falso e as séries nem saem.
	if !retentionSchedulerArmed(n, time.Hour) {
		t.Fatal("o no REAL nao armou o escalonador — o teste de cablagem mediria outra coisa")
	}
	return n
}
