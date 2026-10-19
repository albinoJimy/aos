package bancoensaio

import (
	"fmt"
	"image"
	"image/color"
)

// OS FOTOGRAMAS DA BATERIA DE VÍDEO (AOS-537). Cada cena desenha-se uma vez, em memória, em RGBA:
// um fundo de cor pura, as linhas de texto na fonte bitmap do OCR (ocr_fonte.go), sem suavizar, e
// os grupos de formas numa fila por baixo do texto. Só aritmética inteira: os píxeis são os mesmos
// em qualquer máquina. Os fotogramas de uma cena são todos iguais — a cena é estática; o que muda
// entre cenas é o fundo, o texto e as formas.

// coresDoVideo é o vocabulário fechado das cores: as palavras que a instrução do motor de visão
// pede ao modelo. Cores saturadas e afastadas umas das outras, para que o nome não seja ambíguo.
var coresDoVideo = map[string]color.RGBA{
	"red":    {R: 230, G: 25, B: 25, A: 255},
	"green":  {R: 20, G: 170, B: 40, A: 255},
	"blue":   {R: 30, G: 60, B: 220, A: 255},
	"yellow": {R: 250, G: 215, B: 0, A: 255},
	"white":  {R: 255, G: 255, B: 255, A: 255},
	"black":  {R: 0, G: 0, B: 0, A: 255},
}

// coresDoTexto diz a cor do texto sobre cada fundo: preto sobre os claros, branco sobre os escuros.
var coresDoTexto = map[string]string{"red": "white", "green": "black", "blue": "white", "yellow": "black", "white": "black", "black": "white"}

// formasDoVideo é o vocabulário fechado das formas (no singular; a linha COUNT põe-lhes o «s»).
var formasDoVideo = map[string]bool{"circle": true, "square": true, "triangle": true}

// escalaDoVideo devolve os píxeis por célula da fonte: 3 nos vídeos de 240 e 270 linhas (letras de
// 21 píxeis), 4 nos de 360 (28 píxeis) — texto legível depois da compressão e da amostragem do
// fornecedor.
func escalaDoVideo(altura int) int { return altura / 80 }

// TamanhoMinimoDaForma é o lado mínimo, em píxeis, de uma forma desenhada: abaixo dele a bateria
// recusa a cena (as formas não caberiam numa fila).
const TamanhoMinimoDaForma = 16

// desenharCena desenha uma cena do vídeo. Recusa a cena cujo texto ou cujas formas não cabem.
func desenharCena(v VideoDaBateria, c CenaDoVideo) (*image.RGBA, error) {
	escala := escalaDoVideo(v.Altura)
	margem := 4 * escala
	img := image.NewRGBA(image.Rect(0, 0, v.Largura, v.Altura))
	pintarRect(img, 0, 0, v.Largura, v.Altura, coresDoVideo[c.Fundo])
	tinta := coresDoVideo[coresDoTexto[c.Fundo]]
	alturaDoTexto := len(c.Texto) * avancoDaLinha * escala
	for n, l := range c.Texto {
		if larg := 2*margem + len([]rune(l))*avancoDaLetra*escala; larg > v.Largura {
			return nil, fmt.Errorf("a linha %q nao cabe na largura (%d px de %d)", l, larg, v.Largura)
		}
		y0 := margem + n*avancoDaLinha*escala
		for k, r := range []rune(l) {
			g, _ := glifo(r)
			x0 := margem + k*avancoDaLetra*escala
			for i := 0; i < alturaDaCelula; i++ {
				for j := 0; j < larguraDaCelula; j++ {
					if g[i][j] {
						pintarRect(img, x0+j*escala, y0+i*escala, x0+(j+1)*escala, y0+(i+1)*escala, tinta)
					}
				}
			}
		}
	}
	if alturaDoTexto+2*margem > v.Altura {
		return nil, fmt.Errorf("o texto nao cabe na altura")
	}
	total := 0
	for _, g := range c.Formas {
		total += g.Quantas
	}
	if total == 0 {
		return img, nil
	}
	topo := margem
	if len(c.Texto) > 0 {
		topo = margem + alturaDoTexto + margem
	}
	fundo := v.Altura - margem
	vaga := (v.Largura - 2*margem) / total
	lado := min(fundo-topo, vaga*3/4, v.Altura/3)
	if lado < TamanhoMinimoDaForma {
		return nil, fmt.Errorf("as %d formas nao cabem (lado de %d px, minimo %d)", total, lado, TamanhoMinimoDaForma)
	}
	y0 := topo + (fundo-topo-lado)/2
	k := 0
	for _, g := range c.Formas {
		cor := coresDoVideo[g.Cor]
		for n := 0; n < g.Quantas; n++ {
			x0 := margem + k*vaga + (vaga-lado)/2
			desenharForma(img, g.Forma, x0, y0, lado, cor)
			k++
		}
	}
	return img, nil
}

// desenharForma desenha uma forma cheia no quadrado de lado `lado` com o canto em (x0, y0).
func desenharForma(img *image.RGBA, forma string, x0, y0, lado int, cor color.RGBA) {
	switch forma {
	case "square":
		pintarRect(img, x0, y0, x0+lado, y0+lado, cor)
	case "circle":
		// O centro do píxel (x, y), em meios-píxeis, a contar do centro do quadrado.
		for y := 0; y < lado; y++ {
			dy := 2*y + 1 - lado
			for x := 0; x < lado; x++ {
				dx := 2*x + 1 - lado
				if dx*dx+dy*dy <= lado*lado {
					pintarRect(img, x0+x, y0+y, x0+x+1, y0+y+1, cor)
				}
			}
		}
	case "triangle":
		// Isósceles, com o vértice em cima e a base em baixo: a fila y tem y+1 píxeis de largura.
		for y := 0; y < lado; y++ {
			w := y + 1
			esq := x0 + (lado-w)/2
			pintarRect(img, esq, y0+y, esq+w, y0+y+1, cor)
		}
	}
}

// pintarRect pinta o rectângulo [x0,x1)×[y0,y1) com a cor, cortado pelos limites da imagem.
func pintarRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, img.Rect.Dx()), min(y1, img.Rect.Dy())
	for y := y0; y < y1; y++ {
		linha := img.Pix[y*img.Stride:]
		for x := x0; x < x1; x++ {
			p := linha[4*x : 4*x+4 : 4*x+4]
			p[0], p[1], p[2], p[3] = c.R, c.G, c.B, c.A
		}
	}
}
