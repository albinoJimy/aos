package modelgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-513 — O PERFIL DA ROTA DECLARA PARÂMETROS DO PEDIDO, A VERSÃO DA PROJECÇÃO E A CLASSE DE
// ESTADO. Sem perfil que os declare, tudo é o de antes, byte a byte.

// aos513Digests são os digests dos quatro perfis da tabela, medidos ANTES do AOS-513 (base
// c3db6ad6). Um perfil que não declara nenhum campo novo tem de continuar a dar estes bytes.
var aos513Digests = map[string]string{
	"gpt-4o-mini":     "sha256:0cbc100e54038f013a6c84867ff08510095f47dd17b2dd07f68b5dc74f090d0f",
	"gpt-4o":          "sha256:efdd13fbdba0b96a8bc83cad7e2fb3f30f7e3ce5553a4eb0f736d7ab69efba36",
	"kimi-for-coding": "sha256:8ed9035604f0339c00e51557c4e0be712126b5f7d381853483acfdd67112cd64",
	"k3":              "sha256:3047b14cc414c48103182b051c0be23b6430c0ad2d53c0b4bb9eafa822958095",
}

// aos513Provider grava o corpo de cada pedido e responde o status e o corpo dados.
type aos513Provider struct {
	mu     sync.Mutex
	corpos [][]byte
	status int
	corpo  string
}

