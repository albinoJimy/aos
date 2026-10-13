package bancoensaio

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// A BATERIA POR DEGRAUS (AOS-525) — a bateria 2.0.0 e a experiência `degraus`.
//
// # PORQUÊ
//
// A 2026-10-12 a bateria 1.0.0 foi passada a seis modelos reais, do mais barato ao mais caro:
// nenhum falhou a pedir a tool à primeira tentativa, e o único recusado foi-o por um nó em
// quarenta. A 1.0.0 não separa modelos. Esta bateria tem casos de dificuldade CRESCENTE — um
// por degrau, cada um com a capacidade que pede dita por extenso na sua descrição —, para se
// ler ATÉ ONDE cada modelo chega.
//
// # O QUE NÃO MUDA
//
// A 1.0.0 fica onde está, com os seus ficheiros e o seu digest: as experiências `bateria`,
// `separadores` e `qualificacao` continuam a correr sobre ela, e os relatórios e os registos
// assinados que a citam continuam a conferir. A 2.0.0 vive noutra pasta, tem outro digest, e só
// a experiência `degraus` a corre.
//
// # O QUE A EXPERIÊNCIA NÃO É
//
// Não dá veredicto: não qualifica nem recusa um modelo, e não substitui a qualificação
// (AOS-518). Devolve taxas POR DEGRAU. «Degrau cumprido» é verificação de factos pela regra dos
// factos (factos.go) sobre documentos sintéticos e curtos — não é um juiz, e não diz se a
// resposta é boa.

//go:embed bateria2/casos.json bateria2/*.txt
var ficheirosDaBateriaPorDegraus embed.FS

// pastaDaBateriaPorDegraus é a pasta da bateria 2.0.0. Nenhum documento dela vem de fora.
const pastaDaBateriaPorDegraus = "bateria2"

// ExperienciaDegraus — a bateria por degraus (2.0.0), no braço A, com taxas por degrau e sem
// veredicto.
const ExperienciaDegraus Experiencia = "degraus"

// CarregarBateriaPorDegraus lê a bateria 2.0.0 embebida, valida-a e calcula o seu digest. As
// regras são as de [CarregarBateria] e mais as dos degraus: todos os casos têm degrau, os
// degraus não têm buracos, e nenhum facto — exigido ou proibido — vem escrito no objectivo.
func CarregarBateriaPorDegraus() (*Bateria, error) {
	return carregarBateriaPorDegraus(ficheirosDaBateriaPorDegraus)
}

func carregarBateriaPorDegraus(origem fs.FS) (*Bateria, error) {
	return carregarDaPasta(origem, pastaDaBateriaPorDegraus, true)
}

// TemDegraus diz se a bateria é uma bateria por degraus.
func (b *Bateria) TemDegraus() bool { return b.porDegraus }

// Degraus devolve o número de degraus da bateria (zero numa bateria sem degraus).
func (b *Bateria) Degraus() int {
	maior := 0
	for _, c := range b.Casos {
		if c.Degrau > maior {
			maior = c.Degrau
		}
	}
	return maior
}

// validarProibidos confere os factos proibidos de um nó. Vale para qualquer bateria: um
// proibido que não esteja nos documentos do nó não é um distractor (nenhuma resposta o traria),
// e um que se confunda com um facto exigido tornava a resposta certa numa resposta errada.
func (b *Bateria) validarProibidos(caso string, n No) error {
	for _, p := range n.FactosProibidos {
		dobrado := semHifenesNemEspacos(p)
		if p != strings.TrimSpace(p) || dobrado == "" {
			return fmt.Errorf("%w: %s/%s tem um facto proibido vazio ou com espacos nas pontas", ErrBateria, caso, n.ID)
		}
		achado := false
		for _, doc := range n.FactosDe {
			if strings.Contains(string(b.documentos[doc]), p) {
				achado = true
			}
		}
		if !achado {
			return fmt.Errorf("%w: %s/%s tem um facto proibido que nao esta nos seus documentos (nao e um distractor)", ErrBateria, caso, n.ID)
		}
		// Na forma larga dos proibidos (sem hífenes nem espaços), que é a que a saída vai ver.
		for _, f := range n.Factos {
			exigido := semHifenesNemEspacos(f)
			if strings.Contains(exigido, dobrado) || strings.Contains(dobrado, exigido) {
				return fmt.Errorf("%w: %s/%s tem um facto proibido igual a um facto exigido, ou um contido no outro", ErrBateria, caso, n.ID)
			}
		}
	}
	return nil
}

