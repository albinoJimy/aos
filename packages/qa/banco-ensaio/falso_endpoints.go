package bancoensaio

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
)

// O CAMINHO E AS CHAVES DO PEDIDO QUE CHEGAM AO FORNECEDOR, E UMA IMAGEM DE SAÍDA (AOS-558,
// spike da pré-condição da fase R3 do ADR-043).
//
// A §3 E do ADR-043 deixa em aberto um adaptador directo à OpenRouter e manda decidir «pelo
// resultado de um spike que meça se o LiteLLM fixado encaminha as APIs de imagem e de vídeo».
// O AOS-534 mediu uma parte de ENTRADA dentro de `chat/completions`; aqui a pergunta é sobre
// ENDEREÇOS QUE NÃO SÃO O DO CHAT — `/v1/images/generations` e a família `/v1/videos` — e sobre
// o sentido da SAÍDA, que é o que a R3 acrescenta.
//
// Para isso o falso do estado passa a registar, de cada pedido:
//
//   - o CAMINHO, por uma lista FECHADA ([ClassificarCaminho]); um caminho fora dela conta como
//     `outro`, e o caminho em bruto nunca se grava;
//   - as CHAVES DO TOPO do corpo, por outra lista fechada ([chavesDoTopoDeGeracao]) — é por aí
//     que se vê se `modalities`, `provider`, `plugins` ou `image_config` sobreviveram ao
//     `drop_params: true` da configuração de produção.
//
// E passa a saber responder em duas formas novas, para que se possa medir o sentido de VOLTA:
//
//   - com [FalsoDeEstado.ImagemDeSaida], uma resposta de chat sem tools leva um `images` na
//     mensagem, na forma da OpenRouter (`image_url.url` com uma data URL de PNG): é o que o
//     adaptador `openrouter` do proxy lê para montar a resposta de `images/generations`;
//   - um pedido a um caminho de vídeo é servido na forma da API de vídeo da OpenAI (um job com
//     `id` e `status`), que é a que o adaptador `openai` do proxy espera.
//
// MEDE, NÃO JULGA: uma recusa do proxy antes de chegar aqui é um resultado do spike, e vê-se
// por não haver pedido nenhum registado no caminho.
//
// O QUE NÃO PROVA: nada sobre a OpenRouter real — que modelos geram imagem ou vídeo, com que
// preço e com que limites. O falso aceita o que o proxy lhe mandar.

// Os caminhos que o falso nomeia em [FormaNoFornecedor.Caminhos]. Vocabulário fechado.
const (
	// CaminhoDeChat — `…/chat/completions`.
	CaminhoDeChat = "chat_completions"
	// CaminhoDeMensagens — `…/messages`, o wire da Anthropic.
	CaminhoDeMensagens = "messages"
	// CaminhoDeImagens — `…/images/generations`.
	CaminhoDeImagens = "images_generations"
	// CaminhoDeEdicaoDeImagens — `…/images/edits`.
	CaminhoDeEdicaoDeImagens = "images_edits"
	// CaminhoDeVideos — `…/videos`: a criação de um job (POST) ou a lista (GET).
	CaminhoDeVideos = "videos"
	// CaminhoDeVideoPorID — `…/videos/<id>`: o estado do job.
	CaminhoDeVideoPorID = "videos_id"
	// CaminhoDoConteudoDoVideo — `…/videos/<id>/content`: os bytes do vídeo.
	CaminhoDoConteudoDoVideo = "videos_id_content"
	// CaminhoOutro — qualquer outro caminho.
	CaminhoOutro = "outro"
)

// ClassificarCaminho devolve o nome, da lista fechada acima, do caminho de um pedido. O caminho
// em bruto não entra no que o falso registou: só este nome.
func ClassificarCaminho(caminho string) string {
	limpo := strings.TrimRight(caminho, "/")
	switch {
	case strings.HasSuffix(limpo, "/chat/completions"):
		return CaminhoDeChat
	case strings.HasSuffix(limpo, "/messages"):
		return CaminhoDeMensagens
	case strings.HasSuffix(limpo, "/images/generations"):
		return CaminhoDeImagens
	case strings.HasSuffix(limpo, "/images/edits"):
		return CaminhoDeEdicaoDeImagens
	case strings.HasSuffix(limpo, "/content") && strings.Contains(limpo, "/videos/"):
		return CaminhoDoConteudoDoVideo
	case strings.HasSuffix(limpo, "/videos"):
		return CaminhoDeVideos
	case strings.Contains(limpo, "/videos/"):
		return CaminhoDeVideoPorID
	}
	return CaminhoOutro
}

// chavesDoTopoDeGeracao é a lista FECHADA das chaves do topo de um pedido que o falso nomeia em
// [FormaNoFornecedor.ChavesDoTopo]. São as do chat, as da geração de imagem da OpenAI, as da
// geração de imagem da OpenRouter (`image_config`) e as de roteamento da OpenRouter (`provider`,
// `plugins`, `transforms`) — estas últimas é que dizem se o proxy deixa DECLARAR o endpoint.
// Uma chave fora da lista conta como `outra`, uma vez, e o seu nome não vai.
var chavesDoTopoDeGeracao = map[string]bool{
	"model": true, "messages": true, "prompt": true, "n": true, "size": true, "quality": true,
	"response_format": true, "modalities": true, "image_config": true, "provider": true,
	"plugins": true, "transforms": true, "input_reference": true, "seconds": true,
	"max_tokens": true, "stream": true, "tools": true, "tool_choice": true, "user": true,
	"metadata": true, "extra_body": true, "background": true, "output_format": true,
}

