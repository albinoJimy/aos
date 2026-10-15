package bancoensaio

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// A BATERIA DE OCR (AOS-529). Uma pasta embebida própria, `bateria-ocr/`, só com o ficheiro dos
// casos: o texto de cada documento sintético, os factos que uma leitura tem de devolver, e as
// imagens a gerar de cada documento (degradação e formato). As imagens desenham-se em memória ao
// carregar (ocr_imagens.go) — o repositório não guarda binários, e quem quiser vê-las escreve-as
// com `aos-ensaio ocr bateria --imagens PASTA`.
//
// O digest da bateria cobre o ficheiro dos casos e os PÍXEIS de cada imagem (antes da codificação
// em PNG, JPEG ou PDF), mais o formato e as dimensões. Assim não muda se a biblioteca-padrão
// passar a comprimir de outra maneira; o digest de cada FICHEIRO, o que o motor de facto lê, vai
// por imagem no relatório.

//go:embed bateria-ocr/casos.json
var ficheirosDaBateriaOCR embed.FS

const pastaDaBateriaOCR = "bateria-ocr"

// BateriaOCR é a bateria de imagens sintéticas da experiência `ocr`.
type BateriaOCR struct {
	Versao string `json:"versao"`
	// Marca é a primeira linha de todos os documentos.
	Marca      string         `json:"marca"`
	Documentos []DocumentoOCR `json:"documentos"`
	Imagens    []ImagemOCR    `json:"imagens"`

	digest string
	media  []MediaOCR
}

// DocumentoOCR é um documento sintético: linhas de texto e, opcionalmente, uma tabela com grelha.
type DocumentoOCR struct {
	ID        string `json:"id"`
	Descricao string `json:"descricao"`
	// Linhas são as linhas de texto, por ordem; a primeira é a marca.
	Linhas []string `json:"linhas"`
	// Tabela, quando presente, desenha-se depois das linhas, com grelha: uma linha por fila, uma
	// célula por coluna.
	Tabela [][]string `json:"tabela,omitempty"`
	// Factos são os valores que a leitura tem de devolver. Cada um ocorre, literalmente, no texto
	// do documento.
	Factos []string `json:"factos"`
}

// ImagemOCR é uma imagem da bateria: um documento, uma degradação e um formato.
type ImagemOCR struct {
	ID         string `json:"id"`
	Documento  string `json:"documento"`
	Degradacao string `json:"degradacao"`
	Formato    string `json:"formato"`
}

// MediaOCR é uma imagem pronta a ler: os bytes do ficheiro, o seu digest e o texto de onde saiu.
type MediaOCR struct {
	ImagemOCR
	// Nome é o nome do ficheiro (`<id>.<extensão>`).
	Nome string
	// Bytes são os bytes do ficheiro, no formato da imagem.
	Bytes []byte
	// Digest é o sha256 dos Bytes.
	Digest string
	// Largura e Altura, em píxeis.
	Largura, Altura int

	// textoDeOrigem é o texto do documento. Só os motores falsos o lêem.
	textoDeOrigem string
}

var (
	degradacoesOCR = map[string]bool{DegradacaoLimpo: true, DegradacaoRuidoRotacao: true, DegradacaoBaixaResolucao: true}
	formatosOCR    = map[string]bool{FormatoPNG: true, FormatoJPEG: true, FormatoPDF: true}
)

// CarregarBateriaOCR lê a bateria de OCR embebida, valida-a, desenha as imagens e calcula o
// digest. Fail-closed: qualquer defeito é [ErrBateria].
func CarregarBateriaOCR() (*BateriaOCR, error) {
	return carregarBateriaOCR(ficheirosDaBateriaOCR, pastaDaBateriaOCR)
}

