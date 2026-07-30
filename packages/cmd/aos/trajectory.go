// AOS-167 — o read-path TEMPO-REAL do nó: GET /runs/{id}/trajectory como Server-Sent
// Events (text/event-stream; D3 fixado). Streama os EVENTOS da trajectória do run (o
// stream_id do Event Store é o RunID) POR SEQ — não fotografias de estado. É o complemento
// de GET /runs/{id} (AOS-166), que dá a fotografia do desfecho; aqui dá-se o log ao vivo.
//
// GARANTIAS (todas com stdlib pura — net/http + encoding/json):
//
//   - BACKFILL. Um cliente que liga tarde vê o que já aconteceu: eventstore.Read(runID,
//     fromSeq) emite o histórico desde fromSeq por seq crescente.
//
//   - RESUME-FROM-SEQ. O cliente reconecta com o header Last-Event-ID = último seq visto; o
//     backfill retoma em seq > lastSeq (Read é inclusivo, pelo que se lê a partir de
//     lastSeq+1) — sem re-enviar o que já viu, sem lacuna.
//
//   - LIVE. Após o backfill, uma subscrição (Subscribe, filtro=stream do run) empurra os
//     eventos NOVOS.
//
//   - DEDUP / SEM-LACUNA na fronteira backfill→live. A subscrição é registada ANTES do
//     backfill: assim nenhum evento com seq acima do snapshot do backfill se perde (sem
//     lacuna). O overlap (eventos vistos tanto no backfill como ao vivo) é eliminado por um
//     WATERMARK = maior seq já emitido: um evento ao vivo com seq <= watermark é SALTADO
//     (sem duplicado).
//
//   - BACKPRESSURE / drop-slow-consumer SEM FALSO-DROP. O callback da subscrição NUNCA
//     escreve directamente no ResponseWriter (bloquearia o fanout do Event Store e todos os
//     outros observadores). Enfileira num buffer POR-LIGAÇÃO (fila FIFO ilimitada sob mutex,
//     enqueue O(1) que NUNCA bloqueia) e sinaliza a goroutine de escrita. Um consumidor é
//     desligado por PROGRESSO DE ESCRITA, não por profundidade de buffer: cada escrita no
//     socket é limitada por um write-deadline PRÓPRIO (SetWriteDeadline via ResponseController);
//     um cliente que parou de ler faz a escrita bloquear, o deadline dispara e a ligação cai.
//     Isto NÃO confunde "backfill grande + burst live" (o cliente progride nas escritas — não
//     é cortado) com "consumidor preso" (uma escrita não retorna — é cortado): o defeito de
//     falso-drop do buffer-de-capacidade-fixa fica eliminado.
//
//   - TRANSPORTE FAIL-SAFE (AC de saída do EPIC-15). Uma ligação SSE é LONGA por natureza. O
//     http.Server de produção (AOS-166) arma um WriteTimeout por-ligação (DefaultWriteTimeout)
//     que cortaria QUALQUER stream ao fim desse tempo. handleTrajectory ISENTA a ligação desse
//     deadline herdado (SetWriteDeadline(zero) no arranque) e re-arma um deadline CURTO só à
//     volta de cada escrita — a ligação sobrevive indefinidamente enquanto o cliente drena,
//     mas um cliente preso continua a ser cortado por-escrita.
//
//   - ADMISSION / ANTI-EXAUSTÃO. Cada stream abre 1 subscrição + goroutines mantidas até o
//     cliente desligar. Um tecto de streams SSE concorrentes por-nó (contador atómico) barra a
//     exaustão de recursos dentro da fronteira de confiança — coerente com o hardening de
//     ingresso de AOS-166. Exceder ⇒ 429.
//
//   - NÃO-ENUMERÁVEL / POSSE. Antes de comprometer o SSE, exige-se POSSE do run nesta réplica
//     (mesmo critério de handleGet: eventos retidos no store OU o run conhecido pelo serviço).
//     Um runID que esta réplica não hospeda nem reteve devolve o MESMO 404 uniforme que
//     handleGet — sem abrir um stream vazio nem virar oráculo de existência. A autorização
//     POR-CHAMADOR do payload byte-a-byte e o selo WORM de leitura (D6/D7) são AOS-172; aqui a
//     rede só é exposta com authn pelo bind-guardrail de AOS-166.
//
//   - FAIL-CLOSED / SEM FUGAS. Flush por evento (streaming real). O ctx do pedido cancelado
//     (cliente desliga) cancela a subscrição e a goroutine de escrita — sem fuga de goroutine
//     nem de subscrição.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// sseConn é o estado por-ligação do stream de trajectória. A fila desacopla o fanout do Event
// Store (produtor, na goroutine da subscrição) da escrita no socket (consumidor, na goroutine
// do pedido): o produtor faz um enqueue O(1) que NUNCA bloqueia; o consumidor drena.
//
// Partilha entre goroutines (seguro para -race):
//   - queue: fila FIFO protegida por mu; o produtor (onEvent) faz append, o consumidor (a
//     goroutine do pedido) faz o pop — ambos sob mu;
//   - notify: sinal coalescido (cap 1) que acorda o consumidor quando estava ocioso; nunca
//     bloqueia o produtor (send não-bloqueante);
//   - rc: *http.ResponseController — SetWriteDeadline é seguro concorrentemente com Write (é o
//     mecanismo stdlib para limitar/interromper uma escrita); só a goroutine do pedido lhe toca;
//   - watermark: acedido SÓ pela goroutine do pedido (backfill + drain); o produtor nunca lhe
//     toca — logo não há corrida.
type sseConn struct {
	mu        sync.Mutex
	queue     []eventstore.Event
	notify    chan struct{}
	rc        *http.ResponseController
	watermark uint64
}

