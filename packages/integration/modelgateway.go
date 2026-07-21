// Package integration é o COMPOSITION ROOT (ápice) do AGENTICOS — o único módulo que
// depende dos pilares concretos e os compõe. Este ficheiro estabelece o composition
// root do MODEL GATEWAY: a montagem de produção que torna a soberania de dados de
// AOS-058 uma IMPOSIÇÃO no data-plane vivo, e não apenas um primitivo testado à parte.
//
// # O que este wiring garante (a dívida diferida de AOS-058, agora saldada)
//
//  1. A ALLOWLIST regional default-deny corre em TODO o tráfego real: o gateway é
//     montado com o estágio allowlist-regional REAL (via
//     [modelgateway.NewProduction], que activa a policy assinada/pinada com
//     [allowlist.LoadAndActivate] e sela a activação no changelog WORM) — NUNCA o
//     passthrough allow-by-default de AOS-055.
//  2. O FAILOVER só encaminha via a guarda de soberania (routing/sovereignty), sem
//     bypass: um failover cross-border é bloqueado fail-closed e selado como deny
//     atribuível — o bloqueio cross-border é imposto no data-plane vivo.
//  3. FAIL-CLOSED POR CONSTRUÇÃO: não existe caminho de produção que caia no
//     passthrough. Por isso o default global da biblioteca não é flipado (partiria o
//     baseline opt-in de AOS-055/056/057) — a segurança é garantida AQUI, no ápice.
//
// # Fronteira de escopo (honesta)
//
// O seam de IDENTIDADE (AOS-057) e a fonte de CREDENCIAIS (o vault de EPIC-07) entram
// por [ModelGatewayConfig] — o composition root liga aqui os concretos reais. O
// wiring completo do estágio de authn de AOS-057 e do vault permanece a dívida de
// integração já registada (ortogonal a AOS-058); este corte prova a imposição de
// soberania end-to-end sobre esses seams.
package integration

import (
	"context"
	"errors"
	"net/http"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// ErrNoAuditStore — o composition root exige um audit.Store concreto (WORM). É a
// única mutação-fronteira estável (AOS-011): produção liga storage append-only real.
var ErrNoAuditStore = errors.New("integration: composition root do model gateway exige um audit.Store (fail-closed)")

// ModelGatewayConfig é a configuração de ÁPICE da montagem do Model Gateway. Reúne os
// concretos que só o composition root conhece: o audit.Store WORM, a fonte de
// credenciais (vault/broker), o estágio de identidade (AOS-057) e a topologia de infra.
type ModelGatewayConfig struct {
	// Provider/BaseURL identificam o provedor default e o endpoint da sua API
	// compatível OpenAI. HTTPClient é opcional (nil ⇒ http.DefaultClient).
	Provider   string
	BaseURL    string
	HTTPClient *http.Client
	// DefaultRegion é a fronteira usada quando o pedido não especifica região.
	DefaultRegion string
	// Audit é o audit.Store WORM onde toda a governação de soberania sela. OBRIGATÓRIO.
	Audit audit.Store
	// Credentials é a fonte de credenciais de infra (o vault/broker de EPIC-07). O
	// composition root é a camada de confiança que a liga; o agente nunca a vê.
	Credentials modelgateway.CredentialProvider
	// Authn é o estágio de identidade de AOS-057 (resolve o principal atribuível). É o
	// seam onde o estágio real se liga; sem ele a montagem de produção falha fail-closed.
	Authn pipeline.Stage
	// Accounts é a topologia de infra (fonte única do keypool + inventário de failover).
	Accounts []modelgateway.InfraAccount
	// Health reporta a saúde de um endpoint (KeyID + região). Nil ⇒ todos saudáveis (a
	// fronteira de soberania é imposta independentemente da saúde).
	Health func(keyID, region string) bool
	// ActivatedAt é o instante selado no changelog WORM na activação da allowlist.
	ActivatedAt time.Time
	// Clock/Tracer/Variance são opcionais.
	Clock    func() time.Time
	Tracer   agentruntime.Tracer
	Variance modelgateway.VarianceSink
}

// AssembleModelGateway é o PONTO DE MONTAGEM DE PRODUÇÃO do Model Gateway no
// composition root. Compõe (não reimplementa) a costura fail-closed de
// [modelgateway.NewProduction]: activa a allowlist regional assinada/pinada, liga o
// router de failover que impõe a soberania e monta o gateway com identidade,
// credenciais e keypool reais. Devolve um gateway em que a allowlist default-deny e o
// bloqueio de failover cross-border correm em TODO o tráfego — nunca o passthrough.
//
// Fail-closed: sem audit.Store (ou se a activação da allowlist / a config de produção
// falharem), NÃO devolve gateway — o ápice recusa montar um data-plane não-soberano.
func AssembleModelGateway(ctx context.Context, cfg ModelGatewayConfig) (*modelgateway.Gateway, error) {
	if cfg.Audit == nil {
		return nil, ErrNoAuditStore
	}
	return modelgateway.NewProduction(ctx, modelgateway.ProductionConfig{
		Provider:      cfg.Provider,
		BaseURL:       cfg.BaseURL,
		HTTPClient:    cfg.HTTPClient,
		DefaultRegion: cfg.DefaultRegion,
		Audit:         cfg.Audit,
		Credentials:   cfg.Credentials,
		Authn:         cfg.Authn,
		Accounts:      cfg.Accounts,
		Health:        cfg.Health,
		ActivatedAt:   cfg.ActivatedAt,
		Clock:         cfg.Clock,
		Tracer:        cfg.Tracer,
		Variance:      cfg.Variance,
	})
}
