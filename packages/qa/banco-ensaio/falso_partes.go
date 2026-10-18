package bancoensaio

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// AS PARTES DO `content` QUE CHEGAM AO FORNECEDOR (AOS-534, spike da sub-fase A6.1).
//
// A A6.1 manda um vídeo ao modelo como uma parte `{"type":"video_url","video_url":{"url":
// "data:video/mp4;base64,…"}}` ao lado do texto (decisão do dono de 2026-10-18, opção A). O que
// o proxy fixado faz a essa parte — entrega-a intacta, recusa-a, troca-lhe o tipo, re-codifica-a
// — só se vê do lado do fornecedor. Por isso o falso do estado regista, de cada parte do
// `content` de cada mensagem que recebe:
//
//   - o `type`, por uma lista FECHADA ([tiposDeParte]); um tipo fora dela conta como `outra`;
//   - as chaves do objecto interior da parte (`video_url.url`, `file.file_data`, …), também por
//     uma lista fechada;
//   - numa parte que traga uma data URL — `video_url.url`, `image_url.url`, `file.file_data` —
//     a classe da URL ([ClasseDataURLVideoMP4] e as outras), o sha256 e o tamanho dos bytes
//     descodificados ([MediaNoFornecedor]).
//
// Os BYTES NÃO SE GRAVAM, nem o texto das partes de texto, nem a URL. O sha256 é de bytes que o
// próprio banco gerou (um MP4 sintético); serve para dizer «chegou o mesmo», nada mais.
//
// O QUE NÃO PROVA: o que a OpenRouter real aceita, ou que modelos lêem vídeo. É só o primeiro elo.

// maxCorpoDoFalsoDoEstado é o tecto de corpo do falso do estado. Subiu de 8 MiB para 32 MiB no
// AOS-534: o caso grande do vídeo (3 a 6 MB em base64) tem de caber, para que um tecto que se
// meça seja o do proxy e não o do falso. Um corpo acima conta-se com [RecusaCorpoAcimaDoTecto].
const maxCorpoDoFalsoDoEstado = 32 << 20

// maxMediaRegistados limita a lista de [FormaNoFornecedor.Media]: um falso de ensaio não guarda
// sem fim. As partes acima do limite continuam a contar em [FormaNoFornecedor.Partes].
const maxMediaRegistados = 256

// As classes da URL de uma parte com media ([MediaNoFornecedor.Classe]). Vocabulário fechado.
const (
	// ClasseDataURLVideoMP4 — `data:video/mp4;base64,` seguido de base64 válido.
	ClasseDataURLVideoMP4 = "data_url_video_mp4"
	// ClasseDataURLVideoOutro — uma data URL base64 de outro `video/*`.
	ClasseDataURLVideoOutro = "data_url_video_outro"
	// ClasseDataURLOutra — uma data URL base64 que não é de vídeo (imagem, PDF, …).
	ClasseDataURLOutra = "data_url_outra"
	// ClasseDataURLBase64Invalido — começa por `data:` e `;base64,`, mas o resto não descodifica.
	ClasseDataURLBase64Invalido = "data_url_base64_invalido"
	// ClasseURLRemota — `http://` ou `https://`.
	ClasseURLRemota = "url_remota"
	// ClasseURLOutra — qualquer outra coisa (uma data URL sem base64, um id de ficheiro, vazio).
	ClasseURLOutra = "outra"
)

// RecusaCorpoAcimaDoTecto — o corpo do pedido passou [maxCorpoDoFalsoDoEstado].
const RecusaCorpoAcimaDoTecto = "corpo_acima_do_tecto_do_falso"

// ContentTextoComVideo é a chave de [FormaNoFornecedor.Partes] que conta as mensagens cujo
// `content` chegou como TEXTO com uma data URL de vídeo lá dentro — a parte achatada em texto.
const ContentTextoComVideo = "content_texto_com_data_url_de_video"

