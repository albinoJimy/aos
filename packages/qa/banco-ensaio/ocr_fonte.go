package bancoensaio

import "strings"

// A FONTE DA BATERIA DE OCR (AOS-529). Uma fonte bitmap de 5×7 desenhada aqui, à mão, porque o
// módulo só usa a biblioteca-padrão e a biblioteca-padrão não traz fontes. É ela que faz as
// imagens da bateria serem deterministas: o mesmo texto dá os mesmos píxeis em qualquer máquina.
//
// A célula de cada letra tem 5 colunas e [alturaDaCelula] linhas:
//
//	linhas 0–1    acento de maiúscula            linha 2      folga (ou acento de minúscula)
//	linhas 3–9    corpo (maiúsculas e algarismos; as minúsculas ocupam as linhas 5–9)
//	linhas 10–12  descendentes (g, j, p, q, y, a vírgula e a cedilha)
//	linha 13      folga
//
// Os acentos das minúsculas ocupam as linhas 2–3 e deixam a linha 4 em branco.
//
// LIMITE, DECLARADO NO RELATÓRIO: uma fonte só, bitmap e sem serifas. Um documento impresso tem
// fontes vectoriais; a taxa medida com esta fonte não é a de um documento real.

const (
	larguraDaCelula = 5
	alturaDaCelula  = 14
	// avancoDaLetra e avancoDaLinha são o passo, em células, entre duas letras e duas linhas.
	avancoDaLetra = 6
	avancoDaLinha = 15
)

// corpos são as sete linhas do corpo (linhas 3–9 da célula) de cada letra sem acento.
var corpos = map[rune]string{
	'A': ".###./#...#/#...#/#####/#...#/#...#/#...#",
	'B': "####./#...#/#...#/####./#...#/#...#/####.",
	'C': ".###./#...#/#..../#..../#..../#...#/.###.",
	'D': "####./#...#/#...#/#...#/#...#/#...#/####.",
	'E': "#####/#..../#..../####./#..../#..../#####",
	'F': "#####/#..../#..../####./#..../#..../#....",
	'G': ".###./#...#/#..../#.###/#...#/#...#/.####",
	'H': "#...#/#...#/#...#/#####/#...#/#...#/#...#",
	'I': ".###./..#../..#../..#../..#../..#../.###.",
	'J': "..###/...#./...#./...#./...#./#..#./.##..",
	'K': "#...#/#..#./#.#../##.../#.#../#..#./#...#",
	'L': "#..../#..../#..../#..../#..../#..../#####",
	'M': "#...#/##.##/#.#.#/#.#.#/#...#/#...#/#...#",
	'N': "#...#/#...#/##..#/#.#.#/#..##/#...#/#...#",
	'O': ".###./#...#/#...#/#...#/#...#/#...#/.###.",
	'P': "####./#...#/#...#/####./#..../#..../#....",
	'Q': ".###./#...#/#...#/#...#/#.#.#/#..#./.##.#",
	'R': "####./#...#/#...#/####./#.#../#..#./#...#",
	'S': ".####/#..../#..../.###./....#/....#/####.",
	'T': "#####/..#../..#../..#../..#../..#../..#..",
	'U': "#...#/#...#/#...#/#...#/#...#/#...#/.###.",
	'V': "#...#/#...#/#...#/#...#/#...#/.#.#./..#..",
	'W': "#...#/#...#/#...#/#.#.#/#.#.#/#.#.#/.#.#.",
	'X': "#...#/#...#/.#.#./..#../.#.#./#...#/#...#",
	'Y': "#...#/#...#/.#.#./..#../..#../..#../..#..",
	'Z': "#####/....#/...#./..#../.#.../#..../#####",

	// O zero é sem barra, como na maior parte dos impressos, e mais estreito do que o «O» (quatro
	// colunas em vez de cinco), que é o que o distingue dele.
	'0': ".##../#..#./#..#./#..#./#..#./#..#./.##..",
	'1': "..#../.##../..#../..#../..#../..#../.###.",
	'2': ".###./#...#/....#/...#./..#../.#.../#####",
	'3': "####./....#/....#/.###./....#/....#/####.",
	'4': "...#./..##./.#.#./#..#./#####/...#./...#.",
	'5': "#####/#..../####./....#/....#/#...#/.###.",
	'6': "..##./.#.../#..../####./#...#/#...#/.###.",
	'7': "#####/....#/...#./..#../.#.../.#.../.#...",
	'8': ".###./#...#/#...#/.###./#...#/#...#/.###.",
	'9': ".###./#...#/#...#/.####/....#/...#./.##..",

	'a': "...../...../.###./....#/.####/#...#/.####",
	'b': "#..../#..../#.##./##..#/#...#/#...#/####.",
	'c': "...../...../.###./#..../#..../#...#/.###.",
	'd': "....#/....#/.##.#/#..##/#...#/#...#/.####",
	'e': "...../...../.###./#...#/#####/#..../.###.",
	'f': "..##./.#..#/.#.../###../.#.../.#.../.#...",
	'g': "...../...../.####/#...#/#...#/#...#/.####",
	'h': "#..../#..../#.##./##..#/#...#/#...#/#...#",
	'i': "..#../...../.##../..#../..#../..#../.###.",
	'ı': "...../...../.##../..#../..#../..#../.###.",
	'j': "...#./...../..##./...#./...#./...#./...#.",
	'k': "#..../#..../#..#./#.#../##.../#.#../#..#.",
	'l': ".##../..#../..#../..#../..#../..#../.###.",
	'm': "...../...../##.#./#.#.#/#.#.#/#...#/#...#",
	'n': "...../...../#.##./##..#/#...#/#...#/#...#",
	'o': "...../...../.###./#...#/#...#/#...#/.###.",
	'p': "...../...../####./#...#/#...#/#...#/####.",
	'q': "...../...../.####/#...#/#...#/#...#/.####",
	'r': "...../...../#.##./##..#/#..../#..../#....",
	's': "...../...../.####/#..../.###./....#/####.",
	't': ".#.../.#.../###../.#.../.#.../.#..#/..##.",
	'u': "...../...../#...#/#...#/#...#/#..##/.##.#",
	'v': "...../...../#...#/#...#/#...#/.#.#./..#..",
	'w': "...../...../#...#/#...#/#.#.#/#.#.#/.#.#.",
	'x': "...../...../#...#/.#.#./..#../.#.#./#...#",
	'y': "...../...../#...#/#...#/#...#/#...#/.####",
	'z': "...../...../#####/...#./..#../.#.../#####",

	' ': "...../...../...../...../...../...../.....",
	'.': "...../...../...../...../...../.##../.##..",
	',': "...../...../...../...../...../.##../.##..",
	':': "...../.##../.##../...../.##../.##../.....",
	'-': "...../...../...../#####/...../...../.....",
	'/': "....#/....#/...#./..#../.#.../#..../#....",
	'(': "...#./..#../.#.../.#.../.#.../..#../...#.",
	')': ".#.../..#../...#./...#./...#./..#../.#...",
	'%': "##..#/##..#/...#./..#../.#.../#..##/#..##",
}

