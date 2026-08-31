package natsjs_test

// FICHEIRO TEMPORÁRIO DE AUDITORIA — não faz parte do pacote.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// arrancarFake põe de pé um servidor TCP que fala o protocolo NATS o suficiente para o
// handshake (INFO -> CONNECT) e delega o resto no manipulador.
func arrancarFake(t *testing.T, trata func(c net.Conn, br *bufio.Reader)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		br := bufio.NewReader(c)
		_, _ = c.Write([]byte("INFO {\"server_id\":\"fake\",\"max_payload\":1048576,\"headers\":true}\r\n"))
		if _, err := br.ReadString('\n'); err != nil { // CONNECT
			return
		}
		trata(c, br)
	}()
	return ln.Addr().String()
}

func lerLinha(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	l, err := br.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimRight(l, "\r\n")
}

// ---------------------------------------------------------------------------
// A1. PUB com payload nil não emite o CRLF final -> comando malformado.
// ---------------------------------------------------------------------------

func TestAudit_PUBSemPayloadOmiteCRLFFinal(t *testing.T) {
	recebido := make(chan string, 1)
	addr := arrancarFake(t, func(c net.Conn, br *bufio.Reader) {
		_ = lerLinha(t, br) // SUB
		_ = c.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
		var b bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, err := br.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		recebido <- b.String()
	})

	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()

	_, _ = cn.InfoDoStream("AOS", 300*time.Millisecond) // dados == nil

	bytesNoFio := <-recebido
	t.Logf("bytes emitidos após o SUB: %q", bytesNoFio)

	if !strings.HasPrefix(bytesNoFio, "PUB ") {
		t.Fatalf("esperava um PUB, veio %q", bytesNoFio)
	}
	// O protocolo exige PUB <subj> <reply> 0\r\n\r\n : cabeçalho + payload vazio + CRLF.
	if !strings.Contains(bytesNoFio, " 0\r\n\r\n") {
		t.Errorf("DEFEITO MEDIDO: PUB de payload vazio emitido SEM o CRLF do payload; "+
			"bytes=%q", bytesNoFio)
	}
	// Prova adicional: o byte seguinte ao cabeçalho é já o comando seguinte.
	if strings.Contains(bytesNoFio, " 0\r\nUNSUB") {
		t.Errorf("DEFEITO MEDIDO: o comando seguinte (UNSUB) cola-se ao PUB; "+
			"o servidor real lê 'UN' onde espera CRLF e responde -ERR + fecha. bytes=%q", bytesNoFio)
	}
}

// ---------------------------------------------------------------------------
// A2. Tabela do parser MSG/HMSG (com e sem reply).
// ---------------------------------------------------------------------------

