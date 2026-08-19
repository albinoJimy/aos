package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// readApprovalStream lê a partição de aprovações tratando o stream AINDA INEXISTENTE como
// VAZIO. Num nó fresco (nenhuma aprovação alguma vez emitida) o Event Store devolve
// ErrStreamNotFound; propagá-lo faria toda a consulta de aprovações falhar em vez de
// responder "não há nada" — que é a verdade.
func readApprovalStream(ctx context.Context, store approvalAppendReader) ([]eventstore.Event, error) {
	events, err := store.Read(ctx, approvalStream, 0)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return events, nil
}

// ---------------------------------------------------------------------------
// AOS-021 — ApprovalStore DURÁVEL sobre o Event Store
// ---------------------------------------------------------------------------
//
// PORQUÊ NÃO IN-MEMORY: os grants vivem no caminho de AUTORIZAÇÃO. Uma store em memória
// perde-os num restart ou failover — e, entre o POST /approve e a retoma, isso obriga a
// repetir a cerimónia four-eyes inteira (duas assinaturas, dois challenges, attestation).
// O efeito é fail-closed (nada executa indevidamente), mas é uma avaria operacional real.
//
// COMO SE OBTÉM O USO-ÚNICO ATÓMICO NUM LOG APPEND-ONLY: não se apaga nada — RECLAMA-SE.
// O consumo é um APPEND com idempotency_key derivada do id do grant; o dedup do Event
// Store (StatusDuplicate) é o primitivo atómico: o primeiro consumo COMMITA, todos os
// seguintes vêm duplicados. É o mesmo mecanismo em que o step-ledger assenta o
// "already-applied precede qualquer efeito".

// Tipos de evento e partição da governança de aprovações.
const (
	approvalStream            = "gov.approvals"
	approvalGrantedEventType  = "approval.granted"
	approvalConsumedEventType = "approval.consumed"
	// approvalRunID é o "run" sintético que, com o StepID, forma a idempotency_key
	// (run_id + ":" + step_id) — o âmbito de dedup dos grants.
	approvalRunID = "approval"
)

// approvalAppendReader é o subconjunto do Event Store de que a store depende.
// *[eventstore.Store] satisfá-lo.
type approvalAppendReader interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// grantPayload é a forma serializada de um [ApprovalGrant] no log. A Preview vai em
// base64 pelo JSON (é []byte); não há segredos aqui — nem assinaturas nem challenges,
// só a amarra à acção, a atribuição e a frescura.
type grantPayload struct {
	ID          string   `json:"id"`
	Preview     []byte   `json:"preview"`
	Approvers   []string `json:"approvers"`
	DualControl bool     `json:"dual_control"`
	ExpiresAt   string   `json:"expires_at"` // RFC3339
}

// eventStoreApprovalStore é a [ApprovalStore] DURÁVEL. Sobrevive a restart/failover: os
// grants são factos append-only no Event Store, e o consumo é uma reclamação idempotente.
type eventStoreApprovalStore struct {
	store approvalAppendReader
}

// NewEventStoreApprovalStore constrói a store durável sobre o Event Store do nó. É a
// implementação de PRODUÇÃO — [NewMemApprovalStore] fica para testes/dev.
func NewEventStoreApprovalStore(store approvalAppendReader) (ApprovalStore, error) {
	if store == nil {
		return nil, ErrNilApprovalStore
	}
	return &eventStoreApprovalStore{store: store}, nil
}

// Put persiste o grant como facto append-only. A idempotency_key é derivada do id do
// grant, pelo que reemitir o MESMO id não duplica o registo (o Event Store dedup).
func (s *eventStoreApprovalStore) Put(ctx context.Context, g ApprovalGrant) error {
	body, err := json.Marshal(grantPayload{
		ID:          g.ID,
		Preview:     g.Preview,
		Approvers:   g.Approvers,
		DualControl: g.DualControl,
		ExpiresAt:   g.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = s.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalGrantedEventType,
		Payload: body,
		RunID:   approvalRunID,
		StepID:  "grant-" + g.ID,
	})
	return err
}

