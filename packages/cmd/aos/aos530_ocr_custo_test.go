package main

// AOS-530 (peça 3b) — O CUSTO DA `ocr_read`, peça a peça, sobre o gateway FALSO.
//
// O caminho composto (o gateway real, o orçamento do nó, a retoma, o replay, o JetStream) está em
// aos530_ocr_custo_no_test.go. Aqui provam-se as regras da tool: um evento por resposta paga — aceite
// ou recusada —, nenhum sem resposta; a forma do evento sem texto; a chave que não colide com a da
// mediação; a soma ao consumo durável; e a fórmula da reserva.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/substrate/eventstore"
)

// aos530Registos grava o que a tool manda gravar.
type aos530Registos struct {
	mu  sync.Mutex
	run []string
	p   []custoDeToolPayload
}

func (r *aos530Registos) registar(_ context.Context, runID string, p custoDeToolPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = append(r.run, runID)
	r.p = append(r.p, p)
	return nil
}

// UM EVENTO POR RESPOSTA PAGA, qualquer que seja o resultado da tool; NENHUM sem resposta do
// provider. O payload leva o custo, os tokens, o alias, o modelo servido e o resultado — nunca o
// texto do modelo.
func TestAOS530_Custo_UmEventoPorRespostaPaga(t *testing.T) {
	pasta := t.TempDir()
	if err := os.WriteFile(filepath.Join(pasta, "a.png"), aos530PNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	const segredo = "TEXTO-DO-MODELO-QUE-NAO-VAI-AO-EVENTO-530"
	semEscolhas := aos530Resposta("", port.FinishStop, 5)
	semEscolhas.Choices = nil
	comTool := aos530Resposta(segredo, port.FinishToolCalls, 6)
	comTool.Choices[0].Message.ToolCalls = []port.ToolCall{{ID: "c", Type: "function", Function: port.FunctionCall{Name: "x", Arguments: "{}"}}}
	recusaPaga := &modelgateway.PaidRejectionError{
		Err:         fmt.Errorf("gw: %w", &modelgateway.RouteVarianceError{Check: "diferente", Cause: modelgateway.RouteCauseModelDifferent}),
		Usage:       port.Usage{PromptTokens: 900, CompletionTokens: 40, CostMicroUSD: 91},
		ServedModel: "outro/modelo",
	}
	casos := []struct {
		nome      string
		gw        *aos530GwFalso
		resultado string
		custo     int64
		servido   string
		prazo     time.Duration
	}{
		{nome: "transcrito", gw: &aos530GwFalso{resp: aos530Resposta(segredo, port.FinishStop, 840)}, resultado: ocrResultadoTranscrito, custo: 840, servido: "openrouter/anthropic/modelo-de-visao"},
		{nome: "cortada", gw: &aos530GwFalso{resp: aos530Resposta(segredo, port.FinishLength, 77)}, resultado: ocrCausaCortado, custo: 77, servido: "openrouter/anthropic/modelo-de-visao"},
		{nome: "vazia", gw: &aos530GwFalso{resp: aos530Resposta(" ", port.FinishStop, 8)}, resultado: ocrCausaVazio, custo: 8, servido: "openrouter/anthropic/modelo-de-visao"},
		{nome: "sem escolhas", gw: &aos530GwFalso{resp: semEscolhas}, resultado: ocrCausaVazio, custo: 5, servido: "openrouter/anthropic/modelo-de-visao"},
		{nome: "tool call", gw: &aos530GwFalso{resp: comTool}, resultado: ocrCausaRespostaForma, custo: 6, servido: "openrouter/anthropic/modelo-de-visao"},
		{nome: "modelo servido diferente em enforce", gw: &aos530GwFalso{err: recusaPaga}, resultado: ocrCausaModeloServido, custo: 91, servido: "outro/modelo"},
		// Sem resposta não há custo conhecido: nenhum evento.
		{nome: "erro do gateway", gw: &aos530GwFalso{err: errors.New("provider 500")}, resultado: ""},
		{nome: "prazo esgotado", gw: &aos530GwFalso{esperar: true}, resultado: "", prazo: 20 * time.Millisecond},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			l := aos530LigacaoFalsa(t, pasta, c.gw)
			if c.prazo > 0 {
				l.prazo = c.prazo
			}
			reg := &aos530Registos{}
			l.ligarCusto(nil, reg.registar, nil)
			_, custo, _ := l.executar(aos530ComCredencial(), aos530Entrada("a.png"))
			if c.resultado == "" {
				if len(reg.p) != 0 || custo != 0 {
					t.Fatalf("sem resposta do provider nao ha evento de custo: %+v custo=%d", reg.p, custo)
				}
				return
			}
			if len(reg.p) != 1 || reg.run[0] != "run-530" || custo != c.custo {
				t.Fatalf("quer 1 evento do run-530 com custo %d; vieram %+v (custo devolvido %d)", c.custo, reg.p, custo)
			}
			p := reg.p[0]
			if p.Tool != ocrToolName || p.StepID != "passo-7" || p.Alias != "ocr-visao" || p.Resultado != c.resultado || p.CostMicroUSD != c.custo || p.ModeloServido != c.servido || p.InputTokens != 900 || p.OutputTokens != 40 {
				t.Fatalf("payload do evento: %+v", p)
			}
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), segredo) || strings.Contains(string(raw), "a.png") {
				t.Fatalf("o evento de custo nao leva texto do modelo nem o doc_id: %s", raw)
			}
		})
	}
	// As recusas ANTES do provider (sem credencial, limites) não gravam nada.
	l := aos530LigacaoFalsa(t, pasta, &aos530GwFalso{resp: aos530Resposta("x", port.FinishStop, 1)})
	reg := &aos530Registos{}
	l.ligarCusto(nil, reg.registar, nil)
	_, _, _ = l.executar(context.Background(), aos530Entrada("a.png"))
	_, _, _ = l.executar(aos530ComCredencial(), aos530Entrada("nao-existe.png"))
	if len(reg.p) != 0 {
		t.Fatalf("uma recusa antes do provider nao grava custo: %+v", reg.p)
	}
}

