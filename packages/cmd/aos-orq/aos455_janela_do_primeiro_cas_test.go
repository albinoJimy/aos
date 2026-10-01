package main

// aos455_janela_do_primeiro_cas_test.go — o CÓDIGO DE SAÍDA de quem perde o lease dentro da
// janela de um stream acabado de criar, reproduzido de forma DETERMINISTA e sem cluster
// (AOS-455).
//
// # O que o gate `nats` mostrava, e porque é que não se reproduzia
//
// Três vermelhos em dois dias sobre árvores idênticas, sempre o mesmo desenho: N reclamantes a
// disputar o lease de um run sobre um stream R3 acabado de criar, e um perdedor a sair `1` em
// vez de `3`. Contra um cluster local de quatro nós (nats-server 2.10.22) a falha apareceu 4
// vezes em 50 em `TestAOS100_NServe…` e em `TestAOS432_LeaseSobreStreamFrescoNegaPeloLease`,
// sempre pela mesma porta: um `STREAM.INFO` que o servidor NÃO RESPONDE enquanto o grupo R3 se
// forma, e a que o `esperarLider` dava o prazo inteiro. No CI viu-se a outra porta: um 503 no
// primeiro PUB depois de o INFO já anunciar líder. Ver jetstream/aos455_janela_test.go para as
// medições e a linha do servidor de cada uma.
//
// # O que este ficheiro faz
//
// Um JetStream de brincar — só o que o `serve` usa até ao `Claim` — com as duas janelas
// accionadas por CONTAGEM na ligação do perdedor. O vencedor reclama primeiro, pelo
// `LeaseManager` real; o perdedor é o `cmdServe` real, e o código de saída é o do `codigoDe`.
// Sem a correcção, os dois casos saem `1` (`exitErro`); com ela, `3` (`exitPosseNegada`).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore/jetstream"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

type msgDeBrincar struct {
	seq     uint64
	subject string
	dados   []byte
}

// jsDeBrincar é um JetStream mínimo: um stream, CAS por subject, leitura por consumidor push e
// MSG.GET. As janelas aplicam-se às ligações a partir da `janelaDesde` (1 = a primeira).
type jsDeBrincar struct {
	ln net.Listener

	mu       sync.Mutex
	ligacoes int
	msgs     []msgDeBrincar

	janelaDesde int
	// infoCalado: quantos STREAM.INFO de cada ligação na janela ficam sem resposta.
	infoCalado int
	// leituraAtrasada: quantos INFO com `subjects_filter` de cada ligação na janela respondem
	// com o stream VAZIO — a leitura do perdedor aconteceu antes da escrita do vencedor.
	leituraAtrasada int
	// pub503: quantas publicações de cada ligação na janela recebem 503.
	pub503 int
}

func arrancarJSDeBrincar(t *testing.T) *jsDeBrincar {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &jsDeBrincar{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.ligacoes++
			n := s.ligacoes
			s.mu.Unlock()
			go s.servir(c, n)
		}
	}()
	return s
}

