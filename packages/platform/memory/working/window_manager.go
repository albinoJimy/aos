package working

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
)

// Erros sentinela da gestão da janela (fail-closed, comparáveis com errors.Is).
var (
	// ErrNilAssembler — construção sem system prompt/assembler utilizável.
	ErrNilAssembler = errors.New("working: assembler nulo")
	// ErrInvalidLimit — limite de tokens do modelo não-positivo.
	ErrInvalidLimit = errors.New("working: limite de tokens do modelo tem de ser > 0")
	// ErrInvalidRatio — rácio de exaustão fora de (0,1].
	ErrInvalidRatio = errors.New("working: rácio de exaustão tem de estar em (0,1]")
	// ErrMissingRunID — run_id em falta (raiz de idempotência/correlação).
	ErrMissingRunID = errors.New("working: run_id obrigatório")
	// ErrPrefixPinned — tentativa de introduzir uma tool/entrada que QUEBRARIA o
	// prefixo congelado no run corrente. O prefixo é imutável: a nova tool só entra
	// num RUN NOVO (usar NewRunWith). Fail-closed para a estabilidade do prefixo.
	ErrPrefixPinned = errors.New("working: prefixo pinado — nova tool só entra em run novo")
	// ErrNoEvictionSink — eviction pedida sem sink de preservação configurado. A
	// eviction NUNCA pode apagar o registo (Princípio 4, AOS-036): sem backend onde
	// preservar o que sai da vista, a eviction é recusada.
	ErrNoEvictionSink = errors.New("working: eviction sem sink de preservação (registo seria perdido)")
	// ErrEvictionRecordConflict — a preservação colidiu com um registo EXISTENTE de
	// conteúdo DIVERGENTE na mesma chave de idempotência f(RunID, Class, ID). O
	// backend é idempotente: um RecordID repetido devolve o registo já persistido sem
	// sobrescrever, pelo que o segmento distinto NÃO ficaria de facto no registo. A
	// eviction ABORTA fail-closed antes de o remover da vista (Princípio 4, AOS-036):
	// o que sai da vista tem SEMPRE de permanecer, byte-a-byte, no registo.
	ErrEvictionRecordConflict = errors.New("working: eviction abortada — registo existente diverge do segmento a preservar")
)

// Atributos de span no namespace próprio aos.working.* (a gestão da janela NÃO é
// inferência GenAI). A OCUPAÇÃO da janela é, porém, exposta na chave canónica
// gen_ai.usage.input_tokens (é literalmente o prompt que o modelo vai ver), para
// que o backend OTel (EPIC-08) a mapeie sem renomear e o burn-down (ADR-008) a leia.
//
// ALERTA de aproximação do limite (critério de aceitação 2): este pacote EXPÕE o
// substrato — a métrica de ocupação (gen_ai.usage.input_tokens vs attrLimitTokens),
// o predicado Occupancy.Exhausted() e o sinal ~80% (attrExhausted). A DEFINIÇÃO da
// regra de alerta em si (limiar, janela, encaminhamento) é infra de observabilidade
// (EPIC-08) e NÃO é definida em código aqui.
const (
	spanWindowTurn        = "working.window.turn"
	spanWindowEvict       = "working.window.evict"
	attrPrefixTokens      = "aos.working.prefix_tokens"
	attrTailTokens        = "aos.working.tail_tokens"
	attrLimitTokens       = "aos.working.limit_tokens"
	attrThresholdTokens   = "aos.working.threshold_tokens"
	attrExhausted         = "aos.working.exhausted"
	attrMarkedCompression = "aos.working.marked_for_compression"
	attrCacheHitRate      = "aos.working.cache_hit_rate"
	attrTurnIndex         = "aos.working.turn_index"
	attrEvictedSegments   = "aos.working.evicted_segments"
	attrEvictionPreserved = "aos.working.eviction_preserved"
)

// ToolSpec é a identidade PINADA de uma tool/skill congelada no run. É um alias do
// tipo canónico do Agent Runtime (ADR-009): reutilizamos o layout cache-estável em
// vez de o reimplementar. A ordem no tool set é significativa e NÃO é reordenada —
// reordenar quebraria a estabilidade byte-a-byte do prefixo.
type ToolSpec = agentruntime.ToolSpec

