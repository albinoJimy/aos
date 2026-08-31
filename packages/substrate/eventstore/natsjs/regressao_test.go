package natsjs_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// Estes testes fixam os defeitos que a revisão adversarial de 2026-08-31 MEDIU no
// cliente. Correm contra um servidor FALSO, sem cluster: são regressões de protocolo e
// de concorrência, e têm de correr em toda a CI, não só onde há NATS.
//
// A regra que os governa: cada teste falha se o defeito voltar, e o comentário diz qual
// era o defeito — para que a correcção não seja removida por parecer supérflua.

const infoComHeaders = `INFO {"server_id":"FAKE","version":"2.10.0","headers":true,"max_payload":1048576}` + "\r\n"

// fake é um servidor NATS mínimo para testes: anuncia INFO, guarda tudo o que recebe e
// deixa o teste responder o que quiser.
type fake struct {
	t        *testing.T
	ln       net.Listener
	mu       sync.Mutex
	recebido bytes.Buffer
	pronto   chan struct{}
}

// arrancarFake abre um servidor falso. `servir` corre por ligação aceite e recebe o
// socket e um leitor já posicionado depois do INFO/CONNECT.
func arrancarFake(t *testing.T, info string, servir func(c net.Conn, br *bufio.Reader, f *fake)) *fake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fake{t: t, ln: ln, pronto: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		if _, err := io.WriteString(c, info); err != nil {
			return
		}
		br := bufio.NewReader(c)
		if _, err := br.ReadString('\n'); err != nil { // CONNECT
			return
		}
		close(f.pronto)
		if servir != nil {
			servir(c, br, f)
		}
	}()
	return f
}

func (f *fake) addr() string { return f.ln.Addr().String() }

// consumir lê tudo o que chegar durante `d` e acumula em recebido.
func (f *fake) consumir(c net.Conn, br *bufio.Reader, d time.Duration) {
	_ = c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.recebido.Write(buf[:n])
			f.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (f *fake) fio() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recebido.String()
}

func ligarAoFake(t *testing.T, f *fake) *natsjs.Conn {
	t.Helper()
	cn, err := natsjs.Ligar(f.addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("Ligar ao fake: %v", err)
	}
	t.Cleanup(func() { _ = cn.Close() })
	return cn
}

// TestRegressao_PUBSemCorpoLevaTerminador — DEFEITO: `enviarPartes` tratava `corpo ==
// nil` como «comando sem payload», mas `publicar` passava o `dados` do chamador, que é
// nil quando não há corpo. Um PUB de comprimento zero saía SEM o CRLF terminador, o
// servidor lia o comando seguinte no sítio errado e matava a ligação com
// `-ERR 'Unknown Protocol Operation'`. Toda a API do JetStream sem corpo passa por aqui
// — incluindo o InfoDoStream de que a re-hidratação de arranque depende.
func TestRegressao_PUBSemCorpoLevaTerminador(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		f.consumir(c, br, 2*time.Second)
	})
	cn := ligarAoFake(t, f)

	_, _ = cn.Request("teste.sem.corpo", nil, nil, 300*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	fio := f.fio()
	i := strings.Index(fio, "PUB teste.sem.corpo ")
	if i < 0 {
		t.Fatalf("não encontrei o PUB no fio recebido: %q", fio)
	}
	resto := fio[i:]
	if !strings.HasPrefix(resto, "PUB teste.sem.corpo _INBOX.") {
		t.Fatalf("PUB malformado: %q", resto)
	}
	// Tem de haver " 0\r\n\r\n": o tamanho, a linha do comando, e a linha VAZIA do
	// payload de comprimento zero.
	if !strings.Contains(resto, " 0\r\n\r\n") {
		t.Fatalf("PUB de payload vazio sem o CRLF terminador — o servidor desalinha e mata a ligação.\nfio: %q", resto)
	}
}

