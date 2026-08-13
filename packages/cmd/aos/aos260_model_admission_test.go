package main

// AOS-260 (LADO DO NÓ) — a DEGRADAÇÃO DECLARADA e o detector de replay.
//
// O adaptador de `integration` já foi provado contra a árvore de orçamento real. O que se
// prova aqui é a metade node-local, e é toda ela sobre o que ACONTECE quando a admissão nega:
//
//  1. com o prompt de exaustão ARMADO, a negação SUSPENDE o run pela maquinaria HITL que já
//     existia — o mesmo `errExhaustionSuspended` que o nó absorve como suspensão;
//  2. sem ele, a negação passa como VEREDICTO e o run pára com razão própria, cujo selo
//     durável é `timed_out`/`budget_exhausted` — nunca `failed`;
//  3. o detector de replay lê o MESMO plano que faz o model client devolver a captura, que é
//     o que torna «sem chamada, sem cobrança» uma propriedade e não uma coincidência;
//  4. a env do tecto em dólares é fail-closed na configuração.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// pedidoDeAdmissao monta um pedido de admissão com um prompt realista.
func pedidoDeAdmissao(runID string, turn int) agentruntime.TurnAdmissionRequest {
	return agentruntime.TurnAdmissionRequest{
		RunID:   runID,
		StepID:  "step-1",
		Turn:    turn,
		ModelID: "claude-opus-4-8",
		View:    agentruntime.PromptView{Turn: turn, Materialized: []byte(strings.Repeat("le o documento ", 200))},
	}
}

// admissaoQueNega é a admissão INTERIOR já esgotada. O que estes testes exercem é o decorator
// do nó — a FORMA da degradação —, não a aritmética do tecto, que é selada contra a árvore
// real em `integration/aos260_model_admission_test.go`. Injectar a negação em vez de a
// provocar mantém cada teste sobre uma coisa só.
type admissaoQueNega struct{ razao string }

func (a admissaoQueNega) AdmitTurn(context.Context, agentruntime.TurnAdmissionRequest) (agentruntime.TurnAdmissionVerdict, error) {
	return agentruntime.TurnAdmissionVerdict{Reason: a.razao}, nil
}

func (admissaoQueNega) SettleTurn(context.Context, agentruntime.TurnSettlement) error { return nil }

// lerFonteDoNo lê um ficheiro do composition-root (para os gates de wiring).
func lerFonteDoNo(t *testing.T, nome string) string {
	t.Helper()
	b, err := os.ReadFile(nome)
	if err != nil {
		t.Fatalf("ler %q: %v", nome, err)
	}
	return string(b)
}

// TestAOS260_NegacaoComPromptArmadoSuspendeORun é o caminho principal da degradação declarada:
// a negação NÃO devolve um veredicto que o loop tenha de interpretar sozinho — levanta a MESMA
// pergunta humana durável de AOS-263 e devolve o sinal de suspensão, que o nó absorve.
func TestAOS260_NegacaoComPromptArmadoSuspendeORun(t *testing.T) {
	// Tecto de 10 tokens: nenhum prompt real cabe. O harness de AOS-263 dá-nos o prompt
	// ARMADO sobre a maquinaria HITL real (pendentes duráveis + gates + rota de decisão).
	h := novoAOS263Harness(t, 10, 0.80, 0)
	const run = "run-260-suspende"
	gate := h.abreEReclama(t, run)

	rb, err := integration.NewRunBudget(10)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	adm := &nodeModelAdmission{
		inner:  admissaoQueNega{razao: "orcamento do run esgotado: o turno 1 precisa de ~903 tokens e o tecto por-run nao os comporta. Nenhuma chamada ao modelo foi feita"},
		rb:     rb,
		prompt: h.prog.prompt,
		log:    h.prog.log,
	}

	v, err := adm.AdmitTurn(context.Background(), pedidoDeAdmissao(run, 1))
	if !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("com o prompt armado, a negacao tem de devolver o sinal de SUSPENSAO (que o no absorve como waiting_on_human, nao como falha); deu v=%+v err=%v", v, err)
	}
	if got := gate.m.Current(); got != state.WaitingOnHuman {
		t.Fatalf("o run tinha de suspender em waiting_on_human; esta em %q", got)
	}
	if n := len(h.exaustoesDe(t, run)); n != 1 {
		t.Fatalf("tinha de ficar selado 1 pendente de exaustao (a pergunta que o operador responde); ficaram %d", n)
	}
	// A LINHA DO LOG sai SEMPRE e ANTES da suspensão — o operador tem de saber que o run
	// parou por orçamento mesmo que a suspensão falhe a seguir.
	logs := h.logs.String()
	if !strings.Contains(logs, "ADMISSAO DO TURNO DE MODELO NEGADA") {
		t.Errorf("faltou a linha de negacao no log do no:\n%s", logs)
	}
	if !strings.Contains(logs, "O loop NAO retenta") {
		t.Errorf("a linha tem de dizer que NAO ha retentativa — e o que distingue esta paragem de um deny-loop:\n%s", logs)
	}
}

