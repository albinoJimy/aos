package agentruntime

import (
	"context"
	"errors"
	"fmt"
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
//
// A QUARENTENA VIVE SOBRE UM ARMAZÉM (AOS-550, fase R2 do ADR-043). Até ao AOS-550 a
// [Quarantine] era um mapa em memória: sem durabilidade, sem cifra, sem proveniência e fora
// do alcance da DSAR e da retenção. Passa a ser um ÍNDICE — id opaco → (digest + factos de
// nível-esquema) — sobre um [QuarantineStore] que detém os bytes. Nenhum byte do conteúdo
// fica neste pacote: [Quarantine.Resolve] vai buscá-los ao armazém por
// `(titular, run, digest)`, e é o armazém que lhes dá cifra por titular, proveniência selada
// e crypto-shredding.

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

// HandleFacts são os factos de NÍVEL-ESQUEMA que o NÓ AFIRMA sobre o conteúdo que um
// [Handle] referencia (AOS-550, decisão do dono de 2026-10-23 — emenda ao ADR-043 §2.4, que
// os tinha escrito como «a referência e um resumo»).
//
// NÍVEL-ESQUEMA, E NUNCA UM RESUMO. O que entra aqui é o que se sabe do conteúdo SEM o ler
// como linguagem: o tipo declarado, o tamanho em bytes, o nome com que foi gravado, e se os
// bytes são bem formados para o tipo. Um resumo — mesmo gerado pelo nó — seria conteúdo
// untrusted reescrito em prosa, e punha de volta no control-plane exactamente aquilo que a
// separação existe para tirar de lá. Quem precisar de decidir sobre o que os dados DIZEM
// espera a R3: um `model_invoke` em quarentena cujo resultado é outro handle.
//
// É O NÓ QUE AFIRMA. Nenhum destes campos vem do modelo nem do conteúdo: o tipo é o do
// vocabulário fechado com que o armazém aceitou a gravação, os bytes são os que o nó contou, o
// nome passou o alfabeto do nó, e [HandleFacts.WellFormed] é o veredicto do validador do nó.
type HandleFacts struct {
	// MediaType é o tipo declarado do conteúdo (`application/json`, `text/markdown`, …).
	MediaType string
	// Bytes é o tamanho do conteúdo em claro, contado pelo nó.
	Bytes int64
	// Name é o nome com que o conteúdo foi gravado no armazém.
	Name string
	// WellFormed é a VALIDADE: o nó afirma que os bytes são bem formados para o
	// [HandleFacts.MediaType] declarado (JSON que parseia, SVG que passou as regras). É um
	// facto de esquema — diz que a forma está certa, nunca o que o conteúdo diz.
	WellFormed bool
}

// Handle é uma referência OPACA a conteúdo untrusted em quarentena, com os factos de
// NÍVEL-ESQUEMA que o nó afirma sobre ele ([HandleFacts]). É um id não-secreto e SEM QUALQUER
// BYTE DO CONTEÚDO — o planeador manipula handles, não dados untrusted. O valor-zero é o
// "handle nulo" (nenhuma quarentena).
type Handle struct {
	id     string
	digest string
	origin taint.Origin
	facts  HandleFacts
}

// String devolve o id opaco do handle (ex.: "h1"). Nunca revela o conteúdo.
func (h Handle) String() string { return h.id }

// IsZero indica se é o handle nulo (não referencia nada).
func (h Handle) IsZero() bool { return h.id == "" }

// Digest é a chave do conteúdo no armazém (`sha256:<hex>`). É um COMPROMISSO com os bytes,
// não os bytes: é por ele que o armazém confere, depois de decifrar, que entrega exactamente
// o conteúdo que o Reference Monitor autorizou pela referência.
func (h Handle) Digest() string { return h.digest }

// Origin é a proveniência real do conteúdo em quarentena (web, schema MCP, tool_result, …),
// preservada para o forense (ASI06). Classifica sempre untrusted — ver [quarantineOrigin].
func (h Handle) Origin() taint.Origin { return h.origin }

// Facts devolve os factos de nível-esquema que o nó afirma sobre o conteúdo.
func (h Handle) Facts() HandleFacts { return h.facts }

// HandleRefPrefix é o prefixo da forma TEXTUAL de um [Handle] — a referência que o modelo
// emite num argumento de tool: `aos-handle/v1:h1`.
//
// PORQUE UMA STRING E NÃO UM OBJECTO JSON. Os argumentos das tools são esquemas tipados
// (`content` é uma string, `doc_id` é uma string): um objecto no lugar de uma string faria o
// `json.Decode` do nó falhar antes de haver referência que resolver. Uma string atravessa
// qualquer esquema que aceite texto, e o prefixo torna-a inconfundível com texto do modelo.
//
// O PREFIXO NÃO É AUTORIDADE. Qualquer um pode escrever estes bytes — um documento lido, o
// próprio modelo. Reconhecer a referência é só o primeiro passo; quem decide se ela vale são
// as duas condições independentes do AOS-550: (a) o id tem de constar da quarentena do run, e
// um id só entra na quarentena por uma tool de CÓDIGO DO NÓ (a regra do `media.go:26-32`); e
// (b) o par `(run, digest)` tem de constar de um `artifact.created` do stream do run.
const HandleRefPrefix = "aos-handle/v1:"

// Ref devolve a forma TEXTUAL do handle ([HandleRefPrefix] + id) — o que o nó mostra ao
// modelo e o que o modelo escreve de volta num argumento. O handle nulo não tem forma textual.
func (h Handle) Ref() string {
	if h.IsZero() {
		return ""
	}
	return HandleRefPrefix + h.id
}

// ParseHandleRef reconhece a forma textual de um handle e devolve o id opaco. Devolve false
// para tudo o que não seja EXACTAMENTE [HandleRefPrefix] seguido de um id da forma `h<n>`,
// com n inteiro positivo sem zero à esquerda — a mesma forma que [Quarantine] cunha. Não
// consulta a quarentena: só reconhece a forma.
func ParseHandleRef(s string) (string, bool) {
	if len(s) <= len(HandleRefPrefix) || s[:len(HandleRefPrefix)] != HandleRefPrefix {
		return "", false
	}
	return validHandleID(s[len(HandleRefPrefix):])
}

// validHandleID aceita `h<n>` com n ≥ 1 e sem zero à esquerda, até 10 dígitos (um run não tem
// mais handles do que isso, e o tecto fecha a porta a um id gigante vindo de fora).
func validHandleID(id string) (string, bool) {
	if len(id) < 2 || len(id) > 11 || id[0] != 'h' || id[1] == '0' {
		return "", false
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return "", false
		}
	}
	return id, true
}

