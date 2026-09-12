package modelgateway

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// TestPortErrorCode fixa a tradução opt-in dos erros da porta C4 para os códigos de porta
// ESTÁVEIS (AOS-382): a soberania regional (allowlist) → E_REGION_DENIED; a montagem do adaptador
// (provider/base url) → E_MODEL_UNAVAILABLE; embrulhos resolvem; o não-reconhecido dá "".
func TestPortErrorCode(t *testing.T) {
	casos := []struct {
		nome string
		err  error
		quer string
	}{
		{"nil", nil, ""},
		{"modelo fora da allowlist regional", allowlist.ErrModelNotAllowed, CodeRegionDenied},
		{"allowlist embrulhado", fmt.Errorf("regiao: %w", allowlist.ErrModelNotAllowed), CodeRegionDenied},
		{"provider desconhecido", ErrUnknownProvider, CodeModelUnavailable},
		{"sem base url", ErrNoBaseURL, CodeModelUnavailable},
		{"base url embrulhado", fmt.Errorf("adaptador: %w", ErrNoBaseURL), CodeModelUnavailable},
		{"nao reconhecido", errors.New("qualquer outro"), ""},
	}
	for _, c := range casos {
		if got := PortErrorCode(c.err); got != c.quer {
			t.Errorf("%s: PortErrorCode = %q, quero %q", c.nome, got, c.quer)
		}
	}
}
