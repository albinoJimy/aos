package agentruntime

import (
	"context"
	"fmt"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
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
	// Subject é o TITULAR DOS DADOS do run (AOS-440): a KEK por-titular sob a qual o conteúdo
	// não-determinístico (texto do modelo, outputs de tools) é selado — na captura do turno e no
	// step-ledger — e a que um apagamento DSAR tem de destruir para o tornar ilegível. SEPARADO do
	// `Principal.NHIID`, que continua a ser o produtor dos eventos e o atributo do span: num run
	// filho de um plano quem chama o nó é o drenador, e os dados são de quem pediu o plano.
	//
	// Vazio ⇒ `Principal.NHIID` ([Goal.Titular]) — o que todos os runs anteriores usaram, e o que
	// continua a valer para um run que não é trabalho de um plano.
	Subject string
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
	// Tools é o tool set OFERECIDO ao modelo neste run (ordem significativa, nunca reordenada):
	// é dele que saem o bloco TOOLSET do prefixo e o `tools` do manifesto do turno. Num run sem
	// lista-branca é o tool set congelado inteiro; num run com [Goal.AllowedTools] é a
	// subsequência que a lista admite, na ordem congelada (AOS-486) — quem compõe o run faz o
	// corte, o loop usa a lista tal como a recebe.
	Tools []ToolSpec
	// AllowedTools é a LISTA-BRANCA de tools (por nome, o `ToolID`) que este run pode chamar
	// (AOS-413, ADR-027). nil ⇒ sem restrição além do token — o comportamento de sempre.
	// Não-nil ⇒ uma tool call fora dela é NEGADA pelo Reference Monitor, sem despacho, e a
	// recusa fica no log como qualquer outra (AOS-485): o ciclo entrega a lista ao RM em
	// cada call ([referencemonitor.Call.AllowedTools]) e é ele que a impõe. Uma lista
	// não-nil VAZIA nega TODAS — é o caso de um nó do plano sem tools pinadas, que de outro
	// modo herdaria as tools de todo o token do run. Existe
	// para um run que é o trabalho de UM nó de um plano: o nó só pode usar as tools que o
	// plano lhe pinou, e não as de todo o run que o token autoriza. ESTREITA a autoridade,
	// nunca a alarga: uma tool na lista continua sujeita a todo o RM.
	AllowedTools []string
	// Skills são as skills pinadas do run (vão ao manifesto).
	Skills []ToolSpec
	// Objective é a instrução inicial (semeia o tail append-only, trusted).
	Objective string
	// MemoryContext é o contexto de memória injectado no tail (ver EPIC-04).
	MemoryContext []byte
	// Inputs são os payloads que o PLANO declarou que este nó consome (AOS-414): produto de
	// outros runs, entregue por quem submete. Entram no tail como segmentos
	// [TailPlanInput], marcados `taint=untrusted` e com a proveniência do contrato. Vazio ⇒
	// nada muda no prompt (um run que não é nó de um plano nunca os tem).
	Inputs []PlanInput
	// RetryNotice declara que este run é a NOVA TENTATIVA de um trabalho cuja tentativa anterior
	// acabou de uma forma que quem compõe o run provou (AOS-506, ADR-039 §2.7). Vazio ⇒ nada
	// muda no prompt. Não vazio ⇒ a semente do tail ganha, a seguir ao objectivo, um segmento
	// `notice` de texto CONSTANTE escrito pelo kernel ([RetryNotice]): quem compõe o run escolhe
	// um valor de vocabulário fechado e não escreve um byte do prompt. Um valor desconhecido, ou
	// um layout sem `notice` ⇒ o run não arranca ([ErrUnknownRetryNotice]).
	RetryNotice RetryNotice
	// MaxTurns limita o nº de iterações (0 ⇒ [DefaultMaxTurns]).
	MaxTurns int
	// AssemblyVersion FIXA o layout de montagem do prompt deste run (AOS-489): o prefixo, a
	// sequência de segmentos de cada turno e o `manifest.assembly_version` que cada turno
	// grava. Vazio ⇒ o layout dos runs NOVOS (o do [Runtime], por omissão [AssemblyVersion]).
	//
	// Só quem RE-HOSPEDA um run o preenche: a retoma por aprovação e a recuperação de crash
	// repõem aqui a versão que ficou no registo de retoma, para o run continuar no layout em
	// que começou. Um run que mudasse de layout a meio ficava com um log misto, e os turnos
	// já dados — que a retoma REPRODUZ desde o turno 1 — seriam remontados com outros bytes
	// do que os gravados. Versão desconhecida ⇒ o run não arranca
	// ([ErrUnknownAssemblyVersion]).
	AssemblyVersion string
	// CompletionRequires é o CONTRATO DE CONCLUSÃO do run (AOS-493, ADR-037): os nomes (o
	// `ToolID`) das tools de que a conclusão depende. O run só conclui cumprido com pelo menos
	// uma chamada EFECTIVA de cada uma — despachada, sem recusa e sem erro de tool. Vazio ⇒ sem
	// contrato. Quem o declara é quem compõe o run; nunca sai de conteúdo do modelo.
	CompletionRequires []string
	// CompletionMode é o MODO DE APLICAÇÃO do veredicto, fixado por run como o layout: vazio ou
	// [CompletionOff] ⇒ o desfecho de sempre, sem veredicto; [CompletionObserve] ⇒ o veredicto
	// calcula-se e vai no [Result], sem mudar o desfecho; [CompletionEnforce] ⇒ um veredicto
	// negativo fecha o run como não cumprido ([Result.Unfulfilled]). Um valor fora do
	// vocabulário ⇒ o run não arranca ([ErrUnknownCompletionMode]). Vai no manifesto de cada
	// turno, para o replay chegar ao mesmo desfecho.
	CompletionMode CompletionMode
	// OutputFromTool é a ORIGEM DECLARADA da saída do run (AOS-497, ADR-038): o nome (o `ToolID`)
	// da tool cujo resultado é a saída. Vazio ⇒ sem declaração, e o run grava os bytes de sempre.
	// Quem a declara é quem compõe o run; nunca se infere nem sai de conteúdo do modelo. Com ela,
	// o kernel designa qual chamada é a origem e devolve a âncora ([Result.OutputSource]). Uma
	// tool fora do tool set ou da lista-branca, ou com um nome que a âncora selada não admite ⇒
	// o run não arranca ([ErrImpossibleOutputSource]).
	// Com o modo do veredicto desligado é ignorada, como o contrato.
	OutputFromTool string
	// OutputSourceBinding é o VÍNCULO dessa declaração, dado pelo chamador e fixado por run:
	// [OutputSourceMeasure] (a âncora sela-se e o desfecho é o de um run sem declaração) ou
	// [OutputSourceBinds] (em imposição, origem em falta ou ambígua fecha o run como não
	// cumprido). Obrigatório com a origem declarada e proibido sem ela
	// ([ErrBadOutputSourceBinding]). Vai, com a origem, no manifesto de cada turno.
	OutputSourceBinding OutputSourceBinding

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
	// CustoNaoDerivado diz que pelo menos um turno do run não teve custo derivado (AOS-406):
	// TotalCostMicroUSD é então uma soma sem fonte, não o custo do run.
	CustoNaoDerivado bool
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
	// Unfulfilled indica que o run ACABOU SEM CONCLUIR (AOS-493): o turno que o terminou teve
	// veredicto negativo e o run corria em modo de imposição. Não é erro do loop (como
	// BudgetExhausted, é um desfecho declarado), Terminated fica false e FinalText fica vazio:
	// o que o modelo escreveu nesse turno não é resposta de nada.
	Unfulfilled bool
	// Verdict é o veredicto do kernel sobre a conclusão do run, calculado no turno que o
	// terminou. nil quando o run não chegou a um turno terminal ou corria com o modo
	// desligado. Em modo de observação um veredicto negativo vem com Terminated=true.
	Verdict *Verdict
	// OutputSource é a âncora da saída do run (AOS-497): a designação da origem declarada, com o
	// digest do resultado designado. nil quando o run não declarou a origem, corria com o modo
	// desligado ou não chegou a um turno terminal. Sem conteúdo.
	OutputSource *OutputSource
	// ToolCallsRequested é o total de tool calls que o modelo pediu e o loop despachou no run,
	// qualquer que seja o modo. É medição.
	ToolCallsRequested int
	// ToolsOffered é o tamanho do TOOL SET DO RUN ([Goal.Tools], tal como o loop o recebeu) — o
	// que o prefixo do prompt e o manifesto pinam. NÃO é o número de schemas que o cliente de
	// modelo enviou ao provider em cada turno: esse é [ModelResponse.ToolsOffered] (AOS-491), por
	// turno, e pode ser menor. É medição: serve a quem conta os runs que concluíram sem pedir
	// nenhuma tool tendo-as no tool set. Não entra no veredicto.
	ToolsOffered int
	// LastToolOutcome diz sobre o que o run acabou ([RunEvidence.LastToolOutcome]): o desfecho
	// do último turno que despachou tool calls — `none`, `effective`, `denied` ou `tool_error`.
	// Preenchido em qualquer modo e em todos os caminhos de saída. É medição.
	LastToolOutcome string
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
	toolCallStats    ToolCallStats          // AOS-489: tool calls despachadas e repetidas, por turno (leitura)
	stopReasonStats  StopReasonStats        // AOS-491: o motivo de paragem de cada turno (leitura)
	admission        ModelAdmission         // AOS-260: admissão do TURNO DE MODELO (reserva antes, saldo depois)
	progressObserver ProgressObserver       // AOS-262: burn-down + aviso a ~limiar (leitura, nunca decisão)
	escalation       EscalationSink         // AOS-021: tool call escalada → espera por humano
	approvalEvidence ApprovalEvidenceSource // AOS-021: prova de aprovação a anexar na retoma
	windowFactory    WindowFactory          // AOS-037: dono único do tail/assembly (D-TAIL)
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

// PlanInput é um payload consumido de outro nó do plano (AOS-414, ADR-022 §2.3): o contrato
// que o declara (nó de origem e nome do output), o digest do conteúdo e o conteúdo. É SEMPRE
// untrusted no prompt; o digest serve a integridade, não a confiança.
type PlanInput struct {
	From    string
	Output  string
	Digest  string
	Content []byte
}

// CodeToolOutsideRunAllowlist é o Code de Deny quando a tool call não está na lista-branca
// [Goal.AllowedTools] do run (AOS-413). Nada é despachado. É o MESMO símbolo do Reference
// Monitor, que é quem nega desde o AOS-485 — não uma segunda cópia da string, para o código
// que o modelo vê no tail não poder divergir do que fica no evento e no selo.
const CodeToolOutsideRunAllowlist = referencemonitor.CodeToolOutsideRunAllowlist

// WithCallRewriter injecta o [CallRewriter]. Default: nenhum (Call inalterada).
func WithCallRewriter(r CallRewriter) Option { return func(rt *Runtime) { rt.callRewriter = r } }

// WithAssemblyVersion fixa o LAYOUT de montagem dos runs que não tragam o seu
// ([Goal.AssemblyVersion]); por omissão [AssemblyVersion]. Desde o AOS-489 a versão não é só o
// que o manifesto grava: é o layout com que o prompt é de facto montado, pelo que tem de ser
// uma versão que o assembler conheça — outra faz o run falhar no arranque
// ([ErrUnknownAssemblyVersion]). Vazio ⇒ [AssemblyVersion].
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

// openWindow constrói a janela do run JÁ decorada com o rótulo de autoridade (ADR-034). É o
// único sítio que vê a janela da fábrica; fail-closed: sem janela não há prompt a montar.
func (rt *Runtime) openWindow(goal Goal, lay layout) (*authorityWindow, error) {
	w, err := rt.windowFactory.NewWindow(goal.RunID, goal.System, goal.Tools, lay.version)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWindow, err)
	}
	return newAuthorityWindow(w), nil
}

