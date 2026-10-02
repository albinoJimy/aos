package plannerevents

// aos477_proposta_test.go — O `plan.proposed` passa a levar o compromisso do objectivo recebido
// e a referência ao pedido de origem (AOS-477), como campos ADITIVOS do `aos.planner.v1`.
//
// O que se prova: (1) sem os campos novos o payload é byte-a-byte o de antes; (2) um leitor
// ANTERIOR — a struct sem os campos — lê um payload novo sem partir; (3) os dois campos, quando
// presentes, são impostos na forma pelo emissor, e um malformado não chega ao store.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// compromissoDeTeste é o VECTOR do contrato do compromisso: HMAC-SHA256 com o sal 00..1f (32
// bytes) sobre «recolher e analisar dados». O mesmo valor está nos testes do nó e do `aos-orq`,
// que o calculam cada um com a sua cópia da função.
const compromissoDeTeste = "hmac-sha256:a95479ee76699c6bd97e848784f11a25ae02d8a5f7175f376e02b071ea8e1b2f"

func TestAOS477PropostaSemCamposNovosFicaComoEra(t *testing.T) {
	p := ProposedPayload{PlanID: "plan-1", PlanHash: "sha256:h", Meta: PlannerMeta{Model: "m", PromptVersion: "1.0.0", CapabilitiesHash: "sha256:c"}, Attempt: 1}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	const antes = `{"plan_id":"plan-1","plan_hash":"sha256:h","planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"sha256:c"},"attempt":1}`
	if string(raw) != antes {
		t.Fatalf("um plan.proposed sem compromisso nem pedido tem de ser byte-a-byte o de antes:\n quer %s\n veio %s", antes, raw)
	}
}

func TestAOS477LeitorAnteriorLeAPropostaNova(t *testing.T) {
	novo := ProposedPayload{PlanID: "plan-1", PlanHash: "sha256:h", Attempt: 2,
		ObjectiveCommitment: compromissoDeTeste,
		Request:             &PlanRequestRef{Stream: "aos-internal/plan-requests", Seq: 44, RunID: "plan-e2e"}}
	raw, err := json.Marshal(novo)
	if err != nil {
		t.Fatal(err)
	}
	for _, campo := range []string{`"objective_commitment":"` + compromissoDeTeste + `"`, `"request":{"stream":"aos-internal/plan-requests","seq":44,"run_id":"plan-e2e"}`} {
		if !strings.Contains(string(raw), campo) {
			t.Fatalf("o payload novo tem de levar %s, veio %s", campo, raw)
		}
	}
	// O leitor ANTERIOR: a forma do payload antes do AOS-477, decodificada como os leitores da
	// árvore a decodificam (json.Unmarshal, sem DisallowUnknownFields).
	var antigo struct {
		PlanID   string      `json:"plan_id"`
		PlanHash string      `json:"plan_hash"`
		Meta     PlannerMeta `json:"planner_meta"`
		Attempt  int         `json:"attempt"`
	}
	if err := json.Unmarshal(raw, &antigo); err != nil {
		t.Fatalf("um leitor anterior tem de ler o payload novo: %v", err)
	}
	if antigo.PlanHash != "sha256:h" || antigo.Attempt != 2 {
		t.Fatalf("o leitor anterior leu mal os campos que conhece: %+v", antigo)
	}
}

func TestAOS477RecordProposedLevaOsCamposEOsImpoeNaForma(t *testing.T) {
	store := &captureStore{}
	rec, err := NewRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	bom := ProposedPayload{PlanID: "plan-1", PlanHash: "sha256:h", Attempt: 1, ObjectiveCommitment: compromissoDeTeste,
		Request: &PlanRequestRef{Stream: "aos-internal/plan-requests", Seq: 7, RunID: "plan"}}
	if _, err := rec.RecordProposed(context.Background(), bom); err != nil {
		t.Fatalf("RecordProposed: %v", err)
	}
	if len(store.appends) != 1 || store.appends[0].Type != EventProposed || store.appends[0].SchemaVersion != DomainVersion {
		t.Fatalf("esperava um plan.proposed em %s, veio %+v", DomainVersion, store.appends)
	}
	var lido ProposedPayload
	if err := json.Unmarshal(store.appends[0].Payload, &lido); err != nil {
		t.Fatal(err)
	}
	if lido.ObjectiveCommitment != compromissoDeTeste || lido.Request == nil || lido.Request.Seq != 7 {
		t.Fatalf("o facto apenso perdeu o compromisso ou o pedido: %+v", lido)
	}

	maus := map[string]ProposedPayload{
		"sem esquema":       {PlanID: "plan-1", ObjectiveCommitment: strings.TrimPrefix(compromissoDeTeste, CommitmentScheme)},
		"sha256 sem chave":  {PlanID: "plan-1", ObjectiveCommitment: "sha256:" + strings.TrimPrefix(compromissoDeTeste, CommitmentScheme)},
		"hex curto":         {PlanID: "plan-1", ObjectiveCommitment: CommitmentScheme + "abcd"},
		"hex maiusculo":     {PlanID: "plan-1", ObjectiveCommitment: strings.ToUpper(compromissoDeTeste)},
		"texto em claro":    {PlanID: "plan-1", ObjectiveCommitment: CommitmentScheme + strings.Repeat("o objectivo ", 6)[:64]},
		"pedido sem seq":    {PlanID: "plan-1", Request: &PlanRequestRef{Stream: "s", RunID: "r"}},
		"pedido sem stream": {PlanID: "plan-1", Request: &PlanRequestRef{Seq: 1, RunID: "r"}},
		"pedido sem run":    {PlanID: "plan-1", Request: &PlanRequestRef{Stream: "s", Seq: 1}},
	}
	for nome, p := range maus {
		if _, err := rec.RecordProposed(context.Background(), p); !errors.Is(err, ErrInvalidProposal) {
			t.Errorf("%s: esperava ErrInvalidProposal, veio %v", nome, err)
		}
	}
	if len(store.appends) != 1 {
		t.Fatalf("uma proposta malformada não pode chegar ao store: %d apensos", len(store.appends))
	}
}
