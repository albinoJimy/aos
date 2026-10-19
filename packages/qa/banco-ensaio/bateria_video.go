package bancoensaio

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"io/fs"
	"strings"
	"sync"
)

// A BATERIA DE VÍDEO (AOS-537, sub-fase A6.1). Uma pasta embebida própria, `bateria-video/`, só
// com o ficheiro dos casos: cada vídeo é uma sequência de CENAS estáticas — um fundo de cor pura,
// linhas de texto (na fonte bitmap do OCR, ocr_fonte.go) e grupos de formas geométricas — e os
// FACTOS que uma descrição do vídeo tem de trazer. Os fotogramas desenham-se em memória ao
// carregar (video_fotogramas.go), sempre da mesma maneira; o MP4 gera-o o ffmpeg, num contentor
// fixado pelo digest (video_ffmpeg.go), só quando um motor precisa dele. O repositório não guarda
// binários.
//
// # OS FACTOS
//
// Cinco tipos, todos verificáveis pelo texto da resposta com a regra dos factos do banco
// (factos.go):
//
//   - `texto` — uma linha de texto que aparece num fotograma;
//   - `injeccao` — uma linha de uma INSTRUÇÃO escrita no fotograma: o facto é transcrevê-la como
//     dado; um modelo que lhe obedece não a transcreve, e perde-a;
//   - `contagem` — `COUNT: <n> <cor> <forma>s`, a linha que a instrução do motor de visão pede;
//   - `cor` — `BACKGROUND <cena>: <cor>`, a cor do fundo de uma cena;
//   - `ordem` — duas linhas de texto, a primeira (`antes`) numa cena anterior à da segunda
//     (`depois`): presente quando as duas estão na resposta e a primeira ocorrência de uma vem
//     antes da da outra.
//
// # O DIGEST
//
// Cobre o ficheiro dos casos e os PÍXEIS de cada cena (antes da codificação), mais as dimensões,
// o ritmo e o número de fotogramas de cada cena. Não depende do ffmpeg: o digest de cada MP4, o
// que o modelo de facto recebe, vai por vídeo no relatório.

//go:embed bateria-video/casos.json
var ficheirosDaBateriaVideo embed.FS

const pastaDaBateriaVideo = "bateria-video"

// Os tipos de facto de um vídeo.
const (
	FactoTexto    = "texto"
	FactoInjeccao = "injeccao"
	FactoContagem = "contagem"
	FactoCor      = "cor"
	FactoOrdem    = "ordem"
)

// TiposDeFactoDoVideo são os tipos de facto, pela ordem do relatório.
var TiposDeFactoDoVideo = []string{FactoTexto, FactoInjeccao, FactoContagem, FactoCor, FactoOrdem}

// Os limites de um vídeo da bateria (decisão do dono para a A6.1: vídeos curtos e pequenos).
const (
	SegundosMinimosDoVideo = 5
	SegundosMaximosDoVideo = 30
	FPSMinimoDoVideo       = 1
	FPSMaximoDoVideo       = 4
	LarguraMinimaDoVideo   = 320
	LarguraMaximaDoVideo   = 640
	AlturaMinimaDoVideo    = 240
	AlturaMaximaDoVideo    = 360
	VideosMinimosDaBateria = 6
	VideosMaximosDaBateria = 10
	// FormasMinimasDeUmGrupo é 2: uma contagem de 1 pede o singular, e a linha COUNT é no plural.
	FormasMinimasDeUmGrupo = 2
	FormasMaximasDeUmGrupo = 9
)

// BateriaVideo é a bateria de vídeos sintéticos da experiência `video`.
type BateriaVideo struct {
	Versao string           `json:"versao"`
	Videos []VideoDaBateria `json:"videos"`

	digest string
	// cenas são os píxeis de cada cena, por vídeo. Partilhados entre as cópias da bateria e só
	// lidos: ninguém os escreve depois do desenho.
	cenas map[string][]*image.RGBA
}

// VideoDaBateria é um vídeo: as dimensões, o ritmo, as cenas e os factos.
type VideoDaBateria struct {
	ID        string         `json:"id"`
	Descricao string         `json:"descricao"`
	Largura   int            `json:"largura"`
	Altura    int            `json:"altura"`
	FPS       int            `json:"fps"`
	Cenas     []CenaDoVideo  `json:"cenas"`
	Factos    []FactoDoVideo `json:"factos"`
}

// CenaDoVideo é uma cena estática: dura `Segundos` e tem um fundo, linhas de texto e formas.
type CenaDoVideo struct {
	Segundos int             `json:"segundos"`
	Fundo    string          `json:"fundo"`
	Texto    []string        `json:"texto,omitempty"`
	Formas   []GrupoDeFormas `json:"formas,omitempty"`
}

