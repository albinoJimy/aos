package modelgateway_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-535 — A CAPACIDADE `video` DO PERFIL DA ROTA. Um pedido com a parte de vídeo só sai para uma
// rota RESOLVIDA cujo perfil declare `video`; de outro modo, e sempre em streaming, é recusado
// antes da credencial, do selo de atribuição, do adaptador e do custo. A montagem é a do AOS-530
// (aos530Compor): contabilidade de custo, atribuição selada, tracer que grava.

// aos535Corpo é o corpo da `mdat`: bytes que não aparecem por acaso em nenhum texto.
var aos535Corpo = []byte("MDAT-aos535-gw-corpo-do-video-\x00\x01\xfe")

func aos535Caixa(tipo string, corpo []byte) []byte {
	b := make([]byte, 8, 8+len(corpo))
	binary.BigEndian.PutUint32(b, uint32(8+len(corpo))) // #nosec G115 -- corpos de teste pequenos
	copy(b[4:], tipo)
	return append(b, corpo...)
}

// aos535MP4 é um MP4 mínimo com o índice à cabeça: ftyp (isom), moov, mdat.
func aos535MP4() []byte {
	return bytes.Join([][]byte{
		aos535Caixa("ftyp", []byte("isom\x00\x00\x02\x00isomiso2avc1mp41")),
		aos535Caixa("moov", aos535Caixa("mvhd", make([]byte, 20))),
		aos535Caixa("mdat", aos535Corpo),
	}, nil)
}

func aos535PerfilComVideo(requested string) modelgateway.RouteProfile {
	return modelgateway.RouteProfile{Requested: requested, ExpectedModel: "openrouter/google/modelo-de-video",
		WireClass: modelgateway.WireOpenAIChat, Capabilities: []string{modelgateway.CapabilityVideo}}
}

func aos535PedidoComVideo(modelo string) port.ChatRequest {
	return port.ChatRequest{Model: modelo, RunID: aos530Run, StepID: "step-000001", Principal: "tok", Board: "board-eu",
		Messages: []port.Message{
			{Role: port.RoleSystem, Content: "descreve o video"},
			{Role: port.RoleUser, Content: "video:", Videos: []port.VideoPart{{MediaType: port.VideoMediaMP4, Data: aos535MP4()}}},
		}}
}

func aos535QueroRecusa(t *testing.T, err error, causa string) {
	t.Helper()
	var re *modelgateway.RouteWithoutVideoError
	if !errors.Is(err, modelgateway.ErrRouteWithoutVideo) || !errors.As(err, &re) || re.Cause != causa {
		t.Fatalf("erro = %v; quero ErrRouteWithoutVideo com causa %s", err, causa)
	}
	// Não é falha do modelo: o disjuntor do candidato não a conta.
	if c := modelgateway.ClassifyTurnFailure(err); c != modelgateway.TurnFailurePinnedProfile || !modelgateway.TurnFailureNotOfTheModel(c) {
		t.Fatalf("a recusa por capacidade foi classificada %q; quero %q (nao e do modelo)", c, modelgateway.TurnFailurePinnedProfile)
	}
}