// validarDegraus confere o que só os degraus pedem. Numa bateria sem degraus recusa o campo:
// um caso com degrau fora da bateria por degraus não tinha quem o lesse.
func (b *Bateria) validarDegraus() error {
	if !b.porDegraus {
		for _, c := range b.Casos {
			if c.Degrau != 0 {
				return fmt.Errorf("%w: %s traz um degrau e esta bateria nao e por degraus", ErrBateria, c.ID)
			}
		}
		return nil
	}
	vistos := map[int]bool{}
	anterior := 0
	var objectivos []string
	for _, c := range b.Casos {
		if c.Degrau <= 0 {
			return fmt.Errorf("%w: %s nao tem degrau (um inteiro a partir de 1)", ErrBateria, c.ID)
		}
		if c.Degrau < anterior {
			return fmt.Errorf("%w: %s esta fora de ordem: os casos vem por degrau crescente", ErrBateria, c.ID)
		}
		if strings.TrimSpace(c.Descricao) == "" {
			return fmt.Errorf("%w: %s nao diz por extenso a capacidade que o degrau pede", ErrBateria, c.ID)
		}
		vistos[c.Degrau], anterior = true, c.Degrau
		for _, n := range c.Nos {
			// O objectivo é o que identifica o nó para o provider falso dos degraus, e é o que o
			// modelo lê: um facto escrito nele podia ser copiado sem ler documento nenhum.
			objectivo := dobrarParaComparar(n.Objectivo)
			for _, f := range append(append([]string(nil), n.Factos...), n.FactosProibidos...) {
				if strings.Contains(objectivo, dobrarParaComparar(f)) {
					return fmt.Errorf("%w: %s/%s tem um facto escrito no proprio objectivo", ErrBateria, c.ID, n.ID)
				}
			}
			for _, outro := range objectivos {
				if strings.Contains(outro, n.Objectivo) || strings.Contains(n.Objectivo, outro) {
					return fmt.Errorf("%w: %s/%s tem um objectivo igual ao de outro no, ou um contido no outro", ErrBateria, c.ID, n.ID)
				}
			}
			objectivos = append(objectivos, n.Objectivo)
		}
	}
	for d := 1; d <= anterior; d++ {
		if !vistos[d] {
			return fmt.Errorf("%w: falta o degrau %d (os degraus nao tem buracos)", ErrBateria, d)
		}
	}
	return nil
}

// MaxTurnosDosDegraus é o tecto de turnos — invocações do modelo — de um run da experiência
// `degraus`. São seis, e não os quatro das outras experiências, por causa do degrau 2: ler
// quatro documentos um de cada vez são quatro turnos, e o quinto é a resposta. Com quatro, um
// modelo que lê bem mas faz uma leitura por turno acabava em `turnos_esgotados`, e o degrau
// media o paralelismo das chamadas em vez do volume.
const MaxTurnosDosDegraus = 6

// PlanoDosDegraus devolve o plano da experiência `degraus`: a bateria por degraus inteira, no
// braço A, `amostras` vezes, com até três tentativas por nó e [MaxTurnosDosDegraus] turnos.
//
// O PIOR CASO é o de [Plano.PedidosMaximos]: por passagem, cada nó da bateria pode gastar
// `Tentativas × MaxTurnos` pedidos. É declarado no relatório (`pedidos.previstos_max`) e, com
// contador, conferido contra o que resta do tecto do dia ANTES do primeiro pedido.
func PlanoDosDegraus(b *Bateria, amostras int, semente uint64) Plano {
	p := PlanoDaBateria(b, BracoA, amostras)
	p.Experiencia, p.Semente, p.MaxTurnos = ExperienciaDegraus, semente, MaxTurnosDosDegraus
	return p
}

// O resultado da verificação dos factos proibidos de um run concluído.
const (
	// ProibidosNenhum — nenhum facto proibido do nó está na saída.
	ProibidosNenhum = "nenhum"
	// ProibidosPresente — pelo menos um está: é a «resposta errada».
	ProibidosPresente = "presente"
)

// semHifenesNemEspacos devolve o texto dobrado pela regra dos factos e, depois, sem hífenes nem
// espaços. É a forma em que os factos PROIBIDOS se comparam.
func semHifenesNemEspacos(s string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(dobrarParaComparar(s))
}

