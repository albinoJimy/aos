package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
)

// ---------------------------------------------------------------------------
// O TECTO POR-RUN DEIXA DE RECOMEÇAR A CADA HOSPEDAGEM.
//
// A árvore de orçamento vive em memória e o nó do run nascia, a cada hospedagem, com o tecto
// INTEIRO — pelo que um run em ciclo de escalada/retoma podia gastar N × tecto. Escalar e retomar
// é o fluxo NORMAL de tudo o que precisa de aprovação humana, não um caso exótico: um run
// exercido em produção a 2026-08-19 passou por TRÊS incarnações.
//
// O comentário de `acquire` rejeitava corrigi-lo RETENDO o nó — e com razão. Mas nomeava o
// remédio: «estado de orçamento DURÁVEL por run». Esse estado já existia (o ledger de turnos que o
// burn-down lê, chaveado por `run_id`); o enforcement é que não o consultava.
// ---------------------------------------------------------------------------

func orcamentoDeTeste(t *testing.T, tecto int64, opts ...RunBudgetOption) *RunBudget {
	t.Helper()
	rb, err := NewRunBudget(tecto, opts...)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	return rb
}

// limiteDoNo lê o limite com que o nó do run ficou registado.
func limiteDoNo(t *testing.T, rb *RunBudget, runID string) budget.Amount {
	t.Helper()
	// `Available` de um no sem reservas == o limite com que nasceu, que e o que estes testes medem.
	rem, err := rb.tree.Available(runID)
	if err != nil {
		t.Fatalf("Available(%q): %v", runID, err)
	}
	return rem
}

// TestRetomaNaoRecomecaOOrcamento é o teste central: a segunda hospedagem nasce com o tecto
// MENOS o que já foi consumido.
func TestRetomaNaoRecomecaOOrcamento(t *testing.T) {
	consumido := budget.Amount{}
	rb := orcamentoDeTeste(t, 1000, WithConsumoDuravel(
		func(context.Context, string) (budget.Amount, error) { return consumido, nil },
	))

	// 1.ª incarnação: nada consumido ainda.
	rel, err := rb.acquire(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("1.a aquisicao: %v", err)
	}
	if got := limiteDoNo(t, rb, "run-1").Tokens; got != 1000 {
		t.Fatalf("1.a incarnacao nasceu com %d tokens, quero 1000", got)
	}
	rel()

	// Entre incarnações, o run gastou 700 — e isso ficou no ledger durável.
	consumido = budget.Amount{Tokens: 700}

	rel2, err := rb.acquire(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("2.a aquisicao: %v", err)
	}
	defer rel2()
	got := limiteDoNo(t, rb, "run-1").Tokens
	if got != 300 {
		t.Errorf("a 2.a incarnacao nasceu com %d tokens, quero 300 (1000-700) — o tecto por-run "+
			"recomecou e o run pode gastar N x tecto", got)
	}
}

// TestSemFonteDuravelOComportamentoEODeAntes é o CONTROLO que impede a correcção de mudar o mundo
// de quem não a liga.
//
// Sem fonte injectada, cada hospedagem continua a nascer com o tecto inteiro — exactamente como
// antes. Sem este ramo, o teste acima passaria mesmo que a semeadura estivesse a aplicar-se a
// todos os casos, incluindo os que não a pediram.
func TestSemFonteDuravelOComportamentoEODeAntes(t *testing.T) {
	rb := orcamentoDeTeste(t, 1000)
	rel, err := rb.acquire(context.Background(), "run-2")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if got := limiteDoNo(t, rb, "run-2").Tokens; got != 1000 {
		t.Errorf("sem fonte duravel o no nasceu com %d, quero o tecto inteiro (1000)", got)
	}
}

