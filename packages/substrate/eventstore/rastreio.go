package eventstore

import "context"

// rastreio.go — a porta de RASTREIO do Event Store (EPIC-08).
//
// # Porque não é o Observer
//
// O [Observer] declara-se, no seu próprio doc, como um gancho leve que «não puxa o SDK
// OTel (isso é EPIC-08)» e que serve contagem/latência. Repropô-lo para rastreio seria
// contrariar o que ele diz ser — e, pior, não daria o que um span precisa: o [Observer]
// é chamado DEPOIS da operação e sem contexto, pelo que um span nascido dele seria
// ÓRFÃO. Um span sem pai não se liga à trajectória que o causou, e a árvore de spans
// (AOS-077) é precisamente o que dá sentido a observar um Event Store.
//
// Esta porta recebe o `ctx` da operação, e por isso o span fica ONDE deve: por baixo do
// span do passo que o provocou.
//
// # Porque é declarada aqui e não importada
//
// Este módulo tem ZERO dependências, e é isso que permite ao binário do nó ser zero-dep
// (ADR-017 §1). Importar o módulo de observabilidade para obter uma interface de três
// métodos trocaria essa propriedade por conveniência. A forma é deliberadamente a mesma
// do `Tracer`/`Span` de `substrate/otel-genai`, para que a ponte no ápice de composição
// seja um adaptador fino e não uma tradução.

// Rastreador abre spans. Uma implementação nil-safe está em [NopRastreador].
type Rastreador interface {
	// Iniciar abre um span para a operação e devolve o ctx com o span corrente (para
	// os filhos se ligarem) e o próprio span.
	Iniciar(ctx context.Context, operacao string) (context.Context, Rastro)
}

// Rastro é um span aberto.
type Rastro interface {
	// Atributo acrescenta um par chave/valor ao span.
	Atributo(chave string, valor any)
	// Fim fecha o span. Chamar duas vezes é permitido e não faz nada da segunda.
	Fim()
}

// Operações que este pacote emite. Vivem aqui, junto de quem as emite, porque um nome de
// operação composto em runtime não é enumerável a partir do código — a mesma razão que o
// gate `event-catalog` impõe aos tipos de evento.
const (
	// OperacaoAppend — uma escrita no log, do pedido ao desfecho.
	OperacaoAppend = "aos.eventstore.append"
	// OperacaoRead — a leitura de um stream (a base do replay e da re-hidratação).
	OperacaoRead = "aos.eventstore.read"
)

// Atributos que este pacote põe nos seus spans.
const (
	// AtributoStream — o stream_id do AOS (que é o run_id).
	AtributoStream = "aos.eventstore.stream_id"
	// AtributoSeq — o seq atribuído (0 quando a escrita foi recusada).
	AtributoSeq = "aos.eventstore.seq"
	// AtributoDesfecho — committed | duplicate | rejected, para Append; ok | not_found,
	// para Read. É o que distingue «recusámos» de «avariou» numa vista de query-time.
	AtributoDesfecho = "aos.eventstore.outcome"
	// AtributoContagem — quantos eventos uma leitura devolveu.
	AtributoContagem = "aos.eventstore.events"
	// AtributoErro — o código canónico do erro (E_*), quando houve.
	AtributoErro = "error.type"
)

// NopRastreador é o default: não observa nada.
//
// Existe para que o campo nunca seja nil no caminho quente — um `if r != nil` por
// operação seria ruído em todas as vias, e esquecê-lo uma vez seria um panic no caminho
// mais crítico que existe.
type NopRastreador struct{}

// Iniciar implementa [Rastreador] sem abrir nada.
func (NopRastreador) Iniciar(ctx context.Context, _ string) (context.Context, Rastro) {
	return ctx, nopRastro{}
}

type nopRastro struct{}

func (nopRastro) Atributo(string, any) {}
func (nopRastro) Fim()                 {}
