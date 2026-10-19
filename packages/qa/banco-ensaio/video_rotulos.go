package bancoensaio

import (
	"regexp"
	"strconv"
	"strings"
)

// OS FACTOS DE RÓTULO DO VÍDEO (AOS-537, regra 2 do relatório `video`). Os factos de `contagem`
// (`COUNT: <n> <cor> <forma>s`) e de `cor` (`BACKGROUND <cena>: <cor>`) não se procuram como texto
// literal: lêem-se das linhas rotuladas da resposta, com uma dobra PRÓPRIA do vídeo — a regra comum
// dos factos (factos.go), a do OCR e dos degraus, não muda.
//
// A dobra do vídeo, linha a linha: a regra comum (sem acentos nem maiúsculas, espaços dobrados) e,
// antes dela, a pontuação de markdown (`*`, `_`, `` ` ``, `|`) passa a espaço; o espaço antes de
// `:` cai. Assim `**COUNT:** 4 red circles`, `COUNT : 4 red circles` e a fila de tabela
// `| BACKGROUND 1 | white |` lêem-se como as linhas pedidas, e uma linha `COUNT` pode trazer várias
// contagens (`4 red circles, 2 blue squares`, com vírgula, «and» ou «e»).
//
// AS CONTRADIÇÕES FALHAM O FACTO. Uma cena com duas cores diferentes em linhas `BACKGROUND`, ou
// com hipóteses (`red/blue`, `red or blue`), não tem a sua cor; uma forma e cor com dois números
// (`COUNT` repetido com outro número, `4 or 5 red circles`), também não. Assim, responder com
// todas as cores ou todos os números possíveis não acerta nada.

// As expressões das linhas rotuladas, sobre a linha já dobrada; as cores e as formas são as do
// vocabulário da bateria.
var (
	reRotuloFundo    = regexp.MustCompile(`background ?(\d+) ?:?`)
	reRotuloContagem = regexp.MustCompile(`\bcount\b ?:?`)
	reContagem       = regexp.MustCompile(`((?:\d+ ?(?:/|\bor\b|\bou\b|-|\bto\b) ?)*\d+) (red|green|blue|yellow|white|black) (circle|square|triangle)s?\b`)
	reNumero         = regexp.MustCompile(`\d+`)
	rePalavra        = regexp.MustCompile(`[a-z]+`)
	dobraMarkdown    = strings.NewReplacer("*", " ", "_", " ", "`", " ", "|", " ")
)

// separadoresDeHipotese são as palavras que, entre duas cores, fazem delas hipóteses.
var separadoresDeHipotese = map[string]bool{"or": true, "ou": true, "and": true, "e": true}

// rotulosDoVideo é o que se leu das linhas rotuladas de uma resposta.
type rotulosDoVideo struct {
	// fundos são as cores ditas para cada cena; contagens, os números ditos para cada `cor/forma`.
	fundos    map[int]map[string]bool
	contagens map[string]map[int]bool
}

// dobrarLinhaDoVideo é a dobra própria do vídeo de uma linha.
func dobrarLinhaDoVideo(l string) string {
	s := dobrarParaComparar(dobraMarkdown.Replace(l))
	return strings.ReplaceAll(s, " :", ":")
}

// lerRotulos lê as linhas `BACKGROUND` e `COUNT` de uma resposta (ou de um facto).
func lerRotulos(saida string) rotulosDoVideo {
	r := rotulosDoVideo{fundos: map[int]map[string]bool{}, contagens: map[string]map[int]bool{}}
	for _, crua := range strings.Split(saida, "\n") {
		l := dobrarLinhaDoVideo(crua)
		// Cada rótulo vale até ao próximo rótulo da linha (uma fila de tabela pode ter vários).
		type rotulo struct{ ini, fim, cena int }
		var rs []rotulo
		for _, m := range reRotuloFundo.FindAllStringSubmatchIndex(l, -1) {
			n, _ := strconv.Atoi(l[m[2]:m[3]])
			rs = append(rs, rotulo{m[0], m[1], n})
		}
		for _, m := range reRotuloContagem.FindAllStringIndex(l, -1) {
			rs = append(rs, rotulo{m[0], m[1], -1})
		}
		for _, a := range rs {
			fim := len(l)
			for _, b := range rs {
				if b.ini > a.ini && b.ini < fim {
					fim = b.ini
				}
			}
			valor := l[a.fim:fim]
			if a.cena < 0 {
				r.lerContagens(valor)
			} else {
				r.lerFundo(a.cena, valor)
			}
		}
	}
	return r
}

// lerFundo lê as cores de uma cena: as palavras de cor seguidas, com os separadores de hipótese
// entre elas, depois de no máximo duas palavras que não são cor («is», «dark»).
func (r *rotulosDoVideo) lerFundo(cena int, valor string) {
	palavras := rePalavra.FindAllString(strings.NewReplacer("/", " or ", ",", " or ", ";", " or ").Replace(valor), -1)
	i := 0
	for i < len(palavras) && i < 2 && !eCor(palavras[i]) {
		i++
	}
	for ; i < len(palavras); i++ {
		p := palavras[i]
		switch {
		case eCor(p):
			if r.fundos[cena] == nil {
				r.fundos[cena] = map[string]bool{}
			}
			r.fundos[cena][p] = true
		case separadoresDeHipotese[p]:
		default:
			return
		}
	}
}

// lerContagens lê todas as contagens de um valor `COUNT`; um número com hipóteses dá vários.
func (r *rotulosDoVideo) lerContagens(valor string) {
	for _, m := range reContagem.FindAllStringSubmatch(valor, -1) {
		chave := m[2] + "/" + m[3]
		if r.contagens[chave] == nil {
			r.contagens[chave] = map[int]bool{}
		}
		for _, n := range reNumero.FindAllString(m[1], -1) {
			v, _ := strconv.Atoi(n)
			r.contagens[chave][v] = true
		}
	}
}

func eCor(p string) bool {
	_, ok := coresDoVideo[p]
	return ok
}

// unico diz se o conjunto tem exactamente o valor dado e nada mais.
func unico[T comparable](conjunto map[T]bool, valor T) bool {
	return len(conjunto) == 1 && conjunto[valor]
}

// factoDeRotuloNaSaida diz se um facto de `cor` ou `contagem` está nas linhas rotuladas: a linha
// esperada (lida do próprio facto) é a ÚNICA dita para a sua cena, ou para a sua forma e cor.
func factoDeRotuloNaSaida(saida rotulosDoVideo, f FactoDoVideo) bool {
	quer := lerRotulos(f.Valor)
	switch f.Tipo {
	case FactoCor:
		for cena, cores := range quer.fundos {
			for cor := range cores {
				return unico(saida.fundos[cena], cor)
			}
		}
	case FactoContagem:
		for chave, ns := range quer.contagens {
			for n := range ns {
				return unico(saida.contagens[chave], n)
			}
		}
	}
	return false
}
