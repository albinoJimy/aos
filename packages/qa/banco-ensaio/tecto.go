package bancoensaio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// OS TECTOS DO DIA E O CONTADOR PERSISTENTE (AOS-512).
//
// # A REGRA
//
// Cada pedido ao fornecedor é CONTADO ANTES DE SER ENVIADO, num ficheiro em disco, por
// fornecedor e por dia (UTC). O transporte HTTP do nó de ensaio chama [Contador.Reservar] antes
// de cada pedido; se a reserva falha — tecto atingido, ficheiro ilegível, escrita falhada — o
// pedido NÃO SAI. É fail-closed em todos os ramos: não há caminho em que um erro do contador
// deixe passar um pedido.
//
// # O QUE O CONTADOR NÃO FAZ
//
// Não recomeça do zero. Um ficheiro que existe e não se lê, ou que não tem a forma esperada,
// recusa o arranque e todos os pedidos: quem o repara é o dono, à mão. Só um ficheiro que
// ainda NÃO EXISTE é criado, a zero — é o primeiro uso.
//
// # O GASTO EM DÓLARES É UMA ESTIMATIVA
//
// O tecto em dólares usa os tokens que o fornecedor devolve no `usage` e a tabela de preços
// declarada na configuração do banco ([Preco]). Não é a factura. Por isso o gasto de um pedido
// só se conhece DEPOIS da resposta, e o tecto em dólares pode ser ultrapassado pelo custo de um
// pedido — o último antes de parar. O tecto de pedidos, esse, é exacto.

// Tectos são os tectos do dia de um fornecedor.
type Tectos struct {
	// PedidosDia é o número máximo de pedidos por dia. Tem de ser positivo: não há omissão.
	PedidosDia int64
	// MicroUSDDia é o tecto de gasto estimado por dia, em micro-USD. Só vale com TemUSD.
	MicroUSDDia int64
	// TemUSD diz que o dono definiu um tecto em dólares.
	TemUSD bool
}

// Preco é o preço declarado de um modelo, em micro-USD por milhão de tokens.
type Preco struct {
	EntradaMicroUSDPorMTok int64 `json:"entrada_micro_usd_por_mtok"`
	SaidaMicroUSDPorMTok   int64 `json:"saida_micro_usd_por_mtok"`
}

// TabelaDePrecos é a tabela de preços declarada na configuração do banco: um ficheiro JSON
// `{"modelos": {"<modelo>": {"entrada_micro_usd_por_mtok": N, "saida_micro_usd_por_mtok": M}}}`.
// O banco não traz preços: quem os declara é quem corre o ensaio, com a tabela do fornecedor à
// frente.
type TabelaDePrecos struct {
	Modelos map[string]Preco `json:"modelos"`
}

// LerTabelaDePrecos lê a tabela de preços de um ficheiro.
func LerTabelaDePrecos(caminho string) (*TabelaDePrecos, error) {
	cru, err := os.ReadFile(caminho)
	if err != nil {
		return nil, fmt.Errorf("banco-ensaio: tabela de precos ilegivel: %w", err)
	}
	var t TabelaDePrecos
	if err := json.Unmarshal(cru, &t); err != nil {
		return nil, fmt.Errorf("banco-ensaio: tabela de precos mal formada: %w", err)
	}
	return &t, nil
}

// ErrSemPreco — há um tecto em dólares e o modelo não tem preço declarado. O arranque é
// recusado: sem preço, o tecto em dólares não se conseguia verificar.
var ErrSemPreco = errors.New("banco-ensaio: ha um tecto em dolares e o modelo nao tem preco declarado (entrada e saida, positivos) na tabela de precos")

// PrecoDe devolve o preço de um modelo. Tabela nil, modelo ausente ou preço não positivo ⇒
// [ErrSemPreco].
func (t *TabelaDePrecos) PrecoDe(modelo string) (Preco, error) {
	if t == nil {
		return Preco{}, ErrSemPreco
	}
	p, ok := t.Modelos[modelo]
	if !ok || p.EntradaMicroUSDPorMTok <= 0 || p.SaidaMicroUSDPorMTok <= 0 {
		return Preco{}, ErrSemPreco
	}
	return p, nil
}

