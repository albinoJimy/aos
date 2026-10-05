package plannerevents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// AOS-500 — o `plan.payload_published` e a origem declarada de uma saída.
//
// Este ticket NÃO muda o evento: não há campo novo (a origem no evento, `source`, é do AOS-501).
// O que muda é uma coisa que o evento DERIVA do documento aprovado — o `contract_digest`:
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

func TestAOS500_EventoDeUmContratoComOrigemTemOutroDigestENenhumCampoNovo(t *testing.T) {
	produtor := plan.Node{NodeID: "recolha", Role: "searcher", Objective: "o",
		Tools:   []plan.ToolRef{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:d"}},
		Outputs: []plan.Output{{Name: "achados", Type: plan.PayloadRecord, FromTool: "doc_read"}}}
	p, err := NewPayloadPublished(aos500Ref(), produtor)
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
	// As chaves são as de sempre: este ticket não acrescenta a origem ao evento.
	var chaves map[string]json.RawMessage
	if err := json.Unmarshal(raw, &chaves); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"source", "from_tool"} {
		if _, ha := chaves[k]; ha {
			t.Fatalf("o evento ganhou a chave %q, que e do AOS-501:\n%s", k, raw)
		}
	}
	if len(chaves) != 7 {
		t.Fatalf("o evento tem %d chaves; tinha 7 (plan_id, node_id, output, type, taint, contract_digest, record):\n%s", len(chaves), raw)
	}
}
