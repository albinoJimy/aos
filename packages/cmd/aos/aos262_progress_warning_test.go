package main

// AOS-262 — o BURN-DOWN + AVISO no nó (primeira entrega: sem decisão).
//
// Quatro eixos, todos gates:
//
//  1. `AOS_PROGRESS_THRESHOLD` RECUSA arrancar com valor inválido (molde de
//     [ErrBadBreakerThresholds]) — nunca o fallback silencioso de `WithThreshold`;
//  2. pedir o aviso SEM tecto ABORTA o arranque ([ErrProgressBudgetUnwired]) — um aviso que
//     nunca poderia disparar é uma promessa vazia (molde de AOS-246);
//  3. o observador, sobre os adaptadores node-local REAIS, avisa a ~limiar UMA VEZ por run;
//  4. POSTURA ANUNCIADA = POSTURA LIGADA: o banner deriva do observador composto.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	eventstore "github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// --- (1) A env: default, valores válidos, e o fail-closed ---------------------------

func TestAOS262_ThresholdEnvAusenteUsaODefault(t *testing.T) {
	t.Setenv("AOS_PROGRESS_THRESHOLD", "")

	th, pedido, err := progressThresholdFromEnv()
	if err != nil {
		t.Fatalf("sem a variavel definida devia usar o default; err=%v", err)
	}
	if th != progresssurface.DefaultThreshold {
		t.Fatalf("default = %v, esperado %v", th, progresssurface.DefaultThreshold)
	}
	if pedido {
		t.Error("a ausencia da variavel NAO e um pedido explicito do operador — e o que arma o gate de cablagem")
	}
}

func TestAOS262_ThresholdEnvValidaEUsadaTalQual(t *testing.T) {
	t.Setenv("AOS_PROGRESS_THRESHOLD", " 0.65 ")

	th, pedido, err := progressThresholdFromEnv()
	if err != nil {
		t.Fatalf("progressThresholdFromEnv: %v", err)
	}
	if th != 0.65 {
		t.Fatalf("o valor do operador nao pode ser reinterpretado: got %v", th)
	}
	if !pedido {
		t.Error("uma variavel definida E um pedido explicito")
	}
}

// TestAOS262_ThresholdEnvInvalidaAborta é o critério de aceitação literal: recusa arrancar,
// NÃO o fallback silencioso de [progresssurface.WithThreshold].
func TestAOS262_ThresholdEnvInvalidaAborta(t *testing.T) {
	casos := map[string]string{
		"nao-numero":  "oitenta",
		"negativo":    "-0.5",
		"zero":        "0",   // avisaria em TODOS os turnos (0 tokens gastos ja >= 0)
		"um":          "1",   // nunca avisaria antes de o tecto estar esgotado
		"acima-de-um": "1.5", // idem, e mais obviamente errado
		"percentagem": "80%", // o erro de digitacao mais provavel do operador
		"oitenta":     "80",  // idem: uma percentagem em vez de uma fraccao
	}
	for nome, valor := range casos {
		t.Run(nome, func(t *testing.T) {
			t.Setenv("AOS_PROGRESS_THRESHOLD", valor)
			th, pedido, err := progressThresholdFromEnv()
			if !errors.Is(err, ErrBadProgressThreshold) {
				t.Fatalf("AOS_PROGRESS_THRESHOLD=%q devia ABORTAR com ErrBadProgressThreshold; err=%v", valor, err)
			}
			if th != 0 || pedido {
				t.Errorf("um valor invalido nao pode devolver um limiar parcialmente aceite: th=%v pedido=%v", th, pedido)
			}
		})
	}
}

// --- (2) O gate de cablagem no ARRANQUE do nó REAL ---------------------------------

// aos262LimparEnvs neutraliza as envs que este ticket toca (e a do tecto de que depende),
// para que cada caso parta do estado por omissão e não do ambiente de quem corre os testes.
func aos262LimparEnvs(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AOS_PROGRESS_THRESHOLD", "AOS_BUDGET_MAX_TOKENS"} {
		t.Setenv(k, "")
	}
}

