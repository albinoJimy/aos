package semantic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/substrate/eventstore"
)

// CurationRequest é o pedido de CURADORIA/PROMOÇÃO de um facto em quarentena
// (untrusted) para conhecimento confiável (trusted). A validação é OBRIGATÓRIA e
// EXPLÍCITA (política assinada OU decisão humana) — não há promoção silenciosa.
type CurationRequest struct {
	// FactID identifica o facto em quarentena a promover.
	FactID string
	// Method é a forma de validação (política ou humano) — ver [provenance.ValidationMethod].
	Method provenance.ValidationMethod
	// Validator identifica QUEM/QUE validou (id de política ou principal humano).
	Validator string
	// Justification é a justificação de auditoria (opcional, selada na cadeia).
	Justification string
	// AgentID e RunID atribuem a promoção à NHI e ao run (accountability).
	AgentID string
	RunID   string
	// AuditPartition é a fronteira de encadeamento do audit da promoção (default:
	// RunID, ou "global" se vazio) — INDEPENDENTE da partição de conteúdo.
	AuditPartition string
}

// Curate PROMOVE um facto em quarentena (untrusted) para trusted através do
// [provenance.Promoter] de AOS-042: exige validação explícita e REGISTA a promoção na
// hash-chain tamper-evident (AOS-011). Passos (todos fail-closed):
//
//  1. localiza o facto; um id inexistente → [ErrFactNotFound];
//  2. reconstrói o registo e SELA-o como untrusted ([provenance.Seal]) — Seal REJEITA
//     um registo trusted/sem proveniência, pelo que só conhecimento genuinamente em
//     quarentena é promovível (a curadoria de algo já trusted falha-fecha);
//  3. delega em [provenance.Promoter.Promote]: valida a curadoria e sela a promoção na
//     hash-chain (o taint de ORIGEM untrusted + a validação ficam selados);
//  4. persiste um evento de promoção no Event Store (para a reconstrução servir o facto
//     como trusted) referenciando o audit_seq da cadeia. O facto original NÃO é
//     reescrito (permanece imutável, coerente com o event-sourcing).
//
// Devolve o AuditRecord SELADO da promoção (a prova auditável). Não há promoção
// automática: sem [CurationRequest.Method]/Validator explícitos, [provenance.Promoter]
// recusa com [provenance.ErrPromotionNotValidated].
func (kb *KnowledgeBase) Curate(ctx context.Context, req CurationRequest) (audit.AuditRecord, error) {
	_, span := kb.tracer.StartSpan(ctx, spanCurate)
	defer span.End()
	span.SetAttribute(attrFactID, req.FactID)

	// Serializa a curadoria com as demais escritas: torna atómico o localizar-selar-
	// -persistir (evita selar a promoção mais do que uma vez / correr com um Write).
	kb.writeMu.Lock()
	defer kb.writeMu.Unlock()

	st, err := kb.buildState(ctx)
	if err != nil {
		return audit.AuditRecord{}, err
	}
	env, ok := st.facts[req.FactID]
	if !ok {
		span.SetAttribute(attrResult, "not_found")
		return audit.AuditRecord{}, ErrFactNotFound
	}

	// Reconstrói o registo do facto (o corpo é decifrado se recuperável; a promoção
	// não depende do conteúdo, só da classificação de origem selada no envelope).
	createdAt, _ := time.Parse(time.RFC3339Nano, env.CreatedAt)
	rec, _, _ := kb.recordFromEnvelope(env, createdAt)

	// SELA como untrusted (AOS-042): Seal rejeita um registo trusted/sem proveniência —
	// só conhecimento em quarentena é promovível (fail-closed).
	untrusted, err := provenance.Seal(rec)
	if err != nil {
		span.SetAttribute(attrResult, "rejected")
		return audit.AuditRecord{}, err
	}

	// Promoção auditável via o Promoter de AOS-042.
	_, sealed, err := kb.promoter.Promote(ctx, provenance.PromotionRequest{
		Entry:          untrusted,
		Method:         req.Method,
		Validator:      req.Validator,
		Justification:  req.Justification,
		AgentID:        req.AgentID,
		RunID:          req.RunID,
		AuditPartition: req.AuditPartition,
	})
	if err != nil {
		span.SetAttribute(attrResult, "rejected")
		return audit.AuditRecord{}, err
	}
	span.SetAttribute(attrAudit, sealed.AuditSeq)

	// Persiste o evento de promoção: a reconstrução servirá o facto como trusted,
	// admitindo-o pela fonte-curadora (trusted) que o método de validação implica.
	pe := promotionEnvelope{
		SchemaVersion: semanticRecordSchemaVersion,
		FactID:        req.FactID,
		// LIGA a promoção ao conteúdo concreto validado: a reconstrução só serve como
		// trusted enquanto o envelope efectivo tiver EXACTAMENTE este ContentHash.
		ContentHash:       env.ContentHash,
		CuratorSource:     string(curatorSource(req.Method)),
		Method:            string(req.Method),
		Validator:         req.Validator,
		RunID:             req.RunID,
		AgentID:           req.AgentID,
		PromotionAuditSeq: sealed.AuditSeq,
	}
	if err := kb.appendPromotion(ctx, pe); err != nil {
		return audit.AuditRecord{}, err
	}
	span.SetAttribute(attrResult, "promoted")
	return sealed, nil
}

// curatorSource mapeia o método de validação à FONTE trusted que a reconstrução usa
// para admitir o facto promovido no control-plane. Uma validação HUMANA é um principal
// autenticado (authenticated_user); uma validação por POLÍTICA é o próprio sistema
// (policy-as-code). Ambas classificam trusted — é a autoridade legítima da promoção.
func curatorSource(m provenance.ValidationMethod) provenance.Source {
	if m == provenance.ValidationHuman {
		return provenance.SourceAuthenticatedUser
	}
	return provenance.SourceSystem
}

// appendPromotion escreve o evento de promoção append-only no Event Store. A
// idempotência é f(run_id, "semantic:promote:"+fact_id).
func (kb *KnowledgeBase) appendPromotion(ctx context.Context, pe promotionEnvelope) error {
	payload, err := json.Marshal(pe)
	if err != nil {
		return err
	}
	_, err = kb.es.Append(ctx, KnowledgeStreamID, eventstore.EventInput{
		Type:     EventTypeFactPromoted,
		Payload:  payload,
		RunID:    pe.RunID,
		StepID:   "semantic:promote:" + pe.FactID,
		Producer: eventstore.Producer{NHIID: pe.AgentID},
	})
	return err
}
