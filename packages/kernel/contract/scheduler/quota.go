package scheduler

import (
	"context"
	"time"
)

// ProviderKey identifica a dimensão sobre a qual o rate limit do provider é
// partilhado: um bucket por provider:model:region. É a chave do stream do bucket
// no Event Store.
type ProviderKey struct {
	Provider string
	Model    string
	Region   string
}

// String devolve a forma canónica "provider:model:region", usada como chave de
// stream e nos eventos. É estável (serialização determinística para replay).
func (k ProviderKey) String() string {
	return k.Provider + ":" + k.Model + ":" + k.Region
}

// ProviderLimits são os limites REAIS do provider para uma [ProviderKey]:
// tokens-por-minuto (TPM), requests-por-minuto (RPM) e a janela real de
// reposição. O token-bucket distribuído é parametrizado por estes valores; a
// soma das reservas activas dentro de Window nunca excede TPM/RPM.
type ProviderLimits struct {
	// TPM é o tecto de tokens admitidos por janela (tokens-por-minuto).
	TPM int64
	// RPM é o tecto de requests admitidos por janela (requests-por-minuto).
	RPM int64
	// Window é a janela real de reposição do provider (tipicamente 1 minuto).
	// Uma reserva é contabilizada enquanto a sua idade for < Window; findo esse
	// prazo, o refill temporizado liberta-a implicitamente.
	Window time.Duration
}

// QuotaProvider é a PORTA que devolve os limites reais de um provider/modelo/
// região. O Model Gateway (EPIC-06) é o implementador de produção; NÃO é
// implementado aqui. A admissão lê SEMPRE os limites por esta porta — nunca de
// constantes locais.
type QuotaProvider interface {
	// Limits devolve os limites globais efectivos para a chave. Um erro impede a
	// admissão (fail-closed: sem limites conhecidos, não se reserva quota).
	Limits(ctx context.Context, key ProviderKey) (ProviderLimits, error)
}

// TenantQuotaProvider é uma PORTA OPCIONAL: se o [QuotaProvider] a implementar, a
// admissão aplica também um tecto por tenant (quota multidimensional) SOBRE o
// bucket global. O tecto global domina SEMPRE — um tenant nunca excede o global,
// mesmo com headroom na sua partição.
type TenantQuotaProvider interface {
	// TenantLimits devolve o tecto do tenant para a chave e se existe um tecto
	// definido (ok=false ⇒ tenant sem cap próprio, só limitado pelo global).
	TenantLimits(ctx context.Context, key ProviderKey, tenant string) (ProviderLimits, bool, error)
}

// CostEstimator estima o custo em tokens de um trabalho antes da admissão. É uma
// PORTA injectável: a heurística/histórico real liga-se a EPIC-06/08; aqui um
// estimador simples é suficiente. O custo é sempre >= 1 request.
type CostEstimator interface {
	// EstimateTokens devolve o custo previsto em tokens do pedido de admissão.
	EstimateTokens(req AdmitRequest) int64
}

// AdmitRequest descreve um pedido de admissão ao admission control global.
type AdmitRequest struct {
	Key             ProviderKey
	Tenant          string
	Board           string
	EstimatedTokens int64
}

// AdmitResponse é o veredicto de uma admissão.
type AdmitResponse struct {
	Granted          bool
	Rejected         bool
	ReservationID    string
	RetryAfter       time.Duration
	HeadroomTokens   int64
	HeadroomRequests int64
}

// Admission é a PORTA do admission control global. A implementação vive no
// control-plane/scheduler; os adaptadores finos do data-plane usam esta interface.
type Admission interface {
	Admit(ctx context.Context, req AdmitRequest) (AdmitResponse, error)
}
