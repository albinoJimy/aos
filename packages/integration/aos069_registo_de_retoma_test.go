package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-069 — o registo de retoma decide a AUTORIDADE do run re-hospedado (ADR-034: o rótulo é
// re-dobrado do objectivo, dos `inputs` e da memória que ele guarda). Desde que a via durável
// entrega ao RM o rótulo do contexto, um registo de retoma sem os `inputs` re-autorizaria
// trusted o que foi untrusted. Estes testes fixam que a leitura só aceita o que o Put deste
// nó escreveria.
//
// A «escrita crua» é simulada de duas formas: um Append ao Event Store real com OUTRO run_id
// de envelope (o que escapa à dedup do Put), e — para os casos que a dedup do Event Store
// tornaria inalcançáveis por essa via — um stream fabricado ([streamFabricado]).

func retomaComInputs(runID string) ResumeRecord {
	rec := sampleResume(runID)
	rec.Inputs = []agentruntime.PlanInput{{From: "n0", Output: "document_content", Digest: "sha256:00", Content: []byte("dados")}}
	return rec
}

func novoES(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

// envelopeDeRetoma devolve o evento que o Put escreveria (ou um adulterado), sem passar por ele.
func envelopeDeRetoma(t *testing.T, envRunID, stepRunID string, env resumeEnvelope) eventstore.Event {
	t.Helper()
	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return eventstore.Event{Type: approvalResumeEventType, RunID: envRunID, StepID: "resume-" + stepRunID, Payload: payload}
}

func selado(t *testing.T, rec ResumeRecord) resumeEnvelope {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s, _ := cifraReversivel{}.SealContent(context.Background(), rec.Principal.NHIID, approvalStream, body)
	return resumeEnvelope{RunID: rec.RunID, Subject: rec.Principal.NHIID, Sealed: s}
}

func emClaro(t *testing.T, rec ResumeRecord) resumeEnvelope {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return resumeEnvelope{RunID: rec.RunID, Body: body}
}

// streamFabricado é um stream de governação com eventos postos à mão — o que um escritor cru
// no transporte conseguiria, incluindo o que a dedup do Event Store em memória não deixa fazer.
type streamFabricado struct{ events []eventstore.Event }

func (s *streamFabricado) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, errors.New("stream fabricado: so leitura")
}
func (s *streamFabricado) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return s.events, nil
}

// Um segundo registo com outro run_id de envelope (a única forma de escapar à dedup do Put) é
// recusado — no Event Store real, depois do registo legítimo.
func TestAOS069_RetomaRecusaRegistoForaDoPut(t *testing.T) {
	ctx := context.Background()
	es := novoES(t)
	r, _ := NewResumeRecords(es, cifraReversivel{})
	if err := r.Put(ctx, retomaComInputs("run-x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ev := envelopeDeRetoma(t, "escritor-cru", "run-x", selado(t, sampleResume("run-x")))
	if _, err := es.Append(ctx, approvalStream, eventstore.EventInput{Type: ev.Type, RunID: ev.RunID, StepID: ev.StepID, Payload: ev.Payload}); err != nil {
		t.Fatalf("Append cru: %v", err)
	}
	if _, ok, err := r.Get(ctx, "run-x"); !errors.Is(err, ErrResumeRecordForaDoPut) || ok {
		t.Fatalf("um registo com outro run_id de envelope devia recusar a retoma (antes ganhava por ser o ultimo): ok=%v err=%v", ok, err)
	}
	// O Put repetido é deduplicado — a via legítima nunca produz um segundo registo.
	if err := r.Put(ctx, retomaComInputs("run-x")); err != nil {
		t.Fatalf("Put repetido: %v", err)
	}
}

// Os restantes sentinelas, sobre um stream fabricado com o run_id de envelope do Put.
func TestAOS069_RetomaRecusaRegistoQueOPutNaoEscreveria(t *testing.T) {
	legit := selado(t, retomaComInputs("run-x"))
	casos := []struct {
		nome   string
		events []eventstore.Event
		quer   error
	}{
		{"em claro com cifrador activo", []eventstore.Event{envelopeDeRetoma(t, approvalRunID, "run-x", emClaro(t, sampleResume("run-x")))}, ErrResumeRecordEmClaro},
		{"selado de outro run", []eventstore.Event{envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, sampleResume("run-outro")))}, ErrResumeRecordDeOutroRun},
		{"segundo registo divergente", []eventstore.Event{
			envelopeDeRetoma(t, approvalRunID, "run-x", legit),
			envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, sampleResume("run-x"))),
		}, ErrResumeRecordDivergente},
		{"segundo registo em claro", []eventstore.Event{
			envelopeDeRetoma(t, approvalRunID, "run-x", legit),
			envelopeDeRetoma(t, approvalRunID, "run-x", emClaro(t, sampleResume("run-x"))),
		}, ErrResumeRecordEmClaro},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r, _ := NewResumeRecords(&streamFabricado{events: c.events}, cifraReversivel{})
			_, ok, err := r.Get(context.Background(), "run-x")
			if !errors.Is(err, c.quer) || ok {
				t.Fatalf("esperava %v, veio ok=%v err=%v", c.quer, ok, err)
			}
			if !RegistoDeRetomaRecusado(err) {
				t.Fatalf("o erro devia ser classificado como registo recusado (409 na API): %v", err)
			}
		})
	}
}

// O caso legítimo de dois registos iguais (dedup perdida pelo transporte) continua a retomar,
// com o conteúdo do primeiro (inputs incluídos).
func TestAOS069_RetomaAceitaRegistosIguais(t *testing.T) {
	rec := retomaComInputs("run-x")
	env := selado(t, rec)
	r, _ := NewResumeRecords(&streamFabricado{events: []eventstore.Event{
		envelopeDeRetoma(t, approvalRunID, "run-x", env),
		envelopeDeRetoma(t, approvalRunID, "run-x", env),
	}}, cifraReversivel{})
	got, ok, err := r.Get(context.Background(), "run-x")
	if err != nil || !ok {
		t.Fatalf("registos iguais deviam retomar: ok=%v err=%v", ok, err)
	}
	if len(got.Inputs) != 1 || got.Objective != rec.Objective {
		t.Fatalf("o registo resolvido devia ser o legitimo, com os inputs: %+v", got)
	}
}

// O log do operador (crash-resume e rota de retoma registam o erro com %v) nomeia o sentinela.
func TestAOS069_SentinelasDeRetomaNomeiamSeNaMensagem(t *testing.T) {
	for nome, err := range map[string]error{
		"ErrResumeRecordEmClaro":    ErrResumeRecordEmClaro,
		"ErrResumeRecordDeOutroRun": ErrResumeRecordDeOutroRun,
		"ErrResumeRecordDivergente": ErrResumeRecordDivergente,
		"ErrResumeRecordForaDoPut":  ErrResumeRecordForaDoPut,
	} {
		if !strings.Contains(err.Error(), nome) {
			t.Errorf("a mensagem de %s tem de o nomear: %q", nome, err.Error())
		}
		if !RegistoDeRetomaRecusado(err) {
			t.Errorf("%s tem de ser classificado como registo recusado", nome)
		}
	}
	if RegistoDeRetomaRecusado(errors.New("outro")) {
		t.Error("um erro qualquer nao e um registo recusado")
	}
}
