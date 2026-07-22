package control

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// LoopSteer adapta o [SteerChannel] à porta [agentruntime.SteerSource] — o wiring que
// liga o canal de controlo out-of-band (AOS-023) ao loop base (AOS-158). É o
// "adaptador-no-pilar" do idioma AOS-060: o kernel define a porta, o pacote de controlo
// (que já depende do kernel) fornece o concreto, sem o kernel importar `control`.
//
// O loop consulta-o na fronteira de fim-de-turno: [LoopSteer.GracefulPause] materializa
// a pausa durável (running→paused) via o [StateGate] do run, e
// [LoopSteer.PendingCorrection] expõe a correcção humana trusted a injectar no tail.
type LoopSteer struct {
	ch    *SteerChannel
	gates func(runID string) StateGate
}

// NewLoopSteer constrói o adaptador. gates resolve o [StateGate] (a máquina de estados
// durável, AOS-017) de cada run — o [SteerChannel.GracefulPause] precisa dele para a
// transição running→paused. Um gates que devolva nil para um run desliga a pausa desse
// run (o loop continua), fail-safe.
func NewLoopSteer(ch *SteerChannel, gates func(runID string) StateGate) *LoopSteer {
	return &LoopSteer{ch: ch, gates: gates}
}

// GracefulPause implementa [agentruntime.SteerSource]: se um interrupt estiver pendente
// para o run, materializa a pausa durável via o StateGate e devolve true (o loop pára).
func (a *LoopSteer) GracefulPause(ctx context.Context, runID string) (bool, error) {
	if a == nil || a.ch == nil || a.gates == nil {
		return false, nil
	}
	gate := a.gates(runID)
	if gate == nil {
		return false, nil
	}
	return a.ch.GracefulPause(ctx, runID, gate)
}

// PendingCorrection implementa [agentruntime.SteerSource]: expõe a correcção pendente do
// run (dado de controlo trusted), ou (nil, false).
func (a *LoopSteer) PendingCorrection(runID string) ([]byte, bool) {
	if a == nil || a.ch == nil {
		return nil, false
	}
	return a.ch.PendingCorrection(runID)
}

// Assegura em compile-time que LoopSteer satisfaz a porta do loop.
var _ agentruntime.SteerSource = (*LoopSteer)(nil)
