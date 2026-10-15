package bancoensaio

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
)

// AS IMAGENS DA BATERIA DE OCR (AOS-529). Desenham-se em memória, a partir do texto dos casos,
// sempre da mesma maneira: só aritmética inteira (a rotação usa seno e co-seno em vírgula fixa,
// e o ruído um gerador próprio de semente fixa), para que os píxeis sejam os mesmos em qualquer
// máquina e em qualquer arquitectura. Nada de binário fica no repositório.

// Os formatos e as degradações, em vocabulário fechado.
const (
	FormatoPNG  = "png"
	FormatoJPEG = "jpeg"
	FormatoPDF  = "pdf"

	DegradacaoLimpo          = "limpo"
	DegradacaoRuidoRotacao   = "ruido-rotacao"
	DegradacaoBaixaResolucao = "baixa-resolucao"
)

const (
	// escalaNormal e escalaBaixa são os píxeis por célula da fonte: com 4, a altura das maiúsculas
	// é 28 píxeis (perto de um corpo 10 a 300 ppp); com 2, metade.
	escalaNormal = 4
	escalaBaixa  = 2
	// margemEmCelulas é a margem branca à volta do texto.
	margemEmCelulas = 10
	// qualidadeJPEG é a qualidade do JPEG: baixa de propósito, para os artefactos de compressão.
	qualidadeJPEG = 55
	// ppp é a resolução declarada no PDF: uma página digitalizada a 300 pontos por polegada.
	ppp = 300
	// O ângulo da rotação ligeira (1,5°), em vírgula fixa de 16 bits: seno e co-seno escritos à
	// mão, para não depender da implementação de math.Sin.
	senoDaRotacao   = 1715  // round(sin(1.5°) · 65536)
	cosenoDaRotacao = 65514 // round(cos(1.5°) · 65536)
	// sementeDoRuido fixa o ruído: a mesma imagem tem sempre o mesmo ruído.
	sementeDoRuido = 529
)

// desenharDocumento desenha o documento a preto sobre branco, à escala dada, já suavizado.
func desenharDocumento(d DocumentoOCR, escala int) *image.Gray {
	linhas, grelha := d.linhasParaDesenho()
	colunas := 0
	for _, l := range linhas {
		if n := len([]rune(l)); n > colunas {
			colunas = n
		}
	}
	largura := (2*margemEmCelulas + colunas*avancoDaLetra) * escala
	altura := (2*margemEmCelulas + len(linhas)*avancoDaLinha) * escala
	img := image.NewGray(image.Rect(0, 0, largura, altura))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for n, l := range linhas {
		x0 := margemEmCelulas * escala
		y0 := (margemEmCelulas + n*avancoDaLinha) * escala
		for k, r := range []rune(l) {
			c, _ := glifo(r)
			desenharCelula(img, c, x0+k*avancoDaLetra*escala, y0, escala)
		}
	}
	if grelha != nil {
		desenharGrelha(img, grelha, escala)
	}
	return suavizar(img)
}

func desenharCelula(img *image.Gray, c [alturaDaCelula][larguraDaCelula]bool, x0, y0, escala int) {
	for i := 0; i < alturaDaCelula; i++ {
		for j := 0; j < larguraDaCelula; j++ {
			if !c[i][j] {
				continue
			}
			retangulo(img, x0+j*escala, y0+i*escala, x0+(j+1)*escala, y0+(i+1)*escala)
		}
	}
}

// grelhaDaTabela diz onde ficam as linhas de uma tabela: a primeira linha de texto da tabela, o
// número de linhas dela, e as colunas (em células) onde caem as linhas verticais.
type grelhaDaTabela struct {
	primeiraLinha, linhas int
	verticais             []int
}

