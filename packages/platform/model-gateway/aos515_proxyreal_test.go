package modelgateway_test

// AOS-515 (e AOS-513) — O QUE O GATEWAY ENVIA, ATRÁS DO PROXY REAL.
//
// A MESMA imagem do LiteLLM de produção, fixada pelo digest, com um provider falso atrás que
// GRAVA o corpo de cada pedido que recebe. O gateway de produção — com um perfil de rota que
// declara parâmetros e `devolver: obrigatorio`, a captura e a governação da rota ligadas — faz
// dois turnos por cada rota do proxy, e o teste regista o que CHEGOU ao provider no segundo:
//
//   - rota `openai/…` (a classe de produção): o falso fala o wire OpenAI;
//   - rota `anthropic/…` (a da segunda família, AOS-516): o falso fala o wire de mensagens da
//     Anthropic, e o proxy traduz nos dois sentidos.
//
// É MEDIÇÃO: o teste falha se o ensaio não se montar ou se um turno não se completar, e regista
// — sem falhar — o que o proxy fez a cada campo. Só corre a pedido, com Docker e a imagem já
// descarregada:
//
//	AOS_WIRE_LIVE=1 go test -run TestAOS515_ProxyReal -v -count=1 .
//	bash scripts/ci/wire-live.sh
//
// O QUE NÃO PROVA: nada sobre o provider real. O falso não valida assinaturas.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// aos515FalsoNoContentor grava os corpos recebidos e devolve-os em GET /pedidos. Ao primeiro
// pedido de cada rota (sem mensagem `assistant`) responde com raciocínio assinado e uma tool
// call; aos seguintes, com o texto final. Fala o wire OpenAI e, em `/v1/messages`, o da Anthropic.
const aos515FalsoNoContentor = `
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
PEDIDOS = []
BLOCOS = [{"type":"thinking","thinking":"S-PENSA-0 <b> & e","signature":"U0lH-0+/=="},{"type":"redacted_thinking","data":"UkVE-0"},{"type":"thinking","thinking":"","signature":"VkFaSU8-0"}]
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def responder(self, obj):
        b = json.dumps(obj).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def do_GET(self):
        self.responder(PEDIDOS)
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        cru = self.rfile.read(n).decode("utf-8")
        PEDIDOS.append({"caminho": self.path, "corpo": cru})
        try:
            pedido = json.loads(cru)
        except Exception:
            pedido = {}
        segundo = any(m.get("role") == "assistant" for m in pedido.get("messages", []))
        if "/v1/messages" in self.path:
            if segundo:
                self.responder({"id":"msg_2","type":"message","role":"assistant","model":"claude-ensaio","content":[{"type":"text","text":"feito"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7}})
            else:
                self.responder({"id":"msg_1","type":"message","role":"assistant","model":"claude-ensaio","content":[
                    {"type":"thinking","thinking":"S-PENSA-0 <b> & e","signature":"U0lH-0+/=="},
                    {"type":"redacted_thinking","data":"UkVE-0"},
                    {"type":"tool_use","id":"toolu_Exigente00","name":"doc_read","input":{"doc_id":"notas"}}],
                    "stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7}})
            return
        if segundo:
            self.responder({"id":"c2","object":"chat.completion","created":1700000000,"model":"modelo-falso","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}})
        else:
            self.responder({"id":"c1","object":"chat.completion","created":1700000000,"model":"modelo-falso","choices":[{"index":0,"message":{"role":"assistant","content":None,
                "reasoning_content":"S-RACIOCINIO-0","thinking_blocks":BLOCOS,
                "tool_calls":[{"id":"toolu_Exigente00","type":"function","thought_signature":"S-ASSINATURA-0/+=","function":{"name":"doc_read","arguments":'{"doc_id":"notas"}'}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}})
ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
`

// aos515ModeloAnthropic é o nome que a rota anthropic/ do ensaio pede ao falso. Tem de ser um
// nome que a imagem fixada do proxy CONHEÇA como modelo com raciocínio: com um nome desconhecido
// o proxy recusa o parâmetro `thinking` antes de contactar o provider (medido).
const aos515ModeloAnthropic = "anthropic/claude-sonnet-4-5"

// aos515Sonda é o que o proxy fez a um parâmetro do pedido numa rota de sonda.
type aos515Sonda struct {
	Rota    string `json:"rota"`
	Enviado string `json:"enviado"`
	Status  int    `json:"status"`
	// ChegouAoProvider diz se o proxy chegou a contactar o provider.
	ChegouAoProvider bool `json:"chegou_ao_provider"`
	// Chegou é o valor cru de cada parâmetro tal como o provider o recebeu.
	Chegou map[string]string `json:"chegou,omitempty"`
}

