package port_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-530 — PARTES DE IMAGEM NO PEDIDO (porta 1.12.0, ADR-042 §2.4). A porta valida a forma e
// escreve-as no wire; as respostas continuam só texto; e os bytes não saem por mais via nenhuma.

// aos530PNG é um PNG mínimo para o teste: a assinatura e um corpo com bytes que não aparecem por
// acaso em nenhum texto do pedido.
func aos530PNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("IHDR-aos530-corpo-da-imagem-\x00\x01\x02\xfe")...)
}

func aos530JPEG() []byte {
	return append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("JFIF-aos530-corpo-jpeg-\x7f\x80")...)
}

func aos530Pedido(msgs ...port.Message) port.ChatRequest {
	return port.ChatRequest{Model: "ocr-visao", Messages: msgs}
}

// Cada recusa da forma, com a sua causa, antes de o pedido seguir.
func TestAOS530_Normalize_RecusaCadaForma(t *testing.T) {
	t.Parallel()
	png := aos530PNG()
	grande := append(append([]byte(nil), png...), make([]byte, port.MaxImageBytes)...)
	casos := []struct {
		nome  string
		msg   port.Message
		causa string
	}{
		{"tipo fora do vocabulario", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: "image/gif", Data: png}}}, port.ImageCauseMediaType},
		{"tipo com caixa diferente", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: "image/PNG", Data: png}}}, port.ImageCauseMediaType},
		{"tipo vazio", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{Data: png}}}, port.ImageCauseMediaType},
		{"bytes vazios", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG}}}, port.ImageCauseEmpty},
		{"acima de 4 MiB", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: grande}}}, port.ImageCauseTooLarge},
		{"png declarado, jpeg nos bytes", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: aos530JPEG()}}}, port.ImageCauseMagic},
		{"jpeg declarado, png nos bytes", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaJPEG, Data: png}}}, port.ImageCauseMagic},
		{"assinatura cortada", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png[:4]}}}, port.ImageCauseMagic},
		{"segunda parte invalida", port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png}, {MediaType: "text/plain", Data: png}}}, port.ImageCauseMediaType},
		{"mensagem system", port.Message{Role: port.RoleSystem, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png}}}, port.ImageCauseRole},
		{"mensagem assistant", port.Message{Role: port.RoleAssistant, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png}}}, port.ImageCauseRole},
		{"mensagem tool", port.Message{Role: port.RoleTool, ToolCallID: "c1", Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png}}}, port.ImageCauseRole},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			_, err := aos530Pedido(port.Message{Role: port.RoleSystem, Content: "s"}, c.msg).Normalize()
			var ie *port.ImagePartError
			if !errors.Is(err, port.ErrImagePart) || !errors.As(err, &ie) || ie.Cause != c.causa {
				t.Fatalf("Normalize = %v; quero ErrImagePart com causa %s", err, c.causa)
			}
			// A mensagem de erro é vocabulário fechado: não repete o tipo declarado nem os bytes.
			if strings.Contains(err.Error(), "gif") || strings.Contains(err.Error(), "IHDR") {
				t.Fatalf("o erro repete conteudo do pedido: %v", err)
			}
			// O wire, chamado sem Normalize, também não escreve a parte inválida.
			if _, err := aos530Pedido(c.msg).MarshalWire(false); !errors.Is(err, port.ErrImagePart) {
				t.Fatalf("MarshalWire escreveu uma parte invalida: %v", err)
			}
		})
	}
}

// Os limites aceites: exactamente 4 MiB, PNG e JPEG, várias partes.
func TestAOS530_Normalize_AceitaOsLimites(t *testing.T) {
	t.Parallel()
	png := aos530PNG()
	noTecto := append(append([]byte(nil), png...), make([]byte, port.MaxImageBytes-len(png))...)
	req := aos530Pedido(port.Message{Role: port.RoleUser, Content: "le", Images: []port.ImagePart{
		{MediaType: port.ImageMediaPNG, Data: noTecto},
		{MediaType: port.ImageMediaJPEG, Data: aos530JPEG()},
	}})
	out, err := req.Normalize()
	if err != nil {
		t.Fatalf("Normalize recusou partes validas: %v", err)
	}
	if !out.HasImages() || len(out.Messages[0].Images) != 2 {
		t.Fatalf("Normalize perdeu as imagens: %d", len(out.Messages[0].Images))
	}
	if (port.ChatRequest{Messages: []port.Message{{Role: port.RoleUser}}}).HasImages() {
		t.Fatal("HasImages num pedido sem imagens")
	}
}

