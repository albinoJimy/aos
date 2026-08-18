package agentruntime

import (
	"context"
	"fmt"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
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
	// Tripped indica que o run PAROU por DISPARO do circuit breaker do agente vivo
	// (AOS-080/081) na fronteira de fim-de-turno: deixou de progredir, excedeu o
	// wall-clock ou a velocidade de queima. A transição durável já foi materializada
	// pelo adaptador. É o VEREDICTO ÚTIL que substitui o esgotamento cego de MaxTurns.
	Tripped bool
	// BreakerTarget é o rótulo do estado durável atingido no disparo ("paused" |
	// "timed_out"). Vazio quando !Tripped.
	BreakerTarget string
	// BudgetExhausted indica que o run PAROU porque a ADMISSÃO DO TURNO DE MODELO
	// (AOS-260) negou headroom: o orçamento do run não comporta mais uma inferência.
	// NENHUMA chamada ao modelo ocorreu no turno em que isto ficou true — o turno não
	// chegou a existir, e por isso [Result.Turns] conta os turnos COMPLETOS anteriores.
	//
	// É uma DEGRADAÇÃO DECLARADA e não uma falha: o loop não retenta (um deny-loop cego
	// queimaria wall-clock e morreria com a causa errada) e não devolve erro. Distinto de
	// Tripped (disjuntor), Paused (steer) e de ErrMaxTurnsExceeded (tecto de ITERAÇÕES, não
	// de gasto).
	BudgetExhausted bool
	// BudgetExhaustionReason é o rótulo ATRIBUÍVEL da negação (nunca segredo) devolvido
	// pela porta de admissão — é o que faz o log dizer «parou por orçamento» em vez de
	// deixar o operador a procurar a causa no disjuntor. Vazio quando !BudgetExhausted.
	BudgetExhaustionReason string
	// Escalated indica que o run PAROU à espera de AVAL HUMANO (AOS-021): o Reference
	// Monitor devolveu `escalate` numa tool call (nenhum efeito ocorreu) e o
	// [EscalationSink] suspendeu o run (running → waiting_on_human). Distinto de Paused
	// (steer) e de Tripped (disjuntor).
	Escalated bool
	// EscalatedPreview é o digest canónico da acção que aguarda aprovação — o mesmo valor
	// que as pernas de aprovação assinam. Vazio quando !Escalated.
	EscalatedPreview []byte
}

