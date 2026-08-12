package main

// AOS-261 — A FONTE DO BURN-DOWN DO NÓ: o LEDGER DE TURNOS.
//
// Este é o adaptador node-local da porta [progresssurface.BurndownSource]. Lê o stream do
// run no Event Store e soma os eventos `turn.recorded` que o
// [agentruntime.TurnRecorder] já grava por CADA turno (`input_tokens`, `output_tokens`,
// `cost_micro_usd`). Não produz nada, não retém spans, não abre um segundo canal de
// contabilidade — lê o que já é durável.
//
// PORQUE O LEDGER E NÃO UM DECORADOR DE EXPORTER (a alternativa que AOS-261 põe em cima da
// mesa): a razão longa está no cabeçalho de `burndown_source.go` no core. Em três linhas:
// o ledger é DEDUPLICADO na origem (idempotency_key `run_id:step_id`), pelo que uma
// re-emissão não infla o burn-down; herda a retenção DURÁVEL que o nó já tem
// (`AOS_RETENTION_*`) em vez de exigir uma política de retenção nova em memória; e é
// chaveado pelo `run_id`, que sobrevive à retoma — enquanto agregar por `trace_id` zerava
// o burn-down a cada incarnação, que é o pior modo de falha de um aviso de exaustão.
//
// NODE-LOCAL (ADR-018): as únicas dependências são o Event Store e o kernel. Não importa
// `control-plane/orchestrator` nem `control-plane/scheduler` — o guarda
// `boundary_orq_sch_test.go` cobre este ficheiro (imports directos E grafo transitivo).
//
// FAIL-CLOSED (o critério duro de AOS-261): nenhum caminho devolve um consumo de zero com
// erro nil. Stream inexistente, stream sem turnos, payload ilegível — todos devolvem
// [ErrBurndownNoLedger] ou o erro do store; e turnos gravados com a dimensão que DECIDE
// (tokens) a zero devolvem [ErrBurndownNoUsage]. Um burn-down de 0% por falta de dados é
// pior do que não existir: parece protecção. O guarda está na GRANDEZA e não só na contagem
// de turnos, porque é a grandeza que alimenta `consumedFraction`.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/aos-ref/control-plane/budget"
	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrBurndownNoLedger — o run não tem ledger de turnos legível, logo NÃO HÁ burn-down.
//
// É um erro e não um zero porque as duas situações são indistinguíveis do lado de fora e
// têm consequências opostas: «este run ainda não gastou nada» é uma leitura, «não sei o
// que este run gastou» é uma cegueira. Chegar aqui na fronteira de fim-de-turno significa
// que o turno corrente NÃO foi gravado no stream que esta fonte lê — tipicamente porque o
// [agentruntime.TurnRecorder] do runtime está sobre OUTRO Event Store que não o do nó.
// Isso é um defeito de composição, e é exactamente o que se quer ver denunciado.
var ErrBurndownNoLedger = errors.New("aos: sem ledger de turnos para o run (nenhum evento turn.recorded) — o burn-down NAO tem fonte; 0% seria uma leitura falsa")

