package main

// AOS-261 — a FONTE do burn-down no nó: o ledger de turnos.
//
// Estes testes selam as três propriedades que motivaram a escolha do LEDGER em vez de um
// decorador de Exporter que retém SpanData:
//
//  1. IMUNIDADE À RE-EMISSÃO — um turno gravado duas vezes com o mesmo (run_id, step_id) é
//     UMA entrada, e o burn-down não infla. Um retentor de spans contaria 2×.
//  2. RETOMA (multi-incarnação) — o prefixo T1 continua a contar depois de o cursor ser
//     esquecido (o equivalente a um restart do processo), e a reprodução T2 não duplica.
//     Agregar por trace_id zerava aqui.
//  3. FAIL-CLOSED — sem ledger, ou com ledger vazio, a leitura devolve ERRO. Nunca 0%.
//
// E, transversalmente, sela o CONTRATO DO PAYLOAD: a soma é feita sobre um evento gravado
// pelo [agentruntime.TurnRecorder] REAL. Se um nome de campo JSON mudar do lado do produtor,
// a soma cai a zero e (1) avermelha.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// gravaTurno grava um turno REAL pelo TurnRecorder do kernel (não um Append à mão): é o que
// torna o teste uma prova do CONTRATO e não da nossa própria struct.
func gravaTurno(t *testing.T, rec *agentruntime.TurnRecorder, runID, stepID string, turn int, in, out, cost int64) {
	t.Helper()
	if _, err := rec.Record(context.Background(), agentruntime.TurnRecord{
		RunID:        runID,
		StepID:       stepID,
		Turn:         turn,
		Usage:        agentruntime.Usage{InputTokens: in, OutputTokens: out},
		CostMicroUSD: cost,
		Producer:     eventstore.Producer{NHIID: "nhi:teste-aos261"},
	}); err != nil {
		t.Fatalf("Record(turno %d): %v", turn, err)
	}
}

// TestAOS261_FonteLeOLedgerRealDoTurnRecorder — a fonte soma o que o produtor REAL gravou, e
// uma RE-EMISSÃO do mesmo turno não infla a contagem.
func TestAOS261_FonteLeOLedgerRealDoTurnRecorder(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()

	rec := agentruntime.NewTurnRecorder(es)
	const run = "run-261-a"
	gravaTurno(t, rec, run, "step-1", 1, 100, 50, 1_200_000)
	gravaTurno(t, rec, run, "step-2", 2, 200, 30, 800_000)

	src := newTurnLedgerBurndown(es)
	got, err := src.ConsumedByRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ConsumedByRun: %v", err)
	}
	if got.Consumed.Tokens != 380 {
		t.Fatalf("tokens do ledger: got %d, esperado 380 (100+50+200+30) — um campo JSON do turnPayload pode ter mudado", got.Consumed.Tokens)
	}
	if got.Consumed.CostMicroUSD != 2_000_000 {
		t.Fatalf("custo do ledger: got %d, esperado 2000000", got.Consumed.CostMicroUSD)
	}
	if got.Turns != 2 || got.LastTurn != 2 {
		t.Fatalf("turnos somados: %+v", got)
	}

	// (1) RE-EMISSÃO: o MESMO (run_id, step_id) outra vez. O Event Store deduplica pela
	// idempotency_key, pelo que o total NÃO muda. É a propriedade que um retentor de
	// SpanData não teria — ele contaria o span duas vezes.
	gravaTurno(t, rec, run, "step-2", 2, 200, 30, 800_000)
	depois, err := src.ConsumedByRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ConsumedByRun após re-emissão: %v", err)
	}
	if depois.Consumed.Tokens != got.Consumed.Tokens || depois.Turns != got.Turns {
		t.Fatalf("a re-emissão do mesmo turno INFLOU o burn-down: antes %+v, depois %+v", got, depois)
	}
}

