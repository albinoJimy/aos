package main

// ligacoes_aceites.go — TECTO DE LIGAÇÕES ACEITES NO http.Server (AOS-465).
//
// Todos os outros tectos do nó (taxa, runs em curso, streams SSE, fila de planos) actuam DEPOIS de
// uma ligação ser aceite. Este é o que está por baixo deles: sem ele, cada ligação TCP aceite custa
// um descritor de ficheiro e uma goroutine até os timeouts a fecharem, e não há número máximo.
//
// Escreve-se aqui em vez de usar `golang.org/x/net/netutil.LimitListener` porque essa dependência não
// está no `go.mod` e o build de produção é offline (`GOPROXY=off`). E a semântica NÃO é a mesma, de
// propósito.
//
// UM TECTO QUE ESPERA É UM TRINCO BARATO. A primeira versão fazia o `Accept` esperar no tecto, e a
// revisão adversarial mediu o preço: uma ligação keep-alive depois de um 404 segura a vaga 60 s, uma
// h2 com o preâmbulo e sem pedido 61 s, e ~17 ligações/s de UMA origem mantinham as 1024 ocupadas com
// o `/healthz` a falhar. Sem tecto nenhum, o mesmo ataque precisava de esgotar os descritores.
//
// A SEGUNDA VERSÃO CLASSIFICAVA ESTADOS, E CADA ESTADO TINHA UMA VARIANTE. Protegia «ligações com
// handler em curso sem corpo por ler» e «ligações cuja resposta o servidor ainda não terminou». A
// revisão seguinte mediu três formas de segurar vagas em estados protegidos, todas à espera do
// CLIENTE: h2 com RST_STREAM a meio de um handler (a ligação ficava «em serviço» 60 s), o descarte do
// corpo que o servidor faz DEPOIS de o handler sair (15 s), e h2 com janela de controlo de fluxo a 0,
// com a escrita da resposta presa (15 s). E em h2 todo o pedido tem corpo, por isso até um stream SSE
// saudável era despejável.
//
// O CRITÉRIO PASSOU A SER ONDE O SERVIDOR ESTÁ BLOQUEADO, não em que estado a ligação está. Uma
// ligação só está protegida enquanto um handler dela faz trabalho do SERVIDOR — isto é, não está
// dentro de uma leitura do corpo nem de uma escrita da resposta, que é quando espera pelo cliente.
// Com o tecto atingido, uma ligação nova despeja:
//
//  1. a ligação sem handler e declarada ociosa pelo servidor (`StateIdle`), ou ainda sem pedido —
//     keep-alive, h2 sem streams, TCP ou TLS sem bytes, cabeçalhos a pingar;
//  2. só se não houver nenhuma: a ligação em que o servidor espera pelo cliente — todos os handlers
//     dentro de uma leitura do corpo ou de uma escrita da resposta, ou nenhum handler mas a resposta
//     por terminar (descarte do corpo, escrita presa no controlo de fluxo);
//  3. nenhuma das duas: o `Accept` espera, e é o único caso em que espera.
//
// Dentro de cada escalão sai a que está À ESPERA HÁ MAIS TEMPO: o relógio lógico `ultimoUso` avança
// quando a ligação chega, fica ociosa, entra numa leitura ou escrita, ou sai do handler. Uma escrita
// legítima para um cliente que lê dura microssegundos e é sempre a mais recente; uma escrita presa
// por um cliente que não lê fica cada vez mais antiga.
//
// E NENHUMA LIGAÇÃO SE DESPEJA ANTES DE ESTAR NO ESTADO ACTUAL HÁ [GracaDeDespejo] — dentro do
// PRAZO de cada chegada, que é essa mesma graça (ver [listenerLimitado.despejarUma]). Em h2 o
// `StateIdle` chega quando o stream fecha, com a última trama ainda no buffer do servidor e não no
// socket; um despejo nesse instante cortava a resposta (medido: `unexpected EOF` num pedido h2 que o
// servidor tinha acabado de servir). Em h1 o análogo é o RST de um fecho com dados do cliente por ler,
// que é a razão do `rstAvoidanceDelay` do próprio `net/http`. As ligações que os ataques medidos
// usam estão no mesmo estado há segundos; uma resposta legítima acabada, ou uma ligação acabada de
// chegar, fica protegida durante a graça. Esgotado o prazo da chegada, a graça cede: uma ligação nova
// nunca espera mais do que ela, salvo com TODAS as vagas a fazer trabalho do servidor. O preço é que,
// com todas as vagas mais novas do que a graça (inundação volumétrica), a ligação nova pode despejar
// uma resposta acabada de terminar.
//
// O que isto NÃO fecha, e declara-se:
//   - uma inundação de ligações NOVAS despeja-se a si própria e às legítimas ainda sem pedido, por
//     ordem de chegada. Acima de centenas por segundo é volumétrico e é do edge (`limit_conn`);
//   - com o segundo escalão a ser o único, uma ligação nova pode despejar uma legítima a meio de uma
//     escrita, ou no intervalo entre o handler sair e a resposta terminar. Só acontece com todas as
//     vagas ocupadas por ligações à espera do cliente, e a escolhida é sempre a que espera há mais
//     tempo;
//   - ocupar uma vaga protegida exige trabalho do servidor dentro de um handler, e isso é limitado
//     pelos tectos acima deste (taxa, runs em curso, SSE), não por este.
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
	"time"
)

