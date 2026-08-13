package integration

// AOS-256 + AOS-257 — provas do ORÇAMENTO POR-RUN ligado à cadeia REAL.
//
// O risco que estes testes existem para apanhar não é «o orçamento não nega»; é o inverso:
// um orçamento composto SEM o nó por-run registado faz [budget.Budget.Reserve] devolver
// ErrUnknownNode, o adaptador converte-o em deny, e o nó NEGA 100% das tool calls com uma
// razão que parece falta de orçamento. Por isso a prova central é o CAMINHO FELIZ — uma tool
// call que ATRAVESSA a cadeia inteira com o orçamento ligado e EXECUTA — acompanhada da sua
// prova negativa (o mesmo hook, sem nó, nega).
//
// O segundo eixo é a FUGA de headroom: a reserva é feita no hook, mas o desfecho só se
// conhece a jusante. Um `deny` do egress (o único hook DEPOIS do orçamento na ordem canónica
// de AOS-154) ou um erro fatal do despacho deixariam a reserva pendente para sempre — o run
// acabaria negado por «falta de orçamento» sem ter gasto nada.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	authz "github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/memory/provenance"
	domain "github.com/aos-ref/platform/registry/domain"
	toolset "github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// budgetHarness é a CADEIA REAL de produção (a mesma de [TestDemo_PermitNodeEndToEnd]) com o
// orçamento ligado: identidade verificada, catálogo assinado, PDP com o bundle de referência
// e uma Authority que concede LEGITIMAMENTE cap:fs.read. Nada aqui relaxa a postura do nó —
// o que muda entre os casos é só o RECURSO da tool call (que decide o veredicto do egress).
type budgetHarness struct {
	sec       *SecuredRuntime
	rb        *RunBudget
	goal      agentruntime.Goal
	execCount *int
	// panicNaTool arma um PANIC dentro do handler da tool. O RM só recupera panics de
	// HOOKS ([safeEvaluate]); um panic do DESPACHO atravessa `Mediate` e é a única forma de
	// exercer o ramo de panic do decorator de saldo PELA CADEIA COMPOSTA — ver
	// [TestAOS257_PanicDaToolLibertaAReservaPelaCadeiaComposta].
	panicNaTool *bool
}

const (
	bhIssuer = "iss:test-idp"
	bhUser   = "human:alice"
	bhAgent  = "agt-budget"
	bhClass  = "agent-worker" // classe allowlisted para cap:fs.read no bundle de referência
	bhCap    = "cap:fs.read"
)

// bhCall é o RECURSO de uma tool call do guião: "doc" faz o hook de egress ABSTER-SE
// (caminho feliz); "url" fá-lo DECIDIR contra a allowlist embutida — que nega tudo a esta
// classe (default deny) —, o que dá a negação A JUSANTE do orçamento.
type bhCall struct{ resType, resValue string }

// bhInput é o payload de TODAS as tool calls do guião (uma constante para que a estimativa
// seja derivável sem duplicar o literal).
const bhInput = `{"doc_id":"notes"}`

// bhEstimativa devolve os tokens que o estimador REAL (AOS-258, [TokenOnlyEstimator]) atribui
// à call que o loop constrói para este [bhCall].
//
// Os tectos dos testes DERIVAM daqui em vez de serem constantes calibradas à mão: a estimativa
// depende do estimador composto, e um número copiado passaria a mentir em silêncio assim que
// ele mudasse — que foi exactamente o que aconteceu quando AOS-258 substituiu o
// [budget.DefaultEstimator] (o tecto de 6 tokens, calibrado para «~5 tokens por call», passou a
// negar tudo).
func bhEstimativa(c bhCall) int64 {
	return TokenOnlyEstimator(&referencemonitor.Call{
		ToolID:     "doc_read",
		Capability: bhCap,
		Resource:   referencemonitor.Resource{Type: c.resType, Value: c.resValue, Region: "eu"},
		Input:      []byte(bhInput),
	}).Tokens
}

// newBudgetHarness monta a cadeia com um turno por entrada de calls (mais um turno final).
//
// maxTokens <= 0 monta a MESMA cadeia SEM orçamento nenhum ([SecuredConfig.Budget] nil), que
// é o default do nó — e nesse caso [budgetHarness.rb] fica nil.
func newBudgetHarness(t *testing.T, runID string, maxTokens int64, calls ...bhCall) *budgetHarness {
	t.Helper()
	return newBudgetHarnessWith(t, runID, maxTokens, nil, calls)
}