// TestAOS262_BootComLimiarSemTectoAborta: o nó REAL recusa arrancar quando o operador pede
// o aviso mas não configurou o tecto. Sem denominador a fracção é 0 para sempre — o aviso
// nunca dispararia, e o banner estaria a prometê-lo.
func TestAOS262_BootComLimiarSemTectoAborta(t *testing.T) {
	aos262LimparEnvs(t)
	t.Setenv("AOS_PROGRESS_THRESHOLD", "0.8")

	node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if node != nil {
		t.Cleanup(func() { _ = node.Close() })
	}
	if !errors.Is(err, ErrProgressBudgetUnwired) {
		t.Fatalf("limiar sem tecto devia abortar com ErrProgressBudgetUnwired; err=%v node=%v", err, node != nil)
	}
}

// TestAOS262_BootComLimiarInvalidoAborta: a validação da env corre no arranque REAL, não só
// na função isolada.
func TestAOS262_BootComLimiarInvalidoAborta(t *testing.T) {
	aos262LimparEnvs(t)
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "50000")
	t.Setenv("AOS_PROGRESS_THRESHOLD", "80")

	node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if node != nil {
		t.Cleanup(func() { _ = node.Close() })
	}
	if !errors.Is(err, ErrBadProgressThreshold) {
		t.Fatalf("limiar invalido devia abortar com ErrBadProgressThreshold; err=%v", err)
	}
}

// TestAOS262_BootComTectoCompoeOObservador: o caminho legítimo — com tecto (e com o limiar
// por omissão ou explícito) o nó arranca e o observador FICA composto. É a metade que
// impede o gate de degenerar em "recusar sempre".
func TestAOS262_BootComTectoCompoeOObservador(t *testing.T) {
	aos262LimparEnvs(t)
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "50000")
	t.Setenv("AOS_PROGRESS_THRESHOLD", "0.75")

	node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("com tecto e limiar validos o no tem de arrancar: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.progress == nil {
		t.Fatal("com tecto configurado o observador de burn-down TEM de ser composto")
	}
	if node.progress.threshold != 0.75 {
		t.Fatalf("o limiar do operador nao chegou a superficie: %v", node.progress.threshold)
	}
	// nil-safe: o forget do fim do run nunca pode explodir.
	node.progress.forget("run-que-nao-existe")
}

// TestAOS262_BootSemTectoNaoCompoeObservador: sem tecto o observador NÃO é composto (e o
// banner declara-o). Um observador composto sem denominador seria uma fachada.
func TestAOS262_BootSemTectoNaoCompoeObservador(t *testing.T) {
	aos262LimparEnvs(t)

	node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("sem tecto o no tem de arrancar na mesma: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.progress != nil {
		t.Fatal("sem AOS_BUDGET_MAX_TOKENS nao ha denominador — o observador NAO pode ser composto")
	}
	// nil-safe em todos os caminhos que o service.go usa.
	node.progress.forget("run-1")
	if obs := node.progress.observer(); obs != nil {
		t.Fatal("o observador nao composto tem de ser um nil de INTERFACE (senao o kernel ligava um observador fantasma)")
	}
}

// --- (3) O observador sobre os adaptadores node-local REAIS ------------------------

// aos262Harness compõe o observador com as peças REAIS do nó (orçamento por-run, ledger de
// turnos sobre um Event Store, gates de estado) e devolve o que os testes precisam.
type aos262Harness struct {
	prog   *runProgress
	rec    *agentruntime.TurnRecorder
	tracer *otelgenai.RecordingTracer
	gates  *runStateGates
	// logs captura o LOG DO NÓ — a superfície onde o aviso é visível SEM OTLP. É a
	// segunda metade de AC3 (o span só tem destino com AOS_OTLP_ENDPOINT definida).
	logs *syncBuf
}

func novoAOS262Harness(t *testing.T, maxTokens int64, threshold float64) *aos262Harness {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	rb, err := integration.NewRunBudget(maxTokens)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	gates := newRunStateGates(es, nil, 0)
	logs := &syncBuf{}
	log := func(format string, args ...any) { fmt.Fprintf(logs, "[aos] "+format+"\n", args...) }
	// prompt=nil (AOS-263): estes testes selam o comportamento de AOS-262 PURO — avisar sem
	// decidir. Com o prompt armado o run suspender-se-ia, que é o que aos263_*_test.go cobre.
	prog := newRunProgress(gates, rb, newTurnLedgerBurndown(es), tracer, threshold, log, nil)
	if prog == nil {
		t.Fatal("o observador tinha de ser composto com orcamento e fonte")
	}
	return &aos262Harness{prog: prog, rec: agentruntime.NewTurnRecorder(es), tracer: tracer, gates: gates, logs: logs}
}