// TestRegressao_InjeccaoDeCabecalhoERecusada — DEFEITO: um valor de cabeçalho com CRLF
// forjava cabeçalhos adicionais. Como o hdr_len é calculado DEPOIS da codificação, o
// enquadramento ficava válido e o servidor parseava as linhas injectadas como
// cabeçalhos verdadeiros — incluindo um segundo Nats-Expected-Last-Subject-Sequence.
// Qual dos dois vence depende da ordem de iteração do mapa, que é aleatória: era uma
// via NÃO-DETERMINISTA de contornar a arbitragem em que todo o AOS-100 assenta, a
// partir de dados de workflow (a chave de dedup é f(run_id, step_id)).
func TestRegressao_InjeccaoDeCabecalhoERecusada(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		f.consumir(c, br, 2*time.Second)
	})
	cn := ligarAoFake(t, f)

	venenoso := natsjs.Header{
		natsjs.HdrMsgID: "run-1\r\n" + natsjs.HdrExpectedLastSubjectSeq + ": 99999\r\nNats-Rollup: sub",
	}
	_, err := cn.Publicar("teste.injeccao", venenoso, []byte(`{}`), 300*time.Millisecond)
	if err == nil {
		t.Fatal("um valor de cabeçalho com CRLF foi ACEITE — é injecção de cabeçalhos")
	}
	if !strings.Contains(err.Error(), "injecção") {
		t.Fatalf("erro %v; queria a recusa explícita de injecção", err)
	}
	if strings.Contains(f.fio(), "99999") {
		t.Fatalf("os bytes injectados chegaram ao fio: %q", f.fio())
	}
}

// TestRegressao_InjeccaoNoSubjectERecusada — DEFEITO: um subject com espaço forjava
// argumentos extra no comando; com CRLF, comandos inteiros. Nenhum era validado.
func TestRegressao_InjeccaoNoSubjectERecusada(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		f.consumir(c, br, 2*time.Second)
	})
	cn := ligarAoFake(t, f)

	for _, mau := range []string{"aos.outro _INBOX.forjado", "aos.x\r\nSUB forjado 9", ""} {
		if _, err := cn.Publicar(mau, nil, []byte(`{}`), 300*time.Millisecond); err == nil {
			t.Errorf("subject %q foi aceite", mau)
		}
	}
	if strings.Contains(f.fio(), "forjado") {
		t.Fatalf("bytes forjados chegaram ao fio: %q", f.fio())
	}
}

// TestRegressao_TamanhosDoFioNaoDerrubamOProcesso — DEFEITO: os tamanhos vinham do FIO e
// chegavam sem validação a `make([]byte, total+2)` e a `corpo[:hdrLen]`. Um `HMSG … -5
// 10` dava `slice bounds out of range`; um `HMSG … -5 -3` dava `makeslice: len out of
// range`; um total absurdo era alocado sem tecto (OOM). Qualquer um aborta o processo do
// nó, e o pacote declara que corre SEM TLS e SEM autenticação — bastam quatro bytes de
// quem estiver na rota.
//
// Um teste que entra em pânico derruba o processo de teste, pelo que este teste PASSAR
// já é o resultado.
func TestRegressao_TamanhosDoFioNaoDerrubamOProcesso(t *testing.T) {
	for _, quadro := range []string{
		"HMSG teste 1 -5 10\r\n",
		"HMSG teste 1 -5 -3\r\n",
		"MSG teste 1 100000000\r\n",
		"HMSG teste 1 20 10\r\n", // hdr > total
	} {
		t.Run(strings.TrimSpace(quadro), func(t *testing.T) {
			f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
				go f.consumir(c, br, 2*time.Second)
				time.Sleep(50 * time.Millisecond)
				_, _ = io.WriteString(c, quadro)
				time.Sleep(500 * time.Millisecond)
			})
			cn := ligarAoFake(t, f)
			// A ligação tem de morrer com erro, não com panic.
			_, err := cn.Request("teste.q", nil, []byte(`{}`), 700*time.Millisecond)
			if err == nil {
				t.Fatalf("quadro %q não produziu erro", quadro)
			}
		})
	}
}