// TailKind classifica um segmento do tail append-only. Alias do tipo canónico do
// Agent Runtime.
type TailKind = agentruntime.TailKind

// Reexporta as classes de tail para o chamador da memória de trabalho.
const (
	TailMemory     = agentruntime.TailMemory
	TailTimestamp  = agentruntime.TailTimestamp
	TailObjective  = agentruntime.TailObjective
	TailToolResult = agentruntime.TailToolResult
	TailHistory    = agentruntime.TailHistory
)

// DefaultExhaustionRatio é o limiar default de exaustão graciosa (~80%), coerente
// com o burn-down de custo (ADR-008) e o prompt de exaustão graciosa da Dim. 6. Ao
// atingi-lo, a janela EMITE SINAL (marca para compressão/escala), nunca hard-stop.
const DefaultExhaustionRatio = 0.80

// ExhaustionAction é a acção recomendada pelo sinal de exaustão graciosa. NUNCA é
// um hard-stop: é sempre uma preparação (compressão em checkpoint — AOS-043) ou uma
// escalada ao runtime.
type ExhaustionAction string

const (
	// ActionNone — abaixo do limiar; nada a fazer.
	ActionNone ExhaustionAction = "none"
	// ActionMarkForCompression — ocupação ≥ limiar (~80%): MARCAR para compressão
	// assíncrona em checkpoint (AOS-043). A compressão em si NÃO acontece aqui.
	ActionMarkForCompression ExhaustionAction = "mark_for_compression"
	// ActionEscalate — ocupação ≥ limite do modelo: escalar ao runtime (o tail já
	// não cabe). Continua a NÃO ser um hard-stop cego: a janela aceita o tail e
	// sinaliza para o runtime decidir (comprimir/ramificar/escalar).
	ActionEscalate ExhaustionAction = "escalate"
)

// Exhaustion é o SINAL de exaustão graciosa. Triggered=true a partir de ~80%. É um
// valor de DADOS entregue ao runtime — a memória de trabalho não decide sozinha
// comprimir nem aborta: expõe a ocupação e a acção recomendada.
type Exhaustion struct {
	Triggered bool
	Action    ExhaustionAction
	Occupancy Occupancy
}

// Config parametriza o WindowManager. O estimador e o relógio são injectáveis
// (determinismo). Rácio e limite têm validação fail-closed.
type Config struct {
	// RunID é o run que congela o prefixo (obrigatório).
	RunID string
	// System é o system prompt (parte imutável do prefixo).
	System string
	// Tools é o tool set CONGELADO no run (ordem significativa).
	Tools []ToolSpec
	// ModelTokenLimit é o limite de tokens do modelo para a janela (> 0).
	ModelTokenLimit int
	// ExhaustionRatio é o limiar de exaustão graciosa em (0,1]; 0 usa o default (~0.80).
	ExhaustionRatio float64
	// Estimator é o estimador de tokens (nil usa DefaultTokenEstimator).
	Estimator TokenEstimator
	// Tracer é a porta OTel (nil usa NoopTracer, zero-dep).
	Tracer agentruntime.Tracer
	// Sink preserva os segmentos evictados no backend (nil = eviction recusada).
	Sink EvictionSink
}

// tailEntry é uma unidade do tail append-only com a sua contabilidade e prioridade
// de eviction. seq dá ordem estável (FIFO) dentro da mesma prioridade.
type tailEntry struct {
	seg      agentruntime.TailSegment
	tokens   int
	priority int
	turn     int
	seq      uint64
	recordID string
}

// WindowManager gere a janela de contexto de UM run: prefixo imutável (congelado
// na construção) + tail append-only. Não é seguro para uso concorrente por vários
// escritores do MESMO run (a hot path do loop é sequencial por run); é imutável no
// prefixo por construção — não existe qualquer método que o altere.
type WindowManager struct {
	runID        string
	asm          *agentruntime.PromptAssembler
	prefixHash   string
	prefixTokens int
	limit        int
	threshold    int
	ratio        float64 // rácio de exaustão ORIGINAL (não o threshold truncado) — herdado tal-e-qual por NewRunWith
	estimator    TokenEstimator
	tracer       agentruntime.Tracer
	sink         EvictionSink

	frozenTools map[string]ToolSpec
	toolOrder   []ToolSpec // ordem congelada (para derivar runs novos)
	system      string

	tail       []tailEntry
	tailTokens int
	seq        uint64
	turn       int

	marked bool // marcado para compressão (AOS-043) — não comprime aqui
	sli    cacheSLI
}

