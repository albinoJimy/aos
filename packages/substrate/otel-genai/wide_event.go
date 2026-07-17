package otelgenai

// Padrão WIDE EVENTS (AOS-082). O plano-base filtrava no EMIT-TIME ("os
// diagnósticos auto-limpam, só emito sinais operator-fixable"), o que ESCONDE
// padrões sistémicos: o que não parece accionável hoje é a pista da falha de
// amanhã. O AOS substitui-o por: capturar TUDO num evento LARGO de alta
// cardinalidade por unidade de trabalho, e filtrar SEMPRE no QUERY-TIME.
//
// Este ficheiro dá a camada pura de otel-genai FOLHA (zero-dep, só stdlib):
//
//   - [WideEvent] — a projecção FLAT de um [SpanData] com TODAS as dimensões
//     (principal, modelo, tokens, custo, latência, decisão de política/PDP, taint,
//     versões pinadas) em campos tipados de conveniência MAIS o bag Attributes
//     COMPLETO (nada descartado — é a garantia de "sem descarte no emit-time / sem
//     perda de cardinalidade").
//   - [WideEventFromSpanData] — a projecção, com enriquecimento opcional das
//     dimensões que vivem no manifesto/tenant (tenant, versões pinadas) e não no
//     span de modelo.
//   - [WideEventStore] — a referência in-memory EFÉMERA com TTL (relógio
//     injectável, eviction), FÍSICA e LOGICAMENTE distinta do audit WORM (AOS-083):
//     tem TTL e a eviction é DESTRUTIVA — o oposto do write-once-read-many.
//   - API de QUERY-TIME pura ([Filter]/[GroupByWide]/[AggregateUsage]/[SumBy]) que
//     responde a perguntas NÃO previstas (ex. custo por tenant E por modelo) por
//     agregação ad-hoc sobre os eventos JÁ recolhidos, sem reinstrumentar.
//
// # Diagnóstico efémero != Audit permanente (fronteira crítica)
//
// Os wide events são DIAGNÓSTICOS EFÉMEROS com TTL — auto-limpam-se por eviction.
// NÃO são o audit trail, que é PERMANENTE e tamper-evident (hash-chain + WORM +
// assinatura, AOS-083, NOUTRO caminho). Não confundir: perder um wide event por
// TTL é esperado e inócuo; perder um evento de audit seria uma falha de
// conformidade. O backend real de alta cardinalidade (ClickHouse/etc.) é um
// adapter de deployment DIFERIDO — esta referência in-memory prova a semântica.

import (
	"math"
	"strings"
	"sync"
	"time"
)

// WideEvent é o registo FLAT de alta cardinalidade que projecta uma unidade de
// trabalho (um [SpanData]) com TODAS as dimensões. Os campos tipados são
// DERIVADOS dos atributos (conveniência de query e de asserção); o mapa
// [WideEvent.Attributes] carrega o bag COMPLETO — cada atributo do span mais cada
// chave de enriquecimento, NADA descartado. É essa completude que garante "sem
// descarte no emit-time / sem perda de cardinalidade": qualquer dimensão sem
// campo tipado responde-se lendo o bag no query-time.
type WideEvent struct {
	// Ephemeral marca este registo como DIAGNÓSTICO EFÉMERO (TTL-bound), DISTINTO do
	// audit trail permanente e tamper-evident (AOS-083). É sempre true — um wide
	// event é, por natureza, diagnóstico descartável, nunca prova de audit.
	Ephemeral bool
	// ExpiresAtUnixNano é o prazo de eviction (unix-nano). 0 = ainda não atribuído (é
	// fixado quando o evento entra num [WideEventStore] com TTL). O audit WORM NÃO
	// tem TTL — esta é a marca estrutural que separa o efémero do permanente.
	ExpiresAtUnixNano int64

	// --- identidade / topologia ---
	TraceIDHex      string
	SpanIDHex       string
	ParentSpanIDHex string
	Operation       string

	// --- principal / tenant ---
	PrincipalNHI string
	TenantID     string

	// --- modelo / usage ---
	Model        string
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64

	// --- latência (End-Start; 0 se os spans não modelam relógio) ---
	LatencyNanos int64

	// --- decisão de política (PDP) ---
	Decision string
	DeniedBy string

	// --- taint ---
	Taint       string
	ResultTaint string

	// --- correlação / tool ---
	RunID    string
	StepID   string
	ToolName string

	// --- content-capture por referência (só hashes, nunca payload — ADR-005) ---
	PromptHash   string
	PrefixHash   string
	ToolCallHash string

	// --- erro / estado ---
	ErrorType  string
	StatusCode StatusCode

	// --- versões pinadas do manifesto (aos.pinned.*), recolhidas do bag ---
	PinnedVersions map[string]string

	// Attributes é o bag COMPLETO de alta cardinalidade: TODOS os atributos do span
	// mais TODAS as chaves de enriquecimento. NADA é descartado — é a garantia de
	// "capturar tudo". O query-time lê daqui as dimensões sem campo tipado.
	Attributes map[string]any
}

