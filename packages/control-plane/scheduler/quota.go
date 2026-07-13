package scheduler

// quota.go — a PORTA de quotas (AOS-027). Os limites de admissão do token-bucket
// distribuído NÃO são constantes locais: derivam do TPM/RPM REAL por
// provider:model:region, lidos de uma PORTA [QuotaProvider]. O Model Gateway
// (EPIC-06) implementá-la-á sobre os limites reais do provider; aqui fornecemos
// uma impl de referência determinística ([StaticQuotaProvider]), à imagem do
// ModelClient port do AOS-013. NUNCA se usa uma constante hard-coded como fonte
// de verdade dos limites.

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

// StaticQuotaProvider é a impl de referência determinística da [QuotaProvider]
// (e da [TenantQuotaProvider]). Substitui o Model Gateway em testes e no
// arranque: um mapa fixo de limites por chave, com um limite por omissão. É
// determinística (sem I/O, sem relógio) — segura para replay.
type StaticQuotaProvider struct {
	// byKey são limites globais explícitos por provider:model:region.
	byKey map[string]ProviderLimits
	// def é o limite global por omissão (chave ausente).
	def ProviderLimits
	// tenants são tectos por chave→tenant. Ausência ⇒ tenant sem cap próprio.
	tenants map[string]map[string]ProviderLimits
}

// NewStaticQuotaProvider constrói a impl de referência com um limite global por
// omissão. Use [StaticQuotaProvider.SetKey] / [StaticQuotaProvider.SetTenant]
// para afinar por chave/tenant.
func NewStaticQuotaProvider(def ProviderLimits) *StaticQuotaProvider {
	return &StaticQuotaProvider{
		byKey:   make(map[string]ProviderLimits),
		def:     def,
		tenants: make(map[string]map[string]ProviderLimits),
	}
}

// SetKey fixa os limites globais de uma chave específica.
func (p *StaticQuotaProvider) SetKey(key ProviderKey, lim ProviderLimits) *StaticQuotaProvider {
	p.byKey[key.String()] = lim
	return p
}

// SetTenant fixa o tecto de um tenant sobre uma chave (quota multidimensional).
func (p *StaticQuotaProvider) SetTenant(key ProviderKey, tenant string, lim ProviderLimits) *StaticQuotaProvider {
	m := p.tenants[key.String()]
	if m == nil {
		m = make(map[string]ProviderLimits)
		p.tenants[key.String()] = m
	}
	m[tenant] = lim
	return p
}

// Limits implementa [QuotaProvider].
func (p *StaticQuotaProvider) Limits(_ context.Context, key ProviderKey) (ProviderLimits, error) {
	if lim, ok := p.byKey[key.String()]; ok {
		return lim, nil
	}
	return p.def, nil
}

// TenantLimits implementa [TenantQuotaProvider].
func (p *StaticQuotaProvider) TenantLimits(_ context.Context, key ProviderKey, tenant string) (ProviderLimits, bool, error) {
	if m, ok := p.tenants[key.String()]; ok {
		if lim, ok := m[tenant]; ok {
			return lim, true, nil
		}
	}
	return ProviderLimits{}, false, nil
}

// CostEstimator estima o custo em tokens de um trabalho antes da admissão. É uma
// PORTA injectável: a heurística/histórico real liga-se a EPIC-06/08; aqui um
// estimador simples é suficiente. O custo é sempre >= 1 request.
type CostEstimator interface {
	// EstimateTokens devolve o custo previsto em tokens do pedido de admissão.
	EstimateTokens(req AdmitRequest) int64
}

// FixedCostEstimator devolve sempre o mesmo custo em tokens (determinístico).
// Se o pedido trouxer EstimatedTokens > 0, esse valor tem precedência (ver
// [Admission.Admit]); o estimador só é consultado quando o pedido não o fixa.
type FixedCostEstimator struct {
	Tokens int64
}

// EstimateTokens implementa [CostEstimator].
func (e FixedCostEstimator) EstimateTokens(_ AdmitRequest) int64 { return e.Tokens }
