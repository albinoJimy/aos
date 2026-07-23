package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	eventstore "github.com/aos-ref/substrate/eventstore"
)

// AOS-167 — testes do read-path TEMPO-REAL (GET /runs/{id}/trajectory como SSE). Cada teste
// é não-vacuoso: verifica o EFEITO observável no stream (os seqs emitidos, a ordem, a ausência
// de duplicado/lacuna, o desligamento do consumidor lento e a ausência de fuga). Correr SEMPRE
// com -race.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// appendTraj apende um evento ao stream (= runID) e devolve o seq atribuído.
func appendTraj(t *testing.T, es *eventstore.Store, streamID, stepID string) uint64 {
	t.Helper()
	res, err := es.Append(context.Background(), streamID, eventstore.EventInput{
		Type:     "traj.step",
		Payload:  []byte(`{"note":"passo"}`),
		RunID:    streamID,
		StepID:   stepID,
		Producer: eventstore.Producer{NHIID: "nhi:test"},
	})
	if err != nil {
		t.Fatalf("append(%s/%s): %v", streamID, stepID, err)
	}
	return res.Seq
}

// sseMsg é uma mensagem SSE descodificada.
type sseMsg struct {
	id   string
	data string
}

// readSSE lê a próxima mensagem SSE (até à linha em branco). Devolve io.EOF quando o stream
// fecha.
func readSSE(br *bufio.Reader) (sseMsg, error) {
	var m sseMsg
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return m, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if m.id != "" || m.data != "" {
				return m, nil
			}
			continue // salta blanks iniciais
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			m.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			m.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// openTraj abre um stream SSE contra o servidor de teste. lastEventID vazio ⇒ sem header.
func openTraj(t *testing.T, serverURL, runID, lastEventID string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/runs/"+runID+"/trajectory", nil)
	if err != nil {
		cancel()
		t.Fatalf("NewRequest: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET trajectory: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("trajectory devia dar 200, veio %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		resp.Body.Close()
		cancel()
		t.Fatalf("Content-Type devia ser text/event-stream, veio %q", ct)
	}
	return resp, bufio.NewReader(resp.Body), cancel
}

// waitUntil faz polling até cond ser verdadeiro (ou falha por timeout). Usa um poll curto —
// aceitável em teste, nunca em produção.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout à espera de: %s", msg)
}

// newTrajServer compõe um nó + serviço + handler e serve-o num httptest.Server. Devolve o
// servidor, o Event Store partilhado, e limpa tudo no t.Cleanup.
func newTrajServer(t *testing.T, opts ...APIOption) (*httptest.Server, *eventstore.Store) {
	t.Helper()
	node, _ := newAPINode(t, &countingModel{}, false)
	_, h := newAPI(t, node, opts...)
	ts := httptest.NewServer(h)
	t.Cleanup(func() {
		ts.Close()
		_ = node.Close()
	})
	return ts, node.EventStore
}

// ---------------------------------------------------------------------------
// (a) BACKFILL — eventos pré-existentes emitidos por seq crescente com id:=seq.
// ---------------------------------------------------------------------------

func TestAPITrajectoryBackfill(t *testing.T) {
	ts, es := newTrajServer(t)
	for i := 1; i <= 3; i++ {
		appendTraj(t, es, "run-a", "s"+strconv.Itoa(i))
	}
	resp, br, cancel := openTraj(t, ts.URL, "run-a", "")
	defer cancel()
	defer resp.Body.Close()

	for i := 1; i <= 3; i++ {
		m, err := readSSE(br)
		if err != nil {
			t.Fatalf("readSSE #%d: %v", i, err)
		}
		if m.id != strconv.Itoa(i) {
			t.Fatalf("backfill fora de ordem: evento #%d tem id=%q, esperava %d", i, m.id, i)
		}
		var ev eventstore.Event
		if err := json.Unmarshal([]byte(m.data), &ev); err != nil {
			t.Fatalf("data não descodifica como Event: %v (%q)", err, m.data)
		}
		if ev.Seq != uint64(i) || ev.StreamID != "run-a" {
			t.Fatalf("Event descodificado inesperado: seq=%d stream=%q", ev.Seq, ev.StreamID)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) LIVE — um evento apendido APÓS a ligação chega ao cliente.
// ---------------------------------------------------------------------------

func TestAPITrajectoryLive(t *testing.T) {
	ts, es := newTrajServer(t)
	appendTraj(t, es, "run-b", "s1") // backfill: seq 1

	resp, br, cancel := openTraj(t, ts.URL, "run-b", "")
	defer cancel()
	defer resp.Body.Close()

	// Consome o backfill (seq 1).
	if m, err := readSSE(br); err != nil || m.id != "1" {
		t.Fatalf("backfill inicial: id=%q err=%v", m.id, err)
	}

	// Apende um evento NOVO depois da ligação — tem de chegar ao vivo.
	appendTraj(t, es, "run-b", "s2") // seq 2
	m, err := readSSE(br)
	if err != nil {
		t.Fatalf("readSSE live: %v", err)
	}
	if m.id != "2" {
		t.Fatalf("evento live devia ter id=2, veio %q", m.id)
	}
}

// ---------------------------------------------------------------------------
// (c) RESUME — Last-Event-ID=k ⇒ o stream começa em >k (sem re-enviar <=k).
// ---------------------------------------------------------------------------

func TestAPITrajectoryResumeFromSeq(t *testing.T) {
	ts, es := newTrajServer(t)
	for i := 1; i <= 3; i++ {
		appendTraj(t, es, "run-c", "s"+strconv.Itoa(i))
	}
	// Reconecta afirmando ter visto até ao seq 2.
	resp, br, cancel := openTraj(t, ts.URL, "run-c", "2")
	defer cancel()
	defer resp.Body.Close()

	m, err := readSSE(br)
	if err != nil {
		t.Fatalf("readSSE resume: %v", err)
	}
	if m.id != "3" {
		t.Fatalf("resume devia começar em >2 (id=3), veio id=%q — re-enviou histórico já visto ou saltou", m.id)
	}
}

// ---------------------------------------------------------------------------
// (d) DEDUP / SEM-LACUNA — a fronteira backfill→live não duplica nem salta.
// ---------------------------------------------------------------------------

func TestAPITrajectoryDedupNoGap(t *testing.T) {
	ts, es := newTrajServer(t)
	// Histórico inicial 1..5.
	for i := 1; i <= 5; i++ {
		appendTraj(t, es, "run-d", "s"+strconv.Itoa(i))
	}

	resp, br, cancel := openTraj(t, ts.URL, "run-d", "")
	defer cancel()
	defer resp.Body.Close()

	// DETERMINISMO da fronteira: só disparamos os appends 6..10 DEPOIS de recebermos o primeiro
	// evento de backfill (id:1) — nesse ponto a subscrição já está registada e o backfill já
	// começou a fluir, pelo que 6..10 chegam pela via LIVE (não caem todos no snapshot do
	// backfill). Assim a travessia backfill→live é SEMPRE exercitada (o watermark faz o handoff
	// em seq 5→6), e não apenas probabilisticamente. O invariante testado — 1..10 contíguo, sem
	// lacuna nem duplicado — cobre tanto o no-gap (todo seq>watermark passa) como o dedup (nada
	// <=watermark é reenviado).
	first, err := readSSE(br)
	if err != nil {
		t.Fatalf("readSSE backfill inicial: %v", err)
	}
	if first.id != "1" {
		t.Fatalf("primeiro evento devia ser o backfill id=1, veio %q", first.id)
	}
	for i := 6; i <= 10; i++ {
		appendTraj(t, es, "run-d", "s"+strconv.Itoa(i))
	}

	got := []int{1}
	for {
		m, err := readSSE(br)
		if err != nil {
			t.Fatalf("readSSE #%d: %v", len(got)+1, err)
		}
		n, err := strconv.Atoi(m.id)
		if err != nil {
			t.Fatalf("id não-numérico: %q", m.id)
		}
		got = append(got, n)
		if n >= 10 {
			break
		}
	}
	// A sequência recebida tem de ser EXACTAMENTE 1..10, contígua (sem lacuna) e sem
	// duplicado (a fronteira não repete nem salta).
	if len(got) != 10 {
		t.Fatalf("esperava 10 eventos contíguos, recebi %d: %v", len(got), got)
	}
	for i, n := range got {
		if n != i+1 {
			t.Fatalf("sequência com lacuna/duplicado na posição %d: %v", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// (e) BACKPRESSURE — um cliente que NÃO lê é DESLIGADO e NÃO bloqueia um segundo
//     cliente saudável nem o Append ao Event Store.
// ---------------------------------------------------------------------------

// recordingWriter é um ResponseWriter+Flusher que NUNCA bloqueia (cliente saudável). Acumula
// os bytes escritos sob mutex para inspecção concorrente.
type recordingWriter struct {
	hdr  http.Header
	mu   sync.Mutex
	buf  bytes.Buffer
	code int
}

func newRecordingWriter() *recordingWriter { return &recordingWriter{hdr: make(http.Header)} }

func (r *recordingWriter) Header() http.Header  { return r.hdr }
func (r *recordingWriter) WriteHeader(code int) { r.mu.Lock(); r.code = code; r.mu.Unlock() }
func (r *recordingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}
func (r *recordingWriter) Flush() {}
func (r *recordingWriter) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Contains(r.buf.String(), s)
}

// blockingWriter é um ResponseWriter+Flusher cuja Write BLOQUEIA (cliente que parou de ler),
// modelando um socket cheio. Modela FIELMENTE um write-deadline real: a escrita presa só
// desbloqueia (com os.ErrDeadlineExceeded) quando um deadline NÃO-ZERO posto por
// SetWriteDeadline expira — exactamente o mecanismo por-escrita que o handler usa para desligar
// um consumidor preso. Um SetWriteDeadline(zero) (limpeza do deadline herdado) NÃO desbloqueia.
// Sinaliza firstWrite na primeira escrita.
type blockingWriter struct {
	hdr        http.Header
	code       int
	writes     atomic.Int64
	firstWrite chan struct{}
	fwOnce     sync.Once
	release    chan struct{}
	relOnce    sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{
		hdr:        make(http.Header),
		firstWrite: make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (b *blockingWriter) Header() http.Header  { return b.hdr }
func (b *blockingWriter) WriteHeader(code int) { b.code = code }
func (b *blockingWriter) Write(p []byte) (int, error) {
	b.writes.Add(1)
	b.fwOnce.Do(func() { close(b.firstWrite) })
	// Bloqueia até um write-deadline expirar (consumidor preso ⇒ desligado por-escrita).
	<-b.release
	return 0, os.ErrDeadlineExceeded
}
func (b *blockingWriter) Flush() {}
func (b *blockingWriter) SetWriteDeadline(t time.Time) error {
	if t.IsZero() {
		return nil // limpar o deadline (ex.: anular o WriteTimeout herdado) NÃO desbloqueia
	}
	// Deadline real: a escrita presa expira quando t chega.
	if d := time.Until(t); d > 0 {
		time.AfterFunc(d, func() { b.relOnce.Do(func() { close(b.release) }) })
	} else {
		b.relOnce.Do(func() { close(b.release) })
	}
	return nil
}

func TestAPITrajectoryBackpressureDropsSlowConsumer(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	es := node.EventStore

	// Dois handlers sobre o MESMO nó/Event Store: o do cliente lento com um write-deadline
	// por-escrita CURTO (corta o consumidor preso depressa), o do saudável com o default (drena
	// um burst sem falso drop). As subscrições vão ambas ao mesmo store partilhado.
	svc, hSlow := newAPI(t, node, WithTrajectoryWriteTimeout(40*time.Millisecond))
	hHealthy, herr := NewAPIHandler(svc, node)
	if herr != nil {
		t.Fatalf("NewAPIHandler (healthy): %v", herr)
	}

	const runID = "run-bp"
	appendTraj(t, es, runID, "s1") // backfill: seq 1

	// Cliente SAUDÁVEL (drena sempre). Corre o handler numa goroutine; cancela no fim.
	healthy := newRecordingWriter()
	hctx, hcancel := context.WithCancel(context.Background())
	defer hcancel()
	hreq := httptest.NewRequest(http.MethodGet, "/runs/"+runID+"/trajectory", nil).WithContext(hctx)
	go hHealthy.ServeHTTP(healthy, hreq)
	// Espera que o saudável subscreva + receba o backfill (seq 1) antes dos appends live.
	waitUntil(t, 3*time.Second, func() bool { return healthy.contains("id: 1") }, "cliente saudável recebe backfill")

	// Cliente LENTO (bloqueia em Write). Corre o handler numa goroutine.
	slow := newBlockingWriter()
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	sreq := httptest.NewRequest(http.MethodGet, "/runs/"+runID+"/trajectory", nil).WithContext(sctx)
	slowDone := make(chan struct{})
	go func() { hSlow.ServeHTTP(slow, sreq); close(slowDone) }()
	// Espera a primeira escrita do lento (backfill seq 1): garante que já subscreveu e leu.
	select {
	case <-slow.firstWrite:
	case <-time.After(3 * time.Second):
		t.Fatal("cliente lento não chegou a escrever o backfill")
	}

	// Apende eventos LIVE. Os Appends NÃO podem bloquear (fanout non-blocking), mesmo com o
	// consumidor lento preso — verifica-se explicitamente com um timeout.
	appendsDone := make(chan struct{})
	go func() {
		for i := 2; i <= 4; i++ {
			appendTraj(t, es, runID, "s"+strconv.Itoa(i))
		}
		close(appendsDone)
	}()
	select {
	case <-appendsDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Append ao Event Store BLOQUEOU por causa do consumidor lento (fanout devia ser non-blocking)")
	}

	// O cliente lento tem de ser DESLIGADO (o handler retorna).
	select {
	case <-slowDone:
	case <-time.After(3 * time.Second):
		t.Fatal("consumidor lento NÃO foi desligado (handler não retornou)")
	}

	// O cliente saudável continua a receber os eventos live (não foi bloqueado pelo lento).
	waitUntil(t, 3*time.Second, func() bool {
		return healthy.contains("id: 2") && healthy.contains("id: 4")
	}, "cliente saudável recebe os eventos live apesar do consumidor lento")
}

// ---------------------------------------------------------------------------
// (f) LIFECYCLE — cancelar o ctx do cliente limpa a subscrição (sem fuga de
//     goroutine); o Append subsequente não bloqueia.
// ---------------------------------------------------------------------------

func TestAPITrajectoryLifecycleNoLeak(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	es := node.EventStore

	const runID = "run-life"
	appendTraj(t, es, runID, "s1") // backfill: seq 1

	// Um ciclo de aquecimento (para materializar goroutines lazy) antes da baseline.
	openCancelCycle(t, h, runID)

	runtime.GC()
	baseline := runtime.NumGoroutine()

	// Abre e cancela muitas ligações: se cada uma vazasse a subscrição/goroutine, a contagem
	// cresceria monotonicamente e nunca assentaria perto da baseline.
	for i := 0; i < 20; i++ {
		openCancelCycle(t, h, runID)
	}

	// O Append subsequente não pode bloquear (fanout intacto após todas as limpezas).
	done := make(chan struct{})
	go func() { appendTraj(t, es, runID, "s-after"); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Append pós-limpeza BLOQUEOU (fanout comprometido)")
	}

	// As goroutines têm de assentar de volta perto da baseline (sem fuga).
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= baseline+3 {
			return // assentou: sem fuga
		}
		if time.Now().After(deadline) {
			t.Fatalf("fuga de goroutine provável: %d goroutines (baseline %d) após fechar todas as ligações", n, baseline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// openCancelCycle abre um stream de trajectória, espera pela entrega do backfill (a
// subscrição e as goroutines estão de pé) e cancela o ctx (cliente desliga), aguardando o
// handler retornar — o que garante que o defer Unsubscribe correu (join da goroutine da
// subscrição).
func openCancelCycle(t *testing.T, h http.Handler, runID string) {
	t.Helper()
	rec := newRecordingWriter()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/runs/"+runID+"/trajectory", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	waitUntil(t, 3*time.Second, func() bool { return rec.contains("id: 1") }, "ligação recebe backfill")
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler não retornou após cancelamento do ctx (limpeza presa)")
	}
}

// ---------------------------------------------------------------------------
// (g) TRANSPORTE FAIL-SAFE — servido pelo APIServer REAL de produção (AOS-166), a
//     ligação SSE SOBREVIVE para além do WriteTimeout por-ligação do http.Server.
// ---------------------------------------------------------------------------

func TestAPITrajectoryTransportFailSafe(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Servidor de PRODUÇÃO real (APIServer de AOS-166) com um WriteTimeout por-ligação CURTO —
	// exactamente o caminho onde o defeito 'SSE cortado ao fim do WriteTimeout' se manifesta.
	// httptest.NewServer NÃO arma WriteTimeout, pelo que este defeito só aparece aqui.
	srv, err := NewAPIServer(svc, node, WithServerWriteTimeout(150*time.Millisecond))
	if err != nil {
		t.Fatalf("NewAPIServer: %v", err)
	}
	ln, err := srv.listen("127.0.0.1:0") // loopback ⇒ o bind-guardrail permite
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.http.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	es := node.EventStore
	const runID = "run-failsafe"
	appendTraj(t, es, runID, "s1") // backfill: seq 1

	resp, br, cancel := openTraj(t, "http://"+ln.Addr().String(), runID, "")
	defer cancel()
	defer resp.Body.Close()

	// Consome o backfill (seq 1).
	if m, err := readSSE(br); err != nil || m.id != "1" {
		t.Fatalf("backfill inicial: id=%q err=%v", m.id, err)
	}

	// Espera BEM ALÉM do WriteTimeout por-ligação do servidor (150ms). Sem o transporte
	// fail-safe (anular o deadline herdado + deadlines só por-escrita), a ligação seria cortada
	// aqui e o evento live seguinte NUNCA chegaria.
	time.Sleep(450 * time.Millisecond)

	// Evento live DEPOIS da janela do WriteTimeout: TEM de chegar (a ligação sobreviveu).
	appendTraj(t, es, runID, "s2") // seq 2
	m, err := readSSE(br)
	if err != nil {
		t.Fatalf("ligação cortada pelo WriteTimeout do servidor — transporte NÃO fail-safe: %v", err)
	}
	if m.id != "2" {
		t.Fatalf("evento live pós-WriteTimeout devia ter id=2, veio %q", m.id)
	}
}

// ---------------------------------------------------------------------------
// (h) SEM FALSO-DROP no backfill — histórico grande + burst live concorrente: um
//     cliente que drena completa o backfill e recebe todo o live (não é cortado como
//     'consumidor lento' por profundidade de buffer).
// ---------------------------------------------------------------------------

func TestAPITrajectoryNoFalseDropDuringBackfill(t *testing.T) {
	ts, es := newTrajServer(t)
	const runID = "run-nofalsedrop"
	const history = 150 // >> qualquer buffer fixo (o antigo default era 64)
	const live = 150

	for i := 1; i <= history; i++ {
		appendTraj(t, es, runID, "s"+strconv.Itoa(i))
	}
	// Burst LIVE concorrente com a ligação. Sob o antigo buffer fixo (64), estes eventos
	// encheriam o buffer DURANTE a emissão do backfill e cortariam o cliente SAUDÁVEL como
	// 'consumidor lento'. Com o drop-slow-consumer por PROGRESSO (write-deadline), um cliente
	// que drena completa o backfill e recebe todo o live — sem falso-drop.
	go func() {
		for i := history + 1; i <= history+live; i++ {
			appendTraj(t, es, runID, "s"+strconv.Itoa(i))
		}
	}()

	resp, br, cancel := openTraj(t, ts.URL, runID, "")
	defer cancel()
	defer resp.Body.Close()

	got, last := 0, 0
	for {
		m, err := readSSE(br)
		if err != nil {
			t.Fatalf("cliente saudável cortado como falso 'consumidor lento' após %d eventos: %v", got, err)
		}
		n, err := strconv.Atoi(m.id)
		if err != nil {
			t.Fatalf("id não-numérico: %q", m.id)
		}
		if n != last+1 {
			t.Fatalf("lacuna/duplicado: recebi %d após %d", n, last)
		}
		last, got = n, got+1
		if n >= history+live {
			break
		}
	}
	if got != history+live {
		t.Fatalf("esperava %d eventos contíguos, recebi %d", history+live, got)
	}
}

// ---------------------------------------------------------------------------
// (i) NÃO-ENUMERÁVEL — um runID que esta réplica não hospeda nem reteve devolve o
//     MESMO 404 uniforme que handleGet, sem abrir um stream SSE vazio.
// ---------------------------------------------------------------------------

func TestAPITrajectoryUnknownRunNotEnumerable(t *testing.T) {
	ts, _ := newTrajServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/desconhecido/trajectory", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET trajectory desconhecido: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("run desconhecido devia dar 404 (não-enumerável, coerente com handleGet), veio %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("run desconhecido NÃO devia abrir um stream SSE (Content-Type=%q)", ct)
	}
}

// ---------------------------------------------------------------------------
// (j) ADMISSION — o tecto de streams SSE concorrentes barra ligações em excesso (429),
//     sem abrir subscrição (anti-exaustão coerente com o hardening de AOS-166).
// ---------------------------------------------------------------------------

func TestAPITrajectoryConcurrencyCeiling(t *testing.T) {
	ts, es := newTrajServer(t, WithMaxTrajectoryConns(1))
	const runID = "run-ceiling"
	appendTraj(t, es, runID, "s1") // backfill: seq 1

	// Primeira ligação ocupa o único slot (fica no loop live após o backfill).
	resp1, br1, cancel1 := openTraj(t, ts.URL, runID, "")
	defer cancel1()
	defer resp1.Body.Close()
	if m, err := readSSE(br1); err != nil || m.id != "1" {
		t.Fatalf("primeira ligação backfill: id=%q err=%v", m.id, err)
	}

	// Segunda ligação CONCORRENTE: excede o tecto ⇒ 429 (sem abrir stream).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("segunda ligação: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("segunda ligação concorrente devia dar 429 (tecto de streams), veio %d", resp2.StatusCode)
	}
}
