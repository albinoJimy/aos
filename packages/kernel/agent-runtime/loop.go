package agentruntime

import (
	"context"
	"fmt"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// DefaultMaxTurns é o tecto de turnos por omissão (paragem defensiva do
// esqueleto; a terminação rica é a máquina de estados durável AOS-017).
const DefaultMaxTurns = 16

// Goal é o objectivo submetido a [Runtime.Run]: identidade, escopo, configuração
// de modelo e o system prompt + tool set congelado do run.
type Goal struct {
	// RunID é o identificador da trajectória (stream_id no Event Store). Obrigatório.
	RunID string
	// Principal é a NHI que origina o run e a sua cadeia de delegação (ADR-003).
	// NHIID é obrigatório.
	Principal referencemonitor.Principal
	// Credential é o token NHI (AOS-005) que autentica o Principal do run. É
	// PROPAGADO a cada [referencemonitor.Call] mediada (Credential), onde o hook de
	// identidade (identity.IdentityCheck) o verifica e resolve a autoridade. Vazio ⇒
	// chamada anónima: um RM composto com o hook de identidade NEGA fail-closed toda a
	// tool call (predecessor de segurança AOS-152). Um RM com o stub de identidade
	// ignora-o (comportamento inalterado). NÃO entra no prompt cache-estável (ADR-009)
	// nem na idempotency key (ADR-001) — é material de mediação, não de prompt.
	Credential string
	// Scope são os scopes activos do run (vão ao Producer dos eventos).
	Scope []string
	// Model pina o modelo (model_id/params/seed) — vai ao manifesto.
	Model ModelConfig
	// System é o system prompt — a parte imutável do prefixo cache-estável.
	System string
	// Tools é o tool set CONGELADO no run (ordem significativa, nunca reordenada).
	Tools []ToolSpec
	// Skills são as skills pinadas do run (vão ao manifesto).
	Skills []ToolSpec
	// Objective é a instrução inicial (semeia o tail append-only, trusted).
	Objective string
	// MemoryContext é o contexto de memória injectado no tail (ver EPIC-04).
	MemoryContext []byte
	// MaxTurns limita o nº de iterações (0 ⇒ [DefaultMaxTurns]).
	MaxTurns int

	// ParentTraceParent é o SEED cross-fronteira da árvore de spans (AOS-077):
	// quando este run é um sub-agente DELEGADO, transporta o traceparent W3C do span
	// invoke_agent-âncora aberto pelo Orquestrador no Spawn (ver
	// [orchestrator.SpawnHandle.ChildSeedTraceParent]). [Run] semeia o ctx-raiz com
	// ele antes de abrir o SEU invoke_agent, que assim herda o trace_id do pai e
	// aponta ParentSpanID ao span_id da âncora — ligando a sub-árvore do filho ao pai
	// pela mecânica NATIVA OTel (não por atributos NHI). Vazio ⇒ run-raiz (trace
	// novo). Um traceparent malformado é ignorado (best-effort: a perda da LIGAÇÃO ao
	// pai nunca aborta o run; a trajectória própria do filho é exportada na íntegra de
	// qualquer modo). A recursão neto→filho usa o mesmo campo em cada nível.
	ParentTraceParent string
}

// Result é o desfecho de [Runtime.Run].
type Result struct {
	RunID string
	// FinalText é a resposta final do modelo (quando Terminated).
	FinalText string
	// Turns é o nº de turnos executados.
	Turns int
	// TotalUsage é o consumo agregado de tokens do run.
	TotalUsage Usage
	// TotalCostMicroUSD é o custo agregado do run em micro-USD inteiro.
	TotalCostMicroUSD int64
	// ToolResults são TODOS os resultados de tools despachadas, na ordem de
	// despacho, cada um marcado untrusted (ADR-005).
	ToolResults []Tainted
	// TurnSeqs são os seq (no Event Store) dos eventos "turn.recorded" gravados.
	TurnSeqs []uint64
	// Terminated indica que o run atingiu uma resposta final (vs esgotar MaxTurns).
	Terminated bool
	// Paused indica que o run PAROU graciosamente por um interrupt out-of-band
	// consumido na fronteira de fim-de-turno (AOS-158): a pausa durável (running→
	// paused, AOS-023) foi materializada e o loop parou limpo entre turnos (nunca a
	// meio). Distinto de Terminated (resposta final) e de ErrMaxTurnsExceeded.
	Paused bool
}

// Runtime é o Agent Runtime: corre o loop base. Detém um *[referencemonitor.Monitor]
// (NUNCA uma tool executável) — o único caminho de execução de tools é
// [referencemonitor.Monitor.Mediate]. Construir com [New].
type Runtime struct {
	model    ModelClient
	rm       *referencemonitor.Monitor
	recorder *TurnRecorder

	tracer          Tracer
	stepIdentity    StepIdentity
	checkpointer    Checkpointer
	capturer        Capturer
	steer           SteerSource
	windowFactory   WindowFactory      // AOS-037: dono único do tail/assembly (D-TAIL)
	compaction      CompactionTrigger  // AOS-043: compressão em checkpoint
	dispatcher      ActivityDispatcher // AOS-021: despacho durável do efeito
	assemblyVersion string
	defaultMaxTurns int
}

// Option configura o Runtime na construção.
type Option func(*Runtime)

// WithTracer injecta a porta de observabilidade (default [NoopTracer]).
func WithTracer(t Tracer) Option { return func(rt *Runtime) { rt.tracer = t } }

// WithStepIdentity injecta o derivador de step_id (ponto de ligação AOS-014).
func WithStepIdentity(s StepIdentity) Option { return func(rt *Runtime) { rt.stepIdentity = s } }

// WithCheckpointer injecta o checkpointer intra-iteração (ponto de ligação AOS-015).
func WithCheckpointer(c Checkpointer) Option { return func(rt *Runtime) { rt.checkpointer = c } }

// WithCapturer injecta o capturer de não-determinismo (ponto de ligação AOS-016).
// Default [noopCapturer] — sem ele o comportamento de AOS-013 é inalterado.
func WithCapturer(c Capturer) Option { return func(rt *Runtime) { rt.capturer = c } }

// WithAssemblyVersion sobrepõe a versão do assembler gravada no manifesto (por
// omissão [AssemblyVersion]). Útil para testes de replay/versão.
func WithAssemblyVersion(v string) Option { return func(rt *Runtime) { rt.assemblyVersion = v } }

// WithMaxTurns sobrepõe o tecto de turnos por omissão.
func WithMaxTurns(n int) Option { return func(rt *Runtime) { rt.defaultMaxTurns = n } }

// New constrói o Runtime. model, rm e recorder são obrigatórios (não-nil); a
// validação estrita acontece em [Runtime.Run]. Defaults: [NoopTracer],
// step_id sequencial, checkpointer no-op, [AssemblyVersion], [DefaultMaxTurns].
func New(model ModelClient, rm *referencemonitor.Monitor, recorder *TurnRecorder, opts ...Option) *Runtime {
	rt := &Runtime{
		model:           model,
		rm:              rm,
		recorder:        recorder,
		tracer:          NoopTracer{},
		stepIdentity:    sequentialStepIdentity{},
		checkpointer:    noopCheckpointer{},
		capturer:        noopCapturer{},
		windowFactory:   defaultWindowFactory{},
		compaction:      noopCompactionTrigger{},
		assemblyVersion: AssemblyVersion,
		defaultMaxTurns: DefaultMaxTurns,
	}
	for _, o := range opts {
		o(rt)
	}
	if rt.tracer == nil {
		rt.tracer = NoopTracer{}
	}
	if rt.stepIdentity == nil {
		rt.stepIdentity = sequentialStepIdentity{}
	}
	if rt.checkpointer == nil {
		rt.checkpointer = noopCheckpointer{}
	}
	if rt.capturer == nil {
		rt.capturer = noopCapturer{}
	}
	if rt.windowFactory == nil {
		rt.windowFactory = defaultWindowFactory{}
	}
	if rt.compaction == nil {
		rt.compaction = noopCompactionTrigger{}
	}
	// Dispatcher default = Mediate directo sobre o RM do runtime (AOS-013). Definido
	// APÓS as opções para poder ligar rt.rm; um WithActivityDispatcher sobrepõe-no.
	if rt.dispatcher == nil {
		rt.dispatcher = directDispatcher{rm: rt.rm}
	}
	if rt.assemblyVersion == "" {
		rt.assemblyVersion = AssemblyVersion
	}
	if rt.defaultMaxTurns <= 0 {
		rt.defaultMaxTurns = DefaultMaxTurns
	}
	// WIRING do tracer partilhado (AOS-076): o span execute_tool é aberto no RM (o
	// ponto único de mediação, ADR-002). Para que esse span caia na MESMA árvore/sink
	// dos spans invoke_agent/chat abertos aqui, o RT injecta o SEU tracer no Monitor
	// que detém. Com o default [NoopTracer], o comportamento do Monitor é inalterado
	// para qualquer caller que o construa sem tracer.
	if rt.rm != nil {
		rt.rm.SetTracer(rt.tracer)
	}
	return rt
}

// validate verifica pré-condições do run.
func (rt *Runtime) validate(goal Goal) error {
	switch {
	case rt.model == nil:
		return ErrNoModelClient
	case rt.rm == nil:
		return ErrNoMonitor
	case rt.recorder == nil:
		return ErrNoRecorder
	case goal.RunID == "":
		return ErrEmptyRunID
	case goal.Principal.NHIID == "":
		return ErrNoPrincipal
	}
	return nil
}

// Run percorre o loop montar → chamar → despachar → verificar até uma resposta
// final ou esgotar MaxTurns. Cada turno é gravado no Event Store com o manifesto
// por trajectória; cada tool call atravessa o Reference Monitor; cada resultado
// de tool volta ao loop marcado untrusted.
func (rt *Runtime) Run(ctx context.Context, goal Goal) (Result, error) {
	if err := rt.validate(goal); err != nil {
		return Result{}, err
	}
	maxTurns := goal.MaxTurns
	if maxTurns <= 0 {
		maxTurns = rt.defaultMaxTurns
	}

	// DONO ÚNICO do tail/assembly (AOS-037, decisão D-TAIL): o loop delega a posse do
	// tail append-only e da montagem cache-estável à [WindowPort] — há UM só assembler /
	// prefix-hash por run (o da janela). Fail-closed: sem janela não há prompt a montar.
	// O default ([inlineWindow]) reproduz o PromptAssembler + tail inline byte-a-byte.
	win, err := rt.windowFactory.NewWindow(goal.RunID, goal.System, goal.Tools)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrWindow, err)
	}
	producer := eventstore.Producer{
		NHIID:           goal.Principal.NHIID,
		DelegationChain: toStoreChain(goal.Principal.DelegationChain),
		Scope:           goal.Scope,
	}

	// SEED cross-fronteira (AOS-077): se este run é um sub-agente delegado, semeia o
	// ctx-raiz com o SpanContext do pai transportado no traceparent, ANTES de abrir o
	// invoke_agent — que assim herda o trace_id do pai e o parenteia por span_id. Um
	// traceparent malformado é ignorado fail-open (a perda da ligação ao pai não
	// aborta o run; a trajectória própria do filho é exportada de qualquer modo).
	if goal.ParentTraceParent != "" {
		if sc, perr := ParseTraceParent(goal.ParentTraceParent); perr == nil {
			ctx = ContextWithSpanContext(ctx, sc)
		}
	}

	// Span invoke_agent envolve o run inteiro (ADR-010).
	ctx, agentSpan := rt.tracer.StartSpan(ctx, OpInvokeAgent)
	agentSpan.SetAttribute(AttrOperationName, OpInvokeAgent)
	agentSpan.SetAttribute(AttrRequestModel, goal.Model.ModelID)
	agentSpan.SetAttribute(AttrRunID, goal.RunID)

	res := Result{RunID: goal.RunID}

	// Anotar o uso/custo AGREGADO no span invoke_agent em TODOS os caminhos de
	// saída — inclusive nos returns de erro (ErrModelCall/ErrTurnRecord/checkpoint/
	// cancelamento) — para não perder o burn-down parcial de uma run falhada. O
	// defer lê a variável `res`, que carrega o acumulado no momento do return.
	defer func() {
		rt.annotateAgentSpan(agentSpan, res)
		agentSpan.End()
	}()

	// Tail append-only, semeado com memory_context + objectivo (trusted). O tail é agora
	// propriedade da [WindowPort] (D-TAIL) — o loop só lhe entrega segmentos.
	if len(goal.MemoryContext) > 0 {
		win.Append(TailSegment{Kind: TailMemory, Content: goal.MemoryContext})
	}
	if goal.Objective != "" {
		win.Append(TailSegment{Kind: TailObjective, Content: []byte(goal.Objective)})
	}

	// pendingCorrection carrega a correcção de steer TRUSTED injectada no tail no FIM do
	// turno anterior (a "leading correction" do turno corrente). É a costura que leva a
	// correcção — que só é conhecida DEPOIS da captura do turno em que foi emitida — à
	// captura do turno SEGUINTE, onde de facto pertence ao prompt (AOS-218). Sem steer
	// ligado permanece nil e a captura fica byte-idêntica (retro-compat).
	var pendingCorrection []byte

	for turn := 1; turn <= maxTurns; turn++ {
		stepID := rt.stepIdentity.StepID(goal.RunID, turn)

		// (1) MONTAR — prompt cache-estável (prefixo imutável + tail append-only). A
		// janela é o dono único do assembler: um só prefix-hash por run.
		view := win.Assemble(ctx, turn)
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseAssembled); err != nil {
			return res, err
		}

		// (2) CHAMAR — Model Gateway sob span chat (ADR-010).
		resp, err := rt.callModel(ctx, goal, stepID, view)
		if err != nil {
			return res, err
		}
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseModelCalled); err != nil {
			return res, err
		}
		res.TotalUsage.InputTokens += resp.Usage.InputTokens
		res.TotalUsage.OutputTokens += resp.Usage.OutputTokens
		res.TotalCostMicroUSD += resp.CostMicroUSD

		// Gravar o turno com o manifesto por trajectória.
		seq, err := rt.recordTurn(ctx, goal, win.SystemHash(), stepID, turn, view, resp, producer)
		if err != nil {
			return res, err
		}
		res.TurnSeqs = append(res.TurnSeqs, seq)
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseTurnRecorded); err != nil {
			return res, err
		}

		// Histórico do turno no tail append-only (o prefixo nunca muda). A saída do
		// modelo é untrusted-por-construção — marcada com a mesma proveniência dos
		// resultados de tool (consistência de auditoria, ADR-005).
		//
		// SEPARAÇÃO DE PLANOS (dual-LLM/CaMeL) — DIFERIDA (AOS-069). O conteúdo
		// untrusted (esta saída do modelo e os resultados de tool abaixo) é acrescentado
		// INLINE ao tail que asm.Assemble transforma no prompt do próximo turno; NÃO
		// passa ainda por [SeparatePlanes]/[ControlPlanner]/[Quarantine]. A defesa activa
		// no loop base é o default fail-closed do [referencemonitor.TaintGate]: nenhuma
		// call é marcada trusted por omissão, logo uma acção privilegiada influenciada
		// por injecção é BLOQUEADA (ver taint_plane_test.go). A barreira estrutural "o
		// planeador só vê trusted + handles" existe como primitivo (taint_plane.go) mas o
		// seu wiring à montagem de prompt do loop é DIFERIDO para o ticket de integração
		// de superfície (EPIC-12), à semelhança das notas de AOS-021/022 em
		// mediateToolCall e da fronteira de fim-de-turno de AOS-023 — sem ele o
		// comportamento de AOS-013 permanece inalterado.
		if resp.Text != "" {
			win.Append(tailFromHistory(resp.Text))
		}

		// (3) DESPACHAR — cada tool call PRETENDIDA via o Reference Monitor.
		// turnCaptured acumula os resultados DESTE turno (com a invocação original)
		// para a captura de não-determinismo (AOS-016), sem afectar res.ToolResults.
		var turnCaptured []CapturedToolResult
		for j, inv := range resp.ToolCalls {
			result, toolErr, err := rt.mediateToolCall(ctx, goal, stepID, j, inv)
			if err != nil {
				return res, err
			}
			res.ToolResults = append(res.ToolResults, result)
			turnCaptured = append(turnCaptured, CapturedToolResult{Invocation: inv, Result: result, ToolError: toolErr})
			// O tail materializa a condição de erro da tool (se houver) para o
			// modelo poder reagir; o conteúdo mantém-se untrusted, append-only.
			win.Append(tailFromResult(result, toolErr))
			// Checkpoint intra-iteração (AOS-015): a activity j ficou CONFIRMADA
			// (efeito externo concluído). O cursor carrega o sub-passo confirmado e
			// as activities ainda pendentes do turno — o resume retoma no próximo
			// sub-passo não confirmado sem repetir os já aplicados.
			if err := rt.cpActivity(ctx, goal.RunID, stepID, turn, j, len(resp.ToolCalls)); err != nil {
				return res, err
			}
		}

		// CAPTURA (AOS-016) — persiste os inputs não-determinísticos do turno (a
		// resposta do modelo COMPLETA + o output de cada tool call) para o replay
		// reconstruir a trajectória sem re-executar o modelo nem os efeitos. É
		// ADITIVA: default no-op ⇒ AOS-013 inalterado. Corre DEPOIS do despacho
		// (para captar os resultados das tools) e ANTES da verificação.
		if err := rt.capturer.Capture(ctx, TurnCapture{
			RunID:       goal.RunID,
			StepID:      stepID,
			Turn:        turn,
			Response:    resp,
			ToolResults: turnCaptured,
			Producer:    producer,
			// AOS-093: o TITULAR do run (o principal, ADR-003) sob cuja chave
			// por-titular o capturer cifra o conteúdo não-determinístico antes do ES.
			Subject: goal.Principal.NHIID,
			// AOS-218: a correcção de steer TRUSTED que o turno ANTERIOR injectou no tail
			// (leading correction deste turno). Vazia nos runs sem steer — captura
			// byte-idêntica. Capturá-la aqui é o que torna o replay do run steerado fiel.
			LeadingCorrection: pendingCorrection,
		}); err != nil {
			return res, fmt.Errorf("%w: turno %d: %w", ErrCapture, turn, err)
		}

		// (4) VERIFICAR — terminação simples (a máquina de estados é AOS-017).
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseVerified); err != nil {
			return res, err
		}
		// TERMINAÇÃO — uma resposta final acaba o run (não se pausa um run já concluído).
		if resp.Final || len(resp.ToolCalls) == 0 {
			res.FinalText = resp.Text
			res.Turns = turn
			res.Terminated = true
			return res, nil
		}

		// FRONTEIRA DE FIM DE TURNO (AOS-158) — consumir o canal de controlo out-of-band.
		// É AQUI, com todas as activities do turno confirmadas e antes do turno seguinte,
		// que a pausa é GRACIOSA (entre turnos, nunca a meio — AOS-023). Aditivo: sem um
		// [SteerSource] ligado ([WithSteerSource]), o comportamento de AOS-013 permanece
		// byte-idêntico.
		if rt.steer != nil {
			paused, err := rt.steer.GracefulPause(ctx, goal.RunID)
			if err != nil {
				return res, err
			}
			if paused {
				res.Turns = turn
				res.Paused = true
				return res, nil
			}
			// Uma correcção de um humano AUTENTICADO é dado de controlo TRUSTED —
			// injectada no tail do turno seguinte (taint=trusted), nunca como conteúdo
			// untrusted (separação control/data-plane, ADR-005). Guarda-se em
			// pendingCorrection para a captura do turno SEGUINTE a persistir (AOS-218): é
			// no prompt desse turno que a correcção entra, logo é lá que o replay tem de a
			// reconstruir para o prompt_hash bater.
			if corr, ok := rt.steer.PendingCorrection(goal.RunID); ok {
				win.Append(tailFromCorrection(corr))
				pendingCorrection = corr
			} else {
				pendingCorrection = nil
			}
		}

		// COMPACTAÇÃO EM CHECKPOINT (AOS-043) — observa a ocupação da janela na fronteira
		// de fim-de-turno (com o tail do turno completo, incluindo eventual correcção) e
		// pode enfileirar compressão assíncrona FORA do turno. Aditivo: default no-op ⇒
		// AOS-013 inalterado. Corre só em turnos NÃO-terminais (um run concluído/pausado
		// já retornou acima) — a compressão prepara a janela do turno SEGUINTE.
		if _, err := rt.compaction.Observe(ctx, goal.RunID, turn, win.Signal()); err != nil {
			return res, err
		}
	}

	res.Turns = maxTurns
	return res, ErrMaxTurnsExceeded
}

