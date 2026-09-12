package main

// model_gateway_wiring.go — T2-B (AOS-391): liga o Model Gateway REAL à porta
// `decompose.Model` do planeador, no binário aos-orq (composition root, exempto ADR-018).
//
// O `--goal` sem `--decompose-fixture` passa a decompor via LLM vivo (endpoint
// OpenAI-compatível), sob a NHI verificada do run (que sela `model:invoke`) e a mediação
// do estágio authn REAL do gateway (cutover de identidade, família AOS-278). Mantém-se o
// `fixtureModel` como override de teste offline; sem fixture E sem gateway, recusa
// fail-closed (nenhum plano fantasma).
//
// ADR-020 (fidelidade do token): a invocação corre sob a NHI do RUN, que sela
// `model:invoke` e enraíza no humano (ADR-003). O ideal — a NHI própria `agent:planner` —
// exigiria o `planner.Planner` expor o seu token filho no ctx da decomposição (o
// `decompose.Model` corre lá dentro), o que é uma mudança no control-plane fora do âmbito
// do aos-orq; fica como residual declarado. O escopo é reconciliado fail-closed pelo
// estágio authn (efectivo = utilizador ∩ classe ∩ token.Scope, menor privilégio).
//
// ZERO-DEP externa: model-gateway + transitivos são `replace` path-local (offline).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	decompose "github.com/aos-ref/control-plane/orchestrator/decompose"
	audit "github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
	"github.com/aos-ref/platform/model-gateway/port"
)

// modelInvokeCapability é a capability que o token NHI tem de SELAR no escopo para invocar
// um modelo (casa com o token_policy.json embebido do estágio authn do gateway).
const modelInvokeCapability = "model:invoke"

// gatewayConfig é a configuração do Model Gateway lida do ambiente.
type gatewayConfig struct {
	endpoint    string
	model       string
	apiKeyPath  string
	region      string
	board       string
	egressHosts []string
	production  bool
}

// gatewayConfigFromEnv lê a config do Model Gateway do ambiente. Devolve (nil, nil) quando
// `AOS_MODEL_ENDPOINT` está ausente — não há gateway, e o `--goal` sem fixture recusa
// fail-closed a montante. Fail-closed em config parcial (endpoint sem modelo; produção sem
// credencial).
func gatewayConfigFromEnv() (*gatewayConfig, error) {
	endpoint := strings.TrimSpace(os.Getenv("AOS_MODEL_ENDPOINT"))
	if endpoint == "" {
		return nil, nil
	}
	model := strings.TrimSpace(os.Getenv("AOS_MODEL_NAME"))
	if model == "" {
		return nil, errors.New("AOS_MODEL_ENDPOINT definido sem AOS_MODEL_NAME: o par (endpoint, modelo) é obrigatório")
	}
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")
	apiKeyPath := strings.TrimSpace(os.Getenv("AOS_MODEL_API_KEY_PATH"))
	if production && apiKeyPath == "" {
		return nil, errors.New("produção exige AOS_MODEL_API_KEY_PATH (credencial de infra do modelo)")
	}
	region := strings.TrimSpace(os.Getenv("AOS_MODEL_REGION"))
	if region == "" {
		region = "eu"
	}
	board := strings.TrimSpace(os.Getenv("AOS_MODEL_BOARD"))
	if board == "" {
		board = "board-eu"
	}
	var egress []string
	if h := strings.TrimSpace(os.Getenv("AOS_MODEL_EGRESS_HOSTS")); h != "" {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				egress = append(egress, p)
			}
		}
	}
	return &gatewayConfig{endpoint: endpoint, model: model, apiKeyPath: apiKeyPath, region: region, board: board, egressHosts: egress, production: production}, nil
}

// staticCredencialModelo implementa [modelgateway.CredentialProvider]: devolve o segredo de
// infra lido de ficheiro. Fail-closed: segredo vazio é recusado.
type staticCredencialModelo struct{ secret string }

func (c staticCredencialModelo) Fetch(context.Context, string, string) (string, error) {
	if c.secret == "" {
		return "", errors.New("credencial de infra do modelo vazia")
	}
	return c.secret, nil
}

