package main

import (
	"context"
	"encoding/json"
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
// Com o AOS-481 o desfecho passou a chegar ao stream e a promoção ficou viva; as revisões
// independentes provaram caminhos de promoção indevida, e estes testes reproduzem as provas.

// aos481Plano é o que a política faz a uma call: permite, escala (aprovada na retoma) ou nega.
type aos481Plano int

const (
	aos481Permite aos481Plano = iota
	aos481Escala
	aos481Nega
	aos481Rebenta // permite, e a tool falha na execução
)

// aos481Politica decide pelo passo da call; uma escalada só se levanta com a aprovação humana
// VERIFICADA pelo ApprovalGate real (a mesma prova que o nó usa).
type aos481Politica struct{ planos map[string]aos481Plano }

func (aos481Politica) Name() string { return "policy" }

func (p aos481Politica) Evaluate(_ context.Context, c *referencemonitor.Call) (referencemonitor.HookResult, error) {
	switch p.planos[c.StepID] {
	case aos481Escala:
		if c.HumanApproval() == nil {
			return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "aos481"}, nil
		}
	case aos481Nega:
		return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "aos481"}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// aos481Verificador aceita a evidência "grant" (o four-eyes do nó, reduzido ao que o teste usa).
type aos481Verificador struct{}

func (aos481Verificador) VerifyApproval(_ context.Context, ev, _ []byte) (referencemonitor.ApprovalProof, error) {
	if string(ev) != "grant" {
		return referencemonitor.ApprovalProof{}, errors.New("aos481: evidencia invalida")
	}
	return referencemonitor.ApprovalProof{Approvers: []string{"human:ana", "human:rui"}, DualControl: true}, nil
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

// aos481SinkSemEscaladas falha a escrita de toda a escalada: o registo best-effort de
// [referencemonitor.Monitor] perde-a, e o log do run não a tem.
type aos481SinkSemEscaladas struct{ dentro referencemonitor.EventSink }

func (s aos481SinkSemEscaladas) RecordMediation(ctx context.Context, rec referencemonitor.MediationRecord) (uint64, error) {
	if rec.Effect == referencemonitor.EffectEscalate {
		return 0, errors.New("aos481: registo da escalada falhou")
	}
	return s.dentro.RecordMediation(ctx, rec)
}

type aos481Opcoes struct {
	configurados   []autonomyLevelSpec // nil ⇒ workerFsConfigurado
	nivel          autonomy.Level      // o nível inicial de class:worker:fs
	janela         time.Duration       // zero ⇒ 24 h (maior do que o prazo do desfecho)
	perde          map[string]bool     // passos cujo desfecho se perde
	semEscaladas   bool                // o registo das escaladas falha
	arrancarDepois bool                // a subscrição só arranca com [aos481Banco.ligar]
}

// aos481Banco é o RM real, o Event Store real, o controlador com o registo de níveis, e a fonte
// de fiabilidade a correr pela sua subscrição de produção, com um relógio manual (atómico: a
// subscrição lê-o noutra goroutine).
type aos481Banco struct {
	t       *testing.T
	es      eventstore.EventStore
	agg     *fiabilidadeAgregada
	rm      *referencemonitor.Monitor
	ctrl    *autonomy.Controller
	reg     *autonomy.LevelRegistry
	planos  map[string]aos481Plano
	relogio atomic.Int64
	janela  time.Duration
	run     string
	n       int
	ligada  bool
}

const aos481Classe = "worker"

func novoAos481Banco(t *testing.T, o aos481Opcoes) *aos481Banco {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	if o.janela == 0 {
		o.janela = 24 * time.Hour
	}
	if o.configurados == nil {
		o.configurados = workerFsConfigurado
	}
	b := &aos481Banco{t: t, es: es, planos: map[string]aos481Plano{}, janela: o.janela, run: "run-aos481"}
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	b.relogio.Store(base.UnixNano())
	b.agg = novaFiabilidade(es, func() time.Time { return time.Unix(0, b.relogio.Load()).UTC() }, b.janela, time.Hour)
	b.agg.comParesConfigurados(o.configurados)
	// O controlador do nó, com a classe no nível inicial, e a leitura do nível ligada à fonte.
	b.ctrl, b.reg = controladorComFonte(t, b.agg, b.janela, o.nivel)
	b.agg.comControlador(b.ctrl, b.reg.LevelFor)
	// A observação já cobre a janela (o WindowOK honesto exige-o): só falta o que chega pelo stream.
	b.avancar(b.janela + time.Minute)

	var mediacoes referencemonitor.EventSink = referencemonitor.NewEventStoreSink(es)
	if o.semEscaladas {
		mediacoes = aos481SinkSemEscaladas{dentro: mediacoes}
	}
	var desfechos referencemonitor.OutcomeSink = referencemonitor.NewEventStoreOutcomeSink(es)
	if o.perde != nil {
		desfechos = aos481SinkComBuraco{dentro: desfechos, perde: o.perde}
	}
	// O RM com os dois canais no Event Store, como o nó os compõe (integration/secured.go), e o
	// ApprovalGate real antes da política.
	b.rm = referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.NewApprovalGate(aos481Verificador{}), aos481Politica{planos: b.planos}),
		referencemonitor.WithEventSink(mediacoes),
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
	if !o.arrancarDepois {
		b.ligar()
	}
	return b
}

// ligar arranca a subscrição de produção: [fiabilidadeAgregada.correr], com a cadência de 1 h (o
// teste avalia à mão, com o relógio parado). `subscrita` diz quando a subscrição existe.
func (b *aos481Banco) ligar() {
	b.t.Helper()
	b.agg.subscrita = make(chan struct{})
	stop := make(chan struct{})
	b.t.Cleanup(func() { close(stop) })
	go b.agg.correr(stop)
	select {
	case <-b.agg.subscrita:
	case <-time.After(10 * time.Second):
		b.t.Fatal("a subscricao de correr nao ficou activa")
	}
	b.ligada = true
}

func (b *aos481Banco) avancar(d time.Duration) { b.relogio.Add(int64(d)) }

func (b *aos481Banco) call(classe, passo string, plano aos481Plano) referencemonitor.Call {
	in := []byte("ok")
	if plano == aos481Rebenta {
		in = []byte("rebenta")
	}
	return referencemonitor.Call{
		RunID: b.run, StepID: passo, ParentStepID: passo[:len(passo)-len("-tool-1")],
		ToolID: "fs.write", Capability: "cap:fs.write",
		Resource:  referencemonitor.Resource{Type: "path", Value: "/x"},
		Principal: referencemonitor.Principal{NHIID: "nhi-1", AgentID: "agt-1", AgentClass: classe},
		Input:     in,
	}
}

func (b *aos481Banco) mediar(c referencemonitor.Call, quer referencemonitor.Effect) {
	b.t.Helper()
	if d, err := b.rm.Mediate(context.Background(), c); err != nil || d.Effect != quer {
		b.t.Fatalf("%s esperado em %s: eff=%q err=%v", quer, c.StepID, d.Effect, err)
	}
}

// escalar faz `n` calls da classe que escalam, SEM as aprovar; devolve os passos.
func (b *aos481Banco) escalar(classe string, n int) []string {
	b.t.Helper()
	var passos []string
	for i := 0; i < n; i++ {
		b.n++
		passo := fmt.Sprintf("step-%06d-tool-1", b.n)
		b.planos[passo] = aos481Escala
		b.mediar(b.call(classe, passo, aos481Escala), referencemonitor.EffectEscalate)
		passos = append(passos, passo)
	}
	return passos
}

// aprovar retoma, com a aprovação, as calls escaladas nos passos dados.
func (b *aos481Banco) aprovar(classe string, passos []string) {
	b.t.Helper()
	for _, passo := range passos {
		c := b.call(classe, passo, aos481Escala)
		c.ApprovalEvidence = []byte("grant")
		b.mediar(c, referencemonitor.EffectPermit)
	}
	b.esperarEntrega()
}

// chamar faz `n` calls da classe com o plano dado, pelo RM. Uma escalada é aprovada e retomada no
// MESMO passo.
func (b *aos481Banco) chamar(classe string, n int, plano aos481Plano) {
	b.t.Helper()
	if plano == aos481Escala {
		b.aprovar(classe, b.escalar(classe, n))
		return
	}
	for i := 0; i < n; i++ {
		b.n++
		passo := fmt.Sprintf("step-%06d-tool-1", b.n)
		b.planos[passo] = plano
		quer := referencemonitor.EffectPermit
		if plano == aos481Nega {
			quer = referencemonitor.EffectDeny
		}
		b.mediar(b.call(classe, passo, plano), quer)
	}
	b.esperarEntrega()
}

// esperarEntrega espera que a subscrição tenha entregue tudo o que o stream do run tem.
func (b *aos481Banco) esperarEntrega() {
	b.t.Helper()
	if !b.ligada {
		return
	}
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
		var visto uint64
		if v := b.agg.ultimoSeq[b.run]; v != nil {
			visto = v.seq
		}
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

// promover corre o Evaluate do nó ([fiabilidadeAgregada.avaliarPromocoes]) e devolve o nível.
func (b *aos481Banco) promover() autonomy.Level {
	b.t.Helper()
	b.agg.avaliarPromocoes(context.Background())
	return b.nivel()
}

func (b *aos481Banco) nivel() autonomy.Level {
	return b.reg.LevelFor(autonomy.ClassPrefix+aos481Classe, "fs")
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
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
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
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("nivel depois da promocao %s, quer L2", got)
	}
}

// TestAOS481_P1_MaioriaEscaladaNaoPromove — prova P1 da revisão: 20 calls, 12 escaladas e
// aprovadas, sem erros, em L2. A fracção das calls que precisou de humano é 60 % > 40 %: fica em
// L2. Contando o permit pós-aprovação como decisão, lia-se 12/32 = 37,5 % e promovia.
func TestAOS481_P1_MaioriaEscaladaNaoPromove(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L2})
	b.chamar(aos481Classe, 12, aos481Escala)
	b.chamar(aos481Classe, 8, aos481Permite)
	if rel := b.fiab(aos481Classe); !rel.WindowOK || rel.OverrideRate != 0.6 {
		t.Fatalf("fiabilidade %+v, quer WindowOK e override 12/20", rel)
	}
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("60%% das calls escaladas promoveu para %s; quer ficar em L2", got)
	}
}