// newBudgetHarnessWith é o mesmo harness com um ponto de extensão: `extra` são opções do
// [agentruntime.Runtime] a acrescentar à composição. Existe para AOS-260 poder ligar a porta
// de admissão do turno de modelo NA CADEIA REAL (e não numa composição paralela) sem duplicar
// as ~100 linhas de identidade/catálogo/PDP que este harness monta.
func newBudgetHarnessWith(t *testing.T, runID string, maxTokens int64, extra []agentruntime.Option, calls []bhCall) *budgetHarness {
	t.Helper()
	ctx := context.Background()

	anchor, err := base64.StdEncoding.DecodeString("tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=")
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	policyDP, err := pdp.Open("../control-plane/pdp/policies", pdp.WithTrustAnchor(ed25519.PublicKey(anchor)))
	if err != nil {
		t.Fatalf("pdp.Open (bundle de referência): %v", err)
	}

	pub, priv := enfKeys(0x31)
	classes := map[string]identity.ClassPolicy{bhClass: {TTL: 5 * time.Minute, Scope: []string{bhCap}}}
	iss, err := identity.NewIssuer(bhIssuer, priv, classes, identity.WithIssuerClock(enfClock()))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier := identity.NewVerifier(identity.WithTrustedIssuer(bhIssuer, pub), identity.WithVerifierClock(enfClock()))
	tok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID: bhUser, AgentID: bhAgent, AgentClass: bhClass,
		PolicyRef: "policy://agent-worker@1", UserAuthority: []string{bhCap},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	authority := authz.NewStaticAuthoritySource().
		Set(bhUser, bhCap).
		Set(bhAgent, bhCap).
		Set("agent:"+bhClass, bhCap)

	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)
	entry := signedEntry(t, signer, "doc_read", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	catalog := &fakeCatalog{entries: []domain.Entry{entry}}
	rv := newRevalidator(t, trust, auditStore,
		NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock())),
		NewRecordingAlerter())

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = trajStore.Close() })

	var rb *RunBudget
	if maxTokens > 0 {
		rb, err = NewRunBudget(maxTokens)
		if err != nil {
			t.Fatalf("NewRunBudget: %v", err)
		}
	}

	model := &scriptedModel{}
	for _, c := range calls {
		model.responses = append(model.responses, agentruntime.ModelResponse{
			Text: "vou ler o documento",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "doc_read", Capability: bhCap, Input: []byte(bhInput),
				ResourceType: c.resType, ResourceValue: c.resValue, ResourceRegion: "eu",
			}},
			Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
		})
	}
	model.responses = append(model.responses, agentruntime.ModelResponse{
		Text: "concluo", Final: true, Usage: agentruntime.Usage{InputTokens: 6, OutputTokens: 3},
	})

	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:          model,
		Recorder:       agentruntime.NewTurnRecorder(trajStore),
		Catalog:        catalog,
		Revalidator:    rv,
		Policy:         StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:           audit.NewMemStore(),
		Verifier:       verifier,
		Authority:      authority,
		PDP:            policyDP,
		Budget:         rb, // <-- AOS-257: o BudgetCheck REAL no lugar do BudgetStub (nil ⇒ stub)
		FreezeOptions:  []toolset.Option{toolset.WithClock(fixedClock())},
		RuntimeOptions: extra, // AOS-260: onde entra a porta de admissão do turno de modelo
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	execCount := 0
	panicNaTool := false
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		if panicNaTool {
			panic("a tool explodiu a meio do efeito")
		}
		execCount++
		return []byte(`{"content":"ok"}`), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	return &budgetHarness{
		sec: sec,
		rb:  rb,
		goal: agentruntime.Goal{
			RunID: runID,
			Principal: referencemonitor.Principal{
				NHIID: bhAgent, AgentID: bhAgent, AgentClass: bhClass, Authority: []string{bhCap},
				DelegationChain: []referencemonitor.DelegationHop{{Sub: bhUser, ActAs: bhAgent}},
			},
			Scope:      []string{bhCap},
			Credential: tok.Compact,
			Model:      agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
			System:     "assistente de leitura de documentos",
			Objective:  "le o documento notes",
		},
		execCount:   &execCount,
		panicNaTool: &panicNaTool,
	}
}