// TestRegressao_Status503EOperacionalNaoDeProtocolo — DEFEITO: o CONNECT pede
// `no_responders:true`, pelo que o servidor RESPONDE com uma mensagem de estado
// `NATS/1.0 503` e corpo vazio. O código de estado era descartado, a mensagem chegava
// como «sem cabeçalhos e sem corpo», e o Publicar devolvia «violação de protocolo:
// PubAck ilegível» — que o próprio pacote documenta como «sempre um defeito nosso,
// nunca do operador». Mas 503 é a condição OPERACIONAL mais comum que existe: JetStream
// desligado, stream inexistente, subject fora do stream, sem permissões. O operador era
// mandado caçar um bug do cliente.
func TestRegressao_Status503EOperacionalNaoDeProtocolo(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		// O cliente envia SUB <inbox> <sid> ANTES do PUB — é preciso saltar tudo o que
		// não seja o PUB, senão o fake responde à linha errada e fecha.
		var reply string
		for reply == "" {
			linha, err := br.ReadString('\n')
			if err != nil {
				return
			}
			campos := strings.Fields(linha) // PUB <subject> <reply> <n>
			if len(campos) >= 4 && strings.EqualFold(campos[0], "PUB") {
				reply = campos[2]
				_, _ = br.ReadString('\n') // payload vazio: a linha em branco
			}
		}
		bloco := "NATS/1.0 503\r\n\r\n"
		_, _ = io.WriteString(c, fmt.Sprintf("HMSG %s 1 %d %d\r\n%s\r\n", reply, len(bloco), len(bloco), bloco))
		time.Sleep(400 * time.Millisecond)
	})
	cn := ligarAoFake(t, f)

	_, err := cn.Publicar("sem.responders", nil, nil, 2*time.Second)
	if !errors.Is(err, natsjs.ErrSemResponders) {
		t.Fatalf("erro = %v; queria ErrSemResponders — um 503 é condição do operador, não defeito do cliente", err)
	}
	if errors.Is(err, natsjs.ErrProtocolo) {
		t.Fatal("o 503 continua classificado como violação de protocolo")
	}
}

// TestRegressao_PublicarComCASNaoMutaOHeaderDoChamador — DEFEITO: PublicarComCAS
// escrevia o expected_seq NO MAPA DO CHAMADOR. Duas consequências medidas: publicações
// seguintes com o mesmo Header levavam uma afirmação de CAS que ninguém pediu; e duas
// goroutines a partilhar um Header davam `fatal error: concurrent map writes`, que nem
// `recover` apanha — apesar de a Conn estar documentada como segura para uso concorrente.
func TestRegressao_PublicarComCASNaoMutaOHeaderDoChamador(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		f.consumir(c, br, 3*time.Second)
	})
	cn := ligarAoFake(t, f)

	h := natsjs.Header{natsjs.HdrMsgID: "run:passo-1"}
	_, _ = cn.PublicarComCAS("teste.cas", 7, h, []byte(`{}`), 200*time.Millisecond)

	if _, existe := h[natsjs.HdrExpectedLastSubjectSeq]; existe {
		t.Fatalf("o Header do chamador foi mutado: %v", h)
	}
	if len(h) != 1 {
		t.Fatalf("o Header do chamador tem %d entradas, quer 1: %v", len(h), h)
	}

	// E o uso concorrente do MESMO Header não pode ser um fatal error.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = cn.PublicarComCAS("teste.cas", uint64(j), h, []byte(`{}`), 50*time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
}