// TestAOS481_P2_TudoEscaladoComRecusasNaoPromove — prova P2 da revisão: classe em L0, 20 calls
// todas escaladas e aprovadas, mais 10 recusas pela política. As recusas não entram no
// denominador e cada escalada conta uma vez: override 20/20, fica em L0.
func TestAOS481_P2_TudoEscaladoComRecusasNaoPromove(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L0})
	b.chamar(aos481Classe, 20, aos481Escala)
	b.chamar(aos481Classe, 10, aos481Nega)
	if _, _, dec, esc := b.contagens(aos481Classe); dec != 20 || esc != 20 {
		t.Fatalf("decisoes %d (escaladas %d), quer 20 (20): recusas fora, uma por call", dec, esc)
	}
	if got := b.promover(); got != autonomy.L0 {
		t.Fatalf("tudo escalado promoveu para %s; quer ficar em L0", got)
	}
}

// TestAOS481_RecusasGarantidasNaoBaixamAOverride — a decisão do AOS-485 que ficava com o dono:
// metade das calls escaladas (50 % > 40 %) e 20 recusas ao lado. Com as recusas no denominador
// lia-se 10/40 = 25 % e promovia.
func TestAOS481_RecusasGarantidasNaoBaixamAOverride(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
	b.chamar(aos481Classe, 10, aos481Escala)
	b.chamar(aos481Classe, 10, aos481Permite)
	b.chamar(aos481Classe, 20, aos481Nega)
	if rel := b.fiab(aos481Classe); rel.OverrideRate != 0.5 {
		t.Fatalf("override %v, quer 0.5 (recusas fora do denominador)", rel.OverrideRate)
	}
	if got := b.promover(); got != autonomy.L1 {
		t.Fatalf("recusas garantidas promoveram para %s; quer ficar em L1", got)
	}
}

