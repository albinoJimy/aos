package integration

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	memory "github.com/aos-ref/platform/memory"
	memadapters "github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
)

// PII SINTÉTICA — valores INVENTADOS, nunca reais. secrets.sh não os pode confundir
// com material verdadeiro (domínio example.com reservado por RFC 2606; PAN de teste).
// A prova falsificável assenta em que ESTE valor em claro NUNCA aparece nas portas
// quando o motor está ligado, e APARECE quando está desligado.
const (
	synthEmail = "alice.tester@example.com"
	synthPAN   = "4111111111111111" // PAN de teste Visa (Luhn-válido), não emitido
)

// episodicStream é o stream do Event Store onde o EventStoreAdapter escreve a classe
// episódica (streamPrefix "memory." + "episodic").
const episodicStream = "memory.episodic"

// buildGateway compõe um gateway de ingestão sobre substrato REAL (Event Store,
// memory.Service, SpanTracer→RecordingExporter, WORM). withEngine=false injecta
// ingestor nil — o motor DESLIGADO, para a prova falsificável.
func buildGateway(t *testing.T, withEngine bool) (*IngestionGateway, *eventstore.Store, *otelgenai.RecordingExporter, audit.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	exporter := &otelgenai.RecordingExporter{}
	tracer := otelgenai.NewTracer(exporter)
	memPort := memadapters.NewEventStoreAdapter(es, memadapters.WithEventStoreTracer(tracer))
	memSvc := memory.NewService(memPort)
	worm := audit.NewMemStore()

	var ingestor *redaction.Ingestor
	if withEngine {
		// MESMA disciplina do nó de produção: RemoveAllPolicy (minimização), sem KeySource.
		ingestor = redaction.NewIngestor(redaction.NewEngine(nil), redaction.RemoveAllPolicy("test-v1"))
	}
	gw, err := NewIngestionGateway(ingestor, memSvc, tracer, worm)
	if err != nil {
		t.Fatalf("NewIngestionGateway: %v", err)
	}
	return gw, es, exporter, worm
}

// readEpisodicGoal lê o objectivo persistido no Event Store para runID (via o stream
// de memória episódica) — a prova de que o valor está NO EVENT STORE, não só em RAM.
func readEpisodicGoal(t *testing.T, es *eventstore.Store, runID string) string {
	t.Helper()
	events, err := es.Read(context.Background(), episodicStream, 1)
	if err != nil {
		t.Fatalf("es.Read(%s): %v", episodicStream, err)
	}
	for _, ev := range events {
		if ev.Type != memadapters.EventTypeWritten {
			continue
		}
		rec, derr := domain.UnmarshalRecord(ev.Payload)
		if derr != nil {
			t.Fatalf("UnmarshalRecord: %v", derr)
		}
		if rec.Metadata.RunID != runID {
			continue
		}
		body, ok := rec.Body.(domain.EpisodicBody)
		if !ok {
			t.Fatalf("body de tipo inesperado %T", rec.Body)
		}
		return body.Goal
	}
	t.Fatalf("nenhum registo episodico para run %q no Event Store", runID)
	return ""
}

// spanObjective lê o atributo do objectivo do span de ingestão EXPORTADO.
func spanObjective(t *testing.T, exporter *otelgenai.RecordingExporter) string {
	t.Helper()
	spans := exporter.SpansByName(opIngestRedacted)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span %q exportado, obtive %d", opIngestRedacted, len(spans))
	}
	v, ok := spans[0].Attribute(attrObjectiveRedacted)
	if !ok {
		t.Fatalf("span sem atributo %q", attrObjectiveRedacted)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("atributo do objectivo de tipo %T (esperado string)", v)
	}
	return s
}

