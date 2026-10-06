package modelgateway_test

// AOS-505 — «UMA TROCA DE MODELO POR BAIXO É DETECTADA», COM O PROXY REAL.
//
// O gateway de produção (NewProduction) à frente da MESMA imagem do LiteLLM que corre em
// produção, fixada pelo digest, com dois providers falsos atrás. A meio de um run troca-se a
// configuração do proxy por baixo do nome pedido — primeiro o modelo, depois o endpoint — e o
// turno seguinte tem de registar a variância em observação e falhar com causa em imposição.
//
// PRECISA DE DOCKER e da imagem já descarregada (não a descarrega). Por isso só corre a pedido:
//
//	AOS_ROTA_LIVE=1 go test -run TestAOS505_ProxyReal -v -count=1 .
//	bash scripts/ci/rota-live.sh        # o mesmo, com o salto declarado quando não há Docker
//
// Sem AOS_ROTA_LIVE=1 o teste SALTA e di-lo; o equivalente offline, com httptest a emitir os
// mesmos cabeçalhos, corre sempre (route_aos505_test.go). Não monta ficheiros do host: o provider
// falso e a configuração do proxy entram nos contentores por argumento e por variável de
// ambiente, e os providers falsos correm dentro da própria imagem do proxy.
//
// O QUE NÃO PROVA: nada sobre o provider real (não é contactado), e nada sobre uma troca feita
// pelo provider por trás do mesmo nome e endpoint — essa não muda nenhum cabeçalho do proxy.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// aos505ImagemDoProxy é a imagem do LiteLLM de produção, pelo digest (litellm 1.96.2).
const aos505ImagemDoProxy = "ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63"

// aos505ChaveMestra é a chave do proxy de ENSAIO — um valor de teste, sem uso fora deste ficheiro.
const aos505ChaveMestra = "sk-ensaio-rota-505"

// aos505ProviderFalso é um provider OpenAI-compatível mínimo. Devolve no `model` do corpo o seu
// próprio nome (o que um provider real faz), que o proxy depois carimba com o nome pedido.
const aos505ProviderFalso = `
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
NOME = os.environ["FAKE_NAME"]
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(n)
        b = json.dumps({"id": "chatcmpl-" + NOME, "object": "chat.completion", "created": 1700000000,
            "model": "modelo-real-" + NOME,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "ola de " + NOME}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
`

// aos505ArranqueDoProxy escreve a configuração (da variável CFG) e substitui-se pelo proxy.
const aos505ArranqueDoProxy = `
import os
open("/tmp/config.yaml", "w").write(os.environ["CFG"])
os.execvp("litellm", ["litellm", "--config", "/tmp/config.yaml", "--port", "4000"])
`

type aos505Ensaio struct {
	t                    *testing.T
	rede, falsoA, falsoB string
	proxy                string
	endereco             string // http://127.0.0.1:<porta>
	porta                string // a porta do host, fixada no primeiro arranque
	arranques            int
}

