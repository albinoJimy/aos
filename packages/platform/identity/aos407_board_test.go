package identity

// AOS-407 — o board de soberania vai assinado no token NHI, é herdado pelos filhos e chega ao
// rm.Principal pelo hook de identidade (DEF-909: até aqui o hook apagava-o).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

func TestAOS407_BoardAssinadoChegaAoPrincipal(t *testing.T) {
	iss, pub := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	tok, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		Board:         "board:prod",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok.Claims.Board != "board:prod" {
		t.Fatalf("claims.Board = %q", tok.Claims.Board)
	}
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	p, err := v.Verify(ctx, tok.Compact)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Board != "board:prod" {
		t.Fatalf("Principal.Board = %q, quero board:prod", p.Board)
	}
}

func TestAOS407_FilhoHerdaOBoardDoPai(t *testing.T) {
	iss, pub := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	root, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		Board:         "board:prod",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	child, err := iss.IssueChild(ctx, root.Compact, ChildRequest{AgentID: "planner", AgentClass: "planner", Authority: []string{"cap:http.get"}})
	if err != nil {
		t.Fatalf("IssueChild: %v", err)
	}
	grand, err := iss.IssueChild(ctx, child.Compact, ChildRequest{AgentID: "worker", AgentClass: "worker", Authority: []string{"cap:http.get"}})
	if err != nil {
		t.Fatalf("IssueChild neto: %v", err)
	}
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	p, err := v.Verify(ctx, grand.Compact)
	if err != nil {
		t.Fatalf("Verify neto: %v", err)
	}
	if p.Board != "board:prod" {
		t.Fatalf("o neto tem de herdar o board da raiz: %q", p.Board)
	}
}

// TestAOS407_BoardAdulteradoInvalidaAAssinatura — o board é uma afirmação de soberania: mudá-lo
// no token tem de partir a assinatura, não passar a outra fronteira.
func TestAOS407_BoardAdulteradoInvalidaAAssinatura(t *testing.T) {
	iss, pub := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	tok, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		Board: "board:prod", UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	partes := strings.Split(tok.Compact, ".")
	bruto, err := base64.RawURLEncoding.DecodeString(partes[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(bruto, &claims); err != nil {
		t.Fatal(err)
	}
	claims["board"] = "board:outra-fronteira"
	adulterado, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	partes[1] = base64.RawURLEncoding.EncodeToString(adulterado)
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	if _, err := v.Verify(ctx, strings.Join(partes, ".")); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("board adulterado tem de dar ErrSignatureInvalid; veio %v", err)
	}
}

// TestAOS407_TokenSemBoardContinuaAVerificar — compatibilidade: o campo é omitempty, e um token
// sem board verifica com board vazio (é o PDP com soberania ligada que o nega, não o verifier).
func TestAOS407_TokenSemBoardContinuaAVerificar(t *testing.T) {
	iss, pub := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	tok, err := iss.Issue(ctx, IssueRequest{UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator", UserAuthority: []string{"cap:http.get"}})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if strings.Contains(string(mustDecodeClaims(t, tok.Compact)), `"board"`) {
		t.Fatal("um token sem board não pode serializar o campo (bytes de sempre)")
	}
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	p, err := v.Verify(ctx, tok.Compact)
	if err != nil || p.Board != "" {
		t.Fatalf("token sem board: principal=%+v err=%v", p, err)
	}
}

type aos407EspiaDoPrincipal struct{ board *string }

func (aos407EspiaDoPrincipal) Name() string { return "espia" }

func (h aos407EspiaDoPrincipal) Evaluate(_ context.Context, call *rm.Call) (rm.HookResult, error) {
	*h.board = call.Principal.Board
	return rm.HookResult{Decision: rm.HookAllow}, nil
}

// TestAOS407_IdentityCheckPropagaOBoard — FALHA-ANTES: o hook substituía o rm.Principal sem Board.
func TestAOS407_IdentityCheckPropagaOBoard(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	iss, pub := newIssuer(t, researcherClasses(), store)
	ctx := context.Background()
	tok, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "agt-1", AgentClass: "researcher",
		Board: "board:prod", UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	var visto string
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	m := rm.New(rm.WithHooks(NewIdentityCheck(v), aos407EspiaDoPrincipal{board: &visto}), rm.WithEventSink(rm.NewEventStoreSink(store)))
	if err := m.Register("tool.fetch", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatal(err)
	}
	call := rmCall()
	call.Credential = tok.Compact
	if _, err := m.Mediate(ctx, call); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if visto != "board:prod" {
		t.Fatalf("o hook seguinte à identidade viu Board=%q, quero board:prod", visto)
	}

	// O evento de emissão regista o board (auditoria do binding).
	evs, err := store.Read(ctx, streamIdentity, 1)
	if err != nil {
		t.Fatalf("ler o stream de identidade: %v", err)
	}
	achou := false
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), `"jti":"`+tok.Claims.JTI+`"`) {
			achou = true
			if !strings.Contains(string(ev.Payload), `"board":"board:prod"`) {
				t.Fatalf("o identity.nhi.issued tem de levar o board: %s", ev.Payload)
			}
		}
	}
	if !achou {
		t.Fatal("o evento de emissão do token não foi encontrado")
	}
}

func mustDecodeClaims(t *testing.T, compact string) []byte {
	t.Helper()
	partes := strings.Split(compact, ".")
	bruto, err := base64.RawURLEncoding.DecodeString(partes[1])
	if err != nil {
		t.Fatal(err)
	}
	return bruto
}
