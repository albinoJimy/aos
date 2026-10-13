package bancoensaio

import (
	"strings"
	"unicode"
)

// A REGRA DOS FACTOS (AOS-525). Um facto está na saída quando ocorre nela depois de os dois
// lados serem DOBRADOS: sem acentos, sem distinção de maiúsculas e com os espaços seguidos
// reduzidos a um.
//
// Porquê: a regra anterior era a ocorrência literal, e os documentos sintéticos estão escritos
// sem acentos («Tomas Albergaria-Pinto»). Um modelo que escreve «Tomás» — a grafia certa do
// nome — era contado como tendo perdido o facto. Medido a 2026-10-13, com as respostas
// guardadas de uma qualificação real: 12 ausências em 56 runs, e as 12 eram só o acento.
//
// O que a dobra NÃO perdoa: um nome trocado, um número diferente, um hífen a mais ou a menos,
// uma letra em falta. Continua a ser verificação de factos sobre dados nossos, sem juiz
// probabilístico.

// semAcento leva cada letra acentuada do português e das línguas vizinhas à sua letra base.
// É uma tabela fechada, e não a decomposição Unicode, porque o módulo não tem dependências
// fora da biblioteca-padrão; uma letra que não esteja aqui compara-se como está.
var semAcento = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ç': 'c', 'ñ': 'n',
}

// dobrarParaComparar devolve o texto na forma em que os factos se comparam.
func dobrarParaComparar(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	espaco := false
	for _, r := range s {
		// Um acento escrito à parte da letra (forma decomposta) cai, e a letra fica.
		if unicode.Is(unicode.Mn, r) {
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
		if base, ok := semAcento[r]; ok {
			r = base
		}
		b.WriteRune(r)
	}
	return b.String()
}

// factoNaSaida diz se o facto ocorre na saída, pela regra dos factos.
func factoNaSaida(saida, facto string) bool {
	return strings.Contains(dobrarParaComparar(saida), dobrarParaComparar(facto))
}
