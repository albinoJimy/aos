package integration

// AOS-258 — O ESTIMADOR REAL DO ORÇAMENTO POR-RUN.
//
// # O problema que este ficheiro fecha
//
// O desafio A1 nomeou-o: o [referencemonitor.Call] NÃO transporta o prompt nem a contagem de
// tokens do turno, pelo que injectar uma função por [budget.WithEstimator] não basta se essa
// função pretender estimar o TURNO. A alternativa mais barata — e a que aqui se toma — é
// estimar FORA do Reference Monitor, sobre a Call MATERIALIZADA (o que o RM realmente vê),
// e entregar o resultado pela seam que já existe. Nenhuma seam nova é inventada: a função
// vive em `integration` (fora do kernel), entra pela opção que o adaptador já expõe, e lê a
// Call depois de [SecuredConfig.EffectRewriter] lhe ter dado a FORMA FINAL do efeito.
//
// O que substitui: [budget.DefaultEstimator], que o próprio autor documenta como
// «PLACEHOLDER honesto» — contava só `len(call.Input)/4` e inventava uma tarifa fixa de
// 10 micro-USD/token. Passa a NÃO SER USADO EM PRODUÇÃO (critério de aceitação de AOS-258,
// selado por [TestAOS258_ProducaoNaoUsaODefaultEstimator]).
//
// # O QUE ESTIMA
//
// A pegada em TOKENS que a tool call MATERIALIZADA imprime na transcrição do modelo:
//
//   - os ARGUMENTOS (`call.Input`) na sua forma FINAL — depois da reescrita do efeito
//     (AOS-005/AOS-064), que é a forma que o RM medeia, que o humano aprova e que executa.
//     Estimar sobre os args PRÉ-reescrita seria estimar um efeito que não é o que corre;
//   - o ENVELOPE que viaja com eles na transcrição e não é grátis: `ToolID`, `Capability` e
//     o `Resource` (tipo/valor/região). Uma tool call com um URL de 300 bytes custa esses
//     bytes ao contexto tanto quanto os custaria dentro do payload — [budget.DefaultEstimator]
//     não os via.
//
// A contagem é uma APROXIMAÇÃO POR ÁTOMOS ([approxTokens]) com o piso da heurística de bytes:
// determinística, sem dependências (ADR-017) e — ao contrário de `bytes/4` — sem
// SUBESTIMAR payloads estruturados, que é a direcção fail-OPEN de um controlo de admissão.
//
// # O QUE **NÃO** ESTIMA (e porquê) — a lista é a razão de o alcance declarado em AOS-255 não mudar
//
//   - O TURNO DE MODELO. A inferência é invocada directamente pelo loop
//     (`kernel/agent-runtime/loop.go`, `rt.model.Call`), FORA da cadeia do Reference Monitor:
//     nenhuma reserva a admite, e por isso nenhuma estimativa aqui a poderia cobrir. É o eixo
//     AOS-260, e é a linha de custo DOMINANTE. O banner continua a dizê-lo.
//   - O RESULTADO DA TOOL. O output volta à transcrição e é reenviado em cada turno seguinte
//     — é frequentemente maior do que os argumentos. Não é estimável ANTES do efeito (só se
//     conhece depois de executar) e o saldo de AOS-257 confirma a RESERVA, não a MEDIÇÃO. O
//     canal de custo medido é o eixo AOS-259.
//   - DÓLARES. A dimensão `CostMicroUSD` é devolvida a ZERO de propósito: sem canal de custo
//     ponta a ponta (AOS-259) uma tarifa aqui seria um número inventado, e um tecto em $
//     comparado com consumo contado a zero é uma capacidade-fantasma. Token-only.
//   - O TOKENIZADOR DO PROVIDER. Isto não é BPE: é uma aproximação sem vocabulário. Direcção
//     do erro, declarada: SOBRESTIMA texto latino acentuado (1 token por rune não-ASCII),
//     aproxima CJK, e deixa de SUBESTIMAR JSON/base64/URLs. Sobrestimar esgota o tecto mais
//     cedo — a direcção fail-closed de um orçamento. Um tokenizador real é uma dependência
//     que o nó não tem (ADR-017) e teria de ser por-modelo.
//
// # Alternativa considerada e REJEITADA: a estimativa DECLARADA pelo chamador
//
// `CallContext.BudgetTokensRemaining` seria o campo óbvio para o chamador declarar a quantia
// (o orquestrador e o planeador já o preenchem: `delegation.go`, `planner.go`). Não é lido
// aqui, por duas razões que se somam:
//
//   - nos dois produtores REAIS o campo é a FATIA HERDADA / a reserva de planeamento, e a
//     sub-árvore do filho é debitada por ancestralidade — cobrá-la também na admissão do
//     spawn seria DUPLA CONTAGEM;
//   - no nó, nenhuma tool call do loop o preenche (fica a zero), pelo que lê-lo não mudaria
//     decisão nenhuma. Um campo lido que nunca decide é uma capacidade-fantasma com aspecto
//     de mecanismo.
//
// Quando houver um produtor honesto de estimativa por-call, o sítio é este — e o combinador
// tem de ser um MÁXIMO com a pegada local (uma declaração só pode AGRAVAR a estimativa,
// nunca baixá-la abaixo do que o RM consegue provar sozinho).