// TestIngestionRedactsAcrossEventStoreAndSpans é a PROVA FALSIFICÁVEL de AOS-208: um
// objectivo com PII sintética produz, NO Event Store E nos spans exportados, o valor
// REDIGIDO — e o valor em claro NUNCA aparece. A não-vacuidade é garantida pelo
// sub-teste do motor DESLIGADO (ver TestIngestionDisabledLeaksCleartext): a MESMA
// asserção falha quando o motor não está ligado, porque o claro passa a alcançar as
// portas.
func TestIngestionRedactsAcrossEventStoreAndSpans(t *testing.T) {
	gw, es, exporter, worm := buildGateway(t, true)
	const runID = "run-redact-1"
	const subject = "nhi:agent-1"
	objective := "Envia o relatorio para " + synthEmail + " e cobra o cartao " + synthPAN

	out, err := gw.IngestObjective(context.Background(), subject, runID, subject, objective)
	if err != nil {
		t.Fatalf("IngestObjective: %v", err)
	}

	// O objectivo devolvido (que segue para o loop do run) está MINIMIZADO.
	assertRedacted(t, "objectivo devolvido", out.Redacted)

	// PORTA Event Store + memory: o valor no store está redigido, o claro ausente.
	assertRedacted(t, "Event Store (memory.episodic)", readEpisodicGoal(t, es, runID))

	// PORTA otel-genai: o span exportado transporta o valor redigido, o claro ausente.
	assertRedacted(t, "span exportado", spanObjective(t, exporter))

	// PORTA audit: o WORM selou o HASH do payload JÁ TRATADO (nunca o cru).
	recs, err := worm.Read(context.Background(), "ingestion:"+runID, 1, 1<<62)
	if err != nil || len(recs) != 1 {
		t.Fatalf("WORM Read ingestion:%s: err=%v n=%d", runID, err, len(recs))
	}
	wantHash := sha256.Sum256([]byte(out.Redacted))
	if string(recs[0].PayloadRef.ContentHash) != string(wantHash[:]) {
		t.Fatalf("audit selou hash do payload errado (esperava hash do redigido)")
	}
	if len(recs[0].Obligations) == 0 || recs[0].Obligations[0].Type != "redact_pii" {
		t.Fatalf("audit sem obrigacao redact_pii: %+v", recs[0].Obligations)
	}

	// PROVA de AUSÊNCIA por [Engine.Scan]: o payload redigido não tem PII detectável; o
	// cru TEM — o que torna a asserção acima não-vacua por construção.
	engine := redaction.NewEngine(nil)
	if f := engine.ScanText(out.Redacted); len(f) != 0 {
		t.Fatalf("Scan do redigido devolveu PII (esperava vazio): %+v", f)
	}
	if f := engine.ScanText(objective); len(f) == 0 {
		t.Fatalf("Scan do cru NAO devolveu PII — o objectivo de teste nao carrega PII detectavel (asserção seria vacua)")
	}
}

// TestIngestionDisabledLeaksCleartext é o PIVOT da falsificabilidade: com o motor
// DESLIGADO (ingestor nil), o valor EM CLARO alcança o Event Store E os spans. Isto
// prova que a asserção de TestIngestionRedactsAcrossEventStoreAndSpans não é vacua —
// se o motor não estivesse ligado, o teste positivo FALHARIA (o claro apareceria).
func TestIngestionDisabledLeaksCleartext(t *testing.T) {
	gw, es, exporter, _ := buildGateway(t, false) // motor DESLIGADO
	const runID = "run-leak-1"
	const subject = "nhi:agent-1"
	objective := "Envia para " + synthEmail

	out, err := gw.IngestObjective(context.Background(), subject, runID, subject, objective)
	if err != nil {
		t.Fatalf("IngestObjective: %v", err)
	}
	if !strings.Contains(out.Redacted, synthEmail) {
		t.Fatalf("motor desligado deveria devolver o objectivo EM CLARO, obtive %q", out.Redacted)
	}

	// A MESMA asserção positiva aplicada aqui FALHARIA — o claro está presente nas portas.
	esGoal := readEpisodicGoal(t, es, runID)
	if !strings.Contains(esGoal, synthEmail) {
		t.Fatalf("com o motor desligado o Event Store deveria conter o claro, obtive %q", esGoal)
	}
	if spanGoal := spanObjective(t, exporter); !strings.Contains(spanGoal, synthEmail) {
		t.Fatalf("com o motor desligado o span deveria conter o claro, obtive %q", spanGoal)
	}
	// Confirma explicitamente que a asserção de redacção FALHA aqui (não-vacuidade).
	if redactedContains(esGoal) {
		t.Fatalf("Event Store nao deveria estar redigido com o motor desligado: %q", esGoal)
	}
}

// TestIngestionGatewayFailClosed prova que colaboradores obrigatórios em falta são
// recusados fail-closed (nenhuma porta silenciosamente omissa).
func TestIngestionGatewayFailClosed(t *testing.T) {
	worm := audit.NewMemStore()
	tracer := otelgenai.NoopTracer{}
	memSvc := memory.NewService(memadapters.NewEventStoreAdapter(mustES(t)))
	cases := []struct {
		name    string
		mem     *memory.Service
		tracer  otelgenai.Tracer
		worm    audit.Store
		wantErr error
	}{
		{"sem memory", nil, tracer, worm, ErrNoIngestionMemory},
		{"sem tracer", memSvc, nil, worm, ErrNoIngestionTracer},
		{"sem worm", memSvc, tracer, nil, ErrNoIngestionWORM},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewIngestionGateway(nil, c.mem, c.tracer, c.worm)
			if err != c.wantErr {
				t.Fatalf("erro = %v, esperava %v", err, c.wantErr)
			}
		})
	}
}

func mustES(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return es
}

// assertRedacted exige que value contenha os marcadores de redacção e NÃO contenha a
// PII sintética em claro.
func assertRedacted(t *testing.T, where, value string) {
	t.Helper()
	if strings.Contains(value, synthEmail) || strings.Contains(value, synthPAN) {
		t.Fatalf("%s: PII EM CLARO presente (esperava redigido): %q", where, value)
	}
	if !redactedContains(value) {
		t.Fatalf("%s: sem marcador de redacção (esperava [REDACTED:...]): %q", where, value)
	}
}

func redactedContains(value string) bool {
	return strings.Contains(value, "[REDACTED:email]") || strings.Contains(value, "[REDACTED:credit_card]")
}