// proibidoNaSaida diz se um facto proibido ocorre na saída. A comparação é MAIS LARGA do que a
// dos factos exigidos: além da dobra, ignora hífenes e espaços. Nos exigidos a regra estrita
// joga contra o erro (um nome mal escrito conta como ausente); nos proibidos jogava a favor
// dele — «Quelhas Bordalo», com um espaço pelo hífen, passava por não ter mencionado o
// fornecedor. Um nome ou um código DIFERENTE continua a não disparar.
func proibidoNaSaida(saida, proibido string) bool {
	largo := semHifenesNemEspacos(proibido)
	return largo != "" && strings.Contains(semHifenesNemEspacos(saida), largo)
}

// verificarProibidos dá o resultado da verificação dos factos proibidos de um run concluído
// ([proibidoNaSaida]).
func verificarProibidos(saida string, proibidos []string) string {
	for _, p := range proibidos {
		if proibidoNaSaida(saida, p) {
			return ProibidosPresente
		}
	}
	return ProibidosNenhum
}

// RegraDoDegrauCumprido é a regra, por extenso, do agregado «degrau cumprido». Vai no relatório.
const RegraDoDegrauCumprido = "Uma passagem por um degrau (um caso, numa amostra) conta como cumprida quando TODOS os nos do caso tem um run que fechou cumprido (o contrato de conclusao), com todos os factos exigidos do no na saida e nenhum facto proibido nela. Os factos exigidos comparam-se sem acentos, sem maiusculas, com os espacos reduzidos a um e os hifenes tipograficos iguais ao simples; os proibidos, alem disso, sem hifenes nem espacos. Um no que nao chegou a correr, porque o no de que depende nao concluiu por culpa do modelo, conta como nao cumprido. Uma passagem INTERROMPIDA sai de todas as taxas do degrau e conta a parte: e aquela em que a ultima tentativa de algum no fechou por uma causa passageira, pela classificacao da qualificacao (429, 5xx, erro de transporte ou prazo esgotado, conta ou rota, tecto, cancelamento), ou em que a corrida parou antes de um no correr — e em que, ate ai, nada estava firmemente falhado. Uma razao firme ganha a uma passageira, porque as contagens so crescem: se antes da interrupcao um no ja tinha fechado sem cumprir por culpa do modelo (incluindo uma tentativa anterior do proprio no interrompido), ou concluido com um facto ausente ou um proibido presente, a passagem conta como NAO cumprida, e os nos da interrupcao em diante nao entram em nos_cumpridos. Um degrau so com passagens interrompidas nao tem taxa (0/0): nao foi medido."

// InterrompidaPelaParagem é a causa de uma passagem em que a corrida parou antes de um nó
// correr. As outras causas são as da qualificação ([estadoFinalDoNo]).
const InterrompidaPelaParagem = "corrida_parada"

// TaxasDoDegrau são as taxas de um degrau.
type TaxasDoDegrau struct {
	Degrau int `json:"degrau"`
	// Capacidade é a capacidade que o degrau pede, por extenso: a descrição dos seus casos.
	Capacidade string   `json:"capacidade"`
	Casos      []string `json:"casos"`
	// Passagens é o número de passagens pelo degrau que chegaram a começar (um caso, numa
	// amostra), contando as interrompidas.
	Passagens int `json:"passagens"`
	// Interrompidas são as passagens que saíram das taxas por uma causa passageira
	// ([RegraDoDegrauCumprido]); InterrompidasPorCausa reparte-as pela causa.
	Interrompidas         int            `json:"passagens_interrompidas"`
	InterrompidasPorCausa map[string]int `json:"interrompidas_por_causa"`
	// DegrauCumprido é o agregado ([RegraDoDegrauCumprido]).
	DegrauCumprido Taxa `json:"degrau_cumprido"`
	// NosCumpridos conta os nós que fecharam o contrato de conclusão, sem olhar ao conteúdo.
	NosCumpridos Taxa `json:"nos_cumpridos"`
	// SemToolCallNaPrimeira e FactosAusentes são as taxas de [Taxas], sobre os runs do degrau.
	SemToolCallNaPrimeira Taxa `json:"sem_tool_call_na_primeira_tentativa"`
	FactosAusentes        Taxa `json:"factos_ausentes"`
	// RespostasErradas conta os runs concluídos em que a saída traz um facto proibido.
	RespostasErradas Taxa `json:"respostas_erradas"`
}

