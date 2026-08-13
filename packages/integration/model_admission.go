package integration

// AOS-260 — A ADMISSÃO DO TURNO DE MODELO LIGADA AO ORÇAMENTO POR-RUN.
//
// Este ficheiro é o adaptador entre a porta que o kernel abriu ([agentruntime.ModelAdmission])
// e o orçamento hierárquico que JÁ EXISTE ([budget.Budget], via [RunBudget]). Não há aqui um
// segundo orçamento, nem uma segunda contabilidade, nem um tecto novo: é o MESMO nó por-run
// que o hook de tool calls debita (AOS-256/AOS-257), agora também debitado pela inferência —
// que é a linha de custo DOMINANTE e era a única que ninguém admitia (desafio A1, risco 1;
// decisão do dono D1 = opção B, 2026-08-13).
//
// # O MOLDE É O HOOK DE TOOL CALLS, e as diferenças são as que a inferência obriga
//
// O [budget.BudgetCheck] estima, reserva no `Evaluate` e é saldado por um decorator do
// dispatcher (AOS-257). A forma é a mesma — reservar antes, saldar depois, indexado por
// `run_id:step_id` — e há três diferenças, todas forçadas pelo objecto:
//
//  1. O SALDO É PELO REAL, não pelo veredicto. Uma tool call salda-se com commit ou release
//     (o efeito ocorreu ou não). Um turno de modelo tem um consumo MEDIDO que a estimativa
//     nunca acerta, e o canal que o traz existe desde AOS-259 (`Usage` + `CostMicroUSD`).
//     Confirmar a estimativa em vez do real faria o tecto ser consumido por provisão
//     fantasma: com 1024 tokens de provisão de output e turnos de 200, um tecto de 100k
//     esgotava-se ~4× mais cedo do que o gasto real justifica — negar cedo é tão errado
//     como negar tarde;
//  2. HÁ UMA DIMENSÃO $ A SÉRIO. A estimativa da tool call zera micro-USD porque o custo de
//     um efeito não é conhecível antes dele. Aqui é: o turno anterior do MESMO run já disse
//     quanto custou por token, e é dessa medição — não de uma tarifa inventada — que sai a
//     projecção (ver [runMeter.projectCost]);
//  3. O REPLAY TEM DE SER RECONHECIDO. Uma tool call reproduzida bate no `already-applied`
//     do step-ledger e não volta a ser mediada, pelo que o hook nem chega a correr. O turno
//     de modelo reproduzido ATRAVESSA o loop na mesma (é o [agentruntime.ModelClient] que
//     devolve a captura em vez de falar com o provider) — logo a dedup tem de ser explícita,
//     e é, por `run_id:step_id`.
//
// # ARITMÉTICA
//
// Micro-USD e tokens são INTEIROS de ponta a ponta ([budget.Amount]); nenhuma linha deste
// ficheiro introduz vírgula flutuante no caminho do dinheiro.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// DefaultOutputProvisionTokens é a PROVISÃO DE OUTPUT por omissão: quantos tokens de resposta
// a admissão reserva por turno, já que o output é desconhecido antes da chamada.
//
// O número é um compromisso declarado, e as duas alternativas puras estão ambas erradas:
//
//   - provisão ZERO (reservar só o input) é FAIL-OPEN na dimensão que mais cresce: o último
//     turno de um run podia gerar uma resposta inteira depois de a admissão ter dito «cabe»,
//     e o tecto seria excedido pelo output de cada turno. O excedente é depois cobrado no
//     saldo, mas cobrado é diferente de ADMITIDO — admission control que só descobre a seguir
//     não é admission control;
//   - provisão IGUAL AO MÁXIMO do modelo (dezenas de milhar) negaria runs com headroom real
//     de sobra, porque a reserva é feita ao máximo e o gasto típico é uma fracção dele.
//
// 1024 é a ordem de grandeza de uma resposta de agente com tool calls (o caso normal deste
// loop) e — o que torna o número pouco crítico — a provisão vive apenas ENTRE a admissão e o
// saldo: assim que a resposta chega, o real substitui-a e o excedente volta ao headroom no
// mesmo turno. Sobre-provisionar custa admissão momentânea, nunca orçamento.
//
// O valor é um TECTO da provisão, não a provisão em vigor: ver [maxProvisionDivisor].
const DefaultOutputProvisionTokens = 1024

