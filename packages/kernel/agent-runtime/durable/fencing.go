package durable

import (
	"context"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// FencingToken é a REALIZAÇÃO do contador de fencing monotónico de AOS-018 — o
// mesmo contrato que a máquina de estados de AOS-017 ([state.FencingToken]:
// Valid()/Value()) exige na transição ready → running (o claim). É um uint64 em que
// qualquer valor > 0 é válido (0 = ausência de token). A sua ORIGEM monotónica
// durável é o [LeaseManager], que o minta via a concorrência optimista do Event
// Store (ver lease.go); aqui é só o tipo do contrato + o enforcement.
//
// Por ser estruturalmente idêntico a [state.FencingToken], um FencingToken é
// passável directamente a Machine.Transition sem conversão nem acoplamento de
// pacotes (durable NÃO importa state — a ligação é por interface implícita).
type FencingToken uint64

// Valid implementa o contrato de AOS-017: qualquer token > 0 é utilizável para
// reclamar o run. 0 é reservado a "sem token" (nunca mintado por um claim).
func (t FencingToken) Valid() bool { return t > 0 }

// Value implementa o contrato de AOS-017: devolve o valor monotónico do token.
func (t FencingToken) Value() uint64 { return uint64(t) }

// TokenSource é a autoridade que reporta o fencing token CORRENTE de um run — o
// maior token alguma vez mintado (o último claim vencedor). O [FencedAppender]
// consulta-a para decidir se uma escrita é de um worker obsoleto. O [LeaseManager]
// satisfá-la; o Escalonador de EPIC-03 pode fornecer a sua própria autoridade sobre
// o mesmo contrato (interface partilhável — ver [LeaseAuthority]).
type TokenSource interface {
	// CurrentToken devolve o token corrente do run (0 se nunca foi reclamado).
	CurrentToken(ctx context.Context, runID string) (FencingToken, error)
}

// LeaseExpiryAuthority é uma capacidade OPCIONAL que uma [TokenSource] pode também
// implementar para o [FencedAppender] negar a escrita de um detentor cujo lease já
// EXPIROU (TTL esgotado por ausência de heartbeat) — mesmo que o token ainda seja o
// corrente (ainda não superado por um novo claim). Sem esta capacidade, o enforcement
// fecha apenas o caso token < corrente (worker superado); com ela, fecha TAMBÉM o
// fail-open de liveness em que um detentor morto (lease expirado) escrevia com sucesso
// na janela expirado-mas-não-superado — honrando o contrato de [ErrLeaseExpired]. O
// [LeaseManager] satisfá-la sobre o seu relógio injectado; mantém-se SEPARADA de
// [TokenSource] para não onerar o contrato mínimo partilhado com o Escalonador.
type LeaseExpiryAuthority interface {
	// CurrentLeaseExpired reporta se o lease corrente do run já expirou e se existe
	// algum lease (false, false para um run nunca reclamado).
	CurrentLeaseExpired(ctx context.Context, runID string) (expired, exists bool, err error)
}

// FenceObserver é o gancho de observabilidade do enforcement (contadores de
// escritas aceites/rejeitadas por fencing). Recebe apenas os valores dos tokens e o
// run_id — rótulos, nunca segredos nem payload. Default: [NopFenceObserver].
type FenceObserver interface {
	// Accepted é chamado após uma escrita fenced ser aceite (token >= corrente).
	Accepted(runID string, token uint64)
	// Rejected é chamado quando uma escrita é fenced-out (token < corrente) — o
	// worker está obsoleto e a escrita NÃO chega ao Event Store.
	Rejected(runID string, token, current uint64)
}

// NopFenceObserver descarta a observabilidade do enforcement. É o default.
type NopFenceObserver struct{}

// Accepted implementa [FenceObserver].
func (NopFenceObserver) Accepted(string, uint64) {}

// Rejected implementa [FenceObserver].
func (NopFenceObserver) Rejected(string, uint64, uint64) {}

// FencedAppender é o ENFORCEMENT do fencing de AOS-018: um GUARD OPT-IN com que o
// consumidor ENVOLVE as suas escritas duráveis de efeito, REJEITANDO
// ([ErrStaleFencingToken]) qualquer escrita cujo token seja INFERIOR ao corrente do
// run — ou seja, de um worker cujo lease já foi superado por um novo claim. A escrita
// obsoleta NÃO chega ao log.
//
// # Alcance HONESTO (o que o fencing garante e onde)
//
// O fencing só protege as escritas EFECTIVAMENTE ROTEADAS por este Append. NÃO está
// (por AOS-018) ligado aos caminhos de escrita internos do módulo: o [StepLedger], o
// [EventStoreCheckpointer] e a máquina de estados de AOS-017 persistem DIRECTO no
// Event Store. Nesses caminhos, o que impede duplicados hoje é a DEDUP por
// idempotency_key do Event Store (StatusDuplicate) MAIS a idempotência DOWNSTREAM —
// NÃO o fencing. Um consumidor que queira a garantia de "no máximo um escritor
// efectivo" tem de encaminhar a sua escrita de efeito por este [FencedAppender]
// (padrão: Claim → token → FencedAppender.Append), como no teste de integração. O
// fencing e a dedup do ES são camadas COMPLEMENTARES, não substitutas.
//
// # Autoridade do token corrente
//
// O token corrente é lido da [TokenSource] (o [LeaseManager] lê-o do stream de lease
// no Event Store — durável e ordem-total). A comparação é token >= corrente:
//   - token < corrente → o lease foi superado; a escrita é fenced-out (rejeitada);
//   - token == corrente → o detentor legítimo escreve (salvo lease expirado — ver
//     [LeaseExpiryAuthority]);
//   - token > corrente → nunca deveria acontecer (não há token acima do reclamado);
//     é tolerado (>=) por robustez, mas o caminho normal nunca o produz.
//
// # Janela de verificação-depois-escrita (TOCTOU) — limite conhecido
//
// O token é consultado EXTERNAMENTE e NÃO é dobrado no envelope/CAS do próprio evento
// de negócio (o Event Store de referência não expõe um expected_seq condicionado ao
// token). Entre a leitura do corrente e o Append há, por isso, uma janela em que um
// novo claim poderia elevar o corrente. Ela só afecta uma escrita cujo token ERA o
// corrente no momento da leitura (o detentor a ser superado nesse instante) — NUNCA
// deixa passar um token ESTRITAMENTE inferior (o caso do worker obsoleto). AOS-018
// fecha assim, de forma provada, o caso token-estritamente-inferior; o boundary
// token-IGUAL sob concorrência real (dobrar o token no CAS durável do Event Store via
// expected_seq) fica delegado à implementação de produção do substrate — está FORA do
// âmbito deste módulo. Ver o teste de interleaving supersessão-durante-append.
type FencedAppender struct {
	store  EventStore
	tokens TokenSource
	obs    FenceObserver
}

// FencedOption configura o [FencedAppender].
type FencedOption func(*FencedAppender)

// WithFenceObserver injecta o gancho de observabilidade do enforcement (default
// [NopFenceObserver]).
func WithFenceObserver(o FenceObserver) FencedOption {
	return func(a *FencedAppender) {
		if o != nil {
			a.obs = o
		}
	}
}

// NewFencedAppender constrói o enforcement sobre um Event Store e uma autoridade de
// token. Ambos são obrigatórios ([ErrNilStore] / [ErrNilTokenSource]).
func NewFencedAppender(store EventStore, tokens TokenSource, opts ...FencedOption) (*FencedAppender, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if tokens == nil {
		return nil, ErrNilTokenSource
	}
	a := &FencedAppender{store: store, tokens: tokens, obs: NopFenceObserver{}}
	for _, o := range opts {
		o(a)
	}
	if a.obs == nil {
		a.obs = NopFenceObserver{}
	}
	return a, nil
}

// Append escreve in no stream do run (stream_id == runID, a convenção da máquina de
// estados de AOS-017 e do ledger de AOS-014) APENAS se token não for inferior ao
// token corrente do run. Um token em falta/0 ([FencingToken.Valid] falso) ou inferior
// ao corrente devolve [ErrStaleFencingToken] SEM tocar no Event Store. Caso contrário
// delega no Append do Event Store (preservando a sua dedup por idempotency_key e a
// concorrência optimista via opts).
//
// runID identifica simultaneamente o stream de escrita e o run cujo token corrente é
// consultado — o mesmo identificador que o claim usou.
func (a *FencedAppender) Append(ctx context.Context, runID string, token FencingToken, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if runID == "" {
		return eventstore.AppendResult{}, ErrEmptyRunID
	}
	if !token.Valid() {
		// Token ausente/0 nunca é o corrente de um run reclamado: trata-se como
		// obsoleto (fenced-out) em vez de escrever sem fencing.
		a.obs.Rejected(runID, token.Value(), 0)
		return eventstore.AppendResult{}, fmt.Errorf("%w: token ausente/0 (run %s)", ErrStaleFencingToken, runID)
	}

	current, err := a.tokens.CurrentToken(ctx, runID)
	if err != nil {
		return eventstore.AppendResult{}, err
	}
	if token.Value() < current.Value() {
		a.obs.Rejected(runID, token.Value(), current.Value())
		return eventstore.AppendResult{}, fmt.Errorf("%w: token=%d inferior ao corrente=%d (run %s)",
			ErrStaleFencingToken, token.Value(), current.Value(), runID)
	}

	// Liveness (opcional): se a autoridade souber reportar expiração, um detentor cujo
	// lease EXPIROU por ausência de heartbeat é fenced-out mesmo estando o seu token
	// ainda "corrente" (janela expirado-mas-não-superado) — fecha o fail-open de
	// [ErrLeaseExpired]. Só se aplica a token >= corrente (o caso < já foi rejeitado).
	if exp, ok := a.tokens.(LeaseExpiryAuthority); ok {
		expired, exists, eerr := exp.CurrentLeaseExpired(ctx, runID)
		if eerr != nil {
			return eventstore.AppendResult{}, eerr
		}
		if exists && expired {
			a.obs.Rejected(runID, token.Value(), current.Value())
			return eventstore.AppendResult{}, fmt.Errorf("%w: lease expirado por ausência de heartbeat (token=%d, run %s)",
				ErrStaleFencingToken, token.Value(), runID)
		}
	}

	res, err := a.store.Append(ctx, runID, in, opts...)
	if err != nil {
		return res, err
	}
	a.obs.Accepted(runID, token.Value())
	return res, nil
}
