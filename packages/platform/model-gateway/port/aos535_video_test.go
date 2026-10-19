package port_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-535 — A PARTE DE VÍDEO NO PEDIDO (porta 1.13.0). A porta valida a forma (MP4 pelos bytes, o
// índice à cabeça, o tecto, o papel, um vídeo por pedido e sem imagens) e escreve-a no wire como
// `video_url`; os bytes não saem por mais via nenhuma.

// aos535Caixa monta uma caixa MP4 de topo: o tamanho (32 bits), o tipo e o corpo.
func aos535Caixa(tipo string, corpo []byte) []byte {
	b := make([]byte, 8, 8+len(corpo))
	binary.BigEndian.PutUint32(b, uint32(8+len(corpo))) // #nosec G115 -- corpos de teste pequenos
	copy(b[4:], tipo)
	return append(b, corpo...)
}

// aos535Corpo é o corpo da `mdat`: bytes que não aparecem por acaso em nenhum texto do pedido.
var aos535Corpo = []byte("MDAT-aos535-corpo-do-video-\x00\x01\x02\xfe\x7f\x80")

// aos535Ftyp é a caixa `ftyp` com a marca dada (a do ffmpeg é `isom`).
func aos535Ftyp(marca string) []byte {
	return aos535Caixa("ftyp", []byte(marca+"\x00\x00\x02\x00isomiso2avc1mp41"))
}

// aos535MP4 é um MP4 mínimo com o índice à cabeça (o `+faststart`): ftyp, moov, mdat.
func aos535MP4() []byte {
	var b []byte
	b = append(b, aos535Ftyp("isom")...)
	b = append(b, aos535Caixa("moov", aos535Caixa("mvhd", make([]byte, 20)))...)
	b = append(b, aos535Caixa("mdat", aos535Corpo)...)
	return b
}

// aos535MP4DeTamanho devolve um MP4 válido com exactamente n bytes (a `mdat` cresce).
func aos535MP4DeTamanho(n int) []byte {
	cabeca := append(aos535Ftyp("isom"), aos535Caixa("moov", nil)...)
	return append(cabeca, aos535Caixa("mdat", make([]byte, n-len(cabeca)-8))...)
}

func aos535Video(data []byte) port.VideoPart {
	return port.VideoPart{MediaType: port.VideoMediaMP4, Data: data}
}

func aos535Pedido(msgs ...port.Message) port.ChatRequest {
	return port.ChatRequest{Model: "video-visao", Messages: msgs}
}

func aos535User(videos ...port.VideoPart) port.Message {
	return port.Message{Role: port.RoleUser, Content: "descreve", Videos: videos}
}

// aos535QueroCausa confere o erro da porta e a sua causa.
func aos535QueroCausa(t *testing.T, via string, err error, causa string) {
	t.Helper()
	var ve *port.VideoPartError
	if !errors.Is(err, port.ErrVideoPart) || !errors.As(err, &ve) || ve.Cause != causa {
		t.Fatalf("%s = %v; quero ErrVideoPart com causa %s", via, err, causa)
	}
}

// Cada recusa da forma, com a sua causa — no Normalize E no wire chamado sem Normalize.
func TestAOS535_Normalize_RecusaCadaForma(t *testing.T) {
	t.Parallel()
	mp4 := aos535MP4()
	ftyp := aos535Ftyp("isom")
	moov := aos535Caixa("moov", nil)
	mdat := aos535Caixa("mdat", aos535Corpo)
	junta := func(partes ...[]byte) []byte { return bytes.Join(partes, nil) }
	truncada := append(junta(ftyp, moov), 0, 0, 0)                            // cabeçalho de caixa com 3 bytes
	caixaMente := junta(ftyp, moov, []byte{0, 0, 0xff, 0xff}, []byte("mdat")) // diz 65535 e restam 8
	caixaPequena := junta(ftyp, moov, []byte{0, 0, 0, 4}, []byte("mdat"))     // tamanho menor que o cabeçalho
	larga := junta(ftyp, moov, []byte{0, 0, 0, 1}, []byte("mdat"), []byte{0, 0, 0, 0})
	grande := aos535MP4DeTamanho(port.MaxVideoBytes + 1)
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'x'}

	casos := []struct {
		nome  string
		msg   port.Message
		causa string
	}{
		{"tipo fora do vocabulario", aos535User(port.VideoPart{MediaType: "video/webm", Data: mp4}), port.VideoCauseMediaType},
		{"tipo com caixa diferente", aos535User(port.VideoPart{MediaType: "video/MP4", Data: mp4}), port.VideoCauseMediaType},
		{"tipo quicktime", aos535User(port.VideoPart{MediaType: "video/quicktime", Data: mp4}), port.VideoCauseMediaType},
		{"tipo vazio", aos535User(port.VideoPart{Data: mp4}), port.VideoCauseMediaType},
		{"bytes vazios", aos535User(aos535Video(nil)), port.VideoCauseEmpty},
		{"acima de 4 MiB", aos535User(aos535Video(grande)), port.VideoCauseTooLarge},
		{"png nos bytes", aos535User(aos535Video(png)), port.VideoCauseMagic},
		{"primeira caixa nao e ftyp", aos535User(aos535Video(junta(moov, ftyp, mdat))), port.VideoCauseMagic},
		{"marca quicktime", aos535User(aos535Video(junta(aos535Ftyp("qt  "), moov, mdat))), port.VideoCauseMagic},
		{"marca 3gp", aos535User(aos535Video(junta(aos535Ftyp("3gp4"), moov, mdat))), port.VideoCauseMagic},
		{"marca com caixa diferente", aos535User(aos535Video(junta(aos535Ftyp("ISOM"), moov, mdat))), port.VideoCauseMagic},
		{"ftyp cortada", aos535User(aos535Video(ftyp[:11])), port.VideoCauseMagic},
		{"caixa 1 com a marca mas sem ser ftyp", aos535User(aos535Video(junta(aos535Caixa("free", []byte("isom\x00\x00\x02\x00")), moov, mdat))), port.VideoCauseMagic},
		// Um tamanho menor que o cabeçalho que, lido como se valesse, encaixava numa `mdat` válida.
		{"caixa menor que o cabecalho que encaixava", aos535User(aos535Video(junta(ftyp, moov, []byte{0, 0, 0, 4, 0, 0, 0, 8}, []byte("mdat")))), port.VideoCauseStructure},
		{"moov depois da mdat", aos535User(aos535Video(junta(ftyp, mdat, moov))), port.VideoCauseNotFastStart},
		{"sem moov", aos535User(aos535Video(junta(ftyp, mdat))), port.VideoCauseStructure},
		{"sem mdat", aos535User(aos535Video(junta(ftyp, moov))), port.VideoCauseStructure},
		{"so a ftyp", aos535User(aos535Video(ftyp)), port.VideoCauseStructure},
		{"cabecalho de caixa truncado", aos535User(aos535Video(truncada)), port.VideoCauseStructure},
		{"caixa diz mais bytes do que restam", aos535User(aos535Video(caixaMente)), port.VideoCauseStructure},
		{"caixa menor que o cabecalho", aos535User(aos535Video(caixaPequena)), port.VideoCauseStructure},
		{"tamanho largo truncado", aos535User(aos535Video(larga)), port.VideoCauseStructure},
		{"ftyp diz mais bytes do que ha", aos535User(aos535Video(append([]byte{0, 0, 0x10, 0}, ftyp[4:]...))), port.VideoCauseStructure},
		{"mensagem system", port.Message{Role: port.RoleSystem, Videos: []port.VideoPart{aos535Video(mp4)}}, port.VideoCauseRole},
		{"mensagem assistant", port.Message{Role: port.RoleAssistant, Videos: []port.VideoPart{aos535Video(mp4)}}, port.VideoCauseRole},
		{"mensagem tool", port.Message{Role: port.RoleTool, ToolCallID: "c1", Videos: []port.VideoPart{aos535Video(mp4)}}, port.VideoCauseRole},
		{"dois videos na mesma mensagem", aos535User(aos535Video(mp4), aos535Video(mp4)), port.VideoCauseMoreThanOne},
		{"segundo video invalido", aos535User(aos535Video(mp4), port.VideoPart{MediaType: "text/plain", Data: mp4}), port.VideoCauseMediaType},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			_, err := aos535Pedido(port.Message{Role: port.RoleSystem, Content: "s"}, c.msg).Normalize()
			aos535QueroCausa(t, "Normalize", err, c.causa)
			// A mensagem de erro é vocabulário fechado: não repete o tipo declarado nem os bytes.
			for _, p := range []string{"webm", "quicktime", "MDAT-aos535", "qt  "} {
				if strings.Contains(err.Error(), p) {
					t.Fatalf("o erro repete conteudo do pedido (%q): %v", p, err)
				}
			}
			// O wire, chamado sem Normalize, também não escreve a parte inválida.
			_, err = aos535Pedido(c.msg).MarshalWire(false)
			aos535QueroCausa(t, "MarshalWire", err, c.causa)
		})
	}
}

