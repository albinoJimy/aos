package main

// AOS-209 — provas da TERMINAÇÃO TLS do nó (ingresso API/SSE/DSAR + a quarta conjunção do
// bind-guardrail + o opt-out + a postura de produção). Cada prova é NÃO-VACUOSA: distingue o
// aceite do recusado e, no caminho positivo, prova os DOIS sentidos (um cliente em claro contra
// a porta TLS FALHA; um pedido legítimo sobre TLS é ACEITE — a operabilidade sobrevive).
//
// Os certificados de teste são gerados EM RUNTIME (crypto/x509 + crypto/ecdsa) e escritos em
// t.TempDir(): NENHUM material privado é committado ao repo, aos testes ou às fixtures.

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
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// genTLSCertFiles gera, EM RUNTIME, um par certificado+chave ecdsa self-signed válido para
// 127.0.0.1/::1/localhost e escreve-o em dois ficheiros temporários (o padrão do material
// privado por FICHEIRO montado). Devolve os caminhos e o certificado folha (para o pool de
// confiança do cliente). Nada é committado.
func genTLSCertFiles(t *testing.T) (certPath, keyPath string, leaf *x509.Certificate) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aos-node-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return certPath, keyPath, leaf
}

// newAPIServerTLS compõe um APIServer sobre o nó com as opções dadas.
func newAPIServer(t *testing.T, node *Node, opts ...APIOption) *APIServer {
	t.Helper()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	srv, err := NewAPIServer(svc, node, opts...)
	if err != nil {
		t.Fatalf("NewAPIServer: %v", err)
	}
	return srv
}

// ---------------------------------------------------------------------------
// PROVA NEGATIVA — bind não-loopback em texto-claro sem TLS nem opt-out ⇒ RECUSA
// ---------------------------------------------------------------------------

// TestTLSCleartextNonLoopbackRefused é a prova negativa central: um nó com o canal de controlo
// PLENAMENTE operável (identidade real + 1 operador) — pelo que a recusa NÃO pode vir da
// conjunção de AOS-193 — faz bind a 0.0.0.0 em texto-claro e é RECUSADO pela QUARTA conjunção
// (AOS-209), com o erro tipado capturado. O socket nem chega a abrir.
func TestTLSCleartextNonLoopbackRefused(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true) // 1 operador ⇒ controlAuthenticated == true
	defer func() { _ = node.Close() }()
	srv := newAPIServer(t, node) // SEM TLS e SEM opt-out

	if !srv.controlAuthenticated() {
		t.Fatal("pre-condicao: o no devia estar controlAuthenticated (a recusa tem de vir do TRANSPORTE, nao dos operadores)")
	}
	if srv.transportEncrypted() {
		t.Fatal("pre-condicao: sem TLS nem opt-out, transportEncrypted devia ser false")
	}
	// Serve capta a recusa SÍNCRONA (o Listen nem acontece).
	err := srv.Serve("0.0.0.0:0")
	if !errors.Is(err, ErrRefuseCleartextBind) {
		t.Fatalf("bind 0.0.0.0 em claro sem opt-out devia dar ErrRefuseCleartextBind, veio %v", err)
	}
	// E via listen (o caminho puro) idem.
	if _, lerr := srv.listen("0.0.0.0:0"); !errors.Is(lerr, ErrRefuseCleartextBind) {
		t.Fatalf("listen 0.0.0.0 em claro devia dar ErrRefuseCleartextBind, veio %v", lerr)
	}

	// RETRO-COMPATIBILIDADE: o MESMO nó em claro em LOOPBACK continua a abrir (comportamento
	// actual inalterado — o guardrail sempre permitiu loopback).
	ln, err := srv.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback em claro devia continuar a abrir (retro-compat), veio %v", err)
	}
	_ = ln.Close()
}

// TestTLSExternalTerminationSatisfiesGuard prova que o opt-out declarado (terminação a
// montante) SATISFAZ a quarta conjunção: o MESMO bind não-loopback em claro passa a ser
// permitido, porque quem configurou ASSUMIU a cifra do transporte a montante.
func TestTLSExternalTerminationSatisfiesGuard(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	srv := newAPIServer(t, node, WithExternalTLSTermination(true))

	if !srv.transportEncrypted() {
		t.Fatal("com opt-out declarado, transportEncrypted devia ser true")
	}
	ln, err := srv.listen("0.0.0.0:0")
	if err != nil {
		t.Fatalf("bind 0.0.0.0 com opt-out declarado devia ser PERMITIDO, veio %v", err)
	}
	_ = ln.Close()
}

