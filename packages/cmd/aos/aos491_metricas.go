package main

// AOS-491 — TURNOS POR MOTIVO DE PARAGEM.
//
// Uma resposta cortada pelo limite de tokens, uma recusa por filtro de conteúdo e uma conclusão
// chegavam iguais ao runtime, e nenhum contador do nó as separava: a medição de tool calls do
// AOS-489 só se move quando o turno tem pelo menos uma chamada. Esta família do `/metrics` conta
// os turnos de modelo pelo motivo de paragem que o provider declarou.

import (
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// rotuloSemMotivo é o valor do rótulo `stop_reason` para um turno cujo provider não declarou
// motivo ([agentruntime.StopUnreported], que é a string vazia). Um rótulo vazio em Prometheus é
// o mesmo que rótulo nenhum, e a série confundia-se com um total.
const rotuloSemMotivo = "unreported"

// turnosPorMotivo conta, por processo, os turnos de modelo por motivo de paragem. É o
// [agentruntime.StopReasonStats] do runtime do nó.
//
// O vocabulário é FECHADO — [agentruntime.StopReasons] —, pelo que o rótulo nunca leva texto do
// provider: o runtime normaliza o motivo antes de o reportar, e um valor que mesmo assim chegasse
// fora do vocabulário é somado em `other`.
//
// O QUE A SOMA NÃO É. Como a do AOS-489, é por processo e desde o arranque, e conta turnos
// PERCORRIDOS: um run re-hospedado (retoma, crash-resume) volta a percorrer os turnos já dados e
// soma-os outra vez. Serve para a proporção entre motivos e a sua tendência; o motivo de um turno
// concreto está no `turn.recorded`.
type turnosPorMotivo struct {
	motivos []agentruntime.StopReason
	total   map[agentruntime.StopReason]*atomic.Int64
}

func novoTurnosPorMotivo() *turnosPorMotivo {
	t := &turnosPorMotivo{motivos: agentruntime.StopReasons(), total: map[agentruntime.StopReason]*atomic.Int64{}}
	for _, m := range t.motivos {
		t.total[m] = new(atomic.Int64)
	}
	return t
}

// observar é o [agentruntime.StopReasonStats]. Nil-safe: um nó montado à mão num teste, sem a
// medição, não rebenta.
func (t *turnosPorMotivo) observar(_ string, motivo agentruntime.StopReason) {
	if t == nil {
		return
	}
	if c := t.total[motivo.Normalizado()]; c != nil {
		c.Add(1)
	}
}

// lido devolve o total de um motivo do vocabulário.
func (t *turnosPorMotivo) lido(motivo agentruntime.StopReason) int64 {
	if t == nil || t.total[motivo] == nil {
		return 0
	}
	return t.total[motivo].Load()
}

// rotuloDoMotivo é o valor do rótulo `stop_reason` de um motivo do vocabulário.
func rotuloDoMotivo(motivo agentruntime.StopReason) string {
	if motivo == agentruntime.StopUnreported {
		return rotuloSemMotivo
	}
	return string(motivo)
}
