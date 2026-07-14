package compression

import (
	"context"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/working"
)

// DefaultMaxCheckpoints é o tecto default da fila de checkpoints pendentes. Ao
// atingi-lo, [CheckpointTrigger.Observe] devolve [ErrQueueFull] (backpressure
// fail-closed) — a fila NUNCA cresce sem limite. Ajustável por [WithMaxCheckpoints];
// um valor <= 0 desliga o tecto (fila ilimitada, opt-out explícito).
const DefaultMaxCheckpoints = 1024

// Nomes/atributos de span do trigger (namespace próprio aos.memory.compression.*).
const (
	spanEnqueue          = "memory.compression.enqueue"
	attrTriggerAction    = "aos.memory.compression.trigger_action"
	attrTriggerTriggered = "aos.memory.compression.triggered"
	attrPendingDepth     = "aos.memory.compression.pending"
)

// CacheThrashAlert é o ALERTA emitido quando a compactação de um checkpoint detecta
// que o prefixo imutável mudou (cache thrash): a compressão nunca muta o prefixo, pelo
// que um hash divergente significa invalidação externa da cache — a poupança de
// prefix caching cai.
type CacheThrashAlert struct {
	RunID            string
	CheckpointID     string
	PrefixHashBefore string
	PrefixHashAfter  string
}

// AlertSink recebe os alertas de cache thrash. O encaminhamento real (paging,
// dashboards) é infra de observabilidade (EPIC-08); aqui só se define a porta.
type AlertSink interface {
	CacheThrash(ctx context.Context, alert CacheThrashAlert)
}

// checkpointRequest é um pedido de compactação enfileirado na hot path (só dados,
// sem trabalho). Guarda a origem e o snapshot do hash do prefixo no enfileiramento.
type checkpointRequest struct {
	src    CompactionSource
	action working.ExhaustionAction
}

// CheckpointTrigger liga o SINAL DE EXAUSTÃO GRACIOSA a ~80% da memória de trabalho
// (AOS-037) à compactação assíncrona (AOS-043), impondo a fronteira hot path vs.
// checkpoint:
//
//   - [CheckpointTrigger.Observe] corre NA HOT PATH (a cada turno, após o Signal do
//     WindowManager). Só ENFILEIRA um pedido de compactação quando o sinal está
//     Triggered (~80%) — O(1), SEM tocar no compactor/Event Store/projecção/registo.
//     NUNCA comprime no turno (asserção de caminho: Compactor().Invocations() == 0).
//   - [CheckpointTrigger.RunCheckpoint] corre FORA do turno (num checkpoint). Drena a
//     fila e invoca o compactor por pedido — é aqui, e só aqui, que a compactação
//     (activity durável, ADR-001) executa.
//
// Accionada pelo SINAL, nunca por hard-stop: Observe respeita a exaustão graciosa —
// o WindowManager continua a aceitar tail; a compactação é diferida para o checkpoint.
type CheckpointTrigger struct {
	compactor *AsyncCompactor
	policy    CompressionPolicy
	tracer    agentruntime.Tracer
	alerts    AlertSink

	// prefixHash lê o hash CORRENTE do prefixo imutável (tipicamente
	// WindowManager.PrefixHash) de um trigger SINGLE-RUN. É a âncora de detecção de
	// cache thrash na compactação. Nil desliga a re-leitura (a compactação assume o
	// prefixo da origem, invariante). ATENÇÃO: este leitor é GLOBAL (não sabe a que run
	// pertence), pelo que só é seguro aplicá-lo quando o lote drenado é de um ÚNICO run
	// — aplicá-lo ao checkpoint de OUTRO run compararia o prefixo VIVO de um run com o
	// snapshot de outro (ErrPrefixMutated + alerta de thrash ESPÚRIOS). Para triggers
	// multi-run, injecte um leitor POR RUN via [WithPrefixHashForRun].
	prefixHash func() string

	// prefixHashByRun lê o hash CORRENTE do prefixo POR run (run_id -> leitor). É a
	// forma correcta para um trigger que serve múltiplos runs: cada checkpoint compara
	// sempre contra o prefixo do SEU run, nunca contra o de outro. Tem precedência
	// sobre o leitor global [prefixHash].
	prefixHashByRun map[string]func() string

	maxQueue int

	mu    sync.Mutex
	queue []checkpointRequest
}

// TriggerOption configura o CheckpointTrigger.
type TriggerOption func(*CheckpointTrigger)

