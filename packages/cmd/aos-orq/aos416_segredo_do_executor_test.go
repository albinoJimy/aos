package main

// AOS-416 — O EXECUTOR COMPUNHA-SE SOBRE CREDENCIAIS QUE NÃO CONSEGUIA LER.
//
// Em produção o `secrets/reader-client-secret` está em `0400` do utilizador `aos` e o contentor
// corre como 65532. Medido a 2026-09-20 com o uid e o ficheiro reais:
//
//	docker run --rm --user 65532:65532 -v .../reader-client-secret:/s:ro busybox \
//	  -c "cat /s >/dev/null && echo LEGIVEL || echo ILEGIVEL"
//	cat: can't open '/s': Permission denied
//
// O arranque só via que a string do caminho não estava vazia. O banner dizia COMPOSTO e a falha
// aparecia na primeira submissão de nó — o modo de falha do AOS-413 (o plano despacha, nada
// executa) a voltar por outra porta.
//
// Estes testes medem o ARRANQUE. Os que dependem de bits POSIX saltam em Windows, onde o Go
// reporta 0666/0444 seja qual for a ACL: afirmar postura a partir daí seria inventar. Foram
// corridos em Linux, sob `setpriv --reuid=65532`, que é a identidade real do contentor.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// credencialEm escreve um ficheiro de credencial com o modo pedido.
func credencialEm(t *testing.T, nome string, modo os.FileMode) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), nome)
	if err := os.WriteFile(caminho, []byte("valor-qualquer\n"), modo); err != nil {
		t.Fatalf("escrever %s: %v", nome, err)
	}
	if err := os.Chmod(caminho, modo); err != nil {
		t.Fatalf("chmod %s: %v", nome, err)
	}
	return caminho
}

// ambienteDoExecutor põe as variáveis do executor. Ambas as credenciais são ficheiros reais e
// legíveis, salvo o que cada teste alterar.
func ambienteDoExecutor(t *testing.T, nhi, segredo string) {
	t.Helper()
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ORQ_NODE_URL", "http://aos:8080")
	t.Setenv("AOS_ORQ_NODE_CREDENTIAL_FILE", nhi)
	t.Setenv("AOS_ORQ_OIDC_TOKEN_URL", "https://idp:8443/realms/aos/protocol/openid-connect/token")
	t.Setenv("AOS_ORQ_OIDC_CLIENT_ID", "aos-reader")
	t.Setenv("AOS_ORQ_OIDC_CLIENT_SECRET_FILE", segredo)
}

// modoLegivel é o modo que o repositório usa para tudo o que o contentor lê. A fronteira do
// segredo é o directório `secrets/`, que o `bootstrap.sh` cria em 0700.
const modoLegivel = os.FileMode(0o644)

func saltaSeModosNaoContam(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bits POSIX não são significativos em Windows; o alvo é o contentor Linux")
	}
	if os.Geteuid() == 0 {
		t.Skip("como root nenhum modo torna um ficheiro ilegível")
	}
}

// exigeMensagemAccionavel impede o «permission denied» cru: foi a adivinha do operador que
// produziu a cópia 0444 do segredo na validação do AOS-415, e é dessa cópia que o ticket nasceu.
func exigeMensagemAccionavel(t *testing.T, err error, variavel string) {
	t.Helper()
	if !errors.Is(err, ErrNodeClientConfig) {
		t.Fatalf("erro = %v, quero que embrulhe ErrNodeClientConfig", err)
	}
	for _, exigido := range []string{variavel, "65532", "chmod 0644", "NÃO faça uma cópia"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Fatalf("a mensagem não diz %q — não é accionável: %v", exigido, err)
		}
	}
	if strings.Contains(err.Error(), "chown") {
		t.Fatalf("a mensagem manda fazer `chown`, que exige root e tira a leitura ao backup: %v", err)
	}
}

// TestAOS416_SegredoIlegivelRecusaNoArranque é o defeito medido em produção: o ficheiro existe,
// a configuração está completa, e o processo não o consegue ler.
func TestAOS416_SegredoIlegivelRecusaNoArranque(t *testing.T) {
	saltaSeModosNaoContam(t)
	ambienteDoExecutor(t, credencialEm(t, "nhi.jwt", modoLegivel), credencialEm(t, "reader-client-secret", 0o000))

	cli, err := nodeClientDoAmbiente()
	if err == nil {
		t.Fatalf("o arranque compôs o executor sobre um segredo ILEGÍVEL (cli=%v) — o banner diria COMPOSTO e a falha só apareceria na primeira submissão (AOS-416)", cli != nil)
	}
	exigeMensagemAccionavel(t, err, "AOS_ORQ_OIDC_CLIENT_SECRET_FILE")
}

// TestAOS416_NHIIlegivelRecusaNoArranque é a outra metade da mesma parede: o NHI do run é copiado
// à mão pelo operador com `umask 077`, o que dá 0600 do utilizador dele — ilegível pelo contentor,
// com exactamente o mesmo sintoma.
func TestAOS416_NHIIlegivelRecusaNoArranque(t *testing.T) {
	saltaSeModosNaoContam(t)
	ambienteDoExecutor(t, credencialEm(t, "nhi.jwt", 0o000), credencialEm(t, "reader-client-secret", modoLegivel))

	if _, err := nodeClientDoAmbiente(); err == nil {
		t.Fatal("o arranque compôs o executor sobre um NHI do run ILEGÍVEL (AOS-416)")
	} else {
		exigeMensagemAccionavel(t, err, "AOS_ORQ_NODE_CREDENTIAL_FILE")
	}
}

