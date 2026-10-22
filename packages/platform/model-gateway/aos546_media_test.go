package modelgateway_test

// AOS-546 — A MEDIA NO TURNO DO MODELO DO RUN (ADR-043 §2.2): um tail da 1.7.0 com um segmento
// `media` leva a imagem na mensagem `user` desse segmento, resolvida e conferida pelo adaptador.
// Sem resolvedor, com bytes que não batem ou ilegíveis, o turno falha — nunca sai sem ela.

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos546Vista é um tail da 1.7.0: o objectivo, um turno que chamou `media_view` (cujo resultado é
// a referência à imagem) e `doc_read`, e o segmento `media` a seguir aos resultados.
func aos546Vista(t *testing.T, imagem []byte) agentruntime.PromptView {
	t.Helper()
	parte := port.MediaPart{MediaType: port.ImageMediaPNG, Data: imagem}
	ref, err := agentruntime.MediaRef{Doc: "sala.png", SHA256: parte.Digest(), MediaType: parte.MediaType, Bytes: int64(len(imagem))}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	seq, err := agentruntime.NewTailSequence(agentruntime.AssemblyVersion170)
	if err != nil {
		t.Fatal(err)
	}
	segs, _ := seq.Turn(aos490Passo1, "vou ver a foto",
		[]agentruntime.CapturedToolResult{aos490Permitida("media_view", `{"doc_id":"sala.png"}`, string(ref)), aos490Permitida("doc_read", `{"doc_id":"notes"}`, "texto")})
	tail := append([]agentruntime.TailSegment{aos490Objectivo("Descreve os comodos da foto")}, segs...)
	return aos490Vista(t, agentruntime.AssemblyVersion170, "", tail)
}

func aos546Resolvedor(imagem []byte, chamadas *int) modelgateway.MediaResolver {
	return func(ref agentruntime.MediaRef) (port.MediaPart, error) {
		*chamadas++
		if ref.Doc != "sala.png" {
			return port.MediaPart{}, errors.New("documento desconhecido")
		}
		return port.MediaPart{MediaType: port.ImageMediaPNG, Data: imagem}, nil
	}
}

func TestAOS546_Nativa_AImagemVaiNaMensagemUserDepoisDosResultados(t *testing.T) {
	t.Parallel()
	img := aos530PNG()
	n := 0
	req, _, err := aos490Pedir(t, aos546Vista(t, img), modelgateway.WithProjection(modelgateway.ProjectionNative), modelgateway.WithMediaResolver(aos546Resolvedor(img, &n)))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	formas := strings.Join(aos490Formas(req.Messages), " ")
	if formas != "system user assistant[step-000001-tool-1][step-000001-tool-2] tool:step-000001-tool-1 tool:step-000001-tool-2 user" {
		t.Fatalf("formas = %s", formas)
	}
	ultima := req.Messages[len(req.Messages)-1]
	if len(ultima.Images) != 1 || string(ultima.Images[0].Data) != string(img) || !strings.HasPrefix(ultima.Content, "<media taint=untrusted media_doc=sala.png") {
		t.Fatalf("a mensagem da media nao leva a imagem com o cabecalho: %+v", ultima)
	}
	for _, m := range req.Messages[:len(req.Messages)-1] {
		if len(m.Images) > 0 {
			t.Fatalf("uma imagem saiu noutra mensagem: %s", m.Role)
		}
	}
	if n != 1 {
		t.Fatalf("o resolvedor foi chamado %d vezes", n)
	}
	if _, err := req.MarshalWire(false); err != nil {
		t.Fatalf("o pedido nao se escreve no wire: %v", err)
	}
}

func TestAOS546_TextoUnico_AImagemVaiNaUnicaMensagem(t *testing.T) {
	t.Parallel()
	img := aos530PNG()
	n := 0
	req, _, err := aos490Pedir(t, aos546Vista(t, img), modelgateway.WithMediaResolver(aos546Resolvedor(img, &n)))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Images) != 1 || n != 1 {
		t.Fatalf("texto unico: %d mensagens, imagens %d, resolucoes %d", len(req.Messages), len(req.Messages[0].Images), n)
	}
}