func (s *jsDeBrincar) configurar(f func(*jsDeBrincar)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// estadoDaLigacao conta, por ligação, quanto de cada janela já foi gasto.
type estadoDaLigacao struct {
	infos, filtros, pubs int
}

func (s *jsDeBrincar) servir(c net.Conn, n int) {
	defer func() { _ = c.Close() }()
	if _, err := io.WriteString(c, `INFO {"server_id":"NFAKE","version":"2.10.22","headers":true,"max_payload":1048576}`+"\r\n"); err != nil {
		return
	}
	var wmu sync.Mutex
	escrever := func(f string, a ...any) {
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = fmt.Fprintf(c, f, a...)
	}
	br := bufio.NewReader(c)
	subs := map[string]string{} // subject -> sid
	var el estadoDaLigacao
	for {
		linha, err := br.ReadString('\n')
		if err != nil {
			return
		}
		campos := strings.Fields(strings.TrimRight(linha, "\r\n"))
		if len(campos) == 0 {
			continue
		}
		switch campos[0] {
		case "PING":
			escrever("PONG\r\n")
		case "SUB":
			subs[campos[1]] = campos[len(campos)-1]
		case "PUB", "HPUB":
			total, _ := strconv.Atoi(campos[len(campos)-1])
			hdr := 0
			if campos[0] == "HPUB" {
				hdr, _ = strconv.Atoi(campos[len(campos)-2])
			}
			bruto := make([]byte, total+2)
			if _, err := io.ReadFull(br, bruto); err != nil {
				return
			}
			subj := campos[1]
			reply := ""
			if (campos[0] == "PUB" && len(campos) == 4) || (campos[0] == "HPUB" && len(campos) == 5) {
				reply = campos[2]
			}
			cab := textproto.MIMEHeader{}
			if hdr > 0 {
				tp := textproto.NewReader(bufio.NewReader(strings.NewReader(string(bruto[:hdr]))))
				if _, err := tp.ReadLine(); err == nil { // «NATS/1.0»
					cab, _ = tp.ReadMIMEHeader()
				}
			}
			s.tratar(subj, reply, cab, bruto[hdr:total], n, &el, subs, escrever)
		}
	}
}

func (s *jsDeBrincar) tratar(subj, reply string, cab textproto.MIMEHeader, dados []byte, n int,
	el *estadoDaLigacao, subs map[string]string, escrever func(string, ...any)) {
	responder := func(corpo string) {
		if sid, ok := subs[reply]; ok {
			escrever("MSG %s %s %d\r\n%s\r\n", reply, sid, len(corpo), corpo)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	naJanela := s.janelaDesde > 0 && n >= s.janelaDesde
	switch {
	case strings.HasPrefix(subj, "$JS.API.STREAM.CREATE."):
		responder(`{"type":"io.nats.jetstream.api.v1.stream_create_response"}`)

	case strings.HasPrefix(subj, "$JS.API.STREAM.INFO."):
		el.infos++
		if naJanela && el.infos <= s.infoCalado {
			return // o servidor real cala-se enquanto o grupo R3 se forma
		}
		var pedido struct {
			Filtro string `json:"subjects_filter"`
		}
		_ = json.Unmarshal(dados, &pedido)
		contagens := map[string]int{}
		if pedido.Filtro != "" {
			el.filtros++
			if !(naJanela && el.filtros <= s.leituraAtrasada) {
				for _, m := range s.msgs {
					if m.subject == pedido.Filtro {
						contagens[m.subject]++
					}
				}
			}
		}
		st, _ := json.Marshal(map[string]any{"subjects": contagens})
		responder(`{"type":"io.nats.jetstream.api.v1.stream_info_response","state":` + string(st) +
			`,"cluster":{"name":"c","leader":"NFAKE"}}`)

	case strings.HasPrefix(subj, "$JS.API.CONSUMER.CREATE."):
		var pedido struct {
			Config natsjs.ConsumerConfig `json:"config"`
		}
		_ = json.Unmarshal(dados, &pedido)
		responder(`{"type":"io.nats.jetstream.api.v1.consumer_create_response","name":"c"}`)
		sid, ok := subs[pedido.Config.DeliverSubject]
		if !ok {
			return
		}
		i := 0
		for _, m := range s.msgs {
			if m.subject != pedido.Config.FilterSubject || m.seq < pedido.Config.OptStartSeq {
				continue
			}
			i++
			ack := fmt.Sprintf("$JS.ACK.S.c.1.%d.%d.%d.0", m.seq, i, time.Now().UnixNano())
			escrever("MSG %s %s %s %d\r\n%s\r\n", pedido.Config.DeliverSubject, sid, ack, len(m.dados), m.dados)
		}

	case strings.HasPrefix(subj, "$JS.API.STREAM.MSG.GET."):
		var pedido struct {
			Ultima string `json:"last_by_subj"`
		}
		_ = json.Unmarshal(dados, &pedido)
		var achada *msgDeBrincar
		for i := range s.msgs {
			if s.msgs[i].subject == pedido.Ultima {
				achada = &s.msgs[i]
			}
		}
		if achada == nil {
			responder(`{"error":{"code":404,"err_code":10037,"description":"no message found"}}`)
			return
		}
		corpo, _ := json.Marshal(map[string]any{"message": map[string]any{"subject": achada.subject, "seq": achada.seq, "data": achada.dados}})
		responder(string(corpo))

	case strings.HasPrefix(subj, "$JS."):
		// nada mais é preciso até ao Claim

	default: // publicação num subject do stream, com CAS por subject
		el.pubs++
		if naJanela && el.pubs <= s.pub503 {
			if sid, ok := subs[reply]; ok {
				const bloco = "NATS/1.0 503\r\n\r\n"
				escrever("HMSG %s %s %d %d\r\n%s\r\n", reply, sid, len(bloco), len(bloco), bloco)
			}
			return
		}
		var ultima uint64
		for _, m := range s.msgs {
			if m.subject == subj {
				ultima = m.seq
			}
		}
		if esperado := cab.Get(natsjs.HdrExpectedLastSubjectSeq); esperado != "" {
			if v, _ := strconv.ParseUint(esperado, 10, 64); v != ultima {
				responder(fmt.Sprintf(`{"error":{"code":400,"err_code":10071,"description":"wrong last sequence: %d"}}`, ultima))
				return
			}
		}
		seq := uint64(len(s.msgs) + 1)
		s.msgs = append(s.msgs, msgDeBrincar{seq: seq, subject: subj, dados: append([]byte(nil), dados...)})
		responder(fmt.Sprintf(`{"stream":"S","seq":%d}`, seq))
	}
}

// vencedorReclama — o primeiro reclamante, pelo LeaseManager real, fora de qualquer janela.
func vencedorReclama(t *testing.T, addr, stream, run string) {
	t.Helper()
	st, err := jetstream.Abrir(addr, jetstream.ComNomeDeStream(stream))
	if err != nil {
		t.Fatalf("o vencedor não abriu o store: %v", err)
	}
	defer func() { _ = st.Close() }()
	lm, err := durable.NewLeaseManager(st, leaseTTL, durable.WithWorkerID("dono-455"))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	if _, err := lm.Claim(context.Background(), run); err != nil {
		t.Fatalf("o vencedor não reclamou o run: %v", err)
	}
}

// perdedorSai corre o `serve` REAL do perdedor e devolve o código com que o processo sairia.
func perdedorSai(t *testing.T, addr, stream, run string) (int, error) {
	t.Helper()
	t.Setenv("AOS_ORQ_NODE_URL", "")
	t.Setenv("AOS_MODE", "")
	err := cmdServe([]string{"--nats", addr, "--nats-stream", stream, "--run", run,
		"--nodes", "n1", "--worker", "perdedor-455"})
	return codigoDe(err), err
}

// TestAOS455_PerdedorComINFOCaladoSaiPelaPosse — janela 1: o INFO do perdedor, logo depois do
// CREATE, fica sem resposta. Antes da correcção esse INFO levava o prazo inteiro do store
// (10 s), o `Abrir` falhava com «indeterminado — sem resposta dentro do prazo» e o perdedor
// saía 1. Tem de sair 3, a nomear o dono.
func TestAOS455_PerdedorComINFOCaladoSaiPelaPosse(t *testing.T) {
	js := arrancarJSDeBrincar(t)
	addr := js.ln.Addr().String()
	const stream, run = "AOS455_INFO", "run-455-info"
	vencedorReclama(t, addr, stream, run)
	js.configurar(func(s *jsDeBrincar) { s.janelaDesde, s.infoCalado = 2, 1 })

	codigo, err := perdedorSai(t, addr, stream, run)
	if codigo != exitPosseNegada {
		t.Fatalf("o perdedor saiu %d, quer %d (posse negada): %v", codigo, exitPosseNegada, err)
	}
	if !strings.Contains(err.Error(), `detido por "dono-455"`) {
		t.Fatalf("a recusa não nomeia o dono: %v", err)
	}
}

// TestAOS455_PerdedorCom503NoPrimeiroCASSaiPelaPosse — janela 2: o INFO do perdedor já
// anuncia líder, a leitura dele aconteceu antes da escrita do vencedor (vê o run livre), e o
// seu primeiro CAS recebe 503. Antes da correcção o `Claim` devolvia esse 503 cru e o perdedor
// saía 1. Tem de sair 3: o CAS seguinte é recusado pelo do vencedor, o `Claim` relê, e vê o
// lease.
func TestAOS455_PerdedorCom503NoPrimeiroCASSaiPelaPosse(t *testing.T) {
	js := arrancarJSDeBrincar(t)
	addr := js.ln.Addr().String()
	const stream, run = "AOS455_PUB", "run-455-pub"
	vencedorReclama(t, addr, stream, run)
	// Duas leituras atrasadas: a do `Claim` (readLeaseState) e a hidratação do `Append`.
	js.configurar(func(s *jsDeBrincar) { s.janelaDesde, s.leituraAtrasada, s.pub503 = 2, 2, 1 })

	codigo, err := perdedorSai(t, addr, stream, run)
	if codigo != exitPosseNegada {
		t.Fatalf("o perdedor saiu %d, quer %d (posse negada): %v", codigo, exitPosseNegada, err)
	}
	if !strings.Contains(err.Error(), `detido por "dono-455"`) {
		t.Fatalf("a recusa não nomeia o dono: %v", err)
	}
}

// TestAOS455_ControloSemJanelaOPerdedorSaiPelaPosse — o CONTROLO: o mesmo servidor de brincar
// sem janela nenhuma dá `3`. Sem ele, os dois testes acima não distinguiam «a janela faz sair
// 1» de «o servidor de brincar faz sair 1».
func TestAOS455_ControloSemJanelaOPerdedorSaiPelaPosse(t *testing.T) {
	js := arrancarJSDeBrincar(t)
	addr := js.ln.Addr().String()
	const stream, run = "AOS455_CTL", "run-455-ctl"
	vencedorReclama(t, addr, stream, run)

	codigo, err := perdedorSai(t, addr, stream, run)
	if codigo != exitPosseNegada {
		t.Fatalf("sem janela, o perdedor saiu %d, quer %d: %v", codigo, exitPosseNegada, err)
	}
}
