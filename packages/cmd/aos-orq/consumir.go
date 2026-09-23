package main

// consumir.go — QUEM DRENA A FILA DE PEDIDOS DE PLANO.
//
// O `POST /plans` do nó grava um pedido e devolve `201 accepted`. Até aqui, nada o consumia: o
// `201` prometia uma corrida que nunca começava. Este comando é a outra metade (AOS-423,
// ADR-030).
//
// # DRENA UMA VEZ E TERMINA, E ISSO É DELIBERADO
//
// Não é um serviço de longa duração. Reclama, corre, reporta, repete — e termina quando a fila
// não tem mais nada para este consumidor.
//
// A razão é que a FORMA DO TRABALHADOR não está decidida (ADR-030 §4), e um comando drenável não
// obriga a decidir: quem o invoca pode ser um timer do host (há o precedente do
// `aos-tls-sync.timer`) ou um laço de um serviço. O código é o mesmo nos dois casos, e nenhum dos
// dois exige que este binário seja o primeiro serviço de longa duração do AOS além do nó — com o
// healthcheck, o reinício e a observabilidade próprios que isso traria.
//
// # A TRADUÇÃO CÓDIGO→CLASSE VIVE AQUI, E NÃO NO NÓ
//
// Só este binário sabe o que os códigos de saída do `serve` significam. Pôr a tabela no nó
// obrigá-lo-ia a conhecer a semântica de saída do orquestrador — que é precisamente a fronteira
// do ADR-018. O nó valida o VOCABULÁRIO de classes; a decisão é daqui.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

// maxPedidosPorDrenagem limita quantos pedidos uma invocação consome.
//
// Sem tecto, uma fila grande faria uma invocação correr indefinidamente — e um timer que dispara
// enquanto a invocação anterior ainda corre dá dois consumidores, que é seguro (o `StatusDuplicate`
// arbitra) mas desperdiça. Com tecto, cada invocação termina e a seguinte continua de onde esta
// parou.
const maxPedidosPorDrenagem = 16

// classeDoDesfecho traduz o erro de um `serve` na classe que o nó regista.
//
// A TABELA, e porque a distinção é load-bearing (ADR-030 §2.6): confundir transitório com
// permanente dá um de dois defeitos, e ambos são piores do que a fila parada — um pedido perdido,
// ou um laço a retentar para sempre uma recusa determinista.
//
//	3 exitPosseNegada          TRANSITÓRIO  outro processo detém o lease do RUN; retenta-se
//	4 exitFenced               TRANSITÓRIO  a posse foi superada a meio; retenta-se
//	5 exitWALDetido            TRANSITÓRIO  outro escritor detém o STORE; retenta-se
//	8 exitNosEmVoo             TRANSITÓRIO  o prazo acabou com nós a correr; nova invocação retoma
//	6 exitPendenteDeAprovacao  AGUARDA      o plano espera um humano; nem retentativa nem desfecho
//	7 exitDecisaoRecusada      TERMINAL     houve decisão e foi NÃO; caso fechado
//	9 exitPlanoRecusado        TERMINAL     o planeador esgotou tentativas; não se retenta
//	0 (sem erro)               TERMINAL     o plano correu
//	1 exitErro                 TRANSITÓRIO  genérico — ver abaixo
//
// O GENÉRICO É TRANSITÓRIO, e é a escolha menos óbvia. Um erro que não soubemos classificar pode
// ser uma configuração má (que se repetirá) ou uma falha de rede (que não). Tratá-lo como
// terminal PERDE o pedido em silêncio, que é o defeito que este eixo existe para fechar; tratá-lo
// como transitório devolve-o à fila, onde fica visível e contável. O tecto de pendentes é o que
// impede isso de virar um laço infinito — e é a razão pela qual o tecto recusa em vez de
// descartar.
func classeDoDesfecho(codigo int) string {
	switch codigo {
	case exitOK, exitDecisaoRecusada, exitPlanoRecusado:
		return "terminal"
	case exitPendenteDeAprovacao:
		return "aguarda_humano"
	default:
		return "transitorio"
	}
}