// GrupoDeFormas são `Quantas` formas iguais, da mesma cor.
type GrupoDeFormas struct {
	Forma   string `json:"forma"`
	Cor     string `json:"cor"`
	Quantas int    `json:"quantas"`
}

// FactoDoVideo é um facto: `Valor` nos tipos de uma linha; `Antes` e `Depois` no de ordem.
type FactoDoVideo struct {
	Tipo   string `json:"tipo"`
	Valor  string `json:"valor,omitempty"`
	Antes  string `json:"antes,omitempty"`
	Depois string `json:"depois,omitempty"`
}

// Nome é como o facto se diz no relatório (entre os ausentes).
func (f FactoDoVideo) Nome() string {
	if f.Tipo == FactoOrdem {
		return "ORDEM: " + f.Antes + " -> " + f.Depois
	}
	return f.Valor
}

// Segundos é a duração do vídeo.
func (v VideoDaBateria) Segundos() int {
	n := 0
	for _, c := range v.Cenas {
		n += c.Segundos
	}
	return n
}

// Fotogramas é o número de fotogramas do vídeo.
func (v VideoDaBateria) Fotogramas() int { return v.Segundos() * v.FPS }

// LinhaDeContagem é a linha COUNT de um grupo de formas, como a instrução a pede.
func LinhaDeContagem(g GrupoDeFormas) string {
	return fmt.Sprintf("COUNT: %d %s %ss", g.Quantas, g.Cor, g.Forma)
}

// LinhaDeFundo é a linha BACKGROUND de uma cena (numerada de 1), como a instrução a pede.
func LinhaDeFundo(cena int, cor string) string { return fmt.Sprintf("BACKGROUND %d: %s", cena, cor) }

// CarregarBateriaVideo lê a bateria de vídeo embebida, valida-a, desenha as cenas e calcula o
// digest. Fail-closed: qualquer defeito é [ErrBateria].
func CarregarBateriaVideo() (*BateriaVideo, error) {
	return carregarBateriaVideo(ficheirosDaBateriaVideo, pastaDaBateriaVideo)
}

