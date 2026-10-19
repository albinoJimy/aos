package port

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// PARTE DE VÍDEO NO PEDIDO (AOS-535, sub-fase A6.1; campo aditivo, MINOR 1.13.0).
//
// Uma mensagem `user` de um PEDIDO pode levar uma parte de vídeo ([VideoPart]): o tipo e os bytes
// de um MP4. É o precedente das partes de imagem ([ImagePart], AOS-530) com um campo próprio,
// [Message.Videos], e não uma generalização de [Message.Images] numa lista de «media»: um campo
// novo com `json:"-"` não muda o wire nem os selos de nenhum pedido de hoje, e não toca em quem já
// constrói imagens (a `ocr_read`). Só [ChatRequest.MarshalWire] o escreve, na forma OpenAI de
// chat/completions que o AOS-534 mediu atrás do proxy fixado — `content` como lista de partes, o
// texto primeiro e o vídeo a seguir, como `video_url` numa data URL `video/mp4` em base64.
//
// AS RESPOSTAS CONTINUAM SÓ TEXTO, e [Message.Videos] nunca se lê de JSON (`json:"-"`).
//
// OS BYTES NÃO SAEM POR OUTRA VIA. Um [VideoPart] impresso (`%v`, `%+v`, `%#v`) ou serializado
// sozinho mostra só o tipo, o tamanho e o `sha256` ([VideoPart.String], [VideoPart.MarshalJSON]).
// O único sítio por onde os bytes chegam a um texto é o corpo do pedido ao provider.
//
// QUEM PODE MANDAR VÍDEO NÃO É DECIDIDO AQUI. A porta valida a FORMA ([ChatRequest.Normalize]); é
// o gateway que recusa um vídeo para uma rota cujo perfil não declare a capacidade `video`.

// VideoMediaMP4 é o único tipo aceite numa [VideoPart]. Vocabulário FECHADO, comparação exacta.
const VideoMediaMP4 = "video/mp4"

// MaxVideoBytes é o tecto, em bytes CRUS, de uma parte de vídeo E da soma das partes de vídeo de
// um pedido: 4 MiB. Em base64 são cerca de 5,6 MB, o tamanho que o AOS-534 provou atravessar o
// proxy fixado intacto (4 194 456 bytes crus, pedido de 5 592 829 bytes), nas duas rotas. SUBIR
// ESTE TECTO EXIGE UMA NOVA MEDIDA DO PROXY: acima do que foi medido não se sabe se o corpo chega
// inteiro ao fornecedor.
const MaxVideoBytes = 4 << 20

// VideoPart é a parte de vídeo de uma mensagem `user` de um pedido. MediaType é [VideoMediaMP4] e
// Data um MP4 com o índice à cabeça ([VideoPart.Validate]).
type VideoPart struct {
	MediaType string
	Data      []byte
}

// ErrVideoPart — a parte de vídeo do pedido não passa na validação. Fail-closed: o pedido não sai.
// [*VideoPartError] casa com este erro por [errors.Is].
var ErrVideoPart = errors.New("port: parte de video invalida no pedido")

// Causas de [VideoPartError]. Vocabulário FECHADO: nenhuma leva bytes nem o tipo declarado.
const (
	// VideoCauseMediaType — o tipo não é [VideoMediaMP4].
	VideoCauseMediaType = "tipo_nao_suportado"
	// VideoCauseEmpty — a parte não tem bytes.
	VideoCauseEmpty = "video_vazio"
	// VideoCauseTooLarge — a parte tem mais de [MaxVideoBytes].
	VideoCauseTooLarge = "video_acima_do_tecto"
	// VideoCauseMagic — os bytes não abrem por uma caixa `ftyp` com uma marca aceite.
	VideoCauseMagic = "tipo_nao_bate_com_os_bytes"
	// VideoCauseStructure — as caixas de topo não se lêem até ao fim, ou falta a `moov` ou a `mdat`.
	VideoCauseStructure = "estrutura_mp4_invalida"
	// VideoCauseNotFastStart — a `moov` (o índice) vem depois da `mdat`: o fornecedor teria de ler
	// o ficheiro inteiro antes de o poder descodificar (sem `+faststart`).
	VideoCauseNotFastStart = "moov_depois_da_mdat"
	// VideoCauseRole — a parte está numa mensagem que não é `user`.
	VideoCauseRole = "video_fora_de_mensagem_user"
	// VideoCauseTotalTooLarge — as partes de vídeo de todas as mensagens somam mais de
	// [MaxVideoBytes].
	VideoCauseTotalTooLarge = "videos_acima_do_tecto"
	// VideoCauseMoreThanOne — o pedido leva mais de uma parte de vídeo.
	VideoCauseMoreThanOne = "mais_de_um_video"
	// VideoCauseWithImages — o pedido leva vídeo e imagens.
	VideoCauseWithImages = "video_com_imagens"
)

