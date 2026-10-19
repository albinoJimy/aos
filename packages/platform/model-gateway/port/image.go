package port

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// PARTES DE IMAGEM NO PEDIDO (AOS-530, ADR-042 §2.4; campo aditivo, MINOR 1.12.0).
//
// Uma mensagem `user` de um PEDIDO pode levar, além do texto, partes de imagem ([ImagePart]): o
// tipo e os bytes. Só [ChatRequest.MarshalWire] as escreve, na forma OpenAI de chat/completions —
// `content` como lista de partes, o texto primeiro e as imagens a seguir, cada uma como
// `image_url` numa data URL em base64, a forma que o banco de ensaio mediu (AOS-529,
// `qa/banco-ensaio/ocr_visao.go`). Uma mensagem sem imagens sai com os bytes de sempre.
//
// AS RESPOSTAS CONTINUAM SÓ TEXTO. Nada aqui muda a leitura de uma resposta:
// [ErrContentPartNotText] fica, e [Message.Images] nunca se lê de JSON (`json:"-"`).
//
// OS BYTES NÃO SAEM POR OUTRA VIA. [Message.Images] não se serializa por `encoding/json`
// (`json:"-"`); um [ImagePart] impresso (`%v`, `%+v`, `%#v`) ou serializado sozinho mostra só o
// tipo, o tamanho e o `sha256` ([ImagePart.String], [ImagePart.MarshalJSON]). O único sítio por
// onde os bytes chegam a um texto é o corpo do pedido ao provider.
//
// QUEM PODE MANDAR IMAGENS NÃO É DECIDIDO AQUI. A porta valida a FORMA ([ChatRequest.Normalize]);
// é o gateway que recusa uma imagem para uma rota cujo perfil não declare a capacidade `imagem`.

// Tipos de imagem aceites numa [ImagePart]. Vocabulário FECHADO, comparação exacta.
const (
	ImageMediaPNG  = "image/png"
	ImageMediaJPEG = "image/jpeg"
)

// MaxImageBytes é o tecto, em bytes, de uma parte de imagem E da soma de todas as partes de
// imagem de um pedido: 4 MiB (ADR-042 §2.4, proposta a confirmar contra o limite do fornecedor).
// Uma parte maior, ou um pedido cujas partes somem mais, é recusado antes de sair.
const MaxImageBytes = 4 << 20

// ImagePart é uma parte de imagem de uma mensagem `user` de um pedido. MediaType é
// [ImageMediaPNG] ou [ImageMediaJPEG] e tem de bater com os primeiros bytes de Data.
type ImagePart struct {
	MediaType string
	Data      []byte
}

// ErrImagePart — uma parte de imagem do pedido não passa na validação. Fail-closed: o pedido não
// sai. [*ImagePartError] casa com este erro por [errors.Is].
var ErrImagePart = errors.New("port: parte de imagem invalida no pedido")

// Causas de [ImagePartError]. Vocabulário FECHADO: nenhuma leva bytes nem o tipo declarado.
const (
	// ImageCauseMediaType — o tipo não é [ImageMediaPNG] nem [ImageMediaJPEG].
	ImageCauseMediaType = "tipo_nao_suportado"
	// ImageCauseEmpty — a parte não tem bytes.
	ImageCauseEmpty = "imagem_vazia"
	// ImageCauseTooLarge — a parte tem mais de [MaxImageBytes].
	ImageCauseTooLarge = "imagem_acima_do_tecto"
	// ImageCauseMagic — os primeiros bytes não são os do tipo declarado.
	ImageCauseMagic = "tipo_nao_bate_com_os_bytes"
	// ImageCauseRole — a parte está numa mensagem que não é `user`.
	ImageCauseRole = "imagem_fora_de_mensagem_user"
	// ImageCauseTotalTooLarge — cada parte cabe, mas a soma das partes de imagem de todas as
	// mensagens do pedido passa de [MaxImageBytes].
	ImageCauseTotalTooLarge = "imagens_acima_do_tecto"
)

// ImagePartError é o erro de uma parte de imagem recusada. Casa com [ErrImagePart].
type ImagePartError struct {
	// Cause é uma das constantes ImageCause*.
	Cause string
}

// Error implementa error.
func (e *ImagePartError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrImagePart, e.Cause)
}

// Unwrap liga o erro a [ErrImagePart].
func (e *ImagePartError) Unwrap() error { return ErrImagePart }

// Assinaturas dos primeiros bytes de cada tipo aceite.
var (
	magiaPNG  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	magiaJPEG = []byte{0xff, 0xd8, 0xff}
)

// Validate verifica a parte: tipo no vocabulário, bytes não vazios, no máximo [MaxImageBytes], e
// os primeiros bytes do tipo declarado. A ordem é a da gravidade: um tipo desconhecido não chega
// a ter os bytes lidos.
func (p ImagePart) Validate() error {
	var magia []byte
	switch p.MediaType {
	case ImageMediaPNG:
		magia = magiaPNG
	case ImageMediaJPEG:
		magia = magiaJPEG
	default:
		return &ImagePartError{Cause: ImageCauseMediaType}
	}
	switch {
	case len(p.Data) == 0:
		return &ImagePartError{Cause: ImageCauseEmpty}
	case len(p.Data) > MaxImageBytes:
		return &ImagePartError{Cause: ImageCauseTooLarge}
	case !bytes.HasPrefix(p.Data, magia):
		return &ImagePartError{Cause: ImageCauseMagic}
	}
	return nil
}