// aos262AvisosNoLog conta as linhas de AVISO DE BURN-DOWN emitidas no log do nó.
func aos262AvisosNoLog(s string) int { return strings.Count(s, "AVISO DE BURN-DOWN") }

// TestAOS262_ObservadorAvisaAoLimiarUmaVezPorRun — o caminho completo pelas peças reais:
// os turnos entram no ledger, o observador lê-os na fronteira de fim-de-turno, e o aviso sai
// UMA VEZ quando a fracção cruza o limiar.
func TestAOS262_ObservadorAvisaAoLimiarUmaVezPorRun(t *testing.T) {
	h := novoAOS262Harness(t, 1000, 0.80)
	ctx := context.Background()
	const run = "run-262-aviso"

	// O gate de estado dá ao ProgressReflector um State REAL para reflectir.
	if err := h.gates.Open(ctx, run, state.Uint64Token(1)); err != nil {
		t.Fatalf("gates.Open: %v", err)
	}

	// Turnos 1..3 — 200 tokens cada. Ao 3.º a fracção é 600/1000 = 0.60 < 0.80.
	for turn := 1; turn <= 3; turn++ {
		gravaTurno(t, h.rec, run, "step-"+string(rune('0'+turn)), turn, 150, 50, 0)
		if err := h.prog.ObserveProgress(ctx, run, turn); err != nil {
			t.Fatalf("ObserveProgress(turno %d): %v", turn, err)
		}
	}
	if n := len(h.tracer.SpansByOperation(progresssurface.OpBudgetWarning)); n != 0 {
		t.Fatalf("60%% esta abaixo do limiar — nao devia avisar (%d spans)", n)
	}

	// Turnos 4 e 5 — cruzam o limiar (800/1000 e 1000/1000) e mantêm-se acima.
	for turn := 4; turn <= 5; turn++ {
		gravaTurno(t, h.rec, run, "step-"+string(rune('0'+turn)), turn, 150, 50, 0)
		if err := h.prog.ObserveProgress(ctx, run, turn); err != nil {
			t.Fatalf("ObserveProgress(turno %d): %v", turn, err)
		}
	}
	spans := h.tracer.SpansByOperation(progresssurface.OpBudgetWarning)
	if len(spans) != 1 {
		t.Fatalf("o aviso tem de ser emitido UMA VEZ por run (latch), got %d", len(spans))
	}
	// AC3 — VISÍVEL NO CANAL DE LEITURA EXISTENTE. O span acima só tem destino com
	// AOS_OTLP_ENDPOINT definida (sem ela o tracer do nó é o NoopTracer); o LOG DO NÓ
	// existe sempre, e é lá que o operador da configuração por omissão vê o aviso. Mesmo
	// latch: uma linha, não uma por turno acima do limiar.
	if n := aos262AvisosNoLog(h.logs.String()); n != 1 {
		t.Fatalf("o aviso tem de sair UMA VEZ no LOG DO NO (a superficie que existe sem OTLP), got %d:\n%s", n, h.logs.String())
	}
	for _, marcador := range []string{"turno 4", "800", "NAO para o run"} {
		if !strings.Contains(h.logs.String(), marcador) {
			t.Errorf("a linha de aviso do log devia conter %q:\n%s", marcador, h.logs.String())
		}
	}
	if spans[0].Attributes[otelgenai.AttrRunID] != run {
		t.Fatalf("aviso sem run_id: %+v", spans[0].Attributes)
	}
	if spans[0].Attributes[progresssurface.AttrWarningTurn] != int64(4) {
		t.Fatalf("o aviso devia sair no turno 4 (o primeiro >= 80%%): %+v", spans[0].Attributes)
	}
	if spans[0].Attributes[progresssurface.AttrConsumedTokens] != int64(800) {
		t.Fatalf("o aviso devia trazer os 800 tokens LIDOS DO LEDGER: %+v", spans[0].Attributes)
	}
	// O ProgressReflector node-local tem produtor para AMBOS os campos: o State vem da
	// state.Machine REAL do run, o Step do turno que o loop passou.
	if got := spans[0].Attributes[progresssurface.AttrProgressStep]; got != "chat#4" {
		t.Fatalf("o Step do progresso devia ser chat#4 (tem produtor: o turno), got %v", got)
	}
	if got := spans[0].Attributes[progresssurface.AttrProgressState]; got != string(state.Ready) {
		t.Fatalf("o State devia vir da state.Machine do run (ready, ainda sem claim), got %v", got)
	}

	// (a) A entrega NÃO apresenta opções de decisão: nenhum span de prompt/decisão.
	for _, op := range []string{progresssurface.OpExhaustionPrompt, progresssurface.OpExhaustionDecision} {
		if n := len(h.tracer.SpansByOperation(op)); n != 0 {
			t.Fatalf("a primeira entrega nao apresenta decisao: %d spans %s", n, op)
		}
	}

	// (b) O forget do fim do run liberta o latch E o cursor da fonte.
	h.prog.forget(run)
	gravaTurno(t, h.rec, run, "step-6", 6, 150, 50, 0)
	if err := h.prog.ObserveProgress(ctx, run, 6); err != nil {
		t.Fatalf("ObserveProgress apos forget: %v", err)
	}
	if n := len(h.tracer.SpansByOperation(progresssurface.OpBudgetWarning)); n != 2 {
		t.Fatalf("apos o forget o run devia poder voltar a avisar uma vez, got %d spans", n)
	}
	if n := aos262AvisosNoLog(h.logs.String()); n != 2 {
		t.Fatalf("apos o forget o log do no devia levar a segunda linha de aviso, got %d:\n%s", n, h.logs.String())
	}
}

