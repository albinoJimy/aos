package main

// DEF-012, EIXO 1 — provas do mTLS do PLANO DE CONTROLO. Cada prova é NÃO-VACUOSA e cobre as duas
// invariantes que o eixo exige:
//
//   - NEGATIVA: com o mTLS ligado, /steer SEM certificado de cliente verificado é RECUSADO; e um
//     certificado de cliente VÁLIDO mas com assinatura ed25519 AUSENTE/MÁ continua RECUSADO
//     (o mTLS é ADITIVO, NUNCA um bypass da assinatura).
//   - POSITIVA: /steer com certificado de cliente verificado E assinatura ed25519 válida é ACEITE
//     e a correcção chega ao SteerChannel.
//   - ESCOPO: as rotas fora do plano de controlo (/healthz) servem SEM certificado de cliente.
//   - RETRO-COMPAT: com o mTLS DESLIGADO (o default), o steer assinado passa sem qualquer cert.
//
// Todos os certificados/chaves são gerados EM RUNTIME e escritos em t.TempDir(): nenhum material
// é committado ao repo, aos testes ou às fixtures.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// verifiedClientTLS devolve um *tls.ConnectionState que simula um certificado de cliente JÁ
// verificado pelo handshake (VerifiedChains não-vazio) — o estado que [admitControlMTLS] lê.
func verifiedClientTLS() *tls.ConnectionState {
	return &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{&x509.Certificate{}}}}
}

// postSteerTLS faz um POST /steer ao handler, com o estado TLS dado (nil ⇒ sem certificado).
func postSteerTLS(h http.Handler, runID string, body any, cs *tls.ConnectionState) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/runs/"+runID+"/steer", bytes.NewReader(b))
	req.TLS = cs
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Handler-level — o predicado de recusa/aceitação (mTLS composto via WithControlMTLS)
// ---------------------------------------------------------------------------

// TestControlMTLSRefusesWithoutClientCert: mTLS ligado, /steer sem certificado ⇒ 403 SEM efeito.
func TestControlMTLSRefusesWithoutClientCert(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	// WithControlMTLS liga o flag no handler; o carregamento da CA vive em NewAPIServer (aqui só
	// precisamos do predicado do handler).
	_, h := newAPI(t, node, WithControlMTLS("/mnt/control-ca.pem"))

	const runID = "run-mtls-nocert"
	correction := []byte("aperta o ambito")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())

	// Sem certificado de cliente (r.TLS == nil) ⇒ 403 antes de tocar na assinatura/canal.
	rec := postSteerTLS(h, runID, steerBody(em, correction), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mTLS ligado e sem certificado de cliente devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := node.Steer.PendingCorrection(runID); ok {
		t.Fatal("sem certificado de cliente a correccao NAO devia chegar ao canal")
	}
}

// TestControlMTLSNotBypassOfEd25519 é a prova central de que o mTLS é ADITIVO: um certificado de
// cliente VERIFICADO mas com assinatura ed25519 AUSENTE/MÁ continua RECUSADO.
func TestControlMTLSNotBypassOfEd25519(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node, WithControlMTLS("/mnt/control-ca.pem"))

	// (a) certificado verificado + assinatura ADULTERADA ⇒ 403 (a ed25519 continua a barrar).
	const runIDBad = "run-mtls-badsig"
	correction := []byte("correccao maliciosa")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runIDBad, control.SignalSteer, correction, nonce, tnClock()())
	em.Signature[0] ^= 0xFF // um bit: deixa de validar

	rec := postSteerTLS(h, runIDBad, steerBody(em, correction), verifiedClientTLS())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cert de cliente valido + assinatura MA devia dar 403 (mTLS nao e bypass), veio %d", rec.Code)
	}
	if _, ok := node.Steer.PendingCorrection(runIDBad); ok {
		t.Fatal("assinatura ma NAO devia ter efeito no canal mesmo com certificado de cliente valido")
	}

	// (b) certificado verificado + assinatura AUSENTE (vazia) ⇒ 403.
	const runIDAbsent = "run-mtls-absentsig"
	emAbsent := integration.SignSignal(opPriv, apiOperatorID, runIDAbsent, control.SignalSteer, correction, nonce, tnClock()())
	emAbsent.Signature = nil // assinatura AUSENTE
	recAbsent := postSteerTLS(h, runIDAbsent, steerBody(emAbsent, correction), verifiedClientTLS())
	if recAbsent.Code == http.StatusAccepted {
		t.Fatalf("cert de cliente valido + assinatura AUSENTE NAO devia ser aceite, veio %d", recAbsent.Code)
	}
	if _, ok := node.Steer.PendingCorrection(runIDAbsent); ok {
		t.Fatal("assinatura ausente NAO devia ter efeito no canal")
	}
}