// A ROTA DE HOJE NÃO VÊ VÍDEO: o perfil da tabela em código não declara `video`. E uma rota que
// declara só `imagem` também não.
func TestAOS535_Gateway_RotaSemVideo_RecusaAntesDeSair(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "")
	_, err := m.gw.Chat(context.Background(), aos535PedidoComVideo("gpt-4o"))
	aos535QueroRecusa(t, err, modelgateway.VideoCauseProfileWithout)
	m.aos530SemEfeito(t)
	spans := m.tracer.SpansByOperation("chat")
	if len(spans) != 1 || spans[0].Attributes[agentruntime.AttrErrorType] != "route_without_video" {
		t.Fatalf("o span chat tinha de levar error.type=route_without_video: %+v", spans)
	}
	// Uma rota com `imagem` e sem `video` recusa o vídeo: as capacidades não se confundem.
	i := aos530Compor(t, "", aos530PerfilComImagem(aos530Modelo))
	_, err = i.gw.Chat(context.Background(), aos535PedidoComVideo(aos530Modelo))
	aos535QueroRecusa(t, err, modelgateway.VideoCauseProfileWithout)
	i.aos530SemEfeito(t)
	// E uma rota com `video` e sem `imagem` recusa a imagem.
	v := aos530Compor(t, "", aos535PerfilComVideo(aos530Modelo))
	if _, err := v.gw.Chat(context.Background(), aos530PedidoComImagem(aos530Modelo)); !errors.Is(err, modelgateway.ErrRouteWithoutImage) {
		t.Fatalf("uma rota so com video aceitou uma imagem: %v", err)
	}
	v.aos530SemEfeito(t)
	// O mesmo pedido sem o vídeo passa: a recusa é do vídeo, não da rota.
	semVideo := aos535PedidoComVideo("gpt-4o")
	semVideo.Messages[1].Videos = nil
	if _, err := m.gw.Chat(context.Background(), semVideo); errors.Is(err, modelgateway.ErrRouteWithoutVideo) {
		t.Fatalf("um pedido sem video foi recusado pela capacidade: %v", err)
	}
	if m.adapt.chamado != 1 || m.creds.buscas != 1 || len(m.selos.Partitions()) == 0 {
		t.Fatalf("o pedido sem video tinha de sair (controlo da montagem); chamado=%d buscas=%d", m.adapt.chamado, m.creds.buscas)
	}
}

// Uma rota sem perfil não declara nada: recusa, com a sua causa.
func TestAOS535_Gateway_RotaSemPerfil_Recusa(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "")
	_, err := m.gw.Chat(context.Background(), aos535PedidoComVideo("modelo-sem-perfil"))
	aos535QueroRecusa(t, err, modelgateway.VideoCauseNoProfile)
	m.aos530SemEfeito(t)
}

// O RECUO: o pedido nomeia um alias com `video`, o roteamento resolve-o para uma rota sem ela (ou
// sem perfil). A rota que conta é a resolvida.
func TestAOS535_Gateway_RecuoSemVideo_Recusa(t *testing.T) {
	t.Parallel()
	m := aos530Compor(t, "gpt-4o", aos535PerfilComVideo(aos530Modelo))
	_, err := m.gw.Chat(context.Background(), aos535PedidoComVideo(aos530Modelo))
	aos535QueroRecusa(t, err, modelgateway.VideoCauseProfileWithout)
	m.aos530SemEfeito(t)
	n := aos530Compor(t, "modelo-sem-perfil", aos535PerfilComVideo(aos530Modelo))
	_, err = n.gw.Chat(context.Background(), aos535PedidoComVideo(aos530Modelo))
	aos535QueroRecusa(t, err, modelgateway.VideoCauseNoProfile)
	n.aos530SemEfeito(t)
}

// O STREAMING FICA DE FORA: mesmo numa rota que declara `video`, e também depois de um recuo.
func TestAOS535_Gateway_StreamingComVideo_Recusa(t *testing.T) {
	t.Parallel()
	for _, recuo := range []string{"", "gpt-4o"} {
		m := aos530Compor(t, recuo, aos535PerfilComVideo(aos530Modelo))
		_, err := m.gw.ChatStream(context.Background(), aos535PedidoComVideo(aos530Modelo))
		aos535QueroRecusa(t, err, modelgateway.VideoCauseStreaming)
		m.aos530SemEfeito(t)
	}
	// O mesmo pedido por Chat passa: a recusa é do streaming.
	m := aos530Compor(t, "", aos535PerfilComVideo(aos530Modelo))
	if _, err := m.gw.Chat(context.Background(), aos535PedidoComVideo(aos530Modelo)); err != nil {
		t.Fatalf("Chat: %v", err)
	}
}