// WithTriggerTracer injecta a porta Tracer (default NoopTracer, zero-dep).
func WithTriggerTracer(t agentruntime.Tracer) TriggerOption {
	return func(tr *CheckpointTrigger) {
		if t != nil {
			tr.tracer = t
		}
	}
}

// WithPrefixHash injecta o leitor do hash CORRENTE do prefixo (ex.:
// windowManager.PrefixHash). Habilita a detecção de cache thrash na compactação: se o
// prefixo tiver mudado entre o enfileiramento e o checkpoint, a compactação aborta e
// emite alerta.
func WithPrefixHash(f func() string) TriggerOption {
	return func(tr *CheckpointTrigger) {
		if f != nil {
			tr.prefixHash = f
		}
	}
}

// WithPrefixHashForRun injecta o leitor do hash CORRENTE do prefixo de um run
// ESPECÍFICO (run_id -> ex.: windowManager.PrefixHash desse run). É a forma correcta
// de habilitar a detecção de cache thrash num trigger que serve MÚLTIPLOS runs: cada
// checkpoint é comparado sempre contra o prefixo do seu próprio run, eliminando os
// abortos ErrPrefixMutated e alertas de thrash espúrios que um leitor global geraria
// ao cruzar o prefixo vivo de um run com o snapshot de outro. Tem precedência sobre
// [WithPrefixHash] para os runs registados. Pode ser chamado várias vezes (um por run).
func WithPrefixHashForRun(runID string, f func() string) TriggerOption {
	return func(tr *CheckpointTrigger) {
		if runID == "" || f == nil {
			return
		}
		if tr.prefixHashByRun == nil {
			tr.prefixHashByRun = make(map[string]func() string)
		}
		tr.prefixHashByRun[runID] = f
	}
}

// WithAlertSink injecta o destino dos alertas de cache thrash (default: nenhum).
func WithAlertSink(s AlertSink) TriggerOption {
	return func(tr *CheckpointTrigger) {
		if s != nil {
			tr.alerts = s
		}
	}
}

// WithMaxCheckpoints sobrepõe o tecto da fila de checkpoints (default
// [DefaultMaxCheckpoints]). Um valor <= 0 desliga o tecto (fila ilimitada).
func WithMaxCheckpoints(n int) TriggerOption {
	return func(tr *CheckpointTrigger) {
		tr.maxQueue = n
	}
}

// NewCheckpointTrigger constrói o trigger sobre um compactor e uma política de
// compressão versionada. O compactor é obrigatório (fail-closed); uma política zero
// usa a default. Uma política inválida é rejeitada na construção (nunca há default
// silencioso a meio de um checkpoint).
func NewCheckpointTrigger(compactor *AsyncCompactor, policy CompressionPolicy, opts ...TriggerOption) (*CheckpointTrigger, error) {
	if compactor == nil {
		return nil, ErrNilCompactor
	}
	policy = policy.normalized()
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	tr := &CheckpointTrigger{
		compactor: compactor,
		policy:    policy,
		tracer:    agentruntime.NoopTracer{},
		maxQueue:  DefaultMaxCheckpoints,
	}
	for _, o := range opts {
		o(tr)
	}
	return tr, nil
}

// Compactor devolve o compactor subjacente (para asserção de caminho: Invocations()).
func (t *CheckpointTrigger) Compactor() *AsyncCompactor { return t.compactor }

// Policy devolve a política de compressão versionada em uso.
func (t *CheckpointTrigger) Policy() CompressionPolicy { return t.policy }

// PendingCount devolve o nº de checkpoints por drenar (profundidade da fila).
func (t *CheckpointTrigger) PendingCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queue)
}