// GracaDeDespejo é quanto tempo uma ligação tem de estar no estado actual antes de poder ser
// despejada (ver o cabeçalho do ficheiro). Cobre com folga o flush de uma resposta que acabou de
// terminar; é muito menor do que o tempo que os ataques medidos seguram uma vaga (5 a 60 s).
const GracaDeDespejo = 250 * time.Millisecond

// ligacoesAceites é partilhado entre o listener (que o preenche) e a métrica (que o lê). Nasce em
// [NewAPIServer], antes do handler, para os dois apontarem para o MESMO contador.
type ligacoesAceites struct {
	abertas    atomic.Int64
	despejadas atomic.Int64
}

// limitarLigacoes embrulha `ln` para manter no máximo `n` ligações abertas ao mesmo tempo. O despejo
// só vê o que cada ligação está a fazer se o `http.Server` estiver ligado por [ligarAoServidor]; sem
// isso, todas parecem ociosas e o tecto despeja por ordem de chegada.
func limitarLigacoes(ln net.Listener, n int, contador *ligacoesAceites) net.Listener {
	return &listenerLimitado{
		Listener: ln,
		vagas:    make(chan struct{}, n),
		fechado:  make(chan struct{}),
		livre:    make(chan struct{}, 1),
		contador: contador,
		graca:    GracaDeDespejo,
		agora:    time.Now,
		vivas:    make(map[*ligacaoLimitada]struct{}, n),
	}
}

type listenerLimitado struct {
	net.Listener
	vagas    chan struct{}
	fechado  chan struct{}
	fecharUm sync.Once
	// livre acorda um `Accept` à espera quando uma ligação fica ociosa. Capacidade 1 e envio sem
	// bloqueio: basta um aviso pendente.
	livre    chan struct{}
	contador *ligacoesAceites
	graca    time.Duration
	agora    func() time.Time

	// mu protege `vivas`, `relogio` e os campos de estado de cada [ligacaoLimitada]. É um só mutex
	// para a decisão de despejo e as transições de estado serem atómicas umas em relação às outras.
	mu    sync.Mutex
	vivas map[*ligacaoLimitada]struct{}
	// relogio é lógico, não de parede: só ordena «à espera há mais tempo», e um contador não depende
	// do relógio do sistema nem empata.
	relogio uint64
}