// TestTLSNodeTerminationSatisfiesGuard prova que a terminação TLS NO nó satisfaz a quarta
// conjunção sem opt-out: bind não-loopback permitido porque o transporte é cifrado no nó.
func TestTLSNodeTerminationSatisfiesGuard(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	certPath, keyPath, _ := genTLSCertFiles(t)
	srv := newAPIServer(t, node, WithTLSFiles(certPath, keyPath))

	if !srv.tlsEnabled || !srv.transportEncrypted() {
		t.Fatal("com TLS no no, tlsEnabled e transportEncrypted deviam ser true")
	}
	ln, err := srv.listen("0.0.0.0:0")
	if err != nil {
		t.Fatalf("bind 0.0.0.0 com TLS no no devia ser PERMITIDO, veio %v", err)
	}
	_ = ln.Close()
}

// ---------------------------------------------------------------------------
// PROVA POSITIVA (dois sentidos) — cliente em claro FALHA; pedido sobre TLS é ACEITE
// ---------------------------------------------------------------------------

// TestTLSServesEncryptedAndRejectsCleartext levanta o APIServer com TLS NO NÓ num socket real e
// prova os DOIS sentidos: (a) um cliente HTTP em CLARO contra a porta TLS FALHA; (b) um GET
// /healthz sobre TLS devolve 200 E um `aos steer` ASSINADO sobre TLS é ACEITE e chega ao canal
// — a operabilidade legítima sobrevive ao aperto.
func TestTLSServesEncryptedAndRejectsCleartext(t *testing.T) {
	node, opPriv := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	certPath, keyPath, leaf := genTLSCertFiles(t)
	srv := newAPIServer(t, node, WithTLSFiles(certPath, keyPath))

	// listen aplica os guardrails (loopback ⇒ passa) e serveListener escolhe TLS.
	ln, err := srv.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen TLS: %v", err)
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

	// (a) SENTIDO 1 — cliente HTTP em CLARO contra a porta TLS: NÃO pode obter um /healthz
	// legítimo. O servidor TLS da stdlib detecta o pedido em claro e devolve um 400 em claro
	// ("client sent an HTTP request to an HTTPS server") em vez de o servir — o que conta é que
	// NUNCA vem o 200 que a porta serviria a um cliente TLS. Qualquer erro de transporte também
	// satisfaz (dependendo do timing do handshake).
	clearClient := &http.Client{Timeout: 3 * time.Second}
	if resp, err := clearClient.Get("http://" + addr + "/healthz"); err == nil {
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusOK {
			t.Fatalf("um cliente em CLARO contra a porta TLS NAO devia obter um 200 de /healthz, veio %d", status)
		}
	}

	// Cliente TLS que confia no certificado gerado (pool com a folha).
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	tlsClient := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}

	// (b) SENTIDO 2a — GET /healthz sobre TLS ⇒ 200.
	resp, err := tlsClient.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz sobre TLS devia funcionar, veio %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("GET /healthz sobre TLS devia dar 200, veio %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// (b) SENTIDO 2b — um `aos steer` ASSINADO sobre TLS é ACEITE (202) e a correcção chega ao
	// SteerChannel (não-vacuoso: a operabilidade legítima do plano de controlo sobrevive ao TLS).
	const runID = "run-steer-tls"
	correction := []byte("aperta o ambito sobre TLS")
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	em := integration.SignSignal(opPriv, apiOperatorID, runID, control.SignalSteer, correction, nonce, tnClock()())
	body, _ := json.Marshal(steerBody(em, correction))
	sreq, _ := http.NewRequest("POST", "https://"+addr+"/runs/"+runID+"/steer", bytes.NewReader(body))
	sreq.Header.Set("Content-Type", "application/json")
	sresp, err := tlsClient.Do(sreq)
	if err != nil {
		t.Fatalf("POST /steer sobre TLS: %v", err)
	}
	if sresp.StatusCode != http.StatusAccepted {
		_ = sresp.Body.Close()
		t.Fatalf("steer assinado sobre TLS devia dar 202, veio %d", sresp.StatusCode)
	}
	_ = sresp.Body.Close()
	if got, ok := node.Steer.PendingCorrection(runID); !ok || string(got) != string(correction) {
		t.Fatalf("a correccao assinada sobre TLS devia ter chegado ao canal, veio (%q,%v)", got, ok)
	}
}

// ---------------------------------------------------------------------------
// Config TLS fail-closed — par incompleto / inválido
// ---------------------------------------------------------------------------

func TestTLSIncompleteConfigRefused(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Só o cert (sem key) ⇒ ErrIncompleteTLSConfig.
	if _, err := NewAPIServer(svc, node, WithTLSFiles("/x/cert.pem", "")); !errors.Is(err, ErrIncompleteTLSConfig) {
		t.Fatalf("cert sem key devia dar ErrIncompleteTLSConfig, veio %v", err)
	}
	// Só a key (sem cert) ⇒ idem.
	if _, err := NewAPIServer(svc, node, WithTLSFiles("", "/x/key.pem")); !errors.Is(err, ErrIncompleteTLSConfig) {
		t.Fatalf("key sem cert devia dar ErrIncompleteTLSConfig, veio %v", err)
	}
}

