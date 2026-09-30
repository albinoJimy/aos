package main

// ligacoes_aceites.go — TECTO DE LIGAÇÕES ACEITES NO http.Server (AOS-465).
//
// Todos os outros tectos do nó (taxa, runs em curso, streams SSE, fila de planos) actuam DEPOIS de
// uma ligação ser aceite. Este é o que está por baixo deles: sem ele, cada ligação TCP aceite custa
// um descritor de ficheiro e uma goroutine até os timeouts a fecharem, e não há número máximo.
//
// Escreve-se aqui em vez de usar `golang.org/x/net/netutil.LimitListener` porque essa dependência não
// está no `go.mod` e o build de produção é offline (`GOPROXY=off`). A semântica é a mesma.
//
// ATINGIDO O TECTO, O `Accept` ESPERA — não recusa. As ligações novas ficam na fila de backlog do
// kernel até uma vaga abrir, ou até o cliente desistir. É o que limita descritores e goroutines; um
// 503 exigiria aceitar primeiro, que é precisamente o que o tecto existe para não fazer.

import (
	"net"
	"sync"
	"sync/atomic"
)

// ligacoesAceites é partilhado entre o listener (que o preenche) e a métrica (que o lê). Nasce em
// [NewAPIServer], antes do handler, para os dois apontarem para o MESMO contador.
type ligacoesAceites struct {
	abertas atomic.Int64
}

// limitarLigacoes embrulha `ln` para aceitar no máximo `n` ligações abertas ao mesmo tempo.
func limitarLigacoes(ln net.Listener, n int, contador *ligacoesAceites) net.Listener {
	return &listenerLimitado{
		Listener: ln,
		vagas:    make(chan struct{}, n),
		fechado:  make(chan struct{}),
		contador: contador,
	}
}

type listenerLimitado struct {
	net.Listener
	vagas    chan struct{}
	fechado  chan struct{}
	fecharUm sync.Once
	contador *ligacoesAceites
}

// Accept toma uma vaga ANTES de aceitar. Se o listener for fechado enquanto espera por vaga,
// devolve o erro do listener interior em vez de ficar bloqueado: sem isto, um shutdown com o tecto
// cheio penduraria o `Serve` para sempre, porque o `Close` do listener interior não acorda quem
// espera no semáforo.
func (l *listenerLimitado) Accept() (net.Conn, error) {
	select {
	case l.vagas <- struct{}{}:
	case <-l.fechado:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.vagas
		return nil, err
	}
	if l.contador != nil {
		l.contador.abertas.Add(1)
	}
	return &ligacaoLimitada{Conn: c, soltar: func() {
		if l.contador != nil {
			l.contador.abertas.Add(-1)
		}
		// NUNCA BLOQUEIA. Em código correcto cada ligação devolve o token que tomou, e o `default`
		// é inalcançável. Existe porque receber de um canal vazio espera para sempre: uma libertação
		// a mais penduraria uma goroutine do servidor em vez de o defeito se ver.
		select {
		case <-l.vagas:
		default:
		}
	}}, nil
}

func (l *listenerLimitado) Close() error {
	l.fecharUm.Do(func() { close(l.fechado) })
	return l.Listener.Close()
}

// ligacaoLimitada devolve a vaga no primeiro Close e só nele: o `http.Server` pode fechar a mesma
// ligação mais do que uma vez, e uma vaga devolvida duas vezes deixaria o tecto aceitar a mais.
type ligacaoLimitada struct {
	net.Conn
	soltarUm sync.Once
	soltar   func()
}

func (c *ligacaoLimitada) Close() error {
	err := c.Conn.Close()
	c.soltarUm.Do(c.soltar)
	return err
}
