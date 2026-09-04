package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/agent-runtime/breaker"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-090 / DEF-908 — A DEMOÇÃO AUTOMÁTICA POR ANOMALIA, DO TRIP AO SELO.
//
// O defeito que DEF-908 nomeia não era «falta código»: o [autonomy.Controller] estava
// completo e testado desde AOS-090. Era que NINGUÉM o chamava — a metade de segurança da
// autonomia existia como biblioteca. Estes testes cobrem a ligação que faltava e, sobretudo,
// as duas formas de ela regredir sem ninguém dar por isso: voltar a ficar sem chamador, e
// ficar ligada mas inerte.

// semearMediacoes escreve no WORM as mediações de um run, na partição que É o RunID (ver
// `audit/rmadapter.go:defaultPartition`). É a fonte de onde a demoção deriva os pares.
func semearMediacoes(t *testing.T, store audit.Store, runID string, pares [][2]string) {
	t.Helper()
	ctx := context.Background()
	for _, p := range pares {
		rec := audit.AuditRecord{
			Partition:  runID,
			RunID:      runID,
			Timestamp:  time.Now().UTC(),
			Decision:   audit.DecisionAllow,
			ToolID:     "tool-x",
			Capability: p[1],
			Principal:  audit.Principal{NHIID: p[0]},
		}
		if _, err := store.Append(ctx, rec); err != nil {
			t.Fatalf("semear mediacao %v: %v", p, err)
		}
	}
}

// TestAOS090_TripDespromoveOsParesQueORunMediou — o caminho central. Um trip do disjuntor
// tem de descer DOIS níveis (piso L1) cada par (agente, domínio) que o run tocou, e o selo
// tem de nascer na hash-chain: uma demoção não selada seria o defeito de AOS-306 de volta.
func TestAOS090_TripDespromoveOsParesQueORunMediou(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(autonomy.NewAuditSink(store, "")))
	if _, err := reg.SetLevel(ctx, "agt-1", "fs", autonomy.L5, "inicial", "op:alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SetLevel(ctx, "agt-1", "http", autonomy.L4, "inicial", "op:alice"); err != nil {
		t.Fatal(err)
	}
	ctrl, err := autonomy.NewController(reg, nil, autonomy.DefaultAutonomyControlConfig())
	if err != nil {
		t.Fatal(err)
	}
	semearMediacoes(t, store, "run-1", [][2]string{
		{"agt-1", "cap:fs.write"},
		{"agt-1", "cap:http.post"},
		{"agt-1", "cap:fs.read"}, // mesmo domínio que o primeiro — tem de dedupli­car
	})

	a := novasAnomaliasDeAutonomia(ctrl, store, func(string, ...any) {})
	if a == nil {
		t.Fatal("o encaminhador tem de existir com controlador e WORM compostos")
	}
	a.despromover(breaker.Alert{RunID: "run-1", Kind: breaker.AlertTrip})

	// A escada normativa de tecnica/09 §7: L5->L3 e L4->L2.
	if got := reg.LevelFor("agt-1", "fs"); got != autonomy.L3 {
		t.Errorf("agt-1/fs = %s; quero L3 (L5 menos dois niveis)", got)
	}
	if got := reg.LevelFor("agt-1", "http"); got != autonomy.L2 {
		t.Errorf("agt-1/http = %s; quero L2 (L4 menos dois niveis)", got)
	}
	// E cada demoção TEM de estar selada, com o actor do controlador e o motivo.
	head, err := store.Head(ctx, autonomy.DefaultAutonomyPartition)
	if err != nil || head < 4 {
		t.Fatalf("head da particao de autonomia = %d (err=%v); quero >= 4 (2 iniciais + 2 democoes)", head, err)
	}
	recs, err := store.Read(ctx, autonomy.DefaultAutonomyPartition, 1, head)
	if err != nil {
		t.Fatal(err)
	}
	selosDoControlador := 0
	for _, rec := range recs {
		for _, ob := range rec.Obligations {
			if ob.Type == autonomy.LevelChangedEventType && ob.Params["actor"] == autonomy.ControllerActor {
				selosDoControlador++
				if !strings.Contains(ob.Params["reason"], string(autonomy.AnomalyUnsafeAction)) {
					t.Errorf("o selo tem de nomear a anomalia que o causou: %q", ob.Params["reason"])
				}
			}
		}
	}
	if selosDoControlador != 2 {
		t.Errorf("selos do controlador = %d; quero 2 (um por par)", selosDoControlador)
	}
}