// --- (3-bis) M1: TRANSITÓRIO vs CEGUEIRA na leitura do burn-down --------------------

// storeInstavel devolve [eventstore.ErrNoQuorum] nas primeiras `falhas` leituras e delega no
// store real a partir daí. Reproduz a perda/troca de líder do Event Store — o modo de
// indisponibilidade que o `Read` real tem (`store.go`, `s.leader() == nil`).
type storeInstavel struct {
	inner  turnLedgerStore
	falhas int
}

func (s *storeInstavel) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	if s.falhas > 0 {
		s.falhas--
		return nil, eventstore.ErrNoQuorum
	}
	return s.inner.Read(ctx, streamID, fromSeq)
}

// novoAOS262HarnessComStore é [novoAOS262Harness] com a fonte a ler de `store` em vez do
// Event Store directo — para injectar a indisponibilidade.
func novoAOS262HarnessComStore(t *testing.T, maxTokens int64, threshold float64, embrulha func(turnLedgerStore) turnLedgerStore) *aos262Harness {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rb, err := integration.NewRunBudget(maxTokens)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	logs := &syncBuf{}
	log := func(format string, args ...any) { fmt.Fprintf(logs, "[aos] "+format+"\n", args...) }
	prog := newRunProgress(newRunStateGates(es, nil, 0), rb, newTurnLedgerBurndown(embrulha(es)), tracer, threshold, log, nil)
	if prog == nil {
		t.Fatal("o observador tinha de ser composto")
	}
	return &aos262Harness{prog: prog, rec: agentruntime.NewTurnRecorder(es), tracer: tracer, logs: logs}
}

// TestAOS262_IndisponibilidadeTransitoriaNaoMataORun — a porta do kernel diz que «erros
// TRANSITÓRIOS não devem chegar aqui» e o adaptador do nó propagava TODOS, incluindo o
// [eventstore.ErrNoQuorum] de uma troca de líder. Uma feature de LEITURA PURA passava assim
// a ser um caminho de TERMINAÇÃO de runs saudáveis.
//
// Prova das duas metades: a janela de indisponibilidade é tolerada E o consumo NÃO se perde
// (o cursor não avançou, pelo que a leitura seguinte soma tudo).
func TestAOS262_IndisponibilidadeTransitoriaNaoMataORun(t *testing.T) {
	h := novoAOS262HarnessComStore(t, 1000, 0.80, func(inner turnLedgerStore) turnLedgerStore {
		return &storeInstavel{inner: inner, falhas: maxLeiturasTransitoriasToleradas}
	})
	ctx := context.Background()
	const run = "run-262-sem-quorum"

	gravaTurno(t, h.rec, run, "step-1", 1, 150, 50, 0)
	for turno := 1; turno <= maxLeiturasTransitoriasToleradas; turno++ {
		if err := h.prog.ObserveProgress(ctx, run, turno); err != nil {
			t.Fatalf("a %da leitura sem quorum NAO pode matar o run: %v", turno, err)
		}
	}
	if !strings.Contains(h.logs.String(), "leitura ADIADA") {
		t.Errorf("a indisponibilidade tolerada tem de ficar VISIVEL no log (tolerar em silencio e outra forma de cegueira):\n%s", h.logs.String())
	}

	// O store recuperou: a leitura seguinte tem de somar o turno que ficou por contar.
	gravaTurno(t, h.rec, run, "step-2", 2, 600, 0, 0)
	if err := h.prog.ObserveProgress(ctx, run, 2); err != nil {
		t.Fatalf("com o store recuperado a leitura tem de correr: %v", err)
	}
	spans := h.tracer.SpansByOperation(progresssurface.OpBudgetWarning)
	if len(spans) != 1 {
		t.Fatalf("800/1000 esta acima do limiar — o aviso devia sair 1x, got %d spans (se o consumo do turno adiado se tivesse PERDIDO, nao saia)", len(spans))
	}
	if spans[0].Attributes[progresssurface.AttrConsumedTokens] != int64(800) {
		t.Fatalf("o turno adiado tem de continuar a contar (o cursor nao avancou numa leitura falhada): %+v", spans[0].Attributes)
	}
}