func TestTLSBadKeyPairRefused(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("nao e um PEM"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if _, err := NewAPIServer(svc, node, WithTLSFiles(bad, bad)); !errors.Is(err, ErrBadTLSKeyPair) {
		t.Fatalf("par TLS malformado devia dar ErrBadTLSKeyPair, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// Postura de produção (env) e o parser do opt-out
// ---------------------------------------------------------------------------

// TestProductionRefusesWithoutTLSOrOptOut prova que AOS_MODE=production sem TLS nem opt-out NÃO
// arranca — pelo caminho de env (apiTLSOptionsFromEnv) E pelo caminho de produção (serveAPI).
func TestProductionRefusesWithoutTLSOrOptOut(t *testing.T) {
	t.Setenv("AOS_TLS_CERT_PATH", "")
	t.Setenv("AOS_TLS_KEY_PATH", "")
	t.Setenv("AOS_TLS_EXTERNAL_TERMINATION", "")

	// (a) o construtor de opções recusa em produção.
	if _, _, err := apiTLSOptionsFromEnv(true); !errors.Is(err, ErrProductionNeedsTLS) {
		t.Fatalf("apiTLSOptionsFromEnv(production) sem TLS devia dar ErrProductionNeedsTLS, veio %v", err)
	}
	// FORA de produção, o mesmo estado é PERMITIDO (o bind-guardrail trata o não-loopback).
	if opts, banner, err := apiTLSOptionsFromEnv(false); err != nil || len(opts) != 0 || len(banner) != 0 {
		t.Fatalf("fora de producao, sem TLS nem opt-out devia ser permitido sem opcoes/banner, veio opts=%d banner=%d err=%v", len(opts), len(banner), err)
	}

	// (b) o caminho de PRODUÇÃO (serveAPI, o que o entrypoint invoca) não arranca.
	t.Setenv("AOS_MODE", "production")
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()
	if err := serveAPI(context.Background(), io.Discard, node, "0.0.0.0:0"); !errors.Is(err, ErrProductionNeedsTLS) {
		t.Fatalf("serveAPI em producao sem TLS nem opt-out devia dar ErrProductionNeedsTLS, veio %v", err)
	}
}

// TestProductionAcceptsOptOutWithBanner prova que o opt-out declarado é aceite em produção e
// produz o banner ruidoso (o aviso proeminente do modelo de AOS-203).
func TestProductionAcceptsOptOutWithBanner(t *testing.T) {
	t.Setenv("AOS_TLS_CERT_PATH", "")
	t.Setenv("AOS_TLS_KEY_PATH", "")
	t.Setenv("AOS_TLS_EXTERNAL_TERMINATION", "1")

	opts, banner, err := apiTLSOptionsFromEnv(true)
	if err != nil {
		t.Fatalf("opt-out declarado devia ser aceite em producao, veio %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("opt-out declarado devia produzir uma opcao (WithExternalTLSTermination)")
	}
	if len(banner) == 0 {
		t.Fatal("opt-out declarado devia produzir o banner ruidoso (aviso proeminente)")
	}
}

// TestProductionAcceptsNodeTLSNoBanner prova que TLS no nó é aceite em produção e NÃO emite o
// banner de opt-out (o nó cifra o transporte — não há nada a avisar).
func TestProductionAcceptsNodeTLSNoBanner(t *testing.T) {
	certPath, keyPath, _ := genTLSCertFiles(t)
	t.Setenv("AOS_TLS_CERT_PATH", certPath)
	t.Setenv("AOS_TLS_KEY_PATH", keyPath)
	// Mesmo com o opt-out ligado, o TLS no nó tem PRECEDÊNCIA e o banner NÃO sai.
	t.Setenv("AOS_TLS_EXTERNAL_TERMINATION", "1")

	opts, banner, err := apiTLSOptionsFromEnv(true)
	if err != nil {
		t.Fatalf("TLS no no devia ser aceite em producao, veio %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("TLS no no devia produzir opcoes")
	}
	if len(banner) != 0 {
		t.Fatalf("com TLS no no, o banner de opt-out NAO devia sair, veio %d linhas", len(banner))
	}
}

func TestParseTLSExternalTerminationRejectsGarbage(t *testing.T) {
	for _, ok := range []string{"", "1", "true", "on", "0", "off", "no"} {
		if _, err := parseTLSExternalTermination(ok); err != nil {
			t.Fatalf("valor %q devia ser aceite, veio %v", ok, err)
		}
	}
	for _, bad := range []string{"sim", "yep", "externo", "2", "tls"} {
		if _, err := parseTLSExternalTermination(bad); !errors.Is(err, ErrBadTLSExternalTermination) {
			t.Fatalf("valor %q devia dar ErrBadTLSExternalTermination, veio %v", bad, err)
		}
	}
}
