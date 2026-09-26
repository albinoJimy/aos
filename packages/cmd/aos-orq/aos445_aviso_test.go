package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// AOS-445 — ninguém era avisado do resultado de um plano. O desenho decidido pelo dono: o `consume`
// imprime uma linha estável `aviso:` DEPOIS de o nó ter registado um desfecho TERMINAL; o
// drenar-planos.sh recolhe-a para um outbox; o avisar-planos.sh (cron do `aos`) envia-a por ntfy ao
// operador, com o id pseudonimizado. Estes testes fixam a ordem, a forma da linha nos três lados, e
// — em Linux — correm os dois scripts contra stubs.

// reportadorFalso regista a ordem das coisas: se já havia output quando o reporte foi pedido, a
// linha saiu ANTES do reporte, que é exactamente o defeito que o desenho proíbe.
type reportadorFalso struct {
	out          *bytes.Buffer
	falha        error
	chamadas     int
	outNoReporte string
}

func (r *reportadorFalso) ReportarDesfecho(_ context.Context, _ string, _ int, _ string, _ int, _ string) error {
	r.chamadas++
	r.outNoReporte = r.out.String()
	return r.falha
}

func TestAOS445AvisoSoDepoisDoReporte(t *testing.T) {
	casos := []struct {
		nome   string
		classe string
		codigo int
		falha  error
		quer   string
	}{
		{"terminal/0 reportado avisa", "terminal", exitOK, nil, "aviso: run=plan-445 geracao=2 classe=terminal codigo=0\n"},
		{"terminal/7 reportado avisa com o código", "terminal", exitDecisaoRecusada, nil, "aviso: run=plan-445 geracao=2 classe=terminal codigo=7\n"},
		{"terminal com o reporte FALHADO não avisa", "terminal", exitOK, errors.New("503"), ""},
		{"transitório não avisa — o plano não acabou", "transitorio", exitNosEmVoo, nil, ""},
		{"à espera de humano não avisa", "aguarda_humano", exitPendenteDeAprovacao, nil, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var out bytes.Buffer
			rep := &reportadorFalso{out: &out, falha: c.falha}
			err := reportarEAvisar(context.Background(), rep, &out, "plan-445", 2, c.classe, c.codigo, "resumo: …")
			if !errors.Is(err, c.falha) {
				t.Fatalf("erro %v, quer %v", err, c.falha)
			}
			if rep.chamadas != 1 {
				t.Fatalf("o desfecho tinha de ser reportado uma vez; foi %d", rep.chamadas)
			}
			if rep.outNoReporte != "" {
				t.Fatalf("a linha saiu ANTES do reporte: %q", rep.outNoReporte)
			}
			if out.String() != c.quer {
				t.Fatalf("output %q, quer %q", out.String(), c.quer)
			}
		})
	}
}

// avisoREDoScript extrai a regex AVISO_RE de um script do servidor.
var avisoREDoScript = regexp.MustCompile(`(?m)^AVISO_RE='([^']*)'$`)

