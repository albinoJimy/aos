package agentruntime

// ---------------------------------------------------------------------------------------------
// AOS-336 — O EVENTO DURÁVEL REGISTA QUE O TURNO NÃO FOI MEDIDO.
//
// O `turn.recorded` é a fonte do burn-down do nó. Sem esta marca, um turno não medido e um turno
// gratuito gravam bytes IDÊNTICOS — e quem soma o ledger não tem por onde os distinguir. É o
// último passo de um canal que já existia dos dois lados: a marca nasce no wire do provedor
// (`port.Usage.Ausente`, AOS-321), atravessa em `translateResponse` (AOS-336) e tem de ficar
// DURÁVEL aqui, senão não sobrevive à fronteira de fim-de-turno que a lê.
// ---------------------------------------------------------------------------------------------

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

func aos336Payload(t *testing.T, rec *TurnRecorder, es *eventstore.Store, run string, u Usage, cost int64) map[string]any {
	t.Helper()
	if _, err := rec.Record(context.Background(), TurnRecord{
		RunID:        run,
		StepID:       "step-1",
		Turn:         1,
		Usage:        u,
		CostMicroUSD: cost,
		Producer:     eventstore.Producer{NHIID: "nhi:teste-aos336"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	evs, err := es.Read(context.Background(), run, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("eventos = %d, quer 1", len(evs))
	}
	var got map[string]any
	if err := json.Unmarshal(evs[0].Payload, &got); err != nil {
		t.Fatalf("payload ilegivel: %v", err)
	}
	return got
}

func aos336Store(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

func TestAOS336_TurnRecordedMarcaOTurnoNaoMedido(t *testing.T) {
	casos := []struct {
		nome   string
		usage  Usage
		porque string
	}{
		{"marca explicita do gateway", Usage{Ausente: true}, "a via autoritativa: o gateway sabe que o provedor nao reportou usage"},
		{"ausencia disfarcada de zeros", Usage{InputTokens: 0, OutputTokens: 0}, "nao existe turno de modelo sem entrada — zero prompt tokens e ausencia de leitura"},
		{"so tokens de saida", Usage{InputTokens: 0, OutputTokens: 120}, "saida sem entrada nao e uma medicao credivel do turno"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			es := aos336Store(t)
			got := aos336Payload(t, NewTurnRecorder(es), es, "run-336-"+c.nome, c.usage, 0)
			if got["usage_ausente"] != true {
				t.Fatalf("usage_ausente = %v, quer true — %s; payload: %v", got["usage_ausente"], c.porque, got)
			}
		})
	}
}

// TestAOS336_TurnoMedidoGravaOsMesmosBytesDeSempre é o CONTROLO, e é o que protege o replay.
//
// `usage_ausente` é `omitempty` DE PROPÓSITO: um turno medido tem de gravar exactamente o que
// gravava antes desta mudança, senão todo o golden de replay se move e a mudança deixa de ser
// aditiva. Sem este controlo, uma marca escrita sempre passaria os testes acima e partiria a
// fidelidade do replay em silêncio.
func TestAOS336_TurnoMedidoGravaOsMesmosBytesDeSempre(t *testing.T) {
	es := aos336Store(t)
	got := aos336Payload(t, NewTurnRecorder(es), es, "run-336-medido", Usage{InputTokens: 400, OutputTokens: 100}, 1_250_000)
	if _, presente := got["usage_ausente"]; presente {
		t.Fatalf("o campo apareceu num turno MEDIDO — o payload deixou de ser byte-identico ao de antes: %v", got)
	}
	if got["input_tokens"] != float64(400) || got["output_tokens"] != float64(100) || got["cost_micro_usd"] != float64(1_250_000) {
		t.Fatalf("os campos numericos mudaram: %v", got)
	}
}

// TestAOS336_ZeroMedidoEZeroIndefinidoDeixamDeSerOMesmoEvento é a AC em forma de teste: os dois
// gravam `cost_micro_usd: 0` e `input_tokens: 0`, e a partir daqui NÃO são o mesmo facto.
//
// NOTA SOBRE O CASO «medido»: um turno com `InputTokens > 0` e custo zero é o zero medido
// legítimo — houve leitura, e a tabela de preços produziu zero. É esse que tem de continuar a
// gravar-se sem marca.
func TestAOS336_ZeroMedidoEZeroIndefinidoDeixamDeSerOMesmoEvento(t *testing.T) {
	es := aos336Store(t)
	rec := NewTurnRecorder(es)
	medido := aos336Payload(t, rec, es, "run-336-zero-medido", Usage{InputTokens: 12}, 0)
	indefinido := aos336Payload(t, rec, es, "run-336-zero-indefinido", Usage{Ausente: true}, 0)

	if medido["cost_micro_usd"] != indefinido["cost_micro_usd"] {
		t.Fatalf("premissa partida: os dois deviam gravar o mesmo custo (0), got %v e %v", medido["cost_micro_usd"], indefinido["cost_micro_usd"])
	}
	if medido["usage_ausente"] == indefinido["usage_ausente"] {
		t.Fatal("o evento duravel nao distingue «custo zero medido» de «custo indefinido» — o burn-down nao tem por onde o fazer")
	}
}

// TestAOS336_DefinidoEOCriterioUnico impede que a marca e o critério divirjam: os dois lados da
// fronteira (o gateway que projecta e o recorder que grava) chamam `Usage.Definido()`, e é ele
// que tem de dizer a verdade sobre cada forma.
func TestAOS336_DefinidoEOCriterioUnico(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome  string
		usage Usage
		quer  bool
	}{
		{"medido", Usage{InputTokens: 1}, true},
		{"medido com saida a zero", Usage{InputTokens: 400, OutputTokens: 0}, true},
		{"marcado ausente apesar dos contadores", Usage{InputTokens: 400, OutputTokens: 100, Ausente: true}, false},
		{"vazio", Usage{}, false},
		{"so saida", Usage{OutputTokens: 120}, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			if got := c.usage.Definido(); got != c.quer {
				t.Errorf("Definido(%+v) = %v, quer %v", c.usage, got, c.quer)
			}
		})
	}
}
