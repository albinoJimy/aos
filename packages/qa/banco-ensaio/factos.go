package bancoensaio

import (
	"strings"
	"unicode"
)

// A REGRA DOS FACTOS (AOS-525). Um facto está na saída quando ocorre nela depois de os dois
// lados serem DOBRADOS: sem acentos, sem distinção de maiúsculas, com qualquer sequência de
// espaços (incluindo quebras de linha e espaços não separáveis) reduzida a um espaço e sem os
// das pontas, e com os hífenes tipográficos levados ao hífen simples.
//
// Porquê: a regra anterior era a ocorrência literal, e os documentos sintéticos estão escritos
// sem acentos («Tomas Albergaria-Pinto»). Um modelo que escreve «Tomás» — a grafia certa do
// nome — era contado como tendo perdido o facto. Medido a 2026-10-13, com as respostas
// guardadas de uma qualificação real: 12 ausências em 56 runs, e as 12 eram só o acento.
//
// O que a dobra NÃO perdoa: um nome trocado, um número diferente, um hífen a mais ou a menos,
// uma letra em falta. Continua a ser verificação de factos sobre dados nossos, sem juiz
// probabilístico.
//
// LIMITES CONHECIDOS — grafia que continua a contar como ausência, por decisão: um número
// escrito com separador de milhares («2,356» ou «2 356» por «2356»), porque tirá-lo faria
// casar números diferentes («2.356» é outro número em inglês); formatação a meio do facto
// (negrito só no primeiro nome); a ordem trocada («Pinto, Tomás»). A taxa é um substituto, e
// lê-se com as respostas guardadas ao lado (`--guardar-respostas`).

// semAcento leva cada letra acentuada do português e das línguas vizinhas à sua letra base.
// É uma tabela fechada, e não a decomposição Unicode, porque o módulo não tem dependências
// fora da biblioteca-padrão; uma letra pré-composta que não esteja aqui compara-se como está.
// Na forma decomposta (letra seguida da marca), a marca cai sempre, esteja a letra aqui ou não.
var semAcento = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ç': 'c', 'ñ': 'n',
	// Os hífenes e travessões que um modelo põe num nome composto ou num código, e o sinal de
	// menos: todos valem o hífen simples dos documentos.
	'\u2010': '-', '\u2011': '-', '\u2012': '-', '\u2013': '-', '\u2014': '-', '\u2015': '-', '\u2212': '-',
}

// dobrarParaComparar devolve o texto na forma em que os factos se comparam.
func dobrarParaComparar(s string) string { return dobrar(s, true) }

// dobrarComAcentos é a dobra SEM tirar os acentos (AOS-529): maiúsculas, espaços e hífenes
// dobram-se como na regra, mas «Évora» e «Evora» ficam diferentes. Serve só para medir, à parte
// da regra, um motor de OCR que perde os acentos.
func dobrarComAcentos(s string) string { return dobrar(s, false) }

func dobrar(s string, tirarAcentos bool) string {
	var b strings.Builder
	b.Grow(len(s))
	espaco := false
	for _, r := range s {
		// Um acento escrito à parte da letra (forma decomposta) cai, e a letra fica.
		if tirarAcentos && unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsSpace(r) {
			espaco = true
			continue
		}
		if espaco && b.Len() > 0 {
			b.WriteByte(' ')
		}
		espaco = false
		r = unicode.ToLower(r)
		if base, ok := semAcento[r]; ok && (tirarAcentos || base == '-') {
			r = base
		}
		b.WriteRune(r)
	}
	return b.String()
}

// factoNaSaida diz se o facto ocorre na saída, pela regra dos factos. Um facto que, dobrado,
// fica vazio não ocorre em saída nenhuma: sem isto casava com todas.
func factoNaSaida(saida, facto string) bool {
	dobrado := dobrarParaComparar(facto)
	return dobrado != "" && strings.Contains(dobrarParaComparar(saida), dobrado)
}

// verificarFactos dá o resultado da verificação de factos de um run concluído: presentes só
// se TODOS os factos do nó estiverem na saída.
func verificarFactos(saida string, factos []string) string {
	for _, f := range factos {
		if !factoNaSaida(saida, f) {
			return FactosAusentes
		}
	}
	return FactosPresentes
}

// factoComAcentosNaSaida diz se o facto ocorre na saída com os acentos exactos (AOS-529).
func factoComAcentosNaSaida(saida, facto string) bool {
	dobrado := dobrarComAcentos(facto)
	return dobrado != "" && strings.Contains(dobrarComAcentos(saida), dobrado)
}
