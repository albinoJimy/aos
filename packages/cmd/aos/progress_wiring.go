package main

// AOS-262 — COMPOSIÇÃO DO BURN-DOWN + AVISO NO NÓ (primeira entrega: SEM decisão).
//
// O que este ficheiro liga, e só isto:
//
//	fonte (AOS-261, ledger de turnos) ─┐
//	tecto (BudgetReader sobre o orçamento por-run) ─┼→ ProgressSurface.EvaluateRun → AVISO
//	progresso (ProgressReflector sobre a state.Machine do run) ─┘   (log do nó + span, 1×/run)
//
// O aviso sai no LOG DO NÓ e — quando `AOS_OTLP_ENDPOINT` está definida — também como span
// `aos.control.budget_warning`. O log não é redundância: sem OTLP o tracer é o
// [otelgenai.NoopTracer] e o span não tem destino. Ver [runProgress.avisar].
//
// O QUE ESTE FICHEIRO NÃO LIGA, deliberadamente: as três opções da superfície de progresso
// (`extend`, `summarize_stop` e `abort`). O observador continua a consumir
// [progresssurface.ProgressSurface.EvaluateRun] (aviso) e NUNCA `Evaluate` (o prompt das 3
// opções da superfície), porque apresentar uma escolha que ninguém consegue executar é
// prometer o que não existe: `extend` precisaria de um mutador do tecto que o [budget.Budget]
// não tem (decisão do dono (iii) de 2026-08-12: SAI de AOS-263, com eixo registado) e
// `summarize_stop` de um caminho de resumo que o loop não tem.
//
// O QUE PASSOU A LIGAR (AOS-263): o MESMO aviso alimenta o PROMPT DE EXAUSTÃO — um pendente
// durável de segundo tipo e a suspensão do run em `waiting_on_human` pelo runGate já
// existente (ver exhaustion_prompt.go). As decisões que o prompt apresenta são as DUAS que a
// rota assinada de AOS-263 parte 3 executa (`continue` e `abort`, ver exhaustion_decision.go)
// — o `abort` de lá não é o da superfície de progresso: não duplica o disjuntor, materializa
// a aresta durável waiting_on_human→killed de AOS-017 com selo WORM e principal verificado. O
// prompt só se arma onde a maquinaria HITL E a rota de decisão estão compostas; sem elas o
// comportamento é o de AOS-262, palavra por palavra.
//
// NODE-LOCAL (ADR-018, `boundary_orq_sch_test.go`): os adaptadores das duas portas de
// leitura são construídos AQUI, sobre colaboradores que o nó já detém — o
// [integration.RunBudget] e o [runStateGates]. Nenhum importa
// `control-plane/orchestrator` nem `control-plane/scheduler`, apesar de o doc.go do core os
// nomear como os adaptadores "canónicos" (`HeadroomController.Admit`, `Degrader.ExecuteChain`,
// `StateProjector`): essas são as portas de EXTENSÃO/DEGRADAÇÃO/projecção do plano de
// controlo, e esta entrega não compõe nenhuma delas — o que se compõe é LEITURA.
//
// CICLO DE VIDA POR RUN (molde de [runBreakers]): uma superfície por run, criada a pedido e
// PODADA em [runProgress.forget] quando o run sai do registo de em-curso. É o que impede os
// mapas — e o latch do aviso — de crescerem com o tempo de vida do processo.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/aos-ref/control-plane/budget"
	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	eventstore "github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// maxLeiturasTransitoriasToleradas é quantas fronteiras de fim-de-turno CONSECUTIVAS o
