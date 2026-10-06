package modelgateway

import "unicode/utf8"

// neutralizarQuaseCabecalhos estende, na projecção nativa 1.1.0, o escape de corpo do kernel às
// linhas que se LÊEM como a abrir por '<' sem o fazerem em bytes (revisão do AOS-504, I1).
//
// # O DEFEITO QUE FECHA
//
// O kernel escapa um '<' ou um '\' no byte EXACTO a seguir a uma quebra de linha. Um corpo
// untrusted com a linha ` </plan_input>` — um espaço, um TAB, um BOM, um ZWSP, um NBSP, um NUL,
// um soft hyphen à frente — chegava cru ao pedido. Na 1.0.0 isso era só texto parecido com um
// cabeçalho; na 1.1.0 o protocolo ensina ao modelo que `</plan_input>` FECHA o âmbito do
// untrusted e que um `objective` a seguir é a tarefa, pelo que a quase-forja ganhava alavanca.
//
// # A REGRA
//
// Por linha do corpo: se o primeiro carácter VISÍVEL da linha é '<' ou '\' e vem atrás de pelo
// menos um carácter invisível ([runeInvisivel]), recebe um '\' à frente — o mesmo escape do
// kernel, no sítio onde um leitor vê o princípio da linha. Sem prefixo invisível não se toca:
// essa linha já foi escapada pelo kernel, e escapá-la outra vez mudava-lhe o sentido.
//
// «Linha» é a do layout 1.4.0 do kernel — o único que a projecção nativa cobre
// ([projecaoNativaSuporta]): abre no princípio do corpo e a seguir a '\n', '\r', '\v', '\f',
// U+0085, U+2028 e U+2029.
//
// # É INJECTIVA
//
// Pelo argumento do kernel. Só se insere '\' à frente do primeiro visível de uma linha com
// prefixo invisível, e todo o '\' que já estivesse nessa posição recebe outro: na saída, um '\'
// nessa posição é sempre o inserido. O byte inserido não é quebra nem invisível, logo as linhas
// e os prefixos da saída são os da entrada.
//
// # O QUE NÃO É
//
// Não é uma fronteira de autoridade, como o escape do kernel não é: a separação de privilégio é
// do Reference Monitor. E não toca no tail, no `prompt_hash` nem na 1.0.0 — é parte da função
// de projecção 1.1.0, aplicada ao que o kernel já renderizou. O que o modelo vê num corpo já
// diferia dos bytes do conteúdo (o escape do kernel); o digest de um `plan_input` é do conteúdo
// e vai no cabeçalho, e nada confere os bytes projectados contra ele.
//
// Devolve o próprio argumento quando não há nada a escapar.
func neutralizarQuaseCabecalhos(corpo []byte) []byte {
	var out []byte
	copiado := 0 // corpo[:copiado] já está em out
	noPrefixo, invisiveis := true, 0
	for i := 0; i < len(corpo); {
		r, n := rune(corpo[i]), 1
		if r >= utf8.RuneSelf {
			r, n = utf8.DecodeRune(corpo[i:])
			if r == utf8.RuneError && n == 1 {
				// Byte que não é UTF-8: vai para o wire como U+FFFD, que se vê.
				noPrefixo = false
				i++
				continue
			}
		}
		switch {
		case quebraDeLinha(r):
			noPrefixo, invisiveis = true, 0
		case !noPrefixo:
		case runeInvisivel(r):
			invisiveis++
		default:
			if invisiveis > 0 && (r == '<' || r == '\\') {
				if out == nil {
					out = make([]byte, 0, len(corpo)+8)
				}
				out = append(out, corpo[copiado:i]...)
				out = append(out, '\\')
				copiado = i
			}
			noPrefixo = false
		}
		i += n
	}
	if out == nil {
		return corpo
	}
	return append(out, corpo[copiado:]...)
}

// quebraDeLinha diz se r é uma das quebras de linha do layout 1.4.0 do kernel.
func quebraDeLinha(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	default:
		return false
	}
}

// invisiveis110 é a lista FECHADA dos caracteres que a projecção 1.1.0 trata como invisíveis à
// cabeça de uma linha: as categorias Unicode Z* (separadores), Cc (controlo) e Cf (formato), e
// os que se desenham em branco sem serem dessas categorias (U+034F, os fillers Hangul, U+2800,
// os selectores de variação).
//
// É UMA TABELA CONGELADA, e não uma chamada ao pacote `unicode`: as tabelas do Go mudam com a
// versão do toolchain, e a projecção 1.1.0 tem de dar os mesmos bytes para a mesma vista com
// qualquer uma — é pela versão que um turno gravado se reconstrói. Um teste confere que a
// tabela cobre Z*, Cc e Cf do toolchain em uso; acrescentar-lhe um intervalo muda bytes e
// exige versão nova da projecção.
var invisiveis110 = [...][2]rune{
	{0x0000, 0x0020},
	{0x007F, 0x00A0},
	{0x00AD, 0x00AD},
	{0x034F, 0x034F},
	{0x0600, 0x0605},
	{0x061C, 0x061C},
	{0x06DD, 0x06DD},
	{0x070F, 0x070F},
	{0x0890, 0x0891},
	{0x08E2, 0x08E2},
	{0x115F, 0x1160},
	{0x1680, 0x1680},
	{0x17B4, 0x17B5},
	{0x180E, 0x180E},
	{0x2000, 0x200F},
	{0x2028, 0x202F},
	{0x205F, 0x206F},
	{0x2800, 0x2800},
	{0x3000, 0x3000},
	{0x3164, 0x3164},
	{0xFE00, 0xFE0F},
	{0xFEFF, 0xFEFF},
	{0xFFA0, 0xFFA0},
	{0xFFF9, 0xFFFB},
	{0x110BD, 0x110BD},
	{0x110CD, 0x110CD},
	{0x13430, 0x1343F},
	{0x1BCA0, 0x1BCA3},
	{0x1D173, 0x1D17A},
	{0xE0001, 0xE0001},
	{0xE0020, 0xE007F},
	{0xE0100, 0xE01EF},
}

// runeInvisivel diz se r está em [invisiveis110].
func runeInvisivel(r rune) bool {
	for _, iv := range invisiveis110 {
		if r < iv[0] {
			return false
		}
		if r <= iv[1] {
			return true
		}
	}
	return false
}