// onEvent é o callback da subscrição (corre na goroutine da subscrição do Event Store). NUNCA
// bloqueia nem descarta: enfileira o evento na fila por-ligação e acorda o consumidor. Assim o
// fanout do Event Store — e todos os outros subscritores — nunca são bloqueados nem perdem
// eventos por causa deste cliente. Um cliente que não drena é cortado pela goroutine do pedido
// via o write-deadline por-escrita (ver writeEvent), NÃO por um limite de profundidade — o que
// evita o falso-drop de um cliente saudável que ainda está a receber um backfill grande enquanto
// chega um burst ao vivo.
func (c *sseConn) onEvent(ev eventstore.Event) {
	c.mu.Lock()
	c.queue = append(c.queue, ev)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default: // já sinalizado; o consumidor drena a fila inteira ao acordar
	}
}

// pop retira o próximo evento da fila (ok=false se vazia). O(1) amortizado.
func (c *sseConn) pop() (eventstore.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return eventstore.Event{}, false
	}
	ev := c.queue[0]
	c.queue = c.queue[1:]
	return ev, true
}

// handleTrajectory serve GET /runs/{id}/trajectory como SSE. Ordem deliberada:
//  1. valida + tecto de concorrência (admission) + verifica suporte a streaming (http.Flusher);
//  2. ISENTA a ligação do WriteTimeout herdado do http.Server (SSE é longo — transporte fail-safe);
//  3. SUBSCRIBE primeiro (garante SEM-LACUNA face ao backfill);
//  4. BACKFILL via Read + POSSE — stream inexistente E run desconhecido ⇒ 404 uniforme
//     (não-enumerável); outro erro ⇒ status HTTP (headers não enviados);
//  5. só então compromete o SSE (200 + headers) e emite backfill + live com dedup por
//     watermark, drop-slow-consumer por write-deadline e limpeza no cancelamento do ctx.
func (h *apiHandler) handleTrajectory(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// (0) AUTHZ SOBERANA POR-CHAMADOR (AOS-172, D7 — a authz que AOS-167 deferiu para aqui).
	// Board→região fail-closed; leitor não autorizado ⇒ o MESMO 404 uniforme de um run
	// inexistente (não-enumerável, sem PII). Gate não composto ⇒ legado. Feita ANTES da
	// admission para NÃO consumir recursos (subscrição/goroutines/tecto trajConns) por um
	// leitor não autorizado.
	reader, residency, authorized := h.admitSovereignRead(w, r, runID)
	if !authorized {
		return
	}

	// (1) ADMISSION — tecto de streams SSE concorrentes por-nó (anti-exaustão, coerente com o
	// hardening de ingresso de AOS-166). Incrementado cedo, decrementado ao sair: bounda o número
	// de subscrições + goroutines vivas, incluindo tentativas para runs inexistentes.
	if n := h.trajConns.Add(1); h.cfg.trajMaxConns > 0 && int(n) > h.cfg.trajMaxConns {
		h.trajConns.Add(-1)
		writeError(w, http.StatusTooManyRequests, "tecto de streams concorrentes atingido")
		return
	}
	defer h.trajConns.Add(-1)

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Sem Flusher não há streaming real; fail-closed em vez de bufferizar silenciosamente.
		writeError(w, http.StatusInternalServerError, "streaming nao suportado")
		return
	}
	_ = flusher

	// RESUME-FROM-SEQ: Last-Event-ID = último seq visto ⇒ lê a partir de lastSeq+1 (Read é
	// inclusivo). Ausente/inválido ⇒ 0 (backfill desde o início).
	lastSeq := parseLastEventID(r.Header.Get("Last-Event-ID"))
	readFrom := lastSeq + 1
	if readFrom == 0 { // overflow (lastSeq == MaxUint64): nada a ler
		readFrom = lastSeq
	}

	ctx := r.Context()
	conn := &sseConn{
		notify:    make(chan struct{}, 1),
		rc:        http.NewResponseController(w),
		watermark: lastSeq, // ponto de partida do dedup (nada <= lastSeq deve ser (re)enviado)
	}

	// (2) TRANSPORTE FAIL-SAFE. Anula o write-deadline por-ligação que o http.Server de produção
	// (AOS-166) arma (WriteTimeout) — cortaria toda a ligação SSE ao fim desse tempo. A partir
	// daqui cada escrita gere o seu PRÓPRIO deadline curto (writeEvent), pelo que a ligação
	// sobrevive indefinidamente enquanto o cliente drena, mas um cliente preso é cortado por
	// escrita. Um ResponseWriter que não suporte deadlines devolve erro aqui — ignorado (não há
	// deadline a anular; o caso de teste com writer em memória não bloqueia).
	_ = conn.rc.SetWriteDeadline(time.Time{})

	// (3) SUBSCRIBE PRIMEIRO — sem-lacuna. Registada antes do backfill, a subscrição captura
	// todo o evento com seq acima do snapshot do backfill. O ctx do pedido governa o ciclo de
	// vida: cancelá-lo (cliente desliga) desregista a subscrição automaticamente. O Unsubscribe
	// explícito abaixo garante a limpeza também nas saídas normais e por slow.
	sub, err := h.node.EventStore.Subscribe(ctx, eventstore.Filter{Streams: []string{runID}}, conn.onEvent)
	if err != nil {
		writeError(w, streamSetupErrorStatus(err), "trajectoria indisponivel")
		return
	}
	defer sub.Unsubscribe()

	// (4) BACKFILL + POSSE. Stream inexistente: se o run NÃO for conhecido por esta réplica
	// (nem eventos retidos, nem em-curso/desfecho no serviço), devolve o MESMO 404 uniforme que
	// handleGet — não abre um stream vazio nem vira oráculo de existência (não-enumerável). Se o
	// run for conhecido mas ainda sem eventos, prossegue com backfill vazio (live-only). Qualquer
	// outro erro de leitura ⇒ status HTTP (headers ainda não enviados).
	events, rerr := h.node.EventStore.Read(ctx, runID, readFrom)
	if rerr != nil {
		if errors.Is(rerr, eventstore.ErrStreamNotFound) {
			if !h.runKnown(runID) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			events = nil // run conhecido, ainda sem eventos ⇒ live-only
		} else {
			writeError(w, streamSetupErrorStatus(rerr), "trajectoria indisponivel")
			return
		}
	}

	// (D6) SELO WORM de leitura sensível (AOS-172) como PRÉ-CONDIÇÃO da abertura do stream — a
	// leitura de trajectória deixa de ser SILENCIOSA. Feito DEPOIS de confirmada a posse (o run
	// existe/é conhecido) e ANTES de comprometer o SSE (headers ainda não enviados): se o WORM
	// NÃO selar, NEGA fail-closed com status HTTP (o selo é obrigação de conformidade, não
	// telemetria best-effort — contraste com o fail-open de AOS-173).
	if !h.sealSensitiveRead(w, r, reader, residency, runID, capReadTrajectory) {
		return
	}

	// (5) COMPROMISSO COM O SSE. A partir daqui já não há status HTTP possível: um erro de
	// escrita fecha a ligação (return ⇒ defer Unsubscribe).
	if !h.commitSSE(conn, w) {
		return
	}

	// BACKFILL emitido por seq crescente; o watermark segue o maior seq emitido.
	for i := range events {
		ev := events[i]
		if !h.writeEvent(conn, w, ev) {
			return
		}
		if ev.Seq > conn.watermark {
			conn.watermark = ev.Seq
		}
	}

	// LIVE. Drena a fila por-ligação. DEDUP: um evento com seq <= watermark já foi emitido no
	// backfill (overlap na fronteira) ⇒ salta-se. Sem-lacuna: a subscrição foi registada antes do
	// backfill, pelo que todo o seq > watermark passa por aqui.
	for {
		ev, ok := conn.pop()
		if ok {
			if ev.Seq <= conn.watermark {
				continue // DEDUP
			}
			if !h.writeEvent(conn, w, ev) {
				return
			}
			conn.watermark = ev.Seq
			continue
		}
		select {
		case <-ctx.Done():
			return // cliente desligou ⇒ limpa (defer Unsubscribe)
		case <-conn.notify:
			// acordado por um enqueue; volta ao topo e drena a fila
		}
	}
}

