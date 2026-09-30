package main

// ligacoes_aceites.go — TECTO DE LIGAÇÕES ACEITES NO http.Server (AOS-465).
//
// Todos os outros tectos do nó (taxa, runs em curso, streams SSE, fila de planos) actuam DEPOIS de
// uma ligação ser aceite. Este é o que está por baixo deles: sem ele, cada ligação TCP aceite custa
// um descritor de ficheiro e uma goroutine até os timeouts a fecharem, e não há número máximo.
//
// Escreve-se aqui em vez de usar `golang.org/x/net/netutil.LimitListener` porque essa dependência não
// está no `go.mod` e o build de produção é offline (`GOPROXY=off`). E a semântica NÃO é a mesma, de
// propósito — ver abaixo.
//
// ATINGIDO O TECTO, UMA LIGAÇÃO NOVA DESPEJA A MAIS ANTIGA QUE NÃO ESTEJA A SER SERVIDA. A primeira
// versão (a do `LimitListener`) fazia o `Accept` esperar, e a revisão adversarial mediu o preço:
// um tecto que espera é um trinco barato. Uma ligação keep-alive depois de um 404 segura a vaga 60 s
// (IdleTimeout), uma ligação h2 com o preâmbulo e SEM pedido segura-a 61 s, e ~17 ligações/s de UMA
// origem mantinham as 1024 ocupadas — com o `/healthz` a falhar 0/4, e a sonda de liveness que o
// README recomenda a reiniciar o pod. Sem tecto nenhum, o mesmo ataque precisava de esgotar os
// descritores do processo. O tecto tinha tornado a negação MAIS barata.
//
// Ocupar uma vaga passa a exigir estar DENTRO de um handler a fazer trabalho do lado do servidor, e
// esse trabalho já tem os seus tectos (taxa, runs em curso, streams SSE — este último validado
// estritamente abaixo deste). A ordem de despejo é:
//
//  1. a ligação SEM handler em curso menos recentemente usada — keep-alive ociosa, h2 sem streams,
//     TCP ou TLS ainda sem pedido, cabeçalhos a pingar (slowloris);
//  2. só se não houver nenhuma: a ligação cujos handlers estão TODOS à espera do CORPO do cliente
//     (corpo lento), outra vez a menos recentemente usada;
//  3. nenhuma das duas: o `Accept` espera por uma vaga, e é o único caso em que espera.
//
// O que isto NÃO fecha, e declara-se: uma inundação de ligações NOVAS despeja-se a si própria e às
// ligações legítimas que ainda não enviaram o pedido, por ordem de chegada. Uma ligação legítima
// sobrevive enquanto a inundação não despejar as mais antigas do que ela — com o tecto por omissão e
// 1000 ligações/s, perto de um segundo, o que chega para o handshake e o pedido. Acima disso é
// inundação volumétrica, e isso é do edge (`limit_conn`/`limit_req`), não de um tecto num processo.
//
// O `Accept` aceita PRIMEIRO e procura vaga DEPOIS. Ao contrário, reservar a vaga antes de aceitar
// obrigaria a despejar uma ligação antes de haver quem a quisesse, e com o tecto a 1 nenhuma ligação
// sobreviveria. O custo é uma ligação a mais por chamador de `Accept` concorrente (o `http.Server`
// tem um): o limite de descritores é `n+1`, não `n`.

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// ligacoesAceites é partilhado entre o listener (que o preenche) e a métrica (que o lê). Nasce em
// [NewAPIServer], antes do handler, para os dois apontarem para o MESMO contador.
type ligacoesAceites struct {
	abertas    atomic.Int64
	despejadas atomic.Int64
}

// limitarLigacoes embrulha `ln` para manter no máximo `n` ligações abertas ao mesmo tempo. O despejo
// só vê que uma ligação está a ser servida se o `http.Server` estiver ligado por [ligarAoServidor];
// sem isso, todas parecem ociosas e o tecto despeja por ordem de uso.
func limitarLigacoes(ln net.Listener, n int, contador *ligacoesAceites) net.Listener {
	return &listenerLimitado{
		Listener: ln,
		vagas:    make(chan struct{}, n),
		fechado:  make(chan struct{}),
		livre:    make(chan struct{}, 1),
		contador: contador,
		vivas:    make(map[*ligacaoLimitada]struct{}, n),
	}
}