func carregarBateriaOCR(origem fs.FS, pasta string) (*BateriaOCR, error) {
	cru, err := fs.ReadFile(origem, pasta+"/casos.json")
	if err != nil {
		return nil, fmt.Errorf("%w: casos de OCR ilegiveis: %v", ErrBateria, err)
	}
	var b BateriaOCR
	dec := json.NewDecoder(strings.NewReader(string(cru)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("%w: casos de OCR mal formados: %v", ErrBateria, err)
	}
	if err := b.validar(); err != nil {
		return nil, err
	}
	soma := sha256.New()
	fmt.Fprintf(soma, "casos.json\n%d\n", len(cru))
	soma.Write(cru)
	for _, im := range b.Imagens {
		d, _ := b.documento(im.Documento)
		px := pixeisDaImagem(d, im)
		fmt.Fprintf(soma, "%s\n%s\n%dx%d\n", im.ID, im.Formato, px.Rect.Dx(), px.Rect.Dy())
		soma.Write(px.Pix)
		cod, err := codificar(px, im.Formato)
		if err != nil {
			return nil, fmt.Errorf("%w: imagem %s: %v", ErrBateria, im.ID, err)
		}
		b.media = append(b.media, MediaOCR{
			ImagemOCR: im, Nome: im.ID + "." + extensao(im.Formato), Bytes: cod,
			Digest: digestDe(cod), Largura: px.Rect.Dx(), Altura: px.Rect.Dy(),
			textoDeOrigem: d.Texto(),
		})
	}
	b.digest = "sha256:" + hex.EncodeToString(soma.Sum(nil))
	return &b, nil
}

func (b *BateriaOCR) validar() error {
	if b.Versao == "" || b.Marca == "" || len(b.Documentos) == 0 || len(b.Imagens) == 0 {
		return fmt.Errorf("%w: versao, marca, documentos e imagens sao obrigatorios", ErrBateria)
	}
	docs := map[string]bool{}
	for _, d := range b.Documentos {
		if !identificadorSimples(d.ID) || docs[d.ID] {
			return fmt.Errorf("%w: documento de OCR sem id simples ou repetido (%q)", ErrBateria, d.ID)
		}
		docs[d.ID] = true
		if len(d.Linhas) == 0 || d.Linhas[0] != b.Marca {
			return fmt.Errorf("%w: o documento %s nao abre pela marca de documento sintetico", ErrBateria, d.ID)
		}
		texto := d.Texto()
		if r, ok := fonteTem(strings.ReplaceAll(texto, "\n", "")); !ok {
			return fmt.Errorf("%w: o documento %s tem uma letra que a fonte nao desenha (%q)", ErrBateria, d.ID, r)
		}
		for _, fila := range d.Tabela {
			if len(fila) != len(d.Tabela[0]) {
				return fmt.Errorf("%w: a tabela do documento %s tem filas de tamanhos diferentes", ErrBateria, d.ID)
			}
		}
		if len(d.Factos) == 0 {
			return fmt.Errorf("%w: o documento %s nao tem factos", ErrBateria, d.ID)
		}
		for _, f := range d.Factos {
			if f != strings.TrimSpace(f) || dobrarParaComparar(f) == "" || f == b.Marca {
				return fmt.Errorf("%w: o documento %s tem um facto vazio, com espacos nas pontas ou que e a marca", ErrBateria, d.ID)
			}
			if !strings.Contains(texto, f) {
				return fmt.Errorf("%w: o documento %s tem um facto que nao esta no seu texto (%q)", ErrBateria, d.ID, f)
			}
		}
	}
	vistas := map[string]bool{}
	for _, im := range b.Imagens {
		if !identificadorSimples(im.ID) || vistas[im.ID] {
			return fmt.Errorf("%w: imagem de OCR sem id simples ou repetida (%q)", ErrBateria, im.ID)
		}
		vistas[im.ID] = true
		if !docs[im.Documento] {
			return fmt.Errorf("%w: a imagem %s refere um documento fora da bateria (%q)", ErrBateria, im.ID, im.Documento)
		}
		if !degradacoesOCR[im.Degradacao] || !formatosOCR[im.Formato] {
			return fmt.Errorf("%w: a imagem %s tem uma degradacao ou um formato fora do vocabulario", ErrBateria, im.ID)
		}
	}
	return nil
}

// Digest é o digest da bateria: o ficheiro dos casos e os píxeis de todas as imagens.
func (b *BateriaOCR) Digest() string { return b.digest }

func (b *BateriaOCR) documento(id string) (DocumentoOCR, bool) {
	for _, d := range b.Documentos {
		if d.ID == id {
			return d, true
		}
	}
	return DocumentoOCR{}, false
}

// Media devolve as imagens prontas a ler, pela ordem do ficheiro dos casos.
func (b *BateriaOCR) Media() []MediaOCR {
	out := make([]MediaOCR, len(b.media))
	for i, m := range b.media {
		m.Bytes = append([]byte(nil), m.Bytes...)
		out[i] = m
	}
	return out
}

// Texto devolve o texto do documento: as linhas e, depois, as filas da tabela, com as células
// alinhadas por colunas.
func (d DocumentoOCR) Texto() string {
	linhas, _ := d.linhasParaDesenho()
	return strings.Join(linhas, "\n")
}

// ComAcentos diz se o texto do documento tem letras fora do ASCII.
func (d DocumentoOCR) ComAcentos() bool {
	for _, r := range d.Texto() {
		if r >= 0x80 {
			return true
		}
	}
	return false
}

// linhasParaDesenho devolve as linhas a desenhar e, com tabela, onde fica a grelha. As colunas da
// tabela alinham-se pela célula mais larga de cada uma, com dois espaços de folga.
func (d DocumentoOCR) linhasParaDesenho() ([]string, *grelhaDaTabela) {
	linhas := append([]string(nil), d.Linhas...)
	if len(d.Tabela) == 0 {
		return linhas, nil
	}
	larguras := make([]int, len(d.Tabela[0]))
	for _, fila := range d.Tabela {
		for i, c := range fila {
			if n := len([]rune(c)); n > larguras[i] {
				larguras[i] = n
			}
		}
	}
	g := &grelhaDaTabela{primeiraLinha: len(linhas) + 1, linhas: len(d.Tabela)}
	col := 0
	for _, w := range larguras {
		g.verticais = append(g.verticais, col)
		col += w + 2
	}
	g.verticais = append(g.verticais, col)
	linhas = append(linhas, "")
	for _, fila := range d.Tabela {
		var l strings.Builder
		for i, c := range fila {
			l.WriteString(" ")
			l.WriteString(c)
			l.WriteString(strings.Repeat(" ", larguras[i]+1-len([]rune(c))))
		}
		linhas = append(linhas, l.String())
	}
	return linhas, g
}

// digestDe é o `sha256:<hex>` de uns bytes.
func digestDe(b []byte) string {
	soma := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(soma[:])
}
