package modelgateway_test

// AOS-508 — OS PROVIDERS FALSOS DE WIRE ATRÁS DO PROXY REAL.
//
// A MESMA imagem do LiteLLM que corre em produção, fixada pelo digest, com um provider falso
// atrás que devolve cada caso do wirefake. Para cada caso regista-se O QUE O PROXY ENTREGA: o
// status, as chaves que acrescenta ou retira (no topo e em `message`), se reescreve `content`,
// a ficha da forma do corpo entregue ([port.ProbeResponseShape]) e o que o gateway lhe faz.
// É medição: o teste só falha se o ensaio não se conseguir montar ou se um caso não for medido.
//
// PRECISA DE DOCKER e da imagem já descarregada (não a descarrega). Só corre a pedido:
//
//	AOS_WIRE_LIVE=1 go test -run TestAOS508_ProxyReal -v -count=1 .
//	bash scripts/ci/wire-live.sh        # o mesmo, com o salto declarado quando não há Docker
//
// O falso que corre atrás do proxy é um servidor mínimo dentro da própria imagem do proxy (como
// no AOS-505), que serve OS MESMOS corpos em ficheiro do wirefake, escolhidos pelo primeiro
// segmento do caminho — a regra de [wirefake.Servidor].
//
// O QUE NÃO PROVA: nada sobre o provider real (não é contactado) nem sobre o que o proxy faz a um
// provider que não fale o wire OpenAI (a rota é `openai/…`, a de produção).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos508FalsoNoContentor espera pelos casos (copiados para o contentor) e serve-os pelo primeiro
// segmento do caminho.
const aos508FalsoNoContentor = `
import json, os, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
while not os.path.exists("/tmp/casos.pronto"):
    time.sleep(0.2)
CASOS = json.load(open("/tmp/casos.json", encoding="utf-8"))
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(n)
        caso = self.path.strip("/").split("/")[0]
        corpo = CASOS.get(caso)
        status = 200
        if corpo is None:
            status, corpo = 404, '{"error":{"message":"caso desconhecido"}}'
        b = corpo.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
`

// aos508ArranqueDoProxy espera pela configuração (copiada para o contentor) e substitui-se pelo proxy.
const aos508ArranqueDoProxy = `
import os, time
while not os.path.exists("/tmp/config.pronto"):
    time.sleep(0.2)
os.execvp("litellm", ["litellm", "--config", "/tmp/config.yaml", "--port", "4000"])
`

// aos508Medida é o que o proxy entregou para um caso.
type aos508Medida struct {
	Caso   string `json:"caso"`
	Status int    `json:"status"`
	// As chaves que o proxy acrescentou e retirou, no topo do corpo e em `choices[0].message`.
	TopoMais  []string `json:"topo_mais,omitempty"`
	TopoMenos []string `json:"topo_menos,omitempty"`
	MsgMais   []string `json:"msg_mais,omitempty"`
	MsgMenos  []string `json:"msg_menos,omitempty"`
	// A forma de `content` e o campo do raciocínio, antes (o que o falso mandou) e depois.
	ContentAntes     string `json:"content_antes"`
	ContentDepois    string `json:"content_depois"`
	RaciocinioAntes  string `json:"raciocinio_antes"`
	RaciocinioDepois string `json:"raciocinio_depois"`
	ChoicesAntes     int64  `json:"choices_antes"`
	ChoicesDepois    int64  `json:"choices_depois"`
	ToolCallsDepois  int64  `json:"tool_calls_depois"`
	IDDepois         string `json:"id_depois"`
	ArgsDepois       string `json:"args_depois,omitempty"`
	Recusa           string `json:"refusal_depois"`
	FunctionCall     bool   `json:"function_call_depois"`
	FinishAntes      string `json:"finish_antes"`
	FinishDepois     string `json:"finish_depois"`
	// Gateway é o que o gateway faz ao corpo entregue: `turno`, ou `recusada:<causa>`.
	Gateway string `json:"gateway"`
}

func aos508Chaves(obj map[string]json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for k := range obj {
		out[k] = true
	}
	return out
}