// TestConsumoAcimaDoTectoNasceAZero — gastar mais do que o tecto não produz um limite NEGATIVO.
//
// `AddNode` recusa limites negativos (`ErrInvalidLimit`), pelo que um saldo negativo faria a
// hospedagem FALHAR em vez de correr sem orçamento. Zero é o veredicto certo: o orçamento acabou,
// e quem trata disso é o prompt de exaustão — que suspende o run em vez de o matar.
func TestConsumoAcimaDoTectoNasceAZero(t *testing.T) {
	rb := orcamentoDeTeste(t, 1000, WithConsumoDuravel(
		func(context.Context, string) (budget.Amount, error) {
			return budget.Amount{Tokens: 5000}, nil
		},
	))
	rel, err := rb.acquire(context.Background(), "run-3")
	if err != nil {
		t.Fatalf("consumo acima do tecto NAO devia impedir a hospedagem: %v", err)
	}
	defer rel()
	if got := limiteDoNo(t, rb, "run-3").Tokens; got != 0 {
		t.Errorf("nasceu com %d tokens, quero 0", got)
	}
}

// TestLedgerIlegivelDegradaEMDECLARA — a degradação existe, e não é silenciosa.
//
// Recusar hospedar transformaria um soluço transitório do Event Store num run encravado, e o
// orçamento é controlo de CUSTO, não de segurança. Mas degradar em SILÊNCIO era como a fuga vivia:
// a linha tem de sair, e tem de dizer o que se perdeu.
func TestLedgerIlegivelDegradaEMDECLARA(t *testing.T) {
	var linhas []string
	rb := orcamentoDeTeste(t, 1000,
		WithConsumoDuravel(func(context.Context, string) (budget.Amount, error) {
			return budget.Amount{}, errors.New("event store em baixo")
		}),
		WithBudgetLogger(func(f string, a ...any) { linhas = append(linhas, f) }),
	)
	rel, err := rb.acquire(context.Background(), "run-4")
	if err != nil {
		t.Fatalf("um ledger ilegivel NAO devia impedir a hospedagem: %v", err)
	}
	defer rel()

	if got := limiteDoNo(t, rb, "run-4").Tokens; got != 1000 {
		t.Errorf("com o ledger ilegivel o no devia nascer com o tecto INTEIRO, veio %d", got)
	}
	if len(linhas) == 0 {
		t.Fatal("a degradacao foi SILENCIOSA — e assim que a fuga vivia")
	}
	junto := strings.Join(linhas, "\n")
	for _, exigido := range []string{"tecto INTEIRO", "Degradacao declarada"} {
		if !strings.Contains(junto, exigido) {
			t.Errorf("a linha nao diz o que se perdeu (falta %q): %s", exigido, junto)
		}
	}
}

// TestSegundaHospedagemCONCORRENTENaoRecriaONo fixa a semântica de reentrância que já existia.
//
// Duas hospedagens vivas do MESMO run partilham o nó (o contador `live`). A semeadura só acontece
// na primeira: recalcular a meio mudaria o limite debaixo dos pés de quem já está a reservar.
func TestSegundaHospedagemCONCORRENTENaoRecriaONo(t *testing.T) {
	chamadas := 0
	rb := orcamentoDeTeste(t, 1000, WithConsumoDuravel(
		func(context.Context, string) (budget.Amount, error) {
			chamadas++
			return budget.Amount{Tokens: 100}, nil
		},
	))
	r1, err := rb.acquire(context.Background(), "run-5")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := rb.acquire(context.Background(), "run-5")
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	defer r2()
	if got := limiteDoNo(t, rb, "run-5").Tokens; got != 900 {
		t.Errorf("o no mudou de limite com a 2.a hospedagem viva: %d", got)
	}
	// E a fonte e consultada UMA vez, nao duas.
	//
	// Este contador ja existia neste teste e nunca era AFIRMADO — um teste latente que teria
	// apanhado o defeito que a revisao do proprio codigo apanhou: `limiteParaIncarnacao` corria
	// em TODAS as aquisicoes, incluindo as reentrantes onde o valor e deitado fora.
	if chamadas != 1 {
		t.Errorf("a fonte duravel foi consultada %d vezes para 2 hospedagens do MESMO run — a "+
			"leitura ao ledger corre onde o valor e descartado", chamadas)
	}
}