func TestAudit_TabelaParserMSGHMSG(t *testing.T) {
	casos := []struct {
		nome    string
		quadro  func(inbox, sid string) string
		reply   string
		hdr     string // valor esperado de "A"
		data    string
	}{
		{"MSG sem reply", func(i, s string) string {
			return fmt.Sprintf("MSG %s %s 5\r\nhello\r\n", i, s)
		}, "", "", "hello"},
		{"MSG com reply", func(i, s string) string {
			return fmt.Sprintf("MSG %s %s resp.x 5\r\nhello\r\n", i, s)
		}, "resp.x", "", "hello"},
		{"HMSG sem reply", func(i, s string) string {
			return fmt.Sprintf("HMSG %s %s 18 23\r\nNATS/1.0\r\nA: b\r\n\r\nhello\r\n", i, s)
		}, "", "b", "hello"},
		{"HMSG com reply", func(i, s string) string {
			return fmt.Sprintf("HMSG %s %s resp.x 18 23\r\nNATS/1.0\r\nA: b\r\n\r\nhello\r\n", i, s)
		}, "resp.x", "b", "hello"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
				sub := lerLinha(t, br) // SUB <inbox> <sid>
				campos := strings.Fields(sub)
				if len(campos) != 3 {
					return
				}
				_, _ = cc.Write([]byte(c.quadro(campos[1], campos[2])))
				time.Sleep(500 * time.Millisecond)
			})
			cn, err := natsjs.Ligar(addr, 2*time.Second)
			if err != nil {
				t.Fatalf("ligar: %v", err)
			}
			defer func() { _ = cn.Close() }()
			m, err := cn.Request("x.y", nil, []byte("q"), 2*time.Second)
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if m.Reply != c.reply {
				t.Errorf("Reply=%q quer %q", m.Reply, c.reply)
			}
			if string(m.Data) != c.data {
				t.Errorf("Data=%q quer %q", m.Data, c.data)
			}
			if c.hdr != "" && m.Header["A"] != c.hdr {
				t.Errorf("Header[A]=%q quer %q (header=%v)", m.Header["A"], c.hdr, m.Header)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A3. Mensagem de estado 503 (no_responders) — o que Publicar devolve.
// ---------------------------------------------------------------------------

func TestAudit_NoResponders503(t *testing.T) {
	addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
		sub := lerLinha(t, br)
		campos := strings.Fields(sub)
		if len(campos) != 3 {
			return
		}
		// NATS/1.0 503\r\n\r\n  == 16 bytes, corpo vazio.
		_, _ = cc.Write([]byte(fmt.Sprintf("HMSG %s %s 16 16\r\nNATS/1.0 503\r\n\r\n\r\n", campos[1], campos[2])))
		time.Sleep(500 * time.Millisecond)
	})
	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()

	// Primeiro: o que vê Request (nível baixo).
	m, err := cn.Request("$JS.API.STREAM.INFO.X", nil, []byte("{}"), 2*time.Second)
	t.Logf("Request -> Msg{Header=%v Data=%q} err=%v", m.Header, m.Data, err)
	if m.Header != nil {
		t.Logf("header descodificado: %#v", m.Header)
	} else {
		t.Logf("DEFEITO MEDIDO: descodificarHeader devolveu nil para 'NATS/1.0 503' — "+
			"o código de estado é indistinguível de 'sem cabeçalhos'")
	}
}

func TestAudit_NoResponders503ViaPublicar(t *testing.T) {
	addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
		sub := lerLinha(t, br)
		campos := strings.Fields(sub)
		if len(campos) != 3 {
			return
		}
		_, _ = cc.Write([]byte(fmt.Sprintf("HMSG %s %s 16 16\r\nNATS/1.0 503\r\n\r\n\r\n", campos[1], campos[2])))
		time.Sleep(500 * time.Millisecond)
	})
	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()

	ack, err := cn.PublicarComCAS("aos.run.1", 0, nil, []byte(`{"e":1}`), 2*time.Second)
	t.Logf("PublicarComCAS -> ack=%+v err=%v", ack, err)
	if err == nil {
		t.Errorf("DEFEITO MEDIDO CRÍTICO: 503 no-responders aceite como sucesso (ack=%+v)", ack)
	}
	if errors.Is(err, natsjs.ErrProtocolo) {
		t.Errorf("DEFEITO MEDIDO: 'no responders' (condição de OPERAÇÃO: JetStream desligado / "+
			"stream inexistente / sem permissões) é reportado como ErrProtocolo, que a doc "+
			"promete ser 'sempre um defeito nosso, nunca do operador'. err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// A4. Erro NÃO-timeout depois de a escrita ter sido totalmente entregue.
// ---------------------------------------------------------------------------

func TestAudit_LigacaoParteDepoisDaEscrita(t *testing.T) {
	entregue := make(chan string, 1)
	addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
		_ = lerLinha(t, br) // SUB
		hpub := lerLinha(t, br)
		campos := strings.Fields(hpub)
		if len(campos) >= 1 {
			// ler o resto (bloco + dados + CRLF) para provar que o comando chegou INTEIRO
			var total int
			_, _ = fmt.Sscanf(campos[len(campos)-1], "%d", &total)
			buf := make([]byte, total+2)
			_, _ = io_ReadFull(br, buf)
			entregue <- hpub + "|" + string(buf[:total])
		}
		// O servidor APLICOU e morre antes de responder (falha de nó / drain).
		_ = cc.Close()
	})
	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()

	ack, err := cn.PublicarComCAS("aos.run.1", 0, nil, []byte(`{"e":1}`), 5*time.Second)
	select {
	case v := <-entregue:
		t.Logf("o servidor recebeu o comando COMPLETO: %q", v)
	case <-time.After(time.Second):
		t.Fatalf("o servidor não recebeu o comando completo")
	}
	t.Logf("PublicarComCAS -> ack=%+v err=%v (ErrTimeout=%v)", ack, err, errors.Is(err, natsjs.ErrTimeout))
	if err == nil {
		t.Fatalf("esperava erro")
	}
	if !errors.Is(err, natsjs.ErrTimeout) {
		t.Errorf("DEFEITO MEDIDO: a escrita foi entregue por inteiro e pode ter ficado DURÁVEL, "+
			"mas o erro devolvido (%v) NÃO é ErrTimeout — o contrato 'ERRO ⇒ NADA FICOU DURÁVEL, "+
			"excepto ErrTimeout' é violado", err)
	}
}

