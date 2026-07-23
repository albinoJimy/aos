package main

// AOS-173 (EPIC-15, E7) — o ADAPTER DE DEPLOYMENT OTLP/HTTP que a camada de
// serialização do substrato (otel-genai) declarava DIFERIDO (ver o doc.go de
// otel-genai: "o exportador OTLP-gRPC/HTTP REAL é um adapter de deployment
// DIFERIDO"). Este ficheiro fecha esse gap SEM puxar dependências externas: usa
// apenas net/http + a serialização zero-dep [otelgenai.MarshalOTLP], que já produz
// o documento OTLP/JSON (ResourceSpans→ScopeSpans→Span, ids em HEX). Fica na camada
// de DEPLOYMENT (cmd/aos), mantendo o substrato puro em serialização.
//
// # Invariante crítico: observabilidade NUNCA no caminho crítico (FAIL-OPEN)
//
// A exportação de telemetria não pode quebrar nem bloquear um run. Este exporter é
// ASSÍNCRONO: [OTLPHTTPExporter.Export] (chamado no End() de cada span, no caminho
// do run) apenas ENFILEIRA — nunca faz I/O de rede nem bloqueia. Uma goroutine de
// flush dedicada drena a fila, agrupa em batches e faz o POST com timeout curto e
// retry LIMITADO com backoff. QUALQUER falha de export (endpoint em baixo, timeout,
// 5xx, fila cheia) é CONTABILIZADA e logada — NUNCA propagada ao run. Export devolve
// sempre nil.
//
// # Sem segredos
//
// O corpo OTLP é exactamente o que [otelgenai.MarshalOTLP] serializa a partir das
// SpanData: ids/metadados/hashes, nunca payloads. A invariante semconv (spans só
// transportam ids/metadados) é preservada a montante; este exporter não acrescenta
// nada ao corpo.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ErrBadOTLPEndpoint — o endpoint OTLP configurado não é um URL http(s) absoluto
// bem-formado. Fail-closed de config: um endpoint malformado aborta o arranque (o
// nó não sobe a fingir que exporta telemetria para um destino inválido).
var ErrBadOTLPEndpoint = errors.New("aos: AOS_OTLP_ENDPOINT invalido (esperado URL http(s) absoluto, ex.: http://collector:4318)")

// Defaults do exporter (conservadores: fail-open, caminho crítico intocado).
const (
	defaultOTLPQueueSize     = 2048
	defaultOTLPBatchSize     = 256
	defaultOTLPFlushInterval = 200 * time.Millisecond
	defaultOTLPTimeout       = 2 * time.Second
	defaultOTLPMaxRetries    = 2
	defaultOTLPBackoff       = 50 * time.Millisecond
	defaultOTLPDrainTimeout  = 5 * time.Second
	// otlpTracesPath é o caminho canónico OTLP/HTTP de traces (spec OTLP/HTTP).
	otlpTracesPath = "/v1/traces"
)

// OTLPStats são os contadores (atómicos) do exporter — a prova de que as falhas são
// contabilizadas, não propagadas. Enqueued conta os spans aceites na fila; Dropped os
// largados por fila cheia / exporter já fechado / residuais drenados no shutdown
// (fail-open); Exported os que um POST 2xx confirmou; Failed os que falharam
// definitivamente após esgotar os retries.
type OTLPStats struct {
	Enqueued int64
	Dropped  int64
	Exported int64
	Failed   int64
	Batches  int64
}

// OTLPHTTPExporter implementa [otelgenai.Exporter] fazendo POST de OTLP/JSON a um
// colector OTLP/HTTP. É assíncrono e fail-open (ver o comentário de topo).
type OTLPHTTPExporter struct {
	endpoint     string // URL completo até /v1/traces
	scope        string
	client       *http.Client
	logf         func(string, ...any)
	maxBatch     int
	maxRetries   int
	backoff      time.Duration
	flushEvery   time.Duration
	drainTimeout time.Duration

	queue chan otelgenai.SpanData
	done  chan struct{}
	wg    sync.WaitGroup

	closed    atomic.Bool
	closeOnce sync.Once
	// waitCh é fechado pela ÚNICA goroutine de espera criada dentro do closeOnce quando a
	// goroutine de flush termina. Criá-lo/observá-lo dentro do closeOnce torna Close
	// idempotente sem lançar goroutines adicionais a cada invocação.
	waitCh chan struct{}

	enqueued atomic.Int64
	dropped  atomic.Int64
	exported atomic.Int64
	failed   atomic.Int64
	batches  atomic.Int64
}