// commitSSE escreve os cabeçalhos + 200 e faz o primeiro flush, sob um write-deadline curto.
// Devolve false se a escrita falhar (o handler deve retornar ⇒ defer Unsubscribe).
func (h *apiHandler) commitSSE(conn *sseConn, w http.ResponseWriter) bool {
	_ = conn.rc.SetWriteDeadline(time.Now().Add(h.trajWriteTimeout()))
	setSSEHeaders(w)
	w.WriteHeader(http.StatusOK)
	if err := conn.rc.Flush(); err != nil {
		return false
	}
	_ = conn.rc.SetWriteDeadline(time.Time{}) // limpo entre escritas: um wait live ocioso não é limitado
	return true
}

// writeEvent serializa e escreve um evento SSE sob um write-deadline PRÓPRIO e curto, e faz
// flush. É o coração do drop-slow-consumer por PROGRESSO: se o cliente parou de ler, a escrita
// no socket bloqueia, o deadline dispara (os.ErrDeadlineExceeded) e a função devolve false — o
// handler retorna e a ligação cai. Um cliente que drena (mesmo devagar) completa cada escrita
// dentro do deadline e sobrevive. Devolve false em qualquer erro de escrita/flush.
func (h *apiHandler) writeEvent(conn *sseConn, w io.Writer, ev eventstore.Event) bool {
	_ = conn.rc.SetWriteDeadline(time.Now().Add(h.trajWriteTimeout()))
	if err := writeSSEEvent(w, ev); err != nil {
		return false
	}
	if err := conn.rc.Flush(); err != nil {
		return false
	}
	_ = conn.rc.SetWriteDeadline(time.Time{}) // limpo entre escritas: um wait live ocioso não é limitado
	return true
}