// SEM IMAGENS, OS BYTES DE SEMPRE. O literal foi escrito à mão na forma de antes do AOS-530; uma
// fatia de imagens vazia (não nil) também não muda nada.
func TestAOS530_MarshalWire_SemImagem_BytesDeSempre(t *testing.T) {
	t.Parallel()
	const quero = `{"model":"gpt-4o","messages":[{"role":"system","content":"s"},{"role":"user","content":"ola"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"doc_read","arguments":"{}"}}]},` +
		`{"role":"tool","content":"r","tool_call_id":"c1"}],"max_tokens":7}`
	msgs := func(imgs []port.ImagePart) []port.Message {
		return []port.Message{
			{Role: port.RoleSystem, Content: "s"},
			{Role: port.RoleUser, Content: "ola", Images: imgs},
			{Role: port.RoleAssistant, ToolCalls: []port.ToolCall{{ID: "c1", Type: "function", Function: port.FunctionCall{Name: "doc_read", Arguments: "{}"}}}},
			{Role: port.RoleTool, Content: "r", ToolCallID: "c1"},
		}
	}
	for _, imgs := range [][]port.ImagePart{nil, {}} {
		wire, err := port.ChatRequest{Model: "gpt-4o", Messages: msgs(imgs), MaxTokens: 7}.MarshalWire(false)
		if err != nil {
			t.Fatalf("MarshalWire: %v", err)
		}
		if string(wire) != quero {
			t.Fatalf("o pedido sem imagens mudou:\n veio:  %s\n quero: %s", wire, quero)
		}
	}
}

// COM IMAGENS: só a mensagem que as leva muda de forma — `content` em partes, o texto primeiro e
// cada imagem numa data URL com os bytes exactos. As outras mensagens e o resto do pedido ficam.
func TestAOS530_MarshalWire_ComImagem_PartesNaFormaMedida(t *testing.T) {
	t.Parallel()
	png, jpeg := aos530PNG(), aos530JPEG()
	req := port.ChatRequest{Model: "ocr-visao", MaxTokens: 4096, Messages: []port.Message{
		{Role: port.RoleSystem, Content: "instrucao"},
		{Role: port.RoleUser, Content: "transcreve", Name: "u", Images: []port.ImagePart{
			{MediaType: port.ImageMediaPNG, Data: png},
			{MediaType: port.ImageMediaJPEG, Data: jpeg},
		}},
	}}
	for _, stream := range []bool{false, true} {
		wire, err := req.MarshalWire(stream)
		if err != nil {
			t.Fatalf("MarshalWire: %v", err)
		}
		quero := `{"model":"ocr-visao","messages":[{"role":"system","content":"instrucao"},` +
			`{"role":"user","content":[{"type":"text","text":"transcreve"},` +
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `"}},` +
			`{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString(jpeg) + `"}}],"name":"u"}]`
		if stream {
			quero += `,"stream":true`
		}
		quero += `,"max_tokens":4096}`
		if string(wire) != quero {
			t.Fatalf("stream=%v:\n veio:  %s\n quero: %s", stream, wire, quero)
		}
		// E os bytes, lidos de volta da data URL, são os exactos.
		var corpo struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(wire, &corpo); err != nil {
			t.Fatalf("o wire nao e JSON: %v", err)
		}
		var partes []struct {
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(corpo.Messages[1].Content, &partes); err != nil || len(partes) != 3 {
			t.Fatalf("partes: %v (%d)", err, len(partes))
		}
		_, dados, _ := strings.Cut(partes[1].ImageURL.URL, ";base64,")
		if cru, err := base64.StdEncoding.DecodeString(dados); err != nil || !bytes.Equal(cru, png) {
			t.Fatalf("os bytes da imagem nao chegaram exactos ao wire")
		}
	}
	// Sem texto, só as partes de imagem (uma parte de texto vazia não sai).
	wire, err := aos530Pedido(port.Message{Role: port.RoleUser, Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png}}}).MarshalWire(false)
	if err != nil {
		t.Fatalf("MarshalWire: %v", err)
	}
	if !strings.Contains(string(wire), `"content":[{"type":"image_url"`) {
		t.Fatalf("sem texto, a primeira parte tinha de ser a imagem: %s", wire)
	}
	// O raciocínio continua a não sair, também numa mensagem com imagens.
	comRaciocinio := aos530Pedido(port.Message{Role: port.RoleUser, ReasoningContent: "segredo-do-raciocinio", Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: png}}})
	if wire, _ := comRaciocinio.MarshalWire(false); strings.Contains(string(wire), "segredo-do-raciocinio") {
		t.Fatalf("o raciocinio saiu numa mensagem com imagens: %s", wire)
	}
}