// Observe é chamado NA HOT PATH após o Signal do WindowManager. Se o sinal estiver
// Triggered (~80%, ActionMarkForCompression ou ActionEscalate), ENFILEIRA um pedido
// de compactação com a origem dada — O(1), sem correr o compactor. Devolve true se
// enfileirou. É a exaustão GRACIOSA: não bloqueia nem comprime no turno.
//
// Fail-closed: uma origem mal-formada é rejeitada aqui (nunca entra na fila); a fila
// é LIMITADA (backpressure via ErrQueueFull). Sinal não-Triggered é no-op (false, nil).
func (t *CheckpointTrigger) Observe(ctx context.Context, sig working.Exhaustion, src CompactionSource) (bool, error) {
	_, span := t.tracer.StartSpan(ctx, spanEnqueue)
	defer span.End()
	span.SetAttribute(attrTriggerTriggered, sig.Triggered)
	span.SetAttribute(attrTriggerAction, string(sig.Action))

	if !sig.Triggered || sig.Action == working.ActionNone {
		return false, nil
	}
	if err := src.validate(); err != nil {
		return false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.maxQueue > 0 && len(t.queue) >= t.maxQueue {
		return false, ErrQueueFull
	}
	t.queue = append(t.queue, checkpointRequest{src: src, action: sig.Action})
	span.SetAttribute(attrPendingDepth, len(t.queue))
	return true, nil
}

// RunCheckpoint DRENA a fila e executa a compactação FORA do turno (num checkpoint).
// Para cada pedido invoca o compactor (activity durável, ADR-001): emite o registo
// completo, projecta o sumário e persiste-o idempotentemente. Devolve os resultados
// (ordem de enfileiramento). É AQUI que a compactação corre — nunca em Observe.
//
// Cache thrash: se um leitor de prefixo estiver injectado ([WithPrefixHash]) e o
// prefixo tiver mudado desde o enfileiramento, a compactação desse checkpoint aborta
// fail-closed, emite alerta ([AlertSink]) e o resultado marca CacheThrash — os
// restantes checkpoints continuam. Fail-closed noutros erros: pára e recoloca os não
// processados no início da fila (nada é perdido; a próxima RunCheckpoint retoma).
func (t *CheckpointTrigger) RunCheckpoint(ctx context.Context) ([]CompactionResult, error) {
	t.mu.Lock()
	batch := t.queue
	t.queue = nil
	t.mu.Unlock()

	// O leitor global [prefixHash] não sabe a que run pertence; só é seguro aplicá-lo
	// quando o lote é de um ÚNICO run (senão compararia o prefixo vivo desse run com o
	// snapshot de outro -> thrash espúrio). Determina uma vez se o lote é multi-run.
	batchMultiRun := isMultiRun(batch)

	results := make([]CompactionResult, 0, len(batch))
	for i, req := range batch {
		nowHash := t.currentPrefixHash(req.src, batchMultiRun)
		res, err := t.compactor.Compact(ctx, req.src, t.policy, nowHash)
		if err != nil {
			if res.CacheThrash {
				// Cache thrash: alerta e prossegue (o checkpoint deste run está
				// comprometido, mas os outros não). NÃO recoloca — repetir daria o mesmo
				// thrash enquanto o prefixo estiver mutado.
				if t.alerts != nil {
					t.alerts.CacheThrash(ctx, CacheThrashAlert{
						RunID:            res.RunID,
						CheckpointID:     res.CheckpointID,
						PrefixHashBefore: res.PrefixHashBefore,
						PrefixHashAfter:  res.PrefixHashAfter,
					})
				}
				results = append(results, res)
				continue
			}
			// Outro erro (I/O, etc.): pára e recoloca os não processados (deste inclusive).
			t.mu.Lock()
			t.queue = append(append([]checkpointRequest(nil), batch[i:]...), t.queue...)
			t.mu.Unlock()
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// currentPrefixHash resolve o hash CORRENTE do prefixo a comparar com o snapshot da
// origem, SEMPRE ligado ao run do pedido:
//
//   - leitor POR RUN ([WithPrefixHashForRun]) para src.RunID -> usa-o (correcto para
//     triggers multi-run);
//   - senão, o leitor GLOBAL ([WithPrefixHash]) SÓ quando o lote é de um único run
//     (aplicá-lo em multi-run cruzaria runs -> thrash espúrio);
//   - caso contrário, o snapshot da própria origem (assume invariante — o mesmo
//     comportamento de quando não há leitor: sem re-leitura, sem falso positivo).
func (t *CheckpointTrigger) currentPrefixHash(src CompactionSource, batchMultiRun bool) string {
	if t.prefixHashByRun != nil {
		if f, ok := t.prefixHashByRun[src.RunID]; ok {
			return f()
		}
	}
	if t.prefixHash != nil && !batchMultiRun {
		return t.prefixHash()
	}
	return src.PrefixHash
}

// isMultiRun indica se o lote drenado contém pedidos de mais do que um run distinto.
func isMultiRun(batch []checkpointRequest) bool {
	var first string
	for i, req := range batch {
		if i == 0 {
			first = req.src.RunID
			continue
		}
		if req.src.RunID != first {
			return true
		}
	}
	return false
}