// TestAOS481_EscaladaAntesDoArranqueContaComoHumano — caso A da 2.ª revisão: as escaladas foram
// gravadas antes de este processo subscrever (pendentes num reinício) e aprovadas depois. O
// processo só vê os permits; o selo de cada um diz `human_approved` (a prova verificada pelo
// ApprovalGate), e é por aí que contam como calls que precisaram de humano: 12/20, fica em L2.
func TestAOS481_EscaladaAntesDoArranqueContaComoHumano(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L2, arrancarDepois: true})
	pendentes := b.escalar(aos481Classe, 12)
	b.ligar()
	b.aprovar(aos481Classe, pendentes)
	b.chamar(aos481Classe, 8, aos481Permite)
	if _, _, dec, esc := b.contagens(aos481Classe); dec != 20 || esc != 12 {
		t.Fatalf("decisoes %d (precisaram de humano %d), quer 20 (12)", dec, esc)
	}
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("aprovacoes sem a escalada vista promoveram para %s; quer L2", got)
	}
}

// TestAOS481_EscaladaNaoRegistadaContaComoHumano — caso B: o registo best-effort das escaladas
// falhou (nenhuma está no stream). O permit aprovado diz `human_approved`: 12/20, fica em L2.
func TestAOS481_EscaladaNaoRegistadaContaComoHumano(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L2, semEscaladas: true})
	b.chamar(aos481Classe, 12, aos481Escala)
	b.chamar(aos481Classe, 8, aos481Permite)
	evs, err := b.es.Read(context.Background(), b.run, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == referencemonitor.EventTypeEscalated {
			t.Fatalf("o cenario exige as escaladas fora do stream: %s", ev.StepID)
		}
	}
	if _, _, dec, esc := b.contagens(aos481Classe); dec != 20 || esc != 12 {
		t.Fatalf("decisoes %d (precisaram de humano %d), quer 20 (12)", dec, esc)
	}
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("escaladas nao registadas promoveram para %s; quer L2", got)
	}
}