// FAIL-CLOSED: sem resolvedor, bytes ilegíveis, outros bytes, outro tipo — nas duas formas.
func TestAOS546_SemAImagemCertaOTurnoNaoSai(t *testing.T) {
	t.Parallel()
	img := aos530PNG()
	outra := append(aos530PNG(), 'x')
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 'j', 'f', 'i', 'f'}
	casos := map[string]struct {
		r     modelgateway.MediaResolver
		causa string
	}{
		"sem resolvedor": {nil, modelgateway.MediaCauseNoResolver},
		"ilegivel": {func(agentruntime.MediaRef) (port.MediaPart, error) {
			return port.MediaPart{}, errors.New("o ficheiro desapareceu")
		}, modelgateway.MediaCauseUnreadable},
		"outros bytes": {func(agentruntime.MediaRef) (port.MediaPart, error) {
			return port.MediaPart{MediaType: port.ImageMediaPNG, Data: outra}, nil
		}, modelgateway.MediaCauseMismatch},
		// MUTAÇÃO M1 DA REVISÃO: o ficheiro substituído com o MESMO tamanho e o mesmo tipo (a retoma
		// com a imagem trocada) só o digest apanha.
		"um byte trocado": {func(agentruntime.MediaRef) (port.MediaPart, error) {
			trocada := aos530PNG()
			trocada[len(trocada)-1] ^= 0xff
			return port.MediaPart{MediaType: port.ImageMediaPNG, Data: trocada}, nil
		}, modelgateway.MediaCauseMismatch},
		"outro tipo": {func(agentruntime.MediaRef) (port.MediaPart, error) {
			return port.MediaPart{MediaType: port.ImageMediaJPEG, Data: jpeg}, nil
		}, modelgateway.MediaCauseMismatch},
	}
	for nome, c := range casos {
		for _, nativa := range []bool{false, true} {
			var opts []modelgateway.RuntimeAdapterOption
			if c.r != nil {
				opts = append(opts, modelgateway.WithMediaResolver(c.r))
			}
			if nativa {
				opts = append(opts, modelgateway.WithProjection(modelgateway.ProjectionNative))
			}
			req, _, err := aos490Pedir(t, aos546Vista(t, img), opts...)
			var me *modelgateway.MediaUnavailableError
			if !errors.As(err, &me) || me.Cause != c.causa || !errors.Is(err, modelgateway.ErrMediaUnavailable) {
				t.Fatalf("%s (nativa=%v): err=%v, quer causa %s", nome, nativa, err, c.causa)
			}
			if len(req.Messages) != 0 {
				t.Fatalf("%s (nativa=%v): o pedido chegou ao gateway", nome, nativa)
			}
		}
	}
}