func (p *aos513Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.corpos = append(p.corpos, corpo)
	status, resposta := p.status, p.corpo
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resposta))
		return
	}
	_, _ = w.Write([]byte(`{"id":"cmpl-513","object":"chat.completion","model":"gpt-4o",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
}

func (p *aos513Provider) pedidos() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.corpos...)
}

type aos513Montagem struct {
	gw       *modelgateway.Gateway
	provider *aos513Provider
	set      *modelgateway.RouteProfileSet
	recusas  []modelgateway.RouteParamsRejection
}

// aos513Compor monta o gateway de produção à frente do provider de ensaio, com os perfis
// candidatos dados.
func aos513Compor(t *testing.T, provider *aos513Provider, candidatos ...modelgateway.RouteProfile) *aos513Montagem {
	t.Helper()
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	m := &aos513Montagem{provider: provider}
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.RouteProfiles = candidatos
	cfg.RouteParamsObserver = func(r modelgateway.RouteParamsRejection) { m.recusas = append(m.recusas, r) }
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	set, err := modelgateway.NewRouteProfileSet(candidatos...)
	if err != nil {
		t.Fatalf("NewRouteProfileSet: %v", err)
	}
	m.gw, m.set = gw, set
	return m
}

func aos513Perfil(t *testing.T, doc string) modelgateway.RouteProfile {
	t.Helper()
	p, err := modelgateway.ParseRouteProfile([]byte(doc))
	if err != nil {
		t.Fatalf("ParseRouteProfile(%s): %v", doc, err)
	}
	return p
}

const aos513Base = `"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]`

// INERTE: os perfis da tabela não declaram nenhum campo novo, e os seus digests são os de antes.
// Um perfil que declare a omissão por extenso (`devolver: nunca`, `params: {}`) tem o MESMO
// digest; um que declare qualquer coisa tem outro, e cada parâmetro muda-o.
func TestAOS513_Inerte_DigestsDosPerfisDeHoje(t *testing.T) {
	perfis := modelgateway.RouteProfiles()
	if len(perfis) != len(aos513Digests) {
		t.Fatalf("a tabela tem %d perfis; os digests de antes sao %d — um perfil novo entra com o seu digest neste teste", len(perfis), len(aos513Digests))
	}
	for _, p := range perfis {
		if p.Params != nil || p.ProjectionVersion != "" || p.StateReturn != modelgateway.StateReturnNever {
			t.Fatalf("o perfil %q declara um campo do AOS-513: a rota de producao so muda quando o dono assinar outro perfil", p.Requested)
		}
		if got := p.Digest(); got != aos513Digests[p.Requested] {
			t.Fatalf("o digest do perfil %q mudou: %s (antes %s)", p.Requested, got, aos513Digests[p.Requested])
		}
	}
	if set, err := modelgateway.NewRouteProfileSet(); err != nil || len(set.WithParams()) != 0 {
		t.Fatalf("a tabela em codigo tem de validar e nao ter perfis com parametros: %v %v", set.WithParams(), err)
	}
	base := aos513Digests["gpt-4o"]
	for _, doc := range []string{`{` + aos513Base + `}`, `{` + aos513Base + `,"devolver":"nunca"}`, `{` + aos513Base + `,"params":{}}`} {
		if got := aos513Perfil(t, doc).Digest(); got != base {
			t.Fatalf("a omissao escrita por extenso mudou o digest: %s deu %s", doc, got)
		}
	}
	vistos := map[string]string{base: "base"}
	for _, doc := range []string{
		`{` + aos513Base + `,"params":{"max_tokens":16000}}`,
		`{` + aos513Base + `,"params":{"max_tokens":16001}}`,
		`{` + aos513Base + `,"params":{"reasoning_effort":"low"}}`,
		`{` + aos513Base + `,"params":{"reasoning_effort":"high"}}`,
		`{` + aos513Base + `,"params":{"thinking":{"type":"disabled"}}}`,
		`{` + aos513Base + `,"params":{"thinking":{"type":"enabled"}}}`,
		`{` + aos513Base + `,"params":{"thinking":{"type":"enabled","budget_tokens":2048}}}`,
		`{` + aos513Base + `,"params":{"thinking":{"type":"adaptive"}}}`,
		`{` + aos513Base + `,"projection_version":"1.2.0"}`,
		`{` + aos513Base + `,"projection_version":"1.1.0"}`,
		`{` + aos513Base + `,"devolver":"opcional"}`,
		`{` + aos513Base + `,"devolver":"obrigatorio"}`,
	} {
		d := aos513Perfil(t, doc).Digest()
		if outro, repetido := vistos[d]; repetido {
			t.Fatalf("dois perfis diferentes com o mesmo digest: %s e %s", doc, outro)
		}
		vistos[d] = doc
	}
}

// INERTE NO WIRE: com a tabela em código o corpo do pedido é o golden de antes, e o que um
// CHAMADOR ponha nos campos de raciocínio do pedido é deitado fora — os parâmetros não têm
// origem que não seja o perfil. O JSON de um pedido também não os lê.
func TestAOS513_Inerte_OPedidoEODeAntesEOChamadorNaoDeclaraParametros(t *testing.T) {
	const golden = `{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}]}`
	m := aos513Compor(t, &aos513Provider{})
	limpo := aos505Pedido("gpt-4o")
	doChamador := aos505Pedido("gpt-4o")
	doChamador.Thinking = &port.ThinkingParam{Type: port.ThinkingTypeDisabled}
	doChamador.ReasoningEffort = port.ReasoningEffortHigh
	var doJSON port.ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}],"thinking":{"type":"disabled"},"reasoning_effort":"high","params":{"thinking":{"type":"disabled"}}}`), &doJSON); err != nil {
		t.Fatal(err)
	}
	if doJSON.Thinking != nil || doJSON.ReasoningEffort != "" {
		t.Fatalf("o JSON de um pedido declarou parametros de raciocinio: %+v", doJSON)
	}
	doJSON.Principal, doJSON.Board, doJSON.Region = limpo.Principal, limpo.Board, limpo.Region
	for nome, pedido := range map[string]port.ChatRequest{"limpo": limpo, "campos do chamador": doChamador, "lido de JSON": doJSON} {
		resp, err := m.gw.Chat(context.Background(), pedido)
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		if resp.SentParams != nil {
			t.Fatalf("%s: a resposta declara parametros enviados sem perfil que os declare: %v", nome, resp.SentParams)
		}
		corpos := m.provider.pedidos()
		if got := string(corpos[len(corpos)-1]); got != golden {
			t.Fatalf("%s: o corpo do pedido nao e o de antes:\n veio:  %s\n quero: %s", nome, got, golden)
		}
	}
	if len(m.recusas) != 0 {
		t.Fatalf("o observador dos parametros foi chamado sem parametros: %v", m.recusas)
	}
	// Pelo adaptador do runtime: o turno não declara parâmetros, e a versão da projecção é a do
	// interruptor do nó.
	out, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")).
		Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
	if err != nil || out.RequestParams != nil {
		t.Fatalf("turno sem perfil com parametros: %+v err=%v", out.RequestParams, err)
	}
}

// A LEITURA É FECHADA: uma chave fora do conjunto, um valor de tipo errado, um valor fora do
// vocabulário, uma versão não publicada, um texto com forma de credencial — tudo recusa, e a
// mensagem não repete o valor recusado.
func TestAOS513_Perfil_LeituraFechada(t *testing.T) {
	const marca = "VALOR-RECUSADO-513"
	maus := map[string]string{
		"chave desconhecida no perfil":      `{` + aos513Base + `,"extra_body":{"x":"` + marca + `"}}`,
		"parametro fora do conjunto":        `{` + aos513Base + `,"params":{"temperature":0.2}}`,
		"parametro livre":                   `{` + aos513Base + `,"params":{"` + marca + `":"x"}}`,
		"chave desconhecida em thinking":    `{` + aos513Base + `,"params":{"thinking":{"type":"enabled","display":"` + marca + `"}}}`,
		"thinking em string":                `{` + aos513Base + `,"params":{"thinking":"` + marca + `"}}`,
		"max_tokens em string":              `{` + aos513Base + `,"params":{"max_tokens":"16000"}}`,
		"max_tokens negativo":               `{` + aos513Base + `,"params":{"max_tokens":-1}}`,
		"max_tokens acima do tecto":         `{` + aos513Base + `,"params":{"max_tokens":99999999}}`,
		"thinking.type fora do vocabulario": `{` + aos513Base + `,"params":{"thinking":{"type":"` + marca + `"}}}`,
		"budget com disabled":               `{` + aos513Base + `,"params":{"thinking":{"type":"disabled","budget_tokens":2048}}}`,
		"budget abaixo do minimo":           `{` + aos513Base + `,"params":{"thinking":{"type":"enabled","budget_tokens":5}}}`,
		"reasoning_effort fora":             `{` + aos513Base + `,"params":{"reasoning_effort":"` + marca + `"}}`,
		"reasoning_effort em numero":        `{` + aos513Base + `,"params":{"reasoning_effort":3}}`,
		"versao nao publicada":              `{` + aos513Base + `,"projection_version":"9.9.9"}`,
		"texto livre na versao":             `{` + aos513Base + `,"projection_version":"` + marca + `"}`,
		"devolver fora do vocabulario":      `{` + aos513Base + `,"devolver":"` + marca + `"}`,
		"wire desconhecido":                 `{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"` + marca + `","capabilities":["tools"]}`,
		"capacidade desconhecida":           `{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["` + marca + `"]}`,
		"sem nome pedido":                   `{"expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"nome com espaco":                   `{"requested":"gpt 4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"chave de API no nome esperado":     `{"requested":"gpt-4o","expected_model":"openai/sk-VALORRECUSADO513abcdef0123456789","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"segredo longo no nome pedido":      `{"requested":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"conteudo depois do perfil":         `{` + aos513Base + `} {"x":1}`,
		"nao e JSON":                        marca,
		"lista em vez de objecto":           `[{` + aos513Base + `}]`,
	}
	for nome, doc := range maus {
		_, err := modelgateway.ParseRouteProfile([]byte(doc))
		if !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Errorf("%s: tinha de recusar com ErrBadRouteProfile; veio %v", nome, err)
			continue
		}
		if strings.Contains(err.Error(), marca) || strings.Contains(err.Error(), "VALORRECUSADO") {
			t.Errorf("%s: a mensagem repete o valor recusado: %v", nome, err)
		}
	}
	// Um perfil recusado não compõe gateway, venha como candidato ou em duplicado.
	bom := aos513Perfil(t, `{`+aos513Base+`,"params":{"max_tokens":16000}}`)
	mau := bom
	mau.Params = &port.RequestParams{ReasoningEffort: marca}
	for nome, candidatos := range map[string][]modelgateway.RouteProfile{"invalido": {mau}, "repetido": {bom, bom}} {
		cfg := prodConfig(audit.NewMemStore(), "http://127.0.0.1:1", http.DefaultClient, []modelgateway.InfraAccount{{KeyID: "a", Provider: "openai", Region: "eu"}})
		cfg.RouteProfiles = candidatos
		if _, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Fatalf("candidato %s: a composicao tinha de recusar; veio %v", nome, err)
		}
	}
}