// TestAOS090_SoOTripAutomaticoDespromove — escalada e abort são acções MANUAIS de um
// operador. Despromover por elas inverteria o incentivo: quem escala a um humano perderia
// autonomia POR escalar, e a escalada é precisamente o comportamento desejado.
func TestAOS090_SoOTripAutomaticoDespromove(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(autonomy.NewAuditSink(store, "")))
	if _, err := reg.SetLevel(ctx, "agt-1", "fs", autonomy.L5, "inicial", "op:alice"); err != nil {
		t.Fatal(err)
	}
	ctrl, err := autonomy.NewController(reg, nil, autonomy.DefaultAutonomyControlConfig())
	if err != nil {
		t.Fatal(err)
	}
	semearMediacoes(t, store, "run-1", [][2]string{{"agt-1", "cap:fs.write"}})
	a := novasAnomaliasDeAutonomia(ctrl, store, func(string, ...any) {})

	for _, kind := range []breaker.AlertKind{breaker.AlertEscalate, breaker.AlertAbort} {
		a.Alert(ctx, breaker.Alert{RunID: "run-1", Kind: kind})
		if len(a.fila) != 0 {
			t.Fatalf("kind %q nao pode ser enfileirado para democao", kind)
		}
	}
	if got := reg.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Errorf("agt-1/fs = %s; quero L5 — accoes manuais nao despromovem", got)
	}

	// CONTROLO: o MESMO caminho com AlertTrip enfileira. Sem isto, o teste acima passaria
	// tambem se `Alert` descartasse tudo.
	a.Alert(ctx, breaker.Alert{RunID: "run-1", Kind: breaker.AlertTrip})
	if len(a.fila) != 1 {
		t.Fatal("AlertTrip TEM de ser enfileirado — senao a guarda acima e vacua")
	}
}

// TestAOS090_AlertNaoBloqueia — o contrato de AOS-291 exige que [breaker.AlertSink.Alert]
// retorne promptamente. Com a fila cheia e sem consumidor, `Alert` tem de DESCARTAR e
// devolver, nunca esperar: um sink bloqueado prende a goroutine do run.
func TestAOS090_AlertNaoBloqueia(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	reg := autonomy.NewLevelRegistry()
	ctrl, err := autonomy.NewController(reg, nil, autonomy.DefaultAutonomyControlConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := novasAnomaliasDeAutonomia(ctrl, store, func(string, ...any) {})

	feito := make(chan struct{})
	go func() {
		defer close(feito)
		// Mais do que cabe na fila, SEM consumidor a correr.
		for i := 0; i < filaDeAnomalias*3; i++ {
			a.Alert(ctx, breaker.Alert{RunID: "run-1", Kind: breaker.AlertTrip})
		}
	}()
	select {
	case <-feito:
	case <-time.After(5 * time.Second):
		t.Fatal("Alert BLOQUEOU com a fila cheia — viola o contrato nao-bloqueante de AOS-291")
	}
	if a.descartados.Load() == 0 {
		t.Error("os descartes tem de ser CONTADOS — um trip perdido em silencio e o defeito que isto fecha")
	}
}

// TestAOS090_TripSemMediacoesNaoDespromoveNada — um run que dispara por wall-clock sem ter
// mediado uma única tool call não tem par a que aplicar a decisão. Tem de ser inofensivo:
// despromover «tudo» na ausência de informação seria pior do que não despromover.
func TestAOS090_TripSemMediacoesNaoDespromoveNada(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(autonomy.NewAuditSink(store, "")))
	if _, err := reg.SetLevel(ctx, "agt-1", "fs", autonomy.L5, "inicial", "op:alice"); err != nil {
		t.Fatal(err)
	}
	ctrl, err := autonomy.NewController(reg, nil, autonomy.DefaultAutonomyControlConfig())
	if err != nil {
		t.Fatal(err)
	}
	var linhas []string
	a := novasAnomaliasDeAutonomia(ctrl, store, func(f string, args ...any) {
		linhas = append(linhas, f)
	})
	a.despromover(breaker.Alert{RunID: "run-vazio", Kind: breaker.AlertTrip})

	if got := reg.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Errorf("agt-1/fs = %s; quero L5 — sem mediacoes nao ha par a despromover", got)
	}
	if len(linhas) == 0 || !strings.Contains(strings.Join(linhas, "\n"), "nenhum par") {
		t.Error("um trip que nao despromove nada TEM de ser declarado — em silencio " +
			"e indistinguivel do mecanismo avariado")
	}
	_ = ctx
}