func aos508Diferenca(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// aos508Partes devolve as chaves do topo, as de `choices[0].message` e o `finish_reason` cru.
func aos508Partes(corpo []byte) (topo, msg map[string]bool, finish string) {
	var t map[string]json.RawMessage
	_ = json.Unmarshal(corpo, &t)
	var choices []map[string]json.RawMessage
	_ = json.Unmarshal(t["choices"], &choices)
	var m map[string]json.RawMessage
	finish = "ausente"
	if len(choices) > 0 {
		_ = json.Unmarshal(choices[0]["message"], &m)
		if raw, ok := choices[0]["finish_reason"]; ok {
			finish = string(raw)
		}
	}
	return aos508Chaves(t), aos508Chaves(m), finish
}

func TestAOS508_ProxyReal_OQueOProxyEntregaACadaCaso(t *testing.T) {
	if os.Getenv("AOS_WIRE_LIVE") != "1" {
		t.Skip("SALTADO: AOS_WIRE_LIVE != 1 — o ensaio com o proxy REAL (imagem de producao do LiteLLM, por digest) precisa de Docker; " +
			"correr com `bash scripts/ci/wire-live.sh`. POR VERIFICAR nesta execucao: o que o proxy faz a cada forma de resposta. " +
			"Os mesmos casos, sem proxy, correram (TestAOS508_OQueOGatewayFazACadaCaso).")
	}
	sufixo := fmt.Sprintf("%d", os.Getpid())
	e := &aos505Ensaio{t: t, rede: "aos508-rede-" + sufixo, falsoA: "aos508-falso-" + sufixo, proxy: "aos508-proxy-" + sufixo}
	if out, err := e.docker("image", "inspect", "--format", "{{.Id}}", aos505ImagemDoProxy); err != nil {
		t.Fatalf("AOS_WIRE_LIVE=1 foi pedido e a imagem do proxy nao esta disponivel (este teste nao a descarrega): %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = e.docker("rm", "-f", e.proxy, e.falsoA)
		_, _ = e.docker("network", "rm", e.rede)
	})
	e.dockerOuFalha("network", "create", e.rede)

	// Os casos e a configuração do proxy entram nos contentores por `docker cp`.
	nomes := wirefake.Nomes()
	casos := map[string]string{}
	var cfg strings.Builder
	cfg.WriteString("model_list:\n")
	for _, n := range nomes {
		casos[n] = string(wirefake.Corpo(n))
		cfg.WriteString("  - model_name: caso-" + n + "\n    litellm_params:\n      model: openai/k3\n      api_key: sk-ensaio-provider\n" +
			"      api_base: http://" + e.falsoA + ":8080/" + n + "/v1\n")
	}
	cfg.WriteString("litellm_settings:\n  drop_params: false\n  telemetry: false\n  num_retries: 0\ngeneral_settings:\n  background_health_checks: false\n")
	dir := t.TempDir()
	cruCasos, _ := json.Marshal(casos)
	for nome, conteudo := range map[string][]byte{"casos.json": cruCasos, "config.yaml": []byte(cfg.String()), "pronto": []byte("1")} {
		if err := os.WriteFile(filepath.Join(dir, nome), conteudo, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.dockerOuFalha("run", "-d", "--name", e.falsoA, "--network", e.rede, "--entrypoint", "python", aos505ImagemDoProxy, "-c", aos508FalsoNoContentor)
	e.dockerOuFalha("cp", filepath.Join(dir, "casos.json"), e.falsoA+":/tmp/casos.json")
	e.dockerOuFalha("cp", filepath.Join(dir, "pronto"), e.falsoA+":/tmp/casos.pronto")
	e.dockerOuFalha("run", "-d", "--name", e.proxy, "--network", e.rede, "-p", "127.0.0.1::4000",
		"-e", "LITELLM_MASTER_KEY="+aos505ChaveMestra, "--entrypoint", "python", aos505ImagemDoProxy, "-c", aos508ArranqueDoProxy)
	e.dockerOuFalha("cp", filepath.Join(dir, "config.yaml"), e.proxy+":/tmp/config.yaml")
	e.dockerOuFalha("cp", filepath.Join(dir, "pronto"), e.proxy+":/tmp/config.pronto")
	mapeada := strings.TrimSpace(strings.Split(e.dockerOuFalha("port", e.proxy, "4000/tcp"), "\n")[0])
	endereco := "http://127.0.0.1:" + mapeada[strings.LastIndex(mapeada, ":")+1:]
	vivo := false
	for limite := time.Now().Add(4 * time.Minute); time.Now().Before(limite) && !vivo; time.Sleep(time.Second) {
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(endereco + "/health/liveliness"); err == nil {
			_ = resp.Body.Close()
			vivo = resp.StatusCode == http.StatusOK
		}
	}
	if !vivo {
		logs, _ := e.docker("logs", "--tail", "40", e.proxy)
		t.Fatalf("o proxy nao ficou vivo em 4 min:\n%s", logs)
	}

	var medidas []aos508Medida
	cliente := &http.Client{Timeout: 60 * time.Second}
	for _, n := range nomes {
		req, _ := http.NewRequest(http.MethodPost, endereco+"/v1/chat/completions",
			strings.NewReader(`{"model":"caso-`+n+`","messages":[{"role":"user","content":"ola"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+aos505ChaveMestra)
		resp, err := cliente.Do(req)
		if err != nil {
			t.Fatalf("%s: pedido ao proxy: %v", n, err)
		}
		entregue, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		origem := wirefake.Corpo(n)
		antes, depois := port.ProbeResponseShape(origem), port.ProbeResponseShape(entregue)
		topoA, msgA, finA := aos508Partes(origem)
		m := aos508Medida{Caso: n, Status: resp.StatusCode, ContentAntes: antes.Content, RaciocinioAntes: antes.Reasoning, ChoicesAntes: antes.ChoicesN, FinishAntes: finA}
		if resp.StatusCode != http.StatusOK {
			m.Gateway = "o proxy nao entregou resposta (status " + fmt.Sprint(resp.StatusCode) + ")"
			medidas = append(medidas, m)
			continue
		}
		if !json.Valid(bytes.TrimSpace(entregue)) {
			t.Errorf("%s: o proxy entregou 200 com um corpo que nao e JSON", n)
		}
		topoD, msgD, finD := aos508Partes(entregue)
		m.TopoMais, m.TopoMenos = aos508Diferenca(topoD, topoA), aos508Diferenca(topoA, topoD)
		m.MsgMais, m.MsgMenos = aos508Diferenca(msgD, msgA), aos508Diferenca(msgA, msgD)
		m.ContentDepois, m.RaciocinioDepois, m.ChoicesDepois, m.FinishDepois = depois.Content, depois.Reasoning, depois.ChoicesN, finD
		m.ToolCallsDepois, m.IDDepois, m.ArgsDepois, m.Recusa, m.FunctionCall = depois.ToolCallsN, depois.ToolCallID, depois.ArgumentsForm, depois.Refusal, depois.LegacyFunctionCall
		m.Gateway = "turno"
		if r, err := port.UnmarshalChatResponse(entregue); err != nil {
			m.Gateway = "recusada:" + modelgateway.ResponseRejectionCause(err)
		} else if len(r.Choices) == 0 {
			m.Gateway = "recusada:" + modelgateway.RejectNoChoices
		}
		medidas = append(medidas, m)
	}
	if len(medidas) != len(nomes) {
		t.Errorf("mediram-se %d casos de %d", len(medidas), len(nomes))
	}
	raw, _ := json.Marshal(map[string]any{"imagem": aos505ImagemDoProxy, "casos": len(medidas), "medidas": medidas, "pass": !t.Failed()})
	if destino := os.Getenv("AOS_WIRE_LIVE_OUT"); destino != "" {
		if err := os.WriteFile(destino, raw, 0o644); err != nil {
			t.Errorf("gravar as medidas em %s: %v", destino, err)
		}
	}
	resumo, _ := json.Marshal(map[string]any{"imagem": aos505ImagemDoProxy, "casos": len(medidas), "pass": !t.Failed()})
	t.Logf("AOS_WIRE_LIVE_REPORT %s", resumo)
}
