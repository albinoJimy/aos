package breaker

import (
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
)

// machineWallClock deriva o sinal wall-clock ABSOLUTO da própria [state.Machine]: o
// tempo decorrido desde a ENTRADA no estado corrente ([state.Machine.EnteredAt]),
// medido com o MESMO relógio injectado da máquina ([state.Machine.Clock]). Quando o run
// está em running, é exactamente "tempo absoluto desde running" — o sinal que o breaker
// quer (e o backstop wall-clock absoluto de tecnica/08 §6, NÃO o ActiveWork da liveness
// que congelaria em espera). Partilhar o relógio da máquina garante que o breaker e os
// deadlines duráveis concordam no tempo (sem duas fontes de "agora" a divergir).
type machineWallClock struct {
	m *state.Machine
}

// NewMachineWallClock constrói a [WallClockSource] default de produção sobre a máquina.
func NewMachineWallClock(m *state.Machine) WallClockSource {
	return machineWallClock{m: m}
}

// Elapsed implementa [WallClockSource]: agora − enteredAt, no relógio da máquina. Nunca
// negativo (um enteredAt no futuro por salto de relógio devolve 0).
func (w machineWallClock) Elapsed() time.Duration {
	d := w.m.Clock().Now().Sub(w.m.EnteredAt())
	if d < 0 {
		return 0
	}
	return d
}