// contarChavesDoTopo regista, por NOME, as chaves do topo do corpo de um pedido. Nenhum valor.
func (f *FalsoDeEstado) contarChavesDoTopo(topo map[string]json.RawMessage) {
	outra := false
	for k := range topo {
		if chavesDoTopoDeGeracao[k] {
			f.contar(&f.forma.ChavesDoTopo, k)
		} else {
			outra = true
		}
	}
	if outra {
		f.contar(&f.forma.ChavesDoTopo, "outra")
	}
}

// PNGDoFalso é o PNG que o falso emite como imagem gerada: 4×4 pixéis deterministas, válido como
// PNG e sem nada dentro. Os seus bytes são a SENTINELA do sentido de volta: o que o cliente
// receber do proxy compara-se com o sha256 destes.
//
// Quem mede corre noutro binário (o falso é um binário LINUX, dentro do contentor), por isso o
// sha256 destes bytes vai na forma do falso ([FormaNoFornecedor.ShaDoPNG]): sem o confrontar, um
// falso de outra árvore dava «saída alterada» em tudo, calado.
func PNGDoFalso() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(16 * x), G: uint8(16 * y), B: 0x42, A: 0xFF})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		// Encode de um NRGBA em memória não falha. Se falhasse, o falso emitiria uma data URL
		// vazia; quem mede descarta um candidato vazio em vez de o comparar, e o confronto dos
		// sha na montagem do ensaio apanha-o antes de haver medida nenhuma.
		return nil
	}
	return b.Bytes()
}

// shaDoPNG é o sha256, em hexadecimal, do PNG que este falso emite — vazio quando a imagem de
// saída não está ligada, para não aparecer na forma dos cenários que não a usam.
func (f *FalsoDeEstado) shaDoPNG() string {
	if !f.ImagemDeSaida {
		return ""
	}
	soma := sha256.Sum256(PNGDoFalso())
	return hex.EncodeToString(soma[:])
}

// dataURLDoPNGDoFalso é o PNG de [PNGDoFalso] como data URL base64, na forma em que a OpenRouter
// devolve uma imagem gerada.
func dataURLDoPNGDoFalso() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(PNGDoFalso())
}

// imagensDaResposta é o campo `images` de uma mensagem da OpenRouter com uma imagem gerada.
func imagensDaResposta() []any {
	return []any{map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": dataURLDoPNGDoFalso()},
	}}
}

// IDDoJobDeVideo é o id do job que o falso devolve a uma criação de vídeo. É fixo: há um job por
// corrida, e o cenário pede o estado dele pelo nome.
const IDDoJobDeVideo = "video_banco_de_ensaio"

// conteudoDoVideoDoFalso são os bytes que o falso devolve em `…/videos/<id>/content`. Não é um
// MP4: o spike não mede a descodificação, mede o encaminhamento.
var conteudoDoVideoDoFalso = []byte("AOS-558-conteudo-do-video")

// servirVideos serve os caminhos da API de vídeo, na forma da API de vídeo da OpenAI — a que o
// adaptador `openai` do proxy espera. Um POST cria um job `queued`; um GET pelo id devolve-o
// `completed`; um GET do conteúdo devolve bytes.
//
// O corpo de um POST entra nas chaves do topo quando é JSON; um corpo que não seja JSON (um
// `multipart/form-data`, por exemplo, que é o que o endpoint do proxy também aceita) não se
// grava e conta-se como `corpo_nao_json` nas CHAVES DO TOPO — e não nos caminhos, porque é uma
// observação sobre o corpo, e um segundo nome no mapa dos caminhos para o mesmo pedido fazia
// quem mede ler «mais do que um caminho».
func (f *FalsoDeEstado) servirVideos(w http.ResponseWriter, r *http.Request, caminho string, corpo []byte) {
	if r.Method == http.MethodPost {
		var topo map[string]json.RawMessage
		if json.Unmarshal(corpo, &topo) == nil {
			f.contarChavesDoTopo(topo)
		} else if len(corpo) > 0 {
			f.contar(&f.forma.ChavesDoTopo, "corpo_nao_json")
		}
	}
	if caminho == CaminhoDoConteudoDoVideo {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(conteudoDoVideoDoFalso)
		return
	}
	estado := "queued"
	if r.Method == http.MethodGet {
		estado = "completed"
	}
	cru, _ := json.Marshal(map[string]any{
		"id": IDDoJobDeVideo, "object": "video", "status": estado,
		"model": "modelo-falso-do-banco", "created_at": 0, "progress": 100,
	})
	_, _ = w.Write(cru)
}

// servirImagens serve `…/images/generations` e `…/images/edits` na forma da OpenAI: uma lista
// `data` com o PNG do falso em `b64_json`. É o caminho que o adaptador `openai` do proxy usa; o
// adaptador `openrouter` nunca chega aqui, porque traduz a geração de imagem para chat.
func (f *FalsoDeEstado) servirImagens(w http.ResponseWriter) {
	cru, _ := json.Marshal(map[string]any{
		"created": 0,
		"data":    []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(PNGDoFalso())}},
	})
	_, _ = w.Write(cru)
}
