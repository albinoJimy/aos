package plannerevents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// AOS-500 — o `plan.payload_published` e a origem declarada de uma saída.
//
// O AOS-500 não mudou o evento: o que mudou foi uma coisa que o evento DERIVA do documento
// aprovado — o `contract_digest`. A origem no evento (`source`) chegou com o AOS-501, e os
// testes dela estão em aos501_origem_no_evento_test.go:
//
//   - de um contrato SEM `from_tool`, o evento é byte a byte o de antes (literal congelado);
//   - de um contrato COM `from_tool`, o digest é outro, pelo que uma referência publicada sob um
//     contrato não serve o outro.

// eventoSemOrigemCongelado é o corpo do `plan.payload_published` de `recolha.achados`, tirado
// com o código ANTERIOR ao campo `from_tool`.
const eventoSemOrigemCongelado = `{"plan_id":"plan-aos500","node_id":"recolha","output":"achados","type":"record","taint":"untrusted","contract_digest":"sha256:4c589d9f56ef2492a10a7f38db89368819ef9084ad8994824c7a08a848975494","record":{"store":"eventstore","stream":"run-aos500~recolha","digest":"sha256:conteudo"}}`

func aos500Ref() PayloadPublishedPayload {
	return PayloadPublishedPayload{
		PlanID: "plan-aos500", NodeID: "recolha", Output: "achados",
		Record: PayloadRecordRef{Store: PayloadStoreEventStore, Stream: "run-aos500~recolha", Digest: "sha256:conteudo"},
	}
}

func TestAOS500_EventoDeUmContratoSemOrigemEOdeAntes(t *testing.T) {
	produtor := plan.Node{NodeID: "recolha", Role: "searcher", Objective: "o",
		Outputs: []plan.Output{{Name: "achados", Type: plan.PayloadRecord}}}
	p, err := NewPayloadPublished(aos500Ref(), produtor)
	if err != nil {
		t.Fatalf("NewPayloadPublished: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != eventoSemOrigemCongelado {
		t.Fatalf("o evento de um contrato sem origem mudou:\n got=%s\nwant=%s", raw, eventoSemOrigemCongelado)
	}
}

// Desde o AOS-501 um contrato com origem só se publica com `source`: a referência é a de
// [aos501RefPorReferencia]. O que este teste prende continua a ser o do AOS-500 — o digest do
// contrato é outro — e passa a prender também que a única chave nova é `source`.
func TestAOS500_EventoDeUmContratoComOrigemTemOutroDigestESoAChaveSource(t *testing.T) {
	produtor := plan.Node{NodeID: "recolha", Role: "searcher", Objective: "o",
		Tools:   []plan.ToolRef{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:d"}},
		Outputs: []plan.Output{{Name: "achados", Type: plan.PayloadRecord, FromTool: "doc_read"}}}
	p, err := NewPayloadPublished(aos501RefPorReferencia(), produtor)
	if err != nil {
		t.Fatalf("NewPayloadPublished: %v", err)
	}
	if p.ContractDigest != plan.OutputDigest(produtor, produtor.Outputs[0]) {
		t.Fatal("o contract_digest do evento nao e o do contrato do documento")
	}
	if strings.Contains(eventoSemOrigemCongelado, p.ContractDigest) {
		t.Fatal("o contrato com origem deu o digest do contrato sem ela")
	}
	if p.Taint != plan.TaintUntrusted {
		t.Fatalf("taint do evento = %q; uma saida por referencia continua untrusted", p.Taint)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// As chaves são as de sempre, mais `source` (AOS-501). `from_tool` não é chave do evento.
	var chaves map[string]json.RawMessage
	if err := json.Unmarshal(raw, &chaves); err != nil {
		t.Fatal(err)
	}
	if _, ha := chaves["from_tool"]; ha {
		t.Fatalf("o evento ganhou a chave from_tool; a origem vai em `source`:\n%s", raw)
	}
	if _, ha := chaves["source"]; !ha || len(chaves) != 8 {
		t.Fatalf("o evento de um contrato com origem tem as 7 chaves de sempre e `source`; tem %d:\n%s", len(chaves), raw)
	}
}