// VideoPartError é o erro de uma parte de vídeo recusada. Casa com [ErrVideoPart].
type VideoPartError struct {
	// Cause é uma das constantes VideoCause*.
	Cause string
}

// Error implementa error.
func (e *VideoPartError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrVideoPart, e.Cause)
}

// Unwrap liga o erro a [ErrVideoPart].
func (e *VideoPartError) Unwrap() error { return ErrVideoPart }

// marcasMP4 são as `major_brand` aceites na caixa `ftyp`: as da família ISO base media e do MP4
// (o ffmpeg escreve `isom`). Fica de fora o QuickTime (`qt  `), o 3GP e o resto — vocabulário
// FECHADO, comparação exacta.
var marcasMP4 = map[string]bool{"isom": true, "iso2": true, "mp41": true, "mp42": true, "avc1": true}

// Validate verifica a parte, pela ordem da gravidade: o tipo no vocabulário (um tipo desconhecido
// não chega a ter os bytes lidos), bytes não vazios, no máximo [MaxVideoBytes], a magia (a caixa
// 1 é `ftyp` com uma marca de [marcasMP4]) e a estrutura mínima — as caixas de topo lêem-se até ao
// último byte, há `moov` e `mdat`, e a primeira `moov` vem antes da primeira `mdat`.
//
// Só se lêem cabeçalhos de caixas de topo; nada é descodificado.
func (p VideoPart) Validate() error {
	if p.MediaType != VideoMediaMP4 {
		return &VideoPartError{Cause: VideoCauseMediaType}
	}
	switch {
	case len(p.Data) == 0:
		return &VideoPartError{Cause: VideoCauseEmpty}
	case len(p.Data) > MaxVideoBytes:
		return &VideoPartError{Cause: VideoCauseTooLarge}
	case len(p.Data) < 12 || string(p.Data[4:8]) != "ftyp" || !marcasMP4[string(p.Data[8:12])]:
		return &VideoPartError{Cause: VideoCauseMagic}
	}
	return estruturaMP4(p.Data)
}

// estruturaMP4 percorre as caixas de topo de um MP4 até ao último byte. Fail-closed: um tamanho
// menor que o cabeçalho ou que sai dos bytes, ou um cabeçalho truncado, é estrutura inválida; sem
// `moov` ou sem `mdat` também. Uma caixa de tamanho 0 vai até ao fim dos bytes (é a última).
func estruturaMP4(b []byte) error {
	moov, mdat := -1, -1
	for i := 0; len(b) > 0; i++ {
		if len(b) < 8 {
			return &VideoPartError{Cause: VideoCauseStructure}
		}
		tamanho, cabeca := uint64(binary.BigEndian.Uint32(b)), uint64(8)
		switch tamanho {
		case 0:
			tamanho = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return &VideoPartError{Cause: VideoCauseStructure}
			}
			tamanho, cabeca = binary.BigEndian.Uint64(b[8:]), 16
		}
		if tamanho < cabeca || tamanho > uint64(len(b)) {
			return &VideoPartError{Cause: VideoCauseStructure}
		}
		switch string(b[4:8]) {
		case "moov":
			if moov < 0 {
				moov = i
			}
		case "mdat":
			if mdat < 0 {
				mdat = i
			}
		}
		b = b[tamanho:]
	}
	switch {
	case moov < 0 || mdat < 0:
		return &VideoPartError{Cause: VideoCauseStructure}
	case moov > mdat:
		return &VideoPartError{Cause: VideoCauseNotFastStart}
	}
	return nil
}