// TestAOS260_NegacaoSemPromptParaComRazaoPropria é o outro desfecho: sem maquinaria HITL a
// negação viaja como VEREDICTO (não como erro) e o loop pára — o que o kernel já sela em
// [TestAOS260_NegacaoParaORunUmaVezSemDenyLoop]. Aqui prova-se que o nó não inventa um erro
// onde não há decisão humana possível.
func TestAOS260_NegacaoSemPromptParaComRazaoPropria(t *testing.T) {
	rb, err := integration.NewRunBudget(10)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	const run = "run-260-sem-prompt"

	var linhas strings.Builder
	adm := &nodeModelAdmission{
		inner: admissaoQueNega{razao: "orcamento do run esgotado"},
		rb:    rb,
		log:   func(f string, a ...any) { linhas.WriteString(f) },
	}
	v, err := adm.AdmitTurn(context.Background(), pedidoDeAdmissao(run, 1))
	if err != nil {
		t.Fatalf("sem prompt armado a negacao NAO pode virar erro: %v", err)
	}
	if v.Admitted || v.Reason == "" {
		t.Fatalf("esperava uma negacao com razao atribuivel: %+v", v)
	}
	if !strings.Contains(linhas.String(), "ADMISSAO DO TURNO DE MODELO NEGADA") {
		t.Errorf("a linha do log sai nos DOIS desfechos: %q", linhas.String())
	}
}

// TestAOS260_SemOrcamentoNaoHaAdmissao: o nó não compõe uma admissão que admite tudo. `nil`
// desembrulhado em `port()` é o que impede um admissor fantasma de passar o teste `!= nil` do
// kernel e de ficar a negar (ou a admitir) sem tecto nenhum.
func TestAOS260_SemOrcamentoNaoHaAdmissao(t *testing.T) {
	t.Parallel()
	adm, err := newNodeModelAdmission(nil, nil, nil)
	if err != nil {
		t.Fatalf("sem orcamento a composicao e um no-op, nao um erro: %v", err)
	}
	if adm != nil {
		t.Fatal("sem orcamento nao pode haver admissao composta")
	}
	if adm.port() != nil {
		t.Fatal("port() de um adaptador nil tem de ser uma interface NIL (senao o kernel liga um admissor fantasma)")
	}
}