// maxProvisionDivisor limita a provisão a 1/N do TECTO POR-RUN, e existe para fechar um
// modo de falha que uma constante fixa cria sozinha: um tecto por-run MENOR do que a provisão
// tornaria o tecto INATINGÍVEL — o primeiro turno seria negado sem o run ter gasto nada, e o
// operador leria «orçamento esgotado» num run que nunca correu. Uma provisão é uma aposta
// sobre o tamanho da resposta; deixá-la ser a coisa que sozinha nega o run inverte o seu papel.
//
// A regra, dita de outra maneira: quem decide se um turno cabe tem de ser o PROMPT (que é uma
// verdade sobre o run) mais uma margem, nunca a margem sozinha. Com este divisor, um tecto
// comporta sempre pelo menos ~8 turnos de provisão, e o que pode não caber é o prompt — o que
// é uma negação honesta.
//
// Só morde em tectos pequenos: com um tecto realista (centenas de milhar de tokens) o mínimo é
// [DefaultOutputProvisionTokens] e este divisor nunca entra na conta.
const maxProvisionDivisor = 8

// ReplayDetector diz se o turno `(runID, stepID, turn)` vai ser REPRODUZIDO a partir da
// captura — isto é, se o [agentruntime.ModelClient] vai devolver a resposta registada sem
// falar com nenhum provider.
//
// É uma PORTA e não uma heurística porque a verdade vive fora deste pacote: quem monta o
// plano de replay é o nó (`cmd/aos/resume_model.go`, plano por-run no ctx da retoma). O
// contrato é o de uma pergunta pura e barata — corre no caminho quente, uma vez por turno.
//
// SIMETRIA que este detector garante, e que é a razão de ele existir: a reserva acompanha a
// CHAMADA REAL ao modelo. Se o provider não é chamado, não há consumo novo — e cobrar um
// turno reproduzido cobraria DUAS VEZES o mesmo gasto, uma na incarnação que o produziu e
// outra em cada retoma. O burn-down durável (AOS-261) não infla porque o Event Store
// deduplica `run_id:step_id`; o orçamento inflaria, e as duas leituras do mesmo run passariam
// a discordar.
type ReplayDetector func(ctx context.Context, runID, stepID string, turn int) bool

// ModelTurnAdmission é a [agentruntime.ModelAdmission] sobre o orçamento por-run.
// Construir com [NewModelTurnAdmission] e entregar ao runtime com
// [agentruntime.WithModelAdmission]. Seguro para uso concorrente.
type ModelTurnAdmission struct {
	rb        *RunBudget
	provision int64
	replay    ReplayDetector

	mu      sync.Mutex
	pending map[admissionKey]budget.Reservation // (run, step) → provisão viva, à espera de saldo
	runs    map[string]*runMeter                // runID → dedup + taxa observada + latch de excedente
}

// runMeter é o estado POR-RUN da admissão: o que já foi admitido (dedup), o que já foi
// COBRADO (a base da projecção de custo) e o latch de excedente.
//
// É podado no MESMO seam que remove o nó de orçamento do run ([RunBudget.onRunRelease]) —
// não há aqui um ciclo de vida novo a poder ser esquecido.
type runMeter struct {
	// admitted são os StepIDs já admitidos NESTA incarnação. É a dedup do critério de
	// aceitação (`run_id:step_id`) no seu caso local; o caso que importa de verdade — a
	// retoma noutro processo, onde este mapa nasce vazio — é o do [ReplayDetector].
	admitted map[string]struct{}
	// tokens/cost são os totais REAIS já saldados no run (nunca as estimativas). Servem a
	// projecção de custo do turno seguinte: a tarifa sai da MEDIÇÃO deste run, não de uma
	// tabela que o runtime não tem.
	tokens int64
	cost   int64
	// breach, quando não vazio, é a razão de o tecto ter sido ULTRAPASSADO pelo consumo real
	// de um turno já dado. A partir daí a admissão NEGA — ver [ModelTurnAdmission.SettleTurn].
	breach string
}