// As regras do pedido inteiro: dois vídeos em mensagens diferentes, vídeo com imagens (na mesma
// mensagem e em mensagens diferentes), e o tecto agregado — a soma das partes de vídeo de todas as
// mensagens, que se confere antes da contagem.
func TestAOS535_Pedido_UmVideoSemImagensETectoAgregado(t *testing.T) {
	t.Parallel()
	mp4 := aos535MP4()
	png := port.ImagePart{MediaType: port.ImageMediaPNG, Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'x'}}
	metade := aos535MP4DeTamanho(port.MaxVideoBytes / 2)
	casos := []struct {
		nome  string
		msgs  []port.Message
		causa string
	}{
		{"dois videos em duas mensagens", []port.Message{aos535User(aos535Video(mp4)), aos535User(aos535Video(mp4))}, port.VideoCauseMoreThanOne},
		{"dois videos que somam o tecto exacto", []port.Message{aos535User(aos535Video(metade)), aos535User(aos535Video(metade))}, port.VideoCauseMoreThanOne},
		{"dois videos um byte acima do tecto", []port.Message{aos535User(aos535Video(metade)), aos535User(aos535Video(aos535MP4DeTamanho(port.MaxVideoBytes/2 + 1)))}, port.VideoCauseTotalTooLarge},
		{"video e imagem na mesma mensagem", []port.Message{{Role: port.RoleUser, Videos: []port.VideoPart{aos535Video(mp4)}, Images: []port.ImagePart{png}}}, port.VideoCauseWithImages},
		{"video e imagem em mensagens diferentes", []port.Message{aos535User(aos535Video(mp4)), {Role: port.RoleUser, Images: []port.ImagePart{png}}}, port.VideoCauseWithImages},
		{"imagem antes do video", []port.Message{{Role: port.RoleUser, Images: []port.ImagePart{png}}, aos535User(aos535Video(mp4))}, port.VideoCauseWithImages},
	}
	for _, c := range casos {
		_, errN := aos535Pedido(c.msgs...).Normalize()
		aos535QueroCausa(t, c.nome+": Normalize", errN, c.causa)
		_, errW := aos535Pedido(c.msgs...).MarshalWire(false)
		aos535QueroCausa(t, c.nome+": MarshalWire", errW, c.causa)
	}
}