// TestAOS260_DetectorLeOMesmoPlanoDoModelClient é a prova da SIMETRIA que garante o critério
// 3 no nó: o detector e o [resumeAwareModelClient] consultam o MESMO plano, com a MESMA chave.
// Se divergissem, um turno servido pela captura seria cobrado — ou um turno real ficaria por
// admitir.
func TestAOS260_DetectorLeOMesmoPlanoDoModelClient(t *testing.T) {
	t.Parallel()
	plano := replayPlan{
		1: {Text: "turno 1 reproduzido", Usage: agentruntime.Usage{InputTokens: 5, OutputTokens: 5}},
		2: {Text: "turno 2 reproduzido"},
	}
	ctx := withReplayPlan(context.Background(), plano)
	cliente := newResumeAwareModelClient(agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		return agentruntime.ModelResponse{Text: "PROVIDER REAL"}, nil
	}))

	for turno := 1; turno <= 3; turno++ {
		resp, err := cliente.Call(ctx, agentruntime.PromptView{Turn: turno})
		if err != nil {
			t.Fatalf("Call(turno %d): %v", turno, err)
		}
		doProvider := resp.Text == "PROVIDER REAL"
		reproduzido := replayCoversTurn(ctx, "run", "step", turno)
		// A propriedade: o detector diz «reproduzido» EXACTAMENTE quando o cliente NÃO foi ao
		// provider. É o gémeo independente um do outro — nenhum deles pergunta ao outro.
		if reproduzido == doProvider {
			t.Errorf("turno %d: detector=%v mas o cliente foi ao provider=%v — sem esta simetria a cobranca deixa de acompanhar a chamada real", turno, reproduzido, doProvider)
		}
	}
	// Um run NORMAL (sem plano no ctx) nunca é reproduzido — senão a admissão estaria
	// desligada em todos os runs, que é o modo de falha simétrico e silencioso.
	if replayCoversTurn(context.Background(), "run", "step", 1) {
		t.Error("sem plano de replay no ctx nenhum turno pode ser dado como reproduzido")
	}
}

// TestAOS260_SeloTerminalDoEsgotamento: o desfecho durável de um run que parou por orçamento é
// `timed_out` com rótulo próprio — ao lado de max_turns e do wall-clock. `failed` seria a
// causa errada E accionaria a saga de compensação (AOS-254) sobre efeitos legítimos.
func TestAOS260_SeloTerminalDoEsgotamento(t *testing.T) {
	h := novoAOS263Harness(t, 1000, 0.80, 0)
	const run = "run-260-selo"
	gate := h.abreEReclama(t, run)

	res := agentruntime.Result{RunID: run, Turns: 4, BudgetExhausted: true, BudgetExhaustionReason: "orcamento do run esgotado"}
	selado, err := gate.sealTerminal(context.Background(), res, nil, false)
	if err != nil {
		t.Fatalf("sealTerminal: %v", err)
	}
	if selado != state.TimedOut {
		t.Fatalf("um tecto de gasto atingido sela timed_out (tecto defensivo), nunca %q — failed accionaria a saga de rollback sobre efeitos legitimos", selado)
	}
}