// NewWindowManager congela o prefixo do run a partir do system prompt e do tool set
// e devolve o gestor da janela. Valida a config fail-closed. O prefixo é calculado
// UMA vez (via agentruntime.PromptAssembler) e o seu hash é a âncora de
// imutabilidade provada por teste.
func NewWindowManager(cfg Config) (*WindowManager, error) {
	if cfg.RunID == "" {
		return nil, ErrMissingRunID
	}
	if cfg.ModelTokenLimit <= 0 {
		return nil, ErrInvalidLimit
	}
	ratio := cfg.ExhaustionRatio
	if ratio == 0 {
		ratio = DefaultExhaustionRatio
	}
	if ratio <= 0 || ratio > 1 {
		return nil, ErrInvalidRatio
	}
	est := cfg.Estimator
	if est == nil {
		est = DefaultTokenEstimator
	}
	tracer := cfg.Tracer
	if tracer == nil {
		tracer = agentruntime.NoopTracer{}
	}

	asm := agentruntime.NewPromptAssembler(cfg.System, cfg.Tools)
	if asm == nil {
		return nil, ErrNilAssembler
	}

	frozen := make(map[string]ToolSpec, len(cfg.Tools))
	order := make([]ToolSpec, len(cfg.Tools))
	for i, t := range cfg.Tools {
		frozen[t.Name] = t
		order[i] = t
	}

	// Ocupação do prefixo em tokens: função pura dos bytes congelados do prefixo.
	prefixTokens := est(string(asm.Prefix()))
	threshold := int(float64(cfg.ModelTokenLimit) * ratio)

	return &WindowManager{
		runID:        cfg.RunID,
		asm:          asm,
		prefixHash:   asm.PrefixHash(),
		prefixTokens: prefixTokens,
		limit:        cfg.ModelTokenLimit,
		threshold:    threshold,
		ratio:        ratio,
		estimator:    est,
		tracer:       tracer,
		sink:         cfg.Sink,
		frozenTools:  frozen,
		toolOrder:    order,
		system:       cfg.System,
	}, nil
}

// RunID devolve o run que congelou o prefixo.
func (w *WindowManager) RunID() string { return w.runID }

// PrefixHash devolve sha256(prefixo) no formato "sha256:<hex>". É BYTE-IDÊNTICO
// entre turnos do mesmo run — o SLI de estabilidade do prefixo. Se mudar dentro de
// um run, houve regressão de cache (nunca deve acontecer: não há mutador).
func (w *WindowManager) PrefixHash() string { return w.prefixHash }

// Prefix devolve uma cópia do prefixo congelado (imutável). Existe para asserção de
// imutabilidade byte-a-byte em teste.
func (w *WindowManager) Prefix() []byte { return w.asm.Prefix() }

// SystemHash devolve sha256("<system>") no formato "sha256:<hex>" — o system_hash que
// o manifesto por trajectória grava (ADR-010). Delega no assembler congelado; existe
// para o adaptador da [agentruntime.WindowPort] (AOS-157) alimentar o manifesto do loop
// sem manter um segundo assembler.
func (w *WindowManager) SystemHash() string { return w.asm.SystemHash() }

// HasTool indica se a tool está no tool set CONGELADO do run.
func (w *WindowManager) HasTool(name string) bool {
	_, ok := w.frozenTools[name]
	return ok
}