// aos515Medida é o que chegou ao provider no segundo turno de uma rota.
type aos515Medida struct {
	Rota string `json:"rota"`
	// Erro é o erro do turno que não se completou (vazio se os dois se completaram).
	Erro string `json:"erro,omitempty"`
	// Pedidos é quantos pedidos o provider recebeu desta rota.
	Pedidos int `json:"pedidos"`
	// ChavesDoTopo são as chaves do corpo do segundo pedido, como o provider o recebeu.
	ChavesDoTopo []string `json:"chaves_do_topo,omitempty"`
	// ChavesDoAssistant são as chaves da mensagem `assistant` com a tool call.
	ChavesDoAssistant []string `json:"chaves_do_assistant,omitempty"`
	// O que sobreviveu, por sentinela: o texto e a assinatura do bloco, o bloco redigido, o
	// bloco de texto vazio, o raciocínio em texto, a assinatura da tool call, o id do provider.
	Sobreviveu map[string]bool `json:"sobreviveu,omitempty"`
	// Parametros é o valor cru de cada parâmetro do perfil tal como chegou ao provider.
	Parametros map[string]string `json:"parametros,omitempty"`
	// EstadoRecebido diz o que o gateway capturou no primeiro turno (os nomes dos campos).
	EstadoRecebido []string `json:"estado_recebido,omitempty"`
}

