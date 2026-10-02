package jetstream

// aos432_abrir_espera_test.go — prova, SEM cluster, que o `Abrir` espera pelo líder.
//
// Os testes de lider_test.go provam a espera; não provam que o `Abrir` a chama. Apagar a
// chamada deixava tudo verde fora do gate `nats`. Aqui um servidor NATS mínimo responde ao
// `STREAM.CREATE` com sucesso e ao `STREAM.INFO` sem líder nas primeiras N consultas — a
// forma exacta do que o cluster real faz durante a eleição (medido no AOS-432).

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// servidorEleicao é um NATS de brincar que só conhece o que o `Abrir` pede — e, desde o
// AOS-455, o primeiro `Append` de um stream vazio: o INFO com `subjects_filter` (stream vazio)
// e a publicação com CAS.
type servidorEleicao struct {
	ln       net.Listener
	semLider int // quantos INFO respondem sem líder (-1 = sempre)

	mu     sync.Mutex
	infos  int
	creats int

	// AOS-455 — as duas formas da janela que o cluster real mostrou (medidas, ver o ticket).
	//
	// silencioInfo: quantos INFO ficam SEM RESPOSTA nenhuma. É o que o servidor faz a um
	// `STREAM.INFO` enquanto o grupo R3 de um stream acabado de criar ainda não tem quem
	// responda (nats-server 2.10.22, `jsStreamInfoRequest`: `if bail { return }`).
	silencioInfo int
	// pub503: quantas publicações num subject de stream recebem 503 — o líder já é anunciado
	// pelo INFO, mas ainda não subscreveu os subjects (`setLeader` corre DEPOIS de o Raft já
	// se dizer líder). -1 = sempre. Depois disso a publicação é aceite (`seq` crescente).
	pub503 int
	// subjects é o `config.subjects` que o INFO devolve (nil = o do prefixo por omissão
	// não é anunciado; o Store só o consulta quando não criou o stream).
	subjects []string
	pubs     int
}

// configurar altera os botões do servidor sob o lock (o servir corre noutra goroutine).
func (s *servidorEleicao) configurar(f func(*servidorEleicao)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *servidorEleicao) publicacoes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubs
}

func arrancarServidorEleicao(t *testing.T, semLider int) *servidorEleicao {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &servidorEleicao{ln: ln, semLider: semLider}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.servir(c)
		}
	}()
	return s
}

func (s *servidorEleicao) contagens() (creates, infos int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creats, s.infos
}

func (s *servidorEleicao) servir(c net.Conn) {
	defer func() { _ = c.Close() }()
	if _, err := io.WriteString(c, `INFO {"server_id":"NFAKE","version":"2.10.0","headers":true,"max_payload":1048576}`+"\r\n"); err != nil {
		return
	}
	br := bufio.NewReader(c)
	subs := map[string]string{} // subject -> sid
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
			_, _ = io.WriteString(c, "PONG\r\n")
		case "SUB":
			subs[campos[1]] = campos[len(campos)-1]
		case "PUB", "HPUB":
			total, _ := strconv.Atoi(campos[len(campos)-1])
			if _, err := io.ReadFull(br, make([]byte, total+2)); err != nil {
				return
			}
			if len(campos) < 4 { // sem reply: nada a responder
				continue
			}
			subj, reply := campos[1], campos[2]
			corpo := s.responder(subj)
			sid, ok := subs[reply]
			switch {
			case !ok || corpo == "":
				// silêncio: nenhuma resposta, como o servidor real durante a eleição
			case corpo == respostaNinguemServe:
				const bloco = "NATS/1.0 503\r\n\r\n"
				_, _ = fmt.Fprintf(c, "HMSG %s %s %d %d\r\n%s\r\n", reply, sid, len(bloco), len(bloco), bloco)
			default:
				_, _ = fmt.Fprintf(c, "MSG %s %s %d\r\n%s\r\n", reply, sid, len(corpo), corpo)
			}
		}
	}
}

func (s *servidorEleicao) responder(subj string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(subj, "$JS.API.STREAM.CREATE."):
		s.creats++
		return `{"type":"io.nats.jetstream.api.v1.stream_create_response","config":{"name":"X"}}`
	case strings.HasPrefix(subj, "$JS.API.STREAM.INFO."):
		s.infos++
		if s.infos <= s.silencioInfo {
			return ""
		}
		lider := "NFAKE"
		if s.semLider < 0 || s.infos-s.silencioInfo <= s.semLider {
			lider = ""
		}
		cfg, _ := json.Marshal(map[string]any{"subjects": s.subjects, "num_replicas": 3})
		return `{"type":"io.nats.jetstream.api.v1.stream_info_response","config":` + string(cfg) +
			`,"state":{"subjects":{}},"cluster":{"name":"c","leader":"` + lider + `"}}`
	case strings.HasPrefix(subj, "$JS."):
		return ""
	default: // publicação num subject de stream
		s.pubs++
		if s.pub503 < 0 || s.pubs <= s.pub503 {
			return respostaNinguemServe
		}
		return fmt.Sprintf(`{"stream":"X","seq":%d}`, s.pubs-max(s.pub503, 0))
	}
}

// respostaNinguemServe marca, no servidor de brincar, a resposta de estado 503 (no responders).
const respostaNinguemServe = "\x00503"

// TestAOS432_AbrirNaoDevolveAntesDeHaverLider — o `Abrir` só devolve o Store depois de o
// INFO anunciar líder. Sem a chamada ao esperarLider, o INFO nunca seria pedido (0) e o
// Store sairia em plena eleição — que é o defeito do AOS-432.
func TestAOS432_AbrirNaoDevolveAntesDeHaverLider(t *testing.T) {
	s := arrancarServidorEleicao(t, 3)
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS432FAKE"), ComPrazo(5*time.Second))
	if err != nil {
		t.Fatalf("Abrir com líder eleito à 4.ª consulta: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	creates, infos := s.contagens()
	if creates != 1 {
		t.Fatalf("CREATE = %d, quer 1", creates)
	}
	if infos != 4 {
		t.Fatalf("INFO = %d, quer 4 (3 sem líder + 1 com) — o Abrir devolveu sem esperar pelo líder", infos)
	}
}

// TestAOS432_AbrirSemLiderFalhaFechadoDentroDoPrazo — sem líder nunca, o `Abrir` recusa
// com a indisponibilidade (ErrNoQuorum + ErrStreamSemLider) e dentro do prazo que lhe foi
// dado — não o dobro, nem o quádruplo.
func TestAOS432_AbrirSemLiderFalhaFechadoDentroDoPrazo(t *testing.T) {
	s := arrancarServidorEleicao(t, -1)
	const prazo = 400 * time.Millisecond
	inicio := time.Now()
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS432FAKE"), ComPrazo(prazo))
	gasto := time.Since(inicio)
	if err == nil {
		_ = st.Close()
		t.Fatal("Abrir devolveu um Store sobre um stream que nunca elegeu líder")
	}
	if !errors.Is(err, eventstore.ErrNoQuorum) || !errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("Abrir sem líder: %v — quer ErrNoQuorum E ErrStreamSemLider", err)
	}
	// Folga larga para a máquina partilhada; o que se exclui é o 2× (e o 4×) do prazo.
	if gasto > prazo+prazo/2+500*time.Millisecond {
		t.Fatalf("Abrir levou %s com prazo %s — a espera não partilha o orçamento", gasto, prazo)
	}
}
