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
// ALCANCE (a declaração fixada em AOS-255, que este wiring NÃO alarga): o tecto é
// TOOL-ONLY, porque a cadeia do Reference Monitor só é atravessada por tool call — o turno
// de modelo é invocado directamente pelo loop e nenhuma reserva o admite; e é TOKEN-ONLY,
// porque o canal de custo em micro-USD não está ligado ponta a ponta (eixo AOS-259) e um
// tecto em dólares seria contado a zero.
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

	mu   sync.Mutex
	live map[string]int // runID → nº de aquisições vivas (a retoma reentra no mesmo run)
}

// NewRunBudget constrói o orçamento por-run com um tecto de maxTokens POR RUN.
//
// O estimador é [TokenOnlyEstimator] (AOS-258, em `budget_estimator.go`) — o estimador REAL
// sobre a Call MATERIALIZADA, e não o [budget.DefaultEstimator] placeholder, que deixou de
// ser usado em produção. A dimensão micro-USD é ZERADA de propósito, não por esquecimento: o
// canal de custo não existe ponta a ponta (AOS-259) e uma estimativa em dólares seria
// comparada com um tecto de dólares que ninguém configurou, negando tudo ou nada consoante o
// arredondamento. Token-only é a única dimensão honesta hoje.
func NewRunBudget(maxTokens int64) (*RunBudget, error) {
	if maxTokens <= 0 {
		return nil, ErrBudgetLimitInvalid
	}
	// Raiz ilimitada em tokens (ver [BudgetTreeID]) e a ZERO em micro-USD — que é o valor
	// CORRECTO para a dimensão desligada: o estimador token-only reserva 0 nessa dimensão,
	// logo `0 <= 0` cabe sempre e a dimensão nunca decide nada.
	tree, err := budget.New(BudgetTreeID, budget.Amount{Tokens: math.MaxInt64})
	if err != nil {
		return nil, err
	}
	rb := &RunBudget{
		tree:  tree,
		limit: budget.Amount{Tokens: maxTokens},
		live:  make(map[string]int),
	}
	rb.check = budget.NewBudgetCheck(tree, budget.WithEstimator(TokenOnlyEstimator))
	return rb, nil
}

// MaxTokensPerRun devolve o tecto por-run configurado (para o banner/observabilidade).
func (rb *RunBudget) MaxTokensPerRun() int64 { return rb.limit.Tokens }

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
			defer rb.mu.Unlock()
			rb.live[runID]--
			if rb.live[runID] > 0 {
				return
			}
			delete(rb.live, runID)
			// RemoveNode é idempotente; um erro aqui só pode ser [budget.ErrRootRemoval],
			// impossível com um runID (a raiz é [BudgetTreeID]).
			_ = rb.tree.RemoveNode(runID)
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