// TotalTokens é o volume de tokens de modelo do evento (input + output).
func (w WideEvent) TotalTokens() int64 { return w.InputTokens + w.OutputTokens }

// CostUSD converte o custo do evento (micro-USD inteiro) para USD — só apresentação.
func (w WideEvent) CostUSD() float64 { return MicroUSDToUSD(w.CostMicroUSD) }

// Latency devolve a latência do evento como time.Duration (conveniência).
func (w WideEvent) Latency() time.Duration { return time.Duration(w.LatencyNanos) }

// WideEventFromSpanData projecta um [SpanData] num [WideEvent], capturando TODOS
// os atributos no bag e derivando os campos tipados. A latência é End-Start (0 se
// os timestamps são nulos, ex.: [RecordingTracer]). Os mapas extra opcionais
// ENRIQUECEM o bag com dimensões que vivem no manifesto/tenant (tenant, versões
// pinadas) e não no span de modelo — aplicados por ordem, os últimos ganham. NÃO
// há qualquer filtro que largue dimensões: é captura total, filtragem no
// query-time.
func WideEventFromSpanData(sd SpanData, enrich ...map[string]any) WideEvent {
	bag := make(map[string]any, len(sd.Attributes)+4)
	for _, kv := range sd.Attributes {
		bag[kv.Key] = kv.Value
	}
	// Enriquecimento: junta dimensões do manifesto/tenant ANTES da derivação, para os
	// campos tipados (ex. TenantID, PinnedVersions) verem os valores enriquecidos.
	for _, e := range enrich {
		for k, v := range e {
			bag[k] = v
		}
	}
	w := WideEvent{
		Ephemeral:       true,
		TraceIDHex:      sd.SpanContext.TraceIDHex(),
		SpanIDHex:       sd.SpanContext.SpanIDHex(),
		ParentSpanIDHex: parentHexOf(sd),
		Operation:       operationOf(sd),
		StatusCode:      sd.Status.Code,
		Attributes:      bag,
	}
	if sd.EndUnixNano > sd.StartUnixNano {
		w.LatencyNanos = sd.EndUnixNano - sd.StartUnixNano
	}
	w.deriveFromBag()
	return w
}

// Enrich junta dimensões adicionais ao bag e RE-DERIVA os campos tipados. Usa-se
// quando o tenant/versões pinadas só se conhecem depois da projecção. NÃO remove
// nada — só acrescenta (captura total).
func (w *WideEvent) Enrich(extra map[string]any) {
	if w.Attributes == nil {
		w.Attributes = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		w.Attributes[k] = v
	}
	w.deriveFromBag()
}

