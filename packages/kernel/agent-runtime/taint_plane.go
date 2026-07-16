package agentruntime

import (
	"strconv"
	"sync"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// Este ficheiro formaliza a separação control-plane/data-plane (dual-LLM/CaMeL,
// ADR-005/AOS-069). A barreira é ESTRUTURAL — pelo TIPO — não textual: o planeador
// (control-plane) recebe uma [PlannerView] que, por construção, só contém
// conteúdo TRUSTED e HANDLES opacos; o conteúdo untrusted vive em [Quarantine] e
// nunca é interpolado como instrução no que o planeador vê. Tags in-band (ex.:
// "taint=untrusted\n…" no prompt) NÃO contam como separação de privilégio; esta
// separação é a que conta.

// PlaneSegment é um segmento de contexto com o seu rótulo de taint ESTRUTURAL
// ([taint.Label]) — não uma marca textual dentro do conteúdo. É a entrada de
// [SeparatePlanes]: o rótulo (e não o texto do segmento) decide o plano.
type PlaneSegment struct {
	// Kind classifica o segmento (objectivo, memória, tool_result, …).
	Kind TailKind
	// Label é o rótulo de confiança estrutural do segmento (decide o PLANO).
	Label taint.Label
	// Origin é a PROVENIÊNCIA real do conteúdo (web/mcp_schema/model_output/…) — o
	// "de onde veio". O [Label] decide o plano; o Origin preserva o forense do
	// conteúdo em quarentena (ASI06), para [SeparatePlanes] não achatar toda a
	// origem untrusted para [taint.OriginToolResult]. Vazio ⇒ origem por omissão
	// coerente com o Label (ver [SeparatePlanes]).
	Origin taint.Origin
	// Content é o conteúdo opaco do segmento.
	Content []byte
}

// TrustedSegment constrói um [PlaneSegment] marcado [taint.Trusted] (ex.: system,
// objectivo selado, utilizador autenticado). Proveniência por omissão
// [taint.OriginSystem].
func TrustedSegment(kind TailKind, content []byte) PlaneSegment {
	return PlaneSegment{Kind: kind, Label: taint.Trusted, Origin: taint.OriginSystem, Content: content}
}

// UntrustedSegment constrói um [PlaneSegment] marcado [taint.Untrusted] com
// proveniência por omissão [taint.OriginToolResult]. Para preservar a origem real
// (web, schema MCP, saída do modelo, …) no forense da quarentena use
// [UntrustedSegmentFrom].
func UntrustedSegment(kind TailKind, content []byte) PlaneSegment {
	return PlaneSegment{Kind: kind, Label: taint.Untrusted, Origin: taint.OriginToolResult, Content: content}
}

// UntrustedSegmentFrom constrói um [PlaneSegment] untrusted preservando a
// PROVENIÊNCIA real (origin) do conteúdo — ex.: [taint.OriginWeb],
// [taint.OriginMCPSchema], [taint.OriginModelOutput]. O rótulo continua
// [taint.Untrusted] (a origem só é honrada como proveniência se classificar
// untrusted; ver [SeparatePlanes]), pelo que a separação de planos e o
// enforcement não mudam — só o forense fica fiel.
func UntrustedSegmentFrom(kind TailKind, origin taint.Origin, content []byte) PlaneSegment {
	return PlaneSegment{Kind: kind, Label: taint.Untrusted, Origin: origin, Content: content}
}

// Handle é uma referência OPACA a conteúdo untrusted em quarentena. É um id
// não-secreto e sem qualquer byte do conteúdo — o planeador manipula handles, não
// dados untrusted. O valor-zero é o "handle nulo" (nenhuma quarentena).
type Handle struct {
	id string
}

// String devolve o id opaco do handle (ex.: "h1"). Nunca revela o conteúdo.
func (h Handle) String() string { return h.id }

// IsZero indica se é o handle nulo (não referencia nada).
func (h Handle) IsZero() bool { return h.id == "" }

// Quarantine detém o conteúdo untrusted FORA do alcance do control-plane,
// entregando ao planeador apenas [Handle]s opacos. O data-plane (que só manipula
// dados, nunca decide acções) resolve handles→conteúdo para os ARGUMENTOS das
// tools. É seguro para concorrência.
type Quarantine struct {
	mu    sync.Mutex
	items map[string]taint.Value
	seq   int
}

// NewQuarantine constrói uma quarentena vazia.
func NewQuarantine() *Quarantine {
	return &Quarantine{items: make(map[string]taint.Value)}
}

// Put coloca um valor em quarentena e devolve o [Handle] opaco que o referencia. O
// id é determinista e sequencial ("h1", "h2", …) — sem relógio/rand — para runs
// reproduzíveis. O valor guardado permanece untrusted (a quarentena não promove).
func (q *Quarantine) Put(v taint.Value) Handle {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	id := "h" + strconv.Itoa(q.seq)
	q.items[id] = v
	return Handle{id: id}
}

// Resolve devolve o valor untrusted referenciado por h (para o data-plane o
// entregar como ARGUMENTO de uma tool). O segundo retorno é false para um handle
// nulo ou desconhecido.
func (q *Quarantine) Resolve(h Handle) (taint.Value, bool) {
	if h.IsZero() {
		return taint.Value{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[h.id]
	return v, ok
}

// Len devolve o nº de valores em quarentena.
func (q *Quarantine) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// PlannerView é EXACTAMENTE o que o control-plane (planeador) vê. Por CONSTRUÇÃO
// contém só segmentos TRUSTED e a lista ORDENADA de [Handle]s para o conteúdo
// untrusted em quarentena — nunca bytes untrusted. A separação é do TIPO: não há
// campo por onde conteúdo untrusted chegue ao planeador como instrução.
type PlannerView struct {
	// Trusted são os segmentos de confiança visíveis ao planeador (Label==Trusted).
	Trusted []PlaneSegment
	// Handles referenciam, por ordem, o conteúdo untrusted em quarentena.
	Handles []Handle
}

// SeparatePlanes separa os segmentos em control-plane (trusted, visível ao
// planeador) e data-plane (untrusted → quarentena por handle). É a fronteira
// dual-LLM/CaMeL: o conteúdo untrusted sai da vista do planeador e passa a ser
// referenciado por handle opaco. Determinista (ordem dos segmentos → ordem dos
// handles). O rótulo estrutural de cada segmento — não o seu texto — decide o
// plano, pelo que uma injecção embutida num segmento untrusted nunca alcança o
// planeador como instrução.
func SeparatePlanes(segs []PlaneSegment, q *Quarantine) PlannerView {
	view := PlannerView{}
	for _, seg := range segs {
		if seg.Label.IsTrusted() {
			view.Trusted = append(view.Trusted, seg)
			continue
		}
		// Untrusted: para a quarentena, referenciado por handle. O planeador nunca vê
		// o Content. A proveniência REAL do segmento (web/mcp_schema/model_output/…) é
		// preservada no Value em quarentena para o forense — não achatada para
		// tool_result.
		h := q.Put(taint.FromOrigin(quarantineOrigin(seg.Origin), seg.Content))
		view.Handles = append(view.Handles, h)
	}
	return view
}

// quarantineOrigin devolve a origem a gravar no Value em quarentena. A quarentena
// NUNCA promove: se a origem declarada estiver vazia ou (por incoerência) classificar
// trusted, cai para [taint.OriginToolResult], garantindo que o valor em quarentena é
// sempre untrusted. Caso contrário preserva a proveniência real (web, schema MCP,
// saída do modelo, …) para o forense.
func quarantineOrigin(o taint.Origin) taint.Origin {
	if o != "" && taint.LabelFor(o).IsUntrusted() {
		return o
	}
	return taint.OriginToolResult
}

// ControlPlanner é o control-plane do dual-LLM/CaMeL: decide tool calls olhando
// APENAS para a [PlannerView] (dados trusted + handles opacos). NUNCA recebe bytes
// untrusted — a assinatura torna-o impossível. As invocações que devolve são
// autorizadas pelo plano de confiança; use [AuthorizeTrusted] para as marcar antes
// de as submeter ao RM.
type ControlPlanner interface {
	Plan(view PlannerView) []ToolInvocation
}

// AuthorizeTrusted marca uma tool call como autorizada pelo control-plane TRUSTED
// (só o planeador sobre dados trusted a chama). É esta marca que o Reference
// Monitor lê ([referencemonitor.CallContext].Taint) para permitir uma capability
// privilegiada. Uma invocação NÃO marcada fica untrusted (fail-closed) e não pode
// originar acções privilegiadas.
func AuthorizeTrusted(inv ToolInvocation) ToolInvocation {
	inv.AuthorizationTaint = TaintTrusted
	return inv
}

// authorizationTaintOf devolve o taint de autorização efectivo de uma invocação,
// fail-closed: uma autorização ausente/desconhecida é untrusted. É o valor que o
// loop propaga ao [referencemonitor.CallContext].Taint.
func authorizationTaintOf(inv ToolInvocation) string {
	if taint.ParseLabel(inv.AuthorizationTaint).IsTrusted() {
		return TaintTrusted
	}
	return TaintUntrusted
}