// O TECTO no limite exacto: MaxVideoBytes passa, MaxVideoBytes+1 recusa. Com um vídeo por pedido,
// o tecto agregado coincide com este; os dois estão presos acima.
func TestAOS535_Tecto_LimiteExacto(t *testing.T) {
	t.Parallel()
	if port.MaxVideoBytes != 4<<20 {
		t.Fatalf("MaxVideoBytes = %d; o AOS-534 mediu o proxy com 4 MiB crus — subir exige nova medida", port.MaxVideoBytes)
	}
	noTecto := aos535Pedido(aos535User(aos535Video(aos535MP4DeTamanho(port.MaxVideoBytes))))
	if _, err := noTecto.Normalize(); err != nil {
		t.Fatalf("um video de exactamente %d bytes foi recusado: %v", port.MaxVideoBytes, err)
	}
	if _, err := noTecto.MarshalWire(false); err != nil {
		t.Fatalf("MarshalWire recusou um video no tecto: %v", err)
	}
	acima := aos535Pedido(aos535User(aos535Video(aos535MP4DeTamanho(port.MaxVideoBytes + 1))))
	_, err := acima.Normalize()
	aos535QueroCausa(t, "Normalize", err, port.VideoCauseTooLarge)
	_, err = acima.MarshalWire(false)
	aos535QueroCausa(t, "MarshalWire", err, port.VideoCauseTooLarge)
}