// RequireTool impõe o PINNING do prefixo (ADR-009): se a tool não estiver no tool
// set congelado do run, é REJEITADA com ErrPrefixPinned — introduzi-la mutaria o
// prefixo, o que nunca é permitido no run corrente. A tool nova só entra num RUN
// NOVO (ver NewRunWith). Se já estiver congelada, é no-op sem erro.
func (w *WindowManager) RequireTool(spec ToolSpec) error {
	if existing, ok := w.frozenTools[spec.Name]; ok {
		// Mesma tool: só é a MESMA se a identidade pinada coincidir (uma versão/
		// digest diferente também quebraria o prefixo — trata-se como nova).
		if existing == spec {
			return nil
		}
	}
	return ErrPrefixPinned
}

// NewRunWith deriva um RUN NOVO cujo tool set congelado é o do run corrente MAIS as
// tools extra (na ordem: congeladas + extra). É o ÚNICO caminho pelo qual uma tool
// nova entra: um novo prefixo, um novo hash de prefixo — o prefixo do run corrente
// permanece intocado. Herda limite/rácio/estimador/tracer/sink deste gestor.
func (w *WindowManager) NewRunWith(runID string, extra ...ToolSpec) (*WindowManager, error) {
	tools := make([]ToolSpec, 0, len(w.toolOrder)+len(extra))
	tools = append(tools, w.toolOrder...)
	tools = append(tools, extra...)
	// Herda o rácio de exaustão ORIGINAL tal-e-qual (não reconstruído a partir do
	// threshold inteiro truncado): o round-trip pela verdade truncada perderia
	// precisão (±1 token) e, em limites minúsculos, colapsaria em 0 → sentinela de
	// default. O run derivado herda EXACTAMENTE a mesma política de exaustão.
	return NewWindowManager(Config{
		RunID:           runID,
		System:          w.system,
		Tools:           tools,
		ModelTokenLimit: w.limit,
		ExhaustionRatio: w.ratio,
		Estimator:       w.estimator,
		Tracer:          w.tracer,
		Sink:            w.sink,
	})
}

// TailInput descreve um segmento a acrescentar ao tail append-only.
type TailInput struct {
	// Kind classifica o segmento (memory/tool_result/history/...).
	Kind TailKind
	// Content é o conteúdo já serializado (bytes opacos).
	Content string
	// Priority governa a eviction: MAIOR prioridade é retida por mais tempo. A
	// eviction retira primeiro a MENOR prioridade e, dentro dela, a mais antiga.
	Priority int
	// RecordID é a identidade do fragmento no backend, para a eviction preservar o
	// registo (Princípio 4). Vazio = derivado de (run, turn, seq) na eviction.
	RecordID string
}

// Append acrescenta um segmento ao TAIL append-only e actualiza a contabilidade em
// tokens. NUNCA muta nem reordena o prefixo. NUNCA faz hard-stop por tamanho: se a
// ocupação cruzar o limiar, o SINAL de exaustão passa a Triggered (ver Signal) — o
// append em si é sempre aceite (exaustão graciosa, não cega). Devolve a ocupação
// resultante.
func (w *WindowManager) Append(in TailInput) Occupancy {
	tokens := w.estimator(in.Content)
	w.seq++
	w.tail = append(w.tail, tailEntry{
		seg:      agentruntime.TailSegment{Kind: in.Kind, Content: []byte(in.Content)},
		tokens:   tokens,
		priority: in.Priority,
		turn:     w.turn,
		seq:      w.seq,
		recordID: in.RecordID,
	})
	w.tailTokens += tokens
	if w.Occupancy().Exhausted() {
		w.marked = true
	}
	return w.Occupancy()
}

// Occupancy devolve a fotografia da ocupação da janela EM TOKENS.
func (w *WindowManager) Occupancy() Occupancy {
	return Occupancy{
		PrefixTokens: w.prefixTokens,
		TailTokens:   w.tailTokens,
		Limit:        w.limit,
		Threshold:    w.threshold,
	}
}

