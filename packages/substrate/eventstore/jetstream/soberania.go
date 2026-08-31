package jetstream

import (
	"fmt"
	"strings"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// soberania.go — a fronteira regional do board no substrato replicado (AC5 do AOS-100,
// ADR-011).
//
// # A promessa, e o que a torna verdadeira
//
// «As réplicas e os backups NUNCA cruzam a fronteira regional de soberania.» No store de
// referência isso é decidido em processo: as réplicas são objectos nossos e recusamos
// construir o cluster se alguma estiver fora da região. Num substrato REPLICADO as
// réplicas não são nossas — são pares do cluster, colocados pelo servidor. A fronteira só
// é real se o SERVIDOR a impuser, e a via para isso é a `placement` do stream.
//
// # Porque isto se LÊ DE VOLTA em vez de se dar por assente
//
// Pedir colocação e assumir que foi aplicada seria o mesmo erro de método que o repo já
// pagou. Há um caminho em que o pedido nem chega a ser feito: ligar-se a um stream JÁ
// EXISTENTE (SemCriarStream, ou uma corrida em que outro processo o criou primeiro). Esse
// stream pode ter sido criado SEM colocação — e então um nó com fronteira declarada
// ligar-se-ia a ele e julgar-se-ia soberano. É a falha mais silenciosa possível, porque
// tudo funciona: as escritas passam, as leituras passam, e os dados estão onde não podiam
// estar.
//
// Por isso a fronteira é VERIFICADA contra a configuração armazenada, depois de abrir, e
// uma divergência ABORTA fail-closed.

// TagDeRegiao é a convenção que liga a fronteira declarada no AOS às `server_tags` do
// cluster. Um servidor elegível para a região "eu-west" declara
// `server_tags: ["region:eu-west"]`.
//
// É convenção, não protocolo: o JetStream só compara strings. Fica NOMEADA aqui porque é
// o contrato entre a configuração do nó e a do cluster, e um desencontro entre as duas
// manifesta-se como «no suitable peers» na criação — que é o fail-closed certo, mas só é
// legível para quem souber que a convenção existe.
const TagDeRegiao = "region:"

// ComRegiao declara a fronteira regional de soberania do board (ADR-011): a região onde
// os dados deste store podem residir.
//
// Espelha [eventstore.WithRegion] — as duas implementações têm de significar o MESMO,
// senão a fronteira depende do substrato, que é precisamente o que não pode acontecer.
// Região vazia com fronteira activada é rejeitada (região desconhecida ⇒ deny).
func ComRegiao(regiao string) Option {
	return func(c *config) { c.regiao = regiao; c.fronteira = true }
}

// ComBoardDeSoberania declara a soberania por board: associa o id do board à sua região
// autorizada. Equivale a [ComRegiao] para efeitos de fronteira; o id do board é retido
// como rótulo de observabilidade e NÃO participa na decisão.
func ComBoardDeSoberania(board, regiao string) Option {
	return func(c *config) { c.board = board; c.regiao = regiao; c.fronteira = true }
}

// normalizarRegiao usa a MESMA normalização do store de referência (minúsculas, sem
// espaços em redor). Duas normalizações diferentes fariam "EU-West" e "eu-west" ser a
// mesma região num substrato e regiões diferentes no outro.
func normalizarRegiao(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// tagDaRegiao devolve a tag de colocação correspondente à região.
func tagDaRegiao(regiao string) string { return TagDeRegiao + regiao }

// colocacaoExigida devolve a Placement que a fronteira impõe, ou nil se não há fronteira.
func (c config) colocacaoExigida() *natsjs.Placement {
	if !c.fronteira {
		return nil
	}
	return &natsjs.Placement{Tags: []string{tagDaRegiao(normalizarRegiao(c.regiao))}}
}

// validarFronteira recusa uma fronteira declarada sem região. Fail-closed: uma região
// ausente é uma região DESCONHECIDA, e uma região desconhecida nega.
func (c config) validarFronteira() error {
	if c.fronteira && normalizarRegiao(c.regiao) == "" {
		return fmt.Errorf("%w: fronteira de soberania declarada sem região (região desconhecida ⇒ deny)",
			eventstore.ErrSovereigntyViolation)
	}
	return nil
}

// verificarColocacao confirma, contra a configuração ARMAZENADA no servidor, que o stream
// está de facto confinado à região do board.
//
// Cobre os três modos de falha que importam, e nenhum deles é hipotético:
//
//  1. o stream existia SEM colocação e ligámo-nos a ele (o caso silencioso);
//  2. o stream existia com colocação de OUTRA região (dois boards a partilhar nome);
//  3. o servidor aceitou a criação mas guardou outra coisa (não deveria acontecer — e é
//     exactamente por isso que se verifica em vez de se confiar).
func verificarColocacao(lida natsjs.StreamConfigLida, regiao, stream string) error {
	exigida := tagDaRegiao(regiao)
	if lida.Placement == nil || len(lida.Placement.Tags) == 0 {
		return fmt.Errorf("%w: o stream %q está SEM colocação no servidor, e a fronteira do board exige %q — "+
			"as réplicas podem estar em qualquer par do cluster",
			eventstore.ErrSovereigntyViolation, stream, exigida)
	}
	for _, t := range lida.Placement.Tags {
		if normalizarRegiao(t) == exigida {
			return nil
		}
	}
	return fmt.Errorf("%w: o stream %q está colocado em %v e a fronteira do board exige %q",
		eventstore.ErrSovereigntyViolation, stream, lida.Placement.Tags, exigida)
}

// Regiao devolve a região da fronteira de soberania, ou "" se não há fronteira.
func (s *Store) Regiao() string { return s.regiao }

// BoardDeSoberania devolve o id do board associado à fronteira, ou "" se a fronteira foi
// declarada sem board ou não há fronteira. É rótulo de observabilidade; a decisão da
// fronteira é da região.
func (s *Store) BoardDeSoberania() string { return s.board }
