package bancoensaio

import (
	"errors"
	"fmt"
	"strings"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// OS BRAÇOS DA EXPERIÊNCIA DOS SEPARADORES (AOS-512, desenho A2 §5.3).
//
// # NÃO SÃO VERSÕES DE PROJECÇÃO
//
// As variantes B e C existem SÓ aqui, no banco. Não são versões publicadas da projecção nativa:
// [modelgateway.ParseNativeProjectionVersion] recusa-as, nenhum interruptor do nó as selecciona
// (`AOS_MODEL_PROJECTION_VERSION` só aceita as publicadas), e o binário do nó não contém este
// pacote (TestAOS512_ONoNaoContemOBanco). Se um braço ganhar, a versão de projecção
// correspondente abre-se em ticket próprio, com emenda ao ADR-036; o banco só mede.
//
// # COMO SE CONSTROEM
//
// Cada braço parte das mensagens que a projecção de PRODUÇÃO produz ([modelgateway.
// ProjectNativeVersion], chamada pelo adaptador RT→GW do nó de ensaio) e reescreve-as à saída,
// no decorador da porta do gateway ([portaDeEnsaio]). Os braços A e D não reescrevem nada: são
// as versões publicadas 1.2.0 e 1.0.0, byte a byte.
//
// Uma variante muda UMA coisa em relação ao braço A:
//
//   - B troca os separadores `<kind …>` e `</kind>` por `[[kind …]]` e `[[/kind]]` — sem sinais
//     de menor nem de maior — e, no texto do protocolo, as frases que os nomeiam;
//   - C retira as linhas de fim `</kind>` e, do texto do protocolo, as frases que falam delas.
//
// As substituições no texto do protocolo são EXACTAS e contadas: cada uma tem de ocorrer o
// número de vezes esperado. Se o texto da 1.2.0 mudar, a variante deixa de se conseguir
// construir e a corrida falha antes de enviar um pedido — em vez de medir, sem aviso, um texto
// que já não é «a 1.2.0 com uma diferença».

// Braco é um braço da experiência. Vocabulário fechado.
type Braco string

const (
	// BracoA — a projecção 1.2.0 publicada: separadores `<kind>` … `</kind>`.
	BracoA Braco = "A"
	// BracoB — a 1.2.0 com separadores sem sinais de menor nem de maior.
	BracoB Braco = "B"
	// BracoC — a 1.2.0 sem linhas de fim.
	BracoC Braco = "C"
	// BracoD — o controlo: a projecção 1.0.0 publicada.
	BracoD Braco = "D"
)

// Bracos devolve os quatro braços, por ordem.
func Bracos() []Braco { return []Braco{BracoA, BracoB, BracoC, BracoD} }

// DescricaoDoBraco diz, numa linha, o que o braço é. Vai no relatório.
func DescricaoDoBraco(b Braco) string {
	switch b {
	case BracoA:
		return "projeccao 1.2.0 publicada (separadores <kind> ... </kind>)"
	case BracoB:
		return "1.2.0 com separadores [[kind]] ... [[/kind]], sem sinais de menor nem de maior (so no banco)"
	case BracoC:
		return "1.2.0 sem linhas de fim (so no banco)"
	case BracoD:
		return "controlo: projeccao 1.0.0 publicada"
	default:
		return "desconhecido"
	}
}

// LerBraco valida o nome de um braço.
func LerBraco(s string) (Braco, error) {
	for _, b := range Bracos() {
		if string(b) == s {
			return b, nil
		}
	}
	return "", fmt.Errorf("banco-ensaio: braco desconhecido: %q (aceites: A, B, C, D)", s)
}

// VersaoPublicadaDoBraco é a versão PUBLICADA da projecção nativa de que o braço parte — a que
// o adaptador do nó de ensaio usa.
func VersaoPublicadaDoBraco(b Braco) string {
	if b == BracoD {
		return modelgateway.NativeProjectionVersion
	}
	return modelgateway.NativeProjectionVersion120
}

// ErrVariante — a variante não se consegue construir sobre as mensagens recebidas. Fail-closed:
// o pedido não sai.
var ErrVariante = errors.New("banco-ensaio: a variante de protocolo nao se constroi sobre este pedido")

// troca é uma substituição exacta no texto do protocolo, que tem de ocorrer `vezes` vezes.
type troca struct {
	de, para string
	vezes    int
}

// As substituições do braço B no texto do protocolo da 1.2.0.
var trocasDoBracoB = []troca{
	{`"<kind label=value ...>"`, `"[[kind label=value ...]]"`, 1},
	{`"</kind>"`, `"[[/kind]]"`, 1},
	{`"<objective>"`, `"[[objective]]"`, 1},
	{`whose first visible character would be "<" or "\"`, `whose first visible character would be "[" or "\"`, 1},
}

// As substituições do braço C no texto do protocolo da 1.2.0: saem as frases da linha de fim.
var trocasDoBracoC = []troca{
	{`, then its body, then an end line "</kind>".`, `, then its body.`, 1},
	{`A header line and an end line start at the very first character of a line, and only the runtime writes them.`,
		`A header line starts at the very first character of a line, and only the runtime writes it.`, 1},
	{`, up to that segment's end line.`, `.`, 1},
	{`Do not use the header lines or end lines of the runtime in them.`, `Do not use the header lines of the runtime in them.`, 1},
	{`looks like a header line or an end line - indented`, `looks like a header line - indented`, 1},
}

func aplicarTrocas(texto string, trocas []troca) (string, error) {
	for _, t := range trocas {
		if n := strings.Count(texto, t.de); n != t.vezes {
			return "", fmt.Errorf("%w: uma frase do protocolo 1.2.0 ocorre %d vezes e esperava-se %d (o texto do protocolo mudou?)", ErrVariante, n, t.vezes)
		}
		texto = strings.ReplaceAll(texto, t.de, t.para)
	}
	return texto, nil
}

// TransformarMensagens aplica o braço às mensagens da projecção de produção. A e D devolvem as
// mensagens tal e qual. Não altera o slice recebido.
func TransformarMensagens(b Braco, msgs []port.Message) ([]port.Message, error) {
	switch b {
	case BracoA, BracoD:
		return msgs, nil
	case BracoB, BracoC:
	default:
		return nil, fmt.Errorf("%w: braco %q", ErrVariante, string(b))
	}
	out := make([]port.Message, len(msgs))
	copy(out, msgs)
	vistoSystem := false
	for i := range out {
		switch out[i].Role {
		case port.RoleSystem:
			trocas := trocasDoBracoB
			if b == BracoC {
				trocas = trocasDoBracoC
			}
			novo, err := aplicarTrocas(out[i].Content, trocas)
			if err != nil {
				return nil, err
			}
			out[i].Content, vistoSystem = novo, true
		case port.RoleUser, port.RoleTool:
			novo, err := reescreverSegmentos(b, out[i].Content)
			if err != nil {
				return nil, err
			}
			out[i].Content = novo
		}
	}
	if !vistoSystem {
		return nil, fmt.Errorf("%w: o pedido nao tem mensagem system (nao veio da projeccao nativa?)", ErrVariante)
	}
	return out, nil
}

// reescreverSegmentos reescreve as linhas de cabeçalho e de fim de uma mensagem `user` ou
// `tool` da projecção 1.2.0.
//
// Na 1.2.0, uma linha que abre por '<' numa destas mensagens só pode ter sido escrita pelo
// runtime — cabeçalho ou fim —, porque o kernel escapa as linhas de corpo que abririam por '<'
// ou '\'. É essa garantia que deixa reescrever linha a linha sem interpretar o corpo.
//
// DESVIO DECLARADO do braço B. O escape do corpo continua a ser o do kernel (um '\' à frente de
// uma linha que abriria por '<' ou '\'), e o braço B acrescenta o seu: um '\' à frente de uma
// linha de corpo que abriria por '['. Uma linha de corpo que abrisse por '<' aparece, no braço
// B, com um '\' que o texto do protocolo desse braço não explica. Não acontece com os
// documentos da bateria ([validarDocumento] recusa linhas dessas); pode acontecer com texto que
// o modelo escreva e volte a entrar num pedido, que a experiência dos separadores não tem (um
// só turno por amostra).
func reescreverSegmentos(b Braco, conteudo string) (string, error) {
	linhas := strings.SplitAfter(conteudo, "\n")
	var out strings.Builder
	out.Grow(len(conteudo) + 16)
	for _, linha := range linhas {
		if linha == "" {
			continue
		}
		corpo := strings.TrimSuffix(linha, "\n")
		fimDeLinha := linha[len(corpo):]
		switch {
		case strings.HasPrefix(corpo, "</"):
			if !strings.HasSuffix(corpo, ">") {
				return "", fmt.Errorf("%w: linha de fim malformada", ErrVariante)
			}
			if b == BracoC {
				continue // o braço C não tem linhas de fim
			}
			out.WriteString("[[/" + corpo[2:len(corpo)-1] + "]]" + fimDeLinha)
		case strings.HasPrefix(corpo, "<"):
			if !strings.HasSuffix(corpo, ">") {
				return "", fmt.Errorf("%w: linha de cabecalho malformada", ErrVariante)
			}
			if b == BracoC {
				out.WriteString(linha)
				continue
			}
			out.WriteString("[[" + corpo[1:len(corpo)-1] + "]]" + fimDeLinha)
		case b == BracoB && strings.HasPrefix(corpo, "["):
			out.WriteString(`\` + linha)
		default:
			out.WriteString(linha)
		}
	}
	return out.String(), nil
}