// Consume reclama o grant de forma ATÓMICA e devolve-o. A atomicidade vem do dedup do
// Event Store: o APPEND da reclamação ("approval.consumed", chave derivada do id) COMMITA
// à primeira e devolve StatusDuplicate em todas as seguintes — logo uma aprovação destrava
// no máximo UMA execução, mesmo com dois pedidos concorrentes ou após um failover.
//
// ORDEM DELIBERADA — reclamar ANTES de ler: se a leitura do grant falhasse depois da
// reclamação, o grant fica queimado (é preciso nova aprovação). É o lado seguro do
// trade-off, coerente com [ApprovalBroker.VerifyApproval]: uma tentativa de desvio ou uma
// avaria nunca deixam o grant disponível para nova tentativa.
func (s *eventStoreApprovalStore) Consume(ctx context.Context, id string) (ApprovalGrant, bool, error) {
	if id == "" {
		return ApprovalGrant{}, false, nil
	}
	claim, err := s.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalConsumedEventType,
		Payload: json.RawMessage(`{"id":` + quoteJSON(id) + `}`),
		RunID:   approvalRunID,
		StepID:  "used-" + id,
	})
	if err != nil {
		return ApprovalGrant{}, false, err
	}
	if claim.Status == eventstore.StatusDuplicate {
		// Já reclamado antes: uso-único cumprido.
		return ApprovalGrant{}, false, nil
	}
	// Reclamado por nós: resolver o grant no log.
	g, ok, err := s.lookup(ctx, id)
	if err != nil || !ok {
		return ApprovalGrant{}, false, err
	}
	return g, true, nil
}

// lookup lê a partição de aprovações e devolve o grant com este id. O reference impl
// varre o stream (a partição é pequena — um evento por aprovação); um índice por id é
// optimização, não correcção.
func (s *eventStoreApprovalStore) lookup(ctx context.Context, id string) (ApprovalGrant, bool, error) {
	events, err := readApprovalStream(ctx, s.store)
	if err != nil {
		return ApprovalGrant{}, false, err
	}
	want := "grant-" + id
	for i := len(events) - 1; i >= 0; i-- { // do mais recente para trás
		ev := events[i]
		if ev.Type != approvalGrantedEventType || ev.StepID != want {
			continue
		}
		var p grantPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return ApprovalGrant{}, false, err
		}
		exp, perr := time.Parse(time.RFC3339Nano, p.ExpiresAt)
		if perr != nil {
			return ApprovalGrant{}, false, fmt.Errorf("integration: grant com expires_at invalido: %w", perr)
		}
		return ApprovalGrant{
			ID: p.ID, Preview: p.Preview, Approvers: p.Approvers,
			DualControl: p.DualControl, ExpiresAt: exp,
		}, true, nil
	}
	return ApprovalGrant{}, false, nil
}

// FindUnconsumedByPreview varre a partição e devolve o id de um grant emitido cuja
// preview coincida E que ainda NÃO tenha sido reclamado. É só uma PISTA para a retoma
// («há aprovação para esta acção?»): a exclusão atómica vive no [Consume], pelo que uma
// pista obsoleta (consumo concorrente entretanto) resolve-se fail-closed lá.
func (s *eventStoreApprovalStore) FindUnconsumedByPreview(ctx context.Context, preview []byte) (string, bool, error) {
	if len(preview) == 0 {
		return "", false, nil
	}
	events, err := readApprovalStream(ctx, s.store)
	if err != nil {
		return "", false, err
	}
	// Primeiro os consumidos, para não propor um grant já reclamado.
	consumidos := make(map[string]struct{})
	for _, ev := range events {
		if ev.Type == approvalConsumedEventType {
			consumidos[ev.StepID] = struct{}{} // "used-<id>"
		}
	}
	for i := len(events) - 1; i >= 0; i-- { // do mais recente para trás
		ev := events[i]
		if ev.Type != approvalGrantedEventType {
			continue
		}
		var p grantPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue // um registo ilegível não é uma aprovação utilizável
		}
		if !constantTimeEqualBytes(p.Preview, preview) {
			continue
		}
		if _, usado := consumidos["used-"+p.ID]; usado {
			continue
		}
		return p.ID, true, nil
	}
	return "", false, nil
}

// quoteJSON escapa uma string para interpolação segura num literal JSON.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

var _ ApprovalStore = (*eventStoreApprovalStore)(nil)

// ---------------------------------------------------------------------------
// AOS-021 — Registo DURÁVEL de aprovações PENDENTES
// ---------------------------------------------------------------------------
//
// O pendente é o que o operador VÊ para decidir (por polling). Tal como os grants, vive
// no caminho de aprovação: se evaporasse num restart, o operador deixaria de saber o que
// aprovar e o run ficaria suspenso até expirar — fail-closed, mas uma avaria operacional
// real. Por isso é um facto append-only no MESMO stream.

const (
	approvalPendingEventType = "approval.pending"
	approvalExpiredEventType = "approval.expired"
	// approvalDecidedEventType — o pendente foi RESPONDIDO por um humano (AOS-263 parte 3).
	// É um facto DISTINTO de [approvalExpiredEventType] e não um sinónimo dele: expirar é «o
	// TTL passou sem ninguém decidir», decidir é «um principal VERIFICADO respondeu». Reusar
	// o evento de expiração para retirar da lista um pendente respondido poria no log
	// append-only — que ninguém reescreve — um facto falso sobre uma decisão humana, e o
	// varrimento passaria a anunciar «expirado sem decisao» sobre algo decidido.
	approvalDecidedEventType = "approval.decided"
)

