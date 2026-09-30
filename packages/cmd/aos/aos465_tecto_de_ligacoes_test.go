package main

// AOS-465 — TECTO DE LIGAÇÕES ACEITES NO http.Server. O teste do achado ALTO corre contra o servidor
// REAL (`listen` + `serveListener`, TLS terminado no nó); os da ordem de despejo usam um
// `http.Server` com a MESMA ligação ([ligarAoServidor]) e um handler que bloqueia à ordem, que o
// servidor real não tem.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// servidorComTecto arranca o servidor real com TLS e o tecto dado, e devolve o endereço, o servidor
// e um canal que fecha quando o `Serve` retorna. O tecto SSE fica em 1 para o par ser válido com
// tectos pequenos.
func servidorComTecto(t *testing.T, tecto int) (string, *APIServer, *x509.Certificate, chan struct{}) {
	t.Helper()
	node, _ := newAPINode(t, &countingModel{}, true)
	t.Cleanup(func() { _ = node.Close() })
	certPath, keyPath, leaf := genTLSCertFiles(t)
	srv := newAPIServer(t, node, WithTLSFiles(certPath, keyPath), WithMaxAcceptedConns(tecto), WithMaxTrajectoryConns(1))
	ln, err := srv.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fim := make(chan struct{})
	go func() {
		_ = srv.serveListener(ln)
		close(fim)
	}()
	return ln.Addr().String(), srv, leaf, fim
}

func dialTLS(addr string, leaf *x509.Certificate, prazo time.Duration) (*tls.Conn, error) {
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	d := &net.Dialer{Timeout: prazo}
	c, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// esperarAte faz polling de uma condição com prazo. Os testes que se seguem medem o estado do
// listener depois de o cliente ter feito o dial, e o `Accept` do servidor corre noutra goroutine.
func esperarAte(t *testing.T, prazo time.Duration, cond func() bool, msg string) {
	t.Helper()
	fim := time.Now().Add(prazo)
	for !cond() {
		if time.Now().After(fim) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAOS465LigacoesQueSoSeguramAVagaNaoFechamOHealthz — O ACHADO ALTO DA REVISÃO, COMO REGRESSÃO.
//
// A primeira versão fazia o `Accept` ESPERAR no tecto. A revisão mediu que ligações que só seguram a
// vaga — TCP sem bytes, TLS sem pedido, keep-alive depois de um 404, cabeçalhos a pingar, corpo a
// pingar, h2 sem pedido — enchiam o tecto mais depressa do que os timeouts as libertavam, e o
// `/healthz` passava 0/4 com 32 ligações ociosas e o tecto a 16. Aqui abrem-se mais de dois tectos
// cheios de ligações dessas, de seis formas, e o `/healthz` tem de passar 4/4 — sem nunca haver mais
// ligações abertas do que o tecto.
func TestAOS465LigacoesQueSoSeguramAVagaNaoFechamOHealthz(t *testing.T) {
	const tecto = 5
	addr, srv, leaf, fim := servidorComTecto(t, tecto)
	var ataque []net.Conn
	t.Cleanup(func() {
		for _, c := range ataque {
			_ = c.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-fim
	})

	tlsCom := func(pedido string, lerResposta bool) {
		c, err := dialTLS(addr, leaf, 3*time.Second)
		if err != nil {
			t.Fatalf("dial TLS: %v", err)
		}
		ataque = append(ataque, c)
		if pedido == "" {
			return
		}
		if _, err := c.Write([]byte(pedido)); err != nil {
			t.Fatalf("escrita: %v", err)
		}
		if lerResposta {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatalf("resposta: %v", err)
			}
			_ = resp.Body.Close()
		}
	}
	for i := 0; i < 2; i++ {
		// TCP sem um único byte.
		c, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		ataque = append(ataque, c)
		// TLS completo, sem pedido.
		tlsCom("", false)
		// Keep-alive ocioso depois de uma resposta.
		tlsCom("GET /nao-existe HTTP/1.1\r\nHost: x\r\n\r\n", true)
		// Cabeçalhos a pingar (slowloris).
		tlsCom("GET /healthz HTTP/1.1\r\nHost: x\r\n", false)
		// Corpo a pingar, DENTRO do handler de submissão.
		tlsCom("POST /runs HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{", false)
		// h2: preâmbulo e SETTINGS vazio, sem nenhum pedido — o vector de 61 s que a revisão mediu.
		pool := x509.NewCertPool()
		pool.AddCert(leaf)
		h2, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr, &tls.Config{
			RootCAs: pool, ServerName: "127.0.0.1", NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12,
		})
		if err != nil {
			t.Fatalf("dial h2: %v", err)
		}
		ataque = append(ataque, h2)
		if p := h2.ConnectionState().NegotiatedProtocol; p != "h2" {
			t.Fatalf("o servidor nao negociou h2 (%q): o caso h2 nao estaria a medir nada", p)
		}
		if _, err := h2.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00")); err != nil {
			t.Fatalf("preambulo h2: %v", err)
		}
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	for i := 0; i < 4; i++ {
		cli := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"},
		}}
		resp, err := cli.Get("https://" + addr + "/healthz")
		if err != nil {
			t.Fatalf("/healthz %d/4 falhou com o tecto cheio de ligacoes que so seguram a vaga: %v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/healthz %d/4: %d", i+1, resp.StatusCode)
		}
	}
	if n := srv.ligacoes.abertas.Load(); n > tecto {
		t.Fatalf("%d ligacoes abertas com o tecto a %d", n, tecto)
	}
	if d := srv.ligacoes.despejadas.Load(); d < int64(len(ataque)-tecto) {
		t.Fatalf("so %d despejos para %d ligacoes de ataque e tecto %d", d, len(ataque), tecto)
	}
}