// observador tolera com o substrato indisponível antes de a indisponibilidade deixar de ser
// tratada como transitória.
//
// O número existe porque as duas posturas extremas estão ambas erradas:
//
//   - propagar SEMPRE (o que este adaptador fazia) transforma uma perda momentânea de líder
//     do Event Store — que acontece em resync/failover — num caminho de TERMINAÇÃO de runs
//     saudáveis, criado por uma feature de LEITURA PURA que declaradamente não decide nada.
//     A porta do kernel escreve o contrato ao contrário: «erros TRANSITÓRIOS não devem
//     chegar aqui»;
//   - engolir SEMPRE devolve a cegueira permanente pela porta das traseiras: um store que
//     nunca mais atinge quórum daria um run inteiro sem burn-down, com o banner a prometer o
//     aviso. É o defeito de AOS-261 com outro nome.
//
// Três é o mesmo número de [AOS_BREAKER_MAX_STALE_ITERATIONS]: curto o bastante para que uma
// indisponibilidade real seja denunciada dentro de poucos turnos, largo o bastante para
// atravessar uma troca de líder.
const maxLeiturasTransitoriasToleradas = 3

// ErrProgressBudgetUnwired — `AOS_PROGRESS_THRESHOLD` está definida mas o nó não tem
// orçamento composto (`AOS_BUDGET_MAX_TOKENS` por definir). Fail-closed NO ARRANQUE, molde
// de [ErrBreakerVelocitySourceUnwired].
//
// PORQUE É FATAL E NÃO UM AVISO: sem tecto não há DENOMINADOR, e uma fracção sem
// denominador é 0 para sempre — o aviso NUNCA dispararia. O operador que escreve o limiar
// está a pedir para ser avisado e receberia silêncio permanente, com o banner a falar de um
// aviso a ~80%. Entre arrancar sem a protecção anunciada e não arrancar, o nó não arranca.
var ErrProgressBudgetUnwired = errors.New("aos: AOS_PROGRESS_THRESHOLD esta definida mas AOS_BUDGET_MAX_TOKENS nao — sem tecto NAO ha denominador para o burn-down e o aviso NUNCA dispararia. Defina AOS_BUDGET_MAX_TOKENS, ou remova AOS_PROGRESS_THRESHOLD")

// ---------------------------------------------------------------------------
// Porta 1 — BudgetReader node-local (o TECTO, denominador do burn-down)
// ---------------------------------------------------------------------------

// runBudgetReader adapta o [integration.RunBudget] à porta
// [progresssurface.BudgetReader]. É LEITURA PURA: não reserva, não debita, não muta — o
// enforcement é do hook de orçamento na cadeia de mediação (AOS-256/257), não daqui.
//
// O `treeID` é o RunID: no nó, o nó de orçamento de cada run é registado com o próprio
// RunID (`integration.RunBudget.acquire`). A resolução `runID→treeID` que AOS-261 punha
// como trabalho é, nesta topologia, a IDENTIDADE — e fica DECLARADA aqui em vez de
// adivinhada num resolvedor.
type runBudgetReader struct{ rb *integration.RunBudget }

// Limit devolve o tecto POR-RUN em tokens. É o mesmo para todos os runs (o tecto é
// por-run, não por-árvore) e não depende do nó estar vivo — é a configuração, não o estado.
//
// A dimensão micro-USD fica a ZERO de propósito: não há tecto em dólares no nó (não há
// canal de custo ponta a ponta, eixo AOS-259), e `consumedFraction` ignora as dimensões sem
// limite positivo. Pôr aqui um número inventado faria a fracção sair de uma divisão que
// ninguém configurou.
func (r runBudgetReader) Limit(_ context.Context, _ string) (budget.Amount, error) {
	return budget.Amount{Tokens: r.rb.MaxTokensPerRun()}, nil
}

// Available devolve o remanescente do nó de orçamento do run — o headroom que o hook de
// mediação ainda concede a tool calls.
//
// NÃO é o complemento do burn-down e não pode ser lido como tal: mede as RESERVAS de TOOL
// CALL (AOS-255: alcance tool-only), enquanto o numerador do burn-down vem do ledger de
// TURNOS DE MODELO. São dois consumos disjuntos do mesmo tecto, e é por isso que
// [progresssurface.ProgressSurface.EvaluateRun] usa o Limit e a fonte, nunca este valor.
// Existe porque a porta o exige e porque um run sem nó vivo tem de dar ERRO, não zero.
func (r runBudgetReader) Available(_ context.Context, treeID string) (budget.Amount, error) {
	tokens, ok := r.rb.AvailableTokens(treeID)
	if !ok {
		return budget.Amount{}, fmt.Errorf("aos: run %q sem no de orcamento vivo — o remanescente NAO e zero, e desconhecido", treeID)
	}
	return budget.Amount{Tokens: tokens}, nil
}

