package integration

// AOS-256 + AOS-257 — O ORÇAMENTO POR-RUN LIGADO À CADEIA REAL.
//
// Este ficheiro é as DUAS metades de um único mecanismo, e nenhuma funciona sem a outra:
//
//   - AOS-256, CICLO DE VIDA (nó por-run): o hook de orçamento debita um NÓ da árvore
//     escolhido por [budget.NodeFunc], que por omissão é o RunID. Compor o hook sem
//     REGISTAR esse nó faz [budget.Budget.Reserve] devolver [budget.ErrUnknownNode], que o
//     adaptador converte em HookDeny — ou seja, 100% das tool calls negadas, em todos os
//     runs, com uma razão que parece um problema de orçamento e é um problema de wiring. O
//     registo vive no seam por-run que já existia ([SecuredRuntime.Run], onde o tool set
//     congelado já era `Freeze`/`defer Release`), e a libertação é `defer` — cobre retorno
//     normal, erro E panic;
//   - AOS-257, SALDO (settle): a reserva feita no `Evaluate` fica PENDENTE e só se resolve
//     quando alguém a confirma ou liberta. O saldo vive num DECORATOR do
//     [agentruntime.ActivityDispatcher] e não no hook, porque o hook não vê o desfecho: as
//     fugas reais são as NEGAÇÕES A JUSANTE (o egress é o único hook depois do orçamento na
//     ordem canónica de AOS-154) e os ERROS FATAIS do despacho — não «permit sem Commit».
//
// ALCANCE — a declaração de AOS-255 dizia TOOL-ONLY/TOKEN-ONLY, e AOS-260 alargou-a nas duas
// dimensões (decisão do dono D1 = opção B). O que este ficheiro compõe continua a ser SÓ a
// metade das tool calls; a outra metade — a admissão do TURNO DE MODELO — vive em
// `model_admission.go`, sobre esta MESMA árvore e este MESMO nó por-run, e entra no runtime
// pela porta do loop. Um só tecto, dois pontos de admissão. A dimensão $ passou a ser
// debitada com o custo MEDIDO de AOS-259, e nega quando [WithMaxCostMicroUSDPerRun] o fixa.
//
// GRANULARIDADE (D-A1.3): POR-RUN, nunca por-mandato/delegação. Dois runs concorrentes têm
// nós irmãos e tectos INDEPENDENTES — esgotar um não nega o outro. E POR-INCARNAÇÃO: cada
// re-hospedagem do mesmo RunID (retoma, restart) recebe o tecto inteiro — ver [RunBudget.acquire].