// A ESCRITA NO EVENT STORE: no stream do run, idempotente por (run_id, step_id) da tool call, e SEM
// colidir com o `tool.call.mediated` da mesma call — que, no stream do run, já tem a chave
// `run_id:step_id`. E o consumo durável soma-a, sem a tratar como turno.
func TestAOS530_Custo_EscritaIdempotenteSemColidirComAMediacao(t *testing.T) {
	ctx := context.Background()
	es := storeDeTeste(t)
	const run, passo = "run-530-custo", "step-000002-tool-1"
	// A mediação da mesma call, com a chave run:passo (é o que o RM grava no stream do run).
	if _, err := es.Append(ctx, run, eventstore.EventInput{Type: referencemonitor.EventTypeMediated, Payload: json.RawMessage(`{}`), RunID: run, StepID: passo}); err != nil {
		t.Fatal(err)
	}
	registar := registoDeCustoDeTool(es, producerDoCustoDeTool())
	p := custoDeToolPayload{Tool: ocrToolName, StepID: passo, Alias: "ocr-visao", Resultado: ocrResultadoTranscrito, InputTokens: 900, OutputTokens: 30, CostMicroUSD: 840}
	for i := 0; i < 2; i++ { // a segunda é a repetição do mesmo passo
		if err := registar(ctx, run, p); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := es.Read(ctx, run, 1)
	if err != nil {
		t.Fatal(err)
	}
	custos := aos530Custos(t, evs)
	if len(custos) != 1 || custos[0].envelope != ocrCustoStepPrefix+passo || custos[0].p != p {
		t.Fatalf("quer 1 evento de custo, com a chave do passo prefixada; vieram %+v", custos)
	}
	if len(evs) != 2 || evs[1].Producer.NHIID != toolModelCostNHI {
		t.Fatalf("a mediacao e o custo coexistem no stream: %+v", evs)
	}
	c, err := newTurnLedgerBurndown(es).ConsumedByRun(ctx, run)
	if err != nil {
		t.Fatalf("um run so com o custo de uma tool tem consumo legivel: %v", err)
	}
	if c.Consumed != (budget.Amount{Tokens: 930, CostMicroUSD: 840}) || c.Turns != 0 {
		t.Fatalf("o consumo duravel soma o custo e nao o conta como turno: %+v", c)
	}
	if registoDeCustoDeTool(nil, eventstore.Producer{}) != nil {
		t.Fatal("sem store nao ha escrita")
	}
	if err := registar(ctx, run, custoDeToolPayload{}); err == nil {
		t.Fatal("sem passo a escrita e recusada")
	}
}

// A FÓRMULA DA RESERVA: os tectos de tokens da tool pela tabela de preços do nó, pelo mesmo
// calculador do custo medido. Sem tabela, só tokens; com tabela sem o par, recusa.
func TestAOS530_Custo_AFormulaDaReserva(t *testing.T) {
	tabela, err := pricing.LoadTable([]byte(aos530PrecoComTool))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reservaMaxDoOCR(tabela, aos530AliasTool, "eu")
	// 8192 × 0,8 USD/Mtok = 6553,6 → 6554; 4096 × 4 USD/Mtok = 16384.
	if err != nil || r != (budget.Amount{Tokens: 8192 + 4096, CostMicroUSD: 6554 + 16384}) {
		t.Fatalf("reserva = %+v (%v)", r, err)
	}
	if r, err := reservaMaxDoOCR(nil, aos530AliasTool, "eu"); err != nil || r != (budget.Amount{Tokens: 8192 + 4096}) {
		t.Fatalf("sem tabela a reserva e so de tokens: %+v (%v)", r, err)
	}
	if _, err := reservaMaxDoOCR(tabela, "sem-preco", "eu"); !errors.Is(err, ErrOCRSemPreco) {
		t.Fatalf("sem o par: %v", err)
	}
}

// aos530Custo é um evento de custo lido do stream: o step do envelope e o payload.
type aos530Custo struct {
	envelope string
	p        custoDeToolPayload
}

// aos530Custos devolve os eventos de custo de tool do stream.
func aos530Custos(t *testing.T, evs []eventstore.Event) []aos530Custo {
	t.Helper()
	var out []aos530Custo
	for _, e := range evs {
		if e.Type != EventTypeToolModelCost {
			continue
		}
		var p custoDeToolPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload de %s: %v", EventTypeToolModelCost, err)
		}
		out = append(out, aos530Custo{envelope: e.StepID, p: p})
	}
	return out
}

// aos530PrecoComTool é a tabela de preços dos testes: o titular quase de graça, o alias da tool a
// 0,8 USD/Mtok de entrada e 4 USD/Mtok de saída.
const aos530PrecoComTool = `{"version":"t-530","entries":[{"model":"gpt-4o","region":"eu","rate":{"input_per_mtok_micro_usd":1,"output_per_mtok_micro_usd":1,"cache_read_per_mtok_micro_usd":1,"cache_write_per_mtok_micro_usd":1}},{"model":"ocr-visao","region":"eu","rate":{"input_per_mtok_micro_usd":800000,"output_per_mtok_micro_usd":4000000,"cache_read_per_mtok_micro_usd":80000,"cache_write_per_mtok_micro_usd":1000000}}]}`
