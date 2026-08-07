package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Testa a guarda fail-closed ErrProductionNeedsDurableApproval (AOS-021): sob
// AOS_MODE=production COM aprovadores four-eyes configurados, a EXECUÇÃO DURÁVEL é
// obrigatória.
//
// PORQUÊ (decisão do dono — exigir, não degradar): o bridge negação→aprovação→reexecução
// depende dela em dois pontos. (1) Reproduzir o turno escalado com fidelidade: o log
// durável NÃO guarda os inputs das tool calls (ver durable/resume.go), só a captura de
// replay os tem — sem isso a acção aprovada nunca voltaria a ser apresentada de forma
// idêntica, e a amarra da preview nunca bateria. (2) Impedir a dupla execução das
// activities JÁ APLICADAS do mesmo turno, que é papel do step-ledger. Sem execução
// durável ficaria um four-eyes que verifica assinaturas e não destrava nada.

// prodApprovalEnvBase fixa uma superfície de produção que PASSA as guardas anteriores,
// COM aprovadores four-eyes e SEM execução durável (o gatilho desta guarda).
func prodApprovalEnvBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave de teste: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)

	t.Setenv("AOS_MODE", "production")
	// identidade ENDURECIDA.
	t.Setenv("AOS_ISSUER_ID", "iss:aos-021")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))
	t.Setenv("AOS_ISSUER_KEY_PATH", "")
	t.Setenv("AOS_HUMANS", "operator")
	// soberania FORTE.
	t.Setenv("AOS_BOARD_REGIONS", "board:demo=eu")
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp.example/realms/aos")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	t.Setenv("AOS_SOVEREIGN_OIDC_JWKS_URI", "")
	t.Setenv("AOS_SOVEREIGN_OIDC_MAX_AGE", "")
	t.Setenv("AOS_SOVEREIGN_OIDC_REQUIRE_JTI", "")
	// substrato: SEM WORM durável e SEM execução durável (para isolar ESTA guarda da
	// guarda da KEK, que dispara com qualquer substrato durável).
	t.Setenv("AOS_WORM_PATH", "")
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_DURABLE_EXECUTION", "")
	t.Setenv("AOS_DSAR_VAULT_ADDR", "")
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", "")
	// APROVADORES four-eyes configurados — o gatilho.
	t.Setenv("AOS_APPROVERS_FILE", writeApproversFile(t, approversBody(t)))
	// higiene.
	t.Setenv("AOS_API_ADDR", "")
	t.Setenv("AOS_OTLP_ENDPOINT", "")
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_RETENTION_VERSION", "")
	t.Setenv("AOS_RETENTION_PERIODS", "")
	t.Setenv("AOS_OPERATORS", "")
	t.Setenv("AOS_HUMAN_OIDC_ISSUER", "")
	t.Setenv("AOS_ATTESTATION_VERIFIER_URL", "")
	return dir
}

// approversBody devolve o JSON de 2 aprovadores distintos com autoridade para as classes
// de risco (o helper writeApproversFile é partilhado com control_plane_config_test.go).
func approversBody(t *testing.T) string {
	t.Helper()
	keyHex := func() string {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("gerar pubkey de aprovador: %v", err)
		}
		return hex.EncodeToString(pub)
	}
	return `{"approvers":[
	  {"principal":"human:alice","pubkey":"` + keyHex() + `","authority":["approve:danger","approve:gray","approve:safe"]},
	  {"principal":"human:bob","pubkey":"` + keyHex() + `","authority":["approve:danger","approve:gray","approve:safe"]}
	]}`
}

// TestProductionRefusesFourEyesWithoutDurableExecution: produção + four-eyes sem execução
// durável ⇒ RECUSA o arranque.
func TestProductionRefusesFourEyesWithoutDurableExecution(t *testing.T) {
	prodApprovalEnvBase(t)
	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsDurableApproval) {
		t.Fatalf("producao + four-eyes SEM execucao duravel devia dar ErrProductionNeedsDurableApproval, veio: %v", err)
	}
}

// TestProductionAcceptsFourEyesWithDurableExecution é o contraste (prova que a guarda não
// é vacuosa): ligando a execução durável (e a custódia de KEK que o substrato durável
// passa a exigir), esta guarda deixa de disparar.
func TestProductionAcceptsFourEyesWithDurableExecution(t *testing.T) {
	dir := prodApprovalEnvBase(t)
	t.Setenv("AOS_DURABLE_EXECUTION", "1")
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(dir, "events.wal"))
	t.Setenv("AOS_DSAR_VAULT_ADDR", "https://vault.example:8200") // guarda da KEK (AOS-215)

	_, err := nodeConfigFromEnv()
	if errors.Is(err, ErrProductionNeedsDurableApproval) {
		t.Fatalf("com execucao duravel esta guarda NAO devia disparar; err=%v", err)
	}
}

// TestNonProductionAllowsFourEyesWithoutDurableExecution: fora de produção a exigência
// não se aplica (dev continua a poder experimentar o four-eyes).
func TestNonProductionAllowsFourEyesWithoutDurableExecution(t *testing.T) {
	prodApprovalEnvBase(t)
	t.Setenv("AOS_MODE", "")
	if _, err := nodeConfigFromEnv(); errors.Is(err, ErrProductionNeedsDurableApproval) {
		t.Fatalf("fora de producao a exigencia nao se aplica; err=%v", err)
	}
}

// TestErrProductionNeedsDurableApproval_ExplicaOPorque: a mensagem tem de dizer ao
// operador o que ligar E porquê — um erro fail-closed que não explica gera contorno cego.
func TestErrProductionNeedsDurableApproval_ExplicaOPorque(t *testing.T) {
	msg := ErrProductionNeedsDurableApproval.Error()
	for _, quer := range []string{"AOS_DURABLE_EXECUTION", "inputs das tool calls", "dupla execucao"} {
		if !strings.Contains(msg, quer) {
			t.Fatalf("a mensagem devia conter %q; msg=%s", quer, msg)
		}
	}
}