func desenharGrelha(img *image.Gray, g *grelhaDaTabela, escala int) {
	espessura := escala / 2
	if espessura < 1 {
		espessura = 1
	}
	topo := (margemEmCelulas+g.primeiraLinha*avancoDaLinha)*escala - escala
	fundo := (margemEmCelulas+(g.primeiraLinha+g.linhas)*avancoDaLinha)*escala - escala
	coluna := func(col int) int { return (margemEmCelulas+col*avancoDaLetra)*escala - escala/2 }
	esquerda, direita := coluna(g.verticais[0]), coluna(g.verticais[len(g.verticais)-1])
	for n := 0; n <= g.linhas; n++ {
		y := topo + n*avancoDaLinha*escala
		retangulo(img, esquerda, y, direita+espessura, y+espessura)
	}
	for _, col := range g.verticais {
		x := coluna(col)
		retangulo(img, x, topo, x+espessura, fundo+espessura)
	}
}

// retangulo pinta de preto o rectângulo [x0,x1)×[y0,y1), cortado pelos limites da imagem. As
// imagens do banco começam todas em (0,0), e os píxeis escrevem-se direto em Pix: é o caminho
// quente do desenho, e o detector de corridas dos testes torna cada chamada por píxel cara.
func retangulo(img *image.Gray, x0, y0, x1, y1 int) {
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, img.Rect.Dx()), min(y1, img.Rect.Dy())
	for y := y0; y < y1; y++ {
		linha := img.Pix[y*img.Stride:]
		for x := x0; x < x1; x++ {
			linha[x] = 0
		}
	}
}

// suavizar aplica uma média 3×3 (as bordas usam os vizinhos que existem): o contorno da letra
// fica em tons de cinzento, como numa impressão digitalizada, e não em degraus puros.
//
// Faz-se em duas passagens (somas na horizontal, depois na vertical), que dão a mesma soma e o mesmo
// número de vizinhos que a janela 3×3: os vizinhos que existem num rectângulo são o produto dos que
// existem em cada eixo.
func suavizar(src *image.Gray) *image.Gray {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewGray(src.Rect)
	horizontal := make([]int, w*h)
	nx := make([]int, w)
	for x := 0; x < w; x++ {
		nx[x] = 1 + min(x, 1) + min(w-1-x, 1)
	}
	for y := 0; y < h; y++ {
		linha := src.Pix[y*src.Stride : y*src.Stride+w]
		for x := 0; x < w; x++ {
			soma := int(linha[x])
			if x > 0 {
				soma += int(linha[x-1])
			}
			if x < w-1 {
				soma += int(linha[x+1])
			}
			horizontal[y*w+x] = soma
		}
	}
	for y := 0; y < h; y++ {
		ny := 1 + min(y, 1) + min(h-1-y, 1)
		for x := 0; x < w; x++ {
			soma := horizontal[y*w+x]
			if y > 0 {
				soma += horizontal[(y-1)*w+x]
			}
			if y < h-1 {
				soma += horizontal[(y+1)*w+x]
			}
			dst.Pix[y*dst.Stride+x] = uint8(soma / (nx[x] * ny)) // #nosec G115 -- media de valores de 0 a 255, por isso de 0 a 255
		}
	}
	return dst
}

// rodar roda a imagem 1,5° em torno do centro, por interpolação bilinear em vírgula fixa. O que
// entra de fora da imagem é branco.
func rodar(src *image.Gray) *image.Gray {
	b := src.Rect
	dst := image.NewGray(b)
	cx, cy := b.Dx()/2, b.Dy()/2
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dx, dy := int64(x-cx), int64(y-cy)
			// Rotação inversa: de onde, na origem, vem este píxel. Coordenadas em 1/65536.
			sx := dx*cosenoDaRotacao + dy*senoDaRotacao + int64(cx)<<16
			sy := -dx*senoDaRotacao + dy*cosenoDaRotacao + int64(cy)<<16
			dst.Pix[y*dst.Stride+x] = bilinear(src, sx, sy)
		}
	}
	return dst
}