var _ progresssurface.BudgetReader = runBudgetReader{}

// ---------------------------------------------------------------------------
// Porta 2 — ProgressReflector node-local (os RÓTULOS de progresso)
// ---------------------------------------------------------------------------

// nodeProgressReflector adapta o estado durável do run à porta
// [progresssurface.ProgressReflector]. É POR-RUN porque `Snapshot()` não recebe o run —
// vive dentro da superfície do run, criada em [runProgress.resolve].
//
// State: lido de [state.Machine.Current] — a MESMA máquina que o steer, a escalada e o
// disjuntor usam (não se abre uma segunda). É uma leitura EM MEMÓRIA (`Current`, não
// `Rebuild`), porque isto corre em cada fronteira de fim-de-turno e um replay do stream por
// turno seria O(n²). Um run sem gate aberto (via directa, sem loop de serviço) dá "" —
// campo vazio é uma leitura honesta, um rótulo inventado não seria.
//
// Step: `chat#<turno>`, o formato que o contrato da porta já dá como exemplo. TEM produtor —
// é o turno que o loop passa ao observador, gravado aqui imediatamente antes da avaliação.
// (O critério de aceitação de AOS-262 admitia declarar o campo vazio por falta de produtor;
// há um, e é o mesmo índice que identifica o turno no ledger.)
type nodeProgressReflector struct {
	gates *runStateGates
	runID string
	turn  atomic.Int64
}

// setTurn grava o turno corrente antes da avaliação. Um run corre numa só goroutine, mas o
// atómico torna a leitura segura mesmo que uma inspecção externa lhe toque.
func (r *nodeProgressReflector) setTurn(turn int) { r.turn.Store(int64(turn)) }

// Snapshot implementa [progresssurface.ProgressReflector].
func (r *nodeProgressReflector) Snapshot() progresssurface.ProgressSnapshot {
	snap := progresssurface.ProgressSnapshot{}
	if t := r.turn.Load(); t > 0 {
		snap.Step = fmt.Sprintf("chat#%d", t)
	}
	if r.gates != nil {
		if gate := r.gates.resolveGate(r.runID); gate != nil {
			snap.State = string(gate.m.Current())
		}
	}
	return snap
}

var _ progresssurface.ProgressReflector = (*nodeProgressReflector)(nil)

// ---------------------------------------------------------------------------
// O observador — a porta que o loop consulta na fronteira de fim-de-turno
// ---------------------------------------------------------------------------

// runProgress detém uma [progresssurface.ProgressSurface] por run e é o
// [agentruntime.ProgressObserver] que o loop consulta. Seguro para uso concorrente.
type runProgress struct {
	gates     *runStateGates
	reader    progresssurface.BudgetReader
	source    *turnLedgerBurndown
	tracer    otelgenai.Tracer
	threshold float64

	// prompt é o PROMPT DE EXAUSTÃO (AOS-263) alimentado por este mesmo aviso. nil ⇒
	// DESARMADO: o nó comporta-se exactamente como em AOS-262 (avisa e o run continua).
	prompt *exhaustionPrompt

	// log é o LOG DO NÓ — o mesmo io.Writer onde sai o banner de arranque. É a superfície
	// de leitura do aviso que EXISTE SEMPRE (ver [runProgress.avisar]).
	log func(format string, args ...any)

	mu       sync.Mutex
	surfaces map[string]*runProgressState
}