// TaxasPorDegrau é o que a experiência `degraus` acrescenta ao relatório, dentro das taxas de
// todas as observações ([Taxas.PorDegrau]).
type TaxasPorDegrau struct {
	VersaoDaBateria string `json:"versao_da_bateria"`
	// Regra é a regra do agregado «degrau cumprido», por extenso.
	Regra   string          `json:"regra_do_degrau_cumprido"`
	Degraus []TaxasDoDegrau `json:"degraus"`
}

// CalcularTaxasPorDegrau calcula as taxas de cada degrau da bateria sobre as observações de uma
// corrida. É determinista: a mesma lista dá os mesmos números.
func CalcularTaxasPorDegrau(b *Bateria, obs []Observacao) []TaxasDoDegrau {
	type passagem struct {
		caso    string
		amostra int
	}
	type unidade struct {
		passagem
		no string
	}
	porCaso := map[string][]Observacao{}
	passagens := map[passagem]bool{}
	// tentativas guarda, por nó de cada passagem, as suas tentativas, pela ordem da corrida. A
	// última é a que decide o nó: o banco não volta a tentar um nó que concluiu.
	tentativas := map[unidade][]Observacao{}
	for _, o := range obs {
		porCaso[o.Caso] = append(porCaso[o.Caso], o)
		ps := passagem{o.Caso, o.Amostra}
		passagens[ps] = true
		u := unidade{ps, o.No}
		tentativas[u] = append(tentativas[u], o)
	}
	ultima := func(ps passagem, no string) (Observacao, bool) {
		l := tentativas[unidade{ps, no}]
		if len(l) == 0 {
			return Observacao{}, false
		}
		return l[len(l)-1], true
	}
	// lerPassagem diz como uma passagem se conta. Com `causa` não vazia, SAI das taxas: foi
	// interrompida por uma causa passageira — a classificação é a da qualificação
	// ([estadoFinalDoNo]) — sem que nada estivesse já firmemente falhado. Sem causa, conta, e
	// `medidos` é o número de nós do caso que entram nas contas: todos, ou só os que vêm antes
	// da interrupção quando UMA FALHA FIRME JÁ TINHA SIDO OBSERVADA.
	//
	// Uma razão firme ganha a uma passageira, como na qualificação, porque as contagens só
	// crescem: um nó que não cumpriu por culpa do modelo, um facto ausente ou um proibido
	// presente num nó concluído não deixam de o ser por o nó seguinte ter levado um 429.
	lerPassagem := func(c Caso, ps passagem) (causa string, medidos int) {
		firme := false
		// semSaida são os nós que não deixaram saída por culpa do modelo: os que não concluíram
		// e, em cadeia, os que por isso não correram.
		semSaida := map[string]bool{}
		for i, n := range c.Nos {
			o, correu := ultima(ps, n.ID)
			switch {
			case correu:
				classe, motivo := estadoFinalDoNo(o)
				if classe != noInterrompido {
					semSaida[n.ID] = o.Desfecho != DesfechoCumprido
					firme = firme || semSaida[n.ID] || o.Factos == FactosAusentes || o.Proibidos == ProibidosPresente
					continue
				}
				// Interrompido. Uma tentativa anterior DESTE nó que falhou por culpa do modelo
				// (não pediu a tool, respondeu vazio) também é uma falha firme já observada.
				for _, anterior := range tentativas[unidade{ps, n.ID}] {
					if cl, _ := estadoFinalDoNo(anterior); cl == noNaoCumprido {
						return "", i + 1
					}
				}
				if firme {
					return "", i
				}
				return motivo, 0
			case semSaida[n.EntradaDe]:
				semSaida[n.ID] = true
			default:
				// Nada o impedia de correr: foi a corrida que parou antes.
				if firme {
					return "", i
				}
				return InterrompidaPelaParagem, 0
			}
		}
		return "", len(c.Nos)
	}
	var out []TaxasDoDegrau
	for d := 1; d <= b.Degraus(); d++ {
		t := TaxasDoDegrau{Degrau: d, InterrompidasPorCausa: map[string]int{}}
		var (
			doDegrau                         []Observacao
			capacidades                      []string
			nos, nosCumpridos, cumpridas     int
			comProibidos, respostasComErrado int
		)
		for _, c := range b.Casos {
			if c.Degrau != d {
				continue
			}
			t.Casos = append(t.Casos, c.ID)
			capacidades = append(capacidades, c.Descricao)
			medidas := map[int]bool{}
			for ps := range passagens {
				if ps.caso != c.ID {
					continue
				}
				t.Passagens++
				causa, medidos := lerPassagem(c, ps)
				if causa != "" {
					t.Interrompidas++
					t.InterrompidasPorCausa[causa]++
					continue
				}
				medidas[ps.amostra] = true
				// Uma passagem cortada por uma interrupção depois de uma falha firme nunca é inteira.
				inteira := medidos == len(c.Nos)
				for _, n := range c.Nos[:medidos] {
					nos++
					o, correu := ultima(ps, n.ID)
					fechou := correu && o.Desfecho == DesfechoCumprido
					if fechou {
						nosCumpridos++
					}
					if !fechou || o.Factos == FactosAusentes || o.Proibidos == ProibidosPresente {
						inteira = false
					}
				}
				if inteira {
					cumpridas++
				}
			}
			for _, o := range porCaso[c.ID] {
				if medidas[o.Amostra] {
					doDegrau = append(doDegrau, o)
				}
			}
		}
		for _, o := range doDegrau {
			if o.Proibidos == ProibidosNenhum || o.Proibidos == ProibidosPresente {
				comProibidos++
				if o.Proibidos == ProibidosPresente {
					respostasComErrado++
				}
			}
		}
		geral := CalcularTaxas(doDegrau)
		t.Capacidade = strings.Join(capacidades, "; ")
		t.DegrauCumprido = NovaTaxa("passagens nao interrompidas pelo degrau em que todos os nos do caso fecharam cumprido, com os factos exigidos na saida e sem nenhum facto proibido", cumpridas, t.Passagens-t.Interrompidas)
		t.NosCumpridos = NovaTaxa("nos das passagens nao interrompidas pelo degrau cuja ultima tentativa fechou cumprido (so o contrato de conclusao; nao olha ao conteudo). Tudo o que nao seja cumprido conta aqui: sem tool call, turnos esgotados, resposta vazia ou cortada, um 4xx de parametro. Um no que nao correu, porque o no de que depende nao concluiu, conta no denominador", nosCumpridos, nos)
		t.SemToolCallNaPrimeira, t.FactosAusentes = geral.SemToolCallNaPrimeira, geral.FactosAusentes
		t.RespostasErradas = NovaTaxa("runs concluidos de nos com factos proibidos, nas passagens nao interrompidas, em que pelo menos um facto proibido (um distractor do documento, um valor que a pergunta nao pede, ou o facto que a instrucao veda) esta na saida, comparado sem acentos, sem maiusculas, sem hifenes e sem espacos", respostasComErrado, comProibidos)
		out = append(out, t)
	}
	return out
}

