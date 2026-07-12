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
	if rt.assemblyVersion == "" {
		rt.assemblyVersion = AssemblyVersion
	}
	if rt.defaultMaxTurns <= 0 {
		rt.defaultMaxTurns = DefaultMaxTurns
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

	asm := NewPromptAssembler(goal.System, goal.Tools)
	producer := eventstore.Producer{
		NHIID:           goal.Principal.NHIID,
		DelegationChain: toStoreChain(goal.Principal.DelegationChain),
		Scope:           goal.Scope,
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

	// Tail append-only, semeado com memory_context + objectivo (trusted).
	tail := make([]TailSegment, 0, 8)
	if len(goal.MemoryContext) > 0 {
		tail = append(tail, TailSegment{Kind: TailMemory, Content: goal.MemoryContext})
	}
	if goal.Objective != "" {
		tail = append(tail, TailSegment{Kind: TailObjective, Content: []byte(goal.Objective)})
	}

	for turn := 1; turn <= maxTurns; turn++ {
		stepID := rt.stepIdentity.StepID(goal.RunID, turn)

		// (1) MONTAR — prompt cache-estável (prefixo imutável + tail append-only).
		view := asm.Assemble(turn, tail)
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
		seq, err := rt.recordTurn(ctx, goal, asm, stepID, turn, view, resp, producer)
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
		if resp.Text != "" {
			tail = append(tail, tailFromHistory(resp.Text))
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
			tail = append(tail, tailFromResult(result, toolErr))
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
		}); err != nil {
			return res, fmt.Errorf("%w: turno %d: %w", ErrCapture, turn, err)
		}

		// (4) VERIFICAR — terminação simples (a máquina de estados é AOS-017).
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseVerified); err != nil {
			return res, err
		}
		// FRONTEIRA DE FIM DE TURNO (ponto de ligação AOS-023). É AQUI — com todas as
		// activities do turno confirmadas e antes de o turno seguinte começar — que o
		// runtime chamaria control.SteerChannel.GracefulPause (materializar a pausa
		// graciosa) e injectaria a Correction.TailSegment de um Resume no tail do turno
		// seguinte. AOS-023 entrega e prova a API do canal (runtime/control) de forma
		// isolada; o wiring do canal ao loop de produção é DIFERIDO para o ticket de
		// integração de superfície (EPIC-12) — sem ele o comportamento de AOS-013
		// permanece inalterado.
		if resp.Final || len(resp.ToolCalls) == 0 {
			res.FinalText = resp.Text
			res.Turns = turn
			res.Terminated = true
			return res, nil
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
	span.SetAttribute(AttrCostUSD, microUSDToUSD(resp.CostMicroUSD))
	span.End()
	return resp, nil
}

// recordTurn constrói o manifesto e grava o evento "turn.recorded".
func (rt *Runtime) recordTurn(ctx context.Context, goal Goal, asm *PromptAssembler, stepID string, turn int, view PromptView, resp ModelResponse, producer eventstore.Producer) (uint64, error) {
	manifest := Manifest{
		PromptHash:      view.PromptHash,
		SystemHash:      asm.SystemHash(),
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
// ADOPÇÃO DO CONTRATO DE ACTIVITY (AOS-021): DIFERIDA. Este caminho medeia o efeito
// DIRECTAMENTE via rt.rm.Mediate (no-bypass estrutural + taint untrusted garantidos),
// mas ainda NÃO despacha via activity.Dispatcher.Dispatch — logo a idempotência/replay
// pelo step-ledger (AOS-014/016) NÃO cobre ainda o efeito externo REAL do loop. O
// checkpoint intra-iteração acima é AOS-015 (recorder de cursor), NÃO a dedup do
// ledger. Ligar o loop ao Dispatcher (construído com rm + durable.StepLedger) é wiring
// DEFERIDO (integração AOS-022); o escopo estrito de AOS-021 é o contrato. Ver
// activity/doc.go, "Adopção pelo loop (AOS-013): DIFERIDA".
func (rt *Runtime) mediateToolCall(ctx context.Context, goal Goal, parentStep string, idx int, inv ToolInvocation) (Tainted, error, error) {
	toolStep := parentStep + "-tool-" + itoa(idx+1) // step_id distinto: evento de mediação próprio

	toolCtx, span := rt.startToolSpan(ctx, goal.RunID, toolStep, inv.ToolID)

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
		Context: referencemonitor.CallContext{
			// A intenção de tool call vem do modelo (untrusted). O RM/política é
			// que decide; aqui declaramos a proveniência (ADR-005).
			Taint: TaintUntrusted,
		},
		Input: inv.Input,
	}

	// Mediate recebe o ctx DERIVADO do span execute_tool: futuros spans internos do
	// RM nascem filhos deste, mantendo a propagação de trace (Q3).
	dec, err := rt.rm.Mediate(toolCtx, call)
	if err != nil {
		span.SetAttribute("aos.decision", "error")
		span.End()
		return Tainted{}, nil, err // apenas cancelamento de contexto
	}
	span.SetAttribute("aos.decision", string(dec.Effect))
	// Uma tool PERMITIDA pode falhar em runtime: propaga-se dec.ToolErr para o span
	// (error.type) e para o loop, para não ficar silenciosamente descartado.
	if dec.ToolErr != nil {
		span.SetAttribute(AttrErrorType, dec.ToolErr.Error())
	}
	span.End()

	// Resultado devolvido ao loop SEMPRE marcado untrusted. Só há Output em permit.
	return Untrusted(dec.Output), dec.ToolErr, nil
}

// startToolSpan abre e anota o span execute_tool. Devolve o ctx DERIVADO (para
// propagação de trace na mediação do RM) e o span.
func (rt *Runtime) startToolSpan(ctx context.Context, runID, stepID, toolID string) (context.Context, Span) {
	ctx, span := rt.tracer.StartSpan(ctx, OpExecuteTool)
	span.SetAttribute(AttrOperationName, OpExecuteTool)
	span.SetAttribute(AttrToolName, toolID)
	span.SetAttribute(AttrRunID, runID)
	span.SetAttribute(AttrStepID, stepID)
	return ctx, span
}

// annotateAgentSpan anota o span invoke_agent com o uso e custo agregados.
func (rt *Runtime) annotateAgentSpan(span Span, res Result) {
	span.SetAttribute(AttrInputTokens, res.TotalUsage.InputTokens)
	span.SetAttribute(AttrOutputTokens, res.TotalUsage.OutputTokens)
	span.SetAttribute(AttrCostUSD, microUSDToUSD(res.TotalCostMicroUSD))
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
