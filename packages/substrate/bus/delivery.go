package bus

import (
	"sync"

	"github.com/aos-ref/substrate/eventstore"
)

// Handler processa um evento entregue. É invocado, um de cada vez e em ordem de
// seq por stream, na goroutine de entrega da subscrição. O Handler DEVE confirmar
// o resultado explicitamente:
//
//   - Delivery.Ack — processamento concluído; o cursor avança de forma durável.
//   - Delivery.Nack — falha; o evento é re-entregue (até Retry vezes) e depois
//     encaminhado para a dead-letter queue.
//
// Se o Handler não chamar nem Ack nem Nack (equivalente a uma queda a meio do
// processamento), o evento NÃO é confirmado: o cursor não avança e o evento será
// RE-ENTREGUE após reinício da subscrição (semântica at-least-once). Por isso o
// Handler deve ser idempotente.
type Handler func(*Delivery)

type outcome int

const (
	outcomeNone outcome = iota
	outcomeAck
	outcomeNack
)

// Delivery é a entrega de um único evento a um Handler. Expõe o envelope e os
// meios de confirmação. A primeira chamada a Ack ou Nack vence; chamadas
// subsequentes são ignoradas (idempotente). É seguro chamar Ack/Nack de outra
// goroutine, mas o modelo de referência espera confirmação SÍNCRONA (antes de o
// Handler retornar); um Ack/Nack tardio, após o Handler já ter retornado sem
// confirmar, não tem efeito na decisão de cursor desta entrega.
type Delivery struct {
	// Event é o envelope canónico (cópia própria).
	Event eventstore.Event

	mu    sync.Mutex
	res   outcome
	cause error
}

// Ack confirma o processamento com sucesso: o cursor avança de forma durável.
func (d *Delivery) Ack() {
	d.mu.Lock()
	if d.res == outcomeNone {
		d.res = outcomeAck
	}
	d.mu.Unlock()
}

// Nack sinaliza falha no processamento. cause (opcional) é registado na
// dead-letter queue se as tentativas se esgotarem. O evento é re-entregue até
// Retry vezes; depois é encaminhado para a dead-letter e o cursor avança para
// não prender a subscrição.
func (d *Delivery) Nack(cause error) {
	d.mu.Lock()
	if d.res == outcomeNone {
		d.res = outcomeNack
		d.cause = cause
	}
	d.mu.Unlock()
}

func (d *Delivery) result() (outcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.res, d.cause
}
