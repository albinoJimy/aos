package main

// AOS-297 — UM HEARTBEAT >= TTL É CABLAGEM IMPOSSÍVEL, E TEM DE SER RECUSADA NA CONSTRUÇÃO.
//
// O defeito: [WithLeaseHeartbeat] aceitava qualquer intervalo > 0 sem o comparar com o TTL. O
// doc-comment PEDIA a propriedade — «deve ser confortavelmente inferior ao TTL» — e nada a
// impunha. Um WithLeaseHeartbeat(5*time.Minute) com DefaultLeaseTTL=2*time.Minute produz perda
// de posse determinística aos 2 min em TODOS os runs, e o nó arrancava sem uma palavra.
//
// PORQUE ISTO É PIOR DO QUE UM ERRO DE CONFIGURAÇÃO NORMAL. O sintoma aparece um TTL DEPOIS do
// arranque, e aparece como perda de posse — que é exactamente o que se espera ver quando uma
// réplica morre. Quem investiga procura a réplica que caiu, não a opção que a matou. A distância
// entre a causa e o sintoma é o que torna a validação em falta cara.
//
// A causa não era falta de acesso ao TTL: `cfg.ttl` e `cfg.hbInterval` vivem na MESMA struct e
// são lidos lado a lado em [NewNodeService]. Era validação em falta, e destoava do fail-closed de
// cablagem que o repositório aplica noutro sítio (breaker.ErrProgressSourceInert).
//
// ONDE A VALIDAÇÃO TEM DE VIVER, e porquê não no closure da opção: as NodeServiceOption são
// aplicadas POR ORDEM sobre a mesma config. Validar dentro de [WithLeaseHeartbeat] recusaria
// `NewNodeService(node, WithLeaseHeartbeat(time.Minute), WithLeaseTTL(10*time.Minute))` — um par
// perfeitamente legítimo — só porque o TTL ainda não tinha sido aplicado. O último teste deste
// ficheiro fixa precisamente essa ordem, para que ninguém "simplifique" a validação para dentro
// da opção sem que algo fique vermelho.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAOS297_HeartbeatMaiorOuIgualAoTTLERecusado é o caso do ticket: o par que garante perda de
// posse não constrói. Cobre `>` e `==` — o `==` importa porque a renovação chega no instante
// exacto da expiração, e uma corrida que se perde metade das vezes é pior do que uma que se
// perde sempre.
func TestAOS297_HeartbeatMaiorOuIgualAoTTLERecusado(t *testing.T) {
	casos := []struct {
		nome string
		ttl  time.Duration
		hb   time.Duration
	}{
		{nome: "heartbeat maior que o ttl", ttl: 2 * time.Minute, hb: 5 * time.Minute},
		{nome: "heartbeat igual ao ttl", ttl: 2 * time.Minute, hb: 2 * time.Minute},
		{nome: "heartbeat maior que o ttl por omissao", ttl: DefaultLeaseTTL, hb: DefaultLeaseTTL + time.Second},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			node := newTestNode(t, &countingModel{})
			svc, err := NewNodeService(node, WithLeaseTTL(c.ttl), WithLeaseHeartbeat(c.hb))
			if err == nil {
				t.Fatalf("NewNodeService aceitou heartbeat=%s com ttl=%s — a posse cairia em todos os runs", c.hb, c.ttl)
			}
			if svc != nil {
				t.Fatalf("NewNodeService devolveu um servico E um erro; esperado (nil, erro)")
			}
			if !errors.Is(err, ErrLeaseHeartbeatNotBelowTTL) {
				t.Fatalf("erro = %v, esperado que envolvesse ErrLeaseHeartbeatNotBelowTTL", err)
			}
			// A AC exige que o erro NOMEIE os dois valores: sem eles, quem lê o log sabe que
			// há um problema de cablagem e não sabe qual dos dois números corrigir.
			msg := err.Error()
			if !strings.Contains(msg, c.hb.String()) || !strings.Contains(msg, c.ttl.String()) {
				t.Fatalf("o erro tem de nomear heartbeat (%s) e ttl (%s); veio %q", c.hb, c.ttl, msg)
			}
		})
	}
}

// TestAOS297_HeartbeatAbaixoDoTTLEAceite fixa o outro lado. Sem ele, uma "validação" que
// recusasse tudo passaria o teste de cima e partia o nó — e é o modo de falha mais provável de
// uma guarda escrita à pressa.
func TestAOS297_HeartbeatAbaixoDoTTLEAceite(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseTTL(2*time.Minute), WithLeaseHeartbeat(30*time.Second))
	if err != nil {
		t.Fatalf("um heartbeat de 30s sob um TTL de 2m e legitimo e foi recusado: %v", err)
	}
	if svc == nil {
		t.Fatal("NewNodeService devolveu (nil, nil)")
	}
}

// TestAOS297_SemOpcaoODefaultContinuaAArrancar protege o caminho corrente — que é o único que
// existe hoje em produção, porque WithLeaseHeartbeat não tem chamador fora de testes. O default
// é TTL/3, derivado DEPOIS desta validação; se a guarda olhasse para o valor derivado em vez do
// explícito, este teste ficaria vermelho.
func TestAOS297_SemOpcaoODefaultContinuaAArrancar(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	if _, err := NewNodeService(node); err != nil {
		t.Fatalf("o arranque por omissao (heartbeat derivado de TTL/3) foi recusado: %v", err)
	}
}

// TestAOS297_OrdemDasOpcoesNaoDecide é o teste que amarra ONDE a validação vive. O par
// (1m, 10m) é legítimo nas duas ordens; se a validação migrasse para dentro do closure de
// [WithLeaseHeartbeat], a segunda sub-alínea passaria a recusar um par válido — porque nessa
// altura `cfg.ttl` ainda seria o default.
func TestAOS297_OrdemDasOpcoesNaoDecide(t *testing.T) {
	t.Run("ttl primeiro", func(t *testing.T) {
		node := newTestNode(t, &countingModel{})
		if _, err := NewNodeService(node, WithLeaseTTL(10*time.Minute), WithLeaseHeartbeat(time.Minute)); err != nil {
			t.Fatalf("recusou um par legitimo: %v", err)
		}
	})
	t.Run("heartbeat primeiro", func(t *testing.T) {
		node := newTestNode(t, &countingModel{})
		if _, err := NewNodeService(node, WithLeaseHeartbeat(time.Minute), WithLeaseTTL(10*time.Minute)); err != nil {
			t.Fatalf("recusou o MESMO par so por causa da ordem das opcoes — a validacao migrou para o closure: %v", err)
		}
	})
}