// OS PARÂMETROS CHEGAM AO PROVIDER, vêm só do perfil da rota a que o pedido vai, e ficam
// declarados na resposta para o manifesto do turno.
func TestAOS513_Parametros_NoWireENaResposta(t *testing.T) {
	perfil := aos513Perfil(t, `{`+aos513Base+`,"params":{"thinking":{"type":"enabled","budget_tokens":2048},"reasoning_effort":"high","max_tokens":16000}}`)
	m := aos513Compor(t, &aos513Provider{}, perfil)
	if !reflect.DeepEqual(m.set.WithParams(), []string{"gpt-4o"}) {
		t.Fatalf("perfis com parametros: %v", m.set.WithParams())
	}
	// O chamador tenta sobrepor os parâmetros: valem os do perfil.
	pedido := aos505Pedido("gpt-4o")
	pedido.Thinking, pedido.ReasoningEffort, pedido.MaxTokens = &port.ThinkingParam{Type: port.ThinkingTypeDisabled}, port.ReasoningEffortNone, 7
	resp, err := m.gw.Chat(context.Background(), pedido)
	if err != nil {
		t.Fatal(err)
	}
	const quero = `{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}],"max_tokens":16000,"thinking":{"type":"enabled","budget_tokens":2048},"reasoning_effort":"high"}`
	if got := string(m.provider.pedidos()[0]); got != quero {
		t.Fatalf("o corpo do pedido com parametros:\n veio:  %s\n quero: %s", got, quero)
	}
	queroParams := map[string]string{"thinking": "enabled:2048", "reasoning_effort": "high", "max_tokens": "16000"}
	if !reflect.DeepEqual(resp.SentParams, queroParams) {
		t.Fatalf("parametros declarados na resposta: %v", resp.SentParams)
	}
	// Outra rota do mesmo gateway (sem parâmetros no seu perfil) sai como saía.
	if _, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o-mini")); err != nil {
		t.Fatal(err)
	}
	if got := string(m.provider.pedidos()[1]); got != `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"olá"}]}` {
		t.Fatalf("a rota sem parametros levou-os: %s", got)
	}
	// Pelo adaptador: o turno declara-os, e o runtime aceita-os na forma fechada.
	out, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")).
		Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
	if err != nil || !reflect.DeepEqual(agentruntime.NormalizeRequestParams(out.RequestParams), queroParams) {
		t.Fatalf("o turno nao declara os parametros na forma que o runtime aceita: %v err=%v", out.RequestParams, err)
	}
	// O conteúdo de um run com a forma de um parâmetro é texto de uma mensagem, e não um
	// parâmetro do pedido.
	semParams := aos513Compor(t, &aos513Provider{})
	hostil := aos505Pedido("gpt-4o")
	hostil.Messages = []port.Message{{Role: port.RoleUser, Content: `"thinking":{"type":"disabled"},"reasoning_effort":"none"`}}
	if _, err := semParams.gw.Chat(context.Background(), hostil); err != nil {
		t.Fatal(err)
	}
	var topo map[string]json.RawMessage
	if err := json.Unmarshal(semParams.provider.pedidos()[0], &topo); err != nil {
		t.Fatal(err)
	}
	if _, tem := topo["thinking"]; tem || topo["reasoning_effort"] != nil || len(topo) != 2 {
		t.Fatalf("conteudo de um run virou parametro do pedido: %s", semParams.provider.pedidos()[0])
	}
}