// TestAOS260_TectoEmDolaresEFailClosedNaConfiguracao: a env nova segue a disciplina das
// outras — ilegível/0/negativa aborta, e sem o tecto em tokens aborta também (não há
// orçamento onde pendurar o tecto em $, e escrevê-lo faria o operador ler protecção nenhuma).
func TestAOS260_TectoEmDolaresEFailClosedNaConfiguracao(t *testing.T) {
	casos := []struct {
		nome, tokens, custo string
		querErro            bool
		querCap             int64
	}{
		{nome: "so tokens", tokens: "50000", custo: "", querCap: 0},
		{nome: "tokens + custo", tokens: "50000", custo: "250000", querCap: 250_000},
		{nome: "custo sem tokens", tokens: "", custo: "250000", querErro: true},
		{nome: "custo zero", tokens: "50000", custo: "0", querErro: true},
		{nome: "custo negativo", tokens: "50000", custo: "-1", querErro: true},
		{nome: "custo ilegivel", tokens: "50000", custo: "1e6", querErro: true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Setenv("AOS_BUDGET_MAX_TOKENS", c.tokens)
			t.Setenv("AOS_BUDGET_MAX_COST_MICRO_USD", c.custo)
			rb, err := budgetFromEnv()
			if c.querErro {
				if err == nil {
					t.Fatalf("esperava recusa de arranque; rb=%v", rb)
				}
				if !errors.Is(err, ErrBadBudgetCost) {
					t.Errorf("a recusa tem de nomear a env do custo: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("budgetFromEnv: %v", err)
			}
			got, temTecto := rb.MaxCostMicroUSDPerRun()
			if c.querCap == 0 {
				if temTecto {
					t.Errorf("sem a env o tecto em $ NAO pode ser declarado como configurado (medir e diferente de decidir); got %d", got)
				}
				return
			}
			if !temTecto || got != c.querCap {
				t.Errorf("tecto em $ = (%d,%v), want (%d,true)", got, temTecto, c.querCap)
			}
		})
	}
}

// TestAOS260_NegacaoEmDolaresNomeiaADimensaoNaPerguntaDuravel fecha o achado do «tecto em
// dólares invisível»: a superfície de decisão do operador reportava TOKENS enquanto a razão da
// paragem eram DÓLARES. O pendente durável dizia «X de 1000000 tokens consumidos» com fracção
// 1.00 — auto-contraditório e sem uma única menção a dólares. Quem lesse isso responderia
// `continue` por ver headroom de sobra, o run era re-hospedado e imediatamente re-negado pelo
// MESMO tecto: um ciclo de decisões humanas sobre a grandeza errada.
//
// DIVISÃO DE PROVA (declarada, para não se ler mais do que aqui se prova): o CONTEÚDO da razão
// de um deny em $ com tokens de sobra é selado contra a árvore real em
// `integration.TestAOS260_TectoEmDolaresDecideComATarifaMedida`, que é onde o nó de orçamento
// por-run existe (o seam que o regista, `RunBudget.acquire`, é interno a `integration` — a única
// via de produção é [integration.SecuredRuntime.Run]). O que se prova AQUI é o que é node-local
// e era o que estava partido: os números do tecto em $ entram na avaliação a partir do orçamento
// REAL, e a razão da admissão chega VERBATIM ao registo durável em vez de morrer no log do
// processo.
func TestAOS260_NegacaoEmDolaresNomeiaADimensaoNaPerguntaDuravel(t *testing.T) {
	// Tecto FOLGADO em tokens (1M) e APERTADO em dólares: o que nega é a dimensão $.
	const (
		tectoTokens = 1_000_000
		tectoCusto  = 50_000
		run         = "run-260-dolares"
	)
	// A razão é a que o adaptador de `integration` produz para um deny em $ — mesma forma,
	// mesmos dois pares. Não é decoração: é o valor cuja PROPAGAÇÃO se está a provar.
	const razaoEmDolares = "orcamento do run esgotado: o turno 2 precisa de ~1027 tokens (3 de prompt + 1024 de provisao de output) e ~8216 micro-USD, e o tecto por-run nao os comporta (ja consumidos 5000 tokens / 40000 micro-USD REAIS neste run). Nenhuma chamada ao modelo foi feita"

	rb, err := integration.NewRunBudget(tectoTokens, integration.WithMaxCostMicroUSDPerRun(tectoCusto))
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	h := novoAOS263HarnessSobre(t, rb, 0.80, 0)
	gate := h.abreEReclama(t, run)

	adm := &nodeModelAdmission{
		inner:  admissaoQueNega{razao: razaoEmDolares},
		rb:     rb,
		prompt: h.prog.prompt,
		log:    h.prog.log,
	}
	if _, err := adm.AdmitTurn(context.Background(), pedidoDeAdmissao(run, 2)); !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("com o prompt armado, a negacao em $ tem de suspender o run: err=%v", err)
	}
	if got := gate.m.Current(); got != state.WaitingOnHuman {
		t.Fatalf("o run tinha de suspender em waiting_on_human; esta em %q", got)
	}

	pendentes := h.exaustoesDe(t, run)
	if len(pendentes) != 1 {
		t.Fatalf("esperava 1 pendente de exaustao; ficaram %d", len(pendentes))
	}
	p := pendentes[0]

	// (1) A PERGUNTA TRAZ A DIMENSÃO $, vinda do orçamento REAL — sem isto o operador não vê a
	// grandeza que decidiu. Sem nó vivo o remanescente é desconhecido e a leitura honesta é
	// «tecto consumido»; o que NÃO pode acontecer é o campo continuar a zero.
	if p.LimitCostMicroUSD != tectoCusto {
		t.Errorf("o pendente tem de reportar o TECTO em micro-USD (%d), got %d — a zero, a pergunta so sabe falar de tokens e contradiz a razao da paragem", tectoCusto, p.LimitCostMicroUSD)
	}
	if p.ConsumedCostMicroUSD <= 0 {
		t.Errorf("o pendente tem de reportar o CONSUMIDO em micro-USD; got %d", p.ConsumedCostMicroUSD)
	}
	// (2) A RAZÃO chega VERBATIM: nomeia os micro-USD e traz os dois pares. É o campo que
	// substitui o log do processo, que não é o canal que a decisão assinada lê.
	if p.Reason != razaoEmDolares {
		t.Errorf("a razao ATRIBUIVEL da admissao tem de chegar ao registo duravel tal-qual:\n got %q\nwant %q", p.Reason, razaoEmDolares)
	}
	// (3) E a superfície de leitura do operador (GET /runs/{id}) transporta-a: um registo
	// durável que o wire não expõe é o mesmo defeito com mais passos.
	fonteAPI := lerFonteDoNo(t, "api.go")
	for _, campo := range []string{"Reason:", "ConsumedCostMicroUSD:", "LimitCostMicroUSD:"} {
		if !strings.Contains(fonteAPI, campo) {
			t.Errorf("pendingExhaustionWire deixou de propagar %s — a pergunta volta a chegar ao operador sem a grandeza que a levantou", campo)
		}
	}
}

