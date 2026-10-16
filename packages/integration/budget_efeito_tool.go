package integration

// AOS-530 (peça 3b; ADR-042 §2.6) — A RESERVA DE UMA TOOL CUJO EFEITO É PAGO.
//
// Até aqui o tecto em dólares por run (AOS-260, ligado em produção pelo AOS-527) era debitado só
// pela admissão do TURNO DE MODELO; a estimativa das tool calls é zero em dólares
// ([TokenOnlyEstimator]), porque o custo de um efeito não se conhece antes dele. A `ocr_read` é a
// primeira tool cujo efeito é uma chamada a um modelo — e o custo dela conhece-se ANTES, por cima:
// a tabela de preços × os tectos de tokens da tool. É esse máximo que se reserva aqui.
//
// O MOLDE é o da admissão do turno ([ModelTurnAdmission]): reservar antes, saldar pelo REAL depois
// (libertar a reserva e reservar o medido, limitado ao headroom), sobre o MESMO nó por-run que o
// hook de tool calls e a admissão já debitam. Não há aqui um segundo orçamento.
//
// O que difere do turno, e porquê:
//
//   - A RESERVA É POR CHAMADA, não por passo do loop: quem a pede é a função da tool, dentro do
//     efeito, depois do permit do RM. O replay e a dedup do step-ledger não chegam a executar a
//     tool, logo não reservam — não há aqui um detector de replay porque não há caminho que o
//     peça.
//   - SEM LATCH DE EXCEDENTE. Um custo medido acima do que resta cobra o headroom até ao topo (o
//     run fica com a dimensão a zero) e devolve o que foi cobrado; a reserva seguinte de qualquer
//     coisa com custo deixa de caber. O número exacto não se perde: o chamador grava-o no evento
//     durável do custo, que é a fonte do consumo durável do run.

import (
	"context"
	"errors"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
)

// ErrReservaDeEfeitoSemOrcamento — pediu-se uma reserva a um orçamento nil. Quem a pede tem de
// saber se há orçamento composto ([RunBudget] nil ⇒ não se reserva nada, como para os turnos).
var ErrReservaDeEfeitoSemOrcamento = errors.New("integration: reserva do efeito de uma tool sem orcamento por-run composto")

// ReservaDeEfeito é a reserva viva de um efeito pago de uma tool: o máximo por chamada, à espera
// de ser saldado pelo custo medido ([ReservaDeEfeito.Saldar]) ou libertado
// ([ReservaDeEfeito.Libertar]). Resolve-se UMA vez; as chamadas seguintes são no-op.
type ReservaDeEfeito struct {
	rb    *RunBudget
	runID string
	r     budget.Reservation

	once sync.Once
}

// ReservarEfeitoDeTool reserva `max` no nó de orçamento do run, ANTES do efeito.
//
// Os erros são os da árvore, sem tradução, para que quem chama distinga a NEGAÇÃO
// ([budget.ErrNoHeadroom] — o máximo não cabe no que resta ao run) da CEGUEIRA
// ([budget.ErrUnknownNode] — o run não tem nó vivo, um defeito de composição) e da quantia
// inválida ([budget.ErrInvalidAmount]). Nenhum dos três chega a debitar nada.
func (rb *RunBudget) ReservarEfeitoDeTool(ctx context.Context, runID string, max budget.Amount) (*ReservaDeEfeito, error) {
	if rb == nil {
		return nil, ErrReservaDeEfeitoSemOrcamento
	}
	r, err := rb.tree.Reserve(ctx, runID, max)
	if err != nil {
		return nil, err
	}
	return &ReservaDeEfeito{rb: rb, runID: runID, r: r}, nil
}

// Saldar troca a reserva pelo custo MEDIDO e devolve o que foi COBRADO à árvore.
//
// A troca é a da admissão do turno: libertar a reserva e reservar+confirmar o real, limitado ao
// headroom em cada dimensão. O cobrado é menor do que o real só quando o real excede o que restava
// ao run — e é o REAL, não o cobrado, que o chamador grava no evento durável. Um real a zero nas
// duas dimensões liberta a reserva e não cobra nada.
//
// Resolve-se uma vez: um segundo Saldar (ou um Libertar depois dele) não faz nada e devolve zero.
func (e *ReservaDeEfeito) Saldar(ctx context.Context, real budget.Amount) (budget.Amount, error) {
	if e == nil {
		return budget.Amount{}, nil
	}
	var (
		cobrado budget.Amount
		err     error
	)
	e.once.Do(func() {
		if err = e.rb.tree.Release(ctx, e.r); err != nil {
			return
		}
		// [minAmount] consigo próprio só fixa o piso em zero (um real negativo não cobra nada).
		cobranca := minAmount(real, real)
		if avail, aerr := e.rb.tree.Available(e.runID); aerr == nil {
			cobranca = minAmount(real, avail)
		}
		if cobranca.Tokens <= 0 && cobranca.CostMicroUSD <= 0 {
			return
		}
		var r2 budget.Reservation
		if r2, err = e.rb.tree.Reserve(ctx, e.runID, cobranca); err != nil {
			return
		}
		if err = e.rb.tree.Commit(ctx, r2); err != nil {
			return
		}
		cobrado = cobranca
	})
	return cobrado, err
}

// Libertar devolve a reserva ao headroom sem cobrar nada: o efeito não aconteceu, ou aconteceu sem
// custo conhecido. Resolve-se uma vez, como [ReservaDeEfeito.Saldar].
func (e *ReservaDeEfeito) Libertar(ctx context.Context) error {
	if e == nil {
		return nil
	}
	var err error
	e.once.Do(func() { err = e.rb.tree.Release(ctx, e.r) })
	return err
}

// Reservado devolve a quantia reservada (para a linha de log e para os testes).
func (e *ReservaDeEfeito) Reservado() budget.Amount {
	if e == nil {
		return budget.Amount{}
	}
	return e.r.Amount
}