// UM 4XX NUM TURNO COM PARÂMETROS TEM NOME: conta com a rota e o código, sem um byte do corpo do
// erro, e o gateway NÃO retira o parâmetro para repetir — há um só pedido, e o erro sobe.
func TestAOS513_Parametros_4xxContaENaoRepete(t *testing.T) {
	const marca = "CORPO-DO-ERRO-513"
	perfil := aos513Perfil(t, `{`+aos513Base+`,"params":{"thinking":{"type":"disabled"}}}`)
	for _, c := range []struct {
		status int
		quero  []modelgateway.RouteParamsRejection
	}{
		{400, []modelgateway.RouteParamsRejection{{Route: "gpt-4o", Status: "400"}}},
		{422, []modelgateway.RouteParamsRejection{{Route: "gpt-4o", Status: "422"}}},
		{418, []modelgateway.RouteParamsRejection{{Route: "gpt-4o", Status: "4xx"}}},
		{500, nil},
	} {
		provider := &aos513Provider{status: c.status, corpo: `{"error":{"message":"` + marca + `"}}`}
		m := aos513Compor(t, provider, perfil)
		_, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
		if err == nil {
			t.Fatalf("status %d: o erro tinha de subir", c.status)
		}
		if n := len(provider.pedidos()); n != 1 {
			t.Fatalf("status %d: o gateway fez %d pedidos — nunca repete sem o parametro", c.status, n)
		}
		if !bytes.Contains(provider.pedidos()[0], []byte(`"thinking":{"type":"disabled"}`)) {
			t.Fatalf("status %d: o pedido nao levou o parametro", c.status)
		}
		if !reflect.DeepEqual(m.recusas, c.quero) {
			t.Fatalf("status %d: contado %v, quero %v", c.status, m.recusas, c.quero)
		}
		for _, r := range m.recusas {
			if strings.Contains(r.Route+r.Status, marca) {
				t.Fatalf("o corpo do erro chegou a um rotulo: %+v", r)
			}
		}
		// A mesma resposta numa rota SEM parâmetros não conta.
		sem := aos513Compor(t, &aos513Provider{status: c.status, corpo: "{}"})
		_, _ = sem.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
		if len(sem.recusas) != 0 {
			t.Fatalf("status %d: contou numa rota sem parametros: %v", c.status, sem.recusas)
		}
	}
	codigos := map[string]bool{}
	for _, s := range modelgateway.RouteParamsRejectionStatuses() {
		codigos[s] = true
	}
	if !codigos["400"] || !codigos[modelgateway.RouteParamsRejectionOther] || len(codigos) != 10 {
		t.Fatalf("o vocabulario do codigo mudou: %v", modelgateway.RouteParamsRejectionStatuses())
	}
}