// QuarantineStore é o ARMAZÉM por baixo da quarentena: quem detém os bytes untrusted,
// endereçados por `(titular, run, digest)`. A implementação de produção é o armazém de
// artefactos do run (AOS-548), que lhes dá cifra por titular, proveniência selada no stream do
// run, DSAR e retenção — nada disso é do kernel.
//
// A CHAVE É TRIPLA E NÃO POR ACIDENTE. O titular é quem a cifra e a retenção reconhecem; o run
// é a fronteira dentro da qual um handle vale (entre runs só pela declaração selada do plano,
// ADR-038 — nunca por um digest que o modelo forneça); o digest é o compromisso com os bytes.
//
// FAIL-CLOSED: uma implementação que não encontre, não decifre ou não confirme o digest
// devolve erro. Um handle que não resolve não dá bytes nenhuns — nunca dá bytes errados.
type QuarantineStore interface {
	// SealQuarantined sela o conteúdo no armazém do run e devolve o digest `sha256:<hex>`.
	SealQuarantined(ctx context.Context, subject, runID string, content []byte) (digest string, err error)
	// OpenQuarantined devolve o conteúdo em claro que `(titular, run, digest)` nomeia.
	OpenQuarantined(ctx context.Context, subject, runID, digest string) ([]byte, error)
}

// Erros da quarentena. São vocabulário FECHADO: quem os traduz em recusa de tool nomeia a
// causa a partir deles, e nenhum deles leva um byte do conteúdo.
var (
	// ErrQuarantineUnbound — quarentena construída sem armazém, sem titular ou sem run. Não há
	// onde pôr nem de onde ler: fail-closed na construção.
	ErrQuarantineUnbound = errors.New("agentruntime: quarentena sem armazem, titular ou run — (titular, run, digest) e a chave do conteudo")
	// ErrHandleUnknown — handle nulo, id que a quarentena deste run não cunhou, ou digest que
	// não é o que o id referencia. É a condição (a) do AOS-550: só uma referência produzida por
	// uma tool de código do nó entra na quarentena, logo um handle forjado nunca está lá.
	ErrHandleUnknown = errors.New("agentruntime: handle desconhecido neste run")
	// ErrHandleBodyTooLarge — o corpo excede [MaxToolResultBytes], o tecto do corpo de um
	// `tool_result`. Fail-closed nos DOIS sentidos: nem entra em quarentena, nem sai dela.
	ErrHandleBodyTooLarge = errors.New("agentruntime: corpo acima do tecto do tool_result")
	// ErrHandleEmptyBody — corpo vazio. Um handle para zero bytes não referencia nada.
	ErrHandleEmptyBody = errors.New("agentruntime: corpo vazio nao se poe em quarentena")
	// ErrHandleBadDigest — digest fora da forma `sha256:<64 hex minúsculos>`.
	ErrHandleBadDigest = errors.New("agentruntime: digest de handle malformado (sha256:<64 hex>)")
)