import (
	"unicode/utf8"

	budget "github.com/aos-ref/control-plane/budget"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// bytesPerTokenFloor é o divisor da heurística de bytes que serve de PISO à contagem por
// átomos: ~1 token por 4 bytes. É a mesma constante que [budget.DefaultEstimator] usava e o
// banner declara; mantê-la como piso garante que a troca de estimador NUNCA baixa uma
// estimativa (a troca só pode apertar o tecto, nunca afrouxá-lo).
const bytesPerTokenFloor = 4

// maxRunePerToken é o comprimento máximo de uma sequência alfanumérica que se conta como UM
// token. Sequências mais longas (identificadores, hashes, base64) partem-se — é o que um BPE
// real faz, e é a razão principal de `bytes/4` subestimar payloads densos.
const maxRunePerToken = 4

// TokenOnlyEstimator é o estimador COMPOSTO em produção (entregue a [budget.WithEstimator]
// em [NewRunBudget]). O nome mantém-se de AOS-257 — continua a ser token-only — mas o corpo
// deixou de delegar em [budget.DefaultEstimator]: é o estimador real de AOS-258.
//
// Ver o cabeçalho deste ficheiro para o que estima e, sobretudo, para o que NÃO estima.
//
// Nunca devolve zero: uma tool call não é grátis, e uma estimativa de 0 tokens seria uma
// reserva inválida ([budget.Amount.validReserve]) que o adaptador converteria em deny —
// tudo negado por uma call com argumentos vazios.
func TokenOnlyEstimator(call *referencemonitor.Call) budget.Amount {
	if call == nil {
		return budget.Amount{Tokens: 1}
	}
	// A pegada materializada: os argumentos na forma FINAL do efeito + o envelope que também
	// ocupa contexto. Somam-se as contagens campo a campo (e não a de uma concatenação) para
	// que a fronteira entre campos nunca funda dois átomos num só.
	campos := [...][]byte{
		call.Input,
		[]byte(call.ToolID),
		[]byte(call.Capability),
		[]byte(call.Resource.Type),
		[]byte(call.Resource.Value),
		[]byte(call.Resource.Region),
	}
	var atomos, bytes int64
	for _, c := range campos {
		atomos += approxTokens(c)
		bytes += int64(len(c))
	}

	// PISO DE BYTES: exactamente a fórmula do [budget.DefaultEstimator] (`bytes/4 + 1`), mas
	// aplicada à pegada INTEIRA e não só aos argumentos. Existe por uma propriedade que se
	// quer PROVÁVEL e não conjecturada: a troca de estimador NUNCA baixa uma estimativa
	// (selada por TestAOS258_NuncaSubestimaOPlaceholder). A contagem por átomos é maior em
	// payloads estruturados, mas seria MENOR em texto só com espaços — que ela não conta
	// sozinhos; o máximo das duas remove essa aresta.
	piso := bytes/bytesPerTokenFloor + 1
	tokens := atomos
	if piso > tokens {
		tokens = piso
	}
	if tokens < 1 {
		tokens = 1
	}
	// CostMicroUSD deliberadamente a ZERO — ver «O QUE NÃO ESTIMA», dólares.
	return budget.Amount{Tokens: tokens}
}

// approxTokens conta os «átomos» de um texto: a aproximação sem-dependências de um
// tokenizador BPE. As regras, e o que cada uma modela:
//
//   - uma sequência ALFANUMÉRICA ASCII conta ceil(n/4) tokens — palavras curtas valem 1,
//     identificadores/hashes/base64 partem-se, como num vocabulário real;
//   - ESPAÇO/tabulação/newline não contam sozinhos: colam-se ao átomo seguinte (é assim que
//     os vocabulários reais os codificam, «␣the» é um token). O piso de bytes de
//     [TokenOnlyEstimator] cobre o caso degenerado do payload só com espaços;
//   - qualquer outro byte ASCII (pontuação, `{`, `"`, `:`, `/`, `-`, …) conta 1 token. É AQUI
//     que `bytes/4` falha: `{"a":1}` são 7 bytes ⇒ 2 tokens pela heurística de bytes e ~7 na
//     realidade;
//   - cada rune NÃO-ASCII conta 1 token. Aproxima CJK e sobrestima latino acentuado — a
//     direcção conservadora (sobrestimar aperta o tecto).
//
// É MONÓTONA: acrescentar bytes nunca baixa a contagem — a propriedade sem a qual um
// estimador de orçamento poderia ser afrouxado por inflação do payload.
func approxTokens(b []byte) int64 {
	var tokens int64
	corrida := 0 // comprimento da sequência alfanumérica ASCII corrente
	fecha := func() {
		if corrida > 0 {
			tokens += int64((corrida + maxRunePerToken - 1) / maxRunePerToken)
			corrida = 0
		}
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c >= utf8.RuneSelf: // rune não-ASCII
			fecha()
			_, tamanho := utf8.DecodeRune(b[i:])
			if tamanho < 1 {
				tamanho = 1
			}
			tokens++
			i += tamanho
			continue
		case isAlnumASCII(c):
			corrida++
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			fecha() // o branco cola-se ao átomo seguinte; não conta sozinho
		default:
			fecha()
			tokens++ // pontuação/estrutura: um token cada
		}
		i++
	}
	fecha()
	return tokens
}

// isAlnumASCII indica se o byte é uma letra ou dígito ASCII.
func isAlnumASCII(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