// A VERSÃO DA PROJECÇÃO POR ROTA: o perfil prevalece sobre o interruptor do nó; a versão em que o
// run está fixado prevalece sobre os dois; e uma versão fixada que o binário não conhece falha
// fechado, sem pedido.
func TestAOS513_Projeccao_PerfilSobreOInterruptorERunSobreOPerfil(t *testing.T) {
	perfil := aos513Perfil(t, `{`+aos513Base+`,"projection_version":"1.2.0"}`)
	set, err := modelgateway.NewRouteProfileSet(perfil)
	if err != nil {
		t.Fatal(err)
	}
	view := aos506VistaComTudo(t)
	system := func(fixada string, opts ...modelgateway.RuntimeAdapterOption) (string, string, error) {
		v := view
		v.ProjectionVersion = fixada
		req, out, err := aos490Pedir(t, v, append([]modelgateway.RuntimeAdapterOption{modelgateway.WithProjection(modelgateway.ProjectionNative)}, opts...)...)
		if err != nil {
			return "", "", err
		}
		return req.Messages[0].Content, out.ProjectionVersion, nil
	}
	referencia := func(versao string) string {
		msgs, err := modelgateway.ProjectNativeVersion(versao, view)
		if err != nil {
			t.Fatal(err)
		}
		return msgs[0].Content
	}
	no := modelgateway.WithProjectionVersion(modelgateway.NativeProjectionVersion110)
	for nome, c := range map[string]struct {
		fixada string
		opts   []modelgateway.RuntimeAdapterOption
		quero  string
	}{
		"sem perfil nem fixacao: a de sempre":        {"", nil, "1.0.0"},
		"so o interruptor do no":                     {"", []modelgateway.RuntimeAdapterOption{no}, "1.1.0"},
		"o perfil prevalece sobre o interruptor":     {"", []modelgateway.RuntimeAdapterOption{no, modelgateway.WithRouteProfileSet(set)}, "1.2.0"},
		"o perfil sem interruptor":                   {"", []modelgateway.RuntimeAdapterOption{modelgateway.WithRouteProfileSet(set)}, "1.2.0"},
		"o run fixado prevalece sobre o perfil":      {"1.1.0", []modelgateway.RuntimeAdapterOption{modelgateway.WithRouteProfileSet(set)}, "1.1.0"},
		"o run fixado prevalece sobre o interruptor": {"1.2.0", []modelgateway.RuntimeAdapterOption{no}, "1.2.0"},
	} {
		sys, versao, err := system(c.fixada, c.opts...)
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		if versao != c.quero || sys != referencia(c.quero) {
			t.Fatalf("%s: o turno foi na versao %q (quero %q)", nome, versao, c.quero)
		}
	}
	if _, _, err := system("9.9.9"); !errors.Is(err, modelgateway.ErrPinnedProjectionVersion) {
		t.Fatalf("um run fixado numa versao desconhecida tinha de falhar fechado; veio %v", err)
	}
	// Em texto único não há versão: a fixação não muda nada.
	v := view
	v.ProjectionVersion = "1.2.0"
	req, out, err := aos490Pedir(t, v)
	if err != nil || out.ProjectionVersion != "" || len(req.Messages) != 1 {
		t.Fatalf("texto unico com versao fixada: versao=%q msgs=%d err=%v", out.ProjectionVersion, len(req.Messages), err)
	}
}
