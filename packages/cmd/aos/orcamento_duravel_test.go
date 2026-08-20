package main

import (
	"context"
	"errors"
	"io"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// TestAdaptadorTrataRunSemTurnosComoZero — a distinção que decide se a correcção é usável.
//
// `ErrBurndownNoLedger` significa «este run ainda não tem turnos», que é o caso NORMAL da
// primeira incarnação. Se virasse degradação, CADA run novo arrancava com um aviso de ledger
// ilegível — e o aviso que interessa afogava-se nesses, que é o defeito do papel de parede outra
// vez.
func TestAdaptadorTrataRunSemTurnosComoZero(t *testing.T) {
	// Store VAZIO: o stream do run existe mas nao tem turnos — o caso real da 1.a incarnacao.
	f := consumoDuravelParaOrcamento(newTurnLedgerBurndown(storeVazio{}))
	if f == nil {
		t.Fatal("adaptador nil sobre uma fonte nao-nil")
	}
	got, err := f(context.Background(), "run-novo")
	if err != nil {
		t.Fatalf("um run SEM turnos devia dar consumo zero sem erro, veio: %v", err)
	}
	if got != (budget.Amount{}) {
		t.Errorf("consumo de um run sem turnos = %+v, quero zero", got)
	}
}

// TestAdaptadorNilQuandoNaoHaFonte — sem Event Store não há fonte, e o adaptador tem de devolver
// nil para o orçamento manter o comportamento de antes em vez de chamar um ponteiro vazio.
func TestAdaptadorNilQuandoNaoHaFonte(t *testing.T) {
	if f := consumoDuravelParaOrcamento(nil); f != nil {
		t.Error("fonte nil devia dar adaptador nil")
	}
}

// TestErroRealDoLedgerPassaComoErro é o CONTROLO do primeiro teste.
//
// Sem ele, um adaptador que engolisse TODOS os erros — devolvendo sempre zero — passaria no teste
// de cima. E engolir tudo seria pior do que o defeito original: o tecto seria semeado com zero
// consumo sobre um ledger que ninguém conseguiu ler, silenciosamente.
func TestErroRealDoLedgerPassaComoErro(t *testing.T) {
	fonte := newTurnLedgerBurndown(storeQueFalha{})
	f := consumoDuravelParaOrcamento(fonte)
	if _, err := f(context.Background(), "run-x"); err == nil {
		t.Error("um erro REAL do ledger foi engolido — o tecto seria semeado sobre dados que ninguem leu")
	}
}

// storeQueFalha devolve sempre erro, para distinguir «sem turnos» de «ilegível».
type storeQueFalha struct{}

func (storeQueFalha) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return nil, errors.New("substrato em baixo")
}

// storeVazio devolve um stream sem eventos — o run que ainda não teve turnos.
type storeVazio struct{}

func (storeVazio) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return nil, nil
}

// TestBootstrapLigaOConsumoDuravelAoOrcamento é o teste da CABLAGEM.
//
// Os testes acima provam o que a semeadura FAZ; nenhum deles prova que o nó chega a LIGÁ-LA. Uma
// mutação que apagasse a chamada em `Bootstrap` passava em todos — e o tecto voltava a recomeçar a
// cada hospedagem sem que nada avisasse.
//
// É a quarta vez neste dia que a unidade estava testada e a ligação não.
func TestBootstrapLigaOConsumoDuravelAoOrcamento(t *testing.T) {
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "1000")
	cfg := tnBaseConfig()

	node, err := Bootstrap(t.Context(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer node.Close()

	if node.orcamento == nil {
		t.Fatal("o no arrancou sem tecto por-run apesar de AOS_BUDGET_MAX_TOKENS estar definida")
	}
	if !node.orcamento.TemConsumoDuravel() {
		t.Error("o no NAO ligou a fonte duravel ao tecto por-run — a retoma volta a recomecar o " +
			"orcamento, e um run em ciclo de escalada pode gastar N x tecto")
	}
}

// TestSemTectoNaoHaOrcamentoParaLigar é o CONTROLO: sem `AOS_BUDGET_MAX_TOKENS` não há tecto, e
// não há nada a ligar. Sem este ramo, o teste acima passaria mesmo que a ligação fosse
// incondicional — e uma ligação incondicional rebentaria num nó sem orçamento.
func TestSemTectoNaoHaOrcamentoParaLigar(t *testing.T) {
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "")
	cfg := tnBaseConfig()
	node, err := Bootstrap(t.Context(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap sem tecto: %v", err)
	}
	defer node.Close()
	if node.orcamento != nil {
		t.Error("sem AOS_BUDGET_MAX_TOKENS o no nao devia ter tecto por-run")
	}
}