// runProgressState é a superfície de UM run e o reflector que a alimenta.
type runProgressState struct {
	surface *progresssurface.ProgressSurface
	refl    *nodeProgressReflector
	// transitorias conta as leituras falhadas CONSECUTIVAS por indisponibilidade
	// transitória do substrato. Zera a cada leitura boa — o que se tolera é uma janela de
	// indisponibilidade, não um orçamento de falhas ao longo do run.
	transitorias atomic.Int64
}

// newRunProgress compõe o observador. Devolve nil (observador NÃO composto, declarado no
// banner) quando falta uma das duas metades sem as quais o burn-down seria uma fachada:
//
//   - sem orçamento (`rb == nil`) não há tecto ⇒ não há denominador ⇒ a fracção seria 0
//     para sempre. O gate de arranque [ErrProgressBudgetUnwired] cobre o caso em que o
//     operador PEDIU o aviso; aqui trata-se do default, que o banner declara;
//   - sem fonte (`source == nil`) não há numerador — e compor a superfície devolvendo
//     [progresssurface.ErrNilBurndownSource] em cada turno mataria todos os runs.
//
// O `log` é o LOG DO NÓ (o mesmo writer do banner de arranque) e é obrigatório para o aviso
// ser VISÍVEL: sem OTLP configurado o tracer é o [otelgenai.NoopTracer], pelo que o span do
// aviso não vai a lado nenhum — ver [runProgress.avisar]. Um nil é tolerado (o observador
// continua a funcionar), mas então o aviso só existe com OTLP ligado.
//
// O `prompt` (AOS-263) é EXPLÍCITO e não variádico, pela razão de [newRunStateGates]: um
// parâmetro omissível tornaria o prompt de exaustão desligável POR ESQUECIMENTO, em silêncio,
// num construtor com poucos call sites. nil ⇒ o nó avisa e o run continua (AOS-262 puro).
func newRunProgress(gates *runStateGates, rb *integration.RunBudget, source *turnLedgerBurndown, tracer otelgenai.Tracer, threshold float64, log func(string, ...any), prompt *exhaustionPrompt) *runProgress {
	if rb == nil || source == nil {
		return nil
	}
	return &runProgress{
		gates:     gates,
		reader:    runBudgetReader{rb: rb},
		source:    source,
		tracer:    tracer,
		threshold: threshold,
		prompt:    prompt,
		log:       log,
		surfaces:  make(map[string]*runProgressState),
	}
}

// resolve devolve (construindo à primeira vez) a superfície do run.
func (p *runProgress) resolve(runID string) *runProgressState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.surfaces[runID]; ok {
		return st
	}
	refl := &nodeProgressReflector{gates: p.gates, runID: runID}
	// extender e degrader são NIL de propósito: a extensão delegada e a degradação são as
	// duas metades da fase de DECISÃO, que esta entrega não compõe. Nil é o estado honesto
	// — a superfície devolve ErrNilBudgetExtender/ErrNilDegrader se alguém tentar usá-las,
	// em vez de haver um adaptador que finge decidir.
	st := &runProgressState{
		surface: progresssurface.New(p.reader, nil, nil, refl, p.tracer,
			progresssurface.WithRunID(runID),
			progresssurface.WithBurndownSource(p.source),
			progresssurface.WithThreshold(p.threshold),
		),
		refl: refl,
	}
	p.surfaces[runID] = st
	return st
}

