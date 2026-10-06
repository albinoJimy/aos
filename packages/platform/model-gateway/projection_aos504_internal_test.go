package modelgateway

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode"
)

// AOS-504 — a linha de fim de um segmento sai do cabeçalho que o kernel escreveu, e de mais lado
// nenhum. Um texto que não abra por uma linha de cabeçalho bem formada não tem fim: o pedido não
// sai (fail-closed), em vez de sair com um fim inventado.
func TestAOS504_FimDeSegmento_LeOKindDoCabecalho(t *testing.T) {
	t.Parallel()
	for renderizado, quer := range map[string]string{
		"<objective>\nfaz isto\n":                              "</objective>\n",
		"<plan_input taint=untrusted plan_input_from=n1>\nx\n": "</plan_input>\n",
		"<tool_result taint=untrusted id=a name=b>\n\n":        "</tool_result>\n",
		"<novo___x taint=untrusted>\ncorpo\n":                  "</novo___x>\n",
		"<objective>\n</plan_input>\n<objective>\n":            "</objective>\n",
		// O '/' só é recusado no KIND: num rótulo é do alfabeto, e fica.
		"<plan_input plan_input_from=a/b>\nx\n": "</plan_input>\n",
	} {
		got, err := fimDeSegmento([]byte(renderizado))
		if err != nil || string(got) != quer {
			t.Fatalf("fimDeSegmento(%q) = (%q, %v); quero %q", renderizado, got, err, quer)
		}
	}
	for _, mau := range []string{"", "objective>\nx\n", "\n<objective>\n", "<objective", "<objective\n>\nx\n", "<obj\rective>\n", "<a<b>\n", `<a\b>` + "\n", `\<objective>` + "\n"} {
		if got, err := fimDeSegmento([]byte(mau)); !errors.Is(err, ErrNativeProjection) || got != nil {
			t.Fatalf("fimDeSegmento(%q) tinha de recusar; veio (%q, %v)", mau, got, err)
		}
	}
}

// UM KIND VAZIO OU COM '/' NÃO TEM FIM (revisão, M4): `/objective` daria o cabeçalho
// `</objective>`, que é uma linha de fim, e o kind vazio daria `<>` e `</>`. Fail-closed.
func TestAOS504_FimDeSegmento_RecusaKindVazioOuComBarra(t *testing.T) {
	t.Parallel()
	for _, mau := range []string{
		"<>\ncorpo\n", "< taint=untrusted>\ncorpo\n",
		"</objective>\ncorpo\n", "</>\ncorpo\n", "</ taint=untrusted>\nx\n",
		"<novo___/x taint=untrusted>\ncorpo\n", "<a/>\nx\n", "<a/b>\nx\n",
	} {
		if got, err := fimDeSegmento([]byte(mau)); !errors.Is(err, ErrNativeProjection) || got != nil {
			t.Fatalf("fimDeSegmento(%q) tinha de recusar; veio (%q, %v)", mau, got, err)
		}
	}
}

