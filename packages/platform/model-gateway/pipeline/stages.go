package pipeline

import "context"

// Este ficheiro reúne as implementações de referência PASS-THROUGH dos cinco
// estágios. Cada uma é o mínimo que fecha o contrato do estágio SEM implementar a
// lógica do ticket dono (que a substitui). São deterministas e sem I/O.

// PassthroughAuth é o estágio de auth-principal de referência (AOS-057 substitui).
// Não valida o token (isso é AOS-057): apenas propaga o principal para o rasto,
// deixando o ponto de extensão pronto. Um principal vazio é aceite nesta fase
// (a recusa fail-closed por token inválido/expirado entra com AOS-057).
type PassthroughAuth struct{}

// Name implementa [Stage].
func (PassthroughAuth) Name() string { return "auth-principal" }

// Process implementa [Stage].
func (PassthroughAuth) Process(_ context.Context, ex *Exchange) error {
	ex.record("auth-principal", "pass", "passthrough (AOS-057)")
	return nil
}

// PassthroughAllowlist é o estágio de allowlist regional de referência (AOS-058
// substitui). Permite tudo nesta fase (default-ALLOW pass-through); o
// default-deny concreto e o bloqueio de failover cross-border são AOS-058. É,
// ainda assim, o ponto onde uma recusa FALHA-FECHA a chamada (a mecânica está
// provada por [DenyStage] nos testes).
type PassthroughAllowlist struct{}

// Name implementa [Stage].
func (PassthroughAllowlist) Name() string { return "allowlist-regional" }

// Process implementa [Stage].
func (PassthroughAllowlist) Process(_ context.Context, ex *Exchange) error {
	ex.record("allowlist-regional", "allow", "passthrough (AOS-058)")
	return nil
}

// IdentityRouting é o estágio de roteamento de referência (AOS-059 substitui).
// Resolve o modelo/região PEDIDOS para si próprios (identidade) — não há swap
// nem escolha de tier nesta fase. O provider resolvido é deixado vazio: o GW
// preenche-o com o provider do adaptador configurado. AOS-059 substitui por
// roteamento cost/load-aware que pode escolher outro modelo (=> variância).
type IdentityRouting struct{}

// Name implementa [Stage].
func (IdentityRouting) Name() string { return "roteamento" }

// Process implementa [Stage].
func (IdentityRouting) Process(_ context.Context, ex *Exchange) error {
	ex.ResolvedModel = ex.RequestedModel
	ex.ResolvedRegion = ex.RequestedRegion
	ex.record("roteamento", "identity", "passthrough (AOS-059)")
	return nil
}

// PassthroughCacheLayout é o estágio de validação de layout de cache de
// referência (AOS-060 substitui). Não valida byte-identidade do prefixo nesta
// fase; deixa o ponto de extensão pronto.
type PassthroughCacheLayout struct{}

// Name implementa [Stage].
func (PassthroughCacheLayout) Name() string { return "cache-layout" }

// Process implementa [Stage].
func (PassthroughCacheLayout) Process(_ context.Context, ex *Exchange) error {
	ex.record("cache-layout", "pass", "passthrough (AOS-060)")
	return nil
}

// PassthroughMetering é o estágio de metering de referência (AOS-062 substitui).
// Corre DEPOIS da invocação do provider: o [Exchange.Usage] já está preenchido.
// Nesta fase só regista os tokens no rasto; o custo em USD é AOS-062.
type PassthroughMetering struct{}

// Name implementa [Stage].
func (PassthroughMetering) Name() string { return "metering" }

// Process implementa [Stage].
func (PassthroughMetering) Process(_ context.Context, ex *Exchange) error {
	ex.record("metering", "recorded", "passthrough (AOS-062)")
	return nil
}
