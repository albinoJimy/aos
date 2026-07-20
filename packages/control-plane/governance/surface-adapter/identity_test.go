package surfaceadapter

import (
	"context"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestIdentidade_ReversivelChegaAoGate (AC3): a decisão recolhida numa superfície
// (Telegram, canal capaz para reversível) é DEVOLVIDA ao MESMO gate HITL (AOS-095) com
// a IDENTIDADE do aprovador. O adaptador não decide/assina — compõe o coletor canónico,
// que entrega ao Channel real (que assina/sela). A selagem no audit prova a devolução.
func TestIdentidade_ReversivelChegaAoGate(t *testing.T) {
	ch, store := realChannel(t,
		[]approvalStep{{"operador-slack", true}},
		map[string]byte{"operador-slack": 1},
	)
	coll, err := approvalcard.NewDualControlCollector(ch)
	if err != nil {
		t.Fatalf("NewDualControlCollector: %v", err)
	}
	r, _ := RendererFor(PlatformTelegram)
	auth, err := NewSurfaceAuthorizer(r, coll)
	if err != nil {
		t.Fatalf("NewSurfaceAuthorizer: %v", err)
	}

	dec, rc, err := auth.Authorize(context.Background(), reversibleCard(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if rc.Degraded {
		t.Fatal("reversivel em telegram NAO devia degradar")
	}
	if !dec.Authorized {
		t.Fatalf("esperava autorizado: %s", dec.Reason)
	}
	if len(dec.Approvers) != 1 || dec.Approvers[0] != "operador-slack" {
		t.Fatalf("identidade do aprovador nao propagou ao gate: %+v", dec.Approvers)
	}
	// O Channel (gate) SELOU a decisão — prova de que o adaptador devolveu, não decidiu.
	head, err := store.Head(context.Background(), "hitl:agent-1")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head < 1 {
		t.Fatalf("esperava >=1 decisao selada pelo gate, head=%d", head)
	}
}

// TestIdentidade_IrreversivelDoisAprovadoresChegamAoGate (AC3): num canal CAPAZ
// (desktop), um card irreversível recolhe DOIS aprovadores DISTINTOS e devolve-os ao
// gate com as identidades correctas.
func TestIdentidade_IrreversivelDoisAprovadoresChegamAoGate(t *testing.T) {
	ch, store := realChannel(t,
		[]approvalStep{{"aprovador-a", true}, {"aprovador-b", true}},
		map[string]byte{"aprovador-a": 1, "aprovador-b": 2},
	)
	coll, _ := approvalcard.NewDualControlCollector(ch)
	r, _ := RendererFor(PlatformDesktop)
	auth, _ := NewSurfaceAuthorizer(r, coll)

	dec, rc, err := auth.Authorize(context.Background(), irreversibleCard(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if rc.Degraded {
		t.Fatal("desktop (capaz) NAO devia degradar")
	}
	if !dec.Authorized {
		t.Fatalf("dois distintos deviam autorizar: %s", dec.Reason)
	}
	if len(dec.Approvers) != 2 || dec.Approvers[0] == dec.Approvers[1] {
		t.Fatalf("esperava dois aprovadores distintos no gate: %+v", dec.Approvers)
	}
	if head, _ := store.Head(context.Background(), "hitl:agent-1"); head < 2 {
		t.Fatalf("esperava >=2 decisoes seladas pelo gate, head=%d", head)
	}
}

// TestIdentidade_AdaptadorDevolveEfeitoResolvido (AC3): o adaptador DEVOLVE ao gate um
// [risk.ConfirmationRequest] com o efeito concreto resolvido (preview/capability/
// resource) e o Requester como Principal (base do 4-eyes) — via o coletor canónico. O
// adaptador não assina; só traduz a apresentação e transporta a decisão de volta.
func TestIdentidade_AdaptadorDevolveEfeitoResolvido(t *testing.T) {
	spy := &spyChannel{resps: []risk.ConfirmationResponse{{Approved: true, Approver: "operador-desktop"}}}
	coll, _ := approvalcard.NewDualControlCollector(spy)
	r, _ := RendererFor(PlatformDesktop)
	auth, _ := NewSurfaceAuthorizer(r, coll)

	card := reversibleCard(t)
	dec, _, err := auth.Authorize(context.Background(), card)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized || len(dec.Approvers) != 1 || dec.Approvers[0] != "operador-desktop" {
		t.Fatalf("decisao/identidade nao chegou correcta ao gate: %+v", dec)
	}
	if len(spy.seen) != 1 {
		t.Fatalf("esperava 1 pedido devolvido ao gate, %d", len(spy.seen))
	}
	got := spy.seen[0]
	if got.Preview != card.Preview || got.Capability != card.Capability || got.Resource != card.Resource {
		t.Fatalf("pedido devolvido nao carrega o efeito resolvido: %+v", got)
	}
	if got.Principal != card.Requester {
		t.Fatalf("Principal devolvido devia ser o Requester: %q != %q", got.Principal, card.Requester)
	}
}