// ---------------------------------------------------------------------------
// AOS-263 — DISCRIMINADOR DE TIPO do pendente
// ---------------------------------------------------------------------------
//
// A maquinaria de pendentes nasceu moldada a UMA decisão humana: aprovar uma tool call
// escalada, amarrada ao DIGEST DA PREVIEW (WYSIWYS). O prompt de exaustão de orçamento
// (AOS-263) é uma segunda decisão humana com a MESMA mecânica de suspensão — o run pára em
// `waiting_on_human` pelo mesmo runGate — mas SEM preview: a sua amarra é (run, limiar,
// montante consumido). Em vez de abrir um caminho paralelo (recusado pelo dono: daria uma
// decisão humana mais fraca que o four-eyes já entregue), o registo ganha um TIPO.
//
// # COMPATIBILIDADE COM O QUE JÁ ESTÁ SELADO
//
// Os pendentes já escritos no log são IMUTÁVEIS (append-only, selados no WORM) e não têm
// campo `kind`. A regra que os mantém legíveis é uma só, e está em [PendingKind.Resolved]:
// a AUSÊNCIA do discriminador É o tipo aprovação. Ela vale nos dois sentidos —
//
//   - LEITURA: um registo antigo desserializa com Kind vazio e é normalizado para
//     [PendingKindApproval] antes de chegar a qualquer chamador;
//   - ESCRITA: [PendingApprovals.Put] volta a OMITIR o campo quando o tipo é o default,
//     pelo que um pendente de aprovação escrito hoje é byte-idêntico ao de antes de
//     AOS-263 — e um binário anterior continua a lê-lo (e a um de exaustão ignora-lhe o
//     campo desconhecido, mas nunca o vê: a chave de idempotência é de outro âmbito).
type PendingKind string

const (
	// PendingKindApproval — aprovação de uma tool call ESCALADA (AOS-021). É o DEFAULT e o
	// tipo de TODOS os registos anteriores a AOS-263.
	PendingKindApproval PendingKind = "approval"
	// PendingKindExhaustion — prompt de EXAUSTÃO de orçamento (AOS-263). NÃO tem preview:
	// não há efeito exibido para assinar, a decisão é sobre o run (continuar/parar) e a
	// amarra é (run, limiar, montante consumido).
	PendingKindExhaustion PendingKind = "exhaustion"
)

var (
	// ErrUnknownPendingKind — pediu-se o registo de um pendente de um tipo que este binário
	// não conhece. FAIL-CLOSED na escrita: um tipo desconhecido no log seria uma decisão
	// humana que ninguém sabe apresentar nem consumir.
	ErrUnknownPendingKind = errors.New("integration: tipo de pendente desconhecido")
	// ErrExhaustionWithPreview — um pendente de exaustão trazia preview. RECUSADO: a preview
	// é a amarra do tipo APROVAÇÃO, e um registo de exaustão que a transportasse podia ser
	// «decidido» por um grant emitido para outra coisa, ou renderizado como uma aprovação
	// numa superfície que só olha para a preview. É confusão de tipo, e é fatal.
	ErrExhaustionWithPreview = errors.New("integration: pendente de exaustao nao pode transportar preview")
	// ErrExhaustionPromptColide — o registo de um prompt de EXAUSTÃO caiu na deduplicação do
	// Event Store: já existe um facto com esta chave (tipo, run, passo).
	//
	// Porque é ERRO aqui e no tipo aprovação NÃO: re-escalar a MESMA tool call é normal e a
	// deduplicação é o comportamento certo (o pendente já lá está e é o mesmo). Uma segunda
	// PERGUNTA de exaustão com a chave de uma anterior é outra coisa — a anterior já saiu da
	// lista (expirou ou foi decidida), pelo que a nova NÃO aparece a ninguém: o duplicado é
	// engolido em silêncio, o facto de retirada anterior continua a valer, e o run fica
	// suspenso com uma pergunta INVISÍVEL que nenhuma rota consegue decidir nem expirar. Um
	// erro nomeado faz o prompt falhar ALTO (o run aborta como falhado, visível) em vez de
	// ficar preso sem via.
	ErrExhaustionPromptColide = errors.New("integration: prompt de exaustao com chave ja usada — o duplicado seria engolido pela deduplicacao e a pergunta ficaria invisivel")
)

// Resolved devolve o tipo EFECTIVO: o campo vazio — a forma de todos os registos selados
// antes de AOS-263 — é [PendingKindApproval]. Toda a leitura passa por aqui.
func (k PendingKind) Resolved() PendingKind {
	if k == "" {
		return PendingKindApproval
	}
	return k
}