// cmdConsume drena a fila de pedidos de plano do nó.
func cmdConsume(args []string) error {
	fs := flag.NewFlagSet("consume", flag.ContinueOnError)
	snapshot := fs.String("snapshot", "", "instantâneo de validação que o planeador consome (o mesmo do `serve --goal`)")
	maxPedidos := fs.Int("max", maxPedidosPorDrenagem, "número máximo de pedidos a consumir nesta invocação")
	planTimeout := fs.Duration("plan-timeout", prazoDoPlanoPorOmissao, "prazo de cada plano, passado ao `serve`")
	pollInterval := fs.Duration("poll-interval", intervaloDeSondagemPorOmissao, "intervalo de sondagem do executor, passado ao `serve`")
	worker := fs.String("worker", "", "identidade deste trabalhador, passada ao `serve`")
	var sub substrato
	sub.registarFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxPedidos < 1 {
		return errors.New("--max tem de ser positivo")
	}
	// O SUBSTRATO valida-se AQUI e não só dentro de cada `serve`: uma invocação que não sabe
	// escrever em lado nenhum não deve reclamar um pedido do nó — reclamar e falhar a seguir
	// gasta uma geração por nada.
	if err := sub.validar(); err != nil {
		return err
	}

	cli, err := nodeClientDoAmbiente()
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("consume exige AOS_ORQ_NODE_URL: a fila vive no nó, e sem o canal para o " +
			"nó não há nada para reclamar")
	}

	ctx := context.Background()
	consumidos := 0
	for consumidos < *maxPedidos {
		pedido, houve, err := cli.ReclamarPedido(ctx)
		if err != nil {
			return fmt.Errorf("reclamar pedido: %w", err)
		}
		if !houve {
			break // fila vazia para este consumidor — o desfecho normal
		}
		consumidos++
		fmt.Printf("reclamado: run=%s geracao=%d objectivo=%q\n", pedido.RunID, pedido.Geracao, pedido.Objective)

		erroDoServe := correrPedido(*snapshot, pedido, sub, *planTimeout, *pollInterval, *worker)
		codigo := codigoDe(erroDoServe)
		classe := classeDoDesfecho(codigo)
		detalhe := ""
		if erroDoServe != nil {
			detalhe = erroDoServe.Error()
		}
		fmt.Printf("desfecho: run=%s codigo=%d classe=%s\n", pedido.RunID, codigo, classe)

		// O DESFECHO REPORTA-SE SEMPRE, mesmo quando o `serve` falhou. Não reportar deixa o
		// pedido preso até ao TTL da reclamação — meia hora de silêncio por uma falha que já
		// conhecemos.
		if err := cli.ReportarDesfecho(ctx, pedido.RunID, pedido.Geracao, classe, codigo, detalhe); err != nil {
			// Falhar a reportar NÃO é fatal para os pedidos seguintes: o TTL recupera este.
			// Mas é ruidoso de propósito — um consumidor que não consegue reportar está a
			// trabalhar às cegas.
			fmt.Fprintf(os.Stderr, "aos-orq: desfecho de %s NAO reportado (%v); o pedido volta a "+
				"fila quando a reclamacao expirar\n", pedido.RunID, err)
		}
	}

	if consumidos == 0 {
		fmt.Println("fila vazia: nada a consumir")
	} else {
		fmt.Printf("drenagem terminada: %d pedido(s) consumido(s)\n", consumidos)
	}
	return nil
}

// correrPedido corre UM pedido pelo mesmo caminho que um `serve --goal` manual.
//
// Reutiliza o `cmdServe` em vez de reimplementar o pipeline: o pedido tem de atravessar
// exactamente a mesma governação que uma invocação à mão — posse por lease, decomposição
// governada, gate de aprovação, executor de nós. Um caminho paralelo seria um segundo sítio onde
// a governação podia divergir, que é a forma de defeito que o AOS-424 e o AOS-425 passaram a
// série inteira a encontrar.
func correrPedido(snapshot string, p pedidoReclamado, sub substrato, planTimeout, pollInterval time.Duration, worker string) error {
	args := []string{"--run", p.RunID, "--goal", p.Objective}
	if snapshot != "" {
		args = append(args, "--snapshot", snapshot)
	}
	if worker != "" {
		args = append(args, "--worker", worker)
	}
	args = append(args, "--plan-timeout", planTimeout.String(), "--poll-interval", pollInterval.String())
	args = append(args, sub.comoFlags()...)
	return cmdServe(args)
}
