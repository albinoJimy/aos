package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// stdioTransport fala JSON-RPC 2.0 newline-delimited sobre o stdio de um
// [SandboxProcess] lançado pela porta [SandboxLauncher]. NÃO abre qualquer socket:
// o único canal é o stdio enquadrado do subprocesso isolado (ADR-004). Por isso o
// STDIO NUNCA consulta a [EgressAllowlist] — não há egress de rede a mediar.
type stdioTransport struct {
	proc SandboxProcess
	mu   sync.Mutex // serializa Call (um round-trip de cada vez)
	enc  io.Writer
	dec  *bufio.Reader
	id   int64
	next func() int64
}

// NewSTDIOTransport lança um servidor MCP local via a porta de sandbox e devolve o
// transporte STDIO. launcher nil → [ErrNoLauncher] (o STDIO corre SEMPRE em sandbox;
// não há caminho fora dela). O idSeq é injectável para determinismo em teste; nil usa
// um contador monotónico a começar em 1.
func NewSTDIOTransport(ctx context.Context, launcher SandboxLauncher, spec LaunchSpec, idSeq func() int64) (Transport, error) {
	if launcher == nil {
		return nil, ErrNoLauncher
	}
	proc, err := launcher.Launch(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("%w: launch: %v", ErrHandshakeFailed, err)
	}
	t := &stdioTransport{
		proc: proc,
		enc:  writerFunc(proc.Stdin().Write),
		dec:  bufio.NewReader(readerFunc(proc.Stdout().Read)),
		next: idSeq,
	}
	if t.next == nil {
		t.next = t.monotonic
	}
	return t, nil
}

func (t *stdioTransport) monotonic() int64 { t.id++; return t.id }

// Kind implementa [Transport].
func (t *stdioTransport) Kind() TransportKind { return TransportSTDIO }

// Call escreve o pedido numa linha e lê linhas até encontrar a resposta com o id
// correspondente (notificações/mensagens sem id são ignoradas). É sequencial.
func (t *stdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc == nil {
		return nil, ErrTransportClosed
	}
	id := t.next()
	req, err := newRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	if _, err := t.enc.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("%w: write: %v", ErrProtocol, err)
	}
	for {
		line, err := t.readBytesCtx(ctx)
		// Cancelamento/deadline do ctx do Call: devolve o erro de contexto CRU (não o
		// embrulha como ErrProtocol) para que o chamador o reconheça. readBytesCtx já
		// terminou o subprocesso para desbloquear a leitura pendente.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil && len(line) == 0 {
			return nil, fmt.Errorf("%w: read: %v", ErrProtocol, err)
		}
		trimmed := trimSpace(line)
		if len(trimmed) == 0 {
			if err != nil {
				return nil, fmt.Errorf("%w: read: %v", ErrProtocol, err)
			}
			continue
		}
		var resp rpcResponse
		if uerr := json.Unmarshal(trimmed, &resp); uerr != nil {
			return nil, fmt.Errorf("%w: unmarshal resposta: %v", ErrProtocol, uerr)
		}
		if resp.ID != id {
			continue // notificação ou resposta de outra chamada; ignora.
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// readBytesCtx lê uma linha (newline-delimited) HONRANDO o cancelamento/deadline do
// ctx do Call. O t.dec.ReadBytes é uma leitura BLOQUEANTE na pipe do subprocesso e não
// é interrompível pelo ctx; por isso corre numa goroutine e faz-se select contra
// ctx.Done(). Em cancelamento, TERMINA o subprocesso (t.proc.Close) — isso força um EOF
// na pipe que desbloqueia o ReadBytes pendente, pelo que a goroutine acaba e não fica
// pendurada. Um Call cancelado deixa, assim, o transporte fechado (fail-closed: não se
// reutiliza um canal cujo subprocesso foi morto). É chamada com t.mu detido.
func (t *stdioTransport) readBytesCtx(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type lineRes struct {
		line []byte
		err  error
	}
	ch := make(chan lineRes, 1) // buffer 1: a goroutine nunca bloqueia a escrever
	go func() {
		line, err := t.dec.ReadBytes('\n')
		ch <- lineRes{line, err}
	}()
	select {
	case <-ctx.Done():
		if t.proc != nil {
			_ = t.proc.Close() // força EOF na pipe → desbloqueia o ReadBytes
			t.proc = nil       // transporte fechado após cancelamento
		}
		<-ch // drena a goroutine (já retornou por EOF); sem leak
		return nil, ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
}

// Close termina o subprocesso isolado.
func (t *stdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc == nil {
		return nil
	}
	err := t.proc.Close()
	t.proc = nil
	return err
}

// writerFunc/readerFunc adaptam os métodos Write/Read do SandboxProcess a
// io.Writer/io.Reader sem expor mais superfície do que o stdio enquadrado.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// trimSpace remove espaços/again de fronteira sem depender de bytes.TrimSpace para
// manter o corte estável (só whitespace ASCII relevante ao enquadramento por linha).
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