// A projecção pura recusa um tail com media: sem os bytes, o modelo veria o aviso sem a imagem.
func TestAOS546_ProjectNativeVersionSemResolvedorRecusa(t *testing.T) {
	t.Parallel()
	_, err := modelgateway.ProjectNativeVersion(modelgateway.NativeProjectionVersion130, aos546Vista(t, aos530PNG()))
	if !errors.Is(err, modelgateway.ErrMediaUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// Um segmento `media` num layout que não o tem é forma inválida, não dados.
func TestAOS546_MediaForaDaA170EFormaInvalida(t *testing.T) {
	t.Parallel()
	v := aos546Vista(t, aos530PNG())
	v.AssemblyVersion = agentruntime.AssemblyVersion160
	n := 0
	_, err := modelgateway.ProjectNativeVersionWithMedia(modelgateway.NativeProjectionVersion130, v, aos546Resolvedor(aos530PNG(), &n))
	if !errors.Is(err, modelgateway.ErrNativeProjection) || n != 0 {
		t.Fatalf("err = %v, resolucoes %d", err, n)
	}
}

// A parte genérica: a classe sai do tipo, e AttachMedia põe a parte no campo da sua classe.
func TestAOS546_MediaPartGenerica(t *testing.T) {
	t.Parallel()
	for tipo, classe := range map[string]string{port.ImageMediaPNG: port.MediaClassImage, port.ImageMediaJPEG: port.MediaClassImage, port.VideoMediaMP4: port.MediaClassVideo} {
		if c, ok := port.MediaClassOf(tipo); !ok || c != classe {
			t.Fatalf("MediaClassOf(%s) = %q, %v", tipo, c, ok)
		}
	}
	if _, ok := port.MediaClassOf("image/gif"); ok {
		t.Fatal("image/gif tem classe")
	}
	if c, _ := port.MediaClassOf(port.ImageMediaPNG); c != modelgateway.CapabilityImage {
		t.Fatal("a classe imagem nao e o nome da capacidade do perfil")
	}
	if c, _ := port.MediaClassOf(port.VideoMediaMP4); c != modelgateway.CapabilityVideo {
		t.Fatal("a classe video nao e o nome da capacidade do perfil")
	}
	var m port.Message
	if err := m.AttachMedia(port.MediaPart{MediaType: port.ImageMediaPNG, Data: aos530PNG()}); err != nil || len(m.Images) != 1 || len(m.Videos) != 0 {
		t.Fatalf("AttachMedia(png): %v, %d/%d", err, len(m.Images), len(m.Videos))
	}
	if err := m.AttachMedia(port.MediaPart{MediaType: "image/gif", Data: []byte("GIF89a")}); !errors.Is(err, port.ErrMediaPart) {
		t.Fatalf("AttachMedia(gif): %v", err)
	}
	if err := m.AttachMedia(port.MediaPart{MediaType: port.ImageMediaPNG, Data: []byte("nao e png")}); !errors.Is(err, port.ErrImagePart) {
		t.Fatalf("AttachMedia(png falso): %v", err)
	}
	if s := (port.MediaPart{MediaType: port.ImageMediaPNG, Data: aos530PNG()}).String(); strings.Contains(s, "IHDR") {
		t.Fatalf("a parte impressa mostra os bytes: %s", s)
	}
}

// O TITULAR SÓ VÊ A IMAGEM SE A SUA ROTA DECLARAR `imagem`: com o gateway real, o turno do run com
// media para uma rota sem a capacidade é recusado antes da credencial, do selo, do adaptador e do
// custo; com a capacidade, a imagem chega ao adaptador.
func TestAOS546_GatewayReal_SoUmaRotaComImagemRecebeAMediaDoRun(t *testing.T) {
	t.Parallel()
	img := aos530PNG()
	n := 0
	semImagem := modelgateway.RouteProfile{Requested: aos530Modelo, ExpectedModel: "openrouter/anthropic/claude-haiku-5.5", WireClass: modelgateway.WireOpenAIChat, Capabilities: []string{modelgateway.CapabilityTools}}
	m := aos530Compor(t, "", semImagem)
	_, err := modelgateway.NewModelClient(m.gw, aos530Modelo, modelgateway.WithMediaResolver(aos546Resolvedor(img, &n))).Call(aos546Contexto(), aos546Vista(t, img))
	if !errors.Is(err, modelgateway.ErrRouteWithoutImage) {
		t.Fatalf("rota sem imagem: err = %v", err)
	}
	m.aos530SemEfeito(t)

	comImagem := aos530Compor(t, "", aos530PerfilComImagem(aos530Modelo))
	if _, err := modelgateway.NewModelClient(comImagem.gw, aos530Modelo, modelgateway.WithMediaResolver(aos546Resolvedor(img, &n))).Call(aos546Contexto(), aos546Vista(t, img)); err != nil {
		t.Fatalf("rota com imagem: %v", err)
	}
	if comImagem.adapt.chamado != 1 || !comImagem.adapt.ultimo.HasImages() {
		t.Fatalf("a imagem nao chegou ao adaptador (chamadas %d)", comImagem.adapt.chamado)
	}
}

func aos546Contexto() context.Context {
	return agentruntime.ContextWithModelCall(context.Background(), aos530Run, "step-000002")
}
