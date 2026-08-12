package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
	runbooks "github.com/aos-ref/platform/runbooks"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-274 (F8) — o PRODUTOR de SLOs/alertas em runtime.
//
// O que estes testes provam, e porque é falsificável:
//
//  1. O avaliador dispara sobre SPANS REAIS que o nó emitiu pelo seu tracer — não sobre um
//     snapshot construído à mão. Se a torneira for desligada da composição, o alerta deixa de
//     disparar e o teste falha.
//  2. A janela SUSTENTADA é respeitada: uma observação isolada NÃO alerta (anti-fadiga).
//  3. ANTI-VACUIDADE: os SLIs sem produtor no nó ficam sem amostras e NÃO disparam — o teste
//     falharia se alguém "preenchesse o painel" com um valor plausível.
//  4. A ligação ao registo de RUNBOOKS (AOS-106) resolve para TODO o alerta produzido.
//  5. O FAIL-OPEN é executável: um pânico na avaliação é CONTIDO e o nó continua a servir runs.
//  6. A config é fail-closed (a fronteira declarada da excepção) e o `/metrics` expõe o resultado.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// aos274Clock devolve um relógio que avança `step` a CADA chamada, ancorado em time.Now(). É a
// forma de produzir latência de span determinista sem dormir: StartSpan lê t, End lê t+step, logo
// cada span fica com exactamente `step` de latência. A âncora é o AGORA real de propósito — a
// janela do avaliador filtra por tempo de fecho, e um relógio ancorado em 2023 (como o
// determinista dos outros testes) poria todos os spans fora da janela e esvaziaria o SLI.
func aos274Clock(step time.Duration) func() time.Time {
	base := time.Now()
	var n atomic.Int64
	return func() time.Time {
		return base.Add(time.Duration(n.Add(1)) * step)
	}
}

// aos274Node compõe um nó com observabilidade LIGADA (exporter injectado ⇒ a torneira de spans é
// composta) e o relógio de spans acima.
func aos274Node(t *testing.T, step time.Duration) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.OTLPExporter = &otelgenai.RecordingExporter{}
	cfg.TracerOptions = []otelgenai.TracerOption{otelgenai.WithClock(aos274Clock(step))}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// aos274Service compõe o loop de serviço com o laço DESLIGADO (cadência 0): as passagens são
// conduzidas à mão por [NodeService.EvaluateSLOsNow], para o teste ser determinista.
func aos274Service(t *testing.T, node *Node) *NodeService {
	t.Helper()
	svc, err := NewNodeService(node,
		WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithSLOEvalInterval(0))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Com a cadência a 0 o laço não arranca — mas o avaliador tem de existir para o teste o
	// conduzir. Compõe-se o MESMO objecto que o arranque comporia.
	svc.slo = newSLOEvaluator(node, DefaultSLOEvalInterval, DefaultSLOWindow)
	return svc
}

// aos274EmitToolSpans emite n spans `execute_tool` PELO TRACER DO NÓ — o mesmo caminho que a
// mediação real usa. Com o relógio de [aos274Clock] a passo `step`, cada span fica com `step` de
// latência, o que decide se o SLI de overhead p95 (SLO: 15 ms) cumpre ou viola.
func aos274EmitToolSpans(node *Node, n int) {
	for i := 0; i < n; i++ {
		_, span := node.Tracer.StartSpan(context.Background(), otelgenai.OpExecuteTool)
		span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpExecuteTool)
		span.SetAttribute(otelgenai.AttrToolName, "aos274.tool")
		span.SetAttribute(otelgenai.AttrDecision, otelgenai.DecisionPermit)
		span.End()
	}
}

// aos274Alert localiza um alerta pelo nome na avaliação.
func aos274Alert(t *testing.T, ev *SLOEvaluation, name string) SLOAlert {
	t.Helper()
	for _, a := range ev.Alerts {
		if a.Alert.Name == name {
			return a
		}
	}
	t.Fatalf("alerta %q ausente da avaliacao (regras avaliadas: %d)", name, len(ev.Alerts))
	return SLOAlert{}
}