// TestAOS256_CaminhoFeliz_RunComOrcamentoExecutaAToolCall é a prova CENTRAL de AOS-256: com o
// orçamento composto e um tecto folgado, uma tool call atravessa a cadeia inteira e EXECUTA.
//
// Sem o [SecuredRuntime.Run] registar o nó do run, este teste falharia com execCount==0 — e
// falharia para TODAS as tool calls de TODOS os runs. É por isso que a prova é o permit e não
// só a negação: uma suite que só testasse denies ficaria verde com o wiring partido.
func TestAOS256_CaminhoFeliz_RunComOrcamentoExecutaAToolCall(t *testing.T) {
	h := newBudgetHarness(t, "run-budget-permit", 10_000, bhCall{"doc", "notes"})

	if _, _, err := h.sec.Run(context.Background(), h.goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *h.execCount != 1 {
		t.Fatalf("a tool devia ter executado 1x (permit ponta-a-ponta com orcamento composto); execCount=%d — ErrUnknownNode do orcamento apresenta-se exactamente assim", *h.execCount)
	}
	// O nó do run foi LIBERTADO no fim (o `defer` do seam): já não existe na árvore.
	if _, ok := h.rb.AvailableTokens(h.goal.RunID); ok {
		t.Errorf("o no de orcamento do run %q continua vivo depois de Run devolver — a libertacao do seam nao correu", h.goal.RunID)
	}
}

// TestAOS256_SemNoRegistadoOHookNegaTudo é a PROVA NEGATIVA do teste anterior: o mesmo
// adaptador, sobre a mesma árvore, sem nó registado para o run, NEGA. É o que torna o
// caminho feliz não-vacuoso — e é literalmente o modo de falha que o ticket nomeia.
func TestAOS256_SemNoRegistadoOHookNegaTudo(t *testing.T) {
	t.Parallel()

	rb, err := NewRunBudget(10_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	call := referencemonitor.Call{RunID: "run-sem-no", StepID: "s1", Input: []byte(`{"a":1}`)}

	res, err := rb.check.Evaluate(context.Background(), &call)
	if err != nil {
		t.Fatalf("Evaluate devolveu erro (o adaptador nunca devolve erro; audita a decisao): %v", err)
	}
	if res.Decision != referencemonitor.HookDeny {
		t.Fatalf("sem no de orcamento registado o hook TEM de negar fail-closed; decisao=%v", res.Decision)
	}
	if !strings.Contains(res.Reason, budget.ErrUnknownNode.Code) {
		t.Errorf("a razao devia nomear %s (e o sintoma exacto de compor o hook sem registar o no do run); reason=%q", budget.ErrUnknownNode.Code, res.Reason)
	}
}

// TestAOS256_LibertacaoGarantidaEmRetornoErroEPanic prova o outro lado do ciclo de vida: a
// libertação corre nos TRÊS caminhos. Sem ela, cada run deixaria um nó vivo para sempre e a
// RETOMA do mesmo RunID (que reutiliza o id) colidiria com ErrNodeExists.
func TestAOS256_LibertacaoGarantidaEmRetornoErroEPanic(t *testing.T) {
	t.Parallel()

	rb, err := NewRunBudget(1_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}

	// (a) retorno normal.
	release, err := rb.acquire("run-a")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, ok := rb.AvailableTokens("run-a"); !ok {
		t.Fatal("o no do run devia existir depois do acquire")
	}
	release()
	if _, ok := rb.AvailableTokens("run-a"); ok {
		t.Error("o no do run devia ter sido libertado")
	}

	// (b) PANIC a atravessar o `defer`.
	func() {
		defer func() { _ = recover() }()
		rel, aerr := rb.acquire("run-b")
		if aerr != nil {
			t.Errorf("acquire: %v", aerr)
			return
		}
		defer rel()
		panic("falha a meio do run")
	}()
	if _, ok := rb.AvailableTokens("run-b"); ok {
		t.Error("o no do run devia ter sido libertado mesmo com panic — o `defer` do seam e a garantia")
	}

	// (c) o MESMO RunID pode voltar a ser adquirido (retoma) — sem libertação isto seria
	//     ErrNodeExists e a retoma nunca mais corria.
	rel, err := rb.acquire("run-a")
	if err != nil {
		t.Fatalf("re-acquire do mesmo RunID falhou (%v) — a libertacao nao removeu o no", err)
	}
	rel()
}

// TestAOS256_RunLibertaONoQuandoOLoopFalha fecha o caminho de ERRO pelo seam REAL: um modelo
// sem guião faz o loop falhar no primeiro turno, e o nó do run tem de desaparecer na mesma.
func TestAOS256_RunLibertaONoQuandoOLoopFalha(t *testing.T) {
	h := newBudgetHarness(t, "run-budget-erro", 10_000, bhCall{"doc", "notes"})
	// Guião VAZIO ⇒ o scriptedModel devolve erro no turno 1 ⇒ rt.Run devolve erro.
	h.sec.rt = agentruntime.New(&scriptedModel{}, h.sec.rm, agentruntime.NewTurnRecorder(mustEventStore(t)))

	if _, _, err := h.sec.Run(context.Background(), h.goal, nil); err == nil {
		t.Fatal("o Run devia ter falhado (guiao de modelo vazio) — o teste precisa do caminho de erro")
	}
	if _, ok := h.rb.AvailableTokens(h.goal.RunID); ok {
		t.Error("o no de orcamento continua vivo depois de o run FALHAR — a libertacao tem de cobrir tambem o erro")
	}
}

// TestAOS256_DoisRunsEmParaleloNaoPartilhamTecto é a versão CONCORRENTE de facto de
// [TestAOS256_DoisRunsSequenciaisNaoPartilhamTecto]: as duas queimaduras correm em
// GOROUTINES sobre a MESMA árvore, ao mesmo tempo.
//
// O teste sequencial prova a propriedade D-A1.3 (tectos independentes) mas não toca no eixo
// que o nome «concorrentes» sugere: que a contabilidade por-nó da árvore aguenta reservas
// simultâneas sem que uma corrida faça o run B herdar o gasto do run A (ou vice-versa). É
// isso que se prova aqui — e corre com `-race`, que é onde a prova tem valor.
func TestAOS256_DoisRunsEmParaleloNaoPartilhamTecto(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Tecto = 4 reservas por run. Cada goroutine faz EXACTAMENTE 4: se os tectos fossem
	// partilhados, as 8 reservas não caberiam e pelo menos uma seria negada.
	unidade := TokenOnlyEstimator(&referencemonitor.Call{Input: []byte(bhInput)}).Tokens
	const reservasPorRun = 4
	rb, err := NewRunBudget(unidade * reservasPorRun)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}

	runs := []string{"run-par-A", "run-par-B"}
	negados := make([]int32, len(runs))
	arranca := make(chan struct{})
	var wg sync.WaitGroup
	for i, run := range runs {
		rel, aerr := rb.acquire(run)
		if aerr != nil {
			t.Fatalf("acquire %s: %v", run, aerr)
		}
		defer rel()

		wg.Add(1)
		go func(idx int, runID string) {
			defer wg.Done()
			<-arranca // as duas queimaduras arrancam JUNTAS
			for j := 0; j < reservasPorRun; j++ {
				call := referencemonitor.Call{RunID: runID, StepID: fmt.Sprintf("s%d", j), Input: []byte(bhInput)}
				res, _ := rb.check.Evaluate(ctx, &call)
				if res.Decision != referencemonitor.HookAllow {
					atomic.AddInt32(&negados[idx], 1)
				}
			}
		}(i, run)
	}
	close(arranca)
	wg.Wait()

	for i, run := range runs {
		if n := atomic.LoadInt32(&negados[i]); n != 0 {
			t.Fatalf("o run %q teve %d reserva(s) NEGADA(S) de %d — cada run tem tecto para todas elas; uma negacao aqui significa que os tectos foram PARTILHADOS sob concorrencia (contra D-A1.3)", run, n, reservasPorRun)
		}
	}
	// Não-vacuosidade: o tecto de cada run está mesmo esgotado — a 5.ª reserva NEGA. Sem
	// isto, um tecto acidentalmente infinito passaria neste teste.
	for _, run := range runs {
		call := referencemonitor.Call{RunID: run, StepID: "extra", Input: []byte(bhInput)}
		res, _ := rb.check.Evaluate(ctx, &call)
		if res.Decision != referencemonitor.HookDeny {
			t.Fatalf("o run %q devia estar ESGOTADO apos %d reservas (decisao=%v) — senao a prova acima e vacuosa", run, reservasPorRun, res.Decision)
		}
	}
}

// TestAOS257_PanicDaToolLibertaAReservaPelaCadeiaComposta exerce o ramo de PANIC do saldo
// PELA CADEIA QUE [NewSecuredRuntime] COMPÕE — não por um `budgetSettlingDispatcher{}`
// instanciado à mão.
//
// A distinção é o ponto do teste: [TestAOS257_SaldoDoDecorator] prova a LÓGICA dos quatro
// desfechos sobre o decorator isolado e sobrevive a alguém desligá-lo do wiring; este prova a
// COSTURA. O panic é a única via de o fazer sem execução durável: o RM recupera panics de
// HOOKS ([safeEvaluate]) mas NÃO do despacho da tool, pelo que um handler que explode
// atravessa `Mediate`, `mediateDispatcher` e o `defer` do decorator composto.
//
// (O ramo de ERRO não é alcançável por esta via, e não é omissão: `Monitor.Mediate` só
// devolve erro no caminho do contexto JÁ cancelado, que retorna ANTES da cadeia de hooks —
// logo antes de existir qualquer reserva para libertar. Com execução durável o erro vem do
// `DurableDispatcher`, que o decorator envolve pela mesma via.)
func TestAOS257_PanicDaToolLibertaAReservaPelaCadeiaComposta(t *testing.T) {
	const tecto = 10_000
	h := newBudgetHarness(t, "run-budget-panic", tecto, bhCall{"doc", "notes"})
	*h.panicNaTool = true

	// Segunda aquisição do MESMO run: mantém o nó vivo depois de o `defer` de
	// [SecuredRuntime.Run] o libertar, para que o headroom seja OBSERVÁVEL a seguir ao
	// panic. Sem isto o nó desaparecia e a asserção não teria onde olhar.
	rel, err := h.rb.acquire(h.goal.RunID)
	if err != nil {
		t.Fatalf("acquire (observador): %v", err)
	}
	defer rel()

	antes, ok := h.rb.AvailableTokens(h.goal.RunID)
	if !ok || antes != tecto {
		t.Fatalf("headroom inicial errado: %d (ok=%v), esperado %d", antes, ok, tecto)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("o panic da tool devia ter atravessado a cadeia (o RM so recupera panics de HOOKS) — sem panic este teste nao exerce o ramo que existe para exercer")
			}
		}()
		_, _, _ = h.sec.Run(context.Background(), h.goal, nil)
	}()

	depois, ok := h.rb.AvailableTokens(h.goal.RunID)
	if !ok {
		t.Fatal("o no de orcamento devia continuar vivo (a segunda aquisicao segura-o)")
	}
	if depois != antes {
		t.Fatalf("headroom apos o panic = %d, esperado %d: a reserva da tool call FICOU PRESA — o `defer` do budgetSettlingDispatcher composto por NewSecuredRuntime nao libertou", depois, antes)
	}
}