// Accept aceita uma ligação e só então procura vaga para ela: livre, ou despejando outra (ver o
// cabeçalho do ficheiro). Se o listener for fechado enquanto espera, fecha a ligação que tem na mão
// e devolve [net.ErrClosed]: sem isto, um shutdown com todas as vagas em handlers penduraria o
// `Serve`, porque o `Close` do listener interior não acorda quem espera no semáforo.
//
// NÃO É ACORDADO quando um handler sai ou entra numa escrita — só quando uma ligação fica ociosa ou
// fecha. Acordar à saída do handler despejaria respostas legítimas no intervalo antes de terminarem
// (medido: `unexpected EOF`); acordar à entrada de uma escrita despejaria streams SSE a meio de um
// evento. Uma ligação nova que CHEGA avalia tudo de novo.
func (l *listenerLimitado) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	// O PRAZO conta a partir da primeira vez que há uma candidata, não da chegada. Esperar com todas
	// as vagas a trabalhar é espera legítima e não gasta prazo; contá-lo da chegada deixava uma ligação
	// que esperou segundos despejar sem graça a primeira resposta que acabasse (medido: um pedido h2
	// cortado com `unexpected EOF`).
	var prazoDesde time.Time
	for {
		select {
		case l.vagas <- struct{}{}:
			return l.registar(c), nil
		default:
		}
		// O Close do despejado devolve a vaga ANTES de retornar, e o ciclo volta a tentá-la. Se outro
		// chamador de `Accept` a levar primeiro, despeja-se outra.
		var esperou time.Duration
		if !prazoDesde.IsZero() {
			esperou = l.agora().Sub(prazoDesde)
		}
		despejou, amadurece := l.despejarUma(!prazoDesde.IsZero() && esperou >= l.graca)
		if despejou {
			continue
		}
		if amadurece > 0 && prazoDesde.IsZero() {
			prazoDesde = l.agora()
			esperou = 0
		}
		// Sem candidata madura: acorda quando a primeira amadurecer ou quando o PRAZO desta chegada
		// acabar, o que vier primeiro. Sem candidatas nenhumas (todas a trabalhar), só `livre` ou uma
		// vaga a acordam.
		var t *time.Timer
		var relogio <-chan time.Time
		if amadurece > 0 {
			if prazo := l.graca - esperou; prazo < amadurece {
				amadurece = prazo
			}
			t = time.NewTimer(amadurece)
			relogio = t.C
		}
		// Parado a cada volta e não num `defer`: dentro do `for`, um `defer` acumulava um temporizador
		// por cada `StateIdle` enquanto este `Accept` esperasse (medido: +2,6 MB em 200 000 voltas).
		select {
		case l.vagas <- struct{}{}:
			pararRelogio(t)
			return l.registar(c), nil
		case <-l.livre:
		case <-relogio:
		case <-l.fechado:
			pararRelogio(t)
			_ = c.Close()
			return nil, net.ErrClosed
		}
		pararRelogio(t)
	}
}

