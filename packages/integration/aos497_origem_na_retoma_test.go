package integration

// AOS-497 — a origem declarada da saída e o seu vínculo viajam no registo de retoma, como o
// contrato de conclusão (AOS-493).

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

// TestAOS497_RegistoDeRetomaTransportaAOrigemEOVinculo: ida e volta pelo registo, em claro e
// CIFRADO por-titular — e é com eles que o Goal da retoma sai.
func TestAOS497_RegistoDeRetomaTransportaAOrigemEOVinculo(t *testing.T) {
	for _, cifra := range []struct {
		nome   string
		cipher agentruntime.ContentCipher
	}{{"em claro", nil}, {"cifrado por-titular", cifraPorTitular{}}} {
		for _, vinculo := range agentruntime.OutputSourceBindings() {
			t.Run(cifra.nome+"/"+string(vinculo), func(t *testing.T) {
				ctx := context.Background()
				es, err := eventstore.New()
				if err != nil {
					t.Fatal(err)
				}
				r, err := NewResumeRecords(es, cifra.cipher)
				if err != nil {
					t.Fatal(err)
				}
				rec := sampleResume("run-497")
				rec.CompletionMode = agentruntime.CompletionEnforce
				rec.OutputFromTool = "doc_read"
				rec.OutputSourceBinding = vinculo
				if err := r.Put(ctx, rec); err != nil {
					t.Fatalf("Put: %v", err)
				}
				got, ok, err := r.Get(ctx, "run-497")
				if err != nil || !ok {
					t.Fatalf("Get: ok=%v err=%v", ok, err)
				}
				if !reflect.DeepEqual(got, rec) {
					t.Fatalf("o registo nao sobreviveu a ida e volta:\n veio  %+v\n quero %+v", got, rec)
				}
				g := got.GoalWith("cred")
				if g.OutputFromTool != "doc_read" || g.OutputSourceBinding != vinculo {
					t.Fatalf("o Goal da retoma leva a origem %q e o vinculo %q; quero doc_read e %q", g.OutputFromTool, g.OutputSourceBinding, vinculo)
				}
			})
		}
	}
}

// TestAOS497_RegistoSemDeclaracaoNaoGanhaCampos: um registo anterior ao AOS-497 abre sem
// declaração, e um registo sem declaração serializa com os bytes de antes.
func TestAOS497_RegistoSemDeclaracaoNaoGanhaCampos(t *testing.T) {
	if bytes.Contains([]byte(registoAnteriorAoAOS489), []byte("OutputFromTool")) {
		t.Fatal("o registo antigo do teste nao pode ter os campos da origem")
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
	if got.OutputFromTool != "" || got.OutputSourceBinding != "" {
		t.Fatalf("um registo antigo nao declara origem: %q / %q", got.OutputFromTool, got.OutputSourceBinding)
	}
	if g := got.GoalWith("cred"); g.OutputFromTool != "" || g.OutputSourceBinding != "" {
		t.Fatalf("o Goal da retoma de um registo antigo ganhou uma origem: %q / %q", g.OutputFromTool, g.OutputSourceBinding)
	}
	// Um registo de hoje, com contrato e modo mas sem origem, serializa sem os campos novos.
	rec := sampleResume("run-sem-origem")
	rec.CompletionRequires = []string{"doc_read"}
	rec.CompletionMode = agentruntime.CompletionEnforce
	raw, err := json.Marshal(rec)
	if err != nil || bytes.Contains(raw, []byte("OutputFromTool")) || bytes.Contains(raw, []byte("OutputSourceBinding")) {
		t.Fatalf("um registo sem origem declarada tem de serializar sem os campos: %s (%v)", raw, err)
	}
}

// TestAOS497_OutraOrigemOuOutroVinculoEDivergencia: um segundo registo do mesmo run com outra
// origem, ou com outro vínculo, recusa a retoma — o vínculo decide o desfecho.
func TestAOS497_OutraOrigemOuOutroVinculoEDivergencia(t *testing.T) {
	base := sampleResume("run-x")
	base.OutputFromTool, base.OutputSourceBinding = "doc_read", agentruntime.OutputSourceMeasure
	outraOrigem := base
	outraOrigem.OutputFromTool = "doc_search"
	outroVinculo := base
	outroVinculo.OutputSourceBinding = agentruntime.OutputSourceBinds
	semOrigem := sampleResume("run-x")

	mesmo := []eventstore.Event{
		envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, base)),
		envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, base)),
	}
	r, _ := NewResumeRecords(&streamFabricado{events: mesmo}, cifraReversivel{})
	if got, ok, err := r.Get(context.Background(), "run-x"); err != nil || !ok || got.OutputFromTool != "doc_read" {
		t.Fatalf("dois registos iguais sao o mesmo run: ok=%v err=%v", ok, err)
	}
	for nome, outro := range map[string]ResumeRecord{"outra origem": outraOrigem, "outro vinculo": outroVinculo, "sem origem": semOrigem} {
		diverge := []eventstore.Event{
			envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, base)),
			envelopeDeRetoma(t, approvalRunID, "run-x", selado(t, outro)),
		}
		r, _ = NewResumeRecords(&streamFabricado{events: diverge}, cifraReversivel{})
		if _, ok, err := r.Get(context.Background(), "run-x"); !errors.Is(err, ErrResumeRecordDivergente) || ok {
			t.Fatalf("%s: um segundo registo diferente tinha de recusar a retoma: ok=%v err=%v", nome, ok, err)
		}
	}
}
