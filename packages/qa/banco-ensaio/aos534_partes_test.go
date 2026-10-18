package bancoensaio

// AOS-534 — o falso do estado regista as partes do `content` que lhe chegam, sem os bytes.
// Correm sem Docker.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// aos534MP4 gera um MP4 SINTÉTICO, válido como contentor e não como vídeo: uma caixa `ftyp`
// (`isom`), uma `moov` com uma `mvhd` mínima (escala de 1000 por segundo e a duração dada) e uma
// `mdat` com `n` bytes deterministas. Não tem faixas nem amostras: um leitor não o descodifica.
// Basta para medir o que o proxy faz aos bytes — tem a magia e o tamanho certos —, e um
// fornecedor real recusá-lo-ia.
func aos534MP4(duracaoMs uint32, n int) []byte {
	caixa := func(tipo string, corpo []byte) []byte {
		out := make([]byte, 8, 8+len(corpo))
		binary.BigEndian.PutUint32(out, uint32(8+len(corpo)))
		copy(out[4:], tipo)
		return append(out, corpo...)
	}
	ftyp := []byte("isom\x00\x00\x02\x00isomiso2mp41")
	mvhd := make([]byte, 100)
	// versão 0 e flags (4), criação (4), modificação (4), escala (4), duração (4), ritmo (4),
	// volume (2), reservado (10), matriz (36), pre_defined (24), próxima faixa (4).
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], duracaoMs)
	binary.BigEndian.PutUint32(mvhd[20:], 0x00010000)
	binary.BigEndian.PutUint16(mvhd[24:], 0x0100)
	for i, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		binary.BigEndian.PutUint32(mvhd[36+4*i:], v)
	}
	binary.BigEndian.PutUint32(mvhd[96:], 1)
	dados := make([]byte, n)
	for i := range dados {
		dados[i] = byte(i*31 + 7)
	}
	out := caixa("ftyp", ftyp)
	out = append(out, caixa("moov", caixa("mvhd", mvhd))...)
	return append(out, caixa("mdat", dados)...)
}

// aos534Pedido devolve o corpo de um pedido de chat com texto e uma parte `video_url` com a data
// URL do vídeo dado.
func aos534Pedido(modelo string, video []byte) []byte {
	corpo, _ := json.Marshal(map[string]any{
		"model": modelo, "max_tokens": 256,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": aos534Instrucao},
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(video)}},
		}}},
	})
	return corpo
}

// aos534Instrucao é o texto do pedido; é também uma sentinela — não pode aparecer na forma.
const aos534Instrucao = "S-VIDEO-AOS534 descreve este video numa frase"

func aos534Servir(t *testing.T, f *FalsoDeEstado, corpo []byte) int {
	t.Helper()
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(corpo)))
	return w.Code
}

func TestAOS534_MP4Sintetico_TemAMagiaEADuracao(t *testing.T) {
	v := aos534MP4(2000, 64)
	if string(v[4:8]) != "ftyp" || string(v[8:12]) != "isom" {
		t.Fatalf("sem a magia ftyp/isom: % x", v[:12])
	}
	// As caixas de topo cobrem o ficheiro inteiro, e a mvhd diz 2000/1000 s.
	var tipos []string
	for i := 0; i < len(v); {
		tam := int(binary.BigEndian.Uint32(v[i:]))
		if tam < 8 || i+tam > len(v) {
			t.Fatalf("caixa com tamanho %d em %d (ficheiro de %d)", tam, i, len(v))
		}
		tipos = append(tipos, string(v[i+4:i+8]))
		if string(v[i+4:i+8]) == "moov" {
			mvhd := v[i+8:]
			if string(mvhd[4:8]) != "mvhd" || binary.BigEndian.Uint32(mvhd[20:]) != 1000 || binary.BigEndian.Uint32(mvhd[24:]) != 2000 {
				t.Errorf("mvhd sem a escala 1000 e a duração 2000: % x", mvhd[:28])
			}
		}
		i += tam
	}
	if strings.Join(tipos, ",") != "ftyp,moov,mdat" {
		t.Errorf("caixas de topo = %v", tipos)
	}
	if grande := aos534MP4(2000, aos534BytesGrande); len(grande) < 3<<20 || len(grande) > 6<<20 {
		t.Errorf("o caso grande tem %d bytes, fora de 3 a 6 MiB", len(grande))
	}
}