// Signal devolve o SINAL de exaustão graciosa corrente. Triggered=true a partir de
// ~80%; a acção escala de mark_for_compression (≥ limiar) para escalate (≥ limite).
// É sempre uma preparação/escala, NUNCA um hard-stop cego.
//
// CONSUMO PELO RUNTIME (integração diferida): este é o ponto de consumo previsto
// para o loop do Agent Runtime — obtém Signal()/Occupancy() e trata Action como
// marca-para-compressão (AOS-043) ou escala, nunca como hard-stop. O WIRING do loop
// (packages/kernel/agent-runtime) a este gestor é um ticket dedicado e fica FORA do
// âmbito de AOS-037 (que só EXPÕE o sinal como valor tipado, pronto a consumir).
func (w *WindowManager) Signal() Exhaustion {
	occ := w.Occupancy()
	switch {
	case occ.Total() >= occ.Limit:
		return Exhaustion{Triggered: true, Action: ActionEscalate, Occupancy: occ}
	case occ.Exhausted():
		return Exhaustion{Triggered: true, Action: ActionMarkForCompression, Occupancy: occ}
	default:
		return Exhaustion{Triggered: false, Action: ActionNone, Occupancy: occ}
	}
}

// MarkedForCompression indica se a janela já foi marcada para compressão em
// checkpoint (AOS-043). É PREPARAÇÃO: a compressão assíncrona em si é AOS-043.
func (w *WindowManager) MarkedForCompression() bool { return w.marked }

// CacheHitRate devolve o SLI de cache-hit-rate acumulado (em tokens) ao longo dos
// turnos materializados. Deriva da estabilidade do prefixo (ADR-009): num cenário
// de referência com prefixo estável fica acima do alvo (>80%) e não regride.
func (w *WindowManager) CacheHitRate() float64 { return w.sli.rate() }

// TurnResult é o produto de materializar um turno da janela.
type TurnResult struct {
	// View é o prompt materializado (prefixo congelado ++ tail serializado), com o
	// PrefixHash byte-idêntico entre turnos e o PromptHash do turno.
	View agentruntime.PromptView
	// Occupancy é a ocupação em tokens no momento da materialização.
	Occupancy Occupancy
	// Exhaustion é o sinal de exaustão graciosa no momento da materialização.
	Exhaustion Exhaustion
}

// Turn MATERIALIZA o turno corrente: monta o prompt cache-estável (prefixo
// imutável + tail append-only), regista o turno no SLI de cache-hit-rate, emite o
// span OTel (com gen_ai.usage.input_tokens = ocupação) e avança o índice de turno.
// NÃO muta o prefixo. Devolve a vista, a ocupação e o sinal de exaustão.
func (w *WindowManager) Turn(ctx context.Context) TurnResult {
	segs := make([]agentruntime.TailSegment, len(w.tail))
	for i, e := range w.tail {
		segs[i] = e.seg
	}
	view := w.asm.Assemble(w.turn, segs)

	occ := w.Occupancy()
	sig := w.Signal()

	// SLI: turno 1 estabelece a cache do prefixo (miss); 2..N são hits do prefixo.
	w.sli.observeTurn(w.prefixTokens, w.tailTokens)

	_, span := w.tracer.StartSpan(ctx, spanWindowTurn)
	span.SetAttribute(agentruntime.AttrRunID, w.runID)
	span.SetAttribute(agentruntime.AttrPrefixHash, view.PrefixHash)
	span.SetAttribute(agentruntime.AttrPromptHash, view.PromptHash)
	span.SetAttribute(agentruntime.AttrInputTokens, occ.Total())
	span.SetAttribute(attrTurnIndex, w.turn)
	span.SetAttribute(attrPrefixTokens, occ.PrefixTokens)
	span.SetAttribute(attrTailTokens, occ.TailTokens)
	span.SetAttribute(attrLimitTokens, occ.Limit)
	span.SetAttribute(attrThresholdTokens, occ.Threshold)
	span.SetAttribute(attrExhausted, occ.Exhausted())
	span.SetAttribute(attrMarkedCompression, w.marked)
	span.SetAttribute(attrCacheHitRate, w.sli.rate())
	span.End()

	w.turn++
	return TurnResult{View: view, Occupancy: occ, Exhaustion: sig}
}

// EvictedSegment é um segmento que SAIU da vista injectada. É o que se PRESERVA no
// backend (Princípio 4, AOS-036): a eviction é só da VISTA, nunca do registo.
type EvictedSegment struct {
	RecordID  string
	TurnIndex int
	Kind      string
	Content   string
	Tokens    int
}

