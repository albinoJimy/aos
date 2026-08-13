package integration

// AOS-260 — provas da ADMISSÃO DO TURNO DE MODELO sobre a ÁRVORE DE ORÇAMENTO REAL.
//
// Nenhum destes testes simula o orçamento: a árvore é o [budget.Budget] de produção, o nó
// por-run é registado pelo MESMO seam de AOS-256 (`acquire`) e os números são lidos com
// `AvailableTokens`/`Available`. O que se prova é aquilo que separa esta entrega de uma
// contagem a posteriori:
//
//  1. reserva ANTES, saldo pelo REAL — e o saldo DEVOLVE o que a provisão sobrestimou;
//  2. esgotamento ⇒ negação ATRIBUÍVEL, sem reserva pendente a fugir;
//  3. REPLAY não re-reserva — nem no mesmo processo (dedup por `run_id:step_id`) nem numa
//     incarnação NOVA (o detector, que é a defesa que sobrevive ao restart);
//  4. o excedente (real > tecto) é cobrado até ao topo e ARMA a negação seguinte, em vez de
//     estourar o tecto em silêncio;
//  5. o eixo $ decide quando configurado, com a tarifa MEDIDA do próprio run.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// admissionFixture arma um orçamento por-run com o nó do run JÁ registado pelo seam real, e
// devolve a admissão pronta a usar. A libertação do nó fica no Cleanup — é o mesmo `defer`
// que [SecuredRuntime.Run] faz.
type admissionFixture struct {
	rb    *RunBudget
	adm   *ModelTurnAdmission
	runID string
}

func newAdmissionFixture(t *testing.T, runID string, maxTokens int64, opts ...ModelAdmissionOption) *admissionFixture {
	t.Helper()
	rb, err := NewRunBudget(maxTokens)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	return newAdmissionFixtureOver(t, rb, runID, opts...)
}

func newAdmissionFixtureOver(t *testing.T, rb *RunBudget, runID string, opts ...ModelAdmissionOption) *admissionFixture {
	t.Helper()
	adm, err := NewModelTurnAdmission(rb, opts...)
	if err != nil {
		t.Fatalf("NewModelTurnAdmission: %v", err)
	}
	release, err := rb.acquire(runID)
	if err != nil {
		t.Fatalf("acquire (o seam por-run de AOS-256): %v", err)
	}
	t.Cleanup(release)
	return &admissionFixture{rb: rb, adm: adm, runID: runID}
}

// pedido monta um [agentruntime.TurnAdmissionRequest] com um prompt materializado à medida.
func (f *admissionFixture) pedido(turn int, prompt string) agentruntime.TurnAdmissionRequest {
	return agentruntime.TurnAdmissionRequest{
		RunID:   f.runID,
		StepID:  stepIDDoTurno(turn),
		Turn:    turn,
		ModelID: "claude-opus-4-8",
		View:    agentruntime.PromptView{Turn: turn, Materialized: []byte(prompt)},
	}
}

// stepIDDoTurno reproduz a forma do step_id sequencial do kernel — o que interessa é que seja
// ESTÁVEL por turno, que é o que torna `run_id:step_id` uma chave de dedup.
func stepIDDoTurno(turn int) string {
	return "step-" + strconv.Itoa(turn)
}

func (f *admissionFixture) disponivel(t *testing.T) budget.Amount {
	t.Helper()
	amt, err := f.rb.tree.Available(f.runID)
	if err != nil {
		t.Fatalf("Available(%q): %v", f.runID, err)
	}
	return amt
}

// ---------------------------------------------------------------------------
// 1. Reserva antes, saldo pelo real
// ---------------------------------------------------------------------------

// TestAOS260_ReservaAEstimativaESaldaPeloReal é o critério 1 em números exactos, contra um
// GÉMEO INDEPENDENTE derivado à mão (e não pedido à função que está a ser testada).
//
// O prompt é `"ola mundo"`. A contagem por átomos de AOS-258, aplicada à mão: `ola` é uma
// sequência alfanumérica de 3 ⇒ ceil(3/4) = 1; o espaço não conta sozinho; `mundo` são 5 ⇒
// ceil(5/4) = 2. Total 3 átomos. O piso de bytes é 9/4 + 1 = 3. Máximo ⇒ **3 tokens**.
func TestAOS260_ReservaAEstimativaESaldaPeloReal(t *testing.T) {
	const (
		tecto     = 10_000
		provisao  = 100
		prompt    = "ola mundo"
		promptTok = 3 // gémeo independente: ver o cabeçalho
	)
	f := newAdmissionFixture(t, "run-saldo", tecto, WithOutputProvision(provisao))
	ctx := context.Background()

	if got := ModelPromptTokens(agentruntime.PromptView{Materialized: []byte(prompt)}); got != promptTok {
		t.Fatalf("ModelPromptTokens(%q) = %d, gemeo derivado a mao = %d", prompt, got, promptTok)
	}

	v, err := f.adm.AdmitTurn(ctx, f.pedido(1, prompt))
	if err != nil || !v.Admitted || v.AlreadyAdmitted {
		t.Fatalf("AdmitTurn: v=%+v err=%v", v, err)
	}
	// A RESERVA está feita ANTES da chamada: o headroom já desceu a estimativa inteira.
	if got, want := f.disponivel(t).Tokens, int64(tecto-(promptTok+provisao)); got != want {
		t.Fatalf("headroom apos a reserva = %d, want %d (tecto - prompt - provisao). Se for %d, a reserva nao aconteceu antes da chamada", got, want, int64(tecto))
	}

	// O consumo REAL é MUITO menor do que a provisão — o caso normal.
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1,
		Usage: agentruntime.Usage{InputTokens: 3, OutputTokens: 7}, CostMicroUSD: 250,
	}); err != nil {
		t.Fatalf("SettleTurn: %v", err)
	}
	// Depois do saldo, o que ficou debitado é EXACTAMENTE o real (10 tokens), não a
	// estimativa (103). É isto que impede a provisão de virar contabilidade.
	if got, want := f.disponivel(t).Tokens, int64(tecto-10); got != want {
		t.Fatalf("headroom apos o saldo = %d, want %d (tecto - consumo REAL). %d seria a provisao a ficar cobrada", got, want, int64(tecto-(promptTok+provisao)))
	}
	if got := f.disponivel(t).CostMicroUSD; got != UnlimitedCostMicroUSD-250 {
		t.Errorf("a dimensao $ tem de ser debitada pelo custo MEDIDO mesmo sem tecto em dolares (medir e diferente de decidir): got %d", UnlimitedCostMicroUSD-got)
	}
}

