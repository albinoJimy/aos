package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos481_fiabilidade_real_test.go — AOS-481 e a emenda de 2026-10-18 ao ADR-025 (decisão do
// dono, opção b). A fonte de fiabilidade da promoção recebe desfechos e decisões REAIS: gravados
// pelo Reference Monitor nos sinks do Event Store e entregues pela MESMA subscrição que o nó abre
// ([fiabilidadeAgregada.correr]), e não eventos sintetizados à mão como em aos090_promocao_test.go.
// Com o AOS-481 o desfecho passou a chegar ao stream e a promoção ficou viva; a revisão
// independente provou que ela promovia indevidamente, e estes testes reproduzem as provas.

// aos481Plano é o que a política faz a uma call: permite, escala (e aprova na retoma) ou nega.
type aos481Plano int

const (
	aos481Permite aos481Plano = iota
	aos481Escala
	aos481Nega
	aos481Rebenta // permite, e a tool falha na execução
)

// aos481Politica decide pelo passo da call; a escalada só se levanta com evidência de aprovação.
type aos481Politica struct{ planos map[string]aos481Plano }

func (aos481Politica) Name() string { return "policy" }

func (p aos481Politica) Evaluate(_ context.Context, c *referencemonitor.Call) (referencemonitor.HookResult, error) {
	switch p.planos[c.StepID] {
	case aos481Escala:
		if len(c.ApprovalEvidence) == 0 {
			return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "aos481"}, nil
		}
	case aos481Nega:
		return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "aos481"}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// aos481SinkComBuraco perde o desfecho dos passos marcados — uma falha de escrita fail-open do
// desfecho, a lacuna de medição do ADR-025 §1.
type aos481SinkComBuraco struct {
	dentro referencemonitor.OutcomeSink
	perde  map[string]bool
}

func (s aos481SinkComBuraco) RecordOutcome(ctx context.Context, rec referencemonitor.OutcomeRecord) error {
	if s.perde[rec.StepID] {
		return errors.New("aos481: desfecho perdido")
	}
	return s.dentro.RecordOutcome(ctx, rec)
}

// aos481Banco é o RM real, o Event Store real e a fonte de fiabilidade a correr pela sua
// subscrição de produção, com um relógio manual (atómico: a subscrição lê-o noutra goroutine).
// A janela (24 h) é maior do que [prazoDoDesfecho], para um buraco se ver dentro dela.
type aos481Banco struct {
	t       *testing.T
	es      eventstore.EventStore
	agg     *fiabilidadeAgregada
	rm      *referencemonitor.Monitor
	planos  map[string]aos481Plano
	relogio atomic.Int64
	janela  time.Duration
	run     string
	n       int
}

const aos481Classe = "worker"

func novoAos481Banco(t *testing.T, configurados []autonomyLevelSpec, perde map[string]bool) *aos481Banco {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	b := &aos481Banco{t: t, es: es, planos: map[string]aos481Plano{}, janela: 24 * time.Hour, run: "run-aos481"}
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	b.relogio.Store(base.UnixNano())
	b.agg = novaFiabilidade(es, func() time.Time { return time.Unix(0, b.relogio.Load()).UTC() }, b.janela, time.Hour)
	b.agg.comParesConfigurados(configurados)
	// A observação já cobre a janela (o WindowOK honesto exige-o): só falta o que chega pelo stream.
	b.avancar(b.janela + time.Minute)

	// A subscrição de produção: [fiabilidadeAgregada.correr], com a cadência de 1h (o teste avalia
	// à mão, com o relógio parado). `subscrita` diz quando a subscrição existe.
	b.agg.subscrita = make(chan struct{})
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go b.agg.correr(stop)
	select {
	case <-b.agg.subscrita:
	case <-time.After(10 * time.Second):
		t.Fatal("a subscricao de correr nao ficou activa")
	}

	var desfechos referencemonitor.OutcomeSink = referencemonitor.NewEventStoreOutcomeSink(es)
	if perde != nil {
		desfechos = aos481SinkComBuraco{dentro: desfechos, perde: perde}
	}
	// O RM com os dois canais no Event Store, como o nó os compõe (integration/secured.go).
	b.rm = referencemonitor.New(
		referencemonitor.WithHooks(aos481Politica{planos: b.planos}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
		referencemonitor.WithOutcomeSink(desfechos),
	)
	if err := b.rm.Register("fs.write", func(_ context.Context, in []byte) ([]byte, error) {
		if string(in) == "rebenta" {
			return nil, errors.New("falha de execucao")
		}
		return []byte("ok"), nil
	}); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *aos481Banco) avancar(d time.Duration) { b.relogio.Add(int64(d)) }

// passo devolve o step_id da próxima call (da classe dada).
func (b *aos481Banco) passo() string {
	b.n++
	return fmt.Sprintf("step-%06d-tool-1", b.n)
}

// chamar faz `n` calls da classe com o plano dado, pelo RM. Uma escalada é aprovada e retomada no
// MESMO passo. Devolve os passos.
func (b *aos481Banco) chamar(classe string, n int, plano aos481Plano) []string {
	b.t.Helper()
	ctx := context.Background()
	var passos []string
	for i := 0; i < n; i++ {
		passo := b.passo()
		b.planos[passo] = plano
		in := []byte("ok")
		if plano == aos481Rebenta {
			in = []byte("rebenta")
		}
		c := referencemonitor.Call{
			RunID: b.run, StepID: passo, ParentStepID: passo[:len(passo)-len("-tool-1")],
			ToolID: "fs.write", Capability: "cap:fs.write",
			Resource:  referencemonitor.Resource{Type: "path", Value: "/x"},
			Principal: referencemonitor.Principal{NHIID: "nhi-1", AgentID: "agt-1", AgentClass: classe},
			Input:     in,
		}
		quer := referencemonitor.EffectPermit
		switch plano {
		case aos481Escala:
			if d, err := b.rm.Mediate(ctx, c); err != nil || d.Effect != referencemonitor.EffectEscalate {
				b.t.Fatalf("escalada esperada em %s: eff=%q err=%v", passo, d.Effect, err)
			}
			c.ApprovalEvidence = []byte("grant")
		case aos481Nega:
			quer = referencemonitor.EffectDeny
		}
		if d, err := b.rm.Mediate(ctx, c); err != nil || d.Effect != quer {
			b.t.Fatalf("%s esperado em %s: eff=%q err=%v", quer, passo, d.Effect, err)
		}
		passos = append(passos, passo)
	}
	b.esperarEntrega()
	return passos
}

// esperarEntrega espera que a subscrição tenha entregue tudo o que o stream do run tem.
func (b *aos481Banco) esperarEntrega() {
	b.t.Helper()
	evs, err := b.es.Read(context.Background(), b.run, 1)
	if err != nil {
		b.t.Fatal(err)
	}
	var ultimo uint64
	for _, ev := range evs {
		ultimo = ev.Seq
	}
	prazo := time.Now().Add(10 * time.Second)
	for {
		b.agg.mu.Lock()
		visto := b.agg.ultimoSeq[b.run]
		b.agg.mu.Unlock()
		if visto >= ultimo {
			return
		}
		if time.Now().After(prazo) {
			b.t.Fatalf("a subscricao entregou ate ao seq %d de %d", visto, ultimo)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// contagens devolve desfechos (e erros) e decisões (e escaladas) da classe no domínio fs.
func (b *aos481Banco) contagens(classe string) (desf, erros, dec, esc int) {
	b.agg.mu.Lock()
	defer b.agg.mu.Unlock()
	j := b.agg.porChave[classeDominio{classe: classe, dominio: "fs"}]
	if j == nil {
		return
	}
	for _, a := range j.desfechos {
		desf++
		if a.erro {
			erros++
		}
	}
	for _, a := range j.decisoes {
		dec++
		if a.escala {
			esc++
		}
	}
	return
}

// promover liga um controlador com a classe no nível dado e corre o Evaluate do nó
// ([fiabilidadeAgregada.avaliarPromocoes]); devolve o nível final.
func (b *aos481Banco) promover(nivel autonomy.Level) autonomy.Level {
	b.t.Helper()
	ctrl, reg := controladorComFonte(b.t, b.agg, b.janela, nivel)
	b.agg.comControlador(ctrl)
	b.agg.avaliarPromocoes(context.Background())
	return reg.LevelFor(autonomy.ClassPrefix+aos481Classe, "fs")
}

func (b *aos481Banco) fiab(classe string) autonomy.Reliability {
	return b.agg.Reliability(autonomy.ClassPrefix+classe, "fs", b.janela)
}

// TestAOS481_FiabilidadeRecebeDesfechosReaisPeloRMEEventStore — o caso que promove
// legitimamente: 60 calls (1 com erro de execução), 2 delas escaladas e aprovadas. Chegam 60
// desfechos e 60 decisões (UMA por call: o permit que segue a escalada não conta outra vez); a
// taxa de override é 2/60 e a classe sobe L1 → L2. Antes do AOS-481: 0 desfechos e
// WindowOK=false. Mutação: tirar `EventTypeOutcome` do filtro de [fiabilidadeAgregada.correr]
// deixa 0 desfechos e este teste avermelha.
func TestAOS481_FiabilidadeRecebeDesfechosReaisPeloRMEEventStore(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, nil)
	b.chamar(aos481Classe, 1, aos481Rebenta)
	b.chamar(aos481Classe, 2, aos481Escala)
	b.chamar(aos481Classe, 57, aos481Permite)

	desf, erros, dec, esc := b.contagens(aos481Classe)
	if desf != 60 || erros != 1 {
		t.Fatalf("desfechos observados %d (erros %d), quer 60 (erros 1): o tool.call.outcome nao chegou pela subscricao", desf, erros)
	}
	if dec != 60 || esc != 2 {
		t.Fatalf("decisoes observadas %d (escaladas %d), quer 60 (2): uma por call", dec, esc)
	}
	rel := b.fiab(aos481Classe)
	if !rel.WindowOK || rel.ErrorRate != 1.0/60 || rel.OverrideRate != 2.0/60 {
		t.Fatalf("fiabilidade %+v, quer WindowOK, erro 1/60, override 2/60", rel)
	}
	if got := b.promover(autonomy.L1); got != autonomy.L2 {
		t.Fatalf("nivel depois da promocao %s, quer L2", got)
	}
}

// TestAOS481_P1_MaioriaEscaladaNaoPromove — prova P1 da revisão: 20 calls, 12 escaladas e
// aprovadas, sem erros, em L2. A fracção das calls que precisou de humano é 60 % > 40 %: fica em
// L2. Contando o permit pós-aprovação como decisão, lia-se 12/32 = 37,5 % e promovia.
func TestAOS481_P1_MaioriaEscaladaNaoPromove(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, nil)
	b.chamar(aos481Classe, 12, aos481Escala)
	b.chamar(aos481Classe, 8, aos481Permite)
	if rel := b.fiab(aos481Classe); !rel.WindowOK || rel.OverrideRate != 0.6 {
		t.Fatalf("fiabilidade %+v, quer WindowOK e override 12/20", rel)
	}
	if got := b.promover(autonomy.L2); got != autonomy.L2 {
		t.Fatalf("60%% das calls escaladas promoveu para %s; quer ficar em L2", got)
	}
}

// TestAOS481_P2_TudoEscaladoComRecusasNaoPromove — prova P2 da revisão: classe em L0, 20 calls
// todas escaladas e aprovadas, mais 10 recusas pela política. As recusas não entram no
// denominador e cada escalada conta uma vez: override 20/20, fica em L0.
func TestAOS481_P2_TudoEscaladoComRecusasNaoPromove(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, nil)
	b.chamar(aos481Classe, 20, aos481Escala)
	b.chamar(aos481Classe, 10, aos481Nega)
	if _, _, dec, esc := b.contagens(aos481Classe); dec != 20 || esc != 20 {
		t.Fatalf("decisoes %d (escaladas %d), quer 20 (20): recusas fora, uma por call", dec, esc)
	}
	if got := b.promover(autonomy.L0); got != autonomy.L0 {
		t.Fatalf("tudo escalado promoveu para %s; quer ficar em L0", got)
	}
}

// TestAOS481_RecusasGarantidasNaoBaixamAOverride — a decisão do AOS-485 que ficava com o dono:
// metade das calls escaladas (50 % > 40 %) e 20 recusas pela lista-branca do run ao lado. Com as
// recusas no denominador lia-se 10/40 = 25 % e promovia.
func TestAOS481_RecusasGarantidasNaoBaixamAOverride(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, nil)
	b.chamar(aos481Classe, 10, aos481Escala)
	b.chamar(aos481Classe, 10, aos481Permite)
	b.chamar(aos481Classe, 20, aos481Nega)
	if rel := b.fiab(aos481Classe); rel.OverrideRate != 0.5 {
		t.Fatalf("override %v, quer 0.5 (recusas fora do denominador)", rel.OverrideRate)
	}
	if got := b.promover(autonomy.L1); got != autonomy.L1 {
		t.Fatalf("recusas garantidas promoveram para %s; quer ficar em L1", got)
	}
}

// TestAOS481_SoClassesConfiguradasPromovem — uma classe observada mas não provisionada em
// AOS_AUTONOMY_LEVELS, com fiabilidade perfeita, nunca é promovida e fica no piso; a configurada,
// no mesmo nó, promove.
func TestAOS481_SoClassesConfiguradasPromovem(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, nil)
	b.chamar(aos481Classe, 30, aos481Permite)
	b.chamar("intrusa", 30, aos481Permite)
	if rel := b.fiab("intrusa"); rel.WindowOK {
		t.Fatalf("classe nao configurada julgada: %+v", rel)
	}
	ctrl, reg := controladorComFonte(t, b.agg, b.janela, autonomy.L1)
	piso := reg.LevelFor(autonomy.ClassPrefix+"intrusa", "fs")
	b.agg.comControlador(ctrl)
	b.agg.avaliarPromocoes(context.Background())
	if got := reg.LevelFor(autonomy.ClassPrefix+"intrusa", "fs"); got != piso {
		t.Fatalf("classe nao configurada promovida de %s para %s", piso, got)
	}
	if got := reg.LevelFor(autonomy.ClassPrefix+aos481Classe, "fs"); got != autonomy.L2 {
		t.Fatalf("a classe configurada devia promover: %s", got)
	}
}