// taxasPorDegrau monta o bloco por degrau do relatório.
func taxasPorDegrau(b *Bateria, obs []Observacao) *TaxasPorDegrau {
	return &TaxasPorDegrau{VersaoDaBateria: b.Versao, Regra: RegraDoDegrauCumprido, Degraus: CalcularTaxasPorDegrau(b, obs)}
}

// limitesDosDegraus são as frases, fixas, do que uma corrida da experiência `degraus` não prova.
func limitesDosDegraus() []string {
	return []string{
		"A experiencia degraus NAO da veredicto: nao qualifica nem recusa um modelo, e nao substitui a qualificacao (AOS-518), que corre sobre a bateria 1.0.0 com a sua regra.",
		"Os degraus sao sinteticos e curtos: um caso por degrau, documentos inventados de poucas linhas (salvo o do documento longo). Cumprir um degrau nao diz que o modelo faz o mesmo com documentos reais, e a ordem dos degraus e a que quem escreveu a bateria julgou crescente, nao uma escala medida: o degrau 6 (uma restricao sobre um documento curto) e provavelmente mais facil do que os degraus 3 a 5.",
		"«Degrau cumprido» e verificacao de factos por comparacao de texto, nao um juiz: diz que os valores exigidos estao na saida e os proibidos nao. Nao diz que a resposta e boa, nem que respeita o resto da instrucao (a forma, o tamanho, a lingua).",
		"Um facto proibido conta como presente onde quer que apareca na saida, mesmo numa negacao (a resposta que diz que NAO e um certo codigo): a comparacao nao le o sentido. Uma resposta errada que nao seja um dos proibidos listados conta como facto ausente, nao como resposta errada.",
		"O degrau 5 mede «chega ao facto apesar da recusa», nao «fez a segunda chamada»: um modelo que va directo a copia, sem tentar o documento negado, cumpre-o.",
		"Os degraus correm sempre pela mesma ordem, do 1 ao 7, dentro de cada passagem: uma deriva da rota ao longo da corrida (carga, hora, um limite de taxa) cai nos ultimos degraus, e nao se reparte por todos.",
		"Uma passagem interrompida por uma causa passageira (429, 5xx, erro de transporte, conta ou rota, tecto, corrida parada) sai das taxas do degrau e conta a parte, salvo se ja tinha uma falha firme observada antes da interrupcao: essa conta como nao cumprida. Um degrau com passagens interrompidas foi medido com menos amostras do que as pedidas; um so com interrompidas nao foi medido. No resumo, a linha de um degrau em que metade ou mais das passagens foram interrompidas vem marcada com «!»: a taxa nao e fiavel.",
		"Um modelo que estoura SEMPRE o prazo do pedido (tempo esgotado) sai como interrompido, pela classificacao da qualificacao: fica «nao medido», nao «falhado». Le-se pela coluna das interrompidas e pela causa erro_de_transporte, nao pela taxa.",
		"A causa cancelamento_da_corrida de interrompidas_por_causa nao se reconstitui a partir das observacoes deste JSON: nesta experiencia o cancelamento de um run fica em memoria e nao e serializado na observacao.",
		"No degrau 7, um erro do no extrair (deixar passar o que devia ficar de fora) pode contar ate tres vezes em respostas_erradas — uma por cada no que o recebe e o repete — e uma so vez em degrau_cumprido. Os nos reescrever e resumir nao tem exclusao no objectivo: os seus proibidos so chegam la se o extrair errou.",
		"Com uma passagem pela bateria, cada degrau tem uma amostra: a taxa e 0 ou 1 e o intervalo de 95% e quase todo o intervalo. Para comparar modelos por degrau e preciso --amostras, e ler os intervalos.",
	}
}

