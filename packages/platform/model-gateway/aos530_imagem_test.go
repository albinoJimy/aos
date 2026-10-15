package modelgateway_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-530 — A CAPACIDADE `imagem` DO PERFIL DA ROTA (ADR-042 §2.4, barreira 2). Um pedido com
// uma parte de imagem só sai para uma rota RESOLVIDA cujo perfil declare `imagem`; de outro modo
// é recusado antes da credencial, do selo de atribuição, do adaptador e do custo.

// aos530PNG é um PNG mínimo: a assinatura e um corpo que não aparece por acaso em nenhum texto.
func aos530PNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("IHDR-aos530-gw-corpo-\x00\x01\xfe")...)
}

// aos530Modelo é um par (modelo, região) com preço na tabela embebida: o caminho que passa tem de
// passar também pela contabilidade de custo.
const (
	aos530Modelo = "claude-sonnet"
	aos530Regiao = "eu-west"
	aos530Run    = "run-aos530"
)

// aos530Adaptador conta as chamadas e guarda o último pedido que lhe chegou.
type aos530Adaptador struct {
	adapters.Adapter
	mu      sync.Mutex
	chamado int
	ultimo  port.ChatRequest
}

func (a *aos530Adaptador) Chat(ctx context.Context, req port.ChatRequest, cred adapters.Credential) (port.ChatResponse, error) {
	a.mu.Lock()
	a.chamado++
	a.ultimo = req
	a.mu.Unlock()
	return a.Adapter.Chat(ctx, req, cred)
}

func (a *aos530Adaptador) ChatStream(ctx context.Context, req port.ChatRequest, cred adapters.Credential) (port.ChatStream, error) {
	a.mu.Lock()
	a.chamado++
	a.ultimo = req
	a.mu.Unlock()
	return a.Adapter.ChatStream(ctx, req, cred)
}

// aos530Creds conta as buscas de credencial.
type aos530Creds struct {
	inner  *adapters.StaticCredentialSource
	buscas int
}

func (c *aos530Creds) Fetch(ctx context.Context, provider, region string) (adapters.Credential, error) {
	c.buscas++
	return c.inner.Fetch(ctx, provider, region)
}

// aos530Recuo é um estágio de roteamento que resolve o pedido para outra rota — um recuo.
type aos530Recuo struct{ para string }

func (aos530Recuo) Name() string { return "aos530-recuo" }
func (r aos530Recuo) Process(_ context.Context, ex *pipeline.Exchange) error {
	ex.ResolvedModel = r.para
	return nil
}

// aos530Authn resolve o principal (a atribuição recusa um registo sem ele).
type aos530Authn struct{}

func (aos530Authn) Name() string { return "aos530-authn" }
func (aos530Authn) Process(_ context.Context, ex *pipeline.Exchange) error {
	ex.PrincipalUser, ex.PrincipalAgent, ex.HumanRoot = "user-aos530", "agent-aos530", "user-aos530"
	return nil
}

type aos530Montagem struct {
	gw     *modelgateway.Gateway
	adapt  *aos530Adaptador
	creds  *aos530Creds
	custo  *cost.Recorder
	selos  *audit.MemStore
	tracer *otelgenai.RecordingTracer
}