func TestAOS534_Falso_RegistaAParteDeVideoSemOsBytes(t *testing.T) {
	video := aos534MP4(2000, 4096)
	f := &FalsoDeEstado{FormaDoEstado: FormaOpenRouter}
	if c := aos534Servir(t, f, aos534Pedido("google/gemini", video)); c != http.StatusOK {
		t.Fatalf("o falso respondeu %d", c)
	}
	forma := f.Forma()
	for chave, quer := range map[string]int{"text": 1, "video_url": 1, "video_url.url": 1} {
		if forma.Partes[chave] != quer {
			t.Errorf("Partes[%s] = %d, quer %d (%v)", chave, forma.Partes[chave], quer, forma.Partes)
		}
	}
	soma := sha256.Sum256(video)
	quer := MediaNoFornecedor{Parte: "video_url", Classe: ClasseDataURLVideoMP4, Sha256: hex.EncodeToString(soma[:]), Bytes: len(video)}
	if len(forma.Media) != 1 || forma.Media[0] != quer {
		t.Fatalf("Media = %+v, quer [%+v]", forma.Media, quer)
	}
	// Nem os bytes, nem a URL, nem o texto saem pela forma (o caminho GET do falso).
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequest(http.MethodGet, CaminhoDaForma, nil))
	cru := w.Body.String()
	b64 := base64.StdEncoding.EncodeToString(video)
	for _, proibido := range []string{b64[:40], b64[len(b64)-40:], "data:video", "S-VIDEO-AOS534", string(video[:12])} {
		if strings.Contains(cru, proibido) {
			t.Errorf("a forma deixa sair %q", proibido)
		}
	}
	if !strings.Contains(cru, `"media_no_content"`) || !strings.Contains(cru, quer.Sha256) {
		t.Errorf("a forma não traz a media registada: %s", cru)
	}
}

func TestAOS534_Falso_ClassificaAsOutrasFormas(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nnao-e-uma-imagem")
	corpo, _ := json.Marshal(map[string]any{"model": "m", "messages": []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://exemplo.invalid/v.mp4", "detail": "auto", "segredo": "x"}},
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": "data:video/webm;base64," + base64.StdEncoding.EncodeToString([]byte("webm"))}},
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": "data:video/mp4;base64,@@@nao-e-base64"}},
			map[string]any{"type": "image_url", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
			map[string]any{"type": "file", "file": map[string]any{"file_data": "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("mp4")), "filename": "v.mp4"}},
			map[string]any{"type": "tipo-inventado", "tipo-inventado": map[string]any{"url": "data:video/mp4;base64,AAAA"}},
		}},
		// O vídeo achatado em texto conta à parte; um texto qualquer não conta.
		map[string]any{"role": "user", "content": "olha data:video/mp4;base64,AAAA"},
		map[string]any{"role": "user", "content": "sem media"},
	}})
	f := &FalsoDeEstado{FormaDoEstado: FormaOpenRouter}
	_ = aos534Servir(t, f, corpo)
	forma := f.Forma()
	for chave, quer := range map[string]int{
		"video_url": 3, "video_url.url": 3, "video_url.detail": 1, "video_url.outra": 1, "image_url": 1, "image_url.texto": 1,
		"file": 1, "file.file_data": 1, "file.filename": 1, "outra": 1, ContentTextoComVideo: 1,
	} {
		if forma.Partes[chave] != quer {
			t.Errorf("Partes[%s] = %d, quer %d (%v)", chave, forma.Partes[chave], quer, forma.Partes)
		}
	}
	if _, tem := forma.Partes["video_url.segredo"]; tem {
		t.Errorf("uma chave fora do vocabulário saiu com o nome: %v", forma.Partes)
	}
	classes := make([]string, 0, len(forma.Media))
	for _, m := range forma.Media {
		classes = append(classes, m.Parte+":"+m.Classe)
		if (m.Sha256 == "") != (m.Classe == ClasseURLRemota || m.Classe == ClasseDataURLBase64Invalido || m.Classe == ClasseURLOutra) {
			t.Errorf("sha256 em %+v", m)
		}
	}
	if got := strings.Join(classes, ","); got != "video_url:url_remota,video_url:data_url_video_outro,video_url:data_url_base64_invalido,image_url:data_url_outra,file:data_url_video_mp4" {
		t.Errorf("classes = %s", got)
	}
}