// TestAOS262_IndisponibilidadePersistenteVoltaAMatarORun — a outra ponta: tolerar SEMPRE
// devolveria a cegueira permanente pela porta das traseiras (um store que nunca mais atinge
// quórum daria um run inteiro sem burn-down, com o banner a prometer o aviso).
func TestAOS262_IndisponibilidadePersistenteVoltaAMatarORun(t *testing.T) {
	h := novoAOS262HarnessComStore(t, 1000, 0.80, func(inner turnLedgerStore) turnLedgerStore {
		return &storeInstavel{inner: inner, falhas: maxLeiturasTransitoriasToleradas + 1}
	})
	ctx := context.Background()
	const run = "run-262-quorum-perdido"
	gravaTurno(t, h.rec, run, "step-1", 1, 150, 50, 0)

	for turno := 1; turno <= maxLeiturasTransitoriasToleradas; turno++ {
		if err := h.prog.ObserveProgress(ctx, run, turno); err != nil {
			t.Fatalf("dentro da tolerancia a leitura nao pode matar o run (turno %d): %v", turno, err)
		}
	}
	err := h.prog.ObserveProgress(ctx, run, maxLeiturasTransitoriasToleradas+1)
	if err == nil {
		t.Fatal("passada a tolerancia a indisponibilidade deixa de ser transitoria: o run TEM de abortar, senao o burn-down fica cego para sempre com o banner a prometer o aviso")
	}
	if !errors.Is(err, eventstore.ErrNoQuorum) {
		t.Fatalf("o erro tem de subir identificavel (a causa e do substrato): %v", err)
	}
	if !strings.Contains(err.Error(), "cegueira") {
		t.Errorf("a mensagem tem de dizer PORQUE deixou de ser tolerado: %v", err)
	}
}

// TestAOS262_CegueiraContinuaAMatarORunAoPrimeiroErro — a classificação não pode ter
// abrandado a postura: um erro de AUSÊNCIA DE DADOS aborta o run à PRIMEIRA, sem tolerância
// nenhuma. É o critério duro de AOS-261 e não se negoceia com a janela de M1.
func TestAOS262_CegueiraContinuaAMatarORunAoPrimeiroErro(t *testing.T) {
	h := novoAOS262Harness(t, 1000, 0.80)
	if err := h.prog.ObserveProgress(context.Background(), "run-sem-ledger-nenhum", 1); !errors.Is(err, ErrBurndownNoLedger) {
		t.Fatalf("a ausencia de ledger nao e transitoria — tem de abortar a primeira: %v", err)
	}
}

// TestAOS262_ObservadorSemLedgerFalhaEmVezDeMentir — o critério duro de AOS-261 visto do
// lado do observador: um run cujo turno NÃO está no ledger que a fonte lê (o sintoma de um
// recorder ligado a outro Event Store) faz a observação FALHAR. Sem isto, o loop corria com
// o burn-down cego e o banner a prometer o aviso.
func TestAOS262_ObservadorSemLedgerFalhaEmVezDeMentir(t *testing.T) {
	h := novoAOS262Harness(t, 1000, 0.80)

	err := h.prog.ObserveProgress(context.Background(), "run-sem-ledger", 1)
	if !errors.Is(err, ErrBurndownNoLedger) {
		t.Fatalf("sem ledger a observacao tem de FALHAR com ErrBurndownNoLedger, got %v", err)
	}
}

