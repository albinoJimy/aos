package main

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/pricing"
)

// AOS-527 — A TABELA DE PREÇOS DO OPERADOR, MONTADA NO NÓ DE PRODUÇÃO.
//
// O nó lia AOS_MODEL_PRICING_PATH desde AOS-259, mas o compose de produção não montava nenhum
// ficheiro para onde ela pudesse apontar, e o titular pago por token (AOS-525) corria com custo
// «não derivado». Estes testes lêem os ficheiros REAIS do servidor — o compose, o deploy.sh, o
// workflow do CD e a semente —, porque o defeito estava neles e não no código do nó.

const (
	aos527Compose  = "../../../deploy/server/docker-compose.prod.yml"
	aos527DeploySh = "../../../deploy/server/deploy.sh"
	aos527DeployCD = "../../../.github/workflows/deploy.yml"
	aos527Semente  = "../../../deploy/server/model-pricing/pricing.json"

	// aos527CaminhoNoNo é o valor que o operador põe em AOS_MODEL_PRICING_PATH (README do servidor).
	aos527CaminhoNoNo = "/etc/aos/model-pricing/pricing.json"
)

// reAOS527Montagem casa a linha REAL da montagem da tabela no compose: origem, destino e opções.
var reAOS527Montagem = regexp.MustCompile(`(?m)^\s+- (\./model-pricing[^:\s]*):([^:\s]+)(?::([a-z,]+))?\s*(?:#.*)?$`)

// TestAOS527ComposeMontaAPastaDePrecosSoNoNo: a montagem é de uma PASTA, só de leitura, no serviço
// `aos`; a variável continua vazia por omissão, que é o que deixa um servidor sem tabela arrancar
// como antes; e o caminho documentado cai dentro dela. Desde o AOS-544 o `aos-orq` mede os dólares
// do planeador com a MESMA tabela: recebe a mesma pasta, só de leitura, e a variável vazia por
// omissão.
func TestAOS527ComposeMontaAPastaDePrecosSoNoNo(t *testing.T) {
	compose := lerFonteDeTeste(t, aos527Compose)
	no := blocoDoServico(t, compose, "aos")
	orq := blocoDoServico(t, compose, "aos-orq")

	montagens := reAOS527Montagem.FindAllStringSubmatch(no, -1)
	if len(montagens) != 1 {
		t.Fatalf("o serviço `aos` tem %d montagens de ./model-pricing, quero exactamente uma", len(montagens))
	}
	origem, destino, opcoes := montagens[0][1], montagens[0][2], montagens[0][3]
	// AOS-544: a MESMA tabela no `aos-orq` — a mesma pasta, só de leitura, e a variável vazia por omissão.
	if m := reAOS527Montagem.FindAllStringSubmatch(orq, -1); len(m) != 1 || m[0][1] != "./model-pricing" || m[0][2] != "/etc/aos/model-pricing" || m[0][3] != "ro" {
		t.Errorf("o `aos-orq` tem de montar a mesma pasta da tabela, so de leitura: %v", m)
	}
	if !strings.Contains(orq, `AOS_MODEL_PRICING_PATH: "${AOS_MODEL_PRICING_PATH:-}"`) {
		t.Errorf("o `aos-orq` tem de receber AOS_MODEL_PRICING_PATH vazia por omissao")
	}
	if !strings.Contains(no, `AOS_MODEL_PRICING_PATH: "${AOS_MODEL_PRICING_PATH:-}"`) {
		t.Errorf("AOS_MODEL_PRICING_PATH tem de passar vazia por omissão — um valor fixo obrigaria todo o servidor a ter a tabela")
	}
	// A origem é a PASTA: um bind de FICHEIRO em falta viraria um directório no host.
	if origem != "./model-pricing" {
		t.Errorf("a origem da montagem é %q; tem de ser a pasta ./model-pricing", origem)
	}
	if path.Dir(aos527CaminhoNoNo) != destino {
		t.Errorf("o caminho documentado %q não fica dentro da pasta montada %q", aos527CaminhoNoNo, destino)
	}
	if !slices.Contains(strings.Split(opcoes, ","), "ro") {
		t.Errorf("a montagem %s:%s não é só de leitura (opções %q)", origem, destino, opcoes)
	}
}