// aos274SLI localiza um SLI operacional renderizado.
func aos274SLI(t *testing.T, ev *SLOEvaluation, sli string) otelgenai.SLIValue {
	t.Helper()
	rp, ok := ev.Operational.Panel(sli)
	if !ok {
		t.Fatalf("SLI operacional %q ausente do snapshot", sli)
	}
	return rp.SLI
}

// ---------------------------------------------------------------------------
// (1) Alertas sobre DADOS REAIS — spans que o nó emitiu.
// ---------------------------------------------------------------------------

// TestAOS274_AlertaDisparaSobreSpansReais: com spans `execute_tool` REAIS de 20 ms (SLO: 15 ms), o
// alerta de overhead de mediação dispara — nas DUAS famílias compostas na mesma passagem. Prova a
// cadeia toda: tracer do nó → torneira → agregação query-time → SLI vs. SLO → janela sustentada →
// alerta → runbook.
func TestAOS274_AlertaDisparaSobreSpansReais(t *testing.T) {
	node := aos274Node(t, 20*time.Millisecond) // 20 ms > SLO de 15 ms
	svc := aos274Service(t, node)
	ctx := context.Background()

	if node.sloTap == nil {
		t.Fatal("com observabilidade ligada a torneira de spans (AOS-274) tinha de estar composta")
	}
	aos274EmitToolSpans(node, 12)

	// A janela sustentada mais longa em jogo é 3 (config da mediação, AOS-086): 3 observações
	// consecutivas em breach. As duas primeiras NÃO podem disparar — é o anti-fadiga.
	var ev *SLOEvaluation
	for i := 1; i <= 3; i++ {
		ev = svc.EvaluateSLOsNow(ctx)
		if ev == nil {
			t.Fatalf("passagem %d devolveu nil", i)
		}
		med := aos274Alert(t, ev, otelgenai.AlertMediationOverheadHigh)
		if i < 3 && med.Alert.Fired {
			t.Fatalf("passagem %d: o alerta de mediacao disparou ANTES da janela sustentada (streak=%d) — anti-fadiga partido", i, med.Alert.Streak)
		}
	}

	if ev.SpansInWindow < 12 {
		t.Fatalf("a janela devia agregar os >=12 spans REAIS emitidos, agregou %d", ev.SpansInWindow)
	}
	if ev.SpansObserved < 12 {
		t.Fatalf("a torneira devia ter observado >=12 spans, observou %d", ev.SpansObserved)
	}

	// Família da MEDIAÇÃO (AOS-085/086).
	med := aos274Alert(t, ev, otelgenai.AlertMediationOverheadHigh)
	if !med.Alert.Fired {
		t.Fatalf("apos 3 observacoes em breach o alerta de mediacao tinha de disparar (streak=%d, valor=%g, slo=%g)",
			med.Alert.Streak, med.Alert.Value, med.Alert.Threshold)
	}
	if med.Catalog != sloCatalogMediation {
		t.Errorf("catalogo do alerta de mediacao = %q, esperado %q", med.Catalog, sloCatalogMediation)
	}
	if len(med.Alert.Offenders) == 0 {
		t.Error("um breach de overhead tinha de trazer os trace_ids OFENSORES (drill-down ate ao trace real)")
	}
	// Falsificabilidade do "dados reais": o valor observado é a latência REAL dos spans, não um
	// número qualquer acima do limiar.
	if med.Alert.Value < float64(15*time.Millisecond) {
		t.Errorf("o valor do SLI (%g ns) devia ser o p95 REAL dos spans (~20 ms), acima do SLO", med.Alert.Value)
	}

	// Família OPERACIONAL (AOS-104/105) — a MESMA passagem, sobre os MESMOS spans.
	op := aos274Alert(t, ev, otelgenai.AlertMediationOverheadP95High)
	if !op.Alert.Fired {
		t.Fatalf("o alerta operacional de overhead p95 tinha de disparar na mesma passagem (streak=%d)", op.Alert.Streak)
	}
	if op.Catalog != sloCatalogOperational {
		t.Errorf("catalogo do alerta operacional = %q, esperado %q", op.Catalog, sloCatalogOperational)
	}
}

