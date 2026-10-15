package bancoensaio

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// O CONSELHEIRO (AOS-528) — a bateria 3.0.0 «difícil» e a experiência `conselheiro`.
//
// # PORQUÊ
//
// A fase A4 mediu que uma cascata de modelos (o barato primeiro, o caro quando o barato falha)
// não se justificava com as tarefas que temos: o Claude Haiku cumpriu tudo. O dono quer avaliar
// outra forma — o CONSELHEIRO: o executor barato pode, a meio do run, consultar um modelo mais
// forte por uma tool ([ToolAconselhar]), e decide ele quando. Para se ver se compensa é preciso
// haver tarefas em que o barato falha e o caro acerta: a bateria 3.0.0 tem casos desenhados para
// isso — deduções em vários passos, documentos que se contradizem com uma regra de qual
// prevalece, um documento longo em que a resposta depende de duas passagens afastadas, várias
// restrições ao mesmo tempo.
//
// # A EXPERIÊNCIA
//
// Corre a bateria 3.0.0 em BRAÇOS, na mesma corrida e com a mesma semente — `so-executor` (sem a
// tool) e `executor+conselheiro` (com ela) —, sobre os mesmos casos, intercalados em blocos por
// amostra como os braços do protocolo. Dá, por braço e por caso, os casos cumpridos (a regra do
// degrau cumprido, [RegraDoCasoCumprido]), a taxa de consulta, as consultas por run, os pedidos e
// o custo estimado do executor e do conselheiro separados, e o custo por caso cumprido. Não dá
// veredicto.
//
// O TECTO — o modelo forte sozinho — não é um braço desta corrida: é outra corrida da mesma
// experiência, sem conselheiro e com o modelo forte como executor, com a mesma semente. O nó de
// ensaio tem um executor por corrida (uma rota, um alias, uma allowlist, um contador), e a
// bateria e a ordem são as mesmas: os casos comparam-se entre os dois relatórios.

//go:embed bateria3/casos.json bateria3/*.txt
var ficheirosDaBateriaDificil embed.FS

// pastaDaBateriaDificil é a pasta da bateria 3.0.0. Nenhum documento dela vem de fora.
const pastaDaBateriaDificil = "bateria3"

// ExperienciaConselheiro — a bateria difícil (3.0.0), em braços com e sem conselheiro.
const ExperienciaConselheiro Experiencia = "conselheiro"

// Os braços da experiência `conselheiro`.
const (
	// BracoSoExecutor — o executor sozinho: a tool `aconselhar` não é oferecida.
	BracoSoExecutor = "so-executor"
	// BracoComConselheiro — o executor com a tool `aconselhar`.
	BracoComConselheiro = "executor+conselheiro"
)

// MaxTurnosDoConselheiro é o tecto de turnos de um run da experiência `conselheiro`. Um executor
// que lê os três documentos de um caso um por turno, consulta duas vezes (o máximo por omissão)
// e responde gasta seis; oito deixam folga, sem que o braço com conselheiro fique a medir o
// tecto de turnos em vez da consulta.
const MaxTurnosDoConselheiro = 8

// CarregarBateriaDificil lê a bateria 3.0.0 embebida, valida-a e calcula o seu digest. As regras
// são as de [CarregarBateria] e mais estas: todos os casos dizem por extenso a capacidade que
// pedem; todos os nós têm factos exigidos E proibidos (as respostas erradas plausíveis); e
// nenhum facto vem escrito no objectivo, que é único (os providers falsos reconhecem o nó por
// ele).
func CarregarBateriaDificil() (*Bateria, error) {
	return carregarBateriaDificil(ficheirosDaBateriaDificil)
}

func carregarBateriaDificil(origem fs.FS) (*Bateria, error) {
	b, err := carregarDaPasta(origem, pastaDaBateriaDificil, false)
	if err != nil {
		return nil, err
	}
	b.dificil = true
	if err := b.validarDificil(); err != nil {
		return nil, err
	}
	return b, nil
}

// EDificil diz se a bateria é a difícil (3.0.0), a da experiência `conselheiro`.
func (b *Bateria) EDificil() bool { return b.dificil }

