package approvalcard

import (
	"context"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// irreversibleCard constrói um card irreversível danger para o solicitante "agent-1".
func irreversibleCard(t *testing.T) ApprovalCard {
	t.Helper()
	req := risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      "cap:fs.delete -> file:/data/prod",
		Principal:    "agent-1",
		Capability:   "cap:fs.delete",
		Resource:     "/data/prod",
	}
	card, err := BuildCard(req, WithRequestID("card-irr"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	return card
}

// TestDualControl_DoisDistintosAvanca (AC3): uma acção irreversível com DOIS
// aprovadores DISTINTOS Approved é autorizada. Usa o hitl.Channel REAL (assina/sela).
func TestDualControl_DoisDistintosAvanca(t *testing.T) {
	ch, store := realChannel(t,
		[]approvalStep{{"approver-a", true}, {"approver-b", true}},
		map[string]byte{"approver-a": 1, "approver-b": 2},
	)
	coll, err := NewDualControlCollector(ch)
	if err != nil {
		t.Fatalf("NewDualControlCollector: %v", err)
	}
	dec, err := coll.Authorize(context.Background(), irreversibleCard(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("dois aprovadores distintos deviam autorizar: %s", dec.Reason)
	}
	if len(dec.Approvers) != 2 || dec.Approvers[0] == dec.Approvers[1] {
		t.Fatalf("esperava dois aprovadores distintos: %+v", dec.Approvers)
	}
	// Prova de delegação/enforcement no Channel: as decisões foram SELADAS no audit
	// (o card não sela — o Channel é que o faz).
	head, err := store.Head(context.Background(), "hitl:agent-1")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head < 2 {
		t.Fatalf("esperava >=2 decisoes seladas pelo Channel, head=%d", head)
	}
}

// TestDualControl_UmSoAprovadorNaoAvanca (AC3): um só aprovador (segunda aprovação
// ausente) NÃO avança — fail-closed.
func TestDualControl_UmSoAprovadorNaoAvanca(t *testing.T) {
	ch, _ := realChannel(t,
		[]approvalStep{{"approver-a", true}}, // só uma aprovação disponível
		map[string]byte{"approver-a": 1, "approver-b": 2},
	)
	coll, _ := NewDualControlCollector(ch)
	dec, err := coll.Authorize(context.Background(), irreversibleCard(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatal("um so aprovador NAO devia autorizar uma acao irreversivel")
	}
}

// TestDualControl_DoisIguaisNaoAvanca (AC3, a REGRA NOVA): o mesmo aprovador a aprovar
// duas vezes (self-quorum) NÃO satisfaz o dual-control — approver_1 != approver_2.
func TestDualControl_DoisIguaisNaoAvanca(t *testing.T) {
	ch, _ := realChannel(t,
		[]approvalStep{{"approver-a", true}, {"approver-a", true}},
		map[string]byte{"approver-a": 1},
	)
	coll, _ := NewDualControlCollector(ch)
	dec, err := coll.Authorize(context.Background(), irreversibleCard(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("dois aprovadores IGUAIS nao deviam autorizar (self-quorum): %s", dec.Reason)
	}
	if !strings.Contains(dec.Reason, "DISTINTOS") {
		t.Fatalf("razao devia apontar a regra do quorum distinto: %q", dec.Reason)
	}
}

// TestDualControl_SegundaRecusaNaoAvanca: primeira aprova, segunda RECUSA → deny.
func TestDualControl_SegundaRecusaNaoAvanca(t *testing.T) {
	ch, _ := realChannel(t,
		[]approvalStep{{"approver-a", true}, {"approver-b", false}},
		map[string]byte{"approver-a": 1, "approver-b": 2},
	)
	coll, _ := NewDualControlCollector(ch)
	dec, _ := coll.Authorize(context.Background(), irreversibleCard(t))
	if dec.Authorized {
		t.Fatal("uma recusa na segunda aprovacao nao devia autorizar")
	}
}

// TestReversivel_UmaAprovacaoBasta: uma acção reversível delega UMA aprovação e avança.
func TestReversivel_UmaAprovacaoBasta(t *testing.T) {
	req := risk.ConfirmationRequest{
		Class:        risk.ClassDanger, // danger mas reversível (ex.: post reversível)
		Irreversible: false,
		Preview:      "cap:http.post -> https://api/x",
		Principal:    "agent-1",
		Capability:   "cap:http.post",
		Resource:     "https://api/x",
	}
	card, err := BuildCard(req, WithRequestID("card-rev"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	ch, _ := realChannel(t,
		[]approvalStep{{"approver-a", true}},
		map[string]byte{"approver-a": 1},
	)
	coll, _ := NewDualControlCollector(ch)
	dec, err := coll.Authorize(context.Background(), card)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized || len(dec.Approvers) != 1 {
		t.Fatalf("reversivel com uma aprovacao devia autorizar: %+v", dec)
	}
}

// TestDelegacao_CanalDenyBloqueia (AC4): a decisão passa pela porta HITL; um canal que
// NEGA impede a autorização — o card NÃO decide por si.
func TestDelegacao_CanalDenyBloqueia(t *testing.T) {
	coll, err := NewDualControlCollector(risk.DenyChannel{})
	if err != nil {
		t.Fatalf("NewDualControlCollector: %v", err)
	}
	// Irreversível.
	dec, _ := coll.Authorize(context.Background(), irreversibleCard(t))
	if dec.Authorized {
		t.Fatal("DenyChannel devia impedir a autorizacao (irreversivel)")
	}
	// Reversível também é negado por um canal que nega tudo.
	rev := risk.ConfirmationRequest{Class: risk.ClassGray, Irreversible: false, Preview: "p", Principal: "a", Capability: "cap:x", Resource: "r"}
	card, _ := BuildCard(rev, WithRequestID("c"))
	dec2, _ := coll.Authorize(context.Background(), card)
	if dec2.Authorized {
		t.Fatal("DenyChannel devia impedir a autorizacao (reversivel)")
	}
}

// TestDelegacao_CardDevolveEfeitoResolvido (AC4): o card DEVOLVE ao canal um
// [risk.ConfirmationRequest] com o EFEITO CONCRETO RESOLVIDO (preview/capability/
// resource) e o Requester como Principal (base do 4-eyes). O card não assina — só
// transporta.
func TestDelegacao_CardDevolveEfeitoResolvido(t *testing.T) {
	spy := &spyChannel{resps: []risk.ConfirmationResponse{
		{Approved: true, Approver: "approver-a"},
		{Approved: true, Approver: "approver-b"},
	}}
	coll, _ := NewDualControlCollector(spy)
	card := irreversibleCard(t)
	dec, err := coll.Authorize(context.Background(), card)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("esperava autorizado com dois distintos: %s", dec.Reason)
	}
	if len(spy.seen) != 2 {
		t.Fatalf("dual-control devia submeter 2 pedidos ao canal, submeteu %d", len(spy.seen))
	}
	got := spy.seen[0]
	if got.Preview != card.Preview || got.Capability != card.Capability || got.Resource != card.Resource {
		t.Fatalf("pedido devolvido nao carrega o efeito resolvido: %+v", got)
	}
	if got.Principal != card.Requester {
		t.Fatalf("Principal devolvido devia ser o Requester (base do 4-eyes): %q != %q", got.Principal, card.Requester)
	}
	if got.Class != card.Class || got.Irreversible != card.Irreversible {
		t.Fatalf("classe/irreversivel nao propagados ao canal: %+v", got)
	}
}

// TestSpan_ApresentacaoSemSegredos (AC6): a apresentação emite um span OpApprovalCard
// ligado ao trace, com a classe/decisão/contagem — e SEM segredos/PII (nem o preview,
// nem o resource, nem identidades de aprovador) nos atributos.
func TestSpan_ApresentacaoSemSegredos(t *testing.T) {
	tracer := &agentruntime.RecordingTracer{}
	ch, _ := realChannel(t,
		[]approvalStep{{"approver-a", true}, {"approver-b", true}},
		map[string]byte{"approver-a": 1, "approver-b": 2},
	)
	coll, _ := NewDualControlCollector(ch, WithTracer(tracer))
	card := irreversibleCard(t)
	if _, err := coll.Authorize(context.Background(), card); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	spans := tracer.SpansByOperation(OpApprovalCard)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span de apresentacao, obtive %d", len(spans))
	}
	s := spans[0]
	if !s.Ended {
		t.Fatal("span de apresentacao nao foi fechado")
	}
	if s.Attributes[AttrClass] != "danger" {
		t.Fatalf("classe no span incorrecta: %v", s.Attributes[AttrClass])
	}
	if s.Attributes[AttrDecision] != "allow" {
		t.Fatalf("decisao no span incorrecta: %v", s.Attributes[AttrDecision])
	}
	if s.Attributes[AttrCardApproverCount] != 2 {
		t.Fatalf("contagem de aprovadores no span incorrecta: %v", s.Attributes[AttrCardApproverCount])
	}
	if s.Attributes[AttrCardDualControl] != true {
		t.Fatalf("dual-control no span incorrecto: %v", s.Attributes[AttrCardDualControl])
	}
	// Nenhum atributo pode transportar o preview, o resource ou identidades.
	for k, v := range s.Attributes {
		str, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(str, card.Preview) || (card.Resource != "" && strings.Contains(str, card.Resource)) {
			t.Fatalf("atributo %q vazou preview/resource: %q", k, str)
		}
		if strings.Contains(str, "approver-a") || strings.Contains(str, "approver-b") {
			t.Fatalf("atributo %q vazou identidade de aprovador: %q", k, str)
		}
	}
}