// TestAOS090_ValidadorDeRehidratacaoAceitaOControlador — a regra (0). Sem ela, cada demoção
// automática seria rejeitada no arranque seguinte e a correcção evaporava-se: a demoção
// acontecia em runtime e o par voltava a L5 no reinício.
func TestAOS090_ValidadorDeRehidratacaoAceitaOControlador(t *testing.T) {
	// Operadores e setters VAZIOS de propósito: se a regra (0) dependesse deles, um nó sem
	// chaves de operador perderia as demoções — e é justamente o nó mais exposto.
	v := autonomyRehydrateValidator(nil, nil)
	if err := v(autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L5, New: autonomy.L3,
		Reason: "anomalia unsafe_action", Actor: autonomy.ControllerActor,
	}); err != nil {
		t.Fatalf("o validador tem de aceitar o actor do controlador sem prova: %v", err)
	}
	// CONTROLO: um actor de OPERADOR sem prova continua a ser recusado. Sem este controlo, a
	// regra (0) poderia ter sido escrita larga demais e o teste acima passaria na mesma.
	if err := v(autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L0, New: autonomy.L5,
		Reason: "forjado", Actor: "op:mallory",
	}); err == nil {
		t.Fatal("um registo de operador SEM prova tem de continuar a ser recusado")
	}
}

// TestAOS090_BannerNaoAfirmaDemocaoQueNaoEstaComposta — a regra que este subsistema já violou
// duas vezes. O banner segue o que está COMPOSTO, nunca o que está implementado.
func TestAOS090_BannerNaoAfirmaDemocaoQueNaoEstaComposta(t *testing.T) {
	desligado := strings.Join(autonomiaAnomaliasBanner(false), " ")
	if !strings.Contains(desligado, "NAO COMPOSTA") {
		t.Errorf("sem encaminhador o banner tem de dizer que nao esta composta: %q", desligado)
	}
	if strings.Contains(desligado, "ARMADA") {
		t.Errorf("o banner nao pode afirmar proteccao que nao existe: %q", desligado)
	}
	armado := strings.Join(autonomiaAnomaliasBanner(true), " ")
	if !strings.Contains(armado, "ARMADA") {
		t.Errorf("com encaminhador o banner tem de o declarar: %q", armado)
	}
	// E tem de declarar o que NÃO liga — senão «armada» lê-se como «os três sinais ligados».
	for _, exigido := range []string{"Override-rate", "Drift", "PROMOCAO"} {
		if !strings.Contains(armado, exigido) {
			t.Errorf("o banner tem de declarar o que fica de fora (%q): %q", exigido, armado)
		}
	}
}

// TestAOS090_EncaminhadorTemChamadorDeProducao — a guarda de composição, no molde de
// [TestAOS251_ObserveActionHasProductionCaller]. Falha se `novasAnomaliasDeAutonomia` voltar
// a ficar sem chamador de produção, que é EXACTAMENTE o estado que DEF-908 descreve: o
// mecanismo existe, está testado, e nada o invoca.
func TestAOS090_EncaminhadorTemChamadorDeProducao(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir do pacote do nó: %v", err)
	}
	fset := token.NewFileSet()
	referencias, declarada := 0, false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parser.ParseFile(%q): %v", name, perr)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "novasAnomaliasDeAutonomia" {
				declarada = true
				continue // a declaração não é uma referência
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "novasAnomaliasDeAutonomia" {
					referencias++
				}
				return true
			})
		}
	}
	if !declarada {
		t.Fatal("guarda cega: novasAnomaliasDeAutonomia deixou de existir — actualiza a ancora")
	}
	if referencias == 0 {
		t.Fatal("DEF-908 REGREDIU: o encaminhador de anomalias voltou a ter ZERO chamadores " +
			"de producao — o controlador de AOS-090 volta a ser biblioteca e um par promovido " +
			"a L5 fica a L5 ate alguem reparar")
	}
}