// As formas aceites: a marca de cada família aceite, a `moov` antes da `mdat` com caixas
// intermédias, o tamanho largo (64 bits), a caixa de tamanho 0 no fim, e vídeo sem texto.
func TestAOS535_Normalize_AceitaAsFormasValidas(t *testing.T) {
	t.Parallel()
	moov := aos535Caixa("moov", nil)
	mdat := aos535Caixa("mdat", aos535Corpo)
	largo := make([]byte, 16, 16+len(aos535Corpo))
	binary.BigEndian.PutUint32(largo, 1)
	copy(largo[4:], "mdat")
	binary.BigEndian.PutUint64(largo[8:], uint64(16+len(aos535Corpo)))
	largo = append(largo, aos535Corpo...)
	ateAoFim := append([]byte{0, 0, 0, 0}, append([]byte("mdat"), aos535Corpo...)...)
	formas := map[string][]byte{"o mp4 do teste": aos535MP4()}
	for _, marca := range []string{"isom", "iso2", "mp41", "mp42", "avc1"} {
		formas["marca "+marca] = bytes.Join([][]byte{aos535Ftyp(marca), moov, mdat}, nil)
	}
	formas["caixas intermedias"] = bytes.Join([][]byte{aos535Ftyp("isom"), aos535Caixa("free", nil), moov, aos535Caixa("udta", []byte("x")), mdat, aos535Caixa("mdat", nil)}, nil)
	formas["tamanho largo"] = bytes.Join([][]byte{aos535Ftyp("isom"), moov, largo}, nil)
	formas["caixa ate ao fim"] = bytes.Join([][]byte{aos535Ftyp("isom"), moov, ateAoFim}, nil)
	for nome, b := range formas {
		req := aos535Pedido(port.Message{Role: port.RoleUser, Videos: []port.VideoPart{aos535Video(b)}})
		out, err := req.Normalize()
		if err != nil {
			t.Fatalf("%s: Normalize recusou: %v", nome, err)
		}
		if !out.HasVideos() || len(out.Messages[0].Videos) != 1 {
			t.Fatalf("%s: Normalize perdeu o video", nome)
		}
		if _, err := req.MarshalWire(false); err != nil {
			t.Fatalf("%s: MarshalWire recusou: %v", nome, err)
		}
	}
	if (port.ChatRequest{Messages: []port.Message{{Role: port.RoleUser, Videos: []port.VideoPart{}}}}).HasVideos() {
		t.Fatal("HasVideos num pedido sem videos")
	}
}