// Digest é o `sha256` dos bytes da parte, na forma `sha256:<hex>`. É o que pode ficar num
// resultado, num evento ou num span no lugar dos bytes.
func (p VideoPart) Digest() string {
	soma := sha256.Sum256(p.Data)
	return "sha256:" + hex.EncodeToString(soma[:])
}

// String mostra a parte sem os bytes: o tipo, o tamanho e o digest.
func (p VideoPart) String() string {
	return fmt.Sprintf("VideoPart{media_type=%q,bytes=%d,%s}", p.MediaType, len(p.Data), p.Digest())
}

// GoString é o [VideoPart.String] também para `%#v`.
func (p VideoPart) GoString() string { return p.String() }

// MarshalJSON serializa a parte SEM os bytes: o tipo, o tamanho e o digest. Os bytes só vão ao
// provider, por [ChatRequest.MarshalWire], que não passa por aqui.
func (p VideoPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MediaType string `json:"media_type"`
		Bytes     int    `json:"bytes"`
		Digest    string `json:"sha256"`
	}{p.MediaType, len(p.Data), p.Digest()})
}

// HasVideos diz se alguma mensagem do pedido leva uma parte de vídeo.
func (r ChatRequest) HasVideos() bool {
	return temVideos(r.Messages)
}

// temVideos diz se alguma mensagem leva partes de vídeo.
func temVideos(msgs []Message) bool {
	for i := range msgs {
		if len(msgs[i].Videos) > 0 {
			return true
		}
	}
	return false
}

// validarVideos verifica as partes de vídeo de uma mensagem: só numa mensagem `user`, e cada parte
// válida ([VideoPart.Validate]).
func validarVideos(m Message) error {
	if len(m.Videos) == 0 {
		return nil
	}
	if m.Role != RoleUser {
		return &VideoPartError{Cause: VideoCauseRole}
	}
	for _, p := range m.Videos {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// videosDoPedido aplica as regras do pedido inteiro, depois de cada parte validada, por esta ordem:
//
//  1. o tecto agregado — as partes de vídeo de todas as mensagens somam no máximo [MaxVideoBytes];
//  2. no máximo UM vídeo por pedido. O tecto de 4 MiB é o de um corpo medido atrás do proxy com
//     uma parte; o custo de um vídeo no fornecedor cresce com a duração, e dois vídeos são um
//     pedido cujo custo e cuja leitura ninguém mediu (AOS-537 mede um vídeo por pedido); e a tool
//     que os vai mandar (AOS-536) lê um vídeo de cada vez. Subir para mais é decisão nova, com
//     medida — e o tecto agregado já cá está para quando for;
//  3. vídeo e imagens no mesmo pedido não. Nenhuma rota foi medida com as duas partes juntas, e a
//     recusa é a forma mais simples de não ter de decidir que capacidade um pedido misto exige.
func videosDoPedido(msgs []Message) error {
	total, n := 0, 0
	for i := range msgs {
		for _, p := range msgs[i].Videos {
			total += len(p.Data)
			n++
			if total > MaxVideoBytes {
				return &VideoPartError{Cause: VideoCauseTotalTooLarge}
			}
		}
	}
	switch {
	case n > 1:
		return &VideoPartError{Cause: VideoCauseMoreThanOne}
	case n == 1 && temImagens(msgs):
		return &VideoPartError{Cause: VideoCauseWithImages}
	}
	return nil
}

// Parte de vídeo de `content`, na forma OpenAI de chat/completions que o AOS-534 mediu.
type (
	urlDoVideo struct {
		URL string `json:"url"`
	}
	parteDeVideoNoPedido struct {
		Type     string     `json:"type"`
		VideoURL urlDoVideo `json:"video_url"`
	}
)

// parteDeVideo escreve uma parte de vídeo como `video_url` numa data URL em base64.
func parteDeVideo(p VideoPart) parteDeVideoNoPedido {
	return parteDeVideoNoPedido{
		Type:     "video_url",
		VideoURL: urlDoVideo{URL: "data:" + p.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)},
	}
}