// descendentes são as três linhas abaixo da linha de base (linhas 10–12) das letras que as têm.
var descendentes = map[rune]string{
	'g': "....#/....#/.###.",
	'j': "#..#./.##../.....",
	'p': "#..../#..../.....",
	'q': "....#/....#/.....",
	'y': "....#/#...#/.###.",
	',': "..#../.#.../.....",
}

// Os acentos, em duas linhas.
const (
	acentoAgudo       = "...#./..#.."
	acentoGrave       = ".#.../..#.."
	acentoCircunflexo = "..#../.#.#."
	acentoTil         = ".#..#/#.##."
	cedilha           = "..#../.##.."
)

// compostas são as letras acentuadas: a letra base e o acento (ou a cedilha).
var compostas = map[rune]struct {
	base   rune
	acento string
}{
	'á': {'a', acentoAgudo}, 'à': {'a', acentoGrave}, 'â': {'a', acentoCircunflexo}, 'ã': {'a', acentoTil},
	'é': {'e', acentoAgudo}, 'ê': {'e', acentoCircunflexo},
	'í': {'ı', acentoAgudo},
	'ó': {'o', acentoAgudo}, 'ô': {'o', acentoCircunflexo}, 'õ': {'o', acentoTil},
	'ú': {'u', acentoAgudo},
	'ç': {'c', cedilha},
	'Á': {'A', acentoAgudo}, 'À': {'A', acentoGrave}, 'Â': {'A', acentoCircunflexo}, 'Ã': {'A', acentoTil},
	'É': {'E', acentoAgudo}, 'Ê': {'E', acentoCircunflexo},
	'Í': {'I', acentoAgudo},
	'Ó': {'O', acentoAgudo}, 'Ô': {'O', acentoCircunflexo}, 'Õ': {'O', acentoTil},
	'Ú': {'U', acentoAgudo},
	'Ç': {'C', cedilha},
}

// glifo devolve a célula da letra (alturaDaCelula × larguraDaCelula, verdadeiro = tinta) e se a
// fonte a tem. Uma letra que a fonte não tem não se desenha: a bateria recusa-a ao carregar.
func glifo(r rune) ([alturaDaCelula][larguraDaCelula]bool, bool) {
	var c [alturaDaCelula][larguraDaCelula]bool
	base, acento := r, ""
	if comp, ok := compostas[r]; ok {
		base, acento = comp.base, comp.acento
	}
	corpo, ok := corpos[base]
	if !ok {
		return c, false
	}
	pintar(&c, 3, corpo)
	if d, ok := descendentes[base]; ok {
		pintar(&c, 10, d)
	}
	switch {
	case acento == cedilha:
		pintar(&c, 10, acento)
	case acento != "" && base >= 'A' && base <= 'Z':
		pintar(&c, 0, acento)
	case acento != "":
		pintar(&c, 2, acento)
	}
	return c, true
}

// pintar põe na célula, a partir da linha `primeira`, as linhas de `desenho` (separadas por '/').
func pintar(c *[alturaDaCelula][larguraDaCelula]bool, primeira int, desenho string) {
	for i, linha := range strings.Split(desenho, "/") {
		for j := 0; j < larguraDaCelula && j < len(linha); j++ {
			if linha[j] == '#' {
				c[primeira+i][j] = true
			}
		}
	}
}

// fonteTem diz se a fonte desenha todas as letras do texto.
func fonteTem(texto string) (rune, bool) {
	for _, r := range texto {
		if _, ok := glifo(r); !ok {
			return r, false
		}
	}
	return 0, true
}