func TestAOS534_Falso_CorpoAcimaDoTectoTemCausaPropria(t *testing.T) {
	f := &FalsoDeEstado{FormaDoEstado: FormaOpenRouter}
	if c := aos534Servir(t, f, bytes.Repeat([]byte(" "), maxCorpoDoFalsoDoEstado+1)); c != http.StatusBadRequest {
		t.Fatalf("código %d, quer 400", c)
	}
	if forma := f.Forma(); forma.Recusas[RecusaCorpoAcimaDoTecto] != 1 || forma.Recusas[RecusaPedidoIlegivel] != 0 {
		t.Errorf("recusas = %v", forma.Recusas)
	}
	// O caso grande do cenário cabe no tecto do falso, com folga para o resto do pedido.
	if n := len(aos534Pedido("m", aos534MP4(2000, aos534BytesGrande))); n > maxCorpoDoFalsoDoEstado/2 {
		t.Errorf("o pedido grande tem %d bytes, perto do tecto do falso", n)
	}
}

func TestAOS534_Falso_SemPartesAFormaNaoMuda(t *testing.T) {
	// Um pedido de sempre (content em texto) não acrescenta nada à forma: os relatórios de antes
	// (AOS-516, AOS-518) não ganham campos.
	f := &FalsoDeEstado{FormaDoEstado: FormaOpenRouter}
	_ = aos534Servir(t, f, []byte(`{"model":"m","messages":[{"role":"user","content":"ola"}]}`))
	cru, _ := json.Marshal(f.Forma())
	if strings.Contains(string(cru), "partes_do_content") || strings.Contains(string(cru), "media_no_content") {
		t.Errorf("a forma ganhou campos num pedido sem partes: %s", cru)
	}
}

func TestAOS534_OResultadoDoSpikeLeSeDoQueChegouAoFornecedor(t *testing.T) {
	const sha = "abc"
	video := func(parte, classe, s string, n int) []MediaNoFornecedor {
		return []MediaNoFornecedor{{Parte: parte, Classe: classe, Sha256: s, Bytes: n}}
	}
	for _, c := range []struct {
		nome   string
		medida map[string]any
		quer   string
	}{
		{"nada chegou", map[string]any{"pedidos_no_fornecedor": 0}, aos534RecusadaPeloProxy},
		{"o falso recusou", map[string]any{"pedidos_no_fornecedor": 1, "recusas_do_falso": map[string]int{RecusaCorpoAcimaDoTecto: 1}}, aos534RecusadaPeloFalso},
		{"intacta", map[string]any{"pedidos_no_fornecedor": 1, "media_no_fornecedor": video("video_url", ClasseDataURLVideoMP4, sha, 10)}, aos534Intacta},
		{"outros bytes", map[string]any{"pedidos_no_fornecedor": 1, "media_no_fornecedor": video("video_url", ClasseDataURLVideoMP4, "def", 10)}, aos534AlteradaPeloProxy},
		{"outro tamanho", map[string]any{"pedidos_no_fornecedor": 1, "media_no_fornecedor": video("video_url", ClasseDataURLVideoMP4, sha, 9)}, aos534AlteradaPeloProxy},
		{"outro tipo", map[string]any{"pedidos_no_fornecedor": 1, "media_no_fornecedor": video("file", ClasseDataURLVideoMP4, sha, 10)}, aos534AlteradaPeloProxy},
		{"descartada", map[string]any{"pedidos_no_fornecedor": 1, "recusas_do_falso": map[string]int{}}, aos534AlteradaPeloProxy},
	} {
		if got := aos534Resultado(c.medida, sha, 10); got != c.quer {
			t.Errorf("%s: %s, quer %s", c.nome, got, c.quer)
		}
	}
}