// servidorUnitario monta um `http.Server` com o tecto e a MESMA ligação que o [NewAPIServer] usa
// ([ligarAoServidor]), em claro, para os testes que precisam de um handler que bloqueie à ordem.
func servidorUnitario(t *testing.T, tecto int, h http.Handler) (string, *listenerLimitado, *ligacoesAceites) {
	t.Helper()
	interior, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	contador := &ligacoesAceites{}
	ln := limitarLigacoes(interior, tecto, contador).(*listenerLimitado)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ligarAoServidor(srv)
	fim := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(fim)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-fim
	})
	return interior.Addr().String(), ln, contador
}

// pedir escreve um pedido HTTP/1.1 numa ligação já aberta e lê a resposta.
func pedir(c net.Conn, br *bufio.Reader, caminho string, prazo time.Duration) (int, error) {
	_ = c.SetDeadline(time.Now().Add(prazo))
	if _, err := c.Write([]byte("GET " + caminho + " HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// bloqueante devolve um handler em que `/lento` espera por `soltar` e o resto responde logo, e um
// canal que recebe um sinal por cada `/lento` que entrou.
func bloqueante() (http.Handler, chan struct{}, chan struct{}) {
	soltar := make(chan struct{})
	entrou := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lento" {
			entrou <- struct{}{}
			<-soltar
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}), soltar, entrou
}

// TestAOS465LigacaoASerServidaNaoSeDespeja — o outro lado do despejo: uma ligação DENTRO de um
// handler a fazer trabalho do servidor não é tocada, e é o ÚNICO caso em que o `Accept` espera.
func TestAOS465LigacaoASerServidaNaoSeDespeja(t *testing.T) {
	h, soltar, entrou := bloqueante()
	addr, _, contador := servidorUnitario(t, 2, h)

	respostas := make(chan error, 2)
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = c.Close() }()
		go func() {
			st, err := pedir(c, bufio.NewReader(c), "/lento", 5*time.Second)
			if err == nil && st != http.StatusOK {
				err = fmt.Errorf("status %d", st)
			}
			respostas <- err
		}()
		<-entrou
	}

	// Terceira ligação, com as duas vagas em handlers: NÃO pode ser servida.
	c3, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 3: %v", err)
	}
	defer func() { _ = c3.Close() }()
	if _, err := pedir(c3, bufio.NewReader(c3), "/rapido", 300*time.Millisecond); err == nil {
		t.Fatal("uma 3a ligacao foi servida com o tecto a 2 e as duas vagas em handlers — o tecto " +
			"despejou uma ligacao que estava a ser servida, ou nao morde")
	}
	if d := contador.despejadas.Load(); d != 0 {
		t.Fatalf("%d despejos com todas as ligacoes em handlers", d)
	}

	// Os dois pedidos em curso terminam BEM: não foram cortados.
	close(soltar)
	for i := 0; i < 2; i++ {
		if err := <-respostas; err != nil {
			t.Fatalf("um pedido em curso foi cortado: %v", err)
		}
	}
}