// layoutDoRun resolve o layout em que o run fica FIXADO (AOS-489): o do [Goal], quando quem o
// submete o traz (a retoma de um run começado noutra versão), ou o do runtime. É resolvido UMA
// vez, antes do primeiro turno, e é o mesmo valor que dá o prefixo, a sequência de segmentos
// e o `assembly_version` do manifesto — as três coisas não têm por onde discordar.
func (rt *Runtime) layoutDoRun(goal Goal) (layout, error) {
	v := goal.AssemblyVersion
	if v == "" {
		v = rt.assemblyVersion
	}
	return layoutFor(v)
}

// Titular devolve o titular dos dados do run: [Goal.Subject], ou o `Principal.NHIID` quando vazio
// (AOS-440). É a ÚNICA regra — a captura, o step-ledger (pela Activity), o registo de retoma e o
// selo terminal perguntam-lha a ela.
func (g Goal) Titular() string {
	if g.Subject != "" {
		return g.Subject
	}
	return g.Principal.NHIID
}

// callPrincipal é o Principal que o loop põe em cada tool call: o do Goal, com o titular DERIVADO
// (AOS-440). É por ele que o titular atravessa a via durável até ao step-ledger — a Activity leva o
// Principal inteiro —, sem que a porta da Activity tenha de mudar.
func (g Goal) callPrincipal() referencemonitor.Principal {
	p := g.Principal
	p.Subject = g.Titular()
	return p
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
	//
	// AUTORIZAÇÃO DERIVADA DO CONTEXTO (AOS-069, ADR-034): o loop só conhece a janela
	// decorada, que junta o rótulo de cada segmento ao do contexto. É daqui — e não da
	// resposta do modelo — que sai o taint da autorização de cada tool call. A janela de
	// baixo nunca tem nome neste âmbito ([Runtime.openWindow]): um Append que a contornasse
	// não é escrevível sem se ver.
	//
	// LAYOUT FIXADO POR RUN (AOS-489): resolvido aqui, antes de qualquer efeito, e fail-closed
	// — um run cuja versão este assembler não conhece não chega a montar um prompt.
	lay, err := rt.layoutDoRun(goal)
	if err != nil {
		return Result{}, err
	}
	// A SEMENTE DO TAIL resolve-se aqui, antes de qualquer efeito (AOS-506): um aviso de nova
	// tentativa fora do vocabulário recusa o run antes de haver janela, evento ou span.
	semente, err := seedDoRun(goal, lay)
	if err != nil {
		return Result{}, err
	}
	win, err := rt.openWindow(goal, lay)
	if err != nil {
		return Result{}, err
	}
	// A sequência de segmentos do run (AOS-489): a MESMA que o motor de replay usa.
	sequencia := lay.novaSequencia()
	// O VEREDICTO DO RUN (AOS-493): o modo e o contrato resolvem-se aqui, antes de qualquer
	// efeito, e são os que cada turno grava no manifesto. Os contadores são os do próprio loop.
	conclusao, err := completionDoGoal(goal)
	if err != nil {
		return Result{}, err
	}
	// CONTRATO IMPOSSÍVEL ⇒ O RUN NÃO ARRANCA (AOS-493, revisão I5). Aqui, e não mais acima,
	// porque é neste ponto que [Goal.Tools] e [Goal.AllowedTools] são os definitivos: quem
	// compõe o run já cortou a oferta pela lista-branca (AOS-486) e o Reference Monitor vai
	// impor a mesma lista em cada chamada (AOS-485). Antes de qualquer evento, span ou turno.
	if err := contratoPossivel(conclusao, goal); err != nil {
		return Result{}, err
	}
	// A mesma recusa para a origem declarada da saída (AOS-497).
	if err := origemPossivel(conclusao, goal); err != nil {
		return Result{}, err
	}
	evidencia := NewRunEvidence()
	if conclusao != nil {
		// A evidência segue a tool declarada desde o início: os factos da designação e o digest
		// tiram-se quando o primeiro turno com tools é observado (AOS-497).
		evidencia.FollowOutputFrom(conclusao.OutputFrom)
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

	res := Result{RunID: goal.RunID, ToolsOffered: len(goal.Tools), LastToolOutcome: evidencia.LastToolOutcome()}

	// Anotar o uso/custo AGREGADO no span invoke_agent em TODOS os caminhos de
	// saída — inclusive nos returns de erro (ErrModelCall/ErrTurnRecord/checkpoint/
	// cancelamento) — para não perder o burn-down parcial de uma run falhada. O
	// defer lê a variável `res`, que carrega o acumulado no momento do return.
	defer func() {
		rt.annotateAgentSpan(agentSpan, res)
		agentSpan.End()
	}()

	// Tail append-only, semeado com memory_context + objectivo. O tail é agora propriedade
	// da [WindowPort] (D-TAIL) — o loop só lhe entrega segmentos.
	//
	// OS DOIS NÃO TÊM O MESMO ESTATUTO, e esta nota dizia "(trusted)" para ambos.
	//
	// O OBJECTIVO é trusted com fundamento: vem de `req.Objective` de uma submissão
	// autenticada (cmd/aos/api.go) e nunca de saída de modelo — verificado a 2026-08-28,
	// incluindo o caminho de delegação, cujo `SpawnRequest` transporta identidade,
	// orçamento e profundidade, mas NÃO um objectivo.
	//
	// O MEMORY_CONTEXT não tem fundamento nenhum, e hoje isso não custa nada porque
	// NINGUÉM O PREENCHE: nenhum caminho de produção atribui `Goal.MemoryContext` (o
	// EPIC-04 ainda não o ligou), pelo que este segmento nunca chega a ser emitido.
	//
	// QUANDO O EPIC-04 O LIGAR, A CORRECÇÃO NÃO É PÔR-LHE UM RÓTULO AQUI. Um `taint=` na
	// linha de delimitação deste segmento seria uma tag in-band, e `tecnica/04` §
	// "memory poisoning" e `specs/EPIC-07` recusam-nas explicitamente para esta classe:
	// «tags in-band não são separação de privilégio». A defesa declarada é taint tracking
	// com proveniência (ADR-005) e a separação de planos, cujo registo é o DEF-806.
	//
	// (A palavra-marcador foi evitada de propósito nesta nota: isto CITA o DEF-806, não
	// cria dívida nova, e o gate `deferrals` — que faz bem em não distinguir uma da outra
	// — contaria a citação como um deferimento por registar.)
	//
	// Um rótulo aqui seria pior do que a ausência dele: leria como se o envenenamento de
	// memória estivesse tratado.
	//
	// Na AUTORIDADE a memória já conta (ADR-034): o segmento `memory` torna o contexto
	// untrusted, FAIL-CLOSED, pelo que nenhuma tool call privilegiada pode ser pedida depois
	// dele. Isso não é a defesa contra o envenenamento — é só a garantia de que memória sem
	// proveniência verificada não autoriza nada.
	//
	// AOS-414: os payloads do plano ANTES do objectivo — primeiro o material sobre o qual se
	// trabalha (untrusted), depois a instrução (trusted). A ordem é a mesma do par
	// resultado-de-tool → turno seguinte, e mantém as instruções do runtime como as últimas a
	// falar. A construção é a de [seedDoRun] — a mesma que [SeedTail] expõe.
	for _, seg := range semente {
		win.Append(seg)
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
		// A JANELA MONTOU NO LAYOUT DO RUN? (AOS-489) A fábrica recebe a versão, mas é uma
		// porta: uma implementação que a ignorasse daria um prefixo de um layout com a
		// sequência de segmentos de outro, e o manifesto gravaria uma versão que não é a dos
		// bytes. Fail-closed, antes de o prompt chegar ao modelo.
		if view.AssemblyVersion != lay.version {
			return res, fmt.Errorf("%w: a janela montou o turno %d no layout %q e o run esta fixado em %q",
				ErrWindow, turn, view.AssemblyVersion, lay.version)
		}
		// O rótulo do contexto que o modelo vai ver NESTE turno. Autoriza todas as tool calls
		// que o turno pedir — lido aqui, antes de a resposta existir, para que nada do que o
		// modelo devolva (texto, tool calls, resultados) o possa mudar retroactivamente.
		turnAuthority := win.authority()
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
		// O MOTIVO DE PARAGEM ENTRA NO RUNTIME JÁ FECHADO (AOS-491). O cliente de modelo é uma
		// porta: um que devolvesse o texto do provider tal como veio poria esse texto na
		// captura, no `turn.recorded` e num rótulo de métrica. Daqui para baixo `resp` só tem
		// um valor do vocabulário. O loop não decide o despacho por ele; entra só no veredicto do
		// turno que acaba o run ([ConcludeRun], AOS-493).
		resp.StopReason = resp.StopReason.Normalizado()
		if resp.ToolsOffered < 0 {
			// Uma contagem negativa não existe; não chega ao registo.
			resp.ToolsOffered = 0
		}
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseModelCalled); err != nil {
			return res, err
		}
		if rt.stopReasonStats != nil {
			rt.stopReasonStats(goal.RunID, resp.StopReason)
		}
		res.TotalUsage.InputTokens += resp.Usage.InputTokens
		res.TotalUsage.OutputTokens += resp.Usage.OutputTokens
		res.TotalUsage.CacheReadTokens += resp.Usage.CacheReadTokens
		res.TotalCostMicroUSD += resp.CostMicroUSD
		res.CustoNaoDerivado = res.CustoNaoDerivado || resp.CustoNaoDerivado

		// Gravar o turno com o manifesto por trajectória.
		seq, err := rt.recordTurn(ctx, goal, win.SystemHash(), lay.version, conclusao, stepID, turn, view, resp, producer)
		if err != nil {
			return res, err
		}
		res.TurnSeqs = append(res.TurnSeqs, seq)
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseTurnRecorded); err != nil {
			return res, err
		}

		// O QUE O TURNO ACRESCENTA AO TAIL (o prefixo nunca muda) é decidido num só sítio —
		// [TailSequence.Turn], a MESMA função que o motor de replay usa — e acrescentado
		// de uma vez, quando o despacho do turno acaba ([fecharTail], abaixo): o texto do
		// modelo e, por cada tool call despachada, a chamada e o seu resultado (AOS-489).
		// Antes do AOS-489 o texto era acrescentado aqui e cada resultado dentro do laço de
		// despacho, e o motor espelhava essa ordem à mão. A ordem dos segmentos é a mesma; o
		// tail só é lido no Assemble do turno seguinte, pelo que o momento do append dentro
		// do turno não é observável.
		//
		// No PROMPT, a saída do modelo — texto e tool calls — leva `taint=untrusted`, a mesma
		// marcação de proveniência dos resultados de tool (consistência de auditoria,
		// ADR-005). Na AUTORIDADE, herda o rótulo do contexto que a produziu
		// ([SegmentAuthority]): não o eleva nem o baixa.
		//
		// O QUE O ADR-034 FECHOU E O QUE NÃO FECHOU. Fechou a AUTORIZAÇÃO: uma tool call
		// pedida depois de conteúdo untrusted entrar no tail (plan_input, tool_result,
		// memória) sai com taint untrusted, cunhado aqui a partir do contexto, e o
		// [referencemonitor.TaintGate] nega-a se a capability for privilegiada. Não fechou a
		// SEPARAÇÃO DE PLANOS por handle (dual-LLM/CaMeL, opção A): o conteúdo untrusted
		// continua a entrar INLINE no tail que o modelo lê, e não passa por
		// [SeparatePlanes]/[ControlPlanner]/[Quarantine]. Essa separação fica DIFERIDA no
		// DEF-806 (eixo AOS-069), re-escopada a efeitos parametrizados por dados untrusted,
		// com gatilho: a entrada de uma tool de efeito cujos argumentos venham de conteúdo
		// untrusted.

		// (3) DESPACHAR — cada tool call PRETENDIDA via o Reference Monitor.
		// turnCaptured acumula os resultados DESTE turno (com a invocação original)
		// para a captura de não-determinismo (AOS-016), sem afectar res.ToolResults.
		var turnCaptured []CapturedToolResult
		// fecharTail acrescenta ao tail o que este turno produziu. Tem os MESMOS dois pontos
		// de saída da captura — o fim normal do turno e a escalada —, e corre sempre ANTES
		// dela: o tail e a captura descrevem o mesmo turno a partir do mesmo `turnCaptured`.
		// Na escalada o run pára a seguir e esta janela não volta a ser montada (a retoma
		// re-hospeda desde o turno 1); fecha-se o tail na mesma, para que as duas saídas
		// deixem o mesmo estado e uma mudança futura não tenha de se lembrar da diferença.
		//
		// Os ARGUMENTOS que entram no `tool_call` são os de `turnCaptured[i].Invocation` — a
		// tool call tal como o modelo a emitiu. O `Call` reescrito pelo [CallRewriter] (e a
		// capability, o recurso e a reversibilidade que ele leva) nunca chega aqui.
		//
		// Corre UMA vez por turno (a escalada retorna logo a seguir), e é isso que deixa a
		// medição de repetições ([ToolCallStats]) sair daqui sem contar um turno duas vezes.
		fecharTail := func() {
			segs, repetidas := sequencia.Turn(stepID, resp.Text, turnCaptured)
			for _, seg := range segs {
				win.Append(seg)
			}
			if rt.toolCallStats != nil && len(turnCaptured) > 0 {
				rt.toolCallStats(goal.RunID, len(turnCaptured), repetidas)
			}
			// Os contadores do veredicto (AOS-493) saem do MESMO `turnCaptured` que o tail e a
			// captura, e pela mesma razão do comentário acima somam cada turno uma só vez.
			// Levam o passo e o rótulo do contexto DESTE turno: é do primeiro turno que despachou
			// tool calls que a origem da saída se designa (AOS-497).
			evidencia.Observe(stepID, turnAuthority, turnCaptured)
			res.ToolCallsRequested = evidencia.ToolCallsRequested()
			res.LastToolOutcome = evidencia.LastToolOutcome()
		}
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
				// AOS-093: o TITULAR do run sob cuja chave por-titular o capturer cifra o
				// conteúdo não-determinístico antes do ES. AOS-440: o titular dos DADOS
				// ([Goal.Titular]) — o submissor, num run filho de um plano —, e não o chamador.
				Subject: goal.Titular(),
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
			out, err := rt.mediateToolCall(ctx, goal, stepID, j, inv, turnAuthority)
			if err != nil {
				return res, err
			}
			res.ToolResults = append(res.ToolResults, out.Result)
			// O tail vai materializar a condição de erro da tool (se houver) E o facto de a
			// call ter sido NEGADA pelo RM (rótulos sanitizados, nunca a Reason) para o
			// modelo poder reagir em vez de reemitir a mesma call às cegas; o conteúdo
			// mantém-se untrusted, append-only. É de `turnCaptured` que [fecharTail] o tira.
			turnCaptured = append(turnCaptured, CapturedToolResult{Invocation: inv, Result: out.Result, ToolError: out.ToolErr, Denial: out.Denial})

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
					Principal:      out.Call.Principal,
				}
				// A CAPTURA VEM PRIMEIRO: a retoma reproduz os turnos 1..N a partir das
				// capturas. Sem capturar ESTE turno, o registo de retoma existe mas a
				// trajectória está vazia e o run suspenso fica irrecuperável. Fail-closed
				// pela mesma razão que a escalada: sem captura não há retoma possível.
				fecharTail()
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
			//
			// SÓ CONFIRMA O QUE PRODUZIU EFEITO, e é essa a condição que faltava. O
			// doc de [Runtime.cpActivity] promete «consistência checkpoint↔ledger»,
			// mas o checkpoint corria para TODO o j — incluindo os que o ledger NÃO
			// memoriza:
			//
			//	negado/escalado ⇒ o effect nem chega a correr (activity.ErrMediationDenied)
			//	tool falhada    ⇒ nada memorizado, passo declarado RETRIÁVEL
			//
			// Confirmar um desses põe o cursor a dizer «feito» sobre um passo que o
			// ledger diz «por aplicar», e a retoma SALTA-O — é a mesma corrupção que o
			// ramo da escalada evita ao retornar antes (ver o bloco acima). A diferença
			// é que a escalada é uma paragem e estes dois continuam o turno, pelo que
			// não bastava retornar: é preciso não confirmar e seguir.
			if out.Denial == nil && out.ToolErr == nil {
				if err := rt.cpActivity(ctx, goal.RunID, stepID, turn, j, len(resp.ToolCalls)); err != nil {
					return res, err
				}
			}
		}

		// CAPTURA (AOS-016) — persiste os inputs não-determinísticos do turno (a
		// resposta do modelo COMPLETA + o output de cada tool call) para o replay
		// reconstruir a trajectória sem re-executar o modelo nem os efeitos. É
		// ADITIVA: default no-op ⇒ AOS-013 inalterado. Corre DEPOIS do despacho
		// (para captar os resultados das tools) e ANTES da verificação.
		fecharTail()
		if err := captureTurn(); err != nil {
			return res, err
		}

		// (4) VERIFICAR — terminação simples (a máquina de estados é AOS-017).
		if err := rt.cp(ctx, goal.RunID, stepID, turn, PhaseVerified); err != nil {
			return res, err
		}
		// TERMINAÇÃO — uma resposta final acaba o run (não se pausa um run já concluído). A
		// regra é a de [TurnEndsRun], a MESMA função que o motor de replay usa (AOS-492).
		termina, err := TurnEndsRun(resp, lay.version)
		if err != nil {
			return res, err
		}
		if termina {
			// O DESFECHO é de [ConcludeRun] (AOS-493), a MESMA função que o motor de replay usa:
			// acabar o run e acabá-lo bem deixaram de ser a mesma pergunta.
			fim, err := ConcludeRun(resp, conclusao, evidencia)
			if err != nil {
				return res, err
			}
			res.Turns = turn
			res.Terminated = fim.Terminated
			res.FinalText = fim.FinalText
			res.Unfulfilled = fim.Unfulfilled
			res.Verdict = fim.Verdict
			res.OutputSource = fim.OutputSource
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
			if corr, ok := rt.steer.PendingCorrection(ctx, goal.RunID); ok {
				for _, seg := range sequencia.Correction(corr) {
					win.Append(seg)
				}
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

	// AOS-394: o run e o passo seguem no ctx até ao ModelClient, para que quem sela a chamada
	// (os selos de governação do Model Gateway) a ligue ao passo exacto deste turno.
	chatCtx = ContextWithModelCall(chatCtx, goal.RunID, stepID)
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
	if resp.CustoNaoDerivado {
		// AOS-406: sem fonte de preço não se emite custo nenhum — um `aos.cost.micro_usd` a zero
		// seria lido como turno gratuito pela agregação e pelo SLI de custo por trajectória.
		span.SetAttribute(AttrCostUndefined, true)
	} else {
		span.SetAttribute(AttrCostUSD, microUSDToUSD(resp.CostMicroUSD))
		span.SetAttribute(AttrCostMicroUSD, resp.CostMicroUSD)
	}
	span.End()
	return resp, nil
}

// recordTurn constrói o manifesto e grava o evento "turn.recorded".
func (rt *Runtime) recordTurn(ctx context.Context, goal Goal, systemHash, assemblyVersion string, conclusao *Completion, stepID string, turn int, view PromptView, resp ModelResponse, producer eventstore.Producer) (uint64, error) {
	manifest := Manifest{
		PromptHash: view.PromptHash,
		SystemHash: systemHash,
		// O layout em que o run está FIXADO (AOS-489) — o mesmo que montou `view` (o loop
		// verificou-o) —, e não uma constante: é por ele que o replay escolhe, turno a turno,
		// o layout com que re-materializa.
		AssemblyVersion: assemblyVersion,
		Model: ModelManifest{
			ModelID:       goal.Model.ModelID,
			ServedModelID: resp.Model,
			Params:        goal.Model.Params,
			Seed:          goal.Model.Seed,
			// AOS-505: o digest do perfil da rota, declarado pelo cliente. Vazio com a
			// governação da rota desligada, e o manifesto fica com os bytes de antes.
			RouteProfileDigest: resp.RouteProfileDigest,
		},
		Tools:  pinnedDeps(goal.Tools),
		Skills: pinnedDeps(goal.Skills),
		// A forma em que o prompt foi ENVIADO (AOS-490): declarada por quem fez o pedido, na
		// resposta. Vazia ⇒ texto único, e o manifesto fica com os bytes de antes.
		Projection:        resp.Projection,
		ProjectionVersion: resp.ProjectionVersion,
		// O modo e o contrato com que o run corre (AOS-493). nil com o modo desligado, e o
		// manifesto fica com os bytes de antes.
		Completion: conclusao,
	}
	seq, err := rt.recorder.Record(ctx, TurnRecord{
		RunID:            goal.RunID,
		StepID:           stepID,
		Turn:             turn,
		Manifest:         manifest,
		Usage:            resp.Usage,
		CostMicroUSD:     resp.CostMicroUSD,
		CustoNaoDerivado: resp.CustoNaoDerivado,
		ToolCalls:        len(resp.ToolCalls),
		Final:            resp.Final,
		// AOS-491: o motivo de paragem do turno e quantas tools o pedido ofereceu ao modelo,
		// os dois declarados pelo cliente na resposta.
		StopReason:   resp.StopReason,
		ToolsOffered: resp.ToolsOffered,
		// AOS-505: o resultado da comparação da rota, declarado pelo cliente.
		RouteCheck: resp.RouteCheck,
		// AOS-507: a ficha da forma da resposta, declarada pelo cliente. nil com a medição
		// desligada, e o evento fica com os bytes de antes.
		ResponseShape: resp.Shape,
		Producer:      producer,
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
// downstream falhou (ADR-005 / decision.ToolErr). authority é o rótulo do contexto no
// Assemble do turno que pediu a call (ADR-034) e vai tal e qual para o CallContext.Taint.
//
// ADOPÇÃO DO CONTRATO DE ACTIVITY (AOS-021): o despacho passa agora pela porta
// [ActivityDispatcher] (ver ports.go, AOS-157). O default é Mediate directo (byte-
// idêntico AOS-013, no-bypass estrutural + taint garantidos); um adaptador durável no
// apex (activity.Dispatcher sobre rm + durable.StepLedger) acrescenta idempotência/
// replay pelo step-ledger à volta da MESMA mediação, SEM o loop perder o Credential
// (AOS-152) nem o taint da autorização — a porta recebe o Call já construído aqui.
// «Recebe o Call» não bastava: o adaptador durável traduzia-o numa Activity sem o taint e
// o RM de produção via untrusted em todas as calls (fase 1 do AOS-069, 2026-09-26). O que
// fixa a propriedade é a paridade entre as duas vias
// (`TestAOS069_ViaDuravelPreservaOTaintDaAutorizacao`, packages/integration).
//
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

func (rt *Runtime) mediateToolCall(ctx context.Context, goal Goal, parentStep string, idx int, inv ToolInvocation, authority taint.Label) (toolOutcome, error) {
	// step_id distinto: evento de mediação próprio. É também o `id` do `tool_call` e do
	// `tool_result` no prompt (AOS-489) — o mesmo [ToolStepID], para que o que o modelo vê e o
	// que o log regista falem da mesma chamada.
	toolStep := ToolStepID(parentStep, idx)

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
		// AOS-440: com o titular derivado — ver [Goal.callPrincipal].
		Principal: goal.callPrincipal(),
		// Credential do run propagado à call: é AQUI que o token NHI chega ao hook de
		// identidade (AOS-152). Vazio ⇒ anónimo ⇒ deny fail-closed sob o hook real.
		Credential: goal.Credential,
		Context: referencemonitor.CallContext{
			// Taint da AUTORIZAÇÃO da call (ADR-005/AOS-069, ADR-034): o rótulo do CONTEXTO
			// que o modelo viu no turno que a pediu — cunhado pelo runtime a partir do tail
			// ([ContextAuthority]), nunca lido da [ToolInvocation], que é saída do modelo. O
			// [referencemonitor.TaintGate] impõe: uma autorização untrusted não pode originar
			// uma capability privilegiada.
			Taint: authority.String(),
			// A reversibilidade DECLARADA pelo registry. Sem isto o classificador recebe vazio,
			// trata a acção como irreversível, e toda a tool call sai `danger` — o que colapsa
			// a taxonomia de autonomia L0–L5 em dois estados.
			Reversibility: inv.Reversibility,
		},
		Input: inv.Input,
		// LISTA-BRANCA DO RUN (AOS-413) — entregue ao RM, que a impõe (AOS-485). O slice vai
		// TAL COMO ESTÁ no goal, sem cópia defensiva: `append([]string(nil), x...)` de uma
		// lista vazia devolve nil, e nil é «sem restrição» — a cópia abria todas as tools ao
		// nó do plano que não tem nenhuma.
		AllowedTools: goal.AllowedTools,
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
	// LISTA-BRANCA DO RUN (AOS-413) — quem NEGA é o Reference Monitor (AOS-485, emenda ao
	// ADR-027 §2.3): a call segue para a mediação e a recusa sai de lá com evento, selo e
	// contador, como todas as outras. O que fica AQUI é não construir como efeito uma tool
	// que vai ser negada: para ela a reescrita NÃO corre, e a call chega ao RM tal como o
	// modelo a pediu. Sem esta condição, args malformados numa tool proibida saíam
	// `E_EFFECT_REWRITE` — a razão errada, e sem pegada nenhuma.
	//
	// Fail-closed: uma reescrita que falha (args malformados) NÃO despacha nada e
	// materializa-se como Deny no tail — não é fatal para o loop.
	if rt.callRewriter != nil && referencemonitor.RunAllowsTool(goal.AllowedTools, inv.ToolID) {
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
		// A reescrita define o efeito, não a restrição: a lista é a do goal, qualquer que
		// seja a Call que o rewriter devolveu.
		call.AllowedTools = goal.AllowedTools
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
	if res.CustoNaoDerivado {
		// AOS-406: um total com turnos sem fonte de preço não é o custo do run — o agregado sai
		// marcado e sem número, pela mesma razão do span `chat`.
		span.SetAttribute(AttrCostUndefined, true)
		return
	}
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
	confirmed := ToolStepID(stepID, idx)
	var pending []string
	for k := idx + 1; k < total; k++ {
		pending = append(pending, ToolStepID(stepID, k))
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