// pendingKey compõe a chave de idempotência de um pendente com ÂMBITO POR TIPO: dois
// pendentes de tipos DIFERENTES sobre o mesmo (run, step) são decisões diferentes e não se
// podem deduplicar nem expirar um ao outro.
//
// O tipo DEFAULT não contribui com NADA para a chave. É deliberado e é a segunda metade da
// compatibilidade: as chaves dos pendentes de aprovação — as que já estão no log — mantêm-se
// exactamente `pending-<run>-<step>` / `expired-<run>-<step>`, pelo que a deduplicação e a
// expiração de um pendente escrito ANTES de AOS-263 continuam a resolver depois dele.
func pendingKey(prefix string, kind PendingKind, runID, stepID string) string {
	if kind.Resolved() == PendingKindApproval {
		return prefix + runID + "-" + stepID
	}
	return prefix + "kind=" + string(kind.Resolved()) + "|" + runID + "-" + stepID
}

// chaveDeDeduplicacao é a chave EM MEMÓRIA com que as listagens colapsam registos repetidos
// do mesmo pendente. NÃO é a chave durável ([pendingKey]) e não pode ser: a durável tem de
// ficar byte-idêntica à de antes de AOS-263 no tipo default (`<run>-<step>`), e essa forma é
// AMBÍGUA — `run="a-b",step="c"` e `run="a",step="b-c"` concatenam para a mesma string.
//
// Na durável a ambiguidade é tolerada porque a chave nasceu assim e reescrevê-la partiria a
// deduplicação e a expiração de tudo o que já está no log. Aqui não há nada selado a
// preservar, e a ambiguidade teria um custo real: [PendingApprovals.ListExpirable] percorre
// TODOS os runs, pelo que dois pendentes distintos com concatenações coincidentes
// colapsariam um no outro e um deles NUNCA seria devolvido ao varrimento — logo nunca
// expiraria. Length-prefix ⇒ injectiva, pela mesma razão que o payload assinado de AOS-263
// o é.
func chaveDeDeduplicacao(kind PendingKind, runID, stepID string) string {
	return fmt.Sprintf("%s|%d:%s|%d:%s", kind.Resolved(), len(runID), runID, len(stepID), stepID)
}

// decodePending desserializa um pendente do log e NORMALIZA o discriminador. É o único
// ponto por onde os registos entram: nenhum chamador vê um Kind vazio, e por isso nenhum
// chamador tem de conhecer a regra da ausência.
func decodePending(payload []byte) (PendingRecord, bool) {
	var rec PendingRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		return PendingRecord{}, false
	}
	rec.Kind = rec.Kind.Resolved()
	return rec, true
}

// PendingRecord é uma decisão humana à espera de resposta. No tipo [PendingKindApproval]
// espelha [agentruntime.PendingApproval] com a forma serializável (a preview vai em base64
// pelo JSON). NÃO transporta o Input da tool — o payload pode conter dados sensíveis e não
// deve atravessar a superfície de administração; a amarra é a preview, que já o cobre
// por hash.
//
// O CHAMADOR TEM DE COMUTAR NO [PendingRecord.Kind]. As listagens devolvem TODOS os tipos
// (a store não esconde registos), e um tipo que a superfície não saiba renderizar deve ser
// OMITIDO — nunca apresentado como aprovação: um pendente de exaustão tem preview vazia, e
// exibi-lo como uma aprovação daria ao operador uma assinatura sobre coisa nenhuma.
type PendingRecord struct {
	// Kind é o TIPO da decisão (AOS-263). VAZIO ⇒ [PendingKindApproval] (ver
	// [PendingKind.Resolved]): é assim que os registos já selados no WORM, escritos sem este
	// campo, continuam a ler-se correctamente.
	Kind           PendingKind `json:"kind,omitempty"`
	RunID          string      `json:"run_id"`
	StepID         string      `json:"step_id"`
	Turn           int         `json:"turn"`
	ToolID         string      `json:"tool_id"`
	Capability     string      `json:"capability"`
	ResourceType   string      `json:"resource_type,omitempty"`
	ResourceValue  string      `json:"resource_value,omitempty"`
	ResourceRegion string      `json:"resource_region,omitempty"`
	Preview        []byte      `json:"preview"`
	// Threshold/ConsumedTokens/LimitTokens são a AMARRA do pendente de EXAUSTÃO — o que
	// substitui a preview no tipo que não tem efeito exibido para assinar. Vêm do aviso de
	// burn-down de AOS-262 (a fonte do sinal); NÃO se recalcula consumo aqui. Ausentes (zero)
	// no tipo aprovação.
	Threshold      float64 `json:"threshold,omitempty"`
	ConsumedTokens int64   `json:"consumed_tokens,omitempty"`
	LimitTokens    int64   `json:"limit_tokens,omitempty"`
	// ConsumedCostMicroUSD/LimitCostMicroUSD são a MESMA amarra na dimensão $ (micro-USD
	// INTEIRO), e existem porque sem elas a pergunta humana durável podia CONTRADIZER a razão
	// da paragem: com `AOS_BUDGET_MAX_COST_MICRO_USD` configurada é a dimensão de dólares que
	// nega, e um registo que só sabe falar de tokens apresentava ao operador «5000 de 1000000
	// tokens consumidos» ao lado de uma fracção de 1.00 — auto-contraditório e sem uma única
	// menção à grandeza que bloqueou o run. Ausentes (zero) quando não há tecto em $ em vigor,
	// que é o estado em que a dimensão é medida mas não decide.
	ConsumedCostMicroUSD int64 `json:"consumed_cost_micro_usd,omitempty"`
	LimitCostMicroUSD    int64 `json:"limit_cost_micro_usd,omitempty"`
	// Reason é a RAZÃO da pergunta nas palavras de quem a levantou — e, no caso do tecto
	// atingido, a razão ATRIBUÍVEL que a admissão do turno de modelo produziu, com os dois
	// pares (tokens, micro-USD) já lá dentro. Sem este campo a razão existia apenas no log do
	// processo, que NÃO é o canal que a decisão assinada lê: o operador respondia `continue`
	// sobre a grandeza errada, o run era re-hospedado e imediatamente re-negado pelo mesmo
	// tecto — um ciclo de decisões humanas sobre um número que não é o que decide. Rótulo de
	// diagnóstico legível, nunca segredo. Ausente no tipo aprovação.
	Reason string `json:"reason,omitempty"`
	// CreatedAt é o instante em que a acção foi escalada (RFC3339). É a âncora do TTL:
	// passados [DefaultApprovalTTL] sem decisão, o pendente EXPIRA e o run deixa de
	// esperar. Carimbado por quem regista (o nó tem o relógio); vazio ⇒ sem expiração
	// computável, o pendente fica à espera de decisão explícita (fail-safe: nunca expira
	// sozinho um pendente cuja idade não se sabe).
	CreatedAt string `json:"created_at,omitempty"`
}