// TestControlMTLSAcceptsCertPlusValidSignature: mTLS ligado, cert verificado + assinatura VÁLIDA
// ⇒ 202 e a correcção chega ao canal (a operabilidade legítima sobrevive ao aperto).
func TestControlMTLSAcceptsCertPlusValidSignature(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node, WithControlMTLS("/mnt/control-ca.pem"))

	const runID = "run-mtls-ok"
	correction := []byte("aperta o ambito sobre mtls")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())

	rec := postSteerTLS(h, runID, steerBody(em, correction), verifiedClientTLS())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("cert de cliente verificado + assinatura valida devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
	}
	got, ok := node.Steer.PendingCorrection(runID)
	if !ok || string(got) != string(correction) {
		t.Fatalf("a correccao devia ter chegado ao canal, veio (%q,%v)", got, ok)
	}
}

// TestControlMTLSDisabledRetroCompat: com o mTLS DESLIGADO (o default), o steer assinado passa SEM
// qualquer certificado de cliente — o comportamento actual byte-a-byte.
func TestControlMTLSDisabledRetroCompat(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node) // SEM WithControlMTLS

	const runID = "run-mtls-off"
	correction := []byte("sem mtls, so ed25519")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())

	rec := postSteerTLS(h, runID, steerBody(em, correction), nil) // r.TLS nil
	if rec.Code != http.StatusAccepted {
		t.Fatalf("com mTLS desligado o steer assinado devia dar 202 sem certificado, veio %d", rec.Code)
	}
	if _, ok := node.Steer.PendingCorrection(runID); !ok {
		t.Fatal("com mTLS desligado a correccao assinada devia ter chegado ao canal")
	}
}

// ---------------------------------------------------------------------------
// Config fail-closed — NewAPIServer arbitra
// ---------------------------------------------------------------------------

// TestControlMTLSNeedsNodeTLS: CA de cliente definida mas sem terminação TLS no nó ⇒ recusa.
func TestControlMTLSNeedsNodeTLS(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	caPath, _ := genControlClientCA(t)
	if _, err := NewAPIServer(svc, node, WithControlMTLS(caPath)); !errors.Is(err, ErrControlMTLSNeedsNodeTLS) {
		t.Fatalf("mTLS de controlo sem TLS no no devia dar ErrControlMTLSNeedsNodeTLS, veio %v", err)
	}
}

