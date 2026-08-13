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
	tree  *budget.Budget
	check *budget.BudgetCheck
	limit budget.Amount
	// costCapped distingue um tecto em $ REALMENTE configurado do ilimitado por omissão
	// ([UnlimitedCostMicroUSD]) — o banner e o burn-down têm de dizer qual dos dois é, e
	// `limit.CostMicroUSD` sozinho não os distingue de forma legível.
	costCapped bool

	mu   sync.Mutex
	live map[string]int       // runID → nº de aquisições vivas (a retoma reentra no mesmo run)
	onGo []func(runID string) // AOS-260: notificados quando a última hospedagem do run larga o nó
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
func (rb *RunBudget) acquire(runID string) (func(), error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.live[runID] == 0 {
		if err := rb.tree.AddNode(runID, BudgetTreeID, rb.limit); err != nil {
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