// TestAOS465DespejaAMenosRecentementeUsada — a ordem entre ligações ociosas é a do último USO, não a
// da chegada: uma ligação keep-alive que acabou de ser usada é a última a sair.
func TestAOS465DespejaAMenosRecentementeUsada(t *testing.T) {
	h, _, _ := bloqueante()
	addr, _, contador := servidorUnitario(t, 2, h)

	abrir := func() (net.Conn, *bufio.Reader) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c, bufio.NewReader(c)
	}
	a, ra := abrir()
	b, rb := abrir()
	for _, p := range []struct {
		c net.Conn
		r *bufio.Reader
	}{{a, ra}, {b, rb}, {a, ra}} { // a chegou primeiro mas foi usada por ÚLTIMO
		if _, err := pedir(p.c, p.r, "/rapido", 3*time.Second); err != nil {
			t.Fatalf("pedido: %v", err)
		}
	}

	c, rc := abrir()
	if _, err := pedir(c, rc, "/rapido", 3*time.Second); err != nil {
		t.Fatalf("a ligacao nova devia ser servida despejando uma ociosa: %v", err)
	}
	if _, err := pedir(b, rb, "/rapido", time.Second); err == nil {
		t.Fatal("b (a menos recentemente usada) devia ter sido despejada")
	}
	if _, err := pedir(a, ra, "/rapido", 3*time.Second); err != nil {
		t.Fatalf("a (usada por ultimo) foi despejada em vez de b: %v", err)
	}
	if d := contador.despejadas.Load(); d != 1 {
		t.Fatalf("despejos=%d, esperava 1", d)
	}
}

// TestAOS465OCorpoLentoCedeAntesDoTrabalho — sem ligações ociosas, cede a que está à espera do CORPO
// do cliente, nunca a que está a fazer trabalho do servidor.
func TestAOS465OCorpoLentoCedeAntesDoTrabalho(t *testing.T) {
	h, soltar, entrou := bloqueante()
	addr, _, contador := servidorUnitario(t, 2, h)

	// A: dentro do handler, a trabalhar (sem corpo).
	a, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = a.Close() }()
	respA := make(chan error, 1)
	go func() {
		st, err := pedir(a, bufio.NewReader(a), "/lento", 5*time.Second)
		if err == nil && st != http.StatusOK {
			err = fmt.Errorf("status %d", st)
		}
		respA <- err
	}()
	<-entrou

	// B: dentro do handler, à espera de um corpo que não chega.
	b, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Write([]byte("POST /corpo HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n{")); err != nil {
		t.Fatalf("escrita: %v", err)
	}
	esperarAte(t, 2*time.Second, func() bool { return contador.abertas.Load() == 2 }, "B nao foi aceite")
	time.Sleep(50 * time.Millisecond) // B entra no handler e bloqueia no corpo

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := pedir(c, bufio.NewReader(c), "/rapido", 3*time.Second); err != nil {
		t.Fatalf("a ligacao nova devia ser servida despejando a do corpo lento: %v", err)
	}
	close(soltar)
	if err := <-respA; err != nil {
		t.Fatalf("a ligacao a fazer trabalho do servidor foi despejada: %v", err)
	}
	if d := contador.despejadas.Load(); d != 1 {
		t.Fatalf("despejos=%d, esperava 1 (a do corpo lento)", d)
	}
}