// ObserveProgress implementa [agentruntime.ProgressObserver]: lê o burn-down do run no
// turno dado e, a partir do limiar, emite o aviso (uma vez por run, latch da superfície).
//
// NÃO devolve veredicto e não pára o run — a decisão não existe nesta entrega.
//
// # QUAL ERRO MATA O RUN, E PORQUÊ A DISTINÇÃO É DESTE LADO
//
// A porta do kernel ([agentruntime.ProgressObserver]) diz que um erro é FATAL e que «o
// adaptador do nó é quem decide o que é erro: erros TRANSITÓRIOS não devem chegar aqui».
// Este é esse adaptador, e a classificação é [burndownTransitorio]:
//
//   - CEGUEIRA (fonte sem ledger, ledger sem tokens, payload ilegível, store fechado) ⇒
//     SOBE e o run aborta. Correr um agente autónomo com o burn-down cego depois de o banner
//     ter prometido o aviso é a superfície verde a mentir que AOS-261 remove;
//   - INDISPONIBILIDADE TRANSITÓRIA do substrato (sem quórum durante uma troca de líder,
//     contexto do run a cair) ⇒ a leitura ADIA-SE para a fronteira seguinte. A leitura é
//     pura e o cursor não avançou: o turno não contado neste turno é contado no próximo.
//     Matar um run saudável por causa de um failover de milissegundos seria um caminho de
//     terminação NOVO criado por uma feature que declaradamente não decide nada.
//
// A tolerância é CONSECUTIVA e limitada ([maxLeiturasTransitoriasToleradas]): uma
// indisponibilidade que persiste deixa de ser transitória e passa a ser exactamente a
// cegueira do primeiro ramo — e é tratada como tal.
func (p *runProgress) ObserveProgress(ctx context.Context, runID string, turn int) error {
	if p == nil {
		return nil
	}
	st := p.resolve(runID)
	st.refl.setTurn(turn)
	ev, err := st.surface.EvaluateRun(ctx, runID, turn, runID)
	switch {
	case err == nil:
		st.transitorias.Store(0)
	case !burndownTransitorio(err):
		return fmt.Errorf("aos: burn-down do run %q no turno %d (AOS-262): %w", runID, turn, err)
	case st.transitorias.Add(1) > maxLeiturasTransitoriasToleradas:
		return fmt.Errorf("aos: burn-down do run %q no turno %d (AOS-262): o substrato do ledger esteve indisponivel em %d fronteiras de fim-de-turno CONSECUTIVAS — deixou de ser transitorio e passou a ser cegueira do burn-down: %w",
			runID, turn, maxLeiturasTransitoriasToleradas+1, err)
	default:
		p.logf("burn-down do run %q no turno %d: leitura ADIADA — o substrato do ledger esta transitoriamente indisponivel (%d de %d fronteiras consecutivas toleradas). O consumo NAO se perde: o cursor nao avancou e o turno e contado na fronteira seguinte. Causa: %v",
			runID, turn, st.transitorias.Load(), maxLeiturasTransitoriasToleradas, err)
		return nil
	}
	p.avisar(runID, ev)
	// PROMPT DE EXAUSTÃO (AOS-263) — o MESMO sinal, agora com consequência. Corre DEPOIS do
	// aviso para que a linha do log exista mesmo quando a suspensão falha: o operador tem de
	// saber que o limiar foi cruzado, e não só que o run morreu a tentar parar.
	//
	// nil-safe: com o prompt desarmado devolve nil e o comportamento é o de AOS-262. Quando
	// arma, devolve [errExhaustionSuspended] — que o kernel trata como qualquer erro do
	// observador (aborta o turno) e que o nó reconhece na saída do run como SUSPENSÃO, não
	// como falha (ver [NodeService.absorveSuspensaoPorExaustao]).
	return p.prompt.raise(ctx, runID, ev)
}