// Quarantine detém o conteúdo untrusted FORA do alcance do control-plane, entregando ao
// planeador apenas [Handle]s opacos. É o ÍNDICE de um run: id opaco → (digest + factos de
// nível-esquema), sobre o [QuarantineStore] que tem os bytes. O data-plane (que só manipula
// dados, nunca decide acções) resolve handles→conteúdo para os ARGUMENTOS das tools. É seguro
// para concorrência.
//
// NENHUM BYTE DO CONTEÚDO VIVE AQUI (AOS-550). O mapa de conteúdo em memória saiu: o que fica
// é o índice, e os bytes vêm do armazém a cada [Quarantine.Resolve].
type Quarantine struct {
	store   QuarantineStore
	subject string
	runID   string

	mu   sync.Mutex
	byID map[string]Handle
	seq  int
}

// NewQuarantine constrói a quarentena de UM run sobre um armazém. Os três argumentos são
// obrigatórios: são a chave com que o conteúdo se lê de volta. Sem um deles devolve
// [ErrQuarantineUnbound] — uma quarentena que aceita pôr e nunca consegue ler seria pior do
// que não existir.
func NewQuarantine(store QuarantineStore, subject, runID string) (*Quarantine, error) {
	if store == nil || subject == "" || runID == "" {
		return nil, ErrQuarantineUnbound
	}
	return &Quarantine{store: store, subject: subject, runID: runID, byID: make(map[string]Handle)}, nil
}

// Seal sela o valor untrusted no armazém do run e devolve o [Handle] opaco que o referencia,
// com os factos de nível-esquema que o nó afirma. A quarentena NÃO PROMOVE: o que lá fica é
// sempre untrusted (ver [quarantineOrigin]).
//
// É o caminho de ENTRADA de um corpo que ainda não está no armazém — um `tool_result`, um
// `plan_input`. O tecto de [MaxToolResultBytes] é imposto AQUI, antes de o conteúdo chegar ao
// armazém: é onde a recusa ainda é barata e onde a causa é nomeável.
func (q *Quarantine) Seal(ctx context.Context, v taint.Value, facts HandleFacts) (Handle, error) {
	payload := v.Payload()
	switch {
	case len(payload) == 0:
		return Handle{}, ErrHandleEmptyBody
	case len(payload) > MaxToolResultBytes:
		return Handle{}, fmt.Errorf("%w (%d bytes, tecto %d)", ErrHandleBodyTooLarge, len(payload), MaxToolResultBytes)
	}
	origem := taint.OriginToolResult
	if os := v.Origins(); len(os) > 0 {
		origem = quarantineOrigin(os[0])
	}
	digest, err := q.store.SealQuarantined(ctx, q.subject, q.runID, payload)
	if err != nil {
		return Handle{}, err
	}
	return q.register(digest, origem, facts)
}

// Adopt registra na quarentena conteúdo que JÁ ESTÁ no armazém, pelo seu digest, sem o
// reescrever e sem o ler. É o caminho de entrada do conteúdo que uma tool de código do nó
// gravou no armazém do run — a `artifact_write` (AOS-548) — e cuja referência o nó acabou de
// ver no resultado DELA.
//
// QUEM PODE CHAMAR ISTO É A FRONTEIRA DE CONFIANÇA. Adoptar um digest é afirmar que ele veio
// de código do nó; um digest que apareça no VALOR de outra tool (um documento lido, uma
// resposta remota) nunca deve chegar aqui — é a regra que o `media.go:26-32` escreveu para a
// `media_view` e que vale igual aqui. A condição (b) do AOS-550 — o par `(run, digest)` num
// `artifact.created` do stream do run — é imposta pelo armazém, a jusante e em separado.
func (q *Quarantine) Adopt(digest string, origin taint.Origin, facts HandleFacts) (Handle, error) {
	if NormalizeRouteProfileDigest(digest) == "" {
		return Handle{}, fmt.Errorf("%w: %q", ErrHandleBadDigest, digest)
	}
	return q.register(digest, quarantineOrigin(origin), facts)
}

// register cunha o id sequencial e indexa o handle. Os ids são deterministas e sequenciais
// ("h1", "h2", …) — SEM RELÓGIO E SEM RAND —, pela ordem por que o conteúdo entra. É o que o
// replay por dobra pura exige: a mesma sequência de entradas dá sempre os mesmos ids.
func (q *Quarantine) register(digest string, origin taint.Origin, facts HandleFacts) (Handle, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	h := Handle{id: "h" + strconv.Itoa(q.seq), digest: digest, origin: origin, facts: facts}
	q.byID[h.id] = h
	return h, nil
}