// TestControlMTLSBadCARefused: CA de cliente ilegível/inválida ⇒ ErrBadControlMTLSCA.
func TestControlMTLSBadCARefused(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	certPath, keyPath, _ := genTLSCertFiles(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad-ca.pem")
	if err := os.WriteFile(bad, []byte("nao e um PEM de CA"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if _, err := NewAPIServer(svc, node, WithTLSFiles(certPath, keyPath), WithControlMTLS(bad)); !errors.Is(err, ErrBadControlMTLSCA) {
		t.Fatalf("CA de cliente invalida devia dar ErrBadControlMTLSCA, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end — listener mTLS real (handshake), prova de escopo e de barreira de transporte
// ---------------------------------------------------------------------------

// genControlClientCA gera, EM RUNTIME, uma CA de cliente self-signed e um certificado de cliente
// assinado por ela. Escreve o certificado da CA (material PÚBLICO) num ficheiro temporário (para
// AOS_CONTROL_MTLS_CA_PATH) e devolve o par tls.Certificate do cliente (para o cliente TLS de
// teste apresentar). Nada é committado.
func genControlClientCA(t *testing.T) (caPath string, clientCert tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := x509.Certificate{
		SerialNumber:          big.NewInt(100),
		Subject:               pkix.Name{CommonName: "aos-control-client-ca-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTmpl, &caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	clientTmpl := x509.Certificate{
		SerialNumber:          big.NewInt(101),
		Subject:               pkix.Name{CommonName: "ops:alice-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, &clientTmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	dir := t.TempDir()
	caPath = filepath.Join(dir, "client-ca.crt")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER})
	clientCert, err = tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	return caPath, clientCert
}

// TestControlMTLSEndToEnd levanta o APIServer com TLS no nó + mTLS de controlo num socket real e
// prova, pelo caminho do handshake: (a) /healthz SEM certificado de cliente ⇒ 200 (ESCOPO: as
// rotas fora do plano de controlo não exigem cert); (b) /steer SEM certificado ⇒ 403; (c) /steer
// COM certificado verificado + assinatura válida ⇒ 202 e a correcção chega ao canal.
func TestControlMTLSEndToEnd(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	certPath, keyPath, serverLeaf := genTLSCertFiles(t)
	caPath, clientCert := genControlClientCA(t)
	srv := newAPIServer(t, node, WithTLSFiles(certPath, keyPath), WithControlMTLS(caPath))
	if !srv.controlMTLSEnabled() {
		t.Fatal("pre-condicao: controlMTLSEnabled devia ser true")
	}

	ln, err := srv.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveDone := make(chan struct{})
	go func() {
		_ = srv.serveListener(ln)
		close(serveDone)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		_ = srv.Shutdown(ctx)
		<-serveDone
	}()
	addr := ln.Addr().String()

	serverPool := x509.NewCertPool()
	serverPool.AddCert(serverLeaf)

	// Cliente SEM certificado de cliente (mas confia no servidor).
	noCertClient := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: serverPool, MinVersion: tls.VersionTLS12}},
	}
	// (a) ESCOPO — /healthz sem certificado de cliente ⇒ 200 (a rota fora do plano de controlo
	// serve na mesma; o mTLS NÃO governa /healthz).
	if resp, err := noCertClient.Get("https://" + addr + "/healthz"); err != nil {
		t.Fatalf("/healthz sem cert de cliente devia funcionar (escopo), veio %v", err)
	} else {
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusOK {
			t.Fatalf("/healthz sem cert de cliente devia dar 200 (escopo), veio %d", code)
		}
	}
	// (b) /steer sem certificado de cliente ⇒ 403.
	const runID = "run-mtls-e2e"
	correction := []byte("aperta sobre mtls e2e")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())
	body, _ := json.Marshal(steerBody(em, correction))
	sreq, _ := http.NewRequest("POST", "https://"+addr+"/runs/"+runID+"/steer", bytes.NewReader(body))
	sreq.Header.Set("Content-Type", "application/json")
	sresp, err := noCertClient.Do(sreq)
	if err != nil {
		t.Fatalf("/steer sem cert (POST) devia chegar ao handler: %v", err)
	}
	if sresp.StatusCode != http.StatusForbidden {
		_ = sresp.Body.Close()
		t.Fatalf("/steer sem cert de cliente devia dar 403, veio %d", sresp.StatusCode)
	}
	_ = sresp.Body.Close()
	if _, ok := node.Steer.PendingCorrection(runID); ok {
		t.Fatal("/steer sem cert NAO devia ter efeito no canal")
	}

	// (c) /steer COM certificado de cliente verificado + assinatura válida ⇒ 202 e chega ao canal.
	mtlsClient := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      serverPool,
			Certificates: []tls.Certificate{clientCert},
			MinVersion:   tls.VersionTLS12,
		}},
	}
	nonce2 := make([]byte, 32)
	_, _ = rand.Read(nonce2)
	em2 := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce2, tnClock()())
	body2, _ := json.Marshal(steerBody(em2, correction))
	sreq2, _ := http.NewRequest("POST", "https://"+addr+"/runs/"+runID+"/steer", bytes.NewReader(body2))
	sreq2.Header.Set("Content-Type", "application/json")
	sresp2, err := mtlsClient.Do(sreq2)
	if err != nil {
		t.Fatalf("/steer com cert de cliente (POST) sobre TLS: %v", err)
	}
	if sresp2.StatusCode != http.StatusAccepted {
		_ = sresp2.Body.Close()
		t.Fatalf("/steer com cert verificado + assinatura valida devia dar 202, veio %d", sresp2.StatusCode)
	}
	_ = sresp2.Body.Close()
	if got, ok := node.Steer.PendingCorrection(runID); !ok || string(got) != string(correction) {
		t.Fatalf("a correccao (cert+assinatura validos) devia ter chegado ao canal, veio (%q,%v)", got, ok)
	}
}