// NewModelTurnAdmission compõe a admissão do turno de modelo sobre um orçamento por-run.
//
// Um `rb` nil é ERRO e não um adaptador inerte: quem liga esta porta está a pedir admission
// control, e devolver um adaptador que admite tudo seria a capacidade-fantasma que o banner
// do nó existe para evitar.
func NewModelTurnAdmission(rb *RunBudget, opts ...ModelAdmissionOption) (*ModelTurnAdmission, error) {
	if rb == nil {
		return nil, ErrModelAdmissionNoBudget
	}
	a := &ModelTurnAdmission{
		rb:        rb,
		provision: DefaultOutputProvisionTokens,
		pending:   make(map[admissionKey]budget.Reservation),
		runs:      make(map[string]*runMeter),
	}
	for _, o := range opts {
		if o != nil {
			o(a)
		}
	}
	if a.provision < 0 {
		return nil, ErrModelAdmissionProvisionInvalid
	}
	// A poda do estado por-run pendura-se no ciclo de vida que JÁ existe (AOS-256).
	rb.onRunRelease(a.forgetRun)
	return a, nil
}

// ModelAdmissionOption configura a admissão do turno de modelo.
type ModelAdmissionOption func(*ModelTurnAdmission)

// WithOutputProvision fixa a provisão de output por turno, em tokens (default
// [DefaultOutputProvisionTokens]). Zero é legítimo e DECLARADAMENTE fail-open na dimensão do
// output — ver o comentário da constante.
func WithOutputProvision(tokens int64) ModelAdmissionOption {
	return func(a *ModelTurnAdmission) { a.provision = tokens }
}

// WithReplayDetector liga o reconhecedor de turnos reproduzidos. Sem ele a dedup fica apenas
// com o mapa em memória desta incarnação — suficiente para uma re-entrada no mesmo processo,
// INSUFICIENTE para a retoma, que é o caso que a decisão do dono nomeia.
func WithReplayDetector(d ReplayDetector) ModelAdmissionOption {
	return func(a *ModelTurnAdmission) { a.replay = d }
}

// Erros de composição/execução da admissão do turno de modelo.
var (
	// ErrModelAdmissionNoBudget — pediu-se a admissão sem orçamento por-run.
	ErrModelAdmissionNoBudget = errors.New("integration: admissao do turno de modelo (AOS-260) sem orcamento por-run — construa com NewRunBudget")
	// ErrModelAdmissionProvisionInvalid — provisão de output negativa.
	ErrModelAdmissionProvisionInvalid = errors.New("integration: provisao de output invalida (tokens >= 0)")
	// ErrModelAdmissionBudgetNode — a reserva do turno não encontrou o nó de orçamento do
	// run. É FATAL e não uma negação, por decisão explícita: nesta topologia o nó é
	// registado por [SecuredRuntime.Run] antes do primeiro turno, pelo que a sua ausência é
	// um DEFEITO DE COMPOSIÇÃO. Convertê-lo em deny devolveria «sem orçamento» a 100% dos
	// turnos e mandaria o operador procurar a causa no sítio errado — o modo de falha que
	// AOS-256 nomeia.
	ErrModelAdmissionBudgetNode = errors.New("integration: admissao do turno de modelo sem no de orcamento para o run (defeito de composicao, nao falta de orcamento)")
)