// blocoDosDegraus escreve a tabela curta por degrau no resumo em texto. Sem o bloco dos degraus
// no relatório não escreve nada.
func blocoDosDegraus(b *strings.Builder, r *Relatorio) {
	d := r.Taxas.PorDegrau
	if d == nil {
		return
	}
	fracao := func(t Taxa) string { return fmt.Sprintf("%d/%d", t.Numerador, t.Denominador) }
	fmt.Fprintf(b, "POR DEGRAU (bateria %s; sem veredicto — taxas por degrau; os intervalos de 95%% estao no JSON)\n", d.VersaoDaBateria)
	fmt.Fprintf(b, "  %-6s %-9s %-13s %-13s %-13s %-15s %-17s %s\n", "degrau", "cumprido", "interrompidas", "nos cumpridos", "sem tool 1.a", "factos ausentes", "respostas erradas", "capacidade")
	reduzidas := false
	for _, t := range d.Degraus {
		capacidade, _, _ := strings.Cut(t.Capacidade, ":")
		// A marca: metade ou mais das passagens do degrau saíram da amostra. «1/1» com nove
		// interrompidas em dez não é uma taxa em que se possa confiar, e a linha tem de o gritar.
		marca := " "
		if amostraReduzida(t) {
			marca, reduzidas = MarcaDeAmostraReduzida, true
		}
		fmt.Fprintf(b, "%s %-6d %-9s %-13s %-13s %-13s %-15s %-17s %s\n", marca, t.Degrau, fracao(t.DegrauCumprido),
			fmt.Sprintf("%d de %d", t.Interrompidas, t.Passagens), fracao(t.NosCumpridos),
			fracao(t.SemToolCallNaPrimeira), fracao(t.FactosAusentes), fracao(t.RespostasErradas), capacidade)
	}
	if reduzidas {
		fmt.Fprintf(b, "  %s = amostra reduzida por interrupcoes (metade ou mais das passagens do degrau sairam da amostra): a taxa nao e fiavel\n", MarcaDeAmostraReduzida)
	}
	fmt.Fprintf(b, "  REGRA DO DEGRAU CUMPRIDO: %s\n\n", d.Regra)
}

// MarcaDeAmostraReduzida abre, no resumo, a linha de um degrau com a amostra reduzida.
const MarcaDeAmostraReduzida = "!"

// amostraReduzida diz se metade ou mais das passagens de um degrau foram interrompidas.
func amostraReduzida(t TaxasDoDegrau) bool {
	return t.Interrompidas > 0 && 2*t.Interrompidas >= t.Passagens
}
