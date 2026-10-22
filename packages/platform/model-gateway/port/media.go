package port

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// A PARTE DE MEDIA GENÉRICA (AOS-546, ADR-043 §2.2; aditivo, MINOR 1.14.0).
//
// Uma [MediaPart] é o tipo e os bytes de um media de ENTRADA, qualquer que seja a modalidade. A
// modalidade é uma CLASSE, derivada do tipo por uma tabela única ([MediaClassOf]): `imagem` para
// os tipos de [ImagePart], `video` para o de [VideoPart]. A classe é também o nome da capacidade
// que o perfil da rota tem de declarar para a aceitar (`imagem`, `video` — as mesmas constantes
// do gateway). Uma modalidade nova é uma linha na tabela e a forma do wire dessa classe, e não um
// tipo novo em todos os chamadores.
//
// [ImagePart] e [VideoPart] passam a ser os casos desta parte: [Message.AttachMedia] põe a parte
// no campo da sua classe, onde as regras e o wire de sempre se aplicam — nada muda nos bytes de um
// pedido que já se construía antes.
//
// OS BYTES NÃO SAEM POR OUTRA VIA, pela regra das outras partes: impressa ou serializada sozinha,
// uma [MediaPart] mostra só o tipo, o tamanho e o digest.

// Classes de media. São os nomes das capacidades do perfil da rota que as aceitam.
const (
	MediaClassImage = "imagem"
	MediaClassVideo = "video"
)

// classesDeMedia é a tabela ÚNICA tipo → classe. Vocabulário FECHADO, comparação exacta.
var classesDeMedia = map[string]string{
	ImageMediaPNG:  MediaClassImage,
	ImageMediaJPEG: MediaClassImage,
	VideoMediaMP4:  MediaClassVideo,
}

// MediaClassOf devolve a classe do tipo de media dado, e false se o tipo não for aceite.
func MediaClassOf(mediaType string) (string, bool) {
	c, ok := classesDeMedia[mediaType]
	return c, ok
}

// MediaPart é uma parte de media de entrada, de qualquer classe.
type MediaPart struct {
	MediaType string
	Data      []byte
}

// ErrMediaPart — uma parte de media não tem um tipo aceite. As validações da forma de cada classe
// são as de [ImagePart] e [VideoPart].
var ErrMediaPart = errors.New("port: parte de media de tipo nao aceite")

// Digest é o `sha256` dos bytes, na forma `sha256:<hex>`.
func (p MediaPart) Digest() string {
	soma := sha256.Sum256(p.Data)
	return "sha256:" + hex.EncodeToString(soma[:])
}

// String mostra a parte sem os bytes.
func (p MediaPart) String() string {
	return fmt.Sprintf("MediaPart{media_type=%q,bytes=%d,%s}", p.MediaType, len(p.Data), p.Digest())
}

// GoString é o [MediaPart.String] também para `%#v`.
func (p MediaPart) GoString() string { return p.String() }

// MarshalJSON serializa a parte SEM os bytes.
func (p MediaPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MediaType string `json:"media_type"`
		Bytes     int    `json:"bytes"`
		Digest    string `json:"sha256"`
	}{p.MediaType, len(p.Data), p.Digest()})
}

// Validate verifica a parte pela regra da sua classe: [ImagePart.Validate] ou
// [VideoPart.Validate]. Um tipo fora da tabela é [ErrMediaPart].
func (p MediaPart) Validate() error {
	switch c, _ := MediaClassOf(p.MediaType); c {
	case MediaClassImage:
		return ImagePart(p).Validate()
	case MediaClassVideo:
		return VideoPart(p).Validate()
	default:
		return fmt.Errorf("%w: %q", ErrMediaPart, p.MediaType)
	}
}

// AttachMedia acrescenta a parte à mensagem, no campo da sua classe. Valida a parte antes; as
// regras do pedido inteiro (o papel `user`, os tectos agregados, um vídeo sem imagens) aplicam-se
// depois, em [ChatRequest.Normalize] e [ChatRequest.MarshalWire], como para as partes de sempre.
func (m *Message) AttachMedia(p MediaPart) error {
	if err := p.Validate(); err != nil {
		return err
	}
	switch c, _ := MediaClassOf(p.MediaType); c {
	case MediaClassImage:
		m.Images = append(m.Images, ImagePart(p))
	case MediaClassVideo:
		m.Videos = append(m.Videos, VideoPart(p))
	}
	return nil
}
