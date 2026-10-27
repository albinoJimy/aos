package attribution_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
)

// AOS-565 — O FORNECEDOR A MONTANTE NA ATRIBUIÇÃO, E A ORDEM QUE O OBRIGA A ENTRAR POR OUTRA
// PORTA.
//
// O selo de atribuição é PRÉ-EFEITO (audit-before-effect, ADR-010): quando ele é escrito, o
// fornecedor efectivo ainda não existe. Daí a regra deste ticket, que estes testes prendem:
//
//   - [attribution.Recorder.Record] (o selo) NÃO conhece o campo — um registo com ele selado
//     tem de produzir os MESMOS bytes de um sem ele;
//   - [attribution.Recorder.NoteUpstreamProvider] (pós-efeito) anota o span e emite para o
//     sink, e NÃO acrescenta registo nenhum ao WORM — a cadeia continua a ter exactamente um
//     registo por chamada, que é o que o burn-down e a reconciliação contam.

// aos565Registo é o registo de referência destes testes.
func aos565Registo() attribution.Record {
	return attribution.Record{
		UserID: "user-1", AgentID: "agent-1", AgentClass: "executor", HumanRoot: "user-1",
		DelegationChain: []attribution.Hop{{Sub: "user-1", ActAs: "agent-1"}},
		Model:           "gpt-4o-mini", Region: "eu-west", KeyID: "k1", Operation: "chat",
		PolicyVersion: "v1", RunID: "run-1", StepID: "s1",
		Timestamp: time.Unix(1700000000, 0).UTC(),
	}
}

// TestAOS565_OSeloPreEfeitoNaoMudaComOFornecedor — o campo novo NÃO entra no WORM pelo caminho
// de sempre. Se entrasse, cada chamada selaria um valor que no momento do selo ainda não
// existia.
func TestAOS565_OSeloPreEfeitoNaoMudaComOFornecedor(t *testing.T) {
	t.Parallel()
	semFornecedor, err := attribution.NewRecorder(audit.NewMemStore()).Seal(context.Background(), aos565Registo())
	if err != nil {
		t.Fatalf("selo: %v", err)
	}
	com := aos565Registo()
	com.UpstreamProvider = "Google"
	comFornecedor, err := attribution.NewRecorder(audit.NewMemStore()).Seal(context.Background(), com)
	if err != nil {
		t.Fatalf("selo: %v", err)
	}
	sem, com2 := hex.EncodeToString(semFornecedor.EntryHash), hex.EncodeToString(comFornecedor.EntryHash)
	if sem != com2 {
		t.Fatalf("o selo pre-efeito mudou: %s != %s", sem, com2)
	}
}

// TestAOS565_NoteUpstreamProvider — anota e emite, e não sela.
func TestAOS565_NoteUpstreamProvider(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	var emitidos []attribution.Record
	rec := attribution.NewRecorder(store, attribution.WithSink(attribution.SinkFunc(
		func(_ context.Context, r attribution.Record) { emitidos = append(emitidos, r) })))
	tracer := &agentruntime.RecordingTracer{}
	_, span := tracer.StartSpan(context.Background(), agentruntime.OpChat)

	// Sem fornecedor medido é no-op: nem span, nem sink.
	rec.NoteUpstreamProvider(context.Background(), span, aos565Registo())
	if len(emitidos) != 0 {
		t.Fatalf("sem fornecedor medido nao se emite nada: %+v", emitidos)
	}

	com := aos565Registo()
	com.UpstreamProvider = "Google"
	rec.NoteUpstreamProvider(context.Background(), span, com)
	span.End()
	if len(emitidos) != 1 || emitidos[0].UpstreamProvider != "Google" {
		t.Fatalf("o sink tem de receber o fornecedor a montante: %+v", emitidos)
	}
	if len(store.Partitions()) != 0 {
		t.Fatalf("a anotacao pos-efeito NAO sela: particoes = %v", store.Partitions())
	}
	attrs := tracer.SpansByOperation(agentruntime.OpChat)[0].Attributes
	if attrs[attribution.AttrUpstreamProvider] != "Google" {
		t.Fatalf("o span tem de levar o fornecedor a montante: %+v", attrs)
	}
	// E um span nil não parte nada (um gateway sem tracer composto).
	rec.NoteUpstreamProvider(context.Background(), nil, com)
}
