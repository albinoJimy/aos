package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// AOS-336 — O RUN MISTO. É o caso que o guarda anterior deixava passar, e é o realista.
//
// `ErrBurndownNoUsage` testava `turns > 0 && turnTokens == 0` sobre um cursor CUMULATIVO
// por run: um único turno com usage desarmava-o PARA SEMPRE, e todos os turnos não medidos
// seguintes contavam zero em silêncio. Não apanhava «ao fim de N turnos» — só apanhava o
// run inteiramente cego. Um provider intermitente, ou um par (modelo, região) que sai da
// tabela de preços a meio de um run, produz precisamente o misto.

// gravaTurnoNaoMedido grava um turno que o gateway marcou como NÃO MEDIDO — o que o
// `translateResponse` passa a projectar desde AOS-336. Usa o produtor REAL: é o que torna
// isto uma prova do contrato do evento e não da nossa struct de leitura.
func gravaTurnoNaoMedido(t *testing.T, rec *agentruntime.TurnRecorder, runID, stepID string, turn int) {
	t.Helper()
	if _, err := rec.Record(context.Background(), agentruntime.TurnRecord{
		RunID:    runID,
		StepID:   stepID,
		Turn:     turn,
		Usage:    agentruntime.Usage{Ausente: true},
		Producer: eventstore.Producer{NHIID: "nhi:teste-aos336"},
	}); err != nil {
		t.Fatalf("Record(turno %d): %v", turn, err)
	}
}

func TestAOS336_RunMistoNaoContaOsTurnosNaoMedidosComoZero(t *testing.T) {
	casos := []struct {
		nome   string
		grava  func(t *testing.T, rec *agentruntime.TurnRecorder, run string)
		semUso int
		total  int
		porque string
	}{
		{
			nome: "o turno nao medido vem DEPOIS dos medidos",
			grava: func(t *testing.T, rec *agentruntime.TurnRecorder, run string) {
				gravaTurno(t, rec, run, "s1", 1, 400, 100, 0)
				gravaTurno(t, rec, run, "s2", 2, 300, 80, 0)
				gravaTurnoNaoMedido(t, rec, run, "s3", 3)
			},
			semUso: 1, total: 3,
			porque: "o guarda antigo ja tinha 880 tokens somados e estava desarmado para sempre",
		},
		{
			nome: "o turno nao medido vem PRIMEIRO e o medido reabilitava-o",
			grava: func(t *testing.T, rec *agentruntime.TurnRecorder, run string) {
				gravaTurnoNaoMedido(t, rec, run, "s1", 1)
				gravaTurno(t, rec, run, "s2", 2, 500, 100, 0)
			},
			semUso: 1, total: 2,
			porque: "um turno medido a seguir a um cego zerava a suspeita sobre o cego",
		},
		{
			nome: "usage AUSENTE sem a marca, so pelos contadores a zero",
			grava: func(t *testing.T, rec *agentruntime.TurnRecorder, run string) {
				gravaTurno(t, rec, run, "s1", 1, 500, 100, 0)
				// Sem `Ausente`: é o que um produtor mais antigo — ou um que esqueça a
				// marca — grava. O criterio `input_tokens <= 0` tem de o apanhar na mesma.
				gravaTurno(t, rec, run, "s2", 2, 0, 0, 0)
			},
			semUso: 1, total: 2,
			porque: "a via defensiva nao pode depender de o produtor escrever a marca",
		},
		{
			nome: "varios nao medidos entre medidos",
			grava: func(t *testing.T, rec *agentruntime.TurnRecorder, run string) {
				gravaTurno(t, rec, run, "s1", 1, 200, 50, 0)
				gravaTurnoNaoMedido(t, rec, run, "s2", 2)
				gravaTurnoNaoMedido(t, rec, run, "s3", 3)
				gravaTurno(t, rec, run, "s4", 4, 200, 50, 0)
			},
			semUso: 2, total: 4,
			porque: "o erro tem de dizer QUANTOS turnos ficaram sem medicao",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			es, err := eventstore.New()
			if err != nil {
				t.Fatalf("eventstore.New: %v", err)
			}
			defer func() { _ = es.Close() }()

			const run = "run-336-misto"
			c.grava(t, agentruntime.NewTurnRecorder(es), run)

			got, err := newTurnLedgerBurndown(es).ConsumedByRun(context.Background(), run)
			if !errors.Is(err, ErrBurndownNoUsage) {
				t.Fatalf("err = %v, quer ErrBurndownNoUsage — %s; o burn-down contou %d tokens como se fossem o consumo do run", err, c.porque, got.Consumed.Tokens)
			}
			// O caminho de erro NAO pode devolver leitura nenhuma: uma soma parcial
			// apresentada como total e o mesmo defeito com outro nome.
			if got.Consumed.Tokens != 0 || got.Turns != 0 {
				t.Fatalf("o caminho de erro nao pode devolver leitura: %+v", got)
			}
			// O erro tem de NOMEAR a proporcao: e o que distingue «o provider esta mudo»
			// de «o provider falhou num turno», que tem remedios diferentes.
			quer := strconv.Itoa(c.semUso) + " de " + strconv.Itoa(c.total) + " turno"
			if !strings.Contains(err.Error(), quer) {
				t.Errorf("o erro devia conter %q: %v", quer, err)
			}
		})
	}
}