// TestAOS527UmServidorSemOFicheiroArrancaComoAntes: as três peças que fazem a montagem inócua num
// servidor que nunca recebeu a tabela, e o nó sem a variável a fazer exactamente o que fazia.
func TestAOS527UmServidorSemOFicheiroArrancaComoAntes(t *testing.T) {
	// 1. A pasta existe sempre antes do `compose up`: o deploy.sh cria-a ANTES do primeiro `dc up`.
	deploy := lerFonteDeTeste(t, aos527DeploySh)
	mkdir := regexp.MustCompile(`(?m)^mkdir -p "\$\{APP_DIR\}/model-pricing"$`).FindStringIndex(deploy)
	up := regexp.MustCompile(`(?m)^\s*(?:if !\s+)?dc up\b`).FindStringIndex(deploy)
	if mkdir == nil || up == nil {
		t.Fatalf("deploy.sh: mkdir da pasta em %v, primeiro `dc up` em %v — algum desapareceu", mkdir, up)
	}
	if mkdir[0] > up[0] {
		t.Errorf("deploy.sh: o mkdir de ${APP_DIR}/model-pricing (byte %d) vem depois do primeiro `dc up` (byte %d)", mkdir[0], up[0])
	}
	// 2. O CD semeia a PASTA (o rsync cria a última componente, que no 1.º deploy falta) e nunca
	//    reescreve o que o operador lá tem.
	cd := lerFonteDeTeste(t, aos527DeployCD)
	semente := strings.Index(cd, `rsync -az --ignore-existing -e "$SSH" deploy/server/model-pricing/ "$USER@$HOST:/opt/aos/model-pricing/"`)
	if semente < 0 {
		t.Fatalf("o CD não semeia deploy/server/model-pricing/ com --ignore-existing")
	}
	// A semente tem de chegar ANTES de o deploy.sh correr, senão o 1.º deploy arranca sem ela.
	if corre := strings.Index(cd, "bash /opt/aos/deploy.sh '$IMAGE'"); corre < 0 || semente > corre {
		t.Errorf("deploy.yml: a semente (byte %d) tem de vir antes do passo que corre o deploy.sh (byte %d)", semente, corre)
	}
	if regexp.MustCompile(`for d in [^;]*\bmodel-pricing\b`).MatchString(cd) {
		t.Errorf("model-pricing está no ciclo que sincroniza SEM --ignore-existing: um deploy reescreveria o preço do operador")
	}
	// 3. Sem a variável, a postura é a de antes: tabela embebida, que não cobre o par de produção.
	t.Setenv("AOS_MODEL_PRICING_PATH", "")
	rec, p, err := parseModelPricingFromEnv("gpt-4o", "eu")
	if err != nil {
		t.Fatalf("sem AOS_MODEL_PRICING_PATH o arranque não pode falhar: %v", err)
	}
	if rec != nil || p.Armed || p.External {
		t.Fatalf("sem AOS_MODEL_PRICING_PATH a postura mudou: recorder=%v armado=%v externa=%v", rec != nil, p.Armed, p.External)
	}
}

// TestAOS527ASementeCobreOParDeProducao: a semente carrega com o leitor REAL, cobre (gpt-4o, eu)
// ao preço da OpenRouter para o Claude Haiku, e um turno custa o que a conta dá.
func TestAOS527ASementeCobreOParDeProducao(t *testing.T) {
	b, err := os.ReadFile(aos527Semente)
	if err != nil {
		t.Fatalf("ler a semente: %v", err)
	}
	tabela, err := pricing.LoadTable(b)
	if err != nil {
		t.Fatalf("a semente não carrega com o leitor do nó: %v", err)
	}
	if !strings.HasPrefix(tabela.Version(), "2026.10#") {
		t.Errorf("versão da semente = %q, quero 2026.10#…", tabela.Version())
	}
	taxa, ok := tabela.RateFor("gpt-4o", "eu")
	if !ok {
		t.Fatal("a semente não tem preço para (gpt-4o, eu), o par de produção")
	}
	if taxa.InputPerMTokMicroUSD != 100000 || taxa.OutputPerMTokMicroUSD != 500000 {
		t.Errorf("preço da semente = %d/%d micro-USD por Mtok, quero 100000/500000", taxa.InputPerMTokMicroUSD, taxa.OutputPerMTokMicroUSD)
	}

	// 1000 × 100000 / 1e6 = 100; 500 × 500000 / 1e6 = 250.
	custo, err := cost.NewCalculator(tabela).Cost(cost.TokenCounts{PromptTokens: 1000, CompletionTokens: 500}, "gpt-4o", "eu")
	if err != nil {
		t.Fatalf("custo do turno: %v", err)
	}
	if custo.CostMicroUSD != 350 {
		t.Errorf("1000 tokens de entrada e 500 de saída custam %d micro-USD, quero 350", custo.CostMicroUSD)
	}

	// E com a variável apontada a ela, o nó arma o canal de custo com a tabela externa.
	t.Setenv("AOS_MODEL_PRICING_PATH", aos527Semente)
	rec, p, err := parseModelPricingFromEnv("gpt-4o", "eu")
	if err != nil {
		t.Fatalf("a semente como tabela do operador: %v", err)
	}
	if rec == nil || !p.Armed || !p.External || p.TableVersion != tabela.Version() {
		t.Fatalf("a semente não armou o canal: recorder=%v armado=%v externa=%v versão=%q", rec != nil, p.Armed, p.External, p.TableVersion)
	}
}

