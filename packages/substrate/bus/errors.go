package bus

// BusError é o tipo de erro sentinela do barramento. Carrega um código estável
// (E_BUS_*) e é comparável com errors.Is por identidade.
type BusError struct {
	// Code é o código estável do contrato (ex.: "E_BUS_CONFIG").
	Code string
	// msg é a descrição legível.
	msg string
}

func (e *BusError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas do barramento. Todos comparáveis com errors.Is.
var (
	// ErrConfig — configuração de subscrição/barramento inválida (Name vazio,
	// Handler nil, Buffer < 0, política desconhecida, ou replay (FromSeq) sem
	// Streams no filtro — o replay é por stream e seria irrealizável).
	ErrConfig = &BusError{Code: "E_BUS_CONFIG", msg: "configuracao de subscricao invalida"}

	// ErrClosed — o barramento (ou subscrição) já foi fechado.
	ErrClosed = &BusError{Code: "E_BUS_CLOSED", msg: "barramento fechado"}

	// ErrNilStore — New foi chamado sem um EventStore para envolver.
	ErrNilStore = &BusError{Code: "E_BUS_NIL_STORE", msg: "event store nulo"}
)