func TestAOS515_ProxyReal_OQueChegaAoProviderNoSegundoTurno(t *testing.T) {
	if os.Getenv("AOS_WIRE_LIVE") != "1" {
		t.Skip("SALTADO: AOS_WIRE_LIVE != 1 — o ensaio com o proxy REAL precisa de Docker; correr com `bash scripts/ci/wire-live.sh`. " +
			"POR VERIFICAR nesta execucao: se o estado e os parametros que o gateway envia atravessam o proxy. " +
			"O mesmo, sem proxy, correu (TestAOS515_Obrigatorio_OFalsoExigenteAceitaEOControloFicaVermelho).")
	}
	sufixo := fmt.Sprintf("%d", os.Getpid())
	e := &aos505Ensaio{t: t, rede: "aos515-rede-" + sufixo, falsoA: "aos515-falso-" + sufixo, proxy: "aos515-proxy-" + sufixo}
	if out, err := e.docker("image", "inspect", "--format", "{{.Id}}", aos505ImagemDoProxy); err != nil {
		t.Fatalf("AOS_WIRE_LIVE=1 foi pedido e a imagem do proxy nao esta disponivel (este teste nao a descarrega): %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = e.docker("rm", "-f", e.proxy, e.falsoA)
		_, _ = e.docker("network", "rm", e.rede)
	})
	e.dockerOuFalha("network", "create", e.rede)
	// Duas rotas: os nomes pedidos são os da allowlist embebida; o que está por baixo é do ensaio.
	// As duas rotas do gateway, e as rotas de SONDA dos parâmetros (pedidos crus, sem gateway):
	// a mesma classe de modelo com e sem `allowed_openai_params`, e com `drop_params` por rota.
	rota := func(nome, modelo, base, extra string) string {
		return "  - model_name: " + nome + "\n    litellm_params:\n      model: " + modelo + "\n      api_key: sk-ensaio-provider\n      api_base: http://" + e.falsoA + ":8080" + base + "\n" + extra
	}
	const permitidos = "      allowed_openai_params: [\"thinking\", \"reasoning_effort\"]\n"
	cfg := "model_list:\n" +
		rota("gpt-4o", "openai/k3", "/v1", permitidos) +
		rota("gpt-4o-mini", aos515ModeloAnthropic, "", "") +
		rota("sonda-openai", "openai/k3", "/v1", "") +
		rota("sonda-openai-permitidos", "openai/k3", "/v1", permitidos) +
		rota("sonda-openai-drop", "openai/k3", "/v1", "      drop_params: true\n") +
		rota("sonda-anthropic", aos515ModeloAnthropic, "", "") +
		rota("sonda-anthropic-desconhecido", "anthropic/claude-ensaio", "", "") +
		"litellm_settings:\n  drop_params: false\n  telemetry: false\n  num_retries: 0\ngeneral_settings:\n  background_health_checks: false\n"
	dir := t.TempDir()
	for nome, conteudo := range map[string][]byte{"config.yaml": []byte(cfg), "pronto": []byte("1")} {
		if err := os.WriteFile(filepath.Join(dir, nome), conteudo, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.dockerOuFalha("run", "-d", "--name", e.falsoA, "--network", e.rede, "-p", "127.0.0.1::8080", "--entrypoint", "python", aos505ImagemDoProxy, "-c", aos515FalsoNoContentor)
	e.dockerOuFalha("run", "-d", "--name", e.proxy, "--network", e.rede, "-p", "127.0.0.1::4000",
		"-e", "LITELLM_MASTER_KEY="+aos505ChaveMestra, "--entrypoint", "python", aos505ImagemDoProxy, "-c", aos508ArranqueDoProxy)
	e.dockerOuFalha("cp", filepath.Join(dir, "config.yaml"), e.proxy+":/tmp/config.yaml")
	e.dockerOuFalha("cp", filepath.Join(dir, "pronto"), e.proxy+":/tmp/config.pronto")
	porta := func(contentor, interna string) string {
		mapeada := strings.TrimSpace(strings.Split(e.dockerOuFalha("port", contentor, interna), "\n")[0])
		return "http://127.0.0.1:" + mapeada[strings.LastIndex(mapeada, ":")+1:]
	}
	proxy, falso := porta(e.proxy, "4000/tcp"), porta(e.falsoA, "8080/tcp")
	vivo := false
	for limite := time.Now().Add(4 * time.Minute); time.Now().Before(limite) && !vivo; time.Sleep(time.Second) {
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(proxy + "/health/liveliness"); err == nil {
			_ = resp.Body.Close()
			vivo = resp.StatusCode == http.StatusOK
		}
	}
	if !vivo {
		logs, _ := e.docker("logs", "--tail", "40", e.proxy)
		t.Fatalf("o proxy nao ficou vivo em 4 min:\n%s", logs)
	}
	recebidos := func() (out []struct{ Caminho, Corpo string }) {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(falso + "/pedidos")
		if err != nil {
			t.Fatalf("ler os pedidos do falso: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		cru, _ := io.ReadAll(resp.Body)
		var lidos []struct {
			Caminho string `json:"caminho"`
			Corpo   string `json:"corpo"`
		}
		if err := json.Unmarshal(cru, &lidos); err != nil {
			t.Fatalf("pedidos do falso ilegiveis: %v", err)
		}
		for _, l := range lidos {
			out = append(out, struct{ Caminho, Corpo string }{l.Caminho, l.Corpo})
		}
		return out
	}

	const params = `,"params":{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000},"devolver":"obrigatorio"`
	perfis := map[string]string{
		// Na rota openai/ o proxy fixado recusa `thinking` (medido pelas sondas): o perfil leva
		// `reasoning_effort`, que passa com `allowed_openai_params`.
		"gpt-4o":      `{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"reasoning_effort":"high","max_tokens":16000},"devolver":"obrigatorio"}`,
		"gpt-4o-mini": `{"requested":"gpt-4o-mini","expected_model":"` + aos515ModeloAnthropic + `","wire_class":"openai-chat-completions","capabilities":["tools"]` + params + `}`,
	}
	// AS SONDAS DOS PARÂMETROS (AOS-513): um pedido cru por rota de sonda e por parâmetro; regista-se
	// o status do proxy e o valor com que o parâmetro chegou ao provider (ausente se não chegou).
	var sondas []aos515Sonda
	for _, nome := range []string{"sonda-openai", "sonda-openai-permitidos", "sonda-openai-drop", "sonda-anthropic", "sonda-anthropic-desconhecido"} {
		for _, param := range []string{`"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000`, `"thinking":{"type":"disabled"}`, `"reasoning_effort":"high"`, `"max_tokens":16000`} {
			antes := len(recebidos())
			req, _ := http.NewRequest(http.MethodPost, proxy+"/v1/chat/completions", strings.NewReader(`{"model":"`+nome+`","messages":[{"role":"user","content":"ola"}],`+param+`}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+aos505ChaveMestra)
			resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
			if err != nil {
				t.Fatalf("sonda %s: %v", nome, err)
			}
			_ = resp.Body.Close()
			sd := aos515Sonda{Rota: nome, Enviado: param, Status: resp.StatusCode, Chegou: map[string]string{}}
			if novos := recebidos()[antes:]; len(novos) > 0 {
				var topo map[string]json.RawMessage
				_ = json.Unmarshal([]byte(novos[0].Corpo), &topo)
				for _, k := range []string{"thinking", "reasoning_effort", "max_tokens", "max_completion_tokens", "output_config"} {
					if v, tem := topo[k]; tem {
						sd.Chegou[k] = string(v)
					}
				}
				sd.ChegouAoProvider = true
			}
			sondas = append(sondas, sd)
			t.Logf("AOS513_SONDA %s", func() string { b, _ := json.Marshal(sd); return string(b) }())
		}
	}

	var medidas []aos515Medida
	for _, rota := range []string{"gpt-4o", "gpt-4o-mini"} {
		antes := len(recebidos())
		run := aos515ComporEm(t, proxy+"/v1", &http.Client{Timeout: 60 * time.Second}, aos505ChaveMestra, aos513Perfil(t, perfis[rota]))
		m := aos515Medida{Rota: rota}
		for turno := 1; turno <= 2 && m.Erro == ""; turno++ {
			out, err := run.passo(rota)
			if err != nil {
				m.Erro = fmt.Sprintf("turno %d: %v", turno, err)
				break
			}
			if turno == 1 {
				if st := out.State.Normalizado(); st != nil && len(st.Bytes) > 0 {
					var env struct {
						Fields []struct {
							Where, Name string
						} `json:"fields"`
						ToolCalls []struct {
							IDValue string `json:"id_value"`
							Fields  []struct{ Where, Name string }
						} `json:"tool_calls"`
						Served string `json:"served_model"`
					}
					_ = json.Unmarshal(st.Bytes, &env)
					for _, f := range env.Fields {
						m.EstadoRecebido = append(m.EstadoRecebido, f.Where+"."+f.Name)
					}
					for _, tc := range env.ToolCalls {
						m.EstadoRecebido = append(m.EstadoRecebido, "tool_call.id="+tc.IDValue)
						for _, f := range tc.Fields {
							m.EstadoRecebido = append(m.EstadoRecebido, f.Where+"."+f.Name)
						}
					}
					m.EstadoRecebido = append(m.EstadoRecebido, "servido="+env.Served)
				}
			}
		}
		daRota := recebidos()[antes:]
		m.Pedidos = len(daRota)
		if len(daRota) >= 2 {
			segundo := daRota[1].Corpo
			var topo map[string]json.RawMessage
			_ = json.Unmarshal([]byte(segundo), &topo)
			for k := range topo {
				m.ChavesDoTopo = append(m.ChavesDoTopo, k)
			}
			var msgs []map[string]json.RawMessage
			_ = json.Unmarshal(topo["messages"], &msgs)
			for _, msg := range msgs {
				if string(msg["role"]) == `"assistant"` {
					for k := range msg {
						m.ChavesDoAssistant = append(m.ChavesDoAssistant, k)
					}
				}
			}
			m.Sobreviveu = map[string]bool{}
			for nome, sentinela := range map[string]string{
				"texto do bloco": "S-PENSA-0", "assinatura do bloco": "U0lH-0+/==", "bloco redigido": "UkVE-0", "bloco de texto vazio": "VkFaSU8-0",
				"raciocinio em texto": "S-RACIOCINIO-0", "assinatura da tool call": "S-ASSINATURA-0/+=", "id do provider": "toolu_Exigente00",
			} {
				m.Sobreviveu[nome] = strings.Contains(segundo, sentinela)
			}
			m.Parametros = map[string]string{}
			for _, p := range []string{"thinking", "reasoning_effort", "max_tokens", "output_config"} {
				if v, tem := topo[p]; tem {
					m.Parametros[p] = string(v)
				}
			}
		}
		medidas = append(medidas, m)
		t.Logf("AOS515_MEDIDA %s", func() string { b, _ := json.Marshal(m); return string(b) }())
		if len(daRota) >= 2 {
			t.Logf("AOS515_SEGUNDO_PEDIDO %s %s", rota, daRota[1].Corpo)
		}
	}
	// A rota de produção (openai/…) tem de completar os dois turnos: é a que os testes sem proxy
	// provam. A rota anthropic/… é medição pura — regista-se o que aconteceu.
	if medidas[0].Erro != "" || medidas[0].Pedidos != 2 {
		t.Errorf("a rota openai/ nao completou os dois turnos atras do proxy: %+v", medidas[0])
	}
	raw, _ := json.Marshal(map[string]any{"imagem": aos505ImagemDoProxy, "medidas": medidas, "sondas_de_parametros": sondas, "pass": !t.Failed()})
	if destino := os.Getenv("AOS_WIRE_LIVE_OUT_515"); destino != "" {
		if err := os.WriteFile(destino, raw, 0o644); err != nil {
			t.Errorf("gravar as medidas em %s: %v", destino, err)
		}
	}
	t.Logf("AOS_WIRE_LIVE_REPORT_515 %s", raw)
}
