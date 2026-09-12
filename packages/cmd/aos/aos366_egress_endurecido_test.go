package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-366 — o nó injectava um `http.Client` INCONDICIONALMENTE no model gateway, o que fazia o
// gateway DELEGAR nesse transporte e saltar o caminho endurecido (validateEgressURL: https +
// allowlist + re-validação de cada redirect, o SSRF fail-closed de AOS-223) em QUALQUER
// configuração, produção incluída — na única perna de egress paga do nó. O comentário na linha da
// injecção dizia «seam de dev» dentro do binário que serve produção; o ramo de produção que o
// cabeçalho prometia não existia.
//
// A correcção arma o caminho endurecido sob AOS_MODE=production (HTTPClient nil + AllowedEgressHosts)
// e mantém o seam de dev fora de produção. Estes testes fixam os dois sentidos.

// aos366ProdModelEnv monta uma superfície de produção com o gateway LIGADO e a credencial montada
// (para passar o gate de AOS-247 e chegar ao egress). Deixa AOS_MODEL_ENDPOINT ao chamador.
func aos366ProdModelEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	cred := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(cred, []byte("dev-secret"), 0o600); err != nil {
		t.Fatalf("escrever credencial de teste: %v", err)
	}
	t.Setenv("AOS_MODEL_API_KEY_PATH", cred)
	t.Setenv("AOS_MODEL_EGRESS_HOSTS", "") // por omissão a allowlist deriva do host do endpoint
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
}

// TestAOS366_ProducaoHTTPRecusa (CA-2, metade do esquema): em produção um AOS_MODEL_ENDPOINT em
// http:// é RECUSADO na construção — o erro de validateEgressURL propaga-se (duplo %w) até quem
// compõe. Antes da correcção este endpoint compunha em silêncio, com a validação SSRF fora do
// caminho.
func TestAOS366_ProducaoHTTPRecusa(t *testing.T) {
	aos366ProdModelEnv(t)
	t.Setenv("AOS_MODEL_ENDPOINT", "http://gw.interno:4000/v1")

	_, _, err := parseModelFromEnv(true)
	if !errors.Is(err, modelgateway.ErrInsecureBaseURL) {
		t.Fatalf("producao com egress http devia recusar com ErrInsecureBaseURL, veio: %v", err)
	}
}

// TestAOS366_ProducaoHostForaDaAllowlistRecusa (CA-2, metade do host): com AOS_MODEL_EGRESS_HOSTS a
// apontar um host DIFERENTE do endpoint, o BaseURL fica fora da allowlist e a construção recusa com
// ErrHostNotAllowed. Prova que a allowlist é REALMENTE consultada, e não decorativa.
func TestAOS366_ProducaoHostForaDaAllowlistRecusa(t *testing.T) {
	aos366ProdModelEnv(t)
	t.Setenv("AOS_MODEL_ENDPOINT", "https://gw.interno/v1")
	t.Setenv("AOS_MODEL_EGRESS_HOSTS", "outro.host.interno") // não contém gw.interno

	_, _, err := parseModelFromEnv(true)
	if !errors.Is(err, modelgateway.ErrHostNotAllowed) {
		t.Fatalf("producao com host fora da allowlist devia recusar com ErrHostNotAllowed, veio: %v", err)
	}
}

// TestAOS366_ProducaoHTTPSNaAllowlistCompoe (metade positiva): produção com https e a allowlist
// derivada do próprio host do endpoint COMPÕE. A construção só valida o BaseURL — não dispara
// request —, logo um NewTLSServer basta e não é preciso confiar no cert.
func TestAOS366_ProducaoHTTPSNaAllowlistCompoe(t *testing.T) {
	aos366ProdModelEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL+"/v1")

	mc, _, err := parseModelFromEnv(true)
	if err != nil {
		t.Fatalf("producao com https na allowlist (derivada do endpoint) devia compor, veio: %v", err)
	}
	if mc == nil {
		t.Fatal("o cliente de modelo devia estar composto")
	}
}