// Resolve devolve o valor untrusted que h referencia, LIDO DO ARMAZÉM por
// `(titular, run, digest)` — para o data-plane o entregar como ARGUMENTO de uma tool.
//
// UM HANDLE QUE A QUARENTENA DESTE RUN NÃO CUNHOU NÃO RESOLVE ([ErrHandleUnknown]), e um id
// conhecido com outro digest também não: o digest que se lê é o que o índice guarda, nunca o
// que o chamador trouxe. Os bytes voltam marcados untrusted, com a proveniência preservada: a
// quarentena nunca promove.
func (q *Quarantine) Resolve(ctx context.Context, h Handle) (taint.Value, error) {
	if h.IsZero() {
		return taint.Value{}, ErrHandleUnknown
	}
	q.mu.Lock()
	known, ok := q.byID[h.id]
	q.mu.Unlock()
	if !ok || (h.digest != "" && h.digest != known.digest) {
		return taint.Value{}, fmt.Errorf("%w: %s", ErrHandleUnknown, h.id)
	}
	claro, err := q.store.OpenQuarantined(ctx, q.subject, q.runID, known.digest)
	if err != nil {
		return taint.Value{}, err
	}
	if len(claro) > MaxToolResultBytes {
		return taint.Value{}, fmt.Errorf("%w (%d bytes, tecto %d)", ErrHandleBodyTooLarge, len(claro), MaxToolResultBytes)
	}
	return taint.FromOrigin(known.origin, claro), nil
}

// Lookup devolve o handle que o id opaco referencia — o que resolve uma referência
// [HandleRefPrefix] que o modelo emitiu num argumento. Um id que esta quarentena não cunhou
// devolve false, e é a condição (a) do AOS-550 a fechar.
func (q *Quarantine) Lookup(id string) (Handle, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	h, ok := q.byID[id]
	return h, ok
}

// Handles devolve os handles cunhados, PELA ORDEM DE CUNHAGEM — a mesma ordem das entradas,
// e portanto a mesma em qualquer caminho que reconstrua a quarentena a partir delas.
func (q *Quarantine) Handles() []Handle {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Handle, 0, len(q.byID))
	for i := 1; i <= q.seq; i++ {
		if h, ok := q.byID["h"+strconv.Itoa(i)]; ok {
			out = append(out, h)
		}
	}
	return out
}

// Len devolve o nº de handles cunhados nesta quarentena.
func (q *Quarantine) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byID)
}

// PlannerView é EXACTAMENTE o que o control-plane (planeador) vê. Por CONSTRUÇÃO
// contém só segmentos TRUSTED e a lista ORDENADA de [Handle]s para o conteúdo
// untrusted em quarentena — nunca bytes untrusted. A separação é do TIPO: não há
// campo por onde conteúdo untrusted chegue ao planeador como instrução. O que um handle
// acrescenta são os factos de NÍVEL-ESQUEMA do nó ([HandleFacts]) — nunca conteúdo, nunca um
// resumo.
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
//
// FAIL-CLOSED (AOS-550): com a quarentena sobre um armazém, pôr um segmento em quarentena
// pode falhar (o armazém cheio, o cifrador indisponível, o corpo acima do tecto). Uma falha
// NÃO devolve uma vista parcial — devolve erro: uma [PlannerView] a que falta um handle é um
// prompt que não é o que quem o compôs declarou.
func SeparatePlanes(ctx context.Context, segs []PlaneSegment, q *Quarantine) (PlannerView, error) {
	view := PlannerView{}
	for i, seg := range segs {
		if seg.Label.IsTrusted() {
			view.Trusted = append(view.Trusted, seg)
			continue
		}
		// Untrusted: para a quarentena, referenciado por handle. O planeador nunca vê
		// o Content. A proveniência REAL do segmento (web/mcp_schema/model_output/…) é
		// preservada no Value em quarentena para o forense — não achatada para
		// tool_result.
		h, err := q.Seal(ctx, taint.FromOrigin(quarantineOrigin(seg.Origin), seg.Content), HandleFacts{Bytes: int64(len(seg.Content))})
		if err != nil {
			return PlannerView{}, fmt.Errorf("agentruntime: segmento %d (%s) em quarentena: %w", i, seg.Kind, err)
		}
		view.Handles = append(view.Handles, h)
	}
	return view, nil
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
// untrusted — a assinatura torna-o impossível.
//
// É o primitivo da OPÇÃO A do ADR-034, ainda sem chamador de produção: o loop base autoriza
// pelo rótulo do contexto ([ContextAuthority], opção C). Quando a opção A entrar, a autoridade
// das invocações que um ControlPlanner devolve continua a ser cunhada pelo runtime — a partir
// de a [PlannerView] ser trusted por construção —, e não por um campo das invocações.
type ControlPlanner interface {
	Plan(view PlannerView) []ToolInvocation
}
