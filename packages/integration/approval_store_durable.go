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

// quoteJSON escapa uma string para interpolação segura num literal JSON.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

var _ ApprovalStore = (*eventStoreApprovalStore)(nil)