// AdmitTurn implementa [agentruntime.ModelAdmission]: reserva a provisão do turno ANTES da
// chamada ao modelo. Nunca entra em panic; a NEGAÇÃO viaja no veredicto e só a CEGUEIRA
// (defeito de composição) devolve erro.
func (a *ModelTurnAdmission) AdmitTurn(ctx context.Context, req agentruntime.TurnAdmissionRequest) (agentruntime.TurnAdmissionVerdict, error) {
	a.mu.Lock()
	m := a.meterLocked(req.RunID)
	if _, ja := m.admitted[req.StepID]; ja {
		a.mu.Unlock()
		// DEDUP por `run_id:step_id`: o mesmo passo não se reserva duas vezes. No molde do
		// `already-applied` do step-ledger — admitido, sem débito novo.
		return agentruntime.TurnAdmissionVerdict{Admitted: true, AlreadyAdmitted: true}, nil
	}
	breach, tokens, cost := m.breach, m.tokens, m.cost
	a.mu.Unlock()

	// EXCEDENTE JÁ DECLARADO: um turno anterior custou mais do que o tecto comportava e a
	// diferença não coube. O run não recebe mais um turno — e a razão diz exactamente isso,
	// em vez de deixar o operador a inferi-la de uma negação genérica.
	if breach != "" {
		return agentruntime.TurnAdmissionVerdict{Reason: breach}, nil
	}

	// TURNO REPRODUZIDO: o provider não vai ser chamado, logo não há consumo novo a admitir.
	// Marca-se como admitido (a dedup local passa a cobri-lo) e NÃO se reserva nada.
	if a.replay != nil && a.replay(ctx, req.RunID, req.StepID, req.Turn) {
		a.mu.Lock()
		a.meterLocked(req.RunID).admitted[req.StepID] = struct{}{}
		a.mu.Unlock()
		return agentruntime.TurnAdmissionVerdict{Admitted: true, AlreadyAdmitted: true}, nil
	}

	prompt := ModelPromptTokens(req.View)
	provisao := OutputProvisionFor(a.provision, a.rb.MaxTokensPerRun())
	est := budget.Amount{
		Tokens:       prompt + provisao,
		CostMicroUSD: projectCost(prompt+provisao, tokens, cost),
	}
	r, err := a.rb.tree.Reserve(ctx, req.RunID, est)
	if err != nil {
		if errors.Is(err, budget.ErrNoHeadroom) {
			return agentruntime.TurnAdmissionVerdict{Reason: fmt.Sprintf(
				"orcamento do run esgotado: o turno %d precisa de ~%d tokens (%d de prompt + %d de provisao de output) e ~%d micro-USD, e o tecto por-run nao os comporta (ja consumidos %d tokens / %d micro-USD REAIS neste run). Nenhuma chamada ao modelo foi feita",
				req.Turn, est.Tokens, prompt, provisao, est.CostMicroUSD, tokens, cost)}, nil
		}
		if errors.Is(err, budget.ErrUnknownNode) {
			return agentruntime.TurnAdmissionVerdict{}, fmt.Errorf("%w: run %q turno %d: %w", ErrModelAdmissionBudgetNode, req.RunID, req.Turn, err)
		}
		// QUANTIA INVÁLIDA ⇒ NEGAÇÃO ATRIBUÍVEL, não cegueira do tecto. Uma [budget.Amount] que
		// [budget.Budget.Reserve] recusa é um defeito ARITMÉTICO deste adaptador (uma projecção
		// que transbordou, uma estimativa degenerada) e não a perda de visibilidade sobre o
		// orçamento. Subi-la como erro da porta fazia o loop abortar o run como `failed` — e
		// `failed` aciona a saga de compensação de AOS-254 sobre efeitos LEGÍTIMOS, que é a
		// consequência errada para um número mal calculado. Negar pára o run pelo caminho
		// declarado (razão própria, sem chamada ao modelo) e nomeia a causa real.
		if errors.Is(err, budget.ErrInvalidAmount) {
			return agentruntime.TurnAdmissionVerdict{Reason: fmt.Sprintf(
				"admissao do turno %d NEGADA por quantia de reserva invalida (%d tokens / %d micro-USD): e um defeito de calculo da projeccao deste adaptador, NAO falta de orcamento nem cegueira do tecto. Nenhuma chamada ao modelo foi feita",
				req.Turn, est.Tokens, est.CostMicroUSD)}, nil
		}
		return agentruntime.TurnAdmissionVerdict{}, fmt.Errorf("integration: reserva do turno de modelo (AOS-260) do run %q turno %d: %w", req.RunID, req.Turn, err)
	}

	a.mu.Lock()
	a.meterLocked(req.RunID).admitted[req.StepID] = struct{}{}
	a.pending[admissionKey{runID: req.RunID, stepID: req.StepID}] = r
	a.mu.Unlock()
	return agentruntime.TurnAdmissionVerdict{Admitted: true}, nil
}