// TestAOS262_LeituraNaoMutaOOrcamento — a superfície LÊ. O headroom do run tem de ficar
// exactamente onde estava depois de várias observações (o enforcement é do hook de
// mediação, não desta leitura).
func TestAOS262_LeituraNaoMutaOOrcamento(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rb, err := integration.NewRunBudget(1000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	prog := newRunProgress(newRunStateGates(es, nil, 0), rb, newTurnLedgerBurndown(es), nil, 0.80, nil, nil)
	rec := agentruntime.NewTurnRecorder(es)
	const run = "run-262-leitura-pura"

	gravaTurno(t, rec, run, "step-1", 1, 900, 0, 0)
	antes, _ := rb.AvailableTokens(run) // sem nó vivo: (0,false) — o que importa é a igualdade
	for turn := 1; turn <= 3; turn++ {
		if err := prog.ObserveProgress(context.Background(), run, turn); err != nil {
			t.Fatalf("ObserveProgress: %v", err)
		}
	}
	depois, _ := rb.AvailableTokens(run)
	if antes != depois {
		t.Fatalf("a leitura do burn-down MUTOU o orcamento: antes %d, depois %d", antes, depois)
	}
	if got := rb.MaxTokensPerRun(); got != 1000 {
		t.Fatalf("o tecto nao pode ser mexido pela leitura: %d", got)
	}
}

// --- (3-ter) A PROVA PONTA-A-PONTA: um run do NÓ REAL que avisa -------------------

// aos262ModeloQueimador queima `porTurno` tokens por turno e conclui no turno `final`.
//
// Os turnos NÃO-terminais têm de emitir uma tool call: o loop termina em
// `resp.Final || len(resp.ToolCalls) == 0` (loop.go), pelo que um modelo só de texto acabava
// ao turno 1 e o burn-down nunca chegaria ao limiar. A tool NÃO está registada no RM — é
// negada por default-deny, que é irrelevante para o que se mede aqui (o burn-down conta
// TURNOS DE MODELO) e mantém o loop a andar. O Input VARIA por turno para o sinal de
// no-progress do disjuntor não ver a mesma acção repetida.
type aos262ModeloQueimador struct {
	porTurno int64
	final    int
	turnos   int
}

func (m *aos262ModeloQueimador) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.turnos++
	if m.turnos >= m.final {
		return agentruntime.ModelResponse{
			Text: "concluido", Final: true,
			Usage: agentruntime.Usage{InputTokens: m.porTurno},
		}, nil
	}
	return agentruntime.ModelResponse{
		Text: "a trabalhar",
		ToolCalls: []agentruntime.ToolInvocation{{
			ToolID:     "tool-inexistente",
			Capability: "cap:fs.read",
			Input:      []byte(fmt.Sprintf(`{"passo":%d}`, m.turnos)),
		}},
		Usage: agentruntime.Usage{InputTokens: m.porTurno},
	}, nil
}

// TestAOS262_RunDoNoRealAvisaUmaVez é a prova que faltava a AOS-262: um [Bootstrap] REAL,
// com o tecto e o limiar entrados pela ENV DO OPERADOR, um run pelo [SecuredRuntime] do nó —
// e o aviso a sair, UMA VEZ, na superfície que o operador tem.
//
// Antes disto a amarra loop⇄observador era uma asserção sobre o TEXTO de `bootstrap.go`
// (TestAOS262_BannerDerivaDoObservadorComposto). Um teste de texto não distingue «o
// observador está composto» de «o observador está composto e o aviso chega a algum lado»: é
// a diferença que M2 nomeou, e que só um run real fecha.
//
// NOTA sobre a superfície: este nó NÃO tem AOS_OTLP_ENDPOINT, logo o tracer é o
// [otelgenai.NoopTracer] e o span do aviso não tem destino. A prova é o LOG, de propósito —
// é a configuração por omissão de quem liga o tecto, e era exactamente aí que o nó prometia
// avisar sem ter onde.
func TestAOS262_RunDoNoRealAvisaUmaVez(t *testing.T) {
	aos262LimparEnvs(t)
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "1000")
	t.Setenv("AOS_PROGRESS_THRESHOLD", "0.80")

	cfg := tnBaseConfig()
	// 450 tokens/turno: 0.45, 0.90 — o limiar é cruzado no TURNO 2, e o turno 3 conclui (um
	// run terminal retorna ANTES do observador, pelo que o aviso do turno 2 é o único).
	cfg.Model = &aos262ModeloQueimador{porTurno: 450, final: 3}

	var out syncBuf
	node, err := Bootstrap(context.Background(), cfg, &out)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.progress == nil {
		t.Fatal("com AOS_BUDGET_MAX_TOKENS definida o observador TEM de estar composto")
	}
	// O banner já saiu para `out`; a partir daqui só interessam as linhas do run.
	inicio := len(out.String())

	const run = "run-262-ponta-a-ponta"
	if _, _, err := node.Runtime.Run(context.Background(), steerGoal(run), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	saida := out.String()[inicio:]
	if n := aos262AvisosNoLog(saida); n != 1 {
		t.Fatalf("o run devia produzir EXACTAMENTE 1 aviso de burn-down no log do no, got %d:\n%s", n, saida)
	}
	for _, marcador := range []string{run, "turno 2", "900 de 1000 tokens"} {
		if !strings.Contains(saida, marcador) {
			t.Errorf("a linha de aviso devia conter %q (correlacao (run,turno) e os numeros LIDOS DO LEDGER):\n%s", marcador, saida)
		}
	}
}

