package main

// aos423_consumir_test.go — A TRADUÇÃO CÓDIGO→CLASSE É A DECISÃO MAIS CONSEQUENTE DESTE COMANDO.
//
// Confundir transitório com permanente dá um de dois defeitos, e ambos são piores do que a fila
// parada: um pedido perdido em silêncio, ou um laço a retentar para sempre uma recusa
// determinista. Esta tabela é onde essa decisão vive.

import (
	"strings"
	"testing"
)

// CADA CÓDIGO DE SAÍDA DO `serve` TEM CLASSE, E A CLASSE ESTÁ CERTA.
func TestAOS423ClasseDeCadaCodigoDeSaida(t *testing.T) {
	casos := []struct {
		codigo int
		nome   string
		classe string
		porque string
	}{
		{exitOK, "exitOK", "terminal", "o plano correu; nada a retentar"},
		{exitPosseNegada, "exitPosseNegada", "transitorio", "outro processo detem o lease do RUN; passa"},
		{exitFenced, "exitFenced", "transitorio", "a posse foi superada a meio; passa"},
		{exitWALDetido, "exitWALDetido", "transitorio", "outro escritor detem o STORE; passa"},
		{exitNosEmVoo, "exitNosEmVoo", "transitorio", "o prazo acabou com nos a correr; nova invocacao retoma"},
		{exitPendenteDeAprovacao, "exitPendenteDeAprovacao", "aguarda_humano", "ninguem decidiu ainda"},
		{exitDecisaoRecusada, "exitDecisaoRecusada", "terminal", "houve decisao e foi NAO; caso fechado"},
		{exitPlanoRecusado, "exitPlanoRecusado", "terminal", "o planeador esgotou tentativas"},
	}
	for _, c := range casos {
		if got := classeDoDesfecho(c.codigo); got != c.classe {
			t.Errorf("classeDoDesfecho(%s=%d) = %q, esperava %q — %s",
				c.nome, c.codigo, got, c.classe, c.porque)
		}
	}
}

// O GENÉRICO É TRANSITÓRIO, e é a escolha menos óbvia deste ficheiro.
//
// Um erro que não soubemos classificar pode ser configuração má (repete-se) ou rede (não).
// Tratá-lo como terminal PERDE o pedido em silêncio, que é o defeito que este eixo existe para
// fechar; tratá-lo como transitório devolve-o à fila, onde fica visível e contável. O tecto de
// pendentes é o que impede isso de virar um laço infinito.
func TestAOS423CodigoDesconhecidoEeTransitorio(t *testing.T) {
	for _, codigo := range []int{exitErro, 42, 255, -1} {
		if got := classeDoDesfecho(codigo); got != "transitorio" {
			t.Errorf("classeDoDesfecho(%d) = %q, esperava transitorio:\n"+
				"um codigo que nao sabemos ler nao pode PERDER o pedido — devolve-se a fila,\n"+
				"onde fica visivel, e o tecto de pendentes trava o laco", codigo, got)
		}
	}
}

// AS CLASSES SÃO AS QUE O NÓ ACEITA. O vocabulário é fechado do outro lado (`handlePlanOutcome`
// recusa 400 a uma classe que não conheça), e um desalinhamento aqui só apareceria em produção,
// como um desfecho que o nó rejeita e um pedido preso até ao TTL.
func TestAOS423VocabularioDeClassesCasaComONo(t *testing.T) {
	// Escritas à MÃO de propósito: o `aos-orq` não importa o nó (são binários distintos), e um
	// teste que derivasse os valores da mesma constante não provaria que as duas pontas
	// concordam. Se o nó mudar o vocabulário, isto tem de ficar vermelho.
	doNo := map[string]bool{"transitorio": true, "terminal": true, "aguarda_humano": true}
	for _, codigo := range []int{exitOK, exitErro, exitPosseNegada, exitFenced, exitWALDetido,
		exitPendenteDeAprovacao, exitDecisaoRecusada, exitNosEmVoo, exitPlanoRecusado} {
		if c := classeDoDesfecho(codigo); !doNo[c] {
			t.Errorf("o codigo %d produz a classe %q, que o no NAO aceita (400): o desfecho nunca "+
				"seria registado e o pedido ficava preso ate ao TTL", codigo, c)
		}
	}
}