// TestAOS416_OModoDaConvencaoNaoERecusado é o controlo que mata a primeira versão desta correcção,
// que recusava em produção qualquer ficheiro com bits de grupo ou de outros. Isso teria recusado a
// configuração CORRECTA: a fronteira é o directório `secrets/` em 0700, e `0644` lá dentro é o que
// o `model-api.key` e o `vault-token` já usam.
func TestAOS416_OModoDaConvencaoNaoERecusado(t *testing.T) {
	saltaSeModosNaoContam(t)
	ambienteDoExecutor(t, credencialEm(t, "nhi.jwt", modoLegivel), credencialEm(t, "reader-client-secret", modoLegivel))

	cli, err := nodeClientDoAmbiente()
	if err != nil {
		t.Fatalf("o modo %#o, que é a convenção do repositório para o que o contentor lê, foi RECUSADO: %v", modoLegivel, err)
	}
	if cli == nil || cli.bearer == nil {
		t.Fatal("o executor devia ter Bearer composto")
	}
}

// TestAOS416_CredencialAusenteDizQueEstaAusente separa os problemas de operação que a mensagem
// antiga misturava: «não configurado», «configurado e ausente» e «presente e ilegível» exigem
// gestos diferentes.
func TestAOS416_CredencialAusenteDizQueEstaAusente(t *testing.T) {
	ausente := filepath.Join(t.TempDir(), "nao-existe")
	ambienteDoExecutor(t, credencialEm(t, "nhi.jwt", modoLegivel), ausente)

	_, err := nodeClientDoAmbiente()
	if err == nil {
		t.Fatal("o arranque compôs o executor sobre um segredo AUSENTE (AOS-416)")
	}
	if !strings.Contains(err.Error(), "NÃO existe") {
		t.Fatalf("a mensagem não distingue ausente de ilegível: %v", err)
	}
}

// TestAOS416_CredencialVaziaERecusada fecha o caso que o `[[ -s ]]` do provisionamento também
// vigia: um ficheiro criado mas nunca preenchido.
func TestAOS416_CredencialVaziaERecusada(t *testing.T) {
	vazio := filepath.Join(t.TempDir(), "vazio")
	if err := os.WriteFile(vazio, nil, modoLegivel); err != nil {
		t.Fatalf("escrever: %v", err)
	}
	ambienteDoExecutor(t, credencialEm(t, "nhi.jwt", modoLegivel), vazio)

	if _, err := nodeClientDoAmbiente(); err == nil {
		t.Fatal("o arranque aceitou um segredo VAZIO (AOS-416)")
	}
}

// TestAOS416_OProvisionamentoDaLeituraAoContentor é o sensor do lado do servidor. Amarra-se à
// CADEIA EXECUTÁVEL e não à prosa: o comentário deste bloco nomeia `chmod` e `0644` várias vezes,
// e um `grep` ingénuo sobreviveria a comentar a linha que interessa — foi o que a revisão
// adversarial demonstrou na primeira versão deste teste. É a mesma disciplina de
// `scripts/ci/deploy-gate-lint.sh`.
func TestAOS416_OProvisionamentoDaLeituraAoContentor(t *testing.T) {
	raiz := raizDoRepoParaAOS416(t)
	bruto, err := os.ReadFile(filepath.Join(raiz, "deploy", "server", "provision-identity.sh"))
	if err != nil {
		t.Fatalf("ler o provisionamento: %v", err)
	}

	var executavel []string
	for _, linha := range strings.Split(string(bruto), "\n") {
		if semComentario := strings.TrimSpace(linha); semComentario != "" && !strings.HasPrefix(semComentario, "#") {
			executavel = append(executavel, semComentario)
		}
	}
	corpo := strings.Join(executavel, "\n")

	if !strings.Contains(corpo, `chmod 644 "${SECRETS}/reader-client-secret"`) {
		t.Fatal("nenhuma LINHA EXECUTÁVEL dá leitura ao uid do contentor sobre o reader-client-secret — em 0400 do utilizador `aos` o contentor NÃO o lê (AOS-416)")
	}
	if strings.Contains(corpo, `chown 65532`) {
		t.Fatal("o provisionamento faz `chown 65532`: exige root, que este script não tem, e tira a leitura ao backup.sh, que corre no cron do `aos` e tara o secrets/ inteiro (AOS-416)")
	}
	if strings.Contains(corpo, `chmod 400 "${SECRETS}/reader-client-secret"`) {
		t.Fatal("o provisionamento volta a pôr o segredo em 0400 — é o defeito de origem (AOS-416)")
	}
}

// TestAOS416_ODirectorioDosSegredosEAFronteira fixa a premissa em que a decisão de modo assenta.
// Se alguém afrouxar o `secrets/`, a escolha de `0644` para os ficheiros deixa de se sustentar —
// e é aqui que isso avermelha, em vez de ficar a depender de um comentário.
func TestAOS416_ODirectorioDosSegredosEAFronteira(t *testing.T) {
	raiz := raizDoRepoParaAOS416(t)
	bruto, err := os.ReadFile(filepath.Join(raiz, "deploy", "server", "bootstrap.sh"))
	if err != nil {
		t.Fatalf("ler o bootstrap: %v", err)
	}
	if !strings.Contains(string(bruto), `install -d -m 700 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "${APP_DIR}/secrets"`) {
		t.Fatal("o `secrets/` deixou de ser criado em 0700 — a fronteira do segredo deixou de existir, e o 0644 dos ficheiros lá dentro passa a expô-los (AOS-416)")
	}
}

// raizDoRepoParaAOS416 sobe até à raiz do repositório.
func raizDoRepoParaAOS416(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "deploy", "server")); err == nil {
			return dir
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			break
		}
		dir = pai
	}
	t.Skip("raiz do repositório não encontrada")
	return ""
}
