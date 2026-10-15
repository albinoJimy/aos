package attribution_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
)

// aos530HashDeSempre é o EntryHash do selo de atribuição de um turno do executor, medido com o
// código ANTERIOR ao AOS-530 (attribution.go do commit e957a874). Um turno — sem tool de origem —
// tem de continuar a selar estes bytes: o campo novo só muda o selo de uma chamada feita por uma
// tool. Este ficheiro não usa o campo novo de propósito, para que o mesmo teste compile e corra
// sobre o código de antes.
const aos530HashDeSempre = "2d63db19b304a0a288dda4ec546401b50df5b30d3fa69f1030c94c0320392c66"

func TestAOS530_SeloDeUmTurno_ByteIgualAoDeAntes(t *testing.T) {
	t.Parallel()
	rec := attribution.NewRecorder(audit.NewMemStore())
	selado, err := rec.Seal(context.Background(), attribution.Record{
		UserID: "user-1", AgentID: "agent-1", AgentClass: "executor", HumanRoot: "user-1",
		DelegationChain: []attribution.Hop{{Sub: "user-1", ActAs: "agent-1"}},
		Model:           "gpt-4o", Region: "eu-west", KeyID: "acct-eu-1", Operation: "chat",
		PolicyVersion: "pv-1", RunID: "run-1", StepID: "step-000001",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got := hex.EncodeToString(selado.EntryHash); got != aos530HashDeSempre {
		t.Fatalf("o selo de um turno mudou: %s (antes %s)", got, aos530HashDeSempre)
	}
	if len(selado.Obligations) != 0 {
		t.Fatalf("um turno do executor nao leva obligation de origem: %+v", selado.Obligations)
	}
}
