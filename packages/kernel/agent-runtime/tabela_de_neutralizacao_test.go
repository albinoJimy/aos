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

import "testing"

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
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := string(neutralizarDelimitadores([]byte(c.entrada)))
			if got != c.esperado {
				t.Fatalf("a tabela do comentario mente: neutralizarDelimitadores(%q) = %q, esperado %q",
					c.entrada, got, c.esperado)
			}
		})
	}
}

// TestNeutralizarDelimitadores_EInjectiva prova a propriedade que a tabela ilustra, e não apenas
// os dois pontos dela: entradas distintas produzem saídas distintas. É esta a razão de o '\' ser
// escapado — sem isso, `\<correction>` e `<correction>` colidiriam na saída e a forja de
// segmentos voltava por essa via.
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
	vistos := make(map[string]string, len(entradas))
	for _, e := range entradas {
		saida := string(neutralizarDelimitadores([]byte(e)))
		if anterior, colide := vistos[saida]; colide {
			t.Fatalf("colisao: %q e %q produzem ambos %q — a transformacao deixou de ser injectiva",
				anterior, e, saida)
		}
		vistos[saida] = e
	}
}

// TestNeutralizarDelimitadores_NaoTocaOQueNaoAbreLinha confirma o alcance da regra: ela é POR
// LINHA e só olha para o primeiro byte. Um '<' no meio de uma linha não é um delimitador a abrir
// e não deve ser escapado — escapá-lo alteraria conteúdo legítimo sem ganho de segurança.
func TestNeutralizarDelimitadores_NaoTocaOQueNaoAbreLinha(t *testing.T) {
	entrada := `o resultado foi a < b, e o caminho C:\tmp`
	got := string(neutralizarDelimitadores([]byte(entrada)))
	if got != entrada {
		t.Fatalf("conteudo sem delimitador a abrir linha foi alterado: %q -> %q", entrada, got)
	}
}