// TestAOS274_SemBreachNaoDispara: com spans REAIS abaixo do SLO (1 ms << 15 ms), o SLI é AVALIADO
// (tem amostras) e NÃO dispara. É o outro sentido da prova — sem ele, um avaliador que disparasse
// sempre passaria o teste acima.
func TestAOS274_SemBreachNaoDispara(t *testing.T) {
	node := aos274Node(t, 1*time.Millisecond)
	svc := aos274Service(t, node)
	aos274EmitToolSpans(node, 12)

	var ev *SLOEvaluation
	for i := 0; i < 4; i++ {
		ev = svc.EvaluateSLOsNow(context.Background())
	}
	med := aos274Alert(t, ev, otelgenai.AlertMediationOverheadHigh)
	if med.Alert.Fired {
		t.Fatalf("com overhead REAL de 1 ms (SLO 15 ms) o alerta NAO devia disparar (valor=%g)", med.Alert.Value)
	}
	if ev.Mediation.MediationOverheadP95.Samples == 0 {
		t.Fatal("o SLI tinha de estar AVALIADO (amostras > 0) — senao o 'nao disparou' seria por vacuidade, nao por cumprimento")
	}
}

// ---------------------------------------------------------------------------
// (2) Fontes que NÃO dependem de spans — disponibilidade e integridade do WORM.
// ---------------------------------------------------------------------------

// TestAOS274_DisponibilidadeEIntegridadeSaoMedidasReais: sem UM span sequer, as duas fontes de
// maior severidade continuam a produzir sinal — a sonda de prontidão (a mesma do /readyz) e a
// verificação da hash-chain do WORM. É o que garante que um deployment sem OTLP não fica cego.
func TestAOS274_DisponibilidadeEIntegridadeSaoMedidasReais(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	defer func() { _ = node.Close() }()
	if node.sloTap != nil {
		t.Fatal("sem observabilidade OTLP a torneira NAO devia ser composta (o no fica byte-identico ao modo sem tracing)")
	}
	svc := aos274Service(t, node)

	ev := svc.EvaluateSLOsNow(context.Background())
	if ev == nil {
		t.Fatal("a passagem devolveu nil")
	}
	if ev.SpansInWindow != 0 {
		t.Fatalf("sem tracing nao devia haver spans agregados, houve %d", ev.SpansInWindow)
	}

	avail := aos274SLI(t, ev, otelgenai.SLIControlPlaneAvailability)
	if avail.Samples == 0 {
		t.Fatal("a disponibilidade do plano de controlo tinha de ter amostra (a sonda corre a cada passagem)")
	}
	if avail.Value != 1 {
		t.Errorf("num no saudavel a disponibilidade medida devia ser 1, veio %g", avail.Value)
	}

	worm := aos274SLI(t, ev, otelgenai.SLIAuditWORMIntegrity)
	if !ev.WORMChecked {
		t.Fatal("com WORM composto a hash-chain tinha de ser VERIFICADA na passagem")
	}
	if worm.Breached() {
		t.Error("a cadeia de um no intacto nao devia estar em breach")
	}
}

// tamperedWORM decora o WORM real e devolve, sob pedido, um registo MUTADO na leitura — o
// EntryHash deixa de recomputar e [audit.Verify] devolve um *audit.VerifyError. É a forma de
// exercitar o caminho de ADULTERAÇÃO sem um WAL adulterado em disco (que o próprio arranque
// recusaria, AOS-221).
type tamperedWORM struct {
	audit.Store
	partition string
	broken    atomic.Bool
}

func (w *tamperedWORM) Partitions() []string { return []string{w.partition} }

func (w *tamperedWORM) Read(ctx context.Context, partition string, from, to uint64) ([]audit.AuditRecord, error) {
	recs, err := w.Store.Read(ctx, partition, from, to)
	if err != nil || !w.broken.Load() || len(recs) == 0 {
		return recs, err
	}
	out := append([]audit.AuditRecord(nil), recs...)
	out[0].Capability = "AOS274-MUTADO"
	return out, nil
}

