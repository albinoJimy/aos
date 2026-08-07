package integration

import (
	"context"
	"encoding/json"
	"errors"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-021 — Registo de RETOMA de runs suspensos à espera de aval humano
// ---------------------------------------------------------------------------
//
// Um run que escala uma tool call PÁRA e o processo larga tudo (lease, goroutine,
// heartbeat) — não se seguram recursos durante minutos de latência humana. Para o poder
// retomar depois, é preciso ter guardado o que o reconstitui: o [agentruntime.Goal].
//
// DUAS OMISSÕES DELIBERADAS:
//
//  1. A CREDENCIAL NÃO É GUARDADA. É um bearer token, e este log é lido pelo read-path
//     soberano e replicado. Além disso teria EXPIRADO: a janela de aprovação é de 15 min
//     e os TTL de classe NHI andam nos 5-15 — uma retoma automática falharia na identidade
//     precisamente quando o humano demora. A retoma exige credencial FRESCA.
//  2. O corpo é CIFRADO POR-TITULAR quando há [agentruntime.ContentCipher] (AOS-093). O
//     Goal transporta o objectivo do utilizador e o contexto de memória — conteúdo que o
//     `turn.recorded` deliberadamente NÃO persiste em claro (só hashes). Guardá-lo aqui
//     em claro abriria uma exposição nova; selado, o crypto-shredding alcança-o como
//     alcança o resto do conteúdo do run.

const approvalResumeEventType = "run.resume.record"

// ErrNilResumeStore — construtor sem Event Store.
var ErrNilResumeStore = errors.New("integration: event store nil para o registo de retoma")

// ResumeRecord é o que reconstitui um run suspenso — o [agentruntime.Goal] SEM a
// credencial (ver as omissões acima).
type ResumeRecord struct {
	RunID             string
	Principal         referencemonitor.Principal
	Scope             []string
	Model             agentruntime.ModelConfig
	System            string
	Tools             []agentruntime.ToolSpec
	Skills            []agentruntime.ToolSpec
	Objective         string
	MemoryContext     []byte
	MaxTurns          int
	ParentTraceParent string
}

// GoalWith reconstrói o [agentruntime.Goal] com a credencial FRESCA fornecida na retoma.
// É o único ponto onde a credencial volta a entrar — nunca veio do log.
func (r ResumeRecord) GoalWith(credential string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID:             r.RunID,
		Principal:         r.Principal,
		Credential:        credential,
		Scope:             r.Scope,
		Model:             r.Model,
		System:            r.System,
		Tools:             r.Tools,
		Skills:            r.Skills,
		Objective:         r.Objective,
		MemoryContext:     r.MemoryContext,
		MaxTurns:          r.MaxTurns,
		ParentTraceParent: r.ParentTraceParent,
	}
}

// resumeEnvelope é o que vai ao log: o corpo (cifrado ou em claro) mais o titular sob cuja
// KEK foi selado. O RunID fica FORA do envelope para a leitura o poder filtrar sem decifrar.
type resumeEnvelope struct {
	RunID   string          `json:"run_id"`
	Subject string          `json:"subject,omitempty"`
	Sealed  []byte          `json:"sealed,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// ResumeRecords persiste e resolve registos de retoma.
type ResumeRecords struct {
	store  approvalAppendReader
	cipher agentruntime.ContentCipher
}

// NewResumeRecords constrói o registo. cipher OPCIONAL: quando presente, o corpo é
// CIFRADO por-titular antes de tocar o log (fail-closed — um erro de cifra aborta a
// escrita, nunca se degrada para claro). Sem cipher o corpo vai em claro, o que só é
// aceitável fora de produção.
func NewResumeRecords(store approvalAppendReader, cipher agentruntime.ContentCipher) (*ResumeRecords, error) {
	if store == nil {
		return nil, ErrNilResumeStore
	}
	return &ResumeRecords{store: store, cipher: cipher}, nil
}

// Put persiste o registo de retoma do run. A idempotency_key deriva do RunID: re-escalar o
// mesmo run não duplica o registo (o primeiro fica — o Goal é o mesmo).
func (r *ResumeRecords) Put(ctx context.Context, rec ResumeRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	env := resumeEnvelope{RunID: rec.RunID}
	if r.cipher != nil {
		subject := rec.Principal.NHIID
		sealed, serr := r.cipher.SealContent(ctx, subject, approvalStream, body)
		if serr != nil {
			// FAIL-CLOSED: nunca persistir em claro por baixo de um cifrador activo.
			return serr
		}
		env.Subject, env.Sealed = subject, sealed
	} else {
		env.Body = body
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = r.store.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalResumeEventType,
		Payload: payload,
		RunID:   approvalRunID,
		StepID:  "resume-" + rec.RunID,
	})
	return err
}

// Get resolve o registo de retoma de um run. ok=false se não houver.
//
// Um titular já apagado por crypto-shredding torna o registo INDECIFRÁVEL e devolve erro —
// por desenho: um run cujo conteúdo foi apagado não é retomável.
func (r *ResumeRecords) Get(ctx context.Context, runID string) (ResumeRecord, bool, error) {
	events, err := readApprovalStream(ctx, r.store)
	if err != nil {
		return ResumeRecord{}, false, err
	}
	want := "resume-" + runID
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != approvalResumeEventType || ev.StepID != want {
			continue
		}
		var env resumeEnvelope
		if err := json.Unmarshal(ev.Payload, &env); err != nil {
			return ResumeRecord{}, false, err
		}
		body := env.Body
		if len(env.Sealed) > 0 {
			if r.cipher == nil {
				return ResumeRecord{}, false, errors.New("integration: registo de retoma selado mas sem cifrador para o abrir")
			}
			plain, oerr := r.cipher.OpenContent(ctx, env.Subject, env.Sealed)
			if oerr != nil {
				return ResumeRecord{}, false, oerr
			}
			body = plain
		}
		var rec ResumeRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			return ResumeRecord{}, false, err
		}
		return rec, true, nil
	}
	return ResumeRecord{}, false, nil
}