// TestAOS261_Retoma_ConsumoCumulativoSobreOPrefixo — a política multi-incarnação.
//
// T1 grava dois turnos e o run "morre" (o cursor é esquecido — o mesmo efeito de um
// restart do processo, que perde toda a memória mas não o stream). T2 retoma no MESMO
// run_id: a reprodução do prefixo NÃO acrescenta entradas (dedup por step_id) e o trabalho
// NOVO soma-se ao que T1 já gastou.
func TestAOS261_Retoma_ConsumoCumulativoSobreOPrefixo(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()

	rec := agentruntime.NewTurnRecorder(es)
	const run = "run-261-retoma"
	src := newTurnLedgerBurndown(es)
	ctx := context.Background()

	// T1 — duas iterações, com leitura pelo meio (exercita o cursor incremental).
	gravaTurno(t, rec, run, "step-1", 1, 100, 0, 0)
	if _, err := src.ConsumedByRun(ctx, run); err != nil {
		t.Fatalf("leitura T1: %v", err)
	}
	gravaTurno(t, rec, run, "step-2", 2, 100, 0, 0)
	t1, err := src.ConsumedByRun(ctx, run)
	if err != nil {
		t.Fatalf("leitura T1 final: %v", err)
	}
	if t1.Consumed.Tokens != 200 || t1.Turns != 2 {
		t.Fatalf("T1: %+v", t1)
	}

	// Fim da incarnação: o composition root esquece o run (service.go hostRun).
	src.forget(run)

	// T2 — a retoma REPRODUZ os turnos 1 e 2 (mesmo step_id ⇒ duplicados, sem escrita) e
	// acrescenta o turno 3.
	gravaTurno(t, rec, run, "step-1", 1, 100, 0, 0)
	gravaTurno(t, rec, run, "step-2", 2, 100, 0, 0)
	gravaTurno(t, rec, run, "step-3", 3, 50, 0, 0)

	t2, err := src.ConsumedByRun(ctx, run)
	if err != nil {
		t.Fatalf("leitura T2: %v", err)
	}
	if t2.Consumed.Tokens != 250 {
		t.Fatalf("a retoma tem de ser CUMULATIVA sobre o prefixo T1 e NÃO duplicar a reprodução: esperado 250 tokens, got %d (%+v)", t2.Consumed.Tokens, t2)
	}
	if t2.Turns != 3 || t2.LastTurn != 3 {
		t.Fatalf("T2: %+v", t2)
	}
}

