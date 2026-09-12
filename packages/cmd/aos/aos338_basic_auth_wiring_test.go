package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AOS-338 — o wiring do nó para a basic-auth do verificador de attestation.
//
// O adaptador tem os seus próprios testes (`integration/aos338_basic_auth_test.go`); estes
// provam o que só se vê aqui: que a credencial entra por FICHEIRO MONTADO, que um ficheiro vazio
// ABORTA — o caminho da attestation era o outlier que não o fazia —, e que o banner declara qual
// o esquema composto em vez de dizer só «LIGADA».

const (
	aos338Utilizador = "svc-attestation"
	aos338Senha      = "s3nh4-montada-que-nao-pode-vazar"
)

func aos338Ficheiro(t *testing.T, conteudo string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(p, []byte(conteudo), 0o600); err != nil {
		t.Fatalf("escrever credencial: %v", err)
	}
	return p
}

func TestAOS338_BasicVemDeFicheiroMontado(t *testing.T) {
	// Limpa o irmão para não herdar da máquina (molde de `aos177_attestation_wiring_test.go`):
	// com os dois definidos o construtor abortaria, e o teste falharia por uma razão que não é
	// a sua (AOS-338, achado B5).
	t.Setenv("AOS_ATTESTATION_VERIFIER_TOKEN_PATH", "")
	const comEspacos = " " + aos338Utilizador + ":" + aos338Senha + " "
	t.Setenv("AOS_ATTESTATION_VERIFIER_BASIC_PATH", aos338Ficheiro(t, comEspacos+"\n"))

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	// APARA O TERMINADOR DE LINHA, E SÓ ELE. Os espaços das pontas SOBREVIVEM: o `-u` do curl
	// preserva um espaço final numa senha, e apará-lo corromperia uma credencial legítima sem
	// nada no arranque a explicá-lo (AOS-338, achado M4 da revisão). O terminador de linha é
	// do ficheiro; o resto é da credencial.
	if cfg.AttestationVerifierBasic != comEspacos {
		t.Fatalf("AttestationVerifierBasic = %q, quer %q", cfg.AttestationVerifierBasic, comEspacos)
	}
}

// TestAOS338_CredencialVaziaAborta fecha o defeito que o caminho da attestation tinha e os dois
// Vaults não: um ficheiro em branco era lido em silêncio e o nó passava a falar SEM autenticação
// nenhuma, sem nada no arranque a dizê-lo. Quem monta um ficheiro está a declarar que quer
// autenticação; um ficheiro vazio é erro de montagem, não escolha.
func TestAOS338_CredencialVaziaAborta(t *testing.T) {
	casos := []struct{ nome, env string }{
		{"basic", "AOS_ATTESTATION_VERIFIER_BASIC_PATH"},
		{"bearer", "AOS_ATTESTATION_VERIFIER_TOKEN_PATH"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Setenv(c.env, aos338Ficheiro(t, "   \n\t "))
			_, err := nodeConfigFromEnv()
			if err == nil {
				t.Fatal("um ficheiro de credencial VAZIO tinha de abortar o arranque")
			}
			if !errors.Is(err, ErrBadAttestationCredential) {
				t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
			}
			if !strings.Contains(err.Error(), c.env) {
				t.Errorf("o erro devia nomear a variavel que o operador tem de corrigir: %v", err)
			}
		})
	}
}

// TestAOS338_CredencialIlegivelAborta — o outro ramo do fail-closed. O erro nomeia o CAMINHO
// (que o operador precisa) e nunca o conteúdo.
func TestAOS338_CredencialIlegivelAborta(t *testing.T) {
	t.Setenv("AOS_ATTESTATION_VERIFIER_BASIC_PATH", filepath.Join(t.TempDir(), "nao-existe"))
	_, err := nodeConfigFromEnv()
	if err == nil {
		t.Fatal("um ficheiro inexistente tinha de abortar")
	}
	if !errors.Is(err, ErrBadAttestationCredential) {
		t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
	}
}

// TestAOS338_NenhumaCredencialNaoAborta é o CONTROLO do wiring: sem as duas variáveis o nó
// continua a arrancar. Um fail-closed que negasse sempre seria pior do que o defeito.
func TestAOS338_NenhumaCredencialNaoAborta(t *testing.T) {
	t.Setenv("AOS_ATTESTATION_VERIFIER_BASIC_PATH", "")
	t.Setenv("AOS_ATTESTATION_VERIFIER_TOKEN_PATH", "")
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("sem credenciais o no tem de arrancar: %v", err)
	}
	// NÃO SE IMPRIME O VALOR nem no ramo de falha — é a disciplina do resto deste ficheiro, e
	// um teste que a quebra ensina a quebrá-la (AOS-338, achado B4). Diz-se QUAL dos dois está
	// preenchido, que é o que diagnostica, e o teste cobria os dois mas só imprimia um.
	if cfg.AttestationVerifierBasic != "" || cfg.AttestationVerifierToken != "" {
		t.Errorf("nao devia haver credencial nenhuma: basic-preenchido=%v bearer-preenchido=%v",
			cfg.AttestationVerifierBasic != "", cfg.AttestationVerifierToken != "")
	}
}

// TestAOS338_ACredencialNaoEntraPorVariavelDeAmbiente fixa a regra do ADR-006, que é a mesma que
// motivou a recusa do URL no AOS-333: violá-la aqui reabriria a fuga por outra porta. O valor da
// variável é um CAMINHO — se alguém lá puser o par directamente, isso não é uma credencial
// válida e o arranque tem de recusar, não de a usar.
func TestAOS338_ACredencialNaoEntraPorVariavelDeAmbiente(t *testing.T) {
	t.Setenv("AOS_ATTESTATION_VERIFIER_BASIC_PATH", aos338Utilizador+":"+aos338Senha)
	_, err := nodeConfigFromEnv()
	if err == nil {
		t.Fatal("um par posto DIRECTAMENTE na variavel nao e um caminho — tinha de abortar")
	}
	if !errors.Is(err, ErrBadAttestationCredential) {
		t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
	}
	if strings.Contains(err.Error(), aos338Senha) {
		t.Errorf("o erro ecoa a senha que o operador pos na variavel: %v", err)
	}
}
