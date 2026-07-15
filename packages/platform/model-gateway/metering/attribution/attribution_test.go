package attribution_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
)

// TestRecord_EmptyPrincipal_FailClosed prova a guarda de defesa-em-profundidade
// (Q2 AOS-057): um registo sem principal resolvido (user/agente vazios) é RECUSADO
// por Record E por Seal com ErrNoPrincipal — nunca sela uma chamada "atribuída a
// ninguém" no WORM, mesmo que o composition root ligue a atribuição sem um estágio
// de authn real. A invariante central ("nunca o pool, nunca ninguém") é imposta
// pelo tipo, não só pela convenção de wiring.
func TestRecord_EmptyPrincipal_FailClosed(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	rec := attribution.NewRecorder(store)
	base := sampleRecord()

	cases := []struct {
		name string
		mut  func(attribution.Record) attribution.Record
	}{
		{"user vazio", func(r attribution.Record) attribution.Record { r.UserID = ""; return r }},
		{"agente vazio", func(r attribution.Record) attribution.Record { r.AgentID = ""; return r }},
		{"ambos vazios", func(r attribution.Record) attribution.Record { r.UserID = ""; r.AgentID = ""; return r }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.mut(base)
			if _, err := rec.Seal(context.Background(), r); !errors.Is(err, attribution.ErrNoPrincipal) {
				t.Fatalf("Seal: err = %v, quer ErrNoPrincipal", err)
			}
			if err := rec.Record(context.Background(), nil, r); !errors.Is(err, attribution.ErrNoPrincipal) {
				t.Fatalf("Record: err = %v, quer ErrNoPrincipal", err)
			}
		})
	}
	// Nada foi selado: a cadeia da partição do humano responsável continua vazia.
	head, _ := store.Head(context.Background(), "modelgw:human:alice")
	if head != 0 {
		t.Fatalf("WORM head = %d, quer 0 (nada selado sem principal)", head)
	}
}

func sampleRecord() attribution.Record {
	return attribution.Record{
		UserID: "alice", AgentID: "agent-1", AgentClass: "reader",
		HumanRoot:       "human:alice",
		DelegationChain: []attribution.Hop{{Sub: "human:alice", ActAs: "agent-1"}},
		Model:           "gpt-x", Region: "eu", KeyID: "acct-eu-1",
		Operation: "chat", PolicyVersion: "gw-token-policy/v1#abc123",
		Timestamp: time.Unix(1_700_000_000, 0),
	}
}

// TestSeal_WORM sela o registo no audit WORM e confirma que o principal, o modelo
// e a região ficam na cadeia tamper-evident — nunca "o pool".
func TestSeal_WORM(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	rec := attribution.NewRecorder(store)
	sealed, err := rec.Seal(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.AuditSeq != 1 || len(sealed.EntryHash) == 0 {
		t.Fatalf("registo nao selado na cadeia: seq=%d hash=%x", sealed.AuditSeq, sealed.EntryHash)
	}
	if sealed.Principal.NHIID != "agent-1" {
		t.Fatalf("principal selado = %q, quer agent-1 (nunca o pool)", sealed.Principal.NHIID)
	}
	if sealed.Resource.Value != "gpt-x" || sealed.Resource.Region != "eu" {
		t.Fatalf("modelo/regiao nao selados: %+v", sealed.Resource)
	}
	if sealed.Capability != "model:invoke" {
		t.Fatalf("capability = %q", sealed.Capability)
	}
	if sealed.PolicyVersion != "gw-token-policy/v1#abc123" {
		t.Fatalf("versao da politica nao selada: %q", sealed.PolicyVersion)
	}
	if len(sealed.Principal.DelegationChain) == 0 || sealed.Principal.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("cadeia on-behalf-of nao selada: %+v", sealed.Principal.DelegationChain)
	}
}

// TestAnnotate_Span_NoSecret confirma que o span recebe principal/modelo/região +
// KeyID NÃO-SECRETO, e NADA que se pareça com um segredo.
func TestAnnotate_Span_NoSecret(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}
	_, span := tr.StartSpan(context.Background(), agentruntime.OpChat)
	rec := attribution.NewRecorder(nil)
	rec.Annotate(span, sampleRecord())
	span.End()

	attrs := tr.SpansByOperation(agentruntime.OpChat)[0].Attributes
	if attrs[attribution.AttrPrincipalUser] != "alice" || attrs[attribution.AttrPrincipalAgent] != "agent-1" {
		t.Fatalf("principal ausente no span: %+v", attrs)
	}
	if attrs[attribution.AttrRegion] != "eu" || attrs[attribution.AttrKeyID] != "acct-eu-1" {
		t.Fatalf("regiao/keyid ausentes no span: %+v", attrs)
	}
	if attrs[attribution.AttrPrincipalHuman] != "human:alice" {
		t.Fatalf("humano responsavel ausente: %+v", attrs)
	}
	// O KeyID é NÃO-secreto (id de conta). Nenhum atributo contém material sensível.
	for k, v := range attrs {
		if s, ok := v.(string); ok && (s == "sk-teste" || s == "secret") {
			t.Fatalf("segredo aparente no atributo %q: %q", k, s)
		}
	}
}

// TestRecord_AnnotatesSealsAndEmits: Record faz as três coisas — anota o span,
// sela no WORM e emite para o Sink.
func TestRecord_AnnotatesSealsAndEmits(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	var emitted []attribution.Record
	rec := attribution.NewRecorder(store, attribution.WithSink(attribution.SinkFunc(
		func(_ context.Context, r attribution.Record) { emitted = append(emitted, r) },
	)))
	tr := &agentruntime.RecordingTracer{}
	_, span := tr.StartSpan(context.Background(), agentruntime.OpChat)

	if err := rec.Record(context.Background(), span, sampleRecord()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(emitted) != 1 || emitted[0].PrincipalID() != "user:alice;agent:agent-1" {
		t.Fatalf("sink nao recebeu o registo: %+v", emitted)
	}
	head, _ := store.Head(context.Background(), "modelgw:human:alice")
	if head != 1 {
		t.Fatalf("WORM head = %d, quer 1", head)
	}
}

// TestSeal_NoStore_NoOp: sem store, a selagem é no-op (não entra em pânico).
func TestSeal_NoStore_NoOp(t *testing.T) {
	t.Parallel()
	rec := attribution.NewRecorder(nil)
	if _, err := rec.Seal(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Seal sem store: %v", err)
	}
}
