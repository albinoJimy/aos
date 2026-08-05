package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Testa a guarda fail-closed ErrProductionNeedsDurableKEK (AOS-215/AOS-216): sob
// AOS_MODE=production COM substrato durável (AOS_WORM_PATH e/ou AOS_DURABLE_EXECUTION), a KEK
// por-titular NÃO pode ficar no vault in-memory de referência — senão um restart torna o
// conteúdo selado (D6/captura) permanentemente indecifrável (over-erasure silenciosa). É a
// simétrica de ErrDurableExecutionNeedsDurableSubstrate: a chave tem de ser tão durável quanto
// o substrato que cifra. Antes deste ticket a KEK-em-memória só AVISAVA; agora RECUSA.

// prodKEKEnvBase fixa uma superfície de produção que PASSA as guardas anteriores (identidade
// endurecida + soberania forte), com substrato durável por WORM e SEM custódia de KEK durável.
// Cada teste ajusta a última variável (o vault) para exercitar a guarda. Devolve o tempdir.
func prodKEKEnvBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave de teste: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)

	t.Setenv("AOS_MODE", "production")
	// identidade ENDURECIDA (passa ErrProductionNeedsHardenedIdentity).
	t.Setenv("AOS_ISSUER_ID", "iss:aos-215")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))
	t.Setenv("AOS_ISSUER_KEY_PATH", "")
	t.Setenv("AOS_HUMANS", "operator")
	// soberania FORTE (passa ErrProductionNeedsSovereignAuthority): issuer+audience bastam.
	t.Setenv("AOS_BOARD_REGIONS", "board:demo=eu")
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp.example/realms/aos")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	t.Setenv("AOS_SOVEREIGN_OIDC_JWKS_URI", "")
	t.Setenv("AOS_SOVEREIGN_OIDC_MAX_AGE", "")
	t.Setenv("AOS_SOVEREIGN_OIDC_REQUIRE_JTI", "")
	// SUBSTRATO DURÁVEL por WORM (o gatilho da guarda), execução durável desligada.
	t.Setenv("AOS_WORM_PATH", filepath.Join(dir, "worm.wal"))
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_DURABLE_EXECUTION", "")
	// CUSTÓDIA DA KEK: por omissão AUSENTE (vault in-memory) — os testes ajustam.
	t.Setenv("AOS_DSAR_VAULT_ADDR", "")
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", "")
	// higiene: variáveis que, herdadas da máquina, contaminariam a config.
	t.Setenv("AOS_API_ADDR", "")
	t.Setenv("AOS_OTLP_ENDPOINT", "")
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_RETENTION_VERSION", "")
	t.Setenv("AOS_RETENTION_PERIODS", "")
	t.Setenv("AOS_OPERATORS", "")
	t.Setenv("AOS_APPROVERS_FILE", "")
	t.Setenv("AOS_HUMAN_OIDC_ISSUER", "")
	return dir
}

// TestProductionRefusesInMemoryKEKWithDurableWORM: WORM durável + sem vault ⇒ RECUSA.
func TestProductionRefusesInMemoryKEKWithDurableWORM(t *testing.T) {
	prodKEKEnvBase(t)
	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsDurableKEK) {
		t.Fatalf("producao + WORM duravel + KEK em memoria devia dar ErrProductionNeedsDurableKEK, veio: %v", err)
	}
}

// TestProductionRefusesInMemoryKEKWithDurableExecution: execução durável + sem vault ⇒ RECUSA
// (o outro caminho de substrato durável, independente do WORM).
func TestProductionRefusesInMemoryKEKWithDurableExecution(t *testing.T) {
	dir := prodKEKEnvBase(t)
	t.Setenv("AOS_WORM_PATH", "")
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(dir, "events.wal"))
	t.Setenv("AOS_DURABLE_EXECUTION", "1")
	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsDurableKEK) {
		t.Fatalf("producao + execucao duravel + KEK em memoria devia dar ErrProductionNeedsDurableKEK, veio: %v", err)
	}
}

// TestProductionAcceptsDurableKEK: com AOS_DSAR_VAULT_ADDR + token, a guarda NÃO dispara.
func TestProductionAcceptsDurableKEK(t *testing.T) {
	dir := prodKEKEnvBase(t)
	tok := filepath.Join(dir, "vault-token")
	if err := os.WriteFile(tok, []byte("dev-root"), 0o600); err != nil {
		t.Fatalf("escrever token de teste: %v", err)
	}
	t.Setenv("AOS_DSAR_VAULT_ADDR", "http://vault:8200")
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
	if _, err := nodeConfigFromEnv(); errors.Is(err, ErrProductionNeedsDurableKEK) {
		t.Fatalf("producao COM custodia de KEK duravel NAO devia dar ErrProductionNeedsDurableKEK, veio: %v", err)
	}
}

// TestReferenceModeAllowsInMemoryKEK: fora de produção a KEK-em-memória demo-grade é aceite.
func TestReferenceModeAllowsInMemoryKEK(t *testing.T) {
	prodKEKEnvBase(t)
	t.Setenv("AOS_MODE", "") // modo de referência.
	if _, err := nodeConfigFromEnv(); errors.Is(err, ErrProductionNeedsDurableKEK) {
		t.Fatalf("fora de producao a KEK-em-memoria e aceite; nao devia dar ErrProductionNeedsDurableKEK, veio: %v", err)
	}
}