func bilinear(src *image.Gray, sx, sy int64) uint8 {
	x0, y0 := sx>>16, sy>>16
	fx, fy := sx&0xffff, sy&0xffff
	w, h := int64(src.Rect.Dx()), int64(src.Rect.Dy())
	em := func(x, y int64) int64 {
		if x < 0 || y < 0 || x >= w || y >= h {
			return 0xff
		}
		return int64(src.Pix[y*int64(src.Stride)+x])
	}
	cima := em(x0, y0)*(0x10000-fx) + em(x0+1, y0)*fx
	baixo := em(x0, y0+1)*(0x10000-fx) + em(x0+1, y0+1)*fx
	return uint8((cima*(0x10000-fy) + baixo*fy) >> 32) // #nosec G115 -- interpolacao com pesos que somam 2^32 sobre valores de 0 a 255: o resultado fica de 0 a 255
}

// geradorDoRuido é um splitmix64: pequeno, sem estado global e igual em todas as máquinas.
type geradorDoRuido struct{ estado uint64 }

func (g *geradorDoRuido) proximo() uint64 {
	g.estado += 0x9e3779b97f4a7c15
	z := g.estado
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// sujar acrescenta ruído: um tom de fundo que varia ±24 por píxel, e 0,4% de píxeis pretos ou
// brancos soltos (sal e pimenta).
func sujar(img *image.Gray, semente uint64) {
	g := geradorDoRuido{estado: semente}
	for i, p := range img.Pix {
		v := int(p) + int(g.proximo()%49) - 24
		switch r := g.proximo() % 1000; {
		case r < 2:
			v = 0
		case r < 4:
			v = 0xff
		}
		if v < 0 {
			v = 0
		}
		if v > 0xff {
			v = 0xff
		}
		img.Pix[i] = uint8(v)
	}
}

// semente devolve a semente do ruído de uma imagem: fixa, e diferente de imagem para imagem.
func sementeDe(id string) uint64 {
	s := uint64(sementeDoRuido)
	for _, c := range []byte(id) {
		s = s*131 + uint64(c)
	}
	return s
}

// pixeisDaImagem devolve os píxeis da imagem — o documento à escala, com a degradação aplicada —
// antes da codificação no formato.
func pixeisDaImagem(d DocumentoOCR, im ImagemOCR) *image.Gray {
	escala := escalaNormal
	if im.Degradacao == DegradacaoBaixaResolucao {
		escala = escalaBaixa
	}
	img := desenharDocumento(d, escala)
	if im.Degradacao == DegradacaoRuidoRotacao {
		img = rodar(img)
		sujar(img, sementeDe(im.ID))
	}
	return img
}

// codificar põe os píxeis no formato da imagem.
func codificar(img *image.Gray, formato string) ([]byte, error) {
	var buf bytes.Buffer
	switch formato {
	case FormatoPNG:
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	case FormatoJPEG:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: qualidadeJPEG}); err != nil {
			return nil, err
		}
	case FormatoPDF:
		return pdfDeUmaPagina(img)
	default:
		return nil, fmt.Errorf("formato desconhecido: %q", formato)
	}
	return buf.Bytes(), nil
}

// pdfDeUmaPagina devolve um PDF de uma página com a imagem por inteiro, como sai de um
// digitalizador: uma imagem em tons de cinzento, comprimida, sem camada de texto. A página tem o
// tamanho da imagem a [ppp] pontos por polegada.
func pdfDeUmaPagina(img *image.Gray) ([]byte, error) {
	var comprimida bytes.Buffer
	z := zlib.NewWriter(&comprimida)
	for y := 0; y < img.Rect.Dy(); y++ {
		if _, err := z.Write(img.Pix[y*img.Stride : y*img.Stride+img.Rect.Dx()]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	pw, ph := w*72/ppp, h*72/ppp
	conteudo := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q", pw, ph)
	objectos := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>", pw, ph),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", w, h, comprimida.Len(), comprimida.String()),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(conteudo), conteudo),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	posicoes := make([]int, len(objectos))
	for i, o := range objectos {
		posicoes[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objectos)+1)
	for _, p := range posicoes {
		fmt.Fprintf(&out, "%010d 00000 n \n", p)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objectos)+1, xref)
	return out.Bytes(), nil
}

// extensao devolve a extensão do ficheiro de um formato.
func extensao(formato string) string {
	if formato == FormatoJPEG {
		return "jpg"
	}
	return strings.ToLower(formato)
}