// TestAOS260_SaldoImpedeAProvisaoDeEsgotarOTecto é a consequência PRÁTICA do teste anterior, e
// a razão de o saldo existir: com um tecto que só comporta 3 provisões, um run de consumo real
// pequeno dá MUITO mais do que 3 turnos.
//
// O gémeo independente é o cenário CONTRAFACTUAL, calculado à mão: se o saldo confirmasse a
// estimativa (o que o [budget.BudgetCheck] das tool calls faz), 3 turnos esgotariam o tecto.
func TestAOS260_SaldoImpedeAProvisaoDeEsgotarOTecto(t *testing.T) {
	const (
		tecto    = 8000
		provisao = 1000        // abaixo de tecto/8 = 1000, logo é a provisão EM VIGOR
		prompt   = "ola mundo" // 3 tokens
		estimado = 1003        // 3 + 1000 — o que 1 turno RESERVA
		real     = 10          // o que 1 turno GASTA
	)
	// Contrafactual: com commit-da-estimativa cabiam floor(8000/1003) = 7 turnos.
	const turnosSeCommitDaEstimativa = tecto / estimado
	// Com saldo pelo real cabem os turnos que o consumo real permitir, com a folga da
	// provisão a ser sempre devolvida.
	const turnosEsperados = 20

	f := newAdmissionFixture(t, "run-provisao", tecto, WithOutputProvision(provisao))
	ctx := context.Background()

	admitidos := 0
	for turn := 1; turn <= turnosEsperados; turn++ {
		v, err := f.adm.AdmitTurn(ctx, f.pedido(turn, prompt))
		if err != nil {
			t.Fatalf("AdmitTurn(turno %d): %v", turn, err)
		}
		if !v.Admitted {
			break
		}
		admitidos++
		if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
			RunID: f.runID, StepID: stepIDDoTurno(turn), Turn: turn,
			Usage: agentruntime.Usage{InputTokens: 3, OutputTokens: 7},
		}); err != nil {
			t.Fatalf("SettleTurn(turno %d): %v", turn, err)
		}
	}
	if admitidos <= turnosSeCommitDaEstimativa {
		t.Fatalf("so %d turnos admitidos — e o que se obteria confirmando a ESTIMATIVA (%d). O saldo pelo real tem de devolver a provisao nao gasta", admitidos, turnosSeCommitDaEstimativa)
	}
	if admitidos != turnosEsperados {
		t.Errorf("esperava os %d turnos todos admitidos (10 x 10 tokens reais = 100, muito abaixo do tecto de %d); got %d", turnosEsperados, tecto, admitidos)
	}
	if got, want := f.disponivel(t).Tokens, int64(tecto-turnosEsperados*real); got != want {
		t.Errorf("consumo acumulado errado: headroom=%d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// 2. Esgotamento: negação atribuível, sem fuga de headroom
// ---------------------------------------------------------------------------

// TestAOS260_EsgotamentoNegaComRazaoENaoDeixaReservaPendente é o critério 2 do lado do
// adaptador. A negação NÃO é um erro (o loop tem de a poder tratar como degradação declarada),
// tem razão legível, e não deixa reserva pendente — uma reserva parcial numa negação roubaria
// headroom que ninguém devolveria.
func TestAOS260_EsgotamentoNegaComRazaoENaoDeixaReservaPendente(t *testing.T) {
	// Quem tem de não caber é o PROMPT — a provisão sozinha nunca nega (ver
	// [OutputProvisionFor]). 300 bytes de prompt dão ~100 tokens contra um tecto de 50.
	promptGrande := strings.Repeat("le o documento ", 20)
	f := newAdmissionFixture(t, "run-esgota", 50, WithOutputProvision(1000))
	ctx := context.Background()

	antes := f.disponivel(t)
	v, err := f.adm.AdmitTurn(ctx, f.pedido(1, promptGrande))
	if err != nil {
		t.Fatalf("a NEGACAO nao pode viajar como erro (negar nao e avariar): %v", err)
	}
	if v.Admitted {
		t.Fatalf("um prompt de ~%d tokens nao cabe num tecto de 50 — a admissao TEM de negar", ModelPromptTokens(agentruntime.PromptView{Materialized: []byte(promptGrande)}))
	}
	if !strings.Contains(v.Reason, "esgotado") || !strings.Contains(v.Reason, "Nenhuma chamada ao modelo foi feita") {
		t.Errorf("a razao tem de ser ATRIBUIVEL (dizer que foi o orcamento e que nada foi gasto): %q", v.Reason)
	}
	if got := f.disponivel(t); got != antes {
		t.Errorf("uma negacao nao pode consumir headroom: antes=%+v depois=%+v", antes, got)
	}
	// E a segunda consulta nega da mesma maneira, sem efeitos colaterais (o loop pára na
	// primeira, mas o adaptador não pode depender disso).
	if v2, _ := f.adm.AdmitTurn(ctx, f.pedido(2, promptGrande)); v2.Admitted {
		t.Error("a segunda consulta tambem tem de negar")
	}
}

// TestAOS260_ProvisaoNuncaTornaOTectoInatingivel sela a regra de [OutputProvisionFor], que
// existe para fechar um modo de falha que uma constante fixa cria sozinha: com um tecto MENOR
// do que a provisão, o turno 1 seria negado sem o run ter gasto nada — «orçamento esgotado»
// num run que nunca correu.
//
// A propriedade, e não só a fórmula: para QUALQUER tecto, a provisão em vigor deixa sempre
// espaço para o prompt; quem nega é o prompt, que é uma verdade sobre o run.
func TestAOS260_ProvisaoNuncaTornaOTectoInatingivel(t *testing.T) {
	t.Parallel()
	for _, tecto := range []int64{1, 10, 100, 1000, 10_000, 1_000_000} {
		got := OutputProvisionFor(DefaultOutputProvisionTokens, tecto)
		if got >= tecto && tecto > 1 {
			t.Errorf("tecto=%d: provisao em vigor %d nao pode consumir o tecto inteiro", tecto, got)
		}
		if got < 0 {
			t.Errorf("tecto=%d: provisao negativa (%d)", tecto, got)
		}
	}
	// Num tecto realista a provisão é a configurada — o limite só morde em tectos pequenos.
	if got := OutputProvisionFor(DefaultOutputProvisionTokens, 200_000); got != DefaultOutputProvisionTokens {
		t.Errorf("num tecto de 200k a provisao devia ser a configurada (%d), got %d", DefaultOutputProvisionTokens, got)
	}
	// Provisão 0 continua a ser 0 — o modo declaradamente fail-open, não um valor a corrigir.
	if got := OutputProvisionFor(0, 200_000); got != 0 {
		t.Errorf("provisao 0 tem de continuar 0 (fail-open declarado), got %d", got)
	}
	// E a admissão REAL num tecto minúsculo admite, desde que o prompt caiba.
	f := newAdmissionFixture(t, "run-tecto-minusculo", 200)
	v, err := f.adm.AdmitTurn(context.Background(), f.pedido(1, "ola mundo"))
	if err != nil || !v.Admitted {
		t.Fatalf("com tecto de 200 e um prompt de 3 tokens o turno TEM de ser admitido (senao a provisao fixa tornou o tecto inatingivel): v=%+v err=%v", v, err)
	}
}

// TestAOS260_FalhaDoModeloLibertaAProvisao: sem isto um provider intermitente esgotava o tecto
// com consumo que nunca existiu.
func TestAOS260_FalhaDoModeloLibertaAProvisao(t *testing.T) {
	f := newAdmissionFixture(t, "run-falha", 10_000, WithOutputProvision(100))
	ctx := context.Background()

	antes := f.disponivel(t)
	if _, err := f.adm.AdmitTurn(ctx, f.pedido(1, "ola mundo")); err != nil {
		t.Fatalf("AdmitTurn: %v", err)
	}
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1, Failed: true,
	}); err != nil {
		t.Fatalf("SettleTurn(Failed): %v", err)
	}
	if got := f.disponivel(t); got != antes {
		t.Errorf("a provisao de um turno que FALHOU tem de voltar inteira: antes=%+v depois=%+v", antes, got)
	}
}