// TestAOS256_DoisRunsSequenciaisNaoPartilhamTecto sela D-A1.3: o tecto é POR-RUN, nunca
// por-mandato nem global. Esgotar o run A não pode negar o run B — se a raiz da árvore fosse
// finita, negava. O nome diz SEQUENCIAIS porque é o que o teste faz; a prova sob
// concorrência real está em [TestAOS256_DoisRunsEmParaleloNaoPartilhamTecto].
func TestAOS256_DoisRunsSequenciaisNaoPartilhamTecto(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Tecto = duas reservas por run (derivado do estimador composto, AOS-258 — nunca uma
	// constante calibrada à mão): o run A esgota-o à terceira e o run B, intocado, continua a
	// ser admitido.
	limite := TokenOnlyEstimator(&referencemonitor.Call{Input: []byte(bhInput)}).Tokens * 2
	rb, err := NewRunBudget(limite)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	relA, err := rb.acquire("run-A")
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	defer relA()
	relB, err := rb.acquire("run-B")
	if err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	defer relB()

	// Esgota o run A: reserva repetidamente até o hook negar.
	negouA := false
	for i := 0; i < 20 && !negouA; i++ {
		call := referencemonitor.Call{RunID: "run-A", StepID: string(rune('a' + i)), Input: []byte(bhInput)}
		res, _ := rb.check.Evaluate(ctx, &call)
		negouA = res.Decision == referencemonitor.HookDeny
	}
	if !negouA {
		t.Fatalf("o run A devia ter esgotado o tecto de %d tokens", limite)
	}

	// O run B, intocado, continua a ser admitido.
	callB := referencemonitor.Call{RunID: "run-B", StepID: "s1", Input: []byte(bhInput)}
	resB, _ := rb.check.Evaluate(ctx, &callB)
	if resB.Decision != referencemonitor.HookAllow {
		t.Fatalf("o run B foi negado (%v, %q) por causa do gasto do run A — o tecto esta a ser PARTILHADO, contra D-A1.3", resB.Decision, resB.Reason)
	}
	if av, _ := rb.AvailableTokens("run-B"); av >= limite {
		t.Errorf("a reserva do run B nao debitou o proprio no (available=%d, limite=%d)", av, limite)
	}
}

// TestAOS256_TectoEPorIncarnacaoComoODeclarado sela a GRANULARIDADE que o banner e o
// `deploy/node/README.md` declaram por escrito — as DUAS metades, porque só juntas dizem a
// verdade e cada uma sozinha diz uma mentira diferente:
//
//   - hospedagens SOBREPOSTAS do mesmo RunID PARTILHAM o nó (a reentrância é contada). Sem
//     isto, uma retoma que se sobrepusesse à hospedagem anterior duplicaria o tecto de forma
//     invisível — e ainda por cima apagaria o nó do run vivo à primeira libertação;
//   - uma RE-hospedagem depois de a última libertação ter corrido recebe o tecto INTEIRO. É
//     a fuga que a auditoria nomeou (achado H2), e não se corrige aqui: fechá-la exige estado
//     de orçamento DURÁVEL por run (a árvore vive em memória e zera no restart de qualquer
//     forma). O que se corrigiu foi a DECLARAÇÃO — o banner dizia só "POR-RUN".
//
// Este teste é a amarra dessa declaração ao comportamento: se alguém tornar o tecto
// cumulativo entre incarnações, o banner e o README passam a MENTIR por baixo-declaração, e
// é aqui que isso avermelha. É o mesmo molde "postura anunciada = postura ligada" do gate de
// AOS-255 — que verifica o TEXTO; este verifica o FACTO que o texto afirma.
func TestAOS256_TectoEPorIncarnacaoComoODeclarado(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Tecto = exactamente UMA reserva: a segunda call da mesma incarnação já nega. É o que
	// torna "o tecto voltou inteiro" observável numa única reserva.
	limite := TokenOnlyEstimator(&referencemonitor.Call{Input: []byte(bhInput)}).Tokens
	rb, err := NewRunBudget(limite)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	const run = "run-retomado"

	reserva := func(step string) referencemonitor.HookDecision {
		call := referencemonitor.Call{RunID: run, StepID: step, Input: []byte(bhInput)}
		res, _ := rb.check.Evaluate(ctx, &call)
		return res.Decision
	}

	// --- Incarnação 1: esgota o tecto ---
	rel1, err := rb.acquire(run)
	if err != nil {
		t.Fatalf("acquire (incarnacao 1): %v", err)
	}
	if d := reserva("i1-a"); d != referencemonitor.HookAllow {
		t.Fatalf("a 1a reserva da incarnacao 1 devia ser admitida, veio %v", d)
	}
	if d := reserva("i1-b"); d != referencemonitor.HookDeny {
		t.Fatalf("a 2a reserva devia ESGOTAR o tecto (%d tokens), veio %v — sem isto o resto da prova e vacuoso", limite, d)
	}

	// --- Hospedagem SOBREPOSTA: reentra no MESMO nó, NÃO repõe o tecto ---
	rel1b, err := rb.acquire(run)
	if err != nil {
		t.Fatalf("acquire sobreposto (a retoma reentra no mesmo RunID): %v", err)
	}
	if d := reserva("i1-sobreposto"); d != referencemonitor.HookDeny {
		t.Fatalf("uma hospedagem SOBREPOSTA do mesmo run repos o tecto (decisao=%v): a reentrancia tem de contar, nao registar um no novo — dois hosts do mesmo run duplicariam o tecto em silencio", d)
	}
	// A primeira libertação NÃO pode apagar o nó enquanto a segunda hospedagem corre.
	rel1b()
	if _, vivo := rb.AvailableTokens(run); !vivo {
		t.Fatal("a libertacao de UMA hospedagem sobreposta apagou o no do run ainda vivo — a call seguinte seria negada por ErrUnknownNode, que parece falta de orcamento e e um defeito de ciclo de vida")
	}

	// --- Fim da última hospedagem: o nó sai da árvore ---
	rel1()
	if _, vivo := rb.AvailableTokens(run); vivo {
		t.Fatal("o no do run continuou vivo depois da ULTIMA libertacao — a garantia de libertacao de AOS-256 nao se cumpriu")
	}

	// --- Incarnação 2 (re-hospedagem: /resume apos aprovacao, ou restart) ---
	rel2, err := rb.acquire(run)
	if err != nil {
		t.Fatalf("acquire (incarnacao 2): %v", err)
	}
	defer rel2()
	if d := reserva("i2-a"); d != referencemonitor.HookAllow {
		t.Fatalf("a re-hospedagem do mesmo run devia receber o tecto INTEIRO (decisao=%v).\n"+
			"Se isto passou a NEGAR, o tecto tornou-se cumulativo entre incarnacoes — o que seria uma MELHORIA, mas entao o banner (budgetPostureBanner) e o deploy/node/README.md passaram a mentir por baixo-declaracao: dizem que cada re-hospedagem recebe o tecto inteiro e que um run em ciclo de escalada/retoma pode gastar N x AOS_BUDGET_MAX_TOKENS. Actualize os DOIS textos.", d)
	}
	if av, _ := rb.AvailableTokens(run); av != 0 {
		t.Errorf("o no da incarnacao 2 devia ter o tecto inteiro menos a reserva feita (esperado 0, veio %d)", av)
	}
}

// mustEventStore é um Event Store in-memory para os casos em que só se precisa de um
// TurnRecorder válido.
func mustEventStore(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

// ---------------------------------------------------------------------------
// AOS-257 — o SALDO (Settle) e as FUGAS reais
// ---------------------------------------------------------------------------

// TestAOS257_SemFugaAposDenyDoEgress é a prova da fuga que o desafio A1 nomeou. O egress é o
// ÚNICO hook depois do orçamento na ordem canónica de AOS-154: uma tool call cujo destino não
// está na allowlist obtém RESERVA do orçamento e DENY do egress. Sem o saldo no decorator, o
// headroom reservado ficava preso e o run acabaria negado por «falta de orçamento» sem ter
// executado uma única tool.
func TestAOS257_SemFugaAposDenyDoEgress(t *testing.T) {
	// TECTO APERTADO de propósito: cabe exactamente UMA reserva de cada vez (o tecto é a maior
	// das duas estimativas, e a soma das duas excede-o). É isso que torna a prova observável —
	// a segunda call só é admitida se a primeira tiver DEVOLVIDO o headroom.
	//
	// O tecto DERIVA do estimador composto (AOS-258) em vez de ser um número calibrado à mão:
	// uma constante ficaria obsoleta na próxima troca de estimador e passaria a negar tudo.
	negada := bhCall{"url", "https://api.github.com/repos"} // reserva concedida, NEGADA a jusante pelo egress
	permitida := bhCall{"doc", "notes"}                     // permitida — só corre se o headroom voltou
	limite := bhEstimativa(negada)
	if e := bhEstimativa(permitida); e > limite {
		limite = e
	}
	if limite >= bhEstimativa(negada)+bhEstimativa(permitida) {
		t.Fatalf("o tecto (%d) tem de ser APERTADO — as duas calls (%d + %d) nao podem caber ao mesmo tempo, senao a prova da fuga fica vacuosa",
			limite, bhEstimativa(negada), bhEstimativa(permitida))
	}
	h := newBudgetHarness(t, "run-budget-egress", limite, negada, permitida)

	if _, _, err := h.sec.Run(context.Background(), h.goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *h.execCount != 1 {
		t.Fatalf("execCount=%d, esperado 1: a 1a call e negada pelo EGRESS (a jusante do orcamento) e a 2a e permitida.\n"+
			"execCount=0 significa que a reserva da 1a call FICOU PRESA — o tecto de %d tokens esgotou-se sem que uma unica tool tivesse corrido, e a 2a call foi negada por 'falta de orcamento' em vez de executar.", *h.execCount, limite)
	}
	// O nó do run foi libertado no fim (o `defer` do seam de AOS-256).
	if _, ok := h.rb.AvailableTokens(h.goal.RunID); ok {
		t.Error("o no de orcamento continua vivo depois do run")
	}
}

// erroringDispatcher devolve o erro FATAL que o loop propaga (o caminho de
// `runtime_ports.go`: cancelamento/erro que não é uma negação).
type erroringDispatcher struct{ err error }

func (d erroringDispatcher) Dispatch(context.Context, referencemonitor.Call) (referencemonitor.Decision, error) {
	return referencemonitor.Decision{}, d.err
}

// fixedDecisionDispatcher devolve sempre a mesma [referencemonitor.Decision].
type fixedDecisionDispatcher struct{ dec referencemonitor.Decision }

func (d fixedDecisionDispatcher) Dispatch(context.Context, referencemonitor.Call) (referencemonitor.Decision, error) {
	return d.dec, nil
}

// panickingDispatcher entra em panic (regressão do `defer` com retornos nomeados).
type panickingDispatcher struct{}

func (panickingDispatcher) Dispatch(context.Context, referencemonitor.Call) (referencemonitor.Decision, error) {
	panic("despacho rebentou")
}

// TestAOS257_SaldoDoDecorator exercita os QUATRO desfechos sobre o adaptador REAL: só o
// permit CONFIRMA (o headroom fica gasto); deny, erro fatal e panic DEVOLVEM o headroom.
//
// Um teste que só verificasse o release seria satisfeito por um decorator que libertasse
// sempre — e um orçamento que nunca confirma nada nunca nega ninguém. Daí o caso do permit.
func TestAOS257_SaldoDoDecorator(t *testing.T) {
	t.Parallel()

	const limite = 1_000
	casos := []struct {
		nome        string
		inner       agentruntime.ActivityDispatcher
		panica      bool
		devolveTudo bool // true ⇒ o headroom volta ao limite; false ⇒ fica debitado
	}{
		{
			nome:        "permit confirma (Commit)",
			inner:       fixedDecisionDispatcher{dec: referencemonitor.Decision{Effect: referencemonitor.EffectPermit}},
			devolveTudo: false,
		},
		{
			nome:        "deny a jusante liberta",
			inner:       fixedDecisionDispatcher{dec: referencemonitor.Decision{Effect: referencemonitor.EffectDeny, DeniedBy: "egress"}},
			devolveTudo: true,
		},
		{
			nome:        "escalate liberta",
			inner:       fixedDecisionDispatcher{dec: referencemonitor.Decision{Effect: referencemonitor.EffectEscalate}},
			devolveTudo: true,
		},
		{
			nome:        "erro fatal do despacho liberta",
			inner:       erroringDispatcher{err: errors.New("ledger indisponivel")},
			devolveTudo: true,
		},
		{
			nome:        "panic liberta",
			inner:       panickingDispatcher{},
			panica:      true,
			devolveTudo: true,
		},
	}

	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			ctx := context.Background()
			rb, err := NewRunBudget(limite)
			if err != nil {
				t.Fatalf("NewRunBudget: %v", err)
			}
			rel, err := rb.acquire("run-x")
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			defer rel()

			call := referencemonitor.Call{RunID: "run-x", StepID: "s1", Input: []byte(bhInput)}
			// A RESERVA é a do hook real — é este passo que o `Mediate` faz por dentro.
			res, _ := rb.check.Evaluate(ctx, &call)
			if res.Decision != referencemonitor.HookAllow {
				t.Fatalf("a reserva devia ter sido concedida (tecto folgado); %v %q", res.Decision, res.Reason)
			}
			reservado, _ := rb.AvailableTokens("run-x")
			if reservado >= limite {
				t.Fatalf("a reserva nao debitou o no (available=%d)", reservado)
			}

			dispatch := func() {
				if tc.panica {
					defer func() { _ = recover() }()
				}
				_, _ = budgetSettlingDispatcher{inner: tc.inner, check: rb.check}.Dispatch(ctx, call)
			}
			dispatch()

			depois, ok := rb.AvailableTokens("run-x")
			if !ok {
				t.Fatal("o no do run desapareceu")
			}
			switch {
			case tc.devolveTudo && depois != limite:
				t.Errorf("FUGA DE HEADROOM: available=%d, esperado %d — a reserva ficou presa neste desfecho e o run acabaria negado por «falta de orcamento» sem ter gasto nada", depois, limite)
			case !tc.devolveTudo && depois == limite:
				t.Errorf("o permit NAO confirmou a reserva (available=%d == limite): um orcamento que nunca debita nunca nega ninguem", depois)
			}

			// O headroom SOZINHO não distingue «confirmado» de «ainda pendente»: uma reserva
			// pendente e um débito confirmado subtraem exactamente o mesmo a `Available`
			// (Limit − Reserved − Committed). Um decorator que NÃO fizesse nada em permit
			// passaria o ramo de cima — verificado por mutação. O que separa os dois estados
			// é o par Reserved/Committed do nó, e é por isso que a asserção desce a ele:
			// «commit em permit» (o critério de AOS-257) tem de ser observado como COMMIT,
			// não como ausência de release. Sem isto, uma reserva eternamente pendente ficava
			// verde aqui e só se manifestava no que o log durável NÃO tem (budget.committed).
			estado := rb.tree.Snapshot()["run-x"]
			if tc.devolveTudo {
				if !estado.Reserved.IsZero() || !estado.Committed.IsZero() {
					t.Errorf("apos release o no devia ficar LIMPO; reserved=%v committed=%v", estado.Reserved, estado.Committed)
				}
				return
			}
			if !estado.Reserved.IsZero() {
				t.Errorf("o permit deixou a reserva PENDENTE (reserved=%v): o decorator nao confirmou — o headroom parece gasto mas a reserva nunca se resolveu", estado.Reserved)
			}
			if estado.Committed.Tokens != limite-depois {
				t.Errorf("o permit devia ter CONFIRMADO %d tokens (Reserved→Committed); committed=%v", limite-depois, estado.Committed)
			}
		})
	}
}

