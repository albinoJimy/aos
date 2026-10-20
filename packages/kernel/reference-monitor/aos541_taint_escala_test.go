package referencemonitor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-541 — o gate de taint ESCALA em vez de negar, só no que sobra depois dos AOS-538 e AOS-540.
//
// Os destinos são os do AOS-538 (doc_read é leitura local; ocr_read e web_post não). O verificador
// de aprovação é o mínimo que modela o four-eyes: aceita a evidência "OK" só para a preview que
// lhe disseram.

type aos541Verificador struct{ preview []byte }

func (v aos541Verificador) VerifyApproval(_ context.Context, evidence, preview []byte) (ApprovalProof, error) {
	if string(evidence) != "OK" || string(preview) != string(v.preview) {
		return ApprovalProof{}, errors.New("evidencia invalida ou de outra accao")
	}
	return ApprovalProof{Approvers: []string{"human:alice", "human:bob"}, DualControl: true}, nil
}

// aos541Monitor monta approval → taint (escalante ou não) com o classificador de destino do
// AOS-538 e as capabilities de produção armadas.
func aos541Monitor(t *testing.T, escala bool, v ApprovalVerifier, store *storeQueCaptura) *Monitor {
	t.Helper()
	priv := NewDestinationPrivileged(NewStaticPrivilegedSet(aos538Caps()...), aos538Destinos())
	gate := NewTaintGate(priv)
	if escala {
		gate = NewEscalatingTaintGate(priv)
	}
	m := New(WithHooks(NewApprovalGate(v), gate), WithEventSink(&eventStoreSink{store: store}))
	for _, id := range []string{"doc_read", "ocr_read", "web_post"} {
		if err := m.Register(id, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	return m
}

func aos541Call(tool, capability, rotulo, step string) Call {
	return Call{
		RunID: "run-541", StepID: step, ToolID: tool, Capability: capability,
		Resource:  Resource{Type: "doc", Value: "doc://fatura-5.png", Region: "eu"},
		Principal: Principal{NHIID: "nhi-541"},
		Context:   CallContext{Taint: rotulo},
		Input:     []byte(`{"doc_id":"fatura-5.png"}`),
	}
}

func aos541Payload(t *testing.T, store *storeQueCaptura) mediationPayload {
	t.Helper()
	var p mediationPayload
	if err := json.Unmarshal(store.ultimo, &p); err != nil {
		t.Fatalf("payload ilegivel: %v (%s)", err, store.ultimo)
	}
	return p
}

// Com o interruptor: a call privilegiada com o contexto untrusted ESCALA, atribuída ao taint; o
// evento sela `escalate`, `denied_by=taint`, a razão e o `taint_privilege`.
func TestAOS541_EscalaEmVezDeNegarEOEventoDizQueFoiOTaint(t *testing.T) {
	for _, c := range []struct{ tool, capability, razao string }{
		{"ocr_read", "cap:fs.read", TaintPrivilegeCapability},
		{"web_post", "cap:http.post", TaintPrivilegeCapability},
	} {
		t.Run(c.tool, func(t *testing.T) {
			store := &storeQueCaptura{}
			m := aos541Monitor(t, true, aos541Verificador{}, store)
			dec, err := m.Mediate(context.Background(), aos541Call(c.tool, c.capability, taint.StringUntrusted, "s1"))
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if dec.Effect != EffectEscalate || dec.DeniedBy != "taint" || dec.Code != CodeEscalated || dec.Reason != ReasonTaintEscalated {
				t.Fatalf("queria escalate|taint|%s, veio %s|%q|%s|%q", CodeEscalated, dec.Effect, dec.DeniedBy, dec.Code, dec.Reason)
			}
			p := aos541Payload(t, store)
			if p.Decision != string(EffectEscalate) || p.DeniedBy != "taint" || p.Reason != ReasonTaintEscalated ||
				p.Context.Taint != taint.StringUntrusted || p.Context.TaintPrivilege != c.razao {
				t.Fatalf("o evento nao diz que foi o taint a escalar: %s", store.ultimo)
			}
		})
	}
}

// Sem o interruptor nada muda: a mesma call é NEGADA pelo taint, com a razão de sempre.
func TestAOS541_SemOInterruptorNegaComoAntes(t *testing.T) {
	store := &storeQueCaptura{}
	m := aos541Monitor(t, false, aos541Verificador{}, store)
	dec, err := m.Mediate(context.Background(), aos541Call("ocr_read", "cap:fs.read", taint.StringUntrusted, "s1"))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectDeny || dec.DeniedBy != "taint" || dec.Reason == ReasonTaintEscalated {
		t.Fatalf("sem o interruptor queria deny|taint com a razao de antes, veio %s|%q|%q", dec.Effect, dec.DeniedBy, dec.Reason)
	}
	if g := NewTaintGate(nil); g.Escalates() {
		t.Fatal("NewTaintGate nao pode escalar")
	}
	if g := NewEscalatingTaintGate(nil); !g.Escalates() {
		t.Fatal("NewEscalatingTaintGate tem de escalar")
	}
}

// O que não é privilegiado não escala: a leitura local do AOS-538 com o contexto untrusted e
// qualquer call com o contexto trusted passam sem pedir humanos.
func TestAOS541_LeituraLocalETrustedNaoEscalam(t *testing.T) {
	for _, c := range []struct{ nome, tool, capability, rotulo string }{
		{"doc_read-untrusted", "doc_read", "cap:fs.read", taint.StringUntrusted},
		{"ocr_read-trusted", "ocr_read", "cap:fs.read", taint.StringTrusted},
		{"web_post-trusted", "web_post", "cap:http.post", taint.StringTrusted},
	} {
		t.Run(c.nome, func(t *testing.T) {
			m := aos541Monitor(t, true, aos541Verificador{}, &storeQueCaptura{})
			dec, err := m.Mediate(context.Background(), aos541Call(c.tool, c.capability, c.rotulo, "s1"))
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if dec.Effect != EffectPermit {
				t.Fatalf("queria permit, veio %s|%q", dec.Effect, dec.DeniedBy)
			}
		})
	}
}

// A aprovação verificada destrava ESTA call, sem mudar o rótulo; uma segunda call igual noutro
// passo (outra preview) volta a escalar — a aprovação vale só para a call aprovada.
func TestAOS541_AprovacaoValeSoParaAquelaCallENaoMudaOTaint(t *testing.T) {
	aprovada := aos541Call("ocr_read", "cap:fs.read", taint.StringUntrusted, "s1")
	store := &storeQueCaptura{}
	m := aos541Monitor(t, true, aos541Verificador{preview: ApprovalPreview(aprovada)}, store)

	aprovada.ApprovalEvidence = []byte("OK")
	dec, err := m.Mediate(context.Background(), aprovada)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("a call aprovada tinha de correr, veio %s|%q", dec.Effect, dec.DeniedBy)
	}
	p := aos541Payload(t, store)
	if p.Context.Taint != taint.StringUntrusted || !p.HumanApproved {
		t.Fatalf("o permit tem de dizer untrusted e aprovado por humanos: %s", store.ultimo)
	}

	segunda := aos541Call("ocr_read", "cap:fs.read", taint.StringUntrusted, "s2")
	segunda.ApprovalEvidence = []byte("OK") // a evidência da primeira não serve a outra preview
	dec, err = m.Mediate(context.Background(), segunda)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectEscalate || dec.DeniedBy != "taint" {
		t.Fatalf("a segunda call igual tinha de voltar a escalar, veio %s|%q", dec.Effect, dec.DeniedBy)
	}
}

// O gate escalante continua a ser um TaintGate para a postura: a via endurecida reconhece-o como
// presente e eficaz.
func TestAOS541_GateEscalanteContaComoBarreiraActiva(t *testing.T) {
	priv := NewStaticPrivilegedSet("cap:http.post")
	m := New(WithHooks(NewEscalatingTaintGate(priv)))
	if !m.hasWiredTaintGate() || !m.HasActiveTaintGate() {
		t.Fatal("o gate escalante tem de contar como barreira de taint presente e eficaz")
	}
}
