package main

// AOS-465 — TECTO DE LIGAÇÕES ACEITES NO http.Server. Os testes correm contra o servidor REAL
// (`listen` + `serveListener`), com TLS terminado no nó, porque a afirmação que importa é que o
// tecto conta ligações TCP POR BAIXO do TLS que o `ServeTLS` põe por cima.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// servidorComTecto arranca o servidor real com TLS e o tecto dado, e devolve o endereço, o servidor
// e um canal que fecha quando o `Serve` retorna.
func servidorComTecto(t *testing.T, tecto int) (string, *APIServer, *x509.Certificate, chan struct{}) {
	t.Helper()
	node, _ := newAPINode(t, &countingModel{}, true)
	t.Cleanup(func() { _ = node.Close() })
	certPath, keyPath, leaf := genTLSCertFiles(t)
	srv := newAPIServer(t, node, WithTLSFiles(certPath, keyPath), WithMaxAcceptedConns(tecto))
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

// TestAOS465OTectoMordePorBaixoDoTLS — O CRITÉRIO.
//
// Com o tecto a 2, duas ligações TLS completas ocupam as duas vagas; uma terceira abre o TCP (fica
// no backlog do kernel) mas o handshake NÃO completa, porque o `Accept` está à espera de vaga.
// Fechada uma das duas, a terceira passa. Se o tecto estivesse por CIMA do TLS, ou não existisse, a
// terceira completaria o handshake de imediato.
func TestAOS465OTectoMordePorBaixoDoTLS(t *testing.T) {
	addr, srv, leaf, fim := servidorComTecto(t, 2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-fim
	})

	a, err := dialTLS(addr, leaf, 3*time.Second)
	if err != nil {
		t.Fatalf("1a ligacao: %v", err)
	}
	b, err := dialTLS(addr, leaf, 3*time.Second)
	if err != nil {
		t.Fatalf("2a ligacao: %v", err)
	}
	defer func() { _ = b.Close() }()
	if n := srv.ligacoes.abertas.Load(); n != 2 {
		t.Fatalf("o contador devia estar a 2 com duas ligacoes abertas, esta a %d", n)
	}

	// A TERCEIRA: o handshake TLS tem de NÃO completar enquanto não houver vaga.
	if c, err := dialTLS(addr, leaf, 400*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("a 3a ligacao completou o handshake TLS com o tecto a 2 e duas ligacoes abertas — o " +
			"tecto nao esta a morder, ou esta por CIMA do TLS")
	}

	// Liberta-se uma vaga: uma ligação nova tem de passar.
	_ = a.Close()
	var c *tls.Conn
	prazo := time.Now().Add(3 * time.Second)
	for time.Now().Before(prazo) {
		if c, err = dialTLS(addr, leaf, 500*time.Millisecond); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("depois de fechar uma ligacao, uma nova devia passar: %v — a vaga nao foi devolvida", err)
	}
	_ = c.Close()
}

// TestAOS465ShutdownNaoPenduraComOTectoCheio — a armadilha clássica de um listener limitado.
//
// Com o tecto cheio, o `Accept` do servidor está bloqueado no SEMÁFORO, não no listener interior.
// O `Close` do listener interior não o acorda: sem o canal `fechado`, o `Serve` só retornaria quando
// uma ligação libertasse a vaga — até ao ReadHeaderTimeout (5 s), ou mais para uma ligação que
// mantenha a sessão viva. Mede-se que retorna em menos de um segundo.
func TestAOS465ShutdownNaoPenduraComOTectoCheio(t *testing.T) {
	addr, srv, _, fim := servidorComTecto(t, 1)

	// Ocupa a única vaga com uma ligação TCP que não manda nada.
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	prazo := time.Now().Add(2 * time.Second)
	for srv.ligacoes.abertas.Load() != 1 && time.Now().Before(prazo) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := srv.ligacoes.abertas.Load(); n != 1 {
		t.Fatalf("a vaga devia estar ocupada, contador=%d", n)
	}

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

// TestAOS465AVagaVoltaUmaSoVez — o `http.Server` pode fechar a mesma ligação mais do que uma vez.
//
// Sem o `sync.Once` o defeito tem DOIS modos, e a primeira versão deste teste só apanhava o menos
// útil: fechar duas vezes com o semáforo vazio BLOQUEIA o segundo Close para sempre (receber de um
// canal vazio espera), e o teste pendurava em vez de falhar. O modo que importa é o outro — um Close
// repetido DEPOIS de outra ligação ocupar a vaga rouba o lugar dela, e o tecto passa a aceitar a
// mais. É esse que se mede aqui, com o Close repetido em goroutine para um bloqueio não pendurar.
func TestAOS465AVagaVoltaUmaSoVez(t *testing.T) {
	interior, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	contador := &ligacoesAceites{}
	ln := limitarLigacoes(interior, 1, contador)
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
		t.Fatal("um 3o Accept passou com o tecto a 1 e c2 aberta — o Close repetido de c1 devolveu a " +
			"vaga de c2, e o tecto aceita a mais")
	case <-time.After(300 * time.Millisecond):
	}
	if n := contador.abertas.Load(); n != 1 {
		t.Fatalf("contador=%d, esperava 1 (c2 aberta)", n)
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