type listenerLimitado struct {
	net.Listener
	vagas    chan struct{}
	fechado  chan struct{}
	fecharUm sync.Once
	// livre acorda um `Accept` à espera quando uma ligação SAI de um handler e pode ter passado a ser
	// despejável. Capacidade 1 e envio sem bloqueio: basta um aviso pendente.
	livre    chan struct{}
	contador *ligacoesAceites

	// mu protege `vivas`, `relogio` e os campos de estado de cada [ligacaoLimitada]. É um só mutex
	// para a decisão de despejo e a entrada num handler serem atómicas uma em relação à outra: sem
	// isso, uma ligação escolhida como ociosa podia entrar num handler antes de ser fechada.
	mu    sync.Mutex
	vivas map[*ligacaoLimitada]struct{}
	// relogio é lógico, não de parede: só ordena «menos recentemente usada», e um contador não
	// depende do relógio do sistema nem empata.
	relogio uint64
}

// Accept aceita uma ligação e só então procura vaga para ela: livre, ou despejando outra (ver o
// cabeçalho do ficheiro). Se o listener for fechado enquanto espera, fecha a ligação que tem na mão
// e devolve [net.ErrClosed]: sem isto, um shutdown com todas as vagas em handlers penduraria o
// `Serve`, porque o `Close` do listener interior não acorda quem espera no semáforo.
func (l *listenerLimitado) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	for {
		select {
		case l.vagas <- struct{}{}:
			return l.registar(c), nil
		default:
		}
		// O Close do despejado devolve a vaga ANTES de retornar, e o ciclo volta a tentá-la. Se outro
		// chamador de `Accept` a levar primeiro, despeja-se outra.
		if l.despejarUma() {
			continue
		}
		select {
		case l.vagas <- struct{}{}:
			return l.registar(c), nil
		case <-l.livre:
		case <-l.fechado:
			_ = c.Close()
			return nil, net.ErrClosed
		}
	}
}

func (l *listenerLimitado) Close() error {
	l.fecharUm.Do(func() { close(l.fechado) })
	return l.Listener.Close()
}

func (l *listenerLimitado) registar(c net.Conn) *ligacaoLimitada {
	lc := &ligacaoLimitada{Conn: c, l: l}
	l.mu.Lock()
	lc.ultimoUso = l.relogio
	l.relogio++
	l.vivas[lc] = struct{}{}
	l.mu.Unlock()
	if l.contador != nil {
		l.contador.abertas.Add(1)
	}
	return lc
}

// despejarUma fecha a ligação escolhida pela ordem do cabeçalho e diz se fechou alguma.
func (l *listenerLimitado) despejarUma() bool {
	l.mu.Lock()
	var ociosa, aEsperarCorpo *ligacaoLimitada
	for lc := range l.vivas {
		switch {
		case lc.despejada:
		case lc.handlers == 0 && !lc.emServico:
			if ociosa == nil || lc.ultimoUso < ociosa.ultimoUso {
				ociosa = lc
			}
		case lc.handlers > 0 && lc.handlers == lc.aEsperarCorpo:
			if aEsperarCorpo == nil || lc.ultimoUso < aEsperarCorpo.ultimoUso {
				aEsperarCorpo = lc
			}
		}
	}
	alvo := ociosa
	if alvo == nil {
		alvo = aEsperarCorpo
	}
	if alvo != nil {
		alvo.despejada = true
	}
	l.mu.Unlock()
	if alvo == nil {
		return false
	}
	// Fora do mutex: o Close chama `soltar`, que o toma.
	_ = alvo.Close()
	if l.contador != nil {
		l.contador.despejadas.Add(1)
	}
	return true
}

// entrar / corpoLido / sair / ociosa acompanham o que cada ligação está a fazer. Os três primeiros
// vêm do handler de [ligarAoServidor]; uma ligação h2 pode ter vários handlers ao mesmo tempo, daí
// contadores. O último vem do `ConnState` do servidor.
//
// SAIR DO HANDLER NÃO É FICAR OCIOSA. O `http.Server` escreve a resposta que ficou no buffer DEPOIS de
// o handler retornar, e um despejo nesse intervalo cortava-a — o teste da ligação servida apanhou-o
// com um `unexpected EOF`. Por isso `emServico` só baixa quando o servidor declara a ligação
// `StateIdle`, que em h1 vem depois de a resposta terminar e em h2 quando não resta nenhum stream.
func (l *listenerLimitado) entrar(lc *ligacaoLimitada, comCorpo bool) {
	l.mu.Lock()
	lc.handlers++
	lc.emServico = true
	if comCorpo {
		lc.aEsperarCorpo++
	}
	l.mu.Unlock()
}

func (l *listenerLimitado) corpoLido(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.aEsperarCorpo--
	l.mu.Unlock()
}

