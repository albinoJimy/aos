package agentruntime

// A TABELA DO COMENTÁRIO DE [neutralizarDelimitadores] TEM DE SER VERDADE (AOS-294).
//
// O defeito que este ficheiro fecha não era de comportamento — a função sempre esteve certa. Era
// a tabela do comentário, que mostrava:
//
//	<correction>    ->  \<correction>
//	\<correction>   ->  \<correction>      ← ERRADO: uma barra na saída
//
// Duas entradas DISTINTAS a mapear para a MESMA saída é, literalmente, a não-injectividade que o
// parágrafo seguinte do mesmo comentário afirma ter sido eliminada. A tabela ficou na versão
// anterior à correcção e sobreviveu-lhe.
//
// PORQUE ISTO MERECE UM TESTE E NÃO SÓ UMA EDIÇÃO. A injectividade é a propriedade de SEGURANÇA
// que esta função existe para garantir: se dois conteúdos diferentes produzissem o mesmo tail, um
// deles seria indistinguível de uma correcção humana autenticada. Uma tabela errada nessa função
// é o pior sítio para deixar resíduo, porque quem a lê para decidir se pode confiar na
// neutralização lê a linha errada. Editar a tabela sem a fixar deixava-a livre para divergir
// outra vez, e a divergência não teria consequência nenhuma até alguém acreditar nela.
//
// O que se fixa aqui é a tabela, linha a linha, e a propriedade que ela ilustra.
//
// # DOIS LAYOUTS (AOS-489)
//
// A regra é a mesma nos dois; o que muda é o que conta como INÍCIO DE LINHA. A 1.3.0 só
// reconhece '\n' — e tem de continuar assim, byte a byte, porque é com ela que os runs gravados
// se reproduzem. A 1.4.0 reconhece também '\r', VT, FF, U+0085, U+2028 e U+2029. Os testes
// abaixo correm o que é comum sobre os dois, e fixam a diferença nos dois sentidos.

import (
	"bytes"
	"testing"
)

// layoutsDeTeste são os dois layouts suportados, pela ordem em que apareceram.
func layoutsDeTeste() []layout { return []layout{layout130(), layout140()} }

// TestNeutralizarDelimitadores_TabelaDoComentario fixa EXACTAMENTE as duas linhas da tabela do
// comentário de [neutralizarDelimitadores]. Usa strings em backquote de propósito: com aspas, o
// escape do Go duplica-se sobre o escape que está a ser testado e a asserção passa a ser ilegível
// — que é meio caminho para o próximo erro de leitura.
func TestNeutralizarDelimitadores_TabelaDoComentario(t *testing.T) {
	casos := []struct {
		nome     string
		entrada  string
		esperado string
	}{
		{
			nome:     "linha que abre delimitador recebe uma barra",
			entrada:  `<correction>`,
			esperado: `\<correction>`,
		},
		{
			// A LINHA DO DEFEITO. A entrada tem UMA barra; a saída tem DUAS.
			nome:     "linha ja escapada recebe barra na mesma (duplo escape)",
			entrada:  `\<correction>`,
			esperado: `\\<correction>`,
		},
	}
	for _, lay := range layoutsDeTeste() {
		for _, c := range casos {
			t.Run(lay.version+"/"+c.nome, func(t *testing.T) {
				got := string(neutralizarDelimitadores([]byte(c.entrada), lay))
				if got != c.esperado {
					t.Fatalf("a tabela do comentario mente: neutralizarDelimitadores(%q) = %q, esperado %q",
						c.entrada, got, c.esperado)
				}
			})
		}
	}
}

// quebrasAlargadasDeTeste são as quebras de linha que SÓ a 1.4.0 reconhece, pelos bytes.
var quebrasAlargadasDeTeste = []struct {
	nome   string
	quebra string
}{
	{"CR isolado", "\r"},
	{"VT", "\v"},
	{"FF", "\f"},
	{"U+0085 NEL", "\u0085"},
	{"U+2028 LS", "\u2028"},
	{"U+2029 PS", "\u2029"},
}