// aos530Compor monta o gateway com contabilidade de custo, atribuição selada, um tracer que grava
// e os perfis candidatos dados. `recuo` não vazio liga o estágio de roteamento que resolve para lá.
func aos530Compor(t *testing.T, recuo string, candidatos ...modelgateway.RouteProfile) *aos530Montagem {
	t.Helper()
	table, err := pricing.LoadEmbedded()
	if err != nil {
		t.Fatalf("pricing.LoadEmbedded: %v", err)
	}
	set, err := modelgateway.NewRouteProfileSet(candidatos...)
	if err != nil {
		t.Fatalf("NewRouteProfileSet: %v", err)
	}
	fake := adapters.NewFakeAdapter("fake")
	for _, modelo := range []string{aos530Modelo, "gpt-4o"} {
		fake.SetChatResponse(modelo, port.ChatResponse{
			Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "texto lido"}, FinishReason: "stop"}},
			Usage:   port.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		})
	}
	m := &aos530Montagem{
		adapt:  &aos530Adaptador{Adapter: fake},
		creds:  &aos530Creds{inner: adapters.NewStaticCredentialSource()},
		custo:  cost.NewRecorder(cost.NewCalculator(table)),
		selos:  audit.NewMemStore(),
		tracer: otelgenai.NewRecordingTracer(nil),
	}
	m.creds.inner.Set("fake", aos530Regiao, "sk-teste-aos530")
	opts := []modelgateway.Option{
		modelgateway.WithCredentialSource(m.creds),
		modelgateway.WithDefaultRegion(aos530Regiao),
		modelgateway.WithClock(func() time.Time { return time.Unix(1_700_000_000, 0) }),
		modelgateway.WithTracer(m.tracer),
		modelgateway.WithCost(m.custo),
		modelgateway.WithAttribution(attribution.NewRecorder(m.selos)),
		modelgateway.WithRouteProfiles(set),
		modelgateway.WithAuthnStage(aos530Authn{}),
	}
	if recuo != "" {
		opts = append(opts, modelgateway.WithRoutingStage(aos530Recuo{para: recuo}))
	}
	m.gw = modelgateway.New(m.adapt, opts...)
	return m
}

func aos530PerfilComImagem(requested string) modelgateway.RouteProfile {
	return modelgateway.RouteProfile{Requested: requested, ExpectedModel: "openrouter/anthropic/claude-haiku-5.5",
		WireClass: modelgateway.WireOpenAIChat, Capabilities: []string{modelgateway.CapabilityImage}}
}

func aos530PedidoComImagem(modelo string) port.ChatRequest {
	return port.ChatRequest{Model: modelo, RunID: aos530Run, StepID: "step-000001", Principal: "tok", Board: "board-eu",
		Messages: []port.Message{
			{Role: port.RoleSystem, Content: "transcreve o texto da imagem"},
			{Role: port.RoleUser, Content: "imagem:", Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: aos530PNG()}}},
		}}
}

// aos530SemEfeito confere que a recusa não chegou a nada: nem credencial, nem selo, nem adaptador,
// nem custo.
func (m *aos530Montagem) aos530SemEfeito(t *testing.T) {
	t.Helper()
	if m.adapt.chamado != 0 {
		t.Fatalf("o adaptador foi chamado %d vezes; a recusa tinha de vir antes", m.adapt.chamado)
	}
	if m.creds.buscas != 0 {
		t.Fatalf("a credencial foi buscada %d vezes; a recusa tinha de vir antes", m.creds.buscas)
	}
	if len(m.selos.Partitions()) != 0 {
		t.Fatalf("a atribuicao foi selada (%v) para uma chamada que nao saiu", m.selos.Partitions())
	}
	if _, ok := m.custo.CostForRun(cost.RunKey{RunID: aos530Run, Tenant: "board-eu"}); ok {
		t.Fatal("houve custo contado para uma chamada recusada")
	}
	if runs, _ := m.custo.RetainedKeys(); runs != 0 {
		t.Fatalf("o recorder de custo reteve %d runs", runs)
	}
}

func aos530QueroRecusa(t *testing.T, err error, causa string) {
	t.Helper()
	var re *modelgateway.RouteWithoutImageError
	if !errors.Is(err, modelgateway.ErrRouteWithoutImage) || !errors.As(err, &re) || re.Cause != causa {
		t.Fatalf("erro = %v; quero ErrRouteWithoutImage com causa %s", err, causa)
	}
}