// ---------------------------------------------------------------------------
// 3. Replay não re-reserva (critério 3)
// ---------------------------------------------------------------------------

// TestAOS260_ReplayNaoReReserva_MesmoProcesso: a dedup por `run_id:step_id`. Uma segunda
// admissão do MESMO passo é admitida sem reservar nada — o molde do `already-applied` do
// step-ledger.
func TestAOS260_ReplayNaoReReserva_MesmoProcesso(t *testing.T) {
	f := newAdmissionFixture(t, "run-dedup", 10_000, WithOutputProvision(100))
	ctx := context.Background()

	if _, err := f.adm.AdmitTurn(ctx, f.pedido(1, "ola mundo")); err != nil {
		t.Fatalf("AdmitTurn: %v", err)
	}
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1,
		Usage: agentruntime.Usage{InputTokens: 30, OutputTokens: 20},
	}); err != nil {
		t.Fatalf("SettleTurn: %v", err)
	}
	depoisDoPrimeiro := f.disponivel(t)

	// MESMO run_id:step_id outra vez.
	v, err := f.adm.AdmitTurn(ctx, f.pedido(1, "ola mundo"))
	if err != nil {
		t.Fatalf("AdmitTurn (repetido): %v", err)
	}
	if !v.Admitted || !v.AlreadyAdmitted {
		t.Fatalf("um passo JA admitido tem de voltar admitido e marcado AlreadyAdmitted: %+v", v)
	}
	if got := f.disponivel(t); got != depoisDoPrimeiro {
		t.Errorf("o mesmo run_id:step_id nao pode reservar duas vezes: %+v -> %+v", depoisDoPrimeiro, got)
	}
	// E o saldo do passo repetido é no-op: não há reserva pendente que ele possa libertar.
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1,
		Usage: agentruntime.Usage{InputTokens: 30, OutputTokens: 20},
	}); err != nil {
		t.Fatalf("SettleTurn (repetido): %v", err)
	}
	if got := f.disponivel(t); got != depoisDoPrimeiro {
		t.Errorf("um saldo sem reserva pendente tem de ser no-op (senao saldava a reserva de OUTRO turno): %+v -> %+v", depoisDoPrimeiro, got)
	}
}

