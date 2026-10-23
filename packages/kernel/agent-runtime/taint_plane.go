package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// HandleFacts são os factos de NÍVEL-ESQUEMA que o NÓ CUNHA sobre o conteúdo que um [Handle]
// referencia (AOS-550, decisões do dono de 2026-10-23 — emenda ao ADR-043 §2.4, que os tinha
// escrito como «a referência e um resumo»).
//
// NÍVEL-ESQUEMA, E NUNCA UM RESUMO. O que entra aqui é o que se sabe do conteúdo SEM o ler como
// linguagem. Um resumo — mesmo gerado pelo nó — seria conteúdo untrusted reescrito em prosa, e
// punha de volta no control-plane exactamente aquilo que a separação existe para tirar de lá. Quem
// precisar de decidir sobre o que os dados DIZEM espera a R3: um `model_invoke` em quarentena cujo
// resultado é outro handle.
//
// O NÓ CUNHA, NÃO REPETE. Nenhum destes campos é texto escolhido por quem produziu o conteúdo. Em
// particular o [HandleFacts.Name] é cunhado pelo nó (tool + índice, no molde do [ToolStepID]) e
// NUNCA o nome que o produtor pediu: o nome de um artefacto vem do modelo, com um alfabeto que é
// subconjunto do dos rótulos do tail e que sobrevive à sanitização byte a byte — seriam 64 bytes de
// texto escolhido por um atacante dentro do contexto que esta separação promete limpo. O nome
// legível de quem produziu pertence à superfície de leitura do dono, não à vista do planeador.
//
// E SÃO DETERMINISTAS. Todos são função pura dos bytes e da posição na sequência. É PROIBIDO um
// facto que dependa de um relógio ou de estado de fora da sequência — uma expiração, um instante,
// uma contagem global: o prompt re-materializa-se no replay e o `prompt_hash` compara-se com o
// gravado, pelo que um valor de parede divergiria em todos os turnos.
type HandleFacts struct {
	// MediaType é o tipo declarado do conteúdo (`application/json`, `text/plain`, …).
	MediaType string
	// Bytes é o tamanho do conteúdo em claro, contado pelo nó.
	Bytes int64
	// Name é o nome CUNHADO PELO NÓ: `<tool>-<índice>`, no molde do [ToolStepID]. Nunca o nome
	// que o produtor do conteúdo pediu.
	Name string
	// WellFormed é um facto de FORMA, calculado dos bytes: o nó parseou-os para o
	// [HandleFacts.MediaType] declarado e a forma está certa (JSON que parseia, SVG que passou as
	// regras, texto que é UTF-8). Diz que a FORMA está certa, nunca o que o conteúdo diz, e é
	// função pura dos bytes. Um tipo que o nó não saiba parsear fica `false`: o nó não afirma uma
	// forma que não conferiu.
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

// String devolve o TOKEN opaco do handle (ex.: "h1.0a1b2c3d4e5f6071"). Nunca revela o conteúdo.
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

// HandleRefPrefix é o prefixo da forma TEXTUAL de um [Handle] — a referência que o modelo emite
// num argumento de tool: `aos-handle/v1:h1.0a1b2c3d4e5f6071`.
//
// PORQUE UMA STRING E NÃO UM OBJECTO JSON. Os argumentos das tools são esquemas tipados (`content`
// é uma string, `doc_id` é uma string): um objecto no lugar de uma string faria o `json.Decode` do
// nó falhar antes de haver referência que resolver. Uma string atravessa qualquer esquema que
// aceite texto, e o prefixo torna-a inconfundível com texto do modelo. O alfabeto do token é
// subconjunto do dos rótulos do tail, pelo que sai de lá byte a byte igual ao que entrou.
//
// O PREFIXO NÃO É AUTORIDADE. Qualquer um pode escrever estes bytes — um documento lido, o próprio
// modelo. Reconhecer a forma é só o primeiro passo; quem decide se a referência vale são as duas
// condições independentes do AOS-550: (a) o token tem de constar da quarentena do run, e um token
// só lá entra por uma tool de CÓDIGO DO NÓ (a regra do `media.go:26-32`); e (b) o par
// `(run, digest)` tem de constar de um evento do próprio stream. E NENHUMA DAS DUAS DECIDE O
// TAINT: um run injectado cunha um handle válido sobre conteúdo que escolheu, e quem decide o que
// pode fluir para onde é a política de fluxo de dados do AOS-551.
const HandleRefPrefix = "aos-handle/v1:"

// handleTokenMACBytes é o nº de bytes do selo do token (16 dígitos hexadecimais).
const handleTokenMACBytes = 8

// MintHandleToken cunha o token de um handle: `h<ordinal>.<16 hex>`, em que os 16 hexadecimais são
// os primeiros [handleTokenMACBytes] de
// `sha256("aos-handle/v1" 0x00 runID 0x00 digest 0x00 ordinal)`.
//
// # AS PROPRIEDADES, DITAS COM PRECISÃO
//
//   - CUNHADO PELO NÓ e ESCOPADO AO RUN: o run entra no selo, logo o token de um conteúdo num run
//     não é o token do mesmo conteúdo noutro run;
//   - LIGADO À POSIÇÃO: o ordinal entra no selo. Sem isso, duas posições do mesmo conteúdo no
//     mesmo run tinham selos iguais (`h1.X` e `h7.X`) e o selo deixava de distinguir a entrada que
//     nomeia — medido na revisão de segurança do AOS-550 (achado B-1);
//   - DETERMINISTA, SEM RELÓGIO E SEM `rand`: é função pura de (run, digest, ordinal), pelo que a
//     reconstrução noutro processo — uma retoma, um replay por dobra — cunha o MESMO token. Uma
//     fonte aleatória aqui tornaria o `prompt_hash` irreproduzível.
//
// # O QUE «NÃO ADIVINHÁVEL» SIGNIFICA AQUI, E O QUE NÃO SIGNIFICA
//
// Significa: NÃO ESCREVÍVEL POR QUEM NÃO CONHECE OS BYTES. O selo é função do digest, logo escrever
// um token exige calcular um SHA-256 sobre conteúdo que se conhece.
//
// NÃO significa um segredo com chave. É um hash SEM CHAVE de dois valores que não são secretos: o
// `run_id` (escolhido por quem submete o run, ou derivado) e o digest (publicado ao modelo no
// resultado da `artifact_write` e na lista de artefactos do run). Quem conheça os dois pode
// computar o token.
//
// Em que é que isso é inofensivo HOJE e deixa de ser: na R2 o único produtor sela conteúdo que o
// próprio modelo escreveu, pelo que resolver devolve-lhe o que ele já tinha. Quando um handle
// passar a carregar um documento LIDO DE FORA cujos bytes um atacante escolheu — a R3 —, uma
// referência pré-computada dentro do próprio documento torna-se uma referência válida, e aí o selo
// tem de passar a ser um MAC com chave do nó. Fica como resíduo nomeado do AOS-550, para a fase que
// o torna alcançável; a protecção que vale em ambas as fases é o ÍNDICE (um token só resolve se a
// quarentena do run o tiver cunhado), não o selo.
func MintHandleToken(runID, digest string, ordinal int) string {
	h := sha256.New()
	h.Write([]byte("aos-handle/v1"))
	h.Write([]byte{0})
	h.Write([]byte(runID))
	h.Write([]byte{0})
	h.Write([]byte(digest))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(ordinal)))
	soma := h.Sum(nil)
	return "h" + strconv.Itoa(ordinal) + "." + hex.EncodeToString(soma[:handleTokenMACBytes])
}