// deriveFromBag (re)calcula os campos tipados de conveniência a partir do bag
// completo. É idempotente e não descarta chaves do bag.
func (w *WideEvent) deriveFromBag() {
	w.PrincipalNHI = attrStringBag(w.Attributes, AttrPrincipalNHI)
	w.TenantID = attrStringBag(w.Attributes, AttrTenantID)
	w.Model = attrStringBag(w.Attributes, AttrRequestModel)
	w.InputTokens = attrInt64Bag(w.Attributes, AttrInputTokens)
	w.OutputTokens = attrInt64Bag(w.Attributes, AttrOutputTokens)
	w.CostMicroUSD = costMicroUSDFromBag(w.Attributes)
	w.Decision = attrStringBag(w.Attributes, AttrDecision)
	w.DeniedBy = attrStringBag(w.Attributes, AttrDeniedBy)
	w.Taint = attrStringBag(w.Attributes, AttrTaint)
	w.ResultTaint = attrStringBag(w.Attributes, AttrResultTaint)
	w.RunID = attrStringBag(w.Attributes, AttrRunID)
	w.StepID = attrStringBag(w.Attributes, AttrStepID)
	w.ToolName = attrStringBag(w.Attributes, AttrToolName)
	w.PromptHash = attrStringBag(w.Attributes, AttrPromptHash)
	w.PrefixHash = attrStringBag(w.Attributes, AttrPrefixHash)
	w.ToolCallHash = attrStringBag(w.Attributes, AttrToolCallHash)
	w.ErrorType = attrStringBag(w.Attributes, AttrErrorType)

	// Versões pinadas: TODAS as chaves com o prefixo aos.pinned.* — sem fixar o
	// conjunto, uma dimensão pinada nova aparece sem alterar este código.
	var pinned map[string]string
	for k, v := range w.Attributes {
		if strings.HasPrefix(k, PinnedVersionPrefix) {
			if pinned == nil {
				pinned = make(map[string]string)
			}
			pinned[k] = stringify(v)
		}
	}
	w.PinnedVersions = pinned
}