// O CAMINHO QUE PASSA: uma rota resolvida com `video` recebe o vídeo tal como foi pedido, a chamada
// é contabilizada, e os bytes não estão em nenhum span nem em nenhum selo.
func TestAOS535_Gateway_RotaComVideo_Passa(t *testing.T) {
	t.Parallel()
	for _, recuo := range []string{"", aos530Modelo} {
		m := aos530Compor(t, recuo, aos535PerfilComVideo(aos530Modelo))
		req := aos535PedidoComVideo(aos530Modelo)
		req.OriginTool = "video_read"
		resp, err := m.gw.Chat(context.Background(), req)
		if err != nil {
			t.Fatalf("recuo=%q: Chat: %v", recuo, err)
		}
		if resp.Choices[0].Message.Content != "texto lido" || m.adapt.chamado != 1 {
			t.Fatalf("recuo=%q: a chamada nao chegou ao adaptador (chamado=%d)", recuo, m.adapt.chamado)
		}
		vids := m.adapt.ultimo.Messages[1].Videos
		if len(vids) != 1 || !bytes.Equal(vids[0].Data, aos535MP4()) {
			t.Fatalf("recuo=%q: o adaptador nao recebeu o video exacto", recuo)
		}
		if runs, _ := m.custo.RetainedKeys(); runs != 1 || resp.Usage.CostMicroUSD <= 0 {
			t.Fatalf("recuo=%q: a chamada com video tinha de ser contabilizada", recuo)
		}
		aos535BytesFora(t, m)
	}
}

// aos535BytesFora confere que os bytes do vídeo não estão em nenhum span nem em nenhum selo.
func aos535BytesFora(t *testing.T, m *aos530Montagem) {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(aos535MP4())
	b64Corpo := base64.StdEncoding.EncodeToString(aos535Corpo)
	var textos []string
	for _, s := range m.tracer.Spans() {
		textos = append(textos, fmt.Sprintf("%v", s.Attributes))
	}
	for _, r := range aos530Selos(t, m) {
		cru, _ := json.Marshal(r)
		textos = append(textos, string(cru), fmt.Sprintf("%+v", r))
	}
	if len(textos) < 2 {
		t.Fatal("nada gravado: a verificacao nao verificou nada")
	}
	for _, s := range textos {
		if strings.Contains(s, b64[:12]) || strings.Contains(s, b64Corpo[:12]) || strings.Contains(s, string(aos535Corpo)) {
			t.Fatalf("os bytes do video estao num span ou num selo: %s", s)
		}
	}
}

// Uma parte de vídeo inválida é recusada pela porta antes de tudo, com o erro da porta, e não é
// falha do modelo.
func TestAOS535_Gateway_VideoInvalido_RecusaDaPorta(t *testing.T) {
	t.Parallel()
	for nome, mexer := range map[string]func(*port.ChatRequest){
		"moov depois da mdat": func(r *port.ChatRequest) {
			mp4 := aos535MP4()
			ftyp, moov := mp4[:32], mp4[32:68]
			r.Messages[1].Videos[0].Data = bytes.Join([][]byte{ftyp, aos535Caixa("mdat", aos535Corpo), moov}, nil)
		},
		"dois videos": func(r *port.ChatRequest) {
			r.Messages[1].Videos = append(r.Messages[1].Videos, r.Messages[1].Videos[0])
		},
		"com imagem": func(r *port.ChatRequest) {
			r.Messages[1].Images = []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: aos530PNG()}}
		},
	} {
		m := aos530Compor(t, "", aos535PerfilComVideo(aos530Modelo))
		req := aos535PedidoComVideo(aos530Modelo)
		mexer(&req)
		for via, err := range map[string]error{"Chat": func() error { _, e := m.gw.Chat(context.Background(), req); return e }(),
			"ChatStream": func() error { _, e := m.gw.ChatStream(context.Background(), req); return e }()} {
			if !errors.Is(err, port.ErrVideoPart) {
				t.Fatalf("%s: %s = %v; quero ErrVideoPart", nome, via, err)
			}
			if c := modelgateway.ClassifyTurnFailure(err); c != modelgateway.TurnFailureGatewayDenied {
				t.Fatalf("%s: classificado %q; quero %q", nome, c, modelgateway.TurnFailureGatewayDenied)
			}
		}
		m.aos530SemEfeito(t)
	}
}