// A ROTA DE HOJE NÃO VÊ IMAGENS: o perfil da tabela em código não declara `imagem`.
func TestAOS530_Gateway_RotaSemImagem_RecusaAntesDeSair(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "")
	_, err := m.gw.Chat(context.Background(), aos530PedidoComImagem("gpt-4o"))
	aos530QueroRecusa(t, err, modelgateway.ImageCauseProfileWithout)
	m.aos530SemEfeito(t)
	// O span diz porquê, com vocabulário fechado.
	spans := m.tracer.SpansByOperation("chat")
	if len(spans) != 1 || spans[0].Attributes[agentruntime.AttrErrorType] != "route_without_image" {
		t.Fatalf("o span chat tinha de levar error.type=route_without_image: %+v", spans)
	}
	// O mesmo pedido sem a imagem passa: a recusa é da imagem, não da rota.
	semImagem := aos530PedidoComImagem("gpt-4o")
	semImagem.Messages[1].Images = nil
	if _, err := m.gw.Chat(context.Background(), semImagem); err != nil && errors.Is(err, modelgateway.ErrRouteWithoutImage) {
		t.Fatalf("um pedido sem imagens foi recusado pela capacidade: %v", err)
	}
	if m.adapt.chamado != 1 || m.creds.buscas != 1 {
		t.Fatalf("o pedido sem imagens tinha de chegar ao adaptador; chamado=%d buscas=%d", m.adapt.chamado, m.creds.buscas)
	}
	// Controlo: nesta montagem a atribuição sela — a ausência de selo acima é da recusa.
	if len(m.selos.Partitions()) == 0 {
		t.Fatal("a atribuicao nao selou o pedido que saiu: a verificacao de 'sem selo' nao verificava nada")
	}
}

// Uma rota sem perfil não declara nada: recusa, com a sua causa.
func TestAOS530_Gateway_RotaSemPerfil_Recusa(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "")
	_, err := m.gw.Chat(context.Background(), aos530PedidoComImagem("modelo-sem-perfil"))
	aos530QueroRecusa(t, err, modelgateway.ImageCauseNoProfile)
	m.aos530SemEfeito(t)
}

// O RECUO: o pedido nomeia um alias com `imagem`, o roteamento resolve-o para uma rota sem ela. A
// rota que conta é a resolvida — recusa, e nunca cai em silêncio para a outra rota.
func TestAOS530_Gateway_RecuoSemImagem_Recusa(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "gpt-4o", aos530PerfilComImagem(aos530Modelo))
	_, err := m.gw.Chat(context.Background(), aos530PedidoComImagem(aos530Modelo))
	aos530QueroRecusa(t, err, modelgateway.ImageCauseProfileWithout)
	m.aos530SemEfeito(t)
	// No streaming, a mesma recusa.
	if _, err := m.gw.ChatStream(context.Background(), aos530PedidoComImagem(aos530Modelo)); !errors.Is(err, modelgateway.ErrRouteWithoutImage) {
		t.Fatalf("ChatStream = %v; quero ErrRouteWithoutImage", err)
	}
	m.aos530SemEfeito(t)
	// E um recuo para uma rota sem perfil também.
	n := aos530Compor(t, "modelo-sem-perfil", aos530PerfilComImagem(aos530Modelo))
	_, err = n.gw.Chat(context.Background(), aos530PedidoComImagem(aos530Modelo))
	aos530QueroRecusa(t, err, modelgateway.ImageCauseNoProfile)
	n.aos530SemEfeito(t)
}

// O CAMINHO QUE PASSA: uma rota resolvida com `imagem` recebe a imagem tal como foi pedida, e a
// chamada é contabilizada como qualquer outra.
func TestAOS530_Gateway_RotaComImagem_Passa(t *testing.T) {
	t.Parallel()
	for _, recuo := range []string{"", aos530Modelo} {
		m := aos530Compor(t, recuo, aos530PerfilComImagem(aos530Modelo))
		resp, err := m.gw.Chat(context.Background(), aos530PedidoComImagem(aos530Modelo))
		if err != nil {
			t.Fatalf("recuo=%q: Chat: %v", recuo, err)
		}
		if resp.Choices[0].Message.Content != "texto lido" || m.adapt.chamado != 1 {
			t.Fatalf("recuo=%q: a chamada nao chegou ao adaptador (chamado=%d)", recuo, m.adapt.chamado)
		}
		imgs := m.adapt.ultimo.Messages[1].Images
		if len(imgs) != 1 || string(imgs[0].Data) != string(aos530PNG()) {
			t.Fatalf("recuo=%q: o adaptador nao recebeu a imagem exacta", recuo)
		}
		if runs, _ := m.custo.RetainedKeys(); runs != 1 || resp.Usage.CostMicroUSD <= 0 {
			t.Fatalf("recuo=%q: a chamada com imagem tinha de ser contabilizada", recuo)
		}
		aos530BytesFora(t, m)
	}
}

