package bus

import "github.com/aos-ref/substrate/eventstore"

// Filter selecciona que eventos uma subscrição recebe. Estende o filtro do Event
// Store (que só tem Streams e Types) com a dimensão Producers (por NHIID). Um
// campo vazio não filtra nessa dimensão (recebe tudo). As dimensões combinam-se
// por AND.
type Filter struct {
	// Types filtra por Event.Type (ex.: "tool.call.dispatched").
	Types []string
	// Streams filtra por Event.StreamID. Necessário para catch-up/replay: a
	// leitura histórica do Event Store é por stream (ver doc do pacote).
	Streams []string
	// Producers filtra por Event.Producer.NHIID (o principal emissor).
	Producers []string
}

// matches indica se um evento passa o filtro completo (as três dimensões).
func (f Filter) matches(e eventstore.Event) bool {
	if len(f.Streams) > 0 && !contains(f.Streams, e.StreamID) {
		return false
	}
	if len(f.Types) > 0 && !contains(f.Types, e.Type) {
		return false
	}
	if len(f.Producers) > 0 && !contains(f.Producers, e.Producer.NHIID) {
		return false
	}
	return true
}

// toEventStore projecta o filtro do barramento no filtro do Event Store, que só
// conhece Streams e Types. A dimensão Producers é aplicada no barramento (o
// Event Store não a expressa), por isso matches é sempre reavaliado à entrega.
func (f Filter) toEventStore() eventstore.Filter {
	return eventstore.Filter{Streams: f.Streams, Types: f.Types}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
