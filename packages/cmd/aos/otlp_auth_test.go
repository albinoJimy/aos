package main

// DEF-012, EIXO 2 — provas da AUTENTICAÇÃO FORTE da perna OTLP perante o colector (mTLS de cliente
// OU bearer), SEMPRE preservando o FAIL-OPEN de AOS-173. Cada prova é não-vacuosa:
//
//   - POSITIVA (bearer): com o bearer composto, o POST ao colector LEVA `Authorization: Bearer …`.
//   - POSITIVA (mTLS de cliente): com o par de cliente composto, o handshake a um colector que
//     EXIGE certificado de cliente completa e o export chega (Exported>0).
//   - FAIL-OPEN: uma recusa de autenticação do colector (401) é CONTABILIZADA (Failed>0) e NUNCA
//     quebra/bloqueia — Export/Close não propagam nem entram em pânico.
//   - SEGREDO: o bearer NUNCA aparece nos logs do exporter.
//   - CONFIG fail-closed: par incompleto/inválido e ficheiro de bearer ilegível/vazio ABORTAM.
//
// Todos os certificados/chaves/tokens são gerados EM RUNTIME e escritos em t.TempDir(): nenhum
// material é committado ao repo, aos testes ou às fixtures.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// authCollector é um colector OTLP/HTTP de teste que regista o cabeçalho Authorization de cada POST
// e responde com um status configurável (default 200).
type authCollector struct {
	mu     sync.Mutex
	authz  []string
	status int
}

func (c *authCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	c.mu.Lock()
	c.authz = append(c.authz, r.Header.Get("Authorization"))
	st := c.status
	c.mu.Unlock()
	if st == 0 {
		st = http.StatusOK
	}
	w.WriteHeader(st)
}

func (c *authCollector) headers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.authz))
	copy(out, c.authz)
	return out
}

// syncBuf é um io/logf sink concorrente-seguro (o exporter loga na goroutine de flush).
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(&s.b, format+"\n", args...)
}

// Write torna o mesmo sink utilizável como io.Writer — a forma que [serveAPI] espera para o
// banner de arranque (AOS-277), onde o loop de serviço escreve a partir de goroutines suas.
func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// writeBearerFile gera um token pseudo-aleatório EM RUNTIME e escreve-o num ficheiro temporário
// (o padrão do segredo por ficheiro montado). Devolve o caminho e o token.
func writeBearerFile(t *testing.T) (path, token string) {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand token: %v", err)
	}
	token = "tok-" + hex.EncodeToString(raw)
	dir := t.TempDir()
	path = filepath.Join(dir, "otlp-bearer.token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write bearer: %v", err)
	}
	return path, token
}

// emitOneSpan produz um único span pelo tracer do exporter (para forçar um POST ao colector).
func emitOneSpan(exp otelgenai.Exporter) {
	tracer := otelgenai.NewTracer(exp)
	_, span := tracer.StartSpan(context.Background(), otelgenai.OpChat)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpChat)
	span.End()
}

// ---------------------------------------------------------------------------
// POSITIVA — bearer: o POST leva o cabeçalho Authorization
// ---------------------------------------------------------------------------

func TestOTLPBearerHeaderPresentOnPost(t *testing.T) {
	col := &authCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	tokenPath, token := writeBearerFile(t)
	exp, err := NewOTLPHTTPExporter(srv.URL,
		WithOTLPHTTPClient(srv.Client()),
		WithOTLPBearerTokenFile(tokenPath),
	)
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	emitOneSpan(exp)
	if err := exp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if st := exp.Stats(); st.Exported == 0 {
		t.Fatalf("esperava Exported>0 (o colector aceita), veio %+v", st)
	}
	headers := col.headers()
	if len(headers) == 0 {
		t.Fatal("o colector nao recebeu nenhum POST")
	}
	want := "Bearer " + token
	sawBearer := false
	for _, h := range headers {
		if h == want {
			sawBearer = true
		}
	}
	if !sawBearer {
		t.Fatalf("o POST ao colector devia levar %q, cabecalhos vistos=%v", want, headers)
	}
}

// ---------------------------------------------------------------------------
// FAIL-OPEN + SEGREDO — 401 do colector é contabilizado, nunca quebra; o token nunca é logado
// ---------------------------------------------------------------------------

func TestOTLPBearerAuthRejectionFailsOpenAndNeverLogsToken(t *testing.T) {
	col := &authCollector{status: http.StatusUnauthorized} // o colector RECUSA a autenticação
	srv := httptest.NewServer(col)
	defer srv.Close()

	tokenPath, token := writeBearerFile(t)
	log := &syncBuf{}
	exp, err := NewOTLPHTTPExporter(srv.URL,
		WithOTLPHTTPClient(srv.Client()),
		WithOTLPBearerTokenFile(tokenPath),
		WithOTLPLogger(log.logf),
		WithOTLPMaxRetries(0), // falha rápida (uma tentativa) ⇒ log de falha definitiva
		WithOTLPBackoff(0),
	)
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	emitOneSpan(exp)
	// Close NÃO pode propagar nem bloquear (fail-open): uma recusa de auth do colector não quebra
	// o shutdown do nó.
	if err := exp.Close(); err != nil {
		t.Fatalf("Close devia ser limpo mesmo com o colector a recusar (fail-open), veio %v", err)
	}
	st := exp.Stats()
	if st.Failed == 0 {
		t.Fatalf("esperava Failed>0 (401 contabilizado, fail-open), veio %+v", st)
	}
	if st.Exported != 0 {
		t.Fatalf("nada devia exportar com o colector a recusar, veio Exported=%d", st.Exported)
	}
	// O SEGREDO nunca aparece nos logs — o export falhou e logou, mas sem o token.
	logs := log.String()
	if logs == "" {
		t.Fatal("esperava pelo menos uma linha de log de falha (para provar que ela NAO traz o token)")
	}
	if strings.Contains(logs, token) {
		t.Fatalf("o bearer NUNCA pode aparecer nos logs; apareceu em: %q", logs)
	}
}

