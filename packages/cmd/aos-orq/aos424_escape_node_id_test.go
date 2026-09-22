package main

// aos424_escape_node_id_test.go — DOIS NÓS DE PLANO NUNCA ESCREVEM NO MESMO STREAM.
//
// O `run_id` de um run **é** o seu `stream_id`, e o id do run filho é `<run>~<node_id>`. Como a
// gramática do `node_id` admite `.` e `:` — e o prompt de decomposição diz ao modelo, por
// escrito, que os pode usar —, o id do run filho podia sair irrepresentável num subject NATS.
//
// O escape resolve-o, mas só vale se for INJECTIVO: substituir `.` por `-` faria os nós `a.b` e
// `a-b` colidirem no mesmo run filho — dois nós do plano a escrever no mesmo stream, que é
// pior do que o problema que se estava a resolver. É a mesma razão pela qual o `subjectDe`
// recusa em vez de escapar.

import (
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// O ID DO RUN FILHO É SEMPRE UM `stream_id` VÁLIDO.
//
// A asserção usa a fonte canónica da regra, e não uma lista repetida aqui: se a regra apertar,
// este teste aperta com ela.
func TestAOS424ChildRunIDEeSempreUmStreamValido(t *testing.T) {
	nodeIDs := []string{
		"pesquisa",               // o caso comum
		"n1",                     // idem
		"analise.dados",          // o ponto que a grammar admite e o subject recusa
		"etapa:build",            // dois-pontos (representável, mas parte da grammar)
		"a.b.c",                  // vários
		"com_sublinhado",         // a marca de escape, literal
		"_comeca_com_marca",      // idem, no início
		"analise+2edados",        // parece já escapado — não pode confundir-se
		"til~dentro",             // fora da grammar, mas o Decode não a impõe
		"tudo._:~_junto",         // combinação
		strings.Repeat("a.", 40), // longo
	}
	for _, nodeID := range nodeIDs {
		id := childRunID("run-424", nodeID)
		if err := eventstore.ValidarStreamID(id); err != nil {
			t.Errorf("childRunID(%q) = %q, que nao e um stream_id valido: %v\n"+
				"sobre JetStream o Append recusa e o no do plano nunca executa", nodeID, id, err)
		}
	}
}

// INJECTIVIDADE — a propriedade que impede dois nós de partilharem stream.
//
// Inclui de propósito os pares que uma substituição ingénua colapsaria (`a.b` vs `a-b`) e os
// que uma marca de escape mal fechada colapsaria (`a_2eb` vs `a.b`).
func TestAOS424EscapeEeInjectivo(t *testing.T) {
	nodeIDs := []string{
		"a.b", "a-b", "a_b", "a__b", "a_2eb", "a.b.c", "a_2e_2eb",
		// os que a MARCA `+` torna interessantes
		"a+2eb", "a++2eb", "a+b", "a++b", "+", "++",
		"", "_", "__", "___", ".", "..", "~", "a~b", "a_7eb", "a+7eb",
		"etapa:build", "etapa_3abuild",
	}
	vistos := map[string]string{}
	for _, nodeID := range nodeIDs {
		id := childRunID("run-424", nodeID)
		if anterior, colide := vistos[id]; colide {
			t.Errorf("COLISAO: os node_id %q e %q produzem o MESMO run filho %q.\n"+
				"Dois nos do plano escreveriam no mesmo stream — e o resultado de um apagaria o\n"+
				"do outro, em silencio.", anterior, nodeID, id)
			continue
		}
		vistos[id] = nodeID
	}
}

// O CASO COMUM NÃO MUDA. É o que garante que os ids já existentes continuam a ser os mesmos, e
// que a legibilidade só se paga onde é preciso.
func TestAOS424EscapeNaoTocaNoCasoComum(t *testing.T) {
	// Os `node_id` de todas as fixtures da árvore, à data.
	for _, nodeID := range []string{
		"n1", "n2", "pesquisa", "recolha", "sintese", "fetch", "deepen", "digest",
		"auditoria", "indexacao", "publicacao", "entrega", "exfil", "suplementar",
	} {
		if got, quer := childRunID("run-424", nodeID), "run-424~"+nodeID; got != quer {
			t.Errorf("o node_id comum %q passou a produzir %q, esperava %q:\n"+
				"o escape so pode tocar nos ids que precisam, senao muda ids que ja existem",
				nodeID, got, quer)
		}
	}
}

// O ESCAPE É REVERSÍVEL, e é isso que PROVA a injectividade.
//
// Ninguém reverte isto no produto — procurou-se, e o separador só aparece numa guarda. A
// reversão vive aqui, no teste, porque é a demonstração de que a codificação não perde
// informação: se se consegue voltar atrás, dois valores distintos não podem ter colapsado.
func TestAOS424EscapeEeReversivel(t *testing.T) {
	for _, nodeID := range []string{
		"pesquisa", "analise.dados", "a_b", "a+b", "a++b", "a+2eb", "~", ".", "_", "+", "tudo._:~+junto",
	} {
		escapado := escaparParaStream(nodeID)
		volta, err := desescaparDeTeste(escapado)
		if err != nil {
			t.Errorf("desescapar(%q) de %q falhou: %v", escapado, nodeID, err)
			continue
		}
		if volta != nodeID {
			t.Errorf("a ida e volta perdeu informacao: %q -> %q -> %q", nodeID, escapado, volta)
		}
	}
}

// desescaparDeTeste inverte [escaparParaStream]. Vive no teste de propósito: o produto não
// precisa de reverter, e um decodificador sem chamadores seria código morto a pedir para
// divergir do codificador.
func desescaparDeTeste(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != marcaDeEscape {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == marcaDeEscape {
			b.WriteByte(marcaDeEscape)
			i++
			continue
		}
		if i+2 >= len(s) {
			return "", errSequenciaTruncada
		}
		var v byte
		for _, d := range []byte{s[i+1], s[i+2]} {
			v <<= 4
			switch {
			case d >= '0' && d <= '9':
				v |= d - '0'
			case d >= 'a' && d <= 'f':
				v |= d - 'a' + 10
			default:
				return "", errSequenciaTruncada
			}
		}
		b.WriteByte(v)
		i += 2
	}
	return b.String(), nil
}

var errSequenciaTruncada = errSeq("sequencia de escape truncada ou mal formada")

type errSeq string

func (e errSeq) Error() string { return string(e) }