// TestAOS260_ReplayNaoReReserva_IncarnacaoNova é o caso que a decisão do dono nomeia e o
// único que importa de verdade: a RETOMA. O processo é outro (árvore nova, adaptador novo,
// mapa de dedup VAZIO), os turnos 1..2 são reproduzidos da captura e o turno 3 é novo.
//
// Sem o detector, os turnos reproduzidos reservariam outra vez e o run seria cobrado DUAS
// vezes pelo mesmo turno. A prova está no headroom: só o turno 3 desconta.
func TestAOS260_ReplayNaoReReserva_IncarnacaoNova(t *testing.T) {
	const provisao = 100
	reproduzidos := map[int]bool{1: true, 2: true}
	detector := func(_ context.Context, _, _ string, turn int) bool { return reproduzidos[turn] }

	f := newAdmissionFixture(t, "run-retoma", 10_000, WithOutputProvision(provisao), WithReplayDetector(detector))
	ctx := context.Background()
	tecto := f.disponivel(t)

	for turn := 1; turn <= 2; turn++ {
		v, err := f.adm.AdmitTurn(ctx, f.pedido(turn, "ola mundo"))
		if err != nil {
			t.Fatalf("AdmitTurn(reproduzido %d): %v", turn, err)
		}
		if !v.Admitted || !v.AlreadyAdmitted {
			t.Fatalf("um turno REPRODUZIDO tem de ser admitido SEM reserva: %+v", v)
		}
	}
	if got := f.disponivel(t); got != tecto {
		t.Fatalf("os turnos reproduzidos nao podem tocar no headroom (o consumo original ja foi cobrado na incarnacao que os produziu): %+v -> %+v", tecto, got)
	}

	// O turno NOVO (fora do plano) reserva normalmente — sem isto o detector estaria a
	// desligar a admissão inteira, que é o modo de falha simétrico.
	v, err := f.adm.AdmitTurn(ctx, f.pedido(3, "ola mundo"))
	if err != nil || !v.Admitted || v.AlreadyAdmitted {
		t.Fatalf("o turno NOVO da retoma tem de ser admitido COM reserva: v=%+v err=%v", v, err)
	}
	if got, want := f.disponivel(t).Tokens, tecto.Tokens-(3+provisao); got != want {
		t.Errorf("o turno novo devia reservar (prompt 3 + provisao %d): got %d, want %d", provisao, got, want)
	}
}

// ---------------------------------------------------------------------------
// 4. Excedente: cobrado até ao topo, e a negação seguinte é atribuível
// ---------------------------------------------------------------------------

// TestAOS260_ExcedenteCobraAteAoTectoEArmaANegacaoSeguinte: um turno que gasta MUITO mais do
// que a provisão. O tecto não pode ser estourado nos contadores nem o excedente pode ser
// ignorado — cobra-se até ao topo e o run deixa de receber turnos, com razão própria.
func TestAOS260_ExcedenteCobraAteAoTectoEArmaANegacaoSeguinte(t *testing.T) {
	const tecto = 500
	f := newAdmissionFixture(t, "run-excedente", tecto, WithOutputProvision(50))
	ctx := context.Background()

	if _, err := f.adm.AdmitTurn(ctx, f.pedido(1, "ola mundo")); err != nil {
		t.Fatalf("AdmitTurn: %v", err)
	}
	// O modelo devolveu uma resposta gigante: 9 000 tokens contra uma provisão de 50.
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1,
		Usage: agentruntime.Usage{InputTokens: 3, OutputTokens: 8997},
	}); err != nil {
		t.Fatalf("SettleTurn: %v", err)
	}
	if got := f.disponivel(t).Tokens; got != 0 {
		t.Fatalf("o excedente tem de cobrar ate ao topo (headroom 0, run a 100%%), nunca deixar headroom nem ir a negativo: got %d", got)
	}

	v, err := f.adm.AdmitTurn(ctx, f.pedido(2, "ola mundo"))
	if err != nil {
		t.Fatalf("AdmitTurn(2): %v", err)
	}
	if v.Admitted {
		t.Fatal("depois de um excedente o run NAO pode receber mais turnos de modelo")
	}
	if !strings.Contains(v.Reason, "ULTRAPASSADO") {
		t.Errorf("a razao tem de distinguir o EXCEDENTE do esgotamento normal (foi o consumo real que passou o tecto, nao a estimativa que nao coube): %q", v.Reason)
	}
}

// ---------------------------------------------------------------------------
// 5. Eixo $ — decide quando configurado, com a tarifa MEDIDA
// ---------------------------------------------------------------------------

