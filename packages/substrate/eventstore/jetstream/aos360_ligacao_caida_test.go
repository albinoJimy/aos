package jetstream

// aos360_ligacao_caida_test.go — os sensores in-process de AOS-350 e AOS-354 (AOS-360).
//
// # PORQUE ESTES CAMINHOS PARECIAM EXIGIR CLUSTER, E NÃO EXIGEM
//
// A validação adversarial do EPIC-24 mediu que repor `Ligada()` → `true` (AOS-350) e retirar a
// tradução para [eventstore.ErrNoQuorum] das quatro portas (AOS-354) deixava a suite VERDE. A
// razão escrita na altura foi estrutural: `Store.cn` é um [*natsjs.Conn] CONCRETO, sem costura,
// e os campos do cliente são privados ao seu pacote — pelo que o ramo «ligação viva que caiu»
// parecia só alcançável com um cluster.
//
// Não é. O cliente fala o protocolo NATS sobre um `net.Conn`, e para o pôr nesse estado basta um
// servidor que faça o handshake e depois FECHE a socket. Daí em diante [natsjs.Conn.Ligada] é
// falso e toda a operação devolve [natsjs.ErrDesligado] sem sair — que é exactamente o estado
// que os dois tickets descrevem. Nenhuma costura nova no `Store`: a costura já existia, e era a
// rede.
//
// Para o terceiro caminho do AOS-360 — a regra antiga em `lerLote` (AOS-345) — este fake NÃO
// chega: seria preciso imitar a API do JetStream (consumidor efémero, entrega push com
// `$JS.ACK…`). Esse fica com o sensor que tem desde o AOS-431:
// `TestJanela_AcimaDaJanela_LeTudoEContinuaEscrivel`, que corre no job `nats` (obrigatório) e
// avermelha com a regra antiga — medido no AOS-360, ver a spec.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

const infoDoServidorFalso = `INFO {"server_id":"AOS360","version":"2.10.0","headers":true,"max_payload":1048576}` + "\r\n"

// ligacaoQueCai liga um [natsjs.Conn] a um servidor falso e devolve-o com uma função que DERRUBA
// o servidor (socket e listener) e espera até o cliente dar pela queda. O listener fecha-se
// também, para que a reconexão não tenha para onde ir e o cliente fique desligado.
func ligacaoQueCai(t *testing.T) (*natsjs.Conn, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var socket net.Conn
	pronto := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		socket = c
		mu.Unlock()
		if _, err := io.WriteString(c, infoDoServidorFalso); err != nil {
			return
		}
		br := bufio.NewReader(c)
		if _, err := br.ReadString('\n'); err != nil { // CONNECT
			return
		}
		// O handshake termina num PING à espera do PONG (AOS-470): é o que prova ao cliente
		// que o CONNECT foi aceite.
		if _, err := br.ReadString('\n'); err != nil { // PING
			return
		}
		if _, err := io.WriteString(c, "PONG\r\n"); err != nil {
			return
		}
		close(pronto)
		_, _ = io.Copy(io.Discard, br) // o servidor ouve e não responde, até ser derrubado
	}()

	cn, err := natsjs.Connect(ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("ligar ao servidor falso: %v", err)
	}
	t.Cleanup(func() { _ = cn.Close() })
	select {
	case <-pronto:
	case <-time.After(5 * time.Second):
		t.Fatal("o servidor falso não recebeu o CONNECT em 5 s — o handshake não aconteceu")
	}

	derrubar := func() {
		t.Helper()
		_ = ln.Close()
		mu.Lock()
		_ = socket.Close()
		mu.Unlock()
		prazo := time.Now().Add(5 * time.Second)
		for cn.Ligada() {
			if time.Now().After(prazo) {
				t.Fatal("o servidor fechou a socket e o cliente continua a dizer-se Ligada() ao fim de 5 s — " +
					"é o defeito de AOS-350: uma ligação que caiu lida como viva")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return cn, derrubar
}

func storeSobre(cn *natsjs.Conn) *Store {
	return &Store{
		cn:      cn,
		stream:  "AOS360",
		prefixo: "aos.es.aos360",
		prazo:   2 * time.Second,
		now:     time.Now,
		obs:     observadorNulo{},
		rastro:  eventstore.NopRastreador{},
		streams: map[string]*estado{},
		subs:    map[string]*subscricao{},
	}
}

// TestAOS360_HealthyCaiComALigacao — AOS-350: o ramo «ligação viva que caiu». Antes, só o ramo
// `cn == nil` tinha teste (`TestAcessores_RefletemAConfiguracao`), e `Ligada()` → `true`
// passava. Aqui o MESMO store diz-se saudável com a socket viva e deixa de o dizer quando ela cai.
func TestAOS360_HealthyCaiComALigacao(t *testing.T) {
	cn, derrubar := ligacaoQueCai(t)
	s := storeSobre(cn)
	if !s.Healthy() {
		t.Fatal("Healthy() falso com a socket viva — o controlo do teste não vale")
	}
	derrubar()
	if s.Healthy() {
		t.Fatal("Healthy() verdadeiro com a ligação caída — o /readyz ficava 200 sobre um substrato " +
			"que recusa todas as escritas (AOS-350)")
	}
}

// TestAOS360_AsQuatroPortasTraduzemADesligacao — AOS-354: com a ligação caída, cada uma das
// quatro portas que a tradução cobre devolve um erro que é, AO MESMO TEMPO, o sentinela
// canónico ([eventstore.ErrNoQuorum], que os consumidores do plano de controlo reconhecem) e o
// específico ([natsjs.ErrDesligado], a causa que o operador lê). Retirar o `defer` de qualquer
// uma avermelha o seu subteste.
func TestAOS360_AsQuatroPortasTraduzemADesligacao(t *testing.T) {
	cn, derrubar := ligacaoQueCai(t)
	s := storeSobre(cn)
	derrubar()
	ctx := context.Background()

	portas := []struct {
		nome string
		erro func() error
	}{
		{"Append", func() error {
			_, err := s.Append(ctx, "run-aos360", eventstore.EventInput{Type: "aos360.facto", Payload: json.RawMessage(`{}`)})
			return err
		}},
		{"Read", func() error {
			_, err := s.Read(ctx, "run-aos360", 1)
			return err
		}},
		{"Subscribe", func() error {
			sub, err := s.Subscribe(ctx, eventstore.Filter{Streams: []string{"run-aos360"}}, func(eventstore.Event) {})
			if sub != nil {
				sub.Unsubscribe()
			}
			return err
		}},
		{"Streams", func() error {
			_, err := s.Streams()
			return err
		}},
	}
	for _, p := range portas {
		t.Run(p.nome, func(t *testing.T) {
			err := p.erro()
			if err == nil {
				t.Fatal("a porta devolveu sucesso com a ligação caída")
			}
			if !errors.Is(err, natsjs.ErrDesligado) {
				t.Fatalf("o erro não vem da desligação — o teste não está a medir a tradução: %v", err)
			}
			if !errors.Is(err, eventstore.ErrNoQuorum) {
				t.Fatalf("a desligação saiu SEM tradução para ErrNoQuorum (AOS-354): o burn-down lia-a "+
					"como cegueira e matava o run à primeira: %v", err)
			}
		})
	}
}