// TestAOS465ShutdownNaoPenduraComOTectoCheio — a armadilha clássica de um listener limitado.
//
// Com todas as vagas em handlers, o `Accept` segura a ligação nova e espera por vaga no SEMÁFORO, não
// no listener interior. O `Close` do listener interior não o acorda: sem o canal `fechado`, o `Serve`
// só retornaria quando um handler acabasse. Mede-se que retorna em menos de um segundo.
func TestAOS465ShutdownNaoPenduraComOTectoCheio(t *testing.T) {
	h, soltar, entrou := bloqueante()
	defer close(soltar)
	interior, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	contador := &ligacoesAceites{}
	ln := limitarLigacoes(interior, 1, contador)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ligarAoServidor(srv)
	fim := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(fim)
	}()
	addr := interior.Addr().String()

	a, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = a.Close() }()
	go func() { _, _ = pedir(a, bufio.NewReader(a), "/lento", 5*time.Second) }()
	<-entrou
	// A segunda ligação fica na mão do `Accept`, à espera de vaga.
	b, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = b.Close() }()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inicio := time.Now()
	go func() { _ = srv.Shutdown(ctx) }()
	select {
	case <-fim:
		t.Logf("Serve retornou em %v com o tecto cheio", time.Since(inicio).Round(time.Millisecond))
	case <-time.After(time.Second):
		t.Fatal("o Serve NAO retornou em 1 s depois do Shutdown com o tecto cheio — o Accept esta preso " +
			"no semaforo e o Close do listener interior nao o acorda")
	}
}

// TestAOS465AVagaVoltaUmaSoVez — o `http.Server` pode fechar a mesma ligação mais do que uma vez, e o
// despejo acrescenta um terceiro caminho de fecho.
//
// Sem o `sync.Once` o Close repetido de c1 DEPOIS de c2 ocupar a vaga rouba o lugar de c2, e o tecto
// passa a aceitar a mais. c2 é marcada como servida para o despejo não a tirar e mascarar o defeito.
func TestAOS465AVagaVoltaUmaSoVez(t *testing.T) {
	interior, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	contador := &ligacoesAceites{}
	ln := limitarLigacoes(interior, 1, contador).(*listenerLimitado)
	defer func() { _ = ln.Close() }()

	var clientes []net.Conn
	defer func() {
		for _, c := range clientes {
			_ = c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", interior.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clientes = append(clientes, c)
	}

	c1, err := ln.Accept()
	if err != nil {
		t.Fatalf("1o Accept: %v", err)
	}
	_ = c1.Close()
	c2, err := ln.Accept() // ocupa a vaga que c1 devolveu
	if err != nil {
		t.Fatalf("2o Accept: %v", err)
	}
	defer func() { _ = c2.Close() }()
	ln.entrar(c2.(*ligacaoLimitada), false)

	// O Close REPETIDO de c1, com c2 a ocupar a vaga. Em goroutine: se a implementação bloquear aqui,
	// o teste não pendura.
	go func() { _ = c1.Close() }()
	time.Sleep(50 * time.Millisecond)

	aceite := make(chan struct{})
	go func() {
		if c3, err := ln.Accept(); err == nil {
			_ = c3.Close()
			close(aceite)
		}
	}()
	select {
	case <-aceite:
		t.Fatal("um 3o Accept passou com o tecto a 1 e c2 a ser servida — o Close repetido de c1 " +
			"devolveu a vaga de c2, e o tecto aceita a mais")
	case <-time.After(300 * time.Millisecond):
	}
	if n := contador.abertas.Load(); n != 1 {
		t.Fatalf("contador=%d, esperava 1 (c2 aberta)", n)
	}
}

// TestAOS465OEmbrulhoMantemOMeioFecho — o `http.Server` em claro faz `CloseWrite` antes de fechar
// para o cliente não receber um RST que corte a resposta. O embrulho escondia-o.
func TestAOS465OEmbrulhoMantemOMeioFecho(t *testing.T) {
	interior, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := limitarLigacoes(interior, 1, &ligacoesAceites{})
	defer func() { _ = ln.Close() }()
	cli, err := net.Dial("tcp", interior.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = cli.Close() }()
	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer func() { _ = c.Close() }()
	cw, ok := c.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("a ligacao limitada nao expoe CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cli.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("o cliente devia ler EOF do meio-fecho, leu %v", err)
	}
	// E a outra metade continua aberta: o servidor ainda lê.
	if _, err := cli.Write([]byte("x")); err != nil {
		t.Fatalf("escrita do cliente depois do meio-fecho: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatalf("o servidor devia ler depois do meio-fecho: %v", err)
	}
}

// TestAOS465MetricaDasLigacoes — a série existe e declara o tecto EM VIGOR.
func TestAOS465MetricaDasLigacoes(t *testing.T) {
	addr, srv, leaf, fim := servidorComTecto(t, 7)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-fim
	})
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	cli := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}}}
	resp, err := cli.Get("https://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(strings.Builder)
	b := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	corpo := buf.String()
	if !strings.Contains(corpo, "\naos_api_connections_ceiling 7\n") {
		t.Fatalf("a serie do tecto devia publicar 7 (o valor em vigor)")
	}
	if !strings.Contains(corpo, "\naos_api_connections_open 1\n") {
		t.Fatalf("a serie das ligacoes abertas devia contar a propria ligacao do scrape (1)")
	}
	if !strings.Contains(corpo, "\naos_api_connections_evicted_total 0\n") {
		t.Fatalf("a serie dos despejos devia existir e estar a 0")
	}
}