// TestAOS260_TectoEmDolaresDecideComATarifaMedida: com [WithMaxCostMicroUSDPerRun] a dimensão
// $ passa a negar. O primeiro turno é admitido só por tokens (ainda não há medição — a
// limitação declarada); a partir do segundo, a projecção usa a tarifa OBSERVADA do run.
func TestAOS260_TectoEmDolaresDecideComATarifaMedida(t *testing.T) {
	rb, err := NewRunBudget(1_000_000, WithMaxCostMicroUSDPerRun(10_000))
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	if got, ok := rb.MaxCostMicroUSDPerRun(); !ok || got != 10_000 {
		t.Fatalf("MaxCostMicroUSDPerRun = (%d,%v)", got, ok)
	}
	f := newAdmissionFixtureOver(t, rb, "run-dolares", WithOutputProvision(100))
	ctx := context.Background()

	// Turno 1: sem medição, a projecção é 0 ⇒ decide só por tokens.
	if v, err := f.adm.AdmitTurn(ctx, f.pedido(1, "ola mundo")); err != nil || !v.Admitted {
		t.Fatalf("turno 1: v=%+v err=%v", v, err)
	}
	// E custou caro: 9 000 micro-USD por 100 tokens ⇒ tarifa medida de 90 micro-USD/token.
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1,
		Usage: agentruntime.Usage{InputTokens: 50, OutputTokens: 50}, CostMicroUSD: 9_000,
	}); err != nil {
		t.Fatalf("SettleTurn: %v", err)
	}
	if got := f.disponivel(t).CostMicroUSD; got != 1_000 {
		t.Fatalf("o custo MEDIDO tem de ser debitado no tecto em $: headroom=%d, want 1000", got)
	}

	// Turno 2: a projecção (103 tokens x 90 micro-USD/token ≈ 9 270) não cabe nos 1 000
	// micro-USD que restam — e é a dimensão $ que nega, com os tokens de sobra.
	v, err := f.adm.AdmitTurn(ctx, f.pedido(2, "ola mundo"))
	if err != nil {
		t.Fatalf("AdmitTurn(2): %v", err)
	}
	if v.Admitted {
		t.Fatal("o tecto em DOLARES tem de negar mesmo com tokens de sobra (uma reserva so cabe se couber nas DUAS dimensoes)")
	}
	// A RAZÃO é a única coisa que o operador vê quando o run suspende, e é ela que o nó propaga
	// para o registo durável de exaustão (AOS-263). Tem de trazer os DOIS pares e nomear os
	// micro-USD — senão a pergunta humana fala de tokens sobre um run que os tokens não pararam.
	if !strings.Contains(v.Reason, "micro-USD") {
		t.Errorf("a razao tem de nomear a dimensao que negou: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "9000 micro-USD REAIS") {
		t.Errorf("a razao tem de trazer o CONSUMIDO em micro-USD medido (9000), senao a decisao humana nao tem numeros da grandeza certa: %q", v.Reason)
	}
	// E os TOKENS têm folga enorme — é isso que torna uma leitura só-tokens enganadora, e é
	// exactamente o cenário que o nó tinha de conseguir reportar.
	folga := f.disponivel(t)
	if folga.Tokens*2 < 1_000_000 {
		t.Fatalf("o cenario exige folga GRANDE em tokens (restam %d de 1000000) — senao nao prova que foi a dimensao $ a decidir", folga.Tokens)
	}
	if folga.CostMicroUSD >= 10_000 {
		t.Fatalf("a dimensao $ tinha de estar quase esgotada (restam %d de 10000)", folga.CostMicroUSD)
	}
}

// TestAOS260_ProjeccaoDeCustoEInteiraEConservadora sela a aritmética da projecção contra um
// gémeo derivado à mão, incluindo o arredondamento (para CIMA, a direcção fail-closed) e o
// caso sem medição.
func TestAOS260_ProjeccaoDeCustoEInteiraEConservadora(t *testing.T) {
	casos := []struct {
		nome                       string
		tokens, tokensReais, custo int64
		want                       int64
	}{
		{"sem medicao ainda", 100, 0, 0, 0},
		{"custo medido nulo", 100, 500, 0, 0},
		{"tarifa exacta", 100, 10, 1000, 10_000}, // 100 tokens x 100 micro-USD/token
		{"arredonda para CIMA", 3, 7, 10, 5},     // 3*10/7 = 4,28... ⇒ 5
		{"tokens zero", 0, 10, 1000, 0},
	}
	for _, c := range casos {
		if got := projectCost(c.tokens, c.tokensReais, c.custo); got != c.want {
			t.Errorf("%s: projectCost(%d,%d,%d) = %d, want %d", c.nome, c.tokens, c.tokensReais, c.custo, got, c.want)
		}
	}
}