// TestAOS366_ProducaoArmaOTransporteEndurecido (CA-3): o MESMO endpoint http que a produção RECUSA
// é ACEITE fora de produção. O único diferenciador é o ramo `if client == nil` do gateway
// (production.go): sob produção o nó deixa o HTTPClient nil e o gateway constrói o transporte
// ENDURECIDO — timeout egressTimeout (30 s, production.go), limite de redirects e re-validação de
// cada salto —, em vez do `http.Client{60 s}` injectado que delega tudo. A recusa vs. aceitação do
// mesmo URL prova que os transportes são distintos; a medição do timeout de 30 s em si está no
// white-box do gateway (TestHardenedEgressClient_LegitHTTPS_Works).
func TestAOS366_ProducaoArmaOTransporteEndurecido(t *testing.T) {
	const endpoint = "http://gw.interno:4000/v1"

	// dev: o seam injectado delega — http compõe.
	t.Run("dev_aceita_http", func(t *testing.T) {
		aos366ProdModelEnv(t)
		t.Setenv("AOS_MODEL_ENDPOINT", endpoint)
		mc, _, err := parseModelFromEnv(false)
		if err != nil {
			t.Fatalf("fora de producao o seam de dev tem de compor http, veio: %v", err)
		}
		if mc == nil {
			t.Fatal("dev: o cliente devia estar composto")
		}
	})

	// produção: o mesmo http é recusado — logo o transporte NÃO é o injectado.
	t.Run("producao_recusa_http", func(t *testing.T) {
		aos366ProdModelEnv(t)
		t.Setenv("AOS_MODEL_ENDPOINT", endpoint)
		if _, _, err := parseModelFromEnv(true); !errors.Is(err, modelgateway.ErrInsecureBaseURL) {
			t.Fatalf("producao devia armar o caminho endurecido e recusar http, veio: %v", err)
		}
	})
}

// TestAOS366_ForaDeProducaoOSeamHTTPContinua (CA-4, controlo negativo): o cenário legítimo de dev —
// o gateway apontado a um httptest http, como o dev-hardened faz com o LiteLLM interno — continua a
// compor sem alteração. Uma correcção que partisse isto custaria mais do que o defeito.
func TestAOS366_ForaDeProducaoOSeamHTTPContinua(t *testing.T) {
	aos366ProdModelEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL+"/v1")

	mc, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("fora de producao o seam de httptest http tem de continuar a funcionar, veio: %v", err)
	}
	if mc == nil {
		t.Fatal("o cliente de modelo de dev devia estar composto")
	}
}

// TestAOS366_ProducaoEndpointSemHostRecusa fixa o fail-closed da derivação da allowlist: em
// produção, sem AOS_MODEL_EGRESS_HOSTS e com um endpoint sem host parseável, a composição recusa em
// vez de compor uma allowlist vazia (que negaria tudo a jusante com um erro menos claro).
func TestAOS366_ProducaoEndpointSemHostRecusa(t *testing.T) {
	aos366ProdModelEnv(t)
	t.Setenv("AOS_MODEL_ENDPOINT", "http:///v1") // sem host

	if _, _, err := parseModelFromEnv(true); err == nil {
		t.Fatal("producao com endpoint sem host devia recusar, veio nil")
	}
}

// TestAOS366_IPv6DerivaHostQueCasa é a anti-recorrência do achado F1 da revisão adversarial: a
// allowlist derivada tem de casar o acessor (u.Hostname()) que validateEgressURL compara. Um
// literal IPv6 — com e SEM porta explícita — tem de COMPOR (não recusar): se a derivação usasse
// u.Host, o IPv6 sem porta guardava a entrada com `[...]` e a validação, que usa Hostname() sem
// brackets, não casava — recusando o arranque de um endpoint legítimo.
func TestAOS366_IPv6DerivaHostQueCasa(t *testing.T) {
	casos := []string{
		"https://[2001:db8::1]/v1",      // sem porta (443 implícita) — o caso que F1 reabria
		"https://[2001:db8::1]:4000/v1", // com porta não-default
	}
	for _, endpoint := range casos {
		t.Run(endpoint, func(t *testing.T) {
			aos366ProdModelEnv(t)
			t.Setenv("AOS_MODEL_ENDPOINT", endpoint)

			mc, _, err := parseModelFromEnv(true)
			if err != nil {
				t.Fatalf("endpoint IPv6 legitimo (%s) devia compor em producao, veio: %v", endpoint, err)
			}
			if mc == nil {
				t.Fatal("o cliente de modelo devia estar composto")
			}
		})
	}
}

// TestAOS366_EgressHostsSoVirgulasCaiNoEndpoint (F3 da revisão): AOS_MODEL_EGRESS_HOSTS com só
// vírgulas/espaços colapsa para vazio e deve cair no fallback do endpoint — não recusar com uma
// allowlist vazia. Aqui o endpoint https está na allowlist derivada, logo COMPÕE.
func TestAOS366_EgressHostsSoVirgulasCaiNoEndpoint(t *testing.T) {
	aos366ProdModelEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL+"/v1")
	t.Setenv("AOS_MODEL_EGRESS_HOSTS", " , ") // lixo que colapsa para vazio

	mc, _, err := parseModelFromEnv(true)
	if err != nil {
		t.Fatalf("AOS_MODEL_EGRESS_HOSTS so-virgulas devia cair no fallback do endpoint e compor, veio: %v", err)
	}
	if mc == nil {
		t.Fatal("o cliente de modelo devia estar composto")
	}
}