// PendingApprovals é o registo durável de aprovações pendentes.
type PendingApprovals struct {
	store approvalAppendReader
}

// NewPendingApprovals constrói o registo sobre o Event Store do nó.
func NewPendingApprovals(store approvalAppendReader) (*PendingApprovals, error) {
	if store == nil {
		return nil, ErrNilApprovalStore
	}
	return &PendingApprovals{store: store}, nil
}

// Put regista um pendente. A idempotency_key deriva de (tipo, run, step): re-escalar o
// MESMO passo não duplica o registo, e um pendente de EXAUSTÃO não colide com a aprovação
// do mesmo passo (ver [pendingKey]).
//
// FAIL-CLOSED na escrita: um tipo desconhecido ([ErrUnknownPendingKind]) ou um pendente de
// exaustão com preview ([ErrExhaustionWithPreview]) são RECUSADOS — nada entra no log que a
// leitura não saiba classificar sem ambiguidade.
//
// O tipo default é escrito com o campo AUSENTE (ver [PendingKind]): um pendente de aprovação
// gravado hoje é byte-idêntico ao de antes de AOS-263.
func (p *PendingApprovals) Put(ctx context.Context, rec PendingRecord) error {
	kind := rec.Kind.Resolved()
	switch kind {
	case PendingKindApproval:
		rec.Kind = "" // o default NÃO se escreve — é a ausência que o define
	case PendingKindExhaustion:
		if len(rec.Preview) > 0 {
			return fmt.Errorf("%w: run=%q step=%q", ErrExhaustionWithPreview, rec.RunID, rec.StepID)
		}
		rec.Kind = PendingKindExhaustion
	default:
		return fmt.Errorf("%w: %q", ErrUnknownPendingKind, string(kind))
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// ENCARNAÇÃO. Sem isto, um pendente que já expirou e volta a escalar é engolido pela
	// deduplicação do Event Store — e o run fica à espera de um humano sem nada na lista dele.
	events, err := readApprovalStream(ctx, p.store)
	if err != nil {
		return err
	}
	ger := geracaoDe(events, kind, rec.RunID, rec.StepID)
	res, err := p.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalPendingEventType,
		Payload: body,
		RunID:   approvalRunID,
		StepID:  chaveDeGeracao("pending-", kind, rec.RunID, rec.StepID, ger),
	})
	if err != nil {
		return err
	}
	// A deduplicação do Event Store é SILENCIOSA por desenho (o duplicado "ganha" e devolve
	// StatusDuplicate sem erro) — o que é certo para o tipo aprovação e fatal para o de
	// exaustão (ver [ErrExhaustionPromptColide]).
	if kind == PendingKindExhaustion && res.Status == eventstore.StatusDuplicate {
		return fmt.Errorf("%w: run=%q step=%q", ErrExhaustionPromptColide, rec.RunID, rec.StepID)
	}
	return nil
}