// TestAOS260_UsageAZeroNaoContaminaATarifaMedida fecha a fuga FAIL-OPEN do eixo $: um provider
// que não ecoa `usage` faz cobrar a PROVISÃO (o único número honesto para o tecto), mas essa
// estimativa NÃO pode entrar no medidor — se entrasse, diluiria a tarifa observada e a projecção
// do turno seguinte ficaria abaixo do custo real, admitindo turnos que o tecto devia negar.
//
// O gémeo é derivado à mão: só o turno 2 é medido (800 tokens / 6400 micro-USD ⇒ tarifa 8), pelo
// que a projecção de 100 tokens tem de ser 800 e não 320 (que é o que a tarifa diluída daria:
// 6400/(1200+800) = 3,2).
func TestAOS260_UsageAZeroNaoContaminaATarifaMedida(t *testing.T) {
	f := newAdmissionFixture(t, "run-sem-usage", 1_000_000, WithOutputProvision(1000))
	ctx := context.Background()

	// Turno 1: provider CEGO (usage e custo a zero). A provisão fica cobrada.
	if _, err := f.adm.AdmitTurn(ctx, f.pedido(1, "ola mundo")); err != nil {
		t.Fatalf("AdmitTurn(1): %v", err)
	}
	antes := f.disponivel(t).Tokens
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(1), Turn: 1,
	}); err != nil {
		t.Fatalf("SettleTurn(1): %v", err)
	}
	if got := f.disponivel(t).Tokens; got != antes {
		t.Errorf("sem usage a PROVISAO fica cobrada (o tecto tem de descer): headroom %d -> %d", antes, got)
	}

	// Turno 2: medição real.
	if _, err := f.adm.AdmitTurn(ctx, f.pedido(2, "ola mundo")); err != nil {
		t.Fatalf("AdmitTurn(2): %v", err)
	}
	if err := f.adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: f.runID, StepID: stepIDDoTurno(2), Turn: 2,
		Usage: agentruntime.Usage{InputTokens: 300, OutputTokens: 500}, CostMicroUSD: 6400,
	}); err != nil {
		t.Fatalf("SettleTurn(2): %v", err)
	}

	f.adm.mu.Lock()
	m := f.adm.runs[f.runID]
	tokensMedidos, custoMedido := m.tokens, m.cost
	f.adm.mu.Unlock()

	if tokensMedidos != 800 || custoMedido != 6400 {
		t.Fatalf("o medidor tem de conter SO os turnos com medicao REAL (800 tokens / 6400 micro-USD): got %d / %d. Se os tokens forem 2000+, a ESTIMATIVA do turno cego entrou no medidor e a tarifa esta diluida", tokensMedidos, custoMedido)
	}
	if got, want := projectCost(100, tokensMedidos, custoMedido), int64(800); got != want {
		t.Errorf("projeccao com a tarifa MEDIDA (8 micro-USD/token) = %d, want %d (320 seria a tarifa diluida de 3,2 — subestimar 2,5x e fail-open na dimensao que AOS-260 veio fechar)", got, want)
	}
}