// Os erros do contador. Todos impedem o pedido.
var (
	// ErrTectoAtingido — o tecto do dia foi atingido. A corrida pára.
	ErrTectoAtingido = errors.New("banco-ensaio: tecto do dia atingido")
	// ErrContador — o contador não se lê, não tem a forma esperada ou não se consegue gravar.
	ErrContador = errors.New("banco-ensaio: contador de pedidos inutilizavel (fail-closed: nenhum pedido sai)")
	// ErrSemTecto — o tecto de pedidos não é positivo.
	ErrSemTecto = errors.New("banco-ensaio: tecto de pedidos ausente ou nao positivo (nao ha tecto por omissao)")
	// ErrNaoCabe — a corrida precisa de mais pedidos do que o que resta do tecto do dia.
	ErrNaoCabe = errors.New("banco-ensaio: a corrida nao cabe no que resta do tecto do dia")
)

// versaoDoContador é a versão do formato do ficheiro do contador.
const versaoDoContador = 1

// usoDoDia é o que um fornecedor gastou num dia.
type usoDoDia struct {
	Pedidos  int64 `json:"pedidos"`
	MicroUSD int64 `json:"micro_usd_estimado"`
}

// ficheiroDoContador é o conteúdo do ficheiro: dia (UTC, `2006-01-02`) → fornecedor → uso.
type ficheiroDoContador struct {
	Versao int                            `json:"versao"`
	Dias   map[string]map[string]usoDoDia `json:"dias"`
}

// Contador é o contador persistente de um fornecedor. Seguro para uso concorrente dentro do
// processo; NÃO coordena dois processos (não corras dois ensaios ao mesmo tempo sobre o mesmo
// ficheiro).
type Contador struct {
	caminho    string
	fornecedor Fornecedor
	tectos     Tectos
	preco      Preco
	relogio    func() time.Time

	mu       sync.Mutex
	esgotado bool
	enviados int64
}

// AbrirContador abre (ou cria, se ainda não existir) o contador no caminho dado, para um
// fornecedor e os seus tectos. Recusa: tecto de pedidos não positivo ([ErrSemTecto]); tecto em
// dólares sem preço ([ErrSemPreco]); ficheiro existente que não se lê ([ErrContador]).
func AbrirContador(caminho string, f Fornecedor, t Tectos, precos *TabelaDePrecos, modelo string, relogio func() time.Time) (*Contador, error) {
	if t.PedidosDia <= 0 {
		return nil, ErrSemTecto
	}
	c := &Contador{caminho: caminho, fornecedor: f, tectos: t, relogio: relogio}
	if c.relogio == nil {
		c.relogio = time.Now
	}
	if t.TemUSD {
		if t.MicroUSDDia <= 0 {
			return nil, ErrSemTecto
		}
		p, err := precos.PrecoDe(modelo)
		if err != nil {
			return nil, err
		}
		c.preco = p
	}
	if caminho == "" {
		return nil, fmt.Errorf("%w: sem caminho", ErrContador)
	}
	if _, err := os.Stat(caminho); errors.Is(err, os.ErrNotExist) {
		// Primeiro uso: cria-se a zero. É o ÚNICO caso em que o contador começa do zero.
		if err := os.MkdirAll(filepath.Dir(caminho), 0o700); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrContador, err)
		}
		if err := c.gravar(ficheiroDoContador{Versao: versaoDoContador, Dias: map[string]map[string]usoDoDia{}}); err != nil {
			return nil, err
		}
	}
	if _, err := c.ler(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Contador) dia() string { return c.relogio().UTC().Format("2006-01-02") }

// ler lê e valida o ficheiro. Qualquer falha é [ErrContador].
func (c *Contador) ler() (ficheiroDoContador, error) {
	cru, err := os.ReadFile(c.caminho)
	if err != nil {
		return ficheiroDoContador{}, fmt.Errorf("%w: %v", ErrContador, err)
	}
	var f ficheiroDoContador
	if err := json.Unmarshal(cru, &f); err != nil {
		return ficheiroDoContador{}, fmt.Errorf("%w: conteudo mal formado", ErrContador)
	}
	if f.Versao != versaoDoContador || f.Dias == nil {
		return ficheiroDoContador{}, fmt.Errorf("%w: versao ou forma desconhecida", ErrContador)
	}
	for _, porFornecedor := range f.Dias {
		for _, u := range porFornecedor {
			if u.Pedidos < 0 || u.MicroUSD < 0 {
				return ficheiroDoContador{}, fmt.Errorf("%w: contagem negativa", ErrContador)
			}
		}
	}
	return f, nil
}

// gravar escreve o ficheiro de forma atómica (ficheiro temporário na mesma pasta e rename).
func (c *Contador) gravar(f ficheiroDoContador) error {
	cru, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrContador, err)
	}
	tmp := c.caminho + ".tmp"
	if err := os.WriteFile(tmp, append(cru, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: %v", ErrContador, err)
	}
	if err := os.Rename(tmp, c.caminho); err != nil {
		return fmt.Errorf("%w: %v", ErrContador, err)
	}
	return nil
}