func (b *Bateria) validarDificil() error {
	var objectivos []string
	for _, c := range b.Casos {
		if strings.TrimSpace(c.Descricao) == "" {
			return fmt.Errorf("%w: %s nao diz por extenso a capacidade que pede", ErrBateria, c.ID)
		}
		for _, n := range c.Nos {
			if len(n.Factos) == 0 || len(n.FactosProibidos) == 0 {
				return fmt.Errorf("%w: %s/%s nao tem factos exigidos e factos proibidos (as respostas erradas plausiveis)", ErrBateria, c.ID, n.ID)
			}
			if err := validarObjectivo(c.ID, n, &objectivos); err != nil {
				return err
			}
		}
	}
	return nil
}

// PlanoDoConselheiro devolve o plano da experiência `conselheiro`: a bateria difícil inteira, no
// braço A do protocolo, `amostras` vezes, com até três tentativas por nó e
// [MaxTurnosDoConselheiro] turnos. Com `maxConsultas` positivo corre os dois braços; com zero, só
// o do executor sozinho (a corrida do tecto, ou a de um executor sem conselheiro).
//
// O PIOR CASO é o de [Plano.PedidosMaximos] para o executor (os dois braços) e o de
// [Plano.PedidosMaximosDoConselheiro] para o conselheiro: `maxConsultas` por run do braço com ele.
func PlanoDoConselheiro(b *Bateria, amostras int, semente uint64, maxConsultas int) Plano {
	p := PlanoDaBateria(b, BracoA, amostras)
	p.Experiencia, p.Semente, p.MaxTurnos = ExperienciaConselheiro, semente, MaxTurnosDoConselheiro
	p.BracosDoConselheiro = []string{BracoSoExecutor}
	if maxConsultas > 0 {
		p.BracosDoConselheiro = append(p.BracosDoConselheiro, BracoComConselheiro)
		p.MaxConsultas = maxConsultas
	}
	return p
}

// comConselheiro diz se o plano corre o braço com conselheiro.
func (p Plano) comConselheiro() bool {
	for _, b := range p.BracosDoConselheiro {
		if b == BracoComConselheiro {
			return true
		}
	}
	return false
}

// validarDoConselheiro confere o que é próprio do plano da experiência `conselheiro`, e que os
// outros não trazem nada dela.
func (p Plano) validarDoConselheiro(b *Bateria) error {
	if p.Experiencia != ExperienciaConselheiro {
		if len(p.BracosDoConselheiro) > 0 || p.MaxConsultas != 0 {
			return fmt.Errorf("%w: so a experiencia conselheiro tem bracos do conselheiro e consultas", ErrPlano)
		}
		return nil
	}
	if len(p.Bracos) != 1 || p.Bracos[0] != BracoA || p.Tentativas != 3 || p.MaxTurnos != MaxTurnosDoConselheiro || len(p.Casos) != len(b.Casos) {
		return fmt.Errorf("%w: a experiencia conselheiro corre a bateria dificil inteira, no braco A, com 3 tentativas e %d turnos", ErrPlano, MaxTurnosDoConselheiro)
	}
	so := len(p.BracosDoConselheiro) == 1 && p.BracosDoConselheiro[0] == BracoSoExecutor && p.MaxConsultas == 0
	dois := len(p.BracosDoConselheiro) == 2 && p.BracosDoConselheiro[0] == BracoSoExecutor && p.BracosDoConselheiro[1] == BracoComConselheiro &&
		p.MaxConsultas >= 1 && p.MaxConsultas <= MaxConsultasAceites
	if !so && !dois {
		return fmt.Errorf("%w: os bracos do conselheiro sao %s, ou %s e %s com 1 a %d consultas por run", ErrPlano, BracoSoExecutor, BracoSoExecutor, BracoComConselheiro, MaxConsultasAceites)
	}
	return nil
}

// PedidosMaximosDoConselheiro é o número MÁXIMO de pedidos ao conselheiro que a corrida pode
// fazer: por cada amostra, nó e tentativa do braço com ele, [Plano.MaxConsultas]. Zero sem esse
// braço. A sonda do conselheiro, quando há, é mais um.
func (p Plano) PedidosMaximosDoConselheiro(b *Bateria) (int64, error) {
	if err := p.validar(b); err != nil {
		return 0, err
	}
	if !p.comConselheiro() {
		return 0, nil
	}
	var nos int64
	for _, id := range p.Casos {
		c, _ := b.Caso(id)
		nos += int64(len(c.Nos))
	}
	return int64(p.Amostras) * nos * int64(p.Tentativas) * int64(p.MaxConsultas), nil
}

