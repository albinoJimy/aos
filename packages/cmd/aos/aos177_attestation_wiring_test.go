package main

import (
	"os"
	"testing"
)

// Testa o wiring da attestation de dispositivo WebAuthn (AOS-177) no nó: AOS_ATTESTATION_VERIFIER_URL
// compõe o FourEyesGate com o verificador REMOTO (stdlib), activando o enforcement dormente. O
// binário do nó permanece zero-dep (o CBOR corre no componente externo) — garantido pelo guarda
// dep_isolation_test.go; aqui prova-se a superfície de config.

func TestAttestationVerifierURLFromEnv(t *testing.T) {
	// higiene: fixar as env que a config lê para não herdar da máquina.
	for _, k := range []string{"AOS_ATTESTATION_VERIFIER_TOKEN_PATH"} {
		t.Setenv(k, "")
	}
	t.Setenv("AOS_ATTESTATION_VERIFIER_URL", "http://attestation:8090/verify")
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	if cfg.AttestationVerifierURL != "http://attestation:8090/verify" {
		t.Fatalf("AttestationVerifierURL nao foi lida do env; veio %q", cfg.AttestationVerifierURL)
	}
}

func TestAttestationVerifierTokenFromFile(t *testing.T) {
	dir := t.TempDir()
	tok := dir + "/attn-token"
	if err := os.WriteFile(tok, []byte("  secret-bearer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_ATTESTATION_VERIFIER_URL", "https://attestation.example/verify")
	t.Setenv("AOS_ATTESTATION_VERIFIER_TOKEN_PATH", tok)
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	if cfg.AttestationVerifierToken != "secret-bearer" {
		t.Fatalf("token do ficheiro nao foi lido/aparado; veio %q", cfg.AttestationVerifierToken)
	}
}

func TestAttestationVerifierTokenPathMissingAborts(t *testing.T) {
	t.Setenv("AOS_ATTESTATION_VERIFIER_URL", "https://attestation.example/verify")
	t.Setenv("AOS_ATTESTATION_VERIFIER_TOKEN_PATH", "/nao/existe/attn-token")
	if _, err := nodeConfigFromEnv(); err == nil {
		t.Fatal("token path ilegivel devia ABORTAR o boot (fail-closed)")
	}
}
