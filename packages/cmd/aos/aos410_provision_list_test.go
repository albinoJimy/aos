package main

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// AOS-410 — O CONTROLO DO PROVISIONAMENTO LÊ O 404 COMO O NÓ O LÊ.
//
// Observado em produção a 2026-09-18: `provision-identity.sh` abortava no passo 5 com «o token do
// no NAO consegue listar transit/keys», sobre um token que o /readyz do nó dava como pronto. O
// `vault list` sai com 2 num motor Transit VAZIO («No value found at transit/keys/», um 404) tal
// como num 403, e o script lia os dois como falta de permissão. O nó (provaDeCapacidade) aceita o
// 404 — o controlo do provisionamento tem de usar o mesmo critério, e continuar fail-closed para
// o 403 e para qualquer erro que não reconheça.
//
// O teste corre o bloco REAL do script em bash, com um `nodex` falso: um teste sobre uma cópia do
// bloco deixaria de ver a regressão no dia em que o script mudasse.
// ---------------------------------------------------------------------------

const provisionIdentityScript = "../../../deploy/server/provision-identity.sh"

// blocoListTransit extrai do script o bloco que verifica o `list` de transit/keys.
func blocoListTransit(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(provisionIdentityScript)
	if err != nil {
		t.Fatalf("ler %s: %v", provisionIdentityScript, err)
	}
	re := regexp.MustCompile(`(?ms)^if ! LIST_OUT="\$\(nodex vault list transit/keys 2>&1\)"; then$.*?^unset LIST_OUT$`)
	bloco := re.FindString(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if bloco == "" {
		t.Fatal("o bloco do `vault list transit/keys` não foi encontrado no provision-identity.sh — o controlo mudou de forma ou desapareceu")
	}
	return bloco
}

func TestAOS410_ListTransitVazioNaoAbortaEo403Aborta(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash indisponível neste host")
	}
	bloco := blocoListTransit(t)

	// Saídas do CLI `vault list` para cada resposta do Vault. O código de saída é 2 em todas as
	// falhas — é exactamente por isso que o código não chega para decidir.
	casos := []struct {
		nome, stdout, stderr string
		rc                   int
		passa                bool
	}{
		{"com chaves (200)", "Keys\n----\naos-kek-x\n", "", 0, true},
		{"motor vazio (404)", "", "No value found at transit/keys/\n", 2, true},
		{"sem permissão (403)", "", "Error listing transit/keys/: Error making API request.\n\nURL: GET https://127.0.0.1:8200/v1/transit/keys?list=true\nCode: 403. Errors:\n\n* permission denied\n", 2, false},
		{"erro desconhecido", "", "Error listing transit/keys/: dial tcp 127.0.0.1:8200: connect: connection refused\n", 2, false},
		// Um 403 cuja saída também mencionasse «No value found» não pode passar por 404.
		{"403 que menciona No value found", "", "No value found at transit/keys/\nCode: 403. Errors:\n* permission denied\n", 2, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			script := "set -euo pipefail\n" +
				"log() { printf 'LOG %s\\n' \"$*\"; }\n" +
				"fail() { printf 'FAIL %s\\n' \"$*\"; exit 1; }\n" +
				"nodex() { printf '%s' \"$FAKE_STDOUT\"; printf '%s' \"$FAKE_STDERR\" >&2; return \"$FAKE_RC\"; }\n" +
				bloco + "\necho PASSOU\n"
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append(os.Environ(),
				"FAKE_STDOUT="+c.stdout, "FAKE_STDERR="+c.stderr, "FAKE_RC="+strconv.Itoa(c.rc))
			out, err := cmd.CombinedOutput()
			passou := err == nil && strings.Contains(string(out), "PASSOU")
			if passou != c.passa {
				t.Fatalf("esperado passa=%v, obtido passa=%v (err=%v)\n%s", c.passa, passou, err, out)
			}
			if !c.passa && !strings.Contains(string(out), "FAIL o token do no NAO consegue listar transit/keys") {
				t.Fatalf("a falha não chegou pelo fail() do controlo:\n%s", out)
			}
		})
	}
}

// Um `log "..."` ou `fail "..."` com backticks faz a shell executar o conteúdo como comando
// (foi assim que o passo 4b imprimia «board: command not found» e a mensagem saía sem o nome).
func TestAOS410_MensagensDoProvisionamentoSemBackticks(t *testing.T) {
	raw, err := os.ReadFile(provisionIdentityScript)
	if err != nil {
		t.Fatalf("ler %s: %v", provisionIdentityScript, err)
	}
	re := regexp.MustCompile("^\\s*(log|fail) \"[^\"]*`")
	for i, linha := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if re.MatchString(linha) {
			t.Errorf("%s:%d: backticks dentro de aspas duplas são substituição de comando: %s", provisionIdentityScript, i+1, strings.TrimSpace(linha))
		}
	}
}
