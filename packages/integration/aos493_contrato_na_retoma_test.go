package integration

// AOS-493 — o contrato de conclusão e o modo de aplicação do veredicto viajam no registo de
// retoma, como o layout (AOS-489).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS493_RegistoDeRetomaTransportaContratoEModo: ida e volta pelo registo, em claro e
// CIFRADO por-titular — e é com eles que o Goal da retoma sai.
func TestAOS493_RegistoDeRetomaTransportaContratoEModo(t *testing.T) {
	for _, cifra := range []struct {
		nome   string
		cipher agentruntime.ContentCipher
	}{{"em claro", nil}, {"cifrado por-titular", cifraPorTitular{}}} {
		for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionOff, agentruntime.CompletionObserve, agentruntime.CompletionEnforce} {
			t.Run(cifra.nome+"/"+string(modo), func(t *testing.T) {
				ctx := context.Background()
				es, err := eventstore.New()
				if err != nil {
					t.Fatal(err)
				}
				r, err := NewResumeRecords(es, cifra.cipher)
				if err != nil {
					t.Fatal(err)
				}
				rec := sampleResume("run-493")
				rec.CompletionRequires = []string{"doc_read", "doc_write"}
				rec.CompletionMode = modo
				if err := r.Put(ctx, rec); err != nil {
					t.Fatalf("Put: %v", err)
				}
				got, ok, err := r.Get(ctx, "run-493")
				if err != nil || !ok {
					t.Fatalf("Get: ok=%v err=%v", ok, err)
				}
				if !reflect.DeepEqual(got, rec) {
					t.Fatalf("o registo nao sobreviveu a ida e volta:\n veio  %+v\n quero %+v", got, rec)
				}
				g := got.GoalWith("cred")
				if g.CompletionMode != modo || !reflect.DeepEqual(g.CompletionRequires, rec.CompletionRequires) {
					t.Fatalf("o Goal da retoma leva modo %q e contrato %v; quero %q e %v", g.CompletionMode, g.CompletionRequires, modo, rec.CompletionRequires)
				}
			})
		}
	}
}

// TestAOS493_RegistoAntigoSemModoERetomadoSemVeredicto: um registo sem os campos é um run
// anterior ao AOS-493 — retoma-se com o modo DESLIGADO, e não no modo corrente do nó.
func TestAOS493_RegistoAntigoSemModoERetomadoSemVeredicto(t *testing.T) {
	if bytes.Contains([]byte(registoAnteriorAoAOS489), []byte("Completion")) {
		t.Fatal("o registo antigo do teste nao pode ter os campos do veredicto")
	}
	ev := envelopeDeRetoma(t, approvalRunID, "run-antigo", resumeEnvelope{RunID: "run-antigo", Body: json.RawMessage(registoAnteriorAoAOS489)})
	r, err := NewResumeRecords(&streamFabricado{events: []eventstore.Event{ev}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.Get(context.Background(), "run-antigo")
	if err != nil || !ok {
		t.Fatalf("o registo antigo tem de continuar a abrir: ok=%v err=%v", ok, err)
	}
	if got.CompletionMode != "" || got.ModoDeConclusao() != agentruntime.CompletionOff || got.CompletionRequires != nil {
		t.Fatalf("CompletionMode=%q ModoDeConclusao=%q contrato=%v; quero \"\", off e nil", got.CompletionMode, got.ModoDeConclusao(), got.CompletionRequires)
	}
	if g := got.GoalWith("cred"); g.CompletionMode != agentruntime.CompletionOff {
		t.Fatalf("o Goal da retoma de um registo antigo leva o modo %q — tinha de ser off, nunca vazio (vazio recebe o modo do no que retoma)", g.CompletionMode)
	}
	raw, err := json.Marshal(got)
	if err != nil || bytes.Contains(raw, []byte("Completion")) {
		t.Fatalf("um registo sem contrato nem modo tem de serializar sem os campos: %s (%v)", raw, err)
	}
}

// TestAOS493_RegistoAntigoERetomaDoMesmoRunNaoDivergem: a retoma de um run antigo re-escreve o
// registo com o modo EXPLÍCITO (`off`). Os dois são o mesmo run; outro modo, ou outro contrato,
// é uma divergência real e recusa a retoma.
func TestAOS493_RegistoAntigoERetomaDoMesmoRunNaoDivergem(t *testing.T) {
	antigo := sampleResume("run-x")
	explicito := sampleResume("run-x")
	explicito.CompletionMode = agentruntime.CompletionOff
	outroModo := sampleResume("run-x")
	outroModo.CompletionMode = agentruntime.CompletionEnforce
	outroContrato := sampleResume("run-x")
	outroContrato.CompletionRequires = []string{"doc_read"}

	mesmo := []eventstore.Event{
		envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, antigo)),
		envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, explicito)),
	}
	r, _ := NewResumeRecords(&streamFabricado{events: mesmo}, cifraReversivel{})
	if got, ok, err := r.Get(context.Background(), "run-x"); err != nil || !ok || got.ModoDeConclusao() != agentruntime.CompletionOff {
		t.Fatalf("o registo antigo e a sua re-escrita com off explicito sao o mesmo run: ok=%v err=%v", ok, err)
	}
	for nome, outro := range map[string]ResumeRecord{"outro modo": outroModo, "outro contrato": outroContrato} {
		diverge := []eventstore.Event{
			envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, antigo)),
			envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, outro)),
		}
		r, _ = NewResumeRecords(&streamFabricado{events: diverge}, cifraReversivel{})
		if _, ok, err := r.Get(context.Background(), "run-x"); !errors.Is(err, ErrResumeRecordDivergente) || ok {
			t.Fatalf("%s: um segundo registo diferente tinha de recusar a retoma: ok=%v err=%v", nome, ok, err)
		}
	}
}