// TestAOS260_BurnDownVeODolarQuandoHaTectoEmDolares fecha a outra metade do mesmo achado: sem a
// dimensão $ no `Limit`, `consumedFraction` calculava a fracção SÓ sobre tokens e o aviso nunca
// disparava na dimensão que decide — o run era negado de repente, sem o pré-aviso que AOS-262
// existe para dar.
func TestAOS260_BurnDownVeODolarQuandoHaTectoEmDolares(t *testing.T) {
	t.Parallel()
	const (
		tectoTokens = 1_000_000
		tectoCusto  = 50_000
	)
	comTecto, err := integration.NewRunBudget(tectoTokens, integration.WithMaxCostMicroUSDPerRun(tectoCusto))
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	semTecto, err := integration.NewRunBudget(tectoTokens)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	ctx := context.Background()

	lim, err := runBudgetReader{rb: comTecto}.Limit(ctx, "qualquer")
	if err != nil {
		t.Fatalf("Limit: %v", err)
	}
	if lim.CostMicroUSD != tectoCusto {
		t.Errorf("com AOS_BUDGET_MAX_COST_MICRO_USD em vigor o Limit TEM de trazer a dimensao $ (%d), got %d — a zero, consumedFraction ignora-a e o aviso fica cego na dimensao que decide", tectoCusto, lim.CostMicroUSD)
	}
	if lim.Tokens != tectoTokens {
		t.Errorf("Limit.Tokens = %d, want %d", lim.Tokens, tectoTokens)
	}

	// Sem tecto em $ a dimensão fica a ZERO — e isso continua a ser o correcto: o valor em vigor
	// é UnlimitedCostMicroUSD, e uma fracção sobre esse denominador seria zero para sempre.
	semLim, err := runBudgetReader{rb: semTecto}.Limit(ctx, "qualquer")
	if err != nil {
		t.Fatalf("Limit: %v", err)
	}
	if semLim.CostMicroUSD != 0 {
		t.Errorf("sem tecto em $ o Limit tem de deixar a dimensao a ZERO (medir nao e decidir), got %d", semLim.CostMicroUSD)
	}
}

