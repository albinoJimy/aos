package oidc

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// AOS-229 — ENDURECIMENTO do cliente HTTP DEFAULT do verificador OIDC (defesa-em-profundidade
// sobre o transporte do material de chave, análogo a AOS-223 no Model Gateway). O default (usado
// quando Config.HTTPClient é nil) tem TLS MinVersion 1.2 + timeout, LIMITA a cadeia de redirects e
// RE-VALIDA cada salto (um redirect para http/host interno é recusado — anti-SSRF). Um cliente
// injectado (httptest/deployment) é respeitado tal-qual e não passa por aqui.

func TestHardenedOIDCClient_TLSMinVersionAndTimeout(t *testing.T) {
	c := newHardenedOIDCClient(false)
	if c.Timeout != oidcHTTPTimeout {
		t.Fatalf("timeout=%v, quero %v", c.Timeout, oidcHTTPTimeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatalf("transport devia ser *http.Transport com TLSClientConfig, veio %T", c.Transport)
	}
	if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion=%#x, quero TLS 1.2 (%#x)", tr.TLSClientConfig.MinVersion, tls.VersionTLS12)
	}
}

// Uma cadeia de redirects (loopback http, permitido com allowInsecure) é LIMITADA — não se segue
// indefinidamente um IdP que redirige em ciclo.
func TestHardenedOIDCClient_RedirectLimit(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	c := newHardenedOIDCClient(true) // loopback ⇒ http permitido; aqui testa-se o LIMITE, não o esquema
	if _, err := c.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "demasiados redirects") {
		t.Fatalf("uma cadeia de redirects devia falhar no limite, veio: %v", err)
	}
}

// Com allowInsecure=false (produção), um redirect para http num host NÃO-loopback é RECUSADO
// fail-closed — o vector de SSRF (redirect da JWKS para um host interno em claro) é fechado.
func TestHardenedOIDCClient_RejectsInsecureRedirect(t *testing.T) {
	c := newHardenedOIDCClient(false)
	req, err := http.NewRequest(http.MethodGet, "http://internal.svc.local/jwks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := c.CheckRedirect(req, nil); err == nil {
		t.Fatal("um redirect para http num host não-loopback devia ser RECUSADO (anti-SSRF)")
	}
	// Dois sentidos: um redirect para https (ou loopback) dentro do limite é PERMITIDO.
	reqOK, _ := http.NewRequest(http.MethodGet, "https://idp.corp.example/jwks", nil)
	if err := c.CheckRedirect(reqOK, nil); err != nil {
		t.Fatalf("um redirect https legítimo dentro do limite devia ser permitido, veio: %v", err)
	}
}