// TestRegressao_RoturaAposEscoamentoEIndeterminada — DEFEITO: o contrato prometia
// «ERRO ⇒ NADA FICOU DURÁVEL» com UMA excepção (o timeout). Mas se a ligação parte
// DEPOIS de o comando ter sido escoado por inteiro, a escrita pode ter sido aplicada e
// a resposta perdida — e o erro devolvido era um EOF genérico, indistinguível de uma
// falha que nada escreveu. Um chamador que siga a promessa à letra (o GraphBuilder
// reverte em memória) DIVERGE do log.
func TestRegressao_RoturaAposEscoamentoEIndeterminada(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		// Lê o comando COMPLETO (linha + bloco/payload) e fecha sem responder.
		linha, err := br.ReadString('\n')
		if err != nil {
			return
		}
		f.mu.Lock()
		f.recebido.WriteString(linha)
		f.mu.Unlock()
		campos := strings.Fields(linha)
		if n := len(campos); n >= 2 {
			if total, err2 := parseUltimo(campos); err2 == nil {
				corpo := make([]byte, total+2)
				_, _ = io.ReadFull(br, corpo)
			}
		}
		_ = c.Close()
	})
	cn := ligarAoFake(t, f)

	_, err := cn.PublicarComCAS("teste.rotura", 0, nil, []byte(`{"e":1}`), 3*time.Second)
	if !errors.Is(err, natsjs.ErrIndeterminado) {
		t.Fatalf("erro = %v; queria ErrIndeterminado — a ligação partiu DEPOIS de o comando sair, "+
			"logo não se pode afirmar que nada ficou durável", err)
	}
}

func parseUltimo(campos []string) (int, error) {
	var n int
	_, err := fmt.Sscanf(campos[len(campos)-1], "%d", &n)
	return n, err
}

// TestRegressao_RequestConcorrenteComPrazoCurtoNaoEntraEmPanico — DEFEITO: `entregar`
// obtinha o canal sob o lock, LARGAVA o lock, e só depois enviava. Entre as duas
// instruções, o `defer dessubscrever` de um Request expirado fechava o canal:
// `panic: send on closed channel` na goroutine do leitor, SEM recover — o processo do
// nó morria. Reproduzia-se 3/3 com goroutines a fazer Request com prazo mínimo, que é a
// situação normal de um cluster sob carga.
//
// Este teste PASSAR é o resultado: um panic derruba o processo de teste.
func TestRegressao_RequestConcorrenteComPrazoCurtoNaoEntraEmPanico(t *testing.T) {
	f := arrancarFake(t, infoComHeaders, func(c net.Conn, br *bufio.Reader, f *fake) {
		// Responde a tudo o que chega, o mais depressa possível, para maximizar a
		// corrida entre a entrega e a expiração do prazo.
		for {
			linha, err := br.ReadString('\n')
			if err != nil {
				return
			}
			campos := strings.Fields(linha)
			if len(campos) < 4 || !strings.EqualFold(campos[0], "PUB") {
				continue
			}
			if total, err2 := parseUltimo(campos); err2 == nil {
				corpo := make([]byte, total+2)
				_, _ = io.ReadFull(br, corpo)
			}
			reply := campos[2]
			_, _ = io.WriteString(c, fmt.Sprintf("MSG %s 1 2\r\nok\r\n", reply))
		}
	})
	cn := ligarAoFake(t, f)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 300; j++ {
				_, _ = cn.Request("teste.corrida", nil, []byte("x"), time.Microsecond)
			}
		}()
	}
	wg.Wait()
}

// TestRegressao_ServidorSemCabecalhosERecusadoNoHandshake — DEFEITO: o INFO só era
// verificado pelo prefixo. Um servidor que não suporte cabeçalhos manifestava-se mais
// tarde como uma ligação que morre sem explicação — apesar de o doc do pacote declarar
// que é nos cabeçalhos que viajam o expected_seq e a chave de dedup, ou seja, TODAS as
// garantias do AOS-100.
func TestRegressao_ServidorSemCabecalhosERecusadoNoHandshake(t *testing.T) {
	semHeaders := `INFO {"server_id":"FAKE","version":"2.10.0","headers":false,"max_payload":1048576}` + "\r\n"
	f := arrancarFake(t, semHeaders, nil)
	_, err := natsjs.Ligar(f.addr(), 2*time.Second)
	if err == nil {
		t.Fatal("ligação aceite a um servidor sem suporte de cabeçalhos")
	}
	if !strings.Contains(err.Error(), "cabeçalhos") {
		t.Fatalf("erro = %v; queria a recusa explícita por falta de cabeçalhos", err)
	}
}