// Digest é o `sha256` dos bytes da parte, na forma `sha256:<hex>`. É o que pode ficar num
// resultado, num evento ou num span no lugar dos bytes.
func (p ImagePart) Digest() string {
	soma := sha256.Sum256(p.Data)
	return "sha256:" + hex.EncodeToString(soma[:])
}

// String mostra a parte sem os bytes: o tipo, o tamanho e o digest.
func (p ImagePart) String() string {
	return fmt.Sprintf("ImagePart{media_type=%q,bytes=%d,%s}", p.MediaType, len(p.Data), p.Digest())
}

// GoString é o [ImagePart.String] também para `%#v`.
func (p ImagePart) GoString() string { return p.String() }

// MarshalJSON serializa a parte SEM os bytes: um `encoding/json` acidental (um log estruturado,
// uma captura) escreve o tipo, o tamanho e o digest. Os bytes só vão ao provider, por
// [ChatRequest.MarshalWire], que não passa por aqui.
func (p ImagePart) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MediaType string `json:"media_type"`
		Bytes     int    `json:"bytes"`
		Digest    string `json:"sha256"`
	}{p.MediaType, len(p.Data), p.Digest()})
}

// HasImages diz se alguma mensagem do pedido leva partes de imagem.
func (r ChatRequest) HasImages() bool {
	return temImagens(r.Messages)
}

// temImagens diz se alguma mensagem leva partes de imagem.
func temImagens(msgs []Message) bool {
	for i := range msgs {
		if len(msgs[i].Images) > 0 {
			return true
		}
	}
	return false
}

// validarImagens verifica as partes de imagem de uma mensagem: só numa mensagem `user`, e cada
// parte válida ([ImagePart.Validate]).
func validarImagens(m Message) error {
	if len(m.Images) == 0 {
		return nil
	}
	if m.Role != RoleUser {
		return &ImagePartError{Cause: ImageCauseRole}
	}
	for _, p := range m.Images {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// tectoDoPedido recusa um pedido cujas partes de imagem, somadas por todas as mensagens, passem de
// [MaxImageBytes]. Sem o tecto agregado, o tecto por parte deixava passar um pedido de N partes de
// 4 MiB cada.
func tectoDoPedido(msgs []Message) error {
	total := 0
	for i := range msgs {
		for _, p := range msgs[i].Images {
			total += len(p.Data)
			if total > MaxImageBytes {
				return &ImagePartError{Cause: ImageCauseTotalTooLarge}
			}
		}
	}
	return nil
}

// Partes de `content` de uma mensagem com imagens, na forma OpenAI de chat/completions.
type (
	parteDeTextoNoPedido struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	urlDaImagem struct {
		URL string `json:"url"`
	}
	parteDeImagemNoPedido struct {
		Type     string      `json:"type"`
		ImageURL urlDaImagem `json:"image_url"`
	}
)

// mensagemComPartes é uma mensagem com `content` em lista de partes. Os campos são os de
// [Message], pela mesma ordem; o raciocínio já foi retirado ([semRaciocinio]) e não sai.
type mensagemComPartes struct {
	Role       Role       `json:"role"`
	Content    []any      `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// mensagemNoWire serializa uma mensagem de um pedido que não leva estado para sair. Sem imagens é
// `json.Marshal(m)`, os bytes de sempre. Com imagens, `content` é a lista de partes: o texto (se
// houver) e cada imagem como `image_url` numa data URL em base64. As imagens voltam a ser
// validadas aqui: um pedido que não passou por [ChatRequest.Normalize] não escreve uma imagem
// inválida no wire.
//
// Com uma parte de vídeo (AOS-535) é o mesmo: o texto primeiro e o vídeo a seguir, como
// `video_url` ([parteDeVideo]). O vídeo e as regras do pedido inteiro (um vídeo, nunca com imagens)
// já foram conferidos por [ChatRequest.MarshalWire], o único chamador, antes de chegar aqui.
func mensagemNoWire(m Message) ([]byte, error) {
	if len(m.Images) == 0 && len(m.Videos) == 0 {
		return json.Marshal(m)
	}
	if err := validarImagens(m); err != nil {
		return nil, err
	}
	partes := make([]any, 0, len(m.Images)+len(m.Videos)+1)
	if m.Content != "" {
		partes = append(partes, parteDeTextoNoPedido{Type: "text", Text: m.Content})
	}
	for _, p := range m.Images {
		partes = append(partes, parteDeImagemNoPedido{
			Type:     "image_url",
			ImageURL: urlDaImagem{URL: "data:" + p.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)},
		})
	}
	for _, p := range m.Videos {
		partes = append(partes, parteDeVideo(p))
	}
	return json.Marshal(mensagemComPartes{
		Role:       m.Role,
		Content:    partes,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		ToolCalls:  m.ToolCalls,
	})
}