// autoridadeModelo é o [authn.AuthorityResolver] do estágio authn: CONCEDE `model:invoke` a
// qualquer principal VERIFICADO — a autoridade real vem do ESCOPO SELADO no token NHI, com
// que o estágio authn reconcilia (menor privilégio). Um token cujo escopo não sela
// `model:invoke` é negado atribuívelmente.
type autoridadeModelo struct{}

func (autoridadeModelo) UserAuthority(context.Context, string) ([]string, error) {
	return []string{modelInvokeCapability}, nil
}

func (autoridadeModelo) ClassAuthority(context.Context, string) ([]string, error) {
	return []string{modelInvokeCapability}, nil
}

// gatewayDecomposeModel adapta [port.Gateway] (Chat) à porta [decompose.Model] do
// planeador. Fala DIRECTO com `gw.Chat` passando system+user como duas mensagens — NÃO
// reutiliza o `ModelClientAdapter` (esse colapsa numa só mensagem user). O `Principal` é o
// token NHI que sela `model:invoke`; o estágio authn do gateway verifica-o fail-closed.
type gatewayDecomposeModel struct {
	gw        port.Gateway
	model     string
	region    string
	board     string
	principal string
}

// Complete satisfaz [decompose.Model]: invoca o gateway com o prompt system+user e devolve
// o texto da resposta (o decompositor extrai e valida o JSON a jusante — AOS-231).
func (m gatewayDecomposeModel) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := m.gw.Chat(ctx, port.ChatRequest{
		Model: m.model,
		Messages: []port.Message{
			{Role: port.RoleSystem, Content: system},
			{Role: port.RoleUser, Content: user},
		},
		Principal: m.principal, // token NHI do run que sela model:invoke
		Region:    m.region,
		Board:     m.board,
		// Sem Tools: o decompositor quer texto JSON, não tool_calls.
	})
	if err != nil {
		// Deny do authn (falta model:invoke / token inválido) ou falha do gateway propaga
		// fail-closed: o planeador não avança com um plano fantasma.
		return "", fmt.Errorf("model gateway: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("model gateway: resposta sem escolhas")
	}
	return resp.Choices[0].Message.Content, nil
}

// construirModeloGateway compõe o Model Gateway de produção e devolve-o adaptado à porta
// [decompose.Model], sob o `verifier` de identidade (o issuer efémero do run) e o
// `principal` (token do run que sela `model:invoke`). Fail-closed: sem verifier não há
// estágio authn e o gateway não se compõe.
func construirModeloGateway(ctx context.Context, cfg *gatewayConfig, verifier authn.Verifier, principal string) (decompose.Model, error) {
	if cfg == nil {
		return nil, errors.New("config do Model Gateway nil")
	}
	if verifier == nil {
		return nil, errors.New("verifier de identidade nil — model:invoke não seria verificável (cutover AOS-278)")
	}
	pol, err := authn.LoadPolicy()
	if err != nil {
		return nil, fmt.Errorf("policy do estágio authn: %w", err)
	}
	authnStage := authn.New(verifier, autoridadeModelo{}, pol)

	base := strings.TrimRight(cfg.endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	var secret string
	if cfg.apiKeyPath != "" {
		raw, rerr := os.ReadFile(cfg.apiKeyPath)
		if rerr != nil {
			return nil, fmt.Errorf("credencial de modelo %q: %w", cfg.apiKeyPath, rerr)
		}
		secret = strings.TrimSpace(string(raw))
	}

	gwCfg := modelgateway.ProductionConfig{
		Provider:      "openai",
		BaseURL:       base,
		DefaultRegion: cfg.region,
		Authn:         authnStage,
		Audit:         audit.NewMemStore(),
		Credentials:   staticCredencialModelo{secret: secret},
		Accounts:      []modelgateway.InfraAccount{{KeyID: "model-upstream", Provider: "openai", Region: cfg.region}},
	}
	if cfg.production {
		// Egress REAL endurecido (SSRF fail-closed, AOS-223): HTTPClient nil + allowlist.
		gwCfg.AllowedEgressHosts = cfg.egressHosts
	} else {
		// Seam de dev: transporte injectado governa o egress (aponta a endpoints internos).
		gwCfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}

	gw, err := modelgateway.NewProduction(ctx, gwCfg)
	if err != nil {
		return nil, fmt.Errorf("compor o Model Gateway: %w", err)
	}
	return gatewayDecomposeModel{gw: gw, model: cfg.model, region: cfg.region, board: cfg.board, principal: principal}, nil
}