// TestAOS527ODeployRecusaUmaTabelaQueONoNaoCarregaria executa a PRÉ-VERIFICAÇÃO REAL do passo 0b
// do deploy.sh (o bloco, extraído do ficheiro) contra uma pasta de teste. Reverter não salva um
// pricing.json mau — a imagem anterior lê o mesmo ficheiro e aborta igual —, por isso o deploy tem
// de recusar antes de tocar em produção. Linux: as permissões de «outros» não existem no NTFS.
func TestAOS527ODeployRecusaUmaTabelaQueONoNaoCarregaria(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("AOS-527: as permissões POSIX do cenário só valem em Linux (o CI)")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("sem bash: %v", err)
	}
	deploy := lerFonteDeTeste(t, aos527DeploySh)
	i := strings.Index(deploy, "# A TABELA APONTADA TEM DE CARREGAR")
	j := strings.Index(deploy[max(i, 0):], "\nesac\n")
	if i < 0 || j < 0 {
		t.Fatal("deploy.sh: não encontrei a pré-verificação da tabela de preços")
	}
	bloco := deploy[i : i+j+len("\nesac\n")]

	app := t.TempDir()
	pasta := filepath.Join(app, "model-pricing")
	if err := os.MkdirAll(pasta, 0o755); err != nil {
		t.Fatal(err)
	}
	semente, err := os.ReadFile(aos527Semente)
	if err != nil {
		t.Fatal(err)
	}
	escrever := func(nome string, conteudo []byte, modo os.FileMode) {
		t.Helper()
		f := filepath.Join(pasta, nome)
		if err := os.WriteFile(f, conteudo, modo); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f, modo); err != nil { // a umask não pode decidir o caso
			t.Fatal(err)
		}
	}
	escrever("pricing.json", semente, 0o644)
	escrever("fechado.json", semente, 0o600)
	escrever("vazio.json", nil, 0o644)
	escrever("mau.json", []byte("{"), 0o644)

	_, temPython := exec.LookPath("python3")
	casos := []struct {
		nome, valor string
		passa       bool
		so          bool // só com python3 no PATH
	}{
		{"variável vazia: nada muda", "", true, false},
		{"fora da pasta montada: nada muda", "/etc/aos/outra.json", true, false},
		{"a semente", "/etc/aos/model-pricing/pricing.json", true, false},
		{"ficheiro em falta", "/etc/aos/model-pricing/nao-existe.json", false, false},
		{"ficheiro vazio", "/etc/aos/model-pricing/vazio.json", false, false},
		{"0600, ilegível pelo uid 65532", "/etc/aos/model-pricing/fechado.json", false, false},
		{"com ..", "/etc/aos/model-pricing/../model-pricing/pricing.json", false, false},
		{"JSON inválido", "/etc/aos/model-pricing/mau.json", false, true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if c.so && temPython != nil {
				t.Skip("sem python3 no PATH: o deploy.sh não confere o JSON")
			}
			guiao := "set -euo pipefail\nlog() { :; }\nfail() { printf 'FAIL %s\\n' \"$*\"; exit 1; }\n" + bloco + "echo PASSA\n"
			cmd := exec.Command(bash, "-c", guiao)
			cmd.Env = append(os.Environ(), "APP_DIR="+app, "AOS_MODEL_PRICING_PATH="+c.valor)
			out, err := cmd.CombinedOutput()
			passou := err == nil && strings.Contains(string(out), "PASSA")
			if passou != c.passa {
				t.Fatalf("AOS_MODEL_PRICING_PATH=%q: passou=%v, quero %v\n%s", c.valor, passou, c.passa, out)
			}
		})
	}
}
