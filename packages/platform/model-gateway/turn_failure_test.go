package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
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
		{"401 e da conta (credencial), nao do modelo", status(401), TurnFailureAccountAccess},
		{"403 e da conta (permissao), nao do modelo", status(403), TurnFailureAccountAccess},
		{"402", status(402), TurnFailureProviderStatus},
		{"404", status(404), TurnFailureProviderStatus},
		{"408", status(408), TurnFailureProviderStatus},
		{"504", status(504), TurnFailureProviderStatus},
		{"429 e o limite de ritmo, nao um erro do provider", status(429), TurnFailureRateLimited},
		{"o status sem corpo e o mesmo status", embrulhar((&adapters.StatusError{Provider: "openai", Status: 502}).SemCorpo()), TurnFailureProviderStatus},
		{"resposta sem choices", embrulhar(ErrRespostaSemChoices), TurnFailureResponseRejected},
		{"content numa forma que nao se le", embrulhar(port.ErrContentForm), TurnFailureResponseRejected},
		{"corpo que nao e JSON", embrulhar(&json.SyntaxError{}), TurnFailureResponseRejected},
		{"rota diferente em imposicao", embrulhar(&RouteVarianceError{Check: port.RouteCheckDifferent, Cause: RouteCauseModelDifferent}), TurnFailureRouteDifferent},
		{"rota nao reportada em imposicao nao e rota diferente", embrulhar(&RouteVarianceError{Check: port.RouteCheckUnreported, Cause: RouteCauseModelUnreported}), TurnFailureRouteUnreported},
		{"cancelamento", embrulhar(context.Canceled), TurnFailureTransport},
		{"tempo esgotado sem resposta", embrulhar(context.DeadlineExceeded), TurnFailureTransport},
		{"erro de transporte do cliente HTTP", embrulhar(&url.Error{Op: "Post", URL: "http://x", Err: errors.New("connection refused")}), TurnFailureTransport},
		{"erro de rede", embrulhar(&net.OpError{Op: "dial", Err: errors.New("connection refused")}), TurnFailureTransport},
		{"negacao de um estagio da pipeline", embrulhar(&pipeline.StageError{Stage: "authn", Err: errors.New("token expirado")}), TurnFailureGatewayDenied},
		{"perfil fixado que o no nao tem", embrulhar(&PinnedRouteProfileError{Cause: PinnedCauseModelNotServed}), TurnFailurePinnedProfile},
		{"estado que a rota exige e nao ha", embrulhar(&StateReturnError{Cause: "x"}), TurnFailurePinnedProfile},
		// AOS-530: as recusas de imagem e da tool de origem nunca contam como falha do modelo.
		{"imagem para rota sem a capacidade", embrulhar(&RouteWithoutImageError{Cause: "perfil_sem_imagem"}), TurnFailurePinnedProfile},
		{"imagem para rota sem perfil", embrulhar(&RouteWithoutImageError{Cause: "rota_sem_perfil"}), TurnFailurePinnedProfile},
		{"imagem em streaming", embrulhar(&RouteWithoutImageError{Cause: "imagem_em_streaming"}), TurnFailurePinnedProfile},
		{"parte de imagem invalida", embrulhar(&port.ImagePartError{Cause: port.ImageCauseTotalTooLarge}), TurnFailureGatewayDenied},
		{"tool de origem invalida", embrulhar(port.ErrBadOriginTool), TurnFailureGatewayDenied},
		// SEM CLASSE E SEM EXCLUSAO: quem conta trata como falha nao classificada.
		{"um texto que parece um status nao e um status", embrulhar(errors.New("adapters: provider openai devolveu status 500: x")), ""},
		{"200 sem usage", embrulhar(&costError{err: ErrUsageAusente}), ""},
		{"200 sem usage dentro de um estagio da pipeline", embrulhar(&pipeline.StageError{Stage: "metering", Err: &costError{err: ErrUsageAusente}}), ""},
		{"falha da atribuicao depois da resposta", embrulhar(&attributionError{err: errors.New("worm")}), ""},
	} {
		t.Run(c.nome, func(t *testing.T) {
			got := ClassifyTurnFailure(c.err)
			if got != c.quer {
				t.Fatalf("ClassifyTurnFailure = %q; quer %q", got, c.quer)
			}
			// As duas familias nao se misturam: uma classe do modelo nunca e uma exclusao.
			doModelo := got == TurnFailureResponseRejected || got == TurnFailureProviderStatus || got == TurnFailureRouteDifferent
			if TurnFailureNotOfTheModel(got) == (doModelo || got == "") {
				t.Fatalf("a classe %q esta nas duas familias, ou em nenhuma", got)
			}
		})
	}
}
