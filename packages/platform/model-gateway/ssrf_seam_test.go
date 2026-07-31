package modelgateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-223 — teste falsificável do endurecimento SSRF NO SEAM: prova que a validação
// de BaseURL está REALMENTE ligada em NewProduction (não só nos helpers). Complementa
// ssrf_internal_test.go (que exerce os helpers white-box): aqui a fronteira é o
// construtor público, com HTTPClient == nil (o caminho de egress REAL onde o gateway
// constrói o seu próprio transporte e valida o BaseURL contra AllowedEgressHosts).
//
// Falha-antes: antes do fix, newProviderAdapter aceitava qualquer BaseURL (sem
// validação de esquema/host), pelo que NewProduction com um BaseURL malicioso
// DEVOLVIA um gateway (sem erro) — cada asserção errors.Is(err, ...) abaixo falharia.

// ssrfSeamConfig monta uma ProductionConfig de egress REAL (HTTPClient == nil) com a
// allowlist de egress dada. Reusa os seams deterministas de production_test.go
// (prodAuthn, testCreds) para isolar o que se testa: a validação do BaseURL.
func ssrfSeamConfig(store audit.Store, baseURL string, allowed []string) modelgateway.ProductionConfig {
	cfg := prodConfig(store, baseURL, nil, // nil client => caminho de egress real (valida BaseURL)
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}})
	cfg.AllowedEgressHosts = allowed
	return cfg
}

// TestNewProduction_MaliciousBaseURL_RefusedAtSeam prova que um BaseURL malicioso é
// RECUSADO por NewProduction fail-closed (nenhum gateway é devolvido) no caminho de
// egress real. Cobre esquema não-https, metadados de nuvem, host fora da allowlist e
// allowlist vazia (nega tudo). Falsificável: cada caso exige o sentinela certo E um
// gateway nil.
func TestNewProduction_MaliciousBaseURL_RefusedAtSeam(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		baseURL string
		allowed []string
		want    error
	}{
		{"http_recusado", "http://api.provider.internal/v1", []string{"api.provider.internal"}, modelgateway.ErrInsecureBaseURL},
		{"metadata_cloud_recusado", "http://169.254.169.254/latest/meta-data", []string{"169.254.169.254"}, modelgateway.ErrInsecureBaseURL},
		{"host_fora_allowlist_recusado", "https://evil.example.com/v1", []string{"api.provider.internal"}, modelgateway.ErrHostNotAllowed},
		{"allowlist_vazia_nega_tudo", "https://api.provider.internal/v1", nil, modelgateway.ErrHostNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gw, err := modelgateway.NewProduction(context.Background(), ssrfSeamConfig(audit.NewMemStore(), tc.baseURL, tc.allowed))
			if !errors.Is(err, tc.want) {
				t.Fatalf("BaseURL %q devia ser recusado com %v; got %v", tc.baseURL, tc.want, err)
			}
			if gw != nil {
				t.Fatalf("BaseURL malicioso NAO devia devolver gateway (fail-closed); got %v", gw)
			}
		})
	}
}

// TestNewProduction_LegitBaseURL_ConstructsAtSeam prova que um BaseURL LEGÍTIMO (https,
// host na allowlist) atravessa a validação e NewProduction constrói o gateway no
// caminho de egress real — o endurecimento não parte a configuração são. Par de dois
// sentidos com o teste acima (um caso legítimo TEM de passar).
func TestNewProduction_LegitBaseURL_ConstructsAtSeam(t *testing.T) {
	t.Parallel()
	gw, err := modelgateway.NewProduction(context.Background(),
		ssrfSeamConfig(audit.NewMemStore(), "https://api.provider.internal/v1", []string{"api.provider.internal"}))
	if err != nil {
		t.Fatalf("BaseURL https allowlisted devia construir; got %v", err)
	}
	if gw == nil {
		t.Fatal("gateway legítimo devia ser não-nil")
	}
}