// O PERFIL: `video` é declarável (sozinha ou com `imagem`/`tools`, em JSON), as variantes não, e
// nenhum perfil em código a declara — os digests de hoje não mudam.
func TestAOS535_Perfil_CapacidadeVideo(t *testing.T) {
	t.Parallel()
	for _, caps := range []string{`["video"]`, `["video","imagem"]`, `["tools","video"]`} {
		p, err := modelgateway.ParseRouteProfile([]byte(`{"requested":"video-visao","expected_model":"openrouter/google/modelo-de-video","wire_class":"openai-chat-completions","capabilities":` + caps + `}`))
		if err != nil {
			t.Fatalf("capabilities %s: %v", caps, err)
		}
		if p.Validate() != nil || p.Digest() == "" {
			t.Fatalf("capabilities %s: perfil invalido ou sem digest", caps)
		}
	}
	for _, caps := range []string{`["Video"]`, `["vídeo"]`, `["videos"]`, `["video/mp4"]`} {
		if _, err := modelgateway.ParseRouteProfile([]byte(`{"requested":"x","expected_model":"y","wire_class":"openai-chat-completions","capabilities":` + caps + `}`)); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Fatalf("capabilities %s tinha de ser recusada: %v", caps, err)
		}
	}
	for _, p := range modelgateway.RouteProfiles() {
		for _, c := range p.Capabilities {
			if c == modelgateway.CapabilityVideo {
				t.Fatalf("o perfil em codigo %q declara video: so um registo assinado o pode fazer", p.Requested)
			}
		}
		if got := p.Digest(); got != aos513Digests[p.Requested] {
			t.Fatalf("o digest do perfil %q mudou: %s", p.Requested, got)
		}
	}
}