func pararRelogio(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

func (l *listenerLimitado) Close() error {
	l.fecharUm.Do(func() { close(l.fechado) })
	return l.Listener.Close()
}

// marcar avança o relógio lógico da ligação e regista quando entrou no estado actual. Chamar com
// l.mu tomado.
func (l *listenerLimitado) marcar(lc *ligacaoLimitada) {
	lc.ultimoUso = l.relogio
	l.relogio++
	lc.desde = l.agora()
}

func (l *listenerLimitado) registar(c net.Conn) *ligacaoLimitada {
	lc := &ligacaoLimitada{Conn: c, l: l}
	l.mu.Lock()
	l.marcar(lc)
	l.vivas[lc] = struct{}{}
	l.mu.Unlock()
	if l.contador != nil {
		l.contador.abertas.Add(1)
	}
	return lc
}

// escalao classifica uma ligação para o despejo: 1 e 2 como no cabeçalho do ficheiro, 0 protegida.
// Chamar com l.mu tomado.
func (lc *ligacaoLimitada) escalao() int {
	switch {
	case lc.handlers == 0 && !lc.emServico:
		return 1
	case lc.handlers == 0: // handler saiu, resposta por terminar
		return 2
	case lc.emIO == lc.handlers: // todos os handlers à espera do cliente
		return 2
	}
	return 0
}

// despejarUma fecha a ligação escolhida pela ordem do cabeçalho e diz se fechou alguma.
//
// DENTRO DO PRAZO da chegada que a chamou (`prazoEsgotado` falso), o escalão manda antes da graça: se o
// escalão 1 tem candidatas mas nenhuma madura, espera-se que a primeira amadureça em vez de despejar
// uma madura do escalão 2, que pode estar a meio de uma escrita legítima.
//
// ESGOTADO O PRAZO, a graça deixa de mandar: primeiro as maduras (escalão 1, depois 2), depois as
// imaturas (idem). Sem este limite, a graça era renovável por TERCEIROS — a revisão mediu uma só
// ligação keep-alive a pedir a cada 150 ms a manter-se no escalão 1 imatura para sempre, e com isso a
// impedir todos os despejos do escalão 2: `/healthz` 0/28 em 60 s, 0 despejos, sem token. E todas as
// vagas mantidas imaturas pelo mesmo meio trancavam também. Com o prazo, uma ligação nova espera no
// máximo [GracaDeDespejo] a partir do momento em que há uma candidata — isto é, salvo enquanto TODAS
// as vagas estiverem a fazer trabalho do servidor.
//
// Se não fechou nenhuma, diz quanto falta para a primeira candidata do escalão escolhido amadurecer
// (0 se não há candidatas).
func (l *listenerLimitado) despejarUma(prazoEsgotado bool) (bool, time.Duration) {
	l.mu.Lock()
	var madura, imatura [3]*ligacaoLimitada
	var amadurece [3]time.Duration
	agora := l.agora()
	for lc := range l.vivas {
		if lc.despejada {
			continue
		}
		e := lc.escalao()
		if e == 0 {
			continue
		}
		if falta := l.graca - agora.Sub(lc.desde); falta > 0 {
			if amadurece[e] == 0 || falta < amadurece[e] {
				amadurece[e] = falta
			}
			if imatura[e] == nil || lc.ultimoUso < imatura[e].ultimoUso {
				imatura[e] = lc
			}
			continue
		}
		if madura[e] == nil || lc.ultimoUso < madura[e].ultimoUso {
			madura[e] = lc
		}
	}
	var alvo *ligacaoLimitada
	e := 2
	if madura[1] != nil || imatura[1] != nil {
		e = 1
	}
	if prazoEsgotado {
		for _, c := range []*ligacaoLimitada{madura[1], madura[2], imatura[1], imatura[2]} {
			if c != nil {
				alvo = c
				break
			}
		}
	} else {
		alvo = madura[e]
	}
	if alvo != nil {
		alvo.despejada = true
	}
	l.mu.Unlock()
	if alvo == nil {
		return false, amadurece[e]
	}
	// Fora do mutex: o Close chama `soltar`, que o toma.
	_ = alvo.Close()
	if l.contador != nil {
		l.contador.despejadas.Add(1)
	}
	return true, 0
}

// entrar / sair acompanham os handlers em curso (uma ligação h2 pode ter vários), entrarIO / sairIO
// o tempo que cada um passa bloqueado no cliente, e ociosa o `StateIdle` do servidor.
func (l *listenerLimitado) entrar(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.handlers++
	lc.emServico = true
	l.mu.Unlock()
}

func (l *listenerLimitado) sair(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.handlers--
	l.marcar(lc)
	l.mu.Unlock()
}

func (l *listenerLimitado) entrarIO(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.emIO++
	l.marcar(lc)
	l.mu.Unlock()
}

func (l *listenerLimitado) sairIO(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.emIO--
	l.mu.Unlock()
}

// ociosa limpa a marca de serviço SEM olhar para os handlers. Em h1 o `StateIdle` só chega depois de
// a resposta terminar, sem handler nenhum. Em h2 chega quando não resta nenhum stream aberto — e pode
// chegar ANTES de o handler de um stream cancelado (RST_STREAM) sair. A versão anterior ignorava o
// aviso nesse caso e a ligação ficava «em serviço» até ao IdleTimeout (medido: 60 s por ligação, o
// mesmo custo do trinco original). O handler que ainda corre continua a contar em `handlers`.
func (l *listenerLimitado) ociosa(lc *ligacaoLimitada) {
	l.mu.Lock()
	lc.emServico = false
	l.marcar(lc)
	l.mu.Unlock()
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
	handlers  int
	emIO      int
	emServico bool
	ultimoUso uint64
	desde     time.Time
	despejada bool
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
// contexto de cada pedido, o `ConnState` avisa quando fica ociosa, e o handler embrulhado conta os
// handlers em curso e o tempo que cada um passa bloqueado no cliente.
//
// O PEDIDO É UMA CÓPIA RASA. Mudar `r.Body` no pedido original mudaria também o que o `http.Server`
// guarda dele, e a decisão de descartar ou fechar depois do handler olha para o tipo desse corpo: a
// revisão mediu um 404 a um corpo de 10 MB que o servidor fechava de imediato passar a esperar o
// ReadTimeout inteiro.
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
		lc.l.entrar(lc)
		defer lc.l.sair(lc)
		copia := r.WithContext(r.Context())
		if r.Body != nil {
			copia.Body = &corpoVigiado{ReadCloser: r.Body, lc: lc}
		}
		seguinte.ServeHTTP(&respostaVigiada{ResponseWriter: w, lc: lc}, copia)
	})
}

