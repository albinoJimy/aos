package modelgateway

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// AOS-223 — testes falsificáveis do endurecimento SSRF/transport do seam do Model
// Gateway (white-box: exercem os helpers de egress do pacote). O par com
// ssrf_seam_test.go (que exerce NewProduction fim-a-fim) prova os dois defeitos:
//   (a) http.DefaultClient nu (sem timeout/TLS/limite de redirect);
//   (b) BaseURL sem validação (https + allowlist).

// TestValidateEgressURL_FailClosed prova a política de validação de egress: só um
// URL https com host na allowlist é aceite; http, host fora da allowlist, host
// vazio e URL inválido são RECUSADOS fail-closed. Falsificável em dois sentidos:
// cada caso legítimo tem de devolver nil e cada caso malicioso o sentinela certo.
func TestValidateEgressURL_FailClosed(t *testing.T) {
	t.Parallel()
	allow := newHostAllowlist([]string{"api.provider.internal", "127.0.0.1", "127.0.0.1:8443"})

	cases := []struct {
		name string
		raw  string
		want error // nil = aceite
	}{
		{"https_allowlisted_ok", "https://api.provider.internal/v1", nil},
		{"https_allowlisted_porta_explicita_ok", "https://127.0.0.1:8443/v1", nil},
		{"https_allowlisted_porta443_explicita_ok", "https://api.provider.internal:443/v1", nil},
		{"http_recusado", "http://api.provider.internal/v1", ErrInsecureBaseURL},
		{"http_metadata_recusado", "http://169.254.169.254/latest/meta-data", ErrInsecureBaseURL},
		{"esquema_file_recusado", "file:///etc/passwd", ErrInsecureBaseURL},
		{"https_fora_da_allowlist_recusado", "https://evil.example.com/v1", ErrHostNotAllowed},
		{"https_sem_host_recusado", "https:///v1", ErrHostNotAllowed},
		{"url_invalido_recusado", "https://exa mple.com/v1", ErrInsecureBaseURL},
		// Endurecimento de porta (SSRF, defeito residual de AOS-223): uma entrada NUA
		// ("api.provider.internal") permite só a porta https default; um desvio para uma
		// porta interna (SSH/admin) do MESMO host allowlisted é recusado fail-closed.
		{"host_allowlisted_porta_perigosa_recusado", "https://api.provider.internal:22/", ErrHostNotAllowed},
		{"host_allowlisted_porta_nao_default_recusado", "https://api.provider.internal:8443/", ErrHostNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEgressURL(tc.raw, allow)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("%q devia ser aceite; got %v", tc.raw, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%q: want %v; got %v", tc.raw, tc.want, err)
			}
		})
	}
}

// TestValidateEgressURL_EmptyAllowlist_DenyAll prova o fail-closed do conjunto
// vazio: sem allowlist, NENHUM host é permitido (nem sequer um https bem formado).
func TestValidateEgressURL_EmptyAllowlist_DenyAll(t *testing.T) {
	t.Parallel()
	if err := validateEgressURL("https://api.provider.internal/v1", newHostAllowlist(nil)); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("allowlist vazia devia negar tudo; got %v", err)
	}
}

// TestValidateEgressURL_PortHardening fecha o defeito residual de porta de AOS-223: a
// allowlist é por (host, porta), não só por host. Uma entrada NUA permite só a porta
// https default (443/ausente); um par explícito "host:porta" é exacto. Falsificável em
// dois sentidos e com FALHA-ANTES concreta: a versão anterior descartava a porta
// (u.Hostname()), pelo que os subcasos ":22"/":8443" abaixo devolviam nil (fail-OPEN —
// um 3xx para uma porta interna de um host allowlisted passava). Agora são recusados.
func TestValidateEgressURL_PortHardening(t *testing.T) {
	t.Parallel()
	// Entrada NUA: default permitido, portas não-default recusadas.
	bare := newHostAllowlist([]string{"api.provider.internal"})
	// Entrada com PORTA explícita não-default: só esse par; nem a default nem outra porta.
	pinned := newHostAllowlist([]string{"api.provider.internal:8443"})

	cases := []struct {
		name  string
		allow hostAllowlist
		raw   string
		want  error // nil = aceite
	}{
		{"nua_default_ok", bare, "https://api.provider.internal/v1", nil},
		{"nua_porta443_explicita_ok", bare, "https://api.provider.internal:443/v1", nil},
		{"nua_porta22_recusada", bare, "https://api.provider.internal:22/", ErrHostNotAllowed},
		{"nua_porta8443_recusada", bare, "https://api.provider.internal:8443/", ErrHostNotAllowed},
		{"pinned_par_exacto_ok", pinned, "https://api.provider.internal:8443/v1", nil},
		{"pinned_default_recusada", pinned, "https://api.provider.internal/v1", ErrHostNotAllowed},
		{"pinned_outra_porta_recusada", pinned, "https://api.provider.internal:9000/", ErrHostNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEgressURL(tc.raw, tc.allow)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("%q devia ser aceite; got %v", tc.raw, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%q: want %v; got %v", tc.raw, tc.want, err)
			}
		})
	}
}