// callModel abre o span chat, chama o modelo e anota o span com uso e custo.
func (rt *Runtime) callModel(ctx context.Context, goal Goal, stepID string, view PromptView) (ModelResponse, error) {
	chatCtx, span := rt.tracer.StartSpan(ctx, OpChat)
	span.SetAttribute(AttrOperationName, OpChat)
	span.SetAttribute(AttrRequestModel, goal.Model.ModelID)
	// A NHI do principal que executa o turno (AOS-076 CA1): identifica QUEM corre o
	// chat. É metadado de identidade, nunca um segredo/credencial (ADR-006).
	span.SetAttribute(AttrPrincipalNHI, goal.Principal.NHIID)
	span.SetAttribute(AttrRunID, goal.RunID)
	span.SetAttribute(AttrStepID, stepID)
	span.SetAttribute(AttrPromptHash, view.PromptHash)
	// Hash do prefixo cache-estável: byte-idêntico entre turnos do mesmo run —
	// torna o cache-hit-rate do prefixo observável por telemetria (AOS-013 CA3).
	span.SetAttribute(AttrPrefixHash, view.PrefixHash)

	resp, err := rt.model.Call(chatCtx, view)
	if err != nil {
		span.End()
		return ModelResponse{}, fmt.Errorf("%w: turno %d: %w", ErrModelCall, view.Turn, err)
	}
	span.SetAttribute(AttrInputTokens, resp.Usage.InputTokens)
	span.SetAttribute(AttrOutputTokens, resp.Usage.OutputTokens)
	// Custo do turno em USD (float, conveniência OTel) E em micro-USD INTEIRO (fonte de
	// verdade). O inteiro exacto é o que a agregação por trajectória/sub-árvore (AOS-078)
	// soma sem drift de vírgula flutuante e o que reconcilia com os totais do Model
	// Gateway; é o mesmo valor já em mão (resp.CostMicroUSD), emitido em paralelo — não é
	// contabilidade nova, é a exposição exacta do custo que a chat span já registava.
	span.SetAttribute(AttrCostUSD, microUSDToUSD(resp.CostMicroUSD))
	span.SetAttribute(AttrCostMicroUSD, resp.CostMicroUSD)
	span.End()
	return resp, nil
}