// Uma imagem numa mensagem com estado marcado para sair não se escreve: nunca se descarta em
// silêncio.
func TestAOS530_MarshalWire_ImagemComEstado_Recusa(t *testing.T) {
	t.Parallel()
	req := aos530Pedido(port.Message{Role: port.RoleUser, State: &port.MessageState{Return: true},
		Images: []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: aos530PNG()}}})
	if _, err := req.MarshalWire(false); !errors.Is(err, port.ErrImagePart) {
		t.Fatalf("MarshalWire = %v; quero ErrImagePart", err)
	}
}

// AS RESPOSTAS CONTINUAM SÓ TEXTO, e uma mensagem lida de JSON nunca traz imagens.
func TestAOS530_Resposta_ContinuaSoTexto(t *testing.T) {
	t.Parallel()
	corpo := `{"choices":[{"message":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0K"}}]}}]}`
	if _, err := port.UnmarshalChatResponse([]byte(corpo)); !errors.Is(err, port.ErrContentPartNotText) {
		t.Fatalf("uma resposta com uma parte de imagem tinha de ser recusada: %v", err)
	}
	var m port.Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"x","Images":[{"MediaType":"image/png","Data":"iVBORw0K"}],"images":[1]}`), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.Images != nil {
		t.Fatalf("uma mensagem lida de JSON trouxe imagens: %v", m.Images)
	}
}

// OS BYTES NÃO SAEM POR OUTRA VIA: nem pelo `encoding/json` da mensagem, do pedido ou da parte,
// nem por `fmt` em nenhum verbo. Só o tipo, o tamanho e o digest.
func TestAOS530_BytesSoNoWire(t *testing.T) {
	t.Parallel()
	png := aos530PNG()
	parte := port.ImagePart{MediaType: port.ImageMediaPNG, Data: png}
	msg := port.Message{Role: port.RoleUser, Content: "t", Images: []port.ImagePart{parte}}
	req := aos530Pedido(msg)
	b64 := base64.StdEncoding.EncodeToString(png)
	corpoCru := string(png[8:])

	var textos []string
	for _, v := range []any{msg, req, parte, []port.ImagePart{parte}} {
		cru, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal(%T): %v", v, err)
		}
		textos = append(textos, string(cru))
		for _, verbo := range []string{"%v", "%+v", "%#v", "%s"} {
			textos = append(textos, fmt.Sprintf(verbo, v))
		}
	}
	for _, s := range textos {
		if strings.Contains(s, b64) || strings.Contains(s, corpoCru) || strings.Contains(s, b64[:16]) {
			t.Fatalf("os bytes da imagem sairam fora do wire: %s", s)
		}
	}
	if got, quero := parte.String(), `ImagePart{media_type="image/png",bytes=`+fmt.Sprint(len(png))+`,`+parte.Digest()+`}`; got != quero {
		t.Fatalf("String = %s; quero %s", got, quero)
	}
	cru, _ := json.Marshal(parte)
	if !strings.Contains(string(cru), `"sha256":"`+parte.Digest()+`"`) || !strings.HasPrefix(parte.Digest(), "sha256:") || len(parte.Digest()) != len("sha256:")+64 {
		t.Fatalf("a parte serializada tinha de levar o digest: %s", cru)
	}
	// O pedido com a imagem é o único sítio onde os bytes aparecem.
	wire, err := req.MarshalWire(false)
	if err != nil || !strings.Contains(string(wire), b64) {
		t.Fatalf("o wire tinha de levar os bytes: %v", err)
	}
}

// A tool de origem é metadado de plataforma: não vai no wire.
func TestAOS530_ToolDeOrigem_ForaDoWire(t *testing.T) {
	t.Parallel()
	base := aos530Pedido(port.Message{Role: port.RoleUser, Content: "x"})
	comOrigem := base
	comOrigem.OriginTool = "ocr_read"
	a, errA := base.MarshalWire(false)
	b, errB := comOrigem.MarshalWire(false)
	if errA != nil || errB != nil || string(a) != string(b) {
		t.Fatalf("a tool de origem mudou o wire:\n%s\n%s", a, b)
	}
	if cru, _ := json.Marshal(comOrigem); strings.Contains(string(cru), "ocr_read") {
		t.Fatalf("a tool de origem saiu no JSON do pedido: %s", cru)
	}
}