// OTLPOption configura o [OTLPHTTPExporter].
type OTLPOption func(*OTLPHTTPExporter)

// WithOTLPHTTPClient injecta o http.Client (default: timeout curto). Testes usam-no
// para apontar a um httptest.Server ou impor timeouts próprios.
func WithOTLPHTTPClient(c *http.Client) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if c != nil {
			e.client = c
		}
	}
}

// WithOTLPLogger injecta o logger de falhas de export (default: no-op). As falhas
// são SEMPRE contabilizadas nos stats; o log é observabilidade adicional.
func WithOTLPLogger(logf func(string, ...any)) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if logf != nil {
			e.logf = logf
		}
	}
}

// WithOTLPQueueSize define a capacidade da fila (cap de spans em voo). Fila cheia ⇒
// drop contabilizado (fail-open), nunca bloqueio do run.
func WithOTLPQueueSize(n int) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if n > 0 {
			e.queue = make(chan otelgenai.SpanData, n)
		}
	}
}

// WithOTLPBatchSize define o cap de tamanho de batch por POST.
func WithOTLPBatchSize(n int) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if n > 0 {
			e.maxBatch = n
		}
	}
}

// WithOTLPFlushInterval define o intervalo máximo entre flushes (batches parciais).
func WithOTLPFlushInterval(d time.Duration) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if d > 0 {
			e.flushEvery = d
		}
	}
}

// WithOTLPMaxRetries limita as RE-tentativas de um POST falhado (bounded, nunca
// infinito). 0 ⇒ uma só tentativa.
func WithOTLPMaxRetries(n int) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if n >= 0 {
			e.maxRetries = n
		}
	}
}

// WithOTLPBackoff define o passo de backoff (linear, bounded) entre re-tentativas.
func WithOTLPBackoff(d time.Duration) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if d >= 0 {
			e.backoff = d
		}
	}
}

// WithOTLPDrainTimeout limita quanto tempo [OTLPHTTPExporter.Close] espera pelo dreno
// final da fila antes de deixar o shutdown prosseguir (fail-open: a telemetria nunca
// prende o shutdown do nó). Permite ao chamador apertar/alargar o orçamento de dreno em
// vez do cap fixo [defaultOTLPDrainTimeout]. <=0 mantém o default.
func WithOTLPDrainTimeout(d time.Duration) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if d > 0 {
			e.drainTimeout = d
		}
	}
}

// WithOTLPScope sobrepõe o instrumentation scope emitido no OTLP (default:
// [otelgenai.ScopeName]).
func WithOTLPScope(scope string) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if scope != "" {
			e.scope = scope
		}
	}
}

// NewOTLPHTTPExporter constrói o exporter e arranca a goroutine de flush. endpoint é
// a BASE do colector (ex.: "http://collector:4318"); o caminho canónico
// [otlpTracesPath] é acrescentado se ainda não terminar nele. Fail-closed: um
// endpoint malformado devolve [ErrBadOTLPEndpoint] (nenhuma goroutine é arrancada).
func NewOTLPHTTPExporter(endpoint string, opts ...OTLPOption) (*OTLPHTTPExporter, error) {
	target, err := normalizeOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	e := &OTLPHTTPExporter{
		endpoint:     target,
		scope:        otelgenai.ScopeName,
		client:       &http.Client{Timeout: defaultOTLPTimeout},
		logf:         func(string, ...any) {},
		maxBatch:     defaultOTLPBatchSize,
		maxRetries:   defaultOTLPMaxRetries,
		backoff:      defaultOTLPBackoff,
		flushEvery:   defaultOTLPFlushInterval,
		drainTimeout: defaultOTLPDrainTimeout,
		queue:        make(chan otelgenai.SpanData, defaultOTLPQueueSize),
		done:         make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	e.wg.Add(1)
	go e.loop()
	return e, nil
}

// normalizeOTLPEndpoint valida (fail-closed) e completa o endpoint com /v1/traces.
func normalizeOTLPEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", ErrBadOTLPEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ErrBadOTLPEndpoint
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrBadOTLPEndpoint
	}
	trimmed := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(trimmed, otlpTracesPath) {
		return trimmed, nil
	}
	return trimmed + otlpTracesPath, nil
}

