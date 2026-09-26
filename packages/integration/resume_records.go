package integration

import (
	"bytes"
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
	RunID     string
	Principal referencemonitor.Principal
	Scope     []string
	Model     agentruntime.ModelConfig
	System    string
	Tools     []agentruntime.ToolSpec
	// AllowedTools é a lista-branca do run (AOS-413). Tem de sobreviver à retoma: um run
	// re-hospedado sem ela ganhava as tools de todo o token. nil e vazia NÃO são o mesmo (vazia
	// nega tudo), e o JSON preserva a diferença (`null` vs `[]`). Um registo anterior não a tem
	// e decodifica nil — esses runs nunca tiveram restrição.
	AllowedTools []string
	// Inputs são os payloads do plano (AOS-414): um run re-hospedado sem eles perderia o
	// material sobre o qual o nó trabalha, e o modelo veria só o objectivo.
	Inputs            []agentruntime.PlanInput
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
		AllowedTools:      r.AllowedTools,
		Inputs:            r.Inputs,
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

// Erros da leitura do registo de retoma que RECUSAM a retoma (fail-closed). O `Goal` da retoma
// decide a AUTORIDADE das tool calls do run re-hospedado (ADR-034: o rótulo é re-dobrado do
// objectivo, dos `inputs` e da memória que o registo guarda) — um registo que perdesse os
// `inputs` re-autorizaria trusted o que foi untrusted. Por isso a leitura só aceita o que o
// próprio [ResumeRecords.Put] escreveria.
var (
	// ErrResumeRecordEmClaro — um corpo em CLARO por baixo de um cifrador activo. O Put com
	// cifrador nunca escreve em claro (é fail-closed na escrita); um registo assim só pode
	// vir de uma escrita crua no Event Store.
	ErrResumeRecordEmClaro = errors.New("integration: ErrResumeRecordEmClaro: registo de retoma em claro com o cifrador activo — recusado (so o Put o escreve, e selado)")
	// ErrResumeRecordDeOutroRun — o corpo decifrado nomeia outro run: um selado legítimo de
	// outro run do mesmo titular copiado para este passo (a cifra por-titular não o impede,
	// a chave é a mesma).
	ErrResumeRecordDeOutroRun = errors.New("integration: ErrResumeRecordDeOutroRun: registo de retoma cujo corpo nomeia outro run — recusado")
	// ErrResumeRecordDivergente — mais de um registo para o run, com conteúdo diferente.
	ErrResumeRecordDivergente = errors.New("integration: ErrResumeRecordDivergente: registos de retoma divergentes para o mesmo run — recusado")
	// ErrResumeRecordForaDoPut — um registo com o step_id de retoma do run mas com OUTRO
	// run_id de envelope do Event Store. O Put escreve SEMPRE com [approvalRunID]; é esse par
	// (run_id, step_id) que forma a idempotency_key e dá ao registo a deduplicação. Outro
	// run_id é a única forma de pôr um segundo registo ao lado do legítimo, e só a tem quem
	// escreve cru no stream.
	ErrResumeRecordForaDoPut = errors.New("integration: ErrResumeRecordForaDoPut: registo de retoma com run_id de envelope diferente do do Put — recusado")
)

// RegistoDeRetomaRecusado diz se err é um dos sentinelas acima: o registo EXISTE e foi
// recusado por não ser o que o Put escreveria. Para a API é um conflito com o estado do run
// (409), não uma falha interna.
func RegistoDeRetomaRecusado(err error) bool {
	return errors.Is(err, ErrResumeRecordEmClaro) || errors.Is(err, ErrResumeRecordDeOutroRun) ||
		errors.Is(err, ErrResumeRecordDivergente) || errors.Is(err, ErrResumeRecordForaDoPut)
}

// Get resolve o registo de retoma de um run. ok=false se não houver.
//
// Um titular já apagado por crypto-shredding torna o registo INDECIFRÁVEL e devolve erro —
// por desenho: um run cujo conteúdo foi apagado não é retomável.
//
// O PRIMEIRO REGISTO GANHA, E OS SEGUINTES TÊM DE LHE SER IGUAIS (AOS-069, 2026-09-26). Os
// escritores legítimos (suspensão por escalada e por exaustão, registo de arranque do
// crash-resume, re-hospedagem na retoma) escrevem TODOS o mesmo Goal do mesmo run, e o Put
// usa a mesma idempotency_key (`approval:resume-<run>`) — o Event Store deduplica sobre o
// stream INTEIRO (a tabela de dedup reconstrói-se do stream, também no JetStream) e fica o
// primeiro. Pelo Put há, portanto, UM só registo por run. Esta função lia o ÚLTIMO: quem
// tivesse escrita crua no stream acrescentava, com outro run_id de envelope (que escapa à
// dedup), um Goal sem `inputs`, e a retoma autorizava trusted. Agora qualquer registo que não
// seja o que o Put escreveria — outro run_id de envelope, corpo em claro com cifrador activo,
// corpo de outro run — RECUSA a retoma (erro nomeado), em vez de ser ignorado: ignorá-lo seria
// seguro para esta retoma mas apagava a evidência de uma escrita adulterada no stream de
// governação, e a recusa só custa disponibilidade a quem já tem escrita crua. A comparação
// com o primeiro (ErrResumeRecordDivergente) fica como defesa-em-profundidade para o caso de
// o transporte perder a dedup.
func (r *ResumeRecords) Get(ctx context.Context, runID string) (ResumeRecord, bool, error) {
	events, err := readApprovalStream(ctx, r.store)
	if err != nil {
		return ResumeRecord{}, false, err
	}
	want := "resume-" + runID
	var (
		first     ResumeRecord
		firstJSON []byte
		found     bool
	)
	for _, ev := range events {
		if ev.Type != approvalResumeEventType || ev.StepID != want {
			continue
		}
		if ev.RunID != approvalRunID {
			return ResumeRecord{}, false, ErrResumeRecordForaDoPut
		}
		rec, err := r.open(ctx, ev.Payload)
		if err != nil {
			return ResumeRecord{}, false, err
		}
		if rec.RunID != runID {
			return ResumeRecord{}, false, ErrResumeRecordDeOutroRun
		}
		canon, err := json.Marshal(rec)
		if err != nil {
			return ResumeRecord{}, false, err
		}
		if !found {
			first, firstJSON, found = rec, canon, true
			continue
		}
		if !bytes.Equal(canon, firstJSON) {
			return ResumeRecord{}, false, ErrResumeRecordDivergente
		}
	}
	return first, found, nil
}

// open abre UM envelope de retoma tal como o [ResumeRecords.Put] deste nó o escreveria: com
// cifrador, só selado; sem cifrador, só em claro (um selado sem cifrador não abre).
func (r *ResumeRecords) open(ctx context.Context, payload []byte) (ResumeRecord, error) {
	var env resumeEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return ResumeRecord{}, err
	}
	body := env.Body
	switch {
	case len(env.Sealed) > 0:
		if r.cipher == nil {
			return ResumeRecord{}, errors.New("integration: registo de retoma selado mas sem cifrador para o abrir")
		}
		plain, oerr := r.cipher.OpenContent(ctx, env.Subject, env.Sealed)
		if oerr != nil {
			return ResumeRecord{}, oerr
		}
		body = plain
	case r.cipher != nil:
		return ResumeRecord{}, ErrResumeRecordEmClaro
	}
	var rec ResumeRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return ResumeRecord{}, err
	}
	return rec, nil
}