// MediaNoFornecedor é uma parte com media que chegou ao falso: o tipo da parte, a classe da URL e,
// numa data URL base64 válida, o sha256 (hexadecimal) e o tamanho dos bytes. Nunca os bytes.
type MediaNoFornecedor struct {
	Parte  string `json:"parte"`
	Classe string `json:"classe_da_url"`
	Sha256 string `json:"sha256,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
}

// tiposDeParte é a lista FECHADA dos `type` de parte que o falso nomeia. Inclui os do wire de
// chat da OpenAI e da OpenRouter, e os do wire de mensagens da Anthropic.
var tiposDeParte = map[string]bool{
	"text": true, "image_url": true, "video_url": true, "input_audio": true, "file": true,
	"input_video": true, "video": true, "image": true, "document": true, "input_file": true,
	"tool_use": true, "tool_result": true, "thinking": true, "redacted_thinking": true,
}

// chavesInterioresDaParte é a lista FECHADA das chaves do objecto interior de uma parte.
var chavesInterioresDaParte = map[string]bool{
	"url": true, "detail": true, "format": true, "mime_type": true, "file_data": true,
	"file_id": true, "filename": true, "data": true, "media_type": true, "type": true,
}

// camposComMedia diz, por tipo de parte, a chave interior onde pode vir a data URL.
var camposComMedia = map[string]string{"video_url": "url", "image_url": "url", "file": "file_data", "input_video": "url"}

// classificarURL devolve a classe da URL e, numa data URL base64 válida, os bytes.
func classificarURL(url string) (string, []byte) {
	switch {
	case strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"):
		return ClasseURLRemota, nil
	case !strings.HasPrefix(url, "data:"):
		return ClasseURLOutra, nil
	}
	cabeca, dados, tem := strings.Cut(url, ",")
	if !tem || !strings.HasSuffix(cabeca, ";base64") {
		return ClasseURLOutra, nil
	}
	cru, err := base64.StdEncoding.DecodeString(dados)
	if err != nil {
		return ClasseDataURLBase64Invalido, nil
	}
	tipo := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(cabeca, "data:"), ";base64"))
	switch {
	case tipo == "video/mp4":
		return ClasseDataURLVideoMP4, cru
	case strings.HasPrefix(tipo, "video/"):
		return ClasseDataURLVideoOutro, cru
	}
	return ClasseDataURLOutra, cru
}

// contarPartes regista as partes do `content` de cada mensagem (só a forma, ver o topo do
// ficheiro). Um `content` que é texto não conta, salvo se traz uma data URL de vídeo.
func (f *FalsoDeEstado) contarPartes(msgs []map[string]json.RawMessage) {
	for _, m := range msgs {
		cru := m["content"]
		var texto string
		if json.Unmarshal(cru, &texto) == nil {
			if strings.Contains(texto, "data:video/") {
				f.contar(&f.forma.Partes, ContentTextoComVideo)
			}
			continue
		}
		var partes []map[string]json.RawMessage
		if json.Unmarshal(cru, &partes) != nil {
			continue
		}
		for _, p := range partes {
			var tipo string
			_ = json.Unmarshal(p["type"], &tipo)
			if !tiposDeParte[tipo] {
				f.contar(&f.forma.Partes, "outra")
				continue
			}
			f.contar(&f.forma.Partes, tipo)
			interior := p[tipo]
			var obj map[string]json.RawMessage
			var url string
			switch {
			case json.Unmarshal(interior, &obj) == nil:
				outra := false
				for k := range obj {
					if chavesInterioresDaParte[k] {
						f.contar(&f.forma.Partes, tipo+"."+k)
					} else {
						outra = true
					}
				}
				if outra {
					f.contar(&f.forma.Partes, tipo+".outra")
				}
				if campo, tem := camposComMedia[tipo]; tem {
					_ = json.Unmarshal(obj[campo], &url)
				}
			case json.Unmarshal(interior, &url) == nil:
				// A forma curta, `"image_url": "<url>"`.
				f.contar(&f.forma.Partes, tipo+".texto")
			}
			if _, tem := camposComMedia[tipo]; !tem || url == "" {
				continue
			}
			classe, dados := classificarURL(url)
			media := MediaNoFornecedor{Parte: tipo, Classe: classe}
			if dados != nil {
				soma := sha256.Sum256(dados)
				media.Sha256, media.Bytes = hex.EncodeToString(soma[:]), len(dados)
			}
			if len(f.forma.Media) < maxMediaRegistados {
				f.forma.Media = append(f.forma.Media, media)
			}
		}
	}
}