// ListForRun devolve os pendentes de um run que ainda NÃO foram decididos. É o que a
// superfície de administração expõe ao operador — «o que falta decidir».
//
// Devolve TODOS os tipos, com o [PendingRecord.Kind] já normalizado: a store não esconde
// registos, e é o chamador que comuta no tipo e omite o que não sabe renderizar (ver
// [PendingRecord]).
//
// O filtro «já decidida» é por PREVIEW (existe grant emitido para ela) e por isso só morde
// no tipo aprovação: [constantTimeEqualBytes] devolve false para digests vazios, pelo que um
// pendente de exaustão — que não tem preview — nunca é dado por decidido por um grant
// alheio. As saídas de um pendente de exaustão da lista são a sua EXPIRAÇÃO (ver
// [PendingApprovals.ExpireKind]) e a DECISÃO humana (ver [PendingApprovals.Decide]).
func (p *PendingApprovals) ListForRun(ctx context.Context, runID string) ([]PendingRecord, error) {
	events, err := readApprovalStream(ctx, p.store)
	if err != nil {
		return nil, err
	}
	// Previews com grant emitido, e pendentes RETIRADOS (expirados ou decididos), deixaram de
	// estar "à espera de decisão" — não devem aparecer ao operador como coisas por fazer.
	decididas := make([][]byte, 0, 4)
	retirados := make(map[string]struct{})
	for _, ev := range events {
		switch ev.Type {
		case approvalGrantedEventType:
			var g grantPayload
			if err := json.Unmarshal(ev.Payload, &g); err == nil {
				decididas = append(decididas, g.Preview)
			}
		case approvalExpiredEventType, approvalDecidedEventType:
			retirados[ev.StepID] = struct{}{}
		}
	}
	var out []PendingRecord
	vistos := make(map[string]struct{})
	for _, ev := range events {
		if ev.Type != approvalPendingEventType {
			continue
		}
		rec, ok := decodePending(ev.Payload)
		if !ok {
			continue
		}
		if rec.RunID != runID {
			continue
		}
		if jaRetiradoDoEvento(retirados, ev.StepID) {
			continue
		}
		jaDecidida := false
		for _, d := range decididas {
			if constantTimeEqualBytes(d, rec.Preview) {
				jaDecidida = true
				break
			}
		}
		if jaDecidida {
			continue
		}
		// A chave de deduplicação inclui o TIPO: dentro do mesmo run, a aprovação e o prompt
		// de exaustão do mesmo passo são duas decisões, não uma repetida.
		chave := chaveDeDeduplicacao(rec.Kind, rec.RunID, rec.StepID)
		if _, dup := vistos[chave]; dup {
			continue
		}
		vistos[chave] = struct{}{}
		out = append(out, rec)
	}
	return out, nil
}

// Expire marca uma APROVAÇÃO pendente como EXPIRADA: passou o TTL sem decisão humana. É um
// facto append-only — o pendente deixa de constar de [PendingApprovals.ListForRun] e o run
// pode deixar de esperar. NÃO aprova nada: a acção continua por autorizar, e uma retoma
// posterior encontra-a sem grant e vê-a negada.
//
// Mantém a assinatura (e a chave `expired-<run>-<step>`) de antes de AOS-263 — é o tipo
// default. Para os outros tipos, [PendingApprovals.ExpireKind].
func (p *PendingApprovals) Expire(ctx context.Context, runID, stepID string) error {
	return p.ExpireKind(ctx, PendingKindApproval, runID, stepID)
}

// ExpireKind é o [PendingApprovals.Expire] com o TIPO explícito. A expiração é POR TIPO
// porque a chave é por tipo: expirar a aprovação de um passo NÃO expira o prompt de exaustão
// do mesmo passo, e vice-versa — duas decisões distintas não se apagam uma à outra.
//
// Como Expire, nunca autoriza nada: seja qual for o tipo, expirar só retira o registo da
// lista do operador.
func (p *PendingApprovals) ExpireKind(ctx context.Context, kind PendingKind, runID, stepID string) error {
	// Mesma regra do [PendingApprovals.Put]: o tipo default não se escreve. O facto de
	// expiração de uma aprovação fica byte-idêntico ao de antes de AOS-263 (chave E payload).
	discriminador := ""
	if kind.Resolved() != PendingKindApproval {
		discriminador = `"kind":` + quoteJSON(string(kind.Resolved())) + `,`
	}
	// A expiração retira a encarnação EM CURSO, e por isso precisa de saber qual é.
	eventosParaGeracao, gerr := readApprovalStream(ctx, p.store)
	if gerr != nil {
		return gerr
	}
	_, err := p.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalExpiredEventType,
		Payload: json.RawMessage(`{` + discriminador + `"run_id":` + quoteJSON(runID) + `,"step_id":` + quoteJSON(stepID) + `}`),
		RunID:   approvalRunID,
		StepID:  chaveDeGeracao("expired-", kind, runID, stepID, geracaoDe(eventosParaGeracao, kind, runID, stepID)),
	})
	return err
}

