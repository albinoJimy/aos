package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

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
	events, err := s.store.Read(ctx, approvalStream, 0)
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
	events, err := s.store.Read(ctx, approvalStream, 0)
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
)

// PendingRecord é uma aprovação à espera de decisão humana. Espelha
// [agentruntime.PendingApproval] com a forma serializável (a preview vai em base64 pelo
// JSON). NÃO transporta o Input da tool — o payload pode conter dados sensíveis e não
// deve atravessar a superfície de administração; a amarra é a preview, que já o cobre
// por hash.
type PendingRecord struct {
	RunID          string `json:"run_id"`
	StepID         string `json:"step_id"`
	Turn           int    `json:"turn"`
	ToolID         string `json:"tool_id"`
	Capability     string `json:"capability"`
	ResourceType   string `json:"resource_type,omitempty"`
	ResourceValue  string `json:"resource_value,omitempty"`
	ResourceRegion string `json:"resource_region,omitempty"`
	Preview        []byte `json:"preview"`
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

// Put regista um pendente. A idempotency_key deriva de (run, step): re-escalar o MESMO
// passo não duplica o registo.
func (p *PendingApprovals) Put(ctx context.Context, rec PendingRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = p.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalPendingEventType,
		Payload: body,
		RunID:   approvalRunID,
		StepID:  "pending-" + rec.RunID + "-" + rec.StepID,
	})
	return err
}

// ListForRun devolve os pendentes de um run que ainda NÃO foram aprovados (não existe
// grant emitido para a sua preview). É o que a superfície de administração expõe ao
// operador — «o que falta decidir».
func (p *PendingApprovals) ListForRun(ctx context.Context, runID string) ([]PendingRecord, error) {
	events, err := p.store.Read(ctx, approvalStream, 0)
	if err != nil {
		return nil, err
	}
	// Previews com grant emitido, e pendentes EXPIRADOS, deixaram de estar "à espera de
	// decisão" — não devem aparecer ao operador como coisas por fazer.
	decididas := make([][]byte, 0, 4)
	expirados := make(map[string]struct{})
	for _, ev := range events {
		switch ev.Type {
		case approvalGrantedEventType:
			var g grantPayload
			if err := json.Unmarshal(ev.Payload, &g); err == nil {
				decididas = append(decididas, g.Preview)
			}
		case approvalExpiredEventType:
			expirados[ev.StepID] = struct{}{}
		}
	}
	var out []PendingRecord
	vistos := make(map[string]struct{})
	for _, ev := range events {
		if ev.Type != approvalPendingEventType {
			continue
		}
		var rec PendingRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		if rec.RunID != runID {
			continue
		}
		if _, ja := expirados["expired-"+rec.RunID+"-"+rec.StepID]; ja {
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
		chave := rec.StepID
		if _, dup := vistos[chave]; dup {
			continue
		}
		vistos[chave] = struct{}{}
		out = append(out, rec)
	}
	return out, nil
}

// Expire marca um pendente como EXPIRADO: passou o TTL sem decisão humana. É um facto
// append-only — o pendente deixa de constar de [PendingApprovals.ListForRun] e o run pode
// deixar de esperar. NÃO aprova nada: a acção continua por autorizar, e uma retoma
// posterior encontra-a sem grant e vê-a negada.
func (p *PendingApprovals) Expire(ctx context.Context, runID, stepID string) error {
	_, err := p.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalExpiredEventType,
		Payload: json.RawMessage(`{"run_id":` + quoteJSON(runID) + `,"step_id":` + quoteJSON(stepID) + `}`),
		RunID:   approvalRunID,
		StepID:  "expired-" + runID + "-" + stepID,
	})
	return err
}

// ListExpirable devolve os pendentes de TODOS os runs cuja idade excede ttl e que ainda
// não foram decididos (sem grant) nem expirados. É o que o varrimento periódico consome.
//
// Um pendente sem CreatedAt NUNCA é devolvido: não se expira sozinho aquilo cuja idade se
// desconhece (fail-safe — fica à espera de decisão explícita).
func (p *PendingApprovals) ListExpirable(ctx context.Context, now time.Time, ttl time.Duration) ([]PendingRecord, error) {
	events, err := p.store.Read(ctx, approvalStream, 0)
	if err != nil {
		return nil, err
	}
	decididas := make([][]byte, 0, 4)
	expirados := make(map[string]struct{})
	for _, ev := range events {
		switch ev.Type {
		case approvalGrantedEventType:
			var g grantPayload
			if err := json.Unmarshal(ev.Payload, &g); err == nil {
				decididas = append(decididas, g.Preview)
			}
		case approvalExpiredEventType:
			expirados[ev.StepID] = struct{}{} // "expired-<run>-<step>"
		}
	}
	var out []PendingRecord
	vistos := make(map[string]struct{})
	for _, ev := range events {
		if ev.Type != approvalPendingEventType {
			continue
		}
		var rec PendingRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil || rec.CreatedAt == "" {
			continue // sem âncora de tempo ⇒ nunca expira sozinho
		}
		criado, perr := time.Parse(time.RFC3339Nano, rec.CreatedAt)
		if perr != nil || now.Sub(criado) < ttl {
			continue
		}
		if _, ja := expirados["expired-"+rec.RunID+"-"+rec.StepID]; ja {
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
		chave := rec.RunID + "|" + rec.StepID
		if _, dup := vistos[chave]; dup {
			continue
		}
		vistos[chave] = struct{}{}
		out = append(out, rec)
	}
	return out, nil
}