// TestAOS445ContratoDaLinhaDoAvisoComOsScripts fixa a forma da linha nos TRÊS lados: o Go que a
// imprime, o drenar-planos.sh que a recolhe e o avisar-planos.sh que a lê. Se um mudar sozinho, a
// drenagem deixa de recolher (e os planos terminam sem aviso) ou o aviso descarta a linha — em
// silêncio, porque nenhum dos dois falha a drenagem. O molde do TestAOS443ContratoComOsScriptsDoServidor.
func TestAOS445ContratoDaLinhaDoAvisoComOsScripts(t *testing.T) {
	drenar := lerDoRepo(t, "deploy", "server", "drenar-planos.sh")
	avisar := lerDoRepo(t, "deploy", "server", "avisar-planos.sh")
	var res []string
	for nome, s := range map[string]string{"drenar-planos.sh": drenar, "avisar-planos.sh": avisar} {
		m := avisoREDoScript.FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("%s não declara AVISO_RE='…'", nome)
		}
		res = append(res, m[1])
	}
	if res[0] != res[1] {
		t.Fatalf("os dois scripts lêem a linha com regex diferentes:\n  %s\n  %s", res[0], res[1])
	}
	re, err := regexp.Compile(res[0])
	if err != nil {
		t.Fatalf("AVISO_RE não compila: %v", err)
	}
	for _, run := range []string{"plan-e2e-447-1790400000", "cliente/pedido~n1", "run_Á-é"} {
		for _, codigo := range []int{exitOK, exitDecisaoRecusada, exitPlanoRecusado, exitDocumentoRecusado} {
			if l := linhaDoAviso(run, 3, "terminal", codigo); !re.MatchString(l) {
				t.Errorf("a regex dos scripts não aceita a linha que o consume imprime: %q", l)
			}
		}
	}
	for _, l := range []string{
		linhaDoAviso("plan-x", 1, "transitorio", exitErro),
		linhaDoAviso("plan-x", 1, "aguarda_humano", exitPendenteDeAprovacao),
		"desfecho: run=plan-x codigo=0 classe=terminal",
		"aviso: run=plan x geracao=1 classe=terminal codigo=0",
	} {
		if re.MatchString(l) {
			t.Errorf("a regex dos scripts aceita uma linha que não é aviso: %q", l)
		}
	}
	if !strings.HasPrefix(linhaDoAviso("r", 1, "terminal", 0), prefixoDoAviso) {
		t.Error("linhaDoAviso não começa pelo prefixo declarado")
	}

	// A conferência da drenagem soma os terminais das métricas pelo prefixo da série; tem de ser o
	// que o [serie] escreve, com a classe como PRIMEIRO rótulo.
	prefixoTerminais := `aos_orq_consume_desfechos_total{classe="terminal",`
	if s := serie(metricaDesfechos, "classe", "terminal", "codigo", "0"); !strings.HasPrefix(s, prefixoTerminais) {
		t.Errorf("a série dos terminais (%s) não começa por %s, que o drenar-planos.sh soma", s, prefixoTerminais)
	}
	for _, quer := range []string{
		`index($1, "` + strings.ReplaceAll(prefixoTerminais, `"`, `\"`) + `")`,
		`$1 == "` + metricaNaoReportados + `"`,
		`AVISOS_DIR="${DRENAR_AVISOS_DIR:-${AOS_DIR}/.avisos-planos}"`,
		`exec 8>>"${AVISOS_DIR}/lock"`,
	} {
		if !strings.Contains(drenar, quer) {
			t.Errorf("drenar-planos.sh já não tem %q", quer)
		}
	}
	for _, quer := range []string{
		`AVISOS_DIR="${AOS_AVISO_PLANOS_DIR:-${AOS_DIR}/.avisos-planos}"`,
		`exec 8>>"${AVISOS_DIR}/lock"`,
		`${AOS_DIR}/secrets/ntfy-topico-planos`,
		`${AOS_DIR}/secrets/aviso-planos-hmac.key`,
		`h="$(hmac_sha256 "${CHAVE}" "${run}")"; p="${h:0:12}"`,
		`DESENCONTRO_RE='^desencontro: terminais=([0-9]{1,9}) avisos=([0-9]{1,9}) em=([0-9]{1,12})$'`,
	} {
		if !strings.Contains(avisar, quer) {
			t.Errorf("avisar-planos.sh já não tem %q", quer)
		}
	}
	if !strings.Contains(drenar, `printf 'desencontro: terminais=%d avisos=%d em=%d\n'`) {
		t.Error("drenar-planos.sh já não escreve o desencontro na forma que o avisar-planos.sh lê")
	}
	// A regex do bash lê-se em C nos DOIS scripts: o `[^[:space:]]` depende do locale (U+3000 num
	// run_id não casaria em C.UTF-8). A semântica prova-a o cenário shell; aqui fixa-se o export.
	for nome, s := range map[string]string{"drenar-planos.sh": drenar, "avisar-planos.sh": avisar} {
		if !strings.Contains(s, "\nexport LC_ALL=C\n") {
			t.Errorf("%s já não fixa LC_ALL=C", nome)
		}
	}

	// O ciclo do consume reporta PELO reportarEAvisar — uma chamada directa ao cliente saltaria o
	// aviso sem que nenhum teste de unidade o visse.
	consumir := lerDoRepo(t, "packages", "cmd", "aos-orq", "consumir.go")
	if strings.Contains(consumir, "cli.ReportarDesfecho(") || !strings.Contains(consumir, "reportarEAvisar(ctx, cli, os.Stdout,") {
		t.Error("o consume deixou de reportar pelo reportarEAvisar")
	}

	// O script chega ao servidor.
	yml := lerDoRepo(t, ".github", "workflows", "deploy.yml")
	if !strings.Contains(yml, "deploy/server/avisar-planos.sh \\\n") {
		t.Error("o avisar-planos.sh não está no rsync do deploy.yml — nunca chega ao servidor")
	}
}

// TestAOS445OutboxEAvisos executa o drenar-planos.sh e o avisar-planos.sh REAIS contra stubs
// (docker, curl, logger) — testdata/aos445_avisos_planos.sh.
func TestAOS445OutboxEAvisos(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("AOS-445: o cenário usa flock(1) — corre em Linux (o CI)")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("sem bash: %v", err)
	}
	raiz, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bash, filepath.Join("testdata", "aos445_avisos_planos.sh"), raiz).CombinedOutput()
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("o cenário falhou: %v", err)
	}
	if !strings.Contains(string(out), "\nfalhas: 0\n") {
		t.Fatal("o cenário não terminou com «falhas: 0»")
	}
}