// SEM VÍDEO, OS BYTES DE SEMPRE. O literal é o do AOS-530, escrito à mão na forma de antes; uma
// fatia de vídeos vazia (não nil) também não muda nada.
func TestAOS535_MarshalWire_SemVideo_BytesDeSempre(t *testing.T) {
	t.Parallel()
	const quero = `{"model":"gpt-4o","messages":[{"role":"system","content":"s"},{"role":"user","content":"ola"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"doc_read","arguments":"{}"}}]},` +
		`{"role":"tool","content":"r","tool_call_id":"c1"}],"max_tokens":7}`
	for _, vids := range [][]port.VideoPart{nil, {}} {
		msgs := []port.Message{
			{Role: port.RoleSystem, Content: "s"},
			{Role: port.RoleUser, Content: "ola", Videos: vids},
			{Role: port.RoleAssistant, ToolCalls: []port.ToolCall{{ID: "c1", Type: "function", Function: port.FunctionCall{Name: "doc_read", Arguments: "{}"}}}},
			{Role: port.RoleTool, Content: "r", ToolCallID: "c1"},
		}
		wire, err := port.ChatRequest{Model: "gpt-4o", Messages: msgs, MaxTokens: 7}.MarshalWire(false)
		if err != nil {
			t.Fatalf("MarshalWire: %v", err)
		}
		if string(wire) != quero {
			t.Fatalf("o pedido sem video mudou:\n veio:  %s\n quero: %s", wire, quero)
		}
	}
}

// COM VÍDEO: só a mensagem que o leva muda de forma — `content` em partes, o texto primeiro e o
// vídeo numa data URL `video/mp4` com os bytes exactos, a forma que o AOS-534 mediu.
func TestAOS535_MarshalWire_ComVideo_PartesNaFormaMedida(t *testing.T) {
	t.Parallel()
	mp4 := aos535MP4()
	req := port.ChatRequest{Model: "video-visao", MaxTokens: 8192, Messages: []port.Message{
		{Role: port.RoleSystem, Content: "instrucao"},
		{Role: port.RoleUser, Content: "descreve", Name: "u", Videos: []port.VideoPart{aos535Video(mp4)}},
	}}
	for _, stream := range []bool{false, true} {
		wire, err := req.MarshalWire(stream)
		if err != nil {
			t.Fatalf("MarshalWire: %v", err)
		}
		quero := `{"model":"video-visao","messages":[{"role":"system","content":"instrucao"},` +
			`{"role":"user","content":[{"type":"text","text":"descreve"},` +
			`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + base64.StdEncoding.EncodeToString(mp4) + `"}}],"name":"u"}]`
		if stream {
			quero += `,"stream":true`
		}
		quero += `,"max_tokens":8192}`
		if string(wire) != quero {
			t.Fatalf("stream=%v:\n veio:  %s\n quero: %s", stream, wire, quero)
		}
		var corpo struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(wire, &corpo); err != nil {
			t.Fatalf("o wire nao e JSON: %v", err)
		}
		var partes []struct {
			VideoURL struct {
				URL string `json:"url"`
			} `json:"video_url"`
		}
		if err := json.Unmarshal(corpo.Messages[1].Content, &partes); err != nil || len(partes) != 2 {
			t.Fatalf("partes: %v (%d)", err, len(partes))
		}
		_, dados, _ := strings.Cut(partes[1].VideoURL.URL, "data:video/mp4;base64,")
		if cru, err := base64.StdEncoding.DecodeString(dados); err != nil || !bytes.Equal(cru, mp4) {
			t.Fatal("os bytes do video nao chegaram exactos ao wire")
		}
	}
	// Sem texto, só a parte de vídeo.
	wire, err := aos535Pedido(port.Message{Role: port.RoleUser, Videos: []port.VideoPart{aos535Video(mp4)}}).MarshalWire(false)
	if err != nil || !strings.Contains(string(wire), `"content":[{"type":"video_url"`) {
		t.Fatalf("sem texto, a primeira parte tinha de ser o video: %v %s", err, wire)
	}
	// O raciocínio continua a não sair, também numa mensagem com vídeo.
	comRaciocinio := aos535Pedido(port.Message{Role: port.RoleUser, ReasoningContent: "segredo-do-raciocinio", Videos: []port.VideoPart{aos535Video(mp4)}})
	if wire, _ := comRaciocinio.MarshalWire(false); strings.Contains(string(wire), "segredo-do-raciocinio") {
		t.Fatalf("o raciocinio saiu numa mensagem com video: %s", wire)
	}
}