// aos481HistoriaBoa: 30 calls directas, sem erros — basta para promover.
func aos481HistoriaBoa(b *aos481Banco) { b.chamar(aos481Classe, 30, aos481Permite) }

// TestAOS481_DemocaoRecomecaAJanela — L3, história boa, demoção por anomalia (o caminho do
// disjuntor, [autonomy.Controller.OnAnomaly]) para L1. Uma hora depois o controlador NÃO a
// desfaz com as amostras tiradas em L3: fica em L1.
func TestAOS481_DemocaoRecomecaAJanela(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L3})
	aos481HistoriaBoa(b)
	if _, mudou, err := b.ctrl.OnAnomaly(context.Background(), autonomy.ClassPrefix+aos481Classe, "fs", autonomy.AnomalyUnsafeAction); err != nil || !mudou {
		t.Fatalf("OnAnomaly: mudou=%v err=%v", mudou, err)
	}
	if b.nivel() != autonomy.L1 {
		t.Fatalf("a democao devia dar L1: %s", b.nivel())
	}
	b.avancar(time.Hour)
	if got := b.promover(); got != autonomy.L1 {
		t.Fatalf("a democao foi desfeita uma hora depois com a historia de L3: %s", got)
	}
}

// TestAOS481_OperadorBaixaRecomecaAJanela — o operador põe L0 (POST /autonomy grava um SetLevel);
// um minuto depois continua L0, e não L1 com a história de antes.
func TestAOS481_OperadorBaixaRecomecaAJanela(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
	aos481HistoriaBoa(b)
	if _, err := b.reg.SetLevel(context.Background(), autonomy.ClassPrefix+aos481Classe, "fs", autonomy.L0, "operador baixa", "gov-admin"); err != nil {
		t.Fatal(err)
	}
	b.avancar(time.Minute)
	if got := b.promover(); got != autonomy.L0 {
		t.Fatalf("a decisao do operador foi desfeita um minuto depois: %s", got)
	}
}