// TestAOS481_PermitSemDesfechoInvalidaAJanela — ADR-025 §1: um permit cujo desfecho não chega
// (a escrita do desfecho falhou, fail-open) torna a janela inválida depois de [prazoDoDesfecho];
// antes do prazo ainda é um desfecho a caminho.
func TestAOS481_PermitSemDesfechoInvalidaAJanela(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, map[string]bool{"step-000007-tool-1": true})
	b.chamar(aos481Classe, 30, aos481Permite)
	if desf, _, dec, _ := b.contagens(aos481Classe); desf != 29 || dec != 30 {
		t.Fatalf("desfechos %d e decisoes %d, quer 29 e 30 (um desfecho perdido)", desf, dec)
	}
	if rel := b.fiab(aos481Classe); !rel.WindowOK {
		t.Fatalf("dentro do prazo o desfecho ainda pode chegar: %+v", rel)
	}
	b.avancar(prazoDoDesfecho + time.Minute)
	if rel := b.fiab(aos481Classe); rel.WindowOK {
		t.Fatalf("permit sem desfecho ha mais de %s e a janela continua julgavel: %+v", prazoDoDesfecho, rel)
	}
	if got := b.promover(autonomy.L1); got != autonomy.L1 {
		t.Fatalf("janela com buraco promoveu para %s", got)
	}
}

// TestAOS481_ReentregaNaoContaADobrar — o transporte (JetStream) pode voltar a entregar um evento
// já entregue: o mesmo stream e o mesmo seq não contam outra vez.
func TestAOS481_ReentregaNaoContaADobrar(t *testing.T) {
	b := novoAos481Banco(t, workerFsConfigurado, nil)
	b.chamar(aos481Classe, 5, aos481Escala)
	b.chamar(aos481Classe, 20, aos481Permite)
	d0, _, c0, e0 := b.contagens(aos481Classe)
	evs, err := b.es.Read(context.Background(), b.run, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs { // a re-entrega de todo o stream
		b.agg.observar(ev)
	}
	if d, _, c, e := b.contagens(aos481Classe); d != d0 || c != c0 || e != e0 {
		t.Fatalf("a re-entrega contou a dobrar: desfechos %d→%d, decisoes %d→%d, escaladas %d→%d", d0, d, c0, c, e0, e)
	}
}