// corpoVigiado marca o handler como bloqueado no cliente enquanto está DENTRO de uma leitura.
type corpoVigiado struct {
	io.ReadCloser
	lc *ligacaoLimitada
}

func (b *corpoVigiado) Read(p []byte) (int, error) {
	b.lc.l.entrarIO(b.lc)
	defer b.lc.l.sairIO(b.lc)
	return b.ReadCloser.Read(p)
}

// respostaVigiada faz o mesmo para a escrita da resposta: um cliente que não lê (janela TCP cheia, ou
// janela h2 a 0) deixa o handler preso aqui até ao WriteTimeout. `Flush` existe porque o SSE exige
// `http.Flusher`; `Unwrap` porque o `http.ResponseController` do SSE precisa de chegar ao original
// para o `SetWriteDeadline`.
type respostaVigiada struct {
	http.ResponseWriter
	lc *ligacaoLimitada
}

func (w *respostaVigiada) Write(p []byte) (int, error) {
	w.lc.l.entrarIO(w.lc)
	defer w.lc.l.sairIO(w.lc)
	return w.ResponseWriter.Write(p)
}

func (w *respostaVigiada) FlushError() error {
	w.lc.l.entrarIO(w.lc)
	defer w.lc.l.sairIO(w.lc)
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *respostaVigiada) Flush() { _ = w.FlushError() }

func (w *respostaVigiada) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// semVigia devolve o `ResponseWriter` do servidor por baixo do embrulho, para o [http.MaxBytesReader].
// Ele avisa o servidor de que o limite do corpo foi atingido por um método NÃO exportado do
// `ResponseWriter` original, sem desembrulhar: com o embrulho, o servidor deixava de fechar a ligação
// depois do 413 e tentava drenar o resto do corpo. Quem chama o `MaxBytesReader` tem de passar por
// aqui — e o `TestAOS465O413ContinuaAFecharALigacao` mede-o.
func semVigia(w http.ResponseWriter) http.ResponseWriter {
	if v, ok := w.(*respostaVigiada); ok {
		return v.ResponseWriter
	}
	return w
}