// TestAOS260_PodaDeRunNaoTocaEmRunsCujoIdPartilhaOPrefixo é a prova do âmbito da poda. Com a
// chave concatenada `run_id:step_id` e uma busca por prefixo, o fim do run `t1` libertava as
// provisões VIVAS de `t1:job` — e o saldo desses turnos passava a ser um no-op silencioso, com o
// consumo real a nunca ser debitado. O `run_id` vem do corpo do pedido sem validação de forma,
// logo isto é alcançável por quem submete, e acontece sem adversário nenhum em qualquer
// convenção com `:` (`tenant:run-123`).
func TestAOS260_PodaDeRunNaoTocaEmRunsCujoIdPartilhaOPrefixo(t *testing.T) {
	const (
		curto = "t1"
		longo = "t1:job" // partilha o prefixo "t1:" da chave concatenada
	)
	rb, err := NewRunBudget(100_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	adm, err := NewModelTurnAdmission(rb, WithOutputProvision(100))
	if err != nil {
		t.Fatalf("NewModelTurnAdmission: %v", err)
	}
	libertaCurto, err := rb.acquire(curto)
	if err != nil {
		t.Fatalf("acquire(%q): %v", curto, err)
	}
	libertaLongo, err := rb.acquire(longo)
	if err != nil {
		t.Fatalf("acquire(%q): %v", longo, err)
	}
	defer libertaLongo()

	ctx := context.Background()
	pedido := func(runID string, turn int) agentruntime.TurnAdmissionRequest {
		return agentruntime.TurnAdmissionRequest{RunID: runID, StepID: stepIDDoTurno(turn), Turn: turn,
			View: agentruntime.PromptView{Materialized: []byte("ola mundo")}}
	}
	for _, runID := range []string{curto, longo} {
		if _, err := adm.AdmitTurn(ctx, pedido(runID, 1)); err != nil {
			t.Fatalf("AdmitTurn(%q): %v", runID, err)
		}
	}

	libertaCurto() // fim do run CURTO ⇒ forgetRun("t1")

	adm.mu.Lock()
	_, vivaDoLongo := adm.pending[admissionKey{runID: longo, stepID: stepIDDoTurno(1)}]
	restantes := len(adm.pending)
	adm.mu.Unlock()
	if !vivaDoLongo {
		t.Fatalf("a poda do run %q levou a provisao VIVA do run %q: o saldo desse turno passaria a no-op e o consumo real nunca seria debitado (pendentes restantes: %d)", curto, longo, restantes)
	}

	// E o saldo do run longo continua a debitar o consumo REAL.
	antes, ok := rb.AvailableTokens(longo)
	if !ok {
		t.Fatalf("o run %q perdeu o no de orcamento", longo)
	}
	if err := adm.SettleTurn(ctx, agentruntime.TurnSettlement{
		RunID: longo, StepID: stepIDDoTurno(1), Turn: 1,
		Usage: agentruntime.Usage{InputTokens: 40, OutputTokens: 60},
	}); err != nil {
		t.Fatalf("SettleTurn(%q): %v", longo, err)
	}
	depois, _ := rb.AvailableTokens(longo)
	// A provisão (3+100) é libertada e o real (100) cobrado ⇒ o headroom SOBE face ao reservado.
	if antes+103-100 != depois {
		t.Errorf("o saldo do run %q tem de trocar a provisao pelo real: headroom %d -> %d (esperado %d)", longo, antes, depois, antes+3)
	}
}

// TestAOS260_ProjeccaoNuncaProduzQuantiaInvalida sela a aritmética da projecção nos BORDOS, contra
// um GÉMEO INDEPENDENTE em [big.Int] — precisão arbitrária, nada partilhado com a implementação.
//
// O defeito que fecha é concreto: o arredondamento idiomático `(p + d - 1) / d` transborda para
// NEGATIVO quando o produto se aproxima de [math.MaxInt64], e um `CostMicroUSD` negativo faz
// [budget.Budget.Reserve] devolver [budget.ErrInvalidAmount] — que ANTES subia como erro da porta
// e abortava o run como `failed`, accionando a saga de compensação de AOS-254 sobre efeitos
// legítimos. Uma quantia inválida é um defeito de cálculo, não perda de visibilidade do tecto.
func TestAOS260_ProjeccaoNuncaProduzQuantiaInvalida(t *testing.T) {
	t.Parallel()
	casos := []struct{ tokens, tokensReais, custoReal int64 }{
		{100, 10, 1000},
		{3, 7, 10},
		// O bordo: produto imediatamente abaixo de MaxInt64, com um divisor grande — é aqui que
		// somar `tokensReais-1` ao produto passava a negativo.
		{2, math.MaxInt64 / 4, math.MaxInt64 / 2},
		{math.MaxInt64 / 3, 3, 2},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64},
	}
	for _, c := range casos {
		got := projectCost(c.tokens, c.tokensReais, c.custoReal)
		if got < 0 {
			t.Errorf("projectCost(%d,%d,%d) = %d — uma quantia NEGATIVA e recusada por budget.Reserve", c.tokens, c.tokensReais, c.custoReal, got)
			continue
		}
		// GÉMEO: ceil(tokens*custoReal/tokensReais) em precisão arbitrária, saturado a MaxInt64.
		p := new(big.Int).Mul(big.NewInt(c.tokens), big.NewInt(c.custoReal))
		q, r := new(big.Int).QuoRem(p, big.NewInt(c.tokensReais), new(big.Int))
		if r.Sign() != 0 {
			q.Add(q, big.NewInt(1))
		}
		topo := big.NewInt(math.MaxInt64)
		if q.Cmp(topo) > 0 {
			q = topo
		}
		// A implementação pode SOBRESTIMAR (o ramo do produto inseguro aproxima pela tarifa já
		// arredondada) — sobrestimar é a direcção fail-closed. Subestimar é que não pode.
		if big.NewInt(got).Cmp(q) < 0 {
			t.Errorf("projectCost(%d,%d,%d) = %d SUBESTIMA o gemeo exacto %s — a projeccao tem de arredondar para CIMA", c.tokens, c.tokensReais, c.custoReal, got, q)
		}
	}
}

// ---------------------------------------------------------------------------
// Ciclo de vida e composição
// ---------------------------------------------------------------------------