// Runtime é o Agent Runtime: corre o loop base. Detém um *[referencemonitor.Monitor]
// (NUNCA uma tool executável) — o único caminho de execução de tools é
// [referencemonitor.Monitor.Mediate]. Construir com [New].
type Runtime struct {
	model    ModelClient
	rm       *referencemonitor.Monitor
	recorder *TurnRecorder

	tracer           Tracer
	stepIdentity     StepIdentity
	checkpointer     Checkpointer
	capturer         Capturer
	steer            SteerSource
	breaker          LivenessBreaker        // AOS-080/081: disjuntor multi-sinal do agente vivo
	actionObserver   ActionObserver         // AOS-251: fonte do sinal de no-progress (hash por acção mediada)
	admission        ModelAdmission         // AOS-260: admissão do TURNO DE MODELO (reserva antes, saldo depois)
	progressObserver ProgressObserver       // AOS-262: burn-down + aviso a ~limiar (leitura, nunca decisão)
	escalation       EscalationSink         // AOS-021: tool call escalada → espera por humano
	approvalEvidence ApprovalEvidenceSource // AOS-021: prova de aprovação a anexar na retoma
	windowFactory    WindowFactory          // AOS-037: dono único do tail/assembly (D-TAIL)
	compaction       CompactionTrigger      // AOS-043: compressão em checkpoint
	dispatcher       ActivityDispatcher     // AOS-021: despacho durável do efeito
	callRewriter     CallRewriter           // AOS-005/064: forma final do efeito, na construção
	assemblyVersion  string
	defaultMaxTurns  int
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

// CallRewriter dá a FORMA FINAL ao efeito antes de ele ser descrito seja a quem for: a
// Call que sai daqui é a que o RM medeia, a que o step-ledger indexa, a que o audit sela
// e a que o humano vê na preview de aprovação. Um erro é fail-closed — nenhum efeito
// ocorre e a tool call materializa-se como Deny no tail.
//
// O caso de uso é a mediação de sandbox (AOS-005/AOS-064): os args do modelo (untrusted)
// preenchem slots nomeados de um comando FIXO de configuração trusted.
type CallRewriter func(referencemonitor.Call) (referencemonitor.Call, error)

// CodeEffectRewrite é o Code de Deny quando o [CallRewriter] recusa a Call (ex.: args do
// modelo malformados). Nenhum efeito ocorre.
const CodeEffectRewrite = "E_EFFECT_REWRITE"

// WithCallRewriter injecta o [CallRewriter]. Default: nenhum (Call inalterada).
func WithCallRewriter(r CallRewriter) Option { return func(rt *Runtime) { rt.callRewriter = r } }

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

		// (2a) ADMITIR — RESERVA DE ORÇAMENTO DO TURNO DE MODELO (AOS-260, D1 opção B).
		//
		// Corre AQUI e não noutro sítio: DEPOIS de o prompt estar materializado (é dele que
		// sai a estimativa honesta do input) e IMEDIATAMENTE ANTES de `rt.model.Call` — não
		// há uma única instrução entre a admissão e o efeito que ela admite.
		//
		// NEGAÇÃO ⇒ o run PÁRA AQUI, uma vez, com razão própria. Não se retenta e não se
		// devolve erro: um deny-loop cego queimaria o wall-clock e o run morreria pelo
		// disjuntor com a causa errada no log. `res.Turns` conta os turnos COMPLETOS — este
		// não chegou a existir, nenhum token foi gasto nele. Quem transforma esta paragem
		// numa decisão humana (o prompt de exaustão de AOS-263) é o ADAPTADOR, que é quem
		// tem a maquinaria HITL; o kernel pára e diz porquê.
		adm, err := rt.admitTurn(ctx, goal, stepID, turn, view)
		if err != nil {
			// Fail-closed: uma FALHA da admissão (≠ negação) é cegueira do tecto — correr
			// um agente autónomo com o admission control cego é a superfície verde que este
			// ticket remove.
			return res, err
		}
		if !adm.Admitted {
			res.Turns = turn - 1
			res.BudgetExhausted = true
			res.BudgetExhaustionReason = adm.Reason
			return res, nil
		}

		// (2) CHAMAR — Model Gateway sob span chat (ADR-010).
		resp, err := rt.callModel(ctx, goal, stepID, view)
		// (2b) SALDAR — a provisão reservada acima é substituída pelo consumo REAL da
		// resposta (usage medido + custo micro-USD de AOS-259), ou LIBERTADA quando a
		// chamada falhou. Corre nos DOIS caminhos, antes de qualquer return: uma reserva
		// que ficasse pendente por um provider intermitente esgotaria o tecto do run com
		// consumo que nunca existiu. Num turno REPRODUZIDO (replay) nada foi reservado e
		// isto é no-op — a dedup é do adaptador, por `run_id:step_id`.
		if serr := rt.settleTurn(ctx, goal, stepID, turn, adm, resp, err != nil); serr != nil && err == nil {
			// O erro do MODELO tem precedência: é a causa primeira e o saldo já libertou o
			// que havia a libertar. Só quando o turno correu bem é que a falha do saldo
			// aborta — nesse caso o tecto deixou de ser fiável, e é fail-closed.
			return res, serr
		}
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
		// captureTurn é uma closure porque a captura tem DOIS pontos de saída: o fim
		// normal do turno e a ESCALADA (AOS-021), que retorna de dentro do laço. Um run
		// suspenso cuja retoma depende de reproduzir a trajectória TEM de ter o turno
		// escalado capturado — sem isto o replay encontra a trajectória vazia e a retoma
		// é impossível. Lê turnCaptured no momento da chamada (captura por referência).
		captureTurn := func() error {
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
				return fmt.Errorf("%w: turno %d: %w", ErrCapture, turn, err)
			}
			return nil
		}
		for j, inv := range resp.ToolCalls {
			out, err := rt.mediateToolCall(ctx, goal, stepID, j, inv)
			if err != nil {
				return res, err
			}
			res.ToolResults = append(res.ToolResults, out.Result)
			turnCaptured = append(turnCaptured, CapturedToolResult{Invocation: inv, Result: out.Result, ToolError: out.ToolErr, Denial: out.Denial})
			// O tail materializa a condição de erro da tool (se houver) E o facto de a
			// call ter sido NEGADA pelo RM (rótulos sanitizados, nunca a Reason) para o
			// modelo poder reagir em vez de reemitir a mesma call às cegas; o conteúdo
			// mantém-se untrusted, append-only.
			win.Append(tailFromResultDenied(out.Result, out.ToolErr, out.Denial))

			// ESCALADA PARA HUMANO (AOS-021) — ANTES do checkpoint da activity, e é
			// crítico que seja antes: o cpActivity marca a activity como CONFIRMADA
			// (efeito concluído), e uma activity escalada NÃO produziu efeito nenhum.
			// Marcá-la faria a retoma SALTÁ-LA — a acção aprovada nunca executaria.
			// Paramos o run aqui, com o sub-passo por confirmar, para a retoma o
			// re-mediar com a evidência da aprovação.
			if out.escalated() && rt.escalation != nil {
				pending := PendingApproval{
					RunID: goal.RunID, StepID: out.Call.StepID, Turn: turn,
					ToolID: out.Call.ToolID, Capability: out.Call.Capability,
					ResourceType: out.Call.Resource.Type, ResourceValue: out.Call.Resource.Value,
					ResourceRegion: out.Call.Resource.Region,
					Preview:        referencemonitor.ApprovalPreview(out.Call),
				}
				// A CAPTURA VEM PRIMEIRO: a retoma reproduz os turnos 1..N a partir das
				// capturas. Sem capturar ESTE turno, o registo de retoma existe mas a
				// trajectória está vazia e o run suspenso fica irrecuperável. Fail-closed
				// pela mesma razão que a escalada: sem captura não há retoma possível.
				if err := captureTurn(); err != nil {
					return res, err
				}
				if err := rt.escalation.Escalate(ctx, pending); err != nil {
					// Fail-closed: se a suspensão durável falha, prosseguir deixaria o
					// agente a avançar como se nada tivesse ficado por decidir.
					return res, err
				}
				res.Turns = turn
				res.Escalated = true
				res.EscalatedPreview = pending.Preview
				return res, nil
			}

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
		if err := captureTurn(); err != nil {
			return res, err
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

		// DISJUNTOR DO AGENTE VIVO (AOS-080/081) — na MESMA fronteira de fim-de-turno da
		// pausa graciosa (todas as activities confirmadas, entre turnos e nunca a meio) e
		// DEPOIS da terminação normal, para um run que já concluiu nunca disparar. Fecha a
		// lacuna de um loop que só sabia parar por MaxTurns: aqui o run pára com um
		// VEREDICTO (sem progresso / wall-clock / velocidade de queima) já materializado
		// como transição durável pelo adaptador. Aditivo: sem [WithLivenessBreaker] o
		// comportamento de AOS-013 é byte-idêntico.
		if rt.breaker != nil {
			tripped, target, err := rt.breaker.Observe(ctx, goal.RunID, turn)
			if err != nil {
				// Fail-closed: uma falha da transição durável NÃO é engolida — continuar
				// deixaria o run a queimar recursos com o disjuntor cego.
				return res, err
			}
			if tripped {
				res.Turns = turn
				res.Tripped = true
				res.BreakerTarget = target
				return res, nil
			}
		}

		// BURN-DOWN + AVISO DE EXAUSTÃO (AOS-262) — na MESMA fronteira de fim-de-turno da
		// pausa graciosa e do disjuntor, e DEPOIS de ambos: um run que já parou (pausado ou
		// disparado) retornou acima e não é avisado sobre um orçamento que deixou de queimar.
		// É LEITURA e não decisão — o observador não pode parar o run (ver [ProgressObserver]);
		// quem pára continua a ser o disjuntor ou o operador. Corre DEPOIS de `recordTurn`
		// (mais acima neste mesmo turno), pelo que o turno corrente JÁ está no ledger que a
		// fonte lê. Um erro é FATAL: a fonte só falha quando NÃO TEM DADOS, e continuar com o
		// burn-down cego é a superfície verde a mentir que AOS-261/262 removem. Aditivo: sem
		// [WithProgressObserver] o comportamento de AOS-013 é byte-idêntico.
		if rt.progressObserver != nil {
			if err := rt.progressObserver.ObserveProgress(ctx, goal.RunID, turn); err != nil {
				return res, err
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
// Devolve o resultado (SEMPRE untrusted), a NEGAÇÃO sanitizada (nil em permit — ver
// [ToolDenial]), o erro DA TOOL (dec.ToolErr — não-fatal, para o loop materializar no
// tail) e o erro FATAL do loop (só cancelamento de contexto). Um erro da tool NÃO é uma
// negação de política: a decisão foi Permit e o efeito ocorreu, mas a execução
// downstream falhou (ADR-005 / decision.ToolErr).
//
// ADOPÇÃO DO CONTRATO DE ACTIVITY (AOS-021): o despacho passa agora pela porta
// [ActivityDispatcher] (ver ports.go, AOS-157). O default é Mediate directo (byte-
// idêntico AOS-013, no-bypass estrutural + taint garantidos); um adaptador durável no
// apex (activity.Dispatcher sobre rm + durable.StepLedger) acrescenta idempotência/
// replay pelo step-ledger à volta da MESMA mediação, SEM o loop perder o Credential
// (AOS-152) nem o taint da autorização — a porta recebe o Call já construído aqui.
// toolOutcome é o desfecho de UMA tool call mediada, agregado para não multiplicar
// valores de retorno.
type toolOutcome struct {
	// Result é o resultado devolvido ao loop (SEMPRE untrusted).
	Result Tainted
	// Denial é a decisão sanitizada quando o RM não permitiu (nil em permit).
	Denial *ToolDenial
	// ToolErr é o erro de execução de uma tool PERMITIDA (não é negação de política).
	ToolErr error
	// Call é a call construída e submetida — o loop precisa dela para descrever o
	// pendente de aprovação quando o veredicto é `escalate`.
	Call referencemonitor.Call
}

// escalated indica que o veredicto foi `escalate` (requer gate humano; nenhum efeito).
func (o toolOutcome) escalated() bool {
	return o.Denial != nil && o.Denial.Effect == string(referencemonitor.EffectEscalate)
}

func (rt *Runtime) mediateToolCall(ctx context.Context, goal Goal, parentStep string, idx int, inv ToolInvocation) (toolOutcome, error) {
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
			// A reversibilidade DECLARADA pelo registry. Sem isto o classificador recebe vazio,
			// trata a acção como irreversível, e toda a tool call sai `danger` — o que colapsa
			// a taxonomia de autonomia L0–L5 em dois estados.
			Reversibility: inv.Reversibility,
		},
		Input: inv.Input,
	}

	// FORMA FINAL DO EFEITO (AOS-005/AOS-064) — a reescrita da Call corre AQUI, na
	// construção, e não no despacho.
	//
	// PORQUÊ AQUI: a reescrita é o que DEFINE o efeito (ex.: args do modelo → ExecRequest
	// de sandbox). Tudo a jusante — a preview que o humano aprova, a chave do step-ledger,
	// o registo de audit, a mediação do RM — tem de descrever O MESMO efeito. Enquanto
	// corria dentro do dispatcher, o loop descrevia ao mundo o efeito PRÉ-reescrita e o RM
	// mediava o PÓS-reescrita: as duas previews divergiam e a aprovação humana, embora
	// emitida e consumida, nunca casava com a acção (observado ao vivo). Fazê-la na
	// construção elimina a divergência POR CONSTRUÇÃO.
	//
	// Fail-closed: uma reescrita que falha (args malformados) NÃO despacha nada e
	// materializa-se como Deny no tail — não é fatal para o loop.
	if rt.callRewriter != nil {
		rc, rerr := rt.callRewriter(call)
		if rerr != nil {
			return toolOutcome{
				Result: Untrusted(nil),
				Denial: &ToolDenial{
					Effect:   string(referencemonitor.EffectDeny),
					Code:     CodeEffectRewrite,
					DeniedBy: "effect_rewriter",
				},
				Call: call,
			}, nil
		}
		call = rc
	}

	// EVIDÊNCIA DE APROVAÇÃO (AOS-021) — na RETOMA de uma acção escalada, é aqui que a
	// prova volta a acompanhar a call. A consulta é pela PREVIEW da call já construída
	// (a amarra exacta): uma call diferente da aprovada tem outra preview e não obtém
	// evidência. A fonte é infraestrutura TRUSTED do nó, nunca o modelo — e os bytes
	// continuam opacos até o ApprovalGate os VERIFICAR. Sem fonte ligada, nada muda.
	if rt.approvalEvidence != nil {
		call.ApprovalEvidence = rt.approvalEvidence.EvidenceFor(ctx, goal.RunID, referencemonitor.ApprovalPreview(call))
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
		return toolOutcome{}, err // apenas cancelamento de contexto
	}

	// SINAL DE NO-PROGRESS (AOS-251) — a mediação FECHOU (o span execute_tool terminou,
	// qualquer que seja o veredicto). Reporta o hash canónico da acção ao observador: é a
	// MESMA âncora que o RM acabou de anotar no span
	// ([otelgenai.CanonicalToolCallHash] sobre a call JÁ reescrita), pelo que o detector de
	// acções repetidas e a telemetria falam da mesma "acção". Aditivo: sem observador
	// ligado, nada muda.
	if rt.actionObserver != nil {
		rt.actionObserver(goal.RunID, otelgenai.CanonicalToolCallHash(call.ToolID, call.Input))
	}

	// NEGAÇÃO SANITIZADA para o tail (AOS-013 gap 2): num veredicto não-permit, o loop
	// materializa o FACTO da negação + rótulos de enumeração fechada, para o modelo não
	// confundir "negado" com "a tool não devolveu nada". Reason NUNCA sai daqui — ver
	// [ToolDenial]. Em permit fica nil ⇒ tail byte-idêntico ao de antes.
	var denial *ToolDenial
	if dec.Effect != referencemonitor.EffectPermit {
		denial = &ToolDenial{
			Effect:   string(dec.Effect),
			Code:     dec.Code,
			DeniedBy: dec.DeniedBy,
		}
	}

	// Resultado devolvido ao loop SEMPRE marcado untrusted. Só há Output em permit.
	return toolOutcome{
		Result:  Untrusted(dec.Output),
		Denial:  denial,
		ToolErr: dec.ToolErr,
		Call:    call,
	}, nil
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