// TestAOS465EnvOParFinalContraOTectoSSE — o tecto de ligações tem de exceder o de streams SSE, e o
// par valida-se FINAL: baixar só um dos dois para os pôr ao contrário aborta.
func TestAOS465EnvOParFinalContraOTectoSSE(t *testing.T) {
	casos := []struct {
		conns, traj string
		aceita      bool
	}{
		{"", "", true},         // defaults 1024 / 256
		{"257", "", true},      // o menor que ainda deixa folga
		{"256", "", false},     // igual ao SSE por omissão: os streams ocupam tudo
		{"100", "", false},     // abaixo
		{"", "1024", false},    // SÓ o SSE subido para igualar o default de ligações
		{"", "2000", false},    // SÓ o SSE subido acima dele
		{"4096", "2000", true}, // par coerente, ambos explícitos
		{"0", "", false},       // 0 NÃO desliga
		{"-1", "", false},
		{"abc", "", false},
	}
	for _, c := range casos {
		t.Run("conns="+c.conns+"/traj="+c.traj, func(t *testing.T) {
			clearIngressEnv(t)
			t.Setenv("AOS_API_MAX_CONNS", c.conns)
			t.Setenv("AOS_TRAJECTORY_MAX_CONNS", c.traj)
			// o por-leitor tem de ficar abaixo do SSE para esse par não abortar por outra razão
			if c.traj != "" {
				t.Setenv("AOS_TRAJECTORY_MAX_CONNS_PER_READER", "8")
			}
			lim, _, err := ingressLimitsFromEnv()
			if c.aceita && err != nil {
				t.Fatalf("devia ser aceite: %v", err)
			}
			if !c.aceita && err == nil {
				t.Fatalf("devia abortar — ligacoes=%d, SSE=%d", lim.apiMaxConns, lim.trajMaxConns)
			}
			// E ABORTA PELA RAZÃO CERTA: um caso que abortasse por outro par passaria este teste sem
			// medir o tecto de ligações.
			if !c.aceita && !strings.Contains(err.Error(), "AOS_API_MAX_CONNS") {
				t.Fatalf("abortou, mas por outra razao que nao o tecto de ligacoes: %v", err)
			}
			if c.aceita && lim.apiMaxConns <= lim.trajMaxConns {
				t.Fatalf("par em vigor sem folga: ligacoes=%d <= SSE=%d", lim.apiMaxConns, lim.trajMaxConns)
			}
		})
	}
}

// TestAOS465OAmbienteChegaAoListener — achado MÉDIO da revisão: o caminho do ambiente até ao listener
// não tinha teste, e trocar a opção por `WithMaxAcceptedConns(0)` (que mantém o default) sobrevivia à
// suite inteira. Mede-se o número que o SERVIDOR aplica, a partir do ambiente, e o que o banner diz.
func TestAOS465OAmbienteChegaAoListener(t *testing.T) {
	clearIngressEnv(t)
	t.Setenv("AOS_API_MAX_CONNS", "777")
	lim, opts, err := ingressLimitsFromEnv()
	if err != nil {
		t.Fatalf("ingressLimitsFromEnv: %v", err)
	}
	node, _ := newAPINode(t, &countingModel{}, true)
	t.Cleanup(func() { _ = node.Close() })
	srv := newAPIServer(t, node, opts...)
	if srv.maxLigacoes != 777 {
		t.Fatalf("o servidor aplica %d, o ambiente pediu 777", srv.maxLigacoes)
	}
	ln, err := srv.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if l, ok := ln.(*listenerLimitado); !ok || cap(l.vagas) != 777 {
		t.Fatalf("o listener nao esta limitado a 777: %T", ln)
	}
	// O número que o banner dá ao operador: 777 menos os 256 do SSE por omissão.
	if d := dobraDasLigacoes(lim); !strings.Contains(d, "as outras 521 servem o resto da API") {
		t.Fatalf("o banner nao declara as 521 ligacoes que o SSE nao toma: %s", d)
	}
}