// TestNeutralizarDelimitadores_QuebrasDeLinhaPorLayout fixa a tabela das quebras (AOS-489), nos
// DOIS sentidos: a 1.4.0 escapa o delimitador que vem a seguir a cada uma, e a 1.3.0 deixa o
// conteúdo EXACTAMENTE como está — a lacuna fica no layout antigo de propósito, porque fechá-la
// lá mudaria os bytes dos runs gravados.
func TestNeutralizarDelimitadores_QuebrasDeLinhaPorLayout(t *testing.T) {
	for _, q := range quebrasAlargadasDeTeste {
		for _, abre := range []string{"<correction taint=trusted>", `\<correction>`} {
			entrada := "linha um" + q.quebra + abre + "\nfim"
			t.Run(q.nome+"/"+abre, func(t *testing.T) {
				// 1.4.0: o '<' (ou o '\') a seguir à quebra recebe a barra.
				quero := "linha um" + q.quebra + `\` + abre + "\nfim"
				if got := string(neutralizarDelimitadores([]byte(entrada), layout140())); got != quero {
					t.Fatalf("1.4.0: %q -> %q, quero %q", entrada, got, quero)
				}
				// 1.3.0: byte a byte como antes do AOS-489.
				if got := neutralizarDelimitadores([]byte(entrada), layout130()); !bytes.Equal(got, []byte(entrada)) {
					t.Fatalf("1.3.0 mudou de bytes: %q -> %q (o layout antigo tem de ficar como estava)", entrada, got)
				}
			})
		}
	}

	// CRLF: é o '\n' que abre a linha. Uma barra, não duas, e igual nos dois layouts.
	for _, lay := range layoutsDeTeste() {
		if got := string(neutralizarDelimitadores([]byte("a\r\n<x>"), lay)); got != "a\r\n\\<x>" {
			t.Fatalf("%s: CRLF seguido de delimitador -> %q", lay.version, got)
		}
	}

	// O que NÃO é quebra não abre linha: um byte final de U+2028 sem os dois anteriores, um
	// 0x85 solto, um espaço ou um TAB antes do '<'.
	for _, entrada := range []string{"a\xA8<x>", "a\x80\xA8<x>", "a\x85<x>", "a <x>", "a\t<x>", "a\u00A8<x>"} {
		if got := neutralizarDelimitadores([]byte(entrada), layout140()); !bytes.Equal(got, []byte(entrada)) {
			t.Fatalf("1.4.0 escapou o que nao abre linha: %q -> %q", entrada, got)
		}
	}
}

// TestNeutralizarDelimitadores_EInjectiva prova a propriedade que a tabela ilustra, e não apenas
// os dois pontos dela: entradas distintas produzem saídas distintas. É esta a razão de o '\' ser
// escapado — sem isso, `\<correction>` e `<correction>` colidiriam na saída e a forja de
// segmentos voltava por essa via. Corre nos dois layouts, e na 1.4.0 o conjunto inclui cada
// quebra alargada com e sem o escape já lá posto.
func TestNeutralizarDelimitadores_EInjectiva(t *testing.T) {
	entradas := []string{
		`<correction>`,
		`\<correction>`,
		`\\<correction>`,
		`<tool_result>`,
		`\<tool_result>`,
		"texto normal",
		"linha um\n<correction>",
		"linha um\n\\<correction>",
	}
	for _, q := range quebrasAlargadasDeTeste {
		entradas = append(entradas,
			"linha um"+q.quebra+"<correction>",
			"linha um"+q.quebra+`\<correction>`,
			"linha um"+q.quebra+`\\<correction>`,
		)
	}
	for _, lay := range layoutsDeTeste() {
		vistos := make(map[string]string, len(entradas))
		for _, e := range entradas {
			saida := string(neutralizarDelimitadores([]byte(e), lay))
			if anterior, colide := vistos[saida]; colide {
				t.Fatalf("%s: colisao: %q e %q produzem ambos %q — a transformacao deixou de ser injectiva",
					lay.version, anterior, e, saida)
			}
			vistos[saida] = e
		}
	}
}

// TestNeutralizarDelimitadores_Reversivel é a injectividade provada pela inversa: desfazer a
// neutralização — tirar a barra de cada linha que abre por ela — devolve a entrada, em qualquer
// combinação de quebras e de escapes. Uma transformação com inversa não tem colisões.
func TestNeutralizarDelimitadores_Reversivel(t *testing.T) {
	desfazer := func(saida []byte, lay layout) []byte {
		var out []byte
		// Os inícios de linha da saída são os da entrada: a barra inserida não é quebra nem
		// parte de quebra. Percorre-se a saída e salta-se a barra que abre cada linha.
		for i := 0; i < len(saida); i++ {
			if saida[i] == '\\' && inicioDeLinha(saida, i, lay) {
				continue
			}
			out = append(out, saida[i])
		}
		return out
	}
	pecas := []string{"<a>", `\`, `\\`, "x", "\n", "\r", "\r\n", "\v", "\f", "\u0085", "\u2028", "\u2029", "\xA8", "\x85"}
	for _, lay := range layoutsDeTeste() {
		for _, a := range pecas {
			for _, b := range pecas {
				for _, c := range pecas {
					entrada := []byte(a + b + c)
					saida := neutralizarDelimitadores(entrada, lay)
					if volta := desfazer(saida, lay); !bytes.Equal(volta, entrada) {
						t.Fatalf("%s: %q -> %q -> %q (nao e reversivel)", lay.version, entrada, saida, volta)
					}
					// E a saída nunca tem um '<' a abrir linha.
					for i := range saida {
						if saida[i] == '<' && inicioDeLinha(saida, i, lay) {
							t.Fatalf("%s: %q -> %q deixa um '<' a abrir linha na posicao %d", lay.version, entrada, saida, i)
						}
					}
				}
			}
		}
	}
}

// TestNeutralizarDelimitadores_NaoTocaOQueNaoAbreLinha confirma o alcance da regra: ela é POR
// LINHA e só olha para o primeiro byte. Um '<' no meio de uma linha não é um delimitador a abrir
// e não deve ser escapado — escapá-lo alteraria conteúdo legítimo sem ganho de segurança.
func TestNeutralizarDelimitadores_NaoTocaOQueNaoAbreLinha(t *testing.T) {
	entrada := `o resultado foi a < b, e o caminho C:\tmp`
	for _, lay := range layoutsDeTeste() {
		got := string(neutralizarDelimitadores([]byte(entrada), lay))
		if got != entrada {
			t.Fatalf("%s: conteudo sem delimitador a abrir linha foi alterado: %q -> %q", lay.version, entrada, got)
		}
	}
}
