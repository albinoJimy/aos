package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseBrokerVaultFromEnv_Dormente: sem AOS_BROKER_VAULT_ADDR o broker Vault
// fica dormente (nil, nil, nil) — nada muda no comportamento actual.
func TestParseBrokerVaultFromEnv_Dormente(t *testing.T) {
	t.Setenv("AOS_BROKER_VAULT_ADDR", "")
	client, set, err := parseBrokerVaultFromEnv()
	if err != nil {
		t.Fatalf("dormente nao devia dar erro: %v", err)
	}
	if client != nil || set != nil {
		t.Fatalf("dormente devia devolver (nil,nil), obtido client=%v set=%v", client, set)
	}
}

// TestParseBrokerVaultFromEnv_FailClosed: ADDR presente mas sem token ⇒ ABORTA
// (ErrBadBrokerVault), no molde de ErrBadVaultDSAR — nunca degrada para dormente.
func TestParseBrokerVaultFromEnv_FailClosed(t *testing.T) {
	t.Setenv("AOS_BROKER_VAULT_ADDR", "https://vault:8200")
	t.Setenv("AOS_BROKER_VAULT_TOKEN_PATH", "")
	if _, _, err := parseBrokerVaultFromEnv(); !errors.Is(err, ErrBadBrokerVault) {
		t.Fatalf("addr sem token devia dar ErrBadBrokerVault, obtido %v", err)
	}

	// token path aponta a ficheiro inexistente ⇒ ABORTA.
	t.Setenv("AOS_BROKER_VAULT_TOKEN_PATH", filepath.Join(t.TempDir(), "nao-existe"))
	if _, _, err := parseBrokerVaultFromEnv(); !errors.Is(err, ErrBadBrokerVault) {
		t.Fatalf("token ilegivel devia dar ErrBadBrokerVault, obtido %v", err)
	}
}

// TestParseBrokerVaultFromEnv_Preparado: ADDR + token de ficheiro ⇒ constrói o
// cliente Vault REAL (KV v2) e devolve o material público para o banner.
func TestParseBrokerVaultFromEnv_Preparado(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("s.brokertoken\n"), 0o600); err != nil {
		t.Fatalf("escrever token: %v", err)
	}
	t.Setenv("AOS_BROKER_VAULT_ADDR", "https://vault:8200")
	t.Setenv("AOS_BROKER_VAULT_TOKEN_PATH", tokenFile)
	t.Setenv("AOS_BROKER_VAULT_KV_MOUNT", "kv-aos")

	client, set, err := parseBrokerVaultFromEnv()
	if err != nil {
		t.Fatalf("config valida nao devia falhar: %v", err)
	}
	if client == nil {
		t.Fatal("cliente Vault REAL devia ser construido")
	}
	if set == nil || set.Addr != "https://vault:8200" || set.KVMount != "kv-aos" {
		t.Fatalf("settings do banner incorrectas: %+v", set)
	}
}

// TestBrokerVaultPostureBanner declara o modo com honestidade: dormente vs
// configurado-mas-troca-pendente (nunca "broker ligado").
func TestBrokerVaultPostureBanner(t *testing.T) {
	dormant := brokerVaultPostureBanner(nil)
	if len(dormant) != 1 || !strings.Contains(dormant[0], "DORMENTE") {
		t.Fatalf("banner dormente inesperado: %v", dormant)
	}

	configured := brokerVaultPostureBanner(&brokerVaultSettings{Addr: "https://vault:8200", KVMount: "secret"})
	if len(configured) != 1 {
		t.Fatalf("banner configurado devia ter 1 linha: %v", configured)
	}
	line := configured[0]
	// tem de declarar CONFIGURADO e, sobretudo, que a TROCA está PENDENTE (AOS-265).
	for _, want := range []string{"CONFIGURADO", "KV v2", "PENDENTE", "AOS-265"} {
		if !strings.Contains(line, want) {
			t.Errorf("banner configurado nao menciona %q: %s", want, line)
		}
	}
	// e NUNCA deve anunciar o broker como "ligado".
	if strings.Contains(strings.ToLower(line), "broker ligado") {
		t.Errorf("banner nao deve anunciar 'broker ligado': %s", line)
	}
}