// PELO ADAPTADOR HTTP REAL: o vídeo chega ao provider falso como `video_url` com os bytes exactos;
// sem a capacidade, ou em streaming, o provider conta zero pedidos; e um erro do provider não ecoa
// o vídeo.
func TestAOS535_Gateway_AdaptadorHTTP(t *testing.T) {
	t.Parallel()
	compor := func(p *aos530Provider, candidatos ...modelgateway.RouteProfile) *modelgateway.Gateway {
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		set, err := modelgateway.NewRouteProfileSet(candidatos...)
		if err != nil {
			t.Fatalf("NewRouteProfileSet: %v", err)
		}
		creds := adapters.NewStaticCredentialSource()
		creds.Set("openai", aos530Regiao, "sk-teste-aos535")
		return modelgateway.New(adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client()),
			modelgateway.WithCredentialSource(creds), modelgateway.WithDefaultRegion(aos530Regiao), modelgateway.WithRouteProfiles(set))
	}
	ok := `{"id":"c","object":"chat.completion","model":"video-visao","choices":[{"index":0,"message":{"role":"assistant","content":"visto"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}`
	b64 := base64.StdEncoding.EncodeToString(aos535MP4())

	// Sem `video`: zero pedidos.
	semCap := &aos530Provider{status: http.StatusOK, corpo: ok}
	if _, err := compor(semCap, aos530PerfilComImagem("video-visao")).Chat(context.Background(), aos535PedidoComVideo("video-visao")); !errors.Is(err, modelgateway.ErrRouteWithoutVideo) {
		t.Fatalf("Chat = %v; quero ErrRouteWithoutVideo", err)
	}
	// Em streaming, com `video`: zero pedidos.
	stream := &aos530Provider{status: http.StatusOK, corpo: ok}
	if _, err := compor(stream, aos535PerfilComVideo("video-visao")).ChatStream(context.Background(), aos535PedidoComVideo("video-visao")); !errors.Is(err, modelgateway.ErrRouteWithoutVideo) {
		t.Fatalf("ChatStream = %v; quero ErrRouteWithoutVideo", err)
	}
	if len(semCap.corpos) != 0 || len(stream.corpos) != 0 {
		t.Fatalf("o provider recebeu pedidos recusados: %d e %d", len(semCap.corpos), len(stream.corpos))
	}

	// Com `video`: a data URL com os bytes exactos, na forma que o AOS-534 mediu.
	comCap := &aos530Provider{status: http.StatusOK, corpo: ok}
	if _, err := compor(comCap, aos535PerfilComVideo("video-visao")).Chat(context.Background(), aos535PedidoComVideo("video-visao")); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(comCap.corpos) != 1 {
		t.Fatalf("o provider recebeu %d pedidos", len(comCap.corpos))
	}
	quero := `{"role":"user","content":[{"type":"text","text":"video:"},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + b64 + `"}}]}`
	if !strings.Contains(string(comCap.corpos[0]), quero) {
		t.Fatalf("o corpo nao leva o video na forma medida:\n%s", comCap.corpos[0])
	}
	var corpo struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	var partes []struct {
		VideoURL struct {
			URL string `json:"url"`
		} `json:"video_url"`
	}
	if json.Unmarshal(comCap.corpos[0], &corpo) != nil || json.Unmarshal(corpo.Messages[1].Content, &partes) != nil || len(partes) != 2 {
		t.Fatalf("o corpo nao se le: %s", comCap.corpos[0])
	}
	if cru, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(partes[1].VideoURL.URL, "data:video/mp4;base64,")); err != nil || !bytes.Equal(cru, aos535MP4()) {
		t.Fatal("os bytes do video nao chegaram exactos ao provider")
	}

	// Um 400 que ecoe o pedido não sobe com o corpo.
	eco := &aos530Provider{status: http.StatusBadRequest, corpo: `{"error":{"message":"video recusado: ` + b64 + `"}}`}
	_, err := compor(eco, aos535PerfilComVideo("video-visao")).Chat(context.Background(), aos535PedidoComVideo("video-visao"))
	if err == nil || strings.Contains(err.Error(), b64[:12]) || strings.Contains(err.Error(), "video recusado") {
		t.Fatalf("o erro do provider subiu com o corpo de um pedido com video: %v", err)
	}
	// Controlo: o mesmo 400 num pedido sem vídeo sobe com o corpo — é o vídeo que o corta.
	semVideo := aos535PedidoComVideo("video-visao")
	semVideo.Messages[1].Videos = nil
	_, err = compor(eco, aos535PerfilComVideo("video-visao")).Chat(context.Background(), semVideo)
	if err == nil || !strings.Contains(err.Error(), "video recusado") {
		t.Fatalf("controlo: sem video o corpo do erro tinha de subir (senao o teste acima nao prova nada): %v", err)
	}
}

// O ADAPTADOR RT→GW DOS TURNOS NÃO PRODUZ VÍDEO. Prova dinâmica — o pedido que chega ao gateway,
// em texto único e em cada versão publicada da projecção nativa — e prova pela fonte.
func TestAOS535_AdaptadorDoRuntime_NuncaProduzVideo(t *testing.T) {
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
		if len(req.Messages) == 0 || req.HasVideos() {
			t.Fatalf("caso %d: o turno do executor levou uma parte de video (ou nenhuma mensagem)", i)
		}
	}
	for _, f := range []string{"runtime_adapter.go", "projection.go", "projection_quase_cabecalhos.go"} {
		fonte, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", f, err)
		}
		for _, nome := range []string{"Videos", "VideoPart", "VideoMedia"} {
			if strings.Contains(string(fonte), nome) {
				t.Fatalf("%s nomeia %s: o adaptador dos turnos nao pode produzir video", f, nome)
			}
		}
	}
}