// TestAOS260_EstadoPorRunEPodadoNoSeamDeAOS256: o estado por-run (dedup, tarifa, latch) é
// libertado quando o nó de orçamento do run é removido — e é-o pelo seam que JÁ existe, não
// por alguém se lembrar de chamar um `Forget`.
func TestAOS260_EstadoPorRunEPodadoNoSeamDeAOS256(t *testing.T) {
	rb, err := NewRunBudget(10_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	adm, err := NewModelTurnAdmission(rb, WithOutputProvision(10))
	if err != nil {
		t.Fatalf("NewModelTurnAdmission: %v", err)
	}
	release, err := rb.acquire("run-poda")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	req := agentruntime.TurnAdmissionRequest{RunID: "run-poda", StepID: "s1", Turn: 1,
		View: agentruntime.PromptView{Materialized: []byte("ola mundo")}}
	if _, err := adm.AdmitTurn(context.Background(), req); err != nil {
		t.Fatalf("AdmitTurn: %v", err)
	}

	adm.mu.Lock()
	temEstado := len(adm.runs) == 1 && len(adm.pending) == 1
	adm.mu.Unlock()
	if !temEstado {
		t.Fatalf("estado por-run em falta: runs=%d pending=%d", len(adm.runs), len(adm.pending))
	}

	release() // o MESMO `defer` de [SecuredRuntime.Run]

	adm.mu.Lock()
	defer adm.mu.Unlock()
	if len(adm.runs) != 0 || len(adm.pending) != 0 {
		t.Errorf("o estado por-run tem de ser podado com o no de orcamento (senao e uma fuga num processo de vida longa): runs=%d pending=%d", len(adm.runs), len(adm.pending))
	}
}

// TestAOS260_ComposicaoFailClosed: sem orçamento não há admissão — e a construção RECUSA, em
// vez de devolver um adaptador que admite tudo (a capacidade-fantasma que o banner do nó
// existe para evitar).
func TestAOS260_ComposicaoFailClosed(t *testing.T) {
	t.Parallel()
	if _, err := NewModelTurnAdmission(nil); err == nil {
		t.Error("NewModelTurnAdmission(nil) tem de recusar")
	}
	rb, err := NewRunBudget(100)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	if _, err := NewModelTurnAdmission(rb, WithOutputProvision(-1)); err == nil {
		t.Error("uma provisao negativa tem de recusar")
	}
	if _, err := NewRunBudget(100, WithMaxCostMicroUSDPerRun(0)); err == nil {
		t.Error("um tecto em $ de 0 tem de recusar — nao desliga o tecto, negaria todo o turno com custo")
	}
}

// TestAOS260_RaizIlimitadaNasDuasDimensoes é o guarda da bomba que AOS-260 desarmou: enquanto
// nada reservava custo, a raiz da árvore podia ter a dimensão $ a ZERO. A partir do momento em
// que o turno de modelo passou a debitar dólares, uma raiz a zero negaria a árvore INTEIRA —
// em todos os runs, com uma razão que parece falta de orçamento.
func TestAOS260_RaizIlimitadaNasDuasDimensoes(t *testing.T) {
	t.Parallel()
	rb, err := NewRunBudget(10_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	raiz, err := rb.tree.Available(BudgetTreeID)
	if err != nil {
		t.Fatalf("Available(raiz): %v", err)
	}
	if raiz.CostMicroUSD <= 0 {
		t.Fatalf("a raiz tem de ser ilimitada TAMBEM em micro-USD (a admissao do turno de modelo reserva custo e a reserva sobe a cadeia inteira ate a raiz): got %d", raiz.CostMicroUSD)
	}
	if raiz.Tokens <= 0 {
		t.Fatalf("a raiz tem de continuar ilimitada em tokens: got %d", raiz.Tokens)
	}
}

// TestAOS260_SemFloatNoCaminhoDoDinheiro: guarda AST. A aritmética de tokens/micro-USD é
// INTEIRA por decisão de desenho (ADR-008) — um float64 que entrasse aqui traria arredondamento
// não-determinístico para o caminho que decide se um run continua a gastar.
func TestAOS260_SemFloatNoCaminhoDoDinheiro(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model_admission.go", nil, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == "float64" || id.Name == "float32" {
			t.Errorf("%s: `%s` no caminho do dinheiro — tokens e micro-USD sao INTEIROS (ADR-008)", fset.Position(id.Pos()), id.Name)
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// A prova COMPOSTA: a porta ligada à cadeia real, através de SecuredRuntime.Run
// ---------------------------------------------------------------------------

// TestAOS260_AdmissaoLigadaACadeiaReal prova duas coisas que nenhum teste de unidade prova: que a
// porta entra pelo caminho de composição REAL ([SecuredConfig.RuntimeOptions] →
// [agentruntime.WithModelAdmission]) e que o que fica debitado ao fim de um run inteiro é o
// consumo MEDIDO dos turnos, não as provisões.
//
// O QUE ESTE TESTE NÃO PROVA, e onde a prova vive: a ORDEM entre o seam que regista o nó de
// orçamento e a primeira admissão. O harness é montado com `maxTokens = 0` (⇒
// `SecuredConfig.Budget` nil, o seam não regista nada NESTA árvore) e o nó é registado aqui, à
// mão, com `rb.acquire` antes do `Run` — pelo que uma inversão da ordem em [SecuredRuntime.Run]
// deixaria este teste VERDE. A propriedade da ordem é selada onde o `runBudget` é de facto
// partilhado entre o seam e a admissão: nos testes de nó (`cmd/aos`, aos258_*/aos260_*), que é
// também onde um erro de composição apareceria como [ErrModelAdmissionBudgetNode].
func TestAOS260_AdmissaoLigadaACadeiaReal(t *testing.T) {
	rb, err := NewRunBudget(50_000)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	adm, err := NewModelTurnAdmission(rb, WithOutputProvision(100))
	if err != nil {
		t.Fatalf("NewModelTurnAdmission: %v", err)
	}
	h := newBudgetHarnessWith(t, "run-admissao-real", 0, // maxTokens 0 ⇒ o harness não cria árvore própria
		[]agentruntime.Option{agentruntime.WithModelAdmission(adm)}, []bhCall{{"doc", "notes"}})
	// O harness não compôs orçamento (Budget nil ⇒ stub neutro no hook de tool calls), mas a
	// admissão do turno de modelo tem o SEU orçamento: é a árvore acima. O nó por-run tem de
	// ser registado para ela — o mesmo `acquire` que o seam faz.
	release, err := rb.acquire(h.goal.RunID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()

	antes, _ := rb.AvailableTokens(h.goal.RunID)
	if _, _, err := h.sec.Run(context.Background(), h.goal, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *h.execCount != 1 {
		t.Fatalf("a tool devia ter executado 1x (a admissao do turno de modelo nao pode partir a cadeia): execCount=%d", *h.execCount)
	}
	depois, _ := rb.AvailableTokens(h.goal.RunID)
	// Dois turnos de modelo (o da tool call e o final), com usage 10+5 e 6+3 = 24 tokens
	// REAIS. É o consumo medido que fica debitado, não as duas provisões de 100.
	if got := antes - depois; got != 24 {
		t.Errorf("o consumo debitado tem de ser o MEDIDO dos dois turnos (10+5 e 6+3 = 24): got %d", got)
	}
}