// aos530BytesFora confere que os bytes da imagem não estão em nenhum span nem em nenhum selo.
func aos530BytesFora(t *testing.T, m *aos530Montagem) {
	t.Helper()
	png := aos530PNG()
	b64 := base64.StdEncoding.EncodeToString(png)
	var textos []string
	for _, s := range m.tracer.Spans() {
		textos = append(textos, fmt.Sprintf("%v", s.Attributes))
	}
	for _, p := range m.selos.Partitions() {
		head, _ := m.selos.Head(context.Background(), p)
		recs, err := m.selos.Read(context.Background(), p, 1, head)
		if err != nil {
			t.Fatalf("Read(%s): %v", p, err)
		}
		cru, _ := json.Marshal(recs)
		textos = append(textos, string(cru), fmt.Sprintf("%+v", recs))
	}
	if len(textos) == 0 {
		t.Fatal("nada gravado: a verificacao nao verificou nada")
	}
	for _, s := range textos {
		if strings.Contains(s, b64[:12]) || strings.Contains(s, string(png[8:])) {
			t.Fatalf("os bytes da imagem estao num span ou num selo: %s", s)
		}
	}
}

// Uma parte de imagem inválida é recusada pela porta antes de tudo, com o erro da porta.
func TestAOS530_Gateway_ImagemInvalida_RecusaDaPorta(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "", aos530PerfilComImagem(aos530Modelo))
	req := aos530PedidoComImagem(aos530Modelo)
	req.Messages[1].Images[0].MediaType = port.ImageMediaJPEG
	if _, err := m.gw.Chat(context.Background(), req); !errors.Is(err, port.ErrImagePart) {
		t.Fatalf("Chat = %v; quero ErrImagePart", err)
	}
	m.aos530SemEfeito(t)
}

// O PERFIL: `imagem` é declarável (sozinha ou com `tools`, em JSON), outra capacidade continua
// recusada, e os digests dos perfis de hoje não mudam.
func TestAOS530_Perfil_CapacidadeImagem(t *testing.T) {
	t.Parallel()
	for _, caps := range []string{`["imagem"]`, `["imagem","tools"]`} {
		p, err := modelgateway.ParseRouteProfile([]byte(`{"requested":"ocr-visao","expected_model":"openrouter/anthropic/claude-haiku-5.5","wire_class":"openai-chat-completions","capabilities":` + caps + `}`))
		if err != nil {
			t.Fatalf("capabilities %s: %v", caps, err)
		}
		if p.Digest() == "" {
			t.Fatal("digest vazio")
		}
	}
	for _, caps := range []string{`["video"]`, `["Imagem"]`, `["image"]`} {
		if _, err := modelgateway.ParseRouteProfile([]byte(`{"requested":"x","expected_model":"y","wire_class":"openai-chat-completions","capabilities":` + caps + `}`)); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Fatalf("capabilities %s tinha de ser recusada: %v", caps, err)
		}
	}
	for _, p := range modelgateway.RouteProfiles() {
		for _, c := range p.Capabilities {
			if c == modelgateway.CapabilityImage {
				t.Fatalf("o perfil em codigo %q declara imagem: so um registo assinado o pode fazer", p.Requested)
			}
		}
		if got := p.Digest(); got != aos513Digests[p.Requested] {
			t.Fatalf("o digest do perfil %q mudou: %s", p.Requested, got)
		}
	}
}

// aos530Provider grava os corpos e responde o status e o corpo dados.
type aos530Provider struct {
	mu     sync.Mutex
	corpos [][]byte
	status int
	corpo  string
}

func (p *aos530Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.corpos = append(p.corpos, corpo)
	status, resposta := p.status, p.corpo
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resposta))
}