// EvictionSink recebe cada EvictedSegment para que PERMANEÇA no backend antes de
// sair da vista. A eviction NUNCA apaga o registo: escreve-o primeiro, remove da
// vista depois (fail-closed).
type EvictionSink interface {
	Persist(ctx context.Context, ev EvictedSegment) error
}

// EvictToTailBudget faz eviction do TAIL até a sua ocupação em tokens ser <=
// tailBudget, PRESERVANDO cada segmento evictado no backend ANTES de o remover da
// vista (Princípio 4, AOS-036). O prefixo NUNCA é tocado. A ordem de eviction é
// por PRIORIDADE ascendente e, dentro da mesma prioridade, FIFO (mais antigo
// primeiro). Se o sink falhar a preservar, a eviction ABORTA (o registo nunca é
// perdido). Devolve os segmentos evictados.
//
// É uma POLÍTICA que PREPARA a compressão assíncrona (AOS-043) sem a executar: o
// que sai da janela fica marcado/preservado para um checkpoint posterior comprimir.
func (w *WindowManager) EvictToTailBudget(ctx context.Context, tailBudget int) ([]EvictedSegment, error) {
	if w.tailTokens <= tailBudget {
		return nil, nil
	}
	if w.sink == nil {
		return nil, ErrNoEvictionSink
	}

	// Índices ordenados por (prioridade asc, seq asc) — candidatos a sair primeiro.
	idx := make([]int, len(w.tail))
	for i := range w.tail {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ea, eb := w.tail[idx[a]], w.tail[idx[b]]
		if ea.priority != eb.priority {
			return ea.priority < eb.priority
		}
		return ea.seq < eb.seq
	})

	evictSet := make(map[int]bool)
	projected := w.tailTokens
	var evicted []EvictedSegment
	for _, i := range idx {
		if projected <= tailBudget {
			break
		}
		e := w.tail[i]
		rid := e.recordID
		if rid == "" {
			rid = w.defaultRecordID(e)
		}
		ev := EvictedSegment{
			RecordID:  rid,
			TurnIndex: e.turn,
			Kind:      string(e.seg.Kind),
			Content:   string(e.seg.Content),
			Tokens:    e.tokens,
		}
		// Preservar ANTES de remover da vista (fail-closed).
		if err := w.sink.Persist(ctx, ev); err != nil {
			return evicted, err
		}
		evictSet[i] = true
		projected -= e.tokens
		evicted = append(evicted, ev)
	}

	// Invariante: passado o guard inicial (w.tailTokens > tailBudget), a primeira
	// iteração do loop evicta sempre pelo menos um segmento, pelo que evictSet nunca
	// fica vazio aqui. Não há caminho de "nada a evictar" após este ponto.

	// Reconstrói o tail preservando a ORDEM cronológica dos remanescentes (o tail
	// continua append-only; a eviction só remove, nunca reordena).
	kept := w.tail[:0:0]
	newTokens := 0
	for i, e := range w.tail {
		if evictSet[i] {
			continue
		}
		kept = append(kept, e)
		newTokens += e.tokens
	}
	w.tail = kept
	w.tailTokens = newTokens

	// A eviction pode aliviar a pressão da janela abaixo do limiar de exaustão. O
	// latch marked-for-compression deriva da ocupação CORRENTE — se já não estamos
	// exaustos, limpa-se (senão MarkedForCompression() e o atributo de span ficariam
	// permanentemente verdadeiros, levando a compressões repetidas/desnecessárias em
	// AOS-043 e a telemetria de pressão falseada).
	w.marked = w.Occupancy().Exhausted()

	// Span de eviction: prova observável de que a eviction preservou o registo
	// (eviction_preserved=true) e nunca tocou o prefixo (prefix_hash inalterado).
	_, span := w.tracer.StartSpan(ctx, spanWindowEvict)
	span.SetAttribute(agentruntime.AttrRunID, w.runID)
	span.SetAttribute(agentruntime.AttrPrefixHash, w.prefixHash)
	span.SetAttribute(attrEvictedSegments, len(evicted))
	span.SetAttribute(attrEvictionPreserved, true)
	span.SetAttribute(attrTailTokens, w.tailTokens)
	span.End()

	return evicted, nil
}