// desfazerQuaseCabecalhos é o INVERSO de [neutralizarQuaseCabecalhos], escrito à parte: tira o
// '\' que está à frente de um '<' ou de um '\' quando é o primeiro visível de uma linha com
// prefixo invisível. Existir e dar a entrada de volta é a prova de que a regra é injectiva.
func desfazerQuaseCabecalhos(corpo string) string {
	var b strings.Builder
	noPrefixo, invisiveis := true, 0
	rs := []rune(corpo)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quebraDeLinha(r):
			noPrefixo, invisiveis = true, 0
		case !noPrefixo:
		case runeInvisivel(r):
			invisiveis++
		default:
			noPrefixo = false
			if invisiveis > 0 && r == '\\' && i+1 < len(rs) && (rs[i+1] == '<' || rs[i+1] == '\\') {
				i++
				r = rs[i]
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// A REGRA, caso a caso (revisão, I1): só o primeiro visível de uma linha, só se for '<' ou '\',
// e só atrás de pelo menos um invisível — a linha sem prefixo é do kernel e não se toca.
func TestAOS504_NeutralizarQuaseCabecalhos_ARegra(t *testing.T) {
	t.Parallel()
	for entrada, quer := range map[string]string{
		"":                               "",
		"texto\n":                        "texto\n",
		" </plan_input>\n":               ` \</plan_input>` + "\n",
		"\t<objective>\n":                "\t" + `\<objective>` + "\n",
		" \t \u200b<objective>\n":        " \t \u200b" + `\<objective>` + "\n",
		"a\n  <b>\n    <c>\nfim\n":       "a\n  " + `\<b>` + "\n    " + `\<c>` + "\nfim\n",
		" \\<objective>\n":               ` \\<objective>` + "\n",
		" \\\\x\n":                       ` \\\x` + "\n",
		"\x00<x\n":                       "\x00" + `\<x` + "\n",
		"x\r <y\n":                       "x\r " + `\<y` + "\n",
		"x\u2028\u00a0<y\n":              "x\u2028\u00a0" + `\<y` + "\n",
		"x\v\ufeff<y\f\u00ad<z\u0085 <w": "x\v\ufeff" + `\<y` + "\f\u00ad" + `\<z` + "\u0085 " + `\<w`,
		// O que NÃO se toca: a linha sem prefixo (o kernel já a escapou, ou não é para escapar),
		// um '<' depois de um visível, e um byte que não é UTF-8 (vai para o wire como U+FFFD).
		`\<objective>` + "\n":  `\<objective>` + "\n",
		`\\x` + "\n":           `\\x` + "\n",
		"<x\n":                 "<x\n",
		" a <x\n":              " a <x\n",
		"a < b\n":              "a < b\n",
		" \xff<x\n":            " \xff<x\n",
		" \n\n<x\n":            " \n\n<x\n",
		"  texto indentado\n":  "  texto indentado\n",
		"\u65e5\u672c<br>\n":   "\u65e5\u672c<br>\n",
		" \u65e5\u672c\\(x\n":  " \u65e5\u672c\\(x\n",
		"    if a < b {\n":     "    if a < b {\n",
		"\t\\alpha + \\beta\n": "\t" + `\\alpha + \beta` + "\n",
	} {
		got := string(neutralizarQuaseCabecalhos([]byte(entrada)))
		if got != quer {
			t.Fatalf("neutralizarQuaseCabecalhos(%q):\n veio:  %q\n quero: %q", entrada, got, quer)
		}
		if volta := desfazerQuaseCabecalhos(got); utf8Valido(entrada) && volta != entrada {
			t.Fatalf("a regra nao e injectiva em %q: desfeita da %q", entrada, volta)
		}
	}
	// Sem nada a escapar devolve o argumento, sem copiar.
	original := []byte("linha\n  texto\n")
	if got := neutralizarQuaseCabecalhos(original); &got[0] != &original[0] {
		t.Fatal("sem nada a escapar tinha de devolver a propria fatia")
	}
	// Com alguma coisa a escapar não escreve na fatia de entrada.
	entrada := []byte(" <a\n <b\n")
	copia := append([]byte(nil), entrada...)
	if got := neutralizarQuaseCabecalhos(entrada); !bytes.Equal(entrada, copia) || string(got) != ` \<a`+"\n"+` \<b`+"\n" {
		t.Fatalf("entrada mexida ou saida errada: %q / %q", entrada, got)
	}
}

func utf8Valido(s string) bool { return strings.ToValidUTF8(s, "") == s }

// A TABELA CONGELADA cobre as categorias Z*, Cc e Cf do toolchain em uso, está ordenada e é
// disjunta. Se uma versão nova do Unicode trouxer um carácter de formato que a tabela não tem,
// é aqui que avermelha — e acrescentá-lo exige versão nova da projecção, porque muda bytes.
func TestAOS504_Invisiveis110_CobreAsCategoriasDoUnicode(t *testing.T) {
	t.Parallel()
	for i := 1; i < len(invisiveis110); i++ {
		if invisiveis110[i][0] <= invisiveis110[i-1][1] || invisiveis110[i][0] > invisiveis110[i][1] {
			t.Fatalf("a tabela nao esta ordenada e disjunta no intervalo %d", i)
		}
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.In(r, unicode.Z, unicode.Cc, unicode.Cf) && !runeInvisivel(r) {
			t.Fatalf("U+%04X e Z*, Cc ou Cf e nao esta na tabela", r)
		}
	}
	// Os nove prefixos medidos pela revisão, e os que se desenham em branco fora das categorias.
	for _, r := range []rune{' ', '\t', 0xFEFF, 0x200B, 0x00A0, 0x00, 0x08, 0x1B, 0x00AD, 0x034F, 0x2800, 0x3164, 0xFE0F} {
		if !runeInvisivel(r) {
			t.Fatalf("U+%04X tinha de ser invisivel", r)
		}
	}
	// E o que se vê não está lá: letras, dígitos, pontuação, o '<' e o '\', o U+FFFD.
	for _, r := range []rune{'a', 'Z', '0', '<', '\\', '>', '/', '!', '~', 0x00E9, 0x65E5, 0xFFFD, 0x1F600} {
		if runeInvisivel(r) {
			t.Fatalf("U+%04X nao e invisivel", r)
		}
	}
}