// Decide RETIRA um pendente da lista do operador porque um humano RESPONDEU (AOS-263 parte
// 3). É um facto append-only distinto da expiração, e a distinção é o ponto: no log fica
// registado que houve DECISÃO, de quem e qual — não «passou o tempo».
//
// NÃO É A DECISÃO. O registo de NÃO-REPÚDIO da decisão é o selo na hash-chain WORM (com o
// principal verificado, o run, o montante consumido e a razão); este facto é a contabilidade
// da LISTA DE TRABALHO — o que o operador ainda tem por responder. São duas superfícies com
// propósitos diferentes e nenhuma substitui a outra: sem o selo não há prova inviolável; sem
// este facto, uma pergunta já respondida continuaria a aparecer por responder e o varrimento
// acabaria por a «expirar», anunciando no log que ninguém decidiu.
//
// O `principal` é a identidade VERIFICADA de quem decidiu (o emissor cuja assinatura ed25519
// validou contra a pubkey pinada) e o `decision` é o identificador da opção escolhida. Ambos
// são material PÚBLICO — nunca a assinatura, nunca o nonce, nunca credencial nenhuma.
//
// IDEMPOTENTE por construção: a chave é `decided-<tipo>|<run>-<step>`, pelo que uma segunda
// decisão sobre o MESMO pendente não duplica o facto (o dedup do Event Store devolve o
// original). O que impede uma segunda decisão de ter EFEITO não é isto — é o anti-replay
// durável do nonce e o estado terminal do run.
func (p *PendingApprovals) Decide(ctx context.Context, kind PendingKind, runID, stepID, decision, principal string) error {
	// O tipo default continua a não se escrever (regra de [PendingKind]): um facto de decisão
	// sobre uma aprovação fica na forma que um binário anterior a AOS-263 também sabe ler.
	discriminador := ""
	if kind.Resolved() != PendingKindApproval {
		discriminador = `"kind":` + quoteJSON(string(kind.Resolved())) + `,`
	}
	// Idem: a decisão retira a encarnação em curso, não a primeira de sempre.
	eventosParaGeracao, gerr := readApprovalStream(ctx, p.store)
	if gerr != nil {
		return gerr
	}
	_, err := p.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type: approvalDecidedEventType,
		Payload: json.RawMessage(`{` + discriminador +
			`"run_id":` + quoteJSON(runID) +
			`,"step_id":` + quoteJSON(stepID) +
			`,"decision":` + quoteJSON(decision) +
			`,"principal":` + quoteJSON(principal) + `}`),
		RunID:  approvalRunID,
		StepID: chaveDeGeracao("decided-", kind, runID, stepID, geracaoDe(eventosParaGeracao, kind, runID, stepID)),
	})
	return err
}

// ListExpirable devolve os pendentes de TODOS os runs cuja idade excede ttl e que ainda
// não foram decididos (sem grant, sem decisão humana) nem expirados. É o que o varrimento
// periódico consome.
//
// Um pendente sem CreatedAt NUNCA é devolvido: não se expira sozinho aquilo cuja idade se
// desconhece (fail-safe — fica à espera de decisão explícita).
//
// Cobre TODOS os tipos (o [PendingRecord.Kind] vem normalizado). Quem varre tem de expirar
// com o tipo do registo ([PendingApprovals.ExpireKind]): expirar um pendente de exaustão com
// a chave do tipo default não o retiraria da lista e o varrimento re-tentaria para sempre.
func (p *PendingApprovals) ListExpirable(ctx context.Context, now time.Time, ttl time.Duration) ([]PendingRecord, error) {
	events, err := readApprovalStream(ctx, p.store)
	if err != nil {
		return nil, err
	}
	decididas := make([][]byte, 0, 4)
	retirados := make(map[string]struct{})
	for _, ev := range events {
		switch ev.Type {
		case approvalGrantedEventType:
			var g grantPayload
			if err := json.Unmarshal(ev.Payload, &g); err == nil {
				decididas = append(decididas, g.Preview)
			}
		case approvalExpiredEventType, approvalDecidedEventType:
			// "expired-<run>-<step>" / "decided-<run>-<step>" (com `kind=` nos tipos != default)
			retirados[ev.StepID] = struct{}{}
		}
	}
	var out []PendingRecord
	vistos := make(map[string]struct{})
	for _, ev := range events {
		if ev.Type != approvalPendingEventType {
			continue
		}
		rec, ok := decodePending(ev.Payload)
		if !ok || rec.CreatedAt == "" {
			continue // sem âncora de tempo ⇒ nunca expira sozinho
		}
		criado, perr := time.Parse(time.RFC3339Nano, rec.CreatedAt)
		if perr != nil || now.Sub(criado) < ttl {
			continue
		}
		if jaRetiradoDoEvento(retirados, ev.StepID) {
			continue
		}
		jaDecidida := false
		for _, d := range decididas {
			if constantTimeEqualBytes(d, rec.Preview) {
				jaDecidida = true
				break
			}
		}
		if jaDecidida {
			continue
		}
		chave := chaveDeDeduplicacao(rec.Kind, rec.RunID, rec.StepID)
		if _, dup := vistos[chave]; dup {
			continue
		}
		vistos[chave] = struct{}{}
		out = append(out, rec)
	}
	return out, nil
}

