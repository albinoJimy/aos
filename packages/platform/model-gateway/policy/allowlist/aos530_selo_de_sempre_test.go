package allowlist_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// aos530GovHashDeSempre é o EntryHash do selo de governação por chamada de um turno do executor,
// medido com o código ANTERIOR à tool de origem neste registo (audit.go do commit 6b577000). Um
// turno — sem tool de origem — tem de continuar a selar estes bytes: o campo novo só muda o selo de
// uma chamada feita por uma tool (AOS-530). Este ficheiro não usa o campo novo de propósito, para
// que o mesmo teste compile e corra sobre o código de antes.
const aos530GovHashDeSempre = "61cf36ac29940f5541541ba12a4e957c2809c1913a1117034a723ef41477d9c8"

func TestAOS530_SeloDeGovernacaoDeUmTurno_ByteIgualAoDeAntes(t *testing.T) {
	t.Parallel()
	rec := allowlist.NewRecorder(audit.NewMemStore())
	selado, err := rec.Seal(context.Background(), allowlist.GovRecord{
		Board: "board-eu", PrincipalUser: "user-1", PrincipalAgent: "agent-1", AgentClass: "executor", HumanRoot: "user-1",
		DelegationChain: []allowlist.Hop{{Sub: "user-1", ActAs: "agent-1"}},
		Model:           "gpt-4o", Region: "eu", Decision: audit.DecisionAllow, Reason: "permitido",
		PolicyVersion: "gw-allowlist/v1#abc", Operation: "chat", RunID: "run-1", StepID: "step-000001",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got := hex.EncodeToString(selado.EntryHash); got != aos530GovHashDeSempre {
		t.Fatalf("o selo de governacao de um turno mudou: %s (antes %s)", got, aos530GovHashDeSempre)
	}
	if len(selado.Obligations) != 1 || selado.Obligations[0].Type != "governance" {
		t.Fatalf("um turno do executor so leva a obligation de governacao: %+v", selado.Obligations)
	}
}