// TestAOS260_TectoEmDolaresExigeFonteDePreco: cruzamento das duas posturas no ARRANQUE. Sem
// fonte de preço a dimensão $ compararia sempre contra zero e NUNCA negaria — um tecto escrito
// na config, anunciado no banner e inexistente no comportamento. É o molde de
// [ErrProgressBudgetUnwired] e [ErrBreakerVelocitySourceUnwired], aplicado ao eixo $.
func TestAOS260_TectoEmDolaresExigeFonteDePreco(t *testing.T) {
	comTecto, err := integration.NewRunBudget(200_000, integration.WithMaxCostMicroUSDPerRun(5_000_000))
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	semTecto, err := integration.NewRunBudget(200_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}

	t.Run("modelo de referencia recusa o tecto em $", func(t *testing.T) {
		// cfg.Model nil ⇒ referenceModel, cujo custo por turno é a constante FABRICADA.
		err := requireCostSourceForBudgetCap(comTecto, nil)
		if !errors.Is(err, ErrBudgetCostNoPriceSource) {
			t.Fatalf("um tecto em $ sobre o modelo de referencia tem de ABORTAR o arranque (o custo e inventado e passaria a NEGAR runs): %v", err)
		}
		if !strings.Contains(err.Error(), "AOS_MODEL_PRICING_PATH") {
			t.Errorf("a recusa tem de nomear o remedio alcancavel: %v", err)
		}
	})

	t.Run("par sem preco na tabela recusa o tecto em $", func(t *testing.T) {
		// O caso do achado: gpt-4o com a região default do nó, que a tabela embebida não cobre.
		t.Setenv("AOS_MODEL_PRICING_PATH", "")
		t.Setenv("AOS_MODEL_NAME", "gpt-4o")
		t.Setenv("AOS_MODEL_REGION", defaultModelGatewayRegion)
		if modelPricingPostureFromEnv().Armed {
			t.Skip("a tabela embebida passou a cobrir este par — o cenario do achado deixou de ser reprodutivel por aqui")
		}
		err := requireCostSourceForBudgetCap(comTecto, referenceModelParaTeste{})
		if !errors.Is(err, ErrBudgetCostNoPriceSource) {
			t.Fatalf("um tecto em $ sem preco para o par (modelo, regiao) tem de ABORTAR: %v", err)
		}
	})

	t.Run("sem tecto em $ nao ha nada a exigir", func(t *testing.T) {
		if err := requireCostSourceForBudgetCap(semTecto, nil); err != nil {
			t.Errorf("sem AOS_BUDGET_MAX_COST_MICRO_USD o gate nao pode morder (dolares MEDIDOS sem tecto e o default): %v", err)
		}
		if err := requireCostSourceForBudgetCap(nil, nil); err != nil {
			t.Errorf("sem orcamento composto o gate nao pode morder: %v", err)
		}
	})

	t.Run("o composition-root corre o gate", func(t *testing.T) {
		fonte := lerFonteDoNo(t, "bootstrap.go")
		if !strings.Contains(fonte, "requireCostSourceForBudgetCap(runBudget, cfg.Model)") {
			t.Error("o Bootstrap deixou de cruzar as duas posturas — o tecto em $ voltaria a ser aceite sem fonte de preco, e o banner anunciaria uma proteccao inexistente")
		}
	})
}

// referenceModelParaTeste é um [agentruntime.ModelClient] INJECTADO qualquer: serve para o gate
// sair do ramo do modelo de referência e chegar ao da tabela de preços.
type referenceModelParaTeste struct{}

func (referenceModelParaTeste) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{Final: true}, nil
}

// TestAOS260_BannerDeclaraAAdmissaoComposta: postura anunciada = postura ligada. O ramo
// composto do banner tem de nomear a porta e o que ela faz; se alguém remover o wiring de
// `bootstrap.go`, o gate de composição abaixo avermelha.
func TestAOS260_BannerDeclaraAAdmissaoComposta(t *testing.T) {
	t.Parallel()
	composto := strings.Join(budgetPostureBanner(true), "\n")
	for _, marcador := range []string{"agentruntime.ModelAdmission", "budget_exhausted", "waiting_on_human", "run_id:step_id"} {
		if !strings.Contains(composto, marcador) {
			t.Errorf("o banner composto devia nomear %q — sem isso o operador nao sabe o que muda quando esgota:\n%s", marcador, composto)
		}
	}
	fonte := lerFonteDoNo(t, "bootstrap.go")
	if !strings.Contains(fonte, "agentruntime.WithModelAdmission(modelAdmission.port())") {
		t.Error("o composition-root deixou de ligar a porta de admissao do turno de modelo — o banner passaria a anunciar um tecto sobre a inferencia que nao existe")
	}
	if !strings.Contains(fonte, "newNodeModelAdmission(runBudget, exhaustion, log)") {
		t.Error("a admissao tem de derivar do ESTADO composto (o orcamento e o prompt de exaustao reais), nunca de literais")
	}
}