// ------------------------------------------------------------------------------------------
// GERAÇÕES DE UM PENDENTE — o que faltava para uma re-escalada voltar a ser visível.
//
// O DEFEITO, observado em produção a 2026-08-19 ao exercitar a cerimónia four-eyes: um run
// escalou, o pendente EXPIROU sem decisão (TTL 15m), o run foi retomado, a acção escalou OUTRA
// VEZ — e nada apareceu na lista do operador. O run ficou 10 minutos em `waiting_on_human` com o
// stream a mostrar «expirado» como último estado.
//
// Duas causas que se compõem, e nenhuma sozinha explicava tudo:
//
//  1. o Event Store deduplica por `idempotency_key = f(run_id, step_id)` e o [Put] usava SEMPRE a
//     mesma chave para o mesmo (run, step). O segundo anúncio era engolido em SILÊNCIO — que o
//     comentário do [Put] até declara como «certo para o tipo aprovação», e é, enquanto o
//     pendente está VIVO;
//
//  2. a verificação de «já retirado» era um CONJUNTO de chaves, sem ordem (o antigo
//     `jaRetirado`, hoje substituído por [jaRetiradoDoEvento]). Uma vez expirado, qualquer pendente com
//     aquela chave ficava escondido para sempre — logo, mesmo que (1) fosse resolvido, a
//     listagem continuaria a não o mostrar.
//
// A CORRECÇÃO é dar identidade à ENCARNAÇÃO: (run, step, geração), onde a geração é quantas vezes
// aquele pendente já foi retirado. A primeira encarnação mantém a chave BYTE-IDÊNTICA à de hoje —
// o comentário de [chaveDeDeduplicacao] exige-o, e reescrever a durável partiria a deduplicação e
// a expiração de tudo o que já está no log.
// ------------------------------------------------------------------------------------------

// geracaoDe conta quantas vezes este (kind, run, step) já foi RETIRADO — por expiração ou por
// decisão. É esse número que identifica a encarnação em curso.
func geracaoDe(events []eventstore.Event, kind PendingKind, runID, stepID string) int {
	suf := pendingKey("", kind, runID, stepID)
	n := 0
	for _, ev := range events {
		if ev.Type != approvalExpiredEventType && ev.Type != approvalDecidedEventType {
			continue
		}
		for _, p := range []string{"expired-", "decided-"} {
			if ev.StepID == p+suf || strings.HasPrefix(ev.StepID, p+suf+"#") {
				n++
			}
		}
	}
	return n
}

// chaveDeGeracao é [pendingKey] com a encarnação. A geração ZERO não leva sufixo — é o que
// mantém o log existente legível pelas mesmas regras.
func chaveDeGeracao(prefix string, kind PendingKind, runID, stepID string, ger int) string {
	k := pendingKey(prefix, kind, runID, stepID)
	if ger == 0 {
		return k
	}
	return k + "#" + strconv.Itoa(ger)
}

// jaRetiradoDoEvento decide se ESTE evento de pendente já foi retirado, derivando as duas chaves
// de saída do StepID do PRÓPRIO evento.
//
// São DUAS saídas e não uma: a EXPIRAÇÃO (o TTL passou sem decisão) e a DECISÃO humana
// (AOS-263 parte 3). Ambas são factos append-only, e os prefixos `expired-`/`decided-` tornam-nas
// inconfundíveis dentro do mesmo conjunto — pelo que um só mapa serve as duas leituras sem que
// uma possa mascarar a outra.
//
// A verificação vive AQUI, e não em linha, para que as duas listagens não possam divergir: uma
// que conhecesse a expiração mas não a decisão devolveria ao varrimento um pendente já
// respondido, e o log ganharia um «expirado sem decisao» sobre uma decisão tomada.
//
// É o que faz a correspondência ser POR ENCARNAÇÃO sem que a listagem precise de saber o que é
// uma geração: a expiração de `pending-X` produz `expired-X`, e a de `pending-X#1` produz
// `expired-X#1`. Uma não pode esconder a outra.
func jaRetiradoDoEvento(retirados map[string]struct{}, pendingStepID string) bool {
	suf := strings.TrimPrefix(pendingStepID, "pending-")
	if _, ja := retirados["expired-"+suf]; ja {
		return true
	}
	_, ja := retirados["decided-"+suf]
	return ja
}
