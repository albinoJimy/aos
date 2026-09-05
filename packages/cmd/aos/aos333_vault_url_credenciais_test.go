package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Testa, ao nível do NÓ, que uma URL de Vault com credenciais embutidas é recusada nos DOIS
// eixos e que a senha não aparece em lado nenhum — nem no erro, nem no banner de arranque.
//
// O helper partilhado (`integration.CheckSecureTransportURL`) tem os seus próprios testes; estes
// provam o que só se vê aqui: que os dois chamadores herdam a recusa, e que o banner — que
// imprimia `AOS_BROKER_VAULT_ADDR` CRU — deixou de poder vazar a senha para o log.
func aos333Env(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tok := filepath.Join(dir, "vault.token")
	if err := os.WriteFile(tok, []byte("token-de-teste"), 0o600); err != nil {
		t.Fatalf("escrever token: %v", err)
	}
	t.Setenv("AOS_BROKER_VAULT_TOKEN_PATH", tok)
	t.Setenv("AOS_BROKER_VAULT_KV_MOUNT", "")
	t.Setenv("AOS_BROKER_VAULT_PATH_PREFIX", "")
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
	return dir
}

const aos333Senha = "s3nh4-do-vault-que-nao-pode-vazar"

func TestAOS333_OsDoisVaultsRecusamCredenciaisNoURL(t *testing.T) {
	addr := "https://admin:" + aos333Senha + "@vault.interno:8200"

	t.Run("broker", func(t *testing.T) {
		aos333Env(t)
		t.Setenv("AOS_BROKER_VAULT_ADDR", addr)

		_, _, err := parseBrokerVaultFromEnv()
		if !errors.Is(err, ErrInsecureBrokerVaultAddr) {
			t.Fatalf("erro = %v, queria ErrInsecureBrokerVaultAddr", err)
		}
		if strings.Contains(err.Error(), aos333Senha) {
			t.Errorf("a recusa vaza a senha: %v", err)
		}
	})

	t.Run("custodia da KEK", func(t *testing.T) {
		aos333Env(t)
		t.Setenv("AOS_DSAR_VAULT_ADDR", addr)

		_, err := parseVaultDSARFromEnv()
		if !errors.Is(err, ErrInsecureVaultDSARAddr) {
			t.Fatalf("erro = %v, queria ErrInsecureVaultDSARAddr", err)
		}
		if strings.Contains(err.Error(), aos333Senha) {
			t.Errorf("a recusa vaza a senha: %v", err)
		}
	})
}

// TestAOS333_BannerNaoImprimeCredenciais é DEFESA EM PROFUNDIDADE, e é deliberado que teste um
// estado que o parser já não deixa acontecer.
//
// A recusa acima e a redacção aqui fecham a mesma fuga em dois sítios independentes. Se alguém
// amanhã relaxar o critério de transporte — ou compuser `Config.BrokerVault` por outra via que
// não o ambiente —, o banner continua a não imprimir a senha. Um controlo que só existe no
// parser deixa de existir no dia em que o parser mudar.
func TestAOS333_BannerNaoImprimeCredenciais(t *testing.T) {
	t.Parallel()
	linhas := brokerVaultPostureBanner(&brokerVaultSettings{
		Addr:    "https://admin:" + aos333Senha + "@vault.interno:8200",
		KVMount: "secret",
	})
	if len(linhas) == 0 {
		t.Fatal("o banner devia produzir pelo menos uma linha")
	}
	todo := strings.Join(linhas, "\n")
	if strings.Contains(todo, aos333Senha) {
		t.Error("o banner imprime a senha do Vault — vai em texto claro para o log de arranque")
	}
	if strings.Contains(todo, "admin") {
		t.Error("o banner imprime o utilizador, que numa URL de Vault identifica o principal")
	}
	// CONTROLO: o banner continua a dizer ONDE o nó fala. Redigir tudo seria trocar uma fuga
	// por um banner inútil — e o banner existe para o operador saber o que está composto.
	if !strings.Contains(todo, "vault.interno:8200") {
		t.Errorf("o banner devia continuar a nomear o host e a porta:\n%s", todo)
	}
}