// ErrBurndownNoUsage — HÁ turnos gravados, mas a dimensão que DECIDE (tokens) somou ZERO.
//
// É o mesmo defeito de [ErrBurndownNoLedger] um nível abaixo, e foi o que sobrou quando o
// guarda se pôs na CONTAGEM DE TURNOS em vez de na GRANDEZA: N eventos `turn.recorded` com
// `input_tokens=0` e `output_tokens=0` davam `RunConsumption{Consumed:{0,0}, Turns:N}` com
// `err == nil`, `consumedFraction` devolvia 0, e o run queimava o tecto inteiro sem que o
// aviso disparasse uma única vez — com o banner a prometer «FAIL-CLOSED: sem fonte a leitura
// devolve ERRO e o run aborta, nunca 0%».
//
// NÃO É HIPOTÉTICO: o `translateResponse` do model gateway só preenche `Usage` a partir do
// `resp.Usage.PromptTokens/CompletionTokens` que o provider ecoar. Um provider que não ecoe
// usage grava turnos a zero, e a dimensão irmã (`CostMicroUSD`) já é comprovadamente zero
// ponta a ponta (eixo AOS-259). Zero tokens em N turnos de modelo é IMPOSSÍVEL num caminho
// bem composto — todo o turno tem pelo menos o prompt — logo é AUSÊNCIA DE DADOS disfarçada
// de leitura, e a postura tem de ser a mesma: denunciar, nunca 0%.
//
// A guarda é sobre TOKENS e não sobre micro-USD porque é a dimensão que tem tecto: o
// [runBudgetReader] declara `Limit{Tokens: …}` e deixa micro-USD a zero, pelo que só os
// tokens entram em `consumedFraction`. Um guarda sobre a dimensão sem limite negaria runs
// saudáveis por uma dimensão que não decide nada.
var ErrBurndownNoUsage = errors.New("aos: o ledger de turnos do run tem turnos mas ZERO tokens somados — a dimensao que decide o burn-down nao tem dados (o provider do modelo nao ecoou usage); 0% seria uma leitura falsa")