// SettleTurn implementa [agentruntime.ModelAdmission]: substitui a PROVISÃO pelo consumo
// REAL do turno.
//
// # A troca, e porque é feita com as primitivas que já existem
//
// O [budget.Budget] confirma uma reserva pelo montante RESERVADO — `Commit` não recebe
// quantia, por desenho (é o que torna reserva e débito indistinguíveis do lado do tecto). Para
// saldar pelo real sem inventar uma primitiva nova, a troca é: LIBERTAR a provisão e RESERVAR
// o real. A ordem é essa e não a inversa, e o argumento é topológico:
//
//   - o nó do run é debitado sequencialmente (o loop de um run é uma goroutine; as tool calls
//     do turno só são despachadas DEPOIS deste saldo) e a raiz da árvore é ILIMITADA
//     ([BudgetTreeID]), pelo que nenhum irmão disputa este headroom. Libertar E (a provisão) e
//     reservar R ≤ E não pode falhar por contenção;
//   - reservar primeiro exigiria headroom para E+R ao mesmo tempo, e um run perto do tecto
//     seria negado no seu PRÓPRIO saldo — depois de o dinheiro estar gasto.
//
// R > E (subestimámos: uma resposta muito maior do que a provisão) é o único caso em que a
// reserva do real pode não caber. Aí NÃO se ignora o excedente e não se estoura o tecto:
// cobra-se o headroom até ao topo — o run fica com 100% consumido, que é a verdade mais
// próxima que o tecto consegue representar — e ARMA-SE o latch de excedente, que faz a
// admissão seguinte negar com razão própria. O número exacto do gasto não se perde: vive no
// ledger durável de turnos (AOS-261), que é a fonte do burn-down.
//
// USAGE A ZERO ⇒ FICA A PROVISÃO COBRADA, MAS NÃO MEDIDA. Um provider que não ecoa usage
// devolveria consumo zero, e saldar por zero faria o tecto nunca descer — um orçamento composto
// que não desconta nada é pior do que nenhum. Cobra-se então a estimativa (o único número
// honesto disponível para o TECTO), e é aí que ela pára: a estimativa NÃO entra no medidor do
// run. Alimentá-lo com ela contradiria o contrato de [runMeter] («os totais REAIS já saldados,
// nunca as estimativas») e, pior, DILUIRIA a tarifa de [projectCost] — um turno fantasma de
// 1200 tokens e 0 micro-USD ao lado de um turno medido de 800 tokens/6400 micro-USD daria uma
// tarifa observada de 3,2 em vez de 8, e a admissão seguinte projectaria o custo 2,5× abaixo do
// real. O tecto em dólares seria atravessado por turnos que a admissão devia ter negado: a
// cegueira do provider degradaria FAIL-OPEN precisamente na dimensão que AOS-260 veio fechar. A
// cegueira em si é denunciada onde tem de ser, no burn-down (`ErrBurndownNoUsage`).
//
// # A PROVISÃO NUNCA FICA ÓRFÃ
//
// A entrada de `pending` é retirada à entrada (é o que torna um saldo repetido um no-op honesto
// e impede que se liberte a reserva de outro turno), mas é REPOSTA em cada caminho de erro. Sem
// isso, uma falha de `Release`/`Reserve`/`Commit` deixava a reserva debitada em toda a cadeia de
// ancestrais e fora do mapa — e [ModelTurnAdmission.forgetRun], que é a única rede de
// reclamação, já não a encontrava: o débito na RAIZ ficava para sempre. Hoje é inalcançável no
// nó ([NewRunBudget] compõe [budget.New] sem emitter, e estas primitivas só falham na emissão
// durável), mas fica armado para o orçamento DURÁVEL por run que a nota N-DEF-278 descreve como
// o passo seguinte: aí uma indisponibilidade transitória do substrato faria a raiz encolher
// monotonamente a cada turno afectado, e o nó acabaria a negar TODOS os runs com uma razão que
// parece falta de orçamento. Repor a entrada é barato e é o que mantém a promessa do comentário
// de `forgetRun`.
func (a *ModelTurnAdmission) SettleTurn(ctx context.Context, s agentruntime.TurnSettlement) error {
	key := admissionKey{runID: s.RunID, stepID: s.StepID}
	a.mu.Lock()
	r, ok := a.pending[key]
	delete(a.pending, key)
	a.mu.Unlock()
	if !ok {
		// Sem provisão viva: turno reproduzido, ou saldo repetido. No-op honesto — nunca se
		// liberta/confirma a reserva de outro turno.
		return nil
	}

	// A CHAMADA FALHOU: nenhum turno houve. A provisão volta ao headroom por inteiro — sem
	// isto, um provider intermitente esgotava o tecto de um run com consumo inexistente.
	if s.Failed {
		if err := a.rb.tree.Release(ctx, r); err != nil {
			a.repend(key, r)
			return fmt.Errorf("integration: libertar a provisao do turno %d do run %q apos falha do modelo (AOS-260): %w", s.Turn, s.RunID, err)
		}
		return nil
	}

	real := budget.Amount{
		Tokens:       s.Usage.InputTokens + s.Usage.OutputTokens,
		CostMicroUSD: s.CostMicroUSD,
	}
	// Sem dados de usage — ou com um consumo que já cabe exactamente na provisão — confirma-se
	// a reserva tal-qual: menos uma troca no caminho quente e nenhuma janela em que o tecto
	// esteja momentaneamente sem o débito deste turno. E NÃO se chama `record`: ver acima.
	if real.Tokens <= 0 && real.CostMicroUSD <= 0 {
		if err := a.rb.tree.Commit(ctx, r); err != nil {
			a.repend(key, r)
			return fmt.Errorf("integration: confirmar a provisao do turno %d do run %q sem usage medido (AOS-260): %w", s.Turn, s.RunID, err)
		}
		return nil
	}

	if err := a.rb.tree.Release(ctx, r); err != nil {
		a.repend(key, r)
		return fmt.Errorf("integration: libertar a provisao do turno %d do run %q (AOS-260): %w", s.Turn, s.RunID, err)
	}
	cobranca := real
	if avail, err := a.rb.tree.Available(s.RunID); err == nil {
		cobranca = minAmount(real, avail)
	}
	if cobranca.Tokens > 0 || cobranca.CostMicroUSD > 0 {
		r2, err := a.rb.tree.Reserve(ctx, s.RunID, cobranca)
		if err != nil {
			// `r` já foi libertada; repô-la faz `forgetRun` chamar um Release IDEMPOTENTE (por
			// reservation.ID, [budget.Budget.Release] devolve nil sobre uma já libertada) — o
			// custo de a repor é nulo e o benefício é a reclamação continuar a cobrir o caso em
			// que o Release, afinal, não tinha aplicado.
			a.repend(key, r)
			return fmt.Errorf("integration: cobrar o consumo real do turno %d do run %q (AOS-260, %d tokens / %d micro-USD): %w", s.Turn, s.RunID, cobranca.Tokens, cobranca.CostMicroUSD, err)
		}
		if err := a.rb.tree.Commit(ctx, r2); err != nil {
			// Aqui a reserva VIVA é a `r2` (a do consumo real), e é essa que a rede de
			// reclamação tem de conhecer — repor a `r` deixaria a `r2` debitada e invisível.
			a.repend(key, r2)
			return fmt.Errorf("integration: confirmar o consumo real do turno %d do run %q (AOS-260): %w", s.Turn, s.RunID, err)
		}
	}
	// A MEDIÇÃO que alimenta a projecção de custo é sempre a REAL, mesmo quando a cobrança foi
	// limitada pelo headroom: a tarifa do modelo não muda por o tecto ter acabado.
	a.record(s.RunID, real)

	if cobranca != real {
		a.arm(s.RunID, fmt.Sprintf(
			"orcamento do run ULTRAPASSADO no turno %d: o consumo REAL (%d tokens / %d micro-USD) excedeu o que restava do tecto por-run (%d tokens / %d micro-USD cobrados). O turno correu e esta no ledger duravel; o run NAO recebe mais turnos de modelo",
			s.Turn, real.Tokens, real.CostMicroUSD, cobranca.Tokens, cobranca.CostMicroUSD))
	}
	return nil
}