// --- (4) POSTURA ANUNCIADA = POSTURA LIGADA ----------------------------------------

func TestAOS262_BannerDerivaDoObservadorComposto(t *testing.T) {
	t.Parallel()

	fonte, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	src := string(fonte)

	if !strings.Contains(src, "burndownPostureBanner(progress != nil, progressThreshold)") {
		t.Error("o composition-root tem de chamar burndownPostureBanner(progress != nil, ...) — o argumento e o ESTADO composto, nunca um literal")
	}
	if strings.Contains(src, "burndownPostureBanner(false") || strings.Contains(src, "burndownPostureBanner(true") {
		t.Error("burndownPostureBanner voltou a ser chamado com um literal no composition-root")
	}
	// O MESMO observador que o banner descreve tem de ser o que o loop consulta.
	if !strings.Contains(strings.ReplaceAll(src, " ", ""), "agentruntime.WithProgressObserver(progress.observer())") {
		t.Error("o observador declarado no banner tem de ser o entregue a agentruntime.WithProgressObserver")
	}
}

// TestAOS262_BannerNaoPrometeDecisaoNemTotal sela as DUAS afirmações que o banner não pode
// fazer: que o aviso decide alguma coisa, e que o burn-down é o consumo TOTAL do run.
func TestAOS262_BannerNaoPrometeDecisaoNemTotal(t *testing.T) {
	t.Parallel()

	composto := strings.Join(burndownPostureBanner(true, 0.80), "\n")
	for _, marcador := range []string{
		"COMPOSTO",
		"LEDGER DE TURNOS",      // a fonte, nomeada
		"LIMITE INFERIOR",       // o alcance: nao e o total
		"NAO DECIDE NADA",       // a abstinencia
		"AOS_BUDGET_MAX_TOKENS", // o tecto de que depende
	} {
		if !strings.Contains(composto, marcador) {
			t.Errorf("o banner do estado composto devia conter %q:\n%s", marcador, composto)
		}
	}
	// A abstinência tem de estar NEGADA de forma explícita, não apenas omitida: quem lê o
	// banner precisa de saber que as opções existem no desenho-alvo e NÃO estão ligadas.
	if !strings.Contains(composto, "NAO apresenta extend/summarize_stop/abort") {
		t.Errorf("o banner tem de NEGAR explicitamente as opcoes de decisao:\n%s", composto)
	}
	// E tem de dizer QUEM pára um run, para o aviso não ser confundido com um travão.
	if !strings.Contains(composto, "Quem para um run e o disjuntor") {
		t.Errorf("o banner tem de nomear quem PARA um run (nao e o aviso):\n%s", composto)
	}

	naoComposto := strings.Join(burndownPostureBanner(false, 0.80), "\n")
	if !strings.Contains(naoComposto, "NAO COMPOSTO") {
		t.Errorf("o banner tem de declarar NAO COMPOSTO neste estado:\n%s", naoComposto)
	}
	if !strings.Contains(naoComposto, "AOS_BUDGET_MAX_TOKENS") {
		t.Errorf("o banner devia nomear a variavel que LIGA o burn-down:\n%s", naoComposto)
	}
}