// TestAOS090_OSinkChegaMesmoAoDisjuntor — o vão que as guardas acima deixavam. Provar que o
// encaminhador é CONSTRUÍDO não prova que um trip lhe chega: entre os dois está
// [breaker.WithAlertSink], e sem essa linha o encaminhador ficaria composto e inerte — a
// forma mais cara do defeito, porque o banner declararia protecção.
//
// Verifica as duas metades: (a) `newRunBreakers` guarda o sink que recebe; (b) o pacote de
// produção refere `WithAlertSink` em algum lado.
func TestAOS090_OSinkChegaMesmoAoDisjuntor(t *testing.T) {
	// (a) o registo guarda o sink.
	prov, err := breakerThresholdsFromEnv()
	if err != nil {
		t.Fatalf("breakerThresholdsFromEnv: %v", err)
	}
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	sink := breaker.AlertFunc(func(context.Context, breaker.Alert) {})
	breakers, err2 := newRunBreakers(newRunStateGates(es, nil, 0), prov, sink)
	if err2 != nil {
		t.Fatalf("newRunBreakers: %v", err2)
	}
	if breakers == nil || breakers.alertas == nil {
		t.Fatal("o registo tem de GUARDAR o sink que recebe — senao o resolve nunca o liga")
	}
	// CONTROLO: sem sink o campo fica nil, e o resolve nao liga opcao nenhuma.
	semSink, err3 := newRunBreakers(newRunStateGates(es, nil, 0), prov, nil)
	if err3 != nil {
		t.Fatalf("newRunBreakers(nil): %v", err3)
	}
	if semSink != nil && semSink.alertas != nil {
		t.Fatal("sem sink o campo tem de ficar nil")
	}

	// (b) `WithAlertSink` tem referencia de producao.
	entries, err4 := os.ReadDir(".")
	if err4 != nil {
		t.Fatal(err4)
	}
	fset := token.NewFileSet()
	refs := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatal(perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithAlertSink" {
				refs++
			}
			return true
		})
	}
	if refs == 0 {
		t.Fatal("DEF-908 REGREDIU a meio: o encaminhador existe mas breaker.WithAlertSink " +
			"deixou de ser referido — nenhum trip lhe chega, e o banner declara proteccao " +
			"que nao acontece")
	}
}

// TestAOS090_DemocaoSobreviveAoReinicioComAmbienteInalterado — a propriedade ponta-a-ponta, e
// a que mais importa. Uma demoção automática que não sobrevive ao reinício não fecha DEF-908:
// o agente voltaria ao nível de que a anomalia o tirou, e o «preso a L5» reapareceria por
// outra porta.
//
// Cobre também a alavanca inversa: EDITAR `AOS_AUTONOMY_LEVELS` e reiniciar continua a ganhar,
// em qualquer direcção. Sem esse segundo ramo, uma demoção automática seria irreversível sem
// chaves de operador — e um agente despromovido por engano ficaria preso no piso.
func TestAOS090_DemocaoSobreviveAoReinicioComAmbienteInalterado(t *testing.T) {
	ctx := context.Background()
	worm := audit.NewMemStore()
	specs := []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}

	// Arranque 1: o ambiente declara L5; o controlador despromove para L3 por anomalia.
	w1 := buildAutonomyOracle(specs, autonomy.L0)
	if err := w1.provision(ctx, worm); err != nil {
		t.Fatal(err)
	}
	ctrl1, err := w1.controlador()
	if err != nil {
		t.Fatal(err)
	}
	if _, mudou, err := ctrl1.OnAnomaly(ctx, "agt-1", "fs", autonomy.AnomalyUnsafeAction); err != nil || !mudou {
		t.Fatalf("OnAnomaly: mudou=%v err=%v", mudou, err)
	}
	if got := w1.registry.LevelFor("agt-1", "fs"); got != autonomy.L3 {
		t.Fatalf("apos a anomalia = %s; quero L3", got)
	}

	// Arranque 2: registo NOVO, MESMO WORM, ambiente AINDA a dizer L5. A demoção prevalece.
	w2 := buildAutonomyOracle(specs, autonomy.L0)
	if err := w2.provision(ctx, worm,
		autonomy.WithRehydrateValidator(autonomyRehydrateValidator(nil, nil))); err != nil {
		t.Fatal(err)
	}
	if got := w2.registry.LevelFor("agt-1", "fs"); got != autonomy.L3 {
		t.Fatalf("apos reinicio = %s; quero L3 — a democao automatica TEM de sobreviver, senao "+
			"o agente volta ao nivel de que a anomalia o tirou", got)
	}
	if len(w2.rejeitados) != 0 {
		t.Fatalf("a democao nao pode ser rejeitada na rehidratacao: %+v", w2.rejeitados)
	}

	// Arranque 3: o operador EDITA o ficheiro para L4 e reinicia. O ambiente ganha.
	editado := []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L4}}
	w3 := buildAutonomyOracle(editado, autonomy.L0)
	if err := w3.provision(ctx, worm,
		autonomy.WithRehydrateValidator(autonomyRehydrateValidator(nil, nil))); err != nil {
		t.Fatal(err)
	}
	if got := w3.registry.LevelFor("agt-1", "fs"); got != autonomy.L4 {
		t.Fatalf("apos editar o ambiente = %s; quero L4 — editar o ficheiro e reiniciar tem de "+
			"continuar a ser a alavanca de resposta a incidente, nos dois sentidos", got)
	}
}