// meterLocked devolve (criando à primeira vez) o estado por-run. Chamar com a.mu detido.
func (a *ModelTurnAdmission) meterLocked(runID string) *runMeter {
	m, ok := a.runs[runID]
	if !ok {
		m = &runMeter{admitted: make(map[string]struct{})}
		a.runs[runID] = m
	}
	return m
}

// record soma o consumo REAL de um turno ao medidor do run.
func (a *ModelTurnAdmission) record(runID string, real budget.Amount) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.meterLocked(runID)
	m.tokens += real.Tokens
	m.cost += real.CostMicroUSD
}

// repend repõe uma reserva no mapa de pendentes depois de uma operação sobre a árvore ter
// FALHADO — a rede que impede a reserva órfã (ver o comentário de [ModelTurnAdmission.SettleTurn]).
//
// Não sobrescreve uma entrada que já lá esteja: se uma admissão concorrente do MESMO
// `(run, step)` conseguiu entretanto pôr uma provisão viva, é essa que o saldo seguinte tem de
// resolver — a reposta é a que falhou, e para ela basta a poda de fim de run.
func (a *ModelTurnAdmission) repend(key admissionKey, r budget.Reservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ja := a.pending[key]; !ja {
		a.pending[key] = r
	}
}

// arm arma o latch de excedente do run (a primeira razão fica — é a que explica o excedente).
func (a *ModelTurnAdmission) arm(runID, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.meterLocked(runID)
	if m.breach == "" {
		m.breach = reason
	}
}

