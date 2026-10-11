package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-521 — a classe da falha de um turno, lida dos TIPOS do erro (embrulhado, como chega ao nó).
func TestAOS521_ClassifyTurnFailure(t *testing.T) {
	embrulhar := func(err error) error { return fmt.Errorf("agent-runtime: turno 3: %w", fmt.Errorf("gateway: %w", err)) }
	status := func(n int) error { return embrulhar(&adapters.StatusError{Provider: "openai", Status: n}) }
	for _, c := range []struct {
		nome string
		err  error
		quer string
	}{
		{"sem erro", nil, ""},
		{"500", status(500), TurnFailureProviderStatus},
		{"503", status(503), TurnFailureProviderStatus},
		{"400", status(400), TurnFailureProviderStatus},
		{"401", status(401), TurnFailureProviderStatus},
		{"404", status(404), TurnFailureProviderStatus},
		{"408", status(408), TurnFailureProviderStatus},
		{"504", status(504), TurnFailureProviderStatus},
		{"429 e o limite de ritmo, nao um erro do provider", status(429), TurnFailureRateLimited},
		{"o status sem corpo e o mesmo status", embrulhar((&adapters.StatusError{Provider: "openai", Status: 502}).SemCorpo()), TurnFailureProviderStatus},
		{"resposta sem choices", embrulhar(ErrRespostaSemChoices), TurnFailureResponseRejected},
		{"content numa forma que nao se le", embrulhar(port.ErrContentForm), TurnFailureResponseRejected},
		{"corpo que nao e JSON", embrulhar(&json.SyntaxError{}), TurnFailureResponseRejected},
		{"rota diferente em imposicao", embrulhar(&RouteVarianceError{Check: port.RouteCheckDifferent, Cause: RouteCauseModelDifferent}), TurnFailureRouteDifferent},
		{"rota nao reportada em imposicao nao e rota diferente", embrulhar(&RouteVarianceError{Check: port.RouteCheckUnreported, Cause: RouteCauseModelUnreported}), ""},
		{"cancelamento", embrulhar(context.Canceled), ""},
		{"tempo esgotado sem resposta", embrulhar(context.DeadlineExceeded), ""},
		{"erro de transporte", embrulhar(errors.New("dial tcp 127.0.0.1:4000: connect: connection refused")), ""},
		{"um texto que parece um status nao e um status", embrulhar(errors.New("adapters: provider openai devolveu status 500: x")), ""},
		{"200 sem usage nao e uma resposta rejeitada", embrulhar(ErrUsageAusente), ""},
	} {
		t.Run(c.nome, func(t *testing.T) {
			if got := ClassifyTurnFailure(c.err); got != c.quer {
				t.Fatalf("ClassifyTurnFailure = %q; quer %q", got, c.quer)
			}
		})
	}
}