func (e *aos505Ensaio) docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (e *aos505Ensaio) dockerOuFalha(args ...string) string {
	e.t.Helper()
	out, err := e.docker(args...)
	if err != nil {
		e.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// config é a configuração do proxy: o nome pedido `gpt-4o` servido pelo modelo e pelo provider
// dados. `drop_params: false`, como o runbook pede para produção.
func (e *aos505Ensaio) config(modelo, falso string) string {
	return "model_list:\n" +
		"  - model_name: gpt-4o\n" +
		"    litellm_params:\n" +
		"      model: " + modelo + "\n" +
		"      api_key: sk-ensaio-provider\n" +
		"      api_base: http://" + falso + ":8080/v1\n" +
		"litellm_settings:\n  drop_params: false\n  telemetry: false\n" +
		"general_settings:\n  background_health_checks: false\n"
}

// subirProxy (re)arranca o proxy com a configuração dada — é assim que se «troca por baixo».
func (e *aos505Ensaio) subirProxy(cfg string) {
	e.t.Helper()
	_, _ = e.docker("rm", "-f", e.proxy)
	// A porta do host é sorteada no primeiro arranque e REUTILIZADA nos seguintes: a troca é por
	// baixo do mesmo endereço, que é o que o gateway conhece.
	e.dockerOuFalha("run", "-d", "--name", e.proxy, "--network", e.rede, "-p", "127.0.0.1:"+e.porta+":4000",
		"-e", "LITELLM_MASTER_KEY="+aos505ChaveMestra, "-e", "CFG="+cfg,
		"--entrypoint", "python", aos505ImagemDoProxy, "-c", aos505ArranqueDoProxy)
	e.arranques++
	if e.porta == "" {
		mapeada := strings.TrimSpace(strings.Split(e.dockerOuFalha("port", e.proxy, "4000/tcp"), "\n")[0])
		e.porta = mapeada[strings.LastIndex(mapeada, ":")+1:]
	}
	e.endereco = "http://127.0.0.1:" + e.porta
	limite := time.Now().Add(4 * time.Minute)
	for time.Now().Before(limite) {
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(e.endereco + "/health/liveliness")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(time.Second)
	}
	logs, _ := e.docker("logs", "--tail", "40", e.proxy)
	e.t.Fatalf("o proxy nao ficou vivo em 4 min:\n%s", logs)
}

// cabecalhos faz um pedido cru ao proxy e devolve os cabeçalhos da resposta — o que o proxy
// real emite, lido fora do gateway, para o relatório e para a verificação de fuga.
func (e *aos505Ensaio) cabecalhos() http.Header {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.endereco+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"ola"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aos505ChaveMestra)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		e.t.Fatalf("pedido cru ao proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("pedido cru ao proxy: status %d", resp.StatusCode)
	}
	return resp.Header
}

// aos505Corrida é um gateway de produção num modo, com o seu audit e o que observou.
type aos505Corrida struct {
	modo    string
	cliente *modelgateway.ModelClientAdapter
	store   *audit.MemStore
	visto   *aos505Observado
}

func (e *aos505Ensaio) corrida(modo string) *aos505Corrida {
	e.t.Helper()
	c := &aos505Corrida{modo: modo, store: audit.NewMemStore(), visto: &aos505Observado{}}
	cfg := prodConfig(c.store, e.endereco+"/v1", &http.Client{Timeout: 60 * time.Second},
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.Credentials = testCreds{"openai|eu": aos505ChaveMestra}
	cfg.Variance = c.visto
	cfg.Route = modelgateway.RouteGovernance{Mode: modo, ExpectedAPIHost: e.falsoA + ":8080", Observer: c.visto.observar}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		e.t.Fatalf("NewProduction(%s): %v", modo, err)
	}
	c.cliente = modelgateway.NewModelClient(gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"))
	return c
}

func (c *aos505Corrida) turno(n int) (agentruntime.ModelResponse, error) {
	ctx := agentruntime.ContextWithModelCall(context.Background(), "run-rota-live-"+c.modo, fmt.Sprintf("step-%06d", n))
	return c.cliente.Call(ctx, agentruntime.PromptView{Materialized: []byte(fmt.Sprintf("turno %d", n))})
}

func TestAOS505_ProxyReal_TrocaPorBaixoDetectada(t *testing.T) {
	if os.Getenv("AOS_ROTA_LIVE") != "1" {
		t.Skip("SALTADO: AOS_ROTA_LIVE != 1 — o ensaio com o proxy REAL (imagem de producao do LiteLLM, por digest) precisa de Docker; " +
			"correr com `bash scripts/ci/rota-live.sh`. POR VERIFICAR nesta execucao: que os cabecalhos que o proxy real emite mudam com a troca de configuracao. " +
			"O equivalente offline (TestAOS505_TrocaPorBaixoAMeioDoRun) correu.")
	}
	sufixo := fmt.Sprintf("%d", os.Getpid())
	e := &aos505Ensaio{t: t, rede: "aos505-rede-" + sufixo, falsoA: "aos505-falso-a-" + sufixo, falsoB: "aos505-falso-b-" + sufixo, proxy: "aos505-proxy-" + sufixo}
	if out, err := e.docker("image", "inspect", "--format", "{{.Id}}", aos505ImagemDoProxy); err != nil {
		t.Fatalf("AOS_ROTA_LIVE=1 foi pedido e a imagem do proxy nao esta disponivel (este teste nao a descarrega): %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = e.docker("rm", "-f", e.proxy, e.falsoA, e.falsoB)
		_, _ = e.docker("network", "rm", e.rede)
	})
	e.dockerOuFalha("network", "create", e.rede)
	for nome, falso := range map[string]string{"A": e.falsoA, "B": e.falsoB} {
		e.dockerOuFalha("run", "-d", "--name", falso, "--network", e.rede, "-e", "FAKE_NAME="+nome,
			"--entrypoint", "python", aos505ImagemDoProxy, "-c", aos505ProviderFalso)
	}

	// CONFIGURAÇÃO DE PARTIDA: `gpt-4o` → `openai/k3` no provider A — a rota do perfil.
	e.subirProxy(e.config("openai/k3", e.falsoA))
	base := e.cabecalhos()
	idDoDeployment := base.Get("x-litellm-model-id")
	if base.Get("x-litellm-model-name") != "openai/k3" || !strings.Contains(base.Get("x-litellm-model-api-base"), e.falsoA) || idDoDeployment == "" {
		t.Fatalf("pre-condicao: o proxy real nao emite os cabecalhos medidos: nome=%q api_base=%q id=%q",
			base.Get("x-litellm-model-name"), base.Get("x-litellm-model-api-base"), idDoDeployment)
	}
	observe, enforce := e.corrida(modelgateway.RouteGovernanceObserve), e.corrida(modelgateway.RouteGovernanceEnforce)
	for _, c := range []*aos505Corrida{observe, enforce} {
		out, err := c.turno(1)
		if err != nil || out.RouteCheck != agentruntime.RouteEqual || out.Model != "openai/k3" {
			t.Fatalf("%s, turno 1 (rota do perfil): quero igual e openai/k3; veio %q %q err=%v", c.modo, out.RouteCheck, out.Model, err)
		}
	}

	type passo struct {
		nome, modelo, falso, causa, servido string
	}
	relatorio := []map[string]string{{"passo": "partida", "x-litellm-model-name": base.Get("x-litellm-model-name"), "resultado": "igual"}}
	turno := 1
	for _, p := range []passo{
		{"(i) outro modelo, mesmo endpoint", "openai/outro-nome", e.falsoA, modelgateway.RouteCauseModelDifferent, "openai/outro-nome"},
		{"(ii) mesmo modelo, outro endpoint", "openai/k3", e.falsoB, modelgateway.RouteCauseEndpointDifferent, "openai/k3"},
	} {
		e.subirProxy(e.config(p.modelo, p.falso)) // A TROCA POR BAIXO, a meio dos dois runs
		h := e.cabecalhos()
		if h.Get("x-litellm-model-id") == idDoDeployment {
			t.Errorf("%s: o identificador do deployment nao mudou com a troca — a medicao de 2026-10-06 dizia que mudava", p.nome)
		}
		turno++
		out, err := observe.turno(turno)
		if err != nil || out.RouteCheck != agentruntime.RouteDifferent || out.Model != p.servido {
			t.Fatalf("%s, observacao: quero diferente e %q, com o turno a seguir; veio %q %q err=%v", p.nome, p.servido, out.RouteCheck, out.Model, err)
		}
		_, err = enforce.turno(turno)
		var rerr *modelgateway.RouteVarianceError
		if !errors.As(err, &rerr) || rerr.Cause != p.causa {
			t.Fatalf("%s, imposicao: o turno tinha de falhar com %s; veio %v", p.nome, p.causa, err)
		}
		relatorio = append(relatorio, map[string]string{"passo": p.nome, "x-litellm-model-name": h.Get("x-litellm-model-name"),
			"resultado": "diferente", "causa": p.causa, "observe": "turno segue", "enforce": "turno falha"})
	}

	// O REGISTO: um selo por variância, do turno certo; os contadores; e NADA do que não pode
	// sair — o identificador do deployment (o de cada configuração) e o endereço dos providers.
	for _, c := range []*aos505Corrida{observe, enforce} {
		selos := aos505Selos(t, c.store)
		if len(selos) != 2 {
			t.Fatalf("%s: queria 2 selos de variancia, vieram %d", c.modo, len(selos))
		}
		for i, causa := range []string{modelgateway.RouteCauseModelDifferent, modelgateway.RouteCauseEndpointDifferent} {
			if selos[i].Obligations[0].Params["reason"] != causa || selos[i].StepID != fmt.Sprintf("step-%06d", i+2) {
				t.Errorf("%s, selo %d: causa %q passo %q", c.modo, i+1, selos[i].Obligations[0].Params["reason"], selos[i].StepID)
			}
		}
		var checks []string
		for _, o := range c.visto.obs {
			checks = append(checks, o.Check+"/"+o.Served)
		}
		if got := strings.Join(checks, " "); got != "igual/openai/k3 diferente/outro diferente/openai/k3" {
			t.Errorf("%s: contador = %s", c.modo, got)
		}
		tudo := aos505JSON(t, selos) + aos505JSON(t, c.visto.obs) + aos505JSON(t, c.visto.variancias)
		for _, proibido := range []string{idDoDeployment, e.falsoA, e.falsoB, ":8080", aos505ChaveMestra, "sk-ensaio-provider"} {
			if strings.Contains(tudo, proibido) {
				t.Errorf("%s: o registo leva %q — o identificador do deployment, o endpoint e as chaves nao podem sair do gateway", c.modo, proibido)
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"imagem": aos505ImagemDoProxy, "arranques_do_proxy": e.arranques, "passos": relatorio, "pass": !t.Failed()})
	t.Logf("AOS_ROTA_LIVE_REPORT %s", raw)
}