func carregarBateriaVideo(origem fs.FS, pasta string) (*BateriaVideo, error) {
	cru, err := fs.ReadFile(origem, pasta+"/casos.json")
	if err != nil {
		return nil, fmt.Errorf("%w: casos de video ilegiveis: %v", ErrBateria, err)
	}
	var b BateriaVideo
	dec := json.NewDecoder(strings.NewReader(string(cru)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("%w: casos de video mal formados: %v", ErrBateria, err)
	}
	if err := b.validar(); err != nil {
		return nil, err
	}
	d := desenhoDoVideoEmCache(cru, &b)
	if d.err != nil {
		return nil, d.err
	}
	b.digest, b.cenas = d.digest, d.cenas
	return &b, nil
}

// desenhoDoVideo é o que se desenha de um ficheiro de casos: o digest e os píxeis das cenas.
type desenhoDoVideo struct {
	digest string
	cenas  map[string][]*image.RGBA
	err    error
}

// O DESENHO FAZ-SE UMA VEZ POR PROCESSO e por ficheiro de casos, como o da bateria de OCR: é uma
// função pura dos bytes do `casos.json`, e a chave da cache é o sha256 deles.
var (
	desenhosDoVideoMu sync.Mutex
	desenhosDoVideo   = map[[sha256.Size]byte]*desenhoDoVideoUnico{}
)

type desenhoDoVideoUnico struct {
	uma sync.Once
	d   desenhoDoVideo
}

func desenhoDoVideoEmCache(cru []byte, b *BateriaVideo) desenhoDoVideo {
	chave := sha256.Sum256(cru)
	desenhosDoVideoMu.Lock()
	u, ok := desenhosDoVideo[chave]
	if !ok {
		u = &desenhoDoVideoUnico{}
		desenhosDoVideo[chave] = u
	}
	desenhosDoVideoMu.Unlock()
	u.uma.Do(func() { u.d = desenharBateriaVideo(cru, b) })
	return u.d
}

func desenharBateriaVideo(cru []byte, b *BateriaVideo) desenhoDoVideo {
	d := desenhoDoVideo{cenas: map[string][]*image.RGBA{}}
	soma := sha256.New()
	fmt.Fprintf(soma, "casos.json\n%d\n", len(cru))
	soma.Write(cru)
	for _, v := range b.Videos {
		fmt.Fprintf(soma, "%s\n%dx%d\n%d fps\n", v.ID, v.Largura, v.Altura, v.FPS)
		for i, c := range v.Cenas {
			px, err := desenharCena(v, c)
			if err != nil {
				return desenhoDoVideo{err: fmt.Errorf("%w: video %s, cena %d: %v", ErrBateria, v.ID, i+1, err)}
			}
			fmt.Fprintf(soma, "cena %d\n%d fotogramas\n", i+1, c.Segundos*v.FPS)
			soma.Write(px.Pix)
			d.cenas[v.ID] = append(d.cenas[v.ID], px)
		}
	}
	d.digest = "sha256:" + hex.EncodeToString(soma.Sum(nil))
	return d
}

func (b *BateriaVideo) validar() error {
	if b.Versao == "" || len(b.Videos) < VideosMinimosDaBateria || len(b.Videos) > VideosMaximosDaBateria {
		return fmt.Errorf("%w: a bateria de video tem versao e de %d a %d videos", ErrBateria, VideosMinimosDaBateria, VideosMaximosDaBateria)
	}
	vistos := map[string]bool{}
	tipos := map[string]bool{}
	for _, v := range b.Videos {
		if !identificadorSimples(v.ID) || vistos[v.ID] {
			return fmt.Errorf("%w: video sem id simples ou repetido (%q)", ErrBateria, v.ID)
		}
		vistos[v.ID] = true
		if err := v.validar(); err != nil {
			return fmt.Errorf("%w: video %s: %v", ErrBateria, v.ID, err)
		}
		for _, f := range v.Factos {
			tipos[f.Tipo] = true
		}
	}
	for _, t := range TiposDeFactoDoVideo {
		if !tipos[t] {
			return fmt.Errorf("%w: a bateria de video nao tem nenhum facto do tipo %s", ErrBateria, t)
		}
	}
	return nil
}

func (v VideoDaBateria) validar() error {
	switch {
	case v.Largura < LarguraMinimaDoVideo || v.Largura > LarguraMaximaDoVideo || v.Altura < AlturaMinimaDoVideo || v.Altura > AlturaMaximaDoVideo:
		return fmt.Errorf("dimensoes %dx%d fora de %dx%d a %dx%d", v.Largura, v.Altura, LarguraMinimaDoVideo, AlturaMinimaDoVideo, LarguraMaximaDoVideo, AlturaMaximaDoVideo)
	case v.Largura%2 != 0 || v.Altura%2 != 0:
		return fmt.Errorf("dimensoes %dx%d impares: o yuv420p exige pares", v.Largura, v.Altura)
	case v.FPS < FPSMinimoDoVideo || v.FPS > FPSMaximoDoVideo:
		return fmt.Errorf("%d fps fora de %d a %d", v.FPS, FPSMinimoDoVideo, FPSMaximoDoVideo)
	case len(v.Cenas) == 0:
		return fmt.Errorf("sem cenas")
	case v.Segundos() < SegundosMinimosDoVideo || v.Segundos() > SegundosMaximosDoVideo:
		return fmt.Errorf("%d s fora de %d a %d", v.Segundos(), SegundosMinimosDoVideo, SegundosMaximosDoVideo)
	case len(v.Factos) == 0:
		return fmt.Errorf("sem factos")
	}
	// Onde aparece cada linha de texto (a primeira cena), e as linhas COUNT e BACKGROUND que o
	// vídeo tem: os factos têm de ser destes.
	primeiraCena := map[string]int{}
	contagens := map[string]bool{}
	fundos := map[string]bool{}
	grupos := map[string]bool{}
	for i, c := range v.Cenas {
		if c.Segundos < 1 {
			return fmt.Errorf("a cena %d dura menos de 1 s", i+1)
		}
		if _, ok := coresDoVideo[c.Fundo]; !ok {
			return fmt.Errorf("a cena %d tem um fundo fora do vocabulario (%q)", i+1, c.Fundo)
		}
		// Duas cenas seguidas com o mesmo fundo não se distinguem como cenas: a linha BACKGROUND
		// da segunda ficava sem número certo.
		if i > 0 && v.Cenas[i-1].Fundo == c.Fundo {
			return fmt.Errorf("as cenas %d e %d tem o mesmo fundo", i, i+1)
		}
		fundos[LinhaDeFundo(i+1, c.Fundo)] = true
		for _, l := range c.Texto {
			if l != strings.TrimSpace(l) || dobrarParaComparar(l) == "" {
				return fmt.Errorf("a cena %d tem uma linha de texto vazia ou com espacos nas pontas", i+1)
			}
			if r, ok := fonteTem(l); !ok {
				return fmt.Errorf("a cena %d tem uma letra que a fonte nao desenha (%q)", i+1, r)
			}
			if _, ja := primeiraCena[l]; !ja {
				primeiraCena[l] = i
			}
		}
		for _, g := range c.Formas {
			if _, ok := formasDoVideo[g.Forma]; !ok {
				return fmt.Errorf("a cena %d tem uma forma fora do vocabulario (%q)", i+1, g.Forma)
			}
			if _, ok := coresDoVideo[g.Cor]; !ok || g.Cor == c.Fundo {
				return fmt.Errorf("a cena %d tem formas de uma cor fora do vocabulario ou igual ao fundo (%q)", i+1, g.Cor)
			}
			if g.Quantas < FormasMinimasDeUmGrupo || g.Quantas > FormasMaximasDeUmGrupo {
				return fmt.Errorf("a cena %d tem um grupo de %d formas, fora de %d a %d", i+1, g.Quantas, FormasMinimasDeUmGrupo, FormasMaximasDeUmGrupo)
			}
			// A mesma forma e cor em duas cenas tornaria a contagem ambígua (por cena ou no total).
			chave := g.Forma + "/" + g.Cor
			if grupos[chave] {
				return fmt.Errorf("o grupo %s aparece mais de uma vez no video", chave)
			}
			grupos[chave] = true
			contagens[LinhaDeContagem(g)] = true
		}
	}
	vistos := map[string]bool{}
	for _, f := range v.Factos {
		if vistos[f.Nome()] {
			return fmt.Errorf("facto repetido (%q)", f.Nome())
		}
		vistos[f.Nome()] = true
		forma := f.Valor != "" && f.Antes == "" && f.Depois == ""
		if f.Tipo == FactoOrdem {
			forma = f.Valor == "" && f.Antes != "" && f.Depois != ""
		}
		if !forma {
			return fmt.Errorf("o facto %q nao tem a forma do seu tipo (valor; ou antes e depois, so na ordem)", f.Nome())
		}
		switch f.Tipo {
		case FactoTexto, FactoInjeccao:
			if _, ok := primeiraCena[f.Valor]; !ok {
				return fmt.Errorf("o facto %q nao e uma linha de texto do video", f.Valor)
			}
		case FactoContagem:
			if !contagens[f.Valor] {
				return fmt.Errorf("o facto %q nao e a contagem de um grupo de formas do video", f.Valor)
			}
		case FactoCor:
			if !fundos[f.Valor] {
				return fmt.Errorf("o facto %q nao e o fundo de uma cena do video", f.Valor)
			}
		case FactoOrdem:
			a, okA := primeiraCena[f.Antes]
			d, okD := primeiraCena[f.Depois]
			if !okA || !okD || a >= d {
				return fmt.Errorf("o facto %q nao e de duas linhas do video, a primeira numa cena anterior a da segunda", f.Nome())
			}
			// Uma linha contida na outra faria a ordem casar sempre pela mais curta.
			da, dd := dobrarParaComparar(f.Antes), dobrarParaComparar(f.Depois)
			if strings.Contains(da, dd) || strings.Contains(dd, da) {
				return fmt.Errorf("o facto %q tem uma linha contida na outra", f.Nome())
			}
		default:
			return fmt.Errorf("o facto %q tem um tipo fora do vocabulario (%q)", f.Nome(), f.Tipo)
		}
	}
	return nil
}

// Digest é o digest da bateria: o ficheiro dos casos e os píxeis de todas as cenas.
func (b *BateriaVideo) Digest() string { return b.digest }

// Factos é o número de factos da bateria.
func (b *BateriaVideo) Factos() int {
	n := 0
	for _, v := range b.Videos {
		n += len(v.Factos)
	}
	return n
}

// cenasDe devolve os píxeis das cenas de um vídeo.
func (b *BateriaVideo) cenasDe(id string) []*image.RGBA { return b.cenas[id] }

// escreverFotogramas escreve, por ordem, os fotogramas do vídeo em RGBA cru (4 bytes por píxel,
// linha a linha, sem folga): cada cena repetida `segundos × fps` vezes. É o que o ffmpeg lê.
func (b *BateriaVideo) escreverFotogramas(w io.Writer, v VideoDaBateria) error {
	cenas := b.cenasDe(v.ID)
	if len(cenas) != len(v.Cenas) {
		return fmt.Errorf("banco-ensaio: o video %s nao esta desenhado", v.ID)
	}
	for i, c := range v.Cenas {
		for n := 0; n < c.Segundos*v.FPS; n++ {
			if _, err := w.Write(cenas[i].Pix); err != nil {
				return err
			}
		}
	}
	return nil
}
