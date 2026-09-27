package integration

// aos440_retoma_titular_test.go — O REGISTO DE RETOMA É SELADO SOB O TITULAR DOS DADOS (AOS-440),
// E UM REGISTO ANTIGO CONTINUA A ABRIR.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// cifraPorTitular sela com o titular dentro do blob e só abre com o MESMO titular — o suficiente
// para provar SOB QUEM o registo foi selado e com quem é aberto.
type cifraPorTitular struct{}

func (cifraPorTitular) SealContent(_ context.Context, subject, _ string, body []byte) ([]byte, error) {
	return append([]byte(subject+"|"), body...), nil
}

func (cifraPorTitular) OpenContent(_ context.Context, subject string, sealed []byte) ([]byte, error) {
	p := []byte(subject + "|")
	if len(sealed) < len(p) || string(sealed[:len(p)]) != string(p) {
		return nil, errors.New("selado sob outro titular")
	}
	return sealed[len(p):], nil
}

func envelopeDoRegisto(t *testing.T, es eventstore.EventStore, runID string) resumeEnvelope {
	t.Helper()
	evs, err := es.Read(context.Background(), approvalStream, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.StepID == "resume-"+runID {
			var env resumeEnvelope
			if err := json.Unmarshal(e.Payload, &env); err != nil {
				t.Fatal(err)
			}
			return env
		}
	}
	t.Fatalf("sem registo de retoma de %q", runID)
	return resumeEnvelope{}
}

func TestAOS440RegistoDeRetomaSeladoSobOTitular(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewResumeRecords(es, cifraPorTitular{})
	if err != nil {
		t.Fatal(err)
	}

	// Run filho de um plano: quem chama é o drenador, os dados são da alice.
	filho := ResumeRecord{RunID: "p~n1", Principal: referencemonitor.Principal{NHIID: "drenador", RequestedBy: "sub-alice"}, Subject: "sub-alice"}
	if err := r.Put(ctx, filho); err != nil {
		t.Fatal(err)
	}
	if env := envelopeDoRegisto(t, es, "p~n1"); env.Subject != "sub-alice" {
		t.Fatalf("o registo do run filho tem de ser selado sob o titular dos dados, veio %q", env.Subject)
	}
	got, ok, err := r.Get(ctx, "p~n1")
	if err != nil || !ok || got.Titular() != "sub-alice" || got.Principal.RequestedBy != "sub-alice" {
		t.Fatalf("Get: %+v ok=%t %v", got, ok, err)
	}
	if g := got.GoalWith("cred"); g.Subject != "sub-alice" || g.Titular() != "sub-alice" {
		t.Fatalf("o Goal reconstruido tem de levar o titular: %+v", g)
	}

	// Um registo como os de ANTES do AOS-440 (sem Subject): selado e aberto sob o NHIID.
	antigo := ResumeRecord{RunID: "run-antigo", Principal: referencemonitor.Principal{NHIID: "nhi:agente"}}
	if err := r.Put(ctx, antigo); err != nil {
		t.Fatal(err)
	}
	if env := envelopeDoRegisto(t, es, "run-antigo"); env.Subject != "nhi:agente" {
		t.Fatalf("sem Subject o registo continua selado sob o NHIID, veio %q", env.Subject)
	}
	got, ok, err = r.Get(ctx, "run-antigo")
	if err != nil || !ok || got.Titular() != "nhi:agente" {
		t.Fatalf("o registo antigo tem de continuar a abrir: %+v ok=%t %v", got, ok, err)
	}
}