// Export implementa [otelgenai.Exporter]. É chamado no End() de cada span, NO CAMINHO
// DO RUN — por isso apenas ENFILEIRA (não-bloqueante) e devolve SEMPRE nil. Fila cheia
// ou exporter já fechado ⇒ drop CONTABILIZADO (fail-open): a telemetria degrada, o run
// NUNCA. Nunca faz I/O de rede aqui.
func (e *OTLPHTTPExporter) Export(spans []otelgenai.SpanData) error {
	if e.closed.Load() {
		e.dropped.Add(int64(len(spans)))
		return nil
	}
	for i := range spans {
		select {
		case e.queue <- spans[i]:
			e.enqueued.Add(1)
		case <-e.done:
			// Shutdown a decorrer: não se envia para uma fila que está a ser drenada.
			e.dropped.Add(1)
		default:
			// Fila cheia: largar é preferível a bloquear o run (fail-open).
			e.dropped.Add(1)
		}
	}
	return nil
}

// loop é a goroutine de flush: agrupa spans em batches (até maxBatch), com flush
// periódico (flushEvery) dos batches parciais, e drena a fila no shutdown. NUNCA
// propaga erros — só contabiliza/loga.
func (e *OTLPHTTPExporter) loop() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.flushEvery)
	defer ticker.Stop()
	batch := make([]otelgenai.SpanData, 0, e.maxBatch)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		e.send(batch)
		batch = batch[:0]
	}

	for {
		select {
		case s := <-e.queue:
			batch = append(batch, s)
			if len(batch) >= e.maxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-e.done:
			// Drena o que restar na fila (sem bloquear) e faz o flush final: shutdown
			// não perde spans já enfileirados nem deixa a goroutine pendurada.
			for {
				select {
				case s := <-e.queue:
					batch = append(batch, s)
					if len(batch) >= e.maxBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// send serializa o batch em OTLP/JSON e faz o POST com retry LIMITADO. Toda a falha é
// contabilizada/logada, nunca propagada.
func (e *OTLPHTTPExporter) send(spans []otelgenai.SpanData) {
	e.batches.Add(1)
	body, err := otelgenai.MarshalOTLP(spans, e.scope)
	if err != nil {
		// Serialização falhou (não devia, é só encoding/json): contabiliza e desiste.
		e.failed.Add(int64(len(spans)))
		e.logf("[aos] OTLP (AOS-173): serializacao falhou, %d span(s) descartado(s): %v", len(spans), err)
		return
	}
	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			// Backoff LINEAR e BOUNDED, interrompível pelo shutdown (não atrasa o drain).
			if !e.sleep(time.Duration(attempt) * e.backoff) {
				break
			}
		}
		if e.post(body) {
			e.exported.Add(int64(len(spans)))
			return
		}
	}
	// Falha DEFINITIVA após esgotar os retries: contabilizada e logada, NUNCA propagada.
	e.failed.Add(int64(len(spans)))
	e.logf("[aos] OTLP (AOS-173): export de %d span(s) falhou apos %d tentativa(s) (fail-open: run inalterado)", len(spans), e.maxRetries+1)
}

// sleep espera d respeitando o sinal de shutdown. Devolve false se o shutdown
// disparou durante a espera (o chamador aborta os retries e deixa o drain prosseguir).
func (e *OTLPHTTPExporter) sleep(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-e.done:
		return false
	}
}

// post faz UM POST do corpo OTLP/JSON. Devolve true só num 2xx. Qualquer erro de
// transporte, timeout ou status >= 300 (incl. 5xx) devolve false ⇒ é re-tentado/falha
// fail-open. Nunca entra em pânico nem propaga.
func (e *OTLPHTTPExporter) post(body []byte) bool {
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Drena o corpo para permitir reutilização da conexão keep-alive.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Close inicia o shutdown: marca o exporter fechado (novos Export passam a dropar
// fail-open), sinaliza a goroutine de flush a drenar e espera-a terminar, limitada por
// [OTLPHTTPExporter.drainTimeout] (default [defaultOTLPDrainTimeout], configurável por
// [WithOTLPDrainTimeout]). IDEMPOTENTE e sem goroutines redundantes: a marcação de fecho
// E a única goroutine de espera vivem dentro do closeOnce, pelo que chamadas repetidas
// (ex.: defer de teste + Close explícito) não fecham `done` duas vezes nem lançam mais
// goroutines. Não deixa a goroutine de flush pendurada.
func (e *OTLPHTTPExporter) Close() error {
	e.closeOnce.Do(func() {
		e.closed.Store(true)
		close(e.done)
		// UMA só goroutine de espera, criada aqui dentro (não a cada Close). O closeOnce
		// garante happens-before: e.waitCh está atribuído quando qualquer Do retorna, logo
		// o select abaixo pode lê-lo em segurança em qualquer invocação (mesmo concorrente).
		e.waitCh = make(chan struct{})
		go func() {
			e.wg.Wait()
			close(e.waitCh)
		}()
	})
	select {
	case <-e.waitCh:
		// A goroutine de flush retornou. Fecha a lacuna de reconciliação de stats: uma
		// corrida de shutdown pode ter enfileirado um span DEPOIS de o loop ter drenado a
		// fila e retornado (Export que leu closed==false antes do Store(true)); esse span
		// ficaria em Enqueued sem nunca aparecer em Exported/Dropped/Failed. Como a
		// goroutine de flush já terminou (wg concluído), NINGUÉM mais recebe da fila, pelo
		// que a drenamos aqui e contabilizamos como Dropped (fail-open, só telemetria).
		e.drainResidual()
	case <-time.After(e.drainTimeout):
		// O drain excedeu o orçamento: não bloqueamos o shutdown do nó por causa da
		// telemetria (fail-open). A goroutine terminará quando o POST em curso retornar.
		// Nesta via degradada Enqueued pode exceder Exported+Dropped+Failed (telemetria
		// degradada por design — a reconciliação exacta cede a um shutdown limitado).
		e.logf("[aos] OTLP (AOS-173): drain excedeu %s — shutdown prossegue (fail-open)", e.drainTimeout)
	}
	return nil
}

// drainResidual esvazia (não-bloqueante) quaisquer spans presos na fila após a goroutine
// de flush ter retornado, contabilizando-os como Dropped. PRÉ-CONDIÇÃO: só é chamado
// depois de e.waitCh fechar (wg concluído ⇒ a goroutine de flush terminou e nenhum outro
// receptor concorre pela fila), pelo que os receives são seguros e nenhum span destinado
// à exportação é indevidamente descartado (a fila só contém enfileiramentos pós-dreno).
func (e *OTLPHTTPExporter) drainResidual() {
	for {
		select {
		case <-e.queue:
			e.dropped.Add(1)
		default:
			return
		}
	}
}

// Stats devolve um snapshot atómico dos contadores (para testes/observabilidade do
// próprio exporter).
func (e *OTLPHTTPExporter) Stats() OTLPStats {
	return OTLPStats{
		Enqueued: e.enqueued.Load(),
		Dropped:  e.dropped.Load(),
		Exported: e.exported.Load(),
		Failed:   e.failed.Load(),
		Batches:  e.batches.Load(),
	}
}

// compile-time: o exporter satisfaz a porta do substrato.
var _ otelgenai.Exporter = (*OTLPHTTPExporter)(nil)
