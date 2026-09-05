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
// de turnos, porque é a grandeza que alimenta `consumedFraction`. E desde AOS-336 está por
// TURNO e não sobre o agregado: basta UM turno não medido para a soma deixar de ser o
// consumo do run — um agregado a zero só apanhava o run inteiramente cego, nunca o misto.

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

// ErrBurndownNoUsage — HÁ turnos gravados e PELO MENOS UM não foi MEDIDO.
//
// É o mesmo defeito de [ErrBurndownNoLedger] um nível abaixo, e foi o que sobrou quando o
// guarda se pôs na CONTAGEM DE TURNOS em vez de na GRANDEZA: N eventos `turn.recorded` com
// `input_tokens=0` e `output_tokens=0` davam `RunConsumption{Consumed:{0,0}, Turns:N}` com
// `err == nil`, `consumedFraction` devolvia 0, e o run queimava o tecto inteiro sem que o
// aviso disparasse uma única vez — com o banner a prometer «FAIL-CLOSED: sem fonte a leitura
// devolve ERRO e o run aborta, nunca 0%».
//
// O GUARDA É POR TURNO E NÃO SOBRE O AGREGADO (AOS-336). A primeira versão testava
// `turns > 0 && turnTokens == 0` sobre um cursor CUMULATIVO POR RUN, e a revisão adversarial
// mediu o que isso deixava passar: um único turno com usage desarmava-o PARA SEMPRE, e todos
// os turnos não medidos seguintes contavam zero em silêncio. Não apanhava «ao fim de N
// turnos» — só apanhava o run INTEIRAMENTE a zero, que é o caso menos realista dos dois. Um
// provider intermitente, ou um par (modelo, região) sem preço a meio de um run, produz
// exactamente o run MISTO: uma leitura que parece boa e está em baixo, que é pior do que
// nenhuma. Basta UM turno não medido para a soma deixar de ser o consumo do run.
//
// NÃO É HIPOTÉTICO: o `translateResponse` do model gateway só preenche `Usage` a partir do
// `resp.Usage.PromptTokens/CompletionTokens` que o provider ecoar. Um provider que não ecoe
// usage grava turnos a zero — e arrasta consigo a dimensão irmã, porque desde AOS-259 o
// `CostMicroUSD` é DERIVADO desses mesmos tokens pela tabela de preços: zero tokens ⇒ zero
// custo, pelo que a cegueira é a mesma nas duas dimensões e o guarda continua a ser o
// mesmo. Zero tokens em N turnos de modelo é IMPOSSÍVEL num caminho
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
	// UsageAusente é a marca que o produtor grava quando o turno NÃO foi medido
	// (AOS-336): o gateway projecta-a de `port.Usage` para `agentruntime.Usage`, e o
	// [agentruntime.TurnRecorder] escreve-a no evento. É a via AUTORITATIVA — diz o que o
	// produtor SABE, em vez de o consumidor inferir.
	UsageAusente bool `json:"usage_ausente"`
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

	// AOS-287 — as tool calls somam ao MESMO acumulado (saem do mesmo nó de orçamento),
	// mas são contadas à parte por uma razão de SEGURANÇA DO SINAL: os dois guardas
	// fail-closed abaixo perguntam coisas sobre o MODELO, e misturar as grandezas fá-los-ia
	// calar-se quando mais fazem falta.
	//
	//   - `turns == 0` denuncia «o recorder escreve noutro store». Um run que ainda só fez
	//     tool calls tem turns=0 LEGITIMAMENTE — e o seu consumo NÃO pode ser descartado,
	//     senão o AOS-287 não fecha nada;
	//   - `turnsSemUsage > 0` denuncia «o provider não ecoou usage NALGUM turno». Se olhasse
	//     para o total, os tokens das tool calls mascarariam esse silêncio e o detector
	//     morria — e é por isso que continua a contar TURNOS DE MODELO, um a um.
	toolCalls int

	// turnsSemUsage é quantos turnos de modelo chegaram NÃO MEDIDOS (AOS-336). É uma
	// contagem por turno e não um agregado precisamente porque o agregado era o defeito:
	// um único turno medido zerava a suspeita sobre todos os outros.
	turnsSemUsage int
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
		// CONSUMO DE TOOL CALL (AOS-287). Soma-se ao dos turnos porque sai do MESMO nó de
		// orçamento — e não conta como turno: o contador de turnos governa a fracção de
		// burn-down e a última rodada, que são grandezas do modelo.
		//
		// Não há dupla contagem com os turnos: são tipos de evento disjuntos, e este facto
		// existe precisamente porque o `turn.recorded` NÃO vê as tool calls.
		if ev.Type == EventTypeToolCallBudget {
			var tc toolCallBudgetPayload
			if err := json.Unmarshal(ev.Payload, &tc); err != nil {
				return progresssurface.RunConsumption{}, fmt.Errorf("aos: payload de %s ilegivel no run %q (seq %d): %w", EventTypeToolCallBudget, runID, ev.Seq, err)
			}
			cur.consumed.Tokens += tc.Tokens
			cur.consumed.CostMicroUSD += tc.CostMicroUSD
			cur.toolCalls++
			continue
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
		// DUAS VIAS PARA A MESMA CONCLUSÃO, e a segunda não depende do produtor.
		//
		// A marca é a via autoritativa: diz o que o gateway SABE sobre a resposta do
		// provedor. O critério `InputTokens <= 0` é a via defensiva, e é a que mantém este
		// guarda de pé para eventos gravados por código que ainda não escrevia a marca e
		// para qualquer produtor futuro que a esqueça. Não existe turno de modelo sem
		// entrada — há sempre system+user —, pelo que zero tokens de entrada é ausência de
		// leitura e nunca uma medição. É o mesmo critério de [port.Usage.Definido] no
		// gateway e de `cache_sli.CallRate` na telemetria: sem denominador não há leitura.
		if p.UsageAusente || p.InputTokens <= 0 {
			cur.turnsSemUsage++
		}
		if p.Turn > cur.lastTurn {
			cur.lastTurn = p.Turn
		}
	}

	if cur.turns == 0 && cur.toolCalls == 0 {
		// Stream existe mas SEM turnos gravados E sem tool calls (AOS-287: um run que
		// ainda so fez tool calls tem turns=0 legitimamente, e o seu consumo TEM de
		// contar — descarta-lo era a fuga que este eixo fecha). Na fronteira de fim-de-turno isto é
		// impossível num nó bem composto (o turno é gravado antes) — logo é sintoma de
		// que o recorder escreve noutro store. Denunciar, nunca devolver 0%.
		return progresssurface.RunConsumption{}, fmt.Errorf("%w: run %q (stream existe, zero eventos %s)", ErrBurndownNoLedger, runID, agentruntime.EventTypeTurnRecorded)
	}
	if cur.turnsSemUsage > 0 {
		// HÁ turnos e pelo menos um NÃO FOI MEDIDO. O guarda de cima (contagem de turnos)
		// não apanha este caso, e é o caso que sobrevive num nó onde tudo o resto está bem
		// composto: o recorder grava no store certo, o cursor avança, e mesmo assim a soma
		// não é o consumo do run porque o provider do modelo não ecoou usage nalgum turno.
		//
		// BASTA UM. A versão anterior perguntava se o AGREGADO era zero, e um único turno
		// medido desarmava-a para sempre — deixando passar o run MISTO, que é o realista.
		// Ver [ErrBurndownNoUsage].
		return progresssurface.RunConsumption{}, fmt.Errorf("%w: run %q (%d de %d turno(s) sem usage medido)", ErrBurndownNoUsage, runID, cur.turnsSemUsage, cur.turns)
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