// TestAOS481_OperadorSobeRecomecaAJanela — o operador sobe L1→L2; um minuto depois continua L2, e
// não L3 com a história tirada em L1.
func TestAOS481_OperadorSobeRecomecaAJanela(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
	aos481HistoriaBoa(b)
	if _, err := b.reg.SetLevel(context.Background(), autonomy.ClassPrefix+aos481Classe, "fs", autonomy.L2, "operador sobe", "gov-admin"); err != nil {
		t.Fatal(err)
	}
	b.avancar(time.Minute)
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("subiu mais um degrau com a historia de L1: %s", got)
	}
}

// TestAOS481_PromocaoExigeJanelaInteiraNoNivelNovo — promove L1→L2; 30 calls boas já em L2 e uma
// hora depois (janela de 24 h): não há segundo degrau, porque a janela do nível novo não está
// coberta. Mata a mutação que ignora o início da janela da chave.
func TestAOS481_PromocaoExigeJanelaInteiraNoNivelNovo(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
	aos481HistoriaBoa(b)
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("1.a promocao: %s, quer L2", got)
	}
	aos481HistoriaBoa(b)
	b.avancar(time.Hour)
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("segundo degrau uma hora depois da promocao: %s, quer L2", got)
	}
}

// TestAOS481_SoClassesConfiguradasPromovem — uma classe observada mas não provisionada em
// AOS_AUTONOMY_LEVELS, com fiabilidade perfeita, nunca é promovida e fica no piso; a configurada,
// no mesmo nó, promove.
func TestAOS481_SoClassesConfiguradasPromovem(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
	piso := b.reg.LevelFor(autonomy.ClassPrefix+"intrusa", "fs")
	b.chamar(aos481Classe, 30, aos481Permite)
	b.chamar("intrusa", 30, aos481Permite)
	if rel := b.fiab("intrusa"); rel.WindowOK {
		t.Fatalf("classe nao configurada julgada: %+v", rel)
	}
	if got := b.promover(); got != autonomy.L2 {
		t.Fatalf("a classe configurada devia promover: %s", got)
	}
	if got := b.reg.LevelFor(autonomy.ClassPrefix+"intrusa", "fs"); got != piso {
		t.Fatalf("classe nao configurada promovida de %s para %s", piso, got)
	}
}

// TestAOS481_EntradaDeInstanciaNaoConfiguraAClasse — `worker:fs=L2` em AOS_AUTONOMY_LEVELS é uma
// INSTÂNCIA chamada `worker`, não a classe: a classe `worker` não fica configurada e não promove.
func TestAOS481_EntradaDeInstanciaNaoConfiguraAClasse(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1, configurados: []autonomyLevelSpec{{agent: aos481Classe, domain: "fs", level: autonomy.L2}}})
	aos481HistoriaBoa(b)
	if got := b.promover(); got != autonomy.L1 {
		t.Fatalf("uma entrada de instancia configurou a classe: %s", got)
	}
}

// TestAOS481_PermitSemDesfechoInvalidaAJanela — ADR-025 §1: um permit cujo desfecho não chega
// (a escrita do desfecho falhou, fail-open) torna a janela inválida depois de [prazoDoDesfecho];
// antes do prazo ainda é um desfecho a caminho.
func TestAOS481_PermitSemDesfechoInvalidaAJanela(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1, perde: map[string]bool{"step-000007-tool-1": true}})
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
	if got := b.promover(); got != autonomy.L1 {
		t.Fatalf("janela com buraco promoveu para %s", got)
	}
}