// defaultRecordID deriva uma identidade estável e determinística para um segmento
// sem RecordID explícito, a partir de (run, turn, seq). Sem aleatoriedade.
func (w *WindowManager) defaultRecordID(e tailEntry) string {
	return w.runID + ":" + itoa(e.turn) + ":" + itoa(int(e.seq))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// MemoryPortSink é um EvictionSink que preserva os segmentos evictados como
// registos ClassWorking na MemoryPort (AOS-035/036). É a materialização concreta do
// Princípio 4: o que sai da janela permanece no backend, auditável e recuperável. O
// relógio é INJECTÁVEL (o CreatedAt é metadado obrigatório; determinismo — sem
// time.Now no caminho de decisão).
type MemoryPortSink struct {
	port          ports.MemoryPort
	agentID       string
	runID         string
	provenance    domain.Provenance
	ttl           domain.TTLClass
	schemaVersion string
	clock         func() time.Time
}

// MemoryPortSinkConfig parametriza o MemoryPortSink.
type MemoryPortSinkConfig struct {
	Port          ports.MemoryPort
	AgentID       string
	RunID         string
	Provenance    domain.Provenance
	TTLClass      domain.TTLClass
	SchemaVersion string
	// Clock é injectável (nil usa um relógio fixo determinístico não-zero).
	Clock func() time.Time
}

// NewMemoryPortSink constrói o sink com defaults seguros (proveniência untrusted —
// o tail carrega tool results; TTL efémero — memória de trabalho; schema "1.0.0").
func NewMemoryPortSink(cfg MemoryPortSinkConfig) *MemoryPortSink {
	prov := cfg.Provenance
	if !prov.Valid() {
		prov = domain.ProvenanceUntrusted
	}
	ttl := cfg.TTLClass
	if !ttl.Valid() {
		ttl = domain.TTLEphemeral
	}
	sv := cfg.SchemaVersion
	if sv == "" {
		sv = "1.0.0"
	}
	clock := cfg.Clock
	if clock == nil {
		fixed := time.Unix(0, 0).UTC().Add(time.Second) // não-zero, determinístico
		clock = func() time.Time { return fixed }
	}
	return &MemoryPortSink{
		port:          cfg.Port,
		agentID:       cfg.AgentID,
		runID:         cfg.RunID,
		provenance:    prov,
		ttl:           ttl,
		schemaVersion: sv,
		clock:         clock,
	}
}

// Persist implementa EvictionSink: escreve o segmento evictado como ClassWorking na
// MemoryPort (idempotente por f(RunID, Class, ID)). O registo PERMANECE mesmo depois
// de o segmento sair da vista da janela.
func (s *MemoryPortSink) Persist(ctx context.Context, ev EvictedSegment) error {
	rec := domain.Record{
		ID:    ev.RecordID,
		Class: domain.ClassWorking,
		Metadata: domain.Metadata{
			AgentID:       s.agentID,
			RunID:         s.runID,
			Provenance:    s.provenance,
			CreatedAt:     s.clock(),
			TTLClass:      s.ttl,
			SchemaVersion: s.schemaVersion,
		},
		Body: domain.WorkingBody{
			TurnIndex:  ev.TurnIndex,
			Content:    ev.Content,
			TokenCount: ev.Tokens,
		},
	}
	stored, err := s.port.Put(ctx, rec)
	if err != nil {
		return err
	}
	// A MemoryPort é idempotente por f(RunID, Class, ID): um RecordID repetido devolve
	// o registo JÁ persistido (err=nil) sem sobrescrever. Se o conteúdo devolvido
	// divergir do segmento que se ia evictar, o segmento distinto NÃO ficou de facto no
	// registo — remover-o da vista perdê-lo-ia silenciosamente. Fail-closed: aborta a
	// eviction (Princípio 4, AOS-036 — o que sai da vista permanece byte-a-byte).
	if wb, ok := stored.Body.(domain.WorkingBody); ok {
		if wb.Content != ev.Content || wb.TokenCount != ev.Tokens {
			return fmt.Errorf("%w: record_id=%q", ErrEvictionRecordConflict, ev.RecordID)
		}
	}
	return nil
}
