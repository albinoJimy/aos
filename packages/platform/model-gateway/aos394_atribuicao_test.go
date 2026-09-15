package modelgateway_test

// AOS-394 — o registo de ATRIBUIÇÃO (AOS-057) leva também o passo do run, não só o run. É uma
// segunda cadeia WORM (partição por raiz humana), distinta da governação da allowlist; a revisão
// adversarial notou que a mudança em Gateway.attribute não tinha teste nenhum.

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/keypool"
)

func TestAOS394_Atribuicao_LevaORunEOPasso(t *testing.T) {
	t.Parallel()
	h := newAOS057Gateway(t, keypool.NewPool(keypool.Account{KeyID: "acct-a", LimitRPM: 100}))
	tok := h.token(t, "alice", "agent-1")

	if _, err := h.gw.Chat(context.Background(), port.ChatRequest{
		Model:     "gpt-x",
		Messages:  []port.Message{{Role: port.RoleUser, Content: "oi"}},
		Principal: tok,
		Region:    "eu",
		RunID:     "run-aos394-atrib",
		StepID:    "step-000006",
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	recs := *h.recs
	if len(recs) != 1 {
		t.Fatalf("esperava 1 registo de atribuição, veio %d", len(recs))
	}
	if recs[0].RunID != "run-aos394-atrib" || recs[0].StepID != "step-000006" {
		t.Fatalf("atribuição com RunID=%q StepID=%q; quero run-aos394-atrib/step-000006", recs[0].RunID, recs[0].StepID)
	}
}