import (
	"context"
	"errors"
	"math"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Erros de construção/ciclo de vida do orçamento por-run.
var (
	// ErrBudgetLimitInvalid — tecto por-run <= 0. Fail-closed na CONSTRUÇÃO e não no
	// comportamento: um tecto de 0 tokens não é «orçamento desligado», é «tudo negado»
	// (nenhuma estimativa cabe em zero) — exactamente o modo de falha que AOS-256 nomeia.
	// Quem quer o nó SEM orçamento não fornece [SecuredConfig.Budget].
	ErrBudgetLimitInvalid = errors.New("integration: tecto de orcamento por-run invalido (tokens tem de ser > 0; para correr SEM orcamento nao forneca SecuredConfig.Budget)")

	// ErrBudgetCostLimitInvalid — tecto por-run em micro-USD <= 0 ([WithMaxCostMicroUSDPerRun]).
	// Pela MESMA razão de [ErrBudgetLimitInvalid], e agora que a dimensão $ é debitada de
	// facto (AOS-260): um tecto de 0 micro-USD não é «sem tecto em dólares», é «todo o turno
	// de modelo com custo negado». Quem não quer tecto em $ omite a opção — o default é
	// [UnlimitedCostMicroUSD] (mede, não decide).
	ErrBudgetCostLimitInvalid = errors.New("integration: tecto de orcamento por-run em micro-USD invalido (tem de ser > 0; omita a opcao para medir sem tecto em dolares)")
)

// BudgetTreeID é a RAIZ da árvore de orçamento do nó. Os nós por-run pendem todos dela.
//
// A raiz é DELIBERADAMENTE ILIMITADA em tokens: o tecto da v1 é por-run, e um tecto de
// árvore seria um tecto GLOBAL do nó — um conceito diferente (por-mandato/por-tenant), com
// outra unidade de tempo (janela) e outro dono. Uma raiz finita faria o run B ser negado
// porque o run A gastou, que é precisamente a partilha de tecto que D-A1.3 proíbe.
const BudgetTreeID = "aos-node"

// RunBudget é o orçamento por-run do nó: a árvore, o hook do Reference Monitor e o tecto
// que cada run recebe. Construir com [NewRunBudget]; entregar em [SecuredConfig.Budget].
//
// Seguro para concorrência (a árvore e o adaptador já o são; o mapa de nós vivos aqui é
// protegido por mu).
type RunBudget struct {
	// pico e o maior consumo por-run visto por este processo; picoVisto distingue «nenhum run
	// fechou» de «consumo zero». Ver [RunBudget.PicoDeConsumo].
	pico      int64
	picoVisto bool
	tree      *budget.Budget
	check     *budget.BudgetCheck
	limit     budget.Amount
	// costCapped distingue um tecto em $ REALMENTE configurado do ilimitado por omissão
	// ([UnlimitedCostMicroUSD]) — o banner e o burn-down têm de dizer qual dos dois é, e
	// `limit.CostMicroUSD` sozinho não os distingue de forma legível.
	costCapped bool

	mu   sync.Mutex
	live map[string]int       // runID → nº de aquisições vivas (a retoma reentra no mesmo run)
	onGo []func(runID string) // AOS-260: notificados quando a última hospedagem do run larga o nó
	// consumo/logf: ver [WithConsumoDuravel] — o tecto por-run deixa de recomecar a cada hospedagem.
	consumo ConsumoDuravel
	logf    func(string, ...any)
}

// UnlimitedCostMicroUSD é a dimensão $ SEM tecto — o default de [NewRunBudget] desde
// AOS-260. Não é «desligada»: com o canal de custo ligado ponta a ponta (AOS-259) o gasto em
// micro-USD é REALMENTE debitado na árvore e legível por run; o que este valor diz é que
// nenhuma reserva será NEGADA pela dimensão $, só pela de tokens. É a diferença entre medir
// e decidir, e é ela que o banner declara.
//
// PORQUE NÃO ZERO (o valor de antes de AOS-260): com um tecto de 0 micro-USD qualquer
// reserva com custo > 0 não caberia e o nó negaria TODOS os turnos de modelo assim que o
// custo entrasse no caminho — a dimensão irmã a matar o run por não ter sido configurada.
const UnlimitedCostMicroUSD = math.MaxInt64

// NewRunBudget constrói o orçamento por-run com um tecto de maxTokens POR RUN e, desde
// AOS-260, um tecto OPCIONAL em micro-USD ([WithMaxCostMicroUSDPerRun]).
//
// O estimador das TOOL CALLS é [TokenOnlyEstimator] (AOS-258, em `budget_estimator.go`) — o
// estimador REAL sobre a Call MATERIALIZADA, e não o [budget.DefaultEstimator] placeholder,
// que deixou de ser usado em produção. A dimensão micro-USD dessa estimativa continua a ZERO:
// o custo de uma tool call não é conhecido antes do efeito (o canal medido de AOS-259 é o do
// MODELO). Quem passou a debitar dólares é a admissão do TURNO DE MODELO
// ([NewModelTurnAdmission], AOS-260), que os salda com o número MEDIDO.
func NewRunBudget(maxTokens int64, opts ...RunBudgetOption) (*RunBudget, error) {
	if maxTokens <= 0 {
		return nil, ErrBudgetLimitInvalid
	}
	rb := &RunBudget{
		limit: budget.Amount{Tokens: maxTokens, CostMicroUSD: UnlimitedCostMicroUSD},
		live:  make(map[string]int),
		// No-op por omissao: sem logger injectado, a degradacao fica muda — e e por isso que
		// [WithBudgetLogger] existe e o no o liga.
		logf: func(string, ...any) {},
	}
	for _, o := range opts {
		if o != nil {
			o(rb)
		}
	}
	if rb.limit.CostMicroUSD <= 0 {
		return nil, ErrBudgetCostLimitInvalid
	}
	// Raiz ilimitada nas DUAS dimensões (ver [BudgetTreeID]): o tecto da v1 é por-run, e uma
	// raiz finita faria o run B ser negado porque o run A gastou. A dimensão $ da raiz era 0
	// até AOS-260 — correcto enquanto NADA reservava custo, e uma bomba a partir do momento
	// em que a admissão do turno de modelo passou a fazê-lo (a raiz negaria toda a árvore).
	tree, err := budget.New(BudgetTreeID, budget.Amount{Tokens: math.MaxInt64, CostMicroUSD: math.MaxInt64})
	if err != nil {
		return nil, err
	}
	rb.tree = tree
	rb.check = budget.NewBudgetCheck(tree, budget.WithEstimator(TokenOnlyEstimator))
	return rb, nil
}

// RunBudgetOption configura o orçamento por-run na construção.
type RunBudgetOption func(*RunBudget)

// WithMaxCostMicroUSDPerRun fixa o tecto POR RUN na dimensão $ (micro-USD INTEIRO). Sem ela o
// tecto em $ é [UnlimitedCostMicroUSD]: o custo é debitado e legível, mas nunca nega.
//
// Com ela, a dimensão $ DECIDE — e decide a par da de tokens, porque uma reserva só cabe se
// couber em AMBAS ([budget.Amount] documenta-o): o run é negado pela primeira que esgotar.
func WithMaxCostMicroUSDPerRun(microUSD int64) RunBudgetOption {
	return func(rb *RunBudget) {
		rb.limit.CostMicroUSD = microUSD
		rb.costCapped = true
	}
}

// MaxTokensPerRun devolve o tecto por-run configurado (para o banner/observabilidade).
func (rb *RunBudget) MaxTokensPerRun() int64 { return rb.limit.Tokens }

// MaxCostMicroUSDPerRun devolve o tecto por-run em micro-USD e se ele foi REALMENTE
// configurado. `false` ⇒ a dimensão $ é medida mas não decide ([UnlimitedCostMicroUSD]) — e é
// essa distinção, e não o número, que o banner e o burn-down têm de declarar.
func (rb *RunBudget) MaxCostMicroUSDPerRun() (int64, bool) {
	if !rb.costCapped {
		return 0, false
	}
	return rb.limit.CostMicroUSD, true
}

// onRunRelease regista um observador do FIM da hospedagem de um run: chamado quando a última
// aquisição viva de `runID` larga o nó de orçamento (o mesmo instante em que o nó é removido
// da árvore).
//
// Existe para que o estado POR-RUN dos colaboradores do orçamento — hoje a dedup por
// `run_id:step_id` da admissão do turno de modelo (AOS-260) — seja podado no MESMO seam que
// já governa o ciclo de vida do nó, em vez de depender de alguém se lembrar de chamar um
// `Forget` algures no composition root. Um mapa por-run sem poda é uma fuga num processo de
// vida longa, e a poda esquecida é o modo de falha mais provável.
//
// Os observadores são chamados FORA do lock de [RunBudget] (nenhuma ordem de locks nova).
func (rb *RunBudget) onRunRelease(f func(runID string)) {
	if f == nil {
		return
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.onGo = append(rb.onGo, f)
}

// acquire regista o nó de orçamento do run e devolve a libertação.
//
// A libertação é idempotente e SEGURA em `defer` (retorno normal, erro e panic) — é a
// garantia que AOS-256 exige. A REENTRÂNCIA é contada: a retoma de um run durável reutiliza
// o MESMO RunID, e um segundo `acquire` sobre um nó já vivo não pode falhar nem apagar o nó
// enquanto o primeiro ainda corre.
//
// # O TECTO É POR-INCARNAÇÃO, e isso está declarado e não corrigido
//
// A contagem de reentrância só cobre hospedagens SOBREPOSTAS. Quando a última sai, o nó é
// REMOVIDO e com ele o `reserved`/`committed` acumulado — a árvore é em memória e não há
// estado de orçamento durável. Uma RE-hospedagem posterior do mesmo RunID (o fluxo normal de
// AOS-021: escalate → aprovação humana → `POST /runs/{id}/resume` → `Submit` → `hostRun` →
// [SecuredRuntime.Run] → aqui; ou um restart do processo) regista um nó NOVO com o tecto
// INTEIRO. Um run em ciclo de escalada/retoma pode consumir N × tecto em tool calls.
//
// A assimetria com o AVISO é o que obriga a declará-lo em vez de o deixar implícito: o
// burn-down de AOS-261 lê o LEDGER DURÁVEL chaveado por `run_id` e é CUMULATIVO entre
// incarnações por desenho explícito. O aviso vê o total; o enforcement recomeça. Está no
// banner do nó e em `deploy/node/README.md`.
//
// PORQUE NÃO SE CORRIGE AQUI: reter o nó para além da hospedagem trocaria uma fuga por uma
// fuga pior — nós vivos para sempre num processo de vida longa, e ainda assim zerados no
// primeiro restart, porque nada disto é durável. O que fecha o eixo é estado de orçamento
// DURÁVEL por run (mesma família de AOS-259), não um remendo em memória que mudaria a
// garantia de libertação que AOS-256 exige sem tornar o tecto verdadeiramente por-run.
func (rb *RunBudget) acquire(ctx context.Context, runID string) (func(), error) {
	// PRIMEIRA passagem: só para saber se ESTA hospedagem vai criar o nó.
	//
	// Sem esta verificação, [limiteParaIncarnacao] — que faz I/O ao ledger — corria em TODAS as
	// aquisições, incluindo as reentrantes onde o valor é deitado fora. O desperdício era o menor
	// dos problemas: com o ledger ilegível, escrevia o aviso de degradação («arranca com o tecto
	// INTEIRO») sobre uma decisão que NÃO foi tomada, porque o nó já existia e o limite nem chegou
	// a ser usado. Uma linha de log a descrever um efeito que não aconteceu.
	rb.mu.Lock()
	primeira := rb.live[runID] == 0
	rb.mu.Unlock()

	// A leitura fica FORA do lock: é I/O e não pode segurar o mutex que todas as hospedagens
	// partilham.
	limite := rb.limit
	if primeira {
		limite = rb.limiteParaIncarnacao(ctx, runID)
	}

	rb.mu.Lock()
	defer rb.mu.Unlock()

	// RE-VERIFICAÇÃO sob o lock: entre as duas passagens, outra hospedagem do MESMO run pode ter
	// criado o nó. Nesse caso não se recria — e o limite que se leu descarta-se, que é o preço
	// (raro) de não fazer I/O sob o mutex.
	if rb.live[runID] == 0 {
		if err := rb.tree.AddNode(runID, BudgetTreeID, limite); err != nil {
			return nil, err
		}
	}
	rb.live[runID]++

	var once sync.Once
	return func() {
		once.Do(func() {
			rb.mu.Lock()
			rb.live[runID]--
			if rb.live[runID] > 0 {
				rb.mu.Unlock()
				return
			}
			// CONSUMO FECHADO. Este é o único instante em que o consumo desta incarnação está
			// completo: ler DEPOIS do RemoveNode devolveria «nó inexistente». Ver
			// [RunBudget.PicoDeConsumo] para o porquê de isto existir.
			if st, err := rb.tree.Available(runID); err == nil {
				rb.registarConsumo(limite.Tokens - st.Tokens)
			}
			delete(rb.live, runID)
			// RemoveNode é idempotente; um erro aqui só pode ser [budget.ErrRootRemoval],
			// impossível com um runID (a raiz é [BudgetTreeID]).
			_ = rb.tree.RemoveNode(runID)
			// Cópia sob o lock, invocação FORA dele (ver [RunBudget.onRunRelease]): os
			// observadores têm locks próprios e chamá-los aqui dentro criaria uma ordem de
			// locks nova só para podar mapas.
			hooks := make([]func(string), len(rb.onGo))
			copy(hooks, rb.onGo)
			rb.mu.Unlock()
			for _, h := range hooks {
				h(runID)
			}
		})
	}, nil
}

// AvailableTokens devolve o headroom corrente do nó de um run (tokens). Devolve 0 e false
// se o run não tiver nó vivo. Existe para asserção/observabilidade — nunca decide nada.
func (rb *RunBudget) AvailableTokens(runID string) (int64, bool) {
	amt, err := rb.tree.Available(runID)
	if err != nil {
		return 0, false
	}
	return amt.Tokens, true
}

// AvailableCostMicroUSD devolve o headroom corrente do nó de um run na dimensão $ (micro-USD).
// Devolve 0 e false se o run não tiver nó vivo — o remanescente desconhecido NÃO é zero.
//
// É a irmã de [RunBudget.AvailableTokens] e existe pela razão exacta que o achado do «tecto em
// dólares invisível» nomeia: quando [WithMaxCostMicroUSDPerRun] está em vigor, a dimensão que
// DECIDE pode ser esta, e a superfície de decisão do operador (burn-down, pergunta humana
// durável) tinha de conseguir reportar a grandeza que bloqueou o run em vez de números de tokens
// que a contradizem. Como [AvailableTokens], é LEITURA — nunca decide nada.
//
// Sem tecto em $ configurado o valor é o remanescente de [UnlimitedCostMicroUSD], que não é uma
// leitura útil: quem chama tem de cruzar com [RunBudget.MaxCostMicroUSDPerRun] antes de o
// apresentar.
func (rb *RunBudget) AvailableCostMicroUSD(runID string) (int64, bool) {
	amt, err := rb.tree.Available(runID)
	if err != nil {
		return 0, false
	}
	return amt.CostMicroUSD, true
}

// ---------------------------------------------------------------------------
// AOS-257 — o SALDO (Settle) como decorator do ActivityDispatcher
// ---------------------------------------------------------------------------

// budgetSettlingDispatcher decora o [agentruntime.ActivityDispatcher] para SALDAR a reserva
// que o hook de orçamento fez dentro de `Mediate`.
//
// PORQUE AQUI E NÃO NO HOOK: o hook decide ANTES do efeito e nunca vê o desfecho. Quem vê o
// desfecho é o dispatcher — e vê os TRÊS que interessam:
//
//   - permit sem erro de tool ⇒ Commit (débito final);
//   - deny/escalate ⇒ Release. Inclui a negação A JUSANTE do orçamento: na ordem canónica
//     de AOS-154 o egress corre DEPOIS do budget, pelo que uma reserva concedida pode ser
//     seguida de uma negação — sem este release, cada tool call bloqueada pelo egress
//     roubava headroom ao run até o esgotar (o run acabaria negado por «falta de
//     orçamento» sem ter gasto nada);
//   - erro fatal do despacho (`runtime_ports.go`, cancelamento/erro que sobe ao loop) e
//     PANIC ⇒ Release, pelo `defer` com retornos nomeados.
//
// A ordem é OUTERMOST em relação ao dispatcher durável: num dedup/replay do step ledger não
// há mediação nova, logo não há reserva pendente, e o saldo é um no-op honesto.
type budgetSettlingDispatcher struct {
	inner agentruntime.ActivityDispatcher
	check *budget.BudgetCheck
}

// Dispatch implementa [agentruntime.ActivityDispatcher].
func (d budgetSettlingDispatcher) Dispatch(ctx context.Context, call referencemonitor.Call) (dec referencemonitor.Decision, err error) {
	// O saldo corre SEM o cancelamento do ctx do run: o caminho que MAIS precisa de
	// libertar headroom é justamente o do contexto cancelado, e um Release que falhasse
	// por `context.Canceled` deixaria a reserva pendente para sempre. Os valores do ctx
	// (correlação/tracing) preservam-se.
	settleCtx := context.WithoutCancel(ctx)
	defer func() {
		if err != nil {
			_ = d.check.Release(settleCtx, &call)
			return
		}
		// Em panic, os retornos nomeados ficam no zero-value: dec.Effect != permit ⇒
		// Settle liberta. É o comportamento correcto (nenhum efeito foi confirmado).
		_ = d.check.Settle(settleCtx, &call, dec)
	}()
	return d.inner.Dispatch(ctx, call)
}

var _ agentruntime.ActivityDispatcher = budgetSettlingDispatcher{}

// mediateDispatcher é o despacho DIRECTO sobre o Reference Monitor — o mesmo caminho do
// default do kernel (`agentruntime.directDispatcher`), que é INEXPORTADO e por isso não se
// consegue reutilizar. Só existe porque o decorator do orçamento tem de ter algo por baixo:
// entregar um [agentruntime.WithActivityDispatcher] SUBSTITUI o default do runtime, pelo
// que sem esta peça um nó com orçamento e sem execução durável ficaria sem despacho.
type mediateDispatcher struct{ rm *referencemonitor.Monitor }

// Dispatch implementa [agentruntime.ActivityDispatcher].
func (d mediateDispatcher) Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error) {
	return d.rm.Mediate(ctx, call)
}

var _ agentruntime.ActivityDispatcher = mediateDispatcher{}

// ------------------------------------------------------------------------------------------
// O TECTO POR-RUN DEIXA DE RECOMEÇAR A CADA HOSPEDAGEM.
//
// O DEFEITO, declarado no banner do nó e neste ficheiro desde AOS-256: a árvore de orçamento vive
// EM MEMÓRIA e o nó do run nasce a cada hospedagem com o tecto INTEIRO. Um run em ciclo de
// escalada/retoma podia gastar até N × tecto — e escalar/retomar é o fluxo NORMAL de tudo o que
// precisa de aprovação humana, não um caso exótico. Um run que exerci hoje passou por três
// incarnações.
//
// PORQUE SE PODE CORRIGIR AQUI AGORA. O comentário de [RunBudget.acquire] rejeitava corrigi-lo
// RETENDO o nó — e com razão: nós vivos para sempre num processo de vida longa, e zerados na
// mesma ao primeiro restart. Mas nomeava o remédio: «o que fecha o eixo é estado de orçamento
// DURÁVEL por run». Esse estado EXISTE — é o ledger de turnos que o burn-down (AOS-261) já lê,
// chaveado por `run_id`, deduplicado por `run_id:step_id` e sobrevivente à retoma. O aviso via o
// total; o enforcement é que não o consultava.
//
// Não se retém nada: o nó continua a nascer e a morrer com a hospedagem. O que muda é o LIMITE com
// que nasce — `tecto − já consumido`.
//
// ALCANCE HONESTO, e é parcial: o ledger conta TURNOS DE MODELO e só eles. As tool calls reservam
// do mesmo nó mas não entram no ledger, pelo que a fuga não fecha — ENCOLHE, do tecto inteiro por
// incarnação para o consumo de tool calls por incarnação. Fechá-la de todo exige contabilizar
// também as tool calls de forma durável (mesma família, eixo por abrir).
// ------------------------------------------------------------------------------------------

// ConsumoDuravel devolve o que este run JÁ consumiu, de uma fonte que sobrevive à retoma.
//
// Um run sem turnos ainda registados NÃO é um erro: é o caso normal da primeira incarnação, e
// tem de devolver zero com erro nil. Só um ledger ILEGÍVEL é erro.
type ConsumoDuravel func(ctx context.Context, runID string) (budget.Amount, error)

// WithConsumoDuravel liga o tecto por-run ao consumo já registado, para que a retoma NÃO
// recomece o orçamento.
func WithConsumoDuravel(f ConsumoDuravel) RunBudgetOption {
	return func(rb *RunBudget) { rb.consumo = f }
}

// WithBudgetLogger injecta o logger da DEGRADAÇÃO declarada de [WithConsumoDuravel].
func WithBudgetLogger(logf func(string, ...any)) RunBudgetOption {
	return func(rb *RunBudget) {
		if logf != nil {
			rb.logf = logf
		}
	}
}

// limiteParaIncarnacao devolve o limite com que o nó deste run deve nascer.
//
// DEGRADAÇÃO DECLARADA, e é uma escolha: se o ledger não se consegue ler, nasce com o tecto
// INTEIRO e a linha sai no log. A alternativa — recusar hospedar — transformaria um soluço
// transitório do Event Store num run encravado, e o orçamento é controlo de CUSTO, não de
// segurança. O que não se faz é degradar em silêncio: era assim que a fuga vivia.
func (rb *RunBudget) limiteParaIncarnacao(ctx context.Context, runID string) budget.Amount {
	if rb.consumo == nil {
		return rb.limit
	}
	consumido, err := rb.consumo(ctx, runID)
	if err != nil {
		rb.logf("[aos] orcamento por-run (AOS-256): consumo duravel de %q ILEGIVEL (%v) — a "+
			"incarnacao arranca com o tecto INTEIRO e o run pode exceder o tecto por-run. "+
			"Degradacao declarada, nao silenciosa", runID, err)
		return rb.limit
	}
	restante := rb.limit.Sub(consumido)
	// Consumido ≥ tecto ⇒ nasce a ZERO. É o veredicto certo (o orçamento acabou) e não um erro:
	// quem trata disso é o prompt de exaustão (AOS-263), que suspende o run em vez de o matar.
	if restante.Tokens < 0 {
		restante.Tokens = 0
	}
	if restante.CostMicroUSD < 0 {
		restante.CostMicroUSD = 0
	}
	return restante
}

// LigarConsumoDuravel liga a fonte DEPOIS da construção, e é deliberado que exista.
//
// O tecto por-run nasce na fronteira de AMBIENTE (`AOS_BUDGET_MAX_TOKENS`), onde ainda não há
// Event Store; o ledger de turnos só existe depois de o substrato estar composto. É a MESMA
// fase-2 do provisionamento dos níveis de autonomia — ligar o sink ao store que ainda não existia
// quando a config foi lida.
//
// Sem esta chamada o comportamento é o de antes: cada hospedagem nasce com o tecto inteiro.
func (rb *RunBudget) LigarConsumoDuravel(f ConsumoDuravel, logf func(string, ...any)) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.consumo = f
	if logf != nil {
		rb.logf = logf
	}
}

