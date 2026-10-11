package bancoensaio

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// O ORÇAMENTO DE UMA QUALIFICAÇÃO (AOS-518, decisão D7 do dono, 2026-10-11).
//
// Três tectos, todos ao lado do tecto do dia, que se mantém e continua a contar pelo PIOR caso:
//
//   - 300 pedidos por qualificação ([LimiteDaQualificacao]), contados antes de cada envio;
//   - 3 USD estimados por qualificação, quando o fornecedor tem tecto em dólares no ficheiro de
//     chaves (é o que faz o contador estimar o gasto);
//   - duas qualificações por perfil por dia ([RegistarQualificacao]): repetir até passar é
//     escolher o resultado.

const (
	// TectoDePedidosDaQualificacao é o máximo de pedidos que uma qualificação envia.
	TectoDePedidosDaQualificacao = 300
	// TectoMicroUSDDaQualificacao é o máximo de gasto ESTIMADO de uma qualificação (3 USD).
	TectoMicroUSDDaQualificacao = 3_000_000
	// QualificacoesPorPerfilPorDia é o máximo de qualificações do mesmo perfil no mesmo dia (UTC).
	QualificacoesPorPerfilPorDia = 2
	// PassagensDoControlo é o número de passagens pela bateria da corrida de controlo sem devolução.
	PassagensDoControlo = 1
)

// ErrQualificacaoSemPerfil — a qualificação é de uma rota COM o seu perfil: sem perfil não há
// modelo esperado com que comparar o servido, nem digest que o veredicto possa citar.
var ErrQualificacaoSemPerfil = errors.New("banco-ensaio: a experiencia qualificacao exige um perfil candidato (--perfil)")

// LimiteDaQualificacao é o tecto próprio de pedidos de uma qualificação. Cada pedido é contado
// ANTES de sair, no transporte do nó de ensaio; ao chegar ao máximo, o pedido seguinte não sai.
type LimiteDaQualificacao struct {
	maximo int64

	mu       sync.Mutex
	enviados int64
	atingido bool
}

// NovoLimiteDaQualificacao devolve um limite com o máximo dado (zero ou negativo ⇒ nada sai).
func NovoLimiteDaQualificacao(maximo int64) *LimiteDaQualificacao {
	return &LimiteDaQualificacao{maximo: maximo}
}

// Reservar conta um pedido. Com o máximo já atingido devolve [ErrTectoAtingido]: o pedido não sai.
func (l *LimiteDaQualificacao) Reservar() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.enviados >= l.maximo {
		l.atingido = true
		return fmt.Errorf("%w: pedidos da qualificacao", ErrTectoAtingido)
	}
	l.enviados++
	return nil
}

// Enviados devolve quantos pedidos o limite deixou sair.
func (l *LimiteDaQualificacao) Enviados() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enviados
}

// Atingido diz se algum pedido foi recusado por este limite.
func (l *LimiteDaQualificacao) Atingido() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.atingido
}

// OrcamentoDaQualificacao é o orçamento de uma qualificação e o que ela gastou. Vai no relatório.
type OrcamentoDaQualificacao struct {
	// PiorCasoDeclarado é o máximo de pedidos que o plano podia fazer — a corrida, o controlo e a
	// sonda —, declarado antes do primeiro pedido e conferido contra o que restava do dia.
	PiorCasoDeclarado int64 `json:"pior_caso_declarado_de_pedidos"`
	// TectoDePedidos é o tecto próprio da qualificação; PedidosEnviados, o que ela enviou.
	TectoDePedidos  int64 `json:"tecto_de_pedidos_da_qualificacao"`
	PedidosEnviados int64 `json:"pedidos_enviados"`
	// TectoMicroUSD é o tecto de gasto estimado da qualificação. MicroUSDEstimado é o gasto
	// estimado desta corrida; nil quando o fornecedor não tem tecto em dólares (não há estimativa).
	TectoMicroUSD    int64  `json:"tecto_micro_usd_da_qualificacao"`
	MicroUSDEstimado *int64 `json:"micro_usd_estimado_desta_qualificacao"`
	// TectoMicroUSDImposto diz se o tecto em dólares actuou nesta corrida. É false quando o
	// fornecedor não tem tecto em dólares no ficheiro de chaves: aí o banco não estima o gasto, e
	// o tecto de 3 USD NÃO foi imposto (só o de pedidos).
	TectoMicroUSDImposto bool `json:"tecto_micro_usd_imposto"`
	// QualificacoesDoPerfilHoje é o número de ordem desta qualificação entre as do mesmo perfil no
	// mesmo dia; nil fora do modo real, que é o único com esse contador.
	QualificacoesDoPerfilHoje *int `json:"qualificacoes_do_perfil_hoje"`
	MaximoPorPerfilPorDia     int  `json:"maximo_de_qualificacoes_por_perfil_por_dia"`
	// O mesmo pela ROTA (fornecedor e modelo), com qualquer perfil.
	QualificacoesDaRotaHoje *int `json:"qualificacoes_da_rota_hoje"`
	MaximoPorRotaPorDia     int  `json:"maximo_de_qualificacoes_por_rota_por_dia"`
}

// ErrImagemDoProxy — a imagem do proxy em uso não é a declarada para produção.
var ErrImagemDoProxy = errors.New("banco-ensaio: a imagem do proxy em uso nao e a declarada para producao (causa: imagem_do_proxy_nao_e_a_de_producao); a qualificacao nao arranca")

// conferirImagemDoProxy recusa uma qualificação cuja imagem do proxy não é a declarada. A
// declaração é UMA só — [ImagemDoProxy], em proxy.go —, e é o guarda do AOS-517
// (aos517_digest_do_proxy_test.go) que a prende ao compose de produção. Uma qualificação feita
// atrás de outra imagem não vale: a imagem do proxy é um dos quatro factores da rota.
func conferirImagemDoProxy(emUso, declarada string) error {
	if emUso == "" || emUso != declarada || !strings.Contains(emUso, "@sha256:") {
		return ErrImagemDoProxy
	}
	return nil
}

// digestDaImagem devolve a parte `sha256:…` de uma referência de imagem fixada pelo digest.
func digestDaImagem(referencia string) string {
	if _, digest, tem := strings.Cut(referencia, "@"); tem {
		return digest
	}
	return ""
}