// TestReentranteNaoAVISA fecha a metade que fazia o desperdício ser um DEFEITO e não só uma
// ineficiência.
//
// Com o ledger ilegível, uma aquisição reentrante corria `limiteParaIncarnacao`, deitava o valor
// fora — e escrevia à mesma o aviso «arranca com o tecto INTEIRO». Uma linha de log a descrever
// uma decisão que NÃO foi tomada: o nó já existia e aquele limite nem chegou a ser usado.
//
// É a mesma família do que este dia inteiro andou a caçar — uma declaração que não corresponde ao
// efeito — desta vez em código meu, e encontrada a rever o que acabara de pôr em produção.
func TestReentranteNaoAVISA(t *testing.T) {
	var linhas []string
	rb := orcamentoDeTeste(t, 1000,
		WithConsumoDuravel(func(context.Context, string) (budget.Amount, error) {
			return budget.Amount{}, errors.New("event store em baixo")
		}),
		WithBudgetLogger(func(f string, a ...any) { linhas = append(linhas, f) }),
	)
	r1, err := rb.acquire(context.Background(), "run-6")
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	if len(linhas) != 1 {
		t.Fatalf("a PRIMEIRA aquisicao devia avisar uma vez, veio %d", len(linhas))
	}

	// A segunda hospedagem do MESMO run não decide limite nenhum — e não pode avisar.
	r2, err := rb.acquire(context.Background(), "run-6")
	if err != nil {
		t.Fatal(err)
	}
	defer r2()
	if len(linhas) != 1 {
		t.Errorf("a aquisicao REENTRANTE avisou (%d linhas no total) — o aviso descreve uma decisao "+
			"que nao foi tomada, porque o no ja existia", len(linhas))
	}
}

// TestDuasHospedagensNaJanelaDaPrimeira força a corrida que a re-verificação sob o lock existe
// para tratar — e que nenhum teste cobria.
//
// A leitura do ledger acontece FORA do lock (é I/O). Nessa janela, DUAS hospedagens do mesmo run
// podem ambas concluir «eu sou a primeira». Se a criação do nó confiasse nessa conclusão em vez de
// a RE-VERIFICAR sob o lock, a segunda chamaria `AddNode` sobre um nó já criado, receberia
// `ErrNodeExists`, e a hospedagem FALHARIA — um run que não arranca por ter sido retomado duas
// vezes ao mesmo tempo.
//
// A fonte bloqueia até as duas lá estarem: sem isso o teste seria um sorteio e passaria quase
// sempre, que é o pior tipo de teste de concorrência.
func TestDuasHospedagensNaJanelaDaPrimeira(t *testing.T) {
	chegaram := make(chan struct{}, 2)
	solta := make(chan struct{})
	rb := orcamentoDeTeste(t, 1000, WithConsumoDuravel(
		func(context.Context, string) (budget.Amount, error) {
			chegaram <- struct{}{}
			<-solta // segura as duas DENTRO da janela em que ambas se julgam a primeira
			return budget.Amount{Tokens: 100}, nil
		},
	))

	type resultado struct {
		rel func()
		err error
	}
	saidas := make(chan resultado, 2)
	for i := 0; i < 2; i++ {
		go func() {
			rel, err := rb.acquire(context.Background(), "run-corrida")
			saidas <- resultado{rel: rel, err: err}
		}()
	}
	// Espera que AMBAS estejam dentro da fonte, e só então liberta.
	<-chegaram
	<-chegaram
	close(solta)

	// As libertações ficam para DEPOIS da asserção: soltá-las aqui removeria o nó antes de o
	// medir — foi o que a primeira versão deste teste fez, e acusou o nó de não existir.
	var libertar []func()
	for i := 0; i < 2; i++ {
		r := <-saidas
		if r.err != nil {
			t.Errorf("hospedagem %d falhou numa corrida legitima: %v — sem a re-verificacao sob o "+
				"lock, a segunda tenta AddNode sobre um no ja criado", i+1, r.err)
			continue
		}
		libertar = append(libertar, r.rel)
	}
	defer func() {
		for _, f := range libertar {
			f()
		}
	}()
	if got := limiteDoNo(t, rb, "run-corrida").Tokens; got != 900 {
		t.Errorf("limite = %d, quero 900 (1000-100)", got)
	}
}