// turnLedgerStore é o subconjunto do Event Store de que a fonte depende. Mantê-lo mínimo
// desacopla-a da superfície completa do store e permite um duplo em teste.
// *eventstore.Store satisfá-lo.
type turnLedgerStore interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// turnLedgerPayload é a parte do payload de `turn.recorded` que o burn-down consome. O
// contrato é o `turnPayload` de `kernel/agent-runtime/turn.go`, que é INEXPORTADO — a
// forma de o selar não é copiar a struct e esperar, é o teste
// `TestAOS261_FonteLeOLedgerRealDoTurnRecorder`, que grava com o [agentruntime.TurnRecorder]
// REAL e lê por aqui: se um nome de campo JSON mudar do lado do produtor, a soma cai a zero
// e o teste avermelha.
type turnLedgerPayload struct {
	Turn         int   `json:"turn"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// turnLedgerBurndown é a [progresssurface.BurndownSource] sobre o Event Store do nó.
//
// CURSOR POR RUN: cada leitura recomeça no seq A SEGUIR ao último já somado, e o acumulado
// guarda-se. Sem ele, uma fonte consultada em CADA fronteira de fim-de-turno re-liria o
// stream inteiro todos os turnos — O(n²) no comprimento do run. O cursor NÃO é um cache com
// risco de divergir: o stream é append-only e deduplicado, pelo que somar cada seq uma vez
// dá exactamente o mesmo total que somar tudo de novo.
//
// Seguro para uso concorrente. O estado por-run é libertado por [turnLedgerBurndown.forget]
// quando o run sai do registo de em-curso — o mesmo ponto de [runBreakers.forget].
type turnLedgerBurndown struct {
	store turnLedgerStore

	mu     sync.Mutex
	cursor map[string]*turnLedgerCursor
}

// turnLedgerCursor é o acumulado de UM run e o seq até onde já foi somado.
type turnLedgerCursor struct {
	consumed budget.Amount
	turns    int
	lastTurn int
	nextSeq  uint64 // primeiro seq AINDA não somado
}

// newTurnLedgerBurndown constrói a fonte. store nil ⇒ (nil, nil): sem Event Store não há
// ledger, e a fonte não composta é declarada a montante (o observador de progresso não é
// ligado) em vez de existir e devolver zeros.
func newTurnLedgerBurndown(store turnLedgerStore) *turnLedgerBurndown {
	if store == nil {
		return nil
	}
	return &turnLedgerBurndown{store: store, cursor: make(map[string]*turnLedgerCursor)}
}

// ConsumedByRun implementa [progresssurface.BurndownSource].
//
// A soma é dos TURNOS DE MODELO — a linha de custo dominante — e só deles: o ledger de
// turnos não pesa tool calls, pelo que o valor é um LIMITE INFERIOR do consumo do run (o
// alcance está declarado em [progresssurface.RunConsumption] e no banner do nó). Um limite
// inferior faz o aviso disparar TARDE, nunca cedo por engano.
func (s *turnLedgerBurndown) ConsumedByRun(ctx context.Context, runID string) (progresssurface.RunConsumption, error) {
	if s == nil {
		// Não pode acontecer pela via composta (a fonte nil não é ligada), mas um nil aqui
		// devolveria o zero silencioso que este ticket remove — logo, erro.
		return progresssurface.RunConsumption{}, ErrBurndownNoLedger
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.cursor[runID]
	if !ok {
		cur = &turnLedgerCursor{nextSeq: 1}
		s.cursor[runID] = cur
	}
	evs, err := s.store.Read(ctx, runID, cur.nextSeq)
	switch {
	case errors.Is(err, eventstore.ErrStreamNotFound):
		// Stream inexistente. Se nada foi somado antes, é ausência TOTAL de ledger ⇒ erro.
		// Se já havia acumulado, o stream existe (foi lido antes) e um ErrStreamNotFound
		// aqui seria uma anomalia do store — trata-se pela mesma via, sem inventar dados.
		if cur.turns == 0 {
			return progresssurface.RunConsumption{}, fmt.Errorf("%w: run %q", ErrBurndownNoLedger, runID)
		}
		return progresssurface.RunConsumption{}, fmt.Errorf("%w: run %q (stream desapareceu apos %d turnos somados)", ErrBurndownNoLedger, runID, cur.turns)
	case err != nil:
		// Erro do store (contexto cancelado, store fechado, sem quórum). Sobe tal-qual:
		// uma leitura falhada NÃO é um consumo de zero.
		return progresssurface.RunConsumption{}, fmt.Errorf("aos: ler o ledger de turnos do run %q: %w", runID, err)
	}

	for _, ev := range evs {
		if ev.Seq >= cur.nextSeq {
			cur.nextSeq = ev.Seq + 1
		}
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p turnLedgerPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			// Um payload ilegível é uma leitura PARCIAL apresentada como total — o mesmo
			// defeito, com outro nome. Fail-closed.
			return progresssurface.RunConsumption{}, fmt.Errorf("aos: payload de turn.recorded ilegivel no run %q (seq %d): %w", runID, ev.Seq, err)
		}
		cur.consumed.Tokens += p.InputTokens + p.OutputTokens
		cur.consumed.CostMicroUSD += p.CostMicroUSD
		cur.turns++
		if p.Turn > cur.lastTurn {
			cur.lastTurn = p.Turn
		}
	}

	if cur.turns == 0 {
		// Stream existe mas SEM turnos gravados. Na fronteira de fim-de-turno isto é
		// impossível num nó bem composto (o turno é gravado antes) — logo é sintoma de
		// que o recorder escreve noutro store. Denunciar, nunca devolver 0%.
		return progresssurface.RunConsumption{}, fmt.Errorf("%w: run %q (stream existe, zero eventos %s)", ErrBurndownNoLedger, runID, agentruntime.EventTypeTurnRecorded)
	}
	if cur.consumed.Tokens == 0 {
		// HÁ turnos e a GRANDEZA QUE DECIDE somou zero. O guarda de cima (contagem de
		// turnos) não apanha este caso, e é o caso que sobrevive num nó onde tudo o resto
		// está bem composto: o recorder grava no store certo, o cursor avança, e mesmo
		// assim a fracção é 0 para sempre porque o provider do modelo não ecoou usage.
		// Ver [ErrBurndownNoUsage].
		return progresssurface.RunConsumption{}, fmt.Errorf("%w: run %q (%d turno(s) somado(s), 0 tokens)", ErrBurndownNoUsage, runID, cur.turns)
	}
	return progresssurface.RunConsumption{
		Consumed: cur.consumed,
		Turns:    cur.turns,
		LastTurn: cur.lastTurn,
	}, nil
}

// forget liberta o cursor do run (chamado quando o run sai do registo de em-curso).
// Idempotente; um run desconhecido é ignorado.
func (s *turnLedgerBurndown) forget(runID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.cursor, runID)
	s.mu.Unlock()
}

var _ progresssurface.BurndownSource = (*turnLedgerBurndown)(nil)