// ordemDosBracosDoConselheiro devolve os braços do conselheiro de uma amostra, baralhados pelo
// gerador (blocos aleatorizados, como os braços do protocolo). Sem braços do conselheiro devolve
// um só, vazio, e não toca no gerador: a ordem das outras experiências não muda.
func (p Plano) ordemDosBracosDoConselheiro(g *gerador) []string {
	if len(p.BracosDoConselheiro) == 0 {
		return []string{""}
	}
	ordem := append([]string(nil), p.BracosDoConselheiro...)
	for i := len(ordem) - 1; i > 0; i-- {
		j := int(g.proximo() % uint64(i+1)) // #nosec G115 -- o resto e menor do que i+1, que e um int positivo
		ordem[i], ordem[j] = ordem[j], ordem[i]
	}
	return ordem
}

// CfgDoConselho configura a experiência `conselheiro` numa corrida.
type CfgDoConselho struct {
	// Conselheiro é o conselheiro da corrida: o MESMO que o nó de ensaio regista na tool. Só com
	// o braço com conselheiro; nil na corrida só com o executor.
	Conselheiro *Conselheiro
	// Rota identifica o conselheiro no relatório (fornecedor, modelo e o digest da rota).
	Rota RotaDoRelatorio
	// PrecoDoExecutor e PrecoDoConselheiro são os preços declarados de cada modelo, para o custo
	// estimado por braço e por caso cumprido. nil ⇒ sem preço, e o custo não se estima.
	PrecoDoExecutor, PrecoDoConselheiro *Preco
}

// conselheiro devolve o conselheiro da corrida, ou nil.
func (cfg CfgDaCorrida) conselheiro() *Conselheiro {
	if cfg.Conselho == nil {
		return nil
	}
	return cfg.Conselho.Conselheiro
}

// validarConselho confere que a configuração do conselho é a do plano: só na experiência
// `conselheiro`, e com o conselheiro — o mesmo do nó de ensaio — se e só se o plano corre o braço
// com ele.
func (cfg CfgDaCorrida) validarConselho() error {
	if cfg.Plano.Experiencia != ExperienciaConselheiro {
		if cfg.Conselho != nil {
			return fmt.Errorf("%w: so a experiencia conselheiro tem conselho", ErrPlano)
		}
		return nil
	}
	if cfg.Conselho == nil {
		return fmt.Errorf("%w: a experiencia conselheiro precisa da configuracao do conselho", ErrPlano)
	}
	cs := cfg.Conselho.Conselheiro
	if cfg.Plano.comConselheiro() != (cs != nil) || cfg.No.conselheiro != cs {
		return fmt.Errorf("%w: o braco com conselheiro precisa do conselheiro, o mesmo no no de ensaio, e so ele o tem", ErrPlano)
	}
	if cs != nil && cs.maxConsultas != cfg.Plano.MaxConsultas {
		return fmt.Errorf("%w: o maximo de consultas do conselheiro nao e o do plano", ErrPlano)
	}
	return nil
}

// caberDoConselheiro confere, antes do primeiro pedido, que o pior caso do conselheiro — as
// consultas e a sonda dele — cabe no que resta do tecto do dia do fornecedor DELE. No mesmo
// fornecedor do executor o tecto é um só, e confere-se a soma com `doExecutor` (o pior caso do
// executor, com a sonda dele).
func (cfg CfgDaCorrida) caberDoConselheiro(doExecutor int64) error {
	cs := cfg.conselheiro()
	if cs == nil || cs.contador == nil {
		return nil
	}
	consultas, err := cfg.Plano.PedidosMaximosDoConselheiro(cfg.Bateria)
	if err != nil {
		return err
	}
	if cfg.Sondar {
		consultas++
	}
	if cfg.Contador != nil && cs.contador.fornecedor == cfg.Contador.fornecedor {
		return cfg.Contador.Cabe(doExecutor + consultas)
	}
	if err := cs.contador.Cabe(consultas); err != nil {
		return fmt.Errorf("%w (fornecedor do conselheiro: %s)", err, cs.contador.fornecedor)
	}
	return nil
}

// comParagemDoConselheiro junta a `parar` a paragem pelo conselheiro: o tecto do dia do
// fornecedor dele atingido, ou o contador dele inutilizável, param a corrida — as consultas
// seguintes já não sairiam, e o braço com conselheiro deixava de medir o conselheiro.
func (cfg CfgDaCorrida) comParagemDoConselheiro(parar func() string) func() string {
	cs := cfg.conselheiro()
	if cs == nil {
		return parar
	}
	return func() string {
		if parar != nil {
			if causa := parar(); causa != "" {
				return causa
			}
		}
		return cs.paragemDaCorrida()
	}
}
