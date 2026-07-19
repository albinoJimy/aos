package worker

import (
	"context"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Mediator é a PORTA do Reference Monitor (ADR-002) de que o supervisor depende:
// TODA a tool call de um passo é submetida a [Mediator.Mediate] ANTES de qualquer
// efeito externo. O worker NUNCA executa uma tool por outra via — não há caminho
// que contorne o PEP. *referencemonitor.Monitor satisfá-la directamente; a
// interface existe para o worker não acoplar o dispatch concreto nem re-embeber a
// política, e para os testes injectarem um mediador de observação sem forjar Permits.
type Mediator interface {
	// Mediate avalia e (em permit) despacha a tool call, devolvendo a decisão
	// não-forjável. Uma negação/escalada devolve Decision sem Permit válido (o worker
	// fail-closed com [ErrDenied]); um erro de execução da tool vem em Decision.ToolErr.
	Mediate(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error)
}

// Compile-time: o Monitor real de AOS-003 é um [Mediator] sem adaptação.
var _ Mediator = (*referencemonitor.Monitor)(nil)