// TestAOS465OParComOSSEValeNasOpcoes — o par só era validado na leitura do ambiente; composto por
// opções, o SSE podia igualar ou passar o tecto, ou ficar sem tecto, e ocupar o listener inteiro.
func TestAOS465OParComOSSEValeNasOpcoes(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	for _, c := range []struct {
		conns, sse int
		aceita     bool
	}{
		{10, 9, true},
		{10, 10, false},
		{10, 11, false},
		{10, 0, false}, // SSE sem tecto
	} {
		_, err := NewAPIServer(svc, node, WithMaxAcceptedConns(c.conns), WithMaxTrajectoryConns(c.sse))
		if c.aceita && err != nil {
			t.Fatalf("ligacoes=%d sse=%d devia ser aceite: %v", c.conns, c.sse, err)
		}
		if !c.aceita && !errors.Is(err, ErrConnCeilingNotAboveSSE) {
			t.Fatalf("ligacoes=%d sse=%d devia recusar com ErrConnCeilingNotAboveSSE, deu %v", c.conns, c.sse, err)
		}
	}
}

// TestAOS465OServidorRealDistingueServidaDeOciosa — o sensor da LIGAÇÃO em [NewAPIServer].
//
// Sem [ligarAoServidor], o tecto continua a despejar, mas não vê handlers: todas as ligações parecem
// ociosas e sai a mais antiga. O teste do ALTO continuaria verde. Aqui a mais antiga está DENTRO do
// `POST /runs` à espera do corpo, e a mais nova é TCP sem bytes: com a ligação feita sai a nova
// (primeiro escalão), sem ela sairia a antiga.
func TestAOS465OServidorRealDistingueServidaDeOciosa(t *testing.T) {
	addr, srv, leaf, fim := servidorComTecto(t, 2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-fim
	})

	corpo, err := dialTLS(addr, leaf, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = corpo.Close() }()
	if _, err := corpo.Write([]byte("POST /runs HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{")); err != nil {
		t.Fatalf("escrita: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // entra no handler e bloqueia no corpo

	ociosa, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = ociosa.Close() }()
	esperarAte(t, 2*time.Second, func() bool { return srv.ligacoes.abertas.Load() == 2 }, "a ociosa nao foi aceite")

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	cli := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"},
	}}
	resp, err := cli.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("/healthz: %v", err)
	}
	_ = resp.Body.Close()

	_ = ociosa.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := ociosa.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("a ligacao ociosa devia ter sido despejada (EOF), leu %v", err)
	}
	_ = corpo.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := corpo.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a ligacao dentro do handler foi despejada antes da ociosa (%v) — o servidor real nao "+
			"esta ligado ao tecto, e o despejo nao ve handlers", err)
	}
}

// TestAOS465SairDoHandlerNaoEFicarOciosa — o defeito que o teste da ligação servida apanhou, fixado
// sem depender de tempo. Entre o handler retornar e o servidor declarar `StateIdle`, a resposta ainda
// está no buffer; um despejo nesse intervalo corta-a. O teste de ponta-a-ponta só o via com `-race`
// (o intervalo é de microssegundos), e a mutação que o reintroduz sobrevivia sem ele.
func TestAOS465SairDoHandlerNaoEFicarOciosa(t *testing.T) {
	interior, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := limitarLigacoes(interior, 1, &ligacoesAceites{}).(*listenerLimitado)
	defer func() { _ = ln.Close() }()
	cli, err := net.Dial("tcp", interior.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = cli.Close() }()
	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer func() { _ = c.Close() }()
	lc := c.(*ligacaoLimitada)

	ln.entrar(lc, false)
	ln.sair(lc)
	if ln.despejarUma() {
		t.Fatal("despejou uma ligacao cujo handler saiu mas cuja resposta o servidor ainda nao " +
			"terminou (sem StateIdle) — corta a resposta")
	}
	ln.ociosa(lc)
	if !ln.despejarUma() {
		t.Fatal("depois de StateIdle a ligacao devia ser despejavel")
	}
}