// forgetRun poda o estado por-run. Chamado pelo seam de AOS-256 quando a última hospedagem
// larga o nó de orçamento do run — nunca por quem compõe, para não poder ser esquecido.
//
// As provisões pendentes desse run são libertadas: chegar aqui com uma pendente significa que
// o run morreu ENTRE a admissão e o saldo (panic, cancelamento duro), e deixá-la seria um leak
// no nó que está a ser removido de qualquer modo — mas cujos ancestrais (a raiz) continuam
// debitados.
func (a *ModelTurnAdmission) forgetRun(runID string) {
	a.mu.Lock()
	delete(a.runs, runID)
	var orfas []budget.Reservation
	for k, r := range a.pending {
		// IGUALDADE do runID, nunca prefixo da chave concatenada — ver [admissionKey].
		if k.runID == runID {
			orfas = append(orfas, r)
			delete(a.pending, k)
		}
	}
	a.mu.Unlock()
	for _, r := range orfas {
		_ = a.rb.tree.Release(context.Background(), r)
	}
}

// admissionKey é a chave de dedup/indexação de uma provisão viva: o par `(run_id, step_id)` — o
// MESMO par da idempotency_key do Event Store e do `callKey` do hook de tool calls.
//
// É um VALOR ESTRUTURADO e não a concatenação `run_id:step_id`, e a diferença não é de estilo. Com
// uma chave-string, a poda de fim de run ([ModelTurnAdmission.forgetRun]) só podia procurar por
// PREFIXO — e o plano de dados aceita o `run_id` que vem no corpo do pedido sem validar a forma
// (`api.go` só recusa o vazio). Dois runs concorrentes `t1` e `t1:job` (ou qualquer convenção com
// `:`, do género `tenant:run-123`) partilham o prefixo: o fim do primeiro libertava as provisões
// VIVAS do segundo e removia-as do mapa, e o saldo desses turnos passava a ser um no-op silencioso
// — o consumo REAL nunca era debitado e o tecto por-run do segundo deixava de descer. Repetindo o
// run curto em ciclo, os turnos de modelo do run longo ficavam de graça. Com o par estruturado a
// poda é por IGUALDADE do runID e o âmbito não escorrega.
type admissionKey struct{ runID, stepID string }

// ModelPromptTokens estima os tokens de INPUT de um turno a partir do prompt MATERIALIZADO —
// os mesmos bytes que vão para o provider e cujo hash o manifesto grava.
//
// Reutiliza [approxTokens], a contagem por átomos de AOS-258, com o mesmo piso de bytes: não
// se inventa aqui um segundo estimador, e a direcção do erro é a mesma (sobrestima texto
// acentuado, aproxima CJK, deixa de subestimar JSON/base64/URLs). Sobrestimar aperta o tecto —
// a direcção fail-closed de um controlo de admissão.
//
// Nunca devolve zero: um prompt vazio não existe (o prefixo tem sempre o system + tool set), e
// uma reserva de 0 seria inválida ([budget.Amount] recusa-a).
func ModelPromptTokens(view agentruntime.PromptView) int64 {
	atomos := approxTokens(view.Materialized)
	piso := int64(len(view.Materialized))/bytesPerTokenFloor + 1
	if piso > atomos {
		atomos = piso
	}
	if atomos < 1 {
		atomos = 1
	}
	return atomos
}