// trustServer devolve um cliente cujo transport confia no certificado do
// httptest.NewTLSServer, PRESERVANDO o resto do endurecimento (timeout, MinVersion
// TLS 1.2, política de redirect). Necessário porque o cliente endurecido não tem
// RootCAs do cert auto-assinado do teste — só assim se exerce o egress https real.
func trustServer(t *testing.T, client *http.Client, srv *httptest.Server) {
	t.Helper()
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport endurecido devia ser *http.Transport; got %T", client.Transport)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("cliente endurecido devia impor TLS >= 1.2; got %+v", tr.TLSClientConfig)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr.TLSClientConfig.RootCAs = pool
}

// TestHardenedEgressClient_LegitHTTPS_Works prova que um endpoint LEGÍTIMO (https,
// host na allowlist) continua a funcionar através do cliente endurecido — o
// endurecimento não parte o tráfego são.
func TestHardenedEgressClient_LegitHTTPS_Works(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}))
	if client.Timeout != egressTimeout {
		t.Fatalf("cliente endurecido devia ter timeout %v; got %v", egressTimeout, client.Timeout)
	}
	trustServer(t, client, srv)

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("egress legítimo devia funcionar; got %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok" {
		t.Fatalf("corpo = %q; want ok", b)
	}
}

// TestHardenedEgressClient_RedirectToInsecure_Refused prova que um redirect (3xx)
// para um alvo http é RECUSADO a meio do fluxo (SSRF-via-redirect): a política é
// re-aplicada a cada salto. O alvo nunca é alcançado. Fail-closed: um DefaultClient
// nu seguiria o redirect (sem política de esquema/host).
func TestHardenedEgressClient_RedirectToInsecure_Refused(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "http://127.0.0.1:1/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()

	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}))
	trustServer(t, client, srv)

	_, err := client.Get(srv.URL)
	if !errors.Is(err, ErrInsecureBaseURL) {
		t.Fatalf("redirect para http devia ser recusado por política (ErrInsecureBaseURL); got %v", err)
	}
}

// TestHardenedEgressClient_RedirectToForeignHost_Refused prova que um redirect para
// um host https FORA da allowlist é recusado (SSRF: um provedor comprometido não
// pode desviar o egress para um host interno via 3xx).
func TestHardenedEgressClient_RedirectToForeignHost_Refused(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "https://evil.internal.test/steal", http.StatusFound)
	}))
	defer srv.Close()

	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}))
	trustServer(t, client, srv)

	_, err := client.Get(srv.URL)
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("redirect para host fora da allowlist devia ser recusado (ErrHostNotAllowed); got %v", err)
	}
}

// TestHardenedEgressClient_RedirectToAllowlistedHostDangerousPort_Refused prova o
// endurecimento de porta no fluxo de redirect (SSRF): um 3xx para o MESMO host
// allowlisted mas numa porta interna perigosa (:22) é recusado — a entrada nua só
// permite a porta https default. O alvo nunca é dialado (CheckRedirect recusa antes).
// Falha-antes: a versão que descartava a porta (u.Hostname()) seguiria o redirect.
func TestHardenedEgressClient_RedirectToAllowlistedHostDangerousPort_Refused(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "https://127.0.0.1:22/internal", http.StatusFound)
	}))
	defer srv.Close()

	client := newHardenedEgressClient(newHostAllowlist([]string{"127.0.0.1"}))
	trustServer(t, client, srv)

	_, err := client.Get(srv.URL)
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("redirect para porta perigosa de host allowlisted devia ser recusado (ErrHostNotAllowed); got %v", err)
	}
}

// TestHardenedEgressClient_RedirectLimit prova o LIMITE de redirects: um loop de
// redirects (para um alvo allowlisted) pára ao fim de egressMaxRedirects saltos, em
// vez de seguir indefinidamente. Pina o limite (regressão contra o default 10). O
// destino é allowlisted por host:porta exacta (o httptest liga numa porta efémera, que
// o endurecimento de porta exige que esteja na allowlist).
func TestHardenedEgressClient_RedirectLimit(t *testing.T) {
	t.Parallel()
	var hits int
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Redirect(w, &http.Request{}, srv.URL, http.StatusFound) // loop para si próprio (allowlisted)
	}))
	defer srv.Close()

	// srv.URL = "https://127.0.0.1:<porta efémera>"; a allowlist precisa do host:porta.
	client := newHardenedEgressClient(newHostAllowlist([]string{strings.TrimPrefix(srv.URL, "https://")}))
	trustServer(t, client, srv)

	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("um loop de redirects devia terminar em erro")
	}
	if !strings.Contains(err.Error(), "demasiados redirects") {
		t.Fatalf("erro devia ser o limite de redirects; got %v", err)
	}
	if hits != egressMaxRedirects {
		t.Fatalf("o cliente devia parar em %d saltos; got %d", egressMaxRedirects, hits)
	}
}
