package main

// AOS-368 (EPIC-25) — o documento OTLP que o NÓ exporta passa a ter IDENTIDADE de recurso
// (service.name) e a ESPÉCIE correcta da chamada ao modelo (CLIENT), verificado ponta-a-ponta
// contra o colector falso em processo (a mesma harness de observability_test.go).
//
// É a prova do CONTROLO NEGATIVO exigido pela AC: se a injecção de recurso for retirada por
// mutação (WithServiceName deixar de povoar o recurso, ou o exporter deixar de passar o
// service.name), a asserção de service.name AVERMELHA; e um span sem espécie declarada tem de
// continuar a serializar INTERNAL (compatibilidade dos produtores existentes).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// firstResourceServiceName extrai o service.name do primeiro resourceSpans de cada corpo POSTado.
func resourceServiceNames(t *testing.T, col *otlpCollector) []string {
	t.Helper()
	col.mu.Lock()
	defer col.mu.Unlock()
	var out []string
	for _, b := range col.bodies {
		var doc otlpDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("corpo OTLP mal-formado: %v", err)
		}
		for _, rs := range doc.ResourceSpans {
			for _, a := range rs.Resource.Attributes {
				if a.Key == otelgenai.ServiceNameAttr && a.Value.StringValue != nil {
					out = append(out, *a.Value.StringValue)
				}
			}
		}
	}
	return out
}

// TestAOS368ModelSpanExportsServiceNameAndClientKind serve um colector falso, exporta pelo
// exporter OTLP/HTTP REAL do nó uma chamada ao modelo (OpChat) e afirma, sobre o CORPO POSTado:
//   - o recurso tem service.name (a identidade do produtor — deixou de ser unknown_service);
//   - o span do modelo sai com espécie CLIENT (wire kind 3), distinguível de trabalho interno.
func TestAOS368ModelSpanExportsServiceNameAndClientKind(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	exp, err := NewOTLPHTTPExporter(srv.URL,
		WithOTLPHTTPClient(srv.Client()),
		WithOTLPServiceName("aos-node-teste"))
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	tracer := otelgenai.NewTracer(exp)

	// A chamada ao modelo passa por StartSpan(ctx, OpChat) — o mesmo caminho de gateway.go/
	// loop.go/planner.go — logo a espécie CLIENT é atribuída aqui sem tocar nos produtores.
	_, span := tracer.StartSpan(context.Background(), otelgenai.OpChat)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpChat)
	span.SetAttribute(otelgenai.AttrRequestModel, "model:test-obs")
	span.End()

	if err := exp.Close(); err != nil {
		t.Fatalf("exporter Close: %v", err)
	}

	// (a) IDENTIDADE: o recurso carrega service.name (controlo negativo: sem a injecção, isto
	// avermelha).
	names := resourceServiceNames(t, col)
	found := false
	for _, n := range names {
		if n == "aos-node-teste" {
			found = true
		}
	}
	if !found {
		t.Fatalf("service.name %q ausente no recurso OTLP exportado; vistos: %v", "aos-node-teste", names)
	}

	// (b) ESPÉCIE: o span do modelo sai CLIENT (wire kind 3).
	spans := col.spans(t)
	var chat *otlpSpanWire
	for i := range spans {
		if spans[i].Name == otelgenai.OpChat {
			chat = &spans[i]
			break
		}
	}
	if chat == nil {
		t.Fatalf("faltou o span %q; nomes: %v", otelgenai.OpChat, names)
	}
	if chat.Kind != 3 {
		t.Errorf("span do modelo: kind = %d, esperava 3 (CLIENT) — a chamada ao modelo tem de ser distinguível de trabalho interno", chat.Kind)
	}
}

// TestAOS368DefaultServiceNameIsNeverEmpty prova que o exporter NUNCA serializa sem identidade:
// sem WithOTLPServiceName, o default determinista "aos" é emitido no recurso. É o complemento
// da AC "o exportador deixa de poder serializar sem identidade".
func TestAOS368DefaultServiceNameIsNeverEmpty(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	exp, err := NewOTLPHTTPExporter(srv.URL, WithOTLPHTTPClient(srv.Client())) // SEM service name explícito
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	tracer := otelgenai.NewTracer(exp)
	_, span := tracer.StartSpan(context.Background(), otelgenai.OpExecuteTool)
	span.End()
	if err := exp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	names := resourceServiceNames(t, col)
	if len(names) == 0 {
		t.Fatal("o exporter serializou SEM service.name — devia aplicar o default determinista \"aos\"")
	}
	for _, n := range names {
		if n != defaultOTLPServiceName {
			t.Errorf("service.name default = %q, esperava %q", n, defaultOTLPServiceName)
		}
	}

	// Compatibilidade: um span sem espécie declarada (execute_tool via StartSpan é INTERNAL)
	// continua a sair INTERNAL (kind 1).
	spans := col.spans(t)
	if len(spans) == 0 {
		t.Fatal("nenhum span exportado")
	}
	if spans[0].Kind != 1 {
		t.Errorf("span execute_tool: kind = %d, esperava 1 (INTERNAL)", spans[0].Kind)
	}
}