// attrStringBag lê um atributo string do bag (vazio se ausente ou de outro tipo).
func attrStringBag(bag map[string]any, key string) string {
	if v, ok := bag[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// attrInt64Bag lê um atributo numérico inteiro do bag (0 se ausente/inválido),
// reutilizando a coerção robusta de [attrInt64].
func attrInt64Bag(bag map[string]any, key string) int64 {
	if v, ok := bag[key]; ok {
		if n, ok := attrInt64(v); ok {
			return n
		}
	}
	return 0
}

// costMicroUSDFromBag lê o custo em micro-USD inteiro do bag: prefere o inteiro
// exacto [AttrCostMicroUSD]; na ausência, converte o USD float [AttrCostUSD] com
// round-half. É a mesma regra de [costMicroUSDOf], aqui sobre o mapa.
func costMicroUSDFromBag(bag map[string]any) int64 {
	if v, ok := bag[AttrCostMicroUSD]; ok {
		if n, ok := attrInt64(v); ok {
			return n
		}
	}
	if v, ok := bag[AttrCostUSD]; ok {
		if f, ok := attrFloat64(v); ok {
			return int64(math.Round(f * float64(microUSDPerUSD)))
		}
	}
	return 0
}

// --- API de QUERY-TIME (agregação ad-hoc, pura) ---
//
// Estas funções respondem a perguntas NÃO previstas à instrumentação por
// agregação sobre os wide events JÁ recolhidos, sem reinstrumentar. A filtragem é
// SEMPRE aqui, no query-time — nunca há descarte no emit-time.

// Filter devolve os eventos que satisfazem pred. É a filtragem no QUERY-TIME: os
// dados foram TODOS capturados; escolhe-se o subconjunto na pergunta, não na
// emissão.
func Filter(events []WideEvent, pred func(WideEvent) bool) []WideEvent {
	var out []WideEvent
	for _, e := range events {
		if pred(e) {
			out = append(out, e)
		}
	}
	return out
}

// GroupByWide agrupa os eventos por uma chave composta arbitrária (keyFn). A chave
// pode combinar QUALQUER dimensão (ex. tenant+modelo) — é o que permite responder
// a uma pergunta nova sem reinstrumentar.
func GroupByWide(events []WideEvent, keyFn func(WideEvent) string) map[string][]WideEvent {
	out := make(map[string][]WideEvent)
	for _, e := range events {
		k := keyFn(e)
		out[k] = append(out[k], e)
	}
	return out
}

// AggregateUsage soma tokens/custo (reutilizando [UsageTotals]) por grupo definido
// por keyFn. Responde directamente a "custo por <dimensão(s)>" — ex. keyFn que
// combine tenant e modelo dá "custo por tenant e por modelo".
func AggregateUsage(events []WideEvent, keyFn func(WideEvent) string) map[string]UsageTotals {
	out := make(map[string]UsageTotals)
	for _, e := range events {
		out[keyFn(e)] = out[keyFn(e)].add(UsageTotals{
			InputTokens:  e.InputTokens,
			OutputTokens: e.OutputTokens,
			CostMicroUSD: e.CostMicroUSD,
		})
	}
	return out
}

// SumBy soma um valor inteiro arbitrário (valueFn) por grupo (keyFn) — a agregação
// genérica para qualquer métrica numérica derivável de um evento (ex. latência
// total por operação).
func SumBy(events []WideEvent, keyFn func(WideEvent) string, valueFn func(WideEvent) int64) map[string]int64 {
	out := make(map[string]int64)
	for _, e := range events {
		out[keyFn(e)] += valueFn(e)
	}
	return out
}

// --- Store EFÉMERO com TTL (referência in-memory) ---

// WideEventStore é a referência in-memory dos wide events como DIAGNÓSTICOS
// EFÉMEROS: cada evento ganha um prazo de eviction (TTL) e é DESCARTADO quando
// expira. O relógio é injectável (nunca time.Now() directo em código testável). É
// FÍSICA e LOGICAMENTE distinto do audit WORM (AOS-083): não há hash-chain, não há
// write-once (a eviction remove dados), não há assinatura — a perda por TTL é
// esperada e inócua, ao contrário do audit permanente. O backend real de alta
// cardinalidade é um adapter de deployment DIFERIDO com a mesma semântica.
type WideEventStore struct {
	mu     sync.Mutex
	clock  func() time.Time
	ttl    time.Duration
	events []WideEvent
}

// NewWideEventStore constrói um store efémero com o TTL dado e um relógio
// injectável (nil ⇒ time.Now). Um ttl <= 0 desliga a expiração (os eventos ficam
// até serem lidos/limpos manualmente) — o caso normal passa um TTL positivo.
func NewWideEventStore(ttl time.Duration, clock func() time.Time) *WideEventStore {
	if clock == nil {
		clock = time.Now
	}
	return &WideEventStore{clock: clock, ttl: ttl}
}

// Add insere um wide event, carimba o seu prazo de eviction (now + TTL) e faz
// eviction dos expirados. Força Ephemeral=true — nada que entra aqui é audit.
// Devolve o evento JÁ carimbado (Ephemeral + ExpiresAt), calculado DENTRO da
// secção crítica: o valor devolvido é sempre o desta chamada, nunca o de outra
// goroutine concorrente (não se lê events[len-1] fora do lock).
func (s *WideEventStore) Add(w WideEvent) WideEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	w.Ephemeral = true
	if s.ttl > 0 {
		w.ExpiresAtUnixNano = now.Add(s.ttl).UnixNano()
	}
	s.events = append(s.events, w)
	s.evictLocked(now.UnixNano())
	return w
}

// Record projecta um [SpanData] (com enriquecimento opcional) e insere-o. Devolve
// o wide event resultante (já carimbado com o TTL) — é o valor carimbado por [Add]
// na mesma secção crítica, correcto mesmo sob inserções concorrentes.
func (s *WideEventStore) Record(sd SpanData, enrich ...map[string]any) WideEvent {
	return s.Add(WideEventFromSpanData(sd, enrich...))
}

// Events devolve uma cópia dos eventos VIVOS (não expirados), após eviction. É a
// superfície de leitura para a agregação query-time.
func (s *WideEventStore) Events() []WideEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(s.clock().UnixNano())
	out := make([]WideEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Len devolve o nº de eventos vivos (após eviction).
func (s *WideEventStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(s.clock().UnixNano())
	return len(s.events)
}

// Reap força a eviction dos expirados e devolve quantos foram DESCARTADOS. Prova a
// natureza efémera (destrutiva) do store — a antítese do WORM.
func (s *WideEventStore) Reap() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.events)
	s.evictLocked(s.clock().UnixNano())
	return before - len(s.events)
}

// evictLocked remove in-place os eventos cujo prazo expirou (ExpiresAt > 0 &&
// ExpiresAt <= now). Um evento sem prazo (ExpiresAt == 0, TTL desligado) nunca
// expira. Chamador deve deter s.mu.
func (s *WideEventStore) evictLocked(nowNano int64) {
	kept := s.events[:0]
	for _, e := range s.events {
		if e.ExpiresAtUnixNano == 0 || e.ExpiresAtUnixNano > nowNano {
			kept = append(kept, e)
		}
	}
	// Zera a cauda para libertar referências (os mapas Attributes/PinnedVersions).
	for i := len(kept); i < len(s.events); i++ {
		s.events[i] = WideEvent{}
	}
	s.events = kept
}