// opaqueWORM não implementa [audit.PartitionLister]: a cadeia NÃO é verificável pelo nó.
type opaqueWORM struct{ audit.Store }

// TestAOS274_AdulteracaoDisparaIntegridadeMasIlegivelNaoInventa prova os TRÊS estados de
// [sloEvaluator.probeWORM] — e o terceiro é o que interessa: um WORM que não se consegue ler NÃO
// é declarado adulterado. Um falso positivo aqui manda um operador correr o procedimento de DR
// sem haver DR nenhum a fazer.
func TestAOS274_AdulteracaoDisparaIntegridadeMasIlegivelNaoInventa(t *testing.T) {
	ctx := context.Background()

	t.Run("adulteracao_dispara", func(t *testing.T) {
		node := newTestNode(t, &countingModel{})
		defer func() { _ = node.Close() }()
		w := &tamperedWORM{Store: node.WORM, partition: "aos274.p"}
		if _, err := w.Append(ctx, audit.AuditRecord{
			Partition: "aos274.p", Decision: audit.DecisionAllow, Capability: "aos274:probe",
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
		node.WORM = w
		svc := aos274Service(t, node)

		// Sentido A: intacto ⇒ nenhum alerta de integridade (o teste não é vácuo).
		ev := svc.EvaluateSLOsNow(ctx)
		if aos274Alert(t, ev, otelgenai.AlertAuditWORMIntegrityBroken).Alert.Fired {
			t.Fatal("com a cadeia INTACTA o alerta de integridade nao devia disparar")
		}

		// Sentido B: adulterado ⇒ dispara (janela sustentada 1 — é agudo).
		w.broken.Store(true)
		ev = svc.EvaluateSLOsNow(ctx)
		if !ev.WORMChecked {
			t.Fatal("a adulteracao tinha de contar como VERIFICADA (e o veredicto e 'nao integro')")
		}
		alert := aos274Alert(t, ev, otelgenai.AlertAuditWORMIntegrityBroken)
		if !alert.Alert.Fired {
			t.Fatal("a hash-chain adulterada tinha de disparar o alerta de integridade do WORM")
		}
		if !alert.RunbookResolved || alert.Runbook.ID != otelgenai.ProcDisasterRecovery {
			t.Errorf("o alerta de integridade tinha de encaminhar para %q resolvido no registo, veio %q (resolvido=%t)",
				otelgenai.ProcDisasterRecovery, alert.Runbook.ID, alert.RunbookResolved)
		}
	})

	t.Run("ilegivel_nao_inventa", func(t *testing.T) {
		node := newTestNode(t, &countingModel{})
		defer func() { _ = node.Close() }()
		node.WORM = opaqueWORM{Store: node.WORM}
		svc := aos274Service(t, node)

		ev := svc.EvaluateSLOsNow(ctx)
		if ev.WORMChecked {
			t.Fatal("um WORM OPACO (sem PartitionLister) nao e verificavel — nao podia contar como verificado")
		}
		if aos274SLI(t, ev, otelgenai.SLIAuditWORMIntegrity).Samples != 0 {
			t.Fatal("sem verificacao possivel o SLI tinha de ficar SEM AMOSTRAS (anti-vacuidade), nao a zero")
		}
		if aos274Alert(t, ev, otelgenai.AlertAuditWORMIntegrityBroken).Alert.Fired {
			t.Fatal("um WORM ilegivel NAO e uma adulteracao — disparar aqui e um falso positivo que manda o operador correr DR sem haver DR")
		}
	})
}

// ---------------------------------------------------------------------------
// (3) Anti-vacuidade: o que não tem produtor não é inventado.
// ---------------------------------------------------------------------------

// TestAOS274_SemProdutorNaoDisparaNemAfirmaCumprimento: headroom do scheduler e fidelidade de
// replay não têm produtor no nó. Ficam sem amostras e NÃO disparam. Este teste falha no dia em que
// alguém injectar um valor plausível para "preencher o painel" — que é o alerta sintético que o
// critério de aceitação proíbe.
func TestAOS274_SemProdutorNaoDisparaNemAfirmaCumprimento(t *testing.T) {
	node := aos274Node(t, 1*time.Millisecond)
	svc := aos274Service(t, node)
	aos274EmitToolSpans(node, 8)

	var ev *SLOEvaluation
	for i := 0; i < 4; i++ {
		ev = svc.EvaluateSLOsNow(context.Background())
	}
	for _, sli := range []string{otelgenai.SLIHeadroomTokens, otelgenai.SLIReplayFidelity} {
		v := aos274SLI(t, ev, sli)
		if v.Samples != 0 {
			t.Errorf("o SLI %q nao tem produtor no no — devia ficar SEM amostras, veio %d (valor=%g)", sli, v.Samples, v.Value)
		}
		if v.Breached() {
			t.Errorf("o SLI %q sem amostras nunca pode estar em breach", sli)
		}
	}
	for _, name := range []string{
		otelgenai.AlertHeadroomRateLimitCollapse,
		otelgenai.AlertHeadroomBudgetExhaustion,
		otelgenai.AlertReplayFidelityLow,
	} {
		if aos274Alert(t, ev, name).Alert.Fired {
			t.Errorf("o alerta %q nao tem fonte real neste no — disparar seria sintetico", name)
		}
	}
}

// ---------------------------------------------------------------------------
// (4) Ligação ao registo de runbooks (AOS-106).
// ---------------------------------------------------------------------------

// TestAOS274_TodoAlerteResolveRunbook: cada alerta produzido resolve para uma entrada do registo
// canónico, com título e (nos canónicos) doc + ADR. É o invariante de não-órfãos de AOS-106
// verificado em RUNTIME — onde a config pode ter sido substituída — e não só no CI.
func TestAOS274_TodoAlerteResolveRunbook(t *testing.T) {
	if err := runbooks.Validate(); err != nil {
		t.Fatalf("o registo de runbooks (AOS-106) tem de validar contra os alertas de referencia: %v", err)
	}
	node := aos274Node(t, 20*time.Millisecond)
	svc := aos274Service(t, node)
	aos274EmitToolSpans(node, 8)

	ev := svc.EvaluateSLOsNow(context.Background())
	if len(ev.Alerts) == 0 {
		t.Fatal("a passagem nao avaliou regra nenhuma")
	}
	for _, sa := range ev.Alerts {
		if !sa.RunbookResolved {
			t.Errorf("o alerta %q encaminha para o runbook %q SEM entrada no registo (orfao)", sa.Alert.Name, sa.Alert.Route.Runbook)
			continue
		}
		if sa.Runbook.Title == "" {
			t.Errorf("o runbook %q do alerta %q nao tem titulo", sa.Runbook.ID, sa.Alert.Name)
		}
		if sa.Runbook.Kind == runbooks.KindCanonical && (sa.Runbook.DocPath == "" || sa.Runbook.ADR == "") {
			t.Errorf("o runbook canonico %q devia trazer doc + ADR ao alerta %q (doc=%q adr=%q)",
				sa.Runbook.ID, sa.Alert.Name, sa.Runbook.DocPath, sa.Runbook.ADR)
		}
	}
}

// TestAOS274_LogEstruturadoLevaORunbook: a linha de log de um alerta disparado leva o runbook
// resolvido — id, título, doc e ADR. É a superfície pela qual o operador salta do sinal para o
// procedimento sem procurar.
func TestAOS274_LogEstruturadoLevaORunbook(t *testing.T) {
	node := aos274Node(t, 20*time.Millisecond)
	var logbuf strings.Builder
	svc, err := NewNodeService(node,
		WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithSLOEvalInterval(0), WithServiceLog(&logbuf))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	svc.slo = newSLOEvaluator(node, DefaultSLOEvalInterval, DefaultSLOWindow)
	aos274EmitToolSpans(node, 8)
	for i := 0; i < 3; i++ {
		svc.EvaluateSLOsNow(context.Background())
	}

	out := logbuf.String()
	for _, want := range []string{
		"SLO/ALERTA (AOS-274) A DISPARAR",
		`alerta="mediation_overhead_high"`,
		`runbook="RB-04"`,
		"runbook_titulo=",
		`runbook_doc="docs/runbooks/RB-04.md"`,
		`runbook_adr="ADR-011"`,
		"avaliador de SLOs (AOS-274): passagem",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("o log estruturado devia conter %q; log:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// (5) FAIL-OPEN — a excepção declarada.
// ---------------------------------------------------------------------------

// TestAOS274_FailOpenPanicoContidoNaoDerrubaONo: um pânico na avaliação é CONTIDO — o nó continua
// a servir runs e a passagem seguinte volta a produzir sinal. Sem o recover, um pânico nesta
// goroutine derrubaria o PROCESSO: é o modo de falha exacto que a excepção existe para excluir.
func TestAOS274_FailOpenPanicoContidoNaoDerrubaONo(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	defer func() { _ = node.Close() }()
	svc := aos274Service(t, node)
	ctx := context.Background()

	explode := func(context.Context) bool { panic("AOS-274: sonda em panico (simulado)") }
	if ev := svc.slo.evaluateSafely(ctx, explode, svc.log); ev != nil {
		t.Fatal("uma passagem que entrou em panico nao podia devolver avaliacao")
	}
	if svc.slo.Panics() != 1 {
		t.Fatalf("o panico contido tinha de ser CONTABILIZADO (para ser alertavel de fora), veio %d", svc.slo.Panics())
	}

	// O nó continua a servir: é isto que "a observabilidade nunca derruba o nó" quer dizer.
	if err := svc.Submit(ctx, svcGoal("aos274-pos-panico", "")); err != nil {
		t.Fatalf("apos o panico CONTIDO o no tinha de continuar a aceitar runs: %v", err)
	}
	if _, _, err := svc.Wait(ctx, "aos274-pos-panico"); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// E a avaliação recupera na passagem seguinte.
	if ev := svc.EvaluateSLOsNow(ctx); ev == nil {
		t.Fatal("a passagem seguinte a um panico contido tinha de voltar a produzir avaliacao")
	}
}

// TestAOS274_SondaPenduradaNaoSeguraOLaco: uma dependência crítica PENDURADA atrasa no máximo uma
// passagem (prazo próprio) — nunca segura o laço nem o caminho de execução. É o ponto (3) do
// fail-open.
func TestAOS274_SondaPenduradaNaoSeguraOLaco(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	defer func() { _ = node.Close() }()
	svc := aos274Service(t, node)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan *SLOEvaluation, 1)
	go func() {
		done <- svc.slo.evaluateSafely(ctx, func(c context.Context) bool {
			<-c.Done() // sonda que nunca responde: só sai quando o prazo/ctx a liberta
			return false
		}, svc.log)
	}()
	select {
	case ev := <-done:
		if ev == nil {
			t.Fatal("a passagem devia concluir (com a prontidao a contar como indisponivel), nao abortar")
		}
		if avail := aos274SLI(t, ev, otelgenai.SLIControlPlaneAvailability); avail.Samples == 0 {
			t.Error("mesmo com a sonda pendurada a observacao tinha de ser registada")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a passagem ficou PENDURADA — o fail-open exige que uma dependencia sem resposta atrase uma passagem, nao o laco")
	}
}

// ---------------------------------------------------------------------------
// (6) Config FAIL-CLOSED — a fronteira da excepção.
// ---------------------------------------------------------------------------

// TestAOS274_ConfigFailClosed: a AVALIAÇÃO é fail-open, a CONFIGURAÇÃO não. Um valor ilegível
// aborta; `0` na cadência desliga EXPLICITAMENTE; vazio ⇒ default.
func TestAOS274_ConfigFailClosed(t *testing.T) {
	t.Run("intervalo", func(t *testing.T) {
		cases := []struct {
			raw     string
			want    time.Duration
			wantErr bool
		}{
			{"", DefaultSLOEvalInterval, false},
			{"30s", 30 * time.Second, false},
			{"0", 0, false}, // desliga explicitamente
			{"abc", 0, true},
			{"-1m", 0, true},
			{"5", 0, true}, // sem unidade não é uma duração Go
		}
		for _, tc := range cases {
			t.Setenv("AOS_SLO_EVAL_INTERVAL", tc.raw) // "" e tratada como NAO definida (TrimSpace)
			got, err := sloEvalIntervalFromEnv()
			if tc.wantErr {
				if !errors.Is(err, ErrBadSLOEvalInterval) {
					t.Errorf("AOS_SLO_EVAL_INTERVAL=%q devia abortar com ErrBadSLOEvalInterval, veio (%v, %v)", tc.raw, got, err)
				}
				continue
			}
			if err != nil || got != tc.want {
				t.Errorf("AOS_SLO_EVAL_INTERVAL=%q => (%v, %v), esperado (%v, nil)", tc.raw, got, err, tc.want)
			}
		}
	})

	t.Run("janela", func(t *testing.T) {
		cases := []struct {
			raw     string
			want    time.Duration
			wantErr bool
		}{
			{"", DefaultSLOWindow, false},
			{"10m", 10 * time.Minute, false},
			{"0", 0, true}, // uma janela vazia esvaziaria todos os SLIs derivados de spans
			{"-1m", 0, true},
			{"nao-e-duracao", 0, true},
		}
		for _, tc := range cases {
			t.Setenv("AOS_SLO_WINDOW", tc.raw) // "" e tratada como NAO definida (TrimSpace)
			got, err := sloWindowFromEnv()
			if tc.wantErr {
				if !errors.Is(err, ErrBadSLOWindow) {
					t.Errorf("AOS_SLO_WINDOW=%q devia abortar com ErrBadSLOWindow, veio (%v, %v)", tc.raw, got, err)
				}
				continue
			}
			if err != nil || got != tc.want {
				t.Errorf("AOS_SLO_WINDOW=%q => (%v, %v), esperado (%v, nil)", tc.raw, got, err, tc.want)
			}
		}
	})
}

// TestAOS274_BannerDeclaraAPostura: o banner declara a postura REALMENTE composta (AOS-248) e —
// o ponto — QUE fontes têm produtor. Um painel vazio sem explicação é indistinguível de um nó
// saudável.
func TestAOS274_BannerDeclaraAPostura(t *testing.T) {
	comTracing := aos274Node(t, time.Millisecond)
	ligado := sloEvaluatorBanner(comTracing, DefaultSLOEvalInterval, DefaultSLOWindow)
	for _, want := range []string{"LIGADO", "FAIL-OPEN DECLARADO", "torneira de spans", "AOS-106"} {
		if !strings.Contains(ligado, want) {
			t.Errorf("o banner de ligado devia conter %q; veio:\n%s", want, ligado)
		}
	}

	semTracing := newTestNode(t, &countingModel{})
	defer func() { _ = semTracing.Close() }()
	dormente := sloEvaluatorBanner(semTracing, DefaultSLOEvalInterval, DefaultSLOWindow)
	if !strings.Contains(dormente, "DORMENTES") || !strings.Contains(dormente, "AOS_OTLP_ENDPOINT") {
		t.Errorf("sem observabilidade o banner tem de declarar QUE SLIs ficam sem produtor e como os ligar; veio:\n%s", dormente)
	}

	desligado := sloEvaluatorBanner(semTracing, 0, DefaultSLOWindow)
	if !strings.Contains(desligado, "DESLIGADO") || !strings.Contains(desligado, "AOS_SLO_EVAL_INTERVAL=0") {
		t.Errorf("com cadencia 0 o banner tem de declarar o avaliador DESLIGADO; veio:\n%s", desligado)
	}
}

// ---------------------------------------------------------------------------
// (7) Exposição — GET /metrics.
// ---------------------------------------------------------------------------

// TestAOS274_MetricsExpoeSLOsEAlertas: o resultado é EXPOSTO, com o runbook como label — quem
// recebe o alerta já sabe qual o procedimento. E os trace_ids ofensores NÃO viajam para uma rota
// não-autenticada (a filosofia não-enumerável do nó vale para o /metrics).
func TestAOS274_MetricsExpoeSLOsEAlertas(t *testing.T) {
	node := aos274Node(t, 20*time.Millisecond)
	svc := aos274Service(t, node)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	aos274EmitToolSpans(node, 8)
	var ev *SLOEvaluation
	for i := 0; i < 3; i++ {
		ev = svc.EvaluateSLOsNow(context.Background())
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devia dar 200, veio %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"aos_slo_evaluator_armed 1",
		"aos_slo_evaluations_total 3",
		"aos_slo_evaluator_panics_total 0",
		"aos_slo_spans_in_window",
		`aos_slo_sli{catalog="operational",sli="audit_worm_integrity"}`,
		`aos_slo_samples{catalog="operational",sli="headroom_tokens"} 0`,
		`aos_alert_firing{alert="mediation_overhead_high",catalog="mediation",severity="critical",sli="mediation_overhead_p95",runbook="RB-04",owner="DevOps/SRE",runbook_orphan="0"} 1`,
		`aos_alert_streak{alert="mediation_overhead_high",catalog="mediation"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics devia conter %q", want)
		}
	}
	// NÃO-ENUMERÁVEL: os ofensores existem na avaliação mas não saem pela rota não-autenticada.
	med := aos274Alert(t, ev, otelgenai.AlertMediationOverheadHigh)
	if len(med.Alert.Offenders) == 0 {
		t.Fatal("pre-condicao do teste: o breach tinha de ter ofensores")
	}
	for _, trace := range med.Alert.Offenders {
		if strings.Contains(body, trace) {
			t.Errorf("o trace_id ofensor %q NAO pode sair no /metrics (rota nao-autenticada, filosofia nao-enumeravel)", trace)
		}
	}
}

// TestAOS274_MetricsSemAvaliadorNaoAfirmaNada: com o avaliador desligado, o /metrics declara-o em
// vez de omitir — e não publica SLIs nenhuns (que seriam inventados).
func TestAOS274_MetricsSemAvaliadorNaoAfirmaNada(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute), WithSLOEvalInterval(0))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "aos_slo_evaluator_armed 0") {
		t.Error("com o avaliador desligado o /metrics tem de o DECLARAR (aos_slo_evaluator_armed 0)")
	}
	if strings.Contains(body, "aos_alert_firing") {
		t.Error("sem avaliador nao pode haver alertas expostos")
	}
}

// ---------------------------------------------------------------------------
// (8) O laço no loop de serviço — arranca e pára com o Shutdown.
// ---------------------------------------------------------------------------

// TestAOS274_LacoCorreNoLoopDeServicoEParaNoShutdown: o avaliador é composto no LOOP DE SERVIÇO
// (molde sweeper) e o Shutdown pára-o com o mesmo `sweepStop` dos outros quatro laços.
func TestAOS274_LacoCorreNoLoopDeServicoEParaNoShutdown(t *testing.T) {
	node := aos274Node(t, time.Millisecond)
	svc, err := NewNodeService(node,
		WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithSLOEvalInterval(10*time.Millisecond), WithSLOWindow(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	if !svc.SLOEvaluatorArmed() {
		t.Fatal("com cadencia > 0 o avaliador tinha de estar composto no loop de servico")
	}

	deadline := time.Now().Add(3 * time.Second)
	for svc.slo.Evaluations() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if svc.slo.Evaluations() == 0 {
		t.Fatal("o laco periodico nao produziu nenhuma passagem")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	parado := svc.slo.Evaluations()
	time.Sleep(60 * time.Millisecond) // várias cadências
	if depois := svc.slo.Evaluations(); depois != parado {
		t.Fatalf("o laco devia PARAR no Shutdown (mesmo sweepStop dos outros laços): %d -> %d", parado, depois)
	}
}