// TestAOS481_PrazoDoDesfechoComJanelaCurta — com uma janela de 1 h o prazo é metade dela (30 min):
// um prazo de 1 h nunca se vencia dentro da janela e a regra dos buracos morria.
func TestAOS481_PrazoDoDesfechoComJanelaCurta(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1, janela: time.Hour, perde: map[string]bool{"step-000003-tool-1": true}})
	b.chamar(aos481Classe, 30, aos481Permite)
	b.avancar(31 * time.Minute)
	if rel := b.fiab(aos481Classe); rel.WindowOK {
		t.Fatalf("janela de 1h com um permit sem desfecho ha 31min continua julgavel: %+v", rel)
	}
}

// TestAOS481_DesfechoAntesDoPermitNaoEBuraco — o transporte pode entregar o desfecho antes do
// permit da mesma call. Eventos REAIS do stream, entregues a uma fonte nova com cada desfecho à
// frente do seu permit: depois do prazo, nenhum permit fica a contar como buraco.
func TestAOS481_DesfechoAntesDoPermitNaoEBuraco(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
	b.chamar(aos481Classe, 25, aos481Permite)
	evs, err := b.es.Read(context.Background(), b.run, 1)
	if err != nil {
		t.Fatal(err)
	}
	var trocados []eventstore.Event
	for i := 0; i < len(evs); i++ {
		if i+1 < len(evs) && evs[i].Type == referencemonitor.EventTypeMediated && evs[i+1].Type == referencemonitor.EventTypeOutcome {
			trocados = append(trocados, evs[i+1], evs[i])
			i++
			continue
		}
		trocados = append(trocados, evs[i])
	}
	var relogio atomic.Int64
	relogio.Store(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC).UnixNano())
	agg := novaFiabilidade(b.es, func() time.Time { return time.Unix(0, relogio.Load()).UTC() }, b.janela, time.Hour)
	agg.comParesConfigurados(workerFsConfigurado)
	relogio.Add(int64(b.janela + time.Minute))
	for _, ev := range trocados {
		agg.observar(ev)
	}
	relogio.Add(int64(prazoDoDesfecho + time.Minute))
	if rel := agg.Reliability(autonomy.ClassPrefix+aos481Classe, "fs", b.janela); !rel.WindowOK {
		t.Fatalf("desfechos entregues antes dos permits contaram como buracos: %+v", rel)
	}
}

// TestAOS481_ReentregaNaoContaADobrar — o transporte (JetStream) pode voltar a entregar um evento
// já entregue: o mesmo stream e o mesmo seq não contam outra vez.
func TestAOS481_ReentregaNaoContaADobrar(t *testing.T) {
	b := novoAos481Banco(t, aos481Opcoes{nivel: autonomy.L1})
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

// TestAOS481_PodaDosStreamsVistos — o mapa do último seq por stream (um stream por run) é podado
// pela idade da janela: 300 runs vistos há mais de uma janela saem quando chegam runs novos.
func TestAOS481_PodaDosStreamsVistos(t *testing.T) {
	var relogio atomic.Int64
	relogio.Store(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC).UnixNano())
	janela := time.Hour
	agg := novaFiabilidade(&esFake{}, func() time.Time { return time.Unix(0, relogio.Load()).UTC() }, janela, time.Hour)
	ev := func(run string) eventstore.Event {
		raw, _ := json.Marshal(map[string]any{"capability": "cap:fs.write", "principal": map[string]any{"agent_class": "worker"}})
		return eventstore.Event{StreamID: run, Seq: 1, StepID: "step-000001-tool-1", Type: referencemonitor.EventTypeMediated, Payload: raw}
	}
	for i := 0; i < 300; i++ {
		agg.observar(ev(fmt.Sprintf("run-velho-%d", i)))
	}
	relogio.Add(int64(janela + time.Minute))
	for i := 0; i < podaDasChamadas; i++ {
		agg.observar(ev(fmt.Sprintf("run-novo-%d", i)))
	}
	agg.mu.Lock()
	n := len(agg.ultimoSeq)
	agg.mu.Unlock()
	if n > podaDasChamadas {
		t.Fatalf("o mapa do ultimo seq tem %d streams; os 300 vistos ha mais de uma janela deviam ter saido", n)
	}
}