// Um vídeo numa mensagem com estado marcado para sair não se escreve: nunca se descarta em
// silêncio.
func TestAOS535_MarshalWire_VideoComEstado_Recusa(t *testing.T) {
	t.Parallel()
	req := aos535Pedido(port.Message{Role: port.RoleUser, State: &port.MessageState{Return: true}, Videos: []port.VideoPart{aos535Video(aos535MP4())}})
	_, err := req.MarshalWire(false)
	aos535QueroCausa(t, "MarshalWire", err, port.VideoCauseRole)
}

// AS RESPOSTAS CONTINUAM SÓ TEXTO, e uma mensagem lida de JSON nunca traz vídeo.
func TestAOS535_Resposta_ContinuaSoTexto(t *testing.T) {
	t.Parallel()
	corpo := `{"choices":[{"message":{"role":"assistant","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAAA"}}]}}]}`
	if _, err := port.UnmarshalChatResponse([]byte(corpo)); !errors.Is(err, port.ErrContentPartNotText) {
		t.Fatalf("uma resposta com uma parte de video tinha de ser recusada: %v", err)
	}
	var m port.Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"x","Videos":[{"MediaType":"video/mp4","Data":"AAAA"}],"videos":[1]}`), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.Videos != nil {
		t.Fatalf("uma mensagem lida de JSON trouxe video: %v", m.Videos)
	}
}

// OS BYTES NÃO SAEM POR OUTRA VIA: nem pelo `encoding/json` da mensagem, do pedido ou da parte,
// nem por `fmt` em nenhum verbo (`%#v` incluído, nem em hex nem como literal Go de bytes). Só o
// tipo, o tamanho e o digest.
func TestAOS535_BytesSoNoWire(t *testing.T) {
	t.Parallel()
	mp4 := aos535MP4()
	parte := aos535Video(mp4)
	msg := aos535User(parte)
	req := aos535Pedido(msg)
	b64 := base64.StdEncoding.EncodeToString(mp4)
	b64Corpo := base64.StdEncoding.EncodeToString(aos535Corpo)
	proibidos := []string{b64[:16], b64Corpo[:16], string(aos535Corpo), hex.EncodeToString(aos535Corpo[:8]),
		"0x4d, 0x44", "[]uint8", "[]byte", "ftyp", "moov"}

	var textos []string
	for _, v := range []any{msg, &msg, req, parte, &parte, []port.VideoPart{parte}} {
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
		for _, p := range proibidos {
			if strings.Contains(s, p) {
				t.Fatalf("os bytes do video sairam fora do wire (%q): %s", p, s)
			}
		}
	}
	if got, quero := parte.String(), `VideoPart{media_type="video/mp4",bytes=`+fmt.Sprint(len(mp4))+`,`+parte.Digest()+`}`; got != quero {
		t.Fatalf("String = %s; quero %s", got, quero)
	}
	if fmt.Sprintf("%#v", parte) != parte.String() {
		t.Fatalf("GoString tinha de ser o String: %#v", parte)
	}
	cru, _ := json.Marshal(parte)
	if string(cru) != `{"media_type":"video/mp4","bytes":`+fmt.Sprint(len(mp4))+`,"sha256":"`+parte.Digest()+`"}` || len(parte.Digest()) != len("sha256:")+64 {
		t.Fatalf("a parte serializada tinha de levar so o tipo, o tamanho e o digest: %s", cru)
	}
	// O pedido com o vídeo é o único sítio onde os bytes aparecem.
	if wire, err := req.MarshalWire(false); err != nil || !strings.Contains(string(wire), b64) {
		t.Fatalf("o wire tinha de levar os bytes: %v", err)
	}
	// E um erro de validação também não os leva.
	_, err := aos535Pedido(aos535User(aos535Video(append(aos535Caixa("mdat", aos535Corpo), mp4...)))).Normalize()
	if err == nil || strings.Contains(err.Error(), string(aos535Corpo)) || strings.Contains(err.Error(), b64Corpo[:16]) {
		t.Fatalf("o erro leva os bytes: %v", err)
	}
}