// O SUBSTRATO ATRAVESSA INTACTO PARA O `serve`.
//
// O `consume` recebe o substrato e tem de o passar ao `serve` que invoca. Reconstruir a lista à
// mão é onde uma opção se perde em silêncio — e uma opção de substrato perdida é a diferença
// entre escrever no store certo e no errado.
func TestAOS423SubstratoAtravessaIntacto(t *testing.T) {
	casos := []struct {
		nome  string
		s     substrato
		exige []string
		nega  []string
	}{
		{"wal", substrato{wal: "/tmp/x.wal"}, []string{"--wal", "/tmp/x.wal"}, []string{"--nats"}},
		{
			"nats completo",
			substrato{nats: "h:4222", stream: "S", replicas: 3, regiao: "eu-west"},
			[]string{"--nats", "h:4222", "--nats-stream", "S", "--nats-replicas", "3", "--nats-region", "eu-west"},
			[]string{"--wal"},
		},
		{"vazio", substrato{}, nil, []string{"--wal", "--nats"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := strings.Join(c.s.comoFlags(), " ")
			for _, e := range c.exige {
				if !strings.Contains(got, e) {
					t.Errorf("comoFlags() = %q, faltava %q", got, e)
				}
			}
			for _, n := range c.nega {
				if strings.Contains(got, n) {
					t.Errorf("comoFlags() = %q, nao devia conter %q", got, n)
				}
			}
		})
	}
}

// E COBRE TODAS AS FLAGS QUE O `registarFlags` DECLARA.
//
// É o inverso de uma função, e a forma de falha é acrescentar um campo ao `substrato`, registá-lo
// como flag, e esquecer de o devolver aqui — nessa altura o `consume` invoca o `serve` sem ele e
// nada avisa.
func TestAOS423ComoFlagsCobreTodasAsFlagsDoSubstrato(t *testing.T) {
	cheio := substrato{wal: "w", nats: "n", stream: "s", replicas: 9, regiao: "r"}
	got := strings.Join(cheio.comoFlags(), " ")
	for _, flag := range []string{"--wal", "--nats", "--nats-stream", "--nats-replicas", "--nats-region"} {
		if !strings.Contains(got, flag) {
			t.Errorf("comoFlags() nao devolve %s.\n"+
				"Se acrescentou um campo ao substrato e o registou em registarFlags, devolva-o\n"+
				"tambem aqui: o `consume` passa o substrato ao `serve` por esta lista, e uma\n"+
				"opcao em falta escreve no store errado sem avisar.", flag)
		}
	}
}

// O `consume` EXIGE O CANAL PARA O NÓ. A fila vive no nó; sem canal não há nada para reclamar, e
// reclamar não é opcional como o executor de nós é.
func TestAOS423ConsumeExigeOCanalParaONo(t *testing.T) {
	t.Setenv("AOS_ORQ_NODE_URL", "")
	err := cmdConsume([]string{"--wal", "/tmp/aos423-nao-usado.wal"})
	if err == nil {
		t.Fatal("o consume sem AOS_ORQ_NODE_URL tinha de recusar")
	}
	if !strings.Contains(err.Error(), "AOS_ORQ_NODE_URL") {
		t.Errorf("a recusa devia nomear a variavel em falta, veio: %v", err)
	}
}

// E VALIDA O SUBSTRATO ANTES DE RECLAMAR.
//
// Reclamar e falhar a seguir gasta uma geração por nada, e o pedido fica preso até ao TTL por uma
// razão que já se conhecia antes de o pedir.
func TestAOS423ConsumeValidaOSubstratoAntesDeReclamar(t *testing.T) {
	t.Setenv("AOS_ORQ_NODE_URL", "http://localhost:1")
	err := cmdConsume(nil)
	if err == nil {
		t.Fatal("o consume sem --wal nem --nats tinha de recusar")
	}
	if !strings.Contains(err.Error(), "--wal") {
		t.Errorf("esperava a recusa do substrato ANTES de tocar na rede, veio: %v", err)
	}
}