func (l *listenerLimitado) sair(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.handlers--
	l.mu.Unlock()
	// Pode ter deixado só handlers à espera de corpo: despejável no segundo escalão.
	l.avisarLivre()
}

func (l *listenerLimitado) ociosa(lc *ligacaoLimitada) {
	l.mu.Lock()
	if lc.handlers == 0 {
		lc.emServico = false
		lc.ultimoUso = l.relogio
		l.relogio++
	}
	l.mu.Unlock()
	l.avisarLivre()
}

func (l *listenerLimitado) avisarLivre() {
	select {
	case l.livre <- struct{}{}:
	default:
	}
}

// ligacaoLimitada devolve a vaga no primeiro Close e só nele: o `http.Server` pode fechar a mesma
// ligação mais do que uma vez (e o despejo acrescenta um terceiro caminho), e uma vaga devolvida duas
// vezes deixaria o tecto aceitar a mais.
type ligacaoLimitada struct {
	net.Conn
	l        *listenerLimitado
	soltarUm sync.Once

	// Protegidos por l.mu.
	handlers      int
	aEsperarCorpo int
	emServico     bool
	ultimoUso     uint64
	despejada     bool
}

func (c *ligacaoLimitada) Close() error {
	err := c.Conn.Close()
	c.soltarUm.Do(c.soltar)
	return err
}

func (c *ligacaoLimitada) soltar() {
	l := c.l
	l.mu.Lock()
	delete(l.vivas, c)
	l.mu.Unlock()
	if l.contador != nil {
		l.contador.abertas.Add(-1)
	}
	// NUNCA BLOQUEIA. Em código correcto cada ligação devolve o token que tomou, e o `default` é
	// inalcançável. Existe porque receber de um canal vazio espera para sempre: uma libertação a mais
	// penduraria uma goroutine do servidor em vez de o defeito se ver.
	select {
	case <-l.vagas:
	default:
	}
}

// CloseWrite e ReadFrom repõem o que o embrulho escondia da `*net.TCPConn`. Sem o primeiro, o
// `http.Server` em claro deixa de fazer o meio-fecho antes de fechar e o cliente pode receber um RST
// que corta a resposta; sem o segundo, perde-se o `sendfile`.
func (c *ligacaoLimitada) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *ligacaoLimitada) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(c.Conn, r)
}

type chaveDaLigacao struct{}

// ligacaoDe encontra a ligação limitada por baixo do que o servidor vê: o `ServeTLS` embrulha-a num
// `*tls.Conn` POR CIMA do listener limitado.
func ligacaoDe(c net.Conn) *ligacaoLimitada {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	lc, _ := c.(*ligacaoLimitada)
	return lc
}

// ligarAoServidor liga o `http.Server` ao tecto: o `ConnContext` guarda a ligação limitada no
// contexto de cada pedido, e o handler embrulhado marca-a como servida enquanto corre. É o que
// separa «ligação ocupada a trabalhar» de «ligação a segurar uma vaga».
func ligarAoServidor(srv *http.Server) {
	srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if lc := ligacaoDe(c); lc != nil {
			return context.WithValue(ctx, chaveDaLigacao{}, lc)
		}
		return ctx
	}
	anterior := srv.ConnState
	srv.ConnState = func(c net.Conn, st http.ConnState) {
		if st == http.StateIdle {
			if lc := ligacaoDe(c); lc != nil {
				lc.l.ociosa(lc)
			}
		}
		if anterior != nil {
			anterior(c, st)
		}
	}
	seguinte := srv.Handler
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lc, _ := r.Context().Value(chaveDaLigacao{}).(*ligacaoLimitada)
		if lc == nil {
			seguinte.ServeHTTP(w, r)
			return
		}
		comCorpo := r.Body != nil && r.Body != http.NoBody
		lc.l.entrar(lc, comCorpo)
		defer lc.l.sair(lc)
		if comCorpo {
			cv := &corpoVigiado{ReadCloser: r.Body, fim: func() { lc.l.corpoLido(lc) }}
			// Um handler que não lê o corpo até ao fim também deixa de esperar por ele quando sai.
			defer cv.terminar()
			r.Body = cv
		}
		seguinte.ServeHTTP(w, r)
	})
}

// corpoVigiado avisa uma vez quando o corpo do pedido acabou: fim, erro ou fecho.
type corpoVigiado struct {
	io.ReadCloser
	fim func()
	um  sync.Once
}

func (b *corpoVigiado) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.terminar()
	}
	return n, err
}

func (b *corpoVigiado) Close() error {
	err := b.ReadCloser.Close()
	b.terminar()
	return err
}

func (b *corpoVigiado) terminar() { b.um.Do(b.fim) }