// TestAOS336_TodosOsTurnosMedidosContinuamAPassar e o CONTROLO. Sem ele, um guarda que
// negasse SEMPRE passaria todos os testes acima — e a negacao universal seria pior do que
// o defeito que se fecha, porque abortaria todos os runs saudaveis do no.
func TestAOS336_TodosOsTurnosMedidosContinuamAPassar(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()

	rec := agentruntime.NewTurnRecorder(es)
	const run = "run-336-todo-medido"
	gravaTurno(t, rec, run, "s1", 1, 400, 100, 700_000)
	gravaTurno(t, rec, run, "s2", 2, 300, 80, 500_000)
	gravaTurno(t, rec, run, "s3", 3, 100, 20, 200_000)

	got, err := newTurnLedgerBurndown(es).ConsumedByRun(context.Background(), run)
	if err != nil {
		t.Fatalf("um run inteiramente medido tem de passar: %v", err)
	}
	if got.Consumed.Tokens != 1000 || got.Turns != 3 {
		t.Fatalf("consumo = %+v, quer 1000 tokens em 3 turnos", got)
	}
	if got.Consumed.CostMicroUSD != 1_400_000 {
		t.Fatalf("custo = %d, quer 1400000", got.Consumed.CostMicroUSD)
	}
}

// TestAOS336_ACumulatividadeDoGuardaSobreviveAsLeituras — o burn-down e consultado em CADA
// fronteira de fim-de-turno sobre um cursor cumulativo. Um turno nao medido a meio nao pode
// ser esquecido pela leitura seguinte: e essa amnesia que fazia o guarda depender da ordem
// dos eventos e do momento em que alguem perguntava.
func TestAOS336_ACumulatividadeDoGuardaSobreviveAsLeituras(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()

	rec := agentruntime.NewTurnRecorder(es)
	src := newTurnLedgerBurndown(es)
	const run = "run-336-cumulativo"
	ctx := context.Background()

	gravaTurno(t, rec, run, "s1", 1, 400, 100, 0)
	if _, err := src.ConsumedByRun(ctx, run); err != nil {
		t.Fatalf("turno 1 medido: a leitura tem de passar: %v", err)
	}

	// O turno cego chega. A leitura desta fronteira denuncia-o …
	gravaTurnoNaoMedido(t, rec, run, "s2", 2)
	if _, err := src.ConsumedByRun(ctx, run); !errors.Is(err, ErrBurndownNoUsage) {
		t.Fatalf("err = %v, quer ErrBurndownNoUsage no turno cego", err)
	}

	// … e as fronteiras SEGUINTES continuam a denuncia-lo, mesmo com turnos medidos a
	// chegar por cima. Sem isto, o run recuperava sozinho de uma cegueira que nao passou.
	gravaTurno(t, rec, run, "s3", 3, 900, 200, 0)
	if _, err := src.ConsumedByRun(ctx, run); !errors.Is(err, ErrBurndownNoUsage) {
		t.Fatalf("err = %v, quer ErrBurndownNoUsage — um turno medido a seguir NAO apaga o cego", err)
	}
}