// burndownTransitorio classifica um erro da leitura do burn-down como INDISPONIBILIDADE
// MOMENTÂNEA do substrato (true) ou como CEGUEIRA (false).
//
// A lista é FECHADA e curta de propósito: só entram aqui erros cuja causa é sabidamente
// temporária e cuja repetição é sabidamente denunciada pela contagem de consecutivas.
//
//   - [eventstore.ErrNoQuorum] — o store não tem líder (perda/troca de líder, Kill/Revive de
//     resync). Volta sozinho quando uma réplica suficientemente actualizada regressa;
//   - [context.Canceled]/[context.DeadlineExceeded] — o contexto do PRÓPRIO run está a cair.
//     O run termina pelo caminho dele; carimbar-lhe por cima um erro de burn-down só
//     trocaria a causa da terminação por outra que não é a verdadeira.
//
// Tudo o resto — [ErrBurndownNoLedger], [ErrBurndownNoUsage], [eventstore.ErrClosed],
// payload ilegível, [progresssurface.ErrNilBurndownSource] — é ausência de DADOS, não de
// disponibilidade, e não se resolve esperando.
func burndownTransitorio(err error) bool {
	return errors.Is(err, eventstore.ErrNoQuorum) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// avisar é a SUPERFÍCIE DE LEITURA do aviso — a metade que faltava a AOS-262.
//
// O span `aos.control.budget_warning` que a superfície emite vai para o tracer do nó, e o
// tracer do nó é o [otelgenai.NoopTracer] SEMPRE QUE `AOS_OTLP_ENDPOINT` não está definida
// (bootstrap.go). Na configuração por omissão — tecto definido, OTLP não — o aviso não
// entrava no WORM, nem no event store, nem no stream SSE, nem em endpoint nenhum: o nó
// prometia avisar a ~80% e não havia UM sítio onde o operador pudesse olhar. Uma promessa
// sem superfície é a mesma classe de defeito que AOS-248 fechou no banner.
//
// A linha sai no LOG DO NÓ, que é o canal que existe SEMPRE e onde o operador já lê o
// banner de arranque — não se inventa um canal novo nem se abre uma segunda contabilidade.
// Sai UMA VEZ por run, no MESMO latch do span (`SpanEmitted`), pela mesma razão: um aviso
// repetido a cada turno deixa de ser lido.
func (p *runProgress) avisar(runID string, ev progresssurface.RunEvaluation) {
	if ev.Warning == nil || !ev.Warning.SpanEmitted {
		return
	}
	p.logf("AVISO DE BURN-DOWN (AOS-262) — run %q, turno %d: %.0f%% do tecto por-run consumido (%d de %d tokens, limiar %.2f). Contagem dos TURNOS DE MODELO e so deles (limite INFERIOR: o ledger nao pesa tool calls). Este aviso NAO para o run nem pede escolha — quem para e o disjuntor ou o steer do operador",
		runID, ev.Warning.Turn, ev.Warning.Fraction*100, ev.Burndown.Consumed.Tokens, ev.Burndown.Limit.Tokens, ev.Warning.Threshold)
}

// logf escreve no log do nó quando há writer. nil-safe: um observador construído sem log
// (testes de unidade das portas) continua a funcionar, apenas sem esta superfície.
func (p *runProgress) logf(format string, args ...any) {
	if p == nil || p.log == nil {
		return
	}
	p.log(format, args...)
}

// forget liberta o estado do run — a superfície, o latch do aviso e o cursor da fonte.
// Chamado quando o run sai do registo de em-curso (mesmo ponto de [runBreakers.forget]).
// nil-safe e idempotente.
func (p *runProgress) forget(runID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	st, ok := p.surfaces[runID]
	delete(p.surfaces, runID)
	p.mu.Unlock()
	if ok {
		st.surface.ForgetRun(runID)
	}
	p.source.forget(runID)
}

// promptArmed indica se o PROMPT DE EXAUSTÃO (AOS-263) está armado neste nó — isto é, se o
// aviso de burn-down tem consequência (suspensão em `waiting_on_human`) ou é só um aviso.
//
// Existe para o BANNER derivar do que está REALMENTE composto e não da intenção da config:
// é o mesmo `*exhaustionPrompt` que o observador consulta. nil-safe nos dois níveis (sem
// burn-down composto não há prompt possível).
func (p *runProgress) promptArmed() bool { return p != nil && p.prompt != nil }

// observer devolve o [agentruntime.ProgressObserver] a entregar ao runtime, ou nil quando o
// burn-down não está composto (um `*runProgress` nil embrulhado numa interface não-nil
// passaria o teste `!= nil` do kernel e ligaria um observador fantasma).
func (p *runProgress) observer() agentruntime.ProgressObserver {
	if p == nil {
		return nil
	}
	return p
}