// ---------------------------------------------------------------------------
// POSITIVA — mTLS de cliente: o handshake a um colector que EXIGE cert de cliente completa
// ---------------------------------------------------------------------------

// genOTLPClientCertFiles gera, EM RUNTIME, um certificado de cliente self-signed (que serve também
// de sua própria CA) e escreve cert+chave em ficheiros temporários. Devolve os caminhos e um pool
// contendo o certificado (para o colector VERIFICAR o cliente via ClientCAs). Nada é committado.
func genOTLPClientCertFiles(t *testing.T) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aos-otlp-client-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse client cert: %v", err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "otlp-client.crt")
	keyPath = filepath.Join(dir, "otlp-client.key")
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

func TestOTLPClientCertPresentedToCollector(t *testing.T) {
	certPath, keyPath, clientPool := genOTLPClientCertFiles(t)

	col := &authCollector{}
	srv := httptest.NewUnstartedServer(col)
	// O colector EXIGE e VERIFICA o certificado de cliente contra clientPool: sem o par composto,
	// o handshake falha e nada exporta — o teste seria vacuoso.
	srv.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientPool,
		MinVersion: tls.VersionTLS12,
	}
	srv.StartTLS()
	defer srv.Close()

	// Cliente injectado que CONFIA no certificado do colector, mas SEM certificado de cliente: é
	// [WithOTLPClientCertFiles] que tem de o acrescentar ao transporte. Se a opção não funcionasse,
	// o handshake falharia (o colector exige cert) e Exported==0.
	serverPool := x509.NewCertPool()
	serverPool.AddCert(srv.Certificate())
	injected := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: serverPool, MinVersion: tls.VersionTLS12}},
	}

	exp, err := NewOTLPHTTPExporter(srv.URL,
		WithOTLPHTTPClient(injected),
		WithOTLPClientCertFiles(certPath, keyPath),
	)
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	// WIRING: o par de cliente ficou no transporte (o pedido LEVA o certificado).
	tr, ok := injected.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || len(tr.TLSClientConfig.Certificates) != 1 {
		t.Fatalf("WithOTLPClientCertFiles devia colocar 1 certificado de cliente no transporte")
	}
	emitOneSpan(exp)
	if err := exp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if st := exp.Stats(); st.Exported == 0 {
		t.Fatalf("esperava Exported>0 (mTLS de cliente aceite pelo colector), veio %+v", st)
	}
}

// ---------------------------------------------------------------------------
// CONFIG fail-closed
// ---------------------------------------------------------------------------

func TestOTLPClientAuthConfigFailClosed(t *testing.T) {
	certPath, keyPath, _ := genOTLPClientCertFiles(t)

	// (a) par de cliente INCOMPLETO (só cert) ⇒ ErrIncompleteOTLPClientTLS.
	if _, err := NewOTLPHTTPExporter("http://collector:4318", WithOTLPClientCertFiles(certPath, "")); !errors.Is(err, ErrIncompleteOTLPClientTLS) {
		t.Fatalf("par incompleto devia dar ErrIncompleteOTLPClientTLS, veio %v", err)
	}
	if _, err := NewOTLPHTTPExporter("http://collector:4318", WithOTLPClientCertFiles("", keyPath)); !errors.Is(err, ErrIncompleteOTLPClientTLS) {
		t.Fatalf("par incompleto (so key) devia dar ErrIncompleteOTLPClientTLS, veio %v", err)
	}

	// (b) par de cliente INVÁLIDO (ficheiros que não são PEM) ⇒ ErrBadOTLPClientCert.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("nao e um PEM"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if _, err := NewOTLPHTTPExporter("http://collector:4318", WithOTLPClientCertFiles(bad, bad)); !errors.Is(err, ErrBadOTLPClientCert) {
		t.Fatalf("par invalido devia dar ErrBadOTLPClientCert, veio %v", err)
	}

	// (c) ficheiro de bearer inexistente ⇒ ErrBadOTLPBearerToken.
	if _, err := NewOTLPHTTPExporter("http://collector:4318", WithOTLPBearerTokenFile(filepath.Join(dir, "nao-existe.token"))); !errors.Is(err, ErrBadOTLPBearerToken) {
		t.Fatalf("bearer inexistente devia dar ErrBadOTLPBearerToken, veio %v", err)
	}
	// (d) ficheiro de bearer VAZIO ⇒ ErrBadOTLPBearerToken.
	empty := filepath.Join(dir, "empty.token")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if _, err := NewOTLPHTTPExporter("http://collector:4318", WithOTLPBearerTokenFile(empty)); !errors.Is(err, ErrBadOTLPBearerToken) {
		t.Fatalf("bearer vazio devia dar ErrBadOTLPBearerToken, veio %v", err)
	}
}
