package main

// Liga o MODEL GATEWAY REAL (EPIC-06, packages/platform/model-gateway) à porta [Config.Model] do
// nó, apontado a um endpoint OpenAI-compatível (OmniRoute/OpenRouter/…). NÃO duplica o cliente
// OpenAI: reutiliza o adaptador interno do gateway (internal/adapters/openai_http) e o
// [modelgateway.NewModelClient] canónico (RT→GW). O gateway traz a sua allowlist regional ASSINADA
// (go:embed, trust anchor pinado), keypool, routing de failover, metering/pricing e o endurecimento
// SSRF — tudo o que um cliente à parte não teria.
//
// EGRESS EM DEV: injecta-se um [http.Client] simples, o que faz o gateway DELEGAR a validação de
// BaseURL nesse transporte (o mesmo seam que os testes de integração usam para apontar a um
// httptest). Assim o nó fala com o OmniRoute em http na rede interna sem o bloqueio https+allowlist
// do caminho de egress real. Em produção remove-se o HTTPClient e usa-se BaseURL https +
// AllowedEgressHosts (o SSRF fail-closed de AOS-223 volta a valer).
//
// ZERO-DEP preservado: o model-gateway já está no grafo do nó (replace local) e não traz nenhuma
// dependência EXTERNA além do que o nó já tem.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// modelGatewayRegion / modelGatewayBoard têm de constar da allowlist regional ASSINADA embebida no
// model-gateway (policy/allowlist/allowlist_policy.json, trust anchor PINADO — não re-assinável
// aqui). A regra `board-eu` permite os modelos {gpt-4o, gpt-4o-mini, text-embedding-3-large} nas
// regiões {eu, eu-west}. CONSEQUÊNCIA: o AOS_MODEL_NAME pedido ao gateway tem de ser um destes
// (o gateway NEGA fail-closed um modelo fora da allowlist) — o upstream (OmniRoute) é que mapeia
// esse nome canónico para o provider concreto.
const (
	modelGatewayRegion = "eu"
	modelGatewayBoard  = "board-eu"
)

// staticModelCredential implementa [modelgateway.CredentialProvider]: devolve o segredo de infra
// (o bearer para o gateway upstream) por (provider, região). Em produção o composition root liga
// aqui o vault/broker (EPIC-07); aqui é o valor lido de AOS_MODEL_API_KEY_PATH. OmniRoute em dev
// pode não exigir bearer — devolve-se um valor não-vazio para o keypool não falhar; o upstream
// ignora-o se não o exigir.
type staticModelCredential struct{ secret string }

func (c staticModelCredential) Fetch(_ context.Context, _, _ string) (string, error) {
	if c.secret == "" {
		return "aos-dev-omniroute", nil
	}
	return c.secret, nil
}

// nodeModelAuthn implementa [pipeline.Stage] — o seam de IDENTIDADE (AOS-057) que torna as decisões
// de soberania do gateway atribuíveis a um principal. Em produção liga-se aqui o estágio real
// (identity.Verifier + policy); no nó de referência resolve um principal determinista quando o
// pedido não traz um. É o gémeo do estágio de teste do model-gateway.
type nodeModelAuthn struct{}

func (nodeModelAuthn) Name() string { return "auth-principal" }

func (nodeModelAuthn) Process(_ context.Context, ex *pipeline.Exchange) error {
	if strings.TrimSpace(ex.Principal) == "" {
		ex.PrincipalUser = "aos-node"
		ex.PrincipalAgent = "aos-agent"
		ex.AgentClass = "reader"
		ex.HumanRoot = "aos-node"
		ex.Principal = "aos-node/aos-agent"
	}
	ex.Record("auth-principal", "allow", "identidade de referencia do no (seam AOS-057)")
	return nil
}

// newGatewayModelClient compõe o [modelgateway.Gateway] de produção apontado ao endpoint dado e
// devolve-o já adaptado à porta [agentruntime.ModelClient] via [modelgateway.NewModelClient].
func newGatewayModelClient(baseURL, model, apiKeyPath string) (agentruntime.ModelClient, error) {
	secret := ""
	if apiKeyPath != "" {
		raw, err := os.ReadFile(apiKeyPath)
		if err != nil {
			return nil, fmt.Errorf("%w: ler API key: %v", ErrBadModelConfig, err)
		}
		secret = strings.TrimSpace(string(raw))
		if secret == "" {
			return nil, fmt.Errorf("%w: API key vazia em %q", ErrBadModelConfig, apiKeyPath)
		}
	}
	// BaseURL = raiz OpenAI-compatível (termina em /v1); o adaptador acrescenta /chat/completions.
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	gw, err := modelgateway.NewProduction(context.Background(), modelgateway.ProductionConfig{
		Provider:      "openai",
		BaseURL:       base,
		HTTPClient:    &http.Client{Timeout: 60 * time.Second}, // seam de dev: delega validação de egress
		DefaultRegion: modelGatewayRegion,
		Audit:         audit.NewMemStore(), // audit de governação do GW (activação da allowlist + decisões)
		Credentials:   staticModelCredential{secret: secret},
		Accounts: []modelgateway.InfraAccount{{
			KeyID: "omniroute", Provider: "openai", Region: modelGatewayRegion, LimitRPM: 120, LimitTPM: 200_000,
		}},
		Authn: nodeModelAuthn{},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadModelConfig, err)
	}
	return modelgateway.NewModelClient(gw, model, modelgateway.WithRegionBoard(modelGatewayRegion, modelGatewayBoard)), nil
}