// runKnown indica se esta réplica hospeda ou reteve o run (mesmo critério de observabilidade de
// handleGet): um desfecho retido OU o run em-curso. É a POSSE mínima que autoriza abrir o
// stream quando o Event Store ainda não tem eventos do run — sem revelar a existência de runs
// alheios.
func (h *apiHandler) runKnown(runID string) bool {
	if _, done := h.svc.Outcome(runID); done {
		return true
	}
	for _, id := range h.svc.InProgress() {
		if id == runID {
			return true
		}
	}
	return false
}

// trajWriteTimeout devolve o write-deadline por-escrita do stream SSE, com fallback ao default
// se a configuração vier não-positiva.
func (h *apiHandler) trajWriteTimeout() time.Duration {
	if h.cfg.trajWriteTimeout > 0 {
		return h.cfg.trajWriteTimeout
	}
	return DefaultTrajectoryWriteTimeout
}

// parseLastEventID interpreta o header Last-Event-ID como um seq uint64. Vazio/malformado ⇒
// 0 (backfill desde o início) — fail-safe, nunca um erro que interrompa o stream.
func parseLastEventID(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// setSSEHeaders escreve os cabeçalhos do text/event-stream. no-cache/no-transform impedem
// intermediários de bufferizar o stream; nosniff mantém a política de content-type.
func setSSEHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache, no-transform")
	hd.Set("Connection", "keep-alive")
	hd.Set("X-Content-Type-Options", "nosniff")
}

// writeSSEEvent serializa um Event como uma mensagem SSE: `id: <seq>\ndata: <json>\n\n`. O
// id é o seq (habilita o RESUME-FROM-SEQ via Last-Event-ID no reconnect). O JSON é compactado
// para uma só linha — um payload (json.RawMessage) com espaço/newline não parte o
// enquadramento SSE (data termina no \n).
func writeSSEEvent(w io.Writer, ev eventstore.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("id: ")
	buf.WriteString(strconv.FormatUint(ev.Seq, 10))
	buf.WriteByte('\n')
	buf.WriteString("data: ")
	if cerr := json.Compact(&buf, data); cerr != nil {
		buf.Write(data) // defensivo: se por algum motivo não compactar, emite tal-qual
	}
	buf.WriteString("\n\n")
	_, werr := w.Write(buf.Bytes())
	return werr
}

// streamSetupErrorStatus mapeia os erros de setup do stream (Subscribe/Read, ANTES de
// qualquer header SSE) a um status HTTP. Store fechado ou sem quórum ⇒ 503 (indisponível,
// possivelmente transitório); o resto ⇒ 500. A mensagem no corpo é uniforme (não-enumerável).
func streamSetupErrorStatus(err error) int {
	switch {
	case errors.Is(err, eventstore.ErrClosed), errors.Is(err, eventstore.ErrNoQuorum):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