// TestAOS257_TectoInvalidoERecusadoNaConstrucao sela o fail-closed da construção: um tecto
// <= 0 não é «orçamento desligado», é «tudo negado» (nenhuma estimativa cabe em zero).
func TestAOS257_TectoInvalidoERecusadoNaConstrucao(t *testing.T) {
	t.Parallel()

	for _, tokens := range []int64{0, -1} {
		if _, err := NewRunBudget(tokens); !errors.Is(err, ErrBudgetLimitInvalid) {
			t.Errorf("NewRunBudget(%d) devia recusar com ErrBudgetLimitInvalid; err=%v", tokens, err)
		}
	}
	if _, err := NewRunBudget(1); err != nil {
		t.Errorf("NewRunBudget(1) devia ser aceite; err=%v", err)
	}
}

// TestAOS257_SemOrcamentoNadaMuda é a retro-compatibilidade: sem [SecuredConfig.Budget] o
// ponto de injecção fica com o stub neutro, o seam por-run não regista nada, e a mesma tool
// call permitida continua a executar.
func TestAOS257_SemOrcamentoNadaMuda(t *testing.T) {
	h := newBudgetHarness(t, "run-sem-orcamento", 0, bhCall{"doc", "notes"}) // 0 ⇒ SecuredConfig.Budget nil

	if h.rb != nil {
		t.Fatal("o harness devia ter sido montado SEM orcamento")
	}
	if _, _, err := h.sec.Run(context.Background(), h.goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *h.execCount != 1 {
		t.Fatalf("sem orcamento a tool permitida continua a executar; execCount=%d", *h.execCount)
	}
}
