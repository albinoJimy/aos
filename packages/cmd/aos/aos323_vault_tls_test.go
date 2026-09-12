package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Testa [ErrInsecureBrokerVaultAddr] (AOS-323): o Vault do broker passa a recusar transporte
// inseguro, com o MESMO critério e o MESMO helper que o Vault da KEK já usava desde AOS-249.
//
// A ASSIMETRIA QUE ESTE TICKET FECHOU. O enunciado original dizia que «nem um nem outro valida o
// esquema». Metade estava errada, e a discovery apanhou-a: `parseVaultDSARFromEnv` já chamava
// `integration.CheckSecureTransportURL` (ErrInsecureVaultDSARAddr). O que não validava nada era o
// Vault do BROKER — endereçado pela mesma família de variáveis, a transportar o mesmo tipo de
// material, no mesmo binário. O defeito era a assimetria, não a ausência.
func brokerVaultEnvBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tok := filepath.Join(dir, "vault.token")
	if err := os.WriteFile(tok, []byte("token-de-teste"), 0o600); err != nil {
		t.Fatalf("escrever token: %v", err)
	}
	t.Setenv("AOS_BROKER_VAULT_TOKEN_PATH", tok)
	t.Setenv("AOS_BROKER_VAULT_KV_MOUNT", "")
	t.Setenv("AOS_BROKER_VAULT_PATH_PREFIX", "")
	return dir
}

func TestAOS323_BrokerVaultRecusaTransporteInseguro(t *testing.T) {
	casos := []struct{ nome, addr string }{
		{"http para host remoto", "http://vault.interno:8200"},
		{"esquema em maiusculas nao contorna", "HTTP://vault.interno:8200"},
		{"sem esquema", "vault.interno:8200"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			brokerVaultEnvBase(t)
			t.Setenv("AOS_BROKER_VAULT_ADDR", c.addr)

			if _, _, err := parseBrokerVaultFromEnv(); !errors.Is(err, ErrInsecureBrokerVaultAddr) {
				t.Fatalf("addr %q devia dar ErrInsecureBrokerVaultAddr, veio: %v", c.addr, err)
			}
		})
	}
}

// TestAOS323_BrokerVaultAceitaHTTPSELoopback é o controlo: a guarda tem de deixar passar os dois
// caminhos legítimos, senão o teste acima passaria por a config estar simplesmente partida.
//
// O loopback em http é DELIBERADO e é o mesmo que o Vault da KEK admite — é o padrão de
// desenvolvimento documentado em `vaultkeyvault.go`. Endurecer isto para https-sempre partiria o
// fluxo de dev de ambos os vaults, e é por isso que este teste existe: para que essa mudança não
// possa ser feita por acidente.
func TestAOS323_BrokerVaultAceitaHTTPSELoopback(t *testing.T) {
	casos := []string{
		"https://vault.interno:8200",
		"http://127.0.0.1:8200",
		"http://localhost:8200",
	}
	for _, addr := range casos {
		t.Run(addr, func(t *testing.T) {
			brokerVaultEnvBase(t)
			t.Setenv("AOS_BROKER_VAULT_ADDR", addr)

			client, set, err := parseBrokerVaultFromEnv()
			if errors.Is(err, ErrInsecureBrokerVaultAddr) {
				t.Fatalf("addr %q NAO devia disparar a guarda de transporte, veio: %v", addr, err)
			}
			if err != nil {
				t.Fatalf("addr %q devia compor: %v", addr, err)
			}
			if client == nil || set == nil {
				t.Fatal("addr legitimo devia produzir cliente e settings")
			}
		})
	}
}

// TestAOS323_MesmoCriterioNosDoisVaults amarra a propriedade que o ticket existe para garantir:
// o mesmo endereço tem o mesmo veredicto nos dois eixos. Um teste por eixo provaria cada guarda
// em separado e deixaria a ASSIMETRIA — que era o defeito — sem cobertura nenhuma.
func TestAOS323_MesmoCriterioNosDoisVaults(t *testing.T) {
	const inseguro = "http://vault.interno:8200"

	dir := brokerVaultEnvBase(t)
	t.Setenv("AOS_BROKER_VAULT_ADDR", inseguro)
	_, _, brokerErr := parseBrokerVaultFromEnv()

	t.Setenv("AOS_DSAR_VAULT_ADDR", inseguro)
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", filepath.Join(dir, "vault.token"))
	_, dsarErr := parseVaultDSARFromEnv()

	if brokerErr == nil || dsarErr == nil {
		t.Fatalf("os DOIS vaults deviam recusar %q — broker=%v dsar=%v", inseguro, brokerErr, dsarErr)
	}
	if !errors.Is(brokerErr, ErrInsecureBrokerVaultAddr) || !errors.Is(dsarErr, ErrInsecureVaultDSARAddr) {
		t.Errorf("cada eixo devia recusar com a sua sentinela — broker=%v dsar=%v", brokerErr, dsarErr)
	}
}