// Reservar conta UM pedido, ANTES de ele ser enviado. Devolve nil só depois de o ficheiro ter
// sido lido, verificado contra os tectos e REGRAVADO com o pedido contado. Qualquer erro
// significa que o pedido não pode sair.
func (c *Contador) Reservar() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.ler()
	if err != nil {
		return err
	}
	dia := c.dia()
	if f.Dias[dia] == nil {
		f.Dias[dia] = map[string]usoDoDia{}
	}
	u := f.Dias[dia][string(c.fornecedor)]
	if u.Pedidos >= c.tectos.PedidosDia {
		c.esgotado = true
		return fmt.Errorf("%w: pedidos", ErrTectoAtingido)
	}
	if c.tectos.TemUSD && u.MicroUSD >= c.tectos.MicroUSDDia {
		c.esgotado = true
		return fmt.Errorf("%w: gasto estimado", ErrTectoAtingido)
	}
	u.Pedidos++
	f.Dias[dia][string(c.fornecedor)] = u
	if err := c.gravar(f); err != nil {
		return err
	}
	c.enviados++
	return nil
}

// RegistarUso soma ao gasto estimado do dia o custo de uma resposta, pelos tokens do `usage`.
// Sem tecto em dólares não faz nada.
func (c *Contador) RegistarUso(tokensDeEntrada, tokensDeSaida int64) error {
	if !c.tectos.TemUSD {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.ler()
	if err != nil {
		return err
	}
	dia := c.dia()
	if f.Dias[dia] == nil {
		f.Dias[dia] = map[string]usoDoDia{}
	}
	u := f.Dias[dia][string(c.fornecedor)]
	u.MicroUSD += custoMicroUSD(c.preco, tokensDeEntrada, tokensDeSaida)
	f.Dias[dia][string(c.fornecedor)] = u
	return c.gravar(f)
}

// custoMicroUSD estima o custo de uma resposta, arredondado para cima ao micro-USD.
func custoMicroUSD(p Preco, entrada, saida int64) int64 {
	if entrada < 0 {
		entrada = 0
	}
	if saida < 0 {
		saida = 0
	}
	total := entrada*p.EntradaMicroUSDPorMTok + saida*p.SaidaMicroUSDPorMTok
	return (total + 999_999) / 1_000_000
}

// Estado é a leitura do contador para o relatório.
type Estado struct {
	Dia              string
	PedidosHoje      int64
	TectoPedidos     int64
	PedidosRestantes int64
	TemUSD           bool
	MicroUSDHoje     int64
	TectoMicroUSD    int64
	MicroUSDRestante int64
	// EnviadosNestaCorrida são os pedidos que ESTE processo reservou.
	EnviadosNestaCorrida int64
}

// Estado lê o contador.
func (c *Contador) Estado() (Estado, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.ler()
	if err != nil {
		return Estado{}, err
	}
	dia := c.dia()
	u := f.Dias[dia][string(c.fornecedor)]
	e := Estado{
		Dia: dia, PedidosHoje: u.Pedidos, TectoPedidos: c.tectos.PedidosDia,
		PedidosRestantes: max(c.tectos.PedidosDia-u.Pedidos, 0),
		TemUSD:           c.tectos.TemUSD, MicroUSDHoje: u.MicroUSD, TectoMicroUSD: c.tectos.MicroUSDDia,
		EnviadosNestaCorrida: c.enviados,
	}
	if c.tectos.TemUSD {
		e.MicroUSDRestante = max(c.tectos.MicroUSDDia-u.MicroUSD, 0)
	}
	return e, nil
}

// Cabe verifica, ANTES de a corrida começar, que o número de pedidos que ela pode fazer cabe no
// que resta do tecto do dia. Não cabe ⇒ [ErrNaoCabe], e a corrida não arranca.
func (c *Contador) Cabe(pedidos int64) error {
	e, err := c.Estado()
	if err != nil {
		return err
	}
	if pedidos > e.PedidosRestantes {
		return fmt.Errorf("%w: precisa de ate %d pedidos e restam %d de %d", ErrNaoCabe, pedidos, e.PedidosRestantes, e.TectoPedidos)
	}
	if e.TemUSD && e.MicroUSDRestante <= 0 {
		return fmt.Errorf("%w: o tecto em dolares do dia ja foi atingido", ErrNaoCabe)
	}
	return nil
}

// Esgotado diz se alguma reserva deste processo bateu no tecto.
func (c *Contador) Esgotado() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.esgotado
}
