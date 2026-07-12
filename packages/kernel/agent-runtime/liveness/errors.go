package liveness

import "errors"

var (
	// ErrInvalidGateTTL — o TTL passado a [NewWaitingGate] não é positivo. Um gate de
	// aprovação sem TTL não é fail-closed: sem prazo, uma aprovação em falta deixaria o
	// run pendurado indefinidamente, contrariando o timeout fail-closed do gate humano
	// (ADR-013). Fail-closed na construção: recusa criar um gate que não fecha.
	ErrInvalidGateTTL = errors.New("liveness: TTL do gate de espera tem de ser > 0 (fail-closed, ADR-013)")

	// ErrNilMachine — [NewWaitingGateFrom] recebeu uma [state.Machine] nil. Sem a
	// máquina não há fonte de TTL nem de relógio de onde derivar o gate; recusa
	// fail-closed em vez de fabricar um gate a partir de defaults silenciosos.
	ErrNilMachine = errors.New("liveness: state.Machine nil (sem fonte de TTL/relógio para derivar o gate)")
)