// TestAOS261_SemLedger_ErroExplicitoNuncaZero — o critério DURO do ticket, nos dois
// sabores de ausência.
func TestAOS261_SemLedger_ErroExplicitoNuncaZero(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()
	src := newTurnLedgerBurndown(es)
	ctx := context.Background()

	// (a) Stream inexistente — o run nunca gravou nada.
	got, err := src.ConsumedByRun(ctx, "run-que-nunca-existiu")
	if !errors.Is(err, ErrBurndownNoLedger) {
		t.Fatalf("stream inexistente devia dar ErrBurndownNoLedger, got err=%v consumo=%+v", err, got)
	}
	if got.Consumed.Tokens != 0 || got.Turns != 0 {
		t.Fatalf("o caminho de erro não pode devolver leitura nenhuma: %+v", got)
	}

	// (b) Stream EXISTE mas sem NENHUM turn.recorded — o sintoma de um recorder ligado a
	// outro Event Store. É o modo de falha mais perigoso, porque a soma daria 0 sem erro.
	const run = "run-261-sem-turnos"
	if _, err := es.Append(ctx, run, eventstore.EventInput{
		Type:     "algo.que.nao.e.turno",
		RunID:    run,
		StepID:   "s1",
		Producer: eventstore.Producer{NHIID: "nhi:teste-aos261"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := src.ConsumedByRun(ctx, run); !errors.Is(err, ErrBurndownNoLedger) {
		t.Fatalf("stream sem turnos devia dar ErrBurndownNoLedger (nunca 0%%), got %v", err)
	}
}

// TestAOS261_TurnosSemTokens_ErroExplicitoNuncaZero — o ZERO SILENCIOSO que sobrevivia ao
// guarda posto na CONTAGEM DE TURNOS.
//
// Um stream com N eventos `turn.recorded` TODOS com usage a zero (o provider do modelo não
// ecoou `usage` — o `translateResponse` do gateway só preenche o que o provider devolver)
// dava `RunConsumption{Consumed:{0,0}, Turns:N}` com `err == nil`. A fracção saía 0, o run
// queimava o tecto inteiro e o aviso nunca disparava — exactamente a superfície verde a
// mentir que AOS-261 existe para remover, um nível abaixo de onde o guarda estava.
//
// A guarda tem de estar na GRANDEZA QUE DECIDE (tokens), não na contagem: é ela que alimenta
// `consumedFraction`.
func TestAOS261_TurnosSemTokens_ErroExplicitoNuncaZero(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()
	rec := agentruntime.NewTurnRecorder(es)
	src := newTurnLedgerBurndown(es)
	const run = "run-261-sem-usage"

	// Três turnos REAIS, gravados pelo produtor REAL, com usage a ZERO nas duas dimensões.
	for turno := 1; turno <= 3; turno++ {
		gravaTurno(t, rec, run, "step-"+strconv.Itoa(turno), turno, 0, 0, 0)
	}

	got, err := src.ConsumedByRun(context.Background(), run)
	if !errors.Is(err, ErrBurndownNoUsage) {
		t.Fatalf("3 turnos com 0 tokens deviam dar ErrBurndownNoUsage (nunca 0%% em silencio), got err=%v consumo=%+v", err, got)
	}
	if got.Consumed.Tokens != 0 || got.Turns != 0 {
		t.Fatalf("o caminho de erro nao pode devolver leitura nenhuma: %+v", got)
	}
	// O erro tem de dizer QUANTOS turnos foram somados — é o que distingue «não há ledger»
	// de «há ledger e não tem a grandeza», que têm remédios diferentes.
	if !strings.Contains(err.Error(), "3 turno") {
		t.Errorf("o erro devia nomear os turnos somados (o sintoma que aponta ao provider, nao ao wiring do store): %v", err)
	}

	// NÃO-VACUOSIDADE: basta UM turno com tokens para a leitura passar a ser válida — a
	// guarda não pode ser um "nega sempre" disfarçado.
	gravaTurno(t, rec, run, "step-4", 4, 10, 5, 0)
	ok, err := src.ConsumedByRun(context.Background(), run)
	if err != nil {
		t.Fatalf("com tokens no ledger a leitura tem de passar: %v", err)
	}
	if ok.Consumed.Tokens != 15 || ok.Turns != 4 {
		t.Fatalf("a leitura valida tem de somar TODOS os turnos (incl. os de zero): %+v", ok)
	}
}

// TestAOS261_ErroDoStoreNaoViraConsumoZero — um store fechado (ou um contexto cancelado) é
// uma leitura FALHADA, não um consumo de zero.
func TestAOS261_ErroDoStoreNaoViraConsumoZero(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rec := agentruntime.NewTurnRecorder(es)
	const run = "run-261-fechado"
	gravaTurno(t, rec, run, "step-1", 1, 100, 0, 0)
	src := newTurnLedgerBurndown(es)
	if _, err := src.ConsumedByRun(context.Background(), run); err != nil {
		t.Fatalf("leitura antes do fecho: %v", err)
	}
	if err := es.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := src.ConsumedByRun(context.Background(), run)
	if err == nil {
		t.Fatalf("store fechado devia dar erro, got consumo=%+v", got)
	}
	if !errors.Is(err, eventstore.ErrClosed) {
		t.Fatalf("o erro do store tem de subir identificável, got %v", err)
	}
}

// TestAOS261_RetencaoDoStoreEAFonteDaFonte — a fonte NÃO tem política de retenção própria:
// lê o que o Event Store retém. É a terceira razão da escolha (não se inventa um subsistema
// de retenção em memória, com o seu próprio despejo silencioso).
//
// Prova-o pelo lado observável: um store DURÁVEL reaberto (o equivalente a um restart do
// nó) continua a servir o burn-down do run, com o mesmo total — coisa que um retentor de
// SpanData em memória não conseguiria, porque o processo levou os spans consigo.
func TestAOS261_RetencaoDoStoreEAFonteDaFonte(t *testing.T) {
	path := t.TempDir() + "/events.wal"
	const run = "run-261-duravel"

	es, err := eventstore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gravaTurno(t, agentruntime.NewTurnRecorder(es), run, "step-1", 1, 300, 100, 0)
	if err := es.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reabertura: outro processo, outra incarnação, mesmo ledger.
	es2, err := eventstore.Open(path)
	if err != nil {
		t.Fatalf("reOpen: %v", err)
	}
	defer func() { _ = es2.Close() }()

	got, err := newTurnLedgerBurndown(es2).ConsumedByRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ConsumedByRun após reabertura: %v", err)
	}
	if got.Consumed.Tokens != 400 || got.Turns != 1 {
		t.Fatalf("o burn-down tem de sobreviver ao restart (retenção é a do store): %+v", got)
	}
}