// PELO ADAPTADOR HTTP REAL: a imagem chega ao provider falso como `image_url` com os bytes exactos;
// sem a capacidade, o provider conta zero pedidos; e um erro do provider não ecoa a imagem.
func TestAOS530_Gateway_AdaptadorHTTP(t *testing.T) {
	t.Parallel()
	compor := func(p *aos530Provider, candidatos ...modelgateway.RouteProfile) *modelgateway.Gateway {
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		set, err := modelgateway.NewRouteProfileSet(candidatos...)
		if err != nil {
			t.Fatalf("NewRouteProfileSet: %v", err)
		}
		creds := adapters.NewStaticCredentialSource()
		creds.Set("openai", aos530Regiao, "sk-teste-aos530")
		return modelgateway.New(adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client()),
			modelgateway.WithCredentialSource(creds), modelgateway.WithDefaultRegion(aos530Regiao), modelgateway.WithRouteProfiles(set))
	}
	ok := `{"id":"c","object":"chat.completion","model":"ocr-visao","choices":[{"index":0,"message":{"role":"assistant","content":"lido"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}`

	// Sem `imagem`: zero pedidos.
	semCap := &aos530Provider{status: http.StatusOK, corpo: ok}
	if _, err := compor(semCap).Chat(context.Background(), aos530PedidoComImagem("ocr-visao")); !errors.Is(err, modelgateway.ErrRouteWithoutImage) {
		t.Fatalf("Chat = %v; quero ErrRouteWithoutImage", err)
	}
	if len(semCap.corpos) != 0 {
		t.Fatalf("o provider recebeu %d pedidos de uma rota sem imagem", len(semCap.corpos))
	}

	// Com `imagem`: a data URL com os bytes exactos.
	comCap := &aos530Provider{status: http.StatusOK, corpo: ok}
	if _, err := compor(comCap, aos530PerfilComImagem("ocr-visao")).Chat(context.Background(), aos530PedidoComImagem("ocr-visao")); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(comCap.corpos) != 1 {
		t.Fatalf("o provider recebeu %d pedidos", len(comCap.corpos))
	}
	quero := `{"role":"user","content":[{"type":"text","text":"imagem:"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
		base64.StdEncoding.EncodeToString(aos530PNG()) + `"}}]}`
	if !strings.Contains(string(comCap.corpos[0]), quero) {
		t.Fatalf("o corpo nao leva a imagem na forma medida:\n%s", comCap.corpos[0])
	}

	// Um 400 que ecoe o pedido não sobe com o corpo.
	b64 := base64.StdEncoding.EncodeToString(aos530PNG())
	eco := &aos530Provider{status: http.StatusBadRequest, corpo: `{"error":{"message":"imagem recusada: ` + b64 + `"}}`}
	_, err := compor(eco, aos530PerfilComImagem("ocr-visao")).Chat(context.Background(), aos530PedidoComImagem("ocr-visao"))
	if err == nil || strings.Contains(err.Error(), b64[:12]) || strings.Contains(err.Error(), "imagem recusada") {
		t.Fatalf("o erro do provider subiu com o corpo de um pedido com imagem: %v", err)
	}
}

// BARREIRA 1: o adaptador RT→GW dos turnos não tem por onde produzir uma parte de imagem. Prova
// dinâmica — o pedido que chega ao gateway, em texto único e em cada versão publicada da projecção
// nativa — e prova pela fonte: nem o adaptador nem a projecção nomeiam o campo.
func TestAOS530_AdaptadorDoRuntime_NuncaProduzImagens(t *testing.T) {
	t.Parallel()
	view := aos490VistaDeExemplo(t, aos490Layout)
	casos := [][]modelgateway.RuntimeAdapterOption{nil}
	for _, v := range []string{modelgateway.NativeProjectionVersion, modelgateway.NativeProjectionVersion110, modelgateway.NativeProjectionVersion120, modelgateway.NativeProjectionVersion130} {
		casos = append(casos, []modelgateway.RuntimeAdapterOption{modelgateway.WithProjection(modelgateway.ProjectionNative), modelgateway.WithProjectionVersion(v)})
	}
	for i, opts := range casos {
		req, _, err := aos490Pedir(t, view, opts...)
		if err != nil {
			t.Fatalf("caso %d: Call: %v", i, err)
		}
		if len(req.Messages) == 0 || req.HasImages() {
			t.Fatalf("caso %d: o turno do executor levou uma parte de imagem (ou nenhuma mensagem)", i)
		}
	}
	for _, f := range []string{"runtime_adapter.go", "projection.go", "projection_quase_cabecalhos.go"} {
		fonte, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", f, err)
		}
		for _, nome := range []string{"Images", "ImagePart"} {
			if strings.Contains(string(fonte), nome) {
				t.Fatalf("%s nomeia %s: o adaptador dos turnos nao pode produzir imagens (ADR-042 §2.4, barreira 1)", f, nome)
			}
		}
	}
}
