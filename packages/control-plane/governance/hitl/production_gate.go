package hitl

import (
	"errors"
	"time"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Sentinelas da via sancionada de produção do gate de ratificação (AOS-159).
var (
	// ErrNoNonceStore — [NewProductionRatificationGate] recebeu um nonce-store nil. Sem
	// uso-único durável a mesma ratificação assinada re-promoveria N vezes; recusado.
	ErrNoNonceStore = errors.New("hitl: produção exige um RatificationNonceStore durável (uso-único anti-replay)")
	// ErrNoFreshness — ttl <= 0 deixaria a frescura desactivada; uma ratificação antiga
	// promoveria indefinidamente. Produção exige uma janela temporal; recusado.
	ErrNoFreshness = errors.New("hitl: produção exige uma janela de frescura (ttl > 0) para a ratificação")
)

// NewProductionRatificationGate é a via SANCIONADA de produção do gate de ratificação:
// como [NewRatificationGate], mas FORÇA o endurecimento anti-replay de ADR-012/AOS-159
// em vez de o deixar opcional — freshness (janela temporal) MAIS um nonce-store DURÁVEL
// (uso-único que sobrevive a um restart). Fecha o buraco de uma ratificação assinada
// (identidade estável de conteúdo) re-promover N vezes, inclusive depois de um rollback.
//
// Fail-closed na construção: um nonce-store nil ([ErrNoNonceStore]) ou ttl <= 0
// ([ErrNoFreshness]) são recusados. As opções [WithRatifyFreshness]/[WithRatifyNonceStore]
// são aplicadas por ÚLTIMO, pelo que um `opts` do chamador NÃO as pode desactivar — o
// endurecimento é imposto por construção, não por convenção. É o ponto onde os ports
// anti-replay ficam SEMPRE ligados; o controlador de promoção que compuser o gate de
// auto-modificação deve usar esta via (não [NewRatificationGate] cru).
func NewProductionRatificationGate(
	eval otelgenai.EvalGate,
	registry ApproverRegistry,
	sealer audit.Store,
	nonces RatificationNonceStore,
	ttl, skew time.Duration,
	opts ...RatificationOption,
) (*RatificationGate, error) {
	if nonces == nil {
		return nil, ErrNoNonceStore
	}
	if ttl <= 0 {
		return nil, ErrNoFreshness
	}
	// opts primeiro, o endurecimento por último: freshness+nonce vencem sempre.
	all := make([]RatificationOption, 0, len(opts)+2)
	all = append(all, opts...)
	all = append(all, WithRatifyFreshness(ttl, skew), WithRatifyNonceStore(nonces))
	return NewRatificationGate(eval, registry, sealer, all...)
}