// Ref devolve a forma TEXTUAL do handle ([HandleRefPrefix] + token) — o que o nó mostra ao modelo
// e o que o modelo escreve de volta num argumento. O handle nulo não tem forma textual.
func (h Handle) Ref() string {
	if h.IsZero() {
		return ""
	}
	return HandleRefPrefix + h.id
}

// ParseHandleRef reconhece a forma textual de um handle e devolve o token. Devolve false para tudo
// o que não seja EXACTAMENTE [HandleRefPrefix] seguido de um token na forma que [MintHandleToken]
// cunha. Não consulta a quarentena e não verifica o selo: só reconhece a FORMA.
func ParseHandleRef(s string) (string, bool) {
	if len(s) <= len(HandleRefPrefix) || s[:len(HandleRefPrefix)] != HandleRefPrefix {
		return "", false
	}
	token := s[len(HandleRefPrefix):]
	if _, ok := HandleTokenOrdinal(token); !ok {
		return "", false
	}
	return token, true
}

// HandleTokenOrdinal devolve o ordinal de um token bem formado (`h<n>.<16 hex>`, com n >= 1 sem
// zero à esquerda e até 9 dígitos). É o que o índice da quarentena usa para impor que a sequência
// dos tokens é a ordem das entradas.
func HandleTokenOrdinal(token string) (int, bool) {
	ponto := strings.IndexByte(token, '.')
	if ponto < 2 || ponto > 10 || token[0] != 'h' || token[1] == '0' {
		return 0, false
	}
	if len(token) != ponto+1+2*handleTokenMACBytes {
		return 0, false
	}
	for i := ponto + 1; i < len(token); i++ {
		if c := token[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return 0, false
		}
	}
	n, err := strconv.Atoi(token[1:ponto])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// QuarantineStore é o ARMAZÉM por baixo da quarentena: quem detém os bytes untrusted, endereçados
// por `(titular, run, digest)`. A implementação de produção é o ESPAÇO PRÓPRIO DE HANDLES do nó —
// com os seus tectos, vocabulário aberto e superfície de leitura própria —, que lhes dá cifra por
// titular, proveniência selada no stream do run, DSAR e retenção. Nada disso é do kernel.
//
// PORQUE UM ESPAÇO PRÓPRIO, E NÃO A VIA DA TOOL DE ESCRITA DO MODELO. Medido na revisão do
// AOS-550: os tectos do armazém de artefactos são por run e PARTILHADOS com o que o modelo grava,
// o seu vocabulário de tipos é fechado e recusa binário, e com o mesmo emissor nos eventos todo o
// conteúdo em quarentena passaria a ser descarregável pela capability de leitura dos artefactos —
// conteúdo que hoje só a via de trajectória expõe mudaria de capability sem ninguém o declarar.
//
// A CHAVE É TRIPLA E NÃO POR ACIDENTE. O titular é quem a cifra e a retenção reconhecem; o run é a
// fronteira dentro da qual um handle vale (entre runs só pela declaração selada do plano, ADR-038 —
// nunca por um digest que o modelo forneça); o digest é o compromisso com os bytes.
//
// FAIL-CLOSED: uma implementação que não encontre, não decifre, não confirme o digest ou não
// confirme que o cifrado foi selado PARA ESTE RUN devolve erro. Um handle que não resolve não dá
// bytes nenhuns — nunca dá bytes errados.
type QuarantineStore interface {
	// SealQuarantined sela o conteúdo no espaço de handles do run e devolve o token cunhado pelo
	// nó ([MintHandleToken]), o digest `sha256:<hex>` e OS FACTOS QUE O ARMAZÉM CUNHOU.
	//
	// OS FACTOS VOLTAM DAQUI, e não são os que o chamador passou: o armazém é quem conta os bytes,
	// cunha o nome (`<tool>-<índice>`) e confere a forma, e é isso que fica selado no evento de
	// proveniência. Devolver os do chamador fazia o handle VIVO ter `bytes=0`, `name=""` e
	// `well_formed=false` enquanto o handle RECONSTRUÍDO dos eventos tinha os verdadeiros — duas
	// derivações divergentes para a mesma entrada, medido na revisão do AOS-550. Os `facts` que
	// entram são o que o chamador DECLARA (o tipo de media); os que saem são o que o nó AFIRMA.
	SealQuarantined(ctx context.Context, subject, runID string, content []byte, facts HandleFacts) (token, digest string, cunhados HandleFacts, err error)
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
	// ErrHandleBadToken — token malformado, ou com um ordinal que não é o da sua posição na
	// sequência. O segundo caso é o guarda do determinismo: se o nó cunhasse `h5` na terceira
	// entrada, a reconstrução por dobra sobre as mesmas entradas daria outro token e o
	// `prompt_hash` divergiria no replay.
	ErrHandleBadToken = errors.New("agentruntime: token de handle malformado ou fora de sequencia")
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

	mu    sync.Mutex
	byID  map[string]Handle
	ordem []string
	seq   int
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

// Seal sela o valor untrusted no espaço de handles do run e devolve o [Handle] opaco que o
// referencia, com os factos de nível-esquema que o nó cunha. A quarentena NÃO PROMOVE: o que lá
// fica é sempre untrusted (ver [quarantineOrigin]).
//
// É o caminho de ENTRADA de um corpo que ainda não está no armazém — um `tool_result`, um
// `plan_input`. O tecto de [MaxToolResultBytes] é imposto AQUI, antes de o conteúdo chegar ao
// armazém: é onde a recusa ainda é barata e onde a causa é nomeável.
//
// O TOKEN E OS FACTOS são cunhados pelo ARMAZÉM ([QuarantineStore.SealQuarantined]), que é quem
// sabe o digest, o ordinal durável, o tamanho e a forma. O que este método passa são os factos que
// o chamador DECLARA (o tipo de media); o que indexa são os que o armazém AFIRMA. O índice confere
// que o ordinal é o da posição desta entrada na sequência.
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
	token, digest, cunhados, err := q.store.SealQuarantined(ctx, q.subject, q.runID, payload, facts)
	if err != nil {
		return Handle{}, err
	}
	return q.Adopt(token, digest, origem, cunhados)
}

// Adopt registra na quarentena conteúdo que JÁ ESTÁ no armazém, pelo seu token e digest, sem o
// reescrever e sem o ler. É o caminho por que a quarentena de um run se RECONSTRÓI a partir dos
// eventos duráveis de proveniência — a mesma derivação que corre ao vivo.
//
// QUEM PODE CHAMAR ISTO É A FRONTEIRA DE CONFIANÇA. Adoptar um token é afirmar que ele veio de
// código do nó; um token que apareça no VALOR de uma tool (um documento lido, uma resposta remota)
// nunca deve chegar aqui — é a regra que o `media.go:26-32` escreveu para a `media_view` e que vale
// igual aqui. A condição (b) do AOS-550 — o par `(run, digest)` num evento do stream do run — é
// imposta pelo armazém, a jusante e em separado.
func (q *Quarantine) Adopt(token, digest string, origin taint.Origin, facts HandleFacts) (Handle, error) {
	if NormalizeRouteProfileDigest(digest) == "" {
		return Handle{}, fmt.Errorf("%w: %q", ErrHandleBadDigest, digest)
	}
	ordinal, ok := HandleTokenOrdinal(token)
	if !ok {
		return Handle{}, fmt.Errorf("%w: %q", ErrHandleBadToken, token)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if ordinal != q.seq+1 {
		return Handle{}, fmt.Errorf("%w: %q na posicao %d", ErrHandleBadToken, token, q.seq+1)
	}
	q.seq++
	h := Handle{id: token, digest: digest, origin: quarantineOrigin(origin), facts: facts}
	q.byID[h.id] = h
	q.ordem = append(q.ordem, h.id)
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

// Lookup devolve o handle que o TOKEN referencia — o que resolve uma referência
// [HandleRefPrefix] que o modelo emitiu num argumento. Um token que esta quarentena não cunhou
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
	out := make([]Handle, 0, len(q.ordem))
	for _, id := range q.ordem {
		out = append(out, q.byID[id])
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
//
// AINDA SEM CHAMADOR DE PRODUÇÃO, E O QUE LHE FALTA (AOS-551): a implementação de produção do
// [QuarantineStore] exige saber QUE TOOL e QUE PASSO estão a selar — entram na proveniência e no
// nome que o nó cunha —, e uma quarentena construída sem eles recusa selar. Quem ligar a separação
// de planos tem de construir a quarentena com a tool e o passo do turno que a produziu; não é um
// parâmetro desta função porque a tool e o passo mudam de segmento para segmento.
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
		h, err := q.Seal(ctx, taint.FromOrigin(quarantineOrigin(seg.Origin), seg.Content),
			HandleFacts{Bytes: int64(len(seg.Content)), Name: string(seg.Kind) + "-" + strconv.Itoa(i+1)})
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