// recordTurn constrói o manifesto e grava o evento "turn.recorded".
func (rt *Runtime) recordTurn(ctx context.Context, goal Goal, systemHash string, stepID string, turn int, view PromptView, resp ModelResponse, producer eventstore.Producer) (uint64, error) {
	manifest := Manifest{
		PromptHash:      view.PromptHash,
		SystemHash:      systemHash,
		AssemblyVersion: rt.assemblyVersion,
		Model: ModelManifest{
			ModelID: goal.Model.ModelID,
			Params:  goal.Model.Params,
			Seed:    goal.Model.Seed,
		},
		Tools:  pinnedDeps(goal.Tools),
		Skills: pinnedDeps(goal.Skills),
	}
	seq, err := rt.recorder.Record(ctx, TurnRecord{
		RunID:        goal.RunID,
		StepID:       stepID,
		Turn:         turn,
		Manifest:     manifest,
		Usage:        resp.Usage,
		CostMicroUSD: resp.CostMicroUSD,
		ToolCalls:    len(resp.ToolCalls),
		Final:        resp.Final,
		Producer:     producer,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: turno %d: %w", ErrTurnRecord, turn, err)
	}
	return seq, nil
}

// mediateToolCall traduz uma tool call pretendida num [referencemonitor.Call] e
// submete-a a Mediate (NUNCA executa directamente). O resultado volta marcado
// untrusted (ADR-005), qualquer que seja o veredicto do RM. O span execute_tool
// envolve a mediação. NOTA: o nome evita deliberadamente os identificadores
// reservados de dispatcher do archlint — o único despacho real é rm.Mediate.
// Devolve o resultado (SEMPRE untrusted), o erro DA TOOL (dec.ToolErr — não-fatal,
// para o loop materializar no tail) e o erro FATAL do loop (só cancelamento de
// contexto). Um erro da tool NÃO é uma negação de política: a decisão foi Permit e
// o efeito ocorreu, mas a execução downstream falhou (ADR-005 / decision.ToolErr).
//
// ADOPÇÃO DO CONTRATO DE ACTIVITY (AOS-021): o despacho passa agora pela porta
// [ActivityDispatcher] (ver ports.go, AOS-157). O default é Mediate directo (byte-
// idêntico AOS-013, no-bypass estrutural + taint garantidos); um adaptador durável no
// apex (activity.Dispatcher sobre rm + durable.StepLedger) acrescenta idempotência/
// replay pelo step-ledger à volta da MESMA mediação, SEM o loop perder o Credential
// (AOS-152) nem o taint da autorização — a porta recebe o Call já construído aqui.
func (rt *Runtime) mediateToolCall(ctx context.Context, goal Goal, parentStep string, idx int, inv ToolInvocation) (Tainted, error, error) {
	toolStep := parentStep + "-tool-" + itoa(idx+1) // step_id distinto: evento de mediação próprio

	call := referencemonitor.Call{
		RunID:        goal.RunID,
		StepID:       toolStep,
		ParentStepID: parentStep,
		ToolID:       inv.ToolID,
		Capability:   inv.Capability,
		Resource: referencemonitor.Resource{
			Type:   inv.ResourceType,
			Value:  inv.ResourceValue,
			Region: inv.ResourceRegion,
		},
		Principal: goal.Principal,
		// Credential do run propagado à call: é AQUI que o token NHI chega ao hook de
		// identidade (AOS-152). Vazio ⇒ anónimo ⇒ deny fail-closed sob o hook real.
		Credential: goal.Credential,
		Context: referencemonitor.CallContext{
			// Taint da AUTORIZAÇÃO da call (ADR-005/AOS-069): a proveniência do PLANO
			// que a originou, não a dos seus dados. Só o control-plane sobre dados
			// trusted marca trusted (ver [AuthorizeTrusted]); por omissão é untrusted
			// (fail-closed). O [referencemonitor.TaintGate] impõe: uma autorização
			// untrusted não pode originar uma capability privilegiada.
			Taint: authorizationTaintOf(inv),
		},
		Input: inv.Input,
	}

	// O span execute_tool é aberto AGORA pelo Reference Monitor dentro de Mediate — o
	// ponto único de mediação (ADR-002) — e não mais aqui, para não o DUPLICAR. O RM
	// é a autoridade do span: anota nome/hash(tool+args)/taint da autorização/marca
	// untrusted do resultado/veredicto/denied_by/error.type, e fecha-o em todos os
	// caminhos. Como o RT partilha o seu tracer com o RM (ver [New]), esse span cai na
	// mesma árvore que o invoke_agent propagado por ctx. Mediate recebe o ctx do
	// invoke_agent: o execute_tool liga-se a ele por parent_span_id.
	// Despacho via a porta [ActivityDispatcher] (AOS-021): o default é Mediate directo
	// (byte-idêntico AOS-013); um adaptador durável (activity.Dispatcher) acrescenta
	// idempotência/replay à volta da MESMA mediação. A porta recebe o Call COMPLETO, com
	// o Credential (AOS-152) e o taint da autorização — a identidade nunca se perde.
	dec, err := rt.dispatcher.Dispatch(ctx, call)
	if err != nil {
		return Tainted{}, nil, err // apenas cancelamento de contexto
	}

	// Resultado devolvido ao loop SEMPRE marcado untrusted. Só há Output em permit.
	return Untrusted(dec.Output), dec.ToolErr, nil
}

// annotateAgentSpan anota o span invoke_agent com o uso e custo agregados.
func (rt *Runtime) annotateAgentSpan(span Span, res Result) {
	span.SetAttribute(AttrInputTokens, res.TotalUsage.InputTokens)
	span.SetAttribute(AttrOutputTokens, res.TotalUsage.OutputTokens)
	// AGREGADO do run em USD (float) e micro-USD INTEIRO. Este agregado NÃO deve ser
	// somado pela agregação por trajectória (AOS-078) — duplicaria com os por-turno dos
	// chats; a agregação conta só spans chat. O inteiro exacto aqui serve o consumidor
	// que lê o total directamente do invoke_agent.
	span.SetAttribute(AttrCostUSD, microUSDToUSD(res.TotalCostMicroUSD))
	span.SetAttribute(AttrCostMicroUSD, res.TotalCostMicroUSD)
}

// cp invoca o checkpointer (ponto de ligação AOS-015). Default no-op.
func (rt *Runtime) cp(ctx context.Context, runID, stepID string, turn int, phase CheckpointPhase) error {
	return rt.checkpointer.Checkpoint(ctx, Checkpoint{
		RunID:  runID,
		StepID: stepID,
		Turn:   turn,
		Phase:  phase,
	})
}

// cpActivity é o checkpoint intra-iteração da fase de despacho: confirma a
// activity idx (0-based) do turno e declara as activities ainda pendentes. O
// sub-passo confirmado usa a MESMA convenção que a mediação ("-tool-"+n, 1-based)
// e que o step-ledger de AOS-014, garantindo consistência checkpoint↔ledger. Só a
// ligação do cursor é aditiva; o default no-op ignora os campos extra.
func (rt *Runtime) cpActivity(ctx context.Context, runID, stepID string, turn, idx, total int) error {
	confirmed := stepID + "-tool-" + itoa(idx+1)
	var pending []string
	for k := idx + 2; k <= total; k++ {
		pending = append(pending, stepID+"-tool-"+itoa(k))
	}
	return rt.checkpointer.Checkpoint(ctx, Checkpoint{
		RunID:             runID,
		StepID:            stepID,
		Turn:              turn,
		Phase:             PhaseDispatched,
		ConfirmedStepID:   confirmed,
		PendingActivities: pending,
	})
}

// pinnedDeps projecta ToolSpecs em dependências pinadas do manifesto.
func pinnedDeps(specs []ToolSpec) []PinnedDep {
	if len(specs) == 0 {
		return nil
	}
	out := make([]PinnedDep, len(specs))
	for i, s := range specs {
		out[i] = PinnedDep(s)
	}
	return out
}

// toStoreChain converte a cadeia de delegação do RM para o modelo do Event Store.
func toStoreChain(chain []referencemonitor.DelegationHop) []eventstore.DelegationHop {
	if len(chain) == 0 {
		return nil
	}
	out := make([]eventstore.DelegationHop, len(chain))
	for i, h := range chain {
		out[i] = eventstore.DelegationHop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}
