package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// AOS-450 — o deploy corria contra a drenagem da fila de planos. Medido no deploy da v0.1.35: o CD
// sincronizou os scripts antes de trocar a imagem, o timer calhou entre os dois, e a drenagem correu
// o drenar-planos.sh NOVO com o binário ANTIGO — `failed` falso e alerta. Agora o CD anuncia o deploy
// ANTES do rsync (deploy.sh --anunciar, que espera pela drenagem em curso), o deploy.sh segura o lock
// até o nó estar saudável, e a drenagem que encontra o deploy sai 0 como ADIADA.
//
// Estes testes lêem os scripts do servidor (o molde do TestAOS443ContratoComOsScriptsDoServidor) e,
// em Linux, EXECUTAM-nos: até aqui nenhum teste corria a lógica bash (resíduo 5 do AOS-443).

// TestAOS450ContratoDoAnuncioEDoMarcador fixa as peças que três ficheiros têm de concordar entre si.
// Um lado a mudar sozinho desliga a protecção em silêncio: um caminho de marcador diferente e a
// drenagem nunca o vê; o anúncio depois do rsync e a janela volta a abrir.
func TestAOS450ContratoDoAnuncioEDoMarcador(t *testing.T) {
	deploy := lerDoRepo(t, "deploy", "server", "deploy.sh")
	drenar := lerDoRepo(t, "deploy", "server", "drenar-planos.sh")
	rollback := lerDoRepo(t, "deploy", "server", "rollback.sh")
	yml := lerDoRepo(t, ".github", "workflows", "deploy.yml")

	// O marcador e o lock: o mesmo sítio dos dois lados.
	for _, quer := range []string{`DRENAGEM_DIR="${APP_DIR}/.drenagem"`, `MARCADOR_DEPLOY="${DRENAGEM_DIR}/deploy-em-curso"`, `DRENAGEM_LOCK="${DRENAGEM_DIR}/lock"`} {
		if !strings.Contains(deploy, quer) {
			t.Errorf("deploy.sh já não tem %q", quer)
		}
	}
	for _, quer := range []string{`ESTADO_DIR="${AOS_DIR}/.drenagem"`, `DEPLOY_MARCADOR="${ESTADO_DIR}/deploy-em-curso"`, `exec 9>"${ESTADO_DIR}/lock"`} {
		if !strings.Contains(drenar, quer) {
			t.Errorf("drenar-planos.sh já não tem %q", quer)
		}
	}
	// O adiamento sai 0 e NÃO escreve o carimbo de sucesso: a função `adiar` sai antes de lá chegar.
	adiar := entre(t, drenar, "adiar() {", "\n}\n")
	if !strings.Contains(adiar, "exit 0") || strings.Contains(adiar, "ultima-ok") {
		t.Errorf("adiar() tem de sair 0 sem tocar no carimbo ultima-ok:\n%s", adiar)
	}
	// O rollback avança ao desistir; o deploy aborta por omissão.
	if !strings.Contains(rollback, `DEPLOY_AO_DESISTIR_DA_DRENAGEM="${DEPLOY_AO_DESISTIR_DA_DRENAGEM:-avancar}"`) {
		t.Error("rollback.sh deixou de pedir DEPLOY_AO_DESISTIR_DA_DRENAGEM=avancar")
	}
	if !strings.Contains(deploy, `AO_DESISTIR="${DEPLOY_AO_DESISTIR_DA_DRENAGEM:-abortar}"`) {
		t.Error("deploy.sh deixou de abortar por omissão ao desistir da drenagem")
	}

	// O anúncio corre ANTES do rsync, com o deploy.sh do CHECKOUT (o do servidor ainda é o antigo).
	anuncio := strings.Index(yml, "bash -s -- --anunciar\" \\\n            < deploy/server/deploy.sh")
	rsync := strings.Index(yml, "- name: Sincronizar deploy/server -> /opt/aos")
	troca := strings.Index(yml, "bash /opt/aos/deploy.sh '$IMAGE'")
	if anuncio < 0 || rsync < 0 || troca < 0 {
		t.Fatalf("deploy.yml: anúncio=%d rsync=%d deploy=%d — algum passo desapareceu", anuncio, rsync, troca)
	}
	if !(anuncio < rsync && rsync < troca) {
		t.Errorf("deploy.yml: a ordem tem de ser anúncio (%d) < rsync (%d) < deploy.sh (%d)", anuncio, rsync, troca)
	}
	// O passo do anúncio não pode ser opcional: um `if:` ou um `continue-on-error` deixava o rsync
	// correr sem ele — e a janela reabria com o job verde.
	passo := entre(t, yml, "- name: Segurar a drenagem da fila (antes do rsync)", "\n      - name:")
	for _, proibido := range []string{"\n        if:", "continue-on-error"} {
		if strings.Contains(passo, proibido) {
			t.Errorf("o passo do anúncio tem %q — deixou de ser obrigatório:\n%s", strings.TrimSpace(proibido), passo)
		}
	}
	// A saída de emergência: as duas variáveis do GitHub chegam ao anúncio E ao deploy, validadas
	// antes de entrarem na linha de comando remota.
	passoDeploy := entre(t, yml, "- name: Deploy\n", "\n      - name:")
	for nome, p := range map[string]string{"anúncio": passo, "deploy": passoDeploy} {
		for _, quer := range []string{
			"ESPERA: ${{ vars.DEPLOY_ESPERA_DRENAGEM_S || '2700' }}",
			"AO_DESISTIR: ${{ vars.DEPLOY_AO_DESISTIR_DA_DRENAGEM || 'abortar' }}",
			`[[ "$ESPERA" =~ ^(0|[1-9][0-9]{0,5})$ ]]`,
			`case "$AO_DESISTIR" in abortar|avancar) ;;`,
			"DEPLOY_ESPERA_DRENAGEM_S=$ESPERA DEPLOY_AO_DESISTIR_DA_DRENAGEM=$AO_DESISTIR",
		} {
			if !strings.Contains(p, quer) {
				t.Errorf("deploy.yml, passo do %s: falta %q", nome, quer)
			}
		}
	}
}

// TestAOS450DeployEDrenagem executa o deploy.sh, o rollback.sh e o drenar-planos.sh REAIS contra
// stubs (docker, curl, logger), sem rede e sem docker — testdata/aos450_deploy_drenagem.sh.
func TestAOS450DeployEDrenagem(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("AOS-450: o cenário usa flock(1) e /proc — corre em Linux (o CI)")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("sem bash: %v", err)
	}
	raiz, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, filepath.Join("testdata", "aos450_deploy_drenagem.sh"), raiz)
	out, err := cmd.CombinedOutput()
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("o cenário falhou: %v", err)
	}
	if !strings.Contains(string(out), "\nfalhas: 0\n") {
		t.Fatal("o cenário não terminou com «falhas: 0»")
	}
}

// entre devolve o texto de `s` desde `de` até ao primeiro `ate` a seguir.
func entre(t *testing.T, s, de, ate string) string {
	t.Helper()
	i := strings.Index(s, de)
	if i < 0 {
		t.Fatalf("não encontrei %q", de)
	}
	j := strings.Index(s[i:], ate)
	if j < 0 {
		t.Fatalf("não encontrei %q depois de %q", ate, de)
	}
	return s[i : i+j]
}