// OutputProvisionFor devolve a provisão de output EM VIGOR: a configurada, limitada a
// 1/[maxProvisionDivisor] do tecto por-run. É exportada porque quem calibra um tecto (o
// operador, e os testes de nó) precisa de saber qual é a provisão REAL desse tecto e não a
// nominal — uma calibração feita sobre a constante mentiria em tectos pequenos.
//
// Nunca devolve negativo; devolve 0 quando a provisão configurada é 0 (o modo declaradamente
// fail-open, ver [DefaultOutputProvisionTokens]).
func OutputProvisionFor(configurada, tectoPorRun int64) int64 {
	if configurada <= 0 {
		return 0
	}
	tecto := tectoPorRun / maxProvisionDivisor
	if tecto < configurada {
		configurada = tecto
	}
	if configurada < 0 {
		configurada = 0
	}
	return configurada
}

// projectCost projecta o custo (micro-USD INTEIRO) de um turno de `tokens` a partir da TARIFA
// OBSERVADA do próprio run: os totais REAIS já saldados.
//
// Porque a tarifa vem da medição e não de uma tabela: o runtime não tem tabela de preços — ela
// vive no Model Gateway (AOS-259) e depende do par (modelo, região). Importá-la para aqui
// duplicaria a fonte de verdade do preço e criaria a segunda contabilidade que AOS-259
// recusou. O que o run já pagou é, para o run seguinte turno, o melhor estimador disponível —
// e é um número MEDIDO, não inventado.
//
// PRIMEIRO TURNO DE CADA INCARNAÇÃO: sem medição ainda, a projecção é ZERO e a admissão nesse
// turno decide só pela dimensão de tokens. É a limitação declarada do eixo $: um tecto em
// dólares é atravessado, no máximo, por UM turno antes de passar a morder. Está no banner.
//
// Arredonda para CIMA (a direcção conservadora) e nunca transborda int64.
//
// O arredondamento é feito por QUOCIENTE + RESTO e não pela forma idiomática
// `(p + d - 1) / d`, e a razão é que essa forma transborda no bordo: com o produto
// `tokens*custoReal` perto de [math.MaxInt64] — que o guard abaixo admite — somar-lhe
// `tokensReais-1` dá um valor NEGATIVO. Um `CostMicroUSD` negativo faz
// [budget.Amount.validReserve] recusar e [budget.Budget.Reserve] devolver
// [budget.ErrInvalidAmount], que a admissão trata como negação atribuível (ver [AdmitTurn]) —
// mas o certo é não produzir o número inválido de todo. Quociente e resto nunca transbordam.
func projectCost(tokens, tokensReais, custoReal int64) int64 {
	if tokens <= 0 || tokensReais <= 0 || custoReal <= 0 {
		return 0
	}
	if custoReal <= math.MaxInt64/tokens {
		produto := tokens * custoReal
		q := produto / tokensReais
		if produto%tokensReais != 0 && q < math.MaxInt64 {
			q++
		}
		return q
	}
	// Produto inseguro: aproxima pela tarifa por token (já arredondada para cima). Também aqui
	// o arredondamento é por quociente + resto, e pela MESMA razão: `custoReal + tokensReais - 1`
	// transborda sempre que ambos são grandes, e uma tarifa negativa passava o teste de saturação
	// abaixo e devolvia um produto errado (com `custoReal = tokensReais = tokens = MaxInt64` a
	// forma idiomática devolvia ZERO — a projecção mais fail-open possível, precisamente no caso
	// mais caro).
	taxa := custoReal / tokensReais
	if custoReal%tokensReais != 0 {
		taxa++
	}
	if taxa > math.MaxInt64/tokens {
		return math.MaxInt64
	}
	return taxa * tokens
}

// minAmount devolve o mínimo componente-a-componente (o teto de cobrança: nunca mais do que o
// headroom, em nenhuma dimensão).
func minAmount(a, b budget.Amount) budget.Amount {
	out := a
	if b.Tokens < out.Tokens {
		out.Tokens = b.Tokens
	}
	if b.CostMicroUSD < out.CostMicroUSD {
		out.CostMicroUSD = b.CostMicroUSD
	}
	if out.Tokens < 0 {
		out.Tokens = 0
	}
	if out.CostMicroUSD < 0 {
		out.CostMicroUSD = 0
	}
	return out
}

// Assegura em compile-time que o adaptador satisfaz a porta do kernel.
var _ agentruntime.ModelAdmission = (*ModelTurnAdmission)(nil)