// TemConsumoDuravel diz se o tecto por-run está ligado ao consumo já registado.
//
// Existe para o teste de CABLAGEM: os testes de comportamento provam o que a semeadura faz, e
// nenhum deles prova que o nó chega a LIGÁ-LA. Foi uma mutação — remover a chamada em
// `Bootstrap` — a mostrar que essa metade não tinha teste; é o mesmo padrão que apareceu três
// vezes neste dia, sempre a testar a unidade e não a ligação.
func (rb *RunBudget) TemConsumoDuravel() bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.consumo != nil
}

// ---------------------------------------------------------------------------------------------
// O TECTO QUE NUNCA MORDE TEM DE SER VISÍVEL.
//
// O README declara-o há semanas: `AOS_BUDGET_MAX_TOKENS=200000` contra um consumo MEDIDO de
// ~1 750 tokens por run — o tecto e o aviso aos 80% ficam ~114× acima do uso real. O mecanismo
// FUNCIONA; nesta configuração é protecção que não engata.
//
// O problema não é a configuração: é que **isso só está escrito num parágrafo**. O banner declara
// o tecto e o mecanismo em detalhe, e lê-se como protecção activa. Quem opera o nó não tem por
// onde ver a distância entre o tecto e o uso real — e uma protecção cuja folga ninguém mede é
// indistinguível de uma que morde.
//
// [RunBudget.PicoDeConsumo] é a outra metade do rácio. Guarda o MAIOR consumo por-run que este
// processo viu, medido no momento em que o nó do run é destruído (o único instante em que o
// consumo daquela incarnação está fechado). Cruzado com [RunBudget.MaxTokensPerRun] dá a FOLGA,
// e a folga é o que se alerta.
//
// LIMITE DECLARADO: é por PROCESSO, não durável. Um restart repõe-o a zero, e o valor volta a
// subir com o primeiro run. Persistir isto exigiria um sítio para o guardar e uma decisão sobre
// retenção — e o que a métrica precisa de responder («este tecto chega a apertar?») responde-se
// com dias de observação, não com histórico eterno.
// ---------------------------------------------------------------------------------------------

// PicoDeConsumo devolve o maior consumo por-run (tokens) observado por este processo, e se houve
// alguma observação. (0, false) ⇒ nenhum run fechou ainda — que NÃO é o mesmo que consumo zero, e
// por isso não se emite métrica nenhuma nesse caso.
func (rb *RunBudget) PicoDeConsumo() (int64, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.pico, rb.picoVisto
}

// registarConsumo actualiza o pico. Chamado com rb.mu JÁ SEGURO.
func (rb *RunBudget) registarConsumo(tokens int64) {
	if tokens < 0 {
		return
	}
	rb.picoVisto = true
	if tokens > rb.pico {
		rb.pico = tokens
	}
}