func io_ReadFull(br *bufio.Reader, p []byte) (int, error) {
	n := 0
	for n < len(p) {
		k, err := br.Read(p[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// A5. Corrida entregar/dessubscrever -> send on closed channel.
// ---------------------------------------------------------------------------

func TestAudit_CorridaEnvioEmCanalFechado(t *testing.T) {
	addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			campos := strings.Fields(l)
			if len(campos) == 3 && campos[0] == "SUB" {
				// responde IMEDIATAMENTE, para colidir com o dessubscrever do timeout
				_, _ = cc.Write([]byte(fmt.Sprintf("MSG %s %s 2\r\nok\r\n", campos[1], campos[2])))
			}
		}
	})
	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20000; i++ {
				_, _ = cn.Request("x.y", nil, []byte("q"), time.Nanosecond)
			}
		}()
	}
	wg.Wait()
	t.Logf("160000 pedidos com timeout=1ns sem panic (a janela existe na mesma: ver relatório)")
}

// ---------------------------------------------------------------------------
// A6. Escrita bloqueada: Request ignora o seu próprio timeout.
// ---------------------------------------------------------------------------

func TestAudit_EscritaBloqueadaIgnoraTimeout(t *testing.T) {
	addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
		_ = lerLinha(t, br) // SUB; depois NUNCA mais lê do socket
		time.Sleep(20 * time.Second)
	})
	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()

	grande := bytes.Repeat([]byte("a"), 32<<20) // 32 MiB: enche os buffers TCP
	pronto := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		_, _ = cn.Publicar("aos.run.1", natsjs.Header{"X": "y"}, grande, 200*time.Millisecond)
		pronto <- time.Since(t0)
	}()
	select {
	case d := <-pronto:
		t.Logf("Publicar devolveu em %v", d)
		if d > 3*time.Second {
			t.Errorf("DEFEITO MEDIDO: timeout=200ms mas Publicar demorou %v — o prazo só começa "+
				"DEPOIS da escrita e não há write deadline", d)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("DEFEITO MEDIDO: timeout=200ms e Publicar continua bloqueado ao fim de 5s — "+
			"o temporizador só é criado depois de cn.publicar retornar e não há SetWriteDeadline; "+
			"a goroutine fica pendurada indefinidamente no caminho crítico de escrita")
	}
}

// ---------------------------------------------------------------------------
// A7. total negativo -> panic no leitor (derruba o processo do nó).
// ---------------------------------------------------------------------------

func TestAudit_TotalNegativoPanica(t *testing.T) {
	addr := arrancarFake(t, func(cc net.Conn, br *bufio.Reader) {
		sub := lerLinha(t, br)
		campos := strings.Fields(sub)
		if len(campos) != 3 {
			return
		}
		_, _ = cc.Write([]byte(fmt.Sprintf("MSG %s %s -5\r\n", campos[1], campos[2])))
		time.Sleep(2 * time.Second)
	})
	cn, err := natsjs.Ligar(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("ligar: %v", err)
	}
	defer func() { _ = cn.Close() }()
	_, err = cn.Request("x.y", nil, []byte("q"), 1500*time.Millisecond)
	t.Logf("sem panic; err=%v", err)
}
