package allowlist_test

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// AOS-530: uma chamada feita por uma tool sela a tool de origem numa obligation própria, a seguir à
// de governação; o resto do registo é o de um turno.
func TestAOS530_SeloDeGovernacaoComToolDeOrigem(t *testing.T) {
	t.Parallel()
	rec := allowlist.NewRecorder(audit.NewMemStore())
	selado, err := rec.Seal(context.Background(), allowlist.GovRecord{
		Board: "board-eu", PrincipalUser: "user-1", PrincipalAgent: "agent-1", Model: "ocr-visao", Region: "eu",
		Decision: audit.DecisionAllow, Reason: "permitido", Operation: "chat", RunID: "run-1", StepID: "step-000001-tool-1",
		OriginTool: "ocr_read", Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(selado.Obligations) != 2 || selado.Obligations[0].Type != "governance" ||
		selado.Obligations[1].Type != allowlist.ObligationOrigin || selado.Obligations[1].Params["tool"] != "ocr_read" ||
		len(selado.Obligations[1].Params) != 1 {
		t.Fatalf("a tool de origem tinha de ficar numa obligation propria: %+v", selado.Obligations)
	}
	if selado.RunID != "run-1" || selado.StepID != "step-000001-tool-1" || selado.Principal.NHIID != "agent-1" {
		t.Fatalf("o selo perdeu o principal, o run ou o passo: %+v", selado)
	}
}
