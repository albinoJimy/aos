package plannerevents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-503 — O FACTO DA NOVA TENTATIVA NO LOG DO PLANO (ADR-039).
//
// `plan.node_attempt_started` diz, antes do pedido ao nó, que a tentativa `attempt` do run de um
// nó do plano vai ser pedida, e que run a precedeu. É dele que uma retoma lê em que tentativa
// cada nó ficou. Sem conteúdo: ids, um inteiro e uma razão de um enum de um só valor.

func aos503Leitor() plan.Node {
	return plan.Node{NodeID: "read_notes", Role: "reader", Objective: "o",
		Tools: []plan.ToolRef{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:d"}}}
}

func aos503Facto(tentativa int) NodeAttemptStartedPayload {
	anterior := "run-503~read_notes"
	if tentativa > 2 {
		anterior += "~2"
	}
	return NodeAttemptStartedPayload{PlanID: "plano-503", NodeID: "read_notes", Attempt: tentativa, RetryOf: anterior, Reason: AttemptReasonContractUnmetNoCall}
}

// TestAOS503_Facto_AFormaExacta: os bytes do evento.
func TestAOS503_Facto_AFormaExacta(t *testing.T) {
	p, err := NewNodeAttemptStarted(aos503Facto(2), aos503Leitor())
	if err != nil {
		t.Fatalf("NewNodeAttemptStarted: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if quer := `{"plan_id":"plano-503","node_id":"read_notes","attempt":2,"retry_of":"run-503~read_notes","reason":"contract_unmet_no_call"}`; string(raw) != quer {
		t.Fatalf("o facto:\n  quero: %s\n  veio:  %s", quer, raw)
	}
}

// TestAOS503_Facto_OConstrutorRecusa: cada forma que o facto não admite.
func TestAOS503_Facto_OConstrutorRecusa(t *testing.T) {
	verificador := aos503Leitor()
	verificador.Role = plan.RoleVerifier
	outroNo := aos503Leitor()
	outroNo.NodeID = "outro"
	for nome, c := range map[string]struct {
		mudar func(*NodeAttemptStartedPayload)
		no    plan.Node
	}{
		"sem-plano":               {func(p *NodeAttemptStartedPayload) { p.PlanID = "" }, aos503Leitor()},
		"node-id-fora-da-forma":   {func(p *NodeAttemptStartedPayload) { p.NodeID = "read notes" }, aos503Leitor()},
		"no-que-nao-e-o-do-facto": {func(*NodeAttemptStartedPayload) {}, outroNo},
		"verificador":             {func(*NodeAttemptStartedPayload) {}, verificador},
		"tentativa-1":             {func(p *NodeAttemptStartedPayload) { p.Attempt = 1 }, aos503Leitor()},
		"tentativa-0":             {func(p *NodeAttemptStartedPayload) { p.Attempt = 0 }, aos503Leitor()},
		"tentativa-negativa":      {func(p *NodeAttemptStartedPayload) { p.Attempt = -2 }, aos503Leitor()},
		"tentativa-4":             {func(p *NodeAttemptStartedPayload) { p.Attempt = MaxNodeAttempt + 1 }, aos503Leitor()},
		"razao-vazia":             {func(p *NodeAttemptStartedPayload) { p.Reason = "" }, aos503Leitor()},
		"outra-razao":             {func(p *NodeAttemptStartedPayload) { p.Reason = "contract_unmet_after_denial" }, aos503Leitor()},
		"razao-com-texto":         {func(p *NodeAttemptStartedPayload) { p.Reason = "o modelo disse que nao conseguia" }, aos503Leitor()},
		"sem-retry-of":            {func(p *NodeAttemptStartedPayload) { p.RetryOf = "" }, aos503Leitor()},
		"retry-of-com-espaco":     {func(p *NodeAttemptStartedPayload) { p.RetryOf = "run 503~n1" }, aos503Leitor()},
		"retry-of-com-quebra":     {func(p *NodeAttemptStartedPayload) { p.RetryOf = "run-503\n~n1" }, aos503Leitor()},
		"retry-of-enorme":         {func(p *NodeAttemptStartedPayload) { p.RetryOf = strings.Repeat("a", maxRetryOfBytes+1) }, aos503Leitor()},
	} {
		p := aos503Facto(2)
		c.mudar(&p)
		if _, err := NewNodeAttemptStarted(p, c.no); !errors.Is(err, ErrInvalidNodeAttempt) {
			t.Errorf("%s: queria ErrInvalidNodeAttempt; veio %v", nome, err)
		}
	}
	if MaxNodeAttempt != 3 {
		t.Fatalf("a decisao do dono e duas tentativas a mais: a maior tentativa e a terceira; esta a %d", MaxNodeAttempt)
	}
	if _, err := NewNodeAttemptStarted(aos503Facto(3), aos503Leitor()); err != nil {
		t.Fatalf("a terceira tentativa e admitida: %v", err)
	}
}

// TestAOS503_Facto_UmPorTentativaEAPrimeiraFica: o passo é por (nó, tentativa). Gravar outra vez a
// mesma tentativa é o MESMO facto — é o que deixa uma retoma voltar a gravar sem duplicar —, e a
// tentativa seguinte é outro.
func TestAOS503_Facto_UmPorTentativaEAPrimeiraFica(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rec, err := NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	no := aos503Leitor()
	primeiro, err := rec.RecordNodeAttemptStarted(ctx, aos503Facto(2), no)
	if err != nil {
		t.Fatalf("o facto da tentativa 2: %v", err)
	}
	repetido := aos503Facto(2)
	repetido.RetryOf = "run-503~outro"
	if seq, err := rec.RecordNodeAttemptStarted(ctx, repetido, no); err != nil || seq != primeiro {
		t.Fatalf("gravar outra vez a tentativa 2 tem de ser o MESMO facto (seq %d); veio %d, %v", primeiro, seq, err)
	}
	terceiro, err := rec.RecordNodeAttemptStarted(ctx, aos503Facto(3), no)
	if err != nil || terceiro == primeiro {
		t.Fatalf("a tentativa 3 e outro facto: seq %d (a 2 e %d), %v", terceiro, primeiro, err)
	}
	if _, err := rec.RecordNodeAttemptStarted(ctx, aos503Facto(4), no); !errors.Is(err, ErrInvalidNodeAttempt) {
		t.Fatalf("o gravador passa SEMPRE pelo construtor: a tentativa 4 e recusada; veio %v", err)
	}
	seq, err := Reconstruct(ctx, store, "plano-503")
	if err != nil {
		t.Fatalf("Reconstruct: %v — o tipo novo tem de estar no catalogo do dominio", err)
	}
	if len(seq) != 2 || seq[0].Type != EventNodeAttemptStarted || seq[0].StepID != "planstep:node_attempt_started:read_notes:2" || seq[1].StepID != "planstep:node_attempt_started:read_notes:3" {
		t.Fatalf("queria dois factos, um por tentativa, com o passo por (no, tentativa): %+v", seq)
	}
	var lido NodeAttemptStartedPayload
	if err := json.Unmarshal(seq[0].Payload, &lido); err != nil || lido.RetryOf != "run-503~read_notes" {
		t.Fatalf("o facto que fica e o PRIMEIRO; ficou %+v, %v", lido, err)
	}
	if !knownType(EventNodeAttemptStarted) || !strings.HasPrefix(EventNodeAttemptStarted, "plan.") {
		t.Fatal("o tipo novo tem de pertencer ao catalogo e a familia plan.*")
	}
}
